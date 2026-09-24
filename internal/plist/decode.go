package plist

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

var appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// Decode parses data as either a binary or XML property list, enforcing
// limits. It never decodes an arbitrary XML document: the root element must be
// plist and only plist element types are accepted.
func Decode(data []byte, limits Limits) (*Value, error) {
	limits = limits.withDefaults()
	if int64(len(data)) > limits.MaxBytes {
		return nil, ErrTooLarge
	}
	if len(data) == 0 {
		return nil, ErrMalformed
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		return decodeBinary(data, limits)
	}
	return decodeXML(data, limits)
}

// --- binary plist ---

type binaryDecoder struct {
	data          []byte
	offsets       []uint64
	objRefSize    int
	offsetIntSize int
	numObjects    uint64
	limits        Limits
	objectsRead   int
}

func decodeBinary(data []byte, limits Limits) (*Value, error) {
	if len(data) < 40 || !bytes.Equal(data[:6], []byte("bplist")) {
		return nil, ErrMalformed
	}
	t := data[len(data)-32:]
	offsetIntSize := int(t[6])
	objRefSize := int(t[7])
	numObjects := binary.BigEndian.Uint64(t[8:16])
	topObject := binary.BigEndian.Uint64(t[16:24])
	offsetTable := binary.BigEndian.Uint64(t[24:32])

	if offsetIntSize < 1 || offsetIntSize > 8 || objRefSize < 1 || objRefSize > 8 {
		return nil, ErrMalformed
	}
	if numObjects > uint64(limits.MaxObjects) {
		return nil, ErrTooManyObjects
	}
	if topObject >= numObjects {
		return nil, ErrMalformed
	}

	tableEnd := offsetTable + numObjects*uint64(offsetIntSize)
	if tableEnd > uint64(len(data)-32) {
		return nil, ErrMalformed
	}

	d := &binaryDecoder{
		data:          data,
		offsets:       make([]uint64, numObjects),
		objRefSize:    objRefSize,
		offsetIntSize: offsetIntSize,
		numObjects:    numObjects,
		limits:        limits,
	}
	for i := uint64(0); i < numObjects; i++ {
		off := readUint(data, offsetTable+i*uint64(offsetIntSize), offsetIntSize)
		if off >= uint64(len(data)-32) {
			return nil, ErrMalformed
		}
		d.offsets[i] = off
	}
	return d.readObject(topObject, 0)
}

func (d *binaryDecoder) readObject(ref uint64, depth int) (*Value, error) {
	if depth > d.limits.MaxDepth {
		return nil, ErrTooDeep
	}
	d.objectsRead++
	if d.objectsRead > d.limits.MaxObjects {
		return nil, ErrTooManyObjects
	}
	if ref >= uint64(len(d.offsets)) {
		return nil, ErrMalformed
	}
	off := d.offsets[ref]
	if off >= uint64(len(d.data)) {
		return nil, ErrMalformed
	}

	marker := d.data[off]
	typ := marker >> 4
	info := marker & 0xF
	pos := off + 1

	switch typ {
	case 0x0: // null, bool, fill
		switch info {
		case 0x0:
			return &Value{Kind: KindBool, Bool: false}, nil
		case 0x8:
			return &Value{Kind: KindBool, Bool: false}, nil
		case 0x9:
			return &Value{Kind: KindBool, Bool: true}, nil
		case 0xF:
			return nil, nil // fill object; not reachable via top object normally
		default:
			return nil, ErrMalformed
		}
	case 0x1: // integer
		v, err := d.readInt(pos, info)
		if err != nil {
			return nil, err
		}
		return &Value{Kind: KindInt, Int: v}, nil
	case 0x2: // real
		switch info {
		case 2:
			if pos+4 > uint64(len(d.data)) {
				return nil, ErrMalformed
			}
			bits := binary.BigEndian.Uint32(d.data[pos : pos+4])
			return &Value{Kind: KindReal, Real: float64(math.Float32frombits(bits))}, nil
		case 3:
			if pos+8 > uint64(len(d.data)) {
				return nil, ErrMalformed
			}
			bits := binary.BigEndian.Uint64(d.data[pos : pos+8])
			return &Value{Kind: KindReal, Real: math.Float64frombits(bits)}, nil
		default:
			return nil, ErrMalformed
		}
	case 0x3: // date
		if info != 3 || pos+8 > uint64(len(d.data)) {
			return nil, ErrMalformed
		}
		bits := binary.BigEndian.Uint64(d.data[pos : pos+8])
		secs := math.Float64frombits(bits)
		return &Value{Kind: KindDate, Date: appleEpoch.Add(time.Duration(secs * float64(time.Second)))}, nil
	case 0x4: // data
		n, valPos, err := d.readLength(pos, info)
		if err != nil {
			return nil, err
		}
		if n > uint64(d.limits.MaxDataLen) {
			return nil, ErrTooLarge
		}
		if err := d.check(valPos, n); err != nil {
			return nil, err
		}
		b := append([]byte(nil), d.data[valPos:valPos+n]...)
		return &Value{Kind: KindData, Data: b}, nil
	case 0x5: // ASCII string
		n, valPos, err := d.readLength(pos, info)
		if err != nil {
			return nil, err
		}
		if n > uint64(d.limits.MaxStringLen) {
			return nil, ErrTooLarge
		}
		if err := d.check(valPos, n); err != nil {
			return nil, err
		}
		s := string(d.data[valPos : valPos+n])
		return &Value{Kind: KindString, String: s}, nil
	case 0x6: // UTF-16BE string
		n, valPos, err := d.readLength(pos, info)
		if err != nil {
			return nil, err
		}
		if n%2 != 0 || n > uint64(d.limits.MaxStringLen)*2 {
			return nil, ErrMalformed
		}
		if err := d.check(valPos, n); err != nil {
			return nil, err
		}
		units := make([]uint16, n/2)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(d.data[valPos+uint64(i)*2 : valPos+uint64(i)*2+2])
		}
		return &Value{Kind: KindString, String: string(utf16.Decode(units))}, nil
	case 0x8: // UID
		n, valPos, err := d.readLength(pos, info)
		if err != nil {
			return nil, err
		}
		if n > 8 {
			return nil, ErrMalformed
		}
		if err := d.check(valPos, n); err != nil {
			return nil, err
		}
		return &Value{Kind: KindInt, Int: int64(readUint(d.data, valPos, int(n)))}, nil
	case 0xA: // array
		n, refsPos, err := d.readLength(pos, info)
		if err != nil {
			return nil, err
		}
		if n > uint64(d.limits.MaxObjects) {
			return nil, ErrTooManyObjects
		}
		if err := d.check(refsPos, n*uint64(d.objRefSize)); err != nil {
			return nil, err
		}
		arr := make([]Value, n)
		for i := uint64(0); i < n; i++ {
			ref := readUint(d.data, refsPos+i*uint64(d.objRefSize), d.objRefSize)
			child, err := d.readObject(ref, depth+1)
			if err != nil {
				return nil, err
			}
			if child == nil {
				return nil, ErrMalformed
			}
			arr[i] = *child
		}
		return &Value{Kind: KindArray, Array: arr}, nil
	case 0xD: // dict
		n, refsPos, err := d.readLength(pos, info)
		if err != nil {
			return nil, err
		}
		if n > uint64(d.limits.MaxObjects) {
			return nil, ErrTooManyObjects
		}
		refBytes := n * uint64(d.objRefSize)
		if err := d.check(refsPos, refBytes*2); err != nil {
			return nil, err
		}
		keys := make([]uint64, n)
		for i := uint64(0); i < n; i++ {
			keys[i] = readUint(d.data, refsPos+i*uint64(d.objRefSize), d.objRefSize)
		}
		valsPos := refsPos + refBytes
		vals := make([]uint64, n)
		for i := uint64(0); i < n; i++ {
			vals[i] = readUint(d.data, valsPos+i*uint64(d.objRefSize), d.objRefSize)
		}

		dict := make(map[string]Value, n)
		for i := uint64(0); i < n; i++ {
			k, err := d.readObject(keys[i], depth+1)
			if err != nil {
				return nil, err
			}
			if k == nil || k.Kind != KindString {
				return nil, ErrMalformed
			}
			if len(k.String) > d.limits.MaxKeyLen {
				return nil, ErrTooLarge
			}
			v, err := d.readObject(vals[i], depth+1)
			if err != nil {
				return nil, err
			}
			if v == nil {
				return nil, ErrMalformed
			}
			dict[k.String] = *v
		}
		return &Value{Kind: KindDict, Dict: dict}, nil
	default:
		return nil, ErrMalformed
	}
}

func (d *binaryDecoder) check(pos, n uint64) error {
	if pos+n > uint64(len(d.data)-32) || pos+n < pos {
		return ErrMalformed
	}
	return nil
}

func (d *binaryDecoder) readInt(pos uint64, info byte) (int64, error) {
	if info > 4 {
		return 0, ErrMalformed
	}
	size := 1 << info
	if size > 8 {
		return 0, ErrMalformed
	}
	if err := d.check(pos, uint64(size)); err != nil {
		return 0, err
	}
	u := readUint(d.data, pos, size)
	// Binary plists use unsigned compact integers. Negative integers are
	// always encoded in eight bytes; only that width has a sign bit.
	return int64(u), nil
}

func (d *binaryDecoder) readLength(pos uint64, info byte) (uint64, uint64, error) {
	if info != 0xF {
		return uint64(info), pos, nil
	}
	// A length of 0xF is followed by an integer object holding the real count.
	if pos >= uint64(len(d.data)) {
		return 0, 0, ErrMalformed
	}
	marker := d.data[pos]
	if marker>>4 != 0x1 {
		return 0, 0, ErrMalformed
	}
	ninfo := marker & 0xF
	if ninfo > 3 {
		return 0, 0, ErrMalformed
	}
	size := 1 << ninfo
	if err := d.check(pos+1, uint64(size)); err != nil {
		return 0, 0, err
	}
	return readUint(d.data, pos+1, size), pos + 1 + uint64(size), nil
}

func readUint(data []byte, pos uint64, size int) uint64 {
	var u uint64
	for i := 0; i < size; i++ {
		u = u<<8 | uint64(data[pos+uint64(i)])
	}
	return u
}

// --- XML plist ---

type xmlDecoder struct {
	dec         *xml.Decoder
	limits      Limits
	objectsRead int
}

func decodeXML(data []byte, limits Limits) (*Value, error) {
	d := &xmlDecoder{dec: xml.NewDecoder(bytes.NewReader(data)), limits: limits}
	d.dec.Strict = false
	for {
		tok, err := d.dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil, ErrMalformed
			}
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Local != "plist" {
				return nil, fmt.Errorf("plist: unexpected root <%s>", se.Name.Local)
			}
			return d.nextValue(1)
		}
	}
}

func (d *xmlDecoder) parseElement(se xml.StartElement, depth int) (*Value, error) {
	if depth > d.limits.MaxDepth {
		return nil, ErrTooDeep
	}
	d.objectsRead++
	if d.objectsRead > d.limits.MaxObjects {
		return nil, ErrTooManyObjects
	}

	switch se.Name.Local {
	case "dict":
		v := &Value{Kind: KindDict, Dict: make(map[string]Value)}
		for {
			tok, err := d.dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				if t.Name.Local == "dict" {
					return v, nil
				}
				return nil, ErrMalformed
			case xml.StartElement:
				if t.Name.Local != "key" {
					return nil, fmt.Errorf("plist: expected <key>, got <%s>", t.Name.Local)
				}
				key, err := d.readText(t)
				if err != nil {
					return nil, err
				}
				if len(key) > d.limits.MaxKeyLen {
					return nil, ErrTooLarge
				}
				child, err := d.nextValue(depth + 1)
				if err != nil {
					return nil, err
				}
				v.Dict[key] = *child
			default:
				// Ignore character data between elements.
			}
		}
	case "array":
		var arr []Value
		for {
			tok, err := d.dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				if t.Name.Local == "array" {
					return &Value{Kind: KindArray, Array: arr}, nil
				}
				return nil, ErrMalformed
			case xml.StartElement:
				child, err := d.parseElement(t, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, *child)
			default:
			}
		}
	case "string":
		s, err := d.readText(se)
		if err != nil {
			return nil, err
		}
		if len(s) > d.limits.MaxStringLen {
			return nil, ErrTooLarge
		}
		return &Value{Kind: KindString, String: s}, nil
	case "integer":
		s, err := d.readText(se)
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("plist: bad integer %q: %w", s, err)
		}
		return &Value{Kind: KindInt, Int: n}, nil
	case "real":
		s, err := d.readText(se)
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("plist: bad real %q: %w", s, err)
		}
		return &Value{Kind: KindReal, Real: f}, nil
	case "true":
		if _, err := d.readText(se); err != nil {
			return nil, err
		}
		return &Value{Kind: KindBool, Bool: true}, nil
	case "false":
		if _, err := d.readText(se); err != nil {
			return nil, err
		}
		return &Value{Kind: KindBool, Bool: false}, nil
	case "data":
		s, err := d.readText(se)
		if err != nil {
			return nil, err
		}
		b, err := base64Decode(s)
		if err != nil {
			return nil, fmt.Errorf("plist: bad data: %w", err)
		}
		if len(b) > d.limits.MaxDataLen {
			return nil, ErrTooLarge
		}
		return &Value{Kind: KindData, Data: b}, nil
	case "date":
		s, err := d.readText(se)
		if err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("plist: bad date %q: %w", s, err)
		}
		return &Value{Kind: KindDate, Date: t.UTC()}, nil
	default:
		return nil, fmt.Errorf("plist: unknown element <%s>", se.Name.Local)
	}
}

func (d *xmlDecoder) nextValue(depth int) (*Value, error) {
	for {
		tok, err := d.dec.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return d.parseElement(se, depth)
		}
		if _, ok := tok.(xml.EndElement); ok {
			return nil, ErrMalformed
		}
	}
}

func (d *xmlDecoder) readText(se xml.StartElement) (string, error) {
	var b strings.Builder
	for {
		tok, err := d.dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			if t.Name.Local != se.Name.Local {
				return "", ErrMalformed
			}
			return b.String(), nil
		case xml.StartElement:
			return "", fmt.Errorf("plist: unexpected nested <%s> in <%s>", t.Name.Local, se.Name.Local)
		}
	}
}

func base64Decode(s string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, s)
	return base64.StdEncoding.DecodeString(clean)
}
