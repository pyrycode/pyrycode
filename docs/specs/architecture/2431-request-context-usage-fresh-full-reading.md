# Spec #2431 — answer a client's `request_context_usage` with a fresh full reading

Split from #2293, itself split from #2205. The split-depth cap stops at two, so the
refiner applied `needs-human:sizing` and recorded the split it would otherwise have
made; this builds as one ticket. See § Sizing below for the re-count against the
written plan.

## Files read

Production surface:

- `internal/protocol/codes.go` → `CodeModelListUnavailable`, `CodeMCPStatusUnavailable`,
  `TypeRequestModelList`, `TypeContextUsage` — the mint-with-the-handler rule and the
  two-code reject vocabulary this verb copies; `TypeContextUsage`'s own block already
  names #2293 as the on-demand producer and states the frame is not re-minted.
- `internal/protocol/interactive.go` → `MCPStatusRequestPayload`,
  `RequestModelListPayload`, `ContextUsagePayload` — the request shape to copy
  (one unconditional `conversation_id` key, no omitempty, correlation on the
  envelope), and the answer's mixed-provenance contract.
- `internal/protocol/handshake.go` → `CapabilityInteractive`, `CapabilityQuestion`,
  `CapabilityModelList` — the detection-only capability idiom and its stated reason
  for not gating on the new string.
- `internal/relay/v2session_mcpstatus.go` → `handleMCPStatusRequest`,
  `emitMCPStatusReply`, `forwardMCPStatusReply` — the closest working off-Run handler:
  decode → membership → seam → correlated reply, every reply through `forwardToRun`.
- `internal/relay/v2session_modelrequest.go` → `handleRequestModelList`,
  `rejectModelListRequest`, `modelListReplyError` — the reject-and-logging shape, the
  tolerate-the-payload-decode decision, and the per-arm never-log rule for the
  conversation id.
- `internal/relay/v2session.go` → `dispatchAppFrame`, `enqueueAppFrame`, `appFrameJob`,
  `appFrameKind`, `appFrameWorker`, `forwardToRun` — where the inert gates sit, how a
  kind is appended, and the single-owner-send-CipherState invariant.
- `internal/relay/v2session_seams.go` → `MCPStatusFor`, `ModelListFor`, `V2SessionConfig`
  — the conversation-keyed waiting-seam contract this one is modelled on.
- `internal/relay/v2session_handshake.go` → `supportedV2Capabilities`,
  `negotiateCapabilities` — append-never-insert, and the value-specific
  `slices.Contains` that keeps a non-interactive grant non-interactive.
- `internal/streamsup/runner.go` → `QueryContextUsage`, `contextUsageQueryIDPrefix`,
  `nextControlID` — #2430's waiting query: one bool collapses every not-answered
  outcome, the caller's context is the only bound on a silent child, and the parser
  claims the response before the shared sink.
- `internal/streamsup/envelope.go` → `contextUsageDetailAllowed` — the two accepted
  detail values, `"summary"` and `"full"`.
- `internal/turnbridge/outbound.go` → `MapEvent`'s `turnevent.ContextUsage` arm — the
  one mapper from reading to payload; the on-demand path must not fork it.
- `cmd/pyry/main.go` → `resolveBoundRunner`, `resolveBoundMCPStatus`, `mcpStatusFor`,
  `mcpStatusQuerier` — the resolver shape, and the `CurrentSessionID == ""` guard that
  is the #678 cross-conversation isolation enforcement point.
- `cmd/pyry/streamsup_runner.go` → `streamRunner.QueryMCPStatus` — the adapter idiom
  for a method deliberately kept off `sessions.Runner`.
- `cmd/pyry/stream_turn_busy.go` → `turnBusyTracker`, `WaitIdle`, `waitIdleForDelivery`
  — the mid-turn deferral primitive: idle, unknown and unbound all return at once, and
  the membership check plus generation-channel capture happen under one lock.
- `cmd/pyry/relay.go` → the worker struct's `mcpStatusFor` / `busy` fields and the
  `V2SessionConfig` literal — where the seam is composed and wired.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes` — the guard that
  reads `dispatchAppFrame`'s case selectors as the registry of dispatch decisions.
- `internal/e2e/relay_v2_mcp_status_request_test.go` → the fake-daemon leg shape for a
  conversation-keyed request verb.

Knowledge base: `docs/knowledge/INDEX.md`, and `CLAUDE.md` § Testing — a fixture a test
writes is thrown away with the worktree, which is why this slice commits no capture and
needs none (#2289's `get_context_usage` fixture already answers both detail values on
the hermetic gate).

## Context

The reading a client gets today arrives only after a turn ends, and it is the cheap one:
`detail:"summary"`, answered from the last response's usage plus local estimates.
`turnEndContextUsageRequester` asks for it on every `TurnEnd` and the mapped frame
travels the unsolicited outbound lane. An operator who wants to know what a long turn
actually consumed has no way to ask.

This slice adds the ask. The expensive reading, `detail:"full"`, counts each category
through claude's token-count API, so it is worth taking only when someone is looking at
the screen — which is exactly what an inbound request means.

Three pieces already exist and this ticket is the join between them: #2370 declared
`TypeContextUsage` and `ContextUsagePayload` as the single outbound shape for both
producers; #2371 landed the post-turn producer and the `turnbridge.MapEvent` arm that
maps a reading to that payload; #2430 landed `streamsup.Runner.QueryContextUsage`, whose
parser claims the child's response before the shared sink so a reading handed to a
waiting caller is not also republished as an unsolicited frame. Nothing calls
`QueryContextUsage` today.

No ADR is warranted. Every decision here is an application of a published one — ADR 037
on per-feature capability gating, and the reject-code and seam contracts already stated
in `codes.go` and `v2session_seams.go`.

## Design

### Wire vocabulary — `internal/protocol`

One inbound type, one new reject code, one capability string. No second outbound shape:
the answer is `TypeContextUsage` carrying `ContextUsagePayload`, correlated by the
request envelope's id on `Envelope.InReplyTo`, exactly as `TypeContextUsage`'s own
declaration block already anticipates.

```go
TypeRequestContextUsage = "request_context_usage" // phone → binary, inbound v2 control (switch-intercepted)

CodeContextUsageUnavailable = "context_usage.unavailable" // hosted conversation, no reading to give; retryable

CapabilityContextUsage = "context_usage"

type RequestContextUsagePayload struct {
	ConversationID string `json:"conversation_id"`
}
```

**Two codes, only one new.** A conversation the daemon does not host is refused with the
existing `CodeConversationNotFound`; `CodeContextUsageUnavailable` carries every "hosted,
but no reading" cause merged — no bound session, no live child, a rotation in flight, a
child that never answered, a caller whose context ended, an unmappable payload, and an
unwired seam alike. `QueryContextUsage`'s own contract already collapses those into one
bool, so a richer wire vocabulary would have nothing to source it from, and separating
"unwired" from "no answer yet" would publish a fact about the machine rather than about
the request. Retryable, following the dominant cause: a busy or rotating child will
answer later and the caller changes nothing to make that happen.

**No third code for a malformed payload.** The decode error is discarded, not rejected —
`handleRequestModelList`'s decision, transferable for its stated reason: the id reaches a
registry membership check and nothing else, never a path component, so a failed decode
leaves `ConversationID == ""`, which names no conversation and is refused at membership.
This is the one place the design departs from `handleMCPStatusRequest`, which mints
`CodeProtocolMalformed` because its own vocabulary already carried it.

`CapabilityContextUsage` is **detection only**. The verb gates on `CapabilityInteractive`
alone; gating on the new string would cut off pyrycode-mobile, which advertises
`interactive` and nothing else. It is appended to `supportedV2Capabilities`, never
inserted — `negotiateCapabilities` emits in that slice's order and both test tables
compare with `slices.Equal`.

`TypeRequestContextUsage` must NOT join `inboundAppTypeSet`: it is a v2 control envelope
intercepted before `dispatch.Route`. `compat_test.go` files it in `v2OnlyTypes`;
`relay_guard_test.go` files it in `inboundTypes` as `switch-intercepted` from the moment
it exists, since the declaration and the handler land together.

### Relay handler — `internal/relay/v2session_contextusage.go` (new)

New file for `v2session_modelrequest.go`'s stated reason: one verb, one file, so the
handler reads beside the reject vocabulary it owns.

**It runs on the connection's `appFrameWorker`, not on Run.** This is the load-bearing
placement decision. Answering is a round trip to claude behind a possible wait for an
open turn — `TypeMCPStatusRequest`'s situation, not `TypeRequestModelList`'s registry
lookup. So `dispatchAppFrame` keeps both inert gates on Run and hands an accepted request
to the worker:

```go
case protocol.TypeRequestContextUsage:
	if m.cfg.ContextUsageFor == nil || !s.interactive {
		return
	}
	m.enqueueAppFrame(ctx, s, appFrameJob{plaintext: plaintext, kind: appFrameContextUsageRequest})
	return
```

`appFrameContextUsageRequest` is **appended** to the `appFrameKind` enum and gets its own
arm in `appFrameWorker`. Every reply — the reading and both rejects — returns through
`forwardToRun`. Emitting from the worker instead would be a concurrent `Encrypt` under
the single-owner send `CipherState`: nonce reuse, a real break rather than a race
annoyance. The file header states this as a constraint on future edits, not a
description.

Handler order, each step preventing the next from learning something it should not:

1. **Inert gates, already discharged on Run.** A conn that did not negotiate
   `interactive`, or a daemon with no seam wired, decodes nothing and replies nothing —
   so neither can learn whether the named conversation exists or that this verb is
   implemented. Re-stated in the handler's doc, enforced at the dispatch arm.
2. **Envelope decode.** Unreachable after `dispatchAppFrame` matched these bytes; no
   trustworthy id to correlate on, so log a content-free warn and return.
3. **Payload decode, tolerated.** Error discarded, never logged — `encoding/json` quotes
   offending input into its error string and those bytes are remote-authored.
4. **Membership.** `KnownConversation == nil || !KnownConversation(id)` →
   `CodeConversationNotFound`, and the requested id is **not** logged on this arm:
   nothing has shape-validated it.
5. **Seam.** `ContextUsageFor(ctx, id)`; comma-ok honoured, payload read only on true →
   `CodeContextUsageUnavailable`. The id is loggable from here on, membership having
   established it is registry-canonical.
6. **Emit.** The payload crosses untouched, `EventID` left nil so the frame never enters
   the #647 replay ring and `forwardEnvelope`'s `last_event_id` dedup stays inert for it.

Never logged on any arm at any level: the payload itself. `ContextUsagePayload` is
mixed-provenance — `ConversationID` is daemon-authored, every other string is claude- or
workspace-authored, and `ContextUsageMemoryFile.Path` carries paths off the operator's
own filesystem.

### Relay seam — `internal/relay/v2session_seams.go`

```go
ContextUsageFor func(ctx context.Context, conversationID string) (protocol.ContextUsagePayload, bool)
```

`MCPStatusFor`'s shape exactly: context and conversation id in, payload and comma-ok out,
so `internal/relay` imports neither `internal/sessions` nor `internal/streamsup` and
`protocol` is already imported on both sides. Its doc block carries the waiting-seam
obligations — honour `ctx` so manager shutdown terminates the wait, never touch
`V2Session` or Noise state, the id is an untrusted lookup key that stays one, and every
string in the returned payload is claude-authored and never logged. Optional: nil makes
the inbound type consumed but inert before any decode.

### Daemon-side resolver — `cmd/pyry/relay_context_usage.go` (new)

Two layers in one file, composed at the relay wiring point.

**The query half is two functions, not one, and the seam between them is where the
canonical id appears.** `resolveContextUsageQuerier(convReg, pool, convID)` is
`resolveBoundRunner` plus a `contextUsageQuerier` type assertion, returning the querier
and the **registry-owned** canonical id. `queryContextUsage(ctx, querier, canonicalID)` is
the round trip: `QueryContextUsage(ctx, "full")` → `turnbridge.MapEvent` stamped with that
canonical id → assert the mapped type. Every refusal returns the zero payload and false;
there is no retained-reading fallback and no bootstrap fallthrough —
`resolveBoundRunner`'s `CurrentSessionID == ""` guard is what keeps an unbound
conversation from reaching the shared bootstrap child (#678).

Splitting them is what lets the collapse key be the canonical id rather than the client's
string; see the ordering below.

`streamRunner.QueryContextUsage` forwards to the concrete runner's method and stays off
`sessions.Runner`, `QueryMCPStatus`'s stated reason: its only consumer is the resolver in
this package, and widening the interface would pull every test double into the slice.

**The collapsing half**, `contextUsageResolver`, sits above it and holds the mid-turn
deferral and the per-conversation collapse. Its exported surface is one method matching
the seam:

```go
func newContextUsageResolver(base context.Context, resolve contextUsageResolveFunc, busy *turnBusyTracker, now func() time.Time) *contextUsageResolver
func (r *contextUsageResolver) Get(ctx context.Context, conversationID string) (protocol.ContextUsagePayload, bool)
```

`now` is the injected clock: nil falls back to `time.Now`, and a test supplies its own so
it can pin behaviour on both sides of the window without sleeping on a wall clock. `base`
is the daemon run context, available at the composition point in `startRelayV2`; § "Whose
context owns a flight" below is why it is not the caller's.

**Collapsing is per conversation, not per connection** — two clients watching one
conversation is the case it exists for — which is why it lives here, below the seam,
rather than in the relay handler. State is one map keyed by conversation id, each entry a
flight:

```go
type contextUsageFlight struct {
	done    chan struct{}          // closed when the round trip settles
	payload protocol.ContextUsagePayload
	ok      bool
	settled time.Time              // when done was closed
}
```

**Resolution precedes collapse, and the order is a containment property rather than a
style choice.** `Get` calls `resolveContextUsageQuerier` FIRST — an in-memory registry
lookup and a pool lookup, no round trip and no wait — and refuses before touching the map
when it fails. Only then does it consult or install a flight, keyed on the **canonical id
the registry returned**, never on the client's string.

Both halves of that matter. Keying on the request string would let any paired client mint
map entries by naming conversations the daemon does not host, turning a map bounded by
hosted conversations into one bounded by request volume. And while
`conversations.Registry.Get` matches byte-exactly today — so the two strings happen to
coincide — that is a property of `internal/conversations`, not of this contract; keying on
the returned id makes the guarantee local instead of borrowed, so a future normalising
`Get` cannot silently split one conversation's collapse across several keys.

With the querier and canonical id in hand, `Get` takes the mutex once and leaves in one of
three states:

- **Join.** An entry exists whose `done` is still open — a round trip is in flight. Wait
  on `done` or `ctx.Done()`, then return that flight's result. No second write to the
  child.
- **Reuse.** An entry exists, settled, and `now().Sub(settled) < contextUsageCollapseWindow`.
  Return its result immediately. No round trip at all.
- **Fly.** Otherwise install a fresh flight, release the lock, then
  `busy.WaitIdle(flightCtx, canonicalID)` followed by the query; record the result, stamp
  `settled`, close `done`.

The two collapse arms are what AC-2 asks for on both sides: a second request arriving
while the first is in flight joins it, and one arriving just after it completes reuses
the settled result.

#### Whose context owns a flight

**The flight runs under the daemon run context, not under the first caller's.** A flight
is shared state: if the caller that happened to install it owned its context, that
client's disconnect would cancel a round trip two other clients are waiting on, settle the
shared flight `ok == false`, and — because refusals are cached — hold that refusal for the
window. One client's departure would deny another's answer. So `flightCtx` derives from
the `base` context passed at construction, which is cancelled by daemon shutdown and by
nothing else. Each caller still waits on its **own** `ctx` for the join, so a departing
caller stops waiting without settling anything.

That detachment costs the bound `QueryContextUsage`'s own doc demands of its caller —
"the caller's context is the only bound on the wait when the child stays alive and simply
never answers, so pass a deadline rather than `context.Background()`" — so the flight
restores it explicitly, and at the one step that needs it:

- `busy.WaitIdle(flightCtx, …)` is bounded by `base` alone. AC-4 requires the request to
  be answered *after* the turn ends, and a turn has no upper bound, so a timeout here
  would answer the wrong thing for exactly the case the criterion names.
- the child round trip runs under `context.WithTimeout(flightCtx, contextUsageQueryTimeout)`
  — a named constant beside the window. This is the deadline `QueryContextUsage` asks for,
  and it is what keeps a live-but-silent child from parking the installing conn's
  `appFrameWorker` until daemon shutdown.

An interrupt is unaffected by either wait: `handleInterrupt` runs inline on Run, not on the
worker, so an operator can always stop the turn a parked request is waiting on.

**A refusal is cached for the window too.** "Both are answered from its result" is
unqualified, and the alternative — caching only successes — makes the two arms behave
differently for no stated reason and leaves a failing conversation free to write to the
child as fast as a client can ask. The window is deliberately short
(`contextUsageCollapseWindow = 2 * time.Second`) so a retryable refusal clears quickly;
that is the whole of the cost.

**The map is bounded by hosted conversations**, not by request rate: a key is created only
past `resolveContextUsageQuerier` and under the id that resolver returned, so a client
cannot mint entries by naming ids the daemon does not host, and a repeat request for the
same conversation replaces the entry rather than adding one.

**The mid-turn deferral has no new primitive.** `turnBusyTracker.WaitIdle` returns at once
when the conversation is idle, unknown or unbound, and blocks otherwise until the turn
closes — and it is called **before** the query, so a request arriving mid-turn writes
nothing to the child until that turn ends (AC-4). A nil tracker (PTY mode, where it is
never constructed) skips the wait; `WaitIdle` itself carries no nil-receiver guard, so the
resolver holds that check, matching how the wiring hands the nil to
`waitIdleForDelivery` and to no other method.

### Wiring

`cmd/pyry/main.go` gains one worker field value,
`contextUsageResolve: contextUsageResolve(convReg, pool)`, beside `mcpStatusFor`.
`cmd/pyry/relay.go` declares the field and composes the resolver where `mcpActuator` is
composed — that scope already holds `w.busy` and `startRelayV2`'s `ctx`, which is the
`base` the flight runs under — then sets `ContextUsageFor` on the `V2SessionConfig`.
Foreground and v1 wirings leave the seam nil, so no existing construction site changes
behaviour.

## Concurrency model

No new goroutine. The handler runs on the addressed connection's existing
`appFrameWorker`, which is strictly FIFO per conn, so a conn has at most one context-usage
request in flight and a slow one stalls only that conn's later frames. Replies reach the
Run goroutine through the existing `m.appReply` channel; nothing on this path touches
`s.send`, `s.recv` or any other Run-owned state.

Inside the resolver: one mutex guarding one map, held across the check-and-install so a
joiner cannot miss a flight that is being created, and **released before** `WaitIdle` and
the query — the two operations that block. Nothing else takes it, so there is no lock
ordering to state. `WaitIdle` takes the tracker's own mutex and releases it before
blocking, so the two are never held together.

The flight's `done` channel is closed exactly once, by the goroutine that installed it,
after both result fields are written — so a joiner reading them after `done` closes has a
happens-before edge to those writes. Joiners never write to the flight.

Shutdown: a joiner is bounded by its own caller `ctx` (the manager's run context via the
worker); the flight itself is bounded by `base`, the daemon run context, plus
`contextUsageQueryTimeout` on the child round trip. Both cancel on shutdown. A flight that
ends for any reason — answered, timed out, or cancelled — closes `done` with its result
recorded, so joiners are always released rather than orphaned; the close happens in a
`defer` on the flying path so a panic in the query cannot strand them either.

## Error handling

| Condition | Answer |
|---|---|
| Not interactive, or seam nil | Fully inert — no decode, no reply |
| Envelope undecodable | Content-free warn, no reply (no id to correlate on) |
| Payload undecodable | Tolerated → empty id → refused at membership |
| Conversation not hosted | `CodeConversationNotFound`, non-retryable, id not logged |
| Hosted, no reading | `CodeContextUsageUnavailable`, retryable, id logged |
| Reply marshal fails | Content-free warn, no reply (closed struct; unreachable) |
| Session tearing down | `forwardToRun` false → debug record, dropped |

Neither refusal is met with silence (AC-3). Every reject message is a compile-time
constant; no value derived from an error, an id, or claude's bytes reaches a client-visible
message or a log field.

## Testing strategy

Unit, `internal/protocol`: the new constants' values; `RequestContextUsagePayload`'s wire
key set and its zero-value key presence (an `omitempty` added later must redden); the
compat-drift partition placing the request in `v2OnlyTypes` with `inboundAppTypeSet`'s
count unmoved; the capability constant's value.

Unit, `internal/relay`: table-driven over the handler's arms — hosted conversation answered
with one `TypeContextUsage` correlated by `InReplyTo`; unhosted refused with
`CodeConversationNotFound`; seam comma-ok false and seam nil both refused with
`CodeContextUsageUnavailable` retryable; malformed payload refused as not-found rather than
with a third code; non-interactive conn answered with nothing at all; a poisoned seam double
returning a populated payload with `ok == false` proving the payload is not read on false.
Plus the negotiation tables gaining a `context_usage` row, including "context_usage alone
grants no interactive", and a row proving a client advertising `interactive` without the new
string still reaches the verb (AC-5).

Unit, `cmd/pyry`: the resolver over a fake querier counting its calls and an injected clock
— two concurrent asks produce one call and two identical answers (join); a second ask inside
the window produces no second call (reuse); a second ask past the window produces a second
call (expiry); a refusal collapses the same way; `WaitIdle` is awaited before the query fires,
asserted by a tracker marked busy and a querier that records whether it ran before the turn
closed (AC-4); a nil tracker is not dereferenced; an unbound conversation never reaches the
bootstrap runner. Two more from the security pass: a caller whose own ctx is cancelled mid-flight
leaves the flight running and a live joiner still receives the answer — the finding that moved
the flight off the caller's context; and an unhosted conversation installs no map entry, asserted
on the flight map's length after the refusal rather than on the reply alone.

Guard: `relay_guard_test.go`'s `inboundTypes` gains the request as `switch-intercepted`;
`TypeContextUsage` already sits in `excludedTypes` as `push+reply` and does not move.

e2e, `internal/e2e` (hermetic): one leg on the fake daemon — a paired phone sends
`request_context_usage` for a hosted conversation and receives a `context_usage` frame
correlated by `in_reply_to`, and a second phone naming an unhosted conversation receives the
`conversation.not_found` error frame. The fake claude answers `get_context_usage` at both
detail values from #2289's captured fixture, so this needs no credentials and no live run;
the ticket carries no `needs-real-claude` label and this slice commits no capture.

## Sizing

Re-counted against this plan, per § A4. Production source files created or modified: 11 —
`internal/protocol/{codes,interactive,handshake}.go`, `internal/relay/{v2session,
v2session_seams,v2session_handshake,v2session_contextusage}.go`,
`cmd/pyry/{relay_context_usage,relay,main,streamsup_runner}.go`. Estimated total written
work ~1500 lines. Both exceed the one-ticket boundary.

The parent chain is #2431 → #2293 → #2205, so the split-depth cap applies and no split is
proposed. `needs-human:sizing` is already on the issue with the refiner's recorded
alternative. It would be left whole on its merits anyway: `codes.go`'s own rule mints a
reject code with the handler that emits it, so the declaration cannot be cut from the
handler, and a `cmd/pyry` resolver cut out alone would be a slice whose only consumer is
this ticket's seam — the floor rule's one-consumer case, which the floor wins.

## File-overlap check

`git fetch origin --prune` then a scan of every `origin/feature/<n>` branch against this
plan's file list found one hit: `origin/feature/449` touches `internal/protocol/codes.go`
and `internal/relay/v2session.go`. It is not in-flight work — issue #449 is CLOSED, its
branch tip is dated 2026-05-17, and it has no PR. No blocker set, no rework label.

## Documentation handoff

**Pending — owned by the documentation stage, not by this ticket.** No file under `docs/`
other than this plan is touched here.

- `docs/protocol-mobile.md`, alongside the existing `context_usage` section: document the
  `request_context_usage` request verb, its `context_usage.unavailable` reject code (in
  § Error codes, retryable) and its `context_usage` capability string (in § Capability
  negotiation, detection-only).
- `docs/protocol-mobile.md` § Changelog, the entry dated `2026-09-14`: it currently says of
  this work that "that verb has not landed yet". That sentence is replaced by the landed
  behaviour, including the collapsing of closely-spaced asks and the deferral of an ask that
  arrives mid-turn.

## Open questions

1. **Does a refusal belong in the collapse window?** Resolved in favour of caching both
   outcomes; the reasoning is in § Design and the window is short enough to bound the cost.
   Revisit only if a measured case shows a retryable refusal being held past usefulness.
2. **Should `Get` cap the number of joiners on one flight?** Deferred: joiners are bounded
   by the number of open conns, each of which can hold at most one in-flight request
   (FIFO worker), so the existing per-conn bound already caps it. No new limit.
3. **Does the fake claude's `get_context_usage` arm distinguish `full` from `summary`?**
   To confirm in Phase B against `internal/e2e`'s fake; if it answers both from one fixture
   the e2e leg asserts the frame's arrival and correlation rather than the detail, and the
   `"full"` request value stays pinned by the `cmd/pyry` unit test instead.

## Revisions

**2026-09-14 — the flight runs on its own goroutine, and every caller awaits it.**
§ Design specified the installing caller running the round trip inline and joiners
awaiting it. `TestContextUsageResolver_CallerDepartureLeavesFlightRunning` — written
for the security pass's second MUST FIX — reddened on that shape, and the finding is
the same one generalised: moving the flight off the caller's *context* is not enough
while it still runs on the caller's *goroutine*, because the installer then cannot
leave early even though every joiner can. One client's disconnect would be answered
differently depending on whether it happened to ask first. `Get` now installs the
flight, spawns `fly`, and awaits it exactly as a joiner does. The goroutine's exit is
bounded twice — `WaitIdle` by the daemon context, the query by
`contextUsageQueryTimeout` — so it cannot outlive shutdown or a silent child.

**2026-09-14 — Open Question 3 resolved: the fake claude does not distinguish the
detail values.** `contextUsageRequestID` in the e2e fake accepts both `"summary"` and
`"full"` and answers both from one canned payload. So the e2e leg asserts the reply's
arrival and its correlation rather than the detail, and the `"full"` choice stays
pinned by `TestContextUsageResolver_AsksAtFullDetail` in `cmd/pyry`, exactly as the
question anticipated. No design change.

**2026-09-14 — no handler-side nil-seam check, and one planned test dropped with it.**
§ Design listed a nil `ContextUsageFor` as a handler arm. `handleMCPStatusRequest`, the
handler this one copies, has no such check: the dispatch gate is the only one, which
makes a handler-side copy a second thing to keep in agreement rather than a safety net.
The nil seam is therefore enforced solely at the dispatch arm, where it is also inert
(no decode, no reply), and the planned "nil seam refuses as unavailable" test was
dropped rather than reached through a test-only hook into manager internals — it would
have pinned unreachable code. The inert posture is covered instead, by
`TestV2Session_RequestContextUsage_InertGates`.

## Security review

**Verdict:** PASS (second pass; the first failed with two MUST FIX items, both now addressed
in § Design above)

**Findings:**

- [Trust boundaries] **MUST FIX — fixed before commit.** The first draft had `Get` consult
  and install the collapse map *before* resolving the conversation, keying it on the
  client's string. Any paired client could then mint unbounded map entries by naming
  conversations the daemon does not host, turning a map bounded by hosted conversations
  into one bounded by request volume. § Design now resolves first and keys on the canonical
  id `resolveContextUsageQuerier` returns. The draft's own claim that "a key is created only
  past `resolveBoundRunner`" was not true of the ordering it specified — the kind of
  self-contradiction this pass exists to catch. Note also that keying on the request string
  would have been *incidentally* safe today only because `conversations.Registry.Get`
  matches byte-exactly; borrowing a property from another package is not the same as having
  one.
- [Concurrency] **MUST FIX — fixed before commit.** The first draft ran the shared flight
  under the first caller's context. One client disconnecting mid-flight would cancel a round
  trip other clients were waiting on, settle the shared flight `ok == false`, and — refusals
  being cached — hold that refusal for the whole window: one client's departure denying
  another's answer. § "Whose context owns a flight" moves the flight onto the daemon run
  context, leaves each joiner waiting on its own, and restores the deadline
  `QueryContextUsage`'s contract demands as `contextUsageQueryTimeout` on the round trip
  only — not on the `WaitIdle`, which AC-4 requires to be unbounded.
- [Network & I/O — resource exhaustion] SHOULD FIX, deliberately accepted. Token spend is
  bounded three ways: one request in flight per conn (the FIFO `appFrameWorker`), one round
  trip per conversation per `contextUsageCollapseWindow`, and `appFrameQueueDepth` overflow
  tearing down a conn that floods. The residual is that a request arriving mid-turn parks on
  `WaitIdle` for the turn's full duration and stalls that conn's *later* frames. AC-4
  requires exactly that wait, so it is honoured rather than capped; the cost is recorded
  here, and a bounded hold is the follow-up if a stall is ever observed. An operator is never
  locked out of stopping the turn: `handleInterrupt` runs inline on Run, not on the worker.
- [Error messages, logs, telemetry] No findings. Every reject message is a compile-time
  constant; the conversation id is logged only past the membership gate, where it is
  registry-canonical and so cannot carry log-injection control bytes; the payload is never
  logged at any level on any arm; the `encoding/json` payload-decode error is discarded
  unlogged because it quotes remote-authored bytes. The resolver carries no logger at all —
  `QueryContextUsage`'s doc forbids adding a diagnostic on that path, since the values in
  scope include memory-file paths from the operator's own filesystem.
- [Subprocess / external command execution] No findings, and the reason is structural rather
  than a check: nothing client-authored reaches the claude child's control stream on this
  path. The detail is the daemon-authored constant `"full"`, the request id is minted by
  `nextControlID` under `contextUsageQueryIDPrefix`, and the conversation id is a lookup key
  that selects *which* runner to ask, never a value written into the request.
- [File operations] No findings. No path is constructed, joined, opened, or stat-ed. The
  reading does carry `ContextUsageMemoryFile.Path` values off the operator's filesystem to
  the client, but that is `ContextUsagePayload`'s published contract (#2370) and those same
  strings already reach the same paired clients on the post-turn lane (#2371). This reply is
  strictly narrower: unicast and AEAD-sealed to the conn that asked, never broadcast.
- [Tokens, secrets, credentials] Not applicable. No credential is minted, read, stored, or
  compared on this path, and nothing on the worker touches `s.send`, `s.recv` or key
  material — every reply returns through `forwardToRun` for Run-owned sealing.
- [Cryptographic primitives] No findings. No primitive, key, or nonce is introduced. The one
  crypto-adjacent property in scope is the single-owner send CipherState, and the off-Run
  placement is what makes it a live hazard: emitting from the worker would be a concurrent
  `Encrypt`, i.e. nonce reuse. The handler's file header states this as a constraint on
  future edits, and all three emission routes go through `forwardToRun`.
- [Threat model alignment] No findings. The verb is distinguishable about membership —
  `conversation.not_found` vs `context_usage.unavailable` — and that is deliberate, matching
  `request_snapshot`, `request_history` and `request_model_list`; `codes.go`'s
  `CodeModelListUnavailable` block already records why merging the two buys nothing. Naming a
  conversation is not authorization; authorization is pairing, enforced structurally at the
  Noise_IK handshake. No per-device gate is added, matching the read half `MCPStatusFor` and
  unlike the `MCPActuator` write half: the deciding line is that this verb changes nothing
  about a running child. It does spend operator tokens, which is the one argument for gating
  it — rejected because any paired device can already send a message, which costs
  incomparably more, so a gate here would constrain the cheap path and leave the expensive
  one open.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-14
