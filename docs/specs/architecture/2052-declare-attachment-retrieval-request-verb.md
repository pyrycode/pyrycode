# #2052 — declare the attachment retrieval request verb

Wire vocabulary only: the constant `TypeRequestAttachment`, its payload
`RequestAttachmentPayload`, the four registry entries the build forces, one
golden fixture, and the document that publishes them. Nothing emits, accepts or
dispatches the frame when this lands.

## Files read

- `internal/protocol/codes.go` → `TypeAttachmentChunk`, `TypeAttachmentStored`,
  `TypeQuestionAnswer` blocks — the three doc blocks this constant's own block
  has to agree with; `TypeAttachmentStored`'s is where the correlation decision
  (`in_reply_to`, no request-id key) is argued, and `TypeQuestionAnswer`'s is the
  nearest declared-without-a-handler block. Also the `CodeAttachment*` block,
  whose consumer list still names four closed tickets.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload`,
  `AttachmentStoredPayload` — the two siblings the new payload lands beside;
  `AttachmentChunkPayload`'s no-`conversation_id` block is the code twin of the
  document sentence this slice corrects, and its SECURITY block is the posture
  the new type inherits and narrows.
- `internal/protocol/attachments_test.go` → `TestAttachmentChunkPayload_Retrieval_RoundTrip`
  (its comment records the correlation question this slice answers, and its
  fixture's `in_reply_to` is the value the new fixture's envelope id must match),
  `TestAttachmentStoredPayload_WireKeys`, `TestAttachmentStoredPayload_ZeroValue_KeysPresent`
  — the two test shapes that pin a key set and an `omitempty` respectively.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`,
  `TestTypeConstants_V1V2Partition`, `TestInboundAppTypeSet_CoversAllExportedTypeConstants`
  — three registries that gain an entry and one whose fixed length of 23 must not
  move.
- `internal/protocol/envelope.go` → `inboundAppTypeSet`, `IsKnownAppType` — the
  set this constant must stay out of, and the function that must reject it.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes`,
  `TestEveryInboundV2TypeHasHandler` — Assertion #1 fails an inbound type with no
  handler, Assertion #2 forces the entry up to `inboundTypes` when #2054 lands,
  Assertion #3 fails an unclassified constant from the moment it exists.
- `internal/conversations/id.go` → `ValidID`, `NewID` — the daemon-side shape
  both ids are documented against, byte for byte.
- `docs/protocol-mobile.md` § Attachments, § Application message types, § Error
  codes, § Changelog — the four places that publish this family and the five
  correction sites AC 5 enumerates.
- `docs/knowledge/features/protocol-package-drift-detectors.md` — three lessons
  that shape the test plan below and would otherwise be re-learned: no registry
  catches a wire-*string* typo (only a committed fixture does); a complete-key-set
  test outlives a fixture regeneration that a round trip does not; and the
  `env`-round-trip shape never calls `json.Marshal` on the payload struct, so it
  exercises no struct tag at all (measured by overlay mutant on #2036).

## Context

`docs/protocol-mobile.md` § Attachments publishes the whole attachment transfer
with one hole it names out loud — the retrieval request verb, *"no such type
exists in the daemon today"*. A client that wants a file back has nothing to send.
pyrycode-desktop#687 is parked on exactly that question.

This slice closes the hole by declaring the verb and publishing it, and nothing
more. It is the declare-then-implement step this family has taken three times
(#1752 before #1897, #1895 before #1897, #1983 before #1984): the wire string is
the contract, and a name chosen twice is a name chosen wrong once.

It also settles one open question the code owes:
`TestAttachmentChunkPayload_Retrieval_RoundTrip`'s comment records that *"#1746
still owns whether and how the retrieval verb correlates"*. The answer is
`attachment_stored`'s, unchanged — correlation rides the envelope's `in_reply_to`
and the payload carries no request-id key.

No ADR is warranted. Every decision here either follows a published precedent in
§ Attachments or is recorded in the constant's own doc block; there is no
alternative design being rejected that a future reader would need the ADR form to
reconstruct.

**File-overlap check (§ A2).** One remote branch touches a file this slice edits:
`origin/feature/449` → `internal/protocol/codes.go`. Its ticket (#449, the v2
re-key responder path) closed 2026-05-17 and the branch has no PR and no commit
since 2026-05-17. It is a stale branch of closed work, not in-flight, so it can
never reach integration and no blocker is set. No other feature branch touches
`codes.go`, `attachments.go`, `attachments_test.go`, `compat_test.go`,
`relay_guard_test.go` or `protocol-mobile.md`.

## Design

### The constant

A new one-member block in `internal/protocol/codes.go`, placed after
`TypeAttachmentStored`'s so the attachment family stays contiguous:

```go
const (
	TypeRequestAttachment = "request_attachment" // phone → binary, inbound v2 control (pending handler — #2054)
)
```

The name follows the three inbound "ask the daemon for X" verbs already on this
wire — `request_snapshot`, `request_debug_bundle`, `request_session_settings` —
and #2054's title is already written against it.

Its doc block carries, in the idiom of the blocks around it: why the verb is
named as it is; that correlation rides `in_reply_to` for `TypeAttachmentStored`'s
reasons, so no request-id key exists; that it must NOT join `inboundAppTypeSet`
and why (`IsKnownAppType` rejecting it is the structural bar against a v1 client
pushing one into `dispatch.Route`); that both drift detectors classify it from
the moment the constant exists rather than from the moment something dispatches
it; and that it is filed in `excludedTypes` under a pending-handler label rather
than in `inboundTypes`, which Assertion #1 would fail while this slice ships no
handler — the point `TypeQuestionAnswer`'s block calls *"a lie to the guard"*.

### The payload

Lands in `internal/protocol/attachments.go` after `AttachmentStoredPayload`:

```go
type RequestAttachmentPayload struct {
	ConversationID string `json:"conversation_id"`
	AttachmentID   string `json:"attachment_id"`
}
```

Two fields, both always present (no `omitempty`, no `MarshalJSON`), no
request-id key. Contract points the doc block must carry:

- **One direction only, phone → binary.** That is the whole difference from
  `AttachmentChunkPayload`, which rides both legs and therefore cannot report the
  trust level of its own fields. Here there is nothing to decide: **every field
  is an unverified claim, always**, and a consumer never has to ask where the
  frame came from.
- **`ConversationID` is a lookup key, never a value trusted as sent.** It is
  validated against the daemon's own registry **before it reaches a path join**,
  not merely before the bytes go out, and naming a conversation here is **not
  authorization**. This is the same property
  § Naming a message's attachments states for `send_message`'s `attachment_ids`
  — *"Nothing about the id's shape or its randomness does this work —
  confinement does"* — and it has to be in the Go block as well as the document,
  because the block is what the implementer of #2054 reads.
- **Why a `conversation_id` exists here at all**, when `AttachmentChunkPayload`
  deliberately has none: an upload lands in the conversation the authenticated
  session is already on, so naming one there would only let a client steer bytes
  elsewhere; a retrieval must be able to say which conversation's file it wants.
  § Attachments already committed to that asymmetry.
- **Both ids obey the lowercase-UUIDv4 rule** § Attachments publishes as
  **The `attachment_id` shape**, which is `conversations.ValidID`'s shape byte for
  byte. Lowercase is load-bearing: the id becomes a directory name and a
  case-insensitive host folds two ids into one directory otherwise.
- **Nothing here enforces any of it.** No validator, no bound, no constant. The
  shape check and the registry validation belong to #2054. The block says so, in
  the same words `AttachmentChunkPayload`'s bounds block uses, so a reader cannot
  mistake a declared rule for a checked one.
- **No length ceiling constant is added.** `MaxAttachmentIDBytes` exists only
  because `attachment_chunk`'s envelope arithmetic needed a ceiling; this frame
  is two short strings and needs none, and #1895 had to spend a whole correction
  undoing a reader's belief that the 64-byte ceiling *was* the shape.
- **A hostile or truncated payload decodes to the zero value, not to an error**,
  because every key is optional to `encoding/json` — `AttachmentStoredPayload`'s
  block records the same property. Two empty strings are the result, and the
  empty string is not a valid id under any shape this document publishes, so a
  consumer must resolve nothing from them. Named explicitly because the natural
  failure is silent and specific: `filepath.Join(base, "", "")` is `base`, so a
  consumer that skips the shape check and joins the zero value addresses the
  **conversation directory root** rather than erroring. `question_answer`'s
  published obligation is the shape to follow — a decode failure is a rejected
  frame, never an empty-but-successful request — and discharging it is #2054's,
  which is why the rule is stated here where its decoder will read it.
- **The hazard class `AttachmentChunkPayload` warns about is absent by shape.**
  There is no count and no length field, so NEVER ALLOCATE FROM A CLAIM has
  nothing to bite on here. Stated rather than left silent, so its absence reads
  as a property of the shape rather than an omission.
- **Log only after the shape is validated.** Both fields are client-supplied
  strings, and a raw one in a line-oriented log is the log-injection shape
  § Attachments already forbids for `filename`. The payload carries no
  content-bearing bytes, so after validation it is loggable whole.

### Registries — the cascade, and the one that must not move

| Registry | Change |
|---|---|
| `internal/protocol/codes.go` | the constant |
| `TestIsKnownAppType`'s `cases` (`compat_test.go`) | a reject row, `ErrUnknownType`, beside the `attachment_chunk` / `attachment_stored` rows |
| `v2OnlyTypes` (`compat_test.go`) | one entry under a v2-attachment-retrieval comment |
| `TestTypeConstants_V1V2Partition`'s `all` (`compat_test.go`) | one entry, same comment |
| `excludedTypes` (`cmd/pyry/relay_guard_test.go`) | `"TypeRequestAttachment": "pending handler (#2054)"` |
| `inboundAppTypeSet` (`envelope.go`) | **no change** — its fixed length of 23 does not move |

The `excludedTypes` entry copies `TypeQuestionAnswer`'s from #1983 (added in
`ae09ade6`, moved up to `inboundTypes` by #1984 in `dac742dd`) and carries the
same three reasons in its comment: `inboundTypes` fails Assertion #1 with no
handler; `"push"` is false for a genuinely inbound frame; and Assertion #2 makes
the move to `inboundTypes` mandatory the moment #2054 adds its
`dispatchAppFrame` case.

### The document

Two publication points, as § Attachments does for every frame:

1. A `#### request_attachment` subsection **appended at the end of § Attachments**,
   after `##### Naming a message's attachments`. Placing it earlier would re-parent
   the two `#####` subsections (`The attachment_id shape`,
   `Naming a message's attachments`) under this frame, and neither belongs to it.
   Content: direction and v1 posture; that nothing answers it yet (#2054 answers,
   #2053 streams); a three-column field table — every field is client-asserted, so
   a provenance column would repeat one value where `question_answer`'s varies, and
   the sentence says it once instead; correlation rides `in_reply_to` with no
   request-id key; the registry-validation rule and the "naming a conversation is
   not authorization" sentence AC 4 requires; both ids bound to
   **The `attachment_id` shape**; that an absent or empty id resolves nothing
   rather than resolving the conversation root; that this shape publishes **no
   bound** and a client learns any receiver limit by being rejected, the
   #1752 rule against publishing a figure ahead of the code enforcing it; what a
   client gets back (a stream of `attachment_chunk`, or the existing static
   `attachment.not_found` / `attachment.stream_aborted`, pointed at rather than
   restated); and a JSON example, following `request_session_settings`.
2. A row in § Application message types beside `attachment_chunk` and
   `attachment_stored`.

### Corrections (AC 5)

Every live claim that the retrieval request verb is unpublished, and every live
`#1746` cite in the files this slice touches. #1746 splits three ways: **#2052**
the verb, **#2053** the outbound stream, **#2054** the handler and the reject path.

- `docs/protocol-mobile.md`: the § Attachments scope fence (the claim appears
  twice in one paragraph); the no-`conversation_id` argument; the
  **Retrieval, and its two terminal signals** paragraph; the
  `attachment.not_found` and `attachment.stream_aborted` rows in § Error codes.
- `internal/protocol/codes.go`: *"#1746 serves retrieval"* in
  `TypeAttachmentChunk`'s block, and the `CodeAttachment*` block's consumer list
  naming #1741/#1743/#1744/#1746 — all four closed or split.
- `internal/protocol/attachments.go`: the five `#1746` cites, of which the
  no-`conversation_id` block in `AttachmentChunkPayload`'s doc is the code twin of
  the document sentence above and goes stale the same way.
- `internal/protocol/attachments_test.go`: the three `#1746` cites, including
  `TestAttachmentChunkPayload_Retrieval_RoundTrip`'s *"#1746 still owns whether
  and how the retrieval verb correlates"* — the open question this slice closes.
  Not in the ticket's enumerated list because it is a test file, but it is a file
  this slice already edits and the claim goes false with this commit.
- § Changelog: **one new dated entry**. #1895's entry states the same thing and is
  **left alone**, matching how this file has handled every prior correction.
- **Out of scope**, named rather than silently skipped: the `#1746` cites under
  `internal/attachments/`, `docs/specs/` and `docs/knowledge/`. Those belong to
  the slices that own those files; #1983 made the same bounded re-point for #1907.

## Concurrency model

None, and that is a property rather than an omission: this slice adds one string
constant, one struct, test entries and prose. No goroutine, no channel, no lock,
no shared state, no shutdown path. `dispatchAppFrame`'s switch gains no case, so
every runtime path is byte-identical to before the commit.

## Error handling

No error path ships. `IsKnownAppType` gains a rejection (`ErrUnknownType`) for the
new type by virtue of it being absent from `inboundAppTypeSet` — asserted, not
written. Decode-failure handling, the shape check on either id, the registry
validation, and the `attachment.not_found` / `attachment.stream_aborted` answers
are #2054's, which owns the reject path; the document says so where it publishes
the frame.

## Testing strategy

In `internal/protocol/attachments_test.go`, against one new fixture
`internal/protocol/testdata/request_attachment.json`. The three shapes are chosen
against the drift-detector lessons above, each covering what the others cannot:

- **`TestRequestAttachmentPayload_RoundTrip`** — reads the fixture, asserts the
  envelope type, asserts `InReplyTo` is **nil** (a request is not a reply; the
  correlation runs the other way), asserts both id values, then
  `roundTripEnvelope`. This is the only test that pins the wire *string*
  `"request_attachment"` — no registry does, confirmed by mutant on #1895.
  The fixture's **envelope id is 91**, which is exactly the `in_reply_to` the
  committed `attachment_chunk_retrieval.json` carries, and its `attachment_id` is
  that fixture's. So the two files describe **one retrieval** and the correlation
  decision appears in committed bytes rather than only in prose — the tie
  `attachment_stored.json` made for the upload leg.
- **`TestRequestAttachmentPayload_WireKeys`** — marshals a populated struct and
  asserts the key set is exactly `{conversation_id, attachment_id}`, two-sided.
  This is the machine-checked form of "the payload carries no request-id key" and
  it survives a fixture regeneration that would turn the round trip green again.
- **`TestRequestAttachmentPayload_ZeroValue_KeysPresent`** — marshals the zero
  value and asserts both keys are present at `""`. The `omitempty` pin: the two
  tests above both carry non-empty ids, so an `omitempty` added later leaves both
  green, including the key-set check.

A marshalled zero value rather than a second fixture, for
`AttachmentStoredPayload`'s reason: the payload is flat, so one all-zero struct
reaches every key.

The registry entries are self-testing — `make check` fails on any of the four
sites missed, which is the point of the duplication.

Gate: `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`,
`go build ./cmd/pyry`, plus `make cite-guard` on the comments this commit adds.

## Open questions

1. **Does the fixture's `conversation_id` use a canonical UUIDv4, when the
   neighbouring `request_session_settings.json` uses `"c1"`?** Resolved in the
   design: yes. `attachment_stored.json` set the precedent of putting a published
   shape rule into committed bytes, and this frame's contract binds *both* ids to
   that shape.
2. **Where in § Attachments does the new subsection go?** Resolved in the design:
   appended at the end, so the two existing `#####` subsections keep their parent.
3. **Does `attachment.not_found`'s § Error codes row re-point to #2054 or to
   #2053?** To #2054: it owns the reject path. `attachment.stream_aborted` is
   mid-stream abandonment, so its cite splits — the stream is #2053, the error
   frame that reports it is #2054's to emit.

## Security review

**Verdict:** PASS (second pass; the first found one MUST FIX, fixed in the design
above before this section was written and before the plan was committed).

**Findings:**

- [Trust boundaries] **MUST FIX — resolved in revision.** The first draft required
  the payload block to say `ConversationID` is registry-validated, but said
  nothing about what a *hostile or truncated* payload decodes to. Every key is
  optional to `encoding/json`, so `{}` or `null` yields two empty strings rather
  than an error, and the specific silent failure is
  `filepath.Join(base, "", "")` == `base` — a consumer skipping the shape check
  addresses the conversation directory root instead of erroring. The design now
  requires that rule in the payload's doc block (where #2054's implementer reads
  it) and in the published subsection, following `question_answer`'s obligation
  that a decode failure is a rejected frame, never an empty-but-successful
  request. Boundary itself is explicit and single: one decode site, #2054's.
- [Trust boundaries] The type is **one direction only**, unlike
  `AttachmentChunkPayload`, so no consumer has to infer trust from where a frame
  arrived: every field is a claim, always. That is a strengthening over the
  sibling and is written into the block rather than left to be noticed.
- [Tokens, secrets, credentials] No finding, by design rather than by absence:
  nothing is minted, stored or rotated here. The live trap is a reader taking
  "UUIDv4" as unguessability and concluding the id authorizes — § Naming a
  message's attachments' *"confinement does"* sentence is restated for this frame
  in both the block and the document. Neither id is a secret; both are safe to log
  **after** shape validation.
- [File operations] **SHOULD FIX — folded in.** Both ids become path components
  downstream through `attachments.ResolvePath`. The draft said "validated before
  it resolves anything"; the ordering that matters is **before it reaches a path
  join**, and the wording now says that. Containment follows from the shape and
  never from a length ceiling — which is also why no `Max*` constant is added:
  a ceiling enforces nothing, and #1895 spent a correction undoing exactly that
  misreading of `MaxAttachmentIDBytes`. No TOCTOU, symlink or file-mode surface:
  this slice opens nothing.
- [Subprocess / external command execution] Not applicable, with a reason worth
  recording rather than a bare N/A: unlike `send_message`'s `attachment_ids`
  (#2038), a retrieval request's ids **never become prompt content** — the frame
  asks for bytes outbound, so nothing here is passed to `exec` or to `claude`.
- [Cryptographic primitives] Not applicable — no randomness is drawn and no
  primitive is chosen here; the ids are client-minted. The only crypto-adjacent
  hazard is the unguessability misreading recorded under Tokens.
- [Network & I/O] **OUT OF SCOPE — #2054.** This verb introduces the family's
  first genuine **amplification**: one ~120-byte inbound frame makes the daemon
  read a whole file and emit many ~60 KB frames. Bounding concurrent in-flight
  retrievals is the handler's, and #2053 owns the stream's own pacing. The
  document states that this shape publishes no bound and a client learns any
  receiver limit by being rejected, rather than naming a figure ahead of the code
  that enforces it (#1752's rule). The frame has no count or length field, so
  `AttachmentChunkPayload`'s NEVER ALLOCATE FROM A CLAIM has nothing to bite on —
  stated in the block so the absence reads as a property of the shape.
- [Error messages, logs, telemetry] No finding. Both fields are client-supplied
  strings and inherit § Attachments' log-injection rule — loggable only after the
  shape is validated. The reject answer stays the existing static, deliberately
  merged `attachment.not_found`, which never echoes the requested id or the
  resolved path; the new subsection points at that row rather than restating a
  weaker version of it, so the path-existence-oracle mitigation cannot be diluted
  by a second telling.
- [Concurrency] Not applicable, and checkably so: no goroutine, channel, lock or
  shared state ships. `dispatchAppFrame` gains no case, so every runtime path is
  byte-identical to before the commit.
- [Threat model alignment] Threat 1 (prompt injection) does **not** land — no
  field is claude-authored and none reaches claude, the opposite of
  `attachment_ids`. Threat 4 (token leak via phone) is unchanged: authorization
  is pairing, enforced structurally at the Noise IK handshake, and this frame
  grants no new capability because the id is not one — a stolen paired device's
  reach is bounded by registry confinement, not by id secrecy. Threat 7 (DoS) is
  the amplification finding above, deferred to #2054.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03
