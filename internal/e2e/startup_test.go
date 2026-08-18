//go:build e2e

package e2e

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestE2E_Startup_CorruptRegistryFailsClean(t *testing.T) {
	home, regPath := newRegistryHome(t)

	corrupt := []byte("{not valid json")
	if err := os.WriteFile(regPath, corrupt, 0o600); err != nil {
		t.Fatalf("seed corrupt registry: %v", err)
	}

	res := StartExpectingFailureIn(t, home)

	if res.ExitCode == 0 {
		t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
	}
	if !bytes.Contains(res.Stderr, []byte("registry")) {
		t.Errorf("stderr does not mention registry: %s", res.Stderr)
	}

	got, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry after failed start: %v", err)
	}
	if !bytes.Equal(got, corrupt) {
		t.Errorf("registry mutated by failed start:\nwant: %q\ngot:  %q", corrupt, got)
	}
}

// deadRelayURL is a relay URL that is fully WIRED but can never connect.
// startRelay's only synchronous failures are config errors (bad scheme,
// missing identity, missing device registry) — relay.Connect launches its
// dial in a goroutine and returns — so a wss:// URL nothing answers still
// gets the producer goroutines running and the deferred cleanup registered,
// which is what TestE2E_Startup_SecondInstanceExitsNonZero needs. Port 1 is
// unbindable without root, so nothing can ever answer. wss:// (not ws://)
// passes the scheme check without PYRY_ALLOW_INSECURE_RELAY. Pinning it
// explicitly keeps DefaultConfig's real relay.pyrycode.dev out of `make check`.
const deadRelayURL = "wss://127.0.0.1:1/v2/server"

// A second pyry start against a live daemon's socket must print the
// single-instance diagnostic and exit, not hang. With the relay leg wired,
// Server.Listen's ErrInstanceRunning return is the one error return that sits
// between startRelay and the end of runSupervisor, so it unwinds through the
// deferred relay cleanup — whose producer drains only return once the daemon
// ctx is cancelled (#1492).
//
// Two notes on the helper, because its arms read backwards here:
//
//   - StartExpectingFailureIn polls the socket path IT minted, not the
//     -pyry-socket= override appended after it (flag parsing is last-wins), so
//     its "unexpectedly became ready" arm cannot fire. The load-bearing arms
//     are the exit arm (returns RunResult) and the deadline arm, and the
//     deadline arm is exactly the pre-fix red: the second process wedges in
//     the deferred cleanup and neither exits nor becomes ready.
//   - Both processes share $HOME and -pyry-name=test, so they read the same
//     server-id and registry. That is what an operator's double-start actually
//     looks like. The second process returns before Pool.Run, so it never
//     spawns a child and never rewrites the live daemon's state — asserted by
//     the surviving-daemon check below.
func TestE2E_Startup_SecondInstanceExitsNonZero(t *testing.T) {
	home := shortHome(t)
	h := StartIn(t, home, "-pyry-relay="+deadRelayURL)

	res := StartExpectingFailureIn(t, home,
		"-pyry-socket="+h.SocketPath,
		"-pyry-relay="+deadRelayURL,
	)

	if res.ExitCode == 0 {
		t.Errorf("exit code = 0, want non-zero (stderr=%s)", res.Stderr)
	}
	if !bytes.Contains(res.Stderr, []byte("another pyry instance is already running")) {
		t.Errorf("stderr does not name the running instance: %s", res.Stderr)
	}

	if r := h.Run(t, "status"); r.ExitCode != 0 {
		t.Fatalf("live daemon did not survive the second start: status exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	h.Stop(t)
}

func TestE2E_Startup_MissingClaudeProjectsDir(t *testing.T) {
	// os.MkdirTemp keeps the socket path under macOS's 104-byte sun_path
	// limit; t.TempDir() embeds the (long) test name and overflows.
	home, err := os.MkdirTemp("", "pyry-mp-*")
	if err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	claudeProjects := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(claudeProjects); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".claude/projects/ unexpectedly exists at %s (err=%v); test premise invalidated",
			claudeProjects, err)
	}

	h := StartIn(t, home)

	r := h.Run(t, "status")
	if r.ExitCode != 0 {
		t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, r.Stdout, r.Stderr)
	}

	h.Stop(t)
}
