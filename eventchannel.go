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

// readEventCommands reads and logs the remote-control commands the sender posts
// over the event channel (for example updateInfo, updateAudioFormat, and
// updateProgress). Commands are framed as "POST /command RTSP/1.0" requests
// with binary-plist bodies. Received commands are currently acknowledged
// implicitly by keeping the channel open; command handling is a future
// extension.
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
		h.log.Debug("event command", "method", req.method, "target", req.target, "body", len(req.body))
	}
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
