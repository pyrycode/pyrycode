# #2053 — stream a stored attachment as cap-respecting `attachment_chunk` frames

Split from #1746 (grandparent #1684). The outbound half of the retrieval leg:
a host path in, a conforming stream of `attachment_chunk` frames out. It ships
**unwired** — nothing calls it when it lands, exactly as #812 shipped
`StreamBundle` ahead of #813.

## Size

Over the size-S table's 800-line line at ~1050 lines of total written work, and
under every other line of it: **one** production source file (new), no consumer
call sites, five acceptance criteria, no new exported type, and no state machine
with reject branches — the reject vocabulary is #2054's.

Stated rather than hidden, and built as one ticket for two independent reasons.
The **floor** rule: the only split available is chunk-builder from push-loop,
and the chunk-builder half would be consumed by exactly one sibling in the same
family, which makes it lines inside a ticket rather than a ticket of its own.
When the floor and the ceiling disagree the floor wins. The **split-depth** gate
closes it independently — the parent chain reads parent #1746, grandparent
#1684, so no split is available at this depth at all. No `needs-human:sizing`
marker is added: with no split proposed there is no judgement to park.

## Files read

- `internal/relay/v2bundlestream.go` → `StreamBundle`, `bundleEnvelopes`,
  `ReassembleBundle`, `bundleChunkBytes` — the structural twin. Its transport
  mechanics are copied; its chunking arithmetic is not (three published
  differences, below).
- `internal/relay/v2bundlestream_test.go` → `openBundleSession`,
  `decryptStreamFrames`, `patternBlob`, `waitForEnvelopes`, `decodeNoiseMsg`,
  `silentLogger` — the harness this slice's tests reuse rather than rebuild.
- `internal/relay/v2session.go` → `Push` — the asynchronous send path, and its
  contract: never blocks, never touches `s.send`, control-class frames are never
  dropped, `ErrConnNotFound` for a conn that is not open.
- `internal/relay/v2session_handshake.go` → `maxNoisePayloadBytes` — the
  measurement target for the cap test.
- `internal/relay/v2session_attachment.go` → `handleAttachmentChunk`,
  `attachmentReply` — the inbound leg. Its file header fixes the loggable set
  (conn id, attachment id, index, total) and this slice inherits it.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload`,
  `MaxAttachmentChunkBytes` — the eight fields, and the constant's doc block
  carrying the envelope arithmetic, the raw-bytes unit and the standing rule
  that a failing cap test lowers the constant.
- `internal/attachments/admission.go` → `CheckDeclaration`, `maxUploadBytes` —
  the receiver-side equality this stream's chunk stride must satisfy, and the
  16 MiB bound that makes a whole-file read safe.
- `internal/attachments/storage.go` → `ResolvePath`, `Store`, `EnsureDir` — the
  caller's resolution step, `Store`'s PRECONDITION-and-trust posture that the
  entry point copies, and `ResolvePath`'s logging obligation on the returned
  path.
- `internal/attachments/filename.go` → `SanitizeFilename` — why the outbound
  `filename` is a sanitised rendering rather than the client's own bytes, and
  its allowlist (`a-zA-Z0-9_.-`, ≤ 255 bytes), which is what bounds this leg's
  real worst-case metadata.
- `docs/protocol-mobile.md` § Attachments — the published chunking arithmetic,
  the two terminal signals, and the three sentences AC#4 corrects.
- `docs/knowledge/features/v2-session-manager-state-machine-debug-bundle-streaming-streambundle-bund.md`
  — #812's recorded lessons: the two walls `Push` clears, the
  belt-and-suspenders cap-test pattern, and the non-vacuity shape of its
  no-bytes-logged test.

## Context

`§ Attachments` publishes retrieval as the same frame, daemon-authored, flowing
binary → phone in reply to `request_attachment` (#2052), correlated by
`in_reply_to`. #2054 owns the request handler — registry validation,
`attachments.ResolvePath`, and the merged `attachment.not_found`. This slice
performs none of that: it is handed an already-resolved, already-validated host
path plus the attachment id those bytes are stored under, and its job starts at
the file.

No ADR is warranted. Every decision here is already published in
`docs/protocol-mobile.md` § Attachments or in `MaxAttachmentChunkBytes`' doc
block; this slice implements a written contract rather than choosing one.

## Design

One new production file, `internal/relay/v2attachmentstream.go`, holding the
whole outbound-attachment-stream concept: the pure chunker, the method, and the
receiver-contract oracle. It mirrors `v2bundlestream.go`'s file shape and
depends on `internal/protocol` plus `internal/attachments` (for
`CheckDeclaration` in the oracle) — the same import
`v2session_attachment.go` already takes.

**No new local chunk-size constant.** The bound is read from
`protocol.MaxAttachmentChunkBytes` at every use, never copied as a local 45000,
matching `CheckDeclaration`'s stated discipline: one place it can drift.

### `attachmentEnvelopes` — the pure chunker

```go
func attachmentEnvelopes(attachmentID, filename string, blob []byte, inReplyTo uint64) ([]protocol.Envelope, error)
```

Splits `blob` into `max(1, ceil(len(blob)/MaxAttachmentChunkBytes))`
`TypeAttachmentChunk` envelopes with 0-based ascending `Index`, every one
carrying all eight fields, every one carrying `InReplyTo`. Pure — no manager, no
filesystem — which is what lets the shape criteria be tested without a session.

Three differences from `bundleEnvelopes`, each already published:

- **A zero-byte file is one chunk, not zero.** The `max(1, …)` is the whole
  definition of the zero-byte transfer. `bundleEnvelopes` yields zero chunks for
  an empty blob; right there, wrong here.
- **No completion frame, and none is coming.** `TotalChunks` rides every chunk,
  so a receiver knows the count from frame one. `DebugBundleDonePayload` has no
  peer here.
- **The stride is exact.** Every chunk but the last carries exactly
  `MaxAttachmentChunkBytes` raw bytes. This is not stylistic:
  `attachments.CheckDeclaration` enforces `TotalChunks == max(1, ceil(Size /
  bound))` as an **equality**, so any other stride produces a stream this
  project's own receiver refuses.

The ceiling is written division-then-remainder rather than
`(n + bound - 1) / bound`, matching `CheckDeclaration`, which bans the second
form. Overflow is not reachable here (`len(blob)` is an `int` bounded by the
upload leg's 16 MiB), so this is convention rather than necessity — and the two
sides of one contract reading the same way is the point.

**Three of the eight fields are derived from the bytes, not from anything a
client declared.** `Intake.Receive` calls `Store(dir, chunk.Filename, data)`,
which persists the bytes under the *sanitised* filename and nothing else: the
declared `mime_type` is discarded outright, and the declared `size` and `sha256`
are checked at admission against the assembled bytes and then dropped. So:

| Field | Source |
|---|---|
| `attachment_id` | the caller's, echoed on every chunk |
| `index` / `total_chunks` | the chunker's arithmetic |
| `filename` | `filepath.Base(path)` — `SanitizeFilename`'s rendering, the leaf `ResolvePath` answered |
| `mime_type` | `http.DetectContentType(blob)` — the content, not the name and not the client's claim |
| `size` | `int64(len(blob))` |
| `sha256` | `hex.EncodeToString(sha256.Sum256(blob))`, lowercase |
| `data` | this chunk's raw slice |

`http.DetectContentType` is the stdlib WHATWG mimesniff implementation, reads at
most the first 512 bytes, and is **total** — every input has an answer,
`application/octet-stream` as the fallback and `text/plain; charset=utf-8` for a
zero-byte file. That last one is a consequence of the algorithm rather than a
choice, and it is pinned by a test so it reads as decided. `mime.TypeByExtension`
is deliberately *not* used: the ticket's requirement is that a file whose name
and client-declared type disagree with its content is described by its content.

**Its output is drawn from a closed set of daemon-authored constants** — the
fixed signature table, plus the two defaults — so an attacker choosing the bytes
selects *which* of those strings is emitted and cannot inject text into it. That
is the concrete difference from the inbound declared value, which is an arbitrary
255-byte client string, and it is why the derived field carries neither the
log-injection hazard nor the envelope-budget hazard the declared one does. It
does **not** make the value trustworthy to act on: see the doc correction below.

**`Data` is never nil.** `json.Marshal` renders a nil `[]byte` as `null` and an
empty non-nil one as `""`, and the published field is a base64 **string** that is
always present — `attachment_chunk_zero.json`'s `"data":null` is the zero-value
pin, not a conforming transfer. Slicing a nil blob yields a nil slice, so the
zero-byte chunk's `Data` is normalised to `[]byte{}` explicitly.

`EventID` is left nil on every envelope, so `forwardEnvelope`'s reconnect-replay
dedup guard is inert here, matching `bundleEnvelopes`. `ID` is set to the index
for debuggability and is non-load-bearing — the receiver correlates on
`InReplyTo` and keys on `attachment_id`, exactly as `attachmentReply` records.

### `StreamAttachment` — the entry point

```go
func (m *V2SessionManager) StreamAttachment(ctx context.Context, connID, attachmentID, path string, inReplyTo uint64) error
```

Reads the whole file, builds the envelopes, enqueues each in order via `Push`.

**PRECONDITION, carrying the whole security property:** `path` must be one
`attachments.ResolvePath` answered for a conversation the authenticated session
is already on, and `attachmentID` must be the id those bytes are stored under.
This function resolves nothing and validates neither — the same
trusts-its-caller posture `Store` documents for the `dir` `EnsureDir` returned,
for the same reason: re-deriving here would fork a check that has to happen
before a path is built at all, and the reject vocabulary that answers a failed
check belongs to the handler that can correlate a reject to a request.

Two consequences the doc block must state outright, because the caller is the
one that has to honour them. A path this function did not get from `ResolvePath`
carries **no containment guarantee**: every traversal defence is the equality
check upstream, and nothing here would notice. And a path naming a **non-regular
file** misbehaves rather than erroring — `os.ReadFile` on a FIFO blocks
indefinitely, wedging whichever goroutine the caller runs this on. Neither is
guarded here: `ResolvePath` answers only entries whose `Type().IsRegular()` holds
and only paths equal to the one its two ids build, so both are unreachable
through the sanctioned caller, and a guard here would be a second, weaker copy
of a check that already exists.

Positional parameters rather than a config struct, matching `StreamBundle` and
`attachmentReply`. Three of them are strings and a swap is not a compile error;
that hazard is answered by the precondition and by the caller being a single
sibling, not by minting a type this slice has no second use for.

A whole-file read rather than a streaming one: the upload leg bounds one stored
attachment at 16 MiB (`maxUploadBytes`), so the read is comfortably bounded and
a streaming read would buy nothing but a second failure mode mid-stream.

### `ReassembleAttachment` — the receiver-contract oracle

```go
func ReassembleAttachment(frames []protocol.Envelope, attachmentID string, inReplyTo uint64) ([]byte, error)
```

Exported, pure, and — like `ReassembleBundle` — a **receiver-contract reference
and test oracle**, not a production consumer: the daemon has no inbound caller
for it. Without it the first and third acceptance criteria are not testable at
all, since "reassembling them in index order recovers the file byte for byte" is
a statement about a reassembler.

It **selects one transfer** by matching both `attachment_id` and `in_reply_to`,
skipping every other frame. That models what a client with concurrent retrievals
over one connection must do, and it is what makes the two correlators'
non-redundancy demonstrable rather than asserted: `in_reply_to` says which frame
this answers, the payload id says which transfer it belongs to.

On the selected set it applies the published receiver rules:

- **Never allocate from a claim**: `attachments.CheckDeclaration(TotalChunks,
  Size)` runs on the first selected chunk, before anything is sized. Reusing the
  project's own enforcement point rather than re-deriving the cross-check is
  what keeps the oracle a reference to the real contract.
- Addresses by `Index` and never appends; rejects a duplicate index, an index
  outside `[0, TotalChunks)`, and a `TotalChunks` or `Size` or `SHA256`
  disagreeing with the transfer's earlier chunks.
- Completion is every index in `[0, TotalChunks)` arrived exactly once; the
  assembled length is then compared against `Size` and `sha256(assembled)`
  against `SHA256` as lowercase hex for exact equality.
- On **any** failure returns `(nil, err)` — never partial or corrupted bytes.

## Concurrency model

No new goroutine and no new lock. `StreamAttachment` is safe to call from any
goroutine, including a future #2054 handler on the `Run` goroutine or on a
conn's `appFrameWorker`: `Push` never blocks and never touches `s.send`, so it
sidesteps both walls #812 recorded — the 65535-byte frame cap and the
`handlerOutboundBuf` reply channel, whose small fixed buffer a multi-chunk file
would overrun.

Ordering and no-drop follow from `Push`'s published behaviour rather than from
anything added here: `TypeAttachmentChunk` is control-class (not
`TypeAssistantDelta`), so the `pushQueue` drop policy never evicts it; per-conn
FIFO plus sequential seal on the single `Run` goroutine means chunks are sealed
in enqueue order under a monotonic Noise nonce. A session that closes mid-stream
has its still-buffered chunks dropped by `forwardEnvelope`'s `V2StateOpen` gate
at drain time, never sealed for an un-authenticated peer.

The function returns on successful **enqueue**, not on delivery, matching
`StreamBundle`. That is what makes `attachment.stream_aborted` #2054's to emit
and not this slice's: a per-frame seal failure after enqueue is invisible here.

## Error handling

Three failure modes, all returned and none logged with anything the log rules
forbid.

1. **The file cannot be read.** `os.ReadFile` returns an `*fs.PathError` whose
   `Error()` prints the path, and `ResolvePath`'s doc block bans logging the
   path it answered because its leaf is a sanitised client filename. So the
   error is rebuilt around the **stripped cause** (`errors.As` to `*fs.PathError`,
   wrap `pe.Err`), naming the attachment id and the operation and no path. That
   keeps `errors.Is(err, fs.ErrNotExist)` working for #2054 while making a
   caller that logs the error unable to leak the path through it.
2. **A payload fails to marshal.** Defensive, as in `bundleEnvelopes` — a closed
   struct of strings, ints and a byte slice does not fail in practice. No
   content bytes in the error.
3. **`Push` returns an error.** Returned first-error-and-stop: `ErrConnNotFound`
   means the conn is not open, so there is no point continuing.

**Logging.** One content-free debug line on success — `event`, `conn_id`,
`attachment_id`, `total_chunks`, `in_reply_to`, `bytes` — mirroring
`StreamBundle`'s. The loggable set is `handleAttachmentChunk`'s: conn id,
attachment id, index and total may be logged; **bytes, filename, digest and host
path never**. That is AC#5, and it binds the error values as well as the log
calls, since a returned error can be logged by the caller.

## Testing strategy

New `internal/relay/v2attachmentstream_test.go`, reusing `openBundleSession`,
`decryptStreamFrames`, `patternBlob`, `waitForEnvelopes` and `decodeNoiseMsg`
rather than rebuilding a harness.

Pure-chunker tests (no session):

- **Shape, table-driven over sizes** — 0, 1, `bound-1`, `bound`, `bound+1`,
  `3*bound`, `3*bound+4242`: chunk count equals `max(1, ceil(size/bound))`,
  indices are 0-based ascending, every chunk declares the same `TotalChunks`,
  every chunk but the last carries **exactly** the bound, and the last carries
  the remainder.
- **Zero-byte file** — exactly one chunk, `len(Data) == 0`, `TotalChunks == 1`,
  and the marshalled payload carries `"data":""` rather than `"data":null`.
- **Declaration conformance** — for every size above,
  `attachments.CheckDeclaration(TotalChunks, Size)` returns nil. This is the
  direct proof that the stride satisfies the equality this project's own
  receiver enforces.
- **Derivation** — a blob of PDF magic bytes under the filename `photo.png`:
  `MimeType` is `application/pdf` (the content, not the name), `Size` is the real
  length, `SHA256` is the real lowercase-hex digest. Plus the pinned zero-byte
  answer, `text/plain; charset=utf-8`.
- **Every chunk carries `InReplyTo`**, non-nil and equal to the request id — the
  assertion that distinguishes this stream from `bundleEnvelopes`, which leaves
  it nil on every frame.

Oracle tests (`ReassembleAttachment`, table-driven): happy multi-chunk,
single-chunk, zero-byte, out-of-order arrival **accepted** (addressed by index,
the deliberate weakening from `Seq`'s strict succession), duplicate index,
index out of range, disagreeing `TotalChunks`, size mismatch, digest mismatch,
missing index (incomplete), malformed payload, and a declaration
`CheckDeclaration` refuses. Plus **two interleaved transfers** on one frame
slice, proving the selection needs both correlators.

Session tests (through the real handshake and `Push`):

- **Round trip** — a multi-chunk file streamed to an open conn, every frame
  decrypted in order, `ReassembleAttachment` recovers the bytes byte for byte.
- **Every frame within the cap** — a file large enough to need many chunks,
  streamed under a **255-byte filename of `<`**: the true escape worst case the
  cap arithmetic budgets 1530 bytes for, since `encoding/json` HTML-escapes each
  one to six bytes. Deliberately *not* the sanitised worst case. In practice the
  leaf is `SanitizeFilename`'s output and cannot contain an escaping byte, so the
  budget is slack this leg never spends — but that holds only under the
  precondition, and a cap test that leans on the precondition tests the caller
  rather than the frame. Decrypt every emitted frame and assert
  `len(ciphertext) <= maxNoisePayloadBytes` — measured on the real serialised
  frame, never checked against the arithmetic that chose the bound.
  Belt-and-suspenders, different fabric: the conservative constant is the belt,
  this deterministic per-frame measurement is the suspenders. **If it ever fails,
  lower `MaxAttachmentChunkBytes` — never raise the cap.**
- **Nothing logged** — stream a recognisable byte pattern from a file with a
  distinctive name under a `LevelDebug` capturing handler. Assert the
  `v2.attachment.stream` line **is** present first (without that the rest is
  vacuous), then assert no record at any level contains the blob bytes, their
  base64, the filename, or the host path. Every needle is non-empty and
  distinctive by construction.
- **Not-open conn** — `ErrConnNotFound` propagates; **missing file** — the error
  is `fs.ErrNotExist` under `errors.Is` and its text names no path.

## Doc corrections (AC#4)

Three sentences in `docs/protocol-mobile.md` § Attachments say `filename` and
`mime_type` are stored and echoed back verbatim. Each is wrong in its own way:
`mime_type` is not stored at all, and `filename` is stored *sanitised*.

1. **The "Trust and content hygiene" paragraph** — replace the "arrive
   attacker-chosen at upload time, are stored, and are echoed back verbatim"
   clause with what actually happens on each leg: outbound, `filename` is
   `SanitizeFilename`'s rendering and `mime_type`, `size` and `sha256` are
   derived from the stored bytes.
2. **The `mime_type` field-table row** — "the client's declared media type" is
   true inbound only; the row gains the outbound half.
3. **The `attachment_stored` restatement** — "claims inbound and laundered
   client input outbound" becomes claims inbound, daemon-derived outbound.

**What the correction must not do is weaken the client's obligation.** Deriving
host-side improves *provenance* — genuinely daemon-authored rather than an
attacker-chosen string handed back — and not *trust*: the value is still
computed from bytes an attacker chose, so a sniffed `text/html` is exactly as
dangerous to render as a declared one. The **MUST NOT dispatch on `mime_type` in
any way that grants the content privileges** rule and the sanitise-before-
rendering rule on `filename` both survive the edit unchanged.

Also updated: the two status sentences saying #2053 is future work, which land
false the moment this commits. They become "the stream has landed, unwired" —
`request_attachment` still has no answer, which is #2054's, so **Nothing answers
it yet** stays true and stays. One dated `## Changelog` entry, newest first.

## Open questions

1. **Does the oracle enforce the digest, or only the ordering?** Resolved in
   favour of enforcing it: the published receiver rules make the length and
   digest comparison part of completion, and an oracle that skipped them would
   be a reference to a weaker contract than the one the document publishes.
2. **Does the oracle import `internal/attachments` for `CheckDeclaration`, or
   re-derive the cross-check?** Resolved in favour of importing.
   `v2session_attachment.go` already takes that import for the same
   errors-are-values reason, and a re-derivation would be a second place the
   equality can drift from the constant it is written against.
3. **Should the read be bounded independently of `maxUploadBytes`?** Resolved
   no, pending evidence. `maxUploadBytes` is unexported receiver policy, the
   ticket states the bound explicitly, and the failure mode — a file larger than
   16 MiB in the daemon's own 0700 state directory — requires write access that
   already defeats `devices.json`. Recorded in the security review as an
   accepted residual rather than silently skipped.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. One boundary, and it is explicit: bytes
  cross file → memory at the single `os.ReadFile` in `StreamAttachment`, and
  every one of the eight published fields is derived from that one slice or from
  the caller's two arguments. The `path` / `attachmentID` boundary is *upward* —
  the caller owns it — and the doc block states that as a PRECONDITION in
  `ResolvePath`'s strongest form rather than leaving it to convention. No
  downstream consumer holds these bytes: they are marshalled and enqueued.
- **[Trust boundaries]** Noted, and it strengthens the design rather than
  weakening it: `http.DetectContentType`'s output is drawn from a **closed set of
  daemon-authored constants**, so an attacker choosing the file's bytes selects
  which string is emitted and cannot inject text into it. Recorded in § Design
  and repeated in the code doc block, because it is the concrete difference
  between the derived value and the arbitrary 255-byte declared one.
- **[Tokens, secrets, credentials]** Not applicable by design. Nothing is minted,
  stored, rotated or revoked. Two near-misses were checked and both hold: the
  `attachment_id` is published as **not a capability** and this slice only echoes
  one it was handed, and the `sha256` cannot become a fetch key here because the
  entry point takes a **path, not a digest** — content-addressed retrieval is
  structurally unavailable rather than merely unimplemented.
- **[File operations]** No MUST FIX; three residuals, all bounded by the same
  fact. **Traversal:** this slice concatenates nothing — the equality-containment
  check is `ResolvePath`'s and the plan says so. **TOCTOU:** the check-then-use
  window between `ResolvePath` reading the directory and `os.ReadFile` opening
  the path is real and is widened slightly by this slice; accepted on
  `EnsureDir`'s published bound, since exploiting it needs write access inside
  the daemon's own `0700` state directory and anyone holding that can already
  rewrite `devices.json`. **Symlinks and non-regular files:** `os.ReadFile`
  follows symlinks and blocks indefinitely on a FIFO, and `ResolvePath`'s
  `Type().IsRegular()` filter is the only thing keeping either off this path.
  Pressed on whether a *remote* paired client can reach them: it cannot —
  `Store` is the only writer into an attachment directory, it writes through
  `os.CreateTemp` plus `os.Rename` with a `SanitizeFilename`d leaf, and
  `os.Rename` replaces a symlink rather than following it. So all three need
  local write access, which is the accepted bound and not a new one. Both are
  named in the doc block so #2054's reviewer sees them when choosing the
  goroutine this runs on. **Permissions / atomic writes:** not applicable — this
  slice creates no file and writes nothing to disk.
- **[Subprocess / external command execution]** Not applicable by design. No
  `exec`, no shell, no environment handling anywhere in the slice.
- **[Cryptographic primitives]** No findings. `crypto/sha256` for the digest,
  rendered lowercase hex, which is the representation the published contract
  fixes; no RNG is needed and none is used. **Nonce reuse was the one to check
  and it is structurally excluded:** the slice never touches `s.send` — `Push`
  takes only `m.queues` under `pushMu` and the seal happens on the single `Run`
  goroutine — so no concurrent `Encrypt` is reachable however many callers stream
  at once. Constant-time comparison was considered for the oracle's digest
  equality and rejected on the merits: the digest is published as neither secret
  nor a capability, so a timing channel on it leaks nothing.
- **[Network & I/O]** No findings on the output side; the per-frame envelope cap
  is the whole point of the design and is measured on the real sealed ciphertext
  at the true escape worst case. On the input side, the whole-file read is
  bounded at 16 MiB by `maxUploadBytes` at admission, which a remote client
  cannot exceed. Context cancellation is observed per frame, since `Push` checks
  it at entry on every call.
- **[Errors, logs, telemetry]** One finding, already in the design.
  `os.ReadFile` returns an `*fs.PathError` whose `Error()` prints the path, and
  `ResolvePath` bans logging that path because its leaf is a client filename — so
  a caller that logged this slice's error verbatim would defeat AC#5 through it.
  The error is therefore rebuilt around the **stripped cause**, preserving
  `errors.Is(err, fs.ErrNotExist)` for #2054 while making the leak unavailable.
  The `json.Marshal` error paths were checked for content echo and carry none —
  a `[]byte` field cannot produce an unsupported-value error, and invalid UTF-8
  in the filename is replaced rather than refused. The debug line is counts-only.
  No telemetry.
- **[Concurrency]** No findings. No goroutine is spawned, so none can leak; no
  lock is taken, so there is no ordering to get wrong; the chunker is pure and
  the method holds no state. Two concurrent retrievals on one conn interleave in
  the push queue **by design**, which is exactly why the receiver needs both
  correlators — the oracle's two-interleaved-transfers test is the check that
  this actually works rather than an assumption that it does. Mid-stream session
  close drops still-buffered chunks at `forwardEnvelope`'s `V2StateOpen` gate,
  never sealing for an un-authenticated peer.
- **[Threat model alignment]** § Security model threat 1 (prompt injection) does
  not land: nothing here is claude-authored and nothing here becomes prompt
  content — the bytes travel phone-ward, the opposite of `send_message`'s
  `attachment_ids`. The relay stays transport-only and sees ciphertext, unchanged
  by this slice. **Out of scope and named:** any bound on *concurrent* retrievals
  is #2054's, which § `request_attachment` already publishes as
  receiver-configured and unpublished; the existing `pushQueueByteCeiling`
  (32 MiB, latching into session teardown) is the backstop in the meantime, so
  the deferral is not an unguarded gap.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-03

## Revisions

### 2026-09-03 — implementation departures from the committed design

Two places where Phase B did not do what Phase A wrote down, plus one field
name. Recorded here rather than quietly folded into the design above, so the
diff between the plan and the code reads as decisions instead of drift.

**1. The oracle REJECTS a wrong-transfer frame; the plan said it skips one.**
§ Design described `ReassembleAttachment` as selecting one transfer "by matching
both `attachment_id` and `in_reply_to`, **skipping** every other frame." Writing
the test exposed that as too weak. The two filters answer different questions and
only one of them is a skip:

- A frame answering **another request** (`in_reply_to` differs) is another
  transfer's, and skipping it is right — it is what makes concurrent retrievals
  over one connection separate cleanly, and it mirrors `ReassembleBundle`
  skipping a non-bundle frame.
- A frame answering **this request** while naming another transfer is not
  somebody else's frame. It is the daemon answering the right ask with the wrong
  bytes, and skipping it would let a receiver silently assemble a short or
  wrong file. It is now an error.

That distinction is also what makes the ticket's non-redundancy claim
demonstrable rather than asserted: this is precisely the failure the payload id
catches and `in_reply_to` cannot. Pinned by the `wrong-transfer-under-this-request`
row of `TestReassembleAttachment`.

**2. The cap is measured twice, in two fabrics, where the plan had one test.**
§ Testing strategy put the escape worst case (255 filename bytes of `<`, which
`encoding/json` expands to six bytes each) into the session test. That does not
work as written — the session test streams from the filesystem, so its filename
has to be a real on-disk name, and pinning the worst case there would have meant
choosing between the two properties. Split instead:

- `TestAttachmentEnvelopes_FrameWithinCapAtWorstCaseMetadata` (pure) measures the
  **marshalled envelope** at the full escape worst case the arithmetic budgets.
- `TestStreamAttachment_EveryFrameWithinCap` (session) measures the **real sealed
  ciphertext** against `maxNoisePayloadBytes`, under a real 255-byte name.

Strictly stronger than the single test planned, and it keeps AC#2's "measured on
the real serialised frame" satisfied by the second while the first covers the
metadata case a real filename cannot reach.

**3. The debug line logs `chunks`, not `total_chunks`** — `StreamBundle`'s field
name, kept so the two stream lines read the same way. No content difference.

### 2026-09-03 — a fourth instance of the corrected claim, filed as #2056

The § Attachments wording sweep found the same wrong claim in
`protocol.AttachmentChunkPayload`'s own doc block, where its SECURITY paragraph
calls the outbound fields "daemon-authored and **trustworthy**" — a stronger and
more dangerous form than any of the three sentences AC#4 names, since it reads
as a licence to dispatch on `mime_type`. Two field-contract bullets in the same
block carry the narrower version.

**Not fixed here.** AC#4 scopes the correction to `docs/protocol-mobile.md`
§ Attachments and names three locations, all of which are corrected; this fourth
one is in a production source file the ticket deliberately kept outside its
one-file scope. The inconsistency is pre-existing rather than introduced by this
slice — § Attachments already said the outbound values must not be read as
trustworthy while that block said they are — so it is filed as **#2056** and left
for its own ticket.
