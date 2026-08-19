package devices

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// holdLock acquires the lock for devicesPath on a background goroutine and
// returns once the critical section is entered, so a caller can race a live
// holder deterministically. The returned release function ends the region and
// waits for the holder to unwind; it is idempotent and also runs at test
// cleanup, so a t.Fatalf before the explicit call cannot strand the goroutine.
func holdLock(t *testing.T, devicesPath string) (release func()) {
	t.Helper()
	held := make(chan struct{})
	rel := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithLock(devicesPath, DefaultLockWait, func() error {
			close(held)
			<-rel
			return nil
		})
	}()
	<-held

	var once sync.Once
	release = func() {
		once.Do(func() {
			close(rel)
			if err := <-done; err != nil {
				t.Errorf("holder WithLock(%s): %v", devicesPath, err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// TestWithLock_TwoAcquirersDoNotInterleave is the mutual-exclusion proof: two
// goroutines each open their own file description, which genuinely contend
// because flock(2) locks the description rather than the process. The in-region
// sleep makes the mutant (acquisition deleted) fail deterministically rather
// than probabilistically — both goroutines are inside at once without the lock.
func TestWithLock_TwoAcquirersDoNotInterleave(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")

	var mu sync.Mutex
	var events []string
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, s)
	}

	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithLock(path, DefaultLockWait, func() error {
				record(id + ":enter")
				time.Sleep(30 * time.Millisecond)
				record(id + ":exit")
				return nil
			})
			if err != nil {
				t.Errorf("WithLock(%s): %v", id, err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 4 {
		t.Fatalf("events = %v, want 4 entries", events)
	}
	first, _, _ := strings.Cut(events[0], ":")
	second := "b"
	if first == "b" {
		second = "a"
	}
	want := []string{first + ":enter", first + ":exit", second + ":enter", second + ":exit"}
	if !slices.Equal(events, want) {
		t.Errorf("critical sections interleaved:\n got = %v\nwant = %v", events, want)
	}
}

// TestWithLock_SaveInsideRegionStillExcludes discriminates the sidecar lock
// file from the naive one held on devices.json itself. Save commits by
// renaming a temp file over the target, which installs a NEW inode at that
// path; a lock held on the old inode would stop excluding a second acquirer
// that opens the path afterwards. With the sidecar — which nothing ever
// renames over — the region survives its own write.
func TestWithLock_SaveInsideRegionStillExcludes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	writeDevicesFile(t, path, Device{TokenHash: HashToken("pre"), Name: "pre", PairedAt: when, LastSeenAt: when})

	renamed := make(chan struct{})
	checked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithLock(path, DefaultLockWait, func() error {
			r := &Registry{}
			r.Add(Device{TokenHash: HashToken("inside"), Name: "inside", PairedAt: when, LastSeenAt: when})
			if err := r.Save(path); err != nil {
				return err
			}
			close(renamed)
			<-checked
			return nil
		})
	}()
	<-renamed

	ran := false
	err := WithLock(path, 50*time.Millisecond, func() error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrLockBusy) {
		t.Errorf("acquire after in-region rename: err = %v, want ErrLockBusy", err)
	}
	if ran {
		t.Error("fn ran while another region held the lock")
	}

	close(checked)
	if err := <-done; err != nil {
		t.Fatalf("holder WithLock: %v", err)
	}
	if err := WithLock(path, DefaultLockWait, func() error { return nil }); err != nil {
		t.Errorf("acquire after region closed: %v", err)
	}
}

func TestWithLock_BusyAcquirerWritesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	writeDevicesFile(t, path, Device{TokenHash: HashToken("pre"), Name: "pre", PairedAt: when, LastSeenAt: when})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read devices file: %v", err)
	}

	release := holdLock(t, path)

	ran := false
	err = WithLock(path, 50*time.Millisecond, func() error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrLockBusy) {
		t.Errorf("err = %v, want ErrLockBusy", err)
	}
	if err != nil && !strings.Contains(err.Error(), path+".lock") {
		t.Errorf("err = %q, want it to name the lock path %q", err, path+".lock")
	}
	if ran {
		t.Error("fn ran despite a failed acquire")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read devices file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("devices.json mutated by a failed acquire:\n got = %s\nwant = %s", after, before)
	}
	release()
}

// TestWithLock_WaitIsBounded pins that a peer which never releases within the
// caller's wait does not park the acquirer forever. The upper bound is loose on
// purpose: it discriminates "returned" from "blocked indefinitely", not the
// poll interval's accuracy on a loaded runner.
func TestWithLock_WaitIsBounded(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")
	release := holdLock(t, path)

	start := time.Now()
	err := WithLock(path, 50*time.Millisecond, func() error { return nil })
	elapsed := time.Since(start)
	if !errors.Is(err, ErrLockBusy) {
		t.Errorf("err = %v, want ErrLockBusy", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("acquire took %v for a 50ms wait, want a bounded return", elapsed)
	}
	release()
}

// TestWithLock_CreatesMissingParentDirectory covers cold start: acquisition
// happens before the on-disk read, so on a first-ever `pyry pair` the instance
// directory does not exist yet — it is created later by identity.LoadOrCreate
// and by Save's own mkdir.
func TestWithLock_CreatesMissingParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permission semantics required")
	}
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "pyry")
	path := filepath.Join(dir, "devices.json")

	if err := WithLock(path, DefaultLockWait, func() error { return nil }); err != nil {
		t.Fatalf("WithLock against a missing parent directory: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat instance dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 0700", mode)
	}
	lockInfo, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if mode := lockInfo.Mode().Perm(); mode != 0o600 {
		t.Errorf("lock file mode = %o, want 0600", mode)
	}
}

// TestWithLock_DistinctInstancesDoNotContend stands in for two -pyry-name
// instances: the lock path is derived from the devices path, so their sidecars
// are different files.
func TestWithLock_DistinctInstancesDoNotContend(t *testing.T) {
	t.Parallel()
	pathA := filepath.Join(t.TempDir(), "devices.json")
	pathB := filepath.Join(t.TempDir(), "devices.json")

	release := holdLock(t, pathA)
	if err := WithLock(pathB, 50*time.Millisecond, func() error { return nil }); err != nil {
		t.Errorf("acquire on a second instance while the first is held: %v", err)
	}
	release()
}

func TestWithLock_LocksSiblingNotRegistry(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")

	if err := WithLock(path, DefaultLockWait, func() error { return nil }); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Errorf("stat sidecar lock file: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat devices.json: err = %v, want fs.ErrNotExist — acquisition must not conjure a registry", err)
	}
}

// TestWithLock_RegionSpansReadModifyWrite pins that the entry point composes
// with a real Load → Add → Save rather than merely with a sleep.
func TestWithLock_RegionSpansReadModifyWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")
	when := mustParseTime(t, "2026-05-09T12:34:56.789Z")
	writeDevicesFile(t, path, Device{TokenHash: HashToken("existing"), Name: "existing", PairedAt: when, LastSeenAt: when})

	err := WithLock(path, DefaultLockWait, func() error {
		r, err := Load(path)
		if err != nil {
			return err
		}
		r.Add(Device{
			TokenHash:  HashToken("added"),
			Name:       "added",
			PairedAt:   when.Add(time.Second),
			LastSeenAt: when.Add(time.Second),
		})
		return r.Save(path)
	})
	if err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load after locked write: %v", err)
	}
	got := r.List()
	if len(got) != 2 {
		t.Fatalf("devices after locked write = %d, want 2", len(got))
	}
	for _, name := range []string{"existing", "added"} {
		if !slices.ContainsFunc(got, func(d Device) bool { return d.Name == name }) {
			t.Errorf("device %q missing after locked read-modify-write: %+v", name, got)
		}
	}
}

func TestWithLock_CallbackErrorPropagatesAndReleases(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "devices.json")
	sentinel := errors.New("callback failed")

	err := WithLock(path, DefaultLockWait, func() error { return sentinel })
	if err != sentinel {
		t.Errorf("err = %v, want fn's error returned verbatim", err)
	}
	if err := WithLock(path, 50*time.Millisecond, func() error { return nil }); err != nil {
		t.Errorf("lock not released after fn returned an error: %v", err)
	}
}
