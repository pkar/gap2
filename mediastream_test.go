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
	"github.com/pkar/gap2/internal/stream"
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

// TestMediaAnnounceRejectsCodec ensures a codec the decoder cannot handle (for
// example 24-bit ALAC) is rejected with 415 before the output sink is opened.
func TestMediaAnnounceRejectsCodec(t *testing.T) {
	factory := &mediaFactory{sink: &mediaRecordingSink{}}
	s := newTestMediaServer(t, factory)

	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}

	// 32-bit ALAC is unsupported; fail without opening output.
	body := "v=0\r\n" +
		"o=iTunes 3413825038 0 IN IP4 192.168.1.2\r\n" +
		"s=iTunes\r\n" +
		"c=IN IP4 192.168.1.2\r\n" +
		"t=0 0\r\n" +
		"m=audio 0 RTP/AVP 96\r\n" +
		"a=rtpmap:96 AppleLossless\r\n" +
		"a=fmtp:96 352 0 32 40 10 14 2 255 0 0 44100\r\n"

	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, body)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 415") {
		t.Fatalf("unsupported-codec response = %q", got)
	}
	if factory.sink.format.Rate != 0 {
		t.Fatalf("sink opened for rejected codec: format = %+v", factory.sink.format)
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
func TestNativeAP2RecordBeforeStreamSetup(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		streamType, codec, frames, channels int64
		network                             string
	}{
		{"realtime", 96, 2, 352, 2, "udp4"}, {"buffered", 103, 4, 1024, 2, "tcp4"},
		{"surround51", 103, 4, 1024, 6, "tcp4"}, {"surround71", 103, 4, 1024, 8, "tcp4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := &mediaFactory{sink: &mediaRecordingSink{}}
			s := newTestMediaServer(t, factory)
			s.cfg.OutputRate = 48000
			s.cfg.OutputChannels = 2
			var wire bytes.Buffer
			cs := &connState{log: s.log, w: &wire}
			h := newMediaHandler(s.cfg, s.log, s.clock)
			cs.media = h
			defer h.close()
			h.eventLn = &fakeListener{addr: &net.TCPAddr{Port: 5000}}
			if err := h.record(cs, mediaRequest("RECORD", "rtsp://host/1", "1", nil, "")); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(wire.String(), "200 OK") || !h.recordRequested {
				t.Fatal("rejected early AP2 RECORD")
			}
			h.bind = func(network, _ string) (int, error) {
				if network != tc.network {
					t.Fatalf("bound %s, want %s", network, tc.network)
				}
				return 7000, nil
			}
			h.listenControl = func() (net.PacketConn, error) { return &fakePacketConn{addr: &net.UDPAddr{Port: 7001}}, nil }
			body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
				"streams": plist.Array(plist.Dict(map[string]*plist.Value{
					"type": plist.Int(tc.streamType), "ct": plist.Int(tc.codec), "sr": plist.Int(44100), "ch": plist.Int(tc.channels), "spf": plist.Int(tc.frames), "shk": plist.Data(make([]byte, 32)),
				})),
			}))
			if err != nil {
				t.Fatal(err)
			}
			wire.Reset()
			if err := h.setup(cs, mediaRequest("SETUP", "rtsp://host/1", "2", nil, string(body))); err != nil {
				t.Fatal(err)
			}
			if factory.sink.format.Rate != 48000 || factory.sink.format.Channels != 2 {
				t.Fatalf("opened output %v", factory.sink.format)
			}
			if !strings.Contains(wire.String(), "200 OK") || !h.mediaStarted {
				t.Fatal("native stream did not start after SETUP")
			}
			if h.sess.Stream().Format().Rate != 44100 || h.sess.Stream().Format().Channels != int(tc.channels) {
				t.Fatalf("wrong source format: %+v", h.sess.Stream().Format())
			}
			teardown, err := plist.Encode(plist.Dict(map[string]*plist.Value{
				"streams": plist.Array(plist.Dict(map[string]*plist.Value{"type": plist.Int(tc.streamType)})),
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := h.teardown(cs, mediaRequest("TEARDOWN", "rtsp://host/1", "3", nil, string(teardown))); err != nil {
				t.Fatal(err)
			}
			if h.ctx.Err() != nil || h.eventLn == nil || cs.media != h || h.sess != nil {
				t.Fatal("stream teardown closed the enclosing AirPlay session")
			}
			factory.sink = &mediaRecordingSink{}
			wire.Reset()
			if err := h.setup(cs, mediaRequest("SETUP", "rtsp://host/1", "4", nil, string(body))); err != nil {
				t.Fatal(err)
			}
			if !h.mediaStarted || !strings.Contains(wire.String(), "200 OK") {
				t.Fatal("replacement stream failed:", wire.String())
			}
		})
	}
}

func TestMediaVolumeQuery(t *testing.T) {
	s := newTestMediaServer(t, nil)
	var wire bytes.Buffer
	cs := &connState{log: s.log, w: &wire}
	if err := s.handleMedia(cs, mediaRequest("SET_PARAMETER", "*", "1", nil, "volume: -15.5\r\n")); err != nil {
		t.Fatal(err)
	}
	wire.Reset()
	if err := s.handleMedia(cs, mediaRequest("GET_PARAMETER", "*", "2", nil, "volume\r\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wire.String(), "volume: -15.500000\r\n") {
		t.Fatal("volume query did not return the set value")
	}
}

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
	cs := &connState{log: s.log, w: buf, sessionKey: make([]byte, 32)}

	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()

	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"rtpTime":               plist.Int(88200),
		"networkTimeSecs":       plist.Int(1000),
		"networkTimeFrac":       plist.Int(1 << 62), // 0.25s
		"networkTimeTimelineID": plist.Int(0x12345678),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleRequest(cs, mediaRequest("SETRATEANCHORTIME", "rtsp://host/1", "2", nil, string(body))); err != nil {
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
	if binary.BigEndian.Uint64(a.ClockID[:]) != 0x12345678 {
		t.Fatal("anchor clock identity lost")
	}
	// networkTimeSecs=1000, networkTimeFrac=1<<62 (0.25s) -> 1000.25s in ns.
	if want := uint64(1000_250_000_000); a.MasterNs != want {
		t.Fatalf("anchor master ns = %d, want %d", a.MasterNs, want)
	}
	buf.Reset()
	body, err = plist.Encode(plist.Dict(map[string]*plist.Value{"rate": plist.Int(0)}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.handleRequest(cs, mediaRequest("SETRATEANCHORTIME", "rtsp://host/1", "3", nil, string(body))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "200 OK") {
		t.Fatal("rate-only pause rejected:", buf.String())
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
		h.serveControl(conn, stream.New("test", nil, nil))
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
	h.serveControl(nil, nil)
}

// testClockID is a fixed grandmaster clock identity used in code-215 packets.
var testClockID = [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

// ap2TimingPacket builds a synthetic code-215 anchoring announcement with the
// given frame, grandmaster time, and a fixed clock identity.
func ap2TimingPacket(frame uint32, masterNs uint64) []byte {
	return ap2TimingPacketClock(frame, masterNs, testClockID)
}

// ap2TimingPacketClock builds a synthetic code-215 announcement with an
// explicit clock identity.
func ap2TimingPacketClock(frame uint32, masterNs uint64, clockID [8]byte) []byte {
	pkt := make([]byte, 28)
	pkt[1] = ap2TimingSyncCode
	binary.BigEndian.PutUint32(pkt[4:8], frame)
	binary.BigEndian.PutUint64(pkt[8:16], masterNs)
	copy(pkt[20:28], clockID[:])
	return pkt
}

// TestAp2ControlAnchor parses a code-215 packet and checks the extracted
// frame/grandmaster/clock mapping, plus rejection of short and wrong-type
// packets.
func TestAp2ControlAnchor(t *testing.T) {
	pkt := ap2TimingPacket(0x11223344, 0x8877665544332211)
	frame, masterNs, clockID, ok := ap2ControlAnchor(pkt)
	if !ok {
		t.Fatal("ap2ControlAnchor rejected a valid timing-sync packet")
	}
	if frame != 0x11223344 || masterNs != 0x8877665544332211 {
		t.Fatalf("anchor = (%#x, %#x), want (0x11223344, 0x8877665544332211)", frame, masterNs)
	}
	if clockID != testClockID {
		t.Fatalf("clockID = %x, want %x", clockID, testClockID)
	}

	if _, _, _, ok := ap2ControlAnchor(nil); ok {
		t.Fatal("ap2ControlAnchor accepted a nil packet")
	}
	short := ap2TimingPacket(1, 2)[:27]
	if _, _, _, ok := ap2ControlAnchor(short); ok {
		t.Fatal("ap2ControlAnchor accepted a short packet")
	}
	wrong := ap2TimingPacket(1, 2)
	wrong[1] = ap2TimingSyncCode + 1
	if _, _, _, ok := ap2ControlAnchor(wrong); ok {
		t.Fatal("ap2ControlAnchor accepted a wrong-type packet")
	}
}

// TestHandleControlPacket verifies that a code-215 packet updates the clock
// anchor, recording the clock identity.
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
	if a.ClockID != testClockID {
		t.Fatalf("anchor clockID = %x, want %x", a.ClockID, testClockID)
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

// TestHandleControlPacketClockMatch ensures a code-215 packet whose clock
// matches the selected grandmaster is adopted.
func TestHandleControlPacketClockMatch(t *testing.T) {
	clock := ptp.NewClock()
	clock.HandleAnnounce(ptp.Message{Grandmaster: testClockID})
	h := &mediaHandler{log: slog.Default(), clock: clock}
	h.handleControlPacket(ap2TimingPacket(1000, 5_000_000_000), 44100)

	a, ok := clock.Anchor()
	if !ok {
		t.Fatal("anchor not set for matching clock")
	}
	if a.ClockID != testClockID {
		t.Fatalf("anchor clockID = %x, want %x", a.ClockID, testClockID)
	}
}

// TestHandleControlPacketClockMismatch ensures a code-215 packet for a clock
// other than the selected grandmaster is rejected.
func TestHandleControlPacketClockMismatch(t *testing.T) {
	clock := ptp.NewClock()
	master := [8]byte{0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88}
	clock.HandleAnnounce(ptp.Message{Grandmaster: master})
	h := &mediaHandler{log: slog.Default(), clock: clock}
	h.handleControlPacket(ap2TimingPacket(1000, 5_000_000_000), 44100) // testClockID != master

	if _, ok := clock.Anchor(); ok {
		t.Fatal("anchor set despite clock mismatch")
	}
}
