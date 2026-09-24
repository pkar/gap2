package stream

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/pkar/gap2/internal/rtp"
	"github.com/pkar/gap2/internal/sdp"
	"github.com/pkar/gap2/pcm"
)

// aacFrameLen is the number of PCM samples in one AAC-LC frame (1024), used to
// advance the RTP timestamp across access units packed into a single RTP
// packet.
const aacFrameLen = 1024

// Decoder decodes one audio access unit into interleaved 16-bit PCM. The
// concrete AAC-LC and ALAC implementations plug in here.
type Decoder interface {
	// Decode decodes au and returns a PCM block whose Format matches the
	// stream format. Implementations may return a zero-length block.
	Decode(au []byte) (pcm.Block, error)
}

// TimedSink is a Sink that additionally receives the source RTP timestamp of
// the first frame in each block. Sinks that implement it can schedule playback
// against a synchronized clock; IngestRTP prefers WriteTimed over Write.
type TimedSink interface {
	pcm.Sink
	WriteTimed(ctx context.Context, frame uint32, block pcm.Block) error
}

// writeSink writes block to s.sink, routing through TimedSink when the sink
// carries a source timestamp.
func (s *Stream) writeSink(ctx context.Context, frame uint32, block pcm.Block) error {
	if ts, ok := s.sink.(TimedSink); ok {
		return ts.WriteTimed(ctx, frame, block)
	}
	return s.sink.Write(ctx, block)
}

// State is the coarse state of one media stream.
type State uint8

const (
	StateNew State = iota
	StateAnnounced
	StateSetup
	StateRecording
	StateTearingDown
	StateClosed
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateNew:
		return "new"
	case StateAnnounced:
		return "announced"
	case StateSetup:
		return "setup"
	case StateRecording:
		return "recording"
	case StateTearingDown:
		return "tearing-down"
	case StateClosed:
		return "closed"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Stream owns the state and pipeline for one media stream.
type Stream struct {
	mu              sync.Mutex
	id              string
	state           State
	media           *sdp.Media
	format          pcm.Format
	transport       Transport
	server          int
	decoder         Decoder
	sink            pcm.Sink
	resetDecoder    bool
	flushActive     bool
	flushUntil      uint32
	flushFrom       uint32
	hasFlushFrom    bool
	flushBySequence bool
	bufferedFormat  uint32
	onFormat        func(uint32, pcm.Format)
}

// New returns a stream that will decode with decoder and write decoded blocks
// to sink.
func New(id string, decoder Decoder, sink pcm.Sink) *Stream {
	return &Stream{id: id, state: StateNew, decoder: decoder, sink: sink}
}

// ID returns the stream identifier.
func (s *Stream) ID() string { return s.id }

// State returns the current stream state.
func (s *Stream) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Media returns the announced SDP media description, or nil before Announce.
func (s *Stream) Media() *sdp.Media {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.media
}

// Format returns the derived PCM format.
func (s *Stream) Format() pcm.Format {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.format
}

// MediaFormat derives the PCM output format for an announced media
// description. It mirrors the derivation performed by Announce and lets callers
// open a matching sink before constructing a stream.
func MediaFormat(m *sdp.Media) (pcm.Format, error) {
	if m == nil {
		return pcm.Format{}, errors.New("stream: nil media")
	}
	rate, channels := 0, 0
	switch m.Encoding {
	case "mpeg4-generic", "AAC":
		rate, channels = m.ClockRate, m.Channels
	case "AppleLossless":
		if m.ALAC == nil {
			return pcm.Format{}, fmt.Errorf("stream: %w: AppleLossless without fmtp", ErrUnsupported)
		}
		rate, channels = m.ALAC.SampleRate, m.ALAC.Channels
	default:
		return pcm.Format{}, fmt.Errorf("stream: %w: %s", ErrUnsupported, m.Encoding)
	}
	format := pcm.Format{Rate: rate, Channels: channels, Format: pcm.S16LE}
	if err := format.Valid(); err != nil {
		return pcm.Format{}, err
	}
	return format, nil
}

// Announce accepts the SDP media description and derives the PCM format.
func (s *Stream) Announce(m *sdp.Media) error {
	format, err := MediaFormat(m)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateNew {
		return fmt.Errorf("stream: announce in state %s", s.state)
	}
	s.media = m
	s.format = format
	s.state = StateAnnounced
	return nil
}

// Setup records the negotiated transport and the receiver port that was
// already bound by the network layer.
func (s *Stream) Setup(client Transport, serverPort int) error {
	if client.Protocol == "" {
		return ErrBadTransport
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateAnnounced {
		return fmt.Errorf("stream: setup in state %s", s.state)
	}
	s.transport = client
	s.server = serverPort
	s.state = StateSetup
	return nil
}

// Record transitions the stream into the recording state.
func (s *Stream) Record() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateSetup {
		return fmt.Errorf("stream: record in state %s", s.state)
	}
	s.state = StateRecording
	return nil
}

// Teardown ends the stream.
func (s *Stream) Teardown() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateClosed {
		return nil
	}
	s.state = StateClosed
	return nil
}

// Recover drops partial decoder/output state after the transport disconnects.
// Pairing, negotiated format and the recording state survive reconnection.
func (s *Stream) Recover(ctx context.Context) error {
	s.mu.Lock()
	s.resetDecoder = true
	s.flushActive = false
	s.mu.Unlock()
	return s.sink.Flush(ctx)
}

// FlushRange removes old encoded packets from a seek or channel change.
// Timestamp comparisons use signed deltas to handle RTP wraparound.
func (s *Stream) FlushRange(until uint32, from *uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushUntil, s.flushActive, s.resetDecoder = until, true, from == nil
	s.flushBySequence = false
	s.hasFlushFrom = from != nil
	if from != nil {
		s.flushFrom = *from
	}
}

// FlushBufferedRange uses the buffered transport's 23-bit sequence counter.
// RTP timestamps can change epochs at the endpoint, so cannot identify which
// queued packets belong to the old channel.
func (s *Stream) FlushBufferedRange(until uint32, from *uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushUntil, s.flushActive, s.resetDecoder = until&0x7fffff, true, from == nil
	s.flushBySequence = true
	s.hasFlushFrom = from != nil
	if from != nil {
		s.flushFrom = *from & 0x7fffff
	}
}

// IngestBufferedRTP retains the sequence bits lost when normalizing to RTP.
func (s *Stream) IngestBufferedRTP(ctx context.Context, pkt []byte, sequence uint32) error {
	return s.ingestRTP(ctx, pkt, sequence, true)
}

// SetFormatHandler installs a notification before ingest starts. It runs on
// the decode goroutine, before the first PCM block in the new format.
func (s *Stream) SetFormatHandler(fn func(uint32, pcm.Format)) { s.onFormat = fn }

// IngestRTP handles one RTP packet: it verifies the payload type, extracts
// AAC access units, decodes them, and writes the resulting PCM to the sink.
func (s *Stream) IngestRTP(ctx context.Context, pkt []byte) error {
	return s.ingestRTP(ctx, pkt, 0, false)
}

func (s *Stream) ingestRTP(ctx context.Context, pkt []byte, sequence uint32, buffered bool) error {
	s.mu.Lock()
	state := s.state
	media := s.media
	pt := 0
	if media != nil {
		pt = media.PayloadType
	}
	s.mu.Unlock()

	if state != StateRecording {
		return fmt.Errorf("stream: ingest in state %s", state)
	}
	p, err := rtp.Parse(pkt)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.flushActive {
		position := p.Timestamp
		delta := func(a, b uint32) int32 { return int32(a - b) }
		if s.flushBySequence {
			position = sequence
			delta = func(a, b uint32) int32 { return int32((a-b)<<9) >> 9 }
		}
		if delta(position, s.flushUntil) >= 0 {
			s.flushActive = false
			s.resetDecoder = true
		} else if !s.hasFlushFrom || delta(position, s.flushFrom) >= 0 {
			s.resetDecoder = true
			s.mu.Unlock()
			return nil
		}
	}
	reset := s.resetDecoder
	s.resetDecoder = false
	s.mu.Unlock()
	if buffered && p.SSRC != 0 && p.SSRC != s.bufferedFormat {
		m, err := bufferedMedia(p.SSRC)
		if err != nil {
			return err
		}
		decoder, err := NewDecoder(m)
		if err != nil {
			return err
		}
		format, err := MediaFormat(m)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.media = m
		s.format = format
		s.mu.Unlock()
		s.decoder = decoder
		s.bufferedFormat = p.SSRC
		media = m
		if s.onFormat != nil {
			s.onFormat(p.Timestamp, format)
		}
	}
	if reset {
		if d, ok := s.decoder.(interface{ Reset() }); ok {
			d.Reset()
		}
	}
	if pt != 0 && int(p.PayloadType) != pt {
		return fmt.Errorf("stream: payload type %d, want %d", p.PayloadType, pt)
	}
	if media == nil {
		return fmt.Errorf("stream: %w: no announced media", ErrUnsupported)
	}
	switch media.Encoding {
	case "mpeg4-generic":
		aus, err := rtp.ParseAACPayload(p.Payload)
		if err != nil {
			return err
		}
		// Consecutive AAC-LC access units are one frame (1024 samples) apart
		// in RTP timestamp units.
		for i, au := range aus {
			block, err := s.decoder.Decode(au.Data)
			if err != nil {
				return err
			}
			if err := s.writeSink(ctx, p.Timestamp+uint32(i)*aacFrameLen, block); err != nil {
				return err
			}
		}
		return nil
	case "AppleLossless", "AAC":
		// Native AirPlay packs exactly one raw access unit per packet.
		block, err := s.decoder.Decode(p.Payload)
		if err != nil {
			return err
		}
		return s.writeSink(ctx, p.Timestamp, block)
	default:
		return fmt.Errorf("stream: %w: %s ingest not implemented", ErrUnsupported, media.Encoding)
	}
}
