//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
)

// TestSessionsList_E2E_Table mints two labelled sessions on top of the
// bootstrap entry and asserts that `pyry sessions list` exits 0, prints
// the canonical header, lists every session with full 36-char UUIDs, and
// orders by last-active descending (the second-minted "beta" appears
// before the first-minted "alpha").
func TestSessionsList_E2E_Table(t *testing.T) {
	home, _ := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	alphaID, err := control.SessionsNew(ctx, h.SocketPath, "alpha")
	if err != nil {
		t.Fatalf("sessions.new alpha: %v", err)
	}
	// Sleep briefly to ensure last-active timestamps differ — the registry
	// records seconds-precision timestamps in some paths and we want
	// "beta" strictly newer than "alpha".
	time.Sleep(1100 * time.Millisecond)
	betaID, err := control.SessionsNew(ctx, h.SocketPath, "beta")
	if err != nil {
		t.Fatalf("sessions.new beta: %v", err)
	}

	r := h.Run(t, "sessions", "list")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions list exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	lines := strings.Split(strings.TrimRight(string(r.Stdout), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected header + 3 rows, got %d lines:\n%s", len(lines), r.Stdout)
	}

	// Header: tabwriter pads with two spaces between columns; the literal
	// column names are stable.
	if !strings.HasPrefix(lines[0], "UUID") {
		t.Errorf("expected header starting with UUID, got %q", lines[0])
	}
	for _, col := range []string{"UUID", "LABEL", "STATE", "LAST-ACTIVE"} {
		if !strings.Contains(lines[0], col) {
			t.Errorf("header missing column %q: %q", col, lines[0])
		}
	}

	// Locate alpha, beta, and the bootstrap row by index. UUIDs must
	// appear in their full 36-char canonical form (no truncation).
	idLineIdx := func(id string) int {
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], id) {
				return i
			}
		}
		return -1
	}
	ai := idLineIdx(alphaID)
	bi := idLineIdx(betaID)
	if ai < 0 {
		t.Errorf("alpha row not found (uuid %s) in:\n%s", alphaID, r.Stdout)
	}
	if bi < 0 {
		t.Errorf("beta row not found (uuid %s) in:\n%s", betaID, r.Stdout)
	}
	if ai > 0 && bi > 0 && bi >= ai {
		t.Errorf("ordering: expected beta (line %d) before alpha (line %d) — last-active descending\n%s",
			bi, ai, r.Stdout)
	}
	if ai > 0 && !strings.Contains(lines[ai], "alpha") {
		t.Errorf("alpha row missing label: %q", lines[ai])
	}
	if bi > 0 && !strings.Contains(lines[bi], "beta") {
		t.Errorf("beta row missing label: %q", lines[bi])
	}

	// A bootstrap row appears too — its label is "bootstrap" (substituted
	// by Pool.List per #87) and is neither alphaID nor betaID.
	foundBootstrap := false
	for i := 1; i < len(lines); i++ {
		if i == ai || i == bi {
			continue
		}
		if strings.Contains(lines[i], "bootstrap") {
			foundBootstrap = true
			break
		}
	}
	if !foundBootstrap {
		t.Errorf("expected a bootstrap row in:\n%s", r.Stdout)
	}
}

// TestSessionsList_E2E_JSON drives `pyry sessions list --json` and
// asserts the output decodes into {"sessions": [...]} with the expected
// labels and a non-zero last_active. Pins AC#2's "stable enough that
// jq '.sessions[].label' works".
func TestSessionsList_E2E_JSON(t *testing.T) {
	home, _ := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := control.SessionsNew(ctx, h.SocketPath, "alpha"); err != nil {
		t.Fatalf("sessions.new alpha: %v", err)
	}
	if _, err := control.SessionsNew(ctx, h.SocketPath, "beta"); err != nil {
		t.Fatalf("sessions.new beta: %v", err)
	}

	r := h.Run(t, "sessions", "list", "--json")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions list --json exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	var payload struct {
		Sessions []control.SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(r.Stdout, &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v\nstdout:\n%s", err, r.Stdout)
	}
	if len(payload.Sessions) < 3 {
		t.Fatalf("expected >= 3 sessions (bootstrap + 2 minted), got %d:\n%s",
			len(payload.Sessions), r.Stdout)
	}

	var sawBootstrap, sawAlpha, sawBeta bool
	for _, s := range payload.Sessions {
		if s.LastActive.IsZero() {
			t.Errorf("session %s has zero last_active", s.ID)
		}
		if s.Bootstrap {
			sawBootstrap = true
		}
		switch s.Label {
		case "alpha":
			sawAlpha = true
		case "beta":
			sawBeta = true
		}
	}
	if !sawBootstrap {
		t.Errorf("no entry with bootstrap=true in:\n%s", r.Stdout)
	}
	if !sawAlpha {
		t.Errorf("no entry with label %q in:\n%s", "alpha", r.Stdout)
	}
	if !sawBeta {
		t.Errorf("no entry with label %q in:\n%s", "beta", r.Stdout)
	}
}

// TestSessionsList_E2E_BootstrapOnly pins the empty/bootstrap-only AC:
// without minting any sessions, the table renders the header plus a
// single data row for the bootstrap entry.
func TestSessionsList_E2E_BootstrapOnly(t *testing.T) {
	home, regPath := newRegistryHome(t)
	claudeBin := writeSleepClaude(t, home)
	h := StartIn(t, home, "-pyry-claude="+claudeBin)

	bootstrapID := waitForBootstrap(t, regPath, 5*time.Second)

	r := h.Run(t, "sessions", "list")
	if r.ExitCode != 0 {
		t.Fatalf("pyry sessions list exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	// Output is: header line + single data row + trailing newline.
	// strings.Split on the trimmed output yields exactly 2 lines.
	lines := strings.Split(strings.TrimRight(string(r.Stdout), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected exactly 2 lines (header + 1 data row), got %d:\n%s",
			len(lines), r.Stdout)
	}
	if !strings.HasPrefix(lines[1], bootstrapID) {
		t.Errorf("data row does not start with bootstrap UUID %s: %q",
			bootstrapID, lines[1])
	}
}

// TestSessionsList_E2E_NoDaemon pins AC#3: `pyry sessions list` against
// a non-existent socket exits non-zero with a clean error — no panic,
// goroutine dump, or stack trace. Mirrors TestSessionsNew_E2E_NoDaemon.
func TestSessionsList_E2E_NoDaemon(t *testing.T) {
	bogusSock := filepath.Join(t.TempDir(), "no-such.sock")

	r := RunBare(t, "sessions", "-pyry-socket="+bogusSock, "list")
	if r.ExitCode == 0 {
		t.Fatalf("pyry sessions list against bogus socket unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s",
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
