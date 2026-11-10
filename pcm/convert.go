package pcm

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"
)

// Converter keeps the output device format fixed across source format changes.
// S16LE channels use speaker order L,R,C,LFE,BL,BR,SL,SR for 7.1 and
// L,R,C,LFE,BL,BR for 5.1. Three/four/five channels omit LFE; four has BC.
// Eight-channel wide layouts use the last pair for FLC/FRC instead of SL/SR.
// Stereo downmix includes center/surround at -3 dB, omits LFE, and normalizes
// each row to prevent clipping. Mono duplicates to L/R; upmix adds silence.
type Converter struct {
	mu                sync.Mutex
	sink              Sink
	output, input     Format
	data              []float64
	base, total, next int64
	// kernel holds the resampling filter, 1024 phases of 64 taps. It is half
	// a megabyte, so it is built only when the rates differ.
	kernel *[1024][64]float64
	// mixed and resampled are scratch buffers reused across writes. They
	// never leave the converter.
	mixed, resampled []float64
	closed           bool
}

func NewConverter(sink Sink, output Format) (*Converter, error) {
	if err := conversionFormat(output); err != nil {
		return nil, err
	}
	if sink == nil {
		return nil, fmt.Errorf("pcm: nil conversion sink")
	}
	return &Converter{sink: sink, output: output}, nil
}

// SetVolumeDB forwards output gain without quantizing samples before conversion.
func (c *Converter) SetVolumeDB(db float64) bool {
	if sink, ok := c.sink.(VolumeSink); ok {
		return sink.SetVolumeDB(db)
	}
	return false
}

func conversionFormat(f Format) error {
	if f.Format != S16LE || f.Rate < 8000 || f.Rate > 192000 || f.Channels < 1 || f.Channels > 8 {
		return fmt.Errorf("pcm: unsupported conversion format %s", f)
	}
	return nil
}

func (c *Converter) reset(f Format) {
	c.input = f
	c.data = nil
	c.base = 0
	c.total = 0
	c.next = 0
	if f.Rate == c.output.Rate {
		return
	}
	if c.kernel == nil {
		c.kernel = new([1024][64]float64)
	}
	cutoff := math.Min(1, float64(c.output.Rate)/float64(f.Rate)) * 0.94
	for p := range c.kernel {
		sum := 0.0
		for j := range c.kernel[p] {
			x := float64(j-31) - float64(p)/1024
			v := cutoff
			if x != 0 {
				v = math.Sin(math.Pi*cutoff*x) / (math.Pi * x)
			}
			v *= 0.42 + 0.5*math.Cos(math.Pi*x/32) + 0.08*math.Cos(2*math.Pi*x/32)
			c.kernel[p][j] = v
			sum += v
		}
		for j := range c.kernel[p] {
			c.kernel[p][j] /= sum
		}
	}
}

func (c *Converter) Write(ctx context.Context, b Block) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conversionFormat(b.Format); err != nil {
		return err
	}
	if len(b.Data)%b.Format.BytesPerFrame() != 0 {
		return fmt.Errorf("pcm: incomplete conversion frame")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("pcm: converter closed")
	}
	if b.Format != c.input {
		if c.input.Rate != 0 {
			if err := c.emit(ctx, true); err != nil {
				return err
			}
		}
		c.reset(b.Format)
	}
	if b.Format == c.output {
		return c.sink.Write(ctx, b)
	}
	c.mixed = mix(c.mixed[:0], b, c.output.Channels)
	mixed := c.mixed
	if b.Format.Rate == c.output.Rate {
		return c.writeSamples(ctx, mixed)
	}
	c.data = append(c.data, mixed...)
	c.total += int64(b.Frames())
	return c.emit(ctx, false)
}

// invSqrt2 is the -3 dB gain applied to center and surround channels.
const invSqrt2 = math.Sqrt2 / 2

// mix converts b to float samples in the given channel count, reusing dst's
// storage when it is large enough.
func mix(dst []float64, b Block, channels int) []float64 {
	n := b.Format.Channels
	size := b.Frames() * channels
	if cap(dst) < size {
		dst = make([]float64, size)
	}
	out := dst[:size]
	if n == channels {
		// Rate conversion only: the common stereo-to-stereo case.
		for i := range out {
			out[i] = float64(int16(binary.LittleEndian.Uint16(b.Data[2*i:])))
		}
		return out
	}
	clear(out) // upmixing leaves the added channels silent
	var mapping [8]int
	if channels > 2 && n > 1 && n != channels {
		sources := speakerPositions(n)
		for dst, d := range speakerPositions(channels) {
			mapping[dst] = -1
			for src, s := range sources {
				if d == s {
					mapping[dst] = src
					break
				}
			}
		}
	}
	for i := 0; i < b.Frames(); i++ {
		var v [8]float64
		for j := 0; j < n; j++ {
			v[j] = float64(int16(binary.LittleEndian.Uint16(b.Data[(i*n+j)*2:])))
		}
		if channels <= 2 {
			l, r := v[0], v[0]
			norm := 1.0
			if n > 1 {
				r = v[1]
			}
			if n > 2 {
				l += v[2] * invSqrt2
				r += v[2] * invSqrt2
				norm += invSqrt2
			}
			if n == 4 {
				l += v[3] * 0.5
				r += v[3] * 0.5
				norm += 0.5
			}
			if n == 5 {
				l += v[3] * invSqrt2
				r += v[4] * invSqrt2
				norm += invSqrt2
			}
			if n >= 6 {
				l += v[4] * invSqrt2
				r += v[5] * invSqrt2
				norm += invSqrt2
			}
			if n == 7 {
				l += v[6] * 0.5
				r += v[6] * 0.5
				norm += 0.5
			}
			if n == 8 {
				l += v[6] * invSqrt2
				r += v[7] * invSqrt2
				norm += invSqrt2
			}
			if channels == 1 {
				out[i] = (l + r) / (2 * norm)
			} else {
				out[i*2] = l / norm
				out[i*2+1] = r / norm
			}
		} else if n == 1 {
			out[i*channels] = v[0]
			out[i*channels+1] = v[0]
		} else {
			// Map named positions when layouts gain or lose an LFE slot.
			for dst := 0; dst < channels; dst++ {
				if src := mapping[dst]; src >= 0 {
					out[i*channels+dst] = v[src]
				}
			}
		}
	}
	return out
}

func speakerPositions(count int) []int {
	switch count {
	case 2:
		return []int{0, 1}
	case 3:
		return []int{0, 1, 2}
	case 4:
		return []int{0, 1, 2, 8}
	case 5:
		return []int{0, 1, 2, 4, 5}
	case 7:
		return []int{0, 1, 2, 3, 4, 5, 8}
	default:
		return []int{0, 1, 2, 3, 4, 5, 6, 7}[:count]
	}
}

func (c *Converter) emit(ctx context.Context, tail bool) error {
	if len(c.data) == 0 {
		return nil
	}
	n := c.output.Channels
	inRate, outRate := int64(c.input.Rate), int64(c.output.Rate)
	out := c.resampled[:0]
	for {
		center := c.next / outRate
		if center >= c.total || (!tail && center+32 >= c.total) {
			break
		}
		kernel := &c.kernel[(c.next%outRate)*1024/outRate]
		if first := center - 31; first >= 0 && center+32 < c.total {
			// All 64 taps fall inside the buffered samples, which is every
			// output sample except those at the very start or end of a
			// stream, so the edge clamping below is not needed.
			taps := c.data[(first-c.base)*int64(n):][:64*n]
			if n == 2 {
				var l, r float64
				for j, w := range kernel {
					l += taps[2*j] * w
					r += taps[2*j+1] * w
				}
				out = append(out, l, r)
			} else {
				for ch := 0; ch < n; ch++ {
					sum := 0.0
					for j, w := range kernel {
						sum += taps[j*n+ch] * w
					}
					out = append(out, sum)
				}
			}
		} else {
			for ch := 0; ch < n; ch++ {
				sum := 0.0
				for j, w := range kernel {
					index := center + int64(j) - 31
					if index < 0 {
						index = 0
					}
					if index >= c.total {
						index = c.total - 1
					}
					sum += c.data[(index-c.base)*int64(n)+int64(ch)] * w
				}
				out = append(out, sum)
			}
		}
		c.next += inRate
	}
	c.resampled = out
	keep := c.next/outRate - 32
	if keep > c.base {
		if keep > c.total {
			keep = c.total
		}
		drop := int(keep-c.base) * n
		copy(c.data, c.data[drop:])
		c.data = c.data[:len(c.data)-drop]
		c.base = keep
	}
	return c.writeSamples(ctx, out)
}

func (c *Converter) writeSamples(ctx context.Context, samples []float64) error {
	if len(samples) == 0 {
		return nil
	}
	b := Block{Format: c.output, Data: make([]byte, len(samples)*2)}
	for i, v := range samples {
		v = math.Max(-32768, math.Min(32767, math.Round(v)))
		binary.LittleEndian.PutUint16(b.Data[2*i:], uint16(int16(v)))
	}
	return c.sink.Write(ctx, b)
}
func (c *Converter) Flush(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("pcm: converter closed")
	}
	c.data = nil
	c.base = 0
	c.total = 0
	c.next = 0
	return c.sink.Flush(ctx)
}
func (c *Converter) Position() Position {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.sink.Position()
	if c.input.Rate > 0 && c.input.Rate != c.output.Rate {
		p.Latency += time.Duration(32) * time.Second / time.Duration(c.input.Rate)
	}
	return p
}
func (c *Converter) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.sink.Close()
}
