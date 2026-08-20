//go:build darwin

package rotation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeLsofCmd returns a newLsofCmd replacement that re-execs this test binary as
// TestHelperProcess in the given mode instead of spawning a real lsof. The ctx
// is threaded through exec.CommandContext so a context timeout actually kills
// the fake — that is what lets the timeout test assert the bound fired.
func fakeLsofCmd(mode string) func(ctx context.Context, pid int) *exec.Cmd {
	return func(ctx context.Context, pid int) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = append(os.Environ(), "GO_TEST_HELPER_PROCESS=1", "GO_HELPER_MODE="+mode)
		return cmd
	}
}

// TestHelperProcess is the re-exec fake lsof (CODING-STYLE.md pattern). It only
// runs when the env marker is set, so `go test` skips it as an ordinary test.
// Each branch ends in os.Exit so the testing framework never appends its own
// PASS/ok output to the fake's stdout.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("GO_HELPER_MODE") {
	case "sleep":
		// Outlive any test timeout so only a ctx-triggered SIGKILL ends us.
		time.Sleep(10 * time.Second)
	case "exit1":
		os.Exit(1) // lsof's "no matching files" / "process gone" code.
	case "success":
		raw, err := os.ReadFile(filepath.Join("testdata", "lsof_basic.txt"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Stdout.Write(raw)
	}
	os.Exit(0)
}

// TestOpenJSONL_TimeoutFiresWithinBound covers AC 1: a hung lsof surfaces a
// non-nil timeout error well within the bound instead of blocking forever.
// Non-parallel because it swaps package-level seams.
func TestOpenJSONL_TimeoutFiresWithinBound(t *testing.T) {
	origCmd, origTimeout := newLsofCmd, lsofProbeTimeout
	t.Cleanup(func() { newLsofCmd, lsofProbeTimeout = origCmd, origTimeout })
	lsofProbeTimeout = 100 * time.Millisecond
	newLsofCmd = fakeLsofCmd("sleep") // the fake would otherwise run 10s

	start := time.Now()
	path, err := darwinProbe{}.OpenJSONL(4711)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("OpenJSONL returned (%q, nil); want a timeout error", path)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v; want it to wrap context.DeadlineExceeded", err)
	}
	if path != "" {
		t.Errorf("path = %q; want \"\"", path)
	}
	// Generous ceiling: proves the bound fired and we did not wait out the
	// fake's 10s sleep.
	if elapsed >= 2*time.Second {
		t.Errorf("OpenJSONL took %s; want well under 2s (bound was %s)", elapsed, lsofProbeTimeout)
	}
}

// TestOpenJSONL_Exit1IsBenign covers AC 2: lsof exiting 1 still maps to
// ("", nil), not an error. First unit coverage of the exit-1 branch, which was
// previously reachable only via a real lsof.
func TestOpenJSONL_Exit1IsBenign(t *testing.T) {
	orig := newLsofCmd
	t.Cleanup(func() { newLsofCmd = orig })
	newLsofCmd = fakeLsofCmd("exit1")

	path, err := darwinProbe{}.OpenJSONL(4711)
	if err != nil {
		t.Fatalf("OpenJSONL returned error %v; want nil (exit 1 is benign)", err)
	}
	if path != "" {
		t.Errorf("path = %q; want \"\"", path)
	}
}

// TestOpenJSONL_SuccessPassthrough confirms the command-factory seam did not
// disturb the happy path: a real subprocess emitting the lsof fixture still
// yields the first .jsonl name.
func TestOpenJSONL_SuccessPassthrough(t *testing.T) {
	orig := newLsofCmd
	t.Cleanup(func() { newLsofCmd = orig })
	newLsofCmd = fakeLsofCmd("success")

	path, err := darwinProbe{}.OpenJSONL(4711)
	if err != nil {
		t.Fatalf("OpenJSONL returned error %v; want nil", err)
	}
	want := "/Users/jane/.claude/projects/-Users-jane-Workspace/8a4cf9b2-7e5d-4d3a-9fb2-12c4f8a1de91.jsonl"
	if path != want {
		t.Errorf("path = %q; want %q", path, want)
	}
}
