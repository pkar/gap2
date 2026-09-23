package aac

import (
	"math"
	"testing"
)

func princenBradley(t *testing.T, name string, w []float64) {
	t.Helper()
	n := len(w)
	half := n / 2
	for i := 0; i < half; i++ {
		v := w[i]*w[i] + w[i+half]*w[i+half]
		if math.Abs(v-1) > 1e-6 {
			t.Fatalf("%s: Princen-Bradley violated at %d: %.9f", name, i, v)
		}
	}
}

func TestSineWindow(t *testing.T) {
	for _, n := range []int{256, 2048} {
		w := SineWindow(n)
		if len(w) != n {
			t.Fatalf("len = %d, want %d", len(w), n)
		}
		princenBradley(t, "sine", w)
		scale := math.Pi / float64(n)
		for i, v := range w {
			want := math.Sin(scale * (float64(i) + 0.5))
			if math.Abs(v-want) > 1e-12 {
				t.Fatalf("sine[%d] = %.12f, want %.12f", i, v, want)
			}
		}
	}
}

func TestKBDWindow(t *testing.T) {
	for _, tc := range []struct {
		n     int
		alpha float64
	}{
		{2048, 4},
		{256, 6},
	} {
		w := KBDWindow(tc.n, tc.alpha)
		if len(w) != tc.n {
			t.Fatalf("len = %d, want %d", len(w), tc.n)
		}
		princenBradley(t, "kbd", w)
		for i := 0; i < tc.n; i++ {
			if w[i] < 0 || w[i] > 1 {
				t.Fatalf("kbd[%d] = %f out of range", i, w[i])
			}
			if math.Abs(w[i]-w[tc.n-1-i]) > 1e-12 {
				t.Fatalf("kbd not symmetric at %d", i)
			}
		}
	}
}
