package hap

import (
	"bytes"
	"io"
	"net"
	"testing"
)

// newControllerConn mirrors NewConn for the controller side of the channel:
// its outgoing key is the accessory's incoming key and vice versa.
func newControllerConn(c net.Conn, shared []byte) *Conn {
	return &Conn{
		c:      c,
		outKey: hkdfSHA512(shared, []byte(controlSalt), []byte(controlWriteInfo), 32),
		inKey:  hkdfSHA512(shared, []byte(controlSalt), []byte(controlReadInfo), 32),
	}
}

// TestRecordWireVector checks the exact on-wire bytes for one record with a
// known key and zero counter. The expected ciphertext was produced by an
// independent ChaCha20-Poly1305 implementation.
func TestRecordWireVector(t *testing.T) {
	recKey := mustHexBytes(t, "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	want := mustHexBytes(t, "05008d54eeacb4ac635b8f3bf166e782ed85577a6aa39f")

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	c := &Conn{c: server, outKey: recKey, inKey: recKey}
	go func() {
		if _, err := c.Write([]byte("hello")); err != nil {
			t.Errorf("write: %v", err)
		}
	}()

	got := make([]byte, len(want))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire mismatch\n got %x\nwant %x", got, want)
	}
}

// TestConnRoundTrip exercises encryption in both directions, multiple records
// (to advance the counters), and reads smaller than a record.
func TestConnRoundTrip(t *testing.T) {
	shared := mustHexBytes(t, "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00")
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	accessory := NewConn(server, shared)
	controller := newControllerConn(client, shared)

	// Accessory -> controller.
	msg := bytes.Repeat([]byte("A"), 3000) // spans multiple records
	go func() {
		if _, err := accessory.Write(msg); err != nil {
			t.Errorf("accessory write: %v", err)
		}
	}()
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(controller, got); err != nil {
		t.Fatalf("controller read: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatal("controller received corrupted plaintext")
	}

	// Controller -> accessory with a partial first read.
	reply := []byte("encrypted control reply")
	go func() {
		if _, err := controller.Write(reply); err != nil {
			t.Errorf("controller write: %v", err)
		}
	}()
	buf := make([]byte, len(reply))
	if _, err := io.ReadFull(accessory, buf); err != nil {
		t.Fatalf("accessory read: %v", err)
	}
	if !bytes.Equal(buf, reply) {
		t.Fatal("accessory received corrupted plaintext")
	}
}

// TestConnTamper detects modification of an encrypted record.
func TestConnTamper(t *testing.T) {
	recKey := mustHexBytes(t, "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	c := &Conn{c: server, outKey: recKey, inKey: recKey}
	go func() { _, _ = c.Write([]byte("hello")) }()

	raw := make([]byte, 2+5+16)
	if _, err := io.ReadFull(client, raw); err != nil {
		t.Fatalf("read: %v", err)
	}
	raw[len(raw)-1] ^= 1 // flip a tag byte

	go func() { _, _ = client.Write(raw) }()
	if _, err := c.Read(make([]byte, 8)); err == nil {
		t.Fatal("expected authentication failure for tampered record")
	}
}

func TestRecordNonce(t *testing.T) {
	want := mustHexBytes(t, "000000000100000000000000")
	got := recordNonce(1)
	if !bytes.Equal(got, want) {
		t.Fatalf("record nonce mismatch: got %x want %x", got, want)
	}
	if len(recordNonce(0)) != 12 {
		t.Fatal("record nonce must be 12 bytes")
	}
}
