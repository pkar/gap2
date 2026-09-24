package airplay2

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkar/gap2/internal/hap"
	"github.com/pkar/gap2/internal/plist"
	"github.com/pkar/gap2/internal/ptp"
)

// controlServer serves the AirPlay control endpoint: discovery info, pairing,
// and (after pair verification) the encrypted RTSP control channel.
type controlServer struct {
	cfg      Config
	log      *slog.Logger
	identity hap.Identity
	store    *pairingStore
	pin      string
	clock    *ptp.Clock
}

func newControlServer(cfg Config, id hap.Identity, store *pairingStore, clock *ptp.Clock) *controlServer {
	if clock == nil {
		clock = ptp.NewClock()
	}
	return &controlServer{cfg: cfg, log: cfg.Logger, identity: id, store: store, pin: cfg.PIN, clock: clock}
}

// serve accepts connections until ln is closed or ctx is cancelled, then waits
// for in-flight handlers.
func (s *controlServer) serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	sem := make(chan struct{}, s.cfg.Limits.MaxConnections)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel() // close active connections before waiting for handlers
	stopListener := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stopListener()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
			return nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stopConn()
			s.handleConn(conn)
		}()
	}
}

func (s *controlServer) handleConn(conn net.Conn) {
	defer conn.Close()
	s.log.Debug("control connected", "remote", conn.RemoteAddr())
	cs := &connState{
		conn:   conn,
		br:     bufio.NewReader(conn),
		w:      conn,
		log:    s.log,
		limits: s.cfg.Limits,
	}
	defer func() {
		s.log.Debug("control disconnected", "remote", conn.RemoteAddr(), "encrypted", cs.encrypted != nil)
		if cs.media != nil {
			cs.media.close()
		}
	}()

	for {
		if cs.limits.ReadHeaderTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(cs.limits.ReadHeaderTimeout))
		}
		req, err := cs.readRequest()
		if err != nil {
			if err != io.EOF && !errors.Is(err, net.ErrClosed) {
				s.log.Debug("control read failed", "remote", conn.RemoteAddr(), "err", err)
			}
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		cs.currentRequest = req
		// Method and path are enough to diagnose pairing and media flow;
		// never log pairing bodies or URL query parameters here.
		path, _, _ := strings.Cut(req.target, "?")
		if len(path) > 128 {
			path = path[:128]
		}
		s.log.Debug("control request", "remote", conn.RemoteAddr(), "method", req.method, "path", path,
			"protocol", req.version, "cseq", reqHeader(req, "CSeq") != "", "bodyLength", len(req.body))

		if err := s.handleRequest(cs, req); err != nil {
			s.log.Debug("control request failed", "remote", conn.RemoteAddr(), "err", err)
			return
		}
	}
}

func (s *controlServer) handleRequest(cs *connState, req *ctlRequest) error {
	switch req.method {
	case "OPTIONS":
		// An OPTIONS response rejected by a real sender is hard to diagnose
		// without knowing which protocol variant it requested. Log only
		// presence/format metadata; header values (notably Apple-Challenge)
		// must never be retained in receiver logs.
		s.log.Debug("control options", "rtsp", strings.HasPrefix(req.version, "RTSP/"),
			"cseq", reqHeader(req, "CSeq") != "", "appleChallenge", reqHeader(req, "Apple-Challenge") != "")
		if strings.HasPrefix(req.version, "RTSP/") {
			// RAOP senders use Public to select a supported control path.
			// A bare 200 can be rejected before pairing even begins.
			return cs.writeRTSPResponse(reqHeader(req, "CSeq"), 200, "OK", map[string]string{
				"Public": "ANNOUNCE, SETUP, RECORD, TEARDOWN, FLUSH, OPTIONS, GET_PARAMETER, SET_PARAMETER, POST, GET",
				"Server": "AirTunes/366.0",
			}, nil)
		}
		return cs.writeResponse(200, "OK", "text/plain", nil)
	case "GET":
		if req.target == "/info" {
			return s.handleInfo(cs)
		}
	case "POST":
		switch req.target {
		case "/command":
			typ, command, err := decodeEventCommand(req.body)
			if err != nil {
				return cs.writeError(400, "Bad Request")
			}
			switch typ {
			case commandUpdateMRSupportedCommands, commandUpdateMRNowPlayingInfo, commandUpdateMRPlaybackState:
				// Senders also send capability notifications on a separate
				// plaintext connection. Acknowledge them without changing an
				// authenticated media session's metadata.
				if cs.media != nil && cs.encrypted != nil {
					cs.media.handleEventCommand(typ, command)
				}
				return cs.writeResponse(200, "OK", "application/x-apple-binary-plist", nil)
			default:
				return cs.writeError(501, "Not Implemented")
			}
		case "/feedback":
			if cs.encrypted == nil {
				return cs.writeError(401, "Unauthorized")
			}
			if h := cs.media; h != nil && h.sess != nil && h.mediaStarted {
				body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
					"streams": plist.Array(plist.Dict(map[string]*plist.Value{
						"type": plist.Int(h.streamType), "sr": plist.Real(float64(h.sess.Stream().Format().Rate)),
					})),
				}))
				if err != nil {
					return err
				}
				return cs.writeResponse(200, "OK", "application/x-apple-binary-plist", body)
			}
			return cs.writeResponse(200, "OK", "text/plain", nil)
		case "/fp-setup":
			return s.handleFPSetup(cs, req)
		case "/pair-setup":
			return s.handlePairSetup(cs, req)
		case "/pair-verify":
			return s.handlePairVerify(cs, req)
		}
	case "ANNOUNCE", "SETUP", "RECORD", "TEARDOWN", "FLUSH", "FLUSHBUFFERED",
		"GET_PARAMETER", "SET_PARAMETER", "SETRATEANCHORI", "SETRATEANCHORTI", "SETRATEANCHORTIME", "SETPEERS", "SETPEERSX", "LOUDNESSNORMALIZATION":
		if len(cs.sessionKey) == 0 {
			return cs.writeResponse(401, "Unauthorized", "text/plain", nil)
		}
		return s.handleMedia(cs, req)
	}
	return cs.writeResponse(501, "Not Implemented", "text/plain", nil)
}

func (s *controlServer) handleInfo(cs *connState) error {
	body, err := s.infoPlist()
	if err != nil {
		return cs.writeResponse(500, "Internal Server Error", "text/plain", nil)
	}
	return cs.writeResponse(200, "OK", "application/x-apple-binary-plist", body)
}

func (s *controlServer) infoPlist() ([]byte, error) {
	return plist.Encode(s.infoValue())
}

// PTP, audio authentication and the AP2 routing bit are required for Apple TV
// to negotiate a native stream. Bit 40 selects this path even for realtime
// type 96; type 103 uses the buffered TCP transport.
// FPSAP v2.5 (bit 12), video, cloud and TLS capabilities remain disabled.
const airplayFeatures uint64 = (1 << 9) | // AirPlay audio
	(1 << 14) | // FPLY v3 setup followed by native AP2 stream encryption
	(1 << 15) | (1 << 16) | (1 << 17) | // artwork, progress, DAAP metadata
	(1 << 18) | (1 << 19) | (1 << 20) | // PCM, ALAC, AAC-LC
	(1 << 22) | // unencrypted audio
	(1 << 27) | // legacy pairing
	(1 << 30) | // unified advertising /info
	(1 << 38) | // CoreUtils pairing and encrypted control
	(1 << 40) | // AirPlay 2 audio routing
	(1 << 41) | // PTP timing
	(1 << 46) | (1 << 47) | (1 << 48) // HomeKit, PTP peer management, transient pairing

// infoValue builds the receiver info dictionary shared by the discovery
// /info endpoint and the AP2 event-channel updateInfo push.
func (s *controlServer) infoValue() *plist.Value {
	mac := s.store.deviceID()
	// Senders request the DNS-SD TXT data through /info as well as mDNS.
	// Each string uses the same one-byte length prefix as a DNS TXT record.
	var txt []byte
	for _, entry := range airplayTXT(mac, s.identity) {
		txt = append(txt, byte(len(entry)))
		txt = append(txt, entry...)
	}
	return plist.Dict(map[string]*plist.Value{
		"txtAirPlay":      plist.Data(txt),
		"deviceID":        plist.String(mac),
		"features":        plist.Int(int64(airplayFeatures)),
		"statusFlags":     plist.Int(0x4),
		"model":           plist.String("gap2"),
		"name":            plist.String(s.cfg.Name),
		"pi":              plist.String(string(s.identity.ID)),
		"psi":             plist.String(string(s.identity.ID)),
		"pk":              plist.Data(s.identity.PublicKey()),
		"protocolVersion": plist.String("1.1"),
		"sourceVersion":   plist.String("366.0"),
		"vv":              plist.Int(2),
	})
}

func (s *controlServer) handlePairSetup(cs *connState, req *ctlRequest) error {
	if cs.encrypted != nil {
		return cs.writeResponse(400, "Bad Request", "text/plain", nil)
	}
	// Legacy transient setup sends exactly 32 raw bytes and receives the
	// accessory's long-term Ed25519 public key, without a TLV envelope.
	if len(req.body) == 32 && cs.setup == nil && !strings.Contains(strings.ToLower(reqHeader(req, "Content-Type")), "pairing+tlv8") {
		cs.legacySetup = true
		return cs.writeResponse(200, "OK", "application/octet-stream", s.identity.PublicKey())
	}
	if cs.legacySetup {
		return cs.writeResponse(400, "Bad Request", "text/plain", nil)
	}
	if cs.setup == nil {
		cs.setup = hap.NewPairSetupSession(nil, s.identity, s.pin, s.store)
	}
	res, err := cs.setup.Handle(req.body)
	if err != nil {
		// Malformed pairing bodies may still contain secrets. Log only bounded
		// metadata, never the request payload. A bad SRP proof carries an M4
		// authentication error: return that TLV rather than an empty HTTP 400.
		s.log.Debug("pair setup rejected", "err", err, "bodyLength", len(req.body))
		if len(res.Response) == 0 {
			return cs.writeResponse(400, "Bad Request", "text/plain", nil)
		}
		return cs.writeTLV(res.Response)
	}
	if err := cs.writeTLV(res.Response); err != nil {
		return err
	}
	if res.Done && len(res.SessionKey) > 0 {
		s.log.Debug("pair setup authenticated")
		return cs.upgrade(res.SessionKey)
	}
	return nil
}

func (s *controlServer) handlePairVerify(cs *connState, req *ctlRequest) error {
	if cs.encrypted != nil {
		return cs.writeResponse(400, "Bad Request", "text/plain", nil)
	}
	// Senders may open a fresh connection for verify after transient setup, or
	// skip setup entirely when they already know our public key. M1's binary
	// header distinguishes it from the TLV8 HomeKit pairing exchange.
	legacyM1 := len(req.body) == 68 && bytes.Equal(req.body[:4], []byte{1, 0, 0, 0}) &&
		!strings.Contains(strings.ToLower(reqHeader(req, "Content-Type")), "pairing+tlv8")
	if cs.legacySetup || cs.legacyVerify != nil || legacyM1 {
		if cs.verify != nil || cs.setup != nil {
			return cs.writeResponse(400, "Bad Request", "text/plain", nil)
		}
		if cs.legacyVerify == nil {
			cs.legacyVerify = hap.NewLegacyVerify(s.identity, nil)
		}
		reply, secret, err := cs.legacyVerify.Handle(req.body)
		if err != nil {
			s.log.Debug("legacy pair verify rejected", "err", err)
			return cs.writeResponse(400, "Bad Request", "text/plain", nil)
		}
		cs.legacySetup = true
		if err := cs.writeResponse(200, "OK", "application/octet-stream", reply); err != nil {
			return err
		}
		if len(secret) != 0 {
			s.log.Debug("legacy pair verify authenticated")
			// A live legacy-pairing sender continues with plaintext RTSP after
			// M3. Keep the verified shared secret for media, but do not enable
			// HAP-style control framing on this connection.
			cs.sessionKey = append(cs.sessionKey[:0], secret...)
			return nil
		}
		s.log.Debug("legacy pair verify M1 accepted")
		return nil
	}
	if cs.verify == nil {
		cs.verify = hap.NewPairVerifySession(nil, s.identity, s.store)
	}
	res, err := cs.verify.Handle(req.body)
	if err != nil {
		s.log.Debug("pair verify rejected", "err", err)
		return cs.writeResponse(400, "Bad Request", "text/plain", nil)
	}
	if err := cs.writeTLV(res.Response); err != nil {
		return err
	}
	if res.Done && len(res.SessionKey) > 0 {
		return cs.upgrade(res.SessionKey)
	}
	return nil
}

// connState is the per-connection protocol state.
type connState struct {
	conn net.Conn
	br   *bufio.Reader
	w    io.Writer
	log  *slog.Logger

	limits         Limits
	currentRequest *ctlRequest

	setup        *hap.PairSetupSession
	verify       *hap.PairVerifySession
	legacySetup  bool
	legacyVerify *hap.LegacyVerify
	fpStarted    bool
	encrypted    *hap.Conn
	media        *mediaHandler

	// sessionKey is the verified pairing secret. HAP uses it for encrypted
	// control and events; legacy binary pairing leaves RTSP plaintext, while
	// retaining the secret for media-channel key derivation.
	sessionKey []byte
}

func (cs *connState) upgrade(sessionKey []byte) error {
	if cs.br.Buffered() != 0 {
		return fmt.Errorf("airplay2: buffered plaintext before encryption upgrade")
	}
	ec := hap.NewConn(cs.conn, sessionKey)
	cs.encrypted = ec
	cs.br = bufio.NewReader(ec)
	cs.w = ec
	cs.sessionKey = append(cs.sessionKey[:0], sessionKey...)
	return nil
}

func (cs *connState) writeTLV(body []byte) error {
	// AirPlay carries HAP TLV8 pairing messages in an octet-stream envelope.
	return cs.writeResponse(200, "OK", "application/octet-stream", body)
}

func (cs *connState) writeResponse(status int, reason, contentType string, body []byte) error {
	if cs.currentRequest != nil && strings.HasPrefix(cs.currentRequest.version, "RTSP/") {
		headers := make(map[string]string)
		if contentType != "" {
			headers["Content-Type"] = contentType
		}
		return cs.writeRTSPResponse(reqHeader(cs.currentRequest, "CSeq"), status, reason, headers, body)
	}
	if cs.legacySetup && len(cs.sessionKey) > 0 && cs.log != nil {
		cs.log.Debug("legacy control response", "status", status)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", status, reason)
	fmt.Fprintf(&b, "Content-Length: %d\r\n", len(body))
	if contentType != "" {
		fmt.Fprintf(&b, "Content-Type: %s\r\n", contentType)
	}
	b.WriteString("\r\n")
	b.Write(body)
	return writeAll(cs.w, b.Bytes())
}

// writeRTSPResponse writes an RTSP/1.0 response, echoing the request's CSeq.
func (cs *connState) writeRTSPResponse(cseq string, status int, reason string, headers map[string]string, body []byte) error {
	if cs.legacySetup && len(cs.sessionKey) > 0 && cs.log != nil {
		cs.log.Debug("legacy control response", "status", status)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "RTSP/1.0 %d %s\r\n", status, reason)
	if _, ok := headers["Server"]; !ok {
		b.WriteString("Server: AirTunes/366.0\r\n")
	}
	if cseq != "" {
		fmt.Fprintf(&b, "CSeq: %s\r\n", cseq)
	}
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n", len(body))
	b.WriteString("\r\n")
	b.Write(body)
	return writeAll(cs.w, b.Bytes())
}

func (cs *connState) writeError(status int, reason string) error {
	return cs.writeResponse(status, reason, "text/plain", nil)
}

// ctlRequest is a parsed HTTP/RTSP-like request.
type ctlRequest struct {
	method  string
	target  string
	version string
	headers map[string][]string
	body    []byte
}

func (cs *connState) readRequest() (*ctlRequest, error) {
	start, err := readLineLimited(cs.br, cs.limits.MaxRequestBytes)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(string(start), " ", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("airplay2: malformed request line")
	}

	headers, err := readHeaderBlock(cs.br, cs.limits.MaxHeaders, cs.limits.MaxHeaderBytes)
	if err != nil {
		return nil, err
	}

	n := contentLength(headers)
	if n < 0 {
		return nil, fmt.Errorf("airplay2: invalid content length")
	}
	if n > int64(cs.limits.MaxBodyBytes) {
		return nil, fmt.Errorf("airplay2: request body too large: %d", n)
	}
	body := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(cs.br, body); err != nil {
			return nil, err
		}
	}
	return &ctlRequest{
		method:  parts[0],
		target:  parts[1],
		version: parts[2],
		headers: headers,
		body:    body,
	}, nil
}

func readLineLimited(br *bufio.Reader, max int) ([]byte, error) {
	if max <= 0 {
		max = 16 << 10
	}
	var line []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if b == '\n' {
			return bytes.TrimSuffix(line, []byte{'\r'}), nil
		}
		if len(line) >= max {
			return nil, fmt.Errorf("airplay2: request line too long")
		}
		line = append(line, b)
	}
}

func readHeaderBlock(br *bufio.Reader, maxHeaders, maxBytes int) (map[string][]string, error) {
	headers := make(map[string][]string)
	total := 0
	for i := 0; ; i++ {
		line, err := readLineLimited(br, maxBytes+2)
		if err != nil {
			return nil, err
		}
		if len(line) == 0 {
			return headers, nil
		}
		if i >= maxHeaders {
			return nil, fmt.Errorf("airplay2: too many headers")
		}
		total += len(line)
		if total > maxBytes {
			return nil, fmt.Errorf("airplay2: headers too large")
		}
		j := bytes.IndexByte(line, ':')
		if j <= 0 {
			return nil, fmt.Errorf("airplay2: malformed header")
		}
		name := strings.ToLower(strings.TrimSpace(string(line[:j])))
		value := strings.TrimSpace(string(line[j+1:]))
		headers[name] = append(headers[name], value)
	}
}

func contentLength(h map[string][]string) int64 {
	vals := h["content-length"]
	if len(vals) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(vals[0]), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
