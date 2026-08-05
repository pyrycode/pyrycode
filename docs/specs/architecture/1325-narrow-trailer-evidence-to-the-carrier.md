# #1325 — Narrow the trailer record's evidence tests to what the sighting carrier still lets them prove

**Size:** S · **One file:** `internal/e2e/realclaude/finding_trailer_evidence_test.go` · **Offline, no new symbol except one rename.**

Line numbers are as of `eea9f91`. Locate by symbol, never by line.

---

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_trailer_evidence_test.go` — **the whole file (898 lines)** | The only file this ticket edits. Read it end to end before the first edit; every decision below is a surgery on prose that argues for itself. |
| `…/finding_trailer_evidence_test.go:827-898` | `TestFinTrailerRecordCarriesNoCapturedBytes` — the four-checks-in-a-trench-coat test. §2 and §3 are its surgery. |
| `…/finding_trailer_evidence_test.go:449-554` | `TestFinTrailerRecordReadsTheDecodedTrailer` — the shell. Row 1 (`:464-506`) is deleted by §4; row 2 (`:516-553`) survives and is re-stated by §5. |
| `…/finding_trailer_evidence_test.go:139-154` | `finTrailerRecord`'s Detail content rule — the **type-level** statement of the headroom rule and the source of §3's replacement argument. `:151` carries a second dangling pointer; `:153-154`'s cite is untouched. |
| `…/finding_trailer_evidence_test.go:290-326` | `finTrailerSighting` — the fixture-side copy of the gather's fill, and its **agreement obligation** (`:294-301`). §5's re-stated row is the only executable pin on this half of it. |
| `…/finding_trailer_evidence_test.go:556-671` | `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` — where §4's zero-fields assertions land, driven on **both** arms. Also `:577-582`, the fixture argument §2 reuses. |
| `…/finding_trailer_evidence_test.go:337-447` | `TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator` — the absent row (`:366-372`), its state assertion (`:405-408`) and the executable coverage loop (`:437-441`) that §4's `trailAbsent` claim lands on. |
| `…/finding_run_gather_test.go:1077-1192` | `TestFinGatherReturnsNoCapturedBytes` — where §2's retired in-cap claim now holds. The in-cap precondition **asserted in code** is `:1138-1146`; the carrier is swept as the third return at `:1158-1191` (`:1164-1172` argues why). |
| `…/finding_run_gather_test.go:1681-1712` | `TestFinSightingReachesNoScanType` — where §4's panic claim now holds. `:1704-1709` names "the panic-on-unchecked-deref obligation the discriminated optional imposes". |
| `…/finding_run_gather_test.go:1526-1677` | `TestFinGatherSightingScalarsComeFromTheFullLineDecode` — the **other half** of the agreement obligation, and the naming and shape §5 mirrors. Note `:1662-1667`'s pair precondition and `:1668-1676`'s assertion form. |
| `…/finding_run_record_test.go:466-475` | `finRecordSeenTrailer` / `finRecordAbsentTrailer` — build `finTrailerRecord` with the **shipped** `finTrailerBuild`. First leg of §3's travel argument. |
| `…/finding_run_record_test.go:137-151`, `:193`, `:754-757` | `finRecordRun.Trailer` embedded **whole** under `json:"trailer"`, pinned by `TestFinRecordEmbedsTrailerRecordWhole`. `:141` cites `TestFinTrailerRecordCarriesNoCapturedBytes` and is **out of scope, not falsified** — do not touch. |
| `…/finding_run_record_test.go:930-940` | `TestFinRecordCarriesNoCapturedBytes`' doc: "The marshal sweep below IS recursive — it walks the embedded sub-records". Second leg of §3's travel argument. |
| `…/finding_artifact_write_test.go:759-780` | The artifact's Detail walk: collects every Detail by JSON path, asserts **at least six** (`:763-767`, naming the trailer's as one), and applies the *identical* `room < len(trailNeedle)` test per path (`:772-780`). Third leg of §3. **Read-only — this file is #1326's.** |
| `docs/specs/architecture/1320-trailer-builder-takes-the-sighting-carrier.md:215-231` | #1320's handoff and its Open Question 3 (the shell decision §6 settles) and 4 (the stale "pad 200" cite, #1326's). |

**Do not edit** `finding_run_gather_test.go`, `finding_run_record_test.go` or `finding_artifact_write_test.go`. They are #1324's and #1326's.

---

## Context

Since #1320 (merged at `eea9f91`) `finTrailerBuild` takes `finSighting` — eight scalars, no `.Line`, no `*resultTrailer` — in place of a `trailObservation`. Three rows in `finding_trailer_evidence_test.go` were written against the old channel. #1320 migrated each minimally and marked it `SUPERSEDED BY #1320, RETIRED BY #1321 — NOT A LIVE GUARD`, leaving the argument for retiring them here.

The thing being fixed is a category of prose, not a bug: **a row asserting the absence of a needle its input structurally cannot carry is not a weakened test but a test of nothing.** This family's own doctrine is that a green proof of a false claim is worse than no proof.

The trap is that retirement is **narrower** than the three marks make it look, in two directions:

- `TestFinTrailerRecordCarriesNoCapturedBytes` is four checks in a trench coat and only two ever read `.Line`. The per-row headroom assertion is the fix #1284 shipped a defect to earn; the flat forbidden-key scan guards a *future* field. Retiring the test whole would delete a shipped regression guard.
- The surviving headroom assertion is justified **twice** — in its comment and in its own `t.Errorf` — by "the containment checks below", and those are exactly the checks §2 retires. Retire them without re-stating and the assertion runs with a reason describing nothing in its test, which is the shape a later developer reasonably reads as "this died with them".

---

## Design

Everything is subtractive or prose. **No signature, no type, no constant changes. No new sweep.** `Bounded` keeps its single source. The one new symbol is a rename (§6).

### §1 — The retirement/survivor map

`TestFinTrailerRecordCarriesNoCapturedBytes` (`:827-898`):

| Lines | Check | Verdict |
|---|---|---|
| `:828-829` | `line := trailPaddedTrailer(0)`; `scan := trailScan(...)` | **Fixture kept.** `line` becomes unused — inline it (§2). |
| `:831-833` | fixture-state `Fatalf` (`trailSeen`) | **Kept**, re-argued (§2). |
| `:836-840` | in-cap precondition — needle offset vs. cap | **RETIRED** (AC1) |
| `:841-844` | in-cap precondition — `scan.Line` carries the needle | **RETIRED** (AC1) |
| `:846-847` | the build | **Kept.** |
| `:857-863` | per-row headroom | **Kept; argument RE-STATED** (§3) |
| `:867-869` | detail-quotes-the-line | **RETIRED** (AC1) |
| `:871-874` | `json.Marshal` + error check | **Kept** — the key scan consumes `encoded`. |
| `:875-878` | marshalled-record containment | **RETIRED** (AC1) |
| `:885-897` | flat forbidden-key scan | **Kept, unchanged** (AC2) |

`TestFinTrailerRecordReadsTheDecodedTrailer` (`:449-554`): row 1 deleted (§4), row 2 survives and is re-stated (§5), shell deleted (§6).

### §2 — AC1: the four retirements, and the two compile consequences

Delete `:836-840`, `:841-844`, `:867-869`, `:875-878`.

Two consequences that are **compile errors, not style**:

1. **The `bytes` import (`:75`) becomes unused.** `:875` is its only use in the file. Drop it.
2. **The `line` local becomes unused.** `:836` is its only remaining reader. Inline the fixture into the `trailScan` call.

`strings`, `encoding/json`, `time` and `testing` all keep live uses — verify rather than assume (`strings` survives at `:891` and in §5's row; `json` at `:871`, `:885-886`).

**The fixture is not repadded** (AC1). `trailPaddedTrailer(0)` stays, and the fixture-state precondition at `:831-833` stays with a re-stated reason: it is what puts the build on the **filled** arm of `finTrailerBuild`, whose Detail interpolates all four scalars and is the longer of the two shapes. Measuring headroom against the shorter arm would measure the easier case. This is the same argument `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` already makes for its own fixture at `:577-582`; cite it rather than re-derive it.

**The transported claim (AC1, AC5).** The retirement must carry, in the source, a by-symbol pointer to where the claim now holds, and must transport the **in-cap** claim, not the past-the-cap one:

> `TestFinGatherReturnsNoCapturedBytes` (`finding_run_gather_test.go`) sweeps the carrier as the gather's third return and asserts the same in-cap precondition **in code** — that the needle survived the 512-byte cap in the retained copy — so the plant it plants is one a leaking value would actually leak.

That is the **same** property, at the tier that now drops the line, and it is stronger there than here: this tier could only ever assert the absence of what its input cannot carry. Verified non-vacuous on `eea9f91`.

Doc surgery on `:787-826`:

- `:787-794` (the SUPERSEDED mark) → replaced by the transport statement above.
- `:796-798` (the headline) must narrow. "No trailer line in any form, capped or otherwise, in a field or quoted into the detail" claims the byte half that is leaving. What remains is the headroom rule and the structural key scan.
- `:800-816` ("# Why the plant lands INSIDE the cap") goes **with the plant** — but its argument is what makes the transport a transport of the *strong* claim, so fold that one sentence into the transport statement rather than dropping it silently. Never write prose implying the past-the-cap pad would have done.
- `:818-826` ("# Line is the only channel swept") goes with the plant. Its one durable half — a needle planted in the four decoded scalars would pin *against* AC2, since they cross verbatim by design — is already stated at `:130-137` and `:190-193` in this file and at `finding_run_gather_test.go:1083-1093`. Deleting it here loses no unique content; say where it lives.

### §3 — AC2: the two survivors, and the headroom's re-stated argument

**The flat forbidden-key scan (`:885-897`) is untouched**, including its flatness argument: `finTrailerRecord` is ten scalars, so a top-level key scan examines every key it has. It stays a prospective guard against a future field. No edit.

**The headroom assertion (`:857-863`) survives and `len(trailNeedle)` stays in use as its yardstick** — it is a *size*, not a plant, so §2's retirement does not reach it. But its argument is **re-stated**, not carried over. Both justifications name the retired checks:

- the comment (`:852`): "the containment checks below would then pass against a leaking implementation"
- the `t.Errorf` (`:860`): "the checks below would pass against it"

Replace with the argument its own type doc already makes (`:148-154`). Four beats, in this order:

1. **It is a type-level rule.** `finTrailerRecord`'s doc states it: every Detail leaves `len(trailNeedle)` bytes under `reachMaxCommandBytes`.
2. **The record travels whole.** `finRecordSeenTrailer` / `finRecordAbsentTrailer` build it with the shipped `finTrailerBuild`; `finRecordRun` embeds it whole as `Trailer`, pinned by `TestFinRecordEmbedsTrailerRecordWhole`; the artifact carries it from there.
3. **The sweeps one and two tiers up recurse into it, and a Detail that has eaten its own budget defeats them.** `TestFinRecordCarriesNoCapturedBytes`' marshal sweep "walks the embedded sub-records"; the artifact's Detail walk collects every Detail by JSON path, asserts it found **at least six** — the trailer's among them — and applies the *identical* `room < len(trailNeedle)` test per path. The room is what keeps those sweeps undefeatable by truncation.
4. **The failure lands here, naming the record.** This clause survives from `:855-856` and gets *stronger*: the sweeps it protects are now one and two tiers up, where the same failure would name a JSON path in an artifact and send the reader hunting.

Two things not to overclaim:

- **Register is prospective.** After #1320 the builder reaches no line, so **do not claim a needle reaches this Detail today**. Same register AC2 already grants the key scan: a guard against a FUTURE Detail edit or field.
- **Do not lean on the artifact's trailer-scan plant.** `finding_artifact_write_test.go:788` currently lists "the trailer scan's Line" among its plants — and #1326 retires exactly that. Beat 3 must rest on the **headroom walk** (a length check, untouched by #1326) and on the recursion, never on where a needle is planted.

`:860-862`'s actionable half — "Shorten the detail — the long-form argument belongs in a comment, which no cap applies to" — survives; it is mirrored verbatim at the artifact tier.

**AC2's last clause is a *cite* obligation and is already satisfied.** `:153-154` names `TestFinTrailerRecordCarriesNoCapturedBytes`; §6 renames the *other* test. **Leave `:153-154` untouched** — satisfying it does nothing for the argument, and editing it is churn.

**Fold in: the type doc carries the same dangle.** `:151` reads "turning the sweep below green against a record that did leak". After §2 there is no sweep below. This is the same defect as `:852`/`:860`, four lines above a line the developer is told not to touch — one sentence, same replacement argument. Not a new AC; a consequence of AC2 that AC2's own reasoning demands.

### §4 — AC3: the panic subtest is deleted

Delete `:464-506` whole, including its fixture preconditions (`:465-472`). `finTrailerAbsentScan` survives — it is still used at `:368` and `:703`.

Its assertions are **tautological** under the carrier: an absent scan gives `finTrailerSighting` a false `CarriesTrailer` and four zero scalars, so the record publishes zeroes whether the builder guards or not. Keeping it keeps a second test of nothing. Each claim, named by **symbol**, with the same-property check AC5 demands:

| Retired claim | Now held by | Same property? |
|---|---|---|
| the panic-on-unchecked-deref obligation | `TestFinSightingReachesNoScanType` | **Stronger.** It walks the carrier *type* for all three scan types, so a later field carrying one a level down fails — where the retired row asserted over one instance. Its failure message names the obligation explicitly. |
| the four zero-fields assertions | `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` | **Stronger.** The retired ones pass against a builder with no guard at all; the Fills test drives both arms off one carrier and one flipped bit, so neither an always-zero nor an always-copy builder survives. |
| `rec.State == trailAbsent` | `TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator`'s absent row | **Same**, under an executable coverage claim that goes red if the row is ever dropped. |
| the outcome | — | Not a survival candidate: the retiring subtest never asserts `rec.Outcome`. `TestFinTrailerRecordOutcomeIsConsumedAsHanded` owns it. |

### §5 — AC4: the surviving row, re-stated onto `finTrailerSighting`

The "four fields survive a cap that destroys terminal_reason" row (`:516-553`) **keeps its fixture (`trailPaddedTrailer(2000)`), its state precondition and its disagree-precondition on the capped line** — including the precondition's assert-don't-assume argument at `:519-521`, which stays true.

**What changes is the subject.** The dead claim is "the projection reads Trailer and not Line" (`:535-538`) and the `t.Errorf`'s "a record reading the capped Line could not have recovered it" (`:545-547`) — the builder reads neither. What the row still proves is that **`finTrailerSighting` reads the decode rather than the capped copy.**

Accordingly, drop the `finTrailerBuild` call (`:532-533`) and assert on the `finSighting` the helper returns:

```go
sighting := finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss)
// then the existing three-field table + the is_error check, against `sighting` rather than `rec`
```

Three reasons this is the right subject, all of which belong in the row's new doc:

- **AC5 requires it.** "The survivor is a property of `finTrailerSighting` rather than of the record its name points at" — asserting through a built record makes the code say otherwise.
- **It mirrors the sibling that pins the other half.** `TestFinGatherSightingScalarsComeFromTheFullLineDecode` asserts on `sighting.Subtype` etc. The two halves of one agreement obligation should be pinned in the same shape, or a reader cannot compare them.
- **No coverage is lost.** The builder's carry-through of the four is pinned by `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer`, and nothing about that carry depends on the pad — it is a field-for-field copy. The over-cap dimension was only ever about the *helper's* read.

**Why this row cannot be retired**, and the doc must say so:

- Not against `TestFinGatherSightingScalarsComeFromTheFullLineDecode`: that pins `finGatherReadings`' fill, a **different function**. The two are required to agree precisely because neither proves the other, and that obligation (`:294-301`) is otherwise held only by comment and by review. **This row is the only executable pin on the helper's half of it** — for the decode-vs-capped question specifically. (The Fills test's precondition at `:602-607` pins that the helper fills the four *at all*; it cannot tell which source they came from.)
- Not against `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer`: its fixture is `trailPaddedTrailer(0)`, wholly inside the cap, so it cannot distinguish a decode read from a capped-line read.

**Keep the expectations as literals** (`"error_max_turns"`, `"max_turns"`, `"end_turn"`). Do **not** convert to the sibling's `want := *scan.Trailer` form: one tier down that is literal-free because the gather runs its *own* scan over the same bytes, so `want` and the subject are two computations. Here the helper is handed the very `scan` the test built, so the derived form would compare a value to itself across a two-line assignment. The literals are what tie the assertion to the full-line decode.

**SHOULD (not required): add the pair precondition** — `sighting.State == trailSeen && sighting.CarriesTrailer` — mirroring `finding_run_gather_test.go:1662-1667`. Be honest in the comment about what it is: at this tier a false pair makes the four zero and the assertions go **red, not vacuous**, so this is a *diagnosis* guard (one `Fatalf` naming the pair beats four value mismatches), not a non-vacuity guard. Written as a vacuity guard it would be a false claim. Drop it if it reads as creep.

### §6 — AC5: the shell is settled

The shell has one child left. Settle it rather than leave it half-named — its doc claims two properties and one survives.

- **Delete the shell** `TestFinTrailerRecordReadsTheDecodedTrailer` and its doc (`:449-453`). A single-child `t.Run` is ceremony; the file's precedent is top-level tests (`TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer`, `TestFinTrailerRecordCarriesNoCapturedBytes`), and #1320 made the new test top-level "so that decision is a clean one".
- **Promote the survivor to top-level as `TestFinTrailerSightingScalarsComeFromTheFullLineDecode`.**
- **Keep it where the shell was** (between the Bound test and the Fills test). The file has no ordering discipline that a move would serve, and churn here costs review attention for nothing.

Name rationale, which belongs in the doc: it mirrors `TestFinGatherSightingScalarsComeFromTheFullLineDecode` with the prefix naming **which of the two functions under the agreement obligation it pins**. `grep ScalarsComeFromTheFullLineDecode` then returns both halves — the naming says "these two are required to agree" without prose. `finTrailerSighting` is a real symbol in this file, so `TestFinTrailerSighting…` parses as "about `finTrailerSighting`".

**This is the symbol a sibling file can cite** (AC5). #1326 needs it.

### §7 — Explicitly not touched

- **`finTrailerBuild`, `finTrailerRecord`, `finTrailerSighting`** — no signature, type or constant change. No new sweep. `Bounded` keeps its single source.
- **`:153-154`'s cite** and **`finding_run_record_test.go:141`'s cite** — both still name `TestFinTrailerRecordCarriesNoCapturedBytes`, which keeps its name. Not falsified; leave them.
- **The other three test functions** in this file — `CarriesTheBound…`, `Fills…`, `OutcomeIsConsumed…` — unchanged.
- **The other three realclaude files.** #1324 and #1326 own them.
- **Stale cross-file line cites.** #1320 grew this file by ~250 lines; fourteen cites from siblings now point a few hundred lines off. Separate mechanical concern, and none is fixable from inside this file.

---

## Concurrency model

None. Both edited functions are pure test code; nothing spawns a goroutine, takes a lock or holds package state. The file's function-not-var fixture rule (`trail_run_outcome_test.go:608-610`) is why `finTrailerSighting` and the three scan helpers are functions — `go test -race` runs this package's tests in parallel and a shared backing value would be reachable from all of them. This ticket adds no fixture, so the rule is inherited, not re-argued.

## Error handling

No production error paths. The failure modes that matter are **test-shaped**, and two are the point of the ticket:

- **A vacuous green.** The failure this ticket exists to remove: a check asserting the absence of what its input cannot carry. §2 removes four; §4 removes a subtest; §5 re-states a row rather than letting it claim a dead subject.
- **A silently disarmed sweep.** A Detail edit that eats the 512-byte budget. The surviving headroom assertion is the guard, and §3 keeps its `t.Errorf` naming the record, the byte counts and the remedy — never the Detail's contents.
- **A deleted regression guard.** The concrete risk in AC2: a developer reading "retire the needle checks" beside a headroom check whose only stated purpose is protecting them concludes it died with them, removing #1284's fix. §3's re-statement is the mitigation, and it is why the comment and the `t.Errorf` must *both* change.

Failure messages keep this file's discipline: name `len(scan.Line)`, byte counts, the four scalars and the keys sought; **never render `scan.Line`, `scan`, `scan.Trailer` or a whole `resultTrailer`**. `finding_run_gather_test.go:1141-1145` is the shipped model.

## Testing strategy

The deliverable *is* tests, so verification is that the surviving set still bites and the retired set is genuinely held elsewhere.

- **Build tags.** `make check` and `make build` never compile `e2e_realclaude` files, so a PR whose whole diff sits under that tag is a **vacuous green**. Run both explicitly:
  - `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...`
  - `go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...`
  - Expect a **PASS/SKIP split**, not all-PASS. Both were green on `eea9f91`.
- **The unused-import trap.** Dropping `bytes` is a compile error if missed and a `go vet` failure if left. It will surface on the first `go test` run — do not skip the run because "the change was deletions".
- **Targeted run** while iterating: `go test -race -tags e2e_realclaude -run '^TestFinTrailer' -v ./internal/e2e/realclaude/`, which is the header's own invocation (`:13`). Confirm `TestFinTrailerSightingScalarsComeFromTheFullLineDecode` appears and passes, and that `TestFinTrailerRecordReadsTheDecodedTrailer` no longer appears.
- **Non-vacuity of the re-stated row (§5).** The row must still go red against a helper reading the capped copy. Temporarily change `finTrailerSighting` to fill the four from a re-decode of `scan.Line` and confirm the row fails; revert. This is the one mutation worth running — everything else in the diff is subtractive, and a subtractive diff cannot be verified by a green run.
- **The surviving headroom assertion still bites.** Optional but cheap: lengthen the filled arm's Detail format past the budget and confirm the failure lands in `TestFinTrailerRecordCarriesNoCapturedBytes` naming the record; revert.
- **No `t.Skip`, no live claude, no credentials, no daemon, no turn.** Everything here is offline.

---

## Open questions

1. **The pair precondition in §5 is a SHOULD.** It mirrors the sibling's shape and improves diagnosis, but it is not a vacuity guard at this tier and AC4 says "keeps its fixture, its precondition" — singular, the existing ones. Either resolution is defensible; the spec asks only that the comment not misdescribe it.
2. **`finding_artifact_write_test.go:942-945` will hold three stale facts after this ticket**, all #1326's and none fixable from inside this file: the symbol name (renamed by §6 to `TestFinTrailerSightingScalarsComeFromTheFullLineDecode`), the line range `430-470` (stale since #1320), and "pad 200" against a fixture that is `trailPaddedTrailer(2000)` — the last already flagged as #1320's Open Question 4. Recorded here so #1326 inherits the complete list rather than rediscovering it.
3. **The helper's agreement with the gather stays held by comment and by review.** `finGatherReadings` execs `ps` and this file forbids exec, so no in-file pin is available. §5 makes the *decode-vs-capped* half executable on the helper's side; the fill-shape half remains prose. Unchanged from #1320's Open Question 1 — not this ticket's to close.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings, and the ticket's whole subject is one.** The untrusted→published boundary is verbatim model output: `trailScanResult.Line` (~415 of 512 retained bytes chosen by the model, marked OPERATOR-REVIEW-BEFORE-PASTE at `result_trailer_observation_test.go:100-107`) and `*resultTrailer` with its `PermissionDenials *[]json.RawMessage`. #1320 moved that boundary **one tier up** to the carrier fill; this ticket changes no code on either side of it and adds no field. The boundary stays checked — not asserted — by `TestFinSightingReachesNoScanType`, which walks the type rather than one instance. The live risk here was *removing* a guard, not moving one, which §3 is written against.
- **[Error messages, logs, telemetry] No findings; one guard preserved against a plausible deletion.** This is the category the ticket actually lives in. The published `Detail` may name the outcome, state, `BoundFrom`, `Bounded` and the four scalars, and may never quote `Line` or any derivative. §3 keeps #1284's per-row headroom assertion **and its yardstick** (`len(trailNeedle)`), which is the only thing standing between a lengthened Detail and a silently truncated leak two tiers up. The spec's failure-message discipline (§ Error handling) forbids rendering `scan.Line`, `scan`, `scan.Trailer` or a whole `resultTrailer` in any new or edited message — printing the contents of a value that just failed a leak check would write the leak into CI logs.
- **[Error messages, logs, telemetry] SHOULD FIX — the over-claim risk in §3's re-statement.** The replacement argument must stay in the **prospective** register. A re-statement reading "a needle that leaked into this Detail would be caught here" is false after #1320 — the builder reaches no line — and would be a green-looking claim about a channel that does not exist, the exact defect this ticket removes elsewhere. Equally, §3 beat 3 must not rest on `finding_artifact_write_test.go:788`'s trailer-scan plant, which #1326 retires; it rests on the headroom walk and the recursion, both of which survive #1326. Code-review must check the shipped wording against both.
- **[Tokens, secrets, credentials] No findings, and this is why the label is on the ticket.** The channel this family exists to keep shut is an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaching a publicly-pasted artifact via a verbatim argv or ps column. This ticket touches neither channel: argv reduction is `finRecordBuild`'s and is unchanged. `finTrailerRecord` stays **flat — ten scalars** — which is the sole reason the surviving top-level forbidden-key scan is valid; §7 forbids adding a field, so that scan keeps examining every key the record has.
- **[Subprocess / external command execution] Not applicable, and by symbol rather than by grep.** Neither edited function execs. Every symbol on their paths is pure over bytes: `trailScan`, `trailDetail`, `reachCapCommand`, `trailPaddedTrailer`, `finTrailerSighting`, `finTrailerSeenScan` / `AbsentScan` / `AbortedScan`. The exec-bearing helpers in this package — `pinScanArgv`, `pinReadState` (reached only via `finGatherReadings`), `probeProcessSnapshot`, `tdnScan`, `holdProbeFIFO`, `WithWorktreeAuthenticated` — are on no path this ticket touches. A grep for `exec.` reads clean here **for the wrong reason** (every route off the offline path runs through a helper that execs *inside*), so the enumeration is the check, not the grep. `finGatherReadings`' exec is also why Open Question 3 stays open.
- **[File operations] Not applicable.** No path is constructed, opened or written by anything this ticket touches. The artifact writer (`0o600` into a `t.TempDir()`) is unchanged and out of scope.
- **[Concurrency] No findings.** Both edited functions are pure and hold no package state; §5 adds no fixture, so the function-not-var rule is inherited rather than re-argued under `go test -race`.
- **[Cryptographic primitives] Not applicable.** No randomness, hashing, or comparison against a secret anywhere in the touched surface.
- **[Network & I/O] Not applicable.** No socket and no reader. The one cap in reach, `reachCapCommand`'s 512 bytes, is single-sourced and untouched — the ticket asserts *about* it and never changes it.
- **[Threat model alignment] Aligned; the deliberate window is named and narrowed.** The standing threat is "an artifact published unreviewed carries bytes an operator would have had to review". Between #1320 and this ticket that property held **structurally** at this tier rather than by measurement, with the marks saying so in the source. This ticket converts the marks into retirements and names, by symbol, where each claim now holds — `TestFinGatherReturnsNoCapturedBytes` for the in-cap needle claim (with its own in-cap precondition asserted in code) and `TestFinSightingReachesNoScanType` for the deref obligation. No coverage lapses; AC5's same-property obligation is what keeps that from being taken on the argument alone.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
