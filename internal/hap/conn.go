package hap

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// Control-channel key derivation constants. The accessory's outgoing key is
// the controller's read key, and vice versa.
const (
	controlSalt      = "Control-Salt"
	controlReadInfo  = "Control-Read-Encryption-Key"
	controlWriteInfo = "Control-Write-Encryption-Key"
)

// HAP encrypted-record framing limits.
const (
	maxRecordLen = 0x400
	tagLen       = 16
)

// Conn implements net.Conn over the HAP encrypted transport. Plaintext is
// split into records of at most 1024 bytes; each record is
//
//	uint16 LE plaintext length || ciphertext || 16-byte tag
//
// with the plaintext length as additional authenticated data and a 12-byte
// nonce of four zero bytes followed by a little-endian 64-bit counter.
type Conn struct {
	c net.Conn

	inKey  []byte
	outKey []byte

	inCounter  uint64
	outCounter uint64

	readBuf []byte
	readErr error
}

// NewConn wraps c as an accessory-side encrypted HAP connection. sharedKey is
// the Pair Verify X25519 shared secret or the transient Pair Setup SRP session
// key.
func NewConn(c net.Conn, sharedKey []byte) *Conn {
	return &Conn{
		c:      c,
		outKey: hkdfSHA512(sharedKey, []byte(controlSalt), []byte(controlReadInfo), 32),
		inKey:  hkdfSHA512(sharedKey, []byte(controlSalt), []byte(controlWriteInfo), 32),
	}
}

// Read reads and decrypts up to len(p) bytes of plaintext.
func (c *Conn) Read(p []byte) (int, error) {
	for len(c.readBuf) == 0 && c.readErr == nil {
		c.readErr = c.readRecord()
	}
	if len(c.readBuf) > 0 {
		n := copy(p, c.readBuf)
		c.readBuf = c.readBuf[n:]
		return n, nil
	}
	return 0, c.readErr
}

func (c *Conn) readRecord() error {
	var lenBuf [2]byte
	if _, err := io.ReadFull(c.c, lenBuf[:]); err != nil {
		return err
	}
	n := binary.LittleEndian.Uint16(lenBuf[:])
	if n > maxRecordLen {
		return fmt.Errorf("hap: encrypted record too large: %d", n)
	}
	payload := make([]byte, int(n)+tagLen)
	if _, err := io.ReadFull(c.c, payload); err != nil {
		return err
	}
	plain, err := aeadOpen(c.inKey, recordNonce(c.inCounter), payload, lenBuf[:])
	if err != nil {
		return err
	}
	c.inCounter++
	c.readBuf = plain
	return nil
}

// Write encrypts and writes p, splitting it into HAP records.
func (c *Conn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := len(p)
		if n > maxRecordLen {
			n = maxRecordLen
		}
		if err := c.writeRecord(p[:n]); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (c *Conn) writeRecord(plain []byte) error {
	var lenBuf [2]byte
	binary.LittleEndian.PutUint16(lenBuf[:], uint16(len(plain)))
	sealed := aeadSeal(c.outKey, recordNonce(c.outCounter), plain, lenBuf[:])
	c.outCounter++
	return writeFull(c.c, append(lenBuf[:], sealed...))
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// recordNonce builds the 12-byte record nonce: four zero bytes plus the
// little-endian 64-bit record counter.
func recordNonce(counter uint64) []byte {
	n := make([]byte, 12)
	binary.LittleEndian.PutUint64(n[4:], counter)
	return n
}

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.c.Close() }

// LocalAddr returns the local network address.
func (c *Conn) LocalAddr() net.Addr { return c.c.LocalAddr() }

// RemoteAddr returns the remote network address.
func (c *Conn) RemoteAddr() net.Addr { return c.c.RemoteAddr() }

// SetDeadline sets the read and write deadlines.
func (c *Conn) SetDeadline(t time.Time) error { return c.c.SetDeadline(t) }

// SetReadDeadline sets the read deadline.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.c.SetReadDeadline(t) }

// SetWriteDeadline sets the write deadline.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.c.SetWriteDeadline(t) }
