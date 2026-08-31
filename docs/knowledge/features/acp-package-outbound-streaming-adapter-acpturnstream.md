# Outbound streaming adapter (`acpTurnStream`) (#750)

The **streaming consumer** (epic #600 T6) that turns a session's neutral
[`turnevent.Event`](turnevent-package.md) stream into `session/update`
notifications. It lives in `cmd/pyry/acp_turn_stream.go` (**package `main`**,
keeping `internal/acp` method-agnostic) and is the ACP analogue of the mobile
head's `interactiveTurnEmitterV2` — but **far thinner**: ACP's host owns the
spinner and turn pacing, so none of the mobile emitter's lifecycle machine,
delta coalescing, capability fan-out, replay ring, or conversation cursor apply.
The mapping half is [#769](acpbridge-package.md)'s pure `acpbridge.MapUpdate`;
this ticket owns the subscription sink, the transport emit, `TurnEnd`→T7
signalling, and `Stall`→stderr.

`acpTurnStream` is a **stateless sink** — no per-event state, no goroutine, no
clock read. Its `Handle(ev turnevent.Event)` signature **is**
`turnbridge.Config.OnEvent` exactly, so the producer wiring
([#796](https://github.com/pyrycode/pyrycode/issues/796)) sets
`OnEvent: sink.Handle` with no closure. `turnbridge` invokes `Handle` serially
on its single `Run` goroutine, so no synchronisation is needed.

### `Handle` dispatch (ADR 027 divergences 1 & 3)

`TurnEnd` and `Stall` are type-switched **explicitly, before** `MapUpdate`,
because `MapUpdate` collapses both to `ok == false` and the adapter must
distinguish them (a signal vs a stderr drop):

| Event | Action |
|---|---|
| `TurnEnd{Reason}` | **No** `session/update` (divergence 1). Call `onTurnEnd(string(Reason))` — the ACP `stopReason` **is** `string(Reason)` by identity (`TurnEndReason` values already are the ACP strings). |
| `Stall{}` | **No** `session/update` (divergence 3). `logger.Warn` on stderr, content-free (`event`, `session_id` only). |
| `TextChunk` / `ThoughtChunk` / `ToolStart` / `ToolUpdate` | `acpbridge.MapUpdate(ev)` → `Transport.Notify(MethodSessionUpdate, sessionUpdateParams{sessionID, update})`. |
| nil / future variant | `MapUpdate` returns `ok == false` → defensive `Debug`-logged drop (unreachable for the four above). |

`sessionUpdateParams{SessionID, Update}` is the `{sessionId, update}` params
wrapper — **this consumer's**, not `acpbridge`'s (the mapper is pure
value-to-value; session addressing is the consumer's). The `sessionUpdate`
discriminant rides *inside* `Update`.

### `TurnEnd` → held-call resolution (#751)

`onTurnEnd func(reason string)` is invoked **synchronously on the producer's
single `Run` goroutine** when `TurnEnd` arrives — after the last `session/update`,
so the `stopReason` return is strictly ordered behind every notification (ACP's
streaming-precedes-return contract). #751 supplies the callback:
`func(reason){ holds.end(sessionID, reason) }`, resolving the held
`session/prompt` call with `reason` as the `stopReason` (identity today —
`string(Reason)` **is** the ACP `stopReason`). It **does not block** the producer
goroutine — `promptHolds.end` captures the responder under `mu`, deletes, releases
`mu`, then replies (`mu` stays a leaf; no `mu → writeMu` nesting), and the terminal
branch emits no frame of its own so there is no `writeMu` re-entrancy. A callback
(not a channel) keeps the sink agnostic about the hold-and-resolve mechanism.
`onTurnEnd` is **nil-tolerant** (Debug-log + no-op) so the adapter can still be
constructed for pure-emit tests and the `dir == ""` path. Cancelled is a **result,
not an error**: a `session/cancel`-driven `TurnEnd{Reason: cancelled}` resolves with
`stopReason: "cancelled"` via the same `Reply`, never a JSON-RPC error frame. Full
per-ticket detail in [`codebase/751.md`](../codebase/751.md).

### Chunk grouping is arrival order, not coalescing

The `msgID` from `MapUpdate` is intentionally **discarded**. ACP's
`agent_message_chunk` carries content only — there is no per-message wire
delimiter — so chunks sharing a `MessageID` stream as **separate**
`agent_message_chunk` notifications *in arrival order* and the host concatenates
them. The adapter is stateless w.r.t. `MessageID` and does **not** coalesce
(contrast the mobile emitter's `MessageID`-keyed delta coalescing, #609, which
ACP neither needs nor supports).

### Producer wiring (#796)

`acpTurnStream` shipped in #750 with **zero non-test callers** — a scripted-tested
pure sink. #796 is the composition-root wiring that stands up the live producer to
drive it, giving the sink its first real caller. `acpTurnStreams`
(`cmd/pyry/acp_turn_streams.go`, unexported per-process manager) owns one
[`turnbridge.Producer`](turnbridge-package.md) goroutine per addressable ACP
session, each tailing that session's `<id>.jsonl` and feeding the sink so
`session/update` notifications flow as a turn progresses. Far thinner than the
mobile leg's `startInteractiveTurnStreamV2`: **one host, one fixed session per
stream** via a `resolveBoundSessionJSONL(dir, id)` resolver with `Switch: nil` (no
active-conversation follow, never re-keyed) over `sess.Supervisor()` (the
`turnbridge.SessionHost`) — the degenerate one-host case of the #679 follow-active
resolver.

`serveACPWithPool` gains a `claudeSessionsDir` param; `runACP` computes it as
`sessions.DefaultClaudeSessionsDir(trustedWorkdir)` and passes it down. The dir is
**deliberately NOT set on `sessions.Config.ClaudeSessionsDir`** — that would enable
the pool's `/clear` rotation watcher, which `RotateID`s a session and breaks the
host-held id addressing `session/prompt` / `session/cancel` rely on; the pool stays
byte-unchanged. `dir == ""` (no `$HOME`) disables streaming. The manager is
`attach`ed to the transport (first line of `register`), `start(id)`ed after
`Create` / `Activate` (idempotent — a repeated `session/load` spawns no duplicate),
and joined at shutdown (`cancel()` → `wait()`). The producer runs for the session's
**lifecycle** (re-subscribing per turn via `Producer.Run`'s outer loop), not a
single turn. #796 shipped `acpTurnStreams` with `onTurnEnd` **nil** (a debug no-op);
[#751](https://github.com/pyrycode/pyrycode/issues/751) flipped it to the held-call
resolver — the manager gained an `onTurnEnd func(sessionID, reason string)` seam
wired to `promptHolds.end` at the composition root, bound to the fixed `sessionID`
at `start` and passed to the sink (see [held-call resolution](#turnend--held-call-resolution-751)).

**Not security-sensitive** — outbound-only over local stdio to the host process,
no untrusted inbound parsing, no auth/crypto (the inbound handlers #749/#752 carry
that label). The content-free logging posture is preserved (every new log site
carries only the event kind + session id + error sentinel). Full per-ticket detail
in [`codebase/750.md`](../codebase/750.md) (the sink) and
[`codebase/796.md`](../codebase/796.md) (the producer wiring).
