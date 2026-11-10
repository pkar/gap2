package airplay2

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pkar/gap2/internal/plist"
	"github.com/pkar/gap2/pcm"
)

// countingSink counts the calls that release or discard output.
type countingSink struct{ flushes, closes atomic.Int32 }

func (s *countingSink) Write(context.Context, pcm.Block) error { return nil }
func (s *countingSink) Flush(context.Context) error            { s.flushes.Add(1); return nil }
func (s *countingSink) Position() pcm.Position                 { return pcm.Position{} }
func (s *countingSink) Close() error                           { s.closes.Add(1); return nil }

// countingFactory hands out a new countingSink per Open.
type countingFactory struct{ sinks []*countingSink }

func (f *countingFactory) Open(context.Context, pcm.Format) (pcm.Sink, error) {
	s := &countingSink{}
	f.sinks = append(f.sinks, s)
	return s, nil
}

func mustPlist(t *testing.T, fields map[string]*plist.Value) string {
	t.Helper()
	body, err := plist.Encode(plist.Dict(fields))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// A repeated timing SETUP must answer with the event port it already opened
// instead of leaving a listener behind each time.
func TestRepeatedInitialSetupKeepsEventListener(t *testing.T) {
	s := newTestMediaServer(t, &countingFactory{})
	var wire bytes.Buffer
	cs := &connState{log: s.log, w: &wire}
	h := newMediaHandler(s.cfg, s.log, s.clock)
	cs.media = h
	defer h.close()
	opened := 0
	h.listenEvent = func() (net.Listener, error) {
		opened++
		return &fakeListener{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5000 + opened}}, nil
	}
	body := mustPlist(t, map[string]*plist.Value{"timingProtocol": plist.String("PTP")})
	var first string
	for i := 0; i < 3; i++ {
		wire.Reset()
		if err := s.handleMedia(cs, mediaRequest("SETUP", "rtsp://host/1", "1", nil, body)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(wire.String(), "200 OK") {
			t.Fatalf("SETUP %d = %q", i, wire.String())
		}
		if i == 0 {
			first = wire.String()
		} else if wire.String() != first {
			t.Fatalf("SETUP %d advertised a different event port", i)
		}
	}
	if opened != 1 {
		t.Fatalf("opened %d event listeners, want 1", opened)
	}
}

// A second stream SETUP is refused before it binds, so the first stream's
// socket is neither replaced nor leaked.
func TestRepeatedStreamSetupDoesNotRebind(t *testing.T) {
	for _, native := range []bool{false, true} {
		s := newTestMediaServer(t, &countingFactory{})
		var wire bytes.Buffer
		cs := &connState{log: s.log, w: &wire}
		h := newMediaHandler(s.cfg, s.log, s.clock)
		cs.media = h
		binds := 0
		h.bind = func(string, string) (int, error) { binds++; return 7000, nil }
		h.listenControl = func() (net.PacketConn, error) { return &fakePacketConn{addr: &net.UDPAddr{Port: 7001}}, nil }
		var setup *ctlRequest
		if native {
			setup = mediaRequest("SETUP", "rtsp://host/1", "2", nil, mustPlist(t, map[string]*plist.Value{
				"streams": plist.Array(plist.Dict(map[string]*plist.Value{
					"type": plist.Int(96), "ct": plist.Int(2), "sr": plist.Int(44100), "ch": plist.Int(2), "spf": plist.Int(352), "shk": plist.Data(make([]byte, 32)),
				})),
			}))
		} else {
			if err := s.handleMedia(cs, mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody)); err != nil {
				t.Fatal(err)
			}
			setup = mediaRequest("SETUP", "rtsp://host/1", "2", map[string]string{"Transport": "RTP/AVP/UDP;unicast;client_port=6000-6001"}, "")
		}
		for i, want := range []string{"200 OK", "455"} {
			wire.Reset()
			if err := s.handleMedia(cs, setup); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(wire.String(), want) {
				t.Fatalf("native=%v SETUP %d = %q, want %s", native, i, wire.String(), want)
			}
		}
		if binds != 1 {
			t.Fatalf("native=%v bound %d sockets, want 1", native, binds)
		}
		h.close()
	}
}

// A second ANNOUNCE without a TEARDOWN closes the first stream's output and
// lets RECORD start the new stream.
func TestSecondAnnounceReplacesStream(t *testing.T) {
	factory := &countingFactory{}
	s := newTestMediaServer(t, factory)
	var wire bytes.Buffer
	cs := &connState{log: s.log, w: &wire}
	h := newMediaHandler(s.cfg, s.log, s.clock)
	cs.media = h
	defer h.close()
	h.bind = func(string, string) (int, error) { return 7000, nil }
	play := func() {
		t.Helper()
		for _, req := range []*ctlRequest{
			mediaRequest("ANNOUNCE", "rtsp://host/1", "1", nil, mediaAACBody),
			mediaRequest("SETUP", "rtsp://host/1", "2", map[string]string{"Transport": "RTP/AVP/UDP;unicast;client_port=6000-6001"}, ""),
			mediaRequest("RECORD", "rtsp://host/1", "3", nil, ""),
		} {
			wire.Reset()
			if err := s.handleMedia(cs, req); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(wire.String(), "200 OK") {
				t.Fatalf("%s = %q", req.method, wire.String())
			}
		}
	}
	play()
	first := h.sess
	play()
	if len(factory.sinks) != 2 {
		t.Fatalf("opened %d outputs, want 2", len(factory.sinks))
	}
	if got := factory.sinks[0].closes.Load(); got != 1 {
		t.Fatalf("first output closed %d times, want 1", got)
	}
	if got := factory.sinks[1].closes.Load(); got != 0 {
		t.Fatalf("second output closed %d times, want 0", got)
	}
	if h.sess == first || !h.mediaStarted || h.mediaCancel == nil {
		t.Fatal("second stream was not started")
	}
}

// A FLUSHBUFFERED with a start point applies when playback gets there, so it
// must not throw away the audio queued before that point.
func TestDeferredFlushKeepsQueuedOutput(t *testing.T) {
	factory := &countingFactory{}
	s := newTestMediaServer(t, factory)
	var wire bytes.Buffer
	cs := &connState{log: s.log, w: &wire}
	h := newMediaHandler(s.cfg, s.log, s.clock)
	cs.media = h
	defer h.close()
	h.bind = func(string, string) (int, error) { return 7000, nil }
	h.listenControl = func() (net.PacketConn, error) { return &fakePacketConn{addr: &net.UDPAddr{Port: 7001}}, nil }
	wire.Reset()
	if err := s.handleMedia(cs, mediaRequest("SETUP", "rtsp://host/1", "1", nil, mustPlist(t, map[string]*plist.Value{
		"streams": plist.Array(plist.Dict(map[string]*plist.Value{
			"type": plist.Int(103), "ct": plist.Int(4), "sr": plist.Int(44100), "ch": plist.Int(2), "spf": plist.Int(1024), "shk": plist.Data(make([]byte, 32)),
		})),
	}))); err != nil || !strings.Contains(wire.String(), "200 OK") {
		t.Fatalf("SETUP = %q, %v", wire.String(), err)
	}
	sink := factory.sinks[0]
	for _, tc := range []struct {
		name   string
		fields map[string]*plist.Value
		want   int32
	}{
		{"deferred by sequence", map[string]*plist.Value{"flushUntilTS": plist.Int(9000), "flushUntilSeq": plist.Int(90), "flushFromTS": plist.Int(5000), "flushFromSeq": plist.Int(50)}, 0},
		{"deferred by timestamp", map[string]*plist.Value{"flushUntilTS": plist.Int(9000), "flushFromTS": plist.Int(5000)}, 0},
		{"immediate", map[string]*plist.Value{"flushUntilTS": plist.Int(9000), "flushUntilSeq": plist.Int(90)}, 1},
	} {
		wire.Reset()
		if err := s.handleMedia(cs, mediaRequest("FLUSHBUFFERED", "rtsp://host/1", "2", nil, mustPlist(t, tc.fields))); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(wire.String(), "200 OK") {
			t.Fatalf("%s: %q", tc.name, wire.String())
		}
		if got := sink.flushes.Load(); got != tc.want {
			t.Fatalf("%s: output flushed %d times, want %d", tc.name, got, tc.want)
		}
	}
}
