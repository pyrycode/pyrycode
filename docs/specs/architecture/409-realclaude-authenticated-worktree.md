# Spec: e2e/realclaude — `WithWorktreeAuthenticated` fixture (#409)

Ticket: [#409](https://github.com/pyrycode/pyrycode/issues/409)
Size: XS (one new helper ~15 LoC + one test ~40 LoC, additive only)

## Files to read first

- `internal/e2e/realclaude/fixtures.go:29-37` — `WithWorktree(t)`. The new helper is a thin wrapper around this; mirror the `t.Helper()` call, doc-comment density, and return shape.
- `internal/e2e/realclaude/fixtures.go:87-142` — `RunPyryAgentRun`. Note line 123: `cmd.Env = append(os.Environ(), opts.ExtraEnv...)`. The subprocess already inherits the full outer env including any `ANTHROPIC_API_KEY`; no change to `RunPyryAgentRun` is required for the new helper to work. The new helper's only job vs. plain `WithWorktree` is to **gate the test on the presence of an outer-env credential** and pin the value into the test process so the inheritance is deterministic.
- `internal/e2e/realclaude/fixtures_test.go:22-55` — `TestWithWorktree_ReturnsExistingHomeIsolatedDir`. Patterns: build-tag header, `realclaude` package (not `_test`), `os.Stat` + `os.UserHomeDir` shape, nested-subtest HOME-restore check. The new test sits in the same file and reuses these idioms.
- `internal/e2e/realclaude/per_agent_test.go:83-133` — `runRoleSmokeTest`. The canonical real-claude assertion shape: `ExitCode == 0` → `SessionID != ""` → `parseResultTrailer` → walk JSONL for the last `assistant`-kinded event with `EndOfTurn && TextChars > 0`. Reuse the same five-step shape and the file-local `truncate` helper.
- `internal/e2e/realclaude/per_agent_test.go:104-114` and `internal/e2e/realclaude/tool_loop_test.go:190-203` — `parseResultTrailer` + `resultTrailer.PermissionDenials` shape. Used in the assertion phase to keep diagnostic output consistent with siblings.
- GitHub issue body, §"Evidence (#383 trace)" — the synthetic `"model":"<synthetic>"` + `"error":"authentication_failed"` envelope the test pins against. The test asserts those substrings are NOT present in `result.Stdout` as a belt-and-suspenders check beyond the structural assertions.
- `agents/architect/security-review.md` — the security-review pass appended at the end of this spec follows that template. Ticket is labelled `security-sensitive`; the appended section is the required output.

## Context

`WithWorktree(t)` pins `$HOME` to a per-test temp directory so the in-test process and any spawned subprocess resolve `os.UserHomeDir()` identically. That isolation is load-bearing for the existing tests (`ReadJSONL` depends on it; the JSONL session files claude writes must end up under the pinned `HOME`). **But the pinned `$HOME` also hides any credential material claude would normally find under the real `~/.claude/`.** On platforms / shells where claude's auth lives in a file under `~/.claude/` (Linux; macOS sessions that can't reach the user Keychain), the subprocess returns a synthetic `"Not logged in"` assistant message with `"error":"authentication_failed"` and exits 1 — before any model/tool/permission code path runs.

Existing realclaude tests cope today by either being argv-shape probes (`smoke_test.go`, `allowed_tools_enforcement_test.go`) OR by running on a platform/Keychain combination where claude finds credentials regardless of `$HOME`. The #383 spike (real model response over `--permission-prompt-tool stdio`) was the first test that genuinely needed a real API response in the bug-affected codepath; it surfaced the gap.

The narrow fix: an **opt-in** sibling helper that gates the test on a known credential mechanism the outer environment must satisfy, and propagates that credential into the test process so the subprocess inheritance (`os.Environ()` in `RunPyryAgentRun`) is deterministic. The existing `WithWorktree(t)` is unchanged — the fastest, most isolated path stays the default.

### Why option A (env-passthrough on `ANTHROPIC_API_KEY`)

Three directions were considered (ticket §"Technical Notes"):

- **A. Env-passthrough of `ANTHROPIC_API_KEY`.** Picked. Smallest surface, no on-disk leakage of credentials, no coupling to claude's auth-file shape, works identically on Linux + macOS, CI + local. Local Max-plan users who normally rely on Keychain can either (i) export `ANTHROPIC_API_KEY` for runs that include the new helper's tests, or (ii) let those tests skip — the suite stays green either way.
- **B. Symlink `~/.claude/credentials` into the temp `$HOME`.** Rejected. Couples test infra to claude's on-disk auth shape (currently undocumented and platform-variant: `~/.claude/.credentials.json` on Linux, macOS Keychain on macOS, possibly OAuth tokens). Any future change to that shape silently breaks the test. Also creates a path under the temp dir that points at live credentials — not strictly a leak (the symlink target lives where it always did), but blurs the "$HOME-isolated" mental model the directory name promises.
- **C. `CLAUDE_CONFIG_DIR` pinning instead of `$HOME`.** Rejected without verification. Would require confirming claude actually reads such an env var across platforms and versions; the cost of getting it wrong (silent fallback to the same `authentication_failed` mode) is exactly the failure this helper exists to eliminate.

Option A picks a credential whose presence/absence is a single `os.Getenv` lookup and whose propagation is automatic.

### Why skip (not fail-fast) when the credential is absent

The AC pair pins this:

- AC#4 lets the architect pick skip-or-fail when `ANTHROPIC_API_KEY` is absent.
- AC#5 requires the test to be **skipped** (not failed) so the suite stays green on contributor machines without API keys.

The two reconcile only if the helper itself calls `t.Skip` — a fail-fast helper would fail the test and violate AC#5. The helper performs the skip check **before** it allocates `t.TempDir()` so a skip never strands an empty temp directory.

## Design

### File layout

```
internal/e2e/realclaude/
  fixtures.go            (modify — add WithWorktreeAuthenticated; ~15 LoC + doc comment)
  fixtures_test.go       (modify — add TestWithWorktreeAuthenticated_RealAssistant; ~40 LoC)
```

Both files already carry `//go:build e2e_realclaude` and `package realclaude`. No new files, no Makefile change, no new build tag.

The new test lives in `fixtures_test.go` rather than its own file because it tests the fixture's contract directly. The siblings in their own files (`prompt_fidelity_test.go`, `tool_loop_test.go`, `per_agent_test.go`) exercise product behaviour atop the fixture; this test exercises the fixture itself.

### New exported symbol: `WithWorktreeAuthenticated`

One function, no new types.

**Contract:**

1. `t.Helper()`.
2. `key := os.Getenv("ANTHROPIC_API_KEY")` — read from the outer test-runner environment.
3. If `key == ""` → `t.Skipf("realclaude.WithWorktreeAuthenticated: ANTHROPIC_API_KEY is unset in the outer environment; this helper is opt-in and requires that variable. Export it (or rely on a CI secret) to run tests that use this fixture.")`. **Skip must precede `t.TempDir()`** so the framework doesn't allocate an unused temp dir on skip.
4. `dir := WithWorktree(t)` — delegate to the existing helper for `t.TempDir()` + `t.Setenv("HOME", dir)`.
5. `t.Setenv("ANTHROPIC_API_KEY", key)` — explicit re-pin of the value into the test process. Mechanically redundant (the existing inheritance path in `RunPyryAgentRun` already propagates it), but documents intent at the source so a future refactor of `WithWorktree` (or insertion of an env-scrubbing helper between this call and the subprocess spawn) doesn't silently regress the helper's contract. The `t.Setenv` cleanup ordering also pins the key for the test's duration even if other test code mutates the env.
6. Return `dir`.

**Doc comment** (mandatory per AC#3): names the env var, the use-case (real-API probes only), and the existence of the skip path. Three lines max — `CODING-STYLE.md` rules out multi-paragraph doc comments. Sketch:

```
// WithWorktreeAuthenticated returns a per-test temp directory and pins
// $HOME to it like WithWorktree, then re-pins ANTHROPIC_API_KEY from the
// outer test environment so the subprocess inherits a real-API credential.
// Use ONLY for tests that need a real Anthropic response (not argv-shape
// probes). Skips the test with a named-variable message when
// ANTHROPIC_API_KEY is unset in the outer environment.
```

**Imports added to `fixtures.go`:** none new — `os` and `testing` are already imported.

**Signature (the contract the developer implements; not the body):**

```go
func WithWorktreeAuthenticated(t *testing.T) string
```

Body fits in ~10 non-comment lines; the developer writes it in the project's style (no helper extraction, no abstraction).

### Test: `TestWithWorktreeAuthenticated_RealAssistant`

Added to `fixtures_test.go`. Builds on the existing imports — `os`, `strings`, `testing` are already imported.

**Scenario** (bullet-pointed; developer writes the test body in project idiom):

- Call `WithWorktreeAuthenticated(t)` — assigns `workdir`. If `ANTHROPIC_API_KEY` is unset, the helper itself calls `t.Skip` and the rest does not run.
- Invoke `RunPyryAgentRun(t, RunOpts{...})` with the cheapest viable shape: a one-shot user prompt that produces a single-turn text reply with no tool use.
  - `Workdir`: from step 1.
  - `Prompt`: `"Reply with the single word 'pong' and nothing else."` (haiku reliably produces a short text-only reply for this; the property under test is the auth path, not the model's judgement).
  - `SystemPrompt`: `"You are a minimal e2e authentication probe. Keep replies under 10 words."` (non-empty per `validateRunOpts`).
  - `AllowedTools`: `[]string{"Read"}` (minimum non-empty value; the prompt does not exercise tools).
  - `MaxTurns`: `1`.
  - `Effort`: `"low"`.
  - `Model`: `"claude-haiku-4-5"` (matches sibling tests; ~$0.01 per run).
  - `Timeout`: zero — accept the 5-minute default from `RunPyryAgentRun`.
- Assert `result.ExitCode == 0`. On failure, dump truncated stderr (use the file-local `truncate` helper from `per_agent_test.go`).
- Assert `result.SessionID != ""`. On failure, dump truncated stdout.
- Walk `ReadJSONL(t, workdir, result.SessionID)` for a `*JSONLEntry` whose `Kind == "assistant"`, `EndOfTurn == true`, and `TextChars > 0`. Fail with the resolved JSONL path (via the file-local `jsonlPathFor` helper if exported, otherwise inline the path computation that mirrors `per_agent_test.go:117`) when no such event is found.
- **Negative-pin** (belt-and-suspenders against the specific #383 failure mode): assert that `result.Stdout` does NOT contain the substrings `"\"model\":\"<synthetic>\""` and `"\"error\":\"authentication_failed\""`. On failure, dump the **first 1 KiB** of stdout via `truncate` (NOT a full dump — see Security review §7). The structural asserts above would already fail on the synthetic envelope (`ExitCode == 1`, no `EndOfTurn` text), but the substring check produces a clearer diagnostic for the auth-failed mode specifically.

`t.Parallel()` is NOT called — matches the realclaude convention (cost-predictable, no API rate-limit interaction).

Cost: one haiku/low/max-turns=1 call per `make e2e-realclaude` run, ~$0.01. Wall time ~10–30 s.

### Helper-internal vs test-internal split

The skip-on-missing-credential check belongs **inside** the helper, not in each test. Reasoning:

- AC#3 requires the doc comment to name the prerequisite — the doc and the gate live in one place.
- A test-side check would be duplicated at every consumer; a single consumer today is fine, but as soon as a second real-API test lands the duplication is real.
- The helper IS the gate. A test calling `WithWorktreeAuthenticated` self-elects to require auth; that election is the helper's call to handle.

The helper does not validate the key's shape (e.g., `strings.HasPrefix(key, "sk-ant-")`). Anthropic may change the key format; the only authority that knows whether a key is valid is `claude` itself. The helper propagates any non-empty value and lets the subprocess surface bad values as a non-zero exit.

## Concurrency model

N/A. The helper is fully synchronous, runs inside a single test goroutine, holds no shared state. `t.Setenv` and `t.TempDir` are documented thread-safe with respect to the framework's per-test cleanup ordering. Parallel sub-tests calling the helper each get their own `t.TempDir` and a per-test `HOME` pin — no shared mutable state across parallel instances.

`ANTHROPIC_API_KEY` is read once at the top of the helper and re-pinned via `t.Setenv`; any in-test mutation of the env var is restored on test exit by the framework.

## Error handling

- **Missing credential** → `t.Skip` (not `t.Fatal`). Message names the env var by name only. Never echoes the value (it's empty by definition here, but the principle stands).
- **`os.Getenv` failure** → not possible; `os.Getenv` does not return errors.
- **`WithWorktree` failure** → already handled inside `WithWorktree` (calls `t.TempDir` / `t.Setenv`; both `t.Fatalf` on their own internal failures). No additional handling needed in the wrapper.
- **`t.Setenv("ANTHROPIC_API_KEY", key)` failure** → `t.Setenv` calls `t.Fatalf` internally on failure. No additional handling needed.

The helper never builds an error message that interpolates the credential value. The doc comment, the skip message, and any future log line in this helper must only ever name the variable, never the value.

## Testing strategy

The new test in `fixtures_test.go` IS the verification. Done-when:

1. With `ANTHROPIC_API_KEY` exported and `claude` on `PATH`: `make e2e-realclaude` includes `TestWithWorktreeAuthenticated_RealAssistant` and it passes — at least one assistant event with `EndOfTurn=true && TextChars>0`, no `<synthetic>` model marker, no `authentication_failed` substring.
2. With `ANTHROPIC_API_KEY` unset: `make e2e-realclaude` shows the test as `SKIP` (Go's standard skip output) with the named-variable message. The other realclaude tests continue to run / pass / fail on their own merits — no regression to siblings.
3. `make test` is unchanged (build-tag gating already excludes the whole package).
4. `make check` (vet + test + staticcheck) passes — confirms the additive helper doesn't break the default tag set or trip a linter.
5. The doc comment compiles into `go doc` output and names `ANTHROPIC_API_KEY` literally — searchable via `go doc github.com/pyrycode/pyrycode/internal/e2e/realclaude.WithWorktreeAuthenticated` (under the `e2e_realclaude` build tag).

No CI-side change. `make e2e-realclaude` continues to run during the code-review phase of every dispatched ticket; that phase is the canonical execution site for the new test, and the operator's environment is the canonical source of `ANTHROPIC_API_KEY`.

## Open questions

- **`jsonlPathFor` visibility** — `per_agent_test.go` and `tool_loop_test.go` use a file-private `jsonlPathFor(workdir, sessionID) string` for failure-message paths. If it's currently scoped to one of those files, the new test inlines the same 3-line `filepath.Join` directly rather than pulling it package-private (YAGNI). If it's already package-private, the new test reuses it. The developer makes the call at write-time based on the current scope; no spec change either way.

## Notes for the developer

- The helper must be ~15 LoC; the doc comment ≤3 lines. Anything more is over-engineering for this surface.
- Do NOT write the credential value into any string anywhere. Doc comments, skip messages, error messages, log lines: variable name only.
- Do NOT add a credential-shape validator (`strings.HasPrefix("sk-ant-")` etc.). Claude is the only authority on whether a key is valid; the helper propagates any non-empty value.
- Do NOT mirror the `ANTHROPIC_API_KEY` read into a returned credential or expose it on the helper's API. The helper's signature stays `func(t *testing.T) string` and the string is the workdir path — same shape as `WithWorktree`.
- Do NOT dump `os.Environ()` in any failure message in the new test. If `result.Stderr` or `result.Stdout` need to appear in a diagnostic, use the existing `truncate` helper (1 KiB cap). Claude's stream-json output does not echo the API key today, but the 1 KiB cap is the cheap structural defense against any future change.
- The build-tag `//go:build e2e_realclaude` on the new test is mandatory — without it the test compiles into the default build and breaks `make test`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The helper has one explicit boundary: outer-test-runner env (read via `os.Getenv("ANTHROPIC_API_KEY")`) → in-test process env (written via `t.Setenv`). The boundary is one function, one line of read, one line of write. No parsing, no untrusted-input handling. Downstream code (`RunPyryAgentRun`, which inherits via `os.Environ()`) is the only consumer and already exists; the new helper does not change its contract.
- **[Tokens, secrets, credentials]** SHOULD FIX → addressed in the spec:
  - Skip message names the env var only, never the value (§"Error handling").
  - Doc comment names the env var only (§"New exported symbol").
  - Helper never writes the value to disk, to logs, or to any string that could be embedded in a `t.Fatalf` message (§"Error handling", §"Notes for the developer").
  - No credential-shape validation, no key derivation, no comparison against attacker-controlled values (no constant-time-compare concern).
  - Token lifecycle: creation/rotation/revocation are upstream of this helper (operator + Anthropic); the helper only inherits a credential that already exists in the outer env. Out of scope, but named here so code-review knows not to look for in-helper rotation logic.
  - **Defensive note for the developer** (also in §"Notes for the developer"): test failure messages must NOT dump `os.Environ()` or untruncated subprocess streams. The 1 KiB `truncate` cap on stdout/stderr in the new test is the structural defense; claude's stream-json envelope today does not echo the API key, but the cap is the cheap insurance against a future change to claude's diagnostic output. Code-review must confirm this on the PR.
- **[File operations]** No findings. The helper allocates a `t.TempDir()` via `WithWorktree` and `t.TempDir` handles permissions (0700) and cleanup. No path concatenation of user input, no TOCTOU, no symlink handling, no atomic-write surfaces. The helper does NOT symlink or copy any credential file — the rejection of option B in §"Why option A" pins this design choice.
- **[Subprocess / external command execution]** No findings. The helper itself spawns no subprocess. The downstream consumer (`RunPyryAgentRun`) already wires `cmd.Env = append(os.Environ(), opts.ExtraEnv...)` (line 123); the new helper does not change that. No `sh -c`, no user-controlled argv. Env scrubbing is intentionally NOT done — the test wants the full outer env including the credential.
- **[Cryptographic primitives]** N/A. The helper does no crypto.
- **[Network & I/O]** N/A at this layer. The subprocess (`claude` via `pyry agent-run`) makes the real Anthropic API call over its own TLS stack; the helper is several layers removed from any socket. Network hardening is claude's concern, not this fixture's.
- **[Error messages, logs, telemetry]** Addressed in §"Error handling" and §"Notes for the developer". The skip message names the variable; the new test's failure messages truncate stdout/stderr to 1 KiB; the helper has no log lines. Code-review must reject any PR that dumps `os.Environ()` or full subprocess output in a failure path.
- **[Concurrency]** No findings. Helper is fully synchronous, runs inside one test goroutine. `t.Setenv` and `t.TempDir` are framework-managed and parallel-safe via per-test cleanup ordering.
- **[Threat model alignment]** Out of scope for this layer. The helper is a test-only fixture under `//go:build e2e_realclaude`; it is not part of any production code path, is never compiled into the `pyry` binary, and is never exposed to the network or to a user's input. No relay or CLI threat model section applies.

**Adversarial scenarios considered:**

- *A malicious test in the same package reads `os.Getenv("ANTHROPIC_API_KEY")` directly.* Out of scope of this helper — any test in the same package already has full env access regardless of this helper. Defense lives at the dispatcher / code-review boundary (only trusted code lands in the `realclaude` package).
- *A buggy test calls `t.Setenv("ANTHROPIC_API_KEY", "")` before `WithWorktreeAuthenticated`.* The helper reads `os.Getenv` first and skips on empty; the bad in-test mutation cleanly produces a skip rather than a synthetic-auth-failed run. Test-level confusion, not an exploit.
- *The key value leaks into a `t.Fatalf` message via `result.Stderr` containing an echoed credential.* Claude's current stream-json output does not echo the API key. If a future claude version changes that, the 1 KiB `truncate` cap on stderr in the failure-dump path bounds (but does not eliminate) the leak surface. The structural fix would be to never dump stderr at all on this test's failure path; deferred as a SHOULD FIX in the failure-mode-change scenario only.
- *A future contributor adds a `t.Logf("ANTHROPIC_API_KEY=%s", key)` line to the helper for "debugging".* No structural defense — code-review responsibility. Named here as a hazard so the security review's existence on the spec makes it visible.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-16
