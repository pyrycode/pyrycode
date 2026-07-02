package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pyrycode/pyrycode/internal/acp"
	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// runACP implements the `pyry acp` subcommand: it stands up an embedded
// internal/sessions pool (trimmed to the ACP subset — no relay, control socket,
// conversations registry, or message queue) and serves the internal/acp
// JSON-RPC transport over the process's real stdio, blocking until stdin
// reaches EOF (the host closes the pipe) or a signal cancels the context. It
// takes no flags and no positional arguments. session/new is the one method it
// serves: it allocates a pool session, which spawns exactly one supervised
// interactive claude via the tui-driver (#761, epic #600).
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

	// Standup: a trimmed runSupervisor spine. Reuse the daemon's own workdir
	// (process cwd, confined to $HOME) and the shared cmd-layer trust-mark so
	// the supervised claude never wedges on the workspace-trust modal; no
	// caller-supplied cwd reaches the spawn (see the handler note below). The
	// bootstrap is evicted (no eager claude) and every session runs in service
	// mode (Bridge set) so claude's PTY output routes to the discarding bridge
	// instead of clobbering the JSON-RPC frame stream on stdout.
	workdirReal, err := confineWorkdirToHome("")
	if err != nil {
		return err
	}
	trustedWorkdir, err := trustMark(workdirReal)
	if err != nil {
		return fmt.Errorf("mark workdir trusted in ~/.claude.json: %w", err)
	}
	pool, err := sessions.New(sessions.Config{
		Logger:           logger,
		BootstrapEvicted: true,
		Bootstrap: sessions.SessionConfig{
			ClaudeBin: "claude",
			WorkDir:   trustedWorkdir,
			Bridge:    supervisor.NewBridge(logger),
		},
	})
	if err != nil {
		return fmt.Errorf("acp: pool init: %w", err)
	}

	return serveACPWithPool(ctx, pool, os.Stdin, os.Stdout, logger)
}

// serveACPWithPool backgrounds pool.Run, waits for the pool to become ready (so
// the first session/new cannot race supervise into ErrPoolNotRunning), serves
// the transport with session/new registered, and tears the pool down on return.
// stdin/stdout are parameters (not os.Stdin/os.Stdout) so tests drive it with
// in-memory pipes against a fake-claude pool.
func serveACPWithPool(ctx context.Context, pool *sessions.Pool, stdin io.Reader, stdout io.Writer, logger *slog.Logger) error {
	// A child ctx so the EOF path (serveACP returns nil) can stop pool.Run; the
	// signal path cancels the parent, which cancels this too.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	poolErr := make(chan error, 1)
	go func() { poolErr <- pool.Run(runCtx) }()

	select {
	case <-pool.Ready():
		// runGroup is wired; accepting frames is now safe.
	case err := <-poolErr:
		// Run failed before readiness (e.g. bootstrap supervisor.New). Nothing
		// is serving yet; surface a genuine error, treat cancellation as clean.
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("acp: pool run: %w", err)
		}
		return nil
	case <-ctx.Done():
		// Signal during startup: stop the pool and join before returning clean.
		cancel()
		<-poolErr
		return nil
	}

	register := func(t *acp.Transport) {
		t.Register("session/new", newSessionHandler(pool))
	}
	serveErr := serveACP(runCtx, stdin, stdout, logger, register)

	// Stop pool.Run (the EOF path needs this; the signal path already
	// cancelled) and join so no supervisor goroutine outlives the call.
	cancel()
	<-poolErr
	return serveErr
}

// newSessionResult is the session/new success payload. Unexported type with an
// exported, json-tagged field marshals fine and adds zero exported types.
type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

// newSessionHandler returns the acp.Handler for session/new. It allocates one
// pool session — Pool.Create mints a fresh id and spawns exactly one supervised
// interactive claude via the tui-driver (claude --session-id <uuid>, no -p / no
// Agent SDK) — and returns that id for the host to address in later calls.
//
// ACP session/new params (cwd, mcpServers) are deliberately ignored: the spawn
// uses the daemon's own workdir, so no caller-supplied path reaches it.
// Honouring a caller cwd is a later, security-sensitive ticket (#762+). A
// Create error is wrapped and returned → CodeInternalError on the wire, detail
// logged by the transport, never leaked.
func newSessionHandler(pool *sessions.Pool) acp.Handler {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		id, err := pool.Create(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("session/new: %w", err)
		}
		return newSessionResult{SessionID: string(id)}, nil
	}
}

// serveACP wires the acp transport over stdin/stdout with logger and blocks
// until stdin reaches EOF (returns nil) or ctx is cancelled by a signal
// (returns nil — a deliberate shutdown is a clean exit). A genuine stream break
// (over-long line / read error) while not shutting down returns a wrapped
// error. stdin is an io.Reader (not *os.File) so tests inject an in-memory
// reader without a TTY or real fds.
//
// register, when non-nil, is invoked on the fresh transport after acp.New and
// before Serve — the acp "register before Serve" invariant — so callers wire
// their method handlers (session/new). A nil register wires nothing.
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
func serveACP(ctx context.Context, stdin io.Reader, stdout io.Writer, logger *slog.Logger, register func(*acp.Transport)) error {
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
