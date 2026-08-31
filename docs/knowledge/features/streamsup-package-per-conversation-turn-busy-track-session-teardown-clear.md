# Session-teardown clear (#1202)

A session torn down mid-turn — a `/clear` rotation (`ReasonClear`) or an idle/cap eviction
(`ReasonEviction`) — never produces the abandoned turn's `result` line, so `observe`'s `TurnEnd`-only clear
alone would wedge the conversation busy forever. `turnBusyTracker.clearForSession(sessionID string)`
(`cmd/pyry/stream_turn_busy.go`) closes that gap: nil-receiver-safe (mirrors `observe`), resolves
`sessionID → conversationID` via the tracker's own injected closure **outside** `t.mu` (same lock-order
rule `observe` follows), and on an unresolved session logs `stream_turn.clear_unresolved` (`session_id`
only — the resolved `conversation_id` is deliberately withheld) and returns without mutating. The
membership mutation itself — resolve-then-delete-then-broadcast — was extracted out of `observe` into a
shared `setBusy` so both feeds use one copy of the close-and-replace protocol `WaitIdle`'s
check-and-subscribe atomicity depends on, rather than a second hand-written copy of it. `setBusy` gained a
third parameter, a `toolCallDelta`, with #1917's in-flight tool-call retention — `clearForSession` passes
the zero value, since teardown clears the whole conversation rather than one call. See
[Resolve an in-flight tool call to its conversation](streamsup-package-per-conversation-turn-busy-track-resolve-an-in-flight-tool-call.md).

**Session-keyed, not conversation-keyed, on purpose.** A `clearConversation(convID)` shape would be a
shorter call chain but would accept a conversation id from anywhere, retiring the type's own `SECURITY`
note that the key "is never taken from the wire." Session ids are minted solely by `internal/sessions`'
own lifecycle events, so keeping the clear session-keyed keeps that invariant intact for this feed too.

**Wiring: composed onto the pool's single-valued `TransitionObserver` slot**, not a second install.
`SetTransitionObserver` (`internal/sessions/transition.go`) is a plain assignment — installing twice would
clobber the incumbent wire emitter — so `startSessionTransitionStreamV2` (`cmd/pyry/session_transition_v2.go`)
now composes: `emitter.Enqueue(t)` first (unconditional, non-blocking, so every transition that reached the
emitter before this slice still reaches it, timed identically), then `transitionClearsTurn(t)` — a 3-line
helper that delegates to `toWirePayload`'s existing closed reason switch rather than duplicating it, so an
unknown/future `TransitionReason` clears nothing — and `busy.clearForSession(sid)` on a hit.
`transitionClearsTurn` returns `NewSessionID` (the conversation's live `CurrentSessionID` for both reasons,
per `toWirePayload`'s existing semantics), not `PreviousID` — that's `conversationForSession`'s *primary*
match, so the clear doesn't depend on `RebindSession`'s `SessionHistory` append or the rebind-before-fan-out
ordering the way a `PreviousID`-keyed clear would.

`startSessionTransitionStreamV2` gained a `busy *turnBusyTracker` parameter, always the concrete pointer
(never an interface, same typed-nil hazard `observe`'s doc names). `cmd/pyry/relay.go` hoists the tracker's
declaration above the `w.streamSink != nil` branch so PTY mode — where no tracker is ever constructed —
still installs the composed observer, just with a nil `busy`; `clearForSession`'s nil-receiver guard is
what makes that safe rather than a nil-pointer panic on the pool's lifecycle goroutine.

**The clear runs synchronously**, on the goroutine that fired the transition (the pool's lifecycle
goroutine for eviction, the rotation-watcher goroutine for `/clear`) — satisfying the observer contract's
"MUST NOT block" without a buffered hand-off, because the work is one registry-mutex-guarded slice copy, a
map delete, and a `close()`, and the *same* goroutine already pays a full atomic write (including fsync)
one line earlier on the `/clear` path (`rebindConversation` → `Save`). A resolve-then-mutate this cheap
doesn't need the async escape hatch `WaitIdle` exists to provide for a slower consumer.

**The rotation edge #1201 flagged as this ticket's is unreachable, and the comment is corrected rather
than defended with a guard.** The suspected hazard: because `conversationForSession` matches
`SessionHistory`, a retired session's late `TurnEnd` could clear a turn its successor opened. It would
require two distinct producer tags resolving to the same conversation at once, and the tree admits no such
pair — a `/clear` re-keys **one** pool entry in place (same `Runner`, same process, same `Parser`), and the
Parser's sink tag is fixed at **runner construction** (`cfg.SessionID`, `streamsup_runner.go:105`) while
`RestartFresh` only rotates the runner's internal *spawn* id. Every id reachable via `SessionHistory`
therefore belongs to the same runner that continues under the successor id, tagging its events identically
either way. `SessionHistory`'s only production writer is `RebindSession`
(`internal/conversations/registry.go:241`), reached solely from `ReasonClear`
(`internal/sessions/transition.go:59,78`) — so eviction can't supply a second producer either, being
binding-neutral. One benign, non-bug case survives: between an eviction and the conversation's next
binding, the evicted id is still `CurrentSessionID`, so a late `TurnEnd` from the dying child resolves and
clears — the correct answer for that conversation, and idempotent with the teardown clear itself.

**Ships unwired**, same as #1201: nothing reads `Busy`/`WaitIdle` yet, no v2 frame changes, no delivery
behaviour changes. See [codebase/1202.md](../codebase/1202.md).
