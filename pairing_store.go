package airplay2

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/pkar/gap2/internal/hap"
)

// persistedPairing is the on-disk form of one controller pairing.
type persistedPairing struct {
	Identifier string `json:"identifier"`
	LTPK       string `json:"ltpk"` // base64 standard encoding
	Admin      bool   `json:"admin"`
}

// pairingData is the persistent receiver state: the accessory long-term
// identity and the controller long-term public keys.
type pairingData struct {
	PairingID string             `json:"pairing_id"`
	Seed      string             `json:"ed25519_seed"` // base64 standard encoding
	Pairings  []persistedPairing `json:"pairings"`
}

// pairingStore implements hap.Store. It is safe for concurrent use and, when
// a non-empty path is configured, persists every mutation atomically.
type pairingStore struct {
	mu   sync.Mutex
	path string
	data pairingData
}

// loadPairingStore opens the store at path. An empty path yields a purely
// in-memory store; a missing file is treated as an empty store that will be
// created on the first write.
func loadPairingStore(path string) (*pairingStore, error) {
	s := &pairingStore{path: path}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("airplay2: read pairings: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("airplay2: parse pairings: %w", err)
	}
	return s, nil
}

// ensureIdentity returns the persistent accessory identity, generating and
// persisting one on first use. rand may be nil to use crypto/rand.
func (s *pairingStore) ensureIdentity(rand io.Reader) (hap.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.PairingID == "" || s.data.Seed == "" {
		if rand == nil {
			rand = cryptoRandReader{}
		}
		idBytes := make([]byte, 16)
		if _, err := io.ReadFull(rand, idBytes); err != nil {
			return hap.Identity{}, err
		}
		seed := make([]byte, ed25519.SeedSize)
		if _, err := io.ReadFull(rand, seed); err != nil {
			return hap.Identity{}, err
		}
		s.data.PairingID = hex.EncodeToString(idBytes)
		s.data.Seed = base64.StdEncoding.EncodeToString(seed)
		if err := s.saveLocked(); err != nil {
			return hap.Identity{}, err
		}
	}

	seed, err := base64.StdEncoding.DecodeString(s.data.Seed)
	if err != nil {
		return hap.Identity{}, fmt.Errorf("airplay2: decode identity seed: %w", err)
	}
	return hap.NewIdentityFromSeed(s.data.PairingID, seed)
}

// deviceID renders the pairing identifier as a MAC-style identifier, using the
// leading bytes of the stored pairing identifier.
func (s *pairingStore) deviceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return macStyleID(s.data.PairingID)
}

// macStyleID formats at most six bytes of a hex string as AA:BB:CC:DD:EE:FF.
func macStyleID(hexID string) string {
	if len(hexID) < 12 {
		return hexID
	}
	out := make([]byte, 0, 17)
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hexID[i], hexID[i+1])
	}
	return string(out)
}

func (s *pairingStore) Get(identifier string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.data.Pairings {
		if p.Identifier == identifier {
			ltpk, err := base64.StdEncoding.DecodeString(p.LTPK)
			if err != nil {
				return nil, false, fmt.Errorf("airplay2: decode pairing key: %w", err)
			}
			return ltpk, true, nil
		}
	}
	return nil, false, nil
}

func (s *pairingStore) Put(identifier string, ltpk []byte, admin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	enc := base64.StdEncoding.EncodeToString(ltpk)
	for i := range s.data.Pairings {
		if s.data.Pairings[i].Identifier == identifier {
			s.data.Pairings[i].LTPK = enc
			s.data.Pairings[i].Admin = admin
			return s.saveLocked()
		}
	}
	s.data.Pairings = append(s.data.Pairings, persistedPairing{
		Identifier: identifier,
		LTPK:       enc,
		Admin:      admin,
	})
	return s.saveLocked()
}

func (s *pairingStore) Delete(identifier string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Pairings {
		if s.data.Pairings[i].Identifier == identifier {
			s.data.Pairings = append(s.data.Pairings[:i], s.data.Pairings[i+1:]...)
			return s.saveLocked()
		}
	}
	return nil
}

func (s *pairingStore) List() ([]hap.Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hap.Pairing, 0, len(s.data.Pairings))
	for _, p := range s.data.Pairings {
		ltpk, err := base64.StdEncoding.DecodeString(p.LTPK)
		if err != nil {
			return nil, fmt.Errorf("airplay2: decode pairing key: %w", err)
		}
		out = append(out, hap.Pairing{Identifier: p.Identifier, LTPK: ltpk, Admin: p.Admin})
	}
	return out, nil
}

func (s *pairingStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("airplay2: encode pairings: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("airplay2: create pairings dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pairings-*")
	if err != nil {
		return fmt.Errorf("airplay2: create pairings temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("airplay2: chmod pairings: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("airplay2: write pairings: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("airplay2: sync pairings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("airplay2: close pairings: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("airplay2: replace pairings: %w", err)
	}
	return nil
}

// cryptoRandReader adapts crypto/rand.Reader to io.Reader.
type cryptoRandReader struct{}

func (cryptoRandReader) Read(p []byte) (int, error) { return rand.Read(p) }
