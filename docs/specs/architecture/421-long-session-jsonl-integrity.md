# 421 — e2e/realclaude: long-running session JSONL append integrity

## Files to read first

- `internal/e2e/realclaude/fixtures.go:32-78` — `WithWorktree`, `WithWorktreeAuthenticated`, `ReadJSONL`. The new test uses `WithWorktreeAuthenticated` (real-API call). `ReadJSONL` parses through `jsonl.NewReader` which has a 16 MiB cap, NOT the 64 KiB default — long lines do not break the read path.
- `internal/e2e/realclaude/fixtures.go:107-188` — `RunOpts`, `RunResult`, `RunPyryAgentRun`. The new test composes `RunOpts` directly, same way `budget_test.go` does.
- `internal/e2e/realclaude/budget_test.go:114-189` — the closest analogue: a multi-turn Bash-driven test (`TestRealClaude_MaxTurnsHonored`) with a "use the Bash tool five times in sequence, do NOT combine, do NOT chain" prompt. Reuse the steering wording and prompt shape; bump from 5 ops to 10+ and remove the `error_max_turns`-specific assertions.
- `internal/e2e/realclaude/tool_loop_test.go:189-223` — `resultTrailer` struct + `parseResultTrailer`. The new test parses the trailer to assert `num_turns >= 10`.
- `internal/e2e/realclaude/fixtures_test.go:60-102` — `TestWithWorktreeAuthenticated_RealAssistant` shows the EndOfTurn+TextChars idiom on the last assistant event; the new test mirrors and extends it (count ≥10 + last-event check).
- `internal/e2e/realclaude/prompt_fidelity_test.go:75-89` — `jsonlPathFor` helper for failure diagnostics.
- `internal/e2e/realclaude/per_agent_test.go:135-144` — `truncate(b []byte) string` helper used for stderr diagnostics, capped at 1 KiB.
- `internal/agentrun/jsonl/reader.go:40-83` — `Event` struct contract. The fields the test asserts on: `Kind`, `EndOfTurn`, `TextChars`.
- `internal/agentrun/jsonl/reader.go:27-30` — confirms `ReadJSONL`'s underlying buffer is 16 MiB; long-line tolerance on the read side is structural, not a test concern.

## Context

Existing realclaude tests cap at `MaxTurns ∈ {1,2,3,4}`. The ≥10-turn append path through claude's session JSONL is unexercised end-to-end. The classes of regression that would slip through today's suite:

- A future `bufio.Scanner` introduced anywhere in pyry's stdout/stderr handling without a buffer bump → `token too long` at long-turn-count.
- A trailing-newline drift or off-by-one trailer write that only manifests after several appends.
- A scanner reset bug across turn boundaries.
- A buffer flush gap that swallows the last event when a long run terminates.

The test is forward-defensive: today's `parseResultTrailer` / `parseInitSessionID` use the default 64 KiB `bufio.Scanner` buffer but never see lines large enough for the regression to bite. The assertion `stderr does NOT contain "bufio.Scanner: token too long"` is a tripwire for the day someone wires a Scanner into pyry's production stdout/stderr handling and that Scanner hits a long claude line. The substring check is cheap; the regression class is real.

Cost budget per `make e2e-realclaude` run: ~$0.05–$0.10 on `claude-haiku-4-5` / `Effort: low`. Bounded; in line with `budget_test.go`'s cache-hit and max-turns tests.

## Design

### File

One new file: `internal/e2e/realclaude/long_session_test.go`. Build tag `//go:build e2e_realclaude`. No edits to `fixtures.go`, `tool_loop_test.go`, or any other existing file.

### Test function

Single test: `TestRealClaude_LongSessionJSONLIntegrity`. Uses `WithWorktreeAuthenticated` (requires `ANTHROPIC_API_KEY`; otherwise the fixture auto-skips, matching `budget_test.go`'s pattern).

### Seed fixture

Before the run, write a small file under `workdir`:

- Path: `filepath.Join(workdir, "numbers.txt")`.
- Content: ten short lines, one digit per line (`"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"`). Permissions: `0o600`. Failure to write → `t.Fatalf`.

Rationale: the seed exists solely to give Bash one-per-turn things to inspect (`wc -l`, `head`, `tail`, `sort`, `uniq -c`, `cat`, `grep`, …). Content is incidental.

### Run configuration

```go
RunOpts{
    Workdir:      workdir,
    Prompt:       longSessionUserPrompt,
    SystemPrompt: longSessionSystemPrompt,
    AllowedTools: []string{"Bash"},
    MaxTurns:     12,                 // headroom above the ≥10 floor
    Effort:       "low",
    Model:        "claude-haiku-4-5",
    Timeout:      10 * time.Minute,   // 10 turns × ~10–30 s/turn at low effort, with margin
}
```

`Timeout` is bumped from the 5-minute default (`fixtures.go:154`) to 10 minutes. A 10-turn haiku-low run is typically 1–3 minutes wall-clock, but cold network and queue variance can push it; double the default keeps the test from flaking on tail latency.

### Prompt design (seeded-Bash variant, AC-preferred)

Two constants at file scope, mirroring `budget_test.go`'s `maxTurnsSystemPrompt` / `maxTurnsPrompt`:

- `longSessionSystemPrompt`: one-paragraph regression-guard fixture. Instructs: "use the Bash tool one command at a time, wait for each result before continuing, do NOT combine commands, do NOT chain with `&&` or `;`."
- `longSessionUserPrompt`: enumerated list of ten distinct, single-command Bash operations against `numbers.txt`. Suggested operations (developer may reorder for natural model behavior; all must be one-shot single commands):

  1. `wc -l numbers.txt`
  2. `head -n 3 numbers.txt`
  3. `tail -n 3 numbers.txt`
  4. `sort numbers.txt`
  5. `uniq numbers.txt`
  6. `cat numbers.txt`
  7. `grep 5 numbers.txt`
  8. `wc -c numbers.txt`
  9. `awk '{s+=$1} END {print s}' numbers.txt`
  10. `ls -l numbers.txt`

  Each prefixed with its own numbered line. The prompt ends with an explicit "Do NOT combine these. After all ten results, summarize what you saw." — same anti-collapse pattern as `maxTurnsPrompt` (`budget_test.go:131-135`). The "summarize" tail nudges the model to emit a final `assistant`-kinded `end_turn` text block, giving the last-event-EndOfTurn assertion something to land on.

The text-only fallback variant ("list 10 facts about Helsinki, one per turn") is acceptable if the seeded-Bash path proves flaky in practice. The developer should attempt seeded-Bash first and document any flake. Both variants set `AllowedTools: []string{"Bash"}` (the AC notes that even the fallback needs a non-empty allowed-tools entry to satisfy `validateRunOpts`).

### Assertions (in evaluation order)

1. `result.ExitCode == 0`. Failure includes `truncate(result.Stderr)`.
2. `result.SessionID != ""`. Failure includes `truncate(result.Stdout)`.
3. `trailer, err := parseResultTrailer(result.Stdout); err == nil`. Failure includes `truncate(result.Stdout)`.
4. `trailer.NumTurns >= 10`. Failure message names the observed value and the prompt-design recourse ("if the model is collapsing turns, expand the prompt").
5. JSONL walk via `ReadJSONL`:
   - Count assistant events with `EndOfTurn == true`. Must be `>= 10`. On failure, include `jsonlPath`, the observed count, and the total assistant-kind event count.
   - Track the last `assistant`-kinded event encountered. Must satisfy `EndOfTurn && TextChars > 0`. Failure message includes `jsonlPath`, the last event's `EndOfTurn` / `TextChars` values, and the total assistant count — same shape as `per_agent_test.go:128-132`.
6. `!bytes.Contains(result.Stderr, []byte("bufio.Scanner: token too long"))`. Failure includes `truncate(result.Stderr)`. The substring is the literal stdlib error text; do not paraphrase.

### Helper reuse

- `jsonlPathFor(workdir, sessionID)` from `prompt_fidelity_test.go:79` for the path in failure messages.
- `truncate([]byte) string` from `per_agent_test.go:138` for stderr/stdout snippets in failure messages.
- `parseResultTrailer` from `tool_loop_test.go:211` for the result envelope.
- `ReadJSONL` for the JSONL walk.

No new helpers. No new exported types.

### Concurrency model

N/A — single-test, single subprocess, synchronous orchestration via `RunPyryAgentRun`.

### Error handling

Every assertion failure uses `t.Fatalf` with a diagnostic that names (a) the JSONL path or stdout/stderr snippet (truncated to 1 KiB), (b) the observed value, (c) the expected value or threshold. Match the diagnostic style of `budget_test.go` and `tool_loop_test.go` — labelled fields, embedded `path:` lines, no logging of raw byte slices longer than 1 KiB. The `truncate` and `truncateStdout` helpers exist for exactly this; pick whichever the surrounding test file already uses (in this new file: `truncate`).

## Testing strategy

The test itself is the regression sensor. To verify the test works:

- Run `make e2e-realclaude` locally with `ANTHROPIC_API_KEY` set. The new test must pass with no flakes across two consecutive runs.
- If a run produces `trailer.NumTurns < 10`, the prompt is collapsing turns — strengthen the anti-collapse steering or add operations, do NOT lower the threshold.
- If a run produces fewer than 10 `EndOfTurn=true` events but `trailer.NumTurns >= 10`, that is a real find: the JSONL append discipline disagrees with the trailer's turn count. Investigate before mutating the assertion.
- The negative `bufio.Scanner: token too long` assertion is expected to pass trivially today (no production `bufio.Scanner` in the relevant path); its job is to fire the day someone introduces one.

No unit-test counterpart is required. The whole point is end-to-end coverage that no unit test can provide.

## Open questions

- **Cost cadence.** The AC notes ~$0.05–$0.10 per run. If the realclaude suite expands further, the team may want to gate higher-cost tests behind a separate make target (e.g., `make e2e-realclaude-long`). Not in scope for #421; flag if a follow-up ticket lands.
- **Model collapse over time.** A future haiku revision may collapse the seeded-Bash prompt into fewer turns despite the anti-chain steering. If that happens, bump the operation count (e.g., 15 distinct ops) or switch the fallback text-only variant on. Do NOT weaken the `NumTurns >= 10` threshold — that would defeat the test's purpose.

## Self-check (architect, pre-commit)

- Production-source files modified or created (excluding `*_test.go`, `*.md`, the spec itself): **0**. Well under the ≥5 split threshold.
- Edit fan-out: 0 call sites touched (purely additive new test file).
- Red lines: none tripped. Single new test file, ~120 LOC including constants and comments, no new exported types.
- Security-sensitive label: not present on #421. No adversarial pass required.
