package pcm

import "testing"

func TestFormatValid(t *testing.T) {
	f := Format{Rate: 48000, Channels: 2, Format: S16LE}
	if err := f.Valid(); err != nil {
		t.Fatal(err)
	}
	if f.BytesPerFrame() != 4 {
		t.Fatalf("BytesPerFrame = %d", f.BytesPerFrame())
	}
}

func TestFormatInvalid(t *testing.T) {
	cases := []Format{
		{Rate: 0, Channels: 2, Format: S16LE},
		{Rate: 48000, Channels: 0, Format: S16LE},
		{Rate: 48000, Channels: 2, Format: SampleFormatUnknown},
	}
	for _, f := range cases {
		if err := f.Valid(); err == nil {
			t.Fatalf("expected invalid: %+v", f)
		}
	}
}

func TestBytesPerSample(t *testing.T) {
	if S16LE.BytesPerSample() != 2 ||
		S24LE.BytesPerSample() != 3 ||
		F32LE.BytesPerSample() != 4 {
		t.Fatal("unexpected bytes per sample")
	}
	if SampleFormatUnknown.BytesPerSample() != 0 {
		t.Fatal("unknown should be 0")
	}
}
