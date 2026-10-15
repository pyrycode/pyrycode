package thread

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/history"
)

func TestStoreCertificateWrites(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	unchanged := make(chan struct{})
	baseReader := s.forward
	s.forward = func(id conversations.ConversationID) (storeReader, error) {
		r, err := baseReader(id)
		return testStoreReader{storeReader: r, tail: func(ctx context.Context, feed func([]history.Entry) error) error {
			for i := 0; i < 10; i++ {
				if err := feed(nil); err != nil {
					return err
				}
			}
			close(unchanged)
			return r.Tail(ctx, feed)
		}}, err
	}
	var mu sync.Mutex
	var writes []map[string]json.RawMessage
	s.replace = func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		raw, err := os.ReadFile(to)
		if err != nil {
			return err
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		writes = append(writes, record)
		return nil
	}
	check := func(count int, clean bool) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(writes) != count || string(writes[count-1]["Clean"]) != map[bool]string{true: "true", false: "false"}[clean] {
			t.Fatalf("certificate writes: count=%d, want %d, records=%v", len(writes), count, writes)
		}
	}
	first := testCheckpointLoad(t, s, testStoreA, 0)
	testStoreSignal(t, unchanged)
	s.forward = baseReader
	check(1, false)
	testStoreAppend(t, h, testStoreA, testMain(1, "tool_use", `,"tool_use_id":"read","name":"Read"`))
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	testStoreAppend(t, h, testStoreA, testMain(2, "tool_result", `,"tool_use_id":"read","is_error":false`))
	if testStoreWait(t, s, testStoreA, StateUsable, 2).Active {
		t.Fatal("fixture did not become idle")
	}
	testStoreAppend(t, h, testStoreA, testEntry(0, "unsupported", `{}`))
	completed := testStoreWait(t, s, testStoreA, StateUsable, 3)
	check(1, false)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	testCacheCompact(t, s, completed)
	check(2, true)
	if got := testCheckpointLoad(t, s, testStoreA, 3); got.Epoch != first.Epoch {
		t.Fatal("reload rotated clean epoch")
	}
	check(3, false)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	check(4, true)
}

func TestStoreCertificateMigration(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"regular", "cache-symlink", "recovery-symlink", "run-symlink", "directory", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			unsafe := mode != "regular"
			s, h := testThreadStore(t)
			testStoreAppend(t, h, testStoreA, testMessage(1))
			dir, _ := h.LogDir(testStoreA)
			legacy := []string{"thread-cache.json", "thread-recovery.json", "thread-run-" + strings.Repeat("a", 32) + ".json"}
			unrelated := filepath.Join(dir, "thread-run-invalid.json")
			if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, name := range legacy {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"Epoch":"legacy"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if unsafe {
				index := map[string]int{"recovery-symlink": 1, "run-symlink": 2}[mode]
				path := filepath.Join(dir, legacy[index])
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				var err error
				switch mode {
				case "directory":
					err = os.Mkdir(path, 0700)
				case "fifo":
					err = syscall.Mkfifo(path, 0600)
				default:
					err = os.Symlink(unrelated, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			base := s.replace
			s.replace = func(from, to string) error { return errors.New("injected dirty failure") }
			if err := s.Load(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			testStoreWait(t, s, testStoreA, StateUnavailable, 0)
			for _, name := range legacy {
				if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
					t.Fatal("legacy removed before successful dirty write", err)
				}
			}
			s.replace = base
			if err := s.Retry(context.Background(), testStoreA); err != nil {
				t.Fatal(err)
			}
			if unsafe {
				testStoreWait(t, s, testStoreA, StateUnavailable, 0)
			} else {
				first := testStoreWait(t, s, testStoreA, StateUsable, 1)
				for _, name := range legacy {
					if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("legacy leaf retained", name, err)
					}
				}
				if err := s.Unload(testStoreA); err != nil {
					t.Fatal(err)
				}
				if got := testCheckpointLoad(t, s, testStoreA, 1); got.Epoch != first.Epoch {
					t.Fatal("migration rotated twice")
				}
			}
			if raw, err := os.ReadFile(unrelated); err != nil || string(raw) != "keep" {
				t.Fatal("cleanup touched unrelated target", err)
			}
		})
	}
}

func TestStoreCertificateIsolation(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	a := testCheckpointLoad(t, s, testStoreA, 0)
	b := testCheckpointLoad(t, s, testStoreB, 0)
	base := s.replace
	s.replace = func(from, to string) error {
		if strings.Contains(to, string(testStoreA)) {
			return errors.New("injected final failure")
		}
		return base(from, to)
	}
	if err := s.Shutdown(); err != ErrPersistence {
		t.Fatal("final failure lost", err)
	}
	next := NewStore(h)
	t.Cleanup(func() { _ = next.Shutdown() })
	for _, tc := range []struct {
		id     conversations.ConversationID
		epoch  string
		retain bool
	}{{testStoreA, a.Epoch, false}, {testStoreB, b.Epoch, true}} {
		if got := testCheckpointLoad(t, next, tc.id, 0); (got.Epoch == tc.epoch) != tc.retain {
			t.Fatal("conversation certification was not independent", tc.id)
		}
	}
}

func TestStoreCertificateDirectorySync(t *testing.T) {
	t.Parallel()
	s, h, c := testCheckpointStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(1))
	reached, release := make(chan struct{}), make(chan struct{})
	var once, notified sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	s.syncDir = func(string) error {
		notified.Do(func() { close(reached) })
		<-release
		return errors.New("private path /host/path")
	}
	if err := s.Load(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreSignal(t, reached)
	if got := s.Snapshot(testStoreA); got.State != StateRebuilding || got.Items != nil {
		t.Fatal("dirty rename usable before directory sync", got)
	}
	once.Do(func() { close(release) })
	if got := testStoreWait(t, s, testStoreA, StateUnavailable, 0); got.Err != ErrUnavailable {
		t.Fatal("directory-sync error exposed", got)
	}
	c.count(t, testStoreA, 1)
	s.syncDir = syncCacheDir
	if err := s.Retry(context.Background(), testStoreA); err != nil {
		t.Fatal(err)
	}
	testStoreWait(t, s, testStoreA, StateUsable, 1)
	c.count(t, testStoreA, 2)
	if err := s.Unload(testStoreA); err != nil {
		t.Fatal(err)
	}
	c.count(t, testStoreA, 3)
}

func TestCertificateLeaves(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	testStoreAppend(t, h, testStoreA, testMessage(1))
	for _, name := range []string{"../thread-certificate.json", "/thread-certificate.json", "history.jsonl", "thread-run-invalid.json", "thread-run-" + strings.Repeat("A", 32) + ".json"} {
		if cacheLeaf(name) || s.writeCacheFile(testStoreA, name, struct{}{}) != ErrPersistence || s.readCacheFile(testStoreA, name, &cacheRecord{}) != ErrPersistence {
			t.Fatal("unsafe metadata name accepted", name)
		}
	}
}

func TestStoreCertificateFinalSyncFailure(t *testing.T) {
	t.Parallel()
	s, h := testThreadStore(t)
	first := testCheckpointLoad(t, s, testStoreA, 0)
	s.syncDir = func(string) error { return errors.New("injected final sync failure") }
	if err := s.Unload(testStoreA); err != ErrPersistence {
		t.Fatal("final sync failure lost", err)
	}
	next := NewStore(h)
	t.Cleanup(func() { _ = next.Shutdown() })
	if got := testCheckpointLoad(t, next, testStoreA, 0); got.Epoch == first.Epoch {
		t.Fatal("failed final directory sync certified clean")
	}
}
