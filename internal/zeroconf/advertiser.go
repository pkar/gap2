package zeroconf

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

const (
	mdnsTTL   = 120
	maxPacket = 9000
)

// mdnsMulticast is the IPv4 mDNS multicast destination.
var mdnsMulticast = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// Service describes one DNS-SD service instance to advertise.
type Service struct {
	// Type is the service type without the trailing ".local", for example
	// "_airplay._tcp".
	Type string
	// Instance is the human-facing instance label, for example "Living Room".
	Instance string
	// TXT holds the service's TXT records as "key=value" entries.
	TXT []string
}

// Config configures an Advertiser.
type Config struct {
	// Hostname is the DNS host label (no dots) used as the SRV target.
	Hostname string
	// Port is the advertised service port.
	Port int
	// IPv4s are the host addresses published in A records. When empty, the
	// advertiser collects interface addresses.
	IPv4s []net.IP
	// Services lists the DNS-SD services to advertise.
	Services []Service
	// Interfaces restricts sockets to named interfaces. Empty means all
	// up, multicast-capable interfaces.
	Interfaces []string
	// Logger receives diagnostics. Nil means slog.Default.
	Logger *slog.Logger
}

// Advertiser announces DNS-SD services with multicast DNS.
type Advertiser struct {
	cfg  Config
	log  *slog.Logger
	mu   sync.Mutex
	conn []*net.UDPConn
	wg   sync.WaitGroup
}

// New validates cfg and returns an Advertiser without opening sockets.
func New(cfg Config) (*Advertiser, error) {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("zeroconf: invalid port %d", cfg.Port)
	}
	if cfg.Hostname == "" {
		return nil, fmt.Errorf("zeroconf: hostname is empty")
	}
	if len(cfg.Services) == 0 {
		return nil, fmt.Errorf("zeroconf: no services configured")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Advertiser{cfg: cfg, log: cfg.Logger}, nil
}

// Start opens multicast sockets and begins announcing and answering queries.
func (a *Advertiser) Start(ctx context.Context) error {
	ifaces, err := a.selectInterfaces()
	if err != nil {
		return err
	}
	if len(ifaces) == 0 {
		return fmt.Errorf("zeroconf: no multicast interfaces available")
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.conn) > 0 {
		return fmt.Errorf("zeroconf: advertiser already started")
	}

	for _, ifc := range ifaces {
		conn, err := net.ListenMulticastUDP("udp4", ifc, &net.UDPAddr{
			IP:   net.IPv4(224, 0, 0, 251),
			Port: 5353,
		})
		if err != nil {
			a.closeLocked()
			return fmt.Errorf("zeroconf: listen on %s: %w", ifc.Name, err)
		}
		a.conn = append(a.conn, conn)
	}

	if len(a.cfg.IPv4s) == 0 {
		a.cfg.IPv4s = collectIPv4(ifaces)
	}

	a.announceLocked()

	for _, conn := range a.conn {
		conn := conn
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.readLoop(ctx, conn)
		}()
	}
	return nil
}

// Close stops advertising and closes all sockets. It is safe to call more
// than once.
func (a *Advertiser) Close() error {
	a.mu.Lock()
	a.closeLocked()
	a.mu.Unlock()
	a.wg.Wait()
	return nil
}

func (a *Advertiser) closeLocked() {
	for _, c := range a.conn {
		_ = c.Close()
	}
	a.conn = nil
}

func (a *Advertiser) selectInterfaces() ([]*net.Interface, error) {
	if len(a.cfg.Interfaces) > 0 {
		out := make([]*net.Interface, 0, len(a.cfg.Interfaces))
		for _, name := range a.cfg.Interfaces {
			ifc, err := net.InterfaceByName(name)
			if err != nil {
				return nil, fmt.Errorf("zeroconf: interface %s: %w", name, err)
			}
			out = append(out, ifc)
		}
		return out, nil
	}
	all, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("zeroconf: list interfaces: %w", err)
	}
	var out []*net.Interface
	for i := range all {
		if all[i].Flags&net.FlagUp != 0 && all[i].Flags&net.FlagMulticast != 0 {
			out = append(out, &all[i])
		}
	}
	return out, nil
}

func collectIPv4(ifaces []*net.Interface) []net.IP {
	var out []net.IP
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsUnspecified() {
				out = append(out, ip4)
			}
		}
	}
	return out
}

func (a *Advertiser) readLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, maxPacket)
	for {
		n, peer, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			// Socket closed by Close: exit quietly.
			a.mu.Lock()
			closed := len(a.conn) == 0
			a.mu.Unlock()
			if closed {
				return
			}
			a.log.Debug("zeroconf read failed", "err", err)
			continue
		}
		query, err := parseDNS(buf[:n])
		if err != nil {
			continue
		}
		if query.flags&flagResponse != 0 {
			continue // ignore responses and announcements from other hosts
		}
		if len(query.questions) == 0 {
			continue
		}
		resp := a.buildResponse(query)
		if resp == nil || (len(resp.answers) == 0 && len(resp.additional) == 0) {
			continue
		}
		packet, err := resp.marshal()
		if err != nil {
			a.log.Debug("zeroconf marshal failed", "err", err)
			continue
		}
		dst := mdnsMulticast
		if wantsUnicastResponse(query) {
			dst = peer
		}
		if _, err := conn.WriteToUDP(packet, dst); err != nil {
			a.log.Debug("zeroconf write failed", "err", err)
		}
	}
}

func (a *Advertiser) announceLocked() {
	msg := a.announcement()
	if msg == nil {
		return
	}
	packet, err := msg.marshal()
	if err != nil {
		a.log.Debug("zeroconf announce marshal failed", "err", err)
		return
	}
	for _, conn := range a.conn {
		if _, err := conn.WriteToUDP(packet, mdnsMulticast); err != nil {
			a.log.Debug("zeroconf announce failed", "err", err)
		}
	}
}

// wantsUnicastResponse reports whether any question set the mDNS
// unicast-response bit.
func wantsUnicastResponse(q *dnsMessage) bool {
	for _, question := range q.questions {
		if question.class&classFlush != 0 {
			return true
		}
	}
	return false
}

func (a *Advertiser) announcement() *dnsMessage {
	msg := &dnsMessage{flags: flagResponse | flagAuthoritative}
	for _, svc := range a.cfg.Services {
		typeName := svc.Type + ".local"
		instName := svc.Instance + "." + typeName
		rdata, err := nameRData(instName)
		if err != nil {
			continue
		}
		msg.answers = append(msg.answers, dnsRecord{
			name:  typeName,
			typ:   typePTR,
			class: classIN,
			ttl:   mdnsTTL,
			rdata: rdata,
		})
		msg.additional = append(msg.additional, a.serviceRecords(svc, instName)...)
	}
	msg.answers = append(msg.answers, a.addressRecords(a.cfg.Hostname+".local")...)
	return msg
}

func (a *Advertiser) buildResponse(q *dnsMessage) *dnsMessage {
	msg := &dnsMessage{flags: flagResponse | flagAuthoritative}
	hostName := a.cfg.Hostname + ".local"

	for _, question := range q.questions {
		// The top bit of the question class is the mDNS unicast-response bit;
		// mask it before matching the class.
		qclass := question.class &^ classFlush
		if qclass != classIN && qclass != classAny {
			continue
		}
		for _, svc := range a.cfg.Services {
			typeName := svc.Type + ".local"
			instName := svc.Instance + "." + typeName

			switch question.name {
			case typeName:
				if question.typ == typePTR || question.typ == typeAny {
					rdata, err := nameRData(instName)
					if err == nil {
						msg.answers = append(msg.answers, dnsRecord{
							name:  typeName,
							typ:   typePTR,
							class: classIN,
							ttl:   mdnsTTL,
							rdata: rdata,
						})
					}
					msg.additional = append(msg.additional, a.serviceRecords(svc, instName)...)
				}
			case instName:
				msg.answers = append(msg.answers, a.instanceRecords(svc, question.typ, instName, hostName)...)
			case hostName:
				if question.typ == typeA || question.typ == typeAny {
					msg.answers = append(msg.answers, a.addressRecords(hostName)...)
				}
			}
		}
	}
	return msg
}

func (a *Advertiser) serviceRecords(svc Service, instName string) []dnsRecord {
	hostName := a.cfg.Hostname + ".local"
	recs := a.instanceRecords(svc, typeAny, instName, hostName)
	recs = append(recs, a.addressRecords(hostName)...)
	return recs
}

func (a *Advertiser) instanceRecords(svc Service, qtype uint16, instName, hostName string) []dnsRecord {
	var recs []dnsRecord
	if qtype == typeSRV || qtype == typeAny {
		if rdata, err := srvRData(uint16(a.cfg.Port), hostName); err == nil {
			recs = append(recs, dnsRecord{
				name:  instName,
				typ:   typeSRV,
				class: classIN | classFlush,
				ttl:   mdnsTTL,
				rdata: rdata,
			})
		}
	}
	if qtype == typeTXT || qtype == typeAny {
		if rdata, err := txtRData(svc.TXT); err == nil {
			recs = append(recs, dnsRecord{
				name:  instName,
				typ:   typeTXT,
				class: classIN | classFlush,
				ttl:   mdnsTTL,
				rdata: rdata,
			})
		}
	}
	return recs
}

func (a *Advertiser) addressRecords(hostName string) []dnsRecord {
	var recs []dnsRecord
	for _, ip := range a.cfg.IPv4s {
		if rdata, err := ipv4RData(ip); err == nil {
			recs = append(recs, dnsRecord{
				name:  hostName,
				typ:   typeA,
				class: classIN | classFlush,
				ttl:   mdnsTTL,
				rdata: rdata,
			})
		}
	}
	return recs
}

func nameRData(name string) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeName(&buf, name); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
