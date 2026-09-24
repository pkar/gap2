package ptp

import "errors"

// Errors returned by message parsing.
var (
	// ErrShort indicates a message shorter than required for its type.
	ErrShort = errors.New("ptp: message shorter than required")
	// ErrBadLength indicates the header length field disagrees with the
	// received message length.
	ErrBadLength = errors.New("ptp: header length field mismatch")
)
