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

// Decoder decodes one audio access unit into interleaved 16-bit PCM. The
// concrete AAC-LC and ALAC implementations plug in here.
type Decoder interface {
	// Decode decodes au and returns a PCM block whose Format matches the
	// stream format. Implementations may return a zero-length block.
	Decode(au []byte) (pcm.Block, error)
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
	mu        sync.Mutex
	id        string
	state     State
	media     *sdp.Media
	format    pcm.Format
	transport Transport
	server    int
	decoder   Decoder
	sink      pcm.Sink
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
	case "mpeg4-generic":
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

// IngestRTP handles one RTP packet: it verifies the payload type, extracts
// AAC access units, decodes them, and writes the resulting PCM to the sink.
func (s *Stream) IngestRTP(ctx context.Context, pkt []byte) error {
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
	if pt != 0 && int(p.PayloadType) != pt {
		return fmt.Errorf("stream: payload type %d, want %d", p.PayloadType, pt)
	}
	if media == nil {
		return fmt.Errorf("stream: %w: no announced media", ErrUnsupported)
	}
	if media.Encoding != "mpeg4-generic" {
		return fmt.Errorf("stream: %w: %s ingest not implemented", ErrUnsupported, media.Encoding)
	}
	aus, err := rtp.ParseAACPayload(p.Payload)
	if err != nil {
		return err
	}
	for _, au := range aus {
		block, err := s.decoder.Decode(au.Data)
		if err != nil {
			return err
		}
		if err := s.sink.Write(ctx, block); err != nil {
			return err
		}
	}
	return nil
}
