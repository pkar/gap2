package aac

import "errors"

// Errors returned by BitReader.
var (
	ErrShortRead = errors.New("aac: not enough bits")
	ErrBitRange  = errors.New("aac: bit count out of range")
)

// BitReader reads bits from an AAC payload most-significant-bit first, which
// is the bit order used by the MPEG-4 audio bitstream syntax.
type BitReader struct {
	data []byte
	pos  int // bit position from the start of data
}

// NewBitReader returns a BitReader over data.
func NewBitReader(data []byte) *BitReader {
	return &BitReader{data: data}
}

// Remaining reports the number of unread bits.
func (r *BitReader) Remaining() int {
	return len(r.data)*8 - r.pos
}

// Read returns the next n bits as an unsigned value. n may be 0..32.
func (r *BitReader) Read(n int) (uint32, error) {
	if n < 0 || n > 32 {
		return 0, ErrBitRange
	}
	if n == 0 {
		return 0, nil
	}
	if r.Remaining() < n {
		return 0, ErrShortRead
	}
	var v uint32
	for i := 0; i < n; i++ {
		bit := (r.data[r.pos>>3] >> (7 - uint(r.pos&7))) & 1
		v = v<<1 | uint32(bit)
		r.pos++
	}
	return v, nil
}

// ReadBit returns the next single bit.
func (r *BitReader) ReadBit() (bool, error) {
	v, err := r.Read(1)
	return v != 0, err
}

// Skip advances n bits without returning them.
func (r *BitReader) Skip(n int) error {
	if n < 0 {
		return ErrBitRange
	}
	if r.Remaining() < n {
		return ErrShortRead
	}
	r.pos += n
	return nil
}

// Align discards the remaining bits in the current byte.
func (r *BitReader) Align() {
	r.pos = (r.pos + 7) &^ 7
}

// Position reports the current byte offset and bit offset within that byte.
func (r *BitReader) Position() (byte int, bit int) {
	return r.pos >> 3, r.pos & 7
}
