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
func (r *Runner) SetSpawnArgs(args []string) // #1580 — Restart's swap half, no kill

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
  iterCtx, cancel, args, forceFirst := beginSpawn(ctx, firstRun)  // ONE restartMu section: reads the
                                                        //   Restart-swapped argv AND the (possibly
                                                        //   rotated) id, consumes rotatePending,
                                                        //   builds the argv, publishes iterCancel
  if forceFirst → firstRun = true                       // a RestartFresh was consumed: re-arm first-run form
  log "spawning claude"                                 // AFTER the section: no log I/O under a leaf mutex
  started, waitErr := spawnAndWait(iterCtx, args)       // blocks until child exits or a restart cancels iterCtx
  cancel(); clearIterCancel()
  if ctx.Err() != nil → return ctx.Err()                // parent-ctx cancel = teardown, not a crash
  if started → firstRun = false                         // see the firstRun gate below
  if drainRestart() → continue                          // deliberate restart, not a crash: skip backoff
  delay := backoff.next(uptime)
  select { <-time.After(delay) | <-ctx.Done() → return ctx.Err() | <-restartCh → relaunch now }
```

**The single `beginSpawn` acquisition is load-bearing (#1481), not tidiness.** Reading the spawn inputs and publishing `iterCancel` are one `restartMu` section, so a racing `Restart`/`RestartFresh` — which takes that mutex exactly once — is serialised either *wholly before* it (the spawn being set up observes the swapped argv / rotated id) or *wholly after* it (it finds the just-published cancel and tears that spawn down, and `drainRestart` relaunches immediately under the new state). There is no third position, so "a live child under a pre-rotation id **and** no live iteration cancel" is unreachable. Until #1481 this was two sections with a `buildArgs` allocation and a synchronous log write between them, and `iterCancel` was still `nil` from the previous iteration across that gap: a racer landing there wrote its rotation, cancelled **nothing**, and the spawn launched a child under the pre-rotation id for that child's whole lifetime — after which the still-set `rotatePending` made the next crash-respawn `--session-id <newID>`, a fresh transcript, silently discarding every turn since the rotation. `buildArgs` had to move inside the section because `restartMu` is not reentrant (the old `liveArgs()`/`nextSpawnID()` accessors each took it, so a fused section could not call them); both are deleted, and the publish side is narrowed to a no-argument `clearIterCancel` so nothing outside `beginSpawn` can express a publish at all.

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
| `user` | one `ToolUpdate` per `tool_result` block (status from `is_error`, content from the string/array union); every other block surfaces as `Unrecognized{Site: user_block}` **except one exact 100-byte payload** (#1247, below), dropped in silence |
| `result` | exactly one `TurnEnd` — **the turn boundary**; `Reason` is `resultTurnEndReason(subtype)` (#1120): `error_during_execution` → `TurnEndReasonCancelled`, everything else (including no/unknown `subtype`) → `TurnEndReasonEndTurn` |
| `system` (unmapped subtypes) | nothing — the **known-ignored** tier, Debug-logged by type only, never content |
| `system/task_started` | one `BackgroundTaskStarted` (#1380, below) |
| `system/task_updated` | one `BackgroundTaskUpdated` (#1382, below) |
| `system/background_tasks_changed` | one `BackgroundTaskRoster` (#1381, below) — the family's one **aggregate** variant |
| `system/thinking_tokens` | **at most one** `ThinkingProgress` per `minThinkingTokensPerEvent` (64) tokens of accumulated `estimated_tokens_delta` (#1385, below) — the family's one **rate-bounded** variant; most lines emit nothing |
| `rate_limit_event` | one `turnevent.RateLimited` **unless** `rate_limit_info.status` is the one measured-benign value or the line carries no decodable `rate_limit_info` (#1404, below) — the family's **first non-`system` mapping**, and the one whose gate suppresses the common case rather than the rare one |
| `control_response` | nothing — consumed **content-free** from its own arm, matched on the top-level `type` ALONE so any `subtype` is consumed (#1500). This is the ack the daemon **solicits for itself**: interrupt on this path is a stdin `control_request` and claude answers ~40 ms later on the same stdout, so without the arm every interrupt fired a false `unrecognized_message`. Shape authority is the verbatim capture in [`set-permission-mode-inband-probe.md`](set-permission-mode-inband-probe.md#the-control_response-received-verbatim) — `subtype` and `request_id` nest **under `response`**, inverting the request side, so `streamLine.Subtype` decodes empty. A `subtype:"error"` NAK is consumed indistinguishably; deliberate, no such failure has been observed |
| any other type, and any line/block that fails to decode | one `Unrecognized` — the **surfaced** tier (see below) |

**Two tiers, and the split is the whole design.** Before this, everything outside the three mapped
types was dropped with a `Debug` log. The production daemon runs at info level, so that drop left **no
trace anywhere and no client was told** — fine for the types we ignore on purpose, useless for a type we
have never seen. Now the parser distinguishes *known and deliberately ignored* (silent, as before) from
*genuinely unrecognized* (surfaced as `turnevent.Unrecognized`, which reaches desktop clients as an
`unrecognized_message` frame and renders as an expandable timeline row).

`ignoredLineTypes` holds the first tier. It is **measured, not guessed** — claude driven directly on the
bare stream-json surface on 2026-07-27, three turns each on two models, one calling tools:

- `system` is claude's catch-all namespace and its highest-rate emitter: `system/init` fires **once per
  turn** and `system/thinking_tokens` roughly ten times per turn, so subtype-grained matching risks
  turning every new subtype into a per-turn noise row — exactly the failure the two tiers exist to
  prevent. Until 2026-08-07 that argument was implemented by ignoring `system` **wholesale**; #1380
  refines it (below) rather than reversing it — `system` stays on `ignoredLineTypes` unchanged, and one
  measured subtype is now mapped inside that same ignored branch.
- `rate_limit_event` fires ~1 per run. **MAPPED since 2026-08-09 (#1404)** — it is no longer a member of
  `ignoredLineTypes` (the map is down to `{"system": true}`); it has its own arm in `consumeLine`'s main
  switch, gated on `status`, below.
- The measurement also settled a standing question: claude does **not** echo the delivered prompt back
  as a `user`/`text` message on this surface, though it does on the agent-run surface. So `user`/`text`
  needs no ignore entry, and one appearing in future is a real change that surfaces.

**AMENDED 2026-07-30 (#1247).** That measurement never drove a *backgrounded* command. Backgrounding
produces a turn with no visible model output, and claude's harness then injects a `user`/`text` message
prodding the model to speak — reproduced 3 of 3 on the #1240 probe, claude 2.1.220. Exactly one such
string, `harnessNoOutputNudge`, is now dropped in silence by byte-exact equality, guarded on block type
`text` so a `tool_result` (whose payload decodes into `Content`, never `Text`) can't reach it — this is
the parser's **first block-level suppression**, a new tier sitting below `ignoredLineTypes` rather than
an entry on it (that map stays top-level types only, and its own comment now carries this amendment
in place). `continue`, not `return`, scopes the drop to the one block, so a sibling `tool_result` in the
same message still maps. Every *other* `user`/`text` block is still a real change and still surfaces —
matched by exact string, not prefix or substring, because the wording is attested on one claude version
and drift must bring the row back rather than stay silently swallowed. See
[codebase/1247.md](../codebase/1247.md).

**Second observation, 2026-08-02 (#1260).** A separate capture session (independent of the one #1247's
constant was transcribed from) reproduced the same block byte-exact
(`harness_nudge.matches_shipped_constant: true` in `testdata/dropped_lines_v2.1.220.json`) — the second
confirmed payload `harnessNoOutputNudge`'s own doc comment names as the trigger for promoting the
constant from a lone string to a set with a pin test. That promotion has not been done; it is deferred
as a follow-up rather than bundled into #1260, which was scoped to capture and record only. See
[codebase/1260.md](../codebase/1260.md).

`TestParser_IgnoredLineTypesIsTheMeasuredSet` pins the list, so growing it is a deliberate edit with a
measurement behind it. The real-claude suite's shared `drainForCompletedTurn` fails on **any**
unrecognized frame, so every stream spec is a sentinel: it goes red the day claude adds a message type,
in the pre-ship gate rather than in front of a user.

**`system` maps per-subtype since 2026-08-07 (#1380) — the wholesale-drop design's first crack.** `status`
and any never-seen subtype stay silent exactly as before. Four subtypes are now mapped:
`system/task_started` → `turnevent.BackgroundTaskStarted` (`TaskID`, `ToolCallID` — claude's
`tool_use_id`, renamed to match `ToolStart`/`ToolUpdate`'s field name for the same identifier —
`Description`, `TaskType`, `TruncatedFields`); `system/task_updated` → `turnevent.BackgroundTaskUpdated`
(`TaskID`, `Patch`, `TruncatedFields`); `system/background_tasks_changed` → `turnevent.
BackgroundTaskRoster` (`Tasks []BackgroundTask`, `DroppedTasks`) — the aggregate variant, snapshotting
every task claude is tracking at that moment rather than reporting what happened to one; and
`system/thinking_tokens` → `turnevent.ThinkingProgress` (`EstimatedTokens`, `EstimatedTokensDelta`,
**no** `TruncatedFields` — two `int`s cannot grow) — the one **rate-bounded** variant, described below.
The first three mappings fix #1240's symptom: previously a backgrounded command's lifecycle was
indistinguishable from a genuine turn end (`turn_end`/`end_turn`, state `idle`) because the whole
`system` family was dropped regardless of subtype. `thinking_tokens` fixes a different gap: it is
claude's only mid-turn proof of life on this surface, so mapping it gives a client watching a long turn
something to distinguish "slow" from "wedged."

The match (`emitSystemSubtype`) sits **inside** `consumeLine`'s existing `ignoredLineTypes` branch rather
than beside it — `system` stays on the list unchanged, so `emitUnrecognized` (the surfaced tier) stays
structurally unreachable from any `system` line whatever its subtype, and `TestParser_
IgnoredLineTypesIsTheMeasuredSet` above is unaffected. `emitSystemSubtype`'s `case` arms are the single
enumeration of the mapped set; every comment describing the drop rule (this file included) points there
rather than restating it — a fifth captured subtype is a new case arm there, not a new sibling ticket.

**`system/thinking_tokens` → `turnevent.ThinkingProgress` is rate-bounded, not 1:1 (#1385).** claude's
`estimated_tokens_delta` is accumulated in a new unexported `Parser.thinkingSinceEmit int` field and an
event is emitted once the accumulated delta crosses `minThinkingTokensPerEvent` (64) — most lines
consume silently. The bound keys on the per-line **delta**, not the cumulative `estimated_tokens`
counter, because that counter restarts near zero at every inference-request boundary within one turn: a
high-water-mark rule over the cumulative counter goes silent for a whole burst (the committed capture's
burst 2 never exceeds burst 1's peak), while a delta-accumulator only ever grows and so is immune by
construction. The crossing test is written subtracted (`d >= bound - acc`), never additive (`acc +=
d; if acc >= bound`) — the additive form overflows on an extreme `estimated_tokens_delta` and silently
kills the turn's liveness signal for the rest of the turn; subtracted, both operands stay in `[1, bound]`
by the invariant `acc ∈ [0, bound-1]`, so the failure is unrepresentable. `thinkingSinceEmit` resets
unconditionally on `consumeLine`'s `result` arm (both result subtypes) — the parser's one turn boundary.
See [codebase/1385.md](../codebase/1385.md).

**`rate_limit_event` → `turnevent.RateLimited` is the fifth mapping, the first that is not a `system`
subtype, and its substance is a gate rather than the field copy (#1404).** claude emits this line **once
per run** whatever the state of the usage-limit window — `status` reads `"allowed"` in all three captures
on record (three claude versions), i.e. every run that produced one hit no limit at all — so a 1:1
mapping would put one "you are rate limited" event on every healthy turn. `status` is the discriminator,
with three reachable readings, all decided and stated at `emitRateLimit`: (1) `status ==
benignRateLimitStatus` ("allowed") → silence; (2) `status` non-empty and not the benign value → one
`RateLimited`, because the failure direction is the safe one — an unrecognised status surfaces and a human
looks, rather than a real limit vanishing; (3) `rate_limit_info` absent, present-but-empty, or the line
fails to decode → silence, **not** emit — the naive "emit unless status is allowed" reading is rejected,
because an absent container answered with "emit" turns a container rename into a per-run noise row on
every healthy run forever (the worse failure), whereas answering it with silence produces a false negative
on a condition that has never fired once in three captures. The cost is named rather than hidden: a
container rename is undetected by any automatic test, and the only backstop is the live drop census (the
same one that surfaced this payload in the first place, #1260) still recording the dropped line's shape.
`RateLimited{Status, LimitType, ResetsAt int64, TruncatedFields}` — `Status`/`LimitType` bounded by
`maxRateLimitField` (256, ~28× the observed 7–9 byte values — wide because the value set beyond the one
benign status is unmeasured); `ResetsAt` is claude's unix-seconds number passed through **unbounded and
unvalidated in both directions** (not `time.Time` — converting would invent a claim the bytes do not
make). No rate bound: once-per-run is measured, not enforced, so an adversarial or buggy claude emitting
many non-benign lines produces many events (an accepted, named exposure, not a mechanised one, per
evidence-based fix selection). The variant is a **report, never a control input** — nothing in the daemon
may key a behaviour on it. `session_id`/`uuid` are absent from the decode target itself, as with the
background-task family; so are the payload's four `overage*` keys, two of which are measured
version-variable across claude 2.1.158/2.1.199/2.1.220. The drop site logs one message
(`rateLimitDropMsg`) with a `reason` drawn from a closed keyword set — `Status` never reaches a log, since
it is the one claude-authored value on this line and the field a drop site is most tempted to explain
itself with. `cmd/pyry/interactive_turn_v2.go`'s `eventKind` gained a fifth mapped arm
(`turnevent.RateLimited → "rate_limited"`, name only); `acpbridge.MapUpdate` and `stream_turn_busy.go`'s
opener whitelist correctly drop it through their existing `default:` arms — the ACP/desktop lane is
deliberately untouched (#1262) and a usage-limit report opens no turn. `turnbridge.MapEvent` no longer
drops it: the wire type landed in #1405 and the mapping arm in #1410, so the variant now reaches an
interactive v2 mobile client instead of falling to `default:`. See [codebase/1404.md](../codebase/1404.md),
[codebase/1405.md](../codebase/1405.md), [codebase/1410.md](../codebase/1410.md).

Every claude-derived field is truncated **at construction**, mirroring `maxUnrecognizedRaw`'s
cap-at-construction precedent, with each cut named in `TruncatedFields`. The two scalar events share
`maxTaskFieldID` (256) / `maxTaskDescription` (4096) / `maxTaskPatch` (4096). `BackgroundTaskRoster`
needed a second, genuinely new dimension: the `tasks` array's **length** is claude's to choose, so a
per-entry text cap alone leaves the event's total size unbounded. It is bounded in both dimensions —
`maxTaskRosterEntries` (8) on the entry count, with overflow reported as `DroppedTasks` on the event
rather than a per-entry field, and `maxTaskFieldID`/`maxTaskRosterDescription` (512 — a smaller budget
than the scalar `Description`'s 4096, because this is the one field in the family whose budget is
multiplied by a count claude chooses) on each entry's text, reported per entry in that entry's
`TruncatedFields`. That forced a qualification of the family's stated single-worst-case doctrine
(`maxTaskPatch`'s comment: one number a reader can hold) — an aggregate variant cannot share a scalar
variant's worst case unless its cardinality is 1, so the doctrine now reads one worst case **per shape**:
the scalar pair ≤ ~4.9 KB, the roster ≤ 8 KiB (12.5% of the 65519-byte v2 application-envelope cap),
rather than forced to fit or silently abandoned.

claude's `session_id` and `uuid` are deliberately not carried by any of the four — absent from the
decode targets themselves (a field never declared cannot leak), enforced further by a reflection sweep
in each mapping test. No terminal/finish event is ever synthesized for a background task: the parser
holds no roster and no per-task memory, and a task's disappearance from a later roster — the only
available finish signal — has never been observed, so `BackgroundTaskRoster` reports the snapshot and
nothing more. (`thinkingSinceEmit`, #1385's token counter, doesn't change this refusal — it remembers no
task and no roster, only a count reset at the turn boundary.) Field mapping and cap arithmetic for the
three text-bearing events are built from the same committed capture (#1260), never a hand-built line; the
two bounds `BackgroundTaskRoster` needs are proven by lines synthesized to exceed them, since the
capture's single 212-byte, one-entry roster is far under either cap. `thinking_tokens`' bound is proven
the same way — the capture's bursts (126–197 tokens of delta) prove the bound's *ceiling*, but crossing
it repeatedly needs synthesized lines. See [codebase/1380.md](../codebase/1380.md),
[codebase/1382.md](../codebase/1382.md), [codebase/1381.md](../codebase/1381.md),
[codebase/1385.md](../codebase/1385.md).

**Content blocks are held as `[]json.RawMessage`, decoded per block.** This is load-bearing rather than
tidying: `streamBlock` declares only the fields the mapping reads, so decoding straight into it would
discard exactly the unknown fields an unrecognized block exists to show, and re-marshalling afterwards
would lose them. It also turns a block that fails to decode into a surfaced event rather than a silent
skip. `Unrecognized.Raw` is truncated to `maxUnrecognizedRaw` (16 KiB) **at construction**, so an
oversized payload never enters the event stream or any log; it is a `string` rather than
`json.RawMessage` because a truncated blob is no longer valid JSON.

Mapping logic mirrors (not imports — `mapper.go`'s helpers are unexported and keyed on tui-driver types)
[`turnbridge/mapper.go`](turnbridge-package.md). Two deliberate divergences: (1) a stream-json
`assistant` event carries a *whole message* that may hold several content blocks (`mapper.go` sees one
block per JSONL line), so the parser iterates `message.content` and emits one event per block,
preserving order; (2) `tool_use` input is carried through as claude's already-decoded
`json.RawMessage` verbatim (`ToolStart.RawInput`) rather than `mapper.go`'s map-re-marshal — one fewer
parse, and it preserves the original key order for what is an opaque pass-through field.

**Turn-stateless in everything that describes claude's output; one token counter as of #1385.** The
parser holds no turn counter, no `awaiting` flag, no transcript, and remembers nothing any line *said* —
every mapping but one is a pure function of the line it reads. The one exception is
`thinkingSinceEmit` (above), a plain accumulated-delta counter, not a memory of content. The *only* turn
boundary is a `result` line: it is where `thinkingSinceEmit` resets, unconditionally, and no other line
type can create, reset, or leak state across one — this is what keeps zero cross-turn bleed structural
rather than tracked, proven by a headline round-trip test driving 3 turns with distinct per-turn markers
over one held-open `Stdin()` handle. Per the T1 spike (#1075): `system/init` fires **once per turn**, not
once per session, and the session id is constant across turns of one process — `init` is still correctly
left out of the reset logic; giving one accumulator two reset boundaries to keep agreeing would cost more
than the single `result` boundary already buys. A child that dies without a `result` leaves a residual
`< minThinkingTokensPerEvent` behind on a long-lived parser (one per session, not per turn) — bounded and
named, not a second reset path.

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
`request_id` is locally minted by a per-`Runner` `atomic.Uint64` (`nextControlID`, stringified,
monotonic from 1; renamed from `nextInterruptID`/`interruptSeq` by #1603 — see below), never
caller-supplied; this ticket writes the id but never reads the
`control_response` ack, so uniqueness-within-the-runner's-lifetime is sufficient — no `crypto/rand`/UUID
dependency. `w == nil` (no live child) is checked first and returns `ErrNoLiveChild` with zero bytes
written — the same safe-no-op contract `WriteTurn` holds — so `Interrupt()` can't panic or partial-write
when called against an idle runner. Small enough (`<PIPE_BUF`) that one `write(2)` can't interleave with
a concurrent `WriteTurn` line on the same fd — the package's existing single-writer-per-syscall
discipline, not a new one. `Interrupt` is a **concrete method on `*Runner`, deliberately not added to
`sessions.Runner`** (kept un-widened per #1077) — mirrors how `*supervisor.Supervisor` encapsulates
`SendEsc` (#726) off the interface; the interrupt *routing* sibling (#1121) reaches it via its own
narrow interface or a type assertion. See [codebase/1120.md](../codebase/1120.md).

**Bypass revocation send primitive (#1603).** `(*Runner).RevokeBypass() error` writes a single
structured `control_request` line —
`{"type":"control_request","request_id":"<id>","request":{"subtype":"set_permission_mode","mode":"default"}}`
— onto the live child's held-open stdin, dropping a running child's bypass posture with **no
respawn** (#1595 measured this live against claude 2.1.220: `success` ack, next `init` reporting
`permissionMode: default`, and turn-2 behaviour matching a `default`-launched control child
exactly). `WriteBypassRevocation(w io.Writer, requestID string) error` (`envelope.go`) is the
free-function marshal+write half, mirroring `WriteInterrupt` field-for-field.
`controlRequestInner` — previously carrying only `Subtype` — gained a second field, `Mode`
(tagged `mode,omitempty`), declared **after** `Subtype` so the wire order matches the measured
line; `omitempty` keeps every interrupt line byte-identical to before (`TestMarshalInterruptEnvelope`'s
`want` is unmodified and is the sole detector if that tag is ever dropped). **Neither
`WriteBypassRevocation` nor `RevokeBypass` takes a mode** — no parameter, field, or option
anywhere on the surface selects one. This is deliberate: the opposite direction (granting bypass
over this channel) would be a privilege escalation reachable over the daemon's own stdin, and
claude refuses it anyway on the launch argv (#1595). Re-granting bypass stays on the
`Restart(newArgs)` respawn path. `request_id` now comes from `nextControlID`, the renamed,
**shared** counter (`interruptSeq` → `controlSeq`) — one sequence, not one per subtype, because
`request_id` must be unique across all in-flight control requests on the stream, not merely within
one subtype. `RevokeBypass` is a concrete method on `*Runner`, deliberately not on
`sessions.Runner`, same discipline as `Interrupt`; the session-layer wiring (#1604) reaches it via
a type assertion. No production caller exists yet — that is #1604's scope, not this ticket's. The
`control_response` ack (no reader needed — see the event-catalog row above, #1500) is unaffected.
See [codebase/1603.md](../codebase/1603.md) and
[set-permission-mode-inband-probe.md](set-permission-mode-inband-probe.md).

**Fresh-restart under a new id (#1124).** `RestartFresh(newID string)` rotates the runner into a fresh
session: the *next* spawn uses `--session-id <newID>` (a new transcript, no fork) instead of `--resume`,
and a later crash-respawn then `--resume`s `newID` — never the pre-rotation id. It reuses the live-restart
seam above, but rotates the **session id**, not the argv: a new `restartMu`-guarded pair,
`sessionID` (the mutable analogue of the construction-time, immutable `cfg.SessionID`; seeded from it in
`New`) and `rotatePending` (a one-shot flag), sit alongside `args`/`iterCancel` in the same field group.
`Run`'s spawn loop reads them via `beginSpawn()` — the `restartMu`-guarded accessor that snapshots
`(sessionID, rotatePending, args)`, clears `rotatePending`, builds the argv **and** publishes
`iterCancel`, all in ONE acquisition — instead of `r.cfg.SessionID` directly; when `rotatePending` is
true the returned `forceFirst` re-arms the Run-goroutine-private `firstRun` local to `true`, and the
same section has already fed that into `buildArgs` (itself untouched). The one acquisition is the
#1481 fix: splitting the id read from the `iterCancel` publish — as this loop did from #1124 until
#1481 — leaves a gap where a racing `RestartFresh` sets `sessionID`/`rotatePending`, reads a `nil`
cancel, cancels nothing, and the spawn launches under the pre-rotation id anyway (see the supervise
loop above). The existing `started`-gated `firstRun`
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
deliberately, since the `Parser` holds no `awaiting` flag (the half of "turn-stateless" #1385 left
unchanged; the `Parser` does now hold one rate-bound token counter, `thinkingSinceEmit`, unrelated to
what this tracker needs). The caller fans
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
`Run` now has it). A third leaf mutex `restartMu` guards `args` (the live spawn base argv — assigned in
exactly one place, `setArgsLocked`, called by both `Restart` and `SetSpawnArgs`), `iterCancel` (the
current spawn iteration's `context.CancelFunc`), and — since #1124 — the `sessionID`/`rotatePending` pair
`RestartFresh` rotates (see "Fresh-restart under a new id" below). Since #1481 `args` and the
`sessionID`/`rotatePending` pair are read, and `iterCancel` is published, by `beginSpawn` in a **single**
section per iteration; `clearIterCancel` drops the cancel once the iteration ends. That teardown accessor
takes no argument deliberately — publishing a non-`nil` cancel outside `beginSpawn`'s section is precisely
the #1481 defect, so the API cannot express it, and a future re-split has to add the parameter back before
it can reintroduce the window. `Restart(args []string)` installs through `setArgsLocked` inside its
existing single `restartMu` section, sends a non-blocking hint on a buffered(1) `restartCh` (coalesces
rapid restarts to one relaunch with the newest args), and cancels the current `iterCancel` — since #1481
that cancel goes live as soon as an iteration's spawn setup runs, before its child necessarily exists, so
a racing `Restart` can also catch a not-yet-launched iteration: the cancel fails that iteration's
`cmd.Start`, and the immediate relaunch that follows observes the swapped `args` — mirroring
`supervisor.Restart` byte-for-byte in shape. `Restart` touches only `restartMu`/`restartCh`/`iterCancel`,
never a `Pool`/`Session` lock, so `Pool.UpdateSettings` can call it after releasing `Pool.mu` with no
lock-order concern. Because `firstRun` is already `false` after the first successful spawn, a plain
restart always respawns via `--resume <sessionID>` — the conversation resumes rather than forking;
`RestartFresh` is the one path that re-arms `firstRun` to force a fresh `--session-id` spawn instead.

**`SetSpawnArgs(args []string)` — the swap without the kill (#1580).** `Restart` fuses two operations:
installing the next spawn's argv, and ending the live child so `Run` relaunches under it. `SetSpawnArgs`
is the first half alone — it calls `setArgsLocked` under one `restartMu` acquisition and returns, sending
no `restartCh` hint and never touching `iterCancel`, so a running child is left alone and the swap lands
on whichever spawn comes next (a crash-respawn, or an evict → `Activate`). It is the third racer
`beginSpawn`'s single-acquisition doc enumerates, and the weakest: one acquisition like the other two, and
it writes `args` only, so it cannot reach the forbidden state (a live child under a pre-rotation id with
no live iteration cancel) — that state is defined over `sessionID`/`rotatePending`/`iterCancel`, none of
which it touches. Declining to call `Restart` is not a substitute for calling this: it loses the swap
outright, and the next spawn silently re-execs the stale argv. The argv is installed **verbatim** — no
validation, no shaping; that stays upstream in `Session.spawnArgs` (`claudeSettingsArgs` enforces the YOLO
fail-safe there), and construction-time shaping (`stripSessionIDFlags`, `withApprovalArgs`, both
construction-only) is **not** reapplied on this or any post-construction install path. It is on
`sessions.Runner` (unlike `Interrupt`/`RestartFresh`/`BeginRotation`, which stay off it and are reached by
capability type-assertion) because there is exactly one production implementation — `streamRunner` — plus
five test doubles, all in this repo, so widening is compile-checked across the whole set. Its one
production caller is `Pool.UpdateSettings`' in-band branch (#1581), which installs the recomposed argv
through it and then delivers the change as a `/model` / `/effort` command instead of respawning. See
[codebase/1580.md](../codebase/1580.md).

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

**Non-blocking send, class-aware drop since #1496.** `sinkFor`'s closure runs on claude's stdout forwarder
goroutine (the same one `os/exec` drives the Parser's `Write` from); a blocking send on a full channel would
wedge the child. Before #1496 a full channel dropped the newest event of **any** class, `turnevent.TurnEnd`
included — see [§ Class-aware fan-in reserve (#1496)](#class-aware-fan-in-reserve-1496) below for why that
was an ADR 025 violation and how it's fixed. Today only the droppable class is refused once the channel
reaches `droppableCap`, and only that drop Debug-logs content-free (`event`, `kind`, `session_id` only —
never `ev`'s assistant/thought/tool content). The channel is **never closed** (a Parser may outlive the
drain during shutdown; the drain stops on `ctx`, not on channel close, so a send-on-closed panic is
structurally impossible).

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
sid)` — the same resolver `session_transition` frames use, `relay.go:784`), keyed by conversation (not
session) so a `/clear`-rotated session's late events still land under `SessionHistory`'s match. An
unresolvable or empty-string conversation id is simply not tracked (never under an empty key — that would
both wedge and collide with the "unknown conversation" answer).

The opener set is a **whitelist**: `ThoughtChunk`/`TextChunk`/`ToolStart`/`ToolUpdate` add the conversation,
`TurnEnd` (either stop reason — `resultTurnEndReason` sends both through one parser arm) deletes it,
everything else (`Stall`/`ApiRetry`/`Compacting`/`Unrecognized`, and any future variant) is a no-op.

**The whitelist has now been vindicated by a real case.** `Stall`/`ApiRetry`/`Compacting` are tui-driver
signals this sink's only producer never emits, so they are asserted at the unit tier only, fed directly.
`Unrecognized` is different: it **is** reachable — the parser emits it for any claude output outside the
measured known-ignored list — and it reached this tracker correctly **without one line of change here**,
because a new variant falls to the default. A blacklist ("anything that isn't `TurnEnd` opens a turn")
would be behaviourally identical through the older sink and would have wedged every conversation that met
an unknown message: the turn would open, and no turn end would ever follow, because we could not
understand the message that opened it.

Concurrency: one mutex guards one `map[string]struct{}` plus a `chan struct{}` "generation" broadcast,
closed-and-replaced under the same lock as any membership mutation. `WaitIdle` captures that channel and
re-checks membership under one lock acquisition (splitting the two reintroduces a lost-wakeup race), then
selects on it against `ctx.Done()`. `Busy`/`WaitIdle` are callable from any goroutine; `observe` is called
only from the drain goroutine and inherits its single-writer invariant, though the type is self-synchronised
regardless. Existence-oracle discipline (#1101 posture): `Busy`'s signature is `bool`-only — no error, no
second `found` bool — so a foreign conversation id is indistinguishable from an idle one in both value and
code path.

**Shipped unwired.** At #1201's landing no production caller read `Busy`/`WaitIdle` — `observe`'s
`nil`-receiver no-op is what let the 7 pre-existing drain-test call sites take a bare `nil` for the new
parameter instead of each constructing a tracker. That is no longer true: #1199 (below) is the
inbound-delivery consumer both readers were built for. The parameter stays the concrete `*turnBusyTracker`,
never an interface (a typed-nil in an interface field would be non-nil at the interface level and route
past the nil guard into a nil-map read — the `screenSnapshotterOrNil` hazard, `relay.go:406-421`).

**Known gap, recorded in the tracker's own doc comment, not just here:** #1201 shipped with the clear
event-driven only (`TurnEnd` on the fan-in). #1202 (below) closed the session-teardown half of that gap.
One path remains open: a child that dies mid-turn and respawns, firing no pool transition and no `result`
line for the abandoned turn. #1206 (below) added the runner-side seam that makes that exit observable;
#1209 (below) added the fan-in lane that turns such an exit into a clear, correctly ordered against the
dead child's already-pushed events — still shipped **unfired**. #1210 (open, blocked-by #1209) is the
wiring slice that assigns `streamsup.Config.OnChildExit` in production; the gap stays open until #1210
lands. See [codebase/1201.md](../codebase/1201.md).

### Session-teardown clear (#1202)

A session torn down mid-turn — a `/clear` rotation (`ReasonClear`) or an idle/cap eviction
(`ReasonEviction`) — never produces the abandoned turn's `result` line, so `observe`'s `TurnEnd`-only clear
alone would wedge the conversation busy forever. `turnBusyTracker.clearForSession(sessionID string)`
(`cmd/pyry/stream_turn_busy.go`) closes that gap: nil-receiver-safe (mirrors `observe`), resolves
`sessionID → conversationID` via the tracker's own injected closure **outside** `t.mu` (same lock-order
rule `observe` follows), and on an unresolved session logs `stream_turn.clear_unresolved` (`session_id`
only — the resolved `conversation_id` is deliberately withheld) and returns without mutating. The
membership mutation itself — resolve-then-delete-then-broadcast — was extracted out of `observe` into a
shared `setBusy(conversationID string, open bool)` so both feeds use one copy of the close-and-replace
protocol `WaitIdle`'s check-and-subscribe atomicity depends on, rather than a second hand-written copy of
it.

**Session-keyed, not conversation-keyed, on purpose.** A `clearConversation(convID)` shape would be a
shorter call chain but would accept a conversation id from anywhere, retiring the type's own `SECURITY`
note that the key "is never taken from the wire." Session ids are minted solely by `internal/sessions`'
own lifecycle events, so keeping the clear session-keyed keeps that invariant intact for this feed too.

**Wiring: composed onto the pool's single-valued `TransitionObserver` slot**, not a second install.
`SetTransitionObserver` (`internal/sessions/transition.go`) is a plain assignment — installing twice would
clobber the incumbent wire emitter — so `startSessionTransitionStreamV2` (`cmd/pyry/session_transition_v2.go`)
now composes: `emitter.Enqueue(t)` first (unconditional, non-blocking, so every transition that reached the
emitter before this slice still reaches it, timed identically), then `transitionClearsTurn(t)` — a 3-line
helper that delegates to `toWirePayload`'s existing closed reason switch rather than duplicating it, so an
unknown/future `TransitionReason` clears nothing — and `busy.clearForSession(sid)` on a hit.
`transitionClearsTurn` returns `NewSessionID` (the conversation's live `CurrentSessionID` for both reasons,
per `toWirePayload`'s existing semantics), not `PreviousID` — that's `conversationForSession`'s *primary*
match, so the clear doesn't depend on `RebindSession`'s `SessionHistory` append or the rebind-before-fan-out
ordering the way a `PreviousID`-keyed clear would.

`startSessionTransitionStreamV2` gained a `busy *turnBusyTracker` parameter, always the concrete pointer
(never an interface, same typed-nil hazard `observe`'s doc names). `cmd/pyry/relay.go` hoists the tracker's
declaration above the `w.streamSink != nil` branch so PTY mode — where no tracker is ever constructed —
still installs the composed observer, just with a nil `busy`; `clearForSession`'s nil-receiver guard is
what makes that safe rather than a nil-pointer panic on the pool's lifecycle goroutine.

**The clear runs synchronously**, on the goroutine that fired the transition (the pool's lifecycle
goroutine for eviction, the rotation-watcher goroutine for `/clear`) — satisfying the observer contract's
"MUST NOT block" without a buffered hand-off, because the work is one registry-mutex-guarded slice copy, a
map delete, and a `close()`, and the *same* goroutine already pays a full atomic write (including fsync)
one line earlier on the `/clear` path (`rebindConversation` → `Save`). A resolve-then-mutate this cheap
doesn't need the async escape hatch `WaitIdle` exists to provide for a slower consumer.

**The rotation edge #1201 flagged as this ticket's is unreachable, and the comment is corrected rather
than defended with a guard.** The suspected hazard: because `conversationForSession` matches
`SessionHistory`, a retired session's late `TurnEnd` could clear a turn its successor opened. It would
require two distinct producer tags resolving to the same conversation at once, and the tree admits no such
pair — a `/clear` re-keys **one** pool entry in place (same `Runner`, same process, same `Parser`), and the
Parser's sink tag is fixed at **runner construction** (`cfg.SessionID`, `streamsup_runner.go:105`) while
`RestartFresh` only rotates the runner's internal *spawn* id. Every id reachable via `SessionHistory`
therefore belongs to the same runner that continues under the successor id, tagging its events identically
either way. `SessionHistory`'s only production writer is `RebindSession`
(`internal/conversations/registry.go:241`), reached solely from `ReasonClear`
(`internal/sessions/transition.go:59,78`) — so eviction can't supply a second producer either, being
binding-neutral. One benign, non-bug case survives: between an eviction and the conversation's next
binding, the evicted id is still `CurrentSessionID`, so a late `TurnEnd` from the dying child resolves and
clears — the correct answer for that conversation, and idempotent with the teardown clear itself.

**Ships unwired**, same as #1201: nothing reads `Busy`/`WaitIdle` yet, no v2 frame changes, no delivery
behaviour changes. See [codebase/1202.md](../codebase/1202.md).

### Per-child-exit seam (#1206)

Split from #1203, alongside #1207 (open, blocked-by this ticket). #1201/#1202 can clear the turn-busy
tracker on a `TurnEnd` or a pool transition, but neither fires when a child simply crashes and the runner
respawns it: no `result` line for the abandoned turn, no pool transition (`RotateBootstrapForSelfHeal`
deliberately fires none). Nothing outside `internal/streamsup` could observe that exit at all — the only
escaping lifecycle state, `PhaseStopped`, means "`Run` has returned" and fires once, on permanent
shutdown, never on a crash-respawn.

```go
// exported, unlike onSpawn, because #1207's consumer lives outside this package
OnChildExit func()
```

One new field on `Config`, called once per **completed supervision iteration** — after the spawn attempt
finishes, before `Run` decides whether to shut down, relaunch immediately, or back off. The single call
site sits between `uptime := time.Since(start)` and `Run`'s post-spawn `if ctx.Err() != nil { return
ctx.Err() }`, i.e. above both the shutdown return *and* the `claude exited` log — `spawnAndWait` returns
from exactly one point and the loop only then branches on why the child is gone, so one unconditional call
there covers the crash, deliberate-restart, and shutdown paths without enumerating them. The two anchors
that look obvious instead — the `claude exited` log, or a `return ctx.Err()` site (there are three; only
one sits in the actual fire window) — each satisfy two of the three exit paths and silently miss the
third, which is why the shutdown-path test is the load-bearing one.

**Cardinality is per iteration, not per live child.** The call is unconditional, so it also fires when
`spawnAndWait` reports `started == false` — a pre-launch setup failure where no claude process ever
existed. Deliberate: no child existed, so no turn of this runner's can be open, and #1207's intended clear
is idempotent either way. A caller that needs "a real child died" cannot get that from this seam.

**Runs synchronously on the `Run` goroutine with no Runner lock held**, so a callback may call any Runner
method without deadlocking — but the state some of those methods return has not caught up yet at this
exact line: `State()` still reports `PhaseRunning` plus the dead child's PID (cleared only below the fire,
and never cleared on the shutdown path), and `Restart()` called from inside the callback skips the backoff
ladder entirely, since the fire precedes `drainRestart()`. Not a bug in this slice (no consumer yet), but a
sharp edge any future caller of this seam — #1207 included — has to read past the "no lock held" framing
to see. The callback must not block (it stalls the restart ladder) or panic (no `recover`, matching
`onSpawn`), and it is not a drain barrier: bytes the dead child already wrote may still be in flight in a
downstream sink when it fires, so a consumer needing ordering against those events must get it from that
sink, not from this callback.

**Ships unwired, with zero `cmd/pyry` diff.** Every `streamsup.Config` literal tree-wide is named-field, so
the new field is nil on the sole production construction path (`streamsup_runner.go`'s
`mapStreamsupConfig`) with no edit required. #1209/#1210 are the consumers — the exit lane (#1209, below)
and its production wiring (#1210, open) that together close the mid-turn crash clear the #1201/#1202 pair
couldn't reach. See [codebase/1206.md](../codebase/1206.md).

### Exit lane on the turn-busy fan-in (#1209)

Split from #1207 (itself the last child of the #1203/#1198 crash-clear lineage), alongside open sibling
#1210. #1206's `Config.OnChildExit` is explicitly *not* a drain barrier: `cmd.Wait` joins the stdout
copier goroutine and `Parser.emit` calls its sink synchronously (`parser.go:231-235`), so by the time the
callback fires, every event the dead child produced has been **pushed** onto `streamTurnSink.ch` — but not
necessarily **drained** by the separate drain goroutine reading that 256-slot buffer. A clear delivered on
any lane other than that channel could land before the drain processes buffered openers the crashed child
already emitted, re-marking the conversation busy with the clear already spent and no further exit coming.
This slice closes that ordering hole by putting the clear signal **on the fan-in itself**: FIFO with a
single reader, so it cannot be overtaken.

`streamTurnEnvelope` gains an explicit `exit bool` field — never a nil `turnevent.Event` used as a
sentinel, since `eventKind(nil)` returns `"unknown"` rather than failing (`interactive_turn_v2.go:419-421`),
which would make a missed nil-check silent rather than loud, and would make the exit signal a value of the
same type `Handle` accepts, retiring "Handle cannot receive a non-event" as a type-level fact.
`streamTurnSink.exitFor(sessionID string) func()` mirrors `sinkFor`'s non-blocking `select`/`default` send
— same drop-newest-on-full behaviour — but is a deliberately separate closure with its own diagnostic: the
drop is logged at **`Warn`** (`sinkFor`'s is `Debug`) with exactly `event: "stream_turn.exit_sink_full"` and
`session_id` — no `kind`, mirroring the existing content-free `clear_unresolved` shape. The asymmetry is
the point: a dropped ordinary event is a lost delta, invisible at the default `LevelInfo` on purpose; a
dropped exit is a conversation that (once #1210 wires a producer) stays busy forever, which is degraded
operation and must be visible by default.

The drain's `sink.ch` arm handles `env.exit` as its **first** statement —
`busy.clearForSession(env.sessionID); continue` — ahead of `observe`, the active-session gate, and
`emitter.Handle`. Each position is load-bearing: before `observe`, because an exit carries no event to
route through the event path; before the gate, for the same reason the tracker itself is fed before it —
the gate would otherwise drop a background conversation's exit, and background is the common case for a
crash; before `Handle`, which (combined with the explicit field) keeps `Handle` structurally unable to
receive a non-event. `clearForSession` (`stream_turn_busy.go:240`, extended in #1202) is called **as-is** —
no second session→conversation resolution, no second copy of the membership-mutation protocol — so it
inherits the nil-receiver no-op and the fail-closed `clear_unresolved` skip on an unresolvable session for
free; that is why this slice's own drop diagnostic withholds the conversation id (the sink closure holds no
resolver and structurally cannot name one).

**No new goroutine.** The clear runs inline on the drain goroutine — the same single reader/writer
`observe` already uses — so this feed is serialised against the event feed by construction rather than by
the tracker's mutex. A deferred or goroutine-dispatched clear would satisfy the positive ordering test
(`[opener, exit]` → idle) but fail the negative one (`[exit, opener]` → busy): the test that catches it
barriers on a *third*, later envelope rather than on the absence of an effect, since with the exit arriving
first a goroutine-dispatched clear is a harmless no-op regardless of scheduling (see
[codebase/1209.md](../codebase/1209.md) for the mutation-testing writeup).

**Fired in production since #1210.** `newStreamRunnerFactory` assigns
`streamsup.Config.OnChildExit = sink.exitFor(cfg.SessionID)` one line below the `sinkFor` install
(`streamsup_runner.go`), bound from the same `cfg.SessionID` — which is what keeps the two lanes' session
tags identical by construction. A conversation whose claude child dies mid-turn now returns to idle: no
`TurnEnd` for the abandoned turn and no pool transition are involved, the two feeds that are structurally
silent on that path. The tracker itself stays unread by any delivery path and no v2 frame changed — the
lane closes the crash-clear gap, it does not open a consumer. See [codebase/1210.md](../codebase/1210.md)
for the wiring and its structural (no-runtime-check) ordering argument; [codebase/1209.md](../codebase/1209.md)
for the lane itself.

### Class-aware fan-in reserve (#1496)

`sinkFor`'s drop-newest was, until #1496, blind to class: on a full 256-slot channel it dropped whichever
envelope arrived next, `turnevent.TurnEnd` included. `pushQueue.enqueue` (`internal/relay/v2session_modal.go`)
— the precedent `sinkFor`'s own comment cited — is deliberately class-aware and never drops a control
event, per ADR 025 § Backpressure's *"control events (`modal_shown`, `turn_end`, `tool_*`) never drop."* The
fan-in was the one place in the stream-json path that promise didn't hold, and because `turnBusyTracker.observe`
has no `TurnStart` — only openers and a `TurnEnd` closer — a dropped `TurnEnd` didn't just lose one event of
fidelity, it left the mark permanently open: `waitIdleForDelivery` parks every later `send_message` until
`streamTurnHoldTimeout`, and `msgqueue` eventually gives up with `session_error`. This also meant the "no
reachable sequence leaves a conversation reported busy forever" claim `turnBusyTracker`'s own doc comment
and [codebase/1210.md](../codebase/1210.md)/[codebase/1199.md](../codebase/1199.md) record as SATISFIED was
false at the fan-in the whole time #1201–#1210 were landing — those tickets closed every *clear-side* gap
correctly; the gap #1496 found was upstream of all of them, in whether a `TurnEnd` reached `observe` at all.

**The fix reserves capacity rather than replacing the channel.** A channel producer can't inspect or remove
a queued element without receiving it — mirroring `pushQueue.enqueue`'s slice-and-mutex shape literally was
rejected for exactly that reason (breaks the fan-in's documented sole-reader invariant). Instead,
`newStreamTurnSink` computes an unexported `droppableCap` once (`buf - min(streamTurnSinkCloseReserve, buf/2)`,
32 slots reserved at the production `buf` of 256): the droppable class is refused once `len(s.ch) >=
droppableCap`, and a closing-class envelope always sends against the channel's **full** capacity. The
channel stays strictly bounded — the reserve partitions existing capacity, it adds none — so this is the
opposite trilemma trade from `pushQueue`: `pushQueue` yields strictly-bounded (soft-overflows control,
affordable because #911's per-conn in-flight gate bounds the excursion); the fan-in yields never-drop-control
instead, because its producer is claude's unrate-limited stdout with no analogous gate, and a soft overflow
there would be a memory-exhaustion vector rather than a bounded excursion.

**One classifier, two callers.** `turnMarkFor(ev) (turnMarkNone | turnMarkOpen | turnMarkClose)`
(`stream_turn_busy.go`) is `observe`'s extracted `switch ev.(type)`, now the sole definition of the
open/close split — both `observe` and `sinkFor` read it, so a future `turnevent.Event` variant added to
one side's set and not the other can't silently reintroduce this bug. The closing class is exactly
`turnevent.TurnEnd` and the fan-in's `exit` envelope (`streamTurnEnvelope.exit`, #1209); everything else,
including an unrecognized future variant, falls to droppable — the safe default, since a wrongly-reserved
variant only costs capacity while a wrongly-droppable *closer* is the only misclassification that wedges.

**Loss is narrowed, not made impossible — the residual is `Warn`, not `Debug`.** Past the reserve a
closing-class send can still lose the race (`sinkFor` logs `stream_turn.close_sink_full`; `exitFor`,
unchanged by this ticket, already logged `stream_turn.exit_sink_full`), both at `Warn` — content-free
(`event`, `kind` where there's a discriminant to name, `session_id`) and visible at the daemon's default
`LevelInfo`, where the old `Debug`-only record was not. One documented risk: closing-class sends bypass the
watermark entirely, so a single session bursting more `TurnEnd`s than the reserve while the drain is stalled
could in principle crowd out a *different* session's closer — assessed as a narrowed version of pre-#1496
behaviour (today's bug crowds out everything, including the bursting session's own closer) rather than a new
vector, and left undefended since a fix would need the per-session accounting this design exists to avoid.

**Reconciling the "busy forever" claim.** It now holds up to the documented 32-slot reserve rather than
unconditionally: `turnBusyTracker`'s three closing feeds (`observe`'s `TurnEnd`, #1202's teardown clear,
#1210's exit lane) are unchanged by this ticket and still jointly exhaustive over *how* a turn closes — what
#1496 fixed is that the fan-in itself no longer discards the first of those three before it can be observed,
short of exhausting the reserve. See [codebase/1496.md](../codebase/1496.md).

### Delivery-seam consumer, mid-turn hold (#1199)

The first — and, as of this ticket, only — production reader of `Busy`/`WaitIdle`. It closes the actual
regression #1201 was built for: on `interactive_runner: stream-json`, `streamsup.Runner.WriteUserTurn`
returns as soon as the user-turn envelope is in the child's stdin pipe (`runner.go:283-285`), so
`internal/msgqueue`'s serial drain emptied as fast as it could write bytes instead of pacing on turn-end
the way `msgqueue.DeliverFunc`'s contract requires ("MUST block while claude is busy … that blocking IS
the drain's turn-end pacing", `msgqueue/queue.go:87-92`). The queued-backlog UI and the drop-before-drain
control were both regressed as a result — present on `pty` (which honours the contract via
`supervisor.WriteUserTurn`'s idle gate) and absent on `stream-json`.

Two new nil-receiver-safe, empty-key-safe methods on `turnBusyTracker`, both thin wrappers over the
existing primitives — no new fields, no new synchronisation:

```go
func (t *turnBusyTracker) waitIdleForDelivery(ctx context.Context, conversationID string, timeout time.Duration) error
func (t *turnBusyTracker) openForDelivery(conversationID string) (undo func())
```

`newInboundDeliver` (`cmd/pyry/main.go`, the `msgqueue.Config.Deliver` seam) calls both, in this exact
order, both between `Activate` and `WriteUserTurn`: `waitIdleForDelivery` first (bounded by the new
`streamTurnHoldTimeout`, 15 minutes), then `openForDelivery`, whose returned `undo` runs only if the
subsequent write fails. Two placement facts make this a **guarantee**, not a better race:

- **Before the write, therefore before `WriteTurn`'s `turncommit` claim.** `msgqueue.commitGate`'s own
  doc says the seam calls it "after the idle-gate wait and before the write" (`queue.go:419-425`) —
  holding the wait *outside* that claim is what keeps the queued head `draining && !committing` for the
  whole wait, the only window in which `msgqueue.Remove` drops it. The drop-before-drain control is
  delivered by placement, not by new removal logic.
- **The mark precedes the write, not follows it.** The tracker's ordinary opener feed (`observe`, fed
  from the parsed turn stream) is asynchronous; a mark placed after a successful write races it — on a
  fast child the turn's own `TurnEnd` can clear before the marking statement runs, leaving a stale mark
  nothing will ever clear. Marking first makes the ordering unconditional: no byte has reached the child
  yet, so no event for this turn can precede the mark.

**Determinism, the property the hold actually needs.** The msgqueue drain is serial per conversation, and
`openForDelivery` runs on that same drain goroutine inside the same `deliver` call that then writes. So
for messages *A* then *B* on one conversation: mark(A) happens-before `deliver(A)` returns happens-before
`deliver(B)` starts happens-before *B*'s `waitIdleForDelivery` reads membership — program order on one
goroutine, not a race against the child's speed.

**`setBusy` gained a `changed bool` return**, reported out of the single lock acquisition it already
takes. `openForDelivery`'s `undo` is live only when its own `setBusy` call actually moved membership —
guarding against a foreign opener (a `--resume` respawn replaying events is the plausible route) landing
in the gap between the wait returning nil and the mark; without the report, a naive undo could clear a
turn this delivery never opened and let the next message through unheld. Deriving the same answer from a
separate `Busy` read would reintroduce the TOCTOU this closes.

**`streamTurnHoldTimeout` (15 min, `main.go`, beside `inboundActivateTimeout`) bounds one delivery
attempt**, not the message — msgqueue's own retry (1s) and give-up (2m) bounds mean a turn that never
ends surfaces as a typed `session_error`/`CodeSessionBlocked` after ≈2× the timeout (≈30 min) rather than
holding the conversation forever. Deliberately no `Pending`-style exemption (contrast
`supervisor.ErrTrustModalPending`): that would reset the give-up streak forever, which a legitimate human
decision may need and a running turn should not.

**Trust boundary, restated honestly for this feed.** The tracker's SECURITY note previously claimed "the
key is never taken from the wire" — true of `observe`/`clearForSession`, which key off a daemon-resolved
session id. `openForDelivery`'s key is the conversation id from a `send_message` payload, which the
property still holds for, but for a narrower reason: that id passes two independent daemon-side gates
before it can reach the mark (`router.Route` at enqueue, `sessionRouter.resolve` again as `deliver`'s
first statement) — an unknown, unbound, or forged id returns before the mark, so the mark only ever
describes the conversation the daemon is about to write to, one the caller was already authorized to
write to.

PTY is unaffected: the tracker is nil there, both calls are no-ops, and `newInboundDeliver`'s body is
semantically identical to before #1199. See [codebase/1199.md](../codebase/1199.md).

**Forced-ordering test coverage across a `new_session` rotation (#1295).** The #1137 e2e had
failed twice at its M4 milestone with the same shape — rotation succeeds, the follow-up turn is
accepted, then zero bytes reach any child for the full 20 s deadline — consistent with a
delivery parked here (`waitIdleForDelivery`, bounded by `streamTurnHoldTimeout`, 15 min: far
outside the e2e's window, and silent while parked, matching the record). Rather than wait for
the ~1-in-N-per-week e2e to fire again, #1295 drives the ordering directly at this seam: park a
delivery in the hold, run the rotation underneath it (rekey the binding, tear the child down,
fire `clearForSession` keyed to the rotation's new session id), and check whether the clear is
what releases it. Forced 50/50 under `-race -count=50`, mutation-demonstrated — **the clear does
release the parked delivery**, and it lands in the post-rotation child and no other. That rules
out this seam as the M4 stall's cause on the ordering the test forces (pre-rotation turn events
and the transition arriving in order); it does **not** decide a straggler pre-rotation event
re-marking the conversation *after* the clear fires, which needs the drain's fan-in rather than
the seam alone and is filed as [#1298](https://github.com/pyrycode/pyrycode/issues/1298). See
[codebase/1295.md](../codebase/1295.md).

### Rotation-delivery gate (#1330)

Closes #1295's Open question 4 ("the clear-before-`RestartFresh` window … not structurally
excluded"). `startFreshRunner` ran `rotate()` — including `Pool.RotateForNewSession`'s
`ReasonClear` transition fan-out — to completion before calling `RestartFresh`, so the clear
reached clients, and released whatever the turn-busy tracker held, while the outgoing child
was still alive and the fresh one did not yet exist. A turn accepted in that ~4 ms window was
written into the doomed child; msgqueue reads a successful write as a commit and drops the
head, so the turn is gone — silently, since nothing errors.

**The invariant:** from `BeginRotation()` until the next child's stdin binds, `WriteUserTurn`
writes nothing and returns `ErrNoLiveChild`. Two new fields, `rotating bool` and
`rotateGen uint64`, live under the **same leaf mutex `mu` already guards `stdin` with** —
`mu`'s charter widened from "guards the stdin handle" to "guards which child, if any, may
receive a turn." That widening is the whole mechanism: `turnTarget() (w io.Writer, gated bool)`
answers "is a rotation armed?" and "which child's stdin?" as **one question under one
acquisition**. Two acquisitions — read the flag, then separately read `stdin` — reopen the
race at nanosecond width; a later "simplification" that splits them back apart would reintroduce
this ticket's defect invisibly.

`BeginRotation() (abort func())` sets `rotating = true`, bumps `rotateGen`, and returns a
disarm that only takes effect if no later arm has landed — the same generation-stamped shape
as `openForDelivery`'s undo (#1199, above). This is load-bearing, not defensive padding: two
overlapping `new_session` frames are an ordinary sequence (the e2e's own retry loop re-sends
every ~250 ms), and the loser's `rotate()` ordinarily fails `ErrSessionNotFound`
(`sessions/transition.go:120-123`) and runs its `abort()`. Unstamped, that would clear the
*winner's* arm while the winner's outgoing child is still alive, reproducing the defect on
demand from two frames. `setStdin` clears `rotating` in the same acquisition that publishes
the new handle; `takeStdin` deliberately leaves it alone, since the teardown it performs is the
*middle* of the window the gate covers, not its end — releasing on `RestartFresh`'s return
would only narrow the race, since that call returns after `cancel()` while the kill, `cmd.Wait`,
and the respawn all still run later on the Run goroutine.

`startFreshRunner` (`cmd/pyry/main.go`) arms the gate via `beginRotationOrNoop`, an **optional**
type assertion (`interface{ BeginRotation() func() }`) rather than a widened `RestartFresh`
dispatch case — so `new_session_routing_test.go`'s stub-based subtests keep exercising the same
dispatch shape rather than silently degenerating into the inert default arm. The existing
rotate-before-`RestartFresh` order (load-bearing for the double-rotation watcher skip-set) is
unchanged; the arm is inserted ahead of both statements. `streamRunner.BeginRotation()` forwards
it, the third concrete method reached by type assertion off the un-widened `sessions.Runner`
after `Interrupt` (#1120) and `RestartFresh` (#1124).

**The refusal record is logged at `Info`, deliberately diverging from the spec's `Debug`.** The
e2e's stability guard greps the daemon's *whole* captured stderr for the literal
`level=DEBUG`; since the refusal fires on essentially every rotation, a `Debug` record would
have satisfied that guard from the fix's own diagnostic, turning a 20-run stability measurement
into a near-tautology. `Info` keeps the "refused by the gate" vs. "no child yet" discriminator
without touching that guard. It names only the event and the runner's own session id — never
the `conversationID` parameter (accepted for interface conformance, otherwise unused) and never
payload bytes.

**Known accepted gap, not fixed (code review SHOULD FIX, below the merge-blocking threshold):
the release side is not generation-aware.** `setStdin` clears `rotating` for *any* child that
binds, not specifically the successor of the arm currently standing. Two overlapping frames can
still, in principle, hand a turn to a doomed child: frame 1 arms → rotates → `RestartFresh` →
`cancel()` returns with the respawn still pending → frame 2 arms and starts rotating → frame
1's respawn completes → `setStdin` clears `rotating` while frame 2's outgoing child is still
alive. Narrower than the pre-fix window by a large margin, and no failure of this shape has
been observed — accepted per the project's evidence-based-fix-selection convention rather than
built out speculatively. If evidence appears, the fix is a generation-aware clear: capture the
arm's generation at `RestartFresh` and compare it in `setStdin`, the same shape `abort` already
uses. See [codebase/1330.md](../codebase/1330.md) for the full review finding.

See [codebase/1330.md](../codebase/1330.md).

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
- [`codebase/1124.md`](../codebase/1124.md) — the fresh-restart-under-a-new-id mechanism slice (`RestartFresh`/`beginSpawn`).
- [`codebase/1168.md`](../codebase/1168.md) — `withApprovalArgs`, wiring the #1106 permission-approval flags onto the live interactive spawn.
- [`codebase/1201.md`](../codebase/1201.md) — the per-conversation turn-busy tracker (`turnBusyTracker`), fed from the drain before its active-session gate; shipped unwired, and closed off by #1202 (session teardown clear), #1206/#1209/#1210 (mid-turn respawn clear), and #1199 (the delivery-path consumer both readers were built for).
- [`codebase/1202.md`](../codebase/1202.md) — `clearForSession`, the session-teardown half of the tracker's clear (`/clear` rotation + eviction), composed onto the pool's `TransitionObserver` slot.
- [`codebase/1206.md`](../codebase/1206.md) — `Config.OnChildExit`, the per-child-exit seam on the streamsup `Run` loop (one field, one unconditional call above the shutdown return); split from #1203 alongside #1207, which itself later split into #1209 (the fan-in exit lane) and #1210 (the production wiring, landed).
- [`codebase/1209.md`](../codebase/1209.md) — `streamTurnEnvelope.exit` / `streamTurnSink.exitFor`, the fan-in lane that carries a child-exit signal ordered correctly against the dead child's already-pushed events; fired in production by #1210.
- [`codebase/1199.md`](../codebase/1199.md) — `waitIdleForDelivery`/`openForDelivery`, the inbound-delivery seam that holds a mid-turn send in the queue and marks the conversation busy before writing; the tracker's first production reader.
- [`codebase/1295.md`](../codebase/1295.md) — forced-ordering test proving this seam's hold is released by the `new_session` rotation's `clearForSession` and delivers to the post-rotation child; eliminates one M4-stall hypothesis, leaves the straggler-re-mark hypothesis ([#1298](https://github.com/pyrycode/pyrycode/issues/1298)) open and named the clear-before-`RestartFresh` window as an undecided Open question 4, closed by #1330.
- [`codebase/1330.md`](../codebase/1330.md) — the rotation-delivery gate (`BeginRotation`/`turnTarget`, `rotating`/`rotateGen` under `mu`) that closes #1295's Open question 4: no turn accepted once a `new_session` rotation has begun is written into the outgoing child.
- [`codebase/1380.md`](../codebase/1380.md) — the `system` subtype dispatch mechanism (`emitSystemSubtype`) and its first mapping, `system/task_started` → `turnevent.BackgroundTaskStarted`; fixes #1240's symptom. `task_updated` (#1382) and `background_tasks_changed` (#1381) extend the same mapped set.
- [`codebase/1385.md`](../codebase/1385.md) — the fourth arm, `system/thinking_tokens` → `turnevent.ThinkingProgress`, and the parser's first rate-bounded mapping / first piece of cross-line state.
- [`codebase/1404.md`](../codebase/1404.md) — the fifth arm and the first non-`system` mapping, `rate_limit_event` → `turnevent.RateLimited`, gated on `status` so a once-per-run report doesn't become a per-turn noise row; `ignoredLineTypes` is down to `{"system": true}`.
- [`codebase/1240.md`](../codebase/1240.md) — the symptom #1380 fixes the cause of: `turn_end`/`end_turn` and state `idle` while a backgrounded command claude started is provably still alive.
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
- Spec [`docs/specs/architecture/1206-streamsup-child-exit-seam.md`](../../specs/architecture/1206-streamsup-child-exit-seam.md) — the per-child-exit seam architect spec, including the two-wrong-anchors proof and the fire-window security review.
- Spec [`docs/specs/architecture/1295-forced-rotation-delivery-ordering.md`](../../specs/architecture/1295-forced-rotation-delivery-ordering.md) — the forced-ordering test spec, including the ranked open-questions list #1330 closes item 4 of.
- Spec [`docs/specs/architecture/1330-rotation-delivery-gate.md`](../../specs/architecture/1330-rotation-delivery-gate.md) — the rotation-delivery gate architect spec, including the two-frame concurrency proof and security review.
