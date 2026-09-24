package airplay2

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pkar/gap2/internal/hap"
)

func TestPairingStoreMemoryRoundTrip(t *testing.T) {
	s, err := loadPairingStore("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get("c1"); err != nil || ok {
		t.Fatalf("Get before Put = ok %v, err %v", ok, err)
	}
	ltpk := make([]byte, 32)
	for i := range ltpk {
		ltpk[i] = byte(i)
	}
	if err := s.Put("c1", ltpk, true); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("c1")
	if err != nil || !ok {
		t.Fatalf("Get after Put = ok %v, err %v", ok, err)
	}
	if string(got) != string(ltpk) {
		t.Fatalf("Get = %x, want %x", got, ltpk)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || !list[0].Admin {
		t.Fatalf("List = %+v, err %v", list, err)
	}
	if err := s.Delete("c1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("c1"); ok {
		t.Fatal("Get after Delete still found pairing")
	}
}

func TestPairingStorePersistsIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pairings.json")

	s1, err := loadPairingStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id1, err := s1.ensureIdentity(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(id1.ID) == "" {
		t.Fatal("empty pairing identifier")
	}
	if got := s1.deviceID(); len(got) != 17 {
		t.Fatalf("deviceID = %q, want MAC-style 17 chars", got)
	}
	if err := s1.Put("c1", []byte("ltpk"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pairings file not written: %v", err)
	}

	s2, err := loadPairingStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s2.ensureIdentity(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(id1.ID) != string(id2.ID) || string(id1.PublicKey()) != string(id2.PublicKey()) {
		t.Fatal("identity changed across reload")
	}
	if _, ok, err := s2.Get("c1"); err != nil || !ok {
		t.Fatalf("pairing not restored: ok=%v err=%v", ok, err)
	}
}

func TestAirPlayPairingID(t *testing.T) {
	const stored = "00112233445566778899aabbccddeeff"
	const want = "00112233-4455-4677-8899-aabbccddeeff"
	if got := airplayPairingID(stored); got != want {
		t.Fatalf("airplayPairingID = %q, want %q", got, want)
	}
	if got := macStyleID(stored); got != "00:11:22:33:44:55" {
		t.Fatalf("device ID changed: %q", got)
	}
}

func TestMacStyleID(t *testing.T) {
	if got := macStyleID("00112233445566778899AABBCCDDEEFF"); got != "00:11:22:33:44:55" {
		t.Fatalf("macStyleID = %q", got)
	}
}

var _ hap.Store = (*pairingStore)(nil)
