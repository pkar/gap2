package rtp

import "errors"

// Errors returned by AAC payload parsing.
var (
	ErrAACHeaderLength = errors.New("rtp: invalid AAC AU-headers-length")
	ErrAACShort        = errors.New("rtp: AAC payload shorter than AU data")
)

// AACAU is one access unit carried by an RFC 3640 AAC RTP payload.
type AACAU struct {
	// SizeBits is the AU size from the AU header, in bits.
	SizeBits int
	// Index is the AU-Index field (3 bits). Single-packet mode uses 0.
	Index int
	// Data is the raw access unit (raw_data_block) payload.
	Data []byte
}

// ParseAACPayload parses an RFC 3640 AAC RTP payload of the form
// AU-headers-length, followed by 16-bit AU headers, followed by the access
// units. The returned slice is in transmission order.
func ParseAACPayload(payload []byte) ([]AACAU, error) {
	if len(payload) < 2 {
		return nil, ErrAACShort
	}
	headerBits := int(payload[0])<<8 | int(payload[1])
	if headerBits%16 != 0 {
		return nil, ErrAACHeaderLength
	}
	headerBytes := headerBits / 8
	if headerBytes > len(payload)-2 {
		return nil, ErrAACShort
	}
	n := headerBytes / 2
	aus := make([]AACAU, 0, n)
	off := 2 + headerBytes
	for i := 0; i < n; i++ {
		h := int(payload[2+2*i])<<8 | int(payload[3+2*i])
		sizeBits := h >> 3
		sizeBytes := (sizeBits + 7) / 8
		if sizeBytes > len(payload)-off {
			return nil, ErrAACShort
		}
		aus = append(aus, AACAU{
			SizeBits: sizeBits,
			Index:    h & 0x7,
			Data:     payload[off : off+sizeBytes],
		})
		off += sizeBytes
	}
	if off != len(payload) {
		return nil, ErrAACShort
	}
	return aus, nil
}
