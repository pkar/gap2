package airplay2

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/pkar/gap2/internal/hap"
	"github.com/pkar/gap2/internal/plist"
)

// serveEvent accepts the AirPlay 2 event-channel TCP connection on ln, wraps
// it with the event cipher, pushes an updateInfo plist, and then reads
// remote-control commands until the sender closes the connection or the
// session is torn down. The event channel shares the control channel's shared
// secret but derives independent keys with the event-specific salt and info
// labels.
func (h *mediaHandler) serveEvent(ln net.Listener, sessionKey []byte, legacy bool) {
	if ln == nil {
		return
	}
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	if legacy {
		// The main control socket stays plaintext for binary pairing, but
		// events have their own salt and directional keys. Never reuse a main
		// channel key/counter on this separate connection.
		h.log.Debug("legacy event channel connected")
	}
	ec := hap.NewEventConn(conn, sessionKey)
	if err := h.sendUpdateInfo(ec); err != nil {
		h.log.Debug("event updateInfo failed", "err", err)
		return
	}
	h.readEventCommands(ec)
}

// sendUpdateInfo writes the receiver info dictionary wrapped in an updateInfo
// event to w. It is a no-op when no info provider is configured (tests and
// early construction).
func (h *mediaHandler) sendUpdateInfo(w io.Writer) error {
	if h.info == nil {
		return nil
	}
	update := plist.Dict(map[string]*plist.Value{
		"type":  plist.String("updateInfo"),
		"value": h.info(),
	})
	body, err := plist.Encode(update)
	if err != nil {
		return err
	}
	return writeEventCommand(w, body)
}

// Event-channel command types the sender posts to the receiver. These are
// one-way notifications (no response is expected), carrying metadata and
// playback state from the source device.
const (
	commandUpdateMRNowPlayingInfo    = "updateMRNowPlayingInfo"
	commandUpdateMRSupportedCommands = "updateMRSupportedCommands"
	commandUpdateMRPlaybackState     = "updateMRPlaybackState"
)

// playbackState is the sender-reported play/pause state carried by
// updateMRPlaybackState commands, matching MediaRemote's MRPlaybackState
// values.
type playbackState uint32

const (
	playbackUnknown     playbackState = 0
	playbackPlaying     playbackState = 1
	playbackPaused      playbackState = 2
	playbackStopped     playbackState = 3
	playbackInterrupted playbackState = 4
)

// String returns a human-readable name for the playback state.
func (s playbackState) String() string {
	switch s {
	case playbackPlaying:
		return "playing"
	case playbackPaused:
		return "paused"
	case playbackStopped:
		return "stopped"
	case playbackInterrupted:
		return "interrupted"
	default:
		return "unknown"
	}
}

// PlaybackState returns the most recently reported playback state.
func (h *mediaHandler) PlaybackState() playbackState {
	return playbackState(h.playback.Load())
}

// readEventCommands reads the remote-control notifications the sender posts
// over the event channel (updateMRNowPlayingInfo, updateMRSupportedCommands
// and updateMRPlaybackState) and dispatches each decoded command. These are
// one-way notifications: the sender expects no response.
func (h *mediaHandler) readEventCommands(ec *hap.Conn) {
	br := bufio.NewReader(ec)
	for {
		req, err := readEventRequest(br, h.cfg.Limits)
		if err != nil {
			if err != io.EOF {
				h.log.Debug("event read failed", "err", err)
			}
			return
		}
		typ, cmd, err := decodeEventCommand(req.body)
		if err != nil {
			h.log.Debug("event command decode failed", "err", err, "bytes", len(req.body))
			continue
		}
		h.handleEventCommand(typ, cmd)
	}
}

// handleEventCommand dispatches a decoded event-channel command. The sender's
// commands are notifications: updateMRPlaybackState updates the playback
// state, updateMRNowPlayingInfo replaces the now-playing snapshot, and
// supported-commands updates are recognised but not actionable. No response is
// written back.
func (h *mediaHandler) handleEventCommand(typ string, cmd *plist.Value) {
	switch typ {
	case commandUpdateMRPlaybackState:
		if st, ok := mrPlaybackState(cmd); ok {
			h.playback.Store(uint32(st))
			h.log.Debug("event playback state", "state", st.String())
			return
		}
	case commandUpdateMRNowPlayingInfo:
		if np, ok := parseNowPlaying(cmd); ok {
			h.nowPlaying.Store(np)
			h.log.Debug("event now playing", "info", np.Summary())
			return
		}
	case commandUpdateMRSupportedCommands:
		// Informational; nothing actionable for a receive-only device.
	default:
	}
	h.log.Debug("event command", "type", typ)
}

// decodeEventCommand decodes one event-channel command body, a binary plist
// dict, and returns its "type" string together with the full command dict.
func decodeEventCommand(body []byte) (string, *plist.Value, error) {
	v, err := plist.Decode(body, plist.DefaultLimits())
	if err != nil {
		return "", nil, err
	}
	if v == nil || v.Kind != plist.KindDict {
		return "", nil, fmt.Errorf("airplay2: event command body is not a dict")
	}
	typ, ok := v.Dict["type"]
	if !ok || typ.Kind != plist.KindString {
		return "", nil, fmt.Errorf("airplay2: event command missing type")
	}
	return typ.String, v, nil
}

// mrPlaybackState extracts the mrPlaybackState field of an
// updateMRPlaybackState command (command.params.mrPlaybackState).
func mrPlaybackState(cmd *plist.Value) (playbackState, bool) {
	params, ok := dictField(cmd, "params")
	if !ok {
		return 0, false
	}
	st, ok := params.Dict["mrPlaybackState"]
	if !ok || st.Kind != plist.KindInt {
		return 0, false
	}
	return playbackState(st.Int), true
}

// NowPlaying is a snapshot of the current track reported by the sender over
// the event channel. Zero-valued fields are unknown/absent. The snapshot is
// immutable after it is stored.
type NowPlaying struct {
	Title        string
	Artist       string
	Album        string
	Genre        string
	Composer     string
	UniqueID     string
	Duration     float64 // seconds; 0 when unknown
	PlaybackRate float64
	TrackNumber  int64
	// Artwork is the cover art image (JPEG/PNG) bytes, or nil when absent.
	Artwork []byte
}

// Summary returns a concise "title - artist (album)" description.
func (n *NowPlaying) Summary() string {
	var b strings.Builder
	if n.Title != "" {
		b.WriteString(n.Title)
	}
	if n.Artist != "" {
		if b.Len() > 0 {
			b.WriteString(" - ")
		}
		b.WriteString(n.Artist)
	}
	if n.Album != "" {
		if b.Len() > 0 {
			b.WriteString(" (")
			b.WriteString(n.Album)
			b.WriteByte(')')
		}
	}
	return b.String()
}

// NowPlaying returns the most recently reported now-playing snapshot, or nil
// if none has been received.
func (h *mediaHandler) NowPlaying() *NowPlaying {
	return h.nowPlaying.Load()
}

// parseNowPlaying extracts the now-playing fields of an
// updateMRNowPlayingInfo command. The now-playing dictionary nests under
// command.params.params and uses MediaRemote keys. ok is false when no
// identifying field is present.
func parseNowPlaying(cmd *plist.Value) (*NowPlaying, bool) {
	params, ok := dictField(cmd, "params")
	if !ok {
		return nil, false
	}
	npi, ok := dictField(params, "params")
	if !ok {
		return nil, false
	}
	np := &NowPlaying{
		Title:        stringField(npi, "kMRMediaRemoteNowPlayingInfoTitle"),
		Artist:       stringField(npi, "kMRMediaRemoteNowPlayingInfoArtist"),
		Album:        stringField(npi, "kMRMediaRemoteNowPlayingInfoAlbum"),
		Genre:        stringField(npi, "kMRMediaRemoteNowPlayingInfoGenre"),
		Composer:     stringField(npi, "kMRMediaRemoteNowPlayingInfoComposer"),
		UniqueID:     stringField(npi, "kMRMediaRemoteNowPlayingInfoUniqueIdentifier"),
		Duration:     realField(npi, "kMRMediaRemoteNowPlayingInfoDuration"),
		PlaybackRate: realField(npi, "kMRMediaRemoteNowPlayingInfoPlaybackRate"),
		TrackNumber:  intField(npi, "kMRMediaRemoteNowPlayingInfoTrackNumber"),
		Artwork:      dataField(npi, "kMRMediaRemoteNowPlayingInfoArtworkData"),
	}
	if np.Title == "" && np.Artist == "" && np.Album == "" && np.UniqueID == "" {
		return nil, false
	}
	return np, true
}

// dictField returns the dict-valued field key of a dict node, or ok=false when
// cmd is not a dict or the field is absent or not a dict.
func dictField(cmd *plist.Value, key string) (*plist.Value, bool) {
	if cmd == nil || cmd.Kind != plist.KindDict {
		return nil, false
	}
	v, ok := cmd.Dict[key]
	if !ok || v.Kind != plist.KindDict {
		return nil, false
	}
	return &v, true
}

// stringField returns the string-valued field key of a dict node, or "".
func stringField(dict *plist.Value, key string) string {
	if dict == nil || dict.Kind != plist.KindDict {
		return ""
	}
	v, ok := dict.Dict[key]
	if !ok || v.Kind != plist.KindString {
		return ""
	}
	return v.String
}

// realField returns the real-valued field key of a dict node, or 0.
func realField(dict *plist.Value, key string) float64 {
	if dict == nil || dict.Kind != plist.KindDict {
		return 0
	}
	v, ok := dict.Dict[key]
	if !ok || v.Kind != plist.KindReal {
		return 0
	}
	return v.Real
}

// intField returns the integer-valued field key of a dict node, or 0.
func intField(dict *plist.Value, key string) int64 {
	if dict == nil || dict.Kind != plist.KindDict {
		return 0
	}
	v, ok := dict.Dict[key]
	if !ok || v.Kind != plist.KindInt {
		return 0
	}
	return v.Int
}

// dataField returns the data-valued field key of a dict node as a copy, or
// nil. The copy detaches the snapshot from the decoded request buffer so the
// immutable snapshot does not alias transient input.
func dataField(dict *plist.Value, key string) []byte {
	if dict == nil || dict.Kind != plist.KindDict {
		return nil
	}
	v, ok := dict.Dict[key]
	if !ok || v.Kind != plist.KindData {
		return nil
	}
	return append([]byte(nil), v.Data...)
}

// readEventRequest parses one event-channel command request using the same
// framing as the control channel, bounded by l.
func readEventRequest(br *bufio.Reader, l Limits) (*ctlRequest, error) {
	start, err := readLineLimited(br, l.MaxRequestBytes)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(string(start), " ", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("airplay2: malformed event request line")
	}
	headers, err := readHeaderBlock(br, l.MaxHeaders, l.MaxHeaderBytes)
	if err != nil {
		return nil, err
	}
	n := contentLength(headers)
	if n < 0 {
		return nil, fmt.Errorf("airplay2: invalid content length")
	}
	if n > int64(l.MaxBodyBytes) {
		return nil, fmt.Errorf("airplay2: event body too large: %d", n)
	}
	body := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, err
		}
	}
	return &ctlRequest{
		method:  parts[0],
		target:  parts[1],
		version: parts[2],
		headers: headers,
		body:    body,
	}, nil
}

// writeEventCommand writes one event-channel command request over w.
func writeEventCommand(w io.Writer, body []byte) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "POST /command RTSP/1.0\r\n")
	fmt.Fprintf(&b, "Content-Length: %d\r\n", len(body))
	b.WriteString("Content-Type: application/x-apple-binary-plist\r\n")
	b.WriteString("\r\n")
	b.Write(body)
	return writeAll(w, b.Bytes())
}
