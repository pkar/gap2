package playout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pkar/gap2/pcm"
)

var testFormat = pcm.Format{Rate: 44100, Channels: 2, Format: pcm.S16LE}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type recordingSink struct {
	mu       sync.Mutex
	frames   int64
	flushes  int
	closed   bool
	writeErr error
}

func (s *recordingSink) Write(_ context.Context, b pcm.Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.writeErr != nil {
		return s.writeErr
	}
	s.frames += int64(b.Frames())
	return nil
}

func (s *recordingSink) Flush(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.flushes++
	return nil
}

func (s *recordingSink) Position() pcm.Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	return pcm.Position{Frames: s.frames}
}

func (s *recordingSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return nil
}

func (s *recordingSink) snapshot() (frames int64, flushes int, closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames, s.flushes, s.closed
}

func block(frames int) pcm.Block {
	b, err := pcm.NewBlock(testFormat, frames)
	if err != nil {
		panic(err)
	}
	return b
}

func TestNewValidation(t *testing.T) {
	sink := &recordingSink{}
	if _, err := New(nil, testFormat, Config{}); err == nil {
		t.Fatal("expected error for nil sink")
	}
	badFormat := testFormat
	badFormat.Rate = 0
	if _, err := New(sink, badFormat, Config{}); err == nil {
		t.Fatal("expected error for invalid format")
	}
	if _, err := New(sink, testFormat, Config{Target: time.Second, Max: time.Millisecond}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestAdvanceAutoStartAndSchedule(t *testing.T) {
	sink := &recordingSink{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	p, err := New(sink, testFormat, Config{Target: 20 * time.Millisecond, Max: 100 * time.Millisecond, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx := context.Background()
	if err := p.Push(ctx, block(441)); err != nil {
		t.Fatal(err)
	}
	if err := p.Advance(clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := sink.snapshot(); got != 0 {
		t.Fatalf("wrote %d frames before target, want 0", got)
	}
	if p.State() != StateBuffering {
		t.Fatalf("state = %v, want buffering", p.State())
	}

	if err := p.Push(ctx, block(441)); err != nil {
		t.Fatal(err)
	}
	if err := p.Advance(clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := sink.snapshot(); got != 441 {
		t.Fatalf("wrote %d frames, want 441", got)
	}
	if p.State() != StatePlaying {
		t.Fatalf("state = %v, want playing", p.State())
	}
	pos := p.Position()
	if pos.PlayedFrames != 441 || pos.BufferedFrames != 441 {
		t.Fatalf("position = %+v, want played 441 buffered 441", pos)
	}

	clock.set(time.Unix(1000, 10*int64(time.Millisecond)))
	if err := p.Advance(clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := sink.snapshot(); got != 882 {
		t.Fatalf("wrote %d frames, want 882", got)
	}
	if got := p.Position(); got.PlayedFrames != 882 || got.BufferedFrames != 0 {
		t.Fatalf("position = %+v, want played 882 buffered 0", got)
	}
}

func TestPauseResume(t *testing.T) {
	sink := &recordingSink{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	p, err := New(sink, testFormat, Config{Target: 20 * time.Millisecond, Max: 100 * time.Millisecond, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx := context.Background()
	if err := p.Push(ctx, block(441)); err != nil {
		t.Fatal(err)
	}
	if err := p.Push(ctx, block(441)); err != nil {
		t.Fatal(err)
	}
	if err := p.Advance(clock.Now()); err != nil {
		t.Fatal(err)
	}
	p.Pause()
	clock.set(time.Unix(1000, 30*int64(time.Millisecond)))
	if err := p.Advance(clock.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := sink.snapshot(); got != 441 {
		t.Fatalf("wrote %d frames while paused, want 441", got)
	}
	if p.State() != StatePaused {
		t.Fatalf("state = %v, want paused", p.State())
	}

	resumeAt := time.Unix(1000, 100*int64(time.Millisecond))
	clock.set(resumeAt)
	p.Resume(resumeAt)
	if err := p.Advance(resumeAt); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := sink.snapshot(); got != 882 {
		t.Fatalf("wrote %d frames after resume, want 882", got)
	}
}

func TestFlushResets(t *testing.T) {
	sink := &recordingSink{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	p, err := New(sink, testFormat, Config{Target: 20 * time.Millisecond, Max: 100 * time.Millisecond, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx := context.Background()
	if err := p.Push(ctx, block(441)); err != nil {
		t.Fatal(err)
	}
	if err := p.Push(ctx, block(441)); err != nil {
		t.Fatal(err)
	}
	if err := p.Advance(clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	_, flushes, _ := sink.snapshot()
	if flushes != 1 {
		t.Fatalf("flushes = %d, want 1", flushes)
	}
	pos := p.Position()
	if pos.PlayedFrames != 0 || pos.BufferedFrames != 0 || !pos.Presentation.IsZero() {
		t.Fatalf("position after flush = %+v", pos)
	}
	if p.State() != StateIdle {
		t.Fatalf("state = %v, want idle", p.State())
	}
}

func TestPushBackpressure(t *testing.T) {
	sink := &recordingSink{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	p, err := New(sink, testFormat, Config{Target: 20 * time.Millisecond, Max: 100 * time.Millisecond, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx := context.Background()
	// 100 ms at 44.1 kHz = 4410 frames, exactly filling Max.
	if err := p.Push(ctx, block(4410)); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- p.Push(ctx, block(441)) }()

	select {
	case err := <-done:
		t.Fatalf("push completed early with %v, want backpressure", err)
	case <-time.After(20 * time.Millisecond):
	}

	if err := p.Advance(clock.Now()); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("push after drain = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("push remained blocked after draining")
	}
}

func TestPushRejectsBlockLargerThanBuffer(t *testing.T) {
	p, err := New(&recordingSink{}, testFormat, Config{Target: 20 * time.Millisecond, Max: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Push(context.Background(), block(4411)); !errors.Is(err, ErrBlockTooLarge) {
		t.Fatalf("push = %v, want ErrBlockTooLarge", err)
	}
}

func TestSinkErrorPropagates(t *testing.T) {
	sink := &recordingSink{writeErr: errors.New("sink boom")}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	p, err := New(sink, testFormat, Config{Target: 20 * time.Millisecond, Max: 100 * time.Millisecond, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Push(context.Background(), block(882)); err != nil {
		t.Fatal(err)
	}
	if err := p.Advance(clock.Now()); !errors.Is(err, sink.writeErr) {
		t.Fatalf("advance = %v, want sink error", err)
	}
	if err := p.Push(context.Background(), block(1)); !errors.Is(err, sink.writeErr) {
		t.Fatalf("push after sink error = %v, want sink error", err)
	}
}

func TestCloseAndMismatch(t *testing.T) {
	sink := &recordingSink{}
	p, err := New(sink, testFormat, Config{Target: 20 * time.Millisecond, Max: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	other := testFormat
	other.Channels = 1
	if err := p.Push(context.Background(), pcm.Block{Format: other, Data: make([]byte, 2)}); !errors.Is(err, ErrFormatMismatch) {
		t.Fatalf("err = %v, want ErrFormatMismatch", err)
	}

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Push(context.Background(), block(1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("push after close = %v, want ErrClosed", err)
	}
	if err := p.Advance(time.Now()); !errors.Is(err, ErrClosed) {
		t.Fatalf("advance after close = %v, want ErrClosed", err)
	}
	if p.State() != StateClosed {
		t.Fatalf("state = %v, want closed", p.State())
	}
}
