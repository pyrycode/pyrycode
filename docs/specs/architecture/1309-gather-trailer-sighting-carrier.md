# #1309 — Return the trailer sighting's measurement from the run-outcome gather as a leak-free carrier

**Size:** S (see § Scope check) · **Label:** `security-sensitive` · **Everything offline.**

## Files to read first

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go:126-156` | The constants block. `finGatherTrailerWait` is AC1's upper bound. Its "NO ROW EVER WAITS IT OUT" claim is **not** falsified here — leave it alone (#1310 owns it). |
| `…/finding_run_gather_test.go:253-358` | `finGatherReadings` — the doc block to re-state (`:263-271`) and the body to fill from (`:304-306`, where `obs` lives). |
| `…/finding_run_gather_test.go:388-426` | `finGatherCases()`. The C4 row at `:413-423` is AC2's no-decoded-trailer arm; its comment states why it costs no wall clock. |
| `…/finding_run_gather_test.go:930-1035` | `TestFinGatherReturnsNoCapturedBytes` — AC4's target. The `{what, value}` slice is at `:1010-1016`; the cap premise on the **scan result** is at `:993`. |
| `…/finding_run_gather_test.go:1039-1101` | `finGatherForbiddenKeys` / `finGatherExemptKeys` / `finGatherForbiddenKeyPaths` — the walk AC4 reuses unchanged. |
| `…/finding_run_gather_test.go:96-113` | The file header's **Fatalf content rule**. Its licence sentence covers "a whole readings or record" and does not yet cover a sighting — see § The header's licence. |
| `…/result_trailer_observation_test.go:75-90` | The three bound-origin constants, incl. `trailBoundFromStart` "BOUNDS NOTHING". |
| `…/result_trailer_observation_test.go:94-137` | `trailScanResult` (`.Line` = OPERATOR-REVIEW-BEFORE-PASTE, `.Trailer` = discriminated optional) and `trailObservation`. The three fields the carrier must **not** be able to reach. |
| `…/result_trailer_observation_test.go:242-274` | `trailWaitForTrailer`. Note `Staleness` and `BoundFrom` are set at `:256-262` from the poll's own stamps — the gather takes no clock reading of its own. |
| `…/finding_trailer_evidence_test.go:100-155` | `finTrailerRecord`'s field set, its json keys, the **no-`omitempty` decision**, and § "What the four trailer fields are worth" (`:112-124`) — `StopReason` is the one model-influenced field crossing uncapped. |
| `…/finding_trailer_evidence_test.go:203-234` | `finTrailerBuild`. `:209-215` is `Bounded`'s single source (no `Bounded` on the carrier); `:218` is the `State && Trailer != nil` pair whose *outcome* the carrier records. |
| `…/finding_trailer_evidence_test.go:623-662` | `TestFinTrailerRecordCarriesNoCapturedBytes` — the needle-inside-the-cap Fatalf and the per-row headroom check. Precedent only; this ticket adds no Detail, so no headroom obligation arises. |
| `…/finding_run_record_test.go:724-756` | `finRecordInputReaches` — **the walker AC3 reuses.** Same package, same build tag. |
| `…/finding_run_record_test.go:784-816` | `TestFinRecordEmbedsTrailerRecordWhole`'s structural subtest — the shape AC3's test copies, with one more forbidden type. |
| `…/trail_run_outcome_test.go:200-219` | `trailRunReadings.BoundFrom` and the comment stating why `Staleness` is **deliberately absent** there. AC1's "no counterpart to agree with". |
| `…/tool_loop_test.go:194-203` | `resultTrailer` — note `PermissionDenials *[]json.RawMessage` at `:199`, raw bytes no cap applies to. This is why the walk forbids the decoded type, not only the two scan types. |
| `…/finding_stage_held_group_test.go:409-437` | The seventh call site (`:423`). One-token edit. |

## Scope check

Measured at `fddf873`, before writing:

| Red line | Limit | This ticket |
|---|---|---|
| New files | > 3 | **0** (spec only) |
| Total written LOC | > ~600 | **~440–510** (see below) |
| New exported types | > 5 | **1** (`finSighting`) |
| Consumer call sites | > 10 | **7** — `finding_run_gather_test.go:463, 644, 768, 793, 889, 964` + `finding_stage_held_group_test.go:423` |
| Acceptance criteria | > 5 | **4** |
| Reject / error branches | > ~10 | **1** (carries-a-decoded-trailer or not; no reject arm) |

LOC is the only projection, so it is anchored on a measured analogue rather than bottom-up alone. `fc33196` (#1302, the immediately preceding ticket in this same file) measured **390 insertions / 48 deletions = 438** and landed as `size:s` with no salvage label. Its shape: one new 6-field type with a 63-line doc block, two new tests (93 + 100 lines), a helper, and seven call sites converted from positional args to struct literals. #1309's shape: one new 8-field type (~90 lines with doc), three new tests (~50 + ~100 + ~45), a four-clause doc re-statement (~60), and seven **one-token** call-site edits. Heavier on tests and doc, materially lighter on the call-site cascade — within ~15% of the analogue either way, and the analogue is a completed S run.

Bottom-up sums to ~362; this package's doc-density correction (~+40%) puts it at ~505. Under the line on both methods. **Size S; spec written.**

Production-source-file count for the pre-commit gate: **0** — both touched files are `*_test.go` and excluded by the rule.

Branch-overlap check (`git fetch origin --prune` then `git diff --name-only origin/main...origin/feature/N` over all 41 remote feature branches): **no branch touches either file.** `origin/feature/1307` — the parent this was split from — has an empty diff against main. No `addBlockedBy` needed.

## Context

`finGatherReadings` runs the trailer poll, classifies against the sighting, and throws the sighting away. Step 3 of the live composition (`finTrailerBuild`) needs it. Recovering it with a second `trailWaitForTrailer` is not equivalent and the difference is a **mis-report**: by then the trailer is already in the buffer, the second call matches on its first poll, and it returns `trailBoundFromStart` — a discriminator whose own doc says it BOUNDS NOTHING. The first sighting's bound is the measurement the artifact exists to carry.

The withholding itself is correct and is not undone: `trailObservation` embeds `trailScanResult`, so returning it promotes `.Line` (verbatim model output, ~415 of its retained 512 bytes being the assistant's last message) and `.Trailer` (a `*resultTrailer` carrying uncapped `PermissionDenials`) into the caller's reach.

So the gather hands back what the sighting **measured** without handing back the sighting. This ticket is that carrier: it exists, it is filled, and it is proven to reach neither the scan types nor any captured byte. Nothing consumes it yet — `finTrailerBuild` keeps its signature and its callers; #1308 moves it across.

**This ticket makes structural claims only.** That the carrier reports the *miss* bound over a buffer that misses a poll first, and that its four scalars come from the full-line decode rather than the capped copy, are behavioural claims **#1310** proves by measurement. Do not build fixtures for them here.

## Design

### The carrier

New type in `finding_run_gather_test.go`, beside the gather. Prefix `finSight*` verified at **0** occurrences at `fddf873` (control: `trail[A-Z]` = 2193 by the ticket's own recipe, `rg -c … --no-filename` summed).

```go
type finSighting struct {
	State          string        `json:"trailer_state"`
	BoundFrom      string        `json:"lateness_bound_from"`
	Staleness      time.Duration `json:"staleness_ns"`
	CarriesTrailer bool          `json:"carries_trailer"`

	Subtype        string `json:"subtype"`
	IsError        bool   `json:"is_error"`
	TerminalReason string `json:"terminal_reason"`
	StopReason     string `json:"stop_reason"`
}
```

Eight fields, and the set is exactly what `finTrailerBuild` reads off a `trailObservation` today (`:203-233`) minus what it derives or ignores. What the type's doc comment must establish — the developer writes the prose in the file's idiom, but each of these has to be in it:

- **No `Bounded`.** `lateness_bounded` is `BoundFrom == trailBoundFromMiss` and nothing else, at `finding_trailer_evidence_test.go:209-215`, and that stays its one source. The carrier supplies the discriminator that derivation reads. A second source would let a record publish a non-bound wearing a bound's label.
- **No `ObservedAt`.** `finTrailerBuild` does not read it, and the constraint is one-way: the carrier holds what its consumer needs and nothing more. A clock value with no reader is a field whose zero has to be argued about later.
- **`Staleness` is published evidence and NOT a classifier input.** `trailRunReadings` deliberately has no field for it (`trail_run_outcome_test.go:211-214`: `trailBoundFromStart` carries a real duration that bounds nothing, so a classifier able to read a staleness could be tempted to discriminate on it). Carrying it *here* does not admit it *there*; the two types stay separate for that reason.
- **`CarriesTrailer` records the outcome of a pair, not a State.** `finTrailerBuild` gates on `obs.State == trailSeen && obs.Trailer != nil` (`:218`). Once the carrier is forbidden the pointer, that pair is unrecomputable downstream, and `State` alone cannot serve — a `trailSeen` scan with a nil `Trailer` is exactly the case the pair separates. The field is what lets a reader tell "there was no trailer" from "the trailer's fields were empty". No reachable sighting separates the pair from `State` today (`trailWaitForTrailer` fills `Trailer` on every `trailSeen` result), and AC2 does not ask for one; the pair is the shape that stays correct if a later scan learns to return `trailSeen` with a nil pointer.
- **Zero polarity is the safe direction.** `CarriesTrailer`'s zero is false, which routes to the no-decoded-trailer arm where the four scalars are zeroes — an honest nothing-was-measured, the same argument `finGatherInputs.PyryExited` makes for itself (`:209-213`). An incompletely-filled carrier degrades to a named nothing rather than to a claim.
- **`StopReason` is the one model-influenced field, and it crosses uncapped.** `wireFields` derives `Subtype`, `IsError` and `TerminalReason` from a single `ExitReason` — `TerminalReason` is pyry's own synthesis and claude never emitted it — while `StopReason` is forwarded from the model's last message unvalidated (`emitter.go:210`). `finding_trailer_evidence_test.go:114-124` states this for the published record; **state it here too**, in the same words and for the same purpose: so a later sweep author does not plant a needle in a field the carrier must carry verbatim, and so the exposure is inherited knowingly rather than by omission. Capping it is out of scope, exactly as it is at `:122-123`.
- **JSON tags, mirroring `finTrailerRecord`'s keys, and no `omitempty` anywhere.** Tags because `encoding/json` renders a `time.Duration` as a bare nanosecond count and the unit belongs in the key — the reason `trailObservation:133-135` and `finTrailerRecord:147` both give. Key names mirrored so #1308's move onto the carrier is a rename-free projection. No `omitempty` for `finTrailerRecord:102-110`'s reason: the discriminator must always be present beside the four, and dropping a `false` `IsError` or an honest zero `Staleness` collapses distinctions the type exists to keep.
- **No `Detail`.** Nothing needs one — the published Detail is `finTrailerBuild`'s and stays there. This is a deliberate omission and the doc should say so, because it is what discharges AC4's headroom clause structurally: with no formatted string on the carrier there is no #1284-shaped budget for a leak to hide behind. **Do not add one.**

### The fill

Inside `finGatherReadings`, from the **same** `obs` that already fills `readings.BoundFrom` and feeds `trailGate` (`:304-306`). One `trailWaitForTrailer` call; no second scan, no second wait, no `time.Now()` of the gather's own.

Inline in the gather body rather than behind a `finSightingFrom` constructor: funnelling the whole composition through one function is what makes it checkable in one place (`:258-261`), and a constructor would add a symbol whose tests either duplicate AC1/AC2 or do not exist.

Shape — ~10 lines:

```go
sighting := finSighting{State: obs.State, BoundFrom: obs.BoundFrom, Staleness: obs.Staleness}
sighting.CarriesTrailer = obs.State == trailSeen && obs.Trailer != nil
if sighting.CarriesTrailer {
	// the four scalars, copied out
}
```

The `State` operand goes **first** and Go's `&&` short-circuits left to right, so the ordering is a property of the source rather than of a comment — deliberately identical to `finTrailerBuild:218`, because #1308 moves that builder onto this carrier and the two computations must agree.

### The signature and the cascade

`func finGatherReadings(in finGatherInputs) (trailRunReadings, finAttributeRecord, finSighting)` — **appended**, so no existing binding moves and each of the seven call sites takes one more token. Six take `_` (nothing consumes the carrier yet); `finding_run_gather_test.go:964` takes it, because AC4 sweeps it.

**`finding_run_gather_test.go:204` is not a call site — do not edit it.** It is prose inside `finGatherInputs`' doc comment showing the positional form the struct replaced; adding a return value to it would corrupt an argument about parameter shape.

### The doc claim that stops being true

`finGatherReadings`' § "What it returns, and what it deliberately does not" (`:263-271`). **Check each clause; they do not all fail the same way, and do not paste the old phrasing forward.**

| Clause | Verdict | Obligation |
|---|---|---|
| `:265` "The trailer observation is a FUNCTION-LOCAL INTERMEDIATE and is never returned" | **Stays true**, now incomplete | Extend, do not invert. The observation is still not returned and `.Trailer`/`.Line` are still unreachable from everything that is — but the sighting's *measurements* now leave the function, on a value carrying neither. |
| `:268-269` the `trailRigGather` comparison | **Narrows** | The divergence is no longer "this one withholds `Staleness`" but "this one withholds the observation and copies out what a reader needs". |
| `:269` "nothing here [needs `Staleness`]" | **Falsified** | This is the clause that must change. `Staleness` travels on the carrier from this ticket onward. |
| `:270-271` "`Staleness` is NOT a classifier input and must not be used as one" | **Stays true** | Must survive the rewrite. Carrying it as published evidence is not admitting it as an input. |

The rewritten block must also say **why the line and the decoded-trailer pointer are still withheld** — that is still true and it is the whole point of copying scalars out. `resultTrailer` is not merely "the pointer": it carries `PermissionDenials *[]json.RawMessage` (`tool_loop_test.go:199`), raw bytes no cap applies to.

The constants block's separate claim at `:129-135` is **not** falsified by anything here — every row still pre-seeds or aborts immediately. Leave it alone; #1310 owns it.

### The header's licence

The file header's Fatalf content rule (`:107-113`) ends: *"Printing a whole readings or record IS safe, and only because `TestFinGatherReturnsNoCapturedBytes` proves it."* The new tests print a whole **sighting**, which is neither. Extend that sentence to cover it, and add the sighting's scalars to the MAY-name list. The licence genuinely extends because AC4 extends the sweep that grants it — which is precisely why the two edits belong in the same change. Fold this into the header block; **do not add a fifth AC for it.**

## Concurrency model

Unchanged. The carrier is a value copy of data `trailWaitForTrailer` has already returned: no new goroutine, no new shared state, no new lock, no clock reading. `trailWaitForTrailer` reads `stdout.Bytes()`, which returns a copy under the buffer's mutex (`background_trigger_probe_test.go:736-742`), so the scan always runs over a private snapshot — the property that already licenses `finGatherNegativeInputs` to share one buffer across two gather calls (`:756-763`). New tests must not introduce a second buffer-sharing pattern without that same argument; the simplest course is a fresh `probeSyncBuffer` per subtest, which is what `finGatherCases`' rows already do.

## Error handling

The carrier has no reject arm and no error return, and that is deliberate. It has exactly one branch — whether the sighting carried a decoded trailer — and both sides are records rather than failures. This matches the family's contract: `trailScan`, `trailGate`, `trailAdmitAttribution`, `trailClassifyRun` and `finTrailerBuild` are all pure, take no `*testing.T`, and never fail a test, because an instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn.

The two malformed-input classes are already answered upstream and reach the carrier as ordinary values: an aborted scan arrives as `State == trailAborted`, `BoundFrom == trailBoundNone`, `Staleness == 0`, `CarriesTrailer == false`; an absent trailer as `trailAbsent` with the same bound and the same zeroes. The gather validates neither, for the reason it validates no other reading (`:340-345`): a second opinion here repairs exactly the records the classifier's contract checks exist to reject.

## Testing strategy

Four claims, each as bullet scenarios. Every row is offline: no live claude, no credentials, no daemon, no turn, no `t.Skip`, and **no row waits out `finGatherTrailerWait`**.

### AC1 — the carrier is filled from the sighting the classification used

One gather call over a pre-seeded `trailFixtureTrailer` buffer (first poll hits):

- The carrier's `BoundFrom` equals the returned `trailRunReadings.BoundFrom`. Both read `trailBoundFromStart` on this row.
- `Staleness` is **non-zero and below `finGatherTrailerWait`** — pinned as *filled from this sighting rather than left behind*. `trailRunReadings` deliberately carries no staleness field, so there is no counterpart to agree with; this is the substitute claim, and the AC says so.
- The carrier's `State` is `trailSeen`, tying the row to the fixture that produced it.

**Be honest about what this shows.** Agreement on the discriminator is what a single call *can* show: a second `trailWaitForTrailer` over the same buffer would also return `trailBoundFromStart`, so this row does not go red against a second scan. The row that would is **#1310's**. Do not build a miss-first buffer or a timing rig here — that is the other ticket's fixture and its wall clock.

### AC2 — the decoded discriminator and the four scalars

A two-row table. Neither row needs a new fixture and neither costs wall clock:

- **Decoded arm** — seed `trailFixtureTrailer`. Expect `CarriesTrailer == true` and the four scalars equal to the shipped decode's.
- **No-decoded-trailer arm** — seed `trailPaddedTrailer(trailOverlongPad)`, `finGatherCases()`' C4 row (`:413-423`). `trailWaitForTrailer` returns from an aborted scan **immediately** because abortion is monotone. Expect `CarriesTrailer == false` and the four at their zero values; `State` is `trailAborted`, `BoundFrom` is `trailBoundNone`, and `Staleness` is legitimately zero — **AC1's staleness pin does not belong on this row.**

**Do not reach for a genuinely absent trailer.** That arm polls until `finGatherTrailerWait` and burns 10 s, which is the rule the constants block at `:129-135` exists to state.

Shape the expectation so both arms share one comparison block: recompute `scan := trailScan(seed)` in the test, take `want := *scan.Trailer` on the decoded arm and the zero `resultTrailer{}` on the other, then compare the carrier's four fields against `want`'s. This makes "their zero values" exact rather than four typed-in literals, and it follows the file's own recomputation idiom (`finGatherAssertContract`'s C2 at `:502`). Per-row premises, each of which turns a fixture edit into a named failure:

- `scan.State` equals the row's expected state.
- `(scan.Trailer != nil)` equals the row's expected discriminator — tying the row's expectation to the shipped scan rather than to a literal.

**What this row is and is not.** Over the shipped in-cap fixture the decoded arm is a **fill check and nothing more**: the capped copy and the full line are the same bytes there, so it cannot and does not claim which of the two the implementation read. That discrimination is #1310's. Equally, no reachable sighting produces `trailSeen` with a nil `Trailer`, so the inconsistent pair is not exercised — that is a hand-built fixture's case and nothing the gather can emit.

### AC3 — the carrier reaches none of the three types

Reuse `finRecordInputReaches` (`finding_run_record_test.go:731`) — same package, same build tag. **Do not write a second walker**; the increment here is the third forbidden type, not a new traversal. It already follows struct fields, slice and array elements, pointers, and map keys and values, so naming `resultTrailer` catches a `*resultTrailer` too.

- Walk `reflect.TypeOf(finSighting{})` against `trailObservation`, `trailScanResult` and `resultTrailer`; each reachable type is one `t.Errorf` naming which and why.
- Scope is the carrier alone. `trailRunReadings`' own non-reachability is argued at `trail_run_outcome_test.go:208-214` and `finRecordRun`'s is pinned by `TestFinRecordEmbedsTrailerRecordWhole`; restating either would be scope creep.
- The failure message must say what a hit would cost, not merely that it happened: taking any of the three promotes `.Line` (verbatim model output) or the `*resultTrailer` (and with it uncapped `PermissionDenials`) back into the caller's reach, plus the panic-on-unchecked-deref obligation the discriminated optional imposes.

This is proven by **walking types** precisely so that a later edit adding a field carrying any of them one level down fails too, rather than by asserting over a single instance.

### AC4 — the sweep covers the third return

`TestFinGatherReturnsNoCapturedBytes` (`:959`) already seeds `finGatherNeedleTrailer` on stdout and a needle-bearing anchored reap line on stderr, asserts three premises, and marshals both returns from one loop over a `{what, value}` slice (`:1010-1016`).

- Take the third return at `:964` and add **one row** to that slice. Marshalled, checked for the needle, walked for forbidden keys — the existing loop body is unchanged.
- The three existing premises stay, unchanged, including the needle's survival of the cap asserted on **`trailScan(seed).Line`** (`:993`) — on the scan result, never on the input.
- **Do not swap the fixture.** `finGatherNeedleTrailer` renders 380 bytes with the needle at `[97,139)`, wholly inside the 512-byte cap; `trailPaddedTrailer` places the needle past it deliberately and its `error_max_turns` subtype would trip this test's own gate premise (`:158-177`).
- **No headroom assertion is owed**, because the carrier carries no formatted Detail. If a Detail is ever added, #1284's rule applies in full and the headroom must be asserted **per row on the output** the way `finding_trailer_evidence_test.go:648-662` does. The correct move today is not to add one.

Expect this row to pass structurally: `resultTrailer` has no `result` member, so no plant in the trailer's `result` field can reach the carrier through the decode, and the carrier holds no `Line`. That is the property being claimed, not a vacuity — the row is a live guard against a *future* field, which the marshal sweep would catch by bytes and the key walk by name, and which AC3 catches by type. **Do not invent a plant to make it "bite"**; a plant in the four decoded scalars would be pinning against AC2 rather than for it, exactly as `finding_trailer_evidence_test.go:616-622` argues one tier down.

### Verification

`make check` and `make build` never compile `e2e_realclaude`-tagged files, so a PR whose whole diff sits under that tag is a vacuous green. Run both with the tag and expect a **PASS/SKIP split**, not all-PASS:

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/...
go test -race -tags e2e_realclaude ./internal/e2e/realclaude/...
```

## Open questions

1. **`Staleness > 0` and clock granularity.** On the seeded row, `Staleness` is `now.Sub(start)` across `start.Add(timeout)`, a var declaration and loop entry — tens of nanoseconds on a ns-granularity monotonic clock, so non-zero holds on Linux and macOS. If it ever flakes, **the remedy is not `>= 0`**: a left-behind field is exactly zero, so the weakened form is vacuous. The remedy would be to assert the field against the sighting's own bound origin instead (`BoundFrom != trailBoundNone ⟹ Staleness > 0`), which keeps the claim. Flagged, not designed around — no observed failure.
2. **Whether #1308 wants `ObservedAt`.** Excluded here because `finTrailerBuild` does not read it. If #1308's move surfaces a need, adding one field to the carrier is a one-line change with no reachability consequence — `time.Time` reaches none of the three forbidden types. Not pre-added, per the one-way constraint.

## Security review

**Verdict:** PASS

**Findings:**

- **[1. Trust boundaries]** This ticket *is* a trust boundary, and the design makes it explicit rather than scattered. Untrusted side: model-chosen stdout bytes → `trailWaitForTrailer` → `trailObservation`, holding `.Line` (verbatim model output, OPERATOR-REVIEW-BEFORE-PASTE) and `.Trailer` (`*resultTrailer`, carrying uncapped `PermissionDenials`). Trusted side: `finSighting`, eight scalars. The boundary is one function (`finGatherReadings`) and one assignment block, and downstream callers get a type-system signal rather than a convention — AC3's walk makes reachability enforced rather than reviewed, and it fails a *later* edit as well as this one. No finding.
- **[2. Tokens, secrets, credentials]** The family's live threat is an operator's `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` reaching a public GitHub issue via a pasted artifact. Two channels are relevant and both stay shut: verbatim argv (`pinScan.Matches`, untouched — the gather still takes `[]int`) and the trailer line (`.Line`, not carried, not reachable). **SHOULD FIX, folded into the design:** `StopReason` is forwarded from the model's last message unvalidated (`emitter.go:210`) and crosses the carrier uncapped. It introduces no *new* exposure — `finTrailerRecord` already carries the identical value by #1290's AC2 and capping it is out of scope there — but the carrier must name it in its doc the way `finding_trailer_evidence_test.go:114-124` does, so the exposure is inherited knowingly and so a later sweep author does not plant a needle in a field the carrier must carry verbatim. Written into § The carrier.
- **[3. File operations]** Not applicable, and by design rather than by omission: the increment is a value copy inside an already-shipped function. No path is constructed, no file is opened, and `finGatherNeedles`' path is a match pattern handed to a Go-side matcher at which nothing is ever created (`:146-149`).
- **[4. Subprocess execution]** The gather does exec — `pinScanArgv` shells out to `ps` — but this ticket changes nothing on that leg: no new argument, no new needle, no change to the `[]int`-not-`[]reachProc` obligation at `:279-283`. The carrier is pure over the observation. No finding.
- **[5. Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against a secret anywhere in the increment.
- **[6. Network & I/O]** No network. The one input-size question is already answered upstream and unchanged: `bufio.Scanner`'s 64 KiB default is deliberately not raised (`result_trailer_observation_test.go:161-163`), and an over-long line arrives at the carrier as `trailAborted` — a named record, not an unbounded read. `Staleness` is bounded by `finGatherTrailerWait`, which AC1 asserts.
- **[7. Error messages, logs, telemetry]** **SHOULD FIX, folded into the design.** The file header's Fatalf content rule (`:107-113`) licenses printing "a whole readings or record" and grounds that licence in `TestFinGatherReturnsNoCapturedBytes`. The new tests print a whole *sighting*, which the sentence does not cover — a real gap, since a `%+v` of the carrier prints `StopReason`. The design extends the header's MAY-name list and its licence sentence in the same change that extends the sweep granting it (§ The header's licence). No `t.Fatalf` in the new tests may name `.Line`, a `trailScanResult`'s trailer, or `pinScan.Matches`; none needs to.
- **[8. Concurrency]** No new goroutine, lock, or shared state — see § Concurrency model. The one live hazard is buffer sharing across gather calls, which is sound only because `probeSyncBuffer.Bytes()` returns a copy; the spec directs new tests to a fresh buffer per subtest rather than to a second sharing pattern needing its own argument. No finding.
- **[9. Threat model alignment]** The relevant threat is the family's own — captured bytes reaching a public issue through the published artifact — and the carrier sits directly on that path once #1308 lands. Addressed on both axes: structurally by AC3 (no forbidden type is reachable, now or after a later edit) and by bytes by AC4 (the marshal sweep plus the recursive forbidden-key walk). Out of scope and named: capping `StopReason` (inherited from #1290, unowned); retiring the now-vacated downstream sweeps (**#1308**'s AC4); proving the carrier reports the miss bound and reads the full line rather than the capped copy (**#1310**).

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
