package aac

import (
	"errors"
	"math"

	"github.com/pkar/gap2/pcm"
)

// Syntactic element types (ISO/IEC 14496-3 Table 4.85).
const (
	elSCE = 0
	elCPE = 1
	elCCE = 2
	elLFE = 3
	elDSE = 4
	elPCE = 5
	elFIL = 6
	elEND = 7
)

// Window sequence values from ics_info.window_sequence.
const (
	onlyLong   = 0
	longStart  = 1
	eightShort = 2
	longStop   = 3
)

// Huffman codebook selectors used outside huffman.go.
const (
	zeroHCB       = 0
	reservedHCB   = 12
	noiseHCB      = 13
	intensityHCB2 = 14
	intensityHCB  = 15
)

const (
	sfOffset        = 100 // scalefactor gain offset in 2^0.25 steps
	maxWindowGroups = 8
	maxSFBCount     = 64
	maxTNSOrder     = 20
)

// Errors returned by Decoder.
var (
	ErrMalformed         = errors.New("aac: malformed access unit")
	ErrUnsupported       = errors.New("aac: unsupported feature")
	ErrUnsupportedObject = errors.New("aac: unsupported audio object type")
)

// tnsMaxBandsLong/Short cap the TNS-filtered band region per sample-rate
// index (ISO/IEC 14496-3 Table 4.139).
var (
	tnsMaxBandsLong  = [13]int{31, 31, 34, 40, 42, 51, 46, 46, 42, 42, 42, 39, 39}
	tnsMaxBandsShort = [13]int{9, 9, 10, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14}
)

// icsInfo is the per-channel window and grouping description.
type icsInfo struct {
	windowSequence  int
	windowShape     int
	maxSfb          int
	numWindows      int
	numWindowGroups int
	windowGroupLen  [maxWindowGroups]int
	swb             []uint16
	numSwb          int
}

// pulseInfo carries the optional pulse escape (long windows only).
type pulseInfo struct {
	numPulse int
	startSfb int
	offset   [4]int
	amp      [4]int
}

// tnsFilter is one temporal-noise-shaping filter's synthesis coefficients.
type tnsFilter struct {
	length    int
	order     int
	direction int
	coef      [maxTNSOrder]float64
}

// tnsInfo holds the parsed TNS filters per window.
type tnsInfo struct {
	nFilt   [8]int
	coefRes [8]int
	filt    [8][4]tnsFilter
}

// channelData holds one channel's parsed ICS and decoded spectrum.
type channelData struct {
	info       icsInfo
	globalGain int
	sfbCb      [maxWindowGroups][maxSFBCount]uint8
	sf         [maxWindowGroups][maxSFBCount]int
	spec       [1024]float64
	tns        tnsInfo
	hasTNS     bool
	hasPulse   bool
	pulse      pulseInfo
}

// Decoder decodes AAC-LC access units (raw_data_block payloads, one frame
// per call) into interleaved 16-bit PCM. The IMDCT overlap makes frame N
// depend on frame N-1; Reset clears that state after a seek.
type Decoder struct {
	rate     int
	rateIdx  int
	channels int

	overlap  [2][1024]float64
	prevWin  [2]int
	pnsState uint32
}

// NewDecoder returns a Decoder for an AudioSpecificConfig. Only AAC-LC with
// one or two channels is supported.
func NewDecoder(asc ASC) (*Decoder, error) {
	if asc.ObjectType != 2 {
		return nil, ErrUnsupportedObject
	}
	if asc.SBR {
		return nil, ErrUnsupported
	}
	if asc.ChannelConfiguration != 1 && asc.ChannelConfiguration != 2 {
		return nil, ErrUnsupported
	}
	idx := samplingIndex(asc.SamplingFrequency)
	if idx < 0 {
		return nil, ErrUnsupported
	}
	return &Decoder{
		rate:     asc.SamplingFrequency,
		rateIdx:  idx,
		channels: asc.ChannelConfiguration,
		prevWin:  [2]int{shapeSine, shapeSine},
		pnsState: 0x1f2e3d4c,
	}, nil
}

// samplingIndex maps a sample rate to its scalefactor-band table index.
func samplingIndex(rate int) int {
	for i, r := range sampleRates {
		if r == rate {
			return i
		}
	}
	return -1
}

// Reset clears the filterbank overlap and window history after a seek.
func (d *Decoder) Reset() {
	d.overlap = [2][1024]float64{}
	d.prevWin = [2]int{shapeSine, shapeSine}
}

// Decode decodes one access unit into a 1024-frame interleaved S16LE block.
func (d *Decoder) Decode(au []byte) (pcm.Block, error) {
	format := pcm.Format{Rate: d.rate, Channels: d.channels, Format: pcm.S16LE}
	block, err := pcm.NewBlock(format, 1024)
	if err != nil {
		return pcm.Block{}, err
	}
	out := make([]float64, d.channels*1024)

	r := NewBitReader(au)
	slot := 0
	for r.Remaining() >= 3 && slot < d.channels {
		tag, err := r.Read(3)
		if err != nil {
			return pcm.Block{}, ErrMalformed
		}
		switch tag {
		case elSCE, elLFE:
			if _, err := r.Read(4); err != nil { // element_instance_tag
				return pcm.Block{}, ErrMalformed
			}
			cd := &channelData{}
			if err := d.decodeChannelData(r, cd, false); err != nil {
				return pcm.Block{}, err
			}
			d.dequant(cd)
			d.applyPNS(cd)
			d.finishChannel(cd, slot, out)
			slot++
		case elCPE:
			if _, err := r.Read(4); err != nil { // element_instance_tag
				return pcm.Block{}, ErrMalformed
			}
			if slot+2 > d.channels {
				return pcm.Block{}, ErrUnsupported
			}
			if err := d.decodePair(r, slot, out); err != nil {
				return pcm.Block{}, err
			}
			slot += 2
		case elDSE:
			if err := skipDSE(r); err != nil {
				return pcm.Block{}, err
			}
		case elPCE:
			if err := skipPCE(r); err != nil {
				return pcm.Block{}, err
			}
		case elFIL:
			if err := skipFIL(r); err != nil {
				return pcm.Block{}, err
			}
		case elEND:
			r.Align()
			return writeBlock(block, out)
		default: // CCE
			return pcm.Block{}, ErrUnsupported
		}
	}
	return writeBlock(block, out)
}

// writeBlock converts float samples at integer-PCM scale into interleaved
// little-endian 16-bit PCM.
func writeBlock(block pcm.Block, out []float64) (pcm.Block, error) {
	for i := range out {
		// The filterbank writes planar channels; PCM sinks require frames
		// interleaved as L,R,L,R rather than a whole L block then R.
		v := out[(i%block.Format.Channels)*1024+i/block.Format.Channels]
		s := floatToInt16(v)
		block.Data[2*i] = byte(s)
		block.Data[2*i+1] = byte(s >> 8)
	}
	return block, nil
}

func floatToInt16(v float64) int16 {
	if v >= 32767 {
		return 32767
	}
	if v <= -32768 {
		return -32768
	}
	return int16(v)
}

// decodePair decodes a channel-pair element, applying the shared window and
// M/S stereo before the per-channel filterbank.
func (d *Decoder) decodePair(r *BitReader, leftSlot int, out []float64) error {
	common, err := r.ReadBit()
	if err != nil {
		return ErrMalformed
	}
	var shared icsInfo
	msMask := 0
	var msUsed [maxWindowGroups][maxSFBCount]bool
	if common {
		if !d.parseICSInfo(r, &shared) {
			return ErrMalformed
		}
		v, err := r.Read(2)
		if err != nil {
			return ErrMalformed
		}
		msMask = int(v)
		if msMask == 1 {
			for g := 0; g < shared.numWindowGroups; g++ {
				for sfb := 0; sfb < shared.maxSfb; sfb++ {
					bit, err := r.ReadBit()
					if err != nil {
						return ErrMalformed
					}
					msUsed[g][sfb] = bit
				}
			}
		}
	}
	left, right := &channelData{}, &channelData{}
	if common {
		left.info = shared
		right.info = shared
	}
	if err := d.decodeChannelData(r, left, common); err != nil {
		return err
	}
	if err := d.decodeChannelData(r, right, common); err != nil {
		return err
	}
	d.dequant(left)
	d.dequant(right)
	d.applyPNS(left)
	d.applyPNS(right)
	if common && msMask != 0 {
		applyMS(left, right, msMask, &msUsed)
	}
	applyIntensity(left, right, msMask, &msUsed)
	d.finishChannel(left, leftSlot, out)
	d.finishChannel(right, leftSlot+1, out)
	return nil
}

// parseICSInfo reads ics_info, resolving window grouping and the
// scalefactor-band table for the window type.
func (d *Decoder) parseICSInfo(r *BitReader, info *icsInfo) bool {
	if _, err := r.ReadBit(); err != nil { // ics_reserved_bit
		return false
	}
	seq, err := r.Read(2)
	if err != nil {
		return false
	}
	shape, err := r.ReadBit()
	if err != nil {
		return false
	}
	info.windowSequence = int(seq)
	if shape {
		info.windowShape = shapeKBD
	} else {
		info.windowShape = shapeSine
	}
	if info.windowSequence == eightShort {
		v, err := r.Read(4)
		if err != nil {
			return false
		}
		info.maxSfb = int(v)
		grouping, err := r.Read(7)
		if err != nil {
			return false
		}
		info.numWindows = 8
		info.numWindowGroups = 1
		info.windowGroupLen[0] = 1
		for i := 0; i < 7; i++ {
			if grouping&(1<<uint(6-i)) != 0 {
				info.windowGroupLen[info.numWindowGroups-1]++
			} else {
				info.numWindowGroups++
				info.windowGroupLen[info.numWindowGroups-1] = 1
			}
		}
		info.swb = swbOffsetShort[d.rateIdx]
		info.numSwb = len(info.swb) - 1
	} else {
		v, err := r.Read(6)
		if err != nil {
			return false
		}
		info.maxSfb = int(v)
		pred, err := r.ReadBit()
		if err != nil {
			return false
		}
		if pred { // predictor_data_present is not valid in AAC-LC
			return false
		}
		info.numWindows = 1
		info.numWindowGroups = 1
		info.windowGroupLen[0] = 1
		info.swb = swbOffsetLong[d.rateIdx]
		info.numSwb = len(info.swb) - 1
	}
	return info.maxSfb <= info.numSwb && info.maxSfb <= maxSFBCount
}

// decodeChannelData reads an individual_channel_stream: global_gain, then
// ics_info unless the window is shared, then section, scalefactor, pulse,
// TNS, gain-control, and spectral data.
func (d *Decoder) decodeChannelData(r *BitReader, cd *channelData, common bool) error {
	v, err := r.Read(8)
	if err != nil {
		return ErrMalformed
	}
	cd.globalGain = int(v)
	if !common {
		if !d.parseICSInfo(r, &cd.info) {
			return ErrMalformed
		}
	}
	if !d.sectionData(r, cd) {
		return ErrMalformed
	}
	if !d.scaleFactorData(r, cd) {
		return ErrMalformed
	}
	hasPulse, err := r.ReadBit()
	if err != nil {
		return ErrMalformed
	}
	cd.hasPulse = hasPulse
	if cd.hasPulse {
		if !parsePulse(r, cd) {
			return ErrMalformed
		}
	}
	hasTNS, err := r.ReadBit()
	if err != nil {
		return ErrMalformed
	}
	cd.hasTNS = hasTNS
	if cd.hasTNS {
		if !parseTNS(r, cd) {
			return ErrMalformed
		}
	}
	gain, err := r.ReadBit()
	if err != nil {
		return ErrMalformed
	}
	if gain { // gain control is not allowed in AAC-LC
		return ErrUnsupported
	}
	if !d.spectralData(r, cd) {
		return ErrMalformed
	}
	return nil
}

// groupStarts returns the first window index of each window group.
func groupStarts(info *icsInfo) [maxWindowGroups]int {
	var gs [maxWindowGroups]int
	acc := 0
	for g := 0; g < info.numWindowGroups; g++ {
		gs[g] = acc
		acc += info.windowGroupLen[g]
	}
	return gs
}

// sectionData assigns a Huffman codebook to every scalefactor band.
func (d *Decoder) sectionData(r *BitReader, cd *channelData) bool {
	info := &cd.info
	lenBits := uint(5)
	esc := uint32(31)
	if info.windowSequence == eightShort {
		lenBits = 3
		esc = 7
	}
	for g := 0; g < info.numWindowGroups; g++ {
		k := 0
		for k < info.maxSfb {
			v, err := r.Read(4)
			if err != nil {
				return false
			}
			cb := uint8(v)
			if cb == reservedHCB {
				return false
			}
			length := 0
			for {
				incr, err := r.Read(int(lenBits))
				if err != nil {
					return false
				}
				length += int(incr)
				if incr != esc {
					break
				}
			}
			if length == 0 {
				return false
			}
			for i := 0; i < length && k < info.maxSfb; i++ {
				cd.sfbCb[g][k] = cb
				k++
			}
		}
	}
	return true
}

// scaleFactorData DPCM-decodes the scalefactors, intensity positions, and
// noise energies.
func (d *Decoder) scaleFactorData(r *BitReader, cd *channelData) bool {
	info := &cd.info
	scale := cd.globalGain
	isPos := 0
	noiseEnergy := cd.globalGain - 90
	firstNoise := true
	for g := 0; g < info.numWindowGroups; g++ {
		for sfb := 0; sfb < info.maxSfb; sfb++ {
			switch cb := cd.sfbCb[g][sfb]; {
			case cb == zeroHCB:
				cd.sf[g][sfb] = 0
			case cb == intensityHCB || cb == intensityHCB2:
				delta, err := decodeScalefactor(r)
				if err != nil {
					return false
				}
				isPos += delta
				cd.sf[g][sfb] = isPos
			case cb == noiseHCB:
				if firstNoise {
					firstNoise = false
					v, err := r.Read(9)
					if err != nil {
						return false
					}
					noiseEnergy += int(v) - 256
				} else {
					delta, err := decodeScalefactor(r)
					if err != nil {
						return false
					}
					noiseEnergy += delta
				}
				cd.sf[g][sfb] = noiseEnergy
			default:
				delta, err := decodeScalefactor(r)
				if err != nil {
					return false
				}
				scale += delta
				if scale < 0 || scale > 255 {
					return false
				}
				cd.sf[g][sfb] = scale
			}
		}
	}
	return true
}

// parsePulse reads pulse_data.
func parsePulse(r *BitReader, cd *channelData) bool {
	v, err := r.Read(2)
	if err != nil {
		return false
	}
	cd.pulse.numPulse = int(v) + 1
	start, err := r.Read(6)
	if err != nil {
		return false
	}
	cd.pulse.startSfb = int(start)
	for i := 0; i < cd.pulse.numPulse; i++ {
		off, err := r.Read(5)
		if err != nil {
			return false
		}
		amp, err := r.Read(4)
		if err != nil {
			return false
		}
		cd.pulse.offset[i] = int(off)
		cd.pulse.amp[i] = int(amp)
	}
	return true
}

// spectralData Huffman-decodes the quantized coefficients section by section.
func (d *Decoder) spectralData(r *BitReader, cd *channelData) bool {
	info := &cd.info
	gs := groupStarts(info)
	var tuple [4]int
	var pos [1024]int
	for g := 0; g < info.numWindowGroups; g++ {
		L := info.windowGroupLen[g]
		sfb := 0
		for sfb < info.maxSfb {
			cb := int(cd.sfbCb[g][sfb])
			end := sfb
			for end < info.maxSfb && int(cd.sfbCb[g][end]) == cb {
				end++
			}
			if cb == zeroHCB || cb == noiseHCB || cb == intensityHCB || cb == intensityHCB2 || cb >= 12 {
				sfb = end
				continue
			}
			n := 0
			for s := sfb; s < end; s++ {
				width := int(info.swb[s+1]) - int(info.swb[s])
				for w := 0; w < L; w++ {
					base := (gs[g]+w)*128 + int(info.swb[s])
					for k := 0; k < width; k++ {
						pos[n] = base + k
						n++
					}
				}
			}
			dim := hcbDim[cb-1]
			for read := 0; read < n; read += dim {
				if err := decodeSpectral(r, cb, tuple[:dim]); err != nil {
					return false
				}
				for t := 0; t < dim && read+t < n; t++ {
					cd.spec[pos[read+t]] = float64(tuple[t])
				}
			}
			sfb = end
		}
	}
	return true
}

// iqTable caches |q|^(4/3) for the common range of quantized magnitudes.
var iqTable [8192]float64

func init() {
	for i := range iqTable {
		iqTable[i] = float64(i) * math.Cbrt(float64(i))
	}
}

// iquant is the AAC inverse quantizer: sign(q) * |q|^(4/3).
func iquant(q float64) float64 {
	a := q
	if a < 0 {
		a = -a
	}
	var v float64
	if a < float64(len(iqTable)) {
		v = iqTable[int(a)]
	} else {
		v = a * math.Cbrt(a)
	}
	if q < 0 {
		return -v
	}
	return v
}

// dequant applies pulses, inverse quantization, and scalefactor gains.
func (d *Decoder) dequant(cd *channelData) {
	info := &cd.info
	if cd.hasPulse && info.windowSequence != eightShort {
		applyPulse(cd)
	}
	gs := groupStarts(info)
	for g := 0; g < info.numWindowGroups; g++ {
		for sfb := 0; sfb < info.maxSfb; sfb++ {
			cb := cd.sfbCb[g][sfb]
			if cb == zeroHCB || cb == noiseHCB || cb == intensityHCB || cb == intensityHCB2 || cb >= 12 {
				continue
			}
			gain := math.Exp2(0.25 * float64(cd.sf[g][sfb]-sfOffset))
			start, end := int(info.swb[sfb]), int(info.swb[sfb+1])
			for w := 0; w < info.windowGroupLen[g]; w++ {
				base := (gs[g] + w) * 128
				for k := start; k < end; k++ {
					cd.spec[base+k] = iquant(cd.spec[base+k]) * gain
				}
			}
		}
	}
}

// applyPulse adds the pulse escape to the raw quantized coefficients.
func applyPulse(cd *channelData) {
	p := &cd.pulse
	info := &cd.info
	if p.startSfb >= info.numSwb {
		return
	}
	k := int(info.swb[p.startSfb])
	for i := 0; i < p.numPulse; i++ {
		k += p.offset[i]
		if k >= 1024 {
			return
		}
		if cd.spec[k] > 0 {
			cd.spec[k] += float64(p.amp[i])
		} else {
			cd.spec[k] -= float64(p.amp[i])
		}
	}
}

func isIntensity(cb uint8) bool { return cb == intensityHCB || cb == intensityHCB2 }

// applyPNS fills perceptual-noise-substitution bands with scaled random
// values at the coded energy.
func (d *Decoder) applyPNS(cd *channelData) {
	info := &cd.info
	gs := groupStarts(info)
	for g := 0; g < info.numWindowGroups; g++ {
		for sfb := 0; sfb < info.maxSfb; sfb++ {
			if cd.sfbCb[g][sfb] != noiseHCB {
				continue
			}
			scale := math.Exp2(0.25 * float64(cd.sf[g][sfb]-sfOffset))
			start, end := int(info.swb[sfb]), int(info.swb[sfb+1])
			for w := 0; w < info.windowGroupLen[g]; w++ {
				base := (gs[g] + w) * 128
				for k := start; k < end; k++ {
					cd.spec[base+k] = d.pnsNext() * scale
				}
			}
		}
	}
}

// pnsNext returns the next PRNG sample in [-1, 1) (xorshift32).
func (d *Decoder) pnsNext() float64 {
	d.pnsState ^= d.pnsState << 13
	d.pnsState ^= d.pnsState >> 17
	d.pnsState ^= d.pnsState << 5
	return float64(int32(d.pnsState)) / 2147483648.0
}

// applyMS reverses M/S stereo per scalefactor band, skipping intensity and
// noise bands.
func applyMS(left, right *channelData, msMask int, msUsed *[maxWindowGroups][maxSFBCount]bool) {
	info := &left.info
	gs := groupStarts(info)
	for g := 0; g < info.numWindowGroups; g++ {
		for sfb := 0; sfb < info.maxSfb; sfb++ {
			on := msMask == 2 || (msMask == 1 && msUsed[g][sfb])
			if !on || left.sfbCb[g][sfb] >= noiseHCB || right.sfbCb[g][sfb] >= noiseHCB {
				continue
			}
			start, end := int(info.swb[sfb]), int(info.swb[sfb+1])
			for w := 0; w < info.windowGroupLen[g]; w++ {
				base := (gs[g] + w) * 128
				for k := start; k < end; k++ {
					m, s := left.spec[base+k], right.spec[base+k]
					left.spec[base+k] = m + s
					right.spec[base+k] = m - s
				}
			}
		}
	}
}

// applyIntensity fills the right channel's intensity bands by scaling the
// left channel's coefficients.
func applyIntensity(left, right *channelData, msMask int, msUsed *[maxWindowGroups][maxSFBCount]bool) {
	info := &right.info
	gs := groupStarts(info)
	for g := 0; g < info.numWindowGroups; g++ {
		for sfb := 0; sfb < info.maxSfb; sfb++ {
			cb := right.sfbCb[g][sfb]
			if !isIntensity(cb) {
				continue
			}
			scale := math.Exp2(-0.25 * float64(right.sf[g][sfb]))
			if cb == intensityHCB2 {
				scale = -scale
			}
			if msMask == 1 && msUsed[g][sfb] {
				scale = -scale
			}
			start, end := int(info.swb[sfb]), int(info.swb[sfb+1])
			for w := 0; w < info.windowGroupLen[g]; w++ {
				base := (gs[g] + w) * 128
				for k := start; k < end; k++ {
					right.spec[base+k] = left.spec[base+k] * scale
				}
			}
		}
	}
}

// parseTNS reads tns_data and converts coded reflection coefficients into
// synthesis LPC coefficients.
func parseTNS(r *BitReader, cd *channelData) bool {
	info := &cd.info
	t := &cd.tns
	nFiltBits, lengthBits, orderBits := uint(2), uint(6), uint(5)
	if info.windowSequence == eightShort {
		nFiltBits, lengthBits, orderBits = 1, 4, 3
	}
	for w := 0; w < info.numWindows; w++ {
		v, err := r.Read(int(nFiltBits))
		if err != nil {
			return false
		}
		nfilt := int(v)
		t.nFilt[w] = nfilt
		if nfilt == 0 {
			continue
		}
		coefRes, err := r.ReadBit()
		if err != nil {
			return false
		}
		if coefRes {
			t.coefRes[w] = 1
		} else {
			t.coefRes[w] = 0
		}
		for f := 0; f < nfilt && f < 4; f++ {
			filt := &t.filt[w][f]
			length, err := r.Read(int(lengthBits))
			if err != nil {
				return false
			}
			filt.length = int(length)
			rawOrder, err := r.Read(int(orderBits))
			if err != nil {
				return false
			}
			if rawOrder == 0 {
				filt.order = 0
				continue
			}
			dir, err := r.ReadBit()
			if err != nil {
				return false
			}
			if dir {
				filt.direction = 1
			}
			compress, err := r.ReadBit()
			if err != nil {
				return false
			}
			resBits := t.coefRes[w] + 3
			coefBits := resBits
			if compress {
				coefBits--
			}
			signMask := 1 << uint(coefBits-1)
			negMask := ^((1 << uint(coefBits)) - 1)
			iqfac := (float64(int(1)<<uint(resBits-1)) - 0.5) / (math.Pi / 2)
			iqfacM := (float64(int(1)<<uint(resBits-1)) + 0.5) / (math.Pi / 2)
			order := int(rawOrder)
			if order > maxTNSOrder {
				order = maxTNSOrder
			}
			filt.order = order
			var refl [maxTNSOrder]float64
			for i := 0; i < int(rawOrder); i++ {
				v, err := r.Read(coefBits)
				if err != nil {
					return false
				}
				if i >= maxTNSOrder {
					continue
				}
				c := int(v)
				if c&signMask != 0 {
					c |= negMask
				}
				fac := iqfac
				if c < 0 {
					fac = iqfacM
				}
				refl[i] = math.Sin(float64(c) / fac)
			}
			reflToLPC(refl[:filt.order], filt.coef[:filt.order])
		}
	}
	return true
}

// reflToLPC runs the Levinson step-up recursion, converting reflection
// coefficients into direct-form LPC coefficients a[1..order].
func reflToLPC(refl, a []float64) {
	order := len(refl)
	var lpc [maxTNSOrder + 1]float64
	lpc[0] = 1
	for m := 1; m <= order; m++ {
		var b [maxTNSOrder + 1]float64
		for i := 1; i < m; i++ {
			b[i] = lpc[i] + refl[m-1]*lpc[m-i]
		}
		for i := 1; i < m; i++ {
			lpc[i] = b[i]
		}
		lpc[m] = refl[m-1]
	}
	for i := 0; i < order; i++ {
		a[i] = lpc[i+1]
	}
}

// applyTNS runs the all-pole synthesis filters over the spectral coefficients.
func applyTNS(cd *channelData, rateIdx int) {
	info := &cd.info
	t := &cd.tns
	maxBands := tnsMaxBandsLong[rateIdx]
	if info.windowSequence == eightShort {
		maxBands = tnsMaxBandsShort[rateIdx]
	}
	for w := 0; w < info.numWindows; w++ {
		if t.nFilt[w] == 0 {
			continue
		}
		top := info.numSwb
		base := w * 128
		for f := 0; f < t.nFilt[w]; f++ {
			filt := &t.filt[w][f]
			bottom := top - filt.length
			if bottom < 0 {
				bottom = 0
			}
			if filt.order == 0 {
				top = bottom
				continue
			}
			lo := bottom
			if lo > maxBands {
				lo = maxBands
			}
			if lo > info.maxSfb {
				lo = info.maxSfb
			}
			hi := top
			if hi > maxBands {
				hi = maxBands
			}
			if hi > info.maxSfb {
				hi = info.maxSfb
			}
			start := int(info.swb[lo])
			end := int(info.swb[hi])
			size := end - start
			if size <= 0 {
				top = bottom
				continue
			}
			arFilter(cd.spec[base+start:base+end], size, filt)
			top = bottom
		}
	}
}

// arFilter applies one all-pole synthesis filter in place over spec.
func arFilter(spec []float64, size int, filt *tnsFilter) {
	inc := 1
	off := 0
	if filt.direction != 0 {
		inc = -1
		off = size - 1
	}
	var state [maxTNSOrder]float64
	pos := off
	for i := 0; i < size; i++ {
		y := spec[pos]
		for j := 0; j < filt.order; j++ {
			if i-1-j >= 0 {
				y -= filt.coef[j] * state[j]
			}
		}
		for j := filt.order - 1; j > 0; j-- {
			state[j] = state[j-1]
		}
		state[0] = y
		spec[pos] = y
		pos += inc
	}
}

// skipDSE skips a data_stream_element payload.
func skipDSE(r *BitReader) error {
	if _, err := r.Read(4); err != nil { // element_instance_tag
		return ErrMalformed
	}
	if _, err := r.ReadBit(); err != nil { // data_byte_alignment_flag
		return ErrMalformed
	}
	count, err := r.Read(8)
	if err != nil {
		return ErrMalformed
	}
	if count == 255 {
		esc, err := r.Read(8)
		if err != nil {
			return ErrMalformed
		}
		count += esc
	}
	if err := r.Skip(int(count) * 8); err != nil {
		return ErrMalformed
	}
	return nil
}

// skipPCE skips a program_config_element payload.
func skipPCE(r *BitReader) error {
	if _, err := r.Read(4); err != nil { // element_instance_tag
		return ErrMalformed
	}
	if _, err := r.Read(2); err != nil { // object_type
		return ErrMalformed
	}
	if _, err := r.Read(4); err != nil { // sampling_frequency_index
		return ErrMalformed
	}
	front, err := r.Read(4)
	if err != nil {
		return ErrMalformed
	}
	side, err := r.Read(4)
	if err != nil {
		return ErrMalformed
	}
	back, err := r.Read(4)
	if err != nil {
		return ErrMalformed
	}
	lfe, err := r.Read(2)
	if err != nil {
		return ErrMalformed
	}
	assoc, err := r.Read(3)
	if err != nil {
		return ErrMalformed
	}
	cc, err := r.Read(4)
	if err != nil {
		return ErrMalformed
	}
	if mono, err := r.ReadBit(); err != nil {
		return ErrMalformed
	} else if mono {
		if _, err := r.Read(4); err != nil {
			return ErrMalformed
		}
	}
	if stereo, err := r.ReadBit(); err != nil {
		return ErrMalformed
	} else if stereo {
		if _, err := r.Read(4); err != nil {
			return ErrMalformed
		}
	}
	if matrix, err := r.ReadBit(); err != nil {
		return ErrMalformed
	} else if matrix {
		if _, err := r.Read(2); err != nil {
			return ErrMalformed
		}
		if _, err := r.ReadBit(); err != nil {
			return ErrMalformed
		}
	}
	if err := skipTagSelects(r, int(front)); err != nil {
		return err
	}
	if err := skipTagSelects(r, int(side)); err != nil {
		return err
	}
	if err := skipTagSelects(r, int(back)); err != nil {
		return err
	}
	for i := 0; i < int(lfe); i++ {
		if _, err := r.Read(4); err != nil {
			return ErrMalformed
		}
	}
	for i := 0; i < int(assoc); i++ {
		if _, err := r.Read(4); err != nil {
			return ErrMalformed
		}
	}
	for i := 0; i < int(cc); i++ {
		if _, err := r.ReadBit(); err != nil { // cc_element_is_ind_sw
			return ErrMalformed
		}
		if _, err := r.Read(4); err != nil {
			return ErrMalformed
		}
	}
	r.Align()
	comment, err := r.Read(8)
	if err != nil {
		return ErrMalformed
	}
	return r.Skip(int(comment) * 8)
}

func skipTagSelects(r *BitReader, n int) error {
	for i := 0; i < n; i++ {
		if _, err := r.ReadBit(); err != nil { // is_cpe
			return ErrMalformed
		}
		if _, err := r.Read(4); err != nil { // tag_select
			return ErrMalformed
		}
	}
	return nil
}

// skipFIL skips a fill_element payload.
func skipFIL(r *BitReader) error {
	count, err := r.Read(4)
	if err != nil {
		return ErrMalformed
	}
	if count == 15 {
		esc, err := r.Read(8)
		if err != nil {
			return ErrMalformed
		}
		count += esc - 1
	}
	return r.Skip(int(count) * 8)
}
