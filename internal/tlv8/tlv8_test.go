package tlv8

import (
	"bytes"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := []Item{{Type: 1, Value: []byte("abc")}, {Type: 2, Value: []byte{}}}
	enc, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	if out[0].Type != 1 || string(out[0].Value) != "abc" {
		t.Fatalf("item0 = %+v", out[0])
	}
	if out[1].Type != 2 || len(out[1].Value) != 0 {
		t.Fatalf("item1 = %+v", out[1])
	}
}

func TestFragmentRoundTrip(t *testing.T) {
	big := bytes.Repeat([]byte{0xAB}, 600)
	in := []Item{{Type: 3, Value: big}}
	enc, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !bytes.Equal(out[0].Value, big) {
		t.Fatal("fragmented round trip failed")
	}
}

func TestDecodeTruncated(t *testing.T) {
	if _, err := Decode([]byte{0x01}); err == nil {
		t.Fatal("expected truncated header error")
	}
	if _, err := Decode([]byte{0x01, 0x03, 0xAA}); err == nil {
		t.Fatal("expected truncated value error")
	}
}

func TestDecodeOrphanFragment(t *testing.T) {
	if _, err := Decode([]byte{0x81, 0x01, 0xAA}); err == nil {
		t.Fatal("expected orphan fragment error")
	}
}
