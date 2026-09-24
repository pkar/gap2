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
// Power-of-two blocks use a sparse inverse FFT in O(n log n). Other sizes
// retain the direct formulation.
func IMDCT(dst, src []float64) error {
	n := len(src)
	if n == 0 || len(dst) != 2*n {
		return ErrIMDCTShape
	}
	if n&(n-1) == 0 {
		// cos(pi/n * (i+n/2+1/2) * (k+1/2)) is the real part
		// of bin (2*i+n+1) in an 8*n inverse DFT with src[k]
		// at odd input index (2*k+1). No FFT normalization is applied.
		x := make([]complex128, 8*n)
		for k, v := range src {
			x[2*k+1] = complex(v, 0)
		}
		inverseFFT(x)
		for i := range dst {
			dst[i] = real(x[2*i+n+1]) / float64(n)
		}
		return nil
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

func inverseFFT(x []complex128) {
	for i, j := 1, 0; i < len(x); i++ {
		bit := len(x) >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= len(x); size <<= 1 {
		s, c := math.Sincos(2 * math.Pi / float64(size))
		step := complex(c, s)
		for base := 0; base < len(x); base += size {
			w := complex(1, 0)
			for j := 0; j < size/2; j++ {
				a, b := x[base+j], w*x[base+j+size/2]
				x[base+j], x[base+j+size/2] = a+b, a-b
				w *= step
			}
		}
	}
}
