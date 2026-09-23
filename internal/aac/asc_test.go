package aac

import (
	"testing"
)

func TestParseASCAACLC(t *testing.T) {
	// fmtp config=1210: AAC-LC, 44100 Hz, stereo.
	asc, err := ParseASC([]byte{0x12, 0x10})
	if err != nil {
		t.Fatal(err)
	}
	if asc.ObjectType != 2 {
		t.Fatalf("object type = %d, want 2", asc.ObjectType)
	}
	if asc.SamplingFrequency != 44100 {
		t.Fatalf("sample rate = %d, want 44100", asc.SamplingFrequency)
	}
	if asc.ChannelConfiguration != 2 {
		t.Fatalf("channels = %d, want 2", asc.ChannelConfiguration)
	}
	if asc.FrameLengthFlag || asc.DependsOnCoreCoder || asc.ExtensionFlag || asc.SBR {
		t.Fatalf("unexpected flags = %+v", asc)
	}
}

func TestParseASCErrors(t *testing.T) {
	for _, data := range [][]byte{nil, {0x12}} {
		if _, err := ParseASC(data); err == nil {
			t.Fatalf("ParseASC(%x) succeeded, want error", data)
		}
	}
}
