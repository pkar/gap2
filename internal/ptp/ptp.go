// Package ptp implements the subset of IEEE 1588-2008 (PTPv2) used by AirPlay 2
// audio timing. It parses and serializes the PTP common header and the
// Sync, Follow_Up, Delay_Req, Delay_Resp and Announce messages, and estimates
// the offset between the local monotonic clock and a remote grandmaster clock.
//
// AirPlay 2 uses the Apple Vendor PTP profile: the receiver acts as a PTP
// slave and passively follows the sender's grandmaster by observing Sync and
// Follow_Up messages. The offset estimate is one-way (network propagation
// delay is ignored), matching the reference behaviour, which is adequate on a
// LAN where AirPlay already buffers seconds of audio.
package ptp

import "encoding/binary"

// PTP message type codes (IEEE 1588-2008, Table 19).
const (
	TypeSync      uint8 = 0
	TypeDelayReq  uint8 = 1
	TypePDelayReq uint8 = 2
	TypePDelayRes uint8 = 3
	TypeFollowUp  uint8 = 8
	TypeDelayResp uint8 = 9
	TypeAnnounce  uint8 = 11
	TypeSignaling uint8 = 12
)

// Apple Vendor PTP profile constants.
const (
	// TransportSpecific is the transportSpecific field Apple sends in every
	// PTP message header (upper nibble of byte 0).
	TransportSpecific uint8 = 1
	// VersionPTP is the PTP protocol version (lower nibble of byte 1).
	VersionPTP uint8 = 2
	// DefaultDomain is the PTP domain number Apple uses.
	DefaultDomain uint8 = 0
	// DefaultFlags is the 16-bit flags field Apple uses (0x0608).
	DefaultFlags uint16 = 0x0608
	// ControlSync is the controlField value carried by Sync, Follow_Up,
	// Delay_Req and Delay_Resp messages.
	ControlSync uint8 = 5
	// ControlOther is the controlField value carried by Announce messages.
	ControlOther uint8 = 2

	// CorrectionScale is the fixed-point scale of the correctionField: the
	// 64-bit field holds nanoseconds in the high 48 bits and a fractional
	// part in the low 16 bits, so dividing by 2^16 yields whole nanoseconds.
	CorrectionScale int64 = 1 << 16

	// LogSyncInterval is Apple's sync message interval, log2 seconds (-3 =
	// 125 ms). It is unused for decoding but documents the expected rate.
	LogSyncInterval int8 = -3
	// LogAnnounceInterval is Apple's announce interval, log2 seconds (0 = 1s).
	LogAnnounceInterval int8 = 0
)

// headerSize is the size of the PTP common message header.
const headerSize = 34

// Timestamp is a PTP timestamp: a 48-bit seconds field plus a 32-bit
// nanoseconds field, encoded as 10 bytes big-endian.
type Timestamp struct {
	Seconds uint64
	Nanos   uint32
}

// AsNanos returns the timestamp as a whole number of nanoseconds.
func (t Timestamp) AsNanos() uint64 {
	return t.Seconds*1_000_000_000 + uint64(t.Nanos)
}

// appendTimestamp appends the 10-byte big-endian encoding of t to b.
func appendTimestamp(b []byte, t Timestamp) []byte {
	var tmp [10]byte
	binary.BigEndian.PutUint16(tmp[0:2], uint16(t.Seconds>>32))
	binary.BigEndian.PutUint32(tmp[2:6], uint32(t.Seconds))
	binary.BigEndian.PutUint32(tmp[6:10], t.Nanos)
	return append(b, tmp[:]...)
}

// parseTimestamp decodes a 10-byte big-endian PTP timestamp from b.
func parseTimestamp(b []byte) Timestamp {
	hi := uint64(binary.BigEndian.Uint16(b[0:2]))
	lo := uint64(binary.BigEndian.Uint32(b[2:6]))
	return Timestamp{
		Seconds: hi<<32 | lo,
		Nanos:   binary.BigEndian.Uint32(b[6:10]),
	}
}

// Header is the PTP common message header.
type Header struct {
	MessageType uint8
	Version     uint8
	// Length is the full message length in bytes, including the header.
	Length uint16
	Domain uint8
	Flags  uint16
	// CorrectionNs is the correction field converted to whole nanoseconds.
	// The wire value is a 48.16 fixed-point number of nanoseconds.
	CorrectionNs int64
	// ClockID is the sourcePortIdentity.clockIdentity.
	ClockID [8]byte
	// SourcePort is the sourcePortIdentity.portNumber.
	SourcePort uint16
	Sequence   uint16
	Control    uint8
	// LogInterval is the logMessagePeriod (log2 seconds).
	LogInterval int8
}

// Message is a parsed PTP message. Exactly one of the timestamp fields is
// populated depending on MessageType: Origin for Sync/Delay_Req, Precise for
// Follow_Up, Receive for Delay_Resp, and GM for the Announce grandmaster
// identity.
type Message struct {
	Header  Header
	Origin  Timestamp // Sync, Delay_Req, Announce originTimestamp
	Precise Timestamp // Follow_Up preciseOriginTimestamp
	Receive Timestamp // Delay_Resp receiveTimestamp
	// Grandmaster is the grandmasterIdentity from an Announce message.
	Grandmaster [8]byte
}

// Parse decodes a PTP message from b and reports the type and any relevant
// timestamps. It returns ErrShort for a message shorter than the common
// header, and ErrBadLength if the header's length field disagrees with len(b).
func Parse(b []byte) (Message, error) {
	if len(b) < headerSize {
		return Message{}, ErrShort
	}
	h := parseHeader(b)
	if h.Length != 0 && int(h.Length) != len(b) {
		return Message{}, ErrBadLength
	}
	m := Message{Header: h}

	body := b[headerSize:]
	switch h.MessageType {
	case TypeSync, TypeDelayReq:
		if len(body) < 10 {
			return Message{}, ErrShort
		}
		m.Origin = parseTimestamp(body)
	case TypeFollowUp:
		if len(body) < 10 {
			return Message{}, ErrShort
		}
		m.Precise = parseTimestamp(body)
	case TypeDelayResp:
		if len(body) < 20 {
			return Message{}, ErrShort
		}
		m.Receive = parseTimestamp(body)
	case TypeAnnounce:
		if len(body) < 30 {
			return Message{}, ErrShort
		}
		m.Origin = parseTimestamp(body)
		// IEEE 1588 Announce body: originTimestamp(10) currentUtcOffset(2)
		// reserved(1) priority1(1) clockQuality(4) priority2(1), then
		// grandmasterIdentity(8) at offset 19, stepsRemoved(2), timeSource(1).
		copy(m.Grandmaster[:], body[19:27])
	}
	return m, nil
}

// parseHeader decodes the 34-byte common header from b (which must be at
// least headerSize bytes).
func parseHeader(b []byte) Header {
	corrRaw := int64(binary.BigEndian.Uint64(b[8:16]))
	return Header{
		MessageType:  b[0] & 0x0f,
		Version:      b[1] & 0x0f,
		Length:       binary.BigEndian.Uint16(b[2:4]),
		Domain:       b[4],
		Flags:        binary.BigEndian.Uint16(b[6:8]),
		CorrectionNs: corrRaw / CorrectionScale,
		ClockID:      [8]byte(b[20:28]),
		SourcePort:   binary.BigEndian.Uint16(b[28:30]),
		Sequence:     binary.BigEndian.Uint16(b[30:32]),
		Control:      b[32],
		LogInterval:  int8(b[33]),
	}
}

// Marshal encodes a message header and a 10-byte timestamp body of the given
// type. It is used by tests and, potentially, by a future slave role that
// sends Delay_Req. The correction field is written as a 48.16 fixed-point
// value.
func Marshal(messageType uint8, seq uint16, clockID [8]byte, ts Timestamp) []byte {
	b := make([]byte, headerSize+10)
	b[0] = TransportSpecific<<4 | messageType&0x0f
	b[1] = VersionPTP
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	b[4] = DefaultDomain
	binary.BigEndian.PutUint16(b[6:8], DefaultFlags)
	// correctionField left zero
	copy(b[20:28], clockID[:])
	binary.BigEndian.PutUint16(b[28:30], 1) // sourcePortID
	binary.BigEndian.PutUint16(b[30:32], seq)
	b[32] = ControlSync
	li := LogSyncInterval
	b[33] = byte(li)
	copy(b[headerSize:], marshalTimestamp(ts))
	return b
}

// marshalTimestamp returns the 10-byte big-endian encoding of t.
func marshalTimestamp(t Timestamp) []byte {
	out := make([]byte, 0, 10)
	return appendTimestamp(out, t)
}
