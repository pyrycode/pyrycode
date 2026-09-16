# 2470 — keep the failing test name in the gate-log tail

Test-only. No production file changes, no behaviour change.

## Files read

- `internal/streamsup/helper_test.go` → `TestMain` — the single install point; it
  already dispatches the fake-claude child before `flag.Parse`, so the logger
  install has to sit on the `m.Run()` side of that branch and nowhere else.
- `internal/streamsup/runner.go` → `New` — the `cfg.Logger == nil` fallback to
  `slog.Default()`, and `Runner.log`, the field it lands in. This is the fallback
  every one of the 101 stderr records came through.
- `internal/streamsup/watchdog.go` → `NewWatchdog`, `newStallTracker` — two more
  `slog.Default()` fallbacks in the same package; the same install covers them,
  which is why the fix belongs at the default logger and not at a call site.
- `internal/streamsup/runner_test.go` → `helperRunCfg` — the shared test Config
  builder. It leaves `Logger` unset, so it is literally the "test that omits an
  explicit logger" the second acceptance criterion names; the regression test
  reuses it rather than hand-rolling a Config.
- `docs/knowledge/features/streamsup-package.md` § "Testing" — records that the
  fake-child harness dispatches from `TestMain` on `GO_STREAMSUP_HELPER=1`
  *before* `flag.Parse`; that ordering constraint is why the install goes after
  the helper branch.

## Change

`TestMain` installs a discarding default logger — `slog.SetDefault(slog.New(slog.DiscardHandler))`
— immediately before `m.Run()`, after the existing `GO_STREAMSUP_HELPER` child
dispatch. Every supervisor record this package's test binary writes today reaches
stderr through the `cfg.Logger == nil` → `slog.Default()` fallback in `New` (and
the two siblings in `watchdog.go`); nothing in the package captures `os.Stderr`
or calls `slog.SetDefault`, so re-pointing the process default is sufficient and
is the only edit needed. Measured at `dbf97327`: the `-race` binary run from the
package directory exits 0 and writes 25,661 bytes over 101 lines to its own
stderr; the same run after the change must write 0.

Nothing else moves. Tests that assert on log content already pass their own
recorder into `Config.Logger`, so they never consult the default. The install is
process-local to this test binary, so `internal/e2e`'s assertion on
`spawning claude` in the *daemon's* stderr is untouched. The helper-child branch
keeps priority: `helperChild` always `os.Exit`s, and putting the install after it
keeps the fake claude byte-identical to what it is today.

One new test file, `internal/streamsup/log_silence_test.go`, holds the
regression test.

## Testing strategy

`TestDefaultLogger_DiscardsEveryRecord` — the guard the second acceptance
criterion asks for, in two parts:

- The installed default refuses every level: a table over `slog.LevelDebug`
  through `slog.LevelError+4` requiring `slog.Default().Enabled` false for each.
  Deleting or weakening the `slog.SetDefault` call in `TestMain` reddens this —
  the stdlib default is enabled from Info up.
- The omitted-logger path resolves to that default: build a Config with
  `helperRunCfg` (which sets no `Logger`), construct through `New`, and require
  the resulting `Runner.log` to be `slog.Default()` by pointer identity. Without
  this half the first half is a statement about `slog` rather than about this
  package — a future change re-pointing `New`'s fallback at a fresh stderr
  handler would restore the 101 records with the level table still green.

Verification per § B2: `go test -race ./internal/streamsup/...`, `go vet ./...`,
`go build ./cmd/pyry`, plus a before/after byte count of the compiled `-race`
binary's own stderr, run from the package directory, recorded in the PR.

## Documentation handoff

None. The ticket names no documentation acceptance criterion, and no shared doc
under `docs/` states the current stderr behaviour.
