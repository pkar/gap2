package airplay2

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
	return newControlServer(cfg, id, store)
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
	s := newControlServer(DefaultConfig(), id, store) // Output nil

	buf := &bytes.Buffer{}
	cs := &connState{log: s.log, w: buf}
	if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody)); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "RTSP/1.0 503") {
		t.Fatalf("no-output ANNOUNCE response = %q", got)
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
