package hap

import (
	"fmt"
	"io"

	"github.com/pkar/gap2/internal/tlv8"
)

// HKDF salt and info for Pair Verify session encryption.
const (
	pairVerifyEncryptSalt = "Pair-Verify-Encrypt-Salt"
	pairVerifyEncryptInfo = "Pair-Verify-Encrypt-Info"
)

// PairVerifyResult is the outcome of one Pair Verify message exchange.
type PairVerifyResult struct {
	// Response is the TLV payload to return to the controller.
	Response []byte
	// Done reports that verification completed and the control channel may be
	// upgraded.
	Done bool
	// SessionKey is the shared secret feeding control-channel key derivation.
	SessionKey []byte
	// ClientID is the verified controller pairing identifier.
	ClientID string
}

// PairVerifySession drives the accessory side of HAP Pair Verify (M1..M4).
type PairVerifySession struct {
	identity Identity
	store    Store
	rand     io.Reader

	clientPub []byte
	serverPub []byte
	shared    []byte
	session   []byte
}

// NewPairVerifySession starts a Pair Verify exchange. rand may be nil to use
// the crypto/rand source.
func NewPairVerifySession(rand io.Reader, id Identity, store Store) *PairVerifySession {
	return &PairVerifySession{identity: id, store: store, rand: rand}
}

// Handle processes one controller Pair Verify message and returns the
// accessory response.
func (s *PairVerifySession) Handle(req []byte) (PairVerifyResult, error) {
	items, err := tlv8.Decode(req)
	if err != nil {
		return PairVerifyResult{}, fmt.Errorf("%w: %v", ErrInvalidTLV, err)
	}
	state, ok := getItem(items, TLVState)
	if !ok || len(state) != 1 {
		return PairVerifyResult{}, fmt.Errorf("%w: missing state", ErrInvalidTLV)
	}
	switch state[0] {
	case StateM1:
		return s.handleM1(items)
	case StateM3:
		return s.handleM3(items)
	default:
		return PairVerifyResult{}, fmt.Errorf("%w: state %d", ErrState, state[0])
	}
}

func (s *PairVerifySession) handleM1(items []tlv8.Item) (PairVerifyResult, error) {
	clientPub, err := requireItem(items, TLVPublicKey)
	if err != nil {
		return PairVerifyResult{}, err
	}
	if len(clientPub) != 32 {
		return PairVerifyResult{}, fmt.Errorf("%w: bad curve25519 public key", ErrInvalidTLV)
	}

	priv, serverPub, err := x25519KeyPair(s.rand)
	if err != nil {
		return PairVerifyResult{}, err
	}
	shared, err := x25519Shared(priv, clientPub)
	if err != nil {
		return PairVerifyResult{}, fmt.Errorf("%w: bad curve25519 public key", ErrInvalidTLV)
	}
	session := hkdfSHA512(shared, []byte(pairVerifyEncryptSalt), []byte(pairVerifyEncryptInfo), 32)

	s.clientPub = append([]byte(nil), clientPub...)
	s.serverPub = serverPub
	s.shared = shared
	s.session = session

	// AccessoryInfo = accessory curve25519 pub || accessory ID || client pub.
	signed := make([]byte, 0, len(serverPub)+len(s.identity.ID)+len(clientPub))
	signed = append(signed, serverPub...)
	signed = append(signed, s.identity.ID...)
	signed = append(signed, clientPub...)
	sig := s.identity.Sign(signed)

	subPlain, err := encodeTLV([]tlv8.Item{
		{Type: TLVIdentifier, Value: s.identity.ID},
		{Type: TLVSignature, Value: sig},
	})
	if err != nil {
		return PairVerifyResult{}, err
	}
	sealed := aeadSeal(session, padNonce8([]byte("PV-Msg02")), subPlain, nil)

	resp, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM2}},
		{Type: TLVPublicKey, Value: serverPub},
		{Type: TLVEncryptedData, Value: sealed},
	})
	if err != nil {
		return PairVerifyResult{}, err
	}
	return PairVerifyResult{Response: resp}, nil
}

func (s *PairVerifySession) handleM3(items []tlv8.Item) (PairVerifyResult, error) {
	if s.session == nil {
		return PairVerifyResult{}, fmt.Errorf("%w: no M1", ErrState)
	}
	encrypted, err := requireItem(items, TLVEncryptedData)
	if err != nil {
		return PairVerifyResult{}, err
	}

	plain, err := aeadOpen(s.session, padNonce8([]byte("PV-Msg03")), encrypted, nil)
	if err != nil {
		return s.m3AuthError(), nil
	}
	sub, err := tlv8.Decode(plain)
	if err != nil {
		return s.m3AuthError(), nil
	}
	clientID, err := requireItem(sub, TLVIdentifier)
	if err != nil {
		return s.m3AuthError(), nil
	}
	clientSig, err := requireItem(sub, TLVSignature)
	if err != nil {
		return s.m3AuthError(), nil
	}

	ltpk, ok, err := s.store.Get(string(clientID))
	if err != nil {
		return PairVerifyResult{}, fmt.Errorf("hap: pairing store: %w", err)
	}
	if !ok {
		return s.m3AuthError(), nil
	}

	// ControllerInfo = client curve25519 pub || client ID || accessory pub.
	signed := make([]byte, 0, len(s.clientPub)+len(clientID)+len(s.serverPub))
	signed = append(signed, s.clientPub...)
	signed = append(signed, clientID...)
	signed = append(signed, s.serverPub...)
	if !verifySignature(ltpk, signed, clientSig) {
		return s.m3AuthError(), nil
	}

	resp, err := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM4}},
	})
	if err != nil {
		return PairVerifyResult{}, err
	}
	return PairVerifyResult{
		Response:   resp,
		Done:       true,
		SessionKey: append([]byte(nil), s.shared...),
		ClientID:   string(clientID),
	}, nil
}

func (s *PairVerifySession) m3AuthError() PairVerifyResult {
	resp, _ := encodeTLV([]tlv8.Item{
		{Type: TLVState, Value: []byte{StateM4}},
		{Type: TLVError, Value: []byte{ErrorAuthentication}},
	})
	return PairVerifyResult{Response: resp}
}
