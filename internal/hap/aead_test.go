package hap

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"math/rand/v2"
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

func BenchmarkAEADOpenAudioPacket(b *testing.B) {
	key := make([]byte, 32)
	nonce := make([]byte, 12)
	aad := make([]byte, 8)
	sealed := aeadSeal(key, nonce, make([]byte, 1408), aad)
	b.SetBytes(int64(len(sealed)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := aeadOpen(key, nonce, sealed, aad); err != nil {
			b.Fatal(err)
		}
	}
}

// poly1305Reference is a direct big-integer transcription of RFC 8439
// section 2.5, used to cross-check the limb implementation.
func poly1305Reference(key, msg []byte) [16]byte {
	le := func(b []byte) *big.Int {
		r := make([]byte, len(b))
		for i := range b {
			r[i] = b[len(b)-1-i]
		}
		return new(big.Int).SetBytes(r)
	}
	rb := append([]byte(nil), key[:16]...)
	for _, i := range []int{3, 7, 11, 15} {
		rb[i] &= 15
	}
	for _, i := range []int{4, 8, 12} {
		rb[i] &= 252
	}
	r, s := le(rb), le(key[16:32])
	p := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 130), big.NewInt(5))
	acc := new(big.Int)
	for i := 0; i < len(msg); i += 16 {
		n := min(16, len(msg)-i)
		block := append(append([]byte(nil), msg[i:i+n]...), 1)
		acc.Add(acc, le(block))
		acc.Mul(acc, r)
		acc.Mod(acc, p)
	}
	acc.Add(acc, s)
	b := acc.Bytes()
	var tag [16]byte
	for i := 0; i < len(b) && i < 16; i++ {
		tag[i] = b[len(b)-1-i]
	}
	return tag
}

// RFC 8439 appendix A.3 vectors 5-9 exercise the final h >= p reduction.
func TestPoly1305EdgeVectors(t *testing.T) {
	rep := func(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }
	key := func(r byte, s []byte) []byte {
		k := make([]byte, 32)
		k[0] = r
		copy(k[16:], s)
		return k
	}
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for i, tc := range []struct {
		key, msg []byte
		tag      string
	}{
		{key(2, nil), rep(0xff, 16), "03000000000000000000000000000000"},
		{key(2, rep(0xff, 16)), cat([]byte{2}, rep(0, 15)), "03000000000000000000000000000000"},
		{key(1, nil), cat(rep(0xff, 16), rep(0xf0, 1), rep(0xff, 15), []byte{0x11}, rep(0, 15)), "05000000000000000000000000000000"},
		{key(1, nil), cat(rep(0xff, 16), []byte{0xfb}, rep(0xfe, 15), rep(0x01, 16)), "00000000000000000000000000000000"},
		{key(2, nil), cat([]byte{0xfd}, rep(0xff, 15)), "faffffffffffffffffffffffffffffff"},
	} {
		got := poly1305Tag(tc.key, tc.msg)
		if hex.EncodeToString(got[:]) != tc.tag {
			t.Errorf("vector %d: tag %x, want %s", i+5, got, tc.tag)
		}
	}
}

func TestPoly1305MatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	fill := func(b []byte, mode int) {
		for i := range b {
			switch mode {
			case 0:
				b[i] = byte(rng.Uint32())
			case 1:
				b[i] = 0xff
			default:
				b[i] = 0
			}
		}
	}
	for i := 0; i < 3000; i++ {
		key := make([]byte, 32)
		msg := make([]byte, rng.IntN(200))
		fill(key, i%3)
		fill(msg, (i/3)%3)
		if got, want := poly1305Tag(key, msg), poly1305Reference(key, msg); got != want {
			t.Fatalf("case %d: tag %x, want %x (key %x msg %x)", i, got, want, key, msg)
		}
	}
}
