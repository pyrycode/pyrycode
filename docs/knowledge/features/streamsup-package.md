# `internal/streamsup` — persistent stream-json child lifecycle

Stream-json sibling of [`internal/supervisor`](../architecture/system-overview.md) (the PTY path): supervises a **long-lived, multi-turn** headless `claude` child instead of hosting a screen. Where [`streamrunner`](streamrunner-package.md) spawns claude for one turn, writes the envelope, closes stdin, and exits, `streamsup` spawns claude **once per crash cycle**, holds its stdin open across many turns, and restarts it with the supervisor's backoff ladder on crash. Process lifecycle (#1087), the turn I/O boundary — envelope write + stdout→turnevent parser (#1088) —, the send-side turncommit gate (#1093), the receive-side idle/stall watchdog (#1094), satisfying the `sessions.Runner` seam (`State`/`WriteUserTurn`/`WaitForPTY`/`Restart` + a live-restart seam, #1097), the `newStreamRunnerFactory` constructor that builds a `streamRunner` from a `supervisor.Config` (#1109), the drain that fans its parsed turnevents into the unchanged `interactiveTurnEmitterV2` (#1098), the interrupt send primitive (#1120), the fresh-restart-under-a-new-id mechanism (`RestartFresh`, #1124), and the live permission-approval-flag injection onto the factory's spawn (`withApprovalArgs`, #1168) have shipped. **It is now live in production**: the `interactive_runner: "stream-json"` config toggle (#1081) selects `newStreamRunnerFactory` as `sessions.Config.RunnerFactory` and wires its drain at the relay leg — see [config-package.md](config-package.md) and [codebase/1081.md](../codebase/1081.md).

**No transcript *tailing* lives in this package.** That is the entire point of the stream-json path: it structurally removes the `<uuid>.jsonl` bind-latency race the PTY path fought (#528/#996/#989). The only filesystem canonicalisation `streamsup` performs is resolving `WorkDir` via `agentrun.ResolveWorkdir` before spawn (macOS `/tmp` → `/private/tmp`, the #989 symlink hazard) — no fsnotify, no JSONL path opened, watched, or read anywhere; the #1088 parser reinforces this by opening/watching/resolving no path at all. Since #1630 the package does touch a transcript path in one narrow way: an at-most-one-per-spawn `os.Stat` by id, gated on `Config.ClaudeSessionsDir` being set (see `useCreateForm` below) — an *existence* check, never a read, and never a directory scan.

## Public API

```go
type Config struct {
    ClaudeBin      string        // required; resolved path to claude
    WorkDir        string        // required; resolved via agentrun.ResolveWorkdir in New
    SessionID      string        // required; caller-minted claude session UUID
    ClaudeSessionsDir string     // optional; empty disables the #1630 by-id probe (see below)
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

## Held-open stdin — the deliberate inversion from `streamrunner`

`streamrunner.Run` (#390/#391) closes the child's stdin after writing one turn envelope — it's a single-turn primitive. `streamsup` is the opposite: `cmd.StdinPipe()` is opened before `cmd.Start()`, the write end is stored under a leaf mutex, and **it is never closed while the child is alive**. Turn envelopes are written onto it by the #1088 follow-on slice; this slice only owns the handle's lifecycle:

- On spawn: `setStdin(stdin, freshSeq)` publishes the new handle unconditionally, then — since #1482 — releases the rotation gate only if that spawn's `freshSeq` snapshot authorises it (see "Rotation-delivery gate" below), then the unexported `onSpawn(pid)` test seam fires (nil in production).
- On exit (crash or ctx-cancel teardown): `takeStdin()` clears the field first — so `Stdin()` reports "no live child" immediately — then closes the old handle **outside** the lock. `cmd.Wait()` has usually already closed the parent write end, so a broken-pipe / already-closed error here is expected; it's filtered through [`agentrun.ExitErrIsBenign`](agentrun-package.md) to avoid a spurious Warn (same discipline as `streamrunner`).

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

**#1630 added three tests, all pure insertions — the four argv-through-a-real-spawn pins and the three `buildArgs`-shape tests above stay byte-unmodified.** `TestUseCreateForm_ProbeDecidesIDFlag` (pure, table, composes `useCreateForm`+`buildArgs` over `t.TempDir()` fixtures with hand-written `<uuid>.jsonl` files, mtime-differentiated via `os.Chtimes` so a "newer unrelated transcript" row is deterministic rather than write-order-dependent); `TestRunner_BeginSpawn_FirstSpawnResumesExistingTranscript` (the wiring pin — proves `beginSpawn` actually feeds the probe's answer to `buildArgs` rather than passing `firstRun` straight through); `TestRunner_RestartFresh_ProbeDecidesPerSpawn` (the per-spawn pin — an *asymmetric* fixture, transcript present only for the pre-rotation id, is the one arrangement that discriminates a per-spawn decision from one memoised at construction; a fixture with both ids absent would pass either way). One general lesson from building the table: a row composing two functions (`useCreateForm` then `buildArgs`) only proves the override if its `latchCreate` column is set *against* the expected flag — a row where the latch already agrees with the probe's answer stays green under a mutant that deletes the probe entirely, so it reads as coverage while proving nothing about the override.

## Turn I/O — envelope write + stdout parser (#1088)

The turn I/O boundary fills `Stdin()`/`Config.Stdout` with two additive seams — no `runner.go` diff.

```go
var ErrNoLiveChild = errors.New("streamsup: no live child")

func WriteTurn(ctx context.Context, w io.Writer, prompt []byte) error

type Parser struct { /* sink, byte buffer, maxBuf, logger */ }
func NewParser(sink func(turnevent.Event), logger *slog.Logger) *Parser
func (p *Parser) Write(b []byte) (int, error) // io.Writer; set as Config.Stdout
```

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
line for the abandoned turn. #1206 (below) added the runner-side seam that makes that exit observable; #1209 (below) added the fan-in lane that turns such an exit into a clear, correctly ordered against the
dead child's already-pushed events — still shipped **unfired**. #1210 (open, blocked-by #1209) is the
wiring slice that assigns `streamsup.Config.OnChildExit` in production; the gap stays open until #1210
lands. See [codebase/1201.md](../codebase/1201.md).

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
daemon restarts. No cross-session disclosure (unmatched tag ⇒ dropped, not misdelivered). Follow-up: #1133. Confirmed live (not just by inspection) by the #1137 `new_session` e2e: after a stream rotation, a
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


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [`buildArgs` — the id-flag inversion that keeps the on-disk session stable](streamsup-package-buildargs-the-id-flag-inversion-that-keeps.md) — func buildArgs(base []string, create bool, sessionID string) []string
- [Supervise loop (`Run`)](streamsup-package-supervise-loop-run.md) — Mirrors `supervisor.Run`'s restart/backoff/resume shape, including the live-restart (`restartCh`/`iterCtx`) seam since #1097 (see…
- [Send half — `WriteTurn`](streamsup-package-send-half-writeturn.md) — **Send half — `WriteTurn`.** Mirrors `streamrunner`'s `userTurn`/`userTurnMessage`/ `userTurnContentText` envelope shape verbatim…
- [`system` maps per-subtype since 2026-08-07 (#1380) — the wholesale-drop design's first crack](streamsup-package-system-maps-per-subtype-since-2026-08-07.md) — **`system` maps per-subtype since 2026-08-07 (#1380) — the wholesale-drop design's first crack.** `status` and any never-seen subtype stay…
- [`system/thinking_tokens` → `turnevent.ThinkingProgress` is rate-bounded, not 1:1 (#1385)](streamsup-package-system-thinking-tokens-turnevent-thinkingpro.md) — **`system/thinking_tokens` → `turnevent.ThinkingProgress` is rate-bounded, not 1:1 (#1385).** claude's `estimated_tokens_delta` is…
- [Content blocks are held as `[]json.RawMessage`, decoded per block](streamsup-package-content-blocks-are-held-as-json-rawmessage.md) — **Content blocks are held as `[]json.RawMessage`, decoded per block.** This is load-bearing rather than tidying: `streamBlock` declares…
- [Firing the ask at spawn time (#1839)](streamsup-package-firing-the-ask-at-spawn-time.md) — **Firing the ask at spawn time (#1839).** A new `Config.RequestInitializeOnSpawn bool` gates one call to `RequestInitialize()` inside…
- [The per-entry byte budget's third dimension is bounded too, since #1821](streamsup-package-the-per-entry-byte-budget-s-third-dimension.md) — **The per-entry byte budget's third dimension is bounded too, since #1821.** `EffortLevels` is the family's first field that is a list…
- [Two further lessons, from tightening that same table's assertions to pin the #1828 collapse](streamsup-package-two-further-lessons-from-tightening-that-sam.md) — **Two further lessons, from tightening that same table's assertions to pin the #1828 collapse.** First, a spec's mutation predictions are…
- [Producing `turnevent.SlashCommandList` (#1877)](streamsup-package-producing-turnevent-slashcommandlist.md) — **Producing `turnevent.SlashCommandList` (#1877).** `emitModelList`'s rung 4 gained a second, independent gate on `commands`, below the…
- [The commands-only rung's emit lands (#1891)](streamsup-package-the-commands-only-rung-s-emit-lands.md) — **The commands-only rung's emit lands (#1891).** Rung 4 now calls the same `emitSlashCommandList` rung 5 does, with the identical…
- [Idle/stall watchdog — receive-side, emit-not-kill (#1094), arms from the send side (#1504)](streamsup-package-idle-stall-watchdog-receive-side-emit-not-kill.md) — Lifts the type-aware idle watchdog from `streamrunner` (`internal/agentrun/streamrunner/watchdog.go`) with one crucial divergence: the…
- [Satisfying `sessions.Runner` (#1097)](streamsup-package-satisfying-sessions-runner.md) — `*streamsup.Runner` gained the four methods [`internal/sessions.Runner`](sessions-package.md) requires beyond `Run`/`Stdin`, so a…
- [Constructing a `streamRunner` — `newStreamRunnerFactory` (#1109, extended #1098, #1168)](streamsup-package-constructing-a-streamrunner-newstreamrunnerfacto.md) — `cmd/pyry/streamsup_runner.go` also holds `newStreamRunnerFactory(sink *streamTurnSink, mcpApprovePath string) sessions.RunnerFactory` —…
- [Retaining the decoded model list for the session (#1840)](streamsup-package-retaining-the-decoded-model-list-for-the-session.md) — `emitModelList` (above) mints one `turnevent.ModelList` per child and hands it to the parser's sink — but that sink is…
- [Draining turnevents into the interactive emitter (#1098)](streamsup-package-draining-turnevents-into-the-interactive-emitter.md) — The turn I/O parser (#1088) emits neutral `turnevent.Event`s from its sink callback, but that sink is fixed where the runner is…
- [Session-teardown clear (#1202)](streamsup-package-per-conversation-turn-busy-track-session-teardown-clear.md) — A session torn down mid-turn — a `/clear` rotation (`ReasonClear`) or an idle/cap eviction (`ReasonEviction`) — never produces the…
- [Per-child-exit seam (#1206)](streamsup-package-per-conversation-turn-busy-track-per-child-exit-seam.md) — Split from #1203, alongside #1207 (open, blocked-by this ticket). 
- [Exit lane on the turn-busy fan-in (#1209)](streamsup-package-per-conversation-turn-busy-track-exit-lane-on-the-turn-busy-fan.md) — Split from #1207 (itself the last child of the #1203/#1198 crash-clear lineage), alongside open sibling #1210. 
- [Class-aware fan-in reserve (#1496)](streamsup-package-per-conversation-turn-busy-track-class-aware-fan-in-reserve.md) — `sinkFor`'s drop-newest was, until #1496, blind to class: on a full 256-slot channel it dropped whichever envelope arrived next,…
- [Delivery-seam consumer, mid-turn hold (#1199)](streamsup-package-per-conversation-turn-busy-track-delivery-seam-consumer-mid-turn-hold.md) — The first — and, as of this ticket, only — production reader of `Busy`/`WaitIdle`. 
- [Rotation-delivery gate (#1330)](streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md) — Closes #1295's Open question 4 ("the clear-before-`RestartFresh` window … not structurally excluded"). 
- [Resolve an in-flight tool call to its conversation (#1917)](streamsup-package-per-conversation-turn-busy-track-resolve-an-in-flight-tool-call.md) — A third feed, `inflight`, retains which `tool_use_id`s are in flight per conversation and reports membership only.
- [Related](streamsup-package-related.md) — see the document
