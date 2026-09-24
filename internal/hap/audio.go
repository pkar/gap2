package hap

import "fmt"

// AudioDecryptor authenticates AirPlay 2 realtime RTP packets using the
// per-stream shk key supplied over the encrypted control connection.
type AudioDecryptor struct{ key []byte }

func NewAudioDecryptor(key []byte) (*AudioDecryptor, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("hap: audio key must be 32 bytes")
	}
	return &AudioDecryptor{key: append([]byte(nil), key...)}, nil
}

// Open returns an ordinary RTP packet with its authenticated audio payload.
// The timestamp and SSRC form the AAD; the final eight bytes carry the nonce.
func (d *AudioDecryptor) Open(packet []byte) ([]byte, error) {
	if len(packet) < 12+16+8 || packet[0] != 0x80 {
		return nil, fmt.Errorf("hap: invalid encrypted audio packet")
	}
	return d.open(packet)
}

// OpenBuffered authenticates a TCP audio frame and normalizes its 24-bit
// sequence header to RTP. Its timestamp and format word are authenticated
// exactly as in realtime audio; the length prefix is removed by the transport.
func (d *AudioDecryptor) OpenBuffered(packet []byte) ([]byte, error) {
	if len(packet) < 12+16+8 {
		return nil, fmt.Errorf("hap: invalid encrypted buffered audio packet")
	}
	out, err := d.open(packet)
	if err != nil {
		return nil, err
	}
	out[0], out[1] = 0x80, 96
	return out, nil
}

func (d *AudioDecryptor) open(packet []byte) ([]byte, error) {
	nonce := make([]byte, 12)
	copy(nonce[4:], packet[len(packet)-8:])
	plain, err := aeadOpen(d.key, nonce, packet[12:len(packet)-8], packet[4:12])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 12, 12+len(plain))
	copy(out, packet[:12])
	return append(out, plain...), nil
}
