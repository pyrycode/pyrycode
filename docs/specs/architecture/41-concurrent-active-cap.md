# Phase 1.2c-B: Concurrent Active Cap (LRU Eviction)

**Ticket:** [#41](https://github.com/pyrycode/pyrycode/issues/41)
**Size:** S (~40–60 production lines)
**Depends on:** [#40](https://github.com/pyrycode/pyrycode/issues/40) — Phase 1.2c-A introduces `Pool.Activate` / `Session.Evict` and the active-state bookkeeping this ticket reuses. Cannot be implemented in isolation.

## Context

Phase 1.2c-A (#40) gives every session an `active`/`evicted` lifecycle: an idle timer per session evicts the running claude after N minutes of inactivity, and the next message respawns it lazily. Idle eviction alone bounds *steady-state* RAM, not *peak*. A burst of activity across many sessions can spike RAM beyond what the operator wants — every session that gets a message stays active until its idle timer fires, regardless of how many other sessions are also active right now.

This ticket adds a second eviction trigger that closes that gap: a configurable cap on the number of *concurrently active* claude processes. When activating a session would push the active count past the cap, the least-recently-active currently-active session is evicted at that moment, before the new spawn proceeds.

The two policies — idle and cap — compose cleanly because they share one mechanism. They differ only in *who picks the victim*:

| Trigger | Goroutine | Victim | Mechanism |
|---|---|---|---|
| Idle (1.2c-A) | per-session idle timer | itself | `Session.Evict` |
| Cap (1.2c-B, this ticket) | `Pool.Activate` caller | LRU active session | `Session.Evict` |

Both call into the same `Evict` primitive. The cap policy is a check executed at the spawn-path entry; the idle timer keeps running on its own goroutine. They are independent triggers with a shared mechanism.

### Why this is deferred but specified now

Today's session count is bounded by user-created CLI sessions (#28/#29/#34). The cap rarely binds. It earns its keep when Phase 2.0's first-message lazy bind enables Discord channels to mint sessions automatically — a noisy server can multiply session count overnight. We design and ship now so 2.0 lands against a finished primitive rather than racing it.

## Design

### Package layout

All new code lives in `internal/sessions`. No new packages, no new files.

```
internal/sessions/
  pool.go             [edit] Config.ActiveCap field, cap-check call in Activate
  session.go          [edit, maybe] re-export last_active_at if needed for victim selection helper
  pool_test.go        [edit] cap-zero parity, cap-binds eviction, race test
```

The code added here sits inside `Pool.Activate` — the function #40 introduces. If #40 names it differently (e.g. `Pool.Wake`, `Pool.EnsureActive`), this spec's references to `Activate` should be read as "the function #40 added that transitions an evicted session back to active and is the single spawn-path entry." Hooking the cap check anywhere else is wrong: idle-timer-driven eviction is *already* off the spawn path, and we need exactly one place where the cap is enforced.

### Key types and signatures

```go
// pool.go — Config gains one field.
type Config struct {
    Bootstrap         SessionConfig
    Logger            *slog.Logger
    RegistryPath      string
    ClaudeSessionsDir string

    // ActiveCap is the maximum number of concurrently active claude
    // processes this Pool will run. Zero (the unset default) means
    // uncapped — preserves Phase 1.2c-A's idle-only behaviour byte-for-byte.
    //
    // When set, an Activate that would push the count past the cap first
    // evicts the least-recently-active currently-active session via the
    // existing Evict primitive. The evicted session transitions to
    // `evicted` exactly as it would from an idle timeout — same on-disk
    // state change, same registry write.
    //
    // ActiveCap >= 1; values <= 0 are treated as unset.
    ActiveCap int
}
```

`Pool` itself does **not** gain a field. The active count is derived by iterating `p.sessions` and counting `Session.IsActive()` (the predicate #40 introduces). For early phases (≤ 100 sessions) the O(n) iteration is fine; introducing a counter is premature optimisation and risks a drift bug between counter and truth.

### The cap check

A single unexported helper, called from `Pool.Activate` immediately after it acquires `p.mu` (write) and before it issues the spawn:

```go
// pool.go

// enforceActiveCapLocked evicts the LRU active session if activating one
// more would exceed cfg.ActiveCap. Caller MUST hold p.mu (write).
//
// Returns nil when no eviction is needed (cap unset, or active count is
// already below cap, or `target` is itself currently active — already
// counted). Returns the evictee's id and any Evict error otherwise.
//
// `target` is the session whose Activate triggered the check; we exclude
// it from victim selection (you cannot evict the session you are about
// to activate to make room for itself).
func (p *Pool) enforceActiveCapLocked(target SessionID, cap int) (SessionID, error) {
    if cap <= 0 {
        return "", nil
    }

    var (
        active    int
        victim    *Session
        oldest    time.Time
    )
    for id, s := range p.sessions {
        if !s.IsActive() {
            continue
        }
        active++
        if id == target {
            continue // target is already active; not a victim, not a new slot
        }
        if victim == nil || s.lastActiveAt.Before(oldest) {
            victim = s
            oldest = s.lastActiveAt
        }
    }

    // Already-active target: no new slot consumed, no eviction needed.
    if _, ok := p.sessions[target]; ok && p.sessions[target].IsActive() {
        return "", nil
    }
    if active < cap {
        return "", nil
    }
    if victim == nil {
        // Pathological: cap == 1 and target is the only session.
        // Activating it consumes its own slot — no peer to evict.
        // Should not happen in practice (target must be inactive to reach
        // here, so active < 1 < cap is false only if cap <= 0).
        return "", nil
    }

    if err := victim.evictLocked(); err != nil {
        return victim.id, fmt.Errorf("cap: evict LRU victim %s: %w", victim.id.Short(), err)
    }
    return victim.id, nil
}
```

`evictLocked` is the lock-held variant of `Session.Evict` that #40 must expose to package-internal callers (the idle path holds `Session`-local state; the cap path holds `Pool.mu`). If #40 only exposes a public `Evict` that takes its own lock, this spec's implementation calls that — but the contract change requested below avoids the lock-order hazard.

### Contract requested from #40

This ticket's correctness depends on three properties from the #40 primitives. The #40 spec should state them; if they're missing or named differently, the implementer of this ticket must either confirm equivalence or push back on #40's spec before coding:

1. **`Session.IsActive() bool`** — read-only predicate, safe to call under `Pool.mu` (read or write). Returns true iff the underlying claude child is currently running.
2. **`Session.lastActiveAt time.Time`** — already exists in 1.2a; #40 must keep this updated on every Activate (the LRU bookkeeping reads this field). No new persisted field is introduced by this ticket.
3. **`Session.evictLocked() error`** — package-internal eviction that the caller invokes while holding `Pool.mu` (write). Synchronously stops the child and transitions registry state to `evicted`. The wait-for-exit may be bounded (e.g. SIGTERM with a 2s deadline then SIGKILL); the call returns once the process is reaped and bookkeeping is done.

If #40 chose a different lock discipline — e.g. `Session` has its own mutex and `Pool.mu` is released before per-session ops — this spec accepts that, with one constraint: **the cap-check, victim selection, eviction commit (the point of no return), and new spawn must be serialized** such that two concurrent `Activate` calls cannot both observe `active < cap` and both proceed to spawn. The cleanest serialization is `Pool.mu` (write) held across the whole sequence; if #40 uses a finer-grained scheme, this ticket will still need a Pool-level critical section for the cap check.

### Activate integration

Pseudocode for the modified `Pool.Activate` after this ticket lands:

```go
func (p *Pool) Activate(id SessionID) error {
    p.mu.Lock()
    defer p.mu.Unlock()

    sess, ok := p.sessions[id]
    if !ok {
        return ErrSessionNotFound
    }
    if sess.IsActive() {
        sess.touchLastActive() // update LRU stamp (1.2c-A behaviour, unchanged)
        return nil
    }

    if _, err := p.enforceActiveCapLocked(id, p.activeCap); err != nil {
        return err // cap binding + victim eviction failed: caller decides
    }

    return sess.activateLocked() // existing 1.2c-A path: spawn, mark active
}
```

`p.activeCap` is set from `Config.ActiveCap` in `New`. The hot path with `ActiveCap == 0` does `if cap <= 0 { return "", nil }` at the very top of `enforceActiveCapLocked` — a single branch, no map iteration. This is what "byte-identical when unset" buys: the loop is skipped entirely.

### Concurrency model

```
                  Pool.mu (write) held across:
                  ┌─────────────────────────────┐
  Activate(id) ──>│ 1. lookup sess              │
                  │ 2. if active: touch + return│
                  │ 3. enforceActiveCapLocked:  │
                  │      iterate, find LRU      │
                  │      victim.evictLocked()   │
                  │ 4. sess.activateLocked()    │
                  └─────────────────────────────┘

  Idle timer goroutine (per session, started by 1.2c-A):
                  ┌─────────────────────────────┐
  fires        ──>│ Pool.mu (write) →           │
                  │ session.evictLocked()       │
                  └─────────────────────────────┘
```

Both triggers serialize on `Pool.mu` (write). The idle timer and the cap check never deadlock because neither calls into the other while holding the lock; they just each grab the lock, do their bookkeeping + child kill, and release. Eviction's child-process work happens under the lock — this is a deliberate trade: we accept blocking other Pool ops for the eviction's bounded SIGTERM-then-SIGKILL window (~2s worst case) in exchange for never needing to reason about "what if the active count changed between check and act". For pyry's session counts and access patterns, this is a non-issue. If it ever becomes one, the fix is to copy the victim out, release the lock, evict, re-acquire — the helper signature already supports that refactor.

### Error handling

| Failure | Outcome |
|---|---|
| `enforceActiveCapLocked` finds no victim (cap=1, single session) | Return nil. `Activate` proceeds — the target itself fills the only slot. Pathological in practice (cap < 1 is treated as unset; cap == 1 with one session can't happen unless the lone session is currently inactive). |
| `victim.evictLocked()` returns error (kill failed, registry write failed) | Return the wrapped error to `Activate`'s caller. The new session does **not** spawn (cap would be exceeded). Caller treats this like any other Activate failure. |
| `sess.activateLocked()` fails after a successful eviction | Eviction is not rolled back. The pool now has one fewer active session than before; the operator is short one running claude. This is acceptable — the LRU victim was going to be evicted anyway under sustained load, and rollback would require respawning a process we just killed. |

### Testing strategy

Three test cases in `pool_test.go`, all using `t.TempDir()` and the test-only persistence-disabled mode established in 1.2a:

1. **Cap-zero parity.** Build a Pool with `ActiveCap == 0` and exercise the same Activate/idle sequence as a 1.2c-A test. Assert no observable behaviour difference (no eviction events emitted, registry contents identical to a 1.2c-A baseline). This is the "byte-identical when unset" AC.

2. **Cap binds, evicts LRU.** Build a Pool with `ActiveCap == 2` and three sessions A/B/C. `Activate(A)` then `Activate(B)` (both succeed, both active). `Activate(C)` — assert: A is now `evicted`, B and C are `active`, registry-on-disk matches. Drives `last_active_at` ordering by inserting `time.Sleep(5*time.Millisecond)` between activations, or by manually stamping (the helper takes time from `time.Now().UTC()` already, so a small sleep is enough; explicit clock injection is overkill for one test).

3. **Race: concurrent Activate against cap=1.** Spawn N (≥10) goroutines, all calling `Activate` against different sessions, with `ActiveCap == 1`. Use `sync.WaitGroup` to release them simultaneously. After the dust settles, assert `count(IsActive()) == 1`. This is the AC's "race coverage" requirement and the binding test for the Pool.mu-serialises-cap-check claim. Run under `-race`.

A manual smoke test (cap=2, 3 sessions, A→B→C) is recorded in the PR description, not as a Go test, because it needs a real claude binary and observable process exits.

### Migration / rollout

No migration. The default behaviour is unchanged (`ActiveCap == 0` is uncapped). Operators who want the cap set it via whatever surface `cmd/pyry` exposes — a flag, env var, or config file. **This ticket does not wire `Config.ActiveCap` into `cmd/pyry`'s flag-parsing.** The `Config` field is plumbed; surfacing it to the CLI is a follow-up (logical pairing: it lands with the same Phase 2.0 work that would actually need it). A test-only consumer in `pool_test.go` is sufficient for AC verification.

## Open Questions

- **CLI exposure.** Should `Config.ActiveCap` be flagged in cmd/pyry now, deferred to Phase 2.0, or only ever set from a config file? The spec defers — no operator needs it before 2.0. If the implementer wants to add `-pyry-active-cap N` for parity with other flags, that's fine and ~3 extra lines; not required.
- **Eviction-during-attach.** If the LRU victim has a live attach (someone is actively typing into its PTY), the cap policy will still kill its claude child. The user sees the claude subprocess die mid-conversation, the bridge pipes EOF, the operator's terminal disconnects. This is *correct* — the cap is a hard limit — but worth flagging. A future refinement could exclude attached sessions from the LRU candidate set; not in scope here.
- **Pathological cap=1 + one inactive session.** `Activate(only-session)` with cap=1 should succeed (no peer to evict, the target fills the slot). The pseudocode handles this. Verified by adding it as a sub-case to test #2 above.

## Why this is sized S

- One config field on an existing struct.
- One unexported helper (~25 lines including comments).
- One call site in `Pool.Activate` (~3 lines).
- Three test cases, none requiring infrastructure beyond what 1.2a/1.2c-A already exercise.

Total: ~40–60 production lines, ~80–100 test lines. Within S.

The dependency on #40 is the binding constraint — without #40's `Activate`/`Evict`/`IsActive` surface, this ticket has nothing to call. The architect-driven sizing call is: ship #40 first, then ship #41 against the landed primitive. Do not attempt to write both in one PR; the spawn-path ergonomics shake out from #40's implementation in ways that can't be predicted in a parent spec.
