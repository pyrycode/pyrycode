# `internal/attachments` — inbound attachment-chunk accumulation

Package (#1769, #1770, #1772, #1776, #1777, #1787, #1781, #1788, #1782, #1795, #1796, #1784, #1880, #1817), fourteen slices of the family split from #1741/#1766:
holds one inbound
attachment upload's chunks in memory, addressed by index, refuses a stream
whose framing contradicts what the transfer declared at admission (#1769),
and — once the transfer is complete — checks the assembled bytes against the
transfer's declared length and lowercase-hex sha256 before yielding a single
byte (#1770). Bounds the retained bytes of one upload to a
receiver-configured, unpublished ceiling, refusing at either the declared
size or the accumulated total (#1777, see § "Per-upload byte bound" below),
and holds the uploads currently in flight in a conn-keyed `Registry` (#1787),
whose only exported way in runs both declaration checks, the incumbent
look-up, and a daemon-wide entry-count ceiling under one lock acquisition
before admitting anything (`Admit`, #1788, #1795, #1796, see § "In-flight
upload registry" below). `Registry.Deliver` (#1784) is the other half of that
mechanism: it routes a later chunk to the admitted entry and gives the slot
back on completion or refusal, so `maxInFlightUploads` bounds *live* transfers
rather than a lifetime quota.
Accumulation and admission stay in-memory — no disk, no wire codes, no
logger — but the package is no longer in-memory-only end to end: `EnsureDir`
(#1781, see § "Directory resolution and creation" below) resolves and creates
the on-host directory one attachment is filed under, and `Store` (#1782, see
§ "Writing attachment bytes" below) writes the verified bytes into it — the
package's only two functions that touch a filesystem. `Accumulator` itself
still carries no lock; synchronisation lives in `Registry` alone.

`SanitizeFilename` (#1772, `filename.go`) is unrelated in shape but shares the
package: a pure, stateless function turning a client-supplied
`AttachmentChunkPayload.Filename` into one safe host-side path component —
never called on `AttachmentID`, whose contract is a canonical-shape check that
*rejects* rather than this treatment. **The returned component is not
unique** — `a/b` and `a_b` both answer `a_b`, every unusable name answers the
fixed fallback, and APFS folds case — so #1782 must key stored files by
`attachment_id`, not by this component (`EnsureDir`, #1781, already keys the
directory that way), and #1782's file-write code should be the only caller
that reads `Filename` at all. Sanitising also does
not make a name loggable: it removes the log-injection shape but not the
independent privacy reason § Attachments already bans logging a filename for.

- Wire contract: [`protocol-package.md`](protocol-package.md) § "v2 attachment-chunk vocabulary" (`AttachmentChunkPayload`, `MaxAttachmentChunkBytes`).
- Design reference this package deliberately diverges from: [`v2-session-manager.md`](v2-session-manager.md) § "Debug-bundle streaming" — `ReassembleBundle`.
- Ticket record: [codebase/1769.md](../codebase/1769.md) does not exist — this is a lessons-only package overview per the documentation phase's per-ticket-file retirement (2026-08-19); read the ticket/spec/PR directly for implementation detail.

## What it copies from `ReassembleBundle`, and what it doesn't

`ReassembleBundle` (`internal/relay/v2bundlestream.go`) is the nearest existing
receiver for a payload that splits across frames, but it runs the other
direction (daemon → client) and is a pure oracle with no inbound production
caller. `Accumulator` copies exactly one property from it and diverges on two:

- **Copies** the all-or-nothing posture — `Assemble` either yields the
  complete bytes or fails cleanly, never partial or corrupted output.
- **Does not copy the ordering rule.** `ReassembleBundle` demands strict `Seq`
  succession; the published attachment contract stores each chunk **by
  index**, so chunks may arrive in any order. Copying the neighbouring rule
  was the specific mistake the contract warns against by name — see the
  scenario-1 fixture below.
- **Does not copy the signature.** `ReassembleBundle` takes every frame at
  once and returns once, so its "incomplete" is terminal. `Accumulator`'s
  incomplete is resumable: a later `Add` can turn the same instance into a
  complete one. That difference is why this is a stateful type, not a
  function.

## Admission layer (#1776)

`CheckDeclaration` (`admission.go`) is the first-chunk `total_chunks`/`size`
cross-check, run before an `Accumulator` exists so a claim is refused before
either number sizes anything. It is pure and stateless — no lock, no
goroutine — unlike `Accumulator`, which is fed serially by one session's
`appFrameWorker`. It lives in this package rather than `internal/protocol`
because the check is receiver policy: `protocol` ships wire vocabulary with
no validator, by design.

- **The negative-`size` guard cannot be folded into the equality check** —
  measured, not assumed: `max(1, ceil(-1 / bound))` is 1 under either natural
  ceiling form, so the pair `size` −1 / `total_chunks` 1 passes the
  cross-check unaided. It needs its own guard ahead of the equality, sharing
  `ErrInvalidDeclaration` but pinned by a separate test row.
- **The ceiling must be division-then-remainder, never `(size + bound - 1) /
  bound`.** That numerator wraps for every `size` within `bound`-1 of
  `math.MaxInt64`, and `max(1, …)` launders the wrapped negative result back
  to 1 — silently admitting the largest declaration the wire can carry
  (measured: wraps to −204963823041216 where the correct count is
  204963823041218). Also compare `int64(totalChunks) != want`, never
  `int(want) != totalChunks` — narrowing re-introduces the same class of wrap
  on a platform where `int` is 32 bits.
- **`ErrInvalidDeclaration` is a distinct sentinel from `ErrTotalChunksMismatch`
  on purpose.** The latter means a chunk disagrees with the count the
  transfer was already admitted under; `CheckDeclaration` runs before any
  count is admitted, so there is nothing yet to disagree with.
- **Admission runs in front of `Accumulator` and does not replace its own
  guards.** `NewAccumulator` stays exported and constructible without passing
  through `CheckDeclaration`, so `Assemble`'s `totalChunks < 1` clause is
  still load-bearing — it is not dead code made redundant by this layer.
- **An arithmetically-conforming but enormous declaration is admitted here on
  purpose** (`size` `math.MaxInt64` with its honest matching count) — this
  layer checks consistency between the two claimed numbers, not their
  magnitude. The per-upload byte bound is a separate concern (#1777); adding
  an absolute size cap here would be scope creep this ticket deliberately
  declined.

## What a successful `Assemble` does not mean

Both #1770 checks pass when the assembled bytes match what the transfer
declared — that's integrity, not authenticity. The same party (the uploading
client) supplies both the bytes and the declared digest, so a match proves
the transfer wasn't corrupted in transit and proves nothing about whether the
content is safe. `protocol.AttachmentChunkPayload`'s doc block states this
directly; it matters here because the consumers of a successful `Assemble`
— #1772's filename sanitiser, #1781/#1782's storage, #1746's retrieval — are the
ones who'd otherwise read a green `Assemble` as a safety verdict rather than
a corruption check.

The declared `sha256` is also deliberately **not** promoted to a lookup key
anywhere in this package: there's no content-addressed retrieval ("know the
hash, fetch the blob"), which is a requirement from
`AttachmentChunkPayload`'s SECURITY block, not an oversight.


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Per-upload byte bound (#1777)](attachments-package-per-upload-byte-bound.md) — Two rungs enforce one constant, `maxUploadBytes` (16 MiB, `admission.go`), and share one sentinel, `ErrUploadTooLarge`: `CheckDeclaredSize`…
- [In-flight upload registry (#1787, #1788, #1795, #1796, #1880, #1881, #1817)](attachments-package-in-flight-upload-registry.md) — `Registry` (`registry.go`) gives `Accumulator` somewhere to live between chunks: a map from `uploadKey{connID, attachmentID}` to an…
- [Directory resolution and creation (#1781)](attachments-package-directory-resolution-and-creation.md) — `EnsureDir` (`storage.go`) resolves and creates the on-host directory one attachment of one conversation is filed under —…
- [Writing attachment bytes (#1782)](attachments-package-writing-attachment-bytes.md) — `Store` (`storage.go`) takes the directory `EnsureDir` returned, a client-supplied filename, and already-verified bytes, and writes them…
- [Sentinels and discard semantics](attachments-package-sentinels-and-discard-semantics.md) — Eight exported sentinels, `errors.New("attachments: …")` house style, in three families plus two non-discarding outliers. 
- [Mutation-testing lessons (measured across #1769, #1770, #1772, #1787, #1795, and #1796)](attachments-package-mutation-testing-lessons-measured-across.md) — This package's sole-redness claims are measured with `go test -overlay` (mutants applied via an absolute-path JSON manifest, no worktree…
- [Blocked family (not landed)](attachments-package-blocked-family-not-landed.md) — `total_chunks`/`size` cross-check landed as `CheckDeclaration` (#1776, see § "Admission layer" above), the per-upload byte bound landed as…
