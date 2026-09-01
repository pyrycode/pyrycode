//go:build e2e

package e2e

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/noise"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// Interactive-capability handshake helpers, shared by the stream interactive
// specs. They lived in relay_two_phone_structured_test.go until #1348 deleted
// that spec with the rest of the terminal-path twins; nine surviving stream
// specs call them, so they moved here rather than dying with their old home.

// buildHelloEarlyInteractive mirrors buildHelloEarly (relay_v2_handshake_test.go)
// but advertises the interactive capability so the daemon grants it. Defined here
// (not in relay_v2_handshake_test.go) alongside the other v2 two-phone helpers.
func buildHelloEarlyInteractive(t *testing.T, token string) []byte {
	t.Helper()
	return buildHelloEarlyInteractiveResuming(t, token, nil)
}

// buildHelloEarlyInteractiveResuming is buildHelloEarlyInteractive with an
// optional last_event_id, so a reconnecting phone can ask for the missed tail of
// the daemon-resolved conversation. nil omits the field entirely (the fresh-connect
// hello buildHelloEarlyInteractive delegates here for); a pointer to 0 is the
// documented fresh-consumer input Ring.After answers with the WHOLE retained tail,
// which is what lets a re-dialing witness read history it was disconnected for
// without any event-id bookkeeping.
//
// A sibling rather than a widened signature: nine specs call the drive helper below
// and none of them wants this parameter.
func buildHelloEarlyInteractiveResuming(t *testing.T, token string, lastEventID *uint64) []byte {
	t.Helper()
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       "v2-e2e-phone",
		ClientVersion:    "0.0.1-test",
		ProtocolVersions: []string{"v2"},
		Token:            token,
		Capabilities:     []string{protocol.CapabilityInteractive},
		LastEventID:      lastEventID,
	})
	if err != nil {
		t.Fatalf("marshal interactive hello payload: %v", err)
	}
	envBytes, err := json.Marshal(protocol.Envelope{
		ID:      1,
		Type:    protocol.TypeHello,
		TS:      time.Now().UTC(),
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("marshal interactive hello envelope: %v", err)
	}
	return envBytes
}

// driveHandshakeToOpenDaemonInteractive is driveHandshakeToOpenDaemon
// (relay_v2_daemon_test.go) with a capability-advertising hello. It also asserts
// the hello_ack early data echoes the interactive grant, pinning the precondition
// that this conn is on the structured-stream path (the grant phone A relies on).
func driveHandshakeToOpenDaemonInteractive(t *testing.T, phone *fakephone.Client, pubKey []byte, token string) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	return driveHandshakeToOpenDaemonInteractiveResuming(t, phone, pubKey, token, nil)
}

// driveHandshakeToOpenDaemonInteractiveResuming is
// driveHandshakeToOpenDaemonInteractive with an optional last_event_id on the
// hello, for a phone re-dialing mid-session. It runs a FULL Noise_IK handshake and
// returns a FRESH CipherState pair: v2 has no session resumption, so a caller
// re-dialing must rebind to these and must never carry a prior conn's cipher
// states — reusing them desyncs the AEAD nonce and surfaces as a decrypt failure
// that reads like a daemon bug.
//
// A sibling rather than a widened signature: nine specs call the delegating helper
// above and this ticket must not fan out into them.
func driveHandshakeToOpenDaemonInteractiveResuming(t *testing.T, phone *fakephone.Client, pubKey []byte, token string, lastEventID *uint64) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	initPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("phone keygen: %v", err)
	}
	initiator, err := noise.NewInitiator(initPriv.Bytes(), pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	initMsg, err := initiator.WriteInit(buildHelloEarlyInteractiveResuming(t, token, lastEventID))
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)

	inner := readInnerFrame(t, phone, 3*time.Second)
	if inner.Type != protocol.TypeNoiseResp {
		t.Fatalf("handshake: got inner type %q, want %q", inner.Type, protocol.TypeNoiseResp)
	}
	respRaw, err := base64.StdEncoding.DecodeString(inner.Data)
	if err != nil {
		t.Fatalf("decode noise_resp data: %v", err)
	}
	earlyAck, initSend, initRecv, err := initiator.ReadResp(respRaw)
	if err != nil {
		t.Fatalf("initiator.ReadResp: %v", err)
	}

	var ackEnv protocol.Envelope
	if err := json.Unmarshal(earlyAck, &ackEnv); err != nil {
		t.Fatalf("decode hello_ack envelope: %v", err)
	}
	if ackEnv.Type != protocol.TypeHelloAck {
		t.Fatalf("early-data type = %q, want %q", ackEnv.Type, protocol.TypeHelloAck)
	}
	var ack protocol.HelloAckPayload
	if err := json.Unmarshal(ackEnv.Payload, &ack); err != nil {
		t.Fatalf("decode hello_ack payload: %v", err)
	}
	if !slices.Contains(ack.Capabilities, protocol.CapabilityInteractive) {
		t.Fatalf("daemon did not grant interactive (hello_ack capabilities=%v); test precondition unmet", ack.Capabilities)
	}
	return initSend, initRecv
}
