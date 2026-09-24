package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/pkar/gap2/internal/aac"
	"github.com/pkar/gap2/internal/sdp"
	"github.com/pkar/gap2/internal/stream"
	"github.com/pkar/gap2/pcm"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

type recoveryListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *recoveryListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *recoveryListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *recoveryListener) Addr() net.Addr { return &net.TCPAddr{} }

type recoverySink struct{ played chan struct{} }

func (s *recoverySink) Write(context.Context, pcm.Block) error { s.played <- struct{}{}; return nil }
func (s *recoverySink) Flush(context.Context) error            { return nil }
func (s *recoverySink) Position() pcm.Position                 { return pcm.Position{} }
func (s *recoverySink) Close() error                           { return nil }
func recoverySession(t *testing.T) (*Session, *recoveryListener, *recoverySink, []byte) {
	t.Helper()
	m := &sdp.Media{Encoding: "AAC", PayloadType: 96, ClockRate: 44100, Channels: 2, AAC: &sdp.AACConfig{ASC: []byte{0x12, 0x10}}}
	sink := &recoverySink{make(chan struct{}, 10)}
	s, err := NewSession("recovery", m, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.stream.Announce(m); err != nil {
		t.Fatal(err)
	}
	if err = s.stream.Setup(stream.Transport{Protocol: "RTP/AVP/TCP"}, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.stream.Record(); err != nil {
		t.Fatal(err)
	}
	l := &recoveryListener{conns: make(chan net.Conn), closed: make(chan struct{})}
	s.tcp = l
	fixture, err := os.ReadFile("../aac/testdata/stereo-44100.aac")
	if err != nil {
		t.Fatal(err)
	}
	h, err := aac.ParseHeader(fixture)
	if err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 12)
	packet[0] = 0x80
	packet[1] = 96
	binary.BigEndian.PutUint32(packet[8:], 0x16000000)
	packet = append(packet, fixture[h.HeaderLength:h.FrameLength]...)
	var wire bytes.Buffer
	binary.Write(&wire, binary.BigEndian, uint16(len(packet)+2))
	wire.Write(packet)
	return s, l, sink, wire.Bytes()
}
func TestBufferedReconnectAfterEOFAndTruncation(t *testing.T) {
	s, l, sink, wire := recoverySession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer s.Close()
	recovered := make(chan error, 1)
	s.SetRecoveryHandler(func(err error) { recovered <- err })
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	for i := 0; i < 100; i++ {
		server, client := net.Pipe()
		select {
		case l.conns <- server:
		case <-ctx.Done():
			t.Fatal("accept stalled")
		}
		client.SetWriteDeadline(time.Now().Add(time.Second))
		// Fragment every valid packet; inject EOF partway through the next frame.
		for pos := 0; pos < len(wire); pos += 7 {
			if _, err := client.Write(wire[pos:min(pos+7, len(wire))]); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case <-sink.played:
		case <-ctx.Done():
			t.Fatal("playback did not resume")
		}
		if i%2 != 0 {
			if _, err := client.Write(wire[:5]); err != nil {
				t.Fatal(err)
			}
		}
		client.Close()
		select {
		case err := <-recovered:
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				t.Fatalf("unexpected recovery: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("disconnect not detected")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("accept did not cancel")
	}
}
func TestBufferedStalledFrameCancellation(t *testing.T) {
	s, l, _, wire := recoverySession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.Close()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	server, client := net.Pipe()
	defer client.Close()
	l.conns <- server
	if _, err := client.Write(wire[:5]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("partial TCP read survived cancellation")
	}
}

// Opt-in so socket restrictions do not silently skip network validation.
func TestBufferedTCPResetRecovery(t *testing.T) {
	if os.Getenv("GAP2_NETWORK_TEST") != "1" {
		t.Skip("set GAP2_NETWORK_TEST=1 for real TCP resets")
	}
	s, _, sink, wire := recoverySession(t)
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.tcp = ln
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	recovered := make(chan error, 1)
	s.SetRecoveryHandler(func(err error) { recovered <- err })
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp4", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(time.Second))
		if _, err = c.Write(wire); err != nil {
			t.Fatal(err)
		}
		select {
		case <-sink.played:
		case <-ctx.Done():
			t.Fatal("TCP playback did not resume")
		}
		c.(*net.TCPConn).SetLinger(0)
		c.Close()
		select {
		case <-recovered:
		case <-ctx.Done():
			t.Fatal("TCP reset not detected")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TCP accept did not cancel")
	}
}
