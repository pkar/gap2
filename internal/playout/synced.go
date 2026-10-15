package playout

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkar/gap2/pcm"
)

// sleepChunk bounds how long a single timer wait lasts so context cancellation
// is observed promptly while still sleeping in coarse, low-overhead chunks.
const sleepChunk = 10 * time.Millisecond

// alignTolerance is the deadband for output alignment, which absorbs device
// position quantization and timer slack instead of chasing every observation.
const alignTolerance = 2 * time.Millisecond

// maxAhead bounds how far in the future a block's deadline may be. Blocks are
// handled in order, so the next block is normally due within a fraction of a
// second, and a new anchor schedules its first frame shortly after it
// arrives. A deadline much further out comes from a packet of an earlier RTP
// epoch that a flush did not remove, such as the previous track's tail held
// across a track change. Waiting for it would silence the stream.
const maxAhead = 10 * time.Second

// Synced writes PCM blocks to an underlying sink at the presentation times
// mapped from their RTP timestamps. It is the PTP-synchronized scheduling
// layer: mapFrame converts a source frame timestamp into a local monotonic
// nanosecond deadline (typically ptp.Clock.FrameLocalTime), and now supplies
// the matching monotonic clock (typically ptp.MonotonicNanos).
//
// A frame with no mapped deadline (for example before a playback anchor and
// clock offset are established) is written immediately, so audio still flows
// even while synchronization is being acquired.
//
// While paused, blocks wait rather than being discarded. A sender resumes or
// starts a new track by sending its first packets before the anchor that
// schedules them; dropping those packets would start the track late. Flush
// discards held blocks.
type Synced struct {
	mu         sync.Mutex
	started    bool // the sink has received a block since the last flush; guarded by mu
	paused     atomic.Bool
	generation atomic.Uint64
	offset     atomic.Int64
	padded     atomic.Int64
	trimmed    atomic.Int64
	skew       atomic.Int64
	sink       pcm.Sink
	mapFrame   func(frame uint32) (uint64, bool)
	now        func() uint64
}

// SetOffset shifts presentation deadlines. Positive values play later;
// negative values compensate for latency downstream of the PCM device.
func (s *Synced) SetOffset(offset time.Duration) { s.offset.Store(int64(offset)) }

// Correction reports cumulative inserted/removed time and the last observed
// difference between the presentation deadline and the hardware queue tail.
func (s *Synced) Correction() (padding, trimming, skew time.Duration) {
	return time.Duration(s.padded.Load()), time.Duration(s.trimmed.Load()), time.Duration(s.skew.Load())
}

func (s *Synced) deadline(frame uint32) (uint64, bool) {
	d, ok := s.mapFrame(frame)
	offset := s.offset.Load()
	if offset < 0 && d < uint64(-offset) {
		return 0, ok
	}
	return uint64(int64(d) + offset), ok
}

// NewSynced returns a Synced that schedules writes onto sink. A nil mapFrame
// means frames have no deadline (every block is written immediately); a nil
// now is replaced with a zero clock, which only matters once mapFrame returns
// deadlines.
func NewSynced(sink pcm.Sink, mapFrame func(frame uint32) (uint64, bool), now func() uint64) *Synced {
	if sink == nil {
		panic("playout: nil sink")
	}
	s := &Synced{sink: sink, mapFrame: mapFrame, now: now}
	if s.mapFrame == nil {
		s.mapFrame = func(uint32) (uint64, bool) { return 0, false }
	}
	if s.now == nil {
		s.now = func() uint64 { return 0 }
	}
	return s
}

// WriteTimed writes block, whose first frame has RTP timestamp frame, at the
// deadline mapped from frame. It blocks until the deadline passes (or ctx is
// cancelled) before handing the block to the sink, and holds the block while
// playback is paused. A flush discards the block.
func (s *Synced) WriteTimed(ctx context.Context, frame uint32, block pcm.Block) error {
	generation := s.generation.Load()
	for {
		stale, err := s.wait(ctx, frame, generation)
		if err != nil {
			return err
		}
		if stale {
			s.trimmed.Add(int64(block.Duration()))
			return nil
		}
		s.mu.Lock()
		if generation != s.generation.Load() {
			s.mu.Unlock()
			return nil
		}
		if !s.paused.Load() {
			break
		}
		s.mu.Unlock() // paused after the wait ended; keep holding the block
	}
	defer s.mu.Unlock()
	if err := s.align(ctx, frame, &block); err != nil || len(block.Data) == 0 {
		return err
	}
	if err := s.sink.Write(ctx, block); err != nil {
		return err
	}
	s.started = true
	return nil
}

// wait returns when block's write time arrives, when a flush discards it
// (generation changes), or with ctx's error. While paused it keeps waiting:
// the anchor that schedules the block may not have arrived yet. It reports
// stale when the block's deadline is more than maxAhead away.
func (s *Synced) wait(ctx context.Context, frame uint32, generation uint64) (stale bool, err error) {
	for generation == s.generation.Load() {
		delay := sleepChunk
		if !s.paused.Load() {
			deadline, ok := s.deadline(frame)
			if !ok {
				return false, nil
			}
			if int64(deadline)-int64(s.now()) > int64(maxAhead) {
				return true, nil
			}
			// A hardware sink must receive samples before their presentation
			// deadline so the DMA queue can absorb scheduler and network
			// jitter. File sinks have no playback clock and retain exact
			// capture timing.
			if s.sink.Position().Timed {
				const lead = uint64(100 * time.Millisecond)
				if deadline > lead {
					deadline -= lead
				} else {
					deadline = 0
				}
			}
			remain := int64(deadline) - int64(s.now())
			if remain <= 0 {
				return false, nil
			}
			delay = min(time.Duration(remain), sleepChunk)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
		// Seek/reconnect can replace the anchor while this packet waits.
		// Recompute its deadline instead of sleeping against a stale epoch.
	}
	return false, nil
}

// align pads or trims block so it plays at its deadline. It is called with mu
// held and may leave block empty when all of it would play too late.
func (s *Synced) align(ctx context.Context, frame uint32, block *pcm.Block) error {
	due, ok := s.deadline(frame)
	if !ok || block.Format.Rate <= 0 {
		return nil
	}
	tolerance := int64(alignTolerance)
	p := s.sink.Position()
	if !p.Timed {
		// A sink without a playback clock plays each block after the ones
		// already queued, so the first block after a flush fixes the schedule
		// until the next flush. When that block's write time has already
		// passed (for example, an anchor that arrives after its presentation
		// time), drop the late audio so the stream starts on time instead of
		// staying behind. Later blocks keep the established schedule.
		if s.started {
			return nil
		}
		if late := int64(s.now()) - int64(due); late > tolerance {
			s.trim(block, time.Duration(late))
		}
		return nil
	}
	// Queuing early alone does not establish the presentation time: the
	// device may already contain audio, or may start with an empty queue.
	// Align the queue's tail to this block's deadline. Keep a small deadband
	// for device-position quantization instead of chasing every observation.
	errorNs := int64(due) - int64(s.now()) - int64(p.Latency)
	s.skew.Store(errorNs)
	if errorNs > tolerance {
		// Bound padding if an anchor changed after the scheduling wait.
		if errorNs > int64(100*time.Millisecond) {
			errorNs = int64(100 * time.Millisecond)
		}
		frames := int(errorNs * int64(block.Format.Rate) / int64(time.Second))
		padding, err := pcm.NewBlock(block.Format, frames)
		if err != nil {
			return err
		}
		if err := s.sink.Write(ctx, padding); err != nil {
			return err
		}
		s.padded.Add(int64(padding.Duration()))
	} else if errorNs < -tolerance {
		s.trim(block, time.Duration(-errorNs))
	}
	return nil
}

// trim drops the part of block that would play after its deadline: the first
// late of audio, or the whole block when late covers it.
func (s *Synced) trim(block *pcm.Block, late time.Duration) {
	if late >= block.Duration() {
		s.trimmed.Add(int64(block.Duration()))
		block.Data = block.Data[:0]
		return
	}
	frames := int(int64(late) * int64(block.Format.Rate) / int64(time.Second))
	s.trimmed.Add(int64(time.Duration(frames) * time.Second / time.Duration(block.Format.Rate)))
	block.Data = block.Data[frames*block.Format.BytesPerFrame():]
}

// Write writes block immediately, bypassing scheduling. It exists so Synced
// satisfies pcm.Sink for callers that do not carry a source timestamp.
func (s *Synced) Write(ctx context.Context, block pcm.Block) error {
	return s.sink.Write(ctx, block)
}

// Flush discards held and waiting blocks and forwards to the underlying sink.
func (s *Synced) Flush(ctx context.Context) error {
	s.generation.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = false
	return s.sink.Flush(ctx)
}

// SetPaused discards queued and waiting output without closing the stream.
// Blocks written while paused wait for SetPaused(false) or a flush.
func (s *Synced) SetPaused(ctx context.Context, paused bool) error {
	s.paused.Store(paused)
	if paused {
		return s.Flush(ctx)
	}
	return nil
}

// Position forwards to the underlying sink.
func (s *Synced) Position() pcm.Position { return s.sink.Position() }

// Close forwards to the underlying sink.
func (s *Synced) Close() error { return s.sink.Close() }
