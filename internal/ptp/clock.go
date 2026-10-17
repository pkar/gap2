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
	// carried reports that the anchor survived a grandmaster change and is
	// held in local monotonic time: carriedNs is the local time at which
	// anchor.Frame plays. The sender's timeline does not move when the group
	// elects a new grandmaster, so the frames keep their local presentation
	// times until the new master's offset settles and the anchor is rebased
	// onto it.
	carried   bool
	carriedNs uint64

	// stepCount counts consecutive Follow_Up samples that disagree with the
	// offset by more than stepThreshold and agree with each other; stepRaw is
	// the latest of them.
	stepCount int
	stepRaw   int64

	// onMasterChange, when set, is called after a grandmaster change.
	onMasterChange func(MasterChange)
	// onStep, when set, is called after the offset steps.
	onStep func(Step)
	// onSourceChange, when set, is called when Sync is taken from a
	// different clock for the same grandmaster.
	onSourceChange func(SourceChange)
}

// stepThreshold is how far a Follow_Up sample may disagree with the offset
// before it is treated as a jump in the source's time rather than network
// delay. Wi-Fi delay spikes reach tens of milliseconds, rarely more.
const stepThreshold int64 = 250_000_000

// stepConfirm is how many consecutive, mutually consistent samples beyond
// stepThreshold step the offset: half a second at Apple's 125 ms Sync
// interval. A smaller run is a delay spike and is ignored. Averaging toward
// a jump instead would take about 15 seconds per hour of difference to
// settle, and every block scheduled meanwhile plays at the wrong time or is
// dropped as late.
const stepConfirm = 4

// Step describes a jump of the offset to a new source time base.
type Step struct {
	Source       [8]byte
	FromNs, ToNs int64
}

// SourceChange describes a switch of the clock whose Sync messages drive the
// offset, without a change of grandmaster.
type SourceChange struct {
	From, To [8]byte
}

// SetStepHandler registers f to be called, outside the clock's lock, after
// the offset steps to a new source time base.
func (c *Clock) SetStepHandler(f func(Step)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStep = f
}

// SetSourceChangeHandler registers f to be called, outside the clock's lock,
// when the clock switches to the grandmaster's own Sync messages.
func (c *Clock) SetSourceChangeHandler(f func(SourceChange)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onSourceChange = f
}

// rebaseSamples is how many Follow_Up samples from a new grandmaster the
// offset average takes before a carried anchor moves onto it. The first
// sample is a single one-way measurement; rebasing on it would keep its
// network delay as a permanent playback offset. At Apple's 125 ms Sync
// interval this is two seconds, during which the local clock drifts by
// microseconds.
const rebaseSamples = 16

// MasterChange describes a switch from one grandmaster to another.
type MasterChange struct {
	From, To [8]byte
	// Carried reports that the playback anchor is kept in local time until
	// the new grandmaster's offset settles.
	Carried bool
}

// SetMasterChangeHandler registers f to be called, outside the clock's lock,
// after each change from one grandmaster to another. It is not called for
// the first grandmaster.
func (c *Clock) SetMasterChangeHandler(f func(MasterChange)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMasterChange = f
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
// changes, because the new master's epoch may differ.
//
// A change from one master to another happens mid-stream when speakers join
// or leave the group. The playback anchor maps RTP frames to the old
// master's epoch, but the frames' real presentation times do not change, and
// the sender does not re-anchor until playback is paused, seeked, or moves
// to another track. The anchor is therefore carried in local time and
// rebased onto the new master once its offset settles (see HandleFollowUp).
// Without a valid offset there is no local time to carry, and the anchor is
// cleared. The zero-to-first-master transition keeps any anchor already
// recorded, since there is no prior epoch for it to be stale against.
//
// Other members of an AirPlay group relay the grandmaster's time and announce
// it as their grandmaster too. The first Announce heard selects its sender
// as the Sync source, but the grandmaster's own Sync is preferred as soon as
// it announces: a relaying speaker can send Sync in another time base while
// it joins or leaves the grandmaster's timeline.
func (c *Clock) HandleAnnounce(m Message) {
	c.mu.Lock()
	if m.Grandmaster == c.masterID {
		var notify func(SourceChange)
		event := SourceChange{From: c.sourceID, To: m.Header.ClockID}
		if m.Header.ClockID == m.Grandmaster && c.sourceID != m.Grandmaster {
			// Keep the offset: a relay that tracks the grandmaster agrees
			// within a millisecond or so, and a stepped one is caught by
			// the step check on the next samples.
			c.sourceID = m.Header.ClockID
			c.syncs = nil
			c.stepCount = 0
			notify = c.onSourceChange
		}
		c.mu.Unlock()
		if notify != nil {
			notify(event)
		}
		return
	}
	from := c.masterID
	changed := from != [8]byte{}
	if changed && c.anchorSet && !c.carried {
		if c.valid {
			c.carriedNs = uint64(int64(c.anchor.MasterNs) - c.offsetNs)
			c.carried = true
		} else {
			c.anchorSet = false
		}
	}
	c.masterID = m.Grandmaster
	c.sourceID = m.Header.ClockID
	c.syncs = nil
	c.offsetNs = 0
	c.valid = false
	c.samples = 0
	c.stepCount = 0
	notify, event := c.onMasterChange, MasterChange{From: from, To: m.Grandmaster, Carried: c.anchorSet && c.carried}
	c.mu.Unlock()
	if changed && notify != nil {
		notify(event)
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
// adopted directly. A sample more than stepThreshold away is ignored as a
// delay spike unless stepConfirm consecutive samples agree, which steps the
// offset to the source's new time base.
func (c *Clock) HandleFollowUp(m Message, localNs uint64) {
	c.mu.Lock()
	step, notify := c.followUp(m, localNs)
	c.mu.Unlock()
	if step != nil && notify != nil {
		notify(*step)
	}
}

func (c *Clock) followUp(m Message, localNs uint64) (*Step, func(Step)) {
	if c.sourceID != [8]byte{} && m.Header.ClockID != c.sourceID {
		return nil, nil
	}
	correction := m.Header.CorrectionNs
	if sample, ok := c.syncs[keyFor(m.Header)]; ok {
		localNs = sample.local
		correction += sample.correction
		delete(c.syncs, keyFor(m.Header))
	}

	raw := int64(m.Precise.AsNanos()) + correction - int64(localNs)
	var step *Step
	if c.valid && abs(raw-c.offsetNs) > stepThreshold {
		if c.stepCount > 0 && abs(raw-c.stepRaw) <= stepThreshold {
			c.stepCount++
		} else {
			c.stepCount = 1
		}
		c.stepRaw = raw
		if c.stepCount < stepConfirm {
			return nil, nil
		}
		step = &Step{Source: c.sourceID, FromNs: c.offsetNs, ToNs: raw}
		c.valid = false // adopt raw below
	}
	c.stepCount = 0
	c.localNs = localNs
	c.samples++

	if !c.valid {
		c.offsetNs = raw
		c.valid = true
	} else {
		// Average against the estimate, not the previous raw sample.
		// Integrating asymmetrically weighted sample differences makes
		// ordinary jitter drift.
		c.offsetNs += (raw - c.offsetNs) / 8
	}
	if c.carried && c.samples >= rebaseSamples {
		// Move the carried anchor onto the new master: the same frame, the
		// same local presentation time, expressed in the new epoch.
		c.anchor.MasterNs = uint64(int64(c.carriedNs) + c.offsetNs)
		c.anchor.ClockID = c.masterID
		c.carried = false
	}
	return step, c.onStep
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
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
	c.carried = false
}

// ClearAnchor starts a new RTP timestamp epoch while retaining PTP sync.
func (c *Clock) ClearAnchor() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.anchorSet = false
	c.carried = false
}

// ChangeRate preserves the transition frame's presentation time when a
// buffered stream changes its sample clock without replacing its transport.
func (c *Clock) ChangeRate(frame uint32, rate int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.anchorSet || rate <= 0 || rate == c.anchor.Rate {
		return
	}
	if c.carried {
		ns, ok := c.carriedAnchor().MasterTime(frame)
		if !ok {
			return
		}
		c.carriedNs = ns
		c.anchor.MasterNs, _ = c.anchor.MasterTime(frame)
	} else {
		ns, ok := c.anchor.MasterTime(frame)
		if !ok {
			return
		}
		c.anchor.MasterNs = ns
	}
	c.anchor.Frame = frame
	c.anchor.Rate = rate
}

// carriedAnchor is the carried anchor with its time in the local clock.
func (c *Clock) carriedAnchor() Anchor {
	a := c.anchor
	a.MasterNs = c.carriedNs
	return a
}

// Anchor returns the most recent playback anchor and whether one is set.
// While an anchor is carried across a grandmaster change, MasterNs still
// refers to the previous grandmaster's epoch.
func (c *Clock) Anchor() (Anchor, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.anchor, c.anchorSet
}

// FrameLocalTime returns the local monotonic time, in nanoseconds, at which
// the RTP frame with timestamp frame should be presented. It combines the
// playback anchor (RTP timestamp -> grandmaster time) with the offset estimate
// (grandmaster time -> local time), and returns ok=false when either the
// anchor or the offset estimate is unavailable. An anchor carried across a
// grandmaster change is already in local time and needs no offset.
func (c *Clock) FrameLocalTime(frame uint32) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.anchorSet && c.carried {
		return c.carriedAnchor().LocalTime(frame, 0)
	}
	if !c.anchorSet || !c.valid {
		return 0, false
	}
	return c.anchor.LocalTime(frame, c.offsetNs)
}
