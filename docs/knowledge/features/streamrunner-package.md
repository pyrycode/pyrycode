# `internal/agentrun/streamrunner` — no-PTY stream-json subprocess primitive

Headless sibling of [`agentrun.Drive`](agentrun-package.md): spawns `claude` as a plain subprocess (no PTY), writes one stream-json user-turn envelope to its stdin, forwards stdout/stderr to caller-supplied writers, and maps the child's exit to the verb-level contract. Caller assembles the full claude argv (including `--input-format stream-json --output-format stream-json --dangerously-skip-permissions`); this primitive owns only the spawn, the stdin envelope, and the ctx-cancel teardown.

Introduced #390 as a leaf primitive. Caller wiring landed in #391: [`pyry agent-run`](pyry-agent-run-command.md) cut over from the PTY-drive path to eliminate the 2026-05-14 `/doctor` prompt-poisoning regression. **#470 cut the verb's default back to [`ptyrunner`](ptyrunner-package.md) to land on the explicitly subscription-eligible interactive surface ahead of Anthropic's 2026-06-15 billing-policy deadline; streamrunner stays as the operator-facing rollback knob, selected via `PYRY_USE_STREAMJSON=1`, and is preserved indefinitely for billing-classification comparison (operator decision 2026-05-19).**

## Public API

```go
type Config struct {
    ClaudeBin   string       // required; resolved path to claude
    WorkDir     string       // required; child cwd
    Args        []string     // full claude argv (excluding argv[0])
    PromptBytes []byte       // user-turn prompt text; UTF-8; JSON-encoded into the envelope
    Stdout      io.Writer    // required; forwarded child stdout. MUST NOT block.
    Stderr      io.Writer    // required; forwarded child stderr. MUST NOT block.
    Env         []string     // optional; appended to os.Environ() in the child
    Logger      *slog.Logger // optional; defaults to slog.Default()
}

// Run spawns claude with cfg.Args, writes one stream-json user-turn envelope
// to its stdin and closes stdin, forwards stdout/stderr, and waits for the
// child to exit.
//
// Returns:
//   - nil on clean (exit 0) child termination.
//   - nil on ctx-cancel-driven teardown — operator shutdown is success.
//   - *exec.ExitError on non-zero child exit not triggered by ctx cancel.
//   - a wrapped error from pre-Start setup (stdin pipe, spawn).
func Run(ctx context.Context, cfg Config) error
```

No other exported identifiers. The envelope is constructed from unexported `userTurn` / `userTurnMessage` / `userTurnContentText` structs.

## Envelope shape

A single newline-terminated JSON line matching the 2026-05-14 probe:

```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<prompt>"}]}}
```

`PromptBytes` is JSON-encoded via `encoding/json` — **not** shell-escaped — so embedded double-quotes, backslashes, newlines, and control characters round-trip cleanly. After the marshalled bytes, `Run` writes `'\n'` and closes stdin. The write is synchronous in the calling goroutine (single ~150-byte payload); failures are logged and not returned. Reason: the child may have already exited and its exit code is the authoritative outcome. Same logged-and-continued pattern as `drive.go`'s PTY writes.

The `stdin.Close()` error log is filtered through [`agentrun.ExitErrIsBenign`](agentrun-package.md) (#527): a benign teardown shape (`EPIPE` / `os.ErrClosed` when the child exited early) logs at Debug as `"streamrunner: stdin close: child already exited"`; a genuine failure logs at Warn as `"streamrunner: stdin close failed"`. The sibling `stdin.Write` failure is **not** filtered — it stays a Warn unconditionally (`"streamrunner: stdin write failed"`) because a mid-write failure is genuinely unexpected, not a teardown response.

## ctx-cancel teardown

```go
cmd.Cancel = func() error {
    reapDescendantGroupsFn(cmd.Process.Pid, logger)
    return cmd.Process.Signal(syscall.SIGTERM)
}
cmd.WaitDelay = killGrace // 5 * time.Second
```

`cmd.Cancel` fires when `childCtx` is done — either the operator ctx (SIGTERM/SIGINT) or the
idle-stall watchdog's `cancelChild` (`childCtx` wraps the operator `ctx` via
`exec.CommandContext(childCtx, ...)`; see `watchdog.go`) — so one hook covers both teardowns. Stdlib's
ctx-watcher invokes `cmd.Cancel` (reap, then SIGTERM) → child exits or `killGrace` elapses → stdlib
SIGKILLs + closes pipes → `cmd.Wait()` returns → `Run` returns nil. No package-owned goroutine, timer,
or state machine beyond the watchdog's own. `killGrace` is hardcoded per AC ("no timing knobs") and
mirrors `budget.GracePeriod` for symmetry. See [Teardown reap: descendant process groups](#teardown-reap-descendant-process-groups-reapgo-924) below for what the reap does and why it's safe to fire unconditionally on both teardown paths.

Return mapping at exit:

```go
waitErr := cmd.Wait()
if ctx.Err() != nil { return nil }   // operator teardown is success
return waitErr                       // nil on exit 0; *exec.ExitError otherwise
```

## Idle-stall watchdog: synthetic trailer newline guard (#1497)

`watchdog.go`'s `streamParser` wraps `cfg.Stdout`: `Write` forwards every byte from claude verbatim *before* parsing, then feeds the forwarded slice to `feed`, which tracks `awaiting`/`lastEvent`/`sawResult` off complete newline-delimited lines. When the poll loop judges the stream wedged (silent past the idle threshold while `awaiting`), it fires `cancelChild` — the source of the "watchdog" leg of § ctx-cancel teardown above — and `Run` synthesises a `result` trailer via `writeIdleStallResult`. That trailer is the only signal this path produces: `Run` returns nil by contract, and the dispatcher retries `terminal_reason: idle_stall` as transient.

If claude's stream ended mid-line, the unterminated bytes are already on `cfg.Stdout` by the time the trailer is composed, so the trailer needs a leading `'\n'` to start its own line rather than splicing onto the partial and becoming unparseable — and a stream that ended newline-terminated must *not* get one, or the dispatcher sees a stray blank line. `streamParser` decides this with a `lineOpen` field, set in `feed` from the last byte of the slice that actually reached `dst` (`b[:n]`, never the caller's full `b`), and read via a `hasSeenResult`-shaped accessor (`hasOpenLine`) threaded into `writeIdleStallResult` as its `leadNewline` argument. This is deliberately **not** `len(buf) > 0`: `feed` drops the partial-line accumulator to `nil` once it grows past `maxBuf`, *after* those bytes have already reached stdout — on an oversized partial, `buf` reports the line closed at exactly the moment it is still open. `lineOpen` and `buf` are different properties of the parser for this reason and must stay that way.

Testing note: the guard's off-case (no partial in flight) is pinned at the parser level — a fresh `streamParser.hasOpenLine()` is `false` — not by any `Run`-level test. `TestRun_IdleStall_NoEvents` asserts with `strings.Contains` and would pass even with a stray leading newline; don't read it as covering that property.

`internal/streamsup` lifted this watchdog's type-aware core in #1094 (see [streamsup-package.md](streamsup-package.md), § "Idle/stall watchdog") but that watchdog *emits* on idle rather than killing, so it was out of scope for this ticket's measurement; whether its own trailer path has an analogous splice hazard is unmeasured.

## Teardown reap: descendant process groups (`reap.go`, #924)

claude isolates every Bash tool command into its own detached process group two levels below pyry
(`pyry → claude → zsh -c eval '<cmd>' → <cmd>`) and does not reap that group itself, even on a
graceful SIGTERM — #565 measured this 3/3 on the interactive PTY path, and it carries by
construction to headless `claude -p` (the detachment is claude's own `setpgid` isolation of each tool
command, independent of which runner signals it). Left alone, a dispatcher wall-clock SIGTERM landing
mid-tool orphans the group to init — unbounded for a command that never returns.

`internal/agentrun/streamrunner/reap.go` defines the consumer-side seam:

```go
var reapDescendantGroupsFn = agentrun.ReapDescendantGroups
```

`cmd.Cancel` calls it with `cmd.Process.Pid` (claude's pid, alive and un-signalled at fire time — the
same race-free invariant ptyrunner relies on) immediately before the existing SIGTERM. Byte-for-byte
mirror of [ptyrunner's seam](ptyrunner-package.md#teardown-reap-descendant-process-groups-reapgo-565-864-923),
minus `killGrace` (streamrunner already defines its own SIGTERM-grace const locally). The reaper
implementation, its enumeration walk, and its three load-bearing guards
(`pgid<=1` / pyry's own group / claude's own group) live in the shared
[`internal/agentrun.ReapDescendantGroups`](agentrun-package.md) (lifted from `ptyrunner` in #923) —
this package only wires the call site, it does not re-implement the walk.

One hook, no `ctx.Err()` discriminator: `cmd.Cancel` fires from **both** the operator ctx
(SIGTERM/SIGINT) and the idle-stall watchdog's `cancelChild` (§ ctx-cancel teardown above), and the
reap is safe unconditionally on either — the watchdog only fires while claude owes an assistant turn
(no tool in flight), so on that path the descendant walk finds nothing and the reap is a harmless
no-op. Never fires on a clean exit: `cancelChild` there runs only after `cmd.Wait()` returns, by which
point os/exec has already stopped its ctx watcher.

Test: `TestRun_CtxCancel_ReapsDescendantGroups` swaps `reapDescendantGroupsFn` to a mutex-guarded
`reapRecorder` (non-parallel — mutates the package var, restored via `t.Cleanup`) and asserts it fired
with a plausible live pid during the existing ctx-cancel-mid-run scenario. One test covers both
teardown paths because both route through the identical `cmd.Cancel` closure; the watchdog path
independently proving it cancels `childCtx` is covered by the existing watchdog tests.

See [`codebase/924.md`](../codebase/924.md) (this ticket), [`codebase/565.md`](../codebase/565.md)
(the original ptyrunner reap and the 3/3 measurement), and
[`codebase/923.md`](../codebase/923.md) (the lift into `internal/agentrun`).

## Logging discipline

The package logs **only**:

- Warn on stdin write failure (`"streamrunner: stdin write failed"`, error message only).
- Warn on stdin close failure (`"streamrunner: stdin close failed"`, error message only) — **only** when [`agentrun.ExitErrIsBenign(err)`](agentrun-package.md) is false. Benign close shape (`EPIPE` / `os.ErrClosed` after child early-exit) logs at Debug as `"streamrunner: stdin close: child already exited"` (#527).

It does **not** log:

- `cfg.PromptBytes` or any substring.
- The marshalled envelope bytes.
- Anything from `cfg.Stdout` / `cfg.Stderr` (opaque `io.Writer` values — the package never sees event payload content in a parseable form).

Pinned in the package doc-comment.

## Dependency direction

Stdlib (`context`, `encoding/json`, `errors`, `fmt`, `io`, `log/slog`, `os`, `os/exec`, `syscall`, `time`) plus the shared parent `internal/agentrun` — imported for `ExitErrIsBenign` (#527) and, as of #924, `ReapDescendantGroups` (via the local `reapDescendantGroupsFn` seam in `reap.go`). Crucially, the package does **not** import `internal/supervisor` (the PTY helper) nor any **sibling** `internal/agentrun/*` subpackage (`ptyrunner`, `budget`, `jsonl`, `trust`, `settings`) — the parent-only import keeps it a leaf primitive relative to its siblings, which is exactly why #923 lifted the reaper up to the parent instead of leaving it inside `ptyrunner`. Verifiable by:

```bash
go list -deps ./internal/agentrun/streamrunner/... | grep pyrycode/internal/supervisor
```

Expected output: empty.

## Why a separate primitive

- `agentrun.Drive` (PTY-driven, interactive bridge mode) and `streamrunner.Run` (subprocess, headless stream-json mode) are different mechanisms for different claude invocation shapes. The 2026-05-14 probe confirmed that `claude --input-format stream-json --output-format stream-json --dangerously-skip-permissions` runs cleanly without a PTY, without a trust dialog, and without a JSONL tail watcher — stdin in, event stream out, exit code at the end. None of `Drive`'s PTY plumbing, defensive trust-dialog dismissal, or background-drain goroutine is needed.
- Keeping the two side-by-side (rather than overloading `Drive` with a no-PTY mode flag) preserves a clean test surface and a clean failure mode — readers of either function don't have to reason about both paths.
- The existing `internal/agentrun/streamjson` package is upstream of this one's eventual caller: it parses JSONL `Event`s and re-emits stream-json. `streamrunner` owns the subprocess + stdio pipe; `streamjson` owns the event-shape transformation. The two never compose inside this package.

## Testing

Same-package `_test.go` with a `TestStreamRunnerHelperProcess` fake claude (re-exec via `os.Args[0]` + `-test.run`). Modes keyed by `GO_STREAMRUNNER_HELPER_MODE`:

- `clean` — read stdin to EOF, write three deterministic stream-json lines (`system init` / `assistant text` / `result success`), exit 0.
- `exit1` — drain stdin briefly, exit 1.
- `sleep` — install SIGTERM handler that prints `"got SIGTERM"` to stderr and exits 0 within ~50ms; otherwise sleep 30s.
- `echo_stdin` — copy stdin to `GO_STREAMRUNNER_HELPER_STDIN_FILE` (mode 0o600), exit 0.

Four test cases against the four observable behaviours: clean exit (stdout substring check), non-zero exit (`errors.As(&exitErr)` + `ExitCode() == 1`), ctx-cancel mid-run (`Run` returns nil, elapsed < 6s, `"got SIGTERM"` on stderr — sanity-checks SIGTERM not SIGKILL was the trigger), and stdin envelope round-trip with a deliberately tricky prompt (embedded `"`, `\n`, `\\`, `\x01`) → assert the helper's captured file unmarshalls into the expected `userTurn` shape with `Content[0].Text` byte-for-byte equal to the input. Plus (#924) `TestRun_CtxCancel_ReapsDescendantGroups`, cloning the ctx-cancel-mid-run `sleep`-mode harness with the `reapDescendantGroupsFn` seam swapped to a recorder — see [Teardown reap](#teardown-reap-descendant-process-groups-reapgo-924) above.

`helper_test.go` additionally carries a family of `stall_*` modes exercising the idle-stall watchdog (§ above) in `watchdog_test.go`, including (#1497) a mode that writes one unterminated line and then blocks until SIGTERM, for the newline-guard tests.

## Consumers

- [`pyry agent-run`](pyry-agent-run-command.md) (#391) — assembles the full claude argv (`--input-format stream-json --output-format stream-json --verbose --dangerously-skip-permissions --allowed-tools … --model … --effort … --max-turns … --append-system-prompt-file …`), passes the prompt bytes through `Config.PromptBytes`, threads the verb's stdout (forwarded byte-for-byte to the dispatcher) and `os.Stderr`, and maps the runner's return (`nil` / `context.Canceled` / `*exec.ExitError`) to the verb's exit-code contract. The PTY-drive sibling `agentrun.Drive` is no longer invoked from this verb's runtime path, but stays compiled because `cmd/pyry/agent_run_selfcheck.go` (#336) still depends on the surrounding `agentrun` package for `WriteSettings` / `MarkWorkdirTrusted`.

## Out of scope

- Parsing stream-json events on the way through — that's [`streamjson`](streamjson-package.md)'s concern, and this primitive's writers are opaque.
- Multi-turn drives — the AC pins single user-turn-then-close-stdin. Defer multi-turn until a consumer needs it.
- Operator-tunable timing knobs (trust-dialog delay, prompt delay, grace window) — none apply; stream-json mode has no trust dialog and no TUI write timing, and the SIGTERM grace is fixed.

## Related

- [agentrun-package.md](agentrun-package.md) — the PTY-driven sibling (`Drive`) this primitive parallels; shares the "ctx-cancel is success" return contract, the "log-and-continue on stdin write failure" pattern, and (post-#924) the `ReapDescendantGroups` consumer relationship.
- [pyry-agent-run-command.md](pyry-agent-run-command.md) — the verb that consumes this primitive (cut over from `Drive` in #391).
- [streamjson-package.md](streamjson-package.md) — the pre-#391 event-stream emitter; no longer composed with this primitive (claude itself emits the canonical stream-json events on its own stdout under stream-json mode). Package stays in tree pending the cleanup ticket.
- [ptyrunner-package.md](ptyrunner-package.md#teardown-reap-descendant-process-groups-reapgo-565-864-923) — the parity reference for the #924 teardown reap; same seam shape, same guards, same "reap before signal" ordering.
- [`codebase/924.md`](../codebase/924.md) — this ticket: wiring the descendant-group reap into the streamrunner teardown.
- Spec [`docs/specs/architecture/390-streamrunner-primitive.md`](../../specs/architecture/390-streamrunner-primitive.md) — the build-time architect spec for this package. Spec [`docs/specs/architecture/924-streamrunner-reap-descendant-groups.md`](../../specs/architecture/924-streamrunner-reap-descendant-groups.md) — the #924 reap wiring. Spec [`docs/specs/architecture/1497-idle-stall-trailer-newline-guard.md`](../../specs/architecture/1497-idle-stall-trailer-newline-guard.md) — the #1497 newline-guard design.
