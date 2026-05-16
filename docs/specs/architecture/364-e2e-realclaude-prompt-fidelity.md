# Spec: e2e/realclaude prompt-fidelity regression guard (#364)

## Files to read first

- `internal/e2e/realclaude/fixtures.go` — `WithWorktree`, `ReadJSONL`,
  `RunPyryAgentRun`, `RunOpts`, `RunResult`, `JSONLEntry` (the alias to
  `jsonl.Event`). The whole helper surface this test composes over. No
  modifications.
- `internal/e2e/realclaude/fixtures_test.go:55-86` — `TestReadJSONL_HappyPath`
  and the `writeFixtureLines` helper at lines 325-347. Same package; the new
  test can call `writeFixtureLines` if it ever needs a fixture, but it
  shouldn't — this test runs real claude end-to-end.
- `internal/e2e/realclaude/smoke_test.go` — only file in the package that
  exercises a real binary; mirror its build-tag header (`//go:build
  e2e_realclaude`).
- `internal/agentrun/jsonl/reader.go:41-83` — `Event` struct. Two fields
  matter for the assertion: `Kind` (string, "user" / "assistant" / ...) and
  `Raw` (`json.RawMessage`, the verbatim line bytes with the trailing `\n`
  stripped). `Raw` is the substring-match target.
- `internal/agentrun/jsonl/reader.go:134-167` — `rawLine` plus the comment at
  line 137 ("user lines have content as a string, not an array"). Documents
  why the assertion must tolerate both shapes — `Raw` does this naturally.

## Context

`pyry agent-run` was cut over to streamrunner in #391; the prompt now travels
as a single `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<prompt>"}]}}`
envelope on stdin, so the 2026-05-14 `/doctor`-poisoning failure mode is
structurally impossible. This ticket is a **regression guard**: a real-claude
e2e test that pins the prompt-handoff contract end-to-end. If anyone later
re-introduces preprocessing, breaks the envelope's text round-trip, or wires
a path that bypasses streamrunner, this test catches it before merge.

The fixture helpers landed in #372 (`WithWorktree`, `ReadJSONL`) and #373
(`RunPyryAgentRun`). The test body is structurally a fixture composition.

## Design

One new file: `internal/e2e/realclaude/prompt_fidelity_test.go`. Build-tagged
`//go:build e2e_realclaude` so `make test` skips it; runs under
`make e2e-realclaude` only. No production-code changes.

### Test shape

`TestRealClaude_PromptFidelity` performs four phases in sequence:

1. **Allocate worktree.** `workdir := realclaude.WithWorktree(t)`. Pins
   `$HOME` to the temp dir; the subprocess writes its JSONL under
   `<workdir>/../<tmpHome>/.claude/projects/<encoded-workdir>/` — same root
   as `os.UserHomeDir()` returns in-test.

2. **Run pyry agent-run with a distinctive literal prompt.**

   - Prompt literal: a single ASCII token unlikely to appear in any system
     prompt or in claude's response template. **Choose**
     `INTEGRATION_TEST_PROMPT_PROMPTFIDELITY_2N7Q4R8W` and assign it to a
     package-private `const distinctivePrompt = ...` so a future reader sees
     why it's odd. Token requirements: ASCII letters+digits only (no
     punctuation that JSON would have to escape — keeps the substring match
     deterministic against `Raw`).
   - `RunOpts`:
     - `Workdir`: from step 1
     - `Prompt`: `distinctivePrompt`
     - `SystemPrompt`: `"You are an e2e regression-guard test. Reply with a single short word."`
       (any non-empty value satisfies `validateRunOpts`; this phrasing also
       discourages claude from echoing the prompt back, which would muddy
       a future test that needs to distinguish input from output)
     - `AllowedTools`: `[]string{"Read"}` (minimum non-empty)
     - `MaxTurns`: `1`
     - `Effort`: `"low"`
     - `Model`: `"claude-haiku-4-5"` (matches the model literal used
       elsewhere in the package's tests; ticket body says `--model haiku`,
       and the CLI's resolver maps short names — but stick to the literal
       used in `fixtures_test.go:163` for consistency)
     - `ExtraEnv`, `Timeout`: omit (5-min default is more than enough)

3. **Assert run-level success first.** Before opening JSONL:

   - `result.ExitCode == 0` — `t.Fatalf` with stderr on miss
   - `result.SessionID != ""` — `t.Fatalf` with a hint that the init line
     wasn't found

   Stating these first guarantees that a fixture/setup failure produces a
   readable message instead of a "no user entry" assertion that buries the
   real cause.

4. **Assert prompt fidelity in JSONL.**

   - `events := realclaude.ReadJSONL(t, workdir, result.SessionID)`
   - Walk `events` for the first `e.Kind == "user"`.
   - **Found:** assert `bytes.Contains(e.Raw, []byte(distinctivePrompt))`.
     On miss, `t.Fatalf` with the JSONL path (computed via
     `agentrun.EncodeProjectDir` — same pattern used by `writeFixtureLines`)
     and `string(e.Raw)` so a future debugger sees the actual envelope.
   - **Not found:** `t.Fatalf` listing every `events[i].Kind` joined by `,`
     plus the resolved JSONL path. No event bodies in the dump — kind tags
     alone tell a future debugger which event-class delivered the prompt,
     and skipping bodies avoids accidentally logging anything sensitive
     that downstream tests might add later.

### Why `Raw` instead of decoding `message`

`jsonl.Event` exposes `Kind` but only assistant-shaped fields are decoded
out of `message` (`StopReason`, `TextChars`, `Usage`). A user line's
`message.content` field can be either a string or a `[{type:"text",text:...}]`
array (see `reader.go:137` comment), so any new decoding here either
duplicates parser code or grows it. `Raw` is the verbatim line — a
`bytes.Contains` against it is true iff the prompt literal appears anywhere
in the JSON, regardless of which content shape claude used that day.

This is fine because the prompt literal is distinctive enough that any
substring match implies the real text round-tripped. A future migration to
binary content blocks would invalidate the assertion shape; that's the
correct moment to revisit, not now.

### No new exports, no production changes

The test consumes only existing realclaude package exports. The helper
package itself is untouched.

## Concurrency model

None. Single-goroutine test that subprocess-execs `pyry agent-run` (already
guarded by the helper's `context.WithTimeout`) and then reads a static file.

## Error handling

All error paths are `t.Fatalf` — there's no recovery to attempt in a
regression-guard. The two non-obvious failure messages:

- **`SessionID == ""`** → include `string(result.Stdout)` truncated to e.g.
  1 KiB so the developer can see whether claude emitted anything at all.
  (Use `string(result.Stdout)` directly if `< 1024` bytes; else slice and
  append `... (truncated)`.) This is the most common headless failure mode
  (no API key, network down) and the cheapest to diagnose from log output.
- **No user event found** → comma-joined `Kind` values plus resolved JSONL
  path. A reader can `cat` the file and see exactly what claude wrote.

Nothing else needs custom phrasing; the helpers' own `t.Fatalf` messages
(timeout, validation, build) already embed all necessary context.

## Testing strategy

The test IS the test. It is its own regression guard. Verification at
implementation time:

- `go vet ./...` — must pass with the `e2e_realclaude` tag set
  (`go vet -tags=e2e_realclaude ./internal/e2e/realclaude/...`).
- `go build -tags=e2e_realclaude ./internal/e2e/realclaude/...` — must
  compile.
- `make e2e-realclaude` (or the equivalent `go test -tags=e2e_realclaude
  -run TestRealClaude_PromptFidelity ./internal/e2e/realclaude/...`) — must
  pass against a working `claude` binary and a live API key. **One real
  haiku call per run.** Developer should confirm a single successful local
  invocation; do not loop.
- `make test` — must still pass (i.e. the new file must be invisible to
  the default test build).

## Open questions

None. The ticket body has resolved every prior ambiguity (single mode, no
parametrization; helpers supply the path; substring match against `Raw`).

## Out of scope

- No assertion on the assistant reply. (#365 and siblings will cover
  response shapes.)
- No assertion on `result.Stdout` stream-json content beyond what
  `RunPyryAgentRun` already parses for the session ID.
- No new fixture helpers. If a future test needs to look up a `user` entry
  in JSONL, that pattern can be extracted then — not preemptively.
