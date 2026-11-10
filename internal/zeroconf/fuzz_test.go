package zeroconf

import "testing"

// FuzzParseDNS parses arbitrary mDNS packets, which any host on the link can
// send.
func FuzzParseDNS(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 8, '_', 'a', 'i', 'r', 'p', 'l', 'a', 'y', 4, '_', 't', 'c', 'p', 5, 'l', 'o', 'c', 'a', 'l', 0, 0, 12, 0, 1})
	f.Add([]byte{0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0, 1, 'a', 0xc0, 12, 0, 12, 0, 1, 0xc0, 12, 0, 12, 0, 1, 0, 0, 0, 1, 0, 2, 0xc0, 12})
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = parseDNS(data) })
}
