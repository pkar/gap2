package pcm

import "testing"

func TestNewBlock(t *testing.T) {
	f := Format{Rate: 48000, Channels: 2, Format: S16LE}
	b, err := NewBlock(f, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Data) != 400 {
		t.Fatalf("len = %d", len(b.Data))
	}
	if b.Frames() != 100 {
		t.Fatalf("frames = %d", b.Frames())
	}
}

func TestBlockPartialFrame(t *testing.T) {
	f := Format{Rate: 48000, Channels: 2, Format: S16LE}
	b := Block{Format: f, Data: make([]byte, 5)}
	if b.Frames() != 1 {
		t.Fatalf("frames = %d", b.Frames())
	}
}
