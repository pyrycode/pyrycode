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
	mu        sync.RWMutex
	sessions  map[SessionID]*Session
	bootstrap SessionID
	log       *slog.Logger
}

// New constructs a Pool, generating an ID for the bootstrap entry and
// constructing the underlying *supervisor.Supervisor. Returns an error if
// the rng or supervisor.New fails — both are fatal-at-startup conditions.
func New(cfg Config) (*Pool, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	id, err := NewID()
	if err != nil {
		return nil, fmt.Errorf("sessions: generate bootstrap id: %w", err)
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
		id:     id,
		sup:    sup,
		bridge: cfg.Bootstrap.Bridge,
		log:    cfg.Logger,
	}
	return &Pool{
		sessions:  map[SessionID]*Session{id: sess},
		bootstrap: id,
		log:       cfg.Logger,
	}, nil
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
