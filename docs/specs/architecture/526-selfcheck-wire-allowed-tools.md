# Spec #526 — Wire `AllowedTools` into selfcheck's `ptyrunner.Config`

## Files to read first

- `internal/agentrun/selfcheck/selfcheck.go:62` — the `ptyRun` package-level seam (`var ptyRun = ptyrunner.Run`) the test suite mocks.
- `internal/agentrun/selfcheck/selfcheck.go:72-77` — `canonicalAllow` constant (`[]string{"Read"}`); the doc comment couples it to the deny-default invariant and explicitly forbids parallel literals.
- `internal/agentrun/selfcheck/selfcheck.go:172` — the existing `settingsWrite(canonicalAllow)` call; the same constant must be passed to `ptyrunner.Config.AllowedTools` so the spawn's allow-list and the per-spawn settings file agree byte-for-byte.
- `internal/agentrun/selfcheck/selfcheck.go:196-215` — the broken `ptyrunner.Config` struct literal. The one-line fix lands here.
- `internal/agentrun/selfcheck/selfcheck_test.go:36-58` — `installSeams(t)`. Existing tests override `ptyRun` per-case; the new regression test follows the same shape.
- `internal/agentrun/selfcheck/selfcheck_test.go:71-102` — `TestSelfCheck_Pass`. Closest sibling to the new test (single-line `passLine`, then `select` block waiting for ctx-done). Reuse the fixture pattern verbatim; only the assertions change.
- `internal/agentrun/ptyrunner/runner.go:245-246` — the `cfg.AllowedTools == nil` check whose presence makes this field load-bearing. Read once to understand why nil (vs empty slice) trips it; the spec does NOT change runner.go.

## Context

`pyry agent-run --self-check` is the install-time canary that the agent-run pipeline refuses tools NOT on the allow-list. Since commit `058c569` tightened `ptyrunner.Config` to require `AllowedTools` (the synthetic init envelope stamps `Tools: cfg.AllowedTools`), every build off main fails with `pyry: agentrun: self-check: ptyrunner: AllowedTools required` before claude is ever spawned.

The production dispatcher path (`runAgentRunPty` in `cmd/pyry/agent_run.go`) already wires `parsed.allowedTools` correctly; only the selfcheck's `ptyrunner.Config` literal at `selfcheck.go:196-215` omits the field. The existing test suite missed the regression because its `ptyRun` mock accepts any `ptyrunner.Config` — the real Config's required-field contract is never exercised end-to-end inside the package.

## Design

### Production change (AC1)

Inside the `errgroup` goroutine that calls `ptyRun` at `selfcheck.go:196-215`, add one struct-literal field:

```go
AllowedTools: canonicalAllow,
```

Place it adjacent to `SettingsPath` (the two fields are the only configuration that carries the deny-default allow-list — keeping them visually paired makes the coupling obvious to future readers). The field reuses the existing `canonicalAllow` constant; no new symbol, no new file, no parallel literal.

Rationale recap (do not duplicate in code comments — the existing `canonicalAllow` doc comment at `selfcheck.go:72-77` already states it):

- `canonicalAllow` is `[]string{"Read"}`. The same slice is already passed to `settingsWrite(canonicalAllow)` at `selfcheck.go:172`, so the settings file on disk and the synthetic init envelope agree.
- `ptyrunner.Config.AllowedTools == nil` trips the runner.go:245 check; an empty `[]string{}` would pass the check but break the invariant. Using `canonicalAllow` is the only correct choice.

### Test change (AC2)

Add one new test to `internal/agentrun/selfcheck/selfcheck_test.go`. Its sole job is to capture the `ptyrunner.Config` the mock seam receives and assert `cfg.AllowedTools` equals `canonicalAllow`. This is the regression net: the next time someone adds a required field to `ptyrunner.Config` without wiring it into the selfcheck construction, this test (combined with the runner.go required-field check, which the test exercises via the real ptyRun in a follow-up only if we want a deeper net — see "Open questions") would fail.

**Test name:** `TestSelfCheck_PassesCanonicalAllowToPtyRunner`.

**Behaviour to specify (bullet-pointed scenarios — developer writes the code in the project's testing idiom):**

- Call `installSeams(t)` (existing helper at `selfcheck_test.go:36-58`).
- Override `ptyRun` with a closure that:
  - Captures `cfg.AllowedTools` into a `t.Run`-scoped variable (e.g. `var observedAllow []string`).
  - Writes `passLine + "\n"` to `cfg.Stdout` so the watcher sees end-of-turn and the call returns PASS (mirrors `TestSelfCheck_Pass`).
  - Holds briefly via `select { case <-ctx.Done(): case <-time.After(50 * time.Millisecond): }` (same pattern as `TestSelfCheck_Pass`).
- Call `SelfCheckDenyDefault(context.Background(), baseConfig(t))`; assert no error.
- Assert `len(observedAllow) == len(canonicalAllow)` and the slices are element-equal. `reflect.DeepEqual(observedAllow, canonicalAllow)` is acceptable; an explicit length-then-loop check is equally acceptable (project uses both idioms — match whichever is closest to the surrounding test file). Do NOT compare via slice identity (`&observedAllow[0] == &canonicalAllow[0]`) — that's not what we're asserting; identity may or may not hold depending on how the runner.go path eventually consumes the slice.
- Assert `observedAllow != nil` explicitly (separate from the equality check). The runner.go nil-check is the actual failure mode; an explicit non-nil assertion future-proofs against a regression where someone passes `[]string{}` "to be safe", which would silently break the deny-default invariant without tripping the runner.go check.

**What this test does NOT do:**

- Does not exercise the real `ptyrunner.Run` — the test seam at `selfcheck.go:62` is still in effect. The point is to catch the silent-drift pattern at the call site, not to integration-test the runner.
- Does not assert anything about `cfg.SettingsPath`, `cfg.SystemPrompt`, etc. — those have their own existing coverage (or not, but expanding the test surface is out of scope for this XS ticket).

### Manual smoke (AC3)

After the code change, the developer runs `pyry agent-run --self-check` on macOS against a real claude binary and captures the output. Expected:

```
pyry agent-run --self-check: PASS
```

Document the run in the PR description: paste the command, the output line, and the exit code (`echo $?` → `0`). This is the install-time canary; "I trust the unit test" is not sufficient here.

## Concurrency model

No change. The existing `errgroup.WithContext(timeoutCtx)` + two-goroutine spawner/watcher shape is unchanged; the fix is a struct-literal field addition inside the existing spawner closure.

## Error handling

No change to selfcheck's error surface. The current `ptyrunner: AllowedTools required` error disappears at the source (the Config is no longer malformed); all other error paths — `ErrBashInvoked`, `ErrTimeout`, trust/settings/sessionID failures, ptyrunner errors propagated through `g.Wait()` — are unchanged.

## Testing strategy

1. **The new regression test** (AC2) — described above.
2. **Existing test suite must continue to pass unchanged.** None of the existing tests assert on `cfg.AllowedTools`, so the additive field will not perturb them. Run `go test -race ./internal/agentrun/selfcheck/...` after the change.
3. **`go vet ./...` and `staticcheck ./...`** per project conventions.
4. **Manual smoke against real claude on macOS** (AC3) — the install-time canary that motivates the ticket.

## Open questions

None. The fix is mechanical, the constant to reuse is named and documented in-file, and the regression net is unambiguous.

## Out of scope (explicitly)

- Tightening the seam-mock contract globally (e.g. injecting the real runner.go required-field validation into the mock by default). Tempting after this bug, but it's a structural change to the test harness that affects nine sibling tests; not XS, not this ticket. If we want a wider net, file a follow-up.
- Renaming `canonicalAllow` or moving it. The constant lives where it's used; no churn.
- Adding new ptyrunner.Config required fields or relaxing the existing `AllowedTools == nil` check. The runner-side contract is correct as written; the bug is in the caller.
