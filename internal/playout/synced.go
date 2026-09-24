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

// Synced writes PCM blocks to an underlying sink at the presentation times
// mapped from their RTP timestamps. It is the PTP-synchronized scheduling
// layer: mapFrame converts a source frame timestamp into a local monotonic
// nanosecond deadline (typically ptp.Clock.FrameLocalTime), and now supplies
// the matching monotonic clock (typically ptp.MonotonicNanos).
//
// A frame with no mapped deadline (for example before a playback anchor and
// clock offset are established) is written immediately, so audio still flows
// even while synchronization is being acquired.
type Synced struct {
	mu         sync.Mutex
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
// cancelled) before handing the block to the sink.
func (s *Synced) WriteTimed(ctx context.Context, frame uint32, block pcm.Block) error {
	generation := s.generation.Load()
	for {
		if s.paused.Load() || generation != s.generation.Load() {
			return nil
		}
		deadline, ok := s.deadline(frame)
		if !ok {
			break
		}
		// A hardware sink must receive samples before their presentation
		// deadline so the DMA queue can absorb scheduler and network jitter.
		// File sinks have no playback clock and retain exact capture timing.
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
			break
		}
		delay := time.Duration(remain)
		if delay > sleepChunk {
			delay = sleepChunk
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		// Seek/reconnect can replace the anchor while this packet waits.
		// Recompute its deadline instead of sleeping against a stale epoch.
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused.Load() || generation != s.generation.Load() {
		return nil
	}
	// Queuing early alone does not establish the presentation time: the
	// device may already contain audio, or may start with an empty queue.
	// Align the queue's tail to this block's deadline. Keep a small deadband
	// for device-position quantization instead of chasing every observation.
	if due, ok := s.deadline(frame); ok && block.Format.Rate > 0 {
		p := s.sink.Position()
		if p.Timed {
			errorNs := int64(due) - int64(s.now()) - int64(p.Latency)
			s.skew.Store(errorNs)
			const tolerance = int64(2 * time.Millisecond)
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
				late := time.Duration(-errorNs)
				if late >= block.Duration() {
					s.trimmed.Add(int64(block.Duration()))
					return nil // This entire block would play after its deadline.
				}
				frames := int(int64(late) * int64(block.Format.Rate) / int64(time.Second))
				s.trimmed.Add(int64(time.Duration(frames) * time.Second / time.Duration(block.Format.Rate)))
				block.Data = block.Data[frames*block.Format.BytesPerFrame():]
			}
		}
	}
	return s.sink.Write(ctx, block)
}

// Write writes block immediately, bypassing scheduling. It exists so Synced
// satisfies pcm.Sink for callers that do not carry a source timestamp.
func (s *Synced) Write(ctx context.Context, block pcm.Block) error {
	return s.sink.Write(ctx, block)
}

// Flush forwards to the underlying sink.
func (s *Synced) Flush(ctx context.Context) error {
	s.generation.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sink.Flush(ctx)
}

// SetPaused discards queued and waiting output without closing the stream.
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
