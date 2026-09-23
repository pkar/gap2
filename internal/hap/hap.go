// Package hap implements the HomeKit Accessory Protocol pairing and
// session-security primitives used by AirPlay 2 receivers.
//
// It is dependency-free and CGO-free: all cryptography is built from the Go
// standard library plus the small ChaCha20-Poly1305 construction in this
// package. The implementation follows the HAP non-commercial specification
// pairing flows and has been cross-checked against the RFC 8439 ChaCha20 and
// Poly1305 test vectors and the published 3072-bit SRP test values.
package hap

import (
	"errors"
	"fmt"

	"github.com/pkar/gap2/internal/tlv8"
)

// TLV type tags used by pairing requests and responses.
const (
	TLVMethod        uint8 = 0
	TLVIdentifier    uint8 = 1
	TLVSalt          uint8 = 2
	TLVPublicKey     uint8 = 3
	TLVProof         uint8 = 4
	TLVEncryptedData uint8 = 5
	TLVState         uint8 = 6
	TLVError         uint8 = 7
	TLVRetryDelay    uint8 = 8
	TLVCertificate   uint8 = 9
	TLVSignature     uint8 = 10
	TLVPermissions   uint8 = 11
	TLVFragmentData  uint8 = 12
	TLVFragmentLast  uint8 = 13
	TLVFlags         uint8 = 19
	TLVSeparator     uint8 = 255
)

// Pairing methods carried in TLVMethod.
const (
	MethodPairSetup     uint8 = 0
	MethodPairSetupMFi  uint8 = 1
	MethodPairVerify    uint8 = 2
	MethodAddPairing    uint8 = 3
	MethodRemovePairing uint8 = 4
	MethodListPairings  uint8 = 5
)

// Pairing state values carried in TLVState.
const (
	StateM1 uint8 = 1
	StateM2 uint8 = 2
	StateM3 uint8 = 3
	StateM4 uint8 = 4
	StateM5 uint8 = 5
	StateM6 uint8 = 6
)

// Pairing error codes carried in TLVError.
const (
	ErrorUnknown        uint8 = 1
	ErrorAuthentication uint8 = 2
	ErrorBackoff        uint8 = 3
	ErrorMaxPeers       uint8 = 4
	ErrorMaxTries       uint8 = 5
	ErrorUnavailable    uint8 = 6
	ErrorBusy           uint8 = 7
)

// Pairing flags carried in TLVFlags.
const (
	// FlagTransient requests a transient (non-persistent) pairing.
	FlagTransient uint32 = 1 << 4
	// FlagSplit requests split pairing. Not supported by this package.
	FlagSplit uint32 = 1 << 24
)

// Controller permissions carried in TLVPermissions.
const (
	PermissionUser  uint8 = 0
	PermissionAdmin uint8 = 1
)

// Pairing is one saved controller pairing.
type Pairing struct {
	// Identifier is the controller's pairing identifier.
	Identifier string
	// LTPK is the controller's 32-byte Ed25519 long-term public key.
	LTPK []byte
	// Admin reports whether the controller has administrative rights.
	Admin bool
}

// Store persists controller long-term public keys.
//
// Implementations must serialize calls: the pairing state machines may invoke
// Store from a single server goroutine, but a Store is shared across all
// connections to a receiver.
type Store interface {
	// Get returns the long-term public key for identifier.
	Get(identifier string) (ltpk []byte, ok bool, err error)
	// Put saves or replaces a controller pairing.
	Put(identifier string, ltpk []byte, admin bool) error
	// Delete removes a controller pairing. Deleting a missing pairing is
	// not an error.
	Delete(identifier string) error
	// List returns all saved pairings.
	List() ([]Pairing, error)
}

// Sentinel errors returned by this package.
var (
	// ErrInvalidTLV reports a malformed or incomplete pairing message.
	ErrInvalidTLV = errors.New("hap: invalid pairing TLV")
	// ErrAuthentication reports a failed proof, signature, or AEAD tag check.
	ErrAuthentication = errors.New("hap: authentication failed")
	// ErrNotPaired reports that a controller is not paired.
	ErrNotPaired = errors.New("hap: controller not paired")
	// ErrState reports an out-of-order pairing message.
	ErrState = errors.New("hap: unexpected pairing state")
)

// getItem returns the value for the first item with type t.
func getItem(items []tlv8.Item, t uint8) ([]byte, bool) {
	for _, it := range items {
		if it.Type == t {
			return it.Value, true
		}
	}
	return nil, false
}

// requireItem returns the value for t or ErrInvalidTLV when absent.
func requireItem(items []tlv8.Item, t uint8) ([]byte, error) {
	if v, ok := getItem(items, t); ok {
		return v, nil
	}
	return nil, fmt.Errorf("%w: missing type %d", ErrInvalidTLV, t)
}

// encodeTLV is a small convenience wrapper around tlv8.Encode.
func encodeTLV(items []tlv8.Item) ([]byte, error) {
	return tlv8.Encode(items)
}
