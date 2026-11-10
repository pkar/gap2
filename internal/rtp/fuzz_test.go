package rtp

import "testing"

// FuzzParse parses arbitrary RTP packets and AAC payloads.
func FuzzParse(f *testing.F) {
	f.Add([]byte{0x80, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 16, 0, 8, 1})
	f.Add([]byte{0xb1, 96, 0, 1, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Parse(data)
		if err == nil {
			_, _ = ParseAACPayload(p.Payload)
		}
		_, _ = ParseAACPayload(data)
	})
}
