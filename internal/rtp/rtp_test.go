package rtp

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseBasic(t *testing.T) {
	b := []byte{
		0x80,       // version 2, no padding/extension, 0 CSRC
		0xe0,       // marker=1, payload type 96
		0x12, 0x34, // sequence 0x1234
		0xde, 0xad, 0xbe, 0xef, // timestamp
		0x01, 0x02, 0x03, 0x04, // SSRC
		0xaa, 0xbb, 0xcc,
	}
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 2 || p.Padding || p.Extension || p.CSRCCount != 0 {
		t.Fatalf("flags = %+v", p)
	}
	if !p.Marker || p.PayloadType != 96 {
		t.Fatalf("marker/type = %v/%d", p.Marker, p.PayloadType)
	}
	if p.Sequence != 0x1234 || p.Timestamp != 0xdeadbeef || p.SSRC != 0x01020304 {
		t.Fatalf("fields = %+v", p)
	}
	if !bytes.Equal(p.Payload, []byte{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("payload = %x", p.Payload)
	}
}

func TestParseCSRCAndExtensionAndPadding(t *testing.T) {
	// 2 CSRCs, extension with 1 word, padding with 4 bytes total.
	b := []byte{
		0xb2, // version 2, padding=1, extension=1, CC=2
		0x60, // marker=0, payload type 96
		0x00, 0x01,
		0, 0, 0, 1,
		0, 0, 0, 2,
		// CSRC 1
		0x11, 0x11, 0x11, 0x11,
		// CSRC 2
		0x22, 0x22, 0x22, 0x22,
		// extension header: profile, length=1 word
		0xbe, 0xef, 0x00, 0x01,
		// extension data (4 bytes)
		0x01, 0x02, 0x03, 0x04,
		// payload + padding: 3 bytes payload, 1 byte padding count = 1
		0xaa, 0xbb, 0xcc, 0x01,
	}
	p, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Padding || !p.Extension || p.CSRCCount != 2 {
		t.Fatalf("flags = %+v", p)
	}
	if len(p.CSRCs) != 2 || p.CSRCs[0] != 0x11111111 || p.CSRCs[1] != 0x22222222 {
		t.Fatalf("csrcs = %#x", p.CSRCs)
	}
	if p.ExtProfile != 0xbeef {
		t.Fatalf("ext profile = %#x", p.ExtProfile)
	}
	if !bytes.Equal(p.ExtData, []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("ext data = %x", p.ExtData)
	}
	if !bytes.Equal(p.Payload, []byte{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("payload = %x", p.Payload)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want error
	}{
		{"short", []byte{0x80, 0x00}, ErrShortPacket},
		{"bad version", []byte{0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, ErrBadVersion},
		{"short csrc", []byte{0x81, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, ErrShortCSRC},
		{"short ext header", []byte{0x90, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, ErrShortHeader},
		{"short ext data", []byte{0x90, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xbe, 0xef, 0x00, 0x01}, ErrShortExtData},
		{"bad padding", []byte{0xa0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x00}, ErrBadPadding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.b); !errors.Is(err, tc.want) {
				t.Fatalf("Parse = %v, want %v", err, tc.want)
			}
		})
	}
}
