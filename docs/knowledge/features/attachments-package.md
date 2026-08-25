# `internal/attachments` — inbound attachment-chunk accumulation

New package (#1769), first slice of the blocked family split from #1741/#1766:
holds one inbound attachment upload's chunks in memory, addressed by index, and
refuses a stream whose framing contradicts what the transfer declared at
admission. In-memory only — no disk, no wire codes, no logger, no lock.

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

## Sentinels and discard semantics

Four exported sentinels, `errors.New("attachments: …")` house style. Three
discard the transfer (latched on first refusal — every later `Add` and
`Assemble` returns the identical wrapped error, and the held chunk bytes are
dropped at the moment of refusal so a poisoned accumulator holds no memory
past that point). `ErrIncomplete` is the fourth and does **not** latch or
discard — it is the resumable answer a later chunk can turn into bytes.

| Sentinel | Latches? |
|---|---|
| `ErrTotalChunksMismatch` | yes |
| `ErrIndexOutOfRange` | yes |
| `ErrDuplicateIndex` | yes |
| `ErrIncomplete` | no |

Mapping these to the wire's single `attachment.invalid_chunk` code (`internal/protocol/codes.go`'s `CodeAttachmentInvalidChunk`) is #1744's job at the dispatch site — this package emits no wire codes and does not import `codes.go`.

**Presence comes from map-key membership, never from the stored value.**
`Add`'s duplicate check is a two-value lookup (`_, dup := a.chunks[i]`); a
`chunks[i] != nil` check would silently accept a re-sent zero-byte chunk (both
`nil` `Data` and an empty non-nil slice are legitimate "received" values). The
zero-byte-attachment criterion exists specifically to catch this class of bug.

## A mutation-testing lesson worth carrying into #1770

The architecture spec's testing strategy credited scenario 2's
*identical-bytes* duplicate row as the sole red test for the
`!= nil`-vs-two-value-lookup mistake above. Measured under that mutant (`go
test -overlay`, no worktree write), that row stays **green**: it re-sends ten
non-empty bytes, so a non-nil value is still parked at the key and the buggy
check still reports the duplicate correctly. The mutant only surfaces where
the stored value is legitimately nil — a **zero-byte** re-send — which is why
the shipped tests add `TestAccumulator_DuplicateZeroByteChunk` as the actual
sole red for it (confirmed: exactly one failing test under the mutant).

**Generalization:** a test row aimed at "presence must come from the key, not
the value" has to use the value that is *indistinguishable from absent* for
the type in play (here, zero-byte `Data`). Any other value can leave a
presence-from-value mutant alive while looking like coverage. #1770 adds the
sibling claim checks (`size`/`sha256`) against this same file — worth
re-checking each new presence- or value-based assertion there against this
same trap before trusting a sole-redness claim on paper.

## Three shipped properties with no test pin (as of #1769)

Code review passed #1769 with three SHOULD-FIX/NIT findings that are real
gaps in the spec's testing strategy, not deviations from it — all three
behaviors are correctly implemented but unpinned, so a future refactor of this
file could silently regress them:

- `Assemble` on a `NewAccumulator(0, …)` (declared `total_chunks == 0`)
  returns `ErrIncomplete` rather than a vacuous empty-bytes success — no test
  asserts this; deleting the `totalChunks < 1` guard leaves the package green.
- A refused transfer's `chunks` map is set to `nil` at the moment of refusal
  (so a hostile client's bytes don't outlive the refusal) — no test asserts
  `a.chunks == nil` after a reject; deleting that line also leaves the
  package green.
- `Assemble` never fast-paths a single-chunk transfer by returning
  `chunks[0]` directly (it always allocates a fresh slice, so a caller
  mutating one returned slice cannot corrupt the accumulator or a second
  caller's copy) — no test currently assembles a single-chunk transfer with
  non-empty bytes to catch a fast-path regression.

#1770 touches this same production file and test file to add the `size`/`sha256`
checks; land these three pins there rather than leaving them to be
rediscovered by another mutation pass.

## Blocked family (not this slice)

- **#1770** — checks the assembled bytes against the transfer's latched
  `size` and `sha256`. Reads the two fields `NewAccumulator` already latches
  and this slice never reads.
- **#1767** — admission: the first-chunk `total_chunks`/`size` cross-check,
  the byte bound, the in-flight-upload count, and the registry (`attachment_id`
  → `*Accumulator`) that needs its own lock — `Accumulator` itself carries
  none, because it is fed serially by one session's `appFrameWorker` goroutine.
- **#1742** — expiry/abandonment of a partial upload.
- **#1743** — storage, filename sanitisation, resolving `attachment_id` to a
  path.
- **#1744** — wires the dispatch site: maps the three discard sentinels to
  `CodeAttachmentInvalidChunk` via `errors.Is`, and is the first production
  caller of this package.
