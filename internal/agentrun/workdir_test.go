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
	got, err := ResolveWorkdir(wd)
	if err != nil {
		t.Fatalf("ResolveWorkdir(%q): %v", wd, err)
	}
	const want = "/private/var/"
	if len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("ResolveWorkdir(%q) = %q, want prefix %q", wd, got, want)
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
