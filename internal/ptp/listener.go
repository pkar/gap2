package ptp

import (
	"context"
	"errors"
	"fmt"
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

// ptpMulticast is the IPv4 PTP primary-domain multicast address (IEEE
// 1588-2008 Annex F, domain 0). AirPlay senders multicast Sync on port 319 and
// Follow_Up/Announce on port 320 to this group.
var ptpMulticast = net.IPv4(224, 0, 1, 129)

// ListenMulticast joins the PTP multicast group on the given interface and
// port, returning a Listener that updates c.
func ListenMulticast(iface *net.Interface, port int, c *Clock) (*Listener, error) {
	if c == nil {
		return nil, errors.New("ptp: nil clock")
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{
		IP:   ptpMulticast,
		Port: port,
	})
	if err != nil {
		return nil, err
	}
	return newListener(conn, c, MonotonicNanos), nil
}

// Group is a set of Listeners bound to the standard PTP ports across one or
// more interfaces, all feeding a single Clock.
type Group struct {
	listeners []*Listener
}

// ListenGroup joins the PTP multicast group on ports 319 and 320 for each
// named interface, or for every up, multicast-capable interface when ifaces is
// empty. If any socket fails to bind, the partially opened sockets are closed
// and the error is returned.
func ListenGroup(ifaces []string, c *Clock) (*Group, error) {
	if c == nil {
		return nil, errors.New("ptp: nil clock")
	}
	selected, err := multicastInterfaces(ifaces)
	if err != nil {
		return nil, err
	}
	g := &Group{}
	for _, ifc := range selected {
		for _, port := range []int{319, 320} {
			l, err := ListenMulticast(ifc, port, c)
			if err != nil {
				_ = g.Close()
				return nil, fmt.Errorf("ptp: listen %s:%d: %w", ifc.Name, port, err)
			}
			g.listeners = append(g.listeners, l)
		}
	}
	return g, nil
}

// Serve reads PTP messages from every socket until ctx is cancelled, all
// sockets are closed, or one returns an error. A nil return means clean
// shutdown. The first non-nil error is returned after the remaining sockets
// are stopped.
func (g *Group) Serve(ctx context.Context) error {
	if len(g.listeners) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, len(g.listeners))
	for _, l := range g.listeners {
		l := l
		go func() { errs <- l.Serve(ctx) }()
	}
	var first error
	for range g.listeners {
		if err := <-errs; err != nil && first == nil {
			first = err
			cancel()
		}
	}
	return first
}

// Close closes every socket and is idempotent.
func (g *Group) Close() error {
	var first error
	for _, l := range g.listeners {
		if err := l.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// multicastInterfaces resolves interface names or, when empty, returns every
// up, multicast-capable interface.
func multicastInterfaces(ifaces []string) ([]*net.Interface, error) {
	if len(ifaces) > 0 {
		out := make([]*net.Interface, 0, len(ifaces))
		for _, name := range ifaces {
			ifc, err := net.InterfaceByName(name)
			if err != nil {
				return nil, fmt.Errorf("ptp: interface %s: %w", name, err)
			}
			out = append(out, ifc)
		}
		return out, nil
	}
	all, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("ptp: list interfaces: %w", err)
	}
	var out []*net.Interface
	for i := range all {
		if all[i].Flags&net.FlagUp != 0 && all[i].Flags&net.FlagMulticast != 0 {
			out = append(out, &all[i])
		}
	}
	return out, nil
}
