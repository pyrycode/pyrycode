package streamrunner

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

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
