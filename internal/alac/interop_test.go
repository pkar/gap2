package alac

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func TestIndependentSurround24(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels int
	}{{"stereo24-48000", 2}, {"surround24-48000", 6}, {"surround71-48000", 8}} {
		t.Run(tc.name, func(t *testing.T) { compareIndependentALAC(t, tc.name, tc.channels) })
	}
}
func compareIndependentALAC(t *testing.T, name string, channels int) {
	wire, err := os.ReadFile("testdata/" + name + ".frames")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := os.ReadFile("testdata/" + name + ".s16le")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.BitDepth = 24
	cfg.Channels = channels
	cfg.SampleRate = 48000
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	for len(wire) > 0 {
		if len(wire) < 4 {
			t.Fatal("short frame")
		}
		n := int(binary.BigEndian.Uint32(wire))
		wire = wire[4:]
		if n > len(wire) {
			t.Fatal("truncated frame")
		}
		b, err := d.Decode(wire[:n])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b.Data...)
		wire = wire[n:]
	}
	if !bytes.Equal(got, reference) {
		for i := 0; i < len(got) && i < len(reference); i++ {
			if got[i] != reference[i] {
				t.Fatalf("PCM differs at byte %d: got %x want %x", i, got[i:i+2], reference[i:i+2])
			}
		}
		t.Fatalf("lengths %d != %d", len(got), len(reference))
	}
}
