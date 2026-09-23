package session

import "testing"

func TestLegalPath(t *testing.T) {
	m := New()
	path := []Phase{
		PhasePairing,
		PhaseAuthorized,
		PhaseTimingConfigured,
		PhaseStreamsConfigured,
		PhaseReady,
		PhasePlaying,
		PhasePaused,
		PhasePlaying,
		PhaseTearingDown,
		PhaseClosed,
	}
	for _, p := range path {
		if err := m.Transition(p); err != nil {
			t.Fatalf("transition to %s: %v", p, err)
		}
	}
}

func TestIllegalTransition(t *testing.T) {
	m := New()
	if err := m.Transition(PhasePlaying); err == nil {
		t.Fatal("expected illegal transition")
	}
}
