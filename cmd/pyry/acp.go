package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/acp"
)

// runACP implements the `pyry acp` subcommand: it serves the internal/acp
// JSON-RPC transport over the process's real stdio, blocking until stdin
// reaches EOF (the host closes the pipe) or a signal cancels the context. It
// takes no flags and no positional arguments, registers no method handlers, and
// drives no claude — it is the composition root the later session/* handler
// tickets register against.
func runACP(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("acp: unexpected arguments: %s", strings.Join(args, " "))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Plain stderr logger: the ACP host captures our stderr and keeps its own
	// diagnostics ring, so — unlike runSupervisor — this subprocess needs no
	// control.SlogTee/ring buffer. AC#5 requires only that diagnostics stay off
	// stdout, which the frame stream owns.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("acp: serving JSON-RPC transport over stdio")

	return serveACP(ctx, os.Stdin, os.Stdout, logger)
}

// serveACP wires the acp transport over stdin/stdout with logger and blocks
// until stdin reaches EOF (returns nil) or ctx is cancelled by a signal
// (returns nil — a deliberate shutdown is a clean exit). A genuine stream break
// (over-long line / read error) while not shutting down returns a wrapped
// error. stdin is an io.Reader (not *os.File) so tests inject an in-memory
// reader without a TTY or real fds.
//
// AC#4 — unblocking a blocked read: the transport's Serve doc notes a Read
// already parked on a quiet reader cannot be woken by ctx alone (the ctx check
// only happens between frames). Closing os.Stdin does not fix this: a Read
// parked in the kernel on the non-pollable os.Stdin fd cannot be interrupted by
// Close (docs/lessons.md #78, :243) — Close itself would then block on the fd's
// mutex. Instead we bridge real stdin through an in-memory io.Pipe: Serve reads
// the PipeReader, and pr.CloseWithError synchronously unblocks any blocked
// PipeReader.Read (io.Pipe is a channel internally), so shutdown is prompt with
// zero fd/poller machinery.
func serveACP(ctx context.Context, stdin io.Reader, stdout io.Writer, logger *slog.Logger) error {
	pr, pw := io.Pipe()

	// closer (AC#4): on shutdown, unblock a Read blocked inside Serve's scanner
	// by closing the in-memory pipe. On the EOF path this goroutine stays parked
	// on ctx.Done until runACP's defer cancel() fires, then no-ops on the
	// already-closed pipe — so it never outlives the call.
	go func() {
		<-ctx.Done()
		_ = pr.CloseWithError(ctx.Err())
	}()

	// bridge: feed the pipe from real stdin; the host closing stdin surfaces as
	// EOF, which closes the pipe and lets Serve return (AC#3). On the signal
	// path this goroutine stays blocked on stdin.Read and is reaped by process
	// exit — acceptable only because `pyry acp` is a one-shot subprocess that
	// exits immediately after serveACP returns (exactly one goroutine dies with
	// the process). serveACP must NOT join it: joining would force the EOF path
	// to wait for a signal, and the bridge is unjoinable on the signal path.
	go func() {
		_, _ = io.Copy(pw, stdin)
		_ = pw.Close()
	}()

	t := acp.New(pr, stdout, logger) // registers NO handlers (AC#2)
	err := t.Serve(ctx)
	if ctx.Err() != nil {
		// A deliberate SIGINT/SIGTERM shutdown. Serve surfaces either
		// context.Canceled (cancelled between frames) or a wrapped os.ErrClosed
		// (the forced pipe close unblocked a read) — guarding on ctx.Err()
		// rather than errors.Is(err, context.Canceled) absorbs both as a clean
		// exit, while still declining to swallow a genuine stream break (which
		// only occurs with ctx.Err() == nil).
		return nil
	}
	if err != nil {
		return fmt.Errorf("acp: %w", err)
	}
	return nil
}
