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

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The only input is `Config.DebugCapture`, a bool decoded by `config.Load` from the operator-owned config file; decoding is unchanged. `checkDebugCapture` reads the parsed bool and nothing else, and runs at the composition root in `runSupervisor` before any untrusted peer (control socket, relay) can reach the process.
- [Tokens, secrets] No findings introduced. A `.cast` recording held every PTY byte, so it can hold secrets a user typed. After this change nothing in the module writes to `~/.local/share/pyry-recordings`: the deleted `SessionConfig.RecordDir` was the last plumbing, and `git grep` finds no other Go writer of that path (`resolveRecordingsDir` and `debugbundle.DefaultRecordingsDir` only resolve it for reading). The change therefore strictly reduces secret-bearing data at rest. A recording left from before #1348 still ships in a debug bundle, by design (AC 3); that surface is unchanged, and the bundle's request authorization and sealed-push delivery are untouched.
- [File operations] No findings. No write path remains. The surviving read path is unchanged: `resolveRecordingsDir` returns a compile-time-fixed path under `$HOME` with no untrusted input, `newestRecording` globs only the directory's top level, and `writeRecordingMember` opens with `O_NOFOLLOW` and copies exactly the stat-reported size, so a swapped-in symlink fails the open rather than exfiltrating its target. Stale recordings persist until the operator deletes them; that was already true when capture was turned off, and this ticket does not change it.
- [Subprocess execution] No findings. `RecordDir` was never read by `sessions.RunnerConfig` or any spawn path, so deleting it changes no argv, environment or spawn behaviour; the interactive spawn is byte-identical to today.
- [Cryptographic primitives] Not applicable — the change neither adds nor touches randomness, keys or primitives.
- [Network & I/O] No findings introduced. The bundle reads at most one recording, whose size is whatever an old recorder wrote; that bound is pre-existing and unchanged, and with no writer left the directory cannot grow.
- [Error messages, logs] No findings. The rejection error from `checkDebugCapture` is a static string naming only the key `debug_capture`, #1348 and the fix; it carries no config contents, paths or secrets. `false` and an absent key take the nil branch and log nothing. Assemble's zero-log-call property is untouched.
- [Concurrency] No findings. The guard runs before the pool, runner factory, control server or any goroutine is started, so a rejected start leaves no partial state, socket or child process behind.
- [Threat model] No findings. The change removes a capability (at-rest capture of PTY bytes) and adds a startup refusal; it opens no new endpoint, and the mobile protocol's debug-bundle path is unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-23

## Revisions

- 2026-09-23 (rework, verifier MUST FIX on PR #2537): added the `## Security review` section above, which the `security-sensitive` label requires and the first pass omitted. The review found nothing to change, so the design and the code are as committed.
