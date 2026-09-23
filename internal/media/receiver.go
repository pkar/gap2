// Package media implements the RTP media transport: binding the UDP socket
// that receives RTP packets from a sender and dispatching each datagram to a
// handler. Packet parsing, access-unit assembly, and decoding live in the rtp,
// stream, and aac packages so this package stays transport-only and easily
// testable against a loopback socket.
package media

import (
	"context"
	"errors"
	"net"
)

// maxDatagram bounds the receive buffer. It is far larger than any realistic
// RTP MTU (typically <= 1500 bytes) while still bounding per-socket memory.
const maxDatagram = 1 << 16

// Receiver receives RTP datagrams from a bound packet connection.
type Receiver struct {
	conn net.PacketConn
}

// Listen binds a UDP socket on the given network ("udp", "udp4", or "udp6")
// and address, returning a receiver ready to serve. An empty host in addr
// binds all interfaces; port 0 selects an ephemeral port.
func Listen(network, addr string) (*Receiver, error) {
	ua, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP(network, ua)
	if err != nil {
		return nil, err
	}
	return &Receiver{conn: conn}, nil
}

// newReceiver wraps an arbitrary packet connection. It exists so Serve can be
// unit-tested with an in-memory connection that needs no network access.
func newReceiver(conn net.PacketConn) *Receiver {
	return &Receiver{conn: conn}
}

// Addr returns the bound address, which callers use to report the negotiated
// server_port in an RTSP SETUP response.
func (r *Receiver) Addr() net.Addr {
	return r.conn.LocalAddr()
}

// Serve reads datagrams until ctx is cancelled or the connection is closed.
// For each datagram it calls handle with a slice valid only for the duration
// of the call; handle must not retain it. A nil return means clean shutdown;
// otherwise the read or handler error is returned.
func (r *Receiver) Serve(ctx context.Context, handle func(pkt []byte) error) error {
	stop := context.AfterFunc(ctx, func() { _ = r.conn.Close() })
	defer stop()

	buf := make([]byte, maxDatagram)
	for {
		n, _, err := r.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if err := handle(buf[:n]); err != nil {
			return err
		}
	}
}

// Close closes the underlying connection, unblocking any in-progress Serve.
func (r *Receiver) Close() error {
	return r.conn.Close()
}
