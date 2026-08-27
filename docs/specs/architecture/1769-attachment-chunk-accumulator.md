# #1769 — Accumulate one attachment's chunks by index and refuse bad framing

**Ticket:** [#1769](https://github.com/pyrycode/pyrycode/issues/1769) · `size:s` · `security-sensitive`
**Split from:** #1766 (closed), which split from #1741 (closed). Sibling #1770 adds the two claim checks on top of what this ships.

## Files to read first

| Read | Symbol / section | What to extract |
|---|---|---|
| `internal/protocol/attachments.go` | `AttachmentChunkPayload` | The eight always-present fields, and the `SECURITY:` / `NEVER ALLOCATE FROM A CLAIM` doc block. This slice is the receiver half of that block's first half — it is the type this package consumes and never re-declares. |
| `internal/protocol/attachments.go` | `MaxAttachmentChunkBytes` | Context only. This slice enforces **no** byte cap; the constant's two open consequences belong to #1767. Read it so you can see what you are *not* implementing. |
| `internal/relay/v2bundlestream.go` | `ReassembleBundle` | The all-or-nothing posture to copy (never partial, never corrupted output), and the two things not to copy: its strict `Seq` succession rule, and its take-every-frame-at-once signature. |
| `docs/protocol-mobile.md` | § Attachments → "Reassembly & integrity (the receiver's rules)" | The receiver's published rules: store at `index`, any arrival order, complete when every index in `[0, total_chunks)` arrived **exactly once**, three discard triggers. |
| `docs/protocol-mobile.md` | § Attachments, the sender's chunking arithmetic `total_chunks = max(1, ceil(size / 45000))` | Why a zero-byte file is **one chunk carrying zero bytes** rather than zero chunks. This is what the last AC implements. |
| `docs/knowledge/features/protocol-package.md` | § "v2 attachment-chunk vocabulary", the `Data` bullet and the `Index`/`TotalChunks` naming bullet | `Data` never aliases a reused read buffer, so no defensive copy; and why the fields are not named `Seq`/`Total`. |
| `internal/conversations/registry.go` | the `Err*` `var` block | The sentinel house style this package copies: `errors.New("<pkg>: <lowercase message>")`, grouped in one `var` block with a doc comment each. |
| `internal/eventring/ring.go` | package doc comment + `New` | The shape of a small in-memory, daemon-resident package. Note the package comment sits atop the primary file — there is no `doc.go` anywhere under `internal/`. |
| `internal/relay/v2session.go` | `appFrameWorker` | Exactly one goroutine per session, strict FIFO, no two handlers for one conn concurrent. This is the whole reason the new type carries no mutex. |
| `internal/protocol/codes.go` | `CodeAttachmentInvalidChunk` | Context only. All three framing sentinels map to this one wire code — **at #1744's dispatch site**, not here. This slice must not import or mention it in code. |

## Context

The daemon has never held partial state across inbound messages. The one payload that splits across frames today runs the other way (daemon → client) and its receiver-side reference, `ReassembleBundle`, is pure, exported, and has no inbound production caller. The three-way "copy this, not that" is in the ticket body and is the design's spine:

- **Copy** the all-or-nothing posture.
- **Not** its ordering rule — the published attachment contract is deliberately weaker than `Seq` succession, and copying the neighbouring rule is the mistake the contract warns about by name.
- **Not** its signature — `ReassembleBundle` takes every frame at once and returns once, so its *incomplete* is **terminal**. Here *incomplete* is **resumable**: a later chunk can turn it into bytes. That single difference is why this slice is a stateful type rather than a function.

Everything on the wire is already published (#1751/#1752/#1753) and is not re-decided here. What this slice decides is the Go shape of the receiver, and the four sentinels that make its three refusals plus its one non-refusal distinguishable in-process.

**No ADR is warranted.** The architectural decision — index-addressed reassembly rather than succession — was made and published by #1751, and this spec plus the package doc comment record the Go-level consequences. The documentation phase will likely want an `attachments-package.md` under `docs/knowledge/features/` once #1767/#1742/#1743 have filled the package out; it is not this ticket's deliverable and must not be an AC.

## Design

### Package: `internal/attachments` (new)

One new package, one new production file: `internal/attachments/accumulator.go`, with `internal/attachments/accumulator_test.go` beside it.

The package is new because the whole blocked family lands in it — #1767's admission and bounds, #1742's expiry, #1743's storage. It is not `internal/protocol`: that package holds wire vocabulary with no behaviour, a posture `MaxAttachmentChunkBytes`' own doc is explicit about ("a declared contract with no validator in this package"). It is not `internal/relay`: #1744 wires the dispatch site there and imports this.

### Constructor and type

```go
// NewAccumulator latches the transfer's three declared facts and returns an
// empty accumulator. It cannot fail: refusing an implausible declaration is
// the admission layer's job (#1767), which runs before this type exists.
func NewAccumulator(totalChunks int, size int64, sha256 string) *Accumulator
```

`Accumulator` is a struct with five unexported fields: the three latched declarations, `chunks map[int][]byte`, and `rejected error`.

- **All three declarations are latched even though this slice reads only `totalChunks`.** `size` and `sha256` come from the admission decision, not from the stream, and #1770 reads them directly — same package, no accessors, no signature change. Write a one-line comment on each saying so, so they do not read as dead weight at review. *(Verified: `staticcheck`'s `unused`/U1000 does not flag a field that a constructor's composite literal writes, so `make check` stays green with both fields unread.)*
- **`chunks` is `make(map[int][]byte)` with no capacity hint.** See § Security review — the hint is a claim-driven allocation wearing a different shape.
- **No `*slog.Logger`, and this package makes zero log calls.** The sentinel is what makes a refusal distinguishable to the logger at #1744's dispatch site, which is also the only place that knows the attachment id and conn id worth logging. Adding a logger here would put a per-reject log call in the accumulator and duplicate what the dispatch site must log anyway.
- **No mutex.** One accumulator is fed serially from `appFrameWorker` — one goroutine per session, strict FIFO. Say this in the type's doc comment as a **caller obligation**, not as a happy accident: synchronising the registry of in-flight uploads belongs to #1767/#1744, and to #1742's release path, never to this type.

### `Add`

```go
// Add stores one chunk's bytes at its index, or refuses and discards the
// transfer. Once refused, the transfer stays refused: every later Add and
// every later Assemble answers with the same error.
func (a *Accumulator) Add(chunk protocol.AttachmentChunkPayload) error
```

Checks run in this fixed order, and the order is part of the contract (a frame carrying two faults must yield one deterministic sentinel):

1. **Already rejected** → return the latched error verbatim.
2. **`chunk.TotalChunks != a.totalChunks`** → `ErrTotalChunksMismatch`. First, because a disagreeing count invalidates the frame of reference the range check below uses. Note this is *stronger* than the doc's "disagreeing with the stream's earlier chunks": the comparison is against the latched declaration, so even the very first chunk can be refused for disagreeing with what was admitted.
3. **`chunk.Index < 0 || chunk.Index >= a.totalChunks`** → `ErrIndexOutOfRange`.
4. **Index already present in `chunks`** → `ErrDuplicateIndex`. Presence is decided by the **two-value map lookup** (`_, dup := a.chunks[i]`), never by `a.chunks[i] != nil` — see the trap below.
5. Otherwise store `chunk.Data` at `chunk.Index` and return `nil`.

On any refusal: wrap the sentinel with the offending numbers (`fmt.Errorf("attachments: chunk index %d ...: %w", …, ErrIndexOutOfRange)`), **latch that wrapped error** in `rejected`, **set `chunks` to nil** so a discarded transfer stops holding its bytes, and return it. Later calls return the identical error value, so the offending index survives into whatever #1744 logs. Tests match with `errors.Is`, never `==`.

**The trap this design exists to avoid.** `m[k] = nil` creates the key; `_, ok := m[k]` reports `true` for it. An implementation that tests `a.chunks[i] != nil` for the duplicate check silently accepts a re-sent zero-byte chunk, and — worse — an implementation that computes completeness by counting non-nil values reports a zero-byte attachment as permanently incomplete. Both are exactly what the last AC catches, and both come from the same mistake: **inferring presence from the bytes rather than from the key.**

### `Assemble`

```go
// Assemble returns the transfer's bytes once every index has arrived exactly
// once. It neither consumes nor mutates the accumulator, so an incomplete
// transfer can be completed by a later Add and assembled then.
func (a *Accumulator) Assemble() ([]byte, error)
```

1. `rejected != nil` → `(nil, a.rejected)`. A discarded stream never yields bytes, however complete it looks.
2. `a.totalChunks < 1 || len(a.chunks) != a.totalChunks` → `(nil, ErrIncomplete)`.
3. Otherwise concatenate `chunks[0] … chunks[totalChunks-1]` into a freshly allocated slice sized from the **sum of the arrived chunk lengths**, and return it with a nil error.

Three points worth stating in the doc comment:

- **`ErrIncomplete` is not a refusal.** It does not latch and does not discard. It is the resumable answer the third AC pins, and it is the one sentinel here that a caller must not map to `attachment.invalid_chunk`.
- **`totalChunks < 1` is never complete.** Without that clause the completion test is vacuously true for a declared count of zero and a zero-chunk transfer assembles to empty bytes — a success from a declaration the published contract forbids (`total_chunks >= 1`). It is not a fourth sentinel: no index is admissible for such a transfer, so *permanently incomplete* is the honest answer, and refusing the declaration itself is #1767's decision to make once, at admission.
- **Never fast-path the single-chunk case** by returning `chunks[0]` directly. `Assemble` returns a fresh slice on every call; a caller mutating a returned slice must not be able to corrupt the accumulator or a second caller's copy.

### Sentinels

Four exported sentinels in one `var` block, house style `errors.New("attachments: …")`, one doc comment each:

| Sentinel | Raised by | Discards the stream? | Maps to (at #1744, not here) |
|---|---|---|---|
| `ErrTotalChunksMismatch` | `Add` | yes | `attachment.invalid_chunk` |
| `ErrIndexOutOfRange` | `Add` | yes | `attachment.invalid_chunk` |
| `ErrDuplicateIndex` | `Add` | yes | `attachment.invalid_chunk` |
| `ErrIncomplete` | `Assemble` | **no** | nothing — not a client-visible refusal |

The many-to-one mapping is deliberate: distinguishability is wanted in-process, for this slice's tests and for the daemon's logs, not on the wire.

### What this slice deliberately does not do

- **No `attachment_id` check.** The caller looks the accumulator up *by* id, so a foreign chunk cannot reach it; a check here would invent a fourth framing sentinel no criterion covers. The field is never a path component here.
- **No per-chunk `size` or `sha256` check.** Both are latched from the admitted transfer, so a later chunk restating either differently changes nothing that is checked. That is the security property, and it is why only a `total_chunks` disagreement is a framing refusal.
- **No byte cap, no in-flight count, no first-chunk cross-check** — #1767.
- **No expiry** — #1742. **No disk, no filename sanitisation** — #1743. **No wire codes, no dispatch** — #1744.

## Concurrency model

No goroutines, no channels, no locks. One `*Accumulator` is owned by one session's `appFrameWorker` goroutine and fed serially; the type documents that as its precondition. `Assemble` is a read that happens on the same goroutine.

The registry that maps `attachment_id` → `*Accumulator` is shared across handlers and *does* need synchronisation — that registry is #1767's, and its lock is #1767's. This spec deliberately ships no registry, so there is no lock ordering to document.

## Error handling

- Every refusal is a wrapped sentinel; callers match with `errors.Is`. Never compare error strings.
- Wrapped messages carry **`Index` and `TotalChunks` only**. Never `Data`, never `Filename` — an error string reaches a log, `Data` is a user's private file bytes, and a client-supplied `Filename` in a line-oriented log is a log-injection shape. `AttachmentChunkPayload`'s doc block states both rules; this is where the first consumer honours them.
- A discarded transfer releases its held bytes at the moment of refusal (`chunks = nil`), so the memory does not survive until #1742's reaper.
- No panics. A nil `chunk.Data` is a legitimate value, not a programmer bug.

## Testing strategy

Table-driven, `t.Parallel()`, stdlib `testing`, same-package (`package attachments`) so the tests can build an accumulator directly. One small helper builds an `AttachmentChunkPayload` from an index, a total and bytes, leaving the untouched fields zero — the helper filling only what the accumulator reads is itself a statement about what it reads.

Fixtures are small (tens of bytes). This slice enforces no byte cap, so a 45000-byte fixture would only make the tests slow and imply an enforcement that lives in #1767.

**1 — Round trip in any arrival order.** One fixture whose **last chunk is shorter than the rest** (e.g. 10 + 10 + 4 bytes, `totalChunks = 3`), fed in ascending, reverse, and a fixed shuffled order (a written-out permutation, not a seeded RNG). Each yields the exact original bytes.
*Sole red for:* any reassembly keyed to a uniform stride, and any implementation that appends in arrival order instead of addressing by index. A uniform-chunk fixture would let both pass.

**2 — Each framing fault has its own sentinel.** One row per fault, each row carrying exactly **one** fault so no row depends on the check ordering:
- duplicate index re-sent with **different** bytes → `ErrDuplicateIndex`
- duplicate index re-sent with the **identical** bytes → `ErrDuplicateIndex` (there is no idempotent-retry carve-out; completion is every index arriving *exactly once*)
- `Index == -1` → `ErrIndexOutOfRange`
- `Index == totalChunks` → `ErrIndexOutOfRange`
- a chunk whose `TotalChunks` is **greater** than declared → `ErrTotalChunksMismatch`
- a chunk whose `TotalChunks` is **less** than declared → `ErrTotalChunksMismatch`

*Sole red for:* dropping the `!= nil`→two-value fix on the duplicate check (the identical-bytes row alone), a one-sided range check (`< 0` or `>= totalChunks` alone), and a one-sided `total_chunks` comparison.

**3 — A refused transfer stays refused.** Driven from the same table as scenario 2, after each refusal:
- a subsequent **valid** `Add` returns the same sentinel, and
- `Assemble` returns the same sentinel and **no bytes** — asserted after feeding every remaining valid chunk, so the transfer would otherwise be complete.

*Sole red for:* deleting the latch, or checking the latch in `Add` but not in `Assemble`. Without the "feed the rest, then assemble" step, an `Assemble` that skips the latch stays green because the transfer is incomplete anyway.

**4 — Incomplete is resumable.** Feed all but one index → `Assemble` returns `ErrIncomplete` and no bytes. Feed the missing index into **the same accumulator** → `Assemble` yields the exact original bytes.
*Sole red for:* an `ErrIncomplete` that latches like a refusal, and for any `Assemble` that consumes or clears state.

**5 — Zero-byte attachment.** `totalChunks = 1`, one chunk at index 0, two rows: `Data: nil` and `Data: []byte{}`. Both complete; both yield zero bytes (`len(got) == 0`) and a nil error; neither yields `ErrIncomplete`.
*Sole red for:* presence inferred from `len(Data)` or from a nil check, in either the duplicate check or the completion count.

Optional, not required by any criterion: a row combining a bad `TotalChunks` with an out-of-range `Index`, asserting the documented precedence (`ErrTotalChunksMismatch` wins).

**Gates.** `make check` is the gate — `gofmt`, `go vet`, `-race`, `staticcheck`, and `cite-guard`. Every new comment in this package must cite by symbol; a `file.go:NNN` in a new comment fails the build with no depth or range exemption.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — the boundary is explicit and narrow.** Every field of an inbound `AttachmentChunkPayload` is an unverified claim, and `Add` is the single place this package reads one. It reads exactly two: `Index` and `TotalChunks`, both range- or equality-checked against the latched declaration before use, plus `Data`, which is stored and never interpreted. `AttachmentID`, `Filename`, `MimeType`, `Size` and `SHA256` are never read here — recorded in `Add`'s doc comment so a later reader does not mistake the omission for an oversight. Nothing in this package promotes a claim to a fact: the assembled bytes leave `Assemble` unchecked and no caller exists until #1744.
- **[Resource exhaustion — MUST FIX, fixed in this spec before commit] `make(map[int][]byte, totalChunks)` is an allocation from a claim.** The obvious constructor writes a capacity hint from the declared count, and a claimed `total_chunks` of 2³¹−1 pre-allocates the bucket array from a single ~60 KB frame — the exact attack `AttachmentChunkPayload`'s `NEVER ALLOCATE FROM A CLAIM` block names, in a shape that block does not literally spell out (it names `make([][]byte, TotalChunks)` and `make([]byte, Size)`). The design now specifies a **hint-free** `make(map[int][]byte)`, and the same rule governs `Assemble`: its output buffer is sized from the **sum of the arrived chunk lengths**, never from `a.size`. Both are stated as rules in § Design rather than left to the developer's judgement. This is what makes the slice safe standing alone, before #1767 lands.
- **[Resource exhaustion] Remaining unboundedness is real and is #1767's, named here rather than assumed away.** With no admission layer this type will hold up to `totalChunks` chunks of unbounded total size, and the registry that keys accumulators by a client-chosen `attachment_id` is itself unbounded state. Neither is closable here: the byte bound, the in-flight count and the `total_chunks`/`size` cross-check are #1767's, and #1767 blocks on #1770 which blocks on this. What this slice *does* bound is the key space — the range check means no accumulator ever holds more than `totalChunks` entries, so the map cannot grow past the declaration a bounded admission will later police. Nothing production-side feeds this package until #1744, so the gap is never live.
- **[Memory retention] No findings — refusal frees.** A discarded transfer sets `chunks = nil` at the moment of refusal, so a hostile client cannot park held bytes behind a poisoned accumulator until #1742's reaper runs. Without that, "discarded rather than continued" would be true of the *answers* and false of the *memory*.
- **[Aliasing] SHOULD FIX — the no-copy retention is a caller obligation, and this spec is where it first becomes one.** `Add` retains `chunk.Data` without copying, which is safe today only because `encoding/json` allocates a fresh slice per base64 field (`docs/knowledge/features/protocol-package.md` § "v2 attachment-chunk vocabulary", the `Data` bullet — surfaced by code review, not stated in the shipped doc block). A future caller that decodes into a reused buffer, or mutates `Data` after `Add` returns, silently corrupts an in-flight upload. Write it as an explicit precondition on `Add`'s doc comment so #1744's review has something to check against; do not add a defensive copy, which would double peak memory for every upload against the byte bound #1767 introduces.
- **[Error messages, logs, telemetry] No findings — content hygiene is enforced by omission.** The package makes zero log calls and has no logger to make them with. Wrapped error messages carry `Index` and `TotalChunks` only; `Data` (a user's private file bytes) and `Filename` (private in itself, and a log-injection shape in a line-oriented log) never enter an error string. Both rules come straight from `AttachmentChunkPayload`'s doc block, and this is its first consumer.
- **[Integrity] No findings — no partial or corrupted output is structurally impossible to emit.** `Assemble` has exactly one success return, guarded by the latch check and the exactly-once completion test; every other path returns `(nil, err)`. The `totalChunks < 1` clause closes the one vacuous success the completion test would otherwise permit (a declared count of zero assembling to empty bytes). Byte-level integrity against the declared `size` and `sha256` is **#1770's** and is not weakened by shipping this first: nothing consumes `Assemble`'s output until #1744, which lands after #1770.
- **[Replay / idempotence] No findings — the absence of a retry carve-out is deliberate and is tested.** A re-sent chunk is refused even when it repeats the bytes already held. The tempting softening ("same bytes, so accept it") would let a client probe which indices a receiver holds by observing which re-sends are accepted, and would break the published *exactly once* completion rule. The identical-bytes row in scenario 2 is the pin.
- **[Concurrency] No findings — no locks, and the precondition is written down.** No goroutines are spawned, so none can leak; no locks are taken, so there is no ordering to get wrong. The serial-feed precondition is documented on the type rather than assumed, and the shared registry that genuinely needs synchronisation is explicitly not in this slice.
- **[File operations] Not applicable — this slice touches no filesystem.** `attachment_id` is a stream's identity and never a path component here; canonical-shape validation before it reaches `filepath.Join` is #1743's, with `conversations.ValidID` as the named precedent.
- **[Tokens / credentials] Not applicable — no secrets.** `sha256` is latched and unread here; when #1770 reads it, it is **integrity, not authenticity** and explicitly not a fetch key, so no constant-time comparison is warranted (there is no secret to leak by timing — the client supplied both the bytes and the digest).
- **[Cryptographic primitives] Not applicable — this slice performs no cryptography.** The digest comparison, including its exact-lowercase-hex rule, is #1770's.
- **[Subprocess execution] Not applicable — no `exec`.**
- **[Network & I/O] Not applicable — this slice reads no socket.** Per-frame size limits are the transport's (`maxNoisePayloadBytes`) and the published per-chunk bound's (`MaxAttachmentChunkBytes`, enforced at #1767); this type is handed already-decoded payloads.
- **[Threat model alignment] No findings.** The relevant published threats are `docs/protocol-mobile.md` § Attachments' three discard triggers and its "never allocate from a claim" rule. All three triggers are implemented with distinguishable sentinels; the allocation rule is implemented in both places it applies and is the finding above. The two threats the same section raises that this slice does **not** address — integrity of the assembled bytes, and resource bounds — are named as out of scope above with their owning tickets (#1770, #1767).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25

## Open questions

- **Does `attachments.NewAccumulator` or `attachments.New` win?** This spec chooses `NewAccumulator`, leaving `New` free for whichever type turns out to be the package's front door once #1767's registry and #1743's store land. The repo has both patterns — `eventring.New` for a one-type package, `control.NewServer` / `noise.NewResponder` for multi-type packages — and this package is heading for the second shape. If #1767 finds a better front door, renaming a symbol with one caller is cheap; picking `New` now and having to move it is not.
- **Will #1742 need a read-only view of progress** (how many indices have arrived, when the last one did) to decide abandonment? Probably, and it is a field plus an accessor when it does. Deliberately not added here: nothing needs it yet, and an unused accessor is a contract this slice cannot test.
