package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// TestResolveSpawnDir_BareTilde_ExpandsToHome covers AC#1: a bare "~" expands
// to the daemon's $HOME (never a literal "~" segment under the process cwd). The
// resolved realpath trustMark receives is EvalSymlinks(home), and no "~" literal
// survives into the path.
func TestResolveSpawnDir_BareTilde_ExpandsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}
	const trustedSentinel = "/trusted/realpath/sentinel"
	rec := installRecordingTrustMark(t, trustedSentinel, nil)

	got, err := resolveSpawnDir("~")
	if err != nil {
		t.Fatalf("resolveSpawnDir(\"~\") error = %v", err)
	}
	if got != trustedSentinel {
		t.Errorf("resolveSpawnDir = %q, want trustMark's return %q", got, trustedSentinel)
	}
	if rec.calls != 1 {
		t.Fatalf("trustMark called %d times, want exactly 1", rec.calls)
	}
	if rec.gotWorkdir != homeReal {
		t.Errorf("trustMark got %q, want EvalSymlinks(home) %q", rec.gotWorkdir, homeReal)
	}
	if strings.Contains(rec.gotWorkdir, "~") {
		t.Errorf("resolved path %q still contains a literal ~", rec.gotWorkdir)
	}
}

// TestResolveSpawnDir_TildePrefixMissing_ExpandsCreatesResolves covers AC#1 +
// AC#2: a "~/"-prefixed path whose dir (and parents) do not yet exist expands to
// $HOME, is created under $HOME, and resolves so the spawn proceeds. The realpath
// trustMark receives is an existing directory equal to EvalSymlinks(home)/.pyrycode/scratch.
func TestResolveSpawnDir_TildePrefixMissing_ExpandsCreatesResolves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}
	const trustedSentinel = "/trusted/realpath/sentinel"
	rec := installRecordingTrustMark(t, trustedSentinel, nil)

	got, err := resolveSpawnDir("~/.pyrycode/scratch")
	if err != nil {
		t.Fatalf("resolveSpawnDir(\"~/.pyrycode/scratch\") error = %v", err)
	}
	if got != trustedSentinel {
		t.Errorf("resolveSpawnDir = %q, want trustMark's return %q", got, trustedSentinel)
	}
	if rec.calls != 1 {
		t.Fatalf("trustMark called %d times, want exactly 1", rec.calls)
	}
	want := filepath.Join(homeReal, ".pyrycode", "scratch")
	if rec.gotWorkdir != want {
		t.Errorf("resolved realpath = %q, want %q", rec.gotWorkdir, want)
	}
	info, err := os.Stat(rec.gotWorkdir)
	if err != nil {
		t.Fatalf("resolved path %q does not exist after resolve: %v", rec.gotWorkdir, err)
	}
	if !info.IsDir() {
		t.Errorf("resolved path %q is not a directory", rec.gotWorkdir)
	}
}

// TestResolveSpawnDir_SymlinkedAncestorEscaping_RejectedNotCreated is the key new
// security regression guard for AC#3: a symlinked ancestor under $HOME pointing
// outside it (with a not-yet-existing suffix) is rejected, trustMark is never
// called, and the escaping target is NEVER created — the containment check runs
// against the symlink-resolved candidate BEFORE MkdirAll.
func TestResolveSpawnDir_SymlinkedAncestorEscaping_RejectedNotCreated(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	link := filepath.Join(home, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	rec := installRecordingTrustMark(t, "/unused", nil)

	if _, err := resolveSpawnDir("~/link/scratch"); err == nil {
		t.Fatalf("resolveSpawnDir(\"~/link/scratch\") = nil error, want rejection of symlinked-ancestor escape")
	} else if !errors.Is(err, handlers.ErrSpawnDirRejected) {
		t.Errorf("error %v does not match ErrSpawnDirRejected", err)
	}
	if rec.calls != 0 {
		t.Errorf("trustMark called %d times for an escaping symlinked ancestor, want 0", rec.calls)
	}
	target := filepath.Join(outside, "scratch")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("escaping target %q was created (Stat err = %v), want never created", target, err)
	}
}

// TestResolveSpawnDir_DeepMissingChain_CreatesAllParents guards the longest-
// existing-ancestor walk past a single level: a "~/a/b/c" whose every component
// is missing is created in full (each dir 0o700) and resolves under $HOME.
func TestResolveSpawnDir_DeepMissingChain_CreatesAllParents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}
	const trustedSentinel = "/trusted/realpath/sentinel"
	rec := installRecordingTrustMark(t, trustedSentinel, nil)

	got, err := resolveSpawnDir("~/a/b/c")
	if err != nil {
		t.Fatalf("resolveSpawnDir(\"~/a/b/c\") error = %v", err)
	}
	if got != trustedSentinel {
		t.Errorf("resolveSpawnDir = %q, want trustMark's return %q", got, trustedSentinel)
	}
	want := filepath.Join(homeReal, "a", "b", "c")
	if rec.gotWorkdir != want {
		t.Errorf("resolved realpath = %q, want %q", rec.gotWorkdir, want)
	}
	for _, p := range []string{
		filepath.Join(homeReal, "a"),
		filepath.Join(homeReal, "a", "b"),
		filepath.Join(homeReal, "a", "b", "c"),
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("expected created dir %q missing: %v", p, err)
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", p)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("created dir %q mode = %o, want 0700", p, perm)
		}
	}
}

// TestResolveSpawnDir_TildeUser_NotExpanded pins the minimal-expansion boundary:
// only a leading "~/" and a bare "~" expand. A "~user" form is treated as a
// literal segment, fails resolution under the process cwd, and is rejected.
func TestResolveSpawnDir_TildeUser_NotExpanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rec := installRecordingTrustMark(t, "/unused", nil)

	if _, err := resolveSpawnDir("~root/x"); err == nil {
		t.Fatalf("resolveSpawnDir(\"~root/x\") = nil error, want rejection (no ~user expansion)")
	} else if !errors.Is(err, handlers.ErrSpawnDirRejected) {
		t.Errorf("error %v does not match ErrSpawnDirRejected", err)
	}
	if rec.calls != 0 {
		t.Errorf("trustMark called %d times for an unexpanded ~user path, want 0", rec.calls)
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
