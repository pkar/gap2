package aac

import (
	"fmt"
	"math"
	"testing"
)

// forwardMDCT is the analysis-side MDCT, used only to verify IMDCT through
// time-domain aliasing cancellation. The factor 2 mirrors the AAC analysis
// filterbank so that the synthesis IMDCT (2/N) round-trips.
func forwardMDCT(x []float64) []float64 {
	n := len(x) / 2
	out := make([]float64, n)
	n0 := (float64(n) + 1.0) / 2.0
	for k := range out {
		arg := math.Pi / float64(n) * (float64(k) + 0.5)
		var sum float64
		for i, v := range x {
			sum += v * math.Cos(arg*(float64(i)+n0))
		}
		out[k] = 2 * sum
	}
	return out
}

func TestIMDCTShape(t *testing.T) {
	if err := IMDCT(make([]float64, 3), make([]float64, 2)); err != ErrIMDCTShape {
		t.Fatalf("err = %v, want ErrIMDCTShape", err)
	}
	if err := IMDCT(nil, nil); err != ErrIMDCTShape {
		t.Fatalf("empty err = %v, want ErrIMDCTShape", err)
	}
}

func TestFFTMatchesDirectIMDCT(t *testing.T) {
	for _, n := range []int{128, 1024} {
		src, got := make([]float64, n), make([]float64, 2*n)
		for k := range src {
			src[k] = math.Sin(float64(k)*0.37) * 100
		}
		if err := IMDCT(got, src); err != nil {
			t.Fatal(err)
		}
		for _, i := range []int{0, 1, n / 2, n - 1, n, 2*n - 1} {
			var want float64
			for k, x := range src {
				want += x * math.Cos(math.Pi/float64(n)*(float64(i)+float64(n+1)/2)*(float64(k)+0.5)) / float64(n)
			}
			if math.Abs(got[i]-want) > 1e-9 {
				t.Fatalf("n=%d sample=%d: got %g want %g", n, i, got[i], want)
			}
		}
	}
}

func TestIMDCTPerfectReconstruction(t *testing.T) {
	for _, n := range []int{4, 8, 16, 64, 256} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			blockLen := 2 * n
			signal := make([]float64, 2*blockLen)
			for i := range signal {
				signal[i] = math.Sin(float64(i)*0.37) + 0.5*math.Cos(float64(i)*0.11)
			}

			window := make([]float64, blockLen)
			for i := range window {
				window[i] = math.Sin(math.Pi / float64(blockLen) * (float64(i) + 0.5))
			}

			// Two consecutive frames overlap by half a block.
			first := make([]float64, blockLen)
			second := make([]float64, blockLen)
			for i := 0; i < blockLen; i++ {
				first[i] = signal[i] * window[i]
				second[i] = signal[n+i] * window[i]
			}

			imdct0 := make([]float64, blockLen)
			imdct1 := make([]float64, blockLen)
			if err := IMDCT(imdct0, forwardMDCT(first)); err != nil {
				t.Fatal(err)
			}
			if err := IMDCT(imdct1, forwardMDCT(second)); err != nil {
				t.Fatal(err)
			}

			for i := 0; i < n; i++ {
				// Both analysis and synthesis use the same window; the
				// overlap-add of the two synthesis-windowed halves cancels
				// time-domain aliasing and reconstructs the original.
				got := imdct0[n+i]*window[n+i] + imdct1[i]*window[i]
				want := signal[n+i]
				if math.Abs(got-want) > 1e-9 {
					t.Fatalf("sample %d: got %v want %v", i, got, want)
				}
			}
		})
	}
}
