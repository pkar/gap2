package alac

import (
	"testing"

	"github.com/pkar/gap2/internal/sdp"
)

// FuzzDecode decodes arbitrary frames at each channel count and bit depth.
// Frames arrive from the network, so malformed input must produce an error,
// never a panic or a block larger than one frame.
func FuzzDecode(f *testing.F) {
	f.Add(byte(2), byte(16), []byte{0x20, 0, 0, 0x12, 0, 0, 1, 0x60, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 0xe0})
	f.Add(byte(6), byte(24), make([]byte, 64))
	f.Add(byte(8), byte(16), []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, ch, depth byte, data []byte) {
		channels := int(ch%8) + 1
		bits := []int{16, 20, 24, 32}[depth%4]
		d, err := NewDecoder(&sdp.ALACConfig{FrameLength: 352, BitDepth: bits, PB: 40, MB: 10, KB: 14, Channels: channels, SampleRate: 44100})
		if err != nil {
			return
		}
		for i := 0; i < 2; i++ {
			b, err := d.Decode(data)
			if err == nil && len(b.Data) > 352*channels*2 {
				t.Fatalf("oversized block %d", len(b.Data))
			}
		}
	})
}
