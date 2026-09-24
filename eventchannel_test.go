package airplay2

import (
	"bufio"
	"bytes"
	"log/slog"
	"testing"

	"github.com/pkar/gap2/internal/plist"
)

// TestEventCommandFraming checks that writeEventCommand and readEventRequest
// round-trip the event-channel request framing.
func TestEventCommandFraming(t *testing.T) {
	body := []byte{0x62, 0x70, 0x6c, 0x69, 0x73, 0x74} // "bplist"
	var b bytes.Buffer
	if err := writeEventCommand(&b, body); err != nil {
		t.Fatalf("write: %v", err)
	}
	req, err := readEventRequest(bufio.NewReader(&b), DefaultLimits())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if req.method != "POST" || req.target != "/command" || req.version != "RTSP/1.0" {
		t.Fatalf("unexpected request line: %q %q %q", req.method, req.target, req.version)
	}
	if !bytes.Equal(req.body, body) {
		t.Fatalf("body mismatch: got %x want %x", req.body, body)
	}
	if ct := req.headers["content-type"]; len(ct) != 1 || ct[0] != "application/x-apple-binary-plist" {
		t.Fatalf("unexpected content-type: %v", ct)
	}
}

// TestSendUpdateInfo checks that the updateInfo push wraps the receiver info
// dictionary under the expected "type"/"value" keys.
func TestSendUpdateInfo(t *testing.T) {
	h := &mediaHandler{
		info: func() *plist.Value {
			return plist.Dict(map[string]*plist.Value{
				"deviceid": plist.String("AA:BB:CC:DD:EE:FF"),
				"model":    plist.String("AppleTV6,2"),
			})
		},
	}
	var b bytes.Buffer
	if err := h.sendUpdateInfo(&b); err != nil {
		t.Fatalf("sendUpdateInfo: %v", err)
	}
	req, err := readEventRequest(bufio.NewReader(&b), DefaultLimits())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	v, err := plist.Decode(req.body, plist.DefaultLimits())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v == nil || v.Kind != plist.KindDict {
		t.Fatalf("updateInfo body not a dict: %+v", v)
	}
	if typ, ok := v.Dict["type"]; !ok || typ.Kind != plist.KindString || typ.String != "updateInfo" {
		t.Fatalf("missing or invalid type: %+v", v.Dict["type"])
	}
	value, ok := v.Dict["value"]
	if !ok || value.Kind != plist.KindDict {
		t.Fatalf("missing or invalid value: %+v", v.Dict["value"])
	}
	if id, ok := value.Dict["deviceid"]; !ok || id.String != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("deviceid mismatch: %+v", value.Dict["deviceid"])
	}
}

// TestSendUpdateInfoNilInfo verifies the no-op path when no info provider is
// configured.
func TestSendUpdateInfoNilInfo(t *testing.T) {
	h := &mediaHandler{}
	var b bytes.Buffer
	if err := h.sendUpdateInfo(&b); err != nil {
		t.Fatalf("sendUpdateInfo: %v", err)
	}
	if b.Len() != 0 {
		t.Fatalf("expected no output, got %d bytes", b.Len())
	}
}

// TestDecodeEventCommand checks that a command body is decoded to its "type"
// string and the full command dict.
func TestDecodeEventCommand(t *testing.T) {
	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"type":  plist.String("setRate"),
		"value": plist.Real(1.0),
	}))
	if err != nil {
		t.Fatal(err)
	}
	typ, cmd, err := decodeEventCommand(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if typ != "setRate" {
		t.Fatalf("type = %q, want setRate", typ)
	}
	if cmd == nil || cmd.Kind != plist.KindDict {
		t.Fatalf("command = %+v, want dict", cmd)
	}
	if v, ok := cmd.Dict["value"]; !ok || v.Kind != plist.KindReal || v.Real != 1.0 {
		t.Fatalf("value = %+v, want real 1.0", cmd.Dict["value"])
	}
}

// TestDecodeEventCommandNoPayload covers a command body without any payload
// key beyond "type".
func TestDecodeEventCommandNoPayload(t *testing.T) {
	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"type": plist.String("ping"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	typ, cmd, err := decodeEventCommand(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if typ != "ping" {
		t.Fatalf("type = %q, want ping", typ)
	}
	if cmd == nil || cmd.Kind != plist.KindDict {
		t.Fatalf("command = %+v, want dict", cmd)
	}
}

// TestDecodeEventCommandErrors covers malformed and non-dict bodies.
func TestDecodeEventCommandErrors(t *testing.T) {
	if _, _, err := decodeEventCommand([]byte("not a plist")); err == nil {
		t.Fatal("expected error for malformed body")
	}
	body, err := plist.Encode(plist.String("not a dict"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeEventCommand(body); err == nil {
		t.Fatal("expected error for non-dict body")
	}
	body, err = plist.Encode(plist.Dict(map[string]*plist.Value{"nope": plist.Int(1)}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeEventCommand(body); err == nil {
		t.Fatal("expected error for missing type")
	}
}

// playbackStateCommand encodes an updateMRPlaybackState command body with the
// given mrPlaybackState value.
func playbackStateCommand(state int64) []byte {
	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"type": plist.String(commandUpdateMRPlaybackState),
		"params": plist.Dict(map[string]*plist.Value{
			"mrPlaybackState": plist.Int(state),
		}),
	}))
	if err != nil {
		panic(err)
	}
	return body
}

// TestMrPlaybackState extracts the mrPlaybackState field across valid and
// invalid shapes.
func TestMrPlaybackState(t *testing.T) {
	for _, tc := range []struct {
		state int64
		want  playbackState
	}{
		{1, playbackPlaying},
		{2, playbackPaused},
		{3, playbackStopped},
		{4, playbackInterrupted},
		{0, playbackUnknown},
	} {
		_, cmd, err := decodeEventCommand(playbackStateCommand(tc.state))
		if err != nil {
			t.Fatalf("decode state %d: %v", tc.state, err)
		}
		got, ok := mrPlaybackState(cmd)
		if !ok || got != tc.want {
			t.Fatalf("mrPlaybackState(%d) = (%v, %v), want (%v, true)", tc.state, got, ok, tc.want)
		}
	}

	// Missing params.
	_, cmd, _ := decodeEventCommand(playbackStateCommand(1))
	delete(cmd.Dict, "params")
	if _, ok := mrPlaybackState(cmd); ok {
		t.Fatal("mrPlaybackState ok without params")
	}

	// Non-integer state.
	_, cmd, _ = decodeEventCommand(playbackStateCommand(1))
	cmd.Dict["params"].Dict["mrPlaybackState"] = *plist.String("playing")
	if _, ok := mrPlaybackState(cmd); ok {
		t.Fatal("mrPlaybackState ok with string state")
	}
}

// TestHandleEventCommandPlaybackState verifies that an updateMRPlaybackState
// command updates the handler's reported playback state.
func TestHandleEventCommandPlaybackState(t *testing.T) {
	h := &mediaHandler{log: slog.Default()}
	_, cmd, err := decodeEventCommand(playbackStateCommand(2))
	if err != nil {
		t.Fatal(err)
	}
	h.handleEventCommand(commandUpdateMRPlaybackState, cmd)
	if got := h.PlaybackState(); got != playbackPaused {
		t.Fatalf("PlaybackState = %v, want paused", got)
	}
}

// TestParseNowPlaying checks the full now-playing extraction including
// duration, playback rate, and track number.
func TestParseNowPlaying(t *testing.T) {
	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"type": plist.String(commandUpdateMRNowPlayingInfo),
		"params": plist.Dict(map[string]*plist.Value{
			"params": plist.Dict(map[string]*plist.Value{
				"kMRMediaRemoteNowPlayingInfoTitle":            plist.String("Song"),
				"kMRMediaRemoteNowPlayingInfoArtist":           plist.String("Artist"),
				"kMRMediaRemoteNowPlayingInfoAlbum":            plist.String("Album"),
				"kMRMediaRemoteNowPlayingInfoGenre":            plist.String("Rock"),
				"kMRMediaRemoteNowPlayingInfoDuration":         plist.Real(180.5),
				"kMRMediaRemoteNowPlayingInfoPlaybackRate":     plist.Real(1.0),
				"kMRMediaRemoteNowPlayingInfoTrackNumber":      plist.Int(3),
				"kMRMediaRemoteNowPlayingInfoUniqueIdentifier": plist.String("track-1"),
			}),
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, cmd, err := decodeEventCommand(body)
	if err != nil {
		t.Fatal(err)
	}
	np, ok := parseNowPlaying(cmd)
	if !ok {
		t.Fatal("parseNowPlaying ok = false")
	}
	if np.Title != "Song" || np.Artist != "Artist" || np.Album != "Album" {
		t.Fatalf("unexpected identity fields: %+v", np)
	}
	if np.Genre != "Rock" || np.UniqueID != "track-1" {
		t.Fatalf("unexpected genre/id: %+v", np)
	}
	if np.Duration != 180.5 || np.PlaybackRate != 1.0 || np.TrackNumber != 3 {
		t.Fatalf("unexpected numeric fields: %+v", np)
	}
	if got := np.Summary(); got != "Song - Artist (Album)" {
		t.Fatalf("Summary = %q", got)
	}
}

// TestParseNowPlayingArtwork verifies cover-art bytes are extracted and copied
// out of the decoded body.
func TestParseNowPlayingArtwork(t *testing.T) {
	art := []byte{0xff, 0xd8, 0xff, 0xd9} // JPEG magic
	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"type": plist.String(commandUpdateMRNowPlayingInfo),
		"params": plist.Dict(map[string]*plist.Value{
			"params": plist.Dict(map[string]*plist.Value{
				"kMRMediaRemoteNowPlayingInfoTitle":       plist.String("Song"),
				"kMRMediaRemoteNowPlayingInfoArtworkData": plist.Data(art),
			}),
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, cmd, err := decodeEventCommand(body)
	if err != nil {
		t.Fatal(err)
	}
	np, ok := parseNowPlaying(cmd)
	if !ok {
		t.Fatal("parseNowPlaying ok = false")
	}
	if !bytes.Equal(np.Artwork, art) {
		t.Fatalf("Artwork = %x, want %x", np.Artwork, art)
	}
	// The snapshot must not alias the decoded body's buffer.
	np.Artwork[0] = 0x00
	if bytes.Equal(np.Artwork, art) {
		t.Fatal("Artwork aliases the decoded body")
	}
}

// TestParseNowPlayingEmpty verifies ok=false when no identifying field is
// present.
func TestParseNowPlayingEmpty(t *testing.T) {
	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"type": plist.String(commandUpdateMRNowPlayingInfo),
		"params": plist.Dict(map[string]*plist.Value{
			"params": plist.Dict(map[string]*plist.Value{
				"kMRMediaRemoteNowPlayingInfoPlaybackRate": plist.Real(1.0),
			}),
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, cmd, _ := decodeEventCommand(body)
	if _, ok := parseNowPlaying(cmd); ok {
		t.Fatal("parseNowPlaying ok = true for playback-rate-only body")
	}
}

// TestHandleEventCommandNowPlaying verifies that an updateMRNowPlayingInfo
// command updates the handler's now-playing snapshot.
func TestHandleEventCommandNowPlaying(t *testing.T) {
	h := &mediaHandler{log: slog.Default()}
	body, err := plist.Encode(plist.Dict(map[string]*plist.Value{
		"type": plist.String(commandUpdateMRNowPlayingInfo),
		"params": plist.Dict(map[string]*plist.Value{
			"params": plist.Dict(map[string]*plist.Value{
				"kMRMediaRemoteNowPlayingInfoTitle":  plist.String("Song"),
				"kMRMediaRemoteNowPlayingInfoArtist": plist.String("Artist"),
			}),
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, cmd, err := decodeEventCommand(body)
	if err != nil {
		t.Fatal(err)
	}
	h.handleEventCommand(commandUpdateMRNowPlayingInfo, cmd)
	if got := h.NowPlaying(); got == nil || got.Title != "Song" || got.Artist != "Artist" {
		t.Fatalf("NowPlaying = %+v", got)
	}
}

// TestPlaybackStateString covers the String method.
func TestPlaybackStateString(t *testing.T) {
	if playbackPlaying.String() != "playing" || playbackUnknown.String() != "unknown" {
		t.Fatalf("unexpected String values: %q %q", playbackPlaying.String(), playbackUnknown.String())
	}
}
