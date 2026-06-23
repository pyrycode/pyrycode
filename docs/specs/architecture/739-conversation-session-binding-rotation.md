# Spec #739 — Maintain the conversation↔session binding across /clear rotation

**Ticket:** [#739](https://github.com/pyrycode/pyrycode/issues/739) · split from #738
**Size:** S (architect-confirmed; not downgraded to XS — see *Sizing* below)
**Labels:** `security-sensitive` (embedded security-review pass below; verdict PASS)
**Blocks:** #741 (the downstream consumer that resolves session id → owning conversation)

---

## Files to read first

- `internal/sessions/transition.go:47-72` — `notifyTransition` (the off-lock chokepoint both reasons pass through) + `onRotate`. **The reason-branch and the new `rebindConversation` land here.**
- `internal/conversations/registry.go:169-207` — `Update` / `Delete` conventions to mirror for the new `RebindSession`: scan-under-`r.mu`, first-match, **no Save** (caller owns persistence).
- `internal/conversations/conversation.go:44-59` — the documented `CurrentSessionID` + `SessionHistory` field contracts. `SessionHistory` is oldest-first; "rotation appends in place (`append(SessionHistory, prevID)`)" is the contract AC#1 must satisfy.
- `internal/sessions/pool.go:154-222` — `Pool` struct: the read-only-after-`New` fields the rebind reads — `convReg` (`*conversations.Registry`), `convRegistryPath` (string), `log` (`*slog.Logger`); plus the `transitionObserver` single-slot doc.
- `internal/sessions/pool.go:443-480` — `RotateID`: the in-memory map re-key (`sess.id = newID`, `:470`) that **precedes** the rebind, and its `Pool.mu → Session.lcMu` / `saveLocked` lock-order invariants. The rebind runs *after* `RotateID`, off all pool locks.
- `internal/sessions/session.go:270-306` — the **eviction** fire site: `notifyTransition` with `PreviousID = s.id`, `NewID == ""` (`:291`). This is the signal the reason-branch must **not** rebind (AC#2).
- `cmd/pyry/session_transition_v2.go:186-223` — the #657/#659 producer that already owns the **single** observer slot (`SetTransitionObserver(emitter.Enqueue)`, `:207`). Why a second observer is impossible, and why the rebind drives from the server-side `notifyTransition`, not a new observer.
- `cmd/pyry/main.go:909-932` — `sessionRouter.resolve`: the `CurrentSessionID == "" → errNoBoundSession` guard (`:923`) that AC#2's binding-neutrality protects (the `send_message` respawn path the ticket cites at `main.go:923`).
- `internal/sessions/pool_conv_sweep_test.go:1-50` — `seedConvRegistry` helper + the `pool.convReg = …; pool.convRegistryPath = …` in-package test-wiring pattern to reuse for the pool-side rebind tests.
- `internal/sessions/transition_test.go:41-102, 220-246` — `transitionRecorder` + the `onRotate` happy/unknown-id/nil-observer test shapes the rebind tests extend.
- `internal/conversations/registry_test.go:517-572, 765-797` — `TestRegistry_Update_Hit/Miss` + `TestRegistry_Promote_DoesNotPersist` shapes to mirror for the `RebindSession` unit tests.

---

## Context

`internal/conversations.Conversation` documents two binding fields — `CurrentSessionID` (`conversation.go:44`) and the oldest-first `SessionHistory` trail (`conversation.go:51`) — but the **rotation-case maintenance is documented and never implemented**: `Registry.Update` has no production caller, `SessionHistory` is never written, and the `/clear` rotation seam (`onRotate → RotateID`) re-keys the pool's session map but leaves the conversation registry frozen at its creation-time binding (`create_conversation.go:197`).

Consequence: after the **first** `/clear` rotation a conversation still points at the retired session id. A reverse lookup (session id → owning conversation) silently misses for the rotated session, and the documented history trail does not exist. This ticket implements the **write side** of that maintenance so #741 (the read side) has a correct binding to resolve.

**Eviction is deliberately out of the rebind path.** `ReasonEviction` fires with `PreviousID = s.id`, **empty `NewID`** (`session.go:291`): the session keeps its id, stays in the pool map, and re-activates later under the *same* id. `CurrentSessionID` therefore stays valid across eviction — the binding needs **no write**. The danger is the opposite: a naive "set `CurrentSessionID = NewID`, append `PreviousID`" applied to the eviction signal would clear the binding (`NewID == ""`) — breaking the `send_message` respawn guard at `main.go:923` — and append a colliding duplicate of the current id. AC#2 is the no-corruption guarantee.

---

## Design

Two surgical additions, no new types, no new files:

### 1. `conversations.Registry.RebindSession` — the write primitive

New method on the existing registry, mirroring the `Update`/`Delete` shape (scan under `r.mu`, first-match, **no Save**):

```go
// RebindSession re-points the conversation currently bound to oldID at newID,
// recording oldID in SessionHistory. Returns true iff a conversation was
// rebound. Scan + mutate happen atomically under r.mu (no TOCTOU window).
//   hit  → CurrentSessionID = newID; SessionHistory = append(SessionHistory, oldID)
//   miss → no mutation, false (the rotated session is owned by no conversation)
// Guard: oldID == "" returns false immediately (an unbound conversation carries
// CurrentSessionID == "" and must NEVER be matched). Precondition (caller-
// guaranteed): oldID, newID non-empty and distinct. Does NOT persist — caller
// owns Save, matching Create/Update/Promote/Delete.
func (r *Registry) RebindSession(oldID, newID string) bool
```

- **First-match-and-stop**, mirroring `Get`/`Update`: a session id binds exactly one conversation (set once at `create_conversation.go:197`), so the first match is the only match. Pathological duplicates rebind the first only — deterministic, documented.
- The `oldID == ""` short-circuit is a **data-integrity guard**, not the primary eviction defense (that lives at the call site, layer 2 below). It exists so a stray empty-id call can never sweep every unbound conversation into a rebind. Invariant asserted by a unit test.
- Behavioral contract verbalized in the doc comment; the developer writes the body in the registry's existing idiom.

### 2. `Pool.rebindConversation` + the reason-branch in `notifyTransition`

`notifyTransition` (`transition.go:50`) gains a reason-branch that rebinds **before** the observer fan-out:

```go
func (p *Pool) notifyTransition(t SessionTransition) {
    // A /clear rotation changed the session id in place: re-point the owning
    // conversation's binding BEFORE the observer fan-out, so #741 resolves
    // session→conversation against the CURRENT binding. Eviction keeps its id
    // (NewID==""), is binding-neutral, and skips this branch (AC#2).
    if t.Reason == ReasonClear {
        p.rebindConversation(t.PreviousID, t.NewID)
    }
    if p.transitionObserver != nil {
        p.transitionObserver(t)
    }
}
```

```go
// rebindConversation maintains the conversation↔session binding after a /clear
// rotation re-keyed a session (oldID → newID). No-op when no registry is wired
// (test pools, p.convReg == nil) or no conversation owns oldID (AC#4 — skips
// Save so the file mtime stays stable). On a successful rebind it persists
// conversations.json via the registry's atomic Save; a Save error is logged at
// Warn and swallowed — the in-memory rebind is already applied and usable
// (best-effort durability, matching create_conversation's eager persist).
func (p *Pool) rebindConversation(oldID, newID SessionID)
```

**Why drive from `notifyTransition`, not a new observer.** The transition signal has a **single** observer slot (`SetTransitionObserver`, "set once") already owned by the `session_transition_v2.go` producer (`:207`, installed before `Pool.Run`). A second observer is impossible. `notifyTransition` is the common chokepoint for both reasons, runs **off all pool locks**, and the pool already holds the registry via `Config.ConversationsRegistry` (mirrored to `p.convReg` / `p.convRegistryPath`). Placing the rebind there — branch on reason, rebind *then* observer — co-locates all transition side-effects at the documented chokepoint and makes the #741 ordering structural (one function: rebind completes synchronously before the observer hand-off).

### Data flow (clear rotation)

```
rotation watcher → onRotate(old,new)
    → RotateID(old,new)              [Pool.mu held: map re-key + sessions.json save]
    → notifyTransition({Clear, old, new})        [no locks held]
        → rebindConversation(old,new)            [ReasonClear branch]
            → convReg.RebindSession(old,new)     [conversations.Registry.mu: scan+mutate]
            → convReg.Save(convRegistryPath)     [on true only; atomic temp+fsync+rename]
        → transitionObserver({Clear,old,new})    [#657 emitter Enqueue; #741 reads binding later]
```

Eviction (`session.Run → notifyTransition({Eviction, id, ""})`) skips the `ReasonClear` branch entirely — the registry is never touched. Binding-neutrality is therefore structural, not a runtime guard.

---

## Concurrency model

- **No new goroutines.** The rebind runs inline on whichever goroutine fired the transition — the rotation-watcher goroutine for `ReasonClear` (the only path that rebinds). The eviction lifecycle goroutine never enters the branch.
- **Lock order:** `notifyTransition` is invoked with **no** `Pool.mu` / `Session.lcMu` held (confirmed: `onRotate` releases `Pool.mu` inside `RotateID` before calling `notifyTransition`; the eviction site fires post-`transitionTo`, off `lcMu`). `RebindSession` then takes `conversations.Registry.mu` alone. New edge: **pool transition path → conversations.Registry.mu**, taken with no pool lock held — no cycle with the existing sweep loop (which takes the same registry mutex independently) or `RotateID`'s `Pool.mu`.
- **Reentrancy:** `RebindSession`'s `fn`-free body never calls back into the registry — no `sync.Mutex` re-entry (the documented `Registry.Update` hazard does not apply; we don't use `Update`).
- **Concurrent Save:** the rotation rebind's `Save` and the sweep loop's periodic `Save` may race on the same path. Both snapshot under `r.mu` then write atomically (temp + fsync + rename); the rename is the commit point, so the loser is cleanly overwritten by an equally-consistent snapshot. This is the existing posture — `create_conversation.go:208` and the sweep loop already both Save the same file. No new concern.
- **`#741` ordering:** the in-memory rebind completes synchronously before `transitionObserver` is invoked; the observer only hands the signal to a buffered channel, and #741 resolves the binding later on its own Run goroutine — by which point the rebind (and its Save) are long done. Ordering holds with margin.

---

## Error handling

| Failure | Behavior |
|---|---|
| No conversation owns `oldID` (AC#4) | `RebindSession` → `false`; `rebindConversation` skips `Save`; `onRotate` returns nil. No mutation, no error, no file write. Map re-key + observer fan-out unchanged. |
| No registry wired (`p.convReg == nil`) | `rebindConversation` no-ops immediately. Keeps every existing `transition_test.go` test (which wires no registry) green. |
| `Save` fails after a successful in-memory rebind | Logged at `Warn` (content-free fields), swallowed. In-memory binding is correct and usable; durability is best-effort, exactly as `create_conversation.go:208` and `RunSweepLoop` treat their own Save. The rotation does **not** fail on a persist error (matches `RotateID`'s "save error is non-fatal to the in-memory rotation" contract). |
| Eviction signal reaches `notifyTransition` | Reason-branch skips it; binding untouched (AC#2). Belt-and-suspenders layer 2: even a mis-routed empty-`oldID` call hits `RebindSession`'s `oldID == ""` → `false` guard. |

---

## Security review (security-sensitive — run on this spec; verdict: PASS)

**Trust boundary.** The rebind is driven **entirely by server-internal state**. The transition signal originates from the daemon's own rotation watcher (fsnotify detecting claude's `/clear`) and eviction lifecycle — never from phone input. `oldID`/`newID` are session UUIDs the pool itself manages and re-keyed in `RotateID`; the conversation registry is the daemon's own on-disk state. **No untrusted input flows into the rebind.** The confidentiality stake (per the ticket) is downstream: the binding maintained here is the attribution #741 uses to route a session-boundary event to a mobile-facing conversation, so a mis-write is a cross-conversation leak that *originates here*.

**Threat — mis-attribution (rebind the wrong conversation).** Mitigated by **deterministic code**. `RebindSession` matches by `CurrentSessionID == oldID` (byte-exact) and rebinds the **first** match only. Each session id binds exactly one conversation (set once at creation), so first-match is the sole correct match. The `oldID == ""` guard structurally prevents the catastrophic case of matching every unbound conversation (`CurrentSessionID == ""`). No stochastic gate is involved.

**Threat — stale/colliding history entry (the named eviction hazard).** Applying the naive rebind to an eviction signal (`NewID == ""`) would clear the binding (breaking the `send_message` respawn guard at `main.go:923`) and append a duplicate of the current id to `SessionHistory`. Mitigated by **two independent deterministic layers, different fabric**: (1) the `ReasonClear` branch in `notifyTransition` — eviction never reaches the rebind; (2) the `oldID == ""` guard inside `RebindSession` — a second check at the primitive boundary. Both are code, not agent rules. AC#2 pins layer (1); the registry empty-`oldID` unit test pins layer (2).

**Threat — cross-conversation leak via the persisted file.** `RebindSession` mutates **exactly** the matched entry; no other row is read-modified. `Save` persists the whole consistent snapshot atomically (temp + fsync + rename) — no torn write, no partial state observable by a reload or a concurrent reader.

**Information disclosure / logging.** The only log line the rebind can emit is a `Save`-failure `Warn` carrying content-free fields — an event discriminant + the session ids (non-secret routing identifiers; `session_id` is already a standard structured field across `internal/sessions`). It never logs conversation content, names, or the registry payload. Matches the never-echo discipline of `session_transition_v2.go` and `msgqueue`.

**TOCTOU.** The scan and mutate are a single critical section under `r.mu` — no find-then-update window where a concurrent `Delete`/`Create` could redirect the write. The rebind completes before the observer fan-out, so #741 never resolves against a half-applied binding.

**Verdict: PASS.** No revision needed.

---

## Testing strategy

Stdlib `testing` only, table-driven where natural, `-race` clean. Scenarios (the developer writes bodies in the project idiom):

### Registry unit tests — `internal/conversations/registry_test.go` (mirror `TestRegistry_Update_*`)

- **Hit + history append (AC#1):** registry with a conversation bound to `oldID` (plus unrelated rows) → `RebindSession(old,new)` returns `true`; that conversation's `CurrentSessionID == new`; `SessionHistory` tail `== old`, length grew by one; **unrelated rows byte-identical** (deep-compare).
- **Append order is oldest-first (AC#1):** a conversation already carrying `SessionHistory == [s0]` → after rebind, `[s0, old]` (append-in-place, not prepend).
- **Miss (AC#4):** registry populated but no row bound to `oldID` → `false`; no row mutated (snapshot equality before/after).
- **Empty-`oldID` guard (security layer 2):** registry containing an **unbound** conversation (`CurrentSessionID == ""`) → `RebindSession("", new)` returns `false` and does **not** match the unbound row.
- **Does-not-persist:** mirror `TestRegistry_Promote_DoesNotPersist` — `RebindSession` alone writes no file; the caller owns `Save`.
- **First-match-only:** two rows pathologically bound to the same `oldID` → only the first is rebound; the second is untouched.

### Pool-side tests — `internal/sessions/transition_test.go` (reuse `seedConvRegistry`, `transitionRecorder`, `helperPool*`; wire via `pool.convReg = …; pool.convRegistryPath = …`)

- **`TestPool_OnRotate_RebindsOwningConversation` (AC#1 + AC#3):** seed a registry with a conversation bound to the bootstrap id; wire it into the pool + a `transitionRecorder`. `onRotate(old,new)` →
  - in-memory: the conversation now has `CurrentSessionID == new`, `SessionHistory` tail `== old`;
  - **on disk:** `conversations.Load(path)` reloads the rebind (AC#3 reload survival);
  - the observer still fired **exactly once** with `ReasonClear/old/new` (fan-out unchanged).
- **`TestPool_OnRotate_NoOwnerNoOp` (AC#4):** registry has conversations but none bound to the rotated id → `onRotate` returns nil; no conversation mutated; capture the file's pre-rotation bytes/mtime and assert **no write** occurred; observer still fired once.
- **`TestPool_Eviction_BindingNeutral` (AC#2):** seed a conversation bound to the bootstrap id; wire the registry into a `helperPoolIdle` pool; drive an idle eviction (poll to `stateEvicted`). Assert the conversation's `CurrentSessionID` is **unchanged** (still the evicted id) and `SessionHistory` is **empty** (no colliding append). This proves the reason-branch skips eviction end-to-end.
- **No-registry regression:** the existing `TestPool_TransitionObserver_*` suite (which wires **no** registry) must stay green — the `p.convReg == nil` no-op guard guarantees it. No new test needed; call this out in the PR description.
- **(Optional) `-race`:** extend `TestPool_TransitionObserver_RaceConcurrentFires` to wire a registry whose conversations are each bound to one rotating id — concurrent `RebindSession` + `Save` under `-race` exercises the registry-mutex + atomic-Save safety. Low cost; include if the budget allows.

---

## Open questions

- **Per-rotation Save vs. coalescing.** Each `/clear` rotation triggers one synchronous atomic `Save` of `conversations.json` on the watcher goroutine (sub-ms for a small file, human-paced rotations). This matches `RotateID`'s own synchronous `sessions.json` save on the same goroutine — no coalescing needed at this scale. If rotation frequency ever spikes, batching is a later optimization, not part of this slice.
- **Shared session→conversation scan helper.** This ticket lands only the **write** (`RebindSession`). #741's **read** lookup (session id → conversation id) is a separate scan. PROJECT-MEMORY's "Resist over-DRY on duplicated registry primitives" tolerates the duplication until a third consumer forces extraction — so #741 may add its own `Get`-style scan rather than reuse anything here. No shared helper is introduced now.

---

## Sizing

**S, not downgraded to XS.** 2 production files (`internal/conversations/registry.go`, `internal/sessions/transition.go`), ~45 production LOC, 0 new exported types, 1 new exported method. No edit fan-out (`codegraph_impact notifyTransition` → contained to its own file; `RebindSession` is net-new with no callers). The ticket's XS condition — "the maintenance reduces to a *single* rotation rebind call" — is not met: there is a genuine reason-fork in `notifyTransition`, a new registry primitive with its own hit/miss/guard/no-persist test matrix, and a persistence + reload obligation (AC#3) plus an eviction-neutrality obligation (AC#2). The test surface (6 registry scenarios + 3 pool scenarios incl. on-disk reload) is squarely S, not XS. No red line tripped; ships as one ticket.

**File-overlap check:** clean. No in-flight `origin/feature/*` branch touches `registry.go`, `registry_test.go`, `transition.go`, or `transition_test.go`. No blocked-by set.
