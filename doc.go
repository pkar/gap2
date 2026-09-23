// Package airplay2 implements a pure-Go AirPlay 2 audio receiver.
//
// The package is designed both to be imported into a host application and to
// back the standalone airplay2-receiver command, which is a thin wrapper over
// the same public API.
//
// This is an early-stage implementation. The current milestone provides the
// public lifecycle, PCM contracts, deterministic protocol-building blocks,
// HomeKit Accessory Protocol pairing (Pair Setup and Pair Verify), an encrypted
// control transport, persistent or transient pairings, a control endpoint that
// serves discovery info and pairing over TCP, and mDNS discovery. Media and
// codec groundwork is in progress (ADTS framing, RTP parsing, an AAC synthesis
// IMDCT, and a buffered PCM playout scheduler); full AAC-LC/ALAC decoding and
// synchronized playback are not yet available.
package airplay2
