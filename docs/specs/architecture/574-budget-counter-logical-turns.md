# Spec — ptyrunner `--max-turns` budget counts claude's logical turns, not raw assistant events (#574)

**Size:** S (PO's `size:s`, not overridden). 3 production files (one new ~6-LOC helper + two ~6-line call-site changes), one new pure-predicate test, two new `budget_test.go` table cases. No edit fan-out (the helper is a new symbol with exactly one call site per consumer; no signature changes to existing widely-used symbols).

Split from #567. The **enforcement** sibling of #573 (the **reporting** path), which has landed. #573 fixed the identical double-count in `streamjson/emitter.go`'s `num_turns`; this ticket applies the same predicate to the `budget.Counter` so the reported `num_turns` and the enforced budget agree on what a turn is.

## Files to read first

- `internal/agentrun/budget/budget.go:75-83` — `Counter` struct fields. Add one field (`lastAssistantMsgID string`) alongside `count`, under the existing `mu`.
- `internal/agentrun/budget/budget.go:106-151` — `OnEvent`. **The only production change site.** The `entry.Type != "assistant"` early-return at :116 and the unconditional `c.count++` at :120 are what change; the `fired` short-circuit, the `count < MaxTurns` gate, the `AfterFunc` timer arm, and the `Terminate()` call below them stay byte-for-byte unchanged.
- `internal/agentrun/streamjson/emitter.go:80-98` — `Emitter` struct: it already carries `lastAssistantMsgID string` (#573). This is the reference state field; budget mirrors it.
- `internal/agentrun/streamjson/emitter.go:179-225` — `Emit`. The `if entry.Type == "assistant"` block at :189-225 holds #573's inline predicate (:200-207). **Refactor target:** replace the inline `if id == "" || id != e.lastAssistantMsgID` boundary test (:204) with a call to the new shared helper. Everything else in `Emit` (raw passthrough, usage aggregation, `lastStopReason`/`lastAssistantText` capture, sticky-`writeErr`) stays unchanged. emitter's 21 existing tests pin this refactor as behaviour-preserving.
- `internal/agentrun/exitclass.go` — `package agentrun`, the shared home. Lives one level above both `budget` and `streamjson`. `budget` already imports it (`agentrun.ExitErrIsBenign`); `agentrun` imports neither subpackage, so adding `streamjson → agentrun` introduces no cycle. New file `internal/agentrun/turncount.go` joins it.
- `internal/agentrun/budget/budget_test.go:56-58` — `assistantEntry()` helper (returns `Type:"assistant"` with **empty** `Message.ID`). Existing tests use it; under the empty-id floor each empty-id entry is its own turn, so all current budget tests stay green unchanged. Add an `assistantEntryID(id string)` sibling for the new cases.
- `internal/agentrun/budget/budget_test.go:94-150` — `TestOnEvent_NonAssistantKindsDoNotCount` + `TestOnEvent_SIGTERMFiresExactlyAtBudget`. The established table-driven, stdlib-only, `signalRecorder`-based idiom the new cases follow.
- `docs/specs/architecture/573-ptyrunner-num-turns-logical-turn-count.md` — the predicate's empirical basis (the 2.1.158 capture: 3 assistant entries, 2 distinct `message.id`s, native `num_turns: 2`), the empty-id-floor rationale, and the no-interleaving (no `A,B,A`) invariant. The rule this ticket mirrors verbatim. Its "Open questions" § explicitly hands the extract-vs-replicate decision to this ticket.
- `docs/knowledge/codebase/573.md` — the landed implementation summary of the predicate and the "count logical turns by `message.id`" pattern.
- `$(go list -m -f '{{.Dir}}' github.com/pyrycode/tui-driver)/pkg/tuidriver/jsonl.go:108-135` — `JSONLEntry{Type, Message *EntryMessage, ...}` + `EntryMessage{ID string, ...}`. Confirms `entry.Message.ID` reachability (nil `Message` on synthetic entries → the `id := ""` guard). No tuidriver change needed.

## Context

ptyrunner enforces `--max-turns` itself because interactive `claude` (unlike `claude -p`) does not stop natively at the cap. `budget.Counter.OnEvent` increments `c.count` for **every** assistant JSONL entry and fires SIGTERM (then SIGKILL after the grace window) when `count` reaches `MaxTurns`.

claude 2.1.158 serialises one logical reply as multiple consecutive assistant entries sharing one `message.id` (a `thinking` line, a `tool_use` line, a `text` line). The Counter charges each against the budget, so one logical turn costs 2–3, and a `--max-turns=N` cap is reached ~twice as fast on ptyrunner as on the streamrunner baseline. #561 worked around the symptom by bumping test `maxTurns` ceilings; this is the root cause.

#573 fixed the identical over-count on the reporting path (`emitter.go`'s `num_turns`) by counting **distinct consecutive assistant `message.id`s**. This ticket applies the same predicate to the budget Counter so `num_turns` (reported) and the budget (enforced) cannot disagree on what a turn is, and so the cap means N model turns regardless of how claude chunks a reply.

## Design

### The shared predicate (drift-prevention decision)

The drift-prevention AC is explicit, and the ticket leaves the mechanism to the architect (extract a shared predicate, or replicate with a load-bearing cross-reference comment). **This spec extracts a shared predicate.** Rationale:

1. **The two copies must be bit-identical forever — divergence is always a bug.** The entire purpose of this ticket is that reported `num_turns` and the enforced budget agree on "what is a turn." That is the *opposite* of the registry-recipe duplication the codebase deliberately tolerates (`PROJECT-MEMORY.md` § "Atomic-write recipe duplicated until a fifth registry forces extraction"), where divergence is an *expected* future need. Here a single source of truth is correct precisely because divergence must never happen.
2. **#573 deferred this exact decision to this ticket.** Its "Open questions" § says: *"the sibling ticket decides whether to extract once it can see both call shapes ... the precise rule above ... is documented here for it to mirror verbatim."* Both shapes are now visible (`Emitter`'s running `numTurns` count vs. `Counter`'s `count` budget gate) and the boundary predicate is identical — they differ only in the increment target.
3. **#573's sole objection to extraction dissolves.** It found "no natural shared home below both packages (tuidriver declines `msg_id` grouping)." Expressing the predicate over two **strings** (`currentID, lastID`) rather than over a `JSONLEntry` sidesteps tuidriver entirely. The home is `internal/agentrun` — already imported by `budget`, the parent package of `streamjson`, and free of any tuidriver dependency. No import cycle (`agentrun` imports neither subpackage).
4. **A shared function is a deterministic drift guarantee; a cross-reference comment is advisory.** The explicit "cannot drift" AC is better served by code-level enforcement (architect principle: a safety net should be deterministic, not another stochastic rule).

New file `internal/agentrun/turncount.go`, one exported pure function:

```go
// IsNewLogicalTurn reports whether an assistant entry with message id
// currentID begins a new logical turn, given lastID (the previous assistant
// entry's id; "" before any assistant entry). [contract — ~1 line of body]
func IsNewLogicalTurn(currentID, lastID string) bool
```

Behaviour (the whole turn-boundary definition lives here, in one place):
- `currentID == ""` → **true** (empty id is ungroupable → its own turn; the empty-id floor, matching #573).
- `currentID != lastID` (both non-empty) → **true** (the `message.id` changed → new turn).
- `currentID == lastID` (non-empty) → **false** (same logical reply split across entries → same turn).

The id-extraction (`entry.Message != nil` nil-guard) is *not* the drift-prone part — it is trivial nil-safety, orthogonal to the turn definition — so it stays inline in each consumer. Keeping the helper pure-over-strings is what keeps `agentrun` free of the tuidriver dependency and makes the predicate trivially unit-testable without constructing entries.

### `budget.Counter` change

- **State:** add `lastAssistantMsgID string` to the `Counter` struct (alongside `count`, under the existing `mu`; `""` = no assistant entry seen yet). Mirrors `Emitter`.
- **`OnEvent`:** extract `id` from `entry.Message` (nil-guarded), then under `c.mu` increment `c.count` only when `agentrun.IsNewLogicalTurn(id, c.lastAssistantMsgID)`, and store `c.lastAssistantMsgID = id` — before the existing `fired` short-circuit and `count < MaxTurns` gate. Contract shape (~6 lines, replaces the unconditional `c.count++` at :120):

  ```go
  // (after the entry.Type != "assistant" early-return, before the lock)
  id := ""
  if entry.Message != nil {
      id = entry.Message.ID
  }
  c.mu.Lock()
  if agentrun.IsNewLogicalTurn(id, c.lastAssistantMsgID) {
      c.count++
  }
  c.lastAssistantMsgID = id
  // ... unchanged from here: if c.fired { ... }; if c.count < MaxTurns { ... }; fire path
  ```

  `OnEndOfTurn`, `Reason`, `Stop`, `killAfterGrace`, and the SIGTERM→SIGKILL escalation are **unaffected** — only what increments `c.count` changes.

Interleaved non-assistant entries (`user`/`tool_result`) hit the `entry.Type != "assistant"` early-return, so they never touch `lastAssistantMsgID` and cannot split a turn whose lines straddle them (AC#1, second sentence). When the budget fires on the first entry of a turn, subsequent same-id entries of that turn hit `id == lastID` → no increment, and `fired` short-circuits anyway — no double-fire.

### `streamjson.Emitter` refactor

Replace the inline boundary test at `emitter.go:204` (`if id == "" || id != e.lastAssistantMsgID`) with `if agentrun.IsNewLogicalTurn(id, e.lastAssistantMsgID)`. The `id`-extraction lines above it (:200-203) and the `e.lastAssistantMsgID = id` line below (:207) stay. Add the `internal/agentrun` import. This is a behaviour-preserving refactor; emitter's existing tests (incl. `TestEmit_NumTurnsCountsLogicalTurns` and `TestCapturedFixture_ByteEquivalence`) pin it green with no test edits.

## Concurrency model

Unchanged. `budget`'s new `lastAssistantMsgID` is leaf state under the pre-existing `c.mu`, read/written only in `OnEvent` (same as `count`). `IsNewLogicalTurn` is a pure function — no state, no locks, safe for any concurrent caller. No new goroutines or channels. The package's no-log-entry-content discipline holds: the change reads `entry.Message.ID` into a count decision and never logs it.

## Error handling

No new failure modes. `entry.Message == nil` (synthetic/malformed entries) is handled by the `id := ""` guard (no nil deref) → empty-id floor → its own turn. The `fired` / `count < MaxTurns` short-circuits and the grace-timer escalation are untouched.

## Testing strategy

### New — `internal/agentrun/turncount_test.go` (pure predicate)

Table-driven, stdlib only. Cases for `IsNewLogicalTurn(currentID, lastID)`:
- `("", "")` → true (first entry, empty id).
- `("", "msg_A")` → true (empty id is always its own turn).
- `("msg_A", "")` → true (first non-empty id).
- `("msg_B", "msg_A")` → true (id changed → new turn).
- `("msg_A", "msg_A")` → false (same id → same turn).

### Extend — `internal/agentrun/budget/budget_test.go`

Add an `assistantEntryID(id string)` helper (returns `tuidriver.JSONLEntry{Type:"assistant", Message: &tuidriver.EntryMessage{ID: id}}`), then two table-driven cases (reusing `signalRecorder` / `mustNew`):

- **Split-reply counts as one turn (AC#1).** `MaxTurns=2`, `GracePeriod` short. Feed: `id=msg_A [thinking]`, `id=msg_A [tool_use]` (same id), a `user` (`tool_result`) entry interleaved, then `id=msg_B [text]`. Assert Terminate is **not** called after the first three (only 1 logical turn so far), and **is** called exactly once after `msg_B` (the 2nd logical turn reaches `MaxTurns`). Pins both AC#1 sentences (split reply = 1 turn; interleaved non-assistant doesn't split).
- **K logical turns span more than K entries (AC#2).** `MaxTurns=K` (e.g. 3). Build a stream where each logical turn is `id=msg_i [thinking]` + `id=msg_i [tool_use]` (2 entries per turn, > K entries total for K turns). Assert Terminate fires only after the K-th distinct `message.id` first appears — not after `MaxTurns` raw assistant entries (it would have fired at entry 3 = mid-turn-2 under the old per-entry count).
- **No-regression for one-entry-per-reply + empty-id floor (AC#5).** Verify the existing `assistantEntry()`-based tests (empty id) still pass unchanged: each empty-id entry is its own turn, so `TestOnEvent_SIGTERMFiresExactlyAtBudget` (3× empty-id, `MaxTurns=3`) still fires at the 3rd. Optionally add an explicit one-distinct-id-per-entry case (`msg_A`, `msg_B`, `msg_C`, `MaxTurns=3`) asserting it fires at the 3rd — one entry per distinct id still counts as one turn each.

### Verification commands

```bash
go test ./internal/agentrun/...
go test -race ./internal/agentrun/...
go vet ./...
```

## Open questions

- **None blocking.** The extract-vs-replicate decision (deferred to this ticket by #573) is resolved above in favour of extraction. If a future claude build ever interleaves message ids (`A,B,A`) — currently unobserved and ruled out by #573's no-interleaving invariant — transition-counting would over-count; per Evidence-Based Fix Selection, do not add a seen-set to defend a failure mode that has not occurred. The shared `IsNewLogicalTurn` is the single place such a change would land if it ever becomes necessary, which is itself part of the value of extracting it.
