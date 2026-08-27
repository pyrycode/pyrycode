# #1849 — emit the mapped `model_list` frame on the live interactive turn lane

**Size:** s (confirmed; see § Scope check)
**Labels:** `enhancement`, `size:s`, `security-sensitive`

## Files to read first

Read these before writing anything. This list is the turn-1 data load; everything the design needs is in it.

- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle` — the type switch you add one arm to. Read the `ModelAnnounced` and `RateLimited` arms end to end; they are the shape, and their doc comments state the switch's own rule for when two variants merge into one arm and when they stay apart.
- `cmd/pyry/interactive_turn_v2.go` → `eventKind` — its `ModelAnnounced` arm and its `ModelList` arm are the two comments this commit corrects (§ Comment corrections). `eventKind` itself needs **no new case**: the `ModelList` arm already exists and already returns `"model_list"`.
- `cmd/pyry/interactive_turn_v2.go` → `emitMapped`, `emit` — the two functions the arm reaches. `emit` owns the capability gate, the `eventring` append and the per-conn fan-out; the arm adds none of that.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.ModelList` case — what the arm's payload will be. Extract two rules it states and that the arm inherits: nothing is re-capped or re-ordered, and the payload **shares backing arrays** with the event (§ The carry-never-mutate rule).
- `cmd/pyry/session_model_hold.go` → `sessionModelHold.Sink` — proof the event already travels this lane: it stores the list and then forwards `ev` unchanged to `streamTurnSink.sinkFor`'s closure. Read its type doc for why the retention sits *upstream* of the fan-in send. Do **not** wire the emitter to this type (§ Explicit non-goals).
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — the `default` arm answers `turnMarkNone` for `ModelList` with no code change. The emitter arm must agree with that answer.
- `cmd/pyry/stream_turn_drain.go` → `streamTurnSink.sinkFor` — where a `turnMarkNone` event is refusable at `droppableCap`. Extract: the live lane is best-effort by construction, and that is the loss point the arm's comment names but does not fix.
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant` — already carries a `turnevent.ModelList` row pinned at `turnMarkNone`. **No change.** Read it so you assert the same lifecycle answer in the emitter tests.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_ModelAnnouncedNoLifecycleMutation`, `TestInteractiveTurnEmitterV2_ModelAnnouncedMidTurnDoesNotDisturbOpenTurn`, `TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant` — the three tests this slice's tests mirror one variant over.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` — the existing rig you extend. Read its "MIND THE POLARITY" paragraph: the log-absence half is passed by an arm that silently drops the event, so the payload-presence half is what discriminates.
- `cmd/pyry/interactive_turn_v2_test.go` → `stubCursor`, `fakeInteractiveBcast`, `pushTypes`, `pushesFor`, `assistantDeltas`, `turnStateValues`, `discardLogger`, `testConvID` — reuse every one of these; add no new test double.
- `internal/protocol/interactive.go` → `ModelListPayload`, `ModelOption`, and their `MarshalJSON` methods — what a decoded envelope looks like. Note the deliberate asymmetry the tests must not "fix": `EffortLevels` normalises nil→`[]`, `TruncatedFields` leaves nil as `null`.
- `internal/streamsup/parser.go` → `maxModelListEntries` — the aggregate size bound already applied at the producer, with the arithmetic against the 65519-byte envelope cap written out. This is why the arm adds no cap of its own (§ Security review, Network & I/O).
- `docs/knowledge/features/turnbridge-package.md` § "The outbound adapter (`MapEvent` / `BuildTurnState`)" — the per-variant mapping table; the `ModelList` (#1848) row states every field-level rule the arm inherits.
- `docs/knowledge/features/streamsup-package.md` § the `newStreamRunnerFactory` / drain description — how a parsed `turnevent` reaches `interactiveTurnEmitterV2.Handle` on the stream-json path.

## Context

`turnevent.ModelList` has had a wire mapping since #1848: `MapEvent` returns `protocol.TypeModelList` and a fully populated `ModelListPayload`. **Nothing calls it.** `interactiveTurnEmitterV2.Handle` has no case for the variant, so a decoded list falls into the `default` and logs `interactive_turn.unknown`. The desktop consumer ([pyrycode-desktop#561](https://github.com/pyrycode/pyrycode-desktop/issues/561)) has been blocked since 2026-08-19 on a menu that never arrives.

This slice adds the one arm that puts the frame on the wire, and nothing else.

The event already reaches the emitter. `sessionModelHold.Sink` (#1840) is a pass-through decorator: it records the list for the session **and** forwards the event to `streamTurnSink.sinkFor`'s closure, so a `turnevent.ModelList` travels the same fan-in lane every other variant does. The retention exists for the client that connects *later* — that is #1846, and this slice does not read it.

**Cadence.** Not per run, not per turn. claude answers one `initialize` control request per child (#1839), and the parser's `emitModelList` produces at most one `ModelList` from it. So the frame fires once per child spawn: at daemon start, and again on a crash-recovery or `/clear` respawn. The list otherwise changes only when a model ships — weeks apart.

**Which of those two cases this slice actually delivers.** `Handle` returns early when `sup.CurrentConversation()` is `""`, and `activeConversation.set` is called from exactly one place, `sessionRouter.Route`'s successful-route path — so the cursor is `""` until a message has been routed. The `initialize` ask fires at child spawn, and `sessions.Pool` constructs the bootstrap session at `New()`. Therefore:

- **Bootstrap child at daemon start** — cursor `""`, the list is dropped at the no-cursor guard. Always. **#1846 owns closing this**; its AC 1 already covers it. AC 3 here *pins that drop as correct behaviour*, not as a gap.
- **Respawn during a routed session** — the cursor is already stamped, and the frame goes out. This is the case this slice delivers, and the one `sessionModelHold`'s "a respawn's report supersedes rather than accumulates" exists for.

The cursor question is **settled as a scope decision**, not left open: answering it the other way — adding a trigger, a lifecycle hook, or a retention read to close the bootstrap case — collapses #1846 into this slice and produces two publishers of one frame.

**ADR:** none warranted. This is one arm in an existing switch, following a per-variant pattern four prior tickets already established (#1386, #1394, #1410, #1638). The design decisions worth recording are already recorded — the wire shape in `docs/protocol-mobile.md` § `model_list` (#1705) and the mapping rules in `turnbridge-package.md` (#1848).

**One published-doc claim goes stale with this commit and the developer must NOT fix it.** `docs/knowledge/features/protocol-package.md` states that `interactiveTurnEmitterV2.Handle` has no arm routing a decoded model list to `MapEvent`, so "the mapping is reachable but never actually reached on any production lane". That becomes false here. `docs/knowledge/` belongs to the documentation phase; flagging it in this section is how it gets folded in. Same for the `#1693` attributions elsewhere in the tree (`cmd/pyry/relay_guard_test.go`, `internal/protocol/codes.go`, `internal/protocol/interactive.go`, `internal/streamsup/parser.go`) — those are **#1847's sweep**, out of scope here. The only `#1693` this commit retires is the one inside `eventKind`'s own `ModelList` arm, and it retires it because the surrounding claim is being rewritten anyway (§ Comment corrections).

## Design

One new `case` in `interactiveTurnEmitterV2.Handle`, placed immediately after the `turnevent.ModelAnnounced` arm.

**Behaviour, exactly the status-peer shape:**

1. `e.flushDelta(ctx)` — any buffered assistant text keeps its wire position ahead of the frame.
2. `e.emitMapped(ctx, convID, ev)` — one `model_list` envelope per interactive conn.

**No turn-lifecycle mutation.** No `startTurnIfNeeded`, no `transitionTo`, no `endTurn`. `inTurn`, `turnID`, `turnConvID`, `currentState` and `seq` are all untouched.

**A separate `case`, not merged into the `ModelAnnounced` arm above it** — despite an identical two-line body. This follows the switch's own stated rule: arms merge when they share a *reason* (`ApiRetry`/`Compacting`; the three background-task variants) and stay apart when each has its own. `ModelAnnounced`'s reason is that an announced model is a property of the **turn's configuration** — claude emits its `init` line once per turn, so the frame rides along with every turn. `ModelList`'s reason is one step further out: the menu is a property of the **child**, reported once per `initialize` exchange, so it is not per-turn at all. Different reason, separate arm. Write the reason into the arm's doc comment the way every neighbouring arm does.

**What the arm's doc comment must state** (prose is yours; these are the claims):

- Same shape as the status peers: no turn-lifecycle mutation, and *why* for this variant specifically — the frame is not turn-scoped and not even per-turn, so opening a turn would leave a mark no turn end could clear.
- `TestTurnMarkFor_TotalOverEveryVariant` already pins the lifecycle answer as `turnMarkNone`, and this arm agrees with it.
- The delta flush precedes the emit, for the neighbours' reason.
- **The two-queue distinction — do not write a blanket "never dropped" claim.** Downstream at `pushQueue` this is not a droppable delta (the droppable set there is `assistant_delta` only, #610), same as its peers. But **upstream** at the fan-in, `turnMarkFor` answers `turnMarkNone`, so `streamTurnSink.sinkFor` classes it droppable and can refuse it at `droppableCap` under load. That is a known loss point this slice does not fix: the live lane is best-effort by construction, acceptable precisely because `sessionModelHold`'s retention sits above that send and #1846 is the reliable path.
- No rate bound on either side, and that is deliberate: one `initialize` exchange per child is the cadence, the frames are bounded upstream, and a second differently-shaped filter here is the hazard the `ThinkingProgress`, `RateLimited` and `ModelAnnounced` arms each name by that name.
- No capability gate in the arm. The interactive grant is filtered once, in `emit`, for every frame type.

**Data flow (unchanged upstream of the new arm):**

```
claude stdout
  → streamsup.Parser.emitModelList      one turnevent.ModelList, all dimensions capped
  → sessionModelHold.Sink               retains for the session, forwards unchanged  (#1840)
  → streamTurnSink.sinkFor              fan-in; watermarked, refusable under load
  → drain goroutine
  → interactiveTurnEmitterV2.Handle     ← THIS SLICE: the new case
  → emitMapped → turnbridge.MapEvent    protocol.TypeModelList + ModelListPayload   (#1848)
  → emit                                ring.Append, capability gate, per-conn Push
```

### The carry-never-mutate rule

`MapEvent`'s `ModelList` arm copies slice **headers** into the payload, so the payload shares backing arrays with the event. `sessionModelHold` retains that same `turnevent.ModelList` **without copying**, and `sessionModelHold.ModelList()` is read on a relay-leg goroutine. A sort, an in-place dedupe, a filter, or an append into a slice the event owns would therefore corrupt the session's retained menu across two goroutines — a data race, not merely a wrong menu.

The arm holds this **by construction**: it passes `ev` straight through and touches no field. Do not "normalise" the list on the way past. `json.Marshal` in `emit` is a read only, and nothing on this lane ever writes those arrays, so the marshal is race-free.

### `eventring` retention comes for free, and it is not #1846

`emit` appends every emitted envelope to `e.ring`, so a `model_list` joins the #647 reconnect-replay ring like any other frame. That is **reconnect** replay — a client resuming with a `last_event_id` — and is not a fresh-connect backfill. It does not close the bootstrap case, and it must not be described as doing so. #1846 remains the backfill path.

### Explicit non-goals

- **Do not widen `sessions.Runner`.** Nothing here needs a new method on it.
- **Do not read `sessionModelHold` / `streamRunner.ModelList()`.** The emitter has no handle on either, and giving it one is #1846's design.
- **Do not add a trigger, lifecycle hook, or synthesised emit** to cover the bootstrap case. AC 4 exists to make that a red test.
- **Do not touch `eventKind`'s switch cases, `turnMarkFor`, `TestTurnMarkFor_TotalOverEveryVariant`, `relay_guard_test.go`, `internal/turnbridge`, `internal/protocol`, or any file under `docs/knowledge/`.** All are already correct or belong to another ticket.

## Comment corrections

Two comments in `eventKind` are already wrong at HEAD and this change makes both wronger. Correct both **in this commit**. Fixing one and leaving the other is the specific failure to avoid: #1847's sweep would re-point the surviving `#1693` and leave its false claim standing, and neither ticket's criteria would catch it.

**1. `eventKind`'s `ModelAnnounced` arm** claims *"Handle now has an arm for 15 of turnevent.Event's 16 implementations, and the one without is PermissionRequest."*

`turnevent.Event` has **17** implementations — 16 in `internal/turnevent/event.go` plus `PermissionRequest` in `permission.go` (re-derived at `0ab38b42`: 17 `isTurnEvent()` method declarations). `ModelList` is the seventeenth and landed in #1839, after that comment was written, so today both the count and the exclusion list are short by one. After this slice the arithmetic closes again: **16 of 17, and the one without is `PermissionRequest`** — which `streamsup.Parser` never produces (it is PTY/modalbridge-only). Change the two numerals; the surrounding argument stands unchanged.

**2. `eventKind`'s own `ModelList` arm** claims *"Unlike the three arms above this variant is NOT claimed by a Handle case on this lane: `turnbridge.MapEvent`'s default drops it until #1693."* The `MapEvent` half is **already false at HEAD** (#1848 landed that arm) and this slice falsifies the `Handle` half.

Rewrite the second paragraph so it says what the three arms above say, one variant over:

- **Keep** the content-free half verbatim in substance: `Value`, `ResolvedModel` and `DisplayName` are exactly the fields #833's posture exists to keep out of a log, none of them is returned, and neither is the entry count.
- **Replace** the "NOT claimed" claim: like the three arms above, this variant **is** now claimed by a `Handle` case on this lane, so this file's `interactive_turn.unknown` Debug is no longer a live call site for it.
- **Keep** the reason the arm still exists: the other `eventKind` call sites (`acp_turn_stream.go`, `stream_turn_busy.go`, `stream_turn_drain.go`) plus the reachable no-cursor drop on this lane, all of which would otherwise read `kind=unknown` for a variant the daemon does recognize.
- **Retire `#1693`.** Cite this ticket (#1849) if a number helps, or none at all. Do not reintroduce `#1693` anywhere — #1847 sweeps the rest of the tree and should find one fewer, not one re-pointed.

## Concurrency model

Unchanged. No new goroutine, no new channel, no new lock, no new field on `interactiveTurnEmitterV2`.

`Handle` runs only on the drain's single goroutine, and the arm inherits that. The one cross-goroutine surface it touches is `e.ring`, which is self-synchronised and already touched by every other arm through `emit`. `sessionModelHold.mu` is a leaf lock this lane never takes — the arm holds a value the hold already released, so no new edge enters the daemon's lock order.

## Error handling

- **Empty cursor** → the existing `interactive_turn.no_cursor` Debug drop, before the type switch. Unchanged, and pinned by AC 3.
- **`MapEvent` returns `ok == false`** → `emitMapped`'s `interactive_turn.unmapped` Debug. Stays **defensive and unreachable** for this variant: `MapEvent`'s `ModelList` arm has no suppression branch and maps even a zero-value list. Add no fallback.
- **`json.Marshal` failure in `emit`** → the existing `interactive_turn.marshal_err` Debug. Defensive; `ModelListPayload` and `ModelOption` are closed string/int/slice structs whose `MarshalJSON` methods cannot fail on this input.
- **`Push` failure** → the existing `interactive_turn.push_err` Debug, per conn, with the transport sentinel only. Unchanged.
- **Refused at the fan-in under load** → `stream_turn.sink_full` Debug, logging `kind` and `session_id` only. Not this slice's to fix (§ Design).

No new error path, no new sentinel, no new log record. Every log record reachable on this path is content-free today and must stay so; AC 5 pins it.

## Testing strategy

`cmd/pyry/interactive_turn_v2_test.go` only. Reuse `stubCursor`, `fakeInteractiveBcast`, `pushTypes`, `pushesFor`, `assistantDeltas`, `turnStateValues`, `discardLogger` and `testConvID`; add no new double.

**Fixtures.** Declare one package-level sentinel set for the variant, mirroring `modelAnnouncedFixture`'s precedent and its stated constraint: sentinels must be conspicuous strings that are **not substrings of the log's own text**, or the negative assertions go red against a correct implementation. `kind=model_list` is what appears in the log, so no fixture may contain `model`, `list`, `announced`, or `event`. Use two **distinguishable** entries (per `turnbridge-package.md`'s own rule — identical rows let a swapped-index bug pass), each with a distinct `ResolvedModel`, `Value`, `DisplayName`, and `EffortLevels`, and a conspicuous non-zero `DroppedModels`.

Scenarios:

1. **`…_ModelListFansOutToEveryInteractiveConn`** (AC 1). Live cursor; snapshot of two interactive conns plus one non-interactive. Drive one `turnevent.ModelList` with the two-entry fixture. Assert: each interactive conn received exactly one envelope and its type is `protocol.TypeModelList`; the non-interactive conn received none; and one decoded `ModelListPayload` carries `ConversationID == testConvID`, both entries' six fields verbatim in claude's order, and `DroppedModels` equal to the fixture's. This is the **only** test that decodes payload fields — the rest assert on `pushTypes`.
2. **`…_ModelListNoLifecycleMutation`** (AC 2). Live cursor, no turn open. Drive one `ModelList`. Assert the push sequence is exactly `[model_list]` — a `turn_state` anywhere is the observable signature of a `transitionTo` — and `e.inTurn == false`, `e.turnID == ""`, `e.currentState == ""`. Then drive a `TextChunk` and flush, and assert a fresh turn still opens: `[model_list, turn_state(responding), assistant_delta]`.
3. **`…_ModelListMidTurnDoesNotDisturbOpenTurn`** (AC 2). Open a turn with a buffered `TextChunk`, capture `turnID` / `currentState`, drive the `ModelList`, then drive **one more event past it** plus a `TurnEnd` — the event past the frame is what catches an `endTurn`, since a frame-local check passes either way. Assert the full order `[turn_state, assistant_delta, model_list, assistant_delta, turn_end, turn_state]`, that both deltas carry the pre-frame `turnID`, and that `seq` reads `0, 1` — an unbroken sequence is what proves no re-mint.
4. **`…_ModelListEventKindNamesTheVariant`** (AC 3 + AC 5). Empty cursor, `slog` text handler at Debug with `slog.TimeKey` dropped (the neighbouring tests' measured reason: a whole-log `strings.Contains` otherwise has a host-dependent source of digits to collide with). Drive the sentinel `ModelList`. Assert: a log was produced; it contains `kind=model_list`; it does **not** contain `kind=unknown`; it contains **none** of the string sentinels; and it does not contain the decimal spelling of `DroppedModels` or of the entry count. Then the AC 3 half on the same rig: **zero** pushes, and `e.inTurn == false`. The empty cursor is load-bearing rather than incidental — with a live cursor the arm claims the event and it reaches no `eventKind` site at all, which is exactly what makes this the reachable call site.
5. **`…_ModelListNotSynthesizedWithoutAnEvent`** (AC 4). Live cursor, and drive a complete turn — `TextChunk`, `ToolStart`, `ToolUpdate`, `TurnEnd` — with **no** `ModelList` anywhere. Assert no `protocol.TypeModelList` appears in the push sequence at any point. This is AC 4's pin and it is not a tautology: `MapEvent` maps a zero-value `ModelList` rather than dropping it, so an arm emitting from anything other than a received event would put an empty menu on the wire, and this is the test that reddens.
6. **Extend `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak`** (AC 5), rather than adding a sixth test — the precedent #1638 set for `ModelAnnounced`. Add the sentinel `ModelList` to that test's event sequence and its sentinel strings to the leak loop, so the variant is driven through the rig whose single conn always fails `Push` and therefore fires the log-heavy `push_err` branch for every envelope. Then add the **polarity pair**: find the `model_list` envelope among the recorded pushes and assert a sentinel field survived on the wire. Mind the polarity, and read that test's existing paragraph on it — log-absence alone is passed by an arm that silently drops the event, so payload-presence is the half that discriminates.

**Do not assert** that `kind=model_list` appears on the `push_err` path in scenario 6. It is unsatisfiable: `push_err` carries no `kind` field, and after this arm lands, the only records that log `eventKind` for this variant on this lane are the no-cursor drop (scenario 4) and two branches the arm makes unreachable. The positive control proving the event traversed the log-heavy path is that test's existing `logs == ""` Fatal.

**Gate:** `make check`. This lane has no `e2e_realclaude` coverage in this slice — the hermetic end-to-end proof is #1845.

## Scope check

Re-applied against this written spec, not against the initial sketch.

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created/modified | ≤ 3 | **1** — `cmd/pyry/interactive_turn_v2.go` |
| Total written work | ≤ 400 lines | **~330** (~50 production incl. the two comment rewrites; ~280 tests) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — additive `case`, no signature change |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0 new** — every branch on the path already exists |

Analogue commits, re-derived at `0ab38b42` with `git show --stat`: #1638 (`ea2efa23`) `cmd/pyry` half = 51 production + 187 test lines; #1410 (`9ee56a95`) = 25 + 198. This slice is #1638's shape with two extra tests (AC 4's negative and AC 1's multi-conn fan-out) and no `internal/turnbridge` half, #1848 (`45374455`) having shipped it.

**Branch-overlap check** (`git fetch origin --prune`, then every `origin/feature/<N>` diffed against `origin/main`): no in-flight branch touches `cmd/pyry/interactive_turn_v2.go` or `cmd/pyry/interactive_turn_v2_test.go`. No block set.

## Open questions

None blocking. Two decisions are recorded here rather than left open:

- **The cursor question is closed as scope**, not as design: the bootstrap-child case belongs to #1846 and duplicating it here would create two publishers of one frame (§ Context).
- **The fan-in drop under load is accepted**, not fixed: the retention above it and #1846 below it are what make a best-effort live lane acceptable (§ Design).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is claude's subprocess stdout, crossed once at `streamsup.Parser.emitModelList`, which bounds all three of the list's dimensions **at construction** (`maxModelListEntries`, `maxModelResolved`/`maxModelValue`/`maxModelDisplayName`, `maxModelEffortLevel`/`maxModelEffortLevelCount`) so an oversized payload never enters the event stream, the ring, the push queue or a log. Downstream of it the value is bounded-but-**not sanitised**, and that is the deliberate posture: the daemon bounds, the client's render boundary sanitises. The arm this slice adds is a pure forwarder and introduces no second boundary. **Concretely: the frame is a report, never a control input** — no value in it selects a model, reaches `exec.Command`, or feeds a decision on the daemon side; `internal/relay`'s `validModel`/`validEffort` bound a *phone-supplied inbound* override and are a deliberately different rule that must not be applied to this outbound report.
- **[Trust boundaries — aliasing across goroutines]** SHOULD FIX, addressed in the spec (§ The carry-never-mutate rule). This is the one real hazard in the change and it is not obvious: `MapEvent` copies slice *headers*, `sessionModelHold` retains the same `turnevent.ModelList` without copying, and `sessionModelHold.ModelList()` reads it on a relay-leg goroutine — so any in-place sort, dedupe, filter or append inside the new arm is a cross-goroutine data race on the session's retained menu, not merely a wrong menu. The design holds it by construction (the arm passes `ev` through untouched) and the spec names it so a "helpful" normalisation does not get added later. Code review should treat any field access on `ev` inside the arm as a finding.
- **[Tokens, secrets, credentials]** Not applicable — the change moves no credential. Worth stating for the adjacent surface: model / effort values are **not** secrets but are governed by #833's never-log posture, handled under Logs below.
- **[File operations]** Not applicable — the arm performs no filesystem access, opens no path, and writes no registry. Nothing in the change concatenates any claude-authored string into a path.
- **[Subprocess / external command execution]** Not applicable in the outbound direction, and that is the load-bearing claim rather than an absence: this frame is daemon→client only. No value in a `ModelList` is passed to `exec.Command`, and nothing in this slice creates a path by which one could be. The child's own spawn arguments are `streamsup`'s and untouched here.
- **[Cryptographic primitives]** No findings. The arm mints no ids — it deliberately does **not** call `startTurnIfNeeded`, so it never reaches `conversations.NewID`'s `crypto/rand`. Envelope sealing is `internal/relay`'s `Push`, unchanged.
- **[Network & I/O — frame size]** No MUST FIX, and the arithmetic is worth writing down because the obvious defensive reflex here is wrong. The per-frame ceiling is already closed upstream: `maxModelListEntries`' doc derives 10 entries × 1024 bytes = 10 KiB retained, ~15.6% of the 65519-byte v2 application-envelope cap, ~31% under pathological all-quote escaping. **Adding a cap in the arm would be a second place the limit is decided, able to disagree silently with the producer's** — the hazard the `ThinkingProgress`, `RateLimited` and `ModelAnnounced` arms each name. Do not reach for `maxDeltaTextBytes`; it bounds assistant text on a different path and is not an applicable bound here.
- **[Network & I/O — memory amplification]** No findings, checked rather than assumed. `emit` appends every envelope to `e.ring`, whose bound is `eventring.MaxEventsPerConversation` = 1024 **events**, not bytes — so a per-frame size cap is also a per-conversation memory multiplier. Worst case for this variant is ~20 KB escaped × 1024, but it is strictly below the ceiling that already exists: `assistant_delta` is bounded at `maxDeltaTextBytes` = 10000 raw bytes, whose own doc measures 60220 bytes escaped, three times a `model_list`. The ring is bounded by count across all variants, so `model_list` frames displace other events rather than adding a new ceiling. The conversation-level memory bound is unchanged by this slice.
- **[Network & I/O — flood resistance]** No findings. The cadence is one frame per child spawn, driven by claude's own `initialize` reply and not by anything network-reachable — a phone cannot request one, because this slice declares no inbound verb (`relay_guard_test.go` classifies `TypeModelList` as an outbound push, unchanged here). The only amplifier is a respawn loop, which is already governed by the supervisor's backoff ladder. Per the arms this one mirrors, adding a rate filter here would be the differently-shaped second filter, not a defence.
- **[Error messages, logs, telemetry]** No MUST FIX; this is the category AC 5 exists for and it is pinned by two tests. #833's posture — restated on this path in `internal/relay`'s `v2session_settings.go`, `internal/sessions`' `pool.go` and `eventKind`'s own `ModelAnnounced` arm — is that model / effort / YOLO values are never logged at any level. `ModelList` multiplies the temptation rather than merely repeating it: every entry carries a `Value`, a `ResolvedModel`, a `DisplayName` and an `EffortLevels` list. **MUST-NOT-log: all four, plus the entry count and `DroppedModels`** (both derived from the list's contents). MUST-log stays the existing content-free discriminant set: `event`, `kind` (the variant name alone, from `eventKind`), `conversation_id`, `turn_id`, `env_id`, `conn_id`, and `Push`'s transport sentinel. `sessionModelHold` enforces the same posture by construction — no logger field, and its constructor takes none — and this spec adds no logger anywhere. Scenario 4's per-value negatives are what discriminate: an `eventKind` arm returning `"model_list:" + Value` or `+ strconv.Itoa(len(Models))` leaves `strings.Contains(logs, "kind=model_list")` **true**, so the positive assertion alone would pass it.
- **[Concurrency]** No findings. The arm spawns no goroutine, takes no lock, and adds no field. `Handle` runs only on the drain's single goroutine; the sole cross-goroutine surface is `e.ring`, self-synchronised and already reached by every other arm through `emit`. `sessionModelHold.mu` is a leaf lock this lane never takes — the hold releases it before forwarding — so no new edge enters the lock order alongside `Pool.mu`, `Session.lcMu` or `capMu`. No new shutdown path: `emit`'s existing `ctx.Err()` early return covers teardown. The one concurrency hazard that *is* real is the aliasing finding above, filed under trust boundaries where it originates.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model's outbound-push threats are addressed by mechanisms this slice inherits unchanged: the frame is sealed per conn by `Push`, gated on the negotiated interactive capability once in `emit` (a second gate in the arm is explicitly rejected — that is how a single gate stops being single), and carries no daemon-internal state beyond the conversation id. **Named as out of scope with an owner:** delivery reliability for this frame — the fan-in `droppableCap` refusal and the bootstrap-cursor drop both mean a live client can miss the list — is **#1846**'s, which reads the retention `sessionModelHold` already holds. Neither is a confidentiality or integrity gap; both are availability of a menu, self-healed by the retained copy.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-27
