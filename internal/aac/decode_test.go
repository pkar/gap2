package aac

import (
	"strings"
	"testing"
)

// packBits packs an MSB-first bit string (spaces allowed, '0'/'1') into
// bytes, padding the final byte with zero bits.
func packBits(s string) []byte {
	s = strings.Map(func(r rune) rune {
		if r == ' ' {
			return -1
		}
		return r
	}, s)
	buf := make([]byte, (len(s)+7)/8)
	for i, ch := range s {
		if ch == '1' {
			buf[i/8] |= 1 << (7 - uint(i%8))
		}
	}
	return buf
}

// TestDecodeZeroSpectrum decodes a minimal mono AAC-LC frame whose sole
// scalefactor band uses the all-zero codebook, and checks the filterbank
// emits a silent 1024-sample block across two consecutive frames.
func TestDecodeZeroSpectrum(t *testing.T) {
	d, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: 44100, ChannelConfiguration: 1})
	if err != nil {
		t.Fatal(err)
	}
	// SCE(000) instance_tag(0000) global_gain=100(01100100)
	// ics: reserved(0) only_long(00) sine(0) max_sfb=1(000001) no predictor(0)
	// section: cb=0(0000) len=1(00001)
	// pulse(0) tns(0) gain_control(0)
	au := packBits("000 0000 01100100 0 00 0 000001 0 0000 00001 0 0 0")
	for frame := 0; frame < 2; frame++ {
		block, err := d.Decode(au)
		if err != nil {
			t.Fatalf("frame %d: %v", frame, err)
		}
		if block.Frames() != 1024 {
			t.Fatalf("frame %d: frames = %d, want 1024", frame, block.Frames())
		}
		for i := 0; i < 1024; i++ {
			s := int16(block.Data[2*i]) | int16(block.Data[2*i+1])<<8
			if s != 0 {
				t.Fatalf("frame %d sample %d = %d, want 0", frame, i, s)
			}
		}
	}
}

// bitWriter assembles an MSB-first bitstream for hand-built access units.
type bitWriter struct{ bits []byte }

func (w *bitWriter) put(v uint32, n int) {
	for b := n - 1; b >= 0; b-- {
		w.bits = append(w.bits, byte((v>>uint(b))&1))
	}
}

func (w *bitWriter) bytes() []byte {
	buf := make([]byte, (len(w.bits)+7)/8)
	for i, b := range w.bits {
		if b != 0 {
			buf[i/8] |= 1 << (7 - uint(i%8))
		}
	}
	return buf
}

// TestDecodeNonzeroSpectrum decodes a mono frame whose first band carries a
// single +1 coefficient under a high scalefactor, and checks the filterbank
// produces audible output (proving the spectral/dequant/synthesis path).
func TestDecodeNonzeroSpectrum(t *testing.T) {
	d, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: 44100, ChannelConfiguration: 1})
	if err != nil {
		t.Fatal(err)
	}
	var w bitWriter
	w.put(0, 3)                                           // SCE
	w.put(0, 4)                                           // element_instance_tag
	w.put(200, 8)                                         // global_gain (high so a +1 coefficient reaches int16 scale)
	w.put(0, 1)                                           // ics_reserved_bit
	w.put(0, 2)                                           // only_long_sequence
	w.put(0, 1)                                           // window_shape = sine
	w.put(1, 6)                                           // max_sfb = 1
	w.put(0, 1)                                           // predictor_data_present = 0
	w.put(1, 4)                                           // codebook 1
	w.put(1, 5)                                           // section length = 1
	w.put(scalefactorCodes[60], int(scalefactorBits[60])) // DPCM delta 0
	w.put(0, 1)                                           // pulse_data_present
	w.put(0, 1)                                           // tns_data_present
	w.put(0, 1)                                           // gain_control_data_present
	// Codebook 1 tuple (0, +1, 0, 0): raw (1,2,1,1) -> index 1*27+2*9+1*3+1.
	w.put(uint32(spectralCodes[0][49]), int(spectralBits[0][49]))

	block, err := d.Decode(w.bytes())
	if err != nil {
		t.Fatal(err)
	}
	nonzero := false
	for i := 0; i < 1024; i++ {
		s := int16(block.Data[2*i]) | int16(block.Data[2*i+1])<<8
		if s != 0 {
			nonzero = true
		}
	}
	if !nonzero {
		t.Fatal("expected nonzero output for a unit spectral coefficient")
	}
}

func TestDecodeTruncated(t *testing.T) {
	d, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: 44100, ChannelConfiguration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode([]byte{0x00, 0x01}); err == nil {
		t.Fatal("expected error for truncated access unit")
	}
}

func TestNewDecoderRejects(t *testing.T) {
	if _, err := NewDecoder(ASC{ObjectType: 3, SamplingFrequency: 44100, ChannelConfiguration: 1}); err == nil {
		t.Fatal("expected error for non-LC object type")
	}
	if _, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: 44100, ChannelConfiguration: 6}); err == nil {
		t.Fatal("expected error for unsupported channel configuration")
	}
	if _, err := NewDecoder(ASC{ObjectType: 2, SamplingFrequency: 12345, ChannelConfiguration: 2}); err == nil {
		t.Fatal("expected error for unsupported sample rate")
	}
}
