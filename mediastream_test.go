package airplay2

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkar/gap2/internal/plist"
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
