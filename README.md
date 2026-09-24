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
an application-provided sink. A streaming, 64-tap polyphase resampler adapts
source rates to a fixed output rate. Mono through eight-channel audio can be
preserved or downmixed; 5.1 and 7.1 decoding is checked against independently
encoded fixtures. ALAC accepts 16/24-bit input and emits 16-bit PCM.

Buffered audio format changes switch decoders without closing the transport
or output device. Supported format identifiers cover AAC-LC stereo at
44.1/48 kHz, AAC-LC 5.1/7.1 at 48 kHz, and ALAC stereo at 44.1 kHz/16-bit or
48 kHz/24-bit. Unsupported identifiers fail explicitly. This is channel-based
PCM support, not an object-audio/Atmos renderer. HE-AAC, arbitrary AAC program
configurations, and full-resolution 24-bit output are not implemented.
Lip-sync accuracy, physical surround-speaker routing, and the wider
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

The module path is `github.com/pkar/gap2`.

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
  -pairings "$GAP2_PAIRINGS_PATH" -audio-device "$GAP2_AUDIO_DEVICE" \
  -output-rate 48000 -output-channels 2
```

Use `-output audio.pcm` instead of `-audio-device` to capture raw interleaved
S16LE PCM. Apple TV's volume setting is applied to either output. Access to
the ALSA device and PTP ports 319/320 is required. The sample
[`deploy/gap2.service`](deploy/gap2.service) runs as a dedicated `gap2` user
with membership in `audio` and permission to bind those ports. Adjust its
interface and device in the external `/etc/gap2/receiver.env` file before
installing it. Host-specific configuration and validation records belong
outside this repository.

The device must accept the selected output format. Zero output-rate/channel
flags preserve the initial source format; subsequent changes are converted
to that format. Use `-output-channels 6` or `8` with a matching multichannel
device to preserve surround. PCM uses L,R,C,LFE,BL,BR for 5.1 and adds SL,SR
for standard 7.1. ALAC eight-channel and AAC configuration 7 use FLC,FRC
instead of SL,SR; verify the physical device's channel mapping. Stereo downmix
includes center and surround, excludes LFE, and normalizes to avoid clipping.

## Playback and recovery validation

Run an accelerated replay of 30 minutes of encoded audio, repeatedly switching
codec, source rate and channel count while checking output continuity and
retained heap size:

```sh
GAP2_SOAK_DURATION=30m go test ./internal/stream \
  -run TestBufferedLongPlayback -v -count=1 -timeout=5m
```

Set `GAP2_SOAK_REALTIME=1` and a longer test timeout to pace the same test in
real time. It validates the decode/conversion pipeline; it does not substitute
for listening or real hardware playback.

Network tests exercise 100 real TCP resets, fragmented/truncated frames,
reconnects on the same negotiated stream, stalled reads, control-header
timeouts, and receiver cancellation:

```sh
GAP2_NETWORK_TEST=1 go test ./internal/media -run 'TestBuffered.*Recovery' -v
go test -race . ./internal/media ./internal/stream ./internal/ptp ./pcm
```

After a broken TCP audio connection, the receiver keeps the negotiated port
available for sender reconnection and drops stale decode/output state. The
sender still needs to reconnect or select the receiver again after a full
control-session loss; gap2 cannot force Apple TV to reselect its output.

Synchronized hardware playback aligns each block with the measured output
queue, padding early audio or trimming late audio outside a 2 ms tolerance.
For a downstream amplifier or speaker with additional delay, `-output-offset`
accepts a duration between `-500ms` and `500ms`: negative plays earlier and
positive plays later. Start at zero and calibrate at the listening position.
Keep device-specific calibration values in external configuration.

Debug progress logs include stream duration, played frames, queue latency,
underruns, and timing lead. Stream shutdown emits a summary at info level.
To summarize a single-receiver, uninterrupted listening interval locally on
the receiver host, without saving raw logs:

```sh
go build ./cmd/gap2-soak-report
journalctl -u gap2.service --since '10 minutes ago' -o cat --no-pager |
  ./gap2-soak-report -min-duration 9m
```

The report ignores startup underruns, then requires advancing hardware frames
without new underruns, transport disconnects, or progress gaps over 15 seconds.
It exits unsuccessfully when the observed interval is too short or fails those
checks. Select an interval after intentional channel changes/reconnections.

## License

No license has been selected yet. Treat the code as all-rights-reserved until a
license is chosen.
