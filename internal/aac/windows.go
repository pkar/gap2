package aac

import "math"

// SineWindow returns the AAC sine window of length n:
//
//	w(i) = sin(pi/n * (i + 1/2)), i = 0..n-1
//
// It satisfies the Princen-Bradley condition required for overlap-add
// reconstruction in the synthesis filterbank.
func SineWindow(n int) []float64 {
	w := make([]float64, n)
	scale := math.Pi / float64(n)
	for i := range w {
		w[i] = math.Sin(scale * (float64(i) + 0.5))
	}
	return w
}

// KBDWindow returns the AAC Kaiser-Bessel derived window of length n with the
// given alpha. alpha is 4 for the long (n=2048) window and 6 for the short
// (n=256) window in AAC. The returned full-length window satisfies the
// Princen-Bradley condition w(i)^2 + w(i+n/2)^2 = 1.
func KBDWindow(n int, alpha float64) []float64 {
	half := n / 2
	if half < 1 {
		return nil
	}

	// Kaiser kernel over half+1 points (ISO 14496-3 4.6.14.4.1.1).
	kernel := make([]float64, half+1)
	for i := 0; i <= half; i++ {
		x := 2*float64(i)/float64(half) - 1
		kernel[i] = besselI0(math.Pi * alpha * math.Sqrt(1-x*x))
	}
	var total float64
	for _, v := range kernel {
		total += v
	}

	halfWin := make([]float64, half)
	var running float64
	for i := 0; i < half; i++ {
		running += kernel[i]
		halfWin[i] = math.Sqrt(running / total)
	}

	w := make([]float64, n)
	copy(w[:half], halfWin)
	for i := 0; i < half; i++ {
		w[half+i] = halfWin[half-1-i]
	}
	return w
}

// besselI0 computes the modified Bessel function of the first kind, order
// zero, using its series expansion with enough terms for the AAC window
// argument range.
func besselI0(x float64) float64 {
	term := 1.0
	sum := 1.0
	x2 := (x / 2) * (x / 2)
	for k := 1; k < 200; k++ {
		term *= x2 / float64(k*k)
		sum += term
		if term < 1e-15*sum {
			break
		}
	}
	return sum
}
