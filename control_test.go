package airplay2

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
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
	for _, key := range []string{"deviceID", "features", "statusFlags", "model", "name", "pi", "psi", "pk", "protocolVersion", "sourceVersion", "vv"} {
		if _, ok := v.Dict[key]; !ok {
			t.Fatalf("info plist missing key %q", key)
		}
	}
	if pk := v.Dict["pk"]; pk.Kind != plist.KindData || !slices.Equal(pk.Data, s.identity.PublicKey()) {
		t.Fatalf("/info pk must be the advertised Ed25519 public key as plist data")
	}
	if v.Dict["pi"].String != string(s.identity.ID) || v.Dict["psi"].String != string(s.identity.ID) || v.Dict["vv"].Int != 1 {
		t.Fatal("/info identity or version disagrees with discovery")
	}
	features := v.Dict["features"]
	if features.Kind != plist.KindInt || features.Int != int64(airplayFeatures) {
		t.Fatalf("/info features = %v, want 0x%X", features, airplayFeatures)
	}
}

func TestAirPlayFeaturesMatchImplementedAudio(t *testing.T) {
	const unsupported = (1 << 0) | (1 << 2) | (1 << 7) | // video/mirroring
		(1 << 12) | (1 << 14) | // FairPlay SAP
		(1 << 40) | // buffered audio: only realtime is implemented
		(1 << 35) // TLS-PSK
	if airplayFeatures&unsupported != 0 {
		t.Fatalf("unsupported capabilities advertised: 0x%X", airplayFeatures&unsupported)
	}
	const required = (1 << 9) | (1 << 19) | (1 << 20) | (1 << 27) | (1 << 41) | (1 << 46)
	if airplayFeatures&required != required {
		t.Fatalf("missing audio, pairing or PTP capabilities: 0x%X", required&^airplayFeatures)
	}
	s := newTestControlServer(t)
	want := fmt.Sprintf("features=0x%X,0x%X", uint32(airplayFeatures&0xffffffff), airplayFeatures>>32)
	txt := airplayTXT(s.store.deviceID(), s.identity)
	if !slices.Contains(txt, want) {
		t.Fatalf("DNS-SD features do not match /info: want %s", want)
	}
	if !slices.Contains(txt, "pi="+string(s.identity.ID)) || !slices.Contains(txt, "pk="+hex.EncodeToString(s.identity.PublicKey())) {
		t.Fatal("DNS-SD pairing ID or public key does not match HAP identity")
	}
	raop := raopTXT(s.identity)
	for _, field := range []string{
		fmt.Sprintf("ft=0x%X,0x%X", uint32(airplayFeatures&0xffffffff), airplayFeatures>>32),
		"sf=0x4", "et=0", "pk=" + hex.EncodeToString(s.identity.PublicKey()),
	} {
		if !slices.Contains(raop, field) {
			t.Fatalf("RAOP discovery missing %s", field)
		}
	}
	if len(s.identity.ID) != 36 || s.identity.ID[8] != '-' || s.identity.ID[13] != '-' || s.identity.ID[18] != '-' || s.identity.ID[23] != '-' {
		t.Fatal("DNS-SD pairing ID is not a UUID")
	}
}

func TestControlPairSetupBadProofReturnsM4Error(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	request := func(items []tlv8.Item) ctlResponse {
		t.Helper()
		body, err := tlv8.Encode(items)
		if err != nil {
			t.Fatal(err)
		}
		return exchange(t, conn, "POST /pair-setup HTTP/1.1\r\nContent-Type: application/pairing+tlv8\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"+string(body))
	}
	m2 := request([]tlv8.Item{{Type: 0, Value: []byte{0}}, {Type: 6, Value: []byte{1}}, {Type: 19, Value: []byte{16}}})
	if statusCode(m2.status) != "200" {
		t.Fatalf("M2 status = %q", m2.status)
	}
	m4 := request([]tlv8.Item{
		{Type: 6, Value: []byte{3}},
		{Type: 3, Value: []byte{5}},          // a valid, nonzero SRP public value
		{Type: 4, Value: []byte{0xde, 0xad}}, // deliberately wrong proof
	})
	if statusCode(m4.status) != "200" {
		t.Fatalf("M4 status = %q, want TLV error with HTTP 200", m4.status)
	}
	items, err := tlv8.Decode(m4.body)
	if err != nil {
		t.Fatal(err)
	}
	var state, authError bool
	for _, item := range items {
		switch item.Type {
		case 6:
			state = slices.Equal(item.Value, []byte{4})
		case 7:
			authError = slices.Equal(item.Value, []byte{2})
		}
	}
	if !state || !authError {
		t.Fatalf("M4 missing state or authentication error")
	}
	// A failed proof must not authorize media requests on this connection.
	resp := exchange(t, conn, "SETUP /stream RTSP/1.0\r\nCSeq: 1\r\n\r\n")
	if statusCode(resp.status) != "401" {
		t.Fatalf("unauthenticated SETUP status = %q", resp.status)
	}
}

func TestControlOptions(t *testing.T) {
	s := newTestControlServer(t)
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	conn := startPipeConn(t, s)
	resp := exchange(t, conn, "OPTIONS * RTSP/1.0\r\nCSeq: 1\r\nApple-Challenge: private-test-value\r\n\r\n")
	if got := logs.String(); !strings.Contains(got, "rtsp=true cseq=true appleChallenge=true") || strings.Contains(got, "private-test-value") {
		t.Fatalf("OPTIONS diagnostic must report only safe metadata: %q", got)
	}
	if resp.status != "RTSP/1.0 200 OK" {
		t.Fatalf("status = %q, want RTSP/1.0 200 OK", resp.status)
	}
	if got := resp.headers["cseq"]; len(got) != 1 || got[0] != "1" {
		t.Fatalf("CSeq = %v, want 1", got)
	}
	if got := resp.headers["public"]; len(got) != 1 || !strings.Contains(got[0], "SETUP") || !strings.Contains(got[0], "POST") {
		t.Fatalf("Public = %v, want supported RAOP methods", got)
	}
	if got := resp.headers["server"]; len(got) != 1 || got[0] != "AirTunes/366.0" {
		t.Fatalf("Server = %v, want AirTunes/366.0", got)
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

func TestControlLegacyTransientSetup(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	body := strings.Repeat("x", 32)
	resp := exchange(t, conn, "POST /pair-setup RTSP/1.0\r\nContent-Length: 32\r\n\r\n"+body)
	if statusCode(resp.status) != "200" || string(resp.body) != string(s.identity.PublicKey()) {
		t.Fatalf("legacy setup: status=%q body=%x", resp.status, resp.body)
	}
	if ct := resp.headers["content-type"]; len(ct) != 1 || ct[0] != "application/octet-stream" {
		t.Fatalf("legacy content type: %v", ct)
	}
}

func TestControlLegacyVerifyOnFreshConnection(t *testing.T) {
	s := newTestControlServer(t)
	conn := startPipeConn(t, s)
	// A client that already knows the receiver's identity can verify on a
	// connection that never sent /pair-setup.
	clientX, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientEd := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	m1 := append([]byte{1, 0, 0, 0}, clientX.PublicKey().Bytes()...)
	m1 = append(m1, clientEd.Public().(ed25519.PublicKey)...)
	resp := exchange(t, conn, "POST /pair-verify RTSP/1.0\r\nContent-Type: application/octet-stream\r\nContent-Length: 68\r\n\r\n"+string(m1))
	if statusCode(resp.status) != "200" || len(resp.body) != 96 {
		t.Fatalf("legacy verify M1: status=%q bodyLen=%d", resp.status, len(resp.body))
	}
	if ct := resp.headers["content-type"]; len(ct) != 1 || ct[0] != "application/octet-stream" {
		t.Fatalf("legacy verify content type: %v", ct)
	}
	serverX, err := ecdh.X25519().NewPublicKey(resp.body[:32])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := clientX.ECDH(serverX)
	if err != nil {
		t.Fatal(err)
	}
	key := sha512.Sum512(append([]byte("Pair-Verify-AES-Key"), shared...))
	iv := sha512.Sum512(append([]byte("Pair-Verify-AES-IV"), shared...))
	block, err := aes.NewCipher(key[:16])
	if err != nil {
		t.Fatal(err)
	}
	stream := cipher.NewCTR(block, iv[:16])
	var skip [64]byte // M2 signature consumed the first 64 bytes of the CTR stream
	stream.XORKeyStream(skip[:], skip[:])
	message := append(clientX.PublicKey().Bytes(), resp.body[:32]...)
	sig := ed25519.Sign(clientEd, message)
	stream.XORKeyStream(sig, sig)
	m3 := append([]byte{0, 0, 0, 0}, sig...)
	resp = exchange(t, conn, "POST /pair-verify RTSP/1.0\r\nContent-Type: application/octet-stream\r\nContent-Length: 68\r\n\r\n"+string(m3))
	if statusCode(resp.status) != "200" || len(resp.body) != 0 {
		t.Fatalf("legacy verify M3: status=%q bodyLen=%d", resp.status, len(resp.body))
	}
	// Binary legacy verify authenticates but does not encrypt RTSP: the next
	// request must still parse and receive a plaintext response.
	resp = exchange(t, conn, "OPTIONS * RTSP/1.0\r\nContent-Length: 0\r\n\r\n")
	if statusCode(resp.status) != "200" {
		t.Fatalf("plaintext OPTIONS after verify: %q", resp.status)
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
