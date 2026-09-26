package ptp

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeConn is an in-memory net.PacketConn that returns preloaded datagrams and
// reports net.ErrClosed after Close, letting Serve be tested without network.
type fakeConn struct {
	mu     sync.Mutex
	pkts   [][]byte
	closed bool
	notify chan struct{}
	addr   net.Addr
}

func newFakeConn(pkts ...[]byte) *fakeConn {
	return &fakeConn{pkts: pkts, notify: make(chan struct{}), addr: fakeAddr("127.0.0.1:319")}
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

func TestListenerHandle(t *testing.T) {
	c := NewClock()
	now := uint64(0)
	l := newListener(nil, c, func() uint64 { return now })

	// Sync then Follow_Up: offset = precise - receive = 1000.000000500 - 1000.000000000.
	sync := Marshal(TypeSync, 1, [8]byte{1}, Timestamp{Seconds: 1000, Nanos: 500})
	now = 1000_000_000_000
	l.handle(sync)

	followUp := Marshal(TypeFollowUp, 1, [8]byte{1}, Timestamp{Seconds: 1000, Nanos: 500})
	l.handle(followUp)

	off, ok := c.Offset()
	if !ok {
		t.Fatal("offset not set after Sync+Follow_Up")
	}
	if off != 500 {
		t.Fatalf("offset = %d, want 500", off)
	}
}

func TestListenerHandleAnnounce(t *testing.T) {
	c := NewClock()
	l := newListener(nil, c, func() uint64 { return 0 })

	gm := [8]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	b := make([]byte, headerSize+30)
	b[0] = TransportSpecific<<4 | TypeAnnounce
	b[1] = VersionPTP
	b[4] = DefaultDomain
	b[6] = 0x06
	b[7] = 0x08
	b[headerSize+18] = 0xf8
	copy(b[headerSize+19:], gm[:])
	l.handle(b)

	if got := c.Info(); got.MasterID != gm {
		t.Fatalf("master id = %x, want %x", got.MasterID, gm)
	}
}

func TestListenerHandleIgnoresMalformed(t *testing.T) {
	c := NewClock()
	l := newListener(nil, c, func() uint64 { return 0 })
	l.handle(nil)
	l.handle([]byte{0x01, 0x02, 0x03})
	if _, ok := c.Offset(); ok {
		t.Fatal("offset set from malformed packets")
	}
}

func TestListenerServeDispatch(t *testing.T) {
	c := NewClock()
	now := uint64(1000_000_000_000)
	l := newListener(newFakeConn(
		Marshal(TypeFollowUp, 1, [8]byte{1}, Timestamp{Seconds: 1000, Nanos: 500}),
	), c, func() uint64 { return now })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := c.Offset(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("offset not set from served Follow_Up")
		}
		time.Sleep(5 * time.Millisecond)
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

func TestGroupServeDispatch(t *testing.T) {
	c := NewClock()
	now := uint64(1000_000_000_000)
	g := &Group{listeners: []*Listener{
		newListener(newFakeConn(), c, func() uint64 { return now }),
		newListener(newFakeConn(
			Marshal(TypeFollowUp, 1, [8]byte{1}, Timestamp{Seconds: 1000, Nanos: 500}),
		), c, func() uint64 { return now }),
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := c.Offset(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("offset not set from served Follow_Up across group")
		}
		time.Sleep(5 * time.Millisecond)
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
