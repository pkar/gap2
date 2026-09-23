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

	"github.com/pkar/gap2/internal/sdp"
	"github.com/pkar/gap2/internal/stream"
	"github.com/pkar/gap2/pcm"
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

// Session coordinates one media stream's UDP receive pipeline: it owns the
// per-stream decoder/sink state and the receiver that feeds RTP datagrams into
// it. The RTSP control layer drives Session through the ANNOUNCE/SETUP/RECORD
// lifecycle; this type keeps that wiring independent of the wire protocol.
type Session struct {
	stream *stream.Stream
	recv   *Receiver
}

// NewSession builds a Session for the announced media, decoding into sink. The
// stream is left in StateNew until its Announce method is called by the RTSP
// layer.
func NewSession(id string, m *sdp.Media, sink pcm.Sink) (*Session, error) {
	dec, err := stream.NewDecoder(m)
	if err != nil {
		return nil, err
	}
	return &Session{stream: stream.New(id, dec, sink)}, nil
}

// Stream exposes the underlying stream so the RTSP layer can advance its
// announce/setup/record state and query the derived format.
func (s *Session) Stream() *stream.Stream { return s.stream }

// Bind binds the RTP receiver on the given network/address and returns the
// bound UDP port for the RTSP SETUP response. Port 0 selects an ephemeral port.
func (s *Session) Bind(network, addr string) (int, error) {
	recv, err := Listen(network, addr)
	if err != nil {
		return 0, err
	}
	s.recv = recv
	ua, ok := recv.Addr().(*net.UDPAddr)
	if !ok {
		return 0, errors.New("media: receiver did not bind a UDP address")
	}
	return ua.Port, nil
}

// setReceiver injects a receiver, used by tests to avoid real sockets.
func (s *Session) setReceiver(recv *Receiver) { s.recv = recv }

// Serve reads RTP datagrams and feeds each to the stream until ctx is
// cancelled. It requires a prior Bind (or setReceiver). It returns nil on clean
// shutdown.
func (s *Session) Serve(ctx context.Context) error {
	if s.recv == nil {
		return errors.New("media: serve before bind")
	}
	return s.recv.Serve(ctx, func(pkt []byte) error {
		return s.stream.IngestRTP(ctx, pkt)
	})
}

// Close tears down the receiver, unblocking any in-progress Serve.
func (s *Session) Close() error {
	if s.recv == nil {
		return nil
	}
	return s.recv.Close()
}
