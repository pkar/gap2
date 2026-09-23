package airplay2

// State is the lifecycle state of a Receiver.
type State uint8

const (
	// StateIdle is the state of a newly created receiver before Run.
	StateIdle State = iota
	// StateStarting is the state while Run initializes resources.
	StateStarting
	// StateRunning is the state of an active receiver.
	StateRunning
	// StateStopped is the state after a clean shutdown.
	StateStopped
	// StateFailed is the state after an unrecoverable error.
	StateFailed
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopped:
		return "stopped"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// PlaybackState is the playback state of one session.
type PlaybackState uint8

const (
	PlaybackIdle PlaybackState = iota
	PlaybackBuffering
	PlaybackPlaying
	PlaybackPaused
)

func (p PlaybackState) String() string {
	switch p {
	case PlaybackIdle:
		return "idle"
	case PlaybackBuffering:
		return "buffering"
	case PlaybackPlaying:
		return "playing"
	case PlaybackPaused:
		return "paused"
	default:
		return "unknown"
	}
}

// SessionInfo is a stable snapshot of one receiver session.
type SessionInfo struct {
	ID         string
	ClientID   string
	RemoteAddr string
	Playback   PlaybackState
}

// TrackInfo carries optional track metadata reported by a sender.
type TrackInfo struct {
	Title  string
	Artist string
	Album  string
}
