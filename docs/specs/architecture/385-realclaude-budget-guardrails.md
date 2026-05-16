# Spec #385 — `e2e/realclaude`: cache-hit verification + max-turns boundary tests

Two budget-guardrail tests in a new `budget_test.go` under the existing
`internal/e2e/realclaude/` package. They pin two cost-relevant guarantees the
suite does not currently exercise:

1. **Prompt-cache alignment across runs.** Phase C's cost model assumes
   Anthropic's prompt cache hits across `pyry agent-run` invocations within
   the 1-hour TTL when the system prompt + tools list are identical. A
   regression (dynamic content leaking into the system prompt; per-invocation
   tool-list churn) would silently multiply API spend. Pinned via:
   run a small canned prompt twice with the same system prompt; assert the
   second run's `result` trailer reports `usage.cache_read_input_tokens > 0`.

2. **`--max-turns` enforcement at the budget boundary.** Today the only
   structural pin lives at the emitter unit-test layer
   (`internal/agentrun/streamjson/emitter_test.go:225`). Nothing asserts
   end-to-end that `pyry agent-run --max-turns=N` actually caps a run at N
   turns against the real `claude` CLI. Pinned via: prompt that requires
   ≥5 tool turns, set `--max-turns=2`, assert
   `result.subtype == "error_max_turns"`, `terminal_reason == "max_turns"`,
   `num_turns == 2`, `stop_reason != "end_turn"`.

Composition is the established `WithWorktreeAuthenticated` → `RunPyryAgentRun`
→ `parseResultTrailer` shape (sibling tests #381 / #382 / #384). Zero edits to
`fixtures.go`; zero production-source changes.

## Files to read first

- `internal/e2e/realclaude/fixtures.go:32-225` — `WithWorktree`,
  `WithWorktreeAuthenticated`, `RunOpts`, `RunResult`, `RunPyryAgentRun`. The
  argv contract you compose against; in particular the `--max-turns`
  lowering at line 163 and the `--allowed-tools` join at 162.
- `internal/e2e/realclaude/tool_loop_test.go:185-209` — `resultTrailer`
  struct and `parseResultTrailer(stdout)`. Reuse both as-is; this is the
  **fifth** consumer of `parseResultTrailer` (after #376, #381, #382, #384).
- `internal/e2e/realclaude/prompt_fidelity_test.go:79-89` — `jsonlPathFor`
  helper for resolving the JSONL path in failure messages. **Eighth**
  consumer if you cite it (still file-private; YAGNI bar for promoting to
  fixture export not yet met).
- `internal/agentrun/streamjson/emitter.go:251-273` — the exact wire shape
  of the `type:"result"` trailer (`trailer` + `trailerUsage`). The field
  names + JSON tags your assertions bind to. Note **`terminal_reason`**
  (not `termination_reason`) and **`subtype`** values
  `"success"` / `"error_max_turns"` / `"error_during_execution"`.
- `internal/agentrun/streamjson/emitter.go:238-249` — `wireFields(r)`
  mapping table. The exact `(subtype, terminal_reason, is_error)` triple
  the max-turns path emits: `("error_max_turns", "max_turns", true)`.
- `internal/agentrun/budget/budget.go:101-138` — `Counter.OnEvent`. Confirms
  the budget is enforced **on assistant-entry count**, not raw turn count,
  and fires `SetExitReason(ExitReasonMaxTurns)` via the `Terminate` hook
  when `count >= MaxTurns` AND the current event is **not** `end_turn`. The
  `num_turns == 2` assertion below derives from this — the emitter's
  `numTurns` is incremented per assistant entry and the trailer reports the
  count at termination.
- `internal/agentrun/streamjson/emitter_test.go:225-240` — `TestTrailer_MaxTurns`.
  Reference for the exact field-level assertion shape this spec mirrors at
  the real-`claude` boundary.
- `internal/agentrun/jsonl/reader.go:85-92` — `UsageBlock` field names.
  Confirms `CacheReadInputTokens` / `CacheCreationInputTokens` snake-case
  JSON keys at the on-disk surface (the trailer aggregates them at
  `emitter.go:269-272`).
- `docs/knowledge/codebase/372.md` + `docs/knowledge/codebase/373.md` —
  helper authoring rationale; `WithWorktreeAuthenticated` from #409.
- `docs/knowledge/features/e2e-realclaude.md` § sibling-test prose (lines
  containing `#381`, `#382`, `#384`) — established patterns for shared
  helper reuse, prompt-as-enforcement-fixture discipline, and `t.Parallel()`
  convention (never call it in this suite).

## Context

This is the seventh consumer of the `WithWorktree(Authenticated)` →
`RunPyryAgentRun` → `parseResultTrailer` trio. Sized **S** by analogy with
the four most recent siblings: `per_agent_test.go` (#381, ~174 LoC),
`resilience_test.go` (#382, ~380 LoC), `mcp_smoke_test.go` (#384, ~290 LoC),
`prompt_fidelity_test.go` (#364, ~89 LoC). Single concern, no split.

**Blockers** (closed): #372, #373 — fixture helpers. **Adjacent in-flight
branch:** `feature/363` carries the original umbrella spec and re-touches
`fixtures.go`; this spec does NOT modify `fixtures.go`, so there is no
merge-conflict surface even if #363 lands later.

## Design

### File layout

One new file, no edits elsewhere:

```
internal/e2e/realclaude/budget_test.go
```

Header (verbatim, matches every other test in the package):

```go
//go:build e2e_realclaude

package realclaude
```

### Tests

#### `TestRealClaude_CacheHitWarmsAcrossRuns`

**Property under test.** Within a single test process, two back-to-back
`pyry agent-run` invocations against identical `(system-prompt, allowed-tools,
model, effort)` parameters produce a second-run `result` trailer with
`usage.cache_read_input_tokens > 0`. A regression that breaks cache-key
alignment (e.g. dynamic content sneaking into the system prompt, or
per-run tool-list churn) will show `cache_read_input_tokens == 0` on the
second run.

**Shape.**

1. `workdir := WithWorktreeAuthenticated(t)` — pins `$HOME` to a tempdir and
   self-skips with the canonical `WithWorktreeAuthenticated` message when
   `ANTHROPIC_API_KEY` is absent. The same workdir is reused for both runs;
   this is incidental (the cache is per-API-key per-content-hash, not
   per-workdir) but pins the simplest shape.
2. Build a shared `RunOpts` skeleton — see fields below.
3. Run 1: `firstResult := RunPyryAgentRun(t, opts)`. Assert
   `firstResult.ExitCode == 0`, `firstResult.SessionID != ""`, then
   `firstTrailer := parseResultTrailer(firstResult.Stdout)` (hard-fatal
   on error per `parseResultTrailer`'s contract).
4. Run 2: `secondResult := RunPyryAgentRun(t, opts)` against the **same**
   `opts` struct (same `prompt.txt` content, same `system.txt` content —
   `RunPyryAgentRun` rewrites both files on each call). Same exit-code +
   session-id + trailer-parse asserts.
5. **Primary assertion**: `secondTrailer.Usage.CacheReadInputTokens > 0`.
6. **Diagnostic assertion**: `firstTrailer.Usage.CacheCreationInputTokens > 0`.
   This is a clarifying check — if it fires, the cache is not engaging
   on the first run (likely the prompt is below the per-model minimum
   cache size). If it does NOT fire but the primary assertion still
   passes, the cache was already warm from an earlier test (the second
   run hit a pre-existing cache); the primary assertion holds, this
   diagnostic surfaces a soft signal. **Order matters**: assert the
   primary first so a regression in cache alignment surfaces with the
   correct failure message; assert the diagnostic with `t.Errorf`
   (not `t.Fatalf`) so it doesn't mask the primary on a passing run.

**RunOpts.**

| Field          | Value                                                                                  |
|----------------|----------------------------------------------------------------------------------------|
| `Workdir`      | `workdir` (from `WithWorktreeAuthenticated`)                                            |
| `Prompt`       | file-local const `cacheHitUserPrompt = "Reply with a single word: ok."`                |
| `SystemPrompt` | file-local const `cacheHitSystemPrompt` — multi-sentence guard prompt, see below       |
| `AllowedTools` | `[]string{"Read"}`                                                                     |
| `MaxTurns`     | `1`                                                                                    |
| `Effort`       | `"low"`                                                                                |
| `Model`        | `"claude-haiku-4-5"` (literal — matches the sibling-test convention; the AC's `haiku` short-form is rendered as the literal model id at the helper boundary) |

**System prompt sizing.** Anthropic's implicit prompt cache requires the
cacheable prefix to exceed a per-model minimum (1024 tokens on Sonnet,
2048 on Haiku 4.5 per public docs as of 2026-05). Claude Code's built-in
system prompt + the tool-definition section together typically exceed
this threshold even with `--allowed-tools=Read` only; sibling test
`per_agent_test.go` (#381) verifies the cache warms across haiku runs at
`--effort=low` with a similar shape. Still, to harden against a future
threshold change, `cacheHitSystemPrompt` SHOULD be ≥ 5 plain-English
sentences (~200-300 tokens) — enough to contribute material to the
cacheable prefix without inflating cost meaningfully (~$0.001 per
nightly run). Suggested content:

> `You are a Pyrycode end-to-end regression-guard test fixture. You exist solely to drive a single, minimal claude turn so the surrounding test can assert on the resulting stream-json output. Reply with at most one short word; do not elaborate; do not invoke any tools; do not ask clarifying questions. The test that hosts you is named TestRealClaude_CacheHitWarmsAcrossRuns and lives in internal/e2e/realclaude/budget_test.go. Its job is to verify the Anthropic prompt cache hits across two back-to-back invocations with identical system-prompt content.`

The exact wording is not load-bearing; the only constraints are
(a) deterministic across both runs (use a single `const`), (b) sized to
clear the implicit-cache threshold by margin, (c) discourages tool use
and verbose replies. **Do NOT include the literal date, run id, or any
other dynamic content** — that's the regression class this test is
designed to catch.

**Cost.** Two haiku/low calls, 1 turn each, ~300-token system prompt
plus minimal user prompt and reply. **~$0.02 per nightly** (well under
the AC's $0.04 combined cap; the max-turns test contributes the rest).

**`t.Parallel()`: NOT called.** Matches the established realclaude
convention. Also load-bearing here — running this concurrently with
another suite test would muddy the "cache warmed by run 1 specifically"
diagnostic. Sequential execution keeps the cache lineage clean.

#### `TestRealClaude_MaxTurnsHonored`

**Property under test.** `pyry agent-run --max-turns=2` against a prompt
that natural-completion would require ≥5 turns to satisfy causes the
budget Counter to terminate the run at the cap. The emitted trailer
reports the structured signal of budget exhaustion (NOT natural
completion).

**Shape.**

1. `workdir := WithWorktreeAuthenticated(t)`.
2. `result := RunPyryAgentRun(t, opts)` with the configuration below.
3. Standard run-level asserts: `ExitCode == 0`, `SessionID != ""`.
   - **`ExitCode == 0` is correct here.** Per `streamrunner` / `agent-run`
     wiring, `pyry agent-run` exits 0 on a successfully-emitted result
     trailer regardless of trailer `is_error`. The trailer field is the
     wire-level signal of budget exhaustion, NOT the subprocess exit code.
4. `trailer := parseResultTrailer(result.Stdout)` (hard-fatal on parse miss).
5. **Property assertions (all four must hold):**
   - `trailer.Subtype == "error_max_turns"`
   - `trailer.TerminalReason == "max_turns"`
   - `trailer.NumTurns == 2` (exact match — the budget caps at exactly the
     `--max-turns` value; an off-by-one fires here)
   - `trailer.StopReason != "end_turn"` (terminal reason was NOT natural
     completion). Per `emitter.go:213`, `StopReason` is the last assistant
     message's `stop_reason`; on max-turns termination this is typically
     `"tool_use"` (the run was capped mid-tool-loop), but the assertion is
     phrased as `!= "end_turn"` per the AC because the precise value
     depends on what state the model was in when SIGTERM landed.

6. **`trailer.IsError == true` SHOULD also be asserted** — the wire-level
   `is_error: true` flag is the explicit AC signal of "budget exhaustion,
   not natural completion" beyond the subtype string. Add as a fifth
   assertion alongside the four above.

**RunOpts.**

| Field          | Value                                                                                      |
|----------------|--------------------------------------------------------------------------------------------|
| `Workdir`      | `workdir`                                                                                  |
| `Prompt`       | file-local const `maxTurnsPrompt` — see below                                              |
| `SystemPrompt` | file-local const `maxTurnsSystemPrompt = "You are an e2e regression-guard test. When asked to run shell commands, use the Bash tool once per command and wait for each result before continuing."` |
| `AllowedTools` | `[]string{"Bash"}`                                                                         |
| `MaxTurns`     | `2`                                                                                        |
| `Effort`       | `"low"`                                                                                    |
| `Model`        | `"claude-haiku-4-5"`                                                                       |

**`maxTurnsPrompt` shape — force ≥5 distinct tool turns.** The prompt
must compel the model into a sequence haiku/low cannot collapse into a
single Bash call. Recommended literal:

> `Use the Bash tool five times in sequence to run these commands one at a time, each in its own separate Bash call, waiting for each result before issuing the next:`
> `1. echo step-one`
> `2. echo step-two`
> `3. echo step-three`
> `4. echo step-four`
> `5. echo step-five`
> `Do NOT combine these into a single command. Do NOT use && or ; to chain them. After all five outputs, tell me which step numbers you saw.`

**Why this shape.** Haiku/low's default behaviour with `--allowed-tools=Bash`
on a "do five things" instruction is to use the tool once per turn for
each step — confirmed empirically across siblings (#376, #382). With the
explicit "do NOT combine" guidance, the model will not collapse into a
single `echo step-one; echo step-two; …` call. Each step consumes one
assistant turn (tool_use), so 5 steps + a final summary turn = 6 turns
of work; capped at `--max-turns=2`, the run terminates after exactly 2
assistant entries with the budget signal.

**Tool-call collapse risk.** If a future haiku revision is smart enough
to fire all five `echo`s in one tool_use block (or otherwise complete in
≤2 turns naturally), the assertions `Subtype == "error_max_turns"` and
`TerminalReason == "max_turns"` will fail — surfacing as a clear test
failure rather than a silent pass. The mitigation is to bump the prompt
to force more turns (e.g. 8 sequential `read X.txt` calls with seeded
file content the model must enumerate by name), not to weaken the
assertion. Pin this in a one-line code comment above the prompt const
so the next maintainer knows what to tune if the test breaks on a model
upgrade.

**Cost.** Two haiku/low turns capped at 2 (out of an intended 6),
`--allowed-tools=Bash` invoked twice. **~$0.02 per nightly** (matches
the AC's $0.04 combined estimate when added to the cache-hit test).

**`t.Parallel()`: NOT called.** Suite convention.

### Helpers — none added

Both tests compose existing helpers. **Do NOT add anything to
`fixtures.go`.** Spec-level non-goal; spec violation if extended.

- `WithWorktreeAuthenticated(t)` — `fixtures.go` (#409).
- `RunPyryAgentRun(t, opts)` — `fixtures.go` (#373).
- `parseResultTrailer(stdout)` — `tool_loop_test.go:197`.
- `resultTrailer` struct — `tool_loop_test.go:185`.

No new types. No new file-local helpers (assertions inline; the test
bodies are short enough that abstracting an `assertMaxTurnsTrailer`
helper would add reading cost without removing duplication — the two
tests share zero assertion structure, so deduplication isn't on offer).

### `resultTrailer` struct sufficiency check

The package-private `resultTrailer` in `tool_loop_test.go:185-191`
defines:

```
Type              string
Subtype           string
StopReason        string
NumTurns          int
PermissionDenials *[]json.RawMessage
```

For the new tests we additionally need:
- `TerminalReason` (string, `terminal_reason` wire tag)
- `IsError` (bool, `is_error` wire tag)
- `Usage` (struct with `CacheReadInputTokens` + `CacheCreationInputTokens` ints)

**Extension policy.** Add the three missing fields to the existing
`resultTrailer` struct (NOT a new struct). All fields are
`omitempty`-tagged so existing tests (#376/#381/#382/#384) that decode
the trailer without referencing these fields are unaffected. The
struct doc comment gains one new sentence noting the fields and their
consumer.

```go
type resultTrailer struct {
    Type              string             `json:"type"`
    Subtype           string             `json:"subtype"`
    StopReason        string             `json:"stop_reason"`
    NumTurns          int                `json:"num_turns"`
    PermissionDenials *[]json.RawMessage `json:"permission_denials,omitempty"`

    // Added by #385 for the budget-guardrail tests. omitempty-tagged so
    // pre-#385 consumers (#376/#381/#382/#384) that don't read these
    // fields decode unchanged.
    IsError        bool               `json:"is_error,omitempty"`
    TerminalReason string             `json:"terminal_reason,omitempty"`
    Usage          resultTrailerUsage `json:"usage,omitempty"`
}

type resultTrailerUsage struct {
    InputTokens              int `json:"input_tokens,omitempty"`
    OutputTokens             int `json:"output_tokens,omitempty"`
    CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
    CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}
```

**Why extend, not duplicate.** The trailer shape is single-source; two
parsers would diverge silently on a future emitter field rename. The
package-private struct already lives in `tool_loop_test.go`; adding
three fields and one new sub-struct alongside it (in the same file) is
the minimum-friction extension. **Edit count: 1 file, 1 struct, 1 new
sub-struct, ~8 added lines.** Stays well inside the size budget.

If the developer prefers to define the new fields directly inside
`budget_test.go` via a separate struct (`type budgetTrailer struct { … }`)
to keep `tool_loop_test.go` untouched, that is also acceptable — the
trade-off is one extra parse pass per test (`parseResultTrailer` plus
a second `json.Unmarshal` of the same line for budget-specific fields).
Pick the cheaper option at implementation time; both satisfy the spec.

### Note on cache-trailer vs. on-disk usage

The trailer's `usage.cache_*_input_tokens` are aggregated by the emitter
across all assistant messages seen during the run
(`emitter.go:216-221`). The on-disk JSONL also carries per-assistant
`usage` blocks (`reader.go:152-157` decodes them into `Event.Usage`),
and either source could in principle satisfy the AC. **Use the trailer
on stdout, NOT the on-disk JSONL,** because:

1. The trailer is what nightly cost-regression dashboards aggregate over
   (`total_cost_usd` + `usage` ride in the same envelope). Asserting on
   the same wire shape the dashboard reads pins both ends of the cost
   path with one assertion.
2. The trailer is already the assertion target across siblings #376 /
   #381 / #382 / #384 — staying consistent reduces reader load.
3. The on-disk JSONL aggregation requires walking events and summing
   `Event.Usage.CacheReadInputTokens` across all assistant entries,
   duplicating what the emitter already aggregated.

The spec explicitly forbids reaching into `internal/agentrun/jsonl` for
this assertion. `parseResultTrailer(result.Stdout)` is the sole entry
point.

## Concurrency model

Neither test goroutines. Both invoke `RunPyryAgentRun` synchronously
twice (cache-hit) / once (max-turns). `t.Parallel()` is NOT called in
either test. No subprocess interaction beyond `RunPyryAgentRun`'s
internal `exec.CommandContext` + buffered stdout/stderr capture.

## Error handling

- `WithWorktreeAuthenticated` self-skips on missing `ANTHROPIC_API_KEY`.
- `RunPyryAgentRun` fatals on validation / build / exec / timeout —
  same contract as every other consumer.
- `parseResultTrailer` fatals on missing trailer (per its established
  contract since #376) — a missing trailer is a structural break, not
  a property failure.
- A non-zero subprocess exit on the cache-hit test is itself a failure
  (`ExitCode == 0` is asserted first); on the max-turns test, the
  assertion is also `ExitCode == 0` because `pyry agent-run` exits 0 on
  successful trailer emission regardless of trailer `is_error`. The
  budget-exhaustion signal lives in the trailer fields, NOT the exit
  code.
- Failure messages cite the resolved JSONL path via `jsonlPathFor`
  (eighth consumer; still file-private) only for JSONL-specific
  failures. The cache-hit and max-turns tests are trailer-only — they
  do NOT call `ReadJSONL` — so `jsonlPathFor` may or may not be
  invoked here. If the developer adds an optional supplementary JSONL
  walk for debugging context on a failure, cite the path; otherwise
  embed the first 1 KiB of `result.Stdout` in failure messages (the
  pattern from `tool_loop_test.go:127`).

## Testing strategy

End-to-end against the real `claude` CLI. There is no unit-test
equivalent for the property under test — the cache-hit property is a
property of the Anthropic API + claude CLI cache-key construction; the
max-turns property at the budget-Counter boundary is already pinned
unit-side at `internal/agentrun/budget/budget_test.go` and emitter-side
at `internal/agentrun/streamjson/emitter_test.go:225`. This spec adds
the missing **end-to-end** pin: the wire-level shape pyry actually emits
when claude is the upstream.

### Build-tag verification

- `make test 2>&1 | grep budget_test` must be empty —
  `//go:build e2e_realclaude` excludes the file from default `go test`.
- `make e2e-realclaude` must run both new tests (`-run
  'TestRealClaude_(CacheHitWarmsAcrossRuns|MaxTurnsHonored)'` is the
  selective re-run command for local iteration).
- `make check` (vet + test + staticcheck) unaffected.

### Manual verification on first run

Before declaring the spec implemented, the developer SHOULD inspect:

- `firstTrailer.Usage.CacheCreationInputTokens` — to confirm the
  system-prompt size cleared the implicit-cache threshold. If this is
  consistently 0 across nightly runs, the prompt is too small and needs
  padding; the diagnostic-only `t.Errorf` assertion exists to surface
  this case without failing the suite on the days the second run hits a
  pre-existing cache.
- `secondTrailer.NumTurns` on the cache-hit test — should be 1 (single
  reply, no tool loop). If > 1, the model is invoking tools despite
  `--allowed-tools=Read` and a "single short word" system-prompt
  instruction; the test still passes structurally but per-run cost goes
  up. Not a failure mode; a tuning signal.
- `result.Stdout` length on the max-turns test — should be small
  (`init`, ≤2 assistant blocks, `result`). A bloated stdout means the
  Counter is firing late; investigate.

These are operator-side observations during the developer's first
green run, not assertions.

## Open questions

- **Prompt-cache implicit minimum on Haiku 4.5 specifically.** The 2048-token
  figure cited above is from Anthropic's public docs as of 2026-05; the
  exact number for Haiku 4.5 may differ. The diagnostic-only
  `firstTrailer.Usage.CacheCreationInputTokens > 0` assertion (via
  `t.Errorf`, not `t.Fatalf`) lets a sub-threshold prompt surface as
  a soft signal during the developer's first green run rather than a
  spec violation. Resolution: developer measures on the first run; if
  `CacheCreationInputTokens == 0` consistently, pad `cacheHitSystemPrompt`
  with 2-3 additional sentences and re-run.
- **`num_turns == 2` exactness vs. tolerance.** The Counter fires on
  the Nth assistant entry where N == `MaxTurns` (`budget.go:119`). The
  emitter's `numTurns` is incremented on every assistant Emit
  (`emitter.go` — counter increment, not shown above). On `MaxTurns=2`,
  exactness should hold: the test asserts `== 2`. If a future
  refactor changes the counting cardinality (e.g. counts EOT entries
  separately), this assertion is the structural pin that catches it.
  No tolerance recommended.
- **Whether to also assert `total_cost_usd` is populated.** The trailer
  carries `total_cost_usd` but the emitter zeros it
  (`emitter.go:215`). Out of scope for #385; a future cost-dashboard
  ticket may pin this once the field is populated.

## Out of scope

- Anthropic API-level cache shape probing (cache breakpoint markers,
  TTL boundary, eviction). Pyry consumes claude's cache reporting
  passively; how the cache is constructed is upstream's concern.
- A nightly cost-regression CI assertion (compare `cache_read_input_tokens`
  to a baseline). Filed as a follow-up if observed drift warrants it.
- Re-validating the `--max-turns` flag value parser. Pinned at
  `cmd/pyry/agent_run_test.go`; not re-asserted here.
- Promoting `jsonlPathFor` / `parseResultTrailer` / `truncate` to
  fixture exports. YAGNI bar still not met after this seventh consumer.

## Related

- `docs/knowledge/codebase/372.md` — `WithWorktree` + `ReadJSONL` fixtures.
- `docs/knowledge/codebase/373.md` — `RunPyryAgentRun` subprocess fixture.
- `docs/knowledge/codebase/376.md` — `parseResultTrailer` + `resultTrailer`.
- `docs/knowledge/codebase/381.md` — per-role smoke; first reader of
  `parseResultTrailer` post-#376.
- `docs/knowledge/codebase/382.md` — protocol resilience; trailer-shape
  assertions for non-`end_turn` paths.
- `docs/knowledge/codebase/384.md` — MCP smoke; established the
  "compose existing helpers; do not edit `fixtures.go`" pattern.
- `internal/agentrun/budget/budget.go` — the upstream of the max-turns
  signal under test.
- `internal/agentrun/streamjson/emitter.go` — the on-the-wire shape
  of `type:"result"` the assertions bind to.
