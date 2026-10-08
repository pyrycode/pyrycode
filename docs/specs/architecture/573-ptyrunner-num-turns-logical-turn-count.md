# Spec — ptyrunner `num_turns` reports claude's logical-turn count, not raw assistant-event count (#573)

**Size:** XS (architect override of PO's `size:s` — single production file, ~12 LOC, no signature change, zero edit fan-out).

Split from #567. Covers the **reporting** path only (the emitter's `num_turns`). The **enforcement** path (`internal/agentrun/budget` Counter, which double-counts identically via `OnEvent`) is the dependent sibling ticket and is **out of scope here**.

## Context

pyry has two interchangeable agent-run runners. **streamrunner** forwards claude's native `num_turns` from claude's own `type:"result"` envelope. **ptyrunner** (production default) tails the session JSONL and synthesises its own trailer via `internal/agentrun/streamjson`.

They diverge: `emitter.go:189` does `e.numTurns++` for **every** assistant JSONL entry. claude 2.1.158 serialises one logical assistant reply as multiple consecutive assistant entries (a `thinking` line, a `tool_use` line, a `text` line), so the emitter over-counts. The dispatcher logs and acts on `num_turns`, and saw the signal silently change after the ptyrunner cutover.

The fix: count **logical turns** — one per distinct assistant message — matching what claude's native count reports.

### Empirical evidence (do not assume the shape — this was observed)

`permission_protocol_v2.1.158.json` `stdout_events`, annotated:

| idx | type | `message.id` | content blocks |
|----|------|------------|----------------|
| 2 | assistant | `msg_014BuW…` | `[thinking]` |
| 3 | assistant | `msg_014BuW…` | `[tool_use]` |
| 5 | assistant | `msg_01Dbd3…` | `[text]` |

claude's native result: **`num_turns: 2`**. Raw assistant-entry count = 3. Distinct `message.id` count = **2** — exactly claude's logical-turn count.

This also rules out the heuristics the ticket floated as alternatives:
- *"count entries carrying text"* → only idx 5 has text → **1**. Wrong.
- *"thinking-only vs text entry"* → idx 3 (tool_use, no text, no thinking) is unclassifiable. Wrong.
- *grouping by `message.id`* → **2**. Correct.

`message.id` is the robust signal. The session JSONL that ptyrunner actually tails shares the same `message.id` across a turn's split lines (confirmed by the tuidriver docstring cited above); the `stdout_events` capture is the streamrunner side, and the two counts agreeing at 2 is precisely what AC#3's strict-equality e2e assertion verifies live.

## Design

One production file: `internal/agentrun/streamjson/emitter.go`.

### State

Add one unexported field to `Emitter` (alongside `numTurns`, under the existing `mu`):

```go
lastAssistantMsgID string // message.id of the previous assistant entry; "" = none seen yet
```

No new exported symbols, no signature changes, no new package. Concurrency model unchanged — the field is read/written only inside `Emit` while holding `e.mu`, same as `numTurns`.

### Counting rule

Replace the unconditional `e.numTurns++` at `emitter.go:189` with a transition-counted increment. The contract (this *is* the behavioural spec; ~6 lines):

```go
id := ""
if entry.Message != nil {
    id = entry.Message.ID
}
// New logical turn when the message id changes. Empty id is ungroupable
// (synthetic/malformed entries) → counted as its own turn, preserving the
// pre-fix per-entry behaviour for id-less entries.
if id == "" || id != e.lastAssistantMsgID {
    e.numTurns++
}
e.lastAssistantMsgID = id
```

Properties this guarantees, mapped to ACs:

- **AC#1** — consecutive assistant entries sharing one `message.id` (2.1.158's `thinking` line + `text` line of one reply) count as **one** turn. The second entry hits `id == e.lastAssistantMsgID` → no increment.
- **AC#2** — a multi-turn run counts one turn per distinct `message.id`. Intervening non-assistant entries (`user`/`tool_result`) fall outside the `if entry.Type == "assistant"` block, so they never touch `lastAssistantMsgID` and never split a turn whose lines straddle them.
- **AC#5** — only the `num_turns` *value* changes; the trailer field set and key order are untouched (`Close` is not modified).

### empty-id floor (why existing green tests stay green)

Real claude always emits a non-empty `message.id`. Empty ids appear only in synthetic/malformed entries. The `id == ""` short-circuit makes every id-less assistant entry its own turn — identical to the pre-fix per-entry count for those entries. Consequences:

- `TestReadUsage_*` (single id-less assistant entry → `num_turns == 1`): first entry, `id == ""` → increment → 1. **Green.**
- `TestEmit_AggregatesUsage`, `TestEmit_LastStopReasonWins`, `TestTrailer_Result*` (multiple id-less assistant entries, but **none assert `num_turns`**): unaffected.
- `TestCapturedFixture_ByteEquivalence` (fixture = 2 distinct ids): `msg_1` → +1, `msg_2` → +1 → 2, matches fixture result `num_turns: 2`. **Green, unchanged.**

### Grouping-validity assumption (document, don't defend)

Transition-counting (compare to the immediately-previous assistant entry's id) equals distinct-id-counting **iff** one message's lines are never interleaved with another message's lines (pattern `A,B,A`). claude completes one assistant message before starting the next, so this holds. No evidence of interleaving exists; per Evidence-Based Fix Selection, do not add machinery (e.g. a seen-set) to defend a failure mode that hasn't been observed. A one-line code comment noting the assumption is sufficient.

## Concurrency model

Unchanged. `lastAssistantMsgID` is leaf state under the pre-existing `e.mu`, mutated only in `Emit`. No new goroutines, channels, or locks. The package's `MUST NOT log entry content` discipline is preserved — the change reads `entry.Message.ID` into a count decision and never logs it.

## Error handling

No new failure modes. `entry.Message == nil` is handled by the `id := ""` guard (no nil deref). Sticky-`writeErr` and `closed` short-circuits at the top of `Emit` are untouched, so the new state only advances on entries that are actually processed.

## Testing strategy

### Unit — `internal/agentrun/streamjson/emitter_test.go`

1. **Add an id-bearing helper** (AC#4 enabler). Either extend `assistantEntry` with an `id` parameter, or add a small `assistantEntryID(id, rawLine, stopReason string, blockTypes []string)` that sets `Message.ID` and builds `Content` blocks of the given types (e.g. `thinking`, `text`). Keep it in the established table-driven, stdlib-only idiom; the developer chooses the exact shape.

2. **Rewrite `TestEmit_NumTurnsCountsAssistantEvents`** (AC#4) to assert **logical-turn** semantics with a fixture that includes a split thinking+text turn. Scenario to encode (the 2.1.158 shape):
   - assistant `id=msg_A` blocks `[thinking]`
   - assistant `id=msg_A` blocks `[tool_use]`  *(same id — same turn)*
   - `user` (`tool_result`)
   - assistant `id=msg_B` blocks `[text]`  *(new id — new turn)*
   - → assert `num_turns == 2`.
   Add at least one pure split-thinking+text reply (`id=msg_C` `[thinking]` then `id=msg_C` `[text]` → contributes **1**) so the "trivial reply" case from the ticket body is directly pinned. Rename the test if `…CountsAssistantEvents` now misleads (e.g. `TestEmit_NumTurnsCountsLogicalTurns`); update any reference.

3. **Leave `TestReadUsage_*` and `TestCapturedFixture_ByteEquivalence` as-is** — verify they still pass; do not edit the fixture file.

### E2E — `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go` (AC#3)

Replace the `num_turns` relaxation block (`:512-524`) with strict equality plus a sanity floor:
- assert `streamResult.NumTurns == ptyResult.NumTurns` (the two runners must now agree on claude's native logical-turn count); on mismatch, `t.Errorf` with both values.
- keep a `ptyResult.NumTurns < 1` floor so a `0 == 0` "both wedged" outcome still fails loudly.
- update the now-stale comment that says the divergence is expected and tracked by #567.

This test runs live against claude 2.1.158 (build-tagged `realclaude`; not in default `go test ./...`). It is the empirical proof of AC#1/#2/#3 end-to-end.

### Verification commands

```bash
go test ./internal/agentrun/streamjson/...        # unit + captured-fixture
go test -race ./internal/agentrun/streamjson/...
# e2e (requires live claude 2.1.158 on PATH):
go test -tags=realclaude -run TestPtyRunnerVsStreamRunner_StructuralEquivalence ./internal/e2e/realclaude/...
```

## Open questions

- **Shared turn-detection primitive for the budget sibling.** The ticket notes "if a shared turn-detection primitive is extracted here, the sibling consumes it." This spec deliberately does **not** extract one: the change is ~6 lines of consumer-local state, the two consumers (`streamjson.Emitter`'s running count vs. `budget.Counter`'s per-turn budget gate) have different shapes, and there is no natural shared home below both packages (tuidriver — the only common dependency — explicitly declines to own msg_id grouping). Per "Don't define interfaces preemptively," the sibling ticket decides whether to extract once it can see both call shapes; the precise rule above (transition-count on `message.id`, empty-id floor) is documented here for it to mirror verbatim. Not a blocker for this ticket.
