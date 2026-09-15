package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// pushLockGrace is how long an interleaving test waits before concluding that the
// handler is genuinely parked on the devices lock rather than merely slow. Mirrors
// mintBlockGrace in cmd/pyry/pair_lock_test.go.
const pushLockGrace = 150 * time.Millisecond

// holdPushLock acquires the devices lock for path on a background goroutine and
// returns once the critical section is entered, so a test can race the handler
// against a live holder deterministically. flock(2) contends per open file
// description rather than per process, so an in-process holder is a real
// contender for a handler running in this same binary — the interleaving needs no
// second OS process and no daemon harness.
//
// The returned release ends the region and waits for the holder to unwind. It is
// idempotent and also runs at cleanup, so a t.Fatalf before the explicit call
// cannot strand the goroutine. Both waits are bounded: a holder that never enters,
// or never unwinds, reports a failure instead of hanging the package. Mirrors
// holdPairLock in cmd/pyry.
func holdPushLock(t *testing.T, path string) (release func()) {
	t.Helper()
	held := make(chan struct{})
	rel := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- devices.WithLock(path, devices.DefaultLockWait, func() error {
			close(held)
			<-rel
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("holder never entered the region for %s: %v", path, err)
	}

	var once sync.Once
	release = func() {
		once.Do(func() {
			close(rel)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("holder WithLock(%s): %v", path, err)
				}
			case <-time.After(devices.DefaultLockWait):
				t.Errorf("holder for %s did not unwind within %v", path, devices.DefaultLockWait)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// raisePushLockWait widens the handler's acquisition bound for the duration of the
// test. An interleaving test has to keep the handler parked for longer than its
// grace, and the production bound is deliberately shorter than that.
//
// SAFE DESPITE THE SHARED VAR: every test that calls this is deliberately NOT
// t.Parallel(), and Go resumes a package's parallel tests only once its sequential
// tests have finished — so no parallel reader of pushRegistryLockWait ever overlaps
// this write, and the cleanup restores the production value before any resumes.
func raisePushLockWait(t *testing.T) {
	t.Helper()
	orig := pushRegistryLockWait
	pushRegistryLockWait = devices.DefaultLockWait
	t.Cleanup(func() { pushRegistryLockWait = orig })
}

// backdatePush stamps path with a distinctly old mtime and returns it. Save
// commits by renaming a freshly created temp file over the target, so a Save
// always installs a current mtime — which makes an unchanged old one proof that no
// Save ran, rather than the vacuous observation that the bytes happen to match
// what an idempotent rewrite would have produced.
func backdatePush(t *testing.T, path string) time.Time {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
	return pushModTime(t, path)
}

func pushModTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	return fi.ModTime()
}

func assertPushNotRewritten(t *testing.T, path string, want time.Time) {
	t.Helper()
	if got := pushModTime(t, path); !got.Equal(want) {
		t.Errorf("devices.json mtime = %v, want the untouched %v — a Save ran", got, want)
	}
}

// assertNoSidecar is the "the locked region was never entered" witness. WithLock
// creates path+".lock" before it runs anything the caller passed it, so the
// sidecar's continued absence is a strictly stronger claim than "no Save ran" — it
// needs no seam, no counter and no fake, and it is what pins that the dedupe and
// display-safety fast paths stay outside the region.
func assertNoSidecar(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock sidecar %s.lock exists (stat err = %v); this path must acquire no lock", path, err)
	}
}

// capturePushLogger returns a logger writing into a buffer plus a reader for what
// it collected, for the two assertions that are about what a branch LOGGED rather
// than what it replied: the event that tells a busy lock from a save failure (the
// replies are deliberately identical), and the no-leak rule that no branch may name
// the device token or its hash. No mutex: every caller reads the buffer only after
// the handler call it captures has returned.
func capturePushLogger() (*slog.Logger, func() string) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf.String
}

// TestRegisterPushToken_SurvivesWriteCommittedWhileParkedOnLock is this ticket's
// central interleaving, and it covers both directions of the race in one run: the
// writer that commits while the handler is parked on the lock adds device B and
// revokes device C, and the handler's save may undo neither.
//
// A BUSY-LOCK TEST CANNOT REPLACE THIS. A build that wraps only the existing Save
// in WithLock genuinely refuses when busy and passes every contention assertion,
// while still reading its snapshot before the lock was ever taken — so its save
// silently erases B. Only an interleaving assertion tells the two apart; #1531 hit
// exactly that mutant, and devices-registry.md § Testing a best-effort,
// lock-guarded persist records it.
//
// Not t.Parallel(): it retunes pushRegistryLockWait and depends on a grace.
func TestRegisterPushToken_SurvivesWriteCommittedWhileParkedOnLock(t *testing.T) {
	raisePushLockWait(t)

	a := devices.Device{
		TokenHash:  devices.HashToken(testPlainToken),
		Name:       "device-a",
		PairedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	devC := devices.Device{
		TokenHash: devices.HashToken("plain-c"),
		Name:      "device-c",
		PairedAt:  time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	// The daemon's long-lived registry and disk both start at [A, C].
	reg := &devices.Registry{}
	reg.Add(a)
	reg.Add(devC)
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := reg.Save(path); err != nil {
		t.Fatalf("Save [A,C]: %v", err)
	}

	release := holdPushLock(t, path)

	snapshot := a
	conn, recv := newTestConn(t, &snapshot)
	h := RegisterPushToken(reg, path, testLogger(t))
	done := make(chan error, 1)
	go func() {
		done <- h(context.Background(), conn, makeRequest(t, protocol.RegisterPushTokenPayload{
			Platform:   testPlatform,
			Token:      testPushToken,
			DeviceName: "device-a",
		}))
	}()

	select {
	case err := <-done:
		t.Fatalf("handler completed while another holder had the devices lock (err=%v); its reconcile-and-save is not inside a locked region", err)
	case <-time.After(pushLockGrace):
	}

	// The racing writer commits [A, B] from under the holder: B is newly paired
	// and C is revoked. Both changes are invisible to the registry the parked
	// handler holds in memory.
	b := devices.Device{
		TokenHash: devices.HashToken("plain-b"),
		Name:      "device-b",
		PairedAt:  time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC),
	}
	racer := &devices.Registry{}
	racer.Add(a)
	racer.Add(b)
	if err := racer.Save(path); err != nil {
		t.Fatalf("Save [A,B]: %v", err)
	}
	release()

	if err := <-done; err != nil {
		t.Fatalf("handler: %v", err)
	}
	assertEnvelopeShape(t, recv(), protocol.TypeAck)

	back, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gotA, ok := back.FindByTokenHash(devices.HashToken(testPlainToken))
	if !ok {
		t.Fatal("device A missing from disk after its own register_push_token")
	}
	if gotA.Platform != testPlatform || gotA.PushToken != testPushToken {
		t.Errorf("A = Platform %q PushToken %q, want %q/%q — the registration was not persisted",
			gotA.Platform, gotA.PushToken, testPlatform, testPushToken)
	}
	if _, ok := back.FindByTokenHash(devices.HashToken("plain-b")); !ok {
		t.Error("device B, paired while the handler was parked on the lock, was erased by its save")
	}
	if _, ok := back.FindByTokenHash(devices.HashToken("plain-c")); ok {
		t.Error("device C, revoked while the handler was parked on the lock, was resurrected by its save")
	}
}

// TestRegisterPushToken_ReconcileDropsDevice_RefusedNotAcked is AC-3, and the one
// test that can tell the two operation orderings apart. This conn's device was
// revoked on disk after it authenticated, so the in-region reconcile drops it and
// the mutation reports no match: the frame is refused on the existing
// non-retryable auth.invalid_token and no row is written. Under the old
// mutate-then-reconcile ordering the very same input is ACKED and the survivor set
// rewritten, which is the free guarantee this ordering buys — the same one
// ClearRedeemBy gets from it.
//
// It needs no lock holder: the revocation has already committed, so the race is
// over by the time the handler runs. Not t.Parallel(), because it reads
// pushRegistryLockWait, which the interleaving test retunes.
func TestRegisterPushToken_ReconcileDropsDevice_RefusedNotAcked(t *testing.T) {
	a := devices.Device{
		TokenHash: devices.HashToken(testPlainToken),
		Name:      "device-a",
		PairedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	b := devices.Device{
		TokenHash: devices.HashToken("plain-b"),
		Name:      "device-b",
		PairedAt:  time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC),
	}
	// Memory is the daemon's startup snapshot [A, B]; `pyry pair revoke` has
	// since committed [B] alone.
	reg := &devices.Registry{}
	reg.Add(a)
	reg.Add(b)
	path := filepath.Join(t.TempDir(), "devices.json")
	onDisk := &devices.Registry{}
	onDisk.Add(b)
	if err := onDisk.Save(path); err != nil {
		t.Fatalf("Save [B]: %v", err)
	}
	before := backdatePush(t, path)

	snapshot := a
	conn, recv := newTestConn(t, &snapshot)
	h := RegisterPushToken(reg, path, testLogger(t))
	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: "device-a",
	})
	if err := h(context.Background(), conn, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeAuthInvalidToken {
		t.Errorf("Code = %q, want %q — a device the reconcile dropped must be refused, not acked", payload.Code, protocol.CodeAuthInvalidToken)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false")
	}

	assertPushNotRewritten(t, path, before)
	back, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := back.FindByTokenHash(devices.HashToken(testPlainToken)); ok {
		t.Error("the revoked device was written back to disk by the refused frame")
	}
}

// TestRegisterPushToken_LockBusy_EmitsServerBinaryBusyWithoutWriting is AC-4: a
// lock this handler cannot acquire within its bound is surfaced on the existing
// retryable server.binary_busy reply rather than skipped to write unlocked.
//
// The busy branch's REPLY is deliberately identical to a save failure's — a phone
// can act on neither distinction — so the only place the two are told apart is the
// log event, which is why this asserts on captured output. The same capture pins
// AC-4's no-leak half directly: neither the plain token nor its hash may appear
// anywhere the handler logged.
//
// Not t.Parallel(): it holds a real flock and reads pushRegistryLockWait.
func TestRegisterPushToken_LockBusy_EmitsServerBinaryBusyWithoutWriting(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := reg.Save(path); err != nil {
		t.Fatalf("Save seed: %v", err)
	}
	before := backdatePush(t, path)

	holdPushLock(t, path)

	logger, logged := capturePushLogger()
	snapshot := d
	conn, recv := newTestConn(t, &snapshot)
	h := RegisterPushToken(reg, path, logger)
	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   "fcm",
		Token:      "new-fcm",
		DeviceName: "phone",
	})
	if err := h(context.Background(), conn, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeServerBinaryBusy {
		t.Errorf("Code = %q, want %q — a busy lock must refuse, never fall through to an unlocked write", payload.Code, protocol.CodeServerBinaryBusy)
	}
	if !payload.Retryable {
		t.Errorf("Retryable = false, want true")
	}
	assertPushNotRewritten(t, path, before)

	out := logged()
	if !strings.Contains(out, "register_push_token.lock_busy") {
		t.Errorf("log %q carries no lock_busy event; the replies are identical, so the event is the only place a busy lock is told from a save failure", out)
	}
	if strings.Contains(out, testPlainToken) {
		t.Errorf("log %q names the plain device token", out)
	}
	if strings.Contains(out, devices.HashToken(testPlainToken)) {
		t.Errorf("log %q names the device token hash", out)
	}
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
	// The dedupe fast path stays OUTSIDE the locked region, so a deduped frame —
	// which the phone sends on every WS connect — costs no acquisition at all.
	assertNoSidecar(t, path)
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

// TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy pins the disk-failure
// reply and the documented post-condition that memory is NOT rolled back with it.
//
// THE BLOCKER IS BUILT SO THE FAILURE LANDS ON Save, which is the whole of #1532's
// repair to this test. Making the registry's parent unopenable is not enough on its
// own once the persist runs under a lock: WithLock does its MkdirAll and opens its
// sidecar BEFORE it runs anything the caller passed it, so a parent that blocks
// those fails a layer early and the test proves nothing about Save — it stays green
// while asserting a reply that no longer comes from where it claims. Seeding a real
// devices.json, pre-creating the sidecar at 0600 and only then dropping the
// directory to 0500 puts the acquisition back inside reach (O_CREATE on an existing
// file needs only x on the directory) and the EACCES back on Save's temp file. The
// assertion on Save's own step word is what keeps it there.
func TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("posix-only permission test")
	}
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

	dir := t.TempDir()
	registryPath := filepath.Join(dir, "devices.json")
	// A real on-disk registry, so the in-region reconcile finds the device rather
	// than reconciling membership to empty and refusing before Save is reached.
	if err := reg.Save(registryPath); err != nil {
		t.Fatalf("Save seed: %v", err)
	}
	if err := os.WriteFile(registryPath+".lock", nil, 0o600); err != nil {
		t.Fatalf("pre-create sidecar: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Pre-flight: ensure 0500 actually blocks writes for this user (root bypasses
	// DAC). If a probe write succeeds, Save will too — skip.
	probe := filepath.Join(dir, ".probe.tmp")
	if f, perr := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600); perr == nil {
		_ = f.Close()
		_ = os.Remove(probe)
		t.Skip("chmod 0500 did not block writes for this user (running as root?)")
	}

	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   "fcm",
		Token:      "new-fcm",
		DeviceName: "phone",
	})

	logger, logged := capturePushLogger()
	h := RegisterPushToken(reg, registryPath, logger)
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

	// The anti-hollow-out assertion: "registry: create temp" is Save's own step
	// word, so its presence proves the lock was acquired and the region entered.
	// A failure at the sidecar open would name "devices: open lock" instead and
	// satisfy every assertion above while testing nothing this test is named for.
	if out := logged(); !strings.Contains(out, "registry: create temp") {
		t.Errorf("log %q names no Save step; the failure landed before Save, which is how this test was hollowed out", out)
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
	assertNoSidecar(t, path)
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
	assertNoSidecar(t, path)
}

// pushStoredName, pushStoredPlatform and pushStoredPush are the seeded triple every
// display-safety test below asserts is STILL on the device after a reject. They
// are deliberately distinct from testDeviceName / testPlatform / testPushToken so
// a mutation that wrote the payload's values through would be visible rather than
// coincidentally equal to what was already there.
const (
	pushStoredName     = "seeded-name"
	pushStoredPlatform = "apns"
	pushStoredPush     = "apns-token-seed"
)

// assertRejectedNoWrite is the shared assertion for every display-safety reject:
// the reply is a non-retryable protocol.malformed error, the seeded device is
// unmutated in memory, and devices.json was never created.
//
// IT ALSO ASSERTS THE MESSAGE DOES NOT ECHO THE REFUSED VALUE, which is the
// on-the-wire half of this ticket's no-leak rule and the reason the check is a
// helper rather than four copies: a reply that quoted the bytes would hand a
// display-forgery payload to whatever renders the error, which is the hazard the
// gate exists to stop. It returns the message so a caller can compare messages
// ACROSS branches without this helper needing to know the constants.
func assertRejectedNoWrite(t *testing.T, env protocol.Envelope, reg *devices.Registry, path, refused string) string {
	t.Helper()
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeProtocolMalformed {
		t.Errorf("Code = %q, want %q", payload.Code, protocol.CodeProtocolMalformed)
	}
	if payload.Retryable {
		t.Errorf("Retryable = true, want false (the phone re-sends this frame on every connect)")
	}
	if payload.Message == "" {
		t.Error("Message is empty, want a static explanation")
	}
	if refused != "" && strings.Contains(payload.Message, refused) {
		t.Errorf("Message %q echoes the refused value; it must be static", payload.Message)
	}

	got, ok := reg.FindByTokenHash(devices.HashToken(testPlainToken))
	if !ok {
		t.Fatal("seeded device missing from registry after reject")
	}
	if got.Name != pushStoredName || got.Platform != pushStoredPlatform || got.PushToken != pushStoredPush {
		t.Errorf("device mutated by a rejected frame: Name=%q Platform=%q PushToken=%q, want %q/%q/%q",
			got.Name, got.Platform, got.PushToken, pushStoredName, pushStoredPlatform, pushStoredPush)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected registry file to NOT exist after a rejected frame; stat err = %v", err)
	}
	// The display-safety gate runs ahead of the locked region, so a refused frame
	// creates no sidecar either — a stronger claim than "no Save ran".
	assertNoSidecar(t, path)
	return payload.Message
}

// seededDevice is the fixture every display-safety test starts from: a paired
// device already carrying a full, safe (Platform, PushToken, Name) triple.
func seededDevice() devices.Device {
	return devices.Device{
		TokenHash: devices.HashToken(testPlainToken),
		Name:      pushStoredName,
		Platform:  pushStoredPlatform,
		PushToken: pushStoredPush,
	}
}

// TestRegisterPushToken_UnsafeFieldValues_EmitProtocolMalformedNoWrite is the
// ticket's central table: every refused character class, on each of the two
// client-authored fields the gate covers.
//
// THE REFUSED SET IS mintLabelIsDisplaySafe's, and the rows pin its three EDGES
// rather than only its interior, because an off-by-one on any of them is the
// realistic way this predicate breaks: U+001F/U+0020 (top of C0), U+007F (DEL) and
// U+009F (top of C1) are each refused here while their safe neighbours are
// admitted by TestRegisterPushToken_AdmissibleFieldValues_StoredVerbatim.
//
// Every value embeds the character MID-STRING, so a check that only inspected a
// prefix or a suffix would pass the table while leaving the injection possible.
//
// The ESC rows carry a BARE ESC, deliberately not the ESC-then-open-bracket that
// starts a real ANSI run: cmd/substrate-guard bans that source sequence in every
// .go file outside its two-path allowlist, with no per-line exemption. The gate
// refuses ESC as a C0 control on its own, so the CSI tail exercised no extra
// branch — do not "complete" these values.
func TestRegisterPushToken_UnsafeFieldValues_EmitProtocolMalformedNoWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		field string // which payload field carries the hostile value
		value string
	}{
		{"device_name NUL", "device_name", "kitchen\x00pad"},
		{"device_name TAB", "device_name", "kitchen\tpad"},
		{"device_name LF forges a log line", "device_name", "kitchen\nfake log line"},
		{"device_name CR", "device_name", "kitchen\rfake log line"},
		{"device_name ESC", "device_name", "kitchen\x1bpad"},
		{"device_name U+001F top of C0", "device_name", "kitchen\x1fpad"},
		{"device_name DEL", "device_name", "kitchen\x7fpad"},
		{"device_name U+0085 NEL in C1", "device_name", "kitchen\u0085pad"},
		{"device_name U+009F top of C1", "device_name", "kitchen\u009fpad"},
		{"platform LF", "platform", "fcm\nfake log line"},
		{"platform ESC", "platform", "fcm\x1bpad"},
		{"platform DEL", "platform", "fcm\x7f"},
		{"platform U+009F", "platform", "fcm\u009f"},
	}

	// Messages are collected per field so the per-branch-message design is pinned
	// without naming a literal: rows sharing a field must answer identically, and
	// the two fields must answer differently. A single shared "malformed" string
	// would satisfy every other assertion in this test and fail here.
	byField := map[string]string{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg, path := freshRegistryWithDevice(t, seededDevice())
			snapshot := seededDevice()
			c, recv := newTestConn(t, &snapshot)

			p := protocol.RegisterPushTokenPayload{
				Platform:   testPlatform,
				Token:      testPushToken,
				DeviceName: testDeviceName,
			}
			switch tc.field {
			case "device_name":
				p.DeviceName = tc.value
			case "platform":
				p.Platform = tc.value
			}

			h := RegisterPushToken(reg, path, testLogger(t))
			if err := h(context.Background(), c, makeRequest(t, p)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			env := assertEnvelopeShape(t, recv(), protocol.TypeError)
			msg := assertRejectedNoWrite(t, env, reg, path, tc.value)

			if prev, seen := byField[tc.field]; seen && prev != msg {
				t.Errorf("message for field %q = %q, want %q (rows sharing a field share a message)", tc.field, msg, prev)
			}
			byField[tc.field] = msg
		})
	}

	if a, b := byField["device_name"], byField["platform"]; a != "" && b != "" && a == b {
		t.Errorf("device_name and platform share the message %q; each reject branch carries its own", a)
	}
}

// TestRegisterPushToken_DeviceNameByteBound_RefusesOverAcceptsAt pins
// protocol.MaxDeviceNameBytes at this frame, in BYTES rather than runes, and pins
// that the answer is a rejected frame rather than a truncated name.
//
// The at-bound row runs the whole accept path — reload, save, reloaded-value
// check — so a guard written with >= instead of > reddens on a stored value and
// not merely on a reply code.
func TestRegisterPushToken_DeviceNameByteBound_RefusesOverAcceptsAt(t *testing.T) {
	t.Parallel()
	atBound := strings.Repeat("n", protocol.MaxDeviceNameBytes)
	overBound := atBound + "n"

	t.Run("over bound is refused and nothing is written", func(t *testing.T) {
		t.Parallel()
		reg, path := freshRegistryWithDevice(t, seededDevice())
		snapshot := seededDevice()
		c, recv := newTestConn(t, &snapshot)

		h := RegisterPushToken(reg, path, testLogger(t))
		req := makeRequest(t, protocol.RegisterPushTokenPayload{
			Platform:   testPlatform,
			Token:      testPushToken,
			DeviceName: overBound,
		})
		if err := h(context.Background(), c, req); err != nil {
			t.Fatalf("handler: %v", err)
		}
		env := assertEnvelopeShape(t, recv(), protocol.TypeError)
		assertRejectedNoWrite(t, env, reg, path, overBound)
	})

	t.Run("exactly at bound is stored whole", func(t *testing.T) {
		t.Parallel()
		reg, path := freshRegistryWithDevice(t, seededDevice())
		if err := reg.Save(path); err != nil {
			t.Fatalf("Save seed: %v", err)
		}
		snapshot := seededDevice()
		c, recv := newTestConn(t, &snapshot)

		h := RegisterPushToken(reg, path, testLogger(t))
		req := makeRequest(t, protocol.RegisterPushTokenPayload{
			Platform:   testPlatform,
			Token:      testPushToken,
			DeviceName: atBound,
		})
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
		if got.Name != atBound {
			t.Errorf("stored Name is %d bytes, want the %d supplied verbatim (never truncated)", len(got.Name), len(atBound))
		}
	})
}

// TestRegisterPushToken_UnsafeNameMatchingStored_RefusedNotDeduped is the
// ORDERING test, and the one this ticket's third acceptance criterion exists for.
//
// The seeded device already carries an unsafe name — the pre-gate residual, a
// value stored before any check existed — and the payload repeats it verbatim, so
// the dedupe comparison would match on all three fields. A guard placed AFTER that
// comparison answers ack and the unsafe value keeps its place in the registry and
// in every log that names it; a guard placed BEFORE refuses. Nothing else in the
// suite can tell those two implementations apart.
func TestRegisterPushToken_UnsafeNameMatchingStored_RefusedNotDeduped(t *testing.T) {
	t.Parallel()
	const unsafe = "kitchen\nfake log line"
	d := devices.Device{
		TokenHash: devices.HashToken(testPlainToken),
		Name:      unsafe,
		Platform:  testPlatform,
		PushToken: testPushToken,
	}
	reg, path := freshRegistryWithDevice(t, d)
	snapshot := d
	c, recv := newTestConn(t, &snapshot)

	h := RegisterPushToken(reg, path, testLogger(t))
	req := makeRequest(t, protocol.RegisterPushTokenPayload{
		Platform:   testPlatform,
		Token:      testPushToken,
		DeviceName: unsafe,
	})
	if err := h(context.Background(), c, req); err != nil {
		t.Fatalf("handler: %v", err)
	}

	env := assertEnvelopeShape(t, recv(), protocol.TypeError)
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if payload.Code != protocol.CodeProtocolMalformed {
		t.Errorf("Code = %q, want %q — an unsafe value that matches the stored one must be refused, not deduped", payload.Code, protocol.CodeProtocolMalformed)
	}
	if strings.Contains(payload.Message, unsafe) {
		t.Errorf("Message %q echoes the refused value", payload.Message)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected registry file to NOT exist after a rejected frame; stat err = %v", err)
	}
	assertNoSidecar(t, path)
}

// TestRegisterPushToken_AdmissibleFieldValues_StoredVerbatim is the other half of
// the character-set boundary: the neighbours of every refused edge, plus the two
// characters a NEARBY predicate refuses and this one must not.
//
// internal/sessions' admissibleClientField shares this loop but additionally
// refuses a double quote and invalid UTF-8, for reasons its own block states about
// a system prompt it renders. Copying that function instead of
// mintLabelIsDisplaySafe would pass every reject row above and redden exactly
// here, which is why a quote has a row.
//
// STORED VERBATIM means byte-for-byte: no truncation, no escaping, no repair. The
// assertion reads the value back off disk rather than out of memory so an escape
// introduced by the persist path would redden too.
func TestRegisterPushToken_AdmissibleFieldValues_StoredVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
	}{
		{"space, the neighbour below U+001F", "kitchen pad"},
		{"tilde U+007E, the neighbour below DEL", "kitchen~pad"},
		{"U+00A0, the neighbour above the C1 range", "kitchen\u00a0pad"},
		{"a double quote, which admissibleClientField refuses and this must not", `kitchen "pad"`},
		{"a multi-byte rune", "keittiö — Juhana's iPad 📱"},
		{"the empty string, meaning the client named no device", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, path := freshRegistryWithDevice(t, seededDevice())
			if err := reg.Save(path); err != nil {
				t.Fatalf("Save seed: %v", err)
			}
			snapshot := seededDevice()
			c, recv := newTestConn(t, &snapshot)

			h := RegisterPushToken(reg, path, testLogger(t))
			req := makeRequest(t, protocol.RegisterPushTokenPayload{
				Platform:   testPlatform,
				Token:      testPushToken,
				DeviceName: tc.value,
			})
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
			if got.Name != tc.value {
				t.Errorf("stored Name = %q, want %q verbatim", got.Name, tc.value)
			}
		})
	}
}
