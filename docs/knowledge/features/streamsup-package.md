# `internal/streamsup` — persistent stream-json child lifecycle

Stream-json sibling of [`internal/supervisor`](../architecture/system-overview.md) (the PTY path): supervises a **long-lived, multi-turn** headless `claude` child instead of hosting a screen. Where [`streamrunner`](streamrunner-package.md) spawns claude for one turn, writes the envelope, closes stdin, and exits, `streamsup` spawns claude **once per crash cycle**, holds its stdin open across many turns, and restarts it with the supervisor's backoff ladder on crash. Process lifecycle (#1087), the turn I/O boundary — envelope write + stdout→turnevent parser (#1088) —, the send-side turncommit gate (#1093), the receive-side idle/stall watchdog (#1094), and satisfying the `sessions.Runner` seam (`State`/`WriteUserTurn`/`WaitForPTY`/`Restart` + a live-restart seam, #1097) have shipped. It still ships with no *production* consumer — `send_message`/`set_session_settings` work for free once a `streamRunner` is constructed, but the factory arm that builds one from a `supervisor.Config` is #1081's scope, not yet landed.

**No transcript tailing lives in this package.** That is the entire point of the stream-json path: it structurally removes the `<uuid>.jsonl` bind-latency race the PTY path fought (#528/#996/#989). The only filesystem canonicalisation `streamsup` performs is resolving `WorkDir` via `agentrun.ResolveWorkdir` before spawn (macOS `/tmp` → `/private/tmp`, the #989 symlink hazard) — no fsnotify, no JSONL-path resolution anywhere; the #1088 parser reinforces this by opening/watching/resolving no path at all.

## Public API

```go
type Config struct {
    ClaudeBin      string        // required; resolved path to claude
    WorkDir        string        // required; resolved via agentrun.ResolveWorkdir in New
    SessionID      string        // required; caller-minted claude session UUID
    Args           []string      // pass-through argv (e.g. --model <m>); New clones it
    Stdout         io.Writer     // optional; nil → child stdout discarded (/dev/null)
    Stderr         io.Writer     // optional; nil → discarded
    Env            []string      // optional; appended to os.Environ() in the child
    Logger         *slog.Logger  // optional; nil → slog.Default()
    BackoffInitial time.Duration // zero → 500ms
    BackoffMax     time.Duration // zero → 30s
    BackoffReset   time.Duration // zero → 60s
}

func New(cfg Config) (*Runner, error)
func (r *Runner) Run(ctx context.Context) error // blocks until ctx cancel; supervise loop
func (r *Runner) Stdin() io.Writer               // held-open stdin, or nil between spawns

// sessions.Runner seam (#1097) — see "Satisfying sessions.Runner" below
func (r *Runner) State() State
func (r *Runner) WriteUserTurn(ctx context.Context, conversationID string, payload []byte) error
func (r *Runner) WaitForPTY(ctx context.Context) error
func (r *Runner) Restart(args []string)
```

`New` validates `ClaudeBin`/`WorkDir`/`SessionID` non-empty, `exec.LookPath`s the binary, resolves `WorkDir` (a missing dir → wrapped `fs.ErrNotExist`), and applies backoff defaults. `Stdin()` returns `io.Writer`, not `io.WriteCloser` — deliberately, so a consumer (the #1088 turn writer) cannot close a handle the runner owns; it returns nil whenever no child is currently live (before first spawn, mid-restart, during teardown).

## `buildArgs` — the id-flag inversion that keeps the on-disk session stable

```go
func buildArgs(base []string, firstRun bool, sessionID string) []string
```

Pure function, assembled fresh each spawn (never mutates `base`):

1. Fixed stream-json prefix: `--input-format stream-json --output-format stream-json --verbose`. **Never `-p`/`--print`** — the non-`-p` choice is billing-classification-tied and was spike-verified live (#1075): multi-turn, interrupt, resume, and the approval round-trip all work without it.
2. Then the caller's `base` (`Config.Args`, e.g. `--model <m>`).
3. Then the id flag: **first spawn** → `--session-id <sessionID>` (establishes the on-disk transcript under a known id); **every respawn** → `--resume <sessionID>` (reattach, append, **no fork** — `--fork-session` is the explicit, unused opt-in).

Passing the *same* `sessionID` to both flags is why the on-disk session id survives a kill-and-restart untouched — pool id bookkeeping (eviction/reactivation) never has to reconcile a forked id. Mirrors the daemon bootstrap's deterministic-`--session-id` precedent (#839).

## Held-open stdin — the deliberate inversion from `streamrunner`

`streamrunner.Run` (#390/#391) closes the child's stdin after writing one turn envelope — it's a single-turn primitive. `streamsup` is the opposite: `cmd.StdinPipe()` is opened before `cmd.Start()`, the write end is stored under a leaf mutex, and **it is never closed while the child is alive**. Turn envelopes are written onto it by the #1088 follow-on slice; this slice only owns the handle's lifecycle:

- On spawn: `setStdin(stdin)` publishes the new handle, then the unexported `onSpawn(pid)` test seam fires (nil in production).
- On exit (crash or ctx-cancel teardown): `takeStdin()` clears the field first — so `Stdin()` reports "no live child" immediately — then closes the old handle **outside** the lock. `cmd.Wait()` has usually already closed the parent write end, so a broken-pipe / already-closed error here is expected; it's filtered through [`agentrun.ExitErrIsBenign`](agentrun-package.md) to avoid a spurious Warn (same discipline as `streamrunner`).

## Supervise loop (`Run`)

Mirrors `supervisor.Run`'s restart/backoff/resume shape, including the live-restart (`restartCh`/`iterCtx`) seam since #1097 (see "Satisfying `sessions.Runner`" below):

```
loop:
  if ctx.Err() != nil → return ctx.Err()               // graceful shutdown, not a crash
  args := buildArgs(liveArgs(), firstRun, SessionID)    // liveArgs() picks up a Restart-swapped argv
  iterCtx, cancel := context.WithCancel(ctx); setIterCancel(cancel)
  started, waitErr := spawnAndWait(iterCtx, args)       // blocks until child exits or Restart cancels iterCtx
  cancel(); setIterCancel(nil)
  if ctx.Err() != nil → return ctx.Err()                // parent-ctx cancel = teardown, not a crash
  if started → firstRun = false                         // see the firstRun gate below
  if drainRestart() → continue                          // deliberate restart, not a crash: skip backoff
  delay := backoff.next(uptime)
  select { <-time.After(delay) | <-ctx.Done() → return ctx.Err() | <-restartCh → relaunch now }
```

Shutdown is detected via **parent-ctx cancellation**, never via the child-exit error value — `spawnAndWait`'s `waitErr` only ever means "crashed" once `ctx.Err()` has been checked and is nil. An `iterCtx`-only cancel (from `Restart`) leaves the parent `ctx.Err()` nil, so the loop falls through and relaunches instead of returning. One goroutine total (the caller's `Run`); `cmd.Wait` blocks it, and os/exec runs its own internal ctx-watcher goroutine that invokes `cmd.Cancel` off-loop — on either a parent-ctx cancel (shutdown) or an `iterCtx` cancel (restart).

### `firstRun` gate: only advances on a successful `cmd.Start` (fix 66cc50e)

`spawnAndWait` returns `(started bool, waitErr error)`. `started` is `false` only when the spawn fails during **setup** (`cmd.StdinPipe()` or `cmd.Start()` erroring) — claude never launched, so `--session-id` never ran and the on-disk session was never established. The original implementation flipped `firstRun = false` unconditionally after every iteration; a transient setup failure (e.g. a momentarily-unavailable binary) would make the *next* attempt respawn with `--resume <id>` against a session that was never created, and claude would error ("no conversation found") on every subsequent attempt — a permanent, unrecoverable crash-loop that defeated the very retry the backoff loop exists for. Fixed by threading `started` through and gating the flip: `if started { firstRun = false }`. A setup failure now correctly retries with `--session-id` until one succeeds. Regression test drives `Run` against a non-existent binary and asserts every retry keeps `--session-id`.

## Teardown: SIGTERM → SIGKILL grace + descendant-group reap

Same shape as [`streamrunner`](streamrunner-package.md#teardown-reap-descendant-process-groups-reapgo-924), copied verbatim down to the seam name:

```go
cmd.Cancel = func() error {
    reapDescendantGroupsFn(cmd.Process.Pid, r.log)
    return cmd.Process.Signal(syscall.SIGTERM)
}
cmd.WaitDelay = killGrace // 5 * time.Second
```

`cmd.Cancel` fires only on ctx cancellation (stdlib's ctx-watcher), never on a spontaneous crash — matching the proven streamrunner/ptyrunner behaviour; there is no evidence claude orphans descendant Bash process groups on its own exit, so no speculative crash-path reaping was added (Evidence-Based Fix Selection). `reapDescendantGroupsFn` is a package-var seam defaulting to [`agentrun.ReapDescendantGroups`](agentrun-package.md), swapped in tests via a mutex-guarded recorder.

## Dependency direction (AC1)

Imports only stdlib and the shared parent `internal/agentrun` (`ResolveWorkdir`, `ExitErrIsBenign`, `ReapDescendantGroups`). Must not import `internal/supervisor` (the PTY helper) nor any sibling `agentrun` subpackage (`streamrunner`, `ptyrunner`, …). The `backoffTimer` is **copied verbatim** into `backoff.go` rather than imported from `internal/supervisor`, specifically to preserve this boundary — the two are expected to stay byte-identical; the lifted `backoff_test.go` ladder table guards both independently. Verify with:

```bash
go list -deps ./internal/streamsup/... | grep pyrycode/internal/supervisor   # expect: empty
```

## Testing

Table-driven stdlib `testing`, `go test -race`. Fake-child harness dispatches from `TestMain` on `GO_STREAMSUP_HELPER=1` **before `flag.Parse`** — not streamrunner's `os.Args[0]` + `-test.run` re-exec trick, because `buildArgs` prepends the fixed stream-json flags *ahead of* the caller's args, so a `-test.run` flag can never be made to sort first; `go test` would exit 2 on the unknown leading flag before the helper ever ran. Dispatching from `TestMain` on an env var sidesteps flag parsing entirely. Modes keyed by `GO_STREAMSUP_HELPER_MODE`: `echo_lines` (proves stdin stays open — echoes each line, only emits `GOT_EOF` if EOF is actually reached), `block_sigterm` (teardown grace test), `crash` (forces respawns; optionally records its own argv to `GO_STREAMSUP_HELPER_ARGV_FILE` for the resume-id-stability assertion).

Scenarios: `buildArgs` shape (pure, table — fixed prefix present, `-p` absent, `--session-id` vs `--resume`, id byte-identical across first-spawn/respawn, `base` order preserved and not mutated); held-open stdin (echo round-trip + `GOT_EOF` absent while alive); backoff ladder (lifted `supervisor.backoff_test.go` verbatim against the copied `backoffTimer`); restart-on-crash (≥2 spawns observed via `onSpawn`); resume-id-stable-across-restart (captured argv: spawn 1 has `--session-id <id>`, spawn 2 has `--resume <id>`, same id); teardown SIGTERM+grace (`Run` returns within `< killGrace`, "got SIGTERM" on stderr); teardown reaps descendant groups (`reapDescendantGroupsFn` swap, non-parallel); the `firstRun`-gate regression test (non-existent binary, every retry keeps `--session-id`).

## Turn I/O — envelope write + stdout parser (#1088)

The turn I/O boundary fills `Stdin()`/`Config.Stdout` with two additive seams — no `runner.go` diff.

```go
var ErrNoLiveChild = errors.New("streamsup: no live child")

func WriteTurn(ctx context.Context, w io.Writer, prompt []byte) error

type Parser struct { /* sink, byte buffer, maxBuf, logger */ }
func NewParser(sink func(turnevent.Event), logger *slog.Logger) *Parser
func (p *Parser) Write(b []byte) (int, error) // io.Writer; set as Config.Stdout
```

**Send half — `WriteTurn`.** Mirrors `streamrunner`'s `userTurn`/`userTurnMessage`/
`userTurnContentText` envelope shape verbatim
(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"…"}]}}`). The prompt is
carried as a JSON string value and `json.Marshal`-escaped, so every embedded metacharacter — critically
every newline — is escaped: the marshalled envelope is always exactly one physical line, and the
trailing `'\n'` `WriteTurn` appends is the only raw newline. This is the injection-resistance property
the ticket called out: a prompt from an untrusted party (mobile client, over the relay) cannot forge a
second stream-json control line (a fake `result`, a `control_request` interrupt, or a permission
approval) on claude's stdin — enforced by structured encoding, not string concatenation, and pinned by
a table-driven test asserting exactly-one-newline + byte-exact round-trip across forged-`result`,
`\r\n`, and control-byte payloads. `WriteTurn(nil, …)` (the shape `Stdin()` returns between spawns)
returns `ErrNoLiveChild` and writes nothing; a write failure (e.g. `EPIPE` mid-teardown) is wrapped and
returned, never panics. `w`'s `io.Writer` type makes a half-close/EOF forgery structurally impossible.
The caller writes turn N+1 by calling `WriteTurn` again on the *same* `Stdin()` handle — no re-open, no
per-turn stdin lifecycle.

**Turncommit gate on send (#1093).** `WriteTurn` claims the [`internal/turncommit`](../../internal/turncommit)
gate carried on `ctx`, mirroring `supervisor.deliverViaSession` on the PTY path: after the `w == nil`
check, before `marshalTurnEnvelope`. A false claim — the queued head was dropped during the wait for
claude to go ready — returns `turncommit.ErrDropped` bare (unwrapped, so the queue can key drop-handling
on `errors.Is`) and writes **zero bytes**; a nil gate (the non-queue paths, e.g. a direct single-turn
send) delivers unconditionally. The `w == nil` check stays first and consumes no claim, so a
no-live-child send keeps the retryable `ErrNoLiveChild` outcome rather than permanently burning the
claim as a false drop. See [codebase/1093.md](../codebase/1093.md).

**Receive half — `Parser`.** An `io.Writer` wired as `Config.Stdout`. Buffers bytes, splits on `'\n'`
(mirroring `streamrunner/watchdog.go`'s `streamParser.feed` mechanics, but as the terminal sink, not a
tee — `Write` always reports `(len(b), nil)`), drops an oversized unterminated partial past `maxBuf`
(4 MiB). Each complete line is decoded into a minimal local shape and switched on the line's **top-level
`type` only** — nested content (assistant text, tool-result content) is opaque data and is never
re-scanned for control types, so a tool result whose text literally contains `{"type":"result"}` cannot
forge a turn boundary:

| Line `type` | Emits |
|---|---|
| `assistant` | one event per content block, in order: `text`→`TextChunk`, `thinking`→`ThoughtChunk`, `tool_use`→`ToolStart` |
| `user` | one `ToolUpdate` per `tool_result` block (status from `is_error`, content from the string/array union) |
| `result` | exactly one `TurnEnd{Reason: TurnEndReasonEndTurn}` — **the turn boundary** |
| `system` (`init`/`thinking_tokens`/`status`), `rate_limit_event`, unknown/malformed | nothing (Debug-logged by type/reason only, never content) |

Mapping logic mirrors (not imports — `mapper.go`'s helpers are unexported and keyed on tui-driver types)
[`turnbridge/mapper.go`](turnbridge-package.md). Two deliberate divergences: (1) a stream-json
`assistant` event carries a *whole message* that may hold several content blocks (`mapper.go` sees one
block per JSONL line), so the parser iterates `message.content` and emits one event per block,
preserving order; (2) `tool_use` input is carried through as claude's already-decoded
`json.RawMessage` verbatim (`ToolStart.RawInput`) rather than `mapper.go`'s map-re-marshal — one fewer
parse, and it preserves the original key order for what is an opaque pass-through field.

**Turn-stateless by design.** The parser holds no turn counter, no `awaiting` flag, no per-session
accumulator — only the partial-line byte buffer. The *only* turn boundary is a `result` line, and no
other line type can create, reset, or leak state across one — this is what makes zero cross-turn bleed
structural rather than tracked, proven by a headline round-trip test driving 3 turns with distinct
per-turn markers over one held-open `Stdin()` handle. Per the T1 spike (#1075): `system/init` fires
**once per turn**, not once per session (dropping it is correct precisely because there's no session
state for a mistaken init to reset), and the session id is constant across turns of one process.

**Concurrency — no mutex.** `os/exec` drives a non-`*os.File` `Config.Stdout` through exactly one
internal `io.Copy` goroutine, so `Parser.Write` is only ever invoked serially from that goroutine.
Unlike `streamrunner`'s `streamParser` (which locks because a separate watchdog goroutine reads its
state), this parser has no second reader. `sink` runs on that same forwarder goroutine; the consumer
owns any synchronization it needs beyond "called serially, in stream order." This slice spawns no
goroutines of its own.

**No transcript tailing on this path (AC4).** `envelope.go`/`parser.go` open, watch, or resolve no
filesystem path — the parser's only input is the bytes handed to `Write`. Reinforced by the same
`go list -deps` import-boundary invariant #1087 pins (no fsnotify, no `internal/supervisor`).

Deferred to #1089 (needs the interrupt/gate context): richer `TurnEnd` reason classification
(`max_tokens`/`refusal`, and mapping the interrupt `result` subtype `error_during_execution` →
`TurnEndReasonCancelled`); authoring/routing the `control_request` interrupt control line on the send
path; whether the parser's line buffer needs resetting across a crash-restart (see
[codebase/1088.md](../codebase/1088.md) — code review flagged a stale-partial edge case, non-blocking
for this unwired slice).

## Idle/stall watchdog — receive-side, emit-not-kill (#1094)

Lifts the type-aware idle watchdog from `streamrunner` (`internal/agentrun/streamrunner/watchdog.go`)
with one crucial divergence: the one-shot runner **kills** on idle stall; `streamsup`'s watchdog **emits
and never kills**. The T1 spike (#1075, claude 2.1.199) measured a pending permission approval blocking
claude synchronously until the approval tool answers — minutes of *owed* silence with `awaiting` true the
whole time. A watchdog that killed on `awaiting && idle` would destroy a legitimately-blocked-on-approval
turn.

```go
type WatchdogConfig struct {
    Idle              time.Duration               // 0 → 240s
    PendingPermission func() bool                 // the content-free timing hook; nil → always-false
    OnStall           func(pendingPermission bool) // the stall signal; nil → no-op
    Logger            *slog.Logger                 // nil → slog.Default
}

func NewWatchdog(cfg WatchdogConfig) *Watchdog
func (w *Watchdog) Writer() io.Writer          // the tracker; compose into Config.Stdout
func (w *Watchdog) Start(ctx context.Context)  // launches the one poll goroutine
func (w *Watchdog) Wait()                       // blocks until the poll goroutine exits
```

**Two pieces, additive, zero `runner.go`/`parser.go` diff.** An unexported `stallTracker` (`io.Writer`)
carries its own type-tracking state over the same stdout stream the #1088 `Parser` already reads —
deliberately, since the `Parser` is turn-stateless by design and holds no `awaiting` flag. The caller fans
stdout to both with `io.MultiWriter(parser, wd.Writer())` (deferred to the wiring slice). The tracker
reads only each line's top-level `type` — never event content (AC3) — to track whether claude *owes an
assistant turn*: `assistant`→not-awaiting (a tool run's silence that follows is expected and never trips
the watchdog — the type-aware core), `user`/`tool_result`→awaiting, `result`→not-awaiting. A single poll
goroutine (`Start`/`Wait`) ticks at `watchdogTickFor(Idle)` (`idle/8` clamped to `[5ms, 5s]`, lifted
verbatim) and, on the edge of `awaiting && silent > Idle` (latched once per stall episode), evaluates
`PendingPermission()` and calls `OnStall(pending)`.

**The hook annotates the signal, it does not gate it.** Both a genuine wedge (`pending=false`) and an
approval-wait (`pending=true`) call `OnStall` — the distinction lives in the argument, not in whether the
callback fires; gating the emit off while pending was considered and rejected (the ticket says the
watchdog *emits* while a permission is pending, it doesn't stay silent).

**Emit-not-kill is enforced structurally.** `Watchdog` holds no `context.CancelFunc` and no process
handle — `WatchdogConfig` has no field to wire one in — so a future edit cannot reintroduce a kill without
changing the type's shape. The one-shot runner's KILL machinery (`idleStallResult`, `idleStallUsage`,
`writeIdleStallResult`, the synthetic `result` trailer, `sawResult`) was deliberately not lifted. See
[codebase/1094.md](../codebase/1094.md) for the full design writeup and code-review notes.

Deferred: the pending-permission signal's **producer** (the approval flow — mcp-approve stdio tool,
control-socket verb, spawn-arg injection) lands in #1079/#1080 with its own security review; this hook is
a local, content-free `func() bool` only, which is why this slice is not `security-sensitive`.

## Satisfying `sessions.Runner` (#1097)

`*streamsup.Runner` gained the four methods [`internal/sessions.Runner`](sessions-package.md) requires
beyond `Run`/`Stdin`, so a stream-json session can be driven through the exact seam `*supervisor.Supervisor`
already satisfies (`internal/sessions/runner.go`, introduced by #1077). `send_message` (→
`Session.WriteUserTurn` → `sup.WriteUserTurn`) and `set_session_settings` (→ `Pool.UpdateSettings` →
`sup.Restart`) work unchanged the moment a `streamRunner` exists — no new dispatch wiring, because both
paths already call through the interface rather than the concrete type.

**The covariant snag.** `sessions.Runner.State()` returns `supervisor.State`, but this package's dependency
direction (above) forbids importing `internal/supervisor`. Go has no covariant return on interface
satisfaction, so `*streamsup.Runner` cannot declare that signature directly. Resolved at the seam, not by
relaxing the interface: `*streamsup.Runner` gets a **native** `State() streamsup.State` (new `state.go`,
mirroring `supervisor.State`/`Phase` field-for-field and string-for-string), and a thin adapter —
`cmd/pyry/streamsup_runner.go`'s `streamRunner{ r *streamsup.Runner }` — maps `streamsup.State →
supervisor.State` via `mapStreamState` (`Phase` converts by a plain string cast, the six other fields copy
through) and forwards `WriteUserTurn`/`WaitForPTY`/`Run`/`Restart` unchanged. `streamRunner`, not the
concrete `*streamsup.Runner`, is what satisfies `sessions.Runner`; `var _ sessions.Runner = streamRunner{}`
is the compile-time proof. Same shape as `poolResolver` (`cmd/pyry/main.go`) and the pattern documented in
`docs/lessons.md` § "Interface adapters for covariant returns". The factory arm that *constructs* a
`streamRunner` from a `supervisor.Config` is #1081's scope — this ticket delivers only the adapter and the
assertion.

**`State`/`Phase` (`state.go`).** `Phase` is one of `starting`/`running`/`backoff`/`stopped`. `State{Phase,
ChildPID, StartedAt, RestartCount, LastUptime, NextBackoff}` — all six fields are kept faithful because
`cmd/pyry`'s status builder (`buildStatus(supervisor.State)`) reads all six, not just `Phase`. A leaf
`stateMu` (separate from the existing `mu` guarding `stdin`) guards `state`; `updateState(fn)` is called
only by the `Run` goroutine, `State()` is safe from any goroutine. The `Run` loop instruments it exactly
where `supervisor.Run` does: `Starting` at top (once), `Running` + `ChildPID` on spawn, `Backoff` +
`RestartCount++`/`LastUptime`/`NextBackoff` before the crash-path backoff wait (never on a deliberate
restart), `Stopped` in a top-level `defer`.

**Live-restart seam** (previously absent — the old `runner.go` doc explicitly called this out as a gap;
`Run` now has it). A third leaf mutex `restartMu` guards `args` (the live spawn base argv, swapped by
`Restart`, read via `liveArgs()`) and `iterCancel` (the current spawn iteration's `context.CancelFunc`,
published via `setIterCancel` each iteration). `Restart(args []string)` swaps `args`, sends a non-blocking
hint on a buffered(1) `restartCh` (coalesces rapid restarts to one relaunch with the newest args), and
cancels the current `iterCancel` if a child is live — mirroring `supervisor.Restart` byte-for-byte in
shape. `Restart` touches only `restartMu`/`restartCh`/`iterCancel`, never a `Pool`/`Session` lock, so
`Pool.UpdateSettings` can call it after releasing `Pool.mu` with no lock-order concern. Because `firstRun`
is already `false` after the first successful spawn, a restart always respawns via `--resume <sessionID>`
— the conversation resumes rather than forking.

**`WriteUserTurn`/`WaitForPTY`.** `WriteUserTurn(ctx, conversationID, payload)` is a one-line wrap of the
already-reviewed `WriteTurn` free function (#1088/#1093) — no new envelope construction, and it inherits
`WriteTurn`'s exact contract (`ErrNoLiveChild` with no live child, `turncommit.ErrDropped` with zero bytes
on a gate deny). `conversationID` is accepted only for interface conformance and future outbound-cursor
wiring (T4/T7); this slice does not track a cursor. `WaitForPTY(ctx) error` is a bare `return nil` — the
stream path has no PTY to wait for, and the no-live-child window is already handled per-turn by
`WriteTurn`'s retryable `ErrNoLiveChild`.

Concurrency model: three **leaf** mutexes on `Runner` (`mu`, `stateMu`, `restartMu`), never nested, each
owned by a different goroutine/concern. See [codebase/1097.md](../codebase/1097.md).

## Out of scope (follow-on slices)

- **The `stream-json` `RunnerFactory` arm** (#1081) — constructs a `streamRunner` from a
  `supervisor.Config` and wires it into the config selector so a session can actually be created against
  this path. Until it lands, `streamRunner`'s only caller is the `var _ sessions.Runner = streamRunner{}`
  compile assertion.
- **Pool/relay/`cmd/pyry` turn-stream wiring** (drain, interrupt, new-session, snapshot; T4/T7) — the
  #1094 watchdog's `Writer()` still needs composing into `Config.Stdout` alongside the #1088 `Parser`
  with `OnStall`/`PendingPermission` supplied, and a decision on what a `stall(false)` does.
- **The pending-permission signal producer** (#1079/#1080) — the approval flow that would report a
  pending permission to the #1094 hook.

## Related

- [streamrunner-package.md](streamrunner-package.md) — the single-turn stream-json sibling this package inverts (held-open vs. close-after-one-turn); shares the reap seam shape, `ExitErrIsBenign` discipline, and the envelope shape/line-buffering mechanics the turn I/O slice mirrors.
- [turnbridge-package.md](turnbridge-package.md) — `mapper.go`, the content-extraction logic the #1088 parser mirrors (not imports) for assistant text/thinking/tool_use and tool_result mapping.
- [agentrun-package.md](agentrun-package.md) — the shared parent supplying `ResolveWorkdir`, `ExitErrIsBenign`, `ReapDescendantGroups`.
- [ptyrunner-package.md](ptyrunner-package.md) — the PTY-path analogue this package's spawn/teardown shape and #1087's "ptyrunner-skeleton analogue" framing both reference.
- `internal/supervisor`'s `backoffTimer`/`Run` (see [system-overview.md](../architecture/system-overview.md)) — the exponential-backoff-with-stability-reset ladder this package copies verbatim (cannot import across the PTY/stream-json boundary).
- [`codebase/1087.md`](../codebase/1087.md) — the process-lifecycle slice.
- [`codebase/1088.md`](../codebase/1088.md) — the turn I/O slice (this section).
- [`codebase/1093.md`](../codebase/1093.md) — the send-side turncommit gate slice.
- [`codebase/1094.md`](../codebase/1094.md) — the receive-side idle/stall watchdog slice.
- [`codebase/1097.md`](../codebase/1097.md) — the `sessions.Runner` satisfaction slice (`State`/`WriteUserTurn`/`WaitForPTY`/`Restart` + `cmd/pyry` adapter).
- [sessions-package.md](sessions-package.md) — the `Runner` interface / `RunnerFactory` seam this package now satisfies.
- Spec [`docs/specs/architecture/1087-streamsup-child-lifecycle.md`](../../specs/architecture/1087-streamsup-child-lifecycle.md) — the process-lifecycle architect spec.
- Spec [`docs/specs/architecture/1088-streamsup-turn-io.md`](../../specs/architecture/1088-streamsup-turn-io.md) — the turn I/O architect spec.
- Spec [`docs/specs/architecture/1093-turncommit-gate-on-streamsup-send.md`](../../specs/architecture/1093-turncommit-gate-on-streamsup-send.md) — the turncommit gate architect spec.
- Spec [`docs/specs/architecture/1094-streamsup-idle-stall-watchdog.md`](../../specs/architecture/1094-streamsup-idle-stall-watchdog.md) — the idle/stall watchdog architect spec.
- Spec [`docs/specs/architecture/1097-streamsup-runner-satisfies-sessions-runner.md`](../../specs/architecture/1097-streamsup-runner-satisfies-sessions-runner.md) — this slice's architect spec.
