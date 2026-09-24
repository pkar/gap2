package pcm

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
)

type gainCapture struct {
	Sink
	block Block
}

func (s *gainCapture) Write(_ context.Context, b Block) error { s.block = b; return nil }

func TestGainAttenuationAndMute(t *testing.T) {
	s := &gainCapture{}
	g := NewGain(s, -20)
	b := Block{Format: Format{Rate: 44100, Channels: 2, Format: S16LE}, Data: []byte{0x10, 0x27, 0xf0, 0xd8}}
	if err := g.Write(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if int16(binary.LittleEndian.Uint16(s.block.Data)) != 1000 || int16(binary.LittleEndian.Uint16(s.block.Data[2:])) != -1000 {
		t.Fatal("-20 dB did not attenuate samples by 10")
	}
	if binary.LittleEndian.Uint16(b.Data) != 10000 {
		t.Fatal("input block mutated")
	}
	g.SetDB(-144)
	g.SetDB(math.NaN()) // invalid updates must not unmute
	_ = g.Write(context.Background(), b)
	if binary.LittleEndian.Uint32(s.block.Data) != 0 {
		t.Fatal("mute failed")
	}
}
