package airplay2

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkar/gap2/internal/plist"
	"github.com/pkar/gap2/internal/ptp"
	"github.com/pkar/gap2/pcm"
)

// mediaRecordingSink implements pcm.Sink and records the last opened format.
type mediaRecordingSink struct {
	format pcm.Format
}

func (s *mediaRecordingSink) Write(context.Context, pcm.Block) error { return nil }
func (s *mediaRecordingSink) Flush(context.Context) error            { return nil }
func (s *mediaRecordingSink) Position() pcm.Position                 { return pcm.Position{} }
func (s *mediaRecordingSink) Close() error                           { return nil }

type mediaFactory struct{ sink *mediaRecordingSink }

func (f *mediaFactory) Open(_ context.Context, format pcm.Format) (pcm.Sink, error) {
	f.sink.format = format
	return f.sink, nil
}

func newTestMediaServer(t *testing.T, factory pcm.Factory) *controlServer {
	t.Helper()
	store, err := loadPairingStore("")
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.ensureIdentity(nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Output = factory
	return newControlServer(cfg, id, store, nil)
}

const mediaAACBody = "v=0\r\n" +
	"o=iTunes 3413825038 0 IN IP4 192.168.1.2\r\n" +
	"s=iTunes\r\n" +
	"c=IN IP4 192.168.1.2\r\n" +
	"t=0 0\r\n" +
	"m=audio 0 RTP/AVP 96\r\n" +
	"a=rtpmap:96 mpeg4-generic/44100/2\r\n" +
	"a=fmtp:96 streamtype=5; profile-level-id=1; mode=AAC-hbr; config=1210; sizeLength=13; indexLength=3; indexDeltaLength=3; constantDuration=1024\r\n"

func mediaRequest(method, target, cseq string, headers map[string]string, body string) *ctlRequest {
	h := map[string][]string{"cseq": {cseq}}
	for k, v := range headers {
		h[strings.ToLower(k)] = []string{v}
	}
	return &ctlRequest{method: method, target: target, headers: h, body: []byte(body)}
}

// TestMediaHandlerFlow drives ANNOUNCE -> SETUP -> RECORD -> TEARDOWN through
// handleMedia and checks the RTSP responses.
func TestMediaHandlerFlow(t *testing.T) {
	factory := &mediaFactory{sink: &mediaRecordingSink{}}
	s := newTestMediaServer(t, factory)

	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}

	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 200 OK") || !strings.Contains(got, "CSeq: 1") {
		t.Fatalf("ANNOUNCE response = %q", got)
	}
	if factory.sink.format.Rate != 44100 || factory.sink.format.Channels != 2 {
		t.Fatalf("sink format = %+v, want 44100 Hz stereo", factory.sink.format)
	}
	buf.Reset()

	// Stub the socket bind so SETUP does not need network access.
	cs.media.bind = func(_, _ string) (int, error) { return 7000, nil }

	if err := s.handleMedia(cs, mediaRequest("SETUP", "rtsp://host/1", "2", map[string]string{
		"Transport": "RTP/AVP/UDP;unicast;client_port=6000-6001",
	}, "")); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "RTSP/1.0 200 OK") {
		t.Fatalf("SETUP response = %q", got)
	}
	if !strings.Contains(got, "server_port=7000-7001") {
		t.Fatalf("SETUP missing server_port: %q", got)
	}
	if !strings.Contains(got, "Session: 1") {
		t.Fatalf("SETUP missing Session header: %q", got)
	}
	buf.Reset()

	if err := s.handleMedia(cs, mediaRequest("RECORD", "rtsp://host/1", "3", nil, "")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 200 OK") {
		t.Fatalf("RECORD response = %q", got)
	}
	buf.Reset()

	if err := s.handleMedia(cs, mediaRequest("TEARDOWN", "rtsp://host/1", "4", nil, "")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 200 OK") {
		t.Fatalf("TEARDOWN response = %q", got)
	}
}

// TestMediaHandlerErrors covers the state and validation error paths.
func TestMediaHandlerErrors(t *testing.T) {
	factory := &mediaFactory{sink: &mediaRecordingSink{}}
	s := newTestMediaServer(t, factory)
	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}

	// SETUP before ANNOUNCE.
	if err := s.handleMedia(cs, mediaRequest("SETUP", "rtsp://host/1", "1", map[string]string{
		"Transport": "RTP/AVP/UDP;unicast;client_port=6000-6001",
	}, "")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 455") {
		t.Fatalf("SETUP-before-ANNOUNCE response = %q", got)
	}
	buf.Reset()

	// ANNOUNCE with an unsupported encoding (rejected by the SDP parser).
	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "2", nil, "v=0\r\nm=audio 0 RTP/AVP 96\r\na=rtpmap:96 L16/44100/2\r\n")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 400") {
		t.Fatalf("unsupported-encoding response = %q", got)
	}
}

// TestMediaAnnounceNoOutput returns 503 when the receiver has no PCM sink.
func TestMediaAnnounceNoOutput(t *testing.T) {
	store, err := loadPairingStore("")
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.ensureIdentity(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := newControlServer(DefaultConfig(), id, store, nil) // Output nil

	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}
	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 503") {
		t.Fatalf("no-output ANNOUNCE response = %q", got)
	}
}

// fakeTCPAddr and fakeListener stand in for a bound TCP listener in tests.
type fakeTCPAddr struct{ port int }

func (fakeTCPAddr) Network() string  { return "tcp" }
func (a fakeTCPAddr) String() string { return "127.0.0.1:" + itoa(a.port) }

func itoa(n int) string { return strconv.Itoa(n) }

type fakeListener struct{ addr net.Addr }

func (l *fakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *fakeListener) Close() error              { return nil }
func (l *fakeListener) Addr() net.Addr            { return l.addr }

// fakeUDPAddr and fakePacketConn stand in for a bound UDP socket in tests.
type fakeUDPAddr struct{ port int }

func (fakeUDPAddr) Network() string  { return "udp" }
func (a fakeUDPAddr) String() string { return "127.0.0.1:" + itoa(a.port) }

type fakePacketConn struct{ addr net.Addr }

func (c *fakePacketConn) ReadFrom(b []byte) (int, net.Addr, error)  { return 0, nil, net.ErrClosed }
func (c *fakePacketConn) WriteTo(b []byte, a net.Addr) (int, error) { return len(b), nil }
func (c *fakePacketConn) Close() error                              { return nil }
func (c *fakePacketConn) LocalAddr() net.Addr                       { return c.addr }
func (c *fakePacketConn) SetDeadline(time.Time) error               { return nil }
func (c *fakePacketConn) SetReadDeadline(time.Time) error           { return nil }
func (c *fakePacketConn) SetWriteDeadline(time.Time) error          { return nil }

// TestMediaHandlerAp2Setup drives the AirPlay 2 plist SETUP exchanges: the
// initial (timing) SETUP followed by the stream SETUP.
func TestMediaHandlerAp2Setup(t *testing.T) {
	factory := &mediaFactory{sink: &mediaRecordingSink{}}
	s := newTestMediaServer(t, factory)
	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}
	// Pre-create the media handler so we can inject the socket factories
	// before the first SETUP (which runs before ANNOUNCE).
	cs.media = newMediaHandler(s.cfg, s.log, s.clock)

	// Initial (timing) SETUP with PTP. No ANNOUNCE has happened yet.
	cs.media.listenEvent = func() (net.Listener, error) {
		return &fakeListener{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5000}}, nil
	}
	initBody, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"timingProtocol": plist.String("PTP"),
		"groupUUID":      plist.String("00000000-0000-0000-0000-000000000000"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleMedia(cs, mediaRequest("SETUP", "rtsp://host/1", "1", nil, string(initBody))); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "RTSP/1.0 200 OK") {
		t.Fatalf("initial SETUP response = %q", got)
	}
	if !strings.Contains(got, "Content-Type: application/x-apple-binary-plist") {
		t.Fatalf("initial SETUP missing plist Content-Type: %q", got)
	}
	buf.Reset()

	// ANNOUNCE the audio stream, then the stream SETUP (type 96).
	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "2", nil, mediaAACBody)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()

	cs.media.bind = func(_, _ string) (int, error) { return 7000, nil }
	cs.media.listenControl = func() (net.PacketConn, error) {
		return &fakePacketConn{addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7001}}, nil
	}
	streamBody, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"streams": plist.Array(plist.Dict(map[string]*plist.Value{
			"type":     plist.Int(96),
			"dataPort": plist.Int(6000),
		})),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleMedia(cs, mediaRequest("SETUP", "rtsp://host/1", "3", nil, string(streamBody))); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 200 OK") {
		t.Fatalf("stream SETUP response = %q", got)
	}
}

// TestMediaHandlerAp2SetupRejectsNTP returns 400 for a non-PTP timing setup.
func TestMediaHandlerAp2SetupRejectsNTP(t *testing.T) {
	factory := &mediaFactory{sink: &mediaRecordingSink{}}
	s := newTestMediaServer(t, factory)
	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}

	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"timingProtocol": plist.String("NTP"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleMedia(cs, mediaRequest("SETUP", "rtsp://host/1", "1", nil, string(body))); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 400") {
		t.Fatalf("NTP SETUP response = %q, want 400", got)
	}
}

// playback anchor is recorded against the negotiated sample rate.
func TestMediaSetRateAnchor(t *testing.T) {
	factory := &mediaFactory{sink: &mediaRecordingSink{}}
	s := newTestMediaServer(t, factory)
	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}

	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()

	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"rtpTime":         plist.Int(88200),
		"networkTimeSecs": plist.Int(1000),
		"networkTimeFrac": plist.Int(1 << 62), // 0.25s
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleMedia(cs, mediaRequest("SETRATEANCHORI", "rtsp://host/1", "2", nil, string(body))); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 200 OK") {
		t.Fatalf("SETRATEANCHORI response = %q", got)
	}

	a, ok := s.clock.Anchor()
	if !ok {
		t.Fatal("anchor not set")
	}
	if a.Frame != 88200 {
		t.Fatalf("anchor frame = %d, want 88200", a.Frame)
	}
	if a.Rate != 44100 {
		t.Fatalf("anchor rate = %d, want 44100", a.Rate)
	}
	// networkTimeSecs=1000, networkTimeFrac=1<<62 (0.25s) -> 1000.25s in ns.
	if want := uint64(1000_250_000_000); a.MasterNs != want {
		t.Fatalf("anchor master ns = %d, want %d", a.MasterNs, want)
	}
}

// TestMediaSetRateAnchorBadBody covers malformed and missing-field bodies.
func TestMediaSetRateAnchorBadBody(t *testing.T) {
	factory := &mediaFactory{sink: &mediaRecordingSink{}}
	s := newTestMediaServer(t, factory)
	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}

	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()

	// Malformed plist body.
	if err := s.handleMedia(cs, mediaRequest("SETRATEANCHORI", "rtsp://host/1", "2", nil, "\x00not-a-plist")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 400") {
		t.Fatalf("malformed SETRATEANCHORI response = %q", got)
	}
	buf.Reset()

	// Missing rtpTime field.
	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"networkTimeSecs": plist.Int(1000),
		"networkTimeFrac": plist.Int(0),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleMedia(cs, mediaRequest("SETRATEANCHORI", "rtsp://host/1", "3", nil, string(body))); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 400") {
		t.Fatalf("missing-field SETRATEANCHORI response = %q", got)
	}
}

// TestMediaRequiresEncryption ensures RTSP media methods are rejected before
// pair verification completes.
func TestMediaRequiresEncryption(t *testing.T) {
	s := newTestMediaServer(t, &mediaFactory{sink: &mediaRecordingSink{}})
	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}

	if err := s.handleRequest(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "HTTP/1.1 401") {
		t.Fatalf("unauthenticated ANNOUNCE response = %q", got)
	}
}

// feedbackPacketConn delivers the packets in packets one per ReadFrom call and
// then returns net.ErrClosed, so a drain loop consumes everything and exits.
type feedbackPacketConn struct {
	addr    net.Addr
	packets [][]byte
}

func (c *feedbackPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if len(c.packets) == 0 {
		return 0, nil, net.ErrClosed
	}
	n := copy(b, c.packets[0])
	c.packets = c.packets[1:]
	return n, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 6000}, nil
}
func (c *feedbackPacketConn) WriteTo(b []byte, a net.Addr) (int, error) { return len(b), nil }
func (c *feedbackPacketConn) Close() error                              { return nil }
func (c *feedbackPacketConn) LocalAddr() net.Addr                       { return c.addr }
func (c *feedbackPacketConn) SetDeadline(time.Time) error               { return nil }
func (c *feedbackPacketConn) SetReadDeadline(time.Time) error           { return nil }
func (c *feedbackPacketConn) SetWriteDeadline(time.Time) error          { return nil }

// TestServeControlDrains checks that the control-port drain loop consumes the
// packets the sender posts and exits once the socket is closed.
func TestServeControlDrains(t *testing.T) {
	conn := &feedbackPacketConn{
		addr:    &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7001},
		packets: [][]byte{[]byte("pkt0"), []byte("pkt1")},
	}
	h := &mediaHandler{log: slog.Default()}
	done := make(chan struct{})
	go func() {
		h.serveControl(conn, 44100)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveControl did not exit after draining packets")
	}
	if len(conn.packets) != 0 {
		t.Fatalf("serveControl left %d packets undrained", len(conn.packets))
	}
}

// TestServeControlNil is a no-op guard for a nil control socket.
func TestServeControlNil(t *testing.T) {
	h := &mediaHandler{log: slog.Default()}
	h.serveControl(nil, 44100)
}

// ap2TimingPacket builds a synthetic code-215 anchoring announcement with the
// given frame and grandmaster time.
func ap2TimingPacket(frame uint32, masterNs uint64) []byte {
	pkt := make([]byte, 28)
	pkt[1] = ap2TimingSyncCode
	binary.BigEndian.PutUint32(pkt[4:8], frame)
	binary.BigEndian.PutUint64(pkt[8:16], masterNs)
	return pkt
}

// TestAp2ControlAnchor parses a code-215 packet and checks the extracted
// frame/grandmaster mapping, plus rejection of short and wrong-type packets.
func TestAp2ControlAnchor(t *testing.T) {
	pkt := ap2TimingPacket(0x11223344, 0x8877665544332211)
	frame, masterNs, ok := ap2ControlAnchor(pkt)
	if !ok {
		t.Fatal("ap2ControlAnchor rejected a valid timing-sync packet")
	}
	if frame != 0x11223344 || masterNs != 0x8877665544332211 {
		t.Fatalf("anchor = (%#x, %#x), want (0x11223344, 0x8877665544332211)", frame, masterNs)
	}

	if _, _, ok := ap2ControlAnchor(nil); ok {
		t.Fatal("ap2ControlAnchor accepted a nil packet")
	}
	short := ap2TimingPacket(1, 2)[:15]
	if _, _, ok := ap2ControlAnchor(short); ok {
		t.Fatal("ap2ControlAnchor accepted a short packet")
	}
	wrong := ap2TimingPacket(1, 2)
	wrong[1] = ap2TimingSyncCode + 1
	if _, _, ok := ap2ControlAnchor(wrong); ok {
		t.Fatal("ap2ControlAnchor accepted a wrong-type packet")
	}
}

// TestHandleControlPacket verifies that a code-215 packet updates the clock
// anchor.
func TestHandleControlPacket(t *testing.T) {
	clock := ptp.NewClock()
	h := &mediaHandler{log: slog.Default(), clock: clock}
	h.handleControlPacket(ap2TimingPacket(1000, 5_000_000_000), 44100)

	a, ok := clock.Anchor()
	if !ok {
		t.Fatal("anchor not set")
	}
	if a.Frame != 1000 || a.MasterNs != 5_000_000_000 || a.Rate != 44100 {
		t.Fatalf("anchor = %+v, want frame 1000 master 5000000000 rate 44100", a)
	}
}

// TestHandleControlPacketNoClock ensures a nil clock is a no-op.
func TestHandleControlPacketNoClock(t *testing.T) {
	h := &mediaHandler{log: slog.Default()}
	h.handleControlPacket(ap2TimingPacket(1000, 5_000_000_000), 44100) // must not panic
}

// TestHandleControlPacketZeroRate ensures a non-positive rate does not set an
// anchor.
func TestHandleControlPacketZeroRate(t *testing.T) {
	clock := ptp.NewClock()
	h := &mediaHandler{log: slog.Default(), clock: clock}
	h.handleControlPacket(ap2TimingPacket(1000, 5_000_000_000), 0)
	if _, ok := clock.Anchor(); ok {
		t.Fatal("anchor set for zero rate")
	}
}
