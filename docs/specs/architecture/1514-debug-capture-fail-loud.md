# #1514 — reject `debug_capture: true` at startup; delete the dead `RecordDir` plumbing

Short plan: the change is one guard plus deletions, no new type, state or failure mode beyond the one rejected value.

## Files read

- `cmd/pyry/main.go` → `runSupervisor` — the `recordDir` local, its `cfg.DebugCapture` gate and the `RecordDir:` write into `sessions.SessionConfig`; the guard goes right after `config.Load`'s error check, before any session is built or the control socket served.
- `cmd/pyry/main.go` → `selectInteractiveRunner` — the `"pty"` arm is the precedent for a removed-in-#1348 rejection message; its doc comment says `config.Load` stays parse-only "matching DebugCapture", which remains true.
- `cmd/pyry/main.go` → `resolveRecordingsDir` and the `bundleRecordingsDir` wiring comment above `debugbundle.Assemble` — both survive; their comments become false and get rewritten.
- `cmd/pyry/streamsup_runner.go` → `mapStreamsupConfig` doc comment — drop only `RecordDir` from the "not mapped" list.
- `internal/sessions/pool.go` → `SessionConfig.RecordDir` — the field nothing reads; deleted.
- `internal/config/config.go` → `Config.DebugCapture` — field stays decoded; doc comment rewritten to say `true` is rejected at startup.
- `cmd/pyry/interactive_runner_test.go` → `TestSelectInteractiveRunner` — the test shape to mirror.

## Change

Add `checkDebugCapture(cfg config.Config) error` in `cmd/pyry/main.go`: nil when `DebugCapture` is false, otherwise an error naming `debug_capture`, saying the recorder was removed in #1348, and telling the operator to remove the key or set it to `false`. `runSupervisor` calls it immediately after `config.Load` succeeds and returns the error, so the daemon exits non-zero before building the pool, the runner factory or the control server. `false`/absent takes the nil branch: no log, no warning. Delete the `recordDir` local and gate, the `RecordDir:` field write, `SessionConfig.RecordDir`, and the `RecordDir` token in `mapStreamsupConfig`'s comment (other stale names there stay). `resolveRecordingsDir` and `bundleRecordingsDir` stay, so old `.cast` files still ship in a debug bundle; only their comments change. `config.Load` stays parse-only; `internal/config/config_test.go` is not touched.

## Testing strategy

New `cmd/pyry/debug_capture_test.go`, table test of `checkDebugCapture` beside `TestSelectInteractiveRunner`: zero-value config and `DebugCapture: false` → nil; `DebugCapture: true` → error containing `debug_capture`, `#1348`, and `false`. Deletion is covered by the build (`go build`, `go vet`) and the AC's `git grep -in recorddir -- '*.go'` returning nothing. The existing `debug_capture` cases in `internal/config/config_test.go` run unmodified.

## Documentation handoff (pending — documentation stage)

- `docs/knowledge/features/config-package.md` — `DebugCapture` is described as a live recording toggle; say `debug_capture: true` is rejected at startup since #1514.
- `docs/knowledge/INDEX.md` — same correction, and drop the description of the deleted `ptyrunner` flight recorder.
