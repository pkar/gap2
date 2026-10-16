package ptp

import "testing"

func TestChangeRatePreservesPresentationTime(t *testing.T) {
	c := NewClock()
	c.SetAnchor(Anchor{Frame: 1000, MasterNs: 5_000_000_000, Rate: 44100})
	c.ChangeRate(45100, 48000)
	a, ok := c.Anchor()
	if !ok {
		t.Fatal("lost anchor")
	}
	if got, _ := a.MasterTime(45100); got != 6_000_000_000 {
		t.Fatalf("transition moved to %d", got)
	}
	if got, _ := a.MasterTime(93100); got != 7_000_000_000 {
		t.Fatalf("new rate not applied: %d", got)
	}
}

func TestClockUsesMatchingSyncReceiveTime(t *testing.T) {
	c := NewClock()
	h := Header{ClockID: [8]byte{1}, SourcePort: 2, Sequence: 3, CorrectionNs: 100}
	c.HandleSync(Message{Header: h}, 1_000_000_000)
	h.CorrectionNs = 200
	c.HandleFollowUp(Message{Header: h, Precise: Timestamp{Seconds: 1, Nanos: 500}}, 1_080_000_000)
	if offset, _ := c.Offset(); offset != 800 {
		t.Fatalf("offset = %d, want 800 ns independent of follow-up delay", offset)
	}
}

func TestClockJitterDoesNotAccumulateDrift(t *testing.T) {
	c := NewClock()
	for i := uint64(0); i < 1000; i++ {
		local := 10_000_000_000 + i*125_000_000
		delay := uint64(10_000_000)
		if i%2 == 0 {
			delay = 60_000_000
		}
		master := local + 1_000_000_000
		c.HandleFollowUp(Message{Precise: Timestamp{Seconds: master / 1_000_000_000, Nanos: uint32(master % 1_000_000_000)}}, local+delay)
	}
	offset, _ := c.Offset()
	if offset < 940_000_000 || offset > 990_000_000 {
		t.Fatalf("network jitter accumulated into clock drift: %d", offset)
	}
}

// followUp feeds n Follow_Up samples whose master time is local+offset.
func followUp(c *Clock, local uint64, offset int64, n int) uint64 {
	for i := 0; i < n; i++ {
		master := uint64(int64(local) + offset)
		c.HandleFollowUp(Message{Precise: Timestamp{Seconds: master / 1_000_000_000, Nanos: uint32(master % 1_000_000_000)}}, local)
		local += 125_000_000
	}
	return local
}

func TestHandleAnnounceWithoutOffsetClearsAnchor(t *testing.T) {
	c := NewClock()
	c.SetAnchor(Anchor{Frame: 100, MasterNs: 1_000_000_000, Rate: 44100})

	gm1 := [8]byte{1}
	c.HandleAnnounce(Message{Grandmaster: gm1})
	if _, ok := c.Anchor(); !ok {
		t.Fatal("anchor cleared on first Announce")
	}

	// No offset to the old master: the anchor cannot be placed in local
	// time, so a change of master discards it.
	gm2 := [8]byte{2}
	c.HandleAnnounce(Message{Grandmaster: gm2})
	if _, ok := c.Anchor(); ok {
		t.Fatal("unsynchronized anchor retained across grandmaster change")
	}
	if got := c.Info().MasterID; got != gm2 {
		t.Fatalf("masterID = %x, want %x", got, gm2)
	}
}

// Speakers joining a group can elect a new grandmaster mid-stream. The
// sender does not re-anchor, so frames must keep their presentation times.
func TestGrandmasterChangeCarriesAnchor(t *testing.T) {
	c := NewClock()
	gm1, gm2 := [8]byte{1}, [8]byte{2}
	var events []MasterChange
	c.SetMasterChangeHandler(func(e MasterChange) { events = append(events, e) })
	c.HandleAnnounce(Message{Grandmaster: gm1})
	local := followUp(c, 100_000_000_000, 5_000_000_000, 4)
	c.SetAnchor(Anchor{Frame: 1000, MasterNs: 106_000_000_000, Rate: 48000, ClockID: gm1})
	due, ok := c.FrameLocalTime(49000) // one second after the anchor frame
	if !ok || due != 102_000_000_000 {
		t.Fatalf("FrameLocalTime before change = %d, %v", due, ok)
	}

	c.HandleAnnounce(Message{Grandmaster: gm2})
	if _, ok := c.Offset(); ok {
		t.Fatal("offset retained across grandmaster change")
	}
	if got, ok := c.FrameLocalTime(49000); !ok || got != due {
		t.Fatalf("FrameLocalTime after change = %d, %v; want %d, true", got, ok, due)
	}
	if len(events) != 1 || events[0] != (MasterChange{From: gm1, To: gm2, Carried: true}) {
		t.Fatalf("master change events = %+v", events)
	}

	// The new master has an unrelated epoch. Until its offset settles the
	// carried local time stands; afterwards the anchor is in its epoch.
	local = followUp(c, local, -40_000_000_000, rebaseSamples-1)
	if a, _ := c.Anchor(); a.ClockID != gm1 {
		t.Fatal("anchor rebased before the new offset settled")
	}
	if got, ok := c.FrameLocalTime(49000); !ok || got != due {
		t.Fatalf("FrameLocalTime while settling = %d, %v; want %d", got, ok, due)
	}
	followUp(c, local, -40_000_000_000, 1)
	a, ok := c.Anchor()
	if !ok || a.ClockID != gm2 || a.Frame != 1000 || a.MasterNs != 101_000_000_000-40_000_000_000 {
		t.Fatalf("rebased anchor = %+v, %v", a, ok)
	}
	if got, ok := c.FrameLocalTime(49000); !ok || got != due {
		t.Fatalf("FrameLocalTime after rebase = %d, %v; want %d", got, ok, due)
	}
}

// A brief switch to another master and back must not convert the carried
// anchor twice, and a new anchor from the sender replaces it.
func TestGrandmasterFlapKeepsCarriedAnchor(t *testing.T) {
	c := NewClock()
	gm1, gm2 := [8]byte{1}, [8]byte{2}
	c.HandleAnnounce(Message{Grandmaster: gm1})
	local := followUp(c, 100_000_000_000, 5_000_000_000, 4)
	c.SetAnchor(Anchor{Frame: 0, MasterNs: 106_000_000_000, Rate: 48000})
	due, _ := c.FrameLocalTime(0)

	c.HandleAnnounce(Message{Grandmaster: gm2})
	local = followUp(c, local, 9_000_000_000, 2)
	c.HandleAnnounce(Message{Grandmaster: gm1})
	if got, ok := c.FrameLocalTime(0); !ok || got != due {
		t.Fatalf("FrameLocalTime after flap = %d, %v; want %d", got, ok, due)
	}

	c.SetAnchor(Anchor{Frame: 0, MasterNs: 107_000_000_000, Rate: 48000})
	if _, ok := c.FrameLocalTime(0); ok {
		t.Fatal("new anchor scheduled before the new master's offset")
	}
	followUp(c, local, 5_000_000_000, 1)
	if got, ok := c.FrameLocalTime(0); !ok || got != 102_000_000_000 {
		t.Fatalf("FrameLocalTime of new anchor = %d, %v", got, ok)
	}
}

func TestChangeRateWhileCarried(t *testing.T) {
	c := NewClock()
	c.HandleAnnounce(Message{Grandmaster: [8]byte{1}})
	followUp(c, 100_000_000_000, 5_000_000_000, 1)
	c.SetAnchor(Anchor{Frame: 1000, MasterNs: 106_000_000_000, Rate: 44100})
	c.HandleAnnounce(Message{Grandmaster: [8]byte{2}})
	c.ChangeRate(45100, 48000) // the transition frame plays at local 102s
	if got, ok := c.FrameLocalTime(45100 + 48000); !ok || got != 103_000_000_000 {
		t.Fatalf("FrameLocalTime after carried rate change = %d, %v", got, ok)
	}
}
