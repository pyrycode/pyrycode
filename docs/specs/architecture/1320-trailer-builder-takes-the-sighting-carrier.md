# #1320 — `finTrailerBuild` takes the sighting carrier in place of the observation

Ticket: <https://github.com/pyrycode/pyrycode/issues/1320> · Size `s` · `security-sensitive`
Split from #1315. Blocks #1304, #1316, #1321. Nothing blocks it.
Line numbers as of `9b7a51d`; **locate by symbol name, not by line.**

---

## Files to read first

Turn-1 data load. Read these before writing anything.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:203-234` | `finTrailerBuild` — the signature you change, the `Bounded` derivation you must NOT touch (`:209-215`), the guard you rewrite (`:218`), the two Detail shapes. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:82-155` | `finTrailerRecord`. The ten flat scalars, `:86-94`'s two claims (AC5 items 1–2), and `:112-124`'s `StopReason` caveat — **the reason the new trap-free claim must not over-claim.** |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:159-202` | The builder's doc block. `:168-171` (AC5 item 3) and `:192-202` (AC5 item 4). `:173-190` (outcome consumed, never decided) is unchanged by the new input — leave it. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:238-252` | `finTrailerSeenScan` / `AbsentScan` / `AbortedScan`. The new helper goes immediately after these, same tier. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:263-311` | The bound table and the **`synthetic` field idiom** (`:270-272`, used at `:305-309`) the new test's false arm must be named in. |
| `internal/e2e/realclaude/finding_run_gather_test.go:346-356` | `finSighting` — the eight fields, the json tags, the `CarriesTrailer` pair. |
| `internal/e2e/realclaude/finding_run_gather_test.go:442-453` | **The fill the new helper must be a copy of.** State operand first, four scalars only behind the pair. `:438-441` is #1309's declined-constructor reason; `:443-447` is the agreement obligation that lands on the helper. |
| `internal/e2e/realclaude/finding_run_gather_test.go:1595-1680` | `TestFinGatherSightingScalarsComeFromTheFullLineDecode` — where the retiring `:430-470` row's claim already lives, one tier down. |
| `internal/e2e/realclaude/finding_run_gather_test.go:1696-1722` | `TestFinSightingReachesNoScanType` — the **checked** proof that `finSighting` reaches no scan type. Every "trap-free by construction" sentence you write cites this, not a comment. |
| `internal/e2e/realclaude/finding_run_record_test.go:464-479` | Two of the nine call sites. |
| `internal/e2e/realclaude/finding_artifact_write_test.go:165-192` | `finWriteTrailerPad = 0` and `finWritePlantedTrailerScan` — the in-cap plant. Do **not** repad. |
| `internal/e2e/realclaude/finding_artifact_write_test.go:263-287` | The ninth call site, inside `finWriteInputs`. |
| `internal/e2e/realclaude/finding_artifact_write_test.go:899-940` | `TestFinWriteArtifactPublishesNoVerbatimModelOutput` — **the surviving true-arm pin.** Read it before deciding what the new test owes. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:52-90` | The three scan states and the three lateness discriminators, by name. |
| `CODING-STYLE.md` | Table-driven tests, stdlib only, error-message register. |

---

## Context

`finTrailerBuild` projects a run's outcome plus its trailer evidence onto the published `finTrailerRecord`. It takes a `trailObservation` today, which embeds `trailScanResult` — so `.Line` (verbatim model output, OPERATOR-REVIEW-BEFORE-PASTE, ~415 of its retained 512 bytes being the assistant's last message) and the `*resultTrailer` are both in the builder's reach. That is why `finGatherReadings` will not hand its observation back, and why the live composition currently has no way to build a record from the sighting it classified against.

The workaround that shape invites is a **mis-report, not a cost**: a second `trailWaitForTrailer` matches on its first poll and reports `trailBoundFromStart`, a discriminator whose own doc says it bounds nothing. `Bounded` is `BoundFrom == trailBoundFromMiss` and nothing else, so such a record publishes `lateness_bounded: false` for a run whose first sighting was genuinely miss-bounded — understating exactly the evidence the artifact exists to carry.

`finSighting` (#1309) closes this. Seven of its eight fields are the record fields the builder projects; the eighth, `CarriesTrailer`, decides which arm fills the four scalars. With `Outcome` from the argument, `Bounded` derived and `Detail` formatted, **all ten record fields are reachable from `(outcome, carrier)` — the projection is total.**

This ticket is the move: signature, nine call sites, guard, and the builder's own four doc claims. It is bounded by **compilation** — all nine sites must migrate in one diff or the package does not build. Everything that is argument rather than compilation is #1321's.

---

## Design

### 1. The signature

```go
func finTrailerBuild(outcome string, sighting finSighting) finTrailerRecord
```

`trailObservation` disappears from the builder and from all nine fixtures. AC1's structural half falls out of the type: no consumer can take a second observation of the same buffer to build a record, because the type the builder accepts is not one a scan produces.

`Bounded` keeps its single source — `sighting.BoundFrom == trailBoundFromMiss`, the `:209-215` comment intact and its prose unchanged. Do not move the derivation onto the carrier; `finSighting` deliberately carries no `Bounded` (`finding_run_gather_test.go:286-290`).

### 2. The fixture helper — the design decision AC3 leaves open

**Decision: one test-side helper, not inline-per-site.**

```go
// finTrailerSighting is the fixture-side stand-in for finGatherReadings' carrier
// fill, and is a copy of it (finding_run_gather_test.go:442-453).
func finTrailerSighting(scan trailScanResult, staleness time.Duration, boundFrom string) finSighting
```

Behaviour, one line: copies `State`, `BoundFrom` and `Staleness` through, sets `CarriesTrailer` from `scan.State == trailSeen && scan.Trailer != nil` with **the State operand first**, and fills the four scalars off `scan.Trailer` only behind that pair.

Why a helper and not nine inline literals:

- AC3 requires the four scalars and `CarriesTrailer` be **derived from a shipped scan**, and requires the projection gain no second source that can drift from the gather's. Inline-per-site recomputes the pair **nine** times; the helper recomputes it **once**. One recomputation pinned against the gather's is what the ticket asks for: *"computed in as few places as the shape allows."*
- #1309's declined `finSightingFrom` was declined **inside the gather** — "funnelling the whole composition through one function is what makes it checkable in one place" (`:438-441`). That argument is about the *live composition*. This helper is not in the composition; it is a fixture that stands where `finGatherReadings` stands. It adds no symbol to the production path and forks nothing.
- Taking `scan trailScanResult` rather than `obs trailObservation` means **no fixture in these three files constructs a `trailObservation` again**. The parameter list is a mechanical transposition of what every site writes today (`{trailScanResult: X, Staleness: Y, BoundFrom: Z}`), so the migration is a transposition and not a rewrite.

**The helper inherits the agreement obligation.** After this ticket the builder computes nothing, so the "deliberately identical to `finTrailerBuild:218`, because the two computations must agree" obligation at `finding_run_gather_test.go:443-447` lands here. The helper's doc comment must state it, cite the gather's fill by symbol, and say the two are required to agree. Re-stating the *gather's* side of that comment is #1321's — **do not edit `finding_run_gather_test.go`.**

No deterministic pin of helper-against-gather is available inside this file: `finGatherReadings` execs `ps` via `pinScanArgv` / `pinReadState`, and `finding_trailer_evidence_test.go` forbids exec by its own header (`:8-11`). The obligation is therefore held by the comment plus review, as it is on the gather's side today. Flagged in Open Questions.

### 3. The guard, and the drop as a decision

The guard reads `sighting.CarriesTrailer`. The operand pair it evaluates today is unrecomputable from the carrier — that is `CarriesTrailer`'s whole reason to exist.

**On the false arm the builder still ZEROES the four scalars rather than copying them through.** Today `!carriesTrailer` implies there is no pointer to read, so the zeroes are by construction and the early return could not do otherwise. Under the carrier the scalars are separately settable, so `CarriesTrailer: false` beside a non-zero `Subtype` is reachable for the first time — and it is the only input shape that separates a builder that drops from one that copies through.

The argument for the drop is **in the arm's own Detail**: `:219-221` formats *"the four trailer fields hold their zero values"*, so a builder that copied them through would publish a record contradicting its own Detail in the same breath. Keep the Detail's wording; it is now load-bearing.

The two Detail shapes, the single `if`, no third branch and no reject arm all stay. The builder stays pure over its inputs and never fails a test.

### 4. The nine call sites

Each site's `trailObservation{...}` literal becomes one `finTrailerSighting(...)` call. Sites that omitted `Staleness` pass `0` explicitly.

| Site | Becomes |
|---|---|
| `finding_trailer_evidence_test.go:328-333` | delete the `obs :=` local; `finTrailerSighting(tc.scan, tc.staleness, tc.boundFrom)` |
| `:402` | `finTrailerSighting(scan, 0, trailBoundNone)` |
| `:446` | `finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss)` |
| `:514` | `finTrailerSighting(tc.scan, 0, trailBoundNone)` |
| `:570-576` | the `obs` local becomes a `finSighting` from `finTrailerSighting(finTrailerSeenScan(), 250*time.Millisecond, trailBoundFromMiss)`; the loop still varies only the outcome |
| `:642` | `finTrailerSighting(scan, 250*time.Millisecond, trailBoundFromMiss)` |
| `finding_run_record_test.go:467` | `finTrailerSighting(trailScan([]byte(trailFixtureTrailer+"\n")), 250*time.Millisecond, trailBoundFromMiss)` |
| `:475` | `finTrailerSighting(trailScan([]byte(trailFixtureNoTrailer)), 0, trailBoundNone)` |
| `finding_artifact_write_test.go:278` | `finTrailerSighting(finWritePlantedTrailerScan(), 250*time.Millisecond, trailBoundFromMiss)` |

The premise check at `:320` (`tc.scan.State != tc.wantState`) stays on the scan — its claim is about the shipped scanner. The helper's `State` copy is still pinned, transitively: `:335` asserts `rec.State` against `tc.wantState`, and that path now runs scan → helper → builder.

No import changes: all three files still use `time`, and `trailObservation` is a package type, not an import.

### 5. AC5 — the builder's four doc claims

Re-state the claim each block now makes. **Do not paste the old phrasing forward**, and do not merely negate it — three of the four invert, and the interesting content is *why*.

| Block | What it says now | What it must say |
|---|---|---|
| `:86-91` | the record is trap-free by construction, "THE BUILDER IS NOT — its input does carry the capped line" | Both are trap-free now, at two tiers. Keep the "said again at `finTrailerBuild`" pairing consistent with item 3 below — re-stating one and not the other leaves the pair contradicting. |
| `:93-94` | the four fields are "copied BY VALUE at build time, which severs the pointer" | The severing happens **one tier up** — at the carrier fill, where the pointer still exists. `finSighting` has no pointer field at all, so no two records built from one carrier can alias a `*resultTrailer`: the property is now structural rather than bought at build time. |
| `:168-171` | "UNLIKE THE RECORD IT RETURNS, THIS FUNCTION IS NOT TRAP-FREE BY CONSTRUCTION" — no-captured-bytes held by the Detail rule plus the sweep, "not by the shape of the input" | It **is** trap-free by construction: the input carries no `.Line` and no `*resultTrailer`, and that is **checked** by `TestFinSightingReachesNoScanType` (`finding_run_gather_test.go:1696`) rather than asserted in prose. The no-captured-bytes property is now held by the shape of the input. |
| `:192-202` | "# The State is consulted before the pointer is dereferenced" — `&&` short-circuit ordering, the `trailGate` precedent | There is no pointer and no deref here; the guard reads one bool. The ordering argument moves to wherever the pair is computed (the gather's fill, and `finTrailerSighting`). What stays: the false arm publishes the zeroes **under whatever State was handed** and lets the Detail name which arm fired; one `if`, two Detail shapes, no third branch, no reject arm. What is **new**: the drop is a decision rather than a consequence, because `CarriesTrailer: false` beside non-zero scalars is now reachable — with the Detail-contradiction argument and a pointer to the new test. |

**Do not over-claim "trap-free."** It means no `.Line`, no `*resultTrailer`, no `PermissionDenials`. It does **not** mean no model-influenced bytes cross: `StopReason` is forwarded from the model's last message, uncapped, **by design**, and the record's own doc says so at `:112-124`. Any re-statement that reads as "nothing model-influenced reaches the record" contradicts a shipped block two screens up and is wrong. Name the exception.

`:173-190` (the outcome is consumed, never decided) is unchanged by the new input — leave it alone.

### 6. The four marks — migrate minimally, mark, do not retire

These are call sites, so they migrate here. The argument for retiring each is #1321's. Add one comment per site saying the claim is superseded, why, and that #1321 retires it — so none reads as a live guard. **Do not rewrite the superseded comment bodies; mark above them.** A developer's instinct will be to fix the now-false prose at `:394-401` and `:452-455`; that is #1321's diff, not this one.

1. **`TestFinTrailerRecordCarriesNoCapturedBytes` (`:623`)** — plants `trailNeedle` in a `.Line` the builder's input no longer has, so it asserts the absence of a needle its input structurally cannot carry. Coverage held meanwhile by `TestFinGatherReturnsNoCapturedBytes` (`finding_run_gather_test.go:1107`), which sweeps the carrier as its third return (`:1172`) and asserts its own in-cap non-vacuity precondition (`:1141-1146`).
2. **#1286's Plant #3 (`finding_artifact_write_test.go:278`)** — same, at the artifact tier. The mark may point forward at `finWriteInputs`' plant list (`:228`, "the trailer scan's Line — dropped by `finTrailerBuild`") as #1321's to revisit; **do not edit `:228`.** The directory-wide needle sweep stays non-vacuous — the other three plants (row `Command`, `ClaudeCommand`, reap stderr) are untouched.
3. **The `:384` "a no-trailer observation returns rather than panicking" subtest** — the panic claim (`:394-401`) is structurally unreachable with no pointer to deref. Its zero-fields assertions survive and are AC2's, **but they are tautological over the migrated fixture**: the carrier derives all-zero scalars from an absent scan, so they pass against a builder with no guard at all. The panic-on-unchecked-deref obligation is named by `TestFinSightingReachesNoScanType`'s failure message.
4. **The `:430-470` "the four fields survive a cap that destroys terminal_reason in the line" subtest** — its claim, "the projection reads Trailer and not Line" (`:452-455`), is dead: the builder reads neither. #1313 shipped that claim one tier down as `TestFinGatherSightingScalarsComeFromTheFullLineDecode` (`finding_run_gather_test.go:1595`), on an over-cap fixture with the disagree-precondition asserted on the **key**.

Because rows 3 and 4 stop carrying AC2's weight, the new test below is what carries it.

---

## Concurrency model

None, and that is the design. `finTrailerBuild` and `finTrailerSighting` are pure over their inputs: no goroutine, no lock, no clock, no channel, no `context.Context`, no `*testing.T`. Neither fails a test — an instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn (the same contract as `trailScan`, `trailGate`, `trailAdmitAttribution`, `trailClassifyRun`, `finOutcomeStagingGate`, `tdnClassifyReapLog`, `pinReadState`).

The one concurrency-adjacent rule this file already enforces and the helper must not break: **fixtures are functions, never package-level vars** (`trail_run_outcome_test.go:608-610`) — a shared backing array is reachable from every test in the package and `go test -race` runs them in parallel. `finTrailerSighting` returns a fresh value per call and holds no package state.

---

## Error handling

No error paths are added. The builder has two arms and no reject arm, and that stays. The relevant failure modes are *fixture* failure modes:

- **A carrier that says one thing and holds another.** `CarriesTrailer: false` beside non-zero scalars is now constructible. The builder's answer is the drop; the new test pins it; the arm's own Detail is the argument.
- **A helper that silently disagrees with the gather.** Not detectable in this file (the gather execs). Held by the helper's doc comment stating the obligation and citing `finding_run_gather_test.go:442-453`. See Open Questions.
- **An incompletely-filled carrier.** `CarriesTrailer`'s zero is `false`, which routes to the no-decoded-trailer arm and publishes the zeroes under whatever State was handed — an honest nothing-was-measured rather than a claim (`finding_run_gather_test.go:320-323`). Unchanged.

---

## Testing strategy

### New: one test pinning the guard in **both** directions

Name suggestion: `TestFinTrailerRecordFillsTheFourScalarsOnlyBehindCarriesTrailer`.

Two arms, because a false-arm-only pin leaves a real hole. Check the mutation table before writing:

| Mutation | Caught by |
|---|---|
| builder ignores `CarriesTrailer`, always **copies through** | the new false arm ✓ |
| builder ignores `CarriesTrailer`, always **zeroes** | today: `:446` and `:642` — **both marked for retirement in #1321.** After #1321 the only survivor is `finding_artifact_write_test.go:899-935`, one tier up and in another file. The new true arm closes that in this file. |

Scenarios (bullets, not code — write them in this file's idiom):

- **Build the true arm's carrier through `finTrailerSighting` over `finTrailerSeenScan()`.** No typed-in scalars anywhere.
- **Assert the non-vacuity precondition with `t.Fatalf` before anything else:** the carrier reports `CarriesTrailer == true` and all four scalars are non-zero. Without it, a broken helper makes both arms compare zero against zero and the whole test is theatre. The file Fatalf's its preconditions everywhere (`:320`, `:386`, `:437`, `:627`) — match that register.
- **Derive the false arm from the true one by flipping the single bit** (`dropped := seen; dropped.CarriesTrailer = false`). This is the pairing AC2 wants and no scan can produce. It is a *stronger* reading of AC3's no-literal rule than typing the row in — everything a scan could produce still comes from the scan, and only the impossible bit is set by hand. It also makes the contrast structural: the two carriers differ in exactly one field, so nothing else can explain a difference in the built records. Assumption flagged in Open Questions.
- **Name the false arm as synthetic**, in the `:305-309` register: no run produces the pairing; the row is a contract check on a builder pure over its inputs; it is what separates a builder that drops from one that copies through. Naming it is what keeps it from reading as an observation.
- **True arm asserts** all four scalars equal the carrier's.
- **False arm asserts** all four are zero, and that `State`, `BoundFrom`, `Staleness` and `Bounded` still cross — a run with no trailer publishes the zeroes *under the state that says so* rather than collapsing into "there was no trailer".
- **Assert the two Details differ**, one line: it pins that the guard branched, not merely that some fields changed. Do not string-match the Detail's wording — the assertions on the four zeroes are what enforce the Detail's claim, and a comment should say so.

### Existing tests — what each keeps (AC4)

| Test | Keeps |
|---|---|
| `TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator` (`:263`) | all four rows, all three scan states, all three discriminators, the executable coverage loops (`:367-376`), and the pin that `Bounded` is true on the miss bound **alone** — including the two rows carrying a non-zero staleness beside a discriminator that bounds nothing. The aborted row's `synthetic` string (`:305-309`) stays true. |
| `TestFinTrailerRecordOutcomeIsConsumedAsHanded` (`:483`) | both disagreeing rows, the disjointness subtest, the 18-value union, the full coverage loop. The outcome stays consumed and never decided. |
| `TestFinTrailerRecordReadsTheDecodedTrailer` (`:383`) | both subtests migrate and are **marked**; neither is deleted here. |
| `TestFinTrailerRecordCarriesNoCapturedBytes` (`:623`) | migrates and is **marked**; the headroom-per-row check and the flat forbidden-key scan stay untouched. |
| `finding_run_record_test.go` / `finding_artifact_write_test.go` | every downstream assertion must stay green unchanged. In particular `TestFinWriteArtifactPublishesNoVerbatimModelOutput` (`:899-935`) still reads `error_max_turns` / `max_turns` / `end_turn` / `is_error` off the written artifact — if that goes red, the helper is dropping scalars the gather would have carried. |

### Commands

`make check` and `make build` never compile `e2e_realclaude`-tagged files, so a PR whose whole diff sits under that tag is a **vacuous green**. Run both explicitly:

```bash
go vet  -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...
```

Expect a **PASS/SKIP split**, not all-PASS — the live specs skip without credentials. An all-PASS run or a ~3-second exit means the offline specs did not run either; treat that as a failed run, not a green one.

Everything in this ticket runs offline: no live claude, no credentials, no daemon, no turn, no process-table read, no `t.Skip`.

### Mutation checks before opening the PR

Deterministic, via `go test -overlay`:

1. Delete the guard (always copy the four through) → the new false arm must go RED.
2. Delete the guard the other way (always zero) → the new true arm must go RED.
3. Change `Bounded` to `BoundFrom != trailBoundNone` → `TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator`'s start-bound row must go RED.

Grade each under `-run '^TestName$'` **alone** — a co-firing test masks a vacuous one.

---

## Scope guards — do not touch

The seam with #1321 is compilation. Everything below is argument, and #1321 owns it:

- **`internal/e2e/realclaude/finding_run_gather_test.go` — the entire file.** Including `finSighting`'s `:281-282` ("#1308 moves `finTrailerBuild` onto it; nothing consumes it yet"), `:311`, `:341` ("#1308's move onto this value is a rename-free projection"), `:443-447` and the `CarriesTrailer` failure message at `:1460`. Several become false the moment this ticket lands. That is deliberate and wired.
- `finding_run_record_test.go:45-58` — "the STRONGER property #1290 could not buy" and "exactly `finTrailerBuild`'s posture".
- `finding_artifact_write_test.go:228` and `:940`.
- The superseded comment bodies at `:394-401` and `:452-455` — **mark, do not rewrite**.
- `finWriteTrailerPad` — do **not** repad. It is `0` deliberately; `:167-186` is the argument, and a past-the-cap pad turns the sweep into a green proof of a false claim.
- Do **not** add a `docs/knowledge/codebase/1320.md` — the documentation phase writes it after merge.

---

## Open questions

1. **The helper's agreement with the gather is held by a comment, not by code.** `finGatherReadings` execs `ps`, and this file forbids exec, so no in-file pin is available. The obligation moves from `finTrailerBuild:218` to `finTrailerSighting`; #1321 re-states the gather's side. If a deterministic pin is wanted, it belongs in the gather's file, on the gather's tier, in a later ticket — not here.
2. **The derive-then-flip form of the synthetic row is a reading of AC3.** AC3 anticipated the contract-check row being "typed in". Deriving it from the true arm and flipping the one impossible bit satisfies the no-literal rule maximally while producing exactly the pairing AC2 requires. If review prefers a literal `finSighting{...}`, the assertions are unchanged — only the row's construction moves.
3. **After #1321, `TestFinTrailerRecordReadsTheDecodedTrailer` has no live subtest left.** Both of its rows are marked here. #1321 decides whether the shell is deleted or renamed; the new test is deliberately top-level so that decision is a clean one.
4. **`finding_artifact_write_test.go:940` cites "pad 200"; the fixture at `:436` is `trailPaddedTrailer(2000)`.** A pre-existing stale cite in a comment pointing at a row #1321 retires. Not this ticket's — flagged so #1321's architect sees it rather than inheriting it.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST-FIX, addressed in the design.** This ticket *is* a trust-boundary move, and getting it backwards was the live risk. The untrusted-to-published boundary is verbatim model output (`trailScanResult.Line`, ~415 of 512 retained bytes chosen by the model; `*resultTrailer` and its `PermissionDenials *[]json.RawMessage`, raw bytes no cap applies to). Today that boundary sits **inside** `finTrailerBuild`, held by the Detail content rule plus a needle sweep — a stochastic guard over a reachable field. After this ticket it sits **one tier up**, at the carrier fill, and the builder holds only scalars. The boundary becomes structural and is **checked** by `TestFinSightingReachesNoScanType` (`finding_run_gather_test.go:1696`), which walks `finSighting` for three forbidden types rather than asserting over one instance. Different fabric, deterministic side: the belt-and-suspenders rule holds. The spec requires the guard-marking (§6) precisely so the retiring needle sweep is not read as still holding the property it no longer holds.
- **[Trust boundaries] SHOULD FIX — the over-claim risk in AC5.** "Trap-free by construction" is true for `.Line`, the pointer and `PermissionDenials`; it is **false** as a blanket claim, because `StopReason` crosses uncapped and model-influenced **by design** (`finding_trailer_evidence_test.go:112-124`, `finding_run_gather_test.go:325-335`). A re-statement that reads as "nothing model-influenced reaches the record" would contradict a shipped block two screens up and would mislead the next sweep author into planting a needle in a field the record must carry verbatim. §5 names this explicitly; code-review must check the shipped wording against it.
- **[Error messages, logs, telemetry] No findings, and one property strengthened.** The published `Detail` may name the outcome, state, `BoundFrom`, `Bounded` and the four scalars, and may never quote `Line` or any derivative (length, byte count, prefix, hash). Under the carrier the builder has **no `Line` to quote** — the rule survives as a rule about what the Detail may *say*. #1284's per-row headroom check (`:648-662`) is untouched and still asserts `reachMaxCommandBytes - len(Detail) >= len(trailNeedle)` per row, so a later Detail lengthening still fails loudly rather than silently disarming a sweep. The new test's Detail assertions are deliberately non-string-matching, so they add no new coupling to Detail wording.
- **[File operations] Not applicable by design.** No path is constructed, opened or written by anything this ticket touches. `finWriteArtifacts` writes at `0o600` into a `t.TempDir()` and is unchanged; the ninth call site only supplies it a sub-record.
- **[Subprocess / external command execution] Not applicable, and the boundary is enforced by the file.** Neither `finTrailerBuild` nor `finTrailerSighting` execs. Every symbol they call is pure over bytes: `trailScan`, `trailDetail`, `reachCapCommand`, `trailPaddedTrailer`, `finTrailerSeenScan` / `AbsentScan` / `AbortedScan`, `finWritePlantedTrailerScan`. The two exec-bearing helpers on the *gather's* path are `pinScanArgv` and `pinReadState`, reached only through `finGatherReadings` — which this ticket does not call and does not touch, and which is why no in-file helper-vs-gather pin is available (Open Question 1). Other helpers in this package exec internally as well (`probeProcessSnapshot`, `tdnScan`, `holdProbeFIFO`, `WithWorktreeAuthenticated`); none is on any path this ticket touches. A grep for `exec.` would read clean here for the wrong reason — every route off the offline path runs through a shipped helper that execs *inside* — so the symbol enumeration above, not the grep, is the check.
- **[Tokens, secrets, credentials] No findings, and it is the reason the label is on the ticket.** The channel this family exists to keep shut is an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaching an artifact destined for a public issue via a verbatim argv or ps column. This ticket touches neither channel: `finRecordInputs` still carries `Rows[i].Command` and `ClaudeCommand`, and their reduction is `finRecordBuild`'s, unchanged. `finTrailerRecord` stays **flat — ten scalars** — which is the sole reason the top-level forbidden-key scan at `:684-696` is valid; the spec changes the builder's **input** and adds no field to the record, so that scan keeps examining every key the record has.
- **[Concurrency] No findings.** Both functions are pure and hold no package state; the helper returns a fresh value per call, honouring the file's function-not-var fixture rule (`trail_run_outcome_test.go:608-610`) under `go test -race`.
- **[Cryptographic primitives] Not applicable.** No randomness, hashing, comparison against a secret, or key material anywhere in the touched surface.
- **[Network & I/O] Not applicable.** No socket, no reader with a size cap to argue about. The one cap in reach, `reachCapCommand`'s 512 bytes, is single-sourced and untouched.
- **[Threat model alignment] Aligned; one deliberate window named.** The standing threat this family addresses is "an artifact published unreviewed carries bytes an operator would have had to review". Between this ticket and #1321 the needle sweeps at `finding_trailer_evidence_test.go:623` and `finding_artifact_write_test.go:278` are green **structurally** rather than by measurement. No coverage lapses: `TestFinGatherReturnsNoCapturedBytes` (`finding_run_gather_test.go:1107`) already sweeps the carrier as its third return (`:1172`) with its own in-cap non-vacuity precondition (`:1141-1146`), one tier down and independent of this diff. §6's marking requirement is what keeps that window named in the source rather than silent.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-05
