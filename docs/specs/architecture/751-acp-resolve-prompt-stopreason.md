# Spec #751 — Resolve `session/prompt` with `stopReason` on `TurnEnd`

**Ticket:** [#751](https://github.com/pyrycode/pyrycode/issues/751) · **Size:** S · **Epic:** #600 (`pyry acp`) — divergence 1 · **Security-sensitive:** no

## Summary

Flip `acpTurnStream`'s `onTurnEnd` seam from `nil` to the held-call resolver. When a
turn ends, the outbound stream's `TurnEnd` event resolves the in-flight
`session/prompt` call (`promptHolds.end`) with the ACP `stopReason` mapped from the
neutral reason. This is the one genuinely event-stream-to-RPC-return part of the ACP
adapter; everything else is naming.

The two halves already exist in merged code and are independently tested:

- **Producer side (#796):** `acpTurnStreams.start` builds one `turnbridge.Producer` →
  `acpTurnStream` sink per session. The sink already calls `onTurnEnd(string(e.Reason))`
  on `TurnEnd` — but the manager passes `nil`, so it is a logged debug no-op today.
- **Held-call side (#749):** `promptHandler` registers a hold (`promptHolds.begin`) and
  returns `ErrDeferred`; `promptHolds.end(sessionID, stopReason)` resolves it idempotently.

This ticket is the **join**: thread `promptHolds.end` into the streams manager so the
sink's `onTurnEnd` resolves the held call. Net production change is ~12 lines across two
`cmd/pyry` files; the substance is the test matrix (AC1–AC5).

## Files to read first

| File / lines | What to extract |
|---|---|
| `cmd/pyry/acp_turn_streams.go:104-144` | `start`: the exact `newACPTurnStream(m.transport, sessionID, nil, m.logger)` flip site (line 116) and the per-session closure to build. Producer/Run/wg wiring already correct. |
| `cmd/pyry/acp_turn_streams.go:34-55` | `acpTurnStreams` struct + `newACPTurnStreams` constructor — add the `onTurnEnd` field and the new param here. |
| `cmd/pyry/acp.go:115-165` | `serveACPWithPool`: `holds` is built at :118, `streams` at :124. Pass `holds.end` into `newACPTurnStreams`. Update the stale comment at :115-117 ("until then a delivered prompt stays held…"). Same `holds` instance is already shared with the prompt handler (:131). |
| `cmd/pyry/acp_prompt.go:98-114` | `promptHolds.end(sessionID, stopReason string)` — the resolver being wired. Its `func(string, string)` shape is directly assignable to the seam as a method value. Delete-under-mu = at-most-one of `end`/`fail` finds the entry (AC3). |
| `cmd/pyry/acp_prompt.go:57-72, 116-130` | `promptHolds` struct + `fail` — the exactly-once invariant `end` shares (delete-under-mu + #765 `Responder.done` CAS). AC3 rests on these; no change needed. |
| `cmd/pyry/acp_turn_stream.go:65-79` | Sink `Handle` `TurnEnd` branch: `a.onTurnEnd(string(e.Reason))`. This `string()` cast **is** the reason→stopReason identity map. Nil-tolerant (debug-log fallback). No change to the sink. |
| `internal/turnevent/taxonomy.go:34-43, 70-73` | The five `TurnEndReason` string values (`end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, `cancelled`). These literal strings are what AC1 pins. |
| `cmd/pyry/acp_prompt_test.go:115-231` | `promptHarness` + `endTurn`/`readReply`/`promptReply` — reuse verbatim for AC2/AC3 (send a prompt, resolve via `holds.end`, assert `stopReason`). `TestACPPrompt_DeliversAndHolds` (:298) is the template. |
| `cmd/pyry/acp_turn_streams_test.go:33-88` | `TestACPTurnStreams_ScriptedTurnEmitsOrderedFrames` — the AC5 template. `scriptedSubscriber` + `jsonlStreamEvent`/`streamEntry`/`endOfTurnEvent` drive a real producer→sink. Extend with `onTurnEnd → holds.end` on a shared transport. |
| `cmd/pyry/acp_turn_stream_test.go:27-34, 162-189` | `newStreamHarness(t, onTurnEnd func(string))` + `TestACPTurnStream_TurnEndSignalsAndEmitsNothing` — reuse for AC1's five-reason table. |
| `internal/acp/responder.go:41-86` | `Responder` (unexported fields), `Reply`, `ReplyError`, `ResponderFrom(ctx)`. A `*Responder` is only obtainable through dispatch — AC5 must run `Serve` once over a `session/prompt` frame to register a real hold before driving the producer (see Testing). |

## Context

**What problem this solves.** ACP models a turn as a `session/prompt` request that streams
`session/update` notifications and then *returns* a `stopReason`. Pyrycode's neutral model
is a pure event stream ending in `turnevent.TurnEnd{Reason}`. Without this ticket, a
delivered prompt stays held until host disconnect: the producer emits notifications but the
sink drops `TurnEnd` on the floor (nil `onTurnEnd`), so the host never learns the turn
completed. This wires the terminal event back into the RPC return.

**Why now.** #796 wired the producer→sink chain and deliberately left `onTurnEnd` nil,
reserving the flip for this ticket (see the comment at `acp_turn_streams.go:114-115`). #749
built and tested `promptHolds`. Both prerequisites are merged; this is the last join for
divergence 1.

**Hard cost invariant (unchanged).** No new claude spawn, no `claude -p`, no Agent SDK. The
turn already runs on the interactive session; this only converts its end event to an RPC
return.

## Design

Two production files, package `main`, purely additive. The reason→`stopReason` mapping is
**identity and already lives in the sink** (`string(e.Reason)` at `acp_turn_stream.go:73`) —
this ticket introduces **no new mapping function**. The neutral `TurnEndReason` values were
shaped to the ACP `stopReason` set on purpose; a wrapper that returns `string(r)` would be
ceremony. Divergence, if it ever appears, is caught by AC1's literal-string assertions, not
by a speculative indirection (Evidence-Based Fix Selection).

### 1. `acpTurnStreams` gains an `onTurnEnd` seam

Mirror the sink's own seam design: a `func(sessionID, reason string)` on the manager, set at
construction, nil-tolerant. Accept-a-function-at-the-consumer, consistent with the codebase's
`promptDeliverer` / `interrupter` / `resolve` idiom — the manager stays decoupled from the
concrete `promptHolds` type and is testable with a recording double.

- Add field `onTurnEnd func(sessionID, reason string)` to the `acpTurnStreams` struct.
- Add the param to `newACPTurnStreams(ctx, pool, dir, onTurnEnd, logger)`.

### 2. `start` builds the per-session closure

At the flip site (`acp_turn_streams.go:116`), bind the manager seam to the fixed `sessionID`
and pass it to the sink, preserving nil-tolerance so #796's pure-emit tests and the
`dir == ""` path still pass `nil` through:

```go
var onEnd func(string)
if m.onTurnEnd != nil {
    onEnd = func(reason string) { m.onTurnEnd(sessionID, reason) }
}
sink := newACPTurnStream(m.transport, sessionID, onEnd, m.logger)
```

Update the adjacent comment (`:114-115`) to state that `onTurnEnd` now resolves the held call.

### 3. Wire `holds.end` at the composition root

In `serveACPWithPool` (`acp.go`), `holds` (built at :118) is already the same instance the
prompt handler registers against (:131). `promptHolds.end` has signature
`end(sessionID, stopReason string)` — a `func(string, string)` — so the bound **method value**
`holds.end` is directly assignable to the new seam:

```go
streams := newACPTurnStreams(runCtx, pool, claudeSessionsDir, holds.end, logger)
```

`holds` is declared before `streams`, so the closure over it is well-formed. Update the stale
comment at `:115-117` (drop "until then a delivered prompt stays held until host disconnect").

### Data flow (after this ticket)

```
claude turn ends
   │  (tui-driver end-of-turn → turnbridge mapEvent)
   ▼
turnevent.TurnEnd{Reason}
   │  (producer Run goroutine, after the last session/update)
   ▼
acpTurnStream.Handle → onTurnEnd(string(Reason))          // sink, identity map
   │
   ▼
m.onTurnEnd(sessionID, reason)  ==  promptHolds.end(sessionID, reason)
   │  (delete-under-mu; nil-tolerant if no pending call)
   ▼
Responder.Reply(promptResult{StopReason: reason})          // held session/prompt returns
```

## Concurrency model

No new goroutines, no new locks. Two correctness pins, both already satisfied by the merged
code — the tests confirm the wired path does not regress them:

- **Resolution rides the producer's single Run goroutine, after the last notification.**
  `onTurnEnd` is invoked *synchronously inside* `acpTurnStream.Handle`'s `TurnEnd` branch, on
  the same goroutine that emitted every prior `session/update`. So the `stopReason` return is
  strictly ordered after all notifications on the outbound wire (ACP's streaming-precedes-return
  contract). It is **not** a racing goroutine that could resolve before a late chunk — that was
  the explicit design constraint in the ticket's Technical Notes.
- **No `writeMu` re-entrancy.** The `TurnEnd` branch returns immediately after `onTurnEnd`; it
  never calls `transport.Notify`. So `holds.end → Responder.Reply` (which takes `writeMu`) does
  not nest inside a `Notify` (which also takes `writeMu`). Contrast the `OnChange`-fan-out
  deadlock class — not applicable here because the terminal branch emits no frame of its own.
- **Exactly-once across every resolve race (AC3).** `promptHolds.end` deletes the entry under
  `mu` before replying, and #765's `Responder.done` CAS guards the frame write. A second
  `TurnEnd` (or an `end`/`fail` race) finds no entry → no-op; the CAS bounds it even if two
  callers captured the same `*Responder`. No change needed; AC3 is a regression guard.

## Error handling

- **Identity mapping, no new error path.** `string(TurnEndReason)` is the ACP `stopReason`; a
  `TurnEnd` always maps to a success `Reply`, never a `ReplyError`. The error path
  (`promptHolds.fail` → `CodeInternalError`) belongs to delivery failure (#749) and is untouched.
- **Cancelled is a result, not an error (AC2).** A cancelled turn (`session/cancel`, #753)
  surfaces as `TurnEnd{Reason: cancelled}`, which resolves the held call with
  `stopReason: "cancelled"` via the same `Reply` path — never a JSON-RPC error frame.
  (Whether a cancel actually *produces* a `cancelled` reason is turnbridge/#753's concern; #751
  only asserts the resolution shape given that reason.)
- **No pending call (AC4).** `promptHolds.end` on a session with no registered hold is a no-op
  (`resp == nil` → return). A `TurnEnd` for a session that was never prompted is handled without
  error and emits nothing.

## Testing strategy

Reuse the two existing harnesses; add no new production surface for tests. All tests are
`cmd/pyry` package `main`, table-driven where natural, `-race`-clean. Scenarios (developer
writes the assertions in the project idiom):

**AC1 — full five-reason mapping (sink level, divergence catcher).** In
`acp_turn_stream_test.go`, table over all five `TurnEndReason` values. For each, drive
`sink.Handle(turnevent.TurnEnd{Reason: r})` through `newStreamHarness(t, recordingOnTurnEnd)`
and assert the recorded string equals the **literal ACP stopReason** (`"end_turn"`,
`"max_tokens"`, `"max_turn_requests"`, `"refusal"`, `"cancelled"`) — expected values written as
literals, *not* `string(turnevent.X)`, so a future divergence between the neutral set and the
ACP set fails the test. Assert exactly one `onTurnEnd` call and zero emitted frames per reason.

**AC1/AC2 — join resolves the held call with the mapped stopReason (wire level).** In
`acp_prompt_test.go`, reuse `promptHarness`: send a `session/prompt`, `waitEntered`, then
`h.endTurn(sess, string(reason))` and assert `readReply` yields `reply.Error == nil` and
`reply.Result.StopReason == <reason>`. Cover `cancelled` explicitly for AC2 (result, not error).
`TestACPPrompt_DeliversAndHolds` already proves the general shape; this pins the mapped values.

**AC3 — exactly one resolution.** Send a prompt, `endTurn` once, read the single reply, then
call `holds.end(sess, "end_turn")` again (duplicate `TurnEnd`) and assert **no second frame**
is written and no panic. The second `end` finds no entry (deleted) → no-op.

**AC4 — no pending call is a no-op.** Direct unit on `promptHolds`: `newPromptHolds(logger)`,
then `holds.end("absent-session", "end_turn")` returns without panic and writes nothing. (No
harness needed — the store has no Responder to resolve.)

**AC5 — end-to-end scripted turn: notifications then stopReason on one wire.** Extend the
`TestACPTurnStreams_ScriptedTurnEmitsOrderedFrames` pattern so the sink's `onTurnEnd` resolves a
**real** held call on the **same transport** the notifications write to:

  1. Build one `*acp.Transport` over a `bytes.Buffer` writer. Register `session/prompt` against a
     real `promptHolds` + non-gated `recordingDeliverer`. Run `Serve` once over a single
     `session/prompt` frame (`promptFrame(...)`) fed via `strings.NewReader` — Serve dispatches
     it (hold registered inline, `ErrDeferred`, no frame written), hits EOF, returns nil. The
     hold is now registered with a `Responder` bound to this transport. (A `*Responder` is only
     obtainable through dispatch — hence the single Serve pass.)
  2. Build the sink on that **same transport** with `onTurnEnd = func(r){ holds.end(sessionID, r) }`,
     and a `turnbridge.Producer` over a `scriptedSubscriber`.
  3. Drive `jsonlStreamEvent(streamEntry(...))` events (thought/text/tool chunks), then
     `endOfTurnEvent()`; cancel + `waitClosed` the producer.
  4. Decode all frames from the buffer. Assert every `session/update` notification precedes the
     one `session/prompt` result frame, and that result carries the expected `stopReason`.

  A buffer-backed writer (not a blocking pipe) lets all frames accumulate for a single post-hoc
  read; the outbound *ordering* (notifications-before-result) is what AC5 asserts, and it holds
  because the producer emits the result strictly after the notifications on its one Run goroutine.
  (A pipe-based concurrent `serveACP` — as `promptHarness` uses — is an equally valid but heavier
  shape; the buffer variant is preferred for simplicity.)

**AC6 — `make check` green** (`gofmt`, `go vet`, `staticcheck`, `go test -race`).

Update the two existing `newACPTurnStreams(...)` test call sites
(`TestACPTurnStreams_LifecycleTeardownJoinsProducer`, `TestACPTurnStreams_EmptyDirDisablesStreaming`)
to pass `nil` for the new `onTurnEnd` param — those tests don't exercise resolution.

## Security

Preservation, not a new surface (no `security-sensitive` label). The reason string is
daemon-originated (tui-driver → turnbridge → `turnevent.TurnEnd`), never host-controlled; it is
one of five closed taxonomy values. No inbound host content is parsed here, no outbound policy
decision is made, no crypto. The sink's content-free logging discipline is unchanged: logs carry
only the event kind, session id, and error sentinels — never model output. `promptHolds.end`
logs nothing at all on the resolve path.

## Open questions

- **None blocking.** The reason set is identity today; if a future ACP-spec revision diverges
  either set, AC1's literal-string table fails first and forces an explicit divergence handler in
  the sink's `TurnEnd` branch — the intended containment point.
