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

func TestDecodeAppleSRPPublicKeyFragments(t *testing.T) {
	// A real HAP 384-byte SRP public key is type 3, length 255, then
	// type 3 again, length 129. The high bit is NOT a fragment marker.
	key := bytes.Repeat([]byte{0x5a}, 384)
	wire := append([]byte{3, 255}, key[:255]...)
	wire = append(wire, 3, 129)
	wire = append(wire, key[255:]...)
	items, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Type != 3 || !bytes.Equal(items[0].Value, key) {
		t.Fatal("did not reassemble same-type public key fragments")
	}
	encoded, err := Encode(items)
	if err != nil || !bytes.Equal(encoded, wire) {
		t.Fatal("did not encode same-type fragments")
	}
}

func TestDecodeDistinctRepeatedTypeAndHighType(t *testing.T) {
	wire := []byte{0x81, 1, 0xaa, 3, 1, 0x01, 3, 1, 0x02}
	items, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Type != 0x81 || items[1].Type != 3 || items[2].Type != 3 {
		t.Fatalf("unexpected items: %+v", items)
	}
}
