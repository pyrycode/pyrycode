# Remove the stale live-Claude onboarding prerequisite

## Files read

- `internal/e2e/realclaude/fixtures.go` → `authenticatedHome`, `WithWorktreeAuthenticated`, `liveHome`, `homeEnv`, `RunPyryAgentRun`: credential gate, serial/parallel HOME contracts and real stream-json invocation.
- `internal/e2e/realclaude/fixtures_test.go` → `TestWithWorktreeAuthenticated_RealAssistant`, OAuth copy/skip tests, missing-credential subprocess test and `TestMain`: existing proof and offline re-exec pattern.
- `docs/knowledge/features/e2e-realclaude.md` § Parallel tests: `authenticatedHome` must not mutate process environment; children receive HOME explicitly.
- `docs/knowledge/features/e2e-realclaude-operator-authentication.md` and `e2e-realclaude-smoke-test-go.md`: obsolete onboarding prerequisite belongs to the documentation handoff.
- `docs/knowledge/features/development-verification.md` § Captures and live evidence / Test execution and artifact survival: named non-skipped results and actual assistant output are required evidence.
- `CODING-STYLE.md`: table-driven tests, subprocess skip assertions, race checks and symbol citations.

## Context

The OAuth branch reads and copies the operator's `.claude.json`, skipping on a read error. This prerequisite served the deleted PTY runner and can silently suppress stream-json tests even with credentials present. Removing it is one fixture behavior change with offline regression coverage and live proof. No decision record is needed.

Sizing: approximately 260 written lines including tests and this plan; zero new exported types/interfaces, zero consumer call-site updates, three acceptance criteria and one remaining reject branch (missing credentials). The #496 analogue introduced the copy that this ticket removes. All five limits remain below their ceilings. No other fetched feature branch touches these fixture files.

## Design

`authenticatedHome(t) string` accepts either nonempty supported credential and returns `t.TempDir()` without reading or copying operator configuration. Its existing skip message remains when neither credential is nonempty. `WithWorktreeAuthenticated` continues pinning HOME and re-pinning only nonempty credentials, preserving unset versus set-empty variables.

Replace the obsolete copy/skip tests with a matrix over both fixture entry points, OAuth-only/API-key-only/both credentials and missing/read-failing/readable operator configuration. A directory at `.claude.json` gives a reliable file-read error even under root. Check isolation, no seeded configuration, credential value/presence preservation, HOME pinning for the wrapper and no process environment mutation for `authenticatedHome`. An outer assertion detects an unexpected subtest skip rather than accepting it as green.

Extend `TestWithWorktreeAuthenticated_RealAssistant` with the inherited-credential proof plus named OAuth-only missing-file and read-failing-file cases. Only skip those cases when the OAuth variable is unavailable; no additional opt-in switch. Use disposable operator HOMEs, clear the API key locally, then invoke real `RunPyryAgentRun`. Each case requires exit success, a nonempty session ID, no synthetic/auth-failed markers and an assistant JSONL event with end-of-turn and nonempty text. Preserve the operator's actual configuration.

## Concurrency model

No new goroutines. Environment-changing contract and live tests remain serial and use `t.Setenv` cleanup. `authenticatedHome` remains usable by parallel consumers; `homeEnv` remains unchanged. Existing `RunPyryAgentRun` owns subprocess timeouts and cleanup.

## Error handling

Only missing credentials cause a fixture skip. Temporary directory creation failures remain testing-framework failures. Credential validity is decided by real Claude, so invalid authentication fails the live proof rather than becoming a successful skip.

## Testing strategy

Write the matrix first and observe failures for OAuth missing/read-failing configuration and readable-file copying. Preserve and extend the subprocess test asserting missing-credential skips and both variable names for both fixture entry points. Run relevant offline fixture contracts with `-race -tags e2e_realclaude` and no real credentials, compile/vet the tagged package, run `go vet ./...` and build `cmd/pyry` into scratch storage.

The dispatcher owns the full-module gate and live-Claude run. Pending live evidence must report named results for `TestWithWorktreeAuthenticated_RealAssistant/inherited`, `/oauth_only_missing` and `/oauth_only_read_error`, plus executed/passed/failed/skipped counts and assistant/session assertions. Test starts or exit 0 alone are insufficient. No committed capture artifacts are required.

## Open questions

None. Live OAuth validity and assistant output are verified by the dispatcher-owned gate, not inferred from offline tests.

## Documentation handoff

- Pending documentation stage: replace **Second prerequisite: `~/.claude.json` with `hasCompletedOnboarding=true`** in `docs/knowledge/features/e2e-realclaude-operator-authentication.md`, and revise the `WithWorktreeAuthenticated` fixture paragraph in `docs/knowledge/features/e2e-realclaude-smoke-test-go.md`. State that current stream-json tests accept either credential, isolate HOME without copying the onboarding file, and skip for missing credentials only.
- Pending documentation stage: in `docs/release-tooling.md`, under **Live-claude suite — read the count, not the exit code**, distinguish test starts from named non-skipped passes and explain that authentication evidence requires a real assistant response.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `authenticatedHome` checks credential presence only; real Claude remains the authority on validity. The live proof requires assistant content, preventing synthetic auth envelopes from passing.
- [Tokens] Credentials stay in the inherited environment and are never printed by new assertions; offline values are synthetic. OAuth cases clear the API key with scoped cleanup so API authentication cannot falsely prove OAuth validity.
- [File operations] Removing operator-file reads/copies reduces exposure. All test configuration is under `t.TempDir`; the read-error witness is a private directory, not an operator file or symlink. No persistent writes or traversal inputs are introduced.
- [Subprocesses] Reuse `RunPyryAgentRun` with fixed arguments, isolated HOME and its existing context deadline; no shell interpreter or new execution path.
- [Cryptography] No keys, nonces or cryptographic operations change.
- [Network and I/O] No listener or new parser changes; live tests reuse the existing bounded subprocess timeout and JSONL parser.
- [Errors, logs, telemetry] New success evidence states only that session/assistant assertions passed. Do not log inherited credential values or operator configuration.
- [Concurrency] Matrix/live tests stay serial; `t.Setenv` restores changes. No environment writes enter `authenticatedHome`, and no new goroutine needs shutdown.
- [Threat model] Test-fixture-only change: no relay/mobile protocol or daemon authorization changes. Operator configuration is never edited or relocated.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07
