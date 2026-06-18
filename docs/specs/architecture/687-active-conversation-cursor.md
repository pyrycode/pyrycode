# Spec #687 — structured turn stream stamps and follows the active-conversation cursor

**Size:** XS (PO sized S; architect downgrades — the verified `cursorReader` injection seam means the live read site does not change, and the concurrency reconcile is a ~12-LOC guarded holder + one `-race` probe).

## Files to read first

- `cmd/pyry/interactive_turn_v2.go:104-137` — emitter constructor `newInteractiveTurnEmitterV2(sup cursorReader, …)` and the cursor read `convID := e.sup.CurrentConversation()` at `:131`; the empty-cursor drop at `:132-137`. **This read site does not change** — it reads through the `cursorReader` interface, so re-keying is an injection swap at the constructor call.
- `cmd/pyry/interactive_turn_stream_v2.go:45-66` — `startInteractiveTurnStreamV2`: the emitter is built at `:52` (`newInteractiveTurnEmitterV2(sup, mgr, logger)`) and the #647 replay source is set at `:60` (`mgr.SetReplaySource(emitter.ring, sup.CurrentConversation)`). **Both readers** re-key here. `sup *supervisor.Supervisor` stays a param — `NewSessionSubscriber(sup, …)` at `:66` still needs it.
- `cmd/pyry/assistant_turn.go:20-26` — the `cursorReader` interface (`interface { CurrentConversation() string }`). The new holder satisfies this verbatim.
- `cmd/pyry/main.go:647-705` — `errNoBoundSession`, `sessionRouter` struct (`:662`), `Route` (`:671`), `boundSession`. `Route`'s success path (after `pool.Lookup` succeeds, before `return boundSession{…}`) is where the signal is stamped. `Route` has a **value receiver** behind the `handlers.SessionRouter` interface — the holder field must be a **pointer** so the copy writes the one holder the emitter reads.
- `cmd/pyry/main.go:580-586` — `runSupervisor`'s `startRelay(…, sessionRouter{pool: pool, convReg: convReg}, …)` call: the single production construction site. Construct the holder here and pass it both into the `sessionRouter` literal and (separately) down the wiring chain.
- `cmd/pyry/relay.go:88-102` (`startRelay` signature), `:145` (`startRelayV2` call), `:272-287` (`startRelayV2` signature), `:348` (`startInteractiveTurnStreamV2` call) — the three signatures that gain a `*activeConversation` param, each with exactly one caller.
- `internal/relay/v2session.go:1436` — `SetReplaySource(ring *eventring.Ring, currentConv func() string)`; second arg is `func() string`, so a method value `activeConv.CurrentConversation` drops in where `sup.CurrentConversation` is today. **No `internal/relay` change.**
- `cmd/pyry/assistant_turn_test.go:22-32` — `stubCursor` (`atomic.Value`-backed `CurrentConversation()` + `set(id)`). The production holder mirrors this exactly; it is also the precedent for the holder's shape.
- `cmd/pyry/session_router_test.go:35-100` — `TestSessionRouter_Route`. The literal at `:46` gains the holder field; the success subtest at `:48-63` gains a "Route stamped the holder" assertion; the reject subtests assert the holder stays empty.
- `docs/knowledge/codebase/678.md` — the documented outbound asymmetry this ticket closes ("inbound now routes per-conversation; outbound still taps the bootstrap PTY").

## Context

Since #678, an inbound `send_message` is routed to the conversation's own bound claude session via `sessionRouter.Route` (`cmd/pyry/main.go:671`), which writes the turn through `boundSession.WriteUserTurn` → that session's *own* supervisor. The **outbound** structured turn stream, however, still reads its conversation cursor from the **bootstrap** supervisor: the emitter does `convID := e.sup.CurrentConversation()` (`interactive_turn_v2.go:131`) and **drops every event on an empty cursor** (`:132-137`). Routed turns now commit on bound-session supervisors and never touch the bootstrap supervisor's cursor, so the bootstrap cursor stays empty and the structured reply stream goes silent after the first per-conversation route.

This ticket introduces the missing signal — *"the conversation currently being interacted with"* — and re-keys the structured stream's two cursor readers (live emit + #647 replay) to it. After this, the stream emits again and each envelope carries the routed conversation's `conversation_id`.

**Scope boundary.** This ticket does **not** change which transcript the producer tails (`resolveLatestSessionJSONL` stays recency-resolved) — that is #679, which `blocked-by` this ticket. In the single-operator case the active bound session is also the most-recent JSONL writer, so attribution and content already agree; the cross-conversation hazard (a *different* session writing more recently) is #679's. This is a real, shippable increment on its own: it un-breaks the silent stream and fixes attribution.

## Design

### The signal — `activeConversation` (new, in `cmd/pyry/main.go`)

A small, concurrency-safe holder of one conversation id, owned by `cmd/pyry`. Contract:

- `func (a *activeConversation) set(id string)` — stamps the current conversation. Called from `sessionRouter.Route` on the successful-route path only.
- `func (a *activeConversation) CurrentConversation() string` — returns the stamped id, `""` before any route. Satisfies the existing `cursorReader` interface (`assistant_turn.go:24`) verbatim, and its method value satisfies `SetReplaySource`'s `func() string`.

It lives in `main.go` beside `sessionRouter`/`boundSession`/`sessionMinter`/`poolResolver` — the family of `cmd/pyry` adapters that bridge `internal/*` packages. Mechanism: a `sync.Mutex`-guarded `string` (mirrors the supervisor's own `convMu`+`currentConvID` cursor) **or** an `atomic.Value` (mirrors the `stubCursor` test double at `assistant_turn_test.go:22`). Either is correct; pick the one that reads cleanest against the surrounding code. Unexported — no new exported type.

### Wiring — one holder, two references

`runSupervisor` (`main.go`, near `:580`) constructs **one** holder and shares it two ways:

1. **Writer side** — stored as a pointer field `active *activeConversation` on the `sessionRouter` literal (`main.go:582`), so `Route` can stamp it.
2. **Reader side** — passed as a new `*activeConversation` parameter down `startRelay` → `startRelayV2` → `startInteractiveTurnStreamV2`, where it becomes the emitter's cursor and the replay source.

Explicit param threading (not extracting the holder off the `handlers.SessionRouter` interface, which only exposes `Route`) keeps the dependency visible and avoids a type assertion. Each of the three signatures gains one `*activeConversation` arg; each has exactly one caller.

### `Route` stamps on success only (`main.go:671`)

After `sess, err := r.pool.Lookup(id)` succeeds and before `return boundSession{…}`, call `r.active.set(conversationID)`. The argument `conversationID` is the routed conversation's id (already confirmed present by the `convReg.Get` at the top of `Route`). The signal is **not** stamped on any reject path — unknown conversation (`ErrConversationNotFound`), empty binding (`errNoBoundSession`), or dangling id (`ErrSessionNotFound`) — so a failed route never moves the cursor.

### `startInteractiveTurnStreamV2` re-keys both readers (`interactive_turn_stream_v2.go:52,60`)

- `:52` — `newInteractiveTurnEmitterV2(activeConv, mgr, logger)` (was `sup`). The emitter's `:131` read now flows from the active-conversation signal. No edit at `:131`.
- `:60` — `mgr.SetReplaySource(emitter.ring, activeConv.CurrentConversation)` (was `sup.CurrentConversation`). The reconnect-replay attribution follows the same signal. **This is a correctness reconcile, not cosmetic:** leaving `:60` on the empty bootstrap cursor would re-introduce the exact empty-cursor drop on the replay path (#647 SetReplaySource is the second reader — see [[po-cursor-rekey-has-second-replay-reader]]).
- `sup *supervisor.Supervisor` stays a param — `NewSessionSubscriber(sup, resolve, tr, logger)` at `:66` still subscribes over the supervised session.

### Data flow (after)

```
phone send_message ──► dispatch ──► handlers.SendMessage ──► sessionRouter.Route(convID)
                                                                  │  (success)
                                                                  ├─► boundSession.WriteUserTurn  (turn commits on bound supervisor)
                                                                  └─► activeConversation.set(convID)   ◄── NEW
                                                                            │
              ┌─────────────────────────────────────────────────────────── │ (read on producer's single Run goroutine)
              ▼                                                              ▼
   emitter.Handle: convID := activeConv.CurrentConversation()      SetReplaySource(ring, activeConv.CurrentConversation)
   (interactive_turn_v2.go:131 — emits, stamps right convID)       (interactive_turn_stream_v2.go:60 — replay attribution)
```

### Readers left unchanged (out of scope)

Two other bootstrap-cursor readers exist and are **intentionally not re-keyed** here — they serve the *coarse* (non-structured) surface, not the operator-facing structured stream this ticket fixes:

- `assistant_turn.go:102` — v1 coarse bridge (legacy, `PYRY_MOBILE_V2` unset).
- `assistant_turn_v2.go:110` — v2 coarse bridge, fans to **non-interactive** v2 conns (#589/#634; interactive conns get the structured stream instead, per `relay.go:330-338`).

Interactive operators receive the structured stream (readers re-keyed here); the coarse bridges serve a different audience. Re-keying them shares the same shape but is a separate concern — noted under Open questions, not built.

## Concurrency model

- **Write:** `activeConversation.set` runs on the routing-path goroutine (the per-conn dispatch goroutine that invokes `handlers.SendMessage` → `Route`).
- **Read:** `CurrentConversation` runs on the producer's single Run goroutine — both the live `emitter.Handle` path (`OnEvent` invoked serially) and the replay-source method value the manager calls. These are different goroutines from the writer.
- **Guarantee:** the holder's internal mutex (or `atomic.Value`) makes the read/write race-free. This is the one piece of new synchronization; the emitter's other counters stay unguarded-single-goroutine exactly as documented at `interactive_turn_v2.go:42-44` — the holder absorbs the cross-goroutine hand-off so nothing else in the emitter has to. No new lock ordering: the holder's lock is a leaf, never held while calling out.
- The holder is the same precedent the codebase already trusts: the supervisor's `convMu`+`currentConvID` cursor and the `stubCursor` test double are both exactly this shape.

## Error handling

No new error paths. The holder has no failure mode (set/get are total). `Route`'s existing error contract is untouched — the only change is a side-effecting `set` on the already-existing success path. The empty-signal state (`""`) is the well-defined "no conversation routed yet" case and is handled by the emitter's pre-existing empty-cursor drop (AC#4).

## Testing strategy

Unit tests (stdlib `testing`, `t.Parallel()` where safe). Test files do not count against the production budget.

- **`activeConversation` holder (new test file, e.g. `cmd/pyry/active_conversation_test.go`):**
  - Zero value → `CurrentConversation()` returns `""`.
  - `set("x")` then `CurrentConversation()` returns `"x"`; a second `set("y")` overwrites.
  - **Race probe (AC#3):** one goroutine loops `set`, another loops `CurrentConversation`, run under `go test -race`; assert no race and a returned value is always a value that was set (or `""`).
- **`sessionRouter.Route` stamps the signal (extend `session_router_test.go`):**
  - Add the holder to the literal at `:46` (`active: &activeConversation{}`).
  - Success subtest: after `Route("conv-bound")` succeeds, `r.active.CurrentConversation()` == `"conv-bound"`.
  - Each reject subtest (unknown / unbound / dangling): `r.active.CurrentConversation()` stays `""` — a failed route never moves the cursor.
- **Emitter reads the re-keyed signal (AC#2):** already covered structurally — `newInteractiveTurnEmitterV2` takes a `cursorReader`, and the existing emitter tests (`interactive_turn_v2_test.go`) drive a non-empty cursor → emits with that `conversation_id`, and an empty cursor → drops (`stubCursor`). `activeConversation` satisfies `cursorReader` identically, so those tests cover the live path without modification. Optionally, one small test may wire a real `activeConversation` as the emitter's cursor, `set` it, and assert the emitted envelope's `conversation_id` — nice-to-have, not required for coverage.
- **Build/vet:** `go build ./...`, `go vet ./...`, `go test -race ./cmd/pyry/...`.

## Open questions

- **Coarse bridges share the same bootstrap-cursor dependency.** `assistant_turn.go:102` (v1) and `assistant_turn_v2.go:110` (v2 non-interactive) read the same empty bootstrap cursor post-#678 and would also go silent for a routed conversation. They are out of scope here (the structured interactive stream is the operator-facing surface this ticket targets). If the non-interactive coarse surface needs the same fix, file a follow-up — the `activeConversation` holder introduced here is directly reusable as their cursor too.
- **Holder mechanism (mutex vs `atomic.Value`).** Developer's call within the codebase idiom; both are correct. The mutex mirrors the supervisor cursor; `atomic.Value` mirrors the test double. No behavioural difference.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No MUST FIX. The id written into the signal is **not** raw attacker input: `sessionRouter.Route` stamps it only after `convReg.Get(conversationID)` confirms the conversation exists *and* `pool.Lookup` resolves its non-empty bound session (`main.go:671-685`). The empty-binding guard (`errNoBoundSession`, #678 AC#4) still fires before any stamp, so an unbound/unknown/dangling id never reaches `set`. The signal therefore reflects an already-authorized route — the boundary (phone frame → trusted state) is the existing `Route` gate, not widened here.
- **[Threat model — cross-conversation confidentiality]** OUT OF SCOPE (pre-existing, not regressed). The structured stream broadcasts to **all** interactive conns by capability with **no conn→conversation binding for output**, keyed off one global cursor. Under concurrent multi-operator routing, the last-writer-wins cursor could attribute in-flight events to the most-recently-routed conversation. This is **not introduced** by this ticket: it is exactly the single-global-cursor behavior the pre-#678 bootstrap cursor already had (every turn stamped the one bootstrap cursor). #678 emptied that cursor (breaking delivery, not leaking); this ticket restores a single global cursor that behaves identically for attribution. The supported deployment is single-operator (cursor and content agree). Multi-operator isolation is undesigned and owned by #679 (content follows the bound transcript) + a future conn→conversation subscription — see [[po-reply-stream-resolver-distinct-and-broadcast-fanout]]. No confidentiality regression vs. the historical behavior.
- **[Tokens / secrets / crypto]** No findings — N/A. The holder stores one non-secret UUID conversation id; no tokens, keys, credentials, or crypto primitives are touched. Conversation-id generation (`conversations.NewID` over `crypto/rand`) is unchanged and elsewhere.
- **[File operations]** No findings — N/A. The holder is purely in-memory; nothing is read from or written to disk. The transcript resolver (`resolveLatestSessionJSONL`, #668's `ResolveTranscript`) is explicitly untouched (AC#4) — no path handling, TOCTOU, or permission surface added.
- **[Subprocess / external command]** No findings — N/A. No `exec`, no environment handling, no signals introduced.
- **[Network & I/O]** No findings — N/A. No new socket reads/writes or input parsing; envelopes fan out via the established `V2SessionManager.Push` + capability gate, unchanged. The id is already validated upstream, so no new size/shape cap is needed at this layer.
- **[Error messages / logs / telemetry]** No findings. `conversation_id` is already a logged field on the emitter's existing log lines and is a non-secret UUID. The holder logs nothing. The emitter's "never log application output" discipline (`interactive_turn_v2.go:60-66`) is untouched — this change alters *which* conversation_id is stamped, never whether content is logged.
- **[Concurrency]** No MUST FIX (covered by AC#3 + the `-race` test). The single new cross-goroutine state is mutex/atomic-guarded; the lock is a leaf (never held across a call-out, never nested), so no lock-ordering hazard. `set`/`CurrentConversation` are each atomic single operations — no check-then-act/TOCTOU on the holder. No new goroutine is spawned (writer = existing dispatch goroutine; readers = existing single Run goroutine). The holder is in-memory, so signal-mid-write leaves no partial on-disk state. The emitter reads the cursor once per `Handle`, giving deterministic per-event last-writer-wins attribution — the intended semantics.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-18
