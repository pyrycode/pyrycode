package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWithinDir pins the boundary-aware containment test that bounds the
// daemon's workspace-trust auto-accept to $HOME. The /home/userfoo case is the
// security-critical one: a naive string-prefix check would treat it as inside
// /home/user and trust a sibling user's space.
func TestWithinDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dir  string
		path string
		want bool
	}{
		{"dir itself", "/home/user", "/home/user", true},
		{"direct child", "/home/user", "/home/user/proj", true},
		{"deeply nested", "/home/user", "/home/user/a/b/c", true},
		{"sibling prefix", "/home/user", "/home/userfoo", false},
		{"sibling", "/home/user", "/home/other", false},
		{"ancestor", "/home/user", "/home", false},
		{"root parent", "/home/user", "/", false},
		{"unrelated", "/home/user", "/srv/data", false},
		{"everything under root", "/", "/srv/data", true},
		{"root under root", "/", "/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := withinDir(tc.dir, tc.path); got != tc.want {
				t.Errorf("withinDir(%q, %q) = %v, want %v", tc.dir, tc.path, got, tc.want)
			}
		})
	}
}

// TestConfineWorkdirToHome_AcceptsWithinHome covers the happy path: a workdir
// at or below $HOME resolves to its realpath and is accepted. Uses t.Setenv so
// it cannot run in parallel.
func TestConfineWorkdirToHome_AcceptsWithinHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("home itself", func(t *testing.T) {
		want, err := filepath.EvalSymlinks(home)
		if err != nil {
			t.Fatalf("EvalSymlinks(home): %v", err)
		}
		got, err := confineWorkdirToHome(home)
		if err != nil {
			t.Fatalf("confineWorkdirToHome(home): %v", err)
		}
		if got != want {
			t.Errorf("realpath = %q, want %q", got, want)
		}
	})

	t.Run("nested under home", func(t *testing.T) {
		proj := filepath.Join(home, "projects", "app")
		if err := os.MkdirAll(proj, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		want, err := filepath.EvalSymlinks(proj)
		if err != nil {
			t.Fatalf("EvalSymlinks(proj): %v", err)
		}
		got, err := confineWorkdirToHome(proj)
		if err != nil {
			t.Fatalf("confineWorkdirToHome(proj): %v", err)
		}
		if got != want {
			t.Errorf("realpath = %q, want %q", got, want)
		}
	})
}

// TestConfineWorkdirToHome_EmptyResolvesToCwd verifies the unset-flag case:
// an empty workdir resolves to the process working directory (matching how the
// supervisor launches claude with no -pyry-workdir). HOME is set to the cwd so
// the resolved path is inside $HOME and is accepted.
func TestConfineWorkdirToHome_EmptyResolvesToCwd(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Setenv("HOME", cwd)

	want, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("EvalSymlinks(cwd): %v", err)
	}
	got, err := confineWorkdirToHome("")
	if err != nil {
		t.Fatalf("confineWorkdirToHome(\"\"): %v", err)
	}
	if got != want {
		t.Errorf("empty workdir resolved to %q, want cwd %q", got, want)
	}
}

// TestConfineWorkdirToHome_RejectsOutsideHome covers the security bound: a
// workdir resolving outside $HOME is rejected with a content-free error naming
// the path and the boundary — never the contents of ~/.claude.json.
func TestConfineWorkdirToHome_RejectsOutsideHome(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir() // sibling temp dir, not under home
	t.Setenv("HOME", home)

	_, err := confineWorkdirToHome(outside)
	if err == nil {
		t.Fatalf("confineWorkdirToHome(%q) = nil error, want rejection", outside)
	}
	msg := err.Error()
	if !strings.Contains(msg, "outside the home directory") {
		t.Errorf("error %q does not explain the $HOME boundary", msg)
	}
	if strings.Contains(msg, ".claude.json") {
		t.Errorf("rejection error leaks the trust file path: %q", msg)
	}
}

// TestConfineWorkdirToHome_CanonicalisesBothSides proves the home side is
// EvalSymlinks-resolved too: with $HOME pointed at a symlink to the real home,
// a workdir under the real home must still be accepted. Without canonicalising
// the home side this would be a false reject (the #118/#221 gotcha).
func TestConfineWorkdirToHome_CanonicalisesBothSides(t *testing.T) {
	realHome := t.TempDir()
	symHome := filepath.Join(t.TempDir(), "homelink")
	if err := os.Symlink(realHome, symHome); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("HOME", symHome)

	proj := filepath.Join(realHome, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatalf("EvalSymlinks(proj): %v", err)
	}
	got, err := confineWorkdirToHome(proj)
	if err != nil {
		t.Fatalf("confineWorkdirToHome with symlinked HOME rejected a valid workdir: %v", err)
	}
	if got != want {
		t.Errorf("realpath = %q, want %q", got, want)
	}
}

// TestConfineWorkdirToHome_RejectsSymlinkEscapingHome proves the workdir side is
// resolved to its realpath before the bound is applied: a symlink that lives
// under $HOME but points outside it is rejected, because claude keys trust on
// the realpath, not the symlink.
func TestConfineWorkdirToHome_RejectsSymlinkEscapingHome(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	t.Setenv("HOME", home)

	link := filepath.Join(home, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := confineWorkdirToHome(link); err == nil {
		t.Fatalf("confineWorkdirToHome(%q) = nil error, want rejection of escaping symlink", link)
	}
}
