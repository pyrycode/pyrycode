# Sentinels and discard semantics

Nine exported sentinels, `errors.New("attachments: …")` house style, in
three families plus two non-discarding outliers. Six discard the transfer
(latched on first refusal — every later `Add` and `Assemble` returns the
identical wrapped error, and the held chunk bytes are dropped at the moment
of refusal so a poisoned accumulator holds no memory past that point).
`ErrIncomplete` is the outlier and does **not** latch or discard — it is the
resumable answer a later chunk can turn into bytes.

| Sentinel | Family | Latches? |
|---|---|---|
| `ErrTotalChunksMismatch` | framing | yes |
| `ErrIndexOutOfRange` | framing | yes |
| `ErrDuplicateIndex` | framing | yes |
| `ErrSizeMismatch` (#1770) | integrity | yes |
| `ErrDigestMismatch` (#1770) | integrity | yes |
| `ErrUploadTooLarge` (#1777) | resource | yes (from `Add`); n/a (from `CheckDeclaredSize` — no `Accumulator` exists yet) |
| `ErrTooManyUploads` (#1796) | resource | n/a — refused before any `Accumulator` exists, by `Registry`'s admission gate rather than `Add`/`Assemble` |
| `ErrIncomplete` | — | no |
| `ErrUnknownUpload` (#1784) | — | n/a — no `Accumulator` is looked up for the pair; refused by `Registry.Deliver` itself before either `Add` or `Assemble` runs |

`ErrUploadTooLarge` is the one sentinel not scoped to `accumulator.go`'s var
block, whose opening sentence reads "Sentinel errors returned by `Add` and
`Assemble`" — it is also returned by an admission function that runs before
an `Accumulator` exists, so it lives in `admission.go` beside
`ErrInvalidDeclaration` instead.

Mapping these to the wire is #1897's job at the dispatch site — this package
emits no wire codes and does not import `codes.go`, and #1896's `Intake`
(the package's first composite caller, see § "Chunk intake driver") hands
every sentinel here back verbatim rather than mapping any of them. The
mapping is deliberately many-to-one within the framing and integrity
families: all three framing sentinels answer `CodeAttachmentInvalidChunk`,
both integrity sentinels answer `CodeAttachmentIntegrityFailed`.
`ErrUploadTooLarge` is one-to-one — `CodeAttachmentTooLarge` — and permanent
for that file, in contrast to `ErrTooManyUploads`'s
`CodeAttachmentTooManyUploads` (#1796), which is marked transient because it
clears once other uploads finish. `ErrNoConversation` (#1896) never got a
dedicated mapping and was deleted by #2143 along with the resolver that
raised it: the destination now arrives as a caller-validated `Receive`
parameter, so this package no longer has a "resolved no conversation" case to
raise a sentinel for at all. The refusal moved one layer up, into
`internal/relay`'s `KnownConversation` gate ahead of this package, which
answers the already-published `attachment.invalid_chunk` rather than mint a
replacement here — see [Chunk intake driver](attachments-package-intake-driver.md)
§ "The conversation is resolved once, on the completing chunk only".
Distinguishing
sentinel from sentinel is wanted in-process, for this package's own tests and
for the daemon's logs, not on the wire — a single corrupt transfer must still
resolve to exactly one wire code, which is why `reject` (the shared latch
primitive every discarding family calls) has to run for a resource refusal
too: without the latch, a chunk sent after an `ErrUploadTooLarge` reject on
an already-refused transfer would fall through to a framing check, and one
transfer would emit two different wire codes at #1897's dispatch site.

`Assemble`'s two integrity comparisons (#1770) both read the assembled `out`
slice itself — never a proxy computed from the chunk map or the per-chunk
lengths — so a future regression in the assembly loop above can't slip past
a green check. That's also why `Assemble` must keep allocating a fresh slice
on every call rather than fast-pathing a single-chunk transfer to
`chunks[0]`: with a fresh copy, the bytes verified are the bytes returned, and
no later mutation of the accumulator's stored chunks — or of a caller's own
copy of a previous return — can retroactively change what an already-returned
slice contains. `internal/update`'s `VerifySHA256` is the nearest existing
"bytes against a declared hex digest" comparison in the repo and folds case
with `strings.EqualFold`; that's correct where it lives (a digest already
lowercased by its own file's parser) and wrong here — the published contract
in `docs/protocol-mobile.md` calls a case-insensitive or prefix comparison a
hole, in the same sentence that resolves the seeming tension with an
uppercase sender by requiring clients to send lowercase. The comparison here
is plain `!=` against `hex.EncodeToString`'s (lowercase) output — no
normalisation, no `subtle.ConstantTimeCompare` either, since both the bytes
and the claimed digest are attacker-supplied, so timing leaks nothing the
attacker doesn't already hold.

Neither integrity error string carries the declared `sha256` itself — it's an
attacker-chosen, JSON-decoded string headed for #1897's line-oriented log, the
same hazard `AttachmentChunkPayload`'s `Data` and `Filename` are already
banned from error strings for. `ErrDigestMismatch`'s message instead carries
the **computed** digest (daemon-authored, fixed shape, one-way) and
`len(a.sha256)` as a diagnostic for a truncated or empty claim. `len` on a Go
string is a **byte** count, not a rune count, and the message currently calls
it "characters" — code review flagged this as a NIT (not fixed, since the
value itself is still bounded and injection-free): a 64-emoji claim would
report 256. Worth remembering before reaching for `len(str)` on any other
attacker-supplied, JSON-decoded string in this codebase — say "bytes" or use
`utf8.RuneCountInString`, not both loosely.

**Presence comes from map-key membership, never from the stored value.**
`Add`'s duplicate check is a two-value lookup (`_, dup := a.chunks[i]`); a
`chunks[i] != nil` check would silently accept a re-sent zero-byte chunk (both
`nil` `Data` and an empty non-nil slice are legitimate "received" values). The
zero-byte-attachment criterion exists specifically to catch this class of bug.
