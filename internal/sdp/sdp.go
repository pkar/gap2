// Package sdp implements a bounded parser for the SDP bodies carried by
// AirPlay ANNOUNCE requests. It extracts the single audio stream the receiver
// supports: mpeg4-generic (AAC-LC) or AppleLossless (ALAC).
package sdp

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Errors returned by Parse.
var (
	ErrTooLarge    = errors.New("sdp: body too large")
	ErrNoAudio     = errors.New("sdp: no audio media description")
	ErrNoRTCPMap   = errors.New("sdp: missing rtpmap for audio payload type")
	ErrBadPayload  = errors.New("sdp: invalid payload type")
	ErrBadFmtp     = errors.New("sdp: invalid fmtp attribute")
	ErrUnsupported = errors.New("sdp: unsupported audio encoding")
)

// Limits bounds parser memory use.
type Limits struct {
	MaxBodyBytes int
	MaxLines     int
	MaxLineBytes int
}

// DefaultLimits returns conservative limits for ANNOUNCE SDP bodies.
func DefaultLimits() Limits {
	return Limits{
		MaxBodyBytes: 64 << 10,
		MaxLines:     512,
		MaxLineBytes: 16 << 10,
	}
}

// AACConfig holds the fmtp parameters that configure AAC-LC RTP ingest.
type AACConfig struct {
	SizeLength       int
	IndexLength      int
	IndexDeltaLength int
	Mode             string
	StreamType       string
	ProfileLevelID   string
	// ASC is the decoded AudioSpecificConfig carried in the config= fmtp
	// parameter.
	ASC []byte
}

// ALACConfig holds the magic-cookie parameters Apple's ALAC fmtp attribute.
type ALACConfig struct {
	FrameLength       int
	CompatibleVersion int
	BitDepth          int
	PB                int
	MB                int
	KB                int
	Channels          int
	MaxRun            int
	MaxFrameBytes     int
	AvgBitRate        int
	SampleRate        int
}

// Media is the audio stream described by an SDP body.
type Media struct {
	PayloadType int
	Encoding    string
	ClockRate   int
	Channels    int
	AAC         *AACConfig
	ALAC        *ALACConfig
}

// Parse extracts the audio stream from body.
func Parse(body []byte, limits Limits) (*Media, error) {
	if len(body) > limits.MaxBodyBytes {
		return nil, ErrTooLarge
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > limits.MaxLines {
		return nil, ErrTooLarge
	}

	var (
		payloadType int
		haveMedia   bool
		rtpmap      string
		fmtp        string
	)
	for _, raw := range lines {
		if len(raw) > limits.MaxLineBytes {
			return nil, ErrTooLarge
		}
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m=audio "):
			pt, err := parseAudioLine(line)
			if err != nil {
				return nil, err
			}
			payloadType = pt
			haveMedia = true
		case strings.HasPrefix(line, "a=rtpmap:"):
			pt, rest, err := parseAttribute(line, "a=rtpmap:")
			if err != nil {
				return nil, err
			}
			if pt == payloadType {
				rtpmap = rest
			}
		case strings.HasPrefix(line, "a=fmtp:"):
			pt, rest, err := parseAttribute(line, "a=fmtp:")
			if err != nil {
				return nil, err
			}
			if pt == payloadType {
				fmtp = rest
			}
		}
	}
	if !haveMedia {
		return nil, ErrNoAudio
	}
	if rtpmap == "" {
		return nil, ErrNoRTCPMap
	}

	encoding, clockRate, channels, err := parseRTCPMap(rtpmap)
	if err != nil {
		return nil, err
	}
	m := &Media{
		PayloadType: payloadType,
		Encoding:    encoding,
		ClockRate:   clockRate,
		Channels:    channels,
	}
	switch encoding {
	case "mpeg4-generic":
		cfg, err := parseAACFmtp(fmtp)
		if err != nil {
			return nil, err
		}
		m.AAC = cfg
	case "AppleLossless":
		cfg, err := parseALACFmtp(fmtp)
		if err != nil {
			return nil, err
		}
		m.ALAC = cfg
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupported, encoding)
	}
	return m, nil
}

func parseAudioLine(line string) (int, error) {
	fields := strings.Fields(strings.TrimPrefix(line, "m=audio "))
	if len(fields) < 3 {
		return 0, ErrBadPayload
	}
	pt, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrBadPayload, fields[2])
	}
	return pt, nil
}

func parseAttribute(line, prefix string) (int, string, error) {
	rest := strings.TrimPrefix(line, prefix)
	colon := strings.IndexByte(rest, ' ')
	if colon <= 0 {
		return 0, "", ErrBadFmtp
	}
	pt, err := strconv.Atoi(rest[:colon])
	if err != nil {
		return 0, "", fmt.Errorf("%w: %q", ErrBadPayload, rest[:colon])
	}
	return pt, strings.TrimSpace(rest[colon+1:]), nil
}

func parseRTCPMap(v string) (encoding string, clockRate, channels int, err error) {
	parts := strings.Split(v, "/")
	encoding = parts[0]
	if len(parts) >= 2 {
		clockRate, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, 0, ErrNoRTCPMap
		}
	}
	if len(parts) >= 3 {
		channels, err = strconv.Atoi(parts[2])
		if err != nil {
			return "", 0, 0, ErrNoRTCPMap
		}
	}
	return encoding, clockRate, channels, nil
}

func parseAACFmtp(v string) (*AACConfig, error) {
	cfg := &AACConfig{
		SizeLength:       13,
		IndexLength:      3,
		IndexDeltaLength: 3,
	}
	if v == "" {
		return nil, fmt.Errorf("%w: empty fmtp for mpeg4-generic", ErrBadFmtp)
	}
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("%w: %q", ErrBadFmtp, part)
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.TrimSpace(kv[1])
		switch key {
		case "sizelength", "indexlength", "indexdeltalength":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("%w: %s=%q", ErrBadFmtp, key, val)
			}
			switch key {
			case "sizelength":
				cfg.SizeLength = n
			case "indexlength":
				cfg.IndexLength = n
			case "indexdeltalength":
				cfg.IndexDeltaLength = n
			}
		case "mode":
			cfg.Mode = val
		case "streamtype":
			cfg.StreamType = val
		case "profile-level-id":
			cfg.ProfileLevelID = val
		case "config":
			asc, err := hex.DecodeString(val)
			if err != nil || len(asc) == 0 || len(asc) > 64 {
				return nil, fmt.Errorf("%w: config=%q", ErrBadFmtp, val)
			}
			cfg.ASC = asc
		}
	}
	if len(cfg.ASC) == 0 {
		return nil, fmt.Errorf("%w: missing config for mpeg4-generic", ErrBadFmtp)
	}
	return cfg, nil
}

func parseALACFmtp(v string) (*ALACConfig, error) {
	fields := strings.Fields(v)
	if len(fields) != 11 {
		return nil, fmt.Errorf("%w: AppleLossless needs 11 parameters, got %d", ErrBadFmtp, len(fields))
	}
	vals := make([]int, 11)
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrBadFmtp, f)
		}
		vals[i] = n
	}
	return &ALACConfig{
		FrameLength:       vals[0],
		CompatibleVersion: vals[1],
		BitDepth:          vals[2],
		PB:                vals[3],
		MB:                vals[4],
		KB:                vals[5],
		Channels:          vals[6],
		MaxRun:            vals[7],
		MaxFrameBytes:     vals[8],
		AvgBitRate:        vals[9],
		SampleRate:        vals[10],
	}, nil
}
