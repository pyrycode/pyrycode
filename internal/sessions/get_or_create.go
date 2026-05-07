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
	if !ValidID(string(id)) {
		return "", ErrInvalidSessionID
	}

	// buildSession touches no Pool state and is non-blocking
	// (supervisor.New does not spawn anything yet). Build it before taking
	// p.mu so the critical section stays small for concurrent same-id
	// callers and so we can discard the loser's freshly-built session
	// cheaply.
	sess, err := p.buildSession(id, label)
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	if existing, ok := p.sessions[id]; ok {
		p.mu.Unlock()
		_ = existing // take-path: caller's label is silently dropped; documented
		return id, nil
	}

	p.sessions[id] = sess
	if err := p.saveLocked(); err != nil {
		delete(p.sessions, id)
		p.mu.Unlock()
		return "", err
	}

	// Prime the rotation watcher's skip-set inside the same critical
	// section so any concurrent watcher snapshot sees register + skip-set
	// atomically.
	p.registerAllocatedUUIDLocked(id)

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
		return "", ErrPoolNotRunning
	}

	// Schedule the lifecycle goroutine while still holding p.mu — closes
	// the race where a concurrent same-id caller could observe the
	// registered entry, return the canonical id, and call Activate before
	// the lifecycle goroutine has parked on activateCh / runCtx.Done().
	// g.Go is non-blocking (it spawns a goroutine that parks immediately
	// on the buffered activate signal); holding p.mu across it is safe.
	g.Go(func() error { return sess.Run(gctx) })
	p.mu.Unlock()

	if err := p.Activate(ctx, id); err != nil {
		return id, err
	}
	return id, nil
}
