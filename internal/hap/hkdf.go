package hap

import (
	"crypto/hmac"
	"crypto/sha512"
)

// hkdfSHA512 implements RFC 5869 HKDF with SHA-512. HAP pairing derives all
// session and control keys with SHA-512.
func hkdfSHA512(ikm, salt, info []byte, length int) []byte {
	prk := extract(salt, ikm)
	return expand(prk, info, length)
}

func extract(salt, ikm []byte) []byte {
	if len(salt) == 0 {
		salt = make([]byte, sha512.Size)
	}
	h := hmac.New(sha512.New, salt)
	h.Write(ikm)
	return h.Sum(nil)
}

func expand(prk, info []byte, length int) []byte {
	if length < 0 || length > 255*sha512.Size {
		panic("hap: invalid HKDF output length")
	}
	var out []byte
	var t []byte
	for i := byte(1); len(out) < length; i++ {
		h := hmac.New(sha512.New, prk)
		h.Write(t)
		h.Write(info)
		h.Write([]byte{i})
		t = h.Sum(nil)
		out = append(out, t...)
	}
	return out[:length]
}
