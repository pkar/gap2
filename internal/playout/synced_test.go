package playout

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkar/gap2/pcm"
)

func syncedBlock() pcm.Block {
	b, _ := pcm.NewBlock(testFormat, 2)
	return b
}

func TestSyncedImmediateWhenNoDeadline(t *testing.T) {
	rs := &recordingSink{}
	s := NewSynced(rs, nil, nil)

	if err := s.WriteTimed(context.Background(), 0, syncedBlock()); err != nil {
		t.Fatal(err)
	}
	if frames, _, _ := rs.snapshot(); frames != 2 {
		t.Fatalf("frames = %d, want 2", frames)
	}
}

func TestSyncedWaitsForDeadline(t *testing.T) {
	rs := &recordingSink{}
	var now atomic.Uint64
	s := NewSynced(rs,
		func(uint32) (uint64, bool) { return 100_000_000, true },
		func() uint64 { return now.Load() },
	)

	done := make(chan error, 1)
	go func() { done <- s.WriteTimed(context.Background(), 0, syncedBlock()) }()

	// The deadline is still ahead; the write must not have happened yet.
	time.Sleep(20 * time.Millisecond)
	if frames, _, _ := rs.snapshot(); frames != 0 {
		t.Fatalf("frames before deadline = %d, want 0", frames)
	}

	now.Store(100_000_000)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("WriteTimed did not complete after the deadline passed")
	}
	if frames, _, _ := rs.snapshot(); frames != 2 {
		t.Fatalf("frames after deadline = %d, want 2", frames)
	}
}

func TestSyncedCancellation(t *testing.T) {
	rs := &recordingSink{}
	s := NewSynced(rs,
		func(uint32) (uint64, bool) { return 1 << 62, true },
		func() uint64 { return 0 },
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.WriteTimed(ctx, 0, syncedBlock()) }()

	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WriteTimed did not observe cancellation")
	}
	if frames, _, _ := rs.snapshot(); frames != 0 {
		t.Fatalf("frames after cancel = %d, want 0", frames)
	}
}

func TestSyncedDelegates(t *testing.T) {
	rs := &recordingSink{}
	s := NewSynced(rs, nil, nil)

	if err := s.Write(context.Background(), syncedBlock()); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pos := s.Position(); pos.Frames != 2 {
		t.Fatalf("Position().Frames = %d, want 2", pos.Frames)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, closed := rs.snapshot(); !closed {
		t.Fatal("underlying sink not closed")
	}
}
