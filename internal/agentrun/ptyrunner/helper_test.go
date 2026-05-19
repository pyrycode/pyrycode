package ptyrunner

import (
	"fmt"
	"io"
	"os"
	"os/signal"
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
//
// All modes install a SIGTERM handler so Session.Close()'s
// SIGTERM→grace→SIGKILL sequence resolves on the SIGTERM step rather
// than waiting out the 3-second grace.
func runHelper() {

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
	default:
		fmt.Fprintf(os.Stderr, "unknown GO_PTYRUNNER_HELPER_MODE: %q\n", mode)
		os.Exit(99)
	}
	_ = os.Stdout.Sync()

	// Drain stdin so PTY writes from the parent (WritePrompt's
	// bracketed-paste sequence) don't backpressure into the parent's
	// PTY master write. Discarded — this slice does not verify
	// WritePrompt's exact wire shape (the tui-driver package already
	// pins it via internal tests).
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	select {
	case <-sigCh:
		os.Exit(0)
	case <-time.After(30 * time.Second):
		os.Exit(0)
	}
}
