package stream

import (
	"context"
	"encoding/binary"
	"github.com/pkar/gap2/internal/aac"
	"github.com/pkar/gap2/pcm"
	"math"
	"os"
	"runtime"
	"testing"
	"time"
)

type countingSink struct {
	frames int64
	writes int
	format pcm.Format
	energy float64
}

func (s *countingSink) Write(_ context.Context, b pcm.Block) error {
	s.frames += int64(b.Frames())
	s.writes++
	s.format = b.Format
	for i := 0; i < len(b.Data); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(b.Data[i:])))
		s.energy += v * v
	}
	return nil
}
func (s *countingSink) Flush(context.Context) error { return nil }
func (s *countingSink) Position() pcm.Position      { return pcm.Position{Frames: s.frames} }
func (s *countingSink) Close() error                { return nil }

type fixture struct {
	word         uint32
	rate, frames int
	packets      [][]byte
}

func switchingFixtures(t *testing.T) []fixture {
	t.Helper()
	var result []fixture
	for _, entry := range []struct {
		name string
		word uint32
		alac bool
	}{{"stereo-44100", 0x16000000, false}, {"surround-48000", 0x27000000, false}, {"surround71-48000", 0x28000000, false}, {"stereo24-48000", 0x15000000, true}} {
		path := "../aac/testdata/" + entry.name + ".aac"
		if entry.alac {
			path = "../alac/testdata/" + entry.name + ".frames"
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f := fixture{word: entry.word, rate: 48000, frames: 1024}
		if entry.word == 0x16000000 {
			f.rate = 44100
		}
		if entry.alac {
			f.frames = 4096
		}
		for len(data) > 0 {
			n, head := 0, 0
			if entry.alac {
				n = int(binary.BigEndian.Uint32(data)) + 4
				head = 4
			} else {
				h, err := aac.ParseHeader(data)
				if err != nil {
					t.Fatal(err)
				}
				n, head = h.FrameLength, h.HeaderLength
			}
			f.packets = append(f.packets, append([]byte(nil), data[head:n]...))
			data = data[n:]
		}
		result = append(result, f)
	}
	return result
}
func TestBufferedCodecChangesKeepOutput(t *testing.T) { runSwitching(t, 2*time.Second) }
func TestBufferedLongPlayback(t *testing.T) {
	value := os.Getenv("GAP2_SOAK_DURATION")
	if value == "" {
		t.Skip("set GAP2_SOAK_DURATION, e.g. 30m, for accelerated media-duration soak")
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		t.Fatal("invalid soak duration")
	}
	runSwitching(t, d)
}
func runSwitching(t *testing.T, duration time.Duration) {
	t.Helper()
	fixtures := switchingFixtures(t)
	sink := &countingSink{}
	output := pcm.Format{Rate: 48000, Channels: 2, Format: pcm.S16LE}
	converter, err := pcm.NewConverter(sink, output)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := bufferedMedia(fixtures[0].word)
	decoder, _ := NewDecoder(m)
	s := New("switch", decoder, converter)
	if err := s.Announce(m); err != nil {
		t.Fatal(err)
	}
	if err := s.Setup(Transport{Protocol: "RTP/AVP/TCP"}, 7000); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(); err != nil {
		t.Fatal(err)
	}
	var elapsed time.Duration
	var seq, ts uint32
	changes := 0
	s.SetFormatHandler(func(uint32, pcm.Format) { changes++ })
	started := time.Now()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	expected := 0.0
	for cycle := 0; elapsed < duration; cycle++ {
		f := fixtures[cycle%len(fixtures)] // switch every ~0.16s using complete independent encoded segments
		for i, au := range f.packets {
			frames := f.frames
			if f.word == 0x15000000 && i == len(f.packets)-1 {
				frames = 7680 - 4096
			}
			packet := rtpPacketTS(au, ts)
			binary.BigEndian.PutUint32(packet[8:12], f.word)
			if err := s.IngestBufferedRTP(context.Background(), packet, seq); err != nil {
				t.Fatalf("cycle %d word %#x: %v", cycle, f.word, err)
			}
			seq++
			ts += uint32(frames)
			dt := time.Duration(frames) * time.Second / time.Duration(f.rate)
			elapsed += dt
			// ALAC's final packet can be shorter than its nominal frame length.
			expected += float64(frames) * 48000 / float64(f.rate)
			if os.Getenv("GAP2_SOAK_REALTIME") == "1" {
				if delay := time.Until(started.Add(elapsed)); delay > 0 {
					time.Sleep(delay)
				}
			}
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if sink.format != output || sink.energy == 0 || changes < 4 {
		t.Fatalf("output=%v energy=%v changes=%d", sink.format, sink.energy, changes)
	}
	if delta := math.Abs(float64(sink.frames) - expected); delta > float64(changes)+64 {
		t.Fatalf("frame drift %.2f: got %d expected %.2f", delta, sink.frames, expected)
	}
	if after.HeapAlloc > before.HeapAlloc+16<<20 {
		t.Fatalf("retained heap grew by %d", after.HeapAlloc-before.HeapAlloc)
	}
	t.Logf("media=%s wall=%s packets=%d changes=%d frames=%d heapBefore=%d heapAfter=%d", elapsed, time.Since(started), seq, changes, sink.frames, before.HeapAlloc, after.HeapAlloc)
}
