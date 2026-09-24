package airplay2

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
)

// The FPLY v3 setup exchange precedes native AP2 stream negotiation. These
// are fixed protocol responses, not receiver credentials. Native AP2 audio
// keys arrive separately as shk inside the encrypted SETUP request.
var fpSetupReplies = [...]string{
	"46504c59030102000000008202000f9f3f9e0a2521dbdf312ab2bfb29e8d232b6376a8c818701d22ae93d82737feaf9db4fdf41c2dba9d1f49caaabf6591ac1f7bc6f7e0663d21afe01565953eab81f418ceed095adb7c3d0e254909a79831d49c3982973434facb42c63a1cd911a6fe941a8a6d4a743b46c3a7649e44c78955e49d8155009549c4e2f7a3f6d5ba",
	"46504c5903010200000000820201cf32a25714b2524f8aa0ad7af164e37bcf4424e200047efc0ad67afcd95ded1c2730bb591b962ed63a9c4ded88ba8fc78de64d91ccfd5c7b56da88e31f5cceafc7431995a01665a54e1939d25b94db64b9e45d8d063e1e6af07e9656162b0efa404275ea5a44d9591c7256b9fbe6513898b80227721988571650942ad946688a",
	"46504c5903010200000000820202c169a352eeed35b18cdd9c58d64f16c1519a89eb5317bd0d4336cd68f638ff9d016a5b52b7fa9216b2b65482c78444118121a2c7fed83db7119e9182aad7d18c7063e2a457555910af9e0efc76347d164043807f581ee4fbe42ca9dedc1b5eb2a3aa3d2ecd59e7eee70b3629f22afd161d877353ddb99adc8e07006e56f850ce",
	"46504c59030102000000008202039001e1727e0f57f9f5880db104a6257a23f5cfff1abbe1e93045251afb97eb9fc0011ebe0f3a81df5b691d76acb2f7a5c708e3d328f56bb39dbde5f29c8a17f481487e3ae863c678325422e6f78e166d18aa7fd636258bce28726f661f738893ce44311e4be6c0535193e5ef72e8686233729c227d820c999445d89246c8c359",
}

func (s *controlServer) handleFPSetup(cs *connState, req *ctlRequest) error {
	if cs.encrypted == nil {
		return cs.writeError(401, "Unauthorized")
	}
	b := req.body
	if len(b) < 12 || !bytes.Equal(b[:6], []byte{'F', 'P', 'L', 'Y', 3, 1}) ||
		binary.BigEndian.Uint32(b[8:12]) != uint32(len(b)-12) {
		return cs.writeError(400, "Bad Request")
	}
	var reply []byte
	switch {
	case b[6] == 1 && len(b) == 16 && int(b[14]) < len(fpSetupReplies):
		reply, _ = hex.DecodeString(fpSetupReplies[b[14]])
		cs.fpStarted = true
	case b[6] == 3 && len(b) == 164 && cs.fpStarted:
		reply = append([]byte{'F', 'P', 'L', 'Y', 3, 1, 4, 0, 0, 0, 0, 20}, b[len(b)-20:]...)
		cs.fpStarted = false
	default:
		return cs.writeError(400, "Bad Request")
	}
	return cs.writeResponse(200, "OK", "application/octet-stream", reply)
}
