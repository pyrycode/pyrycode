package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pyrycode/pyrycode/internal/supervisor"
)

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
	mu           sync.RWMutex
	sessions     map[SessionID]*Session
	bootstrap    SessionID
	log          *slog.Logger
	registryPath string
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
		sessions:     map[SessionID]*Session{bootstrapID: sess},
		bootstrap:    bootstrapID,
		log:          cfg.Logger,
		registryPath: cfg.RegistryPath,
	}

	// Persist on cold start (no prior file). Warm starts do not rewrite —
	// the AC promises "writes only on state-changing operations", and a
	// warm reload is not a state change.
	if cfg.RegistryPath != "" && reg == nil {
		if err := p.saveLocked(); err != nil {
			return nil, fmt.Errorf("sessions: save registry: %w", err)
		}
	}
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

// Default returns the bootstrap session. Equivalent to Lookup("") today;
// kept as an explicit accessor because cmd/pyry will need the bootstrap
// entry at startup (Child B).
func (p *Pool) Default() *Session {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[p.bootstrap]
}

// Run blocks until ctx is cancelled, supervising every session in the pool.
// Phase 1.0: one session, one direct call to bootstrap.Run(ctx) — no errgroup.
// Phase 1.1+ replaces the body with errgroup fan-out; the call shape is the
// extension point.
func (p *Pool) Run(ctx context.Context) error {
	p.mu.RLock()
	bootstrap := p.sessions[p.bootstrap]
	p.mu.RUnlock()
	return bootstrap.Run(ctx)
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
