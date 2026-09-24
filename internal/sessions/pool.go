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
	systemPromptPath, err := writeSystemPrompt(cfg.RegistryPath, "", systemPromptText)
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
	bootstrapArgs := composeSpawnArgs(base, settings)
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
		convSweepInterval:  sweepInterval,
		activeCap:          cfg.ActiveCap,
		sessionTpl:         cfg.Bootstrap,
		idleTimeoutDefault: cfg.IdleTimeout,
		turnBusy:           cfg.TurnBusy,
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
// saveLocked, Activate) read it directly. The old "no concurrent reader exists"
// claim went stale in #839 and MUST NOT be restored now that #2137 has retired
// the rotation watcher that made it stale: a re-key still races the lifecycle
// goroutines, only from a different goroutine. AdoptAnnouncedID — the sibling
// that carries the announced-reset rotation — re-keys from the follower's
// decorator on the runner's parse goroutine, which runs concurrently with the
// per-session lifecycle goroutines exactly as the watcher's did. lastActiveAt
// shares the same lcMu section. Lock order remains Pool.mu → Session.lcMu.
//
// No production caller remains since #2137 (the watcher was the last one).
// RotateID is kept deliberately: it predates the watcher, is exported, and is
// the seam ~40 test references across five files drive. Removing it is a
// separate call, not a side effect of retiring its last caller.
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
// Unlike RotateForNewSession it does NOT fire a client transition: a ReasonClear
// would mislead clients into thinking the user ran /clear (client notification of
// a self-heal is out of scope). Both methods used to differ on a second axis too
// — whether they primed the freshly-allocated skip-set that suppressed a
// spurious rotation-watcher CREATE — but #2137 retired the watcher and deleted
// the skip-set with it, so that asymmetry no longer exists on either side.
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
// persist (the caller invokes saveLocked). Shared by all four re-key paths so the
// invariant lives in one place: AdoptAnnouncedID (claude's announced reset — the
// only one with a production caller since #2137 retired the rotation watcher),
// RotateForNewSession and RotateBootstrapForSelfHeal (daemon-driven, onto a freshly
// minted id), and RotateID. Lock order remains Pool.mu → Session.lcMu.
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
// The permission posture is stored as TWO fields that can never disagree (#2043).
// validatePermissionUpdate rejects an unrecognised mode
// (ErrUnsupportedPermissionMode) and a mode contradicting a YOLO in the same frame
// (ErrPermissionModeConflict) before anything is mutated; what survives derives
// both fields together:
//
//	update carries                          stored mode        stored YOLO
//	yolo:true                               bypassPermissions  true
//	yolo:false, stored mode is bypass       default            false
//	yolo:false, stored mode is anything else unchanged         false
//	mode bypassPermissions                  bypassPermissions  true
//	any other known mode                    that mode          false
//
// Row three is the one to read twice: a yolo:false must not drag an operator out
// of plan and into default as a side effect of naming a field it was not asked
// about.
//
// After a successful persist of a real change the change is also LIVE-APPLIED to
// the session's supervisor, by one of two mechanisms picked on what the update
// carried — which fields, and for the posture its value too (inBandDeliverable):
//
//   - A change claude accepts on the stream the daemon already holds open is
//     delivered IN-BAND (#1581, #1604, #2043, #2066, #2280): model through
//     set_model, effort through an ordinary /effort user turn, and ANY of the six storable postures as a
//     set_permission_mode control request, so the child is neither terminated nor
//     respawned and its transcript survives. That branch still installs the
//     recomposed argv, via SetSpawnArgs — Restart's swap half — because skipping
//     the install would let the operator's change silently revert on the next
//     crash-respawn or evict → Activate. Fire-and-forget; see
//     deliverSettingsInBand.
//   - Every other change keeps the live restart (#842): the argv is recomposed
//     from the persisted settings and, if a child is running, that child is
//     killed so the supervisor relaunches it — resuming the conversation — under
//     the new settings. Since #2066 this is the path a model or effort cleared back
//     to claude's own default takes, and nothing else.
//
// The posture split is on the POSTURE ASKED FOR, not on presence, and it is
// measured rather than assumed. #1595 and #2041 measured the five non-escalating
// modes on a held-open stream; #2060 measured the ESCALATION the same way, on a
// child launched with --dangerously-skip-permissions, which #2065 made every child.
// So all six now apply to the running child with no respawn. Before #2066 the
// escalation was the exception, because claude refused it in words on a child
// launched without the flag and only a relaunch under a recomposed argv could grant
// it; that asymmetry is what this ticket removed, and every caller that modelled it
// can stop.
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
	if err := validatePermissionUpdate(update); err != nil {
		p.mu.Unlock()
		return err
	}
	merged := sess.settings
	if update.Model != nil {
		merged.Model = *update.Model
	}
	if update.Effort != nil {
		merged.Effort = *update.Effort
	}
	// The posture's two fields move together, and the mode arm subsumes the YOLO
	// arm: validatePermissionUpdate has already refused a frame carrying both
	// with values that disagree, so when both are present they derive the same
	// pair and running the mode arm alone is not a precedence rule.
	switch {
	case update.PermissionMode != nil:
		merged.PermissionMode = *update.PermissionMode
		merged.YOLO = merged.PermissionMode == permissionModeBypass
	case update.YOLO != nil:
		merged.YOLO = *update.YOLO
		// canonicalPermissionMode is what makes a revoke land in default while
		// leaving a non-bypass mode alone: a yolo:false must not drag an
		// operator out of plan as a side effect of naming a field it was not
		// asked about.
		merged.PermissionMode = canonicalPermissionMode(merged.PermissionMode, merged.YOLO)
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
	//
	// The posture install (#2064) sits ABOVE the split because it is the one thing
	// BOTH branches need and only one of them would otherwise get. The runner asserts
	// its stored posture to every child it spawns, and neither branch rebuilds the
	// runner — so a construction-time value goes stale here, and a later
	// crash-respawn would re-assert the posture the session had at daemon start. The
	// escalation branch below is the half that makes the placement necessary rather
	// than tidy: it never calls SetPermissionMode, so an install folded into
	// deliverSettingsInBand would miss exactly the transition that most needs it.
	//
	// Unconditional rather than gated on update.PermissionMode/YOLO being present:
	// merged.PermissionMode is the session's stored posture whatever this update
	// named, so re-installing it is idempotent and a future branch cannot forget it.
	// It is non-blocking and takes no Pool lock, so it is safe here, past the unlock.
	sup.SetSpawnPermissionMode(merged.PermissionMode)
	if inBandDeliverable(update) {
		sup.SetSpawnArgs(newArgs)
		p.deliverSettingsInBand(id, sup, update, merged)
		return nil
	}
	// This is a LIVE PRODUCTION CALLER of Restart, and #2066 did not remove it —
	// though it did remove the reason the previous revision gave. The bypass posture
	// has now moved off this branch ENTIRELY: both spellings of an enable go in-band
	// above, so the escalation reaches Restart only in combination with something
	// else. What keeps this call reachable is the OTHER half of inBandDeliverable's
	// reject set: a Model or Effort explicitly cleared to "" means "run at claude's
	// own default", which claudeSettingsArgs expresses by omitting the flag. Pyrycode
	// deliberately retains restart semantics for an explicit clear, so it is applied by relaunching
	// under the recomposed argv. #1574 may still NOT delete Restart, and a future
	// slice that gives the empty value an in-band form is the one that inherits the
	// question.
	// Refuse writes before the kill (#1513). Inside this branch and NOT above the
	// split: the in-band branch tears no child down, so an arm hoisted above it would
	// refuse turns for every live settings change until the next respawn. It is
	// non-blocking, takes no Pool lock and makes no call-out, so it is safe here past
	// the unlock beside the two SetSpawn* calls, and it cannot delay the Restart.
	sup.BeginTeardown()
	sup.Restart(newArgs)
	return nil
}

// UpdateDormantSettings merges update's Model and Effort into id's dormant
// registry entry and persists it, or returns ErrSessionNotFound if this pool
// holds no dormant entry under that id. It is UpdateSettings' dormant half: the
// two partition the ids the daemon has a record of, on the same basis
// DormantSettingsFor and SettingsFor partition them.
//
// It exists so set_session_settings can land on a conversation this daemon has
// not materialised (#2463). Pool.New materialises only the bootstrap, so after a
// restart that is EVERY conversation until its first message revives it, and a
// model or effort chosen before that message was refused with session.not_found
// while the menus the client had just rendered said otherwise (#2449 fixed the
// read and made this reachable).
//
// A SECOND WRITE rather than a fallback folded into UpdateSettings, for the
// reason DormantSettingsFor records one layer up and one more of its own.
// Folding it in would change what ErrSessionNotFound means for every other caller
// of the live write, several of which use exactly that answer to decide a session
// is not addressable. And UpdateSettings' body past its persist is entirely about
// a live session — the recomposed argv, the posture install, the in-band
// delivery, the supervisor capture — none of which has a dormant analogue.
//
// IT MATERIALISES NOTHING, deliberately, and that is a contract rather than an
// implementation detail. #2449 AC 3 made the settings READ materialise nothing so
// that N channel activations cannot become N sessions; the write rides the same
// frame family on the same restart edge, and a client that re-asserts its footer
// state on activation would otherwise wake every channel it touched. Reviving
// here is also not available on its own terms: Pool.Revive needs a spawn
// directory to re-validate through the caller's $HOME confinement, this seam is
// keyed by SESSION rather than by conversation, and a session no conversation
// binds has no Cwd at all.
//
// THE POSTURE IS REFUSED, NOT PERSISTED, and the refusal is honesty rather than
// caution. Both readers of p.dormant build the posture structurally from Model
// and Effort alone — revivedSettings never looks at the entry's yolo or
// permission_mode, and DormantSettingsFor derives the default from the same two
// fields — so a persisted posture is invisible to both, and accepting one would
// report success for a change the very next read contradicts. Making it visible
// instead would mean teaching the revive to read a persisted posture back, and
// the disk cannot tell a posture granted after a restart from one granted before
// it, so that would resurrect exactly the bypass a restart revokes (#1487, ADR
// 035 as amended by #2448). Carrying a NON-ESCALATING mode across a revive is a
// real option and a security decision of its own; it is not this method's.
//
// The exclusion is doubled rather than singled: the refusal below returns before
// any mutation, AND the merge names Model and Effort literally, so a posture
// could not reach the entry even if the guard were deleted. That is mintSettings'
// and revivedSettings' recorded discipline — a field added to registryEntry later
// is not carried until someone opts it in — applied to a write.
//
// VALIDATING THE VALUES IS NOT THIS METHOD'S JOB, exactly as it is not
// UpdateSettings'. It operates on operator-trusted input. The relay handler owns
// the charset and length shape check for Model and the closed enum for Effort,
// and for a non-empty model cmd/pyry's settingsUpdaterAdapter then owns the
// membership check against the retained published vocabulary before this method
// can write anything. A written Model becomes the revived child's --model, so a
// caller that skips that gate is handing an unvalidated value to an argv sink.
//
// Carried over from UpdateSettings' shape: a no-op returns success without
// rewriting the registry (compared on the two scalar fields rather than on the
// whole entry, which embeds time.Time values whose == compares representation
// rather than instant), and a failed save restores the previous entry.
//
// Concurrency: MUST be called with p.mu unheld — one Lock acquisition, no
// delegation to another locking accessor, UpdateSettings' contract verbatim. The
// lookup, the merge and the save all run inside that one critical section, so
// there is no check-then-mutate gap of this method's own. Never takes
// Session.lcMu; p.dormant is a p.mu-guarded field like sessions and label.
//
// ONE WINDOW OUTSIDE IT, bounded by p.dormant only ever shrinking: a caller
// composing this after a live write (settingsUpdaterAdapter) can have a revive
// land between the two. The id can only move live-ward, so this method finds a
// CLEAN MISS rather than a torn entry, and answers ErrSessionNotFound. That
// refusal is correct rather than merely safe: the settings were applied to
// nothing. No retry is attempted — the operator's next pick reaches the now-live
// session through the live write.
//
// The SECOND window this doc used to name is closed (#2492). Pool.Revive
// evaluated revivedSettings as an ARGUMENT to materialise, so that read's RLock
// was released before materialise took the write lock and retired the entry, and
// a write landing in the gap was acknowledged and then dropped — the session
// materialised under the value read before it. materialise now takes its settings
// as a source and evaluates it inside the critical section that retires the
// entry, so a write reaching this method before that section is CARRIED by the
// revived session and one reaching it after gets ErrSessionNotFound above. Those
// were always the two outcomes this seam's docstrings reasoned about; what is
// gone is the third.
//
// No id validation and no logging, DormantSettingsFor's posture verbatim: the id
// names no file and never leaves the map lookup, so a malformed id is a map miss
// — already the correct answer — and both errors are returned bare rather than
// wrapped with it, so a hostile id cannot be reflected into a log line or a wire
// frame a consumer builds from the error.
func (p *Pool) UpdateDormantSettings(id SessionID, update SettingsUpdate) error {
	p.mu.Lock()
	entry, ok := p.dormant[id]
	if !ok {
		p.mu.Unlock()
		return ErrSessionNotFound
	}
	if update.YOLO != nil || update.PermissionMode != nil {
		p.mu.Unlock()
		return ErrDormantPostureUnsupported
	}
	model, effort := entry.Model, entry.Effort
	if update.Model != nil {
		model = *update.Model
	}
	if update.Effort != nil {
		effort = *update.Effort
	}
	if model == entry.Model && effort == entry.Effort {
		p.mu.Unlock()
		return nil
	}
	prev := entry
	entry.Model, entry.Effort = model, effort
	p.dormant[id] = entry
	if err := p.saveLocked(); err != nil {
		p.dormant[id] = prev
		p.mu.Unlock()
		return err
	}
	p.mu.Unlock()
	return nil
}

// validatePermissionUpdate refuses the two posture updates that have no correct
// reading, BEFORE Pool.UpdateSettings mutates anything, so a rejected frame
// leaves the stored settings, the registry file and the running child
// byte-identical to their prior state:
//
//   - a mode outside permissionModeKnown, including the empty string — unlike
//     Model and Effort, where "" means "omit the flag, run at claude's own
//     default", the default posture is a NAMEABLE mode, so an explicit "" has no
//     reading. Refusing here is what keeps an unrecognised value out of the
//     registry, and therefore out of every argv composed from it later and away
//     from the child.
//   - a mode and a YOLO bit that contradict each other. Neither precedence is
//     fail-safe in both directions, so neither is chosen; see
//     ErrPermissionModeConflict.
//
// Neither error carries the rejected value (#833). Total over any
// SettingsUpdate: an update naming no mode is trivially valid, so the caller can
// invoke it unconditionally.
func validatePermissionUpdate(update SettingsUpdate) error {
	if update.PermissionMode == nil {
		return nil
	}
	mode := *update.PermissionMode
	if !permissionModeKnown(mode) {
		return ErrUnsupportedPermissionMode
	}
	if update.YOLO != nil && (mode == permissionModeBypass) != *update.YOLO {
		return ErrPermissionModeConflict
	}
	return nil
}

// inBandDeliverable reports whether update's PRESENT fields are all changes
// claude accepts on a stream it is already reading — a non-empty Model through a
// set_model control request, a non-empty Effort as an /effort command, and any of the SIX storable postures —
// the five non-escalating ones and, since #2066, the escalation — as a
// set_permission_mode control request (#1604, #2043, #2066) — and so the changes
// Pool.UpdateSettings can live-apply without tearing the child down.
//
// The rule keys on what the wire carried, never on merged-vs-previous per field:
// SetSessionSettingsPayload's fields are omitempty pointers documented as a
// presence contract, so a client changing only the model sends only the model.
// A present YOLO or PermissionMode is read for its VALUE as well as its presence,
// which is still a property of the frame and NOT a diff against stored state — do
// not quietly convert this predicate into a per-field differ. What #2043 adds is
// that one posture is expressed by TWO fields, so an update naming either is an
// update to the posture; the routing still reads the frame, and it is
// deliverSettingsInBand that resolves which posture RESULTS. Three consequences
// are deliberate rather than incidental:
//
//   - The split is on the POSTURE ASKED FOR, not on the presence of a posture
//     field (#1595 and #2041 measured the five live, #2060 the escalation). It no
//     longer splits on DIRECTION, which is #2066's change: all six postures go in
//     band, an escalation included, whether spelled as yolo:true or as the
//     bypassPermissions mode. Claude used to gate the escalation on the launch argv
//     and refuse the control request in words, so only a respawn under the
//     recomposed argv could grant it; #2065 put that flag on every argv and #2060
//     measured claude accepting the re-escalation on such a child at 2.1.239. Both
//     spellings had to open together — internal/relay's validPermissionMode refuses
//     the mode string, so a mobile client can only ever send the bit, and opening
//     the mode clause alone would have shipped the change as a no-op for every
//     relay session.
//   - A revoke goes in-band INCLUDING when it equals the stored value, which
//     sends a revocation to a child that was never in bypass. Harmless and
//     deliberately not fixed: the delivery is fire-and-forget, the installed argv
//     is the durable half and composes the same non-escalated posture either way,
//     and the child is not in bypass to begin with. (Before #2065 that read "and
//     carries no bypass flag either way"; every argv carries the flag now, and it
//     is the --permission-mode pair beside it, plus the spawn-time write, that
//     carry the revocation. The redundancy is unchanged; only its spelling is.)
//     It is the same redundancy this path already
//     tolerates for an unchanged model re-sent alongside a new effort.
//   - A present-but-empty Model or Effort takes the restart. Empty means "run at
//     claude's own default", which claudeSettingsArgs expresses by OMITTING the
//     flag; this contract does not use control-layer reset spellings. That reject wins over
//     ANY posture change in the same frame — an escalation as much as a revoke —
//     and loses nothing, since the restart recomposes argv from the merged
//     settings, so the respawn carries the posture. No frame can lose a posture
//     change by mixing. Since #2066 these two clauses are also the ONLY surviving
//     route from an escalation to the restart branch, which is what keeps
//     Pool.UpdateSettings' Restart call reachable at all.
//
// Total over any SettingsUpdate. The nothing-present clause is redundant at the
// one call site, since an all-nil update returns early as a no-op before the
// live-apply, but keeping it makes the predicate independently testable instead
// of dependent on a caller-side invariant.
func inBandDeliverable(update SettingsUpdate) bool {
	// Refused by NON-MEMBERSHIP, so no spelling is named here and every
	// unanticipated one is refused for free — the shape the writer-side allow-list
	// uses for the same reason. permissionModeKnown rather than permissionModeInBand
	// is #2066's one-word routing open: every posture this daemon can STORE is now
	// one it can deliver in band, so the predicate that answers "storable" answers
	// this too. permissionModeInBand is deliberately NOT widened — three other
	// callers read it for a different question and must keep excluding the
	// escalation; see its own doc. An unrecognised mode never reaches this predicate
	// in production (validatePermissionUpdate rejects the frame outright), so this
	// arm is defence rather than a live route.
	if update.PermissionMode != nil && !permissionModeKnown(*update.PermissionMode) {
		return false
	}
	if update.Model == nil && update.Effort == nil && update.YOLO == nil && update.PermissionMode == nil {
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
// live child stdin as the non-restarting live-apply (#1581): model as a set_model
// control request, effort as an ordinary /effort user turn, and the resulting permission POSTURE
// as a set_permission_mode control request via SetPermissionMode (#1604 built the
// revoke-only form; #2043 generalised it). Caller must have released p.mu and must
// have installed the recomposed argv already, so a failed delivery still reaches
// the next spawn. merged is the posture that update RESULTS in, computed under
// p.mu by the caller, so this site reads no pool state.
//
// One send per PRESENT field, not per changed field: a frame carrying an
// unchanged model alongside a new effort re-sends the model, which claude answers
// and discards. That costs one round trip in a case no frame has been observed to
// produce, and per-field diffing is the refinement the presence contract rules
// out. The commands are SEPARATE turns — a two-command message is unmeasured —
// and model → effort → posture is fixed for the same reason claudeSettingsArgs
// fixes that order: determinism buys testability at no cost.
//
// The posture clause is EXACTLY ONE send for an update naming either posture
// field, and that arithmetic is the point (#2043's AC3). The posture is expressed
// by two fields, so a mode and a YOLO in one frame are one change, not two; the
// pre-#2043 shape — a mode clause beside the old !*update.YOLO → RevokeBypass()
// clause — would emit TWO identical control requests for one revocation, because
// the derivation makes a yolo:false update also carry a non-bypass mode. Dropping
// that clause without this replacement would emit ZERO and quietly end the
// revocation this path performs. The wire bytes of a revocation are unchanged by
// the collapse — RevokeBypass was SetPermissionMode("default") — so what changed
// is which method emits them, and that RevokeBypass is off the Runner seam.
//
// The value delivered is the posture that RESULTS, not the field the frame
// named: a yolo:false against a stored plan re-sends plan, which the child is
// already in. That is the same redundancy this path already tolerates for an
// unchanged model re-sent alongside a new effort, and it is reachable only when
// some other field changed too — an update that changes nothing returns as a
// no-op in UpdateSettings before the live-apply.
//
// THE BYPASS GUARD IS GONE (#2066), and its absence is load-bearing rather than a
// simplification. Until that ticket this site returned early for a merged posture of
// bypassPermissions, as "the second of three independent stops for the escalation,
// after the routing predicate and before the writer's own allow-list". All three
// stops existed because claude gated the escalation on the launch argv and refused
// the control request in words; #2065 removed that gate and #2060 measured the
// acceptance, so keeping any of them would mean the routing sends an escalation this
// site silently drops — an UpdateSettings that reports success and changes nothing.
// TestPool_DeliverSettingsInBand_EnableWritesTheEscalation is the inverse of the
// test that used to assert the guard, and it is the red for a tree that restores it.
//
// What still stops an escalation is upstream and unchanged: internal/relay's
// validPermissionMode refuses bypassPermissions as a mode string, so the wire keeps
// exactly one spelling of the escalation (the YOLO bit), and Pool.UpdateSettings
// gates every stored posture through permissionModeKnown. This site is a delivery,
// not a policy.
//
// Two ordering facts a reader will otherwise get wrong:
//
//   - Model and posture are control requests and do not pass the turncommit gate;
//     effort remains a queued turn. The fixed call order is model, effort, posture,
//     so a blocked effort send also delays the posture write from this call.
//   - It changes the child's permission mode, not work already dispatched. A tool
//     call in flight when the request arrives is not torn down — the old restart
//     killed the child and so ended it. `interrupt` remains the verb for ending a
//     running turn. The in-flight window itself — a revoke arriving while a tool
//     call is already dispatched — is not measured live; only the turn boundary is.
//
// THE MODEL SENT IS THE FAMILY ALIAS, NOT THE FRAME'S VALUE (#2447). A mid-session
// pick of a row claude publishes as an exact id — Fable's, or "Haiku 4.5" — would
// otherwise hold the session on a model claude has superseded, so familyAlias
// rewrites it on the way out. This site and claudeSettingsArgs are its complete
// caller set and cannot disagree: Pool.UpdateSettings has already assigned
// update.Model into merged before releasing p.mu, so the live child and the argv
// installed for the next spawn name the same model. What is STORED stays the row
// as picked — the menu matches it by exact equality.
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
// NEVER logged, at any level: the model, effort or permission-mode value, the
// payload bytes, the conversation id. #833 keeps settings values out of the daemon
// log and this path gets no exemption just because the value now travels as
// command text. The posture record satisfies that rule structurally rather than by
// discipline: "permission_mode" is the constant field NAME, and the seam's error
// carries no mode either — Runner.SetPermissionMode refuses an unsupported mode
// with a bare sentinel that does not echo the rejected string, which is why the
// error may be logged verbatim. That second clause is the one that survived #2043
// taking the no-mode revoke shorthand off the seam.
func (p *Pool) deliverSettingsInBand(id SessionID, sup Runner, update SettingsUpdate, merged SessionSettings) {
	notDelivered := func(setting string, err error) {
		p.log.Info("sessions: in-band settings command not delivered",
			"event", "sessions.settings.delivery_err",
			"session", id, "setting", setting, "err", err)
	}
	send := func(setting, command string) {
		if err := sup.WriteUserTurn(context.Background(), "", []byte(command)); err != nil {
			notDelivered(setting, err)
		}
	}
	if update.Model != nil {
		if err := sup.SetModel(familyAlias(*update.Model)); err != nil {
			notDelivered("model", err)
		}
	}
	if update.Effort != nil {
		send("effort", "/effort "+*update.Effort)
	}
	if update.PermissionMode == nil && update.YOLO == nil {
		return
	}
	if err := sup.SetPermissionMode(merged.PermissionMode); err != nil {
		notDelivered("permission_mode", err)
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
	// The other half of #2448's invariant, at the one site that deletes a live
	// session: a removal is final, so the id must not also be sitting in the
	// dormant map waiting to be written back on the next save. materialise
	// retired it when the session was registered, so this is a no-op on every
	// reachable path — it is here because the removal's finality is this
	// function's claim to make, not a property borrowed from a distant call site.
	// Nothing to roll back below: saveLocked writes a live id from its session
	// either way, so restoring p.sessions[id] restores the file unchanged.
	delete(p.dormant, id)
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

	// And this session's appended system-prompt file, on the same terms (#2150).
	// #2093's daemon-scoped file was deliberately NOT removed here — it served
	// every other live session — but this one is this session's alone and carries
	// the operator's own text, so leaving it would leave that text in the data
	// dir past the conversation that owns it.
	if sess.systemPromptPath != "" {
		_ = os.Remove(sess.systemPromptPath)
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

// DormantSettingsFor returns the SessionSettings a Revive of id would
// materialise — the model and effort id's own persisted entry carries, and the
// default posture — or ErrSessionNotFound if this pool holds no dormant entry
// under that id. It is SettingsFor's dormant half: the two partition the ids the
// daemon has a record of, because materialise retires the entry it takes over
// and Remove drops the one it deletes (see Pool.dormant).
//
// It exists so request_session_settings can answer a conversation bound to a
// session this daemon has not materialised (#2449). Pool.New materialises only
// the bootstrap, so after a restart that is EVERY conversation until its first
// message revives it, and the reply's every field sat at its zero: a client's
// model and effort menus went inert and its model label resolved the daemon's
// default row — a model the channel is not on.
//
// A SECOND READ rather than a fallback inside SettingsFor, deliberately. Folding
// it in would change what "not found" means for every other caller of the live
// read, several of which use exactly that answer to decide a session is not
// addressable. Here the two meanings are kept apart and the caller composes them.
//
// NOT Pool.revivedSettings, which is the same map read and cannot be reused as it
// stands: it collapses "no entry" into the zero SessionSettings, which is correct
// for a revive (an id with no record revives to claude's defaults) and wrong for
// a reader, which would then report an unknown bound id beside empty settings.
// The miss is this method's own.
//
// THE POSTURE IS BUILT, NOT CLEARED. The literal names Model and Effort only and
// is then canonicalised, so YOLO false and the default mode are structural:
// mintSettings' and revivedSettings' recorded reason, which is that a clearing
// statement is something a later edit can delete, and that a field added to
// registryEntry is not inherited until someone opts it in. Two things follow that
// a caller depends on. A restart stays a revocation point for a phone-granted
// permission bypass (#1487): a persisted yolo or permission_mode cannot reach
// this reply however the entry was written. And the reported posture is the one
// Revive will actually materialise, because canonicalSettings here is the same
// function buildSession applies to what Revive hands it — an agreement by
// construction rather than by two places spelling the same constant.
//
// Concurrency: MUST be called with p.mu unheld — one RLock acquisition, no
// delegation to another locking accessor. Go's RWMutex is not reentrant, so a
// call from inside a critical section self-deadlocks as soon as a writer queues;
// this is the hazard SettingsFor and mintSettings already record. The returned
// value is a snapshot copy, not a lease: SessionSettings is a value type and
// registryEntry is stored by value in p.dormant.
//
// No id validation and no logging, SettingsFor's posture verbatim: the id names
// no file and never leaves the map lookup, so a malformed id is a map miss —
// already the correct answer — and the error is returned bare rather than
// wrapped with it, so a hostile id cannot be reflected into a log line or a wire
// frame a consumer builds from the error.
func (p *Pool) DormantSettingsFor(id SessionID) (SessionSettings, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entry, ok := p.dormant[id]
	if !ok {
		return SessionSettings{}, ErrSessionNotFound
	}
	return canonicalSettings(SessionSettings{
		Model:  entry.Model,
		Effort: entry.Effort,
	}), nil
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

// mintSettings returns the SessionSettings a freshly-minted session starts
// with: the operator's configured model and effort level, sourced from the
// bootstrap session's persisted settings so a new conversation does not fall
// back to claude's own defaults.
//
// Built field by field rather than by copying DefaultSettings' return, so YOLO
// is excluded structurally rather than by a clearing statement someone could
// later delete: a phone-granted escalation can never reach a minted session's
// POSTURE. (Since #2065 it cannot reach its argv either, because the flag is
// there unconditionally and carries no posture; what the excluded YOLO bit buys
// is that the minted session's stored mode stays non-escalated, so its child is
// written back down to that mode at every spawn.) Any field added in future is
// likewise not inherited until someone opts it in. That is the fail-closed
// direction and it is the same reasoning Revive's docstring records (#1487).
//
// It resolves p.bootstrap and reads its settings DIRECTLY rather than through
// DefaultSettings, which takes an RLock of its own. Since #2492 both settings
// sources are evaluated under the CALLER's p.mu — materialise reads this inside
// the critical section that retires a dormant entry — and Go's RWMutex is not
// reentrant, so a delegating body would self-deadlock as soon as a writer queued.
// The two reads stay in one critical section either way, which is
// DefaultSettings' own atomicity property against a RotateID landing between
// them; it is now the caller's to hold rather than this function's.
//
// The nil-bootstrap arm is that delegation's other half made explicit:
// DefaultSettings used to supply the zero SessionSettings for a pool with no
// bootstrap and this function discarded its existence bool. The no-configuration
// argv still falls out of the zero value.
//
// Concurrency: MUST be called with p.mu HELD, read or write. Takes no lock
// itself. This is the inverse of the contract it carried before #2492, and the
// inversion is shared with revivedSettings — see settingsSource. Its three
// callers hold the lock accordingly: materialise (write, via the source it is
// passed as), CreateIn (read, around its own call).
func (p *Pool) mintSettings() SessionSettings {
	boot := p.sessions[p.bootstrap]
	if boot == nil {
		return SessionSettings{}
	}
	return SessionSettings{
		Model:  boot.settings.Model,
		Effort: boot.settings.Effort,
	}
}

// revivedSettings returns the SessionSettings a revive of id starts from: the
// model and effort id's own dropped entry persisted, and no posture. An id with
// no dormant entry yields the zero value, which is what every revive got before
// #2448 — so an unknown id, and an entry that persisted neither field, both
// revive exactly as they did.
//
// The asymmetry is the whole decision, and it is narrower than it looks. A
// restart stays a revocation point for a permission bypass (#1487): a persisted
// yolo or permission_mode is not read here, so it cannot reach the revived
// session however the entry was written. Model and effort carry no privilege and
// are the operator's choice for that conversation, so dropping them only made the
// next turn run under claude's defaults.
//
// Built field by field rather than from settingsFromEntry with the posture
// cleared afterwards, matching mintSettings above for mintSettings' own recorded
// reason: the posture is then excluded STRUCTURALLY rather than by a clearing
// statement someone could later delete, and any field added to registryEntry in
// future is likewise not inherited until someone opts it in. That is the
// fail-closed direction. The cleared posture is spelled out downstream —
// buildSession's canonicalSettings turns the zero value into the default mode,
// exactly as it did for the zero value this replaces.
//
// Concurrency: MUST be called with p.mu HELD, read or write. Takes no lock
// itself, which is mintSettings' contract too — see settingsSource, the seam both
// are passed through.
//
// That contract is #2492's, and it inverts what this function carried before.
// Revive used to evaluate it as an ARGUMENT to materialise, so this RLock was
// released before materialise's write lock deleted the entry, and the previous
// revision of this paragraph called the gap benign — reasoning only about a
// concurrent REVIVE, which does land on materialise's take path and does drop the
// caller's settings by contract. What it did not reason about was a concurrent
// dormant WRITE, which had no writer until Pool.UpdateDormantSettings (#2463):
// such a write was persisted, acknowledged, and then deleted by the retirement,
// and the session came up on the value read before it. materialise now evaluates
// this INSIDE the critical section that retires the entry, so the read and the
// delete cannot be split and no window remains between them.
func (p *Pool) revivedSettings(id SessionID) SessionSettings {
	entry, ok := p.dormant[id]
	if !ok {
		return SessionSettings{}
	}
	return SessionSettings{
		Model:  entry.Model,
		Effort: entry.Effort,
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
// persist (rollback the in-memory entry on save failure) → schedule sess.Run on
// Pool.Run's errgroup via supervise → call Pool.Activate (cap-aware) to wake the
// lifecycle goroutine. Everything up to and including supervise is Pool.Mint;
// this is that plus the Activate.
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
// sequence, concurrency, and error semantics. The register-and-supervise half
// lives in Mint; this is that plus the cap-aware Activate, and the split is
// what lets the relay's per-conversation mint defer the spawn to the first
// message while the control plane's `sessions new` verb keeps bringing its
// session up (#2085).
func (p *Pool) CreateIn(ctx context.Context, label, spawnDir string) (SessionID, error) {
	id, err := p.Mint(label, spawnDir)
	if err != nil {
		return id, err
	}
	if err := p.Activate(ctx, id); err != nil {
		return id, err
	}
	return id, nil
}

// Mint registers a fresh session — mint an id, build it, persist it, schedule
// its lifecycle goroutine — WITHOUT spawning claude. The returned session is in
// the evicted state with its goroutine parked on the activate signal, which is
// byte-for-byte the shape an idle-evicted session has, so the child comes up on
// the caller's first Pool.Activate through the existing lazy-respawn path. It
// adds no lifecycle path; it only stops one step short of Create's.
//
// The absent context.Context is the API signal for that, as it is on Revive:
// Mint does no blocking work and cannot spawn.
//
// It exists because create_conversation must bind a session id to a fresh
// conversation without starting a process for a discussion nobody has spoken in
// — and, the load-bearing half, so that every per-session setting chosen before
// the first message is simply what the child launches with, instead of an
// in-band correction applied to an already-running child (#2085).
//
// spawnDir is the per-session spawn working directory, used verbatim and NOT
// validated, canonicalised, or trust-checked by the pool — callers supply a
// pre-resolved, $HOME-confined realpath, exactly as CreateIn, GetOrCreateIn and
// Revive require (#685/#696). spawnDir == "" spawns in the shared template
// workdir. SECURITY: it is a phone-influenced workspace path, so it must never
// be logged (the #741 precedent for session ids and conversation ids); nothing
// here logs, and nothing added here should.
//
// A minted session carries mintSettings — the operator's configured model and
// effort, never the bypass (#1575). That is the one thing separating it from
// Revive, which inherits nothing.
//
// Returns:
//   - id, nil — registered, persisted, and scheduled
//   - "", err — the failure was at or before the persist; nothing is on disk and
//     nothing in memory changed
//   - id, ErrPoolNotRunning — the registry entry IS on disk but no lifecycle
//     goroutine was scheduled (fix and retry by calling Run + Activate)
//
// Concurrency: safe for concurrent use. Each call serialises through Pool.mu
// briefly (registration + persist), then supervises off-lock.
func (p *Pool) Mint(label, spawnDir string) (SessionID, error) {
	id, err := NewID()
	if err != nil {
		return "", fmt.Errorf("sessions: create id: %w", err)
	}

	// A minted session starts at the operator's configured model and effort
	// (#1575). Since #2492 mintSettings is an already-locked reader, so the RLock
	// is taken here rather than inside it — and released before buildSession, which
	// must stay off p.mu.
	//
	// This entry point does NOT go through materialise and needs none of its
	// #2492 machinery: id comes fresh from NewID above, so it can name no dormant
	// entry and no dormant write can race it. The window that remains — a
	// concurrent Pool.UpdateSettings on the BOOTSTRAP between this read and the
	// registration below — is the pre-existing, benign one every lock-releasing
	// settings accessor carries, and it changes what a new session inherits, never
	// what an existing one holds.
	p.mu.RLock()
	minted := p.mintSettings()
	p.mu.RUnlock()
	sess, err := p.buildSession(id, label, spawnDir, minted)
	if err != nil {
		return "", err
	}

	// Persist before activating: if saveLocked fails, roll the in-memory
	// registration back so a retry sees a clean slate. See Create's docstring
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
		// The prompt file is discarded on the same terms and for the same reason,
		// with confidentiality on top: it holds the operator's text (#2150).
		_ = os.Remove(sess.systemPromptPath)
		return "", err
	}
	p.mu.Unlock()

	if err := p.supervise(sess); err != nil {
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
// so an unconfigured spawn's argv carries none. CreateIn and GetOrCreateIn source
// them from mintSettings — the operator's configured model and effort, never the
// bypass (#1575). Pool.Revive sources them from revivedSettings — the model and
// effort the dropped session's own entry persisted, and no posture, so a
// phone-granted bypass still cannot survive a daemon restart (#1487/#2448).
//
// Via materialise (GetOrCreateIn and Revive) the value reaching here is the
// PROVISIONAL one, read before this build; materialise re-reads the source under
// its own lock and, on the rare path where the two disagree, replaces both the
// stored settings and the runner's argv before the session is published (#2492).
// This function is unchanged by that and stays off p.mu deliberately: it writes
// two files and calls the injected RunnerFactory, none of which belongs inside
// the pool's write lock.
//
// The session it builds runs claude: CreateIn is the fresh-id path and nothing
// exposes a harness to clients yet (#2593). materialise, whose id may name a
// dormant entry of another harness, calls buildSessionAs.
func (p *Pool) buildSession(id SessionID, label, spawnDir string, settings SessionSettings) (*Session, error) {
	return p.buildSessionAs(id, label, spawnDir, settings, HarnessClaude)
}

// buildSessionAs is buildSession for a given harness, which it canonicalises and
// carries onto both the RunnerConfig and the Session (#2593). A harness the
// injected factory has no runner for fails here as an ordinary runner-construction
// error, with the session's two files removed and nothing registered.
func (p *Pool) buildSessionAs(id SessionID, label, spawnDir string, settings SessionSettings, harness string) (*Session, error) {
	harness = canonicalHarness(harness)
	tpl := p.sessionTpl
	// Normalise the posture before it reaches either the argv or the stored
	// value, so a minted session and a revived one — both two-field literals,
	// mintSettings' and revivedSettings' — hold the default mode spelled out
	// rather than an empty string. It is a normalisation, not a gate: neither caller takes a
	// mode from operator input — Pool.UpdateSettings is where an unrecognised
	// mode is rejected.
	settings = canonicalSettings(settings)
	// The per-session --settings file pre-approves the project's MCP servers so
	// claude's startup enablement modal never wedges the readiness check (#943).
	// It joins spawnBase (below) rather than claudeSettingsArgs so it survives
	// every recompose (backoff restart, #842 live restart). Removed in Pool.Remove
	// after the child is confirmed dead.
	settingsPath, err := writeMCPSettings(p.registryPath, id)
	if err != nil {
		return nil, fmt.Errorf("sessions: write mcp settings: %w", err)
	}
	// This session's appended system-prompt file: #2093's constant plus the bound
	// conversation's operator prompt, if it has one (#2150). Per-session rather
	// than daemon-scoped precisely because that text differs per conversation, and
	// composed HERE because label is the conversation id at both production sites
	// (create_conversation's mint and sessionRouter's revive), so nothing has to
	// widen a signature to carry the prompt. A label naming no conversation, or a
	// pool with no conversations registry, resolves to no bytes — never an error.
	//
	// The bytes are refreshed again in Pool.Activate before the child comes up:
	// since #2085 a conversation's session is minted at create and started on its
	// first message, so the prompt is typically set AFTER this runs.
	operatorPrompt := p.conversationPrompt(label)
	promptPath, err := writeSystemPrompt(p.registryPath, id, composeSystemPrompt(operatorPrompt))
	if err != nil {
		// The wrapped error carries paths only. No error and no log line on any
		// path may carry a fragment of the operator's prompt (#2150 AC #3).
		_ = os.Remove(settingsPath)
		return nil, fmt.Errorf("sessions: write system prompt: %w", err)
	}
	// Same reasoning as Pool.New's cleanup defer (#1518): in the data dir an
	// orphan is permanent, so an error return past this point must take the files
	// with it. Only one such return exists today; the defer shape means a future
	// one is covered without anyone remembering to add a removal.
	built := false
	defer func() {
		if !built {
			_ = os.Remove(settingsPath)
			_ = os.Remove(promptPath)
		}
	}()
	// base is the settings-free argv (template, resume suffix, and the immutable
	// --settings pair). Storing base on the Session lets a live restart recompose
	// full argv from the persisted settings (#842). composeSpawnArgs returns a
	// fresh final argv, so base remains the provenance source.
	base := append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))
	base = append(base, "--settings", settingsPath)
	// This session's own appended system-prompt file (#2093's flag, #2150's file).
	// On the base rather than in claudeSettingsArgs so the path survives every
	// recompose — a backoff restart, the #842 live settings-restart — each of
	// which re-execs whatever bytes the file then holds.
	base = append(base, "--append-system-prompt-file", promptPath)
	args := composeSpawnArgs(base, settings)
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
		SessionID: string(id),
		// Same seam as Pool.New's (#2135), and p is already in hand here.
		AdoptAnnouncedReset: func(oldID, newID string) error {
			return p.AdoptAnnouncedID(SessionID(oldID), SessionID(newID))
		},
		ClaudeArgs: args,
		Harness:    harness,
		// Same seed as Pool.New's, and canonicalSettings runs above this site too
		// (#2064).
		PermissionMode: settings.PermissionMode,
		// Same derivation as Pool.New's and off BASE for its reason (#2065). tpl is
		// p.sessionTpl, which is cfg.Bootstrap verbatim, so a minted session inherits
		// the same operator pass-through the bootstrap has — the provenance answer is
		// per-daemon, not per-session.
		OperatorBypass: operatorBypass(base),
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
		id:               id,
		sup:              sup,
		log:              p.log,
		label:            label,
		createdAt:        now,
		lastActiveAt:     now,
		bootstrap:        false,
		harness:          harness,
		settings:         settings,
		spawnBase:        base,
		settingsPath:     settingsPath,
		systemPromptPath: promptPath,
		systemPrompt:     operatorPrompt,
		pool:             p,
		idleTimeout:      idleTimeout,
		turnBusy:         p.turnBusy,
		removedCh:        make(chan struct{}),
		lcState:          stateEvicted,
		activeCh:         make(chan struct{}),
		evictedCh:        closedChan(),
		activateCh:       make(chan struct{}, 1),
		evictCh:          make(chan struct{}, 1),
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
