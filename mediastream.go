package airplay2

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pkar/gap2/internal/media"
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

	sess *media.Session
	sink pcm.Sink

	// bind is overridable in tests to avoid binding a real socket.
	bind func(network, addr string) (int, error)
}

func newMediaHandler(cfg Config, log *slog.Logger) *mediaHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &mediaHandler{cfg: cfg, log: log, ctx: ctx, cancel: cancel}
}

// close cancels any active serve loop and releases the receiver and sink. It
// is idempotent and safe to call after Teardown.
func (h *mediaHandler) close() {
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	if h.sess != nil {
		_ = h.sess.Close()
	}
	if h.sink != nil {
		_ = h.sink.Close()
	}
}

// handleMedia dispatches one RTSP media request on an encrypted control
// connection.
func (s *controlServer) handleMedia(cs *connState, req *ctlRequest) error {
	if cs.media == nil {
		cs.media = newMediaHandler(s.cfg, s.log)
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
	case "GET_PARAMETER", "SET_PARAMETER", "SETRATEANCHORTIME", "SETPEERS", "SETPEERSX":
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
	sink, err := h.cfg.Output.Open(h.ctx, format)
	if err != nil {
		h.log.Debug("media sink open failed", "err", err)
		return cs.writeRTSPResponse(cseq, 500, "Internal Server Error", nil, nil)
	}
	h.sink = sink

	sess, err := media.NewSession(streamID(req.target), m, sink)
	if err != nil {
		return cs.writeRTSPResponse(cseq, 415, "Unsupported Media Type", nil, nil)
	}
	if err := sess.Stream().Announce(m); err != nil {
		return cs.writeRTSPResponse(cseq, 400, "Bad Request", nil, nil)
	}
	h.sess = sess

	return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
}

func (h *mediaHandler) setup(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")
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

func (h *mediaHandler) record(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")
	if h.sess == nil {
		return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
	}
	if err := h.sess.Stream().Record(); err != nil {
		return cs.writeRTSPResponse(cseq, 455, "Method Not Valid in This State", nil, nil)
	}
	sess := h.sess
	go func() {
		if err := sess.Serve(h.ctx); err != nil {
			h.log.Debug("media serve ended", "err", err)
		}
	}()
	return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
}

func (h *mediaHandler) teardown(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")
	if h.sess != nil {
		_ = h.sess.Stream().Teardown()
	}
	h.close()
	h.sess = nil
	h.sink = nil
	return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
}

func (h *mediaHandler) flush(cs *connState, req *ctlRequest) error {
	cseq := reqHeader(req, "CSeq")
	if h.sink != nil {
		_ = h.sink.Flush(h.ctx)
	}
	return cs.writeRTSPResponse(cseq, 200, "OK", nil, nil)
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
