package aac

import "errors"

// ErrBadHuffman reports a codeword that is not present in the active book,
// which indicates a damaged bitstream.
var ErrBadHuffman = errors.New("aac: invalid Huffman codeword")

// Per-codebook structural facts for codebooks 1..11, indexed cb-1 (ISO/IEC
// 14496-3 section 4.6.2). dim is the tuple size, mod the base of the
// index-to-value decomposition, off the signed-value offset, and unsigned
// marks books whose magnitudes carry separate sign bits.
var (
	hcbDim      = [11]int{4, 4, 4, 4, 2, 2, 2, 2, 2, 2, 2}
	hcbMod      = [11]int{3, 3, 3, 3, 9, 9, 8, 8, 13, 13, 17}
	hcbOff      = [11]int{1, 1, 0, 0, 4, 4, 0, 0, 0, 0, 0}
	hcbUnsigned = [11]bool{false, false, true, true, false, false, true, true, true, true, true}
)

// huffBook is a Huffman codebook stored as a flat binary tree. Node k uses
// tree[2k] for a zero bit and tree[2k+1] for a one bit. A slot holds a
// child node index (> 0), a leaf (negative, encoding the codebook index as
// -idx-1), or 0 for an unreached path.
type huffBook struct {
	tree []int32
}

var (
	spectralBooks   [11]huffBook
	scalefactorBook huffBook
)

func init() {
	for cb := 0; cb < 11; cb++ {
		codes := make([]uint32, len(spectralCodes[cb]))
		for i, c := range spectralCodes[cb] {
			codes[i] = uint32(c)
		}
		spectralBooks[cb] = buildBook(codes, spectralBits[cb])
	}
	scalefactorBook = buildBook(scalefactorCodes[:], scalefactorBits[:])
}

// buildBook inserts each (codeword, length) pair MSB-first. The root is node
// 0, which no valid prefix can point back to, so 0 marks an unreached path.
func buildBook(codes []uint32, bits []uint8) huffBook {
	tree := make([]int32, 2)
	for i := range codes {
		cw, l := codes[i], int(bits[i])
		node := 0
		for b := l - 1; b >= 0; b-- {
			slot := 2*node + int((cw>>uint(b))&1)
			if b == 0 {
				tree[slot] = -(int32(i) + 1)
				break
			}
			if tree[slot] == 0 {
				tree[slot] = int32(len(tree) / 2)
				tree = append(tree, 0, 0)
			}
			node = int(tree[slot])
		}
	}
	return huffBook{tree}
}

// decode walks the tree one bit at a time and returns the decoded index.
func (b *huffBook) decode(r *BitReader) (int, error) {
	node := 0
	for {
		bit, err := r.ReadBit()
		if err != nil {
			return 0, err
		}
		slot := 2 * node
		if bit {
			slot++
		}
		v := b.tree[slot]
		switch {
		case v < 0:
			return int(-v - 1), nil
		case v == 0:
			return 0, ErrBadHuffman
		default:
			node = int(v)
		}
	}
}

// decodeSpectral reads one codebook tuple into out[:dim], applying the
// index-to-value decomposition, sign bits for unsigned books, and codebook
// 11's escape sequence for magnitude 16.
func decodeSpectral(r *BitReader, cb int, out []int) error {
	if cb < 1 || cb > 11 {
		return ErrBadHuffman
	}
	idx, err := spectralBooks[cb-1].decode(r)
	if err != nil {
		return err
	}
	dim := hcbDim[cb-1]
	mod := hcbMod[cb-1]
	for d := dim - 1; d >= 0; d-- {
		out[d] = idx % mod
		idx /= mod
	}
	if !hcbUnsigned[cb-1] {
		off := hcbOff[cb-1]
		for d := 0; d < dim; d++ {
			out[d] -= off
		}
		return nil
	}
	// Codebook 11's escape word comes before its sign bit: the base value 16
	// is replaced by the escaped magnitude, and only then is the sign read.
	if cb == 11 {
		for d := 0; d < dim; d++ {
			if out[d] == 16 {
				out[d], err = decodeEscape(r)
				if err != nil {
					return err
				}
			}
		}
	}
	for d := 0; d < dim; d++ {
		if out[d] != 0 {
			neg, err := r.ReadBit()
			if err != nil {
				return err
			}
			if neg {
				out[d] = -out[d]
			}
		}
	}
	return nil
}

// decodeEscape reads codebook 11's escape word: N leading ones, a zero, then
// N+4 magnitude bits, giving 2^(N+4) + word.
func decodeEscape(r *BitReader) (int, error) {
	n := 0
	for {
		bit, err := r.ReadBit()
		if err != nil {
			return 0, err
		}
		if !bit {
			break
		}
		n++
		if n > 24 {
			return 0, ErrBadHuffman
		}
	}
	v, err := r.Read(n + 4)
	if err != nil {
		return 0, err
	}
	return (1 << uint(n+4)) + int(v), nil
}

// decodeScalefactor reads one DPCM scalefactor delta in [-60, 60].
func decodeScalefactor(r *BitReader) (int, error) {
	idx, err := scalefactorBook.decode(r)
	if err != nil {
		return 0, err
	}
	return idx - 60, nil
}
