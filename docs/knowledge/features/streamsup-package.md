# `internal/streamsup` — persistent stream-json child lifecycle

Stream-json sibling of [`internal/supervisor`](../architecture/system-overview.md) (the PTY path): supervises a **long-lived, multi-turn** headless `claude` child instead of hosting a screen. Where [`streamrunner`](streamrunner-package.md) spawns claude for one turn, writes the envelope, closes stdin, and exits, `streamsup` spawns claude **once per crash cycle**, holds its stdin open across many turns, and restarts it with the supervisor's backoff ladder on crash. Process lifecycle (#1087), the turn I/O boundary — envelope write + stdout→turnevent parser (#1088) —, the send-side turncommit gate (#1093), the receive-side idle/stall watchdog (#1094), satisfying the `sessions.Runner` seam (`State`/`WriteUserTurn`/`WaitForPTY`/`Restart` + a live-restart seam, #1097), the `newStreamRunnerFactory` constructor that builds a `streamRunner` from a `supervisor.Config` (#1109), the drain that fans its parsed turnevents into the unchanged `interactiveTurnEmitterV2` (#1098), the interrupt send primitive (#1120), the fresh-restart-under-a-new-id mechanism (`RestartFresh`, #1124), and the live permission-approval-flag injection onto the factory's spawn (`withApprovalArgs`, #1168) have shipped. **It is now live in production**: the `interactive_runner: "stream-json"` config toggle (#1081) selects `newStreamRunnerFactory` as `sessions.Config.RunnerFactory` and wires its drain at the relay leg — see [config-package.md](config-package.md) and [codebase/1081.md](../codebase/1081.md).

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

// concrete, off sessions.Runner (#1077) — see "Fresh-restart under a new id" below
func (r *Runner) Interrupt() error
func (r *Runner) RestartFresh(newID string)
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
| `result` | exactly one `TurnEnd` — **the turn boundary**; `Reason` is `resultTurnEndReason(subtype)` (#1120): `error_during_execution` → `TurnEndReasonCancelled`, everything else (including no/unknown `subtype`) → `TurnEndReasonEndTurn` |
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

**Interrupt send primitive (#1120).** `Runner.Interrupt() error` writes a single structured
`control_request` line — `{"type":"control_request","request_id":"<id>","request":{"subtype":"interrupt"}}`
— onto the live child's held-open stdin, ending the running turn (claude acks in ~40ms and closes out the
turn with a `result` whose `subtype` is `error_during_execution`, which the receive-side table above maps
to `TurnEndReasonCancelled` — spike T1, #1075, verified live 2026-07-19). `WriteInterrupt(w io.Writer,
requestID string) error` (`envelope.go`) is the free-function marshal+write half, mirroring `WriteTurn`
minus the turncommit gate — an interrupt is not a queued turn, so there is nothing to claim or drop.
`request_id` is locally minted by a per-`Runner` `atomic.Uint64` (`nextInterruptID`, stringified,
monotonic from 1), never caller-supplied; this ticket writes the id but never reads the
`control_response` ack, so uniqueness-within-the-runner's-lifetime is sufficient — no `crypto/rand`/UUID
dependency. `w == nil` (no live child) is checked first and returns `ErrNoLiveChild` with zero bytes
written — the same safe-no-op contract `WriteTurn` holds — so `Interrupt()` can't panic or partial-write
when called against an idle runner. Small enough (`<PIPE_BUF`) that one `write(2)` can't interleave with
a concurrent `WriteTurn` line on the same fd — the package's existing single-writer-per-syscall
discipline, not a new one. `Interrupt` is a **concrete method on `*Runner`, deliberately not added to
`sessions.Runner`** (kept un-widened per #1077) — mirrors how `*supervisor.Supervisor` encapsulates
`SendEsc` (#726) off the interface; the interrupt *routing* sibling (#1121) reaches it via its own
narrow interface or a type assertion. See [codebase/1120.md](../codebase/1120.md).

**Fresh-restart under a new id (#1124).** `RestartFresh(newID string)` rotates the runner into a fresh
session: the *next* spawn uses `--session-id <newID>` (a new transcript, no fork) instead of `--resume`,
and a later crash-respawn then `--resume`s `newID` — never the pre-rotation id. It reuses the live-restart
seam above, but rotates the **session id**, not the argv: a new `restartMu`-guarded pair,
`sessionID` (the mutable analogue of the construction-time, immutable `cfg.SessionID`; seeded from it in
`New`) and `rotatePending` (a one-shot flag), sit alongside `args`/`iterCancel` in the same field group.
`Run`'s spawn loop reads `nextSpawnID()` — a `restartMu`-guarded accessor that snapshots
`(sessionID, rotatePending)` and clears `rotatePending` in one critical section — instead of
`r.cfg.SessionID` directly; when `rotatePending` is true it re-arms the Run-goroutine-private `firstRun`
local to `true` before calling `buildArgs` (itself untouched). The existing `started`-gated `firstRun`
flip (see the gate above) then does the rest for free: a successful fresh spawn flips `firstRun` back to
`false` so the next respawn `--resume`s the rotated id; a setup failure on the fresh spawn leaves
`firstRun` `true` so the retry keeps trying `--session-id <newID>` rather than `--resume`-ing a session
that was never established. `RestartFresh` mirrors `Restart`'s hint-before-cancel ordering and
newest-wins coalescing exactly, but **leaves `r.args` untouched** — `new_session` rotates identity, not
flags, so args stay `Restart`/`UpdateSettings`'s concern. An empty `newID` is a `Warn`-logged no-op (the
runner never spawns `--session-id ""`); this is a deterministic last-resort guard, not the primary
validation — that's the pool/routing layer's job (#1125), per the "caller-supplied id validation at the
primitive boundary" convention. Concrete method, off `sessions.Runner` (#1077), same discipline as
`Interrupt` above — the routing sibling (#1125) reaches it via a narrow interface or type assertion. See
[codebase/1124.md](../codebase/1124.md).

Still deferred (needs richer context than this slice): `max_tokens`/`refusal` `TurnEnd` reason
classification (`resultTurnEndReason`'s `default` branch is the safe placeholder until one is observed);
routing an inbound remote interrupt frame to the correct per-conversation runner (#1121, blocked-by
#1120); routing an inbound `new_session` frame to the correct per-conversation runner and the pool-side
`Pool.RotateID` (#1125, blocked-by #1124); whether the parser's line buffer needs resetting across a
crash-restart (see [codebase/1088.md](../codebase/1088.md) — code review flagged a stale-partial edge
case, non-blocking for this unwired slice).

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
`docs/lessons.md` § "Interface adapters for covariant returns". The factory that *constructs* a
`streamRunner` from a `supervisor.Config` — `streamRunnerFactory`, in the same file — is #1109 (below); the
`interactive_runner` selection that injects it on `sessions.Config.RunnerFactory` is #1081 (shipped —
see below).

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
`Restart`, read via `liveArgs()`), `iterCancel` (the current spawn iteration's `context.CancelFunc`,
published via `setIterCancel` each iteration), and — since #1124 — the `sessionID`/`rotatePending` pair
`RestartFresh` rotates (see "Fresh-restart under a new id" below). `Restart(args []string)` swaps `args`,
sends a non-blocking hint on a buffered(1) `restartCh` (coalesces rapid restarts to one relaunch with the
newest args), and cancels the current `iterCancel` if a child is live — mirroring `supervisor.Restart`
byte-for-byte in shape. `Restart` touches only `restartMu`/`restartCh`/`iterCancel`, never a
`Pool`/`Session` lock, so `Pool.UpdateSettings` can call it after releasing `Pool.mu` with no lock-order
concern. Because `firstRun` is already `false` after the first successful spawn, a plain restart always
respawns via `--resume <sessionID>` — the conversation resumes rather than forking; `RestartFresh` is the
one path that re-arms `firstRun` to force a fresh `--session-id` spawn instead.

**`WriteUserTurn`/`WaitForPTY`.** `WriteUserTurn(ctx, conversationID, payload)` is a one-line wrap of the
already-reviewed `WriteTurn` free function (#1088/#1093) — no new envelope construction, and it inherits
`WriteTurn`'s exact contract (`ErrNoLiveChild` with no live child, `turncommit.ErrDropped` with zero bytes
on a gate deny). `conversationID` is accepted only for interface conformance and future outbound-cursor
wiring (T4/T7); this slice does not track a cursor. `WaitForPTY(ctx) error` is a bare `return nil` — the
stream path has no PTY to wait for, and the no-live-child window is already handled per-turn by
`WriteTurn`'s retryable `ErrNoLiveChild`.

Concurrency model: three **leaf** mutexes on `Runner` (`mu`, `stateMu`, `restartMu`), never nested, each
owned by a different goroutine/concern. See [codebase/1097.md](../codebase/1097.md).

## Constructing a `streamRunner` — `newStreamRunnerFactory` (#1109, extended #1098, #1168)

`cmd/pyry/streamsup_runner.go` also holds `newStreamRunnerFactory(sink *streamTurnSink, mcpApprovePath
string) sessions.RunnerFactory` — returns exactly the `sessions.RunnerFactory` signature (above); #1109
delivered the constructor (as the bare `streamRunnerFactory` func, the first `streamsup.New` caller
tree-wide), #1098 turned it into this `sink`-capturing constructor so the Parser it installs has somewhere
to send turnevents, and #1168 added the `mcpApprovePath` param to inject the permission-approval flags.
Body of the returned closure: `scfg := mapStreamsupConfig(cfg)`; `scfg.Args =
withApprovalArgs(scfg.Args, mcpApprovePath)`; `scfg.Stdout =
streamsup.NewParser(sink.sinkFor(cfg.SessionID), cfg.Logger)`; `streamsup.New(scfg)`; on error,
`fmt.Errorf("cmd/pyry: stream runner: %w", err)` and a genuine nil `sessions.Runner`; on success,
`streamRunner{r: r}`.

**`withApprovalArgs(args []string, mcpApprovePath string) []string` (#1168)** is the interactive-stream
twin of `agent_run.go`'s non-yolo `permissionArgs` wiring (#1106) — the first live consumer of
`permissionArgs`/`writeMCPApproveConfig` on the interactive path. Reads `--dangerously-skip-permissions`
off `args` as the single deterministic per-spawn yolo signal (both the bootstrap operator pass-through and
`sessions.claudeSettingsArgs`'s per-session YOLO funnel through that one flag): present → return `args`
unchanged (byte-identical to pre-#1168, no duplicate flag); absent → `append(slices.Clone(args),
permissionArgs(false, mcpApprovePath)...)`. Runs inside the shared factory closure, so it covers **both**
the bootstrap runner and per-conversation runners — a per-conversation stream session cannot silently
bypass the approval gate. `mcpApprovePath` is the daemon-global `--mcp-config` file `runSupervisor` writes
once at startup via `writeMCPApproveConfig` (gated on `cfg.InteractiveRunner == "stream-json"`,
fail-closed on write error, removed at shutdown); on the `""`/`"pty"` path the factory is never built, so
the PTY interactive argv is untouched. See [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md) and
[codebase/1168.md](../codebase/1168.md).

**No PTY fallback, structurally.** The function has no branch that calls `supervisor.New` — a
`streamsup.New` failure (missing binary, absent work dir; an empty `SessionID` is impossible at the pool
sites per #1108) always surfaces as an error rather than silently degrading the bootstrap (the session
`pyry attach` drives) to the PTY path.

**`mapStreamsupConfig(cfg supervisor.Config) streamsup.Config`** is the pure, fully-inspectable mapper and
the primary tested surface — **unchanged by #1098**. Field mapping: `ClaudeBin`/`WorkDir`/`SessionID`/
`Logger`/`BackoffInitial`/`BackoffMax`/`BackoffReset` copy verbatim; `ClaudeArgs → Args` (streamsup's argv
field has a different name) through `stripSessionIDFlags`; `Stdout`/`Stderr`/`Env` stay nil **inside the
mapper** (`Stdout` is filled one layer up, in `newStreamRunnerFactory`'s closure — keeping the mapper pure
and its `Stdout == nil` assertion untouched; `Stderr`/`Env` have no `supervisor.Config` analogue). The
seven PTY-only fields (`ResumeLast`, `ResolveSessionID`, `Bridge`, `ValidateConversation`,
`ResolveTranscript`, `RecordDir`, `helperEnv`) are deliberately not mapped.

**`stripSessionIDFlags(args []string) []string`** returns a fresh slice — never mutating the input, which
is aliased into the pool's `spawnBase` — with every `--session-id`/`--resume` occurrence removed (two-token
form, joined `--flag=value` form, and a dangling flag with no following token all handled). `buildArgs`
(above) re-injects `--session-id <id>` on first spawn / `--resume <id>` on respawn from `Config.SessionID`
itself, so an un-stripped id flag in `Args` would double-inject. Required at the per-session site
(`Pool.buildSession` bakes `--session-id <id>` into `ClaudeArgs`); a harmless no-op at the bootstrap site
(`Pool.New`'s `ClaudeArgs` carry no id flag).

Neither #1109 nor #1098 wired the factory into production on their own — that was #1081's scope (below).
See [codebase/1109.md](../codebase/1109.md).

## Draining turnevents into the interactive emitter (#1098)

The turn I/O parser (#1088) emits neutral `turnevent.Event`s from its sink callback, but that sink is
fixed where the runner is **constructed** (`newStreamRunnerFactory`, above), which sees only a
`supervisor.Config` — no handle on `interactiveTurnEmitterV2`, which is built later and separately on the
relay leg. `cmd/pyry/stream_turn_drain.go` lines the two lifetimes up with a late-bound, daemon-singleton
fan-in, and reproduces the PTY path's `startInteractiveTurnStreamV2` `OnEvent`/`FlushSignal`/`OnFlush`
triple **without `turnbridge`** — the Parser already emits `turnevent.Event`, so there is nothing to
un-map back to a `tuidriver.Event` for `turnbridge` to re-map.

```go
type streamTurnEnvelope struct {
    sessionID string
    ev        turnevent.Event
}

type streamTurnSink struct { /* one buffered chan streamTurnEnvelope */ }

func newStreamTurnSink(buf int, logger *slog.Logger) *streamTurnSink
func (s *streamTurnSink) sinkFor(sessionID string) func(turnevent.Event) // non-blocking send

func startStreamTurnDrainV2(
    ctx context.Context,
    sink *streamTurnSink,
    emitter *interactiveTurnEmitterV2,
    activeSession func() (sessionID string, ok bool),
    busy *turnBusyTracker,
    logger *slog.Logger,
) (cleanup func())
```

**Fan-in, not fan-out.** N per-session Parsers (each fixed at runner construction, one per
`newStreamRunnerFactory` invocation) push `{sessionID, ev}` onto the one buffered channel (256 slots, the
`pushQueueCap` precedent); `startStreamTurnDrainV2` spawns the sole reader goroutine. There is no
session-keyed registry and no subscribe/unsubscribe — the per-conn fan-out stays entirely inside the
unchanged emitter, so this is deliberately *not* shared fan-out infrastructure.

**Non-blocking send, drop-newest.** `sinkFor`'s closure runs on claude's stdout forwarder goroutine (the
same one `os/exec` drives the Parser's `Write` from); a blocking send on a full channel would wedge the
child. On a full channel it drops the newest event and Debug-logs content-free (`event`, `kind`,
`session_id` only — never `ev`'s assistant/thought/tool content). The channel is **never closed** (a
Parser may outlive the drain during shutdown; the drain stops on `ctx`, not on channel close, so a
send-on-closed panic is structurally impossible).

**The per-event session gate is AC2's scoping property.** The drain goroutine resolves `activeSession()`
and forwards to `emitter.Handle` only when the producing session equals the active conversation's bound
session; every other session's event is dropped **before** `Handle` is ever called, so a background
conversation's connection never receives it. Gating happens at `Handle` time (in the drain goroutine),
not inside the sink, so the drop decision stays consistent with the cursor `Handle` itself reads via
`CurrentConversation()`. On an active-conversation switch the emitter's own #1062 `turnConvID` guard
flushes the prior conversation's buffered delta and re-mints a fresh turn for the new one — the drain
supplies session-level gating, the emitter's existing follow-active logic does the rest.

**Single-writer invariant.** Only the drain goroutine ever calls `emitter.Handle`/`flushDelta` — same
single-Run-goroutine assumption the PTY producer relies on, `-race`-tested by feeding two sessions'
Parsers concurrently. The drain also selects `emitter.flushC()` (the emitter arms its own coalescing
timer inside `Handle` but does not select it — a driver must) and calls `flushDelta` on the same
goroutine, so there's no cross-goroutine timer race.

**No transcript on this path (AC3).** `stream_turn_drain.go` imports no fsnotify, resolves no `<uuid>.jsonl`
path — structural, asserted by the test file doing no filesystem setup.

**The emitter is passed in, not built here** — the caller (the unit test in this ticket; #1081's
production wiring, below) owns its construction and replay wiring (`SetReplaySource`). This ticket's
`activeSession` is a plain injected func; #1081 composes it as `boundSessionIDForActive(w.active,
w.convReg)` — the relay leg's own follow-active resolver, not `boundHost`. See
[codebase/1098.md](../codebase/1098.md).

## Per-conversation turn-busy tracking (#1201)

`cmd/pyry/stream_turn_busy.go`'s `turnBusyTracker` (`newTurnBusyTracker(resolve, logger) *turnBusyTracker`,
`Busy(conversationID string) bool`, `WaitIdle(ctx, conversationID) error`) is a self-synchronised,
per-conversation set of conversations with an open turn on the stream-json path — the answer the delivery
path will need ("is a turn running for conversation X?") that the emitter's own lifecycle fields
structurally cannot give: those are unguarded (single-Handle-goroutine only), scalar rather than
per-conversation, and populated only for the conversation the cursor points at.

**Fed from `startStreamTurnDrainV2`'s `sink.ch` arm, one line before the `activeSession()` gate** — a
`busy.observe(env.sessionID, env.ev)` call, unconditional, ahead of the existing drop-if-not-active check.
Ordering is the entire contract: the gate gets its identity from a *different* place than `emitter.Handle`
does (`env.sessionID`, tagged at parser construction, vs. the cursor `Handle` reads internally), so feeding
before the gate is what lets a turn on a *non-active* conversation still report busy — feeding after it
would make the tracker just as cursor-blind as the emitter it's replacing. `observe` resolves
`sessionID → conversationID` via an injected closure (production passes `conversationForSession(w.convReg,
sid)` — the same resolver `session_transition` frames use, `relay.go:756`), keyed by conversation (not
session) so a `/clear`-rotated session's late events still land under `SessionHistory`'s match. An
unresolvable or empty-string conversation id is simply not tracked (never under an empty key — that would
both wedge and collide with the "unknown conversation" answer).

The opener set is a **whitelist**: `ThoughtChunk`/`TextChunk`/`ToolStart`/`ToolUpdate` add the conversation,
`TurnEnd` (either stop reason — `resultTurnEndReason` sends both through one parser arm) deletes it,
everything else (`Stall`/`ApiRetry`/`Compacting`, and any future variant) is a no-op. The evidence is the
parser's own tolerate-and-drop `default:` arm (`parser.go:158-163`) — `rate_limit_event` is the line that
becomes a wired `ApiRetry` the day someone adds it, and a blacklist ("anything that isn't `TurnEnd` opens a
turn") would wedge a conversation on it. `Stall` cannot reach this tracker through the real sink today (the
parser emits only the five variants in the table above) — it's asserted only at the unit tier, fed directly.

Concurrency: one mutex guards one `map[string]struct{}` plus a `chan struct{}` "generation" broadcast,
closed-and-replaced under the same lock as any membership mutation. `WaitIdle` captures that channel and
re-checks membership under one lock acquisition (splitting the two reintroduces a lost-wakeup race), then
selects on it against `ctx.Done()`. `Busy`/`WaitIdle` are callable from any goroutine; `observe` is called
only from the drain goroutine and inherits its single-writer invariant, though the type is self-synchronised
regardless. Existence-oracle discipline (#1101 posture): `Busy`'s signature is `bool`-only — no error, no
second `found` bool — so a foreign conversation id is indistinguishable from an idle one in both value and
code path.

**Ships unwired.** No production caller reads `Busy`/`WaitIdle` yet — `observe`'s `nil`-receiver no-op is
what let the 7 pre-existing drain-test call sites take a bare `nil` for the new parameter instead of each
constructing a tracker. The parameter stays the concrete `*turnBusyTracker`, never an interface (a typed-nil
in an interface field would be non-nil at the interface level and route past the nil guard into a nil-map
read — the `screenSnapshotterOrNil` hazard, `relay.go:395-410`).

**Known gap, recorded in the tracker's own doc comment, not just here:** the clear is event-driven only —
a turn closes solely on its `TurnEnd` arriving. Two paths reach a permanently-busy conversation with no
`TurnEnd` ever arriving, and each is its own slice landing before any consumer reads the signal: a session
torn down under the conversation (`/clear` rotation, idle/cap eviction) — #1202; a child that dies mid-turn
and respawns, firing no pool transition and no `result` line for the abandoned turn — #1203. A narrower
rotation edge is folded into #1202 too: because the resolver matches `SessionHistory`, a retired session's
late `TurnEnd` can clear a turn its successor opened — fails open (reports idle when busy), same direction
as a daemon restart (starts all-idle). See [codebase/1201.md](../codebase/1201.md).

## Production wiring — the `interactive_runner` toggle (#1081)

`cmd/pyry/main.go`'s `selectInteractiveRunner(cfg, logger)` maps `cfg.InteractiveRunner` to
`(sessions.RunnerFactory, *streamTurnSink, error)`: `""`/`"pty"` returns `(nil, nil, nil)` (the rollback
path — nil `RunnerFactory` byte-identical to today); `"stream-json"` builds exactly one
`newStreamTurnSink`, feeds it to `newStreamRunnerFactory(sink)`, and returns both the factory and the
sink so the caller can thread the **same instance** two ways: `RunnerFactory` in the `sessions.Config`
literal, and `streamSink` in `relayWiring` for the relay leg's drain. Any other value aborts daemon
startup before `sessions.New` runs (AC4 — no silent PTY fallback).

At the relay leg (`cmd/pyry/relay.go`), a non-nil `w.streamSink` is the stream-mode discriminant. It
gates off the three raw `w.sup.State()` readers that would nil-deref on the typed-nil bootstrap
supervisor in stream mode (`snapshotUsage`, and both PTY interactive streams —
`startInteractiveTurnStreamV2`/`startInteractiveModalStreamV2`), and in their place builds
`newInteractiveTurnEmitterV2` + `mgr.SetReplaySource(...)` (byte-identical to the PTY path's
construction) fed by `startStreamTurnDrainV2`, scoped to the active conversation's bound session via
`boundSessionIDForActive(w.active, w.convReg)` — fail-closed on no active conversation / unknown
conversation / unbound session (never falls through to the bootstrap session). The PTY modal stream's
stream-mode analogue (the #1080 approval bridge) was already wired unconditionally, so only the turn
stream needed a replacement. See [codebase/1081.md](../codebase/1081.md) for the full wiring and
[config-package.md](config-package.md) for the operator-facing `interactive_runner` field and rollback.

**Known gap (tracked, fail-closed): frozen sink tag vs. `new_session` rotation.** The sink tags each
event with the runner's *construction-time* `SessionID`; `RestartFresh` (a stream-mode `new_session`)
rebinds `conv.CurrentSessionID` to a fresh id but doesn't retag the Parser, so `boundSessionIDForActive`
and the event tag diverge and the drain's scoping gate drops everything for that conversation until the
daemon restarts. No cross-session disclosure (unmatched tag ⇒ dropped, not misdelivered). Follow-up:
#1133. Confirmed live (not just by inspection) by the #1137 `new_session` e2e: after a stream rotation, a
subsequent turn's `assistant_delta` never reaches the phone, so that spec's post-rotation "serving a turn"
milestone asserts delivery at the fakeclaude stdin boundary instead — see
[codebase/1137.md § The post-rotation drain divergence](../codebase/1137.md).

## Test fake for this wire — fakeclaude stream-json mode (#1140)

`internal/e2e/internal/fakeclaude`'s `PYRY_FAKE_CLAUDE_STREAM_JSON` mode
hand-mirrors this package's wire (not imports — the types here are unexported)
so the stream `interactive_runner` path has a fake `claude` child to drive
end-to-end without a real binary: it reads `{"type":"user",…}` envelopes shaped
like `envelope.go`'s `userTurn` and emits `assistant`/`result` lines this
package's own `Parser` maps to `TextChunk`/`TurnEnd`. See
[fakeclaude-binary.md § Stream-json mode](fakeclaude-binary.md#stream-json-mode-1140)
and [codebase/1140.md](../codebase/1140.md).

## Out of scope (follow-on slices)

- **Composing the #1094 watchdog's `Writer()` into `Config.Stdout` alongside the #1098 `Parser`** — still
  not done; `newStreamRunnerFactory`'s closure sets `scfg.Stdout` to the `Parser` alone, with
  `OnStall`/`PendingPermission` unsupplied. Needs an `io.MultiWriter(parser, wd.Writer())` composition
  and a decision on what a `stall(false)` does downstream.
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
- [`codebase/1109.md`](../codebase/1109.md) — the `streamRunnerFactory`/`mapStreamsupConfig`/`stripSessionIDFlags` construction slice.
- [`codebase/1098.md`](../codebase/1098.md) — the turnevent drain slice (`streamTurnSink`/`startStreamTurnDrainV2`).
- [`codebase/1120.md`](../codebase/1120.md) — the interrupt send primitive + `result` subtype mapping slice.
- [`codebase/1124.md`](../codebase/1124.md) — the fresh-restart-under-a-new-id mechanism slice (`RestartFresh`/`nextSpawnID`).
- [`codebase/1168.md`](../codebase/1168.md) — `withApprovalArgs`, wiring the #1106 permission-approval flags onto the live interactive spawn.
- [`codebase/1201.md`](../codebase/1201.md) — the per-conversation turn-busy tracker (`turnBusyTracker`), fed from the drain before its active-session gate; ships unwired, blocked-on-by #1202 (session teardown clear) and #1203 (mid-turn respawn clear) before any consumer reads it.
- [pyry-mcp-approve-command.md](pyry-mcp-approve-command.md) — the MCP stdio server `withApprovalArgs`'s `--mcp-config` points claude's approval-prompt tool at.
- `cmd/pyry/interactive_turn_v2.go`'s `interactiveTurnEmitterV2` / `cmd/pyry/interactive_turn_stream_v2.go`'s `startInteractiveTurnStreamV2` — the PTY-path emitter and lifecycle shape #1098's drain reproduces for the stream-json path (no dedicated feature doc yet; see [turnbridge-package.md](turnbridge-package.md) for the producer side it mirrors).
- [sessions-package.md](sessions-package.md) — the `Runner` interface / `RunnerFactory` seam this package now satisfies, and the `supervisor.Config.SessionID` seam `mapStreamsupConfig` reads.
- Spec [`docs/specs/architecture/1087-streamsup-child-lifecycle.md`](../../specs/architecture/1087-streamsup-child-lifecycle.md) — the process-lifecycle architect spec.
- Spec [`docs/specs/architecture/1088-streamsup-turn-io.md`](../../specs/architecture/1088-streamsup-turn-io.md) — the turn I/O architect spec.
- Spec [`docs/specs/architecture/1093-turncommit-gate-on-streamsup-send.md`](../../specs/architecture/1093-turncommit-gate-on-streamsup-send.md) — the turncommit gate architect spec.
- Spec [`docs/specs/architecture/1094-streamsup-idle-stall-watchdog.md`](../../specs/architecture/1094-streamsup-idle-stall-watchdog.md) — the idle/stall watchdog architect spec.
- Spec [`docs/specs/architecture/1097-streamsup-runner-satisfies-sessions-runner.md`](../../specs/architecture/1097-streamsup-runner-satisfies-sessions-runner.md) — the `sessions.Runner` satisfaction architect spec.
- Spec [`docs/specs/architecture/1109-streamsup-runner-factory.md`](../../specs/architecture/1109-streamsup-runner-factory.md) — the `streamRunnerFactory` construction architect spec.
- Spec [`docs/specs/architecture/1098-stream-turn-drain.md`](../../specs/architecture/1098-stream-turn-drain.md) — this slice's architect spec, including the scoping proof and security review.
