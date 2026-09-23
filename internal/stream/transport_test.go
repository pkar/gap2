package stream

import (
	"errors"
	"testing"
)

func TestParseTransportUDPUnicast(t *testing.T) {
	tr, err := ParseTransport("RTP/AVP/UDP;unicast;client_port=6000-6001;server_port=7000-7001")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Protocol != "RTP/AVP/UDP" || tr.Mode != "unicast" {
		t.Fatalf("transport = %+v", tr)
	}
	if tr.ClientPort != 6000 || tr.ClientPortEnd != 6001 {
		t.Fatalf("client ports = %+v", tr)
	}
	if tr.ServerPort != 7000 || tr.ServerPortEnd != 7001 {
		t.Fatalf("server ports = %+v", tr)
	}
}

func TestParseTransportTCPInterleaved(t *testing.T) {
	tr, err := ParseTransport("RTP/AVP/TCP;unicast;interleaved=0-1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Protocol != "RTP/AVP/TCP" || tr.Interleaved != 0 || tr.InterleavedEnd != 1 {
		t.Fatalf("transport = %+v", tr)
	}
}

func TestParseTransportErrors(t *testing.T) {
	for _, v := range []string{"", ";unicast", "RTP/AVP/UDP;client_port=bad"} {
		if _, err := ParseTransport(v); !errors.Is(err, ErrBadTransport) {
			t.Fatalf("ParseTransport(%q) = %v, want ErrBadTransport", v, err)
		}
	}
}
