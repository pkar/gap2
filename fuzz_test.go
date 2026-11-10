package airplay2

import (
	"bufio"
	"bytes"
	"testing"
)

// FuzzControlParsers feeds arbitrary bytes to the parsers a sender reaches
// before and after pairing: SETUP bodies, event commands, RTSP requests on the
// control and event channels, and control-port timing packets. None may panic.
func FuzzControlParsers(f *testing.F) {
	f.Add([]byte("POST /command RTSP/1.0\r\nContent-Length: 3\r\n\r\nabc"))
	f.Add([]byte("bplist00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseAp2Setup(data)
		_, _, _ = decodeEventCommand(data)
		_, _ = readEventRequest(bufio.NewReader(bytes.NewReader(data)), Limits{}.withDefaults())
		cs := &connState{br: bufio.NewReader(bytes.NewReader(data)), limits: Limits{}.withDefaults()}
		_, _ = cs.readRequest()
		if len(data) >= 28 {
			_, _, _, _ = ap2ControlAnchor(data)
		}
	})
}
