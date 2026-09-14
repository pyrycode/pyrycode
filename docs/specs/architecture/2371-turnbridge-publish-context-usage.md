# #2371 — turnbridge: publish context usage on the interactive client path

## Files read

- `internal/turnevent/event.go` → `ContextUsage`, `ContextUsageCategory`, `ContextUsageMCPTool`,
  `ContextUsageMemoryFile` — the source event's exact field set and the three independent
  dropped counts this arm must carry unchanged.
- `internal/protocol/interactive.go` → `ContextUsagePayload`, `MarshalJSON`, the three row
  types — the declared wire contract (#2370). Its doc is the single source of truth for
  mixed provenance, the "bounds are not re-decided here" rule, and the `ServerName`
  name-collision trap with `MCPReconnectPayload`.
- `internal/turnbridge/outbound.go` → `MapEvent`, arms `turnevent.MCPStatus`,
  `turnevent.ModelList`, `turnevent.SlashCommandList` — the status-peer mapping shape this
  arm copies: conversation identity only, fresh outer slice, nothing re-capped or re-sorted.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`, `emitMapped`, `emit`, `eventKind` — the
  emitter arm's shape (`flushDelta` then `emitMapped`, no lifecycle mutation) and the single
  capability gate inside `emit`.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — confirms the variant falls to the
  whitelist's `turnMarkNone` default. **No change needed**;
  `TestTurnMarkFor_TotalOverEveryVariant` already pins it.
- `cmd/pyry/stream_context_usage.go` → `turnEndContextUsageRequester.Sink` — the post-turn
  ask (#2289). **Unconditional**: no env rider gates it, which decides the e2e's shape.
- `internal/streamsup/parser.go` → `emitContextUsage`, `contextUsageResponseLine` — what the
  producer actually constructs. Confirms `Model` is bounded at 256 B (whole reading rejected
  over it), and that the decode target excludes `color` and `isDeferred`.
- `internal/streamsup/context_usage_bound.go` → `boundContextUsageEntries`,
  `maxContextUsageEntries` (32), `maxContextUsageStringBytes` (256) — the producer's bounds,
  and the arithmetic behind the measured dropped counts below.
- `internal/e2e/internal/fakeclaude/main.go` → `writeContextUsageAck`, `cannedContextUsage`,
  `cannedContextUsageMCPTools` — the canned hostile payload the e2e asserts against.
- `internal/e2e/relay_v2_stream_session_facts_test.go` → `driveSessionFactsTurn`,
  `TestRelayV2_StreamSessionFactsReachesConnectedPhone` — the worked arrival-proof analogue,
  including the raw-bytes key-set assertion.
- `cmd/pyry/interactive_turn_v2_test.go` → `TestInteractiveTurnEmitterV2_MCPStatusFansOutAndRecordsOnce`,
  `TestInteractiveTurnEmitterV2_MCPStatusPreservesMidTurnSequence`,
  `TestInteractiveTurnEmitterV2_MCPStatusEventKindIsContentFree` — the unit-tier harness
  (`stubCursor`, `fakeInteractiveBcast`, `pushesFor`) and the non-interactive-conn gate proof.
- `internal/relay/v2session_history_request.go` → `maxAppEnvelopeBytes` — the 65519-byte cap,
  and the fact that an over-cap envelope is **rejected by the transport** rather than
  truncated. Feeds the one substantive security finding below.

## Context

#2370 declared `protocol.TypeContextUsage` and `ContextUsagePayload`; #2357/#2291 shipped the
`turnevent.ContextUsage` source event; #2289 wired the automatic post-turn ask and the
fake-Claude answer. Both contracts stand and neither needs work. What is missing is the joint:
`MapEvent` has no arm for the variant, so `emitMapped` would drop it, and
`interactiveTurnEmitterV2.Handle` has no case, so today the event falls through `default` and
Debug-logs `kind=unknown`.

This slice adds one mapper arm, one emitter arm, and one `eventKind` arm. No new type, no new
path, no new gate.

**Sizing.** Re-counted against this written plan: 2 production files, 0 new exported types, 0
consumer call sites, 5 acceptance criteria, 0 reject branches — all inside the boundary. Total
written work is over the 800-line ceiling (the refiner estimated ~1050). #2371 is a grandchild
(#2205 → #2292 → #2371), so the depth gate bars a third split level; `needs-human:sizing` is
already on the ticket with the refiner's recorded split. Building as one ticket, per the gate.

**No ADR warranted.** This arm decides nothing new; it applies decisions #2370 and #2289 already
recorded.

## Design

### `internal/turnbridge/outbound.go` — one arm in `MapEvent`

Placed with the status peers, after the `turnevent.SlashCommandList` arm. Contract:

```
case turnevent.ContextUsage:
    → (protocol.TypeContextUsage, protocol.ContextUsagePayload{...}, true)
```

- `ConversationID` is taken from `tc.ConversationID` and is **the only value the mapping
  supplies**. `tc.TurnID` and `tc.Seq` are ignored; the payload has no field for either.
- Every other field crosses 1:1 and verbatim: `Model`, `TotalTokens`, `MaxTokens`,
  `Percentage`, and the three dropped counts. Nothing is recomputed, re-sorted, re-capped,
  charset-checked or defaulted.
- Each inventory is rebuilt as a **fresh outer slice** by a read-only `append` loop over the
  event's rows, element-wise into the `protocol` row type. A nil inventory stays nil, so
  `ContextUsagePayload.MarshalJSON` remains the sole owner of nil→`[]`.
- No suppression branch. A zero-value `ContextUsage` maps, like every status peer above.
- `MapEvent` never refuses this variant, so `emitMapped`'s unmapped-drop stays unreachable
  for it.

Three points the arm's comment must carry, because each is a trap a later reader would
"fix" wrongly:

1. **`MemoryFile.Path` is not normalised.** No `filepath.Clean`, `Join`, `Abs`, or `Stat`.
   The protocol type's doc states the rule; the mapper is the layer most tempted to break it,
   because the value is path-shaped. Normalising would imply the frame names a real file it
   acts on, which it does not.
2. **`MCPTool.ServerName` is inert here** and collides in name with `MCPReconnectPayload`'s,
   which *is* an actuation target. No helper may be shared between the two arms.
3. **Carry-never-mutate holds by construction, and for a different reason than `ModelList`'s.**
   All three row types are flat scalars — no slice field anywhere — so the element-wise copy is
   a total copy and shares no backing array. There is no `sessionModelHold` analogue retaining
   a `ContextUsage`. The rule is stated so that a reader does not add a deep copy that is
   already total, nor assume the `ModelList` data-race hazard applies.

### `cmd/pyry/interactive_turn_v2.go` — one arm in `Handle`, one in `eventKind`

`Handle`, placed with the status peers after `turnevent.SlashCommandList`:

```
case turnevent.ContextUsage:
    e.flushDelta(ctx)
    e.emitMapped(ctx, convID, ev)
```

Identical body to its three neighbours, kept a separate case per this switch's own rule
(arms merge when they share a *reason*, not a body). The reason that distinguishes it is the
**arrival order**: the reading is solicited by `turnEndContextUsageRequester.Sink` *after*
`TurnEnd` is sunk, so it lands with the turn already closed. An arm that called
`startTurnIfNeeded` would mint a turn nothing will ever end — exactly the hazard the
`ModelList` arm names — so no lifecycle state is touched: `inTurn`, `turnID` and
`currentState` are unchanged across the event.

`flushDelta` first, so buffered text keeps its wire position ahead of the report. No
capability gate in the arm: `emit` filters once for every frame type, and a second gate here
is how that single gate stops being single.

`eventKind`: return the variant name `"context_usage"` and **no field of the event**. The
temptation here is larger than for any neighbour — `Model`, every category `Name`, every MCP
`Name`/`ServerName`, and every memory-file `Path`. None is returned, and neither is any list
length or dropped count.

`turnMarkFor` needs **no change**: its opener set is a whitelist and the default answers
`turnMarkNone`, which is correct — the event arrives after the turn closed.

## Concurrency model

No goroutine is created or changed. `Handle` runs on the parser's stdout-forwarder goroutine,
serially in stream order, as every other arm does. The mapper is pure.

Carry-never-mutate is satisfied structurally rather than by discipline: the three row types
hold only `string` and `int`, so the element-wise copy shares no mutable backing array. String
headers are shared, which is safe because Go strings are immutable. No value is retained past
the call by either layer.

## Error handling

No new failure mode and no new error value. The paths that can drop the frame, all pre-existing:

| Path | Site | Reachable for this variant? |
|---|---|---|
| No conversation cursor | `Handle`'s no-cursor guard | Yes — returns before the type switch |
| Unknown event | `Handle`'s `default` | No, once this arm lands |
| No wire mapping | `emitMapped`'s unmapped drop | No — `MapEvent` never refuses the variant |
| Payload marshal | `emit`'s marshal guard | Defensive only; the payload is a closed scalar struct |
| Push failure | `emit`'s per-conn push guard | Yes, on a dropped conn |
| Fan-in droppable refusal | `streamTurnSink`'s `droppableCap` | Yes — `turnMarkNone` classes it droppable |

Delivery on this lane is **best-effort by construction** and this arm does not pretend
otherwise. The reading is re-asked after the next turn, so a loss is self-repairing in a way
an inventory's is not.

**Every one of the log sites above must stay content-free.** `eventKind` returns the variant
name; `marshal_err` logs `conversation_id` and `turn_id` only; `push_err` adds `conn_id`,
`env_id` and a transport `err` that carries no payload bytes. AC5 is asserted rather than
argued — see Testing strategy.

## Testing strategy

### Measured inputs (not assumed)

The fake's canned payload through `boundContextUsageEntries` yields, measured:

| List | Source | Rejected (>256 B) | Retained | Dropped |
|---|---|---|---|---|
| Categories | 4 | 0 | 4 | **0** |
| MCP tools | 33 | 1 (a 317-byte name) | 32 | **1** |
| Memory files | 3 | 1 (a 325-byte path) | 2 | **1** |

**Correction to the ticket's Technical Notes**, which claim "three dropped counts come out at
three different values, one of them zero". Measured, they are **0 / 1 / 1** — two distinct
values. Independence is still observable and the assertion still earns its place: zero
separates no-loss from loss, and the two ones are reached with different retained counts (32
and 2) and by different mechanisms in the cap (a full 32-entry list versus a 2-entry one). The
e2e asserts the measured numbers.

Category order after the descending sort: tools (4200), messages (2600), system prompt (1800),
deferred tools (900). Memory files: preferences (31), project (19), each with an empty `type`
— the canned entries carry no `type` key and the decode target declares one.

### `internal/turnbridge/outbound_test.go` (unit)

Table-driven where the shape allows, mirroring the `ModelList`/`MCPStatus` tests:

- Full carry: every scalar and all three dropped counts reach the payload unchanged, with a
  fixture whose three dropped counts are mutually distinct so a cross-wired pair reddens.
- `ConversationID` is supplied from `TurnContext`; `TurnID`/`Seq` are set on the context and
  must appear nowhere in the payload.
- Nil inventories stay nil at the struct boundary, and reach the wire as `[]` via
  `MarshalJSON` — asserted on bytes, so the layer that owns the normalisation is pinned.
- Order is preserved, not re-sorted: a deliberately unsorted fixture crosses in source order.
- Over-long strings are **not** re-capped here: a 300-byte name crosses whole.
- A traversal-shaped memory path (`../../etc/passwd`-shaped) crosses byte-for-byte, pinning
  the no-normalisation rule by test rather than by comment.
- On-the-wire key sets: exact top-level key set and exact per-row key sets for all three row
  types, asserted against raw bytes.
- A measurement test recording the worst-case marshalled size against
  `maxV2AppEnvelope` — see the security finding; it documents the headroom rather than
  enforcing a cap.

### `cmd/pyry/interactive_turn_v2_test.go` (unit)

- **Lifecycle neutrality**: `inTurn`, `turnID`, `currentState` unchanged across the event,
  both from idle and mid-turn, and specifically *after* a `TurnEnd` — the real arrival order.
- **Fan-out and the capability gate** (AC4): three conns, two interactive and one not; the
  non-interactive conn receives zero envelopes. Ring append and event-id identity asserted
  once, as the `MCPStatus` test does.
- **Ordering**: a pending delta is flushed ahead of the frame; the turn's `turn_end` precedes
  it in the push sequence.
- **`eventKind` content-free** and the AC5 non-leak sweep: a hostile fixture whose category
  names, MCP tool and server names, and memory paths are distinctive needles, driven through
  the no-cursor drop *and* through a failing-push `emit` with a Debug-level logger; no needle
  appears in the captured log, and `kind=context_usage` does.

### `internal/e2e` (fake-daemon, AC3)

New file modelled on `relay_v2_stream_session_facts_test.go`. One completed turn from a
connected interactive v2 client; exactly one `context_usage` frame arrives, after that turn's
`turn_end`, carrying the measured values above, with exact top-level and per-row key sets
asserted against raw bytes.

**Two structural departures from the analogue, both forced:**

1. **No rider, so no rider-off control.** `turnEndContextUsageRequester` is unconditional —
   every completed turn asks. The vacuity guard is therefore the milestone assertions
   (`sawEcho`, `sawTurnEnd`) asserted first and fatally, plus the exactly-one count and the
   ordering claim, rather than a negative twin.
2. **The drain must continue past `turn_end`.** The analogue terminates *on* `turn_end`; this
   frame arrives after it. The loop drains to `turn_end`, records its ordinal, then continues
   through a bounded settle window (the ~2 s idiom
   `relay_v2_stream_slash_command_list_reconcile_test.go` uses) so "exactly one" is an exact
   claim rather than a first-sighting.

Ordering is asserted by recorded ordinal, not by timing.

### Gate

`go test -race` on `./internal/turnbridge/...`, `./cmd/pyry/...`, `./internal/e2e/...`, plus
`go vet ./...` and `go build ./cmd/pyry`. The full-module race suite is the verifier's gate.

## Open questions

1. **Do the fake's dropped counts match the ticket's claim?** Resolved before writing this
   plan by measurement: no — they are 0/1/1, not three distinct values. Recorded above; the
   e2e asserts the measured numbers.
2. **Does the mapper need a frame byte cap, as `SlashCommandList` did?** Resolved: not in this
   slice — AC1 forbids it and #2370's contract assigns bounds to the producer. The arithmetic
   and the residual risk are recorded as a security finding and will be filed as a follow-up
   ticket.
3. **How large a settle window does the e2e need?** To resolve during implementation by
   running the test; start at the repo's 2 s idiom and only widen if it proves flaky.

## Documentation handoff

Pending for the documentation stage. Not done here, per the builder's file restrictions.

1. **`docs/protocol-mobile.md`** — update the `context_usage` section and its `2026-09-14`
   changelog entry, which currently reads **Declared, not yet emitted**, to record that the
   daemon now publishes the frame after receiving Claude's solicited post-turn reading.
   Preserve the distinction from the existing transcript-derived snapshot percentage: neither
   reading replaces the other in this slice.
2. **`docs/knowledge/features/turnbridge-package.md`** — the per-variant `MapEvent` table needs
   a row for this arm. **Blocked on a split first**: at `d683c9b7` the document is 49957 bytes
   against `cmd/docs-guard`'s 50000-byte cap — 43 bytes of headroom, where rows in that table
   run past a thousand. Adding the row reddens `make check` until the document is split under
   CLAUDE.md's package-overview rule. This PR touches neither file, so its own gate stays green.

## Security review

**Verdict:** PASS

This slice's whole security character is that it is the **first producer to put
claude- and workspace-authored descriptive text onto the encrypted client path** for this
frame. Every finding below is about what that text may and may not touch.

**Findings:**

- **[Trust boundaries]** No MUST FIX. The boundary is explicit and singular: `emitContextUsage`
  is where claude's stdout becomes a bounded `turnevent.ContextUsage` (every string ≤ 256 B —
  `Model` included, the whole reading rejected over it — every list ≤ 32 entries). This arm is
  downstream of it and re-decides nothing. Downstream callers are told which side they hold by
  `protocol.ContextUsagePayload`'s own doc, which declares the value mixed-provenance and
  requires a client to render each string as inert text. Concrete decision recorded so it is not
  silently reversed: **the mapper adds no validation, no allow-list and no charset check**, for
  the reason `SessionFacts`' arm gives — a membership test here would drop the first report of
  something nobody has heard of, which is the case an operator most needs to see.

- **[Trust boundaries — name collision]** SHOULD FIX, addressed in Phase B by comment and by
  construction. `protocol.ContextUsageMCPTool.ServerName` is inert, and collides in name with
  `MCPReconnectPayload.ServerName`, which crosses an actuation seam. The realistic exploit is a
  *confused developer*, not a remote attacker: a later change that shares a helper between the
  two arms, or a client that feeds this `ServerName` to an MCP verb on the strength of having
  seen it here. Mitigation: no helper is shared between the arms, and the arm's comment names
  the collision. The payload type's doc already carries the client-side half.

- **[Tokens, secrets, credentials]** Not applicable, by design rather than by luck. The event
  carries no credential material: `Model` is a claude-authored label, the integers are token
  *counts*, and the three inventories name categories, MCP tools and memory files. No token is
  generated, stored, compared or logged on this path. The frame rides the existing Noise-sealed
  conversation lane; this arm mints no key material and no identifier beyond the envelope id
  `emit` already assigns.

- **[File operations]** No findings, and this is the category most at risk of a well-meaning
  regression. `ContextUsageMemoryFile.Path` is path-shaped but is **never** joined, cleaned,
  resolved, stat'ed, opened or matched — not by this arm and not by anything it calls. The arm
  adds zero filesystem operations. A `filepath.Clean` added "for tidiness" would be the bug:
  it would imply the frame names a real file the daemon acts on. Pinned in Phase B by a
  traversal-shaped path in the mapper fixture that must cross byte-for-byte, so the rule is
  enforced by a failing test rather than by this paragraph. No file is created, so file modes
  and atomic-write discipline do not arise.

- **[Subprocess / external command execution]** No findings. The enumeration of new sinks for
  these strings is exactly one: fields of a `protocol.ContextUsagePayload`. No `exec.Command`
  argument, no `filepath.Join`, no `filepath.Match`, no `regexp`, no log attribute, no
  environment variable. This is the `SlashCommandList` arm's enumeration, restated because the
  strings here are the same trust class or lower.

- **[Cryptographic primitives]** No findings. No randomness, no comparison against a secret, no
  key or nonce decision is introduced. The frame is sealed by the existing `bcast.Push` path,
  which owns the per-conn nonce sequence; this arm produces one logical event and lets `emit`
  fan it out, so it cannot desync a `CipherState` by construction.

- **[Network & I/O]** **SHOULD FIX — the one substantive finding, deferred with reasons and a
  follow-up ticket.** The frame has no byte budget, unlike `SlashCommandList`, whose arm carries
  `maxSlashCommandListBytes` for exactly this reason. Worst case, measured against the producer's
  caps: 32 categories × ~288 B + 32 MCP tools × ~561 B + 32 memory files × ~554 B + scalars ≈
  **45.5 KB raw**, against `maxAppEnvelopeBytes` (65519) — it fits, with ~20 KB headroom. But
  `json.Marshal` HTML-escapes `<`, `>` and `&` to six bytes each, so roughly **4,000 such
  characters — about 9% of the string content — exhausts the headroom**, and an over-cap
  envelope is *rejected by the transport with `message.too_long`*, not truncated.

  Why this is SHOULD FIX and not MUST FIX: the consequence is one **lost informational frame on
  a lane that is best-effort by construction**, and the reading is re-asked after the next turn,
  so the loss self-repairs. The total stays bounded at ~245 KB worst case by the producer's caps,
  so it is not a resource-exhaustion vector. The reachable actor is whoever can name files or MCP
  servers in the operator's own workspace — someone who already has far more capability than
  suppressing one breakdown frame. And fixing it *here* would contradict two standing decisions:
  AC1 forbids the mapper re-capping, and #2370's committed contract assigns bounds to the
  producer, naming a second cap in the protocol layer as "a second place the limit is decided,
  free to disagree silently".

  Phase B action: implement per AC (no cap), **add a measurement test** recording the worst-case
  marshalled size and the surviving headroom against `maxV2AppEnvelope` so the claim is pinned by
  arithmetic rather than by this paragraph, and **file a follow-up ticket** for the budget
  decision. Named as out of scope here rather than left implicit.

- **[Error messages, logs, telemetry]** No MUST FIX; AC5 is the category's own acceptance
  criterion and is asserted rather than argued. MUST-NOT-log on this path: any category name, MCP
  tool or server name, memory-file path, or `Model`. MUST-log: the variant name and the
  daemon-authored identifiers `emit` already carries. Four sites walked: `eventKind` returns the
  variant name only; `Handle`'s unknown-event drop becomes unreachable once this arm lands but
  still reads through `eventKind`; `emit`'s marshal guard logs `conversation_id` and `turn_id`
  and deliberately omits `err.Error()` — `encoding/json` quotes invalid input bytes into its
  error, which is the specific leak that guard's comment exists to prevent; `emit`'s push guard
  adds `conn_id`, `env_id` and a **transport** error that carries no payload bytes. The residual
  risk is that the last claim is inherited rather than checked, so Phase B drives a *failing
  push* with a hostile fixture and asserts no needle reaches the log. No telemetry or metric is
  added.

- **[Concurrency]** No findings, and the reasoning differs from the neighbouring arm's in a way
  worth recording so nobody "restores" a protection that is already total. All three row types
  are flat `string`/`int` structs with no slice field, so the element-wise copy into a fresh
  outer slice is a complete copy sharing no mutable backing array; shared string headers are safe
  because Go strings are immutable. Nothing retains a `ContextUsage` past the call — there is no
  `sessionModelHold` analogue — so the cross-goroutine hazard that makes `ModelList`'s
  carry-never-mutate rule load-bearing does not arise here. No lock is taken, no goroutine is
  spawned, and `Handle` keeps running serially on the parser's stdout-forwarder goroutine.

- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model assigns
  who may see a conversation's events to pairing, capability negotiation and conversation
  binding, all of which are already decided before `emit`. This arm adds **no path and no second
  gate**: it publishes through the existing conversation-keyed fan-out, and the interactive
  capability is filtered once inside `emit`. AC4 pins both halves. Explicitly out of scope and
  named: the byte-budget finding above, and #2293's on-demand reply, which will reuse this same
  payload type through a different verb.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14
