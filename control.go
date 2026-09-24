package airplay2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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
	sem := make(chan struct{}, s.cfg.Limits.MaxConnections)
	var wg sync.WaitGroup
	defer wg.Wait()

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
			s.handleConn(conn)
		}()
	}
}

func (s *controlServer) handleConn(conn net.Conn) {
	defer conn.Close()
	cs := &connState{
		conn:   conn,
		br:     bufio.NewReader(conn),
		w:      conn,
		log:    s.log,
		limits: s.cfg.Limits,
	}
	defer func() {
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

		if err := s.handleRequest(cs, req); err != nil {
			s.log.Debug("control request failed", "remote", conn.RemoteAddr(), "err", err)
			return
		}
	}
}

func (s *controlServer) handleRequest(cs *connState, req *ctlRequest) error {
	switch req.method {
	case "OPTIONS":
		return cs.writeResponse(200, "OK", "text/plain", nil)
	case "GET":
		if req.target == "/info" {
			return s.handleInfo(cs)
		}
	case "POST":
		switch req.target {
		case "/pair-setup":
			return s.handlePairSetup(cs, req)
		case "/pair-verify":
			return s.handlePairVerify(cs, req)
		}
	case "ANNOUNCE", "SETUP", "RECORD", "TEARDOWN", "FLUSH", "FLUSHBUFFERED",
		"GET_PARAMETER", "SET_PARAMETER", "SETRATEANCHORI", "SETRATEANCHORTI", "SETPEERS", "SETPEERSX":
		if cs.encrypted == nil {
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

// infoValue builds the receiver info dictionary shared by the discovery
// /info endpoint and the AP2 event-channel updateInfo push.
func (s *controlServer) infoValue() *plist.Value {
	mac := s.store.deviceID()
	return plist.Dict(map[string]*plist.Value{
		"deviceid": plist.String(mac),
		"features": plist.Int(0x5A7FFFF7),
		"flags":    plist.Int(0x4),
		"model":    plist.String("AppleTV6,2"),
		"name":     plist.String(s.cfg.Name),
		"pi":       plist.String(string(s.identity.ID)),
		"pk":       plist.String(base64.StdEncoding.EncodeToString(s.identity.PublicKey())),
		"srcvers":  plist.String("366.0"),
		"vv":       plist.Int(2),
	})
}

func (s *controlServer) handlePairSetup(cs *connState, req *ctlRequest) error {
	if cs.encrypted != nil {
		return cs.writeResponse(400, "Bad Request", "text/plain", nil)
	}
	if cs.setup == nil {
		cs.setup = hap.NewPairSetupSession(nil, s.identity, s.pin, s.store)
	}
	res, err := cs.setup.Handle(req.body)
	if err != nil {
		s.log.Debug("pair setup rejected", "err", err)
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

func (s *controlServer) handlePairVerify(cs *connState, req *ctlRequest) error {
	if cs.encrypted != nil {
		return cs.writeResponse(400, "Bad Request", "text/plain", nil)
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

	limits Limits

	setup     *hap.PairSetupSession
	verify    *hap.PairVerifySession
	encrypted *hap.Conn
	media     *mediaHandler

	// sessionKey is the shared secret established by pair setup/verify. It
	// feeds both the control channel (via hap.Conn) and the AP2 event channel
	// (via hap.NewEventConn).
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
	return cs.writeResponse(200, "OK", "application/pairing+tlv8", body)
}

func (cs *connState) writeResponse(status int, reason, contentType string, body []byte) error {
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

// writeRTSPResponse writes an RTSP/1.0 response over the encrypted control
// channel, echoing the request's CSeq.
func (cs *connState) writeRTSPResponse(cseq string, status int, reason string, headers map[string]string, body []byte) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "RTSP/1.0 %d %s\r\n", status, reason)
	fmt.Fprintf(&b, "CSeq: %s\r\n", cseq)
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
