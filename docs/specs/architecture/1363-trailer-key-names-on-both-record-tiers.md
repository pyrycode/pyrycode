# #1363 — Carry the trailer's key names onto both record tiers, bounded, with the artifact's standing safety claim made true

**Ticket:** https://github.com/pyrycode/pyrycode/issues/1363
**Size:** S (confirmed, see § 0). **Split from** #1361. **Blocks** #1364 and #1362.
**Labels:** `security-sensitive` — the § 8 security-review pass ran and PASSED.

---

## 0. Size check — confirmed S, no split

Measured on `d14e8ad`. Every red line, with its measurement:

| Red line | Measured | Verdict |
|---|---|---|
| > 3 new files | **0 new files** — every edit lands in an existing file | pass |
| > ~600 lines total written | **~211 net insertions + ~50 in-place line rewrites**; with this package's measured +40% comment-density correction, **~370** | pass |
| > 5 new exported types/interfaces | **0 new types.** 2 constants, 1 helper func, 1 field name reused on 2 structs | pass |
| > 10 consumer call sites needing simultaneous update | **0.** See below | pass |
| > 5 acceptance criteria | exactly 5 | pass (at the line) |
| ≥ 10 error/reject branches | **0** — no state machine; one pre-existing `if`/return | pass |

**The edit fan-out check, run with codegraph rather than grep.** `codegraph_impact finSighting` and
`codegraph_impact finTrailerRecord` each return the defining file alone — no dependent chain. Verified
against the code:

- `finSighting{` literals: 3 total (2 in gather, 1 in evidence) — **all keyed**, so a new field breaks none.
- `finTrailerRecord{` literals: 1, keyed.
- `finTrailerBuild(` / `finTrailerSighting(` calls: 24 across 5 files — **no signature changes**, so none move.

Zero call sites break. The cascade in this ticket is a *cite* sweep (line numbers inside comments), not a
call-site cascade — no symbol's signature, name, or arity changes.

**Codegraph gap, noted rather than hidden.** `codegraph_context` over this task returned `cmd/pyry` entry
points and nothing from `internal/e2e/realclaude` — the whole package sits behind the `e2e_realclaude`
build tag and the index does not reach it. `codegraph_impact` on the two named symbols *did* resolve
(both to `:348` / `:157`), so the symbol table is partially populated. The § 1 reading list is therefore
built from direct reads plus the two `codegraph_impact` results, which is the documented fallback.

**Nearest-analogue measurement (the instrument, not an estimate).** #1357 was this exact work one tier
down — one struct field, one insertion point, the same three-form cite sweep — and shipped at `size:s`:

| commit | what | churn |
|---|---|---|
| `45ef577` | the field + the reader + its whole proof file | 463 insertions (**435 of them the new `trailer_key_names_test.go`**) |
| `bc6d4c8` | named-cite sweep | 25 lines / 7 files |
| `ebfce63` | bare + chained cite sweep | 9 lines / 5 files |

#1363's core is **strictly less** than `45ef577`: the 435-line proof file has no counterpart here — the
hostile-fixture proofs are #1364's and the artifact-wide sweeps are #1362's, both explicitly out of
scope. What #1363 adds beyond #1357 is one more insertion point and the safety-claim repair, together
~40 lines. Its sweep is comparable (23 named inbound cites vs #1357's 25).

**No split reduces the sweep; every candidate increases it.** Stated so it is not re-litigated:

- *Split by tier* (`finSighting` first, `finTrailerRecord` second) — `finTrailerBuild` computes nothing,
  so the upper tier has no source until the lower one carries it; and both files shift either way, so
  the sweep is paid **twice**.
- *Split the bounds off* — the honest wording of the safety claim (AC4) requires the bounds to exist.
- *Split the safety-claim repair off* — `run.md`'s standing sentence goes false the moment the field
  lands, so the intervening ticket ships a false claim in a pasteable artifact.
- *Split the sweep off* — a sweep ticket cannot exist before the shift it sweeps.

**Re-applying the red lines to PO's body rather than to my design:** the body names 6 files, 5 ACs, one
struct-shape change and a documented sweep. It trips nothing. PO's `size:s` is confirmed, not deferred to.

**The § 4 production-file self-check is vacuous here and is recorded as such.** Every file in this
package is `*_test.go` under the `e2e_realclaude` tag, so the "≥ 5 production source files" count is
**0**. Applying the spirit instead: **6 existing files touched, 0 created** — against #1357's 9 touched
at `size:s`. In family.

**File-overlap check (§ 1.5):** `git fetch origin --prune` then a diff of all 46 `origin/feature/<n>`
branches against `origin/main` over this spec's six target files. **No overlap.** No `blockedBy` set.

---

## 1. Files to read first

Turn-1 data load. Read these before writing anything.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/result_trailer_observation_test.go:120-137` | `trailScanResult.KeyNames` — the source field, its `omitempty` (this tier only), and the `#1358` pointer at `:134` you repair |
| `internal/e2e/realclaude/result_trailer_observation_test.go:192-213` | `trailScan`'s match return: `KeyNames: trailKeyNames(scanner.Bytes())`, and **`:199-205`** — the argument that the map decode always succeeds and always carries at least `type`. This is the reason AC1's collapse is unreachable |
| `internal/e2e/realclaude/result_trailer_observation_test.go:177-179` | the 64 KiB scanner buffer decision — the byte ceiling that constrains AC2's count bound |
| `internal/e2e/realclaude/result_trailer_observation_test.go:141-153` | `trailObservation` **embeds** `trailScanResult`, so `obs.KeyNames` is already in reach at the live fill site |
| `internal/e2e/realclaude/trailer_key_names_test.go:72-106` | `trailKeyNames`' three contract properties — no value crosses, top level only, sorted. AC1 compares the published names against **this function's own output** |
| `internal/e2e/realclaude/trailer_key_names_test.go:55-59`, `:300-310` | the two `#1358` pointers you repair (`:59` → #1363, `:304` → **#1362**), and `TestTrailKeyNamesCarryNoValues` — the shipped proof AC1 leans on |
| `internal/e2e/realclaude/finding_run_gather_test.go:339-358` | `finSighting`'s doc (the json-key and no-`omitempty` argument) and the struct — **insertion point A** |
| `internal/e2e/realclaude/finding_run_gather_test.go:435-455` | the live carrier fill, and `:445-448`'s "the two computations must agree" — **fill site 1** |
| `internal/e2e/realclaude/finding_run_gather_test.go:1916-1941` | `TestFinSightingReachesNoScanType`'s three forbidden types, and `finGatherForbiddenKeys` (`command`, `args`, `comm`, `argv`, `line`, `stderr`) — the key scan your json tag must clear |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:108-170` | the no-`omitempty` decision (`:114-122`), the Detail content + headroom rule (`:138-156`), and `finTrailerRecord` — **insertion point B**, which is at `~:142` inside the doc comment, not `:157` |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:241-272` | `finTrailerBuild` — the guard, the two Detail shapes, **fill site 3** |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:292-328` | `finTrailerSighting` — **fill site 2**, and the agreement obligation stated at `:296-303` |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:583-689` | `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` — **do not edit it**; note `:676`'s "reaches nothing else on the record", which you do repair |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:850-931` | `TestFinTrailerRecordCarriesNoCapturedBytes` — drives the **filled arm only** (`:857`), asserts headroom at `:897`, runs its own key scan at `:922` forbidding `line`/`trailer_line`/`result`/`raw` |
| `internal/e2e/realclaude/finding_artifact_write_test.go:87-107` | `finWriteSafetyClaim` and its doc — **AC4's target**; note the doc cites `finding_trailer_evidence_test.go:142`, which moves |
| `internal/e2e/realclaude/finding_artifact_write_test.go:1067-1088` | the contains-the-claim assertion and the two forbidden caveat strings AC4 must keep green |
| `internal/e2e/realclaude/finding_artifact_write_test.go:610-662` | `finWriteDeclaredPaths` / `finWriteObservedPaths` and the `< 35` floor at `:627` — leave the floor alone; the new path is picked up automatically |
| `internal/e2e/realclaude/finding_run_record_test.go:754-778` | `TestFinRecordEmbedsTrailerRecordWhole` — why `finRecordRun` needs no field of its own; `:771`'s "ten scalars" is one of the four prose repairs |
| `internal/e2e/realclaude/background_reach_probe_test.go:120-126`, `:945-950` | `reachTruncationMarker` (`"...(truncated by #1230 probe)"`, 29 bytes) and `reachCapCommand`'s truncate-and-mark idiom your helper mirrors |
| `internal/e2e/realclaude/trailer_admissibility_test.go:206` | `trailDetail` — the 512-byte Detail cap the headroom rule is measured against |

Not code, read anyway:

- The ticket body's **"Adding a field to these two structs is an inbound-cite sweep"** section — the
  three cite forms and the method that finally worked on #1357.

---

## 2. Context

`terminal_reason` is a pyry invention. On the headless `PYRY_USE_STREAMJSON=1` path claude's stdout
crosses unchanged, so a healthy run's trailer is claude's own `result` line and carries no
`terminal_reason` key at all. After the fixed decode through `resultTrailer`, an **absent**
`terminal_reason` and one emitted as `""` are the same value. The presence or absence of the *key* is
therefore the only thing separating "pyry wrote this trailer, so the watchdog fired" from "claude wrote
it, so the run was healthy" — and a published probe record that omits that discriminator cannot be
audited from the filed artifact alone.

`trailScan` already reads the names (`trailScanResult.KeyNames`). This ticket carries them the rest of
the way — onto both record tiers, bounded — and repairs the artifact's standing safety sentence, which
goes false the moment a field of claude-authored strings lands in a record that claims every field is
"a string this rig authored".

Out of scope and deliberately so: the hostile-fixture proofs that the bounds bite (#1364), the
artifact-wide containment sweeps (#1362), and the backtick-fence exposure (`run.md` embeds the record in
a ```` ```json ```` fence and `MarshalIndent` does not escape backticks) — that door is already open
through `stop_reason`, which crosses uncapped by design, and closing it for key names alone would defend
one door while the older one stays open.

---

## 3. Design

### 3.1 The chain, and the three edits on it

```
trailScan → trailScanResult.KeyNames
              ├─ finTrailerSighting  (fixture side) ─┐
              └─ finGatherReadings   (live side)   ─┴→ finSighting.KeyNames
                                                        → finTrailerBuild → finTrailerRecord.KeyNames
                                                          → finRecordRun.Trailer (embedded whole)
                                                            → run.json / run.md
```

`finRecordRun` gains **no field of its own** — it embeds the trailer sub-record whole and
`TestFinRecordEmbedsTrailerRecordWhole` pins that carriage.

### 3.2 The two bounds, as exported constants

Placed in `finding_run_gather_test.go` immediately after `finSighting`, because that is the type whose
fill applies them and both fill sites produce it.

```go
const finTrailerMaxKeyNames = 32      // how many names the carrier publishes
const finTrailerMaxKeyNameBytes = 64  // how many bytes each published name may hold
```

Why these values, and why they must differ:

- **The count ceiling is the scanner's 65535-byte line, not a name count.** `trailScan` reads with a
  `bufio.Scanner` whose buffer is deliberately not raised past the 64 KiB default; a line at or past the
  limit **aborts the scan** rather than truncating, so `KeyNames` comes back nil and `CarriesTrailer` is
  false. Re-measured on `d14e8ad`: 65535 accepted, 65536 rejected; **4680** names fit at ordinary
  `"k000000":"v"` shapes, 8710 at maximally compact ones. A bound near or above 4680 is **unprovable**,
  because #1364's fixture must carry `bound + 1` names *and still scan*. 32 is two orders of magnitude
  below that floor, so a 33-name fixture is ~400 bytes and stays readable rather than golfed.
- **32 is ~3× the real shape.** The trailer carries eleven top-level keys
  (`trailExpectedKeyNames()`), all CLI-envelope-authored. 64 is ~4× the longest of them
  (`terminal_reason`, 15 bytes).
- **The two values are deliberately different.** A fill or a proof that applies the wrong constant is
  then detectable; two 64s would make a transposition invisible. State that reason at the constants.
- **Worst-case published size** is `32 × (64 + 29)` ≈ 3 KiB before JSON quoting — bounded, and the
  artifact is meant to be pasted.

### 3.3 The bounding helper — one copy, not two

The ticket's binding constraint: *"The two fill sites do not each carry their own copy of the bounding
arithmetic; the code already states these two fills must agree, and two hand-written copies of a cap is
how they stop agreeing."*

```go
// finBoundKeyNames returns names bounded for publication: at most
// finTrailerMaxKeyNames entries, each at most finTrailerMaxKeyNameBytes bytes.
func finBoundKeyNames(names []string) []string
```

Contract — five clauses, each load-bearing:

1. **Returns nil for a nil or empty input.** Never `[]string{}`. A not-seen record must render `null`,
   which is the shape AC1's distinction rests on.
2. **Keeps at most `finTrailerMaxKeyNames` entries, in the input's order** — `trailKeyNames` already
   sorted them, so the kept set is the alphabetic prefix.
3. **Bounds each entry INDIVIDUALLY, never as a joined string.** This is AC2's core: a joined-string cap
   lets a leak in a late name be truncated away and turns a containment sweep green over a record that
   leaked — the defect #1284 shipped and had to fix.
4. **An over-long entry is truncated and marked, never dropped**: `name[:finTrailerMaxKeyNameBytes] +
   reachTruncationMarker`, mirroring `reachCapCommand`. Dropping removes evidence silently; truncating
   announces itself. Cross-prefix reuse of the `reach` cap vocabulary is already established here —
   `trailScan` calls `reachCapCommand` and `trailDetail` caps at `reachMaxCommandBytes`.
5. **Allocates its own backing array on EVERY path; never returns the input slice and never mutates it.**
   Explicitly including the case where the input is already under both bounds — **do not add a
   "nothing to do, return `names`" fast path.** It looks free and is not: `finTrailerBuild` copies the
   field by plain slice assignment (§ 3.5, fill site 3), so a pass-through makes the published
   `finTrailerRecord.KeyNames` alias `trailScanResult.KeyNames` itself, and the shipped
   `dropped := seen` struct copy in `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer`
   (`:619`) puts two carriers on one backing array. `go test -race` runs this package's tests in
   parallel; the standing reason is `trail_run_outcome_test.go:608-610`. The builder's plain assignment
   is safe **only because this clause holds**, which is why the clause is stated at the producer rather
   than at the consumer.

Do **not** introduce a `finSightingFrom` constructor. `finGatherReadings`' fill comment
(`:440-443`) rejects one by name: funnelling the whole composition through one function is what makes it
checkable in one place. `finBoundKeyNames` is the one new shared symbol, and it is narrow on purpose.

### 3.4 The field, on both structs

```go
KeyNames []string `json:"trailer_keys"`
```

- **The name** mirrors `trailScanResult.KeyNames`.
- **The json key** is `trailer_keys` — the same one `trailScanResult` chose. It faces **two independent
  key scans, not one**, because the field lands on two structs: `finGatherForbiddenKeys` forbids
  `command`/`args`/`comm`/`argv`/`line`/`stderr`, and `TestFinTrailerRecordCarriesNoCapturedBytes`'s own
  scan forbids `line`/`trailer_line`/`result`/`raw`. `trailer_keys` clears both; `trailer_line` is the
  near miss a key chosen against the first list alone would still trip.
- **No `omitempty`, on either struct.** Both types carry a blanket rule and it applies unchanged.
- **Position:** on `finTrailerRecord`, its own group after `StopReason` and before `Detail`, separated by
  a blank line. On `finSighting`, likewise after `StopReason`. It is the fifth trailer *field* but **not
  a fifth decoded scalar** — it comes from a different reader (`trailKeyNames` over the full line) than
  the four (`resultTrailer`'s fixed decode). Keeping it in its own group is what leaves the file's
  ~11 "the four decoded scalars" prose sites true.

**State the `omitempty` decision at the field** (AC1). The prose must say:

- the blanket no-`omitempty` rule applies, **and**
- the specific collapse that rule defends against is **unreachable** here anyway: `trailScan`'s match
  return is past `tr.Type == "result"`, reachable only from a line that already decoded as a JSON
  object, so the map decode always succeeds and always carries at least `type`
  (`result_trailer_observation_test.go:199-205`). A **seen** trailer therefore can never produce an empty
  name set; "no names" is reachable only from the not-seen arm.
- the three rendered shapes measured on `d14e8ad`: nil → `{"trailer_keys":null}`, empty →
  `{"trailer_keys":[]}`, filled → `{"trailer_keys":["type"]}`. `trailScanResult`'s own tier uses
  `omitempty` and is **not** a precedent here.

**Also state the truncation semantics at the field**, because a reader must not be misled:

- A list at exactly `finTrailerMaxKeyNames` entries may be an alphabetic **prefix**, in which case a name
  sorting late — `terminal_reason` among them — could be absent from a line that carried it. No producer
  emits a `result` line with more than eleven top-level keys (the keys are the claude CLI's envelope, not
  the model's text), so this is unreachable from a real run and the bound is a cap on the artifact rather
  than a live defence. Say so; do not special-case any name to survive the cut.
- Two distinct names sharing a `finTrailerMaxKeyNameBytes`-byte prefix collapse to the same truncated
  string. Both then carry `reachTruncationMarker`, so the duplication is visibly an artefact.

### 3.5 The three fills

All three go **behind the existing `CarriesTrailer` guard**, beside the four scalars:

| site | file | edit |
|---|---|---|
| 1 — fixture | `finding_trailer_evidence_test.go` `finTrailerSighting` | `sighting.KeyNames = finBoundKeyNames(scan.KeyNames)` inside the `if` |
| 2 — live | `finding_run_gather_test.go` `finGatherReadings` | `sighting.KeyNames = finBoundKeyNames(obs.KeyNames)` inside the `if`. `trailObservation` embeds `trailScanResult`, so `obs.KeyNames` needs no new plumbing |
| 3 — builder | `finding_trailer_evidence_test.go` `finTrailerBuild` | `rec.KeyNames = sighting.KeyNames` inside the `if`. **A plain copy — the builder computes nothing, and bounding here as well would be the second copy of the arithmetic 3.3 forbids** |

**Both fill sites must call `finBoundKeyNames`, and nothing structural enforces it.** This is the
ticket's own stated trap, and it is asymmetric in the dangerous direction: every offline test drives the
**fixture** side, so a developer who bounds site 1 and forgets site 2 ships an **unbounded field on
every live probe run with nothing red**. No in-file pin is available — `finGatherReadings` reaches `ps`
through `pinScanArgv`/`pinReadState` and `finding_trailer_evidence_test.go` forbids exec by its own
header — so the obligation is held by comment and by review, in exactly the register the file already
uses for the `CarriesTrailer` pair (`finding_trailer_evidence_test.go:296-303`,
`finding_run_gather_test.go:445-448`). Two obligations follow:

- **Developer:** extend the existing "deliberately identical … the two computations must agree" comment
  at `finding_run_gather_test.go:445-448` and its mirror at `finding_trailer_evidence_test.go:296-303` to
  cover the bounding call, so a later editor sees the agreement obligation on the line they are editing.
- **Code review:** discharge it field-for-field — read both fills side by side, confirm both route through
  `finBoundKeyNames`, and report that check as work done rather than as a missing pin.

**Behind the guard, and that is a decision.** On a real non-seen scan `obs.KeyNames` is already nil, so
an unconditional fill would give the same answer — but under the carrier the field is *separately
settable*, so `CarriesTrailer: false` beside a filled `KeyNames` is a reachable input, and a builder
copying it through would publish names beside a Detail saying the trailer fields hold their zeros. That
is exactly the "copies through" shape `finTrailerBuild`'s doc forbids for the four. Same doctrine, same
arm.

**Consequently the false arm's Detail must name the drop.** Its wording is load-bearing —
`finTrailerBuild`'s doc calls it "THE ARGUMENT IS IN THE ARM'S OWN DETAIL". Change exactly one clause:

> `…so the four trailer fields hold their zero values…`
> → `…so the four trailer fields and the key names hold their zero values…`

Do **not** renumber to "five": `# What the four trailer fields are worth` at `:124` is about the four
*decoded scalars* and stays true, and one phrase meaning two different counts in one file is the drift
this file's discipline exists to prevent.

The **seen** arm's Detail is **unchanged** — AC3 forbids it interpolating any key name or any count
derived from the line, and it already reads correctly about the four it names.

### 3.6 The safety claim (AC4)

`finWriteSafetyClaim` is a constant precisely so the writer and the test cannot drift. It currently
claims every field is *"a count, an integer, an enumerated verdict, a decoded trailer scalar, or a string
this rig authored"* — five categories, none of which a **claude-authored key name** belongs to. Nothing
catches this: the shipped test asserts only that the note *contains* the constant, never that it is true,
so the false version would ship green.

Ship this text:

```go
const finWriteSafetyClaim = "This record carries no verbatim model output and no verbatim argv. " +
	"Every field it holds is a count, an integer, an enumerated verdict, a decoded trailer scalar, " +
	"or a string this rig authored — with one field named here rather than left to be discovered: " +
	"the trailer's top-level key NAMES cross verbatim from claude's own output line. They are key " +
	"names and never values, bounded in count and in length, and no byte of any field's contents " +
	"can reach them."
```

Three properties any reword must keep, so review can check the property rather than the prose:

1. **It contains neither `operator-review-before-paste` nor `OPERATOR-REVIEW`** — the negative half at
   `finding_artifact_write_test.go:1081-1087` stays green. The record still has no field that obligation
   attaches to, and the honest description of the names is **safe**, not reviewable. (If the honest
   wording had to be "review this before pasting", the right answer would have been not to carry the
   names at all.)
2. **It does not claim the names are rig-authored.** It says they cross verbatim from claude's output —
   which is what makes the sentence true rather than merely unfalsified.
3. **It states both halves of why they are safe**: *bounded* (this ticket's constants) and *value-free*
   (proved at the reader tier by the shipped `TestTrailKeyNamesCarryNoValues`, which plants a distinct
   needle in every string-valued position of a trailer line and asserts none reaches
   `trailScanResult.KeyNames`).

Update the constant's own doc comment (`:87-96`) to match — it currently ends "so there is no field left
to mark for review", which stays true, and cites `finding_trailer_evidence_test.go:142` as "ten scalars
with no Line", which is one of the four prose repairs **and** a cite that moves.

### 3.7 What must NOT change

- `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer` — untouched, and **not renamed**.
  It stays correct about the four decoded scalars; the new test in § 5 owns the fifth field's two arms.
  Renaming it would cascade into its four inbound cites for nothing.
- `TestFinTrailerRecordCarriesNoCapturedBytes`'s existing row — untouched. It drives the **filled arm
  only** (`:857`), whose Detail is unchanged, so the per-row headroom assertion at `:897` passes with no
  edit. AC3 satisfied by construction.
- `TestFinSightingReachesNoScanType` — untouched. It walks `finSighting` for `trailObservation`,
  `trailScanResult` and `resultTrailer`; a `[]string` reaches none of them.
- `finWriteDeclaredPaths` / `finWriteObservedPaths` and the `< 35` floor at `:627` — untouched. The walk
  derives paths from `reflect.TypeOf(finRecordRun{})`, so the nested field is declared automatically, and
  the observer records a path for any key present whatever its value. A `[]string` adds `trailer.<key>`
  on both sides and nothing under it (41 → 42, still ≥ 35).
- The commit-anchored census at `finding_artifact_write_test.go:621-624` ("41 at `4bc5f5b` … 10 under
  trailer") — a historical snapshot anchored to a named commit; correct as written, leave it.

All of the above except the Detail reword were verified on `d14e8ad` under a `go test -overlay` mutant
that added the field to both carriers and filled it at all three sites: the whole `^TestFin|^TestTrail`
selector passed.

---

## 4. Concurrency model

None. Every symbol here is pure over its inputs — no goroutines, no context, no clock, no exec, no
filesystem. The one concurrency-shaped obligation is § 3.3 clause 5: `finBoundKeyNames` allocates its own
backing array, because `go test -race` runs this package's tests in parallel and a shared backing array
lets one row's mutation reach another's.

## 5. Error handling

No new failure modes. `finBoundKeyNames` cannot fail — it has no error return, matching the family's
pure-builder contract (`trailScan`, `trailGate`, `finTrailerBuild`, `pinReadState` all return data and
never fail a test, because an instrument failure observed mid-turn is a datum to publish, not a reason to
abort the turn). `trailKeyNames`' one failure arm (a line that does not decode as a JSON object) is
already unreachable from `trailScan`'s match return and is not re-handled here.

---

## 6. Testing strategy

### 6.1 The new test (AC1)

`TestFinRecordPublishesTheTrailerKeyNamesTheReaderRead`, **appended at the end of
`finding_run_record_test.go`**. That file's highest inbound cite is `:1036` and the file is 1057 lines,
so appending shifts **zero** cites — and the test needs that file's `finRecordBuild` / `finRecordInputs`
/ `finRecordProofAttribution` fixtures, exactly as `TestFinRecordEmbedsTrailerRecordWhole` does.

Scenarios, as behaviour rather than code:

- **Fixture, from the shipped scanner.** `trailScan([]byte(trailPaddedTrailer(0) + "\n"))`, so the row can
  never assert against a state `trailScan` would not return for those bytes.
- **Precondition, fatal and first.** Fail fast unless the scan is `trailSeen` with a non-nil `Trailer`
  **and** a non-empty `KeyNames`. Without it, an equality against a nil expectation passes on a builder
  that publishes nothing.
- **Precondition, the one AC1 names explicitly.** Fatal unless the fixture's name set sits under **both**
  bounds — `len(scan.KeyNames) <= finTrailerMaxKeyNames` and every name
  `<= finTrailerMaxKeyNameBytes`. This is what makes "the reader's names" and "the published names" the
  same list rather than a prefix of one. (Measured: the fixture carries 11 names, longest 15 bytes.)
- **Precondition, non-vacuity of the discriminator.** Fatal unless the expected list contains
  `terminal_reason` — the key the whole field exists to carry. Say so in the message.
- **End to end through the shipped builders, nothing hand-assembled.** `finTrailerSighting(scan, …)` →
  `finTrailerBuild(outcome, sighting)` → `finRecordBuild(finRecordInputs{Trailer: …})`. Assert
  `rec.Trailer.KeyNames` equals `trailKeyNames([]byte(trailPaddedTrailer(0)))` — **the reader's own
  output for that line**, not a hand-written list. A hand-written expectation would pin the fixture's key
  set instead of pinning the carriage.
- **The not-seen arm renders differently — the distinction the family exists to keep.** Build a second
  record the same way from `finTrailerAbsentScan()`. Marshal both trailer sub-records, decode each into
  `map[string]json.RawMessage`, and assert:
  - the `trailer_keys` key is **present on both** — an `omitempty` variant would drop it and this is what
    catches that;
  - its raw bytes are exactly `null` on the not-seen record;
  - its raw bytes decode to a **non-empty array** on the seen one.

  Assert on the *marshalled* bytes rather than the Go value, so the claim is about the published artifact
  rather than about an in-memory struct. Say in the failure message that an `omitempty`-shaped collapse
  would make these two identical.

### 6.2 What is deliberately not tested here

- That the bounds **bite** — the hostile fixtures carrying `finTrailerMaxKeyNames + 1` names and a name
  of `finTrailerMaxKeyNameBytes + 1` bytes are **#1364's**, along with the headroom row that makes AC3's
  prohibition red rather than merely stated. Do not write them.
- The artifact-wide containment sweep over the written files — **#1362's**, with its own narrower plant
  list restricted to the positions the pipeline reduces.

### 6.3 The verification gate is tag-scoped — `make check` is not it

Every file here carries the `e2e_realclaude` build tag, so `make check`'s untagged `go vet ./...` /
`go test -race ./...` / `staticcheck ./...` **never compile this diff**. A green standard gate would say
nothing about it (observed on PR #1359). Run:

```bash
go test -race -tags e2e_realclaude -run '^TestFin|^TestTrail' ./internal/e2e/realclaude/
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
```

This is **not** a `needs-real-claude` ticket: these tests are offline, carry no env gate and no
`t.Skip`, and run rather than skip without credentials — re-confirmed on `d14e8ad`, that selector reports
**70 PASS and 0 SKIP** with no Anthropic credentials in the environment. Run `make check` too, but do not
read its green as evidence about this diff.

---

## 7. The cite sweep (AC5) — scoped by measured line-count delta

This is the single largest risk. #1357 was this work one tier down and **failed code review twice on the
cite sweep alone**. Follow the recorded method rather than re-deriving it.

### 7.1 Bound the sweep first: only three files have non-zero delta

| file | delta | inbound named cites | sweep needed? |
|---|---|---|---|
| `finding_run_gather_test.go` | **+** (field + prose + 2 consts + helper) | 13, at `105 249 256 298 348 348 444 444 478 1916×4` | **yes** — 9 at or past `:348` |
| `finding_trailer_evidence_test.go` | **+** (field + prose) | 13, at `38 114 142 142 247 247 318 339 476 648 686 688 850` | **yes** — **11 at or past `~:142`** |
| `finding_artifact_write_test.go` | **+** (claim + its doc) | 3, at `:141-150`, `:183-184`, `:244-259` | **yes** — all 3 are below the edit |
| `finding_run_record_test.go` | **+ at EOF only** | 25, highest `:1036`, file is 1057 lines | **no** — append after `:1057`; keep the `:771` repair line-count-neutral |
| `result_trailer_observation_test.go` | **0** (one-token `#1358`→`#1363`) | 40 inbound, 27 at or past `:141` | **no**, *provided* the repair stays neutral |
| `trailer_key_names_test.go` | **0** (two one-token repairs) | 1 inbound (`:87`) | **no**, same proviso |

A file with delta 0 cannot have moved its own self-refs, so its bare refs need no re-check. That drops
three of six files out of the sweep before a line is read — **this is why every repair in § 7.4 is
line-count-neutral, and it is not a style preference.**

**Correction to the ticket body worth carrying:** the body puts `finding_trailer_evidence_test.go`'s
insertion point at `:157` (the struct) and counts 9 at-or-past. AC1's "state that at the field" and
AC2's bound documentation both add prose **inside the doc comment above it**, moving the real insertion
point to `~:142` and the count to **11** — the two extra being `finding_artifact_write_test.go:93` and
`:674`, which cite `:142` directly. Both must go through the line map rather than being hand-fixed for
the word "ten" alone.

### 7.2 All three cite forms — a filename grep finds only the first

1. **Filename-anchored** — `finding_trailer_evidence_test.go:157`. The 26 counted above.
2. **Bare and chained** — `(:NNN)` and `(:NNN there)`, inheriting the filename from the last `.go` file
   named earlier in the same comment block. ~128 candidates across 19 files in this package. **Scan
   statefully**, carrying the last-named file forward. They are **not reliably self-referential**:
   `finding_artifact_write_test.go:45` carries `(:1088)` and `(:191)` in a block whose last-named file is
   `process_pin_liveness_test.go`, and both resolve into *that* file. Local convention discriminates in
   `finding_run_gather_test.go`: every cross-file bare cite is marked **"there"** and every self-ref is
   not. Also: `line[:512]` is Go slice syntax quoted in prose, never a cite.
3. **Symbol-anchored** — `<TypeName>:NNN`. Neither a filename grep nor a bare-ref scan finds it. Census
   over the package:
   ```bash
   grep -rhoE '[A-Za-z_][A-Za-z0-9_]*:[0-9]+' internal/e2e/realclaude/ --include='*.go' \
     | grep -v '^go:' | sed 's/:[0-9]*$//' | sort | uniq -c | sort -rn
   ```
   The one that **moves** is **`finTrailerRecord:147`** at `finding_run_gather_test.go:342` — it points
   into the doc comment above the struct, exactly where this ticket adds prose. (`trailObservation:133-135`
   points into a delta-0 file and does not move.)

### 7.3 The method, not to be re-derived

- **Rewrite off a line map, not by hand.** Diff `git show origin/main:<file>` against the working copy
  with `difflib.SequenceMatcher` to build old→new, then substitute. Content-preserving for every cite at
  once, including ones whose target is an approximation that must stay exactly as approximate.
- **Anchor on `origin/main`, not on the map.** For each cite compare its value on `main` against its value
  now: `main == now` and `map(main) != main` ⇒ unswept; `main != now` and `now == map(main)` ⇒ already
  swept. Re-running a naive map over a partly-swept tree **double-shifts** what the first pass fixed.
- **Classify before fixing.** Only what this diff broke is in scope. Pre-existing staleness goes in the PR
  body, not in the diff — attribute by gap size (a +2 shift cannot open a 124-line gap).
- **Verify each bare hit by its target's text**, by locating the named symbol
  (`grep -n '^func <sym>' *.go`), not by trusting the carry heuristic. On #1357 that heuristic flagged 17
  and **6 were correct**.
- **Keep every late fix line-count-neutral.** On #1357 a NIT that reworded 4 comment lines into 6 turned
  all 24 already-correct cites into off-by-two. If an edit genuinely cannot be neutral, make it **first**,
  then re-derive the map.
- **Only `internal/` is in scope.** `docs/specs/**` carries 75 further named cites into
  `finding_trailer_evidence_test.go` alone; they are commit-anchored historical build artefacts. #1357's
  two sweep commits (`bc6d4c8`, `ebfce63`) touched `internal/` exclusively. **Do not sweep `docs/`.**

### 7.4 The seven prose repairs, all line-count-neutral

**The four stale "ten scalars" sites** (the record gains a fifth field):

| site | current | note |
|---|---|---|
| `finding_run_record_test.go:771` | "DeepEqual over ten scalars" | count only |
| `finding_trailer_evidence_test.go:915` | "finTrailerRecord is FLAT — ten scalars" | **preserve the surrounding argument** — that a top-level key scan examines every key the record has stays *true* of a `[]string`, since the scan is over top-level keys and a slice adds no nested ones. Fix the count; do not rewrite the argument |
| `finding_artifact_write_test.go:93` | "is ten scalars with no Line" | the load-bearing half is **"no Line"**; only the count moves. **Also cites `finding_trailer_evidence_test.go:142`** — route through the line map |
| `finding_artifact_write_test.go:674` | "is ten scalars with no Line" | same, same cite |

**The three `#1358` pointers.** #1358 was split and closed; all three name a ticket that no longer exists.

| site | current | repair |
|---|---|---|
| `result_trailer_observation_test.go:134` | "bounding them is #1358's" | → **#1363** (discharged here) |
| `trailer_key_names_test.go:59` | "The per-name cap belongs at the tier that publishes: #1358." | → **#1363** (discharged here) |
| `trailer_key_names_test.go:304` | "The artifact-wide sweep … is #1358's" | → **#1362**, which owns that work. Do **not** leave it or disclaim it: #1362 has no AC for the pointer, so disclaiming strands a comment naming a closed ticket with nobody assigned. It is one token in a file this ticket already edits |

**The two "what the guard drops" sites**, which go incomplete once `KeyNames` joins the guarded group:

| site | current | repair |
|---|---|---|
| `finding_trailer_evidence_test.go:230` | quotes `"the four trailer fields hold their zero values"` | update the quote to match § 3.5's new Detail |
| `finding_trailer_evidence_test.go:595` | same quote | same |
| `finding_trailer_evidence_test.go:676` | "the drop is the four scalars' and reaches nothing else on the record" | name the key names too |

These three sit in a file that already has positive delta, so they may rewrap **within their existing
line count** — they must not add a line.

### 7.5 One pre-existing stale cite that is NOT this ticket's to fix

`finding_run_gather_test.go:342` cites `finTrailerRecord:147` as giving the reason a Duration's unit
belongs in its json key; `finding_trailer_evidence_test.go:147` is the Detail headroom rule and says
nothing of the sort. **Remap it if the line map moves it** (it does — see § 7.2), but do **not** chase the
semantic drift. That is separate debt; report it in the PR body.

---

## 8. Security review

**Verdict:** PASS (after two revisions — see [Concurrency] and [Trust boundaries] below; both are
already folded into §§ 3.3 and 3.5 above, and the checklist was re-run from the top afterwards.)

**The standing threat this package exists under, stated once:** a probe record is destined for a
**public GitHub issue**. Every field is a publication decision, and the artifact's own standing sentence
tells an operator it is safe to paste unreviewed. There is no `docs/threat-model.md` in this repo, and
`docs/protocol-mobile.md` § Security model is relay-scoped and not engaged by this diff.

**Who authors the key names — the fact several findings turn on.** The **claude CLI's serialiser**, not
the model. `type`, `subtype`, `is_error`, `result`, `session_id`, `duration_ms`, `num_turns`,
`total_cost_usd`, `usage`, `stop_reason`, `terminal_reason` are envelope fields. A prompt-injected model
influences `result`'s **value** and cannot add a top-level key. That is why AC4's clause says
*claude-authored* rather than *model-authored*, and why the record's opening claim "carries no verbatim
model output" survives this ticket.

**Findings:**

- **[1. Trust boundaries] SHOULD FIX — addressed in § 3.5.** The design has four boundaries and each is a
  single named function: `trailScan` (64 KiB scanner limit; a longer line **aborts** and yields nil
  `Trailer` *and* nil `KeyNames`, `result_trailer_observation_test.go:177-179`, `:216-225`) →
  `trailKeyNames` (returns `[]string`, discards the `map[string]json.RawMessage`, so **no value can
  cross** — structural, not a discipline) → `finBoundKeyNames` (this ticket's new enforcement point) →
  `json.MarshalIndent`. The hole: the third boundary has **two** entry points and nothing structural
  forces both through it, and the asymmetry runs the wrong way — every offline test drives the *fixture*
  side, so bounding site 1 and forgetting site 2 ships an unbounded field on every **live** run with
  nothing red. No in-file pin is possible (`finGatherReadings` reaches `ps`; the evidence file forbids
  exec by its own header). Spec revised to state the obligation at both fills and to name code-review as
  the discharge point, in the register the file already uses for the `CarriesTrailer` pair.
- **[2. Tokens, secrets, credentials] No findings.** No token is generated, stored, compared or logged.
  A key name cannot itself be a secret: it comes from the CLI envelope, not from a value. The package's
  one live secret channel is unrelated and untouched — `reachProc.Command` is verbatim argv and a `ps`
  column is how an operator's `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` reaches a public issue,
  which is why callers convert `pinScan.Matches` to `[]int` at their own call site
  (`finding_run_gather_test.go:405-409`). This diff adds no path to it.
- **[3. File operations] No findings.** No key name reaches a filesystem path at any depth. The writer's
  two filenames are the constants `run.json` / `run.md`, written at `0o600` into a `t.TempDir()`; no
  concatenation of scanned input into a path, so no traversal and no TOCTOU. Checked adversarially:
  `finWriteObservedPaths` does build dotted *paths* out of JSON keys, but they are in-memory set members
  compared against `finWriteDeclaredPaths`, never opened — and the names are array **elements**, so they
  contribute no path segment at all (`trailer.trailer_keys` and nothing under it).
- **[4. Subprocess / external command execution] No findings.** Nothing in this diff execs.
  `finding_trailer_evidence_test.go` forbids exec by its own file header; `finBoundKeyNames` is pure over
  a `[]string`. The live fill sits inside `finGatherReadings`, which does reach `ps` through
  `pinScanArgv` — that call site is untouched, and no scanned byte becomes an argument to it.
- **[5. Cryptographic primitives] Not applicable, with the reason.** No randomness, no hashing, no key
  material, and no comparison of an attacker-controlled value against a secret — so no
  `crypto/subtle` obligation arises. The only operations on untrusted input are `sort.Strings` (upstream,
  already shipped) and a length test.
- **[6. Network & I/O] No findings; the input cap is the load-bearing control and is documented.** No
  socket, no server, no TLS, so the timeout and slow-loris questions do not arise. The size limit the
  category demands exists in both directions and both numbers are in the spec: **in**, 65535 bytes per
  line, enforced by `bufio.Scanner`'s deliberately-unraised default, above which the scan **aborts**
  rather than truncating; **out**, `finTrailerMaxKeyNames × (finTrailerMaxKeyNameBytes +
  len(reachTruncationMarker))` = `32 × 93` ≈ 3 KiB. The pre-#1363 record published none of this, so
  there is no regression — but an unbounded field would have been a new one, which is exactly why the
  bounds cannot defer to #1364.
- **[7. Error messages, logs, telemetry] No findings.** `finBoundKeyNames` has no error return and
  formats nothing, so it cannot reintroduce the leak `trailKeyNames` already closes by **dropping** its
  json error rather than wrapping it — `json.SyntaxError` carries a byte offset into its own input and
  `UnmarshalTypeError` names the offending value, both chosen by the line
  (`trailer_key_names_test.go:88-99`). AC3 keeps the same door shut at the `Detail`: no key name, no
  count derived from the line, and § 3.5's false-arm reword adds a **constant clause with no verb**.
  The new test's failure messages print fixture names only; it never handles a live line.
- **[8. Concurrency] MUST FIX — fixed inline in § 3.3 clause 5, checklist re-run.** The original clause
  said "allocates its own backing array", which a developer would reasonably implement with a
  `if len(names) <= max { return names }` fast path — the obvious, free-looking optimisation. That
  pass-through makes the published `finTrailerRecord.KeyNames` alias `trailScanResult.KeyNames` itself,
  because `finTrailerBuild` copies the field by **plain slice assignment**, and the shipped
  `dropped := seen` struct copy at `finding_trailer_evidence_test.go:619` then puts two carriers on one
  backing array under `go test -race`. Clause 5 now forbids the fast path by name and states that the
  builder's plain assignment is safe *only because* the clause holds. No locks, no goroutines, no
  shutdown path, no shared mutable state otherwise.
- **[9. Threat model alignment] No findings; two exposures accepted and named.**
  - *Backtick fence escape* — **OUT OF SCOPE, no owner ticket, and that is deliberate.** `run.md` embeds
    the record in a ```` ```json ```` fence (`finding_artifact_write_test.go:145-151`) and
    `MarshalIndent` escapes control characters and quotes but **not** backticks, so a string carrying a
    fence-closing sequence breaks out when pasted. The door is already open through `stop_reason`, which
    crosses **uncapped by design** (`finding_trailer_evidence_test.go:130-136`); closing it for key names
    alone would defend one door while the older one stays open. Noted rather than deferred to a named
    ticket, per the ticket body: it is a separate ticket if it is anyone's. `finBoundKeyNames`' 64-byte
    cap shortens but does not eliminate it — a four-backtick sequence is 4 bytes.
  - *Discriminator inversion at exactly the bound* — **accepted, and stated in code by AC1's field
    prose.** If the count bound cut an alphabetic prefix such that `terminal_reason` fell off, a
    pyry-authored trailer would read as claude-authored: a **false verdict**, not merely missing
    evidence. Unreachable from any producer (the envelope keys are CLI-authored and number eleven; the
    bound is 32), so per *Evidence-Based Fix Selection* it gets prose at the field rather than a
    mechanism. No name is special-cased to survive the cut — a hard-coded keep-list would be a second
    source of truth for what the line carried.
  - *False assurance to an operator* — the highest-severity item, and **not** deferrable, which is why
    AC4 sits in this ticket. A standing sentence telling an operator every field is rig-authored,
    printed beside a field of claude-authored strings, is worse than no sentence: it converts a field
    needing one line of justification into a blanket "safe to paste unreviewed". Nothing catches it — the
    shipped test asserts only that the note *contains* the constant, never that it is true
    (`finding_artifact_write_test.go:1067-1072`), so the false version ships green. Mitigated by § 3.6,
    whose three checkable properties are what review verifies instead of the prose.
  - *Value leakage through the new field* — blocked twice: structurally at the reader, and proved by the
    already-shipped `TestTrailKeyNamesCarryNoValues`, which plants a distinct needle in **every**
    string-valued position of a trailer line and asserts none reaches `KeyNames`. AC1's end-to-end
    equality is what ties the **published** names to that proven-clean output; without it the proof stops
    one tier short of the artifact. The per-element bound of § 3.3 clause 3 is what keeps #1362's sweep
    honest — a joined-string cap would truncate a leak in a late name away and turn the sweep green over
    a record that leaked, which is the defect #1284 shipped and had to fix.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-07

---

## 9. Open questions

1. **`finTrailerMaxKeyNames = 32` vs a larger bound.** 32 is ~3× the real eleven-key shape and two orders
   of magnitude below the 4680-name provability floor. If the claude CLI ever grows past ~32 envelope
   keys the record publishes a prefix and a reader sees `len == 32`. Changing it is a one-line constant
   edit; #1364's fixtures assert against the constant rather than against the literal, so nothing else
   moves. Flagged, not blocking.
2. **Whether `#1364` should also assert `finBoundKeyNames` does not alias its input.** § 3.3 clause 5 is
   a contract this spec states and no test pins. It is not a correctness risk today (both fill sites hand
   it a freshly-scanned slice), only a `-race` hazard if a future caller shares one. Left to #1364's
   judgement rather than mandated here.
