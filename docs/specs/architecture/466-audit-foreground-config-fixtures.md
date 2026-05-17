# Spec — #466: audit + migrate remaining foreground-mode Config fixtures to Bridge mode

## Files to read first

The developer's turn-1 read list. Each entry names the file, the relevant range, and what to extract.

- `internal/sessions/pool_test.go:19-44` — `helperPool` doc-comment + conditional Bridge wiring. The intent-locking pattern (doc-comment says "tests don't call Run") that the audit treats as already satisfying AC#3.
- `internal/sessions/pool_test.go:46-79` — `helperPoolWithSleepArgs`, the canonical Bridge-mode fixture. The 11-line doc-comment block (`pool_test.go:46-56`) is the rationale that this spec's other Bridge-additions are aligning with — copy its shape, not its verbatim text.
- `internal/sessions/pool_test.go:147-163` — `helperPoolPersistent` doc-comment, the "tests don't call Run" shape; same pattern applies to `helperPoolReconciling` (`pool_test.go:302-315`).
- `internal/sessions/pool_test.go:803-872` — `TestPool_Run_StartsWatcher` end-to-end (cfg literal, Bridge omission, `go func() { done <- pool.Run(ctx) }`). One of two `pool_test.go` Run-reaching sites that needs Bridge.
- `internal/sessions/pool_test.go:967-983` — `TestPool_Resurrect_*` cfg with Bridge correctly set, including the inline rationale comment. Reference shape for the in-place style when the cfg sits inside a `New(...)` call.
- `internal/sessions/pool_test.go:1086-1109` — `helperDummySession` using `supervisor.Config{}` (out of scope here — listed only so the developer doesn't accidentally edit it).
- `internal/sessions/pool_cap_test.go:16-50` — `helperPoolCap`, the two-paragraph doc-comment shape used when the helper extracts a `logger` to share with `NewBridge`. Use this as the layout template for the new Bridge insertions.
- `internal/sessions/pool_create_test.go:18-52` — `helperPoolCreate`, a second example of the logger-extraction pattern.
- `internal/sessions/session_test.go:192-209` — `TestSession_IdleEvictionDeferredWhileAttached`, the inline-test style with `logger := …; bridge := supervisor.NewBridge(logger); cfg := Config{… Bridge: bridge …}`. Pattern for inline tests; matches the existing imports.
- `internal/sessions/session_persist_test.go:1-10` — current import block. Adding Bridge requires importing `github.com/pyrycode/pyrycode/internal/supervisor`.
- `internal/supervisor/supervisor.go:81` and `:444-448` — foreground-mode mechanism (`Bridge == nil` wires PTY to `os.Stdin`/`os.Stdout`) and `/dev/tty` fallback. Background context only; do not modify.
- `internal/supervisor/supervisor_test.go` — `TestSupervisor_Foreground_NoStdinReaderLeak`. Out of scope — guards the production foreground path itself.
- `docs/lessons.md` — search for the 2026-05-02 entry "Bridge fixtures shipped, OS-level flake class identified" for the canonical narrative.

## Context

#41 surfaced a deadlock class in test fixtures that run the supervisor in foreground mode at scale. In foreground mode (`SessionConfig.Bridge == nil`) the supervisor wires the child PTY directly to `os.Stdin` / `os.Stdout`. Every `Run()` cycle spawns an `io.Copy(ptmx, os.Stdin)` goroutine; `os.Stdin` has an internal `fdMutex` that strands these goroutines with no return path, deadlocking teardown when many supervisors run concurrently (e.g. `TestPool_Supervise_ConcurrentCalls_RaceClean`'s 33-way fan-out).

Production runs as a service, so `Bridge` is always non-nil there. The class is purely a test-fixture mismatch with the production code path.

#41 migrated the highest-traffic fixture (`helperPoolWithSleepArgs`). Subsequent work migrated `helperPoolCap`, `helperPoolCreate`, and several inline test cfgs. This ticket finishes the audit: every `sessions.Config` literal in `internal/sessions/*_test.go` is either (a) set to Bridge mode if it reaches `Run()`, or (b) explicitly annotated as intentionally foreground if it does not.

The work is **audit-then-fix**, not blanket migration. Configs that never call `Run()` are safe in foreground mode — the I/O pumps only spawn when `runOnce` is invoked. Annotating them prevents future edits from silently turning a lookup-only fixture into a `Run()`-reaching one without setting Bridge.

Out of scope (do **not** edit):

- `internal/sessions/rotation/*_test.go` — different `Config` type, no supervisor involved.
- `internal/supervisor/*_test.go` — foreground-mode regression tests by design; they MUST stay foreground.
- `helperDummySession` (`pool_test.go:1086-1109`) and `addCapTestSession` (`pool_cap_test.go:58-95`) — these use `supervisor.Config{}`, not `sessions.Config{}`. Both already set Bridge correctly.

## Audit

The ticket body listed ~10 sites; a fresh `grep -n 'Config{' internal/sessions/*_test.go` (excluding `rotation/`) returns **17 `sessions.Config` literals** plus **2 `supervisor.Config` literals**. The body called this out — the audit is canonical, the body is informative-only.

Classification of all 17 `sessions.Config` literals:

### Already correct — no edit

| File:Line | Symbol | Why correct |
|---|---|---|
| `pool_test.go:30` | `helperPool` | Doc-comment (L19-24) locks intent ("tests that use this do not call Run"). Bridge is set conditionally via `withBridge` parameter for Attach-related callers. AC#3 satisfied by the doc-comment. |
| `pool_test.go:63` | `helperPoolWithSleepArgs` | Migrated 2026-05-02 with canonical doc-comment (L46-56). |
| `pool_test.go:154` | `helperPoolPersistent` | Doc-comment (L147-148) says "tests don't call Run". AC#3 satisfied. |
| `pool_test.go:309` | `helperPoolReconciling` | Doc-comment (L302-303) says "never spawned". AC#3 satisfied. |
| `pool_test.go:968` | `TestPool_Resurrect_*` | Bridge already set (L981) with inline rationale. |
| `pool_create_test.go:34` | `helperPoolCreate` | Bridge set + doc-comment. |
| `pool_cap_test.go:33` | `helperPoolCap` | Bridge set + canonical doc-comment. |
| `session_test.go:194` | `TestSession_IdleEvictionDeferredWhileAttached` | Bridge set (L198), uses `Attach` (needs Bridge). |

### Reaches `Run()` — needs Bridge added

| File:Line | Test/helper | Run path |
|---|---|---|
| `pool_test.go:805` | `TestPool_Run_StartsWatcher` | `go func() { done <- pool.Run(ctx) }()` at L825. Suspected source of the intermittent flake noted on #39's PR review. |
| `pool_test.go:1028` | `TestPool_ParityWhenIdleDisabled` | `go func() { _ = sess.Run(ctx) }()` at L1047. |
| `session_persist_test.go:24` | `helperPoolPersistentIdle` | All three callers (`TestSession_EvictBlocksUntilPersisted`, `TestSession_ActivateBlocksUntilPersisted`, `TestSession_EvictActivateStress`) use `runPoolInBackground` which calls `pool.Run(ctx)`. |
| `session_test.go:124` | `helperPoolIdle` | Caller `TestSession_IdleEvictionFires` calls `sess.Run(ctx)` at L166. |
| `session_test.go:376` | `TestSession_IdleEviction_EmitsLogRecord` | `go func() { _ = sess.Run(ctx) }()` at L395. Logger variable already extracted at L375 — only the Bridge field needs adding. |

### Stays foreground — needs intent-locking comment

| File:Line | Test/helper | Why foreground is safe |
|---|---|---|
| `pool_test.go:1068` | `TestPool_New_MalformedRegistryIsFatal` | `New(...)` is expected to return an error before any `Run` path; `pool` is `nil` on success. |
| `pool_conv_sweep_test.go:152` | `TestPool_New_HonoursConfigSweepInterval` | Reads `pool.convSweepInterval` after `New`. No `Run` call. |
| `pool_conv_sweep_test.go:176` | `TestPool_New_DefaultSweepIntervalWhenConfigZero` | Same shape — `New` then field read. |
| `pool_list_test.go:180` | `TestPool_List_RaceClean` | Concurrent `List()` readers + `RotateID` mutator. Exercises `Pool.mu` lock ordering only; no `Run` call. |

### Already in the file but using `supervisor.Config` (out of scope)

- `pool_test.go:1098` (`helperDummySession`) — supervisor cfg, Bridge already set.
- `pool_cap_test.go:61` (`addCapTestSession`) — supervisor cfg, Bridge already set.

## Required changes

Active edits: **5 Bridge additions** + **4 intent-locking comments** + **1 new import**. ~20 LOC total across 6 files.

### Bridge additions

For each site below, follow the surrounding style. Two acceptable shapes already exist in the codebase:

- **Helper-with-extracted-logger** (preferred when the literal sits in a helper):
  - Extract `logger := slog.New(slog.NewTextHandler(io.Discard, nil))` ahead of the cfg literal.
  - Reuse it for both `Bridge: supervisor.NewBridge(logger)` and `Logger: logger`.
  - Reference shape: `helperPoolCap` (`pool_cap_test.go:32-44`).

- **Inline-with-inline-NewBridge** (preferred when the cfg is inside a `New(...)` call inside a single test):
  - `Logger: logger` at the cfg's outer field.
  - `Bridge: supervisor.NewBridge(logger)` inside `SessionConfig`.
  - Reference shape: `TestPool_Resurrect_*` (`pool_test.go:967-983`).

Per-site:

1. **`pool_test.go:805` (`TestPool_Run_StartsWatcher`).** Extract `logger` before the `New(Config{...})` call (currently `Logger: slog.New(...)` is inline at L813). Use inline-style; copy the rationale comment shape from `pool_test.go:977-981`. Mention in a one-line comment that this also addresses the suspected flake noted on #39 PR review.

2. **`pool_test.go:1028` (`TestPool_ParityWhenIdleDisabled`).** Helper-with-extracted-logger style (the cfg is assigned to a variable). Add Bridge inside the `SessionConfig`.

3. **`session_persist_test.go:24` (`helperPoolPersistentIdle`).** Helper-with-extracted-logger style. **Add `"github.com/pyrycode/pyrycode/internal/supervisor"` to the import block** — currently absent. Reference: `pool_cap_test.go` import block.

4. **`session_test.go:124` (`helperPoolIdle`).** Helper-with-extracted-logger style. `supervisor` import already present in this file.

5. **`session_test.go:376` (`TestSession_IdleEviction_EmitsLogRecord`).** Logger already extracted at L375 (`logger := slog.New(rec)`). Just add `Bridge: supervisor.NewBridge(logger)` inside `Bootstrap`. This is the smallest change.

### Intent-locking comments

The wording should make the contract obvious to a future reader who runs `git blame` after a test starts deadlocking. Suggested templates (pick whichever fits the local context — these are not mandatory verbatim text):

- `pool_test.go:1068` (above the `pool, err := New(Config{...` line):
  ```
  // No Bridge — New is expected to return an error here; Run is never reached.
  ```

- `pool_conv_sweep_test.go:152` and `:176` (above each `cfg := Config{` line):
  ```
  // No Bridge — test only inspects post-New fields; Run is not called.
  ```

- `pool_list_test.go:180` (above the `pool, err := New(Config{...`):
  ```
  // No Bridge — exercises Pool.mu lock ordering via List/RotateID only; Run is not called.
  ```

A one-liner placed immediately above the cfg literal (not as a trailing comment) keeps grep-ability and survives gofmt rewrites.

### What NOT to change

- Do not modify any helper or test currently in the "Already correct" table.
- Do not delete `TestPool_Run_StartsWatcher`. The ticket asks only to note in the PR description if its flake disappears post-fix (and link the #39 review thread). Deletion is gated on #55 coverage, which is out of scope.
- Do not edit `internal/supervisor/*_test.go` — those guard the foreground-mode production path.
- Do not edit `internal/sessions/rotation/*_test.go` — different `Config` type.

## Test plan

The audit is mechanical; correctness lands in two checks:

1. **Build still compiles after import addition.** `go vet ./internal/sessions/...` after editing `session_persist_test.go` — catches a missing/unused-import or mismatched-logger-variable bug fast.
2. **Race-clean under repetition.** `go test -race -count=10 ./internal/sessions/...` on macOS (the dev box). CI runs Linux separately on push. Watch for:
   - Any test that now deadlocks (would indicate a Bridge wire mismatch — e.g. forgot `Logger: logger` and Bridge picked up a different logger).
   - `TestPool_Run_StartsWatcher` going from flaky to clean across all 10 runs — note this in the PR description if observed.

If a flake unrelated to this work surfaces during `-count=10`, document it in the PR description but **do not fix it in this PR**. Scope discipline.

## Open questions

- **None blocking.** The audit is exhaustive and the per-site classifications are unambiguous from the existing doc-comments and `Run`-call presence.
- **Follow-up (out of scope here):** if #55's coverage of the watcher-start path lands, `TestPool_Run_StartsWatcher` becomes deletable. Track separately, not in this PR.

## Acceptance criteria checklist

- [ ] All 17 `sessions.Config` literals in `internal/sessions/*_test.go` are classified per the audit table above.
- [ ] Each of the 5 Run-reaching sites sets `Bridge: supervisor.NewBridge(logger)`.
- [ ] Each of the 4 foreground-staying sites carries a one-line intent-locking comment above the cfg literal (the 4 sites listed under "Stays foreground"). The 4 existing helpers with doc-comments that already lock intent (helperPool, helperPoolWithSleepArgs, helperPoolPersistent, helperPoolReconciling) do not need additional inline comments.
- [ ] `session_persist_test.go` imports `github.com/pyrycode/pyrycode/internal/supervisor`.
- [ ] `go test -race -count=10 ./internal/sessions/...` is green on macOS; CI Linux job is green on push.
- [ ] PR description notes whether `TestPool_Run_StartsWatcher`'s suspected flake disappears, with a link to the #39 PR review thread that first flagged it.
