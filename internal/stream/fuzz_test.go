package stream

import "testing"

// FuzzParseTransport parses arbitrary Transport headers.
func FuzzParseTransport(f *testing.F) {
	f.Add("RTP/AVP/UDP;unicast;interleaved=0-1;mode=record;control_port=6001;timing_port=6002")
	f.Fuzz(func(t *testing.T, s string) { _, _ = ParseTransport(s) })
}

// FuzzBufferedMedia parses arbitrary buffered-audio packet prefixes.
func FuzzBufferedMedia(f *testing.F) {
	f.Add(uint32(0x01000000))
	f.Fuzz(func(t *testing.T, w uint32) {
		m, err := bufferedMedia(w)
		if err != nil {
			return
		}
		if _, err := NewDecoder(m); err != nil {
			return
		}
		if _, err := MediaFormat(m); err != nil {
			t.Fatalf("decoder without format: %v", err)
		}
	})
}
