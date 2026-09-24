// Package tlv8 implements HomeKit-style TLV8 encoding and decoding, including
// value fragmentation. Fragments use consecutive records of the SAME type;
// the preceding record's length of 255 marks a continuation.
package tlv8

import (
	"errors"
)

const maxFragment = 255

// Item is one decoded logical TLV.
type Item struct {
	Type  uint8
	Value []byte
}

// Encode serializes items, fragmenting values larger than 255 bytes.
func Encode(items []Item) ([]byte, error) {
	var out []byte
	for _, it := range items {
		if len(it.Value) == 0 {
			out = append(out, it.Type, 0)
			continue
		}

		v := it.Value
		for len(v) > 0 {
			n := len(v)
			if n > maxFragment {
				n = maxFragment
			}
			out = append(out, it.Type, byte(n))
			out = append(out, v[:n]...)
			v = v[n:]
		}
	}
	return out, nil
}

// Decode parses data and reassembles continuation fragments.
func Decode(data []byte) ([]Item, error) {
	var items []Item
	previousFull := false
	for len(data) > 0 {
		if len(data) < 2 {
			return nil, errors.New("tlv8: truncated header")
		}
		t := data[0]
		n := int(data[1])
		data = data[2:]
		if n > len(data) {
			return nil, errors.New("tlv8: truncated value")
		}
		if previousFull && len(items) > 0 && items[len(items)-1].Type == t {
			items[len(items)-1].Value = append(items[len(items)-1].Value, data[:n]...)
		} else {
			items = append(items, Item{Type: t, Value: append([]byte(nil), data[:n]...)})
		}
		previousFull = n == maxFragment
		data = data[n:]
	}
	return items, nil
}
