package aac

// Window shape selectors from ics_info.window_shape.
const (
	shapeSine = 0
	shapeKBD  = 1
)

// Precomputed full-length synthesis windows: long (2048 samples) and short
// (256 samples), for each window shape. Both are symmetric: the left half
// rises with the half-window and the right half falls with its mirror, which
// is the Princen-Bradley form the overlap-add step relies on.
var (
	longWindow  [2][2048]float64
	shortWindow [2][256]float64
)

func init() {
	copy(longWindow[shapeSine][:], SineWindow(2048))
	copy(shortWindow[shapeSine][:], SineWindow(256))
	copy(longWindow[shapeKBD][:], KBDWindow(2048, 4))
	copy(shortWindow[shapeKBD][:], KBDWindow(256, 6))
}

// finishChannel runs the TNS synthesis filter (when present) and the
// synthesis filterbank for one channel, overlap-adding the resulting 1024
// samples onto the previous frame. Samples are left at integer-PCM scale;
// the caller converts to the output sample format.
func (d *Decoder) finishChannel(cd *channelData, outCh int, out []float64) {
	if cd.hasTNS {
		applyTNS(cd, d.rateIdx)
	}
	info := &cd.info
	curShape := info.windowShape
	prevShape := d.prevWin[outCh]
	var cur [2048]float64
	if info.windowSequence == eightShort {
		shortFilterbank(cd, prevShape, curShape, &cur)
	} else {
		var z [2048]float64
		_ = IMDCT(z[:], cd.spec[:1024])
		longWindowApply(&z, &cur, info.windowSequence, prevShape, curShape)
	}
	ov := &d.overlap[outCh]
	base := outCh * 1024
	for i := 0; i < 1024; i++ {
		out[base+i] = cur[i] + ov[i]
		ov[i] = cur[1024+i]
	}
	d.prevWin[outCh] = curShape
}

// longWindowApply windows a 2048-sample long IMDCT output. The left half uses
// the previous frame's window shape and the right half the current one.
// LONG_START tapers into the short-block region and LONG_STOP tapers out of
// it; both place the short-shaped section at the 448/128/448 boundaries that
// match the short filterbank's offsets.
func longWindowApply(z, cur *[2048]float64, seq, prevShape, curShape int) {
	if seq == longStop {
		// Left half: flat zero, a rising short half, then flat unity.
		for n := 0; n < 448; n++ {
			cur[n] = 0
		}
		wl := &shortWindow[prevShape]
		for n := 0; n < 128; n++ {
			cur[448+n] = z[448+n] * wl[n]
		}
		for n := 576; n < 1024; n++ {
			cur[n] = z[n]
		}
	} else {
		wl := &longWindow[prevShape]
		for n := 0; n < 1024; n++ {
			cur[n] = z[n] * wl[n]
		}
	}
	if seq == longStart {
		// Right half: flat unity, a falling short half, then flat zero.
		for n := 1024; n < 1472; n++ {
			cur[n] = z[n]
		}
		wr := &shortWindow[curShape]
		for n := 0; n < 128; n++ {
			cur[1472+n] = z[1472+n] * wr[128+n]
		}
		for n := 1600; n < 2048; n++ {
			cur[n] = 0
		}
	} else {
		wr := &longWindow[curShape]
		for n := 1024; n < 2048; n++ {
			cur[n] = z[n] * wr[n]
		}
	}
}

// shortFilterbank runs the eight short IMDCTs, windows each 256-sample block
// with the short window, and overlap-adds them into the 2048-sample frame at
// 128-sample hops starting at offset 448. The first block's left half uses
// the previous frame's window shape.
func shortFilterbank(cd *channelData, prevShape, curShape int, cur *[2048]float64) {
	*cur = [2048]float64{}
	for i := 0; i < 8; i++ {
		var z [256]float64
		_ = IMDCT(z[:], cd.spec[i*128:i*128+128])
		lShape := curShape
		if i == 0 {
			lShape = prevShape
		}
		wl := &shortWindow[lShape]
		wr := &shortWindow[curShape]
		off := 448 + i*128
		for n := 0; n < 128; n++ {
			cur[off+n] += z[n] * wl[n]
			cur[off+128+n] += z[128+n] * wr[128+n]
		}
	}
}
