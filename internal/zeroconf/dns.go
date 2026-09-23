// Package zeroconf implements the small subset of multicast DNS (mDNS) and
// DNS Service Discovery needed to advertise an AirPlay receiver. It is
// dependency-free and CGO-free, and deliberately bounds all packet parsing.
package zeroconf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// DNS record types used by DNS-SD.
const (
	typeA    uint16 = 1
	typePTR  uint16 = 12
	typeTXT  uint16 = 16
	typeAAAA uint16 = 28
	typeSRV  uint16 = 33

	typeAny uint16 = 255
)

// DNS record classes.
const (
	classIN    uint16 = 1
	classAny   uint16 = 255
	classFlush uint16 = 0x8000 // mDNS cache-flush bit
)

// Header flags.
const (
	flagResponse      uint16 = 0x8000
	flagAuthoritative uint16 = 0x0400
)

var (
	errPacketTooSmall  = errors.New("zeroconf: packet too small")
	errNameTooLong     = errors.New("zeroconf: name too long")
	errLabelTooLong    = errors.New("zeroconf: label too long")
	errCompressionLoop = errors.New("zeroconf: compression pointer loop")
	errTruncated       = errors.New("zeroconf: truncated record")
)

// dnsQuestion is one parsed question.
type dnsQuestion struct {
	name  string
	typ   uint16
	class uint16
}

// dnsRecord is one resource record to encode.
type dnsRecord struct {
	name  string
	typ   uint16
	class uint16
	ttl   uint32
	rdata []byte
}

// dnsMessage is a DNS packet.
type dnsMessage struct {
	id         uint16
	flags      uint16
	questions  []dnsQuestion
	answers    []dnsRecord
	additional []dnsRecord
}

func encodeName(w *bytes.Buffer, name string) error {
	if name == "" {
		w.WriteByte(0)
		return nil
	}
	if len(name) > 253 {
		return errNameTooLong
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 {
			return errNameTooLong
		}
		if len(label) > 63 {
			return errLabelTooLong
		}
		w.WriteByte(byte(len(label)))
		w.WriteString(label)
	}
	w.WriteByte(0)
	return nil
}

func (m *dnsMessage) marshal() ([]byte, error) {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.BigEndian, m.id); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.BigEndian, m.flags); err != nil {
		return nil, err
	}
	for _, n := range []uint16{
		uint16(len(m.questions)),
		uint16(len(m.answers)),
		0,
		uint16(len(m.additional)),
	} {
		if err := binary.Write(&buf, binary.BigEndian, n); err != nil {
			return nil, err
		}
	}
	for _, q := range m.questions {
		if err := encodeName(&buf, q.name); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.BigEndian, q.typ); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.BigEndian, q.class); err != nil {
			return nil, err
		}
	}
	for _, section := range [][]dnsRecord{m.answers, m.additional} {
		for _, r := range section {
			if err := encodeRecord(&buf, r); err != nil {
				return nil, err
			}
		}
	}
	return buf.Bytes(), nil
}

func encodeRecord(w *bytes.Buffer, r dnsRecord) error {
	if err := encodeName(w, r.name); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, r.typ); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, r.class); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, r.ttl); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint16(len(r.rdata))); err != nil {
		return err
	}
	_, err := w.Write(r.rdata)
	return err
}

// parseDNS parses a DNS packet's header and questions. Resource record
// sections are not decoded because the responder only consults questions.
func parseDNS(packet []byte) (*dnsMessage, error) {
	if len(packet) < 12 {
		return nil, errPacketTooSmall
	}
	m := &dnsMessage{
		id:    binary.BigEndian.Uint16(packet[0:2]),
		flags: binary.BigEndian.Uint16(packet[2:4]),
	}
	qd := int(binary.BigEndian.Uint16(packet[4:6]))
	if qd > 64 {
		return nil, fmt.Errorf("zeroconf: unreasonable question count %d", qd)
	}

	offset := 12
	for i := 0; i < qd; i++ {
		name, next, err := decodeName(packet, offset, 0)
		if err != nil {
			return nil, err
		}
		offset = next
		if offset+4 > len(packet) {
			return nil, errTruncated
		}
		q := dnsQuestion{
			name:  name,
			typ:   binary.BigEndian.Uint16(packet[offset : offset+2]),
			class: binary.BigEndian.Uint16(packet[offset+2 : offset+4]),
		}
		offset += 4
		m.questions = append(m.questions, q)
	}
	return m, nil
}

// decodeName decodes a possibly compressed DNS name starting at offset.
func decodeName(packet []byte, offset, depth int) (string, int, error) {
	if depth > 16 {
		return "", 0, errCompressionLoop
	}
	var labels []string
	next := offset
	ptrEnd := -1
	for {
		if next >= len(packet) {
			return "", 0, errTruncated
		}
		length := int(packet[next])
		if length == 0 {
			next++
			if ptrEnd < 0 {
				ptrEnd = next
			}
			return strings.Join(labels, "."), ptrEnd, nil
		}
		if length&0xc0 == 0xc0 {
			if next+1 >= len(packet) {
				return "", 0, errTruncated
			}
			pointer := int(binary.BigEndian.Uint16(packet[next:next+2]) & 0x3fff)
			if ptrEnd < 0 {
				ptrEnd = next + 2
			}
			target, _, err := decodeName(packet, pointer, depth+1)
			if err != nil {
				return "", 0, err
			}
			if len(labels) == 0 {
				return target, ptrEnd, nil
			}
			return strings.Join(append(labels, target), "."), ptrEnd, nil
		}
		if length&0xc0 != 0 {
			return "", 0, fmt.Errorf("zeroconf: unsupported name encoding")
		}
		if length > 63 {
			return "", 0, errLabelTooLong
		}
		if next+1+length > len(packet) {
			return "", 0, errTruncated
		}
		labels = append(labels, string(packet[next+1:next+1+length]))
		next += 1 + length
	}
}

func txtRData(entries []string) ([]byte, error) {
	var buf bytes.Buffer
	for _, e := range entries {
		if len(e) == 0 || len(e) > 255 {
			return nil, fmt.Errorf("zeroconf: invalid TXT entry length")
		}
		buf.WriteByte(byte(len(e)))
		buf.WriteString(e)
	}
	return buf.Bytes(), nil
}

func srvRData(port uint16, target string) ([]byte, error) {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.BigEndian, uint16(0)); err != nil { // priority
		return nil, err
	}
	if err := binary.Write(&buf, binary.BigEndian, uint16(0)); err != nil { // weight
		return nil, err
	}
	if err := binary.Write(&buf, binary.BigEndian, port); err != nil {
		return nil, err
	}
	if err := encodeName(&buf, target); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func ipv4RData(ip net.IP) ([]byte, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("zeroconf: not an IPv4 address")
	}
	return append([]byte(nil), ip4...), nil
}
