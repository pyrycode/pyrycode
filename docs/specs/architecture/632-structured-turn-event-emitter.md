# Spec #632 — Event-stream bridge: structured turn-event emitter

**Part of EPIC #596** (Phase 2 structured streaming). See [ADR 025](../../knowledge/decisions/025-mobile-remote-head-interactive-session.md) § Phase 2.
**`security-sensitive`** — see § Security review at the end.

## Context

This is the **stateful structured emitter** at the heart of Phase 2. Its two upstream
siblings are merged: the pure mapping adapter (#627 — `MapEvent`/`BuildTurnState` in
`internal/turnbridge/outbound.go`) and the capability negotiation (#626 — the per-conn
`interactive` flag plus `ActiveConns`). #627 deliberately left the **turn-state state
machine to its consumer**. This slice is that consumer: it consumes structured
`turnevent.Event`s, derives `turn_state` statefully, maps content events to v2
envelopes, and fans each envelope only to `interactive`-granted v2 conns.

It mirrors #589's fan-out shape but consumes structured events instead of PTY chunks,
derives `turn_state`, and gates on the `interactive` grant. It does **NOT** wire the
live production producer (sibling #633 — the emitter is exercised here against an
injected event source so it unit-tests without a live claude) and does **NOT** touch
#589's coarse path.

## Design

### One new file, no edits to existing production files

`cmd/pyry/interactive_turn_v2.go` (new), `package main`, **all unexported, zero new
exported types** (mirrors #589). The reuse seams are referenced, not modified:
`cursorReader` (assistant_turn.go), `relay.ActiveConn`/`Push`/`ActiveConns`,
`turnbridge.MapEvent`/`BuildTurnState`/`TurnContext`/`TurnState`/`State*`,
`turnevent.*`, `protocol.Envelope`, `conversations.NewID`. Production wiring
(constructing the `turnbridge.Producer` and attaching `OnEvent`) is **#633's** edit to
`cmd/pyry/relay.go` — not this slice.

Imports: `context`, `encoding/json`, `log/slog`, `time`, `internal/conversations`,
`internal/protocol`, `internal/relay`, `internal/turnbridge`, `internal/turnevent`.

### Consumer-declared broadcaster interface (CODING-STYLE: interface at the consumer)

A new minimal interface, distinct from #589's `v2Broadcaster` (which uses the
capability-agnostic `ActiveConnIDs`). `*relay.V2SessionManager` satisfies it.

```go
// interactiveBroadcaster is the capability-aware fan-out surface the structured
// emitter needs: the interactive-conn snapshot (#626) and the per-conn sealed
// push (#571). *relay.V2SessionManager satisfies it.
type interactiveBroadcaster interface {
	ActiveConns(ctx context.Context) []relay.ActiveConn
	Push(ctx context.Context, connID string, env protocol.Envelope) error
}
```

### The emitter type — a single-goroutine state machine

`interactiveTurnEmitterV2` holds the broadcaster, the cursor reader, the logger, and
the **lifecycle state** — all plain fields, no atomics, no mutex (read/written only on
the single goroutine that calls `Handle`; see § Concurrency):

| field | role |
|---|---|
| `inTurn bool` | whether a turn is currently open |
| `turnID string` | the current turn's id (minted at turn start) |
| `seq int` | per-turn assistant-delta counter; reset to 0 at each turn boundary |
| `currentState turnbridge.TurnState` | last-emitted turn_state, for transition de-dup |
| `nextID uint64` | **session-monotonic** envelope-ID counter; **never reset across turns** |

Constructor `newInteractiveTurnEmitterV2(sup cursorReader, bcast interactiveBroadcaster,
logger *slog.Logger) *interactiveTurnEmitterV2`.

### Entry point — `Handle(ctx context.Context, ev turnevent.Event)`

The emitter is driven one event at a time. The method takes a `ctx` (needed for
`ActiveConns`/`Push`); **#633** wires it to the producer's ctx-less `OnEvent` seam via a
closure that captures the relay lifecycle ctx — `OnEvent: func(ev) { emitter.Handle(relayCtx, ev) }`
— keeping `context.Context` out of the struct (the documented Go anti-pattern). Unit
tests call `Handle` directly with a scripted sequence.

`Handle` reads the conversation cursor once (`sup.CurrentConversation()`); on `""` it
drops the event with a DEBUG log and returns (mirrors #589's no-cursor drop). Otherwise
it type-switches:

| `ev` concrete type | Lifecycle action | Emitted envelope(s), in order |
|---|---|---|
| `ThoughtChunk` | `startTurnIfNeeded`; `transitionTo(StateThinking)` | `turn_state{thinking}` (only if transitioning). **Thought text never forwarded.** |
| `TextChunk` | `startTurnIfNeeded`; `transitionTo(StateResponding)`; `MapEvent`→emit; then `seq++` | `turn_state{responding}` (if transitioning), then `assistant_delta{…, seq}` |
| `ToolStart` | `startTurnIfNeeded`; `transitionTo(StateResponding)`; `MapEvent`→emit | `turn_state{responding}` (if transitioning), then `tool_use{…}` |
| `ToolUpdate` | `startTurnIfNeeded`; `transitionTo(StateResponding)`; `MapEvent`→emit | `turn_state{responding}` (if transitioning), then `tool_result{…}` |
| `TurnEnd` | if `inTurn`: `MapEvent`→emit; `transitionTo(StateIdle)`; `endTurn` | `turn_end{stop_reason}`, then `turn_state{idle}` |
| nil / unknown | drop, DEBUG log | — |

- **`startTurnIfNeeded`** — no-op if `inTurn`. Else mint a turn id
  (`conversations.NewID()` — UUIDv4 via `crypto/rand`, the same generator #589 uses for
  `MessageID`; on its rare error, WARN and return without opening the turn so the next
  event retries), set `seq=0`, `inTurn=true`, `currentState=""` (no state emitted yet).
- **`transitionTo(ctx, convID, state)`** — de-dup: return if `currentState == state`.
  Else set `currentState = state`, call `BuildTurnState(convID, state)`, and `emit` it.
  State-change-based emission (a superset of "first content → responding") naturally
  handles interleaving (thinking → text → thinking re-emits each transition).
- **`endTurn`** — `inTurn = false`. The next content/thought event re-mints a fresh
  turn id and resets `seq`/`currentState`.
- `TurnContext` for content arms is built per-event as
  `{ConversationID: convID, TurnID: e.turnID, Seq: e.seq}`. `Seq` is consumed only by
  the `TextChunk`→`assistant_delta` mapping (#627); `seq++` happens **only** after a
  `TextChunk` emit, so tool/turn_end events carry the current seq (ignored by their
  mappings) and never advance it.

### The fan-out primitive — `emit(ctx, typ string, payload any)`

The ~25-LOC #589 echo, the one place envelopes reach the wire. Per the AC the envelope
ID policy is **mirrored from #589 exactly**: `nextID++` per conn per envelope.

1. `json.Marshal(payload)` once; on error, DEBUG log (no payload, **no `err.Error()`**)
   and return — defensive, the payloads are closed string/int/bool structs.
2. Snapshot `bcast.ActiveConns(ctx)` (fresh per envelope: a conn that joined mid-turn is
   included next emit; a dropped conn is absent or surfaces as a `Push` error).
3. For each `c`: **`if !c.Interactive { continue }`** (the capability gate — see
   § Security), then `e.nextID++`, build `protocol.Envelope{ID: e.nextID, Type: typ,
   TS: time.Now().UTC(), Payload: payloadJSON}`, and `Push`.
4. A per-conn `Push` error: if `ctx.Err() != nil` return (teardown); else DEBUG log
   (`conn_id`, `env_id`, `conversation_id`, `turn_id`, `err`) and `continue` — a dropped
   conn must not abort the turn for the others (AC#4).

### Data flow

```
turnevent.Event (injected source in tests; #615 producer's OnEvent in #633)
        │  Handle(ctx, ev)   [single goroutine]
        ▼
  cursor read (convID) ──"" → drop
        ▼
  type-switch → lifecycle (startTurn / transition / seq / endTurn)
        │
        ├─ transitionTo → BuildTurnState(convID, state) ─┐
        ├─ content event → MapEvent(ev, TurnContext) ────┤
        └─ TurnEnd → MapEvent (turn_end) + idle ─────────┤
                                                         ▼
                                              emit(ctx, typ, payload)
                                                         │ marshal once; nextID++ per conn
                                                         ▼
                              ActiveConns(ctx) → filter Interactive==true → Push per conn
```

## Concurrency model

- **No goroutine is spawned by this slice.** The emitter is a passive state machine; the
  draining goroutine belongs to the producer, wired by #633. There is no queue, no
  `Run`, no cleanup func here (contrast #589, which needed a buffered queue + `Run`
  goroutine because its source — `Bridge.Write` → observer — ran on the PTY-drain
  goroutine and a slow fan-out would wedge claude). Here the source is the producer's
  Run goroutine draining `Session.Events()`; a slow consumer does **not** wedge claude
  (tui-driver drains the PTY independently and `TailJSONL` reads a file — #593/#615), so
  no non-blocking decoupling queue is required.
- **`Handle` is not safe for concurrent use; it is designed for the producer's single
  Run goroutine** (which calls `OnEvent` serially — producer.go:98-118). All emitter
  state (`inTurn`/`turnID`/`seq`/`currentState`/`nextID`) is therefore read/written on
  one goroutine — no atomics, no mutex (mirrors #589's plain `nextID`).
- **`ActiveConns`/`Push` funnel through the manager's single Run goroutine** (the
  package's no-mutex single-owner idiom), each with a `ctx.Done` escape arm. A `Handle`
  in flight during teardown passes a cancelled ctx → `ActiveConns` returns `nil` (fan
  out to nobody) and `Push` returns `ctx.Err()` → `emit` returns early. The emitter
  never blocks on a winding-down manager.

## Error handling

| Failure | Branch | Log (level, fields) | State effect |
|---|---|---|---|
| Empty cursor | drop event | DEBUG: `event`, `kind` | none |
| Turn-id mint fails (`conversations.NewID`) | drop event | WARN: `event`, `conversation_id` (no err detail beyond the sentinel) | `inTurn` stays false; retried next event |
| `MapEvent` `ok==false` on a content arm | skip emit (defensive; unreachable for Text/Tool/TurnEnd) | DEBUG: `event`, `kind` | none |
| `json.Marshal` payload error | drop that envelope | DEBUG: `event`, `conversation_id`, `turn_id` — **never payload, never `err.Error()`** | none |
| Per-conn `Push` error (ctx live) | DEBUG + `continue` | DEBUG: `event`, `conn_id`, `env_id`, `conversation_id`, `turn_id`, `err` | none (other conns proceed) |
| Per-conn `Push` error (ctx cancelled) | return early | — | none |
| `TurnEnd` while `!inTurn` | drop | DEBUG: `event` | none |

Six branches; each one log call (under the architect 10-branch red line).

## Testing strategy

Unit tests in `cmd/pyry/interactive_turn_v2_test.go` (`package main`, stdlib `testing`
only, `-race`). Drive `Handle` with a scripted `[]turnevent.Event` sequence against a
**fake `interactiveBroadcaster`** (records `(connID, env)` pushes, supports a
per-conn injected error and a configurable `ActiveConns` snapshot) and a **stub
`cursorReader`**. Decode each pushed `env.Payload` to assert the wire shape. Scenarios:

- **turn_state transition order** — script `[Thought, Text, Tool, TurnEnd]` (single
  interactive conn) → assert the emitted `Type`/state sequence:
  `turn_state{thinking}`, `turn_state{responding}` + `assistant_delta`, `tool_use`,
  `turn_end` + `turn_state{idle}`. Add an interleave row (`Thought, Text, Thought,
  Text`) asserting `thinking, responding, thinking, responding` (de-dup proves no
  duplicate same-state emission).
- **per-turn seq reset** — two turns each with two `TextChunk`s → `assistant_delta.seq`
  is `0,1` in turn A and `0,1` in turn B; the two turns carry distinct `turn_id`s.
- **monotonic env-id across turns** — collect `env.ID` across both turns (all envelope
  kinds, single conn) → strictly increasing with **no reset** at the turn boundary.
- **fan-out only to interactive conns** — `ActiveConns` returns a mixed snapshot
  (`{a, interactive}`, `{b, non-interactive}`) → every push targets `a`, none target
  `b`, across every envelope kind. The structured stream never reaches a non-interactive
  conn.
- **mid-turn join** — `ActiveConns` returns `{a}` for the first event then `{a, b}` for
  the next → `b` receives only envelopes from the second event onward.
- **drop-skip on per-conn Push error** — three interactive conns, the middle one's
  `Push` returns `relay.ErrConnNotFound` → the other two still receive the envelope (the
  turn continues); assert no panic, no early abort.
- **no application-output log leak (AC#5)** — capture the slog output (a buffer
  handler), drive a full turn carrying distinctive thought text, assistant text, a tool
  title/input, and a tool result → assert none of those substrings appear in any log
  line and none appear in any field of any non-pushed log; assert thought text never
  appears in any pushed `env.Payload` either.

No e2e in this slice (no production caller until #633); the real sealed round-trip is
#633's deliverable. Scenarios above are the security/behaviour oracle.

## Open questions

- **Turn-id generator.** Spec reuses `conversations.NewID()` (UUIDv4 / `crypto/rand`),
  consistent with #589's `MessageID` minting — a minor, established coupling to
  `internal/conversations` for a generic UUID. If a developer prefers, a 3-line local
  `crypto/rand` UUIDv4 helper is equivalent; reuse is recommended to avoid new surface.
- **Per-conn-distinct `env.ID`.** Mirroring #589, `nextID++` runs per conn, so the same
  logical envelope gets a different `env.ID` on each conn. This is the policy the AC
  mandates ("mirrors #589's nextID policy"). It is monotonic-per-session and each conn
  sees a strictly-increasing subsequence — sufficient for #611's `last_event_id` resync.
  Promotion to a manager-owned per-session counter is #611's call, not this slice's.

## Out of scope (deferred to siblings)

- **Production wiring** — constructing `turnbridge.Producer` with
  `NewSessionSubscriber` + the JSONL resolver + live-`/clear` re-subscription, attaching
  `OnEvent: emitter.Handle(ctx, …)`, and the foreground gate / cleanup in
  `cmd/pyry/relay.go`. **Sibling #633.**
- **Retiring #589's coarse `message` path** — its own sibling slice re-targets that;
  untouched here.
- **Backpressure / delta coalescing / droppable-delta policy** and **mid-turn reconnect
  replay (#611) / event ring** — separate Phase 2 children per ADR 025 § Backpressure.
  This slice emits synchronously and unbuffered.

## Security review

**Verdict:** PASS

This pass was run adversarially against the spec above, assuming it has holes. The
ticket's stated threat focus: *a non-capable (or capability-spoofing) phone must never
receive the structured stream, and per-conversation envelopes must reach only the conns
for that conversation* (#607 deferred the gated-fan-out review onto this slice).

**Findings:**

- **[Trust boundaries]** No findings. The load-bearing boundary is *which conns receive
  the structured stream*, and it is a **single explicit gate** — `if !c.Interactive { continue }`
  in `emit` — backed by two different-fabric deterministic nets: `ActiveConns` enumerates
  only `V2StateOpen` (token-authenticated) sessions, and `Push` independently re-gates
  `V2StateOpen` (`ErrSessionNotOpen`). The emitter never constructs a `conn_id` from
  event data; it only iterates the manager's snapshot, so hostile claude output can never
  redirect delivery. The inbound boundary (claude output → `turnevent.Event`) carries the
  operator's own session content to the operator's own authorized devices — no privilege
  crossing.
- **[Trust boundaries — capability flag integrity]** No findings. The emitter *trusts*
  `ActiveConn.Interactive`, whose integrity is #626's (reviewed PASS): the flag is built
  by iterating the daemon's *authoritative* `supportedV2Capabilities` (spoofing impossible
  by construction), written only on the token-OK branch, fail-closed `false` by default.
  This slice consumes it correctly (filters `== true`) and adds no path that could set or
  forge it.
- **[Trust boundaries — per-conversation confinement]** OUT OF SCOPE (named).
  The fan-out broadcasts to *every* interactive conn (the #589 shape), not a
  `conversation_id`-subscribed subset. This is **not a confidentiality leak today**: the
  daemon hosts one supervised claude with a single `CurrentConversation()` cursor, and all
  paired devices belong to the one operator — every open interactive conn is that
  operator's own device viewing the one live session. Each payload carries
  `conversation_id` for client-side filtering. A true `conversation_id → conn_id`
  subscription map is deferred to **pyrycode-mobile#336** (the #589 deferral, inherited).
- **[Tokens, secrets, credentials]** No findings — N/A. The emitter is downstream of all
  credential handling; it never sees a token or key. Authentication happened in #626's
  handshake; sealing happens inside `Push` (#571) on the manager's goroutine. The emitter
  hands `Push` a plaintext `protocol.Envelope` and touches no nonce/key.
- **[File operations]** No findings — N/A. No path handling, no file I/O.
  `conversations.NewID()` reads `crypto/rand`, not the filesystem.
- **[Subprocess / external execution]** No findings — N/A. None.
- **[Cryptographic primitives]** No findings. Turn-ids are minted via
  `conversations.NewID()` = `crypto/rand` UUIDv4 (PROJECT-MEMORY); a turn-id is a grouping
  key, not a secret or capability, so even non-crypto RNG would be acceptable — `crypto/rand`
  is strictly fine. No crypto is *performed* here; AEAD sealing + the nonce single-writer
  funnel are `Push`/#571's (reviewed PASS).
- **[Network & I/O]** No findings. No direct socket I/O. `assistant_delta.Text` is not
  bounded by this slice, but it is claude's own token-bounded incremental output, and the
  Noise seal caps plaintext at the frame limit — an over-large chunk fails `Encrypt`,
  surfaces as a per-conn `Push` error, and is dropped + DEBUG-logged + skipped (no crash,
  no DoS). Coalescing/chunking large deltas is the deferred backpressure child (ADR 025
  § Backpressure). Fan-out is O(open conns) per envelope, conns bounded by paired devices —
  no amplification introduced.
- **[Error messages, logs, telemetry]** No findings — AC#5 is the design's spine. Every
  log branch (§ Error handling) carries only `event`, `kind` (the event-type discriminant,
  not content), `conversation_id`, `turn_id`, `env_id`, `conn_id`, and `err`. Application
  output — assistant text, **thought text** (never even mapped), tool title/input/result
  summaries — is NEVER logged at any level. The marshal-error branch explicitly omits both
  the payload and `err.Error()` (the #589 lesson: `encoding/json` echoes invalid input
  bytes into its error string). **Deliberate carve-out, documented so code-review does not
  flag it as a regression:** the per-conn `Push`-error branch logs `err` verbatim,
  mirroring #589 (reviewed PASS) — `Push`'s errors are transport sentinels
  (`ErrConnNotFound`/`ErrSessionNotOpen`), and its internal seal/marshal sub-errors operate
  on the already-validated `json.RawMessage` this emitter produced in `emit` step 1, so no
  app content is reachable through them. No telemetry.
- **[Concurrency]** No findings. Zero goroutines spawned by this slice → no leak possible
  (strictly better than #589). Emitter state is lock-free because `Handle` runs only on the
  producer's single Run goroutine (producer.go contract; mirrors #589's plain `nextID`).
  **Named assumption for #633 to verify:** the wiring closure must invoke `Handle` from
  that one goroutine only — concurrent invocation would race the unguarded counters. The
  snapshot-then-`Push` gap is the #588/#589 reviewed pattern; `Interactive` is set-once
  (#626, never mutated, re-key preserves it) so there is no TOCTOU on the flag, and `Push`'s
  `V2StateOpen` re-gate catches a conn that left the open state in the gap. ctx-cancel
  mid-`Handle` drains cleanly via the `ActiveConns`/`Push` `ctx.Done` arms; the emitter
  holds only in-memory counters, so no partial cross-process state can corrupt.
- **[Threat model alignment]** All relevant `protocol-mobile.md` § Security model / ADR 025
  threats addressed: capability-spoofing phone (defeated by #626's authoritative-set
  construction + the `Interactive` gate + two `V2StateOpen` nets), old/non-interactive
  phone (`Interactive==false` → skipped; still served by the untouched #589 coarse path),
  unauthenticated peer (`V2StateOpen` gate ×2), per-conversation confinement (satisfied in
  the single-operator/single-conversation model; routing deferred to pyrycode-mobile#336).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-08
