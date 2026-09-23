package aac

import (
	"errors"
	"fmt"
	"io"
)

// DefaultMaxFrameSize bounds a single ADTS frame. Real AAC-LC frames at the
// supported bitrates are far below this, but the bound keeps a corrupt length
// field from triggering an unbounded allocation.
const DefaultMaxFrameSize = 1 << 16

// Frame is one complete ADTS frame.
type Frame struct {
	Header  Header
	Payload []byte // raw_data_block bytes, header excluded
}

// Framer splits an ADTS byte stream into complete frames, resynchronizing at
// the next syncword after garbage or corruption.
type Framer struct {
	r       io.Reader
	maxSize int

	buf    []byte
	start  int
	end    int
	synced bool
}

// NewFramer returns a Framer over r with a maximum frame size bound.
func NewFramer(r io.Reader, maxFrameSize int) *Framer {
	if maxFrameSize <= 0 {
		maxFrameSize = DefaultMaxFrameSize
	}
	return &Framer{r: r, maxSize: maxFrameSize}
}

// Next returns the next frame, or io.EOF at a clean stream end.
func (f *Framer) Next() (Frame, error) {
	for {
		if !f.synced {
			if err := f.resync(); err != nil {
				return Frame{}, err
			}
		}
		// A header needs at least 7 bytes; read more without dropping the
		// current window.
		if f.end-f.start < 7 {
			if err := f.fill(); err != nil {
				if err == io.EOF && f.end == f.start {
					return Frame{}, io.EOF
				}
				if err == io.EOF {
					return Frame{}, io.ErrUnexpectedEOF
				}
				return Frame{}, err
			}
			if f.end-f.start < 7 {
				return Frame{}, io.ErrUnexpectedEOF
			}
		}

		h, err := ParseHeader(f.buf[f.start:f.end])
		if err != nil {
			// Not a valid header at this offset; advance and resync.
			f.start++
			f.synced = false
			continue
		}
		if h.FrameLength > f.maxSize {
			return Frame{}, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, h.FrameLength, f.maxSize)
		}
		if f.end-f.start < h.FrameLength {
			if err := f.fillFrame(h.FrameLength); err != nil {
				if err == io.EOF {
					return Frame{}, io.ErrUnexpectedEOF
				}
				return Frame{}, err
			}
		}

		payload := make([]byte, h.PayloadLength())
		copy(payload, f.buf[f.start+h.HeaderLength:f.start+h.FrameLength])
		f.start += h.FrameLength
		if f.start == f.end {
			f.start, f.end = 0, 0
		}
		f.synced = true
		return Frame{Header: h, Payload: payload}, nil
	}
}

// resync positions the buffer at the next ADTS syncword.
func (f *Framer) resync() error {
	const window = 8 << 10 // search window before giving up
	searched := 0
	for {
		i := findSync(f.buf[f.start:f.end])
		if i >= 0 {
			f.start += i
			f.synced = true
			return nil
		}
		searched += f.end - f.start
		if searched >= window {
			f.synced = false
			return ErrSyncLost
		}
		f.start = 0
		f.end = 0
		if err := f.fill(); err != nil {
			return err
		}
	}
}

// findSync returns the offset of the first ADTS syncword in b, or -1.
func findSync(b []byte) int {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == 0xff && b[i+1]&0xf6 == 0xf0 {
			return i
		}
	}
	return -1
}

// fill reads more bytes into the buffer, compacting consumed data first.
func (f *Framer) fill() error {
	if f.start > 0 {
		copy(f.buf, f.buf[f.start:f.end])
		f.end -= f.start
		f.start = 0
	}
	if len(f.buf) == 0 {
		f.buf = make([]byte, 0, f.maxSize)
	}
	if cap(f.buf) == f.end {
		// Grow once instead of appending in tiny slices.
		grown := make([]byte, f.end, f.end*2+4096)
		copy(grown, f.buf)
		f.buf = grown
	}
	n, err := f.r.Read(f.buf[f.end:cap(f.buf)])
	f.end += n
	if n > 0 && err == io.EOF {
		return nil
	}
	return err
}

// fillFrame ensures at least want bytes are buffered or returns an error.
func (f *Framer) fillFrame(want int) error {
	for f.end-f.start < want {
		if f.end == cap(f.buf) {
			if f.start > 0 {
				copy(f.buf, f.buf[f.start:f.end])
				f.end -= f.start
				f.start = 0
			} else if cap(f.buf) < want {
				grown := make([]byte, f.end, want)
				copy(grown, f.buf)
				f.buf = grown
			}
		}
		if f.end == cap(f.buf) {
			return fmt.Errorf("aac: frame %d exceeds buffer capacity %d", want, cap(f.buf))
		}
		if err := f.fill(); err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
	return nil
}
