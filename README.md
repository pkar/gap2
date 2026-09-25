# gap2

A pure-Go AirPlay 2 audio receiver with an importable module and standalone command. This is a prerelease: it works with tested Apple TV audio streams, but broader device compatibility is still being validated.

## Install

```sh
curl -fsSLO https://raw.githubusercontent.com/pkar/gap2/main/install.sh
sh install.sh
```

The installer puts `airplay2-receiver` in `~/.local/bin`. It downloads the newest release (including prereleases), verifies its SHA-256 checksum, and builds from source with Go 1.24+ when no binary matches. Set `GAP2_VERSION` to pin a tag or `GAP2_INSTALL_DIR` to choose another directory. It does not configure an audio device or service.

To build locally instead, run `make install`; `BINDIR` and `PREFIX` override its destination.

## Run

```sh
airplay2-receiver run -name gap2 \
  -pairings "$GAP2_PAIRINGS_PATH" -audio-device "$GAP2_AUDIO_DEVICE" \
  -output-rate 48000 -output-channels 2
```

Linux amd64/arm64 uses native ALSA playback. Other platforms can capture S16LE PCM with `-output audio.pcm` or provide a sink through the Go API. The sample [systemd service](deploy/gap2.service) reads device and interface settings from an external environment file; keep machine-specific configuration outside this repository. See `airplay2-receiver run -help` for flags.

## Support

Custom output sinks can implement `pcm.VolumeSink` to apply attenuation at
their native precision. The receiver then preserves S16LE samples through
resampling and delegates gain, including mute, to that sink. Outputs without
this capability keep the default S16LE software gain.

- Encrypted AirPlay 2 control and audio, AAC-LC and ALAC, stream replacement, and buffered TCP reconnects.
- Stereo playback with resampling and surround downmix; supported 5.1/7.1 streams can also retain their channels on a matching device.
- PTP-timed playback with hardware queue correction; `-output-offset` adjusts for downstream speaker delay.

HE-AAC, arbitrary AAC program configurations, Atmos object rendering, and 24-bit PCM output are not supported. Full lip-sync accuracy, physical surround routing, and the wider sender/device matrix still need validation.

## Develop

```sh
make test
make vet
make cross
```

`GAP2_NETWORK_TEST=1 go test ./internal/media` runs real TCP reset recovery tests. `GAP2_SOAK_DURATION=30m go test ./internal/stream -run TestBufferedLongPlayback -v -timeout=10m` runs an accelerated codec-switch replay. Neither replaces listening on real hardware.

## License

No license has been selected. The code is all rights reserved until one is chosen.
