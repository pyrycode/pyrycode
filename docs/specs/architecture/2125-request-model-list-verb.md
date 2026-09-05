# #2125 — `request_model_list`: let a client ask for a conversation's model list at any time

Split from #2084. Blocked by #2124, which merged 2026-09-05 (PR #2128) and is on `main` at
this branch's base.

## Files read

Production surface the design rests on, with what each contributed:

- `cmd/pyry/session_model_list.go` → `resolveBoundModelList` — the daemon-side answer, unchanged
  by this ticket. Its block fixes three rules this plan inherits rather than restates: the
  registry lookup is the whole security boundary, the comma-ok is the only spelling of "nothing
  to send" (no empty `models` may stand in for "unknown"), and the resolver takes **no logger and
  must not grow one**. Also `retainedModelVocabulary` (the #2124 daemon-wide fallback — why a
  conversation with no session now answers) and `retainedModelLists` (the connect-time
  enumerator, and why it is the wrong seam shape here).
- `internal/relay/v2session_history_request.go` → `handleRequestHistory`, `rejectHistoryRequest`,
  `historyReplyError` — the **reject-path shape** this ticket copies: an ordered gate list, one
  `attachmentReject` value per code so a code cannot be paired with the wrong `retryable`, a
  `TypeError` envelope correlated by `in_reply_to`, and the per-field never-log rule (an id is
  loggable only *after* membership has established it is registry-canonical).
- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings` — the **dispatch and
  capability shape**: `!s.interactive` first and fully inert, a tolerated payload decode, the
  comma-ok honoured rather than discarded, and `forwardEnvelope` as the single reply route for a
  handler running inline on Run. Deliberately *not* its refusal posture (see Design).
- `internal/relay/v2session_modelreconcile.go` → `reconcileModelLists` — what the connect-time
  answer looks like on the wire, which is what AC #1's "same payload" is measured against:
  `TypeModelList`, envelope `ID: 1`, `EventID` nil. Unchanged by this ticket.
- `internal/relay/v2session.go` → `dispatchAppFrame` — the interception switch. The three arms
  that hand off to `enqueueAppFrame` and why (`attachment_chunk`, `request_attachment`,
  `request_history` hash, read files, or budget a page); this verb does none of that.
- `internal/relay/v2session_seams.go` → `RunConfigFor` (the conversation-keyed seam shape to
  copy, including its comma-ok contract and its untrusted-id block), `KnownConversation` (the
  membership gate, and its own note on why a pure membership check is right for a verb that must
  serve an *unbound* conversation), `RetainedModelLists` (enumerate-all, and its stated reason —
  a `V2Session` carries no conversation id).
- `internal/protocol/codes.go` → the `Code*` error block (history group's retryability split) and
  the `TypeRequestHistory` / `TypeRequestAttachment` declaration blocks — the naming rule, the
  "must not be added to `inboundAppTypeSet`" rule, and the registry-filing obligation.
- `internal/protocol/history.go` → `RequestHistoryPayload` — the one-field request payload's
  block: no `omitempty`, correlation rides `InReplyTo` so there is no request-id key, the id is a
  lookup key and naming a conversation is not authorization.
- `internal/protocol/settings.go` → `RequestSessionSettingsPayload` — the closest single-field
  precedent, and the reason it has no `omitempty`.
- `internal/protocol/interactive.go` → `ModelListPayload` — the reply payload, unchanged.
- `cmd/pyry/relay.go` → the `V2SessionConfig` literal, the `KnownConversation` closure, and the
  wiring struct's `runSettings` / `retainedModelLists` fields (where a new conversation-keyed
  seam field belongs and why `relay.go` cannot build it itself — it holds no pool).
- `cmd/pyry/main.go` → the wiring-struct literal where `runSettings` and `retainedModelLists` are
  assigned; the composition root that does hold `convReg` and `pool`.
- `internal/relay/v2session_settings_read_test.go` → `readManagerFor`, `readSeams`,
  `poisonedRunConfig` — the harness to mirror, and specifically the **poisoned refusal return**
  trick that makes "fail closed on `ok == false`" a property of `internal/relay` rather than one
  borrowed from `cmd/pyry`.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`,
  `TestEveryInboundV2TypeHasHandler`, and the `parseGoFile` family — the two registries this
  ticket must amend, and the AST machinery a wiring guard would reuse.
- `internal/protocol/compat_test.go` → `v2OnlyTypes` and the partition `all` slice.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — three lessons that change what
  this ticket builds: **no `compat_test.go` registry can catch a wire-string typo, only a
  committed fixture can**; a `WireKeys` key-set pin outlives a fixture regeneration that a round
  trip does not; and **`go test -overlay` cannot mutation-test any guard in the `parseGoFile`
  family** — those read the file on disk at run time, so a mutant must be written over the real
  file and restored.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md`
  and `…-inbound-request-history-historypager-seam.md` — the two package-overview sections for
  the precedents above.
- `docs/protocol-mobile.md` → § Application message types (the `request_history` /
  `request_session_settings` rows), § `model_list` (the sentence AC #5 requires **replaced**),
  § Error codes (the `history.*` rows' shape), § Security model.

## Context

`model_list` reaches a client two ways and a conversation created **after** the client connected
gets neither: the live turn lane drops every event whose producing session is not the active
conversation's, and `reconcileModelLists` runs only inside `handleNoiseInit`, so a conversation
that did not exist at handshake time is not in it. There is no request verb, so there is no third
option. pyrycode-desktop#1054 sees this as a blank model/effort composer on a new chat; #2085
makes it universal by deferring the spawn to the first message.

The daemon-side answer already exists: `resolveBoundModelList`, which since #2124 answers for a
conversation with no session too, from the daemon-wide vocabulary. This ticket adds the wire verb,
the interception and the seam that reaches that resolver. **It adds a path and removes none** —
the live lane and `reconcileModelLists` are untouched (AC #4).

**No ADR is warranted.** Every decision here is an application of a precedent already recorded:
the declare-then-serve sequencing (#2052→#2054, #2113→#2116), the conversation-keyed seam shape
(`RunConfigFor`), and the reject-envelope shape (`request_history`). ADR 037 already covers
capability strings. The one genuinely new judgement — merging "no seam wired" into
`model_list.unavailable` rather than minting a third code — is stated in Design and published in
§ Error codes, which is where a client author looks.

## Size — two boundaries exceeded, deliberately, and my count differs from the refiner's

| Boundary | Limit | This ticket |
|---|---|---|
| Production source files | ≤ 5 | **7** |
| Total written work | ≤ 800 | **~900** |
| New exported types/interfaces | ≤ 5 | 1 (`RequestModelListPayload`) |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — every change is additive |
| Acceptance criteria | ≤ 5 | 5 |
| Distinct reject branches | ≤ 10 | 2 |

The seven production files are `internal/protocol/codes.go`, `internal/protocol/interactive.go`,
`internal/relay/v2session.go`, `internal/relay/v2session_modelrequest.go` (new),
`internal/relay/v2session_seams.go`, `cmd/pyry/relay.go` and **`cmd/pyry/main.go`**. The refiner's
estimate named six and missed `main.go`: `relay.go` deliberately imports neither
`internal/sessions` nor holds a pool reference (its `runSettings` / `retainedModelLists` field
blocks state this), so the closure over `convReg` and `pool` can only be built at the composition
root, exactly as `runSettings` is.

**The floor rule decides this, and it beats the ceiling.** The only clean cut is the one this repo
has made four times — declaration first, handler second — and the declaration slice here is one
type constant, one error code, a one-field payload, a fixture and three registry rows whose
**only** daemon-side consumer is the handler in this same slice. A slice consumed by exactly one
sibling in its own family is part of that sibling. Splitting also buys a second refiner pass and a
second builder leg (~$15 by the 2026-09-02 measurements) to insure against a budget miss that now
costs one continuation leg, and it would make each child a grandchild of #2084, one step from the
split-depth gate. The parent chain is `parent 2084, grandparent none`, so a split is structurally
permitted and is being declined on the floor rule, not blocked by the gate.

**Edit fan-out is zero.** Nothing is renamed, no signature changes, no exported symbol is removed.
`ModelListFor` is a new optional seam field, so every existing `V2SessionConfig` literal in
production and tests compiles unchanged and behaves unchanged (nil ⇒ refuse).

**File-overlap check (§ A2):** `git fetch origin --prune` then a scan of every
`origin/feature/<n>` branch against this ticket's file list found one hit — `origin/feature/449`,
last commit 2026-05-17, on an issue **closed** 2026-05-17 with no PR of any state. That is an
abandoned leftover branch, not in-flight work; it will never merge and cannot conflict. No live
overlap. No blocker set.

## Design

### Wire vocabulary (`internal/protocol`)

- **`TypeRequestModelList = "request_model_list"`** in `codes.go`, its own doc block appended
  after the history block, following #2113's placement. The name joins the five inbound *ask the
  daemon for X* verbs already on this wire. **Must not** be added to `inboundAppTypeSet`: it is a
  v2 control envelope intercepted before `dispatch.Route`, and `IsKnownAppType` rejecting it is
  the structural bar against a v1 client pushing one into the v1 handler chain.
- **`CodeModelListUnavailable = "model_list.unavailable"`** appended to the error-code block after
  the history group, minted **with the handler that sends it** — #2052's rule, and the reason no
  reject vocabulary appears in a declaration-only ticket.
- **`RequestModelListPayload`** in `interactive.go` beside `ModelListPayload`: one field,
  `ConversationID string \`json:"conversation_id"\``, no `omitempty` (absent and empty are the
  same case, and keeping the key on the wire lets a fixture pin the full shape), no `MarshalJSON`.
  Correlation rides `Envelope.InReplyTo`, so there is **no request-id key** —
  `TypeAttachmentStored`'s decision, transferred.
- **Fixture** `internal/protocol/testdata/request_model_list.json`, one envelope. It is the only
  thing in this ticket that can catch a wire-*string* typo: all three `compat_test.go` registries
  key on the Go symbol and would move consistently through a renamed value.

`TypeModelList` and `ModelListPayload` are **unchanged**. No second outbound shape is minted and
the `turnbridge.MapEvent` mapping is not forked — AC #1's "same payload" means *same source*.

### Registries — all four amended in the same commit as the constants

1. `internal/protocol/compat_test.go`: `TypeRequestModelList` joins the partition `all` slice and
   `v2OnlyTypes`; the size assertion moves by one.
2. `cmd/pyry/relay_guard_test.go`: `inboundTypes["TypeRequestModelList"] = "switch-intercepted"`.
   It never sits in `excludedTypes` as "pending handler", because the handler lands here — filing
   it there instead would fail Assertion #2 the moment the switch case exists.
   `TypeModelList` **stays** in `excludedTypes` as an outbound push, exactly as `TypeHistoryPage`
   does for `request_history`.
3. `TestErrorCode_Constants_MatchSpec` gains a `model_list.unavailable` row.
4. `inboundAppTypeSet` does **not** move, and neither does its asserted count.

### The seam (`internal/relay/v2session_seams.go`)

One new optional field on `V2SessionConfig`, placed immediately after `RunConfigFor` — the struct
is organised by role, and this is the inbound-request cluster:

```go
ModelListFor func(conversationID string) (protocol.ModelListPayload, bool)
```

Conversation-keyed, not enumerate-all: the request carries an id, so `RetainedModelLists`' forced
enumeration (a `V2Session` carries no conversation id) has no reason to exist here.
`protocol.ModelListPayload` crosses the boundary already-marshal-ready, so `internal/relay` gains
no import. Comma-ok, and the doc block states the same contract `RunConfigFor`'s does: **a caller
MUST NOT read the payload when `ok` is false**. Nil ⇒ the verb refuses (foreground / v1 /
unwired).

The block also states the rule that keeps this seam from becoming a second opinion: it decides
nothing about **which** vocabulary answers. That decision lives inside `resolveBoundModelList`,
whose block forbids a caller forking it, and `KnownConversation` answers only *is this
conversation ours*.

### The handler (`internal/relay/v2session_modelrequest.go`, new)

`handleRequestModelList(ctx, s *V2Session, env protocol.Envelope)` — takes the already-probed
`Envelope` because it runs **inline on the Run goroutine**, like `handleRequestSessionSettings`,
not the plaintext the off-Run workers receive. Answering is a registry lookup, a map read behind
one leaf mutex and one small marshal; it neither hashes, reads a file, nor budgets against the
envelope cap, so it needs neither an `appFrameJob` kind nor a worker route.

**Order is the design, and the ordering carries the security property:**

1. **`!s.interactive` ⇒ return.** Fully inert: no decode, no seam call, no reply, so the conn
   cannot learn whether the named conversation exists (AC #3). It must stay ahead of both gates
   below.
2. **Decode, tolerated.** `_ = json.Unmarshal(env.Payload, &p)` — `handleRequestSessionSettings`'
   posture, not `handleRequestHistory`'s reject. The tolerance is safe *here* for a reason that
   does not generalise: a decode failure leaves `ConversationID == ""`, which reaches only a
   registry membership check, never a path join (`filepath.Join(dir, "")` is `dir`, which is why
   the history verb cannot tolerate it). No conversation carries an empty id — every one is minted
   by `conversations.NewID` — so the empty case lands on step 3's refusal without a third code.
   **Never echo or log the error**: `encoding/json` quotes remote-authored bytes into it.
3. **Membership.** `m.cfg.KnownConversation == nil || !m.cfg.KnownConversation(p.ConversationID)`
   ⇒ `conversation.not_found`, **not retryable**. A nil seam refuses everything, the only
   fail-safe reading. The requested id is **not logged** on this arm: nothing has shape-validated
   it, and membership answering false is precisely the case where it may be an arbitrary client
   string.
   Membership rather than a session router, for the reason `HistoryPage`'s block already gives: a
   router additionally refuses a known conversation with **no bound session**, which is exactly
   the conversation this verb exists to serve.
4. **Resolve.** `m.cfg.ModelListFor == nil`, or a comma-ok of false, ⇒ `model_list.unavailable`,
   **retryable**. The comma-ok is honoured, not discarded — the payload variable is assigned only
   on `true`.
5. **Reply.** One `TypeModelList` envelope: `ID: 1` (non-load-bearing), `InReplyTo: &env.ID`,
   `EventID` **nil**, payload = what the seam returned, byte for byte. Emitted through
   `forwardEnvelope`, the same seal-and-forward route the success and reject paths of every
   inline-on-Run handler share.

**The two refusal arms, and the one merge.** `KnownConversation` is what separates them: it
answers *is this conversation ours*, so a false there is `conversation.not_found` and a seam
refusal past it is `model_list.unavailable`. **A nil seam is merged into the retryable arm rather
than given a third code**, and that is a decision rather than an oversight. Both mean the same
thing to a client — *the daemon hosts this conversation and has no menu to give you* — and the
client's repair is identical: render a usable UI without one and ask again later. Distinguishing
them would publish whether the daemon's model-list source is wired, which is a fact about the
host's configuration and not about the request. The retryability follows the dominant cause: the
bootstrap child has not yet answered its `initialize` ask, or was constructed evicted
(`sessions.Config.BootstrapEvicted`, which `pyry acp` sets). Both clear without the client
changing anything.

**Never an empty `models` array standing in for "unknown"** (AC #2). `turnevent.ModelList.Models`
is documented never-empty and `resolveBoundModelList`'s block forbids a partially-filled payload;
the comma-ok is the only spelling of "nothing to send", and this handler turns it into an `error`
frame rather than a degraded `model_list`. This is where the verb departs from
`request_session_settings`, whose all-zero reply is a real answer — a shape `model_list` cannot
borrow.

Two `attachmentReject` values (the existing struct, reused as `request_history` reuses it, so a
code cannot be paired with the wrong `retryable` flag) and a `modelListReplyError` helper of its
own — the package's stated posture is that each reply-owing handler owns its error helper, and
sharing one would misfile every log event under another verb's name.

### Wiring

- `cmd/pyry/relay.go`: the wiring struct gains
  `modelListFor func(convID string) (protocol.ModelListPayload, bool)`, and the `V2SessionConfig`
  literal gains `ModelListFor: w.modelListFor` — assigned straight through, never wrapped, for
  `RetainedModelLists`' stated reason: a wrapper is non-nil even when the field is nil and would
  silently defeat the seam's nil ⇒ refuse contract.
- `cmd/pyry/main.go`: an inline closure over `convReg` and `pool` delegating to
  `resolveBoundModelList`, mirroring `runSettings` exactly. No new adapter function: the closure
  has no logic of its own, and `resolveBoundModelList` already carries eight tests including the
  #2124 fallback and the nothing-retained-anywhere refusal.

## Concurrency model

No goroutine is spawned, so there is nothing to leak and no shutdown path to add. The handler runs
synchronously on the manager's **single Run dispatch goroutine**, which is what makes
`s.interactive` and `s.connID` readable lock-free under the package's single-owner invariant and
what makes `forwardEnvelope` the correct (and only) reply route — sealing from any other goroutine
would be a concurrent `Encrypt` on the single-owner send `CipherState`, a nonce reuse.

Below the seam, `resolveBoundModelList` is a synchronous read whose locks — the registry's,
`Pool.Lookup`'s RLock, the bound hold's leaf mutex, and since #2124 `Pool.Default`'s RLock and the
bootstrap hold's leaf mutex — are acquired **sequentially and never nested**. This path therefore
adds no edge to the daemon's lock order. It does move those acquisitions onto the Run goroutine,
which is new: the reconcile already runs them there once per handshake, and this verb runs them
once per request. The work is a linear registry scan plus a deep copy of at most ten model rows —
microseconds — and no lock is held across a channel send. A request naming a conversation that
rotates concurrently gets either the menu bound a moment ago or the daemon-wide copy, and both are
correct because the vocabulary is machine- and account-scoped.

Serialisation: a conn's frames are dispatched in order on one goroutine, so a client cannot
overlap two of these with itself; a burst is answered one at a time. No per-verb concurrency limit
is minted, matching `request_history`.

## Error handling

| Condition | Answer | Retryable | Logged |
|---|---|---|---|
| Conn did not negotiate `interactive` | nothing at all | — | nothing |
| Payload did not decode (empty id) | `conversation.not_found` | no | code + reason; **no id, no decode error** |
| `KnownConversation` nil, or false | `conversation.not_found` | no | code + reason; **no id** |
| `ModelListFor` nil | `model_list.unavailable` | yes | code + reason + id (post-gate, canonical) |
| Seam comma-ok false | `model_list.unavailable` | yes | code + reason + id |
| Reply payload marshal fails | nothing (defensive; unreachable) | — | `conn_id` only, **never** the payload or `err` |
| `forwardEnvelope` fails | dropped | — | Debug, the package's outbound-drop posture |

Messages are **static daemon-authored constants**. No value derived from an error, an id, or a
model string ever reaches the wire. The `reason` field distinguishes the arms for an operator
while the wire answer stays exactly as coarse as it must be — `rejectHistoryRequest`'s bargain.

The marshal branch is defensive and no test can redden it: `ModelListPayload` is a closed struct
whose two custom `MarshalJSON`s delegate to `json.Marshal` over closed types. It is written
anyway, without echoing `err`, because `encoding/json` would quote model values into the record.

## Testing strategy

**RED first.** The `internal/relay` table drives real frames through the manager's Frames/Run
loop; before the handler exists every row fails because `dispatch.Route` answers
`protocol.unknown_type` instead.

`internal/relay/v2session_modelrequest_test.go` — a `readManagerFor`-shaped harness, table-driven:

- interactive + hosted + resolvable ⇒ exactly one frame, `type == "model_list"`, `in_reply_to ==`
  the request's envelope id, `event_id` **absent**, payload equal to the fixture the double
  returned field-for-field (including `dropped_models`, which must ride through and never be
  recomputed from `len(models)`).
- the **conversation with no bound session** (AC #1's named case): the double answers it, which is
  what the production resolver does since #2124 — asserted as a distinct row so the case is
  visible in this package rather than only in `cmd/pyry`.
- non-interactive ⇒ **zero outbound frames** (AC #3), asserted against the recorder, not merely
  "no `model_list`".
- unhosted conversation ⇒ one `error`, `conversation.not_found`, `retryable:false`.
- hosted + seam refuses ⇒ one `error`, `model_list.unavailable`, `retryable:true`.
- hosted + `ModelListFor` nil ⇒ the same retryable error.
- `KnownConversation` nil ⇒ `conversation.not_found`.
- malformed / absent payload ⇒ `conversation.not_found`, and the seam is **never consulted**
  (a counter, not just the reply shape).
- **Poisoned refusal**: the double returns a fully-populated `ModelListPayload` alongside
  `ok == false`, so a handler that discarded the comma-ok emits a menu and reddens. Without this,
  every refusal row passes against a zero-valued double and proves nothing —
  `poisonedRunConfig`'s lesson.
- A seam-consultation counter (atomic — the closure runs on Run while assertions run on the test
  goroutine) pins that the non-interactive row consults **nothing**.

`internal/protocol`:

- `TestRequestModelListPayload_RoundTrip` against the committed fixture — the only check on the
  wire *string*.
- `TestRequestModelListPayload_WireKeys` — the key set is exactly `{conversation_id}`, marshalling
  a populated struct rather than reading the fixture, so the pin survives a later fixture
  regeneration.
- The registry edits are themselves the drift assertions.

`cmd/pyry`:

- A structural guard in `relay_guard_test.go` pinning that the `V2SessionConfig` literal's
  `ModelListFor` element is bound to the `w.modelListFor` selector — the #1980 shape, reusing the
  `parseGoFile` family. `startRelayV2` has no test and cannot cheaply get one, so without this a
  deleted assignment ships a daemon that answers `model_list.unavailable` to every request,
  silently, caught by nothing. Deterministic code as the safety net for a wiring line, not a
  second stochastic reviewer.
- `TestEveryInboundV2TypeHasHandler` covers the switch case for free once the registry row lands.

**Mutation-testing note:** any assertion inside the `parseGoFile` family must be mutation-tested by
writing the mutant over the real file and restoring it in the same shell invocation. `go test
-overlay` cannot reach these guards — they `parser.ParseFile` the on-disk file at run time, so a
mutant under an overlay is invisible to them while `go build` sees it, and the guard reports a
false green.

**Not built:** a dedicated `internal/e2e` walk. #1610, the nearest shape analogue, added none and
extended an existing suite instead; the handler's unit tests and the wiring guard are the proof
this ticket owes. AC #4 is discharged by the existing `reconcileModelLists` and turn-lane tests
passing **unedited** — that they need no edit is the assertion.

**Gate:** `go test -race ./internal/protocol/... ./internal/relay/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`. The full-module race suite is the verifier's.

## Documentation (`docs/protocol-mobile.md`)

- **§ Application message types** — a `request_model_list` row after `request_history`: phone →
  binary, not in v1, the `conversation_id` is a lookup key validated against the daemon's registry
  and naming a conversation is not authorization, correlation rides `in_reply_to` so there is no
  request-id key, the answer is one `model_list` or one of two coded rejects, interactive-gated.
- **§ `model_list`** — the sentence *"and there is still **no way to ask for one** on demand
  between connects"* is **replaced, not reworded** (AC #5): the clause it exists to state is now
  false. A new **Ask for it on demand** paragraph follows the *Reconcile on (re)connect* one and
  publishes the verb, its single-field payload, the reply (the same frame, correlated by
  `in_reply_to`, carrying **no** `event_id` so it never enters the replay ring and advances no
  cursor), the two rejects, and the guarantee AC #2 names: **never an empty `models` array
  standing in for "unknown"** — absence and an `error` are the only two "no list" answers.
  The *"must not block its model menu on the live frame"* half of that sentence stays true and
  stays.
- **§ Error codes** — a `model_list.unavailable` row: retryable after a backoff, what it means,
  why it is merged across "nothing retained" and "no source wired", and that a static message
  names no model, no conversation and no path.
- **Changelog** — one dated entry naming what landed, and closing the loop on #2124's entry, which
  deliberately left the sentence standing as this ticket's to replace.

## Open questions

1. **Does `TestErrorCode_Constants_MatchSpec` live in `codes_test.go` or elsewhere in the
   package?** Resolve by locating it in Phase B; the row lands wherever it is.
2. **Is there a reusable "field bound to selector" extractor in `relay_guard_test.go`, or must the
   wiring guard write one?** #1980's extractors are bespoke to `OutstandingQuestions`' three-fact
   shape. If nothing generalises cheaply, write a compact single-fact extractor rather than
   generalising #1980's — this guard pins one fact, not three.
3. **Placement of the `TypeRequestModelList` block within `codes.go`.** Appended after the history
   family, matching #2113's own placement; confirm against the file's tail in Phase B and follow
   whatever the last landed block did.

Each is a location question, not a design question; none can change the contract. Any that changes
the design gets a `## Revisions` entry in the same commit as the code that departs.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary is explicit and singular: `handleRequestModelList`'s step 3,
  `KnownConversation`. Above it `p.ConversationID` is an arbitrary client string; below it, a
  registry-canonical id. Two properties were probed rather than assumed. **The seam does not trust
  the gate** — `resolveBoundModelList` performs its own hard `conversations.Registry.Get` and
  refuses independently, so both checks fail closed on their own and neither is load-bearing for
  the other's correctness. And **the client's spelling never reaches the reply**:
  `resolveBoundModelList` stamps `conv.ID` from the resolved record, so nothing a caller sent is
  reflected back. The consequence of the double lookup is a benign TOCTOU: a conversation deleted
  between the two arms is answered `model_list.unavailable` instead of `conversation.not_found` —
  a misclassification of a refusal, never a disclosure, and both spellings are `Registry.Get`'s
  byte-exact compare, so no non-canonical id can pass one and resolve a different record in the
  other.

- **[Trust boundaries — the empty id]** A decode failure yields `ConversationID == ""`, which is
  tolerated rather than rejected. No second guard is added, deliberately:
  `handlers.CreateConversation` mints every id from `conversations.NewID`, so no record carries an
  empty id and membership answers false. `resolveBoundModelList`'s block already declines a
  `convID == ""` pre-check for this reason — a second spelling of a check in one of six twins
  closes nothing. **The reason this tolerance does not generalise is stated in the Design**: in
  `handleRequestHistory` the empty id becomes a path component, where `filepath.Join(dir, "")` is
  the log root.

- **[Tokens, secrets, credentials]** Not applicable, with the reason rather than a shrug: this
  path mints, stores, compares and rotates nothing. Sending the verb is **not a capability** and
  neither is receiving an answer — authorization is pairing, enforced structurally at the
  Noise_IK handshake, and naming a conversation is not authorization. The one gate the frame is
  subject to, `interactive`, is negotiated at handshake and is not carried in the frame, so
  nothing in the payload can influence it.

- **[File operations]** No finding, and the category is genuinely empty by construction: this path
  builds no path, opens no file, creates nothing and stats nothing. That absence is what licenses
  the tolerated decode above, and it is the single difference from `request_history` that changes
  a design decision.

- **[Subprocess / external command execution]** No finding. Nothing is executed; no client value
  reaches an argv, an environment or a signal.

- **[Cryptographic primitives]** No primitive is chosen or minted here, but one crypto property is
  load-bearing and is a constraint on future edits rather than an observation. The reply is
  emitted through `forwardEnvelope` and sealed by Run under the **single-owner send
  `CipherState`**. Emitting from any other goroutine is a concurrent `Encrypt` — a **nonce reuse**,
  a real break rather than a race annoyance. So a later edit that moves this arm to
  `enqueueAppFrame` (as `request_history` did, for work this verb does not do) **must** switch the
  emit to `forwardToRun` in the same change. Named in the handler's file header.

- **[Network & I/O — inbound]** No finding. The frame is bounded by the Noise transport (65535
  bytes including the AEAD tag) before `dispatchAppFrame` sees it, and the payload carries **no
  count and no length field**, so the *NEVER ALLOCATE FROM A CLAIM* rule has no claim to apply to
  — there is nothing here a hostile value could size.

- **[Network & I/O — outbound]** No finding, but the inherited bound is restated rather than
  assumed, because this is the first `model_list` path a **client** can trigger on demand. The
  payload arrives already bounded at construction — the producer caps entries at ten
  (`DroppedModels` reports the overflow) and truncates each row's fields (`TruncatedFields`) — so
  the reply cannot approach the application-envelope cap and needs no budgeting loop. **No second
  cap is added here**: `RetainedModelLists`' block states why a second place deciding the limit is
  worse than one, and the obligation stays the producer's.

- **[Network & I/O — resource exhaustion]** SHOULD FIX, stated as a bound on the implementation
  rather than a mechanism. A paired but hostile client can spin this verb, and each request costs
  a linear `Registry.Get` scan, a bounded deep copy and one marshal **on the Run dispatch
  goroutine, which is shared across every conn of the manager** — the cross-conn head-of-line
  surface #965 removed and #1491 / #2116 moved heavier work off. Three things make inline correct
  here and one of them is a constraint to honour in Phase B: the work is strictly *cheaper* than
  the already-shipped inline `handleRequestSessionSettings`, which reads a transcript file on the
  same goroutine; a conn's frames are dispatched serially, so a client cannot overlap its own
  requests; and **the handler must stay O(one registry scan + one bounded copy)** — if a future
  edit makes it read a file, enumerate the registry, or call anything unbounded, it moves off Run.
  No per-verb rate limit is minted, matching every inline arm; any bound stays
  receiver-configured and unpublished, the posture § Conversation history publishes.

- **[Error messages, logs, telemetry]** SHOULD FIX — the concrete one, and the reason this
  category is not "no findings". `resolveBoundModelList` and `retainedModelLists` enforce the #833
  posture **by construction**: neither has a logger field, so neither can leak a model value.
  **This handler is the first model-list path that carries a logger** (`m.cfg.Logger`), so the
  property stops being structural and becomes a discipline. Phase B must hold three lines, each
  checkable against the Error handling table: the resolved `payload` variable appears in **no log
  call at any level**; the `json.Unmarshal` error is never echoed (`encoding/json` quotes
  remote-authored bytes into it); and the requested `conversation_id` is logged **only past the
  membership gate**, where it is registry-canonical and so cannot carry the control bytes a
  log-injection needs — which is itself the reason the gate must stay ahead of the log rather than
  merely ahead of the resolve.

- **[Concurrency]** No finding beyond what the Concurrency model section states. No goroutine is
  spawned, so nothing can leak and no shutdown path is added. The five locks below the seam are
  acquired sequentially and never nested, so no edge joins the daemon's lock order; the handler
  moves those acquisitions onto Run, where the connect-time reconcile already runs them. No lock
  is held across a channel send. The check-then-use window is the benign one under Trust
  boundaries above.

- **[Threat model alignment]** § Security model threats 1–3 are each addressed rather than waved
  at. **Threat 1 (prompt injection):** the reply carries claude-authored text (`display_name`,
  `resolved_model`, `value`), which is already this frame's published content class — this ticket
  adds a *route*, not a content class, and the bytes are byte-identical to what the connect-time
  reconcile already unicasts. **Threat 2 (server-id race):** unaffected; the verb is unreachable
  outside an established Noise session, so an attacker who wins a server-id race still cannot send
  one. **Threat 3 (relay operator MITM):** the reply is AEAD-sealed like every other application
  frame; the relay sees ciphertext only.

- **[Threat model — existence oracle]** The class most relevant here and absent from the numbered
  list, so it is walked separately. `conversation.not_found` is distinguishable from
  `model_list.unavailable`, which does tell a caller whether the daemon hosts a named
  conversation. That discloses nothing new: a paired client can already enumerate conversations
  with `list_conversations`, and both `request_snapshot` and `request_history` already answer an
  unknown conversation distinguishably. The genuinely closable case is the **non-interactive**
  conn, and AC #3 closes it at step 1 — inert before any decode, seam call or reply, so such a
  conn cannot learn whether the named conversation exists, whether a menu is retained, or that the
  verb is implemented at all. **The merge of "no seam wired" into `model_list.unavailable` is a
  disclosure decision** for the same reason: a distinguishable code would publish whether the
  host's model-list source is configured, which is a fact about the machine rather than about the
  request.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-05

## Revisions

### 2026-09-05 — a named `modelListFor` adapter instead of an inline closure

**What changed.** The plan's Wiring section specified an inline closure at
`cmd/pyry/main.go` delegating to `resolveBoundModelList`, and said explicitly "no new
adapter function". The implementation adds `modelListFor` to
`cmd/pyry/session_model_list.go` instead, and `main.go` calls it the way it already
calls `retainedModelLists`.

**What drove it.** A compile error, not a preference: `cmd/pyry/main.go` does not import
`internal/protocol`, and an inline closure needs that import for its one type
annotation. The choice was to add the import for a single line, or to build the closure
in the file that already has it. The second is also the better shape on its own merits —
it is byte-for-byte the construction its twin `retainedModelLists` uses, and the two are
read together.

**What it changes about the contract.** Nothing. The adapter forwards both returns and
adds no filter, no second lookup and no re-derivation of which vocabulary answers; the
seam, the handler and the wire are as planned.

**What it changes about the count.** Production source files go from the plan's stated
seven to **eight** — `cmd/pyry/session_model_list.go` joins the list. The overage against
the size table's ceiling of five widens accordingly, on the same floor-rule reasoning the
Size section states, and is recorded here rather than left to be discovered in the diff.

**What it changes about the tests.** The adapter is directly reachable, so
`TestModelListFor_ForwardsTheResolversAnswer` pins that it forwards rather than
transforms — an argument swap, a dropped comma-ok or a filtered row. The plan's Testing
section already named the wiring guard as the proof that the seam is *assigned*; the two
are disjoint, and the guard's own doc comment states that this test stays green with the
`ModelListFor:` line deleted.
