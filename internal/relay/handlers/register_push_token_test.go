package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

const (
	testConnID     = "c-test"
	testRequestID  = uint64(8)
	testNextID     = uint64(2)
	testPlainToken = "plain-token"
	testPlatform   = "fcm"
	testPushToken  = "fcm-token-abc"
	testDeviceName = "Juhana's Pixel 8"
)

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func makeRegisterRouting(t *testing.T, payload protocol.RegisterPushTokenPayload) protocol.RoutingEnvelope {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := protocol.Envelope{
		ID:      testRequestID,
		Type:    protocol.TypeRegisterPushToken,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return protocol.RoutingEnvelope{ConnID: testConnID, Frame: envJSON}
}

// freshRegistryWithDevice seeds a Registry with d and returns a
// registryPath inside t.TempDir() (writable by default; the file does
// not yet exist).
func freshRegistryWithDevice(t *testing.T, d devices.Device) (*devices.Registry, string) {
	t.Helper()
	r := &devices.Registry{}
	r.Add(d)
	path := filepath.Join(t.TempDir(), "devices.json")
	return r, path
}

// assertEnvelopeShape decodes resp.Frame as a protocol.Envelope and
// verifies id, in_reply_to, and type.
func assertEnvelopeShape(t *testing.T, resp protocol.RoutingEnvelope, wantType string) protocol.Envelope {
	t.Helper()
	if resp.ConnID != testConnID {
		t.Errorf("Response.ConnID = %q, want %q", resp.ConnID, testConnID)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(resp.Frame, &env); err != nil {
		t.Fatalf("unmarshal response envelope: %v", err)
	}
	if env.Type != wantType {
		t.Errorf("Type = %q, want %q", env.Type, wantType)
	}
	if env.ID != testNextID {
		t.Errorf("ID = %d, want %d", env.ID, testNextID)
	}
	if env.InReplyTo == nil || *env.InReplyTo != testRequestID {
		t.Errorf("InReplyTo = %v, want pointer to %d", env.InReplyTo, testRequestID)
	}
	return env
}

func TestHandle_FirstTimeRegister_WritesAndAcks(t *testing.T) {
	t.Parallel()
	d := devices.Device{
		TokenHash:  devices.HashToken(testPlainToken),
		Name:       "old-name",
		PairedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	reg, path := freshRegistryWithDevice(t, d)
	snapshot := d

	routing := makeRegisterRouting(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: testDeviceName,
	})

	resp, err := Handle(routing, &snapshot, reg, path, testNextID, testLogger(t))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertEnvelopeShape(t, resp, protocol.TypeAck)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected registry file to exist after first register: %v", err)
	}
	back, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := back.FindByTokenHash(devices.HashToken(testPlainToken))
	if !ok {
		t.Fatal("device missing from reloaded registry")
	}
	if got.Platform != testPlatform || got.PushToken != testPushToken || got.Name != testDeviceName {
		t.Errorf("reloaded device = %+v, want Platform=%q PushToken=%q Name=%q",
			got, testPlatform, testPushToken, testDeviceName)
	}
}

func TestHandle_ReregisterIdentical_NoWriteAndAcks(t *testing.T) {
	t.Parallel()
	d := devices.Device{
		TokenHash:  devices.HashToken(testPlainToken),
		Name:       testDeviceName,
		Platform:   testPlatform,
		PushToken:  testPushToken,
		PairedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	reg, path := freshRegistryWithDevice(t, d)
	snapshot := d

	routing := makeRegisterRouting(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: testDeviceName,
	})

	resp, err := Handle(routing, &snapshot, reg, path, testNextID, testLogger(t))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertEnvelopeShape(t, resp, protocol.TypeAck)

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected registry file to NOT exist (dedupe path skips Save); stat err = %v", err)
	}
}

func TestHandle_ReregisterChanged_WritesAndAcks(t *testing.T) {
	t.Parallel()
	d := devices.Device{
		TokenHash:  devices.HashToken(testPlainToken),
		Name:       "phone",
		Platform:   "fcm",
		PushToken:  "old-fcm",
		PairedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	reg, path := freshRegistryWithDevice(t, d)
	if err := reg.Save(path); err != nil {
		t.Fatalf("Save initial: %v", err)
	}
	snapshot := d

	routing := makeRegisterRouting(t, protocol.RegisterPushTokenPayload{
		Platform:   "fcm",
		Token:      "new-fcm",
		DeviceName: "phone",
	})

	resp, err := Handle(routing, &snapshot, reg, path, testNextID, testLogger(t))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertEnvelopeShape(t, resp, protocol.TypeAck)

	back, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := back.FindByTokenHash(devices.HashToken(testPlainToken))
	if !ok {
		t.Fatal("device missing from reloaded registry")
	}
	if got.PushToken != "new-fcm" {
		t.Errorf("PushToken = %q, want %q", got.PushToken, "new-fcm")
	}
}

func TestHandle_SaveFailure_EmitsServerBinaryBusy(t *testing.T) {
	t.Parallel()
	d := devices.Device{
		TokenHash:  devices.HashToken(testPlainToken),
		Name:       "phone",
		Platform:   "fcm",
		PushToken:  "old-fcm",
		PairedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	reg := &devices.Registry{}
	reg.Add(d)
	snapshot := d

	// Block Save: a regular file at the parent path makes MkdirAll fail.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("file-not-dir"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	registryPath := filepath.Join(blocker, "devices.json")

	routing := makeRegisterRouting(t, protocol.RegisterPushTokenPayload{
		Platform:   "fcm",
		Token:      "new-fcm",
		DeviceName: "phone",
	})

	resp, err := Handle(routing, &snapshot, reg, registryPath, testNextID, testLogger(t))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	env := assertEnvelopeShape(t, resp, protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeServerBinaryBusy {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeServerBinaryBusy)
	}
	if !payload.Retryable {
		t.Errorf("Retryable = false, want true")
	}
	if payload.RetryAfterS != nil {
		t.Errorf("RetryAfterS = %v, want nil", payload.RetryAfterS)
	}

	// In-memory state IS mutated despite the disk failure (documented
	// post-condition: in-memory is the runtime source of truth).
	got, ok := reg.FindByTokenHash(devices.HashToken(testPlainToken))
	if !ok {
		t.Fatal("device missing from in-memory registry")
	}
	if got.PushToken != "new-fcm" {
		t.Errorf("in-memory PushToken = %q, want %q (save failure must not roll back memory)",
			got.PushToken, "new-fcm")
	}
}

func TestHandle_UnauthenticatedConn_EmitsAuthInvalidTokenNoWrite(t *testing.T) {
	t.Parallel()
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken("unrelated"),
		Name:      "other",
	})
	path := filepath.Join(t.TempDir(), "devices.json")

	before := len(reg.List())

	routing := makeRegisterRouting(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: testDeviceName,
	})

	resp, err := Handle(routing, nil, reg, path, testNextID, testLogger(t))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	env := assertEnvelopeShape(t, resp, protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeAuthInvalidToken {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeAuthInvalidToken)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected registry file to NOT exist after unauth reject; stat err = %v", err)
	}
	if got := len(reg.List()); got != before {
		t.Errorf("in-memory device count = %d, want %d (unauth must not mutate registry)", got, before)
	}
}

func TestHandle_MalformedFrame_ReturnsSentinel(t *testing.T) {
	t.Parallel()
	routing := protocol.RoutingEnvelope{ConnID: testConnID, Frame: []byte("not-json")}
	resp, err := Handle(routing, nil, &devices.Registry{}, "", testNextID, testLogger(t))
	if !errors.Is(err, ErrMalformedFrame) {
		t.Errorf("err = %v, want ErrMalformedFrame", err)
	}
	if !equalRouting(resp, protocol.RoutingEnvelope{}) {
		t.Errorf("resp = %+v, want zero RoutingEnvelope", resp)
	}
}

func equalRouting(a, b protocol.RoutingEnvelope) bool {
	return a.ConnID == b.ConnID && len(a.Frame) == 0 && len(b.Frame) == 0
}
