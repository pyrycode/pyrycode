package sessions

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// threadCapture records the RunnerConfig every factory call carried, keyed by
// session id, and accepts every harness, so a test can drive the RecordThread
// callback the pool handed a runner.
type threadCapture struct {
	mu   sync.Mutex
	seen map[string]RunnerConfig
}

func (c *threadCapture) factory(cfg RunnerConfig) (Runner, error) {
	c.mu.Lock()
	if c.seen == nil {
		c.seen = make(map[string]RunnerConfig)
	}
	c.seen[cfg.SessionID] = cfg
	c.mu.Unlock()
	return fakeRunner{}, nil
}

func (c *threadCapture) config(t *testing.T, id SessionID) RunnerConfig {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg, ok := c.seen[string(id)]
	if !ok {
		t.Fatalf("no RunnerConfig built for %s", id)
	}
	return cfg
}

// TestPool_Thread_RegistryRoundTripsByteIdentical: a registry holding a codex
// entry with a thread id and a claude entry without one loads and saves back
// byte-identical. The pre-key shape is TestPool_Harness_LegacyRegistryRoundTripsByteIdentical's.
func TestPool_Thread_RegistryRoundTripsByteIdentical(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	var cap threadCapture
	pool, _ := helperPoolInjectedFactory(t, regPath, t.TempDir(), cap.factory,
		registryEntry{ID: helperDormantID(t), Label: "codex", Harness: "codex", ThreadID: "thr-1"},
		registryEntry{ID: helperDormantID(t), Label: "claude", Model: "opus"},
	)
	before, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !bytes.Contains(before, []byte(`"thread_id": "thr-1"`)) {
		t.Fatalf("pre-written registry lacks the thread id, so this test proves nothing:\n%s", before)
	}

	forceSave(t, pool)

	after, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("registry changed across load+save:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestPool_Thread_ReviveResumesAndRecordFollowsRotation: a revived entry hands
// its stored thread to the runner; a recorded thread reaches the entry on disk;
// a new_session rotation leaves the new entry with no thread, refuses a report
// for the old id, and takes one for the new id.
func TestPool_Thread_ReviveResumesAndRecordFollowsRotation(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "sessions.json")
	id := helperDormantID(t)
	var cap threadCapture
	pool, _ := helperPoolInjectedFactory(t, regPath, t.TempDir(), cap.factory,
		registryEntry{ID: id, Label: "conv-1", Harness: "codex", ThreadID: "thr-1"},
	)
	runPoolInBackground(t, pool)

	if _, err := pool.Revive(id, "conv-1", ""); err != nil {
		t.Fatalf("Revive: %v", err)
	}
	cfg := cap.config(t, id)
	if cfg.ThreadID != "thr-1" {
		t.Fatalf("RunnerConfig.ThreadID = %q, want the stored thr-1", cfg.ThreadID)
	}
	if cfg.RecordThread == nil {
		t.Fatal("RunnerConfig.RecordThread is nil")
	}

	if err := cfg.RecordThread(string(id), "thr-2"); err != nil {
		t.Fatalf("RecordThread: %v", err)
	}
	if got := entryByID(t, regPath, id); got == nil || got.ThreadID != "thr-2" {
		t.Fatalf("entry after RecordThread = %+v, want thread thr-2", got)
	}

	newID, err := pool.RotateForNewSession(id)
	if err != nil {
		t.Fatalf("RotateForNewSession: %v", err)
	}
	if got := entryByID(t, regPath, newID); got == nil || got.ThreadID != "" {
		t.Fatalf("rotated entry = %+v, want it present with no thread", got)
	}
	if err := cfg.RecordThread(string(id), "thr-stale"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("RecordThread for the old id = %v, want ErrSessionNotFound", err)
	}
	if err := cfg.RecordThread(string(newID), "thr-3"); err != nil {
		t.Fatalf("RecordThread for the new id: %v", err)
	}
	if got := entryByID(t, regPath, newID); got == nil || got.ThreadID != "thr-3" {
		t.Fatalf("rotated entry after RecordThread = %+v, want thread thr-3", got)
	}
	if got := entryByID(t, regPath, id); got != nil {
		t.Fatalf("old id still on disk: %+v", got)
	}
}
