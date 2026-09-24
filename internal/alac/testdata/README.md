These are generated signals, not recorded media. Run `go run generate.go`
in this directory with FFmpeg/ffprobe installed to reproduce them.

Each `.frames` file stores raw ALAC packets prefixed by a four-byte big-endian
packet length. Inputs are 24-bit, 48 kHz, 0.16-second independent per-channel
tones. The reference `.s16le` files are FFmpeg's decoded interleaved output,
including the same 24-to-16-bit truncation used by gap2. Tests require exact
sample equality for stereo, 5.1, and 7.1-wide output.

FFmpeg's Lavc63.1.101 encoder generated the checked-in fixtures. Production
gap2 does not invoke FFmpeg, ffprobe, or any audio subprocess.
