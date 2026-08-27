# #1751 — Publish the attachment transfer contract and its reject codes

**Ticket:** [#1751](https://github.com/pyrycode/pyrycode/issues/1751) · `size:s` · `security-sensitive`
**Blockers:** #1752 (frame type + payload) and #1753 (`MaxAttachmentChunkBytes`) — both landed.
**Consumers wired blocked-by this ticket:** #1741 (reassembly), #1743 (storage), #1744 (inbound dispatch), #1746 (retrieval).

This ticket is **documentation plus wire vocabulary**. It ships no producer, no consumer,
no validator and no state machine. Two things change: `docs/protocol-mobile.md` gains the
published contract, and `internal/protocol/codes.go` gains seven `attachment.` reject-code
constants that the four blocked slices answer with instead of each inventing its own.

---

## Files to read first

Cite by symbol, never by line — everything below is a symbol or a `§` heading, and
`codegraph_search` resolves the Go names on demand.

| Read | Symbol / section | What to extract |
|---|---|---|
| `internal/protocol/attachments.go` | `AttachmentChunkPayload` | **The entire source material for the field table.** Eight field contracts, the SECURITY block's inbound-claim / outbound-authored asymmetry, `NEVER ALLOCATE FROM A CLAIM`, the no-`conversation_id` reasoning, and the "no completion frame because `total_chunks` rides every chunk" property. |
| `internal/protocol/attachments.go` | `MaxAttachmentChunkBytes` | The bound AC 1 publishes: **45000 raw, pre-base64 bytes of `Data`** — not base64 characters, not payload bytes, not envelope bytes. Also the envelope-cap arithmetic and the fact that this is a producer-side contract with **no validator in the package**. |
| `internal/protocol/attachments.go` | `MaxAttachmentIDBytes`, `MaxAttachmentFilenameBytes`, `MaxAttachmentMimeTypeBytes` | The three metadata byte bounds a client must obey for its own frames to fit. Note `MaxAttachmentIDBytes`' own warning that a length ceiling is **not** a safety property. |
| `internal/protocol/codes.go` | `TypeAttachmentChunk` | The block that **fixes two names this ticket may not choose**: the section heading `§ Attachments` and the `attachment.*` prefix. It also delegates reject #6 (a retrieval abandoned mid-stream) here by name, as a `TypeError` correlated via `in_reply_to`. |
| `internal/protocol/codes.go` | `CodeSessionBlocked` | The existing `Code*` block: the `Code<Category><Reason>` naming convention, the one-line-group-comment style, and "grouped by category in spec-table order". The new group goes at the **end** of that const block. |
| `internal/protocol/compat_test.go` | `TestErrorCode_Constants_MatchSpec` | The two hand-maintained maps AC 5 extends, plus the `len(cases) != len(want)` guard. **Neither map enumerates anything** — a new constant that skips both leaves the test green, which is why AC 5 exists. |
| `docs/protocol-mobile.md` | § Debug bundle (v2) | The shape precedent, and the closest neighbour: a field table per frame, a **Reassembly & integrity** paragraph, and a **Content hygiene** paragraph. Note `debug_bundle_chunk`'s `seq` succession rule — this frame's rule is deliberately *weaker* and the contrast has to be published. |
| `docs/protocol-mobile.md` | § Error codes | The table AC 4 extends: `Code \| Retryable \| Notes`. Existing rows show the house voice for the Notes column. |
| `docs/protocol-mobile.md` | § Application message types | The per-type table. `attachment_chunk` has **no row yet** — see "Edits beyond the ACs" below. |
| `docs/protocol-mobile.md` | § Application-envelope size cap | The paragraph AC 2 corrects. Its closing sentence is the false claim. |
| `docs/protocol-mobile.md` | § Scope → "Out of scope (v2)" | The **second** stale attachment claim, same class as AC 2's. |
| `docs/protocol-mobile.md` | § Changelog | House convention: every section addition gets a dated entry. **Read one for the voice, not the length** — see the size fence below. |
| `internal/relay/v2bundlestream.go` | `ReassembleBundle`, `StreamBundle` | The receiver and sender references #1741 and #1746 are told to follow. The rules published here must not contradict them. |
| `docs/knowledge/features/protocol-package.md` | `codes.go` row of the file table | Where a new const group is recorded. **Its "13 Code\* string constants" count is already stale (there are 14).** Do not trust it as an inventory; do not fix it either — `features/` belongs to the documentation phase. |

---

## Context

`docs/protocol-mobile.md` is the only artifact the mobile and desktop clients code
against; they do not read `internal/protocol`. #1752 and #1753 landed the Go — a shared
bidirectional frame and a per-chunk byte bound — with **nothing published**, so today a
client author would have to reverse-engineer both legs from struct comments. Worse, the
spec actively contradicts the landed code: § Application-envelope size cap still closes by
saying large payloads "require an envelope-level chunking scheme that is out of scope for
this spec", and § Scope still lists attachments as out of scope for v2.

The reject vocabulary is the other half. #1744 and #1746 are both wired blocked-by this
ticket precisely so neither invents codes; a condition missing from this vocabulary is a
code one of them will have to make up, and two slices making up codes months apart is the
drift the shared frame was designed to prevent.

**No ADR is warranted.** The load-bearing design decisions (one frame for both directions,
no completion peer, no `conversation_id`) were made and recorded by #1752 in
`AttachmentChunkPayload`'s and `TypeAttachmentChunk`'s doc comments. This ticket publishes
them to clients; it does not re-decide them. The one decision that *is* new — merging
"unknown attachment" and "outside the conversation's directory" into a single reject code —
belongs in the spec's own § Error codes row, where clients can rely on it, rather than in a
knowledge-base ADR they never read.

---

## Design

### 1. The reject vocabulary — seven codes

Added at the end of the `Code*` const block in `internal/protocol/codes.go`, under a
`// Attachment errors.` group comment, following the existing `Code<Category><Reason>`
convention.

| Constant | Wire value | Retryable | Condition | Inventory item |
|---|---|---|---|---|
| `CodeAttachmentInvalidChunk` | `attachment.invalid_chunk` | no | A chunk's framing claims are inconsistent or out of range: a duplicate `index`, an `index` outside `[0, total_chunks)`, or a `total_chunks` disagreeing with the stream's earlier chunks. | 1 (#1741 AC 3) |
| `CodeAttachmentIntegrityFailed` | `attachment.integrity_failed` | no | The assembled bytes do not match the declared `sha256`, **or** the assembled length does not match the declared `size`. | 2 (#1741 AC 2) |
| `CodeAttachmentTooManyUploads` | `attachment.too_many_uploads` | **yes**, after a backoff | The receiver's bound on concurrent in-flight uploads is hit. | 3a (#1741 AC 4) |
| `CodeAttachmentTooLarge` | `attachment.too_large` | no | One upload exceeds the receiver's per-upload byte bound — whether detected from the declared `size` on the first chunk or from accumulated bytes later. | 3b (#1741 AC 4) |
| `CodeAttachmentStorageFailed` | `attachment.storage_failed` | yes, after a backoff | A verified attachment could not be written to the host. | 4 (#1743, surfaced by #1744 AC 3) |
| `CodeAttachmentNotFound` | `attachment.not_found` | no | A retrieval request does not resolve to a file inside the named conversation's directory. **Deliberately merged** — see § 2. | 5 (#1746 AC 3) |
| `CodeAttachmentStreamAborted` | `attachment.stream_aborted` | yes, after a backoff | The daemon abandoned a retrieval mid-stream. A `TypeError` correlated via `in_reply_to`, **not** a second attachment frame. | 6 (delegated by `TypeAttachmentChunk`) |

**Seven codes for six inventory conditions: condition 3 splits, deliberately.** The ticket
records condition 3 as uniformly transient. It is not. "Too many concurrent uploads" clears
when other uploads finish, so a retry succeeds. "This upload is over the byte bound" is
permanent *for that file* — the same bytes fail the same way forever. A single code marked
`retryable: yes` would publish a contract under which a client re-uploads an oversized file
in a loop; a single code marked `no` would tell a client to give up on a bound that clears
in seconds. AC 3 asks for codes "covering every condition in the inventory", and two codes
cover condition 3; AC 4's retryability column is only honest with the split. There is no
disclosure cost — both concern the client's own upload, whose size it already knows.

**No number from either bound is published.** The concurrency bound and the per-upload byte
bound are #1741's to choose. The section says they are receiver-configured and that a client
learns them by being rejected. The **only** numeric bound this section publishes is
`MaxAttachmentChunkBytes` (45000), which AC 1 names explicitly.

**Go-comment size fence.** The group gets one block comment of roughly eight to twelve lines
carrying three things: the pointer to `docs/protocol-mobile.md` § Attachments, the merged-code
disclosure decision in two sentences, and why one code in the group is retryable. Per-constant
trailing comments only where a code is genuinely confusable — `attachment.too_large` beside
`message.too_long`, and `attachment.too_many_uploads` as the group's only retryable member.
**Do not write a doc comment in `AttachmentChunkPayload`'s register.** That comment is 120
lines because it was the only place its reasoning could live; this ticket's whole point is
that the reasoning now has a published home, and duplicating it into Go creates a second
copy to drift.

### 2. The disclosure decision — one code for two conditions

#1746 AC 3 folds "unknown attachment" and "resolves outside the named conversation's
directory" into one reject. This vocabulary makes that structural: **`attachment.not_found`
is the answer to every retrieval that does not yield bytes**, covering at least

- an attachment id naming nothing on the host,
- an id whose canonical shape is invalid,
- an id that resolves to a path outside the conversation's own directory,
- an id whose file exists but belongs to a different conversation.

Two distinguishable codes would make the retrieval verb a path-existence oracle: a traversal
probe learns whether it hit a real file, which is exactly the signal it is fishing for. One
code teaches it nothing. The name is chosen to match — `not_found` is the benign-looking
member of the pair and matches the `conversation.not_found` / `session.not_found` precedent,
where a name like `access_denied` would confirm existence in the word itself.

Three rules make the merge real rather than nominal, and all three are published:

1. **Same message.** The reject carries a static message. It never echoes the requested id,
   the resolved path, or the underlying filesystem error — any of which re-opens what the
   shared code closes.
2. **Same work.** The receiver performs the same resolution steps in the same order for
   every retrieval request, so an unknown id and a contained-path failure do not separate on
   response timing. (Weak channel over WSS + Noise + a mobile network, but the mitigation is
   free at design time and expensive to retrofit.)
3. **No sub-cases on the wire.** Clients MUST NOT branch on sub-cases of this code, because
   there are none to branch on.

### 3. The published section — `docs/protocol-mobile.md` § Attachments

Heading text is **fixed as `Attachments`** by four in-tree citations (`TypeAttachmentChunk`
and `AttachmentChunkPayload` in `internal/protocol`, and #1752's own spec). Placement: as a
`###` section in the same v2 family as § Debug bundle (v2) — landing it **after** § Debug
bundle keeps the two streaming-transport sections adjacent, and the § Session settings (v2)
family that follows stays undisturbed.

The section has no request-verb subsection. `request_attachment` exists nowhere in the tree
and #1746 AC 1 owns declaring it; publishing a verb ahead of its Go is what #1752 existed to
prevent. Name it as #1746's, exactly as `AttachmentChunkPayload`'s doc already does.

Subsections, in order:

**(a) Intro — the shared frame and the two directions.** One frame, `attachment_chunk`,
carries both legs: **upload** (phone → binary) and **retrieval** (binary → phone). State that
this is the mechanism that stops the legs drifting — there is no second shape to keep in
step — and that the relay stays transport-only with no blob endpoint.

**(b) `attachment_chunk` field table.** Eight rows from `AttachmentChunkPayload`, in struct
order, each with type, meaning, and the trust note where it differs by direction:

| Field | Wire type | Notes to carry |
|---|---|---|
| `attachment_id` | string | Repeated on every chunk of one transfer; the key a receiver accumulates under and the identifier that later resolves to a file. **Not a capability** — not secret, not unguessable, never the only thing between a caller and a file. Bounded at `MaxAttachmentIDBytes` bytes. |
| `index` | integer | 0-based, in `[0, total_chunks)`. **Decides where the bytes land** — a receiver addresses by it, never appends. |
| `total_chunks` | integer | ≥ 1, identical on every chunk of one transfer. This is why the stream needs no completion frame. |
| `filename` | string | The client's own name for the file: a display string and a sanitiser input, **never a path**. Bounded at `MaxAttachmentFilenameBytes` bytes. |
| `mime_type` | string | The client's declared media type: a display and dispatch hint, **not a verified property of the bytes**. Bounded at `MaxAttachmentMimeTypeBytes` bytes. |
| `size` | integer | Declared byte length of the **whole file**, not of this chunk. |
| `sha256` | string | Lowercase hex sha256 of the **whole file**, not of this chunk; always 64 characters. |
| `data` | string (base64) | This chunk's raw bytes, standard-base64. At most **45000 raw bytes** before encoding. |

Also state that **every field is always present** in both directions (no field is elided),
and that there is **no `conversation_id`** — an upload lands in the conversation the
authenticated session is already on, so a client cannot steer bytes into another
conversation's directory by naming one.

**(c) Chunking rule (the sender's obligation).** Publish it as arithmetic a client can
implement without reading Go:

- Every chunk but the last carries exactly `45000` raw bytes; the last carries the remainder.
- `total_chunks = max(1, ceil(size / 45000))`. The `max(1, …)` is what defines the zero-byte
  case: one chunk carrying zero bytes, consistent with `total_chunks ≥ 1`.
- The bound counts **raw bytes of `data` before base64**. A client that mistakes it for
  base64 characters or envelope bytes produces frames that fit but wastes a quarter of each.

State the consequence plainly: a chunk that exceeds the 65519-byte application envelope after
serialisation is rejected by the transport with **`message.too_long`**, not with any
`attachment.*` code, because `MaxAttachmentChunkBytes` is a producer-side contract with no
validator behind it. Contrast the two size codes explicitly — `message.too_long` means *one
envelope* was too big; `attachment.too_large` means the *whole transfer* exceeds the
receiver's per-upload bound.

**(d) Reassembly & integrity (the receiver's rules).** Mirror § Debug bundle's paragraph of
the same name, with the differences called out:

- The receiver addresses by `index`; **chunks may arrive in any order**. This is a
  deliberately weaker rule than `debug_bundle_chunk`'s `seq`, which demands strict
  succession. Publish the contrast — the neighbouring section's stricter rule is otherwise
  the obvious thing to copy.
- A duplicate `index`, an `index` outside `[0, total_chunks)`, or a `total_chunks` that
  disagrees with the stream's earlier chunks → `attachment.invalid_chunk`, stream discarded.
- The transfer is **complete** when every index in `[0, total_chunks)` has arrived exactly
  once. Only then: assembled length vs `size`, then `sha256(assembled)` vs `sha256`, compared
  as **lowercase hex for exact equality**. A case-insensitive or prefix comparison is a hole;
  rejecting an uppercase-sending client is an availability bug, so the canonical form is
  stated as lowercase and clients are told to send it that way.
- Either mismatch → `attachment.integrity_failed`. **Never emit partial or corrupted output.**
- `sha256` is **integrity, not authenticity**: the same party supplies the bytes and the
  digest, so a match proves the transfer was not corrupted and proves nothing about the
  content. It is not a fetch key — content-addressed retrieval ("know the hash, fetch the
  blob") would promote a non-secret claim into a capability, and retrieval names a
  conversation and an attachment, never a hash.
- **Never allocate from a claim** (inbound leg only): `total_chunks` and `size` are
  attacker-chosen integers, and `make` on a claimed `total_chunks` of 2³¹−1 is a
  multi-gigabyte allocation driven by one ~60 KB frame. Both are range-checked, and
  cross-checked against each other via the 45000-byte bound, **before** anything is sized —
  a check available from the first chunk. Publish that the check exists and that exceeding
  either bound yields `attachment.too_large` or `attachment.too_many_uploads`; do **not**
  publish the guard's exact comparison form, which is #1741's (see Open questions).

**(e) Retrieval and its two terminal signals.** The retrieval leg is the same frame,
daemon-authored, flowing outbound, in reply to the verb #1746 declares.

- **Completion** is `total_chunks` indices received — there is no completion frame. The
  bundle stream needs `debug_bundle_done` because its chunks carry only `seq` and the count
  is unknowable until the end; here the count rides frame one, so truncation is detectable
  *earlier* rather than later.
- **Abandonment** is `attachment.stream_aborted`, a `TypeError` correlated by `in_reply_to`.
  On receiving it a client **MUST discard everything accumulated for that transfer** and MUST
  NOT present the partial bytes as the file. With no completion frame, this is the only
  negative signal the stream has, and a client that keeps its buffer renders a truncated file
  as a whole one.
- A stream that simply stops with no abort frame (the session died) is detected by the
  client's own timeout. Say so — the protocol offers no frame for it.
- A request that yields no bytes is `attachment.not_found`, per § 2.

**(f) Trust and content hygiene.** The subsection that earns the `security-sensitive` label,
mirroring § Debug bundle's **Content hygiene** paragraph:

- **Inbound, every field is a claim, not a fact.** The daemon validates each before use.
- **Outbound, the fields are daemon-authored — but two of them are laundered client input.**
  `filename` and `mime_type` arrive attacker-chosen at upload time, are stored, and are
  echoed back on the retrieval leg. "Daemon-authored" reads as "trustworthy" and for these
  two it is not: a client **MUST** sanitise `filename` before rendering it and **MUST NOT**
  use it as a path or a filesystem name without sanitising, and **MUST NOT** dispatch on
  `mime_type` in any way that grants the content privileges (no rendering an
  attacker-declared `text/html` as markup). This round-trip is the section's least obvious
  hazard and gets its own sentence.
- A client should also **bound what it allocates from an outbound `size` / `total_chunks`**
  against its own memory budget, and refuse a transfer larger than it can hold rather than
  attempt it. The daemon is trusted here, but a fixed-budget client still has a budget.
- **`data` is content-bearing and never logged** — a stronger rule than the debug bundle's,
  because those are daemon-authored diagnostics and these are a user's own private file
  bytes. `filename` gets the same treatment for two independent reasons: a filename is often
  private in itself, and a client-supplied string in a line-oriented log is a log-injection
  shape. Log the attachment id, the index and the total; never the bytes, never a raw
  filename. Publishing the rule tells client authors it applies on their side too.
- **Authorization is pairing**, enforced structurally at the Noise IK handshake, exactly as
  § Debug bundle records — an unpaired device is refused at the handshake (WS 4401) and never
  reaches these paths. There is no per-verb authorization gate, and none is invented here.

**(g) Scope fence — what this section does *not* publish.** Two sentences, because both
absences are load-bearing and a reader will otherwise assume an omission:

- The **retrieval request verb** is #1746's (AC 1).
- The **upload success reply** is #1744's (AC 2). This section publishes the chunk frame and
  the reject vocabulary; it declares no success frame, and the developer must not invent one.

### 4. The § Error codes table

Seven rows appended to the existing table, in the § 1 order, each with the condition that
produces it and its retryability. Match the house voice of the existing rows: the Notes
column names the producing condition, the owning ticket, and the one thing a client gets
wrong by default. Three rows carry extra weight:

- `attachment.not_found` — state that it is deliberately indistinguishable across unknown-id
  and outside-the-directory, and that this is a disclosure decision, not an imprecision. Add
  the client-facing consequence so the merge does not read as a loss: both outcomes mean the
  same thing to a client — re-list the conversation's attachments — so there is nothing a
  distinguishable pair would let it do differently.
- `attachment.too_many_uploads` — contrast with the permanent `attachment.too_large` in the
  same breath, the way the existing `session.blocked` row contrasts itself with
  `server.binary_busy`.
- `attachment.stream_aborted` — name the discard obligation.

**All three `yes` rows carry an explicit backoff obligation: retry after a backoff, never
immediately.** `retryable: yes` on its own reads as permission to resend at once, and each of
the three then becomes a hot loop — `attachment.too_many_uploads` because the bound clears
only when *other* uploads finish, `attachment.storage_failed` because a host condition may
not clear at all, and `attachment.stream_aborted` because a re-request re-runs the same
resolution. The `4429` row in the WS close-code table below already carries this MUST
back-off language for the same reason; match its voice. `noise.rekey_failed`'s "may retry
after a backoff" is the phrasing precedent inside this table.

---

## Edits beyond the ACs

Three edits the ACs do not name but the house convention requires. All three are one-liners
except the changelog, and all three are counted in the size re-check.

1. **A row for `attachment_chunk` in § Application message types.** Every other v2 frame has
   one, including `debug_bundle_chunk` and `session_error`. The changelog itself records a
   missing row as a defect ("fixed a #1074 omission: `api_retry` and `compacting` shipped
   without rows in the application-message-types table above"). Direction is **phone ↔
   binary** — the table's first genuinely bidirectional entry — with "Carries handshake
   early-data?" = no, and a link to the new section.
2. **§ Scope's "Out of scope (v2)" attachment bullet.** Currently reads *"**Attachments.** v2
   is the encryption layer; first attachment release rides on top."* Amend rather than delete:
   the wire contract is now published in this document, while the daemon-side implementation
   is not yet built. Deleting the bullet would over-claim; leaving it contradicts the new
   section exactly as AC 2's sentence does.
3. **A dated § Changelog entry.** House convention for every section addition. **Bound it to
   one paragraph** — name the section, the seven codes, the merged-code disclosure decision,
   the split of condition 3 on retryability, the two stale claims corrected, and that nothing
   emits or enforces any of it yet (the declare-then-publish sequencing #1704→#1705 and
   #1726→#1718 used). The recent entries in that file run to a screen each; do not match their
   length, only their voice.

---

## Error handling

This ticket introduces no error paths — it declares the vocabulary other slices answer with.
Two contracts the vocabulary imposes on those slices, stated here so they are not
rediscovered four times:

- **The sentinel → code map is many-to-one, and that is deliberate.** #1741 AC 3 requires a
  duplicate index, an out-of-range index, and a `total_chunks` disagreement to be
  *distinguishable errors* — as Go sentinels, for its own tests and for the daemon's logs.
  All three map to `attachment.invalid_chunk` on the wire. #1744 AC 3's "its corresponding
  dotted wire code" is satisfied by a well-defined many-to-one correspondence; it does not
  ask for one code per sentinel, and three codes here would publish three names for one
  client-visible outcome. The same holds for the two integrity sentinels (digest, length) →
  `attachment.integrity_failed`, and for every retrieval-resolution failure →
  `attachment.not_found`, where the collapse is a security requirement rather than a
  convenience.
- **Refusal-to-wire-code mapping is the consumer's job, not the primitive's** — the
  project-level convention in `docs/PROJECT-MEMORY.md`. `internal/protocol` declares the
  strings; `internal/relay`'s dispatch site maps sentinels to them via `errors.Is`. This
  ticket adds constants only, and wires nothing.

---

## Concurrency model

None. `internal/protocol` is a pure-data leaf package — no I/O, no goroutines, no `context`,
no `slog` — and this ticket adds string constants plus a Markdown section. The concurrency
questions the published rules raise (how many uploads may be in flight, what bounds one
accumulator) are enforced by #1741 and named here without numbers.

---

## Testing strategy

- **AC 5 is the only mechanised rung.** Extend both maps in `TestErrorCode_Constants_MatchSpec`
  with all seven constants — the `cases` map (constant → value) and the `want` map (constant →
  literal string). The `len(cases) != len(want)` guard catches a one-sided edit but **not** a
  constant that skips both maps, which is precisely why AC 5 is its own criterion.
- **Verify each pin actually fails.** For each of the seven, the `want` entry must be a
  hand-typed literal, never a reference to the constant — a row written as
  `"CodeAttachmentNotFound": CodeAttachmentNotFound` is tautological and green under any
  value. A quick check that the pins bite: change one constant's value, confirm the test
  reddens, revert. Run it over a `go test -overlay` scratch file rather than editing the
  worktree.
- **AC 4 is verified by reading**, not by a test. Nothing reads `docs/protocol-mobile.md` at
  runtime — every mention of it in a `_test.go` is a comment. Read the seven table rows
  against the seven constants in `codes.go` in both directions: every constant has a row, and
  every row has a constant.
- **AC 2 is verified by grep.** After the edit, no sentence in the file should claim a
  chunking scheme is out of scope. Sweep for `out of scope` near the envelope-cap paragraph
  **and** for the § Scope bullet, since the same claim appears twice in different words.
- **Gate:** `make check`. No new test file, no new package, no e2e rung — nothing in this
  ticket executes.

---

## Open questions

1. **#1746 AC 1 contradicts the landed frame design, and this section will make the
   contradiction visible.** That AC says retrieval is answered with "ordered chunks ending in
   a **completion marker**", but `TypeAttachmentChunk`'s block records that there is no
   completion frame, deliberately, because `total_chunks` rides every chunk. The section
   published here follows the landed code. **Recommended resolution for #1746:** read
   "completion marker" as the chunk whose `index` is `total_chunks - 1` — the transfer is
   complete when every index has arrived — and add no second frame type. This is #1746's
   ticket to reword, not this one's; flagging it here so its architect run does not
   re-litigate the decision #1752 already made.
2. **The exact form of #1741's `total_chunks` / `size` cross-check is left open, on purpose.**
   `MaxAttachmentChunkBytes`' doc records that only the *equality* form
   (`total_chunks == ceil(size / bound)`) bounds `total_chunks` from above, and that the
   equality is already false at `size == 0`. This section publishes the **sender's**
   obligation — `total_chunks = max(1, ceil(size / 45000))`, every chunk but the last exactly
   at the bound — which is the one rule that satisfies a strict receiver guard and is free
   for a client to obey. #1741 must accept that form, including the zero-byte case. If #1741
   decides the empty-file case differently, the published rule is what has to change, and it
   is one line.
3. **The per-upload byte bound and the concurrency bound stay unpublished.** Once #1741 picks
   them, a follow-up may want them in the table so a client can pre-flight a large file
   instead of discovering the limit by rejection. Deliberately not this ticket's — publishing
   a number ahead of the code that enforces it is the failure mode #1752 existed to prevent.

---

## Scope check (re-applied to this written spec)

| Boundary | Limit | This spec | |
|---|---|---|---|
| Production source files created or modified | ≤ 3 | **1** — `internal/protocol/codes.go`. `compat_test.go` is a test file, `protocol-mobile.md` is Markdown; both excluded by the counting rule. | ✅ |
| Total written work | ≤ 400 | **≈ 210** — codes.go ≈ 30 (7 constants + a fenced group comment), compat_test.go 14 (7 rows × 2 maps), § Attachments ≈ 120, § Error codes 7, envelope-cap edit 2, type-table row 1, § Scope bullet 2, changelog ≈ 12, spec-file edits 0. Worst case ≈ 260. | ✅ |
| New exported types or interfaces | ≤ 5 | **0** — seven untyped string constants; no type, no interface. | ✅ |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — purely additive. Every existing `Code*` reference in the tree names a pre-existing constant, and the only enumeration of the set is the two maps in `TestErrorCode_Constants_MatchSpec`, which are AC 5 itself. Verified by sweeping `Code[A-Z]` across `internal/` and `cmd/`, not asserted. | ✅ |
| Acceptance criteria | ≤ 5 | **5** — at the boundary, not over it. | ✅ |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** — this ticket ships no state machine. The seven codes are constants nothing switches on yet; the branches live in #1741 and #1746. | ✅ |

Ships as one `size:s` ticket.

**File-overlap check (§ 1.5).** `git fetch origin --prune` then a sweep of every
`origin/feature/<N>` branch for `internal/protocol/codes.go`, `internal/protocol/compat_test.go`
and `docs/protocol-mobile.md` returns exactly one hit: `origin/feature/449`, touching
`codes.go`. It is **stale, not in-flight** — issue #449 closed 2026-05-17, the branch tip is
from 2026-05-17, it has no PR in any state, and it is not an ancestor of `main`. A branch
abandoned three months ago on a closed issue cannot merge, so it cannot conflict at
integration time. No `blockedBy` set; recorded here so review does not re-flag it.

---

## Security review

**Verdict:** PASS — after two passes and inline revisions. The first pass found a MUST FIX
(trust boundaries), which was fixed in § 3f and the checklist re-run from the top; the second
pass found one SHOULD FIX (network & I/O), fixed in § 4. No finding is left open.

**Findings:**

- **[Trust boundaries]** MUST FIX → **FIXED inline** (§ 3f). The spec's first draft published
  the inbound-claim / outbound-authored asymmetry as `AttachmentChunkPayload`'s SECURITY block
  states it, and that framing is a trap for a client author: on the retrieval leg `filename`
  and `mime_type` are *daemon-authored* yet are verbatim attacker-chosen strings stored at
  upload time and echoed back. A client reading "daemon-authored, trustworthy" renders a
  laundered attacker string — the classic stored-injection shape, and for `mime_type` a
  content-sniffing privilege escalation. `AttachmentChunkPayload`'s doc anticipates the
  round-trip only as a *size* hazard (an unbounded filename overflowing the daemon's own
  outbound frame). § 3f now states the client-side obligation explicitly: sanitise `filename`
  before rendering, never use it as a path, and never let `mime_type` grant the content
  privileges. This is the finding this pass existed to catch.
- **[Trust boundaries]** No further findings — the boundary itself is explicit and
  single-typed (`AttachmentChunkPayload`, both legs), and the spec publishes which side of it
  each field sits on per direction rather than leaving it to the struct, which cannot report
  direction.
- **[Tokens, secrets, credentials]** No findings, and one restatement made load-bearing:
  `attachment_id` is **not a capability** and `sha256` is **not a fetch key** (§ 3b, § 3d).
  Both are published as non-secret, guessable claims so that no client author builds
  know-the-hash-fetch-the-blob retrieval against this contract. No token is generated, stored,
  rotated or revoked by this ticket.
- **[File operations]** OUT OF SCOPE for the code, IN SCOPE for the contract. This ticket
  writes no file and constructs no path; #1743 and #1746 do. What it owes them is the reject
  vocabulary that does not leak the outcome of their path checks, and § 2 delivers it: one
  code for unknown-id and outside-the-directory alike, a static message that never echoes the
  id or the resolved path, and the same resolution work on every request so the two do not
  separate on timing. Traversal defence itself is #1743 AC 2 and #1746 AC 3;
  `MaxAttachmentIDBytes`' own doc already warns that a length ceiling is not one.
- **[Subprocess / external command]** N/A — no path in this design reaches `exec.Command`, and
  none of the seven codes is produced by a subprocess failure.
- **[Cryptographic primitives]** No findings. The one primitive named is sha256 over the whole
  file, published as **integrity, not authenticity** (§ 3d), with the comparison pinned to
  exact lowercase-hex equality — a case-insensitive or prefix compare is called out as a hole
  in the published text. Constant-time comparison is deliberately **not** required: the digest
  is a client-supplied claim compared against a digest of bytes the client itself sent, so
  there is no secret on either side of the comparison. No RNG is introduced.
- **[Network & I/O]** SHOULD FIX → **FIXED inline** (§ 4), found on the second pass. The three
  codes marked `retryable: yes` published no retry discipline, and `yes` on its own reads as
  permission to resend immediately — turning each into a client-driven hot loop against the
  daemon, a self-inflicted DoS from a *conforming* client rather than a hostile one.
  `attachment.too_many_uploads` is the sharpest case: the bound clears only when other uploads
  finish, so an immediate retry both fails and consumes the capacity it is waiting for. All
  three rows now carry an explicit back-off obligation, matching the `4429` row's existing
  MUST-back-off language in the same document.
- **[Network & I/O]** No further findings. Every size the section publishes is a cap, never a floor:
  45000 raw bytes per chunk against the 65519-byte envelope, the three metadata byte bounds,
  and two receiver-side bounds named without values. The **never allocate from a claim** rule
  (§ 3d) is published as a receiver obligation with the cheap first-chunk cross-check, which
  is the ticket's one real resource-exhaustion vector. `message.too_long` is named as the
  transport's answer to an oversize frame so no reader assumes `MaxAttachmentChunkBytes` is
  enforced anywhere.
- **[Error messages, logs, telemetry]** SHOULD FIX → addressed in the published text, flagged
  for code review. Three leak shapes are closed by the section rather than by code:
  `attachment.not_found` and `attachment.storage_failed` both carry static messages and never
  the resolved path or the underlying filesystem error (a storage error string is a host-path
  disclosure); `data` is never logged; `filename` is never logged raw, both because it is
  private and because a client-supplied string in a line-oriented log is a log-injection
  shape. Enforcement is #1744 AC 3's ("no attachment bytes appear in any log line") — this
  spec cannot enforce it, and code review on those tickets must.
- **[Concurrency]** N/A — `internal/protocol` is a pure-data leaf package with no goroutines,
  no locks and no shared mutable state. This ticket adds constants and Markdown.
- **[Threat model alignment]** No findings, one deliberate silence. § Security model's threat
  #7 (denial of service, `mitigation: deferred`) is the threat this section touches, and it is
  *narrowed* rather than left alone: the allocation-from-a-claim vector is named with its
  mitigation, and the two receiver bounds are published as existing. § Security model is **not
  amended** — its threat entries are #430's dated artefact, the bounds behind the narrowing
  are #1741's to land, and the honest place for the rules is the section that publishes the
  contract. Threats #1 (prompt injection) and #4 (token leak via phone) are unchanged by an
  attachment path that adds no new authorization surface: authorization is pairing, structural
  at the Noise IK handshake, and § 3f says so rather than inventing a per-verb gate.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
