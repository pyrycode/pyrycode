package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/sessions/rotation"
	"github.com/pyrycode/pyrycode/internal/supervisor"
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

// ErrPoolNotRunning is returned by supervise when called before Pool.Run has
// wired the supervisor handle, or after Run has cleared it. Callers must
// invoke supervise only while Pool.Run is active.
var ErrPoolNotRunning = errors.New("sessions: pool not running")

// Config is what cmd/pyry hands to sessions.New.
type Config struct {
	Bootstrap SessionConfig
	Logger    *slog.Logger

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
	Bridge     *supervisor.Bridge // nil = foreground

	BackoffInitial time.Duration
	BackoffMax     time.Duration
	BackoffReset   time.Duration

	// IdleTimeout, when positive, causes the session's claude process to
	// exit after the configured period with no attached clients. The
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
	allocated         map[SessionID]time.Time

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
		lcState      lifecycleState // defaults to stateActive
	)
	if entry := pickBootstrap(reg); entry != nil {
		bootstrapID = entry.ID
		label = entry.Label
		createdAt = entry.CreatedAt
		lastActiveAt = entry.LastActiveAt
		lcState = parseLifecycleState(entry.LifecycleState)
	} else {
		id, err := NewID()
		if err != nil {
			return nil, fmt.Errorf("sessions: generate bootstrap id: %w", err)
		}
		bootstrapID = id
		now := time.Now().UTC()
		createdAt, lastActiveAt = now, now
	}

	supCfg := supervisor.Config{
		ClaudeBin:      cfg.Bootstrap.ClaudeBin,
		WorkDir:        cfg.Bootstrap.WorkDir,
		ResumeLast:     cfg.Bootstrap.ResumeLast,
		ClaudeArgs:     cfg.Bootstrap.ClaudeArgs,
		Bridge:         cfg.Bootstrap.Bridge,
		Logger:         cfg.Logger,
		BackoffInitial: cfg.Bootstrap.BackoffInitial,
		BackoffMax:     cfg.Bootstrap.BackoffMax,
		BackoffReset:   cfg.Bootstrap.BackoffReset,
	}
	sup, err := supervisor.New(supCfg)
	if err != nil {
		return nil, fmt.Errorf("sessions: bootstrap supervisor: %w", err)
	}
	idleTimeout := cfg.Bootstrap.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = cfg.IdleTimeout
	}
	sess := &Session{
		id:           bootstrapID,
		sup:          sup,
		bridge:       cfg.Bootstrap.Bridge,
		log:          cfg.Logger,
		label:        label,
		createdAt:    createdAt,
		lastActiveAt: lastActiveAt,
		bootstrap:    true,
		idleTimeout:  idleTimeout,
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
	p := &Pool{
		sessions:           map[SessionID]*Session{bootstrapID: sess},
		bootstrap:          bootstrapID,
		log:                cfg.Logger,
		registryPath:       cfg.RegistryPath,
		claudeSessionsDir:  cfg.ClaudeSessionsDir,
		allocated:          make(map[SessionID]time.Time),
		activeCap:          cfg.ActiveCap,
		sessionTpl:         cfg.Bootstrap,
		idleTimeoutDefault: cfg.IdleTimeout,
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

	if err := reconcileBootstrapOnNew(p, cfg.ClaudeSessionsDir, cfg.Logger); err != nil {
		return nil, fmt.Errorf("sessions: reconcile bootstrap: %w", err)
	}
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
// Invariant: this mutates session.id without taking session.lcMu. Today the
// only callers (startup reconciliation, future fsnotify-driven /clear
// detection) run before any lifecycle goroutine begins observing the id, so
// no concurrent reader exists. lastActiveAt IS protected by lcMu and is
// taken briefly. Lock order remains Pool.mu → Session.lcMu.
//
// This is the load-bearing seam the live-detection ticket reuses.
func (p *Pool) RotateID(oldID, newID SessionID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	sess, ok := p.sessions[oldID]
	if !ok {
		return ErrSessionNotFound
	}
	if oldID == newID {
		return nil
	}
	sess.id = newID
	sess.lcMu.Lock()
	sess.lastActiveAt = time.Now().UTC()
	sess.lcMu.Unlock()
	delete(p.sessions, oldID)
	p.sessions[newID] = sess
	if p.bootstrap == oldID {
		p.bootstrap = newID
	}
	return p.saveLocked()
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

// Default returns the bootstrap session. Equivalent to Lookup("") today;
// kept as an explicit accessor because cmd/pyry will need the bootstrap
// entry at startup (Child B).
func (p *Pool) Default() *Session {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[p.bootstrap]
}

// Run blocks until ctx is cancelled, supervising every session in the pool
// and (when ClaudeSessionsDir is set) running the rotation watcher alongside
// it. errgroup ties the goroutines together: cancellation propagates, and
// Wait returns the first non-nil error.
//
// Phase 1.1+ extends the fan-out to one supervisor.Run goroutine per session
// — the errgroup wrapper introduced here is the extension point.
func (p *Pool) Run(ctx context.Context) error {
	p.mu.RLock()
	bootstrap := p.sessions[p.bootstrap]
	dir := p.claudeSessionsDir
	p.mu.RUnlock()

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
				return p.RotateID(SessionID(oldID), SessionID(newID))
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
	id, err := NewID()
	if err != nil {
		return "", fmt.Errorf("sessions: create id: %w", err)
	}

	tpl := p.sessionTpl
	args := append(slices.Clone(tpl.ClaudeArgs), "--session-id", string(id))
	var bridge *supervisor.Bridge
	if tpl.Bridge != nil {
		bridge = supervisor.NewBridge(p.log)
	}

	supCfg := supervisor.Config{
		ClaudeBin:      tpl.ClaudeBin,
		WorkDir:        tpl.WorkDir,
		ResumeLast:     false,
		ClaudeArgs:     args,
		Bridge:         bridge,
		Logger:         p.log,
		BackoffInitial: tpl.BackoffInitial,
		BackoffMax:     tpl.BackoffMax,
		BackoffReset:   tpl.BackoffReset,
	}
	sup, err := supervisor.New(supCfg)
	if err != nil {
		return "", fmt.Errorf("sessions: create supervisor: %w", err)
	}

	idleTimeout := tpl.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = p.idleTimeoutDefault
	}

	now := time.Now().UTC()
	sess := &Session{
		id:           id,
		sup:          sup,
		bridge:       bridge,
		log:          p.log,
		label:        label,
		createdAt:    now,
		lastActiveAt: now,
		bootstrap:    false,
		pool:         p,
		idleTimeout:  idleTimeout,
		lcState:      stateEvicted,
		activeCh:     make(chan struct{}),
		evictedCh:    closedChan(),
		activateCh:   make(chan struct{}, 1),
		evictCh:      make(chan struct{}, 1),
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
