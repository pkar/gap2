package hap

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"
)

// TestSRPServerReference cross-checks the SRP-6a server against values
// produced by an independent implementation of the HAP SRP convention
// (SHA-512, RFC 5054 3072-bit group, fixed salt and private exponent).
func TestSRPServerReference(t *testing.T) {
	randInput := mustHexBytes(t,
		"00112233445566778899aabbccddeeff"+ // 16-byte salt
			"0102030405060708090a0b0c0d0e0f10"+
			"1112131415161718191a1b1c1d1e1f20"+
			"2122232425262728292a2b2c2d2e2f30"+
			"3132333435363738393a3b3c3d3e3f40") // 64-byte private exponent

	srv, err := newSRPServer(bytes.NewReader(randInput), "Pair-Setup", "3939")
	if err != nil {
		t.Fatalf("newSRPServer: %v", err)
	}

	const wantB = "0883f9cec922ad32dd41c9b59357e8e6080a1ab4040e8157e52929893cc85b5f" +
		"e421586256d8f696e14bfe2aa2157a9701e1cdfbf1655cd7098ef7240100d56c198b1c58fd" +
		"f5d1b06e44bfce5084556b6fd67f81b08fa80d9d0697f11e31b725519781a17201c8d53dff" +
		"4b3ac11878352bc6f98110c8ef1e912454c0493ff8ae4391947574920d47120b8707280f86" +
		"be9301e9920b3ddf68db75c3284f247d88a4b318fc571061d1fb50ee9245e723b813ff7569" +
		"523c2a40b6101d25aed83d8beb581aef2dcc0060124fb8f7208953e6091d31b38c18ac62ba" +
		"6da23807752b07e810c911f2a6d8befeb9184732123c44922ccbb68205f07f3a24ace4e4ef" +
		"cad245eb10c3968c505fc4dcf9bdc6125506ca89fe6dd945bf2534835cbc04c6500ccd6945" +
		"5531c390df1a03dbcb029eb808fd25b679304c1c7dbaa5c69835a0dffcbad03fb1fcdcefd3" +
		"d8b58530d6a60a77e5c2ce01c95aef7e06fd7094244432c844c09d93fe04b5b2d38a17c8b7" +
		"f710b08b00684df5f134188780eb286d776654"

	if got := hex.EncodeToString(srv.publicBytes()); got != wantB {
		t.Fatalf("B mismatch\n got %s\nwant %s", got, wantB)
	}

	clientA := new(big.Int).SetBytes(bytes.Repeat([]byte{0x11}, 64))
	clientA.Mod(clientA, srpN)
	clientAPub := new(big.Int).Exp(srpG, clientA, srpN)
	if err := srv.setClientPublic(clientAPub.Bytes()); err != nil {
		t.Fatalf("setClientPublic: %v", err)
	}

	const wantK = "e0b8b7a5a6098120394fe18b3072b2d26313bd6ab04949febc8279e1a3e12180" +
		"2d9dd4731bb6fe971041303ba7ee56d1f8de889d660ea260730e1121a5104a9d"
	if got := hex.EncodeToString(srv.sessionKeyBytes()); got != wantK {
		t.Fatalf("K mismatch\n got %s\nwant %s", got, wantK)
	}

	const wantM1 = "244765c8ac5556f16924e8acd1955686b72c209dd0c6a2999c666319883af5da" +
		"acc86aa11ab9ad73c955733ac6ed0203d2ed8cedd1e76528a9df21d87e510e83"
	if got := hex.EncodeToString(srv.m1.Bytes()); got != wantM1 {
		t.Fatalf("M1 mismatch\n got %s\nwant %s", got, wantM1)
	}

	if !srv.verifyProof(mustHexBytes(t, wantM1)) {
		t.Fatal("verifyProof rejected the correct client proof")
	}

	const wantM2 = "b851b84f58a45db254854ac211456c3a5de92907b2abf3652cc7ddba0c221f29" +
		"f9e451a02024a81e339a6e0ca06908c63139778805dbb7247edbbe8d0ef88699"
	if got := hex.EncodeToString(srv.proofBytes()); got != wantM2 {
		t.Fatalf("M2 mismatch\n got %s\nwant %s", got, wantM2)
	}

	if srv.verifyProof(mustHexBytes(t, "ffff")) {
		t.Fatal("verifyProof accepted a wrong client proof")
	}
}

// TestSRPInterop checks that a simulated controller and the server derive the
// same session key and accept each other's proofs.
func TestSRPInterop(t *testing.T) {
	randInput := mustHexBytes(t,
		"00112233445566778899aabbccddeeff"+
			"0102030405060708090a0b0c0d0e0f10"+
			"1112131415161718191a1b1c1d1e1f20"+
			"2122232425262728292a2b2c2d2e2f30"+
			"3132333435363738393a3b3c3d3e3f40")
	srv, err := newSRPServer(bytes.NewReader(randInput), "Pair-Setup", "3939")
	if err != nil {
		t.Fatalf("newSRPServer: %v", err)
	}

	// Controller side with a fixed exponent.
	controllerExp := mustHexBytes(t, "1111111111111111111111111111111111111111111111111111111111111111")
	a := newBigInt(controllerExp)
	a.Mod(a, srpN)
	clientPub := newBigInt(nil).Exp(srpG, a, srpN)

	if err := srv.setClientPublic(clientPub.Bytes()); err != nil {
		t.Fatalf("setClientPublic: %v", err)
	}
	serverPub := newBigInt(nil).Set(srv.public)

	// Controller computes x = H(salt || H(username:password)), v = g^x,
	// u = H(pad(A) || pad(B)), then S = (B - k*v)^(a + u*x) mod N.
	x := srpHash(false, srv.salt, srpHash(false, "Pair-Setup:3939"))
	v := newBigInt(nil).Exp(srpG, x, srpN)
	u := srpHash(true, clientPub, serverPub)
	k := srpHash(true, srpN, srpG)

	kv := newBigInt(nil).Mul(k, v)
	S := newBigInt(nil).Sub(serverPub, kv)
	S.Mod(S, srpN)
	if S.Sign() < 0 {
		S.Add(S, srpN)
	}
	exp := newBigInt(nil).Mul(u, x)
	exp.Add(exp, a)
	S.Exp(S, exp, srpN)
	controllerKey := srpHash(false, S)

	if controllerKey.Cmp(srv.key) != 0 {
		t.Fatalf("session key mismatch\n server %x\n client %x", srv.key.Bytes(), controllerKey.Bytes())
	}

	hN := srpHash(false, srpN)
	hG := srpHash(false, srpG)
	hNG := new(big.Int).Xor(hN, hG)
	clientM1 := srpHash(false, hNG, srpHash(false, "Pair-Setup"), srv.salt, clientPub, serverPub, controllerKey)
	if !srv.verifyProof(clientM1.Bytes()) {
		t.Fatal("server rejected interop client proof")
	}
	if srv.m2.Cmp(srpHash(false, clientPub, clientM1, controllerKey)) != 0 {
		t.Fatal("server M2 does not match controller expectation")
	}
}

func newBigInt(b []byte) *big.Int { return new(big.Int).SetBytes(b) }
