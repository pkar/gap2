package airplay2

import (
	"errors"

	"github.com/pkar/gap2/internal/plist"
)

// AirPlay 2 stream types carried in the "streams" array of a SETUP request.
const (
	ap2StreamRealtime = 96  // realtime audio over UDP RTP
	ap2StreamBuffered = 103 // buffered audio over TCP
)

// errNotAp2Setup reports a SETUP body that is not an AirPlay 2 plist (for
// example the empty body of an AirPlay 1 Transport-header SETUP).
var errNotAp2Setup = errors.New("airplay2: not an AP2 plist SETUP")

// ap2SetupKind classifies an AirPlay 2 SETUP request by the shape of its
// plist body: the initial (timing) exchange has no "streams" key, while the
// media exchange carries a "streams" array.
type ap2SetupKind uint8

const (
	setupInitial ap2SetupKind = iota
	setupStream
)

// ap2SetupRequest is a parsed AirPlay 2 SETUP plist body.
type ap2SetupRequest struct {
	kind                     ap2SetupKind
	timingProtocol           string // "PTP", "NTP", "None", or ""
	groupUUID                string
	groupContainsGroupLeader bool
	streamTypes              []int64 // requested stream types, in order
}

// parseAp2Setup decodes body as an AirPlay 2 SETUP plist. It returns
// errNotAp2Setup when body is not a plist dictionary (the AirPlay 1 case).
func parseAp2Setup(body []byte) (*ap2SetupRequest, error) {
	v, err := plist.Decode(body, plist.DefaultLimits())
	if err != nil || v == nil || v.Kind != plist.KindDict {
		return nil, errNotAp2Setup
	}

	req := &ap2SetupRequest{}
	if streams, ok := v.Dict["streams"]; ok {
		req.kind = setupStream
		if streams.Kind == plist.KindArray {
			for i := range streams.Array {
				it := &streams.Array[i]
				if it.Kind != plist.KindDict {
					continue
				}
				if t, ok := it.Dict["type"]; ok && t.Kind == plist.KindInt {
					req.streamTypes = append(req.streamTypes, t.Int)
				}
			}
		}
		return req, nil
	}

	req.kind = setupInitial
	if tp, ok := v.Dict["timingProtocol"]; ok && tp.Kind == plist.KindString {
		req.timingProtocol = tp.String
	}
	if g, ok := v.Dict["groupUUID"]; ok && g.Kind == plist.KindString {
		req.groupUUID = g.String
	}
	if g, ok := v.Dict["groupContainsGroupLeader"]; ok && g.Kind == plist.KindBool {
		req.groupContainsGroupLeader = g.Bool
	}
	return req, nil
}

// buildAp2InitialResponse builds the initial SETUP response plist: the TCP
// event port for remote-control events, a timingPort of 0 (PTP carries timing
// over multicast rather than a dedicated port), and the receiver's
// timingPeerInfo.
func buildAp2InitialResponse(eventPort uint16, selfIP string) ([]byte, error) {
	dict := plist.Dict(map[string]*plist.Value{
		"eventPort":  plist.Int(int64(eventPort)),
		"timingPort": plist.Int(0),
		"timingPeerInfo": plist.Dict(map[string]*plist.Value{
			"Addresses": plist.Array(plist.String(selfIP)),
			"ID":        plist.String(selfIP),
		}),
	})
	return plist.Encode(dict)
}

// buildAp2StreamResponse builds the stream SETUP response plist for one media
// stream, echoing the negotiated type and the receiver's data and control
// ports. Buffered streams additionally advertise their audio buffer size.
func buildAp2StreamResponse(streamType int64, dataPort, controlPort uint16, bufferSize uint32) ([]byte, error) {
	entry := map[string]*plist.Value{
		"type":        plist.Int(streamType),
		"dataPort":    plist.Int(int64(dataPort)),
		"controlPort": plist.Int(int64(controlPort)),
	}
	if streamType == ap2StreamBuffered {
		entry["audioBufferSize"] = plist.Int(int64(bufferSize))
	}
	dict := plist.Dict(map[string]*plist.Value{
		"streams": plist.Array(plist.Dict(entry)),
	})
	return plist.Encode(dict)
}
