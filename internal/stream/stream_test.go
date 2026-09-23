package stream

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pkar/gap2/internal/sdp"
	"github.com/pkar/gap2/pcm"
)

type stubDecoder struct {
	block pcm.Block
	err   error
}

func (d stubDecoder) Decode(_ []byte) (pcm.Block, error) {
	return d.block, d.err
}

type recordingSink struct {
	format pcm.Format
	blocks []pcm.Block
}

func (r *recordingSink) Write(_ context.Context, b pcm.Block) error {
	r.blocks = append(r.blocks, b)
	return nil
}

func (r *recordingSink) Flush(context.Context) error { return nil }
func (r *recordingSink) Position() pcm.Position      { return pcm.Position{} }
func (r *recordingSink) Close() error                { return nil }

func aacMedia() *sdp.Media {
	return &sdp.Media{
		PayloadType: 96,
		Encoding:    "mpeg4-generic",
		ClockRate:   44100,
		Channels:    2,
		AAC: &sdp.AACConfig{
			SizeLength:       13,
			IndexLength:      3,
			IndexDeltaLength: 3,
			ASC:              []byte{0x12, 0x10},
		},
	}
}

func rtpPacket(payload []byte) []byte {
	p := make([]byte, 12, 12+len(payload))
	p[0] = 0x80 // version 2
	p[1] = 0x60 // payload type 96, no marker
	return append(p, payload...)
}

func TestStreamAACIngest(t *testing.T) {
	decoded := pcm.Block{Format: pcm.Format{Rate: 44100, Channels: 2, Format: pcm.S16LE}, Data: []byte{0, 1, 2, 3}}
	sink := &recordingSink{}
	s := New("1", stubDecoder{block: decoded}, sink)

	if err := s.Announce(aacMedia()); err != nil {
		t.Fatal(err)
	}
	tr, err := ParseTransport("RTP/AVP/UDP;unicast;client_port=6000-6001")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Setup(tr, 7000); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(); err != nil {
		t.Fatal(err)
	}

	// One AAC AU of 3 bytes.
	payload := []byte{0x00, 0x10, 0x00, 0xc0, 0xaa, 0xbb, 0xcc}
	if err := s.IngestRTP(context.Background(), rtpPacket(payload)); err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(sink.blocks))
	}
	if !bytes.Equal(sink.blocks[0].Data, decoded.Data) {
		t.Fatalf("block = %x, want %x", sink.blocks[0].Data, decoded.Data)
	}
}

func TestStreamStateErrors(t *testing.T) {
	s := New("1", stubDecoder{}, &recordingSink{})
	if err := s.IngestRTP(context.Background(), rtpPacket(nil)); err == nil {
		t.Fatal("ingest before record succeeded")
	}
	if err := s.Record(); err == nil {
		t.Fatal("record before setup succeeded")
	}
	if err := s.Setup(Transport{Protocol: "RTP/AVP/UDP"}, 7000); err == nil {
		t.Fatal("setup before announce succeeded")
	}
}

func aacMediaMono() *sdp.Media {
	return &sdp.Media{
		PayloadType: 96,
		Encoding:    "mpeg4-generic",
		ClockRate:   44100,
		Channels:    1,
		AAC: &sdp.AACConfig{
			SizeLength:       13,
			IndexLength:      3,
			IndexDeltaLength: 3,
			ASC:              []byte{0x12, 0x08}, // AAC-LC, 44.1 kHz, mono
		},
	}
}

// packBits packs an MSB-first bit string (spaces allowed) into bytes.
func packBits(s string) []byte {
	s = strings.ReplaceAll(s, " ", "")
	buf := make([]byte, (len(s)+7)/8)
	for i := 0; i < len(s); i++ {
		if s[i] == '1' {
			buf[i/8] |= 1 << (7 - uint(i%8))
		}
	}
	return buf
}

// TestStreamAACEndToEnd drives a real mono AAC-LC access unit through the
// full pipeline: SDP -> NewDecoder -> Announce/Setup/Record -> RTP ingest ->
// AAC decode -> PCM sink.
func TestStreamAACEndToEnd(t *testing.T) {
	m := aacMediaMono()
	dec, err := NewDecoder(m)
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingSink{}
	s := New("1", dec, sink)

	if err := s.Announce(m); err != nil {
		t.Fatal(err)
	}
	tr, err := ParseTransport("RTP/AVP/UDP;unicast;client_port=6000-6001")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Setup(tr, 7000); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(); err != nil {
		t.Fatal(err)
	}

	// A silent mono frame: SCE, global_gain=100, only-long, max_sfb=1,
	// codebook 0, no pulse/TNS/gain control.
	au := packBits("000 0000 01100100 0 00 0 000001 0 0000 00001 0 0 0")
	// RFC 3640: 16-bit AU-headers-length, one 16-bit AU header (13-bit AU size
	// in bits followed by a 3-bit AU index), then the access unit.
	sizeBits := len(au) * 8
	header := sizeBits << 3
	payload := append([]byte{0x00, 0x10, byte(header >> 8), byte(header)}, au...)
	if err := s.IngestRTP(context.Background(), rtpPacket(payload)); err != nil {
		t.Fatal(err)
	}

	if len(sink.blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(sink.blocks))
	}
	b := sink.blocks[0]
	if b.Format.Rate != 44100 || b.Format.Channels != 1 {
		t.Fatalf("block format = %+v, want 44100 Hz mono", b.Format)
	}
	if b.Frames() != 1024 {
		t.Fatalf("frames = %d, want 1024", b.Frames())
	}
	for i := 0; i < 1024; i++ {
		v := int16(b.Data[2*i]) | int16(b.Data[2*i+1])<<8
		if v != 0 {
			t.Fatalf("sample %d = %d, want 0", i, v)
		}
	}
}
