package aac

import (
	"os"
	"testing"
)

// FuzzDecode feeds arbitrary access units to decoders for each supported
// channel layout. Access units arrive from the network, so malformed input
// must produce an error, never a panic or an oversized block.
func FuzzDecode(f *testing.F) {
	for _, name := range []string{"stereo-44100", "noise-44100", "surround-48000", "surround71-48000"} {
		wire, err := os.ReadFile("testdata/" + name + ".aac")
		if err != nil {
			f.Fatal(err)
		}
		for i := 0; len(wire) > 0 && i < 4; i++ {
			h, err := ParseHeader(wire)
			if err != nil {
				f.Fatal(err)
			}
			cfg := byte(h.ChannelConfig)
			if name == "surround71-48000" {
				cfg = 12
			}
			f.Add(cfg, byte(h.SamplingFrequency/1000), wire[h.HeaderLength:h.FrameLength])
			wire = wire[h.FrameLength:]
		}
	}
	rates := map[byte]int{44: 44100, 48: 48000, 32: 32000, 22: 22050, 96: 96000, 8: 8000}
	f.Fuzz(func(t *testing.T, cfg, rate byte, au []byte) {
		sr, ok := rates[rate]
		if !ok {
			sr = 44100
		}
		d, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: sr, ChannelConfiguration: int(cfg % 16)})
		if err != nil {
			return
		}
		// Decode twice so state carried between frames (overlap, window
		// shape) is exercised with hostile input on both sides.
		for range 2 {
			b, err := d.Decode(au)
			if err != nil {
				continue
			}
			if b.Frames() != 1024 {
				t.Fatalf("decoded %d frames", b.Frames())
			}
		}
	})
}
