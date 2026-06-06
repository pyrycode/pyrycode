package ptyrunner

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestMain dispatches to the fake-claude helper before Go's test-binary
// flag parser runs. This is required because ptyrunner builds its own
// argv (--session-id, --settings, …) — those flags are unknown to `go
// test`, which would os.Exit(2) at flag parsing if we reached m.Run.
// The dispatcher is keyed by GO_PTYRUNNER_HELPER=1; when unset, the
// process behaves like any other test binary.
func TestMain(m *testing.M) {
	if os.Getenv("GO_PTYRUNNER_HELPER") == "1" {
		runHelper()
		// runHelper terminates via os.Exit.
		return
	}
	// #576: drop any ambient PYRY_RECORD_DIR so tests that don't explicitly
	// opt into recording never inherit it. The recording gate (runner.go) reads
	// os.Getenv in THIS process; clearing it once here makes every non-opt-in
	// TestRun_* deterministic regardless of the caller's shell. Recording tests
	// re-set it per-test via t.Setenv (which restores to unset on cleanup).
	_ = os.Unsetenv("PYRY_RECORD_DIR")
	os.Exit(m.Run())
}

// runHelper is the fake-claude entry point for Run's tests. The test
// binary re-execs itself with GO_PTYRUNNER_HELPER=1 and behaviour keyed
// by GO_PTYRUNNER_HELPER_MODE:
//
//   - "idle":            write ❯ + space to stdout so tuidriver.IsIdle
//                        succeeds, then idle for SIGTERM.
//   - "trust":           write the trust-modal anchor + ❯ so HasTrustModal
//                        AND IsIdle both fire, then idle for SIGTERM.
//   - "mcp_failure":     write the MCP-failure banner + ❯ so
//                        HasMcpFailureBanner AND IsIdle both fire.
//   - "network_failure": write FailedToOpenSocket + ❯ so HasNetworkFailure
//                        AND IsIdle both fire.
//   - "slow_spawn":      sleep 5s before writing anything (parent's
//                        ctx-cancel fires inside WaitUntil first).
//   - "jsonl":           write ❯ + space (IsIdle), then once the parent's
//                        WritePrompt arrives on stdin, write
//                        GO_PTYRUNNER_JSONL_BODY to GO_PTYRUNNER_JSONL_PATH
//                        (atomic-append, 0600). The body's lines drive the
//                        parent's tail.Watcher + streamjson.Emitter.
//   - "jsonl_exit143":   same wiring as "jsonl", but the SIGTERM handler
//                        exits with code 143 (128 + SIGTERM(15)) so the
//                        parent's Session.Close surfaces an *exec.ExitError
//                        with that exit code — the steady-state shape on
//                        every `max_turns` exhaustion in production.
//   - "mid_trust":       write ❯ + space (IsIdle, no modal anchor at start),
//                        then once stdin's first byte arrives (WritePrompt
//                        landed), write the trust-folder modal anchor +
//                        ❯ to stdout. The merge loop's 50 ms poll detects
//                        the rising edge and emits EventKindPtyModalShown
//                        with Modal=ModalClassTrustFolder.
//   - "mid_mcp_failure": same shape as mid_trust but the post-stdinSeen
//                        write is the "1 MCP server failed" banner. The
//                        merge loop emits EventKindPtyMcpFailureShown.
//   - "mid_network_failure": same shape, post-stdinSeen write is the
//                        FailedToOpenSocket anchor. The merge loop emits
//                        EventKindPtyNetworkFailureShown.
//
// All modes install a SIGTERM handler so Session.Close()'s
// SIGTERM→grace→SIGKILL sequence resolves on the SIGTERM step rather
// than waiting out the 3-second grace.
func runHelper() {
	// reap-tree fixture modes (TestReapDescendantGroups) build a process tree
	// instead of standing in for claude. Keyed by GO_PTYRUNNER_REAP_MODE so the
	// claude-stand-in switch below stays untouched.
	if role := os.Getenv("GO_PTYRUNNER_REAP_MODE"); role != "" {
		runReapHelper(role)
		// runReapHelper terminates via os.Exit.
		return
	}

	// UTF-8 encodings of claude's TUI glyphs the parent's detectors look
	// for (after StripANSI).
	const idleGlyph = "\xe2\x9d\xaf" // ❯

	mode := os.Getenv("GO_PTYRUNNER_HELPER_MODE")
	switch mode {
	case "idle":
		fmt.Fprint(os.Stdout, idleGlyph+" ")
	case "trust":
		// "Quicksafetycheck" is the space-stripped header anchor used by
		// tuidriver.HasTrustModal. The ❯ glyph satisfies IsIdle so the
		// post-idle modal check fires.
		fmt.Fprint(os.Stdout, "Quicksafetycheck"+idleGlyph+" ")
	case "mcp_failure":
		fmt.Fprint(os.Stdout, "1 MCP server failed "+idleGlyph+" ")
	case "network_failure":
		fmt.Fprint(os.Stdout, "FailedToOpenSocket "+idleGlyph+" ")
	case "slow_spawn":
		time.Sleep(5 * time.Second)
		fmt.Fprint(os.Stdout, idleGlyph+" ")
	case "jsonl", "jsonl_exit143":
		fmt.Fprint(os.Stdout, idleGlyph+" ")
	case "commit_wedge_chip":
		// "Pasted text" chip + ❯ at idle — the chip carries no ✻ so IsIdle
		// still fires, and hasPastedChip(snapshot) is true. With the JSONL
		// held back by commitModeJSONLDelay, the parent's first commit window
		// elapses with no JSONL and the chip-gated branch re-delivers.
		fmt.Fprint(os.Stdout, "[Pasted text +3 lines] "+idleGlyph+" ")
	case "commit_slow_nochip":
		// ❯ only (no chip) — same render as jsonl; the only difference is the
		// delayed JSONL write. The input box is empty, so the chip-gated
		// branch treats the slow turn as committed-but-slow and does NOT
		// re-deliver (#227 protection).
		fmt.Fprint(os.Stdout, idleGlyph+" ")
	case "mid_trust", "mid_mcp_failure", "mid_network_failure":
		fmt.Fprint(os.Stdout, idleGlyph+" ")
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_PTYRUNNER_HELPER_MODE: %q\n", mode)
		os.Exit(99)
	}
	_ = os.Stdout.Sync()

	// Drain stdin so PTY writes from the parent (WritePrompt's
	// bracketed-paste sequence) don't backpressure into the parent's
	// PTY master write. For jsonl mode, signal stdinSeen on the first
	// byte so the main goroutine knows WritePrompt has landed and the
	// JSONL body can be flushed to disk.
	stdinSeen := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 4096)
		first := true
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 && first {
				first = false
				select {
				case stdinSeen <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()

	if mode == "jsonl" || mode == "jsonl_exit143" || mode == "commit_wedge_chip" || mode == "commit_slow_nochip" {
		path := os.Getenv("GO_PTYRUNNER_JSONL_PATH")
		body := os.Getenv("GO_PTYRUNNER_JSONL_BODY")
		if path == "" {
			fmt.Fprintln(os.Stderr, "jsonl mode requires GO_PTYRUNNER_JSONL_PATH")
			os.Exit(98)
		}
		// The commit-mode fixtures hold the body back so the parent's first
		// prompt-commit window elapses with no JSONL → the chip-gated branch
		// fires; the delayed body then lets WaitForSessionJSONL complete the
		// run cleanly. jsonl / jsonl_exit143 write with no delay.
		var delay time.Duration
		if mode == "commit_wedge_chip" || mode == "commit_slow_nochip" {
			delay = commitModeJSONLDelay
		}
		go func() {
			select {
			case <-stdinSeen:
			case <-time.After(20 * time.Second):
				fmt.Fprintln(os.Stderr, "jsonl mode: stdin first-byte timeout")
				return
			}
			if delay > 0 {
				time.Sleep(delay)
			}
			if body == "" {
				return
			}
			writeSessionJSONLBody(path, body)
		}()
	}

	if mode == "mid_trust" || mode == "mid_mcp_failure" || mode == "mid_network_failure" {
		path := os.Getenv("GO_PTYRUNNER_JSONL_PATH")
		if path == "" {
			fmt.Fprintln(os.Stderr, mode+": requires GO_PTYRUNNER_JSONL_PATH")
			os.Exit(98)
		}
		var anchor string
		switch mode {
		case "mid_trust":
			anchor = "Quicksafetycheck" + idleGlyph + " "
		case "mid_mcp_failure":
			anchor = "1 MCP server failed " + idleGlyph + " "
		case "mid_network_failure":
			anchor = "FailedToOpenSocket " + idleGlyph + " "
		}
		go func() {
			select {
			case <-stdinSeen:
			case <-time.After(20 * time.Second):
				fmt.Fprintln(os.Stderr, mode+": stdin first-byte timeout")
				return
			}
			// Create the per-session JSONL file (empty) so the
			// parent's WaitForSessionJSONL resolves and Session.Events
			// opens the unified stream. The mid-run anchor write
			// below then drives the Events merge loop's PTY axis.
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				fmt.Fprintf(os.Stderr, mode+": mkdir %s: %v\n", filepath.Dir(path), err)
				return
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				fmt.Fprintf(os.Stderr, mode+": open %s: %v\n", path, err)
				return
			}
			_ = f.Close()
			fmt.Fprint(os.Stdout, anchor)
			_ = os.Stdout.Sync()
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	exitOnSigterm := 0
	if mode == "jsonl_exit143" {
		exitOnSigterm = 143
	}
	select {
	case <-sigCh:
		os.Exit(exitOnSigterm)
	case <-time.After(30 * time.Second):
		os.Exit(0)
	}
}

// commitModeJSONLDelay is how long the commit-mode fixtures
// (commit_wedge_chip / commit_slow_nochip) hold the session JSONL body back
// after the prompt lands. It must exceed the test's PromptCommitTimeout
// (200ms) so the parent's first commit window elapses with no JSONL and the
// chip-gated branch is exercised; 500ms leaves a 300ms margin. Correctness
// does not depend on the wedge committing inside the retry budget — control
// always falls through to WaitForSessionJSONL, which picks up the delayed
// body — so the margin is what keeps the suite -race -count stable.
const commitModeJSONLDelay = 500 * time.Millisecond

// runReapHelper is the process-tree fixture for TestReapDescendantGroups,
// dispatched by GO_PTYRUNNER_REAP_MODE. It never stands in for claude; it just
// shapes a tree the reaper walks:
//
//   - "leaf":         block (no children). Used as a fresh-group descendant,
//                     a same-group sibling, or a no-descendant root.
//   - "parent_fresh": spawn one "leaf" grandchild in a FRESH process group
//                     (Setpgid), report its pid via GO_PTYRUNNER_REAP_REPORT,
//                     then block. Mirrors claude → zsh+tail (the reaped group).
//   - "parent_same":  spawn one "leaf" grandchild in the SAME group (no
//                     Setpgid), report its pid, then block. Exercises the
//                     "rootPid's own group is excluded" guard.
//
// The reaper kills with SIGKILL (uncatchable), so no mode needs a signal
// handler; the 30s block is a backstop so a leaked helper self-terminates.
func runReapHelper(role string) {
	switch role {
	case "leaf":
		blockUntilKilled()
	case "parent_fresh":
		spawnGrandchildAndBlock(true)
	case "parent_same":
		spawnGrandchildAndBlock(false)
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_PTYRUNNER_REAP_MODE: %q\n", role)
		os.Exit(97)
	}
}

// blockUntilKilled blocks for a generous backstop window, then exits. The
// reaper (and the test's cleanup) kill via SIGKILL, which needs no handler;
// the timer only bounds a helper the test forgot to kill.
func blockUntilKilled() {
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// spawnGrandchildAndBlock re-execs this binary as a "leaf" grandchild — in a
// fresh process group when freshGroup is set — writes the grandchild's pid to
// GO_PTYRUNNER_REAP_REPORT so the parent test can target its assertions, then
// blocks. It reaps the grandchild in the background so a SIGKILL'd group truly
// empties: a zombie still answers kill(-pgid, 0), which would otherwise defeat
// the test's group-gone probe.
func spawnGrandchildAndBlock(freshGroup bool) {
	reportPath := os.Getenv("GO_PTYRUNNER_REAP_REPORT")
	if reportPath == "" {
		fmt.Fprintln(os.Stderr, "parent reap helper requires GO_PTYRUNNER_REAP_REPORT")
		os.Exit(96)
	}
	gc := exec.Command(os.Args[0])
	gc.Env = append(os.Environ(), "GO_PTYRUNNER_HELPER=1", "GO_PTYRUNNER_REAP_MODE=leaf")
	if freshGroup {
		gc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := gc.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "parent reap helper: start grandchild: %v\n", err)
		os.Exit(95)
	}
	go func() { _ = gc.Wait() }()
	if err := os.WriteFile(reportPath, []byte(strconv.Itoa(gc.Process.Pid)), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "parent reap helper: write report: %v\n", err)
		os.Exit(94)
	}
	blockUntilKilled()
}

// writeSessionJSONLBody appends body to the per-session JSONL at path,
// creating the encoded project dir first. Shared by every fake-claude mode
// that surfaces a session turn (jsonl, jsonl_exit143, and the commit-mode
// fixtures). MkdirAll guards against a missing encoded project dir in test
// mode — the helper owns parent-dir creation because no other actor creates
// it before the JSONL write. The parent's tuidriver.WaitForSessionJSONL polls
// the path via os.Stat and resolves as soon as the file appears, regardless
// of whether the parent dir pre-existed.
func writeSessionJSONLBody(path, body string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "jsonl mode: mkdir %s: %v\n", filepath.Dir(path), err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jsonl mode: open %s: %v\n", path, err)
		return
	}
	if _, err := io.WriteString(f, body); err != nil {
		fmt.Fprintf(os.Stderr, "jsonl mode: write %s: %v\n", path, err)
	}
	_ = f.Sync()
	_ = f.Close()
}
