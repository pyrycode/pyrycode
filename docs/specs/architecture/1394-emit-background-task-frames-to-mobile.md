# #1394 — Emit the background-task frames to a mobile client

**Size: S** (PO sized `s`; not overridden). Deterministic red-line counts, measured at HEAD `ef7ba1f`:

| Red line | Threshold | This ticket |
|---|---|---|
| New files | > 3 | **0** — every change edits an existing file |
| Total written LOC (prod + tests + helpers + per-branch logs + doc) | > ~600 | **~460 projected** |
| New exported types / interfaces | > 5 | **0** |
| Consumer call sites needing simultaneous update | > 10 | **0** — `MapEvent`'s signature is unchanged; every insertion is an additive `switch` arm |
| Acceptance criteria | > 5 | **5** |
| Distinct error/reject branches in a state machine | > ~10 | **0** |

Production source files this spec prescribes new content for: **2** (`internal/turnbridge/outbound.go`, `cmd/pyry/interactive_turn_v2.go`).

Sizing anchor is the direct predecessor rather than the ticket's cited analogue. #1393's developer commit `fe03b66` landed **550 insertions across 9 files** for the protocol-shape half of this same family and shipped within budget; #1394 is the adjacent layer with a strictly smaller surface (no new types, no testdata fixtures, no `codes.go` registration — all of that is already done) plus a prose doc section. `a5f7bdb` (#1074) remains a useful floor for the *mapping half* specifically: `outbound.go` +19 / `interactive_turn_v2.go` +19 / `outbound_test.go` +24 / `interactive_turn_v2_test.go` +153 for two scalar events through exactly this insertion set.

**File-overlap check: clean.** `git fetch origin --prune` then a scan of every `origin/feature/<N>` branch against this ticket's five files (`internal/turnbridge/outbound.go`, `internal/turnbridge/outbound_test.go`, `cmd/pyry/interactive_turn_v2.go`, `cmd/pyry/interactive_turn_v2_test.go`, `docs/protocol-mobile.md`) found no in-flight branch touching any of them. No `blockedBy` set.

---

## Files to read first

Turn-1 reading list. Line ranges are at `ef7ba1f`; if a range has drifted, the symbol name is authoritative.

**The two insertion sites**

- `internal/turnbridge/outbound.go:62-133` — `MapEvent`'s switch. Insertion site #1. Read the whole function; the contract is in the doc comment at `:44-61` (pure, no I/O, no clock, no sealing, consumer owns the envelope).
- `internal/turnbridge/outbound.go:93-128` — the `Stall` / `ApiRetry` / `Compacting` / `Unrecognized` arms. **This is the shape to mirror**: conversation identity only, `tc.TurnID` and `tc.Seq` deliberately ignored, and each arm's comment says *why* it is not turn-scoped. The `Unrecognized` arm at `:115-128` is the closest template — it also carries a producer-truncated string and re-caps nothing.
- `cmd/pyry/interactive_turn_v2.go:228-262` — the `Stall`, `ApiRetry, Compacting` and `Unrecognized` handler arms, ending at the `default:` at `:257`. Insertion site #2. The `ApiRetry, Compacting` arm at `:229-240` is the exact two-line body (`flushDelta` then `emitMapped`) the new arm takes.
- `cmd/pyry/interactive_turn_v2.go:416-443` — `eventKind`. Insertion site #3. Read the `Unrecognized` arm's comment at `:437-440`: it states the rule AC 4 enforces (variant name only, never the event's claude-derived field).

**Contracts the mapping must satisfy exactly**

- `internal/protocol/interactive.go:149-305` — the three payload structs, `BackgroundTask` row, and `BackgroundTaskRosterPayload.MarshalJSON`. Field names, JSON tags, and the nil→`[]` normalisation. `:248-280` (the marshaller's doc comment) explains why AC 3's assertion must be on produced bytes.
- `internal/turnevent/event.go:77-232` and `:234-289` — `BackgroundTaskStarted` (`:106`), `BackgroundTaskUpdated` (`:161`), `BackgroundTask` (`:206`), `BackgroundTaskRoster` (`:269`). Source fields for the mapping. Note `BackgroundTaskRoster` has **no** `TruncatedFields`; `DroppedTasks` is its only truncation report.

**Test templates to reuse — do not invent new harness**

- `cmd/pyry/interactive_turn_v2_test.go:1238-1355` — the `Unrecognized` trio: `…FansOutToInteractiveOnly`, `…NoLifecycleMutation`, `…FlushesPendingDeltaFirst`. The closest three-test template for AC 1 + AC 2.
- `cmd/pyry/interactive_turn_v2_test.go:705-739` — `…StatusPeersNoLifecycleMutation`: how two peers share one lifecycle assertion via `pushTypes`. Reuse this shape for all three background events in one test rather than three.
- `cmd/pyry/interactive_turn_v2_test.go:431-481` — `…NoAppOutputLogLeak`, the AC 4 template. Note the `pushErr: map[string]error{"a": relay.ErrConnNotFound}` trick that forces the log-heavy push-error branch, and the `if logs == ""` guard that stops the test passing vacuously.
- `cmd/pyry/interactive_turn_v2_test.go:95` — `pushTypes` helper; also `stubCursor`, `fakeInteractiveBcast`, `discardLogger`, `testConvID` in the same file's preamble.
- `internal/turnbridge/outbound_test.go:14-253` — `TestMapEventOutbound`'s table (`name` / `ev` / `tc` / `wantTyp` / `wantPayload` / `wantOK`). Add rows here; it compares payloads by value.

**Doc half**

- `docs/protocol-mobile.md:516-521` — § *Interactive events (v2, capability-gated)* opener. **Count literal #1 is on line 518.**
- `docs/protocol-mobile.md:576-612` — `api_retry` + `compacting`. The fidelity bar: field table, then prose that gives the frame its meaning and states what it is *not*.
- `docs/protocol-mobile.md:613-687` — `unrecognized_message`. The longest peer; shows how a security caveat about claude-derived text is written into a frame's prose.
- `docs/protocol-mobile.md:688-690` — `session_transition`. **Count literal #2 is on line 690**, and the sentence is also the insertion boundary (see § *The doc half* below).

**Evidence for the two "do not edit" instructions**

- `cmd/pyry/stream_turn_busy.go:160-172` — `observe`'s `default:` arm, which `return`s before `resolve`/`setBusy`. Confirms the three variants already open and close no turn there by construction. **Verified at HEAD.**
- `internal/protocol/codes.go:256-258` and `cmd/pyry/relay_guard_test.go:139-141` — the three types are registered and already classed `"push"`. No edit owed.
- `internal/streamsup/parser.go:47,66,106,155,177` — `maxTaskFieldID` 256, `maxTaskDescription` 4096, `maxTaskPatch` 4096, `maxTaskRosterEntries` 8, `maxTaskRosterDescription` 512. Read only to confirm the caps exist upstream; **this ticket adds no truncation and owes no cap test.**

---

## Context

`internal/streamsup/parser.go` already translates claude's `system/task_started`, `system/task_updated` and `system/background_tasks_changed` lines into `turnevent.BackgroundTaskStarted`, `BackgroundTaskUpdated` and `BackgroundTaskRoster` (#1380, #1382, #1381). #1393 gave all three their v2 protocol shape — payload structs, type constants, testdata fixtures, compat registration.

The events therefore exist and the wire types exist, but nothing joins them: `turnbridge.MapEvent` returns `ok == false` for any variant it does not name, so all three fall into its `default:` and are dropped with a debug log. A phone watching a conversation sees `turn_end`/`idle` and has no way to distinguish a genuine finish from #1240's symptom — a turn that reported itself done while a command claude started is still alive.

This ticket is the join, plus the client-facing documentation. It is the *mobile* ticket because that route already has both lanes wired: `Unrecognized` maps to `protocol.TypeUnrecognizedMessage` and is handled at `interactive_turn_v2.go:240` and `:435`. The ACP/desktop route has neither, which is #1262.

Three properties constrain the design and are worth restating because each is easy to violate while doing something that looks reasonable:

1. **Keep translating.** The daemon is the one place a claude vocabulary change lands. The event layer already did that translation; passing claude's own message names through would undo it and let one claude release break every client at once.
2. **An empty roster is a signal, not an absence.** The parser emits `BackgroundTaskRoster` with no entries rather than dropping it, because "nothing is alive" is precisely the reassurance #1240's symptom needs. Suppressing it anywhere on the way to the wire deletes the payoff of the whole feature.
3. **Carry what the events carry.** The roster is a snapshot, not a delta. A task's disappearance from a later roster is the *available* finish signal, but that transition has never been observed and the daemon synthesises no terminal event. Diffing successive rosters is a legitimate thing for a *consumer* to do on its own terms — it is not the daemon's inference to make, and the doc must not present it as one.

---

## Design

Three additive insertion points across two production files. No new types, no new functions, no signature changes, no state.

### 1. `internal/turnbridge/outbound.go` — three `MapEvent` arms

Insert after the `turnevent.Unrecognized` arm (`:128`), before `default:`. Each arm follows the established status-peer posture: **conversation identity only; `tc.TurnID` and `tc.Seq` are ignored** because a background task's lifecycle is orthogonal to the turn that spawned it.

The mapping is 1:1 and total. Nothing is derived, summarised, re-capped, or invented; `ConversationID` is the bridge's only addition.

| Event field | → Payload field | Notes |
|---|---|---|
| **`BackgroundTaskStarted` → `protocol.TypeBackgroundTaskStarted` / `BackgroundTaskStartedPayload`** |||
| — | `ConversationID` | `tc.ConversationID` |
| `TaskID` | `TaskID` | verbatim |
| `ToolCallID` | `ToolCallID` | verbatim |
| `Description` | `Description` | verbatim — **do not** route through `truncate`/`inputSummary` |
| `TaskType` | `TaskType` | verbatim |
| `TruncatedFields` | `TruncatedFields` | slice header copied; nil stays nil |
| **`BackgroundTaskUpdated` → `protocol.TypeBackgroundTaskUpdated` / `BackgroundTaskUpdatedPayload`** |||
| — | `ConversationID` | `tc.ConversationID` |
| `TaskID` | `TaskID` | verbatim |
| `Patch` | `Patch` | verbatim, whole, unparsed |
| `TruncatedFields` | `TruncatedFields` | nil stays nil |
| **`BackgroundTaskRoster` → `protocol.TypeBackgroundTaskRoster` / `BackgroundTaskRosterPayload`** |||
| — | `ConversationID` | `tc.ConversationID` |
| `Tasks` | `Tasks` | element-wise loop, see below |
| `DroppedTasks` | `DroppedTasks` | **the field most easily lost** |
| **`turnevent.BackgroundTask` → `protocol.BackgroundTask` (roster row)** |||
| `TaskID` / `TaskType` / `Description` / `TruncatedFields` | same names | verbatim |

Two failure modes to design against explicitly:

- **`DroppedTasks` is a truncation report that is not called `truncated_fields`,** and the roster payload deliberately has no top-level `truncated_fields` for a grep to land on. Dropping it tells a phone that a capped roster is the whole roster. It is pinned by a test row (see § Testing).
- **The roster loop must not allocate for an empty roster.** `turnevent.BackgroundTaskRoster.Tasks` is nil both for a genuinely empty roster and when claude omits the key. Passing the nil straight through is correct and intended — `BackgroundTaskRosterPayload.MarshalJSON` substitutes `[]` (`internal/protocol/interactive.go:274-280`). A `make([]protocol.BackgroundTask, 0, …)` before a length check would still produce `[]` on the wire, so it is not *wrong*, but it hides the normalisation the marshaller owns and makes AC 3's assertion pass for the wrong reason. Prefer the nil-preserving shape:

```go
case turnevent.BackgroundTaskRoster:
    var tasks []protocol.BackgroundTask
    for _, t := range e.Tasks {
        tasks = append(tasks, protocol.BackgroundTask{ /* four fields, verbatim */ })
    }
    return protocol.TypeBackgroundTaskRoster, protocol.BackgroundTaskRosterPayload{
        ConversationID: tc.ConversationID,
        Tasks:          tasks,
        DroppedTasks:   e.DroppedTasks,
    }, true
```

Each arm carries a comment in the file's established register, stating (a) that it is not turn-scoped so `tc.TurnID`/`tc.Seq` are ignored, and (b) for the roster, that a nil `Tasks` is forwarded rather than filtered and why. Update `MapEvent`'s doc comment at `:53-56` only if the "ok is false for…" sentence becomes misleading — it enumerates the *dropping* set, which these additions do not change.

### 2. `cmd/pyry/interactive_turn_v2.go` — one handler arm

Insert one `case` listing all three variants, between the `Unrecognized` arm (`:240-256`) and `default:`. All three take the identical two-line body the status peers take:

```go
case turnevent.BackgroundTaskStarted, turnevent.BackgroundTaskUpdated, turnevent.BackgroundTaskRoster:
    e.flushDelta(ctx)
    e.emitMapped(ctx, convID, ev)
```

That body is the whole of AC 2: no `startTurnIfNeeded`, no `transitionTo`, no `endTurn`, so `inTurn`, `turnID` and `currentState` are untouched, and `flushDelta` first keeps buffered text ahead of the frame on the wire. `emitMapped` injects `convID` from the cursor into `TurnContext`, which is where the frames' conversation identity comes from — **not** claude's `session_id`, which no `turnevent` variant in this family carries.

The arm's comment should say why these are *not* turn openers, in the register of the `Unrecognized` arm above it: a background task outlives the turn that spawned it, which is #1240's entire point, so opening a turn on one would wedge the conversation exactly as opening one on an unrecognized message would.

Note the switch binds `v := ev.(type)` for `TextChunk`'s field access; a multi-type `case` leaves `v` as `turnevent.Event`, which is fine — the arm passes `ev` on and reads no field. Do not restructure the switch.

### 3. `cmd/pyry/interactive_turn_v2.go` — three `eventKind` arms

Three one-line arms returning `"background_task_started"`, `"background_task_updated"`, `"background_task_roster"`. **Variant name only** — `TaskID`, `Description`, `TaskType` and `Patch` are all claude-derived and this function feeds log fields (AC 4). One comment on the group restating the rule the `Unrecognized` arm at `:437-440` already states is enough; three copies is noise.

`eventKind` is shared, not local: seven production call sites across four files (`interactive_turn_v2.go:145,259,347`, `stream_turn_busy.go:192`, `stream_turn_drain.go:96,231`, `acp_turn_stream.go:102,114` — **verified at HEAD**). Today these three events log `kind=unknown` on the busy, drain and ACP lanes as well as the mobile one; the arms fix all four. That is **not** desktop wiring — `eventKind` produces a log discriminant and never a client-visible frame.

### Explicitly out of scope — four files that must stay untouched

- **`cmd/pyry/stream_turn_busy.go`.** `observe` classifies turn openers with a whitelist and `return`s from `default:` before `resolve`/`setBusy` (`:160-172`, verified). The three variants already open and close no turn there by construction. **Adding them to the opener set would wedge a conversation on a background task** — the exact failure the whitelist exists to prevent.
- **`internal/acpbridge/outbound.go`.** Its `default:` arm (`:175`) means a desktop client silently receives nothing. That is the correct outcome here and the explicit subject of #1262. Do not extend the ACP switch.
- **`cmd/pyry/relay_guard_test.go`.** #1393 already registered all three as `"push"` (`:139-141`), so `TestEveryInboundV2TypeHasHandler` excludes them, and they are correctly absent from `inboundAppTypeSet` in `internal/protocol/envelope.go`. Both stay.
- **`internal/turnbridge/mapper.go`.** It maps the *tuidriver/PTY* path into `turnevent`. These events arrive from `internal/streamsup/parser.go`, which already produces them. The #1074 analogue touched `mapper.go` only because its events *were* PTY-derived.

Also not owed: no new `internal/protocol/testdata/*.json` (all four fixtures landed in #1393), no `codes.go` change, no `compat_test.go` change, no truncation code or cap test — every claude-derived string was bounded by the producer at construction, so a test asserting a field is under its cap would pass without this ticket existing.

---

## The doc half — `docs/protocol-mobile.md`

Three new `####` sections at peer fidelity (field table + prose that gives the frame its meaning), plus a count correction.

**Insertion point: between `unrecognized_message` (ends ~`:687`) and `#### session_transition` (`:688`).** This placement is load-bearing, not cosmetic — see the recount below.

**The recount — the answer is `twelve`, at two literals, not one.** The ticket's Technical Notes flags one stale count and supplies "thirteen". Recounting at HEAD as instructed, both parts need correcting:

- There are **two** count literals, not one. Line **518** ("These eight envelope types form the structured live-session stream") and line **690** ("distinct from the **eight** turn-stream events above", inside `session_transition`).
- They count the **same set**, and `session_transition` is **excluded from it** — by line 690's own words, which say `session_transition` "does not belong to the structured live-session stream and carries no `event_id`". Git confirms the two were introduced and maintained in lockstep: at `5f93830` (the commit that added `session_transition`) both literals read "six", with exactly six stream frames above it and `session_transition` as the excluded seventh `####`.
- The set at HEAD is therefore `turn_state`, `assistant_delta`, `tool_use`, `tool_result`, `turn_end`, `stall`, `api_retry`, `compacting`, `unrecognized_message` = **9**. Both literals say eight; both are stale by one. (The eleventh `####`, "Reconnect replay & resync", is prose, not an envelope type, and never counted.)
- 9 + 3 = **twelve**. Update **both** line 518 and line 690.

"Thirteen" comes from counting all ten `####` headings including `session_transition`, which contradicts the doc's own exclusion of it. Re-verify both numbers at implementation time rather than trusting this paragraph — another frame landing first makes it stale, and the same drift that produced the current error is what this instruction exists to catch.

Placing the three new sections *before* `session_transition` is what keeps both literals counting the same set. Placing them after would make 518 read twelve and 690 read nine, re-opening the drift.

**Section content.** Each section gets a field table matching the `#1393` payload's JSON tags exactly, then prose. What the prose must establish, beyond the table:

- **`background_task_started`** — announces work that outlives the turn that spawned it; that is why there is no `turn_id` and why the frame opens and closes no turn. `tool_call_id` is claude's `tool_use_id` under the name `tool_use` / `tool_result` already use, so a client joins all three with no vocabulary lookup. `truncated_fields` is load-bearing, not decoration: a client that ignores it presents claude's cut text as complete. **SECURITY:** `description` is, for the `local_bash` task type, the literal command line — safe to render as inert text, never to execute, re-shell, or feed to an HTML sink, an attribute, or a URL.
- **`background_task_updated`** — the peer: `background_task_started` opens the task, this reports what happened to it afterwards; `task_id` is the join key. **SECURITY:** `patch` is claude's patch object carried whole and unparsed as a **string**, not nested JSON — the daemon truncates it at construction and a truncated object is no longer valid JSON, so **a client must not assume it parses**. Same render-never-execute rule, stated rather than delegated: a patch's structured shape makes it the more tempting thing to feed somewhere that runs it.
- **`background_task_roster`** — a **snapshot, not a delta**. State the three things a client will otherwise get wrong: (a) `tasks` is always present and never `null`; **an empty `[]` is a positive statement that nothing is alive**, which is the signal #1240's symptom needs, and it is forwarded rather than filtered; (b) `dropped_tasks` is the roster's *only* truncation report — there is no top-level `truncated_fields` — and the roster's true size is `len(tasks) + dropped_tasks`; (c) **the daemon emits no terminal/finish event, deliberately.** A task's disappearance from a later roster is the available finish signal, but that transition has never been observed, so the daemon does not report a finish it cannot detect. Diffing successive snapshots is a legitimate thing for a client to do *on its own terms*; the doc must present it that way and must not present it as a daemon-provided finish. **SECURITY:** each row's `description` carries the same literal command line under a tighter cap; the warning is repeated per-row rather than delegated because a *list* of command lines is a more tempting shape to feed somewhere structured than a single one.

All three sections state that the frames are capability-gated (`interactive`), binary → phone only, and carry `event_id` like their stream peers.

---

## Concurrency model

Unchanged, and nothing here introduces any. `MapEvent` stays a pure value-to-value adapter — no I/O, no state, no clock, no sealing. `Handle` remains single-goroutine (the producer's `Run` goroutine); the new arm reads and writes no emitter state at all, which is the point of AC 2. Frames reach the wire through the existing `emitMapped` → `emit` path, so ring append, `event_id` assignment, per-conn fan-out and sealing are all inherited unchanged.

One inherited property worth naming because a burst is plausible here: these frames flow through `emit()` and are **not** droppable deltas — the droppable set is `assistant_delta` only (#610). A roster storm therefore holds queue slots rather than being silently discarded. That is the same accepted trade-off `Unrecognized` documents, and it is the right one: the producer already caps roster entries at 8 and the events only fire on claude's own lifecycle lines.

---

## Error handling

No new failure modes.

- `MapEvent` cannot fail; it returns `(typ, payload, true)` unconditionally for all three.
- `emitMapped`'s `ok == false` branch stays defensive and becomes unreachable for these three, exactly as it is for the existing mapped variants. Its comment at `:335-338` enumerates the reachable-drop set (`ThoughtChunk` and nil) — extend the "unreachable for" list if the developer judges the comment misleading otherwise.
- The empty-cursor drop at `:141-147` applies unchanged: a background-task event arriving with no conversation cursor is dropped with a `kind=…` debug log and no push. This is the one path where `eventKind` is reached for these variants on the mobile lane, and it is what makes the AC 4 arm live code here rather than only on the busy/drain/ACP lanes.
- Marshal failure in `emit` stays defensive; the three payloads are closed string/int/slice structs.

---

## Testing strategy

Scenarios, not test code. Use the existing harness (`stubCursor`, `fakeInteractiveBcast`, `pushTypes`, `discardLogger`, `testConvID`) and the table in `TestMapEventOutbound`.

### `internal/turnbridge/outbound_test.go`

Rows on the existing `TestMapEventOutbound` table:

- `BackgroundTaskStarted` → `background_task_started`, every field populated with a distinct value including a two-element `TruncatedFields`, expecting the payload with `ConversationID` from `tc` and `TurnID`/`Seq` **absent from the payload struct entirely** (they are not fields — this is what pins "not turn-scoped").
- `BackgroundTaskUpdated` → `background_task_updated`, with a non-empty `Patch` and populated `TruncatedFields`.
- `BackgroundTaskRoster` with two entries **and a non-zero `DroppedTasks`** → `background_task_roster`. The non-zero `DroppedTasks` is the row that fails if the field is dropped; a roster row with `DroppedTasks: 0` would pass against a mapping that never reads it, so at least one row must carry a non-zero value.
- `BackgroundTaskRoster` with per-entry `TruncatedFields` populated on one entry and nil on the other — pins that the row's report rides the row rather than being hoisted or flattened.
- `BackgroundTaskUpdated{}` zero value → maps with `ok == true` and empty strings. Pins that empty is forwarded, not treated as absent.

Separate test for AC 3's byte assertion:

- Call `MapEvent(turnevent.BackgroundTaskRoster{}, tc)` — the genuinely empty roster with a nil `Tasks`. Assert `ok == true` (**non-suppression**, AC 3's "not because the mapping suppresses one"), then `json.Marshal` the returned payload and assert the bytes contain `"tasks":[]`. The assertion must run on the marshalled output of the value `MapEvent` returned — never on a `BackgroundTaskRosterPayload` the test constructed, which would prove the #1393 marshaller works rather than that the mapping reached it.
- Same test, second case: a roster with one entry, asserting the bytes carry that entry — so the empty case is not passing because the marshaller emits `[]` for everything.

### `cmd/pyry/interactive_turn_v2_test.go`

Modelled on the `Unrecognized` trio at `:1238-1355`:

- **Fan-out, one test per variant** (or one table over the three): each event, handled against a cursor with one interactive and one non-interactive conn, produces exactly one push, to the interactive conn only, with the expected envelope type; decode the payload and assert every field round-trips, including `truncated_fields` and — for the roster — `dropped_tasks` non-zero and the entries in order.
- **No lifecycle mutation**, one test covering all three (mirror `…StatusPeersNoLifecycleMutation` at `:705`): handle the three events bare, assert `pushTypes` is exactly the three background types with **no `turn_state` and no `turn_end`**, then handle a `TextChunk` and assert it opens a fresh turn — proving `inTurn`/`turnID`/`currentState` were untouched.
- **Flush ordering**: `TextChunk` then a background event, asserting `pushTypes` is `[assistant_delta, background_task_*]` — buffered text keeps its wire position ahead of the frame.
- **Mid-turn non-disturbance** (mirror `…StallMidTurnDoesNotDisturbOpenTurn` at `:570`): open a turn, interleave a roster, complete the turn, and assert the turn's `turn_id` is unchanged across the interleave and that exactly one `turn_end` is emitted.
- **AC 3 (a) — an ordinary turn gains no traffic**: drive a normal turn (`TextChunk` → `ToolStart` → `ToolUpdate` → `TurnEnd`) and assert `pushTypes` contains none of the three background types. Pair it with the non-suppression assertion above so the two together say "absent because unproduced", not "absent because filtered".
- **AC 3 (b) at the wire**: handle a bare `turnevent.BackgroundTaskRoster{}` and assert the recorded push's `env.Payload` bytes contain `"tasks":[]`. This is the end-to-end form of the `turnbridge` assertion and is worth having at both layers, since it is the property most easily broken by a well-meaning `make(...)` in the loop.
- **AC 4 — log leak**: extend `…NoAppOutputLogLeak` (or add a sibling) with the three events, each carrying a distinct `SECRET…ZZZ` marker in **every** claude-derived string — `TaskID`, `ToolCallID`, `Description`, `TaskType`, `Patch`, and both the roster's entry `TaskID`/`TaskType`/`Description` — under the existing `pushErr` setup that forces the log-heavy branch. Keep the `if logs == ""` vacuity guard. Assert no marker appears in the captured logs.
- **AC 4 — `eventKind` is live and content-free**: handle each of the three events against an **empty cursor** (the `no_cursor` drop at `:141-147` is the reachable `eventKind` call site for these variants on this lane), capture the debug log, and assert it carries `kind=background_task_started` / `_updated` / `_roster` — **not** `kind=unknown` — and carries no secret marker. The positive `kind=` assertion is what pins the arms; a leak-only assertion would pass against a missing arm.

### Gate

`make check` (vet + `-race` unit tests + staticcheck + substrate-guard + fake-claude e2e). No new tier. No realclaude run is owed: the events' capture-backed fixtures already live in the parser's tests from #1380/#1381/#1382, and this layer's contract is value-to-value.

---

## Open questions

- **Frame ordering within a turn is claude's, not ours.** `BackgroundTaskRoster` can arrive before or after the `background_task_started` for a task it lists — the parser emits in stream order and this layer preserves it. Nothing here needs to change if that proves confusing on a phone; it would be a consumer-side reconciliation and the doc already tells a client to join on `task_id`. Flagged only so a reviewer does not read the absence of ordering logic as an oversight.
- **`docs/knowledge/features/protocol-package.md`** was extended by #1393's documentation phase. Whether the mobile-wire join warrants a paragraph there is the documentation phase's call after this PR merges — **not a developer deliverable**, and not an AC.
- **#1262 (ACP/desktop)** will need the same three arms in `internal/acpbridge/outbound.go` plus a client-visible lane. Nothing in this design forecloses it; the `eventKind` arms added here are already shared with that lane's logs.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — but the boundary's location matters and the spec now states it.** The untrusted→bounded transition happens **upstream**, in `internal/streamsup/parser.go`, which caps every claude-derived string at construction and scrubs invalid UTF-8. This ticket's layer adds no boundary and deliberately re-caps nothing. The consequence a reviewer must hold: these values are **bounded but not sanitised**, and they stay untrusted all the way to the phone. `MapEvent` is the single, explicit crossing point into wire types — one function, not scattered. Downstream signal is by doc comment (`internal/protocol/interactive.go`'s SECURITY blocks, #1393) and by the client-facing prose AC 5 requires, not by the type system; that is the same posture `tool_use` and `unrecognized_message` already take. The one field the mapping *invents* — `ConversationID` — is daemon-generated (`e.sup.CurrentConversation()`), never claude's `session_id`, which no variant in this family carries.
- **[Tokens, secrets, credentials] No new disclosure class, and the reason is load-bearing rather than "no tokens here."** `Description` is, for claude's `local_bash` task type, the **literal command line**, and a command line can carry secrets (`curl -H "Authorization: Bearer …"`, an inline env assignment). This ticket forwards it to a phone. That is acceptable **because the same data already crosses to the same peer on `tool_use`** — `ToolUsePayload.InputSummary` is the compacted raw input of a Bash tool call — over the same AEAD-sealed, capability-gated, paired-device channel. The new frames widen no audience. Nothing here generates, stores, rotates, or compares a credential.
- **[File operations] Not applicable — zero file I/O.** The design is a pure value-to-value mapping plus one `switch` arm; no path is constructed, opened, statted, or written anywhere in it. No traversal, TOCTOU, mode, symlink, or atomic-write surface exists to audit.
- **[Subprocess / external command execution] No daemon-side exposure; the residual is client-side and documentary.** `Description` and `Patch` carry command text, but this design adds **no execution path** — the values enter a struct and leave via `json.Marshal`. The guarantee here is structural, not a grep result: nothing between `MapEvent` and `emit` invokes anything. The real risk is a *future consumer* shelling out what it renders, which is why AC 5's render-never-execute-or-re-shell warnings are an acceptance criterion rather than a nicety, and why the roster repeats the warning per row (a **list** of command lines is a more tempting shape to feed something structured than a single one).
- **[Cryptographic primitives] Not applicable — none introduced, all inherited.** No RNG (the turn-id mint via `conversations.NewID`/`crypto/rand` is not on this path — these frames open no turn), no key, no nonce, no comparison. Sealing happens unchanged in `emit`, downstream of everything this spec touches.
- **[Network & I/O] No findings — the size bound is already measured, not argued.** #1393's `TestBackgroundTaskPayloads_FitV2EnvelopeCap` (`internal/protocol/interactive_test.go:536-602`) pins every payload under the v2 application-envelope cap at genuine worst case: a `<` fill (six wire bytes per input byte under `SetEscapeHTML`, not a misleading `a` fill), all 8 roster entries at their per-entry caps, `DroppedTasks: 1<<31`, and a hostile 64-byte `ConversationID`. **Verified at HEAD.** This is exactly why the spec says no cap test is owed at this layer — the obligation is discharged upstream, not skipped.
- **[Error messages, logs, telemetry] No findings, and AC 4 protects a second sink the ticket does not name.** All four `eventKind` call-site families were audited, not assumed: `interactive_turn_v2.go:145,259,347`, `stream_turn_busy.go:192`, `stream_turn_drain.go:96,231`, `acp_turn_stream.go:102,114` — every one logs a discriminant plus a session/conversation id (and at `acp_turn_stream.go:114` a transport `err`), never event content, each carrying an explicit SECURITY comment. Adding the three arms changes those lanes from `kind=unknown` to a **daemon-owned constant**, so it strictly improves them and introduces no leak. `emit`'s marshal-error branch already refuses to echo the payload or `err.Error()`. **The second sink:** the debug bundle is assembled from `debugbundle.Assemble(recordingsDir, logs)` where `logs` is `logRing.Snapshot()` — *every daemon log line* — and a bundle is an archive a user ships to a maintainer, i.e. materially less protected than the E2E-sealed mobile stream. AC 4 is therefore the barrier between a literal command line and every future debug bundle, not hygiene. The replay ring (`internal/eventring`), which *does* hold the marshalled payloads, is **not** bundled — `Assemble` takes only the log text, the manifest, and the newest PTY recording. Verified.
- **[Concurrency] No findings.** No new goroutine, lock, or mutable state; `Handle` stays single-goroutine (the producer's `Run`) and the new arm reads and writes no emitter field — which is AC 2 restated. The one subtlety worth recording so code-review does not have to rediscover it: the mapping copies **slice headers** (`TruncatedFields`, and the roster's rows), so the payload briefly aliases the event's backing arrays. That is safe here because `emit` marshals to bytes immediately, the ring stores the *marshalled bytes* rather than the payload, nothing retains the payload past the call, and no second goroutine touches the event. A future change that queued payloads instead of bytes would invalidate this and would need a copy.
- **[Threat model alignment] Walked against `docs/protocol-mobile.md` § Security model.** **#1 Prompt injection (high, partial)** — directly in scope and the governing threat: a prompt-injected claude influences every string these frames carry. Mitigation is unchanged from its peers — bounded upstream, forwarded verbatim, documented as inert-render-only. Sanitising *here* would be worse, not better: it would put the escaping decision in the daemon for a rendering context only the client knows, and duplicate a cap the producer already owns. **#3 Relay MITM (cryptographic)** and **#6 Replay (AEAD nonce)** — inherited unchanged; the relay sees ciphertext because sealing is downstream in `emit`. **#4 Token leak via phone** — the frames reach only `Interactive`-granted conns via the existing capability gate in `emit`; the fan-out test asserts interactive-only delivery. **#7 Denial of service (low-medium, deferred)** — see the OUT OF SCOPE item below.
- **[Network & I/O / #7 DoS] OUT OF SCOPE — burst behaviour, deferred to the protocol doc's standing DoS item.** These frames flow through `emit` and are **not** droppable deltas (#610 makes `assistant_delta` the only droppable type), so a roster churn holds queue slots. Two bounded consequences: delivery to a saturated conn degrades (the push errors, logs content-free, and the daemon continues), and — the more interesting one — `internal/eventring` is bounded by event **count, not bytes**, while a worst-case roster frame is materially larger than an `assistant_delta`, so a roster burst evicts more conversation text from a reconnecting phone's replay window than an equal count of deltas would. Neither is a memory-growth vector (both the ring and the producer's 8-entry cap are bounded), and an attacker able to churn background tasks is already executing commands. This is the identical accepted posture `Unrecognized` documents, and it belongs to threat #7, which the protocol doc already marks deferred. No fix in this ticket; flagged so a reviewer reads it as a known trade-off rather than an oversight.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
