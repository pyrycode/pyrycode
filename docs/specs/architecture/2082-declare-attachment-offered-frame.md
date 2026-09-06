# #2082 — declare the frame that announces a host-produced attachment

Wire vocabulary only: the constant `TypeAttachmentOffered`, its payload
`AttachmentOfferedPayload`, the four registry entries the build forces, one
golden fixture, and the document that publishes them. Nothing emits, accepts or
dispatches the frame when this lands; the producer is #2083.

## Files read

- `internal/protocol/codes.go` → `TypeAttachmentChunk`, `TypeAttachmentStored`,
  `TypeRequestAttachment`, `TypeBackgroundTaskStarted` blocks. The first three are
  the family this constant's own block has to agree with;
  `TypeAttachmentStored`'s is where the "outbound-only, no inbound leg, entry
  never moves" reasoning is argued, and `TypeRequestAttachment`'s is the block
  whose `excludedTypes` paragraph I must NOT copy — it filed `"pending handler"`
  because it is inbound, and this frame is not.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload`,
  `AttachmentStoredPayload`, `RequestAttachmentPayload`, and the
  `MaxAttachmentFilenameBytes` / `MaxAttachmentIDBytes` const block. The three
  siblings the new payload lands beside; `AttachmentChunkPayload`'s SECURITY
  block is where `Filename`'s "display string and sanitiser input, never a path"
  and NEVER LOGGED rules live, and both bind this frame unchanged.
  `MaxAttachmentFilenameBytes` is the 255-byte ceiling AC 1 documents against
  rather than minting a second filename rule; its doc block records that it is a
  contract with no validator in this package.
- `internal/protocol/messaging.go` → `ModalShownPayload`, `MessagePayload`.
  `ModalShownPayload.ConversationID`'s field comment (*"outbound routing/scoping
  key; daemon-asserted, client filters on it"*) is the precedent AC 4's
  delivered-to-every-client sentence is drawn from, verbatim in posture.
  `MessagePayload` is the shape the ticket's Context rules out widening — it is
  four fields, and #2115's `newOperatorMessageHistory` writes it to the durable
  log rather than pushing it.
- `internal/protocol/attachments_test.go` → `TestRequestAttachmentPayload_RoundTrip`,
  `TestRequestAttachmentPayload_WireKeys`,
  `TestRequestAttachmentPayload_ZeroValue_KeysPresent`,
  `TestAttachmentStoredPayload_ZeroValue_KeysPresent` — the three test shapes this
  slice replicates, and the fourth is the one AC 3 names as the reason a key-set
  assertion over a populated value is not sufficient on its own.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope` — it re-marshals
  the decoded payload struct and compares **canonical bytes**, which is what makes
  AC 3's pairwise-distinct requirement load-bearing: a field-reordering mutant
  re-encodes identically whenever two swapped keys share a value.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`,
  `TestTypeConstants_V1V2Partition`, `TestInboundAppTypeSet_CoversAllExportedTypeConstants`
  — three registries that gain an entry and one whose fixed length of 23 must not
  move.
- `internal/protocol/envelope.go` → `Envelope`, `inboundAppTypeSet`,
  `IsKnownAppType`. The set this constant must stay out of, the function that must
  reject it, and the envelope shape the fixture is written against — `InReplyTo`
  and `EventID` are both pointer + `omitempty`, so an unsolicited push fixture
  simply omits them.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`,
  `TestEveryInboundV2TypeHasHandler`. Assertion #3 fails an *unclassified*
  constant from the moment it exists; the `TypeBackgroundTaskStarted` block is the
  entry shape AC 2 points at.
- `docs/protocol-mobile.md` § Attachments, § Application message types,
  § Security model → Threats → threat 1, § Changelog — the four places this slice
  writes, plus the stale sentence AC 5 requires correcting.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — four lessons
  that shape the test plan below and would otherwise be re-learned. (1) None of
  the three `compat_test.go` registries can catch a wire-**string** typo; only a
  committed fixture can, *confirmed by mutant run on #1895*. (2)
  `TestTypeConstants_V1V2Partition` does not itself fail on an unpartitioned
  constant — `all` and `v2OnlyTypes` are hand-written literals, so the detector
  that actually reddens is `relay_guard_test.go`'s Assertion #3, which reads the
  AST of `codes.go`. (3) A complete-key-set test outlives a fixture regeneration
  that a round trip does not. (4) The classic `env`-round-trip shape never calls
  `json.Marshal` on the payload struct at all, so it exercises no struct tag —
  only `roundTripEnvelope`, a `WireKeys` assertion or a `ZeroValue` case does
  (*measured by overlay mutant on #2036*).
- `docs/knowledge/features/protocol-package-constants-codes-go-envelope-types-attachments.md`,
  `.../protocol-package.md` — the package's standing posture: stdlib-only leaf
  data package, wire vocabulary, no producer, no consumer, no validator.

## Context

The attachment transfer is complete in both directions with one hole: **a client
can only ever name a file it minted itself.** Upload lands and is acknowledged
(#1895–#1898), a message declares which attachments it carries (#2036, #2038),
and retrieval finished on 2026-09-03 (#2052, #2053, #2054). Every one of those
needs an `attachment_id` the client already holds. Nothing on the wire hands a
client an id it did not mint, so a file the assistant produced reaches a client
as nothing at all — pyrycode-desktop#1028's words, with #815 and #868 parked on
it.

This slice declares the announcement frame and publishes it, and nothing more.
It is the declare-then-implement step this family has taken three times (#1752
before #1897, #1895 before #1897, #2052 before #2054): the wire string is the
contract, the desktop client writes its decoder against the published name, and
a name chosen twice is a name chosen wrong once. The producer — which is also
where the file itself comes from — is #2083.

**Two obvious moves are wrong, and the ticket re-derived both against `4c5f3c87`.**
Widening `MessagePayload` announces nothing at the moment a file appears: the
live assistant reply on the v2 interactive lane is a stream of `assistant_delta`
closed by `turn_end`, and `message` has exactly one producer
(`newOperatorMessageHistory`, #2115) which hands it to `appendConversationHistory`
— a write to the durable log, not a push. It would also change a record shape
`history_page` decoders already read. And there is no list verb, so a client
cannot ask what a conversation holds either.

**No ADR is warranted.** Every decision here either follows a published precedent
in § Attachments / § Modal or is recorded in the constant's own doc block; there
is no rejected alternative design a future reader would need the ADR form to
reconstruct.

### Size (§ A1 / § A4), stated rather than elided

Re-counted against this written plan: **2 production source files**
(`internal/protocol/codes.go`, `internal/protocol/attachments.go`) against a
ceiling of 5; **2 new exported symbols** (one constant, one type) against 5; **4
consumer call sites** (three registries in `compat_test.go`, one in
`relay_guard_test.go`) against 10; **5 acceptance criteria** against 5; **0
reject branches**, since this package validates nothing.

**Total written work is over the 800-line ceiling** — the refiner estimated ~900
and the two nearest analogues measured 839 (#2052) and 979 (#2036), with ~45% of
each sitting in the spec doc rather than in code. It stays one ticket. The only
available cut is the constant from its sole consumer, the payload it types:
neither half changes anything observable alone, so the one-consumer floor
forbids it, and **the floor wins over the ceiling by rule**. #2052 shipped its
839 without exhausting a builder leg.

### File-overlap check (§ A2)

One remote branch touches a file this slice edits: `origin/feature/449` →
`internal/protocol/codes.go`. Its ticket (#449, the v2 re-key responder path)
closed 2026-05-17, the branch has no PR and no commit since 2026-05-17, and it is
already contained in no other ref. It is a stale branch of closed work, not
in-flight, so it can never reach integration and no blocker is set. No other
feature branch touches `codes.go`, `attachments.go`, `attachments_test.go`,
`compat_test.go`, `relay_guard_test.go` or `protocol-mobile.md`. (#2052 recorded
the identical finding for the identical branch.)

## Design

### 1. The constant — `internal/protocol/codes.go`

```go
const (
	TypeAttachmentOffered = "attachment_offered" // binary → phone, unsolicited push: a file exists on the host for this conversation
)
```

Its own `const (...)` block with a doc block, placed after
`TypeRequestAttachment`'s so the attachment family stays contiguous. The block
records, in the family's own idiom:

- **The name is fixed here rather than in the producer**, #2052's reason. The
  past-participle form follows `attachment_stored`. **"Offered" rather than
  "sent" is load-bearing**: no bytes ride this frame, and a client that wants
  them asks with `TypeRequestAttachment` and gets `TypeAttachmentChunk` back.
- **It is a PUSH, not a reply**, and that is one decision expressed in three
  places that must agree: this block, the § Application message types row, and
  `excludedTypes`. Nothing solicits it, so there is no request envelope for
  `InReplyTo` to name — the difference from `TypeAttachmentStored`, which is
  outbound-only too but correlates to the chunk that completed the transfer.
  `TypeModalShown` is the nearest analogue on this count and on correlation.
- **It must NOT be added to `inboundAppTypeSet`**: an old (v1) phone never
  receives it, and `IsKnownAppType` rejecting it with `ErrUnknownType` is the
  structural bar against a v1 client *sending* one into `dispatch.Route` and
  asserting a file exists that does not.
- **Filing is mandatory from the moment the constant exists**, not from the
  moment something emits it — Assertion #3 of `TestEveryInboundV2TypeHasHandler`
  reports an unclassified constant, not an unemitted one. And because this frame
  has no inbound leg at all, the entry is `"push"` **permanently** and never
  moves to `inboundTypes`, unlike `TypeRequestAttachment`'s did.

### 2. The payload — `internal/protocol/attachments.go`

Contract, placed after `RequestAttachmentPayload` so the file's four types read in
the family's own order:

```go
type AttachmentOfferedPayload struct {
	ConversationID string `json:"conversation_id"`
	AttachmentID   string `json:"attachment_id"`
	Filename       string `json:"filename"`
}
```

Three fields, no `omitempty`, no `MarshalJSON`. Behaviour: a decoder may rely on
all three keys being present on the wire; a truncated or hostile payload decodes
to three empty strings rather than to an error, and the empty string is a valid
value for none of them. The invariant is asserted by
`TestAttachmentOfferedPayload_ZeroValue_KeysPresent` (key presence) and
`TestAttachmentOfferedPayload_WireKeys` (the complete key set).

The doc block records, per field and then per property:

- **`ConversationID` is the whole of the correlation, following
  `ModalShownPayload`.** It is a daemon-asserted routing/scoping key the client
  **filters on**; the daemon delivers the frame to every attached client rather
  than routing it to one. That is the same origin story: a permission prompt also
  begins as an MCP tool call from claude, arrives over the control socket, and is
  broadcast to attached clients from the control-server handler goroutine.
- **There is no `turn_id`, and the omission is a property rather than an
  oversight.** It is the field a reader reaches for first and it is **not
  reachable from that lane** — it is private state on the interactive emitter,
  minted at turn start, and nothing on the control path references it.
  Publishing it would either oblige the producer to build a seam nobody has asked
  for or ship a field that is empty in practice. The consumer does not need it:
  pyrycode-desktop's timeline already appends items carrying no turn id at all,
  its own user messages and session boundaries among them, in arrival order.
- **`AttachmentID` obeys the published `attachment_id` shape** — the lowercase
  UUIDv4 rule § Attachments publishes, `conversations.ValidID`'s shape byte for
  byte — rather than a rule minted here. Mentions `MaxAttachmentIDBytes` only to
  say what it is not: a ceiling for `attachment_chunk`'s envelope arithmetic, not
  the shape. **Receiving the frame is not a capability**: the id is not secret,
  not unguessable, and #2054 re-validates it against the daemon's own registry
  regardless of what was announced, so an announcement grants nothing.
- **`Filename` is display text, never a path**, the posture
  `AttachmentChunkPayload.Filename` already carries, bounded by the same
  `MaxAttachmentFilenameBytes` (POSIX `NAME_MAX`, 255 bytes) rather than a second
  filename rule. It is the first string in this family that **originates from
  `claude`** — see the threat-1 paragraph below.
- **`Filename` IS NEVER LOGGED RAW, and this frame is therefore NOT safe to log
  whole.** `AttachmentChunkPayload`'s SECURITY block puts that rule on `Filename`
  for two independent reasons — a filename is often private in itself, and a
  string in a line-oriented log is a log-injection shape — and here **both are
  stronger, not weaker**: the string is `claude`-authored, and nothing on this
  path strips control characters or terminal escape sequences. The block states
  the rule positively because the natural reading is the opposite one:
  `AttachmentStoredPayload` is published as **safe to log whole** precisely
  because it carries no filename, so a reader who takes this frame for that
  frame's twin logs a subprocess-authored string verbatim. `ConversationID` and
  `AttachmentID` are ids and stay loggable once their shape is validated — the
  family's existing rule, *log the attachment id, never a raw filename*.
- **No count and no length field, so `NEVER ALLOCATE FROM A CLAIM` has nothing to
  bite on**, and no bound of this type's own is minted. Whether one turn may
  offer several files, and any limit on that, belongs to the producer and is
  learned by being rejected — #1752's rule.
- **Nothing here enforces any of it.** `internal/protocol` declares shapes and
  validates none; the two documented ceilings belong to the producer and to each
  consumer.

### 3. The four registry entries

| Site | Entry | Why |
|---|---|---|
| `TestIsKnownAppType` cases (`compat_test.go`) | `{"attachment_offered-rejected", TypeAttachmentOffered, false, ErrUnknownType}` | outbound-only v2 type; an old phone never receives it, and rejection also bars a phone from sending one |
| `v2OnlyTypes` (`compat_test.go`) | `TypeAttachmentOffered: true` | the partition's v2 half |
| `TestTypeConstants_V1V2Partition`'s `all` (`compat_test.go`) | `TypeAttachmentOffered` | the partition's input list |
| `excludedTypes` (`cmd/pyry/relay_guard_test.go`) | `"TypeAttachmentOffered": "push"` | outbound-only, no inbound leg, so the entry is permanent |

`inboundAppTypeSet` is the **fifth** registry and must not gain it; its `all`
list asserts a fixed length of 23 and that number does not move. Its size
assertion is `len(inboundAppTypeSet) + len(v2OnlyTypes) == len(all)`, so all
three `compat_test.go` edits must land together or the partition test fails on
the arithmetic.

The `excludedTypes` entry is placed **beside `TypeAttachmentStored`'s** rather
than in the anonymous `"push"` block, so the attachment family's two outbound
frames read together, and its comment states the one difference: `attachment_stored`
is a `"reply"` because correlation rides `in_reply_to`; this one is a `"push"`
because nothing solicits it.

### 4. The fixture — `internal/protocol/testdata/attachment_offered.json`

One line, following `attachment_stored.json`'s convention: an `Envelope` with
`id`, `type`, `ts` and `payload`, and **no `in_reply_to` and no `event_id`** —
both are pointer + `omitempty`, and an unsolicited push omits them, exactly as
`modal_shown.json` does. Whether the producer stamps an `event_id` is #2083's
call and is not a property of the declared shape.

The three payload values are **pairwise distinct**, which AC 3 requires and
`roundTripEnvelope` is the reason for: it compares canonical bytes, so a
field-reordering mutant re-encodes identically whenever two swapped keys share a
value. The ids are placeholders whose value and length are not a contract; both
conform to the published `attachment_id` shape so the fixture cannot be read as
licence to ignore it, and the filename is a plain display name.

### 5. The document — `docs/protocol-mobile.md`

Four writes, all additive except one corrected sentence.

**(a) A `#### attachment_offered` subsection under § Attachments**, placed after
`#### request_attachment` (the family's chronological order). Per-field table
with a **Provenance** column, following `attachment_stored`'s table — but
**provenance here is per field, not one blanket verdict**, which is § Question's
rule (*"Provenance is per field"*) rather than `attachment_stored`'s. Its table
carries one row and says *daemon-asserted*; copying that verdict across all three
rows here would publish `filename` as trusted chrome while it carries a
`claude`-authored string, which is exactly the defect § `question_dismissed`
warns about. So: `conversation_id` and `attachment_id` are **daemon-asserted**,
`filename` is **`claude`-authored**, marked in the column and not left for a
reader to infer from the security paragraph.

The prose states the four things a reader gets wrong by default:

1. **No bytes ride this frame.** It announces; the client fetches with
   `request_attachment` and receives `attachment_chunk` frames. This is the whole
   reason the name is *offered* and not *sent*.
2. **Receiving it is not a capability.** #2054 re-validates the id against the
   daemon's own registry regardless of what was announced, so the frame grants
   nothing an unannounced id would not already have.
3. **It is delivered to every attached client, not routed to one**, scoped by
   `conversation_id` **at the consumer** exactly as `modal_shown` is. A decoder
   author would otherwise assume the daemon routes it.
4. **The trust statement**, which the section's own convention requires of every
   frame — below.

**(b) The threat-1 statement, written for this direction rather than copied.**
§ Attachments says of each frame whether § Security model's **threat 1 (prompt
injection)** lands: `attachment_stored` carries no client-authored and no
claude-authored byte so it does not; `send_message`'s `attachment_ids` reach
`claude` so it lands squarely; `request_attachment` becomes no prompt content so
it does not. **This frame is the first in the section whose string originates
from `claude`** — #2083 takes the name from the model's own tool call — so
threat 1 **does land**, in the *inverse* direction from `send_message`'s: the
injected string does not flow into `claude`, it flows **out of** `claude` toward
the client's UI. That is `question_shown`'s direction, and § Question's wording
for it is the register to follow.

The doc binds § Attachments' existing consumer MUSTs on `filename` **unchanged** —
sanitise before rendering, never use as a path or any part of one, never trust as
a description of the bytes — since *"a sanitised filename is still
attacker-shaped"*. Whether the producer sanitises before announcing is #2083's to
decide and changes none of this.

Two consequences are drawn out concretely, because each is a way a client
discharges the MUSTs in letter and breaks them in spirit. **An extension is not
evidence of content**: a client that picks a viewer or a handler from `.html` or
`.svg` in this name has dispatched on a `claude`-authored string, which is the
hazard `mime_type`'s never-dispatch rule already names one field over. And **the
frame is not safe to log whole** — the payload block's rule, published here too,
because `attachment_stored`'s *safe to log whole* sits four subsections up and is
the sentence a reader will carry over.

**One byte bound is the producer's and is named as such.** `filename` is
documented against `MaxAttachmentFilenameBytes` and **nothing checks it here**;
unbounded, a `claude`-authored 100 KB name makes the **daemon's own outbound
frame** exceed the envelope cap and be dropped — the self-inflicted availability
bug `MaxAttachmentFilenameBytes`' block already records for the retrieval leg.
#2083 enforces it.

**(c) A row in § Application message types**, after the `request_attachment` row
so the family stays contiguous: direction `binary → phone`, no handshake
early-data, Notes naming what it announces, that no bytes ride it, that nothing
emits it yet (#2083), and a link to § Attachments.

**(d) The one correction AC 5 requires.** § Attachments currently claims *"The
section now publishes every frame in the transfer"* — that predates this frame
and is now false. It is rewritten to name this frame as the announcement half.
**Out of scope, and deliberately untouched:** the section's other stale claims
(*"Nothing emits, accepts or enforces any of this yet"* and *"Nothing answers it
yet"*) went stale when #2053/#2054 landed for reasons unrelated to this frame.
Correct only the sentence this frame itself falsifies; do not audit the section.

**(e) A dated § Changelog entry appended** at the head of the list (entries are
newest-first). The existing dated entries are **not** rewritten.

## Concurrency model

**None.** `internal/protocol` is a stdlib-only leaf data package: wire vocabulary,
no producer, no consumer, no validator, no goroutine, no shared state. This slice
adds one string constant and one struct with three string fields. There is
nothing to synchronise and no shutdown path to define.

The one concurrency property worth recording is the producer's, and it is
#2083's: the frame originates on the control-server handler goroutine (the lane
`modal_shown` already broadcasts from), not on the interactive turn emitter,
which is precisely why no `turn_id` is reachable. Stating it here so the producer
does not read the absent field as an oversight to repair.

`QuestionShownPayload.MarshalJSON`'s backing-array data-race argument does **not**
transfer: it exists for a slice field, and this payload has none. Do not add a
`MarshalJSON` by analogy.

## Error handling

**No failure modes to handle, and that is a design property rather than a gap.**
This package declares shapes and validates none, the posture all three siblings
ship with. Concretely:

- **No admission-time check.** Nothing here checks the `attachment_id` shape, the
  `filename` byte ceiling, or that the conversation exists. Both documented
  ceilings are producer-side and consumer-side contracts.
- **A hostile or truncated payload decodes to the zero value, not to an error**,
  since every key is optional to `encoding/json`. Three empty strings, no error.
  The empty string names no conversation, is not a valid attachment id under any
  published shape, and is not a filename — so a consumer must resolve **nothing**
  from them. The specific silent failure to warn about is the one
  `RequestAttachmentPayload` records: `filepath.Join(dir, "")` is `dir`, so a
  consumer that skips the shape check and joins the zero value addresses the
  conversation directory root rather than erroring.
- **No new reject code is minted.** The producer's failure paths are #2083's, and
  publishing a code ahead of the code that would send it is what § Attachments
  already refuses to do.

## Testing strategy

Three tests in `internal/protocol/attachments_test.go`, mirroring the trio
`RequestAttachmentPayload` ships, each sole-red for a class the others miss.

1. **`TestAttachmentOfferedPayload_RoundTrip`** — reads
   `testdata/attachment_offered.json`, asserts `env.Type == TypeAttachmentOffered`,
   asserts `env.InReplyTo == nil` (this frame is unsolicited, not a reply) and
   `env.EventID == nil`, asserts each of the three payload values, then calls
   `roundTripEnvelope`. **This is the only test that can catch a wire-string
   typo** — all three `compat_test.go` registries key on the Go *symbol* and
   would move consistently under a mutated literal, confirmed by mutant run on
   #1895. `roundTripEnvelope` is also the only helper here that calls
   `json.Marshal` on the payload struct, so it is what exercises the struct tags
   at all (measured on #2036).
2. **`TestAttachmentOfferedPayload_WireKeys`** — marshals a freshly *populated*
   struct and asserts its key set is **exactly**
   `{conversation_id, attachment_id, filename}`, two-sided: every expected key
   present AND no unexpected key. A one-sided containment check is what lets an
   added field through, and that is the mutant class this test exists for. It
   outlives a fixture regeneration the round trip does not: adding a field
   reddens both, but regenerating the fixture under that same change turns the
   round trip green while this stays red. It is also where a `turn_id` arriving
   as a field reddens by construction.
3. **`TestAttachmentOfferedPayload_ZeroValue_KeysPresent`** — marshals the zero
   value and asserts all three `":""` keys are present. This is the **`omitempty`
   pin**, and it must exist separately from both tests above: `omitempty` elides a
   key only at its zero value, and both of those marshal non-empty values, so an
   `omitempty` added to any field leaves them entirely green — including the
   key-set check, which would still see the keys it expects. That is exactly why
   `TestAttachmentStoredPayload_ZeroValue_KeysPresent` exists, and AC 3 names it.
   A marshalled zero value rather than a second fixture, for that test's reason:
   the payload is flat, so one all-zero struct reaches every key.

Plus the four registry entries, which are themselves the drift tests. Verification
per § B2: `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`,
`go build ./cmd/pyry`.

**Mutants to run before committing** (per the package's own recorded lessons —
run over a `go test -overlay` scratch copy, never predicted): `omitempty` on each
of the three keys, three key renames, one field reordering, and one wire-string
typo on the constant. Expected: each turns at least one test red, the reordering
mutant is caught only because the three fixture values are pairwise distinct, and
the wire-string typo is caught by the round trip alone. `relay_guard_test.go`'s
AST-reading guards are **not** overlay-testable (they `parser.ParseFile` the file
on disk at test runtime, #1980) — if that entry needs proving, edit and restore
the real file in one shell invocation.

## Open questions

1. **Where the `#### attachment_offered` subsection sits under § Attachments.**
   Resolved in Design § 5(a): after `#### request_attachment`, the family's
   chronological order, so the retrieval pair stays adjacent. Nothing depends on
   it beyond anchor stability, and the anchor is new either way.
2. **Whether the frame is interactive-capability-gated.** Not declared here, and
   the declaration mints no gate. § Attachments states that authorization is
   **pairing**, enforced structurally at the Noise IK handshake, with no per-verb
   gate on any of the family's three existing frames — this one inherits that and
   invents nothing. Whether #2083's emission path is additionally capability-gated
   is the producer's, and gating an emission does not change a declared shape.
3. **Whether the producer records the offer into the conversation-history log.**
   Explicitly #2083's call, per the ticket. The log (`internal/history`, #2112)
   stores typed envelopes and now holds `message` among them, so the question is
   live — but this slice declares vocabulary and writes no producer. Recorded here
   so the producer does not read the silence as a decision already taken.

Any resolution that departs from the above lands as a `## Revisions` entry in the
same commit as the code that departs.

## Security review

**Verdict:** PASS (first pass FAILED on two MUST FIX; both revised into the plan
above, checklist re-walked from the top.)

**Findings:**

- **[Trust boundaries] MUST FIX — fixed.** The first draft of Design § 5(a) said
  *"every field is daemon-asserted, so there is nothing to disambiguate"*, copied
  from `attachment_stored`'s one-row table. **That is false of `filename`**, the
  one string in this frame that crosses the subprocess boundary from `claude`,
  and publishing it as daemon-asserted moves the frame to a different trust tier
  while its own table still reads trusted — the exact defect § `question_dismissed`
  warns about for `outcome`, arrived at from the opposite direction. Revised:
  **provenance is per field**, § Question's rule — `conversation_id` and
  `attachment_id` daemon-asserted, `filename` `claude`-authored, marked in the
  column rather than left to be inferred from the security paragraph. This is the
  design's single trust boundary and it is now explicit at the field, not the
  frame.
- **[Error messages, logs, telemetry] MUST FIX — fixed.** The first draft's
  payload doc block omitted `AttachmentChunkPayload`'s **NEVER LOGGED** rule on
  `Filename`. Omission is not neutral here: `AttachmentStoredPayload` is published
  as **safe to log whole** precisely because it carries no filename, and it is
  this frame's nearest sibling, so a reader takes the twin's verdict and logs a
  `claude`-authored string verbatim into a line-oriented log that strips no
  control characters and no terminal escape sequences. Revised: the rule is stated
  **positively** in both the payload block and the published section — this frame
  is **not** safe to log whole, `filename` is never logged raw, and the two ids
  stay loggable once shape-validated.
- **[Threat model alignment] No findings.** Threat 1 (prompt injection,
  `severity: high`, `mitigation: partial`) **lands**, in the inverse direction
  from `send_message`'s `attachment_ids` — out of `claude` toward a remote render
  surface, `question_shown`'s direction — and AC 5 makes the doc say so and bind
  the existing consumer MUSTs unchanged. Threats 2 and 3 are untouched: the
  payload rides inside the Noise ciphertext like every other application frame, so
  a relay operator or a server-id racer learns **no new plaintext** from it.
- **[File operations] SHOULD FIX — discharged in the plan.** Nothing in this
  package touches a filesystem, but both string fields are values a consumer will
  reach for as path components. Handled by citing the existing rules rather than
  minting new ones: `attachment_id` obeys the published shape **before** it
  becomes a directory component (containment is a consequence of the shape, never
  of the length ceiling), and `filename` is *never a path or any part of one* —
  the "any part of" strengthening is what forbids the `download_dir + filename`
  join the plain wording invites. The zero-value trap is named in § Error
  handling: `filepath.Join(dir, "")` is `dir`, so a consumer skipping the shape
  check addresses the conversation directory root rather than erroring.
- **[Subprocess / external command execution] SHOULD FIX — discharged in the
  plan.** Nothing here executes anything, and the string never returns to a
  command line. The live hazard is one step past the MUSTs as written: a client
  that obeys *sanitise before rendering* and then picks a viewer or a handler from
  the name's **extension** has dispatched on a `claude`-authored string. Design
  § 5(b) draws that out concretely against the existing *never trust as a
  description of the bytes* MUST — an extension is not evidence of content — so it
  is a consequence made explicit, not a new rule.
- **[Network & I/O] SHOULD FIX — assigned to #2083, named in the plan.** The
  payload carries no count and no length field, so `NEVER ALLOCATE FROM A CLAIM`
  has nothing to bite on and the shape is safe by construction. The one real byte
  question is the producer's: `filename` is documented against
  `MaxAttachmentFilenameBytes` and **nothing checks it here** (this package
  enforces no bounds by design), so an unbounded `claude`-authored name makes the
  **daemon's own outbound frame** exceed the envelope cap and be dropped — the
  self-inflicted availability bug that constant's block already records for the
  retrieval leg. Design § 5(b) states it and assigns it to #2083.
- **[Tokens, secrets, credentials] No findings.** No secret material, no token
  lifecycle, no comparison. `attachment_id` is published as **not a capability** —
  not secret, not unguessable — and #2054 re-validates it against the daemon's own
  registry regardless of what was announced, so an announcement grants nothing an
  unannounced id would not already have. The one property genuinely new here is
  that a client learns an id **it did not mint**, widening what it knows from
  *files I uploaded* to *files this conversation holds*. That is the ticket's whole
  point and it is safe for a stated reason rather than an assumed one: delivery is
  to attached clients, every attached client is a paired device, and § Attachments
  already makes **pairing** the authorization boundary with no per-verb gate — so
  the disclosure is to exactly the tier that could already ask.
- **[Cryptographic primitives] No findings — not applicable, by design.** No RNG,
  no key, no digest, no comparison in this slice. The adjacent decision that would
  invite one is already refused in the published contract: a client **must not
  read "UUIDv4" as a claim of unguessability**, and confinement rather than
  secrecy is what does the work.
- **[Concurrency] No findings — not applicable, by design.** `internal/protocol`
  is a stdlib-only leaf data package: one string constant and one struct of three
  strings, no goroutine, no shared state, no shutdown path. The producer's lane is
  recorded in § Concurrency model so the absent `turn_id` is not read as an
  oversight to repair, and `QuestionShownPayload.MarshalJSON`'s backing-array
  data-race argument is explicitly ruled out as non-transferable (it exists for a
  slice field; this payload has none).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
