# #2142 — `attachment_chunk` names the conversation an upload belongs to

**Status:** plan
**Date:** 2026-09-06
**Ticket:** [#2142](https://github.com/pyrycode/pyrycode/issues/2142) (split from #2098)
**Enforcement:** #2143

## Files read

- `internal/protocol/attachments.go` → `AttachmentChunkPayload`, `MaxAttachmentChunkBytes`,
  `MaxAttachmentIDBytes`, `AttachmentStoredPayload`, `RequestAttachmentPayload`,
  `AttachmentOfferedPayload` — the type this ticket widens, the byte-budget block the
  ninth field has to fit inside, and the two siblings whose field order and whose
  `conversation_id` doc language this frame now adopts.
- `internal/protocol/codes.go` → `TypeRequestAttachment` — carries the same asymmetry
  argument as `RequestAttachmentPayload`, one of the live statements this ticket reverses.
- `internal/protocol/messaging.go` → `SendMessagePayload` (its `AttachmentIDs` block) — a
  **fifth live Go statement the ticket body does not name**: it borrows the absent
  `conversation_id` as the property that establishes confinement for the upload leg. Found
  by the `conversation_id` sweep AC-5 makes the checkable boundary, not by the ticket's
  own site list. See § The site list is longer than the ticket's.
- `internal/protocol/attachments_test.go` → `TestAttachmentChunkPayload_Upload_RoundTrip`,
  `_Retrieval_RoundTrip`, `_ZeroValue_RoundTrip`, `_FitV2EnvelopeCap` — the four tests that
  move; the last is the one AC-3 warns greens either way.
- `internal/protocol/envelope_test.go` → `canonical`, `readFixture`; `interactive_test.go`
  → `roundTripEnvelope` — `canonical` is `json.Compact`, so fixture key order is a wire
  assertion and the new key's position in the struct decides it.
- `internal/protocol/testdata/attachment_chunk_{upload,retrieval,zero}.json` — the three
  fixtures that re-pin.
- `internal/relay/v2attachmentstream.go` → `StreamAttachment` (its
  `protocol.AttachmentChunkPayload{` literal) — keyed, so the retrieval leg emits the new
  field at its zero value with no edit. Verified, not assumed.
- `internal/e2e/relay_v2_attachment_upload_test.go` → the AC-2 comment block — prose only;
  the assertions stay correct because behaviour does not change until #2143.
- `docs/protocol-mobile.md` → § Attachments (`#### attachment_chunk`, `#### attachment_stored`,
  `##### Naming a message's attachments`), § Application message types, § Changelog.
- `docs/knowledge/features/protocol-package-constants-codes-go-envelope-types-attachments.md`
  — three lessons that shape this build:
  1. *"A sole-redness claim about an `,omitempty` mutant depends on which regeneration state
     you measured, and the two states can disagree"* — re-run the mutant in **both** states,
     never predict. Drives § Testing strategy.
  2. *"A byte guard's own source literal can silently lose the escape it exists to prove"* —
     grep for the six-byte escape after touching the upload fixture's `filename` assertions.
  3. *"A doc-correction AC that names specific sentences to fix does not imply it found every
     instance"* — the reason the sweep, not the ticket's list, is authoritative.

## Context

`attachment_chunk` carries no `conversation_id`, and § Attachments publishes the omission as
a security property: an upload "lands in the conversation the authenticated session is
already on", so naming one "would only let a client steer bytes elsewhere". The daemon
discharges it structurally — `attachments.Intake` resolves the destination through a
construction-time resolver over the daemon-global follow-active cursor, stamped only by
`sessionRouter.Route` on `send_message`'s successful-route path — so no field on the frame
can reach it.

That costs two operator-visible misfiles: an attachment added before a conversation's first
message can never be stored ([pyrycode-desktop#1076](https://github.com/pyrycode/pyrycode-desktop/issues/1076),
and under #2085 an unmessaged conversation is the normal state), and send-in-A / send-in-B /
return-to-A-and-attach files the bytes under B.

The same section already grants the retrieval leg what it denies the upload leg:
`request_attachment` names a conversation, and this document's own words for that id are
"a lookup key validated against the daemon's registry before it reaches a path join", "never
a value trusted as sent", and "naming a conversation is not authorization". A paired client
can already name any conversation on `send_message` and can already fetch any conversation's
file by naming it. The property the omission bought was never isolation between
conversations — it was the absence of a field.

**This ticket publishes the field. It does not enforce it.** #2143 owns the validation and
the intake rework. This is #2052's filing, which published `request_attachment` ahead of
#2054's handler.

**No ADR.** This reverses a design statement rather than minting one, and the reversal's
reasoning belongs in the changelog entry AC-5 requires plus this plan; `docs/knowledge/`
is the documentation phase's to write.

## Design

### The field

`AttachmentChunkPayload` gains `ConversationID string \`json:"conversation_id"\`` **as its
first field**. Both siblings in this family — `RequestAttachmentPayload` and
`AttachmentOfferedPayload` — declare `ConversationID` first, and `roundTripEnvelope`
compares `json.Compact` output, so struct field order *is* the fixtures' key order. First
is the position the family already uses; anywhere else would make this frame the odd one.

No `omitempty`, matching every other field on the type: § Attachments publishes "every field
is always present in both directions", and the zero fixture's byte guards are what hold it.

No new `Max*` constant. `MaxAttachmentIDBytes` (64) already budgets an id-shaped string on
this frame, the conversation id obeys the same lowercase-UUIDv4 shape under § The
`attachment_id` shape, and `RequestAttachmentPayload`'s block already records why a second
ceiling would enforce nothing while inviting the "ceiling read as shape" mistake that has
been made here once.

### Semantics per leg

| Leg | Value | Reader's obligation |
|---|---|---|
| upload (phone → binary) | the conversation the client means the bytes to land in | an unverified claim; #2143 validates it against the registry before it becomes a path component |
| retrieval (binary → phone) | **empty** | ignore it — the chunk is correlated by `in_reply_to` to a `request_attachment` that already named the conversation |

The retrieval leg's emptiness is committed **as bytes** in `attachment_chunk_retrieval.json`,
not left to prose. `StreamAttachment`'s literal is keyed, so the field emits at its zero value
with no edit to `internal/relay/v2attachmentstream.go` — that file is untouched by this ticket.
This is the reason `attachment_stored` and `history_page` carry no `conversation_id` either.

### Published safety language (MUST carry)

The deliverable is a contract, so the safety is published *with* the field rather than left to
its first implementer — the shape `request_attachment` set. Three sentences are
non-negotiable, and each appears in **both** publication sites for the field (the
`docs/protocol-mobile.md` field-table row and the Go field comment), pointing at the rule
already published rather than minting a second telling:

1. **A lookup key validated against the daemon's registry before it becomes a path
   component, never a value trusted as sent, and naming a conversation is not
   authorization.** #2143 enforces it; nothing does today.
2. **Containment is that validation and never the 64-byte ceiling.** `MaxAttachmentIDBytes`
   accommodates `../../../../etc/passwd` several times over. This is not a hypothetical
   confusion: the package overview records that a ceiling in this very block "was read as the
   shape once already", which is why `RequestAttachmentPayload` declined to mint a second one.
   A field table that publishes "at most 64 bytes" beside a value destined for a path join,
   without this clause, hands the next reader exactly that mistake.
3. **Loggable only after its shape is validated** — raw, it is the client-supplied string in a
   line-oriented log that § Attachments already forbids for `filename`. Nothing logs it today
   (verified: every record in `handleAttachmentChunk` names explicit fields), and #2143 will
   want to on a reject.

Do **not** argue UUIDv4-therefore-unguessable anywhere on this field. The family's published
stance is that these ids are not capabilities, and an entropy claim would promote one.

### The byte budget

`MaxAttachmentChunkBytes`' doc block carries arithmetic that justifies the constant. It gains
one row and its structural row moves, because a ninth key costs structure as well as value:

```
	envelope wrapper, every optional key present      194
	payload braces, keys, quotes, colons, commas      125   (was 104)
	index + total_chunks + size, 3 × 20                60
	sha256, 64 × 6                                    384
	attachment_id, MaxAttachmentIDBytes × 6           384
	conversation_id, MaxAttachmentIDBytes × 6         384   (new)
	filename, MaxAttachmentFilenameBytes × 6         1530
	mime_type, MaxAttachmentMimeTypeBytes × 6        1530
	                                        fixed    4591
	data at the bound, 4 × ceil(45000 / 3)          60000
	                                        frame   64591   (928 B spare)
```

The structural row is 104 → 125: the key `conversation_id` is 15 bytes, plus two quotes, a
colon, a separating comma and the value's own two quotes — 21. **The ticket's own
`1333 − 384 = 949` reading omits those 21 bytes; the answer is 928.**

The ceiling recomputes to `floor((65519 − 4591) / 4) × 3 = 45696`, and 45000 still sits below
it, so **`MaxAttachmentChunkBytes` does not move.** That is the outcome, not the input: the
constant's own block says to LOWER it and never raise the cap if the measurement disagrees,
and `TestAttachmentChunkPayload_FitV2EnvelopeCap` is what measures.

Two neighbouring doc blocks state counts that this field changes and must move with it:
the `const` block header's "three metadata bounds count BYTES", and `MaxAttachmentIDBytes`'
own "bounds AttachmentID" — now two id-shaped fields under one constant. The number of
*constants* does not change; the number of *fields they bound* does, and both sentences say
the second thing.

### The reversal

AC-5's checkable boundary: after this lands, `git grep -n "steer bytes"` and a
`conversation_id` sweep of `internal/protocol/` return no live claim that this frame carries
none. Two categories stay by design — the dated changelog entries and the frozen
`docs/specs/architecture/` artifacts, which quote the argument as the record of #2052's and
#1752's decisions; and `internal/attachments/intake.go` plus `cmd/pyry/relay.go`, which are
#2143's to remove with the intake rework.

Every rewrite points at the rule `request_attachment` already publishes rather than minting a
second telling, and names #2143 as the ticket that enforces it.

#### The site list is longer than the ticket's

The ticket names four live Go sites. The sweep finds **five**. The fifth is
`SendMessagePayload`'s `AttachmentIDs` block in `internal/protocol/messaging.go`, whose
confinement argument borrows this frame's absent `conversation_id` as its precedent — the
exact Go twin of the doc site at § Naming a message's attachments that the ticket *does* call
out as invisible to a number-grep. Its own confinement claim for `attachment_ids` stays true;
only the borrowed parallel is re-anchored. This is the package overview's recorded lesson
firing on the very next ticket: a named site list is a starting point, and the sweep is the
authority.

| # | Site | Shape of the edit |
|---|---|---|
| 1 | `docs/protocol-mobile.md` § `attachment_chunk`, the eight-field count sentence | eight → nine |
| 2 | § `attachment_chunk`, the "three metadata bounds (64 / 255 / 255)" sentence | four bounds, `64 / 64 / 255 / 255` |
| 3 | § `attachment_chunk`, the omission paragraph | replaced by what the field means per leg and who validates it |
| 4 | § Application message types, the `attachment_chunk` row | one clause, per AC-4 |
| 5 | § `attachment_stored`, the "No `conversation_id`" bullet | absence still correct, stated reason replaced |
| 6 | § Naming a message's attachments | re-anchor the borrowed parallel; its own argument stands |
| 7 | `AttachmentChunkPayload`'s doc block | the "There is NO conversation_id" paragraph becomes the field's contract |
| 8 | `AttachmentStoredPayload`'s bullet | twin of site 5 |
| 9 | `RequestAttachmentPayload`'s "WHY THERE IS A conversation_id HERE" | the contrast **survives** — the retrieval id does something the chunk's will not until #2143 — but is restated against a frame that now has the field |
| 10 | `TypeRequestAttachment`'s block (`codes.go`) | twin of site 9 |
| 11 | `SendMessagePayload.AttachmentIDs`' block (`messaging.go`) | **not in the ticket's list**; re-anchor the borrowed precedent |
| 12 | `internal/e2e/relay_v2_attachment_upload_test.go`, the AC-2 comment | "there is no field to steer with" is false once the field exists; assertions untouched |

Plus a new dated changelog entry at the head of the list, naming the reversal and why the
omission no longer holds.

## Concurrency model

None. This ticket adds one struct field, three fixture keys, prose, and test coverage. No
goroutine, no channel, no shutdown path, no shared state.

## Error handling

None, deliberately, and that is the ticket's whole posture. `internal/protocol` declares
shapes and validates nothing — the position every sibling in this family ships with. A
hostile or truncated payload decodes to the zero value rather than an error, since every key
is optional to `encoding/json`; the empty string is not a valid conversation id under any
published shape, so a consumer resolves nothing from it. The concrete silent failure a
consumer must not fall into is the one `RequestAttachmentPayload`'s block already names:
`filepath.Join(base, "", "")` is `base`. Discharging it is #2143's.

## Testing strategy

RED first: add the fixture keys and the assertions, watch them fail against the
un-widened struct, then add the field.

- **`TestAttachmentChunkPayload_Upload_RoundTrip`** — assert a real, non-empty
  `ConversationID`, and that the fixture carries it. The upload leg is where the field means
  something, so the populated fixture is where a reader looks for its shape.
- **`TestAttachmentChunkPayload_Retrieval_RoundTrip`** — assert `ConversationID == ""`, with
  the reason in the assertion's message rather than only in prose. This is AC-2's "committed
  as bytes": `roundTripEnvelope` then holds the emptiness against the committed fixture.
- **`TestAttachmentChunkPayload_ZeroValue_RoundTrip`** — a ninth byte guard
  `"conversation_id":""`, a ninth decode assertion, and every "eight" in the block and in the
  inline comment becomes "nine".
- **`TestAttachmentChunkPayload_FitV2EnvelopeCap`** — `ConversationID: fill(MaxAttachmentIDBytes)`
  alongside the others. **AC-3 names this the one guard that greens whether or not the work is
  done**: the test measures whatever payload it builds, so a missing fill line re-proves the
  eight-field frame silently. The fill line is therefore written *with* the field, and the
  logged byte total is read against the table above rather than assumed.

**Re-run the `,omitempty` mutant; do not extend the existing claim by analogy.** The zero
test's block carries a measured claim about eight mutants. Adding a key makes it a claim about
nine, and the package overview records that this exact claim has already been got wrong by
prediction. Method (the repo's convention, no worktree writes): a `go test -overlay` scratch
copy of `attachments.go` with `,omitempty` on the new tag, run in **both** states — as
committed, and with the fixtures regenerated under the mutant — because the overview records
that the two states disagree. Whatever the run says is what the block says. The expectation
being tested, not asserted: the retrieval fixture's deliberately-empty value should give this
key directional-fixture redness the way `index`'s zero does for upload, which would make
`index` no longer the only such key.

**Gate:** `go test -race ./internal/protocol/... ./internal/relay/... ./internal/attachments/...
./internal/e2e/...`, `go vet ./...`, `go build ./cmd/pyry`. The relay, attachments and e2e
packages are in scope not because they change but because all sixteen
`AttachmentChunkPayload{` composite literals across eight files are keyed — verified, so a new
field compiles everywhere untouched — and compiling them is what proves it. One of the eight,
`internal/e2e/realclaude/interactive_stream_attachment_read_test.go`, sits behind the
`e2e_realclaude` tag and is never compiled by the standard gate, so it stays keyed and is not
touched; `go vet ./...` does not reach it either.

## Open questions

1. **Does the retrieval leg's empty value need a stated obligation, or only a stated meaning?**
   Resolution: state the meaning ("emitted empty, a receiver ignores it") and stop there.
   Publishing an obligation would be a figure ahead of the code that enforces it — the #1752
   rule this section already follows.
2. **Does the metadata-bounds sentence become "four bounds" or stay "three"?** The value is
   budgeted against `MaxAttachmentIDBytes` in the cap arithmetic, so a client must obey 64
   bytes on it for its own frames to fit. Four bounds, `64 / 64 / 255 / 255`. Resolve against
   the measured `FitV2EnvelopeCap` total before writing the sentence.
3. **Does `RequestAttachmentPayload`'s asymmetry block survive at all?** Yes — the retrieval
   id resolves something today and the chunk's resolves nothing until #2143 — but "deliberately
   has none" is false the moment this lands, so the block is restated rather than deleted.
   Confirm against the final struct before committing.

Each is resolved in Phase B; anything that changes the design above is recorded under
`## Revisions`.

## Security review

**Verdict:** PASS (second pass; the first failed on a MUST FIX, and § Published safety
language is the revision that answers it)

**Findings:**

- **[Trust boundaries]** SHOULD FIX, answered in the design. The boundary is explicit and
  single — `handleAttachmentChunk`'s decode into `AttachmentChunkPayload` — and the new value
  goes nowhere from there today: `Intake.Receive` reads `AttachmentID`, `Index`, `TotalChunks`
  and `Data` only, and `Accumulator.Add` retains only `chunk.Data`. Verified by reading both,
  not assumed from the ticket's "nothing else in the wiring moves". The field is a plain
  `string` with no type-level signal that it is untrusted, exactly like
  `RequestAttachmentPayload.ConversationID`, so the *doc block* is the only thing telling the
  next reader — the #2143 implementer — that it is an unverified claim. § Published safety
  language makes that sentence mandatory rather than implicit.
- **[Tokens, secrets, credentials]** Not applicable. No token, no secret, no credential, no
  lifecycle. The adjacent claim that would matter — that receiving or naming this id grants
  something — is denied explicitly by safety sentence 1.
- **[File operations]** **MUST FIX on the first pass; fixed.** The value becomes a path
  component in #2143 and `MaxAttachmentIDBytes` is 64, which accommodates
  `../../../../etc/passwd` several times over. The first draft published the bound in the
  field table and left containment to a vague "who validates it", which is the precise mistake
  the package overview records having already been made in this block once. Safety sentence 2
  now has to appear in both publication sites. No TOCTOU, no permission, no symlink and no
  atomic-write surface: this ticket writes no file and builds no path.
- **[Subprocess / external command execution]** Not applicable. Nothing here reaches
  `exec.Command`, an argv or an environment.
- **[Cryptographic primitives]** Not applicable. No randomness, no key, no comparison against
  a secret. The trap named so it is not reinvented downstream: no UUIDv4-unguessability
  argument on this field.
- **[Network & I/O]** No finding, measured rather than argued. The published 64-byte bound has
  no validator — the posture every field on this frame ships with — so the practical per-frame
  ceiling stays the transport's 65519 B. There is no N× retention multiplier, because
  `Accumulator.Add` stores `chunk.Data` and discards the rest of the struct. The envelope
  budget recomputes to 4591 B fixed / 928 B spare with `MaxAttachmentChunkBytes` unmoved, and
  `TestAttachmentChunkPayload_FitV2EnvelopeCap` measures it rather than trusting the table —
  subject to AC-3's warning that the fill line has to actually be written.
- **[Error messages, logs, telemetry]** SHOULD FIX, answered in the design. Every record in
  `handleAttachmentChunk` names explicit fields (`attachment_id`, `index`, `total_chunks`,
  `code`) and never the struct, so publishing the field adds no log line today and creates no
  log-injection vector on its own. #2143 will want to log it on a reject, which is why safety
  sentence 3 publishes the loggable-only-after-validation rule now rather than after a client
  has shipped against a field table that omits it.
- **[Concurrency]** Not applicable. One struct field, no goroutine, no lock, no shared state.
  `Intake`'s `conversation` resolver stays a construction-time dependency in this ticket, so
  no routing behaviour changes and no check-then-use is introduced.
- **[Threat model alignment]** § Security model threat 1 does not land: the value is
  client-authored and never becomes prompt content — unlike `attachment_offered`'s
  `claude`-authored `filename`, and unlike `send_message`'s `attachment_ids`, which reaches
  `claude`. The threat this ticket does touch is § Attachments' own — a client steering bytes
  into another conversation — and it is **re-scoped, not dismissed**: it moves from
  "structurally impossible, there is no field" to "bounded by registry validation and
  `EnsureDir` refusing an escaping directory", with #2143 installing the first half. Between
  this ticket and #2143 the field is inert, which is the same declare-then-implement window
  `request_attachment` sat in. Stating that reduction plainly in the changelog entry is AC-5's
  requirement and this review's.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
