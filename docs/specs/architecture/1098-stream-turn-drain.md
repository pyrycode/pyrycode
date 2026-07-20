# Spec #1098 — Stream turn-stream: drain streamsup turnevents into `interactiveTurnEmitterV2`

## Files to read first

- `cmd/pyry/interactive_turn_v2.go:76-133` — the **unchanged** emitter: struct fields, `newInteractiveTurnEmitterV2(cursor, bcast, logger)`, `flushC()`. Extract: the emitter is a passive, single-goroutine state machine that owns the coalescing `flushTimer` but does **not** select it — a driver must. This ticket supplies that driver.
- `cmd/pyry/interactive_turn_v2.go:135-234` — `Handle(ctx, turnevent.Event)`: reads the cursor via `CurrentConversation()`, drops on empty cursor, and the #1062 follow-active-switch block (`convID != e.turnConvID`). Extract: the emitter stamps whatever the live cursor says; a mid-stream cursor change flushes the prior conversation's delta against its captured `deltaConvID` and re-mints a turn. This is the whole basis for per-conversation scoping.
- `cmd/pyry/interactive_turn_v2.go:36-43` — the `cursorReader` seam (`CurrentConversation() string`). The drain does **not** bypass it; it reuses it (AC "reuse the existing conversation stamp").
- `cmd/pyry/interactive_turn_stream_v2.go:57-145` — `startInteractiveTurnStreamV2`: the **PTY-path** wiring to mirror for *lifecycle shape only* (build emitter → run one goroutine → return a cleanup that blocks on `<-done`). Extract: the `OnEvent: func(ev){ emitter.Handle(ctx, ev) }` + `FlushSignal: emitter.flushC()` + `OnFlush: func(){ emitter.flushDelta(ctx) }` triple. The stream drain reproduces this triple **without** turnbridge (see Design § "Why not turnbridge").
- `internal/streamsup/parser.go:41-90` — `Parser` (`io.Writer`), `NewParser(sink func(turnevent.Event), logger)`. Extract: the sink is called **serially on the os/exec forwarder goroutine**, one-per-session; the Parser is installed as `Config.Stdout`; it emits already-neutral `turnevent.Event` values (no transcript, AC3).
- `cmd/pyry/streamsup_runner.go:53-101,135-138` — `streamRunnerFactory` + `mapStreamsupConfig` (#1109). Extract: the factory is where `Config.Stdout` gets set; #1109 left it **nil** (pinned by `TestMapStreamsupConfig_Bootstrap`'s `got.Stdout == nil`) and scoped the sink to this ticket. `mapStreamsupConfig` stays **pure** — the Parser install goes in the factory, not the mapper (so the mapper's purity proof and its `Stdout == nil` assertion are untouched).
- `cmd/pyry/streamsup_runner_test.go:112-224` — the `mapStreamsupConfig` / `streamRunnerFactory` tests to adjust. Extract: `TestMapStreamsupConfig_*` stay green unchanged (mapper still nils `Stdout`); only `TestStreamRunnerFactory_*` re-target the constructor (`newStreamRunnerFactory(sink)(cfg)`).
- `cmd/pyry/main.go:1246-1291` — `activeConversation`: `CurrentConversation()`, `set`, `watch`. Extract: the single daemon-wide active-conversation cursor the emitter reads and the drain gates on.
- `cmd/pyry/main.go:786-812` — the `boundHost` closure (`convID → (host, sessionID, dir, ok)`). Extract: how the active conversation resolves to its bound session **id** (`conv.CurrentSessionID` == the pool session id == the parser's `cfg.SessionID`). The stream gate uses the id, not the host (the host is a `streamRunner`, not a `*supervisor.Supervisor`, so `Session.Supervisor()` returns nil — do not reach through it).
- `internal/sessions/runner.go:38-46` — `RunnerFactory func(cfg supervisor.Config) (Runner, error)`. Extract: the factory sees only `supervisor.Config` (carries `SessionID`, no emitter handle) — the reason the sink must be late-bound.
- `cmd/pyry/acp_turn_stream.go:21-64` — the ACP `acpTurnStream.Handle` doc, for the content-free-logging + single-Run-goroutine posture to copy verbatim.

## Context

The interactive turn machinery derives `turn_state` / `assistant_delta` / tool events from a neutral `turnevent.Event` stream via `interactiveTurnEmitterV2`. On the PTY path that stream comes from a transcript-tailing subscriber (`turnbridge.New` over a `TargetSubscriber`). The stream-json path has **no transcript** — its turnevents come straight off the runner's stdout parser (`streamsup.Parser`, #1088), installed as the runner's `Config.Stdout`. #1109 built the pool-supervised streamsup runner (via `streamRunnerFactory` / `RunnerFactory`) but deliberately left `Config.Stdout` nil, scoping the sink to this ticket.

This ticket drains that parsed stream into the **unchanged** emitter. It builds the mechanism and unit-tests it end-to-end (bytes → `Parser` → sink → emitter output). Production *selection* of the stream factory (assigning it onto `sessions.Config.RunnerFactory`) is #1081 and is out of scope here; nothing in this ticket wires `main.go` / `relay.go`. The mechanism is U1000-safe because its unit tests exercise every new symbol.

## Design

### The seam decision (the heart of this ticket)

Two lifetimes are disjoint and must be lined up:

1. **Parser sink** — fixed at *runner construction* (the factory, per session, inside `Pool.New` for the bootstrap and `Pool.buildSession` per conversation). The factory sees only `supervisor.Config`; it has **no emitter handle**.
2. **Emitter** — a single daemon-resident instance built on the *relay leg*, following the active conversation via `CurrentConversation()`.

The factory runs for the bootstrap session *before* the relay leg builds the emitter, so the sink cannot capture the emitter directly. The lining-up object is a **late-bound, daemon-singleton fan-in** created before the pool, captured by the factory (as the parser sink) and by the drain (as its source):

```
N parsers (one per session, each on its own os/exec forwarder goroutine)
      │  sink(sessionID, ev)  — non-blocking send
      ▼
streamTurnSink   (one buffered chan streamTurnEnvelope{sessionID, ev})
      │  read
      ▼
one drain goroutine ── gate(sessionID == active session?) ──> emitter.Handle(ctx, ev)
      │  select emitter.flushC()  ──────────────────────────> emitter.flushDelta(ctx)
```

This is the minimal mechanism the drain needs — a fan-**in** to one point plus a single consumer goroutine. It is **not** "shared fan-out infrastructure": there is no session-keyed registry, no subscribe/unsubscribe, no per-conn broadcasting added here (the existing per-conn fan-out lives *inside* the unchanged emitter). So the ticket does not split on that axis (the escape hatch the issue named is not triggered).

### Why not `turnbridge`

`turnbridge.Subscriber` yields `<-chan tuidriver.Event` (raw PTY events) and the producer maps them to `turnevent.Event`. The stream `Parser` already emits `turnevent.Event` (it *is* the mapper). Feeding turnevents back through turnbridge would require un-mapping to `tuidriver.Event` — absurd. So the drain reproduces turnbridge's `OnEvent`/`FlushSignal`/`OnFlush` triple with a bespoke ~one-goroutine loop, and reuses nothing of `turnbridge`.

### New type — `streamTurnSink` (`cmd/pyry/stream_turn_drain.go`)

Contract (names are suggestions; match surrounding idiom):

- `streamTurnEnvelope struct { sessionID string; ev turnevent.Event }` — the fan-in element.
- `streamTurnSink` holds one buffered `chan streamTurnEnvelope`.
- `newStreamTurnSink(buf int) *streamTurnSink` — constructor; `buf` defaults to the `MirrorOutput` precedent (256).
- `(s *streamTurnSink) sinkFor(sessionID string) func(turnevent.Event)` — returns the per-parser closure the factory hands to `NewParser`. The closure does a **non-blocking** send of `{sessionID, ev}` (drop-newest on a full channel, content-free Debug log on drop). Non-blocking is load-bearing: the parser runs on claude's stdout forwarder goroutine, and a blocking send would wedge the child (mirrors the emitter's "owns no queue → never wedge claude" principle; the channel *is* the queue and drops rather than blocks).

The channel is **never closed** — parsers may outlive the drain during shutdown, and a send-on-closed panic must be structurally impossible. The drain stops by ctx, not by channel close; post-shutdown parser sends land in the drop path.

### The drain — `startStreamTurnDrainV2` (`cmd/pyry/stream_turn_drain.go`)

Signature (narrow and testable — the caller composes the active-session resolver):

```
func startStreamTurnDrainV2(
    ctx context.Context,
    sink *streamTurnSink,
    emitter *interactiveTurnEmitterV2,
    activeSession func() (sessionID string, ok bool),
    logger *slog.Logger,
) (cleanup func())
```

Behavior: spawn one goroutine; `select` over `ctx.Done()`, `sink.ch`, and `emitter.flushC()`:
- `sink.ch` receives `{sid, ev}`: resolve `active, ok := activeSession()`; if `ok && sid == active`, call `emitter.Handle(ctx, ev)`; else drop (content-free Debug). Gating **at Handle time** (not in the sink) keeps the stamp consistent with the cursor the emitter reads inside `Handle`.
- `emitter.flushC()` fires: call `emitter.flushDelta(ctx)`.
- `ctx.Done()`: return.

`cleanup` blocks until the goroutine exits (`<-done`), mirroring `startInteractiveTurnStreamV2`.

The emitter is passed **in** (not built here) so the caller owns construction + replay wiring. This ticket's caller is the unit test; #1081's caller composes `activeSession` from `active.CurrentConversation` + `boundHost` and wires `mgr.SetReplaySource` — none of that is in scope here.

### Factory change (`cmd/pyry/streamsup_runner.go`)

Replace the bare `streamRunnerFactory` func with a constructor that captures the sink and installs the Parser:

- `newStreamRunnerFactory(sink *streamTurnSink) sessions.RunnerFactory` returns a closure that: `scfg := mapStreamsupConfig(cfg)`; `scfg.Stdout = streamsup.NewParser(sink.sinkFor(cfg.SessionID), cfg.Logger)`; then `streamsup.New(scfg)` wrapped in `streamRunner` (existing error contract, existing no-silent-PTY-fallback wrap).
- `mapStreamsupConfig` stays pure and keeps nilling `Stdout` (the Parser is a runtime object, installed one layer up). The two `TestMapStreamsupConfig_*` tests are unchanged; only their in-code comment about "#1098 fills Stdout" moves to name the factory.
- `cfg.SessionID` is the pool session id (#1108 guarantees non-empty at both sites), and it equals `conv.CurrentSessionID` that `boundHost` returns — the gate is a clean pool-id equality (see Scoping proof).

### Scoping proof (AC2 — the security-relevant property)

The emitter stamps each envelope with `CurrentConversation()`. The drain forwards session `S`'s event **only when `S` is the bound session of the active conversation**, so at `Handle` time the cursor names `S`'s conversation and the stamp is correct. A background conversation `B`'s parser events (`B ≠ active session`) are dropped before ever reaching `Handle` — `B`'s conn never receives `A`'s events. On an active switch `A → B`, the drain begins forwarding `B`'s events; the emitter's #1062 block flushes `A`'s buffered delta against its captured `deltaConvID` and re-mints a fresh turn for `B`. This is the PTY path's follow-active semantics, realized via a per-event session gate instead of a transcript re-subscription (the Parser's sink is fixed at construction and cannot be torn down/re-subscribed the way a file tail can).

Residual race (pre-existing, not introduced here): between the drain's gate check and `Handle`'s own cursor read, the cursor could change; the emitter's cursor read is authoritative and #1062 reconciles. The PTY path carries the identical tolerance — this ticket does not widen it.

## Concurrency model

- **One writer to the emitter.** Only the drain goroutine calls `Handle` / `flushDelta`. Parser goroutines only push to the channel. The emitter's unguarded fields stay race-free — the same single-Run-goroutine invariant the PTY producer relies on. `-race`-testable by feeding two parsers concurrently and asserting no race.
- **Flush timer.** The emitter arms `flushTimer` inside `Handle` and expects a driver to select `flushC()`; the drain goroutine is that driver (both `Handle` and `flushDelta` on the one goroutine — no cross-goroutine timer race, Go 1.26 timer semantics).
- **Goroutine lifecycle.** The drain goroutine exits on `ctx.Done()`; `cleanup` joins it. Parser goroutines are owned by `os/exec` and exit on child-stdout close; their sink closures never block (non-blocking send), so an absent/slow drain cannot leak or wedge them.
- **Locks.** The drain takes none. `activeSession()` reads `active.mu` then the pool/convReg locks — the same order as the existing `boundHost`, no new lock-order edge. The sink closure takes no lock (channel send).

## Error handling

- **Full channel** → drop-newest, content-free Debug log. Accepted tradeoff: unlike the PTY path (re-tails the persisted transcript), a dropped stream event is lost — but wedging claude is worse, and the emitter is explicitly a no-queue design. See Open questions.
- **Empty cursor / unresolvable active conversation** (`activeSession()` returns `ok=false`, or the emitter's own empty-cursor guard) → event dropped, no emission. Matches the PTY path's empty-bootstrap-cursor drop.
- **`streamsup.New` failure in the factory** → unchanged #1109 contract: `(nil, wrapped error)`, no silent PTY fallback.
- **Parser-level malformed/oversized child output** → handled upstream by #1088 (unparseable line dropped, 4 MiB partial-line cap). This ticket adds no new parsing and does not widen that boundary.

## Testing strategy

New `cmd/pyry/stream_turn_drain_test.go` (stdlib, table/scenario, `-race`), plus fakes for `interactiveBroadcaster` (capture pushed envelopes) and `cursorReader`:

- **Full single-turn** — cursor=`convA`, `activeSession`→(`sessA`,true); feed an `assistant` line (text block) then a `result` line through `NewParser(sink.sinkFor("sessA"))`; assert the fake broadcaster received `turn_state:responding`, `assistant_delta` (text stamped `convA`), `turn_state:idle`, in order.
- **AC2 scoping** — cursor=`convA`/active `sessA`; feed `sessB`'s bytes → assert **nothing** pushed (dropped, not active). Flip cursor→`convB` and `activeSession`→(`sessB`,true); feed `sessB`'s bytes → assert envelopes pushed stamped `convB`. Feed `sessA` and `sessB` **concurrently** under `-race` to prove single-writer safety.
- **Empty cursor drop** — `activeSession`→(_,false); feed bytes → assert nothing pushed.
- **Flush-timer wiring** — feed a lone `assistant` text line with no following event; assert the coalesced `assistant_delta` emerges within a bound comfortably over `coalesceWindow` (250 ms) — proving the drain selects `flushC()` and calls `flushDelta`. Keep this the only timing-sensitive case; scoping tests flush deterministically at the `result`→`TurnEnd` boundary.
- **Tool events** — feed a `tool_use` assistant block and a `tool_result` user line; assert the mapped tool envelopes fan through (reuses #1088's parser + #627 mapper; a light assertion, not a re-test of the mapper).
- **AC3 (no transcript)** — structural: the test does no filesystem setup and the drain imports no fsnotify / jsonl resolver. State it in a comment; the absence is the assertion.

Adjust `cmd/pyry/streamsup_runner_test.go`: re-target `TestStreamRunnerFactory_Construct` / `_ErrorPropagation` to `newStreamRunnerFactory(newStreamTurnSink(...))(cfg)`. `TestMapStreamsupConfig_*` stay green unchanged.

## Open questions

- **Drop-newest vs. bounded-block on a full sink channel.** Spec chooses drop-newest (never wedge claude). If field use shows meaningful delta loss under a slow phone, a small bounded block with a deadline could be revisited — but that reintroduces wedge risk and should be its own ticket. Buffer size (256) is a starting point; the developer may tune with a comment.
- **Per-conversation fan-out is still the emitter's job, unchanged.** The emitter fans every active-conversation envelope to *all* interactive conns (each carries `conversation_id`; the phone renders its active conversation). This ticket preserves that; it does not add conversation-level conn filtering (out of scope — that's the emitter's existing #589/#626 contract).

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The untrusted→trusted boundary (claude child stdout → parent state) is the #1088 `Parser`, upstream and unchanged: turn-stateless, segments only on top-level `type`, drops unparseable lines, caps the partial buffer at 4 MiB. This ticket consumes already-neutral `turnevent.Event` values (post-boundary) and adds no new parsing — it never re-scans nested content, so a `tool_result` whose text is `{"type":"result"}` cannot forge a turn boundary (that property is #1088's, preserved). The new code holds only typed events.
- **[Error messages, logs, telemetry]** MUST-follow instruction to the developer (SHOULD FIX if missed, code-review must catch): every log line in `stream_turn_drain.go` — the drop-on-full path, the not-active drop, any Debug — MUST be content-free: `event`, `kind` (via `eventKind`), `session_id`, `conversation_id`, `env_id` only. NEVER `ev` content (assistant text, thought text, tool title/input/result). This mirrors the emitter's and `acpTurnStream`'s documented posture; the spec states it as a hard constraint so the developer wires it from the start.
- **[Concurrency]** No MUST FIX. Single-writer-to-emitter invariant (only the drain goroutine touches the emitter) is stated and `-race`-tested. The fan-in channel is never closed → no send-on-closed panic during shutdown. Goroutine exits on ctx; `cleanup` joins it; parser sinks are non-blocking so they cannot leak or wedge. No new lock-order edge (`activeSession` reuses `boundHost`'s order; the drain takes no lock).
- **[Network & I/O — resource exhaustion]** No MUST FIX. A hostile/pathological child cannot exhaust memory: the Parser caps its partial line (#1088, 4 MiB), and the fan-in channel is bounded with drop-newest — no unbounded queue growth from a fast child + slow drain.
- **[Confidentiality — the core property, AC2]** No MUST FIX. Cross-conversation leakage is the primary threat on this internet-exposed surface. The per-event session gate (forward only the active conversation's bound-session events; drop all others) enforces it, stamped consistently via the emitter's authoritative `CurrentConversation()` read. Proven by the AC2 scoping test (conversation B never receives A's events, and the switch case re-mints correctly). No new access-control surface is added; conversation-level conn fan-out remains the emitter's existing contract.
- **[Tokens/secrets, File operations, Subprocess exec, Cryptographic primitives]** Not applicable — this ticket adds no tokens/credentials, no filesystem paths (AC3: no transcript, no jsonl resolution, no fsnotify), no new subprocess argv (the factory's argv handling is #1109's, unchanged), and no crypto. Each is N/A by the design's construction, not by omission.
- **[Threat model alignment]** The relevant `protocol-mobile.md` threat is cross-session/cross-conversation content disclosure to a paired phone; addressed by the scoping gate above. Out of scope and named: production *selection* of the stream factory and its replay/`SetReplaySource` wiring (#1081); bounded-block backpressure alternative (future, if drop-loss proves material).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-21
