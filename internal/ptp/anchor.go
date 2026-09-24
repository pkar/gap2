package ptp

// Anchor relates an RTP timestamp to a PTP grandmaster time. The AirPlay 2
// sender announces, over the event channel, that the RTP frame with timestamp
// Frame will be presented at grandmaster time MasterNs. Given that anchor, any
// other frame's presentation time follows from the sample rate.
type Anchor struct {
	// Frame is the RTP timestamp (in sample-rate units) the anchor refers to.
	Frame uint32
	// MasterNs is the grandmaster time, in nanoseconds, at which Frame plays.
	MasterNs uint64
	// Rate is the sample rate in Hz.
	Rate int
}

// MasterTime returns the grandmaster time, in nanoseconds, at which the RTP
// frame with timestamp frame should be presented. The frame delta is computed
// in int32 arithmetic so the 32-bit RTP timestamp wraparound is handled
// correctly. It returns ok=false when the anchor is not usable (zero rate).
func (a Anchor) MasterTime(frame uint32) (uint64, bool) {
	if a.Rate <= 0 {
		return 0, false
	}
	delta := int64(int32(frame - a.Frame))
	return uint64(int64(a.MasterNs) + delta*1_000_000_000/int64(a.Rate)), true
}

// LocalTime returns the local monotonic time, in nanoseconds, at which frame
// should be presented, converting from grandmaster time using the
// master-minus-local offset reported by a Clock. It returns ok=false when the
// anchor is not usable.
func (a Anchor) LocalTime(frame uint32, offsetNs int64) (uint64, bool) {
	master, ok := a.MasterTime(frame)
	if !ok {
		return 0, false
	}
	return uint64(int64(master) - offsetNs), true
}

// FrameAtMaster returns the RTP timestamp whose presentation time is nearest
// the given grandmaster time, or ok=false when the anchor is not usable. It is
// the inverse of MasterTime.
func (a Anchor) FrameAtMaster(masterNs uint64) (uint32, bool) {
	if a.Rate <= 0 {
		return 0, false
	}
	deltaNs := int64(masterNs - a.MasterNs)
	delta := deltaNs * int64(a.Rate) / 1_000_000_000
	return a.Frame + uint32(int32(delta)), true
}
