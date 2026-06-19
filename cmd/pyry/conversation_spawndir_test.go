package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// trustMarkRecorder captures the calls resolveSpawnDir makes to the package-level
// trustMark seam so each test can assert call count + the workdir argument.
type trustMarkRecorder struct {
	gotWorkdir string
	calls      int
}

// installRecordingTrustMark overrides the trustMark seam with a recorder that
// returns (ret, retErr) and restores the production value at test exit. Mirrors
// installFakeSeams in agent_run_test.go, scoped to the single seam resolveSpawnDir
// consumes. Tests using it set $HOME via t.Setenv, so they cannot run parallel.
func installRecordingTrustMark(t *testing.T, ret string, retErr error) *trustMarkRecorder {
	t.Helper()
	orig := trustMark
	t.Cleanup(func() { trustMark = orig })
	rec := &trustMarkRecorder{}
	trustMark = func(workdir string) (string, error) {
		rec.gotWorkdir = workdir
		rec.calls++
		if retErr != nil {
			return "", retErr
		}
		return ret, nil
	}
	return rec
}

// TestResolveSpawnDir_Empty_NoTrustMark covers the default-Cwd path: an empty
// requested dir resolves to ("", nil) — the pool spawns in the shared trusted
// template workdir — and trustMark is never called (no per-conversation mark).
func TestResolveSpawnDir_Empty_NoTrustMark(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rec := installRecordingTrustMark(t, "/should/not/be/returned", nil)

	got, err := resolveSpawnDir("")
	if err != nil {
		t.Fatalf("resolveSpawnDir(\"\") error = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("resolveSpawnDir(\"\") = %q, want \"\" (shared workdir)", got)
	}
	if rec.calls != 0 {
		t.Errorf("trustMark called %d times for an empty spawnDir, want 0", rec.calls)
	}
}

// TestResolveSpawnDir_WithinHome_TrustsRealpath covers AC#3: a within-$HOME dir
// is confined to its realpath, trust-marked once with that realpath, and the
// returned path is byte-identical to trustMark's return (NOT re-derived) so
// claude's cwd and the trust-marked path stay identical.
func TestResolveSpawnDir_WithinHome_TrustsRealpath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "projects", "app")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	wantConfined, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatalf("EvalSymlinks(proj): %v", err)
	}
	const trustedSentinel = "/trusted/realpath/sentinel"
	rec := installRecordingTrustMark(t, trustedSentinel, nil)

	got, err := resolveSpawnDir(proj)
	if err != nil {
		t.Fatalf("resolveSpawnDir(%q) error = %v", proj, err)
	}
	if got != trustedSentinel {
		t.Errorf("resolveSpawnDir = %q, want trustMark's return %q (byte-identical, AC#3)", got, trustedSentinel)
	}
	if rec.calls != 1 {
		t.Fatalf("trustMark called %d times, want exactly 1", rec.calls)
	}
	// confine runs before trust, so trustMark receives the confined realpath.
	if rec.gotWorkdir != wantConfined {
		t.Errorf("trustMark got %q, want the confined realpath %q", rec.gotWorkdir, wantConfined)
	}
}

// TestResolveSpawnDir_OutsideHome_Rejected covers the security bound: a dir
// resolving outside $HOME is rejected wrapping ErrSpawnDirRejected, and trustMark
// is never called (confine gates the $HOME bound before trust — trustMark has no
// $HOME bound of its own).
func TestResolveSpawnDir_OutsideHome_Rejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir() // sibling temp dir, not under home
	t.Setenv("HOME", home)
	rec := installRecordingTrustMark(t, "/unused", nil)

	got, err := resolveSpawnDir(outside)
	if err == nil {
		t.Fatalf("resolveSpawnDir(%q) = (%q, nil), want rejection", outside, got)
	}
	if !errors.Is(err, handlers.ErrSpawnDirRejected) {
		t.Errorf("error %v does not match ErrSpawnDirRejected", err)
	}
	if got != "" {
		t.Errorf("resolveSpawnDir returned %q on rejection, want \"\"", got)
	}
	if rec.calls != 0 {
		t.Errorf("trustMark called %d times for an escaping dir, want 0", rec.calls)
	}
}

// TestResolveSpawnDir_SymlinkEscapingHome_Rejected proves the candidate is
// resolved to its realpath before the bound: a symlink living under $HOME but
// pointing outside it is rejected (the symlink-escape case AC#2/AC#5 name).
func TestResolveSpawnDir_SymlinkEscapingHome_Rejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	link := filepath.Join(home, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	rec := installRecordingTrustMark(t, "/unused", nil)

	if _, err := resolveSpawnDir(link); err == nil {
		t.Fatalf("resolveSpawnDir(%q) = nil error, want rejection of escaping symlink", link)
	} else if !errors.Is(err, handlers.ErrSpawnDirRejected) {
		t.Errorf("error %v does not match ErrSpawnDirRejected", err)
	}
	if rec.calls != 0 {
		t.Errorf("trustMark called %d times for an escaping symlink, want 0", rec.calls)
	}
}

// TestResolveSpawnDir_TrustMarkError_NotRejectedSentinel covers the retryable
// branch: a within-$HOME dir whose trust-mark fails (a transient ~/.claude.json
// write error) returns the trust error PLAIN — it must NOT wrap ErrSpawnDirRejected,
// so the handler classifies it retryable rather than as a deterministic refusal.
func TestResolveSpawnDir_TrustMarkError_NotRejectedSentinel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	trustErr := errors.New("claude.json write failed")
	rec := installRecordingTrustMark(t, "", trustErr)

	_, err := resolveSpawnDir(proj)
	if err == nil {
		t.Fatalf("resolveSpawnDir(%q) = nil error, want the trust-mark error", proj)
	}
	if errors.Is(err, handlers.ErrSpawnDirRejected) {
		t.Errorf("trust-mark error matched ErrSpawnDirRejected; want a plain (retryable) error")
	}
	if !errors.Is(err, trustErr) {
		t.Errorf("error %v does not wrap the trust-mark error", err)
	}
	if rec.calls != 1 {
		t.Errorf("trustMark called %d times, want exactly 1 (confine passed, trust attempted)", rec.calls)
	}
}
