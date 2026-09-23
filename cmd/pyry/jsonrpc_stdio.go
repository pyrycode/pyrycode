package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/pyrycode/pyrycode/internal/acp"
)

// serveJSONRPCStdio wires the line-delimited JSON-RPC transport over
// stdin/stdout with logger and blocks until stdin reaches EOF (returns nil) or
// ctx is cancelled by a signal (returns nil — a deliberate shutdown is a clean
// exit). A genuine stream break (over-long line / read error) while not shutting
// down returns a wrapped error. stdin is an io.Reader (not *os.File) so tests
// inject an in-memory reader without a TTY or real fds.
//
// register, when non-nil, is invoked on the fresh transport after acp.New and
// before Serve — the transport's "register before Serve" invariant — so callers
// wire their method handlers. A nil register wires nothing.
//
// It was called serveACP and lived beside the ACP subcommand until #1348
// deleted that surface. The name was never accurate to what it does: it is a
// generic JSON-RPC-over-stdio server, and its callers are the MCP servers
// claude spawns: runMCPApprove, the permission gate on the stream path, and
// runMCPFiles. Renamed on the way out, because a function named for a deleted feature
// is how the next reader concludes the feature is still here — the same trap
// that made two files named "stream" turn out to be the terminal path.
//
// The transport package keeps its name for now. Renaming it is a wider change
// than this one and wants its own pass.
//
// AC#4 — unblocking a blocked read: the transport's Serve doc notes a Read
// already parked on a quiet reader cannot be woken by ctx alone (the ctx check
// only happens between frames). Closing os.Stdin does not fix this: a Read
// parked in the kernel on the non-pollable os.Stdin fd cannot be interrupted by
// Close (docs/lessons.md #78, "Closing a fd to interrupt a goroutine's Read
// requires O_NONBLOCK") — Close itself would then block on the fd's
// mutex. Instead we bridge real stdin through an in-memory io.Pipe: Serve reads
// the PipeReader, and pr.CloseWithError synchronously unblocks any blocked
// PipeReader.Read (io.Pipe is a channel internally), so shutdown is prompt with
// zero fd/poller machinery.
func serveJSONRPCStdio(ctx context.Context, stdin io.Reader, stdout io.Writer, logger *slog.Logger, register func(*acp.Transport)) error {
	pr, pw := io.Pipe()

	// closer (AC#4): on shutdown, unblock a Read blocked inside Serve's scanner
	// by closing the in-memory pipe. On the EOF path this goroutine stays parked
	// on ctx.Done until the caller's defer cancel() fires (runMCPApprove,
	// runMCPFiles), then no-ops on the already-closed pipe — so it never
	// outlives the call.
	go func() {
		<-ctx.Done()
		_ = pr.CloseWithError(ctx.Err())
	}()

	// bridge: feed the pipe from real stdin; the host closing stdin surfaces as
	// EOF, which closes the pipe and lets Serve return (AC#3). On the signal
	// path this goroutine stays blocked on stdin.Read and is reaped by process
	// exit — acceptable only because `pyry mcp-approve` and `pyry mcp-files` are
	// one-shot subprocesses that exit immediately after serveJSONRPCStdio
	// returns (exactly one goroutine dies with the process). serveJSONRPCStdio
	// must NOT join it: joining would force the EOF path to wait for a signal,
	// and the bridge is unjoinable on the signal path.
	go func() {
		_, _ = io.Copy(pw, stdin)
		_ = pw.Close()
	}()

	t := acp.New(pr, stdout, logger)
	if register != nil {
		register(t)
	}
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
