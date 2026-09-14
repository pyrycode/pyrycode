# `internal/turnbridge` — outbound event-to-wire adapter

A pure value-to-value adapter mapping the neutral internal turn-event model
([`internal/turnevent`](turnevent-package.md), #606) OUT to the v2 interactive
mobile wire payloads ([`internal/protocol`](protocol-package.md), #607):
`MapEvent` shapes one `turnevent.Event` into a typed payload, `BuildTurnState`
shapes the `turn_state` payload the lifecycle machine drives. No I/O, no state,
no envelope-ID minting, no clock read, no sealing — all of that belongs to the
consumer, `cmd/pyry/interactive_turn_v2.go`'s `interactiveTurnEmitterV2`
(`.emit` wraps a payload into an `Envelope`; `.Handle`/`.transitionTo` own the
turn-lifecycle machine that calls `MapEvent`/`BuildTurnState`).

**History — the package used to bridge in both directions.** An **inbound
producer** (`producer.go` + `mapper.go`, #615, #679) drained the supervised
claude session's PTY-hosted tui-driver `Events()` stream and mapped each event
INTO the `turnevent.Event` model — the exact mirror of what `MapEvent` now does
OUT. #1348 deleted every terminal-driving claude path that fed and consumed it
(including `internal/supervisor` itself and `cmd/pyry/interactive_turn_stream_v2.go`'s
wiring), which orphaned both files without touching them; **#1543** deleted
`producer.go`, `producer_test.go`, `mapper.go` and `mapper_test.go` (dropping the
package's `tui-driver` dependency) and re-homed the package doc onto
`outbound.go`. There is no dedicated `docs/knowledge/codebase/1543.md` record —
that directory froze 2026-08-19, the day before #1543 landed — but the
producer's own history stays readable in the frozen ticket records linked under
§ Related below. The live inbound counterpart today is
[`internal/streamsup`](streamsup-package.md)'s stream-json parser — a
structurally different path with no `turnbridge` re-mapping step, since the
parser emits `turnevent.Event` directly.

## Files

```
internal/turnbridge/
├── outbound.go       MapEvent / BuildTurnState + summary helpers (#627)
└── outbound_test.go  table-driven model→payload + drop tests; inputSummary / resultSummary / truncate / BuildTurnState
```

## Public API

```go
// TurnContext is the per-event turn addressing the consumer supplies. The adapter
// never derives these — which conversation / turn / seq applies is a lifecycle
// decision owned by the consumer.
type TurnContext struct {
    ConversationID string
    TurnID         string
    Seq            int // per-lane assistant-delta order; consumed ONLY by TextChunk
}

// TurnState is the coarse lifecycle state BuildTurnState shapes into a turn_state
// payload. String-backed so the call site is enum-safe.
type TurnState string
const (
    StateThinking   TurnState = "thinking"
    StateResponding TurnState = "responding"
    StateIdle       TurnState = "idle"
)

func MapEvent(ev turnevent.Event, tc TurnContext) (typ string, payload any, ok bool)
func BuildTurnState(conversationID string, state TurnState) (typ string, payload protocol.TurnStatePayload)
```

Two exported types (`TurnContext`, `TurnState`), two functions, three state
constants — the entire public surface, since #1543.

## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [The outbound adapter (`MapEvent` / `BuildTurnState`)](turnbridge-package-outbound-adapter.md) — the full per-variant `MapEvent` table (every `turnevent.Event` arm and its wire payload, including `ContextUsage` (#2371)), `BuildTurnState`, the summary-derivation and per-field-input-extraction helpers, and what the adapter deliberately does not do.

## Concurrency model

None. `MapEvent`/`BuildTurnState` are pure synchronous functions — no goroutines,
channels, shared state, or shutdown sequence. Before #1543 this package also hosted
the only concurrent machinery it ever had (the deleted producer's `Run`/`drain`
re-subscribe loop plus its per-subscription Wait- and switch-watcher goroutines);
deleting it reduced the package's concurrency surface to zero. That history is in
[codebase/615.md](../codebase/615.md) and [codebase/679.md](../codebase/679.md).

## Not `security-sensitive`

A pure value-to-value mapper: no untrusted-party input, no capability decision, no
dispatch. All capability enforcement lives in the consumer slice (#616,
`cmd/pyry/interactive_turn_v2.go`), which carries the `security-sensitive` label.
Labelling this package would track data lineage rather than the security-relevant
design decision.

## Related

- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § Phase 2
  structured streaming, § "The event model", § Backpressure.
- [turnevent-package.md](turnevent-package.md) (#606) — the pivot model `MapEvent`
  maps OUT of.
- [protocol-package.md](protocol-package.md) (#607) — the v2 interactive wire types
  `MapEvent` maps OUT to.
- [streamsup-package.md](streamsup-package.md) — the live inbound counterpart on the
  stream-json path (structurally different: no `turnbridge` re-mapping step; the
  parser emits `turnevent.Event` directly). Its `toolResultContent` is what
  `resultSummary`'s doc comment names above.
- [acpbridge-package.md](acpbridge-package.md) (#769) — the sibling outbound ACP
  adapter, the exact ACP mirror of this package's `MapEvent` (same neutral source,
  different wire framing).
- [codebase/627.md](../codebase/627.md) — this file's own ticket record (patterns +
  lessons).
- [codebase/639.md](../codebase/639.md), [codebase/1074.md](../codebase/1074.md) —
  the stall / api-retry / compacting `MapEvent` rows landed by those tickets.
- **History of the removed inbound producer** (deleted #1543; orphaned by #1348):
  [codebase/615.md](../codebase/615.md) (the producer), [codebase/609.md](../codebase/609.md)
  (the flush-signal coalescing seam it offered), [codebase/679.md](../codebase/679.md)
  (the follow-active subscriber), [codebase/686.md](../codebase/686.md) (per-conversation
  JSONL directory), [codebase/854.md](../codebase/854.md) (bootstrap resolver),
  [codebase/1062.md](../codebase/1062.md) (mid-turn switch fix),
  [codebase/1243.md](../codebase/1243.md) (the interrupt-marker signal) — all frozen
  and still searchable via `mcp__qmd__query(collection: "pyrycode-docs", …)`.
