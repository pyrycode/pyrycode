# `internal/attachments` — inbound attachment-chunk accumulation

Package (#1769, #1770, #1772, #1776, #1777, #1787, #1781, #1788, #1782), nine
slices of the family split from #1741/#1766: holds one inbound attachment
upload's chunks in memory, addressed by index, refuses a stream whose framing
contradicts what the transfer declared at admission (#1769), and — once the
transfer is complete — checks the assembled bytes against the transfer's
declared length and lowercase-hex sha256 before yielding a single byte
(#1770). Bounds the retained bytes of one upload to a receiver-configured,
unpublished ceiling, refusing at either the declared size or the accumulated
total (#1777, see § "Per-upload byte bound" below), and holds the uploads
currently in flight in a conn-keyed `Registry` (#1787), whose only exported
way in runs both declaration checks before admitting anything (`Admit`,
#1788, see § "In-flight upload registry" below).
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

## Per-upload byte bound (#1777)

Two rungs enforce one constant, `maxUploadBytes` (16 MiB, `admission.go`), and
share one sentinel, `ErrUploadTooLarge`: `CheckDeclaredSize` refuses a
declared `size` above the bound on the first chunk, before any bytes are
held; `Add`'s step 5 — last in its fixed check order, after the duplicate
check, immediately before the store — refuses the chunk that would take the
held total past it, routed through the existing `reject` latch. The crossing
chunk's own bytes are the one transient "slack," bounded by the transport's
65519-byte envelope cap and never retained.

- **The declared rung is a sibling of `CheckDeclaration`, not a fold-in** —
  measured, not assumed, same as the admission-layer decisions above.
  `TestCheckDeclaration`'s `math.MaxInt64` row is the only row that pins the
  ceiling arithmetic's exact value; folding a magnitude check into
  `CheckDeclaration` would refuse that pair and destroy the sole pin on the
  wrapping form that silently admits the wire's largest declaration. Two
  sentinels with two different client-visible repairs — re-chunk vs. shrink
  the file — is also a worse contract from one function than from two.
- **`CheckDeclaredSize` admits a negative `size` on purpose.** Magnitude is
  its whole subject; `CheckDeclaration` already owns the negative-`size`
  refusal, and a second owner would make which sentinel a caller sees depend
  on call order. Both checks must run, on the first chunk, before
  `NewAccumulator` — `Registry.Admit`'s obligation (#1788), not either
  function's, since neither can enforce the other's presence: `NewAccumulator`
  stays exported and constructible without passing through `Admit`, so a
  second caller could still run one check or neither.
- **The pairing is what bounds the key space, not either check alone.**
  `CheckDeclaration` bounds `total_chunks` from above, `CheckDeclaredSize`
  bounds `size` from above; together an admitted transfer declares at most
  373 chunks. `CheckDeclaredSize` alone admits a declaration of 2³¹−1 declared
  zero-byte chunks, which no byte bound can refuse, since Σ `len(Data)` stays
  0 regardless of how many chunks are declared.
- **The accumulated rung is subtraction, not a sum**: `len(chunk.Data) >
  maxUploadBytes - a.received`, never `received + len > bound`. The
  invariant `0 <= received <= maxUploadBytes` is what keeps the subtraction's
  right operand non-negative; the addend is a materialised slice length
  rather than a claimed number, so the sum form's wrap isn't reachable in
  practice either, but the safe form is written anyway so a later reader
  doesn't "simplify" it back or read the discipline as cargo cult.
- **Code review correction: the daemon-wide worst-case *peak* is `2N ×
  bound`, not `(N+1) × bound`.** The landed doc's "Retained is not peak"
  paragraph states the worst case as one bound of retained memory across `N`
  concurrent uploads plus a single extra bound for one in-flight `Assemble`
  call — true only if `Assemble` calls across sessions were serialized. They
  are not: `appFrameWorker` (`internal/relay/v2session.go`) documents "no two
  handlers for **the same conn** run concurrently," which is a per-session
  guarantee, not a daemon-wide one — one worker is spawned per session, so
  `N` concurrent sessions can each be inside their own `Assemble` call at the
  same moment. The correct worst-case peak is `N` bounds retained plus up to
  `N` more in flight — 128 MiB at a concurrency of 4, not 80 MiB. The
  *resident* (non-peak) figure the constant's doc states — `bound ×
  concurrency` — is correct and is what #1786 should inherit as its budget;
  it is only the peak-during-assembly refinement layered on top that
  undercounts. **#1786: budget peak as `2N × bound`, not the `(N+1) × bound`
  the code comment currently states** — this was a code-review SHOULD FIX
  landed as text-only (no enforcement is wrong, only the doc's peak
  refinement), so the comment itself was not corrected.

Blocked, still: the in-flight-upload count cap (#1786) — this package bounds
one upload's bytes, not how many uploads may exist at once. The registry that
holds one accumulator per in-flight upload has since landed; see § "In-flight
upload registry" below.

## In-flight upload registry (#1787, #1788)

`Registry` (`registry.go`) gives `Accumulator` somewhere to live between
chunks: a map from `uploadKey{connID, attachmentID}` to the `*Accumulator` in
flight for that pair, an unexported never-replacing `insert`, comma-ok
`Lookup`, `Release`, and an unexported `count`. Still unreachable from
production — nothing calls it until #1744 wires the dispatch site, and #1744
lands last in the family.

`Admit` (#1788) is the registry's only exported way in: it runs
`CheckDeclaration` then `CheckDeclaredSize` on a first chunk and, only on a
nil answer from both, constructs the `Accumulator` and hands it to `insert`.
A refusal returns the check's error verbatim — no wrapping, no added context
— which is what keeps `attachmentID` and `sha256` (the two banned strings
this entry point necessarily holds) out of an error string with no format
string for either to enter, and keeps both sentinels reachable with
`errors.Is`. A repeat under a held pair answers the incumbent with a nil
error and discards the accumulator it had just built. Which check runs first
is deliberately not a contract — a doubly-bad declaration gets whichever
sentinel runs first, and no test may read an order out of that.

- **Pairing two checks that answer different sentinels into one entry point
  needs one fixture per check, never a doubly-bad one.** A declaration both
  `CheckDeclaration` and `CheckDeclaredSize` would refuse reddens under
  dropping either guard and so proves neither is load-bearing, and it would
  additionally pin a cross-check order nothing has decided (the two
  sentinels differ, so an order asserted here would be an accident promoted
  to a contract). The fix used here: one row that's admissible by
  arithmetic but oversized, one that's within the byte bound but
  arithmetically wrong — each is a sole red for exactly one guard.
- **`insert`'s never-replace behaviour was pinned once at the unexported
  layer (#1787) and had to be re-pinned through the exported one.** The
  #1787 test drives `insert` directly, so it stays green against an `Admit`
  that released the pair before inserting, or that installed a freshly
  built accumulator by some other route — the property a caller actually
  depends on only holds if the exported path is tested too, not inherited
  from coverage one layer down.
- **Sequencing note for the rest of the family, from this ticket's security
  review:** #1744 (the dispatch site, and this package's first production
  caller) must not land ahead of #1786 (the in-flight entry-count cap).
  Nothing calls this package today, so `Admit` changes no live exposure yet
  — but the moment #1744 wires it up, `Admit` bounds one upload's bytes and
  places no bound on how many uploads may exist, so N distinct
  `attachment_id` values on one conn yield N entries. `Admit` is what closes
  the key-space hole `CheckDeclaredSize`'s doc names (paired, an admitted
  transfer declares at most 373 chunks); it does nothing about the entry
  count, which is #1786's bound alone.

- **Keyed by conn-and-attachment_id, not the bare id this doc used to sketch.**
  `docs/protocol-mobile.md` § Attachments documents `attachment_id` as "not a
  capability" — not secret, not unguessable. Keyed alone, a phone that drops
  mid-upload and reconnects re-sends the same id while its old session may not
  have torn down, colliding two conns onto one `*Accumulator`, which carries no
  mutex by design: a data race and a path for one conn's bytes into another's
  file. A concatenated string key (`connID+attachmentID`) reintroduces the same
  collision (`"ab"+"c"` and `"a"+"bc"` are one key); only a struct key forecloses
  it structurally.
- **One `sync.Mutex`, taken once per operation and held for the whole body —
  never once to read, again to write.** That is what lets the same-pair
  concurrent-insert case tell exactly one caller it got a fresh insert, and it's
  the shape #1786's count-then-admit gate needs: a registry that locks its read
  and its write separately passes `-race` and every other criterion here, but
  can't support the indivisible look-up-then-count-then-insert #1786 requires.
  `insert`'s never-replace behaviour is a security property, not hygiene — it
  stops a second first-chunk for a live pair from swapping the declared
  `size`/`sha256` out from under the bytes already accumulated.
- **Off-lock feeding is sound only because the conn is in the key.** The
  registry hands the `*Accumulator` back and the caller feeds it with the lock
  released; that's safe because `V2SessionManager.appFrameWorker` serialises
  every frame for one conn, so exactly one goroutine can ever reach one
  accumulator. #1784 (routing a later chunk into an admitted upload) inherits
  this posture rather than re-deriving it.
- `insert` stays unexported; `Admit` (#1788, above) is now the only way in,
  running `CheckDeclaration` + `CheckDeclaredSize` before construction. After
  #1787 landed with no policy on what may enter, the property this closes is
  a fact about the package's exported surface rather than something an
  in-package test can assert — a test can still call `insert` directly — so
  no acceptance criterion claims it. `Registry`'s type doc's "no method of
  `Registry` calls another" also needed correcting once `Admit` composes with
  `insert`: the rule was re-scoped to "no method takes `mu` and then calls
  another that takes `mu`" — `Admit` takes no lock of its own, so it doesn't
  engage the rule, and the four locked methods still never call one another.

## Directory resolution and creation (#1781)

`EnsureDir` (`storage.go`) resolves and creates the on-host directory one
attachment of one conversation is filed under —
`conversations/<conversation-id>/attachments/<attachment-id>` beneath the
daemon instance directory, every level `0o700` — and returns its
`EvalSymlinks`-resolved path. Writing bytes into it is #1782's; dispatching to
it and mapping refusals to wire codes is #1744's. It carries two sentinels of
its own, `ErrInvalidID` and `ErrNotContained`, structurally unlike the seven
below: `EnsureDir` has no accumulator state to latch or discard, so the
discard-semantics table doesn't apply to it.

Both ids are validated against `conversations.ValidID` before any filesystem
call — `attachment_id` is client-chosen and its 64-byte wire ceiling is
explicitly not a defence, so containment comes entirely from the resolution
check, never from the id being hard to guess. The check itself is
`candidate == want` (full-path equality against a destination built textually
beneath the `EvalSymlinks`-resolved instance directory), not a
`filepath.Rel`-style "is it under the root" test — equality is what refuses a
conversation directory symlinked to a *sibling* conversation, which a
containment-only test would pass.

- **A precedent's ordering rationale doesn't transfer unchanged when a caller
  strengthens the comparison it protects — measured, not assumed.**
  `confineWorkdirToHomeCreating` (`cmd/pyry/main.go`) is this design's
  precedent, and its ancestor walk probes with `os.Lstat` rather than
  `os.Stat` so a symlink is resolved instead of stepped over. `EnsureDir`
  copied that probe, but re-measuring it under the *stronger* comparison this
  design uses (equality, not the precedent's `filepath.Rel` containment test)
  found the swap reddens **nothing** in the suite: the two probes diverge only
  for a *dangling* link, and on one, `Stat` steps over it so the pre-creation
  check passes vacuously, but `MkdirAll` then fails `EEXIST` on the symlink
  name — landing in the same wrapped-OS-error class the `Lstat` build reaches
  by failing to resolve it. `Lstat` was kept anyway (refusal should come from
  the resolution step rather than incidentally from directory creation, and
  the precedent's weaker containment test genuinely does depend on the probe),
  with the code comment rewritten to state the measured fact rather than the
  inherited one. Anyone copying a piece of `confineWorkdirToHomeCreating` into
  a design that changes the comparison it was written for should re-measure
  that piece, not inherit its justification.
- **A spec's own fixture rule can make its own predicted sole-red row
  unsatisfiable.** The `os.Stat` mutant above was predicted (in the
  architect's mutant table) to redden the escaping- and sibling-conversation
  symlink tests. Both of those fixtures are required to use targets that
  *exist* — `EvalSymlinks` on a dangling link errors, so a dangling-target
  fixture would assert the wrong sentinel for a right-looking reason — which
  is exactly the fixture the `Stat`/`Lstat` divergence needs to be visible at
  all. No test satisfying those two criteria can ever be the mutant's sole
  red. When a mutant table names a row its own fixture constraints forbid,
  treat the row as unsatisfiable rather than an unwritten test to chase; a
  dangling-symlink case still deserves its own test (`EnsureDir` returns a
  wrapped OS error and neither sentinel for one — broken host state is not a
  containment breach), just not as that mutant's proof.

## Writing attachment bytes (#1782)

`Store` (`storage.go`) takes the directory `EnsureDir` returned, a
client-supplied filename, and already-verified bytes, and writes them there
with the house temp-and-rename recipe (temp file created *in* `dir`, `Chmod`
`0o600`, write, `Sync`, `Close`, `Rename`), returning the joined path. It
trusts `dir` completely and adds no resolution or containment check of its
own — re-deriving one would fork the check `EnsureDir` exists to own, and
would break the reason two attachments carrying the same client filename can
coexist: the per-`attachment_id` directory component disambiguates them, not
the sanitised name, which `SanitizeFilename` (#1772) documents as explicitly
not unique. `ErrWriteFailed` is a third top-level sentinel in `storage.go`,
beside `ErrInvalidID`/`ErrNotContained` — not folded into `accumulator.go`'s
grouped block, whose opening sentence scopes it to `Add`/`Assemble`. `Store`
is idempotent (`Rename`, not `O_EXCL`), which is what lets a phone's
reconnect-and-resend overwrite with the same bytes instead of failing. No
production caller yet; #1744 wires the dispatch site.

- **A privacy rule stated as one property across several failure branches
  needs checking branch by branch, not as a whole.** The spec banned a
  rename-failure error from naming the sanitised client filename — correct,
  since `os.Rename` returns `*os.LinkError`, whose `Error()` prints its
  destination unconditionally — and then extended the same reasoning to rule
  out a test on the create-temp branch too, calling it "vacuous on the
  create-temp path where the name is never touched." Code review measured
  it: an overlay mutant touching *only* the create-temp branch's `%q`
  operand (`dir` → `path`) leaked the sanitised filename into the error text
  and the full suite — six tests, ten subtests — stayed green. The
  create-temp branch never names `path` in the correct build, but nothing
  had asserted it couldn't in a wrong one. The reasoning that made one branch
  unpinnable didn't actually apply to the other five; it was accepted for
  all six because it was true for one. When a spec calls an assertion
  vacuous across several code paths, verify the claim against each path the
  assertion would cover, not just the one that prompted the reasoning.
- **An already-extracted, same-shaped atomic-write helper can still be the
  wrong copy target.** `internal/update.AtomicReplace` is an exported,
  doc-commented version of this exact recipe whose signature would have
  dropped in almost verbatim. It was correctly not used: its rename-failure
  message formats the destination path into the text unconditionally (making
  the branch-privacy gap above permanent instead of one CR finding from
  closed), and it collapses five distinguishable failure points into one
  message, losing the per-step `<op>` this package's error strings need for
  the operator's log. The Technical Notes' "ten packages hand-roll this
  recipe, none imports another's — don't extract a shared helper" is
  guidance against building a *new* one; it isn't a green light to reach for
  an existing one without reading what that helper puts in its own errors.

## Sentinels and discard semantics

Seven exported sentinels, `errors.New("attachments: …")` house style, in
three families plus one non-discarding outlier. Six discard the transfer
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
| `ErrIncomplete` | — | no |

`ErrUploadTooLarge` is the one sentinel not scoped to `accumulator.go`'s var
block, whose opening sentence reads "Sentinel errors returned by `Add` and
`Assemble`" — it is also returned by an admission function that runs before
an `Accumulator` exists, so it lives in `admission.go` beside
`ErrInvalidDeclaration` instead.

Mapping these to the wire is #1744's job at the dispatch site — this package
emits no wire codes and does not import `codes.go`. The mapping is
deliberately many-to-one within the framing and integrity families: all
three framing sentinels answer `CodeAttachmentInvalidChunk`, both integrity
sentinels answer `CodeAttachmentIntegrityFailed`. `ErrUploadTooLarge` is
one-to-one — `CodeAttachmentTooLarge` — and permanent for that file, in
contrast to #1786's still-unbuilt `CodeAttachmentTooManyUploads`, which is
marked transient because it clears once other uploads finish. Distinguishing
sentinel from sentinel is wanted in-process, for this package's own tests and
for the daemon's logs, not on the wire — a single corrupt transfer must still
resolve to exactly one wire code, which is why `reject` (the shared latch
primitive every discarding family calls) has to run for a resource refusal
too: without the latch, a chunk sent after an `ErrUploadTooLarge` reject on
an already-refused transfer would fall through to a framing check, and one
transfer would emit two different wire codes at #1744.

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

## Mutation-testing lessons (measured across #1769, #1770, #1772, and #1787)

This package's sole-redness claims are measured with `go test -overlay`
(mutants applied via an absolute-path JSON manifest, no worktree write), not
argued from the table alone. Five traps found that way, in the order they
surfaced:

- **A mutant that perturbs a shared constant reddens more rows than a
  hand-derived table predicts, because moving the constant moves every
  boundary at once.** #1776's admission-layer table predicted its
  wrong-bound mutant (reading 32768 in place of `MaxAttachmentChunkBytes`)
  would redden the three rows placed *at* 45000; measured, it reddened five
  — the extra two were rows whose *distance* from the boundary changed sign
  once the bound moved, not rows sitting on it. The predicted set was a
  subset, so nothing was falsified, but the right way to predict a
  shared-constant mutant's red set is "every row whose arithmetic reads the
  constant, re-run under the new value" — not "every row placed at the
  constant, by inspection".

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
- **A lock-contention mutant can be probabilistic, and the fix is more
  rounds, not more goroutines.** #1787's split-lock mutant on
  `Registry.insert` (look the pair up under one acquisition of `mu`, unlock,
  reacquire to store) reddens the spec's own same-pair concurrent-insert test
  at only ~9% per iteration; run at the spec's own prescribed `-count=5`, that
  recipe is a false green roughly 62% of the time (measured at `-count=500`:
  45/500 red). Raising the fan-out from 32 to 256 goroutines barely moved
  detection (9% → 13.8%) — the collision window exists only during the first
  insert burst, so more contenders barely widen it. What worked was more
  *rounds*: a fresh `Registry` per round inside a loop, ~75 rounds for ≈99.9%
  detection. Before trusting a `-count=N` recipe on a lock-contention pin,
  measure its actual per-iteration hit rate rather than assuming N repeats
  compounds toward certainty — and prefer scaling round count over goroutine
  count when it doesn't.
- **An injected split-lock mutant has to replace the whole critical section,
  not prepend an extra check ahead of it.** The first attempt at #1787's
  split-lock mutant added an unlocked-then-relocked pre-check before the
  original lookup-then-store body; the original body's own atomic check still
  ran underneath it, so the mutant stayed green — which looks like "the
  criterion can't be pinned" but is actually an under-built mutant. Delete and
  replace the section, don't layer around it.
- **A "returns a fresh copy" test must mutate its own copy of the fixture,
  not the shared package-level one.** `TestAccumulator_SingleChunk_ReturnsAFreshCopy`
  has to feed the accumulator a copy of `testFixture`, then mutate the
  *returned* slice to prove the accumulator's copy is independent. Feeding
  `testFixture` directly and mutating the return would scribble on the
  package-level fixture, reddening every other parallel test in the file
  under the fast-path mutant instead of just this row — a sole-redness
  measurement taken against that setup would report the wrong thing about a
  mutant that is genuinely caught.
- **Deleting a struct field's `+=` update is not the same hazard as deleting
  a local's sole read or an import** (#1777, refining the build-failure trap
  above). Dropping `a.received += int64(len(chunk.Data))` compiles cleanly —
  a field that's assigned but never read doesn't trip Go's unused-variable
  check the way a local does — so the mutant measured the crossing test's
  real coverage with no `_ = a.received` prop needed. The "grep for `build
  failed`" trap is specific to locals and imports; a field-update deletion
  needs no such guard before trusting its verdict.

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
— #1772's filename sanitiser, #1781/#1782's storage, #1746's retrieval — are the
ones who'd otherwise read a green `Assemble` as a safety verdict rather than
a corruption check.

The declared `sha256` is also deliberately **not** promoted to a lookup key
anywhere in this package: there's no content-addressed retrieval ("know the
hash, fetch the blob"), which is a requirement from
`AttachmentChunkPayload`'s SECURITY block, not an oversight.

## Blocked family (not landed)

- **#1767** — closed, split into this family. The first-chunk
  `total_chunks`/`size` cross-check landed as `CheckDeclaration` (#1776, see
  § "Admission layer" above), the per-upload byte bound landed as
  `CheckDeclaredSize` + `Add`'s step 5 (#1777, see § "Per-upload byte bound"
  above), and the conn-keyed registry of in-flight uploads landed as
  `Registry` (#1787, see § "In-flight upload registry" above). Still blocked:
  the entry-count cap on that registry (**#1786**), which should inherit the
  `2N × bound` peak / `N × bound` resident figures from § "Per-upload byte
  bound" rather than re-deriving them.
- **#1742** — expiry/abandonment of a partial upload. Note this covers
  *partial* uploads only — a complete-but-corrupt transfer that fails one of
  #1770's integrity checks is not partial, so it's #1770's own `reject` latch
  (not #1742's reaper) that frees those held bytes.
- **#1773** — split into #1781 and #1782 while this family waited. Resolving
  and creating the attachment directory landed as `EnsureDir` (#1781, see
  § "Directory resolution and creation" above); writing bytes into it landed
  as `Store` (#1782, see § "Writing attachment bytes" above). (#1743 split
  into #1772/#1773 earlier in the same wait; #1772 — filename sanitisation —
  landed, see `SanitizeFilename` above.)
- **#1744** — wires the dispatch site: maps the three framing sentinels to
  `CodeAttachmentInvalidChunk` and the two integrity sentinels to
  `CodeAttachmentIntegrityFailed`, both via `errors.Is`, and is the first
  production caller of this package.
