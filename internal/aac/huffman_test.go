package aac

import "testing"

// encodeBits packs the low n bits of code MSB-first into bytes, matching the
// bit order consumed by BitReader.
func encodeBits(code uint32, n int) []byte {
	buf := make([]byte, (n+7)/8)
	for b := n - 1; b >= 0; b-- {
		if (code>>uint(b))&1 != 0 {
			buf[(n-1-b)/8] |= 1 << (7 - uint((n-1-b)%8))
		}
	}
	return buf
}

// treeLeaves counts the leaf slots in a flat Huffman tree.
func treeLeaves(b huffBook) int {
	n := 0
	for _, v := range b.tree {
		if v < 0 {
			n++
		}
	}
	return n
}

func TestHuffmanRoundTrip(t *testing.T) {
	for cb := 0; cb < 11; cb++ {
		if treeLeaves(spectralBooks[cb]) != len(spectralCodes[cb]) {
			t.Errorf("book %d: leaves %d != codes %d", cb+1, treeLeaves(spectralBooks[cb]), len(spectralCodes[cb]))
		}
		for i, code := range spectralCodes[cb] {
			n := int(spectralBits[cb][i])
			r := NewBitReader(encodeBits(uint32(code), n))
			got, err := spectralBooks[cb].decode(r)
			if err != nil {
				t.Fatalf("book %d index %d: %v", cb+1, i, err)
			}
			if got != i {
				t.Fatalf("book %d index %d: decoded %d", cb+1, i, got)
			}
		}
	}
	if treeLeaves(scalefactorBook) != len(scalefactorCodes) {
		t.Errorf("scalefactor: leaves %d != codes %d", treeLeaves(scalefactorBook), len(scalefactorCodes))
	}
	for i, code := range scalefactorCodes {
		n := int(scalefactorBits[i])
		r := NewBitReader(encodeBits(uint32(code), n))
		got, err := scalefactorBook.decode(r)
		if err != nil {
			t.Fatalf("scalefactor index %d: %v", i, err)
		}
		if got != i {
			t.Fatalf("scalefactor index %d: decoded %d", i, got)
		}
	}
}

func TestHuffmanSentinels(t *testing.T) {
	if scalefactorCodes[60] != 0 || scalefactorBits[60] != 1 {
		t.Fatalf("scalefactor delta 0 sentinel: code=%#x len=%d", scalefactorCodes[60], scalefactorBits[60])
	}
	r := NewBitReader(encodeBits(0, 1))
	if delta, err := decodeScalefactor(r); err != nil || delta != 0 {
		t.Fatalf("decodeScalefactor(0b0) = %d, %v; want 0, nil", delta, err)
	}

	// Book 1 index 40 is the single-bit zero codeword and decodes to an
	// all-zero 4-tuple after the -1 signed offset is applied.
	var out [4]int
	r = NewBitReader(encodeBits(0, 1))
	if err := decodeSpectral(r, 1, out[:]); err != nil {
		t.Fatalf("decodeSpectral book 1: %v", err)
	}
	if out != [4]int{0, 0, 0, 0} {
		t.Fatalf("decodeSpectral book 1 = %v", out)
	}

	// Book 11 index 0 is the four-bit zero codeword for a zero 2-tuple.
	var out2 [2]int
	r = NewBitReader(encodeBits(0, 4))
	if err := decodeSpectral(r, 11, out2[:]); err != nil {
		t.Fatalf("decodeSpectral book 11: %v", err)
	}
	if out2 != [2]int{0, 0} {
		t.Fatalf("decodeSpectral book 11 = %v", out2)
	}
}

func TestScaleFactorBandTables(t *testing.T) {
	for i := range swbOffsetLong {
		checkBands(t, "swbOffsetLong", i, swbOffsetLong[i], 1024)
		checkBands(t, "swbOffsetShort", i, swbOffsetShort[i], 128)
	}
	// The supported rates (48 kHz index 3, 44.1 kHz index 4) have 49 long
	// bands and 14 short bands.
	if n := len(swbOffsetLong[3]) - 1; n != 49 {
		t.Errorf("48 kHz long bands = %d, want 49", n)
	}
	if n := len(swbOffsetShort[3]) - 1; n != 14 {
		t.Errorf("48 kHz short bands = %d, want 14", n)
	}
	if n := len(swbOffsetLong[4]) - 1; n != 49 {
		t.Errorf("44.1 kHz long bands = %d, want 49", n)
	}
	if n := len(swbOffsetShort[4]) - 1; n != 14 {
		t.Errorf("44.1 kHz short bands = %d, want 14", n)
	}
}

func checkBands(t *testing.T, name string, idx int, offs []uint16, end uint16) {
	t.Helper()
	if len(offs) < 2 {
		t.Fatalf("%s[%d] too short", name, idx)
	}
	if offs[0] != 0 || offs[len(offs)-1] != end {
		t.Fatalf("%s[%d] bounds = %d..%d, want 0..%d", name, idx, offs[0], offs[len(offs)-1], end)
	}
	for j := 1; j < len(offs); j++ {
		if offs[j] <= offs[j-1] {
			t.Fatalf("%s[%d] not strictly increasing at %d", name, idx, j)
		}
	}
}
