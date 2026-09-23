# gap2 — Pure-Go AirPlay 2 Audio Receiver

`gap2` is a pure-Go AirPlay 2 audio receiver delivered as an importable Go
module and a standalone command that wraps the same API.

## Status

**In progress.** The public lifecycle, pairing, discovery, and PCM contracts
plus deterministic protocol-building blocks are implemented and tested.
Discovery, HAP pairing, and the encrypted control transport are committed.
Buffered playback is partially implemented: ADTS framing and a realtime PCM
playout scheduler exist, but AAC-LC bitstream decoding and the RTSP media
session are not implemented yet and are reported as such rather than
simulated.

## Layout

```
.                       public package airplay2 (lifecycle, config, events, PCM contracts)
pcm/                    PCM formats, blocks, and sink contracts
output/pcmfile/         deterministic raw-PCM sink for tests and capture
internal/rtsp/          bounded RTSP-like message parser
internal/tlv8/          HomeKit-style TLV8 encode/decode with fragmentation
internal/hap/           HAP pairing, HKDF/SRP, and encrypted control transport
internal/zeroconf/      mDNS/DNS-SD advertisement
internal/aac/           bounded ADTS framing for AAC audio
internal/playout/       buffered realtime PCM playout scheduler
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
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go build ./cmd/airplay2-receiver
```

The production contract is cgo-free; the suite also runs under `go test -race`
in a cgo-enabled environment when available.

## License

No license has been selected yet. Treat the code as all-rights-reserved until a
license is chosen.
