package airplay2

import (
	"errors"
	"testing"

	"github.com/pkar/gap2/internal/plist"
)

func TestParseAp2SetupInitialPTP(t *testing.T) {
	body := plist.Dict(map[string]*plist.Value{
		"timingProtocol":           plist.String("PTP"),
		"groupUUID":                plist.String("00000000-0000-0000-0000-000000000000"),
		"groupContainsGroupLeader": plist.Bool(true),
		"timingPeerInfo": plist.Dict(map[string]*plist.Value{
			"Addresses": plist.Array(plist.String("192.0.2.1")),
			"ID":        plist.String("clockid"),
		}),
	})
	enc, err := plist.Encode(body)
	if err != nil {
		t.Fatal(err)
	}

	req, err := parseAp2Setup(enc)
	if err != nil {
		t.Fatal(err)
	}
	if req.kind != setupInitial {
		t.Fatalf("kind = %v, want initial", req.kind)
	}
	if req.timingProtocol != "PTP" {
		t.Fatalf("timingProtocol = %q, want PTP", req.timingProtocol)
	}
	if !req.groupContainsGroupLeader {
		t.Fatal("groupContainsGroupLeader = false, want true")
	}
	if req.groupUUID == "" {
		t.Fatal("groupUUID not extracted")
	}
}

func TestParseAp2SetupStream(t *testing.T) {
	body := plist.Dict(map[string]*plist.Value{
		"streams": plist.Array(
			plist.Dict(map[string]*plist.Value{"type": plist.Int(96)}),
			plist.Dict(map[string]*plist.Value{"type": plist.Int(103)}),
		),
	})
	enc, err := plist.Encode(body)
	if err != nil {
		t.Fatal(err)
	}

	req, err := parseAp2Setup(enc)
	if err != nil {
		t.Fatal(err)
	}
	if req.kind != setupStream {
		t.Fatalf("kind = %v, want stream", req.kind)
	}
	if len(req.streamTypes) != 2 || req.streamTypes[0] != 96 || req.streamTypes[1] != 103 {
		t.Fatalf("streamTypes = %v, want [96 103]", req.streamTypes)
	}
}

func TestParseAp2SetupRejectsNonPlist(t *testing.T) {
	for _, body := range [][]byte{nil, {}, []byte("RTSP not a plist"), []byte{0x00, 0x00, 0x00}} {
		if _, err := parseAp2Setup(body); !errors.Is(err, errNotAp2Setup) {
			t.Fatalf("parseAp2Setup(%q) = %v, want errNotAp2Setup", body, err)
		}
	}
}

func TestBuildAp2InitialResponse(t *testing.T) {
	body, err := buildAp2InitialResponse(5000, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	v, err := plist.Decode(body, plist.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != plist.KindDict {
		t.Fatalf("kind = %v, want dict", v.Kind)
	}
	if got := v.Dict["eventPort"]; got.Kind != plist.KindInt || got.Int != 5000 {
		t.Fatalf("eventPort = %+v, want int 5000", got)
	}
	if got := v.Dict["timingPort"]; got.Kind != plist.KindInt || got.Int != 0 {
		t.Fatalf("timingPort = %+v, want int 0", got)
	}
	peer := v.Dict["timingPeerInfo"]
	if peer.Kind != plist.KindDict {
		t.Fatalf("timingPeerInfo kind = %v, want dict", peer.Kind)
	}
	id := peer.Dict["ID"]
	if id.Kind != plist.KindString || id.String != "192.0.2.10" {
		t.Fatalf("timingPeerInfo.ID = %+v, want string 192.0.2.10", id)
	}
	addrs := peer.Dict["Addresses"]
	if addrs.Kind != plist.KindArray || len(addrs.Array) != 1 || addrs.Array[0].String != "192.0.2.10" {
		t.Fatalf("timingPeerInfo.Addresses = %+v, want [192.0.2.10]", addrs)
	}
}

func TestBuildAp2StreamResponseRealtime(t *testing.T) {
	body, err := buildAp2StreamResponse(96, 7000, 7001, 0)
	if err != nil {
		t.Fatal(err)
	}
	v, err := plist.Decode(body, plist.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	streams := v.Dict["streams"]
	if streams.Kind != plist.KindArray || len(streams.Array) != 1 {
		t.Fatalf("streams = %+v, want one-element array", streams)
	}
	e := streams.Array[0]
	if e.Dict["type"].Int != 96 || e.Dict["dataPort"].Int != 7000 || e.Dict["controlPort"].Int != 7001 {
		t.Fatalf("stream entry = %+v", e)
	}
	if _, ok := e.Dict["audioBufferSize"]; ok {
		t.Fatal("realtime stream should not advertise audioBufferSize")
	}
}

func TestBuildAp2StreamResponseBuffered(t *testing.T) {
	body, err := buildAp2StreamResponse(103, 7000, 7001, 1024)
	if err != nil {
		t.Fatal(err)
	}
	v, err := plist.Decode(body, plist.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	e := v.Dict["streams"].Array[0]
	if e.Dict["type"].Int != 103 {
		t.Fatalf("type = %v, want 103", e.Dict["type"].Int)
	}
	if buf, ok := e.Dict["audioBufferSize"]; !ok || buf.Int != 1024 {
		t.Fatalf("audioBufferSize = %+v, want int 1024", e.Dict["audioBufferSize"])
	}
}
