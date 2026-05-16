# Spec: realclaude protocol resilience tests

**Ticket:** #382
**Size:** S
**Status:** Ready for implementation

## Files to read first

- `internal/e2e/realclaude/fixtures.go:32-188` — `WithWorktree`, `WithWorktreeAuthenticated`, `RunPyryAgentRun`, `RunOpts`, `RunResult`. The new tests use `RunPyryAgentRun` for AC#1 and AC#4; do NOT modify this file.
- `internal/e2e/realclaude/tool_loop_test.go:147-202` — `contentBlock`, `parseContentBlocks`, `resultTrailer`, `parseResultTrailer`. AC#1 reuses these directly; the new file imports nothing new for trailer parsing.
- `internal/e2e/realclaude/tool_loop_test.go:27-145` — full happy-path tool-loop test. AC#1 mirrors its event-walking shape, just with a failing Bash command and an extra assertion on tool_result content.
- `internal/e2e/realclaude/prompt_fidelity_test.go:75-89` — `jsonlPathFor` helper for failure-message diagnostics. Reuse for the JSONL-reading tests.
- `internal/e2e/realclaude/smoke_test.go:12-24` — pattern for `exec.LookPath("claude")` plus a bounded-timeout `exec.CommandContext`. AC#2 and AC#3 mirror this shape directly.
- `cmd/pyry/agent_run.go:207-265` — `PYRY_CLAUDE_BIN` resolution and `buildClaudeArgs`. Tests #2 and #3 mirror the same argv shape when invoking claude directly (sans pyry).
- `internal/agentrun/streamrunner/runner.go:82-99,179-195` — `userTurn` envelope shape (`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}`). AC#2 must construct one to write before closing stdin (per AC: "write the initial prompt, then close stdin").
- `docs/lessons.md` §"Claude session storage on disk" — encoded-cwd rule (`/` AND `.` → `-`). Only matters if a test reads JSONL; the helper `ReadJSONL` handles encoding for you.

## Context

The realclaude suite covers happy paths only. Production dispatch fails in four shapes that pyry must interpret as structured signals rather than silent hangs: a Bash tool returns non-zero, claude's stdin closes before processing finishes, claude receives malformed stream-json, and prompts approach the context window. This ticket adds one test per shape to lock the contract down.

The four tests share the realclaude file shape (single build tag `e2e_realclaude`, real `claude` binary, `--model claude-haiku-4-5 --effort low --max-turns 3`, ~$0.08 per nightly run with cache warm).

## Design

### Scope

**One new file, no edits to existing files:**

- `internal/e2e/realclaude/resilience_test.go` (new)

Helpers needed by AC#2 / AC#3 (direct claude exec, no pyry) live inside the new file. This keeps the spec scope tight and avoids touching shared `fixtures.go`. If a future ticket adds a third raw-exec test, promote the helper then.

### Test 1 — `TestRealClaude_BashTool_NonZeroExit`

**Shape:** invokes `RunPyryAgentRun` with `AllowedTools: []string{"Bash"}`, a prompt that asks claude to run a Bash command guaranteed to exit non-zero (`ls /nonexistent-path-for-pyrycode-382` is safe and stable across Linux/macOS).

**Assertions, in order:**

1. `result.ExitCode == 0` (pyry exit; the tool failure does not propagate as a process exit).
2. `result.SessionID != ""` (same shape as existing tests).
3. Walk JSONL events. Find a `tool_use` block with `Name == "Bash"`, record its `ID`. Find the matching `user` `tool_result` with `ToolUseID == that ID`. **Assert the `tool_result` content is non-empty AND `is_error == true`** — this is the new field under assertion that existing tests do not cover.
4. After the tool_result, find at least one subsequent assistant `text` block (claude continued reasoning with the error in context — `sawFinalText` shape from tool_loop_test).
5. Parse the result trailer (`parseResultTrailer`). Assert `trailer.Subtype == "success"`. **Do NOT assert on `StopReason`** — claude may end on `end_turn` (recovered gracefully) or `tool_use_error` (named structured signal); both satisfy the AC. The spec contract is "structured signal, not pipe break"; `Subtype == "success"` IS that contract.

**`tool_result` `is_error` extraction:** extend `contentBlock` with an `IsError` bool with json tag `is_error,omitempty`. The struct is defined in `tool_loop_test.go`; since both test files live in the same package under the same build tag, the new test imports it directly. (One-line struct extension is the cleanest path; do not duplicate the struct.) **Caveat:** if `tool_loop_test.go` is excluded from a future build (`-run` filter does not change build set, but a structural refactor could), the import dependency breaks. Acceptable today — both files share the same build tag and package.

**Cost:** one turn at `--max-turns=3` (assistant invokes Bash → tool_result with error → assistant final text → end). Same envelope as `TestRealClaude_ToolLoopIntegrity`.

### Test 2 — `TestRealClaude_PrematureStdinClose`

**Shape:** invokes `claude` directly via `exec.CommandContext` (NOT via pyry). The AC says "write the initial prompt, then close stdin before claude finishes" — `streamrunner` already does this in production. The test value is asserting the property explicitly under a tight timeout so a future claude regression (e.g., claude blocks waiting for a second envelope) trips the build.

**Invocation:**

- Resolve claude binary via `resolveClaudeBin(t)` (helper defined below; mirrors `cmd/pyry/agent_run.go:209-212`).
- Argv: equivalent to `buildClaudeArgs` for `model=claude-haiku-4-5, effort=low, max-turns=3, allowed-tools=Read`, plus `--input-format stream-json --output-format stream-json --verbose --dangerously-skip-permissions`. No `--append-system-prompt-file` (optional; not needed for the property under test).
- `cmd.Dir = workdir` (from `WithWorktreeAuthenticated(t)` — this test calls the real Anthropic API).
- `ctx` with 60-second deadline (tight; the property is "no hang").
- Write the same `userTurn` envelope shape as `streamrunner.marshalEnvelope` (prompt body: any short ASCII text, e.g. `"Reply with a single short word."`), then `stdin.Close()`. The "premature" framing comes from closing stdin before claude finishes; matches the production pattern exactly.

**Assertions:**

1. `ctx.Err() != context.DeadlineExceeded` — bounded-timeout property. If this fires, claude hung; that IS the regression being guarded against.
2. `cmd.ProcessState.ExitCode() == 0` — clean exit per AC ("non-error status"). If claude exits non-zero under this pattern, pyry's production dispatch is broken and this test makes it visible.
3. No assertion on stdout content. The point is liveness, not the response.

**`WithWorktreeAuthenticated`** is required (not `WithWorktree`) because this test hits the real Anthropic API; the helper skips cleanly when `ANTHROPIC_API_KEY` is unset.

**Orphan-process check** (AC mentions "no orphan child processes left behind"): `exec.CommandContext` with a deadlined context already SIGKILLs the child on deadline expiry. The bounded-timeout assertion (#1) is the operational expression of the no-orphan property. Adding a `/proc`-walk check is over-engineering for a property `exec.CommandContext` already provides.

### Test 3 — `TestRealClaude_MalformedStreamJSON`

**Shape:** invokes `claude` directly (same path as Test 2), writes garbage bytes (`[]byte("this is not stream-json\n}}{{not json either\n")`) to stdin, closes stdin, waits.

**Invocation:** identical argv to Test 2. `WithWorktree(t)` is sufficient (no API call expected — claude rejects before reaching the model). 60-second timeout.

**Assertions:**

1. `ctx.Err() != context.DeadlineExceeded`.
2. `cmd.ProcessState.ExitCode() != 0` — claude rejected the input.
3. `len(stderr) > 0` — a diagnostic was emitted.

**Stderr substring assertion:** the AC asks for a "diagnostic substring" check. Hard-coding a specific string couples the test to claude's diagnostic copy, which can change without notice. Instead: assert `len(stderr) > 0` AND that the diagnostic mentions at least one of `{"json", "parse", "input", "format", "envelope"}` (case-insensitive, any match). This catches "claude crashed silently" without ratcheting on prose. Implementation: build the predicate as a `strings.Contains(strings.ToLower(stderrStr), candidate)` loop over the candidate slice.

**Why not assert on a specific exit code:** unknown stability across claude versions. The "non-zero" assertion is the contract.

### Test 4 — `TestRealClaude_LargePromptNearContextWindow`

**Shape:** invokes `RunPyryAgentRun` with a ~50 KB prompt body (`strings.Repeat("All work and no play makes Jack a dull boy. ", 1200)` → ~52 KB).

**Invocation:** standard `RunOpts` with `MaxTurns: 3`, `AllowedTools: []string{"Read"}`, `Effort: "low"`, `Model: "claude-haiku-4-5"`. Use `WithWorktreeAuthenticated(t)` (real API call). 5-minute timeout (haiku at 50 KB is fast; default is fine).

**Assertions (disjunctive — AC says "either ... OR"):**

1. `result.SessionID != ""` and the result trailer exists (`parseResultTrailer`).
2. **Either branch A** (claude processed it): `trailer.Subtype == "success"` AND `trailer.StopReason != "context_overflow"` AND `trailer.StopReason != "max_context_length"` (the test asserts on the two known overflow-shaped reasons; an unknown new value is treated as the structured-error branch).
3. **Or branch B** (claude rejected via structured stream-json): `trailer.Subtype != "success"` (i.e., `"error"`, `"error_max_turns"`, or another named subtype) AND `result.ExitCode` is the documented non-success exit (do NOT pin a specific exit code — `result.ExitCode != 0` is sufficient when paired with the non-success subtype). The point is: there IS a structured result trailer (no crash mid-stream).
4. The disjunction is "(A) or (B)"; failure mode is "neither" — e.g., no result trailer at all (crash), or a result trailer with success subtype but `StopReason` matching an overflow shape (silent truncation).

**Implementation note on disjunction:** structure as a single `if !branchA && !branchB { t.Fatalf(...) }` with both branches' diagnostics included in the failure message. Do not split into two separate `if/else`-chained asserts — the diagnostic is more useful when both branches are visible.

### Shared local helpers (live inside `resilience_test.go`)

**`resolveClaudeBin(t *testing.T) string`** — returns `os.Getenv("PYRY_CLAUDE_BIN")` if non-empty AND not equal to `os.Args[0]` (defense against the 2026-05-16 fork-bomb pattern documented on `RunOpts.UseTestBinaryAsFakePyry`). Otherwise resolves `claude` via `exec.LookPath`; `t.Skipf` if not found (matches `smoke_test.go:12-14` skip-with-named-variable shape).

**`directClaudeArgs() []string`** — returns the argv slice used by Tests 2 and 3. Pure function; one-liner equivalent to a slice literal.

**`runClaudeDirect(t, workdir, ctx, stdinBytes) (exitCode int, stdout, stderr []byte)`** — wraps the `exec.CommandContext` + `StdinPipe` + `Run` shape. Returns the three values needed by Tests 2 and 3. ~25 lines. Does NOT call `t.Fatalf` on non-zero exit; callers assert. Does NOT propagate `ctx.Err()` — callers check `ctx.Err()` themselves to distinguish deadline-exceeded from clean exit.

### Concurrency model

Each test is `func Test...(t *testing.T)` and does NOT call `t.Parallel()`. The realclaude suite is sequential by design (real API calls; serial execution keeps cost predictable and avoids spurious 429s). Matches existing tests in the package.

### Error handling

- Structural failures (claude binary missing, workdir setup failed) → `t.Skipf` (binary missing) or `t.Fatalf` (workdir setup; these are not transient).
- Bounded timeouts: every test uses `context.WithTimeout`. Deadline expiry IS the failure mode being tested against — assertions check `ctx.Err()` explicitly.
- API errors from Anthropic (auth, throttling): surface via `result.ExitCode != 0` + stderr in `t.Fatalf` message. Do NOT retry; the suite is nightly, transient failures are noise but better surfaced than masked.

### Testing strategy

These ARE the tests; no meta-tests. The suite is gated by build tag `e2e_realclaude`; CI runs it nightly via the existing realclaude job.

Each test asserts a **structured exit signal** (exit code, JSONL event, or stderr substring) per the AC's "no test passes on 'claude eventually returned something'" rule. Walk through the four:

- Test 1: structured signal = JSONL `tool_result` with `is_error == true` + result trailer `subtype == "success"`.
- Test 2: structured signal = `ctx.Err() == nil` after `cmd.Wait` + `ExitCode == 0`.
- Test 3: structured signal = `ExitCode != 0` + stderr mentions a parse-shaped keyword.
- Test 4: structured signal = result trailer present AND (success-without-overflow OR non-success-with-named-subtype).

### Open questions

- **Does `is_error == true` reliably appear in tool_result content blocks for non-zero Bash exits in the current claude version?** The architect believes yes (it's the documented stream-json contract per `docs/knowledge/architecture/system-overview.md`'s reference to claude's protocol), but the implementing developer should verify with one manual run against the real binary and adjust the field name if claude emits a different key. If the field is absent, fall back to asserting the tool_result content contains the literal command name (`ls`) AND a non-zero indicator substring (e.g., `"No such file"`); document the fallback choice in a comment.
- **Stable diagnostic keywords in claude's stderr for malformed input (Test 3).** The candidate list `{"json", "parse", "input", "format", "envelope"}` is a reasonable starting set; if zero matches occur in the first dev run, expand the list with whatever claude actually prints (still keep the OR-of-keywords shape, never pin a specific phrase).
- **Whether `--max-turns=3` is enough for Test 1's "claude continues to a subsequent turn with the error in context" assertion.** Three turns is the same budget as `TestRealClaude_ToolLoopIntegrity`, which succeeds with that budget. If a flake appears, the budget is the first knob to tune (raise to 4); document the tuning rationale in-comment if changed.

## Implementation checklist (for the developer)

1. Read the files in "Files to read first" (especially `tool_loop_test.go` for the JSONL walk pattern, `smoke_test.go` for the `exec.LookPath` skip pattern, and `agent_run.go:254-266` for `buildClaudeArgs`).
2. Create `internal/e2e/realclaude/resilience_test.go` with build tag `//go:build e2e_realclaude` and package `realclaude`.
3. Add the three helpers (`resolveClaudeBin`, `directClaudeArgs`, `runClaudeDirect`) at the bottom of the file.
4. Extend `contentBlock` in `tool_loop_test.go` with `IsError bool \`json:"is_error,omitempty"\`` (one-line struct field addition; no behavior change to existing tests).
5. Write the four `Test...` functions in declaration order matching this spec.
6. Run `go vet ./...` and `go build -o /tmp/pyry ./cmd/pyry` to confirm the package compiles.
7. Run `go test -race -tags e2e_realclaude -run 'TestRealClaude_(BashTool_NonZeroExit|PrematureStdinClose|MalformedStreamJSON|LargePromptNearContextWindow)' ./internal/e2e/realclaude/` with `ANTHROPIC_API_KEY` exported and `claude` on PATH. All four pass. Total cost target: ~$0.08.
8. Commit with subject `test(e2e/realclaude): protocol resilience tests (#382)`.
