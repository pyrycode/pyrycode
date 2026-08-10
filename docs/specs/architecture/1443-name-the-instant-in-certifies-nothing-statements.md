# #1443 — Name the instant in the run classifier's certifies-nothing statements

**Ticket:** [#1443](https://github.com/pyrycode/pyrycode/issues/1443) · **Size:** S (confirmed, not overridden) · **Baseline:** `main` at `5907785`

Every line reference below was measured against that commit.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/trail_run_outcome_test.go:1-92` | The file header's doctrine. The probe's question — *was a backgrounded command still running when pyry declared the turn finished?* — is the declared-finished instant, stated in prose before any constant names it. |
| `…/trail_run_outcome_test.go:114-220` | The thirteen-value space. `:130-135` is the budget-fired carve-out, `:136-138` / `:164-182` / `:183-214` the four docs this ticket amends or leaves as the model. |
| `…/trail_run_outcome_test.go:358-373` | `trailClassifyRun`'s ordering argument. Shape-A site `:361`. |
| `…/trail_run_outcome_test.go:530-600` | Step 1's switch: the in-body comment (`:531-532`, shape A), the three Details this ticket exchanges (`:556-557`, `:573-574`, `:588-589`), the two in-arm comments (`:568`, `:582`, shape B), and the two statements that are already correct and must not move (`:548`, `:598`). |
| `…/trail_run_outcome_test.go:1074-1135` | `TestTrailClassifyRun` — AC4's per-row truncation-marker check at `:1098`, and the coverage loop. **Read it to confirm it needs no edit**, not to edit it. |
| `…/trail_run_outcome_test.go:1652-1678` | The file's last declaration. Line `1655` is the highest line any other file cites; the new constant is appended after `1678`. |
| `internal/e2e/realclaude/trail_sighting_liveness_test.go:203-216` | `trailSightingInstantClause` — the shape to mirror: fixed prose, no interpolation, a stated byte cost, and a doc that says sharing alone does not make the rule true. |
| `internal/e2e/realclaude/trail_sighting_liveness_test.go:576-585` | #1440's clause assertion. The shape to mirror *and to deviate from* — that one is unconditional over every row; this ticket's must be conditional on the arm. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:352-354` | `trailDetail` = `reachCapCommand(fmt.Sprintf(...))`. Nothing is prepended, so a rendered Detail's byte count is exactly the formatted string. |
| `internal/e2e/realclaude/background_reach_probe_test.go:117-124, :945-950` | `reachMaxCommandBytes = 512`, `reachTruncationMarker`, and `reachCapCommand`'s silent truncation — unchanged at exactly 512, marker only past it. |
| `internal/e2e/realclaude/trail_ptyrunner_composition_test.go:180-251` | AC5's regression pin. Its headroom check at `:219-226` measures the `run-running-at-trailer` arm, which this ticket does not touch. |

---

## Size check

**S, one grade below no override needed.** Red lines, measured rather than judged:

| Red line | Measured |
|---|---|
| > 3 new files | **1** (`trail_run_instant_clause_test.go`) |
| > ~600 LOC total written | **~200** (~15 const + doc, ~110 new test file, ~40 rewritten prose lines, spec doc excluded) |
| > 5 new exported types | **0** (one unexported `const`) |
| > 10 consumer call sites | **0** — see below; this is the finding that shaped the design |
| > 5 ACs of work | **5 ACs, 3 of work** — AC4 and AC5 are discharged by shipped mechanisms and are verification only |
| > 10 reject branches | **0 new** — no arm added, no branch added, no log call added |

Production source files (non-test `*.go`) prescribed: **0**.

### The edit fan-out check found a real cascade, and the design removes it

`trail_run_outcome_test.go` carries **100 inbound line-number cites from 16 other files** in `internal/e2e/realclaude/`, plus cites from `docs/knowledge/codebase/*.md`. The distribution is front-loaded:

| Insertion point | Cites displaced |
|---|---|
| the import block (`:93-98`) | 98 |
| the budget-fired doc (`:130`) | 93 |
| before `trailClassifyRun` (`:333`) | 62 |
| inside step 1 (`:530`) | 38 |
| inside `TestTrailClassifyRun` (`:1098`) | 11 |
| after the last declaration (`:1678`) | **0** |

Naive execution of this ticket — a carve-out sentence at `:132`, a clause constant somewhere sensible, an assertion inside `TestTrailClassifyRun`, an `"os"` import for the sweep — displaces essentially every one of those 100 cites and turns a prose ticket into a 100-edit cross-file renumbering sweep. That is the exact failure #1417 hit in this same file: **two code-review FAIL rounds, 79 then 2 stale cites**, recorded at `docs/knowledge/features/e2e-realclaude.md:1785`.

The response is **not** a split. Splitting multiplies the cascade — two children inserting into this file means two sweeps. The response is a design in which the displacement is **zero**, verified by one deterministic command (§ *The zero-displacement rule*). This is not "the edits are mechanical so they don't count"; it is "there are no edits, because nothing moves."

### File-overlap check

`git fetch origin --prune` then a scan of every `origin/feature/<n>` branch's diff against `origin/main`: **no in-flight branch touches `internal/e2e/realclaude/trail_run_outcome_test.go`**. No `blockedBy` edge needed.

---

## Context

#1440 shipped `trailEstablishSighting` (`trail_sighting_liveness_test.go:350`), which establishes aliveness at **the trailer's sighting on pyry's stdout** from a certified ordering plus a pinned pid — on exactly the paths where no terminal reason is certified. Two instants now exist in this family where one used to:

- the **declared-finished** instant, which exists only where the gate certifies a terminal reason, and
- the **trailer-sighting** instant, which exists whenever the trailer was sighted.

`trail_run_outcome_test.go` predates that vocabulary. Six of its statements forecloses a claim on the grounds that nothing was certified, and each is right about the declared-finished instant and wrong as a blanket. Read today, `:361`'s *"there is no certified instant for **any** claim to be about"* forecloses the sighting-based finding #1440 just built, from a file that cannot see it.

Nothing here changes a verdict. No value joins any space, no arm moves, no input is read that was not read before. Three Details exchange a hand-written sentence for a shared one; every outcome this classifier can reach stays the outcome it reaches today.

---

## Design

Four pieces, in dependency order.

### 1. `trailDeclaredFinishInstantClause` — the shared clause

**Contract.** An unexported package-level `const string` in `trail_run_outcome_test.go`. Fixed prose interpolating no input — no `%` verb, no argument, nothing derived from a reading. It is **prose, not a value**: it does not join `TestTrailAdmissibilityConstantsAreClosed`'s union map, exactly as `trailSightingInstantClause` did not (that map holds the fifty-one space values; a clause in it would assert a membership nobody can hold).

**Recommended text** (measured; substitutions must re-run the arithmetic in § 2):

> `Nothing is certified, so there is no declared-finished instant for a claim about aliveness-at-declared-finish to be about.`

**122 bytes.** Spliced by the arms as `"…prose. "+trailDeclaredFinishInstantClause+" Kept apart from …"`, so its contribution including the one separating space is **123 bytes, under AC1's 129-byte ceiling with 6 to spare**. #1440's analogue costs 76.

**Why `aliveness-at-declared-finish` and not `aliveness-at-trailer-write`.** The parallel-sounding name is the trap: `aliveness-at-trailer-write` **contains** `aliveness-at-trailer` as a substring, which makes AC3's residual sweep impossible as a plain occurrence count — it would have to distinguish qualified from unqualified uses by lookahead. The chosen term shares no substring with the phrase being retired, so the sweep is a literal count of two. It also ties to the file header's own statement of the probe's question (`:17-18`, "WHEN PYRY DECLARED THE TURN FINISHED") rather than coining a second vocabulary for it.

**Placement: appended after the file's current last line (`:1678`), in its own section**, not beside the arms that use it. This is the one deliberate oddity in the change and it must carry its reason **in code**, because a reviewer will ask: the file has 100 inbound line cites whose highest target is `:1655`, and a declaration anywhere above that displaces them. The identifier is one grep away in the same file; a hundred stale cites are not.

### 2. The three Details — an exchange, never an append

Measured on `main` at `5907785`, with `reachMaxCommandBytes = 512` and `reachCapCommand` truncating silently:

| arm | now | append the clause | **exchange** the 89 B sentence | result |
|---|---|---|---|---|
| `trailGateNoTrailer` (`:556-559`) | 262 B | 385 B — fits | replaces `:557`'s equivalent sentence | **295 B** (217 free) |
| `trailGateAbsentOwesNone` (`:571-577`) | 461 B | **584 B — 72 over** | 372 B of other prose + 123 | **495 B** (17 free) |
| `trailGatePresentOwesNone` (`:586-592`) | 472 B | **595 B — 83 over** | 383 B of other prose + 123 | **506 B** (6 free) |

Both `OwesNone` arms already spend the 89-byte sentence *"Nothing is certified, so there is no declared-finished instant for this run to be about. "* saying by hand what the constant will say. **Exchanging that sentence for the clause is what makes AC2 and AC4 jointly satisfiable.** Appending makes them contradictory and the developer meets an unresolvable red.

`trailGateNoTrailer` has no such sentence verbatim; its `:556-557` says the same thing in its own words (*"so there is no instant at which pyry declared the turn finished for this run to be about"*). Exchange that clause, keeping *"no trailer line was written"* — that half is the arm's own content, echoed by `trailOutcomeVoidNoTrailer`'s doc (`:136-138`), by two sibling Details (`:575`, `:590`) and by the table row name (`:928`).

**Do not shorten any arm's surviving prose to buy headroom.** `:571-577`'s and `:586-592`'s closing "Kept apart from %s" arguments are asserted by the two composition tests (`:1364`, `:1507`), and the exchange fits without touching them.

### 3. The six statements

All six are **comments**, so none is runtime-observable and none can be pinned by a test. AC3's checkable half is the residual sweep in § 4; these six are prose changes reviewed by reading.

Two defect shapes, and the amendment differs per shape:

| # | site | shape | amendment | Δ chars |
|---|---|---|---|---|
| S1 | `:361` | A — blanket | `no certified instant` → `no certified declared-finished instant` | +18 |
| S2 | `:531-532` | A — blanket | same | +18 |
| S3 | `:167-169` | B — two-way ambiguous | `aliveness-at-trailer` → `aliveness-at-declared-finish` | +8 |
| S4 | `:188-190` | B — two-way ambiguous | same | +8 |
| S5 | `:568` | B — unqualified claim | `for a claim to be about` → `for an aliveness-at-declared-finish claim` | +17 |
| S6 | `:582` | B — unqualified claim | same | +17 |

**Why shape A needs only the instant.** Once the sentence reads *"without a usable trailer there is no certified **declared-finished** instant for a claim to be about"*, the claim it forecloses is scoped by the instant it names — claims that need a declared-finished instant. A sighting-instant claim needs no such instant and is left untouched, which is the whole point of the ticket. Adding a second qualifier to the claim as well would push both paragraphs past their reflow budget (§ *The zero-displacement rule*) for no gain.

**Four statements are already correct and are the model, not the work.** `:137`, `:200` and `:928` say *"when the turn was declared finished"* outright; `:598` says *"no instant is certified"*, a statement about certification rather than about a claim; `:548` is the one place an instant **is** certified. None moves.

### 4. The budget-fired carve-out, and the residual sweep

`trailOutcomeVoidBudgetFired`'s doc (`:132`) and its arm's Detail (`:552`) keep the bare phrase deliberately. They argue from a different premise: the gate **certifies** a terminal reason there and the reap provably preceded the trailer write (`runner.go:492-503`), which forecloses **both** instants rather than neither. Once shape B is disambiguated these are the only bare uses left in the file, so the carve-out is recorded rather than left silent, in two places:

- **In place, at both sites**, by extending the shared phrase to `…could prove aliveness-at-trailer, at either instant.` — 19 bytes at each, which fits `:132-134`'s comment reflow (43 chars of slack) and the arm's Detail (341 B rendered, 171 B free → 360 B, 152 B free). The phrase itself stays literally present, so the sweep still counts two.
- **In full, in the sweep test's doc**, which is also the mechanism that reddens when someone "fixes" one of them.

**The sweep** counts occurrences of the literal phrase in `trail_run_outcome_test.go`'s source and requires **exactly two**, each on a line that also carries the budget-fired argument's shared prefix (`no attribution on that path could prove`). Both surviving sites carry it today: `:132` is a comment and `:552` is a format string, so no runtime inspection of Details can see both — **reading the source file is the only mechanism that can**, and that is why this test exists rather than an assertion over rendered Details.

Two constraints on it:

- Its needle must be **assembled**, not written as one literal, or the sweep's own source would match if it ever moved into the file under test. Say so in its doc.
- It asserts **no count** of `aliveness-at-declared-finish`. A number kept by hand beside a set drifts — this family states that doctrine itself at `trailer_admissibility_test.go:1172-1177`, where a shipped comment said twenty-nine while the map already held thirty-five.

### The zero-displacement rule

**Every hunk in `trail_run_outcome_test.go` at or before old line 1655 must add exactly as many lines as it removes. The only unbalanced hunk permitted is the append after old line 1678.** Consequences that follow from it, and that the developer must design each edit around:

- **No new import in `trail_run_outcome_test.go`.** The clause is a plain string and the arms splice it by concatenation; nothing new is needed. The sweep test's `os` would have cost one import line and displaced 98 cites — that alone is why the two new tests live in a new file rather than beside the loop they mirror.
- **No new lines in `TestTrailClassifyRun`'s loop.** AC2's assertion becomes its own test over `trailRunCases()` in the new file. It is the same coverage — each of the three arms has exactly one row — and it buys a place to argue the conditionality properly.
- **Every prose amendment is a reflow of its own paragraph**, not an insertion. Comments in this file wrap at 80–83 columns with tabs at 4; the measured slack at each site is:

  | site | lines | used | slack at 82 | needs |
  |---|---|---|---|---|
  | `:132-134` (carve-out) | 3 | 203 | 43 | +19 ✅ |
  | `:167-169` (S3) | 3 | 206 | 40 | +8 ✅ |
  | `:188-190` (S4) | 3 | 235 | 11 | +8 ✅ |
  | `:360-363` (S1) | 4 | 271 | 57 | +18 ✅ |
  | `:530-544` (S2) | 15 | 1200 | 30 | +18 ✅ |
  | `:566-570` (S5) | 5 | 383 | 27 | +17 ✅ |
  | `:579-585` (S6) | 7 | 547 | 27 | +17 ✅ |

  Every site fits without spilling a line. `:188-190` and the two in-arm comments are the tight ones; reflow the whole paragraph rather than editing one line.

- **The three Detail blocks keep their source line counts** (4, 7 and 7). Their source gets *shorter* — an 89-byte literal becomes a 32-character identifier — even though the rendered string grows, so holding the line count is a matter of not letting the reflow collapse a line.

**Verify, don't assume:**

```bash
git diff -U0 -- internal/e2e/realclaude/trail_run_outcome_test.go |
  awk '/^@@/{split($2,a,","); split($3,b,","); o=(a[2]==""?1:a[2]); n=(b[2]==""?1:b[2]);
       if (o!=n) print "UNBALANCED " $0}'
```

Any output other than the single trailing append hunk means cites have moved and the change is not done. Spot-check three anchors afterwards — `:431` must still be C2's comment, `:612` the proof arm's `if`, `:1655` `func TestTrailRunOutcomeValuesAgreeWithThePredicate`.

If a site genuinely cannot be balanced, **that is the expensive path and it must be reported, not absorbed**: every displaced cite in the sixteen citing files has to be re-pointed in the same commit, in all three forms this repo uses — `<file>.go:NNN`, bare `(:NNN)` inheriting the last-named file, and `<Symbol>:NNN`.

---

## Concurrency model

None. `trailClassifyRun` is pure over its input — no exec, no clock, no filesystem, no `*testing.T` — and this ticket adds no goroutine, channel, lock or shared state. The one package rule that binds: **fixtures are functions, never package-level vars** (`:697-699`), because `go test -race` runs this package in parallel and a shared backing array is reachable from every test. The new constant is a `const string`, which is immutable and therefore outside that rule; the sweep test's file read is per-invocation and shares nothing.

## Error handling

Unchanged, and deliberately so. The classifier never fails a test and never returns an error — an instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn (`:338-342`). No new failure mode is introduced: no arm is added, no branch is added, and the only new I/O in the change is the sweep test's `os.ReadFile` of a fixed path in its own package directory, whose failure is a `t.Fatalf` because a sweep that cannot read its subject has measured nothing and must never report a clean pass.

---

## Testing strategy

Both new tests live in `internal/e2e/realclaude/trail_run_instant_clause_test.go` (`//go:build e2e_realclaude`, `package realclaude`).

### `TestTrailRunCertifiesNothingArmsNameTheInstant` — AC2

Drives `trailRunCases()` and checks the clause **conditionally on the outcome**, in both directions:

- For each row reaching `trailOutcomeVoidNoTrailer`, `trailOutcomeVoidPathOwesNoReason` or `trailOutcomeVoidReasonNotOwedByPath`: the Detail **contains** `trailDeclaredFinishInstantClause`. An arm that drops the clause from its own format string reddens here — sharing the constant is not on its own what makes the rule true.
- For every other row: the Detail **does not** contain it. This is what makes "only these three" checkable rather than "at least these three", and it is the executable half of the budget-fired carve-out — the budget-fired arm forecloses both instants from a *certified* reason and must not wear a certifies-nothing clause.
- A premise first, in this package's idiom: assert that the three outcomes are distinct and that each is reached by at least one row, so a table that stopped producing them cannot make the conditional vacuously true.
- The doc must state why the check is conditional where #1440's (`trail_sighting_liveness_test.go:581`) is unconditional: there, every arm of one predicate names one instant; here, three of thirteen outcomes do.

### `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly` — AC3

- Reads `trail_run_outcome_test.go` from the package directory (`go test` runs with the package as cwd). A read error is `t.Fatalf` — see § *Error handling*.
- Needle assembled from parts, never written whole. State why in the doc.
- **Exactly two** occurrences. On a mismatch, report the line numbers found, not just the count — a bare count tells the next reader nothing about which site drifted.
- Each occurrence's line also contains `no attribution on that path could prove`. This pins their *identity*, where the count pins the set's *size*.
- The doc carries the carve-out in full: the budget-fired arm's gate certifies a reason, the reap provably preceded the trailer write, so both instants are foreclosed there and the phrase is deliberately unqualified — which is why qualifying it would be wrong, not an improvement.

### What is already covered and must not be duplicated

- **AC4** — `TestTrailClassifyRun`'s per-row check (`:1098`) asserts the truncation marker is absent on every row, and each of the three arms has exactly one row in `trailRunCases()`. The two composition tests assert it again at `:1367` and `:1510`. All three stay unamended; the mechanism must be left unweakened, not re-implemented.
- **AC5** — `trailRunOutcomeValues()` (`:1633`) and its thirteen-value pin (`:1657`) are untouched, and `trail_ptyrunner_composition_test.go:180-251` passes unamended. Its Detail-headroom check (`:219-226`) measures the `run-running-at-trailer` arm, which this ticket does not touch — a regression guard here, not the cap check for the added prose.

### Verification — the build tag hides these files from `make check`

`//go:build e2e_realclaude` means `make check` **does not compile them**. An exit code cannot tell a skip from a pass, so read the count of tests that actually ran:

```bash
go test -tags e2e_realclaude -race -run '^TestTrail' -v ./internal/e2e/realclaude/ 2>&1 | grep -c '^=== RUN'
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
gofmt -l internal/e2e/realclaude/
```

Nothing here needs a live claude, credentials or a daemon: every arm is driven from fixtures over the readings record, as #1439 and #1440 were. Then run the hunk-parity command from § *The zero-displacement rule*.

---

## AC → deliverable

| AC | Deliverable |
|---|---|
| 1 | `trailDeclaredFinishInstantClause`, appended after `:1678`, 122 B, no interpolation, not in the union map |
| 2 | Three Details exchange their hand-written sentence for the clause; `TestTrailRunCertifiesNothingArmsNameTheInstant` asserts presence on those three and absence elsewhere |
| 3 | Six prose amendments (S1–S6); `TestTrailRunBareAlivenessAtTrailerIsBudgetFiredOnly` pins the two-site residual; carve-out recorded in place (`, at either instant`) and in full in that test's doc |
| 4 | No new code — `:1098`, `:1367`, `:1510` stay unamended and green |
| 5 | No new code — `trailRunOutcomeValues()` and `trail_ptyrunner_composition_test.go` stay unamended and green |

## Out of scope

- **`trailer_admissibility_test.go:758` and `:843`** carry the same phrase in the **gate** space. Named by the ticket as out of scope; this ticket is the run classifier only.
- **`trailer_admissibility_test.go:174`** carries *"declared-finished instant for such a claim to be about"* in the gate value's own doc. Not named by the ticket, same space as the two above, same reason. Leave it.
- **`docs/knowledge/codebase/1443.md`** is the documentation phase's, written from this spec and the merged diff after the PR lands. Not a developer deliverable.
- **`docs/knowledge/features/e2e-realclaude.md`** carries no line-number cite into `trail_run_outcome_test.go` (verified), so the zero-displacement design leaves it correct with no edit.

## Open questions

1. **Clause text.** The recommended 122-byte text is measured and fits with 6 bytes to spare on the tightest arm. A developer who prefers different wording must re-run § 2's arithmetic against `trailGatePresentOwesNone`, whose 383 bytes of other prose set the ceiling at 129 including the separating space. Longer shapes were measured and rejected: spelling the instant out as *"no instant at which pyry declared the turn finished"* costs 132 B, and adding an explicit *"…and no other claim is foreclosed"* rider costs 143 B — both overflow that arm. AC4's per-row check is what catches an overrun; treat a red there as arithmetic, not as a test to relax.
2. **Constant name.** `trailDeclaredFinishInstantClause` is chosen for symmetry with `trailSightingInstantClause` and because it names its instant in the identifier, which is the property this ticket is about. If a reviewer prefers `trailRunDeclaredFinishClause`, nothing else in the design changes.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The change adds no boundary and moves none. `trailClassifyRun` is pure over `trailRunReadings`, which by construction carries discriminators and already-publishable records only (`:224-270`); the clause is a compile-time constant that interpolates no input at all, so it cannot become a channel for anything a reading carried. The one new read of external data is the sweep test's `os.ReadFile` of a fixed, repo-relative filename — repo source, not a capture, and never rendered into any published record.
- **[Tokens, secrets, credentials]** No findings, and the category is load-bearing here rather than absent: this file's whole doctrine (`:47-77`) exists because `ps`-style columns route an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` into artifacts destined for public issues. The design adds **no field** to `trailRunReadings` or `trailRunOutcome`, adds **no `%` verb** to any Detail, and interpolates nothing new — the three exchanged Details interpolate exactly the outcome and gate constants they interpolate today. `TestTrailRunOutcomeCarriesNoCapturedBytes` (`:1571-1627`) and its structural key walk stay unamended and still enforce it.
- **[File operations]** SHOULD FIX, bounded. The sweep test reads `trail_run_outcome_test.go` by fixed relative name from the package directory. No user input reaches the path, no traversal is possible, no write occurs, and no TOCTOU window exists (single read, no prior stat). The one hazard is a **silent** failure: a read error, or a rename of the subject file, must be `t.Fatalf` and never a skipped check that reports two-and-clean by vacuity. Specified in § *Error handling*; code-review should confirm it, since a sweep that passes without reading is precisely the "grep recipe that cannot fail" shape.
- **[Subprocess / external command execution]** Not applicable by design, and checkable: the change adds no `exec`, no `os/exec` import, and no helper that reaches one. Every arm remains driven from fixtures over the readings record — the property that lets the whole family run with no credentials and no live claude.
- **[Cryptographic primitives]** Not applicable. No randomness, no comparison against a secret, no key material anywhere in the change. The sweep's comparisons are `strings.Contains` over repo-authored prose.
- **[Network & I/O]** Not applicable. No socket, no HTTP server, no deadline, no connection. The only size bound in play is `reachMaxCommandBytes = 512`, and the design **tightens** the margin on two arms rather than relaxing it (`:571-577` to 495 B, `:586-592` to 506 B) with the shipped per-row marker check unchanged as the enforcement.
- **[Error messages, logs, telemetry]** No findings, and this is the category the ticket is actually about. Every byte added to a published Detail is fixed repo prose; nothing added is derived from a trailer, an argv row, a subprocess's stderr or a pid. The failure messages the two new tests emit print the classifier's own Detail and the sweep's own line numbers — both already publishable under the same doctrine.
- **[Concurrency]** No findings. No goroutine, channel or lock is added. The package's one standing rule — fixtures are functions, never package-level vars, because `go test -race` runs these in parallel (`:697-699`) — is satisfied trivially: the addition is an immutable `const string`, and the sweep reads its own byte slice per invocation.
- **[Threat model alignment]** The relevant threat for this family is the one `docs/knowledge/features/e2e-realclaude.md` states for the whole probe: a published artifact must be safe to paste into a public issue **without operator review**. The design's contribution is that it adds only fixed prose to that artifact and no new interpolation site. The adjacent risk this ticket deliberately does not address — the same ambiguous phrase in the **gate** space (`trailer_admissibility_test.go:758`, `:843`, `:174`) — is named out of scope above and remains a correctness/clarity matter, not an exposure.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
