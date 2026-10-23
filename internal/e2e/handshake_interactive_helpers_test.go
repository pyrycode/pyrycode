//go:build e2e

package e2e

import (
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
	payload, err := json.Marshal(protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       "v2-e2e-phone",
		ClientVersion:    "0.0.1-test",
		ProtocolVersions: []string{"v2"},
		Token:            token,
		Capabilities:     []string{protocol.CapabilityInteractive},
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
func driveHandshakeToOpenDaemonInteractive(t *testing.T, phone *fakephone.Client, pubKey []byte, token string, caps ...string) (*noise.CipherState, *noise.CipherState) {
	t.Helper()
	initiator, err := noise.NewInitiator(fakephone.InstallKey(token), pubKey)
	if err != nil {
		t.Fatalf("NewInitiator: %v", err)
	}
	early := buildHelloEarlyInteractive(t, token)
	if len(caps) > 0 {
		var env protocol.Envelope
		var hello protocol.HelloClientPayload
		if err := json.Unmarshal(early, &env); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(env.Payload, &hello); err != nil {
			t.Fatal(err)
		}
		hello.Capabilities = append(hello.Capabilities, caps...)
		env.Payload, err = json.Marshal(hello)
		if err != nil {
			t.Fatal(err)
		}
		early, err = json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
	}
	initMsg, err := initiator.WriteInit(early)
	if err != nil {
		t.Fatalf("WriteInit: %v", err)
	}
	sendNoiseInit(t, phone, initMsg)
	t.Log("interactive handshake: noise_init sent; awaiting noise_resp")

	inner := readInnerFrame(t, phone, 3*time.Second)
	t.Log("interactive handshake: response frame received")
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
	for _, cap := range caps {
		if !slices.Contains(ack.Capabilities, cap) {
			t.Fatalf("daemon did not grant %s", cap)
		}
	}
	return initSend, initRecv
}
