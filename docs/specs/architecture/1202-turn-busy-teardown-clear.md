# Spec #1202 — clear the turn-busy signal on session teardown transitions

Ticket: [#1202](https://github.com/pyrycode/pyrycode/issues/1202) · size `s` · `security-sensitive` · split from #1198 (siblings: #1201 landed, #1203 outstanding)

## Files to read first

| Path | What to extract |
|---|---|
| `cmd/pyry/stream_turn_busy.go:11-48` | The type doc: the `KNOWN GAP` register this slice half-closes and corrects (`:27-41`), and the `SECURITY: content-free` posture (`:43-48`) the new method must inherit. |
| `cmd/pyry/stream_turn_busy.go:102-163` | `observe` — the exact three-step shape the clear reuses: resolve **outside** `t.mu` (`:122-130`), unresolved → Debug-and-skip (`:131-143`), then mutate + close-and-replace `t.changed` under **one** acquisition (`:145-162`). Also the nil-receiver guard at `:103-105`. |
| `cmd/pyry/stream_turn_busy_test.go:319-341` | `TestTurnBusyTracker_ImportsStayMinimal` — a **closed** import set (`context`, `turnevent`, `log/slog`, `sync`). This is a hard design constraint, not a preference: the clear must not drag `internal/sessions` into this file. |
| `cmd/pyry/stream_turn_busy_test.go:19-44` | `stubBusyResolve` and `requireWaitIdle` — the two helpers the new tracker-tier tests reuse verbatim. |
| `cmd/pyry/session_transition_v2.go:180-252` | `toWirePayload` (the closed reason switch + the "`NewSessionID` is the live binding id for both reasons" semantics) and `startSessionTransitionStreamV2` (the single install site + its install-before-`Pool.Run` doc). |
| `cmd/pyry/session_transition_v2.go:120-141` | `broadcast`'s resolve step — the precedent for "resolve once, off the per-conn loop, drop on unresolvable" and the `conversation_id`-is-never-logged rule. |
| `cmd/pyry/relay.go:697-740` | The `var (...)` cleanup block (`:697-701`) that gains the hoisted declaration, the `w.streamSink != nil` gate (`:702`), and the tracker construction + comment to amend (`:726-740`). |
| `cmd/pyry/relay.go:756-770` | The **unconditional** transition install — the asymmetry that makes the nil tracker reachable in PTY mode. |
| `cmd/pyry/relay.go:813-840` | `conversationForSession` — the empty-`sid` guard (`:831-833`) and the `CurrentSessionID` **or** `SessionHistory` match (`:835`). |
| `internal/sessions/transition.go:30-64` | The `TransitionObserver` contract (synchronous, no lock held, MUST NOT block) and `notifyTransition`'s rebind-**before**-fan-out ordering. |
| `internal/sessions/session.go:665-699` | `beginEvict` — the eviction fire site. Note it calls `notifyTransition` **before** `s.lcMu.Lock()` and before the `stateEvicted` flip. |
| `internal/conversations/registry.go:79-90` | `Save` releases `r.mu` **before** any file I/O — so a concurrent `List()` never blocks behind an fsync. Load-bearing for the synchronous-clear decision. |
| `internal/conversations/registry.go:232-246` | `RebindSession` — the single production writer of `SessionHistory`. |
| `cmd/pyry/streamsup_runner.go:101-110` | `scfg.Stdout = streamsup.NewParser(sink.sinkFor(cfg.SessionID), …)` — the producer tag is fixed at **runner construction**. This is the derivation behind the comment correction in AC#5. |
| `cmd/pyry/interactive_turn_v2_test.go:45-91` | `discardLogger`, and `fakeInteractiveBcast` — read its doc comment closely: **it has no mutex** because "the emitter spawns no goroutine". That assumption is false for this slice's tests. See § Testing strategy. |
| `cmd/pyry/session_transition_v2_test.go:100-150` | `mixedSnapshot` / `TestSessionTransitionBroadcast_Clear` — the assertion idiom the new wiring test mirrors. |
| `internal/sessions/transition_test.go:500-540` | `TestPool_Eviction_BindingNeutral` — the proof that an evicted id is still `CurrentSessionID` at observer time. Read it; **do not extend it** (see § Testing strategy). |

## Context

`turnBusyTracker` (#1201) closes a turn only when its `TurnEnd` arrives on the stream-json fan-in. A session torn down mid-turn produces no `result` line, so the conversation stays reported busy forever. Two teardowns reach that state and `internal/sessions` already observes both — a `/clear` rotation (`ReasonClear`) and an eviction (`ReasonEviction`) — surfaced through the pool's single `TransitionObserver` slot.

This slice hangs the clear off that existing signal. The tracker is still **unwired**: nothing reads `Busy`/`WaitIdle`, no v2 frame changes, no delivery behaviour changes. After this lands, #1203 (mid-turn child death + respawn) is the last gap before the signal is safe to consult.

The failure directions are asymmetric and that asymmetry drives every design call below. A **missed** clear wedges a conversation busy (annoying, fails closed). A **spurious** clear reports a live turn as idle, which under the consuming slice releases a mid-turn send into a running conversation. Session→conversation resolution is therefore the security-relevant part of this slice.

## Design

Three production files, no new files, no new exported identifiers.

### 1. `cmd/pyry/stream_turn_busy.go` — a session-keyed clear

Extract the mutate-and-broadcast block currently inline in `observe` (`:145-162`) into an unexported helper, then add the clear on top of it:

```go
// setBusy applies one membership change under a single t.mu acquisition and
// broadcasts on t.changed only when the set actually moved.
func (t *turnBusyTracker) setBusy(conversationID string, open bool)

// clearForSession closes any open turn on the conversation that owns sessionID.
// A nil receiver is a no-op. Idempotent; a conversation that is already idle
// is not re-broadcast.
func (t *turnBusyTracker) clearForSession(sessionID string)
```

`setBusy` is the current `:145-162` block verbatim (membership check → mutate → `close(t.changed)` + replace), with `observe`'s tail becoming `t.setBusy(convID, opens)`. This is not opportunistic refactoring: the close-and-replace protocol is the subtle invariant `WaitIdle`'s check-and-subscribe atomicity depends on, and hand-duplicating it in a second method is the bug this extraction forecloses.

`clearForSession` is `observe`'s structure minus the event switch:

1. nil-receiver guard (mirrors `:103-105`);
2. `convID, ok := t.resolve(sessionID)` — **outside** `t.mu`, same lock-order reason as `:122-130`;
3. `!ok || convID == ""` → Debug `stream_turn.clear_unresolved` with `session_id` only, return;
4. `t.setBusy(convID, false)`.

**Why session-keyed, not conversation-keyed.** A `clearConversation(convID)` method would be a shorter call chain (the composed observer already holds a session→conversation resolver), but it opens a clear that accepts a conversation id from *anywhere*. The file's `SECURITY` block (`:43-48`) states the key "is never taken from the wire — it is resolved daemon-side." Keeping the clear session-keyed means the only way to clear anything is to name a session id, and the only producer of those is the pool's own transition. Resolution also stays in one file with one lock-order rule and one unresolved-skip log.

**Why the tracker's own injected `resolve` and not the caller's.** They are the same closure — `relay.go:739` and `:770` are character-identical. Using the tracker's keeps `stream_turn_busy.go` self-contained.

Also in this file: rewrite the `KNOWN GAP` register (`:27-41`) per AC#5 — see § 4.

### 2. `cmd/pyry/session_transition_v2.go` — reason mapping + observer composition

```go
// transitionClearsTurn reports whether t tears a session down under its
// conversation and, if so, the session id that is that conversation's LIVE
// binding at observer time. Delegates to toWirePayload so this file carries one
// closed reason switch, not two: an unknown reason returns ok=false (no clear),
// the same drop toWirePayload already forces on the wire path.
func transitionClearsTurn(t sessions.SessionTransition) (sessionID string, ok bool)
```

Body is three lines: `p, ok := toWirePayload(t); if !ok { return "", false }; return p.NewSessionID, true`.

`toWirePayload`'s `NewSessionID` is already documented (`:128-132`) as "the live binding id for both reasons" — `NewID` post-rebind for `ReasonClear`, the mirrored `PreviousID` for the binding-neutral `ReasonEviction`. That is exactly the id this clear needs, so reuse is not a coincidence, it is the same question asked twice.

Using `NewID` rather than `PreviousID` on the clear path matters: `NewID` is the conversation's `CurrentSessionID` (the **primary** match at `relay.go:835`), so the clear does not depend on `RebindSession`'s `SessionHistory` append nor on the rebind-before-fan-out ordering. `PreviousID` would also resolve today, via history — but only for as long as that ordering holds.

`startSessionTransitionStreamV2` gains one parameter, placed to mirror `startStreamTurnDrainV2`'s `(…, busy, logger)`:

```go
func startSessionTransitionStreamV2(
	ctx context.Context,
	sink transitionObserverSink,
	bcast interactiveBroadcaster,
	resolveConv func(string) (string, bool),
	busy *turnBusyTracker, // may be nil (PTY mode) — clearForSession is nil-safe
	logger *slog.Logger,
) func()
```

and installs a composed observer instead of `emitter.Enqueue` directly:

```go
sink.SetTransitionObserver(func(t sessions.SessionTransition) {
	emitter.Enqueue(t)                       // incumbent FIRST — see below
	if sid, ok := transitionClearsTurn(t); ok {
		busy.clearForSession(sid)
	}
})
```

**Incumbent first.** `Enqueue` is a documented non-blocking buffered send that cannot be delayed by anything downstream of it. Running it first keeps the existing producer's behaviour — including its drop-on-full decision — timed identically to today relative to the pool goroutine's progress, which is what AC#3's "every transition that reaches its emitter today still reaches it" actually asks for.

**Type stays concrete.** `*turnBusyTracker`, never an interface — a typed-nil in an interface is non-nil at the interface level and routes straight past the guard into a nil-map read. Same rule `startStreamTurnDrainV2:114-118` already documents.

**Also correct two stale citations** in this function's doc comment while editing it (`:218-221`): `startRelay` is now `main.go:984` and `pool.Run` is `main.go:1066`, not `:489`/`:514`. AC#3 leans on that comment as the authority for the install-before-`Run` ordering, so a citation that no longer resolves is a live defect in this slice's evidence chain, not adjacent cleanup.

### 3. `cmd/pyry/relay.go` — hoist the declaration

Add `busy *turnBusyTracker` to the existing `var (…)` block at `:697-701`. Inside `if w.streamSink != nil` the construction at `:738` becomes an assignment (`busy = newTurnBusyTracker(...)`) rather than a `:=`. Pass `busy` at the `startSessionTransitionStreamV2` call (`:769`).

In PTY mode the branch never runs, `busy` stays nil, and the unconditional install at `:769` hands a nil tracker to a nil-safe method. That is the whole of AC#4.

Amend the comment at `:733-737`: the value is still a local and still not a `relayWiring` field, and the signal is still unwired for delivery — but it is now read by the transition wiring below, and #1203 alone remains outstanding.

### 4. Comment register (AC#5)

Two sites name this ticket. Confirm the starting count before editing — `grep -n '#1202' cmd/pyry/*.go` must return exactly **2** hits (`stream_turn_busy.go:32`, `relay.go:736`); a third means a site was added after this spec was written.

Three sentences must change:

1. `stream_turn_busy.go:27-29` — "the clear is event-driven only … Two paths reach a permanently-busy conversation" → the clear now has two feeds (the fan-in's `TurnEnd`, and pool teardown transitions), and **one** path remains open: #1203.
2. `stream_turn_busy.go:32-35` — the rotation-edge claim ("because `conversationForSession` matches `SessionHistory`, a retired session's late `TurnEnd` can clear a turn its successor opened") is **corrected, not defended**. It is unreachable as written, and the replacement text should record why in one or two sentences: a `/clear` re-keys **one** pool entry in place, and the parser's sink tag is fixed at runner construction (`streamsup_runner.go:105`) while `RestartFresh` rotates only the runner's internal spawn id — so every id reachable through `SessionHistory` belongs to the *same* runner that continues under the successor id. There is no second, concurrently-producing runner whose tag resolves to the same conversation. (Eviction cannot supply one either: binding-neutral, so an evicted id never enters `SessionHistory`.) Do **not** add a guard for it.
3. `relay.go:736` — "#1202 and #1203 are the two clears that must land" → #1203 is the last one.

I re-derived the unreachability independently from the ticket body and reached the same result; the load-bearing step is the construction-time tag at `streamsup_runner.go:105` combined with `RebindSession` being `SessionHistory`'s only production writer (`internal/conversations/registry.go:241`, reached solely via `transition.go:78` ← `:59`). One benign case survives and is correct, not a bug: between an eviction and the conversation's next binding the evicted id is still `CurrentSessionID`, so a late `TurnEnd` from the dying child resolves and clears — the right answer for that conversation, and idempotent with this slice's own clear.

**Out of scope for the developer:** `docs/knowledge/features/streamsup-package.md:29-32` carries the same retired claim. That file belongs to the documentation phase — see § Open questions.

## Concurrency model

No new goroutines. The clear runs **synchronously** on the goroutine that fired the transition (the pool's lifecycle goroutine for eviction via `beginEvict`, the rotation-watcher goroutine for clear via `onRotate` / `RotateForNewSession`).

**This is the load-bearing decision of the slice**, because `TransitionObserver`'s contract says "MUST NOT block — hand the signal off to a buffered channel and return." Synchronous satisfies it:

- The work is one `convReg.List()` (a slice-header copy under `r.mu`), one map delete, and one `close()`. All bounded; `close` never blocks; no channel receive, no I/O, no callback into another subsystem.
- `List()` cannot block behind disk I/O: `Save` copies under `r.mu` and **releases it before** `MkdirAll`/write/fsync/rename (`registry.go:87-90`), holding only the separate `saveMu` across the file work.
- The same pool goroutine already performs a full atomic write (`convReg.Save`, including fsync) inline one line earlier on the `/clear` path — `notifyTransition` → `rebindConversation` → `Save` (`transition.go:57-64`, `:81`). A mutex-guarded slice copy is orders of magnitude cheaper than what that goroutine already pays before the observer is even called.
- Lock order is unchanged and already established: `convReg.mu` → `tracker.mu`, never the reverse, because resolve happens outside `t.mu` (`stream_turn_busy.go:122-130`). No pool or session lock is held during the observer call.

The buffered hand-off the contract suggests exists because the *incumbent* consumer does genuinely blocking work — an `ActiveConns` snapshot and a per-conn `Push` over the transport. A leaf-mutex membership delete is not that.

Two properties come free from synchronous:

- On eviction, `beginEvict` fires the observer **before** `s.lcMu.Lock()` and before the `stateEvicted` flip (`session.go:686-698`). So the tracker reports idle strictly before any consumer can observe the session as evicted — the same "signal ordered ahead of the flip" guarantee #1186 established for the wire.
- Tests assert `Busy` directly after invoking the observer, with no barrier. `WaitIdle` is not needed as a test barrier (the ticket flagged it as the fallback if the clear went async); it still gets its own test because consumers will depend on it.

`WaitIdle` waiters woken by the clear re-acquire `t.mu` on **their** goroutines; the clearing goroutine never waits on them.

## Error handling

| Condition | Behaviour |
|---|---|
| nil tracker (PTY mode) | `clearForSession` returns immediately. Emitter still runs. |
| Unknown/future `TransitionReason` | `transitionClearsTurn` → `ok=false`; no clear. Whitelist, matching `toWirePayload`'s existing drop. |
| Session resolves to no conversation | Debug `stream_turn.clear_unresolved` (`session_id` only), no mutation. Expected on bootstrap eviction and on an evicted id whose conversation has re-bound elsewhere. |
| Conversation already idle | `setBusy` no-ops; **no** broadcast (no spurious `WaitIdle` wakeups). |
| Empty session id | Cannot reach a clear: `conversationForSession` guards `sid == ""` (`relay.go:831-833`). |

No error is returned or propagated anywhere — a teardown clear has no caller that could act on one, and the observer contract has no error channel.

## Testing strategy

All unit-tier, in two existing test files. **No new e2e test and no new e2e build-tag work** — AC#5 asks only that existing stream-mode e2e still passes.

`cmd/pyry/stream_turn_busy_test.go` (reuse `stubBusyResolve`, `requireWaitIdle`, `discardLogger`, `testConvID`/`testConvIDB`):

- **Table test, `clearForSession` semantics.** Sub-cases: (a) a conversation opened via `observe(sess-a, TextChunk)` is idle after `clearForSession("sess-a")`; (b) `clearForSession` on a session absent from the resolver leaves an unrelated busy conversation untouched; (c) clearing an already-idle conversation is a no-op; (d) `var tr *turnBusyTracker; tr.clearForSession("x")` does not panic.
  - Case (a) is AC#1/AC#2's teeth and must feed **no** `TurnEnd` anywhere in the test — the failure mode being closed is the one where the stream went silent.
  - Case (b) is the spurious-clear direction and the more important half.
- **`clearForSession` wakes a blocked `WaitIdle`.** Mirror `TestTurnBusyTracker_WaitIdleBlocksUntilTurnEnd` (`:202`): block a waiter on a busy conversation, clear, assert `nil` within a deadline. This is the test that fails if the close-and-replace is skipped.
- `TestTurnBusyTracker_ImportsStayMinimal` must stay green **unedited**. If it needs a new import added to its `want` list, the design has gone wrong.

`cmd/pyry/session_transition_v2_test.go`:

- **`transitionClearsTurn` table.** `ReasonClear{prev,new}` → `("new", true)`; `ReasonEviction{prev,""}` → `("prev", true)`; `TransitionReason("workspace_change")` → `("", false)`.
- **Composed-observer table test.** A fake `transitionObserverSink` captures the installed observer; call `startSessionTransitionStreamV2` with a real tracker pre-loaded busy, invoke the captured observer, then assert **both** halves. Sub-cases: `ReasonClear`, `ReasonEviction`, and **nil tracker** (AC#4 at the wiring tier — no panic, envelope still emitted).
  - Busy-half assertion is direct (`busy.Busy(conv)` — the clear is synchronous).
  - Incumbent-half assertion needs a barrier because `emitter.Run` is async.
- **Concurrent-fires test.** Fire the composed observer for two conversations from separate goroutines (**N = 8 total**, comfortably under `sessionTransitionQueueSize = 16` so no drop-on-full flake) while readers call `Busy`. Assert all 8 envelopes arrive and both conversations end idle. Value is the `-race` run.

Two traps to spec explicitly:

1. **Do not reuse `fakeInteractiveBcast` for the wiring tests.** Its doc comment (`interactive_turn_v2_test.go:57-62`) states it carries no mutex because "the emitter spawns no goroutine" — true for the existing tests, which call `e.broadcast(...)` directly on the test goroutine. `startSessionTransitionStreamV2` **does** spawn `emitter.Run`, so `Push` appends to `f.pushes` from that goroutine while the test reads it: a `-race` failure. Write a small local double whose `ActiveConns` returns a fixed read-only slice and whose `Push` does a buffered-channel send; receive from that channel with a ~2s deadline as the barrier. Do not add a mutex to the shared fixture.
2. **Do not extend `internal/sessions/transition_test.go`.** The ticket suggests `TestPool_TransitionObserver_RaceConcurrentFires:608` as the home for the composition's race proof, but the composed observer lives in `package main`; that test cannot reference it. The sessions-side tests stay untouched — they already prove both reasons reach the observer, which is precisely the seam this slice consumes rather than changes.

**Non-vacuity check.** Delete the `busy.clearForSession(sid)` line from the composed observer: the clear sub-cases of the composed-observer test must go red while the incumbent half stays green. Run it once; restore.

**Verification:** `go test -race ./cmd/pyry/...`, then `make check`.

## Open questions

- **For the documentation phase, not the developer.** `docs/knowledge/features/streamsup-package.md:29-32` repeats the retired rotation-edge claim and attributes it to #1202. Once this merges it is wrong on both counts (the ticket is closed; the hazard is unreachable). `docs/knowledge/codebase/1201.md` may carry it too. Correcting shared knowledge docs is documentation's job, so it is deliberately **not** a developer AC — flagging it here so it is not lost.
- **`transitionClearsTurn` via `toWirePayload` couples the clear to a wire mapping.** If a future reason ever needs to fire the clear but *not* the wire event (or vice versa), the shared switch must split. Today the two questions have the same answer for every reason the pool produces, and `TestToWirePayload` already pins it, so one switch is correct now. Noted so a future divergence is recognised as a split rather than patched with a special case.

## Security review

**Verdict:** PASS

This ticket is `security-sensitive` because it introduces the **clear** half of a gating signal. Per the ticket's own framing and [[security-sensitive-label-tracks-design-not-lineage]], the label tracks the design property: the consuming slice will treat "idle" as permission to release a send into a conversation, so a spurious clear opens the gate on a live turn. Walking the categories:

**Trust boundaries / where the key comes from (the core property).** The clear's parameter is a **session id**, and the only production producer of that value is `internal/sessions`' own `SessionTransition` — the pool's record of a lifecycle event it performed itself. The conversation key is then resolved daemon-side by the tracker's injected `conversationForSession` closure, exactly as `observe` does. Nothing on the v2 wire, in the stream bytes, or from a child process reaches this path. The rejected `clearConversation(convID)` shape is the reason this matters: it would have accepted a conversation key from any caller and quietly retired the "the key is never taken from the wire" invariant the type doc asserts at `stream_turn_busy.go:43-48`. Recorded here as an active decision, not a N/A.

**Spurious-clear containment (the failure direction that costs).** Four structural bounds, each testable: (a) the reason set is a **whitelist** — an unknown/future `TransitionReason` clears nothing, inheriting `toWirePayload`'s existing closed switch rather than a "not-X ⇒ clear" blacklist; (b) an unresolvable session **skips**, never falls back to a wildcard or empty-key clear (the empty key is separately unreachable — `conversationForSession` guards `sid == ""`); (c) the mutation is a single-key `delete`, never a bulk reset, so the blast radius of any resolution error is exactly one conversation; (d) the clear is scoped to the id the pool named, not to the active-conversation cursor. Test case (b) in the tracker table — busy conversation survives a clear for an unrelated session — is the direct assertion.

**The rotation edge is closed by derivation, not by a guard.** #1201 flagged a suspected path where a retired id resolving through `SessionHistory` lets a late `TurnEnd` clear its successor's turn. Independently re-derived here (§ 4) and found unreachable: a `/clear` re-keys one pool entry in place, and the parser's sink tag is fixed at runner construction (`streamsup_runner.go:105`), so there is never a second concurrently-producing runner whose tag resolves to the same conversation; `RebindSession` (`registry.go:241`) is `SessionHistory`'s only production writer. Building a speculative guard against an unreachable path would add a branch on the security-relevant resolution step with no observed failure to calibrate it against — the more expensive error here. The comment is corrected instead.

**Confidentiality / logging.** Content-free, and one field is deliberately withheld: the new Debug log carries `session_id` only. It must **not** log the resolved `conversation_id`, because `sessionTransitionEmitterV2`'s SECURITY block (`session_transition_v2.go:38-47`) classifies `conversation_id` as a routing key treated as sensitive alongside session ids and `workspace_cwd` — resolved, stamped on the wire, never logged. Session ids are already standard non-secret log fields across `internal/sessions`. No event content, no turn text, no counts, no timestamps enter the tracker or the log.

**Existence oracle.** Unchanged. `clearForSession` returns nothing — no bool, no error — so it cannot report whether a conversation existed, was busy, or resolved. `Busy`'s deliberately un-widened `(bool)` signature (`stream_turn_busy.go:165-175`) is untouched.

**Availability / DoS.** Two surfaces. (1) The synchronous clear runs on the pool's lifecycle/watcher goroutine; bounded to a mutex-guarded slice copy + map delete + `close`, strictly cheaper than the fsync that goroutine already performs one line earlier on the same path (§ Concurrency model), with no new lock order and no lock held across a callback. A wedged fan-out cannot stall the pool because there is no fan-out. (2) The nil-tracker guard (AC#4) is an availability control, not a nicety: the observer is installed **unconditionally** while the tracker exists only under `w.streamSink != nil`, so an unguarded clear panics on the pool's lifecycle goroutine at the first `/clear` or eviction **in the daemon's default PTY mode** — a crash on the supervisor's own goroutine, reachable without any remote input. Guarded at the type (nil receiver) and asserted at both tiers.

**Injection / tokens / secrets / crypto / file operations.** N/A. No untrusted data reaches a control surface (the reason switch matches package-local constants; the id is pool-minted). No new filesystem, exec, or crypto operations — the clear performs no I/O. No wire frame changes: the slice ships unwired, so no new bytes reach any transport.
