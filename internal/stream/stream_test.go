package stream

import (
	"bytes"
	"context"
	"fmt"
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

// timedSink records the RTP frame timestamps it is handed through the
// TimedSink path, so tests can assert timestamp threading without a real clock.
type timedSink struct {
	frames []uint32
	blocks []pcm.Block
}

func (r *timedSink) WriteTimed(_ context.Context, frame uint32, b pcm.Block) error {
	r.frames = append(r.frames, frame)
	r.blocks = append(r.blocks, b)
	return nil
}

func (r *timedSink) Write(_ context.Context, b pcm.Block) error {
	r.blocks = append(r.blocks, b)
	return nil
}

func (r *timedSink) Flush(context.Context) error { return nil }
func (r *timedSink) Position() pcm.Position      { return pcm.Position{} }
func (r *timedSink) Close() error                { return nil }

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

// rtpPacketTS builds an RTP packet with an explicit 32-bit timestamp.
func rtpPacketTS(payload []byte, ts uint32) []byte {
	p := rtpPacket(payload)
	p[4] = byte(ts >> 24)
	p[5] = byte(ts >> 16)
	p[6] = byte(ts >> 8)
	p[7] = byte(ts)
	return p
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

func TestBufferedFlushDropsOldFramesAcrossWrap(t *testing.T) {
	sink := &timedSink{}
	s := New("1", stubDecoder{block: pcm.Block{}}, sink)
	m := aacMedia()
	m.Encoding = "AAC"
	if err := s.Announce(m); err != nil {
		t.Fatal(err)
	}
	if err := s.Setup(Transport{Protocol: "RTP/AVP/TCP"}, 7000); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(); err != nil {
		t.Fatal(err)
	}
	s.FlushRange(1024, nil)
	for _, ts := range []uint32{^uint32(0) - 1023, 0, 1024} {
		if err := s.IngestRTP(context.Background(), rtpPacketTS([]byte{0}, ts)); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.frames) != 1 || sink.frames[0] != 1024 {
		t.Fatalf("flush played timestamps %v", sink.frames)
	}
}

func TestBufferedFlushSequenceSurvivesTimestampEpochChange(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		sink := &timedSink{}
		s := New("1", stubDecoder{block: pcm.Block{}}, sink)
		m := aacMedia()
		m.Encoding = "AAC"
		if err := s.Announce(m); err != nil {
			t.Fatal(err)
		}
		if err := s.Setup(Transport{Protocol: "RTP/AVP/TCP"}, 7000); err != nil {
			t.Fatal(err)
		}
		if err := s.Record(); err != nil {
			t.Fatal(err)
		}
		var from *uint32
		if deferred {
			f := uint32(0x7fffff)
			from = &f
		}
		s.FlushBufferedRange(1, from)
		// The endpoint starts a new timestamp epoch behind the old one,
		// while sequence counters wrap independently at 23 bits.
		for i, seq := range []uint32{0x7ffffe, 0x7fffff, 0, 1, 2} {
			ts := uint32(3504865437)
			if i >= 3 {
				ts = uint32(3348046257 + (i-3)*1024)
			}
			if err := s.IngestBufferedRTP(context.Background(), rtpPacketTS([]byte{0}, ts), seq); err != nil {
				t.Fatal(err)
			}
		}
		want := 2
		if deferred {
			want++
		}
		if len(sink.frames) != want || sink.frames[want-2] != 3348046257 {
			t.Fatalf("deferred=%v played timestamps %v", deferred, sink.frames)
		}
	}
}

func TestStreamTimedSink(t *testing.T) {
	decoded := pcm.Block{Format: pcm.Format{Rate: 44100, Channels: 2, Format: pcm.S16LE}, Data: []byte{0, 1, 2, 3}}
	sink := &timedSink{}
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

	// One AAC AU, RTP timestamp 44100. The sink must receive exactly that
	// frame timestamp through the TimedSink path.
	payload := []byte{0x00, 0x10, 0x00, 0xc0, 0xaa, 0xbb, 0xcc}
	if err := s.IngestRTP(context.Background(), rtpPacketTS(payload, 44100)); err != nil {
		t.Fatal(err)
	}
	if len(sink.frames) != 1 || sink.frames[0] != 44100 {
		t.Fatalf("frames = %v, want [44100]", sink.frames)
	}
	if len(sink.blocks) != 1 || !bytes.Equal(sink.blocks[0].Data, decoded.Data) {
		t.Fatalf("blocks = %+v, want decoded block", sink.blocks)
	}
}

func TestStreamTimedSinkMultiAU(t *testing.T) {
	decoded := pcm.Block{Format: pcm.Format{Rate: 44100, Channels: 2, Format: pcm.S16LE}, Data: []byte{0, 1, 2, 3}}
	sink := &timedSink{}
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

	// Two 3-byte AUs. Consecutive AAC-LC frames are 1024 samples apart, so the
	// second AU must carry timestamp+1024.
	payload := []byte{
		0x00, 0x20, // AU-headers-length = 32 bits
		0x00, 0xc0, // AU1: 24 bits, index 0
		0x00, 0xc0, // AU2: 24 bits, index 0
		0xaa, 0xbb, 0xcc,
		0xdd, 0xee, 0xff,
	}
	if err := s.IngestRTP(context.Background(), rtpPacketTS(payload, 44100)); err != nil {
		t.Fatal(err)
	}
	want := []uint32{44100, 44100 + 1024}
	if len(sink.frames) != 2 || sink.frames[0] != want[0] || sink.frames[1] != want[1] {
		t.Fatalf("frames = %v, want %v", sink.frames, want)
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

// TestStreamALACEndToEnd drives a real uncompressed mono ALAC frame through
// the pipeline: SDP -> NewDecoder -> Announce/Setup/Record -> RTP ingest ->
// ALAC decode -> PCM sink.
func TestStreamALACEndToEnd(t *testing.T) {
	m := &sdp.Media{
		PayloadType: 96,
		Encoding:    "AppleLossless",
		ALAC: &sdp.ALACConfig{
			FrameLength: 4096,
			BitDepth:    16,
			PB:          40,
			MB:          10,
			KB:          14,
			Channels:    1,
			SampleRate:  44100,
		},
	}
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

	// Uncompressed mono frame: SCE, one sample, value 1234.
	const sample = int16(1234)
	var bits string
	bits += "000"          // SCE element type
	bits += "0000"         // element instance tag
	bits += "000000000000" // unused header bits
	bits += "1"            // has_size
	bits += "00"           // extra_bits
	bits += "1"            // uncompressed
	bits += fmt.Sprintf("%032b", 1)
	bits += fmt.Sprintf("%016b", uint16(sample))
	bits += "111" // TYPE_END

	if err := s.IngestRTP(context.Background(), rtpPacket(packBits(bits))); err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(sink.blocks))
	}
	b := sink.blocks[0]
	if b.Format.Rate != 44100 || b.Format.Channels != 1 {
		t.Fatalf("block format = %+v, want 44100 Hz mono", b.Format)
	}
	if b.Frames() != 1 {
		t.Fatalf("frames = %d, want 1", b.Frames())
	}
	if got := int16(b.Data[0]) | int16(b.Data[1])<<8; got != sample {
		t.Fatalf("sample = %d, want %d", got, sample)
	}
}
