package pcm

import (
	"bytes"
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

type outputGainCapture struct {
	gainCapture
	supported bool
	levels    []float64
}

func (s *outputGainCapture) SetVolumeDB(db float64) bool {
	s.levels = append(s.levels, db)
	return s.supported
}

func TestGainDelegatesThroughConverter(t *testing.T) {
	format := Format{Rate: 48000, Channels: 2, Format: S16LE}
	b := Block{Format: format, Data: []byte{1, 0, 255, 255}}
	for _, supported := range []bool{true, false} {
		s := &outputGainCapture{supported: supported}
		converter, err := NewConverter(s, format)
		if err != nil {
			t.Fatal(err)
		}
		g := NewGain(converter, -60)
		for _, db := range []float64{-60, -144, 0} {
			g.SetDB(db)
			if err := g.Write(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			want := b.Data
			if !supported && db != 0 {
				want = []byte{0, 0, 0, 0}
			}
			if !bytes.Equal(s.block.Data, want) {
				t.Fatalf("supported=%v db=%v: got %v, want %v", supported, db, s.block.Data, want)
			}
			if got := s.levels[len(s.levels)-1]; got != db {
				t.Fatalf("output received %v dB, want %v", got, db)
			}
		}
		calls := len(s.levels)
		for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1} {
			g.SetDB(invalid)
		}
		if len(s.levels) != calls {
			t.Fatal("invalid volume reached the output")
		}
	}
}

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
