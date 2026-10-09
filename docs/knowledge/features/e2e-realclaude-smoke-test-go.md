# smoke_test.go
- `smoke_test.go` (#361) — one test, `TestClaudeBinaryAvailable`, that:
  - Asserts `exec.LookPath("claude")` succeeds. **Fatal, not skip** — the suite is opted-into by typing `make e2e-realclaude`, so a missing binary is misconfiguration, not absence.
  - Runs `exec.CommandContext(ctx, "claude", "--version")` under a 10 s timeout and asserts a zero exit. `CombinedOutput()` is reported on failure for debuggability. The version string is NOT parsed — "real claude is on PATH and executes" is the entire assertion.
- `fixtures.go` — shared primitives for every downstream test. All symbols live in the same file under the same `//go:build e2e_realclaude` tag.
  - `WithWorktree(t) string` (#372) — `t.TempDir()` + `t.Setenv("HOME", dir)`, returns the path. Pins HOME for both the in-test process and any subprocess so `os.UserHomeDir()` resolves to the same root on both sides. Does NOT create `.claude/…`; the runtime owns that.
  - `WithWorktreeAuthenticated(t) string` — fixture for tests that need a
    real Anthropic response. Its shared `authenticatedHome` helper accepts
    either nonempty `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN`, or both,
    and returns `t.TempDir()` without requiring, reading or copying the
    operator's `.claude.json`. It skips before allocating HOME only when both
    credentials are unset or empty. The diagnostic names both variables and
    includes the macOS Keychain extraction recipe; see
    [operator authentication](e2e-realclaude-operator-authentication.md).
    `WithWorktreeAuthenticated` pins HOME with `t.Setenv` and re-pins only
    nonempty credentials for `RunPyryAgentRun`'s environment inheritance.
    Absent and set-empty variables retain their original presence and value.
    `authenticatedHome` does not mutate the process environment, so parallel
    consumers can pass its returned HOME explicitly through `homeEnv`; see
    [parallel tests](e2e-realclaude-test-infrastructure.md#test-infrastructure).
    Credential presence is the fixture's gate; real Claude decides validity.
    **Why HOME stays pinned:** per-test transcript namespaces under
    `~/.claude/projects/<encoded-cwd>/<sid>.jsonl` prevent cross-test JSONL
    collisions. Reusing the operator's HOME to authenticate would defeat that
    isolation. The removed PTY runner's onboarding prerequisite does not apply
    to current stream-json paths ([#1479](https://github.com/pyrycode/pyrycode/issues/1479)).
    Plain `WithWorktree` remains available for probes that need no API response.

    **Fixture regression coverage** in `fixtures_test.go`:
    `TestAuthenticatedHome_CredentialIsolation` covers both entry points with
    OAuth-only, API-key-only and dual credentials, including absent versus
    set-empty variables, against missing, read-failing and readable operator
    configuration. It checks isolated HOME, no copied configuration, credential
    preservation, wrapper HOME pinning and no environment mutation by
    `authenticatedHome`. A passing `t.Run` can contain a skipped child, so an
    outer assertion must verify that the fixture returned; otherwise the stale
    skip gate would also skip the assertions and leave this regression green.
    `testOperatorHome` uses a directory at `.claude.json` as the read-error
    witness: file permissions alone do not prevent reads when tests run as root.
    `TestWithWorktreeAuthenticated_SkipsAndNamesBothEnvVarsWhenNeitherSet`
    re-execs the test binary under `PYRY_REALCLAUDE_AUTH_SKIP_INNER=1` to assert
    both entry points skip and the diagnostic names both credentials and the
    Keychain recipe. Synthetic credentials keep these contracts runnable offline.

    **Live authentication proof:**
    `TestWithWorktreeAuthenticated_RealAssistant/inherited`,
    `/oauth_only_missing` and `/oauth_only_read_error` run through real
    `pyry agent-run` stream-json. The OAuth-only arms clear the API key and
    use disposable operator HOMEs without changing actual operator configuration;
    they skip only when the OAuth token is unavailable, with no extra opt-in
    switch. Each proof requires exit success, a nonempty session ID, no
    synthetic/authentication-failed markers and an assistant JSONL event with
    end-of-turn and nonempty text. Count named non-skipped leaf results rather
    than passing parent tests; see [the live evidence runbook](../../release-tooling.md#live-claude-suite--read-the-count-not-the-exit-code).
  - `ReadJSONL(t, workdir, sessionID) []JSONLEntry` (#372; path composition delegated to `tuidriver.SessionJSONLPath` in [#508](../codebase/508.md)) — opens `tuidriver.SessionJSONLPath(<HOME>, workdir, sessionID)` and runs it through `jsonl.NewReader(...).Next()`. Empty file → empty slice; open/parse failures call `t.Fatalf` with the resolved path embedded. A private `resolveAndOpenJSONL` split exists so the missing-file path is testable as a returned error.
  - `JSONLEntry = jsonl.Event` (#372) — type **alias**, not a wrapper struct. Keeps downstream tests from importing `internal/agentrun/jsonl` directly while preserving full field access. See [`codebase/372.md`](../codebase/372.md) for the design rationale.
  - `RunPyryAgentRun(t, opts) RunResult` (#373) — synchronous subprocess invoker. Builds `pyry` once per test process via `sync.Once` (honours `PYRY_E2E_BIN` short-circuit), writes `<workdir>/prompt.txt` + `system.txt` from `opts.Prompt`/`opts.SystemPrompt`, invokes `pyry agent-run` with the eight required flags in `--flag=value` form (`--prompt-file`, `--system-prompt-file`, `--allowed-tools`, `--max-turns`, `--effort`, `--model`, `--workdir`, `--output-format=stream-json`), captures stdout/stderr/exit code, and returns the `session_id` parsed from the first stream-json `{"type":"system","subtype":"init",…}` line. **Non-zero subprocess exit is NOT fatal** — callers assert on `RunResult.ExitCode` themselves so the real CLI's error paths can be tested. Only structural failures (validation, build, exec start, timeout) call `t.Fatalf`. `RunOpts` mirrors `cmd/pyry/agent_run.go`'s flag surface 1:1 — no `SessionID` input, no `Mode` enum. See [`codebase/373.md`](../codebase/373.md) for the design rationale.

The composition pattern downstream tests use: `WithWorktree` → `RunPyryAgentRun` → `ReadJSONL`. Subsequent tickets (#364–#368, the actual prompt-poisoning / trust-boundary tests) build on this three-step shape.

- `prompt_fidelity_test.go` (#364) — first consumer of the trio. `TestRealClaude_PromptFidelity` runs `pyry agent-run` against the real `claude` CLI with a distinctive ASCII-only prompt literal (`INTEGRATION_TEST_PROMPT_PROMPTFIDELITY_2N7Q4R8W`), then asserts the literal survives byte-for-byte into the first `user`-kinded entry of the resulting JSONL session via `bytes.Contains(event.Raw, []byte(literal))`. Pins the streamrunner envelope's text round-trip end-to-end — would catch any future preprocessing, escaping drift, or wiring change that bypasses streamrunner. Run-level asserts (`ExitCode == 0`, `SessionID != ""`) come BEFORE the JSONL assert so a setup/auth/network failure surfaces as itself rather than as "no user entry". Uses `--max-turns=1`, `--effort=low`, `--model=claude-haiku-4-5` to minimise per-run cost (~$0.01). See [`codebase/364.md`](../codebase/364.md) for the design rationale.
- `prompt_fidelity_unicode_test.go` (#419) — UTF-8 sibling of #364. `TestRealClaude_PromptFidelity_Unicode` clones the #364 body 1:1 except for the prompt literal and test name. Literal is a single Go string with three byte-width classes (Finnish `ä`/`ö`/`å` 2-byte, em-dash `—` 3-byte, `🐍` 4-byte outside the BMP — the byte-slicing canary) wrapped in an ASCII frame `INTEGRATION_TEST_PROMPT_UNICODEFIDELITY_K3M9P2X7 … INTEGRATION_TEST_PROMPT_UNICODEFIDELITY_END_R5T8V1Z4` so substring matching stays deterministic and the frame-vs-payload diagnostic distinguishes "prompt missing" from "prompt corrupted on a UTF-8 boundary". Characters are literal in source (not `\uXXXX`) and sent without NFC/NFD normalization — the assertion is byte-identical preservation, not decode-equivalence. Reuses `jsonlPathFor` directly (same package, same build tag); no fixture-helper changes, no Makefile changes. Closes the JSON-escape / re-encoding / buffer-slicing-on-multi-byte-boundary gap that #364's ASCII-only literal deliberately left open for Finnish-speaking users. Uses the same `RunOpts` as #364 (~$0.01 per run). See [`codebase/419.md`](../codebase/419.md) for the design rationale.
