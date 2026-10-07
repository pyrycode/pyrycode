package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"golang.org/x/sync/errgroup"
)

// ErrSessionNotFound is returned by Pool.Lookup for a non-empty unknown id.
var ErrSessionNotFound = errors.New("sessions: session not found")

// ErrDormantPostureUnsupported is returned by Pool.UpdateDormantSettings for an
// update naming YOLO or PermissionMode. A dormant entry's persisted posture is
// read by nothing — a revive builds the default structurally from model and
// effort (#1487) — so storing one would report success for a change the next
// read contradicts. See Pool.UpdateDormantSettings for the whole decision.
//
// DISTINCT FROM ErrSessionNotFound on purpose, even though cmd/pyry's
// settingsUpdaterAdapter maps both to the same wire code. That adapter composes
// the live write and the dormant one, and reads the live write's
// ErrSessionNotFound as "try the other half"; one shared sentinel would mean
// that and "refuse" one line apart. It would also have this method reporting
// "not found" about an id it did find. Matchable via errors.Is.
var ErrDormantPostureUnsupported = errors.New("sessions: dormant session cannot store a permission posture")

// ErrCannotRemoveBootstrap is returned by Pool.Remove for the bootstrap
// session. The bootstrap is a per-process invariant, not an operator
// resource — removing it would leave the pool in a state Pool.Lookup("")
// can't satisfy. Matchable via errors.Is.
var ErrCannotRemoveBootstrap = errors.New("sessions: cannot remove bootstrap session")

// ErrPoolNotRunning is returned by supervise when called before Pool.Run has
// wired the supervisor handle, or after Run has cleared it. Callers must
// invoke supervise only while Pool.Run is active.
var ErrPoolNotRunning = errors.New("sessions: pool not running")

// ErrAmbiguousSessionID is returned by Pool.ResolveID when a non-empty,
// non-full-UUID arg matches the prefix of two or more sessions. The wrapped
// error's Error() lists each match as `<uuid> (<label>)` on its own line so
// a CLI consumer can print it verbatim. Matchable via errors.Is.
var ErrAmbiguousSessionID = errors.New("sessions: ambiguous session id")

// Config is what cmd/pyry hands to sessions.New.
type Config struct {
	Bootstrap SessionConfig
	Logger    *slog.Logger

	// BootstrapEvicted, when true, constructs the bootstrap session in
	// stateEvicted instead of the default stateActive, so Pool.Run parks it in
	// runEvicted and it spawns no claude. The zero value (false) is
	// byte-identical to today's eager-bootstrap behaviour.
	//
	// This exists for embedded hosts that map each caller session onto its own
	// Pool.Create'd claude and must not run a second, unaddressed bootstrap
	// claude (pyry acp / divergence 6, #761). It is sound only when the
	// bootstrap is never Activated and its evicted state is never persisted: the
	// pool keeps it as a dormant Default()/Lookup("") placeholder until Run's
	// ctx cancels. Do NOT pair it with a RegistryPath that would persist
	// "evicted" for the bootstrap and later wake it with no attach client to
	// drive Activate (the #202 hang the New warm-start branch guards against).
	BootstrapEvicted bool

	// RegistryPath is the on-disk path of the sessions.json registry. Empty
	// disables persistence (test-only). In production this is always
	// ~/.pyry/<sanitized-name>/sessions.json — see cmd/pyry resolveRegistryPath.
	RegistryPath string

	// ClaudeSessionsDir is the directory containing claude's <uuid>.jsonl
	// files for this WorkDir. Empty disables startup reconciliation (test
	// default, and the production fallback when $HOME is unresolvable).
	// Production callers in cmd/pyry resolve this via
	// DefaultClaudeSessionsDir from cfg.Bootstrap.WorkDir.
	ClaudeSessionsDir string

	// ConversationsRegistry, when non-nil, enables the periodic auto-archive
	// sweep goroutine inside Pool.Run. The registry must already be Loaded
	// by the caller (cmd/pyry handles the conversations.Load call so the
	// sessions package stays path-agnostic about conversations.json — see
	// docs/knowledge/features/conversations-registry.md).
	//
	// nil disables the sweep goroutine entirely (test default; existing
	// pool tests construct Config without this field and remain unchanged).
	ConversationsRegistry *conversations.Registry

	// ConversationsRegistryPath is the on-disk path of conversations.json
	// — passed to RunSweepLoop's Save call after each non-empty tick. In
	// production this is always ~/.pyry/<sanitized-name>/conversations.json
	// — see cmd/pyry resolveConversationsRegistryPath.
	//
	// Required when ConversationsRegistry is non-nil; ignored when nil.
	ConversationsRegistryPath string

	// ReadFolders are the folders the file reader serves besides a
	// conversation's workspace (#2710), as cmd/pyry's resolveReadFolders
	// returned them: only the entries that resolved, never a skipped one. Every
	// appended system prompt names them in one sentence after systemPromptText
	// (#2711). Nil, the zero value, composes every prompt byte for byte as before.
	ReadFolders []string

	// DefaultModel answers the model claude starts on, for a child spawned in
	// workDir, when its argv names none: Claude Code's own model setting. A
	// session with no model of its own would otherwise run whatever that setting
	// pins. When the answer is a pinned Claude id, the argv names its family
	// instead (composeSpawnArgs), so such a session still follows the latest model
	// of a family. cmd/pyry wires ClaudeSettingsModel. Nil, the zero value,
	// resolves nothing and composes every argv as before; every test pool leaves
	// it nil so the host's own settings file cannot reach a test's argv.
	DefaultModel func(workDir string) string

	// SweepInterval, when > 0, overrides the conversations sweep tick
	// interval Pool.Run passes to conversations.RunSweepLoop. Zero (the
	// default) means use conversations.SweepInterval (one hour). Production
	// callers leave this zero; the cmd/pyry -pyry-conv-sweep-interval flag
	// (intended for e2e tests) plumbs a small duration here so the sweep
	// loop can be exercised without waiting an hour.
	//
	// Ignored when ConversationsRegistry is nil (the sweep goroutine
	// doesn't run at all in that case).
	SweepInterval time.Duration

	// IdleTimeout is the default per-session idle eviction window. A
	// SessionConfig with IdleTimeout==0 inherits this value at New().
	// Zero here means "never evict". cmd/pyry passes the -pyry-idle-timeout
	// flag, which also defaults to 0, so idle eviction is off unless the
	// operator sets the flag.
	IdleTimeout time.Duration

	// TurnBusy, when non-nil, reports whether the given session has a turn
	// open. An idle-timer fire that finds its session busy re-arms instead of
	// evicting, so a long turn is not killed mid-stream (#1486). It is called
	// on the session's lifecycle goroutine with no pool or session lock held,
	// and must not block. nil means no signal: the timer evicts on fire. One
	// bool per session is all that crosses here — cmd/pyry resolves the
	// session to its conversation on its own side of the seam.
	TurnBusy func(SessionID) bool

	// OnRunnerStopped, when non-nil, observes each completed Runner.Run call,
	// after the producer returns and before eviction completion or reactivation.
	// Called without pool/session locks; it must not block. Unlike the transition
	// observer's early eviction signal, this is a producer shutdown boundary.
	OnRunnerStopped func(SessionID)

	// ActiveCap is the maximum number of concurrently active claude
	// processes this Pool will run. Zero (the unset default) means
	// uncapped — preserves Phase 1.2c-A's idle-only behaviour
	// byte-for-byte (no LRU bookkeeping cost on the hot path).
	//
	// When set (>= 1), an Activate that would push the count past the
	// cap first evicts the least-recently-active currently-active peer
	// via Session.Evict. The evicted session transitions to `evicted`
	// exactly as it would from an idle timeout — same on-disk state
	// change, same registry write.
	//
	// Idle eviction (1.2c-A) and cap eviction compose: same Evict
	// mechanism, different victim picker. The idle timer runs per
	// session and picks itself; the cap path runs at Pool.Activate's
	// entry and picks the LRU peer.
	//
	// Values <= 0 are treated as unset.
	ActiveCap int

	// RunnerFactory is the injection seam for the supervised child, invoked at
	// every construction site (bootstrap in New and per-session in
	// buildSession). It is required: New returns an error on a nil factory.
	// Before #1348 nil selected the terminal supervisor, which no longer exists;
	// the daemon passes the stream-json runner's factory.
	RunnerFactory RunnerFactory
}

// SessionConfig is the per-session invocation shape. Phase 1.0 uses it only
// for the bootstrap entry; Phase 1.1's `pyry sessions new` populates it for
// each new session.
//
// Phase 1.0 honours ResumeLast (which maps to supervisor.Config.ResumeLast,
// i.e. --continue on restart). The locked-design `claude --session-id <uuid>`
// invocation lands in Phase 1.1+ and is deliberately NOT introduced here
// (parent spec open question #2: "wait").
type SessionConfig struct {
	ClaudeBin  string
	WorkDir    string
	ResumeLast bool
	ClaudeArgs []string

	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffReset   time.Duration

	// IdleTimeout, when positive, causes the session's claude process to
	// exit after the configured period, deferred while Config.TurnBusy
	// reports a turn open (re-checked once per period). The
	// JSONL on disk is preserved; a subsequent Activate spawns a fresh
	// claude that reads the prior conversation. Zero inherits
	// Config.IdleTimeout; if both are zero, eviction is disabled.
	IdleTimeout time.Duration
}

// Pool owns the set of sessions managed by one pyry process. Phase 1.0
// constructs exactly one entry — the bootstrap session — at New().
type Pool struct {
	// Separate from lifecycle locks: freshness persistence never holds mu.
	handoffMu    sync.Mutex
	handoffStale map[conversations.ConversationID]bool

	// Separate from mu: construction already holds mu when it reads instructions.
	instructionsMu     sync.RWMutex
	daemonInstructions string

	mu                sync.RWMutex
	sessions          map[SessionID]*Session
	bootstrap         SessionID
	log               *slog.Logger
	registryPath      string
	claudeSessionsDir string

	// dormant holds the entries loadRegistry returned that this pool did not
	// materialise, keyed by id — precisely, a persisted entry with no live
	// *Session behind it. Guarded by p.mu, the same lock as sessions, on the same
	// discipline: mutated under the write lock, read under either.
	//
	// It exists because saveLocked rewrites the whole file from p.sessions, and
	// New materialises only the bootstrap: without this the first save after a
	// daemon restart erased every other session's record, model and effort
	// included, so the entry did not survive the restart it was written to
	// survive (#2448). saveLocked writes these back beside the live sessions.
	//
	// The KEY SET is populated only in New and only ever shrinks thereafter —
	// materialise retires the entry it takes over and Remove drops the one it
	// deletes — which is what keeps the "no live *Session" half of the meaning
	// true, and what Pool.revivedSettings and Pool.DormantSettingsFor (#2449) rest
	// on. Read it as a statement about KEYS, not about values: since #2463 an
	// entry's Model and Effort are mutable in place, via
	// Pool.UpdateDormantSettings, which replaces a value under an existing key and
	// so adds no key and removes none. Nothing else about an entry is ever
	// rewritten here, the persisted posture included.
	//
	// A nil map (the bare &Pool{} literals in this package's tests) is safe for
	// every operation outside New: read, delete and range all tolerate it, and
	// both writes — materialise's rollback restore and the merge above — are
	// reachable only after a read hit, which a nil map cannot produce.
	dormant map[SessionID]registryEntry

	// systemPromptPath is the absolute path to the daemon's appended
	// system-prompt file (#2093), a member of every session's spawnBase as
	// "--append-system-prompt-file <path>". ONE file serves the whole daemon —
	// the text is a constant, so there is nothing per-session about it — which is
	// why it is here rather than on Session beside settingsPath, and why
	// Pool.Remove must not remove it. Written by Pool.New and removed by Pool.Run
	// on shutdown. Read-only after New, so no lock; buildSession is its only
	// other reader.
	systemPromptPath string

	// clientIdentity is the resolver naming the clients attached when a session's
	// appended prompt is composed (#2148), installed by SetClientIdentityResolver
	// and read only by attachedClients. A nil-or-unset pointer means no relay is
	// wired — foreground mode, v1, and almost every test here — and yields no
	// client names rather than an error.
	//
	// Atomic rather than a plain field, unlike transitionObserver above it: the
	// install runs inside startRelayV2 with the relay manager's Run goroutine
	// already started, and Run creates the per-conn workers that reach
	// Pool.Activate. SetClientIdentityResolver's doc has the full argument.
	clientIdentity atomic.Pointer[ClientIdentityResolver]

	// convReg and convRegistryPath mirror Config.ConversationsRegistry /
	// .ConversationsRegistryPath. Read-only after New — set once,
	// consulted only by Pool.Run to decide whether to register the sweep
	// goroutine. No lock needed.
	convReg          *conversations.Registry
	convRegistryPath string

	// readFolders mirrors Config.ReadFolders. Read-only after New, so no lock;
	// every compose passes it through daemonPromptText (#2711).
	readFolders []string

	// defaultModel mirrors Config.DefaultModel. Read-only after New, so no lock;
	// called only off p.mu, by buildSessionAs.
	defaultModel func(workDir string) string

	// convSweepInterval is the resolved interval Pool.Run passes to
	// conversations.RunSweepLoop. Set in New from cfg.SweepInterval, with
	// conversations.SweepInterval as the zero-value fallback. Read-only
	// after New — set once, consulted only by Pool.Run. No lock needed.
	//
	// In-package tests may overwrite this field directly after construction
	// (mirrors the existing convReg / convRegistryPath pattern in
	// pool_conv_sweep_test.go).
	convSweepInterval time.Duration

	// activeCap mirrors Config.ActiveCap. Read-only after New, so no lock
	// is needed to read it. Zero means uncapped — see Config.ActiveCap.
	activeCap int

	// capMu serializes the Pool.Activate cap-check + victim-eviction +
	// new-spawn sequence so two concurrent Activates can't both observe
	// active < cap and both proceed. Held only on the cap path
	// (activeCap > 0); the uncapped path is byte-identical to 1.2c-A.
	//
	// Lock order: capMu is the outermost lock; while it is held the
	// caller may go on to take Pool.mu (read or write) and per-session
	// lcMu, in that order. capMu is never re-acquired by callees.
	capMu sync.Mutex

	// runGroup and runCtx are set when Pool.Run begins and cleared before
	// Run returns. supervise(sess) reads them under p.mu (RLock) and calls
	// runGroup.Go off-lock so future Pool.Create can hand a freshly-built
	// Session into the same errgroup that supervises the bootstrap. nil
	// when Run is not active. Read together so a caller never sees a
	// half-initialised handle.
	runGroup *errgroup.Group
	runCtx   context.Context

	// readyCh is closed once by Run — under readyOnce — after runGroup/runCtx
	// are wired and the bootstrap is supervised, i.e. once Pool.Create's
	// supervise call will succeed instead of returning ErrPoolNotRunning.
	// Created open in New (never ready until Run); read via Ready(). readyOnce
	// guards the theoretical double-Run. Read lock-free: set once in New and
	// never reassigned.
	readyCh   chan struct{}
	readyOnce sync.Once

	// sessionTpl is the per-session template captured from cfg.Bootstrap
	// at New(). Pool.Create copies this, overrides ResumeLast/ClaudeArgs,
	// and (in service mode, Bridge != nil) mints a fresh Bridge so each
	// new session gets its own I/O channel. Read-only after New — no lock
	// needed.
	sessionTpl SessionConfig

	// idleTimeoutDefault mirrors Config.IdleTimeout. Pool.Create uses it as
	// the same fallback New() applies to the bootstrap when the per-session
	// IdleTimeout is zero. Read-only after New.
	idleTimeoutDefault time.Duration

	// turnBusy mirrors Config.TurnBusy; New hands it to the bootstrap and
	// buildSession to every other session. Read-only after New.
	turnBusy func(SessionID) bool

	// newRunner is cfg.RunnerFactory, never nil because New refuses a nil
	// factory. Set once in New and
	// read by buildSession thereafter — read-only post-construction, same
	// lifetime and lock-free discipline as log / convReg. See Config.RunnerFactory.
	newRunner RunnerFactory

	// transitionObserver is the optional, injectable signal surfaced on
	// /clear rotations and evictions. Set once via SetTransitionObserver
	// BEFORE Pool.Run; read-only thereafter, so the goroutines Run
	// transitively spawns — the per-session lifecycle goroutines, and the
	// runners they in turn start — read it lock-free via Run's
	// goroutine-start happens-before. nil disables it. See transition.go.
	transitionObserver TransitionObserver
	onRunnerStopped    func(SessionID) // construction-bound, read-only after New
	// switchPublisher is installed before Run and may wait for daemon-owned
	// publication. Ordinary lifecycle observers remain nonblocking.
	switchPublisher func(SessionTransition)
}

// SnapshotEntry is one (id, pid) pair captured by Pool.Snapshot. The primitive
// field types are what let the retired rotation watcher consume snapshots without
// importing internal/sessions; #2137 removed that consumer. Both the type and
// Pool.Snapshot are kept for the same reason RotateID is — they are exported, and
// removing them is a separate deliberate call rather than a side effect of
// retiring the watcher.
type SnapshotEntry struct {
	ID  SessionID
	PID int
}

// SessionInfo is one session's operator-visible metadata, returned by
// Pool.List. Field types are deep-copy-safe: SessionID and string are values,
// time.Time is a value, and lifecycleState is a uint8 enum. Mutating a
// SessionInfo does not affect Pool state or the on-disk registry.
type SessionInfo struct {
	ID SessionID
	// Label is the operator-set label, except that the bootstrap entry's
	// empty on-disk label is substituted with the synthetic string
	// "bootstrap" here. The on-disk value is unchanged.
	Label          string
	LifecycleState lifecycleState
	LastActiveAt   time.Time
	// Bootstrap is true for the bootstrap entry; lets consumers
	// disambiguate without re-checking IDs.
	Bootstrap bool
}

// List returns a snapshot of every session in the pool — bootstrap and minted
// alike — sorted by LastActiveAt descending (most recent first). Ties break
// on SessionID ascending for deterministic ordering. The returned slice and
// its elements are deep-copied: callers may mutate freely without affecting
// pool or registry state.
//
// The bootstrap entry's Label field is the synthetic string "bootstrap" when
// the on-disk label is empty; the on-disk registry entry is NOT mutated by
// this substitution. Non-empty bootstrap labels (operator-set) pass through
// verbatim.
//
// Read-only: this method does not bump LastActiveAt, transition lifecycle
// state, or persist anything. Safe for concurrent use; takes Pool.mu (read)
// and each Session.lcMu briefly.
//
// Lock order: Pool.mu (RLock) → Session.lcMu (Lock). Same order as
// Pool.saveLocked and Pool.pickLRUVictim — no new lock-order edges.
func (p *Pool) List() []SessionInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]SessionInfo, 0, len(p.sessions))
	for _, s := range p.sessions {
		s.lcMu.Lock()
		state := s.lcState
		lastActive := s.lastActiveAt
		s.lcMu.Unlock()

		label := s.label
		if s.bootstrap && label == "" {
			label = "bootstrap"
		}

		out = append(out, SessionInfo{
			ID:             s.id,
			Label:          label,
			LifecycleState: state,
			LastActiveAt:   lastActive,
			Bootstrap:      s.bootstrap,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastActiveAt.Equal(out[j].LastActiveAt) {
			return out[i].LastActiveAt.After(out[j].LastActiveAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// New constructs a Pool. If a registry exists at cfg.RegistryPath, the
// bootstrap session reuses the persisted UUID and metadata; otherwise a
// fresh UUID is minted and the registry is written before New returns.
// Returns an error if cfg.RunnerFactory is nil, or if the rng, the runner
// factory, registry load, or registry save fails — all are fatal-at-startup
// conditions.
func New(cfg Config) (*Pool, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// A factory is mandatory since #1348, and a nil one is an error here. It used
	// to default to the terminal supervisor when nil, which meant an unconfigured
	// caller silently got the runner that no longer exists. newRunner is threaded
	// through both construction sites (bootstrap below, per-session via
	// Pool.newRunner in buildSession).
	newRunner := cfg.RunnerFactory
	if newRunner == nil {
		return nil, errors.New("sessions: Config.RunnerFactory is required")
	}

	var reg *registryFile
	if cfg.RegistryPath != "" {
		var err error
		reg, err = loadRegistry(cfg.RegistryPath)
		if err != nil {
			return nil, fmt.Errorf("sessions: load registry: %w", err)
		}
	}

	var (
		bootstrapID  SessionID
		label        string
		createdAt    time.Time
		lastActiveAt time.Time
		lcState      lifecycleState  // defaults to stateActive
		settings     SessionSettings // zero on cold start; from disk on warm start
	)
	if entry := pickBootstrap(reg); entry != nil {
		bootstrapID = entry.ID
		label = entry.Label
		createdAt = entry.CreatedAt
		lastActiveAt = entry.LastActiveAt
		// Honour persisted spawn settings across a daemon restart. Unlike
		// lifecycle_state (per-process, ignored below), these are the
		// operator's spawn intent and must survive restart. settingsFromEntry
		// owns the posture's default-tolerant read, so an entry written before
		// #2043 — no permission_mode key at all — materialises as the mode its
		// yolo already implies.
		settings = settingsFromEntry(*entry)
		// Bootstrap-only: ignore persisted lifecycle_state. The bootstrap
		// is the per-process auto-spawn entry; daemon-mode startup
		// contract is "claude is available". Idle eviction within a
		// process is correct (frees claude after the idle window), but a
		// fresh daemon process starts with a fresh active state — there
		// is no carry-over. Persisting "evicted" for the bootstrap and
		// then waking with no attach client to drive Activate would hang
		// the daemon forever (see #202). Non-bootstrap sessions keep
		// their persisted state; lazy respawn on attach is correct there.
		lcState = stateActive
		_ = entry.LifecycleState
	} else {
		id, err := NewID()
		if err != nil {
			return nil, fmt.Errorf("sessions: generate bootstrap id: %w", err)
		}
		bootstrapID = id
		now := time.Now().UTC()
		createdAt, lastActiveAt = now, now
	}

	// Cold start's zero value carries no posture; normalise it here so the
	// bootstrap Session holds the default mode spelled out, exactly as the
	// warm-start branch above does (settingsFromEntry normalises too, so this is
	// idempotent on that path).
	settings = canonicalSettings(settings)

	// Embedded hosts (pyry acp, #761) suppress the eager bootstrap claude so a
	// per-caller Pool.Create is the only interactive claude. Overrides whatever
	// the warm/cold-start branch chose above; sound only because such callers
	// never persist or Activate the bootstrap (see Config.BootstrapEvicted).
	if cfg.BootstrapEvicted {
		lcState = stateEvicted
	}

	// The per-session --settings file pre-approves the project's MCP servers so
	// claude's startup enablement modal never wedges the readiness check (#943).
	// It joins spawnBase (below) rather than claudeSettingsArgs so it survives
	// every recompose (backoff restart, #842 live restart). A write failure is
	// fatal at startup: a daemon that started anyway would silently wedge every
	// turn on the modal — a loud failure is strictly better. Removed when Pool.Run
	// returns (see the defer in Run); the bootstrap is never Remove-d.
	settingsPath, err := writeMCPSettings(cfg.RegistryPath, bootstrapID)
	if err != nil {
		return nil, fmt.Errorf("sessions: write mcp settings: %w", err)
	}
	// Since #1518 the file lives in the daemon data dir, where nothing reaps it,
	// so every error return between here and the successful one has to remove it
	// or the orphan is permanent. A defer keyed on a success flag covers both of
	// today's (the newRunner failure and the saveLocked failure) and covers a
	// future one by construction — this ticket exists because a hand-placed
	// cleanup was forgotten at two sites.
	built := false

	// Per-session prompt files carry operator text (#2150) and must never outlive
	// the daemon that wrote them. Pool.Run's defer is the ordinary removal; this
	// purge is the same claim held after a SIGKILL, which no defer survives. It is
	// sound HERE and nowhere later: no session has been materialised yet, so no
	// live session can own a file in that directory.
	if dir := sessionPromptsDirFor(cfg.RegistryPath); dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			return nil, fmt.Errorf("sessions: purge stale session prompts: %w", err)
		}
	}

	// The bootstrap's appended system-prompt file (#2093) tells claude its replies
	// are rendered by a separate client rather than printed in a terminal. Like
	// the --settings file it joins spawnBase rather than claudeSettingsArgs, so it
	// survives every recompose; UNLIKE it, this one is daemon-scoped — the empty
	// id selects #2093's fixed name — so the path is kept on the Pool and removed
	// once, in Run. The bootstrap is built before any conversation exists and can
	// never become a conversation's bound session, so it carries no per-conversation
	// text; every other session gets its own file in buildSession (#2150). A write
	// failure is fatal at startup for the settings file's reason inverted: a daemon
	// that started anyway would silently spawn every session reasoning about the
	// wrong surface, which is a quiet wrong answer rather than a loud one.
	systemPromptPath, err := writeSystemPrompt(cfg.RegistryPath, "", daemonPromptText(cfg.ReadFolders))
	if err != nil {
		// Ordered after the settings write so this failure is covered by the defer
		// below, which is installed with the two paths already in hand.
		_ = os.Remove(settingsPath)
		return nil, fmt.Errorf("sessions: write system prompt: %w", err)
	}
	defer func() {
		if !built {
			_ = os.Remove(settingsPath)
			_ = os.Remove(systemPromptPath)
		}
	}()

	// base is the settings-free bootstrap argv (template plus the immutable
	// --settings and --append-system-prompt-file pairs). base is stored on the
	// bootstrap Session so a live restart (#842) can recompose full argv from the
	// persisted settings. composeSpawnArgs returns a fresh final argv and leaves
	// the base untouched for operator-bypass provenance.
	base := append(slices.Clone(cfg.Bootstrap.ClaudeArgs), "--settings", settingsPath)
	base = append(base, "--append-system-prompt-file", systemPromptPath)
	// Resolved here, off every lock, because it reads Claude Code's settings files.
	defaultFamily := resolveDefaultFamily(cfg.DefaultModel, cfg.Bootstrap.WorkDir)
	bootstrapArgs := composeSpawnArgs(base, settings, defaultFamily)
	// p is late-bound: the &Pool{} literal below assigns it, and the
	// ResolveSessionID closure only reads it at spawn time (supervisor.Run),
	// long after New returns — identical timing to the pidFn holder above.
	var p *Pool
	supCfg := RunnerConfig{
		ClaudeBin: cfg.Bootstrap.ClaudeBin,
		WorkDir:   cfg.Bootstrap.WorkDir,
		// #1108 seam: the already-minted bootstrap id, exposed as a
		// construction-safe value the stream RunnerFactory reads (#1109). It is
		// construction-fixed and does NOT mirror a /clear rotation.
		//
		// The resume/self-heal/transcript machinery that used to sit here went
		// with the terminal runner in #1348. Every one of those fields was already
		// dropped on the floor by the stream factory, so removing them changes no
		// behaviour — it stops advertising behaviour nothing implements. See
		// RunnerConfig's doc for the two that represent real gaps.
		SessionID: string(bootstrapID),
		// #2135's announced-reset seam. p is the same late-bound pointer the
		// comment above describes: the closure only fires when claude announces a
		// reset on a running child, long after New has returned and assigned it.
		// Both ids are parameters because SessionID above is construction-fixed —
		// see the field's own doc.
		AdoptAnnouncedReset: func(oldID, newID string) error {
			return p.AdoptAnnouncedID(SessionID(oldID), SessionID(newID))
		},
		ClaudeArgs: bootstrapArgs,
		// The bootstrap is the daemon's auto-spawned claude session by definition,
		// so its entry's own harness key is not read (#2593): a hand-edited value
		// there would otherwise refuse daemon startup rather than one session.
		Harness: HarnessClaude,
		// The stored posture the stream runner asserts to every child it spawns
		// (#2064). settings is canonicalSettings'd above, so this is a real mode and
		// never the empty one, whichever of the warm/cold-start branches ran.
		PermissionMode: settings.PermissionMode,
		// Read off BASE, never bootstrapArgs: base is the settings-free half, so the
		// escalation can only be there because the operator's pass-through claude args
		// put it there. bootstrapArgs carries claudeSettingsArgs' unconditional flag
		// and would answer true for every daemon (#2065).
		OperatorBypass: operatorBypass(base),
		Logger:         cfg.Logger,
		BackoffInitial: cfg.Bootstrap.BackoffInitial,
		BackoffMax:     cfg.Bootstrap.BackoffMax,
		BackoffReset:   cfg.Bootstrap.BackoffReset,
	}
	sup, err := newRunner(supCfg)
	if err != nil {
		return nil, fmt.Errorf("sessions: bootstrap runner: %w", err)
	}
	idleTimeout := cfg.Bootstrap.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = cfg.IdleTimeout
	}
	sweepInterval := cfg.SweepInterval
	if sweepInterval <= 0 {
		sweepInterval = conversations.SweepInterval
	}
	sess := &Session{
		id:           bootstrapID,
		sup:          sup,
		log:          cfg.Logger,
		label:        label,
		createdAt:    createdAt,
		lastActiveAt: lastActiveAt,
		bootstrap:    true,
		harness:      HarnessClaude,
		settings:     settings,
		spawnBase:    base,
		settingsPath: settingsPath,
		idleTimeout:  idleTimeout,
		turnBusy:     cfg.TurnBusy,
		removedCh:    make(chan struct{}), // never closed: bootstrap is ErrCannotRemoveBootstrap
		lcState:      lcState,
		activateCh:   make(chan struct{}, 1),
		evictCh:      make(chan struct{}, 1),
	}
	sess.defaultFamily = defaultFamily
	if lcState == stateActive {
		sess.activeCh = closedChan()
		sess.evictedCh = make(chan struct{})
	} else {
		sess.activeCh = make(chan struct{})
		sess.evictedCh = closedChan()
	}
	p = &Pool{
		sessions:           map[SessionID]*Session{bootstrapID: sess},
		dormant:            dormantEntries(reg, bootstrapID),
		bootstrap:          bootstrapID,
		systemPromptPath:   systemPromptPath,
		readyCh:            make(chan struct{}),
		log:                cfg.Logger,
		registryPath:       cfg.RegistryPath,
		claudeSessionsDir:  cfg.ClaudeSessionsDir,
		convReg:            cfg.ConversationsRegistry,
		convRegistryPath:   cfg.ConversationsRegistryPath,
		readFolders:        slices.Clone(cfg.ReadFolders),
		defaultModel:       cfg.DefaultModel,
		convSweepInterval:  sweepInterval,
		activeCap:          cfg.ActiveCap,
		sessionTpl:         cfg.Bootstrap,
		idleTimeoutDefault: cfg.IdleTimeout,
		turnBusy:           cfg.TurnBusy,
		newRunner:          newRunner,
		onRunnerStopped:    cfg.OnRunnerStopped,
	}
	sess.pool = p
	if err := p.loadDaemonInstructions(); err != nil {
		return nil, err
	}

	// Persist on cold start (no prior file). Warm starts do not rewrite —
	// the AC promises "writes only on state-changing operations", and a
	// warm reload is not a state change.
	if cfg.RegistryPath != "" && reg == nil {
		if err := p.saveLocked(); err != nil {
			return nil, fmt.Errorf("sessions: save registry: %w", err)
		}
	}

	// #839: no startup adopt-by-mtime. The bootstrap id is authoritative from
	// the persisted registry (warm start) or a freshly-minted id (cold start);
	// it is never rotated to a foreign <uuid>.jsonl found in the shared sessions
	// dir. The transcript resolvers stay — they still back the #838
	// growth-confirm baseline.
	built = true
	return p, nil
}

// Lookup resolves a SessionID to a Session. An empty id resolves to the
// default (bootstrap) entry — this is the mechanism that lets the Phase 1.0
// control plane (after Child B) call Lookup(req.SessionID) with the
// currently-empty field, and Phase 1.1 populates the field with no handler
// diff. A non-empty unknown id returns ErrSessionNotFound.
func (p *Pool) Lookup(id SessionID) (*Session, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if id == "" {
		return p.sessions[p.bootstrap], nil
	}
	sess, ok := p.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

// ResolveID maps a user-supplied UUID-or-prefix string to the canonical
// SessionID of a session in the pool. Empty arg returns the bootstrap
// session's id (same seam as Pool.Lookup("")).
//
// Resolution order:
//
//  1. arg == "" → bootstrap id, no error.
//  2. arg is an exact key in the in-memory session map → that id, no error.
//     This short-circuit is a single map lookup; it never falls through to
//     the prefix scan, so an exact full-UUID match wins even if the same
//     string would also be a HasPrefix hit on the same session.
//  3. otherwise, scan the in-memory map and collect every session whose
//     SessionID has arg as a prefix (strings.HasPrefix). Exactly one match
//     → that id, no error. Zero matches → ErrSessionNotFound. Two or more
//     → ErrAmbiguousSessionID with a list of matches in the wrapped message.
//
// No minimum prefix length is enforced. A one-character prefix is accepted
// when it is unique; refusing short prefixes is a CLI-layer policy concern,
// not a pool invariant.
//
// Concurrency: takes p.mu (RLock) for the entire resolution. The in-memory
// map is the source of truth — sessions.json is not re-read. Concurrent
// Pool.List/Lookup/Snapshot share the read lock; concurrent writers
// (Rename/Create/Remove/RotateID/saveLocked) serialise behind the write
// lock as today.
//
// Lock order: Pool.mu (RLock) only. Does not take Session.lcMu — `id` and
// `label` are guarded by Pool.mu (RotateID and Rename mutate them under
// Pool.mu write), so no torn reads.
func (p *Pool) ResolveID(arg string) (SessionID, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if arg == "" {
		return p.bootstrap, nil
	}
	// Exact-match short-circuit. AC #1: full UUID always wins, with no
	// extra scan cost beyond the existing map lookup.
	if _, ok := p.sessions[SessionID(arg)]; ok {
		return SessionID(arg), nil
	}

	var matches []*Session
	for id, s := range p.sessions {
		if strings.HasPrefix(string(id), arg) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return "", ErrSessionNotFound
	case 1:
		return matches[0].id, nil
	default:
		return "", ambiguousError(matches)
	}
}

// ambiguousError formats an ErrAmbiguousSessionID-wrapping error whose
// message lists each match as `<uuid> (<label>)` on its own line, sorted by
// SessionID ascending (same tiebreak as Pool.List). The bootstrap entry's
// empty on-disk label is substituted with the synthetic string "bootstrap"
// to mirror Pool.List one-for-one.
func ambiguousError(matches []*Session) error {
	sort.Slice(matches, func(i, j int) bool { return matches[i].id < matches[j].id })
	var b strings.Builder
	for i, s := range matches {
		label := s.label
		if s.bootstrap && label == "" {
			label = "bootstrap"
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s (%s)", s.id, label)
	}
	return fmt.Errorf("%w:\n%s", ErrAmbiguousSessionID, b.String())
}

// Default returns the bootstrap session. Equivalent to Lookup("") today;
// kept as an explicit accessor because cmd/pyry will need the bootstrap
// entry at startup (Child B).
func (p *Pool) Default() *Session {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[p.bootstrap]
}

// HarnessFor returns the coding agent the session id runs — the live session's
// own harness, else its dormant entry's, canonical in both cases so an entry with
// no key answers HarnessClaude — or ErrSessionNotFound when this pool holds id in
// neither half (#2629). The settings gate reads it to check a model and an effort
// against the entries of that agent rather than of claude alone.
//
// Live first, under one read lock, so the two halves are consulted as one
// partition rather than across a revive window. The harness itself cannot change
// under a caller: it is construction-fixed on a Session and carried from the
// entry by a revive. The empty id is a miss, not the bootstrap — both maps are
// read by exact key, unlike Lookup. No id validation and no logging, and the
// error is returned bare: DormantSettingsFor's posture.
func (p *Pool) HarnessFor(id SessionID) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if sess, ok := p.sessions[id]; ok {
		return sess.harness, nil
	}
	if entry, ok := p.dormant[id]; ok {
		return canonicalHarness(entry.Harness), nil
	}
	return "", ErrSessionNotFound
}

// EverActivated reports whether the session named by id has ever been activated
// — the durable, restart-surviving answer to "has this conversation ever run?"
// (#2521). It is what lets the named new_session path tell a conversation created
// but never messaged, which must stay inert, from a previously-used session that
// merely has no child at this instant, which must be resettable.
//
// It answers from ONE predicate over TWO sources, which is the whole point: the
// live session's lastActiveAt against its createdAt when the pool holds it, and
// the persisted entry's when it does not. That second arm is
// DormantSettingsFor's shape and exists for the same reason (#2448/#2449) — after
// a daemon restart New materialises only the bootstrap, so every
// per-conversation session is a persisted entry with no *Session behind it, and a
// reader that consulted p.sessions alone would answer "never run" for every
// channel the daemon holds a record of.
//
// THE TIMESTAMP PAIR IS AN EXACT READING, not a tolerance. buildSession stamps
// createdAt and lastActiveAt from one now value, so a never-activated session has
// them equal to the nanosecond; and every writer of lastActiveAt afterwards —
// Session.transitionTo, Session.touchLastActive, Session.beginEvict and
// rekeyLocked — fires only on an activation, an eviction or a rotation, none of
// which a minted-and-untouched session reaches. Both fields are already in
// registryEntry, so nothing here is a schema change and no migration exists: a
// channel that ran before this method did already carries the distinguishing
// pair.
//
// THE READING SURVIVES A MATERIALISATION, and only because materialise carries a
// retired dormant entry's created_at/last_active_at onto the session it registers.
// That dependency is worth stating rather than leaving to be re-derived, because
// buildSession stamps a FRESH equal pair: without the carry a revived-but-not-yet-
// activated session answers false here while its own retired entry said true, and
// the retirement has already taken the dormant arm's fallback with it. /clear
// reaches this method in precisely that state — its route revives for binding
// validation before the intercept raises the reset — so a future edit that drops
// the carry does not merely lose an age field, it re-opens #2521 on that route.
//
// ITS ONE BLIND SPOT, named rather than papered over: the bootstrap session
// warm-starts in stateActive straight from its persisted row without a
// transition, so a bootstrap whose timestamps still read equal answers false even
// while its child is up. Every caller today asks only about a session with NO
// LIVE CHILD, where that reading is the right one anyway; a caller that wants
// "is it running" wants State().ChildPID, which is a different question.
//
// The empty id is refused BEFORE either map read. That is the #678 isolation
// point resolveBoundSession documents: Pool.Lookup("") resolves to the BOOTSTRAP
// session, and a reader that let the empty id through would answer a question
// about the daemon's shared child instead of the caller's. No id validation
// beyond that and no logging, SettingsFor's and DormantSettingsFor's posture
// verbatim — a malformed id is a map miss, which is already the correct answer,
// and nothing about it reaches a log line.
//
// Concurrency: safe from any goroutine. Lock order Pool.mu (read) → Session.lcMu,
// the order List, saveLocked and pickLRUVictim already keep; no new edge.
func (p *Pool) EverActivated(id SessionID) bool {
	if id == "" {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if sess, ok := p.sessions[id]; ok {
		sess.lcMu.Lock()
		defer sess.lcMu.Unlock()
		return sess.lastActiveAt.After(sess.createdAt)
	}
	entry, ok := p.dormant[id]
	return ok && entry.LastActiveAt.After(entry.CreatedAt)
}

// BootstrapID returns the pool's current bootstrap session id under p.mu
// (RLock). It resolves p.bootstrap fresh on each call — mirroring
// Default/DefaultSettings — so it stays correct across a /clear rotation:
// RotateID flips p.bootstrap under the write lock, so the next spawn's
// ResolveSessionID provider (which calls this) resolves the rotated id (#839).
//
// Concurrency: reads p.bootstrap (a p.mu-guarded SessionID value), deliberately
// NOT Default().ID()/sess.id. Reading p.bootstrap keeps the spawn path on the
// Pool.mu-guarded value and avoids taking Session.lcMu — which now guards
// sess.id alongside Pool.mu (#866) — while introducing no new sess.id reader.
// The RLock is race-clean against RotateID's Lock.
func (p *Pool) BootstrapID() SessionID {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bootstrap
}

// Run blocks until ctx is cancelled, supervising every session in the pool and
// running the conversations auto-archive sweep loop (when ConversationsRegistry
// is set) alongside it. errgroup ties the goroutines together: cancellation
// propagates, and Wait returns the first non-nil error.
//
// Until #2137 this also ran an fsnotify rotation watcher over ClaudeSessionsDir,
// which guessed at which session had rotated by asking the OS which transcript
// each tracked pid held open. claude announces the fact directly now
// (conversation_reset, #2134/#2135/#2136), so the guess and its goroutine are
// gone; AdoptAnnouncedID carries the rotation instead.
//
// Phase 1.1+ extends the fan-out to one supervisor.Run goroutine per session
// — the errgroup wrapper introduced here is the extension point.
func (p *Pool) Run(ctx context.Context) error {
	p.mu.RLock()
	bootstrap := p.sessions[p.bootstrap]
	p.mu.RUnlock()

	// The bootstrap is never Remove-d (ErrCannotRemoveBootstrap), so its
	// MCP-enable settings file (#943) lives for the daemon-process lifetime and is
	// removed here, when Run returns on ctx cancel / shutdown. Since #1518 the
	// file sits in the daemon data dir rather than os.TempDir, so nothing reaps
	// what this defer misses — a SIGKILL still leaks it, which AC #4's id-derived
	// naming bounds by overwriting on the next start rather than accumulating.
	defer func() {
		if bootstrap != nil && bootstrap.settingsPath != "" {
			_ = os.Remove(bootstrap.settingsPath)
		}
		// The bootstrap's appended system-prompt file (#2093) is daemon-scoped, so
		// this is its ONE removal site: it is not any single session's to delete.
		// Same SIGKILL exposure as the line above, bounded the same way — the name
		// is fixed, so the next start overwrites rather than accumulating.
		if p.systemPromptPath != "" {
			_ = os.Remove(p.systemPromptPath)
		}
		// Every OTHER session's prompt file goes with the daemon, whether or not
		// its session was ever Remove-d (#2150): these carry operator text, and
		// "never outlives the daemon" is an acceptance criterion rather than
		// housekeeping. Removing the whole directory rather than walking live
		// sessions is deliberate — a session torn down between the walk and the
		// removal would still leave its file behind. A warm start rebuilds each
		// revived session's file through buildSession, so nothing needs these
		// across a restart; Pool.New purges what a SIGKILL leaves.
		//
		// This defer runs after Run's errgroup has returned, so no lifecycle
		// goroutine survives to respawn a child into a deleted path.
		if dir := sessionPromptsDirFor(p.registryPath); dir != "" {
			_ = os.RemoveAll(dir)
		}
	}()

	g, gctx := errgroup.WithContext(ctx)

	p.mu.Lock()
	p.runGroup, p.runCtx = g, gctx
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.runGroup, p.runCtx = nil, nil
		p.mu.Unlock()
	}()

	if err := p.supervise(bootstrap); err != nil {
		return fmt.Errorf("sessions: supervise bootstrap: %w", err)
	}
	// Signal readiness: runGroup/runCtx are wired and the bootstrap is
	// supervised, so Pool.Create's supervise can no longer hit
	// ErrPoolNotRunning. Embedded hosts (pyry acp) gate their first
	// session/new on Ready() to close that unrecoverable startup race.
	p.readyOnce.Do(func() { close(p.readyCh) })

	if p.convReg != nil {
		interval := p.convSweepInterval
		g.Go(func() error {
			return conversations.RunSweepLoop(gctx, p.convReg, p.convRegistryPath, interval, p.log)
		})
	}

	return g.Wait()
}

// supervise schedules sess.Run on Pool.Run's live errgroup. Returns
// ErrPoolNotRunning when the handle is not set. Callers must invoke
// supervise only while Pool.Run is active; the sentinel turns the
// race-prone "supervise after shutdown" case into a clean error rather
// than a silent leak.
//
// Lock discipline: takes p.mu (RLock) briefly to snapshot runGroup and
// runCtx, then releases the lock before calling g.Go. Does not touch
// Session.lcMu. Preserves Pool.mu → Session.lcMu and
// Pool.capMu → Pool.mu → Session.lcMu lock orders.
func (p *Pool) supervise(sess *Session) error {
	p.mu.RLock()
	g, gctx := p.runGroup, p.runCtx
	p.mu.RUnlock()
	if g == nil {
		return ErrPoolNotRunning
	}
	g.Go(func() error { return sess.Run(gctx) })
	return nil
}

// Ready returns a channel closed once Pool.Run has wired its supervisor handle
// and supervised the bootstrap — i.e. once Pool.Create will schedule the new
// session instead of returning ErrPoolNotRunning. Before Run is ever called the
// channel is open (never ready). Closed exactly once; safe to select on
// repeatedly and from multiple goroutines.
func (p *Pool) Ready() <-chan struct{} {
	return p.readyCh
}

// Snapshot returns one entry per session, capturing the current
// supervisor.State().ChildPID. PID == 0 means no live child. Safe for
// concurrent use; takes RLock only.
func (p *Pool) Snapshot() []SnapshotEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]SnapshotEntry, 0, len(p.sessions))
	for _, s := range p.sessions {
		out = append(out, SnapshotEntry{ID: s.id, PID: s.State().ChildPID})
	}
	return out
}

// saveLocked snapshots the current in-memory sessions into a registryFile and
// writes it atomically. Caller MUST hold p.mu (write). No-op when
// registryPath is empty (test-only persistence-disabled mode).
//
// The file is the union of the live sessions and p.dormant — the entries this
// process parsed at New and has not materialised. Writing the live half alone is
// what erased a dropped session's record, model and effort included, on the first
// save after a daemon restart (#2448).
//
// Each session's lifecycle state and lastActiveAt are read under Session.lcMu.
// Lock order: Pool.mu (held by caller) → Session.lcMu. transitionTo enforces
// the symmetric rule (release lcMu before acquiring Pool.mu via persist).
func (p *Pool) saveLocked() error {
	if p.registryPath == "" {
		return nil
	}
	reg := &registryFile{
		Version:  1,
		Sessions: make([]registryEntry, 0, len(p.sessions)+len(p.dormant)),
	}
	for _, s := range p.sessions {
		s.lcMu.Lock()
		state := s.lcState
		lastActive := s.lastActiveAt
		s.lcMu.Unlock()
		entry := registryEntry{
			ID:           s.id,
			Label:        s.label,
			CreatedAt:    s.createdAt,
			LastActiveAt: lastActive,
			Bootstrap:    s.bootstrap,
			// s.settings is read under the held Pool.mu (same discipline as
			// s.label above, NOT under lcMu); Pool.UpdateSettings mutates it
			// under that same lock. omitempty on the tags keeps the
			// default-session on-disk shape byte-stable.
			Model:  s.settings.Model,
			Effort: s.settings.Effort,
			YOLO:   s.settings.YOLO,
			// permissionModeForDisk drops the default posture and the
			// escalation, so the omitempty shape above stays byte-stable for a
			// default session and the escalation keeps one on-disk spelling.
			PermissionMode: permissionModeForDisk(s.settings),
			// claude writes no key, so a claude session's entry keeps its
			// pre-#2593 shape.
			Harness: harnessForDisk(s.harness),
			// Empty for claude, so omitempty keeps its entry's shape (#2622).
			ThreadID: s.threadID,
		}
		// omitempty on the JSON tag keeps the stable on-disk shape for
		// the dominant active case — important for the existing
		// idempotent-reload guarantee.
		if state == stateEvicted {
			entry.LifecycleState = state.String()
		}
		reg.Sessions = append(reg.Sessions, entry)
	}
	for id, entry := range p.dormant {
		// The invariant: an id reaches disk exactly once, from its live session
		// whenever one exists. materialise retires the entry it takes over and
		// Remove drops the one it deletes, so this skip fires on no path today —
		// it is kept because it is the invariant's enforcement at the single
		// write point, where a delete per mutation site is one site per path.
		// rekeyLocked is the live reason: it moves a session onto an id this
		// package does not choose (claude announces it), and a duplicate id in
		// sessions.json is silent corruption no loadRegistry reader can
		// disambiguate.
		if _, live := p.sessions[id]; live {
			continue
		}
		reg.Sessions = append(reg.Sessions, entry)
	}
	// Already the merged list's ordering rule: the dormant entries carry the
	// CreatedAt they were written with, so a file this process only passes
	// through keeps its order.
	sortEntriesByCreatedAt(reg.Sessions)
	return saveRegistryLocked(p.registryPath, reg)
}

// persist takes Pool.mu (write) and writes the registry. Called by
// Session.transitionTo after a state transition; the transition's lcMu is
// already released before this is called.
func (p *Pool) persist() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveLocked()
}

// Activate is the spawn-path entry: resolves an id and calls Session.Activate,
// enforcing the concurrent-active cap (Config.ActiveCap) along the way.
//
// When the cap is unset (activeCap <= 0), this is a thin wrapper around
// Session.Activate — byte-identical to Phase 1.2c-A's behaviour, no LRU
// bookkeeping cost on the hot path.
//
// When the cap is set, capMu serializes the cap-check + victim-eviction +
// new-spawn sequence. Two concurrent Activates against the same Pool can't
// both observe active < cap and both proceed. If the target is already
// active, lastActiveAt is bumped (LRU touch) and the call returns. Otherwise,
// when activating one more would exceed the cap, the LRU peer is evicted via
// Session.Evict before this Activate proceeds.
//
// Until #2137 this was also where the rotation watcher's freshly-allocated
// skip-set was primed: claude opening <id>.jsonl looked like a /clear rotation
// to the watcher, so the spawn had to announce its own id as not-a-rotation, and
// #2085 moved that prime here from mint time because the TTL only covered a
// spawn that followed within milliseconds. The watcher is gone and the skip-set
// with it, so the prime is gone too — this is once again a plain spawn entry.
//
// Lock discipline: the Lookup above takes and releases p.mu.RLock before p.capMu
// is acquired below. The documented capMu → mu → lcMu order is not inverted,
// because no goroutine ever holds p.mu while acquiring p.capMu.
func (p *Pool) Activate(ctx context.Context, id SessionID) error {
	sess, err := p.Lookup(id)
	if err != nil {
		return err
	}
	p.refreshSystemPrompt(ctx, sess)
	if p.activeCap <= 0 {
		return sess.Activate(ctx)
	}

	p.capMu.Lock()
	defer p.capMu.Unlock()

	// Already active: refresh LRU stamp and return. The slot is already
	// counted; no eviction needed.
	if sess.LifecycleState() == stateActive {
		sess.touchLastActive()
		return nil
	}

	if victim := p.pickLRUVictim(sess.id); victim != nil {
		if err := victim.Evict(ctx); err != nil {
			return fmt.Errorf("cap: evict lru victim %s: %w", victim.id, err)
		}
	}
	return sess.Activate(ctx)
}

// pickLRUVictim returns the least-recently-active currently-active session
// (by Session.lastActiveAt) when activating one more session would exceed
// p.activeCap. Returns nil when the cap would not bind, when no eligible
// peer exists (target is the only candidate), or when activeCap <= 0.
//
// Excludes target from the victim set: you cannot evict the session you are
// about to activate to make room for itself. Caller must hold p.capMu.
func (p *Pool) pickLRUVictim(target SessionID) *Session {
	if p.activeCap <= 0 {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	var (
		active int
		victim *Session
		oldest time.Time
	)
	for id, s := range p.sessions {
		s.lcMu.Lock()
		isActive := s.lcState == stateActive
		la := s.lastActiveAt
		s.lcMu.Unlock()
		if !isActive {
			continue
		}
		active++
		if id == target {
			continue
		}
		if victim == nil || la.Before(oldest) {
			victim = s
			oldest = la
		}
	}
	if active < p.activeCap {
		return nil
	}
	return victim
}
