# Spec #639 — Bridge & fan-out the stall to phones

**Ticket:** [#639](https://github.com/pyrycode/pyrycode/issues/639) — `stall_detected → Stall → stall envelope → capability-gated push`
**Epic:** #596 (Phase 2 structured streaming). Split from #624 (child B of 2).
**Depends on:** #638 (child A) — **CLOSED & merged to main** (`turnevent.Stall`, `protocol.StallPayload`, `protocol.TypeStall` all present). Block cleared; ticket is in architecture.
**Size:** S (additive only; near XS — 3 production files, ~16 production LOC, 0 new exported types, 0 consumer call-site cascade).
**Label:** `security-sensitive` — security-review pass appended below (verdict **PASS**).

---

## Files to read first

The developer's turn-1 data load. Every entry is on the existing turn-event path; the stall is an additive `case` at three already-built switch sites plus the two tests that currently assert the drop.

- `internal/turnevent/event.go:72-78` — `turnevent.Stall struct{}` (child A). The internal variant you map *to*. Onset-only, no fields, no conversation identity.
- `internal/protocol/interactive.go:79-89` — `protocol.StallPayload{ConversationID string}` + (`codes.go:105`) `TypeStall = "stall"` (child A). The wire form you map *to*. Carries `conversation_id` only — no `turn_id` (not turn-scoped), no clearing field (onset-only).
- `internal/turnbridge/mapper.go:11-32` — `mapEvent`'s switch. `EventKindStallDetected` currently falls through `default: return nil, false` (the drop). **Add the case here.** The doc comment at lines 16-18 names "the stall marker" among the dropped kinds — **update it.**
- `internal/turnbridge/outbound.go:49-97` — `MapEvent`'s type-switch. `turnevent.Stall` currently hits `default: return "", nil, false`. **Add the case here** returning `TypeStall` + `StallPayload`. Mirror the `TurnEnd` arm's shape (lines 87-92).
- `cmd/pyry/interactive_turn_v2.go:80-129` — `Handle`'s type-switch (the consumer). `turnevent.Stall` currently hits `default` → debug-drop. **Add the case** — emit via `emitMapped`/`emit` with **no lifecycle mutation**. Also extend `eventKind` (lines 239-254) with a `stall` arm, and the `emitMapped` reachability comment (lines 173-176).
- `cmd/pyry/interactive_turn_v2.go:196-235` — `emit()`: the single capability-gated fan-out point (`c.Interactive` gate + per-conn `Push`). The stall rides this unchanged. **Read it to see why no new dispatch logic is needed.**
- `cmd/pyry/interactive_turn_stream_v2.go:45-81` — `startInteractiveTurnStreamV2`: producer→emitter wiring (already built). Confirms `OnEvent → emitter.Handle`. The comment at line 54 ("drives only the dropped stall arm, which the mapper discards anyway") becomes stale — **update it** (the stall is no longer discarded).
- `internal/turnbridge/producer.go:95-115` — `drain`: `te, ok := mapEvent(ev)`; on `ok` invokes `OnEvent(te)`. Confirms a now-mapped stall reaches `Handle`. No change here.
- `internal/turnbridge/mapper_test.go:143-167` — the drop-table. Line 154 `{name: "drop stall detected", …}` asserts `wantOK == false`. **Flip it** to `want: turnevent.Stall{}, wantOK: true` and rename; update the table's lead comment (lines 143-144) which lists "stall" among drops.
- `internal/turnbridge/outbound_test.go:14-…` — `TestMapEventOutbound` table (`in / wantTyp / wantOK`). **Add a row** `turnevent.Stall{}` → `wantTyp: protocol.TypeStall, wantOK: true`. The `ThoughtChunk dropped` row (line 145) is the shape model for a payload-bearing assertion.
- `internal/turnbridge/producer_test.go:92-102` — `TestDrain_NilOnEventIsNoop` sends a stall with `onEvent: nil`. **No change** — nil OnEvent drops regardless of mapping; verify it stays green.
- `cmd/pyry/interactive_turn_v2_test.go:18-112` — emitter test harness: `fakeInteractiveBcast` (scripted `ActiveConns` + recorded `Push`), `recordedPush`, `pushTypes`, `pushesFor`, `turnStateValues`. **Reuse these** for the new stall tests; no new doubles needed.

---

## Context

A stalled turn — claude gone quiet mid-turn, or the screen-parser degrading — is detected by tui-driver (`EventKindStallDetected`, shipped v1.3.0) and drained off `Session.Events()`, but **dropped at the daemon**: `mapEvent` maps only the robust JSONL-sourced kinds and explicitly discards the stall marker ("the internal model has no type for them"). The mobile UI already knows how to surface a stall (#373, which this ticket unblocks) and the data vocabulary landed in child A (#638). This slice is the **bridge wiring**: it un-drops the marker and threads it through all three bridge stages to the capability-gated push surface so the phone sees the stall instead of a silently-hanging session.

The whole producer→emitter pipeline is **already built and wired** (#615/#632, `interactive_turn_stream_v2.go`). This ticket adds one `case` at each of the three drop points and the tests that prove the end-to-end path.

---

## Design

### Data flow (unchanged pipeline; the stall now survives two drop points)

```
tui-driver Session.Events()                       [EventKindStallDetected — rising edge, ~1 per onset]
        │
        ▼  turnbridge/producer.go drain()
   mapEvent(ev)            ── STAGE 1 ──  EventKindStallDetected → turnevent.Stall{}, true   (was: nil,false drop)
        │ ok
        ▼  OnEvent(te)  →  emitter.Handle(ctx, ev)
   Handle type-switch      ── STAGE 3 ──  case turnevent.Stall:  emitMapped (NO lifecycle mutation)   (was: default drop)
        │
        ▼  emitMapped → MapEvent(ev, tc)
   MapEvent(ev,tc)         ── STAGE 2 ──  turnevent.Stall → (TypeStall, StallPayload{ConversationID}, true)   (was: "",nil,false drop)
        │ ok
        ▼  emit(ctx, convID, typ, payload)
   for c := range bcast.ActiveConns:  if !c.Interactive { continue }   [capability gate]
        Push(ctx, c.ConnID, stall-envelope)        → interactive-capable phones only
```

Stage numbering follows the ticket (1 = tui→internal, 2 = internal→wire, 3 = consumer fan-out). Note the consumer (`Handle`) is reached *before* `MapEvent` is called — `emitMapped` invokes `MapEvent` internally. The three stages are three files; each is a thin echo of an existing arm.

### Stage 1 — `internal/turnbridge/mapper.go`

Add to `mapEvent`'s top-level switch (alongside `EventKindJsonlEntry` / `EventKindJsonlEndOfTurn`):

```go
case tuidriver.EventKindStallDetected:
    return turnevent.Stall{}, true
```

Update the package/function doc comment (lines 16-18): the stall marker is **no longer** in the dropped set. It now maps to the internal `Stall` signal; every PTY-state kind (idle/thinking/modal/mcp/network) and `Unknown` still drop. Behaviour: the marker is a one-shot rising edge with no payload, so the mapping is a zero-field `Stall{}` — no field extraction.

### Stage 2 — `internal/turnbridge/outbound.go`

Add to `MapEvent`'s type-switch (mirror the `TurnEnd` arm):

```go
case turnevent.Stall:
    return protocol.TypeStall, protocol.StallPayload{
        ConversationID: tc.ConversationID,
    }, true
```

`StallPayload` carries `ConversationID` only. `tc.TurnID` and `tc.Seq` are **ignored** (a stall is not turn-scoped and not a delta) — exactly as `BuildTurnState` ignores them. Pure value-to-value; no I/O, no env-ID, no clock, consistent with the file's contract.

### Stage 3 — `cmd/pyry/interactive_turn_v2.go`

Add to `Handle`'s type-switch a case that emits with **no lifecycle mutation**:

```go
case turnevent.Stall:
    // Onset-only control/state signal — a peer of turn_state. Emit with NO
    // lifecycle mutation: stall is orthogonal to thinking/responding/idle and
    // not turn-scoped (no startTurnIfNeeded / transitionTo / endTurn; inTurn,
    // turnID, seq, currentState untouched). The phone self-clears on the next
    // turn activity. Like turn_state, it flows through emit() and is NOT a
    // droppable delta — the droppable set is assistant_delta only (#610).
    e.emitMapped(ctx, convID, ev)
```

This is the AC2 contract in code: the comment records that the droppable set is `assistant_delta` only, so a stall is never coalesced/discarded. `emitMapped` is reused verbatim — for a `Stall`, `MapEvent` reads only `tc.ConversationID`, so passing the (possibly zero) `turnID`/`seq` is harmless. The cursor gate at the top of `Handle` (`convID == ""` → drop) already applies; no separate guard.

Extend `eventKind` with `case turnevent.Stall: return "stall"` (content-free discriminant for the no-cursor/unmapped debug logs). Update `emitMapped`'s reachability comment (lines 173-176) to add `Stall` to the reachable set.

### Why no turn is required, and no state changes

tui-driver's `EventKindStallDetected` fires on the rising edge of "(not idle) AND (PTY quiet > PTYQuietLimit) AND (no JSONL within that window)" and **does not repeat while the stall persists** (`tui-driver@v1.3.0 events.go:154-156`). It can therefore arrive whether or not the emitter currently has a turn open. Emitting unconditionally (subject only to the existing non-empty-cursor gate) with no lifecycle mutation is the ticket's resolved design fact ("onset-only — no recovery edge / no lifecycle mutation"). The phone self-clears the stall indicator on the next `turn_state`/content activity.

---

## Concurrency model

No new goroutines, no new locks, no new channels. `Handle` runs **only** on the producer's single `Run`/`drain` goroutine (`producer.go` invokes `OnEvent` serially; `interactive_turn_stream_v2.go:40-44` documents the single-goroutine assumption). The emitter's unguarded fields (`inTurn`, `turnID`, `seq`, `currentState`, `nextID`) are touched by exactly one goroutine; the stall case touches **none** of the lifecycle fields and only `nextID` via the shared `emit()` (which already runs on that same goroutine). The stall introduces zero new concurrency surface.

`Push` is synchronous against the relay's single dispatch goroutine (`v2session.go:1560-1573`: send `pushReq`, block on `req.reply` or `ctx.Done()`). The stall rides the identical path every other turn envelope uses — it adds no buffering, no fan-out goroutine, and no backpressure semantics beyond what `turn_state` already has.

---

## Error handling

Reuses the existing `emit()` failure posture verbatim — the stall introduces no new error branch:

- **Empty cursor** (`convID == ""`): drop + debug-log at the top of `Handle` (existing gate). A stall before any user turn has no conversation to address.
- **Payload marshal failure**: `StallPayload` is a single-string struct — cannot fail in practice; the existing defensive `emit()` branch debug-logs without echoing payload bytes.
- **`Push` error** (conn torn down, session not open, ctx cancelled): per-conn debug-log inside `emit()`'s loop; the loop continues to the next conn. On `ctx.Err()` it returns (teardown). A failed Push to one phone never blocks the stall reaching the others. This is **not** a silent drop in the AC2 sense — AC2's "never silently dropped" is about the *droppable-delta classification* (the stall is never coalesced/discarded as a delta), not a delivery guarantee against a dead socket.
- **`MapEvent` ok==false**: unreachable for `Stall` (it now has an explicit arm); `emitMapped`'s defensive `!ok` debug-log remains as belt-and-suspenders.

---

## Testing strategy

Each package tests its own seam (matching the existing layout); together they verify the conceptual `stall_detected → stall envelope` end-to-end.

**Stage 1 — `internal/turnbridge/mapper_test.go`** (flip an existing row, not a new test):
- Change line 154 from the drop assertion to: `in: kindEvent(tuidriver.EventKindStallDetected)`, `want: turnevent.Stall{}`, `wantOK: true`; rename to `"stall detected -> Stall"`. Update the table's lead comment so "stall" is no longer listed among drops.

**Stage 2 — `internal/turnbridge/outbound_test.go`** (add one table row):
- `in: turnevent.Stall{}` with `tc` carrying a `ConversationID` (and a non-empty `TurnID`/non-zero `Seq` to prove they're ignored) → `wantTyp: protocol.TypeStall`, `wantOK: true`; assert the decoded `StallPayload.ConversationID` equals `tc.ConversationID` and that no `turn_id` leaks (the payload struct has none).

**Stage 3 — `cmd/pyry/interactive_turn_v2_test.go`** (the AC1 end-to-end emitter test; reuse `fakeInteractiveBcast`):
- **Stall fans out to interactive conns only.** Snapshot = one interactive + one non-interactive conn; set a non-empty cursor on the fake `cursorReader`; `Handle(ctx, turnevent.Stall{})`. Assert: exactly one `Push`, to the interactive conn; `env.Type == protocol.TypeStall`; decoded `StallPayload.ConversationID == cursor`. The non-interactive conn receives nothing (the capability gate).
- **No lifecycle mutation.** Feed a bare `Stall` to a fresh emitter (no prior turn). Assert: the recorded pushes contain **no** `turn_state` envelope and **no** `assistant_delta`; only the `stall`. Then feed a `TextChunk` and assert the *first* subsequent envelope is a `turn_state: responding` (proving the stall left `inTurn`/`currentState` untouched — the turn opens fresh as if the stall never happened).
- **Stall mid-turn doesn't disturb the open turn.** Drive `TextChunk` (opens turn → `responding` + `assistant_delta`, `seq` advances), then `Stall`, then another `TextChunk`. Assert the second delta's `seq` continues the sequence (stall did not reset `seq`), and exactly one `stall` envelope sits between them. No extra `turn_state` is emitted around the stall.
- **No cursor → drop.** Empty cursor; `Handle(ctx, turnevent.Stall{})`. Assert zero pushes (existing gate covers the stall for free).

`go test -race ./...`, `go vet ./...`, `staticcheck ./...` all green. The producer `TestDrain_NilOnEventIsNoop` must remain green (no change expected).

---

## Open questions

- **Should a stall require an open turn?** Resolved: **no** — onset-only, no lifecycle mutation (ticket design fact). Emit whenever the cursor is non-empty. Recorded here so the developer doesn't add a turn guard.
- **Where does the AC2 "droppable set is `assistant_delta` only" contract live?** Resolved: as the one-line comment on the `Handle` stall case (Stage 3 above). #610 (the droppable-delta optimizer) is a **non-dependency** — the stall is structurally a control event (peer of `turn_state`), outside any future droppable set, so the contract is documentation, not sequencing. Do not block on #610.

---

## Scope check (self-audit before commit)

Production source files (new or modified, excluding tests/`.md`/spec): `internal/turnbridge/mapper.go`, `internal/turnbridge/outbound.go`, `cmd/pyry/interactive_turn_v2.go` = **3** (< 5 → S holds). 0 new files, 0 new exported types, 0 consumer call-site cascade (additive `case` insertions only), 0 new reject branches. ~16 production LOC + ~70 test LOC. Red lines: none tripped.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The only boundary this slice touches is **binary → phone (outbound)** across the internet-exposed relay. The stall carries no phone-supplied data: `StallPayload.ConversationID` is daemon-owned (the supervisor's `CurrentConversation()` cursor), and `turnevent.Stall{}` is a zero-field marker minted from tui-driver's own screen observation — no untrusted bytes enter the path. There is **no new inbound surface**. The boundary is explicit and single: `emit()`'s `c.Interactive` gate (`interactive_turn_v2.go:212-214`), backed independently by `handlePush`'s `V2StateOpen` check (`v2session.go:1595`) — so an unauthenticated/unvalidated or non-interactive conn cannot receive the stall even if the capability snapshot raced. Defense in depth, unchanged from the existing turn-event fan-out.

- **[Network & I/O — DoS, the labelled concern]** No MUST FIX. The ticket flags a "never-dropped-under-backpressure dispatch policy" as DoS-shaped. Walked it: (1) **Source rate is structurally bounded** — `EventKindStallDetected` fires on a *rising edge* and "does not repeat while the stall persists" (`tui-driver@v1.3.0 events.go:154-156`), gated behind a full `PTYQuietLimit` of genuine PTY silence. It is ~1 event per real stall onset; there is no amplification primitive and no phone-triggerable way to make it fire. (2) **No unbounded buffer is introduced** — `emit()` → `Push()` is synchronous against the relay's single dispatch goroutine (`v2session.go:1560-1573`); under backpressure it blocks or returns on `ctx.Done()`, identical to `turn_state`/`assistant_delta`. The stall adds **zero** new buffering. (3) **"Never silently dropped" is a classification property, not a resource commitment** — it means the stall is excluded from the future #610 droppable-*delta* set (which is `assistant_delta` only), so a coalescing optimizer can never discard it; it does **not** mean unbounded retention. A Push to a dead/slow socket still errors and is debug-logged per-conn, and the loop proceeds. The labelled DoS concern is therefore *resolved by the rising-edge + synchronous-path design*, not introduced.

- **[Error messages, logs, telemetry]** No MUST FIX. The stall path logs only content-free discriminants — `eventKind` returns the literal `"stall"` (the variant name, never content; the marker has no content to leak), and `emit()`'s existing logs carry only `conn_id`, `env_id`, `conversation_id`, `turn_id`, and the transport-sentinel `err`. The new `case` adds no log line that echoes payload, key, or ciphertext bytes — it inherits `interactive_turn_v2.go`'s documented "application output is NEVER logged" discipline (file header, lines 41-46). `handlePush`'s seal/marshal errors already MUST-NOT echo envelope/plaintext/key bytes (`v2session.go:1585-1587`).

- **[Concurrency]** No MUST FIX. No new goroutine, lock, or channel. The stall case runs on the single `Handle`/`drain` goroutine and touches **no** lifecycle field (no `inTurn`/`seq`/`turnID`/`currentState` mutation) — it cannot introduce a data race or a TOCTOU on emitter state. `nextID` is advanced only inside the shared `emit()` on that same goroutine. Goroutine lifecycle is unchanged (the producer goroutine still exits on `ctx.Done()` via `prod.Run`).

- **[Tokens/secrets, File ops, Subprocess, Cryptographic primitives]** Not applicable — this slice adds three `case` arms over in-memory value types and reuses the existing sealed-push path. It generates no token, touches no filesystem path, spawns no subprocess, and performs no crypto (the Noise seal on the outbound frame is the existing, unchanged `handlePush` path). Stated explicitly rather than skipped: there is no code in this diff that could exercise these categories.

- **[Threat model alignment]** No MUST FIX. The relevant threat (`docs/protocol-mobile.md` § Security model — only authenticated, capability-granted phones receive session content) is satisfied by the unchanged `V2StateOpen` + `Interactive` double gate. The stall is strictly less sensitive than the `assistant_delta`/`tool_result` envelopes already flowing this path (it carries no application text at all), so it widens no existing exposure. #610 (droppable-delta optimizer) is named OUT OF SCOPE and is a non-dependency.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-08
