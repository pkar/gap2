package zeroconf

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func newTestAdvertiser(t *testing.T) *Advertiser {
	t.Helper()
	a, err := New(Config{
		Hostname: "001122334455",
		Port:     7000,
		IPv4s:    []net.IP{net.ParseIP("192.168.1.10").To4()},
		Services: []Service{{
			Type:     "_airplay._tcp",
			Instance: "Receiver",
			TXT:      []string{"txtvers=1", "deviceid=AA:BB:CC:DD:EE:FF"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEncodeDecodeName(t *testing.T) {
	for _, name := range []string{"", "local", "_airplay._tcp.local", "001122334455.local"} {
		var buf bytes.Buffer
		if err := encodeName(&buf, name); err != nil {
			t.Fatalf("encodeName(%q): %v", name, err)
		}
		got, next, err := decodeName(buf.Bytes(), 0, 0)
		if err != nil {
			t.Fatalf("decodeName(%q): %v", name, err)
		}
		if got != name {
			t.Fatalf("decodeName(%q) = %q", name, got)
		}
		if next != buf.Len() {
			t.Fatalf("decodeName(%q) next = %d, want %d", name, next, buf.Len())
		}
	}
}

func TestParseCompressedName(t *testing.T) {
	var buf bytes.Buffer
	// Header: ID, flags, QDCOUNT, ANCOUNT, NSCOUNT, ARCOUNT.
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(2)) // two questions
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(0))

	if err := encodeName(&buf, "_airplay._tcp.local"); err != nil {
		t.Fatal(err)
	}
	binary.Write(&buf, binary.BigEndian, typePTR)
	binary.Write(&buf, binary.BigEndian, classIN)

	// Second question reuses the first name via a compression pointer to
	// offset 12 (the start of the first question name).
	buf.WriteByte(0xc0)
	buf.WriteByte(12)
	binary.Write(&buf, binary.BigEndian, typePTR)
	binary.Write(&buf, binary.BigEndian, classIN)

	msg, err := parseDNS(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(msg.questions))
	}
	for i, q := range msg.questions {
		if q.name != "_airplay._tcp.local" {
			t.Fatalf("question %d name = %q", i, q.name)
		}
	}
}

func TestBuildResponsePTR(t *testing.T) {
	a := newTestAdvertiser(t)
	q := &dnsMessage{questions: []dnsQuestion{{name: "_airplay._tcp.local", typ: typePTR, class: classIN}}}
	qp, err := q.marshal()
	if err != nil {
		t.Fatal(err)
	}
	query, err := parseDNS(qp)
	if err != nil {
		t.Fatal(err)
	}
	resp := a.buildResponse(query)

	if len(resp.answers) == 0 || resp.answers[0].typ != typePTR {
		t.Fatalf("answers = %+v, want PTR", resp.answers)
	}
	target, _, err := decodeName(resp.answers[0].rdata, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if target != "Receiver._airplay._tcp.local" {
		t.Fatalf("PTR target = %q", target)
	}

	var srv, txt, aRec bool
	for _, r := range resp.additional {
		switch r.typ {
		case typeSRV:
			srv = true
		case typeTXT:
			txt = true
		case typeA:
			aRec = true
		}
	}
	if !srv || !txt || !aRec {
		t.Fatalf("additionals missing records: srv=%v txt=%v a=%v", srv, txt, aRec)
	}
}

func TestWantsUnicastResponse(t *testing.T) {
	q := &dnsMessage{questions: []dnsQuestion{
		{name: "_airplay._tcp.local", typ: typePTR, class: classIN},
		{name: "_raop._tcp.local", typ: typePTR, class: classIN | classFlush},
	}}
	if !wantsUnicastResponse(q) {
		t.Fatal("wantsUnicastResponse = false, want true")
	}

	a := newTestAdvertiser(t)
	resp := a.buildResponse(q)
	if len(resp.answers) == 0 {
		t.Fatal("buildResponse dropped unicast-response query")
	}
}

func TestBuildResponseInstanceAndHost(t *testing.T) {
	a := newTestAdvertiser(t)

	t.Run("SRV", func(t *testing.T) {
		q := &dnsMessage{questions: []dnsQuestion{{name: "Receiver._airplay._tcp.local", typ: typeSRV, class: classIN}}}
		resp := a.buildResponse(q)
		if len(resp.answers) != 1 || resp.answers[0].typ != typeSRV {
			t.Fatalf("answers = %+v, want one SRV", resp.answers)
		}
		// SRV rdata: priority(2) weight(2) port(2) target(name).
		port := binary.BigEndian.Uint16(resp.answers[0].rdata[4:6])
		if port != 7000 {
			t.Fatalf("SRV port = %d", port)
		}
		target, _, err := decodeName(resp.answers[0].rdata, 6, 0)
		if err != nil {
			t.Fatal(err)
		}
		if target != "001122334455.local" {
			t.Fatalf("SRV target = %q", target)
		}
	})

	t.Run("TXT", func(t *testing.T) {
		q := &dnsMessage{questions: []dnsQuestion{{name: "Receiver._airplay._tcp.local", typ: typeTXT, class: classIN}}}
		resp := a.buildResponse(q)
		if len(resp.answers) != 1 || resp.answers[0].typ != typeTXT {
			t.Fatalf("answers = %+v, want one TXT", resp.answers)
		}
		if !bytes.Contains(resp.answers[0].rdata, []byte("deviceid=AA:BB:CC:DD:EE:FF")) {
			t.Fatalf("TXT rdata = %q", resp.answers[0].rdata)
		}
	})

	t.Run("A", func(t *testing.T) {
		q := &dnsMessage{questions: []dnsQuestion{{name: "001122334455.local", typ: typeA, class: classIN}}}
		resp := a.buildResponse(q)
		if len(resp.answers) != 1 || resp.answers[0].typ != typeA {
			t.Fatalf("answers = %+v, want one A", resp.answers)
		}
		if got := net.IP(resp.answers[0].rdata).String(); got != "192.168.1.10" {
			t.Fatalf("A address = %s", got)
		}
	})
}
