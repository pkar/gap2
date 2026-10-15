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
		func(uint32) (uint64, bool) { return uint64(5 * time.Second), true },
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
	s := NewSynced(rs, func(uint32) (uint64, bool) { return uint64(5 * time.Second), true }, func() uint64 { return 0 })
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

// A track change pauses, flushes, then sends the new track's first packets
// before the anchor that resumes playback. Those packets must wait for the
// anchor instead of being dropped, or the new track starts late.
func TestPauseHoldsNewBlockUntilResume(t *testing.T) {
	rs := &recordingSink{}
	var now atomic.Uint64
	s := NewSynced(rs, func(uint32) (uint64, bool) { return uint64(time.Second), true }, func() uint64 { return now.Load() })
	if err := s.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.WriteTimed(context.Background(), 0, syncedBlock()) }()
	time.Sleep(30 * time.Millisecond)
	if frames, _, _ := rs.snapshot(); frames != 0 {
		t.Fatal("audio played while paused")
	}
	select {
	case err := <-done:
		t.Fatalf("paused block returned early: %v", err)
	default:
	}
	now.Store(uint64(time.Second)) // the resume anchor makes the block due
	if err := s.SetPaused(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("held block was not written after resume")
	}
	if frames, _, _ := rs.snapshot(); frames != 2 {
		t.Fatalf("frames after resume = %d, want 2", frames)
	}
}

// A packet left over from the previous track can be held through a track
// change and then mapped by the new anchor to a deadline hours away. It must
// be discarded instead of silencing the stream, and the next track's audio
// must still play.
func TestHeldStaleBlockDoesNotStallResume(t *testing.T) {
	rs := &recordingSink{}
	var anchorSet atomic.Bool
	s := NewSynced(rs, func(frame uint32) (uint64, bool) {
		if !anchorSet.Load() {
			return 0, false
		}
		if frame == 1 { // previous track's epoch under the new anchor
			return uint64(5 * time.Hour), true
		}
		return uint64(time.Second), true
	}, func() uint64 { return uint64(time.Second) })
	if err := s.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.WriteTimed(context.Background(), 1, syncedBlock()) }()
	time.Sleep(20 * time.Millisecond)
	anchorSet.Store(true)
	if err := s.SetPaused(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale block stalled playback after resume")
	}
	if frames, _, _ := rs.snapshot(); frames != 0 {
		t.Fatal("stale block was played")
	}
	if err := s.WriteTimed(context.Background(), 2, syncedBlock()); err != nil {
		t.Fatal(err)
	}
	if frames, _, _ := rs.snapshot(); frames != 2 {
		t.Fatalf("frames after stale block = %d, want 2", frames)
	}
}

// A stream's first packets arrive before its anchor. With an anchor wait they
// are held, then scheduled by the anchor instead of played unscheduled.
func TestUnanchoredBlockWaitsForAnchor(t *testing.T) {
	rs := &recordingSink{}
	var anchorSet atomic.Bool
	// The anchor puts the first frame 5ms in the past: an untimed sink starts
	// on time by trimming it.
	s := NewSynced(rs, func(uint32) (uint64, bool) {
		return uint64(time.Second), anchorSet.Load()
	}, func() uint64 { return uint64(time.Second + 5*time.Millisecond) })
	s.SetAnchorWait(time.Minute)
	format := pcm.Format{Rate: 48000, Channels: 2, Format: pcm.S16LE}
	block, err := pcm.NewBlock(format, 1024)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.WriteTimed(context.Background(), 0, block) }()
	time.Sleep(30 * time.Millisecond)
	if frames, _, _ := rs.snapshot(); frames != 0 {
		t.Fatalf("unanchored block was written early: %d frames", frames)
	}
	anchorSet.Store(true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("block did not play after the anchor arrived")
	}
	if frames, _, _ := rs.snapshot(); frames != 1024-240 {
		t.Fatalf("frames = %d, want %d", frames, 1024-240)
	}
}

// A sender that never anchors still plays once the wait expires, and later
// blocks do not wait again.
func TestUnanchoredBlocksPlayAfterAnchorWait(t *testing.T) {
	rs := &recordingSink{}
	s := NewSynced(rs, func(uint32) (uint64, bool) { return 0, false }, func() uint64 { return 0 })
	s.SetAnchorWait(40 * time.Millisecond)
	start := time.Now()
	if err := s.WriteTimed(context.Background(), 0, syncedBlock()); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 40*time.Millisecond {
		t.Fatalf("first unanchored block waited %v, want at least 40ms", waited)
	}
	start = time.Now()
	if err := s.WriteTimed(context.Background(), 1, syncedBlock()); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited > 20*time.Millisecond {
		t.Fatalf("second unanchored block waited %v after the wait expired", waited)
	}
	if frames, _, _ := rs.snapshot(); frames != 4 {
		t.Fatalf("frames = %d, want 4", frames)
	}
}

func TestFlushDiscardsHeldBlock(t *testing.T) {
	rs := &recordingSink{}
	s := NewSynced(rs, func(uint32) (uint64, bool) { return 0, true }, func() uint64 { return 0 })
	if err := s.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.WriteTimed(context.Background(), 0, syncedBlock()) }()
	time.Sleep(20 * time.Millisecond)
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaused(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("flushed block remained held")
	}
	if frames, _, _ := rs.snapshot(); frames != 0 {
		t.Fatal("flushed audio played")
	}
}

func TestPausedBlockObservesCancellation(t *testing.T) {
	s := NewSynced(&recordingSink{}, func(uint32) (uint64, bool) { return 0, true }, func() uint64 { return 0 })
	if err := s.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("paused block ignored cancellation")
	}
}

// A sink without a playback clock plays blocks back to back, so a late first
// block would keep the whole stream late. Only that first block after a flush
// is trimmed; later blocks keep the established schedule.
func TestSyncedStartsUntimedSinkOnTime(t *testing.T) {
	format := pcm.Format{Rate: 48000, Channels: 2, Format: pcm.S16LE}
	block := func(frames int) pcm.Block {
		b, err := pcm.NewBlock(format, frames)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	rs := &recordingSink{}
	// Every block is due at 1s; the clock reads 1.01s, so each is 10ms late.
	s := NewSynced(rs, func(uint32) (uint64, bool) { return uint64(time.Second), true }, func() uint64 { return uint64(1010 * time.Millisecond) })
	ctx := context.Background()
	write := func(frames int) int64 {
		t.Helper()
		before, _, _ := rs.snapshot()
		if err := s.WriteTimed(ctx, 0, block(frames)); err != nil {
			t.Fatal(err)
		}
		after, _, _ := rs.snapshot()
		return after - before
	}
	if got := write(240); got != 0 {
		t.Fatalf("expired first block wrote %d frames, want 0", got)
	}
	if got := write(1024); got != 1024-480 {
		t.Fatalf("late first block wrote %d frames, want %d", got, 1024-480)
	}
	if got := write(1024); got != 1024 {
		t.Fatalf("block after start wrote %d frames, want 1024", got)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := write(1024); got != 1024-480 {
		t.Fatalf("late first block after flush wrote %d frames, want %d", got, 1024-480)
	}
}

func TestSyncedRecomputesChangedAnchor(t *testing.T) {
	rs := &recordingSink{}
	var deadline atomic.Uint64
	deadline.Store(uint64(5 * time.Second))
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
