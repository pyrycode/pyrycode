# Spec: emit the usage-limit frame to an interactive v2 mobile client (#1410)

**Size:** S (confirmed; see § Sizing). **Labels:** `security-sensitive` → § Security review below is mandatory and ran.
**Blocked-by:** none open. **Blocks:** #1411 (the healthy-run silence proofs, both tiers).

## Files to read first

Generated from `codegraph_context` on the ticket title + AC paraphrase, then pruned to what the design actually decides, plus the markdown sites codegraph does not parse.

**The two production edit points**

- `internal/turnbridge/outbound.go:189-214` — the `ThinkingProgress` arm and the `default:` that follows it. The new arm goes **between** them. Extract: the arm's exact shape (comment stating the non-turn-scoped posture, then a single composite-literal `return`), and that `default:` returns `"", nil, false`.
- `internal/turnbridge/outbound.go:129-146` — `turnevent.BackgroundTaskStarted`, the arm the ticket names as the template. Extract: the `TruncatedFields: e.TruncatedFields` pass-through and the sentence explaining why dropping it would present cut text as complete.
- `internal/turnbridge/outbound.go:159-188` — `BackgroundTaskRoster`. Extract the **one line that does not transfer**: it deliberately leaves nil as nil because `BackgroundTaskRosterPayload.MarshalJSON` normalises nil→`[]`. `RateLimitedPayload` has no such marshaller, so here nil-left-as-nil produces `null` and *that* is the required wire value. Same code, opposite reason.
- `cmd/pyry/interactive_turn_v2.go:274-295` — the `ThinkingProgress` handler case. The new case goes immediately after it, before `default:`. Extract: the two-line body (`flushDelta` then `emitMapped`) and the comment structure (shared posture, then the reason specific to *this* variant).
- `cmd/pyry/interactive_turn_v2.go:140-160` — the top of `Handle`: the empty-cursor drop log at `:143-145` (the log line AC3's test rides) and the follow-active-switch block. Extract: the drop log's three attrs — `msg`, `event=interactive_turn.no_cursor`, `kind=eventKind(ev)`. That is the entire claude-visible surface of that log line.
- `cmd/pyry/interactive_turn_v2.go:501-509` — the `eventKind` arm that **already exists**. Read it, do not rewrite it. This ticket adds its test only.

**The contract this maps onto**

- `internal/protocol/interactive.go:340-397` — `RateLimitedPayload`'s doc comment and struct. Extract: the five fields and their wire tags, the SECURITY paragraph, and `:371-377` — the explicit statement that this type has **no** `MarshalJSON` because nothing-was-cut is an absence that must reach the wire as `null`.
- `internal/turnevent/event.go:360-440` — `turnevent.RateLimited`. Extract: "It is a REPORT, never a control input", "opens and closes no turn", the `session_id`/`uuid`/`overage*` exclusions, and that both strings are bounded **at construction** by the producer.
- `internal/streamsup/parser.go:1227-1265` (`emitRateLimit`) and `:279` (`benignRateLimitStatus = "allowed"`). Extract: the three-rung gate. This is the whole reason the mapping does no filtering — the policy decision is already made upstream. Also extract `:1255-1259` — the two `bound()` calls that fix `TruncatedFields`' member names (`"status"`, `"limit_type"`) and their order.

**The test templates**

- `internal/turnbridge/outbound_test.go:14-40` + `:381-399` — `TestMapEventOutbound`'s table struct and its loop. Extract: the comparison is `reflect.DeepEqual(payload, tt.wantPayload)`, which distinguishes a nil `[]string` from an empty one — that is a free second rung under AC1's nil pin.
- `internal/turnbridge/outbound_test.go:402-470` — `TestMapEventBackgroundTaskRosterEmptyTasksOnTheWire`. **Take the shape, invert the assertion** (§ AC1 below). Extract: the `want`/`notWant` row struct, the marshal-what-`MapEvent`-returned discipline, and the deliberate control row.
- `cmd/pyry/interactive_turn_v2_test.go:1830-1867` — `…ThinkingProgressNoLifecycleMutation`. The AC2 bare-event template.
- `cmd/pyry/interactive_turn_v2_test.go:1869-1927` — `…ThinkingProgressMidTurnDoesNotDisturbOpenTurn`. The AC2 open-turn template; note its own comment on why the event driven *past* the frame is the load-bearing part.
- `cmd/pyry/interactive_turn_v2_test.go:1966-2001` — `…ThinkingProgressEventKindNamesTheVariant`. The AC3 template, **with one construction defect not to copy** (§ AC3, the timestamp hazard — measured).
- Helpers in the same file: `stubCursor`, `fakeInteractiveBcast`, `discardLogger()`, `pushTypes()`, `turnStateValues()`, `assistantDeltas()`, `testConvID`.

**The doc sweep targets** (markdown — grep/Read, not codegraph)

- `internal/protocol/codes.go:318-321`, `:252-254` (the optional twin)
- `docs/protocol-mobile.md:448`, `:931-933`, and `:523` / `:979` (the two count literals — **read only, do not touch**)
- `docs/knowledge/features/protocol-package.md:12`, `:862`, `:1183`, `:1219`, `:1271`
- `docs/knowledge/features/turnbridge-package.md:330-334` (the `MapEvent` table)
- `docs/knowledge/codebase/1394.md`, `docs/knowledge/codebase/1405.md` — the two predecessors' full write-ups

## Context

`internal/streamsup` translates claude's top-level `rate_limit_event` line into `turnevent.RateLimited` (#1404). `internal/protocol` declares `TypeRateLimited` + `RateLimitedPayload`, both fixtures, both drift-detector registrations, and the `docs/protocol-mobile.md` § `rate_limited` section (#1405). Nothing joins them: `turnbridge.MapEvent` falls to `default:` for the variant, so it returns `ok == false` and the report dies at the daemon boundary. A phone sees an unexplained stall.

This ticket adds the one mapping arm and the one handler case that close the gap, pins them, and falsifies the six places that currently state the frame is never emitted.

The route is fully established: #1394 did exactly this for the three background-task frames, #1386 for `thinking_progress`, #1074 for `api_retry`/`compacting`. No new abstraction, no signature change, no new exported symbol.

**What this slice must NOT do.** The decision "is this worth telling a person" is already made upstream by `emitRateLimit`'s three-rung gate — a benign report (`status == "allowed"`, which is what all three captures on record carry) produces no daemon event at all. By the time an event reaches `MapEvent` the policy has been applied. This slice is **translation, not policy**: no filtering, no clamping, no defaulting, no re-capping, no validation. A second, differently-shaped filter here would diverge from the producer's silently — the exact hazard `ThinkingProgress`'s arm comment already names.

**Turn scoping is inherited, not re-decided.** `RateLimitedPayload` has no `turn_id` (#1405) and `turnevent.RateLimited`'s doc states it opens and closes no turn. `cmd/pyry/stream_turn_busy.go`'s opener whitelist needs no change and its comment already records that (`DISCHARGED 2026-08-09 (#1404)` at `:142`). **Nothing in this ticket opens `stream_turn_busy.go`.**

## Design

### 1. The mapping arm — `internal/turnbridge/outbound.go`

One additive `case turnevent.RateLimited:` inserted **after** the `ThinkingProgress` arm (`:189-210`) and **before** `default:` (`:211`).

Contract:

```
MapEvent(turnevent.RateLimited{Status, LimitType, ResetsAt, TruncatedFields}, tc)
  → (protocol.TypeRateLimited, protocol.RateLimitedPayload{…}, true)
```

The mapping is 1:1 and total. Field correspondence, exhaustively — five payload fields, four from the event, one from the turn context, nothing else:

| payload field | source | rule |
|---|---|---|
| `ConversationID` | `tc.ConversationID` | the bridge's **only** addition |
| `Status` | `e.Status` | verbatim |
| `LimitType` | `e.LimitType` | verbatim |
| `ResetsAt` | `e.ResetsAt` | verbatim — **no clamp, no range check, no zero-means-now rewrite** |
| `TruncatedFields` | `e.TruncatedFields` | verbatim, **including nil-stays-nil** |

`tc.TurnID` and `tc.Seq` are **ignored**, as in every non-turn-scoped arm above. The payload has no field for either; a usage-limit window is orthogonal to whichever turn happened to observe it.

Three properties the arm's doc comment must state, because each is a thing a later reader would otherwise "fix":

1. **Nil `TruncatedFields` is passed straight through, and here that is what puts `null` on the wire.** The roster arm looks identical and means the opposite: there, `BackgroundTaskRosterPayload.MarshalJSON` converts the nil to `[]`. `RateLimitedPayload` has no `MarshalJSON` on purpose (`interactive.go:371-377`) — nothing-was-cut is an *absence*, and an allocating mapper (`make([]string, 0, …)`, or `append` into a pre-declared empty slice) would silently change the wire from `null` to `[]`. Nothing in the tree notices today; AC1's byte test is what makes it notice.
2. **`ResetsAt` crosses unvalidated in both directions.** It is claude's number. Not necessarily in the future, not necessarily in a sane range. The bounds decision belongs to nobody in this slice — see `RateLimitedPayload`'s SECURITY paragraph and `turnevent.RateLimited.ResetsAt`'s own doc.
3. **Neither string is re-capped.** The producer bounded both at construction (`streamsup.maxRateLimitField`), following `Unrecognized`'s precedent. A second cap here would be a second place the limit is decided and the two could disagree silently — the same reason `BackgroundTaskStarted`'s arm states it.

`Status` and `TruncatedFields` are the two fields a hurried mapping drops, and the two least droppable: `Status` is the only field that says *why*, and the producer's gate is deliberately loud in that direction (any non-benign status emits, so an unrecognised one surfaces and a human looks) — dropping it silences that one layer later. Dropping `TruncatedFields` presents claude's cut text to a phone as complete.

### 2. The handler case — `cmd/pyry/interactive_turn_v2.go`

One additive `case turnevent.RateLimited:` in `Handle`'s type switch, inserted **after** the `ThinkingProgress` case (`:274-295`) and **before** `default:` (`:296`).

Its own case, **not folded into** the `BackgroundTask*` multi-variant case: the body is identical but the justification is not, and every peer in this switch carries its own reason. Body, two statements, matching `:294-295` exactly:

```go
e.flushDelta(ctx)
e.emitMapped(ctx, convID, ev)
```

**No** `startTurnIfNeeded`, **no** `transitionTo`, **no** `endTurn`. `inTurn`, `turnID`, `currentState` are untouched. `flushDelta` runs first so buffered assistant text keeps its wire position ahead of the frame — this is the shape every non-turn-scoped variant already takes (`Stall` `:218`, `ApiRetry`/`Compacting` `:229`, `Unrecognized` `:240`, the background-task trio `:256`, `ThinkingProgress` `:274`).

The variant-specific reason for the comment: a usage limit is a **condition of the account, not of the turn**. Opening a turn on one would wedge the conversation exactly as opening one on an unrecognized message would — no turn end follows a fact orthogonal to the turn. And the frame may legitimately arrive with no turn open at all, since claude emits its line once per run regardless of turn state.

Also state, as the peers do: like `turn_state` this flows through `emit()` and is **not** a droppable delta (the droppable set is `assistant_delta` only, #610), so it holds a queue slot. That is accepted and bounded by the producer — `emitRateLimit` fires at most once per run.

### 3. `eventKind` — **no production change**

`cmd/pyry/interactive_turn_v2.go:501-509` already returns `"rate_limited"`; it landed in #1404. This ticket adds the **test**, not the arm. Do not touch `:501-509` and do not touch `cmd/pyry/relay_guard_test.go` (`excludedTypes["TypeRateLimited"]` landed in #1405).

Both sibling tests shipped in the *emit* ticket rather than the shape ticket (`…BackgroundTasksEventKindNamesTheVariant` in `9ca9b77`/#1394, `…ThinkingProgressEventKindNamesTheVariant` in `43f5131`/#1386). This is the emit ticket.

### 4. Data flow, end to end

```
claude stdout: {"type":"rate_limit_event","rate_limit_info":{…}}
  → streamsup.Parser.emitRateLimit          three-rung gate (POLICY lives here)
      benign "allowed" / empty status / undecodable → dropped, one closed-keyword log
      otherwise → bound() both strings, record cut names
  → turnevent.RateLimited{Status, LimitType, ResetsAt, TruncatedFields}   (no identity)
  → cmd/pyry interactiveTurnEmitterV2.Handle          NEW case: flushDelta, then emitMapped
  → turnbridge.MapEvent                               NEW arm: + tc.ConversationID, 1:1
  → protocol.RateLimitedPayload                       no MarshalJSON — nil ⇒ "null"
  → emit() → json.Marshal → eventring.Append → sealed envelope per interactive conn
```

### 5. Fixture values — one collision-free sentinel set, shared by both test files

AC1 needs distinct values in every string field (so a swapped mapping cannot pass). AC3 needs values that are not substrings of the captured log, of the frame name, or of each other (so a `strings.Contains` negative is not red against a correct build). One set satisfies both; the constraints reinforce rather than compete.

The AC3 log line is exactly: `level=DEBUG msg="relay: interactive-turn drop; no cursor" event=interactive_turn.no_cursor kind=rate_limited` (plus `time=…`, see the hazard below). The trap the ticket names: a natural `Status` fixture of `"limited"`, `"limit"`, `"rate"` or `"rate_limited"` is a substring of `kind=rate_limited`, making the negative **red against a correct implementation**.

Recommended set — the developer may substitute equivalents that satisfy every constraint below:

| field | value | notes |
|---|---|---|
| `Status` | `"qq-status-sentinel"` | no `<`/`>`/`&`, so `json.Marshal` does not HTML-escape it (#1405's `"<unmeasured>"` fixture *is* escaped — do not reuse it in a byte-substring assertion) |
| `LimitType` | `"zz-limittype-sentinel"` | distinct prefix from `Status`; neither is a substring of the other |
| `ConversationID` | `"cc-conv-sentinel"` | turnbridge test only — AC3's cursor is empty by construction |
| `TruncatedFields` | `[]string{"tf-alpha-sentinel", "tf-beta-sentinel"}` | two distinct members so **order** is pinned, not just membership |
| `ResetsAt` | `-1` (row A) and `4102444800` (row B, 2100-01-01Z) | neither is a plausible instant; a clamp-to-zero, an abs(), a reformat, or an absent-means-now rewrite all go red |

Constraint checklist the chosen values must satisfy — worth restating because it is the whole point:

- none is a substring of `rate_limited`, `interactive_turn.no_cursor`, `relay: interactive-turn drop; no cursor`, `DEBUG`, `kind`, `event`, `msg`, or `level`
- none is a substring of another
- none contains a character `encoding/json` escapes, so the byte assertions can be written literally

### 6. The timestamp hazard in the AC3 test — measured, not hypothetical

The AC3 template asserts its negative with a bare `strings.Contains` over the whole captured log, and its haystack **includes slog's own `time=` attribute**: `time=2026-08-09T09:16:40.272+03:00 level=DEBUG …`.

Measured on this host, 200 000 samples of that exact log line: the needle `"37"` — which the template test uses today, being `ThinkingProgress.EstimatedTokensDelta` — collides with the timestamp in **4310/200 000 = 2.15%** of runs. `"184"`, `"-1"` and `"4102444800"` collided 0/200 000. But `"-1"` is safe here only because this host's UTC offset is `+03:00`; on a host at UTC−10..−12 the offset itself contains `-1`. So needle choice alone is **host-dependent**, which is not a property a test should rest on.

**Therefore: the new AC3 test builds its handler with `slog.HandlerOptions.ReplaceAttr` dropping `slog.TimeKey`**, so the haystack carries no digits at all and no host-dependent bytes. Sentinel choice and time-suppression are deliberately different fabric: the sentinels are a naming discipline, the suppression is a deterministic removal of the entire false-positive source.

Do **not** refactor `…ThinkingProgressEventKindNamesTheVariant` — out of scope. Its 2.15% is worth one line in the codebase note so a future flake is diagnosed rather than re-investigated.

## Concurrency model

Unchanged; nothing new is introduced.

`MapEvent` is a pure function — no state, no I/O, safe from any goroutine. `interactiveTurnEmitterV2.Handle` is explicitly *not* safe for concurrent use and runs only on the producer's single `Run` goroutine (see the struct doc and `flushC`'s comment at `:129-133`); the new case adds no goroutine, no channel, no lock, and no timer interaction beyond `flushDelta`'s existing `flushTimer.Stop()`. No shutdown-sequence change.

## Error handling

No new failure mode. The arm cannot fail — it is a struct literal.

- **`ok == false` is unreachable for this variant after this change**, exactly as for its peers. `emitMapped`'s existing defensive branch (`:383-388`) still logs `interactive_turn.unmapped` with `kind` only, and `eventKind` returns the variant name only, so even that unreachable path leaks nothing.
- **Empty cursor** → the existing `:143-145` drop. Unchanged, and it is the path AC3's test rides.
- **Marshal failure** → `emit`'s existing defensive branch (`:398-408`), which never echoes the payload or `err.Error()`. `RateLimitedPayload` is three strings, an int64 and a string slice; it cannot fail in practice.
- **Follow-active switch mid-turn** → the existing `:149-160` block runs before the type switch and is unaffected.

## Testing strategy

`make check` is the gate. Everything below is hermetic; no realclaude, no relay, no new e2e file. Scenarios are described as inputs + expected behaviour — the developer writes them in the file's existing idiom (table-driven where the surrounding test is, `t.Parallel()` throughout).

### AC1 — every field crosses, none invented

**A. Rows in `TestMapEventOutbound` (`internal/turnbridge/outbound_test.go`).** The loop's `reflect.DeepEqual` already distinguishes nil from empty `[]string`, so these rows are a free second rung under the nil pin.

- *populated, negative instant* — `RateLimited{Status: qq-…, LimitType: zz-…, ResetsAt: -1, TruncatedFields: [tf-alpha…, tf-beta…]}` with `tc` → `TypeRateLimited` + the exactly-corresponding payload, `ok == true`. Distinct strings in every field are what make a swapped mapping fail here.
- *far-future instant, nil truncation* — `ResetsAt: 4102444800`, `TruncatedFields: nil` → `TruncatedFields` **nil** in `wantPayload` (not `[]string{}`).
- *ignores turn addressing* — feed `TurnContext{ConversationID: …, TurnID: "t-must-not-appear", Seq: 42}`; expected payload carries neither. Mirrors the `ThinkingProgress ignores turn addressing` row.
- *zero value maps rather than dropping* — `RateLimited{}` → `ok == true`, payload with `ConversationID` set and every other field at its zero value. This is where an absent-means-now rewrite of `ResetsAt` shows.

**B. New byte-level test, `TestMapEventRateLimitedTruncatedFieldsOnTheWire`** — the AC's mandated pin. Shape lifted from `TestMapEventBackgroundTaskRosterEmptyTasksOnTheWire` (`:402-470`), **assertion polarity inverted**. It marshals the value `MapEvent` *returned*; a test-built payload would prove nothing here, since with no `MarshalJSON` the nil is exactly what the mapping has to have preserved.

- *nil truncation reaches the wire as null* — `TruncatedFields: nil` → `want` `"truncated_fields":null`; `notWant` `"truncated_fields":[]`. **This row is the one that goes red against an allocating mapper**, and it is the mirror image of the template, which *demands* `[]` and *forbids* `null`.
- *populated truncation crosses verbatim and in order* — the control row, so the nil row passes for the right reason: `want` the whole `"truncated_fields":["tf-alpha-sentinel","tf-beta-sentinel"]` literal (one string, so member order is pinned); `notWant` `"truncated_fields":null` and `"truncated_fields":[]`.
- both rows additionally `want` the full `"key":"value"` pairs — `"conversation_id":"cc-conv-sentinel"`, `"status":"qq-status-sentinel"`, `"limit_type":"zz-limittype-sentinel"` — never the bare values. A bare-value needle passes against a mapping that swapped two same-typed neighbours; the keyed form does not.
- `resets_at` asserted as `"resets_at":-1` on one row and `"resets_at":4102444800` on the other. A clamp, a reformat to RFC3339, or an absent-means-now rewrite all go red.

Do **not** add a `TestRateLimitedPayload*` test to `internal/protocol` — #1405 owns that file and its two fixtures already pin the `null`-not-`[]` encoding at the type level. This ticket's obligation is that the *mapping* reaches that encoding with the nil intact.

### AC2 — no turn is opened or closed

Two tests in `cmd/pyry/interactive_turn_v2_test.go`, both directly mirroring the `ThinkingProgress` pair at `:1830` and `:1876`.

**`TestInteractiveTurnEmitterV2_RateLimitedNoLifecycleMutation`** — bare event, non-empty cursor:

- the exact pushed sequence is `[TypeRateLimited]` — assert the whole sequence, not "no `turn_state` present". A `turn_state` anywhere is the observable signature of a `transitionTo`, and an exact-sequence assertion also catches an emit accidentally made unconditional.
- `e.inTurn == false`, `e.turnID == ""`, `e.currentState == ""` after handling.
- then drive a `TextChunk` + `flushDelta` and assert a **fresh** turn opens afterwards (`turn_state:responding` then `assistant_delta`) — proving the frame left the machine in the state a later turn expects.

**`TestInteractiveTurnEmitterV2_RateLimitedMidTurnDoesNotDisturbOpenTurn`** — the one that actually bites:

- open a turn with a `TextChunk{MessageID:"m1"}`, capture `turnID` / `currentState`, assert the precondition `inTurn == true`.
- handle the `RateLimited` event; assert `inTurn` still true and `turnID` / `currentState` unchanged.
- **drive one more event past the frame** (`TextChunk{MessageID:"m2"}` then `TurnEnd`) — this is what catches an `endTurn()`-shaped no-op, which on an already-closed turn is invisible and only shows when the *next* event mints a fresh turn.
- assert the exact envelope order: `turn_state(responding)`, `assistant_delta(a1)`, **`rate_limited` with no surrounding `turn_state`**, `assistant_delta(a2)`, `turn_end`, `turn_state(idle)`. The `a1` delta appearing *before* the frame is the "buffered text keeps its wire position" half of AC2.
- assert both deltas carry the **same** `turn_id` and `seq` `0,1` — a split turn or a disrupted seq is the damage an `endTurn` does.

### AC3 — nothing claude-derived reaches a log

**`TestInteractiveTurnEmitterV2_RateLimitedEventKindNamesTheVariant`** — template at `:1966-2001`, with § 6's handler change.

- empty `stubCursor` so `Handle` takes the `:143-145` no-cursor drop, the reachable `eventKind` call site on this lane.
- logger writes to a `bytes.Buffer` at `LevelDebug` **with `ReplaceAttr` dropping `slog.TimeKey`** (§ 6).
- the event carries the full sentinel set: `Status`, `LimitType`, `TruncatedFields` (both members) and `ResetsAt: 4102444800`.
- assert a log was produced at all (an empty buffer must fail loudly, not vacuously pass).
- **positive:** the log contains `kind=rate_limited`.
- **negative A:** the log does not contain `kind=unknown`.
- **negative B:** for each claude-derived fixture value — `Status`, `LimitType`, both `TruncatedFields` members, and `"4102444800"` — the log does not contain it.

Mutation obligations, both green today, both of which the developer must confirm go **red**:

| mutant | which assertion must catch it |
|---|---|
| the `eventKind` arm removed (falls to `default:`, kind reads `unknown`) | positive + negative A |
| the arm returns claude's `Status` as part of the kind, e.g. `"rate_limited:" + e.Status` | **negative B only** |

The second row is the reason negative B exists and the reason the positive alone does not discharge AC3: `strings.Contains(logs, "kind=rate_limited")` is **still true** when the kind is `rate_limited:qq-status-sentinel`. Verify by mutation rather than by inspection — use `go test -overlay=<abs-path>.json` so the mutants never touch the worktree.

### AC4 — no test

A prose sweep. `make check` does not read English. Verification is the developer re-grepping each of the six sites after the edit and confirming no surviving claim that the frame is unemitted.

### Regression guard

`go test ./... -race` for `internal/turnbridge` and `cmd/pyry`. The two additive arms cannot affect existing rows, but `TestInteractiveTurnEmitterV2_*`'s exact-sequence assertions are the tree's canary for an accidental unconditional emit.

## The stale-claim sweep (AC4)

All six verified present at `e1b1de5` by this spec. Line numbers are as of that commit and will shift as edits land — match on the quoted text, not the number.

| # | site | current claim | action |
|---|---|---|---|
| 1 | `internal/protocol/codes.go:318-321` | "`MapEvent` has **no case** for `turnevent.RateLimited`, so nothing emits this frame yet; the mapping is #1406" | False. Re-state as emitted, mapping landed in **#1410**. The block's next sentence (the `docs/protocol-mobile.md`-lands-with-the-shape rationale) stays true; re-point its `#1406` to `#1410` in the same edit for coherence. |
| 2 | `docs/protocol-mobile.md:448` | frame-table row: "Shape declared by #1405, emitted from #1406; **nothing produces it today**." | False. Re-state as emitted (#1410). Keep the row's `**New in v2** (interactive, capability-gated)` prefix and its section link. |
| 3 | `docs/protocol-mobile.md:931-933` | "**Not emitted yet.** … Nothing in the daemon maps `turnevent.RateLimited` outbound today, so no live traffic carries this frame." | False. Replace with the emitted statement. Keep the surrounding capability/`event_id` sentence at `:928-930` and everything after `:934` — the whole rest of the section (field table, `status`-is-unmeasured rule, `resets_at` hazard, `truncated_fields`, SECURITY) is #1405's and stays. |
| 4 | `docs/knowledge/features/protocol-package.md:862` | "…has no case for the variant, so **nothing emits this frame yet**. The mapping is #1406…" | False. Past-tense to #1410. |
| 5 | `docs/knowledge/features/protocol-package.md:1183` | heading: "**v2 rate-limited vocabulary** (#1405; mapping + `docs/protocol-mobile.md` § `rate_limited` is #1406)" | **Self-contradicting**: the same block's closing sentence (`:1189`) says the doc section landed in #1405. Correct to `(#1405; mapping is #1410)` — the doc-section clause is the false half. Also `:12` ("no producer wired, mapping is #1406") and `:1271` ("#1406 wires the equivalent bridge case") — same class, same fix. |
| 6 | `docs/knowledge/features/turnbridge-package.md` after `:333` | the `MapEvent` table has no `RateLimited` row | Add one beside the `#1394` (`:330-332`) and `#1386` (`:333`) rows, in the same column format: variant `RateLimited` (#1410) → `TypeRateLimited` → the payload literal, `tc.TurnID`/`tc.Seq` ignored, `true`. Name the nil-stays-nil-⇒-`null` behaviour explicitly, since the adjacent roster row states the opposite for identical-looking code. |

### The one site that straddles two tickets — do not past-tense it whole

`docs/knowledge/features/protocol-package.md:1219` lists **three** obligations as "all #1406":

1. `internal/turnbridge/outbound.go`'s `MapEvent` case — **this ticket**, past-tense it to #1410.
2. the untested `eventKind` arm (a #1404 code-review SHOULD FIX) — **this ticket**, past-tense it to #1410. While here, fix the file name: the bullet says the arm lives in `cmd/pyry/interactive_turn_v2_test.go`; the arm is in `cmd/pyry/interactive_turn_v2.go` and it is the *test* that was missing.
3. the live "healthy turn emits zero `RateLimited`" sentinel (#1404's Open Question 1) — **NOT this ticket**. Re-point it at **#1411**, which owns the silence proofs and converts that clause itself. Do not claim it landed.

### Do not touch

- **The two "fourteen turn-stream events" literals** (`docs/protocol-mobile.md:523` and `:979`). Re-verified at `e1b1de5`: exactly 14 `####` frame headings sit between `### Interactive events` (`:521`) and `#### session_transition` (`:977`), which the sentence explicitly excludes. Count *within that range* — there are 39 `####` headings before `session_transition` overall, and counting them all is how a wrong recount gets justified. Emitting an already-documented frame changes no count. The recount trap that caught #1386, #1394 and #1405 in a row does not apply here.
- **The dated changelog entry at `docs/protocol-mobile.md:1636`** — a historical record of what was true on 2026-08-09, not a live claim.
- **Four `#1406` attributions that are not false claims** — `internal/protocol/interactive.go:346`, `cmd/pyry/relay_guard_test.go:150`, `docs/knowledge/features/protocol-package.md:874` and `:1199`. Each names the bridge ticket for something still true after this lands. Re-pointing them at #1410 is welcome but optional; do **not** past-tense them into claims they were never making.
- **Everything #1405 owns**: the wire type, `RateLimitedPayload`, both fixtures, `excludedTypes`, the `compat_test.go` rows, and the `#### rate_limited` section body.
- **`cmd/pyry/stream_turn_busy.go`** — its whitelist comment already reads `DISCHARGED 2026-08-09 (#1404)`.
- **`internal/acpbridge/outbound.go`** — the ACP/desktop lane is #1262, deliberately untouched (same posture as #1394).

### Optional, explicitly not required

- `internal/protocol/codes.go:252-254` — the same false claim for the background-task family ("still returns `ok == false` for all three variants"), untrue since #1394 merged, sitting 66 lines above site 1 in the same file. Correcting it in the same pass is welcome; the rate-limited claims are not optional and this is.
- `cmd/pyry/interactive_turn_v2.go:374-377` — `emitMapped`'s doc comment still enumerates a stale unreachable-drop set (the #1394 code-review NIT, already stale twice over). One line if the developer is already in the function.

Both are cleanups on code this ticket touches, not new scope. Skip either if the turn budget is tight.

## Open questions

1. **`TruncatedFields` fixture values — sentinels or the producer's real names?** The spec picks sentinels (`tf-alpha-sentinel` / `tf-beta-sentinel`) because AC3's leak assertion needs non-colliding needles and `"status"` is too generic to be a trustworthy one. The real producer only ever emits `"status"` and `"limit_type"`, in that order (`parser.go:1255-1259`), and #1405's round-trip test already pins those literals at the protocol layer. The mapping is name-agnostic, so nothing is lost. **Resolved as specified**; flagged only so a reviewer does not read the sentinels as a misunderstanding of the real values.
2. **Whether `…ThinkingProgressEventKindNamesTheVariant`'s measured 2.15% flake deserves its own ticket.** Out of scope here. Record the measurement in `docs/knowledge/codebase/1410.md` (written by the documentation phase, not the developer) so a future flake is recognised rather than re-investigated; file a ticket only if it is actually observed in CI.
3. **Nothing else.** The wire shape, the turn scoping, the gate placement and the opener whitelist are all settled upstream and consumed here unchanged.

## Sizing

**S** — confirmed against the red lines, counted raw.

| red line | limit | this ticket |
|---|---|---|
| new files | > 3 | **0** |
| total written lines | > ~600 | **≈ 410** (see below) |
| new exported types/interfaces | > 5 | **0** |
| consumer call sites needing simultaneous update | > 10 | **0** — both changes are additive `case` arms; no signature change, no type change, no fixture cascade |
| acceptance criteria worth of work | > 5 | **4** |
| distinct error/reject branches | > ~10 | **0 new** — the arm cannot fail; every drop path already exists |
| production `.go` files (commit-time self-check) | ≥ 5 | **3** — `internal/turnbridge/outbound.go`, `cmd/pyry/interactive_turn_v2.go`, `internal/protocol/codes.go` (comment-only) |

**Line projection**, anchored on `bef9c40` (#1386, the same-shaped predecessor), re-verified rather than inherited: that commit is 953 insertions. Removing its 239-line spec and 41-line codebase doc leaves 673 developer-written. Of that, the protocol-shape layer is already landed here by #1405 — `codes.go` +28, `interactive.go` +31, `interactive_test.go` +56, `compat_test.go` +7, fixture +1, `relay_guard_test.go` +5, `protocol-mobile.md` +90 = 218 — and `mapper_test.go` +51 has no analogue (the parser side is #1404's). 673 − 218 − 51 = **404**. This ticket adds a doc sweep #1386 did not have (six sites, mostly single-sentence rewrites) and drops #1386's 90-line new doc section, so ≈ **410** is the working figure. Comfortably under the 600 red line, and consistent with the ticket's own ~407 projection.

**Edit fan-out:** none. Both production changes are new `case` arms inside existing switches whose `default:` already handles the variant; no existing caller, test fixture, or signature moves. The doc sweep is six independent single-site edits with no cascade.

## Security review

**Verdict:** PASS

**What this design actually does, stated adversarially before the walk:** it takes four **claude-authored** values that crossed the subprocess trust boundary at `internal/streamsup/parser.go`'s `json.Unmarshal` of claude's stdout — two model-influenced strings, one model-influenced `int64`, and a slice whose *presence* is a function of claude's input length — and carries all four **out to a remote phone**. Nothing here narrows that exposure; the entire purpose of the ticket is to widen the audience of an already-internal fact. So the question for every category below is: what can claude, or a process that has compromised claude's stdout, make the daemon do or leak by choosing these four values?

**Findings:**

- **[1. Trust boundaries]** No findings. The boundary is a single named one, already existing and not moved: `streamsup.Parser.emitRateLimit` (`parser.go:1227`) is the only place claude's bytes become `turnevent.RateLimited`, and it bounds both strings at construction. Downstream holds a typed value, never raw bytes — the type system carries the signal, since `RateLimited` has no `json.RawMessage` field (unlike `Unrecognized.Raw` and `BackgroundTaskUpdated.Patch`). This spec adds two consumers of that already-parsed type and re-parses nothing. Downstream-caller awareness is documented in three places the design does not weaken: `turnevent.RateLimited`'s doc ("REPORT, never a control input"), `RateLimitedPayload`'s SECURITY paragraph (`interactive.go:379-390`), and the client-facing rules in `docs/protocol-mobile.md` § `rate_limited` (`:944-976` — render `status` as an **opaque label**, MUST NOT branch security-relevant behaviour on it, `resets_at` unvalidated in both directions with "formatting it as a date without a range check is the realistic bug" called out explicitly). The AC4 sweep touches only the false "not emitted yet" claims and leaves every one of those rules intact — that is why site 3's action is scoped to `:931-933` and stops at `:934`.
- **[2. Tokens, secrets, credentials]** Not applicable, by structure rather than by omission. This design generates, stores, compares, and transports no credential. The one adjacent risk — claude's own session identity riding out on a frame — is closed *structurally*: `session_id` and `uuid` never enter `turnevent.RateLimited`'s decode target (#1404), so the payload cannot carry them even if a future contributor tried, and the four `overage*` org-policy keys are absent for the same reason. `ConversationID` is the daemon's own routing key, not a secret, and comes from `sup.CurrentConversation()` — never from anything claude or the phone sends.
- **[3. File operations]** Not applicable. No path is constructed, no file is opened, created, stat-ed, or renamed anywhere in this design. The claude-authored strings never reach a filesystem API, so path traversal, TOCTOU, file mode, symlink and atomic-write questions have no site to attach to. The only persistence any of these bytes touch is `eventring`'s in-memory replay ring (see [8]) — nothing hits disk.
- **[4. Subprocess / external command execution]** Not applicable, and this is the direction that matters: the design consumes subprocess output and **spawns nothing**. No `exec.Command`, no `sh -c`, no environment mutation, no signal delivery. `Status` and `LimitType` are never interpolated into any command, argument, or environment value. This is the strongest single reason a hostile `Status` costs at most one misleading row on a phone: there is no execution path for it to reach.
- **[5. Cryptographic primitives]** Not applicable at the design level; inherited unchanged at the transport level. This design introduces no randomness, no key, no nonce, no comparison against a secret. The frame is sealed by the existing Noise_IK session in `emit`'s fan-out, which this ticket does not touch — no new key, no key reuse, no new nonce sequence. `conversations.NewID()` (`crypto/rand`) is reached only via `startTurnIfNeeded`, which this design explicitly must **not** call (AC2).
- **[6. Network & I/O]** No findings; one deferred observation. Inbound: `TypeRateLimited` is outbound-only, and the two live controls that keep a phone from pushing it into `dispatch.Route` — `compat_test.go`'s v1-rejection row and `relay_guard_test.go`'s `excludedTypes["TypeRateLimited"] = "push"` — both landed in #1405 and are untouched here. No inbound path is added, so no read cap, header check, or timeout is in scope. Outbound sizing is bounded three ways: each string at **256 bytes** (`streamsup.maxRateLimitField`, applied at construction), `TruncatedFields` at **two** members (the producer makes exactly two `bound()` calls), and `ResetsAt` at 8 bytes by type. The design deliberately **re-caps nothing** — a second cap would be a second place the limit is decided and the two could disagree silently. Resource exhaustion: the frame is not a droppable delta (the droppable set is `assistant_delta` only, #610) so it holds a queue slot, but `emitRateLimit` fires **at most once per run**, so no burst path exists — unlike `Unrecognized`, whose burst case the codebase accepts explicitly.
  **OUT OF SCOPE —** `ResetsAt` is an `int64` crossing to a JSON-number wire. Values above 2^53 (9007199254740992) lose precision in any client whose JSON numbers are IEEE-754 doubles, and nothing rejects such a value because nothing validates `ResetsAt` at all — correctly so. The existing client contract's worked range ("negative, zero and year-40000 values", `:951-956`) tops out around 1.2e12 and so does not reach this edge. Not exploitable and not fixable here: the only in-design "fixes" would be a clamp (which the whole design forbids, and rightly) or a string-typed field (which is #1405's declared shape, already merged). Owner is the client contract in `docs/protocol-mobile.md` § `rate_limited`; worth one sentence there in a future ticket, not a gate on this one.
- **[7. Error messages, logs, telemetry]** No findings, and this is the live risk the categories above do not carry — `Status` is precisely the field a log line wants to explain itself with, against a package rule that nothing derived from claude's output reaches a log. All four reachable log sites on this lane were checked and each carries the variant name only, never a field value: `Handle`'s no-cursor drop (`interactive_turn_v2.go:143-145`), `Handle`'s unknown-event default (`:297-299`, no longer reachable for this variant), `emitMapped`'s unmapped branch (`:383-388`, defensive and unreachable after this change), and `emit`'s marshal-failure branch (`:398-408`, which never echoes the payload or `err.Error()` — `encoding/json` quotes invalid input bytes into its error text, which is why). All four route through `eventKind`, whose existing `:501-509` arm returns the variant name only. Upstream is stricter still: `emitRateLimit`'s single drop message carries a `reason` drawn from a closed daemon-authored keyword set, and `Status` never reaches it. This slice adds **no fifth log site**. No metric or telemetry surface exists on this path.
  The residual risk is regression, not design — and it is the reason AC3 exists in the form it does. A mutant returning `"rate_limited:" + Status` from `eventKind` leaves `strings.Contains(logs, "kind=rate_limited")` **true**, so the positive assertion does not discriminate it; only the per-fixture-value negative does. The spec states that explicitly (§ Testing strategy, AC3's mutation table) so the negative cannot later be dropped as redundant, and requires it be verified by mutation rather than by inspection.
- **[8. Concurrency]** No findings. No goroutine, channel, lock, or timer is introduced. `MapEvent` is a pure function — no state, no I/O, callable from any goroutine. `Handle` is documented as not safe for concurrent use and runs only on the producer's single `Run` goroutine (`interactive_turn_v2.go:135-139` and `flushC`'s comment at `:129-133`); the new case is two calls onto that same goroutine. No lock ordering question arises because no lock is taken; `eventring`'s own mutex is inside `Append`, unchanged. Shutdown: the frame is a single `emit` with no partial state to leave behind — an interrupted run loses the frame, which is the same outcome as claude never emitting the line, and nothing on disk is involved.
  One cross-conversation TOCTOU shape was checked concretely rather than assumed, since the new case calls `flushDelta` before `emitMapped` and the two use *different* conversation ids (`e.deltaConvID` vs. the live `convID`). It is safe by an invariant the design does not weaken: the delta buffer is only ever non-empty inside an open turn (`endTurn`'s doc at `:340-343`), and both paths that close a turn flush first — the `TurnEnd` arm at `:214` and the follow-active-switch at `:158-159`, the latter running *before* the type switch and so before the new case. So a `flushDelta` reached from this case with `convID != e.deltaConvID` finds an empty buffer and is a no-op. This is identical to five existing peers and adds no new path.
  Retention: the frame lands in the bounded per-conversation `eventring` (cap `MaxEventsPerConversation = 1024`) and is replayed to a phone that reconnects later, so claude's strings outlive the moment they were produced. That is the ring's designed behaviour for every frame — `unrecognized_message` retains claude's raw bytes and `background_task_started` the literal command line — so this is not a new exposure class, and the bound is unchanged.
- **[9. Threat model alignment]** No findings. Against `docs/protocol-mobile.md` § Security model (`:1366-1447`): **T1 prompt injection** (`high`/`partial`) is the only threat this design touches, and it touches the *return* direction — model-influenced text reaching a human's display. The design's answer is the same one the threat model already relies on and does not weaken it: the daemon never interprets the text, the client contract at `:967-976` names it untrusted model-influenced text, and `status` is required to render as an opaque label. **T3 relay-operator MITM** (`high → low`/`cryptographic`) is unaffected — the frame is sealed under the existing Noise session and the relay sees only opaque ciphertext, exactly as for every other interactive frame. **T7 denial of service** (`low-medium`/`deferred`) is not worsened: at most one non-droppable frame per run against a 1024-event ring. **T2, T4, T5, T6, T8** have no attachment point — no server-id claim, no token handling, no new crypto, no nonce, no static key. No threat is deferred by this design that was not already deferred.

**Not a finding, recorded because a reviewer will reach for it:** the `null`-vs-`[]` distinction on `truncated_fields` is a correctness property with a security-adjacent consequence, not cosmetics — `[]` asserts "nothing was cut" where `null` says "no report", and a mapper that allocates would tell a phone that claude's truncated text is complete. It is pinned by AC1's byte test with the template's polarity inverted (§ Testing strategy, AC1-B), not left to review.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09

## Related

- Issue: [#1410](https://github.com/pyrycode/pyrycode/issues/1410) (split from #1406)
- Blocks: **#1411** — the healthy-run silence proofs at both tiers (hermetic fake-claude and the live realclaude sentinel). Vacuous until this mapping lands, which is why they are a separate ticket. **Do not** add a fake-claude `rate_limit_event` rider, a new `internal/e2e/` file, or a `drainForCompletedTurn` arm here.
- [`codebase/1405.md`](../../knowledge/codebase/1405.md) — the wire shape this consumes; also the "a copied negative pin can invert" lesson, which recurs here as the copied-test-polarity trap
- [`codebase/1394.md`](../../knowledge/codebase/1394.md) — the three-variant precedent for both edit points
- [`codebase/1404.md`](../../knowledge/codebase/1404.md) — the producer, the three-rung gate, the field exclusions
- [`specs/architecture/1386-thinking-progress-mobile-frame.md`](1386-thinking-progress-mobile-frame.md) — the single-variant same-shape predecessor, and this ticket's sizing anchor (`bef9c40`)
- [`features/turnbridge-package.md`](../../knowledge/features/turnbridge-package.md) § `MapEvent` — the table gaining a row
- [`features/protocol-package.md`](../../knowledge/features/protocol-package.md) § Rate-limited event payload — the type's own contract
