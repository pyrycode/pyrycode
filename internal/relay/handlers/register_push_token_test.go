package handlers

import (
	"context"
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
	"github.com/pyrycode/pyrycode/internal/dispatch"
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

// newTestConn returns a *dispatch.Conn whose outbound channel feeds a
// recv helper. The conn's NextID is advanced past id=1 (mirroring the
// gate's hello_ack accounting) so the first handler-originated reply
// observes id=2.
func newTestConn(t *testing.T, dev *devices.Device) (*dispatch.Conn, func() protocol.RoutingEnvelope) {
	t.Helper()
	out := make(chan protocol.RoutingEnvelope, 4)
	c := dispatch.NewTestConn(testConnID, out, dev)
	_ = c.NextID()
	recv := func() protocol.RoutingEnvelope {
		t.Helper()
		select {
		case env := <-out:
			return env
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for outbound envelope")
			return protocol.RoutingEnvelope{}
		}
	}
	return c, recv
}

// makeRequest builds the protocol.Envelope that the dispatcher would
// hand to the handler (already-decoded; the routing envelope is the
// dispatcher's concern).
func makeRequest(t *testing.T, payload any) protocol.Envelope {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return protocol.Envelope{
		ID:      testRequestID,
		Type:    protocol.TypeRegisterPushToken,
		TS:      time.Now().UTC(),
		Payload: payloadJSON,
	}
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

func TestRegisterPushToken_FirstTimeRegister_WritesAndAcks(t *testing.T) {
	t.Parallel()
	d := devices.Device{
		TokenHash:  devices.HashToken(testPlainToken),
		Name:       "old-name",
		PairedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	reg, path := freshRegistryWithDevice(t, d)
	// Persist [A] to disk first: post-#782 the handler reloads devices.json
	// before its Save, mirroring production where the registry was Load'ed
	// from an existing file (a successful handshake — which reloads the same
	// file — always precedes register_push_token). Without an on-disk file the
	// reload would, per the ENOENT->empty contract, correctly reconcile
	// membership to empty.
	if err := reg.Save(path); err != nil {
		t.Fatalf("Save seed [A]: %v", err)
	}
	snapshot := d
	c, recv := newTestConn(t, &snapshot)

	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: testDeviceName,
	})

	h := RegisterPushToken(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertEnvelopeShape(t, recv(), protocol.TypeAck)

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

func TestRegisterPushToken_ReregisterIdentical_NoWriteAndAcks(t *testing.T) {
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
	c, recv := newTestConn(t, &snapshot)

	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: testDeviceName,
	})

	h := RegisterPushToken(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertEnvelopeShape(t, recv(), protocol.TypeAck)

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected registry file to NOT exist (dedupe path skips Save); stat err = %v", err)
	}
}

func TestRegisterPushToken_ReregisterChanged_WritesAndAcks(t *testing.T) {
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
	c, recv := newTestConn(t, &snapshot)

	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   "fcm",
		Token:      "new-fcm",
		DeviceName: "phone",
	})

	h := RegisterPushToken(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertEnvelopeShape(t, recv(), protocol.TypeAck)

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

// TestRegisterPushToken_ReloadPreventsClobberOfNewlyPairedDevice is the
// core data-loss guard (#782): device A (authed since startup) sends
// register_push_token before device B — added to devices.json by a separate
// `pyry pair` process after startup — ever handshakes. The handler must
// reload disk into memory before its whole-file Save, or the write erases B.
func TestRegisterPushToken_ReloadPreventsClobberOfNewlyPairedDevice(t *testing.T) {
	t.Parallel()
	a := devices.Device{
		TokenHash:  devices.HashToken("plain-a"),
		Name:       "device-a",
		PairedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	// Daemon in-memory registry holds only [A] (the startup snapshot).
	reg := &devices.Registry{}
	reg.Add(a)
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := reg.Save(path); err != nil {
		t.Fatalf("Save [A]: %v", err)
	}

	// `pyry pair` (a separate process) appends B to the same file.
	b := devices.Device{
		TokenHash:  devices.HashToken("plain-b"),
		Name:       "device-b",
		PairedAt:   time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC),
	}
	diskReg := &devices.Registry{}
	diskReg.Add(a)
	diskReg.Add(b)
	if err := diskReg.Save(path); err != nil {
		t.Fatalf("Save [A,B]: %v", err)
	}

	snapshot := a
	c, recv := newTestConn(t, &snapshot)
	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: "device-a",
	})

	h := RegisterPushToken(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertEnvelopeShape(t, recv(), protocol.TypeAck)

	back, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := back.FindByTokenHash(devices.HashToken("plain-b")); !ok {
		t.Fatal("device B erased by A's register_push_token write (clobber regression)")
	}
	gotA, ok := back.FindByTokenHash(devices.HashToken("plain-a"))
	if !ok {
		t.Fatal("device A missing after write")
	}
	if gotA.PushToken != testPushToken {
		t.Errorf("A.PushToken = %q, want %q", gotA.PushToken, testPushToken)
	}
}

func TestRegisterPushToken_GoneMidConn_EmitsAuthInvalidToken(t *testing.T) {
	t.Parallel()
	// Registry is empty; the snapshot device's TokenHash is not present,
	// so UpdatePushRegistration returns false.
	reg := &devices.Registry{}
	path := filepath.Join(t.TempDir(), "devices.json")
	snapshot := devices.Device{
		TokenHash: devices.HashToken(testPlainToken),
		Name:      "phone",
	}
	c, recv := newTestConn(t, &snapshot)

	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: testDeviceName,
	})

	h := RegisterPushToken(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertEnvelopeShape(t, recv(), protocol.TypeError)
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
}

func TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy(t *testing.T) {
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
	c, recv := newTestConn(t, &snapshot)

	// Block Save: a regular file at the parent path makes MkdirAll fail.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("file-not-dir"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	registryPath := filepath.Join(blocker, "devices.json")

	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   "fcm",
		Token:      "new-fcm",
		DeviceName: "phone",
	})

	h := RegisterPushToken(reg, registryPath, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertEnvelopeShape(t, recv(), protocol.TypeError)
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

func TestRegisterPushToken_UnauthenticatedConn_EmitsAuthInvalidTokenNoWrite(t *testing.T) {
	t.Parallel()
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken("unrelated"),
		Name:      "other",
	})
	path := filepath.Join(t.TempDir(), "devices.json")
	before := len(reg.List())

	c, recv := newTestConn(t, nil)

	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: testDeviceName,
	})

	h := RegisterPushToken(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertEnvelopeShape(t, recv(), protocol.TypeError)
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

func TestRegisterPushToken_MalformedPayload_EmitsProtocolMalformed(t *testing.T) {
	t.Parallel()
	d := devices.Device{
		TokenHash: devices.HashToken(testPlainToken),
		Name:      "phone",
	}
	reg := &devices.Registry{}
	reg.Add(d)
	path := filepath.Join(t.TempDir(), "devices.json")
	snapshot := d
	c, recv := newTestConn(t, &snapshot)

	req := protocol.Envelope{
		ID:      testRequestID,
		Type:    protocol.TypeRegisterPushToken,
		TS:      time.Now().UTC(),
		Payload: []byte("not-json"),
	}

	h := RegisterPushToken(reg, path, testLogger(t))
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}
	env := assertEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeProtocolMalformed {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeProtocolMalformed)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected registry file to NOT exist after malformed reject; stat err = %v", err)
	}
}
