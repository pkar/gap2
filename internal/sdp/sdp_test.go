package sdp

import (
	"bytes"
	"errors"
	"testing"
)

const aacSDP = "v=0\r\n" +
	"o=iTunes 3413825038 0 IN IP4 192.168.1.2\r\n" +
	"s=iTunes\r\n" +
	"c=IN IP4 192.168.1.2\r\n" +
	"t=0 0\r\n" +
	"m=audio 0 RTP/AVP 96\r\n" +
	"a=rtpmap:96 mpeg4-generic/44100/2\r\n" +
	"a=fmtp:96 streamtype=5; profile-level-id=1; mode=AAC-hbr; config=1210; sizeLength=13; indexLength=3; indexDeltaLength=3; constantDuration=1024\r\n"

const alacSDP = "v=0\r\n" +
	"o=iTunes 3413825038 0 IN IP4 192.168.1.2\r\n" +
	"s=iTunes\r\n" +
	"c=IN IP4 192.168.1.2\r\n" +
	"t=0 0\r\n" +
	"m=audio 0 RTP/AVP 96\r\n" +
	"a=rtpmap:96 AppleLossless\r\n" +
	"a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100\r\n"

func TestParseAAC(t *testing.T) {
	m, err := Parse([]byte(aacSDP), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if m.PayloadType != 96 || m.Encoding != "mpeg4-generic" || m.ClockRate != 44100 || m.Channels != 2 {
		t.Fatalf("media = %+v", m)
	}
	if m.AAC == nil {
		t.Fatal("missing AAC config")
	}
	if m.AAC.SizeLength != 13 || m.AAC.IndexLength != 3 || m.AAC.IndexDeltaLength != 3 {
		t.Fatalf("AAC sizes = %+v", m.AAC)
	}
	if m.AAC.Mode != "AAC-hbr" || m.AAC.StreamType != "5" || m.AAC.ProfileLevelID != "1" {
		t.Fatalf("AAC params = %+v", m.AAC)
	}
	if !bytes.Equal(m.AAC.ASC, []byte{0x12, 0x10}) {
		t.Fatalf("ASC = %x", m.AAC.ASC)
	}
}

func TestParseALAC(t *testing.T) {
	m, err := Parse([]byte(alacSDP), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if m.PayloadType != 96 || m.Encoding != "AppleLossless" {
		t.Fatalf("media = %+v", m)
	}
	if m.ALAC == nil {
		t.Fatal("missing ALAC config")
	}
	if m.ALAC.FrameLength != 352 || m.ALAC.BitDepth != 16 || m.ALAC.Channels != 2 || m.ALAC.SampleRate != 44100 {
		t.Fatalf("ALAC = %+v", m.ALAC)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"no audio", "v=0\ns=x\n", ErrNoAudio},
		{"no rtpmap", "v=0\nm=audio 0 RTP/AVP 96\n", ErrNoRTCPMap},
		{"unsupported", "v=0\nm=audio 0 RTP/AVP 96\na=rtpmap:96 L16/44100/2\n", ErrUnsupported},
		{"bad asc", "v=0\nm=audio 0 RTP/AVP 96\na=rtpmap:96 mpeg4-generic/44100/2\na=fmtp:96 config=zz\n", ErrBadFmtp},
		{"alac count", "v=0\nm=audio 0 RTP/AVP 96\na=rtpmap:96 AppleLossless\na=fmtp:96 1 2 3\n", ErrBadFmtp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.body), DefaultLimits()); !errors.Is(err, tc.want) {
				t.Fatalf("Parse = %v, want %v", err, tc.want)
			}
		})
	}
}
