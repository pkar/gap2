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

	syncs    map[syncKey]syncSample
	sourceID [8]byte
	// samples counts Follow_Up messages since the master was selected.
	samples uint64

	// anchor maps an RTP timestamp to a grandmaster presentation time, as
	// announced by the sender's SETRATEANCHORI request. anchorSet reports
	// whether a usable anchor (positive rate) has been recorded.
	anchor    Anchor
	anchorSet bool
}

type syncKey struct {
	clock          [8]byte
	port, sequence uint16
	domain         uint8
}
type syncSample struct {
	local      uint64
	correction int64
}

func keyFor(h Header) syncKey { return syncKey{h.ClockID, h.SourcePort, h.Sequence, h.Domain} }

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

// Status is a compact snapshot of a Clock for reporting. It folds the offset
// estimate and playback anchor into two booleans plus the raw offset.
type Status struct {
	// Synced reports whether an offset estimate is available.
	Synced bool
	// OffsetNs is the smoothed master-minus-local offset in nanoseconds.
	OffsetNs int64
	// Anchored reports whether a usable playback anchor has been recorded.
	Anchored bool
}

// Status returns a snapshot suitable for status reporting.
func (c *Clock) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{Synced: c.valid, OffsetNs: c.offsetNs, Anchored: c.anchorSet}
}

// NewClock returns an empty Clock.
func NewClock() *Clock { return &Clock{} }

// HandleAnnounce records the grandmaster identity from an Announce message.
// The clock identity is taken from the grandmasterIdentity field carried in
// the Announce body (m.Grandmaster). The offset is reset if the grandmaster
// changes, because the new master's epoch may differ. A genuine change from
// one master to another also clears the playback anchor, whose
// RTP-frame-to-grandmaster-time mapping refers to the old master's epoch and
// is meaningless for the new one; the zero-to-first-master transition keeps
// any anchor already recorded, since there is no prior epoch for it to be
// stale against.
func (c *Clock) HandleAnnounce(m Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m.Grandmaster == c.masterID {
		return
	}
	changed := c.masterID != [8]byte{}
	c.masterID = m.Grandmaster
	c.sourceID = m.Header.ClockID
	c.syncs = nil
	c.offsetNs = 0
	c.valid = false
	c.samples = 0
	if changed {
		c.anchorSet = false
	}
}

// HandleSync retains the event packet's receive time for its Follow_Up.
// Follow_Up network transit time must not become clock error.
func (c *Clock) HandleSync(m Message, localNs uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sourceID != [8]byte{} && m.Header.ClockID != c.sourceID {
		return
	}
	if c.syncs == nil || len(c.syncs) >= 8 {
		c.syncs = make(map[syncKey]syncSample)
	}
	c.syncs[keyFor(m.Header)] = syncSample{localNs, m.Header.CorrectionNs}
}

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
	if c.sourceID != [8]byte{} && m.Header.ClockID != c.sourceID {
		return
	}
	correction := m.Header.CorrectionNs
	if sample, ok := c.syncs[keyFor(m.Header)]; ok {
		localNs = sample.local
		correction += sample.correction
		delete(c.syncs, keyFor(m.Header))
	}

	raw := int64(m.Precise.AsNanos()) + correction - int64(localNs)
	c.localNs = localNs
	c.samples++

	if !c.valid {
		c.offsetNs = raw
		c.valid = true
		return
	}
	// Average against the estimate, not the previous raw sample. Integrating
	// asymmetrically weighted sample differences makes ordinary jitter drift.
	c.offsetNs += (raw - c.offsetNs) / 8
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

// SetAnchor records the sender's playback anchor: the RTP frame Frame will be
// presented at grandmaster time MasterNs, and subsequent frames follow at the
// sample rate Rate. An anchor with a non-positive rate is ignored.
func (c *Clock) SetAnchor(a Anchor) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a.Rate <= 0 {
		return
	}
	c.anchor = a
	c.anchorSet = true
}

// ClearAnchor starts a new RTP timestamp epoch while retaining PTP sync.
func (c *Clock) ClearAnchor() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.anchorSet = false
}

// ChangeRate preserves the transition frame's presentation time when a
// buffered stream changes its sample clock without replacing its transport.
func (c *Clock) ChangeRate(frame uint32, rate int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.anchorSet || rate <= 0 || rate == c.anchor.Rate {
		return
	}
	ns, ok := c.anchor.MasterTime(frame)
	if !ok {
		return
	}
	c.anchor.Frame = frame
	c.anchor.MasterNs = ns
	c.anchor.Rate = rate
}

// Anchor returns the most recent playback anchor and whether one is set.
func (c *Clock) Anchor() (Anchor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.anchor, c.anchorSet
}

// FrameLocalTime returns the local monotonic time, in nanoseconds, at which
// the RTP frame with timestamp frame should be presented. It combines the
// playback anchor (RTP timestamp -> grandmaster time) with the offset estimate
// (grandmaster time -> local time), and returns ok=false when either the
// anchor or the offset estimate is unavailable.
func (c *Clock) FrameLocalTime(frame uint32) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.anchorSet || !c.valid {
		return 0, false
	}
	return c.anchor.LocalTime(frame, c.offsetNs)
}
