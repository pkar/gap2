// Package pcm defines PCM formats, blocks, and playback sink contracts used
// across the receiver.
package pcm

import "fmt"

// SampleFormat describes the representation of one PCM sample.
type SampleFormat uint8

const (
	// SampleFormatUnknown is an unset or invalid sample format.
	SampleFormatUnknown SampleFormat = iota
	// S16LE is signed 16-bit little-endian PCM.
	S16LE
	// S24LE is signed 24-bit PCM left-aligned in 3 little-endian bytes.
	S24LE
	// F32LE is 32-bit IEEE 754 floating-point little-endian PCM.
	F32LE
)

func (f SampleFormat) String() string {
	switch f {
	case S16LE:
		return "s16le"
	case S24LE:
		return "s24le"
	case F32LE:
		return "f32le"
	default:
		return "unknown"
	}
}

// Valid reports whether f is a supported sample format.
func (f SampleFormat) Valid() bool {
	return f == S16LE || f == S24LE || f == F32LE
}

// BytesPerSample returns the encoded size of one sample, or 0 if invalid.
func (f SampleFormat) BytesPerSample() int {
	switch f {
	case S16LE:
		return 2
	case S24LE:
		return 3
	case F32LE:
		return 4
	default:
		return 0
	}
}

// Format describes an interleaved PCM stream.
type Format struct {
	Rate     int
	Channels int
	Format   SampleFormat
}

// Valid reports whether f describes a playable PCM stream.
func (f Format) Valid() error {
	if f.Rate <= 0 {
		return fmt.Errorf("pcm: invalid rate %d", f.Rate)
	}
	if f.Channels <= 0 {
		return fmt.Errorf("pcm: invalid channels %d", f.Channels)
	}
	if !f.Format.Valid() {
		return fmt.Errorf("pcm: invalid sample format %s", f.Format)
	}
	return nil
}

// BytesPerFrame returns the encoded size of one interleaved frame, or 0 if
// the format is invalid.
func (f Format) BytesPerFrame() int {
	if f.Channels <= 0 || !f.Format.Valid() {
		return 0
	}
	return f.Channels * f.Format.BytesPerSample()
}

func (f Format) String() string {
	return fmt.Sprintf("%dHz/%dch/%s", f.Rate, f.Channels, f.Format)
}
