// Package rtsp implements a bounded, purpose-specific parser for the
// RTSP-like control protocol used by AirPlay receivers. It is intentionally
// small: generic HTTP servers and generic RTSP libraries do not cover the
// dialect's exact framing and limits.
package rtsp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var (
	// ErrLineTooLong is returned when a start line or header line exceeds the limit.
	ErrLineTooLong = errors.New("rtsp: line too long")
	// ErrHeadersTooLarge is returned when header count or total size exceeds limits.
	ErrHeadersTooLarge = errors.New("rtsp: headers too large")
	// ErrBodyTooLarge is returned when a body exceeds the configured limit.
	ErrBodyTooLarge = errors.New("rtsp: body too large")
)

// Limits bounds parser memory use.
type Limits struct {
	MaxLineBytes   int
	MaxHeaders     int
	MaxHeaderBytes int
	MaxBodyBytes   int64
}

// DefaultLimits returns conservative parser limits.
func DefaultLimits() Limits {
	return Limits{
		MaxLineBytes:   16 << 10,
		MaxHeaders:     128,
		MaxHeaderBytes: 64 << 10,
		MaxBodyBytes:   4 << 20,
	}
}

// Method is an RTSP-like method.
type Method string

// Methods observed in AirPlay control exchanges.
const (
	MethodOptions           Method = "OPTIONS"
	MethodGet               Method = "GET"
	MethodPost              Method = "POST"
	MethodAnnounce          Method = "ANNOUNCE"
	MethodSetup             Method = "SETUP"
	MethodRecord            Method = "RECORD"
	MethodTeardown          Method = "TEARDOWN"
	MethodFlush             Method = "FLUSH"
	MethodFlushBuffered     Method = "FLUSHBUFFERED"
	MethodSetParameter      Method = "SET_PARAMETER"
	MethodGetParameter      Method = "GET_PARAMETER"
	MethodSetRateAnchorTime Method = "SETRATEANCHORTIME"
	MethodSetPeers          Method = "SETPEERS"
	MethodSetPeersX         Method = "SETPEERSX"
)

// Valid reports whether m is a recognized method.
func (m Method) Valid() bool {
	switch m {
	case MethodOptions, MethodGet, MethodPost, MethodAnnounce, MethodSetup,
		MethodRecord, MethodTeardown, MethodFlush, MethodFlushBuffered,
		MethodSetParameter, MethodGetParameter, MethodSetRateAnchorTime,
		MethodSetPeers, MethodSetPeersX:
		return true
	default:
		return false
	}
}

// Message is one parsed request or response.
type Message struct {
	IsRequest bool
	Method    Method
	Target    string
	Version   string

	StatusCode int
	Reason     string

	Headers map[string][]string
	Body    []byte
}

// Header returns the first value for the named header, case-insensitively.
func (m *Message) Header(name string) string {
	vals := m.Headers[strings.ToLower(name)]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// HeaderValues returns all values for the named header.
func (m *Message) HeaderValues(name string) []string {
	return m.Headers[strings.ToLower(name)]
}

// CSeq returns the parsed CSeq header value.
func (m *Message) CSeq() (int, error) {
	v := m.Header("CSeq")
	if v == "" {
		return 0, fmt.Errorf("rtsp: missing CSeq")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("rtsp: invalid CSeq %q: %w", v, err)
	}
	return n, nil
}

// ContentLength returns the parsed Content-Length header value, or 0 when
// absent. It does not validate overflow; callers should reject negative
// values returned by readContentLength when stricter checking is needed.
func (m *Message) ContentLength() int64 {
	v := m.Header("Content-Length")
	if v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// Reader reads one Message at a time from a byte stream.
type Reader struct {
	br     *bufio.Reader
	limits Limits
}

// NewReader returns a Reader over r. Zero limit fields are replaced by
// DefaultLimits.
func NewReader(r io.Reader, limits Limits) *Reader {
	d := DefaultLimits()
	if limits.MaxLineBytes <= 0 {
		limits.MaxLineBytes = d.MaxLineBytes
	}
	if limits.MaxHeaders <= 0 {
		limits.MaxHeaders = d.MaxHeaders
	}
	if limits.MaxHeaderBytes <= 0 {
		limits.MaxHeaderBytes = d.MaxHeaderBytes
	}
	if limits.MaxBodyBytes <= 0 {
		limits.MaxBodyBytes = d.MaxBodyBytes
	}
	return &Reader{br: bufio.NewReader(r), limits: limits}
}

// Read parses the next message.
func (r *Reader) Read() (*Message, error) {
	line, err := r.readLine()
	if err != nil {
		return nil, err
	}
	msg, err := parseStartLine(line)
	if err != nil {
		return nil, err
	}

	headers, err := r.readHeaders()
	if err != nil {
		return nil, err
	}
	msg.Headers = headers

	n := msg.ContentLength()
	if n < 0 {
		return nil, fmt.Errorf("rtsp: negative content length %d", n)
	}
	if n > r.limits.MaxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	if n > 0 {
		body := make([]byte, n)
		if _, err := io.ReadFull(r.br, body); err != nil {
			return nil, fmt.Errorf("rtsp: reading body: %w", err)
		}
		msg.Body = body
	}
	return msg, nil
}

func (r *Reader) readLine() ([]byte, error) {
	var line []byte
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if b == '\n' {
			return bytes.TrimSuffix(line, []byte{'\r'}), nil
		}
		if len(line) >= r.limits.MaxLineBytes {
			return nil, ErrLineTooLong
		}
		line = append(line, b)
	}
}

func (r *Reader) readHeaders() (map[string][]string, error) {
	headers := make(map[string][]string)
	total := 0
	lines := 0
	for {
		line, err := r.readLine()
		if err != nil {
			return nil, err
		}
		if len(line) == 0 {
			return headers, nil
		}
		lines++
		if lines > r.limits.MaxHeaders {
			return nil, ErrHeadersTooLarge
		}
		total += len(line)
		if total > r.limits.MaxHeaderBytes {
			return nil, ErrHeadersTooLarge
		}

		i := bytes.IndexByte(line, ':')
		if i <= 0 {
			return nil, fmt.Errorf("rtsp: malformed header %q", line)
		}
		name := strings.ToLower(strings.TrimSpace(string(line[:i])))
		if name == "" {
			return nil, fmt.Errorf("rtsp: empty header name in %q", line)
		}
		value := strings.TrimSpace(string(line[i+1:]))
		headers[name] = append(headers[name], value)
	}
}

func parseStartLine(line []byte) (*Message, error) {
	parts := strings.SplitN(string(line), " ", 3)
	if len(parts) == 3 && strings.HasPrefix(parts[2], "RTSP/") {
		return &Message{
			IsRequest: true,
			Method:    Method(parts[0]),
			Target:    parts[1],
			Version:   parts[2],
		}, nil
	}
	if len(parts) >= 2 && strings.HasPrefix(parts[0], "RTSP/") {
		code, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("rtsp: malformed status line %q: %w", line, err)
		}
		reason := ""
		if len(parts) == 3 {
			reason = parts[2]
		}
		return &Message{
			IsRequest:  false,
			Version:    parts[0],
			StatusCode: code,
			Reason:     reason,
		}, nil
	}
	return nil, fmt.Errorf("rtsp: malformed start line %q", line)
}
