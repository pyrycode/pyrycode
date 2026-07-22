package sessions

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// #1164: the bootstrap resolves its pinned --session-id safely when its
// transcript already exists on disk. claude 2.1.199 refuses --session-id for an
// existing transcript ("Session ID <uuid> is already in use") and the PTY
// supervisor had no --resume branch to escape to, so a hard daemon restart (the
// transcript survives on disk) crash-looped. These tests drive Pool.New with a
// real /bin/sh argv recorder (no live claude) and prove the per-spawn by-id
// probe picks --resume (transcript present) vs --session-id (absent), without a
// dir scan that a foreign transcript could redirect.

// seedBootstrapRegistry writes a warm-start registry pinning id as the bootstrap.
func seedBootstrapRegistry(t *testing.T, regPath string, id SessionID) {
	t.Helper()
	when, _ := time.Parse(time.RFC3339Nano, "2026-04-01T00:00:00Z")
	if err := saveRegistryLocked(regPath, &registryFile{
		Version: 1,
		Sessions: []registryEntry{{
			ID: id, CreatedAt: when, LastActiveAt: when, Bootstrap: true,
		}},
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
}

// writeTranscript creates <id>.jsonl in claudeDir, mimicking a transcript that
// survived on disk across a hard restart (or that a foreign claude wrote).
func writeTranscript(t *testing.T, claudeDir string, id SessionID) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(claudeDir, string(id)+".jsonl"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("write transcript %q: %v", id, err)
	}
}

// TestPool_BootstrapWarmStart_ExistingTranscript_Resumes (AC-1): the pinned
// bootstrap id already has a transcript on disk (a hard restart left it behind).
// The spawn reattaches with --resume <id> — no --session-id refusal, no crash-loop.
func TestPool_BootstrapWarmStart_ExistingTranscript_Resumes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	claudeDir := t.TempDir()

	bootID := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	seedBootstrapRegistry(t, regPath, bootID)
	writeTranscript(t, claudeDir, bootID)

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir, claudeDir)
	runPoolInBackground(t, pool)

	got := waitArgv(t, tplWorkDir)
	want := []string{"--resume", string(bootID)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bootstrap argv = %v, want %v (existing transcript → --resume)", got, want)
	}
}

// TestPool_BootstrapWarmStart_NoTranscript_CreateSemantics (AC-2): the pinned
// bootstrap id has no transcript on disk. Behaviour is byte-identical to #839 —
// --session-id <id> create semantics, no --resume.
func TestPool_BootstrapWarmStart_NoTranscript_CreateSemantics(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	claudeDir := t.TempDir() // empty: no <bootID>.jsonl

	bootID := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	seedBootstrapRegistry(t, regPath, bootID)

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir, claudeDir)
	runPoolInBackground(t, pool)

	got := waitArgv(t, tplWorkDir)
	want := []string{"--session-id", string(bootID)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bootstrap argv = %v, want %v (absent transcript → --session-id create)", got, want)
	}
}

// TestPool_BootstrapWarmStart_ForeignTranscript_IgnoredByIdProbe (AC-3): a
// foreign <uuid>.jsonl (from a second claude in the same shared dir) is present
// and newer, but the pinned id's own transcript is absent. The by-id probe stats
// exactly <bootID>.jsonl — it does not scan the dir — so the foreign file cannot
// redirect the decision: the spawn still uses --session-id <bootID> create
// semantics (the #839 isolation guarantee).
func TestPool_BootstrapWarmStart_ForeignTranscript_IgnoredByIdProbe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regPath := filepath.Join(dir, "sessions.json")
	tplWorkDir := t.TempDir()
	claudeDir := t.TempDir()

	bootID := SessionID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	foreign := SessionID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	seedBootstrapRegistry(t, regPath, bootID)
	writeTranscript(t, claudeDir, foreign) // foreign present; bootID absent

	pool := helperPoolArgvRecorder(t, regPath, tplWorkDir, claudeDir)
	runPoolInBackground(t, pool)

	got := waitArgv(t, tplWorkDir)
	want := []string{"--session-id", string(bootID)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bootstrap argv = %v, want %v (by-id probe must ignore foreign %q)", got, want, foreign)
	}
}
