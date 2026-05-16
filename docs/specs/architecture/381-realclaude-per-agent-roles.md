# Spec — #381: e2e/realclaude per-agent role smoke tests

## Files to read first

- `internal/e2e/realclaude/fixtures.go:32-37, 42-61, 66-86, 92-142` — `WithWorktree`, `RunOpts`, `RunResult`, `RunPyryAgentRun`, `ReadJSONL`. The fixtures you build on. Do not modify; do not widen export surface.
- `internal/e2e/realclaude/tool_loop_test.go:174-202` — `resultTrailer` struct + `parseResultTrailer(stdout)`. Both are package-private but live in the SAME test package (`realclaude`) — you can call them directly from `per_agent_test.go`. Do NOT duplicate them.
- `internal/e2e/realclaude/allowed_tools_enforcement_test.go:27-71` — pattern for `RunPyryAgentRun` + `ReadJSONL` + assertion-with-truncated-diagnostics. Mirror the failure-message style (truncate stdout/stderr to ~1024 bytes before embedding in `t.Fatalf`).
- `internal/e2e/realclaude/prompt_fidelity_test.go:79-89` — `jsonlPathFor(workdir, sessionID)` for diagnostic path strings on JSONL-related failures. Already in package; reuse.
- `internal/agentrun/jsonl/reader.go:40-65` — `Event` field semantics, especially the `EndOfTurn` and `TextChars` fields and the rule that transitional `end_turn` envelopes (thinking-block resolutions) have `EndOfTurn=false`. Read this before writing the "last assistant event" assertion.
- `agents/dispatcher/src/dispatch.ts:1183-1186` (in the sibling repo `agent-dispatcher`, not in this repo) — `baseTools` literal + `needsAgent = ["architect", "code-review"]` membership check. The line numbers in the ticket body (1190-1193) are stale; use these. Source of truth for the constant the helper mirrors.
- Ticket #381 issue body — acceptance criteria, the explicit five test names, the "do NOT factor into a table-driven loop" instruction.

## Context

Pyrycode's `internal/e2e/realclaude/` suite already covers shared infrastructure: tool-loop integrity (#376), allowed-tools enforcement (#365), prompt fidelity (#364). All three exercise pyry's agent-run path with a single representative configuration. The failure mode they don't cover is per-role drift: one dispatcher role's allowed-tools list or system-prompt shape regresses while the others remain green. The 2026-05-14 `/doctor` prompt-poisoning bug failed all five roles together; the next failure of this class will likely hit one role only.

This ticket adds five named smoke tests — one per dispatcher role (`po`, `architect`, `developer`, `code-review`, `documentation`) — that run `pyry agent-run` with that role's `(allowedTools, system-prompt shape)` combination against the real claude binary. Five separate top-level functions, not a table loop, so the nightly board surfaces five independent pass/fail signals.

The system prompts inlined in the tests are **deliberately stand-ins** — 3-5 lines naming the role and pointing at its tool surface. They are NOT verbatim copies of `agents/<role>/CLAUDE.md`. The goal is to exercise the wiring between dispatcher-shaped (allowed-tools, prompt-shape) inputs and pyry's agent-run path; not to validate operator prompts. Operator prompts are large, frequently revised, and would make this suite both expensive and falsely sensitive to prompt-edit churn.

## Design

### File layout

One new file. No production-code changes.

```
internal/e2e/realclaude/per_agent_test.go    NEW (~150 LoC including imports)
```

Build tag: `//go:build e2e_realclaude`. Package: `realclaude`. Same package as the existing fixtures and helper tests, so all package-private symbols (`parseResultTrailer`, `resultTrailer`, `jsonlPathFor`) are accessible without re-export.

### File contract

The file declares (all package-private except the test functions, which are package-private by Go test convention):

1. **A `dispatcherBaseTools` constant** — the comma-split list mirrored from `agent-dispatcher/src/dispatch.ts:1183`. Stored as `[]string` (not a comma-joined string) so the helper can return it directly. Single-line per-element list with a Go raw-string-style comment above citing `agents/dispatcher/src/dispatch.ts:1183` (yes, the relative path other agents use; the spec note above documents the actual repo location for readers). The comment cite is the *seam a future renumber lands a reviewer at*, per AC.

2. **A `dispatcherAllowedToolsForRole(role string) []string` helper.** Returns `dispatcherBaseTools` for `"po"`, `"developer"`, `"documentation"`. Returns `dispatcherBaseTools` with `"Agent"` appended for `"architect"` and `"code-review"`. Unknown role → `t.Fatalf` is NOT possible (no `t` here); instead, panic with a descriptive message — these are tests, the only callers are the five test bodies in this file, and a typo there is a programmer error, not a runtime condition. (Alternative considered: return `(tools, ok)` and have each test assert `ok`; rejected as ceremony for a 5-call-site helper.)

3. **Five top-level test functions**, each named exactly as the AC states:
   - `TestRealClaude_PO_RoleLoop`
   - `TestRealClaude_Architect_RoleLoop`
   - `TestRealClaude_Developer_RoleLoop`
   - `TestRealClaude_CodeReview_RoleLoop`
   - `TestRealClaude_Documentation_RoleLoop`

   All five share the same skeleton (see *Test skeleton* below). The five system prompts and five user prompts are inlined per function as string literals — NOT extracted to a table. The "do not factor into a table-driven loop" AC is binding; five named functions give five rows on the nightly board.

4. **A short header comment** above the five tests explaining: (a) why the five-function shape and not a table (per-role pass/fail signal); (b) why the inlined system prompts are stand-ins, NOT copies from `agents/<role>/CLAUDE.md` (cost + churn-sensitivity); (c) why `Agent` is appended for architect/code-review only (mirrors dispatcher's `needsAgent` membership check).

### Test skeleton (applies identically to all five)

Every test follows this exact shape — describing inputs, assertions, and failure-diagnostic style, NOT the line-by-line code (developer writes it in the project's idiom):

- **Setup**: `workdir := WithWorktree(t)`. No file seeding (unlike `tool_loop_test.go`); these tests don't exercise tool calls, just the loop wiring.
- **Run**: call `RunPyryAgentRun(t, RunOpts{...})` with:
  - `Workdir: workdir`
  - `Prompt`: the role-specific one-shot user prompt (see *Per-test prompts* below)
  - `SystemPrompt`: the role-specific 3-5 line stand-in system prompt (see *Per-test prompts* below)
  - `AllowedTools: dispatcherAllowedToolsForRole("<role>")`
  - `MaxTurns: 4`
  - `Effort: "low"`
  - `Model: "claude-haiku-4-5"`
  - No `Timeout` override — the 5-minute default is the runaway guard.
- **Assertions**, in this order, with failure messages embedding truncated stdout/stderr (≤1024 bytes each, suffixed `... (truncated)`) so the operator can diagnose without re-running:
  1. `result.ExitCode == 0` (mirror `allowed_tools_enforcement_test.go:40-42` failure-message style — "ExitCode = %d, want 0\nstderr:\n%s").
  2. `result.SessionID != ""` (mirror the same file's :43-51 stdout-truncation pattern).
  3. Parse the trailer with `parseResultTrailer(result.Stdout)`. On error, `t.Fatalf` with the parse error and truncated stdout.
  4. Assert `trailer.PermissionDenials == nil || len(*trailer.PermissionDenials) == 0`. The pointer-vs-empty-slice distinction is documented in `tool_loop_test.go:178-184` — preserve it; do not collapse to `len(deref) == 0` without the nil-check, that would panic on the absent-field case.
  5. Assert `trailer.NumTurns >= 1`. (#376's tool-loop test asserts `>= 2` because it expects a tool call + final text; here a single-shot prompt with no tools is the realistic minimum, hence `>= 1`.)
  6. Read JSONL: `events := ReadJSONL(t, workdir, result.SessionID)`. Find the LAST event with `Kind == "assistant"`. Assert `event.EndOfTurn == true` AND `event.TextChars > 0`. On failure, embed `jsonlPathFor(workdir, result.SessionID)` and the count of assistant events seen.

### Per-test prompts

The exact prose is the developer's call as long as the response shape (single end-of-turn assistant text, ≤4 turns, no tool calls) is the realistic expectation. Suggested wording — one line of system + one line of user per role — meeting the AC's "3-5 line" stand-in cap and the AC's per-role task hint:

| Role            | System prompt (stand-in)                                                                                              | User prompt                                                                                       |
|-----------------|-----------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------|
| `po`            | "You are a Pyrycode product-owner agent. You refine issue bodies. Use Read/Edit/qmd/codegraph for research."          | "Rewrite this one-line ticket title in active voice: 'The CLI is crashed by malformed flags.'"    |
| `architect`     | "You are a Pyrycode architect agent. You produce implementation specs. Use Read/Grep/codegraph; delegate via Agent."  | "Produce a five-line implementation sketch for: a Go function that returns the SHA-256 of stdin." |
| `developer`     | "You are a Pyrycode developer agent. You implement specs. Use Read/Edit/Bash to write and verify Go code."            | "Implement a Go function `Add(a, b int) int` that returns a+b. Reply with the function only."    |
| `code-review`   | "You are a Pyrycode code-review agent. You review patches for correctness. Use Read/Grep/Agent for cross-file lookups." | "Review this three-line patch for obvious issues: `func Add(a, b int) int { return a - b }`."    |
| `documentation` | "You are a Pyrycode documentation agent. You summarize changes. Use Read/qmd to extract context."                     | "Summarize this one-line commit message in one sentence: 'fix(supervisor): handle SIGWINCH.'"    |

These are concrete suggestions, not contractual; the developer adjusts wording to keep the 3-5 line cap and the single-shot, no-tool-call shape. The contract is: each test's prompt pair MUST resolve in ≤4 turns with haiku at low effort, with the response being a single end-of-turn assistant text block (no tool_use). If a chosen prompt provokes the model into trying to call a tool, rewrite it.

## Concurrency model

Not applicable. These are sequential `testing.T` tests; `RunPyryAgentRun` is synchronous; no goroutines introduced. `go test` may run the five in parallel by default if `-parallel` is set, but the existing realclaude tests do not call `t.Parallel()` (they each spawn a real claude subprocess and incur API cost; serializing them keeps cost predictable and avoids API-side rate-limit interactions). **Do NOT call `t.Parallel()`** in the new tests — match the existing convention.

## Error handling

Pyrycode-side: every fatal path uses `t.Fatalf` with a message that includes (a) the failing assertion's expected-vs-actual, and (b) truncated stdout/stderr or the JSONL path, depending on which surface the assertion was reading. Truncation cap: 1024 bytes per stream, suffixed `... (truncated)`. The pattern is established in `allowed_tools_enforcement_test.go:43-51` and `tool_loop_test.go:124-131`; mirror byte-for-byte.

Claude-side: a non-zero `ExitCode` from `pyry agent-run` (e.g., transient API failure, claude binary missing) surfaces as the first assertion failing with stderr inline. No retry logic — the nightly workflow's job-level retry handles flakiness.

## Testing strategy

These ARE the tests. There is no test-of-tests. The package is gated by `//go:build e2e_realclaude`; `make test` does NOT run it; `make e2e-realclaude` DOES. No Makefile change needed (the build tag handles routing). Verify locally before committing the developer's work:

```
go test -tags e2e_realclaude -run 'TestRealClaude_.*_RoleLoop' -v ./internal/e2e/realclaude/
```

All five should pass in ~30 seconds total with a warm prompt cache. A cold cache or transient claude API issue may push individual runs past 30 seconds; the 5-minute per-test ceiling absorbs that.

## Open questions

- **Last-assistant `EndOfTurn` assertion**: the AC says "the last `assistant` event in `ReadJSONL(...)` has `EndOfTurn == true`". `internal/agentrun/jsonl/reader.go:58-64` notes that transitional thinking-block `end_turn` envelopes have `EndOfTurn=false`. For these prompts (haiku, low effort, no tools), the realistic shape is a single text-bearing assistant event at the tail with `EndOfTurn=true`. If empirical runs show a transitional `end_turn` arriving AFTER the text-bearing one (unexpected for these prompts but possible), the developer should change the assertion to "ANY assistant event has `EndOfTurn=true && TextChars > 0`" rather than relax to "the last event regardless of EndOfTurn". Document the change in a comment if so.
- **`baseTools` drift detection**: this spec mirrors the constant by hand. If `agent-dispatcher`'s `baseTools` changes, this file goes stale silently. A future ticket could add a CI cross-repo check; out of scope here. The cited-comment-above-the-constant is the deliberate review breadcrumb in the meantime.
