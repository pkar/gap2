package hap

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/sha512"
	"fmt"
	"io"
)

// LegacyVerify handles the binary, non-TLV pair-verify exchange used by
// AirPlay's legacy pairing mode. It is separate from HomeKit pair-verify.
type LegacyVerify struct {
	identity Identity
	rand     io.Reader
	private  *ecdh.PrivateKey
	clientX  []byte
	serverX  []byte
	clientEd []byte
	shared   []byte
}

func NewLegacyVerify(identity Identity, rand io.Reader) *LegacyVerify {
	return &LegacyVerify{identity: identity, rand: rand}
}

// Handle consumes M1 (01 00 00 00 || X25519 || Ed25519) or M3
// (00 00 00 00 || encrypted signature). M2 is X25519 || AES-CTR signature;
// M4 is empty. The shared secret becomes usable only after M3 authenticates.
func (v *LegacyVerify) Handle(body []byte) (reply, shared []byte, err error) {
	if len(body) < 4 || body[1] != 0 || body[2] != 0 || body[3] != 0 {
		return nil, nil, fmt.Errorf("hap: invalid legacy verify header")
	}
	switch body[0] {
	case 1:
		if len(body) != 68 || v.private != nil {
			return nil, nil, fmt.Errorf("hap: invalid legacy verify M1")
		}
		priv, serverX, err := x25519KeyPair(v.rand)
		if err != nil {
			return nil, nil, err
		}
		clientX := append([]byte(nil), body[4:36]...)
		secret, err := x25519Shared(priv, clientX)
		if err != nil {
			return nil, nil, err
		}
		sig := v.identity.Sign(append(append([]byte(nil), serverX...), clientX...))
		key, iv := legacyVerifyKeys(secret)
		stream, err := legacyCTR(key, iv)
		if err != nil {
			return nil, nil, err
		}
		stream.XORKeyStream(sig, sig)
		v.private, v.serverX, v.clientX = priv, serverX, clientX
		v.clientEd = append([]byte(nil), body[36:68]...)
		v.shared = secret
		return append(serverX, sig...), nil, nil
	case 0:
		if len(body) != 68 || v.private == nil || v.shared == nil {
			return nil, nil, fmt.Errorf("hap: invalid legacy verify M3")
		}
		key, iv := legacyVerifyKeys(v.shared)
		stream, err := legacyCTR(key, iv)
		if err != nil {
			return nil, nil, err
		}
		// M2 consumed 64 bytes of the AES-CTR stream. M3 continues at
		// counter block 4, not at the initial counter block.
		var skip [64]byte
		stream.XORKeyStream(skip[:], skip[:])
		plain := make([]byte, 64)
		stream.XORKeyStream(plain, body[4:])
		msg := append(append([]byte(nil), v.clientX...), v.serverX...)
		if !verifySignature(v.clientEd, msg, plain) {
			v.shared = nil
			return nil, nil, fmt.Errorf("hap: invalid legacy verify signature")
		}
		shared = v.shared
		v.shared = nil // reject duplicate M3
		return nil, shared, nil
	default:
		return nil, nil, fmt.Errorf("hap: invalid legacy verify stage")
	}
}

func legacyVerifyKeys(shared []byte) (key, iv []byte) {
	k := sha512.Sum512(append([]byte("Pair-Verify-AES-Key"), shared...))
	i := sha512.Sum512(append([]byte("Pair-Verify-AES-IV"), shared...))
	return k[:16], i[:16]
}

func legacyCTR(key, iv []byte) (cipher.Stream, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewCTR(block, iv), nil
}
