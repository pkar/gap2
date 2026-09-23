package hap

import (
	"bytes"
	"testing"
)

// HKDF-SHA-512 vector generated with an independent implementation
// (RFC 5869 with SHA-512 as the hash).
func TestHKDFSHA512(t *testing.T) {
	ikm := make([]byte, 22)
	for i := range ikm {
		ikm[i] = 0x0b
	}
	salt := mustHexBytes(t, "000102030405060708090a0b0c")
	info := mustHexBytes(t, "f0f1f2f3f4f5f6f7f8f9")
	want := mustHexBytes(t, "832390086cda71fb47625bb5ceb168e4c8e26a1a16ed34d9fc7fe92c14815793")

	got := hkdfSHA512(ikm, salt, info, 32)
	if !bytes.Equal(got, want) {
		t.Fatalf("hkdf mismatch\n got %x\nwant %x", got, want)
	}

	// Different length must be a prefix extension, not a recomputation.
	got48 := hkdfSHA512(ikm, salt, info, 48)
	if !bytes.Equal(got48[:32], want) {
		t.Fatal("hkdf 48-byte output does not extend 32-byte output")
	}
}

func TestHKDFSHA512EmptySalt(t *testing.T) {
	ikm := []byte("input key material")
	got := hkdfSHA512(ikm, nil, []byte("info"), 32)
	if len(got) != 32 {
		t.Fatalf("unexpected length %d", len(got))
	}
}
