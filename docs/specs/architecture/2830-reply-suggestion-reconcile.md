# #2830 — reconcile current reply suggestions on interactive connect

## Files read

- `internal/relay/v2session_turnphasereconcile.go` → `reconcileTurnPhases`: the twin this copies (Push not seal, content-free logs, EventID nil).
- `internal/relay/v2session_turnphasereconcile_test.go`: test shape to copy (`openModalConn`, `noiseMsgsForConn`, `decryptAppFrame`, log-capture with `lockedBuffer`).
- `internal/relay/v2session_seams.go` → `V2SessionConfig.RunningTurnPhases`: where the new seam goes and the doc posture it follows.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` success tail: call site, after `reconcileTurnPhases`.
- `internal/relay/v2session.go` → `forwardEnvelope`: the single seal path for queue drain and replay, where the revision guard goes; `V2Session.replayThrough`: the Run-owned per-conn state regime the new field follows; `Push`: enqueue under `pushMu`, safe to call from Run.
- `internal/relay/v2session_agentgate.go` → `withheldFromConn`, `pushedConversationID`: already withholds any pushed frame by its top-level `conversation_id`, so `reply_suggestion` needs no change there, only a test.
- `internal/relay/v2session_agentgate_test.go` → `gateCodexSeam`, `gateCapableCaps`, `gateOldCaps`: fixtures for the Codex withhold test.
- `internal/protocol/interactive.go` → `ReplySuggestionPayload`: `SuggestedReply *string` without omitempty, so nil marshals to `null`.

No other feature branch touches these files.

## Context

#2828 declared `reply_suggestion`; nothing emits it. Part of #2817. This slice gives the relay the connect-time reconcile (eighth Mode B instance) and the per-conn revision ordering, both testable by calling `Push` directly. The producer and its live fan-out are #2831; the seam stays unwired here. The documentation stage may want a decision record only if it judges the per-conn ordering guard a new pattern; the ticket asks for a feature child doc instead.

## Design

**Seam.** `V2SessionConfig.ReplySuggestions func() []protocol.ReplySuggestionPayload`. Returns one payload per conversation that has suggestion state, a cleared conversation included with `SuggestedReply == nil`. Contract: a pure read in bounded time, called on the Run goroutine, so it must not call back into anything that round-trips through Run (`ActiveConns`). Nil ⇒ no reconcile.

**Reconcile.** `reconcileReplySuggestions(ctx, s)` in a new `v2session_replysuggestionreconcile.go`. Structurally `reconcileTurnPhases` with the payload type substituted: gate on `s.interactive` and a non-nil seam, marshal each payload, `Push` an envelope with `Type: TypeReplySuggestion`, `ID: 1`, `EventID: nil`. Called last in `handleNoiseInit`'s tail; position is not load-bearing (never in the ring, ordered by the guard).

**Revision guard.** New Run-owned field `V2Session.replySuggestionRevisions map[string]uint64` (conversation_id → highest revision delivered on this conn), allocated lazily. In `forwardEnvelope`, after `withheldFromConn` and the `replayThrough` check and before the seal, a helper `replySuggestionStale(s, env) (convID string, rev uint64, stale bool)`, living in the reconcile file:
- not `TypeReplySuggestion`, or payload not decoding ⇒ not stale, nothing recorded (delivered unchanged, the `pushedConversationID` posture);
- `rev <= map[convID]` ⇒ stale; `forwardEnvelope` returns nil (a deliberate drop, not an error);
- otherwise `forwardEnvelope` records `map[convID] = rev` after `m.send`, so a frame that failed to seal never advances the watermark.

A withheld frame is never recorded. A new conn gets a fresh `V2Session`, so it starts empty and a reconnect is never blocked by an earlier conn's revisions.

## Concurrency model

No goroutine added. The seam, the reconcile, `forwardEnvelope` and the map all run on the Run goroutine, the `replayThrough` single-owner regime: no lock. `Push` takes `pushMu` alone, as the twins do. The race the guard closes: the reconcile reads revision N; the producer publishes and pushes N+1 (a clear) to the same queue before the snapshot frame drains; the queue drains N+1 first, records it, then drops N. The opposite order (N drains, then N+1) delivers both, last word N+1.

## Error handling

Marshal failure: unreachable (strings, uint64, *string); defensive Warn with `event`, `conn_id`, `conversation_id` only, skip that payload. Push failure: Debug with `event`, `conn_id`, `err` (a sentinel), stop on ctx error. A stale drop logs nothing (it fires per frame per conn and is expected). No log or error carries `suggested_reply` or payload bytes.

## Testing strategy

New `v2session_replysuggestionreconcile_test.go`, existing harness only:
- Delivery: one, and two conversations including a cleared one; each decoded payload equals the seam's; raw decrypted payload contains a literal `"suggested_reply":null` for the clear; no `event_id`.
- No frame: nil seam, empty result, non-interactive conn (with an interactive control conn proving the seam produces); handshake still opens; no `v2.replysuggestion.` log line.
- Reconnect reads current state: open A at revision 1 text; seam moves to revision 2 clear; open B, which gets only revision 2.
- Interleave, two conns × two conversations: seam call 1 (conn A) returns X@5 text, Y@3 text and, from inside the seam, `Push`es X@6 clear to A; A ends with X = clear@6 and Y@3, X@5 never sent. Seam call 2 (conn B) returns the same snapshot with no live push; B gets X@5 and Y@3 (A's watermark does not leak to B). Then live pushes: Y@3 to both is dropped on both (equal), Y@4 to A only reaches A.
- Codex withhold: with `gateCodexSeam`, a conn on `gateOldCaps` receives the Claude conversation's suggestion only, reconciled and live; a conn on `gateCapableCaps` receives both. Log capture holds no suggestion text.

## Open questions

- Should a non-decoding `reply_suggestion` be dropped rather than delivered? Settled: delivered unchanged; the producer builds it from the DTO, and dropping silently would hide a producer bug from the client too.

## Documentation handoff

Pending for the documentation stage:
- `docs/protocol-mobile.md` § `reply_suggestion`: an interactive conn receives current state at connect, including null clears; a Codex conversation's frame is withheld without `multi_agent`; delivery is monotonic per conversation per connection; no `event_id`, excluded from history and replay. Keep the "declared, not yet emitted" note until #2831.
- `docs/knowledge/features/`: a child doc beside `v2-session-manager-state-machine-connect-time-turn-phase-reconcile-running.md` covering the seam contract (pure bounded read, clears as real null state) and the `forwardEnvelope` revision guard; linked from `## Sections` in `v2-session-manager.md` with a short link line (that file is near the 50000-byte cap).

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. No client-authored byte reaches the frame or the guard: the client's only input is its capability set, judged once in `handleNoiseInit` into `s.interactive` and `s.multiAgent`. The payload comes from the daemon's seam; the revision compared in `replySuggestionStale` is the producer's, read from the daemon's own envelope. `suggested_reply` is claude-derived text, which is why it is never logged.
- [Tokens] No findings. No token, key or credential is touched.
- [File operations] No findings. Memory only; nothing appended to the ring or durable history.
- [Subprocesses] No findings. None.
- [Cryptography] No findings. The reconcile enqueues via `Push` and never seals; the guard drops before `s.send.Encrypt`, so a stale or withheld frame spends no send-nonce, and recording after `m.send` keeps a failed seal from advancing the watermark.
- [Network and I/O] SHOULD FIX: the per-conn map grows by one entry per conversation the conn received a suggestion for. Bounded by the daemon's conversation count and freed with the session at `closeWith`; the build must keep it on `V2Session` (not a manager-level map) so there is nothing to leak.
- [Errors and logs] SHOULD FIX: marshal arm logs `event`, `conn_id`, `conversation_id` only and never `err`; push arm logs Push's sentinel only; stale drop and success log nothing. A test asserts captured logs hold no suggestion text.
- [Concurrency] No findings. Every new read and write is on the Run goroutine. The seam contract forbids calling back into Run, which would deadlock the handshake; #2831's producer must take only a leaf lock. No goroutine started.
- [Threat model] No findings. A non-interactive device gets nothing; an old client without `multi_agent` gets no Codex conversation's suggestion via `withheldFromConn`, reconciled or live; an interactive device sees only state it would see live.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
