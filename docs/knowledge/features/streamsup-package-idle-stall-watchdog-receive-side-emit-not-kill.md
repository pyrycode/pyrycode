# Idle/stall watchdog — receive-side, emit-not-kill (#1094), arms from the send side (#1504)

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
func (w *Watchdog) UserTurnSent()              // #1504 — a user turn's bytes reached the child; arms + restamps
func (w *Watchdog) ChildExited()               // #1504 — the watched child is gone; voids what it owed
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

**The tracker starts not-awaiting, and the send side — not stdout — is the primary arm (#1504).** The
original `awaiting: true` construction-time default was lifted from `streamrunner`, whose runner *is*
the send side and so may assume an owed turn the instant it is built. `streamsup`'s watchdog is
constructed away from the send side: the interactive child spawns on `Activate`/`RestartFresh` and
emits `system`/`init` before any user turn exists — a message may not arrive for hours — and `init` is
activity-only, so the old default fired a false stall 240s after every spawn, and again after every
crash-mid-turn respawn. Because claude does **not** echo the delivered prompt back as a `user` line on
this surface (measured; see the "Two tiers" section below), starting `awaiting: false` with no other
change would have turned that false positive into a silent false negative — a turn that produces
nothing would never arm the watchdog at all. The fix is two explicit lifecycle signals instead:
`UserTurnSent()` (sets `awaiting=true` **and** restamps `lastEvent`, so an idle child's stale
`lastEvent` doesn't fire on the very next tick) and `ChildExited()` (sets `awaiting=false` and restamps
`lastEvent`, voiding whatever the departed child owed so the respawned child starts clean). Both are
lifecycle facts the still-unfiled wiring slice already holds — `UserTurnSent` after a `WriteUserTurn`
that returns `nil` (its two refusals, `ErrNoLiveChild` and `turncommit.ErrDropped`, write zero bytes and
must not arm it), `ChildExited` from the supervise loop's respawn point — but wiring them into
`Config.Stdout`/`Run` is deferred; this slice is additive to `watchdog.go` alone, no production caller.
One `Watchdog` per `Runner`, not one per child: `Config.Stdout` is fixed at `New` and never re-wired, so
a per-child watchdog would have no way to reach a respawned child's stdout. See
[codebase/1504.md](../codebase/1504.md).

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
