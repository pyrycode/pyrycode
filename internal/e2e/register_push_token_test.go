//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// TestRelay_RegisterPushToken_AckAndPersists drives the
// register_push_token verb end-to-end: pair a device to mint a token,
// boot the daemon against a fakerelay, dial a phone with the paired
// token, send hello → expect hello_ack, send register_push_token →
// expect ack with InReplyTo matching the request id and a dispatcher-
// stamped id ≥ 2. Finally, reload the on-disk registry and assert the
// (Platform, PushToken, Name) triple is persisted.
func TestRelay_RegisterPushToken_AckAndPersists(t *testing.T) {
	home := shortHome(t)

	// Pair a device. The daemon below runs with -pyry-name=test (set in
	// the e2e harness's standard flag set), so pair must write to the
	// same instance dir: <home>/.pyry/test/devices.json.
	r := RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")
	if r.ExitCode != 0 {
		t.Fatalf("pyry pair exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	pairPayload := decodePairPayload(t, r.Stdout)

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })

	h := StartInWithEnv(t,
		home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1"},
		"-pyry-relay="+fr.URL()+"/v1/server",
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)

	// Wait for the binary↔relay handshake.
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

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, pairPayload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	// 1. hello → hello_ack
	hello := protocol.Envelope{
		ID:   1,
		Type: protocol.TypeHello,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.HelloClientPayload{
			Role:             "client",
			DeviceName:       "phone-a",
			ClientVersion:    "0.0.1-test",
			ProtocolVersions: []string{"v1"},
		}),
	}
	if err := phone.Send(hello); err != nil {
		t.Fatalf("phone send hello: %v", err)
	}

	got, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive hello_ack: %v", err)
	}
	if got.Type != protocol.TypeHelloAck {
		t.Fatalf("got type %q, want %q", got.Type, protocol.TypeHelloAck)
	}
	if got.InReplyTo == nil || *got.InReplyTo != 1 {
		t.Errorf("hello_ack InReplyTo: got %v, want pointer to 1", got.InReplyTo)
	}

	// 2. register_push_token → ack
	const (
		wantPlatform  = "fcm"
		wantPushToken = "fcm-token-xyz"
	)
	const reqID uint64 = 2
	req := protocol.Envelope{
		ID:   reqID,
		Type: protocol.TypeRegisterPushToken,
		TS:   time.Now().UTC(),
		Payload: mustJSON(t, protocol.RegisterPushTokenPayload{
			Platform:   wantPlatform,
			Token:      wantPushToken,
			DeviceName: "phone-a",
		}),
	}
	if err := phone.Send(req); err != nil {
		t.Fatalf("phone send register_push_token: %v", err)
	}

	ack, err := phone.Receive(3 * time.Second)
	if err != nil {
		t.Fatalf("phone receive ack: %v", err)
	}
	if ack.Type != protocol.TypeAck {
		t.Fatalf("ack Type: got %q, want %q (payload=%s)", ack.Type, protocol.TypeAck, string(ack.Payload))
	}
	if ack.InReplyTo == nil || *ack.InReplyTo != reqID {
		t.Errorf("ack InReplyTo: got %v, want pointer to %d", ack.InReplyTo, reqID)
	}
	if ack.ID < 2 {
		t.Errorf("ack ID: got %d, want >= 2 (hello_ack consumed id=1)", ack.ID)
	}
	var ackPayload protocol.AckPayload
	if err := json.Unmarshal(ack.Payload, &ackPayload); err != nil {
		t.Fatalf("decode ack payload: %v", err)
	}

	// 3. Persisted on disk?
	registryPath := filepath.Join(home, ".pyry", "test", "devices.json")
	reg, err := devices.Load(registryPath)
	if err != nil {
		t.Fatalf("devices.Load(%q): %v", registryPath, err)
	}
	dev, ok := reg.FindByTokenHash(devices.HashToken(pairPayload.Token))
	if !ok {
		t.Fatalf("device not found in registry after ack; list=%+v", reg.List())
	}
	if dev.Platform != wantPlatform {
		t.Errorf("Platform = %q, want %q", dev.Platform, wantPlatform)
	}
	if dev.PushToken != wantPushToken {
		t.Errorf("PushToken = %q, want %q", dev.PushToken, wantPushToken)
	}
	if dev.Name != "phone-a" {
		t.Errorf("Name = %q, want %q", dev.Name, "phone-a")
	}
}
