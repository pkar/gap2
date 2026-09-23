// Package observability provides small thread-safe counters used to expose
// internal health without importing a metrics framework.
package observability

import "sync"

// Counters is a thread-safe set of named int64 counters.
type Counters struct {
	mu     sync.Mutex
	values map[string]int64
}

// NewCounters returns an empty counter set.
func NewCounters() *Counters {
	return &Counters{values: make(map[string]int64)}
}

// Add adds delta to the named counter.
func (c *Counters) Add(name string, delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[name] += delta
}

// Set replaces the named counter's value.
func (c *Counters) Set(name string, value int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[name] = value
}

// Snapshot is a stable copy of all counters.
type Snapshot struct {
	Values map[string]int64
}

// Snapshot returns a copy of the current counters.
func (c *Counters) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return Snapshot{Values: out}
}
