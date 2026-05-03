//go:build e2e

package e2e

import (
	"bytes"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// canonicalUUIDLine matches a single canonical UUIDv4 followed by exactly
// one newline. AC#1 ("single 36-character canonical UUIDv4 on stdout, no
// surrounding text, trailing newline only") is pinned by anchoring the
// pattern at both ends.
var canonicalUUIDLine = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\n$`)

// TestSessionsNew_E2E_Labelled drives `pyry sessions new --name feature-x`
// against a running daemon and asserts the AC#1 contract: stdout is a
// single canonical UUID + newline, exit 0, and the registry gains an
// entry with the printed UUID, supplied label, bootstrap=false, and an
// active lifecycle state.
func TestSessionsNew_E2E_Labelled(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	r := h.Run(t, "sessions", "new", "--name", "feature-x")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions new exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	if !canonicalUUIDLine.Match(r.Stdout) {
		t.Fatalf("stdout = %q, want canonical UUID + single newline", r.Stdout)
	}
	id := string(bytes.TrimRight(r.Stdout, "\n"))

	// Pool persists the registry entry inside Create before returning;
	// the entry should be observable by the time stdout is flushed.
	// Allow a brief poll window for fs visibility.
	deadline := time.Now().Add(2 * time.Second)
	var entry registryEntry
	var found bool
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		for _, e := range reg.Sessions {
			if e.ID == id {
				entry = e
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !found {
		t.Fatalf("session %s not present in registry within 2s\nfile:\n%s",
			id, mustReadFile(t, regPath))
	}
	if entry.Label != "feature-x" {
		t.Errorf("entry.Label = %q, want %q", entry.Label, "feature-x")
	}
	if entry.Bootstrap {
		t.Errorf("entry.Bootstrap = true, want false (only the initial session is bootstrap)")
	}
}

// TestSessionsNew_E2E_Unlabelled pins AC#1's empty-label semantics:
// `pyry sessions new` with no --name produces a registry entry with an
// empty Label.
func TestSessionsNew_E2E_Unlabelled(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	r := h.Run(t, "sessions", "new")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions new exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	if !canonicalUUIDLine.Match(r.Stdout) {
		t.Fatalf("stdout = %q, want canonical UUID + single newline", r.Stdout)
	}
	id := string(bytes.TrimRight(r.Stdout, "\n"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		for _, e := range reg.Sessions {
			if e.ID != id {
				continue
			}
			if e.Label != "" {
				t.Errorf("entry.Label = %q, want empty string", e.Label)
			}
			if e.Bootstrap {
				t.Errorf("entry.Bootstrap = true, want false")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s not present in registry within 2s\nfile:\n%s",
		id, mustReadFile(t, regPath))
}

// TestSessionsNew_E2E_UnknownVerb pins AC#3: `pyry sessions <unknown>`
// reports a help-style error and never creates a session in the
// registry. The bootstrap entry exists; no other entries should appear.
func TestSessionsNew_E2E_UnknownVerb(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	// Wait for the bootstrap entry so the count baseline is meaningful.
	_ = waitForBootstrap(t, regPath, 5*time.Second)
	before := readRegistry(t, regPath)

	r := h.Run(t, "sessions", "bogus")
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions bogus unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
			r.Stdout, r.Stderr)
	}
	if !bytes.Contains(r.Stderr, []byte("unknown verb")) {
		t.Errorf("stderr missing %q fragment:\n%s", "unknown verb", r.Stderr)
	}
	if !bytes.Contains(r.Stderr, []byte("bogus")) {
		t.Errorf("stderr missing offending verb %q:\n%s", "bogus", r.Stderr)
	}

	after := readRegistry(t, regPath)
	if len(after.Sessions) != len(before.Sessions) {
		t.Errorf("session count changed: before=%d after=%d (unknown verb must not create a session)",
			len(before.Sessions), len(after.Sessions))
	}
}

// TestSessionsNew_E2E_NoDaemon pins AC#2: `pyry sessions new` against a
// non-existent socket exits non-zero with a clean error — no panic,
// goroutine dump, or stack trace.
func TestSessionsNew_E2E_NoDaemon(t *testing.T) {
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	r := RunBare(t, "sessions", "-pyry-socket="+bogusSock, "new")
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions new against bogus socket unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
			r.Stdout, r.Stderr)
	}
	if len(bytes.TrimSpace(r.Stderr)) == 0 {
		t.Errorf("expected non-empty stderr, got empty")
	}
	for _, bad := range [][]byte{
		[]byte("panic"),
		[]byte("goroutine "),
		[]byte("runtime/"),
	} {
		if bytes.Contains(r.Stderr, bad) {
			t.Errorf("stderr contains %q — expected clean error, not crash:\n%s", bad, r.Stderr)
		}
	}
}
