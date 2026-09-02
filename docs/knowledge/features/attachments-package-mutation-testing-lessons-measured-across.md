# Mutation-testing lessons (measured across #1769, #1770, #1772, #1787, #1795, #1796, and #2037)

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
  naming scheme. #1795 hit the same failure mode through a different door —
  non-UTF8 fixture bytes landing directly in `go test -v` output rather than
  in a subtest name — which is why `grep -a` belongs in the overlay-mutant
  recipe unconditionally, not only when a subtest name is attacker-shaped.
- **A behaviour-preserving refactor's characterization test is green before
  and after the change, so the diff alone can't show the test is
  load-bearing — only a mutant that would have broken the old code can**
  (#1795, `TestRegistry_AdmitRefusedRepeatUnderAHeldPair_KeepsTheIncumbent`).
  The new test passed against unmodified `registry.go`, correctly, since the
  ticket preserved the pinned behaviour rather than changing it; the only
  evidence the test caught anything came from the mutant that hoists the
  incumbent look-up ahead of both checks, red on exactly the two new rows and
  green on the rest of the package. Where a refactor's spec asks a new test
  to guard against one specific silent flip, running that flip as a mutant is
  the RED half of red-green, not an optional extra.
- **A mutant run filtered with `-run` can't support a "nothing else reddens"
  claim — the filter is exactly what would make the claim look true
  regardless of whether it is.** #1795's first pass scoped the run to
  `-run 'TestRegistry_|TestAccumulator_|TestCheck'`, which happened to cover
  the whole package but was never checked to. The unfiltered re-run (128
  tests) is what actually backs the mutant table's "also reddens: nothing
  else" column — any claim of that shape needs the filter off.
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
- **A property true "by construction" still needs its own mutant, or it
  ships unpinned** (#1796, code review SHOULD FIX). The spec argued AC 3 —
  a held pair is never refused by the capacity gate — was satisfied by
  construction, because the incumbent look-up runs ahead of the gate in
  `insertLocked`. True of the code as written, but no fixture drove a held
  pair while the registry was at `maxInFlightUploads`, so an overlay mutant
  hoisting the gate ahead of the look-up passed **green across all 134**
  tests in the package. A by-construction argument describes the current
  shape of the code; it says nothing about whether a later one-line
  reordering would be caught. Treat every "satisfied by construction" claim
  in a spec as a mutant to build and run, not as a reason a row can be
  skipped — this one shipped as a known, tracked gap (see § "In-flight
  upload registry" above) rather than silently.
- **A split-lock control has to satisfy every other row, or "sole red"
  can't be measured.** #1796's first cut of the count-then-admit mutant
  (`Admit` reads `r.count()` before `mu.Lock()`, decides on the stale
  number inside the lock) returned `capacityRefusal` without first running
  `CheckDeclaration`/`CheckDeclaredSize`, so it also reddened the gate-
  ordering test (AC 1) — two rows red, neither one the sole red the AC 4
  measurement needed. The fix duplicated the declaration checks into the
  mutant's capacity branch so it satisfied every row except the
  indivisibility one. A control mutant built to isolate one property has to
  be built to pass every *other* property first, or its redness proves
  nothing about the one row it was meant to isolate.

- **A mutant that drops a release can redden a test whose *subject* is
  something else, if that test happens to end its fixture through the
  dropped path** (#1784). The mutant that skips `Release` after an `Add`
  refusal was predicted to redden only the four `Add`-refusal table rows;
  measured, it also reddened the held-pair-at-the-bound test, whose closing
  assertion re-delivers a chunk to prove the first one landed in the
  incumbent and, as scaffolding rather than as its point, ends that transfer
  through an `Add` refusal. The prediction was a subset, so nothing was
  falsified, but a sole-redness claim written from a test's *name* will
  understate itself wherever the test's fixture walks through the mutated
  path on its way to proving something else. Predict a mutant's red set from
  what each test *executes*, not from what it is named after.
- **A "distinct from every other sentinel in the package" claim needs the
  package's sentinel list to be a fixture, not a hand-picked sample of near
  misses** (#1784, `packageSentinels` in `registry_test.go`, all eight across
  `accumulator.go`/`admission.go`/`storage.go`). Three chosen-by-eye
  candidates would leave the quantifier ("every") unproved while reading as
  coverage; looping `!errors.Is(err, s)` over the actual declared set is what
  proves it. The cost is real and one-directional: a sentinel added later and
  not appended to that slice weakens the claim silently, with no test going
  red to flag the drift.
- **Predicting a mutant's red set means reading whether each assertion is
  absolute or relative, not just which fixture executes the mutated line**
  (#1880). The spec predicted the unstamped-admission mutant (`insertLocked`
  stores a zero `lastChunkAt`) would redden AC 1, AC 2 and AC 4; measured, it
  reddened AC 1, AC 3 and AC 4 instead — AC 2 stayed green. AC 2's
  post-delivery assertions are relational ("the delivered pair's stamp now
  equals the advanced reading; the untouched pair's stamp is what it was"),
  and a zero pre-delivery stamp that stays zero satisfies both comparisons
  unchanged; AC 3 reddens instead because it asserts the stamp equals the
  admission-time reading outright. The mutant was caught either way, so
  nothing about the shipped code was wrong — but a prediction built from
  "which test's fixture touches the mutated line" gets the row wrong where
  "what shape each assertion actually compares against" would not.
- **One mutant built to defend a by-construction claim doesn't inoculate the
  rest of the same doc against the same failure mode** (#1880, code review
  SHOULD FIX). Mutant 4 in this slice's spec exists specifically to pin
  `insertLocked`'s incumbent-before-store ordering — the same by-construction
  gap #1796 shipped unpinned and paid for, two entries above. The very next
  paragraph of that same spec claimed, also by construction and with no
  mutant of its own, that `Admit` and `insert` "inherit the stamp from this
  one shared core." Code review measured it: an overlay mutant moving the
  stamp out of `insertLocked` into `Admit`'s body passes green across all 151
  tests, because `insert` is unexported and test-only and no production path
  can reach it today. Not a behaviour bug in shipped code, but the exact
  invariant `reapExpiredLocked` (#1881) depends on — an entry inserted with a
  zero stamp reads as infinitely idle and is reaped on the very next pass —
  and #1881's own four new tests all drive `Admit`, so this still shipped
  unpinned rather than fixed.
  Having already named the by-construction trap once in a spec is not
  evidence the rest of that spec is safe from it — every by-construction
  sentence needs its own mutant, not just the one already flagged as risky.
- **A sweep-and-delete mutant's red set is a property of each fixture's
  cardinality, not of which AC the test's name cites** (#1817). The spec
  predicted `ReleaseConn`'s stop-after-first-delete mutant (`return`
  immediately after the loop's first `delete`) would redden only the tests
  named for "every upload" and "the freed slots"; measured, a third reddened
  too, because that test's conn happened to hold two entries rather than one.
  Same discipline #1784 already states two entries above ("predict from what
  each test executes, not from what it is named after"), refined for this
  mutant shape specifically: for a sweep over a multi-entry key space, read
  each fixture's admit calls for how many entries the mutated conn holds,
  not the AC label on the test.
- **A fixture helper's own unexported naming scheme is not a citable
  contract** (#1817). `fillRegistry` names its ids with a private `filler-%d`
  pattern; three of `ReleaseConn`'s four new tests admit their pairs directly
  instead of calling it, because each needed either a second conn or to
  `Lookup` an id back out afterward, and copying the helper's naming scheme to
  the call site would cite an implementation detail with no symbol for
  `cite-guard` to anchor — the one rot shape that guard cannot see. Widening
  `fillRegistry` to take a conn parameter was the alternative and is worse:
  several existing capacity tests depend on it staying single-conn.

- **A validate-then-look-up table built entirely from absent-and-invalid
  fixtures can refuse every row for the wrong reason** (#2037). `EnsureDir`'s
  seven invalid-id rows, reused as `ResolvePath`'s own shape-refusal table,
  each name an id that *also* names a directory that doesn't exist — so every
  row refuses by absence, and the shape check the table is named after is
  never the reason it went red. An overlay mutant deleting both
  `conversations.ValidID` calls passed the whole package green against it.
  The fix was one row where the invalid shape still *resolves*: a
  case-folded id, since `EvalSymlinks` doesn't case-canonicalise and APFS is
  case-insensitive by default, so the uppercased pair reaches the directory
  the lowercase pair stored into on macOS (`TestResolvePath_CaseFoldedID`,
  that mutant's sole red). Any table pairing a validator with a lookup needs
  at least one row where the invalid input still finds something, or the
  validator is never actually exercised by the table at all.
- **A "stable across repeated calls" assertion is satisfied by any
  deterministic rule, including the wrong one** (#2037). `ResolvePath`'s
  requirement that two files in one attachment directory resolve to the same
  path on every call reads as a pure stability property, and a reverse-order
  or write-order tie-break is just as stable as the shipped lexicographic
  rule — every deterministic rule passes a same-answer-every-call test.
  Pinning it needed the exact expected path, from a fixture whose
  lexicographically smaller name is written *first* so the neighbouring
  wrong rules answer a different file than the shipped one. Measured: an
  overlay mutant walking the directory entries in reverse reddens
  `TestResolvePath_TwoFilesOneAttachmentDirectory` alone. A criterion phrased
  as a property ("the same answer every time") often needs a fixture built
  to separate the intended rule from its neighbours, not one that merely
  exhibits the property.

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
