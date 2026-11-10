package sdp

import "testing"

// FuzzParse parses arbitrary ANNOUNCE bodies.
func FuzzParse(f *testing.F) {
	f.Add([]byte("v=0\r\nm=audio 0 RTP/AVP 96\r\na=rtpmap:96 AppleLossless\r\na=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100\r\n"))
	f.Add([]byte("v=0\r\nm=audio 0 RTP/AVP 96\r\na=rtpmap:96 mpeg4-generic/44100/2\r\na=fmtp:96 mode=AAC-hbr;config=1210;sizeLength=13;indexLength=3;indexDeltaLength=3\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = Parse(data, DefaultLimits()) })
}
