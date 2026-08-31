# #1917 — Resolve an in-flight tool call to its conversation

**Size:** S · **One production file:** `cmd/pyry/stream_turn_busy.go`

## Files to read first

Read these before writing anything. The whole slice lives inside one type, and that
type's doc comment carries invariants the design must not retire.

- `cmd/pyry/stream_turn_busy.go` → the `turnBusyTracker` type comment — the
  **THREE FEEDS** paragraph (what closes a turn) and the **SECURITY** paragraph
  ("the key is never taken from the wire") are the two claims every change here
  has to keep true.
- `cmd/pyry/stream_turn_busy.go` → `observe` — where capture hooks in, and the
  already-resolved `convID` it holds by the time it applies the mark.
- `cmd/pyry/stream_turn_busy.go` → `setBusy` — the single mutation point this
  slice widens. Its doc states why every feed funnels through one lock
  acquisition; that argument is the load-bearing one below.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — the purity discipline the new
  classifier copies: switch on the Go variant type, never on a field value.
- `cmd/pyry/stream_turn_busy.go` → `Busy` — the signature-is-the-enforcement
  posture the report mirrors, and the reason neither read carries a nil guard.
- `cmd/pyry/stream_turn_busy.go` → `clearForSession`, `openForDelivery` — the two
  non-tool feeds whose call sites gain a zero delta.
- `cmd/pyry/stream_turn_busy_test.go` → `stubBusyResolve`, `requireWaitIdle`,
  `testConvID` / `testConvIDB` usage in
  `TestTurnBusyTracker_PerConversationIndependence` — the two-conversation
  fixture shape to copy, cursor-free by design.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnBusyTracker_ImportsStayMinimal`
  — **pins the production file's import set exactly.** This design adds no
  import; if you find yourself needing one, the design has drifted.
- `cmd/pyry/stream_turn_busy_test.go` → `exitLaneDrain` — the ready-made drain
  fixture (cursor and gate on B, events for A) for the child-death criterion.
- `cmd/pyry/stream_turn_drain_test.go` → `toolUseLine`, `toolRsltLine`,
  `feedLines`, `waitDropKind`, `dropWatcher` — the stream-line fixtures already
  carrying tool-call id `tu-1`, and the handler whose `recs` field forwards whole
  records (what the no-logging criterion asserts against).
- `cmd/pyry/interactive_turn_v2_test.go` → `discardLogger` — where that helper
  actually lives.
- `internal/turnevent/event.go` → `ToolStart`, `ToolUpdate` — the only two
  variants carrying `ToolCallID`.
- `internal/turnevent/taxonomy.go` → `ToolStatus` — the field the classifier
  deliberately does **not** read.
- `internal/streamsup/parser.go` → `emitUser`, `toolStatus` — the sole production
  `ToolUpdate` emission site and the comment recording why it is always terminal.
- `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md`
  — why `setBusy` became the shared mutator (#1202) and why the clear stays
  session-keyed. Same reasoning drives the sweep's placement here.
- `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md`
  — the child-death lane the third criterion rides.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — cite by symbol, never a
  line, no range exemption. `make cite-guard` is diff-scoped and will fail the
  branch otherwise.

## Context

The delivery hold that #1911 needs must separate *waiting on a person* from
*wedged*, and answering that needs a conversation key for a parked approval. The
ticket rules out both obvious candidates: `activeConv()` is the follow-active
cursor, moved by `sessionRouter.Route` at **enqueue**, so a message enqueued for
B while A's turn is parked misattributes every later approval on A to B; and a
conversation id carried on the control-socket approve payload would make the
delivery decision caller-asserted, retiring the `turnBusyTracker` SECURITY
property that the key is resolved daemon-side and a hostile child can only ever
name its own conversation.

The sound key is already in the stream. `ToolStart.ToolCallID` is claude's
`tool_use_id`, it reaches `observe` as an opener per `turnMarkFor`'s whitelist,
and by that point `observe` has already resolved session → conversation
daemon-side. What is missing is **retention**: which tool calls are currently in
flight, per conversation. This slice adds that retention, one membership report
over it, and nothing else. Nothing in this slice reads the report — #1919 is the
consumer.

**No ADR is warranted.** This extends one existing type along the axis its own
doc already describes; there is no alternative worth recording.

**One knowledge doc goes stale and the documentation phase should refresh it.**
`streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md`
states `setBusy`'s signature as `setBusy(conversationID string, open bool)`,
which this slice widens by one parameter. The developer must not touch that file.

## Design

### The retention shape: nested, conversation-keyed

Add one field to `turnBusyTracker`, guarded by the existing `t.mu`:

```go
// inflight holds the tool calls currently in flight, keyed by conversation and
// then by claude's tool_use_id. An outer key is present only while its inner
// set is non-empty, so the map is bounded by conversations with a tool call
// running — not by every conversation ever seen.
inflight map[string]map[string]struct{}
```

Constructed alongside `busy` in `newTurnBusyTracker`. No new import: `map` and
`struct{}` are builtins.

**Why nested and not a flat `map[toolCallID]conversationID`.** The flat shape is
one lookup instead of two and is *not* conversation-blind (it stores the
conversation and the report compares it), so it satisfies the first criterion's
literal wording. It is still wrong, and the reason is cross-conversation:
tool-call ids are minted by each child, so a hostile or confused child on
conversation C can emit a `tool_use` block reusing an id genuinely in flight on
A. Under the flat map that capture **overwrites** A's entry and flips
`ToolCallInFlight(A, id)` to false — C has changed A's answer. Under the nested
map, C's fabrication lands under C's own key and A's entry is untouched; the
worst C achieves is a false positive about itself, which is exactly the bound the
type's SECURITY paragraph already claims ("can only ever mark its OWN
conversation busy"). The nested map is what keeps that sentence true for the new
feed. Sweeping is also O(1) — one `delete` of the outer key — rather than a scan.

**The flat *set* the ticket warns about** — a single `map[toolCallID]struct{}`
with no conversation at all — is a third shape, and it is the one the
two-conversation test exists to kill.

### The classifier: a second pure function beside `turnMarkFor`

```go
// toolCallDelta is one event's effect on the in-flight set. The zero value
// carries no delta, which is what every non-tool variant produces.
type toolCallDelta struct {
	id      string // empty ⇒ no delta
	started bool   // true: went in flight; false: finished
}

// toolCallDeltaFor classifies one event. Pure, and switches on the Go variant
// type only — the single field it reads is the id itself, never a discriminant.
func toolCallDeltaFor(ev turnevent.Event) toolCallDelta
```

- `turnevent.ToolStart` → `{id: ev.ToolCallID, started: true}`
- `turnevent.ToolUpdate` → `{id: ev.ToolCallID, started: false}`
- everything else → the zero value

**`ToolUpdate.Status` is deliberately not read.** The parser emits `ToolUpdate`
from exactly one site (`emitUser`, from a `tool_result` block) and `toolStatus`
maps `is_error` onto completed/failed only — never pending, never in progress —
so a `ToolUpdate` in production is always terminal.
`turnevent.ToolStatusInProgress` has no production producer at all. Reading
`Status` would therefore buy nothing and would break `turnMarkFor`'s stated
discipline that no content claude produced steers the answer. Not reading it also
fails **closed**: a fabricated `tool_result` drops the child's own call early
rather than pinning it in flight.

**Do not fold this into `turnMarkFor`.** That function has a second caller,
`sinkFor`, whose never-drop reserve reads the same value; widening its return
would couple the fan-in's drop policy to tool-call retention for no reason. Two
small pure classifiers, one concern each.

### `setBusy` widens by one parameter, and the ordering inside is load-bearing

```go
// setBusy applies the tool-call delta AND the membership change for
// conversationID under a SINGLE t.mu acquisition, broadcasting only when
// membership actually moved, and reports whether it moved.
func (t *turnBusyTracker) setBusy(conversationID string, open bool, tool toolCallDelta) (changed bool)
```

The body's order is a contract, not an implementation detail:

1. **Apply the tool delta first, before the early return.** `started` with a
   non-empty `id` inserts (creating the inner map on demand); `!started` deletes,
   and deletes the outer key when the inner set empties. An empty `id` is no
   delta at all. Getting this after the early return is the defect to avoid: a
   `ToolStart` mid-turn arrives on an **already-busy** conversation, so
   `open == was` and the existing `return false` fires — a delta applied after it
   would never be captured for any tool call but the first of a turn.
2. Then the existing membership comparison, returning `false` unchanged when
   `open == was`.
3. Then mutate `busy`; **on close (`open == false`), also `delete(t.inflight,
   conversationID)`** — the whole sweep, one statement.
4. Then the existing close-and-replace broadcast.

Step 3 after step 1 makes close win over a same-call add. That pair is
unreachable today (every tool-bearing variant is an opener) and the ordering
makes it fail closed if it ever becomes reachable.

The four existing in-file call sites gain a delta argument: `observe` passes
`toolCallDeltaFor(ev)`; `clearForSession` and both `openForDelivery` marks pass
`toolCallDelta{}`. No call site outside this file exists — confirmed with
`codegraph_impact` on `setBusy` and with a repo-wide grep, which finds only these
four plus one comment reference in `inbound_deliver_rotation_test.go`.

### `observe` gains one line

`observe` already resolves the conversation and already refuses an unresolved or
empty one before marking. Capture rides that same refusal for free — an
unresolvable session captures nothing, so no tool call is ever retained under an
empty conversation key. The only change is passing `toolCallDeltaFor(ev)` into
`setBusy`. No second `resolve` call, no lock-order change; `resolve` stays
outside `t.mu` as its doc requires.

Note the consequence for `turnMarkNone` events: `observe` returns early on them,
so a variant that is neither opener nor closer contributes no delta even if a
future one carried an id. That is correct — a call cannot be in flight on a
conversation with no turn open.

### The report

```go
// ToolCallInFlight reports whether toolCallID is a tool call currently in
// flight on conversationID.
func (t *turnBusyTracker) ToolCallInFlight(conversationID, toolCallID string) bool
```

Two nested map lookups under `t.mu`, no branch. Membership, not lookup — it
answers "does this call belong to this conversation?", never "which conversation
owns it?", mirroring `Busy(conversationID) bool`. `Busy`'s doc is explicit that
**the signature is the existence-oracle enforcement, not a runtime branch**; the
same holds here, doubly, because the pair `(conversationID, toolCallID)` could
otherwise oracle either id. Unknown, never-seen and empty values of **either**
parameter reach `false` through the identical two lookups — an empty
conversation key is never inserted (`observe` refuses it) and an empty tool-call
id is never inserted (step 1 above), so neither needs a guard, and adding one
would be the runtime branch the posture forbids.

**No nil-receiver guard**, matching `Busy` and `WaitIdle` and unlike
`clearForSession` / `openForDelivery` / `waitIdleForDelivery`. The tracker's own
doc records why: the wiring hands the nil only to the methods PTY mode actually
reaches, and this read is not one of them. A guard here would be the first step
toward a consumer that silently reads `false` in PTY mode instead of failing
loudly.

**Exported-style name on an unexported type**, matching `Busy` / `WaitIdle`: this
file exports the consumer-facing reads and keeps the feeds unexported. No new
exported type or interface is introduced.

### Deliberately not in this slice

- **No consumer.** Nothing reads `ToolCallInFlight`. `staticcheck ./...` includes
  tests, so a test-only caller satisfies the unused check — the direct precedent
  is `abb3fc02`, which shipped `Busy` and `WaitIdle` on this same file with no
  production caller and a green gate. Do not wire a consumer in to placate the
  linter.
- **No fake-tier test.** fakeclaude's approve rider mints a `tool_use_id` but
  writes no `tool_use` block, so the parser emits no `ToolStart` and the
  retention can never populate under that harness — an assertion there reports
  negative whether the daemon is right or wrong, which reads as a pass. #1918
  fixes the harness for #1919; this slice is deliberately not blocked on it.
- **No cap on retained ids.** The inner set is bounded by one turn's concurrent
  tool calls and is deleted whole at turn close; there is no observed
  unboundedness to defend against. If #1919 surfaces one, it gets a cap then.

## Concurrency model

No new goroutine, no new channel, no new lock, no change to the lock order.

- **One mutex, one acquisition per mutation.** `inflight` is guarded by the
  existing `t.mu` and is only ever touched inside `setBusy` and
  `ToolCallInFlight`. Widening the existing mutator rather than adding a second
  one is the whole point: **two acquisitions would be a reachable lost update.**
  Concretely — `observe` runs on the drain goroutine while the teardown feed
  runs `clearForSession` on the pool's lifecycle or rotation-watcher goroutine.
  With capture split into its own method, a `/clear` landing between `observe`'s
  mark and its capture sweeps an empty `inflight[A]` and then the capture
  re-inserts, leaving a call reported in flight on a conversation whose session
  is gone — the third criterion's exact failure, and invisible to `-race` since
  both paths hold the mutex.
- **`resolve` stays outside `t.mu`.** Unchanged; the tracker.mu → convReg.mu
  order is never established.
- **The invariant to preserve:** a non-empty `inflight[c]` implies `busy[c]` is
  present. Capture and mark happen in one call for every tool-bearing variant
  (all of which are openers), and every close sweeps. `openForDelivery`'s undo
  relies on it: the undo only fires when that call actually opened the turn, so
  the conversation was idle, so `inflight[c]` was absent and the sweep is a
  no-op — the undo can never discard another feed's retained calls.
- **The drain's exit arm stays inline.** It reaches `clearForSession` on the
  drain goroutine and sweeps synchronously, so the existing
  `TestStreamTurnDrainV2_ExitClearsInlineBeforeTheNextEnvelope` ordering
  guarantee extends to the sweep with no change.

## Error handling

There is nothing to fail: no I/O, no allocation that can error, no new return
value. The two refusal paths are classifications, not errors.

- **Unresolvable producing session** — unchanged. `observe` returns before
  `setBusy`, so nothing is captured. Keeps logging the existing
  `stream_turn.busy_unresolved` line (kind + session id only).
- **Empty tool-call id** — no delta, silently. **Add no log line for it.** The
  fourth criterion bans a tool name, tool input, tool-call id or conversation id
  from any log added here, and the honest way to satisfy that is to add no log
  line at all on the tool-call paths. This is a per-tool-call hot path with no
  operator action attached, so a diagnostic would be volume for nothing;
  `turnMarkFor`'s default arm is the existing precedent for a silent
  classification default.
- **`ToolUpdate` for a call never started, or for a conversation with nothing in
  flight** — a no-op delete, same as `turnMarkFor`'s "TurnEnd on a conversation
  that is not busy is a plain no-op delete".

## Testing strategy

Unit tier, driving `observe` / `clearForSession` with synthetic `turnevent`
values, plus one drain-tier test reusing `exitLaneDrain`. Extend
`cmd/pyry/stream_turn_busy_test.go`; no new helper file. Scenarios, not code:

**Attribution and independence (first criterion).** The load-bearing test — a
conversation-blind flat set passes every other clause.
- Resolver maps `sess-a → testConvID` and `sess-b → testConvIDB`, copying
  `TestTurnBusyTracker_PerConversationIndependence`'s fixture. **Hold no cursor
  reference anywhere in the test** — that absence is the assertion.
- Two `ToolStart`s on A (distinct ids) and one on B, all in flight at once.
- Assert each of A's ids reports true for A **and false for B**, and B's id
  reports true for B and false for A. The negative half is what kills the flat
  set.
- Assert both of A's ids report true simultaneously — kills a single-entry
  "most recent call" retention, which claude's parallel `tool_use` blocks make
  reachable.

**Negatives collapse (second criterion).** One table over
`ToolCallInFlight(conv, tool)` against a tracker with a known call in flight on
A, every row expecting false: unknown conversation + known tool; known
conversation + unknown tool; both unknown; empty conversation + known tool;
known conversation + empty tool; both empty; never-seen-but-resolver-known
conversation + its own tool id. Each row must fail for the same reason it does
today for `Busy` — no branch distinguishes them.

**Drop signals (third criterion).** One table whose rows differ only in the drop
driver applied after a `ToolStart` on A, asserting the id reports false
afterwards while a second, still-running call on B is untouched (so a row cannot
pass by wiping everything):
- `observe("sess-a", turnevent.ToolUpdate{ToolCallID: …})` — the stream says the
  call finished.
- `observe("sess-a", turnevent.TurnEnd{…})` — both stop reasons, since the
  existing suite asserts both.
- `clearForSession("sess-a")` — the teardown feed, driven by a `/clear` rotation
  and by an idle/cap eviction alike (`transitionClearsTurn` is what routes both
  to this one call, already pinned by `TestTurnBusyTracker_ClearForSession`).

**Child death (third criterion, last clause).** Drain tier, reusing
`exitLaneDrain(t)`: `feedLines(sink, "sess-a", toolUseLine)`, barrier on
`waitDropKind(t, drops, "tool_start")`, assert `ToolCallInFlight(testConvID,
"tu-1")` is true — **assert this first, or the negative below passes
vacuously** — then `sink.exitFor("sess-a")()`, then barrier as
`TestStreamTurnDrainV2_ExitClearsOpenTurn` does and assert false. No `result`
line is fed and no transition observer exists, so the clear can only have come
from the exit lane. The barrier string is `"tool_start"` — that is what
`eventKind` returns for a `ToolStart`, and it is the field `dropWatcher` reads.

**Classifier purity.** Table over `toolCallDeltaFor`: `ToolStart` → add,
`ToolUpdate` → drop, and at least `TextChunk`, `TurnEnd` and
`NewPermissionRequest(…)` → zero value. Include one `ToolUpdate` row per
`ToolStatus` value (pending / in_progress / completed / failed) all expecting the
same drop — that is what pins "`Status` is not read" and fails a later
`switch upd.Status` refinement.

**No new log line (fourth criterion).** Construct the tracker over
`slog.New(dropWatcher{recs: recs})` with a **resolvable** session, drive
`ToolStart` → `ToolUpdate` → `ToolCallInFlight`, and assert **zero** records
reached `recs`. Zero rather than substring-scanning: it has teeth against any
added diagnostic, and it cannot go vacuous. Use a resolvable session so the
pre-existing `stream_turn.busy_unresolved` line is not what the test measures.

**Do not touch `TestTurnBusyTracker_ImportsStayMinimal` or
`TestTurnMarkFor_TotalOverEveryVariant`.** Both must stay green unchanged — the
first because this design adds no import, the second because `turnMarkFor` is not
modified. If either needs editing, the design has drifted.

`make check` is the gate. `go test -race ./cmd/pyry/...` while iterating.

## Open questions

1. **Does `turnevent.ToolStart` ever carry an empty `ToolCallID` in production?**
   Not resolved here and deliberately not depended on — step 1's empty-id refusal
   makes the answer irrelevant to correctness, and the second criterion demands
   the empty answer be negative however it arises. If the developer finds the
   parser already guarantees non-empty, keep the refusal anyway and say so in the
   comment.
2. **Should the report gate on `busy[conversationID]` as well?** Deliberately
   not: it is redundant under the stated invariant and would add a second lookup
   and a branch for no behaviour change. Named here so a reviewer sees it was
   considered rather than missed.
3. **Whether #1919 wants a "list what is in flight on this conversation"
   enumeration** rather than the membership question. Out of scope; the
   membership shape is what the second criterion's posture requires, and an
   enumeration would hand back exactly the ids the oracle discipline withholds.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST FIX — fixed in the design above.** The retained key
  is claude-derived: `ToolStart.ToolCallID` is a child-supplied `tool_use_id`,
  crossing subprocess-stdout → parent state at `streamsup.Parser`. The first
  draft's flat `map[toolCallID]conversationID` let a child on conversation C
  capture an id in flight on A and **overwrite** A's entry, flipping
  `ToolCallInFlight(A, id)` to false and letting C change another conversation's
  answer — a cross-conversation write through a key the child controls. The
  nested `map[conversationID]map[toolCallID]struct{}` confines every capture to
  the conversation `observe` resolved daemon-side, so the worst a hostile child
  achieves is a false positive about itself. That is the bound the type's
  existing SECURITY paragraph already claims; the flat shape would have retired
  it silently. The boundary stays explicit and single: `observe`'s existing
  `resolve` + non-empty check, which capture rides rather than bypasses.
- **[Trust boundaries] No further findings.** No conversation id enters from the
  wire. The ticket's rejected alternative — carrying a conversation id on the
  control-socket approve payload — is not built, so the key remains
  registry-resolved against the runner's construction-time session tag.
- **[Error messages, logs, telemetry] No findings, by construction.** Zero log
  lines are added on any tool-call path, so no tool name, tool input, tool-call
  id or conversation id can appear. The pre-existing
  `stream_turn.busy_unresolved` and `stream_turn.clear_unresolved` lines are
  untouched and already carry kind + session id only. The fourth criterion's test
  asserts zero records rather than scanning for forbidden substrings, so a future
  diagnostic reddens it regardless of what it names. `RawInput` and `Title` from
  `ToolStart` are never read by this slice at all.
- **[Error messages, logs, telemetry] SHOULD FIX — for code review, not a gate.**
  `ToolCallInFlight`'s `bool` return is the anti-oracle enforcement. A later
  widening to `(bool, error)`, or any variant returning the conversation id,
  reintroduces the oracle silently — `Busy`'s doc says exactly this. The report's
  doc comment must carry that sentence so the next reader is warned.
- **[Concurrency] MUST FIX — fixed in the design above.** Splitting capture into
  a second method is a reachable lost update: `observe` on the drain goroutine
  versus `clearForSession` on the pool's lifecycle goroutine, with a `/clear`
  landing between the mark and the capture, leaves a call reported in flight on a
  torn-down session. It is invisible to `-race` because both paths hold `t.mu`.
  Widening the single `setBusy` acquisition is what closes it; the capture-before-
  early-return ordering in step 1 is what makes it correct for the common
  mid-turn `ToolStart`.
- **[Concurrency] No further findings.** No new goroutine, channel or mutex; no
  change to lock ordering; `resolve` stays outside `t.mu`. Shutdown is unaffected
  — the map is in-memory with no persistence and no partial state to recover.
- **[Network & I/O] Resource exhaustion — considered, no finding.** The retained
  set is bounded twice over: the outer key exists only while its inner set is
  non-empty, and the whole entry is deleted at turn close by every one of the
  three close feeds. A child cannot grow it without bound because each fabricated
  id costs a stream line and the entry dies with the turn. No cap is added
  because no unboundedness has been observed; if #1919 surfaces one it gets a cap
  then, with evidence.
- **[Subprocess / external command execution] Not applicable.** Nothing here
  spawns a process or passes a value to `exec.Command`. The tool name and input
  the child sends are never read.
- **[File operations] Not applicable.** No path is constructed, opened or
  written. The retention is in-memory only.
- **[Tokens, secrets, credentials] Not applicable.** No token is minted, stored
  or compared. `tool_use_id` is a correlation handle, not a capability: holding
  one grants nothing, and the report requires the matching daemon-resolved
  conversation to answer true at all.
- **[Cryptographic primitives] Not applicable.** No randomness, no comparison
  against a secret. The map lookups compare non-secret correlation ids, so
  constant-time comparison is not indicated.
- **[Threat model alignment] No findings.** The relevant threat is a hostile or
  confused child steering a delivery decision, which
  `protocol-mobile.md` § Security model and this file's own SECURITY paragraph
  both bound to "its OWN conversation". The design preserves that bound; the
  first finding is where it was nearly lost. The consumer-side threat — what
  #1919 does with a positive answer — is out of scope here and belongs to #1919.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-31
