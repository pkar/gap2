// Package pcmfile provides a deterministic raw-PCM sink that writes
// interleaved samples to an io.Writer. It is intended for tests, capture, and
// custom playback integrations; it is not a real-time clocked output, so its
// Position reports Timed=false.
package pcmfile

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/pkar/gap2/pcm"
)

// Sink writes raw interleaved PCM to an io.Writer.
type Sink struct {
	mu     sync.Mutex
	w      io.Writer
	format pcm.Format
	frames int64
	closed bool
}

// New returns a Sink writing format-encoded PCM to w. w is not closed unless
// it also implements io.Closer.
func New(w io.Writer, format pcm.Format) (*Sink, error) {
	if w == nil {
		return nil, errors.New("pcmfile: nil writer")
	}
	if err := format.Valid(); err != nil {
		return nil, err
	}
	return &Sink{w: w, format: format}, nil
}

// Write appends block's data to the writer. It rejects mismatched formats and
// cancelled contexts.
func (s *Sink) Write(ctx context.Context, block pcm.Block) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("pcmfile: sink closed")
	}
	if block.Format != s.format {
		return errors.New("pcmfile: block format mismatch")
	}
	if _, err := s.w.Write(block.Data); err != nil {
		return err
	}
	s.frames += int64(block.Frames())
	return nil
}

// Flush is a no-op for a file sink, which has no queued audio to discard.
func (s *Sink) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("pcmfile: sink closed")
	}
	return nil
}

// Position reports frames written. Timed is false because there is no
// real-time playback clock.
func (s *Sink) Position() pcm.Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	return pcm.Position{Frames: s.frames}
}

// Close is idempotent and closes the writer when it implements io.Closer.
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if c, ok := s.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Factory returns a pcm.Factory that opens Sinks writing format-encoded PCM
// to w.
func Factory(w io.Writer, format pcm.Format) pcm.Factory {
	return factory{w: w, format: format, pinned: true}
}

// Capture returns a pcm.Factory that writes raw PCM for whatever format the
// stream negotiates. Unlike Factory, which pins one format, Capture adopts the
// announced format so a standalone receiver can record streams at any rate or
// channel count. The writer is shared across opens; callers own its lifetime.
func Capture(w io.Writer) pcm.Factory {
	return factory{w: w}
}

type factory struct {
	w      io.Writer
	format pcm.Format
	pinned bool
}

func (f factory) Open(_ context.Context, format pcm.Format) (pcm.Sink, error) {
	if f.pinned && format != f.format {
		return nil, errors.New("pcmfile: factory format mismatch")
	}
	return New(f.w, format)
}
