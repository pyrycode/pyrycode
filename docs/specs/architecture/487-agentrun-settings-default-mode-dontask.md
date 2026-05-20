# 487 — agent-run/settings: replace invalid `permissions.defaultMode` literal "deny" with "dontAsk"

**Size:** XS (override of PO's `size:s`). Single literal flip in one production file, mechanical doc/comment/operator-string updates across three other production files, three byte-assertion updates in one existing unit test, one pinned-substring update in another existing unit test, one new realclaude regression test.

**Security-sensitive:** yes (label on ticket). Inline review at end of this spec; verdict: PASS.

## Files to read first

- `internal/agentrun/settings/settings.go:1-86` — the entire file. The literal flip lives at line 72; the doc-comment lies at lines 5, 23, 40 must move in lockstep.
- `internal/agentrun/settings/settings_test.go:67-141` — golden-bytes tests at lines 80, 100 and the round-trip `DefaultMode` check at lines 138-139. These are the byte assertions to update.
- `cmd/pyry/agent_run.go:218-235` — short-circuit comment at line 227 names the literal `"deny"`.
- `cmd/pyry/agent_run_selfcheck.go:80-108` — `writeSelfCheckFailMessage` emits the literal `permissions.defaultMode: "deny"` to operator stdout at line 92; the helper's doc-comment (lines 80-86) is pinned by `TestRunAgentRunSelfCheck_FAIL`.
- `cmd/pyry/agent_run_selfcheck_test.go:55-96` — the FAIL-message regression test; line 83 pins the exact substring `permissions.defaultMode: "deny"` that the operator-output line emits.
- `internal/agentrun/selfcheck/selfcheck.go:1-94` — package comment line 4 and the `canonicalPrompt` rationale at lines 65-69 reference the literal `"deny"`. Doc-only — no behaviour here changes; `canonicalAllow` and `ErrBashInvoked` stay byte-identical.
- `internal/e2e/realclaude/allowed_tools_enforcement_test.go` — the structural template for the new regression test. Reuse `WithWorktree*`, `RunPyryAgentRun`, `ReadJSONL`, and the `bashInvokedInRaw`/`structuredDenialHit` pattern of "decode line-by-line, skip parse errors silently". Do NOT mirror that test's gate-held detector — the new test asserts on the **first `user` JSONL entry**, not on `assistant` entries.
- `internal/e2e/realclaude/fixtures.go:1-78` — `WithWorktree` (re-pins HOME), `WithWorktreeAuthenticated` (re-pins HOME, requires `ANTHROPIC_API_KEY`, skips otherwise), `ReadJSONL`, `RunPyryAgentRun`, `RunOpts`. Use as-is — do not extend the fixture surface in this ticket (the wider Max-auth-vs-API-key fixture cleanup is OOS per ticket body).
- `internal/e2e/realclaude/smoke_test.go:1-25` — `TestClaudeBinaryAvailable` is the suite-wide PATH gate; the new test inherits its protection (build-tag `e2e_realclaude` is the same).
- Issue #487 body, "Reproduction" section — the manual reproduction script + the verbatim `/doctor` template substring (`"Help me fix the issues reported by /doctor below."`) that the new test asserts is NOT present.

## Context

`internal/agentrun/settings/settings.go:72` hardcodes `DefaultMode: "deny"` in the per-spawn permissions JSON. Claude 2.1.145 (the production binary as of 2026-05-20) rejects `"deny"` at startup with:

> Settings (...): Invalid value. Expected one of: "acceptEdits", "auto", "bypassPermissions", "default", "dontAsk", "plan"

`"deny"` was never a valid `permissions.defaultMode` value per Anthropic's docs. The Phase A spike (2026-05-14) that introduced the literal picked it by guessing; the empirical "verified — Bash blocked" finding was a false positive (claude silently fell back to `"default"` mode, which happened to refuse the Bash call in the spike's prompt).

Two compounding production failures result:

1. **Permission gate silently broken.** Whatever fallback mode claude lands on under an invalid `defaultMode` is what's actually enforced — not the intended deny-by-default + per-spawn allowlist contract. Headless dispatcher runs may execute tools the agent's whitelist would have refused.

2. **`/doctor` poisoning reopened on the ptyrunner path.** When claude rejects the settings JSON at startup, it prepopulates the input buffer with a `/doctor` repair template. ptyrunner's `Session.WritePrompt` bracketed-paste delivery does NOT preempt this — claude processes the `/doctor` template as the user message instead of pyry's prompt. (The earlier "structurally impossible after Phase B's stream-json pivot" claim was correct only for streamrunner; ptyrunner reintroduced an invalid settings file in Phase C/D and reopened the door.)

Per Anthropic's docs, the documented value for headless deny-default semantics — and the value that survives claude's settings parser — is **`"dontAsk"`**:

> Don't ask mode (`dontAsk`) converts any permission prompt into a denial. Tools that are pre-approved by `allowed_tools`, `settings.json` allow rules, or a hook will still run as normal. Everything else is denied without calling `canUseTool`.

The fix is a single string-literal flip with mechanical doc/comment/operator-string updates that lock the package comment, the operator-visible FAIL message, and the comments in the two callers in step with the new literal. A new realclaude regression test guards against silent recurrence: it runs `pyry agent-run --allowed-tools=Read` against `claude-haiku-4-5` and asserts the first `user` JSONL entry's `content` is the operator's prompt — not a `/doctor` repair template.

## Design

### Single-literal behaviour change

`internal/agentrun/settings/settings.go:72` — flip the string literal from `"deny"` to `"dontAsk"`. Nothing else in the package's structure, validation, error handling, tempfile naming, or cleanup contract moves. The struct field order (`Allow` then `DefaultMode`) is preserved, so the encoded byte sequence remains shape-identical except for the literal:

```
before: {"permissions":{"allow":[<tools>],"defaultMode":"deny"}}\n
after:  {"permissions":{"allow":[<tools>],"defaultMode":"dontAsk"}}\n
```

The Go struct tag `json:"defaultMode"` stays unchanged — the key name is not affected, only the value.

### Mechanical comment/string updates that must move in lockstep

These references quote the literal `"deny"` and must follow the flip. They are NOT semantic claims about the contract (the contract IS still deny-by-default — `dontAsk` produces deny-by-default behaviour), so phrases like "deny-default whitelist", "deny-default settings file", and the variable name `canonicalAllow` stay unchanged. Only references to the literal JSON value need updating.

| File | Lines | Current | Updated to |
|---|---|---|---|
| `internal/agentrun/settings/settings.go` | 5 | `--settings with defaultMode:"deny" replicates -p semantics` | `--settings with defaultMode:"dontAsk" replicates -p semantics` |
| `internal/agentrun/settings/settings.go` | 23 | `{"permissions":{"allow":[...],"defaultMode":"deny"}} byte sequence` | `{"permissions":{"allow":[...],"defaultMode":"dontAsk"}} byte sequence` |
| `internal/agentrun/settings/settings.go` | 40 | `{"permissions":{"allow":[<allowedTools>],"defaultMode":"deny"}}` | `{"permissions":{"allow":[<allowedTools>],"defaultMode":"dontAsk"}}` |
| `cmd/pyry/agent_run.go` | 227 | `that permissions.defaultMode "deny" in the per-spawn settings file` | `that permissions.defaultMode "dontAsk" in the per-spawn settings file` |
| `cmd/pyry/agent_run_selfcheck.go` | 92 | `permissions.defaultMode: "deny", allow: ["Read"]` (operator stdout) | `permissions.defaultMode: "dontAsk", allow: ["Read"]` |
| `internal/agentrun/selfcheck/selfcheck.go` | 4 | `(permissions.defaultMode "deny",` | `(permissions.defaultMode "dontAsk",` |
| `internal/agentrun/selfcheck/selfcheck.go` | 67 | `Under permissions.defaultMode "deny" with allow ["Read"]` | `Under permissions.defaultMode "dontAsk" with allow ["Read"]` |

Add a one-sentence rationale note to `settings.go`'s package comment (replacing the parenthetical reference to "Phase A spike, 2026-05-14") that names the documented Anthropic-docs value `"dontAsk"` and cites #487 (this ticket). One line; no prose expansion.

Lines that are **left as-is**:

- `cmd/pyry/agent_run.go:57,63,289` — phrases like "per-spawn deny-default permissions JSON", "deny-default allow-list", "per-spawn deny-default settings JSON" describe the **semantic property**, not the literal. They remain accurate under `dontAsk` and need no change.
- `cmd/pyry/agent_run_selfcheck.go:27,60,85,88` — similar semantic descriptors ("deny-default whitelist held", "deny-default settings file"). Unchanged.
- `internal/agentrun/selfcheck/selfcheck.go:65,72-73,86-87,92,138` and `ErrBashInvoked` message at line 87 — semantic descriptors, unchanged.
- `cmd/pyry/agent_run_test.go:413,1132` — comments naming "deny-default" as a property, unchanged.
- `internal/e2e/realclaude/allowed_tools_enforcement_test.go:5,14` — historical-framing comments describing the original ticket's matrix in **past tense** ("This ticket was originally framed as a `defaultMode ∈ {deny, default, dontAsk}` matrix..."). Past tense; historical record; unchanged.

### Out-of-scope clarifications (explicit, to bound the developer's blast radius)

The developer must NOT, in this ticket:

- Extend `internal/e2e/realclaude/fixtures.go`'s auth-skip surface to Max-auth credentials. The fixture-wide cleanup is an explicit OOS follow-up.
- Add startup-modal detection in ptyrunner (the "ptyrunner detects claude `/doctor` prompt at startup and fails fast" defense). Explicit OOS follow-up.
- Touch `internal/agentrun/selfcheck/selfcheck.go`'s `canonicalPrompt`, `canonicalAllow`, `ErrBashInvoked`, or `ErrTimeout` — they describe the **property** the selfcheck enforces and remain semantically valid under `dontAsk`.
- Update `cmd/pyry/agent_run_selfcheck.go`'s FAIL-message structure or wording beyond the single literal in the `permissions.defaultMode:` quote at line 92. The other required substrings pinned by `TestRunAgentRunSelfCheck_FAIL` (`"PTY"`, `"#329"`, `"#336"`, `"#470"`, `"#473"`, `["Read"]`) remain.

## Concurrency model

N/A. No new goroutines, no channel coordination changes, no shutdown-sequence changes. The settings tempfile lifecycle (create → encode → close → `defer os.Remove`) is unchanged; the literal flip is byte-level only.

## Error handling

N/A. No new failure modes. Validation (`len(allowedTools) == 0` → error before any file is created) is unchanged. Tempfile cleanup on encode/close failure is unchanged. Caller's `defer os.Remove(path)` pattern is unchanged.

A note on the FAIL path that the selfcheck would now exercise: under the old literal `"deny"`, the selfcheck's PASS condition was a coincidence (claude fell back to `"default"`, which happened to refuse Bash for the canonical prompt). Under `"dontAsk"`, the PASS condition holds because of the documented contract (`dontAsk` converts any permission prompt to denial for tools not in `allow`). The selfcheck's behaviour is now load-bearing; if it ever FAILs, it's a real contract regression — not a literal-rejection artifact. No code in the selfcheck path needs to change; the operator-output literal at line 92 is the only string that quotes the JSON value.

## Testing strategy

### Unit tests — byte-assertion updates

`internal/agentrun/settings/settings_test.go` — three byte goldens and one round-trip check, updated mechanically:

- Line 80 (`TestWriteSettings_SingleToolGoldenBytes`): change the `want` literal from `{"permissions":{"allow":["Bash"],"defaultMode":"deny"}}` to `{"permissions":{"allow":["Bash"],"defaultMode":"dontAsk"}}`.
- Line 100 (`TestWriteSettings_PreservesOrderAndDuplicates`): change `want` similarly.
- Lines 138-139 (`TestWriteSettings_RoundTripParseable`): change the expected `defaultMode` value from `"deny"` to `"dontAsk"` (also update the error-message want literal at line 139).

No structural changes to these tests; the test names, table cases, and `t.Parallel()` discipline remain. The empty-input and tempdir-leak test (`TestWriteSettings_EmptyInputReturnsErrorAndDoesNotWrite`) and the path-shape tests (`TestWriteSettings_PathLocationPrefixSuffix`, `TestWriteSettings_PathIsAbsolute`) are unaffected and untouched.

### Unit test — selfcheck FAIL message

`cmd/pyry/agent_run_selfcheck_test.go:83` — update the pinned substring from `` `permissions.defaultMode: "deny"` `` to `` `permissions.defaultMode: "dontAsk"` ``. No structural changes; the other required substrings (lines 84-89) remain.

### New regression test — realclaude

**File:** `internal/e2e/realclaude/doctor_poisoning_regression_test.go`
**Build tag:** `//go:build e2e_realclaude` (same as siblings)
**Test name:** `TestRealClaude_DoctorPoisoningRegression`

**Contract guarded:** the first `user` JSONL entry in a `pyry agent-run` session is the operator's prompt — NOT a `/doctor` repair template. A regression here means claude rejected pyry's settings JSON at startup and prepopulated `/doctor` into the user buffer (the #487 failure mode).

**Auth gate:** call `WithWorktreeAuthenticated(t)` — already skips with a named-variable diagnostic when `ANTHROPIC_API_KEY` is unset. The Max-auth-only operator scenario falls through this skip (acceptable for this ticket; the fixture extension is explicit OOS).

**PATH gate:** inherited suite-wide from `TestClaudeBinaryAvailable` in `smoke_test.go`; no per-test PATH check needed.

**Test body — bullet sketch (developer writes Go code in the project's testing idiom, mirroring `allowed_tools_enforcement_test.go`'s shape):**

- Acquire workdir via `WithWorktreeAuthenticated(t)`.
- Run `pyry agent-run` via `RunPyryAgentRun(t, RunOpts{...})` with: a short distinctive prompt (e.g. `"Reply with the single word: pong"`), system prompt (e.g. `"You are a regression-guard test agent. Reply tersely."`), `AllowedTools: []string{"Read"}`, `MaxTurns: 1`, `Effort: "low"`, `Model: "claude-haiku-4-5"`. Default 5-minute timeout via `RunOpts.Timeout` zero-value (do not shorten — `/doctor`-poisoned runs hang).
- Fail with the standard ExitCode/SessionID diagnostics (mirroring `allowed_tools_enforcement_test.go:50-61`).
- Read JSONL via `ReadJSONL(t, workdir, result.SessionID)`.
- Walk events; find the first `e.Kind == "user"`. Decode its `Raw` into `{Message struct{Content string|[]any \`json:"content"\`}}`. Treat parse errors silently (mirror `bashInvokedInRaw`'s policy at `allowed_tools_enforcement_test.go:74-76`).
- Assert: the decoded `content` (after coercing list-shape to a single string if necessary) does NOT contain the substring `"Help me fix the issues reported by /doctor below."`. The forbidden substring is the verbatim opening of the `/doctor` template observed in the ticket reproduction.
- On hit, `t.Fatalf` with: the verbatim user-entry content (truncated to ~512 bytes), the JSONL path (from `jsonlPathFor(workdir, result.SessionID)`), and the operator-visible direction "claude is rejecting the per-spawn settings JSON at startup — see #487".
- Sanity check (defence-in-depth): assert that AT LEAST one `assistant` event is present in the JSONL. The healthy path produces an `assistant` event with `stop_reason: end_turn` within ~30s; an empty assistant set under a non-poisoned session would indicate a different upstream failure mode that this test is not designed to diagnose. Treat absence as a soft failure with diagnostic context, not a silent pass.

**What the test does NOT do:**

- It does NOT assert on `stop_reason`. The AC item asking for `stop_reason: end_turn` within ~30s is satisfied by the manual reproduction in the ticket Context (the human runs that to verify the fix end-to-end on their local Mac). An automated assertion would couple the test to an upstream model-behaviour detail beyond the contract we're guarding.
- It does NOT exercise both `defaultMode` values in a matrix. The settings JSON is producer-owned; the regression is "the producer's literal is one claude accepts." A matrix test would re-engineer the production code to take the literal as a parameter, which is over-design for a single-literal guard.
- It does NOT inspect the settings file directly. The JSONL evidence is the operator-visible signal that matters; a settings-file probe would re-test what `TestWriteSettings_*` already covers byte-for-byte.

### Test selection commands

```bash
# Unit tests (fast)
go test ./internal/agentrun/settings/... ./cmd/pyry/...

# Realclaude regression test (requires ANTHROPIC_API_KEY + claude on PATH)
make e2e-realclaude
# or: go test -tags=e2e_realclaude -run TestRealClaude_DoctorPoisoningRegression ./internal/e2e/realclaude/
```

## Open questions

None. The Anthropic docs name `"dontAsk"` as the documented value for headless deny-default semantics; the ticket cites the docs verbatim; the byte shape and lifecycle of the per-spawn settings file are unchanged; the comment/operator-string updates are mechanical.

---

## Security review (label `security-sensitive`, mandatory)

The architect CLAUDE.md instruction references `agents/architect/security-review.md`. That file is not present in this worktree. Performing the review inline using the standard adversarial categories: **trust boundaries, input validation, output redaction, secret hygiene, denial-of-service, escalation paths.** Verdict at end.

**Trust boundaries.**

- The per-spawn settings file is written by pyry and consumed by claude. After this change, claude accepts the file (no `/doctor` prepopulation), and claude's `dontAsk` semantics convert any permission prompt for tools outside `allow` to a denial. The trust boundary at the claude-binary interface is now actually enforced (it was silently broken before).
- The literal-flip itself does not move the boundary; it restores the boundary that the prior literal had silently dissolved.
- `internal/agentrun/settings/settings.go:48` notes that CLI-parse layer (#470) validates `len(allowedTools) > 0`; this primitive's check at line 58 is defence-in-depth. Untouched.
- File: `internal/agentrun/settings/settings.go:62-83` — `os.CreateTemp` mode is 0o600 by default (Go stdlib); the per-spawn settings file is operator-readable only. Unchanged.

**Input validation.**

- `allowedTools` is operator-controlled (CLI flag), passed through verbatim to the JSON `allow` array. No new validation, no new escape: `encoding/json` quotes strings safely; an operator-supplied tool name containing JSON metacharacters would be safely escaped (this contract is unchanged by the literal flip).
- The new literal `"dontAsk"` is a hardcoded constant, not operator-controlled — no injection surface.

**Output redaction.**

- The settings.go package comment block (lines 8-11) pins the no-content-logging discipline: "MUST NOT log the allowedTools slice, the JSON payload, or the returned path." The literal flip does not introduce any new logging; the existing discipline holds.
- The new realclaude regression test logs the first user-entry content on failure (`t.Fatalf` with truncated content + JSONL path). The content is operator-supplied test data ("Reply with the single word: pong") under a tempdir-pinned HOME; no production-operator data leaks. The JSONL path is under the test's `WithWorktreeAuthenticated`-managed tempdir. Acceptable for a test failure diagnostic.
- The operator-output line at `cmd/pyry/agent_run_selfcheck.go:92` is the only operator-visible change. The new string `permissions.defaultMode: "dontAsk"` reveals only the literal pyry writes — which is now documented behaviour. No new secret/config leak.

**Secret hygiene.**

- `ANTHROPIC_API_KEY` flows through `WithWorktreeAuthenticated` into the subprocess environment as it does today; the literal flip does not change auth surface. No new credential storage, no new logging.
- The realclaude test's failure mode (no API key) skips with a non-secret diagnostic ("ANTHROPIC_API_KEY is unset in the outer environment").
- No new file writes outside `os.TempDir()` (production) or `t.TempDir()` (test).

**Denial-of-service.**

- The fix removes a DoS condition: before this change, a poisoned `/doctor` session hangs indefinitely (`pyry hangs indefinitely waiting for an assistant event`; the ticket reports a 2.5h manual smoke before kill). After the fix, the per-spawn agent-run terminates normally.
- The new regression test inherits the default 5-minute `RunPyryAgentRun` timeout — under a poisoned session it would time out and `t.Fatalf` with the timeout diagnostic, not hang the suite indefinitely. The 5-minute default is unchanged.

**Escalation paths.**

- The fix CLOSES an escalation path: under the old literal, headless dispatcher runs may have executed tools the agent's `allow` did NOT include (whatever claude's fallback mode permitted). After the fix, the deny-by-default contract is honoured at the claude-binary boundary.
- No new escalation surface is introduced — the literal is more restrictive (`dontAsk` is documented as deny-on-prompt for tools outside `allow`), not less.

**Verdict: PASS.** This change is net-positive for security: it restores a deny-by-default boundary that was silently dissolved, eliminates a `/doctor`-template prompt-injection vector (a poisoned message reaching the model with the operator's identity but operator-unintended content), and closes the indefinite-hang DoS condition. The realclaude regression test guards against silent recurrence. No new attack surface; no new secret exposure; no new logging of sensitive material.
