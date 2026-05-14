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

## What's there today (#361 scaffold)

- `smoke_test.go` — one test, `TestClaudeBinaryAvailable`, that:
  - Asserts `exec.LookPath("claude")` succeeds. **Fatal, not skip** — the suite is opted-into by typing `make e2e-realclaude`, so a missing binary is misconfiguration, not absence.
  - Runs `exec.CommandContext(ctx, "claude", "--version")` under a 10 s timeout and asserts a zero exit. `CombinedOutput()` is reported on failure for debuggability. The version string is NOT parsed — "real claude is on PATH and executes" is the entire assertion.

Subsequent tickets (#362, #363) add the actual prompt-poisoning / trust-boundary tests on this scaffold.

## Make target

```make
.PHONY: e2e-realclaude
e2e-realclaude:
	$(GO) test -tags e2e_realclaude ./internal/e2e/realclaude/...
```

No `-race`. These are I/O-bound trust-boundary checks, not goroutine-stress tests; flip on `-race` per-test when a future test in the directory does spin goroutines.

`make check` is unchanged. CI's per-PR `make check` does not run this suite — it stays opt-in for that path.

## CI cadence: nightly workflow

`.github/workflows/e2e-realclaude-nightly.yml` (#362) runs `make e2e-realclaude` on a schedule so an upstream `claude` binary regression surfaces within 24h instead of at the next production dispatch.

- **Triggers:** `schedule: cron: "0 4 * * *"` (04:00 UTC daily) + `workflow_dispatch: {}` for manual validation. The 04:00 slot is offset from `self-check-daily.yml`'s 06:13 UTC so the two nightly real-claude jobs don't contend on any shared rate-limit window.
- **Job:** single `e2e-realclaude` job on `ubuntu-latest`. Steps: `actions/checkout@v6` → `actions/setup-go@v6` with `go-version: "1.26.x"` (exact parity with `self-check-daily.yml`) → `npm install -g @anthropic-ai/claude-code` → `make e2e-realclaude` with `ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}` in the step's `env:` block. **No `go build` step** — unlike `self-check-daily.yml` (which builds `pyry` then exec's it), this workflow runs Go tests directly via the make target; `go test` handles compilation.
- **`timeout-minutes: 15`.** Conservative cap. The #361 smoke test is fast (~seconds), but #363–#368 will add longer scenarios. Hard upper bound is GitHub Actions' default 6h; 15 minutes prevents a hung `claude` from burning that ceiling.
- **No `continue-on-error: true`.** A red nightly IS the entire signal.
- **Monitoring contract: badge-only.** Same as `self-check-daily.yml` (#336). No auto-issue, no Discord/Slack webhook. Rationale follows the project's evidence-based fix selection principle — there's no observed "operator missed a red badge" failure mode yet; adding a notification channel speculatively is premature. If badge-only proves insufficient in practice, follow up in a separate ticket.
- **Secret missing failure mode:** if `ANTHROPIC_API_KEY` is unset, the test that depends on it fails inside the suite — surfaces as a red badge with a clear test-output reason. No workflow-level pre-check.
- **Cost:** every run consumes API credits. Do not enable a more aggressive cadence without revisiting the cost/coverage tradeoff.

The make target is the only contract between the workflow and the suite — future tests added under `internal/e2e/realclaude/` are picked up automatically without touching CI.

## Verifying tag exclusion

After landing, `make test 2>&1 | grep realclaude` should be empty (or only an `ok ... [no test files]` line) — files with an unsatisfied build tag are dropped at the build stage, so the package compiles to an empty test binary.

## Related

- [features/e2e-harness.md](e2e-harness.md) — the fake-claude sibling suite.
- [features/install-e2e.md](install-e2e.md) — the `e2e_install`-tagged install round-trip suite (same naming pattern).
- [features/agentrun-selfcheck-package.md](agentrun-selfcheck-package.md) — `self-check-daily.yml`, the sibling badge-only nightly workflow whose monitoring contract this one mirrors.
- Ticket [#361](https://github.com/pyrycode/pyrycode/issues/361) — scaffolding ticket; codebase note at [`codebase/361.md`](../codebase/361.md).
- Ticket [#362](https://github.com/pyrycode/pyrycode/issues/362) — nightly workflow; codebase note at [`codebase/362.md`](../codebase/362.md).
