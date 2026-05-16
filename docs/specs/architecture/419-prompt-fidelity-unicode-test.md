# Spec: e2e/realclaude — UTF-8 prompt fidelity round-trip test (#419)

## Files to read first

- `internal/e2e/realclaude/prompt_fidelity_test.go` (whole file, ~90 LoC) — the template to clone. The new test mirrors this 1:1 except for the prompt literal and the test function name. Note in particular:
  - `const distinctivePrompt = "..."` at line 18 — the literal-constant pattern to copy
  - the early returns on `result.ExitCode != 0` and `result.SessionID == ""` (lines 41–52) — copy verbatim, they are the same failure modes
  - the `for e := range events { if e.Kind == "user" ... }` loop (lines 57–65) — copy verbatim
  - the trailing "no user entry found" diagnostic with the `kinds` slice (lines 67–72) — copy verbatim
  - `jsonlPathFor` at line 79 is an unexported, package-scoped helper. Reuse it directly from the new file (same `package realclaude`, same build tag) — do NOT duplicate it.
- `internal/e2e/realclaude/fixtures.go:83-116` — `RunOpts` field set and `RunResult` shape. Confirms `Workdir`, `Prompt`, `SystemPrompt`, `AllowedTools`, `MaxTurns`, `Effort`, `Model` are all available; no new fields required.
- `internal/e2e/realclaude/fixtures.go:32-78` — `WithWorktree` and `ReadJSONL`. Confirms the same fixture surface used by the ASCII test is also what the new test consumes.
- `Makefile:28-30` — the `e2e-realclaude` target. It runs `go test -tags e2e_realclaude ./internal/e2e/realclaude/...`, so a new `*_test.go` file under the same build tag is picked up automatically. No Makefile changes needed.

## Context

The ticket body is authoritative. In short: the existing prompt-fidelity test uses an ASCII-only literal by design ("unlikely to appear in any system"). That pins the prompt-handoff path but not UTF-8 byte preservation. A regression in JSON escape handling, re-encoding, or buffer slicing on a multi-byte UTF-8 boundary would land silently. Finnish-speaking users are a real cohort; non-ASCII input is realistic, not edge-case.

## Design

### One new file

Create `internal/e2e/realclaude/prompt_fidelity_unicode_test.go`. Same `package realclaude`, same `//go:build e2e_realclaude` tag, same imports as the existing file minus whatever is unused.

Do not modify `prompt_fidelity_test.go`. Do not modify `fixtures.go`. Do not modify the Makefile.

### Prompt literal contract

One package-level constant, defined in the new file, of the shape:

```
ASCII_FRAME_OPEN <non-ASCII payload> ASCII_FRAME_CLOSE
```

Concrete requirements:

- The ASCII frame token mirrors the existing literal's naming convention so the two tests stay greppable and visually parallel. Suggested form: `INTEGRATION_TEST_PROMPT_UNICODEFIDELITY_<random-8-char-suffix>` for both the opening and closing frame (different suffixes for open vs close are fine; the AC just needs *a* distinctive ASCII frame).
- The non-ASCII payload MUST contain, in a single Go string literal:
  - Finnish lowercase `ä`, `ö`, `å` (U+00E4, U+00F6, U+00E5 — 2-byte UTF-8 sequences each)
  - The Unicode em-dash `—` (U+2014 — 3-byte UTF-8)
  - The emoji `🐍` (U+1F40D — 4-byte UTF-8, outside the BMP; this is the byte-slicing canary)
- The literal is written as a normal double-quoted Go string with the characters literally present in the source. Source file is UTF-8 (Go default). Do **not** use `\uXXXX` escapes — the point is to assert the bytes we typed in the source survive end-to-end. Do not normalize (no NFC/NFD massaging); send the bytes as written.
- Wrap the payload so that the matched substring is the *full* `frame_open + payload + frame_close` string. The AC's "if the prompt is dropped entirely the frame match also fails" property is what gives the substring search its diagnostic power: a passing test means the whole framed literal made it through, a failing test on the frame alone says "prompt dropped", a failing test on the framed-payload-but-passing-frame-alone would say "UTF-8 corruption on a boundary" — but the single assertion already distinguishes these two by which substring is searched, and the spec keeps the test to a single `bytes.Contains` call against the framed literal.

### Test function

`func TestRealClaude_PromptFidelity_Unicode(t *testing.T)`. Body is structurally identical to `TestRealClaude_PromptFidelity`:

- `workdir := WithWorktree(t)`
- `result := RunPyryAgentRun(t, RunOpts{...})` with the same `Model: "claude-haiku-4-5"`, `MaxTurns: 1`, `Effort: "low"`, `AllowedTools: []string{"Read"}`, and a `SystemPrompt` along the same lines as the existing test ("regression-guard test, reply with a single short word"). Cost is bounded by the same model/turn settings — ticket budget is ~$0.01/run.
- The same exit-code and empty-session-ID early returns as the ASCII test.
- `events := ReadJSONL(t, workdir, result.SessionID)`
- `jsonlPath := jsonlPathFor(workdir, result.SessionID)` — reuses the existing helper.
- Iterate events, on the first `e.Kind == "user"` run `bytes.Contains(e.Raw, []byte(<full framed literal>))`. On match, return. On miss, `t.Fatalf` with the `path`, the `literal`, and `string(e.Raw)` — same diagnostic shape as the existing test.
- If no `user` entry is found, the same trailing `t.Fatalf` with `path` and the comma-joined `kinds` slice.

That is the entire test body. No helper functions, no new types.

### Behaviour contract — one `bytes.Contains` call

The AC pins exactly one assertion shape: `bytes.Contains(e.Raw, []byte(<literal>))` against the first `user` entry. Do not split into "frame check" + "payload check" or anything cleverer. The single call is the contract; the AC's diagnostic shape is satisfied by the failure message printing the raw event and the literal.

## Concurrency model

N/A. Single-goroutine `testing.T` body; same as the existing test.

## Error handling

Three failure modes, all already covered by the cloned structure:

1. `pyry agent-run` exits non-zero → `t.Fatalf` with `result.ExitCode` and `result.Stderr` (literal copy of existing test's lines 41–43).
2. Stream-json never produced a session/init line → `result.SessionID == ""`, `t.Fatalf` with truncated stdout (literal copy of lines 44–52).
3. JSONL has no `user` entry, OR the first `user` entry's raw bytes do not contain the framed literal → `t.Fatalf` with `path` + diagnostic payload.

No new error paths. No new fixture work.

## Testing strategy

The test IS the regression guard; there is nothing meta to test. Verification is:

1. `go vet ./...` passes.
2. `go test -tags e2e_realclaude ./internal/e2e/realclaude/... -run TestRealClaude_PromptFidelity_Unicode` passes against the real `claude` CLI in the developer's worktree, as the ticket explicitly requires before opening the PR.
3. `make e2e-realclaude` runs both the existing ASCII test and the new Unicode test (the Makefile target runs the package, not a specific test name).

## Open questions

- **JSON-escape risk.** If claude's JSONL writer were to escape non-ASCII bytes as `ä` / `—` / surrogate pairs for `🐍` instead of preserving raw UTF-8, the `bytes.Contains` call would fail. The AC accepts this risk explicitly: the ticket instructs the developer to run `make e2e-realclaude` locally before opening the PR, and the test's purpose is precisely to assert byte-identical preservation. If the test fails on first run against the real CLI:
  - Do **not** change the assertion to a decoded-payload check. The byte-strict contract is the point.
  - Do **not** add a fallback. Surface the finding on the ticket and stop.

  This is the only thing in the spec where the developer might be tempted to "fix" the test against an unexpected outcome. The right answer is to report the result; the right answer is never to weaken the assertion.

- **Test name suffix vs. new file.** The AC offers both a sibling-test-in-same-file and a new-file option. This spec picks the new file (`prompt_fidelity_unicode_test.go`) because it isolates the diff, avoids touching the existing test's literal/constants, and keeps `git blame` on the ASCII test clean. No technical objection to the alternative.
