package hap

import (
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"fmt"
	"io"
	"math/big"
)

// srp implements the SRP-6a server used by HAP Pair Setup. HAP uses the
// 3072-bit group from RFC 5054 with SHA-512 as the hash function, matching
// the behavior of the established accessory implementations.

var (
	srpN = new(big.Int).SetBytes(mustHex(`
FFFFFFFF FFFFFFFF C90FDAA2 2168C234 C4C6628B 80DC1CD1 29024E08
8A67CC74 020BBEA6 3B139B22 514A0879 8E3404DD EF9519B3 CD3A431B
302B0A6D F25F1437 4FE1356D 6D51C245 E485B576 625E7EC6 F44C42E9
A637ED6B 0BFF5CB6 F406B7ED EE386BFB 5A899FA5 AE9F2411 7C4B1FE6
49286651 ECE45B3D C2007CB8 A163BF05 98DA4836 1C55D39A 69163FA8
FD24CF5F 83655D23 DCA3AD96 1C62F356 208552BB 9ED52907 7096966D
670C354E 4ABC9804 F1746C08 CA18217C 32905E46 2E36CE3B E39E772C
180E8603 9B2783A2 EC07A28F B5C55DF0 6F4C52C9 DE2BCBF6 95581718
3995497C EA956AE5 15D22618 98FA0510 15728E5A 8AAAC42D AD33170D
04507A33 A85521AB DF1CBA64 ECFB8504 58DBEF0A 8AEA7157 5D060C7D
B3970F85 A6E1E4C7 ABF5AE8C DB0933D7 1E8C94E0 4A25619D CEE3D226
1AD2EE6B F12FFA06 D98A0864 D8760273 3EC86A64 521F2B18 177B200C
BBE11757 7A615D6C 770988C0 BAD946E2 08E24FA0 74E5AB31 43DB5BFC
E0FD108E 4B82D120 A93AD2CA FFFFFFFF FFFFFFFF`))
	srpG = big.NewInt(5)

	// padLen is the group size in bytes; padded integers are left-padded to
	// this length before being hashed.
	padLen = srpN.BitLen() / 8
)

func mustHex(s string) []byte {
	var out []byte
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
			out = append(out, byte(r))
		}
	}
	if len(out)%2 != 0 {
		panic("hap: odd hex length")
	}
	b := make([]byte, len(out)/2)
	for i := range b {
		hi, _ := fromHexByte(out[2*i])
		lo, _ := fromHexByte(out[2*i+1])
		b[i] = hi<<4 | lo
	}
	return b
}

func fromHexByte(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// srpHash is HAP's SRP hash: SHA-512 over the concatenation of the minimal
// big-endian bytes of each integer argument and the raw bytes of each string
// argument. pad left-pads each integer to the group size.
func srpHash(pad bool, args ...any) *big.Int {
	return new(big.Int).SetBytes(srpDigest(pad, args...))
}

// srpDigest retains all 64 bytes when a hash is fed into another hash.
// Converting an intermediate digest to a big.Int would discard leading zeros.
func srpDigest(pad bool, args ...any) []byte {
	h := sha512.New()
	for _, a := range args {
		switch v := a.(type) {
		case *big.Int:
			b := minBytes(v)
			if pad {
				b = leftPad(b, padLen)
			}
			h.Write(b)
		case []byte:
			h.Write(v)
		case string:
			h.Write([]byte(v))
		default:
			panic(fmt.Sprintf("hap: unsupported SRP hash argument %T", a))
		}
	}
	return h.Sum(nil)
}

// minBytes returns the minimal big-endian representation of a non-negative
// integer, with the empty slice representing zero.
func minBytes(n *big.Int) []byte {
	if n.Sign() == 0 {
		return nil
	}
	return n.Bytes()
}

func leftPad(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}

// srpServer holds one Pair Setup SRP exchange state.
type srpServer struct {
	n *big.Int
	g *big.Int
	k *big.Int

	salt     *big.Int
	verifier *big.Int
	secret   *big.Int // ephemeral private exponent b
	public   *big.Int // B

	clientPublic *big.Int
	u            *big.Int
	session      *big.Int // S
	key          *big.Int // K, for compatibility with integer SRP tests
	keyDigest    []byte   // full 64-byte K for proofs and transport keys
	m1           *big.Int
	m1Digest     []byte
	m2           *big.Int
	m2Digest     []byte
}

// newSRPServer creates a server for username with the given password. HAP
// always uses username "Pair-Setup".
func newSRPServer(rand io.Reader, username, password string) (*srpServer, error) {
	if rand == nil {
		rand = cryptoRand{}
	}
	n := srpN
	g := srpG
	k := srpHash(true, n, g)

	salt, err := randInt(rand, 128)
	if err != nil {
		return nil, err
	}
	salt.Mod(salt, n)

	secret, err := randInt(rand, 512)
	if err != nil {
		return nil, err
	}
	secret.Mod(secret, n)

	// x = H(salt || H(username ":" password))
	x := srpHash(false, salt, srpDigest(false, username+":"+password))
	verifier := new(big.Int).Exp(g, x, n)

	// B = (k*v + g^b) mod n
	public := new(big.Int).Mul(k, verifier)
	public.Add(public, new(big.Int).Exp(g, secret, n))
	public.Mod(public, n)

	return &srpServer{
		n:        n,
		g:        g,
		k:        k,
		salt:     salt,
		verifier: verifier,
		secret:   secret,
		public:   public,
	}, nil
}

// saltBytes returns the salt as minimal big-endian bytes.
func (s *srpServer) saltBytes() []byte { return minBytes(s.salt) }

// publicBytes returns B as minimal big-endian bytes.
func (s *srpServer) publicBytes() []byte { return minBytes(s.public) }

// setClientPublic processes the client's A and derives S, K, and M1.
func (s *srpServer) setClientPublic(aBytes []byte) error {
	a := new(big.Int).SetBytes(aBytes)
	if a.Sign() <= 0 || a.Cmp(s.n) >= 0 {
		return fmt.Errorf("%w: invalid SRP A", ErrAuthentication)
	}
	// A mod n must not be zero.
	a.Mod(a, s.n)
	if a.Sign() == 0 {
		return fmt.Errorf("%w: invalid SRP A", ErrAuthentication)
	}

	s.clientPublic = a
	s.u = srpHash(true, a, s.public)

	// S = (A * v^u)^b mod n
	t := new(big.Int).Exp(s.verifier, s.u, s.n)
	t.Mul(a, t)
	t.Mod(t, s.n)
	t.Exp(t, s.secret, s.n)
	s.session = t

	s.keyDigest = srpDigest(false, s.session)
	s.key = new(big.Int).SetBytes(s.keyDigest)

	// M1 = H(H(N) ^ H(g) || H(I) || s || A || B || K).
	// H(N), H(g), H(I), and K are byte digests, not minimal big integers.
	hNG := srpDigest(false, s.n)
	hG := srpDigest(false, s.g)
	for i := range hNG {
		hNG[i] ^= hG[i]
	}
	s.m1Digest = srpDigest(false, hNG, srpDigest(false, usernamePairSetup), s.salt, a, s.public, s.keyDigest)
	s.m1 = new(big.Int).SetBytes(s.m1Digest)
	return nil
}

// verifyProof checks the client's full 64-byte M1 proof and derives M2.
func (s *srpServer) verifyProof(clientProof []byte) bool {
	if subtle.ConstantTimeCompare(s.m1Digest, clientProof) != 1 {
		return false
	}
	s.m2Digest = srpDigest(false, s.clientPublic, s.m1Digest, s.keyDigest)
	s.m2 = new(big.Int).SetBytes(s.m2Digest)
	return true
}

// proofBytes returns the full 64-byte server M2 proof, valid after verifyProof.
func (s *srpServer) proofBytes() []byte { return s.m2Digest }

// sessionKeyBytes returns the full 64-byte SRP session key.
func (s *srpServer) sessionKeyBytes() []byte { return s.keyDigest }

const usernamePairSetup = "Pair-Setup"

type cryptoRand struct{}

func (cryptoRand) Read(p []byte) (int, error) { return rand.Read(p) }

// randInt returns a uniformly random non-negative integer with at most bits
// bits. HAP draws 128-bit salts and 512-bit private exponents.
func randInt(r io.Reader, bits int) (*big.Int, error) {
	n := (bits + 7) / 8
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(buf), nil
}
