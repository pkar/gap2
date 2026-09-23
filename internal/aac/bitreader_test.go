package aac

import (
	"errors"
	"testing"
)

func TestBitReaderRead(t *testing.T) {
	// 0xA5 = 1010 0101, 0x3C = 0011 1100
	r := NewBitReader([]byte{0xa5, 0x3c})
	if r.Remaining() != 16 {
		t.Fatalf("remaining = %d, want 16", r.Remaining())
	}
	if v, err := r.Read(4); err != nil || v != 0xa {
		t.Fatalf("Read(4) = %#x, %v", v, err)
	}
	if v, err := r.Read(3); err != nil || v != 0x2 {
		t.Fatalf("Read(3) = %#x, %v", v, err)
	}
	// Remaining bits: 1 bit of 0xa5 (1), then full 0x3c.
	if v, err := r.Read(5); err != nil || v != 0x13 {
		t.Fatalf("Read(5) = %#x, %v", v, err)
	}
	if v, err := r.Read(4); err != nil || v != 0xc {
		t.Fatalf("Read(4) = %#x, %v", v, err)
	}
	if r.Remaining() != 0 {
		t.Fatalf("remaining = %d, want 0", r.Remaining())
	}
}

func TestBitReaderReadBitAndAlign(t *testing.T) {
	r := NewBitReader([]byte{0x80, 0x01})
	b, err := r.ReadBit()
	if err != nil || !b {
		t.Fatalf("ReadBit = %v, %v", b, err)
	}
	r.Align()
	bytePos, bitPos := r.Position()
	if bytePos != 1 || bitPos != 0 {
		t.Fatalf("position after align = %d.%d, want 1.0", bytePos, bitPos)
	}
	if v, err := r.Read(8); err != nil || v != 0x01 {
		t.Fatalf("Read(8) = %#x, %v", v, err)
	}
}

func TestBitReaderBounds(t *testing.T) {
	r := NewBitReader([]byte{0xff})
	if _, err := r.Read(0); err != nil {
		t.Fatalf("Read(0) = %v", err)
	}
	if _, err := r.Read(33); !errors.Is(err, ErrBitRange) {
		t.Fatalf("Read(33) = %v, want ErrBitRange", err)
	}
	if err := r.Skip(9); !errors.Is(err, ErrShortRead) {
		t.Fatalf("Skip(9) = %v, want ErrShortRead", err)
	}
	if _, err := r.Read(9); !errors.Is(err, ErrShortRead) {
		t.Fatalf("Read(9) = %v, want ErrShortRead", err)
	}
}
