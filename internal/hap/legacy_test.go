package hap

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestLegacyVerifyExchange(t *testing.T) {
	id, err := NewIdentityFromSeed("accessory", bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	clientX, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientEd := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, 32))
	v := NewLegacyVerify(id, nil)
	m1 := append([]byte{1, 0, 0, 0}, clientX.PublicKey().Bytes()...)
	m1 = append(m1, clientEd.Public().(ed25519.PublicKey)...)
	m2, secret, err := v.Handle(m1)
	if err != nil || secret != nil || len(m2) != 96 {
		t.Fatalf("M2: len=%d secret=%x err=%v", len(m2), secret, err)
	}
	shared, err := clientX.ECDH(mustPublicKey(t, m2[:32]))
	if err != nil {
		t.Fatal(err)
	}
	key, iv := legacyVerifyKeys(shared)
	stream, err := legacyCTR(key, iv)
	if err != nil {
		t.Fatal(err)
	}
	serverSig := append([]byte(nil), m2[32:]...)
	stream.XORKeyStream(serverSig, serverSig)
	if !ed25519.Verify(id.PublicKey(), append(append([]byte(nil), m2[:32]...), clientX.PublicKey().Bytes()...), serverSig) {
		t.Fatal("server signature invalid")
	}
	msg := append(append([]byte(nil), clientX.PublicKey().Bytes()...), m2[:32]...)
	clientSig := ed25519.Sign(clientEd, msg)
	stream.XORKeyStream(clientSig, clientSig)
	m3 := append([]byte{0, 0, 0, 0}, clientSig...)
	if reply, result, err := v.Handle(m3); err != nil || reply != nil || !bytes.Equal(result, shared) {
		t.Fatalf("M4: reply=%x secret=%x err=%v", reply, result, err)
	}
	if _, _, err := v.Handle(m3); err == nil {
		t.Fatal("duplicate M3 accepted")
	}
}

func TestLegacyVerifyRejectsBadSignature(t *testing.T) {
	id, _ := NewIdentityFromSeed("accessory", bytes.Repeat([]byte{3}, 32))
	clientX, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v := NewLegacyVerify(id, nil)
	m1 := append([]byte{1, 0, 0, 0}, clientX.PublicKey().Bytes()...)
	m1 = append(m1, bytes.Repeat([]byte{4}, 32)...)
	if _, _, err := v.Handle(m1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Handle(make([]byte, 68)); err == nil {
		t.Fatal("bad signature accepted")
	}
}

func mustPublicKey(t *testing.T, raw []byte) *ecdh.PublicKey {
	t.Helper()
	key, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
