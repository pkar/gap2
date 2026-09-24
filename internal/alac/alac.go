// Package alac implements an Apple Lossless Audio Codec (ALAC) decoder for the
// 16-bit mono and stereo streams carried by AirPlay. It decodes one compressed
// frame at a time into interleaved S16LE PCM.
//
// The bitstream layout follows the format reverse-engineered by David
// Hammerton and implemented by FFmpeg: each frame is a sequence of syntax
// elements prefixed by a 3-bit element type, terminated by a TYPE_END tag.
package alac

import (
	"errors"
	"fmt"

	"github.com/pkar/gap2/internal/sdp"
	"github.com/pkar/gap2/pcm"
)

// Errors returned by the decoder.
var (
	ErrMalformed   = errors.New("alac: malformed frame")
	ErrUnsupported = errors.New("alac: unsupported")
)

// Syntax element types.
const (
	typeSCE = 0 // single channel element
	typeCPE = 1 // channel pair element
	typeCCE = 2 // coupling channel element
	typeLFE = 3 // low-frequency element
	typeDSE = 4
	typePCE = 5
	typeFIL = 6
	typeEnd = 7
)

// Decoder decodes ALAC frames for one stream configuration.
type Decoder struct {
	frameLength        int
	bitDepth           int
	riceHistoryMult    int
	riceInitialHistory int
	riceLimit          int
	channels           int
	sampleRate         int
}

// NewDecoder validates the ALAC magic cookie and returns a decoder. Only
// 16-bit mono or stereo is supported.
func NewDecoder(cfg *sdp.ALACConfig) (*Decoder, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrMalformed)
	}
	if cfg.BitDepth != 16 {
		return nil, fmt.Errorf("%w: bit depth %d", ErrUnsupported, cfg.BitDepth)
	}
	if cfg.Channels != 1 && cfg.Channels != 2 {
		return nil, fmt.Errorf("%w: %d channels", ErrUnsupported, cfg.Channels)
	}
	if cfg.FrameLength <= 0 {
		return nil, fmt.Errorf("%w: frame length %d", ErrMalformed, cfg.FrameLength)
	}
	d := &Decoder{
		frameLength:        cfg.FrameLength,
		bitDepth:           cfg.BitDepth,
		riceHistoryMult:    cfg.PB,
		riceInitialHistory: cfg.MB,
		riceLimit:          cfg.KB,
		channels:           cfg.Channels,
		sampleRate:         cfg.SampleRate,
	}
	if d.riceHistoryMult == 0 {
		d.riceHistoryMult = 40
	}
	if d.riceInitialHistory == 0 {
		d.riceInitialHistory = 10
	}
	if d.riceLimit == 0 {
		d.riceLimit = 14
	}
	return d, nil
}

// Decode decodes one ALAC frame into an interleaved S16LE PCM block. The block
// length matches the frame's sample count, which may be shorter than the
// configured frame length for a trailing partial frame.
func (d *Decoder) Decode(frame []byte) (pcm.Block, error) {
	r := &bitReader{data: frame}

	var samples [][]int32
	nbSamples := 0
	gotEnd := false
	ch := 0
	for r.remaining() >= 3 {
		element, err := r.read(3)
		if err != nil {
			return pcm.Block{}, ErrMalformed
		}
		if element == typeEnd {
			gotEnd = true
			break
		}
		if element > typeCPE && element != typeLFE {
			return pcm.Block{}, fmt.Errorf("%w: element %d", ErrUnsupported, element)
		}
		channels := 1
		if element == typeCPE {
			channels = 2
		}
		if ch+channels > d.channels {
			return pcm.Block{}, ErrMalformed
		}
		out, n, err := d.decodeElement(r, channels)
		if err != nil {
			return pcm.Block{}, err
		}
		if nbSamples == 0 {
			nbSamples = n
		} else if n != nbSamples {
			return pcm.Block{}, fmt.Errorf("%w: sample count mismatch", ErrMalformed)
		}
		samples = append(samples, out...)
		ch += channels
	}
	if !gotEnd || ch != d.channels || nbSamples == 0 {
		return pcm.Block{}, fmt.Errorf("%w: incomplete frame", ErrMalformed)
	}

	format := pcm.Format{Rate: d.sampleRate, Channels: d.channels, Format: pcm.S16LE}
	block, err := pcm.NewBlock(format, nbSamples)
	if err != nil {
		return pcm.Block{}, err
	}
	for i := 0; i < nbSamples; i++ {
		for c := 0; c < d.channels; c++ {
			v := int16(samples[c][i])
			off := 2 * (i*d.channels + c)
			block.Data[off] = byte(v)
			block.Data[off+1] = byte(uint16(v) >> 8)
		}
	}
	return block, nil
}

// decodeElement decodes one syntax element (mono or stereo) and returns the
// per-channel sample slices and the sample count.
func (d *Decoder) decodeElement(r *bitReader, channels int) ([][]int32, int, error) {
	if _, err := r.read(4); err != nil { // element instance tag
		return nil, 0, ErrMalformed
	}
	if _, err := r.read(12); err != nil { // unused header bits
		return nil, 0, ErrMalformed
	}

	hasSize, err := r.read(1)
	if err != nil {
		return nil, 0, ErrMalformed
	}
	extraBitsRaw, err := r.read(2)
	if err != nil {
		return nil, 0, ErrMalformed
	}
	extraBits := int(extraBitsRaw << 3)
	if extraBits != 0 {
		return nil, 0, fmt.Errorf("%w: %d-bit samples", ErrUnsupported, d.bitDepth)
	}
	bps := d.bitDepth - extraBits + channels - 1
	if bps < 1 || bps > 32 {
		return nil, 0, ErrMalformed
	}
	compressedBit, err := r.read(1)
	if err != nil {
		return nil, 0, ErrMalformed
	}
	isCompressed := compressedBit == 0

	nbSamples := d.frameLength
	if hasSize != 0 {
		v, err := r.read(32)
		if err != nil {
			return nil, 0, ErrMalformed
		}
		nbSamples = int(v)
	}
	if nbSamples <= 0 || nbSamples > d.frameLength {
		return nil, 0, fmt.Errorf("%w: samples %d", ErrMalformed, nbSamples)
	}

	out := make([][]int32, channels)
	for c := range out {
		out[c] = make([]int32, nbSamples)
	}

	if !isCompressed {
		// Raw PCM, one sample_size-bit value per sample per channel.
		for i := 0; i < nbSamples; i++ {
			for c := 0; c < channels; c++ {
				v, err := r.readSigned(d.bitDepth)
				if err != nil {
					return nil, 0, ErrMalformed
				}
				out[c][i] = v
			}
		}
		return out, nbSamples, nil
	}

	// Compressed: read the per-element decorrelation and predictor state.
	decorrShift, err := r.read(8)
	if err != nil {
		return nil, 0, ErrMalformed
	}
	decorrLeftWeight, err := r.read(8)
	if err != nil {
		return nil, 0, ErrMalformed
	}
	if channels == 2 && decorrLeftWeight != 0 && decorrShift > 31 {
		return nil, 0, ErrMalformed
	}

	coefs := make([][]int16, channels)
	orders := make([]int, channels)
	quants := make([]int, channels)
	types := make([]uint32, channels)
	mults := make([]int, channels)

	for c := 0; c < channels; c++ {
		predType, err := r.read(4)
		if err != nil {
			return nil, 0, ErrMalformed
		}
		lpcQuant, err := r.read(4)
		if err != nil {
			return nil, 0, ErrMalformed
		}
		riceMult, err := r.read(3)
		if err != nil {
			return nil, 0, ErrMalformed
		}
		lpcOrder, err := r.read(5)
		if err != nil {
			return nil, 0, ErrMalformed
		}
		if lpcOrder >= uint32(d.frameLength) || lpcQuant == 0 {
			return nil, 0, ErrMalformed
		}
		types[c] = predType
		quants[c] = int(lpcQuant)
		mults[c] = int(riceMult)
		orders[c] = int(lpcOrder)

		coefs[c] = make([]int16, lpcOrder)
		for i := int(lpcOrder) - 1; i >= 0; i-- {
			v, err := r.readSigned(16)
			if err != nil {
				return nil, 0, ErrMalformed
			}
			coefs[c][i] = int16(v)
		}
	}

	for c := 0; c < channels; c++ {
		residuals := make([]int32, nbSamples)
		effectiveMult := mults[c] * d.riceHistoryMult / 4
		if err := d.riceDecompress(r, residuals, bps, effectiveMult); err != nil {
			return nil, 0, err
		}
		if types[c] == 15 {
			// Prediction type 15 runs a first-order pass in place first.
			lpcPrediction(residuals, residuals, bps, nil, 31, 0)
		}
		lpcPrediction(residuals, out[c], bps, coefs[c], orders[c], quants[c])
	}

	if channels == 2 && decorrLeftWeight != 0 {
		decorrelateStereo(out[0], out[1], int(decorrShift), int(decorrLeftWeight))
	}
	return out, nbSamples, nil
}

// riceDecompress decodes nbSamples residuals using the adaptive Rice scheme.
func (d *Decoder) riceDecompress(r *bitReader, out []int32, bps, mult int) error {
	history := uint32(d.riceInitialHistory)
	signModifier := uint32(0)

	for i := 0; i < len(out); i++ {
		if r.remaining() <= 0 {
			return ErrMalformed
		}
		k := log2((history >> 9) + 3)
		if k > d.riceLimit {
			k = d.riceLimit
		}
		x, err := r.decodeScalar(k, bps)
		if err != nil {
			return err
		}
		x += signModifier
		signModifier = 0
		out[i] = int32((x >> 1) ^ (0 - (x & 1)))

		if x > 0xffff {
			history = 0xffff
		} else {
			history += x*uint32(mult) - ((history * uint32(mult)) >> 9)
		}

		// Compressed runs of zero samples.
		if history < 128 && i+1 < len(out) {
			k = 7 - log2(history) + int((history+16)>>6)
			if k > d.riceLimit {
				k = d.riceLimit
			}
			blockSize, err := r.decodeScalar(k, 16)
			if err != nil {
				return err
			}
			if blockSize > 0 {
				if blockSize >= uint32(len(out)-i) {
					blockSize = uint32(len(out)-i) - 1
				}
				for j := uint32(0); j < blockSize; j++ {
					out[i+1+int(j)] = 0
				}
				i += int(blockSize)
			}
			if blockSize <= 0xffff {
				signModifier = 1
			}
			history = 0
		}
	}
	return nil
}

// lpcPrediction reconstructs samples from residuals using the adaptive FIR.
// errorBuf is the residual input and out receives the reconstructed samples;
// they may be the same slice for the in-place first-order pass.
func lpcPrediction(errorBuf, out []int32, bps int, coefs []int16, order, quant int) {
	n := len(out)
	if n == 0 {
		return
	}
	out[0] = errorBuf[0]
	if n == 1 {
		return
	}
	if order == 0 {
		copy(out[1:], errorBuf[1:])
		return
	}
	if order == 31 {
		for i := 1; i < n; i++ {
			out[i] = signExtend(out[i-1]+errorBuf[i], bps)
		}
		return
	}

	for i := 1; i <= order && i < n; i++ {
		out[i] = signExtend(out[i-1]+errorBuf[i], bps)
	}

	for i := order + 1; i < n; i++ {
		d := out[i-order-1]
		var val int64
		for j := 0; j < order; j++ {
			val += int64(out[i-order+j]-d) * int64(coefs[j])
		}
		val = (val + (1 << (quant - 1))) >> quant
		val += int64(d) + int64(errorBuf[i])
		out[i] = signExtend(int32(val), bps)

		// Adapt the predictor coefficients.
		errVal := int64(errorBuf[i])
		errSign := signOnly(errVal)
		if errSign != 0 {
			for j := 0; j < order && errVal*errSign > 0; j++ {
				v := int64(d) - int64(out[i-order+j])
				s := signOnly(v) * errSign
				coefs[j] -= int16(s)
				v *= s
				errVal -= (v >> quant) * int64(j+1)
			}
		}
	}
}

// decorrelateStereo reverses the encoder's stereo decorrelation.
func decorrelateStereo(left, right []int32, shift, weight int) {
	for i := range left {
		a := int64(left[i])
		b := int64(right[i])
		a -= (b * int64(weight)) >> shift
		b += a
		left[i] = int32(b)
		right[i] = int32(a)
	}
}

func signExtend(v int32, bits int) int32 {
	if bits <= 0 || bits >= 32 {
		return v
	}
	shift := 32 - bits
	return int32(uint32(v)<<shift) >> shift
}

func signOnly(v int64) int64 {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	default:
		return 0
	}
}

// log2 returns floor(log2(v)); log2(0) is 0.
func log2(v uint32) int {
	n := 0
	for v > 1 {
		v >>= 1
		n++
	}
	return n
}

// bitReader reads bits MSB-first from a byte slice.
type bitReader struct {
	data   []byte
	bitPos int
}

func (r *bitReader) remaining() int { return len(r.data)*8 - r.bitPos }

func (r *bitReader) read(n int) (uint32, error) {
	if n < 0 || r.bitPos+n > len(r.data)*8 {
		return 0, ErrMalformed
	}
	var v uint32
	for i := 0; i < n; i++ {
		byteIdx := r.bitPos >> 3
		bitIdx := 7 - (r.bitPos & 7)
		v = (v << 1) | uint32((r.data[byteIdx]>>bitIdx)&1)
		r.bitPos++
	}
	return v, nil
}

func (r *bitReader) peek(n int) (uint32, error) {
	if n < 0 || r.bitPos+n > len(r.data)*8 {
		return 0, ErrMalformed
	}
	var v uint32
	p := r.bitPos
	for i := 0; i < n; i++ {
		byteIdx := p >> 3
		bitIdx := 7 - (p & 7)
		v = (v << 1) | uint32((r.data[byteIdx]>>bitIdx)&1)
		p++
	}
	return v, nil
}

func (r *bitReader) skip(n int) { r.bitPos += n }

// readSigned reads n bits and sign-extends them to int32.
func (r *bitReader) readSigned(n int) (int32, error) {
	v, err := r.read(n)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	if n >= 32 {
		return int32(v), nil
	}
	if v&(uint32(1)<<(n-1)) != 0 {
		return int32(v) - int32(1<<n), nil
	}
	return int32(v), nil
}

// readUnary9 reads a unary code capped at 9: the number of leading 1 bits.
func (r *bitReader) readUnary9() (uint32, error) {
	for i := 0; i < 9; i++ {
		b, err := r.read(1)
		if err != nil {
			return 0, err
		}
		if b == 0 {
			return uint32(i), nil
		}
	}
	return 9, nil
}

// decodeScalar decodes one Rice-coded scalar.
func (r *bitReader) decodeScalar(k, bps int) (uint32, error) {
	x, err := r.readUnary9()
	if err != nil {
		return 0, err
	}
	if x > 8 {
		v, err := r.read(bps)
		if err != nil {
			return 0, err
		}
		return v, nil
	}
	if k != 1 {
		extrabits, err := r.peek(k)
		if err != nil {
			return 0, err
		}
		x = (x << k) - x
		if extrabits > 1 {
			x += extrabits - 1
			r.skip(k)
		} else {
			r.skip(k - 1)
		}
	}
	return x, nil
}
