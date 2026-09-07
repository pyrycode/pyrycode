package sessions

import (
	"context"
	"errors"
)

// ErrInvalidSessionID is returned by Pool.GetOrCreate when the supplied id
// is not a canonical UUIDv4-shaped string. Empty id also returns this.
// Matchable via errors.Is.
var ErrInvalidSessionID = errors.New("sessions: invalid session id")

// GetOrCreate is the take-or-create entry point: returns the canonical
// SessionID of the session keyed by id, creating one if none is registered.
// The returned SessionID is exactly id on success.
//
// id MUST be a canonical UUIDv4 string (matches NewID's output shape). Empty
// id and malformed strings return ErrInvalidSessionID.
//
// The "exists" path is a constant-time map lookup that returns without
// activating the session. Subsequent Activate is the caller's responsibility
// (handleAttach already does this).
//
// The "create" path is byte-equivalent to Pool.Create except the caller's id
// is used in place of NewID's output, and the register+persist+supervise
// sequence is held under p.mu. Two concurrent calls for the same id produce
// exactly one registry entry — the loser observes the winner's entry under
// p.mu and returns the canonical id with no error. The lifecycle goroutine
// for the new session is scheduled before the winner's GetOrCreate returns;
// the loser's later Activate is therefore safe.
//
// Concurrency: safe for concurrent use. Concurrent calls for different ids
// serialise only briefly through p.mu.
//
// Returns:
//   - id, nil — session is registered (existed before, or this call created it)
//   - "", ErrInvalidSessionID — id is empty / not a canonical UUIDv4
//   - "", ErrPoolNotRunning — no errgroup wired (Pool.Run has not started or has exited)
//   - "", <other> — supervisor.New, saveLocked, or Activate error (creation path)
//
// On the create path, an Activate failure returns id (the entry is registered
// and lifecycle goroutine is scheduled) plus the underlying error — same shape
// as Pool.Create.
func (p *Pool) GetOrCreate(ctx context.Context, id SessionID, label string) (SessionID, error) {
	return p.GetOrCreateIn(ctx, id, label, "")
}

// GetOrCreateIn is GetOrCreate with an explicit per-session spawn working
// directory, applied only on the *create* path. On the take path (session
// already registered) spawnDir is ignored — the existing session keeps its
// own workdir, mirroring how the take path already drops the caller's label
// (see below). spawnDir == "" spawns in the shared template workdir,
// byte-identical to GetOrCreate; a non-empty spawnDir is used verbatim and is
// NOT validated, canonicalised, or trust-checked by the pool (see #685).
//
// Otherwise identical to GetOrCreate: see its docstring for the full
// take/create semantics, concurrency, and return shapes.
func (p *Pool) GetOrCreateIn(ctx context.Context, id SessionID, label, spawnDir string) (SessionID, error) {
	// A minted session starts at the operator's configured model and effort
	// (#1575). Read here rather than inside materialise, which Revive also
	// calls: revive must keep inheriting nothing. mintSettings takes p.mu.RLock
	// internally, so it must be evaluated before materialise takes p.mu.
	_, took, err := p.materialise(id, label, spawnDir, p.mintSettings())
	if err != nil {
		return "", err
	}
	// The take path returns WITHOUT activating — the caller owns that step
	// (handleAttach already does it). Only the register path activates.
	if took {
		return id, nil
	}
	if err := p.Activate(ctx, id); err != nil {
		return id, err
	}
	return id, nil
}

// materialise is the take-or-register core shared by GetOrCreateIn and Revive:
// validate the id, build the session off-lock, then under p.mu either hand back
// the entry already registered for id (took == true) or register the freshly
// built one, persist it, prime the rotation skip-set, and schedule its
// lifecycle goroutine.
//
// It never spawns claude. A registered session is in stateEvicted with its
// lifecycle goroutine parked on the activate signal, so waking it is the
// caller's business — which is exactly what separates the two callers:
// GetOrCreateIn activates after a register, Revive deliberately does not.
//
// See GetOrCreate's docstring for the semantics this implements; the
// concurrency contract there (register + persist + g.Go held under p.mu, so the
// loser of a same-id race can never Activate before the winner's lifecycle
// goroutine exists) lives here.
//
// settings is forwarded verbatim to buildSession and is the caller's decision,
// not this function's — the second thing that separates the two callers.
// GetOrCreateIn passes Pool.mintSettings, so a minted session starts at the
// operator's configured model and effort; Revive passes the zero value, so a
// revived one inherits nothing. Keeping it a parameter is what stops a change
// to the mint path from silently re-pointing revive at the bootstrap's
// settings; reading mintSettings here instead would do exactly that.
//
// Returns:
//   - (sess, true, nil) — id was already registered; sess is the EXISTING entry
//     and the caller's label + spawnDir are silently dropped
//   - (sess, false, nil) — sess was registered, persisted, and scheduled
//   - (nil, false, err) — nothing registered; ErrInvalidSessionID, a buildSession
//     error, a saveLocked error, or ErrPoolNotRunning, each rolled back
func (p *Pool) materialise(id SessionID, label, spawnDir string, settings SessionSettings) (*Session, bool, error) {
	if !ValidID(string(id)) {
		return nil, false, ErrInvalidSessionID
	}

	// buildSession touches no Pool state and is non-blocking
	// (supervisor.New does not spawn anything yet). Build it before taking
	// p.mu so the critical section stays small for concurrent same-id
	// callers and so we can discard the loser's freshly-built session
	// cheaply.
	sess, err := p.buildSession(id, label, spawnDir, settings)
	if err != nil {
		return nil, false, err
	}

	p.mu.Lock()
	if existing, ok := p.sessions[id]; ok {
		p.mu.Unlock()
		// The discarded session's settings file is deliberately NOT removed here,
		// nor in either rollback below — the asymmetry with CreateIn is a decision,
		// not an oversight (#1518). The name is derived from the session id, so the
		// loser's path is byte-identical to the winner's and the winner's spawnBase
		// already references it; removing it would delete a live session's settings
		// file and re-open the #943 modal wedge. The payload is fixed, so the
		// redundant write is harmless and leaving the file is the correct action.
		return existing, true, nil
	}

	p.sessions[id] = sess
	if err := p.saveLocked(); err != nil {
		delete(p.sessions, id)
		p.mu.Unlock()
		return nil, false, err
	}

	g, gctx := p.runGroup, p.runCtx
	if g == nil {
		// Roll back: registry entry must not survive when no lifecycle
		// goroutine can drive it. Best-effort persist of the rolled-back
		// state — a save failure here is benign (the in-memory map is
		// the source of truth for this process; the next successful save
		// will catch up).
		delete(p.sessions, id)
		_ = p.saveLocked()
		p.mu.Unlock()
		return nil, false, ErrPoolNotRunning
	}

	// Schedule the lifecycle goroutine while still holding p.mu — closes
	// the race where a concurrent same-id caller could observe the
	// registered entry, return the canonical id, and call Activate before
	// the lifecycle goroutine has parked on activateCh / runCtx.Done().
	// g.Go is non-blocking (it spawns a goroutine that parks immediately
	// on the buffered activate signal); holding p.mu across it is safe.
	g.Go(func() error { return sess.Run(gctx) })
	p.mu.Unlock()
	return sess, false, nil
}
