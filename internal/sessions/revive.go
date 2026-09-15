package sessions

// Revive materialises a persisted-but-dropped session back into the pool
// WITHOUT spawning claude. Pool.New builds exactly one *Session from
// sessions.json — the bootstrap — so a conversation bound to a minted session
// points at an id the pool does not have after a daemon restart; this is the
// entry point that puts that id back (#1487).
//
// The returned Session is in the evicted state with its lifecycle goroutine
// scheduled and parked, which is byte-for-byte the shape an idle-evicted
// session has. It therefore respawns on the next Pool.Activate through the
// existing lazy-respawn path — reviving adds no new lifecycle path, and the
// caller must Activate to bring claude up.
//
// The absent context.Context is the API signal for that: Revive does no
// blocking work and cannot spawn. Everything else — id validation, the
// register + persist + rollback critical section, the ErrPoolNotRunning guard —
// is shared verbatim with GetOrCreateIn via materialise, whose docstring carries
// the concurrency contract.
//
// spawnDir is the revived session's spawn working directory, used verbatim and
// NOT validated, canonicalised, or trust-checked by the pool — the caller
// supplies a pre-resolved, $HOME-confined realpath, exactly as CreateIn and
// GetOrCreateIn require (#685/#696). spawnDir == "" spawns in the shared
// template workdir. SECURITY: it is a phone-influenced workspace path, so it
// must never be logged (the #741 precedent for session ids and conversation
// ids); nothing here logs, and nothing added here should.
//
// A revived session carries the MODEL AND EFFORT its own dropped entry persisted,
// and NO POSTURE (#2448). Pool.revivedSettings reads the two fields off
// p.dormant; a persisted yolo or permission_mode is not read at all, so it cannot
// reach the revived session however the entry was written. An id with no
// persisted entry yields the zero value, i.e. exactly what every revive got
// before.
//
// The split is deliberate and is the amendment to ADR 035, which decided the
// revived session carries zero settings outright. A restart stays a natural
// revocation point for a permission bypass and re-granting is one settings verb
// away (#1487) — but model and effort carry no privilege, and dropping them only
// made the next turn after a restart run under claude's defaults instead of the
// settings the channel was visibly set to. Nothing new carries them to the child:
// buildSession already composes claudeSettingsArgs(settings) into the spawn argv,
// so --model (as the family alias, #2447) and --effort are on the first spawn the
// caller's Activate brings up.
//
// Since #2065 the posture's revocation is a DOWNGRADE rather than an omission,
// and the distinction is worth reading. canonicalSettings turns the unset mode
// into the default posture, and claudeSettingsArgs puts
// --dangerously-skip-permissions on every argv, so the revived child LAUNCHES in
// bypass and is walked back to default in-band before any user turn can reach it
// (internal/streamsup's spawn-time write, held behind its posture gate). The
// #1487 property is unchanged — a revived session is not escalated — but what
// enforces it moved from "the flag is absent from the argv" to "the write was
// confirmed".
//
// Returns:
//   - sess, nil — id is registered (existed before, or this call registered it)
//   - nil, ErrInvalidSessionID — id is empty / not a canonical UUIDv4
//   - nil, ErrPoolNotRunning — no errgroup wired (Pool.Run has not started or has exited)
//   - nil, <other> — buildSession or saveLocked failed; nothing is registered
//
// On the take path (id already in the pool) the existing *Session is returned
// unchanged and label/spawnDir are dropped, mirroring GetOrCreateIn — a revive
// racing a live session must never replace it.
//
// Concurrency: safe for concurrent use. Two callers racing the same id both
// receive the same *Session; exactly one registration happens.
func (p *Pool) Revive(id SessionID, label, spawnDir string) (*Session, error) {
	// The session's OWN persisted model and effort, never a persisted posture, so
	// a phone-set escalation still cannot survive a daemon restart (#1487 security
	// review) — as the default posture the revived child is downgraded into, since
	// #2065 (see the contract above). Deliberately NOT mintSettings, which
	// GetOrCreateIn passes: a revived session inherits nothing from the bootstrap,
	// only what it was itself set to. Evaluated before materialise, which takes
	// p.mu: revivedSettings takes it too and RWMutex is not reentrant.
	sess, _, err := p.materialise(id, label, spawnDir, p.revivedSettings(id))
	if err != nil {
		return nil, err
	}
	return sess, nil
}
