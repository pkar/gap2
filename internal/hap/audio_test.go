package hap

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestAudioDecryptorIndependentVector(t *testing.T) {
	// Generated with cryptography's ChaCha20Poly1305, independently of hap.
	packet, _ := hex.DecodeString("806012340102030405060708cfe6b7ccf3f43220f32dc17cdeafdc471ada121a59452b910102030405060708")
	want, _ := hex.DecodeString("8060123401020304050607082000123456789abc")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	d, err := NewAudioDecryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Open(packet)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("Open = %x, %v", got, err)
	}
	// TCP's sequence counter consumes the RTP payload-type byte. Only
	// timestamp and format word are AAD, so changing this byte preserves
	// this independently generated authentication tag.
	buffered := bytes.Clone(packet)
	buffered[1] = 0xfe
	got, err = d.OpenBuffered(buffered)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("OpenBuffered = %x, %v", got, err)
	}
	for _, offset := range []int{4, 8, 12, 20, len(packet) - 1} {
		bad := bytes.Clone(packet)
		bad[offset] ^= 1
		if _, err := d.Open(bad); err == nil {
			t.Fatalf("accepted tampering at byte %d", offset)
		}
	}
	for n := 0; n < 36; n++ {
		if _, err := d.Open(packet[:n]); err == nil {
			t.Fatalf("accepted short packet: %d", n)
		}
	}
	if _, err := NewAudioDecryptor(key[:31]); err == nil {
		t.Fatal("accepted short key")
	}
}
