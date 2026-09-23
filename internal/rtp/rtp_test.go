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

func TestParseAACPayloadSingle(t *testing.T) {
	// One AU of 24 bits (3 bytes), index 0.
	p := []byte{
		0x00, 0x10, // AU-headers-length = 16 bits
		0x00, 0xc0, // size = 24 bits, index = 0
		0xaa, 0xbb, 0xcc,
	}
	aus, err := ParseAACPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(aus) != 1 {
		t.Fatalf("got %d AUs, want 1", len(aus))
	}
	if aus[0].SizeBits != 24 || aus[0].Index != 0 {
		t.Fatalf("au = %+v", aus[0])
	}
	if !bytes.Equal(aus[0].Data, []byte{0xaa, 0xbb, 0xcc}) {
		t.Fatalf("data = %x", aus[0].Data)
	}
}

func TestParseAACPayloadMultiple(t *testing.T) {
	// Two AUs: 24 bits (3 bytes) and 16 bits (2 bytes).
	p := []byte{
		0x00, 0x20, // AU-headers-length = 32 bits
		0x00, 0xc0, // 24 bits, index 0
		0x00, 0x81, // 16 bits, index 1
		0xaa, 0xbb, 0xcc,
		0xdd, 0xee,
	}
	aus, err := ParseAACPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2", len(aus))
	}
	if aus[1].SizeBits != 16 || aus[1].Index != 1 {
		t.Fatalf("second au = %+v", aus[1])
	}
	if !bytes.Equal(aus[1].Data, []byte{0xdd, 0xee}) {
		t.Fatalf("second data = %x", aus[1].Data)
	}
}

func TestParseAACPayloadErrors(t *testing.T) {
	cases := []struct {
		name string
		p    []byte
		want error
	}{
		{"empty", nil, ErrAACShort},
		{"one byte", []byte{0x00}, ErrAACShort},
		{"odd header length", []byte{0x00, 0x08, 0x00, 0x00}, ErrAACHeaderLength},
		{"header too large", []byte{0x00, 0x20, 0x00}, ErrAACShort},
		{"au overruns", []byte{0x00, 0x10, 0x00, 0xff, 0xaa}, ErrAACShort},
		{"trailing bytes", []byte{0x00, 0x10, 0x00, 0x08, 0xaa, 0xbb}, ErrAACShort},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAACPayload(tc.p); !errors.Is(err, tc.want) {
				t.Fatalf("ParseAACPayload = %v, want %v", err, tc.want)
			}
		})
	}
}
