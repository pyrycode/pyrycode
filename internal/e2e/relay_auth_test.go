//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelay_AuthReject_4401 drives the auth-gate reject path end-to-end:
// a phone connects with a token NOT present in the binary's local device
// registry (empty: no `pyry pair` was ever run on this home dir). The
// binary's dispatcher must reply with an `error` envelope (code
// auth.invalid_token) and the phone's WS must close with code 4401.
func TestRelay_AuthReject_4401(t *testing.T) {
	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	home := shortHome(t)
	h := StartInWithEnv(t,
		home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		"-pyry-relay="+fr.URL()+"/v1/server",
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)

	// Wait for the binary↔relay handshake so the binary is ready to
	// receive phone-routed frames.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := fr.LastBinaryHello(serverID); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := fr.LastBinaryHello(serverID); !ok {
		t.Fatal("binary hello not observed within 5s")
	}

	// Dial a phone with an unpaired token. The fakerelay forwards the
	// token to the binary on the first routing envelope; the binary's
	// gate rejects because devices.json is absent (empty registry).
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID,
		"unpaired-token-deadbeef", "test-phone")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	hello := protocol.Envelope{
		ID:   1,
		Type: protocol.TypeHello,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.HelloClientPayload{
			Role:             "client",
			DeviceName:       "test-phone",
			ClientVersion:    "0.0.1-test",
			ProtocolVersions: []string{"v1"},
		}),
	}
	if err := phone.Send(hello); err != nil {
		t.Fatalf("phone send hello: %v", err)
	}

	// Receive #1: the auth.invalid_token error envelope.
	got, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive error envelope: %v", err)
	}
	if got.Type != protocol.TypeError {
		t.Fatalf("got type %q, want %q", got.Type, protocol.TypeError)
	}
	if got.InReplyTo == nil || *got.InReplyTo != 1 {
		t.Errorf("InReplyTo: got %v, want pointer to 1", got.InReplyTo)
	}
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.Code != protocol.CodeAuthInvalidToken {
		t.Errorf("code: got %q, want %q", payload.Code, protocol.CodeAuthInvalidToken)
	}

	// Receive #2: the WS close. fakephone records the CloseStatus on
	// the Read error.
	_, err = phone.Receive(3 * time.Second)
	if err == nil {
		t.Fatal("phone receive: got nil err, want WS close")
	}
	if errors.Is(err, fakephone.ErrReceiveTimeout) {
		t.Fatalf("phone receive timed out without seeing close: %v", err)
	}
	code, ok := phone.LastCloseStatus()
	if !ok {
		t.Fatal("phone LastCloseStatus: not set after close")
	}
	if int(code) != 4401 {
		t.Errorf("WS close code: got %d, want 4401", int(code))
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
