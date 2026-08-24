# #1752 — Declare the attachment chunk frame both directions ride

Wire vocabulary only: one envelope type constant, one payload struct, and the two
structural guard classifications that keep `make check` green. No fixtures, no cap
constant, no dispatch, no reassembly, no storage.

## Files to read first

Read these before writing anything. Each entry names the symbol and what to take
from it — resolve symbols with `codegraph_search` / `codegraph_node`, not by
scrolling.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/protocol/messaging.go` | `DebugBundleChunkPayload` | The nearest shape precedent. `Data []byte` auto-encodes as standard base64 through `encoding/json`; its doc block states the "content-bearing, never logged" line and puts the receiver's ordering contract **on the type**, not in the receiver. Copy that form, not its fields. |
| `internal/protocol/messaging.go` | `DebugBundleDonePayload` | Why this ticket declares no peer of it. Read `Total`'s doc sentence — truncation detection is the job it does, and `TotalChunks` here does it from the first chunk instead. |
| `internal/protocol/interactive.go` | `ToolUsePayload` | The house form for a `SECURITY:` doc block that says a field is a claim rather than a fact, stated on the type. AC 3's enumeration follows this shape. |
| `internal/protocol/messaging.go` | `SessionErrorPayload` | The no-`omitempty` convention and its stated reason ("all three are always present so the golden fixture pins the full shape"). Every field on the new payload follows it. |
| `internal/protocol/codes.go` | `TypeSlashCommandList`, `TypeModelList` | The two most recent declare-only const blocks. Their doc blocks are the length and structure to match: what the frame is, the v1/v2 classification sentence, the guard classification sentence, and the declaring-ticket-vs-producer-ticket sequencing paragraph. |
| `internal/protocol/codes.go` | `TypeRequestDebugBundle` | The **bare control frame** precedent: "no payload, no `conversation_id`, no field an attacker could use to select another session's data — mirroring `TypeInterrupt`". This is the reasoning that decides § Design's no-conversation-id call. |
| `internal/protocol/envelope.go` | `Envelope`, `inboundAppTypeSet` | Confirm for yourself that `Envelope` carries **no** conversation id, and that `inboundAppTypeSet` is the v1 set the new constant must stay out of. No production edit lands in this file. |
| `internal/protocol/compat_test.go` | `TestIsKnownAppType`, `v2OnlyTypes`, `TestTypeConstants_V1V2Partition` | Three of the four cascade sites. Note `TestTypeConstants_V1V2Partition`'s closing size assertion: adding to both `v2OnlyTypes` and its `all` list keeps it balanced. |
| `internal/protocol/compat_test.go` | `TestInboundAppTypeSet_CoversAllExportedTypeConstants` | The one test in this file you must **not** touch. Its hardcoded `want` of 23 counts v1 application types only; a v2 constant never enters it. |
| `cmd/pyry/relay_guard_test.go` | `excludedTypes`, `TestEveryInboundV2TypeHasHandler` | The fourth cascade site, in another package. Read Assertion #1 and Assertion #3 in the test body before choosing where the entry goes — § Design explains why the obvious choice is wrong. |
| `cmd/pyry/relay_guard_test.go` | the `"TypeHello": "handshake"` entry | The precedent to copy for the new entry's comment: a borderline phone → binary type deliberately not filed inbound, carrying its own label and an explanation. The seven `"push"` neighbours are **not** the precedent here. |
| `internal/devices/device.go` | `HashToken` | The house representation of a sha256 in this repo: lowercase hex, "always 64 hex characters (`sha256.Size * 2`)". Fixes the `SHA256` field's wire form. |
| `internal/relay/v2bundlestream.go` | `bundleChunkBytes`, `StreamBundle` | Read only to confirm you are **not** touching it. The per-chunk cap is #1753; this ticket declares no constant of that kind. |

## Context

Attachments have been designed since 2026-05-16 and refined 2026-07-03, and
`internal/protocol` still carries no attachment vocabulary. Six slices are blocked
on this one: #1753 (cap constant + both-direction fixtures), #1751 (the published
spec section and reject codes), #1741 (reassembly), #1743 (storage), #1744
(dispatch), #1746 (retrieval). Each of them is built at a different time against
this doc block, which is why the doc block is a deliverable here rather than a
courtesy.

The settled decisions this slice implements, none of which are open:

- The client chunks the file over the encrypted channel; the relay stays
  transport-only. A blob endpoint was considered and rejected.
- Per-chunk metadata carries attachment id, chunk index, total chunk count,
  filename, mime type, declared size in bytes, and a sha256 of the whole file.
- **Both directions ride one frame.** Upload and retrieval share the type. That is
  the mechanism that stops them drifting once they are built months apart, so it
  belongs in the declaration rather than in a note. Declaring exactly one type is
  what makes the drift impossible by construction; #1753 commits the fixtures that
  check it in both directions.

**An ADR is not warranted.** The cross-cutting decisions (client-side chunking,
relay stays transport-only, per-conversation storage) were taken upstream of this
slice and #1751 publishes the client-facing contract in `docs/protocol-mobile.md`.
This slice adds no decision that outlives its own doc block. The documentation
phase should fold the two design calls below into
`docs/knowledge/features/` rather than open `docs/knowledge/decisions/`.

### Branch-overlap check

Run and recorded on 2026-08-24. One remote feature branch touches a file this
spec prescribes: `origin/feature/449` (`internal/protocol/codes.go`). It is
**not** live work and does not block:

- Issue #449 is CLOSED (2026-05-17) and the branch's tip commit is from the same
  day — three months stale, not a concurrent agent run.
- Its `TypeRekeyRequest` constant is already on main (`v2OnlyTypes` classifies it
  today), so the diff against merge-base is a squash-merge artefact, not pending
  content.

No open ticket's branch touches `codes.go`, `compat_test.go`, `attachments.go` or
`relay_guard_test.go`. No `blockedBy` edge is filed.

## Design

Two production files. Nothing else in `cmd/` or `internal/` changes.

### 1. `internal/protocol/codes.go` — the envelope type constant

One new const block, doc-commented in the form `TypeSlashCommandList` and
`TypeModelList` use.

```go
const (
	TypeAttachmentChunk = "attachment_chunk" // phone ↔ binary, one chunk of an attachment's bytes (both directions)
)
```

The constant lives in `codes.go` and nowhere else. `appTypeConstNames` in
`cmd/pyry/relay_guard_test.go` parses that file only, and the guard's header
comment states the convention explicitly ("Keep new app constants in `codes.go`
so they stay inside the totality tie below"). A constant declared in
`attachments.go` would silently escape the totality tie.

The doc block must state, at minimum:

- What the frame carries and that **both directions ride it** — the trailing
  comment's `phone ↔ binary` is the only bidirectional arrow in the file, so the
  block has to justify it rather than let it read as a typo.
- The v1/v2 sentence in the file's existing words: MUST NOT be added to
  `inboundAppTypeSet` in `internal/protocol/envelope.go`; the drift detector in
  `internal/protocol/compat_test.go` partitions `Type*` constants between
  `inboundAppTypeSet` and `v2OnlyTypes`, and this one lives in the latter.
- The guard sentence: `cmd/pyry/relay_guard_test.go`'s `excludedTypes` records it,
  and **why the reason is not "push"** — see § 3 below. Name #1744 as the slice
  that moves it to `inboundTypes`.
- The declaring-ticket sequencing paragraph: #1752 is wire vocabulary only; #1753
  adds the per-chunk cap and the both-direction fixtures, #1751 publishes the
  contract and the reject codes, #1741 reassembles, #1743 stores, #1744
  dispatches, #1746 serves retrieval.

### 2. `internal/protocol/attachments.go` — the payload

New file. The package is one file per concern (`messaging.go`, `snapshot.go`,
`settings.go`, `workspace.go`, …) and attachments are a new concern; #1753's
fixtures and cap test land beside it as `attachments_test.go`.

```go
type AttachmentChunkPayload struct {
	AttachmentID string `json:"attachment_id"`
	Index        int    `json:"index"`
	TotalChunks  int    `json:"total_chunks"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Data         []byte `json:"data"`
}
```

Field contracts the doc block states (this is the whole surface the six blocked
slices code against):

- `AttachmentID` — identifies the attachment the chunk belongs to. Every chunk of
  one transfer repeats it; it is the key an in-flight upload accumulates under
  (#1741) and the identifier that later resolves to a path on the host (#1743,
  #1746).
- `Index` — 0-based position of this chunk within the attachment, in `[0,
  TotalChunks)`. It decides **where the bytes land**, so a receiver addresses by
  it rather than appending.
- `TotalChunks` — how many chunks the whole attachment splits into, `>= 1`,
  identical on every chunk of one transfer. A receiver knows the expected count
  from the first chunk. `>= 1` is a shape constraint, **not** a sufficient bound —
  see the allocation rule in § The `SECURITY:` doc block.
- `Filename` — the client's own name for the file. A display string and a
  sanitiser input, never a path.
- `MimeType` — the client's declared media type. A display and dispatch hint, not
  a verified property of the bytes.
- `Size` — declared byte length of the **whole file**, not of this chunk. `int64`
  to mirror `os.FileInfo.Size()`; a receiver checks it against the assembled
  length.
- `SHA256` — lowercase hex sha256 of the **whole file**, not of this chunk — 64
  hex characters, the representation `internal/devices`' `HashToken` already
  produces and documents.
- `Data` — this chunk's raw bytes. `[]byte` auto-encodes as standard base64 via
  `encoding/json`, exactly as `DebugBundleChunkPayload.Data` does.

No field carries `omitempty`, for `SessionErrorPayload`'s recorded reason: every
field is always present in both directions, so #1753's fixtures pin the full
shape. State that in the doc block — a later `omitempty` added for tidiness would
silently change the wire.

### The `SECURITY:` doc block — required contents

AC 3 is met by this block and nothing else, so it is a deliverable with a spec of
its own rather than prose to improvise. Follow `ToolUsePayload`'s `SECURITY:`
form: a labelled paragraph at the end of the type's doc comment, stating what the
values are **not**.

It must say all five of the following. The six blocked slices read this block as
their contract; a partial enumeration becomes six divergent guesses.

1. **Direction decides trust, and the struct cannot say which direction it is
   on.** Inbound (client → daemon, upload), every field is an unverified *claim*.
   Outbound (daemon → client, retrieval), the same fields are daemon-authored and
   trustworthy. One type carrying both is the settled anti-drift decision, so the
   doc block is the only place this asymmetry can live.

2. **Name every claim-bearing field, not the obvious three.** `Filename`, `Size`
   and `SHA256` are the ones a reader expects. `AttachmentID`, `Index` and
   `TotalChunks` are the ones whose falsification actually costs something, and
   they must be named with what it costs:
   - `TotalChunks` is what a receiver would size an accumulator from — see the
     allocation rule below.
   - `Index` decides where bytes land, so a duplicate or out-of-range value
     corrupts or escapes the buffer.
   - `AttachmentID` keys the in-flight upload and later resolves to a path on the
     host — see the identifier rule below.

3. **Never allocate from a claim.** `TotalChunks` and `Size` are attacker-chosen
   integers. `make([][]byte, TotalChunks)` or `make([]byte, Size)` on a claimed
   `TotalChunks` of 2³¹−1 is a multi-gigabyte allocation from a single 60 KB
   frame — an unauthenticated-by-content OOM, and the cheapest attack this frame
   offers. The doc block must state that a receiver range-checks both **before**
   sizing anything, and that the two numbers cross-check each other for free:
   given #1753's published per-chunk raw bound, `TotalChunks` must equal
   `ceil(Size / bound)`, and both must be within the receiver's own limits. That
   check is available from the **first** chunk, before a single byte is
   accumulated. #1741's AC 4 owns the enforcement; this block is what tells #1741
   the check exists and is cheap.

4. **`AttachmentID` is validated before it is a path component.** It resolves to a
   file on the host in #1743 and #1746. A client-chosen id reaching
   `filepath.Join` unvalidated is a traversal. No slice's ACs currently cover it —
   #1743's AC 2 covers the *filename* and its AC 3 the *conversation id*, and the
   attachment id falls between them — so the constraint is stated here, where the
   field is declared, and the deterministic check lands in #1741/#1743.
   `conversations.ValidID` is the existing canonical-shape precedent (#1743's own
   body names it). State also that the id is **not** a capability: it is not
   secret, not unguessable, and must never be the only thing standing between a
   caller and a file.

5. **`Data` is content-bearing and never logged.** Stronger here than the line
   `DebugBundleChunkPayload` carries: a debug bundle is daemon-authored
   diagnostics, whereas these are a user's own private file bytes. `Filename` gets
   the same treatment for two independent reasons — a filename is itself often
   private, and a client-supplied string in a line-oriented log is a log-injection
   shape. Log the attachment id, the index and the total; never the bytes, and
   never a raw filename.

Two things the block must **not** claim, because a reader will otherwise assume
them:

- **`SHA256` is integrity, not authenticity.** The same party supplies the bytes
  and the digest, so a matching digest proves the transfer was not corrupted — it
  proves nothing about whether the content is safe. It also must not become an
  access token: content-addressed retrieval ("know the hash, fetch the blob") is a
  tempting shortcut for #1743 and would turn a non-secret claim into a capability.
- The canonical form is **lowercase hex, compared for exact equality** against a
  digest the receiver renders the same way — the form `HashToken` documents. A
  case-insensitive or prefix comparison is a hole; a case-sensitive comparison
  against an uppercase-sending client is an availability bug. Say which one is
  correct so neither gets invented.

### Design calls this slice closes

**No completion frame.** `TotalChunks` rides every chunk, so a receiver knows the
expected count from the first frame it sees and detects a truncated stream
*earlier* than `debug_bundle_done` detects one for the bundle stream — whose
chunks carry only `seq` and therefore need a terminal frame to learn the count at
all. A second frame would double the `codes.go` doc block, the guard entry and the
four-site cascade for a property already carried. Terminal *errors* (a retrieval
the daemon abandons mid-stream) are `TypeError` correlated via `in_reply_to`, which
is #1751's reject vocabulary, not a second attachment frame.

Consequence for PO, not for the developer: **#1746's AC 1 says retrieval ends "in
a completion marker" and now needs rewording.** #1752's own notes name #1746 as
the ticket to reword if the answer lands this way. Reversing this decision is a
new ticket, never an absorption into this one.

**No `conversation_id` on the frame.** `Envelope` carries no conversation id, so
payloads that need one carry it themselves (`ToolUsePayload`, `SessionErrorPayload`).
This frame deliberately does not, and the omission is a security property rather
than an oversight: it is the same reasoning `TypeRequestDebugBundle` records —
"no `conversation_id`, no field an attacker could use to select another session's
data — mirroring `TypeInterrupt`". An upload lands in the conversation the
authenticated v2 session is already on, decided daemon-side by #1744 from session
context, so a client cannot steer bytes into another conversation's directory by
naming one. Retrieval's *request* verb does name a conversation (#1746's AC 1),
but that is a different frame and its validation is #1746's problem. Adding a
conversation id here would also exceed AC 1's settled metadata list.

**`int`, not `uint32`, for `Index` and `TotalChunks`.** Unsigned would make a
negative index structurally impossible, but it would move that rejection into
`encoding/json`'s decode error — splitting one reject class ("out-of-range chunk
index", #1741's AC 3) across two layers and two wire codes. Plain `int` keeps the
whole range check in one place: #1741's guard is `Index < 0 || Index >= TotalChunks`,
table-drivable over negative and too-large alike. It also matches the package
convention (`DebugBundleChunkPayload.Seq`, `DebugBundleDonePayload.Total`). Say so
in the doc block so a later reviewer does not "fix" it. The same holds for a
negative `Size`.

**`Index`/`TotalChunks`, not `Seq`/`Total`.** `Seq` on the bundle stream carries a
strict succession contract (the next chunk's `Seq` must equal the count already
seen); this frame does not — a receiver addresses by index and rejects duplicates
and out-of-range values rather than requiring succession. Naming the field `Seq`
would import that contract by association. `TotalChunks` rather than `Total`
because `Size` sits on the same struct, and a bare `total` beside a `size` reads
as a byte count to a client developer.

### 3. The four-site cascade — and why the fourth site's obvious fix is wrong

Adding a `Type*` constant is a **four**-site cascade, and the fourth is in another
package. `go test ./internal/protocol/...` can be green while `make check` is red.

| # | File | Site | Edit |
|---|---|---|---|
| 1 | `internal/protocol/compat_test.go` | `TestIsKnownAppType`'s `cases` table | Add an `{"attachment_chunk-rejected", TypeAttachmentChunk, false, ErrUnknownType}` row with the one-line comment the neighbouring v2 rows carry. |
| 2 | `internal/protocol/compat_test.go` | `v2OnlyTypes` | Add `TypeAttachmentChunk: true` under a `// v2 attachment vocabulary.` comment. |
| 3 | `internal/protocol/compat_test.go` | `TestTypeConstants_V1V2Partition`'s `all` list | Add the constant. The test's closing size assertion balances only if sites 2 and 3 land together. |
| 4 | `cmd/pyry/relay_guard_test.go` | `excludedTypes` | Add `"TypeAttachmentChunk"` with a reason — see below. |

Site 1's comment should say what the neighbours say and what is true here: an old
phone must never receive this frame, **and** rejection is also what keeps the type
off the v1 inbound path — this frame really is inbound on the upload leg, so
`IsKnownAppType` rejecting it is the structural bar against a v1 client sending
one into `dispatch.Route`.

**Site 4 is harder here than it was for any recent analogue.** The seven newest
`excludedTypes` entries (`TypeModelList`, `TypeSlashCommandList` and friends) all
justify `"push"` with *this slice declares no inbound request verb* — which is
**false** for a frame whose upload leg is inbound. Copying a neighbour would put a
lie in the guard.

Filing it in `inboundTypes` instead fails a different assertion: Assertion #1
requires every inbound type to be registered in `cmd/pyry/relay.go`'s `Handlers`
map or `internal/relay/v2session.go`'s `dispatchAppFrame` switch, and this slice
writes no dispatch. It would be red by construction.

So the entry belongs in `excludedTypes` with its **own** label and a reason that is
actually true — the inbound leg has no handler yet. `TypeHello`'s
`"handshake"` entry is the precedent: a phone → binary type deliberately not filed
inbound, carrying its own label and a comment explaining the borderline. The label
string is free text; Assertion #3 checks membership, not the value. Something like
`"pending handler (#1744)"`, with a comment recording:

- the frame is bidirectional, so `"push"` would be wrong;
- the inbound leg has no dispatch entry yet, so `inboundTypes` would fail
  Assertion #1;
- #1744 adds it to the `dispatchAppFrame` switch, at which point this entry moves
  to `inboundTypes` as `"switch-intercepted"`.

That last line is the handoff #1744 reads. Without it, #1744's developer finds an
entry filed under the wrong half of a disjoint partition and no note saying why.

### Data flow

```
upload (this frame, inbound)          retrieval (this frame, outbound)
  client splits file                    daemon reads stored file
    ↓ N × attachment_chunk                ↓ N × attachment_chunk
  (Noise-encrypted app envelope)        (Noise-encrypted app envelope)
    ↓ #1744 dispatchAppFrame              ↓ #1746 push path
  #1741 reassembly (checks claims)      client reassembles by Index
    ↓                                     ↓ verifies Size + SHA256
  #1743 storage                         file
```

Neither leg exists yet. The diagram is the contract the two legs are built
against, not work in this slice.

## Concurrency model

None. This slice adds two declarations and four test-data entries. No goroutines,
no channels, no shutdown sequence, no shared state. `AttachmentChunkPayload` is a
plain value type: safe to copy, and — like every payload in this package — carrying
no synchronisation of its own. Callers that share one across goroutines share the
`Data` backing array, which is #1741's and #1746's concern, not the type's.

## Error handling

No failure modes are introduced. The type has no constructor, no validator and no
`MarshalJSON`/`UnmarshalJSON` override; decode failures are `encoding/json`'s and
surface as the existing `protocol.malformed`.

Explicitly **not** in this slice, and each one's owner:

| Concern | Owner |
|---|---|
| Per-chunk raw size bound and its worst-case proof | #1753 |
| Reject codes (`attachment.*`) and the published contract | #1751 |
| Duplicate / out-of-range index, disagreeing total, digest and size mismatch | #1741 |
| Filename sanitisation, path containment, conversation-id validation | #1743 |
| Mapping rejects to wire codes on the session | #1744 |
| Serving refusals on retrieval | #1746 |

Validation deliberately does not live on the type. The house rule is that
`internal/*` primitives return Go sentinels and the dispatcher maps them to dotted
wire codes at the call site (`docs/PROJECT-MEMORY.md` § Project-level conventions);
a validator here would be a second place the rules are decided and the two could
disagree silently. The doc block's job is to make the claims **visible** to the
layer that does check them.

## Testing strategy

No new test functions. The four cascade edits are the verification, and they are
structural rather than behavioural:

- **`TestIsKnownAppType`** proves an old (v1) client is refused the type in both
  senses — it never receives one, and it cannot send one into `dispatch.Route`.
  The new row is red before site 1 lands only in the sense that the constant does
  not exist; its value is as a permanent pin.
- **`TestTypeConstants_V1V2Partition`** goes red the moment the constant exists
  and sites 2 + 3 have not landed: `!inV1 && !inV2` reports it unclassified, and
  the closing size assertion catches a half-edit.
- **`TestEveryInboundV2TypeHasHandler`** (Assertion #3, in `cmd/pyry`) goes red the
  moment the constant exists and site 4 has not landed. This is the failure
  `go test ./internal/protocol/...` cannot see.

Verify with `make check`, not `go test ./internal/protocol/...`. A green protocol
package proves nothing about site 4.

No round-trip or fixture test belongs here — #1753 owns encoding fixtures, and a
throwaway round-trip in this slice would be dead weight the moment they land.
`AttachmentChunkPayload` having no producer is expected and matches #1704 and
#1726, both of which declared a payload before anything emitted it; staticcheck's
`unused` does not flag exported identifiers in this mode.

## Open questions

1. **`MimeType` on the retrieval leg.** Nothing in the current slices records the
   client's declared mime type to disk, so #1746 may have to re-derive or default
   it when serving the file back. That is #1743's storage-layout call (whether the
   sidecar metadata persists filename and mime type alongside the bytes), not a
   change to this frame — the field exists on the type either way. Flagged so
   #1743 decides it deliberately rather than discovering it in #1746.
2. **Whether `Filename` and `MimeType` need declared length bounds.** #1753's cap
   proof has to fill the metadata to its worst case, and an unbounded string makes
   the bound unprovable rather than merely generous. #1753's body already names
   this as its own design call; recorded here only so that if bounds are chosen,
   they land as constants in #1753 and **not** retrofitted onto this type.
3. **Nothing else.** The completion frame, the conversation id, the field
   representations and the guard classification are all closed above, deliberately,
   because six slices are blocked on this doc block and an open question here
   becomes six divergent guesses later.

## Scope check (re-applied to this written spec)

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** — `internal/protocol/codes.go`, `internal/protocol/attachments.go` |
| Total written work | ≤ 400 lines | **~155** — ~45 (const + doc block), ~85 (payload + doc block, the larger half of it the `SECURITY:` block the security pass added), ~12 (three `compat_test.go` sites), ~12 (guard entry) |
| New exported types or interfaces | ≤ 5 | **1** type (+1 constant) |
| Consumer call sites needing simultaneous update | ≤ 10 | **4** |
| Acceptance criteria | ≤ 5 | **3** |
| Distinct error/reject branches in a state machine | ≤ 10 | **0** |

Within `size:s` on every line. Most of the work is doc-block prose, which is the
deliverable rather than overhead — six blocked slices read it as their contract.

## Security review

**Verdict:** PASS (second pass; the first pass was FAIL — see below)

**First pass found four MUST FIX items**, all of them the same shape: this slice
ships no executable code, so every security property it can have lives in the doc
block, and the first draft described the fields' *semantics* while leaving their
*trust* properties to the reading list. They are addressed by the new § The
`SECURITY:` doc block, which turns AC 3 from prose-to-improvise into an
enumerated deliverable. The four were: no stated requirement to write a
`SECURITY:` block at all; AC 3's never-logged rule absent from the design body;
`TotalChunks`/`Size` documented as shape constraints with nothing said about
allocation; and `AttachmentID` not declared as needing canonical-shape validation
before use as a path component.

**Findings:**

- **[Trust boundaries]** MUST FIX (fixed). The boundary is unusually hard here: one
  Go type carries client-authored data inbound and daemon-authored data outbound,
  and the type system gives no signal — a developer in #1746 holding an
  `AttachmentChunkPayload` cannot tell from the type where its `Filename` came
  from. Splitting into two types is the one fix the type system offers and it is
  ruled out upstream (it is exactly the drift the one-frame decision exists to
  prevent), and a direction *field* would itself be a client claim. So the
  mitigation must be documentation plus a single checking layer, and § The
  `SECURITY:` doc block item 1 now carries it. Against scattering, § Error
  handling's owner table assigns each claim class exactly one owner: content
  claims (index, total, size, digest) to #1741, path claims (filename, attachment
  id) to #1743. No claim is checked in two places.
- **[Tokens, secrets, credentials]** No findings — the slice declares no token, key
  or credential. Adversarially, the nearest thing is `AttachmentID`, which becomes
  an object reference once #1743 and #1746 resolve it to a path; the doc block now
  states it is not a capability, not secret and not unguessable, so it can never be
  the only thing between a caller and a file.
- **[File operations]** MUST FIX (fixed). No file operation happens in this slice,
  but two of its fields become path input downstream, and only one of them was
  covered by anybody's acceptance criteria: #1743's AC 2 covers the *filename* and
  its AC 3 the *conversation id*, while `AttachmentID` — the field #1746 resolves
  to a path — fell between them. Unvalidated into `filepath.Join`, a client-chosen
  id is a traversal. The constraint is now stated where the field is declared,
  naming `conversations.ValidID` as the canonical-shape precedent; the
  deterministic check remains #1741/#1743's, per the belt-and-suspenders rule that
  the doc block is advisory and the enforcement is code.
- **[Subprocess / external command execution]** No findings — nothing in this slice
  or in any currently-filed consumer passes an attachment value to `exec.Command`.
  #1744's notes exclude prompt construction explicitly, so the eventual slice that
  hands an attachment to claude is unfiled; when it is written, `Filename` and
  `MimeType` remain display strings there for the reasons the doc block already
  states.
- **[Cryptographic primitives]** SHOULD FIX (fixed). `SHA256` is a claim, not a
  MAC: the same party supplies bytes and digest, so a match proves the transfer
  was uncorrupted and says nothing about content safety. The block now says so,
  and warns against the content-addressed-retrieval shortcut that would promote a
  non-secret digest into an access token. Constant-time comparison is deliberately
  **not** required — the digest is not a secret and both peers are already
  authenticated by the Noise IK handshake, so a timing channel on it yields
  nothing. The canonical form (lowercase hex, exact equality, `HashToken`'s form)
  is pinned so that neither a case-insensitive comparison (a hole) nor an
  uppercase-intolerant one (an availability bug) gets invented.
- **[Network & I/O]** MUST FIX (fixed). The sharpest hazard on the type: `TotalChunks`
  and `Size` are attacker-chosen integers, and the ticket's own framing says the
  total is "what a receiver would size an accumulator from". A single ~60 KB frame
  claiming `TotalChunks` of 2³¹−1 is a multi-gigabyte allocation before any
  content check can possibly run, because the digest is only verifiable once all
  bytes are held. The doc block now forbids allocating from a claim and supplies
  the free cross-check available at the *first* chunk — `TotalChunks` must equal
  `ceil(Size / <#1753's per-chunk bound>)`, both within the receiver's own limits.
  Per-frame size is already bounded by the transport (the 65519-byte application
  envelope) and by #1753's producer-side constant; per-upload byte totals and
  concurrent-upload counts are #1741's AC 4.
- **[Error messages, logs, telemetry]** MUST FIX (fixed). AC 3's "content-bearing
  and never logged" was absent from the design body. It is now stated with the
  reason it binds harder here than on `DebugBundleChunkPayload`: those are
  daemon-authored diagnostics, these are a user's private file. `Filename` is
  added to the never-log-raw rule on two independent grounds — a filename is often
  private in itself, and a client-supplied string in a line-oriented log is a
  log-injection shape. Enforcement is #1744's AC 3 ("no attachment bytes appear in
  any log line"). A reject that echoes the *claimed* size or digest leaks nothing
  — the client already holds both.
- **[Concurrency]** No findings, and the reason is worth recording rather than
  asserting: this slice spawns no goroutine and holds no lock, and `encoding/json`
  allocates a **fresh** slice when decoding a base64 field, so `Data` never aliases
  a reused connection read buffer. That is what rules out a TOCTOU in which a
  later frame mutates already-accumulated bytes after their digest check. #1741 may
  therefore retain `Data` without copying — a non-obvious contract it would
  otherwise have to rediscover. Partial-upload state is in-memory only, so a crash
  mid-transfer loses the transfer and leaves nothing partial on disk provided
  #1743 uses the house atomic-write recipe.
- **[Threat model alignment]** No findings. The adversary this frame faces is an
  authenticated **paired** device: the Noise IK pairing gate refuses unpaired
  devices at handshake with 4401 and they never reach `dispatchAppFrame` (the
  reasoning `TypeRequestDebugBundle` records, per ADR 025 § Security model). That
  bounds severity — there is no unauthenticated attacker on this path — without
  eliminating it, since a stolen phone or a malicious client build is inside the
  gate. Under v2's end-to-end encryption the relay stays transport-only and can
  neither read nor forge a chunk, which is the settled decision this frame
  preserves by carrying no relay-visible routing field.
- **[Out of scope, named]** Attachment expiry and abandonment of a partial upload
  (deliberately excluded from #1741, its own unfiled ticket); attachment cleanup
  and eviction (deferred by #1743's settled design); prompt construction from an
  attachment (excluded by #1744).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
