package sessions

import (
	"time"
)

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

// RotateBootstrapForSelfHeal rekeys the current bootstrap onto a minted ID,
// captures/rebinds its owner and emits an internal recovery fact off-lock. It has
// no production caller or automatic recovery policy. Recovery has no legacy
// reason, so it creates no client delimiter or legacy history boundary.
// Persistence is best effort; missing bootstrap entries emit nothing.
func (p *Pool) RotateBootstrapForSelfHeal() (SessionID, error) {
	newID, err := NewID()
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	old := p.bootstrap
	if _, ok := p.sessions[old]; !ok {
		p.mu.Unlock()
		return "", ErrSessionNotFound
	}
	p.rekeyLocked(old, newID)
	t := p.rotationTransitionLocked(old, newID, CauseRecovery, "")
	if err := p.saveLocked(); err != nil {
		p.log.Warn("sessions: self-heal rotate persist failed",
			"event", "rotate_self_heal.persist_failed",
			"session_id", string(newID),
			"previous_session_id", string(old),
			"err", err)
	}
	p.mu.Unlock()
	p.persistTransitionBinding(t)
	p.notifyTransition(t)
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
	// A new pool id is a new conversation, so the rotated entry never inherits
	// the old harness thread (#2622); the runner reports the one it starts next.
	sess.threadID = ""
	delete(p.sessions, oldID)
	p.sessions[newID] = sess
	if p.bootstrap == oldID {
		p.bootstrap = newID
	}
}

// recordThread persists threadID as the harness thread of the live session id
// (#2622): the target of RunnerConfig.RecordThread. ErrSessionNotFound when id is
// not live — a report racing a rotation that already moved the session — with
// nothing written; an unchanged thread skips the save. Takes Pool.mu (write) and
// holds it across the registry write, like Rename.
func (p *Pool) recordThread(id SessionID, threadID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	sess, ok := p.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if sess.threadID == threadID {
		return nil
	}
	sess.threadID = threadID
	return p.saveLocked()
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
