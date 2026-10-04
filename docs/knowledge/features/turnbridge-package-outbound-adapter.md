# The outbound adapter (`MapEvent` / `BuildTurnState`)

`MapEvent` (#627) is a pure type-switch over the sealed `turnevent.Event`,
shaping one event + an explicit `TurnContext` into the matching v2 interactive
wire payload (#607). The sections below describe the mappings and helpers.

## Sections

| Document | Contents |
|---|---|
| [`MapEvent`](turnbridge-package-outbound-adapter-map-event.md) | Per-variant wire mapping, forwarding boundaries and mapping tests. |
| [Summary derivation](turnbridge-package-outbound-adapter-summary-derivation.md) | `inputSummary`, `resultSummary`, truncation and tool-result envelope sizing. |
| [Per-field input extraction](turnbridge-package-outbound-adapter-per-field-input-extraction.md) | `inputFields` / `inputValue`, field caps and confinement tests. |

## `BuildTurnState` — the lifecycle payload builder

Returns `TypeTurnState` + `TurnStatePayload{conversationID, string(state)}`. Concrete
return type (not `any`) because it is monomorphic — no consumer type assertion. The
consumer's lifecycle machine decides *which* state applies (thinking / responding /
idle) and calls this; the adapter only shapes the payload.

## What the outbound adapter does NOT do (the seam)

`MapEvent`/`BuildTurnState` produce only the typed payload + discriminant. The
consumer (integration slice, building on #616's fan-out) owns the envelope `ID` mint,
`TS` clock read, `json.Marshal`, AEAD seal, `Push`, the drop-log for un-mappable
events, **and** every lifecycle decision (which conversation/turn/seq/state applies,
turn-id assignment, seq advancement, coalescing). See
`cmd/pyry/session_transition_v2.go` for the existing shape that wraps a payload into
an `Envelope`; the structurally-identical v2 coarse emitter `assistant_turn_v2.go`
was removed in [#699](../codebase/699.md), and the v1 coarse bridge
`cmd/pyry/assistant_turn.go` this paragraph originally also pointed at was removed in
[#913](../codebase/913.md). This is why the adapter is pure: every clock read,
counter, and I/O lives in the consumer.

`interactiveTurnEmitterV2` therefore owns the parent-keyed assistant-lane state
(#2330). The empty parent uses the existing outer-turn id and sequence; each
non-empty parent lazily receives a distinct stable id and independent counter for
that outer turn. Its coalescer intentionally remains **one active buffer keyed by
both parent id and message id**. A buffer per lane looks natural but can delay an
earlier child until after later main or sibling-child prose, destroying global
arrival order; the single buffer flushes on either key change and still lets
same-lane, same-message text coalesce. Turn end and conversation switch flush before
discarding all lane state. Every flushed chunk then returns through `MapEvent` and
the ordinary emitter path, so attributed prose does not acquire a second mapping,
splitting, replay-ring, history, droppable-classification, or fan-out path.
This isolation does not enable the subprocess's subagent-text forwarding flag;
that production switch and its live-Claude proof belong to #2331.

A lifecycle-neutral emitter test that begins with an open turn proves only that the
event does not alter that turn. It stays green if the handler accidentally opens a
turn from idle. Passive event variants therefore need a separate idle-state assertion
that checks both lifecycle state and the absence of synthetic `turn_state` frames.
