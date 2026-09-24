package ptp

import (
	"context"
	"errors"
	"net"
	"time"
)

// maxDatagram bounds the receive buffer. PTP messages are small (a Sync or
// Follow_Up is 44 bytes, an Announce is 64 bytes plus a path-trace TLV); this
// bound is far larger than any realistic message.
const maxDatagram = 1 << 16

// monoStart is the fixed reference point for MonotonicNanos.
var monoStart = time.Now()

// MonotonicNanos returns a monotonic nanosecond counter. The origin is
// arbitrary; only deltas and cross-package consistency matter. It is suitable
// as the local clock for PTP offset estimation and playback scheduling, since
// time.Since uses the monotonic reading of both operands and never steps with
// wall-clock adjustments.
func MonotonicNanos() uint64 {
	return uint64(time.Since(monoStart))
}

// Listener receives PTP datagrams on a packet connection and updates a Clock
// from the Sync, Follow_Up, and Announce messages it observes. It binds the
// standard PTP ports (319 for event messages, 320 for general messages);
// AirPlay senders multicast Sync on 319 and Follow_Up/Announce on 320, and a
// single wildcard listener can serve both.
type Listener struct {
	conn  net.PacketConn
	clock *Clock
	now   func() uint64
}

// Listen binds a UDP socket on the given network/address and returns a
// Listener that updates c. An empty host binds all interfaces. Port 0 selects
// an ephemeral port for testing.
func Listen(network, addr string, c *Clock) (*Listener, error) {
	if c == nil {
		return nil, errors.New("ptp: nil clock")
	}
	ua, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP(network, ua)
	if err != nil {
		return nil, err
	}
	return newListener(conn, c, MonotonicNanos), nil
}

// newListener wraps an arbitrary packet connection and a clock source. It
// exists so Serve can be unit-tested with an in-memory connection.
func newListener(conn net.PacketConn, c *Clock, now func() uint64) *Listener {
	return &Listener{conn: conn, clock: c, now: now}
}

// Addr returns the bound address.
func (l *Listener) Addr() net.Addr { return l.conn.LocalAddr() }

// Serve reads datagrams until ctx is cancelled or the connection is closed.
// Each datagram is parsed and dispatched to the clock; parse failures and
// non-timing message types are ignored. A nil return means clean shutdown.
func (l *Listener) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = l.conn.Close() })
	defer stop()

	buf := make([]byte, maxDatagram)
	for {
		n, _, err := l.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		l.handle(buf[:n])
	}
}

// handle parses one datagram and updates the clock. The receive time is
// captured before parsing so the offset estimate reflects the actual arrival
// time.
func (l *Listener) handle(pkt []byte) {
	now := l.now()
	m, err := Parse(pkt)
	if err != nil {
		return
	}
	switch m.Header.MessageType {
	case TypeSync:
		l.clock.HandleSync(m, now)
	case TypeFollowUp:
		l.clock.HandleFollowUp(m, now)
	case TypeAnnounce:
		l.clock.HandleAnnounce(m)
	}
}

// Close closes the underlying connection, unblocking any in-progress Serve.
func (l *Listener) Close() error { return l.conn.Close() }
