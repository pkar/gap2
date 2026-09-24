package plist

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCompactUnsignedIntegersFromIndependentEncoder(t *testing.T) {
	// Python plistlib fixture, including the two-byte sample rate 0xac44.
	b, _ := hex.DecodeString("62706c6973743030d301020304050653737066527372547479706511040011ac441067080f13161b1e210000000000000101000000000000000700000000000000000000000000000023")
	v, err := Decode(b, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int64{"sr": 44100, "spf": 1024, "type": 103} {
		if v.Dict[key].Int != want {
			t.Fatalf("%s = %d, want %d", key, v.Dict[key].Int, want)
		}
	}
}

func TestNegativeIntegersUseEightBytes(t *testing.T) {
	for _, n := range []int64{-1, -128, -32768} {
		b, err := Encode(Int(n))
		if err != nil {
			t.Fatal(err)
		}
		if b[8] != 0x13 {
			t.Fatal("negative integer was not encoded in eight bytes")
		}
		v, err := Decode(b, DefaultLimits())
		if err != nil || v.Int != n {
			t.Fatalf("negative round trip: %v, %v", v, err)
		}
	}
}

func TestOffsetWidthIncludesPlistHeader(t *testing.T) {
	v := Array(String(strings.Repeat("x", 245)))
	b, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(b, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	again, err := Encode(decoded)
	if err != nil || !bytes.Equal(b, again) {
		t.Fatal("offset boundary corrupted plist")
	}
}
