# Spec #1201 — A per-conversation turn-busy tracker fed from the stream-json turn fan-in

**Ticket:** [#1201](https://github.com/pyrycode/pyrycode/issues/1201) · **Size:** S · **Labels:** `size:s`, `bug`, `security-sensitive`

One new `cmd/pyry` file holding a self-synchronised, per-conversation busy set, fed
from the drain **before** its active-session gate, plus the drain parameter and the
`relay.go` wiring that constructs it. **Ships unwired** — nothing reads the signal,
no v2 frame changes, no delivery behaviour changes.

---

## Files to read first

Turn-1 data load. Read these before writing any code; every design decision below
cites one of them.

| Path | Extract |
|---|---|
| `cmd/pyry/stream_turn_drain.go` (whole file, 143 lines) | **The file you are modifying.** `streamTurnEnvelope{sessionID, ev}` (`:23-26`) is the only point in the pipeline holding a producing identity. `startStreamTurnDrainV2`'s three-case select (`:122-140`) — the `sink.ch` arm at `:126-136` is where `observe` goes, above the `activeSession()` gate at `:127`. Also the SECURITY note style at `:74-79` / `:129-133` (content-free discriminants only) and the "channel is NEVER closed" shutdown reasoning at `:37-40`. |
| `cmd/pyry/stream_turn_drain_test.go` (whole file, 426 lines) | The 7 call sites that take the new parameter (`:196, :236, :264, :309, :349, :375, :409`) and **every helper the new drain-tier tests reuse**: `stubActiveSession` (`:32`), `chanBcast`/`newChanBcast` (`:48-58`), `dropWatcher` + `waitDropKind` (`:71-96`, `:153-166`), `feedLines` (`:112-117`), `collectEnvs` (`:121`), `assertNoPush` (`:168`), `testConvIDB` (`:27`), the line fixtures (`:100-108`). Do not write new doubles — these cover the whole drain tier. |
| `cmd/pyry/relay.go:702-726` | The stream-mode branch. `:719` builds the emitter, `:725` builds the `activeSession` closure, `:726` starts the drain — the tracker is constructed between `:719` and `:726`, local to this branch. |
| `cmd/pyry/relay.go:755-756` | **Copy this closure verbatim**: `func(sid string) (string, bool) { return conversationForSession(w.convReg, sid) }` — already in scope here, already the session→conversation resolver, and it keeps `internal/conversations` out of the tracker's file. |
| `cmd/pyry/relay.go:799-826` | `conversationForSession` — the `sid == ""` guard (`:817-819`, rationale `:807-809`), the `CurrentSessionID` **or** `SessionHistory` match (`:821`, why a just-rotated session still resolves), and the `List()` race-safety note (`:813-815`). Read-only: the tracker adds a reader, not a policy. |
| `cmd/pyry/relay.go:412-437` | `boundSessionIDForActive` + its #678 `CurrentSessionID == ""` rationale. Read-only context for **why the tracker does not use it**: it answers "which session is the *active* conversation on", the wrong direction for a per-conversation tracker. Byte-identical, untouched. |
| `internal/eventring/ring.go:1-95` | **The primitive to mirror.** Package-doc voice for a self-synchronised per-conversation store, `mu sync.Mutex` + `map[string]…` (`:63-67`), "All methods are safe for concurrent use" (`:62`), and `New`'s panic-on-misconfig (`:78-85`) — the precedent for the constructor's nil-resolver panic. |
| `internal/turnevent/event.go:23-125` | The sealed 8-variant set and the "no variant carries conversation identity" contract (`:75-76`). The type switch in `observe` enumerates these. |
| `internal/streamsup/parser.go:137-226` | The **only** producer into the sink: `TurnEnd` (`:157`), `TextChunk` (`:193`), `ThoughtChunk` (`:195`), `ToolStart` (`:197`), `ToolUpdate` (`:220`), and the `default:` tolerate-and-drop arm (`:158-163`) whose growth path — `rate_limit_event` → a future `ApiRetry` — is the whitelist's actual evidence. `resultTurnEndReason` (`:173`) shows both stop reasons traverse one arm. |
| `cmd/pyry/interactive_turn_v2.go:399-422` | `eventKind` — the content-free discriminant for the tracker's one log field. **Reuse it; do not write a second one** (this is why the tracker lives in `cmd/pyry`, not a new `internal/` package). |
| `cmd/pyry/interactive_turn_v2.go:76-112`, `:140-160` | Why the signal cannot be a tap on the emitter: unguarded single-goroutine lifecycle fields (`:81-86`), scalar `currentState` (`:86`), cursor-resolved `turnConvID` (`:141`, `:84`), and the #1062 follow-active flush (`:149-160`) that **must not** be ported. Read once, then design away from it. |
| `cmd/pyry/streamsup_runner.go:105` | `streamsup.NewParser(sink.sinkFor(cfg.SessionID), …)` — the sink's sole producer, and why the variant set is closed to five today. |
| `cmd/pyry/relay.go:395-410` | `screenSnapshotterOrNil`'s #1101 comment — the existence-oracle posture `Busy`'s signature enforces. |
| `CODING-STYLE.md` § Concurrency, § Testing | "Channels for coordination, mutexes for state"; table-driven, stdlib-only, `t.Parallel()`, `-race`. |

---

## Context

On `interactive_runner: stream-json` the daemon computes turn transitions but keeps
them where no other goroutine can read them: plain fields on the interactive turn
emitter, scalar rather than per-conversation, and populated only for the conversation
the cursor points at. The delivery path needs the opposite shape — a concurrency-safe,
per-conversation, cursor-independent answer to "is a turn running on X?".

This slice ships that primitive **unwired**, the same rollback shape `internal/permbridge`
(#1103) and the `sessions.Runner` seam (#1077) used. No consumer, no wire change, no
delivery change; the follow-on slices that make it safe to consult are #1202 (session
torn down under the conversation) and #1203 (child dies mid-turn, respawns, emits no
`result`).

**The known gap is deliberate and must live in code, not in this spec.** A turn is
closed here only by its `TurnEnd`. Both non-event clears are out of scope, and AC4
requires the tracker's own doc comment to record that and name #1202 + #1203 as its
closers — so a future reader of `stream_turn_busy.go` learns it without finding a
closed ticket.

---

## Design

### One new file, three touched

| File | Change |
|---|---|
| `cmd/pyry/stream_turn_busy.go` | **New.** The tracker: type, constructor, `observe`, `Busy`, `WaitIdle`. |
| `cmd/pyry/stream_turn_drain.go` | `startStreamTurnDrainV2` gains a `busy *turnBusyTracker` parameter and calls `busy.observe` as the **first** statement of the `sink.ch` arm. |
| `cmd/pyry/relay.go` | Construct the tracker in the stream branch (`:702-726`) and pass it to the drain. |

`cmd/pyry/stream_turn_busy_test.go` is new; `cmd/pyry/stream_turn_drain_test.go`'s
7 call sites gain a `nil`. No `main.go` change, no new `relayWiring` field — the
ticket's file budget holds exactly.

### Contract

```go
// cmd/pyry/stream_turn_busy.go — package main

// turnBusyTracker holds the set of conversations with an open turn on the
// stream-json path. Self-synchronised; all methods safe for concurrent use.
type turnBusyTracker struct{ /* mu, busy set, changed chan, resolve, logger */ }

// newTurnBusyTracker panics if resolve == nil (eventring.New precedent);
// a nil logger falls back to slog.Default (newStreamTurnSink precedent).
func newTurnBusyTracker(resolve func(sessionID string) (conversationID string, ok bool), logger *slog.Logger) *turnBusyTracker

// observe is the feed. Called only from the drain goroutine, before the
// active-session gate. A nil receiver is a no-op (see § Nil tolerance).
func (t *turnBusyTracker) observe(sessionID string, ev turnevent.Event)

// Busy reports whether conversationID has an open turn. Unknown, unbound,
// never-seen and empty all report false — see § Existence-oracle discipline.
func (t *turnBusyTracker) Busy(conversationID string) bool

// WaitIdle blocks until conversationID has no open turn, returning nil.
// Returns ctx.Err() if ctx is cancelled first; returns nil immediately when
// already idle.
func (t *turnBusyTracker) WaitIdle(ctx context.Context, conversationID string) error
```

`observe` is unexported and `Busy`/`WaitIdle` are exported-shaped on purpose: the
lowercase feed is internal to this package's stream path, the capitalised pair is
the surface a future consumer will declare as a 2-method interface at its own call
site (CODING-STYLE § Interface Design). **Do not declare that interface here** —
there is no consumer yet.

### Where the signal is derived, and why it must be there

```go
case env := <-sink.ch:
    busy.observe(env.sessionID, env.ev) // BEFORE the gate — AC3's second half
    active, ok := activeSession()
    if !ok || env.sessionID != active {
        logger.Debug(...)               // unchanged not-active drop
        continue
    }
    emitter.Handle(ctx, env.ev)
```

Two lines, and the ordering **is** the contract. Feeding after the gate would make
AC3's cursor-independence half vacuous: a background conversation's events never
reach `Handle`, so a post-gate tracker would report it idle because it never heard
about it. The drain's `sink.ch` arm is also the only place in the pipeline where a
producing identity (`env.sessionID`, tagged at parser construction) and the event
coexist — upstream of the gate, downstream of the fan-in.

Do **not** feed from `sinkFor`'s closure instead. It runs on each child's stdout
forwarder goroutine, so the feed would no longer be the single-writer fan-in the AC
names, and events would be observed in a different order than the drain processes
them.

### Key: conversation, resolved on the write side

`observe` resolves `sessionID → conversationID` via the injected closure
(`conversationForSession(w.convReg, sid)` in production) and keys the set by the
**conversation**. Rejected alternative: keying by session and resolving at read time
— it would need a conversation→session direction that does not exist for an
arbitrary (non-active) conversation, would drag the #678 `Lookup("")` hazard into a
new resolver, and would strand a busy bit under the old session id after a `/clear`
rotation. Write-side resolution inherits `conversationForSession`'s `SessionHistory`
match for free, so a just-rotated session still resolves.

Three rejections, one condition: `!ok || conversationID == ""` → the event is **not
tracked**, Debug-logged once (§ Error handling). The empty-id half is not defensive
padding — an empty key would both wedge and collide with the unknown-conversation
answer, which the ticket names explicitly.

**Resolver cost, stated explicitly** (the ticket asks for this, not an implicit
shrug): `conversationForSession` is an O(conversations) scan of `convReg.List()`,
run once per event. Post-#609 coalescing puts arrivals at ~one per JSONL message /
~250 ms (`stream_turn_drain.go:10-16`), and conversations are a small human-scale
set, so the scan is comfortably within budget on the drain goroutine. If a future
slice raises the arrival rate by an order of magnitude, the fix is a by-session-id
read method on the registry, not a cache here.

### State: a set of busy conversations, not a map of bools

The tracker stores membership only — a conversation is added by an opener and
**deleted** by its `TurnEnd`. Three properties fall out of the data structure rather
than out of a branch:

- Absent key ≡ idle ≡ unknown ≡ unbound ≡ never seen — AC1's existence-oracle answer
  is the same code path for all five, not a branch that could diverge.
- The map is bounded by the number of conversations *currently mid-turn*, not by
  every conversation ever seen. No unbounded growth.
- No value type to get wrong (`false` vs absent).

Never store the event, its fields, a turn id, a timestamp, or a count. The busy bit
and the conversation key are the whole state.

### The opener set is a whitelist

| Variant | Action |
|---|---|
| `ThoughtChunk`, `TextChunk`, `ToolStart`, `ToolUpdate` | add the conversation |
| `TurnEnd` (either reason) | delete the conversation |
| `Stall`, `ApiRetry`, `Compacting`, anything else | **no state change** |

Write it as an explicit type switch over the four openers plus `TurnEnd`, with the
rest falling through to a no-op default. The evidence is the parser's growth path,
not a stray `Stall`: `parser.go:158-163` tolerates-and-drops `rate_limit_event`
today, and that is precisely the line that becomes an `ApiRetry` the day someone
wires it. A blacklist ("anything that isn't `TurnEnd` opens a turn") is behaviourally
identical through today's sink and would wedge a conversation on that first new
variant. `TurnEnd` on a conversation that is not busy is a no-op delete — not a
reject branch, not a log.

### What is deliberately not built

- **No follow-active switch.** The emitter's #1062 mid-turn cursor-switch flush
  (`interactive_turn_v2.go:149-160`) exists because the emitter is cursor-scoped and
  holds exactly one turn. Porting it would close a still-running turn on the
  conversation the cursor left — the exact AC3 failure this slice exists to avoid.
  The tracker holds no cursor reference at all; the constructor takes no
  `cursorReader`. That absence is structural, and § Testing asserts it.
- **No timer, no second parse, no stdin inference.** The tracker's import block is
  exactly `context`, `log/slog`, `sync`, and `internal/turnevent`. Any `time`, `os`,
  `io`, or transcript import is an AC1 violation, and the import block is the
  cheapest place to check it.
- **No transition logging.** No AC asks for it, and the signal is unwired so nothing
  can be debugged through it yet. See § Open questions — the level choice becomes
  load-bearing when #1202/#1203 need it.
- **No persistence.** In-memory only; a daemon restart starts all-idle, which is the
  fail-open direction.

### Nil tolerance — exactly one, and why

`observe` tolerates a nil receiver so the 7 pre-existing drain tests keep their exact
wiring with a one-token `nil` edit each, rather than each constructing a tracker they
do not assert on. `Busy`/`WaitIdle` get **no** nil guard — they have no nil call site,
and shipping a defence for an unobserved failure mode is exactly what this pipeline
forbids.

The parameter's type must stay the concrete `*turnBusyTracker`, never an interface: a
typed-nil pointer in an interface field is non-nil at the interface level and would
route straight past the guard into a nil-map read. This is the same hazard
`screenSnapshotterOrNil` (`relay.go:395-410`) exists to dodge.

### Wiring

In `relay.go`'s stream branch, between the emitter construction (`:719`) and the
drain start (`:726`):

1. Build the resolver closure — copy `:756` verbatim.
2. `busy := newTurnBusyTracker(resolve, logger)`.
3. Pass `busy` to `startStreamTurnDrainV2` (new parameter, positioned after
   `activeSession` and before `logger` — `logger` stays last, house convention).

A local variable is the whole plumbing. It is deliberately **not** hoisted to a
`relayWiring` field or `main.go`: nothing outside this branch may reach it in this
slice, and a field would be the 4th substantially-edited production file the ticket's
budget forbids. The consumer slice that needs it will hoist it then, with a reader in
the same diff.

---

## Concurrency model

No goroutine is spawned. One `sync.Mutex` guards one map and one channel field.

**Writer.** `observe` runs only on the drain goroutine, inheriting the single-writer
invariant `stream_turn_drain.go:103-106` already relies on. The tracker is
self-synchronised anyway, so the invariant is a property of the wiring, not a
correctness requirement of the type — same posture as `eventring.Ring`.

**Readers.** `Busy` and `WaitIdle` may be called from any goroutine.

**Broadcast.** A single generation channel (`chan struct{}`, never sent on) is closed
and replaced whenever — and only when — set membership actually changes. Waiters
re-check their own key after each wakeup, so one channel serves all conversations; a
spurious wakeup costs a map lookup. Waiter count is bounded by in-flight deliveries.

Three invariants the developer must not "clean up":

1. **`WaitIdle` checks membership and captures the generation channel under one lock
   acquisition, then waits outside the lock.** Splitting the check from the capture
   reintroduces the lost-wakeup race — the transition can land in the gap and the
   waiter sleeps on a channel that has already been replaced.
2. **The close-and-replace happens under the lock, together with the map mutation.**
   `close` never blocks, so holding the mutex across it is safe, and it is what makes
   invariant 1 atomic. The instinct to move channel operations outside the lock
   breaks this.
3. **`resolve` is called outside the lock.** It scans the conversations registry and
   takes that registry's mutex; calling it while holding the tracker's mutex would
   establish a `tracker.mu → convReg.mu` order for no benefit. Resolve first, then
   lock, mutate, broadcast, unlock — so the lock is held for a map operation only.

**Lifecycle.** `WaitIdle` always exits: on idle, on the next broadcast (which
re-checks), or on `ctx.Done()`. The tracker owns nothing to close and has no shutdown
step; the drain's cleanup is unchanged.

---

## Error handling

`observe` returns nothing and cannot fail. Its one non-happy path is an unresolvable
producing session (an unbound bootstrap session before any binding, or a session whose
conversation was removed): the event is dropped from tracking and logged once at
**Debug**:

- event: `stream_turn.busy_unresolved`
- fields: `kind` (via `eventKind`, `interactive_turn_v2.go:401`) and `session_id`. Nothing else.

Debug is correct here, and the reasoning is not stylistic: the daemon's default level
is `LevelInfo` (`-pyry-verbose` raises it), this fires once per event on an unbound
session, and it is a diagnostic for a condition no AC observes — matching its two
siblings in the same path, `stream_turn.sink_full` (`stream_turn_drain.go:76`) and
`stream_turn.not_active` (`:130`), both per-event Debug.

`Busy` returns `bool` and cannot fail. `WaitIdle` returns exactly two things: `nil`
(idle) or `ctx.Err()`. No tracker state ever reaches an error value.

**Existence-oracle discipline.** `Busy`'s signature is the enforcement, not a runtime
branch: no `error`, no second `found bool`, no sentinel. A foreign conversation id and
an idle one traverse the identical map lookup, so they are indistinguishable in value
and in timing. Code-review should check the **signature**, not only the behaviour —
a later `(bool, error)` widening would reintroduce the oracle silently.

---

## Testing strategy

Two tiers in one new file, `cmd/pyry/stream_turn_busy_test.go`. Reuse the drain
test's existing doubles (see § Files to read first) — write no new broadcaster,
cursor, or active-session double. The only new helper needed is a map-backed stub
resolver.

### Unit tier — tracker driven directly

The tracker is fed with `observe(sessionID, ev)` calls; no parser, no drain.

- **Opener whitelist (AC4).** Table over all eight `turnevent` variants into a fresh
  tracker whose resolver maps `"sess-a" → convA`. Expect busy after `ThoughtChunk`,
  `TextChunk`, `ToolStart`, `ToolUpdate`; expect **still idle** after `Stall{}`,
  `ApiRetry{}`, `Compacting{}`, and after a lone `TurnEnd{}`. The `Stall` case is the
  point of the table. Its comment must state that this is the **only** tier where the
  whitelist is observable — `Stall` cannot reach the sink (the parser emits five
  variants, `parser.go:157-220`) — and must not imply it is drivable through a parser
  or a live runner.
- **Clear on both stop reasons (AC4).** Open, feed `TurnEnd{Reason: TurnEndReasonEndTurn}`
  → idle. Repeat with `TurnEndReasonCancelled` → idle. Note in the comment that
  `resultTurnEndReason` (`parser.go:173`) sends both through one arm, so this asserts
  one path twice — the AC names both reasons, so assert both anyway.
- **Unknown / unbound / never-seen (AC1).** On a tracker that has seen nothing:
  `Busy("")`, `Busy(<foreign uuid>)`, and `Busy(convA)` all false, and `WaitIdle`
  returns `nil` promptly for each (a bounded-deadline goroutine, not a bare call, so a
  regression to "blocks forever" fails rather than hangs the suite).
- **Unresolvable session is not tracked (AC1).** Resolver returns `ok=false` for
  `"sess-x"`; feed an opener; assert `Busy("")` is false **and** that no conversation
  reports busy (assert `Busy` false for the empty key and for convA) — i.e. nothing
  was tracked under an empty key. Repeat with a resolver returning `("", true)` to pin
  the empty-id half of the guard.
- **Per-conversation independence (AC3, first half).** Open A; `Busy(A)` true,
  `Busy(B)` false. Close A; both false. No cursor exists anywhere in this test — that
  absence is the point.
- **`WaitIdle` blocks, then returns (AC2).** With A busy, start `WaitIdle(ctx, A)` in
  a goroutine writing to a `done` channel; assert it has **not** returned (non-blocking
  receive with `default`); feed `TurnEnd` for A; require `done` within a 2s deadline
  with a `nil` error.
- **`WaitIdle` honours cancellation (AC2).** With A busy, cancel the ctx; require the
  call returns within a deadline and `errors.Is(err, context.Canceled)`.
- **Concurrent readers vs. a driving goroutine (AC2, `-race`).** One goroutine runs N
  open/close cycles across two conversations through `observe`; M reader goroutines
  loop `Busy` plus `WaitIdle` with a short-deadline ctx; join everything and assert
  both conversations end idle. The assertion is `-race` cleanliness and termination,
  not an intermediate value.

### Drain tier — real `startStreamTurnDrainV2`, real `streamsup.Parser`

- **Fed before the gate / cursor-independent (AC3's teeth).** Cursor on conversation B
  and `activeSession` on `"sess-b"`; feed session **A**'s `assistantTextLine` via
  `feedLines(sink, "sess-a", …)`; barrier with `waitDropKind(t, drops, "text_chunk")`
  — the not-active drop is logged *after* `observe`, so observing it proves the feed
  ran. Then assert `Busy(convA)` **true**, `Busy(convB)` false, and `assertNoPush`.
  This one test is what makes AC3 non-vacuous: a tracker fed after the gate fails it,
  and a cursor-derived tracker fails it. Say that in the comment.
- **Open → close through the real parser.** Cursor and active session both on
  `"sess-a"`. Feed only `assistantTextLine`; `collectEnvs(…, 1)` (the `turn_state`
  responding envelope, pushed inside `Handle` and therefore strictly after `observe`)
  → assert `Busy(convA)` true. Then feed `resultLine`; `collectEnvs(…, 3)`
  (`assistant_delta`, `turn_end`, `turn_state` idle — order is invariant even if the
  250 ms timer flushes the delta early) → assert `Busy(convA)` false. Both barriers
  are deterministic; do not use sleeps.

### Structural / unwired assertions (AC5)

- The tracker file's import block is exactly `context`, `log/slog`, `sync`,
  `internal/turnevent` — the machine-checkable form of "no second detector, no timer,
  no stdin inference".
- `grep -rn 'turnBusyTracker\|\.Busy(\|WaitIdle(' cmd/ internal/` outside
  `stream_turn_busy*.go` returns exactly two production hits: the drain parameter and
  the `relay.go` construction. No production caller reads the signal.
- The five stream-mode e2e files are **unedited**, and pass:
  `go test -tags e2e ./internal/e2e/ -run 'TestRelayV2_Stream' -count=1`. Confirm they
  **ran** — `ok` with everything `--- SKIP` is not a pass. `make check` (which includes
  the `e2e` target) is the full gate.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The design deliberately *widens* one boundary: today every
  event crosses `activeSession()` before touching daemon state, and this slice
  mutates state for background sessions too. The mitigation is what crosses. The
  untrusted half — the `turnevent.Event`, parsed from claude's stdout — is reduced to
  an 8-way type discriminant inside `observe` and never stored; the trusted half is
  the daemon's own construction-time `sessionID` tag and a conversation id read from
  the daemon's registry. **The key never comes from the wire or the stream bytes**, so
  a hostile or confused child can only ever mark *its own* conversation busy. The
  boundary is a single named function (`observe`), not scattered. No finding.
- **[Tokens, secrets, credentials]** Not applicable by construction: the tracker
  stores no credential material. Conversation ids are already stamped on every v2
  envelope and session ids are already Debug-logged twice in this same file
  (`stream_turn_drain.go:76`, `:130`), so the one new log field set introduces no
  identifier that is not already there.
- **[File operations]** Not applicable: the tracker performs no filesystem access, and
  the pinned import block (`context`, `log/slog`, `sync`, `turnevent`) makes that
  structurally checkable rather than a promise.
- **[Subprocess / external command execution]** Not applicable: nothing is spawned and
  no value reaches `exec`. The tracker is downstream of the child, never upstream.
- **[Cryptographic primitives]** Not applicable: no randomness and no comparison
  against a secret. Turn-id minting via `crypto/rand` stays in the emitter
  (`interactive_turn_v2.go:255`), untouched.
- **[Network & I/O — resource exhaustion]** Three vectors checked, none exploitable.
  (a) *Map growth* is bounded by conversations *currently mid-turn* because the set
  deletes on clear, and entries can only exist for ids the registry resolved. (b) *Log
  volume*: one Debug line per event from an unbound session, at a level off by default
  (daemon default `LevelInfo`) and at the post-#609 ~250 ms arrival rate — the same
  per-event Debug shape as the two existing drops in this path. (c) *Waiters*: no
  per-waiter allocation (one shared generation channel), and every waiter exits on
  ctx or on the next broadcast.
- **[Error messages, logs, telemetry]** MUST-NOT-log: any `turnevent` field —
  assistant text, thought text, tool title/input/result. MUST-log, Debug only:
  `event`, `kind` (via the content-free `eventKind`), `session_id`. `WaitIdle` returns
  only `ctx.Err()`, so no tracker state can leak through an error value. **Existence
  oracle:** `Busy`'s `bool`-only signature makes a foreign conversation id
  indistinguishable from an idle one in both value and code path (identical map
  lookup) — the #1101 posture `screenSnapshotterOrNil` records. Pinned as a contract
  in § Error handling so code-review checks the signature, not just today's behaviour.
- **[Concurrency]** The category with real content here, and the reason three
  invariants are pinned in § Concurrency model rather than left to taste:
  check-and-subscribe under one lock acquisition (else lost wakeup), close-and-replace
  under the lock with the mutation (else invariant 1 is not atomic), and `resolve`
  called *outside* the lock (else a `tracker.mu → convReg.mu` order is established for
  no benefit). One mutex, never held across a blocking operation; no goroutine spawned,
  so no leak surface; no persistence, so an interrupted process cannot leave a
  recoverable-state problem — a restart is all-idle, the fail-open direction.
- **[Threat model alignment]** The relevant `protocol-mobile.md` threat is
  cross-conversation leakage; the answer is that the tracker holds no content and
  derives its key daemon-side. Two gaps are explicitly **OUT OF SCOPE** and named:
  the permanent-busy wedge via session teardown → **#1202**, and via mid-turn child
  death/respawn → **#1203**; AC4 puts both in the tracker's doc comment. A third,
  smaller edge — after a `/clear` rotation the old session id still resolves to the
  same conversation via `SessionHistory`, so a late `TurnEnd` from the retired session
  can clear a turn opened by its successor — is also **#1202**'s (rotation semantics),
  and fails *open* (reports idle when busy), the same direction as a daemon restart.
  Recorded in § Open questions so #1202 inherits it.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-25

---

## Open questions

1. **Over-clear across a rotation.** `conversationForSession` matches `SessionHistory`,
   so a retired session's late `TurnEnd` clears the conversation even if its successor
   has a turn open. Harmless while unwired and fail-open once consulted, but #1202 owns
   rotation semantics and should decide whether the clear must be session-qualified.
2. **Transition logging for the consumer slices.** Not built here (no AC, nothing to
   debug while unwired). When #1202/#1203 need it, the level is load-bearing, not
   stylistic: a busy/idle transition record at Debug is invisible on a daemon running
   at the default `LevelInfo`, which is exactly the daemon a wedge would be debugged on.
3. **Where the tracker gets hoisted.** The consumer slice needs it reachable from the
   delivery path (`msgqueue` deliver → `deliverViaSession`), which means a
   `relayWiring` field or a `main.go` construction — the `streamSink` precedent
   (`main.go:1027`, `relay.go:245-253`). That hoist belongs in the diff that adds the
   reader, not here.

---

## Scope

Red lines re-checked against this design, counting **total** written work, not
production LOC:

| Red line | Count | Verdict |
|---|---|---|
| New files (> 3) | 2 (`stream_turn_busy.go`, `stream_turn_busy_test.go`) | clear |
| Total written LOC (> ~600) | ~170 tracker (this project's comment density) + ~25 drain + ~8 relay + 7 call-site tokens + ~250 tests ≈ **460** | clear |
| New exported types/interfaces (> 5) | 1 unexported type, 2 exported-shaped methods; **no** consumer interface declared | clear |
| Consumer call sites (> 10) | 8 for `startStreamTurnDrainV2` (`relay.go:726` + 7 in `stream_turn_drain_test.go`), per `codegraph_impact` | clear |
| Acceptance criteria (> 5) | 5 | clear |
| Reject branches in a state machine (≥ 10) | 3 in `observe` (nil receiver, unresolved/empty conversation, non-lifecycle variant) | clear |
| Production source files (≥ 5, pre-commit self-check) | 3 (`stream_turn_busy.go` new, `stream_turn_drain.go`, `relay.go`) | clear |

The 7 mechanical `nil` insertions are counted at face value, not discounted as
boilerplate. No split.
