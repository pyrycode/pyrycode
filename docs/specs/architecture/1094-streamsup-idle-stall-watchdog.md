# Spec #1094 — `internal/streamsup`: type-aware idle watchdog with emit-not-kill stall hook

Fourth slice of `internal/streamsup`, built on the #1087 lifecycle / #1088 turn-I/O / #1093
turncommit-gate slices. It adds the **receive-side idle watchdog**: a poll goroutine that watches the
child's stdout line stream and, while claude *owes an assistant turn*, emits a `stall` signal when the
stream sits silent past a threshold. It **lifts** the type-aware `streamParser` + poll goroutine +
`watchdogTickFor` tick derivation from the one-shot runner
(`internal/agentrun/streamrunner/watchdog.go`) with **one crucial divergence**: the one-shot runner
**KILLS** on idle stall; this slice **emits and never kills**, so a legitimately-blocked-on-approval
turn (unbounded owed-silence, per the T1 spike #1075) is not mistaken for a wedge. It provides only the
timing **hook** — a content-free `func() bool` that tells the watchdog "a permission is pending" — not
the hook's producer (the approval flow, #1079/#1080).

Ships as one standalone, additive component that composes with #1087's existing `Config.Stdout` seam —
**`runner.go`/`parser.go`/`envelope.go` are not modified**. It ships unwired: the pool/relay/`cmd/pyry`
consumer, and the pending-permission signal producer, are follow-on slices (#1079/#1080, T4/T7).

## Files to read first

- `internal/agentrun/streamrunner/watchdog.go:54-234` — **the lift target.** Extract three things:
  (a) `watchdogTickFor` (54-63) — the idle/8-clamped-to-[5ms,5s] tick derivation, **lift verbatim**;
  (b) `streamParser` + `Write`/`feed`/`consumeLine`/`snapshot` (65-183) — the byte-buffer line splitter
  + the structural-`type`-only `awaiting`/`lastEvent` tracking, **lift the mechanics**; (c) `watchdog`
  + `startWatchdog` + `wait` (192-234) — the poll goroutine shape. Divergences below (§ Design).
- `internal/agentrun/streamrunner/watchdog.go:236-289` — the **KILL machinery** (`idleStallResult`,
  `idleStallUsage`, `writeIdleStallResult`). **DO NOT LIFT.** Read only to know precisely what to
  exclude: no synthetic `result` trailer, no `sawResult` field, no `cancel()` on fire.
- `internal/agentrun/streamrunner/watchdog_test.go:172-334` — test idioms to mirror: `fakeClock`
  (12-28), `TestStreamParser_AwaitingTransitions` (172-214), `TestWatchdogTickFor` (319-334). The
  Run-level `TestRun_SlowTool_NoFire` (97-121) is the type-aware "in-flight tool silence must NOT fire"
  precedent for this slice's AC1 no-fire test.
- `internal/streamsup/parser.go:19,41-64` — the existing **turn-stateless `Parser`** and its documented
  "single-writer, no mutex" invariant. The watchdog's tracker is its deliberate complement (stateful +
  mutexed, because the poll goroutine reads its state — see § Concurrency). **Reuse `defaultMaxParseBuf`
  (line 19) — same package, do not redefine.**
- `internal/streamsup/parser_test.go:13-26` — `discardLogger` and `collectEvents` helpers; reuse them.
- `internal/streamsup/runner.go:84-90` — the **`Config.Stdout io.Writer` seam** the watchdog composes
  onto (`Config.Stdout = io.MultiWriter(parser, wd.Writer())`, deferred to the wiring slice). Confirms
  the seam; no change to this file.
- `docs/specs/architecture/1088-streamsup-turn-io.md` — the sibling additive slice this one mirrors in
  shape: standalone component, zero `runner.go` diff, caller composes the seam. Read § "Composition" and
  § "Concurrency model".
- `docs/knowledge/features/streamsup-package.md` § "Out of scope" — pins #1094 as the receive-side
  watchdog, split from #1089 alongside the shipped #1093 send-side gate (no shared code, no blocked-by).
- QMD `second-brain` → `Streamrunner Interactive - Spike Findings` (T1, #1075) — § the approval
  round-trip **blocks claude synchronously until the approval tool answers** (8s approver delay → 11.5s
  turn): unbounded owed-silence, the measured justification for emit-not-kill. Also the type taxonomy
  (`system/init` per-turn, `assistant`/`user`/`result` transitions) the tracker keys on.

## Context

`streamrunner` (the one-shot stream-json runner) already ships a type-aware idle watchdog: it reads only
the structural `type` of each stdout line, tracks whether claude *owes an assistant turn* (`awaiting`),
and on `awaiting && idle > threshold` **kills** claude (cancels the child ctx) and writes a synthetic
`error_idle_stall` `result` trailer. Type-awareness is what makes it safe: an in-flight tool run
(`go test`, a slow `Bash`) is silence that happens *after* an assistant turn, when claude owes nothing,
so it never trips the watchdog.

The persistent `streamsup` runner needs the same idle detection but **cannot** kill on the same
condition. The T1 spike (#1075, claude 2.1.199) measured a pending permission approval blocking claude
synchronously until the approval tool answers — minutes of *owed* silence with `awaiting` true the whole
time. A watchdog that killed on `awaiting && idle` would destroy a legitimately-blocked-on-approval turn.
So this slice **diverges to emit-not-kill**: on the same idle condition it emits a `stall` signal and
never kills, however long the wait. To let a downstream consumer tell a genuine wedge from an
approval-wait, the watchdog consults a **pending-permission hook** (a local `func() bool`) at fire time
and carries its value on the emitted signal. This slice provides only that content-free timing hook; the
signal's *producer* — the internet-exposed approval flow (mcp-approve stdio tool, control-socket verb,
spawn-arg injection) — lands in #1079/#1080 and carries its own security review. That is why this slice
is **not** `security-sensitive` (see § Non-goals).

## Design

### Package layout (additive — no existing production file changes)

```
internal/streamsup/
  watchdog.go        stallTracker (io.Writer), Watchdog + poll goroutine, WatchdogConfig,
                     NewWatchdog, watchdogTickFor, shouldFire, idle/tick consts
  watchdog_test.go   tracker unit tests + shouldFire table + poll-goroutine integration tests
```

`runner.go`, `parser.go`, `envelope.go`, `backoff.go`, `reap.go` are untouched. `internal/supervisor`
is untouched (AC4 — trivially, since this slice never names it). No new imports beyond what the package
and streamrunner's watchdog already use (`bytes`, `context`, `encoding/json`, `io`, `log/slog`, `sync`,
`time`). The `#1087` `go list -deps … | grep internal/supervisor → empty` invariant is preserved.

### Two pieces: the type-tracker and the poll goroutine

The #1088 `Parser` is **turn-stateless** — it holds no `awaiting` flag by design. The watchdog therefore
**carries its own type-tracking state over the same stdout stream** (ticket § Context). Two pieces:

**1. `stallTracker` (unexported `io.Writer`)** — lifted from `streamParser`, minus the passthrough `dst`
and minus `sawResult`:

```go
type stallTracker struct {
    now    func() time.Time
    maxBuf int
    mu        sync.Mutex   // poll goroutine reads state the forwarder goroutine writes
    buf       []byte
    lastEvent time.Time
    awaiting  bool
}
func (t *stallTracker) Write(b []byte) (int, error)          // io.Writer; always (len(b), nil)
func (t *stallTracker) snapshot() (awaiting bool, last time.Time)
```

- `Write` appends to `buf`, consumes every complete `'\n'`-delimited line via `bytes.IndexByte`, keeps
  the partial remainder, drops it past `maxBuf` (reuse `defaultMaxParseBuf`) — the exact `feed`
  mechanics of `streamParser.feed`. **It is the terminal sink, not a tee** (no `dst` passthrough): the
  caller fans the stdout bytes to both the `Parser` and this tracker with `io.MultiWriter`, so `Write`
  returns `(len(b), nil)` (mirrors `Parser.Write`, so `io.MultiWriter` sees a full consume).
- `consumeLine` records every complete line as activity (`lastEvent = now()`, even an unparseable one),
  then decodes **only** `struct { Type string \`json:"type"\` }` and transitions `awaiting`:
  `assistant`→false (claude produced a turn; the tool-run silence that follows is expected);
  `user`/`tool_result`→true (a tool result came back; claude owes the next assistant turn); `result`→
  false (the turn is done); everything else (`system`, `rate_limit_event`, unknown) is activity only.
  **No content field is ever decoded, retained, or logged** (AC3).
- Constructed `awaiting: true`, `lastEvent: now()` — claude owes the first assistant turn once the caller
  has written the opening user envelope (matches `newStreamParser`). See § Open questions on the
  multi-turn turn-start flip.

**2. `Watchdog` (exported)** — owns the tracker, the poll goroutine, and the emit/hook config:

```go
type WatchdogConfig struct {
    Idle              time.Duration              // 0 → idleStall (240s)
    PendingPermission func() bool                // nil → always false (no permission pending) — the hook
    OnStall           func(pendingPermission bool) // the stall signal; nil → no-op guard
    Logger            *slog.Logger               // nil → slog.Default
    now               func() time.Time           // unexported test seam; nil → time.Now
}
func NewWatchdog(cfg WatchdogConfig) *Watchdog
func (w *Watchdog) Writer() io.Writer            // the stallTracker, for Config.Stdout composition
func (w *Watchdog) Start(ctx context.Context)    // launch the poll goroutine
func (w *Watchdog) Wait()                         // block until the poll goroutine has exited
```

The poll goroutine (lifted from `startWatchdog`, diverged) ticks at `watchdogTickFor(Idle)`; on each
tick it reads `awaiting, last := tracker.snapshot()` and applies the pure predicate

```go
func shouldFire(awaiting bool, last, now time.Time, idle time.Duration) bool // awaiting && now.Sub(last) > idle
```

**Divergence — emit, never kill (the whole point):**

- On the rising edge of `shouldFire` (latched once per stall episode — see below), it evaluates the hook
  `pending := cfg.PendingPermission != nil && cfg.PendingPermission()`, logs a content-free `Warn`
  (`idle_seconds` + `pending_permission` bool only), and calls `OnStall(pending)`. **It holds no
  `context.CancelFunc` and no process handle** — unlike `startWatchdog(ctx, …, cancel, …)`, the
  `Watchdog` has *no way to kill*. Emit-not-kill is enforced by the type surface, not just by a rule
  (belt-and-suspenders: the divergence is structural). No synthetic `result` trailer is composed
  (`idleStallResult`/`writeIdleStallResult` are **not** lifted).
- **Latch, edge-triggered once per episode.** Track a local `fired bool` in the goroutine: fire when
  `shouldFire && !fired`, then set `fired = true`; reset `fired = false` on any tick where `!shouldFire`
  (activity advanced `lastEvent`, or `awaiting` flipped false). So one contiguous idle-while-awaiting
  span emits exactly one `stall`; a later span emits again. The goroutine **does not return after
  firing** (contrast `startWatchdog`, which returns after the kill) — it keeps polling until `ctx.Done`,
  which is what lets it tolerate an unbounded pending-permission window (AC2 "however long").
- Exits cleanly on `ctx.Done()` (operator shutdown, or the child ctx the wiring slice passes);
  `close(done)` on exit; `Wait()` joins. No leak (CODING-STYLE "always clean up goroutines").

### The pending-permission hook — how it satisfies AC1 + AC2 without gating the emit

The hook does **not** gate whether `stall` is emitted; it annotates it. Both scenarios emit:

| Situation | `awaiting` | `PendingPermission()` | `shouldFire` past threshold | Watchdog does |
|---|---|---|---|---|
| Genuine wedge | true | false | yes | `OnStall(false)` — a wedge candidate the consumer may act on |
| In-flight tool run (AC1 no-fire) | false | — | **no** (`awaiting` false) | nothing |
| Blocked on approval (AC2) | true | true | yes | `OnStall(true)` — owed-silence; consumer must **not** kill, however long |
| Between turns (post-`result`) | false | — | no | nothing |

The hook is the "don't count this silence as owed" signal (ticket § Context): it flows through to the
consumer as `OnStall`'s `pendingPermission` argument, so #1079/#1080 can distinguish an approval-wait
(tolerate) from a wedge (act) — *this slice itself never kills in either case*. Gating the emit off while
pending is **rejected**: AC2 and the user story both say the watchdog *emits* a `stall` while a
permission is pending ("emits (never kills) while a permission approval is pending"); the distinction
lives in the argument, not in whether the callback fires.

### Composition (deferred to the wiring slice)

No change to `runner.go`. The consumer fans stdout to both the parser and the watchdog and starts the
poll under the runner's ctx:

```
parser := streamsup.NewParser(sink, logger)
wd     := streamsup.NewWatchdog(streamsup.WatchdogConfig{ OnStall: onStall, PendingPermission: pendingFn })
r, _   := streamsup.New(streamsup.Config{ …, Stdout: io.MultiWriter(parser, wd.Writer()) })
wd.Start(ctx); defer wd.Wait()
go r.Run(ctx)
```

## Concurrency model

- **Two goroutines touch tracker state → the tracker needs a mutex.** `os/exec` drives `Config.Stdout`
  through exactly one internal `io.Copy` goroutine, so `stallTracker.Write`/`feed` runs serially from
  that one forwarder (as with `Parser`). But the **poll goroutine** reads `awaiting`/`lastEvent` via
  `snapshot()` concurrently. This is the concrete reason the tracker locks where the #1088 `Parser`
  does not (the `Parser` has no second reader). Same lock discipline as `streamrunner`'s `streamParser`.
- **`OnStall` runs on the poll goroutine.** The watchdog makes no promise beyond "called serially, on
  the poll goroutine, once per stall episode." The consumer owns any synchronization it needs (a test's
  `OnStall` pushes to a buffered channel; a real consumer forwards to the relay / restart logic).
- **One clock seam for both pieces.** `now` (default `time.Now`) is threaded into *both* the tracker
  (`lastEvent`) and the poll's elapsed check (`now().Sub(last)`), so they never read two different
  clocks — a deliberate tightening over `streamrunner`, which mixes `p.now()` in the parser with
  `time.Since` in the poll (harmless there, but this keeps the state test deterministic).
- **Goroutine lifecycle.** `Start` spawns exactly one goroutine; it exits on `ctx.Done`; `Wait` joins on
  a `done` channel. `go test -race`-clean.

## Error handling

| Failure | Handling |
|---|---|
| Stdout line fails to `json.Unmarshal` | Counts as activity (`lastEvent` advances), no `awaiting` transition. One bad line never poisons later lines (each mapped independently) — mirrors `streamParser.consumeLine`. |
| Partial line exceeds `maxBuf` | Drop the accumulated partial (Debug-log byte count only), resume scanning at the next `'\n'`. Bounded memory against a hostile/buggy child. |
| `OnStall` is nil | No-op guard (like `Parser`'s nil-sink guard); the watchdog degrades to detection-only rather than panicking on a misconfiguration. A real watchdog supplies it. |
| `PendingPermission` is nil | Treated as always-false (no permission pending) — the genuine-wedge path. |
| `ctx` cancelled (shutdown) | Poll goroutine returns, `done` closes, `Wait()` unblocks. No stall emitted for a shutdown. |
| A spurious post-restart fire | Benign by construction: emit-not-kill means a false `stall(false)` costs nothing destructive, and `lastEvent` refreshes on the restarted child's first line. See § Open questions. |

Logging is **structural only** — `idle_seconds`, the `pending_permission` bool, byte counts, Go error
values — **never** any line content (AC3). No new error types; the watchdog returns nothing.

## Testing strategy

Table-driven, stdlib `testing`, `go test -race`. Reuse `discardLogger`. Scenarios (bulleted — developer
writes them in the project idiom):

- **`stallTracker` awaiting transitions (white-box, `fakeClock`).** Starts `awaiting=true`; `system/init`
  → activity only, still awaiting; `assistant` → `awaiting=false`; `user`/`tool_result` → `awaiting=true`;
  `result` → `awaiting=false`. Mirrors `TestStreamParser_AwaitingTransitions` minus the `sawResult`
  assertion.
- **`stallTracker` lastEvent advances (white-box, `fakeClock`).** Advance the fake clock, write any
  complete line (including an *unparseable* one), assert `snapshot()`'s `last` moved to the new fake now
  (compare via `time.Time.Equal`). Proves every line is activity.
- **`stallTracker` line buffering.** A line split across two `Write` calls reassembles (no premature
  transition on the partial); multiple complete lines in one `Write` all transition in order; a partial
  exceeding a shrunk `maxBuf` is dropped and scanning recovers at the next newline — mirrors the #1088
  parser-buffering test.
- **`stallTracker` is content-free (AC3), structural + behavioural.** A line with rich nested content
  (`assistant` with text/thinking/tool_use blocks, a `tool_result` with a body) transitions `awaiting`
  identically to the same line with empty content — the tracker reads only top-level `type`. Reviewer
  grep: `watchdog.go` references no `.Text`/`.Thinking`/`.Content`/`.Input`/`message` fields; its decode
  shape is `struct{ Type string }` only.
- **`shouldFire` pure table (AC1).** `awaiting=false, elapsed>idle` → **false** (in-flight tool silence
  never fires — the type-aware core); `awaiting=true, elapsed<=idle` → false; `awaiting=true,
  elapsed>idle` → true. No goroutine, no clock race.
- **Poll goroutine — genuine idle fires (AC1), integration, real short `Idle`.** `NewWatchdog{Idle:
  ~60ms, OnStall: →buffered chan}`; `Start`; feed a `system` line then stay silent → `OnStall` fires
  once with `pendingPermission=false` within a couple of idle windows. `watchdogTickFor(60ms)` derives a
  ~5ms tick, so it fires fast; assert via a `select` on the channel with a generous timeout.
- **Poll goroutine — in-flight tool silence does NOT fire (AC1), integration.** Feed an `assistant` line
  (`awaiting`→false), stay silent well past `Idle` → `OnStall` is **never** called. This is the
  type-aware guarantee (`TestRun_SlowTool_NoFire`'s streamsup analogue); assert the channel stays empty
  across several idle windows.
- **Poll goroutine — pending-permission hook, emit-not-kill (AC2), integration — the load-bearing hook
  test.** `NewWatchdog{Idle: ~60ms, PendingPermission: func() bool { return true }, OnStall:
  →chan}`; `Start`; stay silent for **many** idle windows (e.g. 300ms+, "however long the approval stays
  outstanding"). Assert: (a) `OnStall` fired with `pendingPermission=true`; (b) it fired **exactly once**
  (latched, not per-tick); (c) **no kill happened** — structurally the `Watchdog` holds no cancel/process
  handle (the `WatchdogConfig` has no cancel field to wire one), and the ctx the poll runs under is never
  cancelled by the watchdog. Contrast with the AC1 fire test (same silence, `pending=false`) proves the
  hook's value flows through and is the only difference.
- **Poll goroutine — latch re-arms per episode.** Fire once (silence), then feed a line (activity clears
  `shouldFire`), then go silent again → fires a second time. Confirms edge-triggered once-per-episode.
- **Clean shutdown, no leak.** `Start(ctx)`, cancel `ctx`, `Wait()` returns; `go test -race` clean.
- **`watchdogTickFor` table.** Lift `TestWatchdogTickFor` verbatim (240s→5s cap, 200ms→25ms, 1µs→5ms
  floor).
- **PTY path untouched (AC4), structural.** `internal/supervisor` and the existing `streamsup`
  production files have zero diff — reviewer confirms via the PR diff (additive: one new production file
  + one test file).

## Non-goals (this slice is intentionally NOT `security-sensitive`)

- **The pending-permission signal producer.** This slice provides the `PendingPermission func() bool`
  *hook* only. The approval flow that would return `true` — the mcp-approve stdio tool, the control-socket
  verb, spawn-arg injection — is the internet-exposed surface and lands in #1079/#1080 with its own
  security review. The hook here is a local, content-free `func() bool`: no untrusted parse, no key
  material, no event content inspected or retained (matches the not-sec rationale in
  `docs/knowledge/features/streamsup-package.md` and the #1088 receive-half review, which already
  covered the stdout parse boundary). Structural-`type` reads + a local sync gate are not a new trust
  boundary.
- **The KILL machinery.** `idleStallResult`, `idleStallUsage`, `writeIdleStallResult`, `sawResult`, and
  the `cancel()`-on-fire are **not** lifted — the divergence is emit-not-kill, and this slice synthesises
  no trailer and terminates no child.
- **Wiring.** No pool/relay/`cmd/pyry` consumer, no change to `runner.go`. The wiring slice composes
  `Config.Stdout = io.MultiWriter(parser, wd.Writer())`, supplies `OnStall`/`PendingPermission`, and
  decides what a `stall(false)` does (restart? surface to operator?) — none of that is here.

## Open questions

- **Multi-turn turn-start `awaiting` flip.** The tracker starts `awaiting=true` and flips it via stdout
  `type` transitions only. Within a turn this is exact (`assistant`→false, `tool_result`→true). For the
  *start of turn N+1* (after a `result` set `awaiting=false`), the flip back to true depends on whether
  claude echoes the user prompt as a stdout `{"type":"user"}` line — which the tracker already maps to
  `awaiting=true`. The T1 spike shows `user`/`tool_result` lines on stdout, so this is expected to be
  covered; if a live check finds the opening user prompt is *not* echoed, the wiring slice adds a
  `MarkAwaiting()` nudge called from `WriteTurn` (a stdout-blind seam). Not needed for this slice's ACs
  (each exercises a single awaiting episode) and deferred to the wiring slice, which owns the send side.
- **Post-restart stale state.** `Config.Stdout` (hence the tracker) is set once and reused across
  crash-restarts, so `awaiting`/`lastEvent` survive a restart; a stale `lastEvent` could momentarily
  satisfy `shouldFire` on the restarted child before its first line lands. Under emit-not-kill this is
  benign (a `stall(false)` costs nothing, and `lastEvent` refreshes immediately), so **no reset is added
  here** (Evidence-Based Fix Selection — no observed false-restart-fire, and killing is off the table).
  If the wiring slice finds cross-crash integrity matters, it adds a `Reset()` seam and calls it from
  `Config.onSpawn` — additive, no rework of this slice. Mirrors the #1088 code-review note on the
  parser's cross-restart buffer.
- **Heartbeat vs edge-trigger.** The watchdog emits once per stall episode (edge-triggered). If a
  downstream wants a periodic "still stalled" heartbeat during a long wait, that is a consumer timer, not
  a change here — kept edge-triggered to avoid spamming `OnStall` every tick.
