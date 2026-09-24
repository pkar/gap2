package airplay2

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/pkar/gap2/internal/hap"
)

func TestFPSetup(t *testing.T) {
	s := newTestControlServer(t)
	for mode := byte(0); mode < 4; mode++ {
		var wire bytes.Buffer
		cs := &connState{w: &wire, encrypted: &hap.Conn{}}
		first := []byte{'F', 'P', 'L', 'Y', 3, 1, 1, 0, 0, 0, 0, 4, 2, 0, mode, 0}
		req := &ctlRequest{body: first}
		if err := s.handleFPSetup(cs, req); err != nil {
			t.Fatal(err)
		}
		body := bytes.SplitN(wire.Bytes(), []byte("\r\n\r\n"), 2)[1]
		if len(body) != 142 || body[6] != 2 || body[13] != mode {
			t.Fatalf("invalid stage one response for mode %d", mode)
		}
		second := make([]byte, 164)
		copy(second, first[:12])
		second[6] = 3
		binary.BigEndian.PutUint32(second[8:12], 152)
		for i := 144; i < 164; i++ {
			second[i] = byte(i)
		}
		wire.Reset()
		if err := s.handleFPSetup(cs, &ctlRequest{body: second}); err != nil {
			t.Fatal(err)
		}
		body = bytes.SplitN(wire.Bytes(), []byte("\r\n\r\n"), 2)[1]
		if len(body) != 32 || body[6] != 4 || !bytes.Equal(body[12:], second[144:]) {
			t.Fatal("invalid stage two response")
		}
		wire.Reset()
		_ = s.handleFPSetup(cs, &ctlRequest{body: second})
		if !strings.Contains(wire.String(), "400 Bad Request") {
			t.Fatal("accepted repeated stage two")
		}
	}
}

func TestFPSetupRejectsInvalidRequests(t *testing.T) {
	s := newTestControlServer(t)
	valid := []byte{'F', 'P', 'L', 'Y', 3, 1, 1, 0, 0, 0, 0, 4, 2, 0, 0, 0}
	cases := [][]byte{nil, valid[:4], valid[:15], append(bytes.Clone(valid), 0)}
	for _, offset := range []int{0, 4, 5, 6, 11, 14} {
		b := bytes.Clone(valid)
		b[offset] = 255
		cases = append(cases, b)
	}
	for _, b := range cases {
		var wire bytes.Buffer
		cs := &connState{w: &wire, encrypted: &hap.Conn{}}
		if err := s.handleFPSetup(cs, &ctlRequest{body: b}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(wire.String(), "400 Bad Request") {
			t.Fatal("accepted malformed setup")
		}
	}
	var wire bytes.Buffer
	_ = s.handleFPSetup(&connState{w: &wire}, &ctlRequest{body: valid})
	if !strings.Contains(wire.String(), "401 Unauthorized") {
		t.Fatal("accepted unencrypted setup")
	}
}
