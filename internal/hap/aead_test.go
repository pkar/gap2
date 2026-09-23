package hap

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func mustHexBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex: %v", err)
	}
	return b
}

// RFC 8439 section 2.3.2 ChaCha20 block vector.
func TestChaCha20Block(t *testing.T) {
	key := mustHexBytes(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	nonce := mustHexBytes(t, "000000090000004a00000000")
	want := mustHexBytes(t, "10f1e7e4d13b5915500fdd1fa32071c4"+
		"c7d1f4c733c068030422aa9ac3d46c4e"+
		"d2826446079faa0914c2d705d98b02a2"+
		"b5129cd1de164eb9cbd083e8a2503c4e")
	var got [64]byte
	chacha20Block(key, 1, nonce, got[:])
	if !bytes.Equal(got[:], want) {
		t.Fatalf("keystream mismatch\n got %x\nwant %x", got, want)
	}
}

// RFC 8439 section 2.5.2 Poly1305 vector.
func TestPoly1305Tag(t *testing.T) {
	key := mustHexBytes(t, "85d6be7857556d337f4452fe42d506a80103808afb0db2fd4abff6af4149f51b")
	msg := []byte("Cryptographic Forum Research Group")
	want := mustHexBytes(t, "a8061dc1305136c6c22b8baf0c0127a9")
	got := poly1305Tag(key, msg)
	if !bytes.Equal(got[:], want) {
		t.Fatalf("tag mismatch\n got %x\nwant %x", got, want)
	}
}

// RFC 8439 section 2.8.2 AEAD vector, plus a few additional cross-checked
// vectors with empty plaintext and non-empty AAD.
func TestAEADVectors(t *testing.T) {
	key := mustHexBytes(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce := mustHexBytes(t, "070000004041424344454647")

	cases := []struct {
		name string
		pt   string
		aad  string
		ct   string
	}{
		{
			name: "rfc8439",
			pt:   "4c616469657320616e642047656e746c656d656e206f662074686520636c617373206f66202739393a204966204920636f756c64206f6666657220796f75206f6e6c79206f6e652074697020666f7220746865206675747572652c2073756e73637265656e20776f756c642062652069742e",
			aad:  "50515253c0c1c2c3c4c5c6c7",
			ct:   "d31a8d34648e60db7b86afbc53ef7ec2a4aded51296e08fea9e2b5a736ee62d63dbea45e8ca9671282fafb69da92728b1a71de0a9e060b2905d6a5b67ecd3b3692ddbd7f2d778b8c9803aee328091b58fab324e4fad675945585808b4831d7bc3ff4def08e4b7a9de576d26586cec64b61161ae10b594f09e26a7e902ecbd0600691",
		},
		{
			name: "empty",
			pt:   "",
			aad:  "",
			ct:   "a0784d7a4716f3feb4f64e7f4b39bf04",
		},
		{
			name: "short",
			pt:   "1400000cebccee3bf561b292340fec60",
			aad:  "00000000000000001603030010",
			ct:   "8b7be951ea31ae81e0833d69028ee6ce4a272fd784fb313178d790a3048f8ffa",
		},
		{
			name: "aad",
			pt:   "68656c6c6f",
			aad:  "61626364",
			ct:   "f71e85316ee2c8c85d9e633e9e340a0e4dc46ad3c9",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pt := mustHexBytes(t, tc.pt)
			aad := mustHexBytes(t, tc.aad)
			want := mustHexBytes(t, tc.ct)
			got := aeadSeal(key, nonce, pt, aad)
			if !bytes.Equal(got, want) {
				t.Fatalf("seal mismatch\n got %x\nwant %x", got, want)
			}
			opened, err := aeadOpen(key, nonce, got, aad)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if !bytes.Equal(opened, pt) {
				t.Fatalf("round trip mismatch\n got %x\nwant %x", opened, pt)
			}
		})
	}
}

func TestAEADOpenTampered(t *testing.T) {
	key := make([]byte, 32)
	nonce := make([]byte, 12)
	sealed := aeadSeal(key, nonce, []byte("plaintext"), []byte("aad"))

	sealed[0] ^= 1
	if _, err := aeadOpen(key, nonce, sealed, []byte("aad")); err == nil {
		t.Fatal("expected authentication failure for tampered ciphertext")
	}
}
