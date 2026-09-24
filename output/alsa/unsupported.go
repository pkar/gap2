//go:build !linux || (!amd64 && !arm64)

package alsa

import (
	"context"
	"errors"
	"github.com/pkar/gap2/pcm"
)

func open(context.Context, string, pcm.Format) (pcm.Sink, error) {
	return nil, errors.New("alsa: native playback requires Linux amd64 or arm64")
}
