package aac

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// buildADTS constructs a 7-byte protection-absent ADTS header for the given
// parameters.
func buildADTS(profile, sfIndex, channels, frameLen, rawBlocks int) []byte {
	b := make([]byte, 7)
	b[0] = 0xff
	b[1] = 0xf1 // sync, ID=0 (MPEG-4), layer=0, protection absent
	b[2] = byte(profile<<6 | sfIndex<<2 | ((channels >> 2) & 1))
	b[3] = byte((channels&3)<<6 | ((frameLen >> 11) & 3))
	b[4] = byte(frameLen >> 3)
	b[5] = byte((frameLen & 7) << 5)
	b[6] = byte(rawBlocks & 3)
	return b
}

func TestParseHeader(t *testing.T) {
	hdr := buildADTS(1, 4, 2, 100, 0) // AAC-LC, 44100 Hz, stereo
	h, err := ParseHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if h.Profile != ProfileLC {
		t.Fatalf("profile = %v, want lc", h.Profile)
	}
	if h.SamplingFrequency != 44100 {
		t.Fatalf("sample rate = %d, want 44100", h.SamplingFrequency)
	}
	if h.ChannelConfig != 2 {
		t.Fatalf("channels = %d, want 2", h.ChannelConfig)
	}
	if h.FrameLength != 100 || h.HeaderLength != 7 || h.PayloadLength() != 93 {
		t.Fatalf("lengths = %d/%d/%d, want 100/7/93", h.FrameLength, h.HeaderLength, h.PayloadLength())
	}
	if h.Samples() != 1024 {
		t.Fatalf("samples = %d, want 1024", h.Samples())
	}
	samples := 1024
	rate := 44100
	wantDur := time.Duration(float64(samples) / float64(rate) * float64(time.Second))
	if h.Duration() != wantDur {
		t.Fatalf("duration = %v, want %v", h.Duration(), wantDur)
	}
}

func TestParseHeaderRejects(t *testing.T) {
	if _, err := ParseHeader([]byte{0x00, 0x00, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("expected error for bad syncword")
	}
	if _, err := ParseHeader([]byte{0xff, 0xf1, 0, 0}); err == nil {
		t.Fatal("expected error for short header")
	}
	// sampling_frequency_index 15 = explicit frequency, unsupported.
	bad := buildADTS(1, 4, 2, 100, 0)
	bad[2] = (bad[2] & 0x0f) | (15 << 2)
	if _, err := ParseHeader(bad); err == nil {
		t.Fatal("expected error for explicit sample rate")
	}
}

func TestFramerNext(t *testing.T) {
	var stream bytes.Buffer
	stream.WriteString("junk before the first frame")

	h1 := buildADTS(1, 4, 2, 60, 0)
	p1 := bytes.Repeat([]byte{0x11}, 60-7)
	h2 := buildADTS(1, 4, 2, 70, 0)
	p2 := bytes.Repeat([]byte{0x22}, 70-7)
	stream.Write(append(h1, p1...))
	stream.Write(append(h2, p2...))

	f := NewFramer(bytes.NewReader(stream.Bytes()), 0)

	frame1, err := f.Next()
	if err != nil {
		t.Fatal(err)
	}
	if frame1.Header.FrameLength != 60 || len(frame1.Payload) != 53 {
		t.Fatalf("frame1 length = %d payload = %d", frame1.Header.FrameLength, len(frame1.Payload))
	}
	if frame1.Payload[0] != 0x11 || frame1.Payload[len(frame1.Payload)-1] != 0x11 {
		t.Fatalf("frame1 payload boundary corrupted")
	}

	frame2, err := f.Next()
	if err != nil {
		t.Fatal(err)
	}
	if frame2.Header.FrameLength != 70 || len(frame2.Payload) != 63 {
		t.Fatalf("frame2 length = %d payload = %d", frame2.Header.FrameLength, len(frame2.Payload))
	}
	if frame2.Payload[0] != 0x22 {
		t.Fatalf("frame2 payload corrupted")
	}

	if _, err := f.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final Next = %v, want io.EOF", err)
	}
}

func TestFramerTruncatedFrame(t *testing.T) {
	h := buildADTS(1, 4, 2, 60, 0)
	f := NewFramer(bytes.NewReader(h[:len(h)-3]), 0)
	if _, err := f.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Next = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestFramerOversize(t *testing.T) {
	h := buildADTS(1, 4, 2, 200, 0)
	f := NewFramer(bytes.NewReader(h), 64)
	if _, err := f.Next(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("Next = %v, want ErrFrameTooLarge", err)
	}
}
