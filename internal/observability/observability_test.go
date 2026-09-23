package observability

import "testing"

func TestCounters(t *testing.T) {
	c := NewCounters()
	c.Add("x", 2)
	c.Add("x", 3)
	c.Set("y", 9)
	s := c.Snapshot()
	if s.Values["x"] != 5 || s.Values["y"] != 9 {
		t.Fatalf("snapshot = %+v", s.Values)
	}
	// Mutating a snapshot must not affect the counters.
	s.Values["x"] = 100
	if c.Snapshot().Values["x"] != 5 {
		t.Fatal("snapshot aliases internal state")
	}
}
