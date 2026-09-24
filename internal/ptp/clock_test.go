package ptp

import "testing"

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

func TestHandleAnnounceClearsAnchor(t *testing.T) {
	c := NewClock()
	c.SetAnchor(Anchor{Frame: 100, MasterNs: 1_000_000_000, Rate: 44100})
	if _, ok := c.Anchor(); !ok {
		t.Fatal("anchor not set")
	}

	gm1 := [8]byte{1}
	c.HandleAnnounce(Message{Grandmaster: gm1})
	if _, ok := c.Anchor(); !ok {
		t.Fatal("anchor cleared on first Announce")
	}

	// Establish an offset, then change the grandmaster: both the offset and
	// the anchor must be discarded because they belong to the old master's
	// epoch.
	c.HandleFollowUp(Message{Precise: Timestamp{Seconds: 100}}, 100_000_000_000)
	if _, ok := c.Offset(); !ok {
		t.Fatal("offset not set")
	}

	gm2 := [8]byte{2}
	c.HandleAnnounce(Message{Grandmaster: gm2})
	if _, ok := c.Anchor(); ok {
		t.Fatal("anchor retained across grandmaster change")
	}
	if _, ok := c.Offset(); ok {
		t.Fatal("offset retained across grandmaster change")
	}
	if got := c.Info().MasterID; got != gm2 {
		t.Fatalf("masterID = %x, want %x", got, gm2)
	}
}
