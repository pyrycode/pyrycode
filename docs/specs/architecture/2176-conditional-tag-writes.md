# #2176 — the follower's tag writes become conditional; the rotation that re-keyed the registry wins

## Files read

- `cmd/pyry/session_reset_follow.go` → `sessionResetFollower`, `follow`, `rekeyPool` — the whole
  subject. `follow`'s two `tag.Rotate` calls are the unconditional stores this ticket makes
  conditional; `rekeyPool`'s `ErrSessionNotFound` arm is the false premise.
- `cmd/pyry/stream_turn_drain.go` → `streamSessionTag`, `newStreamSessionTag`, `ID`, `Rotate`,
  `sinkForTag` — the tag is an `atomic.Pointer[string]` with an unconditional `Rotate`. It needs a
  conditional sibling; `sinkForTag` is the surface every assertion reads the consequence through.
- `cmd/pyry/streamsup_runner.go` → `newStreamRunnerFactory` — the sole production construction of
  the follower, and the line that installs `tag.Rotate` as `scfg.OnSessionRotate`. Both the
  follower and the daemon-driven rotation write **the same tag object**, which is the race.
- `internal/sessions/transition.go` → `AdoptAnnouncedID`, `RotateForNewSession`,
  `notifyTransition`, `rebindConversation`, `ErrSessionIDTaken` — the pool half. Confirms
  `AdoptAnnouncedID` returns `ErrSessionNotFound` before any mutation, and that
  `RotateForNewSession` re-keys the pool **before** the transition fan-out (so the tag reaching `M`
  implies the registry already did).
- `internal/streamsup/runner.go` → `Config.OnSessionRotate`, `RestartFresh` — the daemon-driven
  writer of the tag, fired after `RotateForNewSession` succeeded.
- `internal/sessions/runnerstate.go` → `RunnerConfig.AdoptAnnouncedReset` — the seam direction A
  would have widened; left untouched.
- `docs/knowledge/features/streamsup-package-announced-reset-follower.md` — §"The lesson: a refusal
  is not a control until the caller unwinds to match it" and §"`ErrSessionNotFound` after the
  watcher's retirement (#2137)". The first is the invariant this change must not break; the second
  states the unfixed divergence and points here.
- `CODING-STYLE.md` § Interface Design — "Define the interface where it's consumed" and "Don't
  define interfaces preemptively. Wait until you have two implementations **or a testing need**",
  which is the licence for the `sessionTag` seam below; § Levels for the record levels.
- `cmd/pyry/session_reset_follow_test.go` → `TestSessionResetFollower_PoolAnswerDecidesTheTag`,
  `recordingAdopt`, `recordingAdoptRunner`, `drainEnvelopeSessionIDs` — the pinned behaviour AC 3
  protects, and the doubles the new rows reuse.
- `cmd/pyry/modal_resolve_v2_test.go` → `auditLogger` — the existing Debug-level JSON logger
  helper the record assertions (AC 4) reuse rather than reinventing.

## Context

`rekeyPool` answers `nil` on `sessions.ErrSessionNotFound`, which `follow` reads as "the session
now stands on the announced id" — so the tag stays there and the runner adopts it. That reading was
produced by the rotation watcher, which observed the same rotation first and re-keyed onto the
**same** announced id. #2137 retired the watcher. The ticket enumerates every surviving producer of
the sentinel on `main` at `cab4a3b5` — `RotateForNewSession`, `RotateBootstrapForSelfHeal`,
`Remove`, the eviction sweep, the create-rollback deletes — and all of them either mint a
**different** id or leave no successor at all. No remaining code path can make the branch's stated
premise true.

The consequence is a divergence, not merely a stale comment: in the narrow race the ticket
reproduces, the tag and the runner's spawn id end on the announced `A` while the registry and the
conversation binding are on the minted `M`. `sinkForTag` then stamps later events with `A`, the
drain's active-session gate compares against the bound session, and the conversation goes dark —
the same failure class `ErrSessionIDTaken`'s unwind exists to prevent, entering through the other
sentinel.

No ADR is warranted: this corrects one branch's reading of an existing seam and adds no new
architectural decision. The evergreen feature doc's §"`ErrSessionNotFound` after the watcher's
retirement (#2137)" describes the unfixed state and belongs to the documentation phase.

## Design

### Direction chosen, and the one rejected

The ticket leaves two directions open.

**Rejected — the pool reports the id the session stands on.** Widening
`sessions.RunnerConfig.AdoptAnnouncedReset` to answer the current id touches `runnerstate.go`,
`transition.go` and both `RunnerConfig` literals in `pool.go` — five production files, the size-S
file ceiling exactly, for an answer that is stale the instant it is returned. It tells the follower
where the registry was, not whether the follower's own tag write is still the one standing. It also
would not close AC 2(a) at all: in that interleaving the follower never reaches the pool.

**Chosen — the follower's tag writes become conditional.** Both of `follow`'s writes are
unconditional stores over a value read earlier, and that is the actual defect: a *follower* must
not overwrite a *driver*. Making each write land only while the tag still holds the value the
follower read makes the winner's id stand under both interleavings, and it lands in `cmd/pyry`
alone plus one comment-only edit in `internal/sessions`.

The asymmetry is the design principle and is worth stating: `OnSessionRotate` stays an
unconditional `Rotate`, because the daemon-driven path re-keyed the registry itself and *is* the
authority. The follower is following, so it writes only where it still owns the value.

### `streamSessionTag.CompareAndSwap`

```go
func (t *streamSessionTag) CompareAndSwap(oldID, newID string) bool
```

Answers whether the move happened. Empty `newID` is refused (`false`), mirroring `Rotate`'s
invariant that the tag never holds `""`. Implemented as a load, a value comparison against `oldID`,
and `atomic.Pointer.CompareAndSwap` on the loaded pointer.

Comparing the *pointer* rather than re-comparing the value at swap time is deliberate and is
strictly stronger: a competing store that happens to install an equal string in a different
allocation fails the swap. That direction of failure is the safe one — the follower declines and
the competing writer's id stands — and an id that rotates away and back is not something the
minting paths can produce anyway.

`Rotate` is unchanged and keeps its `OnSessionRotate` role.

### `sessionTag` — a two-method interface at the consumer

```go
type sessionTag interface {
	ID() string
	CompareAndSwap(oldID, newID string) bool
}
```

`sessionResetFollower.tag` and `newSessionResetFollower`'s first parameter take this instead of
`*streamSessionTag`. Implicit satisfaction means **no call site changes** — the factory and all
nine existing test constructions pass `*streamSessionTag` unmodified.

It exists for a testing need the ticket names explicitly: AC 2(a) puts a competing rotation between
the read of the live tag and the write that follows it, and those are adjacent statements on one
goroutine with nothing between them. A test double that performs the competing rotation at the top
of its `CompareAndSwap` and then delegates is observationally that ordering, because the follower's
goroutine does nothing else in the gap. `CODING-STYLE.md` licenses exactly this ("or a testing
need"), and the interface is defined at the consumer per the same section.

### `follow` — the new shape

Contract, not body:

1. `oldID := f.tag.ID()`; `newID == oldID` → return untouched. Unchanged (AC 3).
2. `f.tag.CompareAndSwap(oldID, newID)` replaces `f.tag.Rotate(newID)`. `false` → **decline**: write
   one `announced_reset.superseded` record and return. The pool is never called, the runner never
   adopts, and the tag is left exactly where the competing writer put it.
3. `f.rekeyPool(oldID, newID)` unchanged in position — the rotate-before-the-pool ordering the
   type's doc derives survives intact, because a conditional write is still a write before the
   call.
4. Non-nil answer → **decline**: `f.tag.CompareAndSwap(newID, oldID)` replaces
   `f.tag.Rotate(oldID)`, so the unwind lands only while the tag still holds the announced id, then
   one record, then return. The runner is never asked to adopt.
5. `f.adoptRunner(newID)` unchanged, still the single call site on the far side of the one answer.

### `rekeyPool` — the sentinel arm goes

```go
func (f *sessionResetFollower) rekeyPool(oldID, newID string) error
```

Keeps its shape and its documented job — answer the one question both remaining steps need, *does
the session now stand on `newID`?* — and now answers `nil` for exactly two situations: `adopt ==
nil` (no pool wired; the tag half is the whole of it) and a `nil` error from the pool (this call
re-keyed). The `ErrSessionNotFound` arm is deleted; the sentinel now flows out as the error it is.

The biconditional stays expressed once, keyed to the answer rather than to a sentinel list — a
sentinel added later still lands on the unwinding side by default, which is #2135's lesson and is
not weakened here.

### Records — two `event` keys, one per decline (AC 4)

| `event` | Level | Cause | Written by |
|---|---|---|---|
| `announced_reset.superseded` | Info | the session moved elsewhere, or is gone | step 2's `false`, and step 4 when `errors.Is(err, sessions.ErrSessionNotFound)` |
| `announced_reset.refused` | Warn | the announced id names a different live session | step 4 otherwise (`ErrSessionIDTaken` today, and any sentinel added later) |

`announced_reset.already_applied` is removed: it named a case that is no longer "already applied".

Both records carry `session_id` (the announced id) and `previous_session_id` (the id the follower
read), as today. `superseded` adds `current_session_id` — what the tag actually holds now, which is
the whole operator-facing point of the record — and carries `err` only where a pool answer produced
it, which is what tells the two `superseded` paths apart without a third key.

The sentinel test in step 4 selects a **record**, never a control decision; control stays keyed to
`nil` vs non-nil. A future sentinel falls into `refused`, whose message stays generic ("the pool
moved nothing") with `err` carrying the specific value.

Levels, against `CODING-STYLE.md` § Levels: `superseded` is a lifecycle event (a rotation stood
down) and is rare, so Info. It deliberately departs from `already_applied`'s Debug, whose stated
reason was that an operator could not tell #2176's divergence from a benign removal by reading it —
that reason is what this ticket removes. `refused` stays Warn: claude naming an id the daemon
cannot follow it to is still either a defect or a confused child.

## Concurrency model

No goroutines are created and none of the existing lifetimes change. The follower keeps running
synchronously on claude's stdout forwarder goroutine.

What changes is a claim in the type's own doc that has been wrong since #2137: "no event can be
produced between the steps" is still true and still load-bearing, but it does **not** imply the
tag cannot move between the steps. The tag has a second writer on a different goroutine —
`OnSessionRotate`, fired from `RestartFresh` on the runner's own path — and that writer is exactly
the competing rotation. The doc is corrected to say the forwarder goroutine is the only producer of
*events*, not the only writer of the *tag*.

Ordering that makes the fix sound: `RotateForNewSession` re-keys the pool under `p.mu` and only
then returns, and `OnSessionRotate` fires later still, from `RestartFresh`. So the tag holding `M`
implies the registry already holds `M`. Declining whenever the tag has moved therefore leaves the
tag on an id the registry agrees with, which is AC 2 stated as an invariant rather than as two
cases.

`streamSessionTag` remains a single atomic word; `CompareAndSwap` adds no lock and no allocation
beyond the one `Rotate` already makes.

## Error handling

- **Tag moved before the follower's write** → decline, one Info record, nothing else touched.
- **`ErrSessionNotFound`** → conditional unwind, one Info record. Where the unwind's own CAS fails
  (interleaving (b): the competing writer moved the tag after the follower's write), the tag is
  left on the winner's id rather than dragged back to a retired one. The record's
  `current_session_id` reports which happened.
- **`ErrSessionIDTaken` and any later sentinel** → conditional unwind, one Warn record. Identical
  to today whenever nothing else moved the tag, which is the only interleaving AC 3 pins.
- **`adopt == nil`** → `rekeyPool` answers nil, tag and runner both move. Unchanged.
- No new error values, no wrapping change: `rekeyPool` still returns the pool's error unwrapped so
  the record names what the pool actually answered.

## Testing strategy

All hermetic, `cmd/pyry` unit tests under `make check`.

- **RED first.** `TestSessionResetFollower_PoolAnswerDecidesTheTag`'s `ErrSessionNotFound` row is
  rewritten to `wantTag: resetFollowSessionA` and must fail on current `follow` before any
  production edit — on the tag, the derived runner expectation and the envelope alike.
- **AC 1** — that rewritten row, with its comment naming which producer of the sentinel it now
  models (a daemon-driven rotation onto a minted id, or a removal) rather than the retired watcher.
- **AC 3** — the same table gains a `nil` row (tag on the announced id, runner adopted, envelopes
  stamped with it), so the biconditional is asserted over all three answers; the `ErrSessionIDTaken`
  row is untouched. `TestSessionResetFollower_EqualIDChangesNothing` and
  `TestSessionResetFollower_ForwardsEveryVariantAndToleratesNilNext` stay verbatim and must stay
  green.
- **AC 2** — one new test, two subtests, sharing a `racingTag` double (embeds `*streamSessionTag`,
  fires a supplied hook once at the top of `CompareAndSwap`, then delegates) and a `daemonRotate`
  closure that mirrors production's order: re-key the stub registry onto `M`, then
  `tag.Rotate(M)`.
  - (a) the hook is the double's; the rotation lands before the follower's write.
  - (b) the hook is inside the `adopt` double, which then answers `ErrSessionNotFound`; the
    rotation lands between the write and the answer, and needs no `sessionTag` double at all.
  - Both assert through `drainEnvelopeSessionIDs` on a real `sinkForTag`, plus
    `recordingAdoptRunner` for "never adopted `A`", and the pool-call record for whether the pool
    was reached.
- **AC 4** — one test over `auditLogger`'s buffer: each of the three decline paths produces exactly
  one record, with the expected `event` key and both ids present. A small local helper parses the
  JSON lines (`auditRecords` filters on one fixed `msg`, so it does not fit).
- Untouched and expected green: `TestStreamTurnDrainV2_EventsAfterAnnouncedResetStillReachTheClient`
  (real drain, real gate, `nil` answer), `TestSessionResetFollower_ObservesPastASaturatedSink`,
  `TestSessionResetFollower_ConcurrentSinkAndTagRead` (the `-race` arm — `CompareAndSwap` must not
  introduce one).
- Gate: `go test -race ./cmd/pyry/... ./internal/sessions/...`, `go vet ./...`,
  `go build ./cmd/pyry`. The live suite is untouched: it exercises the `nil` answer only.

## Open questions

1. **Should `adoptRunner` also be conditional on the tag still holding `newID`?** A competing
   rotation landing after a *successful* pool answer would leave the tag on `M` and the runner on
   `A`. Resolved: **no.** It is a third interleaving, distinct from both AC 2 pins, unobserved, and
   closing it adds a fourth decline path with no acceptance criterion behind it. Noted here so the
   omission is a decision rather than an oversight; it earns its own ticket if it is ever seen.
2. **One `event` key for both `superseded` paths, or two?** Resolved: one, with
   `current_session_id` and the presence of `err` telling them apart. AC 4 requires the two *causes*
   separated by key, and both of these are the same cause — another rotation won.
3. **Does `transition.go` change beyond its comment?** Resolved: no. `AdoptAnnouncedID`'s doc
   sentence "see rekeyPool, whose caller-side reading of this sentinel has not been revisited" is
   the only stale part; the behaviour is correct as it stands.

## Security review

**Verdict:** PASS

Two claims below are load-bearing and were verified against the tree rather than assumed; both are
marked *(verified)*.

**Findings:**

- **[Trust boundaries]** No findings, and the boundary is narrowed. The boundary is `newID`
  arriving from the supervised child's stdout: its SHAPE is settled upstream by
  `emitConversationReset`'s `transcript.ValidStem`, its DESTINATION by `AdoptAnnouncedID`'s
  collision refusal, and the follower owns the ORDER and the UNWIND. The adversarial question is
  whether a *conditional* unwind can leave the tag on a refused announced id — the exact defect
  #2135's lesson exists to prevent. It cannot: `CompareAndSwap(newID, oldID)` fails only when the
  tag no longer holds `newID`, so a failed unwind is precisely the case where the tag is **not** on
  the refused id. The CAS also introduces no new destination — it can only move the tag from the
  follower's own `oldID` to `newID`, the same pair `Rotate` moved today.
- **[Trust boundaries]** No findings — the tag's writer set is closed. *(verified)* `git grep
  '\.Rotate('` outside tests matches exactly the two calls in `follow` that this ticket replaces;
  the only other writer is `streamsup.Config.OnSessionRotate`, fired from one site in
  `(*Runner).RestartFresh`. So the id a declined announcement leaves standing is always
  daemon-minted and always one the registry already re-keyed onto — never attacker-chosen. The
  whole safety argument for "leave the winner's id" rests on this enumeration.
- **[Subprocess / external command execution]** No findings, and the exposure shrinks. The
  announced id reaches the next child's argv and environment through `adoptRunner` →
  `(*streamsup.Runner).AdoptSessionID` → `beginSpawn`. This change strictly *removes* call paths to
  that sink: after it, an announced id reaches argv only when the pool actually re-keyed onto it.
  No new value reaches `exec.Command`, and nothing here hand-rolls a path join.
- **[Error messages, logs, telemetry]** No findings — the records stay content-free. *(verified)*
  Both `RunnerConfig.AdoptAnnouncedReset` literals in `internal/sessions/pool.go` return
  `AdoptAnnouncedID`'s error **unwrapped**, so the `err` field can only ever carry a bare package
  sentinel, never child-authored text. The new `current_session_id` field carries a daemon-minted
  id or a `ValidStem`-gated one — the same class as the two id fields already logged. The event
  itself is still never logged.
- **[Error messages, logs, telemetry]** No findings on log amplification, despite the Debug → Info
  promotion. A hostile child emitting many `conversation_reset` lines produces at most one record
  per line — 1:1, no amplification — and it can already drive the Warn-level `refused` record at
  that same rate today. An equal-id announcement still returns before any record. Reaching
  `superseded` repeatedly additionally requires the daemon's own session to be gone, i.e. the
  runner is already being torn down.
- **[Concurrency]** No findings, and the change removes a TOCTOU rather than adding one:
  read-then-unconditional-write becomes read-then-conditional-write, which is the correct shape for
  a follower with a second writer on another goroutine. No locks are taken, so there is no ordering
  to document. The pointer-comparison `CompareAndSwap` is not ABA-exploitable: `Rotate` and the CAS
  each store a freshly allocated `*string`, and Go's GC keeps a live pointer from being reused, so
  the same pointer value cannot be reinstalled after a competing store. Its only failure direction
  is spurious refusal, which declines the announcement — the safe side.
- **[Concurrency]** OUT OF SCOPE — one residual window survives: a competing rotation landing
  *after* a successful pool answer leaves the tag on `M` and the runner on `A`, because
  `adoptRunner` is called without re-checking the tag. It is a third interleaving, distinct from
  both the ticket names, unobserved, and outside these acceptance criteria; recorded as Open
  Question 1 and earning its own ticket if it is ever seen. Its blast radius is bounded to the
  wrong `--resume` on a subsequent crash respawn, not a cross-conversation delivery.
- **[Tokens, secrets, credentials]** Not applicable — no credential material is read, written,
  compared or logged on any path this change touches. Session ids are non-secret identifiers,
  already logged by the two records this ticket rewrites.
- **[File operations]** Not applicable — no path is constructed anywhere in this change, and every
  declining path returns from `AdoptAnnouncedID` before `rekeyLocked` and therefore before
  `saveLocked`, so no declined announcement can reach a disk write.
- **[Cryptographic primitives]** Not applicable — `sync/atomic`'s `CompareAndSwap` is a
  concurrency primitive, not a security comparison. Neither operand is a secret (both are the
  daemon's own current and previously-read session ids), so constant-time comparison is not
  required.
- **[Network & I/O]** Not applicable — no socket, no reader, no size-bounded input. The change is
  entirely in-process state.
- **[Threat model alignment]** The relevant threat is `protocol-mobile.md`'s "the supervised child
  redirects a conversation's output" class, which #2135 opened and `ErrSessionIDTaken`'s unwind
  half-closed. This change closes the sibling hole in the same class through the other sentinel.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
