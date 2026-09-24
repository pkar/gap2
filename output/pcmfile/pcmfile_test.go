package pcmfile

import (
	"bytes"
	"context"
	"testing"

	"github.com/pkar/gap2/pcm"
)

func TestWriteAndPosition(t *testing.T) {
	var buf bytes.Buffer
	format := pcm.Format{Rate: 48000, Channels: 2, Format: pcm.S16LE}
	s, err := New(&buf, format)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	block, err := pcm.NewBlock(format, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), block); err != nil {
		t.Fatal(err)
	}
	if got := s.Position().Frames; got != 100 {
		t.Fatalf("frames = %d", got)
	}
	if buf.Len() != 400 {
		t.Fatalf("bytes = %d", buf.Len())
	}
}

func TestWriteMismatch(t *testing.T) {
	var buf bytes.Buffer
	format := pcm.Format{Rate: 48000, Channels: 2, Format: pcm.S16LE}
	s, _ := New(&buf, format)
	defer s.Close()
	other := pcm.Format{Rate: 44100, Channels: 2, Format: pcm.S16LE}
	b, _ := pcm.NewBlock(other, 1)
	if err := s.Write(context.Background(), b); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestCloseIdempotent(t *testing.T) {
	var buf bytes.Buffer
	s, _ := New(&buf, pcm.Format{Rate: 48000, Channels: 2, Format: pcm.S16LE})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCaptureAdoptsFormat ensures the Capture factory writes PCM at whatever
// format the stream announces, unlike Factory which pins one format.
func TestCaptureAdoptsFormat(t *testing.T) {
	var buf bytes.Buffer
	f := Capture(&buf)

	for _, format := range []pcm.Format{
		{Rate: 44100, Channels: 2, Format: pcm.S16LE},
		{Rate: 48000, Channels: 2, Format: pcm.S16LE},
	} {
		s, err := f.Open(context.Background(), format)
		if err != nil {
			t.Fatalf("open %s: %v", format, err)
		}
		block, err := pcm.NewBlock(format, 16)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Write(context.Background(), block); err != nil {
			t.Fatalf("write %s: %v", format, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if buf.Len() != 16*2*2+16*2*2 { // 16 frames x 2 ch x 2 bytes, twice
		t.Fatalf("bytes = %d", buf.Len())
	}
}

// TestFactoryRejectsMismatch confirms the pinned Factory still refuses a
// format other than the one it was configured with.
func TestFactoryRejectsMismatch(t *testing.T) {
	var buf bytes.Buffer
	f := Factory(&buf, pcm.Format{Rate: 44100, Channels: 2, Format: pcm.S16LE})
	if _, err := f.Open(context.Background(), pcm.Format{Rate: 48000, Channels: 2, Format: pcm.S16LE}); err == nil {
		t.Fatal("expected mismatch error")
	}
}
