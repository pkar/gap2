package ptp

import "testing"

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
