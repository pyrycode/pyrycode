# `internal/turnbridge` — event-stream bridge (producer)

The **producer half** of the Phase 2 structured-event bridge (EPIC #596,
[ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) § Phase 2
structured streaming). It drains the supervised claude session's unified
tui-driver `Events()` stream and maps each event into the neutral internal
turn-event model ([`internal/turnevent`](turnevent-package.md), #606). Landed in
#615.

The **consumer half** — mapping the internal model to v2 wire envelopes (#607)
and the capability-gated fan-out to phones — is **#616** and consumes this
producer's output. This package ships **with unit tests, deliberately unwired to
a live fan-out** (the standard "introduce the producer + tests; wire the consumer
in the next slice" pattern). With no consumer attached the producer is a no-op
beyond draining.

> #615 + #616 are the two halves of the originally-combined #608. Docs in #606 /
> #607 that say "the bridge (#608)" mean: producer = #615 (here), consumer = #616.

Dependency direction stays clean — `cmd/pyry → internal/turnbridge →
{tuidriver, turnevent}`. The package does **not** import `internal/supervisor`
(it defines its own `SessionHost` seam, which `*supervisor.Supervisor` satisfies
structurally) and does **not** import `internal/sessions` (the JSONL resolver is
injected). Package name reads naturally beside `turnevent`: `turnbridge` bridges
tui-driver events into the `turnevent` model.

- Spec: [`specs/architecture/615-event-stream-producer.md`](../../specs/architecture/615-event-stream-producer.md).
- Ticket record: [codebase/615.md](../codebase/615.md).
- Output model: [turnevent-package.md](turnevent-package.md) (#606).
- Wire target of the consumer: [protocol-package.md](protocol-package.md) (#607, interactive payloads).

## Files

```
internal/turnbridge/
├── producer.go        Producer lifecycle (drain + re-subscribe) + the live NewSessionSubscriber
├── mapper.go          mapEvent + pure helpers (the tui-event → turnevent type switch)
├── producer_test.go   drain / re-subscribe-across-restart / nil-OnEvent tests (fake Subscriber)
└── mapper_test.go     table-driven event→model + drop tests; toolResultText / toolKind / rawInput
```

Plus an additive `Session()` accessor on the supervisor (`internal/supervisor/supervisor.go`).

## The core/glue split

The producer is split into a **testable core** (drain + re-subscribe loop, driven
by an injected `Subscriber`) and **live glue** (the production `Subscriber` that
calls `Session.Events`). The split is what makes the lifecycle guarantees
unit-testable without spawning a real claude — a `*tuidriver.Session` can only be
`Spawn`ed, so the live subscriber is verified downstream (#616 wiring + the v2
e2e oracle), while this slice unit-tests the core via a fake `Subscriber` and the
pure mapper directly.

## Public API

```go
// Subscriber yields a live session's tui-driver event stream. The returned
// channel closes when that session ends (supervisor restart) or ctx is done.
// Returns a non-nil error ONLY on ctx cancellation; transient resolution
// failures are retried internally, so the channel-or-ctx-done contract holds.
type Subscriber func(ctx context.Context) (<-chan tuidriver.Event, error)

// SessionHost is the supervisor seam the production Subscriber drives.
type SessionHost interface {
    Session() *tuidriver.Session
    WaitForPTY(ctx context.Context) error
}

type Config struct {
    Subscribe Subscriber             // required; New errors if nil
    OnEvent   func(turnevent.Event)  // nil ⇒ no-op beyond draining (AC 4)
    Logger    *slog.Logger           // nil ⇒ slog.Default()
}

func New(cfg Config) (*Producer, error)            // err iff Subscribe == nil
func (p *Producer) Run(ctx context.Context) error  // outer re-subscribe loop
func NewSessionSubscriber(host SessionHost, resolve func(ctx) (path string, off int64, err error), tr *tuidriver.Tracker, log *slog.Logger) Subscriber
```

Four exported types — `Producer`, `Config`, `Subscriber`, `SessionHost`.

### `Run` — the outer re-subscribe loop

`Subscribe(ctx)` → `drain(ctx, ch)` → repeat. `Subscribe` blocks until a live
stream exists, so the loop is naturally paced (no busy-spin). Returns `ctx.Err()`
on cancellation — the only error `Subscribe` yields per its contract.
Re-subscribing after a channel close **is** "no leaked goroutine across a session
restart": the prior session's tui-driver merge goroutine already closed its
channel (its per-session ctx was cancelled — see the live Subscriber).

### `drain` — the inner loop

A `select` over `ctx.Done()` and the event channel:
- `ctx.Done()` → return (clean exit on cancel).
- channel closed → return (clean exit on session restart).
- event received → if `OnEvent == nil`, `continue` (drains the source, does
  nothing else — AC 4); else `mapEvent(ev)` → on `ok` call `OnEvent(te)`, on
  `!ok` `log.Debug("turnbridge: dropping unrepresentable event", "kind", ev.Kind)`.

### Output is a callback, not a channel

A single `OnEvent` callback (nil-allowed) models "no consumer = no-op beyond
draining" precisely and pushes backpressure / queueing / drop-policy to #616
(where ADR 025 § Backpressure puts it). It is set once at construction (one
downstream bridge — no dynamic re-attach, no concurrency on the field). The
callback runs on the single `Run` goroutine, so #616's callback **must not block
it indefinitely** — its own queue owns backpressure.

## The mapper (`mapEvent`)

A **pure function** — no logger, no I/O — so the drain owns the debug-log for
drops and the mapper stays trivially table-testable. `ok == false` means the
internal model has no representation for the event; the caller drops + debug-logs.

| `ev.Kind` | sub-condition (on `e := ev.Entry`) | result |
|---|---|---|
| `EventKindJsonlEntry`, `e.Type=="assistant"` | `ParseToolUse(e.RawLine) != nil` | `ToolStart` |
| ″ | else `AssistantText(e) != ""` | `TextChunk{MessageID, Text}` |
| ″ | else `thinkingText(e) != ""` | `ThoughtChunk{MessageID, Text}` |
| ″ | else | drop |
| `EventKindJsonlEntry`, `e.Type=="user"` | `ParseToolResult(e.RawLine) != nil` | `ToolUpdate` |
| ″ | else | drop |
| `EventKindJsonlEntry`, other `e.Type` | — | drop |
| `EventKindJsonlEndOfTurn` | — | `TurnEnd{Reason: TurnEndReasonEndTurn}` |
| `EventKindPty*` / `EventKindStallDetected` / `EventKindUnknown` | — | drop |

- **The brittleness split (ADR 025).** JSONL-sourced kinds (assistant text, tool
  use/result, end-of-turn) are robust and map. Every **PTY-state** kind
  (`PtyIdle`, `PtyThinking`, `PtyModal*`, `PtyMcpFailure*`, `PtyNetworkFailure*`),
  the `StallDetected` marker, and `Unknown` are **dropped** — not because the
  screen signals are worthless, but because the internal model (#606) has **no
  type** for them (no idle/modal/stall variant; that is deliberate). Idle/modal/
  stall surfacing are later #596/#597 children. All 11 v1.3.0 `EventKind` variants
  are handled: two mapped, the nine others dropped.
- **Tool activity rides JSONL, not a dedicated kind.** There is no `tool-use` /
  `tool-result` event *kind*. `tuidriver.ParseToolUse(e.RawLine)` extracts a
  `tool_use` block (assistant envelope) → `ToolStart`; `ParseToolResult` extracts
  a `tool_result` block (user envelope) → `ToolUpdate`. Branches split on `e.Type`
  first so these re-parse-`RawLine` extractors run only where they can match.
- **Assistant sub-conditions are mutually exclusive in practice.** Claude's
  streaming JSONL serialises one content block per line, so an assistant line
  never carries both `text` and a `tool_use` block; the `tool_use → text →
  thinking` priority order is defensive and drops nothing in practice (confirmed
  against tui-driver's own JSONL-layout doc).
- **Turn-end ordering.** The merge loop emits `EventKindJsonlEntry` then
  `EventKindJsonlEndOfTurn` for the same final entry, so the producer emits a
  `TextChunk` immediately followed by a `TurnEnd` — content, then boundary.
  `EventKindJsonlEndOfTurn` fires only after `IsEndTurn` (`stop_reason=="end_turn"`)
  held, so `TurnEndReasonEndTurn` is the only correct reason; other stop reasons
  are not distinguishable from this event kind in v1.3.0.

### Field mapping & helpers

- `ToolStart{ToolCallID: tu.ID, Title: tu.Name, Kind: toolKind(tu.Name), RawInput: rawInput(tu.Input)}`.
  `Locations` deferred — deriving touched files from tool input is #616/refinement.
- `ToolUpdate{ToolCallID: tr.ToolUseID, Status: completed|failed, Content}`. A
  `tool_result` marks the call finished → terminal status (never pending); empty/
  absent content → `nil` (legal status-only update).
- `TextChunk` / `ThoughtChunk` carry `MessageID = e.Message.ID`.
- `thinkingText` mirrors `tuidriver.AssistantText`'s shape, reading `Raw["thinking"]`
  from `type=="thinking"` blocks (the Anthropic extended-thinking block) — tui-driver
  ships no thinking-text helper.
- `toolResultText` extracts text from the `string | []any | nil` `tool_result`
  Content union; `toolKind` is a best-effort claude-tool-name → ACP-kind switch
  (`Read→read`, `Edit`/`Write→edit`, `Bash→execute`, `Grep`/`Glob→search`,
  `WebFetch→fetch`, `Task→think`, default `other`); `rawInput` re-marshals the
  input map to opaque JSON (empty/error → `nil`).

`mapEvent` **never errors** — unrepresentable events return `(nil, false)` and are
dropped. This follows #606's posture: the producer drops what the model can't
hold; it does not invent error envelopes. (Malformed JSONL never reaches the
mapper — tui-driver's `TailJSONL` silently drops unparseable lines upstream.)

## The live `Subscriber` (`NewSessionSubscriber`)

Builds the production `Subscriber`. Per call:
1. `host.WaitForPTY(ctx)` — block until a session is live; return `ctx.Err()` on cancel.
2. `sess := host.Session()`; if `nil` (torn down between WaitForPTY and capture),
   retry — `WaitForPTY` blocks for the next session, so no spin.
3. `resolve(ctx)` → `tuidriver.WaitForSessionJSONL(ctx, path)` — on ctx-cancel
   return the error; on any other transient error, `log.Warn` + retry after a
   bounded `subscribeRetryDelay` (500ms) to cap the spin.
4. `sessCtx, cancel := context.WithCancel(ctx)`; `go func(){ sess.Wait(); cancel() }()`.
5. `sess.Events(sessCtx, path, off, tr)`; on error `cancel()` + retry; on success
   return the channel.

**The Wait-watcher is the linchpin of "no leaked goroutine across a session
restart."** The Events merge loop exits only on its ctx or when its internal
`TailJSONL` channel closes — but `TailJSONL` tails a file that **persists on disk
after claude exits** (EOF → wait → retry), so it never closes on process exit
alone. The per-session ctx, cancelled by `sess.Wait()` returning when the
supervised process exits, is therefore the *only* thing that closes the stream on
a restart. The watcher is spawned **only on the `Events()` success path**, so an
open-error retry leaks no watcher; it unblocks on the supervisor's guaranteed
`sess.Close()` at every `runOnce` exit (including root-ctx cancel), so it never
leaks. `sess.Wait()` is documented safe to call concurrently with the
supervisor's own `Wait`/`Close`.

`tr` is required by `Session.Events` (a nil tracker panics) but only drives the
dropped stall arm here — a default `tuidriver.NewTracker(tuidriver.TrackerOpts{})`
suffices.

## The `Session()` accessor (supervisor)

```go
// Session returns the currently-hosted tui-driver Session, or nil when no
// claude child is attached (between restarts, mid-spawn, or idle-evicted).
func (s *Supervisor) Session() *tuidriver.Session
```

A nil-safe getter mirroring `ScreenSnapshot`'s capture (lock `sessMu`, read
`s.sess`, unlock). **Additive** — no existing signature changes, zero call-site
blast radius. Returning `*tuidriver.Session` does **not** breach the substrate
seal: the producer only calls `sess.Events()` (typed events), never
`MirrorOutput()`/`Snapshot()` (raw bytes), so no claude-screen literal enters
pyrycode and `cmd/substrate-guard` stays green.

## Concurrency model

- **One producer goroutine** (`Producer.Run`, started by #616 under the daemon
  root ctx). Single owner of the `OnEvent` invocation.
- **One short-lived Wait-watcher goroutine per subscription**, inside the live
  Subscriber. Exits when the session closes. No leak (see above).
- **tui-driver's own merge + tail goroutines** inside `Session.Events`, governed
  by the per-session ctx; they close the channel and exit when it is cancelled.
- **Shutdown:** root ctx cancel → `WaitForPTY`/`drain`/Events-merge all observe
  `ctx.Done()` → channels close → `Run` returns `ctx.Err()`; the supervisor
  independently `Close`s its session, unblocking any in-flight Wait-watcher.
- The only shared state is the supervisor's `sessMu` (already leaf-only), read by
  `Session()`. The producer adds no locks.

## Which JSONL, and surviving `/clear` rotation

**The producer does not own JSONL-path resolution or live-`/clear`-rotation
survival.** It accepts an injected `resolve func(ctx)(path, startOffset, err)` and
re-subscribes per session via the supervisor's restart signal.

- **Supervisor restart is handled here.** A newly-hosted session ends the prior
  one (`sess.Wait()` returns) → per-session ctx cancels → Events channel closes →
  `Run` re-subscribes → `WaitForPTY` blocks for the new session → `resolve` runs
  again. This is exactly the "no leak across restart" guarantee, unit-tested via
  the fake `Subscriber`.
- **Live `/clear` rotation is deferred to #616.** A `/clear` rotates claude's
  on-disk session UUID **without** restarting the supervised process, so the
  Events channel does not close and the producer keeps tailing the now-silent old
  JSONL. Detecting this needs the rotation watcher / fsnotify machinery the
  producer must not import; #616 (where the pool + rotation watcher are in scope)
  supplies the trigger.
- **The production resolver belongs to #616.** Because the supervisor spawns with
  `--continue` (no `--session-id`), there is no stable UUID at spawn time — exactly
  why `deliverViaSession` leaves `JSONLPath` empty. #616's resolver can be
  `tuidriver.SessionJSONLPath(home, workDir, pool.Default().ID())`, re-evaluated
  per subscription, returning the current file size as `startOffset` so a
  (re)subscription streams only new events instead of replaying the whole
  conversation. #615 ships the mechanism; #616 supplies the policy.

## Not `security-sensitive`

The producer reads the supervised claude session's structured events — **trusted
local input** — and maps them to an internal model. No untrusted-party input, no
dispatch to phones, no trust decision, no capability enforcement. All of that
lives in the consumer slice (#616), which carries the `security-sensitive` label.
Labelling this producer would track data lineage rather than the security-relevant
design decision.

## Related

- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § Phase 2
  structured streaming, § "The event model", § "Key architectural insight" (the
  JSONL-robust / PTY-brittle split), § Backpressure.
- [turnevent-package.md](turnevent-package.md) (#606) — the output model this maps INTO.
- [protocol-package.md](protocol-package.md) (#607) — the v2 wire types #616 maps OUT to.
- [codebase/513.md](../codebase/513.md) — `ptyrunner.Run`, the sibling consumer of
  the same `tuidriver.Session.Events()` unified stream (different consumer, same
  per-session-ctx Wait discipline).
- [codebase/615.md](../codebase/615.md) — ticket record (patterns + lessons).
