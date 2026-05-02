package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
}

// SnapshotEntry is one (id, pid) pair captured by Pool.Snapshot. Carries
// only primitive types so the rotation package can consume snapshots without
// importing internal/sessions.
type SnapshotEntry struct {
	ID  SessionID
	PID int
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
	)
	if entry := pickBootstrap(reg); entry != nil {
		bootstrapID = entry.ID
		label = entry.Label
		createdAt = entry.CreatedAt
		lastActiveAt = entry.LastActiveAt
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
	sess := &Session{
		id:           bootstrapID,
		sup:          sup,
		bridge:       cfg.Bootstrap.Bridge,
		log:          cfg.Logger,
		label:        label,
		createdAt:    createdAt,
		lastActiveAt: lastActiveAt,
		bootstrap:    true,
	}
	p := &Pool{
		sessions:          map[SessionID]*Session{bootstrapID: sess},
		bootstrap:         bootstrapID,
		log:               cfg.Logger,
		registryPath:      cfg.RegistryPath,
		claudeSessionsDir: cfg.ClaudeSessionsDir,
		allocated:         make(map[SessionID]time.Time),
	}

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
	sess.lastActiveAt = time.Now().UTC()
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
	g.Go(func() error { return bootstrap.Run(gctx) })

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
func (p *Pool) saveLocked() error {
	if p.registryPath == "" {
		return nil
	}
	reg := &registryFile{
		Version:  1,
		Sessions: make([]registryEntry, 0, len(p.sessions)),
	}
	for _, s := range p.sessions {
		reg.Sessions = append(reg.Sessions, registryEntry{
			ID:           s.id,
			Label:        s.label,
			CreatedAt:    s.createdAt,
			LastActiveAt: s.lastActiveAt,
			Bootstrap:    s.bootstrap,
		})
	}
	sortEntriesByCreatedAt(reg.Sessions)
	return saveRegistryLocked(p.registryPath, reg)
}
