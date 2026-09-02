# #1895 — publish the attachment upload's client-visible success contract

Declaration only: one outbound wire type, one single-field payload, the
`protocol-mobile.md` publication, and the four type-classification registries.
No producer, no consumer, no validator. Split from #1744; the dispatch that emits
the frame is #1897, and #1898 observes it end to end.

## Files read

- `internal/protocol/attachments.go` → `AttachmentChunkPayload`, `MaxAttachmentIDBytes` —
  the frame this one answers. Its inbound-claim / outbound-authored asymmetry is
  the line the new payload sits on the far side of, and its never-log rule
  (`Filename`, `SHA256`, `Data`) is what the new payload must not re-open.
  `MaxAttachmentIDBytes`' doc block carries the stale "the shape is #1741's and
  #1743's to pick" sentence this ticket repairs.
- `internal/protocol/codes.go` → `TypeAttachmentChunk`'s doc block, the
  `CodeAttachment*` reject block — the block the new constant is grouped beside,
  and the seven negatives the new positive is the counterpart to.
  `CodeAttachmentStreamAborted`'s trailing comment already commits the family's
  terminal signals to `in_reply_to` correlation.
- `internal/protocol/attachments_test.go` → `TestAttachmentChunkPayload_Upload_RoundTrip`,
  `TestAttachmentChunkPayload_ZeroValue_RoundTrip` — the fixture and byte-guard
  patterns the new tests mirror, including the `InReplyTo != nil` assertion that
  makes a leg structurally rather than merely numerically distinct.
- `internal/protocol/questions.go` → `QuestionDismissedPayload` — #1974, the
  nearest analogue: a flat daemon-authored payload with no `MarshalJSON`, no
  `omitempty`, and a SECURITY block asserting every field is daemon-asserted.
- `internal/protocol/questions_test.go` → `TestQuestionDismissedPayload_RoundTrip`,
  `TestQuestionDismissedPayload_ZeroValue_KeysPresent` — the two-test shape, and
  the argument for why the omitempty pin cannot ride the round trip.
- `internal/protocol/envelope_test.go` → `readFixture`;
  `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `canonical` —
  the helpers every fixture test in this package uses.
- `internal/protocol/envelope.go` → `Envelope` — `InReplyTo` is `*int`, so an
  absent correlation is `nil` rather than zero, which is what lets a fixture pin
  presence.
- `internal/protocol/handshake.go` → `AckPayload` — empty struct. This is why a
  bare `ack` cannot serve as the upload's success reply.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`,
  `TestTypeConstants_V1V2Partition` — the three hand-written registries, whose
  only cross-check is against each other.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `inboundTypes`,
  `TestEveryInboundV2TypeHasHandler` — the one registry enforced from source.
  `excludedTypes` defines `"reply"` as *outbound reply — correlated to a request
  via `in_reply_to`*, which is the definition this ticket's classification has to
  be true against.
- `internal/attachments/storage.go` → `EnsureDir` — the validator that already
  picked the id shape (`conversations.ValidID`), and whose doc block records the
  lowercase-only alphabet as injectivity-preserving on a case-insensitive
  filesystem.
- `internal/attachments/filename.go` → `SanitizeFilename` — records that its
  result is neither unique nor an identifier, which is why the stored filename
  stays out of the reply.
- `internal/conversations/id.go` → `ValidID` — the concrete shape being
  published: 36 bytes, lowercase hex, `-` at 8/13/18/23, `4` at 14, one of
  `89ab` at 19.
- `docs/protocol-mobile.md` → § Attachments (the scope fence, the
  `attachment_chunk` field table), § Application message types (the
  `session_settings_updated` and `attachment_chunk` rows), § Question's
  `question_dismissed` subsection (the four-column Field / Type / Provenance /
  Meaning table this frame copies), § Session settings' `session_settings_updated`
  subsection (the `in_reply_to`-correlated reply precedent), § Error codes'
  `attachment.storage_failed` row, § Changelog.
- `docs/knowledge/features/` — no package overview exists for `internal/protocol`
  or `internal/attachments`; nothing to carry forward from one.

## Context

`docs/protocol-mobile.md` § Attachments publishes the `attachment_chunk` frame
(#1752), its per-chunk raw byte bound (#1753) and the seven `attachment.*` reject
codes (#1751), and then deliberately stops: *"No success frame is declared here,
and a client must not invent one."* A client that uploads today is told what went
wrong on every failure path and gets silence on the one that worked. This slice
discharges the half of #1744 that owed that frame.

The second deliverable in the same sentence is the `attachment_id` shape, and it
is already decided rather than open. `EnsureDir` validates the id with
`conversations.ValidID` before it becomes a path component, and nothing checks
that shape at admission — `Registry.Admit` keys on the raw string — so a client
picking a non-canonical id uploads every chunk and is refused only when the
completing chunk reaches storage. Meanwhile the published field table for
`attachment_chunk` says only *"At most 64 bytes"*, which reads as permission to
use any short string.

No ADR is warranted: this adds no new architectural axis. It is one more frame in
an established family, following the sequencing #1752 → #1751 already used twice.

## Design

### The wire type

`TypeAttachmentStored = "attachment_stored"`, declared in a new const block in
`internal/protocol/codes.go` immediately after `TypeAttachmentChunk`'s.

**Why `attachment_stored`.** The name is the positive of the reject vocabulary's
`attachment.storage_failed`, so the family's terminal outcomes read as one set
rather than a positive invented in a different idiom from its negatives. It also
states what the daemon actually asserts — the transfer completed, its claims were
checked, and the bytes are on the host addressable by that id — which is the
whole content of the reply. `attachment_uploaded` names the *client's* action
rather than the daemon's assertion, and the wire type names what the frame IS to
a client, the naming rule `TypeQuestionShown`'s block records. `attachment_ack`
is rejected outright: `TypeAck` already exists and `AckPayload` is `struct{}`, so
the name would suggest the existing empty frame while carrying a payload.

The `storage_failed` pairing is deliberately *not* read as narrowing. Six other
reject codes also terminate an upload; this frame is the single positive terminal
for the whole transfer, and its doc block says so rather than leaving the pairing
to imply that only the storage step succeeded.

### Correlation and classification — one decision

Filed as **`"reply"`** in `excludedTypes`: correlation rides the envelope's
`in_reply_to`, not a payload field. This is a single decision expressed in three
places that must agree — the guard entry, the § Application message types row,
and the payload's shape — and the guard's own comments call a mismatch here a lie
to the guard.

The reasons, in the order they bind:

1. `session_settings_updated` is the nearest analogue on both counts: a v2
   outbound confirmation of an inbound control frame, classified `"reply"`, and
   published as *correlated by `in_reply_to`*. Its payload carries only the
   `session_id` it confirms and echoes nothing else back, because the client
   already knows what it sent.
2. The reject half of this same leg already went that way.
   `CodeAttachmentStreamAborted`'s comment reads *"a `TypeError` correlated via
   `in_reply_to`, never a second attachment frame"*, and § Attachments publishes
   the same for abandonment. A success correlating differently from the failure
   it is the alternative to would split one leg across two mechanisms.
3. Filing it in `inboundTypes` is structurally impossible — Assertion #1 requires
   an inbound type to be wired into `relay.go`'s `Handlers` map or
   `dispatchAppFrame`, and this slice ships no dispatch — and it is outbound
   anyway.

**What `in_reply_to` points at, and why the payload still carries the id.** It is
the envelope `id` of the chunk **whose arrival completed the transfer**. That is
not necessarily the chunk with the highest `index`: § Attachments publishes that
chunks may arrive in any order and that the transfer is complete when every index
in `[0, total_chunks)` has arrived exactly once, so the completing chunk is
whichever one closed the set. A client cannot predict which of its envelope ids
that will be, so `in_reply_to` alone is not a usable match key for a multi-chunk
upload — which is exactly why `attachment_id` rides the payload. The two are not
redundant: the envelope field says *which frame this answers*, the payload field
says *which transfer this concludes*, and only the second is something the client
chose and can look up. This is published rather than left to be inferred.

### The payload

`AttachmentStoredPayload` in `internal/protocol/attachments.go`, beside the frame
it answers.

```go
// One field. AttachmentID is the client's own id, echoed back.
type AttachmentStoredPayload struct {
    AttachmentID string `json:"attachment_id"`
}
```

- **One field, not two.** Nothing daemon-side mints an attachment id — the client
  supplies it on every chunk, `EnsureDir` validates it, and #1896's storage keys
  by it. "Tie the reply to the upload" and "name the attachment that was stored"
  resolve to the same value, so it is carried once. A second daemon-side handle
  would be a new identifier with no minting site, no lifecycle and no consumer.
- **No `omitempty`, and no `MarshalJSON`.** The field is always present in the
  one direction this frame travels, matching `QuestionDismissedPayload`. There is
  no slice field, so there is no `nil`→`[]` normalisation to perform, and
  `QuestionShownPayload.MarshalJSON`'s backing-array argument does not transfer.
- **No `conversation_id`**, for `AttachmentChunkPayload`'s reason: the upload
  landed in the conversation the authenticated session is already on, and the
  client holding the transfer already knows it.
- **No `size`, no `sha256`, no `total_chunks`.** The client sent all three and
  they were checked against the assembled bytes before this frame is emitted;
  echoing them back confirms nothing a client could act on.
- **No host path, no directory component, no stored filename.** `SanitizeFilename`'s
  doc block records that its result is neither unique nor an identifier —
  distinct client names collide, and a case-insensitive host folds them further —
  so echoing it would hand a client something it cannot rely on. Retrieval
  (#1746) addresses by `attachment_id`, and the client already knows the name it
  sent. § Error codes already forbids `attachment.storage_failed` from carrying
  the host path; the success frame must not be the leak the failure frame is
  guarded against.

### Registries

Four, three of them hand-written:

| Registry | File | Entry |
|---|---|---|
| `excludedTypes` | `cmd/pyry/relay_guard_test.go` | `"TypeAttachmentStored": "reply"` |
| `TestIsKnownAppType` cases | `internal/protocol/compat_test.go` | rejection row, `false` / `ErrUnknownType` |
| `v2OnlyTypes` | `internal/protocol/compat_test.go` | `TypeAttachmentStored: true` |
| `all` in `TestTypeConstants_V1V2Partition` | `internal/protocol/compat_test.go` | appended |

Only the first is enforced from source — Assertion #3 walks `codes.go` via the
AST and reddens on an unclassified name. The three in `compat_test.go` cross-check
only against each other, so skipping all three stays **green** and ships an
unpinned type. #1974's commit is the shape copied.

The constant must **not** be added to `inboundAppTypeSet` in `envelope.go`: an old
v1 phone never receives this frame, and `IsKnownAppType` rejecting it is the
structural bar against a v1 client sending one into `dispatch.Route`.

### Documentation

`docs/protocol-mobile.md`, four edits:

1. **§ Application message types** — a row after `attachment_chunk`'s:
   `binary → phone`, no handshake early-data, noting the frame is *correlated by
   `in_reply_to`* and that nothing emits it yet (#1897 does).
2. **§ Attachments, the scope fence** — the paragraph currently reads *"the
   **upload success reply**, which is #1744's. No success frame is declared here,
   and a client must not invent one."* Rewritten: the success reply is now
   declared here; the retrieval request verb remains #1746's and is still the one
   thing this section does not publish. Both `#1744` cites in that paragraph are
   repaired in the same rewrite — the inbound dispatch is #1897 — following
   #1860's precedent for a split ticket's dangling cites.
3. **§ Attachments, a new `#### attachment_stored` subsection** carrying:
   - the direction, the declaring ticket, and that nothing emits it yet;
   - the **four-column Field / Type / Provenance / Meaning** table
     `question_dismissed` uses rather than `attachment_chunk`'s three-column one.
     Every field here is daemon-asserted and that is the whole difference from the
     frame it answers, so provenance earns a column;
   - what `in_reply_to` points at and why the payload still carries the id;
   - **the `attachment_id` shape a client must obey** — exactly 36 bytes,
     lowercase hex, `-` at offsets 8/13/18/23, `4` at 14, one of `89ab` at 19 —
     and why lowercase is load-bearing rather than cosmetic: it keeps the
     id-to-directory mapping injective on a case-insensitive filesystem, which
     APFS is by default, so uppercase ids collide on macOS. Also that nothing
     checks the shape at admission today, so a non-canonical id is discovered
     only when the completing chunk reaches storage — a whole transfer spent to
     learn a rule this paragraph now states;
   - what is deliberately absent, and why: no host path, no directory component,
     no stored filename, no `conversation_id`, no echoed metadata.
4. **`attachment_chunk`'s `attachment_id` field row** gains a pointer to that
   shape paragraph. Without it the row's *"At most 64 bytes"* stands unqualified
   and still reads as permission to use any short string — the specific
   misreading this ticket exists to close.
5. **§ Changelog** — one dated entry, the volume #1751's and #1974's each added.

**Cite repairs are bounded to the scope fence.** `MaxAttachmentIDBytes`' doc block
says the canonical shape is #1741's and #1743's to pick; both are closed and
`EnsureDir` picked it, so that sentence is repaired to name the landed shape and
this document as its publication. A wider `#1744` audit is out of scope: sixteen
further cites survive across `codes.go`, `attachments.go`, `attachments_test.go`
and this document, and rewriting them is a cite audit rather than this ticket.
`EnsureDir`'s own *"#1744 publishes the client-visible contract"* is among them
and is now this ticket — flagged for #1897 or the documentation phase rather than
edited here, since `internal/attachments` is outside this slice's files.

## Concurrency model

None. This slice adds a constant and a struct. No goroutine, no lock, no shared
state, no lifecycle. `AttachmentStoredPayload` is a value type with no reference
field, so no receiver can observe a partially-written one and there is no
backing array for a normaliser to reach through — which is the specific reason
`MarshalJSON` is absent rather than merely unneeded.

## Error handling

No error path is introduced: nothing constructs, validates or decodes this type
in this slice. Two failure modes are addressed by shape rather than by code.

- **A decode of a hostile or truncated payload** yields the zero value —
  `AttachmentID: ""` — rather than an error, because every key is optional to
  `encoding/json`. The empty id is not a valid attachment id under any published
  shape, so a consumer that matches on it clears nothing. #1897 owns the decode
  and its error branch.
- **Correlation that cannot be resolved.** A client receiving an
  `attachment_stored` whose `attachment_id` it does not recognise must ignore it,
  the reading `question_dismissed` publishes for an unrecognised
  `question_batch_id`. It is not an error to report, and it is not a reason to
  present bytes the client never uploaded.

## Testing strategy

Three tests in `internal/protocol/attachments_test.go` plus one fixture,
`internal/protocol/testdata/attachment_stored.json`.

- **`TestAttachmentStoredPayload_RoundTrip`** — fixture-driven, via `readFixture`
  and `roundTripEnvelope`. Pins `env.Type`, pins that `env.InReplyTo` is
  **non-nil** and carries the expected id, pins the decoded `AttachmentID`, and
  byte-compares the re-marshalled envelope. The non-nil `InReplyTo` is the
  structural half of the reply classification: the upload chunk fixture asserts
  `InReplyTo == nil` on the same leg, so the pair says *the chunk is not a reply
  and this frame is*.
  The fixture's `attachment_id` is the canonical-shape id
  `attachment_chunk_upload.json` already carries, so the two fixtures describe
  one transfer rather than two unrelated ones, and the published shape appears in
  committed bytes rather than only in prose.
- **`TestAttachmentStoredPayload_WireKeys`** — the complete-key-set pin AC #2
  asks for. Marshals a populated payload, decodes into
  `map[string]json.RawMessage`, and asserts the key set is **exactly**
  `{attachment_id}` — both that the key is present and that the map has length 1.
  This is the test that fails when a later field is added, and it is
  load-bearing beyond the round trip for a reason the round trip cannot cover:
  regenerate the fixture under an added-field mutant and the round trip goes
  green again, while this stays red. It is also the machine-checkable form of
  "no host path, no directory component, no on-disk filename" — any of those
  arriving as a field fails here by construction rather than by a reviewer
  noticing.
- **`TestAttachmentStoredPayload_ZeroValue_KeysPresent`** — the `omitempty` pin.
  A marshalled zero value rather than a second fixture, because the payload is
  flat and one all-zero struct reaches every key. It must exist separately from
  the round trip: `omitempty` elides a key only at its zero value, and the
  fixture's id is non-empty, so adding `omitempty` leaves the round trip entirely
  green.

Mutants each test is expected to be red against, to be confirmed by running them
rather than predicted: adding a second field (WireKeys, and RoundTrip until the
fixture is regenerated); adding `omitempty` (ZeroValue only); renaming the JSON
tag (all three); changing the constant's value (RoundTrip, plus the three
`compat_test.go` registries); dropping the constant from `excludedTypes`
(Assertion #3 in the relay guard).

Verification is `go test -race ./internal/protocol/... ./cmd/pyry/...`,
`go vet ./...`, `go build ./cmd/pyry`. The full-module race suite and
`make cite-guard` are the verifier's gate.

## Open questions

1. **Does `in_reply_to` point at the completing chunk or at the first chunk?**
   Resolved in Design above: the completing chunk, with the consequence that a
   client cannot predict which envelope id that is, which is what makes the
   payload's `attachment_id` the usable match key. Published rather than left
   implicit. Nothing in this slice enforces it — #1897 does.
2. **Should the shape publication also become a validator at admission?**
   No, and deliberately: this slice is declaration only. `Registry.Admit` keying
   on the raw string is named in the ticket as the reason publishing is worth
   doing, not as a defect to fix here — moving the check to admission changes
   behaviour, needs its reject code chosen from the `attachment.*` set, and is a
   second deliverable. Recorded here so the gap is a named decision rather than
   an oversight.
3. **Does the `attachment_chunk` field-table cross-reference count as the wider
   cite audit the ticket rules out?** No — it is one pointer added to the row
   whose current text is the specific misreading this ticket closes, not a sweep.
   Recorded because the boundary is worth stating.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — and the direction is the finding.** This
  frame sits on the opposite side of the boundary from the one it answers.
  `AttachmentChunkPayload`'s SECURITY block records that one type carries both
  legs and that nothing in it reports which direction a value came from;
  `AttachmentStoredPayload` is **outbound only**, so every field is
  daemon-asserted and the ambiguity does not arise. That is not a free property:
  it holds only because the single field is the client's *own* id echoed back
  after `EnsureDir` validated it, so the daemon asserts nothing it did not
  verify. The four-column provenance table publishes this rather than leaving a
  reader to infer it from the direction arrow. **The reverse leg is the case to
  check and it is closed by omission:** nothing decodes this type in this slice,
  and `IsKnownAppType` rejects it, so a phone sending an `attachment_stored`
  cannot reach `dispatch.Route` — the same structural bar the partition test
  pins.
- **[Error messages, logs, telemetry] No findings, by field selection.**
  `AttachmentChunkPayload`'s never-log rule covers `Filename`, `SHA256` and
  `Data`, and the reply carries **none of the three** — the design's exclusion of
  the stored filename, argued from `SanitizeFilename`'s non-identity, doubles as
  the reason this frame is safe to log whole. `AttachmentID` is explicitly
  **not a capability** (`AttachmentChunkPayload`'s doc: not secret, not
  unguessable) and is already named as loggable there. The frame's length is
  daemon-determined and bounded by the id, so it cannot be inflated by client
  input.
- **[File operations] MUST-NOT rather than no findings, and it is discharged by
  the shape.** This is the category where a success reply is most likely to leak:
  the daemon has just resolved a host path (`EnsureDir` returns the
  symlink-resolved absolute path), and the natural implementation of *"name the
  attachment that was stored"* reaches for it. The payload carries **no path, no
  directory component and no on-disk filename**, and
  `TestAttachmentStoredPayload_WireKeys` makes that machine-checked rather than
  reviewed — a later field carrying any of them fails a test. This aligns the
  success frame with the constraint § Error codes already places on
  `attachment.storage_failed`, which *"never [carries] the host path and never the
  underlying filesystem error, either of which discloses the daemon's layout"*.
  A success frame leaking what the failure frame is forbidden to leak would have
  been the whole mitigation undone from the other side.
- **[Network & I/O] No findings — and the published shape is a small
  improvement.** The frame adds no read path and no allocation from a claim. The
  `attachment_id` shape now published is fixed-length (36 bytes), so a client
  implementing the contract has an O(1) admission check available where the
  previous published text (*"At most 64 bytes"*) offered only a ceiling. No
  number is invented: the shape is `conversations.ValidID`'s, already enforced by
  `EnsureDir`, published rather than re-decided.
- **[Threat model alignment]** § Security model threat **1 (prompt injection)**
  does **not** land on this frame — it carries no claude-authored and no
  client-authored byte, which is what the exclusion of `filename` buys.
  Threat **7 (denial of service)** is unchanged: no per-verb resource is
  introduced, and the upload's own bounds (`attachment.too_large`,
  `attachment.too_many_uploads`) stay receiver-configured and unpublished. The
  remaining threats concern the transport and pairing and are untouched by a
  payload declaration.
- **[Tokens, secrets, credentials] Not applicable, by an argued property rather
  than by absence.** The one field is not a token: `AttachmentChunkPayload`'s doc
  states the id is not secret, not unguessable, and never the only thing between
  a caller and a file, and `attachment.not_found`'s deliberate merge is what
  keeps retrieval from becoming a path-existence oracle. Receiving this frame
  therefore grants nothing — it is echoed to the same authenticated session that
  uploaded the bytes, so disclosure widens nothing. No minting site exists, so no
  RNG question arises.
- **[Cryptographic primitives] Not applicable.** No comparison, no digest, no key
  in this slice. The one adjacent rule is left intact rather than restated:
  `sha256` is integrity and not authenticity, and this frame deliberately does
  not echo it, so it cannot be mistaken for a fetch key.
- **[Subprocess / external command execution] Not applicable.** No `exec`, no
  environment, no argument construction anywhere in the diff.
- **[Concurrency] Not applicable, and the reason is structural.** No goroutine,
  no lock, no shared state. The payload is a value type with no reference field,
  so there is no backing array a `MarshalJSON` normaliser could race on — which
  is why `MarshalJSON` is absent by argument rather than merely unwritten.
- **[Admission-time validation] OUT OF SCOPE, named rather than elided.**
  `Registry.Admit` keys on the raw id string, so a non-canonical id is admitted,
  uploads every chunk, and is refused only at `EnsureDir`. That is a wasted
  transfer, not an exploitable one — `EnsureDir` validates before touching the
  filesystem, so no traversal reaches disk, and the length ceiling was never the
  defence (`MaxAttachmentIDBytes` accommodates `../../../../etc/passwd` several
  times over, as its own doc says). Publishing the shape is this ticket;
  enforcing it at admission belongs to #1897's dispatch, which owns the reject
  path and the code to answer with.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — mutant results corrected the Testing strategy's prediction

The plan listed *"changing the constant's value (RoundTrip, plus the three
`compat_test.go` registries)"* among the mutants. Run rather than predicted, that
is **wrong about the registries**: mutating `TypeAttachmentStored`'s value to
`"attachment_saved"` reddens `TestAttachmentStoredPayload_RoundTrip` **only**.

The reason is worth recording, because it changes what those registries are
evidence of. All three key on the **constant symbol**, not on its wire string, so
a value change moves through `TestIsKnownAppType`, `v2OnlyTypes` and the partition
`all` slice consistently and none of them notices. They pin the constant's
**membership** — v2-only, never in `v1TypeSet`, rejected by `IsKnownAppType` — and
the **committed fixture is the only thing that pins the wire value**. A ticket
that added the registry entries and skipped the fixture would ship a type whose
string nothing checks.

The other three mutants behaved as the plan predicted, confirmed by overlay run:

| Mutant | Red | Green |
|---|---|---|
| Second field on the payload | `WireKeys`, `RoundTrip` | `ZeroValue` |
| `omitempty` on the sole field | `ZeroValue` | `WireKeys`, `RoundTrip` |
| Entry dropped from `excludedTypes` | relay guard Assertion #3 | everything in `internal/protocol` |

The added-field row also confirms `WireKeys` is load-bearing beyond `RoundTrip`
rather than merely redundant with it: `WireKeys` never reads the fixture, so
regenerating the fixture under that mutant restores `RoundTrip` to green and
leaves `WireKeys` red.

### 2026-09-02 — the `attachment_id` shape got its own heading

The plan placed the shape as prose inside the `attachment_stored` subsection. It
shipped as a `##### The `attachment_id` shape` heading nested in that subsection
instead. Two links need to resolve to it — the `attachment_chunk` field row's
pointer and the scope fence's — and an anchor needs a heading. Nesting keeps it
inside the subsection AC #3 names while the text states that the rule binds every
frame in the section, not only the frame it sits under. `##### `resync`` is the
existing precedent for the heading level.
