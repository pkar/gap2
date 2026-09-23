package airplay2

import "time"

// EventType identifies the kind of a receiver event.
type EventType uint8

const (
	// EventStateChanged reports a receiver lifecycle state change.
	EventStateChanged EventType = iota
	// EventSessionChanged reports session create/update/teardown.
	EventSessionChanged
	// EventTrackChanged reports new track metadata.
	EventTrackChanged
	// EventVolumeChanged reports a volume change.
	EventVolumeChanged
	// EventError reports a recoverable error.
	EventError
)

// Event is an asynchronous receiver notification.
type Event struct {
	Type    EventType
	State   State
	Session SessionInfo
	Track   TrackInfo
	Volume  float64
	Err     error
	At      time.Time
}
