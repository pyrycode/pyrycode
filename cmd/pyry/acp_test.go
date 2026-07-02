package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// discardLogger builds a logger writing to w so tests can assert diagnostics
// land there (never on the stdout frame stream).
func testLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestServeACP_EOFReturnsNil pins AC#3: when stdin is already at EOF the serve
// loop returns nil and writes nothing to the frame stream.
func TestServeACP_EOFReturnsNil(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	err := serveACP(ctx, strings.NewReader(""), &stdout, testLogger(&stderr))
	if err != nil {
		t.Fatalf("serveACP at EOF: want nil, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("no handlers registered, want empty stdout, got %q", stdout.String())
	}
}

// TestServeACP_BlankLinesThenEOF pins AC#3: whitespace-only lines are skipped
// by the transport and EOF still returns nil with nothing on the frame stream.
func TestServeACP_BlankLinesThenEOF(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	err := serveACP(ctx, strings.NewReader("\n  \n\t\n"), &stdout, testLogger(&stderr))
	if err != nil {
		t.Fatalf("serveACP with blank lines: want nil, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("blank lines produce no frames, want empty stdout, got %q", stdout.String())
	}
}

// TestServeACP_BlockedReadUnblocksOnCancel is the load-bearing test (AC#4): a
// stdin read blocked on a quiet host must be unblocked when the context is
// cancelled, so the serve loop returns promptly instead of hanging until the
// next line arrives. Regression guard for the #78 blocked-read leak.
func TestServeACP_BlockedReadUnblocksOnCancel(t *testing.T) {
	t.Parallel()

	// The read end of an io.Pipe with nothing written blocks forever — a quiet
	// host. Closing the write end in cleanup drains the bridge goroutine so the
	// test leaks nothing.
	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- serveACP(ctx, stdinR, &stdout, testLogger(&stderr)) }()

	// The read genuinely blocks: without cancel/EOF, serveACP cannot return.
	select {
	case err := <-done:
		t.Fatalf("serveACP returned before cancel with %v; the read did not block", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("signal-driven shutdown: want nil (clean exit), got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveACP did not return within 2s of cancel — blocked read was not unblocked (#78 regression)")
	}
	if stdout.Len() != 0 {
		t.Fatalf("shutdown must not emit frames, got %q", stdout.String())
	}
}

// TestServeACP_StreamBreakReturnsError pins AC#5: a genuine stream break while
// not shutting down maps to a wrapped error (exit non-zero), with the detail on
// the diagnostics stream, never the frame stream.
func TestServeACP_StreamBreakReturnsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A single line longer than the transport's maxLineBytes (16 MiB) with no
	// newline is structurally broken: the scanner surfaces bufio.ErrTooLong.
	overlong := strings.Repeat("a", 16<<20+1)

	var stdout, stderr bytes.Buffer
	err := serveACP(ctx, strings.NewReader(overlong), &stdout, testLogger(&stderr))
	if err == nil {
		t.Fatal("over-long line: want a wrapped error, got nil")
	}
	if !strings.Contains(err.Error(), "acp:") {
		t.Fatalf("want error wrapped with 'acp:', got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stream break must not emit frames, got %q", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("stream break detail should be logged to the diagnostics stream")
	}
}

// TestRunACP_RejectsArgs pins AC#1: the subcommand takes no arguments and the
// guard fires before any stdin side effect (so this never blocks).
func TestRunACP_RejectsArgs(t *testing.T) {
	t.Parallel()
	err := runACP([]string{"unexpected"})
	if err == nil {
		t.Fatal("runACP with an argument: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("want the error to name the unexpected argument, got %v", err)
	}
}
