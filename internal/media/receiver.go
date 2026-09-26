// Package media implements the RTP media transport: binding the UDP socket
// that receives RTP packets from a sender and dispatching each datagram to a
// handler. Packet parsing, access-unit assembly, and decoding live in the rtp,
// stream, and aac packages so this package stays transport-only and easily
// testable against a loopback socket.
package media

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/pkar/gap2/internal/sdp"
	"github.com/pkar/gap2/internal/stream"
	"github.com/pkar/gap2/pcm"
)

// maxDatagram bounds the receive buffer. It is far larger than any realistic
// RTP MTU (typically <= 1500 bytes) while still bounding per-socket memory.
const maxDatagram = 1 << 16

// BufferedAudioBytes bounds encoded audio queued ahead of presentation.
const BufferedAudioBytes = 8 << 20

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

// Serve reads datagrams ahead of playout until ctx is cancelled or the
// connection is closed. Scheduling a future presentation must not stop draining
// the UDP socket: unlike TCP, a full kernel receive queue loses audio packets.
// For each datagram it calls handle with a slice valid only for the duration
// of the call; handle must not retain it. A nil return means clean shutdown;
// otherwise the read or handler error is returned.
func (r *Receiver) Serve(ctx context.Context, handle func(pkt []byte) error) error {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { _ = r.conn.Close() })
	defer func() {
		cancel()
		_ = r.conn.Close() // also unblock read-ahead when the handler fails
		stop()
	}()
	err := queuePackets(ctx, r.read, handle)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (r *Receiver) read(handle func([]byte) error) error {
	buf := make([]byte, maxDatagram)
	for {
		n, _, err := r.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
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
	mu           sync.Mutex
	closed       bool
	stream       *stream.Stream
	recv         *Receiver
	tcp          net.Listener
	tcpConn      net.Conn
	decodePacket func([]byte) ([]byte, error)
	onRecovery   func(error)
}

// SetPacketDecoder installs authenticated packet decryption before Serve.
func (s *Session) SetPacketDecoder(decode func([]byte) ([]byte, error)) {
	s.decodePacket = decode
}

// SetRecoveryHandler installs diagnostics before Serve starts.
func (s *Session) SetRecoveryHandler(fn func(error)) { s.onRecovery = fn }

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
	if network == "tcp4" {
		ln, err := net.Listen(network, addr)
		if err != nil {
			return 0, err
		}
		s.tcp = ln
		return ln.Addr().(*net.TCPAddr).Port, nil
	}
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
	if s.tcp != nil {
		return s.serveBuffered(ctx)
	}
	if s.recv == nil {
		return errors.New("media: serve before bind")
	}
	return s.recv.Serve(ctx, func(pkt []byte) error {
		if s.decodePacket != nil {
			var err error
			pkt, err = s.decodePacket(pkt)
			if err != nil {
				return err
			}
		}
		return s.ingest(ctx, pkt)
	})
}

// Close tears down the receiver, unblocking any in-progress Serve.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.tcp != nil {
		if s.tcpConn != nil {
			_ = s.tcpConn.Close()
		}
		return s.tcp.Close()
	}
	if s.recv == nil {
		return nil
	}
	return s.recv.Close()
}

// serveBuffered reads AP2's two-byte big-endian length-prefixed TCP frames.
// The length includes the prefix. A bounded read-ahead queue keeps receiving
// while the sink schedules playout, then applies TCP backpressure when full.
func (s *Session) serveBuffered(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = s.tcp.Close() })
	defer stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.serveBufferedConnection(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed || errors.Is(err, net.ErrClosed) {
			return err
		}
		if s.onRecovery != nil {
			s.onRecovery(err)
		}
		if err := s.stream.Recover(ctx); err != nil {
			return err
		}
	}
}

func (s *Session) serveBufferedConnection(ctx context.Context) error {
	c, err := s.tcp.Accept()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.Close()
		return net.ErrClosed
	}
	s.tcpConn = c
	s.mu.Unlock()
	defer c.Close()
	stopConn := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopConn()
	return queueBuffered(ctx, c, func(pkt []byte) error {
		sequence := binary.BigEndian.Uint32(pkt[:4]) & 0x7fffff
		if s.decodePacket != nil {
			var err error
			pkt, err = s.decodePacket(pkt)
			if err != nil {
				return err
			}
		}
		return s.ingest(ctx, pkt, sequence)
	})
}

// Read ahead independently of playout so the sender's TCP window stays open
// while frames wait for their PTP deadlines. Both bytes and packet count are
// bounded; cancellation interrupts either side of the queue.
func queueBuffered(ctx context.Context, r io.Reader, handle func([]byte) error) error {
	return queuePackets(ctx, func(enqueue func([]byte) error) error {
		return readBuffered(r, enqueue)
	}, handle)
}

// queuePackets bounds read-ahead by both encoded bytes and packet count. It
// copies each packet before the transport reuses its read buffer. The caller
// must interrupt a blocked transport read when ctx is cancelled or this returns.
func queuePackets(ctx context.Context, read func(func([]byte) error) error, handle func([]byte) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	packets := make(chan []byte, 2048)
	result := make(chan error, 1)
	space := make(chan struct{}, 1)
	var queued atomic.Int64
	go func() {
		defer close(packets)
		result <- read(func(packet []byte) error {
			for queued.Load()+int64(len(packet)+2) > BufferedAudioBytes {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-space:
				}
			}
			copyOfPacket := append([]byte(nil), packet...)
			queued.Add(int64(len(packet) + 2))
			select {
			case packets <- copyOfPacket:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case packet, ok := <-packets:
			if !ok {
				return <-result
			}
			queued.Add(-int64(len(packet) + 2))
			select {
			case space <- struct{}{}:
			default:
			}
			if err := handle(packet); err != nil {
				return err
			}
		}
	}
}

func (s *Session) ingest(ctx context.Context, pkt []byte, sequence ...uint32) error {
	// Buffered streams start with an authenticated empty clock marker.
	if len(pkt) == 12 && binary.BigEndian.Uint32(pkt[8:12]) == 0 {
		return nil
	}
	var err error
	if len(sequence) > 0 {
		err = s.stream.IngestBufferedRTP(ctx, pkt, sequence[0])
	} else {
		err = s.stream.IngestRTP(ctx, pkt)
	}
	if err != nil && len(pkt) >= 12 {
		return fmt.Errorf("media: sequence %d timestamp %d format %d payload bytes %d: %w",
			binary.BigEndian.Uint16(pkt[2:4]), binary.BigEndian.Uint32(pkt[4:8]),
			binary.BigEndian.Uint32(pkt[8:12]), len(pkt)-12, err)
	}
	return err
}

func readBuffered(r io.Reader, handle func([]byte) error) error {
	var prefix [2]byte
	buf := make([]byte, 65535-2)
	for {
		if _, err := io.ReadFull(r, prefix[:]); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint16(prefix[:])) - 2
		if n < 12+16+8 {
			return fmt.Errorf("media: invalid buffered frame length %d", n+2)
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return err
		}
		if err := handle(buf[:n]); err != nil {
			return err
		}
	}
}
