package airplay2

import "errors"

// Sentinel errors returned by the receiver and its supporting packages.
var (
	// ErrNotConfigured indicates a configuration that cannot be used.
	ErrNotConfigured = errors.New("airplay2: not configured")

	// ErrNotImplemented indicates a planned feature that is not implemented yet.
	ErrNotImplemented = errors.New("airplay2: not implemented")

	// ErrClosed indicates an operation on a closed receiver.
	ErrClosed = errors.New("airplay2: receiver closed")

	// ErrAlreadyRunning indicates Run was called more than once.
	ErrAlreadyRunning = errors.New("airplay2: receiver already running")

	// ErrUnsupportedFormat indicates a negotiated audio format this build cannot handle.
	ErrUnsupportedFormat = errors.New("airplay2: unsupported audio format")

	// ErrAuthFailed indicates a failed pairing or authentication exchange.
	ErrAuthFailed = errors.New("airplay2: authentication failed")

	// ErrClockUnavailable indicates no usable timing source could be established.
	ErrClockUnavailable = errors.New("airplay2: clock unavailable")

	// ErrResourceExhausted indicates a hard resource limit was reached.
	ErrResourceExhausted = errors.New("airplay2: resource exhausted")

	// ErrOutputFailure indicates the audio sink failed.
	ErrOutputFailure = errors.New("airplay2: output failure")
)
