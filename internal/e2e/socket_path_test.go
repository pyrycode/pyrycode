//go:build e2e

package e2e

import (
	"net"
	"path/filepath"
	"testing"
)

// The test name is deliberately long: it is the fixture. On macOS t.TempDir()
// resolves under /var/folders/<hash>/T/<TestName>/NNN/ and embeds the sanitised
// test name, so the old socket derivation — filepath.Join(home, "pyry.sock") —
// produced a path whose length exceeds the 104-byte sun_path limit for a name
// this long. bind(2) then returns EINVAL and the daemon never reaches readiness.
//
// With the fix, spawnWith derives the socket via shortSocketPath (a short /tmp
// dir decoupled from HOME), so Start(t) reaches readiness and SocketPath stays
// well under 104 bytes regardless of the test name.
//
// To confirm this guard fails against the current derivation (AC-2): temporarily
// revert harness.go's `socket := shortSocketPath(t)` back to
// `filepath.Join(home, "pyry.sock")` and run on macOS — Start blocks on
// waitForReady, which times out (bind EINVAL) and t.Fatalf's before this test
// body ever runs the length assertion.
func TestE2E_SocketPath_DeliberatelyLongTestNameStaysUnderMacOSSunPathLimitRegressionGuard860(t *testing.T) {
	// Start blocks on waitForReady, which only returns once the control socket
	// is bound and dialable. Reaching the next line means the daemon bound the
	// socket — i.e. the derived path fit within sun_path.
	h := Start(t)

	// Deterministic, cross-platform invariant: the socket path never exceeds
	// macOS's 104-byte sun_path limit. Pre-fix on macOS this is the long
	// t.TempDir()-nested path (> 104); post-fix it is the short /tmp path.
	if len(h.SocketPath) > 104 {
		t.Fatalf("socket path length = %d, want <= 104 (sun_path limit)\npath: %s",
			len(h.SocketPath), h.SocketPath)
	}

	// AC-2 "binds and is dialable" made explicit (redundant with Start's
	// readiness gate, but states the contract directly).
	c, err := net.Dial("unix", h.SocketPath)
	if err != nil {
		t.Fatalf("dial control socket %q: %v", h.SocketPath, err)
	}
	_ = c.Close()

	// The decoupling: the socket no longer lives under HOME.
	if filepath.Dir(h.SocketPath) == h.HomeDir {
		t.Errorf("socket dir = HomeDir (%s); expected socket decoupled from HOME", h.HomeDir)
	}
}
