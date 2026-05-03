//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// TestSessionsRename_E2E_Success pins AC#1 happy path: mint a session
// labelled "before", rename it to "after" via CLI, exit 0, registry
// reflects the new label.
func TestSessionsRename_E2E_Success(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, h.SocketPath, "before")
	if err != nil {
		t.Fatalf("sessions.new: %v", err)
	}

	r := h.Run(t, "sessions", "rename", id, "after")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions rename exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		if entry, ok := findSession(reg, id); ok && entry.Label == "after" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s label did not become %q within 2s\nfile:\n%s",
		id, "after", mustReadFile(t, regPath))
}

// TestSessionsRename_E2E_EmptyLabelClear pins AC#1 empty-label clear:
// `pyry sessions rename <uuid> ""` succeeds and clears the label on
// disk. No `--clear` flag is needed.
func TestSessionsRename_E2E_EmptyLabelClear(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, h.SocketPath, "to-clear")
	if err != nil {
		t.Fatalf("sessions.new: %v", err)
	}

	r := h.Run(t, "sessions", "rename", id, "")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions rename %q \"\" exit=%d\nstdout:\n%s\nstderr:\n%s",
			id, r.ExitCode, r.Stdout, r.Stderr)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		if entry, ok := findSession(reg, id); ok && entry.Label == "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s label did not clear within 2s\nfile:\n%s",
		id, mustReadFile(t, regPath))
}

// TestSessionsRename_E2E_UnknownUUID pins AC#2 typed-error mapping: a
// canonical UUID not in the registry produces exit non-zero, an
// `no session with id "..."` stderr message, and no registry change.
func TestSessionsRename_E2E_UnknownUUID(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	_ = waitForBootstrap(t, regPath, 5*time.Second)
	before := readRegistry(t, regPath)

	missing := "00000000-0000-4000-8000-000000000000"
	r := h.Run(t, "sessions", "rename", missing, "anything")
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions rename <missing> unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
			r.Stdout, r.Stderr)
	}
	if !bytes.Contains(r.Stderr, []byte("no session with id")) {
		t.Errorf("stderr missing %q fragment:\n%s", "no session with id", r.Stderr)
	}
	if !bytes.Contains(r.Stderr, []byte(missing)) {
		t.Errorf("stderr missing original id %q:\n%s", missing, r.Stderr)
	}

	after := readRegistry(t, regPath)
	if len(after.Sessions) != len(before.Sessions) {
		t.Errorf("session count changed: before=%d after=%d", len(before.Sessions), len(after.Sessions))
	}
}

// TestSessionsRename_E2E_NoDaemon pins AC#2 dial-failure path: bogus
// socket exits non-zero with a clean error — no panic, goroutine dump,
// or stack trace. Mirrors TestSessionsRm_E2E_NoDaemon.
func TestSessionsRename_E2E_NoDaemon(t *testing.T) {
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	r := RunBare(t, "sessions", "-pyry-socket="+bogusSock, "rename",
		"00000000-0000-4000-8000-000000000000", "alpha")
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions rename against bogus socket unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
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

// TestSessionsRename_E2E_WrongArity pins AC#3 wrong-arity exit-2 path:
// only one positional exits 2, names the expected shape, and leaves
// the registry unchanged.
func TestSessionsRename_E2E_WrongArity(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	_ = waitForBootstrap(t, regPath, 5*time.Second)
	before := readRegistry(t, regPath)

	r := h.Run(t, "sessions", "rename", "00000000-0000-4000-8000-000000000000")
	if r.ExitCode != 2 {
		t.Fatalf("pyry sessions rename <id> (no label) exit=%d (want 2)\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	if !bytes.Contains(r.Stderr, []byte("expected <id> <new-label>")) {
		t.Errorf("stderr missing %q fragment:\n%s", "expected <id> <new-label>", r.Stderr)
	}

	after := readRegistry(t, regPath)
	if len(after.Sessions) != len(before.Sessions) {
		t.Errorf("session count changed: before=%d after=%d", len(before.Sessions), len(after.Sessions))
	}
}
