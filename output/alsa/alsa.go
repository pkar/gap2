// Package alsa plays interleaved S16LE PCM through Linux ALSA character
// devices, without cgo, libasound, or a playback subprocess.
package alsa

import (
	"context"

	"github.com/pkar/gap2/pcm"
)

// Device opens the configured ALSA playback device. The device
// must support the stream's exact sample rate and channel count.
type Device string

func (d Device) Open(ctx context.Context, format pcm.Format) (pcm.Sink, error) {
	return open(ctx, string(d), format)
}
