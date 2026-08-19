# #1638 — Map claude's announced model onto the v2 wire and emit it to an interactive client

## Files to read first

Read these before writing anything. The two production edits are one `case` arm each; almost all of the work is in matching the shape of the analogue exactly, and the analogue is in these files.

- `internal/turnbridge/outbound.go` → `MapEvent`, and specifically its `turnevent.RateLimited` arm (the last arm before `default:`). **The insertion template.** Extract: the "conversation identity only — `tc.TurnID` and `tc.Seq` are ignored" framing every conversation-scoped variant repeats, and the house habit of explaining in the arm's comment *what the mapping deliberately does not do*.
- `internal/protocol/interactive.go` → `ModelAnnouncedPayload`. Extract: the three wire fields and their JSON tags, that it has **no** `MarshalJSON`, and the `SECURITY:` paragraph — it already states the posture this mapping must not violate (no sanitization, no re-cap, no charset check, report-never-a-control-input).
- `internal/turnevent/event.go` → `ModelAnnounced`. Extract: the source field names (`Model`, `Truncated`), that `Model` is verbatim and never empty, and that the producer already bounded it at construction.
- `internal/streamsup/parser.go` → `maxModelField`. Extract: the value (256). It is the number the AC-1 over-cap row has to beat; do not re-declare it in `turnbridge`.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`'s `case turnevent.RateLimited:` (**the body template**); `emit` (where the interactive-capability gate actually lives — read it so you do not write a second gate in the new arm); `eventKind`'s `turnevent.ModelAnnounced` arm (the comment AC-4 corrects).
- `internal/turnbridge/outbound_test.go` → `TestMapEventOutbound`. Extract: the shared `tc`, the row struct, and the four `RateLimited` rows as the row template. Also read `TestMapEventRateLimitedTruncatedFieldsOnTheWire` **only** to confirm you are not writing a counterpart — see § Non-goals.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_RateLimitedNoLifecycleMutation` and `TestInteractiveTurnEmitterV2_RateLimitedMidTurnDoesNotDisturbOpenTurn` (the two AC-2 templates, copy the shape); `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak` (the AC-3 extension site — note its `secret*` const block, its failing-push fake, and its `if logs == ""` Fatal); `fakeInteractiveBcast` (`ActiveConns` clamps to the last snapshot in steady state, and `Push` records the attempt *before* returning its error — both matter, see § Testing strategy); `TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant` (prose corrected, assertions/fixture/cursor untouched).
- `cmd/pyry/stream_turn_busy_test.go` → `TestTurnMarkFor_TotalOverEveryVariant`. **Read-only, do not edit.** Its `ModelAnnounced → turnMarkNone` row and its `PermissionRequest` row are the evidence AC-4's new wording rests on.
- `docs/knowledge/features/turnbridge-package.md` § "`MapEvent` — the outbound type switch". Read for the per-variant table your arm joins, so the arm's comment does not contradict it. **Read-only** — the documentation phase owns that file and adds the row.

## Context

`turnevent.ModelAnnounced` has existed since #1600 and its wire shape since #1616, but nothing joins them: `MapEvent`'s `default` returns `ok == false`, so `protocol.TypeModelAnnounced` is a declared frame no daemon ever sends. This ticket adds the two arms that close the gap — one in the pure adapter, one in the interactive v2 emitter — so every interactive v2 client inherits the capability rather than each one reimplementing it.

The value answers a question the daemon cannot answer itself. `ScreenSnapshotPayload` and `SessionSettingsPayload` both carry a field named `model`, and both mean the per-session **override**, where `""` means "no override, inherited default". In the ordinary case the daemon therefore publishes an empty string while claude has named a concrete model on every turn. The daemon knows what it *requested*; only claude knows what it *got*.

The design is fully determined by the #1410 precedent (`9ee56a9`), one variant over. There is no architectural choice left to make here beyond three things this spec decides so the developer does not have to: whether the `Handle` arm merges with `RateLimited`'s (it does not — § Design), what the AC-1 sentinels must look like for the "lowercases / re-caps / substitutes" mutants to actually go red (§ Testing strategy), and the wording of the two comment corrections (§ AC-4). **No ADR is warranted** — this ticket introduces no new pattern; it is the fourth application of one ADR 025 § Phase 2 already records.

## Design

Two additive `case` arms. No new types, no new interfaces, no signature changes, no consumer call-site edits.

### 1. `internal/turnbridge/outbound.go` — the mapping arm

Insert a `case turnevent.ModelAnnounced:` **immediately after** the `turnevent.RateLimited` arm and immediately before `default:`. The contract:

```go
case turnevent.ModelAnnounced:
    return protocol.TypeModelAnnounced, protocol.ModelAnnouncedPayload{
        ConversationID: tc.ConversationID,
        Model:          e.Model,
        Truncated:      e.Truncated,
    }, true
```

That is the whole body. What the arm's comment must establish, in the register the sibling arms use:

- **Conversation identity only.** `tc.TurnID` and `tc.Seq` are ignored and the payload has no field either could land in. The variant-specific reason, which is *not* `RateLimited`'s: a per-turn announcement is a property of the turn's **configuration**, not a turn boundary — claude emits `init` once per turn and `TestTurnMarkFor_TotalOverEveryVariant` already pins the lifecycle answer as `turnMarkNone`.
- **`Model` crosses byte-for-byte.** No lowercasing, no alias expansion, no date-stamping, no family mapping, no lookup against any published model list. Cross-reference `turnevent.ModelAnnounced`'s field doc rather than restating the measurement — that doc is its single source of truth.
- **No re-cap.** The producer bounded the value at construction (`maxModelField`), following `Unrecognized`'s precedent. A second cap here would be a second place the limit is decided and the two could disagree silently. Note that `maxSummaryLen` lives in this same file and is *not* the applicable bound — it is the tool-précis cap, and reaching for it here is the specific mistake to avoid.
- **No charset check.** `internal/relay`'s `validModel` bounds a *phone-supplied override* and is deliberately a different rule; applying it here would reject identifiers claude legitimately announces.
- **`Truncated` is load-bearing.** A payload that dropped it would present claude's cut text to a phone as complete.
- **No suppression branch.** A zero-value `ModelAnnounced` maps rather than dropping. The gate that decides whether an event exists at all is the producer's (its gate does not emit on an empty model); a second, differently-shaped filter here is the hazard the `ThinkingProgress` and `RateLimited` arms both name.

Do **not** write a `MarshalJSON` and do not pre-allocate anything. `ModelAnnouncedPayload` has three scalar fields; the nil-vs-`[]` hazard that `RateLimitedPayload` carries does not exist here.

### 2. `cmd/pyry/interactive_turn_v2.go` — the emitter arm

Insert a `case turnevent.ModelAnnounced:` immediately after `case turnevent.RateLimited:` and immediately before `default:`, mirroring the adapter's ordering. Body, identical to `RateLimited`'s:

```go
e.flushDelta(ctx)
e.emitMapped(ctx, convID, ev)
```

No `startTurnIfNeeded`, no `transitionTo`, no `endTurn`.

**Keep it a separate `case`; do not merge it into `case turnevent.RateLimited:`.** The bodies are identical, and the file *does* merge arms — `ApiRetry, Compacting` and the three background-task variants. But it merges when the variants share a *reason*, and keeps `ThinkingProgress` and `RateLimited` separate despite identical bodies because each has a variant-specific reason. This one does too: a usage limit is a condition of the **account**, an announced model a property of the **turn's configuration**. Follow the file's own precedent.

**Write no capability gate in the arm.** The `interactive` grant is filtered in `emit`, once, for every frame type. AC-2's "no client-type gate within the v2 interactive surface" is satisfied by the arm containing no branch on client type at all — not by adding one that permits everything.

### Data flow

`claude` stdout `system`/`init` line → `streamsup.Parser` (bounds `Model` at `maxModelField`, scrubs invalid UTF-8) → `turnevent.ModelAnnounced` → the drain loop in `stream_turn_drain.go` → `interactiveTurnEmitterV2.Handle` (new arm) → `emitMapped` → `turnbridge.MapEvent` (new arm) → `emit` → `json.Marshal` → `eventring.Ring.Append` (replay retention) → per-interactive-conn `Push` of a sealed `protocol.Envelope`.

The ACP surface is deliberately untouched (#1386): its `default` arm means a desktop client receives nothing there, and that is the correct outcome (#1262). `pyrycode-desktop#559` confirms the desktop reads the v2 mobile wire contract rather than ACP.

## Concurrency model

Nothing changes. The arm adds no goroutine, no channel, no lock, and no field. `Handle` remains single-goroutine by contract (the drain loop in `stream_turn_drain.go` is its sole production caller), so the emitter's unguarded lifecycle fields stay race-free. The arm touches no lifecycle field; its only state interaction is the `flushDelta` call every sibling arm makes, on that same goroutine. `eventring.Ring` is self-synchronised and is reached exactly as it is for every other frame.

## Error handling

There are no new error paths — zero new reject branches. The two pre-existing defensive branches downstream keep their current behaviour and need no change:

- `emit`'s marshal-error branch. `ModelAnnouncedPayload` is a closed two-string-one-bool struct and cannot fail to marshal in practice; the branch already logs neither payload nor `err.Error()`.
- `emitMapped`'s `ok == false` branch. It was unreachable for every variant `Handle` routes here before this ticket and stays unreachable after — this ticket removes one variant from the set that *could* reach it, and adds none.

A `Push` failure on one conn is already non-fatal and does not stop the fan-out; the new frame inherits that. The frame is **not** a droppable delta — the droppable set is `assistant_delta` only (#610) — so it holds a queue slot, matching every other status peer.

## Testing strategy

All hermetic; `make check` is the gate. This ticket touches no file compiled under the `e2e_realclaude` tag. The live proof is #1634, blocked by this ticket.

### AC-1 — `internal/turnbridge/outbound_test.go`: four rows in `TestMapEventOutbound`

Rows only. The assertion machinery already exists and runs `reflect.DeepEqual` against what `MapEvent` **returned**, which is what AC-1's "not on a test-built payload" clause needs.

The sentinel design is the load-bearing part, and it does not come from the analogue. `RateLimited`'s sentinels are short and all-lowercase, which is fine for *its* hazards but leaves two of AC-1's three named mutants green here:

- **Against a lowercasing mapper**, the `Model` sentinel must contain **uppercase letters**. An all-lowercase sentinel survives `strings.ToLower`. "No lowercasing" is a named property of this value, so this is the mutant most worth killing.
- **Against a re-capping mapper**, one row's `Model` must be **longer than 256 runes** — longer than the producer's `maxModelField` *and* than this package's own `maxSummaryLen` (200), so a re-cap at either bound goes red. A short sentinel survives both. Build it with `strings.Repeat` (already imported) plus a readable prefix; keep it out of the short "verbatim" row so that row's failure output stays legible.
- Use no character `encoding/json` escapes, matching the discipline the existing fixtures state.

The rows:

1. **`ModelAnnounced -> model_announced, every field verbatim`** — a short mixed-case `Model` sentinel, `Truncated: true`, the table's shared `tc`. The shared `tc.ConversationID` ("c1") is already distinct from the sentinel, so a mapper that swaps the two fields in either direction goes red without a second sentinel being introduced.
2. **`ModelAnnounced over-cap model crosses uncut`** — the >256-rune value; expected payload carries it whole. This is the row that kills the re-cap mutant.
3. **`ModelAnnounced ignores turn addressing (not turn-scoped)`** — a `tc` carrying a conspicuous `TurnID` and a non-zero `Seq`; the expected payload has no field either could land in. Mirrors the `RateLimited` and `ThinkingProgress` rows.
4. **`ModelAnnounced zero value maps rather than dropping`** — `turnevent.ModelAnnounced{}` with `wantOK: true` and a payload carrying only `ConversationID`. Pins the absence of an empty-model suppression branch, and supplies the `Truncated: false` polarity that row 1's `true` does not.

### AC-2 — `cmd/pyry/interactive_turn_v2_test.go`: two new tests

Copy the two `RateLimited` templates. Reuse the existing `modelAnnouncedFixture` const rather than declaring a new one — it is already a conspicuous sentinel for this variant in this file, and reusing it changes nothing about the test that owns it.

- **`TestInteractiveTurnEmitterV2_ModelAnnouncedNoLifecycleMutation`** — drive the frame bare, before any turn. Assert the exact envelope sequence is `[model_announced]` (a `turn_state` anywhere is the observable signature of a `transitionTo`), then `inTurn == false`, `turnID == ""`, `currentState == ""`. Then drive a `TextChunk` + flush and assert a fresh turn still opens (`[model_announced, turn_state, assistant_delta]` with `turn_state` = `responding`). This is also AC-2's "an event arriving with no turn open still emits exactly one frame".
- **`TestInteractiveTurnEmitterV2_ModelAnnouncedMidTurnDoesNotDisturbOpenTurn`** — open a turn with a buffered `TextChunk`, capture `turnID`/`currentState`, drive the frame, assert both unchanged and `inTurn` still true, then **drive events past the frame** — that is what actually catches an `endTurn`, since an `endTurn` on an open turn only shows its damage on the next event. Assert the full ordering (buffered text flushed *ahead* of the frame, no surrounding `turn_state`), and that both deltas share the pre-frame `turnID` with seq `0,1`.

Do **not** add a per-variant capability-gate test. The gate is `emit`'s and is variant-agnostic; `TestInteractiveTurnEmitterV2_StallFansOutToInteractiveOnly` already pins it.

### AC-3 — extend `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak`

This test already has exactly the right rig: a live cursor, a fake whose single conn always fails `Push` so the log-heavy `push_err` branch fires, and an `if logs == ""` Fatal that is the positive control proving the event traversed that path.

- Add a `secretModel` const to the existing `secret*` block — conspicuous, mixed-case, distinct from the other four. A local const matches that block's convention; do not reach for `modelAnnouncedFixture` here, whose reason for existing is a different one.
- Add `turnevent.ModelAnnounced{Model: secretModel, Truncated: true}` to the driven event slice, **first**. At that point nothing is buffered, so the arm's `flushDelta` is a no-op and the existing five events' behaviour is undisturbed; it also exercises the no-turn-open path for free. Adding a sixth emit is safe against the fake: `ActiveConns` clamps to the last snapshot in steady state rather than running out.
- Add `secretModel` to the existing whole-log `strings.Contains` loop. **This half alone is passed by a `Handle` arm that silently drops the event** — hence the second assertion.
- **The payload assertion, and mind the polarity.** The loop directly above it requires `secretThought` to be **absent** from every payload. This new one requires the model to be **present**: locate the recorded push whose `env.Type == protocol.TypeModelAnnounced`, fail if there is none, decode its payload into `protocol.ModelAnnouncedPayload`, and assert `Model == secretModel` exactly. `fakeInteractiveBcast.Push` records the attempt *before* returning its error, so the envelope is in `pushes` even though that conn's push fails. Decode-and-compare rather than `bytes.Contains`, matching how the sibling tests in this file read payloads. **Importing the neighbouring loop's direction is the trap** — it has now been recorded twice on this frame's lineage, from the producing (#1405) and consuming (#1410) side.
- **Do not assert `kind=model_announced` appears in the log.** It is unsatisfiable on the path AC-3 mandates: `push_err` carries no `kind` field, and the only records that log `eventKind` are the no-cursor drop (which returns before the type switch), `Handle`'s `default` and `emitMapped`'s unmapped branch — the last two of which this ticket makes unreachable for this variant. Both ways to force such an assertion green are wrong: adding a `kind` field to `push_err` is a production change outside this ticket's two files, and switching to an empty cursor proves nothing about the new arm.

### AC-4 — the two in-file comment corrections

Both in the same commit as the code. Each keeps its surviving clause; neither paragraph is deleted wholesale.

**Site 1 — `eventKind`'s `turnevent.ModelAnnounced` arm in `cmd/pyry/interactive_turn_v2.go`.** The first paragraph ("Model is precisely the field #833's posture … is not returned here") is unaffected and stays. The second paragraph's first sentence expires; its last sentence ("Without the arm every `eventKind` site — here, `acp_turn_stream.go`, `stream_turn_busy.go`, `stream_turn_drain.go` — would read `kind=unknown` for a variant the daemon does recognize") is true before and after and must remain. Replace the expiring sentence with wording to this effect:

> Like the two arms above, this variant is now claimed by a `Handle` case on this lane (#1638), so this file's `interactive_turn.unknown` Debug is no longer a live call site for it. Nor for anything else the production producer emits: `Handle` now has an arm for 15 of the 16 `turnevent.Event` implementations, and the one without is `PermissionRequest`, which `streamsup.Parser` never produces — it is PTY/modalbridge-only, as `TestTurnMarkFor_TotalOverEveryVariant` states independently. The reachable `eventKind` call site left on this lane is the no-cursor drop, which returns before the type switch.

Three constraints on however you word it. Say "no variant the production producer emits", not "unreachable" — a nil `Event` still lands in `default`, so the stronger claim is false. Keep the count accurate: 16 implementations (15 markers in `internal/turnevent`'s `event.go`, plus `PermissionRequest` in its `permission.go`), 15 `Handle` arms after this ticket. And the arm now reads exactly like `ThinkingProgress`'s and `RateLimited`'s — "the arm exists for the OTHER call sites" — so "Unlike the two arms above" becomes wrong and must go.

The `PermissionRequest` half of that claim was checked two independent ways, both of which you can re-run in one grep each if you want them fresh: `Handle`'s sole production feeder is the drain loop in `stream_turn_drain.go` (nothing else calls it, and the `startInteractiveTurnStreamV2` the surrounding comments name no longer has a definition — see § Open questions), and that drain is fed by `streamsup.Parser`, whose package contains no reference to `PermissionRequest` at all. The variant's only production constructor is in `internal/modalbridge`, which does not reach this lane.

**Site 2 — `TestInteractiveTurnEmitterV2_ModelAnnouncedEventKindNamesTheVariant`'s doc comment.** Only the first sentence's parenthetical and its "so it lands in `Handle`'s default" clause expire. Everything from "acp_turn_stream.go, stream_turn_busy.go and stream_turn_drain.go log the kind too" onward survives, including "The empty-cursor drop is the reachable `eventKind` call site on this lane" — which this ticket makes *more* precisely true, since with a live cursor the event is now claimed by the `Handle` arm and reaches no `eventKind` site at all. Say that. The corrected sentence should read to the effect of: *the arm exists for `eventKind`'s call sites rather than for this lane's `Handle` case — the variant now has a `Handle` arm (#1638), and this test reaches `eventKind` only because its cursor is empty and `Handle` returns at the no-cursor guard before the type switch.*

**Nothing else in this test moves.** Its assertions, its `modelAnnouncedFixture` sentinel and its empty cursor stay exactly as they are — the empty cursor is what keeps its `eventKind` assertion reachable at all.

## Non-goals — do not touch

- **The repo-wide doc sweep.** Fourteen live sites assert this frame is never emitted and all fourteen expire when this ships. **Twelve of them belong to #1639**, which is blocked by this ticket. This ticket corrects exactly the two that sit inside files it already edits. Specifically leave alone: `internal/protocol/interactive.go`'s `ModelAnnouncedPayload` doc ("Nothing emits it yet"), `internal/protocol/codes.go` around `TypeModelAnnounced`, and `cmd/pyry/relay_guard_test.go`'s `TypeModelAnnounced` entry (whose comment names #1617 as the wiring ticket). A partial sweep here makes #1639's table wrong.
- **`cmd/pyry/stream_turn_busy_test.go`.** Untouched. `TestTurnMarkFor_TotalOverEveryVariant`'s `ModelAnnounced → turnMarkNone` row stays green and its surrounding comment expires nothing.
- **A byte-level nil-vs-`[]` pin.** `RateLimitedPayload`'s counterpart exists because that payload has no `MarshalJSON` *and* a `[]string` field. `ModelAnnouncedPayload` has no slice field at all, so there is no such hazard and no such test.
- **Any knowledge-base doc.** `docs/knowledge/features/turnbridge-package.md`'s `MapEvent` table needs a new row; the documentation phase adds it after code review. Read it, do not write it.
- **Feeding `Model` back to claude.** It is a report, never a control input. Nothing in this ticket creates a path from the announced value to an `exec.Command` argument or a session setting, and none should be added.

## Security review

**Verdict:** PASS

This ticket takes a claude-authored string that has so far never left the daemon and puts it on a network wire. That is the whole change, and it is squarely what the `security-sensitive` label is for. Findings below; each names the symbol it lives in.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is single and explicit, and it is **upstream of everything this ticket writes**: `streamsup.Parser` calls `truncateField` against `maxModelField` at construction, which bounds the value and scrubs the invalid UTF-8 its own cut can produce. Everything downstream — this mapping arm, this `Handle` arm, `emit` — is pure transport of an already-bounded value. Downstream holders are told the data is untrusted by `ModelAnnouncedPayload`'s `SECURITY:` block and by `docs/protocol-mobile.md` § `model_announced`, both of which state that the daemon bounds but does not sanitize and that the render boundary owing sanitization is the **client's**. The design's one real obligation here is *negative* and § Design states it: the arm must add no second cap and no charset check. `internal/relay`'s `validModel` is the trap — it looks like the right hardening and is the wrong rule, because it bounds a **phone-supplied override** (an input the daemon acts on) rather than a **claude-supplied report** (an output the daemon relays), and applying it here would reject identifiers claude legitimately announces.

- **[Error messages, logs, telemetry]** No MUST FIX; this is the category that actually bites, and it is the one AC-3 exists for. The announced model is precisely the value #833's posture — restated in `internal/relay`'s `v2session_settings.go` and `internal/sessions`' `pool.go` as "model / effort / YOLO values are NEVER logged at any level" — keeps out of logs. Three log sites are adjacent to the new arm and none carries the value: `eventKind`'s arm returns the variant name only; `emit`'s marshal-error branch logs neither payload nor `err.Error()`; `emit`'s `push_err` branch logs `conn_id` / `env_id` / `conversation_id` / `turn_id` / `err` and has no `kind` field. The enforcement is belt-and-suspenders with **different fabric**: the belt is prose (this spec plus the arm's comment, both stochastic), the suspenders is `TestInteractiveTurnEmitterV2_NoAppOutputLogLeak`'s whole-log `strings.Contains` over a conspicuous sentinel — deterministic, and it fires on the log-heavy `push_err` path specifically. Note the AC-3 assertion pair is not decorative: log-absence **alone** is passed by a `Handle` arm that silently drops the event, so the payload-presence half is what makes the pair discriminating.

- **[Network & I/O]** No MUST FIX — and specifically, **no new size cap is needed and none should be invented**. The frame is bounded by construction: `Model` ≤ 256 (`maxModelField`), `ConversationID` a 36-byte id, `Truncated` a bool. Even at the pathological ceiling — 256 four-byte runes, every one of them a character `encoding/json` escapes to six bytes — the payload stays under ~6.2 KB against the 65519-byte v2 application-envelope cap. This is why the frame needs nothing resembling `maxDeltaTextBytes`, which exists because `assistant_delta` carries unbounded assistant text; a developer reaching for that pattern here would be adding a second place the limit is decided.

- **[Network & I/O — frequency]** SHOULD FIX: nothing, but state the posture so it is a decision rather than an oversight. The frame is not a droppable delta (the droppable set is `assistant_delta` only, #610), so each one holds a queue slot, and the producer applies **no rate bound** — `TestParser_ModelAnnouncedIsPerLine` pins one event per `init` line with no dedup. A claude emitting many `init` lines therefore produces many small frames. This is the accepted posture the package already documents for `Unrecognized` ("a burst means something is genuinely wrong and you want to see it"), the frames are ~350 bytes, and the source is claude's own line cadence rather than anything network-reachable. Adding a filter here would be the second-differently-shaped-filter hazard the `ThinkingProgress` and `RateLimited` arms both name. Deliberately unbounded, consistent with the siblings.

- **[Tokens, secrets, credentials]** Not applicable, and the reason is structural rather than incidental: this path mints, stores and compares nothing. The arm does not call `startTurnIfNeeded`, so it never reaches `conversations.NewID`'s `crypto/rand` draw; envelope IDs come from the existing `nextID` counter and sealing is the V2SessionManager's, both untouched. One adjacent exposure **is** new and worth naming: `emit` appends every frame to `eventring.Ring` for reconnect-replay, so the announced model now persists in daemon memory bounded by `eventring.MaxEventsPerConversation`. The replay audience is identical to the live audience — authenticated conns holding the `interactive` capability — so this widens no trust boundary; it is recorded because "emit it" quietly implies "retain it" and that is not obvious from the ACs.

- **[Subprocess / external command execution]** Not applicable to what this ticket writes — the mapping and the emitter never exec. The adversarial version of the question is worth recording because the answer constrains **future** work: `Model` is a claude-controlled string, so a consumer that fed it back as a `--model` argument or wrote it into a session setting would hand claude control of the daemon's next spawn. That is exactly what `ModelAnnouncedPayload`'s "a **report**, never a control input" clause forbids. Nothing in this ticket creates such a path, and § Non-goals states that none may be added.

- **[File operations]** Not applicable. The design touches no path, opens no file, and writes nothing to disk. The value never reaches a filesystem name, so path traversal, TOCTOU, file mode and symlink handling have no surface here.

- **[Cryptographic primitives]** Not applicable. No RNG draw, no comparison against a secret, no key or nonce. Envelope sealing happens downstream in the V2SessionManager and is unchanged.

- **[Concurrency]** No findings. The arm adds no goroutine, channel, lock or struct field, so there is no lock ordering to get wrong and nothing to leak. `Handle` keeps its single-goroutine contract, upheld by its sole production feeder (the drain in `stream_turn_drain.go`), which is what lets the emitter's lifecycle fields stay unguarded. The arm mutates none of them; its only state interaction is the `flushDelta` call every sibling arm makes, on that same goroutine. `eventring.Ring` is self-synchronised. Shutdown is unchanged: a mid-teardown `Push` failure returns early on `ctx.Err()` exactly as it does for every other frame.

- **[Threat model alignment]** Aligned with `docs/protocol-mobile.md` § `model_announced`, which already states the three properties this design depends on — the value is bounded but not sanitized, the client owns the render boundary, and the frame is a report rather than a control input. Two threats are explicitly **someone else's**: client-side render sanitization (the mobile/desktop client's, stated in the protocol doc and in `pyrycode-desktop#559`'s contract), and live end-to-end proof that a real claude's announced value survives the path intact (**#1634**, blocked by this ticket).

- **[Semantic hazard — not a security finding, recorded because it looks like one]** `MapEvent` maps a zero-value `ModelAnnounced` rather than dropping it, so a hypothetical future producer emitting an empty `Model` would put `"model":""` on the wire — which collides with the three existing `model` fields where `""` means "no override, inherited default". The mitigation is documentation, already in place across `ModelAnnouncedPayload`, `turnevent.ModelAnnounced` and the protocol doc's four cross-linked rows, and the producer's gate does not emit on an empty model today. Adding a suppression branch to the mapper would be the wrong fix — it would put the gate in two places. Explicitly **not** a MUST FIX; noted so a reviewer who spots the collision can see it was considered.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20

## Open questions

None blocking. One observation, surfaced while verifying AC-4's totality claim and recorded so the next person does not have to re-derive it: several comments on this lane — `interactiveTurnEmitterV2`'s struct doc, `flushDelta`'s doc, and two in `relay.go` — describe the emitter as fed by `startInteractiveTurnStreamV2` on a PTY path, and **that function has no definition anywhere in the tree**; the references are comment-only, presumably left by #1348's deletion of the terminal runner. The sole production feeder is the drain in `stream_turn_drain.go`. Nothing about this ticket's correctness depends on it — the single-goroutine contract those comments assert still holds, and the drain is what upholds it — and it is **not** one of #1639's fourteen sites. Deliberately out of scope: do not fix it here. It is worth its own ticket.
