// Package airplay2 implements a pure-Go AirPlay 2 audio receiver.
//
// The package is designed both to be imported into a host application and to
// back the standalone airplay2-receiver command, which is a thin wrapper over
// the same public API.
//
// This is an early-stage implementation. The current milestone establishes the
// public lifecycle, PCM contracts, and deterministic protocol-building blocks.
// Discovery, pairing, encrypted control, media transport, codecs, and
// synchronized playback are implemented in subsequent milestones and are not
// yet available.
package airplay2
