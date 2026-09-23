package pcm

import (
	"errors"
	"time"
)

// Block owns an interleaved PCM buffer and the format it is encoded in.
type Block struct {
	Format Format
	Data   []byte
}

// NewBlock allocates a block with room for frames complete frames.
func NewBlock(format Format, frames int) (Block, error) {
	if err := format.Valid(); err != nil {
		return Block{}, err
	}
	if frames < 0 {
		return Block{}, errors.New("pcm: negative frame count")
	}
	size := frames * format.BytesPerFrame()
	if size < 0 {
		return Block{}, errors.New("pcm: frame count overflow")
	}
	return Block{Format: format, Data: make([]byte, size)}, nil
}

// Frames returns the number of complete frames in Data.
func (b Block) Frames() int {
	if bpf := b.Format.BytesPerFrame(); bpf > 0 {
		return len(b.Data) / bpf
	}
	return 0
}

// Duration returns the playback duration of the block's complete frames.
func (b Block) Duration() time.Duration {
	if b.Format.Rate <= 0 {
		return 0
	}
	return time.Duration(float64(b.Frames()) / float64(b.Format.Rate) * float64(time.Second))
}
