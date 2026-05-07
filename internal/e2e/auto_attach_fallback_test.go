//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/pyrycode/pyrycode/internal/control"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// TestE2E_ForegroundAutoAttach_FallsThroughWhenSessionMissing — daemon
// running with at least one registered session, but pyry is invoked
// with a UUID that is NOT registered. Asserts: pyry falls through to
// supervisor mode (its own claude child) and the daemon's session
// registry is unchanged afterward.
//
// The foreground uses its own socket path (distinct from the daemon's)
// so runSupervisor's ctrl.Listen succeeds; structurally the fall-
// through is via stat ENOENT on the foreground's own socket. Gate-
// level discrimination ("session missing") is unit-tested in
// cmd/pyry/auto_attach_test.go; this test pins the system-level
// invariant that fall-through reaches supervisor mode.
func TestE2E_ForegroundAutoAttach_FallsThroughWhenSessionMissing(t *testing.T) {
	daemonSocket, registeredID := spawnDaemonWithRegisteredSession(t, "fallback-decoy")

	pre := mustSessionsList(t, daemonSocket)

	other := newCanonicalUUID(t)
	if other == registeredID {
		t.Fatalf("UUID collision; regenerate test")
	}

	c := startForegroundSupervised(t, other, nil)

	children, err := pgrepChildren(c.Pid)
	if err != nil {
		t.Skipf("e2e: pgrep unavailable: %v", err)
	}
	if len(children) == 0 {
		t.Fatalf("foreground pid=%d has no children; expected supervised claude\nstderr:\n%s",
			c.Pid, c.Stderr.String())
	}

	post := mustSessionsList(t, daemonSocket)
	if !sessionsEqual(pre, post) {
		t.Fatalf("daemon registry changed; pre=%v post=%v", pre, post)
	}
}

// TestE2E_ForegroundAutoAttach_FallsThroughWhenNoDaemon — no daemon
// running, no socket present. Asserts: pyry falls through to
// supervisor mode without attempting to read or connect to the
// control socket. Structural fall-through via stat ENOENT on the
// foreground's own (non-existent) socket.
func TestE2E_ForegroundAutoAttach_FallsThroughWhenNoDaemon(t *testing.T) {
	c := startForegroundSupervised(t, newCanonicalUUID(t), nil)

	children, err := pgrepChildren(c.Pid)
	if err != nil {
		t.Skipf("e2e: pgrep unavailable: %v", err)
	}
	if len(children) == 0 {
		t.Fatalf("foreground pid=%d has no children; expected supervised claude\nstderr:\n%s",
			c.Pid, c.Stderr.String())
	}
}

// TestE2E_ForegroundAutoAttach_RespectsEnvOverride — daemon running,
// requested session-id IS registered, but PYRY_NO_AUTO_ATTACH=1 is
// set in the foreground process's env. Asserts: pyry falls through to
// supervisor mode anyway.
//
// With separate sockets, the structural fall-through is via stat
// ENOENT on the foreground's own socket; this test pins that the
// override flag is read by the binary at startup, doesn't crash the
// foreground, and doesn't mutate the daemon's registry. Gate-level
// "env override skips probe" coverage lives in
// cmd/pyry/auto_attach_test.go.
func TestE2E_ForegroundAutoAttach_RespectsEnvOverride(t *testing.T) {
	daemonSocket, registeredID := spawnDaemonWithRegisteredSession(t, "fallback-override")

	pre := mustSessionsList(t, daemonSocket)

	c := startForegroundSupervised(t, registeredID, []string{"PYRY_NO_AUTO_ATTACH=1"})

	children, err := pgrepChildren(c.Pid)
	if err != nil {
		t.Skipf("e2e: pgrep unavailable: %v", err)
	}
	if len(children) == 0 {
		t.Fatalf("foreground pid=%d has no children; expected supervised claude\nstderr:\n%s",
			c.Pid, c.Stderr.String())
	}

	post := mustSessionsList(t, daemonSocket)
	if !sessionsEqual(pre, post) {
		t.Fatalf("daemon registry changed; pre=%v post=%v", pre, post)
	}
}

func mustSessionsList(t *testing.T, socket string) []control.SessionInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := control.SessionsList(ctx, socket)
	if err != nil {
		t.Fatalf("e2e: sessions.list: %v", err)
	}
	return list
}

// sessionsEqual compares two SessionInfo slices for byte-equivalent
// content. LastActive is compared with time.Equal — comparing wall
// values only — because the JSON roundtrip strips monotonic-clock
// state (see lessons.md § "JSON roundtrip strips monotonic-clock
// state").
func sessionsEqual(a, b []control.SessionInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID ||
			a[i].Label != b[i].Label ||
			a[i].State != b[i].State ||
			a[i].Bootstrap != b[i].Bootstrap ||
			!a[i].LastActive.Equal(b[i].LastActive) {
			return false
		}
	}
	return true
}

func newCanonicalUUID(t *testing.T) string {
	t.Helper()
	id, err := sessions.NewID()
	if err != nil {
		t.Fatalf("e2e: new uuid: %v", err)
	}
	return string(id)
}
