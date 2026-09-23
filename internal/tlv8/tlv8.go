// Package tlv8 implements HomeKit-style TLV8 encoding and decoding, including
// value fragmentation. Fragments after the first carry the type's high bit set
// (0x80) as a continuation marker.
package tlv8

import (
	"errors"
	"fmt"
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
		if it.Type&0x80 != 0 {
			return nil, fmt.Errorf("tlv8: reserved type 0x%02x", it.Type)
		}
		if len(it.Value) == 0 {
			out = append(out, it.Type, 0)
			continue
		}

		t := it.Type
		v := it.Value
		for len(v) > 0 {
			n := len(v)
			if n > maxFragment {
				n = maxFragment
			}
			out = append(out, t, byte(n))
			out = append(out, v[:n]...)
			v = v[n:]
			t = it.Type | 0x80
		}
	}
	return out, nil
}

// Decode parses data and reassembles continuation fragments.
func Decode(data []byte) ([]Item, error) {
	var items []Item
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
		val := append([]byte(nil), data[:n]...)
		data = data[n:]

		if t&0x80 != 0 {
			base := t & 0x7f
			if len(items) == 0 || items[len(items)-1].Type != base {
				return nil, fmt.Errorf("tlv8: unexpected fragment type 0x%02x", t)
			}
			items[len(items)-1].Value = append(items[len(items)-1].Value, val...)
			continue
		}
		items = append(items, Item{Type: t, Value: val})
	}
	return items, nil
}
