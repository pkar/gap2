//go:build linux && (amd64 || arm64)

package alsa

import (
	"context"
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/pkar/gap2/pcm"
)

func TestKernelABI(t *testing.T) {
	if unsafe.Sizeof(hwParams{}) != 608 || unsafe.Sizeof(swParams{}) != 136 || unsafe.Sizeof(transfer{}) != 24 {
		t.Fatal("ALSA structure layout does not match the 64-bit kernel ABI")
	}
	if request(3, 0x11, 608) != 0xc2604111 || request(1, 0x50, 24) != 0x40184150 {
		t.Fatal("ALSA ioctl request encoding mismatch")
	}
}

// Opt-in live test. Writes half a second of silence, verifies that hardware
// consumes frames, flushes, and checks that the closed sink rejects writes.
func TestDevicePlayback(t *testing.T) {
	path := os.Getenv("GAP2_ALSA_TEST_DEVICE")
	if path == "" {
		t.Skip("set GAP2_ALSA_TEST_DEVICE for the hardware test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	format := pcm.Format{Rate: 44100, Channels: 2, Format: pcm.S16LE}
	s, err := Device(path).Open(ctx, format)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, err := pcm.NewBlock(format, 22050)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := s.Write(ctx, b); err != nil {
		t.Fatal(err)
	}
	p := s.Position()
	t.Logf("played=%d latency=%s elapsed=%s", p.Frames, p.Latency, time.Since(started))
	if !p.Timed || p.Frames <= 0 || time.Since(started) < 250*time.Millisecond {
		t.Fatal("hardware did not consume PCM in real time")
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, b); err == nil {
		t.Fatal("write after Close succeeded")
	}
}
