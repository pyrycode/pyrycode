# #1386 — Carry thinking progress to a mobile client

**Size:** S (confirmed; see § Size check). **Blocker:** #1385 (merged `ef7ba1f`). **Sibling:** #1394 (merged `b43de44` — its edits are already on `main`, so the collision the ticket flagged is resolved; write against `main` as it stands).

## Files to read first

Turn-1 data load. Read these before writing anything; every design decision below is anchored in one of them.

| Path | What to extract |
|---|---|
| `internal/turnevent/event.go:291-356` | **The source of truth for the doc section.** `ThinkingProgress`'s doc comment + both field comments carry all four measured consumer hazards with their numbers. Port them; do not re-derive or re-measure. |
| `internal/turnbridge/outbound.go:100-114` | `ApiRetry` / `Compacting` arms — the exact shape the new `MapEvent` case copies (conversation identity only, `tc.TurnID`/`tc.Seq` ignored, ints across verbatim). |
| `internal/turnbridge/outbound.go:49-61` | `MapEvent`'s doc comment — states which variants return `ok == false`. Unchanged by this ticket, but read it so you don't contradict it. |
| `cmd/pyry/interactive_turn_v2.go:229-239` | The `ApiRetry, Compacting` handler case — `flushDelta` then `emitMapped`, no lifecycle call. This is AC2's shape verbatim. |
| `cmd/pyry/interactive_turn_v2.go:240-255` | The `Unrecognized` case — the AC's cited `:240`. Same shape, different reason. |
| `cmd/pyry/interactive_turn_v2.go:256-273` | #1394's background-task case. Your case goes immediately after it. |
| `cmd/pyry/interactive_turn_v2.go:435-465` | `eventKind` — add one arm. 8 non-test call sites across 4 files; it feeds log discriminants only. |
| `internal/protocol/codes.go:224-261` | The `TypeUnrecognizedMessage` and background-task const blocks — the house comment style for a new outbound-only v2 type, including the "MUST NOT be added to `inboundAppTypeSet`" paragraph. |
| `internal/protocol/interactive.go:93-117` | `ApiRetryPayload` / `CompactingPayload` — the closest structural peers (bridge-supplied `ConversationID` + plain ints, no `turn_id`). |
| `internal/protocol/compat_test.go:54-61`, `:163-199`, `:229-258` | The **three** insertion sites: rejected row, `v2OnlyTypes` entry, `all`-slice entry. |
| `internal/protocol/testdata/compacting.json` | Fixture shape — one line, `id` / `type` / `ts` / `payload`. |
| `internal/protocol/interactive_test.go:243-262` | `TestCompactingPayload_RoundTrip` — the round-trip test template, ending in `roundTripEnvelope`. |
| `cmd/pyry/relay_guard_test.go:113-141` | `excludedTypes` — the outbound-push block plus #1393's comment explaining *why* an outbound-only type must be listed. |
| `internal/turnbridge/mapper.go:11-53` | `mapEvent` — the PTY surface's **sole** entry into `turnevent`, seven mapped kinds and a `default: return nil, false`. AC4's subject. |
| `internal/turnbridge/mapper_test.go:178-400` | `kindEvent` helper + `TestMapEvent`'s table, incl. its explicit drop rows. |
| `$(go env GOPATH)/pkg/mod/github.com/pyrycode/tui-driver@v1.12.0/pkg/tuidriver/events.go:25-159` | The `EventKind` iota enum — 18 contiguous members, `EventKindUnknown` … `EventKindError`. AC4's loop bound. |
| `cmd/pyry/interactive_turn_v2_test.go:630-760` | Existing status-peer tests (`pushTypes`, bare-envelope assertions) — reuse these helpers, don't invent new ones. |
| `docs/protocol-mobile.md:579-615` | `api_retry` / `compacting` subsections — the fidelity floor for a field table + prose. |
| `docs/protocol-mobile.md:693-740` | #1394's `background_task_started` subsection — the current fidelity ceiling, and the nearest style anchor. |
| `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go:180-200` | `parserIgnoredTypes` / `expectedStreamRunnerOnlySubtypes` — the **already-committed** live pin that ptyrunner emits zero `thinking_tokens`. AC4's premise. **Read only; no edit is owed here.** |

## Context

#1385 shipped `turnevent.ThinkingProgress{EstimatedTokens, EstimatedTokensDelta}` from `streamsup`'s parser, rate-bounded at one event per `minThinkingTokensPerEvent = 64` tokens of accumulated delta. Nothing consumes it. `turnbridge.MapEvent` has no case for the variant, so it returns `ok == false`; upstream of that, `cmd/pyry/interactive_turn_v2.go`'s type switch has no case either, so the event falls to `default:` (`:274`) and is debug-logged as `interactive_turn.unknown` with `kind="unknown"`. Nothing crosses the wire, and a phone showing "thinking" still cannot separate a slow turn from a wedged one.

This ticket adds one outbound v2 frame carrying the two integers plus conversation identity. It is entirely additive: no existing signature changes, no consumer call site is touched, no parser work.

## Design

### The wire name

Wire type constant `TypeThinkingProgress = "thinking_progress"`.

The name comes from the **daemon's variant name** `turnevent.ThinkingProgress`, following the rule #1393's const block already states: the wire follows the variant, so a claude rename lands in one place. It is deliberately **not** `thinking_tokens` and not a transform of it. The shared word "thinking" is incidental — the discriminating word is `progress` (what the daemon reports) versus `tokens` (what claude's subtype is called). A developer tempted to "match claude's vocabulary so clients can correlate" is undoing the translation layer this ticket exists to preserve; AC1's test pins against exactly that drift.

The name also disambiguates against `ThoughtChunk`, which carries the *content* of reasoning and is never forwarded (ADR 025). This frame carries none — a client that renders it as text has nothing to render.

### Payload

```go
type ThinkingProgressPayload struct {
	ConversationID       string `json:"conversation_id"`
	EstimatedTokens      int    `json:"estimated_tokens"`
	EstimatedTokensDelta int    `json:"estimated_tokens_delta"`
}
```

Three fields, all always present (no `omitempty` — the package rule is that boundary values like `0` are explicit on the wire). No `turn_id`. No `TruncatedFields`: the event carries no claude-authored text, nothing is ever cut, and a permanently-nil field would claim a bound that does not exist (`event.go:330-335`). No `MarshalJSON` — nothing to normalise.

The doc comment must state, at `ApiRetryPayload`'s fidelity: bridge-supplied `ConversationID`; not turn-scoped so no `turn_id`; both ints are claude's own readings; and a pointer to `turnevent.ThinkingProgress`'s doc for the non-monotonicity / non-summability hazards rather than restating them (one source of truth, and it is in `turnevent`).

### Data flow

```
streamsup parser  --ThinkingProgress-->  interactive emitter (cmd/pyry)
                                              |
                                     flushDelta(ctx)      <- buffered text keeps its wire position
                                              |
                                     emitMapped(ctx, convID, ev)
                                              |
                                     turnbridge.MapEvent  -> ("thinking_progress", ThinkingProgressPayload)
                                              |
                                     emit() -> non-droppable -> relay -> phone
```

`tc.TurnID` and `tc.Seq` are ignored by the mapping and have no field in the payload — the same posture as `Stall`, `ApiRetry`, `Compacting`, `Unrecognized` and the three background-task frames.

### Turn lifecycle: none

The handler case calls `flushDelta` then `emitMapped`, and **nothing else**. No `startTurnIfNeeded`, no `transitionTo`, no `endTurn`. `inTurn`, `turnID` and `currentState` are untouched.

The reason is specific to this variant and should be in the case comment: thinking progress is a *reading*, not a state transition. The turn's thinking state is already reported by `turn_state: thinking`, driven by `ThoughtChunk` at `:163`. Opening a turn here would be worse than redundant — the parser emits these during an inference request that may not have produced any assistant content yet, so a turn opened on one has no guaranteed end.

`flushDelta` runs first for the same reason as every peer: buffered assistant text logically preceded the reading, so it must keep its wire position ahead of the frame.

### Traffic

Non-droppable (the droppable set is `assistant_delta` only, #610), so these hold queue slots like `stall` and `unrecognized_message`. Count scales with thinking volume at one per 64 tokens of accumulated delta — roughly 1–2 on a typical turn (~10 lines/turn × ~20 tokens/line measured), 8 on the committed heavy capture. There is no per-turn cap and none is added: the rate bound is the cap, and it lives in the producer where #1385 put it.

### Insertion set

Ten sites. The first nine are the ticket's measured table (verified against `main` at `b43de44`); the doc column is expanded — see § Doc sites below.

| Site | Change |
|---|---|
| `internal/protocol/codes.go` | New const block after the background-task block (`:256-258`), with the house "MUST NOT be added to `inboundAppTypeSet`" paragraph. |
| `internal/protocol/interactive.go` | `ThinkingProgressPayload` + doc comment. |
| `internal/protocol/compat_test.go` | Three sites: rejected row in `TestIsKnownAppType` (`:54-61`), entry in `v2OnlyTypes` (`:163-199`), entry in `TestTypeConstants_V1V2Partition`'s `all` slice (`:229-258`). |
| `internal/protocol/testdata/thinking_progress.json` | **New file** — the round-trip fixture. |
| `internal/protocol/interactive_test.go` | `TestThinkingProgressPayload_RoundTrip`. |
| `cmd/pyry/relay_guard_test.go` | `excludedTypes["TypeThinkingProgress"] = "push"`, **in the same commit as the constant** — Assertion #3 of `TestEveryInboundV2TypeHasHandler` enumerates every `Type*` in `codes.go` and fails any unclassified one, so `make check` goes RED the moment the constant exists without it. |
| `internal/turnbridge/outbound.go` | New `case turnevent.ThinkingProgress:` after the roster arm (`:159-188`). |
| `internal/turnbridge/outbound_test.go` | Table rows in `TestMapEventOutbound`. |
| `cmd/pyry/interactive_turn_v2.go` | Handler case after the background-task case (`:256-273`), **and** an `eventKind` arm (`:435`). |
| `cmd/pyry/interactive_turn_v2_test.go` | AC2 + AC3 tests. |
| `internal/turnbridge/mapper_test.go` | AC4 test. |
| `docs/protocol-mobile.md` | Five sites — see below. |

The `eventKind` arm is worth one sentence of justification, because with the handler case added the emitter's `default:` no longer sees this variant: the arm is for the **other** call sites (`acp_turn_stream.go:102,114`, `stream_turn_busy.go:192`, `stream_turn_drain.go:96,231`), where the ACP surface drops this variant via `acpbridge`'s own `default:` and logs the kind. Without the arm those logs read `kind="unknown"` for a variant the daemon does recognize. Return the variant NAME only, per the arm's standing rule — never a claude-derived string (there is none here anyway: both fields are ints).

**`internal/acpbridge/outbound.go` stays untouched.** Its `default:` arm at `:175` means a desktop client silently receives nothing, which is the correct outcome here and the subject of #1262.

## Doc sites

The ticket's table names one doc site. There are **five**, and four of them are corrections that this change makes necessary. Verified against `main` at `b43de44`:

| Line | Current text | Owed |
|---|---|---|
| `:521` | "These **twelve** envelope types form the structured live-session stream." | → **thirteen**. Counted at HEAD: exactly 12 `####` subsections under § Interactive events (`turn_state`, `assistant_delta`, `tool_use`, `tool_result`, `turn_end`, `stall`, `api_retry`, `compacting`, `unrecognized_message`, and #1394's three), with `session_transition` excluded by `:841`'s own wording. Adding this frame makes it thirteen. |
| `:841` | "distinct from the **twelve** turn-stream events above" | → **thirteen**. Same literal, second site. #1394 had to correct exactly this pair when it found them reading "eight"; do not leave the same debt behind. |
| `:646-654` | "**Three of the four** now reach this wire under their own daemon-owned names (#1394) … `thinking_tokens` **still stops at the daemon boundary** — `turnbridge.MapEvent` has no case for it, so it drops there the same way an unmapped subtype does." | This ticket makes the sentence **false**. Rewrite to: all four mapped `system` subtypes now reach the wire under daemon-owned names, `thinking_tokens` as `thinking_progress`. Keep the surrounding argument intact — the ~10/turn rate is still why the silent-drop default is right for every subtype *not* carved out, and none of this changes what surfaces as `unrecognized_message`. |
| `:441-446` | Application-message-types registry table | New row: `` **`thinking_progress`** \| binary → phone \| no \| **New in v2** (interactive, capability-gated). … (#1386). `` #1394's changelog notes that `api_retry`/`compacting` shipped *without* rows here and had to be back-filled; don't repeat that. |
| `:1497` area | Changelog | New dated entry, in the style of the `2026-08-09` / `2026-07-27` entries above it, naming the two corrected count literals. |

Plus the new `#### thinking_progress` subsection, placed after `background_task_roster` and before `session_transition`.

### The subsection's required content

A field table covering all three fields, plus prose stating **each** of the five below. AC5 enumerates them because a client written from the doc alone gets each one wrong by default — this is a checklist, not a summary, and every number is already measured in `internal/turnevent/event.go:291-356`. Port them from there.

| # | Statement | Numbers that must appear |
|---|---|---|
| 1 | Conversation-scoped, **not** turn-scoped (no `turn_id`); receiving one neither opens nor closes a turn. | — |
| 2 | Rate-bounded; the frames **do not enumerate claude's lines** — one per 64 tokens of accumulated delta. | 33 lines → 8 frames on the committed capture |
| 3 | `estimated_tokens` is **not monotonic**: it restarts near zero at every inference-request boundary, so two readings must never be subtracted expecting a non-negative result. | four restarts in the committed capture's single turn: 5→184, 4→167, 3→126, 1→197 |
| 4 | The `estimated_tokens_delta` values a client **receives do not sum to the turn's total** — the rate bound drops most lines, and **no field reports the residue**. | 674 tokens of delta arrived as 243 across 8 frames |
| 5 | **Absence proves nothing, for two distinct reasons that must both be stated:** (a) on a surface that emits none there will never be a frame; (b) *on the emitting surface*, a gap between two frames may only mean the accumulated delta has not yet crossed the bound. A client must not infer a stall from either. | — |

Statement 5 is the one most likely to be half-written. Both halves are load-bearing and they are not the same argument: (a) is about *which surface* is running, (b) is about *timing within* the emitting surface. A doc that states only (a) leaves a client free to build the exact stall inference this frame's rate bound makes invalid.

Do **not** claim in the doc that this frame is a universal liveness indicator. It is proof of life when present and says nothing when absent.

## Testing strategy

Tier: **hermetic unit + package tests only.** No `internal/e2e` work and no `fakeclaude` change.

That is a deliberate call, and the reasoning is worth recording because the ticket flags the alternative. The stream-tier e2e route (`relay_v2_stream_unrecognized_test.go`'s template) would need a new default-off env rider in `internal/e2e/internal/fakeclaude/main.go` to script a `system/thinking_tokens` line, following `writeBogusLines` and its three const/branch sites. Two reasons against:

1. **It buys no criterion.** ACs 1–3 are provable at the `MapEvent` / emitter seam by handing the switch a synthetic `turnevent.ThinkingProgress`, and AC4 is a statement about the *PTY* surface, which the stream-tier harness does not exercise at all. The e2e would re-prove the parser→wire path that #1385's own tests plus these already cover end to end in pieces.
2. **It trips the scope gate.** `internal/e2e/internal/fakeclaude/main.go` is a non-test `.go` file, so it would be a **5th** production source file — the commit-time self-check's split threshold. Adding it would push this ticket over the line for coverage that criterion (1) shows is redundant.

If a future ticket wants the scripted-line rider for its own reasons, it should own that rider outright rather than have this one smuggle it in.

### AC1 — the frame reaches a phone, and the wire literal is the daemon's name

- `internal/turnbridge/outbound_test.go`: table rows in `TestMapEventOutbound` — a `turnevent.ThinkingProgress{EstimatedTokens: N, EstimatedTokensDelta: M}` with a populated `TurnContext` maps to `("thinking_progress", ThinkingProgressPayload{ConversationID, N, M}, true)`. One row must supply a non-empty `TurnID` and non-zero `Seq` and assert they appear **nowhere** in the payload — that is the "not turn-scoped" claim under test, not just under comment.
- `internal/protocol/interactive_test.go`: `TestThinkingProgressPayload_RoundTrip` over `testdata/thinking_progress.json`, asserting `env.Type == TypeThinkingProgress`, both ints decode to the fixture's values, then `roundTripEnvelope`. The fixture is a JSON file holding the literal `"type":"thinking_progress"`, so it pins the string honestly — a constant splice cannot launder it.
- **The anti-drift assertion:** assert `TypeThinkingProgress != "thinking_tokens"` **and** that the constant contains no `"tokens"` substring. Non-vacuous — it goes RED for `thinking_tokens`, `thinking_tokens_progress`, or any other name derived from claude's subtype. Scope it to the type constant only; the *field* names legitimately contain `tokens`.

### AC2 — the frame opens and closes no turn

In `cmd/pyry/interactive_turn_v2_test.go`, reusing the existing `pushTypes` helper and status-peer harness:

- **No turn open.** Feed one `ThinkingProgress` with no turn in progress. Expect exactly one push, of type `thinking_progress`, and **no accompanying `turn_state`** — a `turn_state` in the sequence is the observable signature of a `transitionTo` call. Assert `inTurn` is still false and `turnID` still empty.
- **Mid-turn, with buffered text.** Open a turn, buffer a `TextChunk`, then feed a `ThinkingProgress`. Expect the push order `assistant_delta` → `thinking_progress` (flush precedes emit). Capture `inTurn` / `turnID` / `currentState` before and after and assert all three are unchanged.
- **Drive one event past it.** After the `ThinkingProgress`, feed another `TextChunk` and assert it carries the **same** `turn_id` as the pre-existing turn and continues the same `seq` sequence. This is the assertion that actually bites: a frame-local check passes even if the handler called `endTurn`, because the damage only shows on the *next* event, when a fresh turn gets minted. Without this row, deleting the "no lifecycle mutation" property leaves the package green. (Same failure shape as #1385's unpinned accumulator guard.)

### AC3 — a quiet turn gains no traffic

- Drive a complete ordinary turn (`TextChunk` … `TurnEnd`) with **no** `ThinkingProgress` anywhere and assert the exact pushed type sequence, unchanged from `main`. Asserting the exact sequence, not merely "no `thinking_progress` present", is what catches an emit accidentally made unconditional.

### AC4 — the PTY surface

- `internal/turnbridge/mapper_test.go`: `TestMapEvent_PtySurfaceNeverProducesThinkingProgress`. Loop `k := tuidriver.EventKindUnknown; k <= tuidriver.EventKindError; k++` — the enum is contiguous iota with 18 members. For each `k`: build `kindEvent(k)`, call `mapEvent`; assert the result is never a `turnevent.ThinkingProgress`, and when `ok`, pass it through `MapEvent` and assert the returned type is never `protocol.TypeThinkingProgress`.
  - The loop bound is **not** load-bearing and the test comment should say so: `mapEvent`'s own `default: return nil, false` means any kind outside the range — including one tui-driver adds tomorrow — drops, which is itself AC4's outcome.
  - This is a strict improvement on `TestMapEvent`'s explicit drop rows, which currently miss `EventKindPtyMidResponseErrorShown/Hidden` and `EventKindError`.
- **"Every other frame unchanged"** is discharged structurally: the change is additive, so `TestMapEvent`'s existing rows must remain byte-identical and green. Do not edit them.
- The premise — that ptyrunner emits zero `thinking_tokens` lines — is already pinned live by `expectedStreamRunnerOnlySubtypes` in `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go` (#1385). Cite it in the test comment; **do not edit that file**, and do not run the live suite locally.

### AC5 — docs

`make check` does not lint prose. The five doc sites and the five required statements are checklist items for code review; the § Doc sites tables above are the checklist.

## Error handling

No new failure modes. `MapEvent` is pure and total over its input; the new case cannot fail. The handler case makes no call that returns an error. Both payload fields are ints, so there is no decode path that can produce a partially-valid payload, no cap to exceed, and nothing to truncate.

The one degenerate input is a zero-value `turnevent.ThinkingProgress{}` (both ints 0). It maps and emits normally — `{0, 0}` is a legitimate reading, exactly as `ApiRetry`'s `{0,0}` counter is a legitimate "count unknown". Do not add a suppression branch for it: the producer's rate bound already governs which lines earn an event, and a second, differently-shaped filter in the mapper would silently diverge from it.

## Concurrency model

Unchanged. No new goroutines, no new shared state, no new locks. `MapEvent` stays a pure value-to-value adapter; the handler case runs on the emitter's existing single-consumer goroutine, the same one every peer case runs on.

## Open questions

None blocking. Two notes for the developer:

1. **Placement.** Put the new `case` last in each switch (after #1394's background-task arms) in all three switches — `MapEvent`, the handler, and `eventKind`. Keeps the diff append-only and the three switches in the same order.
2. **The `interactive` capability gate** needs no work. These frames flow through the same `emit()` path as every peer, which is already capability-gated per connection.

## Size check

Verdict **S** — no red line trips. Recorded so code review can audit the call rather than re-derive it.

| Red line | Threshold | This ticket |
|---|---|---|
| New files | > 3 | **1** (`testdata/thinking_progress.json`) |
| Total written LOC (production + tests + helpers + docs) | > ~600 | **~500** projected |
| New exported types | > 5 | **2** (`TypeThinkingProgress`, `ThinkingProgressPayload`) |
| Consumer call sites needing simultaneous update | > 10 | **0** — fully additive; no signature changes |
| Acceptance criteria | > 5 | **5** |
| Distinct error/reject branches | > ~10 | **0** — no state machine, no reject path |
| Production source files (commit-time self-check) | ≥ 5 | **4** — `codes.go`, `interactive.go`, `outbound.go`, `interactive_turn_v2.go` |

Anchors, per the "size against the nearest analogue commit" rule: #1393 put the *protocol-shape* half of three richer frames (free text, byte caps, a custom `MarshalJSON`, a nested struct) through this same set at 886 insertions / 12 files (~295/frame); #1394 put their *mapping* half through at 688 insertions / 4 files (~230/frame) plus 170 doc lines for three subsections and two count corrections. This ticket is **one** frame of two ints, no caps, no marshaling, no nested type, through both halves. Per-half scaling gives ~150 protocol + ~255 mapping/tests + ~90 docs ≈ 500. The projection sits under the 600-line red line but not comfortably, which is precisely why the e2e/`fakeclaude` route is excluded above rather than left to the developer's discretion — adding it would move both the LOC and the production-file count over the line.

The one item that is *not* in the ticket's measured insertion set is the doc-correction fan-out (four correction sites, § Doc sites). It is ~25 lines and does not change the verdict.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX, and the boundary is worth naming because it is the reason this frame is unusually safe. The untrusted→trusted crossing happens entirely upstream in `internal/streamsup/parser.go`, which decodes claude's `system/thinking_tokens` line into two `int` fields and drops everything else on the line. By the time `MapEvent` sees a `turnevent.ThinkingProgress` it holds **no claude-authored strings at all** — the payload's only string is `ConversationID`, which the daemon supplies from its own registry, never from claude. The two ints are claude-controlled *in value* but not in *shape*: a hostile or buggy claude can make them absurd (negative, `MaxInt`), and the consequence is a wrong number on a phone display, not a parse hazard, an injection sink, or an unbounded allocation. Contrast `UnrecognizedMessagePayload.Raw` (`interactive.go:132-140`), which needs a 16 KiB producer cap and an explicit render-as-inert-text warning; this payload needs neither, and the spec must not copy that warning across by pattern-matching — a SECURITY block claiming a bound this frame does not have would be false.
- **[Injection / consumer safety]** No findings. There is no free text, no command line, no path, and no URL in the payload, so the `background_task_started.description` render-never-execute hazard (`docs/protocol-mobile.md:693-740`) has no analogue here. The doc section must not import that language.
- **[Resource exhaustion]** No MUST FIX. These frames are non-droppable and therefore hold relay queue slots, so the honest question is whether claude can force unbounded frame production. It cannot: the producer emits at most one per 64 tokens of accumulated delta (`streamsup.minThinkingTokensPerEvent`), which ties frame count to model work claude must actually pay for — 8 frames on the committed heavy capture. The spec explicitly declines to add a second, mapper-side cap, and that is the safer choice: a duplicate filter would drift from the producer's and produce a bound nobody could reason about from one place. Deliberately unchanged.
- **[Identifier leakage]** No findings, and this is a real category here rather than a formality. claude's `session_id` and `uuid` both appear on the source line and are dropped by the parser before the event exists (#1380/#1385, `event.go:325-328`). `session_id` in particular is claude's session identity, **not** the daemon's conversation identity, and forwarding it would leak a cross-conversation correlator to a phone. The payload has no field capable of carrying either, so the leak is structurally impossible rather than merely avoided — the design decision that makes this category not applicable is the absence of the field, not care at the call site.
- **[Error messages, logs, telemetry]** No findings. The new `eventKind` arm returns the daemon's variant name as a literal and nothing derived from claude's output, per the arm's standing rule (`interactive_turn_v2.go:443-448`). No new log call is added; the handler case logs nothing. The two ints never reach a log.
- **[Concurrency]** No findings. No new goroutines, no new shared state, no new locks; the handler case runs on the emitter's existing single-consumer goroutine and `MapEvent` remains pure. No lock ordering to document, no TOCTOU surface, nothing to leak at shutdown.
- **[Cryptographic primitives]** Not applicable — no randomness, no keys, no comparisons against secrets. The frame rides the existing sealed v2 envelope path unchanged; this ticket introduces no crypto decision.
- **[File operations]** Not applicable — one new file is created, `internal/protocol/testdata/thinking_progress.json`, a static test fixture at a fixed developer-authored path. No runtime path is constructed from any input.
- **[Subprocess execution]** Not applicable — no `exec`, no environment change. The excluded `fakeclaude` env rider (§ Testing strategy) would have touched a test-only subprocess surface; it is out of scope here and any future ticket adopting it owns that review.
- **[Network & I/O]** No findings. No new socket, listener, timeout, or size cap. The frame inherits the existing v2 application-envelope cap of 65519 bytes with enormous headroom — the payload is one conversation id and two integers, so it cannot approach the cap under any input.
- **[Threat model alignment]** Aligned with `docs/protocol-mobile.md` § Security model on the two threats this frame touches: capability-gating (it flows through the existing `emit()` path, so an old phone that did not negotiate `interactive` never receives it — enforced, not assumed, and pinned by the `compat_test.go` rejected row and the `v2OnlyTypes` entry) and inbound/outbound partition (it is outbound-only and must never be routed inbound — enforced by the `excludedTypes` entry in `relay_guard_test.go`, without which `make check` goes red). No threat is deferred.
- **[Doc-correction integrity]** SHOULD FIX, flagged for code review rather than gating. § Doc sites requires rewriting `docs/protocol-mobile.md:646-654`, which currently asserts that `thinking_tokens` stops at the daemon boundary. A partial edit that updates the "three of the four" count but leaves the "still stops at the daemon boundary" clause — or the reverse — produces a document that contradicts itself about what reaches a phone. Both clauses are in the same bullet; review must confirm both moved together.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
