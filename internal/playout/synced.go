package playout

import (
	"context"
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
	sink     pcm.Sink
	mapFrame func(frame uint32) (uint64, bool)
	now      func() uint64
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
	if deadline, ok := s.mapFrame(frame); ok {
		if err := s.waitUntil(ctx, deadline); err != nil {
			return err
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
func (s *Synced) Flush(ctx context.Context) error { return s.sink.Flush(ctx) }

// Position forwards to the underlying sink.
func (s *Synced) Position() pcm.Position { return s.sink.Position() }

// Close forwards to the underlying sink.
func (s *Synced) Close() error { return s.sink.Close() }

// waitUntil blocks until the monotonic clock reaches until, sleeping in
// bounded chunks so ctx cancellation is honored within sleepChunk.
func (s *Synced) waitUntil(ctx context.Context, until uint64) error {
	for {
		remain := int64(until) - int64(s.now())
		if remain <= 0 {
			return nil
		}
		d := time.Duration(remain)
		if d > sleepChunk {
			d = sleepChunk
		}
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
