package timing

import (
	"testing"
	"time"
)

func TestConversions(t *testing.T) {
	d := FramesToDuration(48000, 48000)
	if d != time.Second {
		t.Fatalf("duration = %v, want 1s", d)
	}
	f := DurationToFrames(d, 48000)
	if f != 48000 {
		t.Fatalf("frames = %d", f)
	}
}

func TestConversionsZeroRate(t *testing.T) {
	if FramesToDuration(100, 0) != 0 {
		t.Fatal("expected 0 duration")
	}
	if DurationToFrames(time.Second, 0) != 0 {
		t.Fatal("expected 0 frames")
	}
}
