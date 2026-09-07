package relay

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
	"github.com/pyrycode/pyrycode/internal/protocol"
)

// redemptionFixture seeds a devices.json holding one device paired to
// v2TestToken and carrying redeemBy — pass the zero time for a record minted
// before the field existed. Returns the in-memory registry the daemon would be
// holding (the same object the manager gets, mirroring production's
// Load-once-at-startup) and the path it was written to. Save takes no lock, so
// the sidecar does not exist afterwards and its absence stays a usable witness.
func redemptionFixture(t *testing.T, redeemBy time.Time) (*devices.Registry, string) {
	t.Helper()
	reg := &devices.Registry{}
	reg.Add(devices.Device{
		TokenHash: devices.HashToken(v2TestToken),
		Name:      v2TestDevName,
		PairedAt:  time.Now().UTC().Add(-time.Minute),
		RedeemBy:  redeemBy,
	})
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := reg.Save(path); err != nil {
		t.Fatalf("seed devices.json: %v", err)
	}
	return reg, path
}

// startRedemptionManager runs a manager over reg with DevicesPath wired, and
// returns everything openModalConn needs to drive a handshake against it.
func startRedemptionManager(t *testing.T, reg *devices.Registry, path string) (mgr *V2SessionManager, frames chan protocol.RoutingEnvelope, rec *v2Recorder, respPub []byte) {
	t.Helper()
	respPriv, respPub := genV2Keypair(t)
	frames = make(chan protocol.RoutingEnvelope, 4)
	rec = &v2Recorder{}
	mgr, stop := startManager(t, V2SessionConfig{
		Frames:      frames,
		Outbound:    rec.outbound,
		StaticPriv:  respPriv,
		Devices:     reg,
		DevicesPath: path,
		ServerID:    v2TestServerID,
		Logger:      silentLogger(),
	})
	t.Cleanup(stop)
	return mgr, frames, rec, respPub
}

// diskDevice reads path fresh — the restarted-daemon view — and returns the
// record paired to v2TestToken.
func diskDevice(t *testing.T, path string) devices.Device {
	t.Helper()
	reg, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	dev, ok := reg.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatalf("paired device missing from %s", path)
	}
	return dev
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	return fi.ModTime()
}

// backdate stamps path with a distinctly old mtime and returns it. Save
// commits by renaming a freshly created temp file over the target, so a Save
// always installs a current mtime — which makes an unchanged old one proof
// that no Save ran, rather than the vacuous observation that the bytes happen
// to match what an idempotent rewrite would have produced.
func backdate(t *testing.T, path string) time.Time {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
	return modTime(t, path)
}

func assertNotRewritten(t *testing.T, path string, want time.Time) {
	t.Helper()
	if got := modTime(t, path); !got.Equal(want) {
		t.Fatalf("devices.json mtime = %v, want the untouched %v — a Save ran", got, want)
	}
}

// TestV2Session_FirstRedemption_ClearsDeadlineOnDisk is AC-1: the first
// successful validation of a device carrying a deadline clears it and persists
// the registry. Placing the write ahead of the V2StateOpen transition makes
// openModalConn's wait-for-open the happens-before edge, so the assertions
// below need no polling.
func TestV2Session_FirstRedemption_ClearsDeadlineOnDisk(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Now().UTC().Add(devices.RedemptionWindow))

	// Non-vacuity guard: a fixture that lost the deadline would make the
	// post-handshake assertions pass against a record that never had one.
	seeded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded devices.json: %v", err)
	}
	if !strings.Contains(string(seeded), "redeem_by") {
		t.Fatalf("seeded devices.json carries no redeem_by key:\n%s", seeded)
	}

	mgr, frames, rec, respPub := startRedemptionManager(t, reg, path)
	openModalConn(t, mgr, frames, rec, respPub, "c-redeem-first", nil)

	if got := diskDevice(t, path).RedeemBy; !got.IsZero() {
		t.Errorf("on-disk RedeemBy = %v, want the zero time", got)
	}
	// omitzero should drop the key outright rather than write a zero instant.
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read devices.json after handshake: %v", err)
	}
	if strings.Contains(string(after), "redeem_by") {
		t.Errorf("devices.json still carries a redeem_by key:\n%s", after)
	}
	// Memory agrees with disk, so the next validation takes the no-write path.
	dev, ok := reg.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatalf("FindByTokenHash: paired device missing from the live registry")
	}
	if !dev.RedeemBy.IsZero() {
		t.Errorf("in-memory RedeemBy = %v, want the zero time", dev.RedeemBy)
	}
}

// TestV2Session_NoDeadline_TakesNoLockAndWritesNothing is AC-2's first half: a
// record that predates the field triggers no write at all. WithLock creates its
// sidecar before running anything the caller passed it, so the sidecar's
// absence is the stronger claim — the region was never entered, not merely
// that no Save ran.
func TestV2Session_NoDeadline_TakesNoLockAndWritesNothing(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Time{})
	before := backdate(t, path)

	mgr, frames, rec, respPub := startRedemptionManager(t, reg, path)
	openModalConn(t, mgr, frames, rec, respPub, "c-redeem-none", nil)

	assertNotRewritten(t, path, before)
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(%s.lock) = %v, want ErrNotExist — the lock was acquired for a record with no deadline", path, err)
	}
}

// TestV2Session_SecondAndThirdRedemption_WriteNothing is AC-2's second half:
// once the deadline is cleared, re-validating the same device writes nothing
// further. Three handshakes, one write.
func TestV2Session_SecondAndThirdRedemption_WriteNothing(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Now().UTC().Add(devices.RedemptionWindow))
	mgr, frames, rec, respPub := startRedemptionManager(t, reg, path)

	openModalConn(t, mgr, frames, rec, respPub, "c-redeem-1", nil)
	if got := diskDevice(t, path).RedeemBy; !got.IsZero() {
		t.Fatalf("after the first handshake, on-disk RedeemBy = %v, want the zero time", got)
	}

	before := backdate(t, path)
	openModalConn(t, mgr, frames, rec, respPub, "c-redeem-2", nil)
	assertNotRewritten(t, path, before)
	openModalConn(t, mgr, frames, rec, respPub, "c-redeem-3", nil)
	assertNotRewritten(t, path, before)
}

// TestV2Session_RedemptionSurvivesRestart is AC-3, both halves: a registry
// loaded fresh from the same path shows no deadline, and that device still
// authenticates against the restarted registry. openModalConn fails the test if
// the conn never opens, so reaching the end of the second handshake IS the
// authentication assertion.
func TestV2Session_RedemptionSurvivesRestart(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Now().UTC().Add(devices.RedemptionWindow))
	mgr, frames, rec, respPub := startRedemptionManager(t, reg, path)
	openModalConn(t, mgr, frames, rec, respPub, "c-redeem-pre", nil)

	restarted, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load(%s) after restart: %v", path, err)
	}
	dev, ok := restarted.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatalf("paired device missing after restart")
	}
	if !dev.RedeemBy.IsZero() {
		t.Fatalf("restarted registry shows RedeemBy = %v, want the zero time", dev.RedeemBy)
	}

	mgr2, frames2, rec2, respPub2 := startRedemptionManager(t, restarted, path)
	openModalConn(t, mgr2, frames2, rec2, respPub2, "c-redeem-post", nil)
}

// TestV2Session_LockBusy_HandshakeStillCompletes is AC-4: a lock this caller
// cannot acquire within its bound does not fail the handshake. The conn reaches
// open and the registry is left alone.
func TestV2Session_LockBusy_HandshakeStillCompletes(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Now().UTC().Add(devices.RedemptionWindow))
	before := backdate(t, path)

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- devices.WithLock(path, devices.DefaultLockWait, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	mgr, frames, rec, respPub := startRedemptionManager(t, reg, path)
	openModalConn(t, mgr, frames, rec, respPub, "c-redeem-busy", nil)
	assertNotRewritten(t, path, before)

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder WithLock: %v", err)
	}
}

// TestV2Session_RecordRedemption_PreservesConcurrentlyPairedDevice is AC-5: a
// `pyry pair` that commits after the handshake's pre-Validate reload but before
// the region acquires the lock is not clobbered. Only a direct call stages that
// interleaving deterministically — a driven handshake would let its own reload
// adopt B first, which would leave the in-region reload proving nothing.
func TestV2Session_RecordRedemption_PreservesConcurrentlyPairedDevice(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Now().UTC().Add(devices.RedemptionWindow))

	diskReg, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	bHash := devices.HashToken("v2-redeem-token-b")
	diskReg.Add(devices.Device{
		TokenHash: bHash,
		Name:      "device-b",
		PairedAt:  time.Now().UTC(),
		RedeemBy:  time.Now().UTC().Add(devices.RedemptionWindow),
	})
	if err := diskReg.Save(path); err != nil {
		t.Fatalf("Save [A,B]: %v", err)
	}

	mgr, _, _, _ := startRedemptionManager(t, reg, path)
	dev, ok := reg.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatalf("FindByTokenHash: device A missing from the live registry")
	}
	mgr.recordRedemption("c-redeem-concurrent", dev)

	after, err := devices.Load(path)
	if err != nil {
		t.Fatalf("Load(%s) after the redemption write: %v", path, err)
	}
	a, ok := after.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatalf("device A missing from disk after the redemption write")
	}
	if !a.RedeemBy.IsZero() {
		t.Errorf("device A RedeemBy = %v, want the zero time", a.RedeemBy)
	}
	b, ok := after.FindByTokenHash(bHash)
	if !ok {
		t.Fatalf("concurrently paired device B was clobbered by the redemption write")
	}
	if b.RedeemBy.IsZero() {
		t.Errorf("device B RedeemBy was cleared; only the redeeming device's deadline may change")
	}
}

// TestV2Session_RecordRedemption_ReloadFailureWritesNothing pins the
// abandon-on-reload-failure rule: with disk membership unknown, committing the
// in-memory snapshot could erase a device this daemon never read. The corrupt
// bytes must survive untouched.
func TestV2Session_RecordRedemption_ReloadFailureWritesNothing(t *testing.T) {
	t.Parallel()

	reg, path := redemptionFixture(t, time.Now().UTC().Add(devices.RedemptionWindow))

	const corrupt = "{not json"
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatalf("corrupt devices.json: %v", err)
	}
	before := backdate(t, path)

	mgr, _, _, _ := startRedemptionManager(t, reg, path)
	dev, ok := reg.FindByTokenHash(devices.HashToken(v2TestToken))
	if !ok {
		t.Fatalf("FindByTokenHash: paired device missing from the live registry")
	}
	mgr.recordRedemption("c-redeem-corrupt", dev)

	assertNotRewritten(t, path, before)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read devices.json: %v", err)
	}
	if string(raw) != corrupt {
		t.Fatalf("devices.json = %q, want the corrupt bytes untouched — the in-memory set was committed against unknown disk membership", raw)
	}
}
