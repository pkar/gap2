package hap

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"math/big"
	"sync"
	"testing"

	"github.com/pkar/gap2/internal/tlv8"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]Pairing
}

func newMemStore() *memStore { return &memStore{m: map[string]Pairing{}} }

func (s *memStore) Get(id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[id]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), p.LTPK...), true, nil
}

func (s *memStore) Put(id string, ltpk []byte, admin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = Pairing{Identifier: id, LTPK: append([]byte(nil), ltpk...), Admin: admin}
	return nil
}

func (s *memStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

func (s *memStore) List() ([]Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Pairing, 0, len(s.m))
	for _, p := range s.m {
		out = append(out, Pairing{Identifier: p.Identifier, LTPK: append([]byte(nil), p.LTPK...), Admin: p.Admin})
	}
	return out, nil
}

func u32flags(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// repeatReader yields an unbounded stream of one byte value. It is used where
// crypto libraries call MaybeReadByte, which may read an extra byte beyond the
// fixed seed (e.g. ecdh.GenerateKey and ed25519.GenerateKey).
type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

// srpClient performs a full client-side Pair Setup against the accessory
// session and returns the derived session key.
func srpClient(t *testing.T, sess *PairSetupSession, clientPriv ed25519.PrivateKey, flags uint32) []byte {
	t.Helper()

	// M1.
	m1, err := encodeTLV([]tlv8.Item{
		{Type: TLVMethod, Value: []byte{MethodPairSetup}},
		{Type: TLVFlags, Value: u32flags(flags)},
		{Type: TLVState, Value: []byte{StateM1}},
	})
	if err != nil {
		t.Fatalf("m1 encode: %v", err)
	}
	m2Res, err := sess.Handle(m1)
	if err != nil {
		t.Fatalf("handle M1: %v", err)
	}
	m2, err := tlv8.Decode(m2Res.Response)
	if err != nil {
		t.Fatalf("m2 decode: %v", err)
	}
	if state, _ := getItem(m2, TLVState); !bytes.Equal(state, []byte{StateM2}) {
		t.Fatalf("unexpected M2 state %x", state)
	}
	salt, _ := requireItem(m2, TLVSalt)
	serverB, _ := requireItem(m2, TLVPublicKey)
	B := new(big.Int).SetBytes(serverB)

	a := new(big.Int).SetBytes(bytes.Repeat([]byte{0x33}, 64))
	a.Mod(a, srpN)
	A := new(big.Int).Exp(srpG, a, srpN)

	x := srpHash(false, salt, srpHash(false, "Pair-Setup:3939"))
	v := new(big.Int).Exp(srpG, x, srpN)
	u := srpHash(true, A, B)
	k := srpHash(true, srpN, srpG)

	// S = (B - k*v)^(a + u*x) mod N
	S := new(big.Int).Mul(k, v)
	S.Sub(B, S)
	S.Mod(S, srpN)
	if S.Sign() < 0 {
		S.Add(S, srpN)
	}
	exp := new(big.Int).Mul(u, x)
	exp.Add(exp, a)
	S.Exp(S, exp, srpN)
	K := srpHash(false, S)

	hN := srpHash(false, srpN)
	hG := srpHash(false, srpG)
	hNG := new(big.Int).Xor(hN, hG)
	M1 := srpHash(false, hNG, srpHash(false, "Pair-Setup"), salt, A, B, K)

	// M3.
	m3, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM3}},
		{Type: TLVPublicKey, Value: A.Bytes()},
		{Type: TLVProof, Value: M1.Bytes()},
	})
	if err != nil {
		t.Fatalf("m3 encode: %v", err)
	}
	m4Res, err := sess.Handle(m3)
	if err != nil {
		t.Fatalf("handle M3: %v", err)
	}
	m4, err := tlv8.Decode(m4Res.Response)
	if err != nil {
		t.Fatalf("m4 decode: %v", err)
	}
	serverM2, _ := requireItem(m4, TLVProof)
	if got, want := srpHash(false, A, M1, K).Bytes(), serverM2; !bytes.Equal(got, want) {
		t.Fatalf("server M2 mismatch\n got %x\nwant %x", got, want)
	}
	if flags&FlagTransient != 0 {
		if !m4Res.Done || !bytes.Equal(m4Res.SessionKey, K.Bytes()) {
			t.Fatal("transient Pair Setup must finish at M4 with the SRP session key")
		}
		return m4Res.SessionKey
	}
	if m4Res.Done || len(m4Res.SessionKey) != 0 {
		t.Fatal("persistent Pair Setup must continue through M5/M6")
	}

	// M5.
	ikm := K.Bytes()
	encKey := hkdfSHA512(ikm, []byte(pairSetupEncryptSalt), []byte(pairSetupEncryptInfo), 32)
	clientLTPK := []byte(clientPriv.Public().(ed25519.PublicKey))
	controllerX := hkdfSHA512(ikm, []byte(pairSetupControllerSignSalt), []byte(pairSetupControllerSignInfo), 32)
	signed := append(append(append([]byte{}, controllerX...), "client-id"...), clientLTPK...)
	clientSig := ed25519.Sign(clientPriv, signed)

	subPlain, err := encodeTLV([]tlv8.Item{
		{Type: TLVIdentifier, Value: []byte("client-id")},
		{Type: TLVPublicKey, Value: clientLTPK},
		{Type: TLVSignature, Value: clientSig},
	})
	if err != nil {
		t.Fatalf("m5 sub encode: %v", err)
	}
	m5, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM5}},
		{Type: TLVEncryptedData, Value: aeadSeal(encKey, padNonce8([]byte("PS-Msg05")), subPlain, nil)},
	})
	if err != nil {
		t.Fatalf("m5 encode: %v", err)
	}
	m6Res, err := sess.Handle(m5)
	if err != nil {
		t.Fatalf("handle M5: %v", err)
	}
	if !m6Res.Done {
		t.Fatal("pair setup did not complete")
	}
	return m6Res.SessionKey
}

func TestPairSetupPersistent(t *testing.T) {
	accID, err := NewIdentity(repeatReader{0x07}, "accessory-id")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	store := newMemStore()
	sess := NewPairSetupSession(bytes.NewReader(bytes.Repeat([]byte{0x05}, 80)), accID, "3939", store)

	clientPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, 32))
	key := srpClient(t, sess, clientPriv, 0)
	if key != nil {
		t.Fatalf("persistent pairing should not produce a session key, got %x", key)
	}
	ltpk, ok, err := store.Get("client-id")
	if err != nil || !ok {
		t.Fatalf("persistent pairing not stored: ok=%v err=%v", ok, err)
	}
	want := clientPriv.Public().(ed25519.PublicKey)
	if !bytes.Equal(ltpk, want) {
		t.Fatalf("stored LTPK mismatch\n got %x\nwant %x", ltpk, want)
	}
}

func TestPairSetupM1VariableWidthFlags(t *testing.T) {
	cases := []struct {
		name      string
		flags     []byte
		transient bool
		reject    bool
	}{
		{"empty", nil, false, false},
		{"zero", []byte{0}, false, false},
		{"one-byte transient", []byte{0x10}, true, false},
		{"two-byte transient", []byte{0x10, 0}, true, false},
		{"three-byte transient", []byte{0x10, 0, 0}, true, false},
		{"four-byte transient", []byte{0x10, 0, 0, 0}, true, false},
		{"split little-endian", []byte{0, 0, 0, 1}, false, true},
		{"oversize", []byte{0x10, 0, 0, 0, 0}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := NewIdentity(repeatReader{0x07}, "accessory-id")
			if err != nil {
				t.Fatal(err)
			}
			sess := NewPairSetupSession(repeatReader{0x05}, id, "3939", newMemStore())
			// The one-byte case is the nine-byte M1 sent by a real Mac.
			m1, err := encodeTLV([]tlv8.Item{
				{Type: TLVMethod, Value: []byte{MethodPairSetup}},
				{Type: TLVFlags, Value: tc.flags},
				{Type: TLVState, Value: []byte{StateM1}},
			})
			if err != nil {
				t.Fatal(err)
			}
			res, err := sess.Handle(m1)
			if tc.reject {
				if err == nil {
					t.Fatal("expected flags rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("M1: %v", err)
			}
			if sess.transient != tc.transient {
				t.Fatalf("transient = %v, want %v", sess.transient, tc.transient)
			}
			items, err := tlv8.Decode(res.Response)
			if err != nil {
				t.Fatal(err)
			}
			if state, ok := getItem(items, TLVState); !ok || !bytes.Equal(state, []byte{StateM2}) {
				t.Fatalf("M2 state = %v", state)
			}
		})
	}
}

func TestPairSetupTransient(t *testing.T) {
	accID, err := NewIdentity(repeatReader{0x07}, "accessory-id")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	store := newMemStore()
	sess := NewPairSetupSession(bytes.NewReader(bytes.Repeat([]byte{0x05}, 80)), accID, "3939", store)

	clientPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, 32))
	key := srpClient(t, sess, clientPriv, FlagTransient)
	if key == nil {
		t.Fatal("transient pairing should produce a session key")
	}
	if _, ok, _ := store.Get("client-id"); ok {
		t.Fatal("transient pairing must not be persisted")
	}
}

func TestPairSetupWrongProof(t *testing.T) {
	accID, _ := NewIdentity(repeatReader{0x07}, "accessory-id")
	sess := NewPairSetupSession(bytes.NewReader(bytes.Repeat([]byte{0x05}, 80)), accID, "3939", newMemStore())

	m1, _ := encodeTLV([]tlv8.Item{
		{Type: TLVMethod, Value: []byte{MethodPairSetup}},
		{Type: TLVState, Value: []byte{StateM1}},
	})
	_, err := sess.Handle(m1)
	if err != nil {
		t.Fatalf("M1: %v", err)
	}
	m3, _ := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM3}},
		{Type: TLVPublicKey, Value: bytes.Repeat([]byte{0x01}, 64)},
		{Type: TLVProof, Value: []byte{0xde, 0xad}},
	})
	res, err := sess.Handle(m3)
	if err == nil {
		t.Fatal("expected authentication failure for bad proof")
	}
	if !bytes.Contains(res.Response, []byte{ErrorAuthentication}) {
		t.Fatalf("expected error response, got %x", res.Response)
	}
}

func TestPairVerify(t *testing.T) {
	// Establish a persistent pairing first, then verify it.
	accID, err := NewIdentity(repeatReader{0x07}, "accessory-id")
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	store := newMemStore()
	setupSess := NewPairSetupSession(bytes.NewReader(bytes.Repeat([]byte{0x05}, 80)), accID, "3939", store)
	clientPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, 32))
	if key := srpClient(t, setupSess, clientPriv, 0); key != nil {
		t.Fatal("unexpected session key")
	}

	// Pair Verify M1.
	clientPrivX, clientPub, err := x25519KeyPair(repeatReader{0x09})
	if err != nil {
		t.Fatalf("x25519: %v", err)
	}
	verifySess := NewPairVerifySession(repeatReader{0x0a}, accID, store)

	m1, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM1}},
		{Type: TLVPublicKey, Value: clientPub},
	})
	if err != nil {
		t.Fatalf("m1 encode: %v", err)
	}
	m2Res, err := verifySess.Handle(m1)
	if err != nil {
		t.Fatalf("handle M1: %v", err)
	}
	m2, err := tlv8.Decode(m2Res.Response)
	if err != nil {
		t.Fatalf("m2 decode: %v", err)
	}
	serverPub, _ := requireItem(m2, TLVPublicKey)
	enc, _ := requireItem(m2, TLVEncryptedData)

	shared, err := x25519Shared(clientPrivX, serverPub)
	if err != nil {
		t.Fatalf("shared: %v", err)
	}
	sessionKey := hkdfSHA512(shared, []byte(pairVerifyEncryptSalt), []byte(pairVerifyEncryptInfo), 32)
	plain, err := aeadOpen(sessionKey, padNonce8([]byte("PV-Msg02")), enc, nil)
	if err != nil {
		t.Fatalf("M2 decrypt: %v", err)
	}
	sub, err := tlv8.Decode(plain)
	if err != nil {
		t.Fatalf("M2 sub decode: %v", err)
	}
	accIDSent, _ := requireItem(sub, TLVIdentifier)
	accSig, _ := requireItem(sub, TLVSignature)
	signed := append(append(append([]byte{}, serverPub...), accIDSent...), clientPub...)
	if !verifySignature(accID.PublicKey(), signed, accSig) {
		t.Fatal("accessory M2 signature invalid")
	}

	// Pair Verify M3.
	signed = append(append(append([]byte{}, clientPub...), "client-id"...), serverPub...)
	clientSig := ed25519.Sign(clientPriv, signed)
	m3Sub, _ := encodeTLV([]tlv8.Item{
		{Type: TLVIdentifier, Value: []byte("client-id")},
		{Type: TLVSignature, Value: clientSig},
	})
	m3, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM3}},
		{Type: TLVEncryptedData, Value: aeadSeal(sessionKey, padNonce8([]byte("PV-Msg03")), m3Sub, nil)},
	})
	if err != nil {
		t.Fatalf("m3 encode: %v", err)
	}
	m4Res, err := verifySess.Handle(m3)
	if err != nil {
		t.Fatalf("handle M3: %v", err)
	}
	if !m4Res.Done {
		t.Fatal("pair verify did not complete")
	}
	if !bytes.Equal(m4Res.SessionKey, shared) {
		t.Fatalf("session key mismatch\n got %x\nwant %x", m4Res.SessionKey, shared)
	}
	if m4Res.ClientID != "client-id" {
		t.Fatalf("unexpected client id %q", m4Res.ClientID)
	}

	// A wrong controller signature must fail.
	badSig := append([]byte(nil), clientSig...)
	badSig[0] ^= 1
	m3BadSub, _ := encodeTLV([]tlv8.Item{
		{Type: TLVIdentifier, Value: []byte("client-id")},
		{Type: TLVSignature, Value: badSig},
	})
	m3Bad, _ := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM3}},
		{Type: TLVEncryptedData, Value: aeadSeal(sessionKey, padNonce8([]byte("PV-Msg03")), m3BadSub, nil)},
	})
	res, err := verifySess.Handle(m3Bad)
	if err != nil {
		t.Fatalf("handle bad M3: %v", err)
	}
	if !bytes.Contains(res.Response, []byte{ErrorAuthentication}) {
		t.Fatalf("expected auth error, got %x", res.Response)
	}
}
