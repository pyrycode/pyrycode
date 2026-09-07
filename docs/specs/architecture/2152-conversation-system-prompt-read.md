# #2152 — a client reads a conversation's system prompt and sees when the running session differs

The read half of the per-conversation system prompt. #2151 landed the write; a
client that opens a conversation it has never written has no way to read one, and
because a change takes effect only at the next session start, showing the stored
value alone would silently mislead an operator who edits it and keeps typing.

## Files read

Paths and the symbols that carried the design.

- `internal/relay/v2session_settings.go` → `handleRequestSessionSettings`,
  `settingsReplyError` — the shape this verb copies whole: capability gate first,
  tolerated decode, comma-ok honoured, one constant reply shape for every
  unresolvable case, `forwardEnvelope` on `Run`. Its four-step "Order mirrors the
  write handler" block is the ordering this handler restates.
- `internal/relay/v2session_modelrequest.go` → `handleRequestModelList`,
  `emitModelListReply` — the nearest analogue by file spread. Its
  marshal-failure posture (log a Warn, answer nothing) is the one this verb takes,
  because this verb mints no wire code at all. Its `KnownConversation` gate is the
  one piece deliberately **not** copied (see Design).
- `internal/relay/v2session_seams.go` → `RunConfigFor`, `ModelListFor`,
  `V2SessionConfig`, `KnownConversation` — the two conversation-keyed seam
  precedents, and `KnownConversation`'s own doc recording that
  `request_session_settings` stopped consulting it at #1610.
- `internal/relay/v2session.go` → `dispatchAppFrame` — the interception switch;
  the inline-on-`Run` arms versus the `enqueueAppFrame` hand-off arms.
- `internal/relay/handlers/set_system_prompt.go` → `SetSystemPrompt`,
  `ConversationSystemPromptSetter` — this ticket's write half. Its layout is
  deliberately **not** copied: that path has no capability gate and says so in its
  own words. Its logging discipline (`conn_id` only, on every branch) **is**
  copied.
- `internal/sessions/systemprompt.go` → `SystemPromptFor` — the spawned-with
  accessor #2150 built for this ticket. Session-keyed; returns `""` for **both**
  no-bytes states; `ErrSessionNotFound` for an id the pool no longer holds.
- `internal/conversations/conversation.go` → `Conversation.SystemPrompt` — the
  stored tri-state (`nil` / non-nil `""` / text) and the `omitempty`-on-a-pointer
  encoding this ticket's reply mirrors on the wire.
- `cmd/pyry/main.go` → `resolveBoundRunSettings`, `boundRunSettings`,
  `sessionSettingsReader` — the resolver half of the composition-root pattern this
  ticket copies exactly: a named, unit-testable resolver over registry + pool,
  returning a cmd/pyry-local struct so no `internal/sessions` type escapes.
- `cmd/pyry/relay.go` → `runConfigFor`, `relayWiring`, `startRelayV2` — the
  adapter half. `runConfigFor`'s `resolve == nil ⇒ nil` decided at build time is
  the structural nil-seam rule this ticket reuses.
- `cmd/pyry/session_model_list.go` → `modelListFor` — the alternative adapter
  shape (a named function in a file importing `internal/protocol`), rejected here
  on file count; see Design § Where the pieces live.
- `internal/protocol/settings.go` → `RequestSessionSettingsPayload`,
  `SessionSettingsPayload` — the request/reply pair shape, and the "every zero is
  a real answer, no `omitempty`" contract.
- `internal/protocol/interactive.go` → `RequestModelListPayload` — the one-field
  request shape and its stated reason for tolerating a decode failure.
- `internal/protocol/codes.go` → `TypeRequestModelList`,
  `TypeRequestSessionSettings` — the constant-declaration doc shape and the
  "MUST NOT be added to `inboundAppTypeSet`" filing rule.
- `internal/protocol/envelope.go` → `inboundAppTypeSet`, `IsKnownAppType` —
  confirms **no change is needed here**: the set is v1 inbound app types only, and
  both new constants are v2 control/reply.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`,
  `TestTypeConstants_V1V2Partition` — the two places a new constant must be filed.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`,
  `configSeamSelector` — the third filing place, and the wiring-guard helper.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md`
  — the package overview for the analogue. Three lessons taken from it: the
  tolerated decode does **not** generalise to a verb whose id becomes a path
  component; a `json.RawMessage` payload cannot be sent as truncated JSON to
  exercise a handler decode failure (use valid JSON of the wrong type); and
  seam-consultation **counters**, not reply shape, are what make the
  non-interactive row discriminating.
- `docs/protocol-mobile.md` → § Setting a conversation's system prompt,
  § Session settings (v2) → `request_session_settings` "Three answers",
  § Application-envelope size cap — the prose this ticket extends.

## Context

`set_system_prompt` (#2151) stores a conversation's operator text; `Pool.Activate`
reads it at the next spawn (#2150). Nothing reads it back. Two consequences a
client cannot work around:

1. A client that did not itself perform the write does not know what is stored.
2. The value applies at the **next** session start, so a client showing the stored
   value alone tells an operator their edit is live when it is not. Surfacing that
   gap is the price of the store-and-apply-later design, and this ticket pays it.

**A read route, not a list field.** The stored prompt does not go on the
`list_conversations` record: it can be 8192 bytes and that reply carries every row,
so a handful of prompted conversations would blow the 65519-byte
application-envelope cap. Nor does it go on `session_settings`, whose documented
contract is that every field sits at its zero when no session resolves — exactly
when an operator most needs to see what is stored.

**No ADR is warranted.** This adds one verb to an established family and invents
no pattern; the two decisions worth recording (why not a list field, why the reply
reports a difference rather than a second copy of the text) belong in
`docs/protocol-mobile.md`, which AC #5 already requires.

### Size overage — stated, not hidden

Two lines of the size-S table are exceeded: **7 production source files against 5**
and **~1350 lines of total written work against 800**. Depth permits a split
(`parent 2094`, `grandparent none`), so this is not the depth-capped case. Every
slice along the registration axis has exactly one consumer — the handler — so the
floor merges them back; the one floor-legal cut, stored-value-then-divergence,
leaves its first child touching the same seven production files and still lands
around 800–900 lines, relieving neither exceeded line while costing a second
refiner and builder pass. `needs-human:sizing` is on the ticket with the
measurement; it blocks nothing.

## Design

### Wire vocabulary — `internal/protocol`

Two constants in `codes.go`, following the five inbound "ask the daemon for X"
verbs rather than inventing a sixth idiom:

- `TypeRequestSystemPrompt = "request_system_prompt"` — phone → binary, inbound v2
  control, switch-intercepted.
- `TypeSystemPrompt = "system_prompt"` — binary → phone, the reply, correlated by
  `in_reply_to`.

Neither goes in `inboundAppTypeSet`: both are v2, intercepted before
`dispatch.Route`. Both are filed in `v2OnlyTypes` and in
`TestTypeConstants_V1V2Partition`'s `all` list, and in `relay_guard_test.go` —
the request as `switch-intercepted` from the moment the constant exists (its
handler lands in the same commit, so a "pending handler" filing would fail
Assertion #2), the reply as an outbound `reply`.

Payloads in a new `internal/protocol/system_prompt.go` — its own file for the
reason `settings.go`, `history.go` and `snapshot.go` each have one: this is a
frame family, and #2151's write payload lives with the conversation **record**
(`conversations_write.go`) while this pair is about the prompt itself.

```go
type RequestSystemPromptPayload struct {
    ConversationID string `json:"conversation_id"`   // no omitempty
}

type SystemPromptPayload struct {
    SystemPrompt        *string `json:"system_prompt,omitempty"`
    SessionPromptStatus string  `json:"session_prompt_status"`  // no omitempty
}

const (
    SystemPromptStatusNoSession = "no_session"
    SystemPromptStatusMatches   = "matches"
    SystemPromptStatusDiffers   = "differs"
)
```

**`SystemPrompt` is a `*string` with `omitempty`, and that pairing is the whole of
AC #1.** `omitempty` on a pointer tests the pointer, not the pointee, so `nil`
omits the key while a non-nil `""` still emits `"system_prompt": ""`. The three
registry states therefore survive the round trip in the encoding the registry
already uses on disk and the write verb already accepts inbound — a client can
read one and write it straight back without collapsing "explicitly empty" into
"no prompt". This is `Conversation.SystemPrompt`'s own reasoning, transferred.

**The reply carries no `conversation_id`.** Correlation rides `InReplyTo`, exactly
as `session_settings` carries none — and it is what lets AC #3's unhosted answer be
byte-identical to the hosted-no-prompt one, since an unhosted request has no
registry-canonical id to report and echoing the client's own would be the shape
this package refuses everywhere else.

**`SessionPromptStatus` is a closed three-value enum, never `""`.** The handler
always sets one of the three constants, including on the unresolvable path. The
zero `SystemPromptPayload` is therefore never a value any path emits.

### The handler — `internal/relay/v2session_systemprompt.go`

Its own file, matching `v2session_modelrequest.go`'s stated reason (a
prompt-request handler in a settings-named file is a readability hazard worth one
file to avoid). Intercepted in `dispatchAppFrame` before `dispatch.Route` and
answered **inline on the `Run` dispatch goroutine** — a registry `Get`, one pool
map read and one small marshal, none of the hashing, file reading or page
budgeting that moved other arms to `enqueueAppFrame`. That placement is a
constraint on future edits: the reply is sealed under the single-owner send
`CipherState`, so an arm moved off `Run` must switch `forwardEnvelope` →
`forwardToRun` in the same change.

`handleRequestSystemPrompt(ctx, s, env)` — contract: answers exactly one
`system_prompt` envelope to an interactive conn, and nothing at all to any other.
Order:

1. **Capability gate, fully inert.** `if !s.interactive { return }` — no decode,
   no seam call, no reply. AC #3's second half. This is the gate #2151's write half
   deliberately does not have, and copying that file's layout is the specific
   mistake to avoid.
2. **Decode, tolerated.** `_ = json.Unmarshal(env.Payload, &p)`; a failure leaves
   `ConversationID == ""`, which names no conversation. The tolerance is safe here
   for this verb's own reason and does not generalise: the id reaches a registry
   lookup and nothing else — it never becomes a path component. The error is never
   echoed and never logged.
3. **Resolve, or don't.** Empty-id guard, nil-seam guard, comma-ok honoured — the
   three properties `handleRequestSessionSettings` spells out, unchanged. The
   payload variable starts at the constant `{SessionPromptStatus: no_session}` and
   is overwritten only on `ok == true`.
4. **No `KnownConversation` call, and that is a decision.** Its own doc records
   that `request_session_settings` stopped consulting it at #1610, because
   membership answers a question this verb does not ask: the resolver already
   refuses an unknown conversation, and AC #3 wants an unknown one answered
   identically to a hosted-but-quiet one, so a second gate could only split an
   answer the AC requires merged. `request_model_list` needs it because it has two
   refusal codes to separate; this verb has none.
5. **Emit.** One `TypeSystemPrompt` envelope, `InReplyTo` set, `EventID` nil (never
   enters the #647 replay ring, advances no client cursor). A marshal failure —
   unreachable for a struct of a `*string` and a constant — logs a Warn and answers
   nothing, `emitModelListReply`'s posture, so **this verb has no error-frame path
   at all** and mints no wire code.

**Logging (AC #4).** `conn_id` and an event string, on every branch, and nothing
else. Never the prompt, never its length, never the conversation id, never the
decode error, and **never the status** — the status is derived from the operator
text, and there is no operational question it answers. This is
`SetSystemPrompt`'s divergence from its own neighbours, kept for the same reason.

**And never `err` on the push-drop branch**, which is where this handler departs
from every neighbour that writes `"err", err` there. `forwardEnvelope` returns two
static sentinels and three wrapped errors, one of them from `json.Marshal` of the
whole envelope — an error string that can quote payload bytes. That marshal is
unreachable for a payload this handler produced itself, so the neighbours are not
wrong; but AC #4 says *no path*, and dropping one attribute makes that structurally
true instead of argued from unreachability. Log the event and `conn_id`.

### The seam — `V2SessionConfig.SystemPromptFor`

```go
SystemPromptFor func(conversationID string) (protocol.SystemPromptPayload, bool)
```

`ModelListFor`'s shape: a string in, a marshal-ready payload and a comma-ok out, so
`internal/relay` imports neither `internal/sessions` nor `internal/conversations`
and `protocol` is already imported on both sides.

Contract points the doc block must carry:

- **Comma-ok, and a caller MUST NOT read the payload on false.** Pinned in
  `internal/relay`'s own tests with a *poisoned* refusal double (a populated
  `SystemPrompt`, a `differs` status, `ok == false`), so fail-closed is a property
  of this package rather than one borrowed from the producer's good behaviour —
  #2125's lesson.
- **Optional: nil ⇒ every request is answered with the constant no-session reply.**
  Not an error, unlike `SettingsUpdater`'s nil: this verb is documented as always
  answering with one shape.
- **Two different resolution failures, not one.** `false` means *this daemon does
  not host the conversation*. A hosted conversation with no running session is
  `true` with `SessionPromptStatus: no_session`. The wire answer merges them by AC
  #3; the seam does not, so the producer is never asked to pre-merge and the
  merge stays one decision made in one place.
- **Bounded time, on the `Run` goroutine** — a registry lookup plus one pool map
  read. An implementation that reads a file belongs off `Run`, and moving it there
  means switching the handler's emit in the same change.
- **Security.** `conversationID` is untrusted network input, is spent only as a
  lookup key into the daemon's own registry, and reaches neither the reply, a log
  line, an error string, nor a path. The returned `SystemPrompt` is
  operator-authored text that must never be logged.

### Where the pieces live in `cmd/pyry` — and why no new file

`runConfigFor`'s split, copied field for field, which is what keeps this at seven
production files instead of eight:

- **`main.go`** (imports `sessions` + `conversations`, not `protocol`) gets the
  resolver and its narrow reader interface, beside `resolveBoundRunSettings`:

  ```go
  type spawnedPromptReader interface {
      SystemPromptFor(id sessions.SessionID) (string, error)
  }

  type conversationPromptState struct {
      stored      *string  // registry tri-state, copied — never the registry's own pointer
      spawnedWith *string  // nil = no live session to compare against
  }

  func resolveConversationPrompt(convReg *conversations.Registry, pool spawnedPromptReader, convID string) (conversationPromptState, bool)
  ```

  Refusal inherited verbatim from `resolveBoundRunSettings`: an unknown
  conversation returns `(zero, false)` **before** the pool is touched, and an
  empty `CurrentSessionID` short-circuits the pool lookup — the #678 isolation
  point, kept even though `Pool.SystemPromptFor` is a plain map read, so no path
  in this file can express fall-through-to-bootstrap. `ErrSessionNotFound` (an
  evicted session) leaves `spawnedWith` nil: there is genuinely no running session
  to compare against. The error is discarded rather than wrapped, so a hostile id
  cannot be reflected into anything a caller builds. It takes no logger and must
  not grow one.

  `stored` is a **copy of the pointee**, not `conv.SystemPrompt` itself:
  `Registry.Get` copies the record shallowly, so the returned pointer aliases
  registry-held memory, and `SetSystemPrompt`'s own doc flags that aliasing as a
  hazard for anything that projects the field. The read itself is race-free —
  `Registry.Get` takes the registry mutex, so the pointer value is read under it,
  and Go strings are immutable so the pointee is never mutated in place. The copy
  is defence against a later change that projects or retains the field, not a fix
  for a live race.

- **`relay.go`** (imports `protocol` + `relay`, not `sessions`) gets the wiring
  field and the adapter:

  ```go
  func systemPromptFor(resolve func(convID string) (conversationPromptState, bool)) func(convID string) (protocol.SystemPromptPayload, bool)
  ```

  `resolve == nil ⇒ nil`, decided at build time before any closure exists, so "no
  path can invoke a nil resolver" is structural — `runConfigFor`'s rule. This is
  where the three-way comparison is computed (below), and `startRelayV2` assigns
  `SystemPromptFor: systemPrompt` into the `V2SessionConfig` literal, the same
  plain-identifier shape `RunConfigFor: runConfig` already has.

**The collapse is the trap, and it lives in `systemPromptFor`.**
`Pool.SystemPromptFor` returns `""` for **both** no-bytes states by design, so the
comparison must be computed on the collapsed stored value —
`collapse(nil) == collapse(&"") == ""`. Comparing the pointer against the
spawned-with string without collapsing first reports a conversation storing an
explicitly empty prompt, whose session spawned with no operator text, as
*differing*. It matches.

| `stored` | `spawnedWith` | `session_prompt_status` |
|---|---|---|
| any | `nil` | `no_session` |
| `nil` | `&""` | `matches` |
| `&""` | `&""` | `matches` — the trap |
| `&"x"` | `&"x"` | `matches` |
| `nil` | `&"x"` | `differs` |
| `&""` | `&"x"` | `differs` |
| `&"x"` | `&""` | `differs` |
| `&"x"` | `&"y"` | `differs` |

The reply carries the *stored* value and a *verdict*, never the spawned-with text:
echoing up to another 8192 bytes back over the wire to say "these differ" is what
the ticket rules out, and a client wanting a diff is a later ticket.

## Concurrency model

No goroutine is spawned and none is needed. The handler runs synchronously on the
manager's single `Run` dispatch goroutine and replies through `forwardEnvelope`,
the seal-under-`s.send` route — **not** `forwardToRun`, because it never leaves
`Run`. The send `CipherState` is single-owner, so sealing from any other goroutine
would be a concurrent `Encrypt`, a nonce reuse rather than a race annoyance.

Below the seam the locks are the registry's and the pool's, acquired sequentially
and never nested, so this path adds no edge to the daemon's lock order — it puts
an existing lock chain on `Run`, where the connect-time reconciles already run
theirs. `Pool.SystemPromptFor` documents that it must be called with `p.mu`
unheld; the resolver holds nothing when it calls it. A conn's frames dispatch in
order on one goroutine, so a client cannot overlap two of its own requests.

The registry read and the pool read are **separately locked**, so a `/clear`
rotation or an idle eviction can land between them. The observable consequence is
bounded and benign: the reply describes the stored value as of the first
acquisition and the running session as of the second, and the worst outcome is a
status one rotation stale on a request the client can simply repeat. Reporting
them under one acquisition would mean a combined pool+registry lock that no
existing path takes, which is a lock-order edge this verb does not justify —
`RunConfigFor`'s single-acquisition argument does not transfer, because there the
two values were *fields of one session*.

## Error handling

There is no error frame on this path and no wire code is minted — the difference
from `request_model_list`, whose two codes exist because an empty `models` array
cannot stand in for "unknown". Here every unresolvable case has a truthful
constant answer.

| Condition | Answer |
|---|---|
| Non-interactive conn | Nothing at all — no reply, no decode, no seam call |
| Payload will not decode | Empty id ⇒ the constant no-session reply |
| Absent or empty `conversation_id` | The constant no-session reply |
| Conversation not hosted (seam `false`) | The constant no-session reply |
| Seam nil (foreground / v1) | The constant no-session reply |
| Hosted, no bound session | `stored` reported, status `no_session` |
| Hosted, bound session the pool no longer holds | `stored` reported, status `no_session` |
| Hosted, bound, live | `stored` reported, status `matches` / `differs` |
| Marshal failure (unreachable) | Warn log, no reply |

The constant reply is `{"session_prompt_status": "no_session"}` with the
`system_prompt` key absent — which is exactly what a hosted conversation holding
no prompt and running nothing is answered with, so an unhosted conversation is
indistinguishable from that state (AC #3) and never draws an error frame.
`request_session_settings`' all-zero posture, transferred.

## Testing strategy

RED first: a `request_system_prompt` frame driven through the manager's
Frames/`Run` loop draws `dispatch.Route`'s `protocol.unknown_type` reply before
the interception case exists.

**`internal/protocol/system_prompt_test.go`** (new)

- Round trip of both payloads through a full `Envelope`, pinning the type strings.
- Complete wire-key set for each payload, so a later `omitempty` added for tidiness
  reddens.
- The tri-state: `nil` omits the key, `&""` emits `"system_prompt": ""`, text
  emits the text — and a decode-then-encode of each of the three is byte-stable,
  which is AC #1's round-trip clause.
- The zero `RequestSystemPromptPayload` still carries its key.

**`internal/protocol/compat_test.go`** — both constants filed in `v2OnlyTypes` and
in `TestTypeConstants_V1V2Partition`'s `all` list.

**`internal/relay/v2session_systemprompt_test.go`** (new), table-driven, real
frames through the manager rather than unit calls on the handler:

- Non-interactive conn: no frame emitted **and** the seam consulted zero times.
  The counter is what makes the row discriminating — without it, "no reply" is
  indistinguishable from "a seam ran and produced nothing".
- Nil seam, empty id, absent payload: the constant reply; seam consulted zero
  times where the guard short-circuits.
- Malformed payload as valid JSON of the wrong type (`{"conversation_id":123}`) —
  a `json.RawMessage` cannot carry truncated JSON, which fails in the test's own
  envelope marshal instead of in the handler.
- Poisoned refusal double: `ok == false` with a populated payload ⇒ the constant
  reply, none of the poison on the wire.
- Each of the three statuses and each of the three stored states forwarded byte
  for byte, `InReplyTo` correlated, `EventID` nil.

**`cmd/pyry/system_prompt_test.go`** (new)

- `resolveConversationPrompt`: unknown conversation ⇒ `(zero, false)` with the
  pool consulted **zero** times (a counting double — the claim is about calls, and
  the returned values cannot carry it); empty `CurrentSessionID` likewise; an
  evicted session ⇒ `spawnedWith` nil; a live session ⇒ the spawned-with bytes;
  the registry's own pointer is not aliased.
- `systemPromptFor`: the eight-row status table above, driven through a fake
  resolve — this is where the collapse is pinned; and `nil` resolve ⇒ `nil` seam.

**`cmd/pyry/relay_guard_test.go`** — `TypeRequestSystemPrompt` filed
`switch-intercepted`, `TypeSystemPrompt` filed `reply`.

**`docs/protocol-mobile.md`** — the two frame-table rows, a
"Reading a conversation's system prompt" section pair documenting both frames, the
three stored states, the three statuses, and **why the stored prompt is not on the
`list_conversations` record** (AC #5), plus a dated changelog entry.

Verification is the touched-scope gate: `go test -race` on
`internal/protocol/...`, `internal/relay/...` and `cmd/pyry/...`, `go vet ./...`,
`go build ./cmd/pyry`.

## Open questions

1. ~~**Does `Registry.Get` take the registry mutex?**~~ **Resolved during the
   security pass, before this plan was committed:** it does, and it returns a
   shallow copy, so the pointer is read under the lock. The copy-the-pointee rule
   stays as forward defence rather than as a race fix; the Design paragraph says
   so.
2. **Which `docs/protocol-mobile.md` tables need the two new rows** — the inbound
   control table near `request_session_settings` and whichever table carries
   outbound replies. Resolve by reading the tables, not by guessing.
3. **Does the e2e fake-daemon harness need a case for the new type?** Only if it
   maintains its own type map; the guard test's `inboundTypes` is the one this
   ticket knows it must feed.

Each remaining question is resolved during Phase B and recorded under
`## Revisions` if it changed the design.

## Security review

**Verdict:** PASS

The pass changed the plan in one place before this verdict was reached: the
push-drop branch no longer logs `err` (finding under Error messages / logs). The
adversarial question driving every category was *what does a paired but hostile
client gain by sending this frame, and what does an operator lose by trusting the
answer?*

**Findings:**

- **[Trust boundaries]** No findings, and the boundary is single rather than
  scattered: `handleRequestSystemPrompt`'s `json.Unmarshal` is the only place
  untrusted bytes become a typed value, and `ConversationID` is spent on exactly
  one thing — a `Registry.Get` key inside `resolveConversationPrompt`. **The
  session id handed to `Pool.SystemPromptFor` is daemon-authored**: it is
  `conv.CurrentSessionID` off the resolved record, never the caller's string. That
  closes the #678 hazard class (read one conversation's state through another's
  id) by construction rather than by a check, and the empty-`CurrentSessionID`
  guard is kept on top of it so no code path in this file can even express
  fall-through-to-bootstrap. **Obligation for a later editor:** feeding `convID`
  to the pool directly, or widening the seam past
  `func(string) (protocol.SystemPromptPayload, bool)`, breaks this — the seam's doc
  block says so.
- **[Trust boundaries — the outbound half]** No findings, one decision recorded.
  This verb publishes operator-authored text that becomes standing instructions to
  a claude child, so *who may read it* is the question. Two properties hold it:
  the reply is **unicast** via `forwardEnvelope(ctx, s.connID, reply)` and never
  the push/broadcast path — which is the exact widening #2151 avoided by keeping
  the prompt off the broadcast `conversation_updated` ack — and the **interactive
  capability gate** makes this read surface strictly *narrower* than the write
  surface #2151 shipped, which has no such gate. No privilege is escalated: a
  party that can send this frame can already set the value.
- **[Tokens, secrets, credentials]** Not applicable, stated rather than skipped:
  nothing is minted, derived, stored, rotated or compared. The prompt is sensitive
  operator text, not a credential, and its handling is covered under logs and
  under the audience decision above.
- **[File operations]** Not applicable by construction, and it is the reason the
  tolerated decode is safe here. No path is built and no file is opened; the id
  reaches a registry lookup and nothing else, so an empty id is refused rather
  than resolving to a directory root the way `filepath.Join(dir, "")` would in
  `handleRequestHistory`. That tolerance is per-verb and must not be copied to a
  verb whose id becomes a path component.
- **[Subprocess / external command execution]** Not applicable, and structurally
  so. The handler holds no seam through which a session could be started,
  restarted, rotated, interrupted, or have its argv recomposed — `SystemPromptFor`
  returns a payload and a bool. This is the read-side twin of
  `ConversationSystemPromptSetter`'s "that this interface names no session, pool or
  runner surface is load-bearing"; reading the prompt leaves a running session
  alone because there is nothing to reach.
- **[Cryptographic primitives]** No primitive is chosen or minted; one is
  *consumed*, and the obligation is real. The reply is sealed under the session's
  single-owner send `CipherState` on the `Run` goroutine, so an edit that moved
  this arm to `enqueueAppFrame` without switching `forwardEnvelope` → `forwardToRun`
  would be a concurrent `Encrypt` — nonce reuse, a break rather than a race
  annoyance. Recorded in the handler's file header as a constraint on future
  edits, not as a fact that happens to be true today.
- **[Network & I/O — inbound]** No findings. The request carries one string and no
  count or length field, so there is nothing a hostile value can size an
  allocation from; the transport's AEAD frame cap is the only bound needed and it
  already exists.
- **[Network & I/O — outbound]** No finding, one figure checked rather than
  assumed. `conversations.MaxSystemPromptBytes` is 8192, and `encoding/json`'s
  worst-case escaping is 6 bytes out per byte in (`\u00XX`), so the largest
  possible reply body is ~49 KB against the 65519-byte application-envelope cap —
  it fits, with headroom, for **one** prompt. That margin is exactly why the value
  cannot ride the `list_conversations` record, where it would multiply by row
  count, and AC #5 requires that reasoning be published.
- **[Network & I/O — resource exhaustion]** OUT OF SCOPE, named rather than
  ignored. A paired client can spam the verb; each request costs a linear registry
  scan under a mutex, one pool map read and a ≤49 KB marshal, all on `Run`. This is
  the same profile `request_session_settings` and `request_model_list` already
  carry, so the ticket adds no new vector — but the marshal is larger than either.
  No per-verb rate limit exists anywhere on this wire, and inventing the first one
  here would be a wire-wide policy decision made inside a read verb. It belongs to
  a ticket that can apply it to the whole inbound surface; a client's frames
  dispatch in order on one goroutine, so a single conn cannot overlap its own
  requests in the meantime.
- **[Error messages, logs, telemetry]** **One finding, fixed in the plan before
  this verdict.** Every neighbour logs `"err", err` on the `forwardEnvelope`
  push-drop branch. `forwardEnvelope` returns two static sentinels and three
  wrapped errors, one of them from `json.Marshal` of the whole envelope — an error
  string that can quote payload bytes, which here are the operator's prompt. The
  marshal is unreachable for a payload this handler produced itself, so the
  neighbours are not wrong, but AC #4 says *no path, including the not-found and
  malformed branches*, and an argument from unreachability is weaker than dropping
  one attribute. **This handler logs the event and `conn_id` and nothing else on
  every branch** — not the prompt, not its length, not the conversation id, not the
  decode error, not `err`, and **not the status**, which is derived from the
  operator text. Do not add the status back for debuggability: that is the shape
  #1687's permission-mode reject rule already refuses one layer in. There is no
  error frame on this path at all, so no reply message can carry a byte either.
- **[Concurrency]** No findings; two windows examined and both benign. Locks are
  the registry's then the pool's, acquired sequentially and never nested, adding no
  edge to the daemon's lock order — `Pool.SystemPromptFor` requires `p.mu` unheld
  and the resolver holds nothing. The **two-acquisition window** (registry read,
  then pool read) can report a stored value one write stale against a current
  session, or a session one rotation stale against a current stored value. Neither
  grants a privilege, bypasses a gate, or crosses a conversation boundary; the
  worst outcome is a status the requesting client can correct by asking again.
  Closing it would mean a combined registry+pool acquisition no existing path
  takes — `RunConfigFor`'s single-acquisition argument does not transfer, because
  there the values were fields of *one session*. No goroutine is spawned, so none
  can leak, and the verb mutates nothing, so a signal mid-handler loses a reply
  and no state.
- **[Threat model alignment]** No findings, and one property is worth naming
  because AC #3 buys it for free. `docs/protocol-mobile.md` § Security model's
  relevant threats: an **unpaired party** is excluded by the Noise_IK handshake
  before a frame is decrypted; a **paired non-interactive conn** is excluded by the
  capability gate, which is fully inert — no decode, no seam call, no reply — so it
  cannot learn that the verb is implemented. A **paired client probing for which
  conversations exist** gains nothing: the unhosted answer is byte-identical to the
  answer for a hosted conversation holding no prompt and running nothing, so the
  verb is not a membership oracle — and even if it were, a paired client can
  already enumerate every conversation with `list_conversations`. Out of scope and
  named: any *authorization* finer than pairing-plus-interactive, which no verb on
  this wire has and which #2151 already decided for the write half.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
