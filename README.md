# gap2 — Pure-Go AirPlay 2 Audio Receiver

`gap2` is a pure-Go AirPlay 2 audio receiver delivered as an importable Go
module and a standalone command that wraps the same API.

## Status

**In progress.** Supports encrypted buffered audio and stream replacement
without reconnecting the enclosing AirPlay session.
The receiver supports transient HAP pairing, encrypted control and event
channels, native AP2 setup, realtime UDP and buffered TCP audio, ALAC and
AAC-LC decoding, and PTP playback anchors. AAC decoding is checked against an
independently encoded tone and reference PCM. Buffered stream replacement
preserves the control/event session, and flushes use the transport sequence
counter to handle channel changes that reset audio timestamps.

Linux amd64 and arm64 can play directly through an ALSA hardware device,
without cgo, libasound, or subprocesses. Other platforms can use a PCM file or
an application-provided sink. Hardware playback requires the device to accept
the negotiated sample rate; resampling, multichannel audio, and seamless
midstream codec changes are not implemented. Lip-sync accuracy and the wider
sender/device matrix still require validation.

## Layout

```
.                       public package airplay2 (lifecycle, config, events, PCM contracts)
pcm/                    PCM formats, blocks, and sink contracts
output/pcmfile/         deterministic raw-PCM sink for tests and capture
output/alsa/            native Linux ALSA playback sink
internal/plist/         bounded Apple property-list parsing and serialization
internal/rtsp/          bounded RTSP-like message parser
internal/sdp/           bounded SDP parser for ANNOUNCE bodies
internal/rtp/           bounded RTP packet parser
internal/media/         realtime UDP and length-framed buffered TCP transport
internal/stream/        per-stream ANNOUNCE/SETUP/RECORD state machine and RTP ingest
internal/aac/           ADTS framing and AAC-LC spectral decoding
internal/alac/          Apple Lossless (ALAC) decoder
internal/playout/       buffered realtime PCM playout scheduler
internal/ptp/           PTPv2 message parsing and clock-offset estimation
internal/tlv8/          HomeKit-style TLV8 encode/decode with fragmentation
internal/hap/           HAP pairing, HKDF/SRP, and encrypted control transport
internal/zeroconf/      mDNS/DNS-SD advertisement
internal/timing/        clock and sample-rate conversion helpers
internal/observability/ small thread-safe counters
internal/session/       per-session state machine
cmd/airplay2-receiver/  standalone command
examples/embed/         minimal embedding example
```

The module path is `github.com/pkar/gap2`; it is a placeholder and can be
changed with a single `go.mod` edit plus import-path rewrites.

## Build and test

```sh
make build          # build ./airplay2-receiver (CGO_ENABLED=0)
make test           # go test ./...
make vet            # go vet ./...
make cross          # cross-build linux/amd64, linux/arm64, darwin/arm64 into dist/
make install        # install to ~/.local/bin (override with BINDIR= or PREFIX=)
```

Equivalent raw Go commands:

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go build ./cmd/airplay2-receiver
```

The production contract is cgo-free; the suite also runs under `go test -race`
in a cgo-enabled environment when available. The `version` subcommand reports
the build version, which release builds set via
`-ldflags "-X main.version=<version>"`.

## Playback

Set the pairing-state path and audio device for your own host, then run:

```sh
airplay2-receiver run -name "gap2" \
  -pairings "$GAP2_PAIRINGS_PATH" -audio-device "$GAP2_AUDIO_DEVICE"
```

Use `-output audio.pcm` instead of `-audio-device` to capture raw interleaved
S16LE PCM. Apple TV's volume setting is applied to either output. Access to
the ALSA device and PTP ports 319/320 is required. The sample
[`deploy/gap2.service`](deploy/gap2.service) runs as a dedicated `gap2` user
with membership in `audio` and permission to bind those ports. Adjust its
interface and device in the external `/etc/gap2/receiver.env` file before
installing it. Host-specific configuration and validation records belong
outside this repository.

## License

No license has been selected yet. Treat the code as all-rights-reserved until a
license is chosen.
