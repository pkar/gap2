package airplay2

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkar/gap2/internal/hap"
	"github.com/pkar/gap2/internal/ptp"
	"github.com/pkar/gap2/internal/zeroconf"
)

// Status is a stable snapshot of a Receiver.
type Status struct {
	State         State
	StartedAt     time.Time
	SessionCount  int
	DroppedEvents int64
	// PTP reports the synchronized clock state. Synced and Anchored are false
	// until PTP messages and a SETRATEANCHORI request have been received.
	PTP ptp.Status
}

// Receiver owns a single AirPlay 2 receiver instance.
//
// A Receiver is created with New and, for now, used for at most one Run call.
// The public contract keeps construction side-effect-free: no networking,
// file, or goroutine work happens until Run is called.
type Receiver struct {
	cfg Config
	log *slog.Logger

	events chan Event

	runOnce        atomic.Bool
	eventsCloseOne sync.Once

	mu           sync.Mutex
	state        State
	startedAt    time.Time
	sessionCount int
	closed       bool
	eventsClosed bool
	ln           net.Listener
	adv          *zeroconf.Advertiser

	// ptpClock is the synchronized clock shared by the PTP listener and the
	// media handlers. ptpGroup owns the live PTP sockets; ptpWG tracks the
	// serve goroutine so shutdown can join it.
	ptpClock *ptp.Clock
	ptpGroup *ptp.Group
	ptpWG    sync.WaitGroup

	droppedEvents atomic.Int64
}

// New validates cfg and returns a Receiver without performing I/O or starting
// goroutines.
func New(cfg Config) (*Receiver, error) {
	cfg = cfg.normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Receiver{
		cfg:      cfg,
		log:      cfg.Logger,
		events:   make(chan Event, 64),
		state:    StateIdle,
		ptpClock: ptp.NewClock(),
	}, nil
}

// Run starts the receiver and blocks until ctx is cancelled, Close is called,
// or an unrecoverable error occurs. A nil return value means clean shutdown.
func (r *Receiver) Run(ctx context.Context) (err error) {
	if r.runOnce.Swap(true) {
		return ErrAlreadyRunning
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	r.setStateLocked(StateStarting)
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		ln := r.ln
		adv := r.adv
		ptpGroup := r.ptpGroup
		r.ln = nil
		r.adv = nil
		r.ptpGroup = nil
		r.mu.Unlock()
		if ln != nil {
			_ = ln.Close()
		}
		if adv != nil {
			_ = adv.Close()
		}
		if ptpGroup != nil {
			_ = ptpGroup.Close()
		}
		r.ptpWG.Wait()
		if err != nil {
			r.mu.Lock()
			r.setStateLocked(StateFailed)
			r.mu.Unlock()
			r.closeEvents()
			return
		}
		r.finish(StateStopped)
	}()

	store, err := loadPairingStore(r.cfg.PairingsPath)
	if err != nil {
		return err
	}
	identity, err := store.ensureIdentity(nil)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", r.cfg.ListenAddr)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.ln = ln
	r.mu.Unlock()

	s := newControlServer(r.cfg, identity, store, r.ptpClock)

	r.startPTP(ctx)

	adv, derr := startDiscovery(ctx, r.cfg, ln.Addr(), identity, store)
	r.mu.Lock()
	if derr == nil {
		r.adv = adv
	} else {
		// Discovery is best-effort: a receiver can still serve control and
		// pairing over a directly-reachable address when multicast is
		// unavailable (for example, in containers or on hosts that already
		// own the mDNS port).
		r.log.Warn("mDNS discovery unavailable", "err", derr)
	}
	r.setStateLocked(StateRunning)
	r.mu.Unlock()

	stopAfter := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stopAfter()

	if err := s.serve(ctx, ln); err != nil {
		return err
	}
	return nil
}

// Events returns the receiver's notification channel. It is closed when the
// receiver stops. Slow subscribers never block protocol or audio workers;
// dropped events are counted in Status.
func (r *Receiver) Events() <-chan Event {
	return r.events
}

// startPTP joins the PTP multicast group and serves it into r.ptpClock. PTP
// is best-effort: when multicast is unavailable the receiver still plays audio
// but cannot synchronize its clock, which is logged rather than failing Run.
func (r *Receiver) startPTP(ctx context.Context) {
	group, err := ptp.ListenGroup(r.cfg.Interfaces, r.ptpClock)
	if err != nil {
		r.log.Warn("PTP listener unavailable", "err", err)
		return
	}
	r.mu.Lock()
	r.ptpGroup = group
	r.mu.Unlock()
	r.ptpWG.Add(1)
	go func() {
		defer r.ptpWG.Done()
		if err := group.Serve(ctx); err != nil {
			r.log.Debug("PTP serve ended", "err", err)
		}
	}()
}

// Addr returns the bound control address. It is nil before Run binds the
// listener and after shutdown.
func (r *Receiver) Addr() net.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln == nil {
		return nil
	}
	return r.ln.Addr()
}

// Status returns a stable snapshot of the receiver.
func (r *Receiver) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{
		State:         r.state,
		StartedAt:     r.startedAt,
		SessionCount:  r.sessionCount,
		DroppedEvents: r.droppedEvents.Load(),
	}
	if r.ptpClock != nil {
		st.PTP = r.ptpClock.Status()
	}
	return st
}

// Close shuts the receiver down and is idempotent. It closes the listener
// backing an active Run, causing Run to return nil. Callers may also cancel
// the context passed to Run.
func (r *Receiver) Close() error {
	r.mu.Lock()
	var ln net.Listener
	var adv *zeroconf.Advertiser
	var ptpGroup *ptp.Group
	if !r.closed {
		r.closed = true
		ln = r.ln
		adv = r.adv
		ptpGroup = r.ptpGroup
		if r.state == StateIdle || r.state == StateStarting {
			r.setStateLocked(StateStopped)
		}
	}
	r.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	if adv != nil {
		_ = adv.Close()
	}
	if ptpGroup != nil {
		_ = ptpGroup.Close()
	}
	r.ptpWG.Wait()

	r.closeEvents()
	return nil
}

func (r *Receiver) setStateLocked(s State) {
	if r.state == s {
		return
	}
	r.state = s
	if s == StateStarting {
		r.startedAt = time.Now()
	}
	r.emitLocked(Event{Type: EventStateChanged, State: s, At: time.Now()})
}

func (r *Receiver) emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitLocked(e)
}

func (r *Receiver) emitLocked(e Event) {
	if r.eventsClosed {
		return
	}
	select {
	case r.events <- e:
	default:
		r.droppedEvents.Add(1)
	}
}

func (r *Receiver) finish(s State) {
	r.mu.Lock()
	r.setStateLocked(s)
	r.mu.Unlock()
	r.closeEvents()
}

func (r *Receiver) closeEvents() {
	r.eventsCloseOne.Do(func() {
		r.mu.Lock()
		r.eventsClosed = true
		r.mu.Unlock()
		close(r.events)
	})
}

// startDiscovery opens and announces the mDNS advertiser for the receiver's
// AirPlay and RAOP service instances. A non-nil error means the receiver runs
// without discovery; callers decide whether that is acceptable.
func startDiscovery(ctx context.Context, cfg Config, addr net.Addr, identity hap.Identity, store *pairingStore) (*zeroconf.Advertiser, error) {
	port, err := portFromAddr(addr)
	if err != nil {
		return nil, err
	}
	mac := store.deviceID()
	hostname := strings.ToLower(strings.ReplaceAll(mac, ":", ""))
	if hostname == "" {
		hostname = "airplay"
	}

	services := []zeroconf.Service{
		{
			Type:     "_airplay._tcp",
			Instance: cfg.Name,
			TXT:      airplayTXT(mac, identity),
		},
		{
			Type:     "_raop._tcp",
			Instance: hostname + "@" + cfg.Name,
			TXT:      raopTXT(identity),
		},
	}

	adv, err := zeroconf.New(zeroconf.Config{
		Hostname:   hostname,
		Port:       port,
		Services:   services,
		Interfaces: cfg.Interfaces,
		Logger:     cfg.Logger,
	})
	if err != nil {
		return nil, err
	}
	if err := adv.Start(ctx); err != nil {
		return nil, err
	}
	return adv, nil
}

func portFromAddr(addr net.Addr) (int, error) {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0, fmt.Errorf("airplay2: parse listen port: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0, fmt.Errorf("airplay2: parse listen port: %w", err)
	}
	return n, nil
}

// airplayTXT builds the DNS-SD TXT record set for the _airplay._tcp instance.
func airplayTXT(mac string, identity hap.Identity) []string {
	return []string{
		"txtvers=1",
		"deviceid=" + mac,
		fmt.Sprintf("features=0x%X,0x%X", uint32(airplayFeatures&0xffffffff), airplayFeatures>>32),
		"flags=0x4",
		"model=AppleTV6,2",
		"pk=" + hex.EncodeToString(identity.PublicKey()),
		"pi=" + string(identity.ID),
		"psi=" + string(identity.ID),
		"protovers=1.1",
		"srcvers=366.0",
		"vv=1",
	}
}

// raopTXT builds the matching AirPlay 2 _raop._tcp record. Its advertised
// features, status flags, and long-term key must agree with _airplay._tcp;
// otherwise Music can discover the receiver but reject the output route.
func raopTXT(identity hap.Identity) []string {
	return []string{
		"txtvers=1",
		"ch=2",
		"cn=0,1,2,3",
		// Only unencrypted RAOP audio is implemented. Types 3 and 5 are
		// FairPlay SAP and require /fp-setup and media-key decryption.
		"et=0",
		"da=true",
		"tp=UDP",
		"md=0,1,2",
		"am=AppleTV6,2",
		fmt.Sprintf("ft=0x%X,0x%X", uint32(airplayFeatures&0xffffffff), airplayFeatures>>32),
		"sf=0x4",
		"pk=" + hex.EncodeToString(identity.PublicKey()),
		"vn=65537",
		"vs=366.0",
		"vv=1",
	}
}
