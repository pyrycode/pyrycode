package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/devices"
)

// mintBlockGrace is how long the revoke interleaving test waits before
// concluding that the verb is genuinely parked on the devices lock.
const mintBlockGrace = 300 * time.Millisecond

// holdPairLock acquires the devices lock for devicesPath on a background
// goroutine and returns once the critical section is entered, so a test can
// race a CLI verb against a live holder deterministically. Mirrors holdLock in
// internal/devices/lock_test.go; flock(2) contends per open file description
// rather than per process, so an in-process holder is a real contender for the
// verb running in this same binary.
//
// The returned release ends the region and waits for the holder to unwind. It
// is idempotent and also runs at cleanup, so a t.Fatalf before the explicit
// call cannot strand the goroutine. Both waits are bounded: a holder that
// never enters, or never unwinds, reports a failure instead of hanging the
// package.
func holdPairLock(t *testing.T, devicesPath string) (release func()) {
	t.Helper()
	held := make(chan struct{})
	rel := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- devices.WithLock(devicesPath, devices.DefaultLockWait, func() error {
			close(held)
			<-rel
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("holder never entered the region for %s: %v", devicesPath, err)
	}

	var once sync.Once
	release = func() {
		once.Do(func() {
			close(rel)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("holder WithLock(%s): %v", devicesPath, err)
				}
			case <-time.After(devices.DefaultLockWait):
				t.Errorf("holder for %s did not unwind within %v", devicesPath, devices.DefaultLockWait)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// seedDevices adds devs to the registry at path and persists it, taking no
// lock. Used both to lay down a starting state and — deliberately — to stand
// in for a racing writer that commits while a locked verb is parked.
func seedDevices(t *testing.T, path string, devs ...devices.Device) {
	t.Helper()
	registry, err := devices.Load(path)
	if err != nil {
		t.Fatalf("devices.Load(%s): %v", path, err)
	}
	for _, d := range devs {
		registry.Add(d)
	}
	if err := registry.Save(path); err != nil {
		t.Fatalf("Save(%s): %v", path, err)
	}
}

// lockTestDevice builds a fixture device whose TokenHash is prefix padded to
// the 64 hex characters a real sha256 digest occupies.
func lockTestDevice(name, prefix string) devices.Device {
	return devices.Device{
		Name:      name,
		TokenHash: prefix + strings.Repeat("0", 64-len(prefix)),
		PairedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// deviceNames returns the Name set of the registry persisted at path.
func deviceNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	registry, err := devices.Load(path)
	if err != nil {
		t.Fatalf("devices.Load(%s): %v", path, err)
	}
	names := make(map[string]bool)
	for _, d := range registry.List() {
		names[d.Name] = true
	}
	return names
}

// silenceStdout points os.Stdout at a throwaway file for tests of successful
// revoke/list operations. Restored at cleanup.
func silenceStdout(t *testing.T) {
	t.Helper()
	orig := os.Stdout
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("os.CreateTemp: %v", err)
	}
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = orig
		_ = f.Close()
	})
}

// isolatedInstance points HOME at a fresh temp dir and clears PYRY_NAME, then
// returns the devices.json path for the default instance.
func isolatedInstance(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PYRY_NAME", "")
	return resolveDevicesPath(defaultName())
}

// shrinkPairLockWait tightens the CLI's acquisition bound for the duration of
// the test. A contention test has to wait the bound out to observe the refusal,
// and the production default would cost the suite five seconds per case.
func shrinkPairLockWait(t *testing.T) {
	t.Helper()
	orig := pairLockWait
	pairLockWait = 50 * time.Millisecond
	t.Cleanup(func() { pairLockWait = orig })
}

// freezeRegistry snapshots the bytes at path and back-dates its mtime,
// returning an assertion that both still hold. The back-date is what makes the
// assertion non-vacuous: Save is idempotent, so rewriting identical content
// still passes a byte comparison, but Save commits by temp-file-then-rename,
// which always installs a current mtime — so a still-back-dated mtime is proof
// that no Save ran at all.
func freezeRegistry(t *testing.T, path string) (assertUnchanged func()) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", path, err)
	}
	backdated := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, backdated, backdated); err != nil {
		t.Fatalf("os.Chtimes(%s): %v", path, err)
	}
	return func() {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%s) after the refusal: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("devices.json content changed despite the refusal:\n got %s\nwant %s", got, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("os.Stat(%s) after the refusal: %v", path, err)
		}
		if !info.ModTime().Equal(backdated) {
			t.Errorf("devices.json mtime advanced from %v to %v; a Save ran despite the refusal",
				backdated, info.ModTime())
		}
	}
}

// assertLockBusy checks the refusal shape both write verbs share: a non-nil
// error carrying devices.ErrLockBusy whose message names the sidecar path.
// main.run turns a returned error into a non-zero exit, so matching here is
// equivalent to asserting the exit status without spawning a subprocess.
func assertLockBusy(t *testing.T, err error, devicesPath string) {
	t.Helper()
	if err == nil {
		t.Fatal("verb succeeded while another holder had the devices lock; it wrote without exclusion")
	}
	if !errors.Is(err, devices.ErrLockBusy) {
		t.Errorf("error %v is not devices.ErrLockBusy", err)
	}
	if lockPath := devicesPath + ".lock"; !strings.Contains(err.Error(), lockPath) {
		t.Errorf("error %q does not name the lock path %q", err, lockPath)
	}
}

// TestRunPairRevoke_BusyLockRefusesWithoutWriting is the revoke leg of the
// same criterion. It also pins that a busy lock is reported as such rather
// than as a missing device: the sentinel branch is matched by value, and
// ErrLockBusy never reaches it.
func TestRunPairRevoke_BusyLockRefusesWithoutWriting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pyry is linux+macOS only")
	}
	path := isolatedInstance(t)
	silenceStdout(t)
	seedDevices(t, path, lockTestDevice("alpha", "aaaaaaaa"))
	assertUnchanged := freezeRegistry(t, path)
	shrinkPairLockWait(t)

	holdPairLock(t, path)

	err := runPairRevoke([]string{"alpha"})
	assertLockBusy(t, err, path)
	if !strings.Contains(err.Error(), "pair revoke:") {
		t.Errorf("error %q missing the %q prefix", err, "pair revoke:")
	}
	assertUnchanged()
}

// TestRunPairRevoke_ReadsSnapshotInsideLock is the revoke leg of the same
// criterion, and the security-relevant direction: a revoke that mutates a
// pre-lock snapshot resurrects whatever a racing writer committed under it.
// Reddens the same two ways as its mint twin.
func TestRunPairRevoke_ReadsSnapshotInsideLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pyry is linux+macOS only")
	}
	path := isolatedInstance(t)
	silenceStdout(t)
	seedDevices(t, path,
		lockTestDevice("alpha", "aaaaaaaa"),
		lockTestDevice("bravo", "bbbbbbbb"))

	release := holdPairLock(t, path)

	errc := make(chan error, 1)
	go func() { errc <- runPairRevoke([]string{"alpha"}) }()

	select {
	case err := <-errc:
		t.Fatalf("runPairRevoke completed while another holder had the devices lock (err=%v); its write is not inside a locked region", err)
	case <-time.After(mintBlockGrace):
	}

	seedDevices(t, path, lockTestDevice("zulu", "cccccccc"))
	release()

	if err := <-errc; err != nil {
		t.Fatalf("runPairRevoke: %v", err)
	}

	names := deviceNames(t, path)
	if !names["zulu"] {
		t.Errorf("device committed while the revoke was parked on the lock was erased (names=%v)", names)
	}
	if names["alpha"] {
		t.Errorf("revoked device alpha is still present (names=%v)", names)
	}
	if !names["bravo"] {
		t.Errorf("unrelated device bravo was erased by the revoke (names=%v)", names)
	}
}

// TestRunPairReadVerbs_TakeNoLock pins the read-only criterion: `pyry pair
// list` and `pyry pair preflight` must not acquire the devices lock, so a
// listing can never block behind a writer. WithLock creates the sidecar before
// running its function, so the sidecar's continued absence is a strictly
// stronger witness than "no Save ran" — it proves the region was never even
// entered. Preflight runs against an empty registry so it takes its exit-0
// path rather than os.Exit(2).
func TestRunPairReadVerbs_TakeNoLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pyry is linux+macOS only")
	}
	path := isolatedInstance(t)
	silenceStdout(t)
	seedDevices(t, path)

	lockPath := path + ".lock"
	if _, err := os.Stat(lockPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sidecar %s exists before any verb ran (stat err=%v)", lockPath, err)
	}

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"list", func() error { return runPairList(nil) }},
		{"preflight", func() error { return runPairPreflight(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err != nil {
				t.Fatalf("runPair%s: %v", tc.name, err)
			}
			if _, err := os.Stat(lockPath); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("`pyry pair %s` created the lock sidecar %s (stat err=%v); read-only verbs must take no lock",
					tc.name, lockPath, err)
			}
		})
	}
}
