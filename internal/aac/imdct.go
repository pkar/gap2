package aac

import (
	"errors"
	"math"
	"sync"
)

// ErrIMDCTShape is returned when IMDCT's source and destination lengths are
// inconsistent.
var ErrIMDCTShape = errors.New("aac: imdct dst length must be twice src length")

// IMDCT computes the inverse modified discrete cosine transform used by the
// AAC synthesis filterbank. src holds n spectral coefficients for a block of
// length 2n; dst receives the 2n time-domain samples:
//
//	dst[i] = 1/n * sum_k src[k] * cos(pi/n * (i + n/2 + 1/2) * (k + 1/2))
//
// Power-of-two blocks compute the underlying DCT-IV with an n/2-point complex
// FFT in O(n log n). Other sizes retain the direct formulation.
func IMDCT(dst, src []float64) error {
	n := len(src)
	if n == 0 || len(dst) != 2*n {
		return ErrIMDCTShape
	}
	if n >= 2 && n&(n-1) == 0 {
		imdctFFT(dst, src, planFor(n))
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

// imdctPlan holds the precomputed tables for one power-of-two IMDCT size.
type imdctPlan struct {
	n       int
	twiddle []complex128 // exp(-i*pi*(t + 1/8) / n), t < n/2
	fftTw   []complex128 // exp(-2*pi*i*k / (n/2)), k < n/4
	rev     []int32      // bit-reversal permutation of n/2 points
}

var (
	// AAC uses only the long (1024) and short (128) sizes; build those once.
	plan1024 = newIMDCTPlan(1024)
	plan128  = newIMDCTPlan(128)
	plans    sync.Map // other power-of-two sizes, built on demand
)

func planFor(n int) *imdctPlan {
	switch n {
	case 1024:
		return plan1024
	case 128:
		return plan128
	}
	if p, ok := plans.Load(n); ok {
		return p.(*imdctPlan)
	}
	p, _ := plans.LoadOrStore(n, newIMDCTPlan(n))
	return p.(*imdctPlan)
}

func newIMDCTPlan(n int) *imdctPlan {
	m := n / 2
	p := &imdctPlan{
		n:       n,
		twiddle: make([]complex128, m),
		fftTw:   make([]complex128, m/2),
		rev:     make([]int32, m),
	}
	for t := range p.twiddle {
		s, c := math.Sincos(-math.Pi * (float64(t) + 0.125) / float64(n))
		p.twiddle[t] = complex(c, s)
	}
	for k := range p.fftTw {
		s, c := math.Sincos(-2 * math.Pi * float64(k) / float64(m))
		p.fftTw[k] = complex(c, s)
	}
	bits := 0
	for 1<<bits < m {
		bits++
	}
	for i := range p.rev {
		r := 0
		for b := 0; b < bits; b++ {
			r |= (i >> b & 1) << (bits - 1 - b)
		}
		p.rev[i] = int32(r)
	}
	return p
}

// imdctFFT evaluates the IMDCT through a DCT-IV of size n,
//
//	u[j] = sum_k src[k] * cos(pi/n * (j + 1/2) * (k + 1/2)),
//
// computed by pairing even and reversed odd inputs into n/2 complex points,
// twiddling by exp(-i*pi*(t + 1/8)/n) before and after a forward FFT. Then
// u[2p] = Re(c[p]) and u[n-1-2p] = -Im(c[p]). The IMDCT output is u
// unfolded with odd symmetry: dst[i] = u[i+n/2] for i < n/2,
// -u[3n/2-1-i] for n/2 <= i < 3n/2, and -u[i-3n/2] above that.
func imdctFFT(dst, src []float64, p *imdctPlan) {
	n := p.n
	m := n / 2
	var buf [512]complex128 // n/2 for AAC's long block; stays on the stack
	var z []complex128
	if m <= len(buf) {
		z = buf[:m]
	} else {
		z = make([]complex128, m)
	}
	// Pre-twiddle, storing in bit-reversed order for the in-place FFT.
	for t := 0; t < m; t++ {
		z[p.rev[t]] = complex(src[2*t], src[n-1-2*t]) * p.twiddle[t]
	}
	for size := 2; size <= m; size <<= 1 {
		half := size / 2
		stride := m / size
		for base := 0; base < m; base += size {
			for j := 0; j < half; j++ {
				a := z[base+j]
				b := z[base+j+half] * p.fftTw[j*stride]
				z[base+j] = a + b
				z[base+j+half] = a - b
			}
		}
	}
	inv := 1 / float64(n)
	put := func(j int, v float64) {
		v *= inv
		dst[3*n/2-1-j] = -v
		if j >= n/2 {
			dst[j-n/2] = v
		} else {
			dst[j+3*n/2] = -v
		}
	}
	for q := 0; q < m; q++ {
		c := z[q] * p.twiddle[q]
		put(2*q, real(c))
		put(n-1-2*q, -imag(c))
	}
}
