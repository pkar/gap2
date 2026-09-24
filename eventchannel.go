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
func (h *mediaHandler) serveEvent(ln net.Listener, sessionKey []byte) {
	if ln == nil {
		return
	}
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

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
// state, while the now-playing-info and supported-commands updates are logged
// for observability. No response is written back.
func (h *mediaHandler) handleEventCommand(typ string, cmd *plist.Value) {
	switch typ {
	case commandUpdateMRPlaybackState:
		if st, ok := mrPlaybackState(cmd); ok {
			h.playback.Store(uint32(st))
			h.log.Debug("event playback state", "state", st.String())
			return
		}
	case commandUpdateMRNowPlayingInfo:
		if info := nowPlayingInfo(cmd); info != "" {
			h.log.Debug("event now playing", "info", info)
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

// nowPlayingInfo extracts a concise "title - artist (album)" description of
// the track from an updateMRNowPlayingInfo command, or "" when no recognised
// field is present. The now-playing dictionary nests under
// command.params.params and uses MediaRemote keys.
func nowPlayingInfo(cmd *plist.Value) string {
	params, ok := dictField(cmd, "params")
	if !ok {
		return ""
	}
	npi, ok := dictField(params, "params")
	if !ok {
		return ""
	}
	title := stringField(npi, "kMRMediaRemoteNowPlayingInfoTitle")
	artist := stringField(npi, "kMRMediaRemoteNowPlayingInfoArtist")
	album := stringField(npi, "kMRMediaRemoteNowPlayingInfoAlbum")
	if title == "" && artist == "" && album == "" {
		return ""
	}
	var b strings.Builder
	if title != "" {
		b.WriteString(title)
	}
	if artist != "" {
		if b.Len() > 0 {
			b.WriteString(" - ")
		}
		b.WriteString(artist)
	}
	if album != "" {
		if b.Len() > 0 {
			b.WriteString(" (")
			b.WriteString(album)
			b.WriteByte(')')
		}
	}
	return b.String()
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
