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

	// Kaiser kernel over half+1 points, matching the AAC KBD derivation.
	alpha2 := 4 * math.Pow(alpha*math.Pi/float64(n), 2)
	temp := make([]float64, half/2+1)
	var denom float64
	for i := range temp {
		temp[i] = besselI0(math.Sqrt(float64(i*(n-i)) * alpha2))
		weight := 1.0
		if i != 0 && i < half/2 {
			weight = 2.0
		}
		denom += temp[i] * weight
	}
	scale := 1.0 / (denom + 1.0)

	halfWin := make([]float64, half)
	var sum float64
	i := 0
	for ; i <= half/2; i++ {
		sum += temp[i]
		if i < half {
			halfWin[i] = math.Sqrt(sum * scale)
		}
	}
	for ; i < half; i++ {
		sum += temp[half-i]
		halfWin[i] = math.Sqrt(sum * scale)
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
