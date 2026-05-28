package agentrun

import (
	"errors"
	"io/fs"
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
