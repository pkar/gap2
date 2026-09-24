These fixtures contain generated signals, not captured program audio.

Generate the tone with:

```sh
ffmpeg -f lavfi -i 'sine=frequency=997:sample_rate=44100:duration=0.25' \
  -ac 2 -c:a aac -b:a 128k -f adts stereo-44100.aac
```

Generate the broadband fixture with:

```sh
ffmpeg -f lavfi -i 'anoisesrc=color=pink:sample_rate=44100:duration=0.4:seed=1' \
  -ac 2 -c:a aac -aac_pns 0 -b:a 256k -f adts noise-44100.aac
```

Perceptual noise substitution is disabled for the broadband fixture because
different decoders use different random noise sequences. Generate each
reference PCM file with `ffmpeg -i NAME.aac -f s16le NAME.s16le`.
Fixtures were generated with FFmpeg's Lavc63.1.101 encoder.

The surround fixtures use 48 kHz, 0.16-second per-channel tones in canonical
speaker order: 311, 523, 733, 61, 997, 1201 Hz for 5.1; 7.1 adds 1409 and
1601 Hz. Each tone has amplitude 0.08. For example:

```sh
ffmpeg -f lavfi -i 'aevalsrc=0.08*sin(2*PI*311*t)|0.08*sin(2*PI*523*t)|0.08*sin(2*PI*733*t)|0.08*sin(2*PI*61*t)|0.08*sin(2*PI*997*t)|0.08*sin(2*PI*1201*t):s=48000:d=0.16:c=5.1' \
  -c:a aac -aac_pns 0 -b:a 512k -f adts surround-48000.aac
```

For `surround71-48000`, append the two extra tones, use `c=7.1`, `-b:a 640k`,
and `-aac_pce 1` (ADTS cannot carry configuration 12 directly). The test
supplies configuration 12, as the buffered AP2 format does, and checks every
interleaved channel against FFmpeg's output. Generate references with the
same `ffmpeg -i NAME.aac -f s16le NAME.s16le` command above.
