package stream

import (
	"fmt"
	"github.com/pkar/gap2/internal/sdp"
)

// Buffered format identifiers are carried in the authenticated SSRC word.
// https://github.com/mikebrady/shairport-sync/blob/master/player.h
func bufferedMedia(word uint32) (*sdp.Media, error) {
	m := &sdp.Media{PayloadType: 96, Encoding: "AAC", ClockRate: 48000, Channels: 2}
	index, config := 3, 2
	switch word {
	case 0x16000000:
		m.ClockRate = 44100
		index = 4
	case 0x17000000:
	case 0x27000000:
		m.Channels = 6
		config = 6
	case 0x28000000:
		m.Channels = 8
		config = 12
	case 0x15000000:
		m.Encoding = "AppleLossless"
		m.ALAC = &sdp.ALACConfig{FrameLength: 4096, BitDepth: 24, PB: 40, MB: 10, KB: 14, Channels: 2, SampleRate: 48000}
		return m, nil
	case 0x0000face:
		m.ClockRate = 44100
		m.Encoding = "AppleLossless"
		m.ALAC = &sdp.ALACConfig{FrameLength: 352, BitDepth: 16, PB: 40, MB: 10, KB: 14, Channels: 2, SampleRate: 44100}
		return m, nil
	default:
		return nil, fmt.Errorf("stream: unsupported buffered audio format %#x", word)
	}
	asc := uint16(2<<11 | index<<7 | config<<3)
	m.AAC = &sdp.AACConfig{ASC: []byte{byte(asc >> 8), byte(asc)}}
	return m, nil
}
