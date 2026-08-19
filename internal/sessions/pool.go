package sessions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/conversations"
	"github.com/pyrycode/pyrycode/internal/sessions/rotation"
	"golang.org/x/sync/errgroup"
)

// allocatedTTL bounds how long a UUID stays in the freshly-allocated skip
// set before being pruned. Defined as a var (not const) so tests can shrink
// it.
var allocatedTTL = 30 * time.Second

// newProbe is the rotation.Probe factory. Indirected via a package var so
// tests can inject a fake without touching the platform-specific build files.
var newProbe = rotation.DefaultProbe

// ErrSessionNotFound is returned by Pool.Lookup for a non-empty unknown id.
var ErrSessionNotFound = errors.New("sessions: session not found")

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
	// Zero here means "never evict" — the test default. Production
	// callers in cmd/pyry default this to 15 minutes via the
	// -pyry-idle-timeout flag.
	IdleTimeout time.Duration

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

	// RunnerFactory is the injection seam for the supervised child. nil selects
	// the default supervisor.New, keeping the PTY / interactive path
	// byte-identical to today — that nil default is the rollback guarantee. A
	// non-nil factory is invoked at every construction site (bootstrap in New
	// and per-session in buildSession); it lets the Streamrunner Interactive work
	// (T4/T7) swap in an alternative Runner behind the same lifecycle. This
	// ticket wires the seam only — no consumer is migrated onto an alternative
	// runner yet.
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
	// exit after the configured period with no attached clients. The
	// JSONL on disk is preserved; a subsequent Activate spawns a fresh
	// claude that reads the prior conversation. Zero inherits
	// Config.IdleTimeout; if both are zero, eviction is disabled.
	IdleTimeout time.Duration

	// RecordDir, when non-empty, records this session's interactive spawns to
	// .cast files under it (supervisor.Config.RecordDir, #802). Only the daemon's
	// bootstrap session sets it, gated on the persisted debug_capture flag;
	// per-caller sessions (buildSession) leave it empty and record nothing.
	RecordDir string
}

// Pool owns the set of sessions managed by one pyry process. Phase 1.0
// constructs exactly one entry — the bootstrap session — at New().
type Pool struct {
	mu                sync.RWMutex
	sessions          map[SessionID]*Session
	bootstrap         SessionID
	log               *slog.Logger
	registryPath      string
	claudeSessionsDir string

	// convReg and convRegistryPath mirror Config.ConversationsRegistry /
	// .ConversationsRegistryPath. Read-only after New — set once,
	// consulted only by Pool.Run to decide whether to register the sweep
	// goroutine. No lock needed.
	convReg          *conversations.Registry
	convRegistryPath string

	// convSweepInterval is the resolved interval Pool.Run passes to
	// conversations.RunSweepLoop. Set in New from cfg.SweepInterval, with
	// conversations.SweepInterval as the zero-value fallback. Read-only
	// after New — set once, consulted only by Pool.Run. No lock needed.
	//
	// In-package tests may overwrite this field directly after construction
	// (mirrors the existing convReg / convRegistryPath pattern in
	// pool_conv_sweep_test.go).
	convSweepInterval time.Duration

	allocated map[SessionID]time.Time

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

	// newRunner is the normalized (never-nil) runner factory: cfg.RunnerFactory
	// when supplied, else a wrapper over supervisor.New. Set once in New and
	// read by buildSession thereafter — read-only post-construction, same
	// lifetime and lock-free discipline as log / convReg. See Config.RunnerFactory.
	newRunner RunnerFactory

	// transitionObserver is the optional, injectable signal surfaced on
	// /clear rotations and evictions. Set once via SetTransitionObserver
	// BEFORE Pool.Run; read-only thereafter, so the lifecycle + watcher
	// goroutines (both spawned by Run) read it lock-free via Run's
	// goroutine-start happens-before. nil disables it. See transition.go.
	transitionObserver TransitionObserver
}

// SnapshotEntry is one (id, pid) pair captured by Pool.Snapshot. Carries
// only primitive types so the rotation package can consume snapshots without
// importing internal/sessions.
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
// Returns an error if the rng, supervisor.New, registry load, or registry
// save fails — all are fatal-at-startup conditions.
func New(cfg Config) (*Pool, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// Normalize the runner factory once: a nil cfg.RunnerFactory selects the
	// default supervisor.New (adapted to the Runner return), so the concrete
	// supervisor flows through and the PTY path is byte-identical — the rollback
	// guarantee. newRunner is threaded through both construction sites (bootstrap
	// below, per-session via Pool.newRunner in buildSession).
	// A factory is mandatory since #1348. It used to default to the terminal
	// supervisor when nil, which meant an unconfigured caller silently got the
	// runner that no longer exists.
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
		// operator's spawn intent and must survive restart.
		settings = SessionSettings{Model: entry.Model, Effort: entry.Effort, YOLO: entry.YOLO}
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
	defer func() {
		if !built {
			_ = os.Remove(settingsPath)
		}
	}()

	// base is the settings-free bootstrap argv (template plus the immutable
	// --settings pair); bootstrapArgs appends the model/effort/YOLO suffix. Clone
	// before appending: today's code aliases cfg.Bootstrap.ClaudeArgs directly,
	// and we must not mutate the caller's slice. base is stored on the bootstrap
	// Session so a live restart (#842) can recompose full argv from the persisted
	// settings.
	base := append(slices.Clone(cfg.Bootstrap.ClaudeArgs), "--settings", settingsPath)
	bootstrapArgs := append(slices.Clone(base), claudeSettingsArgs(settings)...)
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
		SessionID:      string(bootstrapID),
		ClaudeArgs:     bootstrapArgs,
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
		settings:     settings,
		spawnBase:    base,
		settingsPath: settingsPath,
		idleTimeout:  idleTimeout,
		removedCh:    make(chan struct{}), // never closed: bootstrap is ErrCannotRemoveBootstrap
		lcState:      lcState,
		activateCh:   make(chan struct{}, 1),
		evictCh:      make(chan struct{}, 1),
	}
	if lcState == stateActive {
		sess.activeCh = closedChan()
		sess.evictedCh = make(chan struct{})
	} else {
		sess.activeCh = make(chan struct{})
		sess.evictedCh = closedChan()
	}
	p = &Pool{
		sessions:           map[SessionID]*Session{bootstrapID: sess},
		bootstrap:          bootstrapID,
		readyCh:            make(chan struct{}),
		log:                cfg.Logger,
		registryPath:       cfg.RegistryPath,
		claudeSessionsDir:  cfg.ClaudeSessionsDir,
		convReg:            cfg.ConversationsRegistry,
		convRegistryPath:   cfg.ConversationsRegistryPath,
		convSweepInterval:  sweepInterval,
		allocated:          make(map[SessionID]time.Time),
		activeCap:          cfg.ActiveCap,
		sessionTpl:         cfg.Bootstrap,
		idleTimeoutDefault: cfg.IdleTimeout,
		newRunner:          newRunner,
	}
	sess.pool = p

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

// RotateID atomically replaces the in-memory entry keyed by oldID with one
// keyed by newID, updates the bootstrap pointer if oldID was the bootstrap,
// and persists. p.mu is held (write) across the whole operation, matching
// the 1.2a saveLocked invariant.
//
// Returns ErrSessionNotFound if oldID is unknown. Returns the save error
// verbatim if persistence fails — the in-memory rotation is already applied
// at that point; callers decide whether to treat the save error as fatal.
// RotateID(x, x) is a no-op.
//
// Invariant: sess.id is guarded by two locks — the write below holds BOTH
// Pool.mu (W, via the function-level defer) and Session.lcMu; a read is
// race-clean while holding either one. Lifecycle goroutines read it via
// currentID() (Session.lcMu); Pool.mu-holders (List, ResolveID, Snapshot,
// saveLocked, Activate) read it directly. The old "no concurrent reader
// exists" claim went stale when #839 wired RotateID into the live fsnotify
// rotation watcher, whose goroutine runs concurrently with the per-session
// lifecycle goroutines and fires on every /clear. lastActiveAt shares the same
// lcMu section. Lock order remains Pool.mu → Session.lcMu.
//
// This is the load-bearing seam the live-detection ticket reuses.
func (p *Pool) RotateID(oldID, newID SessionID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sessions[oldID]; !ok {
		return ErrSessionNotFound
	}
	if oldID == newID {
		return nil
	}
	p.rekeyLocked(oldID, newID)
	return p.saveLocked()
}

// RotateBootstrapForSelfHeal mints a fresh daemon id, re-keys the CURRENT
// bootstrap entry to it, and persists — all under a single p.mu (write) hold, so
// a (practically impossible during a crash-loop) concurrent /clear cannot skew
// old vs new. It is the supervisor's crash-loop self-heal seam (#1165): when the
// bootstrap child fast-crashes non-zero N times in a row on the same pinned id (a
// deterministic wedge no backoff clears), the supervisor calls this via its
// Config.SelfHeal closure to get the daemon off the wedged id. The next spawn's
// ResolveSessionID pull (BootstrapID()) resolves the rotated id automatically —
// no push into the spawn path, matching the #839 pull-not-push decoupling.
//
// Unlike RotateForNewSession it does NOT register the new id in the allocated
// skip-set and does NOT fire a client transition. The absent skip-set entry is
// deliberate and load-bearing: the rekey commits p.bootstrap → newID BEFORE the
// next spawn creates <newID>.jsonl, so when the rotation watcher's handleCreate
// fires, Snapshot() already reports {ID: newID} and the ref.ID==stem guard
// (watcher.go) returns early — structurally identical to cold start, which
// likewise leaves the bootstrap id un-allocated. A ReasonClear transition would
// mislead clients into thinking the user ran /clear (client notification of a
// self-heal is out of scope).
//
// Returns the minted id. ErrSessionNotFound if the bootstrap entry is somehow
// absent (TOCTOU). A saveLocked failure is logged at Warn and swallowed — the
// in-memory rotation is authoritative for the running daemon (which is what
// self-heal needs: get this process onto the fresh id now); persistence is
// best-effort, matching RotateID / RotateForNewSession.
func (p *Pool) RotateBootstrapForSelfHeal() (SessionID, error) {
	newID, err := NewID()
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.bootstrap
	if _, ok := p.sessions[old]; !ok {
		return "", ErrSessionNotFound
	}
	p.rekeyLocked(old, newID)
	if err := p.saveLocked(); err != nil {
		p.log.Warn("sessions: self-heal rotate persist failed",
			"event", "rotate_self_heal.persist_failed",
			"session_id", string(newID),
			"previous_session_id", string(old),
			"err", err)
	}
	return newID, nil
}

// rekeyLocked moves the in-memory session entry from oldID to newID: it stamps
// the new id + lastActiveAt under Session.lcMu, moves the map entry, and flips
// the bootstrap pointer if oldID was the bootstrap. Caller MUST hold p.mu (write)
// and MUST have already verified oldID is present and oldID != newID; it does not
// persist (the caller invokes saveLocked). Shared by RotateID (watcher-observed
// self-rotation) and RotateForNewSession (daemon-driven new_session) so the
// re-key invariant lives in one place. Lock order remains Pool.mu → Session.lcMu.
func (p *Pool) rekeyLocked(oldID, newID SessionID) {
	sess := p.sessions[oldID]
	sess.lcMu.Lock()
	sess.id = newID
	sess.lastActiveAt = time.Now().UTC()
	sess.lcMu.Unlock()
	delete(p.sessions, oldID)
	p.sessions[newID] = sess
	if p.bootstrap == oldID {
		p.bootstrap = newID
	}
}

// Rename updates the named session's label and persists the change to the
// registry. Empty newLabel is permitted and clears the on-disk label to "";
// Pool.List's synthetic "bootstrap" substitution continues to apply when the
// bootstrap's on-disk label is empty.
//
// Returns ErrSessionNotFound when id is not present in the pool. On the
// not-found path the in-memory registry and the on-disk sessions.json are
// byte-identical to their prior state — saveLocked is not invoked. A no-op
// rename (newLabel equals the current label) also skips saveLocked, keeping
// the registry mtime stable.
//
// Concurrency: takes p.mu (write) for the read-modify-write and holds it
// across the persisted file write, matching the RotateID/saveLocked
// invariant. Concurrent Pool.List/Lookup/Snapshot calls block on Pool.mu
// briefly; nothing else needs synchronisation.
//
// Lock order: Pool.mu (write). Does not take Session.lcMu — Session.label is
// guarded by Pool.mu (the only other readers are List and saveLocked, both
// under Pool.mu).
func (p *Pool) Rename(id SessionID, newLabel string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	sess, ok := p.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if sess.label == newLabel {
		return nil
	}
	prev := sess.label
	sess.label = newLabel
	if err := p.saveLocked(); err != nil {
		sess.label = prev
		return err
	}
	return nil
}

// UpdateSettings merges the fields marked present in update into the stored
// settings of session id and re-persists, taking p.mu (write) exactly as Rename
// does for label. A nil field in update leaves the stored value untouched; a
// non-nil field overwrites it (including "" for Model/Effort and false for
// YOLO). An absent (nil) YOLO can never enable bypass — only an explicit
// non-nil *true does. Returns ErrSessionNotFound for an unknown id, creating no
// entry. A no-op update (every present field already equal to the stored value,
// or every field nil) writes nothing to disk, keeping the registry mtime
// stable. On a saveLocked failure the in-memory settings are rolled back so
// memory stays consistent with disk.
//
// After a successful persist of a real change the change is also LIVE-APPLIED to
// the session's supervisor, by one of two mechanisms picked on what the update
// carried — which fields, and for YOLO its value too (inBandDeliverable):
//
//   - A change claude accepts on the stream the daemon already holds open is
//     delivered IN-BAND (#1581, #1604): the /model and /effort commands as
//     ordinary user turns, and a bypass REVOCATION as a set_permission_mode
//     control request, so the child is neither terminated nor respawned and its
//     transcript survives. That branch still installs the recomposed argv, via
//     SetSpawnArgs — Restart's swap half — because skipping the install would let
//     the operator's change silently revert on the next crash-respawn or evict →
//     Activate. Delivery is fire-and-forget; see deliverSettingsInBand.
//   - Every other change keeps the live restart (#842): the argv is recomposed
//     from the persisted settings and, if a child is running, that child is
//     killed so the supervisor relaunches it — resuming the conversation — with
//     the new model / effort / YOLO. This is the path a bypass ENABLE takes, and
//     the path a model or effort cleared back to claude's own default takes.
//
// The bypass split is on DIRECTION, not presence, and it is measured rather than
// assumed (#1595): claude accepts a revocation on a held-open stream but refuses
// an escalation in words, gating it on the launch argv. So a revoke applies to the
// running child with no respawn, and an enable can only be granted by relaunching
// under the recomposed argv.
//
// For an evicted session neither mechanism finds a child; the argv install alone
// applies on the next Activate, and the caller still sees success. Both branches
// run OUTSIDE p.mu (every installer is non-blocking and drives only
// supervisor-internal state, so the Pool.mu → Session.lcMu order is untouched)
// and only on a real change: an unknown id, a no-op update, and a persist
// failure all skip them, so a still-correct running child is never disturbed.
//
// Lock order: p.mu (write), released before the live-apply. Does not take Session.lcMu
// — Session.settings is guarded by p.mu (the only other reader is saveLocked,
// under p.mu); spawnBase and sup are immutable post-construction.
func (p *Pool) UpdateSettings(id SessionID, update SettingsUpdate) error {
	p.mu.Lock()
	sess, ok := p.sessions[id]
	if !ok {
		p.mu.Unlock()
		return ErrSessionNotFound
	}
	merged := sess.settings
	if update.Model != nil {
		merged.Model = *update.Model
	}
	if update.Effort != nil {
		merged.Effort = *update.Effort
	}
	if update.YOLO != nil {
		merged.YOLO = *update.YOLO
	}
	if merged == sess.settings {
		p.mu.Unlock()
		return nil
	}
	prev := sess.settings
	sess.settings = merged
	if err := p.saveLocked(); err != nil {
		sess.settings = prev
		p.mu.Unlock()
		return err
	}
	// Recompose argv + capture the supervisor while under p.mu (both reads are of
	// state that is either immutable — spawnBase, sup — or the merged value we
	// just persisted). Release p.mu BEFORE the live-apply so no blocking work and
	// no supervisor-internal lock is taken under it.
	newArgs := sess.spawnArgs(merged)
	sup := sess.sup
	p.mu.Unlock()

	// Both branches install newArgs; only one kills. Swap BEFORE the write: the
	// install is the durable half and is non-blocking, so if the delivery fails
	// the next spawn still carries the change.
	if inBandDeliverable(update) {
		sup.SetSpawnArgs(newArgs)
		p.deliverSettingsInBand(id, sup, update)
		return nil
	}
	// This is a LIVE PRODUCTION CALLER of Restart, and #1604 did not remove it.
	// #1574 may NOT delete Restart: its premise — that once model, effort and the
	// bypass posture have all moved off Restart(args) it has no production caller
	// left — is false, because the bypass posture only half-moved. A revoke goes
	// in-band above; an ENABLE reaches here, and only the kill-and-relaunch under
	// the recomposed argv can grant it, since claude refuses the escalation over
	// the control channel (#1595). Deleting this call would regress an enable from
	// "applies now" to "applies at the next spawn", the same regression in the
	// opposite direction to the one the revoke exists to prevent.
	sup.Restart(newArgs)
	return nil
}

// inBandDeliverable reports whether update's PRESENT fields are all changes
// claude accepts on a stream it is already reading — a non-empty Model or Effort
// as a /model or /effort command (#1581), and a bypass REVOCATION as a
// set_permission_mode control request (#1604) — and so the changes
// Pool.UpdateSettings can live-apply without tearing the child down.
//
// The rule keys on what the wire carried, never on merged-vs-previous per field:
// SetSessionSettingsPayload's three fields are omitempty pointers documented as a
// presence contract, so a client changing only the model sends only the model.
// A present YOLO is now read for its VALUE as well as its presence, which is
// still a property of the frame and NOT a diff against stored state — do not
// quietly convert this predicate into a per-field differ. Three consequences are
// deliberate rather than incidental:
//
//   - The split is on the DIRECTION of a bypass change, not its presence (#1595
//     measured both directions live). A revoke goes in-band; an ENABLE takes the
//     restart, because claude gates the escalation on the launch argv and refuses
//     the control request in words, so only a respawn under the recomposed argv
//     can grant it.
//   - A revoke goes in-band INCLUDING when it equals the stored value, which
//     sends a revocation to a child that was never in bypass. Harmless and
//     deliberately not fixed: the delivery is fire-and-forget, the installed argv
//     is the durable half and carries no bypass flag either way, and the child is
//     not in bypass to begin with. It is the same redundancy this path already
//     tolerates for an unchanged model re-sent alongside a new effort.
//   - A present-but-empty Model or Effort takes the restart. Empty means "run at
//     claude's own default", which claudeSettingsArgs expresses by OMITTING the
//     flag; no /model invocation means "revert to default". That reject wins over
//     a revoke in the same frame — and loses nothing, since the restart recomposes
//     argv from the merged settings, so the respawn carries the revocation. No
//     frame can lose a revocation by mixing.
//
// Total over any SettingsUpdate. The nothing-present clause is redundant at the
// one call site, since an all-nil update returns early as a no-op before the
// live-apply, but keeping it makes the predicate independently testable instead
// of dependent on a caller-side invariant.
func inBandDeliverable(update SettingsUpdate) bool {
	if update.YOLO != nil && *update.YOLO {
		return false
	}
	if update.Model == nil && update.Effort == nil && update.YOLO == nil {
		return false
	}
	if update.Model != nil && *update.Model == "" {
		return false
	}
	if update.Effort != nil && *update.Effort == "" {
		return false
	}
	return true
}

// deliverSettingsInBand writes the settings changes implied by update onto id's
// live child stdin as the non-restarting live-apply (#1581): the /model and
// /effort commands as ordinary user turns, and a bypass REVOCATION as a
// set_permission_mode control request via RevokeBypass (#1604). Caller must have
// released p.mu and must have installed the recomposed argv already, so a failed
// delivery still reaches the next spawn.
//
// One send per PRESENT field, not per changed field: a frame carrying an
// unchanged model alongside a new effort re-sends the model, which claude answers
// and discards. That costs one round trip in a case no frame has been observed to
// produce, and per-field diffing is the refinement the presence contract rules
// out. The commands are SEPARATE turns — a two-command message is unmeasured —
// and model → effort → bypass is fixed for the same reason claudeSettingsArgs
// fixes that order: determinism buys testability at no cost.
//
// The bypass clause fires on a revoke and ONLY a revoke. The !*update.YOLO half
// of its guard is unreachable under today's inBandDeliverable, which rejects an
// enable outright; it is kept as the enable-direction fail-safe so this site is
// independently correct rather than dependent on a caller-side invariant — the
// same argument inBandDeliverable's own doc makes for its redundant
// nothing-present clause. TestPool_DeliverSettingsInBand_EnableWritesNothing
// asserts it directly rather than leaving it untested defence.
//
// Two ordering facts a reader will otherwise get wrong:
//
//   - The revocation is a control request, not a queued turn, so it does not pass
//     the turncommit gate and may reach the child AHEAD of a /model turn queued in
//     the same update. That affects arrival order, not the resulting posture: the
//     two settings are independent.
//   - It changes the child's permission mode, not work already dispatched. A tool
//     call in flight when the request arrives is not torn down — the old restart
//     killed the child and so ended it. `interrupt` remains the verb for ending a
//     running turn; #1605 measures the in-flight window live.
//
// Fire-and-forget: every write error is logged and swallowed, which is the
// contract Restart has had on this path since #842. The settings are already
// persisted and the argv already installed, so a failed write loses nothing and
// the caller still sees success. The errors also cannot be classified here —
// internal/sessions must not import internal/streamsup, which would invert the
// Runner seam — and do not need to be: the reachable set (no live child, a
// dropped turn, a wrapped pipe failure) all warrants the same response. The
// dominant case is an evicted session or one between spawns, where nothing is
// degraded, which is why the record is Info and not Warn.
//
// context.Background() is correct here rather than a shortcut: WriteTurn consults
// ctx only for the turncommit gate, and its own doc names the nil-gate case as
// the direct single-turn send this is. Plumbing a ctx would change the
// relay.SettingsUpdater seam signature for no observable gain. conversationID is
// "" — accepted for Runner conformance and unused by the stream runner.
//
// NEVER logged, at any level: the model or effort value, the payload bytes, the
// conversation id. #833 keeps settings values out of the daemon log and this path
// gets no exemption just because the value now travels as command text. The
// bypass record satisfies that rule structurally rather than by discipline —
// RevokeBypass takes no mode, so there is no value it could leak.
func (p *Pool) deliverSettingsInBand(id SessionID, sup Runner, update SettingsUpdate) {
	notDelivered := func(setting string, err error) {
		p.log.Info("sessions: in-band settings command not delivered",
			"session", id, "setting", setting, "err", err)
	}
	send := func(setting, command string) {
		if err := sup.WriteUserTurn(context.Background(), "", []byte(command)); err != nil {
			notDelivered(setting, err)
		}
	}
	if update.Model != nil {
		send("model", "/model "+*update.Model)
	}
	if update.Effort != nil {
		send("effort", "/effort "+*update.Effort)
	}
	if update.YOLO != nil && !*update.YOLO {
		if err := sup.RevokeBypass(); err != nil {
			notDelivered("bypass", err)
		}
	}
}

// JSONLPolicy controls how Pool.Remove handles a session's on-disk JSONL
// transcript file. The zero value (JSONLLeave) preserves the 1.1d-A1 (#94)
// behaviour: the JSONL is untouched.
type JSONLPolicy uint8

const (
	// JSONLLeave leaves the JSONL on disk untouched. Default (zero value).
	JSONLLeave JSONLPolicy = iota
	// JSONLArchive moves the JSONL to <pyry-data-dir>/archived-sessions/<uuid>.jsonl.
	JSONLArchive
	// JSONLPurge deletes the JSONL.
	JSONLPurge
)

// RemoveOptions extends Pool.Remove with disposition policy. The zero value
// behaves identically to the 1.1d-A1 (#94) Pool.Remove: terminate the child,
// drop the registry entry, leave the JSONL on disk.
type RemoveOptions struct {
	// JSONL selects the on-disk JSONL disposition policy. Zero value is
	// JSONLLeave.
	JSONL JSONLPolicy
}

// Remove terminates the named session's claude process (if running), drops
// its registry entry, and applies opts.JSONL to the on-disk transcript file.
//
// opts.JSONL == JSONLLeave (zero value): JSONL untouched (1.1d-A1 behaviour).
// opts.JSONL == JSONLArchive: mv <claudeSessionsDir>/<uuid>.jsonl into
//
//	<pyry-data-dir>/archived-sessions/<uuid>.jsonl. Subdir is auto-created.
//	Errors (wrapping fs.ErrExist) if the destination already exists.
//	Source-absent is a no-op.
//
// opts.JSONL == JSONLPurge: rm <claudeSessionsDir>/<uuid>.jsonl. Source-absent
//
//	is a no-op (per AC: intent is "ensure the file is gone").
//
// Returns ErrSessionNotFound for an unknown id, ErrCannotRemoveBootstrap
// for the bootstrap entry, or ctx.Err() if termination is cancelled. On
// any of those error paths, the in-memory pool, on-disk sessions.json,
// and the JSONL on disk are byte-identical to their prior state — the
// disposition policy is applied AFTER the registry-remove + persist
// completes successfully.
//
// Returns only after the child has exited (modulo ctx cancellation).
//
// Ordering — delete-then-dispose-then-evict. Pool.mu is taken (write) for
// the in-memory delete + saveLocked + JSONL disposition (single critical
// section, single observable transition), then released BEFORE calling
// sess.Evict. The alternative (holding p.mu across Evict) deadlocks:
// Session.transitionTo calls Pool.persist, which reacquires p.mu.
//
// On saveLocked failure, the in-memory delete is rolled back so the disk
// and memory remain consistent (mirrors Pool.Rename); JSONL is untouched
// and the child is not terminated. On disposition failure, the registry
// entry stays removed (already persisted), the disposition error is
// returned, and the child is still terminated via Evict. If both
// disposition and Evict fail, the disposition error wins (it is the new
// failure mode this signature introduces, and the more actionable one).
//
// Lifecycle goroutine after Remove: close(sess.removedCh) signals the
// session's Run loop to exit promptly. Once Evict drives active→evicted
// (or the session is already evicted), Run parks in runEvicted, observes
// the closed removedCh, and returns nil — never through the shared
// errgroup, so no other session or the relay leg is torn down. The
// goroutine and everything it captures are released at Remove time rather
// than surviving until pool shutdown (the bounded-cost note from #94 no
// longer applies).
func (p *Pool) Remove(ctx context.Context, id SessionID, opts RemoveOptions) error {
	p.mu.Lock()
	sess, ok := p.sessions[id]
	if !ok {
		p.mu.Unlock()
		return ErrSessionNotFound
	}
	if sess.bootstrap {
		p.mu.Unlock()
		return ErrCannotRemoveBootstrap
	}
	delete(p.sessions, id)
	if err := p.saveLocked(); err != nil {
		p.sessions[id] = sess
		p.mu.Unlock()
		return err
	}
	disposeErr := p.disposeJSONLLocked(id, opts.JSONL)
	p.mu.Unlock()

	// Signal the lifecycle goroutine to exit. Placed past the saveLocked
	// rollback branch (a rolled-back, still-registered session keeps its
	// goroutine) and off p.mu, next to the already-off-lock Evict call.
	// Closing a write-once channel needs no lock; the close is level-
	// triggered, so ordering it before Evict is safe — the removal is
	// observed the instant Run reaches runEvicted, whether Evict drives the
	// active→evicted transition or the session is already parked there.
	// Single-close is structural: Remove is single-shot per id (a second
	// Remove finds no p.sessions[id] under p.mu and returns before this).
	close(sess.removedCh)

	evictErr := sess.Evict(ctx)

	// Remove the per-session MCP-enable settings file (#943). Runs after Evict
	// returns — the child is confirmed dead, so no backoff respawn can race the
	// removal into re-reading a deleted path. Best-effort in the sense that a
	// failure does not fail the Remove, but no longer optional: since #1518 the
	// file lives in the daemon data dir, where no reaper collects it, so this
	// removal is what keeps the on-disk set bounded by live sessions.
	if sess.settingsPath != "" {
		_ = os.Remove(sess.settingsPath)
	}

	if disposeErr != nil {
		return disposeErr
	}
	return evictErr
}

// dataDir returns the per-instance pyry data directory — the parent of the
// registry path. Empty when persistence is disabled (test-only mode).
func (p *Pool) dataDir() string {
	if p.registryPath == "" {
		return ""
	}
	return filepath.Dir(p.registryPath)
}

// disposeJSONLLocked applies policy to the named session's on-disk JSONL.
// Caller MUST hold p.mu (write).
//
// Returns nil for JSONLLeave or when claudeSessionsDir is empty (test or
// disabled JSONL plumbing). For JSONLArchive without a registryPath the
// data-dir is unresolvable and an error is returned — production callers
// always set RegistryPath; this guard exists so a misconfigured test fails
// loudly rather than archiving into a relative path.
//
// Source-absent is a success no-op for both archive and purge (intent is
// "move it if there is one" / "ensure it's gone"). Archive errors when the
// destination already exists; the error wraps fs.ErrExist so a future CLI
// can offer a --force UX with errors.Is.
func (p *Pool) disposeJSONLLocked(id SessionID, policy JSONLPolicy) error {
	if policy == JSONLLeave {
		return nil
	}
	if p.claudeSessionsDir == "" {
		return nil
	}
	src := filepath.Join(p.claudeSessionsDir, string(id)+".jsonl")
	switch policy {
	case JSONLPurge:
		err := os.Remove(src)
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	case JSONLArchive:
		if _, err := os.Stat(src); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("sessions: archive stat source: %w", err)
		}
		dataDir := p.dataDir()
		if dataDir == "" {
			return errors.New("sessions: archive requires a registry path")
		}
		archiveDir := filepath.Join(dataDir, "archived-sessions")
		dst := filepath.Join(archiveDir, string(id)+".jsonl")
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("sessions: archive destination exists: %s: %w", dst, fs.ErrExist)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("sessions: archive stat destination: %w", err)
		}
		if err := os.MkdirAll(archiveDir, 0o700); err != nil {
			return fmt.Errorf("sessions: archive mkdir: %w", err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("sessions: archive rename: %w", err)
		}
		return nil
	default:
		// Forward-compat: unknown future policy ≡ Leave.
		return nil
	}
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

// DefaultSettings returns the bootstrap session's currently-persisted
// SessionSettings (model, effort, YOLO) plus whether a bootstrap session exists
// to read from. When none exists (the embedded evicted-bootstrap host, or a
// zero-value &Pool{} map-miss) it returns (SessionSettings{}, false) so a
// consumer falls back to the daemon defaults; there is no error path.
//
// It resolves p.bootstrap fresh on each call — mirroring Default — so it stays
// correct across a session-id rotation (RotateID flips p.bootstrap under the
// write lock). SessionSettings is a value type, so the return is a snapshot
// copy with no aliasing of the pool's live field.
//
// Concurrency: reads sess.settings under p.mu (RLock); does NOT take
// Session.lcMu — settings is a p.mu-guarded field (the writer UpdateSettings
// holds p.mu write; the other reader saveLocked holds p.mu), so there is no
// torn read. This accessor is unavoidable: settings is a private field read
// only under Pool.mu, so a consumer outside internal/sessions cannot reach it
// (SessionInfo/List does not carry it).
func (p *Pool) DefaultSettings() (SessionSettings, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	sess := p.sessions[p.bootstrap]
	if sess == nil {
		return SessionSettings{}, false
	}
	return sess.settings, true
}

// SettingsFor returns the named session's currently-persisted SessionSettings
// (model, effort, YOLO), or ErrSessionNotFound if the pool holds no session
// under that id. It is the read half matching UpdateSettings' per-session write:
// before it, an outside caller could change any session's settings but could
// read only the bootstrap's, via DefaultSettings.
//
// SessionSettings is a value type, so the return is a snapshot copy and not a
// lease on pool state — no caller can mutate a session through it, and the copy
// may be one concurrent UpdateSettings stale by the time it is read. That
// staleness is inherent to any lock-releasing accessor and is the contract
// DefaultSettings and mintSettings already carry.
//
// The empty id is deliberately NOT special-cased: it misses p.sessions like any
// other unknown id and gets ErrSessionNotFound. That puts this method on
// UpdateSettings' side of an in-package disagreement and declines Lookup's
// convention, where "" resolves to the bootstrap. Read and write must agree, or
// a caller passing "" reads the bootstrap's settings and writes nowhere. The
// bootstrap is registered under a real UUID, so "" is never a key.
//
// No id validation: unlike writeMCPSettings, the id here names no file and never
// leaves the map lookup, so a malformed id is a map miss — already the correct
// answer. The error is returned bare rather than wrapped with the caller's id
// (as UpdateSettings returns it, and unlike ambiguousError, which echoes only
// ids already resident in the pool), so a hostile or malformed id cannot be
// reflected into a log line or wire frame a consumer builds from the error.
//
// Read-modify-write warning for consumers: a caller that reads this triple and
// then calls UpdateSettings must send only the fields it intends to change.
// Echoing the whole snapshot back re-asserts a YOLO posture an operator may have
// cleared in the interval — SettingsUpdate's pointer-per-field presence contract
// makes sending one field the easy path, and this is the trap mintSettings
// avoids by rebuilding its literal field by field.
//
// Concurrency: MUST be called with p.mu unheld — one RLock acquisition per call,
// with no second lock and no delegation to DefaultSettings, whose own RLock
// would double-acquire. Go's RWMutex is not reentrant, so either shape
// self-deadlocks as soon as a writer queues between the two acquisitions; this
// is the hazard mintSettings' docstring already records. Reads sess.settings
// under p.mu (RLock) and deliberately NOT Session.lcMu — settings is a
// p.mu-guarded field (writer UpdateSettings holds p.mu write; the other reader
// saveLocked holds p.mu), so there is no torn read.
//
// DefaultSettings is not made redundant by this and must not be rewritten to
// call it: SettingsFor(p.BootstrapID()) is TWO acquisitions with a rotation
// window between them — RotateID can flip p.bootstrap under the write lock after
// the first returns — so the composed form can read a session that is no longer
// the bootstrap. DefaultSettings resolves p.bootstrap and reads settings under a
// single acquisition and stays the atomic way to ask for the bootstrap's
// settings.
func (p *Pool) SettingsFor(id SessionID) (SessionSettings, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	sess, ok := p.sessions[id]
	if !ok {
		return SessionSettings{}, ErrSessionNotFound
	}
	return sess.settings, nil
}

// mintSettings returns the SessionSettings a freshly-minted session starts
// with: the operator's configured model and effort level, sourced from the
// bootstrap session's persisted settings so a new conversation does not fall
// back to claude's own defaults.
//
// Built field by field rather than by copying DefaultSettings' return, so YOLO
// is excluded structurally rather than by a clearing statement someone could
// later delete: a phone-granted --dangerously-skip-permissions can never reach
// a minted session's argv, and any field added to SessionSettings in future is
// likewise not inherited until someone opts it in. That is the fail-closed
// direction and it is the same reasoning Revive's docstring records (#1487).
//
// The existence bool is discarded deliberately. DefaultSettings already returns
// the zero SessionSettings when there is no bootstrap, so an early return for
// that case would be a second return site emitting byte-identical output — the
// no-configuration argv falls out of the zero value, not out of a branch.
//
// Concurrency: MUST be called with p.mu unheld. DefaultSettings takes
// p.mu.RLock() and Go's RWMutex is not reentrant, so a call from inside a
// critical section self-deadlocks the pool. Both call sites (CreateIn,
// GetOrCreateIn) build the session before taking p.mu.
func (p *Pool) mintSettings() SessionSettings {
	boot, _ := p.DefaultSettings()
	return SessionSettings{
		Model:  boot.Model,
		Effort: boot.Effort,
	}
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

// Run blocks until ctx is cancelled, supervising every session in the pool,
// running the rotation watcher (when ClaudeSessionsDir is set) and the
// conversations auto-archive sweep loop (when ConversationsRegistry is set)
// alongside it. errgroup ties the goroutines together: cancellation
// propagates, and Wait returns the first non-nil error.
//
// Phase 1.1+ extends the fan-out to one supervisor.Run goroutine per session
// — the errgroup wrapper introduced here is the extension point.
func (p *Pool) Run(ctx context.Context) error {
	p.mu.RLock()
	bootstrap := p.sessions[p.bootstrap]
	dir := p.claudeSessionsDir
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

	if dir != "" {
		w, err := rotation.New(rotation.Config{
			Dir:    dir,
			Probe:  newProbe(p.log),
			Logger: p.log,
			Snapshot: func() []rotation.SessionRef {
				return p.snapshotForRotation()
			},
			IsAllocated: func(id string) bool {
				return p.IsAllocated(SessionID(id))
			},
			OnRotate: func(oldID, newID string) error {
				return p.onRotate(SessionID(oldID), SessionID(newID))
			},
		})
		if err != nil {
			// AC: pyry startup proceeds without a watcher rather than
			// failing.
			p.log.Warn("rotation watcher disabled", "err", err)
		} else {
			g.Go(func() error { return w.Run(gctx) })
		}
	}

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

// Create mints a fresh session, persists it, and brings it up under the
// cap-aware spawn path. Returns the new SessionID and an error.
//
// The returned id is empty only when the failure happened before (or during)
// the persist step — in that case nothing is on disk and nothing in memory
// changed. A non-empty id means the registry entry is on disk; the caller
// uses errors.Is to decide whether the failure was ErrPoolNotRunning (no
// lifecycle goroutine yet — fix and retry by calling Run + Activate) or an
// Activate error (lifecycle goroutine is running and may transition to
// active anyway — see ctx-cancellation note below).
//
// Sequence: NewID → build *Session in stateEvicted → register under p.mu and
// persist (rollback the in-memory entry on save failure) → register the UUID
// in the rotation skip-set → schedule sess.Run on Pool.Run's errgroup via
// supervise → call Pool.Activate (cap-aware) to wake the lifecycle goroutine.
//
// We persist BEFORE activating: a save failure with claude already running
// would leave an unsupervised orphan whose JSONL has no on-disk record. A
// registry-only entry that didn't activate is benign — the same shape as a
// session that ran and then idled out, recoverable on next Activate.
//
// Concurrency: safe for concurrent use. Each call serialises through Pool.mu
// briefly (registration + persist), then runs supervise/Activate off-lock
// under the cap path's existing capMu serialisation.
//
// Note: if ctx is cancelled after supervise succeeded but before Activate
// returns, the lifecycle goroutine still observes the buffered activate
// signal and may transition to active anyway. The lifecycle goroutine
// respects the pool's run-context, not the caller's. Tests should not
// assume "Activate returned ctx.Err → claude is not running."
func (p *Pool) Create(ctx context.Context, label string) (SessionID, error) {
	return p.CreateIn(ctx, label, "")
}

// CreateIn is Create with an explicit per-session spawn working directory.
// spawnDir == "" spawns in the shared template workdir (tpl.WorkDir),
// byte-identical to Create. A non-empty spawnDir is used verbatim and is NOT
// validated, canonicalised, or trust-checked by the pool — callers supply a
// pre-resolved path (see #685). An inaccessible directory surfaces at spawn
// time via the supervisor's existing chdir-failure path, not here.
//
// Otherwise identical to Create: see its docstring for the full create
// sequence, concurrency, and error semantics.
func (p *Pool) CreateIn(ctx context.Context, label, spawnDir string) (SessionID, error) {
	id, err := NewID()
	if err != nil {
		return "", fmt.Errorf("sessions: create id: %w", err)
	}

	// A minted session starts at the operator's configured model and effort
	// (#1575). mintSettings must be read here, above p.mu.Lock — it takes
	// p.mu.RLock internally and the mutex is not reentrant.
	sess, err := p.buildSession(id, label, spawnDir, p.mintSettings())
	if err != nil {
		return "", err
	}

	// Persist before activating: if saveLocked fails, roll the in-memory
	// registration back so a retry sees a clean slate. See docstring
	// rationale ("save failure with claude running would leave an
	// unsupervised orphan").
	p.mu.Lock()
	p.sessions[id] = sess
	if err := p.saveLocked(); err != nil {
		delete(p.sessions, id)
		p.mu.Unlock()
		// Discarding the session discards its settings file too (#1518): in the
		// data dir that orphan is permanent, and id came fresh from NewID above so
		// it is never reused. Safe HERE and deliberately not in materialise's
		// rollbacks, where the id is caller-supplied and a concurrent same-id
		// builder may already own the byte-identical path.
		_ = os.Remove(sess.settingsPath)
		return "", err
	}
	p.mu.Unlock()

	// Prime the rotation watcher's skip-set BEFORE the lifecycle goroutine
	// can spawn claude — claude opening the JSONL would otherwise look like
	// a /clear rotation. allocatedTTL (30s) is well clear of the sub-second
	// spawn path.
	p.RegisterAllocatedUUID(id)

	if err := p.supervise(sess); err != nil {
		return id, err
	}
	if err := p.Activate(ctx, id); err != nil {
		return id, err
	}
	return id, nil
}

// buildSession constructs a per-session supervisor and *Session for a given
// (id, label). Touches no Pool state (the returned Session is in stateEvicted
// and its lifecycle goroutine has not yet been scheduled). Caller is
// responsible for registering it in p.sessions, persisting, and supervising.
//
// Used by Pool.CreateIn (UUID-minted) and Pool.GetOrCreateIn (caller-supplied
// id). Sharing this helper keeps the supervisor.Config + Session field
// shape in one place.
//
// spawnDir is the per-session spawn working directory: when non-empty it is
// used verbatim as supervisor.Config.WorkDir; when empty the child spawns in
// the shared template workdir (tpl.WorkDir), today's behaviour. The pool does
// NOT validate, canonicalise, or trust-check spawnDir — callers supply a
// pre-resolved path (see #685).
//
// settings are the per-session model / effort / YOLO applied to the spawn argv
// (#833) and stored on the returned Session. The zero value appends no flags,
// so an unconfigured spawn's argv carries none. CreateIn and GetOrCreateIn pass
// mintSettings — the operator's configured model and effort, never the bypass
// (#1575). Pool.Revive is the one caller that still passes the zero value, so a
// phone-granted bypass cannot survive a daemon restart (#1487).
func (p *Pool) buildSession(id SessionID, label, spawnDir string, settings SessionSettings) (*Session, error) {
	tpl := p.sessionTpl
	// The per-session --settings file pre-approves the project's MCP servers so
	// claude's startup enablement modal never wedges the readiness check (#943).
	// It joins spawnBase (below) rather than claudeSettingsArgs so it survives
	// every recompose (backoff restart, #842 live restart). Removed in Pool.Remove
	// after the child is confirmed dead.
	settingsPath, err := writeMCPSettings(p.registryPath, id)
	if err != nil {
		return nil, fmt.Errorf("sessions: write mcp settings: %w", err)
	}
	// Same reasoning as Pool.New's cleanup defer (#1518): in the data dir an
	// orphan is permanent, so an error return past this point must take the file
	// with it. Only one such return exists today; the defer shape means a future
	// one is covered without anyone remembering to add a removal.
	built := false
	defer func() {
		if !built {
			_ = os.Remove(settingsPath)
		}
	}()
	// base is the settings-free argv (template, resume suffix, and the immutable
	// --settings pair); full appends the model/effort/YOLO suffix. Storing base on
	// the Session lets a live restart recompose full argv from the persisted
	// settings (#842). Clone before the second append so base and full never share
	// a backing array.
	base := append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))
	base = append(base, "--settings", settingsPath)
	args := append(slices.Clone(base), claudeSettingsArgs(settings)...)
	workDir := tpl.WorkDir
	if spawnDir != "" {
		workDir = spawnDir
	}

	supCfg := RunnerConfig{
		ClaudeBin: tpl.ClaudeBin,
		WorkDir:   workDir,
		// #1108 seam: the same id already baked into ClaudeArgs as
		// "--session-id <id>" is also exposed here so the stream RunnerFactory
		// (#1109) can read it at construction.
		SessionID:      string(id),
		ClaudeArgs:     args,
		Logger:         p.log,
		BackoffInitial: tpl.BackoffInitial,
		BackoffMax:     tpl.BackoffMax,
		BackoffReset:   tpl.BackoffReset,
	}
	sup, err := p.newRunner(supCfg)
	if err != nil {
		return nil, fmt.Errorf("sessions: create runner: %w", err)
	}

	idleTimeout := tpl.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = p.idleTimeoutDefault
	}

	now := time.Now().UTC()
	sess := &Session{
		id:           id,
		sup:          sup,
		log:          p.log,
		label:        label,
		createdAt:    now,
		lastActiveAt: now,
		bootstrap:    false,
		settings:     settings,
		spawnBase:    base,
		settingsPath: settingsPath,
		pool:         p,
		idleTimeout:  idleTimeout,
		removedCh:    make(chan struct{}),
		lcState:      stateEvicted,
		activeCh:     make(chan struct{}),
		evictedCh:    closedChan(),
		activateCh:   make(chan struct{}, 1),
		evictCh:      make(chan struct{}, 1),
	}
	built = true
	return sess, nil
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

// snapshotForRotation translates Pool.Snapshot into the primitive-typed
// shape rotation.Watcher expects. Lives on Pool so the conversion happens
// in exactly one place.
func (p *Pool) snapshotForRotation() []rotation.SessionRef {
	snap := p.Snapshot()
	out := make([]rotation.SessionRef, len(snap))
	for i, s := range snap {
		out[i] = rotation.SessionRef{ID: string(s.ID), PID: s.PID}
	}
	return out
}

// RegisterAllocatedUUID records that id is a UUID pyry just minted (and is
// about to write to disk via claude --session-id). The watcher consults this
// set on every CREATE; matching entries skip the rotation path. Entries are
// consumed on first IsAllocated hit, or pruned after allocatedTTL.
//
// Phase 1.2b-B has no live caller — pyry currently launches claude with
// --continue, so claude picks the UUID. The scaffolding lands now so Phase
// 1.1's `pyry sessions new` and `claude --session-id` wiring is a one-liner.
func (p *Pool) RegisterAllocatedUUID(id SessionID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registerAllocatedUUIDLocked(id)
}

// registerAllocatedUUIDLocked is the lock-held variant of
// RegisterAllocatedUUID. Caller MUST hold p.mu (write). Used by GetOrCreate's
// register-and-supervise critical section to keep skip-set priming inside
// the same atomic step as registry insertion.
func (p *Pool) registerAllocatedUUIDLocked(id SessionID) {
	if p.allocated == nil {
		p.allocated = make(map[SessionID]time.Time)
	}
	p.pruneAllocatedLocked()
	p.allocated[id] = time.Now().Add(allocatedTTL)
}

// IsAllocated reports whether id is in the freshly-allocated set, consuming
// the entry on a true return. Opportunistically prunes expired entries.
// Safe for concurrent use.
func (p *Pool) IsAllocated(id SessionID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneAllocatedLocked()
	deadline, ok := p.allocated[id]
	if !ok {
		return false
	}
	if time.Now().After(deadline) {
		delete(p.allocated, id)
		return false
	}
	delete(p.allocated, id) // consume on first hit
	return true
}

// pruneAllocatedLocked drops expired entries. Caller must hold p.mu (write).
func (p *Pool) pruneAllocatedLocked() {
	now := time.Now()
	for id, d := range p.allocated {
		if now.After(d) {
			delete(p.allocated, id)
		}
	}
}

// saveLocked snapshots the current in-memory sessions into a registryFile and
// writes it atomically. Caller MUST hold p.mu (write). No-op when
// registryPath is empty (test-only persistence-disabled mode).
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
		Sessions: make([]registryEntry, 0, len(p.sessions)),
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
		}
		// omitempty on the JSON tag keeps the stable on-disk shape for
		// the dominant active case — important for the existing
		// idempotent-reload guarantee.
		if state == stateEvicted {
			entry.LifecycleState = state.String()
		}
		reg.Sessions = append(reg.Sessions, entry)
	}
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
func (p *Pool) Activate(ctx context.Context, id SessionID) error {
	sess, err := p.Lookup(id)
	if err != nil {
		return err
	}
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
