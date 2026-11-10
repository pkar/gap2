package ptp

import "testing"

// FuzzListenerHandle gives the listener arbitrary datagrams, also relabelled
// as Announce and Follow_Up, then reads the clock they left behind.
func FuzzListenerHandle(f *testing.F) {
	f.Add(Marshal(TypeSync, 1, [8]byte{1}, Timestamp{Seconds: 1, Nanos: 2}))
	f.Add(make([]byte, 64))
	f.Fuzz(func(t *testing.T, data []byte) {
		c := NewClock()
		l := newListener(nil, c, func() uint64 { return 1 })
		l.handle(data)
		if len(data) > 0 {
			data[0] = data[0]&0xf0 | TypeAnnounce
			l.handle(data)
			data[0] = data[0]&0xf0 | TypeFollowUp
			l.handle(data)
		}
		c.SetAnchor(Anchor{Frame: 1, MasterNs: 5, Rate: 44100})
		c.FrameLocalTime(7)
	})
}
