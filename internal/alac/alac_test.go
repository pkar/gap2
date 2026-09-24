package alac

import (
	"testing"

	"github.com/pkar/gap2/internal/sdp"
)

func testConfig() *sdp.ALACConfig {
	return &sdp.ALACConfig{
		FrameLength: 4096,
		BitDepth:    16,
		PB:          40,
		MB:          10,
		KB:          14,
		Channels:    2,
		SampleRate:  44100,
	}
}

// bitWriter packs bits MSB-first, mirroring the decoder's bitReader.
type bitWriter struct {
	buf    []byte
	bitPos int
}

func (w *bitWriter) write(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.writeBit((v >> uint(i)) & 1)
	}
}

func (w *bitWriter) writeSigned(v int32, n int) {
	w.write(uint32(v), n)
}

func (w *bitWriter) writeBit(b uint32) {
	byteIdx := w.bitPos / 8
	if byteIdx >= len(w.buf) {
		w.buf = append(w.buf, 0)
	}
	if b != 0 {
		w.buf[byteIdx] |= 1 << (7 - (w.bitPos % 8))
	}
	w.bitPos++
}

func readS16(data []byte, i int) int16 {
	return int16(data[2*i]) | int16(data[2*i+1])<<8
}

func TestDecodeUncompressedStereo(t *testing.T) {
	d, err := NewDecoder(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	samples := [][2]int16{{0, 0}, {1, -1}, {1000, -1000}, {-32768, 32767}}

	var w bitWriter
	w.write(typeCPE, 3)
	w.write(0, 4)  // element instance tag
	w.write(0, 12) // unused header bits
	w.write(1, 1)  // has_size
	w.write(0, 2)  // extra_bits
	w.write(1, 1)  // uncompressed
	w.write(4, 32) // output samples
	for _, s := range samples {
		w.writeSigned(int32(s[0]), 16)
		w.writeSigned(int32(s[1]), 16)
	}
	w.write(typeEnd, 3)

	block, err := d.Decode(w.buf)
	if err != nil {
		t.Fatal(err)
	}
	if block.Format.Channels != 2 || block.Format.Rate != 44100 {
		t.Fatalf("format = %+v", block.Format)
	}
	if block.Frames() != len(samples) {
		t.Fatalf("frames = %d, want %d", block.Frames(), len(samples))
	}
	for i, s := range samples {
		l := readS16(block.Data, 2*i)
		r := readS16(block.Data, 2*i+1)
		if l != s[0] || r != s[1] {
			t.Fatalf("sample %d = (%d,%d), want (%d,%d)", i, l, r, s[0], s[1])
		}
	}
}

func TestDecodeUncompressedMono(t *testing.T) {
	cfg := testConfig()
	cfg.Channels = 1
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	samples := []int16{0, 1, -1, 512, -513}

	var w bitWriter
	w.write(typeSCE, 3)
	w.write(0, 4)
	w.write(0, 12)
	w.write(1, 1)
	w.write(0, 2)
	w.write(1, 1)
	w.write(5, 32)
	for _, s := range samples {
		w.writeSigned(int32(s), 16)
	}
	w.write(typeEnd, 3)

	block, err := d.Decode(w.buf)
	if err != nil {
		t.Fatal(err)
	}
	if block.Format.Channels != 1 {
		t.Fatalf("channels = %d", block.Format.Channels)
	}
	for i, s := range samples {
		if got := readS16(block.Data, i); got != s {
			t.Fatalf("sample %d = %d, want %d", i, got, s)
		}
	}
}

// TestDecodeCompressedSingleSample exercises the Rice path with a one-sample
// frame, where the first sample is copied verbatim (no prediction).
func TestDecodeCompressedSingleSample(t *testing.T) {
	cfg := testConfig()
	cfg.Channels = 1
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// zigzag mapping: 0->0, 1->2, -1->1, 2->4, -2->3, 10->20
	cases := []struct {
		residual int32
		x        uint32
	}{
		{0, 0},
		{1, 2},
		{-1, 1},
		{2, 4},
		{-2, 3},
		{10, 20}, // exercises the escape path (x > 8)
	}
	for _, tc := range cases {
		var w bitWriter
		w.write(typeSCE, 3)
		w.write(0, 4)
		w.write(0, 12)
		w.write(1, 1)  // has_size
		w.write(0, 2)  // extra_bits
		w.write(0, 1)  // compressed
		w.write(1, 32) // one sample
		w.write(0, 8)  // decorr_shift
		w.write(0, 8)  // decorr_left_weight
		w.write(0, 4)  // prediction_type
		w.write(9, 4)  // lpc_quant
		w.write(4, 3)  // rice_history_mult
		w.write(0, 5)  // lpc_order
		if tc.x > 8 {
			// escape: nine 1 bits then the raw bps-bit value
			for i := 0; i < 9; i++ {
				w.write(1, 1)
			}
			w.write(tc.x, 16)
		} else {
			for i := uint32(0); i < tc.x; i++ {
				w.write(1, 1)
			}
			w.write(0, 1)
		}
		w.write(typeEnd, 3)

		block, err := d.Decode(w.buf)
		if err != nil {
			t.Fatalf("residual %d: %v", tc.residual, err)
		}
		if got := readS16(block.Data, 0); got != int16(tc.residual) {
			t.Fatalf("residual %d decoded to %d", tc.residual, got)
		}
	}
}

func TestNewDecoderRejectsUnsupported(t *testing.T) {
	for _, cfg := range []*sdp.ALACConfig{
		nil,
		{BitDepth: 24, Channels: 2, FrameLength: 4096, SampleRate: 44100},
		{BitDepth: 16, Channels: 6, FrameLength: 4096, SampleRate: 44100},
		{BitDepth: 16, Channels: 2, FrameLength: 0, SampleRate: 44100},
	} {
		if _, err := NewDecoder(cfg); err == nil {
			t.Fatalf("NewDecoder(%+v) succeeded, want error", cfg)
		}
	}
}

func TestDecodeScalarEscape(t *testing.T) {
	var w bitWriter
	for i := 0; i < 9; i++ {
		w.write(1, 1)
	}
	w.write(20, 16)
	r := &bitReader{data: w.buf}
	x, err := r.decodeScalar(1, 16)
	if err != nil {
		t.Fatal(err)
	}
	if x != 20 {
		t.Fatalf("decodeScalar = %d, want 20", x)
	}
}

func TestSignExtend(t *testing.T) {
	cases := []struct {
		in   int32
		bits int
		want int32
	}{
		{0x1ffff, 17, -1}, // 17-bit 0x1ffff -> sign-extended -1
		{0x0ffff, 17, 0xffff},
		{3, 2, -1}, // 2-bit 0b11 -> -1
		{1, 2, 1},
		{-1, 32, -1},
	}
	for _, tc := range cases {
		if got := signExtend(tc.in, tc.bits); got != tc.want {
			t.Fatalf("signExtend(%#x, %d) = %d, want %d", tc.in, tc.bits, got, tc.want)
		}
	}
}

func TestDecorateStereo(t *testing.T) {
	left := []int32{1000, 500}
	right := []int32{200, 100}
	decorrelateStereo(left, right, 8, 2)
	// a = left - (right*2)>>8 ; b = right + a ; left=b ; right=a
	if left[0] != 1199 || right[0] != 999 {
		t.Fatalf("got (%d,%d), want (1199,999)", left[0], right[0])
	}
}
