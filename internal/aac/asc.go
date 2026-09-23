package aac

import "errors"

// Errors returned by ParseASC.
var (
	ErrUnsupportedASC = errors.New("aac: unsupported AudioSpecificConfig")
	ErrInvalidASC     = errors.New("aac: invalid AudioSpecificConfig")
)

// Sample rates indexed by the 4-bit samplingFrequencyIndex field. Index 15
// (0xF) is the escape value that carries a 24-bit explicit rate instead.
var ascSampleRates = [15]int{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350, 0, 0,
}

// ASC is a parsed MPEG-4 AudioSpecificConfig.
type ASC struct {
	// ObjectType is the audio object type (2 for AAC-LC).
	ObjectType int
	// SamplingFrequency is the output sample rate in Hz.
	SamplingFrequency int
	// ChannelConfiguration is the channel configuration (1 mono, 2 stereo).
	ChannelConfiguration int
	// SBR reports whether an explicit HE-AAC (SBR) extension is present.
	SBR bool
	// ExtensionSamplingFrequency is the base/core sample rate for an SBR
	// stream, when SBR is true.
	ExtensionSamplingFrequency int
	// FrameLengthFlag, DependsOnCoreCoder, and ExtensionFlag are the GA
	// specific configuration bits for AAC-LC.
	FrameLengthFlag    bool
	DependsOnCoreCoder bool
	ExtensionFlag      bool
}

// ParseASC decodes the MPEG-4 AudioSpecificConfig carried in the fmtp config=
// parameter for mpeg4-generic streams.
func ParseASC(data []byte) (ASC, error) {
	if len(data) == 0 {
		return ASC{}, ErrInvalidASC
	}
	br := NewBitReader(data)

	objectType, err := readObjectType(br)
	if err != nil {
		return ASC{}, err
	}
	asc := ASC{ObjectType: objectType}

	// Explicit SBR and PS object types carry the extension sample rate first,
	// followed by a full core AudioSpecificConfig.
	if objectType == 5 || objectType == 29 {
		extRate, err := readSamplingFrequency(br)
		if err != nil {
			return ASC{}, err
		}
		asc.SBR = true
		asc.ExtensionSamplingFrequency = extRate
		objectType, err = readObjectType(br)
		if err != nil {
			return ASC{}, err
		}
		asc.ObjectType = objectType
	}

	rate, err := readSamplingFrequency(br)
	if err != nil {
		return ASC{}, err
	}
	if asc.SBR {
		// The first rate read above is the SBR output rate; the core rate is
		// the second read, stored for completeness.
		asc.SamplingFrequency = asc.ExtensionSamplingFrequency
		asc.ExtensionSamplingFrequency = rate
	} else {
		asc.SamplingFrequency = rate
	}

	ch, err := readUint(br, 4)
	if err != nil {
		return ASC{}, err
	}
	asc.ChannelConfiguration = int(ch)

	// GA specific config is only meaningful for the GA object types. AAC-LC
	// is object type 2.
	if asc.ObjectType == 2 {
		flags, err := readUint(br, 3)
		if err != nil {
			// The flags are present for every valid AAC-LC ASC; treat a
			// truncation as invalid rather than lenient.
			return ASC{}, ErrInvalidASC
		}
		asc.FrameLengthFlag = flags&4 != 0
		asc.DependsOnCoreCoder = flags&2 != 0
		asc.ExtensionFlag = flags&1 != 0
	}
	return asc, nil
}

func readObjectType(br *BitReader) (int, error) {
	v, err := readUint(br, 5)
	if err != nil {
		return 0, err
	}
	if v == 31 {
		ext, err := readUint(br, 6)
		if err != nil {
			return 0, err
		}
		return int(32 + ext), nil
	}
	return int(v), nil
}

func readSamplingFrequency(br *BitReader) (int, error) {
	idx, err := readUint(br, 4)
	if err != nil {
		return 0, err
	}
	if idx == 15 {
		rate, err := readUint(br, 24)
		if err != nil {
			return 0, err
		}
		if rate == 0 {
			return 0, ErrInvalidASC
		}
		return int(rate), nil
	}
	return ascSampleRates[idx], nil
}

func readUint(br *BitReader, n int) (uint32, error) {
	v, err := br.Read(n)
	if err != nil {
		return 0, ErrInvalidASC
	}
	return v, nil
}
