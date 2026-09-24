# gap2 — Pure-Go AirPlay 2 Audio Receiver

`gap2` is a pure-Go AirPlay 2 audio receiver delivered as an importable Go
module and a standalone command that wraps the same API.

## Status

**In progress.** The receiver library is importable and the standalone command
runs. Implemented and tested: discovery (mDNS/DNS-SD), HAP pairing and the
encrypted control transport, RTSP/SDP/RTP media transport with the AirPlay 2
plist-based SETUP flow, ALAC and AAC-LC decoding, a realtime PCM playout
scheduler, PTP synchronization, and PTP-anchored playback driven by the AP2
control channel, plus AP2 event-channel playback-state and now-playing metadata
(including cover art). End-to-end playback from real AirPlay 2 senders has not
yet been validated on hardware; protocol details that require a live sender are
reported as such rather than simulated.

## Layout

```
.                       public package airplay2 (lifecycle, config, events, PCM contracts)
pcm/                    PCM formats, blocks, and sink contracts
output/pcmfile/         deterministic raw-PCM sink for tests and capture
internal/plist/         bounded Apple property-list parsing and serialization
internal/rtsp/          bounded RTSP-like message parser
internal/sdp/           bounded SDP parser for ANNOUNCE bodies
internal/rtp/           bounded RTP packet parser
internal/media/         RTP media transport (UDP binding and dispatch)
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

## License

No license has been selected yet. Treat the code as all-rights-reserved until a
license is chosen.
