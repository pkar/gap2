package airplay2

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkar/gap2/internal/plist"
	"github.com/pkar/gap2/internal/tlv8"
)

func newTestControlServer(t *testing.T) *controlServer {
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
	return newControlServer(cfg, id, store, nil)
}

func startPipeConn(t *testing.T, s *controlServer) net.Conn {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleConn(server)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		<-done
	})
	return client
}

// statusCode extracts the numeric status code from an HTTP/RTSP status line.
func statusCode(status string) string {
	parts := strings.Fields(status)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

type ctlResponse struct {
	status  string
	headers map[string][]string
	body    []byte
}

func exchange(t *testing.T, conn net.Conn, raw string) ctlResponse {
	t.Helper()
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	headers := map[string][]string{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		if line == "\r\n" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("malformed response header %q", line)
		}
		headers[strings.ToLower(strings.TrimSpace(k))] = append(headers[strings.ToLower(strings.TrimSpace(k))], strings.TrimSpace(v))
	}
	n := 0
	if vals := headers["content-length"]; len(vals) > 0 {
		n, err = strconv.Atoi(strings.TrimSpace(vals[0]))
		if err != nil {
			t.Fatalf("bad content length: %v", err)
		}
	}
	body := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(br, body); err != nil {
			t.Fatalf("read body: %v", err)
		}
	}
	return ctlResponse{status: strings.TrimSpace(status), headers: headers, body: body}
}

func TestControlInfo(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	resp := exchange(t, conn, "GET /info HTTP/1.1\r\nHost: x\r\n\r\n")

	if statusCode(resp.status) != "200" {
		t.Fatalf("status = %q, want 200", resp.status)
	}
	if ct := resp.headers["content-type"]; len(ct) != 1 || ct[0] != "application/x-apple-binary-plist" {
		t.Fatalf("content-type = %v", resp.headers["content-type"])
	}
	v, err := plist.Decode(resp.body, plist.DefaultLimits())
	if err != nil {
		t.Fatalf("decode info plist: %v", err)
	}
	if v.Kind != plist.KindDict {
		t.Fatalf("info plist kind = %v, want dict", v.Kind)
	}
	for _, key := range []string{"deviceid", "features", "model", "name", "pi", "pk", "srcvers", "vv"} {
		if _, ok := v.Dict[key]; !ok {
			t.Fatalf("info plist missing key %q", key)
		}
	}
}

func TestControlOptions(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	resp := exchange(t, conn, "OPTIONS * RTSP/1.0\r\nCSeq: 1\r\n\r\n")
	if statusCode(resp.status) != "200" {
		t.Fatalf("status = %q, want 200", resp.status)
	}
}

func TestControlPairSetupM1(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	m1, err := tlv8.Encode([]tlv8.Item{
		{Type: 0, Value: []byte{0}}, // method: Pair Setup (no MFi)
		{Type: 6, Value: []byte{1}}, // state: M1
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := "POST /pair-setup HTTP/1.1\r\nContent-Type: application/pairing+tlv8\r\nContent-Length: " + strconv.Itoa(len(m1)) + "\r\n\r\n" + string(m1)
	resp := exchange(t, conn, raw)

	if statusCode(resp.status) != "200" {
		t.Fatalf("status = %q, want 200 (body %x)", resp.status, resp.body)
	}
	items, err := tlv8.Decode(resp.body)
	if err != nil {
		t.Fatalf("decode pair-setup M2: %v", err)
	}
	var state, salt, pub bool
	for _, it := range items {
		switch it.Type {
		case 6:
			state = len(it.Value) == 1 && it.Value[0] == 2
		case 2:
			salt = len(it.Value) > 0
		case 3:
			pub = len(it.Value) > 0
		}
	}
	if !state || !salt || !pub {
		t.Fatalf("M2 missing expected fields: state=%v salt=%v pub=%v", state, salt, pub)
	}
}

func TestControlPairVerifyM1(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	// The X25519 base point (u = 9) is a valid Curve25519 public key.
	pub := make([]byte, 32)
	pub[0] = 9
	m1, err := tlv8.Encode([]tlv8.Item{
		{Type: 3, Value: pub},       // public key
		{Type: 6, Value: []byte{1}}, // state: M1
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := "POST /pair-verify HTTP/1.1\r\nContent-Type: application/pairing+tlv8\r\nContent-Length: " + strconv.Itoa(len(m1)) + "\r\n\r\n" + string(m1)
	resp := exchange(t, conn, raw)

	if statusCode(resp.status) != "200" {
		t.Fatalf("status = %q, want 200 (body %x)", resp.status, resp.body)
	}
	items, err := tlv8.Decode(resp.body)
	if err != nil {
		t.Fatalf("decode pair-verify M2: %v", err)
	}
	var state, pubOK, encOK bool
	for _, it := range items {
		switch it.Type {
		case 6:
			state = len(it.Value) == 1 && it.Value[0] == 2
		case 3:
			pubOK = len(it.Value) == 32
		case 5:
			encOK = len(it.Value) > 0
		}
	}
	if !state || !pubOK || !encOK {
		t.Fatalf("pair-verify M2 missing fields: state=%v pub=%v enc=%v", state, pubOK, encOK)
	}
}

func TestControlInvalidPairing(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	resp := exchange(t, conn, "POST /pair-setup HTTP/1.1\r\nContent-Length: 4\r\n\r\n0000")
	if statusCode(resp.status) != "400" {
		t.Fatalf("status = %q, want 400", resp.status)
	}
}

func TestControlUnknownRoute(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	resp := exchange(t, conn, "POST /not-a-route HTTP/1.1\r\nContent-Length: 0\r\n\r\n")
	if statusCode(resp.status) != "501" {
		t.Fatalf("status = %q, want 501", resp.status)
	}
}
