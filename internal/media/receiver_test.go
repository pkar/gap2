package media

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeConn is an in-memory net.PacketConn that returns preloaded datagrams and
// reports net.ErrClosed after Close, letting Serve be tested without network
// access.
type fakeConn struct {
	mu     sync.Mutex
	pkts   [][]byte
	closed bool
	notify chan struct{}
	addr   net.Addr
}

func newFakeConn(pkts ...[]byte) *fakeConn {
	return &fakeConn{pkts: pkts, notify: make(chan struct{}), addr: fakeAddr("127.0.0.1:12345")}
}

func (f *fakeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, nil, net.ErrClosed
		}
		if len(f.pkts) > 0 {
			pkt := f.pkts[0]
			f.pkts = f.pkts[1:]
			f.mu.Unlock()
			return copy(p, pkt), f.addr, nil
		}
		ch := f.notify
		f.mu.Unlock()
		<-ch
	}
}

func (f *fakeConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }

func (f *fakeConn) Close() error {
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		close(f.notify)
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeConn) LocalAddr() net.Addr              { return f.addr }
func (f *fakeConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return "udp" }
func (a fakeAddr) String() string  { return string(a) }

// TestReceiverServeDispatch drives Serve with preloaded datagrams and verifies
// they are delivered to the handler in order and that cancellation returns
// cleanly.
func TestReceiverServeDispatch(t *testing.T) {
	packets := [][]byte{{0x80, 0x60, 0x00, 0x01}, {0xaa, 0xbb, 0xcc, 0xdd, 0xee}}
	recv := newReceiver(newFakeConn(packets...))
	if recv.Addr().String() != "127.0.0.1:12345" {
		t.Fatalf("Addr = %v", recv.Addr())
	}

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var got [][]byte
	done := make(chan error, 1)
	go func() {
		done <- recv.Serve(ctx, func(pkt []byte) error {
			mu.Lock()
			got = append(got, append([]byte(nil), pkt...))
			mu.Unlock()
			return nil
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == len(packets) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("received %d datagrams, want %d", n, len(packets))
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	received := make([][]byte, len(got))
	copy(received, got)
	mu.Unlock()
	for i, p := range packets {
		if !bytes.Equal(received[i], p) {
			t.Fatalf("datagram %d = %x, want %x", i, received[i], p)
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

// TestReceiverServeHandlerError verifies a handler error is propagated.
func TestReceiverServeHandlerError(t *testing.T) {
	recv := newReceiver(newFakeConn([]byte{0x01}))
	err := recv.Serve(context.Background(), func([]byte) error {
		return context.Canceled
	})
	if err != context.Canceled {
		t.Fatalf("Serve = %v, want context.Canceled", err)
	}
}
