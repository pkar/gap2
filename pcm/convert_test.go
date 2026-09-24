package pcm

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
)

type conversionSink struct {
	data    []byte
	formats []Format
	closed  int
}

func (s *conversionSink) Write(_ context.Context, b Block) error {
	s.data = append(s.data, b.Data...)
	s.formats = append(s.formats, b.Format)
	return nil
}
func (s *conversionSink) Flush(context.Context) error { s.data = nil; return nil }
func (s *conversionSink) Position() Position          { return Position{} }
func (s *conversionSink) Close() error                { s.closed++; return nil }
func tone(rate, channels, frames int, freq float64) Block {
	b, _ := NewBlock(Format{rate, channels, S16LE}, frames)
	for i := 0; i < frames; i++ {
		for ch := 0; ch < channels; ch++ {
			binary.LittleEndian.PutUint16(b.Data[(i*channels+ch)*2:], uint16(int16(12000*math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))))
		}
	}
	return b
}
func TestResamplingChunkInvariantAndCount(t *testing.T) {
	for _, rates := range [][2]int{{44100, 48000}, {48000, 44100}, {96000, 48000}, {32000, 48000}} {
		b := tone(rates[0], 2, rates[0], 997)
		var outputs [][]byte
		for _, chunk := range []int{1, 137, 1024, rates[0]} {
			sink := &conversionSink{}
			c, _ := NewConverter(sink, Format{rates[1], 2, S16LE})
			for pos := 0; pos < b.Frames(); pos += chunk {
				end := min(pos+chunk, b.Frames())
				if err := c.Write(context.Background(), Block{b.Format, b.Data[pos*4 : end*4]}); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.emit(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if len(sink.data) != rates[1]*4 {
				t.Fatalf("%v: got %d frames", rates, len(sink.data)/4)
			}
			outputs = append(outputs, sink.data)
		}
		for _, out := range outputs[1:] {
			if !bytes.Equal(out, outputs[0]) {
				t.Fatalf("chunk-dependent output at %v", rates)
			}
		}
		var signal, noise float64
		for i := 100; i < rates[1]-100; i++ {
			want := 12000 * math.Sin(2*math.Pi*997*float64(i)/float64(rates[1]))
			got := float64(int16(binary.LittleEndian.Uint16(outputs[0][i*4:])))
			signal += want * want
			noise += (got - want) * (got - want)
		}
		if snr := 10 * math.Log10(signal/noise); snr < 65 {
			t.Fatalf("%v: passband SNR %.2f dB", rates, snr)
		}
	}
}
func TestResamplingRejectsAliasing(t *testing.T) {
	energy := func(freq float64) float64 {
		sink := &conversionSink{}
		c, _ := NewConverter(sink, Format{16000, 1, S16LE})
		if err := c.Write(context.Background(), tone(48000, 1, 48000, freq)); err != nil {
			t.Fatal(err)
		}
		sum := 0.0
		for i := 1000; i < len(sink.data)/2; i++ {
			v := float64(int16(binary.LittleEndian.Uint16(sink.data[2*i:])))
			sum += v * v
		}
		return sum
	}
	if db := 10 * math.Log10(energy(12000)/energy(1000)); db > -45 {
		t.Fatalf("alias rejection only %.1f dB", db)
	}
}
func TestSurroundDownmixAndFormatSwitch(t *testing.T) {
	sink := &conversionSink{}
	output := Format{48000, 2, S16LE}
	c, _ := NewConverter(sink, output)
	for _, input := range []Format{{44100, 2, S16LE}, {48000, 6, S16LE}, {48000, 8, S16LE}, {48000, 1, S16LE}} {
		b := tone(input.Rate, input.Channels, 4096, 997)
		if err := c.Write(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range sink.formats {
		if f != output {
			t.Fatalf("changed device format to %v", f)
		}
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// LFE must not leak into a headphone downmix; center must reach both ears.
	b, _ := NewBlock(Format{48000, 6, S16LE}, 2)
	binary.LittleEndian.PutUint16(b.Data[6:], 12000)
	binary.LittleEndian.PutUint16(b.Data[12+4:], 12000)
	if err := c.Write(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sink.data[:4], []byte{0, 0, 0, 0}) || !bytes.Equal(sink.data[4:6], sink.data[6:8]) || binary.LittleEndian.Uint16(sink.data[4:]) == 0 {
		t.Fatalf("downmix %x", sink.data)
	}
	c.Close()
	c.Close()
	if sink.closed != 1 {
		t.Fatal("closed twice")
	}
}
