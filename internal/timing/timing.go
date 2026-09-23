// Package timing provides clock abstraction and sample-rate conversion
// helpers shared by protocol and playout code.
package timing

import "time"

// Clock abstracts a time source so tests can inject deterministic values.
type Clock interface {
	Now() time.Time
}

// SystemClock uses the process clock.
type SystemClock struct{}

// Now returns the current process time.
func (SystemClock) Now() time.Time { return time.Now() }

// FramesToDuration converts frames at sampleRate to a duration. It returns 0
// for a non-positive sample rate.
func FramesToDuration(frames int64, sampleRate int) time.Duration {
	if sampleRate <= 0 {
		return 0
	}
	return time.Duration(float64(frames) / float64(sampleRate) * float64(time.Second))
}

// DurationToFrames converts d to whole frames at sampleRate, rounding toward
// zero. It returns 0 for a non-positive sample rate.
func DurationToFrames(d time.Duration, sampleRate int) int64 {
	if sampleRate <= 0 {
		return 0
	}
	return int64(float64(d) / float64(time.Second) * float64(sampleRate))
}
