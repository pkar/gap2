package pcm

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
)

type discardSink struct{}

func (discardSink) Write(context.Context, Block) error { return nil }
func (discardSink) Flush(context.Context) error        { return nil }
func (discardSink) Position() Position                 { return Position{} }
func (discardSink) Close() error                       { return nil }

// TestResamplingMatchesReference compares the converter with a direct
// evaluation of the same filter that clamps every tap index, across channel
// counts and across a flush, so the unclamped inner loop and the reused
// scratch buffers cannot drift from it.
func TestResamplingMatchesReference(t *testing.T) {
	for _, tc := range []struct{ in, out, channels, chunk int }{
		{44100, 48000, 2, 352}, {44100, 48000, 2, 1024}, {48000, 44100, 1, 100}, {44100, 48000, 6, 1024}, {96000, 48000, 2, 7},
	} {
		in := Format{tc.in, tc.channels, S16LE}
		src := tone(tc.in, tc.channels, 5000, 997)
		for i := 0; i < src.Frames(); i++ { // make the channels differ
			for ch := 1; ch < tc.channels; ch++ {
				v := int16(binary.LittleEndian.Uint16(src.Data[(i*tc.channels+ch)*2:]))
				binary.LittleEndian.PutUint16(src.Data[(i*tc.channels+ch)*2:], uint16(v/int16(ch+1)))
			}
		}
		sink := &conversionSink{}
		c, _ := NewConverter(sink, Format{tc.out, tc.channels, S16LE})
		ctx := context.Background()
		// A first pass and a flush leave used scratch buffers behind.
		if err := c.Write(ctx, tone(tc.in, tc.channels, 3000, 440)); err != nil {
			t.Fatal(err)
		}
		if err := c.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		bpf := in.BytesPerFrame()
		for pos := 0; pos < src.Frames(); pos += tc.chunk {
			end := min(pos+tc.chunk, src.Frames())
			if err := c.Write(ctx, Block{in, src.Data[pos*bpf : end*bpf]}); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.emit(ctx, true); err != nil {
			t.Fatal(err)
		}

		sample := func(frame, ch int) float64 {
			frame = max(0, min(src.Frames()-1, frame))
			return float64(int16(binary.LittleEndian.Uint16(src.Data[(frame*tc.channels+ch)*2:])))
		}
		frames := len(sink.data) / (2 * tc.channels)
		if want := int(math.Ceil(float64(src.Frames()) * float64(tc.out) / float64(tc.in))); frames != want {
			t.Fatalf("%+v: %d frames, want %d", tc, frames, want)
		}
		for i := 0; i < frames; i++ {
			position := int64(i) * int64(tc.in)
			center := int(position / int64(tc.out))
			kernel := &c.kernel[(position%int64(tc.out))*1024/int64(tc.out)]
			for ch := 0; ch < tc.channels; ch++ {
				sum := 0.0
				for j, w := range kernel {
					sum += sample(center+j-31, ch) * w
				}
				want := math.Max(-32768, math.Min(32767, math.Round(sum)))
				got := float64(int16(binary.LittleEndian.Uint16(sink.data[(i*tc.channels+ch)*2:])))
				if math.Abs(got-want) > 1 {
					t.Fatalf("%+v: frame %d channel %d = %v, want %v", tc, i, ch, got, want)
				}
			}
		}
	}
}

// A converter that never resamples must not carry the filter table.
func TestPassthroughBuildsNoKernel(t *testing.T) {
	c, _ := NewConverter(discardSink{}, Format{48000, 2, S16LE})
	for _, in := range []Format{{48000, 2, S16LE}, {48000, 6, S16LE}} {
		if err := c.Write(context.Background(), tone(in.Rate, in.Channels, 1024, 997)); err != nil {
			t.Fatal(err)
		}
	}
	if c.kernel != nil {
		t.Fatal("built a resampling kernel without a rate change")
	}
}

func benchmarkConvert(b *testing.B, in, out Format, frames int) {
	c, _ := NewConverter(discardSink{}, out)
	block := tone(in.Rate, in.Channels, frames, 997)
	ctx := context.Background()
	b.SetBytes(int64(len(block.Data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.Write(ctx, block); err != nil {
			b.Fatal(err)
		}
	}
}

// One AAC access unit, 44.1 kHz stereo to a 48 kHz device.
func BenchmarkResampleStereoAAC(b *testing.B) {
	benchmarkConvert(b, Format{44100, 2, S16LE}, Format{48000, 2, S16LE}, 1024)
}

// One realtime ALAC packet, 44.1 kHz stereo to a 48 kHz device.
func BenchmarkResampleStereoALAC(b *testing.B) {
	benchmarkConvert(b, Format{44100, 2, S16LE}, Format{48000, 2, S16LE}, 352)
}

func BenchmarkDownmixSurround(b *testing.B) {
	benchmarkConvert(b, Format{48000, 6, S16LE}, Format{48000, 2, S16LE}, 1024)
}
