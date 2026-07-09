package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// TestResolveWorkspaceFolder_CreatesUnderParent_ReturnsRealpath covers AC #1 + #4:
// a fresh folder name under an existing within-$HOME parent is created on disk
// (0700) and the returned path is its EvalSymlinks realpath, landing directly
// under the parent.
func TestResolveWorkspaceFolder_CreatesUnderParent_ReturnsRealpath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "workspace")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatalf("EvalSymlinks(parent): %v", err)
	}
	want := filepath.Join(parentReal, "new-app")

	got, err := resolveWorkspaceFolder(parent, "new-app")
	if err != nil {
		t.Fatalf("resolveWorkspaceFolder error = %v", err)
	}
	if got != want {
		t.Errorf("resolveWorkspaceFolder = %q, want the created realpath %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("created folder %q missing: %v", got, err)
	}
	if !info.IsDir() {
		t.Errorf("created path %q is not a directory", got)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("created folder %q mode = %o, want 0700", got, perm)
	}
}

// TestResolveWorkspaceFolder_Idempotent covers AC #5: creating a folder that
// already exists succeeds and returns the same realpath, no error (MkdirAll
// semantics — no clobber, no error on an existing directory).
func TestResolveWorkspaceFolder_Idempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := filepath.Join(home, "workspace")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	first, err := resolveWorkspaceFolder(parent, "proj")
	if err != nil {
		t.Fatalf("first resolveWorkspaceFolder error = %v", err)
	}
	second, err := resolveWorkspaceFolder(parent, "proj")
	if err != nil {
		t.Fatalf("second (idempotent) resolveWorkspaceFolder error = %v", err)
	}
	if first != second {
		t.Errorf("idempotent create returned differing paths: %q vs %q", first, second)
	}
}

// TestResolveWorkspaceFolder_EscapeRejected_CreatesNothing covers AC #2: a parent
// that resolves outside $HOME (a symlink under $HOME pointing to an external dir)
// is rejected wrapping ErrWorkspaceFolderRejected, and no folder is created under
// the escape target — the containment check runs against the symlink-resolved
// candidate BEFORE MkdirAll.
func TestResolveWorkspaceFolder_EscapeRejected_CreatesNothing(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	link := filepath.Join(home, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := resolveWorkspaceFolder(link, "new-app")
	if err == nil {
		t.Fatalf("resolveWorkspaceFolder(%q, ...) = (%q, nil), want rejection", link, got)
	}
	if !errors.Is(err, handlers.ErrWorkspaceFolderRejected) {
		t.Errorf("error %v does not match ErrWorkspaceFolderRejected", err)
	}
	if got != "" {
		t.Errorf("resolveWorkspaceFolder returned %q on rejection, want \"\"", got)
	}
	target := filepath.Join(outside, "new-app")
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("escaping target %q was created (Stat err = %v), want never created", target, statErr)
	}
}

// TestResolveWorkspaceFolder_TildeParent_ExpandsToHome covers the tilde-parent
// wrapper (parity with resolveWorkspaceDir): a bare "~" parent expands to the
// daemon's $HOME before the join + confine, so the folder lands under the real
// home and no literal "~" survives.
func TestResolveWorkspaceFolder_TildeParent_ExpandsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}
	want := filepath.Join(homeReal, "tilde-folder")

	got, err := resolveWorkspaceFolder("~", "tilde-folder")
	if err != nil {
		t.Fatalf("resolveWorkspaceFolder(\"~\", ...) error = %v", err)
	}
	if got != want {
		t.Errorf("resolveWorkspaceFolder(\"~\", ...) = %q, want %q under real home", got, want)
	}
	if _, statErr := os.Stat(want); statErr != nil {
		t.Errorf("folder %q not created under home: %v", want, statErr)
	}
}
