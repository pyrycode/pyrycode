# Spec #741 — Resolve session→conversation binding in the `session_transition` producer and stamp `conversation_id`

**Size:** XS (PO sized S; downgraded — see § Size).
**Security-sensitive:** yes (outbound dispatch decision; mis-resolution = cross-conversation confidentiality leak). Security review appended at end.

## Files to read first

- `cmd/pyry/session_transition_v2.go` — the producer. `broadcast` (line 106) maps `t → payload` via `toWirePayload` (line 163), marshals once, fans to interactive conns. `newSessionTransitionEmitterV2` (line 61), `sessionTransitionEmitterV2` struct (line 46), `startSessionTransitionStreamV2` (line 200). The SECURITY comment block (lines 38–45) must be updated (it currently asserts the payload "carries no application content").
- `cmd/pyry/relay.go:281` — `startRelayV2` already takes `convReg *conversations.Registry` as a parameter; the `startSessionTransitionStreamV2` call is at line 381, inside `startRelayV2`'s body, where `convReg` is in scope. `relay.go` already imports `internal/conversations` (line 10). This is why no `main.go`/`startRelay` threading is needed (see § Design).
- `internal/conversations/registry.go:152` — `List()` returns a lock-guarded copy of the conversation slice; `RebindSession` (line 213) is the single-owner + empty-`oldID`-guard precedent to mirror in the read scan. There is **no** by-session-id read method — confirming the ticket's "duplicated scan here" decision.
- `internal/conversations/conversation.go:44–59` — `CurrentSessionID string` and `SessionHistory []string` fields. `SessionHistory` is **append-only, never capped** (only writer is `RebindSession`).
- `internal/sessions/transition.go:57` — `notifyTransition` rebinds (`rebindConversation` → `RebindSession`) **before** the observer fan-out on a `ReasonClear`; eviction is binding-neutral (no rebind, id retained). This is the structural ordering the read lookup relies on (#739, merged).
- `internal/protocol/messaging.go:58` — `SessionTransitionPayload.ConversationID` (`conversation_id`, plain string, no `omitempty`) exists from #740; its doc comment names this ticket as the producer that binds it.
- `cmd/pyry/session_transition_v2_test.go` — existing producer tests to update; `mixedSnapshot` (line 94), `decodeSessionTransition` (line 20).
- `cmd/pyry/interactive_turn_v2_test.go:22–73` — `fakeInteractiveBcast` (`snapshots`, `pushErr`), `recordedPush`, `pushesFor`, `pushTypes`. `discardLogger` is in `assistant_turn_test.go:38`. The new tests reuse these.
- `cmd/pyry/session_router_test.go:41` — precedent for constructing a `&conversations.Registry{}` + `Create(...)` in a `cmd/pyry` test (the read-scan unit test reuses this).

## Context

The `session_transition` event ships today (capability-gated to `interactive` phones) but its `conversation_id` is always `""`, so `pyrycode-mobile#336` has no routing key for the boundary marker. Both prerequisites have landed: the wire field (#740) and the maintained binding across rotation (#739). This ticket resolves the transitioning session's owning conversation in the producer and stamps the key onto every emitted envelope, dropping the whole event when the binding is unresolvable.

## Design

### Shape

One new value flows into the emitter: a **resolver closure** `func(sessionID string) (conversationID string, ok bool)`. It is built where `convReg` is concrete (`relay.go`, inside `startRelayV2`), captures the registry, and is threaded one hop into the emitter. `session_transition_v2.go` sees only the `func` type — it never imports `internal/conversations`, preserving the purity the ticket requires.

**Why no `main.go` threading (the ticket's estimate was thicker than reality).** The ticket described mirroring `boundHostFunc`'s `main.go → startRelay → startRelayV2 → …` thread. `boundHost` needs the supervisor+pool, built in `runSupervisor`. Our resolver needs *only* `convReg`, which is **already** a parameter of both `startRelay` and `startRelayV2`. So the closure is constructed at the `startSessionTransitionStreamV2` call site (`relay.go:381`) — no new parameter on `startRelay`, no `main.go` edit. Only `startSessionTransitionStreamV2` and `newSessionTransitionEmitterV2` gain a parameter.

### Components

**1. `conversationForSession` (new, `cmd/pyry/relay.go`)** — the duplicated read scan.

- Contract: `func conversationForSession(convReg *conversations.Registry, sid string) (string, bool)`.
- Behavior: empty `sid` → `("", false)` immediately (mirrors `RebindSession`'s empty-`oldID` guard — never matches an unbound conversation whose `CurrentSessionID == ""`). Otherwise scan `convReg.List()`; a conversation matches iff `c.CurrentSessionID == sid` **or** `slices.Contains(c.SessionHistory, sid)`. First match wins (single-owner invariant — a session id binds exactly one conversation for life). Return `string(c.ID), true`; no match → `("", false)`.
- Needs `slices` added to `relay.go`'s import block.
- Pure over its inputs + the `List()` snapshot; race-safe because `List()` copies the slice header under `r.mu`, and a concurrent `RebindSession` append writes at index `len` (never an index the captured `[0,len)` read touches) or reallocates (leaving the captured backing array immutable). Invariant asserted by the broadcast tests, not by a lock held across the scan.

**2. `sessionTransitionEmitterV2` (modify, `session_transition_v2.go`)** — gains one field `resolveConv func(string) (string, bool)`, set by the constructor. `newSessionTransitionEmitterV2(bcast, resolveConv, logger)` — new parameter inserted before `logger` (keeps logger last, the package idiom).

**3. `broadcast` (modify)** — resolve + stamp + drop, inserted between `toWirePayload` and the marshal:

1. `payload, ok := toWirePayload(t)` — unchanged; `!ok` drops (unknown reason).
2. `convID, ok := e.resolveConv(payload.NewSessionID)`. If `!ok` → **drop the whole event**: log at Debug (`event: "session_transition.unresolved_conversation"`, `reason` only — no ids), `return`. `Run`'s loop survives to the next transition (identical shape to the existing unknown-reason drop). Resolution and the stamp happen **once per transition**, before the per-conn loop — the same `conversation_id` applies to every conn.
3. `payload.ConversationID = convID`.
4. `json.Marshal(payload)` → fan out — unchanged below this point.

**Which id to resolve, and why `payload.NewSessionID`.** `payload.NewSessionID` is the *current/live* binding id for both reasons: for `clear` it is `t.NewID` (== `CurrentSessionID` after #739's rebind-before-fan-out); for `idle_evict` it is the evicted `t.PreviousID` mirrored onto both wire id fields by `toWirePayload` (the retained binding id — `t.NewID` is empty for eviction). Resolving by this single field is branch-free, matches the wire semantic "`conversation_id` names the conversation owning `new_session_id`," and survives a hypothetical future `SessionHistory` cap (it uses the freshest id). The `SessionHistory` half of the scan covers the async-drain race: if a *second* rotation advanced `CurrentSessionID` past this transition's `NewID` before `Run` drained it, `NewID` is then in `SessionHistory` and still resolves to the same conversation. (`payload.NewSessionID` is non-empty for both known reasons; were the eviction-mirror invariant ever broken to emit `""`, the empty-`sid` guard drops the event — fail-safe, never a leak.)

**4. `startSessionTransitionStreamV2` (modify)** — gains the `resolveConv func(string) (string, bool)` parameter; passes it to `newSessionTransitionEmitterV2`. Caller (`relay.go:381`) passes `func(sid string) (string, bool) { return conversationForSession(convReg, sid) }`.

### Data flow

```
/clear rotation or eviction
  → Pool.notifyTransition  (rebind-before-fan-out for clear; #739)
  → emitter.Enqueue        (non-blocking; #659)
  → emitter.Run → broadcast(t):
        payload = toWirePayload(t)                       // conversation_id = ""
        convID, ok = resolveConv(payload.NewSessionID)   // scan CurrentSessionID + SessionHistory
        if !ok: drop whole event, return                 // AC#3
        payload.ConversationID = convID                  // AC#1
        marshal once → fan to interactive conns          // AC#4 gate unchanged
```

## Concurrency model

No new goroutines. The emitter keeps its single `Run` goroutine; `resolveConv` runs on it, synchronously, once per transition. `convReg` is the daemon-singleton registry, internally mutex-guarded — the closure's `List()` call is concurrency-safe against the pool's `RebindSession` writes. No new locks, no lock-ordering change. The unresolvable drop returns from `broadcast`; `Run`'s `select` loop continues (goroutine lifecycle unchanged — exits only on ctx-cancel / channel close, as before).

## Error handling

- **Unresolvable binding (race: session torn down / conversation deleted before drain)** → whole-event drop, Debug log (reason only), `Run` survives. AC#3.
- **Unknown reason / marshal error** → existing drops, unchanged.
- **Per-conn Push error** → existing per-conn Debug log + continue, unchanged (resolved-case resilience untouched). AC#3 last clause.
- `conversationForSession` never errors — `(string, bool)`; the `false` arm is the drop signal.

## Testing strategy

Unit tests only (stdlib `testing`, table-driven, no new harness). Reuse `fakeInteractiveBcast` / `pushesFor` / `discardLogger`.

**Update existing emitter constructions** (6 call sites in `session_transition_v2_test.go`): add the new resolver argument. For the broadcast tests that expect delivery, pass a stub returning `("conv-1", true)`; for `TestSessionTransitionEnqueue_NonBlockingDropOnFull` the resolver is never called (any stub).

**`TestSessionTransitionBroadcast_Clear` / `_Eviction` (update):** assert the decoded payload now carries `conversation_id == "conv-1"` (the stub's value), alongside the existing id/reason/`workspace_cwd` assertions.

**`TestConversationForSession` (new, table-driven against a real `&conversations.Registry{}` + `Create`):**
- current-binding hit — conversation with `CurrentSessionID == "sess-b"`, lookup `"sess-b"` → its id, true (the `clear` post-rebind / `idle_evict` retained case).
- history hit — conversation with `CurrentSessionID == "sess-c"`, `SessionHistory == ["sess-a","sess-b"]`, lookup `"sess-b"` → its id, true (the double-rotation race case).
- miss — lookup a session id no conversation owns → `("", false)`.
- empty-sid guard — a conversation with `CurrentSessionID == ""` present; lookup `""` → `("", false)` (must NOT match the unbound conversation).
- single-owner — two conversations, only one owns the id → returns that one.

**`TestSessionTransitionBroadcast_UnresolvableDrops` (new):** resolver returns `("", false)`; assert zero pushes to any conn (whole-event drop) and that a follow-up `broadcast` with a resolvable resolver still delivers (Run-survives shape — exercise by calling `broadcast` twice on the same emitter).

**`TestSessionTransitionBroadcast_ResolvesByNewSessionID` (new):** an `idle_evict` transition (`NewID == ""`); resolver records the `sid` it was called with; assert it was `payload.NewSessionID` (== the mirrored `PreviousID`), proving eviction resolves the retained id, not the empty `NewID`.

## Size

PO sized **S** with an explicit "architect may downgrade to XS if the threading is thinner than estimated." It is thinner: `convReg` is already a `startRelayV2` parameter, so the closure is built at the call site with **zero** `main.go`/`startRelay` changes.

- **Files:** 2 production (`session_transition_v2.go`, `relay.go`) + 1 test. Under the §4 ≥5-production-file gate.
- **Total written:** ~50 production LOC + ~120 test LOC ≈ 170 LOC. Well under 600.
- **New exported types:** 0 (resolver type is an unexported `func` in package `main`; `conversationForSession` is unexported; no new `Registry` method — the scan is duplicated cmd-side per the ticket).
- **Edit fan-out:** `newSessionTransitionEmitterV2` has 7 callers (6 test + 1 prod), all mechanical one-arg additions; `startSessionTransitionStreamV2` has 1 caller. Both < 10. `codegraph_impact` confirms `startSessionTransitionStreamV2` touches only `relay.go` + itself.
- **Reject branches:** 1 new (unresolvable drop) atop 2 existing.
- **ACs:** 5 cohesive scenario-assertions of one fix.

## Open questions

- **Resolver type: named vs inline.** Spec uses an inline `func(string) (string, bool)` on the struct/params (mirrors how small closures are passed elsewhere). A named `type sessionConvResolver func(string) (string, bool)` is equally acceptable if the developer finds it clearer; not required.
- None blocking.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST-FIX. The boundary is the outbound dispatch decision — what `conversation_id` to stamp on a phone-bound frame. It is explicit and single-point: `resolveConv(payload.NewSessionID)` in `broadcast`, returning a value from the authoritative `conversations.Registry` binding — never a guessed, defaulted, or caller-supplied id. The registry's single-owner invariant (a session id binds exactly one conversation for life; `RebindSession` re-points within one conversation and session ids are unique) guarantees the scan cannot resolve to a *different* conversation than the one that owns the session. No untrusted input reaches the resolver: `sid` comes from `toWirePayload` over the pool's own `SessionTransition`, not the network.
- **[Error messages, logs, telemetry]** No MUST-FIX. AC#5 enforced: `conversation_id` is never logged. The new unresolved-drop log carries `event` + `reason` only (no session ids, no conversation id). The existing emitter SECURITY comment (lines 38–45) asserting the payload "carries no application content" is now stale and **must be updated** by the developer to record that `conversation_id` is a routing key treated as sensitive and kept out of logs — flagged as a required edit in § Design, not a separate gate. The marshaled payload and `err.Error()` remain unlogged.
- **[Concurrency]** No MUST-FIX. No new locks; no lock-ordering change. The read scan reads a `List()` snapshot whose header is copied under `r.mu`; concurrent `RebindSession` appends are race-safe (append writes at/above `len`, or reallocates — neither overlaps the captured `[0,len)` read). No goroutine added; the drop path returns from `broadcast` leaving `Run` alive.
- **[Threat model alignment — cross-conversation confidentiality]** No MUST-FIX. The named threat (mis-resolution leaks one conversation's boundary into another's thread) is addressed by (a) resolving against the maintained binding only, and (b) the **fail-closed drop**: an unresolvable binding (deleted/torn-down session) emits no envelope rather than a guessed or empty key (AC#3). The async-drain double-rotation race resolves correctly via the `SessionHistory` half of the scan; the empty-`sid` guard prevents matching an unbound conversation. This mirrors the confidentiality discipline of the sibling reply-stream resolver (`resolveTarget` retries-never-falls-back under a non-empty cursor, `interactive_turn_stream_v2.go`).
- **[Tokens/secrets, File ops, Subprocess, Crypto, Network & I/O]** Not applicable — this ticket adds no token/secret handling, no filesystem path construction, no subprocess invocation, no cryptographic primitive, and no new socket Read/timeout surface. It is a pure in-memory registry read + a field stamp on an already-gated, already-bounded fan-out.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-23
