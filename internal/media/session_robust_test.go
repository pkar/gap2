package media

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkar/gap2/internal/stream"
	"github.com/pkar/gap2/pcm"
)

// On UDP anyone on the network can reach the audio port, and a datagram can
// arrive damaged. A packet that fails authentication or does not decode must
// be dropped without ending the stream.
func TestSessionServeSkipsBadDatagrams(t *testing.T) {
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

	good := monoRTPPacket()
	wrongType := append([]byte(nil), good...)
	wrongType[1] = 0x61
	badAU := append([]byte(nil), good...)
	badAU[14], badAU[15] = 0xff, 0xf8 // AU size far beyond the payload
	forged := []byte("forged")
	sess.SetPacketDecoder(func(pkt []byte) ([]byte, error) {
		if string(pkt) == "forged" {
			return nil, errors.New("authentication failed")
		}
		return pkt, nil
	})
	var reported atomic.Uint64
	sess.SetDropHandler(func(_ error, total uint64) { reported.Store(total) })
	sess.setReceiver(newReceiver(newFakeConn([]byte{1, 2, 3}, good, forged, wrongType, badAU, good)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sess.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for len(sink.blocksCopy()) < 2 {
		select {
		case err := <-done:
			t.Fatalf("Serve ended on a bad datagram: %v (decoded %d blocks)", err, len(sink.blocksCopy()))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("decoded %d blocks, want 2", len(sink.blocksCopy()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := sess.Dropped(); got != 4 {
		t.Fatalf("dropped %d datagrams, want 4", got)
	}
	if got := reported.Load(); got != 4 {
		t.Fatalf("drop handler last saw %d, want 4", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve = %v, want nil", err)
	}
}

// A failing output is not a packet problem: it must still end Serve.
func TestSessionServeStopsOnSinkError(t *testing.T) {
	m := monoMedia()
	sess, err := NewSession("1", m, failingSink{})
	if err != nil {
		t.Fatal(err)
	}
	st := sess.Stream()
	if err := st.Announce(m); err != nil {
		t.Fatal(err)
	}
	tr, _ := stream.ParseTransport("RTP/AVP/UDP;unicast;client_port=6000-6001")
	if err := st.Setup(tr, 7000); err != nil {
		t.Fatal(err)
	}
	if err := st.Record(); err != nil {
		t.Fatal(err)
	}
	sess.setReceiver(newReceiver(newFakeConn(monoRTPPacket())))
	done := make(chan error, 1)
	go func() { done <- sess.Serve(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errSinkGone) {
			t.Fatalf("Serve = %v, want the sink error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve kept running with a failed output")
	}
}

var errSinkGone = errors.New("output device gone")

type failingSink struct{}

func (failingSink) Write(context.Context, pcm.Block) error { return errSinkGone }
func (failingSink) Flush(context.Context) error            { return nil }
func (failingSink) Position() pcm.Position                 { return pcm.Position{} }
func (failingSink) Close() error                           { return nil }

// Binding again must release the first socket instead of leaving it open.
func TestSessionRebindClosesPreviousSocket(t *testing.T) {
	for _, network := range []string{"udp4", "tcp4"} {
		sess, err := NewSession("1", monoMedia(), &recordingSink{})
		if err != nil {
			t.Fatal(err)
		}
		first, err := sess.Bind(network, "127.0.0.1:0")
		if err != nil {
			t.Skipf("network bind unavailable: %v", err)
		}
		if _, err := sess.Bind(network, "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		// The first port is free again only if its socket was closed.
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(first))
		if network == "udp4" {
			c, err := net.ListenPacket(network, addr)
			if err != nil {
				t.Fatalf("%s: first socket still bound: %v", network, err)
			}
			c.Close()
		} else {
			l, err := net.Listen(network, addr)
			if err != nil {
				t.Fatalf("%s: first listener still bound: %v", network, err)
			}
			l.Close()
		}
		if err := sess.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
