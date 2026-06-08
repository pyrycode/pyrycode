# Spec #627 — Outbound `turnevent.Event` → v2 wire-envelope mapper (pure adapter)

**Ticket:** [#627](https://github.com/pyrycode/pyrycode/issues/627) — Part of EPIC #596 (Phase 2 structured streaming). The pure-mapping cut #616's body pre-authorized.
**Size:** S (1 new production file, 2 exported types, 0 consumer call sites, 5 ACs, no state machine).
**Not security-sensitive** (confirmed against the label, not the lineage): a pure value-to-value adapter — no untrusted input, no capability decision, no dispatch. The trust boundary lives in #616's capability-gated fan-out, already merged.

## Files to read first

| Path | What to extract |
|---|---|
| `internal/turnbridge/mapper.go` (whole, ~182 LOC) | The **symmetric inbound mapper**. Mirror its file shape exactly: one cohesive file, a pure type-switch returning `(value, ok)`, small pure helpers below it, no logger, no I/O. `outbound.go` is its mirror image (model → wire instead of wire → model). |
| `internal/protocol/interactive.go:16-77` | The five payload structs + their json tags + the **"no omitempty / boundary values explicit"** contract. These are the exact targets you build. |
| `internal/protocol/codes.go:100-104` | `TypeTurnState`, `TypeAssistantDelta`, `TypeToolUse`, `TypeToolResult`, `TypeTurnEnd` — the discriminant strings `MapEvent`/`BuildTurnState` return. |
| `internal/turnevent/event.go:22-92` | The sealed `Event` sum type + the five event structs and their fields: `TextChunk.Text`, `ToolStart.{ToolCallID,Title,RawInput}`, `ToolUpdate.{ToolCallID,Status,Content}`, `TurnEnd.Reason`, `ThoughtChunk.Text`. |
| `internal/turnevent/taxonomy.go:24-43` | `ToolStatus` (→ `is_error`) and `TurnEndReason` (string-backed → `stop_reason` via `string(e.Reason)`). |
| `internal/turnevent/content.go` (whole, 35 LOC) | The sealed `ToolContent` sum type — `TextContent` / `DiffContent` / `TerminalContent`, and `nil` = "no content change". `resultSummary` type-switches over exactly these. |
| `cmd/pyry/assistant_turn_v2.go:120-161` | The **consumer seam** this adapter feeds: how a payload becomes an `Envelope` (`nextID++` mint, `time.Now().UTC()`, `json.Marshal(payload)`, `Push`). Read it to confirm what the adapter must **NOT** do (no ID, no TS, no seal). |
| `docs/protocol-mobile.md:466-514` | Authoritative field tables for the five interactive events; `input_summary`/`result_summary` are a "human-readable **précis** (not the raw input/output)"; the `stop_reason` taxonomy. |
| ADR 025 (`docs/knowledge/decisions/025-…md`) §"The event model" + §"Wire-protocol extension" (lines 91-118, 145-152) | The **ThoughtChunk treatment** rationale: thinking is screen-sourced/brittle and surfaces as a `turn_state` transition; #607 defines no thought-text envelope so the thought text is **not forwarded**. |
| `docs/knowledge/codebase/{606,607,615}.md` | Sibling context: the neutral model (606), the wire types (607), the symmetric inbound producer (615) whose patterns this slice mirrors. |

## Context

#615 shipped the **inbound** half of the Phase 2 bridge (`internal/turnbridge`): it drains the live claude session's `Events()` stream and maps each tui-driver event into the neutral internal `turnevent.Event` model (#606). #607 defined the five v2 interactive wire payloads (`internal/protocol/interactive.go`). #616 (merged) wired the capability-gated fan-out but, per `[[po-capability-gated-consumer-hidden-surfaces]]`, deferred the actual event→envelope **mapping** to this slice.

This slice is the symmetric **outbound** adapter: `turnevent.Event` + explicit turn context → the matching v2 wire payload. Keeping it as **pure functions** (turn context in, payload out — no I/O, no state, no envelope-ID minting, no sealing) makes it table-testable and isolates it cleanly from the turn-lifecycle state machine, which lives in the integration slice (the consumer). Verified on `main` (`f29032d`): `internal/turnbridge` does **not** import `internal/protocol` and no outbound payload construction exists anywhere outside `internal/protocol` itself — this is greenfield-additive.

## Design

### Package & file

New file `internal/turnbridge/outbound.go` in the existing package, alongside `mapper.go`. It adds the package's first `internal/protocol` import. Dependency direction stays clean:

```
cmd/pyry  →  internal/turnbridge  →  { turnevent, protocol, tui-driver }
                                       ▲ outbound.go imports only turnevent + protocol
                                         (no tui-driver — the inbound side owns that)
```

The adapter is the mirror of `mapEvent`: where `mapper.go` does `tui-driver event → turnevent.Event`, `outbound.go` does `turnevent.Event → protocol payload`.

### The seam (what is and is NOT this slice's job)

| Concern | Owner |
|---|---|
| `turnevent.Event` → typed wire payload + type discriminant | **this slice** |
| Summary derivation (input JSON → précis; tool content → précis) | **this slice** |
| Which `conversation_id` / `turn_id` / `seq` / `state` applies to an event | consumer (inputs to this slice) |
| Envelope `ID` minting (per-session monotonic counter) | consumer |
| Envelope `TS` (clock read) | consumer |
| `json.Marshal(payload)` → `Envelope.Payload` and AEAD sealing / `Push` | consumer |
| The drop-log for un-mappable events | consumer (one `log.Debug`, per #615's "pure mapper, drop-logging in the caller") |

This is why the adapter is pure: every clock read, every counter, every I/O lives in the consumer (see `cmd/pyry/assistant_turn_v2.go:142-161` for the existing shape of that consumer wrapping).

### Exported surface (2 types, 2 functions, 3 constants)

**`TurnContext`** — the per-event turn addressing the consumer supplies. The adapter never derives these.

- Fields: `ConversationID string`, `TurnID string`, `Seq int`.
- `Seq` is the per-turn assistant-delta sequence; it is consumed **only** by the `TextChunk → assistant_delta` mapping and ignored for every other event. The consumer advances it.

**`TurnState`** — a string-backed typed vocabulary for `BuildTurnState`, with constants `StateThinking`, `StateResponding`, `StateIdle` (values `"thinking"`, `"responding"`, `"idle"`). The wire field stays a plain string per #607; the typed enum keeps the call site safe. (Constants don't count toward the exported-type budget — same accounting as #607.)

**`MapEvent(ev turnevent.Event, tc TurnContext) (typ string, payload any, ok bool)`**

A type-switch over the sealed `Event`, mirroring `mapEvent`'s `(value, ok)` idiom. Behaviour contract (the developer writes the bodies; these are the invariants the tests pin):

| `ev` concrete type | `typ` | `payload` (all fields from `tc` + the event) | `ok` |
|---|---|---|---|
| `TextChunk` | `TypeAssistantDelta` | `AssistantDeltaPayload{tc.ConversationID, tc.TurnID, tc.Seq, ev.Text}` | true |
| `ToolStart` | `TypeToolUse` | `ToolUsePayload{tc.ConversationID, tc.TurnID, ev.ToolCallID, ev.Title, inputSummary(ev.RawInput)}` | true |
| `ToolUpdate` | `TypeToolResult` | `ToolResultPayload{tc.ConversationID, tc.TurnID, ev.ToolCallID, ev.Status == ToolStatusFailed, resultSummary(ev.Content)}` | true |
| `TurnEnd` | `TypeTurnEnd` | `TurnEndPayload{tc.ConversationID, tc.TurnID, string(ev.Reason)}` | true |
| `ThoughtChunk` | `""` | `nil` | **false** |
| nil / unknown | `""` | `nil` | false |

- `payload` is `any` because the four payload types share no marker interface; it is always one of the four concrete `protocol.*Payload` value structs, or `nil` when `ok` is false. The consumer `json.Marshal`s it directly (same as the existing `MessagePayload` path).
- Pure and zero-value-safe: a nil `ev` falls to the default → drop, exactly like `mapper.go`.

**`BuildTurnState(conversationID string, state TurnState) (typ string, payload protocol.TurnStatePayload)`**

Returns `TypeTurnState` and `TurnStatePayload{conversationID, string(state)}`. Concrete return type (not `any`) because it is monomorphic — the consumer needs no type assertion. The consumer's lifecycle machine decides *which* state applies and calls this; the adapter only shapes the payload.

### ThoughtChunk treatment (AC #4 — confirmed against ADR 025)

ADR 025 §"The event model" maps thinking to `turn_state`, and the brittleness split (§ lines 39-46, 145-152) classes thinking as screen-sourced. #607 ships **no** thought-text envelope. Therefore:

- **`MapEvent` drops `ThoughtChunk`** (`ok == false`) — the thought *text is not forwarded* onto the wire (there is nowhere to put it, and forwarding it is explicitly out of scope).
- The thinking **state** surfaces via `BuildTurnState(convID, StateThinking)`, called by the **consumer's** lifecycle machine when it observes a `ThoughtChunk`. Deciding "a ThoughtChunk means we are now in the thinking state" is a lifecycle decision (`[[ADR-025 phase scoping]]`: state transitions are the integration slice's), so it stays out of the pure mapper. The mapper provides the *builder*; the consumer owns the *decision to call it*.

This keeps the seam intact: if the mapper itself emitted `turn_state{thinking}` for a `ThoughtChunk`, it would be embedding a lifecycle rule and would no longer be a pure value-to-value event mapper.

### Summary derivation (AC #3)

Two unexported pure helpers + a shared truncation helper. The précis is **bounded and single-line** — that boundedness is precisely what makes it "a précis, not the raw input/output" (the field-def language). Define `const maxSummaryLen = 200` (runes) — a phone-display bound, not a wire constraint (the 65519-byte envelope cap is far larger); the exact value is tunable (see Open questions).

**`inputSummary(raw json.RawMessage) string`** — `ToolStart.RawInput` is opaque JSON the model never parsed.
- Empty/nil `raw` → `""`.
- Otherwise compact the JSON (strip insignificant whitespace to one line via `json.Compact` into a `bytes.Buffer`), then `truncate` to `maxSummaryLen`.
- Invalid JSON (compact errors) → `""`. `RawInput` is documented best-effort/opaque (#606); a malformed blob yields an empty précis rather than an error — consistent with `rawInput`'s best-effort posture in `mapper.go`.
- **Deferred refinement (not this slice):** per-tool salient-field extraction (Read→`file_path`, Bash→`command`, Grep→`pattern`) would give a richer human précis, but it couples the adapter to claude tool-input schemas. #615 deliberately kept tool metadata minimal (`toolKind` is "intentionally minimal", `Locations` deferred); this slice holds that line. Flag as an open question, do not build.

**`resultSummary(c turnevent.ToolContent) string`** — exhaustive type-switch over the sealed `ToolContent`:
- `nil` → `""` (the legal status-only `ToolUpdate`).
- `TextContent` → `truncate(c.Text, maxSummaryLen)`.
- `DiffContent` → a short descriptor of the edit, e.g. the `Path` (minimal — see note).
- `TerminalContent` → a short reference, e.g. `"terminal " + c.TerminalID`.
- Exhaustive over the closed sum type so a future producer variant cannot silently vanish. **Note:** the current inbound producer (`mapper.go:toolResultContent`) only ever emits `TextContent` or `nil`; `DiffContent`/`TerminalContent` are not reachable today but are handled because the type is sealed and the cost is ~2 lines + 2 test rows each (a future ACP adapter / refinement may emit them). Keep their renderings deliberately minimal.

**`truncate(s string, max int) string`** — returns `s` unchanged when `utf8.RuneCountInString(s) <= max`; otherwise cuts at `max` runes and appends `"…"`. Rune-aware (not byte-slicing) so multibyte text never splits mid-rune.

### `is_error` derivation

`ToolUpdate.Status` (the ACP `ToolStatus` taxonomy) → the wire bool: `is_error == (status == ToolStatusFailed)`. `completed`/`pending`/`in_progress` all map to `false` (not errors). This round-trips with `mapper.go:toolStatus`, which sets `failed`↔error and `completed`↔success.

## Concurrency model

None. Every function is pure, stateless, synchronous, and safe to call from any goroutine (it reads only its arguments and package-level `const`s). The consumer calls them on its single serial broadcast goroutine (the shape `assistantTurnEmitterV2.Run` already uses). No context, no channels, no mutex.

## Error handling

The adapter returns **no `error`** — consistent with the package's pure-mapper posture (#615: "primitive drops what the model can't hold; it does not invent error envelopes"; #606: "primitive exposes `Valid()`, not errors").

- Un-mappable / nil events → `ok == false`, dropped by the consumer (which owns the single `log.Debug`).
- Malformed `RawInput` → empty `input_summary` (best-effort, opaque pass-through).
- `json.Marshal` of the returned payload is the **consumer's** call and its (defensive, practically-unreachable) error branch already exists at the seam (`assistant_turn_v2.go:126-137`). The adapter does not marshal.

No `time.Time` crosses this adapter (TS is the consumer's), so the round-trip `Equal`-vs-`==` discipline does not apply here.

## Testing strategy

One new `internal/turnbridge/outbound_test.go`, table-driven, stdlib `testing` only, `t.Parallel()`. Assertions compare the returned `typ` (string), `ok` (bool), and `payload` (struct equality via `reflect.DeepEqual` after a type assertion, or field compares). Scenarios (developer writes the rows in the project idiom — these are the cases to cover, not code):

- **`MapEvent` per kind** — one row each for `TextChunk`, `ToolStart`, `ToolUpdate`, `TurnEnd`: assert exact `typ` + every payload field carries the `tc`/event value verbatim.
- **Turn-context plumbing** — a `TextChunk` with `Seq == 0` proves the boundary value reaches `assistant_delta.seq` (not dropped as a zero); a non-zero `Seq` proves pass-through.
- **`is_error` mapping** — `ToolUpdate` with `ToolStatusFailed` → `is_error true`; `ToolStatusCompleted` → `false`; one of `pending`/`in_progress` → `false`.
- **`stop_reason` mapping** — `TurnEnd` for at least `end_turn` and one non-default reason (e.g. `cancelled`) → `string(reason)` verbatim.
- **`ThoughtChunk` → drop** — `ok == false`, `typ == ""`, `payload == nil`; assert no thought text leaks.
- **nil / zero-value `Event` → drop** — `ok == false` (zero-value safety).
- **`inputSummary`** — empty/nil → `""`; small valid JSON → compacted (whitespace stripped, single line); oversized → truncated at `maxSummaryLen` with trailing `"…"`; invalid JSON → `""`.
- **`resultSummary`** — `nil` → `""`; `TextContent` short → verbatim; `TextContent` oversized → truncated; `DiffContent` → its descriptor; `TerminalContent` → `"terminal <id>"`.
- **`truncate`** — under-bound unchanged; exactly-at-bound unchanged; over-bound cut + ellipsis; multibyte string cut on a rune boundary (no broken rune).
- **`BuildTurnState`** — each of `StateThinking`/`StateResponding`/`StateIdle` → `TypeTurnState` + `TurnStatePayload{convID, "<state>"}`.

`make check` (vet + `-race` + staticcheck + substrate-guard) green. The substrate guard scans test files — fixtures must avoid banned claude-screen literals (per #615's note). No `make e2e-realclaude` logic depends on this pure leaf, though the credential-less-sandbox caveat (#607 Lessons) may still surface in review; that is an environment issue, not a diff issue.

## Open questions

1. **`maxSummaryLen` value (200 runes).** A UX/display choice, not load-bearing on the wire. 200 is a reasonable one-line phone bound; the consumer/mobile team may want a different cap. Pick 200 now; revisit if mobile asks.
2. **Richer `input_summary` (per-tool salient-field extraction).** Deferred as above — would couple the adapter to tool-input schemas. Revisit only if the compact-JSON précis proves insufficient in the live phone view (evidence-based: no observed need yet).
3. **`DiffContent`/`TerminalContent` précis shape.** Handled exhaustively but minimally because the current producer never emits them. When a producer (ACP adapter #600, or a refinement) does, the descriptor format can be refined against a real consumer.

## Acceptance criteria → design mapping

- **AC1** (event → matching envelope, all fields carried) → `MapEvent` table above.
- **AC2** (`turn_state` builder: thinking/responding/idle, takes context + target state) → `BuildTurnState` + `TurnState` constants.
- **AC3** (input/result summaries → human-readable précis) → `inputSummary` / `resultSummary` / `truncate`.
- **AC4** (`ThoughtChunk` per ADR 025: turn_state transition, text not forwarded) → ThoughtChunk dropped by `MapEvent`; thinking state via `BuildTurnState`, decided by the consumer.
- **AC5** (pure: no I/O, state, ID-minting, sealing; table-tested over every kind + summaries) → the whole design + the testing strategy.

## Out of scope (consumer / siblings)

- Envelope `ID` minting, `TS`, `json.Marshal`, AEAD sealing, `Push`, the drop-log — the integration consumer (`cmd/pyry`, building on #616's fan-out).
- The turn-lifecycle state machine (when to transition thinking/responding/idle; turn-id assignment; delta-seq advancement; coalescing) — the integration slice.
- `docs/knowledge/codebase/627.md` — written by the documentation phase post-merge, not a developer deliverable.
