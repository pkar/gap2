package airplay2

import (
	"context"
	"errors"
	"testing"
)

func TestRunNotImplemented(t *testing.T) {
	r, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Run(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Run = %v, want ErrNotImplemented", err)
	}
	if got := r.Status().State; got != StateStopped {
		t.Fatalf("state after Run = %v, want stopped", got)
	}
}

func TestRunTwice(t *testing.T) {
	r, _ := New(DefaultConfig())
	defer r.Close()
	if err := r.Run(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	r, _ := New(DefaultConfig())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := r.Status().State; got != StateStopped {
		t.Fatalf("state = %v", got)
	}
}

func TestEventsClosedOnClose(t *testing.T) {
	r, _ := New(DefaultConfig())
	events := r.Events()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// The channel holds a buffered state event before it is closed; drain it
	// and verify the channel eventually closes.
	for {
		_, ok := <-events
		if !ok {
			return
		}
	}
}
