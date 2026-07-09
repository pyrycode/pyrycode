package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyrycode/pyrycode/internal/relay/handlers"
)

// TestResolveWorkspaceDir_WithinHome_ReturnsRealpath covers AC #1: an existing
// within-$HOME dir is confined to its canonical realpath and returned. Unlike
// resolveSpawnDir this pins no trustMark (change_workspace does not spawn) and
// creates nothing.
func TestResolveWorkspaceDir_WithinHome_ReturnsRealpath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "projects", "app")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatalf("EvalSymlinks(proj): %v", err)
	}

	got, err := resolveWorkspaceDir(proj)
	if err != nil {
		t.Fatalf("resolveWorkspaceDir(%q) error = %v", proj, err)
	}
	if got != want {
		t.Errorf("resolveWorkspaceDir = %q, want the confined realpath %q", got, want)
	}
}

// TestResolveWorkspaceDir_BareTilde_ExpandsToHome covers AC #1: a bare "~"
// expands to the daemon's $HOME before confinement (a client cannot know the
// daemon's absolute home), and resolves to EvalSymlinks(home) with no literal
// "~" surviving.
func TestResolveWorkspaceDir_BareTilde_ExpandsToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeReal, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks(home): %v", err)
	}

	got, err := resolveWorkspaceDir("~")
	if err != nil {
		t.Fatalf("resolveWorkspaceDir(\"~\") error = %v", err)
	}
	if got != homeReal {
		t.Errorf("resolveWorkspaceDir(\"~\") = %q, want EvalSymlinks(home) %q", got, homeReal)
	}
	if strings.Contains(got, "~") {
		t.Errorf("resolved path %q still contains a literal ~", got)
	}
}

// TestResolveWorkspaceDir_OutsideHome_Rejected covers AC #3: a dir resolving
// outside $HOME is rejected wrapping ErrWorkspaceRejected, and nothing is
// returned.
func TestResolveWorkspaceDir_OutsideHome_Rejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir() // sibling temp dir, not under home
	t.Setenv("HOME", home)

	got, err := resolveWorkspaceDir(outside)
	if err == nil {
		t.Fatalf("resolveWorkspaceDir(%q) = (%q, nil), want rejection", outside, got)
	}
	if !errors.Is(err, handlers.ErrWorkspaceRejected) {
		t.Errorf("error %v does not match ErrWorkspaceRejected", err)
	}
	if got != "" {
		t.Errorf("resolveWorkspaceDir returned %q on rejection, want \"\"", got)
	}
}

// TestResolveWorkspaceDir_NonExistent_RejectedNotCreated covers AC #3's "or is
// unresolvable" branch: a within-$HOME path that does not exist is rejected
// (EvalSymlinks fails) — and the STRICT confiner creates nothing, unlike the
// create path's confineWorkdirToHomeCreating.
func TestResolveWorkspaceDir_NonExistent_RejectedNotCreated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	missing := filepath.Join(home, "not", "there")

	got, err := resolveWorkspaceDir(missing)
	if err == nil {
		t.Fatalf("resolveWorkspaceDir(%q) = (%q, nil), want rejection of an unresolvable target", missing, got)
	}
	if !errors.Is(err, handlers.ErrWorkspaceRejected) {
		t.Errorf("error %v does not match ErrWorkspaceRejected", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Errorf("non-existent target %q was created (Stat err = %v), want never created", missing, statErr)
	}
}

// TestResolveWorkspaceDir_SymlinkEscapingHome_Rejected proves the target is
// resolved to its realpath before the bound: a symlink living under $HOME but
// pointing outside it is rejected (the symlink-escape case AC #3 names).
func TestResolveWorkspaceDir_SymlinkEscapingHome_Rejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)
	link := filepath.Join(home, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := resolveWorkspaceDir(link); err == nil {
		t.Fatalf("resolveWorkspaceDir(%q) = nil error, want rejection of escaping symlink", link)
	} else if !errors.Is(err, handlers.ErrWorkspaceRejected) {
		t.Errorf("error %v does not match ErrWorkspaceRejected", err)
	}
}
