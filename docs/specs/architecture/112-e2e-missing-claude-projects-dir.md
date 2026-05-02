---
ticket: 112
title: E2E test — pyry starts cleanly when ~/.claude/projects/ is missing
status: spec
size: XS
---

# Context

A first-run user has never invoked `claude` and therefore has no
`~/.claude/projects/` directory. The reconcile path in
`internal/sessions/pool.go` handles this via its `MissingDir` branch: a stat
returning `fs.ErrNotExist` is treated as "no JSONL transcripts to reconcile,"
not as an error. The daemon must come up with an empty registry, not crash.

This is covered at unit level today. What's missing is e2e coverage at the
binary boundary — proof that the assembled `pyry` process, not just the
`sessions` package in isolation, tolerates the missing dir.

The corrupt-registry sibling test (#111) already established the
"startup-shaped e2e" file (`internal/e2e/startup_test.go`); this ticket adds
one positive-outcome test to it. The harness's `Start(t)` already allocates
a fresh `t.TempDir()` HOME, which has no `.claude/projects/` by construction —
so no new harness surface is needed.

Split from #108. Sibling of #111 (corrupt-registry fail-loud).

# Design

## What's under test

The path exercised is exactly the happy-path startup, against a HOME that
happens to lack `.claude/projects/`:

```
cmd/pyry/main.go:run() → runSupervisor()
  → sessions.New(Config{...})
      → pool.reconcile()
          → os.Stat(<HOME>/.claude/projects/) → fs.ErrNotExist → MissingDir branch → no-op
      → New returns: pool with empty registry
  → control server enters Serve, socket becomes dialable
```

The assertion is **positive**: pyry reaches ready, `status` exits 0, `stop`
shuts down cleanly. Per ticket guidance, do not assert on a specific log line
about the missing dir; the contract is "comes up, is responsive, shuts down,"
not "logs X."

## Test

Append `TestE2E_Startup_MissingClaudeProjectsDir` to the existing
`internal/e2e/startup_test.go` (same build tag `e2e`, same package). No new
file: this is the second startup-shaped test and the file already exists for
exactly this category.

Sketch:

```go
func TestE2E_Startup_MissingClaudeProjectsDir(t *testing.T) {
    h := Start(t)

    // Sanity: the harness gives us a fresh t.TempDir() HOME, so the missing-dir
    // case is the default state. Assert it explicitly so a future change to
    // Start (e.g. pre-creating .claude/projects/ for some other reason) breaks
    // this test instead of silently invalidating its premise.
    claudeProjects := filepath.Join(h.HomeDir, ".claude", "projects")
    if _, err := os.Stat(claudeProjects); !errors.Is(err, fs.ErrNotExist) {
        t.Fatalf(".claude/projects/ unexpectedly exists at %s (err=%v); test premise invalidated",
            claudeProjects, err)
    }

    r := h.Run(t, "status")
    if r.ExitCode != 0 {
        t.Fatalf("pyry status exit=%d\nstdout:\n%s\nstderr:\n%s",
            r.ExitCode, r.Stdout, r.Stderr)
    }

    h.Stop(t) // teardown is also registered via t.Cleanup; explicit Stop
              // surfaces shutdown errors at the assertion point rather
              // than at end-of-test.
}
```

### Why a sanity check on the missing dir

The ticket says "there is nothing to pre-populate or remove" because
`t.TempDir()` is empty. That's true *today*. Asserting `fs.ErrNotExist` on
`<HomeDir>/.claude/projects/` before calling into the daemon means: if a
future harness change pre-creates that directory (for some unrelated test's
benefit), this test fails loudly instead of silently passing on a different
path than the one it claims to cover. The check costs three lines and
forecloses the failure mode.

### Why explicit `Stop` despite `t.Cleanup`

`Start(t)` registers `h.teardown` via `t.Cleanup`. That handles process
liveness and socket removal. But `t.Cleanup` runs after the test function
returns, so anything it would `t.Logf` lands after the test's pass/fail
verdict, and any `Logf` about a stuck process gets attributed to "after the
test." Calling `h.Stop(t)` inside the test makes "shuts down cleanly" a
verdict-bearing step. `Stop` is idempotent with the cleanup hook
(`sync.Once`), so this does not double-fire.

### Why not a table-driven combination with the corrupt-registry test

The two startup tests assert opposite outcomes: one expects ready+responsive,
the other expects exit-before-ready. They use different harness entry points
(`Start` vs. `StartExpectingFailureIn`) returning different types
(`*Harness` vs. `RunResult`). A table that switches on outcome shape is more
code than two flat tests. Per ticket guidance and #111's spec, keep them flat.

## What is NOT changed

- No changes to `harness.go`. Existing `Start(t)`, `Run`, and `Stop` cover
  every step.
- No changes to `internal/sessions/pool.go` or any production code. The
  `MissingDir` branch already exists; this ticket only covers it at the
  binary boundary.
- No new helpers in `restart_test.go` or anywhere else. `newRegistryHome` is
  used only by tests that pre-populate `<HOME>/.pyry/test/sessions.json`;
  this test does not, so calling it would be misleading.

# Concurrency model

Inherits the harness's existing model. `Start(t)` forks pyry, the harness
runs one wait-goroutine (closes `doneCh` on exit), the test goroutine polls
`waitForReady` until the control socket is dialable. `Run` is synchronous
(`exec.CommandContext` with `runTimeout`). `Stop` sends SIGTERM, waits on
`doneCh`, escalates to SIGKILL if needed. No new goroutines; no new shared
state.

# Error handling

| Failure                                                  | Surfaced as                                              |
|----------------------------------------------------------|----------------------------------------------------------|
| pyry exits before becoming ready                         | `Start` → `t.Fatalf` from `waitForReady` (existing)      |
| pyry never becomes ready within `readyDeadline`          | `Start` → `t.Fatalf` from `waitForReady` (existing)      |
| `.claude/projects/` exists in `t.TempDir()` (premise gone) | Test → `t.Fatalf` (the new sanity check)                 |
| `pyry status` exits non-zero                             | Test → `t.Fatalf`                                        |
| `pyry status` times out                                  | `Run` → `t.Fatalf` (existing `runTimeout` enforcement)   |
| `Stop` cannot terminate process                          | Harness → `t.Logf` after SIGKILL+grace (existing)        |

The test uses `t.Fatalf` throughout: there's only one positive verdict
(`status` exited 0), and downstream steps are meaningful only if upstream
ones succeeded.

# Testing strategy

- The test fails today only if the production `MissingDir` branch regresses
  (e.g. `os.Stat` error gets returned up the stack instead of being
  swallowed). That's the regression we want to catch at the binary boundary.
- The sanity check on missing dir guards against the test silently testing
  the wrong thing if `Start(t)` ever changes to pre-create `.claude/projects/`.
- No new harness code means no new harness coverage gap. If `Start(t)` is
  broken in some future refactor, every other e2e test breaks too — this
  test does not need its own diagnostic for that case.

CI runs `-tags=e2e` only on the e2e job; default `go test ./...` is
unaffected because the file is build-tagged `e2e` (already true of
`startup_test.go`).

# Open questions

None.

# Out of scope

- Asserting on log content about the missing dir. Production may or may not
  log the `MissingDir` no-op; either way is fine, and tying the test to a
  log line would lock the production code into emitting it.
- Coverage of the inverse case where `.claude/projects/` exists but is empty
  versus contains JSONL. The reconcile-from-JSONL path has its own tests at
  the unit level and at e2e (`restart_test.go`).
- Pre-creating `.claude/projects/` for any other test. The smoke test
  (`TestHarness_Smoke`) does not pre-create it today, so no changes to other
  tests are needed.
