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
	for _, name := range []string{"stereo-44100", "noise-44100"} {
		t.Run(name, func(t *testing.T) { compareIndependentAAC(t, name) })
	}
}

func compareIndependentAAC(t *testing.T, name string) {
	wire, err := os.ReadFile("testdata/" + name + ".aac")
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: 44100, ChannelConfiguration: 2})
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
	if snr < 30 {
		t.Fatalf("PCM differs from independent decoder: %.2f dB SNR", snr)
	}
}
