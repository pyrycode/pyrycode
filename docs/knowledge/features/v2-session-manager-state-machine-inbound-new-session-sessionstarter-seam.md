# Inbound new_session (#831, widened #2099) — `SessionStarter` seam

`new_session` is a v2 **control** envelope (phone → binary), intercepted in
`dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (beside
`TypeInterrupt` / `TypeDequeueMessage`) — there is **no** `dispatch.Route`
handler. **Since #2099** it carries an optional `NewSessionPayload{ConversationID
string}`: present and canonical, the daemon rotates *that* conversation's
session id and respawns its child; absent, empty, or a body that fails to
decode are one wire meaning — rotate the conversation the daemon's cursor
points at, the pre-#2099 behaviour verbatim. On the stream path this is a
**kill and respawn under a freshly minted session id**, never a `/clear`
keystroke — the terminal-era framing this doc previously carried is gone from
both the code and `docs/protocol-mobile.md` § New session (v2).
**`security-sensitive`**: `conversationID` is untrusted phone input and a
registry-validated **lookup key**, never authorization — the same posture
`request_attachment` and, since #2142, `attachment_chunk` publish (spec-stage
security review, verdict PASS; see [`codebase/831.md`](../codebase/831.md) and
the ticket's `docs/specs/architecture/2099-new-session-names-conversation.md`).

The frame is **fire-and-forget for every inert arm**, matching `dequeue_message`'s
no-op on an unknown id — answering would make the verb an existence oracle over
conversation ids, which the merged-refusal design below is what actually
prevents. **Since #2443 it is no longer reply-free in every outcome**: a rotation
that completes but cannot move into the conversation's recorded workspace earns
the verb's one and only reply — see [Workspace refusal reply
(#2443)](#workspace-refusal-reply-2443) below. That reply cannot become a second
oracle, because it is built only after a rotation has already happened, below
every inert arm this section describes.

## Trust boundary: the relay is a courier, `cmd/pyry` is the boundary

`internal/relay` imports neither `internal/conversations` nor
`internal/sessions` by design, so `handleNewSession` can neither shape-check
`ConversationID` nor resolve it against the registry — it decodes the payload
tolerantly (`_ = json.Unmarshal(env.Payload, &p)`; a decode failure leaves the
zero value, which is the cursor path) and passes the raw string straight to
`SessionStarter.StartNewSession(conversationID string) error`. Every check
lives in `cmd/pyry`'s `activeSessionStarter`, the sole implementation, which is
why the seam's own doc states the contract in the interface rather than
leaving a future caller to assume the string arrived validated.

Order in `handleNewSession` is load-bearing — capability gate before decode, so
a non-interactive conn's bytes are never parsed:

1. `!s.interactive` → inert (AC-4 negative path; a one-line check, not a shared
   abstraction, per CODING-STYLE over-DRY — `interrupt` / `dequeue_message`
   each keep their own).
2. `m.cfg.SessionStarter == nil` → debug `v2.new_session.inert` (foreground /
   pre-wire).
3. Tolerant decode, then `StartNewSession(p.ConversationID)`, best-effort: an
   error is `Warn`-logged (`v2.new_session.keystroke_err` — the event name is
   the one surviving `/clear`-era label, kept rather than churned for a string
   with no behavioural meaning) with `conn_id` + the sentinel only, never
   payload bytes.

## `activeSessionStarter.StartNewSession` — the resolution order is the design

The empty-string branch runs **before** `conversations.ValidID`, not after:
`ValidID("")` is false by the function's own doc, so reversing the two would
fail every un-upgraded client's bare frame — the shape `new_session` had from
\#831 until #2099 — and silently break backward compatibility. From there,
every ambiguous state is inert (no rotation, no respawn, no spawn of a child
that didn't exist, never a fall-through to the bootstrap session):

1. `conversationID == ""` → the pre-#2099 path, verbatim: rotate
   `currentConv()`'s conversation. The cursor's own id is daemon-authored and
   deliberately **not** shape-checked, since checking it would change behaviour
   on the one path this branch exists to preserve.
2. named but `!conversations.ValidID` → inert, never reaches the registry.
3. `resolveBound` (over `resolveBoundSession`) fails → inert. **Unknown id and
   known-but-unbound id share one debug record** (`v2.new_session.no_bound_session`):
   `resolveBoundSession` refuses both identically and returns `(nil, "", false)`,
   never falling through to `Pool.Lookup("")`'s bootstrap session (the #678
   isolation point). The non-distinction is deliberate, not an oversight — it
   is also what denies a hostile paired client an existence oracle.
4. bound runner exposes no `RestartFresh` → inert (`v2.new_session.no_restart`).
5. **named** conversation whose runner reports `State().ChildPID == 0` → inert
   (`v2.new_session.no_live_child`). Named-only: see below.

Every refusal logs at **debug**, because the id is client-supplied — matching
`handleDequeueMessage`'s posture for its own client-named `conversation_id`,
and deliberately not `activeInterrupter`'s Info (which records a
daemon-authored arm identity, not a client string).

### A capability probe answers "could", not "is" — the arm that shipped broken

Row 4 and row 5 look like one guard and are not, and conflating them is exactly
how AC-4's fourth case ("named conversation with no live child") shipped unmet
in the first PR (#2157) despite the plan reasoning it was free. `interface{
RestartFresh(string) }` is a **type** assertion — it asks what the runner
*can* do, and production's only implementation, `streamRunner`, answers yes
**unconditionally**: `RestartFresh` is exposed regardless of whether a child
has ever spawned, and `Pool.buildSession` assigns it at mint time. Since
\#2085, `create_conversation` binds a conversation's `CurrentSessionID` without
spawning a child — the spawn waits for the conversation's first message — so a
created-but-unmessaged conversation sailed through rows 1–4 with a real,
capable runner and reached `Pool.RotateForNewSession`, which checks only pool
membership. The result was observable damage with nothing to show for it: the
pool rekeyed, `sessions.json` persisted, the conversation rebound, and a
`session_transition` broadcast telling every client to render a delimiter for
a chat that had never had a turn, while `RestartFresh` itself spawned nothing.

The fix is a second, **runtime-state** guard below the capability probe:
`State().ChildPID == 0` (unstarted, backing off, evicted, and stopped all read
0 — every state with nothing to restart fresh; a spawn that started but has
not yet published its pid also reads 0 and is refused, which is the same
fail-safe direction the whole reject set takes, and the frame is re-sendable).
**Generalizes:** a "can this be done" interface assertion and a "has this
already happened" state read are different questions, and a reject set that
needs the second will pass every test that only drives the first.

**The test double is what let the wrong conclusion stand, so it changed too.**
The original `TestActiveSessionStarter_InertArms` row for "no live child"
injected a fake lacking `RestartFresh` entirely — a shape production never
produces for a bound conversation — so it exercised row 4, not row 5, and
greened on a fiction. `restartFreshRunner` (the test double) now offers
`RestartFresh` **always** and carries liveness in `State()`, exactly as
`streamRunner` does, splitting what had been one table row into two: a
capability row (`baseRunner{}` → `no_restart`) and the actual liveness row (a
capable runner with `childPID == 0` → `no_live_child`). **Generalizes:** a test
double that omits a method the production type always provides doesn't just
under-test — it can make the wrong arm look green. When a reject set has two
adjacent refusals, the double must be able to reach both.

### The no-live-child guard is named-only — a known, accepted asymmetry

AC-3 requires the bare (cursor) frame to keep rotating an evicted conversation
exactly as it did before #2099; AC-4 requires a *named* childless conversation
to be inert. A guard applied to both paths would buy the second at the cost of
the first, so `named && runner.State().ChildPID == 0` is scoped to the frame
half this ticket introduced. `TestActiveSessionStarter_UnnamedFollowsTheCursor`
drives a runner with **no** live child specifically to pin the bare path still
rotates it; both directions are mutant-verified (dropping the guard reddens
only the AC-4 row; dropping the `named &&` conjunct reddens only this pin).

The accepted cost: `Pool.Lookup` is plain map membership, so an **idle-evicted**
session stays in `p.sessions` with its conversation still bound — the same
`ChildPID == 0` shape as a never-messaged one. A bare frame on that
conversation still rotates it and it comes back up under the fresh id; the
identical conversation addressed by **name** is now inert. Both halves are
individually published (`docs/protocol-mobile.md` § New session states the
inert set, and separately promises the absent-field path is the pre-#2099
behaviour verbatim), but the document does not say the two disagree on this
one state, and pyrycode-desktop#1087 / pyrycode-mobile#625 always name their
conversation — so on a chat idle long enough to evict, **New session** becomes
a silent no-op with no reply to explain it, where the bare frame would have
worked. Flagged at PR review (verdict PASS; a should-fix for the ticket owner,
not a defect) and left as shipped rather than re-litigated here.

**Answered by #2103, for `interrupt`: no asymmetry arises there, and no guard was
added.** `new_session`'s guard exists because `RestartFresh` on a childless runner
mutates before it can discover there is nothing to restart; `interrupt`'s actuation
(`WriteInterrupt`) checks for a nil writer first and returns `ErrNoLiveChild`
having written nothing, so it is already inert by construction on both the named
and bare paths — adding a guard would have introduced the asymmetry rather than
closed it. See [interrupt § No liveness
guard](v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md#no-liveness-guard--a-blockers-late-fix-is-not-automatically-the-twins-requirement)
for the generalized rule this ticket pair settled: a guard copied from a sibling
is warranted only when the sibling's actuation can leave damage behind with
nothing to show for it, not merely because the sibling needed one.

## Dormant reset: previously-used, no live child (#2521)

Row 5's `no_live_child` guard conflated two states that both read
`State().ChildPID == 0`: a conversation created but never messaged (#2085,
the guard's original subject) and one that ran, then stopped, backed off, or
was idle-evicted. The reported symptom — pressing **New session** on an
existing channel did nothing until a message was sent first — was the second
state, and a daemon restart added a third: after `sessions.New` materialises
only the bootstrap, a previously-used conversation's session is a persisted
`Pool.dormant` entry `resolveBound` (`resolveBoundSession`) simply misses,
landing on row 3 (`no_bound_session`) instead of row 5. The message route
never had this gap — `sessionRouter.resolve` already re-materialises through
`sessionRouter.revive` on `ErrSessionNotFound` — which is why "send one
message first, then Reset" was the workaround.

**The discriminator is "has this conversation ever run?", answered by
[`Pool.EverActivated`](sessions-package-key-types-reviving-a-dropped-session-pool-revive.md).**
It reads a live session's `lastActiveAt` against its `createdAt` — equal to
the nanosecond for a never-activated session, since `buildSession` stamps
both from one `now` — or, when the pool holds no session, the identical pair
off the `Pool.dormant` entry. Both fields are already persisted, so the
reading survives a restart with no schema change. `activeSessionStarter`
gates the row-5 refusal on it (`named && !live && !used && !hasEverRun(oldID)`)
and gains two seams for the after-restart case: `resolveDormant` reads the
conversation's persisted binding without consulting the pool (repeating
`resolveBoundSession`'s empty-id / unbound refusal — the #678 isolation
point — rather than falling through to the bootstrap session), and
`reviveBound` materialises it via `resolveSpawnDir` + `Pool.Revive`, byte for
byte `sessionRouter.revive`'s body. `everRan` sits strictly **between**
`resolveDormant` and `reviveBound` (`reviveDormantBound`,
`cmd/pyry/new_session_dormant.go`) because `Pool.Revive` registers and
persists: a never-used conversation must stay inert **without** that
mutation, not merely without a rotation.

Three new records join the existing set, all still Debug except the failed
revive:

| Condition | Record |
|---|---|
| Bound, dormant, never activated | `v2.new_session.dormant_never_used` |
| Bound, dormant, previously used, revive failed | `v2.new_session.revive_failed` (Warn) |
| Bound, dormant, previously used, revived | `v2.new_session.revived_dormant` |

A failed revive is **inert, not an error return** — the same confidentiality
posture as the workspace-refusal channel below: `resolveSpawnDir`'s
confinement error names the resolved path and the `$HOME` boundary, and
`Pool.Revive`'s can name a settings path, while `handleNewSession` Warn-logs
whatever `start` returns verbatim. The record carries the event and the
conversation id and nothing else. The #2085 never-used refusal is otherwise
**untouched**: `TestActiveSessionStarter_InertArms`'s existing rows stay
green unedited, which is the proof the refusal was narrowed rather than
removed.

**The named-only asymmetry documented above still applies unchanged**: a
bare (cursor) frame on a childless conversation rotates without ever
consulting `everRan`, exactly as before #2521.

**A discriminator read from two sources is only as durable as the move
between them — the reading did not survive a *revive*, only a *restart*.**
`materialise` (shared by `Pool.Revive` and `GetOrCreateIn`) stamps a fresh,
equal `createdAt`/`lastActiveAt` pair and deletes the dormant entry in the
same step, so a session that was revived but not yet activated answered
`false` on both of `EverActivated`'s sources — the live arm because the pair
was freshly equal, the dormant arm because the fallback had just been
retired. That is exactly where `send_message`'s `/clear` intercept
([§ A second caller](#a-second-caller-in-a-different-goroutine-position-2456))
lands: `Route` revives a dormant session for binding validation before the
intercept ever raises the reset, so the typed `/clear` reached this seam
with a session `EverActivated` had just been made to lie about, and worked
on the desktop button while doing nothing for the identical channel typed
into. The fix carries the retired entry's timestamps onto the session
`materialise` registers, described where the fix lives: [`Pool.Revive` §
Reviving a dropped
session](sessions-package-key-types-reviving-a-dropped-session-pool-revive.md).
**Generalizes:** when a predicate reads a primary source with a persisted
fallback, the operation that converts one into the other is the case to
test first, not the restart that merely proves the fallback durable on its
own.

**A repeated named Reset now rotates every time, kept deliberately.**
`rekeyLocked` stamps `lastActiveAt` while `createdAt` is immutable, so after
one reset `EverActivated` is true for the successor and a second Reset with
no message in between rotates again — before #2521 the second frame was
inert. Making it inert again needs a signal no timestamp pair carries ("has
a child ever spawned under the *current* id"), for a state no acceptance
criterion names; the cost is one extra separator and one re-key for a
session that has had no turn, operator-driven rather than client-replayable.

## Workspace re-read on rotation (#1475)

`change_workspace` records a `$HOME`-confined realpath on the conversation, but through #1474 the only production reader was `sessionRouter.revive`, reached on a `Pool.Lookup` miss after a daemon restart — moving a conversation's workspace was a `conversation_updated` reply that changed nothing until the daemon happened to restart. #1475 makes `new_session` rotation the second fresh spawn: `resolveBound` (over `resolveBoundSession`) now also hands back the conversation's **raw, unvalidated** `Cwd` alongside the runner and the old id, and `StartNewSession` re-confines it — `activeSessionStarter.resolveSpawnDir`, wiring the struct's `spawnDirFor` seam to the same `resolveSpawnDir` the mint and revive paths use — before calling `startFreshRunner`.

**Re-validate, not trust — `revive`'s posture, not `sessionMinter.Create`'s.** `conv.Cwd` is raw persisted bytes in a mutable file, re-read here possibly across a daemon restart: a path confined when `change_workspace` stored it can become an escape before this rotation runs, the identical situation `revive`'s own doc argues for. `sessionMinter.Create` may skip re-validation only because its `resolveSpawnDir` output is frozen onto the session at build time; nothing is frozen here.

**Ordering: the install sits between `rotate` and `RestartFresh`, never outside that window.** Below `rotate`, because a *failed* rotation must leave the runner's directory untouched — installed earlier, the next unrelated crash-respawn would silently move a child no rotation ever replaced, breaking the "`change_workspace` alone does not move a running child" guarantee from a lost race, not a bug. Above `RestartFresh`, because that call cancels the live child immediately and the successor's own `beginSpawn` would otherwise race the install. `resolveSpawnDir`'s call itself sits **below every inert arm** above and outside `startFreshRunner` entirely: it does real filesystem I/O (realpath, `MkdirAll`, a `~/.claude.json` write) on `V2SessionManager`'s single dispatch goroutine, and hoisting it above the inert arms would let a frame naming a conversation with no live child drive that I/O on the daemon's behalf. No timeout guards it — a deadline cannot interrupt a syscall, so the wedged-filesystem stall is named and accepted rather than defended with a protection it wouldn't actually have, the same posture `createConversationMintTimeout` takes on the neighbouring mint path.

**Fail-closed, and the refusal record never carries a path.** An empty recording and a refused one both leave the runner in its current directory and let the rotation complete; the two differ only in whether a `v2.new_session.spawn_dir_rejected` Warn is written, and that record carries the event and `conversation_id` only — never the resolved path or the wrapped confinement error, the same channel `Pool.Revive` and `sessionTranscriptDir` already keep closed for a phone-influenced workspace path. A second, distinct event (`v2.new_session.spawn_dir_install_failed`) covers the directory vanishing between confinement and install — rare, and by then the rotation is already committed, so the install failure is Warned and swallowed rather than aborting a rotation that has already re-keyed the pool. **Since #2443 the refusal also reaches the client**, not only the log — see below — but the bound is unchanged: `resolveSpawnDir`'s own confinement error is still discarded at its own site and never travels past the `bool` it returns, so nothing downstream, wire or log, can leak it even by accident.

## Workspace refusal reply (#2443)

`activeSessionStarter.resolveSpawnDir`'s signature is `(convID, recordedCwd string) (dir string, refused bool)` — the `bool` reports **the same condition** the `spawn_dir_rejected` Warn above reports, on purpose, so the wire and the log can never disagree. `StartNewSession` reads it *after* `startFreshRunner` returns nil (the rotation committed) and turns a `true` into `&relay.RotatedWithoutWorkspaceError{ConversationID: convID}` — `convID` here is already the daemon-resolved id (past `conversations.ValidID` on the named path, or the cursor's own on the bare path), never the client's raw string. `handleNewSession` discriminates with `errors.As` above its best-effort `Warn` arm and, on a hit, sends the requester exactly one `TypeError` reply (`newSessionReplyWorkspaceRefused`) carrying `protocol.CodeNewSessionWorkspaceRefused`, a static message, `Retryable: false`, and that conversation id in `ErrorPayload.ConversationID` — the field #2443 added because a bare frame names no conversation of its own, so `in_reply_to` alone cannot tell the client which one the daemon rotated.

**A rotation error wins over a refusal, and the ordering is what makes that true.** `StartNewSession` checks `startFreshRunner`'s error *before* it looks at `refused`: when rotation fails outright — losing a race to a concurrent `new_session` is the ordinary way to land there — that plain error is returned and the refusal is never turned into a `RotatedWithoutWorkspaceError`, even though `resolveSpawnDir` still wrote its Warn. Reporting "rotated without the workspace" for a rotation whose `session_transition` never fired would be a claim the client has no way to check. `TestStartNewSession_RefusedWorkspaceThenRotateError_ReportsTheRotateError` pins it.

**The refusal travels as a typed error, not a sentinel, because the reply must NAME a conversation and a sentinel carries no fields.** Widening `SessionStarter.StartNewSession`'s `error`-only return (to a `(refused bool, err error)` pair, say) was the alternative and was rejected: it would have touched every construction site and test double for a seam with exactly one caller (`handleNewSession`) and one implementation (`activeSessionStarter`), for no expressive gain a typed `errors.As` match doesn't already give for free. `*RotatedWithoutWorkspaceError.Error()` is a **compile-time constant string** — deliberately, because `handleNewSession`'s neighbouring arm logs a plain seam error **verbatim** (`v2.new_session.keystroke_err`), so an id or path folded into `Error()` would reach that log the moment a future edit reordered the two `errors.As` checks. The id lives in the struct field and nowhere else; the underlying confinement error is discarded inside `resolveSpawnDir` and never reaches this type at all.

**Proving the refusal in a test needs a mutation that actually reaches it — two obvious ones do not.** `confineWorkdirToHomeCreating` *accepts* both of the first ideas: a deleted recorded directory is silently re-created by its own `MkdirAll` leg, and an existing regular file inside `$HOME` passes both containment checks untouched (nothing left to create, and `EvalSymlinks` resolves a plain file fine). Either produces a green rotation with the workspace **accepted** — the missing error frame reads as a slow daemon, not a bug. Re-pointing the recorded path at a directory **outside `$HOME` with a symlink** is the one mutation that reaches the refusal, and it is also the ticket's literal scenario (`internal/e2e/relay_v2_new_session_refused_workspace_test.go`). **Generalizes:** a test asserting a *refusal* of a filesystem confinement check needs to check what the confinement function actually accepts before picking how to break the input — "delete it" and "swap in a file" are the two mutations that read as sabotage and are not.

**`cmd/pyry` never calls `slog.SetDefault`.** `startFreshRunner`'s install-failure record needed the daemon's own logger threaded through explicitly (`activeSessionStarter.log`, nil-tolerant for the two direct test callers) — reaching for `slog.Default()` inside a package-level helper here would have sent the record to stderr under Go's default handler, outside the daemon's configured log entirely, not merely at the wrong level.

**Two known residuals from code review, deferred rather than fixed (2026-09-15).** `streamRunner.SetSpawnWorkDir` (`cmd/pyry/streamsup_runner.go`) is reached by type assertion off `Session.Runner()` with no compile-time pin (`var _ interface{ SetSpawnWorkDir(string) error } = streamRunner{}`) — the file keeps one for each of its other capability shapes, and without it a signature drift on the adapter would make this whole ticket inert in production (the rotation still "succeeds", nothing moves) behind a fully green suite and no log line. And `startFreshRunner`'s own doc states no blocking filesystem I/O happens inside the #1330-armed window — true of `resolveSpawnDir` (correctly hoisted above every inert arm here) but not of `installSpawnDir`: `streamRunner.SetSpawnWorkDir` runs two `agentrun.ResolveWorkdir` symlink-resolution walks *inside* that window, between `beginRotationOrNoop` and `RestartFresh`. Neither was blocking (both SHOULD FIX), so neither was fixed pre-merge; worth closing before either becomes an observed failure rather than a reviewed risk.

## The wrap-up turn, and the reply's tense (#2477)

A named or cursor conversation with a **live child** now gets a wrap-up turn
before `startFreshRunner` runs at all: the daemon drops the conversation's
queued backlog (`msgqueue.Queue.Snapshot` + `Remove`, tolerating a refused
committing head — see [msgqueue-package.md § Introspection, removal, and
change notification](msgqueue-package.md#introspection-removal-and-change-notification-719)),
interrupts and waits idle, delivers a fixed daemon-owned prompt as an
ordinary user turn bounded at 90 seconds, and writes that turn's assistant
text as the conversation's handoff note
([`Pool.WriteHandoffNote`](sessions-package-key-types-handoffnote-store.md))
before rotating. The reply is captured by `wrapUpCapture`, a sink decorator
chained into `newStreamRunnerFactory` beside `newSessionResetFollower` — see
[Following claude's announced reset § A second instance of the placement
rule](streamsup-package-announced-reset-follower.md#a-second-instance-of-the-placement-rule-wrapupcapture-2477).
Every failure along this path — idle timeout, an empty or blank reply, a
reply the store's own admission refuses, `ErrHandoffNotesDisabled`, any store
error — leaves the previous note standing and never fails the reset.

**The 90-second bound is a testing seam that can only shorten (#2486).**
`-pyry-wrapup-deadline` (`cmd/pyry/main.go`) feeds `conversationReset.deadline`,
and `bound()` clamps it on both sides — zero *and* anything above
`wrapUpDeadline` both answer `wrapUpDeadline`, so the only reachable effect is
a *shorter* wrap-up. Every production daemon runs with the flag unset, which
is the same zero-means-default shape as `-pyry-conv-sweep-interval`
([`conversations-auto-archive.md` § Single seam](conversations-auto-archive.md#single-seam-configsweepinterval-262)).
The ceiling exists because `wrapUpDeadline`'s own doc fixes ninety seconds as
not operator-editable, on the ground that a longer bound is a way to hold a
child open — a bare override flag would have repealed that sentence, so the
clamp lives in `bound()` itself, the one function total over every
construction path including the struct literals this package's unit tests
build directly. The live suite arms it to prove the reset's failure path:
[`e2e-realclaude.md`](e2e-realclaude.md) covers
`TestInteractiveStreamClearWrapUpSkipped`, which drives a `/clear` under a
one-millisecond bound and observes `skipped` on the `restarting` edge with
the conversation's stored note left byte-identical.

**Blocking `handleNewSession` for up to 90 seconds was not an option**, because
it runs on `V2SessionManager`'s single Run dispatch goroutine and would freeze
frame dispatch for every connection the daemon hosts. So `StartNewSession`'s
new arm hands `wrap-up → rotate` to its own goroutine and returns at once —
**the synchronicity of this seam is a fact that is only visible from reading
the handler**, not from anything this ticket's own filed body or technical
notes said. Trace any new blocking work up to its actual dispatch site before
sizing it; a 90-second bound reads very differently once you know what it
blocks.

### Three attempts at the reply, and why the third one stuck

Moving the tail off Run reopens the [workspace refusal reply](#workspace-refusal-reply-2443)
above, because that reply can no longer seal at the moment `StartNewSession`
returns — the wrap-up has not run yet. Two earlier answers were tried and
retracted before the shipped one:

1. **Return `*relay.RotatedWithoutWorkspaceError` immediately, before the
   rotation happens.** Rejected on review: that type's own doc fixes the
   completed rotation as a **precondition with a tense** — the pool re-keyed,
   the transition already broadcast, "by the time this value exists" — and
   forbids the value outright for a rotation that fails. **A doc comment that
   states a precondition as a tense is a contract, not colour**; reading it as
   a connotation that could be softened ("committed to" rather than
   "completed") was the mistake, not a defensible interpretation.
2. **Withhold the reply on this arm (return `nil`).** Shipped, then reverted:
   it broke `TestRelayV2_NewSessionRefusedWorkspaceRepliesToRequester`, an
   **e2e pin belonging to #2443**, another shipped ticket. The reasoning that
   led here — "a shipped behaviour lost is strictly better than a shipped
   behaviour made false" — was made without checking whether the behaviour
   being traded away had a test. It did. **Weighing a behaviour you are about
   to drop means finding out who asserts it — `grep` for the behaviour, not
   just the symbol, before trading it away.**
3. **Deliver the reply late, once the rotation actually lands.** Both
   commitments — the type's tense, and #2443's e2e pin — hold simultaneously
   once the reply is allowed to travel on its own schedule. `internal/relay`
   grows `LateSessionStarter`, an **optional** widening of `SessionStarter`:
   ```go
   type LateSessionStarter interface {
       SessionStarter
       StartNewSessionLate(conversationID string, outcome func(error))
   }
   ```
   `handleNewSession` asserts it on the configured seam and falls back to the
   plain method when absent, so an implementation that rotates inline —
   every pre-#2477 test double — needs no change and sees no new behaviour.
   `outcome` is called exactly
   once with what `StartNewSession` would have returned, **after** the
   rotation it may report — `resetThenRotate` mints
   `RotatedWithoutWorkspaceError` under the rotation that makes it true, so a
   rotation that fails still reports the plain error and makes no workspace
   claim. The value travels back to Run on a new `newSessionDone` channel and
   is answered in `handleNewSessionDone` — **the identical off-Run-producer
   shape #1491 already built for the debug bundle**
   ([Inbound debug-bundle request § `assembleBundle`](v2-session-manager-state-machine-inbound-debug-bundle-request-request-deb.md)):
   a bounded blocking send with `s.done`/`ctx` as the only escapes, and a
   staleness guard (`res.s.state != V2StateOpen`) borrowed from
   `handleBundleReady` for the identical reason — sealing under a dead session
   burns a send-nonce nobody awaits. Arms 4 and 5 of `handleNewSession`'s
   contract (the workspace-refused reply and the best-effort Warn) moved into
   one `answerNewSession`, so the inline and deferred paths discriminate in a
   single place rather than two that can drift. `var _
   relay.LateSessionStarter = activeSessionStarter{}` pins the capability at
   compile time — the config field stays typed as the plain `SessionStarter`,
   so without that assertion a drifted signature would silently fall back to
   the synchronous path and drop the reply rather than fail to build, which is
   exactly how attempt 2 shipped red once its own regression was found.

**Generalizes: when two contracts look mutually exclusive, suspect the seam
before picking a victim.** Both retracted attempts accepted `StartNewSession(string) error`'s
signature as fixed and then argued over which of two bad answers to give
inside it. The actual constraint was that the seam carries no frame id to
correlate a late reply on — a seam limitation, not a protocol one — and this
package had already solved that exact problem once, for the debug bundle.
Asking "has this package had an off-Run producer that owed a reply before?"
would have been cheaper than either retracted attempt.

## The `resetting` producer, and why it doesn't buffer (#2478)

`resetThenRotate` (the wrap-up-then-rotate tail above) now brackets itself with
three `resettingEmitterV2` calls — a rising `wrapping_up`/`pending` before
`wrapUp`, a rising `restarting` carrying the handoff outcome after it, and a
`defer`-registered falling edge — reported to every interactive client as
[`resetting`](../../protocol-mobile.md#resetting). `wrapUp` and `storeNote`
(`cmd/pyry/session_reset.go`) were widened from no return value to a `bool`
so the emitter learns `written` vs `skipped` without ever seeing the note's
own text: the emitter holds no note store, no reply text and no
`conversationReset`, so the trust boundary the SECURITY review asked for is
structural rather than a discipline someone has to remember.

**Matching the nearest sibling by class was the wrong question; matching the
caller's blocking contract was the right one.** `queueStateEmitterV2` and
`sessionErrorEmitterV2` — the two other v2 status producers, and the obvious
shape to copy — buffer their frames onto a `Run` goroutine with a
drop-on-full send. That shape exists because *their* callers (msgqueue's
`OnChange`/`OnGiveUp` seams) are under a MUST-NOT-BLOCK contract the producer
has to honor. `resetThenRotate` already runs on its own goroutine precisely
so a 90-second wrap-up can't block dispatch, so the queue buys nothing — and
copying it anyway would have cost something real: a drop-on-full send can
drop exactly the falling edge, and `resetting`'s published wire contract
(every rising sequence ends in a falling one, so a client needs no timeout of
its own) depends on that edge never being droppable. `resettingEmitterV2` is
synchronous instead, copying `attachmentOfferEmitterV2`'s shape — the other
v2 emitter that already fans out off its caller's own goroutine. **Generalizes:**
before shaping a new producer after the nearest one of the same wire class,
check what contract the *caller* is under, not just what the sibling looks
like — two producers can be the same class of frame and owe opposite
concurrency answers.

**The falling edge has to land before `release()`, and only defer-stack LIFO
makes that true — it is not something a reviewer can spot by reading the
single-reset path.** `resetThenRotate` registers `defer release()` first and
`defer resetting.done(convID)` second, so `done` runs *before* `release` on
unwind. Reversed, every single-reset test still passes — the bug only shows
up across two resets, where `release` admits a second reset whose
`wrapping_up` can then reach the wire ahead of the first reset's
`active:false`, and a client clears an indicator it had just re-opened. The
shipped test doesn't infer the ordering from behavior; it reads the frame
count standing *at* the `release` call itself
(`TestResetThenRotate_FallingEdgePrecedesTheRelease`). **Generalizes:** an
ordering guarantee that depends on where in a defer stack a cleanup call sits
needs a test that observes the stack mid-unwind, not one that only checks the
final frame sequence — the failure mode is invisible on the path every other
test already exercises.

## Log-bounding: only the invalid-shape arm needs it

Every arm past `conversations.ValidID` logs a string already known to be 36
bytes. The shape-check-failure arm is the **only** place an arbitrary,
attacker-controlled string reaches a log call, bounded upstream only by the
application-envelope byte cap. `boundedConvID` (`cmd/pyry/main.go`) renders it
as-is under 64 bytes or a **copied** prefix plus an elision marker otherwise —
the copy matters as much as the truncation: `s[:n]` on the decoded payload
would pin the whole frame's backing array in a buffered log record, and an
unmarked truncation would read as a complete id and send a reader chasing a
conversation that was never named. `handleDequeueMessage` has the identical
unbounded exposure today and was the thing this would have copied verbatim had
it not been caught in security review.

## A second caller, in a different goroutine position (#2456)

`send_message`'s `/clear` intercept ([`relay-package-handlers.md` § `send_message` grows an eighth seam](relay-package-handlers.md#send_message-grows-an-eighth-seam-the-clear-intercept-2456)) gives this seam a second caller, and the two sit in materially different places: `handleNewSession` runs on `V2SessionManager`'s single `Run` dispatch goroutine (§ *The wrap-up turn* above explains why blocking it for 90 seconds was never an option), while `SendMessage` — a `dispatch.Route` handler — runs on the per-conn worker goroutine `routeAppFrame` spawns (see [`v2-session-manager-concurrency.md`](v2-session-manager-concurrency.md)), never on `Run`.

**Reasoning transferred from one caller to the other would have been wrong in both directions.** `resolveSpawnDir`'s documented blocking (realpath, `MkdirAll`, a `~/.claude.json` write, no timeout) stalls `Run` — and therefore every connection the daemon hosts — when `handleNewSession` calls it; from the `/clear` intercept it stalls only that one connection's own later frames, never `Run` and never another conversation. That difference is also what makes an inline, synchronous seam call safe here with no `LateSessionStarter`-style deferred reply: `StartNewSession` (the plain form, not the late one — see § *Workspace refusal reply* above for why the late form exists at all) is documented safe from any goroutine, and the one slow arm (the wrap-up) already hands itself to its own goroutine inside `resetThenRotate` regardless of which caller invoked it. Nothing about the seam itself changed to support the second caller; only the caller's own goroutine position determined what was safe to do around it. **Before reusing a blocking-cost or concurrency argument made for one caller of a seam, check which goroutine the new caller actually runs on — the seam's own safety contract can be identical while the consequence of calling it is not.**

## Multi-phone / scoping

Any interactive paired phone can send `new_session`; naming a conversation
widens *which* conversation one frame can reach (from "the cursor's" to "any"),
not the trust boundary — a hostile-but-paired device could already reach any
conversation via a two-frame dance (route a message to move the cursor, then
send the bare frame). The named form removes that dance, which is also the
misfire a *benign* client could not previously avoid: opening a chat without
sending is the desktop sidebar's normal case, and the old bare frame restarted
whichever conversation the shared cursor pointed at. The cursor itself is
**never written** by any of this — only a successful `sessionRouter.Route`
stamps it — so a named rotation leaves the active conversation exactly where
it was.

## Related

- [`sessions-package-key-types-reviving-a-dropped-session-pool-revive.md`](sessions-package-key-types-reviving-a-dropped-session-pool-revive.md)
  — `Pool.EverActivated`, the dormant-reset discriminator, and the
  `materialise` timestamp carry that keeps it durable across a revive, not
  only a restart.
- [`e2e-harness-stream-interactive-harness-pattern-startstreamin.md`](e2e-harness-stream-interactive-harness-pattern-startstreamin.md)
  — `RestartStreamInteractiveWithRelay` and the two #2521 specs that drive
  this section's dormant arms end to end.
- [`sessions-package-key-types-handoffnote-store.md`](sessions-package-key-types-handoffnote-store.md)
  — the store the wrap-up turn's reply is written through, and the write-side
  admission #2477 added.
- [`streamsup-package-announced-reset-follower.md`](streamsup-package-announced-reset-follower.md#a-second-instance-of-the-placement-rule-wrapupcapture-2477)
  — where `wrapUpCapture` sits in the sink chain, and why.
- [`v2-session-manager-state-machine-inbound-debug-bundle-request-request-deb.md`](v2-session-manager-state-machine-inbound-debug-bundle-request-request-deb.md)
  — the off-Run-producer / Run-answers shape `LateSessionStarter` reuses.
- [`v2-session-manager-state-machine-inbound-dequeue-message-queueremover-sea.md`](v2-session-manager-state-machine-inbound-dequeue-message-queueremover-sea.md)
  — the tolerant-decode / debug-no-op shape this handler mirrors.
- [`v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md`](v2-session-manager-state-machine-inbound-interrupt-interrupter-seam-esc.md)
  — `activeInterrupter`'s logger-field precedent, and #2103, which gave
  `interrupt` the identical optional `conversation_id` and settled the
  cursor-vs-named liveness-guard question this doc raised (answer: no guard,
  no asymmetry — `interrupt`'s actuation refuses without writing, unlike
  `RestartFresh`).
- [e2e-harness.md § Build Helper](e2e-harness.md#build-helper) — the
  `-overlay` mutation-testing pitfalls this ticket's verification hit again,
  in a new shape: see that section's note on `go test` caching a daemon-spawning
  suite even with the source edited in place.
- [fakephone-harness.md § Library trade-off](fakephone-harness.md#library-trade-off-timed-out-receive)
  — why the e2e case asserts "nothing rotated" from the daemon's log rather
  than by waiting on a `Receive` that will never arrive.
- [`relay-package-handlers.md` § `send_message` grows an eighth seam](relay-package-handlers.md#send_message-grows-an-eighth-seam-the-clear-intercept-2456)
  — the seam's second caller (#2456): a `/clear` in message text reaches this
  same entry point instead of claude, on a different goroutine than
  `handleNewSession`'s.
