package rtsp

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadRequestWithBody(t *testing.T) {
	raw := "POST /pair-setup RTSP/1.0\r\n" +
		"Content-Length: 5\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"CSeq: 7\r\n" +
		"\r\n" +
		"hello"
	r := NewReader(strings.NewReader(raw), DefaultLimits())
	m, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsRequest || m.Method != MethodPost || m.Target != "/pair-setup" {
		t.Fatalf("bad request: %+v", m)
	}
	if string(m.Body) != "hello" {
		t.Fatalf("body = %q", m.Body)
	}
	if m.Header("content-type") != "application/octet-stream" {
		t.Fatal("header case-insensitivity failed")
	}
	if cseq, err := m.CSeq(); err != nil || cseq != 7 {
		t.Fatalf("CSeq = %d, %v", cseq, err)
	}
}

func TestReadResponse(t *testing.T) {
	raw := "RTSP/1.0 200 OK\r\nServer: AirTunes/366.0\r\n\r\n"
	r := NewReader(strings.NewReader(raw), DefaultLimits())
	m, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if m.IsRequest || m.StatusCode != 200 {
		t.Fatalf("bad response: %+v", m)
	}
}

func TestPipelined(t *testing.T) {
	raw := "OPTIONS * RTSP/1.0\r\nCSeq: 1\r\n\r\n" +
		"GET /info RTSP/1.0\r\nCSeq: 2\r\n\r\n"
	r := NewReader(strings.NewReader(raw), DefaultLimits())
	m1, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	m2, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if m1.Method != MethodOptions || m2.Method != MethodGet {
		t.Fatalf("pipelined methods: %s %s", m1.Method, m2.Method)
	}
}

func TestOversizedLine(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxLineBytes = 8
	r := NewReader(strings.NewReader("OPTIONS * RTSP/1.0\r\n"), limits)
	if _, err := r.Read(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
}

func TestBodyTooLarge(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxBodyBytes = 4
	r := NewReader(strings.NewReader("POST /x RTSP/1.0\r\nContent-Length: 10\r\n\r\n"), limits)
	if _, err := r.Read(); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v, want ErrBodyTooLarge", err)
	}
}

func TestTruncated(t *testing.T) {
	r := NewReader(strings.NewReader("POST /x RTSP/1.0\r\nContent-Length: 10\r\n\r\nshort"), DefaultLimits())
	if _, err := r.Read(); err == nil {
		t.Fatal("expected error")
	}
}

func TestEOF(t *testing.T) {
	r := NewReader(strings.NewReader(""), DefaultLimits())
	if _, err := r.Read(); err != io.EOF {
		t.Fatalf("err = %v, want EOF", err)
	}
}
