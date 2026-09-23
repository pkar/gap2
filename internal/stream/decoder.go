package stream

import (
	"fmt"

	"github.com/pkar/gap2/internal/aac"
	"github.com/pkar/gap2/internal/sdp"
)

// NewDecoder returns the codec decoder for an announced media description.
// For mpeg4-generic streams it parses the fmtp config= AudioSpecificConfig and
// returns an AAC-LC decoder; ALAC is not yet implemented.
func NewDecoder(m *sdp.Media) (Decoder, error) {
	if m == nil {
		return nil, fmt.Errorf("stream: %w: nil media", ErrUnsupported)
	}
	switch m.Encoding {
	case "mpeg4-generic":
		if m.AAC == nil {
			return nil, fmt.Errorf("stream: %w: mpeg4-generic without fmtp", ErrUnsupported)
		}
		asc, err := aac.ParseASC(m.AAC.ASC)
		if err != nil {
			return nil, fmt.Errorf("stream: parse AudioSpecificConfig: %w", err)
		}
		return aac.NewDecoder(asc)
	case "AppleLossless":
		return nil, fmt.Errorf("stream: %w: AppleLossless decoder not implemented", ErrUnsupported)
	default:
		return nil, fmt.Errorf("stream: %w: %s", ErrUnsupported, m.Encoding)
	}
}
