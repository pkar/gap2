package ptp

import "testing"

func TestAnchorMasterTime(t *testing.T) {
	a := Anchor{Frame: 1000, MasterNs: 5_000_000_000, Rate: 44100}

	// The anchor frame itself maps to the anchor time.
	got, ok := a.MasterTime(1000)
	if !ok || got != 5_000_000_000 {
		t.Fatalf("MasterTime(anchor) = %d, %v; want 5000000000, true", got, ok)
	}

	// One second later: 44100 frames.
	got, _ = a.MasterTime(1000 + 44100)
	if got != 6_000_000_000 {
		t.Fatalf("MasterTime(+1s) = %d, want 6000000000", got)
	}

	// One second earlier (43100 frames before anchor, wrapped in uint32).
	got, _ = a.MasterTime(uint32(4294924196))
	if got != 4_000_000_000 {
		t.Fatalf("MasterTime(-1s) = %d, want 4000000000", got)
	}
}

func TestAnchorRTPWrap(t *testing.T) {
	// Anchor near the top of the 32-bit space; a frame just after the wrap
	// must still map forward (int32 arithmetic).
	a := Anchor{Frame: 0xffff_ff00, MasterNs: 100_000_000_000, Rate: 44100}
	got, ok := a.MasterTime(0x0000_0100) // 0x100 after wrap, +512 frames
	if !ok {
		t.Fatal("MasterTime across wrap not ok")
	}
	// delta = +512 frames; 512/44100 s = ~11609977 ns.
	want := uint64(100_000_000_000 + 512*1_000_000_000/44100)
	if got != want {
		t.Fatalf("MasterTime across wrap = %d, want %d", got, want)
	}
}

func TestAnchorLocalTime(t *testing.T) {
	a := Anchor{Frame: 0, MasterNs: 10_000_000_000, Rate: 44100}
	// offset = master - local = 500ns, so local = master - 500.
	got, ok := a.LocalTime(0, 500)
	if !ok || got != 10_000_000_000-500 {
		t.Fatalf("LocalTime = %d, %v; want 9999999500, true", got, ok)
	}
}

func TestAnchorFrameAtMaster(t *testing.T) {
	a := Anchor{Frame: 1000, MasterNs: 0, Rate: 44100}
	got, ok := a.FrameAtMaster(1_000_000_000) // +1s = +44100 frames
	if !ok || got != 1000+44100 {
		t.Fatalf("FrameAtMaster = %d, %v; want %d, true", got, ok, 1000+44100)
	}
}

func TestAnchorInvalidRate(t *testing.T) {
	a := Anchor{Frame: 0, MasterNs: 0, Rate: 0}
	if _, ok := a.MasterTime(0); ok {
		t.Fatal("MasterTime ok with zero rate")
	}
	if _, ok := a.LocalTime(0, 0); ok {
		t.Fatal("LocalTime ok with zero rate")
	}
	if _, ok := a.FrameAtMaster(0); ok {
		t.Fatal("FrameAtMaster ok with zero rate")
	}
}
