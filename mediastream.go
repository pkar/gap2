package airplay2

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/pkar/gap2/internal/hap"
	"github.com/pkar/gap2/internal/media"
	"github.com/pkar/gap2/internal/playout"
	"github.com/pkar/gap2/internal/plist"
	"github.com/pkar/gap2/internal/ptp"
	"github.com/pkar/gap2/internal/sdp"
	"github.com/pkar/gap2/internal/stream"
	"github.com/pkar/gap2/pcm"
)

// mediaHandler owns the media session lifecycle for one control connection:
// it turns ANNOUNCE/SETUP/RECORD/TEARDOWN into a media.Session whose receiver
// feeds RTP datagrams into the AAC decoder and sink.
type mediaHandler struct {
	cfg    Config
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	clock  *ptp.Clock

	sess            *media.Session
	sink            pcm.Sink
	volume          float64
	gain            *pcm.Gain
	playout         *playout.Synced
	recordRequested bool
	mediaStarted    bool
	mediaCancel     context.CancelFunc
	streamType      int64

	// info supplies the receiver info dictionary pushed as the event-channel
	// updateInfo; it is injected from the control server after pairing.
	info func() *plist.Value

	// bind is overridable in tests to avoid binding a real socket.
	bind func(network, addr string) (int, error)

	// listenEvent and listenControl are overridable in tests (the sandbox
	// forbids socket binds). They open the AP2 event TCP listener and control
	// UDP socket respectively.
	listenEvent   func() (net.Listener, error)
	listenControl func() (net.PacketConn, error)

	// eventLn is the TCP listener handed back to the sender as the AP2 event
	// port; controlConn is the UDP socket for the AP2 control port. Both stay
	// open for the life of the session so the advertised ports remain valid.
	eventLn     net.Listener
	controlConn net.PacketConn

	// playback is the sender-reported playback state carried by
	// updateMRPlaybackState commands over the event channel. It is written
	// from the event-channel goroutine and read via PlaybackState, so it uses
	// an atomic.
	playback atomic.Uint32

	// nowPlaying is the most recent now-playing snapshot reported over the
	// event channel. The pointed-to value is immutable after storage, so it
	// can be shared with readers via NowPlaying without a lock.
	nowPlaying atomic.Pointer[NowPlaying]
}

func newMediaHandler(cfg Config, log *slog.Logger, clock *ptp.Clock) *mediaHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &mediaHandler{cfg: cfg, log: log, ctx: ctx, cancel: cancel, clock: clock}
}

// close cancels any active serve loop and releases the receiver and sink. It
// is idempotent and safe to call after Teardown.
func (h *mediaHandler) close() {
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.closeAudio()
	if h.eventLn != nil {
		_ = h.eventLn.Close()
		h.eventLn = nil
	}
}

// closeAudio releases only the stream. AP2 keeps control, pairing, and the
// event connection alive when a TEARDOWN names entries in "streams".
func (h *mediaHandler) closeAudio() {
	if h.mediaCancel != nil {
		h.mediaCancel()
		h.mediaCancel = nil
	}
	if h.sess != nil {
		_ = h.sess.Stream().Teardown()
		_ = h.sess.Close()
		h.sess = nil
	}
	if h.sink != nil {
		_ = h.sink.Close()
		h.sink = nil
	}
	if h.controlConn != nil {
		_ = h.controlConn.Close()
		h.controlConn = nil
	}
	h.gain, h.playout = nil, nil
	h.mediaStarted, h.streamType = false, 0
}

// handleMedia dispatches one RTSP media request on an encrypted control
// connection.
func (s *controlServer) handleMedia(cs *connState, req *ctlRequest) error {
	if cs.media == nil {
		cs.media = newMediaHandler(s.cfg, s.log, s.clock)
		cs.media.info = s.infoValue
	}
	h := cs.media

	switch req.method {
	case "ANNOUNCE":
		return h.announce(cs, req)
	case "SETUP":
		return h.setup(cs, req)
	case "RECORD":
		return h.record(cs, req)
	case "TEARDOWN":
		return h.teardown(cs, req)
	case "FLUSH", "FLUSHBUFFERED":
		return h.flush(cs, req)
	case "SETRATEANCHORI", "SETRATEANCHORTI", "SETRATEANCHORTIME":
		return h.setRateAnchor(cs, req)
	case "GET_PARAMETER":
		if strings.TrimSpace(string(req.body)) == "volume" {
			return cs.writeRTSPResponse(reqHeader(req, "CSeq"), 200, "OK",
				map[string]string{"Content-Type": "text/parameters"}, []byte(fmt.Sprintf("volume: %.6f\r\n", h.volume)))
		}
		return cs.writeRTSPResponse(reqHeader(req, "CSeq"), 200, "OK", nil, nil)
	case "SET_PARAMETER":
		if value, ok := strings.CutPrefix(strings.TrimSpace(string(req.body)), "volume:"); ok {
			volume, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || math.IsNaN(volume) || math.IsInf(volume, 0) || volume > 0 || volume < -144 {
				return cs.writeError(400, "Bad Request")
			}
			h.volume = volume
			if h.gain != nil {
				h.gain.SetDB(volume)
			}
		}
		return cs.writeRTSPResponse(reqHeader(req, "CSeq"), 200, "OK", nil, nil)
	case "SETPEERS", "SETPEERSX", "LOUDNESSNORMALIZATION":
		return cs.writeRTSPResponse(reqHeader(req, "CSeq"), 200, "OK", nil, nil)
	default:
		return cs.writeRTSPResponse(reqHeader(req, "CSeq"), 501, "Not Implemented", nil, nil)
	}
}

func (h *mediaHandler) announce(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")

	m, err := sdp.Parse(req.body, sdp.DefaultLimits())
	if err != nil {
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}
	if h.cfg.Output == nil {
		return cs.writeRTSPResponse(cseq, 503, "Service Unavailable", nil, nil)
	}
	format, err := stream.MediaFormat(m)
	if err != nil {
		return cs.writeRTSPResponse(cseq, 415, "Unsupported Media Type", nil, nil)
	}
	// Validate the codec before opening the sink so an unsupported format (for
	// example 24-bit or multichannel ALAC) is rejected without opening output.
	// NewSession re-derives the decoder below; construction is cheap.
	if _, err := stream.NewDecoder(m); err != nil {
		h.log.Debug("media codec rejected", "err", err)
		return cs.writeRTSPResponse(cseq, 415, "Unsupported Media Type", nil, nil)
	}
	sink, err := h.cfg.Output.Open(h.ctx, format)
	if err != nil {
		h.log.Debug("media sink open failed", "err", err)
		return cs.writeRTSPResponse(cseq, 500, "Internal Server Error", nil, nil)
	}
	h.gain = pcm.NewGain(sink, h.volume)
	h.sink = h.gain

	// Wrap the output sink in a PTP-synchronized scheduler so decoded blocks
	// are written at their anchor-derived presentation times rather than as
	// soon as they are received.
	var out pcm.Sink = h.gain
	if h.clock != nil {
		h.playout = playout.NewSynced(h.gain, h.clock.FrameLocalTime, ptp.MonotonicNanos)
		out = h.playout
	}

	sess, err := media.NewSession(streamID(req.target), m, out)
	if err != nil {
		return cs.writeRTSPResponse(cseq, 415, "Unsupported Media Type", nil, nil)
	}
	if err := sess.Stream().Announce(m); err != nil {
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}
	h.sess = sess
	if h.clock != nil {
		h.clock.ClearAnchor()
	}

	return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
}

func (h *mediaHandler) setup(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")

	// AirPlay 2 carries SETUP parameters in a binary plist body; AirPlay 1
	// uses a Transport header with an empty body. Prefer the plist form when
	// the body decodes as one.
	if len(req.body) > 0 {
		if s, err := parseAp2Setup(req.body); err == nil {
			return h.setupAp2(cs, cseq, s)
		} else {
			h.log.Debug("AP2 SETUP parse failed", "err", err)
		}
	}

	if h.sess == nil {
		return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
	}
	tr, err := stream.ParseTransport(reqHeader(req, "Transport"))
	if err != nil {
		return cs.writeRTSPResponse(cseq, 461, "Unsupported Transport", nil, nil)
	}

	bind := h.bind
	if bind == nil {
		bind = h.sess.Bind
	}
	port, err := bind("udp4", ":0")
	if err != nil {
		h.log.Debug("media bind failed", "err", err)
		return cs.writeRTSPResponse(cseq, 500, "Internal Server Error", nil, nil)
	}
	if err := h.sess.Stream().Setup(tr, port); err != nil {
		return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
	}

	headers := map[string]string{
		"Transport": fmt.Sprintf("RTP/AVP/UDP;unicast;server_port=%d-%d", port, port+1),
		"Session":   h.sess.Stream().ID(),
	}
	return cs.writeRTSPResponse(cseq, 200, "OK", headers, nil)
}

// setupAp2 dispatches a parsed AirPlay 2 SETUP body to the timing or media
// exchange.
func (h *mediaHandler) setupAp2(cs *connState, cseq string, s *ap2SetupRequest) error {
	switch s.kind {
	case setupInitial:
		return h.setupInitialAp2(cs, cseq, s)
	case setupStream:
		return h.setupStreamAp2(cs, cseq, s)
	default:
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}
}

// setupInitialAp2 answers the initial AP2 SETUP: it accepts PTP timing and
// returns the event port, a timingPort of 0, and the receiver's timing peer
// info.
func (h *mediaHandler) setupInitialAp2(cs *connState, cseq string, s *ap2SetupRequest) error {
	if s.timingProtocol != "PTP" {
		h.log.Debug("AP2 SETUP timing protocol unsupported", "timingProtocol", s.timingProtocol)
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}

	listen := h.listenEvent
	if listen == nil {
		listen = func() (net.Listener, error) { return net.Listen("tcp4", ":0") }
	}
	ln, err := listen()
	if err != nil {
		h.log.Debug("AP2 event listen failed", "err", err)
		return cs.writeRTSPResponse(cseq, 500, "Internal Server Error", nil, nil)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	h.eventLn = ln

	// Start serving the encrypted event channel once the sender connects to
	// the advertised port. The listener is passed explicitly (rather than read
	// from h.eventLn) to avoid racing with close() during teardown.
	if len(cs.sessionKey) > 0 {
		go h.serveEvent(ln, cs.sessionKey, cs.legacySetup)
	}

	body, err := buildAp2InitialResponse(port, localIP(cs.conn))
	if err != nil {
		return cs.writeRTSPResponse(cseq, 500, "Internal Server Error", nil, nil)
	}
	return cs.writeRTSPResponse(cseq, 200, "OK",
		map[string]string{"Content-Type": "application/x-apple-binary-plist"}, body)
}

// setupStreamAp2 answers the media SETUP: it selects a supported stream type
// (realtime UDP or buffered TCP audio), binds the data and AP2 control ports, and returns
// them in the streams array.
func (h *mediaHandler) setupStreamAp2(cs *connState, cseq string, s *ap2SetupRequest) error {
	h.log.Debug("AP2 requested streams", "types", s.streamTypes)
	var stype int64 = -1
	for _, t := range s.streamTypes {
		if t == ap2StreamRealtime || t == ap2StreamBuffered {
			stype = t
			break
		}
	}
	if stype < 0 {
		return cs.writeRTSPResponse(cseq, 415, "Unsupported Media Type", nil, nil)
	}
	if h.sess == nil {
		for _, entry := range s.streams {
			if entry.Dict["type"].Int == stype {
				if err := h.openNativeStream(entry); err != nil {
					h.log.Debug("AP2 stream rejected", "err", err)
					return cs.writeRTSPResponse(cseq, 415, "Unsupported Media Type", nil, nil)
				}
				break
			}
		}
		if h.sess == nil {
			return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
		}
	}

	bind := h.bind
	if bind == nil {
		bind = h.sess.Bind
	}
	network, protocol := "udp4", "RTP/AVP/UDP"
	var bufferSize uint32
	if stype == ap2StreamBuffered {
		network, protocol = "tcp4", "RTP/AVP/TCP"
		bufferSize = media.BufferedAudioBytes
	}
	dataPort, err := bind(network, ":0")
	if err != nil {
		h.log.Debug("media bind failed", "err", err)
		return cs.writeRTSPResponse(cseq, 500, "Internal Server Error", nil, nil)
	}

	// AP2 has no Transport header; synthesize the negotiated UDP unicast
	// transport from the stream type.
	tr := stream.Transport{Protocol: protocol, Mode: "unicast"}
	if err := h.sess.Stream().Setup(tr, dataPort); err != nil {
		return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
	}
	h.streamType = stype

	listenCtrl := h.listenControl
	if listenCtrl == nil {
		listenCtrl = func() (net.PacketConn, error) {
			return net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
		}
	}
	cconn, err := listenCtrl()
	if err != nil {
		h.log.Debug("AP2 control bind failed", "err", err)
		return cs.writeRTSPResponse(cseq, 500, "Internal Server Error", nil, nil)
	}
	h.controlConn = cconn
	controlPort := uint16(cconn.LocalAddr().(*net.UDPAddr).Port)

	// Drain the advertised control port so RTCP feedback from the sender is
	// consumed rather than accumulating in the socket buffer. The conn and
	// sample rate are passed explicitly (rather than read from h.controlConn /
	// h.sess) to avoid racing with close() and teardown.
	go h.serveControl(cconn, h.sess.Stream().Format().Rate)

	body, err := buildAp2StreamResponse(stype, uint16(dataPort), controlPort, bufferSize)
	if err != nil {
		return cs.writeRTSPResponse(cseq, 500, "Internal Server Error", nil, nil)
	}
	if h.recordRequested {
		if err := h.startMedia(); err != nil {
			return cs.writeError(455, "Method Not Valid in This State")
		}
	}
	return cs.writeRTSPResponse(cseq, 200, "OK",
		map[string]string{"Content-Type": "application/x-apple-binary-plist"}, body)
}

// Native AP2 carries the codec and key in SETUP, without an SDP ANNOUNCE.
func (h *mediaHandler) openNativeStream(entry *plist.Value) error {
	integer := func(name string, fallback int) int {
		if v, ok := entry.Dict[name]; ok && v.Kind == plist.KindInt {
			return int(v.Int)
		}
		return fallback
	}
	ct, rate, channels, frames := integer("ct", 2), integer("sr", 44100), integer("ch", 2), integer("spf", 352)
	h.log.Debug("AP2 audio format", "type", integer("type", 0), "ct", ct, "rate", rate,
		"channels", channels, "frames", frames, "audioFormat", integer("audioFormat", 0))
	if (ct != 2 && ct != 4) || (rate != 44100 && rate != 48000) || channels != 2 || frames <= 0 || frames > 4096 {
		return fmt.Errorf("unsupported AP2 audio format")
	}
	key, ok := entry.Dict["shk"]
	if !ok || key.Kind != plist.KindData {
		return fmt.Errorf("AP2 stream missing encryption key")
	}
	decryptor, err := hap.NewAudioDecryptor(key.Data)
	if err != nil {
		return err
	}
	if h.cfg.Output == nil {
		return fmt.Errorf("no PCM output configured")
	}
	m := &sdp.Media{PayloadType: 96, Encoding: "AppleLossless", ClockRate: rate, Channels: channels,
		ALAC: &sdp.ALACConfig{FrameLength: frames, BitDepth: 16, PB: 40, MB: 10, KB: 14, Channels: channels, SampleRate: rate}}
	if ct == 4 {
		// AAC-LC, stereo, 1024 samples per access unit. Native AP2 sends
		// raw access units without RFC 3640 AU headers.
		index := 4 // 44.1 kHz
		if rate == 48000 {
			index = 3
		}
		asc := uint16(2<<11 | index<<7 | channels<<3)
		m.Encoding, m.ALAC = "AAC", nil
		m.AAC = &sdp.AACConfig{ASC: []byte{byte(asc >> 8), byte(asc)}}
	}
	format, err := stream.MediaFormat(m)
	if err != nil {
		return err
	}
	sink, err := h.cfg.Output.Open(h.ctx, format)
	if err != nil {
		return err
	}
	h.gain = pcm.NewGain(sink, h.volume)
	var out pcm.Sink = h.gain
	if h.clock != nil {
		h.playout = playout.NewSynced(h.gain, h.clock.FrameLocalTime, ptp.MonotonicNanos)
		out = h.playout
	}
	sess, err := media.NewSession("ap2", m, out)
	if err == nil {
		err = sess.Stream().Announce(m)
	}
	if err != nil {
		sink.Close()
		return err
	}
	sess.SetPacketDecoder(decryptor.Open)
	if integer("type", 0) == ap2StreamBuffered {
		var previousFormat uint32
		var first bool
		var firstFrame uint32
		var frames, lastReport uint64
		sess.SetPacketDecoder(func(packet []byte) ([]byte, error) {
			plain, err := decryptor.OpenBuffered(packet)
			if err != nil {
				return nil, err
			}
			format := binary.BigEndian.Uint32(plain[8:12])
			if !first || format != previousFormat {
				h.log.Debug("AP2 buffered packet format", "format", format, "payloadBytes", len(plain)-12)
				first, previousFormat = true, format
			}
			if format != 0 {
				frame := binary.BigEndian.Uint32(plain[4:8])
				if frames == 0 {
					firstFrame = frame
				}
				frames++
				now := ptp.MonotonicNanos()
				if now-lastReport > 5_000_000_000 {
					lastReport = now
					position := sink.Position()
					var lead int64
					if h.clock != nil {
						if due, ok := h.clock.FrameLocalTime(frame); ok {
							lead = (int64(due) - int64(now)) / 1_000_000
						}
					}
					h.log.Debug("AP2 audio progress", "packets", frames, "rtpSpan", uint32(frame-firstFrame),
						"playedFrames", position.Frames, "queueMs", position.Latency.Milliseconds(), "underruns", position.Underruns, "leadMs", lead)
				}
			}
			return plain, nil
		})
	}
	h.sink, h.sess = h.gain, sess
	if h.clock != nil {
		h.clock.ClearAnchor()
	}
	return nil
}

// ap2TimingSyncCode is the AP2 control-port packet type (code 215) the sender
// posts to announce the playback anchor: which RTP frame plays at which
// grandmaster time. It is an RTP-like datagram whose second byte is the code.
const ap2TimingSyncCode = 215

// serveControl drains the AP2 control UDP socket for the life of the stream
// and feeds timing-sync announcements into the clock anchor. The sender posts
// code-215 anchoring announcements (carrying the RTP frame <-> grandmaster
// time mapping) to this port; rate is the negotiated sample rate used to
// complete the anchor. Other packet types (resent audio, resend requests) are
// ignored by this receiver.
func (h *mediaHandler) serveControl(conn net.PacketConn, rate int) {
	if conn == nil {
		return
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		h.handleControlPacket(buf[:n], rate)
	}
}

// handleControlPacket processes one AP2 control-port datagram. A code-215
// timing-sync packet carries the playback anchor (the RTP frame playing at a
// given grandmaster time) and continuously refreshes the anchor set by
// SETRATEANCHORI, correcting drift. The packet also names the grandmaster
// clock it refers to; a change in that identity is logged as a grandmaster
// change. It is a no-op without a synchronized clock or a positive sample
// rate.
func (h *mediaHandler) handleControlPacket(pkt []byte, rate int) {
	frame, masterNs, clockID, ok := ap2ControlAnchor(pkt)
	if !ok || h.clock == nil || rate <= 0 {
		return
	}
	// Cross-check the anchor's clock against the grandmaster the clock is
	// synchronized to. An anchor for a different clock is stale and must not
	// be adopted; the sender will re-announce against the new master. A zero
	// master identity means no Announce has selected a master yet, so there is
	// nothing to compare against and the anchor is accepted.
	if master := h.clock.Info().MasterID; master != ([8]byte{}) && master != clockID {
		h.log.Warn("AP2 control anchor clock mismatch",
			"anchorClock", fmt.Sprintf("%x", clockID), "masterClock", fmt.Sprintf("%x", master))
		return
	}
	// A changed clock identity means the sender switched grandmasters, so the
	// anchor epoch has changed. Log it; the new anchor simply supersedes the
	// old one, mirroring the reference's "Set Anchor Clock" transition.
	if prev, set := h.clock.Anchor(); set && prev.ClockID != ([8]byte{}) && prev.ClockID != clockID {
		h.log.Info("AP2 control anchor clock change",
			"from", fmt.Sprintf("%x", prev.ClockID), "to", fmt.Sprintf("%x", clockID))
	}
	h.clock.SetAnchor(ptp.Anchor{Frame: frame, MasterNs: masterNs, Rate: rate, ClockID: clockID})
	h.log.Debug("AP2 control anchor", "frame", frame, "masterNs", masterNs, "clockID", fmt.Sprintf("%x", clockID))
}

// ap2ControlAnchor parses a code-215 anchoring announcement and returns the
// RTP frame that plays at the given grandmaster time together with the
// grandmaster clock identity. The packet layout is:
//
//	offset 1        packet type code (215 for a timing-sync announcement)
//	offset 4..8     frame (uint32 BE): the RTP timestamp due to play at the
//	                packet's timestamp; the sender bakes the 77175-frame
//	                stream latency into this value.
//	offset 8..16    masterNs (uint64 BE): the grandmaster time, in
//	                nanoseconds, at which frame plays.
//	offset 16..20   frame2 (uint32 BE): the frame the time refers to; the
//	                reference derives stream_specified_latency = frame2-frame
//	                (normally 77175) from it but anchors on frame directly.
//	offset 20..28   clockID ([8]byte BE): the grandmaster clock identity.
//
// ok is false when the packet is too short or is not a timing-sync packet.
// The reference additionally subtracts a 11025-frame (250 ms at 44.1 kHz)
// output-backend latency fudge factor from frame; gap2's playout scheduler
// buffers independently, so the sender's value is used directly. That fudge
// factor is the first thing to revisit if interop reveals a fixed offset.
func ap2ControlAnchor(pkt []byte) (frame uint32, masterNs uint64, clockID [8]byte, ok bool) {
	if len(pkt) < 28 || pkt[1] != ap2TimingSyncCode {
		return 0, 0, [8]byte{}, false
	}
	frame = binary.BigEndian.Uint32(pkt[4:8])
	masterNs = binary.BigEndian.Uint64(pkt[8:16])
	copy(clockID[:], pkt[20:28])
	return frame, masterNs, clockID, true
}

// localIP returns the local IP of a connection as a string, used to populate
// the receiver's timingPeerInfo.
func localIP(c net.Conn) string {
	if c == nil || c.LocalAddr() == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(c.LocalAddr().String())
	if err != nil {
		return c.LocalAddr().String()
	}
	return host
}

func (h *mediaHandler) record(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")
	if h.sess == nil {
		// Native AP2 senders can RECORD after timing SETUP and add the
		// stream in a later SETUP. Start ingest when that stream arrives.
		if h.eventLn != nil {
			h.recordRequested = true
			return cs.writeRTSPResponse(cseq, 200, "OK", map[string]string{"Audio-Latency": "0"}, nil)
		}
		return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
	}
	if err := h.startMedia(); err != nil {
		return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
	}
	h.recordRequested = true
	return cs.writeRTSPResponse(cseq, 200, "OK", map[string]string{"Audio-Latency": "0"}, nil)
}

func (h *mediaHandler) startMedia() error {
	if h.mediaStarted {
		return nil
	}
	if err := h.sess.Stream().Record(); err != nil {
		return err
	}
	h.mediaStarted = true
	sess := h.sess
	ctx, cancel := context.WithCancel(h.ctx)
	h.mediaCancel = cancel
	go func() {
		if err := sess.Serve(ctx); err != nil {
			h.log.Debug("media serve ended", "err", err)
		}
	}()
	return nil
}

func (h *mediaHandler) teardown(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")
	if v, err := plist.Decode(req.body, plist.DefaultLimits()); err == nil && v.Kind == plist.KindDict {
		if streams, ok := v.Dict["streams"]; ok && streams.Kind == plist.KindArray && len(streams.Array) > 0 {
			h.log.Debug("AP2 audio stream teardown")
			h.closeAudio()
			return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
		}
	}
	h.close()
	cs.media = nil
	return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
}

func (h *mediaHandler) flush(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")
	if req.method == "FLUSHBUFFERED" && h.sess != nil {
		v, err := plist.Decode(req.body, plist.DefaultLimits())
		if err != nil || v.Kind != plist.KindDict {
			return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
		}
		until, ok := plistInt(v, "flushUntilTS")
		if !ok {
			return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
		}
		var from *uint32
		if value, ok := plistInt(v, "flushFromTS"); ok {
			f := uint32(value)
			from = &f
		}
		if seq, ok := plistInt(v, "flushUntilSeq"); ok {
			var fromSeq *uint32
			if value, ok := plistInt(v, "flushFromSeq"); ok {
				f := uint32(value)
				fromSeq = &f
			}
			h.sess.Stream().FlushBufferedRange(uint32(seq), fromSeq)
			h.log.Debug("AP2 flush", "until", uint32(until), "untilSeq", uint32(seq)&0x7fffff, "deferred", fromSeq != nil)
		} else {
			h.sess.Stream().FlushRange(uint32(until), from)
		}
	}
	if h.playout != nil {
		_ = h.playout.Flush(h.ctx)
	} else if h.sink != nil {
		_ = h.sink.Flush(h.ctx)
	}
	return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
}

// setRateAnchor handles SETRATEANCHORI/SETRATEANCHORTI. The body is a plist
// carrying an RTP timestamp and the grandmaster time at which that frame will
// be presented; together with the negotiated sample rate they form the
// playback anchor used to schedule frames against the synchronized clock.
func (h *mediaHandler) setRateAnchor(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")
	if h.sess == nil {
		return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
	}

	v, err := plist.Decode(req.body, plist.DefaultLimits())
	if err != nil || v.Kind != plist.KindDict {
		h.log.Debug("SETRATEANCHORI plist invalid", "err", err)
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}
	// Pausing carries just rate=0, without a network timestamp. Treating
	// missing anchor fields as malformed makes Apple TV disconnect on seeks.
	if rate, ok := plistInt(v, "rate"); ok && rate == 0 {
		if h.playout != nil {
			_ = h.playout.SetPaused(h.ctx, true)
		}
		h.log.Debug("AP2 playback paused")
		return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
	}

	rtpTime, ok := plistInt(v, "rtpTime")
	if !ok {
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}
	secs, ok := plistInt(v, "networkTimeSecs")
	if !ok {
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}
	frac, ok := plistInt(v, "networkTimeFrac")
	if !ok {
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}

	anchor := ptp.Anchor{
		Frame:    uint32(rtpTime),
		MasterNs: ptp.NetworkTimeNanoseconds(uint64(secs), uint64(frac)),
		Rate:     h.sess.Stream().Format().Rate,
	}
	if timeline, ok := plistInt(v, "networkTimeTimelineID"); ok {
		binary.BigEndian.PutUint64(anchor.ClockID[:], uint64(timeline))
	}
	if h.clock != nil {
		h.clock.SetAnchor(anchor)
		h.log.Debug("AP2 playback anchor", "frame", anchor.Frame, "masterNs", anchor.MasterNs, "synced", h.clock.Status().Synced)
	}
	if h.playout != nil {
		_ = h.playout.SetPaused(h.ctx, false)
	}

	return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
}

// plistInt extracts an integer value for key from a plist dictionary.
func plistInt(v *plist.Value, key string) (int64, bool) {
	if v == nil || v.Kind != plist.KindDict {
		return 0, false
	}
	item, ok := v.Dict[key]
	if !ok || item.Kind != plist.KindInt {
		return 0, false
	}
	return item.Int, true
}

// streamID extracts the stream identifier from an RTSP target URL, defaulting
// to the empty string for opaque targets.
func streamID(target string) string {
	u := strings.TrimRight(target, "/")
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return u
}

func reqHeader(req *ctlRequest, name string) string {
	vals := req.headers[strings.ToLower(name)]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
