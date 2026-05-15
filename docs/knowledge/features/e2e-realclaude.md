# `internal/e2e/realclaude` — real-`claude`-binary integration suite

Sibling Go package to [`internal/e2e`](e2e-harness.md), gated by a distinct build tag so the real-`claude` trust-boundary suite is opt-in and never runs under `make test` / `make check`.

## Why a sibling, not part of `internal/e2e`

`internal/e2e` carries `//go:build e2e || e2e_install` and drives `pyry` against a fake-claude (`TestHelperProcess` or shell wrapper). That harness deliberately stops at the trust boundary with the real `claude` binary — useful for control-plane / supervisor coverage, but it can't catch the `/doctor` prompt-poisoning class of bug that broke Phase C on 2026-05-14.

`internal/e2e/realclaude` is the package where tests DO cross that boundary. Keeping it separate means:

- `make test` skips it via tag exclusion alone (no path filter).
- A future `make e2e` that picks up `e2e` / `e2e_install` won't accidentally pull real-claude tests in.
- Each suite's tag set documents its intent at the file header.

## Build tag

All files in the directory carry exactly:

```go
//go:build e2e_realclaude
```

Single tag, no alternation. The `e2e_install` precedent established the `e2e_<purpose>` naming.

## What's there today

- `smoke_test.go` (#361) — one test, `TestClaudeBinaryAvailable`, that:
  - Asserts `exec.LookPath("claude")` succeeds. **Fatal, not skip** — the suite is opted-into by typing `make e2e-realclaude`, so a missing binary is misconfiguration, not absence.
  - Runs `exec.CommandContext(ctx, "claude", "--version")` under a 10 s timeout and asserts a zero exit. `CombinedOutput()` is reported on failure for debuggability. The version string is NOT parsed — "real claude is on PATH and executes" is the entire assertion.
- `fixtures.go` — shared primitives for every downstream test. All symbols live in the same file under the same `//go:build e2e_realclaude` tag.
  - `WithWorktree(t) string` (#372) — `t.TempDir()` + `t.Setenv("HOME", dir)`, returns the path. Pins HOME for both the in-test process and any subprocess so `os.UserHomeDir()` resolves to the same root on both sides. Does NOT create `.claude/…`; the runtime owns that.
  - `ReadJSONL(t, workdir, sessionID) []JSONLEntry` (#372) — opens `<HOME>/.claude/projects/<agentrun.EncodeProjectDir(workdir)>/<sessionID>.jsonl` and runs it through `jsonl.NewReader(...).Next()`. Empty file → empty slice; open/parse failures call `t.Fatalf` with the resolved path embedded. A private `resolveAndOpenJSONL` split exists so the missing-file path is testable as a returned error.
  - `JSONLEntry = jsonl.Event` (#372) — type **alias**, not a wrapper struct. Keeps downstream tests from importing `internal/agentrun/jsonl` directly while preserving full field access. See [`codebase/372.md`](../codebase/372.md) for the design rationale.
  - `RunPyryAgentRun(t, opts) RunResult` (#373) — synchronous subprocess invoker. Builds `pyry` once per test process via `sync.Once` (honours `PYRY_E2E_BIN` short-circuit), writes `<workdir>/prompt.txt` + `system.txt` from `opts.Prompt`/`opts.SystemPrompt`, invokes `pyry agent-run` with the eight required flags in `--flag=value` form (`--prompt-file`, `--system-prompt-file`, `--allowed-tools`, `--max-turns`, `--effort`, `--model`, `--workdir`, `--output-format=stream-json`), captures stdout/stderr/exit code, and returns the `session_id` parsed from the first stream-json `{"type":"system","subtype":"init",…}` line. **Non-zero subprocess exit is NOT fatal** — callers assert on `RunResult.ExitCode` themselves so the real CLI's error paths can be tested. Only structural failures (validation, build, exec start, timeout) call `t.Fatalf`. `RunOpts` mirrors `cmd/pyry/agent_run.go`'s flag surface 1:1 — no `SessionID` input, no `Mode` enum. See [`codebase/373.md`](../codebase/373.md) for the design rationale.

The composition pattern downstream tests use: `WithWorktree` → `RunPyryAgentRun` → `ReadJSONL`. Subsequent tickets (#364–#368, the actual prompt-poisoning / trust-boundary tests) build on this three-step shape.

## Test infrastructure

`fixtures_test.go` re-execs the test binary as a fake `pyry` when `GO_TEST_HELPER_PROCESS=1` is set (via a `TestMain` branch), and pins `PYRY_E2E_BIN=os.Args[0]` for every other test so `ensurePyryBuilt` short-circuits to the fake. The fake selects behaviour from `PYRY_E2E_FAKE_MODE` (`happy`, `fail`, `sleep`, `argv`). This lets the helper's contract be validated entirely from within the package — no real `claude` and no real `pyry` build are required for the helper's own tests. (The smoke test `TestClaudeBinaryAvailable` from #361 remains the only test in the suite that depends on real `claude` being on PATH.)

## Make target

```make
.PHONY: e2e-realclaude
e2e-realclaude:
	$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...
```

No `-race`. These are I/O-bound trust-boundary checks, not goroutine-stress tests; flip on `-race` per-test when a future test in the directory does spin goroutines.

`make check` is unchanged. CI's per-PR `make check` does not run this suite — it stays opt-in for that path.

## CI cadence: code-review phase, no nightly workflow

The real-`claude` suite is NOT wired into GitHub Actions. It runs **locally
during the code-review phase** of every dispatched ticket via the pipeline
— see the code-review agent's `CLAUDE.md` for the invocation contract.

The earlier nightly workflow (`.github/workflows/e2e-realclaude-nightly.yml`,
#362) was removed in #379 the same day it landed. CI-side rationale for the
removal:

- GitHub Actions would need an `ANTHROPIC_API_KEY` repo secret; Max-plan
  tokens used locally are free.
- Per-run cost ($0.10–$0.50, scaling with test count) buys nothing local
  runs don't already cover once code-review runs the suite on every PR.
- Failure surface synchronised to dispatch cadence beats unpredictable
  04:00 UTC failures.
- One fewer CI file to keep in lockstep with `self-check-daily.yml`.

The make target is unchanged — `make e2e-realclaude` is still the entry
point, just no longer invoked by CI.

## Verifying tag exclusion

After landing, `make test 2>&1 | grep realclaude` should be empty (or only an `ok ... [no test files]` line) — files with an unsatisfied build tag are dropped at the build stage, so the package compiles to an empty test binary.

## Related

- [features/e2e-harness.md](e2e-harness.md) — the fake-claude sibling suite.
- [features/install-e2e.md](install-e2e.md) — the `e2e_install`-tagged install round-trip suite (same naming pattern).
- [features/agentrun-selfcheck-package.md](agentrun-selfcheck-package.md) — `self-check-daily.yml`, the sibling badge-only nightly self-check workflow.
- Ticket [#361](https://github.com/pyrycode/pyrycode/issues/361) — scaffolding ticket; codebase note at [`codebase/361.md`](../codebase/361.md).
- Ticket [#362](https://github.com/pyrycode/pyrycode/issues/362) — the now-removed nightly workflow; codebase note at [`codebase/362.md`](../codebase/362.md). See also [#379](https://github.com/pyrycode/pyrycode/issues/379) for the removal.
- Ticket [#372](https://github.com/pyrycode/pyrycode/issues/372) — `WithWorktree` + `ReadJSONL` fixture helpers; codebase note at [`codebase/372.md`](../codebase/372.md).
- Ticket [#373](https://github.com/pyrycode/pyrycode/issues/373) — `RunPyryAgentRun` subprocess fixture helper; codebase note at [`codebase/373.md`](../codebase/373.md).
