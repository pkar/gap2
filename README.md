# gap2 — Pure-Go AirPlay 2 Audio Receiver

`gap2` is a pure-Go AirPlay 2 audio receiver delivered as an importable Go
module and a standalone command that wraps the same API.

## Status

**Phase 1 foundation only.** This repository currently provides the public
lifecycle and PCM contracts plus deterministic, tested protocol-building
blocks (bounded RTSP parsing, TLV8, timing helpers, counters, a session state
machine, and a raw PCM sink). Discovery, pairing, encrypted control, media
transport, codecs, and synchronized playback are not implemented yet and are
reported as such rather than simulated.

## Layout

```
.                       public package airplay2 (lifecycle, config, events, PCM contracts)
pcm/                    PCM formats, blocks, and sink contracts
output/pcmfile/         deterministic raw-PCM sink for tests and capture
internal/rtsp/          bounded RTSP-like message parser
internal/tlv8/          HomeKit-style TLV8 encode/decode with fragmentation
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
