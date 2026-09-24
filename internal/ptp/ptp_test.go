package ptp

import (
	"testing"
)

func TestTimestampRoundTrip(t *testing.T) {
	cases := []Timestamp{
		{Seconds: 0, Nanos: 0},
		{Seconds: 1, Nanos: 500_000_000},
		{Seconds: 0x0123456789ab, Nanos: 999_999_999},
		{Seconds: 0xffff_ffff_ffff, Nanos: 0},
	}
	for _, in := range cases {
		got := parseTimestamp(marshalTimestamp(in))
		if got != in {
			t.Fatalf("round trip %+v -> %+v", in, got)
		}
	}
}

func TestTimestampNanos(t *testing.T) {
	ts := Timestamp{Seconds: 3, Nanos: 250_000_000}
	if got := ts.AsNanos(); got != 3_250_000_000 {
		t.Fatalf("Nanos = %d, want 3250000000", got)
	}
}

func TestMarshalParseRoundTrip(t *testing.T) {
	id := [8]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77}
	ts := Timestamp{Seconds: 100, Nanos: 123_456_789}

	for _, mt := range []uint8{TypeSync, TypeFollowUp, TypeDelayReq} {
		b := Marshal(mt, 42, id, ts)
		m, err := Parse(b)
		if err != nil {
			t.Fatalf("type %d: %v", mt, err)
		}
		if m.Header.MessageType != mt {
			t.Fatalf("type = %d, want %d", m.Header.MessageType, mt)
		}
		if m.Header.Version != VersionPTP {
			t.Fatalf("version = %d, want %d", m.Header.Version, VersionPTP)
		}
		if m.Header.Sequence != 42 {
			t.Fatalf("sequence = %d, want 42", m.Header.Sequence)
		}
		if m.Header.ClockID != id {
			t.Fatalf("clock id = %x, want %x", m.Header.ClockID, id)
		}
		switch mt {
		case TypeFollowUp:
			if m.Precise != ts {
				t.Fatalf("precise = %+v, want %+v", m.Precise, ts)
			}
		default:
			if m.Origin != ts {
				t.Fatalf("origin = %+v, want %+v", m.Origin, ts)
			}
		}
	}
}

func TestParseHeaderFields(t *testing.T) {
	b := Marshal(TypeSync, 7, [8]byte{1}, Timestamp{})
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.Length != uint16(len(b)) {
		t.Fatalf("length = %d, want %d", m.Header.Length, len(b))
	}
	if m.Header.Domain != DefaultDomain {
		t.Fatalf("domain = %d, want %d", m.Header.Domain, DefaultDomain)
	}
	if m.Header.Flags != DefaultFlags {
		t.Fatalf("flags = %#x, want %#x", m.Header.Flags, DefaultFlags)
	}
	if m.Header.Control != ControlSync {
		t.Fatalf("control = %d, want %d", m.Header.Control, ControlSync)
	}
	if m.Header.SourcePort != 1 {
		t.Fatalf("source port = %d, want 1", m.Header.SourcePort)
	}
}

func TestParseCorrectionField(t *testing.T) {
	b := Marshal(TypeFollowUp, 1, [8]byte{1}, Timestamp{})
	// Set correction field to 2^16 (one nanosecond in 48.16 fixed point).
	b[8] = 0x00
	b[9] = 0x00
	b[10] = 0x00
	b[11] = 0x00
	b[12] = 0x00
	b[13] = 0x01
	b[14] = 0x00
	b[15] = 0x00
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.CorrectionNs != 1 {
		t.Fatalf("correction = %d, want 1", m.Header.CorrectionNs)
	}
}

func TestParseAnnounce(t *testing.T) {
	// Build a minimal Announce message by hand: 34-byte header + 30-byte body.
	b := make([]byte, headerSize+30)
	b[0] = TransportSpecific<<4 | TypeAnnounce
	b[1] = VersionPTP
	b[4] = DefaultDomain
	b[6] = 0x06
	b[7] = 0x08
	gm := [8]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}
	copy(b[headerSize+20:], gm[:])

	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.MessageType != TypeAnnounce {
		t.Fatalf("type = %d, want %d", m.Header.MessageType, TypeAnnounce)
	}
	if m.Grandmaster != gm {
		t.Fatalf("grandmaster = %x, want %x", m.Grandmaster, gm)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse(nil); err != ErrShort {
		t.Fatalf("short parse err = %v, want ErrShort", err)
	}
	b := Marshal(TypeSync, 1, [8]byte{1}, Timestamp{})
	if _, err := Parse(b[:20]); err != ErrShort {
		t.Fatalf("truncated parse err = %v, want ErrShort", err)
	}
}

func TestClockOffset(t *testing.T) {
	c := NewClock()
	// No estimate yet.
	if _, ok := c.Offset(); ok {
		t.Fatal("offset available before any Follow_Up")
	}

	// Follow_Up: master precise time 1000.000000500s, received at local
	// 1000.000000000s. Offset = +500ns.
	followUp := Message{
		Header:  Header{MessageType: TypeFollowUp},
		Precise: Timestamp{Seconds: 1000, Nanos: 500},
	}
	local := uint64(1000_000_000_000)
	c.HandleFollowUp(followUp, local)

	off, ok := c.Offset()
	if !ok {
		t.Fatal("offset not available after Follow_Up")
	}
	if off != 500 {
		t.Fatalf("offset = %d, want 500", off)
	}

	// master = local + offset.
	if got, _ := c.MasterTime(local); got != 1000_000_000_500 {
		t.Fatalf("MasterTime = %d, want 1000000000500", got)
	}
	// local = master - offset.
	if got, _ := c.LocalTime(1000_000_000_500); got != local {
		t.Fatalf("LocalTime = %d, want %d", got, local)
	}
}

func TestClockCorrectionField(t *testing.T) {
	c := NewClock()
	m := Message{
		Header:  Header{MessageType: TypeFollowUp, CorrectionNs: 250},
		Precise: Timestamp{Seconds: 10, Nanos: 0},
	}
	// Received at local 10.000000100s; precise 10.0 + correction 250ns
	// = 10.000000250s; offset = 150ns.
	c.HandleFollowUp(m, 10_000_000_100)
	off, _ := c.Offset()
	if off != 150 {
		t.Fatalf("offset = %d, want 150", off)
	}
}

func TestClockAnnounceResetsOffset(t *testing.T) {
	c := NewClock()
	c.HandleFollowUp(Message{
		Header:  Header{MessageType: TypeFollowUp},
		Precise: Timestamp{Seconds: 1},
	}, 1_000_000_000)
	if _, ok := c.Offset(); !ok {
		t.Fatal("offset not available")
	}

	// A new grandmaster invalidates the estimate.
	gm := [8]byte{0xde, 0xad, 0xbe, 0xef}
	c.HandleAnnounce(Message{Grandmaster: gm})
	if _, ok := c.Offset(); ok {
		t.Fatal("offset still available after grandmaster change")
	}
	if info := c.Info(); info.MasterID != gm {
		t.Fatalf("master id = %x, want %x", info.MasterID, gm)
	}
}

func TestClockAnchor(t *testing.T) {
	c := NewClock()

	// No anchor until one is set.
	if _, ok := c.Anchor(); ok {
		t.Fatal("anchor available before SetAnchor")
	}
	// A zero-rate anchor is rejected.
	c.SetAnchor(Anchor{Frame: 1, MasterNs: 2, Rate: 0})
	if _, ok := c.Anchor(); ok {
		t.Fatal("zero-rate anchor accepted")
	}

	a := Anchor{Frame: 0, MasterNs: 10_000_000_000, Rate: 44100}
	c.SetAnchor(a)
	got, ok := c.Anchor()
	if !ok || got != a {
		t.Fatalf("Anchor = %+v, %v; want %+v, true", got, ok, a)
	}

	// FrameLocalTime needs a valid offset estimate too.
	if _, ok := c.FrameLocalTime(0); ok {
		t.Fatal("FrameLocalTime ok before offset estimate")
	}

	// Establish offset = +500ns via a Follow_Up.
	c.HandleFollowUp(Message{
		Header:  Header{MessageType: TypeFollowUp},
		Precise: Timestamp{Seconds: 10, Nanos: 500},
	}, 10_000_000_000)

	// The anchor frame plays at master 10s; local = master - offset.
	lt, ok := c.FrameLocalTime(0)
	if !ok || lt != 10_000_000_000-500 {
		t.Fatalf("FrameLocalTime(anchor) = %d, %v; want 9999999500, true", lt, ok)
	}
	// One second of frames later (+44100 frames).
	lt, _ = c.FrameLocalTime(44100)
	if lt != 11_000_000_000-500 {
		t.Fatalf("FrameLocalTime(+1s) = %d, want 10999999500", lt)
	}
}

func TestClockStatus(t *testing.T) {
	c := NewClock()
	if st := c.Status(); st.Synced || st.Anchored || st.OffsetNs != 0 {
		t.Fatalf("initial Status = %+v, want zeroed", st)
	}

	c.HandleFollowUp(Message{
		Header:  Header{MessageType: TypeFollowUp},
		Precise: Timestamp{Seconds: 10, Nanos: 500},
	}, 10_000_000_000)
	if st := c.Status(); !st.Synced || st.OffsetNs != 500 {
		t.Fatalf("Status after FollowUp = %+v, want synced offset 500", st)
	}

	c.SetAnchor(Anchor{Frame: 0, MasterNs: 10_000_000_000, Rate: 44100})
	if st := c.Status(); !st.Anchored {
		t.Fatalf("Status after SetAnchor = %+v, want anchored", st)
	}
}
