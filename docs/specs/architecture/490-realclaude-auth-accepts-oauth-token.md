# #490 — `WithWorktreeAuthenticated` accepts `CLAUDE_CODE_OAUTH_TOKEN`

**Size:** XS — single-file production change (`fixtures.go`), single-file test extension (`fixtures_test.go`), no signature change, zero call-site cascade across the 12 existing callers.

## Context

On a Max-only Mac the operator has no `ANTHROPIC_API_KEY`. The credential is an OAuth access token stored in macOS Keychain (service `Claude Code-credentials`), extractable with:

```
security find-generic-password -s 'Claude Code-credentials' -w | jq -r '.claudeAiOauth.accessToken'
```

The `claude` binary accepts the token via `CLAUDE_CODE_OAUTH_TOKEN` (verified empirically — see ticket body). Today `WithWorktreeAuthenticated` reads only `ANTHROPIC_API_KEY` and silently skips when unset, hiding 7 opt-in tests including `ptyrunner_byte_equivalence` (#482, the equivalence gate that never actually ran on Max-only configurations during the ptyrunner cutover).

`HOME` pinning in `WithWorktree(t)` is load-bearing: per-test JSONL namespace isolation under `~/.claude/projects/<encoded-cwd>/<sid>.jsonl` prevents cross-test JSONL collision. Lifting it would re-introduce that race. So the only correct way to authenticate while keeping `HOME` pinned is the env-var path; copying Keychain into the tmp HOME is not an option (Keychain is not file-backed in any portable sense, and copying `~/.claude/` does not authenticate per the ticket body's empirical check).

## Design

### Behaviour contract for `WithWorktreeAuthenticated(t *testing.T) string`

1. Read both `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` from the outer test environment via `os.Getenv`.
2. If **both** are empty, call `t.Skipf` with a single message that:
   - Names BOTH accepted env vars verbatim.
   - Points operators to the macOS Keychain extraction recipe: ``security find-generic-password -s 'Claude Code-credentials' -w | jq -r '.claudeAiOauth.accessToken'``.
   - Tells them to export the result as `CLAUDE_CODE_OAUTH_TOKEN`.
3. Otherwise, call `WithWorktree(t)` to allocate the tmp HOME and pin it.
4. Re-pin via `t.Setenv` **only** the env var(s) that were non-empty in step 1. Do NOT unconditionally `t.Setenv` an empty string for the absent one — `os.Getenv` cannot distinguish unset from empty-string, but downstream tooling (`claude` itself, or future env sanitisers in `RunPyryAgentRun`) may treat a set-empty value differently from absent. Preserve the original outer-env shape.
5. Return the workdir from step 3.

### Why "set both if both are present"

When the operator happens to have both vars set (uncommon but possible — e.g. a mixed-account Mac), both are re-pinned. `claude`'s precedence between the two is its own concern; the helper's job is to preserve whatever the operator chose to export, against future fixture-level env sanitisation in `RunPyryAgentRun`. The reference shape in the ticket body matches this exactly.

### Skip message — required substrings

The new skip message MUST contain these literal substrings (the skip-message test asserts on them):

- `ANTHROPIC_API_KEY`
- `CLAUDE_CODE_OAUTH_TOKEN`
- `security find-generic-password -s 'Claude Code-credentials' -w`
- `jq -r '.claudeAiOauth.accessToken'`
- `CLAUDE_CODE_OAUTH_TOKEN` (a second appearance, naming the export target — already covered by the first match)

Keep the message a single `t.Skipf` call (one logical line; Go string concatenation across source lines is fine and follows the existing style at `fixtures.go:49`).

### No signature change, no caller updates

The function still returns a single workdir string. The 12 existing call sites work unchanged. No edit fan-out.

## Testing strategy

Extend `internal/e2e/realclaude/fixtures_test.go` with two new tests. Do NOT duplicate the existing `TestWithWorktreeAuthenticated_RealAssistant`; it continues to cover the API-key happy path.

### Test 1 — skip path asserts both var names and Keychain recipe

Pattern: outer/inner subprocess re-exec, identical in shape to `TestRunPyryAgentRun_Timeout` at `fixtures_test.go:319-342`. Required because `t.Skipf` ends the calling goroutine via `runtime.Goexit()` and provides no in-process return value.

Scenarios (all run in one test function with a sentinel env var like `PYRY_REALCLAUDE_AUTH_SKIP_INNER=1`):

- **Inner branch (`sentinel=1`):** `t.Setenv("ANTHROPIC_API_KEY", "")` and `t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")`, then call `WithWorktreeAuthenticated(t)`. Following call is unreachable — if execution gets past it, `t.Fatalf` complaining that the helper did not skip.
- **Outer branch:** `exec.Command(os.Args[0], "-test.run=^TestName$", "-test.v")` with `PYRY_REALCLAUDE_AUTH_SKIP_INNER=1` appended to inherited env. Capture combined output. Assert:
  - Process exited with status 0 (`t.Skip` is success, not failure).
  - Output contains `--- SKIP: TestName` (confirms it actually skipped vs. an unrelated failure path).
  - Output contains every required substring listed under "Skip message — required substrings".

### Test 2 — OAuth-token-only re-pin preserves the value

In-process test, no subprocess. No network call.

- Set up: `t.Setenv("ANTHROPIC_API_KEY", "")`, `t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-token-not-real")`.
- Call: `dir := WithWorktreeAuthenticated(t)`.
- Assert:
  - `dir` is a real directory (`os.Stat` succeeds, `IsDir()` true) — confirms `WithWorktree` ran.
  - `os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")` returns `"test-oauth-token-not-real"` — confirms the re-pin survived `WithWorktree`'s `t.Setenv("HOME", ...)`.
  - `os.Getenv("ANTHROPIC_API_KEY")` returns `""` — confirms the helper did NOT set the absent var (the "preserve original outer-env shape" rule from § Design step 4).
  - `os.Getenv("HOME")` equals `dir` — confirms HOME pin is in place.

### Why no symmetric API-key-only re-pin test

`TestWithWorktreeAuthenticated_RealAssistant` already covers the API-key path end-to-end against the live API when `ANTHROPIC_API_KEY` is set. A structural mirror would duplicate the contract already encoded there. We add the OAuth structural test because no live-API counterpart is feasible (would require a Max-only Mac in the test environment).

### Test naming

Pick names that grep cleanly and group with the existing one:

- `TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet`
- `TestWithWorktreeAuthenticated_OAuthTokenOnly_RepinsAndPreservesAbsentApiKey`

## Error handling and failure modes

The helper has no error returns and no recoverable failures. The two outcomes are:

- Skip (neither cred set) — operator-facing diagnostic, see § Skip message.
- Proceed (≥1 cred set) — `WithWorktree` failures propagate via `t.Setenv` / `t.TempDir` (already governed by `WithWorktree`'s contract).

## Out of scope

Same exclusions as the ticket body:

- Token-refresh handling inside the test process (claude rotates via `refreshToken`; suite is short relative to TTL).
- CI Keychain access (CI continues to use `ANTHROPIC_API_KEY` injection; #406's secret-injection track is separate).
- Changing the dispatcher's `pyry agent-run` to prefer `CLAUDE_CODE_OAUTH_TOKEN` (it runs in the operator's real shell with Keychain accessible normally; the env-var workaround is test-only).

## Open questions

None blocking. Implementation is mechanical against the contract above.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the helper's only inputs are two outer-env variables; their values flow through `t.Setenv` (which is in-process only — does not write to disk or to any other process beyond the eventual `exec.Command` subprocess in `RunPyryAgentRun`). No new boundary introduced; the existing API-key path already crosses the same in-process → subprocess boundary the same way.
- **[Tokens, secrets, credentials]** No findings — the helper does NOT log, persist, or echo either token. The skip message is the only string the helper emits, and it fires only when both vars are empty (so no token can appear there). The Keychain extraction recipe in the skip message names `security find-generic-password` — that command requires interactive user authorization on macOS by default (Keychain prompt), so disclosing the recipe to operator-side logs does not weaken the trust model. The re-pinned values are inherited by `exec.Command` via `cmd.Env = append(os.Environ(), opts.ExtraEnv...)` at `fixtures.go:169`, which is the existing, audited path for the API-key case; the OAuth-token case uses the same code path with no new exposure surface.
- **[File operations]** No findings — the helper performs no file I/O beyond the existing `t.TempDir()` allocation inside `WithWorktree`. `HOME` is pinned to a fresh per-test tmpdir, ruling out cross-test JSONL collision (the original motivation; see § Context).
- **[Subprocess / external command execution]** No findings — neither env var is passed as a command argument; both are inherited via `cmd.Env` (the standard, audited inheritance path). No `sh -c` use. The skip-message test re-execs `os.Args[0]` with a sentinel env var, matching the existing `TestRunPyryAgentRun_Timeout` pattern at `fixtures_test.go:330`.
- **[Cryptographic primitives]** Not applicable — no crypto operations in scope; the helper neither generates, validates, nor compares tokens.
- **[Network & I/O]** Not applicable — the helper does no network I/O. The downstream `claude` subprocess does talk to api.anthropic.com, but that is unchanged from the existing API-key path.
- **[Error messages, logs, telemetry]** No findings — the only string the helper emits is the skip message, and it carries no secret data (it only fires when both creds are empty). No new log lines or telemetry are introduced. The skip-message test asserts substring presence on the operator-facing diagnostic, not on any token value.
- **[Concurrency]** No findings — `t.Setenv` is per-test and serialised by the testing framework; `WithWorktree`'s HOME pin is restored on test exit. No new goroutines spawned. The "per-test JSONL namespace isolation" guarantee documented in § Context is preserved.
- **[Threat model alignment]** Out of scope for the pyrycode CLI threat model — this is a test fixture, not a production code path. The repo has no published `docs/threat-model.md` for test fixtures; the closest analogue is the existing API-key handling in the same function, and the OAuth path mirrors it exactly.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-21
