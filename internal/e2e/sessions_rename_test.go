//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
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

// TestSessionsRename_E2E_Success_Prefix pins AC#1 prefix-resolution
// branch: mint a session labelled "before", run
// `pyry sessions rename <first-8-chars> after`, exit 0, registry entry's
// label flips to "after".
func TestSessionsRename_E2E_Success_Prefix(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, h.SocketPath, "before")
	if err != nil {
		t.Fatalf("sessions.new: %v", err)
	}
	prefix := id[:8]

	r := h.Run(t, "sessions", "rename", prefix, "after")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions rename %q after exit=%d\nstdout:\n%s\nstderr:\n%s",
			prefix, r.ExitCode, r.Stdout, r.Stderr)
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

// TestSessionsRename_E2E_AmbiguousPrefix pins AC#2's ambiguous-prefix
// path. Mints sessions via the wire until two share the same first hex
// char (pigeonhole guarantees a collision within 17 mints over 16 hex
// digits). Running `pyry sessions rename <shared-char> should-not-apply`
// must exit non-zero with every matching UUID and its label printed on
// stderr, and leave both sessions' labels unchanged on disk — the
// resolver bails before any wire mutation.
func TestSessionsRename_E2E_AmbiguousPrefix(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Bound: pigeonhole guarantees a collision in <=17 mints over the
	// 16 hex-digit first-char alphabet. Failure here means UUIDv4
	// generation is broken, not flakiness.
	const maxMints = 17

	type minted struct {
		id    string
		label string
	}
	byFirstChar := make(map[byte]minted, 16)
	prefix := ""
	var collided [2]minted
	for i := 0; i < maxMints && prefix == ""; i++ {
		label := fmt.Sprintf("amb-%d", i)
		id, err := control.SessionsNew(ctx, h.SocketPath, label)
		if err != nil {
			t.Fatalf("sessions.new amb-%d: %v", i, err)
		}
		first := id[0]
		if other, ok := byFirstChar[first]; ok {
			prefix = string(first)
			collided = [2]minted{other, {id: id, label: label}}
			break
		}
		byFirstChar[first] = minted{id: id, label: label}
	}
	if prefix == "" {
		t.Fatalf("no first-char collision after %d mints — UUID generation broken?", maxMints)
	}

	r := h.Run(t, "sessions", "rename", prefix, "should-not-apply")
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions rename %q unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
			prefix, r.Stdout, r.Stderr)
	}
	for _, m := range collided {
		if !bytes.Contains(r.Stderr, []byte(m.id)) {
			t.Errorf("stderr missing matched id %q:\n%s", m.id, r.Stderr)
		}
		if !bytes.Contains(r.Stderr, []byte(m.label)) {
			t.Errorf("stderr missing matched label %q:\n%s", m.label, r.Stderr)
		}
	}

	reg := readRegistry(t, regPath)
	for _, m := range collided {
		entry, ok := findSession(reg, m.id)
		if !ok {
			t.Errorf("session %s missing after ambiguous rename\nfile:\n%s",
				m.id, mustReadFile(t, regPath))
			continue
		}
		if entry.Label != m.label {
			t.Errorf("session %s label = %q, want unchanged %q",
				m.id, entry.Label, m.label)
		}
	}
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
