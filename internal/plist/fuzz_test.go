package plist

import "testing"

// FuzzDecode decodes arbitrary binary and XML property lists. Whatever
// decodes must encode to something that decodes again.
func FuzzDecode(f *testing.F) {
	v := Dict(map[string]*Value{"a": Int(1), "b": String("x"), "c": Array(Data([]byte{1, 2}), Real(1.5)), "é": String("héllo")})
	b, _ := Encode(v)
	f.Add(b)
	f.Add([]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>a</key><integer>1</integer><key>d</key><data>AAEC</data><key>t</key><date>2001-01-01T00:00:00Z</date></dict></plist>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := Decode(data, DefaultLimits())
		if err != nil {
			return
		}
		out, err := Encode(v)
		if err != nil {
			return
		}
		if _, err := Decode(out, DefaultLimits()); err != nil {
			t.Fatalf("re-decode: %v", err)
		}
	})
}
