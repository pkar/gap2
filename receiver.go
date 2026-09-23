package airplay2

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Status is a stable snapshot of a Receiver.
type Status struct {
	State         State
	StartedAt     time.Time
	SessionCount  int
	DroppedEvents int64
}

// Receiver owns a single AirPlay 2 receiver instance.
//
// A Receiver is created with New and, for now, used for at most one Run call.
// The public contract keeps construction side-effect-free: no networking,
// file, or goroutine work happens until Run is called.
type Receiver struct {
	cfg Config
	log *slog.Logger

	events chan Event

	runOnce        atomic.Bool
	eventsCloseOne sync.Once

	mu           sync.Mutex
	state        State
	startedAt    time.Time
	sessionCount int
	closed       bool
	eventsClosed bool

	droppedEvents atomic.Int64
}

// New validates cfg and returns a Receiver without performing I/O or starting
// goroutines.
func New(cfg Config) (*Receiver, error) {
	cfg = cfg.normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Receiver{
		cfg:    cfg,
		log:    cfg.Logger,
		events: make(chan Event, 64),
		state:  StateIdle,
	}, nil
}

// Run starts the receiver and blocks until ctx is cancelled or an
// unrecoverable error occurs. A nil return value means clean shutdown.
func (r *Receiver) Run(ctx context.Context) error {
	if r.runOnce.Swap(true) {
		return ErrAlreadyRunning
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	r.setStateLocked(StateStarting)
	r.mu.Unlock()

	defer r.finish(StateStopped)

	// Phase 1: discovery, pairing, media transport, and playback are not
	// implemented yet. Report that honestly rather than idling silently.
	return ErrNotImplemented
}

// Events returns the receiver's notification channel. It is closed when the
// receiver stops. Slow subscribers never block protocol or audio workers;
// dropped events are counted in Status.
func (r *Receiver) Events() <-chan Event {
	return r.events
}

// Status returns a stable snapshot of the receiver.
func (r *Receiver) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{
		State:         r.state,
		StartedAt:     r.startedAt,
		SessionCount:  r.sessionCount,
		DroppedEvents: r.droppedEvents.Load(),
	}
}

// Close shuts the receiver down and is idempotent. In this phase there are no
// owned goroutines to join, so callers cancel the context passed to Run to
// stop an active Run.
func (r *Receiver) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		if r.state == StateIdle || r.state == StateStarting {
			r.setStateLocked(StateStopped)
		}
	}
	r.mu.Unlock()

	r.closeEvents()
	return nil
}

func (r *Receiver) setStateLocked(s State) {
	if r.state == s {
		return
	}
	r.state = s
	if s == StateStarting {
		r.startedAt = time.Now()
	}
	r.emitLocked(Event{Type: EventStateChanged, State: s, At: time.Now()})
}

func (r *Receiver) emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitLocked(e)
}

func (r *Receiver) emitLocked(e Event) {
	if r.eventsClosed {
		return
	}
	select {
	case r.events <- e:
	default:
		r.droppedEvents.Add(1)
	}
}

func (r *Receiver) finish(s State) {
	r.mu.Lock()
	r.setStateLocked(s)
	r.mu.Unlock()
	r.closeEvents()
}

func (r *Receiver) closeEvents() {
	r.eventsCloseOne.Do(func() {
		r.mu.Lock()
		r.eventsClosed = true
		r.mu.Unlock()
		close(r.events)
	})
}
