package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/permbridge"
	"github.com/pyrycode/pyrycode/internal/sessions"
)

// ErrInstanceRunning is returned by [Server.Listen] when another live pyry
// is already answering on the configured socket path. Distinct from a
// stale-file scenario so callers can present a polished diagnostic without
// grepping the error message.
var ErrInstanceRunning = errors.New("another pyry instance is already running")

// ErrConnNotFound is returned by Rekeyer.Rekey when the named v2 conn is
// not currently open on the session manager. The dispatcher maps this to
// ErrCodeConnNotFound on the wire (via errors.Is, so wrapped sentinels
// also map); the client helper reconstructs this sentinel from the wire
// token so callers can errors.Is against it. Owned in this package
// because the Rekeyer contract is defined here — slice B's
// *relay.V2SessionManager wraps its internal not-found condition with
// %w against this sentinel.
var ErrConnNotFound = errors.New("rekey: conn not found")

// dialProbeTimeout is how long Listen waits for a live-instance probe to
// connect before treating the socket as stale. Short enough not to delay
// the common case (no prior pyry — connection refused fires instantly),
// long enough to absorb a loaded system.
const dialProbeTimeout = 200 * time.Millisecond

// Session is the per-session view the control server depends on. *sessions.Session
// satisfies it structurally; tests fake it directly. Defining it here (where it
// is consumed) keeps the sessions package free of control-plane concerns.
type Session interface {
	State() sessions.State
	// Activate wakes an evicted session and blocks until the supervisor
	// is running again (or ctx cancels). A no-op on an already-active
	// session. handleAttach calls this before Attach so the bridge has a
	// live claude on the other side.
	Activate(ctx context.Context) error
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

// Renamer is the per-pool view the control server depends on for session
// rename. *sessions.Pool satisfies it structurally via Pool.Rename. Defined
// here, where it is consumed; tests fake it directly.
//
// Rename updates the named session's label and persists the change to the
// registry. Empty newLabel is permitted and clears the on-disk label to "".
// Returns sessions.ErrSessionNotFound when id is not present in the pool.
// See Pool.Rename for the full contract, including the no-op shape
// (newLabel == current label) returning nil without persisting.
//
// Rename does not take a context — Pool.Rename's signature is
// (id, newLabel) error and the operation is bounded by a single Pool.mu
// critical section + saveLocked. The seam mirrors Pool.Rename's shape so
// *sessions.Pool satisfies it adapter-free.
type Renamer interface {
	Rename(id sessions.SessionID, newLabel string) error
}

// Lister is the per-pool view the control server depends on for the
// sessions.list verb. *sessions.Pool satisfies it structurally via
// Pool.List. Defined here, where it is consumed; tests fake it directly.
//
// List returns a snapshot of every session in the pool — bootstrap and
// minted alike — sorted by LastActiveAt descending with SessionID
// ascending tiebreak. The bootstrap entry's empty on-disk label is
// substituted with "bootstrap" by Pool.List itself; this seam renders
// verbatim. Read-only: does not bump LastActiveAt or transition state.
// See Pool.List for the full contract.
//
// List does not take a context — Pool.List's signature is
// () []SessionInfo and the operation is bounded by Pool.mu (RLock) +
// each Session.lcMu (briefly). The seam mirrors Pool.List's shape so
// *sessions.Pool satisfies it adapter-free.
type Lister interface {
	List() []sessions.SessionInfo
}

// GetOrCreator is the per-pool view the control server depends on for
// take-or-create attaches (Phase 1.3b). *sessions.Pool satisfies it
// structurally via Pool.GetOrCreate. Defined here, where it is consumed;
// tests fake it directly.
//
// GetOrCreate returns the canonical SessionID for id, creating a new
// session under that exact UUID if one is not already registered. Errors:
// sessions.ErrInvalidSessionID for empty / non-UUIDv4 ids;
// sessions.ErrPoolNotRunning when Pool.Run is not active. See
// Pool.GetOrCreate for the full contract, including the atomic
// register+persist+supervise critical section.
type GetOrCreator interface {
	GetOrCreate(ctx context.Context, id sessions.SessionID, label string) (sessions.SessionID, error)
}

// Rekeyer is the per-conn rekey-trigger view the control server depends
// on for VerbRekey. Slice B's *relay.V2SessionManager satisfies it via
// TriggerRekey; slice A (#459) ships no production implementer — until
// SetRekeyer is called handleRekey replies "rekey: no rekeyer
// configured".
//
// Rekey triggers an immediate Noise re-key on the named conn (the
// operator-driven "manual" rekey path in docs/protocol-mobile.md
// § Re-key). Returns ErrConnNotFound (possibly wrapped) when no conn
// with the given id is currently open on the v2 session manager — the
// dispatcher maps this to ErrCodeConnNotFound on the wire via errors.Is.
// Any other non-nil error is surfaced verbatim through Response.Error
// with no ErrorCode.
//
// Plumbing channel: the operator-facing verb routes through the control
// socket rather than a direct in-process call because the
// `pyry rekey <conn_id>` subcommand runs in a separate process; an
// in-process channel is not a workable alternative when the trigger
// originates outside the daemon process. The trade-off is one socket
// round-trip on a verb the operator invokes interactively — immaterial
// in practice.
//
// Not embedded into Sessioner: slice B's implementer is a different
// type from *sessions.Pool, so aggregating would force a stub method on
// Pool or a covariant adapter. Free-standing matches the
// Remover/Renamer/Lister "interface-at-the-consumer" pattern.
type Rekeyer interface {
	Rekey(ctx context.Context, connID string) error
}

// Sessioner aggregates the lifecycle methods the control server dispatches
// to. Phase 1.1a-B1 added Create; Phase 1.1d-B1 added Remove via the
// embedded Remover; Phase 1.1c-B1 added Rename via the embedded Renamer;
// Phase 1.1b-B1 adds List via the embedded Lister. Phase 1.1e (attach
// orchestration) will continue this pattern — one method (or named
// sub-interface) per verb, embedded onto Sessioner so NewServer's
// signature stays stable across the namespace's growth.
//
// *sessions.Pool satisfies Sessioner structurally — Pool.Create,
// Pool.Remove, Pool.Rename and Pool.List match the embedded interfaces'
// signatures exactly, so no covariant-return adapter is needed (contrast
// with poolResolver's Lookup). Defined here, where it is consumed; tests
// fake it directly.
//
// Create mints a new supervised session with the given (possibly empty)
// label and returns the new SessionID. Errors are surfaced to the client
// verbatim through Response.Error.
type Sessioner interface {
	Create(ctx context.Context, label string) (sessions.SessionID, error)
	GetOrCreator
	Remover
	Renamer
	Lister
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

	// handshakeTimeout bounds the initial request read per conn. Defaults to
	// defaultHandshakeTimeout in NewServer; same-package tests may shrink it
	// before Serve launches to keep timing tests sub-second. Written once
	// before Serve starts and read-only per-conn thereafter — no lock needed.
	handshakeTimeout time.Duration

	mu       sync.Mutex
	listener net.Listener
	closed   bool
	closedCh chan struct{} // closed by Close, lets Serve's ctx-watcher exit
	// rekeyer is the optional Rekeyer used to service VerbRekey
	// requests. Installed via SetRekeyer between NewServer and Serve
	// (so NewServer's signature stays frozen across the 34-call-site
	// fan-out — see #451 split rationale). Read under s.mu by
	// handleRekey; the lock is released before the (potentially
	// blocking) call into the Rekeyer. Zero-value (nil) is the
	// production state until slice B (#460) lands a V2SessionManager.
	rekeyer Rekeyer

	// approvals is the optional pending-approval registry servicing
	// VerbMCPApprove, installed Rekeyer-style via SetApprovalRegistry so
	// NewServer's signature stays frozen. approvalTimeout is the
	// human-approval window handed to permbridge.Register. Since #1932 wired
	// the daemon's liveness report into the registry, that window is a
	// RE-CHECK INTERVAL rather than a hard deadline: the registry denies on it
	// only while nobody can answer the approval, and otherwise re-arms the same
	// window and asks again. Both are read once per request under
	// s.mu, before the lock is released for the (blocking) Await. Nil
	// registry is the production state until the daemon composition wires
	// it, and stays nil in v1/foreground — handleApprove then replies
	// "unavailable" rather than panicking.
	approvals       *permbridge.Registry
	approvalTimeout time.Duration

	// approvalSurfacer, when set, raises a parked approval to interactive clients
	// as a modal_shown (the #1080 stream modal wiring) or, when the approval is
	// claude's clarifying-question tool call, as a question_shown (#1973), and
	// returns a retire closure handleApprove defers to guarantee client-side
	// cleanup + dismissal on EVERY terminal Await path (answer, timeout,
	// disconnect, shutdown). Which frame family a request raises is entirely the
	// surfacer's business; this package neither branches on it nor names it in the
	// seam's signature, which is why that signature did not have to move. Installed
	// Rekeyer-style via SetApprovalSurfacer so NewServer's signature stays frozen;
	// read once per request under s.mu alongside approvals. Nil (v1/foreground/
	// pre-#1080) leaves handleApprove parking-and-awaiting with no client modal —
	// the completer still resolves, just without a phone prompt.
	approvalSurfacer func(permbridge.Request) func()

	// fileAttacher, when set, services VerbAttachFile: it maps the CALLING
	// session to its conversation, confines the claude-named path to that
	// conversation's recorded workspace, and files the bytes, answering with the
	// minted attachment id. Installed Rekeyer-style via SetFileAttacher so
	// NewServer's signature stays frozen across its call-site fan-out.
	//
	// A plain func rather than an interface, following approvalSurfacer rather
	// than approvals: every dependency the work needs — the conversation
	// registry, the session pool, the instance directory — lives at cmd/pyry's
	// composition root, and so does withinDir, which is unexported in package
	// main and unimportable here. This package therefore owns the wire and the
	// guard order, and nothing else.
	//
	// Read once per request under s.mu, then the lock is RELEASED before the
	// call — the same leaf-lock discipline handleApprove follows, and
	// load-bearing here for a different reason: the call reads a file off disk
	// and writes another, so holding s.mu across it would serialise every
	// control verb behind one attachment.
	//
	// Nil is the state until the daemon composition wires it, and stays nil in
	// v1/foreground — handleAttachFile then answers Response.Error rather than
	// panicking, the state mcp.approve sat in until #1080.
	fileAttacher func(sessionID, path string) (string, error)

	// channelCreator, when set, services VerbChannelNew: given the directory a
	// caller is standing in and an optional name, it confines that directory to
	// $HOME, resolves its symlinks, trust-marks the realpath, mints a session,
	// records a promoted conversation and persists the registry — answering with
	// the minted conversation id. Installed Rekeyer-style via SetChannelCreator
	// so NewServer's signature stays frozen across its call-site fan-out.
	//
	// A plain func rather than an interface, following fileAttacher for the same
	// reason: everything the work needs — the conversations registry, its
	// on-disk path, the session pool, and resolveSpawnDir, which is unexported
	// in package main and unimportable here — lives at cmd/pyry's composition
	// root. This package therefore owns the wire and the guard order, and
	// nothing else. In particular it does NO path handling: the cwd it received
	// reaches the creator verbatim, which is what keeps the confinement in one
	// named place instead of two.
	//
	// Read once per request under s.mu, then the lock is RELEASED before the
	// call — the leaf-lock discipline handleApprove and handleAttachFile follow,
	// load-bearing here because the call mints a session and writes two
	// registries, so holding s.mu across it would serialise every control verb
	// behind one channel creation.
	//
	// Nil is the state until the daemon composition wires it, and stays nil in
	// v1/foreground — handleChannelNew then answers Response.Error rather than
	// panicking.
	channelCreator func(cwd, name string) (string, error)

	// channelPoster, when set, services VerbChannelPost: given a channel's
	// display label and a message body, it resolves the label to a conversation
	// — creating one under the daemon's default workspace when nothing matches —
	// and records the content in that conversation's durable log. Installed via
	// SetChannelPoster, channelCreator's shape and for its reasons.
	//
	// Read once per request under s.mu, then the lock is RELEASED before the
	// call, the leaf-lock discipline handleApprove, handleAttachFile and
	// handleChannelNew follow. Load-bearing here because the miss path runs the
	// channel creator whole — minting a session and writing two registries — so
	// holding s.mu across it would serialise every control verb behind one post.
	//
	// Nil is the state until the daemon composition wires it, and stays nil in
	// v1/foreground — handleChannelPost then answers Response.Error rather than
	// panicking.
	channelPoster func(name, text string) error

	// pairingProvider services VerbPairingMint using daemon-authored runtime
	// values. Its pairing and error are both sensitive and never logged or
	// passed through as error detail by the control layer.
	pairingProvider func(deviceLabel string, allowRemotePermissions bool) (string, error)
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
// sessioner is optional. When nil, VerbSessionsNew, VerbSessionsRm,
// VerbSessionsRename, and VerbSessionsList all return error responses
// — same precedent as logs/shutdown. VerbSessionsHasID is independent
// of sessioner (consults SessionResolver instead) and answers
// correctly even with sessioner == nil. The CLI ticket wires
// *sessions.Pool here.
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
		socketPath:       socketPath,
		sessions:         sessions,
		logs:             logs,
		shutdown:         shutdown,
		log:              log,
		sessioner:        sessioner,
		handshakeTimeout: defaultHandshakeTimeout,
		closedCh:         make(chan struct{}),
	}
}

// SocketPath returns the configured socket path.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// SetRekeyer installs the Rekeyer used to service VerbRekey requests.
// Safe to call from any goroutine; canonically called once between
// NewServer and Serve as part of daemon startup. Passing nil clears the
// previously-installed Rekeyer (used by tests; production startup
// installs once and never clears).
//
// Threading the rekeyer through NewServer would cascade across every
// NewServer call site (~1 production + 10+ tests) — see #451 split
// rationale. SetRekeyer keeps the wire contract independent of the
// constructor.
func (s *Server) SetRekeyer(r Rekeyer) {
	s.mu.Lock()
	s.rekeyer = r
	s.mu.Unlock()
}

// SetApprovalRegistry installs the pending-approval registry and the
// human-approval timeout used to service VerbMCPApprove requests. Safe to
// call from any goroutine; canonically called once between NewServer and
// Serve as part of daemon startup. A nil registry (never calling this, or
// v1/foreground) leaves handleApprove replying "no approval registry
// configured" — the same nil-dependency-degrades-cleanly shape as
// SetRekeyer.
//
// timeout bounds a pending approval NOBODY CAN ANSWER: permbridge's
// registry-owned timer denies the request once that window elapses on an
// approval its liveness report calls unanswerable. With a report installed
// (#1932's daemon wiring) the bound is conditional — the wait ends within one
// window of the FIRST reading that says nobody can answer — and with none
// installed (v1/foreground/relay disabled) it stays the hard deadline it has
// always been. Threading it through NewServer would cascade across every call
// site, so it rides the setter like the Rekeyer (see #451 split rationale).
func (s *Server) SetApprovalRegistry(reg *permbridge.Registry, timeout time.Duration) {
	s.mu.Lock()
	s.approvals = reg
	s.approvalTimeout = timeout
	s.mu.Unlock()
}

// SetApprovalSurfacer installs the optional surfacer that raises a parked
// approval to interactive clients — as a modal_shown, or as a question_shown when
// the approval is claude's clarifying-question tool call (#1973) — and returns a
// retire closure handleApprove defers to guarantee client-side cleanup +
// dismissal on every terminal Await path (#1080). Safe to call from any goroutine; canonically
// called once between NewServer and Serve, after SetApprovalRegistry, as part of
// daemon startup. A nil surfacer (never calling this, or v1/foreground/relay
// disabled) leaves handleApprove parking-and-awaiting with no client-facing modal
// — the pre-#1080 behaviour. Mirrors SetApprovalRegistry so NewServer's signature
// stays frozen across its call-site fan-out.
func (s *Server) SetApprovalSurfacer(surface func(permbridge.Request) func()) {
	s.mu.Lock()
	s.approvalSurfacer = surface
	s.mu.Unlock()
}

// SetFileAttacher installs the dependency that services VerbAttachFile: given
// the calling session's id and a claude-named host path, it confines the path
// to that session's conversation's recorded workspace, files the bytes, and
// returns the minted attachment id. Safe to call from any goroutine;
// canonically called once between NewServer and Serve as part of daemon
// startup. Passing nil clears a previously-installed attacher (used by tests;
// production startup installs once and never clears).
//
// Mirrors SetApprovalSurfacer so NewServer's signature stays frozen across its
// call-site fan-out (see #451 split rationale). A nil attacher — never calling
// this, or v1/foreground — leaves handleAttachFile replying "attachment.file:
// no file attacher configured", the same nil-dependency-degrades-cleanly shape
// as SetRekeyer and SetApprovalRegistry.
//
// The returned error's TEXT reaches the wire verbatim, so an implementation
// must keep every refusal reason static: no host path, no filename, no
// workspace and no session id. docs/protocol-mobile.md § Attachments bans
// logging a filename for a privacy reason sanitising does not lift, and a host
// path is worse. Reasons must still be actionable — claude acts on them by
// writing the file into the workspace and calling again, which is what an
// explicit tool call buys over a daemon-side sweep.
func (s *Server) SetFileAttacher(attach func(sessionID, path string) (string, error)) {
	s.mu.Lock()
	s.fileAttacher = attach
	s.mu.Unlock()
}

// SetChannelCreator installs the dependency that services VerbChannelNew: given
// a host directory and an optional name, it creates a promoted conversation
// rooted at that directory and returns the minted conversation id. Safe to call
// from any goroutine; canonically called once between NewServer and Serve as
// part of daemon startup. Passing nil clears a previously-installed creator
// (used by tests; production startup installs once and never clears).
//
// Mirrors SetFileAttacher so NewServer's signature stays frozen across its
// call-site fan-out (see #451 split rationale). A nil creator — never calling
// this, or v1/foreground — leaves handleChannelNew replying "channel.new: no
// channel creator configured", the same nil-dependency-degrades-cleanly shape
// as SetRekeyer and SetApprovalRegistry.
//
// TWO OBLIGATIONS on an implementation, both load-bearing:
//
// The returned error's TEXT reaches the wire verbatim, so every refusal reason
// must be STATIC: no requested path, no resolved path, no $HOME. This is not
// general hygiene — the validator such an implementation is expected to reuse
// (cmd/pyry's resolveSpawnDir) wraps a confinement error whose message embeds
// both the resolved path and the operator's home directory, so forwarding it
// unchanged is the failure mode to design against, not a hypothetical one.
//
// And cwd is passed through UNVALIDATED — this package deliberately does no
// path handling — so the implementation owns confinement in full: confine to
// $HOME and resolve symlinks BEFORE trust-marking anything, since a trust-mark
// carries no $HOME bound of its own. An empty cwd never arrives from here, but
// an implementation must refuse one anyway rather than treat it as a default:
// the seam it would otherwise reach reads the empty string as "use the shared
// trusted workdir" and skips validation entirely, which is the same fail-open
// shape SetFileAttacher's empty-sessionID guard exists to close.
func (s *Server) SetChannelCreator(create func(cwd, name string) (string, error)) {
	s.mu.Lock()
	s.channelCreator = create
	s.mu.Unlock()
}

// SetChannelPoster installs the dependency that services VerbChannelPost: given
// a channel's display label and a message body, it records that content in the
// named conversation's durable log, creating the channel first when the label
// matches nothing. Safe to call from any goroutine; canonically called once
// between NewServer and Serve as part of daemon startup. Passing nil clears a
// previously-installed poster (used by tests; production startup installs once
// and never clears).
//
// Mirrors SetChannelCreator, and for the same reason: everything the work needs
// — the conversation registry, the durable log, the daemon's resolved default
// workspace, and the channel creator itself — lives at cmd/pyry's composition
// root. This package therefore owns the wire and the guard order and nothing
// else. It does NO name handling in particular: the label reaches the poster
// exactly as it arrived, not trimmed and not case-folded, so the matching rule
// lives in one place.
//
// TWO OBLIGATIONS on an implementation, both load-bearing:
//
// The returned error's TEXT reaches the wire verbatim, so every refusal reason
// must be STATIC — or, where a count genuinely has to be reported, a static
// format over a value the DAEMON derived. In particular a refusal must not fold
// back the label it was given: that is caller-authored text, and this verb's
// whole answer to a mistyped label is that the operator sees a diagnostic, not
// an echo.
//
// And the message body must never be logged. It is conversation content, and
// the durable log is the one place it may be written — the discipline
// appendConversationHistory records for the producers that share it.
func (s *Server) SetChannelPoster(post func(name, text string) error) {
	s.mu.Lock()
	s.channelPoster = post
	s.mu.Unlock()
}

// SetPairingProvider installs the dependency that services VerbPairingMint.
// The provider owns every daemon-authored mint input and returns the existing
// opaque pairing string. Safe to call from any goroutine; canonically called
// once between NewServer and Serve. Passing nil clears an installed provider.
// Provider errors are projected to fixed content-free text by the handler.
func (s *Server) SetPairingProvider(provider func(deviceLabel string, allowRemotePermissions bool) (string, error)) {
	s.mu.Lock()
	s.pairingProvider = provider
	s.mu.Unlock()
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
				handleWG.Wait() // wait for in-flight handlers
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
	if s.closed {
		s.mu.Unlock()
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
	s.mu.Unlock()
	return firstErr
}

// defaultHandshakeTimeout caps how long the server waits for a client to send
// its JSON request after connecting. Applied per-conn as s.handshakeTimeout
// (overridable in same-package tests). Two handlers move it once the request
// has been read, and they move it differently: handleApprove clears it for the
// length of a blocking approval wait, since the conn stays open for however
// long the human decision takes; the one-shot session verbs extend — not clear
// — it past their long op, so a slow Create/Remove response still lands (see
// handleSessionsNew, #865).
const defaultHandshakeTimeout = 5 * time.Second

// sessionOpTimeout is the ctx budget handleSessionsNew / handleSessionsRm give
// Pool.Create / Pool.Remove — generous past the documented 2-15s claude spawn
// latency. It stays the binding budget for the operation.
const sessionOpTimeout = 30 * time.Second

// sessionOpConnGrace is the margin the conn write deadline sits above
// sessionOpTimeout while a session verb runs its long op, so the op ctx (not
// the conn deadline) bounds the normal path while a genuinely stuck response
// write still has an upper bound.
const sessionOpConnGrace = 5 * time.Second

// handle dispatches a single client connection. Every verb it dispatches
// replies with one JSON Response and returns, so the deferred close runs for
// all of them — no handler takes ownership of the conn.
//
// TODO: a misbehaving client could open a connection, write a partial JSON
// payload, and hold it. The handshake deadline + per-conn goroutine model
// bounds the damage to ~s.handshakeTimeout × N concurrent slow clients. With
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
	_ = conn.SetDeadline(time.Now().Add(s.handshakeTimeout))

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
	case VerbSessionsNew:
		s.handleSessionsNew(conn, enc, req.Sessions)
	case VerbSessionsRm:
		s.handleSessionsRm(conn, enc, req.Sessions)
	case VerbSessionsRename:
		s.handleSessionsRename(enc, req.Sessions)
	case VerbSessionsList:
		s.handleSessionsList(enc)
	case VerbSessionsHasID:
		s.handleSessionsHasID(enc, req.Sessions)
	case VerbRekey:
		s.handleRekey(enc, req.Rekey)
	case VerbMCPApprove:
		// Blocking verb: handleApprove owns conn for the wait (it clears the
		// handshake deadline and runs a disconnect/shutdown watcher) but does
		// not hand off ownership — handle's deferred conn.Close still runs on
		// return and reaps the watcher's reader.
		s.handleApprove(conn, enc, req.Approve)
	case VerbAttachFile:
		s.handleAttachFile(conn, enc, req.AttachFile)
	case VerbChannelNew:
		s.handleChannelNew(conn, enc, req.Channel)
	case VerbChannelPost:
		s.handleChannelPost(conn, enc, req.ChannelPost)
	case VerbPairingMint:
		s.handlePairingMint(conn, enc, req.Pairing)
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
// generous sessionOpTimeout deadline (well past the documented 2-15s claude
// spawn latency). Before the long call, the conn write deadline — set to the
// handshake timeout in handle — is extended past that budget so the response
// write does not fail after the handshake window elapses (mirrors
// handleAttach, which clears the deadline for its indefinite stream; a
// one-shot verb bounds it instead). See #865.
//
// Errors from sessioner.Create flow to Response.Error verbatim — Pool's own
// messages already carry package context (e.g. "sessions: create
// supervisor: ..."). Only the "sessioner not wired" diagnostic carries the
// "sessions.new: " prefix, mirroring "logs: no log provider configured".
func (s *Server) handleSessionsNew(conn net.Conn, enc *json.Encoder, payload *SessionsPayload) {
	if s.sessioner == nil {
		_ = enc.Encode(Response{Error: "sessions.new: no sessioner configured"})
		return
	}
	var label string
	if payload != nil {
		label = payload.Label
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionOpTimeout)
	defer cancel()
	// Extend the conn write deadline past the op budget before the long call.
	// ctx (sessionOpTimeout) stays the binding budget; this is a backstop so a
	// genuinely stuck response write still has an upper bound. Best-effort like
	// handleAttach — a SetDeadline error on a broken conn surfaces on the
	// Encode below.
	_ = conn.SetDeadline(time.Now().Add(sessionOpTimeout + sessionOpConnGrace))
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
func (s *Server) handleSessionsRm(conn net.Conn, enc *json.Encoder, payload *SessionsPayload) {
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
	ctx, cancel := context.WithTimeout(context.Background(), sessionOpTimeout)
	defer cancel()
	// Extend the conn write deadline past the op budget before the long
	// Remove; see handleSessionsNew.
	_ = conn.SetDeadline(time.Now().Add(sessionOpTimeout + sessionOpConnGrace))
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

// handleSessionsRename serves a VerbSessionsRename request: invoke the
// sessioner to update the named session's label and acknowledge success.
//
// Unlike handleSessionsNew / handleSessionsRm there is no
// context.WithTimeout — Pool.Rename does not take ctx and the operation
// is bounded by a single Pool.mu critical section + saveLocked. The
// conn's existing handshake deadline already bounds slow-write
// pathologies; introducing a seam-level ctx the implementation would
// discard would lie about cancellability.
//
// Empty NewLabel is forwarded unchanged — Pool.Rename treats it as
// "clear the label" per #62. Empty ID is rejected at the boundary
// (a missing-input condition, not a "not found" one) for symmetry
// with handleSessionsRm. The typed sentinel ErrSessionNotFound from
// Pool.Rename surfaces through Response.ErrorCode so the client can
// reconstruct the sentinel for errors.Is matching after the JSON
// round-trip; untyped errors flow through Response.Error verbatim.
func (s *Server) handleSessionsRename(enc *json.Encoder, payload *SessionsPayload) {
	if s.sessioner == nil {
		_ = enc.Encode(Response{Error: "sessions.rename: no sessioner configured"})
		return
	}
	if payload == nil || payload.ID == "" {
		_ = enc.Encode(Response{Error: "sessions.rename: missing id"})
		return
	}
	if err := s.sessioner.Rename(sessions.SessionID(payload.ID), payload.NewLabel); err != nil {
		resp := Response{Error: err.Error()}
		if errors.Is(err, sessions.ErrSessionNotFound) {
			resp.ErrorCode = ErrCodeSessionNotFound
		}
		_ = enc.Encode(resp)
		return
	}
	_ = enc.Encode(Response{OK: true})
}

// handleSessionsList serves a VerbSessionsList request: snapshot every
// session in the pool through Pool.List and write the result back to the
// client. Mirrors handleSessionsRename's nil-sessioner shape (verbatim
// error message, no context.WithTimeout — Pool.List's signature does not
// take ctx and the operation is in-memory).
//
// Pool.List does not return errors — the only failure path here is the
// nil-sessioner branch. Sort order is whatever Pool.List returns
// (LastActiveAt desc, SessionID asc tiebreak); this layer does not
// re-sort.
//
// LifecycleState is encoded as a string via lifecycleState.String() —
// the same encoding the on-disk registry uses, so the wire and the
// registry agree on token spelling. LastActiveAt is passed through as
// time.Time; encoding/json marshals to RFC3339Nano.
func (s *Server) handleSessionsList(enc *json.Encoder) {
	if s.sessioner == nil {
		_ = enc.Encode(Response{Error: "sessions.list: no sessioner configured"})
		return
	}
	snapshot := s.sessioner.List()
	out := make([]SessionInfo, 0, len(snapshot))
	for _, e := range snapshot {
		out = append(out, SessionInfo{
			ID:         string(e.ID),
			Label:      e.Label,
			State:      e.LifecycleState.String(),
			LastActive: e.LastActiveAt,
			Bootstrap:  e.Bootstrap,
		})
	}
	_ = enc.Encode(Response{SessionsList: &SessionsListPayload{Sessions: out}})
}

// handleSessionsHasID serves a VerbSessionsHasID request: report whether
// a session is currently registered under the given UUID. Pure registry
// read — no claude spawn, no state transition, no context.WithTimeout
// (s.sessions.Lookup is in-memory and bounded by Pool.mu RLock; the
// conn's handshake deadline is sufficient).
//
// Empty ID is rejected at the boundary (a missing-input condition, not
// an "absent" one) for symmetry with handleSessionsRm /
// handleSessionsRename. Malformed (non-UUIDv4) ID is also rejected at
// the boundary — Pool.Lookup would return ErrSessionNotFound for any
// non-canonical string regardless, but failing fast at the seam
// distinguishes "client typed garbage" from "well-formed UUID that
// happens to be absent". Per AC.
//
// Lookup returns (*Session, ErrSessionNotFound) for unknown ids,
// (*Session, nil) for known. The Session is discarded — the handler
// only cares whether the entry exists. Any non-ErrSessionNotFound
// error (theoretically unreachable today; defensive against future
// Pool.Lookup error growth) is surfaced verbatim.
func (s *Server) handleSessionsHasID(enc *json.Encoder, payload *SessionsPayload) {
	if payload == nil || payload.ID == "" {
		_ = enc.Encode(Response{Error: "sessions.has-id: missing id"})
		return
	}
	if !sessions.ValidID(payload.ID) {
		_ = enc.Encode(Response{Error: "sessions.has-id: invalid uuid"})
		return
	}
	_, err := s.sessions.Lookup(sessions.SessionID(payload.ID))
	if err != nil && !errors.Is(err, sessions.ErrSessionNotFound) {
		_ = enc.Encode(Response{Error: fmt.Sprintf("sessions.has-id: %v", err)})
		return
	}
	has := err == nil
	_ = enc.Encode(Response{SessionsHasID: &SessionsHasIDResult{Has: has}})
}

// handleRekey serves a VerbRekey request: read the installed Rekeyer
// under s.mu, validate the payload, call Rekeyer.Rekey, map the typed
// ErrConnNotFound sentinel to the wire ErrorCode, and ack.
//
// Mirrors handleSessionsRename's no-ctx-timeout shape: slice B's
// Rekeyer enqueues the trigger and returns once the v2 manager has
// accepted it. The actual Noise handshake runs asynchronously on the
// conn's state machine — a handler-level WithTimeout would lie about
// cancellability. context.Background() is passed into Rekeyer.Rekey so
// slice B can propagate cancellation through its own enqueue logic if
// it wants; this slice does not impose a deadline.
//
// Guards (nil rekeyer, nil/empty payload) fire BEFORE the Rekeyer call
// so a misconfigured daemon or malformed client cannot trigger work or
// reach a nil dereference. The s.mu critical section loads the Rekeyer
// pointer once and releases the lock before the (potentially blocking)
// call into the installed Rekeyer.
func (s *Server) handleRekey(enc *json.Encoder, payload *RekeyPayload) {
	s.mu.Lock()
	r := s.rekeyer
	s.mu.Unlock()
	if r == nil {
		_ = enc.Encode(Response{Error: "rekey: no rekeyer configured"})
		return
	}
	if payload == nil || payload.ConnID == "" {
		_ = enc.Encode(Response{Error: "rekey: missing connID"})
		return
	}
	if err := r.Rekey(context.Background(), payload.ConnID); err != nil {
		resp := Response{Error: err.Error()}
		if errors.Is(err, ErrConnNotFound) {
			resp.ErrorCode = ErrCodeConnNotFound
		}
		_ = enc.Encode(resp)
		return
	}
	_ = enc.Encode(Response{OK: true})
}

// Fixed deny reasons for the daemon-originated fail-closed paths. Constants,
// never host-derived content, so a deny leaks nothing about the request (the
// timeout path's reason comes from permbridge itself). claude's deny path
// does not hang the turn, so a deny is always the safe default.
const (
	reasonApproveDisconnect = "approval caller disconnected"
	reasonApproveShutdown   = "daemon shutting down"
	reasonApproveDuplicate  = "duplicate approval request"
)

// handleAttachFile serves a VerbAttachFile request: hand the calling session's
// id and the claude-named path to the installed file attacher, and answer with
// the minted attachment id or the attacher's refusal reason.
//
// Guard order mirrors handleApprove's — nil dependency BEFORE payload
// validation — so a daemon that never wired the attacher answers the same way
// whatever the request looks like, and a caller cannot use the shape of the
// refusal to probe which dependencies a daemon has installed.
//
// Fail-closed in the literal sense the AC asks for: every branch below encodes
// exactly one Response and returns, so a caller never hangs waiting for an
// answer that was never written, and nothing dereferences payload before the
// nil check. The attacher itself is total — it returns an id or an error,
// never both empty — but a defensive empty-id branch is deliberately NOT
// added: it would be the one branch no test could reach, and an attacher that
// broke that contract should surface as a visibly empty id rather than as a
// message this handler invented.
//
// The empty-SessionID refusal is not merely input hygiene. It is the wire half
// of a two-sided guard whose other half lives in the attacher, because the
// seam an empty id would otherwise reach — sessions.Pool.Lookup("") — resolves
// to the BOOTSTRAP session with a nil error, and a conversation scan keyed on
// CurrentSessionID would match an UNBOUND conversation. Either would file
// claude's bytes under a conversation that never asked. Two guards rather than
// one, deliberately: this one is a wire-shape check, and the attacher's is
// what protects that seam from any future caller that does not come through
// here.
//
// The conn write deadline is extended past the handshake window before the
// call, exactly as handleSessionsNew does: the attacher reads a file off disk
// and fsyncs another, which can outrun the 5s handshake bound on a loaded
// system or a large file. sessionOpTimeout is not a budget on the attacher —
// nothing here cancels it — only a backstop on a genuinely stuck response
// write.
//
// The attacher's error text reaches Response.Error verbatim, which is only
// safe because SetFileAttacher's contract obliges every reason to be static.
// Nothing is logged on any path: a refusal's reason goes to the caller, which
// is the only party that needs it, and the success line belongs to the
// attacher, which is where the ids that are safe to log are minted.
func (s *Server) handleAttachFile(conn net.Conn, enc *json.Encoder, payload *AttachFilePayload) {
	s.mu.Lock()
	attach := s.fileAttacher
	s.mu.Unlock()

	if attach == nil {
		_ = enc.Encode(Response{Error: "attachment.file: no file attacher configured"})
		return
	}
	if payload == nil || payload.SessionID == "" {
		_ = enc.Encode(Response{Error: "attachment.file: missing sessionID"})
		return
	}
	if payload.Path == "" {
		_ = enc.Encode(Response{Error: "attachment.file: missing path"})
		return
	}

	// Best-effort, like handleSessionsNew's: a SetDeadline error on a broken
	// conn surfaces on the Encode below rather than needing its own branch.
	_ = conn.SetDeadline(time.Now().Add(sessionOpTimeout + sessionOpConnGrace))

	id, err := attach(payload.SessionID, payload.Path)
	if err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("attachment.file: %v", err)})
		return
	}
	_ = enc.Encode(Response{AttachFile: &AttachFileResult{AttachmentID: id}})
}

// handleChannelNew serves a VerbChannelNew request: hand the caller's directory
// and chosen name to the installed channel creator, and answer with the minted
// conversation id or the creator's refusal reason.
//
// Guard order mirrors handleAttachFile's — nil dependency BEFORE payload
// validation — so a daemon that never wired the creator answers the same way
// whatever the request looks like, and a caller cannot use the shape of the
// refusal to probe which dependencies a daemon has installed.
//
// Fail-closed in the literal sense: every branch encodes exactly one Response
// and returns, so a caller never hangs waiting for an answer that was never
// written, and nothing dereferences payload before the nil check.
//
// The empty-Cwd refusal is the wire half of a two-sided guard whose other half
// lives in the creator, for the reason SetChannelCreator records: the validator
// an implementation reuses reads the empty string as "use the shared trusted
// workdir" and returns success WITHOUT confining or trust-marking anything, so
// an empty cwd that got this far would produce a conversation rooted nowhere in
// particular. Two guards rather than one, deliberately: this one is a wire-shape
// check, and the creator's is what protects that seam from any future caller
// that does not come through here. Same shape, and the same reasoning, as
// handleAttachFile's empty-SessionID pair.
//
// This handler does NO path handling. The cwd reaches the creator exactly as it
// arrived — not cleaned, not made absolute, not resolved — which is what keeps
// confinement in one named place rather than split across two packages.
//
// The conn write deadline is extended past the handshake window before the call,
// exactly as handleSessionsNew does: the creator mints a session and fsyncs two
// registries, which can outrun the 5s handshake bound on a loaded system.
// sessionOpTimeout is not a budget on the creator — nothing here cancels it —
// only a backstop on a genuinely stuck response write.
//
// The creator's error text reaches Response.Error verbatim, which is only safe
// because SetChannelCreator's contract obliges every reason to be static. This
// handler adds the verb prefix and nothing else; in particular it never folds
// the request's cwd or name back into the refusal, which is what AC#2's "echoes
// neither the requested nor the resolved path" comes down to on this side.
func (s *Server) handleChannelNew(conn net.Conn, enc *json.Encoder, payload *ChannelPayload) {
	s.mu.Lock()
	create := s.channelCreator
	s.mu.Unlock()

	if create == nil {
		_ = enc.Encode(Response{Error: "channel.new: no channel creator configured"})
		return
	}
	if payload == nil || payload.Cwd == "" {
		_ = enc.Encode(Response{Error: "channel.new: missing cwd"})
		return
	}

	// Best-effort, like handleSessionsNew's: a SetDeadline error on a broken
	// conn surfaces on the Encode below rather than needing its own branch.
	_ = conn.SetDeadline(time.Now().Add(sessionOpTimeout + sessionOpConnGrace))

	id, err := create(payload.Cwd, payload.Name)
	if err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("channel.new: %v", err)})
		return
	}
	_ = enc.Encode(Response{ChannelNew: &ChannelNewResult{ConversationID: id}})
}

// handleChannelPost serves a VerbChannelPost request: hand the caller's label
// and message to the installed poster, and answer OK or the poster's refusal.
//
// Guard order mirrors handleChannelNew's — nil dependency BEFORE payload
// validation — so a daemon that never wired the poster answers the same way
// whatever the request looks like, and a caller cannot use the shape of the
// refusal to probe which dependencies a daemon has installed.
//
// Fail-closed in the literal sense: every branch encodes exactly one Response
// and returns, so a caller never hangs waiting for an answer that was never
// written, and nothing dereferences payload before the nil check.
//
// The payload checks are wire-SHAPE checks and nothing more. An empty name and
// an empty body are refused here because ChannelPostPayload declares both
// invalid rather than defaulted — an empty label would otherwise reach the
// resolver as a request to match the empty name, which nothing sensible carries,
// and an empty body would record a blank entry a cron could not tell from a
// delivered one. The cap is enforced here because the poster cannot assume the
// pyry CLI is what dialled: the socket is local and 0600, so the peer is the
// operator, but any process running as the operator can send this frame. The
// CLI checks the same constant before dialling, which is a bound on its own read
// rather than a second copy of this rule.
//
// This handler does NO name handling: the label reaches the poster exactly as it
// arrived, which keeps the matching rule in one place.
//
// The conn write deadline is extended past the handshake window before the call,
// exactly as handleChannelNew does: the create-on-miss path mints a session and
// fsyncs two registries, which can outrun the 5s handshake bound on a loaded
// system. sessionOpTimeout is not a budget on the poster — nothing here cancels
// it — only a backstop on a genuinely stuck response write.
//
// The poster's error text reaches Response.Error verbatim, which is only safe
// because SetChannelPoster's contract obliges every reason to be static or
// daemon-derived. This handler adds the verb prefix and nothing else; in
// particular it never folds the request's label or body back into the refusal.
func (s *Server) handleChannelPost(conn net.Conn, enc *json.Encoder, payload *ChannelPostPayload) {
	s.mu.Lock()
	post := s.channelPoster
	s.mu.Unlock()

	if post == nil {
		_ = enc.Encode(Response{Error: "channel.post: no channel poster configured"})
		return
	}
	if payload == nil || payload.Name == "" {
		_ = enc.Encode(Response{Error: "channel.post: missing name"})
		return
	}
	if payload.Text == "" {
		_ = enc.Encode(Response{Error: "channel.post: empty message"})
		return
	}
	// The refusal names no number: MaxChannelPostBytes is the one home of the
	// bound, and a message restating it is a second copy that goes stale the
	// first time the constant moves.
	if len(payload.Text) > MaxChannelPostBytes {
		_ = enc.Encode(Response{Error: "channel.post: message too large"})
		return
	}

	// Best-effort, like handleChannelNew's: a SetDeadline error on a broken conn
	// surfaces on the Encode below rather than needing its own branch.
	_ = conn.SetDeadline(time.Now().Add(sessionOpTimeout + sessionOpConnGrace))

	if err := post(payload.Name, payload.Text); err != nil {
		_ = enc.Encode(Response{Error: fmt.Sprintf("channel.post: %v", err)})
		return
	}
	// OK rather than a result body, handleSessionsRename's shape: the caller
	// prints nothing and needs nothing back, and a verb that answers with more
	// than its caller needs is a verb whose wire has to be kept compatible for
	// no reason.
	_ = enc.Encode(Response{OK: true})
}

// handlePairingMint invokes the optional provider exactly once for a valid
// request. It releases Server.mu before the call so provider-owned registry
// locking cannot block other control operations behind the server lock.
//
// Only a nil-error pairing reaches the response. Every failure is projected to
// fixed content-free text, and this handler emits no logs. The request-read
// deadline set in handle stays finite, and a fresh deadline bounds the write.
func (s *Server) handlePairingMint(conn net.Conn, enc *json.Encoder, payload *PairingPayload) {
	s.mu.Lock()
	provider := s.pairingProvider
	s.mu.Unlock()

	_ = conn.SetWriteDeadline(time.Now().Add(DialTimeout))

	if provider == nil {
		_ = enc.Encode(Response{Error: "pairing.mint: provider not configured"})
		return
	}
	if payload == nil {
		_ = enc.Encode(Response{Error: "pairing.mint: operation failed"})
		return
	}

	pairing, err := provider(payload.DeviceLabel, payload.AllowRemotePermissions)
	if err != nil {
		_ = enc.Encode(Response{Error: "pairing.mint: operation failed"})
		return
	}
	_ = enc.Encode(Response{Pairing: &PairingResult{Pairing: pairing}})
}

// handleApprove serves a VerbMCPApprove request: register the forwarded
// approval with the pending-approval registry, block until a verdict is
// available (resolver, timeout, disconnect, or shutdown), and return it.
//
// Guard order mirrors handleRekey — nil registry, then malformed payload,
// both before any registry work. Every terminal path other than an explicit
// resolver Allow yields deny (see the package's fail-closed contract): the
// untrusted socket peer can only submit-and-await, never inject an allow.
//
// The conn is owned but NOT handed off: handle's deferred conn.Close runs on
// return and reaps the watcher's parked reader. Input/ToolName are never
// logged — only the correlation key (tool_use_id) and the behavior.
func (s *Server) handleApprove(conn net.Conn, enc *json.Encoder, payload *ApprovePayload) {
	s.mu.Lock()
	reg := s.approvals
	timeout := s.approvalTimeout
	surface := s.approvalSurfacer
	s.mu.Unlock()

	if reg == nil {
		_ = enc.Encode(Response{Error: "mcp.approve: no approval registry configured"})
		return
	}
	if payload == nil || payload.ToolUseID == "" {
		_ = enc.Encode(Response{Error: "mcp.approve: missing tool_use_id"})
		return
	}

	req := permbridge.Request{
		ToolName:  payload.ToolName,
		Input:     payload.Input,
		ToolUseID: payload.ToolUseID,
	}
	pending, err := reg.Register(payload.ToolUseID, req, timeout)
	if err != nil {
		// Duplicate live id (empty id already rejected by the guard above).
		// Fail-closed with a fixed message rather than a wire error. The surfacer
		// is NOT invoked on this early return, so no client modal is raised for a
		// request that never parked (and nothing to retire).
		_ = enc.Encode(Response{Approve: denyResult(reasonApproveDuplicate)})
		return
	}

	// Clear the handshake deadline: the conn deliberately carries no bound of
	// its own for the length of a human decision (mirrors handleAttach). Since
	// #1932 permbridge's registry-owned timer bounds the wait only once its
	// liveness report reads unanswerable, so a conn deadline here would be the
	// binding one — and #1929 removed the client's own per-call bound for the
	// same reason, since a client close reads to watchApproveConn below as a
	// lost caller and terminates the approval.
	_ = conn.SetDeadline(time.Time{})

	// Surface the parked approval to interactive clients as a modal_shown — or, for
	// claude's clarifying-question tool call, as a question_shown (#1973) — if a
	// surfacer is wired (#1080). Which one is the surfacer's decision, taken from
	// the request this package hands it; nothing here branches on the tool. The
	// deferred retire is the guaranteed cleanup + client-dismissal backstop for
	// either family: it fires on the post-Await return, covering resolver-answer /
	// timeout / disconnect / shutdown uniformly. A nil surfacer
	// (v1/foreground/relay disabled) makes this a no-op — the completer still
	// resolves, just without a phone prompt.
	retire := func() {}
	if surface != nil {
		retire = surface(req)
	}
	defer retire()

	// Watch for caller disconnect / daemon shutdown while we block. stop is
	// closed once the verdict lands so the watcher does not resolve a
	// verdict of its own on the normal path.
	stop := make(chan struct{})
	go s.watchApproveConn(conn, reg, payload.ToolUseID, stop)

	verdict := pending.Await() // returns within one window of the first unanswerable reading
	close(stop)

	_ = enc.Encode(Response{Approve: verdictToResult(verdict)})
	s.log.Info("control: approval resolved",
		"tool_use_id", payload.ToolUseID, "behavior", verdict.Behavior)
}

// watchApproveConn maps the two cancellation sources permbridge's ctx-free
// Await cannot observe — caller disconnect and daemon shutdown — into a
// fail-closed deny Resolve, so a lost caller or a shutting-down daemon does
// not leave a pending entry parked. That matters MORE since #1932 made the
// elapsed-time bound conditional: while somebody can still answer, the registry
// re-arms its window instead of denying, so these two are the only bounds on an
// approval whose caller vanished. Started before the Await; the handler closes
// stop after Await returns.
//
// The inner reader blocks on conn.Read (the client sends nothing after its
// request) until EOF/error on disconnect or handle's deferred conn.Close on
// the normal path; either way it is reaped. permbridge's delete-under-lock
// one-shot makes a late Resolve here (racing the timer or a real resolver) a
// harmless no-op.
func (s *Server) watchApproveConn(conn net.Conn, reg *permbridge.Registry, id string, stop <-chan struct{}) {
	readCh := make(chan struct{})
	go func() {
		var one [1]byte
		_, _ = conn.Read(one[:])
		close(readCh)
	}()

	select {
	case <-stop:
		// Verdict already landed; the handler is encoding the response.
	case <-s.closedCh:
		reg.Resolve(id, permbridge.Deny(reasonApproveShutdown))
	case <-readCh:
		reg.Resolve(id, permbridge.Deny(reasonApproveDisconnect))
	}
}

// verdictToResult maps a permbridge.Verdict to the wire ApproveResult — a
// straight field copy across the two byte-identical shapes.
func verdictToResult(v permbridge.Verdict) *ApproveResult {
	return &ApproveResult{
		Behavior:     v.Behavior,
		UpdatedInput: v.UpdatedInput,
		Message:      v.Message,
	}
}

// denyResult builds a deny ApproveResult for the handler's fail-closed
// branches, reusing permbridge.Deny so the wire shape stays in lockstep.
func denyResult(msg string) *ApproveResult {
	return verdictToResult(permbridge.Deny(msg))
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

// buildStatus converts a runner State snapshot into the wire format.
func buildStatus(st sessions.State) *StatusPayload {
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
