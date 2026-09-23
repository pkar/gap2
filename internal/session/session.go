// Package session models the lifecycle of one receiver session. The actual
// legal transition graph is validated against captured sender behavior during
// later phases; this machine enforces a conservative subset and is not yet
// wired into the network stack.
package session

import "fmt"

// Phase is a lifecycle phase of one session.
type Phase uint8

const (
	PhaseIdle Phase = iota
	PhasePairing
	PhaseAuthorized
	PhaseTimingConfigured
	PhaseStreamsConfigured
	PhaseReady
	PhasePlaying
	PhasePaused
	PhaseTearingDown
	PhaseClosed
	PhaseFailed
)

func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhasePairing:
		return "pairing"
	case PhaseAuthorized:
		return "authorized"
	case PhaseTimingConfigured:
		return "timing-configured"
	case PhaseStreamsConfigured:
		return "streams-configured"
	case PhaseReady:
		return "ready"
	case PhasePlaying:
		return "playing"
	case PhasePaused:
		return "paused"
	case PhaseTearingDown:
		return "tearing-down"
	case PhaseClosed:
		return "closed"
	case PhaseFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// allowed maps each phase to the phases it may transition into.
var allowed = map[Phase]map[Phase]bool{
	PhaseIdle:              {PhasePairing: true, PhaseClosed: true, PhaseFailed: true},
	PhasePairing:           {PhaseAuthorized: true, PhaseClosed: true, PhaseFailed: true},
	PhaseAuthorized:        {PhaseTimingConfigured: true, PhaseClosed: true, PhaseFailed: true},
	PhaseTimingConfigured:  {PhaseStreamsConfigured: true, PhaseClosed: true, PhaseFailed: true},
	PhaseStreamsConfigured: {PhaseReady: true, PhaseClosed: true, PhaseFailed: true},
	PhaseReady:             {PhasePlaying: true, PhaseClosed: true, PhaseFailed: true},
	PhasePlaying:           {PhasePaused: true, PhaseReady: true, PhaseTearingDown: true, PhaseFailed: true},
	PhasePaused:            {PhasePlaying: true, PhaseReady: true, PhaseTearingDown: true, PhaseFailed: true},
	PhaseTearingDown:       {PhaseClosed: true, PhaseFailed: true},
	PhaseClosed:            {},
	PhaseFailed:            {PhaseClosed: true},
}

// Machine tracks one session's phase.
type Machine struct {
	phase Phase
}

// New returns a machine in the idle phase.
func New() *Machine {
	return &Machine{phase: PhaseIdle}
}

// Phase returns the current phase.
func (m *Machine) Phase() Phase {
	return m.phase
}

// Transition moves to next when the move is legal.
func (m *Machine) Transition(next Phase) error {
	if !allowed[m.phase][next] {
		return fmt.Errorf("session: illegal transition %s -> %s", m.phase, next)
	}
	m.phase = next
	return nil
}
