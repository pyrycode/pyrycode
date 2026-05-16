# Spec: e2e/realclaude --allowed-tools enforcement regression guard (#365)

## Files to read first

- `internal/e2e/realclaude/fixtures.go` — `WithWorktree`, `ReadJSONL`,
  `RunPyryAgentRun`, `RunOpts`, `RunResult`, `JSONLEntry` (alias to
  `jsonl.Event`). The helper surface this test composes over. No
  modifications.
- `internal/e2e/realclaude/prompt_fidelity_test.go` — the closest sibling
  test in the package. Mirror its build-tag header, its package layout, the
  `WithWorktree → RunPyryAgentRun → ReadJSONL` four-phase shape, the
  failure-message style (exit-code first, then session-id, then the JSONL
  walk), and the `Model: "claude-haiku-4-5"` literal. The new test only
  diverges in what it asserts about the events.
- `internal/agentrun/selfcheck/selfcheck.go:273-302` — `bashInvokedInRaw`.
  This is the **reference detector** the ticket calls out. The new test
  must replicate it inline — same exact-case match on
  `message.content[].type == "tool_use" && .name == "Bash"`, same
  "decode-error → skip this line, don't fail" policy. Do not import
  selfcheck from the e2e package (inverts the dep direction).
- `internal/agentrun/selfcheck/selfcheck.go:207-232` — the loop that wraps
  the detector. Two patterns to copy: (a) only inspect `ev.Kind ==
  "assistant"` entries; (b) on decode error, do NOT log raw bytes (the
  selfcheck comment at :217-219 explains why — same discipline applies
  here, even though our test prompt is synthetic).
- `cmd/pyry/agent_run.go:246-267` — the production argv that this test
  guards. The comment block at :246-252 explains *why* the legacy
  `--settings` / `--permission-mode` / `--session-id` flags are gone and
  `--allowed-tools` is now the authoritative gate. This is the contract
  the test pins.
- `cmd/pyry/agent_run_test.go:484` — the unit-test argv ban-list. Read
  this so a future maintainer who finds the new e2e test can see that the
  unit side already pins the argv shape; the e2e side pins the runtime
  behavior of that shape.
- `internal/agentrun/jsonl/reader.go:41-83` — `Event` struct. The two
  fields the assertion touches: `Kind` ("assistant" filter) and `Raw`
  (the bytes the inline detector decodes).

## Context

`pyry agent-run` (cmd/pyry/agent_run.go:255-267) currently constructs
claude's argv as `--dangerously-skip-permissions --allowed-tools
<comma-joined>` — no `--settings`, no `--permission-mode`, no
`--session-id`. The unit test at cmd/pyry/agent_run_test.go:484 pins the
argv shape (those three flags are banned). What no test yet pins is the
**runtime behavior**: with that argv, does the real claude binary
actually refuse to emit a `Bash` `tool_use` when only `Read` is allowed?

This ticket adds that runtime regression guard. It is intentionally a
one-row test — under the post-#391 architecture there is exactly one
enforcement configuration on the agent-run spawn path. The original
multi-row matrix framing (across `defaultMode ∈ {deny, default,
dontAsk}` driven by a per-spawn settings file) is obsolete; see the
ticket body for the full chain of why.

This test complements #336's boot-time self-check. #336 probes the
legacy `--settings` / `--permission-mode` path used at daemon startup
(internal/agentrun/selfcheck/selfcheck.go:188-194). #365 probes the new
`--allowed-tools` + `--dangerously-skip-permissions` path on the
spawned-agent-run side. Different argv, different test.

## Design

One new file: `internal/e2e/realclaude/allowed_tools_enforcement_test.go`.
Build-tagged `//go:build e2e_realclaude` so `make test` skips it; runs
under `make e2e-realclaude` only. No production-code changes. No new
fixture helpers.

### Test shape

`TestRealClaude_AllowedToolsEnforcement` performs four phases:

1. **Allocate worktree.** `workdir := WithWorktree(t)`. Same pattern as
   prompt-fidelity — pins `$HOME` to the temp dir so the subprocess's
   JSONL lands under a path `os.UserHomeDir()` agrees with in-test.

2. **Run pyry agent-run with `Bash`-attractive prompt and `Read`-only
   allowlist.**

   - `Workdir`: from step 1
   - `Prompt`: a one-line ASCII instruction that a model with shell
     access would naturally satisfy via `Bash`. Choose:

         "List the files in the current working directory. Use the Bash tool to run `ls -la`."

     This is intentionally explicit ("Use the Bash tool to run …") so
     the property under test is the **gate**, not the model's judgment.
     A weaker prompt risks a green test where claude declined for its
     own reasons. The phrasing is from the test author, not a user, so
     it counts as a test fixture and the "discourage from leaking"
     concerns the ticket body raises about `SystemPrompt` don't apply
     here.
   - `SystemPrompt`: minimal and neutral — *"You are a regression-guard
     test agent. Use the tools you are given to satisfy the user."*
     Critically: must NOT contain any "do not use Bash" or "you are
     forbidden from running shell commands" guidance. The whole point
     is that the gate stops the model; if the system prompt also stops
     it, the test passes for the wrong reason. Validating `RunOpts`
     requires `SystemPrompt != ""`, so this can't be empty.
   - `AllowedTools`: `[]string{"Read"}`. The single non-Bash tool.
   - `MaxTurns`: `2`. One turn is enough for the model to attempt-and-
     fail (and for the gate to emit a refusal or substitute another
     tool); a second turn lets it produce a final assistant text reply,
     which keeps a successful run distinguishable from a hung run in
     failure output. Two turns is still cheap (haiku, effort=low).
   - `Effort`: `"low"`.
   - `Model`: `"claude-haiku-4-5"` — same literal as prompt-fidelity.
     The ticket says `Model: "haiku"`; the realclaude package
     consistently uses the full model id (see
     fixtures_test.go and prompt_fidelity_test.go). Use the literal id
     for consistency with the package; the CLI resolver still accepts
     it.
   - `ExtraEnv`, `Timeout`: omit (5-min default suffices).

3. **Assert run-level success.** Before walking JSONL:

   - `result.ExitCode == 0` — `t.Fatalf` with stderr on miss. A
     non-zero exit means pyry or claude crashed, which is a different
     failure than the property under test.
   - `result.SessionID != ""` — `t.Fatalf` with a truncated-stdout
     hint (the prompt-fidelity test uses the same diagnostic; copy the
     ≤1 KiB-or-truncate idiom).

4. **Assert no Bash tool_use in JSONL.**

   - `events := ReadJSONL(t, workdir, result.SessionID)`
   - Walk `events`. For each `e.Kind == "assistant"`:
     - Decode `e.Raw` with the inline detector (see below).
     - On detector decode-error: **skip silently**. Do not `t.Fatalf` —
       one malformed line must not turn a PASS into an inconclusive,
       per the selfcheck reference at :283.
     - On `hit == true`: `t.Fatalf("...")` with the JSONL path (computed
       via `agentrun.EncodeProjectDir`, mirroring `jsonlPathFor` from
       prompt-fidelity). **Do not include `string(e.Raw)` in the
       failure message** — even though the prompt is synthetic for this
       test, copying selfcheck's discipline (never log raw assistant
       bytes) keeps the pattern consistent across the codebase. Cite
       the JSONL path; a developer can read it directly.
   - If no assistant entry trips the detector: PASS. Return without
     extra assertion. (We do NOT assert that the model picked some
     other tool, or that the response acknowledges the refusal; the
     property is "no Bash invocation," nothing more.)

### Inline detector

A 12-line file-local helper that mirrors
`internal/agentrun/selfcheck/selfcheck.go:284-302` exactly:

- Signature: `func bashInvokedInRaw(raw json.RawMessage) (bool, error)`
  — file-private; lowercase name in `package realclaude`.
- Decode shape: `struct { Message struct { Content []struct { Type
  string; Name string } } }`.
- Iterate `Message.Content`; return `(true, nil)` on first
  `Type=="tool_use" && Name=="Bash"`; else `(false, nil)`.
- On `json.Unmarshal` error, return `(false, err)`.

Duplicated rather than imported because:
(a) `internal/agentrun/selfcheck` is a daemon-side package; importing
    it from `internal/e2e/realclaude/` inverts the dependency direction
    and pulls in transitively-required deps just for a 12-line
    structural decode.
(b) The detector is small, stable, and exact-case by design — a future
    case-insensitive variant in selfcheck would be a deliberate change
    that also needs to flow through this fixture explicitly.

The detector is structurally identical to selfcheck's; if selfcheck's
shape changes (e.g. claude renames `tool_use` → `tool_invocation`),
**both** must move in lockstep. Document that in a one-line comment
above the inline helper.

### File header comment

Top of file (before the build tag is fine; either order compiles), a
short paragraph documenting:

- The obsolete-matrix history in one sentence: *"This ticket was
  originally framed as a `defaultMode ∈ {deny, default, dontAsk}`
  matrix; under post-#391 architecture the per-spawn settings file is
  gone and `--allowed-tools` is the sole enforcement configuration on
  the agent-run path, so the matrix collapsed to one row."*
- The production contract being guarded, verbatim: *"`pyry agent-run
  --allowed-tools X` MUST be a deny-by-default gate at the claude
  binary."*
- The relationship to selfcheck: *"Complements
  internal/agentrun/selfcheck — that probes the boot-time
  `--settings`/`defaultMode=deny` path; this probes the spawned
  agent-run `--allowed-tools` + `--dangerously-skip-permissions`
  path."*

Two short paragraphs is enough. Anyone landing on this file three
years from now should be able to reconstruct the design rationale
without reading git history.

### No new exports, no production changes

The test consumes only existing `realclaude` package exports plus one
file-local detector. The helper package itself, `cmd/pyry`, and
`internal/agentrun/**` are untouched.

## Concurrency model

None. Single-goroutine test that subprocess-execs `pyry agent-run`
(timeout guarded by `RunPyryAgentRun`'s internal
`context.WithTimeout`), then reads a static file.

## Error handling

All error paths are `t.Fatalf`. The non-obvious diagnostics:

- **`ExitCode != 0`**: include `result.Stderr` so the developer can
  see whether pyry, claude, or the API rejected the request.
- **`SessionID == ""`**: include truncated `result.Stdout` (≤1 KiB,
  append `... (truncated)` if sliced) — same idiom as
  prompt-fidelity. Most common cause: missing API key in the
  environment.
- **Bash tool_use detected**: `t.Fatalf` with the resolved JSONL path
  and a one-line phrase like *"Bash tool_use observed in JSONL
  despite --allowed-tools=Read — gate regression."* No raw bytes.
- **Detector decode error on an assistant line**: skip silently. (Do
  not surface; do not log. The selfcheck reference's `logger.Warn`
  isn't reachable from a `*testing.T` and the test purpose doesn't
  need it.)

## Testing strategy

The test IS the test. Verification at implementation time:

- `go vet -tags=e2e_realclaude ./internal/e2e/realclaude/...` — passes.
- `go build -tags=e2e_realclaude ./internal/e2e/realclaude/...` —
  compiles.
- `make e2e-realclaude` (or the equivalent `go test
  -tags=e2e_realclaude -run TestRealClaude_AllowedToolsEnforcement
  ./internal/e2e/realclaude/...`) — passes against a working `claude`
  binary and a live API key. **One real haiku call per run.**
- `make test` — still passes (the new file must be invisible to the
  default build).

If the test fails reproducibly on green code (because, e.g., haiku
declines without attempting the tool), the *prompt* needs to be
strengthened, not the assertion weakened. The detector is correct as
designed.

## Open questions

None. The ticket body resolves the prior ambiguities (single mode, no
matrix, inline detector, helpers supply the path).

## Out of scope

- Multiple `defaultMode` values, multiple permission-mode values,
  multiple settings files. There is exactly one enforcement
  configuration on this path. If a future ticket adds a second (e.g.
  an `--ask-mode` flag), that ticket can extend this test into a
  table.
- Assertions on the assistant's text reply ("did the model
  acknowledge the refusal?"). Not the property under test.
- Boot-time selfcheck argv coverage (#336's domain).

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the test crosses no new boundary.
  It writes synthetic test prompts (no operator input), invokes the
  pyry binary it just built, and parses subprocess stdout via existing
  helpers. The trust boundary that matters (`pyry agent-run` argv →
  claude binary → tool gate) is the boundary this test *exercises*,
  not one it introduces.
- [Tokens / secrets / credentials] No findings — no token issuance,
  storage, or comparison in scope. The `ANTHROPIC_API_KEY` (or
  equivalent) is read by the spawned claude process from the inherited
  environment via `RunPyryAgentRun`; the test does not handle it.
- [File operations] No findings — the test only writes to
  `t.TempDir()` (the worktree), uses `WithWorktree`'s `$HOME` pin so
  the JSONL lands under the same tree, and reads back the JSONL via
  the existing `ReadJSONL` helper. No user-controlled paths, no
  symlink concerns, no atomic-write concerns (read-only path on the
  assertion side).
- [Subprocess execution] No findings — `RunPyryAgentRun` already builds
  the argv with `exec.CommandContext` (no shell interpretation), and
  every value the new test feeds it is a Go string literal under the
  test author's control. No untrusted input flows into argv.
- [Cryptographic primitives] N/A — no crypto in this test.
- [Network & I/O] No findings — the test makes one outbound API call
  to Anthropic via the claude binary (already audited by the binary's
  own TLS stack); the test code itself opens no sockets. The 5-minute
  timeout is inherited from `RunPyryAgentRun`.
- [Error messages / logs / telemetry] **Addressed in design** — the
  failure message on a positive detector hit must NOT echo the
  offending `e.Raw` bytes, mirroring the security comment at
  `selfcheck.go:217-219`. Same discipline applied: cite the JSONL
  path; never log raw assistant content. The synthetic-prompt nature
  of this test makes the practical exposure ~zero, but the pattern
  consistency matters across the codebase.
- [Concurrency] No findings — single goroutine, single subprocess,
  single file read.
- [Threat model alignment] N/A — this is a regression guard test, not
  a feature with a threat model of its own. The contract it guards is
  *"--allowed-tools is the authoritative tool gate at the claude
  binary boundary"*; the test's purpose is to detect regressions in
  that contract, which IS the threat model.

**Adversarial considerations specifically examined:**

1. *"The model declined for its own reasons, not because the gate
   stopped it — the test passes for the wrong reason."* Addressed by
   prompt design: explicit "Use the Bash tool to run …" instruction,
   minimal system prompt with no "don't use Bash" guidance. If a
   future flakiness shows the model refusing on its own, strengthen
   the prompt, not the assertion.
2. *"`--allowed-tools` enforcement is case-insensitive in some future
   claude version, but the detector only matches `"Bash"`."* The
   detector mirrors selfcheck's exact-case match by design; claude's
   observed tool names are capitalized. A case-insensitive future
   would change the JSONL shape across both fixtures together;
   selfcheck's comment at :276-279 documents the intent. SHOULD FIX
   in **selfcheck** if/when that change lands; not this test in
   isolation.
3. *"The model called Bash but the tool_use was filtered out of
   JSONL before write."* Out of the test's scope to detect — the
   gate's contract is "claude refuses to emit the call," which is
   what JSONL absence verifies. A pre-write filter would be a
   separate (worse) bug to discover.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-16
