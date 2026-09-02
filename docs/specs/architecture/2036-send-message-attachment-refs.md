# #2036 — declare a message's attachment references on `send_message`

Split from #1745 (grandparent #1684). Ships the **declaration half** of message↔attachment
association: `send_message` gains an optional list of attachment ids, published in
`docs/protocol-mobile.md`, with a bound a client can read before it sends and a named
code for the id-does-not-resolve path. Nothing produces, consumes or validates the field
when this closes — composing the prompt from it is **#2038**.

## Files read

- `internal/protocol/messaging.go` → `SendMessagePayload` — the struct this slice extends; its
  three-key form and the package's no-`omitempty` house style are the starting point.
- `internal/protocol/attachments.go` → `MaxAttachmentChunkBytes`, `MaxAttachmentIDBytes`,
  `AttachmentChunkPayload`, `AttachmentStoredPayload` — the published-number bound idiom to
  copy (a producer-side contract with **no validator**), and the doc-block depth this family
  writes at. `AttachmentChunkPayload`'s SECURITY block is where the "an id becomes a path
  component, so validate its canonical shape first" rule already lives.
- `internal/protocol/codes.go` → `CodeAttachmentNotFound` and the `attachment.*` comment block —
  carries the retrieval-only wording AC 4 requires be brought into agreement with the doc row.
- `internal/protocol/questions.go` → `QuestionAnswerPayload.MarshalJSON` — the one **inbound**
  slice-valued payload with a nil→`[]` normaliser; its rationale is why that precedent does
  **not** transfer here (see Design).
- `internal/protocol/messaging_test.go` → `TestSendMessagePayload_RoundTrip` — the fixture test
  the absent-key regression case rides on.
- `internal/protocol/attachments_test.go` → `TestAttachmentStoredPayload_ZeroValue_KeysPresent`,
  `TestAttachmentStoredPayload_WireKeys` — the two pin shapes; AC 2 names the first by name.
- `internal/protocol/compat_test.go` → the `CodeAttachment*` symbol and value maps — checked to
  confirm **no** entry is needed, since this slice mints no code.
- `internal/relay/handlers/send_message.go` → `SendMessage` — the one production decode site of
  `SendMessagePayload`; its malformed-payload branch is what an ill-typed `attachment_ids`
  will reach.
- `internal/attachments/registry.go` → `ErrUnknownUpload` — the precedent for *how* this family
  hands a code decision along, and the reason AC 4 is this slice's to make rather than inherit.
- `docs/protocol-mobile.md` § Application message types, § Attachments, § Error codes,
  § Changelog — the four publication points.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — two lessons carried into the
  test plan: **only a committed fixture catches a wire-string typo** (the three `compat_test.go`
  registries key on the Go symbol and cannot), and **a key-set pin outlives a fixture
  regeneration** that a round trip does not.
- `docs/knowledge/features/protocol-package-types-messaging-payloads.md` — records the package's
  "pure DTOs, no methods, no `Validate()`" posture, which this plan departs from once and says why.

## Context

The upload leg is complete (#1895–#1898): bytes are reassembled, verified and filed under
`conversations/<conversation-id>/attachments/<attachment-id>/`, and the client is answered with
`attachment_stored`. **Nothing on the wire says which message an uploaded attachment belongs to.**
A daemon would have to infer the set from upload order or arrival timing. This slice removes that
guess by publishing the association, ahead of the consumer, in the family's own
declare-then-implement sequencing.

`send_message` is a **v1-compatible inbound type** (it sits in `inboundAppTypeSet`, the closed set
`IsKnownAppType` gates). That single fact drives every wire decision below: shipping clients predate
the field and send no such key, so the empty-case form has to be *decided* rather than copied from a
v2-only neighbour.

**No ADR is warranted.** The one genuinely novel decision — normalising three empty wire forms to
one in-memory value — is local to a single payload and fully argued in its doc block; it establishes
no cross-package convention. The documentation phase should fold the reasoning into
`docs/knowledge/features/protocol-package-types-messaging-payloads.md` instead.

## Design

### The field

`SendMessagePayload` gains a fourth field: an optional, ordered list of attachment ids.

- Wire key `attachment_ids`, element shape the **lowercase UUIDv4** already published under
  § Attachments → *The `attachment_id` shape* — the same rule binding `attachment_chunk` on both
  legs and `attachment_stored`. Not re-specified; referenced.
- Go type `[]string`, **with `omitempty`** — the only `omitempty` in this package's payload set,
  and the departure is the whole point rather than an oversight (below).
- Order is the client's own presentation order. It is **not** a correlation key: ids identify
  attachments, positions identify nothing, matching `QuestionAnswerEntry`'s "array order is not the
  correlation" rule.
- The bound counts **elements, not distinct ids** — a list may name the same id 32 times, and a
  client reading "at most 32 attachments" must not assume the receiver dedups. Published, so a
  consumer decides deduplication deliberately rather than by omission.

### Every element is a claim — the doc block this field must carry

The field is **inbound and attacker-chosen on every element**, and this slice ships no check of any
kind. The Go doc block therefore carries the rule at the declaration, where #2038's implementer
reads it, rather than only in `docs/protocol-mobile.md` — `AttachmentChunkPayload`'s doc block is
the precedent and the wording to mirror:

- Each element is an **unverified claim**, and it **becomes a directory component** beneath the
  resolved conversation directory. Its canonical shape is validated **before** it reaches
  `filepath.Join`; `conversations.ValidID` is the existing check, the one `attachments.EnsureDir`
  already applies on the upload leg. An element reaching a path unvalidated is a traversal, and
  `MaxAttachmentIDBytes` does not help — 64 bytes accommodates `../../../../etc/passwd` several
  times over, so **containment is a consequence of the shape and never of any length ceiling**.
- The shape check is load-bearing a **second** time, against § Security model **threat 1 (prompt
  injection)**. Unlike `attachment_stored`, which carries no client-authored byte and is exempt,
  every element here is client-authored and #2038 composes a prompt from it. A shape-validated
  canonical id draws from `[0-9a-f-]` only and **cannot carry injection text**; an unvalidated
  element is an arbitrary string that reaches claude's input verbatim. The two hazards are
  independent and one check answers both — which is why the check is not optional even for a
  consumer that never touches the filesystem.
- No named type and no type-system signal for untrustedness: `AttachmentChunkPayload` carries the
  same hazard on a plain `string` field with a doc block, and inventing a `TrustedID` wrapper here
  would establish a convention this package does not otherwise run. The comment is the convention.
- **The elements are not a capability, and this slice must not publish anything implying they
  are.** In particular it must not argue "UUIDv4, therefore unguessable" — the family's published
  stance is that the id is *not* secret, *not* unguessable and never the only thing between a
  caller and a file, and an entropy claim here would quietly promote it. What keeps the field safe
  is confinement, below — never the id's shape or its entropy.

### Confinement is the authorization property

**A named id resolves only under the message's own conversation** — the conversation the
authenticated v2 session is already on, decided daemon-side from session context, never a
client-asserted one. This is the same property `attachment_chunk`'s deliberately absent
`conversation_id` establishes for the upload leg, and it is what stops "name any id, get its bytes
into your prompt" — i.e. what keeps a non-secret, non-capability identifier from becoming a
capability the moment a message can reference one.

It is published in § Attachments as a **security property in its own right**, not merely as the
scope of the `attachment.not_found` row below. A reader who meets confinement only as a footnote on
an error code reads it as "when to emit this code" and implements the resolution without it.

### The empty-case wire form: key absent

**A message naming no attachments carries no `attachment_ids` key at all.** That is the published
form, and `omitempty` is what produces it.

The alternative the package makes tempting is the nil→`[]` `MarshalJSON` that `question_shown`,
`background_task_roster`, `model_list` and `tool_use` carry, so the key is always present and never
null. **Every one of those is an outbound, daemon-authored v2 frame.** `QuestionAnswerPayload` is
the near miss — it is genuinely inbound and still normalises — but it is a v2-only type that has
carried its `answers` array since it was minted, so "always present" was true of every client that
ever sent one. Here it would be false on arrival: a shipping client sends three keys and always
will. Publishing "always present" for a key half the wire's population never sends states a contract
the wire does not honour, and it would break the existing `send_message.json` fixture's
byte-equivalent round trip — which is exactly why AC 3 keeps that fixture as the absent-key
regression case.

So: **absent is canonical**; `null` and `[]` are accepted and normalised on arrival.

### Making the three forms indistinguishable — `UnmarshalJSON`

AC 2 requires that no consumer can branch on which of the three empty forms arrived. **With a plain
`[]string` that is false**, and the gap is the single most common Go slice trap:

| wire | plain `[]string` decodes to | `== nil` |
|---|---|---|
| key absent | `nil` | true |
| `null` | `nil` | true |
| `[]` | empty **non-nil** slice | **false** |

`len(x) == 0` agrees across all three, but `x == nil` does not — so a consumer *can* branch, and
#2038 is the consumer about to read this field. The plan therefore adds a pointer-receiver
`UnmarshalJSON` to `SendMessagePayload` that decodes through an unexported `alias` defined type
(no methods, so no recursion — `QuestionAnswerPayload.MarshalJSON`'s recorded reason, inverted for
the decode direction) and collapses a zero-length result to `nil`. All three forms then produce
exactly `nil`, and the AC becomes a **property rather than a promise**.

Three consequences worth stating:

- The marshal direction needs **no** method: `omitempty` already elides both `nil` and empty-non-nil,
  so the two directions are symmetric for free. Do not add a `MarshalJSON` by analogy with the four
  outbound payloads — it would re-introduce the always-present key this section just rejected.
- An **ill-typed** `attachment_ids` (a string, an object, a number) is a decode **error** that
  propagates to the caller — never a silently-empty list. At the one production decode site,
  `SendMessage` in `internal/relay/handlers/send_message.go`, that reaches the existing
  malformed-payload branch and is answered `protocol.malformed`. Stating this is the point: an
  unspecified decode failure that yields an empty list reads as "this message names no attachments"
  on garbage input.
- A payload of literal `null` decodes cleanly to the zero value rather than erroring, matching
  `QuestionAnswerPayload`'s recorded reading — that is an unknown message, not a rejected frame,
  and judging it belongs to #2038.

This is a deliberate, single departure from the package overview's "pure DTOs: no methods, no
constructors, no `Validate()`" posture. It is not validation — nothing is checked, nothing is
rejected on content — it is **normalisation at the decode boundary**, which is where a
parser-differential ambiguity on an attacker-controlled inbound frame belongs.

### The bound: `MaxAttachmentIDsPerMessage = 32`

A published **number**, the first of the family's two bound idioms — the one
`MaxAttachmentChunkBytes` and `MaxAttachmentIDBytes` use, a producer-side contract a client must
obey to compose a conforming frame. Not the second idiom: the per-upload byte bound and the
concurrency bound behind `attachment.too_large` / `attachment.too_many_uploads` are deliberately
receiver-configured and unpublished, learned by being rejected. A client composing a message needs
this one **before** it sends.

**Stated plainly as unchecked.** `internal/protocol` is a stdlib-only leaf data package with no
producer, no consumer and no validator; nothing here counts the list. Enforcement is #2038's.

Why 32, as arithmetic rather than taste:

- Each canonical id is exactly 36 bytes, costing 39 on the wire inside a JSON array (two quotes and
  one separator). 32 ids are 1248 bytes — **under 2% of the 65519-byte application-envelope cap** —
  so the bound never competes with `text` for envelope budget and a client never trades one against
  the other. This is why the bound is not derived *from* the envelope cap: that derivation yields
  roughly 1600 ids and would leave no room for the message.
- The binding constraint is **resource, not bytes**. Every id a client names becomes a directory
  component beneath the resolved conversation directory, so N ids are N resolutions per inbound
  frame. 32 bounds that work at a number a receiver does inline without a queue.
- 32 sits far above what a person attaches to one message, so no legitimate client meets it.

**State the bound honestly and do not oversell it.** The envelope cap already limits an
unbounded list to roughly 1680 elements (65519 ÷ 39), and a paired device is already authorized to
send messages that spawn claude turns — orders of magnitude more expensive than 1680 directory
resolutions. So this bound is **contract clarity and bounded work**, not a new defence against a
threat the pairing boundary does not already permit. The doc says that rather than dressing it as a
DoS mitigation; overstating a bound is how the next reader concludes the path is guarded when it is
not.

### Logging the elements

`attachment_chunk`'s published rule is "log the attachment id, the index and the total; never the
bytes, and never a raw filename" — but it is written for a value that is about to be, or has been,
shape-checked. **An element of this list is loggable only after its canonical shape is validated.**
Raw, it is an arbitrary client-supplied string in a line-oriented log, which is the same
log-injection shape the family already forbids for `filename` and for exactly the same reason. The
doc block says so at the field, so the permission is not inherited unqualified.

This is stated as a rule for the new field only. That `attachment_chunk`'s own id is likewise
unchecked at admission today is pre-existing and **out of scope here** — § Attachments already
publishes that gap under *The `attachment_id` shape*, and closing it is an admission-time check no
ticket in this family has claimed.

**No code is named for exceeding the bound**, following `ErrUnknownUpload`'s precedent of declining
to publish a mapping this package does not own — that is #2038's decision, at the point something
first counts the list.

### AC 4 — the id-does-not-resolve code: **widen `attachment.not_found`**

The decision this slice owns. A `send_message` naming an id that does not resolve under the
message's own conversation is answered **`attachment.not_found`**, whose published wording widens
from retrieval-only to cover both verbs. Rejected: minting a code for this path.

- It is **literally the same predicate** — an attachment id did not resolve to a file inside the
  named conversation's directory. Only the asking verb differs, and `in_reply_to` already tells a
  client which verb it sent.
- The existing merge rationale is a **disclosure** decision — one code across an unknown id, a
  non-canonical id and an id resolving outside the directory, so the verb is not a path-existence
  oracle for a traversal probe. That reasoning applies with *more* force here, because
  `send_message` is the cheaper probe of the two.
- Retryability (`no`), disclosure posture (a **static** message echoing neither the requested id nor
  any resolved path) and client repair (re-list the conversation's attachments, then re-upload or
  drop the reference and resend) are identical across both verbs. A second code would carry no
  information a client could act on differently.
- The family's published stance on this outcome is already "one code for every such outcome".

Two things the widened row must say, because a list makes them newly reachable:

- The message **must not name which** of several ids failed. A per-id answer turns one message into
  a batch existence-probe for up to 32 ids; the static message is what keeps the merge intact when
  the request names many.
- Resolution is confined to **the message's own conversation** — the one the authenticated session
  is already on, never a client-asserted one, the property `attachment_chunk`'s missing
  `conversation_id` already establishes for the upload leg.

`codes.go`'s `attachment.*` comment block and the § Error codes row both carry the retrieval-only
wording today and both change, so they end up agreeing. **No new constant**, therefore no
`compat_test.go` entries — its two `CodeAttachment*` maps are unchanged.

### Publication points (`docs/protocol-mobile.md`)

1. **§ Application message types** — the `send_message` row's Notes cell, empty today, gains the
   field, its element shape, the bound, the empty-case form and a link into § Attachments.
2. **§ Attachments** — a new `##### Naming a message's attachments` subsection under the existing
   `##### The `attachment_id` shape`: the key, the field table row, the bound with its derivation,
   the three-empty-forms rule, the not-found code, and the explicit "nothing produces, consumes or
   validates this yet — #2038 does" fence this section already writes for every declared-ahead frame.
3. **§ Error codes** — the `attachment.not_found` row, widened per above.
4. **§ Changelog** — one dated entry, `2026-09-02`.

## Concurrency model

None. `internal/protocol` is a stdlib-only leaf data package: pure structs and their
(de)serialization, no goroutines, no shared state, no locks. `UnmarshalJSON` has a pointer receiver
and writes only through it, so it mutates the caller's own value and nothing else — the
data-race argument `QuestionShownPayload.MarshalJSON` records against a *value* receiver sharing a
backing array does not arise in the decode direction, since the slice is freshly allocated by
`encoding/json` per call and never aliased into caller state.

## Error handling

- **Decode failure** — `UnmarshalJSON` returns `encoding/json`'s error unwrapped to its caller. It is
  never swallowed into an empty list. The one production caller, `SendMessage`, already answers
  `protocol.malformed` on it and logs the error without the raw payload.
- **`null` payload / `null` field** — clean decode to the no-attachments case, not an error.
- **No validation ships here**: a non-canonical id, a duplicate id, an over-bound list and an id
  naming nothing all decode successfully. Each is #2038's to reject, and the doc says so at the
  point it publishes each rule rather than leaving a reader to infer it.

## Testing strategy

Six tests in `internal/protocol/messaging_test.go`, one new fixture. Each is stated with the mutant
it kills, because a pin that survives its own mutation is decoration.

- **`TestSendMessagePayload_RoundTrip`** (existing, extended) — `send_message.json` keeps its
  three-key form as the **absent-key regression case** (AC 3), matching the
  `question_shown.json` / `question_shown_empty.json` paired-fixture convention. Adds an assertion
  that the absent key decodes to a `nil` slice. *Kills:* dropping `omitempty` — the re-marshal then
  emits `"attachment_ids":null` and the byte-equivalence fails.
- **`TestSendMessagePayload_Attachments_RoundTrip`** (new) — new fixture
  `testdata/send_message_attachments.json`, a populated two-id list, decoded and re-marshalled
  byte-equivalently. *Kills:* a wire-string typo or rename of `attachment_ids`. Per the drift-detector
  lesson this is the **only** shape that can: the `compat_test.go` registries key on Go symbols.
- **`TestSendMessagePayload_WireKeys`** (new) — marshals a freshly populated struct, asserts the key
  set is exactly the four. *Kills:* an undeclared field added later, and survives a fixture
  regeneration that would turn the round trip green again.
- **`TestSendMessagePayload_ZeroValue_KeyAbsent`** (new) — marshals the zero value, asserts
  `attachment_ids` is **absent** while the other three are present. This is AC 2's empty-case
  wire-form pin, the inverse of `TestAttachmentStoredPayload_ZeroValue_KeysPresent`, and it exists
  for that test's recorded reason read the other way: a key-set assertion over a *populated* value
  stays green when the `omitempty` posture changes. *Kills:* removing `omitempty`.
- **`TestSendMessagePayload_EmptyForms_Indistinguishable`** (new) — table over the three wire forms
  (key absent, `null`, `[]`), each asserting the decoded slice is **`nil`** and `len == 0`. The
  `== nil` half is what makes AC 2 structural. *Kills:* removing `UnmarshalJSON` — the `[]` row then
  decodes to an empty non-nil slice and fails, while the other two rows still pass, so the test also
  reports *which* form regressed.
- **`TestSendMessagePayload_AttachmentIDsIllTyped_Rejected`** (new) — table over a string, an object
  and a number under `attachment_ids`; each must return a decode error. *Kills:* an `UnmarshalJSON`
  that swallows its error and yields an empty list, which would read as "names no attachments" on
  garbage.

Verification gate (§ B2): `go test -race ./internal/protocol/...`, `go vet ./...`,
`go build ./cmd/pyry`. Nothing outside `internal/protocol` changes behaviour, and the field is
additive and optional, so no consumer needs a simultaneous update.

## Open questions

1. **Does the bound belong in `attachments.go` beside the other three, or in `messaging.go` beside
   the field it bounds?** Leaning `messaging.go`: it bounds a `send_message` field, not an
   `attachment_chunk` one, and the three in `attachments.go` share one arithmetic derivation
   (the envelope cap) that this one deliberately does not use. Resolve at implementation; record in
   `## Revisions` if it lands in `attachments.go` instead.
2. **Should `[]` and `null` be published as merely *tolerated*, or as equally canonical?** Leaning
   tolerated-but-normalised — "send no key; a receiver also accepts `null` and `[]` and cannot tell
   them apart" — which keeps one canonical encoding without making a conforming client of anyone
   who sends the other two. Resolve when writing the § Attachments subsection.

## File-overlap check (§ A2)

`git fetch origin --prune` then a scan of every `origin/feature/<n>` branch against the four files
this plan touches returned one hit: `origin/feature/449` touches `internal/protocol/codes.go`.
**Not a blocker.** Issue #449 is CLOSED (2026-05-17), has no PR in any state, and its branch head
predates `main` by three and a half months — an abandoned leftover that will never merge, so it
cannot conflict at integration time. The check is branch-based precisely to catch *in-flight* work
with no PR yet; a closed ticket's stale branch is a false positive, and blocking on it would set a
`blockedBy` that is already CLOSED.

## Split-depth check (§ A1)

`parent 1745 grandparent 1684` — at the depth cap, so a split is off the table regardless. It does
not arise: the size check passes on every line of the table (2 production source files, ~600 lines
total written work, **0** new exported types, **0** consumer call sites needing simultaneous update,
4 acceptance criteria, 0 new reject branches — the one code decision widens an existing row). No
`needs-human:sizing` label, because no split was proposed.

## Security review

**Verdict:** PASS (first pass FAILed on three MUST FIX findings; the plan above is the revision, and
this section records the re-run.)

**Findings:**

- [Trust boundaries] **MUST FIX — fixed.** The first draft placed the "an element becomes a path
  component" rule only in the Files-read entry and in `docs/protocol-mobile.md`, leaving the Go
  declaration silent. #2038's implementer reads the declaration, sees `[]string`, and the natural
  next move is `filepath.Join(convDir, id)` — a traversal, since nothing in this slice checks
  anything and `MaxAttachmentIDBytes` fits `../../../../etc/passwd` several times over. The plan now
  requires the field's doc block to carry the claim/validate-before-`Join` rule naming
  `conversations.ValidID`, mirroring `AttachmentChunkPayload`'s block — see *Every element is a
  claim*. Type-system signalling was considered and rejected with a reason (the family carries this
  hazard on plain `string` with a doc block), rather than left unaddressed.
- [Threat model, § Security model threat 1 — prompt injection] **MUST FIX — fixed.** The draft
  reasoned about the elements as future *path* components only. They are also future *prompt*
  content: #2038 composes claude's input from them, and unlike `attachment_stored` — which is exempt
  because it carries no client-authored byte — every element here is client-authored, so threat 1
  lands squarely. A shape-validated canonical id draws from `[0-9a-f-]` and cannot carry injection
  text; an unvalidated one reaches claude verbatim. The plan now states that the one shape check
  answers both hazards, which is what makes it non-optional for a consumer that never touches the
  filesystem.
- [Tokens / capability] **MUST FIX — fixed.** Confinement to the message's own conversation appeared
  only as a note on the `attachment.not_found` row, where it reads as "when to emit this code" rather
  than as an authorization boundary. Without it the field is "name any id, get its bytes into your
  prompt" — which promotes a published non-capability into a capability. It is now published as a
  security property in its own right. The plan also adds a negative requirement: this slice must
  **not** argue UUIDv4-therefore-unguessable, since an entropy claim would promote the id from the
  other direction.
- [Error messages, logs] **SHOULD FIX — addressed in the plan; the verifier should check it landed.**
  `attachment_chunk`'s "log the attachment id" permission is written for a value that has been
  shape-checked; inherited unqualified it authorises logging an arbitrary client string into a
  line-oriented log — the log-injection shape the family already forbids for `filename`. The doc
  block now qualifies the permission to post-validation elements. The pre-existing equivalent gap on
  `attachment_chunk`'s own id is named **out of scope** (§ Attachments already publishes it; no
  ticket owns the admission-time check) rather than fixed here, per § Scope Discipline.
- [File operations] **SHOULD FIX — addressed.** No path is built in this slice. But a list may repeat
  one id 32 times, and a client reading "at most 32 attachments" may assume the receiver dedups. The
  bound is now published as counting **elements, not distinct ids**, so #2038 decides deduplication
  deliberately. TOCTOU, file modes, symlinks and atomic writes are not applicable — this package
  performs no file operation; `attachments.EnsureDir` owns symlink resolution and #2038 inherits it.
- [Network & I/O] **No finding, and the plan is constrained not to overstate one.** The bound is real
  and required by AC 1, but the envelope cap already limits an unbounded list to ~1680 elements and a
  paired device may already spawn claude turns — far more expensive than 1680 directory resolutions.
  So the bound is contract clarity and bounded work, not a new DoS defence, and the plan says so;
  dressing it as a mitigation is how a later reader concludes the path is guarded when it is not.
  Timeouts, TLS, slow-loris and connection caps are `internal/transport`'s and unchanged.
- [Error messages — decode path] **No finding.** `encoding/json`'s errors embed the field path and the
  *kind* of the offending value (`json.UnmarshalTypeError.Value` is a description such as "string"),
  never the raw payload bytes, so the error `SendMessage` logs satisfies `QuestionAnswerPayload`'s
  published rule that a decode error must not embed remote-authored bytes. The `UnmarshalJSON` also
  propagates that error rather than yielding an empty list, so garbage is a rejected frame and not a
  silent "names no attachments" —
  `TestSendMessagePayload_AttachmentIDsIllTyped_Rejected` is the pin.
- [Concurrency] **No finding.** Leaf data package: no goroutines, no shared state, no locks. The
  pointer-receiver `UnmarshalJSON` writes only through its receiver, and `encoding/json` allocates
  fresh strings rather than aliasing the caller's buffer, so the decoded slice shares nothing with
  the input bytes.
- [Subprocess / external command execution] **No finding.** Nothing in `internal/protocol` execs.
  Downstream the elements reach claude's **stdin as prompt text, never argv and never a shell** —
  which is the prompt-injection surface covered above, not a command-injection one.
- [Cryptographic primitives] **No finding.** No RNG, no hashing, no comparison against a secret. Ids
  are client-minted and this slice asserts no entropy property — deliberately, per the capability
  finding above.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — Phase B

**Open question 1 resolved: the bound lives in `messaging.go`.** As leaned. It bounds a
`send_message` field, not an `attachment_chunk` one, and the three constants in `attachments.go`
share an envelope-cap derivation this one deliberately does not use. No design change.

**Open question 2 resolved: `[]` and `null` are published as tolerated-but-normalised.** As leaned.
§ Attachments states the key is omitted when a message names no attachments, that a receiver also
accepts `null` and `[]`, and that it cannot tell the three apart — one canonical encoding without
making a conforming client of anyone who sends the other two. No design change.

**A seventh test was added beyond the plan's six:
`TestSendMessagePayload_MaxAttachmentIDs_FitV2EnvelopeCap`.** The plan asserted the bound's headroom
arithmetic in prose and left it unchecked. That is the half of the `MaxAttachmentChunkBytes` idiom
the ticket points at which the plan had dropped — that constant's own comment records that its
budget table is *enforced* by `TestAttachmentChunkPayload_FitV2EnvelopeCap`, "which measures the real
total rather than trusting this table". The new test mirrors it: it marshals a `send_message` naming
`MaxAttachmentIDsPerMessage` canonical ids and asserts the envelope lands under a **tenth** of the
cap, not merely under it — a bare "fits" would still pass at a bound of 1000 ids, which is the mutant
it exists for. Measured: 1460 B, 2.2% of the cap.

**Correction to the plan's testing strategy — `TestSendMessagePayload_RoundTrip` is NOT an
`omitempty` pin.** The plan claimed dropping `omitempty` would redden it via the byte comparison.
It does not, and the claim was wrong rather than imprecise: that test re-marshals the *envelope*
while `env.Payload` still holds the fixture's own raw bytes, so the payload struct's tags never run.
Established by mutation rather than by reading — an overlay build with the `omitempty` removed
reddens `TestSendMessagePayload_ZeroValue_KeyAbsent` **alone**. `ZeroValue_KeyAbsent` is therefore
the single `omitempty` pin in the file, which is exactly the load AC 2 assigned it, and the test's
own comment now says so, so nobody weakens it believing the round trip overlaps it.

**Mutation results (overlay, both mutants written and killed):**

| Mutant | Killed by | Notes |
|---|---|---|
| drop the `[]`→`nil` collapse in `UnmarshalJSON` | `EmptyForms_Indistinguishable/empty_array` | Only that subtest reddens — the failure names which of the three forms regressed, as designed. |
| drop `omitempty` from the field tag | `ZeroValue_KeyAbsent` | Only that test. Source of the correction above. |
