package airplay2

import (
	"bufio"
	"bytes"
	"testing"

	"github.com/pkar/gap2/internal/plist"
)

// TestEventCommandFraming checks that writeEventCommand and readEventRequest
// round-trip the event-channel request framing.
func TestEventCommandFraming(t *testing.T) {
	body := []byte{0x62, 0x70, 0x6c, 0x69, 0x73, 0x74} // "bplist"
	var b bytes.Buffer
	if err := writeEventCommand(&b, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	req, err := readEventRequest(bufio.NewReader(&b), DefaultLimits())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if req.method != "POST" || req.target != "/command" || req.version != "RTSP/1.0" {
		t.Fatalf("unexpected request line: %q %q %q", req.method, req.target, req.version)
	}
	if !bytes.Equal(req.body, body) {
		t.Fatalf("body mismatch: got %x want %x", req.body, body)
	}
	if ct := req.headers["content-type"]; len(ct) != 1 || ct[0] != "application/x-apple-binary-plist" {
		t.Fatalf("unexpected content-type: %v", ct)
	}
}

// TestSendUpdateInfo checks that the updateInfo push wraps the receiver info
// dictionary under the expected "type"/"value" keys.
func TestSendUpdateInfo(t *testing.T) {
	h := &mediaHandler{
		info: func() *plist.Value {
			return plist.Dict(map[string]*plist.Value{
				"deviceid": plist.String("AA:BB:CC:DD:EE:FF"),
				"model":    plist.String("AppleTV6,2"),
			})
		},
	}
	var b bytes.Buffer
	if err := h.sendUpdateInfo(&b); err != nil {
		t.Fatalf("sendUpdateInfo: %v", err)
	}
	req, err := readEventRequest(bufio.NewReader(&b), DefaultLimits())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	v, err := plist.Decode(req.body, plist.DefaultLimits())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v == nil || v.Kind != plist.KindDict {
		t.Fatalf("updateInfo body not a dict: %+v", v)
	}
	if typ, ok := v.Dict["type"]; !ok || typ.Kind != plist.KindString || typ.String != "updateInfo" {
		t.Fatalf("missing or invalid type: %+v", v.Dict["type"])
	}
	value, ok := v.Dict["value"]
	if !ok || value.Kind != plist.KindDict {
		t.Fatalf("missing or invalid value: %+v", v.Dict["value"])
	}
	if id, ok := value.Dict["deviceid"]; !ok || id.String != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("deviceid mismatch: %+v", value.Dict["deviceid"])
	}
}

// TestSendUpdateInfoNilInfo verifies the no-op path when no info provider is
// configured.
func TestSendUpdateInfoNilInfo(t *testing.T) {
	h := &mediaHandler{}
	var b bytes.Buffer
	if err := h.sendUpdateInfo(&b); err != nil {
		t.Fatalf("sendUpdateInfo: %v", err)
	}
	if b.Len() != 0 {
		t.Fatalf("expected no output, got %d bytes", b.Len())
	}
}
