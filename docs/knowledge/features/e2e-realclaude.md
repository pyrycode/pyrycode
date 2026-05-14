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

`make check` is unchanged. CI still runs only `make check`. This suite is developer-local and opt-in.

## Verifying tag exclusion

After landing, `make test 2>&1 | grep realclaude` should be empty (or only an `ok ... [no test files]` line) — files with an unsatisfied build tag are dropped at the build stage, so the package compiles to an empty test binary.

## Related

- [features/e2e-harness.md](e2e-harness.md) — the fake-claude sibling suite.
- [features/install-e2e.md](install-e2e.md) — the `e2e_install`-tagged install round-trip suite (same naming pattern).
- Ticket [#361](https://github.com/pyrycode/pyrycode/issues/361) — scaffolding ticket; codebase note at [`codebase/361.md`](../codebase/361.md).
