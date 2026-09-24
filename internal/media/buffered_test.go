package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
)

type shortReader struct{ io.Reader }

func (r shortReader) Read(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return r.Reader.Read(p)
}

type eofSignalReader struct {
	io.Reader
	done chan struct{}
}

func (r eofSignalReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		close(r.done)
	}
	return n, err
}

func TestBufferedReadAheadWhilePlaybackWaits(t *testing.T) {
	var wire bytes.Buffer
	for i := 0; i < 100; i++ {
		_ = binary.Write(&wire, binary.BigEndian, uint16(38))
		wire.Write(make([]byte, 36))
	}
	r := eofSignalReader{Reader: &wire, done: make(chan struct{})}
	release := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { done <- queueBuffered(ctx, r, func([]byte) error { <-release; return nil }) }()
	select {
	case <-r.done: // All input read even though the first playout is waiting.
	case <-ctx.Done():
		close(release)
		t.Fatal("TCP reader blocked behind playout")
	}
	close(release)
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("queue returned %v", err)
	}
}

func TestBufferedFraming(t *testing.T) {
	var wire bytes.Buffer
	packets := [][]byte{bytes.Repeat([]byte{0x31}, 36), bytes.Repeat([]byte{0x72}, 1234)}
	for _, packet := range packets {
		_ = binary.Write(&wire, binary.BigEndian, uint16(len(packet)+2))
		wire.Write(packet)
	}
	count := 0
	err := readBuffered(shortReader{&wire}, func(packet []byte) error {
		if !bytes.Equal(packet, packets[count]) {
			t.Fatalf("packet %d mismatch", count)
		}
		count++
		return nil
	})
	if !errors.Is(err, io.EOF) || count != 2 {
		t.Fatalf("read = %d packets, %v", count, err)
	}
}

func TestBufferedRejectsShortOrTruncatedFrames(t *testing.T) {
	for _, wire := range [][]byte{{0, 0}, {0, 1}, {0, 37}, {0, 38, 1, 2, 3}} {
		err := readBuffered(bytes.NewReader(wire), func([]byte) error { t.Fatal("handler called"); return nil })
		if err == nil {
			t.Fatalf("accepted %x", wire)
		}
	}
}
