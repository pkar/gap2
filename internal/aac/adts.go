// Package aac provides bounded parsing and framing for ADTS-wrapped AAC audio,
// plus a full AAC-LC spectral decoder: it parses the AudioSpecificConfig, and
// decodes the Huffman-coded spectral coefficients, the inverse quantization,
// and the inverse modified discrete cosine transform (IMDCT) filter bank into
// interleaved PCM.
package aac

import (
	"errors"
	"fmt"
	"time"
)

// ErrSyncLost is returned when no ADTS syncword can be found within the
// resync window.
var ErrSyncLost = errors.New("aac: ADTS sync lost")

// ErrFrameTooLarge is returned when a frame advertises a length beyond the
// configured maximum.
var ErrFrameTooLarge = errors.New("aac: ADTS frame too large")

// SampleRates maps ADTS sampling_frequency_index values to Hz. Index 15 is an
// explicit frequency carried in a program config element and is not resolved
// here.
var SampleRates = [13]int{
	96000, 88200, 64000, 48000, 44100, 32000, 24000,
	22050, 16000, 12000, 11025, 8000, 7350,
}

// Profile is an ADTS Audio Object Type minus one (the MPEG-2/MPEG-4 audio
// profile field).
type Profile uint8

// Profiles used by AirPlay audio streams.
const (
	ProfileMain Profile = iota
	ProfileLC
	ProfileSSR
	ProfileLTP
)

func (p Profile) String() string {
	switch p {
	case ProfileMain:
		return "main"
	case ProfileLC:
		return "lc"
	case ProfileSSR:
		return "ssr"
	case ProfileLTP:
		return "ltp"
	default:
		return "unknown"
	}
}

// Header is a parsed ADTS fixed header.
type Header struct {
	Profile           Profile
	SamplingFrequency int // Hz
	ChannelConfig     int
	ProtectionAbsent  bool
	FrameLength       int // total bytes including the header
	HeaderLength      int // 7 or 9 bytes
	RawDataBlocks     int // number_of_raw_data_blocks_in_frame
	BufferFullness    int
}

// Samples returns the number of PCM samples per channel carried by the frame.
func (h Header) Samples() int {
	return 1024 * (h.RawDataBlocks + 1)
}

// Duration returns the playback duration of the frame.
func (h Header) Duration() time.Duration {
	if h.SamplingFrequency <= 0 {
		return 0
	}
	return time.Duration(float64(h.Samples()) / float64(h.SamplingFrequency) * float64(time.Second))
}

// PayloadLength returns the number of raw AAC bytes following the header.
func (h Header) PayloadLength() int {
	return h.FrameLength - h.HeaderLength
}

// ParseHeader parses the ADTS header at the start of b. b must contain at
// least the full header, and the header must begin with a valid syncword.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < 7 {
		return Header{}, fmt.Errorf("aac: short header (%d bytes)", len(b))
	}
	if b[0] != 0xff || b[1]&0xf6 != 0xf0 {
		return Header{}, fmt.Errorf("aac: invalid ADTS syncword %#x %#x", b[0], b[1])
	}

	h := Header{
		Profile:          Profile((b[2] >> 6) & 0x3),
		ChannelConfig:    int((b[2]&0x01)<<2 | (b[3]>>6)&0x03),
		ProtectionAbsent: b[1]&0x01 == 0x01,
	}
	sfIndex := int((b[2] >> 2) & 0x0f)
	switch {
	case sfIndex < len(SampleRates):
		h.SamplingFrequency = SampleRates[sfIndex]
	case sfIndex == 15:
		return Header{}, fmt.Errorf("aac: explicit sample rate not supported")
	default:
		return Header{}, fmt.Errorf("aac: invalid sampling frequency index %d", sfIndex)
	}

	h.HeaderLength = 7
	if !h.ProtectionAbsent {
		h.HeaderLength = 9
	}
	if len(b) < h.HeaderLength {
		return Header{}, fmt.Errorf("aac: short header (%d bytes, need %d)", len(b), h.HeaderLength)
	}

	h.FrameLength = int(b[3]&0x03)<<11 | int(b[4])<<3 | int(b[5]>>5)
	h.BufferFullness = int(b[5]&0x1f)<<6 | int(b[6]>>2)
	h.RawDataBlocks = int(b[6] & 0x03)

	if h.FrameLength < h.HeaderLength {
		return Header{}, fmt.Errorf("aac: frame length %d shorter than header %d", h.FrameLength, h.HeaderLength)
	}
	return h, nil
}
