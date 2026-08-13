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
	// The directory claude writes <session-id>.jsonl into for the ACP pool. Every
	// ACP session spawns in trustedWorkdir (buildSession sets WorkDir there and no
	// caller cwd reaches the spawn until #762+), so one fixed dir serves every
	// stream — coherent-by-construction with the cwd claude launches in. Passed
	// only to serveACPWithPool for the outbound turn-event streams; NOT set on the
	// pool's Config.ClaudeSessionsDir, which would enable the rotation watcher and
	// RotateID a session on /clear, breaking the host-held id addressing that
	// session/prompt and session/cancel rely on.
	claudeSessionsDir := sessions.DefaultClaudeSessionsDir(trustedWorkdir)
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

	return serveACPWithPool(ctx, pool, os.Stdin, os.Stdout, claudeSessionsDir, logger)
}

// serveACPWithPool backgrounds pool.Run, waits for the pool to become ready (so
// the first session/new cannot race supervise into ErrPoolNotRunning), serves
// the transport with session/new registered, and tears the pool down on return.
// stdin/stdout are parameters (not os.Stdin/os.Stdout) so tests drive it with
// in-memory pipes against a fake-claude pool.
func serveACPWithPool(ctx context.Context, pool *sessions.Pool, stdin io.Reader, stdout io.Writer, claudeSessionsDir string, logger *slog.Logger) error {
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

	// holds is the per-session in-flight registry for held session/prompt calls,
	// one per `pyry acp` process. Its end resolver is wired into the turn-event
	// streams below (#751): on TurnEnd the outbound stream resolves the held call
	// with the mapped stopReason. The same holds instance is shared with the
	// session/prompt handler registered further down.
	holds := newPromptHolds(logger)

	// streams owns one turnbridge.Producer per addressable session, driving the
	// acpTurnStream sink so session/update notifications flow during a turn and, on
	// TurnEnd, resolving the held session/prompt call with the turn's stopReason via
	// holds.end (#751). Parented on runCtx, joined by wait() below after cancel().
	// claudeSessionsDir == "" disables streaming.
	streams := newACPTurnStreams(runCtx, pool, claudeSessionsDir, holds.end, logger)

	register := func(t *acp.Transport) {
		streams.attach(t)    // set the transport before any handler dispatch (register-before-Serve)
		registerHandshake(t) // initialize + authenticate (stateless, no pool)
		t.Register("session/new", newSessionHandler(pool, streams))
		t.Register("session/load", loadSessionHandler(pool, streams))
		t.Register("session/prompt", promptHandler(holds, func(id sessions.SessionID) (promptDeliverer, error) {
			// Return a true nil interface on error: a typed nil *sessions.Session
			// would wrap a non-nil interface and defeat the handler's err
			// short-circuit (the same trap the cancel handler documents).
			sess, err := pool.Lookup(id)
			if err != nil {
				return nil, err
			}
			return sess, nil
		}, logger))
		t.Register("session/cancel", cancelSessionHandler(func(p json.RawMessage) (interrupter, error) {
			// Return a true nil interface on error: resolveCancelTarget's typed
			// *supervisor.Supervisor would otherwise wrap a nil pointer in a
			// non-nil interface and defeat the handler's err short-circuit.
			sup, err := resolveCancelTarget(pool, p)
			if err != nil {
				return nil, err
			}
			return sup, nil
		}))
		t.Register("session/set_mode", setModeHandler)                  // single-mode pin (ADR 027)
		t.Register("session/set_config_option", setConfigOptionHandler) // no config surface (ADR 027)
	}
	serveErr := serveACP(runCtx, stdin, stdout, logger, register)

	// Stop pool.Run and every producer (the EOF path needs this; the signal path
	// already cancelled), then join: producers first (streams.wait), then the pool
	// (<-poolErr). Ordering is not load-bearing — both respond to the one cancel()
	// — but joining the consumers first reads cleanly and leaves no goroutine
	// outliving the call.
	cancel()
	streams.wait()
	<-poolErr
	return serveErr
}

// newSessionResult is the session/new (and session/load) success payload. The
// unexported type with json-tagged fields marshals fine and adds zero exported
// types. Modes advertises the single-mode pin (ADR 027 Open Item #1) so a host is
// never offered a mode switch that does nothing — populated from pinnedModeState()
// at both construction sites.
type newSessionResult struct {
	SessionID string           `json:"sessionId"`
	Modes     sessionModeState `json:"modes"`
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
func newSessionHandler(pool *sessions.Pool, streams *acpTurnStreams) acp.Handler {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		id, err := pool.Create(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("session/new: %w", err)
		}
		// Start the outbound turn-event stream for the fresh session so
		// session/update notifications flow during its turns (idempotent).
		streams.start(id)
		return newSessionResult{SessionID: string(id), Modes: pinnedModeState()}, nil
	}
}

// decodeSessionID extracts the sessionId shared by session/load and
// session/cancel from a JSON-RPC params object. A JSON error, an absent
// sessionId, or an empty sessionId returns *acp.Error{CodeInvalidParams}. The
// empty-string rejection is load-bearing: Pool.Lookup("") resolves to the parked
// bootstrap session rather than erroring, so an empty id must be refused here —
// before it reaches Lookup — or session/load would activate the bootstrap.
//
// The *acp.Error return (not a plain error) gives session/load the exact
// CodeInvalidParams wire code; it also satisfies error, so session/cancel passes
// it straight into its logged-and-discarded return with no conversion. Per the
// internal/acp diagnostics discipline, no params bytes are logged.
func decodeSessionID(params json.RawMessage) (sessions.SessionID, *acp.Error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", acp.NewError(acp.CodeInvalidParams, "invalid params")
	}
	if p.SessionID == "" {
		return "", acp.NewError(acp.CodeInvalidParams, "missing sessionId")
	}
	return sessions.SessionID(p.SessionID), nil
}

// loadSessionHandler returns the acp.Handler for session/load: resume an existing
// session by id. It resolves the id with Pool.Lookup (miss → error, no side
// effect) then Pool.Activate (idempotent on the already-active session, so no
// second claude spawns — divergence 6; re-activates in place for the evicted
// case). An unknown id maps to CodeInvalidParams and spawns nothing. GetOrCreate
// is deliberately NOT used: it creates on miss, which would spawn a fresh claude
// for an unknown id and break both AC-2 and divergence 6.
func loadSessionHandler(pool *sessions.Pool, streams *acpTurnStreams) acp.Handler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		id, aerr := decodeSessionID(params)
		if aerr != nil {
			return nil, aerr
		}
		if _, err := pool.Lookup(id); err != nil {
			if errors.Is(err, sessions.ErrSessionNotFound) {
				return nil, acp.NewError(acp.CodeInvalidParams, "unknown session")
			}
			return nil, fmt.Errorf("session/load: %w", err)
		}
		if err := pool.Activate(ctx, id); err != nil {
			return nil, fmt.Errorf("session/load: %w", err)
		}
		// Idempotent: for a this-process id whose stream is already running this is
		// a no-op; the call keeps "every addressable session has a running stream"
		// honest and future-proofs a persistence-backed load.
		streams.start(id)
		return newSessionResult{SessionID: string(id), Modes: pinnedModeState()}, nil
	}
}

// resolveCancelTarget maps a session/cancel notification's params onto the
// per-session supervisor. It returns the supervisor handle T9 (#753) will signal
// with the abort keystroke, or an error when sessionId is missing/malformed/empty
// or names no session. Factored out of the handler so AC-4's "resolves onto the
// correct session's supervisor" is unit-testable without T9's abort logic, and so
// T9 has a named extension point.
func resolveCancelTarget(pool *sessions.Pool, params json.RawMessage) (*supervisor.Supervisor, error) {
	id, aerr := decodeSessionID(params)
	if aerr != nil {
		return nil, aerr
	}
	sess, err := pool.Lookup(id)
	if err != nil {
		return nil, fmt.Errorf("session/cancel: %w", err)
	}
	return sess.Supervisor(), nil
}

// interrupter is the one-method cancel-actuation seam: *supervisor.Supervisor
// satisfies it via SendEsc (#726), the same lever the mobile interrupt /
// modal_cancel frames route through (relay.Interrupter). Declared here in package
// main so cancelSessionHandler is unit-testable with a double, while resolution
// correctness stays in resolveCancelTarget's own test.
type interrupter interface{ SendEsc() error }

// cancelSessionHandler returns the acp.Handler for session/cancel, an ACP
// notification that aborts the running turn. resolve maps the params onto the
// target session's interrupter (the composition root wires resolveCancelTarget).
// On a resolve error the handler returns it so dispatchNotification logs it once
// and writes no response frame (unchanged from #762). On success it actuates the
// interrupt best-effort: a SendEsc failure (no live turn / mid-teardown) is
// swallowed — there is nothing to roll back and a notification owes no reply,
// mirroring relay.handleInterrupt. dispatchNotification discards the (nil, nil)
// return, so no frame is written on either path.
func cancelSessionHandler(resolve func(json.RawMessage) (interrupter, error)) acp.Handler {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		intr, err := resolve(params)
		if err != nil {
			return nil, err
		}
		_ = intr.SendEsc()
		return nil, nil
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
// Close (docs/lessons.md #78, "Closing a fd to interrupt a goroutine's Read
// requires O_NONBLOCK") — Close itself would then block on the fd's
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
