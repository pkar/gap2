package plist

import (
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf16"
)

// Encode serializes v as a binary property list. Dictionary keys are emitted
// in sorted order for deterministic output.
func Encode(v *Value) ([]byte, error) {
	if v == nil {
		return nil, errors.New("plist: nil value")
	}
	limits := DefaultLimits()
	if err := validateValue(v, 0, limits); err != nil {
		return nil, err
	}
	objCount, err := countNodes(v, 0, limits)
	if err != nil {
		return nil, err
	}
	if objCount == 0 {
		return nil, errors.New("plist: empty value")
	}
	if objCount > limits.MaxObjects {
		return nil, ErrTooManyObjects
	}

	e := &binEncoder{
		objRefSize: bytesFor(objCount - 1),
	}
	rootRef, err := e.add(v, 0)
	if err != nil {
		return nil, err
	}
	if rootRef != objCount-1 {
		return nil, errors.New("plist: internal object index mismatch")
	}

	header := []byte("bplist00")
	bodyLen := 0
	for _, obj := range e.objects {
		bodyLen += len(obj)
	}
	offsetIntSize := bytesFor(bodyLen)

	buf := make([]byte, 0, len(header)+bodyLen+objCount*offsetIntSize+32)
	buf = append(buf, header...)

	offsetTable := make([]byte, 0, objCount*offsetIntSize)
	pos := len(header)
	for _, obj := range e.objects {
		for i := offsetIntSize - 1; i >= 0; i-- {
			offsetTable = append(offsetTable, byte(pos>>(8*i)))
		}
		pos += len(obj)
	}
	for _, obj := range e.objects {
		buf = append(buf, obj...)
	}
	offsetTableStart := len(buf)
	buf = append(buf, offsetTable...)

	trailer := make([]byte, 32)
	trailer[6] = byte(offsetIntSize)
	trailer[7] = byte(e.objRefSize)
	binary.BigEndian.PutUint64(trailer[8:16], uint64(objCount))
	binary.BigEndian.PutUint64(trailer[16:24], uint64(rootRef))
	binary.BigEndian.PutUint64(trailer[24:32], uint64(offsetTableStart))
	buf = append(buf, trailer...)
	return buf, nil
}

func validateValue(v *Value, depth int, limits Limits) error {
	if depth > limits.MaxDepth {
		return ErrTooDeep
	}
	switch v.Kind {
	case KindArray:
		for i := range v.Array {
			if err := validateValue(&v.Array[i], depth+1, limits); err != nil {
				return err
			}
		}
	case KindDict:
		for k, val := range v.Dict {
			if len(k) > limits.MaxKeyLen {
				return ErrTooLarge
			}
			if err := validateValue(&val, depth+1, limits); err != nil {
				return err
			}
		}
	case KindData:
		if len(v.Data) > limits.MaxDataLen {
			return ErrTooLarge
		}
	case KindString:
		if len(v.String) > limits.MaxStringLen {
			return ErrTooLarge
		}
	}
	return nil
}

func countNodes(v *Value, depth int, limits Limits) (int, error) {
	if depth > limits.MaxDepth {
		return 0, ErrTooDeep
	}
	n := 1
	switch v.Kind {
	case KindArray:
		for i := range v.Array {
			c, err := countNodes(&v.Array[i], depth+1, limits)
			if err != nil {
				return 0, err
			}
			n += c
		}
	case KindDict:
		keys := sortedKeys(v.Dict)
		n += len(keys)
		for _, k := range keys {
			val := v.Dict[k]
			c, err := countNodes(&val, depth+1, limits)
			if err != nil {
				return 0, err
			}
			n += c
		}
	}
	return n, nil
}

func bytesFor(maxValue int) int {
	switch {
	case maxValue <= 0xFF:
		return 1
	case maxValue <= 0xFFFF:
		return 2
	case maxValue <= 0xFFFFFFFF:
		return 4
	default:
		return 8
	}
}

type binEncoder struct {
	objects    [][]byte
	objRefSize int
}

func (e *binEncoder) add(v *Value, depth int) (int, error) {
	if depth > DefaultLimits().MaxDepth {
		return 0, ErrTooDeep
	}
	var obj []byte
	switch v.Kind {
	case KindBool:
		if v.Bool {
			obj = []byte{0x09}
		} else {
			obj = []byte{0x08}
		}
	case KindInt:
		obj = encodeInt(v.Int)
	case KindReal:
		obj = make([]byte, 9)
		obj[0] = 0x23
		binary.BigEndian.PutUint64(obj[1:], math.Float64bits(v.Real))
	case KindDate:
		secs := v.Date.Sub(appleEpoch).Seconds()
		obj = make([]byte, 9)
		obj[0] = 0x33
		binary.BigEndian.PutUint64(obj[1:], math.Float64bits(secs))
	case KindData:
		obj = encodeBlob(0x40, v.Data)
	case KindString:
		if isASCII(v.String) {
			obj = encodeBlob(0x50, []byte(v.String))
		} else {
			units := utf16.Encode([]rune(v.String))
			raw := make([]byte, len(units)*2)
			for i, u := range units {
				binary.BigEndian.PutUint16(raw[i*2:], u)
			}
			obj = encodeBlob(0x60, raw)
		}
	case KindArray:
		refs := make([]byte, len(v.Array)*e.objRefSize)
		for i := range v.Array {
			ref, err := e.add(&v.Array[i], depth+1)
			if err != nil {
				return 0, err
			}
			putRef(refs[i*e.objRefSize:], ref, e.objRefSize)
		}
		obj = encodeCollection(0xA0, len(v.Array), refs)
	case KindDict:
		keys := sortedKeys(v.Dict)
		keyRefs := make([]byte, len(keys)*e.objRefSize)
		for i, k := range keys {
			ref, err := e.add(&Value{Kind: KindString, String: k}, depth+1)
			if err != nil {
				return 0, err
			}
			putRef(keyRefs[i*e.objRefSize:], ref, e.objRefSize)
		}
		valRefs := make([]byte, len(keys)*e.objRefSize)
		for i, k := range keys {
			val := v.Dict[k]
			ref, err := e.add(&val, depth+1)
			if err != nil {
				return 0, err
			}
			putRef(valRefs[i*e.objRefSize:], ref, e.objRefSize)
		}
		obj = encodeCollection(0xD0, len(keys), append(keyRefs, valRefs...))
	default:
		return 0, errors.New("plist: unknown value kind")
	}

	ref := len(e.objects)
	e.objects = append(e.objects, obj)
	return ref, nil
}

func encodeInt(v int64) []byte {
	switch {
	case v >= -128 && v <= 127:
		return []byte{0x10, byte(v)}
	case v >= -32768 && v <= 32767:
		b := make([]byte, 3)
		b[0] = 0x11
		binary.BigEndian.PutUint16(b[1:], uint16(v))
		return b
	case v >= -2147483648 && v <= 2147483647:
		b := make([]byte, 5)
		b[0] = 0x12
		binary.BigEndian.PutUint32(b[1:], uint32(v))
		return b
	default:
		b := make([]byte, 9)
		b[0] = 0x13
		binary.BigEndian.PutUint64(b[1:], uint64(v))
		return b
	}
}

func encodeBlob(marker byte, data []byte) []byte {
	var out []byte
	if len(data) < 0xF {
		out = append(out, marker|byte(len(data)))
	} else {
		out = append(out, marker|0xF)
		out = append(out, encodeUintAsInt(uint64(len(data)))...)
	}
	return append(out, data...)
}

func encodeCollection(marker byte, count int, refs []byte) []byte {
	var out []byte
	if count < 0xF {
		out = append(out, marker|byte(count))
	} else {
		out = append(out, marker|0xF)
		out = append(out, encodeUintAsInt(uint64(count))...)
	}
	return append(out, refs...)
}

// encodeUintAsInt encodes n as a binary plist integer object using the
// smallest of 1, 2, 4, or 8 bytes.
func encodeUintAsInt(n uint64) []byte {
	switch {
	case n <= 0xFF:
		return []byte{0x10, byte(n)}
	case n <= 0xFFFF:
		b := make([]byte, 3)
		b[0] = 0x11
		binary.BigEndian.PutUint16(b[1:], uint16(n))
		return b
	case n <= 0xFFFFFFFF:
		b := make([]byte, 5)
		b[0] = 0x12
		binary.BigEndian.PutUint32(b[1:], uint32(n))
		return b
	default:
		b := make([]byte, 9)
		b[0] = 0x13
		binary.BigEndian.PutUint64(b[1:], n)
		return b
	}
}

func putRef(dst []byte, ref, size int) {
	for i := size - 1; i >= 0; i-- {
		dst[i] = byte(ref >> (8 * (size - 1 - i)))
	}
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
