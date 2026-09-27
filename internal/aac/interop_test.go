package aac

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

// The fixture is a 997 Hz tone encoded by FFmpeg's native AAC encoder:
// ffmpeg -f lavfi -i sine=frequency=997:sample_rate=44100:duration=0.25
//
//	-ac 2 -c:a aac -b:a 128k -f adts stereo-44100.aac
func TestIndependentStereoAAC(t *testing.T) {
	for _, name := range []string{"stereo-44100", "noise-44100", "surround-48000", "surround71-48000"} {
		t.Run(name, func(t *testing.T) { compareIndependentAAC(t, name) })
	}
}

// decodeFixture decodes testdata/NAME.aac frame by frame and returns the
// decoder, the decoded S16LE PCM, and FFmpeg's reference PCM.
func decodeFixture(t *testing.T, name string) (*Decoder, []byte, []byte) {
	t.Helper()
	wire, err := os.ReadFile("testdata/" + name + ".aac")
	if err != nil {
		t.Fatal(err)
	}
	header, err := ParseHeader(wire)
	if err != nil {
		t.Fatal(err)
	}
	if name == "surround71-48000" {
		header.ChannelConfig = 12
	}
	d, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: header.SamplingFrequency, ChannelConfiguration: header.ChannelConfig})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := os.ReadFile("testdata/" + name + ".s16le")
	if err != nil {
		t.Fatal(err)
	}
	var decoded []byte
	for frame := 0; len(wire) > 0; frame++ {
		h, err := ParseHeader(wire)
		if err != nil {
			t.Fatal(err)
		}
		b, err := d.Decode(wire[h.HeaderLength:h.FrameLength])
		if err != nil {
			t.Fatalf("frame %d: %v", frame, err)
		}
		if b.Frames() != 1024 {
			t.Fatalf("frame %d: %d samples", frame, b.Frames())
		}
		decoded = append(decoded, b.Data...)
		wire = wire[h.FrameLength:]
	}
	if len(decoded) != len(reference) {
		t.Fatalf("PCM length: %d, reference %d", len(decoded), len(reference))
	}
	return d, decoded, reference
}

func compareIndependentAAC(t *testing.T, name string) {
	compareIndependentAACSNR(t, name, 30)
}

func compareIndependentAACSNR(t *testing.T, name string, minSNR float64) {
	d, decoded, reference := decodeFixture(t, name)
	var signal, noise, output float64
	for i := 0; i < len(decoded); i += 2 {
		got := float64(int16(binary.LittleEndian.Uint16(decoded[i:])))
		want := float64(int16(binary.LittleEndian.Uint16(reference[i:])))
		signal += want * want
		noise += (got - want) * (got - want)
		output += got * got
	}
	snr := 10 * math.Log10(signal/noise)
	t.Logf("reference SNR %.2f dB, amplitude ratio %.5f", snr, math.Sqrt(output/signal))
	if snr < minSNR {
		for a := 0; a < d.channels; a++ {
			best, bestScore := -1, 0.0
			for b := 0; b < d.channels; b++ {
				score := 0.0
				for i := 0; i < len(decoded)/(2*d.channels); i++ {
					score += float64(int16(binary.LittleEndian.Uint16(decoded[2*(i*d.channels+a):]))) * float64(int16(binary.LittleEndian.Uint16(reference[2*(i*d.channels+b):])))
				}
				if score > bestScore {
					best, bestScore = b, score
				}
			}
			t.Logf("output channel %d matches reference %d", a, best)
		}
		t.Fatalf("PCM differs from independent decoder: %.2f dB SNR", snr)
	}
}

// Intensity stereo under ms_mask_present == 2 must invert the band sign, as
// FFmpeg does; ignoring the mask left this fixture near 40 dB.
func TestIntensityStereoAAC(t *testing.T) {
	compareIndependentAACSNR(t, "intensity-44100", 60)
}

// PNS noise differs between decoders, so compare the energy of the
// high-frequency region, where the encoder substitutes noise, instead of
// the waveform.
func TestPNSEnergyAAC(t *testing.T) {
	d, decoded, reference := decodeFixture(t, "pns-44100")
	stride := 2 * d.channels
	highpass := func(b []byte) float64 {
		sample := func(i int) float64 { return float64(int16(binary.LittleEndian.Uint16(b[i:]))) }
		var sum float64
		n := 0
		for i := 3 * stride; i+1 < len(b); i += 2 {
			// Third-order difference: a steep high-pass emphasizing the
			// top octaves.
			v := sample(i) - 3*sample(i-stride) + 3*sample(i-2*stride) - sample(i-3*stride)
			sum += v * v
			n++
		}
		return math.Sqrt(sum / float64(n))
	}
	got, want := highpass(decoded), highpass(reference)
	t.Logf("high-pass RMS %.1f, reference %.1f", got, want)
	if ratio := got / want; ratio < 0.95 || ratio > 1.05 {
		t.Fatalf("high-pass RMS ratio %.3f, want within 5%% of FFmpeg", ratio)
	}
}
