// Package playout implements the buffered, timed PCM playback stage used by
// the receiver. It is deliberately independent of any codec: upstream stages
// decode audio into pcm.Block values, push them into a Player, and the Player
// writes them to a pcm.Sink at the sample rate implied by the format.
//
// A Player auto-starts once the buffered duration reaches Config.Target and
// then keeps consuming the queue at realtime rate. Backpressure is applied to
// Push when the queue reaches Config.Max.
package playout

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/pkar/gap2/internal/timing"
	"github.com/pkar/gap2/pcm"
)

// Sentinel errors returned by Player methods.
var (
	ErrClosed         = errors.New("playout: player closed")
	ErrFormatMismatch = errors.New("playout: block format does not match player")
	ErrInvalidConfig  = errors.New("playout: invalid config")
	ErrBlockTooLarge  = errors.New("playout: block exceeds maximum buffer")
)

// State is the coarse playback state exposed for status reporting.
type State uint8

const (
	StateIdle State = iota
	StateBuffering
	StatePlaying
	StatePaused
	StateClosed
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateBuffering:
		return "buffering"
	case StatePlaying:
		return "playing"
	case StatePaused:
		return "paused"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// Config configures a Player.
type Config struct {
	// Target is the minimum buffered duration before playback starts.
	Target time.Duration
	// Max is the maximum buffered duration before Push blocks. It must be
	// greater than or equal to Target.
	Max time.Duration
	// Clock is the time source used by Run and Resume. Zero means the system
	// clock.
	Clock timing.Clock
}

// Position describes the player's current queue and presentation state.
type Position struct {
	// PlayedFrames is the number of frames written to the sink since the
	// current generation started.
	PlayedFrames int64
	// BufferedFrames is the number of decoded frames queued but not written.
	BufferedFrames int64
	// BufferedDuration is BufferedFrames expressed as wall time.
	BufferedDuration time.Duration
	// Presentation is the scheduled write time of the next frame. It is zero
	// before playback starts.
	Presentation time.Time
}

// Player buffers PCM blocks and writes them to a sink on a realtime schedule.
type Player struct {
	sink   pcm.Sink
	format pcm.Format
	target time.Duration
	max    time.Duration
	clock  timing.Clock

	targetFrames int64
	maxFrames    int64

	mu           sync.Mutex
	cond         *sync.Cond
	blocks       [][]byte
	queuedFrames int64
	nextFrame    int64
	started      bool
	paused       bool
	anchor       time.Time
	closed       bool
	err          error
}

// New returns a Player writing to sink with the given PCM format.
func New(sink pcm.Sink, format pcm.Format, cfg Config) (*Player, error) {
	if sink == nil {
		return nil, errors.New("playout: nil sink")
	}
	if err := format.Valid(); err != nil {
		return nil, err
	}
	if cfg.Target <= 0 {
		cfg.Target = 500 * time.Millisecond
	}
	if cfg.Max <= 0 {
		cfg.Max = 3 * time.Second
	}
	if cfg.Max < cfg.Target {
		return nil, ErrInvalidConfig
	}
	if cfg.Clock == nil {
		cfg.Clock = timing.SystemClock{}
	}

	targetFrames := timing.DurationToFrames(cfg.Target, format.Rate)
	maxFrames := timing.DurationToFrames(cfg.Max, format.Rate)
	if targetFrames <= 0 || maxFrames < targetFrames {
		return nil, ErrInvalidConfig
	}

	p := &Player{
		sink:         sink,
		format:       format,
		target:       cfg.Target,
		max:          cfg.Max,
		clock:        cfg.Clock,
		targetFrames: targetFrames,
		maxFrames:    maxFrames,
	}
	p.cond = sync.NewCond(&p.mu)
	return p, nil
}

// Push queues a copy of block, applying backpressure once the buffer reaches
// Config.Max. It returns ctx.Err() if ctx is cancelled while waiting.
func (p *Player) Push(ctx context.Context, block pcm.Block) error {
	if err := block.Format.Valid(); err != nil {
		return err
	}
	if block.Format != p.format {
		return ErrFormatMismatch
	}
	frames := int64(block.Frames())
	if frames == 0 {
		return nil
	}
	if frames > p.maxFrames {
		return ErrBlockTooLarge
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	if p.err != nil {
		return p.err
	}

	// Wake any waiter when ctx is cancelled.
	stop := make(chan struct{})
	defer close(stop)
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				p.cond.Broadcast()
			case <-stop:
			}
		}()
	}

	for p.maxFrames > 0 && p.bufferedLocked()+frames > p.maxFrames {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.err != nil {
			return p.err
		}
		if p.closed {
			return ErrClosed
		}
		p.cond.Wait()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.err != nil {
		return p.err
	}
	if p.closed {
		return ErrClosed
	}

	p.blocks = append(p.blocks, append([]byte(nil), block.Data...))
	p.queuedFrames += frames
	p.cond.Broadcast()
	return nil
}

// Advance starts playback when the target is reached and writes every block
// whose scheduled time is at or before now. It is safe to call from a single
// driver goroutine; Push and Flush may run concurrently.
func (p *Player) Advance(now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	if p.err != nil {
		return p.err
	}

	if !p.started {
		if !p.paused && p.bufferedLocked() >= p.targetFrames {
			p.anchor = now
			p.started = true
		} else {
			return nil
		}
	}
	if p.paused {
		return nil
	}

	for len(p.blocks) > 0 {
		block := pcm.Block{Format: p.format, Data: p.blocks[0]}
		frames := int64(block.Frames())
		due := p.anchor.Add(timing.FramesToDuration(p.nextFrame, p.format.Rate))
		if due.After(now) {
			break
		}
		if err := p.sink.Write(context.Background(), block); err != nil {
			p.err = err
			p.cond.Broadcast()
			return err
		}
		p.blocks[0] = nil
		p.blocks = p.blocks[1:]
		p.nextFrame += frames
		p.cond.Broadcast()
	}
	return nil
}

// Pause stops advancing the presentation clock without discarding the queue.
func (p *Player) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.started {
		return
	}
	p.paused = true
}

// Resume continues playback immediately at now.
func (p *Player) Resume(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.paused {
		return
	}
	p.paused = false
	// Re-anchor so the next queued frame is due exactly at now.
	p.anchor = now.Add(-timing.FramesToDuration(p.nextFrame, p.format.Rate))
}

// Flush discards all queued audio and resets the player to buffering.
func (p *Player) Flush(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrClosed
	}
	p.blocks = nil
	p.queuedFrames = 0
	p.nextFrame = 0
	p.started = false
	p.paused = false
	p.anchor = time.Time{}
	p.cond.Broadcast()
	return p.sink.Flush(ctx)
}

// Close closes the underlying sink and rejects further use.
func (p *Player) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.cond.Broadcast()
	return p.sink.Close()
}

// Position returns a snapshot of playback progress.
func (p *Player) Position() Position {
	p.mu.Lock()
	defer p.mu.Unlock()
	buffered := p.bufferedLocked()
	pos := Position{
		PlayedFrames:     p.nextFrame,
		BufferedFrames:   buffered,
		BufferedDuration: timing.FramesToDuration(buffered, p.format.Rate),
	}
	if p.started {
		pos.Presentation = p.anchor.Add(timing.FramesToDuration(p.nextFrame, p.format.Rate))
	}
	return pos
}

// State returns the coarse playback state.
func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.closed:
		return StateClosed
	case p.paused:
		return StatePaused
	case p.started:
		return StatePlaying
	case p.bufferedLocked() > 0:
		return StateBuffering
	default:
		return StateIdle
	}
}

// Run drives Advance with the configured clock until ctx is cancelled or the
// player is closed. interval bounds polling latency; small values reduce
// playback jitter.
func (p *Player) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 5 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := p.Advance(p.clock.Now()); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *Player) bufferedLocked() int64 {
	return p.queuedFrames - p.nextFrame
}
