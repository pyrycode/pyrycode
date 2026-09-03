# Outbound attachment stream (#2053) — `StreamAttachment` + `attachmentEnvelopes` + `ReassembleAttachment`

`StreamAttachment(ctx, connID, attachmentID, path string, inReplyTo uint64) error`
moves one stored attachment to one addressed, open, authenticated conn as
`attachment_chunk` frames, each correlated to the request that asked for it by
`InReplyTo`. It is the outbound half of the retrieval leg published in
`docs/protocol-mobile.md` § Attachments; the handler that resolves a
`request_attachment` and drives it is #2054. This slice shipped it
**unwired**, test-driven only — exactly as [debug-bundle streaming (#812)](v2-session-manager-state-machine-debug-bundle-streaming-streambundle-bund.md)
shipped `StreamBundle` ahead of the request verb that drove it (#813). New file
`internal/relay/v2attachmentstream.go` holds the whole concept: the pure
chunker, the method, and the reassembly oracle — the same file shape as
[`v2bundlestream.go`](v2-session-manager-state-machine-debug-bundle-streaming-streambundle-bund.md),
whose transport mechanics it copies (the asynchronous `Push` path, so a file
of any size cannot overrun the 8-slot handler-reply buffer) and whose chunking
arithmetic it deliberately does not: a zero-byte file is one chunk rather than
zero, there is no completion frame (`TotalChunks` rides every chunk instead),
and the stride is **exact** because `attachments.CheckDeclaration` enforces
`TotalChunks == max(1, ceil(Size/bound))` as an equality rather than a bound.
`security-sensitive`; architect + code-review security passes both **PASS**.
See [`docs/specs/architecture/2053-attachment-retrieval-stream.md`](../../specs/architecture/2053-attachment-retrieval-stream.md).

**Three of the eight published fields are derived from the stored bytes, not
from anything a client declared** — `Store` persists only the bytes under a
*sanitised* filename; the upload's declared `mime_type` is discarded outright
and its declared `size`/`sha256` are dropped once checked at admission. So
`attachmentEnvelopes` derives `size` and `sha256` from the blob, `filename`
from `filepath.Base(path)` (the sanitised leaf `ResolvePath` answered, never
the client's own bytes), and `mime_type` from `http.DetectContentType(blob)` —
the content, not the name, so a file whose name and client-declared type
disagree with its content is described by its content. That derivation is
drawn from stdlib mimesniff's **closed set of daemon-authored constants**, so
an attacker choosing the file's bytes selects *which* constant is emitted and
cannot inject text into it — an improvement in *provenance*, not *trust*:
`mime_type` is still computed from attacker-chosen bytes, so a sniffed
`text/html` is exactly as dangerous to render as a declared one, and the
client-side MUST NOT-dispatch-on-`mime_type` rule is unchanged. This finding
corrected three sentences in `docs/protocol-mobile.md` § Attachments that said
`filename`/`mime_type` are "stored, and echoed back verbatim" — see that
document's Changelog for 2026-09-03. **A fourth instance of the same wrong
claim survived the sweep**: `protocol.AttachmentChunkPayload`'s own SECURITY
doc block calls the outbound fields "daemon-authored and trustworthy," a
stronger and more dangerous form than any sentence this ticket's AC named,
filed separately as [#2056](protocol-package-constants-codes-go-envelope-types-attachments.md)
rather than fixed here (out of this ticket's one-file scope) — **a
doc-correction AC that names specific locations does not imply those are the
only locations; grep the corrected phrase across the repo, including
production doc comments, not just the document the AC names.**

**The reassembly oracle rejects a wrong-transfer frame; it does not skip one.**
`ReassembleAttachment` selects a transfer by two correlators — `InReplyTo`
(which frame this answers) and the payload's `AttachmentID` (which transfer it
belongs to) — and the two need different handling on mismatch. A frame
answering *another* request is another transfer's and is **skipped**, the same
way `ReassembleBundle` skips a non-bundle frame; a frame answering *this*
request while naming a *different* `AttachmentID` is **rejected** as an error,
because that is the daemon answering the right ask with the wrong bytes, and
silently skipping it would let a receiver assemble a short or corrupted file
without ever seeing an error. Skipping was the design's first instinct (one
verb, "skip every other frame") and proved too weak once the reject-vs-skip
case was written as a test — it is also the concrete case that makes the two
correlators non-redundant rather than belt-and-braces, pinned by
`TestReassembleAttachment`'s wrong-transfer-under-this-request row. A receiver
addresses by `Index` and never appends, so out-of-order arrival is accepted —
deliberately weaker than `debug_bundle_chunk`'s strict-succession `Seq`, which
is why that neighbouring rule is the wrong one to copy here even though it is
the obvious one. `attachments.CheckDeclaration` runs on the first selected
chunk before anything is sized, reusing the project's own enforcement point
rather than re-deriving the cross-check.

**The envelope cap is measured in two fabrics because one test could not cover
both properties.** The plan called for a single session-level cap test at the
true escape worst case (a 255-byte filename of `<`, which `encoding/json`
expands to six bytes each). That does not work as written: a session test
streams from the real filesystem, so its filename has to be a real on-disk
name, and pinning the worst case there would force a choice between "real
file" and "worst-case metadata." Split instead —
`TestAttachmentEnvelopes_FrameWithinCapAtWorstCaseMetadata` (pure) measures the
**marshalled envelope** at the full escape worst case the arithmetic budgets,
and `TestStreamAttachment_EveryFrameWithinCap` (session) measures the **real
sealed ciphertext** against `maxNoisePayloadBytes` under a real 255-byte name —
belt-and-suspenders, different fabric, same pattern
[#812 recorded](v2-session-manager-state-machine-debug-bundle-streaming-streambundle-bund.md):
**if either ever fails, lower `MaxAttachmentChunkBytes`, never raise the cap.**
In practice the real leaf is `SanitizeFilename`'s output and cannot contain an
escaping byte, so the budget the pure test proves is slack the session leg
never spends — that holds only under the precondition below, and a cap test
that leaned on the precondition would be testing the caller, not the frame.

**`Data` is normalised to a non-nil empty slice for the zero-byte case.**
`encoding/json` renders a nil `[]byte` as `null` and an empty non-nil one as
`""`; slicing a nil blob yields a nil slice, so without the explicit
`chunk.Data = []byte{}` fixup a conforming zero-byte transfer would carry
`"data":null` against the published always-a-string contract.

**PRECONDITION carries the whole security property, and two consequences of it
are non-obvious enough to write down.** `path` must be one `ResolvePath`
answered for the conversation the authenticated session is already on;
`StreamAttachment` re-validates nothing. A path from anywhere else carries **no
containment guarantee** — the traversal defence is `ResolvePath`'s equality
check, and nothing here would notice its absence — and a path naming a
**non-regular file** misbehaves rather than erroring: `os.ReadFile` on a FIFO
blocks indefinitely, wedging whichever goroutine calls this. Both are
unreachable through the sanctioned caller (`ResolvePath` answers only regular
files at exactly the path its two ids build), so no guard was added here — a
guard would be a second, weaker copy of a check that already exists elsewhere.

**Errors are rebuilt around the stripped cause to keep a `PathError` from
leaking the host path.** `os.ReadFile`'s `*fs.PathError.Error()` prints the
path, and that path's leaf is a sanitised *client* filename `ResolvePath`'s
doc block already bans logging — so `strippedPathError` unwraps to `pe.Err`
before wrapping, preserving `errors.Is(err, fs.ErrNotExist)` for #2054 while
making the leak unavailable to a caller that logs the returned error verbatim.
The same never-log rule (conn id, attachment id, index, total loggable; bytes,
filename, digest, host path never) binds the one content-free debug line this
method emits on success — logged as `chunks`, matching `StreamBundle`'s field
name rather than the wire's `total_chunks`, so the two stream log lines read
the same way.

## Related

- [Debug-bundle streaming (#812)](v2-session-manager-state-machine-debug-bundle-streaming-streambundle-bund.md) — the structural twin this slice copies transport mechanics from and diverges from on chunking arithmetic.
- [Inbound `attachment_chunk` (#1897)](v2-session-manager-state-machine-inbound-attachment-chunk-attachmentintake-seam.md) — the inbound leg; fixes the loggable field set this slice inherits.
- [Attachment envelope types § retrieval](protocol-package-constants-codes-go-envelope-types-attachments.md) — `AttachmentChunkPayload`, `MaxAttachmentChunkBytes`, and the #2056 doc-block finding this slice's sweep surfaced but did not fix.
- [`attachments-package.md`](attachments-package.md) — `CheckDeclaration`, `ResolvePath`, `Store`, `SanitizeFilename` — the receiver-side equality and the storage/sanitisation posture this stream is built against.
