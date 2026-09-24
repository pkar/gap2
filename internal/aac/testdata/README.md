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
