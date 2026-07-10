package agentrun

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveWorkdir_DarwinRealpath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only: /var symlink")
	}
	wd := t.TempDir()
	want, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", wd, err)
	}
	got, err := ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", wd, err)
	}
	// Property: ResolveWorkdir returns the symlink-resolved form of its input.
	if got != want {
		t.Fatalf("ResolveWorkdir(%q) = %q, want %q", wd, got, want)
	}
	// Delta: resolution actually fired (this is what distinguishes the
	// darwin-only test from TestResolveWorkdir_AlreadyResolved). t.TempDir()
	// crosses a macOS symlink — /var → /private/var by default, or
	// /tmp → /private/tmp under a $TMPDIR pointing at /tmp.
	if got == wd {
		t.Fatalf("ResolveWorkdir(%q) = %q; expected symlink resolution to change the path", wd, got)
	}
}

func TestResolveWorkdir_AlreadyResolved(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	resolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := ResolveWorkdir(resolved)
	if err != nil {
		t.Fatalf("ResolveWorkdir: %v", err)
	}
	if got != resolved {
		t.Fatalf("ResolveWorkdir(%q) = %q, want %q", resolved, got, resolved)
	}
}

func TestResolveWorkdir_RelativePath(t *testing.T) {
	t.Parallel()
	got, err := ResolveWorkdir(".")
	if err != nil {
		t.Fatalf("ResolveWorkdir(\".\"): %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("ResolveWorkdir(\".\") = %q, want absolute", got)
	}
}

func TestResolveWorkdir_MissingPath(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := ResolveWorkdir(missing)
	if err == nil {
		t.Fatalf("ResolveWorkdir(%q) = nil error; want non-nil", missing)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ResolveWorkdir(%q): error %v, want fs.ErrNotExist", missing, err)
	}
}

// TestResolveWorkdir_CaseMismatchFoldsToOnDiskCase is the #910 fix: a workdir
// whose configured case differs from the on-disk directory resolves to the
// on-disk case (AC-1, AC-5). Case-insensitive filesystem only — gated at
// runtime, not on runtime.GOOS, because macOS APFS can be case-sensitive.
func TestResolveWorkdir_CaseMismatchFoldsToOnDiskCase(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, "Workspace"), 0o755); err != nil {
		t.Fatalf("Mkdir Workspace: %v", err)
	}
	// Probe: if an all-caps spelling stats to the just-created dir, the fs
	// folds case; otherwise it is case-sensitive and there is nothing to fix.
	if _, err := os.Stat(filepath.Join(tmp, "WORKSPACE")); err != nil {
		t.Skip("case-sensitive filesystem: no wrong-case fold to test")
	}

	wrongCase := filepath.Join(tmp, "WorkSpace")
	got, err := ResolveWorkdir(wrongCase)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", wrongCase, err)
	}
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", tmp, err)
	}
	want := filepath.Join(resolvedTmp, "Workspace")
	if got != want {
		t.Fatalf("ResolveWorkdir(%q) = %q, want %q (on-disk case)", wrongCase, got, want)
	}
}

// TestResolveWorkdir_CorrectlyCasedMixedCaseUnchanged proves canonicalisation
// does not mangle a component whose case already matches disk (AC-3). Any fs.
func TestResolveWorkdir_CorrectlyCasedMixedCaseUnchanged(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "MixedCaseDir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir MixedCaseDir: %v", err)
	}
	got, err := ResolveWorkdir(dir)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", dir, err)
	}
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", tmp, err)
	}
	want := filepath.Join(resolvedTmp, "MixedCaseDir")
	if got != want {
		t.Fatalf("ResolveWorkdir(%q) = %q, want %q", dir, got, want)
	}
}

// TestResolveWorkdir_SiblingSafetyCaseSensitive pins the exact-match-first
// rule: on a case-sensitive filesystem holding both Workspace and workspace,
// resolving Workspace must never fold onto the sibling (security property).
// Case-sensitive fs only — gated at runtime by whether the second create
// collapses onto the first.
func TestResolveWorkdir_SiblingSafetyCaseSensitive(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	upper := filepath.Join(tmp, "Workspace")
	if err := os.Mkdir(upper, 0o755); err != nil {
		t.Fatalf("Mkdir Workspace: %v", err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "workspace"), 0o755); err != nil {
		t.Skip("case-insensitive filesystem: cannot hold two case-differing siblings")
	}
	got, err := ResolveWorkdir(upper)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", upper, err)
	}
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", tmp, err)
	}
	want := filepath.Join(resolvedTmp, "Workspace")
	if got != want {
		t.Fatalf("ResolveWorkdir(%q) = %q, want %q (must not resolve to sibling)", upper, got, want)
	}
}
