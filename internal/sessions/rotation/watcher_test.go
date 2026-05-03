package rotation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProbe returns a fixed result. If pathFn is non-nil, it dynamically
// returns whatever pathFn returns (pid is ignored). callCount is bumped on
// every call.
type fakeProbe struct {
	mu        sync.Mutex
	pathFn    func() string
	err       error
	callCount int32
}

func (f *fakeProbe) OpenJSONL(pid int) (string, error) {
	atomic.AddInt32(&f.callCount, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pathFn != nil {
		return f.pathFn(), f.err
	}
	return "", f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// rotateRecord captures OnRotate invocations for assertion.
type rotateRecord struct {
	mu    sync.Mutex
	calls [][2]string
}

func (r *rotateRecord) record(oldID, newID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [2]string{oldID, newID})
	return nil
}

func (r *rotateRecord) snapshot() [][2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][2]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// startWatcher builds a Watcher with the given config defaults and starts
// Run in a goroutine. Returns the dir, watcher, cancel, and a wait function
// that returns once Run has exited.
func startWatcher(t *testing.T, cfg Config) (context.CancelFunc, func() error) {
	t.Helper()
	w, err := New(cfg)
	if err != nil {
		t.Fatalf("rotation.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Errorf("watcher did not stop within 2s")
		}
	})
	wait := func() error {
		cancel()
		select {
		case e := <-errCh:
			return e
		case <-time.After(2 * time.Second):
			return errors.New("watcher did not stop within 2s")
		}
	}
	return cancel, wait
}

const newUUID = "8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91"
const oldUUID = "00000000-0000-4000-8000-000000000001"

func TestWatcher_DetectsRotation(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, newUUID+".jsonl")
	probe := &fakeProbe{pathFn: func() string { return newPath }}
	rec := &rotateRecord{}

	startWatcher(t, Config{
		Dir:    dir,
		Probe:  probe,
		Logger: discardLogger(),
		Snapshot: func() []SessionRef {
			return []SessionRef{{ID: oldUUID, PID: 1234}}
		},
		OnRotate: rec.record,
	})

	if err := os.WriteFile(newPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if calls := rec.snapshot(); len(calls) >= 1 {
			if calls[0] != [2]string{oldUUID, newUUID} {
				t.Fatalf("rotate args = %v, want [%q %q]", calls[0], oldUUID, newUUID)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("OnRotate not called within 1s")
}

func TestWatcher_SkipsAllocated(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, newUUID+".jsonl")
	probe := &fakeProbe{pathFn: func() string { return newPath }}
	rec := &rotateRecord{}

	allocCalls := int32(0)
	startWatcher(t, Config{
		Dir:    dir,
		Probe:  probe,
		Logger: discardLogger(),
		Snapshot: func() []SessionRef {
			return []SessionRef{{ID: oldUUID, PID: 1234}}
		},
		IsAllocated: func(id string) bool {
			atomic.AddInt32(&allocCalls, 1)
			return id == newUUID
		},
		OnRotate: rec.record,
	})

	if err := os.WriteFile(newPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("OnRotate called for allocated UUID: %v", calls)
	}
	if atomic.LoadInt32(&allocCalls) == 0 {
		t.Errorf("IsAllocated never consulted")
	}
}

func TestWatcher_SkipsNonJSONL(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	probe := &fakeProbe{pathFn: func() string { return filepath.Join(dir, "foo.txt") }}
	rec := &rotateRecord{}

	startWatcher(t, Config{
		Dir:    dir,
		Probe:  probe,
		Logger: discardLogger(),
		Snapshot: func() []SessionRef {
			return []SessionRef{{ID: oldUUID, PID: 1234}}
		},
		OnRotate: rec.record,
	})

	if err := os.WriteFile(filepath.Join(dir, "foo.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("OnRotate called for non-jsonl: %v", calls)
	}
}

func TestWatcher_SkipsMalformedUUID(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := &rotateRecord{}

	startWatcher(t, Config{
		Dir:    dir,
		Probe:  &fakeProbe{},
		Logger: discardLogger(),
		Snapshot: func() []SessionRef {
			return []SessionRef{{ID: oldUUID, PID: 1234}}
		},
		OnRotate: rec.record,
	})

	if err := os.WriteFile(filepath.Join(dir, "not-a-uuid.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("OnRotate called for malformed uuid: %v", calls)
	}
}

func TestWatcher_NoSessionsZeroPID(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	probe := &fakeProbe{}
	rec := &rotateRecord{}

	startWatcher(t, Config{
		Dir:    dir,
		Probe:  probe,
		Logger: discardLogger(),
		Snapshot: func() []SessionRef {
			return []SessionRef{{ID: oldUUID, PID: 0}}
		},
		OnRotate: rec.record,
	})

	if err := os.WriteFile(filepath.Join(dir, newUUID+".jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("OnRotate called with PID=0 ref: %v", calls)
	}
	if atomic.LoadInt32(&probe.callCount) != 0 {
		t.Errorf("probe called with PID=0: %d", probe.callCount)
	}
}

func TestWatcher_ProbePathMismatch(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Probe reports the OLD jsonl, not the new one.
	probe := &fakeProbe{pathFn: func() string {
		return filepath.Join(dir, oldUUID+".jsonl")
	}}
	rec := &rotateRecord{}

	startWatcher(t, Config{
		Dir:    dir,
		Probe:  probe,
		Logger: discardLogger(),
		Snapshot: func() []SessionRef {
			return []SessionRef{{ID: oldUUID, PID: 1234}}
		},
		OnRotate: rec.record,
	})

	if err := os.WriteFile(filepath.Join(dir, newUUID+".jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("OnRotate called on probe mismatch: %v", calls)
	}
}

func TestWatcher_CreatesMissingDir(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "does", "not", "exist")

	w, err := New(Config{
		Dir:      dir,
		Probe:    &fakeProbe{},
		Logger:   discardLogger(),
		Snapshot: func() []SessionRef { return nil },
		OnRotate: func(string, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.fsw.Close() })

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("not a dir: %s", dir)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 0700", mode)
	}
}

func TestWatcher_ContextCancelExits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := New(Config{
		Dir:      dir,
		Probe:    &fakeProbe{},
		Logger:   discardLogger(),
		Snapshot: func() []SessionRef { return nil },
		OnRotate: func(string, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Errorf("Run returned %v, want context.Canceled", e)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not exit within 500ms after cancel")
	}
}

// TestWatcher_DetectsRotationThroughSymlink locks in the symlink-resolution
// fix: the watch is set up against a symlink to the real sessions dir, and
// the probe reports the *resolved* path (mimicking lsof / proc-fd output).
// Without the EvalSymlinks resolution in New, the comparison gate would
// reject the match and OnRotate would never fire.
func TestWatcher_DetectsRotationThroughSymlink(t *testing.T) {
	t.Parallel()
	realDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-sessions")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	probedPath := filepath.Join(realDir, newUUID+".jsonl")
	probe := &fakeProbe{pathFn: func() string { return probedPath }}
	rec := &rotateRecord{}

	startWatcher(t, Config{
		Dir:    link,
		Probe:  probe,
		Logger: discardLogger(),
		Snapshot: func() []SessionRef {
			return []SessionRef{{ID: oldUUID, PID: 1234}}
		},
		OnRotate: rec.record,
	})

	if err := os.WriteFile(filepath.Join(link, newUUID+".jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if calls := rec.snapshot(); len(calls) >= 1 {
			if calls[0] != [2]string{oldUUID, newUUID} {
				t.Fatalf("rotate args = %v, want [%q %q]", calls[0], oldUUID, newUUID)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("OnRotate not called within 1s")
}
