package tlv8

import (
	"bytes"
	"testing"
)

// FuzzDecode decodes arbitrary TLV8 and checks that what decodes survives an
// encode and decode round trip.
func FuzzDecode(f *testing.F) {
	f.Add([]byte{6, 1, 1, 3, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		items, err := Decode(data)
		if err != nil {
			return
		}
		enc, _ := Encode(items)
		again, err := Decode(enc)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != len(items) {
			t.Fatalf("items %d -> %d", len(items), len(again))
		}
		for i := range items {
			if items[i].Type != again[i].Type || !bytes.Equal(items[i].Value, again[i].Value) {
				t.Fatalf("item %d differs", i)
			}
		}
	})
}
