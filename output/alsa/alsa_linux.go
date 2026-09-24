//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/pkar/gap2/pcm"
)

// Structures and requests from Linux UAPI sound/asound.h, 64-bit ABI.
type interval struct{ Min, Max, Flags uint32 }
type hwParams struct {
	Flags                                        uint32
	Masks                                        [3][8]uint32
	ReservedMasks                                [5][8]uint32
	Intervals                                    [12]interval
	ReservedIntervals                            [9]interval
	RMask, CMask, Info, MSBits, RateNum, RateDen uint32
	FIFO                                         uint64
	Sync                                         [16]byte
	Reserved                                     [48]byte
}
type swParams struct {
	Timestamp                                                                                   int32
	PeriodStep, SleepMin                                                                        uint32
	AvailMin, XferAlign, StartThreshold, StopThreshold, SilenceThreshold, SilenceSize, Boundary uint64
	Protocol, TimestampType                                                                     uint32
	Reserved                                                                                    [56]byte
}
type transfer struct {
	Result int64
	Data   unsafe.Pointer
	Frames uint64
}

const (
	prepare      = 0x4140
	drop         = 0x4143
	bufferFrames = 8192
	periodFrames = 1024
)

func ioctl(fd int, request uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(arg))
	runtime.KeepAlive(arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func request(direction, number, size uintptr) uintptr {
	return direction<<30 | size<<16 | 'A'<<8 | number
}

func hardware(format pcm.Format) hwParams {
	var p hwParams
	p.Masks[0][0] = 1 << 3 // RW_INTERLEAVED
	p.Masks[1][0] = 1 << 2 // S16_LE
	p.Masks[2][0] = 1      // standard subformat
	for i := range p.Intervals {
		p.Intervals[i].Max = ^uint32(0)
	}
	set := func(parameter int, value uint32) { p.Intervals[parameter-8] = interval{value, value, 4} }
	set(8, 16)
	set(9, uint32(16*format.Channels))
	set(10, uint32(format.Channels))
	set(11, uint32(format.Rate))
	set(13, periodFrames)
	set(15, bufferFrames/periodFrames)
	set(17, bufferFrames)
	p.RMask = ^uint32(0)
	return p
}

func open(ctx context.Context, path string, format pcm.Format) (pcm.Sink, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := format.Valid(); err != nil {
		return nil, err
	}
	if format.Format != pcm.S16LE {
		return nil, errors.New("alsa: only S16LE is supported")
	}
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("alsa: open %s: %w", path, err)
	}
	ok := false
	defer func() {
		if !ok {
			syscall.Close(fd)
		}
	}()
	p := hardware(format)
	if err := ioctl(fd, request(3, 0x11, unsafe.Sizeof(p)), unsafe.Pointer(&p)); err != nil {
		return nil, fmt.Errorf("alsa: hardware parameters: %w", err)
	}
	boundary := uint64(bufferFrames)
	for boundary <= (1<<63-1-bufferFrames)/2 {
		boundary *= 2
	}
	sw := swParams{PeriodStep: 1, AvailMin: periodFrames, XferAlign: 1,
		StartThreshold: 4 * periodFrames, StopThreshold: bufferFrames, Boundary: boundary}
	if err := ioctl(fd, request(3, 0x13, unsafe.Sizeof(sw)), unsafe.Pointer(&sw)); err != nil {
		return nil, fmt.Errorf("alsa: software parameters: %w", err)
	}
	if err := ioctl(fd, prepare, nil); err != nil {
		return nil, fmt.Errorf("alsa: prepare: %w", err)
	}
	ok = true
	return &sink{fd: fd, format: format}, nil
}

type sink struct {
	mu         sync.Mutex
	fd         int
	format     pcm.Format
	frames     int64
	generation uint64
	closed     bool
	underruns  uint64
}

func (s *sink) Write(ctx context.Context, block pcm.Block) error {
	if block.Format != s.format || len(block.Data)%(s.format.Channels*2) != 0 {
		return errors.New("alsa: PCM format mismatch")
	}
	s.mu.Lock()
	generation := s.generation
	s.mu.Unlock()
	data := block.Data
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return errors.New("alsa: sink closed")
		}
		if generation != s.generation {
			s.mu.Unlock()
			return nil
		}
		x := transfer{Data: unsafe.Pointer(&data[0]), Frames: uint64(len(data) / (s.format.Channels * 2))}
		err := ioctl(s.fd, request(1, 0x50, unsafe.Sizeof(x)), unsafe.Pointer(&x))
		runtime.KeepAlive(data)
		if err == nil && x.Result < 0 {
			err = syscall.Errno(-x.Result)
		}
		if errors.Is(err, syscall.EPIPE) {
			s.underruns++
			err = ioctl(s.fd, prepare, nil)
			if err == nil {
				err = syscall.EAGAIN
			}
		}
		if err == nil && x.Result > 0 {
			if uint64(x.Result) > x.Frames {
				s.mu.Unlock()
				return errors.New("alsa: invalid frame count")
			}
			s.frames += x.Result
			data = data[int(x.Result)*s.format.Channels*2:]
		}
		s.mu.Unlock()
		if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			return fmt.Errorf("alsa: write: %w", err)
		}
		if err != nil || x.Result == 0 {
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}

func (s *sink) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("alsa: sink closed")
	}
	s.generation++
	if err := ioctl(s.fd, drop, nil); err != nil {
		return err
	}
	return ioctl(s.fd, prepare, nil)
}

func (s *sink) Position() pcm.Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := pcm.Position{Frames: s.frames, Underruns: s.underruns}
	if s.closed {
		return p
	}
	var delay int64
	if err := ioctl(s.fd, request(2, 0x21, unsafe.Sizeof(delay)), unsafe.Pointer(&delay)); err == nil && delay >= 0 {
		p.Frames -= delay
		p.Latency = time.Duration(delay) * time.Second / time.Duration(s.format.Rate)
		p.ObservedAt, p.Timed = time.Now(), true
	}
	return p
}

func (s *sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = ioctl(s.fd, drop, nil)
	return syscall.Close(s.fd)
}
