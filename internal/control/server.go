package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions"
	"github.com/pyrycode/pyrycode/internal/supervisor"
)

// ErrInstanceRunning is returned by [Server.Listen] when another live pyry
// is already answering on the configured socket path. Distinct from a
// stale-file scenario so callers can present a polished diagnostic without
// grepping the error message.
var ErrInstanceRunning = errors.New("another pyry instance is already running")

// dialProbeTimeout is how long Listen waits for a live-instance probe to
// connect before treating the socket as stale. Short enough not to delay
// the common case (no prior pyry — connection refused fires instantly),
// long enough to absorb a loaded system.
const dialProbeTimeout = 200 * time.Millisecond

// Session is the per-session view the control server depends on. *sessions.Session
// satisfies it structurally; tests fake it directly. Defining it here (where it
// is consumed) keeps the sessions package free of control-plane concerns.
type Session interface {
	State() supervisor.State
	Attach(in io.Reader, out io.Writer) (done <-chan struct{}, err error)
	// Activate wakes an evicted session and blocks until the supervisor
	// is running again (or ctx cancels). A no-op on an already-active
	// session. handleAttach calls this before Attach so the bridge has a
	// live claude on the other side.
	Activate(ctx context.Context) error
	// Resize applies the given window size (rows-then-cols) to the
	// session's PTY. Returns sessions.ErrAttachUnavailable for sessions
	// with no bridge (foreground mode); handleAttach swallows that case.
	Resize(rows, cols uint16) error
}

// SessionResolver maps a SessionID to a Session. An empty id resolves to the
// default (bootstrap) entry — the seam Phase 1.1 will swap from Lookup("")
// to Lookup(req.SessionID) without changing handler shape.
type SessionResolver interface {
	Lookup(id sessions.SessionID) (Session, error)
	// ResolveID maps a loose-input session selector (full UUID, unique
	// prefix, or empty for bootstrap) to a concrete SessionID. Errors are
	// returned verbatim — handleAttach wraps them as "attach: <err>".
	ResolveID(arg string) (sessions.SessionID, error)
}

// Remover is the per-pool view the control server depends on for session
// removal. *sessions.Pool satisfies it structurally via Pool.Remove. Defined
// here, where it is consumed; tests fake it directly.
//
// Remove terminates the named session's child, drops its registry entry,
// and applies opts.JSONL to the on-disk transcript file. Returns
// sessions.ErrSessionNotFound for an unknown id,
// sessions.ErrCannotRemoveBootstrap for the bootstrap entry, or ctx.Err()
// if termination is cancelled. See Pool.Remove for the full contract.
type Remover interface {
	Remove(ctx context.Context, id sessions.SessionID, opts sessions.RemoveOptions) error
}

// Sessioner aggregates the lifecycle methods the control server dispatches
// to. Phase 1.1a-B1 added Create; Phase 1.1d-B1 adds Remove via the
// embedded Remover. Phase 1.1b/c/e (list, rename, attach orchestration)
// will continue this pattern — one method per verb, embedded onto Sessioner
// so NewServer's signature stays stable across the namespace's growth.
//
// *sessions.Pool satisfies Sessioner structurally — Pool.Create and
// Pool.Remove match the embedded interfaces' signatures exactly, so no
// covariant-return adapter is needed (contrast with poolResolver's Lookup).
// Defined here, where it is consumed; tests fake it directly.
//
// Create mints a new supervised session with the given (possibly empty)
// label and returns the new SessionID. Errors are surfaced to the client
// verbatim through Response.Error.
type Sessioner interface {
	Create(ctx context.Context, label string) (sessions.SessionID, error)
	Remover
}

// Server listens on a Unix domain socket and answers control requests.
//
// Lifecycle: NewServer → Listen → Serve(ctx) → Close. Listen creates the
// socket file (and any missing parent directory) and returns synchronously,
// so callers can fail fast if the path is unusable. Serve blocks until ctx
// is cancelled or the listener is closed. Close is safe to call multiple
// times and removes the socket file.
type Server struct {
	socketPath string
	sessions   SessionResolver
	logs       LogProvider
	shutdown   func()
	log        *slog.Logger
	sessioner  Sessioner

	mu       sync.Mutex
	listener net.Listener
	closed   bool
	closedCh chan struct{} // closed by Close, lets Serve's ctx-watcher exit

	// streamingWG tracks streaming-handler goroutines (currently: the
	// per-attach detach watcher). Serve waits on it before returning so a
	// caller blocked on Serve can be sure no per-conn goroutines are left.
	streamingWG sync.WaitGroup
}

// NewServer constructs a Server. The socket is not opened until Listen.
//
// sessions must be non-nil — every verb that needs session state resolves
// through it, and the first VerbStatus would otherwise nil-pointer-panic.
// Passing a nil resolver is a programmer error and panics at construction
// time so the failure surfaces immediately rather than on a request from
// a future shell.
//
// logs is optional. When nil, VerbLogs returns an error response.
//
// shutdown is optional. When nil, VerbStop returns an error response. When
// set, a successful VerbStop invokes it after acknowledging the client —
// typically the signal-driven context's cancel function so a stop request
// walks the same shutdown path as SIGINT/SIGTERM.
//
// sessioner is optional. When nil, VerbSessionsNew returns an error
// response ("sessions.new: no sessioner configured") — same precedent as
// logs/shutdown. The CLI ticket wires *sessions.Pool here.
//
// Foreground vs service mode is no longer surfaced as a distinct
// constructor parameter; it is a property of the resolved session's
// bridge. A foreground-mode session's Attach returns
// [sessions.ErrAttachUnavailable], which the attach handler maps back to
// the existing "no attach provider configured (daemon may be in
// foreground mode)" wire string for byte-identical client output.
func NewServer(socketPath string, sessions SessionResolver, logs LogProvider, shutdown func(), log *slog.Logger, sessioner Sessioner) *Server {
	if sessions == nil {
		panic("control.NewServer: sessions is required, got nil")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		socketPath: socketPath,
		sessions:   sessions,
		logs:       logs,
		shutdown:   shutdown,
		log:        log,
		sessioner:  sessioner,
		closedCh:   make(chan struct{}),
	}
}

// SocketPath returns the configured socket path.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// Listen creates the socket file. It is split from Serve so callers can
// surface "another pyry is running", "socket already in use", or
// "permission denied" errors before starting the supervisor proper.
//
// Stale sockets from a prior crash are removed transparently. A LIVE pyry
// on the same path is detected via a short Dial probe and rejected with
// [ErrInstanceRunning], rather than silently hijacking the path — see #17.
func (s *Server) Listen() error {
	if dir := filepath.Dir(s.socketPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create socket dir: %w", err)
		}
	}

	// Detect a live pyry on this path before touching the socket file. The
	// previous behaviour — unconditional os.Remove + net.Listen — would
	// silently unlink a running pyry's socket file and replace it with a
	// fresh listener, leaving the original pyry alive but unreachable
	// (issue #17).
	//
	// The probe distinguishes "stale file from a prior crash" (no peer
	// answers; Dial fails) from "live pyry already bound" (peer answers;
	// Dial succeeds). On Linux & macOS, dialling an unbound Unix socket
	// path returns ECONNREFUSED instantly; the timeout absorbs only the
	// case where a peer accepted but is unresponsive.
	if probe, err := net.DialTimeout("unix", s.socketPath, dialProbeTimeout); err == nil {
		_ = probe.Close()
		return fmt.Errorf("%w on %s — run `pyry status` to inspect, `pyry stop` to shut it down, or start this instance under a different name with -pyry-name",
			ErrInstanceRunning, s.socketPath)
	}

	// Dial failed — file is either absent or a stale leftover from a prior
	// crash that didn't run [Server.Close]. Either way, os.Remove is safe
	// here: a live listener would have answered the probe above.
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", s.socketPath, err)
	}

	// Single-user permissions — only the owner can talk to the daemon.
	// This 0600 chmod is currently the only authentication boundary on the
	// control socket. Any process running as the owning user can connect,
	// send VerbStop, and shut the daemon down. Acceptable for Phase 0
	// (single-user dev/service deployment); revisit before exposing the
	// socket across user boundaries (containers, multi-tenant hosts).
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(s.socketPath)
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	return nil
}

// Serve accepts connections and dispatches requests until ctx is cancelled.
// Listen must be called first.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln == nil {
		return errors.New("control: Listen must be called before Serve")
	}

	// Closing the listener unblocks Accept. We wire it to BOTH ctx
	// cancellation and explicit Close so direct callers of Close (without
	// cancelling ctx) don't leave the watcher goroutine alive forever.
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.closedCh:
			// Close already fired; nothing to do.
		}
	}()

	var handleWG sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			// If we're shutting down, this is expected.
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				handleWG.Wait()        // wait for in-flight handlers
				s.streamingWG.Wait()   // wait for active attach detach-watchers
				return nil
			}
			s.log.Warn("control: accept failed", "err", err)
			continue
		}
		handleWG.Add(1)
		go func() {
			defer handleWG.Done()
			s.handle(conn)
		}()
	}
}

// Close shuts the listener and removes the socket file. Idempotent. Safe to
// call from any goroutine, including the ctx-watcher goroutine in Serve.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.closedCh) // wakes Serve's ctx-watcher goroutine
	var firstErr error
	if s.listener != nil {
		if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			firstErr = err
		}
	}
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// handshakeTimeout caps how long the server waits for a client to send its
// JSON request after connecting. Cleared for streaming verbs (VerbAttach)
// before the ack, since they hold the connection open indefinitely.
const handshakeTimeout = 5 * time.Second

// handle dispatches a single client connection. One-shot verbs reply with one
// JSON Response and close. Streaming verbs (currently just VerbAttach) hand
// off connection ownership to a streaming handler; the deferred close in
// handle is suppressed for them.
//
// TODO: a misbehaving client could open a connection, write a partial JSON
// payload, and hold it. The handshake deadline + per-conn goroutine model
// bounds the damage to ~handshakeTimeout × N concurrent slow clients. With
// the 0600 socket perms the realistic N is "other processes the same user
// runs", which is fine for Phase 0. Revisit if the socket is ever exposed
// beyond that boundary.
func (s *Server) handle(conn net.Conn) {
	closeConn := true
	defer func() {
		if closeConn {
			_ = conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("decode request: %v", err)})
		return
	}

	switch req.Verb {
	case VerbStatus:
		// Phase 1.1 will swap "" → req.SessionID; the empty-id seam
		// resolves to the bootstrap session today.
		sess, err := s.sessions.Lookup("")
		if err != nil {
			_ = enc.Encode(Response{Error: err.Error()})
			return
		}
		_ = enc.Encode(Response{Status: buildStatus(sess.State())})
	case VerbLogs:
		s.handleLogs(enc)
	case VerbStop:
		s.handleStop(enc)
	case VerbAttach:
		// Streaming verb. handleAttach takes ownership of conn on
		// success and is responsible for closing it.
		if s.handleAttach(conn, enc, req.Attach) {
			closeConn = false
		}
	case VerbResize:
		s.handleResize(enc, req.Resize)
	case VerbSessionsNew:
		s.handleSessionsNew(enc, req.Sessions)
	case VerbSessionsRm:
		s.handleSessionsRm(enc, req.Sessions)
	default:
		_ = enc.Encode(Response{Error: fmt.Sprintf("unknown verb: %q", req.Verb)})
	}
}

// handleLogs serves a VerbLogs request: snapshot the ring buffer, write the
// payload, return.
func (s *Server) handleLogs(enc *json.Encoder) {
	if s.logs == nil {
		_ = enc.Encode(Response{Error: "logs: no log provider configured"})
		return
	}
	_ = enc.Encode(Response{Logs: &LogsPayload{
		Lines:    s.logs.Snapshot(),
		Capacity: s.logs.Cap(),
	}})
}

// handleStop serves a VerbStop request: ack, then invoke the configured
// shutdown callback. The ack is written before shutdown so the client reads
// confirmation even if the listener closes immediately.
func (s *Server) handleStop(enc *json.Encoder) {
	if s.shutdown == nil {
		_ = enc.Encode(Response{Error: "stop: no shutdown handler configured"})
		return
	}
	_ = enc.Encode(Response{OK: true})
	s.log.Info("control: stop requested")
	s.shutdown()
}

// handleSessionsNew serves a VerbSessionsNew request: invoke the sessioner
// to mint a new session and write the minted UUID back to the client.
//
// The handler runs Pool.Create against a fresh background context with a
// generous 30s deadline (well past the documented 2-15s claude spawn
// latency). Reusing the conn's handshake deadline would race the
// 5s handshake timer; a separate ctx is the simpler shape.
//
// Errors from sessioner.Create flow to Response.Error verbatim — Pool's own
// messages already carry package context (e.g. "sessions: create
// supervisor: ..."). Only the "sessioner not wired" diagnostic carries the
// "sessions.new: " prefix, mirroring "logs: no log provider configured".
func (s *Server) handleSessionsNew(enc *json.Encoder, payload *SessionsPayload) {
	if s.sessioner == nil {
		_ = enc.Encode(Response{Error: "sessions.new: no sessioner configured"})
		return
	}
	var label string
	if payload != nil {
		label = payload.Label
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := s.sessioner.Create(ctx, label)
	if err != nil {
		_ = enc.Encode(Response{Error: err.Error()})
		return
	}
	_ = enc.Encode(Response{SessionsNew: &SessionsNewResult{SessionID: string(id)}})
}

// handleSessionsRm serves a VerbSessionsRm request: invoke the sessioner to
// terminate the named session, drop its registry entry, and apply the JSONL
// disposition policy. Mirrors handleSessionsNew (fresh background ctx with a
// generous 30s deadline; verbatim error pass-through).
//
// Typed sentinels from Pool.Remove (sessions.ErrSessionNotFound,
// sessions.ErrCannotRemoveBootstrap) are detected via errors.Is — survives
// future wrapping — and surfaced through Response.ErrorCode so the client
// can reconstruct the sentinel for errors.Is matching after the JSON
// round-trip. Untyped errors (e.g. evict failures, registry persist
// failures) flow through Response.Error verbatim with no ErrorCode.
//
// Empty ID is rejected at the handler boundary (a missing-input condition,
// not a "not found" one). Unknown JSONLPolicy values surface as a clear
// "unknown jsonl policy" error rather than silently falling back.
func (s *Server) handleSessionsRm(enc *json.Encoder, payload *SessionsPayload) {
	if s.sessioner == nil {
		_ = enc.Encode(Response{Error: "sessions.rm: no sessioner configured"})
		return
	}
	if payload == nil || payload.ID == "" {
		_ = enc.Encode(Response{Error: "sessions.rm: missing id"})
		return
	}
	policy, err := toSessionsPolicy(payload.JSONLPolicy)
	if err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("sessions.rm: %v", err)})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = s.sessioner.Remove(ctx, sessions.SessionID(payload.ID), sessions.RemoveOptions{JSONL: policy})
	if err != nil {
		resp := Response{Error: err.Error()}
		switch {
		case errors.Is(err, sessions.ErrSessionNotFound):
			resp.ErrorCode = ErrCodeSessionNotFound
		case errors.Is(err, sessions.ErrCannotRemoveBootstrap):
			resp.ErrorCode = ErrCodeCannotRemoveBootstrap
		}
		_ = enc.Encode(resp)
		return
	}
	_ = enc.Encode(Response{OK: true})
}

// toSessionsPolicy maps the wire-level JSONLPolicy enum (string) to the
// internal sessions.JSONLPolicy enum (uint8). Empty string maps to
// JSONLLeave — matching sessions.JSONLPolicy's zero value, so a client
// that omits the field gets the documented default. Unknown values
// return an error rather than silently falling back.
func toSessionsPolicy(p JSONLPolicy) (sessions.JSONLPolicy, error) {
	switch p {
	case "", JSONLPolicyLeave:
		return sessions.JSONLLeave, nil
	case JSONLPolicyArchive:
		return sessions.JSONLArchive, nil
	case JSONLPolicyPurge:
		return sessions.JSONLPurge, nil
	default:
		return 0, fmt.Errorf("unknown jsonl policy %q", string(p))
	}
}

// handleAttach serves a VerbAttach request. Returns true iff connection
// ownership has been transferred to the streaming bridge — in which case the
// caller MUST NOT close conn (a goroutine spawned here handles that when the
// attach ends). Returns false on any pre-attach failure (no provider, bridge
// busy, etc.); in those cases the caller continues to own conn and will
// close it normally.
func (s *Server) handleAttach(conn net.Conn, enc *json.Encoder, payload *AttachPayload) (handedOff bool) {
	var sessionID string
	if payload != nil {
		sessionID = payload.SessionID
	}
	// Two-step resolve-then-lookup. ResolveID maps the loose-input
	// selector (full UUID, unique prefix, or empty → bootstrap) to a
	// concrete SessionID; Lookup then guards against the session being
	// removed between the two RLock acquires. Both errors encode as
	// "attach: <err>" before any bridge work, leaving the bridge state
	// untouched on the failure path.
	id, err := s.sessions.ResolveID(sessionID)
	if err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
		return false
	}
	sess, err := s.sessions.Lookup(id)
	if err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
		return false
	}
	// Clear the handshake deadline BEFORE registering the bridge or writing
	// the ack. Once attach starts, both directions are streaming — the
	// bridge's input goroutine reads from conn until EOF, and the supervisor's
	// PTY output goroutine writes to conn at unpredictable times. A handshake
	// deadline still on the conn would mistakenly terminate either side after
	// a quiet window. A successful attach should keep the conn alive
	// indefinitely.
	_ = conn.SetDeadline(time.Time{})

	// Wake an evicted session before binding the bridge. The 30s window
	// caps the documented 2-15s respawn latency with safety margin; a
	// busted respawn surfaces as a clean error to the client rather than
	// a hung attach.
	activateCtx, cancelActivate := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelActivate()
	if err := sess.Activate(activateCtx); err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("attach: activate: %v", err)})
		return false
	}

	// Apply handshake geometry. Zero in either dimension is the protocol
	// "unknown / don't touch" sentinel — see AttachPayload omitempty tags.
	// int → uint16 boundary: pathological client sizes >65535 are clamped
	// silently. The wire's cols-then-rows order is swapped here to match
	// Bridge.Resize's rows-then-cols (which mirrors pty.Winsize).
	if payload != nil && payload.Cols > 0 && payload.Rows > 0 {
		rows := clampUint16(payload.Rows)
		cols := clampUint16(payload.Cols)
		if err := sess.Resize(rows, cols); err != nil &&
			!errors.Is(err, sessions.ErrAttachUnavailable) {
			s.log.Warn("control: attach geometry resize failed", "err", err, "rows", rows, "cols", cols)
		}
	}

	done, err := sess.Attach(conn, conn)
	if err != nil {
		// Foreground-mode session has no bridge. Map sessions.ErrAttachUnavailable
		// to the Phase 0 wire string verbatim — a bare fmt.Sprintf("attach: %v")
		// would surface "attach: sessions: attach unavailable (no bridge)",
		// observable drift versus today's client output.
		if errors.Is(err, sessions.ErrAttachUnavailable) {
			_ = enc.Encode(Response{Error: "attach: no attach provider configured (daemon may be in foreground mode)"})
			return false
		}
		_ = enc.Encode(Response{Error: fmt.Sprintf("attach: %v", err)})
		return false
	}
	s.log.Info("control: client attached")
	_ = enc.Encode(Response{OK: true})

	// Connection ownership transferred. Close it when the bridge's input
	// pump ends (typically: client disconnected). Tracked on streamingWG
	// so Serve waits for it before returning.
	s.streamingWG.Add(1)
	go func() {
		defer s.streamingWG.Done()
		<-done
		_ = conn.Close()
		s.log.Info("control: client detached")
	}()
	return true
}

// handleResize serves a VerbResize request. Geometry is best-effort: any
// failure inside the seam (transient EBADF on a closed fd, foreground
// session with no bridge) is logged and the client gets an OK ack. The
// only error responses are pre-seam routing failures (resolver lookup
// failure, missing payload). Decoding errors on the request body itself
// land in handle's existing decode-error branch and never reach here.
//
// The resize conn is independent of the attach conn (each control request
// is a fresh connection), so a malformed or routing-failed resize cannot
// tear down an attached session — that property is structural, not coded.
func (s *Server) handleResize(enc *json.Encoder, payload *ResizePayload) {
	if payload == nil {
		_ = enc.Encode(Response{Error: "resize: missing payload"})
		return
	}
	id, err := s.sessions.ResolveID(payload.SessionID)
	if err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("resize: %v", err)})
		return
	}
	sess, err := s.sessions.Lookup(id)
	if err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("resize: %v", err)})
		return
	}
	// Zero in either dim is the "unknown / don't touch" sentinel — see
	// ResizePayload omitempty tags. Cols-then-rows on the wire, swapped
	// here to match Bridge.Resize's rows-then-cols (mirroring pty.Winsize).
	if payload.Cols > 0 && payload.Rows > 0 {
		rows := clampUint16(payload.Rows)
		cols := clampUint16(payload.Cols)
		if err := sess.Resize(rows, cols); err != nil &&
			!errors.Is(err, sessions.ErrAttachUnavailable) {
			s.log.Warn("control: resize failed",
				"err", err, "rows", rows, "cols", cols, "session", id)
		}
	}
	_ = enc.Encode(Response{OK: true})
}

// clampUint16 narrows a non-negative int to uint16, clamping out-of-range
// values to math.MaxUint16. Callers guard against negative inputs (the
// wire protocol's omitempty + > 0 check). No logging on clamp — a client
// reporting dimensions over 65535 is buggy or hostile, and logging it
// would just amplify the noise.
func clampUint16(v int) uint16 {
	if v > math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(v)
}

// buildStatus converts a supervisor.State snapshot into the wire format.
func buildStatus(st supervisor.State) *StatusPayload {
	p := &StatusPayload{
		Phase:        string(st.Phase),
		ChildPID:     st.ChildPID,
		StartedAt:    st.StartedAt.UTC().Format(time.RFC3339),
		Uptime:       time.Since(st.StartedAt).Round(time.Second).String(),
		RestartCount: st.RestartCount,
	}
	if st.LastUptime > 0 {
		p.LastUptime = st.LastUptime.Round(time.Millisecond).String()
	}
	if st.NextBackoff > 0 {
		p.NextBackoff = st.NextBackoff.Round(time.Millisecond).String()
	}
	return p
}
