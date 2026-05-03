//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// findSession returns the registry entry with the given id, or a zero
// value + false when absent.
func findSession(reg registryFile, id string) (registryEntry, bool) {
	for _, e := range reg.Sessions {
		if e.ID == id {
			return e, true
		}
	}
	return registryEntry{}, false
}

// TestSessionsRm_E2E_Success_Default pins AC#1 happy path: mint a
// session via the wire, remove it via CLI with no flags, exit 0,
// registry entry is gone.
func TestSessionsRm_E2E_Success_Default(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, h.SocketPath, "to-remove")
	if err != nil {
		t.Fatalf("sessions.new: %v", err)
	}

	r := h.Run(t, "sessions", "rm", id)
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions rm exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	// Pool persists synchronously on Remove; allow a brief poll for fs visibility.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		if _, ok := findSession(reg, id); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s still present in registry within 2s\nfile:\n%s",
		id, mustReadFile(t, regPath))
}

// TestSessionsRm_E2E_Success_Prefix pins AC#1 prefix-resolution branch:
// mint a session, run `pyry sessions rm <first-8-chars>`, exit 0,
// registry entry is gone.
func TestSessionsRm_E2E_Success_Prefix(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, h.SocketPath, "prefixed")
	if err != nil {
		t.Fatalf("sessions.new: %v", err)
	}
	prefix := id[:8]

	r := h.Run(t, "sessions", "rm", prefix)
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions rm %q exit=%d\nstdout:\n%s\nstderr:\n%s",
			prefix, r.ExitCode, r.Stdout, r.Stderr)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		if _, ok := findSession(reg, id); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s still present in registry within 2s\nfile:\n%s",
		id, mustReadFile(t, regPath))
}

// TestSessionsRm_E2E_Success_Archive pins AC#1 + AC#2's --archive
// branch: the flag plumbs through to a successful invocation.
// (Filesystem JSONL state is owned by #98's wire-layer tests; this
// test asserts only the CLI delivers exit 0 + registry-entry-gone.)
func TestSessionsRm_E2E_Success_Archive(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, h.SocketPath, "archive-me")
	if err != nil {
		t.Fatalf("sessions.new: %v", err)
	}

	r := h.Run(t, "sessions", "rm", "--archive", id)
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions rm --archive exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		if _, ok := findSession(reg, id); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s still present in registry within 2s\nfile:\n%s",
		id, mustReadFile(t, regPath))
}

// TestSessionsRm_E2E_Success_Purge pins AC#1 + AC#2's --purge branch:
// symmetric to _Archive.
func TestSessionsRm_E2E_Success_Purge(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id, err := control.SessionsNew(ctx, h.SocketPath, "purge-me")
	if err != nil {
		t.Fatalf("sessions.new: %v", err)
	}

	r := h.Run(t, "sessions", "rm", "--purge", id)
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions rm --purge exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reg := readRegistry(t, regPath)
		if _, ok := findSession(reg, id); !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s still present in registry within 2s\nfile:\n%s",
		id, mustReadFile(t, regPath))
}

// TestSessionsRm_E2E_AmbiguousPrefix pins AC#3's ambiguous-prefix path.
// Mints sessions via the wire until two share the same first hex char
// (pigeonhole guarantees a collision within 17 mints over 16 hex
// digits — typical run finds one in <=5). Running `pyry sessions rm
// <shared-char>` must exit non-zero with every matching UUID and its
// label printed on stderr, and leave both sessions in the registry.
//
// Pre-seeded non-bootstrap registry entries are NOT loaded into the
// in-memory Pool on warm start (Pool.New hydrates only the bootstrap;
// other entries persist on disk between restarts via the
// "warm-start does not save" rule). Hence the live-mint approach.
func TestSessionsRm_E2E_AmbiguousPrefix(t *testing.T) {
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

	r := h.Run(t, "sessions", "rm", prefix)
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions rm %q unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
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
		if _, ok := findSession(reg, m.id); !ok {
			t.Errorf("session %s missing after ambiguous rm\nfile:\n%s",
				m.id, mustReadFile(t, regPath))
		}
	}
}

// TestSessionsRm_E2E_UnknownUUID pins AC#3's unknown-id path: a
// canonical UUID not in the registry produces exit non-zero, an
// `no session with id "..."` stderr message, and no registry change.
func TestSessionsRm_E2E_UnknownUUID(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	_ = waitForBootstrap(t, regPath, 5*time.Second)
	before := readRegistry(t, regPath)

	missing := "00000000-0000-4000-8000-000000000000"
	r := h.Run(t, "sessions", "rm", missing)
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions rm <missing> unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
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

// TestSessionsRm_E2E_BootstrapRejected pins AC#3's bootstrap path: a
// `pyry sessions rm <bootstrap-uuid>` is rejected with the exact
// "cannot remove bootstrap session" stderr message and the bootstrap
// entry still in the registry.
func TestSessionsRm_E2E_BootstrapRejected(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)

	r := h.Run(t, "sessions", "rm", bootstrapID)
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions rm <bootstrap> unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
			r.Stdout, r.Stderr)
	}
	got := strings.TrimRight(string(r.Stderr), "\n")
	if got != "cannot remove bootstrap session" {
		t.Errorf("stderr = %q, want exactly %q", got, "cannot remove bootstrap session")
	}

	reg := readRegistry(t, regPath)
	entry, ok := findSession(reg, bootstrapID)
	if !ok {
		t.Fatalf("bootstrap entry %s missing after rejected rm\nfile:\n%s",
			bootstrapID, mustReadFile(t, regPath))
	}
	if !entry.Bootstrap {
		t.Errorf("entry.Bootstrap = false, want true")
	}
}

// TestSessionsRm_E2E_FlagsExclusive pins AC#2's mutually-exclusive
// usage error: `--archive --purge` exits 2, stderr names the
// constraint, registry is unchanged.
func TestSessionsRm_E2E_FlagsExclusive(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	_ = waitForBootstrap(t, regPath, 5*time.Second)
	before := readRegistry(t, regPath)

	r := h.Run(t, "sessions", "rm", "--archive", "--purge",
		"00000000-0000-4000-8000-000000000000")
	if r.ExitCode != 2 {
		t.Fatalf("pyry sessions rm --archive --purge exit=%d (want 2)\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}
	if !bytes.Contains(r.Stderr, []byte("mutually exclusive")) {
		t.Errorf("stderr missing %q fragment:\n%s", "mutually exclusive", r.Stderr)
	}

	after := readRegistry(t, regPath)
	if len(after.Sessions) != len(before.Sessions) {
		t.Errorf("session count changed: before=%d after=%d", len(before.Sessions), len(after.Sessions))
	}
}

// TestSessionsRm_E2E_NoDaemon pins AC#3's dial-failure path: bogus
// socket exits non-zero with a clean error — no panic, goroutine
// dump, or stack trace. Mirrors TestSessionsNew_E2E_NoDaemon.
func TestSessionsRm_E2E_NoDaemon(t *testing.T) {
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	r := RunBare(t, "sessions", "-pyry-socket="+bogusSock, "rm",
		"00000000-0000-4000-8000-000000000000")
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions rm against bogus socket unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
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
