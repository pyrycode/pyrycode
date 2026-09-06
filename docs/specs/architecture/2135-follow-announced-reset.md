# #2135 — follow claude's announced reset: re-key the session, draw the delimiter, keep the stream live

## Files read

- `internal/streamsup/parser.go` → `emitConversationReset` — the producer. It gates on `transcript.ValidStem` before constructing the event, so `NewConversationID` reaches every consumer already validated as a canonical lowercase UUID stem. It has no logging surface at all, deliberately.
- `internal/turnevent/event.go` → `ConversationReset` — the one-field event this ticket consumes.
- `internal/sessions/transition.go` → `onRotate`, `RotateForNewSession`, `notifyTransition`, `rebindConversation` — the re-key + rebind + notify sequence this ticket must reuse rather than reimplement. `RotateForNewSession`'s doc states the skip-set asymmetry that decides whether a new entry point registers the announced id.
- `internal/sessions/pool.go` → `RotateID`, `rekeyLocked` — `RotateID` checks membership FIRST and only then returns nil for `oldID == newID`, which is why `onRotate` draws a spurious delimiter on an equal-id rotation (AC3's trap).
- `internal/sessions/pool.go` → `New`, `create` (the two `RunnerConfig` construction sites) — both are inside `Pool` scope; `New`'s literal sits under a `var p *Pool` that the `&Pool{}` literal below assigns, the established late-bind for a closure that only fires long after `New` returns.
- `internal/sessions/runnerstate.go` → `RunnerConfig` — `SessionID`'s own doc says it is construction-fixed and "does NOT mirror a /clear rotation", which decides the shape of the new seam: it takes BOTH ids as arguments rather than closing over one.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — where the live tag, the parser and the two fan-in lanes are bound from one object.
- `cmd/pyry/stream_turn_drain.go` → `streamSessionTag`, `Rotate`, `sinkForTag`, `startStreamTurnDrainV2` — the tag's type doc names `streamsup.Config.OnSessionRotate` as its only writer, which this ticket falsifies and must correct in place. `sinkForTag` reads the tag once per event; the drain's active-session gate is what drops a stale tag's events.
- `cmd/pyry/session_model_hold.go` → `newSessionParser`, `sessionRetentions` — the decorator chain, and the doc sentence that states the property this ticket needs: "what puts each retention upstream of the fan-in send is that all four sit on the parser's side of the channel — not where any of them sits in the chain."
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — `ConversationReset` lands in the default arm (`turnMarkNone`, droppable class), pinned by `TestTurnMarkFor_TotalOverEveryVariant`. Not to be changed.
- `cmd/pyry/session_background_task_hold_test.go` → `TestSessionBackgroundTaskHold_RetainsPastASaturatedSink` — the AC5 test shape, including the before-and-after `len(sink.ch)` conjunction.
- `cmd/pyry/relay.go` → `runConfigFor` / `snapshotUsageFor` call — AC4's observable: one expression over a named conversation's bound session id.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay`, `writeStreamInteractiveConfig`, `seedBoundConversation` — the stream-mode e2e entry point; `extraEnv` is variadic, so the fakeclaude rider needs no harness change.
- `internal/e2e/relay_v2_stream_run_config_test.go` → `TestRelayV2_StreamRequestSessionSettings` — the AC4 template: seed a conversation, request settings, assert `session_id` + the context-window figures.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON` — the rider-parameter convention (each rider lands after the previous one so a transposed call site is a compile error) and the "write the rider BEFORE the reply so a turn_end implies the rider already went through the parser" happens-before.
- `docs/knowledge/features/streamsup-package-conversation-reset-consumed-by-matching.md` — #2134's decode half; confirms the consumer re-derives no validity question.
- `docs/knowledge/features/sessions-package-key-types-transition-observer.md` — the observer's must-not-block contract.

## Context

An in-process `/clear` is invisible to the daemon. claude announces the reset on its own stdout and mounts a fresh transcript under the announced id, then stops writing the old one. Nothing connects that announcement to the pool, so no `session_transition` is drawn and `snapshotUsageFor` keeps resolving a transcript that will never grow again — the gauge is frozen for the rest of the session, not stale for one turn.

#2134 landed the decode half: `turnevent.ConversationReset` is produced by the real parser. This ticket is the consumption half — the pool re-key, the delimiter, and the sink-tag rotation that keeps the still-running child's later events reaching the client.

No ADR is warranted. Every decision here is an application of an existing one (the decorator chain's placement argument, the live-tag argument, `RotateForNewSession`'s skip-set asymmetry).

**Size overage, stated rather than routed back.** This plan prescribes new or modified content for 7 production source files and lands ~950 lines of total written work, over the 5-file and 800-line lines of the size table. The refiner measured the same overage and argued it under the size floor; I re-derived it and agree. Every available cut produces a child whose only consumer is a sibling in this family and which changes nothing observable on its own: the pool entry point alone is unreachable code, the decorator alone observes an event and discards it, the tag rotation alone has nothing to rotate it, and the fakeclaude rider alone emits bytes no test reads. Worse, landing the registry re-key WITHOUT the tag rotation is not a smaller ticket but a regression — the conversation goes dark until the daemon restarts. Per "when the floor and the ceiling disagree, the floor wins", this builds as one ticket.

## Design

### The observation seam

The reset must be observed **on the parser's side of the fan-in channel**. Two constraints bind simultaneously and only that placement satisfies both:

1. Not behind the drain's active-session gate, which drops every event whose producing session is not the active conversation's bound session — a reset observed there would never rotate a background conversation.
2. Upstream of the fan-in send, because `ConversationReset` is droppable class (`turnMarkFor`'s default arm) and `sinkForTag` refuses it at `droppableCap` under load. Anything reading the event back out of the channel loses the reset exactly when AC5 fires.

New type `sessionResetFollower` in a new file `cmd/pyry/session_reset_follow.go`, one more decorator of the same shape as the four retention holds:

```go
type sessionResetFollower struct { /* tag, adopt, next, logger */ }
func newSessionResetFollower(tag *streamSessionTag, adopt func(oldID, newID string) error,
    next func(turnevent.Event), logger *slog.Logger) *sessionResetFollower
func (f *sessionResetFollower) Sink(ev turnevent.Event)
```

`Sink` acts on a `ConversationReset` and then forwards **every** event of every variant unchanged, `ConversationReset` included — the same contract each hold's `Sink` states.

**It is deliberately NOT a fifth member of `sessionRetentions`, and not minted by `newSessionParser`.** It retains nothing and exposes no accessor; it is an *action* on a per-runner object. Putting it inside `newSessionParser` would force that function to take the runner's live tag and the pool callback — runner-level dependencies it has no business knowing — and would cost the five call sites #2106 just paid to make free. Instead the factory chains it at the tail: it decorates `sink.sinkForTag(tag.ID)` and is passed as `newSessionParser`'s `next`. The chain's own doc already states why the position does not matter: what puts a link upstream of the fan-in send is sitting on the parser's side of the channel, not where in the chain it sits.

### What `Sink` does on a reset

Ordered, and each step's placement argued:

1. Read `old := f.tag.ID()` — the **live** id, never a construction-time one. A `RunnerConfig.SessionID` captured in a closure would be right for exactly one rotation and fail-closed after that.
2. If `newID == old`, return without touching anything (AC3: no re-key, no transition, no tag rotation). The tag guard can only live here — the pool cannot guard a value it does not own.
3. `f.tag.Rotate(newID)` — **before** the pool call, and **unconditionally on the differing-id path**. Unconditional because the rotation watcher may have re-keyed the pool first (it fires on the new transcript's CREATE), in which case the pool call returns `ErrSessionNotFound` while the conversation is already bound to the announced id; a tag rotation conditional on the pool's success would leave that conversation dark, which is the exact failure this ticket exists to prevent. Before, because the pool call's `notifyTransition` rebinds the conversation, and the rebind is what makes the old tag dead — rotating first means no other goroutine can observe the rebind while the tag still reports the old id.
4. `f.adopt(old, newID)` — the pool seam. Its error is logged and swallowed; the pool decides whether a transition fires.

The whole sequence runs synchronously on claude's stdout forwarder goroutine, which is also the goroutine that produces subsequent events. No event of this session can therefore be produced between the tag rotation and the rebind. An asynchronous hand-off was rejected: it would reopen exactly that window and make AC2 a race.

The forwarded `ConversationReset` is tagged with the **new** id, because `sinkForTag` reads the tag after `Sink` returns. Immaterial either way — `busy.observe` is a no-op for the variant (`turnMarkNone`) and `interactiveTurnEmitterV2.Handle` has no arm for it — but stated so a reader of a drop record knows which id to expect.

### The pool entry point

New exported method in `internal/sessions/transition.go`:

```go
func (p *Pool) AdoptAnnouncedID(oldID, newID SessionID) error
```

The third sibling of `onRotate` and `RotateForNewSession`. Its **body shape is `RotateForNewSession`'s**, not `onRotate`'s: one `Pool.mu` hold covering every check and the re-key, then `notifyTransition` off the lock. It shares `rekeyLocked`, which is where the re-key invariant lives, so this is the established in-package composition rather than a second copy of the sequence — and unlike `onRotate` it cannot delegate to `RotateID`, because one of its checks must be inside the same critical section as the mutation (below). Contract, in order:

- `oldID` absent → `ErrSessionNotFound`, no mutation, no transition. This is the coexisting rotation watcher's path when it wins the race: it already re-keyed, so `oldID` is gone. That is what makes AC1's "exactly one" structural rather than likely.
- `oldID == newID` → nil, **no re-key and no transition**. The AC3 guard at the seam that owns the invariant: `RotateID` checks membership first and only then no-ops on equal ids, so `onRotate`'s "rotate then notify unconditionally" shape draws a spurious delimiter. The new method must not repeat it. The follower's own equal-id guard is not a duplicate — it guards the tag, which the pool cannot reach.
- `newID` already names a **different** live session → refused with a distinct error, no mutation, no transition. `rekeyLocked` moves a map entry without checking the destination, so adopting a colliding id would overwrite another session's entry and silently swallow it. This path is reachable from the child's stdout with one line, which is a cheaper trigger than the watcher's (a real file creation), so the check belongs at this entry point. It is inside the `Pool.mu` hold with the mutation because a check outside it is a TOCTOU.
- Otherwise `rekeyLocked` + `saveLocked`, then `notifyTransition` with `ReasonClear`. A `saveLocked` failure is logged at Warn and swallowed, matching `RotateForNewSession` and `rebindConversation`: the in-memory re-key is already authoritative and durability is best-effort.
- It does **not** register the announced id in the allocated skip-set, matching `onRotate` and diverging from `RotateForNewSession`. claude created `<newID>.jsonl` itself; the id is deliberately un-allocated, which is how the watcher detects a real self-rotation. Retiring the watcher is #2137.
- It does **not** arm `Runner.BeginRotation`. No child is replaced here and claude keeps running, so arming the rotation gate would refuse turns for no reason.

### Wiring: `RunnerConfig`, not a late-bound singleton in `main.go`

`newStreamRunnerFactory` is called from `selectInteractiveRunner` before the pool exists, so a pool callback cannot be captured at factory-construction time. Of the two open routes, this plan takes the `sessions.RunnerConfig` field:

```go
// RunnerConfig
AdoptAnnouncedReset func(oldID, newID string) error
```

set at both construction sites to a closure over the pool. `Pool.New`'s site uses the `var p *Pool` late-bind already documented there for exactly this timing; `Pool.create`'s site has `p` in hand.

Chosen over a late-bound holder in `main.go` because the pool *builds* `RunnerConfig` per session and it already reaches the factory, so the route costs no new type, no setter, no mutable singleton and not one line in `main.go` — and because the alternative would put a settable global on the composition root for a value the pool can simply hand over.

**It takes both ids as parameters.** That is the whole reason it can live on a per-session config whose `SessionID` is construction-fixed: the closure carries no identity at all, and the caller supplies the live one from the tag.

`mapStreamsupConfig` is untouched — the field is a runtime object, on the same dividing line `Stdout` and `PostureGate` already sit on.

### The comment correction

`streamSessionTag`'s type doc says the tag "is written through `streamsup.Config.OnSessionRotate`, which the runner fires from `RestartFresh`". This ticket adds a second writer on a path that never touches `RestartFresh`, so the sentence becomes false the moment the code lands and nothing reddens when it does. It is corrected where it stands, in this change, to name both writers.

### fakeclaude: the announcement rider

New env `PYRY_FAKE_CLAUDE_STREAM_RESET_TO=<uuid>`, read at startup and threaded to `runStreamJSON` as a trailing `resetToID string` rider — appended after `modelWindows bool` so the tail reads `bool, string` and a transposed call site stays a compile error, per the convention that parameter list already states.

On the FIRST user turn only, and BEFORE the reply, the rider writes one line:

```json
{"type":"conversation_reset","new_conversation_id":"<uuid>"}
```

Before the reply for the reason every sibling rider states: a `turn_end` reaching a client then implies the reset line has already been through the parser, so the e2e needs no sleep and no poll. First turn only so a second turn does not re-announce the same id (which would be an equal-id no-op, but determinism is cheaper than reasoning about it).

The bytes are #2088's captured shape, and #2134's `conversationResetLineFixture` marshals the same two keys.

## Concurrency model

No new goroutines. Every part of this runs on goroutines that already exist:

- **claude's stdout forwarder** runs `Sink`, the tag rotation and the pool call. It is the same goroutine `streamsup.Parser` already runs on, and os/exec's `cmd.Wait` join means forwarder N+1 cannot start until forwarder N finished — the serialisation the parser's own doc asserts. So the follower has exactly one writer.
- **The pool's rotation watcher** may call `onRotate` for the same rotation concurrently. Both paths converge on `RotateID` under `Pool.mu`, so the second one to arrive finds `oldID` gone and returns `ErrSessionNotFound` without a transition.
- **The transition observer** is invoked synchronously from whichever goroutine owns the transition — now including the stdout forwarder. The installed observer (`session_transition_v2.go`) hands to a buffered channel and returns, which is exactly the contract `TransitionObserver` states.

**Locks.** The follower holds none: `streamSessionTag` is a single atomic word (its doc argues why it is not a mutex), and the pool call takes `Pool.mu` → `Session.lcMu` in the established order, off any lock of ours. The new edge is that `Pool.mu` is now taken from the stdout forwarder goroutine as well as from the watcher goroutine. That is not a new ordering — it is the same lock taken from one more caller, and `notifyTransition` is a documented off-lock leaf callback. The cost is that a `saveLocked` disk write briefly stalls the child's stdout drain; bounded by one small JSON write, and the same stall the watcher path already imposes on its own goroutine.

**Shutdown.** Nothing to add: the follower's lifetime is the parser's, which is the runner's, which is the session's.

## Error handling

| Failure | Handling |
|---|---|
| `newID == old` | Return before any mutation. Not an error — AC3's specified behaviour. |
| `adopt == nil` (no pool wired: unit tests, PTY-mode rollback) | Tag still rotates; no pool call. The follower is usable with a nil callback the way each hold is usable with a nil `next`. |
| `AdoptAnnouncedID` → `ErrSessionNotFound` | **Expected**, not exceptional: it is what the watcher racing us produces on every real reset today. Logged at Debug. Warn here would fire on the normal path. |
| `AdoptAnnouncedID` → any other error (a `saveLocked` failure surfaced by `RotateID`) | Logged at Warn — a genuine failure stays visible without the wolf-crying. |
| Malformed / absent / invalid announced id | Never reaches here: `emitConversationReset` gates on `transcript.ValidStem` and emits nothing otherwise. No consumer-side re-derivation of that question, and no empty-string guard, because the producer's contract is the guard. |
| Save failure inside the pool | Already `RotateID`'s contract — the in-memory re-key is authoritative and durability is best-effort, matching `rebindConversation` and `RotateForNewSession`. |

Logging is **content-free**: `event`, `session_id`, `previous_session_id`, and `err` only. Session ids are already the drain's established log vocabulary; nothing claude authored beyond an id validated as a UUID stem reaches a record.

## Testing strategy

### `cmd/pyry` unit — `session_reset_follow_test.go`

- **Announced reset rotates the tag and calls the pool once** (AC1, AC2): a follower over a stub `adopt`; assert the recorded `(old, new)` pair, that `tag.ID()` reports the new id, and that the event was still forwarded to `next`.
- **A second announced reset in the same session** (AC2): two resets; the second's `old` must be the FIRST reset's new id, proving the follower reads the live tag rather than a captured one.
- **Equal id changes nothing** (AC3): `adopt` never called, `tag.ID()` unchanged, event still forwarded.
- **Pool error does not un-rotate the tag** (AC2's dark-conversation guard): `adopt` returns `ErrSessionNotFound`; `tag.ID()` must still report the new id.
- **Retains past a saturated sink** (AC5): the `TestSessionBackgroundTaskHold_RetainsPastASaturatedSink` shape — a `newStreamTurnSink(1, …)` filled with a droppable filler, `len(sink.ch)` asserted BEFORE the reset as well as after, so a green means "saturated, and observed anyway" rather than "the sink quietly queued it".
- **Every non-reset variant is forwarded unchanged**, and a nil `next` forwards nothing without panicking.
- **-race arm**: `Sink` on one goroutine while `tag.ID()` is read on another, mirroring production's forwarder/reader pair.

### `cmd/pyry` unit — drain integration

- **Events after a reset reach the emitter** (AC2, the end this ticket exists for): drive a follower whose `adopt` rebinds a stub active-session to the announced id, then feed a `TextChunk` through `sinkForTag` and assert the drain's gate ADMITS it — the assertion that fails on main, where the tag would still report the pre-reset id.

### `internal/sessions` unit — `transition_test.go` additions

- `AdoptAnnouncedID` on a known session re-keys and fires exactly one `ReasonClear` transition with the right `PreviousID`/`NewID` (AC1).
- Equal ids: no re-key, **no transition** (AC3) — the regression `onRotate` would have.
- Unknown `oldID`: `ErrSessionNotFound`, no transition, no mutation (the watcher-raced path).
- `newID` already owned by a different live session: refused, **both** sessions still present and still keyed as before, no transition.
- The owning conversation is rebound (the AC4 mechanism), asserted through the registry.

### `internal/e2e` — `relay_v2_stream_announced_reset_test.go` (build tag `e2e`)

End-to-end over a real daemon in stream-json mode with a real relay leg, modelled on `TestRelayV2_StreamRequestSessionSettings`:

1. Seed a bound conversation; learn its bound session id from a first `request_session_settings`.
2. Pre-write two usage-bearing transcripts under the daemon's claude-sessions dir — one for the pre-reset id, one for the announced id, with **different** used-token figures, so the AC4 assertion discriminates rather than passing on a shared default.
3. Drive one turn; fakeclaude's rider announces the reset before its reply, so `turn_end` is the happens-before.
4. Assert the client received exactly one `session_transition` with reason `clear` naming both ids (AC1).
5. Assert a second `request_session_settings` names the **announced** id and reports the **announced id's** used-token figure (AC4).
6. Drive a second turn and assert its `turn_end` still reaches the client (AC2 end-to-end — the tag rotated).

## Open questions

- **Does the e2e's daemon resolve transcripts from a directory the test can write into?** `snapshotUsageFor` resolves `<dir>/<session-id>.jsonl` from the daemon's claude-sessions dir; `StartStreamInteractiveWithRelay` does not set `PYRY_FAKE_CLAUDE_SESSIONS_DIR`, so the daemon's own default under `home` is the one to write into. To be confirmed in Phase B against the real resolution; if the directory is not test-writable, step 5 degrades to asserting the announced id plus the fresh-session collapse (non-zero window, zero used) against a pre-reset id whose transcript carries a non-zero used figure — still discriminating.
- **Does the pre-reset transcript need to exist for step 5 to discriminate?** Resolved the same way: the discriminating pair is what matters, not which side carries the non-default figure.
- **Should the follower also feed `turnBusyTracker`?** No — the drain already calls `busy.observe` on the event, and `turnMarkFor` answers `turnMarkNone`, so there is nothing to feed. Recorded so the absence reads as a decision.

Each open question is resolved in Phase B and any design change it forces is recorded under `## Revisions`.

## Security review

**Verdict:** PASS (after one revision — see [Trust boundaries] below)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed by revising the Design before this section was written.** This ticket opens a *new class* of boundary: until now a line on the supervised child's stdout could at most produce a client-visible event, and after this ticket one line mutates the daemon's session registry and rebinds a conversation. The boundary is explicit and singular — `emitConversationReset` is the only producer and `sessionResetFollower.Sink` the only consumer — but the *first* draft of `AdoptAnnouncedID` delegated to `RotateID`, and `rekeyLocked` moves a map entry **without checking the destination**. A hostile or confused announced id naming another live session would therefore have overwritten that session's registry entry and silently swallowed it, triggered by a single stdout line rather than by the watcher's much more expensive real file creation. The Design now refuses a colliding `newID` inside the same `Pool.mu` hold as the mutation (outside it would be a TOCTOU), which is why the method mirrors `RotateForNewSession`'s locked body instead of delegating. The *shape* of the announced id needs no consumer-side check: `emitConversationReset` gates on `transcript.ValidStem`, whose `uuidStemPattern` is an anchored `^…$` match over lowercase hex and dashes only — so the value that later becomes a filename component in `snapshotUsageFor`'s `<dir>/<id>.jsonl` can carry no separator, no `..`, and no length.
- **[Tokens, secrets, credentials] No findings — by construction, not by care.** Nothing here reads, writes, mints or compares a credential. Worth naming positively: an announced reset deliberately does **not** re-derive or re-assert the child's permission posture and does not arm `Runner.BeginRotation`. The child is the same process at the same posture; a posture re-derivation on this path would be a new way to *change* a running child's permissions from its own stdout, which is precisely the channel #2064/#2065 exist to keep one-directional.
- **[File operations] No findings.** This ticket creates and opens no file. It reaches two existing writers — `saveLocked` (sessions.json) and `rebindConversation`'s registry `Save` (conversations.json) — both already atomic temp-plus-rename with their own modes, both already on this exact code path via `onRotate`. The only user-influenced value that reaches a path is the announced id, bounded as above.
- **[Subprocess / external command execution] No findings.** No `exec.Command`, no argv construction, no env mutation, no signal. Explicitly: the child is neither killed nor respawned — that is `RestartFresh`'s path and this ticket stays off it. The new `PYRY_FAKE_CLAUDE_STREAM_RESET_TO` env var is read only by the test binary under `internal/e2e/internal/fakeclaude` and reaches no production code path.
- **[Cryptographic primitives] No findings, and the notable thing is the absence.** `AdoptAnnouncedID` deliberately does not mint an id, so unlike its sibling `RotateForNewSession` it makes no RNG choice at all — it adopts a value claude already committed to on disk. Nothing is compared against a secret, so there is no constant-time question.
- **[Network & I/O] No findings, and one property worth stating positively.** No new socket, listener, deadline or size cap. Because the reset is observed **upstream of the fan-in send**, an actor able to saturate the turn-event channel cannot use that saturation to suppress the re-key — the registry cannot be desynchronised from claude by flooding. AC5 is the test for it, and it is a security property as much as a fidelity one.
- **[Error messages, logs, telemetry] No findings.** Records are content-free: `event`, `session_id`, `previous_session_id`, `err`. The follower never logs the event or a decode error — it has neither, and `emitConversationReset`'s own "no logging surface at all" posture exists because `encoding/json` quotes offending input into its error text. The one err logged is a sessions sentinel or a `saveLocked` os error naming the daemon's own registry path, which `RotateForNewSession` already logs at the same level. The expected-vs-genuine split (Debug for `ErrSessionNotFound`, Warn otherwise) exists so a real failure is not buried under the watcher race's normal noise.
- **[Concurrency] No findings after the [Trust boundaries] revision, which removed the only TOCTOU.** Locks: the follower takes none; the pool call takes `Pool.mu` → `Session.lcMu` in the established order and fans out off-lock. The new edge is one more *caller* of an existing lock, not a new ordering. No goroutine is spawned, so none can leak. Shutdown mid-sequence: the tag rotation is a single atomic store and the re-key is under one lock with an atomic on-disk save, so the recoverable states are "old id, old transcript" or "new id, new transcript" — never a half-applied one.
- **[Threat model alignment] No findings.** This is a local daemon path. No inbound relay verb reaches this seam — the only writer is the supervised child's stdout — so a paired device cannot induce a session re-key. The `session_transition` frame it causes travels the existing v2 push path with no new field and no new recipient class.

**Out of scope, named:** runner-side id adoption so a crash respawn resumes the announced transcript (#2136); retiring the rotation watcher, which would remove the double-fire race entirely (#2137); live confirmation of the announcement bytes against a real claude (#2138).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
