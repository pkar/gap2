package hap

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
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

// poly1305 is a constant-time Poly1305 accumulator using three 64-bit limbs
// (RFC 8439 section 2.5). h is the accumulator; r and s are the clamped key
// halves.
type poly1305 struct {
	h [3]uint64
	r [2]uint64
	s [2]uint64
}

func newPoly1305(key []byte) poly1305 {
	if len(key) != 32 {
		panic("hap: poly1305 key must be 32 bytes")
	}
	return poly1305{
		r: [2]uint64{
			binary.LittleEndian.Uint64(key[0:8]) & 0x0FFFFFFC0FFFFFFF,
			binary.LittleEndian.Uint64(key[8:16]) & 0x0FFFFFFC0FFFFFFC,
		},
		s: [2]uint64{
			binary.LittleEndian.Uint64(key[16:24]),
			binary.LittleEndian.Uint64(key[24:32]),
		},
	}
}

// block absorbs one 16-byte little-endian block plus the 2^128 marker bit
// hibit (1 for a full block, 0 when the caller already appended the 0x01 pad).
func (p *poly1305) block(b []byte, hibit uint64) {
	h0, h1, h2 := p.h[0], p.h[1], p.h[2]
	r0, r1 := p.r[0], p.r[1]
	var c uint64
	h0, c = bits.Add64(h0, binary.LittleEndian.Uint64(b[0:8]), 0)
	h1, c = bits.Add64(h1, binary.LittleEndian.Uint64(b[8:16]), c)
	h2 += c + hibit

	// h * r. h2 stays below 8 and r is clamped, so no product term
	// overflows 128 bits.
	h0r0hi, h0r0lo := bits.Mul64(h0, r0)
	h1r0hi, h1r0lo := bits.Mul64(h1, r0)
	h2r0hi, h2r0lo := bits.Mul64(h2, r0)
	h0r1hi, h0r1lo := bits.Mul64(h0, r1)
	h1r1hi, h1r1lo := bits.Mul64(h1, r1)
	h2r1hi, h2r1lo := bits.Mul64(h2, r1)

	m1lo, c := bits.Add64(h1r0lo, h0r1lo, 0)
	m1hi, _ := bits.Add64(h1r0hi, h0r1hi, c)
	m2lo, c := bits.Add64(h2r0lo, h1r1lo, 0)
	m2hi, _ := bits.Add64(h2r0hi, h1r1hi, c)

	t0 := h0r0lo
	t1, c := bits.Add64(m1lo, h0r0hi, 0)
	t2, c := bits.Add64(m2lo, m1hi, c)
	t3, _ := bits.Add64(h2r1lo, m2hi, c)
	_ = h2r1hi // always zero: h2 < 8 and r1 < 2^60

	// Reduce modulo 2^130-5: fold (t >> 130) * 5 back into the low 130 bits
	// as (t >> 130) * 4 + (t >> 130).
	h0, h1, h2 = t0, t1, t2&3
	cLo, cHi := t2&^3, t3
	h0, c = bits.Add64(h0, cLo, 0)
	h1, c = bits.Add64(h1, cHi, c)
	h2 += c
	cLo, cHi = cLo>>2|cHi<<62, cHi>>2
	h0, c = bits.Add64(h0, cLo, 0)
	h1, c = bits.Add64(h1, cHi, c)
	h2 += c
	p.h[0], p.h[1], p.h[2] = h0, h1, h2
}

// write absorbs msg; a trailing partial block gets the RFC 8439 0x01 pad.
func (p *poly1305) write(msg []byte) {
	for len(msg) >= 16 {
		p.block(msg[:16], 1)
		msg = msg[16:]
	}
	if len(msg) > 0 {
		var buf [16]byte
		copy(buf[:], msg)
		buf[len(msg)] = 1
		p.block(buf[:], 0)
	}
}

// writePadded absorbs msg zero-padded to a multiple of 16 bytes, as the AEAD
// construction requires for the AAD and ciphertext.
func (p *poly1305) writePadded(msg []byte) {
	for len(msg) >= 16 {
		p.block(msg[:16], 1)
		msg = msg[16:]
	}
	if len(msg) > 0 {
		var buf [16]byte
		copy(buf[:], msg)
		p.block(buf[:], 1)
	}
}

func (p *poly1305) sum() [16]byte {
	h0, h1, h2 := p.h[0], p.h[1], p.h[2]
	// Constant-time select of h - p when h >= p = 2^130-5.
	g0, b := bits.Sub64(h0, 0xFFFFFFFFFFFFFFFB, 0)
	g1, b := bits.Sub64(h1, 0xFFFFFFFFFFFFFFFF, b)
	_, b = bits.Sub64(h2, 3, b)
	mask := b - 1 // all ones when there was no borrow (h >= p)
	h0 = h0&^mask | g0&mask
	h1 = h1&^mask | g1&mask

	var c uint64
	h0, c = bits.Add64(h0, p.s[0], 0)
	h1, _ = bits.Add64(h1, p.s[1], c)
	var tag [16]byte
	binary.LittleEndian.PutUint64(tag[0:8], h0)
	binary.LittleEndian.PutUint64(tag[8:16], h1)
	return tag
}

// poly1305Tag computes the Poly1305 one-time authenticator for msg under the
// 32-byte one-time key derived from a ChaCha20 block.
func poly1305Tag(key []byte, msg []byte) [16]byte {
	p := newPoly1305(key)
	p.write(msg)
	return p.sum()
}

// aeadTag computes the RFC 8439 section 2.8 tag over aad, padding,
// ciphertext, padding, and the two 64-bit lengths without assembling them in
// a temporary buffer.
func aeadTag(polyKey, aad, ciphertext []byte) [16]byte {
	p := newPoly1305(polyKey)
	p.writePadded(aad)
	p.writePadded(ciphertext)
	var lengths [16]byte
	binary.LittleEndian.PutUint64(lengths[0:8], uint64(len(aad)))
	binary.LittleEndian.PutUint64(lengths[8:16], uint64(len(ciphertext)))
	p.block(lengths[:], 1)
	return p.sum()
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

	mac := aeadTag(polyKey, aad, out[:len(plaintext)])
	copy(out[len(plaintext):], mac[:])
	return out
}

// aeadOpen decrypts ciphertext||tag and returns plaintext. It fails closed on
// any authentication mismatch.
func aeadOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) < 16 {
		return nil, errAuth
	}
	return aeadOpenTo(make([]byte, len(ciphertext)-16), key, nonce, ciphertext, aad)
}

// aeadOpenTo is aeadOpen writing the plaintext into dst, which must be
// len(ciphertext)-16 bytes. dst may alias the start of ciphertext exactly.
func aeadOpenTo(dst, key, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(nonce) != 12 {
		panic("hap: AEAD nonce must be 12 bytes")
	}
	if len(ciphertext) < 16 || len(dst) != len(ciphertext)-16 {
		return nil, errAuth
	}

	var block [64]byte
	chacha20Block(key, 0, nonce, block[:])
	polyKey := block[:32]

	body := ciphertext[:len(ciphertext)-16]
	got := ciphertext[len(ciphertext)-16:]

	expect := aeadTag(polyKey, aad, body)
	if subtle.ConstantTimeCompare(got, expect[:]) != 1 {
		return nil, errAuth
	}

	chacha20XOR(key, nonce, 1, dst, body)
	return dst, nil
}

// padNonce8 left-pads an 8-byte HAP nonce string with four zero bytes to form
// the 12-byte nonce expected by ChaCha20-Poly1305.
func padNonce8(nonce []byte) []byte {
	n := make([]byte, 12)
	copy(n[4:], nonce)
	return n
}
