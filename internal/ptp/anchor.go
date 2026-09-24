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
	// ClockID is the grandmaster clock identity the anchor refers to. It is
	// carried by code-215 timing-sync packets and lets the receiver detect a
	// grandmaster change: an anchor for a different clock than the one the
	// receiver is synchronized to must not be trusted. It is zero for anchors
	// set by SETRATEANCHORI, which carries no clock identity.
	ClockID [8]byte
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

// NetworkTimeNanoseconds converts the networkTimeSecs and networkTimeFrac
// fields of a SETRATEANCHORI request into a whole-nanosecond grandmaster time.
// networkTimeFrac is a fixed-point fraction of a second with the binary point
// after bit 32 (bit 63 is worth half a second); the low 32 bits are discarded
// before scaling, matching the reference conversion.
func NetworkTimeNanoseconds(secs, frac uint64) uint64 {
	return secs*1_000_000_000 + (frac>>32)*1_000_000_000>>32
}
