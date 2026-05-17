package noise

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// genKeypair returns a fresh X25519 (priv, pub) pair drawn from crypto/rand.
// Helper for setting up handshakes in tests.
func genKeypair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return k.Bytes(), k.PublicKey().Bytes()
}

// runHandshake runs a complete IK handshake between a fresh initiator and a
// fresh responder built with the given keys, returning the four CipherStates
// and any early-data payloads echoed back. Centralises the happy-path setup
// so each test focuses on its own assertion.
func runHandshake(t *testing.T, initPriv, respPriv, respPub []byte, helloEarly, ackEarly []byte) (iSend, iRecv, rSend, rRecv *CipherState, gotHello, gotAck []byte) {
	t.Helper()
	initiator, err := NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	responder, err := NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	initMsg, err := initiator.WriteInit(helloEarly)
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	gotHello, err = responder.ReadInit(initMsg)
	if err != nil {
		t.Fatalf("ReadInit: %v", err)
	}
	respMsg, rs, rr, err := responder.WriteResp(ackEarly)
	if err != nil {
		t.Fatalf("WriteResp: %v", err)
	}
	gotAck, is, ir, err := initiator.ReadResp(respMsg)
	if err != nil {
		t.Fatalf("ReadResp: %v", err)
	}
	return is, ir, rs, rr, gotHello, gotAck
}

func TestRoundTrip_HandshakeCompletes(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)

	hello := []byte("hello-early")
	ack := []byte("hello-ack-early")
	iSend, iRecv, rSend, rRecv, gotHello, gotAck := runHandshake(t, initPriv, respPriv, respPub, hello, ack)

	if !bytes.Equal(gotHello, hello) {
		t.Errorf("early-data hello = %q, want %q", gotHello, hello)
	}
	if !bytes.Equal(gotAck, ack) {
		t.Errorf("early-data ack = %q, want %q", gotAck, ack)
	}
	for name, cs := range map[string]*CipherState{
		"iSend": iSend, "iRecv": iRecv, "rSend": rSend, "rRecv": rRecv,
	} {
		if cs == nil {
			t.Errorf("%s CipherState is nil", name)
		}
	}
}

// TestRoundTrip_BothDirections pins the responder-side cs1/cs2 swap. If
// Responder.WriteResp returned (cs1, cs2) instead of (cs2, cs1), this test
// fails on the first Decrypt with a MAC error. Do not weaken.
func TestRoundTrip_BothDirections(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)
	iSend, iRecv, rSend, rRecv, _, _ := runHandshake(t, initPriv, respPriv, respPub, nil, nil)

	from := []byte("from initiator")
	c1, err := iSend.Encrypt(from)
	if err != nil {
		t.Fatalf("iSend.Encrypt: %v", err)
	}
	got, err := rRecv.Decrypt(c1)
	if err != nil {
		t.Fatalf("rRecv.Decrypt: %v", err)
	}
	if !bytes.Equal(got, from) {
		t.Errorf("rRecv decrypted = %q, want %q", got, from)
	}

	resp := []byte("from responder")
	c2, err := rSend.Encrypt(resp)
	if err != nil {
		t.Fatalf("rSend.Encrypt: %v", err)
	}
	got, err = iRecv.Decrypt(c2)
	if err != nil {
		t.Fatalf("iRecv.Decrypt: %v", err)
	}
	if !bytes.Equal(got, resp) {
		t.Errorf("iRecv decrypted = %q, want %q", got, resp)
	}
}

func TestRoundTrip_ManyFrames(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)
	iSend, iRecv, rSend, rRecv, _, _ := runHandshake(t, initPriv, respPriv, respPub, nil, nil)

	for i := 0; i < 32; i++ {
		// initiator → responder
		pt := []byte{byte(i), 0x01, 0x02, 0x03}
		ct, err := iSend.Encrypt(pt)
		if err != nil {
			t.Fatalf("iSend.Encrypt frame %d: %v", i, err)
		}
		got, err := rRecv.Decrypt(ct)
		if err != nil {
			t.Fatalf("rRecv.Decrypt frame %d: %v", i, err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("frame %d i→r mismatch: got %x want %x", i, got, pt)
		}
		// responder → initiator
		pt = []byte{0xff, byte(i), 0xaa, 0xbb}
		ct, err = rSend.Encrypt(pt)
		if err != nil {
			t.Fatalf("rSend.Encrypt frame %d: %v", i, err)
		}
		got, err = iRecv.Decrypt(ct)
		if err != nil {
			t.Fatalf("iRecv.Decrypt frame %d: %v", i, err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("frame %d r→i mismatch: got %x want %x", i, got, pt)
		}
	}
}

func TestReadInit_RejectsTamperedMessage(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)
	initiator, err := NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	responder, err := NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	initMsg, err := initiator.WriteInit([]byte("hi"))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	tampered := append([]byte(nil), initMsg...)
	tampered[len(tampered)/2] ^= 0x01

	got, err := responder.ReadInit(tampered)
	if err == nil {
		t.Fatal("ReadInit accepted tampered message; want error")
	}
	if got != nil {
		t.Errorf("earlyData = %x, want nil on tamper", got)
	}
}

func TestReadInit_RejectsTruncatedMessage(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)
	initiator, err := NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	responder, err := NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	initMsg, err := initiator.WriteInit([]byte("hi"))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	truncated := initMsg[:len(initMsg)/2]
	if _, err := responder.ReadInit(truncated); err == nil {
		t.Fatal("ReadInit accepted truncated message; want error")
	}
}

func TestReadResp_RejectsTamperedMessage(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)
	initiator, err := NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	responder, err := NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	initMsg, err := initiator.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	if _, err := responder.ReadInit(initMsg); err != nil {
		t.Fatalf("ReadInit: %v", err)
	}
	respMsg, _, _, err := responder.WriteResp(nil)
	if err != nil {
		t.Fatalf("WriteResp: %v", err)
	}
	tampered := append([]byte(nil), respMsg...)
	tampered[len(tampered)/2] ^= 0x01

	got, send, recv, err := initiator.ReadResp(tampered)
	if err == nil {
		t.Fatal("ReadResp accepted tampered message; want error")
	}
	if got != nil || send != nil || recv != nil {
		t.Errorf("returns on error: got=%x send=%v recv=%v, want all nil", got, send, recv)
	}
}

// TestHandshake_RejectsWrongResponderStatic: the initiator builds with the
// wrong responder-pubkey, so the responder's ReadInit observes a MAC
// failure when decrypting the encrypted-s field of message 1 (DH outputs
// disagree). The responder never produces a respMsg, so the initiator's
// ReadResp is never reached — see the architect spec § Wrong-key
// rejection for the note on the AC wording.
func TestHandshake_RejectsWrongResponderStatic(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, _ := genKeypair(t)    // real responder
	_, fakePub := genKeypair(t)     // wrong public the initiator encrypts to

	initiator, err := NewInitiator(initPriv, fakePub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	responder, err := NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	initMsg, err := initiator.WriteInit([]byte("hi"))
	if err != nil {
		t.Fatalf("WriteInit (sealed to wrong pub): %v", err)
	}
	if _, err := responder.ReadInit(initMsg); err == nil {
		t.Fatal("ReadInit accepted handshake sealed to wrong responder pubkey; want MAC failure")
	}
}

func TestNewResponder_RejectsBadKeyLength(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		priv []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"31 bytes", bytes.Repeat([]byte{1}, 31)},
		{"33 bytes", bytes.Repeat([]byte{1}, 33)},
		{"64 bytes", bytes.Repeat([]byte{1}, 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, err := NewResponder(tc.priv)
			if r != nil {
				t.Errorf("Responder = %v, want nil", r)
			}
			if !errors.Is(err, ErrInvalidKeyLength) {
				t.Errorf("err = %v, want ErrInvalidKeyLength", err)
			}
		})
	}
}

func TestNewInitiator_RejectsBadKeyLength(t *testing.T) {
	t.Parallel()
	good := bytes.Repeat([]byte{1}, KeyLen)
	bad31 := bytes.Repeat([]byte{1}, 31)
	bad33 := bytes.Repeat([]byte{1}, 33)
	bad64 := bytes.Repeat([]byte{1}, 64)
	cases := []struct {
		name string
		priv []byte
		pub  []byte
	}{
		{"both nil", nil, nil},
		{"priv 31, pub good", bad31, good},
		{"priv 33, pub good", bad33, good},
		{"priv good, pub 31", good, bad31},
		{"priv good, pub 33", good, bad33},
		{"priv good, pub 64", good, bad64},
		{"both bad", bad31, bad31},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			i, err := NewInitiator(tc.priv, tc.pub)
			if i != nil {
				t.Errorf("Initiator = %v, want nil", i)
			}
			if !errors.Is(err, ErrInvalidKeyLength) {
				t.Errorf("err = %v, want ErrInvalidKeyLength", err)
			}
		})
	}
}

func TestDecrypt_RejectsOutOfOrderFrame(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)
	iSend, _, _, rRecv, _, _ := runHandshake(t, initPriv, respPriv, respPub, nil, nil)

	c1, err := iSend.Encrypt([]byte("frame-0"))
	if err != nil {
		t.Fatalf("Encrypt p1: %v", err)
	}
	c2, err := iSend.Encrypt([]byte("frame-1"))
	if err != nil {
		t.Fatalf("Encrypt p2: %v", err)
	}
	// Deliver c2 before c1 — the wire carries no nonce; rRecv expects
	// nonce 0 but c2 was sealed against nonce 1.
	got, err := rRecv.Decrypt(c2)
	if err == nil {
		t.Fatal("Decrypt accepted out-of-order frame; want error")
	}
	if got != nil {
		t.Errorf("plaintext = %x, want nil on error", got)
	}
	_ = c1
}

func TestDecrypt_RejectsReplayedFrame(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)
	iSend, _, _, rRecv, _, _ := runHandshake(t, initPriv, respPriv, respPub, nil, nil)

	c1, err := iSend.Encrypt([]byte("frame-0"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := rRecv.Decrypt(c1); err != nil {
		t.Fatalf("first Decrypt: %v", err)
	}
	if _, err := rRecv.Decrypt(c1); err == nil {
		t.Fatal("Decrypt accepted replayed frame; want error")
	}
}

// TestResponder_PeerStatic_AfterReadInit_MatchesInitiatorStatic drives a
// fresh IK message 1 from a known initiator static key into a fresh
// Responder, then asserts PeerStatic returns the initiator's public key
// and that the returned slice is a defensive copy (mutating it does not
// affect subsequent calls or flynn's internal state).
func TestResponder_PeerStatic_AfterReadInit_MatchesInitiatorStatic(t *testing.T) {
	t.Parallel()
	initPriv, initPub := genKeypair(t)
	respPriv, respPub := genKeypair(t)

	initiator, err := NewInitiator(initPriv, respPub)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	responder, err := NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	initMsg, err := initiator.WriteInit(nil)
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	if _, err := responder.ReadInit(initMsg); err != nil {
		t.Fatalf("ReadInit: %v", err)
	}

	got := responder.PeerStatic()
	if len(got) != KeyLen {
		t.Fatalf("PeerStatic len = %d, want %d", len(got), KeyLen)
	}
	if !bytes.Equal(got, initPub) {
		t.Errorf("PeerStatic = %x, want %x", got, initPub)
	}

	// Defensive-copy contract: mutating one return must not affect another.
	a := responder.PeerStatic()
	b := responder.PeerStatic()
	a[0] ^= 0xff
	if !bytes.Equal(b, initPub) {
		t.Errorf("second call returned mutated value: got %x, want %x", b, initPub)
	}
	c := responder.PeerStatic()
	if !bytes.Equal(c, initPub) {
		t.Errorf("third call returned mutated value: got %x, want %x", c, initPub)
	}
}

// TestResponder_PeerStatic_BeforeReadInit_ReturnsZeroLength pins the
// pre-handshake contract: no panic, returns a zero-length slice.
func TestResponder_PeerStatic_BeforeReadInit_ReturnsZeroLength(t *testing.T) {
	t.Parallel()
	respPriv, _ := genKeypair(t)
	responder, err := NewResponder(respPriv)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	got := responder.PeerStatic()
	if len(got) != 0 {
		t.Errorf("PeerStatic before ReadInit = %x (len=%d), want zero-length", got, len(got))
	}
}

// TestErrorMessages_DoNotLeakPlaintextOrKey: a single defensive assertion
// against future refactors that might pull plaintext into the error
// message. Cheap, pins the contract documented in the package comment.
func TestErrorMessages_DoNotLeakPlaintextOrKey(t *testing.T) {
	t.Parallel()
	initPriv, _ := genKeypair(t)
	respPriv, respPub := genKeypair(t)
	iSend, _, _, rRecv, _, _ := runHandshake(t, initPriv, respPriv, respPub, nil, nil)

	var probe [16]byte
	if _, err := rand.Read(probe[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	ct, err := iSend.Encrypt(probe[:])
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	tampered := append([]byte(nil), ct...)
	tampered[0] ^= 0x01
	_, err = rRecv.Decrypt(tampered)
	if err == nil {
		t.Fatal("Decrypt accepted tampered ciphertext")
	}
	msg := err.Error()
	if strings.Contains(msg, hex.EncodeToString(probe[:])) {
		t.Errorf("error message contains hex-encoded plaintext probe: %q", msg)
	}
	if strings.Contains(msg, base64.StdEncoding.EncodeToString(probe[:])) {
		t.Errorf("error message contains base64-encoded plaintext probe: %q", msg)
	}
}
