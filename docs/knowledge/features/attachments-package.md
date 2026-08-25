# `internal/attachments` — inbound attachment-chunk accumulation

Package (#1769, #1770, #1772), three slices of the family split from
#1741/#1766: holds one inbound attachment upload's chunks in memory, addressed
by index, refuses a stream whose framing contradicts what the transfer
declared at admission (#1769), and — once the transfer is complete — checks
the assembled bytes against the transfer's declared length and lowercase-hex
sha256 before yielding a single byte (#1770). In-memory only — no disk, no
wire codes, no logger, no lock.

`SanitizeFilename` (#1772, `filename.go`) is unrelated in shape but shares the
package: a pure, stateless function turning a client-supplied
`AttachmentChunkPayload.Filename` into one safe host-side path component —
never called on `AttachmentID`, whose contract is a canonical-shape check that
*rejects* rather than this treatment. **The returned component is not
unique** — `a/b` and `a_b` both answer `a_b`, every unusable name answers the
fixed fallback, and APFS folds case — so #1773 must key stored files by
`attachment_id`, not by this component, and #1773's path-construction code
should be the only caller that reads `Filename` at all. Sanitising also does
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

## Sentinels and discard semantics

Six exported sentinels, `errors.New("attachments: …")` house style, in two
families plus one non-discarding outlier. Five discard the transfer (latched
on first refusal — every later `Add` and `Assemble` returns the identical
wrapped error, and the held chunk bytes are dropped at the moment of refusal
so a poisoned accumulator holds no memory past that point). `ErrIncomplete` is
the outlier and does **not** latch or discard — it is the resumable answer a
later chunk can turn into bytes.

| Sentinel | Family | Latches? |
|---|---|---|
| `ErrTotalChunksMismatch` | framing | yes |
| `ErrIndexOutOfRange` | framing | yes |
| `ErrDuplicateIndex` | framing | yes |
| `ErrSizeMismatch` (#1770) | integrity | yes |
| `ErrDigestMismatch` (#1770) | integrity | yes |
| `ErrIncomplete` | — | no |

Mapping these to the wire is #1744's job at the dispatch site — this package
emits no wire codes and does not import `codes.go`. The mapping is
deliberately many-to-one within each family: all three framing sentinels
answer `CodeAttachmentInvalidChunk`, both integrity sentinels answer
`CodeAttachmentIntegrityFailed`. Distinguishability is wanted in-process, for
this package's own tests and for the daemon's logs, not on the wire — a
single corrupt transfer must still resolve to exactly one wire code, which is
why `reject` (the shared latch primitive both families call) has to run for
an integrity failure too: without the latch, a chunk sent after an integrity
reject on an already-complete transfer would fall through to
`ErrDuplicateIndex`, and one transfer would emit two different wire codes at
#1744.

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
attacker-chosen, JSON-decoded string headed for #1744's line-oriented log, the
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

## Mutation-testing lessons (measured across #1769, #1770, and #1772)

This package's sole-redness claims are measured with `go test -overlay`
(mutants applied via an absolute-path JSON manifest, no worktree write), not
argued from the table alone. Four traps found that way, in the order they
surfaced:

- **A presence check needs a value indistinguishable from absent.** #1769's
  spec credited the *identical-bytes* duplicate row as the sole red test for
  writing the duplicate check as `chunks[i] != nil` instead of the two-value
  map lookup. Measured, that row stayed **green**: it re-sends ten non-empty
  bytes, so a non-nil value is still parked at the key and the buggy check
  still reports the duplicate correctly. The mutant only surfaces where the
  stored value is legitimately nil — a **zero-byte** re-send — which is why
  `TestAccumulator_DuplicateZeroByteChunk` exists as the actual sole red for
  it. Generalization: a row aimed at "presence must come from the key, not the
  value" has to use the value that is *indistinguishable from absent* for the
  type in play; any other value can leave a presence-from-value mutant alive
  while looking like coverage. #1770's `size`/`sha256` checks were built with
  this trap in mind — see the next point.
- **A value-based digest assertion can't pin what the digest is computed
  over when the value doesn't vary.** #1770's zero-byte round-trip row (empty
  bytes, empty-input digest) is credited with nothing about the digest's
  input, because the empty digest is the same whether it's computed over the
  real assembly or over nothing — only the non-empty rows (flip a byte in the
  *last* chunk, not the first, so a "digest over `chunks[0]`" mutant can't
  survive it) pin that.
- **An overlay mutant that deletes a check can also delete the only use of an
  import, and that fails the *build*, not a test — and `go test` exits 1
  either way.** A mutation harness that reads the exit code, or counts
  `--- FAIL:` lines, scores a build failure as *green* ("no test covers
  this"), which is the opposite of what happened. Two of #1770's eight mutant
  claims first measured green for exactly this reason. Fix: keep the deleted
  check's import alive in the mutant (e.g. `sum := sha256.Sum256(out); _ =
  hex.EncodeToString(sum[:])` with the comparison itself removed), and grep
  the overlay run's output for `"build failed"` before trusting any verdict —
  a passing `go test` on a broken build looks identical to a passing one on
  working code from the exit code alone. The same trap has a second shape,
  found in #1772: a mutant that removes the sole **read** of a local variable
  (not just an import) breaks the build the same way — replacing an
  input-derived `if !kept` with a result-derived check left `kept` assigned
  and unread. Go's unused-variable rule makes a local at least as likely a
  casualty as an import, so grep the overlay run's output for both
  `"build failed"` and `"declared and not used"` before trusting a verdict,
  and give a mutant that deletes a read somewhere to keep it alive (`_ =
  kept`) so the measurement is honest.
- **A subtest name built from raw attacker-shaped input can contain a NUL,
  and that turns `go test -v | grep` output into "binary" data** (#1772,
  `t.Run(tt.in, …)` fed a row whose input was `"a\x00b"`). `grep` without
  `-a` collapses the per-row red-line output to a single `Binary file
  (standard input) matches`, which reads as "one red row" rather than as a
  tooling failure — silently weakening every sole-redness measurement taken
  against that suite. The naming convention itself (subtest name = raw input)
  is worth keeping, since it's what makes the classic `report.txt\x00.exe`
  row legible in test output at all; the fix is `grep -a`, not a different
  naming scheme.
- **The mutant that only reddens a helper's own direct test, and nothing
  reached through the public surface, is the one that justifies the direct
  test's existence rather than merely adding to its coverage** (#1772: a
  plain `s[:maxBytes]` in place of `truncateToBytes`'s rune-safe loop
  reddened `TestTruncateToBytes` alone — nothing in `SanitizeFilename`'s own
  table went red). When an earlier pipeline step structurally constrains a
  later one (here: the allowlist step leaves the string pure ASCII before
  truncation ever runs), no fixture driven through the public function can
  falsify the later step's more careful implementation — only a test calling
  the unexported helper directly can. Treat that as a signal to add the
  direct test, not as a reason to skip it as redundant.
- **An unbounded caller-supplied parameter can still be a fine tradeoff
  purely because nothing can reach it** — flagged, not fixed, in #1772's code
  review: `truncateToBytes` with a negative `maxBytes` doesn't panic or
  return early, it spins forever (`utf8.DecodeLastRuneInString("")` returns
  `size == 0`, so the shrink-by-`size` loop stops shrinking while its `len(s)
  > maxBytes` guard stays true). Left as an informational NIT because the
  helper is unexported with one caller passing a positive constant — but the
  failure mode if that ever changes is a hang, not a crash, which is the
  detail worth carrying forward to whoever next gives this helper a
  caller-supplied bound.
- **A "returns a fresh copy" test must mutate its own copy of the fixture,
  not the shared package-level one.** `TestAccumulator_SingleChunk_ReturnsAFreshCopy`
  has to feed the accumulator a copy of `testFixture`, then mutate the
  *returned* slice to prove the accumulator's copy is independent. Feeding
  `testFixture` directly and mutating the return would scribble on the
  package-level fixture, reddening every other parallel test in the file
  under the fast-path mutant instead of just this row — a sole-redness
  measurement taken against that setup would report the wrong thing about a
  mutant that is genuinely caught.

All three properties #1769 shipped without a test pin are now pinned,
landed alongside #1770's own checks rather than left for a third mutation
pass to rediscover: `Assemble` on a declared `total_chunks == 0` answering
`ErrIncomplete` rather than a vacuous empty success
(`TestAccumulator_ZeroDeclaredCount_IsIncomplete`), a refused transfer's
`chunks` map going `nil` at the moment of refusal (the shared reject-row body
in `TestAccumulator_IntegrityFaults_RejectAndDiscard`), and `Assemble` never
fast-pathing a single-chunk transfer to `chunks[0]`
(`TestAccumulator_SingleChunk_ReturnsAFreshCopy`, the same test the
fixture-copy trap above is about).

## What a successful `Assemble` does not mean

Both #1770 checks pass when the assembled bytes match what the transfer
declared — that's integrity, not authenticity. The same party (the uploading
client) supplies both the bytes and the declared digest, so a match proves
the transfer wasn't corrupted in transit and proves nothing about whether the
content is safe. `protocol.AttachmentChunkPayload`'s doc block states this
directly; it matters here because the consumers of a successful `Assemble`
— #1772's filename sanitiser, #1773's storage, #1746's retrieval — are the
ones who'd otherwise read a green `Assemble` as a safety verdict rather than
a corruption check.

The declared `sha256` is also deliberately **not** promoted to a lookup key
anywhere in this package: there's no content-addressed retrieval ("know the
hash, fetch the blob"), which is a requirement from
`AttachmentChunkPayload`'s SECURITY block, not an oversight.

## Blocked family (not landed)

- **#1767** — admission: the first-chunk `total_chunks`/`size` cross-check,
  the byte bound, the in-flight-upload count, and the registry (`attachment_id`
  → `*Accumulator`) that needs its own lock — `Accumulator` itself carries
  none, because it is fed serially by one session's `appFrameWorker` goroutine.
- **#1742** — expiry/abandonment of a partial upload. Note this covers
  *partial* uploads only — a complete-but-corrupt transfer that fails one of
  #1770's integrity checks is not partial, so it's #1770's own `reject` latch
  (not #1742's reaper) that frees those held bytes.
- **#1773** — storage, and resolving `attachment_id` to a path. (#1743 split
  into #1772/#1773 while this family waited; #1772 — filename sanitisation —
  landed, see `SanitizeFilename` above.)
- **#1744** — wires the dispatch site: maps the three framing sentinels to
  `CodeAttachmentInvalidChunk` and the two integrity sentinels to
  `CodeAttachmentIntegrityFailed`, both via `errors.Is`, and is the first
  production caller of this package.
