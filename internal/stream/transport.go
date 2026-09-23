// Package stream models one AirPlay media stream: the ANNOUNCE/SETUP/RECORD
// negotiation, RTP ingest, access-unit extraction, decoding, and handoff of
// decoded PCM to a sink. It is transport-agnostic so the network listener can
// live in the receiver assembly while this package stays unit-testable.
package stream

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Errors returned while negotiating a stream.
var (
	ErrBadTransport = errors.New("stream: invalid transport header")
	ErrUnsupported  = errors.New("stream: unsupported transport or encoding")
)

// Transport describes the negotiated RTP delivery parameters.
type Transport struct {
	// Protocol is "RTP/AVP/UDP" or "RTP/AVP/TCP".
	Protocol string
	// Mode is "unicast" or "multicast".
	Mode string
	// ClientPort and ClientPortEnd are the sender's RTP/RTCP ports. A zero
	// ClientPortEnd means only ClientPort was given.
	ClientPort    int
	ClientPortEnd int
	// ServerPort and ServerPortEnd are the receiver's RTP/RTCP ports.
	ServerPort    int
	ServerPortEnd int
	// Interleaved and InterleavedEnd carry the RTP/RTCP channel numbers for
	// TCP interleaving. InterleavedEnd is zero when absent.
	Interleaved    int
	InterleavedEnd int
}

// ParseTransport parses an RTSP Transport header value such as
// "RTP/AVP/UDP;unicast;client_port=6000-6001" or
// "RTP/AVP/TCP;unicast;interleaved=0-1".
func ParseTransport(v string) (Transport, error) {
	var t Transport
	parts := strings.Split(v, ";")
	if len(parts) == 0 || parts[0] == "" {
		return t, ErrBadTransport
	}
	t.Protocol = strings.TrimSpace(parts[0])
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 1 {
			switch strings.ToLower(kv[0]) {
			case "unicast":
				t.Mode = "unicast"
			case "multicast":
				t.Mode = "multicast"
			default:
				// Unknown bare parameters are ignored for forward compatibility.
			}
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.TrimSpace(kv[1])
		switch key {
		case "client_port":
			start, end, err := parsePortRange(val)
			if err != nil {
				return t, err
			}
			t.ClientPort, t.ClientPortEnd = start, end
		case "server_port":
			start, end, err := parsePortRange(val)
			if err != nil {
				return t, err
			}
			t.ServerPort, t.ServerPortEnd = start, end
		case "interleaved":
			start, end, err := parsePortRange(val)
			if err != nil {
				return t, err
			}
			t.Interleaved, t.InterleavedEnd = start, end
		}
	}
	if t.Protocol == "" {
		return t, ErrBadTransport
	}
	return t, nil
}

func parsePortRange(v string) (start, end int, err error) {
	parts := strings.SplitN(v, "-", 2)
	start, err = parsePort(parts[0])
	if err != nil {
		return 0, 0, err
	}
	if len(parts) == 2 {
		end, err = parsePort(parts[1])
		if err != nil {
			return 0, 0, err
		}
	}
	return start, end, nil
}

func parsePort(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("%w: %q", ErrBadTransport, v)
	}
	return n, nil
}
