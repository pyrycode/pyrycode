# Spec — #750: ACP outbound streaming adapter (turnevent → `session/update`)

**Ticket:** #750 (epic #600, T6). **Size:** S. **Security-sensitive:** no (outbound-only over local stdio; no untrusted inbound parsing, no auth/crypto — the inbound handlers #749/#752 carry that label).

**Blocked by (both landed):** #769 (the pure `turnevent.Event → session/update` mapper, `internal/acpbridge`, merged `e5216fa`/`0ff7e27`) · #761 (`session/new` over the embedded ACP pool, closed).

## Files to read first

| Path · lines | Extract |
|---|---|
| `internal/acpbridge/outbound.go` (whole, 214 L) | `MapUpdate(ev) (update any, msgID string, ok bool)` — the mapper this ticket **consumes**. Payload structs (`AgentMessageChunk`, `ToolCall`, …), `MethodSessionUpdate = "session/update"`, and the two `ok == false` events (`TurnEnd`, `Stall`). The `{sessionId, update}` params wrapper is explicitly **this** ticket's (see its doc, lines 38-41, 110-128). |
| `internal/acp/acp.go:274-305` (`Call`) + `:397-405` (`writeMessage`) | The exact shape `Notify` mirrors: marshal params first, then one `writeMessage` under `writeMu`. `Notify` is `Call` **minus** the id / pending-channel machinery. `writeMu` is a leaf lock serialising every outbound write. |
| `internal/acp/jsonrpc.go:78-88` (`request`) | The wire-struct pattern the new `notification` struct mirrors (note: `request.ID` is non-omitempty `uint64`, so `request` **cannot** be reused for a no-id notification — hence a distinct `notification` type). |
| `cmd/pyry/interactive_turn_v2.go:130-211` (`Handle`) + `:366-383` (`eventKind`) | The mobile emitter template. **Most of it is N/A for ACP** (no coalescing, no `turn_state`, no capability fan-out, no replay ring, no conversation cursor). Reuse the existing package-`main` `eventKind` helper for content-free log discriminants — do **not** redefine it. |
| `cmd/pyry/acp.go:74-145` (`serveACPWithPool`, `register`, `newSessionHandler`) | Where a later ticket (#751/T7) will construct this adapter and start the producer. The transport, pool, and stderr `logger` all exist here. The adapter takes these as constructor args. |
| `internal/turnevent/event.go:23-103` + `taxonomy.go:34-42` | The sealed six-variant `Event`; `TurnEnd.Reason` and `Stall{}`. `TurnEndReason` is `string`-backed and its values ARE the ACP `stopReason` strings — `string(reason)` is the stopReason, no table. |
| `docs/knowledge/decisions/027-acp-mapping.md` § Outbound table + Divergences 1 & 3 | The authoritative contract: `TurnEnd` → the `stopReason` **return** of `session/prompt` (not a notification); `Stall` → dropped by ACP, surfaced on **stderr**. |

## Context

Epic #600 makes `pyry acp` a thin adapter over the neutral `turnevent` core. #769 shipped the **pure** value-to-value mapper (`acpbridge.MapUpdate`). This ticket is the **streaming consumer**: it turns each mapped payload into a JSON-RPC `session/update` **notification** on the ACP transport, signals turn-end out-of-band, and drops `Stall` to stderr. It is the ACP analogue of the mobile head's `interactiveTurnEmitterV2`, but far thinner — ACP's host owns the spinner and the turn pacing, so none of the mobile emitter's stateful machinery applies.

**Finding (the ticket's flagged verification).** The ticket asked us to verify the per-session `turnevent.Event` stream is exposed by the #761 embedded ACP pool. **It is not** — #761's `cmd/pyry/acp.go` stands up a `sessions.Pool` and `session/new` just calls `pool.Create`; no `turnbridge` producer is wired for ACP sessions (the producer is wired only for the mobile head, in `runSupervisor`). **This does not block #750**, because #750 is the outbound *sink*, tested against a scripted `turnevent.Event` sequence (AC-1 says exactly that). Standing up a `turnbridge` producer over a session's supervisor belongs to the `session/prompt` turn owner (#751/T7, per divergence 1: streaming happens *inside* the held `session/prompt` call). The machinery for #751 to do so is fully shipped — see [§ Producer wiring is #751's](#producer-wiring-is-751s). This finding is recorded here so #751's run does not re-discover it.

## Design

Two new files, both **additive** (zero modification to any existing file — see [§ Concurrency & branch isolation](#concurrency--branch-isolation)):

### 1. `internal/acp/notify.go` (new) — the emit path

The transport exposes `Call` (outbound request, with id) but no notification emitter. Add one. Both the method and its wire struct live in this **new** file — matching #765's `responder.go` precedent (new outbound concern → new file) and keeping `internal/acp` conflict-free with the in-flight #765 branch.

```go
// notification is a JSON-RPC 2.0 notification frame: method + params, NO id,
// no response. request cannot be reused — its ID is a non-omitempty uint64.
type notification struct {
	Jsonrpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Notify writes one JSON-RPC notification. Mirrors Call minus the id/pending
// machinery: marshal params first (nil ⇒ omitted), then writeMessage under
// writeMu. Safe from any goroutine but the one running Serve is fine too —
// writeMu serialises it against reply/Call. Never blocks on a response.
func (t *Transport) Notify(method string, params any) error
```

Behaviour contract (assert in `notify_test.go`): a successful call writes `{"jsonrpc":"2.0","method":<m>,"params":<p>}` with **no `id`**; nil params omit the key; a params-marshal failure returns a wrapped error and writes nothing (no partial frame). No response is ever awaited.

### 2. `cmd/pyry/acp_turn_stream.go` (new) — the streaming consumer

A **stateless** adapter (contrast the mobile emitter's lifecycle/coalescing/ring state — ACP needs none). It is the `turnbridge.Config.OnEvent` sink: its `Handle` signature matches `func(turnevent.Event)` exactly, so #751 wires `OnEvent: stream.Handle` with no closure.

```go
// sessionUpdateParams is the ACP session/update params wrapper (this ticket's,
// per acpbridge doc). Update is the MapUpdate payload; the sessionUpdate
// discriminant rides inside it.
type sessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

type acpTurnStream struct {
	transport *acp.Transport
	sessionID string
	onTurnEnd func(reason string) // T7 (#751) seam; nil-tolerant
	logger    *slog.Logger
}

func newACPTurnStream(t *acp.Transport, sessionID string, onTurnEnd func(string), logger *slog.Logger) *acpTurnStream

// Handle is the turnbridge OnEvent sink. One event → at most one session/update.
func (a *acpTurnStream) Handle(ev turnevent.Event)
```

`Handle` dispatch (the whole behaviour — a dispatch, not a validator; no reject branches):

| Event | Action |
|---|---|
| `TurnEnd{Reason}` | **No** `session/update` (divergence 1). Call `onTurnEnd(string(Reason))` — the ACP `stopReason` is `string(Reason)` by identity. Return. |
| `Stall{}` | **No** `session/update` (divergence 3). Surface on stderr via `logger.Warn` (content-free: `event`, `session_id` only). Return. |
| `TextChunk` / `ThoughtChunk` / `ToolStart` / `ToolUpdate` | `update, _, ok := acpbridge.MapUpdate(ev)`; on `ok` emit `transport.Notify(acpbridge.MethodSessionUpdate, sessionUpdateParams{a.sessionID, update})`. |
| nil / future variant | `MapUpdate` returns `ok == false` → `Debug`-log a drop (defensive; unreachable for the four above). |

`TurnEnd` and `Stall` are type-switched **explicitly, before** `MapUpdate`, because `MapUpdate` collapses both to `ok == false` and the adapter needs to distinguish them (signal vs stderr). `MapUpdate` is therefore only ever reached for the four emit-able variants, where `ok` is always true; the `!ok` guard is defensive.

The `msgID` from `MapUpdate` is intentionally **discarded** (`_`). ACP's `agent_message_chunk` carries content only (ADR 027; `acpbridge.AgentMessageChunk` = `{sessionUpdate, content}`) — there is no per-message wire delimiter. "Grouping by message id" (AC-2) is realised by streaming each `TextChunk` as one `agent_message_chunk` **in arrival order**; the host concatenates consecutive chunks into the assistant message. The adapter is stateless w.r.t. `MessageID` and does **not** coalesce — the mobile emitter's `MessageID`-keyed delta coalescing (#609) is an optimisation ACP neither needs nor supports. Document the discard with a one-line comment so a developer reading #769's out-of-band `msgID` return understands why it is unused here.

## The T7 (#751) signal seam

`onTurnEnd func(reason string)` is the coordinated seam the ticket called out. It is invoked **synchronously on the producer's single `Run` goroutine** when `TurnEnd` arrives. #751 supplies a callback that resolves the held `session/prompt` call with `reason` as the `stopReason`. **#751's callback must not block** the producer goroutine — it should hand off via a buffered channel or an immediate resolve (e.g. #765's `Responder`, which returns at once). A callback (not a channel) is the seam because it keeps #750 agnostic about #751's hold-and-resolve mechanism; the channel, if any, lives inside #751's callback. `onTurnEnd` is nil-tolerant (Debug-log + no-op) so the adapter can be constructed for pure emit tests.

## Producer wiring is #751's

Not built here. When #751 owns the `session/prompt` turn it stands up a `turnbridge` producer over the addressed session and feeds this adapter — every building block is already shipped:

- `sess.Supervisor()` (`internal/sessions/session.go:122`) → `*supervisor.Supervisor`, which satisfies `turnbridge.SessionHost` (`Session()` at `supervisor.go:466`, `WaitForPTY` at `:517`).
- `turnbridge.NewTargetSubscriber(resolve, tuidriver.NewTracker(...), logger)` with a fixed-session `TargetResolver` (`Target{Host: sess.Supervisor(), Resolve: resolveBoundSessionJSONL(dir, string(sess.ID())), Switch: nil}`) — ACP has no active-conversation follow, so `Switch` is nil and the resolver is by-id, not recency.
- `dir = sessions.DefaultClaudeSessionsDir(bootstrapWorkdir)` (all ACP sessions spawn in the one confined workdir); the pool session id **is** the `claude --session-id <uuid>` JSONL stem (`pool.go:145`) — #751 to confirm.
- `turnbridge.New(turnbridge.Config{Subscribe: sub, OnEvent: stream.Handle, Logger: logger})`; run in a goroutine, bounded by the held-call ctx. No `FlushSignal`/`OnFlush` — ACP does not coalesce.

If the epic finds no ticket owns this wiring, that is a PO gap to file; T7 (#751) is its natural home.

## Concurrency & branch isolation

- `Handle` runs on the producer's single `Run` goroutine (`turnbridge` invokes `OnEvent` serially). The adapter holds **no mutable per-event state**, so it is effectively stateless — simpler than the mobile emitter's unguarded-but-single-goroutine fields.
- `Notify` writes via `writeMessage` under `writeMu`, the same leaf lock serialising `reply` and `Call`. Adding a third outbound writer (soon a fourth, when #765's `Responder` lands) is safe by the existing discipline — `writeMu` is never held across a handler call or a read.
- **Branch isolation:** #765 (in-flight) modifies `internal/acp/acp.go` (`dispatchRequest`) and adds `responder.go`. This spec's `internal/acp` change is a **new file** (`notify.go`) with **no** `Notify`/`notification` collision, so #750 and #765 merge cleanly with zero conflict. Overlap check (2026-07-03): all four of this ticket's files are new; no in-flight feature branch creates them.

## Error handling

- `Notify` marshal failure → wrapped error returned to `Handle`, which `Debug`-logs it content-free (`event`, `session_id`, `err` sentinel — never params/application text, per the `internal/acp` diagnostics discipline). In practice the payloads are closed structs that cannot fail to marshal; this is defensive.
- `Notify` write failure (host pipe broken) → same content-free `Debug` log; the turn is ending anyway and the transport surfaces stream breaks on its own read side.
- Application content (assistant text, thought text, tool titles/inputs/results) is **never** logged at any level — only variant discriminants via `eventKind`.

## Testing strategy

All ACs are satisfied at the `Handle` level (no live producer/claude needed) — the established "ship the consumer unit-tested, wire it in the lifecycle ticket" posture (#632). Drive `Handle` with scripted `turnevent.Event` values; the adapter holds a **real** `acp.Transport` whose writer is an in-memory `bytes.Buffer`; assert the decoded frames. Table-driven per CODING-STYLE where the shape allows.

- **`notify_test.go`** — `Notify` writes a well-formed notification with no `id`; nil params omit the key; params-marshal failure returns an error and writes nothing. Decode the buffer as a generic map and assert `id` absent.
- **AC-1 (emit)** — a scripted `[TextChunk, ThoughtChunk, ToolStart, ToolUpdate]` sequence produces four `session/update` notifications, each `{sessionId, update}` with the expected `sessionUpdate` discriminant and content, in order, each with no `id`/`result`.
- **AC-2 (grouping)** — `[TextChunk{m1,"Hello "}, TextChunk{m1,"world"}, ToolStart{…}, TextChunk{m2,"!"}]` → `agent_message_chunk("Hello ")`, `agent_message_chunk("world")`, `tool_call(…)`, `agent_message_chunk("!")` **in that order**. The two `m1` chunks stream contiguously as `agent_message_chunk` (host concatenates → `"Hello world"`); no coalescing into one frame; the `m2` chunk after the tool call is its own `agent_message_chunk`.
- **AC-3 (TurnEnd)** — `Handle(TurnEnd{Reason: TurnEndReasonEndTurn})` writes **no** frame and calls `onTurnEnd` exactly once with `"end_turn"`. Second sub-case: nil `onTurnEnd` → no panic, no frame.
- **AC-4 (Stall)** — `Handle(Stall{})` writes **no** frame. Optionally assert a stderr line via a `bytes.Buffer`-backed `slog` logger (secondary; the hard assertion is the empty transport writer).
- **AC-5 (no spawn)** — structural: `grep` confirms neither new file references `claude -p`, `exec`, or an Agent SDK path; `make check` green.

## Open questions

- **`agent_message_chunk` message boundaries.** ADR 027 gives `agent_message_chunk` no per-message delimiter, so distinct claude messages within one turn concatenate host-side. If a future ACP revision or host needs explicit message boundaries, the discarded `msgID` is the hook to revisit — out of scope here (no AC asks for it).
- **Stall log level.** `Warn` (degraded) vs `Info` (lifecycle) for the stderr surfacing — minor; `Warn` chosen to match "degraded operation" in CODING-STYLE. Not load-bearing.

## Scope self-check

Production source files created: **2** (`internal/acp/notify.go`, `cmd/pyry/acp_turn_stream.go`) — under the ≥5 gate. New exported types: **0** (`Notify` is a method; `notification`/`sessionUpdateParams`/`acpTurnStream` are unexported). Total written work ≈ 290 LOC. No edit fan-out (all additive; `Notify` is a new method with zero existing call sites). No reject-branch state machine. Solidly `s`.
