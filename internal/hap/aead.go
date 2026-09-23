package hap

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"math/big"
	"math/bits"
)

// This file implements the IETF ChaCha20-Poly1305 AEAD (RFC 8439) used by the
// HAP pairing and session-security layers. It is built from the standard
// library so the receiver has no external crypto dependencies and stays
// CGO-free.

var errAuth = errors.New("hap: authentication failed")

// chacha20Block writes one ChaCha20 keystream block (64 bytes) into dst. key
// is 32 bytes and nonce is 12 bytes; counter is the 32-bit block counter.
func chacha20Block(key []byte, counter uint32, nonce []byte, dst []byte) {
	s := [16]uint32{
		0x61707865, 0x3320646e, 0x79622d32, 0x6b206574,
		binary.LittleEndian.Uint32(key[0:4]),
		binary.LittleEndian.Uint32(key[4:8]),
		binary.LittleEndian.Uint32(key[8:12]),
		binary.LittleEndian.Uint32(key[12:16]),
		binary.LittleEndian.Uint32(key[16:20]),
		binary.LittleEndian.Uint32(key[20:24]),
		binary.LittleEndian.Uint32(key[24:28]),
		binary.LittleEndian.Uint32(key[28:32]),
		counter,
		binary.LittleEndian.Uint32(nonce[0:4]),
		binary.LittleEndian.Uint32(nonce[4:8]),
		binary.LittleEndian.Uint32(nonce[8:12]),
	}
	x := s
	for i := 0; i < 10; i++ {
		// Column rounds.
		quarterRound(&x[0], &x[4], &x[8], &x[12])
		quarterRound(&x[1], &x[5], &x[9], &x[13])
		quarterRound(&x[2], &x[6], &x[10], &x[14])
		quarterRound(&x[3], &x[7], &x[11], &x[15])
		// Diagonal rounds.
		quarterRound(&x[0], &x[5], &x[10], &x[15])
		quarterRound(&x[1], &x[6], &x[11], &x[12])
		quarterRound(&x[2], &x[7], &x[8], &x[13])
		quarterRound(&x[3], &x[4], &x[9], &x[14])
	}
	for i := range x {
		binary.LittleEndian.PutUint32(dst[4*i:], x[i]+s[i])
	}
}

func quarterRound(a, b, c, d *uint32) {
	*a += *b
	*d ^= *a
	*d = bits.RotateLeft32(*d, 16)
	*c += *d
	*b ^= *c
	*b = bits.RotateLeft32(*b, 12)
	*a += *b
	*d ^= *a
	*d = bits.RotateLeft32(*d, 8)
	*c += *d
	*b ^= *c
	*b = bits.RotateLeft32(*b, 7)
}

// chacha20XOR XORs src with the ChaCha20 keystream starting at counter and
// writes the result into dst. dst and src must have the same length.
func chacha20XOR(key, nonce []byte, counter uint32, dst, src []byte) {
	var block [64]byte
	for len(src) > 0 {
		chacha20Block(key, counter, nonce, block[:])
		n := len(src)
		if n > 64 {
			n = 64
		}
		for i := 0; i < n; i++ {
			dst[i] = src[i] ^ block[i]
		}
		src = src[n:]
		dst = dst[n:]
		counter++
	}
}

// poly1305Tag computes the Poly1305 one-time authenticator for msg under the
// 32-byte one-time key derived from a ChaCha20 block.
func poly1305Tag(key []byte, msg []byte) [16]byte {
	if len(key) != 32 {
		panic("hap: poly1305 key must be 32 bytes")
	}

	// Clamp the r half of the key.
	rb := make([]byte, 16)
	copy(rb, key[:16])
	rb[3] &= 15
	rb[7] &= 15
	rb[11] &= 15
	rb[15] &= 15
	rb[4] &= 252
	rb[8] &= 252
	rb[12] &= 252

	r := leInt(rb)
	s := leInt(key[16:32])

	// p = 2^130 - 5.
	p := new(big.Int).Lsh(big.NewInt(1), 130)
	p.Sub(p, big.NewInt(5))

	acc := new(big.Int)
	for i := 0; i < len(msg); i += 16 {
		n := len(msg) - i
		if n > 16 {
			n = 16
		}
		block := make([]byte, n+1)
		copy(block, msg[i:i+n])
		block[n] = 1
		acc.Add(acc, leInt(block))
		acc.Mul(acc, r)
		acc.Mod(acc, p)
	}
	acc.Add(acc, s)

	mask := new(big.Int).Lsh(big.NewInt(1), 128)
	mask.Sub(mask, big.NewInt(1))
	acc.And(acc, mask)

	var tag [16]byte
	b := acc.Bytes()
	for i := 0; i < len(b); i++ {
		tag[i] = b[len(b)-1-i]
	}
	return tag
}

func leInt(b []byte) *big.Int {
	r := make([]byte, len(b))
	for i := range b {
		r[i] = b[len(b)-1-i]
	}
	return new(big.Int).SetBytes(r)
}

// buildMacData assembles the Poly1305 input described by RFC 8439 section
// 2.8: aad, padding, ciphertext, padding, and the two 64-bit lengths.
func buildMacData(aad, ciphertext []byte) []byte {
	pad := func(n int) int { return (16 - n%16) % 16 }
	buf := make([]byte, 0, len(aad)+pad(len(aad))+len(ciphertext)+pad(len(ciphertext))+16)
	buf = append(buf, aad...)
	buf = append(buf, make([]byte, pad(len(aad)))...)
	buf = append(buf, ciphertext...)
	buf = append(buf, make([]byte, pad(len(ciphertext)))...)
	var lengths [16]byte
	binary.LittleEndian.PutUint64(lengths[0:8], uint64(len(aad)))
	binary.LittleEndian.PutUint64(lengths[8:16], uint64(len(ciphertext)))
	return append(buf, lengths[:]...)
}

// aeadSeal encrypts plaintext with a 12-byte nonce and returns
// ciphertext||tag.
func aeadSeal(key, nonce, plaintext, aad []byte) []byte {
	if len(nonce) != 12 {
		panic("hap: AEAD nonce must be 12 bytes")
	}
	var block [64]byte
	chacha20Block(key, 0, nonce, block[:])
	polyKey := block[:32]

	out := make([]byte, len(plaintext)+16)
	chacha20XOR(key, nonce, 1, out[:len(plaintext)], plaintext)

	mac := poly1305Tag(polyKey, buildMacData(aad, out[:len(plaintext)]))
	copy(out[len(plaintext):], mac[:])
	return out
}

// aeadOpen decrypts ciphertext||tag and returns plaintext. It fails closed on
// any authentication mismatch.
func aeadOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(nonce) != 12 {
		panic("hap: AEAD nonce must be 12 bytes")
	}
	if len(ciphertext) < 16 {
		return nil, errAuth
	}

	var block [64]byte
	chacha20Block(key, 0, nonce, block[:])
	polyKey := block[:32]

	body := ciphertext[:len(ciphertext)-16]
	got := ciphertext[len(ciphertext)-16:]

	expect := poly1305Tag(polyKey, buildMacData(aad, body))
	if subtle.ConstantTimeCompare(got, expect[:]) != 1 {
		return nil, errAuth
	}

	out := make([]byte, len(body))
	chacha20XOR(key, nonce, 1, out, body)
	return out, nil
}

// padNonce8 left-pads an 8-byte HAP nonce string with four zero bytes to form
// the 12-byte nonce expected by ChaCha20-Poly1305.
func padNonce8(nonce []byte) []byte {
	n := make([]byte, 12)
	copy(n[4:], nonce)
	return n
}
