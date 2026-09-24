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

type hardwareRecordingSink struct{ recordingSink }

func (s *hardwareRecordingSink) Position() pcm.Position { return pcm.Position{Timed: true} }

func TestSyncedQueuesHardwareAheadOfDeadline(t *testing.T) {
	rs := &hardwareRecordingSink{}
	s := NewSynced(rs, func(uint32) (uint64, bool) { return uint64(100 * time.Millisecond), true }, func() uint64 { return 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.WriteTimed(ctx, 0, syncedBlock()); err != nil {
		t.Fatal("hardware was not prebuffered:", err)
	}
	if frames, _, _ := rs.snapshot(); frames != 4412 {
		t.Fatalf("hardware received %d frames, want 4410 silence plus 2 audio", frames)
	}
}

type queueSink struct {
	latency time.Duration
	blocks  []pcm.Block
}

func (s *queueSink) Write(_ context.Context, b pcm.Block) error {
	b.Data = append([]byte(nil), b.Data...)
	s.blocks = append(s.blocks, b)
	return nil
}
func (s *queueSink) Position() pcm.Position {
	return pcm.Position{Timed: true, Latency: s.latency}
}
func (s *queueSink) Flush(context.Context) error { return nil }
func (s *queueSink) Close() error                { return nil }

func TestSyncedAlignsHardwareQueue(t *testing.T) {
	for _, tt := range []struct {
		name           string
		rate           int
		queue, offset  time.Duration
		padding, audio int
	}{
		{"early", 48000, 30 * time.Millisecond, 0, 960, 1024},
		{"late", 48000, 60 * time.Millisecond, 0, 0, 544},
		{"expired", 48000, 100 * time.Millisecond, 0, 0, 0},
		{"quantization", 48000, 51 * time.Millisecond, 0, 0, 1024},
		{"source rate", 44100, 20 * time.Millisecond, 0, 1323, 1024},
		{"later offset", 48000, 50 * time.Millisecond, 20 * time.Millisecond, 960, 1024},
		{"earlier offset", 48000, 50 * time.Millisecond, -10 * time.Millisecond, 0, 544},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rs := &queueSink{latency: tt.queue}
			s := NewSynced(rs, func(uint32) (uint64, bool) { return uint64(1050 * time.Millisecond), true }, func() uint64 { return uint64(time.Second) })
			s.SetOffset(tt.offset)
			b, err := pcm.NewBlock(pcm.Format{Rate: tt.rate, Channels: 2, Format: pcm.S16LE}, 1024)
			if err != nil {
				t.Fatal(err)
			}
			for i := range b.Data {
				b.Data[i] = 7
			}
			if err := s.WriteTimed(context.Background(), 0, b); err != nil {
				t.Fatal(err)
			}
			var padding, audio int
			for _, block := range rs.blocks {
				if block.Data[0] == 0 {
					padding += block.Frames()
					for _, v := range block.Data {
						if v != 0 {
							t.Fatal("non-silent padding")
						}
					}
				} else {
					audio += block.Frames()
					for _, v := range block.Data {
						if v != 7 {
							t.Fatal("audio corrupted")
						}
					}
				}
			}
			if padding != tt.padding || audio != tt.audio {
				t.Fatalf("padding/audio = %d/%d, want %d/%d", padding, audio, tt.padding, tt.audio)
			}
		})
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

func TestPauseDiscardsWaitingBlock(t *testing.T) {
	rs := &recordingSink{}
	s := NewSynced(rs, func(uint32) (uint64, bool) { return 1 << 62, true }, func() uint64 { return 0 })
	done := make(chan error, 1)
	go func() { done <- s.WriteTimed(context.Background(), 0, syncedBlock()) }()
	time.Sleep(20 * time.Millisecond)
	if err := s.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("paused frame remained waiting")
	}
	if frames, _, _ := rs.snapshot(); frames != 0 {
		t.Fatal("old audio played after pause")
	}
}

func TestSyncedRecomputesChangedAnchor(t *testing.T) {
	rs := &recordingSink{}
	var deadline atomic.Uint64
	deadline.Store(1 << 62)
	s := NewSynced(rs, func(uint32) (uint64, bool) { return deadline.Load(), true }, func() uint64 { return 0 })
	done := make(chan error, 1)
	go func() { done <- s.WriteTimed(context.Background(), 0, syncedBlock()) }()
	time.Sleep(20 * time.Millisecond)
	deadline.Store(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("playback remained stuck on the old anchor")
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
