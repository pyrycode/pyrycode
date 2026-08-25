# #1770 — Check assembled attachment bytes against the declared size and digest

Adds the two integrity comparisons to `internal/attachments`. One production
file, two new sentinels, no new type, no wire codes, no disk.

## Files to read first

Read in this order. Everything is symbol-named; resolve names with
`codegraph_search` / `codegraph_node` rather than by line.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/attachments/accumulator.go` | `Assemble` | The one place both checks land. Note the order of its existing guards and that it is **non-consuming** — it may be called repeatedly and must keep working. |
| `internal/attachments/accumulator.go` | `reject` | The latch-and-release primitive you reuse. It sets `rejected`, nils `chunks`, returns the error, so a caller reads as `return nil, a.reject(...)`. |
| `internal/attachments/accumulator.go` | `Accumulator` | The `size` and `sha256` fields, latched by `NewAccumulator` and until now unread. Their doc comments say in as many words that this slice is what reads them — update both. |
| `internal/attachments/accumulator.go` | `Add` | Its doc paragraph on why `Size`/`SHA256` are *not* checked per chunk. Still true, but the reason changes: the comparison uses the **latched** declaration, so a later chunk restating either differently changes nothing. Reword, don't delete. |
| `internal/attachments/accumulator_test.go` | `newTestAccumulator`, `testFixture`, `testParts`, `testTotal` | The fixture you extend. The uneven 10+10+4 tail is load-bearing — keep it. |
| `internal/attachments/accumulator_test.go` | `TestAccumulator_FramingFaults_RejectAndDiscard` | The table shape to mirror: one fault per row, and a shared body that asserts the sentinel, then the discard, then the post-refusal `Assemble`. The integrity table is the same shape. |
| `internal/attachments/accumulator_test.go` | `TestAccumulator_ZeroByteAttachment`, `TestAccumulator_DuplicateZeroByteChunk`, `TestAccumulator_Incomplete_IsResumable` | The three fixtures that declare `""` for the digest today. Two of them assert a successful `Assemble` and go red the moment the comparison exists. |
| `internal/protocol/attachments.go` | `AttachmentChunkPayload` | The `SHA256` and `Size` field contracts, the `SECURITY` block, and the **INTEGRITY, NOT AUTHENTICITY** paragraph that states the exact-equality rule and resolves its apparent contradiction. |
| `internal/protocol/codes.go` | `CodeAttachmentIntegrityFailed` | The wire code both new sentinels answer — **at #1744's dispatch site, not here.** Do not import this constant into `internal/attachments`. |
| `internal/protocol/attachments_test.go` | `attachmentSHA256HexLen` | Its doc comment offers a hex validator as a fifth exported constant and addresses it to "#1741", a ticket that has since closed into this family. One comment edit (below). |
| `internal/update/checksum.go` | `VerifySHA256` | **The nearest precedent, and the wrong one to copy.** See § Design. Read it so you recognise it, not so you reuse it. |
| `docs/knowledge/features/attachments-package.md` | § "A mutation-testing lesson worth carrying into #1770", § "Three shipped properties with no test pin" | The sole-redness trap this spec's testing strategy is written against, and the three unpinned #1769 properties two of which ride these criteria for free. |
| `docs/protocol-mobile.md` | § Attachments → "Reassembly & integrity (the receiver's rules)" | The published contract. Not re-decided here. |

## Context

`internal/attachments` (#1769, merged in #1771) accumulates one upload's chunks
by index and yields the assembled bytes once every index in
`[0, total_chunks)` has arrived exactly once. It checks neither of the
transfer's two claims. `NewAccumulator` already latches `size` and `sha256`;
this slice reads them.

The two comparisons run at the one place they can: after the transfer is
complete, before any bytes leave the package. Everything else about the
package is unchanged — in-memory only, no lock, no logger, no wire codes.

Scope fences, so the slice does not absorb its neighbours. Accumulation,
index addressing and the framing rejects are #1769's, landed. The receiver's
resource bounds and the first-chunk `total_chunks`/`size` cross-check are
**#1767's** — this slice may assume the stream it is fed was already
admitted. Expiry of a partial upload is **#1742's**. Filename sanitisation is
**#1772's**; storage and resolving `attachment_id` to a path are **#1773's**
(#1743 split into the two while this ticket waited). Wire codes, logging and
the dispatch site are **#1744's**. Here `attachment_id` is only a stream's
identity and never a path component; this slice never sees one.

No ADR is warranted. The decision this slice implements was published in
`docs/protocol-mobile.md` by #1751 and re-stated on `AttachmentChunkPayload`;
this is its first enforcement, not a new choice. The one thing worth recording
evergreen is the `update.VerifySHA256` near-miss below, which belongs in the
package overview the documentation phase owns.

## Design

### Two new sentinels

Declared in the existing `var` block in `accumulator.go`, after the three
framing sentinels and **before** `ErrIncomplete`, so the block reads
"refusals that discard, then the one answer that does not":

```go
// ErrSizeMismatch reports assembled bytes whose length differs from the
// transfer's declared size. Discards the transfer.
ErrSizeMismatch = errors.New("attachments: assembled length differs from the declared size")

// ErrDigestMismatch reports assembled bytes whose lowercase-hex sha256
// differs from the transfer's declared digest. Discards the transfer.
ErrDigestMismatch = errors.New("attachments: assembled sha256 differs from the declared digest")
```

House style is already fixed by the three siblings: `errors.New`, package
prefix, one sentence saying what it reports and whether it discards.

The block's own doc comment currently says "The first three are framing
refusals and all three map to the one wire code
`protocol.CodeAttachmentInvalidChunk`". Widen it: three framing refusals →
`CodeAttachmentInvalidChunk`, two integrity refusals →
`CodeAttachmentIntegrityFailed`, `ErrIncomplete` → nothing. The many-to-one
mapping is deliberate in both families and stays #1744's job. **This package
still does not import `codes.go`.**

### Where the checks run, and in what order

Inside `Assemble`, after the completeness guard, **after the bytes are
assembled**, before the successful return. Both checks read `out` — the exact
slice that would be yielded — never a proxy computed from the map:

```go
// after out is assembled, before it is returned
if int64(len(out)) != a.size {
    return nil, a.reject(fmt.Errorf(... : %w", ErrSizeMismatch))
}
sum := sha256.Sum256(out)
if actual := hex.EncodeToString(sum[:]); actual != a.sha256 {
    return nil, a.reject(fmt.Errorf(... : %w", ErrDigestMismatch))
}
return out, nil
```

Four decisions in that sketch, each load-bearing:

- **Length first.** Mandated by the ticket and by the shape of the fixtures:
  a digest mismatch is observable on its own (same length, different bytes),
  a length mismatch on an internally consistent declaration is not — a stream
  of the wrong length fails the digest too. Ordering is what makes
  `ErrSizeMismatch` reachable at all for the realistic corrupt stream, and the
  wrong-length row exists to catch a swap.
- **Both checks read `out`, not the map.** Hashing a re-walk of `a.chunks`, or
  comparing the summed chunk lengths instead of `len(out)`, would check a
  proxy for the yielded bytes rather than the yielded bytes. A future
  regression in the assembly loop then slips past a green check. `out` is the
  object the contract is about. This is also why `Assemble` must keep
  allocating a fresh slice rather than fast-pathing a single-chunk transfer to
  `chunks[0]`: with a fresh copy, **the bytes verified are the bytes returned**,
  and no later mutation of the accumulator's stored chunks can retroactively
  change what an already-returned slice contains. A fast path turns the pair of
  checks into a check-then-use over aliased memory.
- **Exact string equality, `!=`.** Not `strings.EqualFold`, not a prefix
  compare, not `subtle.ConstantTimeCompare`. See the two sub-sections below.
- **`a.reject(...)` on both.** That is what makes an integrity failure latch
  and release, per AC 5.

`Assemble` grows from ~16 to ~26 lines of body. Keep the checks inline; a
`checkClaims` helper buys nothing when the tests are in-package.

### Do not copy `update.VerifySHA256`

`internal/update`'s `VerifySHA256` is the closest existing "compare bytes
against a declared hex digest" in the repo, it is two lines from what you
need, and **its comparison is `strings.EqualFold`**. Copying it — or importing
it — is the failure this slice is most likely to ship. This is the same shape
of hazard `Accumulator`'s package doc already records for
`relay.ReassembleBundle`'s succession rule: the neighbouring rule is the
obvious thing to reach for and it is the wrong one here.

`EqualFold` is defensible where it lives (a digest parsed out of a checksums
file the daemon fetched, whose own parser already lowercases). It is not
defensible here: `docs/protocol-mobile.md` names a case-insensitive comparison
"a hole" in the same sentence that mandates lowercase hex. There is also no
sane dependency direction — `internal/attachments` must not import
`internal/update`.

Use `crypto/sha256` + `encoding/hex` directly. `hex.EncodeToString` emits
lowercase, which is what makes the comparison a lowercase-hex comparison
without a normalisation step. The idiom `sum := sha256.Sum256(b);
hex.EncodeToString(sum[:])` is already the house form in
`devices.HashToken`.

### No shape validator, and no carve-out

Uppercase, truncated, non-hex and **empty** claims all fail exact equality and
are all integrity rejects. That is the whole check. Specifically:

- **No hex validator.** `attachmentSHA256HexLen`'s note in
  `internal/protocol/attachments_test.go` offers one as an optional fifth
  exported constant, addressed to "#1741" — closed into this family. This
  slice is the one that would have written it and declines: every malformed
  claim already loses the equality comparison, which is where the published
  contract puts every mismatch, and picking it up would add a production file
  in a second package for a check that changes no outcome. Edit that comment
  to say so and to stop naming a closed ticket (below).
- **No empty-claim carve-out.** The claim is attacker-supplied. "Skip the
  comparison when the claim is empty / malformed / not 64 characters" hands
  the sender an opt-out from integrity checking, which is strictly worse than
  no check at all because the reject path looks live. AC 3 names the empty
  claim explicitly for exactly this reason.

The apparent contradiction in the published contract — "a case-insensitive or
prefix comparison is a hole" beside "a comparison that rejects an
uppercase-sending client is an availability bug" — is resolved by the contract
itself in the trailing clause: *"so the canonical form is lowercase and
clients send it that way."* The receiver compares exactly; the availability
risk is answered by the published client obligation, not by softening the
comparison. Do not re-litigate this in code or in a comment.

### What the error strings may carry

The framing rejects carry the offending **numbers only**, because
`AttachmentChunkPayload` marks `Data` content-bearing and `Filename` both
private and a log-injection shape in a line-oriented log. The declared
`sha256` is the same class of hazard — an attacker-chosen string, unbounded by
anything in this package, that would land in an error string and thence in
#1744's logs. So:

- `ErrSizeMismatch`'s message carries the **declared size and the assembled
  length**. Both integers. Direct analogue of the framing messages.
- `ErrDigestMismatch`'s message carries the **computed** digest and the
  **length in characters** of the declared claim. Never the claim's bytes.
  The computed digest is daemon-authored, fixed shape (64 lowercase hex, no
  injection surface), one-way with respect to content, and not a capability —
  the contract states retrieval names a conversation and an attachment, never
  a hash. The claim's length is what makes a truncated or empty claim
  diagnosable without quoting it.

### What does *not* change

- **`Assemble` stays non-consuming.** On success it must not nil the map:
  `TestAccumulator_Incomplete_IsResumable` assembles twice and expects bytes
  both times. Releasing on success is #1742's and #1744's.
- **Nothing is ever sized from a claim.** Neither check allocates. `out` is
  still sized from the sum of arrived chunk lengths. `a.size` is read only as
  the right operand of a comparison; `a.sha256` only as a string operand.
  A negative or absurd declared size needs no special case — no non-negative
  `len(out)` equals it, so the length check refuses it, and #1767 refuses the
  declaration earlier at admission.
- **`Add` is untouched.** It still reads three fields and still ignores each
  chunk's own `Size`/`SHA256`; the comparison uses the latched declaration, so
  a later chunk restating either differently changes nothing. Amend that
  paragraph's wording; do not weaken the rule.
- **No new sentinel for "no bytes".** On either reject `Assemble` returns
  `nil` bytes, as it already does for `ErrIncomplete` and for a latched
  framing refusal.
- **The package still makes zero log calls and gains no logger.** An integrity
  reject is the obvious place to reach for `slog`, and it is the wrong place:
  this type does not know the attachment id or the conn id, which are the only
  fields worth logging, and #1744's dispatch site knows all three. Adding a
  `*slog.Logger` here would also put a user's private file metadata one
  refactor away from a line-oriented log.

### Doc-comment surface to update

Comment work is a real share of this ticket. Named so it does not get
discovered late:

- Package doc — "refuses a stream whose framing contradicts what the transfer
  declared" is now half the story.
- The sentinel `var` block's leading comment — two families, two wire codes.
- The `size` and `sha256` field comments — drop "unread by this slice, read by
  the sibling (#1770)".
- The `rejected` field comment — "latches the first framing refusal" is now
  "the first refusal, framing or integrity".
- `reject`'s doc — it is now called from `Assemble` too, and its
  release-of-held-bytes paragraph gains a second reason: a complete-but-corrupt
  transfer is not "partial", so #1742's reaper does not cover it.
- `Assemble`'s doc — replace the "returned UNCHECKED against the transfer's
  declared size and sha256; that comparison is #1770's" paragraph with the
  checks, their order and the reason for the order. It must also say what a
  successful return does **not** mean: the same party supplied the bytes and
  the digest, so a match proves the transfer was not corrupted and proves
  nothing about whether the content is safe. Its consumers — #1772's filename
  sanitiser, #1773's storage, #1746's retrieval — are the readers who would
  otherwise take a green `Assemble` for a safety verdict.
- `Add`'s doc — the `Size`/`SHA256` paragraph, per above.
- `internal/protocol/attachments_test.go`, `attachmentSHA256HexLen`'s comment —
  one edit: the validator was declined by #1770, and the constant stays a
  test-local mirror. Comment only; the constant and its use are unchanged.

Citations in every one of these follow `CODING-STYLE.md` § "Comments — Citing
Other Code": name the symbol, never a line and never a range. `make
cite-guard` is a build gate.

## Concurrency model

Unchanged, and deliberately: no goroutine, no lock, no context. `Accumulator`
remains not-safe-for-concurrent-use and fed serially by one session's
`appFrameWorker`. Neither new check introduces shared state — both read
already-latched fields and a local slice. Synchronising the registry of
in-flight uploads stays #1767's.

One property worth naming because it is a resource bound rather than a
correctness one: the latch also caps the work a hostile client can extract.
After the first integrity failure every later `Assemble` returns at the
`rejected` guard, so a client cannot make the daemon re-hash by re-driving a
corrupt transfer. Hashing costs one linear pass over bytes the caller already
holds, so a successful `Assemble` stays the same cost class it already was
(one allocation + one copy, now + one hash). `Assemble` is still not memoised
— a caller that needs the bytes twice should keep them.

## Error handling

| Condition | Answer | Latches? | Releases held bytes? | #1744 wire code |
|---|---|---|---|---|
| already refused | the latched error, verbatim | — | already released | that refusal's code |
| declared count < 1, or an index missing | `ErrIncomplete` | no | no | none — not client-visible |
| assembled length ≠ declared `size` | `ErrSizeMismatch` | yes | yes | `attachment.integrity_failed` |
| assembled digest ≠ declared `sha256` | `ErrDigestMismatch` | yes | yes | `attachment.integrity_failed` |
| framing fault in `Add` | one of the three framing sentinels | yes | yes | `attachment.invalid_chunk` |

Both new rows are returned wrapped (`fmt.Errorf(... %w)`), matched by callers
with `errors.Is`, never by string. AC 5's teeth: after an integrity reject the
only chunk a client can still send on a *complete* transfer is a duplicate, so
without the latch the next `Add` answers `ErrDuplicateIndex` — which #1744
maps to `attachment.invalid_chunk`, making one corrupt transfer emit two
different wire codes. That is the failure the latch prevents, and the test
below is written to see it.

## Testing strategy

In-package, table-driven, `t.Parallel()`, stdlib only — the file's existing
posture. Scenarios below are descriptions, not code.

### Fixture work

- Two written-out digest constants, not computed by a helper: the digest of
  `testFixture` and the digest of the empty input
  (`e3b0c442…855`). Written out, each traces to a value verifiable outside
  this package (`printf '…' | shasum -a 256`, noted in the comment), so a
  mutant that changes the algorithm or the encoding cannot move the
  expectation with it.
- `newTestAccumulator` declares the real fixture digest.
- The two `NewAccumulator(1, 0, "")` fixtures declare the empty-input digest —
  **both**, including the duplicate-chunk one that never assembles
  successfully. A `""` claim left in any fixture is precisely the value AC 3
  says must be rejected, and a later test copying it would be silently
  testing a dead transfer.
- Reuse `testFixture` as the single-chunk fixture (one chunk carrying all 24
  bytes) so the non-empty rows need no third digest literal.

### Rows, and the mutant each is the sole red for

One integrity table drives every reject row through a shared body that asserts,
in order: the expected sentinel via `errors.Is`; that no bytes came back; that
`a.chunks` is `nil`; that a later `Add` of an already-arrived index returns the
**identical** error value (`==`, as the framing table does); and that a later
`Assemble` returns that identical value and no bytes.

| Row | Fixture | Expect | Sole red for |
|---|---|---|---|
| wrong length | declared size + digest of `testFixture`; stream assembles to 23 bytes (short last chunk) | `ErrSizeMismatch` | deleting the length check (answer becomes `ErrDigestMismatch`); **swapping the two checks** (same) |
| absurd declared size | declared size `math.MaxInt64`, correct stream and digest | `ErrSizeMismatch` | sizing anything from `a.size` — the package's cardinal hazard, at the one check that reads the claim |
| wrong bytes, right length | one byte flipped in the **last** chunk; declared size and digest of `testFixture` | `ErrDigestMismatch` | replacing equality with a **shape check** on the claim (a well-formed claim would pass); deleting the digest check |
| correct digest, uppercase | `strings.ToUpper` of the fixture digest, correct stream | `ErrDigestMismatch` | `strings.EqualFold` — the `update.VerifySHA256` copy |
| truncated claim | first 32 characters of the fixture digest | `ErrDigestMismatch` | a carve-out gated on `len(claim) != 64` |
| non-hex claim | 64 lowercase non-hex characters | `ErrDigestMismatch` | a carve-out gated on the claim not being hex |
| empty claim | `""` | `ErrDigestMismatch` | a carve-out gated on `claim == ""` |

Success rows:

| Row | Assertion | Sole red for |
|---|---|---|
| any arrival order (existing test, real digest) | exact original bytes | computing the digest over `chunks[0]`, or over anything short of the whole assembly |
| single chunk, non-empty | assemble, mutate the returned slice, assemble again, expect the original bytes | `Assemble` fast-pathing to the stored slice (#1769's unpinned property) |
| zero-byte, declared empty digest | no error, zero bytes | a guard that rejects an empty assembly. **Cannot** pin what the digest is computed over — the empty digest is the same either way; the non-empty rows are what pin that |
| `NewAccumulator(0, 0, emptyDigest)` | `ErrIncomplete`, no bytes | deleting the `totalChunks < 1` guard. Declared size 0 **and** the empty digest, so nothing downstream can redden it for another reason — the pin fails precisely because the guard is gone |

Three of these rows are #1769's unpinned properties, landed here rather than
left for another mutation pass: the single-chunk row, the `a.chunks == nil`
assertion in the shared reject body, and the zero-declared-count row. The last
is explicitly *not* an AC of this slice; it is cheap and it is in the same
declaration.

### The sole-redness trap this is written against

`docs/knowledge/features/attachments-package.md` records a measured pass on
#1769 where a row credited as the sole red stayed green, because it used a
stored value that was not the value indistinguishable from absent. Two
consequences here, both already folded into the tables above:

- The zero-byte row is credited with **nothing** about the digest's input.
- The digest-mismatch row flips a byte in the **last** chunk, not the first,
  so a "digest over `chunks[0]`" mutant cannot survive it — and the
  multi-chunk success row kills that mutant independently.

Any sole-redness claim added beyond this table should be **measured**, not
argued: `go test -overlay=<abs-path json>` runs a mutant with no worktree
write.

### Gate

`make check` covers this package. `go test -race ./internal/attachments/ ./internal/protocol/`
is the fast inner loop.

## Open questions

None blocking. Two things deliberately closed rather than left open, recorded
so they are not reopened during implementation:

- The hex validator offered to "#1741" by `attachmentSHA256HexLen`'s note is
  **declined**, and that note is edited to say so.
- The two consequences `MaxAttachmentChunkBytes`' doc comment hands to
  "#1741" — the cross-check's equality form and its behaviour at `size == 0`
  — are **#1767's**, not this slice's. Leave that comment alone.

## Security review

**Verdict:** PASS

Run per the `security-sensitive` label, against
`$AGENTS_REPO_PATH/architect/security-review.md`. Four findings, none MUST
FIX; each is closed inside the design above and carries at least one test row.

**Findings:**

- **[Trust boundaries]** SHOULD FIX — *addressed in § Design.* Every value this
  slice reads is a claim: `size` and `sha256` were latched by `NewAccumulator`
  from a client's declaration, and `out` is assembled from bytes a client sent.
  Nothing here is daemon-authored except the computed digest. The boundary is
  explicit and singular — one method, `Assemble`, and both comparisons live
  inside it. The finding is on the **downstream** side: a caller reading a
  successful `Assemble` as "these are the promised file's bytes, so they are
  safe" has misread it, because the same party supplied the bytes and the
  digest. The design makes that survive into `Assemble`'s doc comment,
  addressed by name to #1772, #1773 and #1746.

- **[Tokens, secrets, credentials]** No findings — no token, key or credential
  is created, stored, compared or logged. The declared digest is deliberately
  **not** promoted to one: this slice adds no lookup keyed by a digest, so
  content-addressed retrieval ("know the hash, fetch the blob") stays
  impossible, as `AttachmentChunkPayload`'s SECURITY block requires.

- **[File operations]** No findings — the design is in-memory only. It opens
  no file, writes nothing to disk, and never sees an `attachment_id`, so
  there is no path to canonicalise, no TOCTOU window, no mode to set and no
  symlink to refuse. Path handling arrives with #1773, filename sanitisation
  with #1772.

- **[Subprocess / external command execution]** No findings — the design
  executes nothing and touches no environment variable.

- **[Cryptographic primitives]** No findings, and two decisions worth stating
  rather than leaving to inference. **(a)** `crypto/sha256` +
  `encoding/hex`, the stdlib primitives already used by `devices.HashToken`;
  nothing is hand-rolled and no RNG is involved, so there is no key or nonce to
  reuse. **(b)** `crypto/subtle.ConstantTimeCompare` is deliberately **not**
  used, and its absence is correct rather than an oversight: neither operand is
  a secret. The attacker supplies the bytes *and* the claim and therefore
  already knows both sides of the comparison, so a timing signal reveals
  nothing it does not hold; using it would also imply the digest authenticates
  something, which the contract explicitly denies. OUT OF SCOPE, named forward:
  if a later slice ever compares a caller-supplied hash against a digest the
  caller could not have computed — a digest of a file it cannot read — that
  comparison **does** need constant time, because it becomes a content oracle.
  Retrieval as published never names a hash (#1746), so no such comparison
  exists today.

- **[Network & I/O]** OUT OF SCOPE to **#1767** — every resource bound is its
  slice: how many uploads may be in flight, how many bytes one may accumulate,
  and the first-chunk `total_chunks`/`size` cross-check that refuses before
  allocating. This slice may assume the stream was already admitted, and it
  reads no socket. What it does own is the rule those bounds rest on: **never
  allocate from a claim.** Neither new check allocates. `a.size` appears only
  as the right operand of an integer comparison and `a.sha256` only as a string
  operand of `!=`; `out` is still sized from the sum of arrived chunk lengths,
  and the chunk map is still built without a capacity hint. The
  `math.MaxInt64` declared-size row pins this at the one check that reads the
  claim. Work amplification is bounded too: hashing is one linear pass over
  bytes the caller already holds, and the latch short-circuits every later
  `Assemble` on a refused transfer, so a client cannot drive repeated hashing
  by re-sending.

- **[Error messages, logs, telemetry]** MUST FIX as first drafted —
  *addressed in § Design, "What the error strings may carry".* The declared
  `sha256` is an attacker-chosen string, unbounded by this package, and the
  obvious mismatch message quotes it — which puts attacker text into an error
  value headed for #1744's line-oriented log. That is the same hazard `Data`
  and `Filename` are already banned from error strings for. Closed: the
  message carries the **computed** digest and the **character count** of the
  claim, never the claim's bytes. Including the computed digest is a
  judgement call, stated so a reviewer sees the trade — it is daemon-authored,
  fixed shape, one-way with respect to content, and not a capability. A second
  half of the same finding: this package makes **zero log calls** and holds no
  logger, and an integrity reject is the obvious place to add one. It must
  not; this type knows neither the attachment id nor the conn id, which are
  the only fields worth logging, and #1744 knows all three.

- **[Concurrency]** No findings — no goroutine, no lock, no context, no new
  shared state. `Accumulator` stays not-safe-for-concurrent-use and fed
  serially by one session's `appFrameWorker`, and both new checks read
  already-latched fields plus one local slice. The one TOCTOU shape available
  here is closed by an existing property this slice must preserve: because
  `Assemble` allocates a fresh slice rather than fast-pathing a single-chunk
  transfer to `chunks[0]`, **the bytes verified are the bytes returned**, and
  no later mutation of the accumulator can retroactively change what an
  already-returned slice contains. A fast path would make the pair of checks a
  check-then-use over aliased memory. The single-chunk row is that property's
  first test pin.

- **[Threat model alignment]** No findings — `docs/protocol-mobile.md`
  § Attachments → "Reassembly & integrity (the receiver's rules)" is the
  governing contract and this slice implements it rather than re-deciding it:
  compare only once complete; length against `size` and `sha256(assembled)`
  against `sha256`; lowercase hex for **exact equality**; either mismatch is
  `attachment.integrity_failed`; never emit partial or corrupted output;
  integrity, not authenticity; not a fetch key; never allocate from a claim.
  The one design hole a plausible implementation would open is an **integrity
  opt-out the sender controls** — a carve-out that skips the comparison for an
  empty, malformed or wrong-length claim. It is worse than no check, because
  the reject path still looks live while the sender chooses whether it runs.
  Closed three ways: § Design forbids it in prose, AC 3 names the empty claim,
  and four table rows are each the sole red for one carve-out shape. The
  second is the memory a hostile client can park: a complete-but-corrupt
  transfer is not "partial", so #1742's reaper does not cover it — both
  integrity rejects therefore go through `reject`, which nils the map at the
  moment of refusal, asserted per row. The third is one transfer emitting two
  wire codes: without the latch the next `Add` on a rejected complete transfer
  answers a framing sentinel, which #1744 maps to `attachment.invalid_chunk`.
  Closed by the latch and the identical-error-value assertion. Remaining
  threats are named with owners in § Context's scope fences (#1767, #1742,
  #1744, #1772, #1773).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-25
