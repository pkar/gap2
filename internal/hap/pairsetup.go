package hap

import (
	"fmt"
	"io"

	"github.com/pkar/gap2/internal/tlv8"
)

// HKDF salts and info strings for Pair Setup.
const (
	pairSetupEncryptSalt = "Pair-Setup-Encrypt-Salt"
	pairSetupEncryptInfo = "Pair-Setup-Encrypt-Info"

	pairSetupControllerSignSalt = "Pair-Setup-Controller-Sign-Salt"
	pairSetupControllerSignInfo = "Pair-Setup-Controller-Sign-Info"

	pairSetupAccessorySignSalt = "Pair-Setup-Accessory-Sign-Salt"
	pairSetupAccessorySignInfo = "Pair-Setup-Accessory-Sign-Info"
)

// PairSetupResult is the outcome of one Pair Setup message exchange.
type PairSetupResult struct {
	// Response is the TLV payload to return to the controller. It is always
	// non-nil when Handle returns a nil error.
	Response []byte
	// Done reports that the full exchange completed.
	Done bool
	// SessionKey is the shared control-channel key when a transient pairing
	// completed. It is nil for persistent pairings, which must be followed by
	// Pair Verify.
	SessionKey []byte
	// Saved is the persisted pairing when a persistent pairing completed.
	Saved *Pairing
}

// PairSetupSession drives the accessory side of HAP Pair Setup (M1..M6).
type PairSetupSession struct {
	identity Identity
	pin      string
	store    Store
	rand     io.Reader

	srp       *srpServer
	transient bool
}

// NewPairSetupSession starts a Pair Setup exchange. pin is the numeric setup
// code, e.g. "3939". rand may be nil to use the crypto/rand source.
func NewPairSetupSession(rand io.Reader, id Identity, pin string, store Store) *PairSetupSession {
	return &PairSetupSession{identity: id, pin: pin, store: store, rand: rand}
}

// Handle processes one controller Pair Setup message and returns the
// accessory response.
func (s *PairSetupSession) Handle(req []byte) (PairSetupResult, error) {
	items, err := tlv8.Decode(req)
	if err != nil {
		return PairSetupResult{}, fmt.Errorf("%w: %v", ErrInvalidTLV, err)
	}
	state, ok := getItem(items, TLVState)
	if !ok || len(state) != 1 {
		return PairSetupResult{}, fmt.Errorf("%w: missing state", ErrInvalidTLV)
	}
	switch state[0] {
	case StateM1:
		return s.handleM1(items)
	case StateM3:
		return s.handleM3(items)
	case StateM5:
		return s.handleM5(items)
	default:
		return PairSetupResult{}, fmt.Errorf("%w: state %d", ErrState, state[0])
	}
}

func (s *PairSetupSession) handleM1(items []tlv8.Item) (PairSetupResult, error) {
	if method, ok := getItem(items, TLVMethod); ok {
		if len(method) != 1 || method[0] != MethodPairSetup {
			return PairSetupResult{}, fmt.Errorf("%w: unsupported method", ErrInvalidTLV)
		}
	}
	if flags, ok := getItem(items, TLVFlags); ok {
		// HAP flags are a variable-width (0..4 byte) little-endian integer.
		// Apple senders commonly encode transient pairing in just one byte.
		if len(flags) > 4 {
			return PairSetupResult{}, fmt.Errorf("%w: malformed flags", ErrInvalidTLV)
		}
		var f uint32
		for i, b := range flags {
			f |= uint32(b) << (8 * i)
		}
		if f&FlagSplit != 0 {
			return PairSetupResult{}, fmt.Errorf("%w: split pairing unsupported", ErrInvalidTLV)
		}
		s.transient = f&FlagTransient != 0
	}

	srp, err := newSRPServer(s.rand, usernamePairSetup, s.pin)
	if err != nil {
		return PairSetupResult{}, err
	}
	s.srp = srp

	resp, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM2}},
		{Type: TLVSalt, Value: srp.saltBytes()},
		{Type: TLVPublicKey, Value: srp.publicBytes()},
	})
	if err != nil {
		return PairSetupResult{}, err
	}
	return PairSetupResult{Response: resp}, nil
}

func (s *PairSetupSession) handleM3(items []tlv8.Item) (PairSetupResult, error) {
	if s.srp == nil {
		return PairSetupResult{}, fmt.Errorf("%w: no M1", ErrState)
	}
	public, err := requireItem(items, TLVPublicKey)
	if err != nil {
		return PairSetupResult{}, err
	}
	proof, err := requireItem(items, TLVProof)
	if err != nil {
		return PairSetupResult{}, err
	}

	if err := s.srp.setClientPublic(public); err != nil {
		return PairSetupResult{}, err
	}
	if !s.srp.verifyProof(proof) {
		resp, _ := encodeTLV([]tlv8.Item{
			{Type: TLVState, Value: []byte{StateM4}},
			{Type: TLVError, Value: []byte{ErrorAuthentication}},
		})
		return PairSetupResult{Response: resp}, fmt.Errorf("%w: bad SRP proof", ErrAuthentication)
	}

	resp, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM4}},
		{Type: TLVProof, Value: s.srp.proofBytes()},
	})
	if err != nil {
		return PairSetupResult{}, err
	}
	if s.transient {
		// Transient Pair Setup ends at M4: no accessory/controller identity
		// exchange (M5/M6) follows. The next request uses HAP encryption.
		return PairSetupResult{
			Response:   resp,
			Done:       true,
			SessionKey: s.srp.sessionKeyBytes(),
		}, nil
	}
	return PairSetupResult{Response: resp}, nil
}

func (s *PairSetupSession) handleM5(items []tlv8.Item) (PairSetupResult, error) {
	if s.srp == nil {
		return PairSetupResult{}, fmt.Errorf("%w: no M1", ErrState)
	}
	encrypted, err := requireItem(items, TLVEncryptedData)
	if err != nil {
		return PairSetupResult{}, err
	}

	ikm := s.srp.sessionKeyBytes()
	encKey := hkdfSHA512(ikm, []byte(pairSetupEncryptSalt), []byte(pairSetupEncryptInfo), 32)

	plain, err := aeadOpen(encKey, padNonce8([]byte("PS-Msg05")), encrypted, nil)
	if err != nil {
		return s.m5AuthError(), nil
	}
	sub, err := tlv8.Decode(plain)
	if err != nil {
		return s.m5AuthError(), nil
	}
	controllerID, err := requireItem(sub, TLVIdentifier)
	if err != nil {
		return s.m5AuthError(), nil
	}
	controllerLTPK, err := requireItem(sub, TLVPublicKey)
	if err != nil {
		return s.m5AuthError(), nil
	}
	signature, err := requireItem(sub, TLVSignature)
	if err != nil {
		return s.m5AuthError(), nil
	}

	// Verify the controller signature over
	// HKDF(K, "Pair-Setup-Controller-Sign-*") || ID || LTPK.
	controllerX := hkdfSHA512(ikm, []byte(pairSetupControllerSignSalt), []byte(pairSetupControllerSignInfo), 32)
	signed := make([]byte, 0, len(controllerX)+len(controllerID)+len(controllerLTPK))
	signed = append(signed, controllerX...)
	signed = append(signed, controllerID...)
	signed = append(signed, controllerLTPK...)
	if !verifySignature(controllerLTPK, signed, signature) {
		return s.m5AuthError(), nil
	}

	var saved *Pairing
	if !s.transient {
		p := &Pairing{Identifier: string(controllerID), LTPK: append([]byte(nil), controllerLTPK...), Admin: true}
		if err := s.store.Put(p.Identifier, p.LTPK, p.Admin); err != nil {
			return PairSetupResult{}, fmt.Errorf("hap: persisting pairing: %w", err)
		}
		saved = p
	}

	// Build the accessory M6 payload: ID, LTPK, and a signature over
	// HKDF(K, "Pair-Setup-Accessory-Sign-*") || ID || LTPK.
	accessoryX := hkdfSHA512(ikm, []byte(pairSetupAccessorySignSalt), []byte(pairSetupAccessorySignInfo), 32)
	accessoryLTPK := s.identity.PublicKey()
	signed = signed[:0]
	signed = append(signed, accessoryX...)
	signed = append(signed, s.identity.ID...)
	signed = append(signed, accessoryLTPK...)
	accessorySig := s.identity.Sign(signed)

	subPlain, err := encodeTLV([]tlv8.Item{
		{Type: TLVIdentifier, Value: s.identity.ID},
		{Type: TLVPublicKey, Value: accessoryLTPK},
		{Type: TLVSignature, Value: accessorySig},
	})
	if err != nil {
		return PairSetupResult{}, err
	}
	sealed := aeadSeal(encKey, padNonce8([]byte("PS-Msg06")), subPlain, nil)

	resp, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM6}},
		{Type: TLVEncryptedData, Value: sealed},
	})
	if err != nil {
		return PairSetupResult{}, err
	}

	res := PairSetupResult{Response: resp, Done: true, Saved: saved}
	if s.transient {
		res.SessionKey = append([]byte(nil), ikm...)
	}
	return res, nil
}

func (s *PairSetupSession) m5AuthError() PairSetupResult {
	resp, _ := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM6}},
		{Type: TLVError, Value: []byte{ErrorAuthentication}},
	})
	return PairSetupResult{Response: resp}
}
