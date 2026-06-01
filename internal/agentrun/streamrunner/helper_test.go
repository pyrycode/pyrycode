package streamrunner

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// blockUntilSigterm waits for SIGTERM (exits 0) or a 30s safety timeout. Used
// by the stall helper modes so a watchdog-driven kill terminates the fake
// claude promptly instead of falling through to the SIGKILL grace window.
func blockUntilSigterm() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	select {
	case <-sigCh:
		os.Exit(0)
	case <-time.After(30 * time.Second):
		os.Exit(0)
	}
}

// TestStreamRunnerHelperProcess is the fake-claude entry point for Run's
// tests. The test binary re-execs itself with GO_STREAMRUNNER_HELPER=1 and
// behaviour keyed by GO_STREAMRUNNER_HELPER_MODE:
//
//   - "clean":       read stdin to EOF, write a fixed three-line stream-json
//                    sequence to stdout, exit 0.
//   - "exit1":       read stdin briefly, exit 1. Used to verify Run
//                    surfaces *exec.ExitError on non-zero child exit.
//   - "sleep":       read stdin to EOF, install a SIGTERM handler that
//                    prints "got SIGTERM" to stderr and exits 0; otherwise
//                    sleep 30s. Used by the ctx-cancel test.
//   - "echo_stdin":  read stdin to EOF, write the bytes verbatim to
//                    GO_STREAMRUNNER_HELPER_STDIN_FILE (mode 0o600), exit 0.
//   - "exit0_no_read": exit 0 immediately without reading stdin. Forces the
//                    parent's stdin.Write to hit EPIPE (broken pipe) and the
//                    subsequent stdin.Close to surface a benign error.
//   - "stall_silent": read stdin to EOF, emit NOTHING, then block until
//                    SIGTERM (exit 0) or a 30s safety timeout. Models a run
//                    that wedges before claude's first turn — awaitingAssistant
//                    is true from the start, so the watchdog must fire. The
//                    SIGTERM handler keeps the watchdog-driven kill fast.
//   - "stall_after_user": read stdin to EOF, emit system → assistant(tool_use)
//                    → user(tool_result), then block until SIGTERM (exit 0) or
//                    30s. Models a stall right after a tool result came back —
//                    awaitingAssistant is true again, so the watchdog must fire.
//   - "slow_tool":  read stdin to EOF, emit system → assistant(tool_use), sleep
//                    GO_STREAMRUNNER_HELPER_SLEEP_MS (default 600ms), then emit
//                    user(tool_result) → assistant(text) → result and exit 0.
//                    Models a legitimately long in-flight tool run —
//                    awaitingAssistant is FALSE during the silence, so the
//                    watchdog must NOT fire. Deliberately installs no SIGTERM
//                    handler: a wrong watchdog kill shows up as a missing
//                    success `result` line.
func TestStreamRunnerHelperProcess(t *testing.T) {
	if os.Getenv("GO_STREAMRUNNER_HELPER") != "1" {
		return
	}
	switch os.Getenv("GO_STREAMRUNNER_HELPER_MODE") {
	case "clean":
		_, _ = io.Copy(io.Discard, os.Stdin)
		// Deterministic three-line stream-json sequence. The lines are
		// shape-only — Run does not parse them; the test only checks for
		// substrings.
		lines := []string{
			`{"type":"system","subtype":"init"}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`,
			`{"type":"result","subtype":"success"}`,
		}
		for _, l := range lines {
			fmt.Fprintln(os.Stdout, l)
		}
		os.Exit(0)
	case "exit1":
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		time.Sleep(50 * time.Millisecond)
		os.Exit(1)
	case "sleep":
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM)
		go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "got SIGTERM")
			os.Exit(0)
		case <-time.After(30 * time.Second):
			os.Exit(0)
		}
	case "exit0_no_read":
		// Exit immediately; do not drain stdin. The parent's pipe write
		// hits EPIPE once the kernel observes the read end closed.
		os.Exit(0)
	case "stall_silent":
		_, _ = io.Copy(io.Discard, os.Stdin)
		blockUntilSigterm()
	case "stall_after_user":
		_, _ = io.Copy(io.Discard, os.Stdin)
		for _, l := range []string{
			`{"type":"system","subtype":"init"}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{}}]}}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		} {
			fmt.Fprintln(os.Stdout, l)
		}
		blockUntilSigterm()
	case "slow_tool":
		_, _ = io.Copy(io.Discard, os.Stdin)
		fmt.Fprintln(os.Stdout, `{"type":"system","subtype":"init"}`)
		fmt.Fprintln(os.Stdout, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{}}]}}`)
		sleepMS := 600
		if v := os.Getenv("GO_STREAMRUNNER_HELPER_SLEEP_MS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				sleepMS = n
			}
		}
		time.Sleep(time.Duration(sleepMS) * time.Millisecond)
		for _, l := range []string{
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"done"}]}}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"finished"}]}}`,
			`{"type":"result","subtype":"success"}`,
		} {
			fmt.Fprintln(os.Stdout, l)
		}
		os.Exit(0)
	case "echo_stdin":
		path := os.Getenv("GO_STREAMRUNNER_HELPER_STDIN_FILE")
		if path == "" {
			fmt.Fprintln(os.Stderr, "echo_stdin: GO_STREAMRUNNER_HELPER_STDIN_FILE unset")
			os.Exit(99)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "echo_stdin: open: %v\n", err)
			os.Exit(99)
		}
		if _, err := io.Copy(f, os.Stdin); err != nil {
			fmt.Fprintf(os.Stderr, "echo_stdin: copy: %v\n", err)
			_ = f.Close()
			os.Exit(99)
		}
		_ = f.Sync()
		_ = f.Close()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_STREAMRUNNER_HELPER_MODE: %q\n", os.Getenv("GO_STREAMRUNNER_HELPER_MODE"))
		os.Exit(99)
	}
}
