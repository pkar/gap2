package ptp

import "sync"

// Clock tracks a remote PTP grandmaster and estimates the offset between the
// local monotonic clock and that grandmaster's clock. It is the receiver-side
// (slave) state: Announce messages select the grandmaster, and Sync/Follow_Up
// pairs drive the offset estimate.
//
// The offset is defined so that
//
//	masterTime = localTime + Offset()
//
// i.e. adding the offset to a local monotonic time yields the corresponding
// grandmaster time. It follows the reference implementation's one-way
// estimate: offset = preciseOriginTimestamp + correction - followUpReceiveTime.
type Clock struct {
	mu sync.Mutex

	// masterID is the selected grandmaster clock identity (zero until an
	// Announce is seen).
	masterID [8]byte
	// offsetNs is the smoothed master-minus-local offset, in nanoseconds.
	offsetNs int64
	// localNs is the local monotonic time of the most recent offset sample.
	localNs uint64
	// valid reports whether offsetNs holds an estimate.
	valid bool

	// raw is the most recent un-smoothed offset sample, used for the
	// exponential filter.
	raw int64
	// samples counts Follow_Up messages since the master was selected.
	samples uint64
}

// Info is a snapshot of the clock's current state.
type Info struct {
	// MasterID is the selected grandmaster clock identity.
	MasterID [8]byte
	// OffsetNs is the smoothed master-minus-local offset.
	OffsetNs int64
	// LocalNs is the local monotonic time (nanoseconds) of the last sample.
	LocalNs uint64
	// Valid reports whether OffsetNs holds an estimate.
	Valid bool
}

// NewClock returns an empty Clock.
func NewClock() *Clock { return &Clock{} }

// HandleAnnounce records the grandmaster identity from an Announce message.
// The clock identity is taken from the grandmasterIdentity field carried in
// the Announce body (m.Grandmaster). The offset is reset if the grandmaster
// changes, because the new master's epoch may differ.
func (c *Clock) HandleAnnounce(m Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m.Grandmaster == c.masterID {
		return
	}
	c.masterID = m.Grandmaster
	c.offsetNs = 0
	c.raw = 0
	c.valid = false
	c.samples = 0
}

// HandleSync records receipt of a Sync message. The offset is not updated
// until the matching Follow_Up arrives, so this is a no-op beyond keeping the
// clock warm; it exists for symmetry with the protocol flow.
func (c *Clock) HandleSync(m Message, localNs uint64) {}

// HandleFollowUp updates the offset estimate using the preciseOriginTimestamp
// of a Follow_Up message received at local monotonic time localNs.
//
// The raw offset is:
//
//	offset = preciseOriginTimestamp + correction - localNs
//
// and it is smoothed with an exponential moving average. The first sample is
// adopted directly.
func (c *Clock) HandleFollowUp(m Message, localNs uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	raw := int64(m.Precise.AsNanos()) + m.Header.CorrectionNs - int64(localNs)
	c.localNs = localNs
	c.samples++

	if !c.valid {
		c.raw = raw
		c.offsetNs = raw
		c.valid = true
		return
	}
	// Exponential smoothing: new = old + (raw-old)/8. Large positive jumps are
	// more likely to be network delay, so they are damped more heavily.
	delta := raw - c.raw
	if delta > 0 {
		delta /= 8
	}
	c.raw = raw
	c.offsetNs += delta / 8
}

// Info returns a snapshot of the clock state.
func (c *Clock) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Info{
		MasterID: c.masterID,
		OffsetNs: c.offsetNs,
		LocalNs:  c.localNs,
		Valid:    c.valid,
	}
}

// Offset returns the smoothed master-minus-local offset and whether an
// estimate is available.
func (c *Clock) Offset() (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offsetNs, c.valid
}

// MasterTime converts a local monotonic time (nanoseconds) to grandmaster
// time. It returns ok=false when no offset estimate is available yet.
func (c *Clock) MasterTime(localNs uint64) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid {
		return 0, false
	}
	return uint64(int64(localNs) + c.offsetNs), true
}

// LocalTime converts a grandmaster time (nanoseconds) to local monotonic
// time. It returns ok=false when no offset estimate is available yet.
func (c *Clock) LocalTime(masterNs uint64) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid {
		return 0, false
	}
	return uint64(int64(masterNs) - c.offsetNs), true
}
