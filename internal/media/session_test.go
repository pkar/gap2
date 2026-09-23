package media

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkar/gap2/internal/sdp"
	"github.com/pkar/gap2/internal/stream"
	"github.com/pkar/gap2/pcm"
)

// recordingSink collects written blocks for assertions. It is test-only and,
// unlike production sinks, retains block data.
type recordingSink struct {
	mu     sync.Mutex
	blocks []pcm.Block
}

func (r *recordingSink) Write(_ context.Context, b pcm.Block) error {
	r.mu.Lock()
	r.blocks = append(r.blocks, b)
	r.mu.Unlock()
	return nil
}

func (r *recordingSink) Flush(context.Context) error { return nil }
func (r *recordingSink) Position() pcm.Position      { return pcm.Position{} }
func (r *recordingSink) Close() error                { return nil }

func (r *recordingSink) blocksCopy() []pcm.Block {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pcm.Block(nil), r.blocks...)
}

func monoMedia() *sdp.Media {
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

// monoRTPPacket builds one RTP packet carrying a silent mono AAC access unit.
func monoRTPPacket() []byte {
	// SCE, global_gain=100, only-long, max_sfb=1, codebook 0, no pulse/TNS/gain.
	au := packBits("000 0000 01100100 0 00 0 000001 0 0000 00001 0 0 0")
	sizeBits := len(au) * 8
	header := sizeBits << 3 // 13-bit AU size, 3-bit index 0
	payload := append([]byte{0x00, 0x10, byte(header >> 8), byte(header)}, au...)

	pkt := make([]byte, 12, 12+len(payload))
	pkt[0] = 0x80 // version 2
	pkt[1] = 0x60 // payload type 96, no marker
	return append(pkt, payload...)
}

// TestSessionServeDecodes drives a real RTP datagram through the full media
// pipeline: UDP receive -> stream ingest -> AAC decode -> sink.
func TestSessionServeDecodes(t *testing.T) {
	m := monoMedia()
	sink := &recordingSink{}
	sess, err := NewSession("1", m, sink)
	if err != nil {
		t.Fatal(err)
	}

	st := sess.Stream()
	if err := st.Announce(m); err != nil {
		t.Fatal(err)
	}
	tr, err := stream.ParseTransport("RTP/AVP/UDP;unicast;client_port=6000-6001")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Setup(tr, 7000); err != nil {
		t.Fatal(err)
	}
	if err := st.Record(); err != nil {
		t.Fatal(err)
	}

	sess.setReceiver(newReceiver(newFakeConn(monoRTPPacket())))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sess.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(sink.blocksCopy()) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no PCM block decoded")
		}
		time.Sleep(5 * time.Millisecond)
	}

	b := sink.blocksCopy()[0]
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

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v, want nil on cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancellation")
	}
}

// TestSessionServeBeforeBind reports an error when Serve runs without a bound
// receiver.
func TestSessionServeBeforeBind(t *testing.T) {
	sess, err := NewSession("1", monoMedia(), &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Serve(context.Background()); err == nil {
		t.Fatal("Serve before Bind succeeded, want error")
	}
}
