// Package rtp implements bounded parsing for RTP packets as used by the
// AirPlay media transport. It parses the fixed header, contributing sources,
// the header extension, and padding, and returns the media payload unchanged.
package rtp

import (
	"errors"
	"fmt"
)

// Errors returned by Parse.
var (
	ErrShortPacket  = errors.New("rtp: packet shorter than fixed header")
	ErrBadVersion   = errors.New("rtp: unsupported version")
	ErrShortCSRC    = errors.New("rtp: packet shorter than CSRC list")
	ErrShortHeader  = errors.New("rtp: packet shorter than extension header")
	ErrShortExtData = errors.New("rtp: packet shorter than extension data")
	ErrBadPadding   = errors.New("rtp: invalid padding length")
)

// FixedHeaderSize is the size of the RTP fixed header.
const FixedHeaderSize = 12

// Packet is one parsed RTP packet. Payload excludes CSRCs, the extension
// header, and padding.
type Packet struct {
	Version     uint8
	Padding     bool
	Extension   bool
	CSRCCount   int
	Marker      bool
	PayloadType uint8
	Sequence    uint16
	Timestamp   uint32
	SSRC        uint32
	CSRCs       []uint32
	ExtProfile  uint16
	ExtData     []byte
	Payload     []byte
}

// Parse decodes b and returns its fields. It does not retain b.
func Parse(b []byte) (Packet, error) {
	if len(b) < FixedHeaderSize {
		return Packet{}, ErrShortPacket
	}
	p := Packet{
		Version:     b[0] >> 6,
		Padding:     b[0]&0x20 != 0,
		Extension:   b[0]&0x10 != 0,
		CSRCCount:   int(b[0] & 0x0f),
		Marker:      b[1]&0x80 != 0,
		PayloadType: b[1] & 0x7f,
		Sequence:    uint16(b[2])<<8 | uint16(b[3]),
		Timestamp:   uint32(b[4])<<24 | uint32(b[5])<<16 | uint32(b[6])<<8 | uint32(b[7]),
		SSRC:        uint32(b[8])<<24 | uint32(b[9])<<16 | uint32(b[10])<<8 | uint32(b[11]),
	}
	if p.Version != 2 {
		return Packet{}, fmt.Errorf("%w: %d", ErrBadVersion, p.Version)
	}

	off := FixedHeaderSize
	if csrcBytes := p.CSRCCount * 4; csrcBytes > 0 {
		if off+csrcBytes > len(b) {
			return Packet{}, ErrShortCSRC
		}
		p.CSRCs = make([]uint32, p.CSRCCount)
		for i := range p.CSRCs {
			p.CSRCs[i] = uint32(b[off])<<24 | uint32(b[off+1])<<16 | uint32(b[off+2])<<8 | uint32(b[off+3])
			off += 4
		}
	}

	if p.Extension {
		if off+4 > len(b) {
			return Packet{}, ErrShortHeader
		}
		p.ExtProfile = uint16(b[off])<<8 | uint16(b[off+1])
		extWords := int(uint16(b[off+2])<<8 | uint16(b[off+3]))
		off += 4
		extLen := extWords * 4
		if off+extLen > len(b) {
			return Packet{}, ErrShortExtData
		}
		p.ExtData = b[off : off+extLen : off+extLen]
		off += extLen
	}

	end := len(b)
	if p.Padding {
		if off >= end {
			return Packet{}, ErrBadPadding
		}
		pad := int(b[end-1])
		if pad == 0 || off+pad > end {
			return Packet{}, ErrBadPadding
		}
		end -= pad
	}

	p.Payload = b[off:end:end]
	return p, nil
}
