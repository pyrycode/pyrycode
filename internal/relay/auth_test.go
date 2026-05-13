package relay

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	testConnID      = "c-test"
	testHelloID     = uint64(42)
	testPlainToken  = "plain-token"
	testServerName  = "test-server"
	testDeviceName  = "Pixel"
)

// makeHelloRouting builds a RoutingEnvelope wrapping a hello frame with
// the supplied conn_id and hello envelope id. Used by every test below.
func makeHelloRouting(t *testing.T, connID string, helloID uint64) protocol.RoutingEnvelope {
	t.Helper()
	helloPayload := protocol.HelloClientPayload{
		Role:             "client",
		DeviceName:       "phone",
		ClientVersion:    "v1",
		ProtocolVersions: []string{"v1"},
	}
	payloadJSON, err := json.Marshal(helloPayload)
	if err != nil {
		t.Fatalf("marshal hello payload: %v", err)
	}
	env := protocol.Envelope{
		ID:      helloID,
		Type:    protocol.TypeHello,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal hello envelope: %v", err)
	}
	return protocol.RoutingEnvelope{
		ConnID: connID,
		Frame:  envJSON,
	}
}

func pairedRegistry(t *testing.T) (*devices.Registry, time.Time) {
	t.Helper()
	reg := &devices.Registry{}
	pastSeen := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	reg.Add(devices.Device{
		TokenHash:  devices.HashToken(testPlainToken),
		Name:       testDeviceName,
		PairedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: pastSeen,
	})
	return reg, pastSeen
}

func TestAuthenticateFirstFrame_ValidToken(t *testing.T) {
	t.Parallel()
	reg, pastSeen := pairedRegistry(t)
	env := makeHelloRouting(t, testConnID, testHelloID)

	outcome, err := AuthenticateFirstFrame(env, testPlainToken, reg, testServerName, testLogger(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.CloseConn {
		t.Errorf("CloseConn: got true, want false")
	}
	if outcome.Response.ConnID != testConnID {
		t.Errorf("Response.ConnID: got %q, want %q", outcome.Response.ConnID, testConnID)
	}

	var respEnv protocol.Envelope
	if err := json.Unmarshal(outcome.Response.Frame, &respEnv); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if respEnv.Type != protocol.TypeHelloAck {
		t.Errorf("Type: got %q, want %q", respEnv.Type, protocol.TypeHelloAck)
	}
	if respEnv.ID != 1 {
		t.Errorf("ID: got %d, want 1", respEnv.ID)
	}
	if respEnv.InReplyTo == nil || *respEnv.InReplyTo != testHelloID {
		t.Errorf("InReplyTo: got %v, want pointer to %d", respEnv.InReplyTo, testHelloID)
	}

	var payload protocol.HelloAckPayload
	if err := json.Unmarshal(respEnv.Payload, &payload); err != nil {
		t.Fatalf("unmarshal hello_ack payload: %v", err)
	}
	if payload.ProtocolVersion != "v1" {
		t.Errorf("ProtocolVersion: got %q, want %q", payload.ProtocolVersion, "v1")
	}
	if payload.ServerID != testServerName {
		t.Errorf("ServerID: got %q, want %q", payload.ServerID, testServerName)
	}
	if payload.ConnID != testConnID {
		t.Errorf("ConnID: got %q, want %q", payload.ConnID, testConnID)
	}

	got, ok := reg.FindByTokenHash(devices.HashToken(testPlainToken))
	if !ok {
		t.Fatalf("device not found after Validate")
	}
	if !got.LastSeenAt.After(pastSeen) {
		t.Errorf("LastSeenAt: got %v, want after %v", got.LastSeenAt, pastSeen)
	}

	if outcome.Device == nil {
		t.Fatalf("Device: got nil, want matched *devices.Device on accept")
	}
	if outcome.Device.Name != testDeviceName {
		t.Errorf("Device.Name: got %q, want %q", outcome.Device.Name, testDeviceName)
	}
}

func TestAuthenticateFirstFrame_UnknownToken(t *testing.T) {
	t.Parallel()
	reg := &devices.Registry{}
	env := makeHelloRouting(t, testConnID, testHelloID)

	outcome, err := AuthenticateFirstFrame(env, "never-paired", reg, testServerName, testLogger(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRejectOutcome(t, outcome)
}

func TestAuthenticateFirstFrame_RevokedTokenSameUX(t *testing.T) {
	t.Parallel()
	reg, _ := pairedRegistry(t)
	if !reg.Remove(testDeviceName) {
		t.Fatalf("Remove(%q) returned false", testDeviceName)
	}
	env := makeHelloRouting(t, testConnID, testHelloID)

	outcome, err := AuthenticateFirstFrame(env, testPlainToken, reg, testServerName, testLogger(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRejectOutcome(t, outcome)
}

func TestAuthenticateFirstFrame_EmptyToken(t *testing.T) {
	t.Parallel()
	reg, _ := pairedRegistry(t)
	env := makeHelloRouting(t, testConnID, testHelloID)

	outcome, err := AuthenticateFirstFrame(env, "", reg, testServerName, testLogger(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRejectOutcome(t, outcome)
}

func TestAuthenticateFirstFrame_MalformedHelloFrame(t *testing.T) {
	t.Parallel()
	reg := &devices.Registry{}
	env := protocol.RoutingEnvelope{
		ConnID: testConnID,
		Frame:  []byte("not-json"),
	}

	outcome, err := AuthenticateFirstFrame(env, testPlainToken, reg, testServerName, testLogger(t))
	if !errors.Is(err, ErrMalformedHelloFrame) {
		t.Fatalf("err: got %v, want ErrMalformedHelloFrame", err)
	}
	if outcome.CloseConn || outcome.Response.ConnID != "" || outcome.Response.Frame != nil {
		t.Errorf("outcome: got %+v, want zero value", outcome)
	}
	if outcome.Device != nil {
		t.Errorf("Device: got %+v, want nil on malformed", outcome.Device)
	}
}

func TestStatusUnauthorized_Value(t *testing.T) {
	t.Parallel()
	if StatusUnauthorized != websocket.StatusCode(4401) {
		t.Errorf("StatusUnauthorized: got %d, want 4401", StatusUnauthorized)
	}
}

func assertRejectOutcome(t *testing.T, outcome AuthOutcome) {
	t.Helper()
	if !outcome.CloseConn {
		t.Errorf("CloseConn: got false, want true")
	}
	if outcome.Response.ConnID != testConnID {
		t.Errorf("Response.ConnID: got %q, want %q", outcome.Response.ConnID, testConnID)
	}

	var respEnv protocol.Envelope
	if err := json.Unmarshal(outcome.Response.Frame, &respEnv); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if respEnv.Type != protocol.TypeError {
		t.Errorf("Type: got %q, want %q", respEnv.Type, protocol.TypeError)
	}
	if respEnv.ID != 1 {
		t.Errorf("ID: got %d, want 1", respEnv.ID)
	}
	if respEnv.InReplyTo == nil || *respEnv.InReplyTo != testHelloID {
		t.Errorf("InReplyTo: got %v, want pointer to %d", respEnv.InReplyTo, testHelloID)
	}

	var payload protocol.ErrorPayload
	if err := json.Unmarshal(respEnv.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeAuthInvalidToken {
		t.Errorf("Code: got %q, want %q", payload.Code, protocol.CodeAuthInvalidToken)
	}
	if payload.Message != MsgInvalidToken {
		t.Errorf("Message: got %q, want %q", payload.Message, MsgInvalidToken)
	}
	if payload.Retryable {
		t.Errorf("Retryable: got true, want false")
	}
	if payload.RetryAfterS != nil {
		t.Errorf("RetryAfterS: got %v, want nil", payload.RetryAfterS)
	}
	if outcome.Device != nil {
		t.Errorf("Device: got %+v, want nil on reject", outcome.Device)
	}
}
