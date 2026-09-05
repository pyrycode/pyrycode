# Inbound `request_model_list` (#2125) — `ModelListFor` seam

`request_model_list` is a v2 **control** envelope (phone → binary), intercepted
in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route`, and
answered **inline on the `Run` dispatch goroutine** — the
[`request_session_settings`](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md)
shape, not
[`request_history`](v2-session-manager-state-machine-inbound-request-history-historypager-seam.md)'s
off-`Run` worker handoff, because answering here is a registry lookup, a
bounded deep copy of at most ten model rows and one small marshal — it neither
hashes, reads a file, nor budgets a page against the envelope cap, so it needs
neither an `appFrameJob` kind nor a worker route. It closes the third gap in
[`model_list`](protocol-package-model-list-payload.md)'s delivery: the live
interactive turn lane emits once per child spawn to whoever is connected at
that instant, and
[the connect-time reconcile](v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md)
runs only inside the handshake, so a conversation created **after** the client
connected got neither — its model and effort menus had nothing to wait for
(pyrycode-desktop#1054). This ticket **adds a path and removes none**: the
live lane and `reconcileModelLists` are untouched, proven by omission rather
than by an edited test.

Own file, `internal/relay/v2session_modelrequest.go`, for the reason
`v2session_modelreconcile.go` already states and this one restates with more
force: "modal" and "model" differ by one letter, and a model-list *request*
handler living in a modal- or settings-named file is a readability hazard
worth one file to avoid.

## Order is the design — the security property lives in the sequence, not just the checks

1. **Capability gate first, and fully inert.** `if !s.interactive { return }`
   — no decode, no membership check, no seam call, no reply, so a
   non-interactive conn cannot learn whether the named conversation exists,
   whether a menu is retained, or that this verb is implemented at all.
2. **Decode, tolerated — and the tolerance does not generalise.**
   `_ = json.Unmarshal(env.Payload, &p)` leaves `ConversationID == ""` on any
   decode failure, which reaches only a registry membership check at step 3.
   That is safe here for a reason specific to this verb:
   `handleRequestHistory` cannot tolerate the same failure because there the
   empty id becomes a path component (`filepath.Join(dir, "")` is the log
   **root**, not an error). Reading the tolerant posture as package-wide
   rather than per-verb is exactly how a log-root traversal gets written —
   check what the id becomes downstream before copying the posture, not just
   whether a neighbour uses it.
3. **`KnownConversation` separates the two refusal arms.** It answers only
   *is this conversation ours*; a false is the permanent
   `conversation.not_found`, not logging the requested id (membership
   answering false is precisely the case where it may be an arbitrary client
   string). Membership rather than a session router, for the reason
   `HistoryPager`'s block already gives: a router additionally refuses a
   known conversation with **no bound session**, which is exactly the
   conversation this verb exists to serve.
4. **Resolve via `ModelListFor`, comma-ok honoured.** A nil seam and a false
   comma-ok both land on the same retryable `model_list.unavailable` — see
   the merge below.
5. **Reply.** One `model_list` envelope, `InReplyTo` set, `EventID` **nil**
   (never enters the `#647` replay ring, advances no client cursor — the
   reconcile's own choice), payload forwarded byte for byte: `ConversationID`
   comes out of the resolver's own registry record and `DroppedModels` rides
   through **never recomputed from `len(Models)`**.

**Never an empty `models` array standing in for "unknown."**
`turnevent.ModelList.Models` is documented never-empty and the resolver's
comma-ok is the only spelling of "nothing to send," so a refusal is always a
`TypeError` frame, never a degraded `model_list`. This is where the verb
departs from its dispatch-shape precedent, `request_session_settings`, whose
all-zero reply is a real answer — a shape `model_list` cannot borrow, stated
explicitly so a future reader does not copy the wrong half of that precedent.

## The deliberate merge: one retryable code for two different causes

`KnownConversation == false` → `conversation.not_found` (permanent for the
request as sent). Past that gate, **a nil `ModelListFor` seam and a resolved
`ok == false` are merged into the same retryable `model_list.unavailable`**
rather than given a third code. Both mean the same thing to a client — *the
daemon hosts this conversation and has no menu to give it yet* — and the
client's repair is identical either way: render without one, ask again after
a backoff. Distinguishing them would publish whether the daemon's
model-list source is wired at all, which is a fact about the host's
configuration rather than about the request — a disclosure decision, not an
oversight, mirroring `attachment.not_found`'s "six causes, one code" merge in
[Error codes](protocol-package-constants-codes-go-error-codes-21.md).

## Security / log discipline

The model values (`display_name`, `resolved_model`, `value`, …) reach no log
call at any level, on any arm — the resolver and the reconcile enumerator
both enforce this structurally by carrying no logger at all (#833); this
handler is the **first** model-list path with a logger, so the property is a
discipline here rather than a construction. The requested `conversation_id`
is logged only **past** the membership gate, where it is registry-canonical
and cannot carry the control bytes a log-injection needs; on the membership
arm itself, the id is omitted rather than logged empty. Neither
`json.Unmarshal`'s decode error nor the reply payload is ever echoed —
`encoding/json` quotes offending input into its error string, and the
payload is claude-authored, untrusted text.

## Concurrency

No goroutine is spawned. The handler runs synchronously on the manager's
single `Run` dispatch goroutine and replies through `forwardEnvelope` — the
same seal-under-`s.send` route `handleRequestSessionSettings` uses, **not**
`request_history`'s `forwardToRun`, because this handler never leaves `Run`
in the first place. This distinction is load-bearing rather than stylistic:
the send `CipherState` is single-owner, so sealing from any goroutine but
`Run` would be a concurrent `Encrypt` — a nonce reuse, a real break rather
than a race annoyance. **A future edit that moves this arm to
`enqueueAppFrame`** (as `request_history` did, for work this verb does not
do) **must switch the emit to `forwardToRun` in the same change** — named in
the handler file's header as a constraint on future edits, not a fact that
happens to be true today.

Below the seam, `resolveBoundModelList`'s locks are acquired sequentially and
never nested, so this path adds no edge to the daemon's lock order — it
moves an existing lock chain onto `Run`, where
[the connect-time reconcile](v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md)
already runs it once per handshake; this verb runs it once per request. A
conn's frames dispatch in order on one goroutine, so a client cannot overlap
two of its own requests.

## The seam

**`ModelListFor func(conversationID string) (protocol.ModelListPayload, bool)`**
on `V2SessionConfig`, conversation-keyed rather than enumerate-all — the
`RunConfigFor` shape, not `RetainedModelLists`'s, because the request carries
an id and `RetainedModelLists` only enumerates *because* a `V2Session` does
not. Comma-ok, and the doc comment states the same contract `RunConfigFor`'s
does: a caller MUST NOT read the payload when `ok` is false — pinned by a
**poisoned** refusal double (`poisonedModelList`, non-zero `DroppedModels`,
populated `Models`, `ok == false`) so "fail closed on `ok == false`" is a
tested property of `internal/relay` itself, not one borrowed from the
producer's good behaviour.

The seam decides **nothing** about which vocabulary answers — that stays
inside `cmd/pyry`'s `resolveBoundModelList`
(see [Connect-time model-list reconcile](v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md)
for the daemon-wide fallback it applies since #2124), and `KnownConversation`
answers only membership. Neither is a second opinion on the other's question.

`cmd/pyry/relay.go`'s wiring struct gains `modelListFor` and assigns it
straight through (`ModelListFor: w.modelListFor`, never wrapped — a wrapper
would be non-nil even when the field is nil and would silently defeat the
seam's nil ⇒ refuse contract). The adapter itself,
`modelListFor(convReg, pool)`, lives in `cmd/pyry/session_model_list.go`
beside its twin `retainedModelLists` — **a named function rather than the
planned inline closure at the composition root**, and the reason is worth
generalising past this ticket: `cmd/pyry/main.go` does not import
`internal/protocol`, so an inline `func(string) (protocol.ModelListPayload, bool)`
closure there would not compile without adding that import for one type
annotation. Every neighbouring seam of this shape (`retainedModelLists`,
`runSettings`) is a named adapter in a file that already has the import —
that is not a style choice, it is what the import graph permits. **Check the
import graph before sizing a wiring line as "one line."** `resolveBoundModelList`
now has its first second caller through this adapter (`retainedModelLists`
was its first, from #1867); `modelListFor` adds no filter, no second lookup
and no re-derivation of which vocabulary answers.

A structural guard, `TestModelListForWiredToTheCompositionRoot`
(`cmd/pyry/relay_guard_test.go`), pins that the `V2SessionConfig` literal's
`ModelListFor` element is bound to `w.modelListFor` — deterministic code as
the safety net for a wiring line neither `startRelayV2` nor
`TestModelListFor_ForwardsTheResolversAnswer` (which exercises the adapter
directly) can catch on its own; deleting the assignment line ships a daemon
that answers `model_list.unavailable` to every request, silently. It reuses
`configSeamSelector`, generalised from #1980's question-reconcile guard: the
helper's diagnostic message used to be hardcoded to the question seam, so a
second caller inherited a `Fatal` naming the wrong seam until `configSeamSelector`
took a `missing`-consequence string as a parameter — a two-line
generalisation, not a near-duplicate helper, which is the shape worth
copying the next time a `parseGoFile`-family guard gets a second caller.

## Test design

`internal/relay/v2session_modelrequest_test.go`, table-driven, drives real
frames through the manager's Frames/Run loop so `dispatch.Route` — not a unit
call — is what has to answer `protocol.unknown_type` before the handler
exists (RED first). Two things worth carrying forward:

- **A `json.RawMessage` payload cannot be sent as truncated/invalid JSON to
  exercise a decode failure inside the handler.** Building the test envelope
  with a body like `{"conversation_id":` fails in `json.Marshal` of the
  *envelope* during test setup, not inside `handleRequestModelList` — a
  `json.RawMessage` field must itself be valid JSON to marshal. The shape
  that actually reaches the handler's `json.Unmarshal` and fails there is
  valid JSON of the *wrong type* (`{"conversation_id":123}`) or a bare
  non-object literal (`"c1"`); `TestV2Session_RequestModelList_MalformedPayloadReachesNoResolver`
  uses the former and asserts `KnownConversation` was consulted once (on the
  now-empty id) and `ModelListFor` was consulted zero times.
- **Seam-consultation counters, not just reply shape, are what make the
  non-interactive row discriminating.** A counter on `KnownConversation` and
  `ModelListFor` pins that the non-interactive arm consults **nothing** —
  without it, "no `model_list` frame" is indistinguishable from "a seam ran
  and produced nothing."

## Related

- [Inbound `request_history` (#2116)](v2-session-manager-state-machine-inbound-request-history-historypager-seam.md) — the reject-envelope shape this ticket copies (`TypeError`, `in_reply_to`, one code constant, one merge-vs-split error-code decision), and the off-`Run` worker handoff this verb deliberately does not need.
- [Inbound `request_session_settings`](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md) — the dispatch/capability shape this ticket copies (inline on `Run`, `!s.interactive` first, tolerated decode, `forwardEnvelope`), and the all-zero-reply posture this ticket deliberately does **not** copy.
- [Connect-time model-list reconcile (#1863)](v2-session-manager-state-machine-connect-time-model-list-reconcile-retain.md) — `RetainedModelLists`, the enumerate-all sibling seam; `resolveBoundModelList` and its #2124 daemon-wide fallback, which this seam reaches through the same resolver, unforked.
- [Model-list payload](protocol-package-model-list-payload.md) — `ModelListPayload` / `ModelOption`, unchanged by this ticket, and `RequestModelListPayload`, the new one-field request shape.
- [Error codes](protocol-package-constants-codes-go-error-codes-21.md) — `model_list.unavailable`, minted here.
- [Envelope types § v2 request-model-list vocabulary](protocol-package-constants-codes-go-envelope-types.md) — `TypeRequestModelList`'s registry filing.
