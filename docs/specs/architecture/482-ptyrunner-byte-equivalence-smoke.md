# Spec — ptyrunner ↔ streamrunner real-claude byte-equivalence smoke (#482)

Adds one real-claude e2e test that drives `internal/agentrun/ptyrunner.Run` and
`internal/agentrun/streamrunner.Run` directly with the same prompt + same
budget, normalizes both stream-json byte streams, and asserts structural
equivalence at the dispatcher-visible level. A second test in the same file
asserts every flag `ptyrunner.buildArgs` emits is still recognized by `claude
--help`. Both tests are guarded by the existing `//go:build e2e_realclaude`
tag so default `go test ./...` skips them; `make e2e-realclaude` opts in.

The cutover at `cmd/pyry/agent_run.go` (#470) has already landed on main; the
ticket body describes #482 as a pre-cutover gate, but the empirical-validation
test is still load-bearing as a regression baseline against future drift in
either runner.

## Files to read first

- `internal/agentrun/ptyrunner/runner.go:165-419` — `Run` contract + the
  unexported `buildArgs` shape the argv-flags test mirrors. The package doc
  (lines 1-37) documents the logging discipline + forbidden imports.
- `internal/agentrun/streamrunner/runner.go:101-175` — `Run` signature + the
  `userTurn` stream-json envelope shape; the test uses `streamrunner.Run`
  verbatim. No need to construct the envelope manually — the package owns it.
- `internal/agentrun/streamjson/emitter.go:251-273` — `trailer` JSON shape
  ptyrunner emits via `Emitter.Close`. Pin the normalization rules against the
  field set declared here.
- `internal/agentrun/streamjson/testdata/captured_run.jsonl` — the wire shape
  streamrunner forwards from `claude -p --output-format stream-json` (lines
  1-7). Compare against the emitter trailer shape above; the field deltas
  become the test's documented normalization rules.
- `internal/agentrun/trust/trust.go:40-130` — `MarkWorkdirTrusted` realpath
  return + `~/.claude.json` atomic write. The ptyrunner side wires this once
  before the run; the realpath becomes `ptyrunner.Config.WorkDir`.
- `internal/agentrun/settings/settings.go:57-86` — `WriteSettings` tempfile
  path + `defer os.Remove` cleanup contract. The ptyrunner side wires this and
  the test owns the cleanup defer.
- `internal/e2e/realclaude/smoke_test.go:12-24` — the established
  `exec.LookPath("claude")` gate + install-or-adjust-PATH guidance. The new
  argv-flags test mirrors this gate verbatim.
- `internal/e2e/realclaude/fixtures.go:32-54` — `WithWorktree` / 
  `WithWorktreeAuthenticated` HOME-pinning helpers. `WithWorktreeAuthenticated`
  is the right entry for the byte-equivalence test (needs `ANTHROPIC_API_KEY`).
- `internal/e2e/realclaude/prompt_fidelity_test.go:28-73` — established
  pattern for a real-claude assertion: HOME pinned via `WithWorktree`,
  prompt/system files written into the workdir, exit-code assertion, JSONL
  read for content checks. The byte-equivalence test mirrors the workdir setup
  but skips the `pyry agent-run` exec wrapper (calls `Run` directly).
- `cmd/pyry/agent_run.go:324-358` — `buildStreamRunnerClaudeArgs` shape that
  the test mirrors verbatim for the streamrunner side. (cmd/pyry is `package
  main` so the function is not importable; the test re-states the argv shape
  literally and pins it against the helper output to catch drift.)
- `internal/sessions/id.go:20-43` — `NewID` UUIDv4 generator + `ValidID`
  predicate. The ptyrunner side uses `NewID` to mint a session-id per run.
- `docs/knowledge/codebase/479.md` — sibling slice that wired the budget
  Counter + watchdog Tracker on top of the JSONL tail; explains why
  `MaxTurns` enforcement differs between the two runners (streamrunner via
  `--max-turns` flag, ptyrunner via pyry-side `internal/agentrun/budget`).

## Context

Two parallel claude-spawn primitives now coexist in tree, both producing the
same stream-json wire shape consumed by the dispatcher:

- `streamrunner.Run` — claude as a stream-json subprocess (`--input-format
  stream-json --output-format stream-json --verbose
  --dangerously-skip-permissions ...`). Stream-json comes directly from
  claude's stdout. Live indefinitely as the empirical billing-comparison
  baseline (operator decision 2026-05-19) and as the rollback path
  (`PYRY_USE_STREAMJSON=1`).
- `ptyrunner.Run` — claude as an interactive TUI under PTY drive (`--session-id
  --settings --permission-mode default ...`). Stream-json synthesized
  pyry-side by tailing `~/.claude/projects/<encoded>/<sid>.jsonl` and
  re-emitting each event via `internal/agentrun/streamjson.Emitter`. Default
  path post-#470. Subscription-eligible per Anthropic's 2026-06-15 policy.

The dispatcher cares about the wire shape on stdout: a sequence of
newline-delimited JSON envelopes ending in a `type:"result"` trailer. The
ticket's job is to validate empirically — against the real `claude` binary —
that ptyrunner's synthesized stream-json carries the same dispatcher-visible
signal as streamrunner's pass-through. This is a regression baseline for
either runner shifting shape.

The validation must be **structural**, not byte-for-byte. The two outputs
differ inherently — session-ids are random per run, timestamps drift,
the LLM's text response is non-deterministic for the same prompt, and the
two pipelines have **different field sets in the `result` trailer** (the
ptyrunner emitter omits `api_error_status`, `duration_api_ms`, `result`,
`modelUsage`, `permission_denials`, `fast_mode_state`, `uuid`,
`usage.server_tool_use` that streamrunner forwards from claude's native
output). The normalization rules below pin this delta in code so it survives
the next refactor.

## Design

### Package and file layout

One new test file. No existing files are modified.

```
internal/e2e/realclaude/
  ptyrunner_byte_equivalence_test.go   (new, ~250-300 LOC, //go:build e2e_realclaude)
```

The file holds two `Test*` functions plus their helpers. No new exported
identifiers leave the package. Existing fixture helpers (`WithWorktree`,
`WithWorktreeAuthenticated`, `ReadJSONL`, the package-private parser
helpers) are reused as-is.

### Test 1 — `TestPtyRunnerArgvFlagsExistInClaudeHelp`

Cheap argv-shape gate. Runs `claude --help` once, captures the help text,
asserts every flag `ptyrunner.buildArgs` emits is present in the output.
Fails loudly if a future claude release renames, removes, or shifts the
shape of any of them.

**Setup:**

- `exec.LookPath("claude")` gate mirroring `smoke_test.go:12-24` —
  `t.Fatalf` with the install-or-adjust-PATH guidance if absent.
- 10s `context.WithTimeout` for the `claude --help` invocation.

**Body:**

- Run `exec.CommandContext(ctx, "claude", "--help").CombinedOutput()`.
- `t.Fatalf` on non-nil error with stdout+stderr embedded.
- Walk a literal `[]string{"--session-id", "--settings", "--permission-mode",
  "--append-system-prompt-file", "--model", "--effort"}` — the SAME six flag
  names `ptyrunner.buildArgs` returns at runner.go:395-404.
- For each flag, assert `bytes.Contains(helpOut, []byte(flag))`. Failure
  message: `claude --help does not mention %s; ptyrunner.buildArgs may need
  adjustment (claude --help output:\n%s)`.

**The literal flag-list is the AC's "OR pins the test against a known-good
claude version" lever inverted — we pin against a known-good *flag set*, not
a known-good version. Renaming a flag in `buildArgs` without updating this
list, OR a future claude release dropping one of the flags, is what the
assertion catches. A block comment above the literal slice names the
load-bearing pin and points to `ptyrunner/runner.go:buildArgs` as the source
of truth. Drift in either direction fails the test.**

No tear-down, no helpers, no HOME pinning. The test is wall-clock <1s.

### Test 2 — `TestPtyRunnerVsStreamRunner_StructuralEquivalence`

Runs both pipelines with the same prompt + same budget against real claude,
parses both stdout byte streams into a normalized per-line shape sequence,
and asserts structural equivalence.

**Why "structural equivalence" rather than byte-for-byte:** the field-set
delta documented above (ptyrunner emitter's `result` trailer is a strict
subset of streamrunner's pass-through) is intentional and load-bearing —
ptyrunner does not have the same data available (no `duration_api_ms`
because the API timing isn't in the JSONL; no `total_cost_usd` because pyry
doesn't price events; etc.). Byte-equivalence would require either deleting
fields from streamrunner's stdout post-hoc or synthesizing them in
ptyrunner's emitter; both are wrong. Structural equivalence asks the right
question — *does the dispatcher see the same signal?* — and is robust to
field-set drift on either side.

**Setup:**

- `home := realclaude.WithWorktreeAuthenticated(t)` — pins HOME to a
  per-test `t.TempDir()` and `t.Skip`s when `ANTHROPIC_API_KEY` is absent in
  the outer environment. (The helper is the established pattern for tests
  needing a real Anthropic response, not just argv-shape probes.)
- Two separate workdirs under `home` — one per runner — so the JSONL files
  each runner produces don't collide in `<HOME>/.claude/projects/`:
  - `workdirStream := filepath.Join(home, "stream-work")`
  - `workdirPty    := filepath.Join(home, "pty-work")`
  - `os.MkdirAll(...)` on each with mode `0o700`.
- One shared system-prompt file under `home`:
  - `systemPath := filepath.Join(home, "system.txt")`
  - Contents: `"You are a real-claude smoke test. Reply with the single
    word OK and nothing else."` — single-line, no tool use expected.
- Shared `promptBytes := []byte("Reply with the single word OK and nothing
  else.")`. Same bytes go to both runners verbatim.
- Shared `allowedTools := []string{"Read"}` — least-attractive tool for a
  one-word reply, satisfies `settings.WriteSettings`'s non-empty
  requirement, matches the streamrunner argv's `--allowed-tools=Read`.
- Shared `model := "claude-haiku-4-5"`, `effort := "low"`, `maxTurns := 1`.
  Haiku + low-effort + max-turns=1 minimises both wall-clock and the
  surface area where LLM stochasticity can shift the type sequence.

**The two runs are sequential, not parallel.** A shared `~/.claude.json`
sees one atomic write per runner (ptyrunner pre-marks trust on `workdirPty`
only); sequential simplifies the test and avoids racing on the trust file.
Total wall-clock budget per test: ~3 minutes. Per-run `context.WithTimeout`
of 90s each.

**Step A — streamrunner side:**

```go
var streamOut bytes.Buffer
ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
defer cancel()
err := streamrunner.Run(ctx, streamrunner.Config{
    ClaudeBin:   "claude",
    WorkDir:     workdirStream,
    Args:        streamRunnerArgs(systemPath, model, effort, maxTurns, allowedTools),
    PromptBytes: promptBytes,
    Stdout:      &streamOut,
    Stderr:      io.Discard,  // claude's stderr is operator-readable but the
                              // test only asserts on stdout.
})
// Run returns nil on a clean exit; non-nil → t.Fatalf with err + stderr.
```

`streamRunnerArgs` is a one-line file-local helper that mirrors
`cmd/pyry/agent_run.go:buildStreamRunnerClaudeArgs` verbatim (cmd/pyry is
`package main`, so import is impossible — the helper is duplicated here,
roughly 12 lines). A block comment above the helper points at the cmd
source-of-truth and explains the duplication.

**Step B — ptyrunner side:**

Sequence (all errors → `t.Fatalf` with the call name in the message):

1. `realpath, _ := trust.MarkWorkdirTrusted(workdirPty)` — wires the
   `~/.claude.json` pre-mark; returns the symlink-resolved path that becomes
   `ptyrunner.Config.WorkDir` below.
2. `settingsPath, _ := settings.WriteSettings(allowedTools)` — tempfile.
3. `t.Cleanup(func() { _ = os.Remove(settingsPath) })` — survives `t.Fatalf`.
4. `sid, _ := sessions.NewID()` — fresh UUIDv4 for `Config.SessionID`.
5. `var ptyOut bytes.Buffer; ctx2, cancel2 := context.WithTimeout(ctx,
   90*time.Second); defer cancel2()`.
6. `ptyrunner.Run(ctx2, cfg)` with the same `ClaudeBin / Model / Effort /
   MaxTurns / PromptBytes / SystemPrompt: systemPath / Stderr: io.Discard`
   values as the streamrunner side; `WorkDir: realpath`, `SessionID:
   string(sid)`, `SettingsPath: settingsPath`, `Stdout: &ptyOut`. `HomeDir`
   **intentionally left empty** — `WithWorktreeAuthenticated`'s
   `t.Setenv("HOME", home)` already pins `os.UserHomeDir()` so the watcher
   reads from `home` exactly as in production; setting `HomeDir` would
   test a different code path.

The `WorkDir: realpath` choice is load-bearing — same contract as #470's
`runAgentRunPty` (codebase/470.md). If pyry hands claude a symlinked path
when the trust pre-write keyed the realpath, claude's modal-check misses
and the trust modal renders. The test mirrors production wiring exactly.

The `settingsPath` cleanup is via `t.Cleanup` rather than a `defer` because
the cleanup must survive `t.Fatalf` calls earlier in the test (a `defer
os.Remove` in test code DOES run on t.Fatalf, but `t.Cleanup` is the more
idiomatic pattern for test-owned tempfiles in stdlib).

**Step C — normalize and compare:**

Define a file-local type that captures the per-envelope structural shape:

```go
type envelopeShape struct {
    Type    string   // "system", "user", "assistant", "tool_use", "tool_result", "result"
    Subtype string   // populated only for "system" (e.g. "init") and "result" (e.g. "success", "error_max_turns")
    // Fields the dispatcher reads — pinned to catch silent shape drift.
    // Empty when the field is absent (e.g. Subtype on "user").
}
```

`extractShapes(stream []byte) ([]envelopeShape, error)`: walks newline-delimited
input, decodes each non-empty line into a minimal struct (only `type` +
`subtype`), appends a `envelopeShape`. Empty lines skipped. Malformed JSON →
returned error with the offending line embedded.

`compareShapes(t, gotStream, gotPty []envelopeShape)`: asserts:

1. `len(streamShapes) >= 4` (init + user + assistant + result minimum).
2. `len(ptyShapes) >= 4`.
3. `streamShapes[0].Type == "system" && streamShapes[0].Subtype == "init"`.
4. Same for `ptyShapes[0]`.
5. Last element of each: `Type == "result" && Subtype == "success"`. (If
   the LLM ever genuinely takes >1 turn for the OK prompt, both pipelines
   would emit `subtype == "error_max_turns"` — the assertion accepts either
   shape but requires the two streams to AGREE on which it is.)
6. The two sequences of `(Type, Subtype)` pairs are equal element-by-element
   via `reflect.DeepEqual`. On mismatch, pretty-print BOTH sequences with
   indices and the first divergent position, so the failure message tells
   the reader exactly where the streams diverged.

**Field-level invariants:** in addition to the shape comparison, the test
verifies four content-level invariants — pinned because they're the
dispatcher's load-bearing inputs:

7. The first `type:"system",subtype:"init"` line in BOTH streams carries
   `"model":"claude-haiku-4-5"` (the value passed to `--model`). The
   assertion decodes a `struct { Type, Subtype, Model string }` from
   the line and compares.
8. The first `type:"user"` line in BOTH streams contains the prompt text
   `Reply with the single word OK and nothing else.` somewhere in its
   `message.content` (use `bytes.Contains(line, []byte(promptText))` —
   robust to the different content-array vs content-string shapes the two
   pipelines might emit; the prompt text contains no JSON-escape-sensitive
   characters so this is sound).
9. The `type:"result"` trailer in BOTH streams has `is_error:false` (or
   both have `is_error:true` if the agreed-on subtype was `error_max_turns`).
10. The `type:"result"` trailer in BOTH streams has the SAME `num_turns`
    value. (Whatever number it is — 1 in the success path, but the
    assertion is "they agree" not "it's 1".)

**Normalization rules — what we DO NOT compare and why:**

The test docstring documents these in a comment block above
`extractShapes`, so a future reader sees the full set without
spelunking. The rules are:

| Field | Stripped from comparison | Why |
| --- | --- | --- |
| `session_id` | All envelopes | Random per run (`sessions.NewID` for ptyrunner; claude-minted for streamrunner) |
| `ts`, `timestamp` | All envelopes | Wall-clock-dependent |
| `duration_ms`, `duration_api_ms` | result | Run-time-dependent |
| `total_cost_usd`, `usage.*` | result | Token counts depend on LLM response |
| `result` (the text body field) | result | LLM output text is non-deterministic |
| `uuid` | result (streamrunner only) | claude-internal request id; ptyrunner emitter doesn't have it |
| `api_error_status`, `modelUsage`, `permission_denials`, `fast_mode_state` | result (streamrunner only) | Present in claude's native pass-through; deliberately omitted by ptyrunner's `streamjson.Emitter` trailer — see `internal/agentrun/streamjson/emitter.go:251-273` |
| `usage.server_tool_use` | result (streamrunner only) | Same reason |
| `message.id` | assistant | claude-internal message id |
| `message.content[*].text` | assistant | LLM response text is non-deterministic |
| `message.content[*].id` (tool_use) | assistant, tool_use | claude-internal tool-call id |
| `tool_use_id` | tool_result | Same |
| `message.usage` | assistant | Token counts |
| `parentUuid`, `uuid`, `cwd`, `sessionId` | user (JSONL-native fields ptyrunner sees and streamrunner doesn't) | These are the JSONL file format's per-line metadata; streamrunner emits a minimal user envelope from its `userTurn` struct, ptyrunner re-emits claude's full JSONL line. Documented as a known structural delta — the test's shape-only comparison naturally ignores them. |

**The test does NOT strip these fields from the raw bytes** — that would
require parse-and-re-serialize on every line, which the assertion doesn't
need. The shape extraction only reads `type` + `subtype`; field-level
invariants 7-10 use targeted `Contains` / per-field decodes. The
normalization table above is for the *reader* (and for future debugging
when the test fails); the assertion code itself never materializes a
normalized form.

### Test 2 — failure-message ergonomics

On any assertion failure inside `compareShapes`, the test prints:

- The pretty-printed shape sequence from streamrunner (`%-15s %s` formatted).
- The pretty-printed shape sequence from ptyrunner.
- The first divergent index (or "lengths differ" if that's the cause).
- A 1024-byte truncated prefix of each raw stream for diagnostic context.

The point is to make a regression diagnosable in one read of the failure
output — no need to re-run with `-v` or instrument the test.

## Concurrency model

None. Both `Run` calls are synchronous. The test runs them sequentially.
No goroutines owned by the test.

The ptyrunner package's own internal goroutines (watchdog + tail watcher)
are stopped via the `Run` function's own defer chain by the time `Run`
returns — that's already tested in unit tests under
`internal/agentrun/ptyrunner/runner_test.go`. This test does not introduce
any new coordination layer.

## Error handling

- `claude` binary absence: `exec.LookPath("claude")` gate at the top of
  both tests (Test 1 directly, Test 2 implicit via `WithWorktreeAuthenticated`
  → `WithWorktree` → `streamrunner.Run`/`ptyrunner.Run` exec). Same `t.Fatalf`
  message as `smoke_test.go`.
- `ANTHROPIC_API_KEY` absence: handled by `WithWorktreeAuthenticated`'s
  existing `t.Skipf` path. Test 2 is opt-in; CI may not have the key.
- 90s `context.WithTimeout` on each runner. Timeout → context deadline →
  `streamrunner.Run` returns `nil` (its ctx-cancel-is-success contract) or
  `ptyrunner.Run` returns `nil` (same contract). The shape extraction then
  fails because the result trailer is missing — the failure message points
  at the truncated raw stream, which will reveal whether claude got hung
  pre-idle, mid-emit, or post-emit. The test does not directly assert
  `err != nil` on Run for the timeout path; it lets the structural
  assertions fail with a more informative message.
- Network/auth failures: claude exits non-zero, the stream-json output is
  truncated or absent, the shape extraction asserts `len(shapes) >= 4`
  fails with the truncated raw output embedded. Operator reads the stderr
  in the failure message (the test stops discarding stderr for diagnostics
  — see Testing strategy below).

**Test 2's `Stderr` choice:** the test captures stderr into a per-run
`bytes.Buffer` (NOT `io.Discard`) so the failure message can include it.
On the success path the stderr buffer is unread (no `t.Logf` call); on
failure the assertion prints the stderr alongside the stdout prefix.

## Testing strategy

The test file IS the test. There is no unit test for the helpers
(`extractShapes`, `compareShapes`, `streamRunnerArgs`) — they're so thin
that any bug shows up immediately in the e2e assertion. Adding unit tests
for the helpers would inflate the slice without adding signal; the
real-claude run is the test of the helpers.

Run:

```bash
make e2e-realclaude
# or
go test -tags=e2e_realclaude -run='TestPtyRunner' ./internal/e2e/realclaude/
```

Expected runtime: ~30-60s per `TestPtyRunnerVsStreamRunner_StructuralEquivalence`
invocation (two 5-15s real-claude turns); <1s for
`TestPtyRunnerArgvFlagsExistInClaudeHelp`.

CI: the existing realclaude suite gating (build tag + Makefile opt-in) is
sufficient. The new tests inherit the gate; no `go test ./...` change.

## Open questions

- **Should `TestPtyRunnerVsStreamRunner_StructuralEquivalence` use
  `WithWorktreeAuthenticated` (which `t.Skip`s on missing API key) or fail
  hard?** Skip is the right choice for the existing suite (other realclaude
  tests use the same gate; CI without the key skips cleanly). Decision:
  skip. The argv-flags test does NOT need the key — only the byte-equivalence
  test does.
- **Should the structural-equivalence assertion accept differing `num_turns`
  values across the two streams?** No — they should agree (the LLM produced
  the same number of turns from the same prompt, or both hit `max_turns=1`).
  If they disagree empirically on the first run, that's the signal #482 is
  designed to surface; the assertion failure becomes a knowledge-base note.
- **Tool-use variance.** With `allowedTools = ["Read"]` and a `"Reply with
  the single word OK"` prompt, claude haiku at low effort should not call
  Read. If it does, the type sequence diverges between runs (the runs are
  independent — the same prompt at different timestamps may produce different
  tool-use decisions due to model nondeterminism). Mitigation: the prompt
  is engineered to discourage tool use; haiku is the most deterministic
  model. If flake surfaces in practice, the follow-up is to either pin a
  seed (if the model supports it) or relax the assertion to "both streams
  end with a `result` trailer and the trailing subtype agrees" — the
  knowledge-base note records the observation. We do NOT pre-emptively
  relax the assertion; the strict shape-sequence comparison is what catches
  drift.

## Out of scope

- Per-pipeline-convention, **the developer does NOT write
  `docs/knowledge/codebase/482.md`** and does NOT update
  `docs/knowledge/INDEX.md`. Both are owned by the documentation phase,
  which runs post-merge and writes the codebase note from this spec + the
  merged diff. The ticket body lists the knowledge doc as an AC; this spec
  explicitly de-scopes it, matching the rule in the architect's CLAUDE.md
  ("Do NOT include `docs/knowledge/codebase/<N>.md` as an AC").
- No edits to `internal/agentrun/ptyrunner/` or `internal/agentrun/streamrunner/`.
- No edits to `cmd/pyry/agent_run.go` or any other production code path.
- No new exported identifiers in any package.
- No new test fixtures under `internal/e2e/realclaude/testdata/`.

## Implementation notes for the developer

- **Mirror the size estimate:** ~250-300 LOC in one new file. If your draft
  exceeds 400 LOC, stop and audit — you're probably re-parsing/re-stringifying
  fields you don't need to. The shape comparison reads only `type`/`subtype`;
  resist the urge to write a full normalization-and-canonicalize round trip.
- **Don't import `cmd/pyry`.** It's `package main`; the import will fail.
  Mirror `buildStreamRunnerClaudeArgs` in the test file with a block comment
  pointing at the source-of-truth line range.
- **Don't add an `_argv_test.go` separate file** for Test 1 — keep both
  tests in `ptyrunner_byte_equivalence_test.go` so the build-tag gate and
  the package context stay co-located.
- **Use `io.Discard` for streamrunner's Stderr on the happy path,
  `bytes.Buffer` capture for diagnostics on the failure path.** Either
  approach is fine — pick whichever produces clearer test code. If you go
  with capture-on-failure, lift the `cmd.Stderr` assignment behind a helper
  that returns the buffer and discards the contents on the success path.
- **`t.Cleanup` for `os.Remove(settingsPath)`**, not `defer`. Match the
  existing pattern in fixtures and let the test framework handle the order.
- **The `streamRunnerArgs` helper is local to the test file.** Don't refactor
  `cmd/pyry/agent_run.go` to export it — that's scope creep and the helper
  in this file serves a different purpose (test-local pinning vs runtime
  argv construction).
- **`reflect.DeepEqual` on `[]envelopeShape` is fine** — the shape struct
  is pure data, no pointers, no functions. The diff message you print on
  mismatch carries the burden of being readable.
- **Sequence: Test 1 first, Test 2 second in the file.** Test 1 doesn't
  need the API key, so a contributor without the key can verify Test 1
  locally; running the full file `t.Skip`s Test 2 cleanly.

## Acceptance criteria mapping

The ticket has four ACs. This spec maps to them as follows:

1. *"New real-claude smoke test under internal/e2e/realclaude/…"* —
   `TestPtyRunnerVsStreamRunner_StructuralEquivalence` in
   `internal/e2e/realclaude/ptyrunner_byte_equivalence_test.go`,
   build-tag-guarded, exec.LookPath gate via `WithWorktreeAuthenticated`'s
   downstream path (the runners themselves spawn `claude` and surface the
   exec failure; an explicit pre-check is unnecessary because the argv-shape
   test in the same file already runs `claude --help` and would fail first
   if the binary is missing).
2. *"If exact byte-equivalence is impractical…"* — structural equivalence
   via the `envelopeShape` extraction + the normalization table documented
   inline. Byte-equivalence is impractical (different field sets in the
   `result` trailer); the test never attempts it.
3. *"Verifies the argv shape produced by ptyrunner.buildArgs against `claude
   --help` output…"* — `TestPtyRunnerArgvFlagsExistInClaudeHelp` in the
   same file. Parses `claude --help` for the six flags. Does NOT pin the
   claude version (the AC offered either option; flag-set pinning is more
   durable).
4. *"Knowledge-base note docs/knowledge/codebase/482.md…"* — explicitly
   de-scoped per pipeline convention; see "Out of scope" above. The
   documentation phase owns this artifact.
