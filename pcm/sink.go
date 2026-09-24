package pcm

import (
	"context"
	"time"
)

// Position is an observation of a sink's playback position.
type Position struct {
	// Frames is the number of frames played or written so far.
	Frames int64
	// ObservedAt is when the position was sampled. Zero when not meaningful.
	ObservedAt time.Time
	// Latency estimates queued-but-unplayed audio. Zero means unknown.
	Latency time.Duration
	// Timed reports whether the position is backed by a real-time clock.
	// Untimed sinks may still report frames written.
	Timed bool
	// Underruns counts playback buffer underruns recovered by the sink.
	Underruns uint64
}

// Sink consumes interleaved PCM blocks in order.
//
// Implementations must not retain block data after Write returns. A Sink is
// owned by the receiver and closed exactly once.
type Sink interface {
	// Write queues block for playback, honoring ctx cancellation and bound
	// blocking as documented by the implementation.
	Write(ctx context.Context, block Block) error

	// Flush discards queued-but-unplayed audio and starts a new generation so
	// samples from before a seek can never become audible afterward.
	Flush(ctx context.Context) error

	// Position reports the latest playback position observation.
	Position() Position

	// Close releases resources. Writes and Flushes after Close fail.
	Close() error
}

// Factory creates one Sink for a playback stream in the given format.
type Factory interface {
	Open(ctx context.Context, format Format) (Sink, error)
}
