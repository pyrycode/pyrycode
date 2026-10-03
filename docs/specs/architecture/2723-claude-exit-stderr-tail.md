# #2723 — log why claude exited, with its last stderr lines

## Files read

- `internal/streamsup/runner.go` → `Runner.Run`: the `claude exited` record, the `OnChildExit` call and the shutdown `ctx.Err()` return above it, and `drainRestart` below it.
- `internal/streamsup/runner.go` → `spawnAndWait`: `cmd.Stderr = r.cfg.Stderr`, `cmd.WaitDelay = killGrace`, the StdinPipe / Start failure returns, `cmd.Wait`. Its only caller is `Run`.
- `internal/streamsup/runner.go` → `beginSpawn`: the one restartMu section that snapshots the id each spawn's argv is built from. Six test call sites destructure its return list.
- `internal/streamsup/runner.go` → `Restart`, `RestartFresh`: both cancel `iterCancel`, so the child's iteration ctx is done when a deliberate restart kills it.
- `internal/streamsup/runner.go` → `liveSessionID` doc: the runner logger is pool-wide and untagged.
- `internal/control/logs.go` → `SlogTee`, `teeHandler.Handle`, `teeHandler.WithAttrs`: every record is formatted a second time into `RingBuffer`, which feeds `pyry logs` and `debugbundle.Assemble`.
- `cmd/pyry/main.go` → the daemon logger: `slog.New(control.SlogTee(slog.NewTextHandler(os.Stderr, …), logRing))`. The tee is the only production `slog.Handler` implementation in the tree.
- `cmd/pyry/streamsup_runner.go` → `mapStreamsupConfig`: leaves `Stderr` nil, so production children's stderr goes to `/dev/null` today.
- `internal/streamsup/helper_test.go` → `helperChild`: the fake-claude modes keyed by `GO_STREAMSUP_HELPER_MODE`.
- `internal/streamsup/runner_test.go` → `helperRunCfg`, `runInBackground`, `safeBuffer`.

No other feature branch touches these files.

## Context

On 2026-10-03 a bad flag made every interactive claude child exit about 145 ms after spawn with one stderr line. The daemon logged only `claude exited err="exit status 1"`, without a session id, 56 times. The child's stderr is discarded on the interactive path and the exit record cannot be told apart between sessions.

The tail must reach the daemon's own log output (journald / launchd) but never the log ring, because the ring reaches a paired phone through the debug bundle unredacted, and claude's stderr can hold paths or credentials.

## Design

### Tail capture (`internal/streamsup/stderr_tail.go`, new)

- `stderrTail`: a mutex-guarded `io.Writer` that keeps only the last `stderrTailBytes` (1024) bytes written to it. `String()` trims trailing newlines, keeps the last `stderrTailLines` (5) lines, and returns them joined by `\n`. The result is a suffix of what was written, at most 1024 bytes and at most 5 lines; longer input keeps its end.
- `stderrCapture` wires one spawn's stderr into a `stderrTail` without changing when `cmd.Wait` returns:
  - `captureStderr(cmd, forward io.Writer)`. With `forward` non-nil (tests and live probes that already pass `Config.Stderr`), it sets `cmd.Stderr = io.MultiWriter(forward, tail)`: those callers already had an exec-owned pipe, so their Wait semantics are unchanged. With `forward` nil (the production path), it hands the child the write end of an `os.Pipe` as `cmd.Stderr`. An `*os.File` is passed to the child directly with no exec copier goroutine, so `cmd.Wait` still does not wait on stderr holders — exactly as with the old nil. A pipe-creation failure degrades to the old nil (no tail), never a failed spawn.
  - `started(ok bool)`: called after `cmd.Start`. Closes the parent's write end; on success starts one reader goroutine copying the read end into the tail, on failure closes the read end.
  - `finish() string`: called after `cmd.Wait`. Waits for the reader to hit EOF, bounded by `stderrDrainGrace` (250 ms) in case a descendant of claude still holds the write end, then closes the read end, waits for the reader to exit, and returns the tail text.

Why not let exec own the pipe in production: an exec-owned pipe makes `Wait` block until every holder of the write end closes, up to `WaitDelay` (5 s), and turns a clean exit into `exec.ErrWaitDelay` when a holder lingers. claude's descendants (the stdio MCP servers, including pyry's own `pyry_files`) can inherit its stderr, so that would change crash/clean classification and respawn latency on paths this ticket has no live evidence for.

### Runner (`internal/streamsup/runner.go`)

- `beginSpawn` also returns the session id it built the argv from (`id`, placed between `cancel` and `args` so no two same-typed results sit adjacent). `Run` passes nothing new to `spawnAndWait`; it uses `id` in the exit record. The id is the one the child was spawned with, not the live one, which `AdoptSessionID` can move without a respawn.
- `spawnAndWait` returns a third value, `stderrTail string`. It calls `captureStderr` before `cmd.Start` and `finish` after `cmd.Wait`, and returns the tail only when `waitErr != nil` and its own `ctx` (the iteration ctx) is not done. `Restart`, `RestartFresh` and daemon shutdown all cancel that ctx before the kill, so the deliberate-exit decision is made before `drainRestart` ever runs. A clean exit has `waitErr == nil`.
- `Run`'s `claude exited` records gain `"session", id` on both the Warn and the Info form. When the tail is non-empty the Warn record also carries `"stderr", daemonLogOnly(tail)`. The shutdown return above the record is unchanged, so shutdown logs nothing new.
- `daemonLogOnly` is an unexported `string` type with a `LogDaemonOnly()` marker method and `MarshalText`, so slog's text handler renders it through its quoting path. The tail is only ever an attribute value: never the message, never a key.

### Ring exclusion (`internal/control/logs.go`)

- `teeHandler` replaces, in its ring copy only, any attribute whose resolved value implements `interface{ LogDaemonOnly() }` with the fixed string `(daemon log only)`. It does this for record attributes, for attributes added through `WithAttrs`, and inside group values. `next` (the primary handler) receives the record unchanged.
- The marker is a method-set contract rather than a shared type because `internal/control` imports `internal/sessions` and `internal/streamsup` must not grow that edge. The streamsup test runs through the real `control.SlogTee`, so a rename on either side reddens it.

## Concurrency model

One new goroutine per spawn on the production path: the stderr reader. It exits on EOF (every write end closed), or when `finish` closes the read end after `stderrDrainGrace`, which unblocks its `Read` (an `os.Pipe` file is poller-backed). `finish` always waits for it, so it never outlives `spawnAndWait`. `stderrTail` has its own leaf mutex; the reader writes and `finish` reads after the reader has exited. No runner lock is touched.

## Error handling

- `os.Pipe` failure: no tail, child spawned as before with stderr discarded. Logged at Debug.
- `cmd.Start` / StdinPipe failure: `started(false)` / no capture is attached yet; both pipe ends are closed; the existing error return is unchanged.
- A descendant holding stderr: the tail holds what arrived within the grace; respawn is delayed at most 250 ms.
- Copy errors (deadline/closed) are expected and ignored; the tail is a diagnostic.

## Testing strategy

- `stderr_tail_test.go`: table test of `stderrTail` — short input unchanged, trailing newline trimmed, more than 5 lines keeps the last 5, more than 1024 bytes keeps the end and stays within 1024, writes split across calls, empty input.
- `internal/control/logs_test.go`: a value with `LogDaemonOnly()` reaches the primary handler but the ring line shows `(daemon log only)` and none of its bytes, both as a record attr and through `With`, and nested in a group.
- `runner_test.go` (AC4): new helper mode `stderr_crash` writes ten distinct 300-byte lines to stderr and exits 1. The runner is wired the production way (`Stderr` nil) with `Logger` = `slog.New(control.SlogTee(textHandler(primary), ring))`. Asserts: the primary's `claude exited` line carries `session=<id>` and a quoted `stderr=` value that unquotes to a suffix of the written stderr, ≤ 1024 bytes, ≤ 5 lines, ending with the last line and not containing the first; the ring holds a `claude exited` line with the session id and no byte of any stderr line.
- `runner_test.go` (AC2): table over clean exit (mode writes stderr then exits 0), deliberate `Restart`, `RestartFresh`, and shutdown (mode writes stderr then blocks). None produces a `stderr=` attribute; the restart cases still log `claude exited` with the session id.

## Open questions

- None blocking. Whether the 250 ms grace is ever hit in production is unmeasured; the bound only matters when a descendant holds claude's stderr.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The child's stderr is untrusted text crossing into the daemon at `stderrCapture`'s reader. It is held only as a `daemonLogOnly` value, which is a distinct type downstream code can recognise; it reaches exactly one sink, the `stderr` attribute on the `claude exited` record in `Run`. No parsing, no decision is made from it.
- [Tokens, secrets, credentials] The tail can contain credentials. Exposure is limited to the daemon's own stderr (journald / launchd, readable by the operator account), which already receives the full argv in `spawning claude`. The ring, `pyry logs` and the debug bundle are excluded by `teeHandler`'s replacement. SHOULD FIX during build: the ring placeholder is a constant, so no length or prefix of the tail leaks either — assert in the control test that the ring line contains no byte sequence from the value.
- [File operations] No findings. No file is created; the pipe is anonymous.
- [Subprocesses] The child's argv and environment are unchanged. The stderr write end is closed in the parent right after `Start`, so the parent never holds it and EOF arrives when the child (and any descendant that inherited it) exits. The reader goroutine has two shutdown paths (EOF, close after the grace) and `finish` joins it.
- [Cryptography] Not applicable: the change mints, compares and stores no secret.
- [Network and I/O] Memory is capped at 1024 bytes per spawn regardless of how much the child writes; the reader discards everything older. The drain wait is capped at 250 ms. With `Config.Stderr` set, forwarding stays unbounded as before (test and probe callers only).
- [Errors, logs, telemetry] The tail is an attribute value, never the message or a key; `daemonLogOnly.MarshalText` routes it through slog's text quoting, so embedded newlines, `=` and quotes are escaped and cannot forge a record or a field. SHOULD FIX during build: the runner test asserts the value appears quoted and unquotes to the expected tail. Only the `claude exited` record carries it; no Debug or other record logs stderr.
- [Concurrency] `stderrTail` has a leaf mutex never held across a call-out; no runner lock is involved. The deliberate-exit check reads the iteration ctx inside `spawnAndWait`, before `Run`'s `cancel()`. A child crashing at the same instant a restart lands is classified deliberate and logs no tail — acceptable for a diagnostic.
- [Threat model] The mobile threat this addresses is disclosure through the debug bundle (#812): closed by the ring exclusion. OUT OF SCOPE: a general redactor for the ring and the bundle; none exists and this ticket adds nothing that needs one.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-03
