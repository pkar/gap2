package airplay2

import (
	"testing"
	"time"
)

func TestOutputOffsetBounds(t *testing.T) {
	for _, offset := range []time.Duration{-501 * time.Millisecond, -500 * time.Millisecond, 0, 500 * time.Millisecond, 501 * time.Millisecond} {
		r, err := New(Config{OutputOffset: offset})
		valid := offset >= -500*time.Millisecond && offset <= 500*time.Millisecond
		if (err == nil) != valid {
			t.Fatalf("offset %s: %v", offset, err)
		}
		if r != nil {
			r.Close()
		}
	}
}

func TestNewDefaults(t *testing.T) {
	r, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()
	if got := r.Status().State; got != StateIdle {
		t.Fatalf("state = %v, want idle", got)
	}
}

func TestNewInvalidListenAddr(t *testing.T) {
	if _, err := New(Config{ListenAddr: "badaddr"}); err == nil {
		t.Fatal("New accepted invalid listen address")
	}
}

func TestDefaultLimitsNonZero(t *testing.T) {
	l := DefaultLimits()
	if l.MaxConnections <= 0 ||
		l.MaxRequestBytes <= 0 ||
		l.MaxHeaderBytes <= 0 ||
		l.MaxHeaders <= 0 ||
		l.MaxBodyBytes <= 0 ||
		l.PairTimeout <= 0 ||
		l.ReadHeaderTimeout <= 0 {
		t.Fatalf("DefaultLimits has zero fields: %+v", l)
	}
}

func TestLimitsWithDefaults(t *testing.T) {
	l := Limits{}.withDefaults()
	d := DefaultLimits()
	if l.MaxConnections != d.MaxConnections {
		t.Fatalf("MaxConnections not defaulted: %d != %d", l.MaxConnections, d.MaxConnections)
	}
}

func TestConfigValidateEmptyName(t *testing.T) {
	if err := (Config{ListenAddr: ":7000"}).Validate(); err == nil {
		t.Fatal("expected empty name to fail validation")
	}
}
