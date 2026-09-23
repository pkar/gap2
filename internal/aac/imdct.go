package aac

import (
	"errors"
	"math"
)

// ErrIMDCTShape is returned when IMDCT's source and destination lengths are
// inconsistent.
var ErrIMDCTShape = errors.New("aac: imdct dst length must be twice src length")

// IMDCT computes the inverse modified discrete cosine transform used by the
// AAC synthesis filterbank. src holds n spectral coefficients for a block of
// length 2n; dst receives the 2n time-domain samples.
//
// The direct formulation below is a deliberately simple, allocation-free
// reference implementation. It is O(n²) and therefore suitable for tests and
// low-rate buffered decoding; a radix-2 FFT implementation can replace the
// inner summation without changing this API.
func IMDCT(dst, src []float64) error {
	n := len(src)
	if n == 0 || len(dst) != 2*n {
		return ErrIMDCTShape
	}

	// n0 = (N/2 + 1) / 2 where N = 2n.
	const scale = 2.0
	n0 := (float64(n) + 1.0) / 2.0
	for i := range dst {
		var sum float64
		arg := math.Pi / float64(n) * (float64(i) + n0)
		for k, x := range src {
			sum += x * math.Cos(arg*(float64(k)+0.5))
		}
		dst[i] = scale / float64(2*n) * sum
	}
	return nil
}
