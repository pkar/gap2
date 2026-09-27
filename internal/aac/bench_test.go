package aac

import (
	"os"
	"testing"
)

// BenchmarkDecodeStereo measures one 1024-sample stereo access unit, the
// shape AirPlay senders stream, cycling through the fixture's frames.
func BenchmarkDecodeStereo(b *testing.B) {
	wire, err := os.ReadFile("testdata/stereo-44100.aac")
	if err != nil {
		b.Fatal(err)
	}
	var aus [][]byte
	for len(wire) > 0 {
		h, err := ParseHeader(wire)
		if err != nil {
			b.Fatal(err)
		}
		aus = append(aus, wire[h.HeaderLength:h.FrameLength])
		wire = wire[h.FrameLength:]
	}
	d, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: 44100, ChannelConfiguration: 2})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.Decode(aus[i%len(aus)]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkIMDCT1024(b *testing.B) {
	src, dst := make([]float64, 1024), make([]float64, 2048)
	for i := range src {
		src[i] = float64(i%17) - 8
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = IMDCT(dst, src)
	}
}
