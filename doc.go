// Package airplay2 implements a pure-Go AirPlay 2 audio receiver.
//
// The package is designed both to be imported into a host application and to
// back the standalone airplay2-receiver command, which is a thin wrapper over
// the same public API.
//
// The receiver covers the AirPlay 2 protocol end to end: mDNS/DNS-SD discovery
// advertisement, HomeKit Accessory Protocol pairing (Pair Setup and Pair
// Verify) with persistent or transient pairings, the encrypted control
// transport, RTSP/SDP/RTP media transport, ALAC and AAC-LC decoding, and
// PTP-synchronized playback. AirPlay 2 timing is driven by the sender's PTP
// grandmaster: the receiver estimates the master-to-local offset from
// Sync/Follow_Up messages and schedules decoded PCM against the playback
// anchor the sender announces over the AP2 control channel. The AP2 event
// channel is decoded into playback-state and now-playing metadata, including
// cover art. Audio is delivered through a pcm.Sink (for example the raw-PCM
// file sink in output/pcmfile).
package airplay2
