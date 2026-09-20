# 2521 — Reset a previously-used conversation that has no running assistant

Ticket: https://github.com/pyrycode/pyrycode/issues/2521 (`bug`, `security-sensitive`)

## Files read

- `cmd/pyry/main.go` → `activeSessionStarter`, `activeSessionStarter.start`,
  `activeSessionStarter.resolveSpawnDir`, `startFreshRunner`, `resolveBoundSession`,
  `sessionRouter.resolve`, `sessionRouter.revive` — the whole reset seam and the
  warm-start materialisation path the message route already has and `new_session` lacks.
- `internal/sessions/transition.go` → `Pool.RotateForNewSession`, `Pool.notifyTransition`,
  `Pool.rebindConversation` — the rotation requires `oldID` to be **in `p.sessions`**, and
  it is the one call that re-keys, persists, rebinds the conversation and fires the
  `ReasonClear` transition. AC-2 falls out of it once the session is materialised.
- `internal/sessions/revive.go` → `Pool.Revive` — materialises a persisted-but-dropped
  session **without spawning claude**; the revived session is evicted and comes up on the
  next `Pool.Activate`. Exactly the shape a dormant reset needs.
- `internal/sessions/pool.go` → `Pool` (the `dormant map[SessionID]registryEntry` field and
  its "KEY SET only ever shrinks" contract), `Pool.Activate`, `Pool.buildSession`,
  `Pool.Mint`, `Pool.DormantSettingsFor`, `Pool.rekeyLocked`, `Pool.List` — where
  `createdAt`/`lastActiveAt` are set and bumped, and the established lock order
  `Pool.mu → Session.lcMu` the new reader must keep.
- `internal/sessions/session.go` → `Session.transitionTo`, `Session.touchLastActive`,
  `Session.beginEvict`, `Session.Activate` — the four writers of `lastActiveAt`, all of
  which imply the session was activated or rotated. This is what makes the discriminator
  below exact rather than a heuristic.
- `internal/sessions/registry.go` → `registryEntry` — `created_at` and `last_active_at`
  are both already persisted, so the discriminator needs **no schema change and no
  migration**: an existing user's used channel already carries the distinguishing pair.
- `internal/streamsup/runner.go` → `Runner.RestartFresh`, `Runner.Run` — `RestartFresh`
  on a runner whose `Run` was never entered is safe: it sets the id, latches
  `rotatePending` and finds a nil `iterCancel`, so the first spawn uses
  `--session-id <newID>` with no resume.
- `internal/sessions/reconcile.go` → `DefaultClaudeSessionsDir`, `encodeWorkdir` and
  `cmd/pyry/session_transcript_dir.go` → `sessionTranscriptDir` — read while evaluating
  and then **rejecting** a transcript-existence discriminator (see Design, "Discriminator").
- `cmd/pyry/new_session_starter_test.go` → `TestActiveSessionStarter_InertArms`,
  `restartFreshRunner`, `starterProbe` — the reject set that must stay red-proof, and the
  seam-faking idiom the new tests extend.
- `internal/e2e/harness.go` → `StartIn`, `Harness.Stop`;
  `internal/e2e/relay_v2_stream_new_session_named_test.go` — the fake-daemon shape AC-4's
  integration test copies, including the per-session stdin log that proves which id a
  child spawned under.
- `docs/knowledge/features/conversation-session-binding-routing.md` — the binding/routing
  contract the dormant arm must not bend.

## Context

Reset from Pyrycode Desktop is a named `new_session` frame. It reaches
`activeSessionStarter.start`, which refuses two states that both mean "this conversation
is dormant, not dead":

1. **Retained in the pool with no running child.** `resolveBound` resolves, and the
   `named && !live` guard — `runner.State().ChildPID != 0` — sends it to the inert
   `v2.new_session.no_live_child` arm. That guard exists for a real reason (#2085 defers a
   conversation's first spawn to its first message, so a created-but-unmessaged
   conversation reaches it with a real runner and no child), but `ChildPID == 0` also
   covers a session that ran and then stopped, backed off, or was idle-evicted.

2. **Absent from the pool after a daemon restart.** `Pool.New` materialises only the
   bootstrap (#1487), so every per-conversation session is a persisted entry with no
   `*Session` behind it. `resolveBoundSession`'s `Pool.Lookup` misses and the frame lands
   on `v2.new_session.no_bound_session` — the same record an unknown or unbound
   conversation gets. The message route does not have this gap: `sessionRouter.resolve`
   already re-materialises on `ErrSessionNotFound` through `sessionRouter.revive`. That
   asymmetry is why "send one message first, then Reset" works.

Both states end with the operator pressing Reset and seeing nothing, which is the reported
symptom. The reported session's logs could not tell the two apart; this ticket also makes
them separable.

This design records no ADR. It adds no boundary — it closes a gap between two existing
paths (`new_session` and the message route) using the primitive the message route already
uses.

## Design

### Discriminator: has this session ever been activated?

The one thing the fix needs, and the one thing the current code cannot ask, is
**"has this conversation ever run?"**, answered identically for a session the pool holds
and for one it has not materialised, and surviving a daemon restart.

New `internal/sessions` reader:

```go
// EverActivated reports whether the session named by id has ever been activated.
func (p *Pool) EverActivated(id SessionID) bool
```

It answers from `p.sessions[id]`'s `lastActiveAt` against its `createdAt` when the pool
holds the session, and from `p.dormant[id]`'s persisted `LastActiveAt`/`CreatedAt` when it
does not — one predicate, two sources, the shape `Pool.DormantSettingsFor` already
established for #2449. The empty id is refused before either map read, for
`resolveBoundSession`'s #678 reason.

Why the timestamp pair is exact rather than a heuristic: `Pool.buildSession` stamps
`createdAt` and `lastActiveAt` from **one** `now` value, so a never-activated session has
them equal to the nanosecond, and every writer of `lastActiveAt` afterwards —
`Session.transitionTo`, `Session.touchLastActive`, `Session.beginEvict`,
`Pool.rekeyLocked` — fires only on an activation, an eviction or a rotation. A minted
conversation that has never been messaged hits none of them. Both fields are already in
`registryEntry`, so the reading survives a restart and needs no migration: a real user's
existing used channel already carries `last_active_at > created_at` today.

Its one blind spot is named in its doc comment rather than papered over: the bootstrap
session warm-starts in `stateActive` straight from its persisted row without a transition,
so a bootstrap whose timestamps still read equal answers `false` even while its child
runs. Every caller added here asks only about a session with **no live child**, where that
reading is the correct one anyway.

**Rejected alternative — transcript existence.** `transcript.StatByID` over the session's
`<id>.jsonl` is the other durable "this ran" signal, and it was the first design. Rejected
on three counts. It couples a daemon control-flow decision to claude's private artifact,
which the stream path deliberately stopped watching (#2137 retired the rotation watcher).
It needs the projects folder re-derived from the conversation's recorded — hence
client-influenced — workspace, which `sessionTranscriptDir`'s own doc argues against
because a differently-spelled workdir names a folder claude never writes, and it silently
answers "never used" for any conversation whose workspace moved after its last turn. And
the fake-daemon harness's stream path writes no `<uuid>.jsonl` at all, so AC-4's
integration test could only pass against a transcript the test itself planted.
`EverActivated` needs no filesystem access, no path handling and no client-influenced
input, and the e2e test exercises it by sending a real message.

### Seams on `activeSessionStarter`

`activeSessionStarter` stays the trust boundary for the client-named id. It gains three
fields, all **optional** on the terms its own doc records for `spawnDirFor`, `reset` and
`resetting` — a constructor-less bag of injected seams where an omitted field is a
reachable state. All three nil reproduces pre-#2521 behaviour exactly, which is what keeps
every existing test literal in `cmd/pyry` compiling and green with no edit.

- `everRan func(oldID sessions.SessionID) bool` — production wires `pool.EverActivated`.
  nil answers false, i.e. every dormant reset stays inert.
- `resolveDormant func(convID string) (oldID sessions.SessionID, recordedCwd string, ok bool)`
  — the conversation's **persisted binding**, registry-only, pool deliberately not
  consulted. Production wires a new free function `resolveDormantSession`, which repeats
  `resolveBoundSession`'s unknown/unbound refusal (the #678 isolation point) and stops
  before the `Pool.Lookup` that would have missed.
- `reviveBound func(convID string, oldID sessions.SessionID, recordedCwd string) (sessions.Runner, error)`
  — materialises the dormant session and answers its runner. Production wires
  `resolveSpawnDir` + `Pool.Revive` — byte-for-byte `sessionRouter.revive`'s body, so the
  reset path re-validates the recorded workspace at the same spawn site and on the same
  terms the message route already does.

They are three seams rather than one because the `everRan` gate must sit **between** the
resolve and the revive: AC-3 requires a never-used conversation to stay inert *without
registry mutation*, and `Pool.Revive` registers and persists.

### `activeSessionStarter.start` — the new arms

Only the resolve block and the liveness guard change; every other arm keeps its current
order, record and verdict.

```
resolveBound(convID)
  ok    → live := runner.State().ChildPID != 0
          named && !live && !everRan(oldID)        → inert, v2.new_session.no_live_child   [unchanged]
  !ok   → resolveDormant(convID)
          !ok                                       → inert, v2.new_session.no_bound_session [unchanged record]
          !everRan(oldID)                           → inert, v2.new_session.dormant_never_used [NEW]
          reviveBound(...) errors                   → inert, v2.new_session.revive_failed     [NEW, Warn]
          ok                                        → Debug v2.new_session.revived_dormant, fall through
```

Every change is **strictly additive**: each one can only turn a frame that is inert today
into a rotation, and no arm that rotates today takes a different path. Three consequences
of that, stated so the verifier can check them rather than infer them:

- The `named` asymmetry on the liveness guard is **kept**, not repeated on the new gate.
  A bare frame on a childless conversation rotates exactly as #2099 promised, and never
  calls `everRan`.
- A revived session is previously-used **by construction** (the gate proved it), so `start`
  carries a `used` bool from the dormant arm and short-circuits the second `everRan` call
  rather than paying a duplicate read.
- A revived runner reports `ChildPID == 0`, so the `a.reset != nil && live` wrap-up gate is
  false and the dormant reset takes the **synchronous** rotation path. There is no live
  child to ask for a handoff note and no `resetting` phase to report, which is AC-3's
  "the running-session path keeps its existing handoff capture and `resetting` progress
  events" read the only way it can be true.

Below that block nothing is touched: `resolveSpawnDir` still runs below every inert arm
(so a repeated frame on a never-used conversation still drives no `MkdirAll`),
`startFreshRunner` still arms the #1330 gate, rotates, installs the workspace and calls
`RestartFresh`, and `RotateForNewSession` still re-keys, persists `sessions.json`, rebinds
the conversation through `notifyTransition`'s `ReasonClear` branch and fires the
conversation-scoped transition. AC-2 is therefore inherited whole rather than re-built, and
its "a reset that does not complete emits no success transition" half holds because
`RotateForNewSession` returns before `notifyTransition` on every error.

AC-1's "the first subsequent message uses that identity and cannot resume the retired
transcript" also falls out of the existing primitives: `Pool.Revive` leaves the session
evicted, `RestartFresh(newID)` latches `rotatePending`, and the first message's
`Pool.Activate` starts `Runner.Run`, whose `beginSpawn` consumes the latch and spawns with
`--session-id <newID>`.

### File placement

`cmd/pyry/main.go` is 4000 lines. The three new struct fields and the edited `start` block
stay there (they are edits to existing declarations), and the new free functions plus the
new `reviveDormantBound` helper method go in a new `cmd/pyry/new_session_dormant.go`
alongside the package's existing one-subject files (`session_reset.go`,
`session_transcript_dir.go`).

### Sizing — built as one ticket, with the overage stated

Re-counted against this written plan rather than against the opening sketch: 3 production
source files, 0 new exported types, 0 consumer call sites needing simultaneous update, 4
acceptance criteria, 3 new reject branches — all inside the one-ticket boundary. Total
written work estimates at roughly 865 lines against a 800-line line, an ~8% overage, and
it is taken deliberately.

The only seam the work splits on is "in-pool dormant arm" / "after-restart dormant arm",
and `EverActivated` plus the `everRan` gate is one mechanism serving both: cutting between
them puts half of one discriminator in each child, and the second child's integration test
re-does the first's. Each child would then sit near 70% of the ceiling and the pair would
cost more in total than the whole — the failure the #1720 family measured. AC-1 also names
the two states as one behaviour ("its saved session is **either** absent from the pool ...
**or** retained in the pool"). The floor therefore wins over the ceiling here, per the
sizing rule's own tie-break, and the overage is recorded rather than engineered away.

## Concurrency model

No new goroutines and no new lock-order edges.

- `Pool.EverActivated` takes `p.mu` (read) and, on the live arm, `Session.lcMu` — the
  `Pool.mu → Session.lcMu` order `Pool.List`, `Pool.saveLocked` and `Pool.pickLRUVictim`
  already keep.
- The dormant arm runs synchronously on `V2SessionManager`'s single `Run` dispatch
  goroutine, which is where `resolveSpawnDir`'s `MkdirAll` already blocks on this verb
  (`activeSessionStarter.resolveSpawnDir` records that consequence). `Pool.Revive` does no
  blocking work and cannot spawn — its own doc makes the absent `context.Context` the API
  signal for that — so the dormant arm adds one registry save to a goroutine that already
  pays one on this path.
- `resolveDormant` → `everRan` → `reviveBound` is a check-then-act across three calls. The
  race is a concurrent frame or message materialising or rotating the same session in the
  window. It is benign in both directions and needs no new lock: `Pool.Revive`'s take path
  returns the existing `*Session` unchanged when the id is already registered, and a
  rotation that won the race makes `RotateForNewSession` answer `ErrSessionNotFound`, which
  `startFreshRunner` already disarms the #1330 gate for and returns — "the ORDINARY way to
  land here", in its own words.
- The wrap-up tail (`resetThenRotate`) is unreachable from the dormant arm, so its
  goroutine, its `reset.begin` claim and its deferred falling edge are untouched.

## Error handling

| Condition | Verdict | Record |
| --- | --- | --- |
| Named id not canonical | inert | `v2.new_session.invalid_conv_id` (unchanged) |
| Unknown or unbound conversation, live or dormant | inert | `v2.new_session.no_bound_session` (unchanged) |
| Bound, in pool, no live child, never activated | inert | `v2.new_session.no_live_child` (unchanged) |
| Bound, dormant, never activated | inert | `v2.new_session.dormant_never_used` (new, Debug) |
| Bound, dormant, previously used, revive failed | inert | `v2.new_session.revive_failed` (new, Warn) |
| Bound, dormant, previously used, revived | rotate | `v2.new_session.revived_dormant` (new, Debug) |
| Rotation failed | error returned | unchanged |

A failed revive is **inert, not an error return**, and that is a security decision rather
than a convenience: the two failures reachable there are `resolveSpawnDir`'s confinement
rejection, whose error names the resolved path and the `$HOME` boundary, and `Pool.Revive`'s
build/persist failures, which can name a settings path. `resolveSpawnDir`'s own SECURITY
paragraph forbids either reaching a log, and `start`'s error return is Warn-logged verbatim
by `handleNewSession`. The new record therefore carries the event and the conversation id
and nothing else — no error, no path — and the frame is re-sendable. This also keeps the
inert-arm contract the verb already has: an inert arm is success of a valid request.

## Testing strategy

Unit, `cmd/pyry/new_session_starter_test.go` (extending the existing `starterProbe`
idiom, with the probe gaining counters for the three new seams):

- `TestActiveSessionStarter_InertArms` keeps every current row **unedited and green**,
  which is the proof that the never-used refusal was distinguished rather than removed.
  Two rows are added: a dormant conversation that was never activated, and a dormant
  conversation whose revive fails. Both assert zero rotations, zero respawns, no error, and
  their own event.
- A new test for the in-pool dormant reset: a `restartFreshRunner` with `childPID == 0`,
  `everRan` answering true, a **named** frame — asserts exactly one rotation off the bound
  id, one `RestartFresh` under the rotated id, and that the wrap-up coordinator was never
  claimed.
- A new test for the after-restart dormant reset: `resolveBound` refusing, `resolveDormant`
  answering the persisted binding, `everRan` true — asserts `reviveBound` was called once
  with the conversation id and the bound id, and that the revived runner was rotated and
  respawned.
- An ordering test: for a dormant, never-activated conversation, `reviveBound` is called
  **zero** times. This is AC-3's "without registry mutation" stated as a claim about calls,
  which no return value can carry.
- A bare-frame test: with `everRan` answering false and no named id, the cursor's childless
  conversation still rotates and `everRan` is never consulted — #2099's promise, unmoved.

Unit, `internal/sessions`: `Pool.EverActivated` over a minted-but-never-activated session
(false), the same session after `Pool.Activate` (true), a dormant entry whose persisted
`LastActiveAt` equals `CreatedAt` (false) and one where it is later (true), an unknown id
and the empty id (both false).

Integration, `internal/e2e` (AC-4), fake daemon, reproducing the desktop sequence:

1. Seed conversation A bound to the bootstrap session; create conversation B through
   `create_conversation` and send it one message, so B's session is activated and its
   `last_active_at` is flushed to `sessions.json` by the state transition.
2. Stop the daemon and start a second one on the same `HOME`. B's session is now dormant —
   persisted, not materialised.
3. Re-dial the phone and send a named `new_session` for B **before any message**.
4. Assert: a conversation-scoped `session_transition` for B arrives; B's binding in
   `conversations.json` is a fresh id; A's binding is untouched (isolation).
5. Send a message to B and assert the per-session stdin log named for the **fresh** id
   receives it — the child spawned under the new identity, so the retired transcript was
   not resumed.

Scope check for Phase B: `go test -race` on `./cmd/pyry/...`, `./internal/sessions/...`
and the e2e suite's touched test, plus `go vet ./...` and `go build ./cmd/pyry`. The
full-module race suite is the verifier's gate.

## Open questions

- Whether `create_conversation` in the fake-daemon harness leaves B's session in
  `p.dormant` rather than materialised after the restart. The design says it must
  (`Pool.New` materialises only the bootstrap), but the e2e test is the thing that proves
  it; if B turns out to be materialised, step 3 exercises the in-pool dormant arm instead
  and the after-restart arm keeps only its unit proof. Resolve in Phase B and record here.
- Whether the seeded bootstrap session's `last_active_at` moves during the e2e run. It is
  only load-bearing if the test ever resets conversation A with no live child, which the
  sequence above does not do.

## Documentation handoff

**Pending — owned by the documentation stage, not by this ticket.**

- `docs/knowledge/features/` — the topic covering **"Inbound `new_session` — SessionStarter
  seam"**: update the `new_session` behaviour to describe the previously-used dormant case
  (both the retained-in-pool and the after-daemon-restart shape), the `EverActivated`
  discriminator and the three new records, and **retain** the documented never-used
  refusal, which this ticket deliberately keeps.
- `docs/protocol-mobile.md` — the `new_session` reference: same split, so a client author
  reads "a dormant conversation that has run can be reset without a preliminary message;
  one that has never run stays inert".

No shared doc is edited by this ticket.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The boundary is unmoved: `activeSessionStarter`
  remains the single place the client-named conversation id is validated, and the new arms
  sit strictly **below** `conversations.ValidID` and below `resolveDormant`'s
  unknown/unbound refusal — so no new code sees an unvalidated id. `resolveDormantSession`
  repeats `resolveBoundSession`'s `CurrentSessionID == ""` guard rather than inheriting it
  by proximity, which is the #678 enforcement point: without it `Pool.Lookup("")` (and now
  `Pool.Revive("")`) would resolve to the **bootstrap** session and a named frame could
  rotate the daemon's shared child. `Pool.EverActivated` refuses the empty id for the same
  reason even though its own map reads would simply miss.
- **[Trust boundaries]** SHOULD FIX — the three new seams are optional func fields, so a
  future literal that wires `reviveBound` without `everRan` would revive on every dormant
  frame. The nil-`everRan` default is false, which fails closed (nothing revives), so this
  is a mis-wiring risk rather than an exploit. Phase B states the pairing in `everRan`'s
  doc comment and the ordering test above pins it.
- **[File operations]** No findings, and this is the category the rejected transcript
  design lost on. The chosen discriminator touches no path at all. The one filesystem
  operation the new arm reaches is `resolveSpawnDir` inside `reviveBound` — the same
  validator `sessionRouter.revive` runs, deliberately **re-validating** the recorded `Cwd`
  rather than trusting it, because a path confined when `change_workspace` stored it can be
  turned into an escape before the restart. It stays below the `everRan` gate, so a
  repeated frame naming a never-used conversation still drives no `MkdirAll` — the
  containment `activeSessionStarter.resolveSpawnDir`'s doc requires. Modes, atomicity and
  symlink handling are inherited unchanged from `Pool.Revive` / `Pool.saveLocked`
  (temp-file-plus-rename, 0600) and nothing here writes a file directly.
- **[Error messages, logs, telemetry]** No findings, one deliberate decision. The failed
  revive is reported as an **inert arm with a path-free record** instead of an error
  return, because `resolveSpawnDir`'s error names the resolved path and the `$HOME`
  boundary and `Pool.Revive`'s can name a settings path, while `handleNewSession`
  Warn-logs `start`'s error verbatim. New records carry `event` + `conversation_id` only.
  The conversation id is already logged unbounded on every resolvable arm and has passed
  `conversations.ValidID`, so it is provably 36 bytes — `boundedConvID` stays needed only
  on the invalid-shape arm. The two new Debug records deliberately make the dormant states
  separable in the log, which is what the report on this ticket could not do; neither is
  an existence oracle, because both fire strictly **after** the shared
  `no_bound_session` refusal that already declines to distinguish unknown from unbound.
- **[Concurrency]** No findings. No new goroutine, no new lock-order edge —
  `Pool.EverActivated` keeps `Pool.mu → Session.lcMu`. The resolve→gate→revive
  check-then-act window is documented above and is benign in both directions
  (`Pool.Revive`'s take path, and `RotateForNewSession`'s `ErrSessionNotFound`, which
  `startFreshRunner` already disarms the #1330 rotation gate for).
- **[Network & I/O]** No findings — no new frame, field, parse or size limit. The verb's
  existing envelope cap and `handleNewSession`'s dispatch are untouched.
- **[Subprocess execution]** No findings on the new code: nothing here builds an argv. The
  successor child's `--session-id` is minted by `Pool.RotateForNewSession` through
  `sessions.NewID`, and `streamsup.buildArgs` composes the flag — neither reachable from a
  client-supplied value.
- **[Tokens / credentials]** Not applicable — the design mints, stores, reads and revokes
  no credential. Session ids are not secrets and are already logged on this verb.
- **[Cryptographic primitives]** Not applicable — the only randomness on the path is
  `sessions.NewID`'s existing `crypto/rand` mint inside `RotateForNewSession`, unchanged.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model, "a paired
  client may drive daemon-side work": the relevant threat is a paired-but-hostile client
  replaying `new_session` to make the daemon do unpaid work. The gate order holds the line
  — an invalid id dies at the shape check, an unknown or never-used conversation dies
  before any filesystem or registry write, and only a conversation the daemon's own
  persisted state says has run reaches `Pool.Revive`. **Escalation of a dormant session's
  posture is explicitly out of scope and already closed upstream**: `Pool.Revive` carries
  the entry's model and effort and *no* persisted posture (#1487/#2448), so a reset cannot
  be used to restore a phone-granted bypass across a restart. This ticket adds a caller to
  that path and changes none of it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-20
