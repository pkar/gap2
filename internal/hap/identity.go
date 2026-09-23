package hap

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"fmt"
	"io"
)

// Identity is the accessory's long-term Ed25519 identity. HAP pairing signs
// session material with this key; pairings persist its 32-byte public key.
type Identity struct {
	// ID is the accessory pairing identifier, stored as UTF-8 bytes.
	ID []byte
	// privateKey is the 64-byte Ed25519 seed+public key.
	privateKey ed25519.PrivateKey
}

// NewIdentity generates an Ed25519 identity with the given pairing
// identifier. rand may be nil to use the crypto/rand source.
func NewIdentity(rand io.Reader, id string) (Identity, error) {
	if rand == nil {
		rand = cryptoRand{}
	}
	_, priv, err := ed25519.GenerateKey(rand)
	if err != nil {
		return Identity{}, err
	}
	return Identity{ID: []byte(id), privateKey: priv}, nil
}

// NewIdentityFromSeed restores an identity from a 32-byte Ed25519 seed. It is
// used to persist and reload the accessory long-term key.
func NewIdentityFromSeed(id string, seed []byte) (Identity, error) {
	if len(seed) != ed25519.SeedSize {
		return Identity{}, fmt.Errorf("hap: ed25519 seed must be %d bytes", ed25519.SeedSize)
	}
	return Identity{ID: []byte(id), privateKey: ed25519.NewKeyFromSeed(seed)}, nil
}

// Seed returns the 32-byte Ed25519 seed underlying the identity, suitable for
// persistence with NewIdentityFromSeed.
func (a Identity) Seed() []byte {
	return a.privateKey[:ed25519.SeedSize]
}

// PublicKey returns the 32-byte Ed25519 long-term public key.
func (a Identity) PublicKey() []byte {
	return []byte(a.privateKey.Public().(ed25519.PublicKey))
}

// Sign signs data with the accessory long-term key.
func (a Identity) Sign(data []byte) []byte {
	return ed25519.Sign(a.privateKey, data)
}

// verifySignature reports whether sig is a valid Ed25519 signature for data
// under the 32-byte public key.
func verifySignature(pub, data, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), data, sig)
}

// x25519KeyPair generates an ephemeral Curve25519 key pair.
func x25519KeyPair(rand io.Reader) (*ecdh.PrivateKey, []byte, error) {
	if rand == nil {
		rand = cryptoRand{}
	}
	priv, err := ecdh.X25519().GenerateKey(rand)
	if err != nil {
		return nil, nil, err
	}
	return priv, priv.PublicKey().Bytes(), nil
}

// x25519Shared computes the X25519 shared secret from a private key and the
// peer's 32-byte public key.
func x25519Shared(priv *ecdh.PrivateKey, peerPub []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(peerPub)
	if err != nil {
		return nil, err
	}
	return priv.ECDH(pub)
}
