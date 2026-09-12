//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakephone"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/fakerelay"
	"github.com/pyrycode/pyrycode/internal/e2e/internal/paireddevice"
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

	fr := fakerelay.New(relayTestLogger())
	t.Cleanup(func() { _ = fr.Close() })
	relayURL := fr.URL() + "/v2/server"
	pairPayload, err := paireddevice.Setup(paireddevice.Config{
		Home:         home,
		InstanceName: "test",
		Relay:        relayURL,
		DeviceName:   "phone-a",
	})
	if err != nil {
		t.Fatalf("setup paired device: %v", err)
	}
	pubKey, err := base64.StdEncoding.DecodeString(pairPayload.ServerStaticPubkey)
	if err != nil {
		t.Fatalf("decode server static pubkey: %v", err)
	}

	h := StartInWithEnv(t,
		home,
		[]string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"},
		"-pyry-relay="+relayURL,
	)
	t.Cleanup(func() { h.Stop(t) })

	serverID := readPersistedServerID(t, home)

	// Wait for the binary's relay connection to register.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !fr.WaitBinary(ctx, serverID) {
		t.Fatal("binary connection not registered within 5s")
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	phone, err := fakephone.Dial(dialCtx, fr.URL(), serverID, pairPayload.Token, "phone-a")
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phone.Close() })

	// 1. v2 Noise_IK handshake (the non-interactive hello is embedded in the
	// init frame; no separate hello/hello_ack envelope on the sealed path).
	initSend, initRecv := driveHandshakeToOpenDaemon(t, phone, pubKey, pairPayload.Token)

	// 2. register_push_token → ack (sealed)
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
	reqRaw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal register_push_token envelope: %v", err)
	}
	ct, err := initSend.Encrypt(reqRaw)
	if err != nil {
		t.Fatalf("seal register_push_token envelope: %v", err)
	}
	sendNoiseMsg(t, phone, ct)

	ack := decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)
	if ack.Type != protocol.TypeAck {
		t.Fatalf("ack Type: got %q, want %q (payload=%s)", ack.Type, protocol.TypeAck, string(ack.Payload))
	}
	if ack.InReplyTo == nil || *ack.InReplyTo != reqID {
		t.Errorf("ack InReplyTo: got %v, want pointer to %d", ack.InReplyTo, reqID)
	}
	// v2: the hello is embedded in the Noise handshake, so no hello_ack envelope
	// consumes an outbound id first — the register ack is the daemon's first
	// stamped application reply (id 1). Assert it is stamped (non-zero); the v2
	// daemon round-trip tests likewise assert InReplyTo, not the id magnitude.
	if ack.ID < 1 {
		t.Errorf("ack ID: got %d, want >= 1 (dispatcher-stamped)", ack.ID)
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
