# #1458 — `finGatherReadings` carries the pinned-pid sighting route's pid read from its caller

**Size:** S (confirmed, not overridden). **Security-sensitive:** yes — review pass at the end.
**Everything is offline.** No live claude, no credentials, no `make e2e-realclaude`.

---

## Files to read first

All paths are `internal/e2e/realclaude/` unless stated. Resolve each symbol with
`codegraph_node` / `codegraph_search`; do not go hunting by line number.

| File → symbol | What to extract |
|---|---|
| `finding_run_gather_test.go` → `finGatherInputs` | The seven fields today, the **§ "All three staged fields keep the readings' own names, types and ZERO-POLARITY"** heading, and the `ClaudeState` / `RunnerPath` field docs. The heading is the naming rule the new field obeys, and it is one of the sites AC4 moves. |
| `finding_run_gather_test.go` → `finGatherReadings` | The **§ "The caller's obligations"** block (where the new obligation goes), the per-matched-pid `pinReadState` loop (**leave it exactly as it is**), and the two-line "caller's own two readings, carried WHOLE" tail (where the pass-through goes). |
| `finding_run_gather_test.go` → `TestFinGatherRunnerPathDoesNotReachTheScan` | The shape AC2's test mirrors: a shared `finGatherInputs` base, rows differing in one field, premise-asserts before the negative. |
| `finding_run_gather_test.go` → `finGatherRunnerPathCases`, `TestFinGatherGateReadsTheRunnerPathTheRunTook` | The **only** recipe that drives a gather to `trailGateAbsentOwesNone`: seed `trailKeyNamesNoTerminalReason()`, `RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv)`. AC3's test is unreachable without it. |
| `finding_run_gather_test.go` → `TestFinGatherReturnsNoCapturedBytes` | Its **§ "Both plants, and the one that is excluded"** scope note — an AC4 group-2 site. Read it; do **not** add a plant to it. |
| `trail_run_outcome_test.go` → `trailRunReadings` | The `PinnedPid` field doc — why it is taken **whole**, and why it is never folded into `Liveness`. This is the contract the pass-through must not narrow. |
| `trail_run_outcome_test.go` → `trailClassifyRun` | Two regions: the **no-C10 note** (AC4 group 1) and the comment block above the `readings.Ordering.Value == ""` guard, which holds one group-2 paragraph and **two group-3 paragraphs you must not touch**. |
| `trail_sighting_liveness_test.go` → `trailSightingReasonPidReadFailed` | Its doc names *"the zero `\"\"` of an unfilled pinStateOutcome"* among the shapes it answers for. That sentence is why an unfilled pin needs no guard and why the empty case is honest. |
| `process_pin_liveness_test.go` → `pinReadState`, `pinClassifyState`, `pinStateOutcome` | The producer, its seven fields, and the branch that puts **raw `ps` stderr** into `ToolStderr` (capped by `reachCapCommand`, otherwise verbatim). Note that `pinReadState(0)` returns `pinStateInstrumentFailed`, **not** the zero — see the fourth trap below. |
| `finding_exit_path_probe_test.go` → `finExitRunProbe` | Step 3 (`claudeState := pinReadState(h.ClaudePID).Verdict` — the **wrong pid**) and step 4's `finGatherInputs` literal, where the new field is staged. |
| `finding_live_pin_test.go` → `finLivePinReduce`, `finLivePinWantRows`, `finLivePinReading` | The measured cardinality of `h.Pin.PGIDs`: one entry per FIFO-matched row, `finLivePinWantRows == 2` on a healthy run, both entries naming one group; **nil** when the scan failed. This is AC1's selection rule's basis. |
| `finding_live_run_test.go` → `finLiveRunHandle` | Its doc's *"finGatherInputs' seven fields need neither"* bullet — an AC4 group-2 site. |
| `trail_ordering_premises_test.go` → `trailCertifyOrdering` | Its **§ "The premises are supplied, not recovered"** — an AC4 group-3 site. Verify, expect no edit. |

---

## Context

`trailClassifyRun`'s `trailGateAbsentOwesNone` arm consults the pinned-pid sighting
route. The route reads two fields of `trailRunReadings` — `Ordering` and `PinnedPid` —
and today **no shipped gather fills either**. This ticket ships the pid-read half; #1457
ships the ordering half; #1459 sweeps the gather tier for captured bytes once the channel
is open.

The arm guards on `readings.Ordering.Value == ""` **before** consulting the route, and
that guard is single-sided by design. So with the pin staged and the ordering still
unfilled, the arm answers `trailOutcomeVoidSightingRouteNotStaged` exactly as it does
today. **No outcome moves.** AC3 is the check that keeps that true.

What does move is exposure. `pinStateOutcome` has three string-bearing members —
`Detail`, `StateColumn`, `ToolStderr` — and `pinClassifyState`'s instrument-failed branch
puts raw `ps` stderr into `ToolStderr` and folds it into `Detail`. After this change those
bytes really do enter `trailRunReadings`. Nothing renders them today (`trailClassifyRun`
reads `sighting.Value` and `sighting.Reason` and never renders `PinnedPid`), so it is a
**future-edit exposure, not a live one** — and AC4's interim wording is what keeps the
tree's own claims honest until #1459 fences it.

---

## Design

### 1. The field

Add **one** field to `finGatherInputs`, **at the end**, after `RunnerPath`:

```go
// PinnedPid is the pinned-pid read, TAKEN BY THE CALLER after pyry exited.
PinnedPid pinStateOutcome
```

Name and type are not a choice. `finGatherInputs`' own § heading states the rule —
staged fields keep the readings' own names, types and zero-polarity — and the destination
is `trailRunReadings.PinnedPid pinStateOutcome`. Mirroring is what makes the pass-through
readable as a pass-through.

**Append at the end, never mid-struct.** Inserting above `RunnerPath` displaces in-file
refs below it. (Measured: this file's cite tail below the struct is empty — see
§ *Two corrections to the ticket's own premises* — but the append rule costs nothing and
keeps it that way.)

The field's doc carries, at minimum:

- **Its one admissible producer is `pinReadState`, over a pid from the caller's pinned
  set, read after pyry exited.** Never `trailSightingPin` — that is a fixture constructor
  whose own doc says it sets no `StateColumn` and no `ToolStderr`, which is exactly the
  shape a live failing read *does* produce.
- **The selection rule, stated here and not only at the caller** (AC1 requires this): the
  pinned set is a slice and this field is one outcome, so the caller reads the **first**
  entry when the set is non-empty and takes **no read at all** when it is empty. On a
  healthy run `h.Pin.PGIDs` carries two entries naming one detached group — undeduped by
  design, per `finLivePinReduce` and `TestFinLivePinReduce`'s raw-projection subtest,
  which asserts `len == finLivePinWantRows == 2` with both entries equal. On a failed scan
  it is nil.
- **The zero is admissible and is the honest report.** A zero `pinStateOutcome` carries
  `Verdict ""`, which `trailSightingReasonPidReadFailed`'s doc already names among the
  shapes the route answers for. That is why the empty case needs no invented value and no
  contract check.
- **It travels whole.** Narrowing to `Verdict` would leave #1459's needle no route to
  travel and make that sweep unbuildable — the reason `trailRunReadings.PinnedPid`'s own
  doc already gives for taking it whole.
- **The gather validates nothing here** — a statement about the gather, not a licence for
  its caller. Same sentence `ClaudeState` and `RunnerPath` already carry.

### 2. The pass-through

In `finGatherReadings`, beside the existing `readings.PyryExited` / `readings.ClaudeState`
tail:

```go
readings.PinnedPid = in.PinnedPid
```

Whole value, no default, no zero-value rewrite, no `pinIsVerdict` call, no normalisation —
the identical doctrine the two lines above it state, and for a stronger reason: there is
no contract check over `PinnedPid` (the no-C10 note), so a "repair" here would be
unreviewable by any downstream check.

**The existing per-matched-pid `pinReadState` loop is untouched, and no second
`pinReadState` call is added inside the gather.** The gather's in-body read is over
`scan.Matches` at gather time; this route's read is over a caller pid *after pyry exited*.
Both operands differ. Reusing one for the other answers at the wrong instant.

Add one bullet to § *The caller's obligations*, beside `pinned` and `RunnerPath`:
`PinnedPid` is read **after pyry exited**, an instant this function does not observe and
cannot wait for, so the timing is the caller's obligation and not a check.

### 3. The call site — `finExitRunProbe`

The reduction happens here and never inside the gather. That is #1452's `RunnerPath`
doctrine one field along; the reason here is **timing** rather than credentials.

Between step 3 and step 4, take the read with an explicit empty guard, then stage it in
the existing named-field literal beside `Pinned` and `RunnerPath`. Shape:

- `var pinnedPid pinStateOutcome`
- `if len(h.Pin.PGIDs) > 0 { pinnedPid = pinReadState(h.Pin.PGIDs[0]) }`
- `PinnedPid: pinnedPid,` in the literal.

The comment at the staging site states: which pid is read (the first FIFO-matched row's
process group), why the first is not arbitrary (both entries name one group on a healthy
run), what happens when there is none, and that the read is taken **after** the exit wait.

**No dedupe, no cardinality assertion here.** Whether the set has the expected size is
`finOutcomeStaging`'s count arm's business, checked against `finLivePinWantRows`. A second
opinion at this line would duplicate a gate that already exists.

### 4. Four traps, in the order a developer meets them

1. **The in-gather read.** `finGatherReadings` already calls `pinReadState` once per
   matched pid. That doctrine is `Liveness`' and does not generalise: different operand,
   different instant. Do not reuse it; do not add a second call inside the gather.
2. **`Liveness` instead of `PinnedPid`.** `Liveness` is `[]pinStateOutcome` and
   `pinReadState` returns exactly this type, so `readings.Liveness = append(...)` is
   type-correct, one line from the right answer, and silent. It is also wrong on the
   merits: `Liveness` is the argv scan's per-pid set, feeds `tdnVerdictSummary` into the
   published record, and goes blind once claude exits and the group re-parents to init.
   **AC2's test is the deterministic net.**
3. **The wrong pid at the call site.** `finExitRunProbe` already calls `pinReadState` at
   the right *instant* — `pinReadState(h.ClaudePID).Verdict` for `ClaudeState`. That
   outcome is over `h.ClaudePID`, an `int` naming **claude itself**. This route's pid
   comes from `h.Pin.PGIDs`. Reusing that outcome, or re-reading `h.ClaudePID` whole, is
   type-correct, adjacent to the line being edited, and wrong.
4. **`pinReadState(0)` is not the zero.** On an empty pinned set the guard must **skip the
   call**. `pinReadState` with a non-positive pid returns `Verdict:
   pinStateInstrumentFailed` with a Detail — a member of the closed verdict space, which
   the route reads as *the instrument ran and failed*. The zero `pinStateOutcome`
   (`Verdict ""`) reports *no instrument was there*. They route to the same reason today
   but say different things, and only the second is true.

### 5. What is deliberately not staged

`finStageRun` (`finding_stage_held_group_test.go`) is a fixture helper reached from
**two** enclosing tests — `TestFinStageRealHeldGroupFillsTheGather` and
`TestFinStageRigHardcodingsCannotReachTheFinding`. It stages a rig-held group, and there
is no pyry there to have exited, so there is no instant at which this reading would be
honest. It stays unstaged and its zero is the correct report. Say so in one line rather
than leaving a reader to wonder.

**No call-site cascade.** `finGatherReadings` has 16 call sites (a 17th `grep` hit is a
comment quoting the pre-#1302 positional signature). Every one uses a named-field literal
— verified, not assumed — so the new field is additive and forces **zero** edits.

---

## Data flow

```
h.Pin.PGIDs ──(finLivePinReduce: one entry per FIFO-matched row)──┐
                                                                  │  finExitRunProbe, AFTER the exit wait
                                                                  ▼
                                                   pinReadState(PGIDs[0])   ← empty set ⇒ no call, zero value
                                                                  │
                                                                  ▼
                                       finGatherInputs.PinnedPid (pinStateOutcome, whole)
                                                                  │  finGatherReadings: pass-through, no validation
                                                                  ▼
                                       trailRunReadings.PinnedPid (pinStateOutcome, whole)
                                                                  │  trailClassifyRun, trailGateAbsentOwesNone arm
                                                                  ▼
                                       Ordering.Value == ""  ──yes──▶ run-void-sighting-route-not-staged
                                                                       (route never consulted — TODAY'S ANSWER)
                                                                  │no  (#1457's half)
                                                                  ▼
                                                   trailEstablishSighting(Ordering, PinnedPid)
```

**Concurrency:** none introduced. `pinReadState` is a synchronous `exec` with
`reachPSTimeout`; the gather spawns no goroutine and the new field crosses no boundary.
The only ordering constraint is the one the caller already obeys: the read follows the
exit-wait receive. It must **not** be hoisted above the `select` — `h.ExitStatus` is
race-protected by that receive, but the pin read is not race-relevant and hoisting it
would simply take the reading at the wrong instant.

---

## Error handling / failure modes

| Failure | Behaviour | Why it is the right answer |
|---|---|---|
| Pinned set empty (scan failed) | No read; zero `pinStateOutcome`, `Verdict ""` | `trailSightingReasonPidReadFailed`'s doc names this shape. Reporting an instrument failure instead would claim a read happened. |
| `ps` fails / times out | `pinClassifyState` answers `pinStateInstrumentFailed`, `ToolStderr` carries capped `ps` stderr | The instrument's own contract; the route has a reason for it. |
| Reading arrives malformed | **Nothing catches it.** `PinnedPid` remains the one classifier input with no contract check | Deliberate — the no-C10 note. Which is precisely why the pass-through's shape must be right, and why AC2/AC3 are the nets. |
| Value narrowed to a string by a later edit | AC2's whole-value equality fails to compile / goes red | The test's parameter type is the enforcement. |

**Do not add a C10.** The no-C10 note's *conclusion* survives this change on its own legs;
only its stated *premise* moves. See the sweep below.

---

## Testing strategy

Two new tests in `finding_run_gather_test.go`. Both offline; neither needs claude,
credentials or network. Suggested names, adjust to the file's idiom:

### `TestFinGatherPinnedPidDoesNotReachTheLiveness` (AC2)

Mirrors `TestFinGatherRunnerPathDoesNotReachTheScan`: one shared `finGatherInputs` base,
rows differing **only** in the new reading.

- Base: `Needles: finGatherNeedles(t)` (a `t.TempDir()` path nothing is staged at), a
  seeded stdout, `PyryExited: true`. Because the needle set matches nothing, `Liveness`
  must be empty on **every** row — which is exactly what a build that appended the reading
  to `Liveness` would break.
- **Premise asserts first, before any negative** — the file's own control discipline:
  `!readings.ArgvScanErrored` and `readings.RowsScanned > 0`. Without them a broken `ps`
  makes the whole test vacuous.
- Rows vary the reading's *shape*, so the whole value is proven to cross:
  - the zero `pinStateOutcome`;
  - a running verdict carrying a `StateColumn`;
  - an instrument-failed verdict with a **non-empty `ToolStderr` and `Detail`** — the
    string-bearing members, which is what #1459 will need a route for.
- Per row assert: `readings.PinnedPid == tc.reading` (whole-value equality —
  `pinStateOutcome` is comparable), `len(readings.Liveness) == 0`,
  `readings.MatchCount == 0`, and `readings.RowsScanned` unchanged across rows.
- Rows are **fixtures**, not live `pinReadState` calls. A live read is non-deterministic
  and this test's subject is routing, not the producer. The
  produced-by-`pinReadState` constraint binds the live caller, not a test row.
- **No needle plant and no captured-bytes sweep here.** That is #1459's.

### `TestFinGatherHalfStagedRouteMovesNoOutcome` (AC3)

Drives the shipped classifier over two gathers that differ **only** in the new field.

- Both rows use the one recipe that reaches the arm: seed
  `trailKeyNamesNoTerminalReason()`, `RunnerPath: tdnRunnerFromArgv(tdnFixtureStreamArgv)`,
  `PyryExited: true`, `Ordering` left unfilled (it has no `finGatherInputs` field yet, so
  this is automatic — state that, so #1457 knows to revisit).
- **Premise assert:** `readings.Gate.Value == trailGateAbsentOwesNone` on both rows. Skip
  this and the rows never reach the arm and the comparison proves nothing.
- Row A stages a non-zero `PinnedPid`; row B stages nothing.
- Assert both `trailClassifyRun` outputs have
  `Value == trailOutcomeVoidSightingRouteNotStaged`, **and** that the two whole
  `trailRunOutcome` values are equal (`==` — every field is string/int/bool). Whole-value
  equality is what catches a pass-through that let the single-sided guard be defeated,
  including through `Route`, `RouteReason` or `Detail`.

### Verification commands

These files are behind the `e2e_realclaude` build tag, which **`make check` does not
compile**. Green there proves nothing about this diff.

```bash
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -tags e2e_realclaude -run 'TestFinGather' ./internal/e2e/realclaude/
go test -tags e2e_realclaude -run 'TestTrailRun' ./internal/e2e/realclaude/
make cite-guard
```

---

## AC4 — the comment sweep, anchored on symbols

`cite-guard` bans new symbol-replaceable line citations, and four of the group-1 sites
moved by +2 between `45cd2a8` and `e95f61e`. Anchor on the claim and the symbol, never on
the line. **Do not add new `:NNN` citations while editing these.**

The discriminating anchor for group 1 is the substring `gather` in
`trail_run_outcome_test.go`, **matched case-insensitively**: 9 lines collapsing to 7
regions. Case-sensitive lowercase matches 8 (it misses the one inside `finGatherInputs`)
and reaches the same 7 regions — a sweeper who counts 8 has not found a missing site.
Verified on `e95f61e`.

### Group 1 — seven regions that go outright **false** (`trail_run_outcome_test.go`)

Each phrases the claim differently, so a grep for one phrasing misses the rest.

| Anchor | The claim that dies |
|---|---|
| `trailOutcomeVoidSightingRouteNotStaged`, doc ¶1 | "no shipped gather fills either of the route's two inputs" |
| `trailOutcomeVoidSightingRouteNotStaged`, doc ¶ *"Deliberately NOT trailOutcomeOutOfContract"* | "an unfilled input on a gather that never stages it" |
| `trailClassifyRun`, the **no-C10 note** | "No shipped gather stages either input … `finGatherInputs` carries no field either one could arrive on" |
| `trailRunAbsentOwesNoneReadings`, doc | "the shape every shipped gather produces: neither field is filled anywhere" |
| `trailRunSightingEstablishedReadings`, doc | "No shipped gather stages either field, so this shape is a fixture rather than a reading any run produces today" |
| `trailRunCases`, the row named *"an unstaged sighting route is the absence of an instrument, not a reading it produced"* | "the ONLY shape any shipped gather produces" |
| `TestTrailRunComposesUnderAnAbsentReasonOnAPathThatOwesNone`, doc | "which is what every shipped gather produces" |

**The no-C10 note is the load-bearing one.** It argues no contract check is owed over
`Ordering` or `PinnedPid` because such a check "would answer run-out-of-contract ON EVERY
RUN THAT EXISTS TODAY". This change falsifies that premise **for the pin**. The conclusion
survives on its own legs — the note's next paragraph already says
`trailSightingReasonPidReadFailed`'s doc names the zero `""` among the shapes the route
answers for, so an unfilled pin has an argued home *inside* the route. **Do not add a C10
for the pin.** State what is now true about why it is still not owed, and note that
`PinnedPid` remains the one classifier input with no contract check over it.

### Group 2 — six sites the `gather` anchor cannot reach

The claim that moves here is mostly a **count** or a **basis**, not a reachability
sentence. A sweep that stops at the anchor ships all six stale.

1. **`TestTrailRunOutcomeCarriesNoCapturedBytes`, § *"What the two new-input blocks ARE,
   stated rather than overclaimed"*.** It survives *literally* — the arm reads
   `sighting.Value` / `sighting.Reason`, and the classifier never renders `PinnedPid`. Its
   **basis** moves: today the claim is free because nothing fills the inputs; after this
   change live `ps` bytes really do enter `PinnedPid` and only the non-rendering stands
   between them and a public artifact. Its closing *"It is not a claim that a leak exists
   to be caught"* is the sentence to revisit. Write the **interim** truth: the route is now
   open and **not yet swept at the gather tier**, with **#1459** named as the ticket that
   owes that sweep. #1459 updates this paragraph again once the fence exists.
2. **`trailClassifyRun`, the *"Testing both fields with &&"* paragraph** above the
   `readings.Ordering.Value == ""` guard. Also survives literally; its basis moves the same
   way. It argues against an `&&` guard by describing "a half-staged pair — ordering
   unfilled, pin filled" — and after this change **that pair is what every live run through
   `finGatherReadings` produces** rather than a shape reasoned about. Say so. This is the
   load-bearing justification for AC3.
   **⚠ This paragraph shares one comment block with two group-3 paragraphs.** Edit the
   `&&` paragraph only; leave its neighbours alone.
3. **`finGatherInputs`, "Across seven positional arguments…"** — a counterfactual about the
   rejected positional signature. The number is derived from the field count. Seven → eight.
4. **`finGatherInputs`, § heading "All three staged fields…" and its bullets.** Three →
   four, plus a **fourth zero-polarity bullet**: the zero `pinStateOutcome` carries
   `Verdict ""`, which has an argued home inside the route
   (`trailSightingReasonPidReadFailed`'s doc). The closing "an incompletely-filled
   `finGatherInputs` degrades honestly" stays true **only because** of that, so state it —
   otherwise it becomes an unbacked assertion over a fourth field. **Keep the growth
   minimal**: one bullet and the two numbers.
5. **`finLiveRunHandle`, the *"No workdir and no session id"* bullet** in
   `finding_live_run_test.go`: *"finGatherInputs' seven fields need neither — six until
   #1452 added the runner-path reading, which the consumer fills by reducing a field this
   handle already carries."* The surrounding argument survives — the new field's producer
   also reduces a field the handle already carries, `h.Pin.PGIDs` — but the count is stale
   twice over. Make it eight and name the pid read alongside the runner path.
6. **`TestFinGatherReturnsNoCapturedBytes`, § *"Both plants, and the one that is
   excluded"*.** It says the needle goes into "the two inputs that could carry captured
   bytes into the returns". After this change there is a **third**, and `PinnedPid` is a
   return of that very gather — so the note overstates its own coverage at exactly the tier
   where the channel opens. Same interim wording as site 1: name the third input, say no
   sweep covers it yet, name **#1459**. **Do not add a third plant** — #1452's precedent is
   that a new route gets its own sibling test, and #1459 re-points this note at its own
   sweep when it lands.

### Group 3 — verify, expect **no edit**

Carry these so the survivors are a checked result rather than an omission. A sweeper who
"fixes" them makes the tree wrong.

- **`trailClassifyRun`**, the *"the shape every run reaching here produces today"* and
  *"'' is what an UNFILLED field carries and is the only shape any producer emits"*
  paragraphs. Both are about the **ordering** input, which this ticket does not stage. They
  stay true here and go false under **#1457**. Editing them now strands a false claim in
  the other direction.
- **`finGatherInputs`**, *"Since #1452 `ClaudeState` and `RunnerPath` are both string and
  adjacent, so it does [transpose]"*. The new field is a `pinStateOutcome`; it cannot
  transpose with either. Unchanged.
- **`trailCertifyOrdering`**, § *"The premises are supplied, not recovered"* — *"No shipped
  gather records 'the hold was still held for the whole of the wait'"*. Its subject is the
  `holdHeld` **premise**, which reaches `trailCertifyOrdering` at the call site;
  `trailRunReadings` gains no premise field. Unchanged. (#1453's original enumeration
  listed this as a site; it is not one.)

### Out of the developer's scope

- **`docs/knowledge/features/e2e-realclaude.md`, § *"What's there today"*** carries the
  same claim (*"unvalidated by the contract block — no shipped gather stages either
  field"*). Verified as the only live site in that file: the other matches are under
  `## Related`, the per-ticket changelog, historical by construction. **That file is the
  documentation phase's to write.** Recorded here so the sweep is not later found to have
  stopped at the package boundary.
- **`finding_live_run_test.go`'s `"NEVER len(h.Pin.PGIDs) (1 on a healthy run: …)"`
  parenthetical** is imprecise — it describes the count of *distinct groups*, while
  `len(h.Pin.PGIDs)` is **2**. Pre-existing, about a different subject (which count the
  gate's arm wants), **out of scope — do not fix it here.** It is load-bearing for AC1's
  selection rule only in that a developer who trusts it writes an unguarded index against a
  slice that is nil whenever the scan failed. Measure off `finLivePinReduce` and
  `TestFinLivePinReduce`'s raw-projection subtest instead.

---

## Two corrections to the ticket's own premises

Both were measured, not assumed. Both change what the developer should spend turns on.

### 1. `finding_run_gather_test.go`'s **inbound** cite tail is empty — but that is one file, not the change.

> **Amended after code-review of PR #1460 (2026-08-11).** The measurement below is sound
> and is kept. Its **scope** was wrong as headlined: it measures the inbound tail of
> `finding_run_gather_test.go`, the file gaining the *field*. The file the AC4 comment
> sweep actually *grows* is `trail_run_outcome_test.go` — the most-cited file in the
> package — and its inbound tail was never measured. It is **not** empty: the shipped
> change grows it by +51 lines cumulative across 11 hunks, displacing **182 citation
> endpoints across 93 comment lines in 18 files** (16 in `internal/e2e/realclaude/`, plus
> a bare range in `cmd/cite-guard/main.go`). `finding_live_run_test.go`'s +1 hunk is in
> that count.
>
> **The rule to carry forward:** measure the inbound tail of *every* file the change
> grows, chosen by which file the diff adds lines to — not by which file the ticket is
> named after. `cite-guard` does not close this gap: it bans *new* symbol-replaceable
> cites, it does not detect existing cites going stale, so a displaced tail reaches
> review green.

The ticket's Technical Notes say the `finGatherInputs` doc edit "grows a comment mid-file"
and that "14 live-scope cites (shipped comments plus `docs/knowledge/`) point into that
file below line 259 … Budget for it."

Measured on `e95f61e`, three ways:

- **Zero** `.go` citations anywhere in `internal/` or `cmd/` point into
  `finding_run_gather_test.go` below the growth point.
- **Zero** bare `:NNN` refs *inside* `finding_run_gather_test.go` resolve to this file
  below it — a stateful last-named-file scan resolves every one of them to
  `trail_run_outcome_test.go`, `trailer_admissibility_test.go`,
  `result_trailer_observation_test.go` or `runner.go`.
- The 14 hits the ticket counts are **all** in `docs/knowledge/codebase/<N>.md` — the
  per-ticket changelog, which the ticket itself classifies as *"historical by
  construction"* in the adjacent knowledge-doc note. The note contradicts itself; the
  measurement resolves it.

The scan was run against a known-present control before its negative was trusted: relaxing
the line filter surfaces the two live cites that do exist (both **above** the growth point,
in `docs/knowledge/features/e2e-realclaude.md` and `finding_stage_held_group_test.go`),
so the empty result is a measurement and not a broken grep.

**Consequence:** do **not** re-point `docs/knowledge/codebase/` cites. Editing them is
wrong, not merely wasteful — and a bumped cite re-bumped is #1452's exact failure mode. If
you want to re-verify before trusting this, one command suffices:

```bash
grep -rn --include="*.go" "finding_run_gather_test\.go:[0-9]" internal/ cmd/
```

Every hit it returns targets a line above the struct.

### 2. #1452 is not evidence that this shape ships at S — but the discriminator is measurable.

The ticket cites #1452 as "the measured analogue: same shape, same tier, 11 comments across
5 files plus the implementation, shipped as one S ticket." What actually happened: the
first developer run was killed at its wall and **all work was lost**; the second hit the
wall clock at **92 turns / $15.84** and shipped only via salvage; then two code-review
rounds went entirely on the cite tail, with cites re-pointed the wrong way. #1452's own
architect wrote the same reassurance this ticket does — *"zero call sites are
compile-forced … no red line trips."*

That is a reason to look harder, not to split reflexively. What made #1452 expensive was
**26 external cites across 12 files plus 35 in-file bare refs** — 61 line resolutions.
This ticket's equivalent count is **0**, measured above. The cost driver is absent, and it
is absent as a fact rather than as a framing.

---

## Sizing

**S, confirmed.** Red lines walked against raw counts:

| Red line | This ticket |
|---|---|
| > 3 new files | **0** new files; 4 touched |
| > ~600 LOC total written (prod + tests + helpers + docs) | **≈ 300**: field + doc ≈ 28, `finGatherInputs` doc edits ≈ 14, gather obligation + pass-through ≈ 18, caller ≈ 28, AC2 test ≈ 90, AC3 test ≈ 70, group-1 sweep ≈ +18, group-2 remainder ≈ +30 |
| > 5 new exported types | **0** (nothing exported; one unexported struct field) |
| > 10 consumer call sites needing simultaneous update | **0 compile-forced** — 16 call sites, every one a named-field literal, verified by reading them |
| > 5 acceptance criteria of work | **4** |
| > ~10 error/reject branches | **0** — no state machine, no new branch beyond one length guard |

Edit fan-out honestly counted, **not** re-counted downward: 5 implementation edits, 2 tests
(with their debug cycles), 13 comment sites, 3 group-3 verifications, 4 verification runs.
≈ 30–38 turns. No helper functions, no constructor, no new symbol beyond the two tests.

The one seam a split would follow — deferring the group-2 interim wording — is rejected on
the merits, not on size: those two paragraphs are the honesty obligation for opening the
channel, and deferring them ships a tree that overstates its own sweep coverage at exactly
the tier where `ps` bytes start entering the readings. On a `security-sensitive` ticket
that is the wrong thing to defer. The other candidate seam — splitting the
`finGatherInputs` doc's own field-count edit off the field it counts — would land a struct
whose doc says "all three staged fields" while carrying four.

---

## Open questions

1. **`finStageRun` staging.** This spec says leave it unstaged (no pyry, so no honest
   instant). If code-review disagrees, staging it is one line and a comment — but it would
   need an argument for what instant the reading is taken at.
2. **The empty-set guard has no offline test.** Its only caller is `finExitRunProbe`,
   reachable only from a live-claude run. The guard is held by the field's doc and by
   review — the same fabric the gather's key-name fill obligation already rests on, and
   stated here so a later reader does not over-trust the green. Escalating it would mean
   giving `finExitRunProbe` a testable seam, which is scope this ticket does not have.
3. **#1457 ordering interaction.** Both tickets edit `finGatherInputs` and
   `finExitRunProbe`. No branch overlap exists today (checked: no in-flight feature branch
   touches any of this ticket's four files), but whichever lands second inherits a merge.
   Appending at the end of the struct keeps that merge to a single hunk.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** — **The finding.** This change opens a new boundary crossing:
  `ps` stderr → `pinClassifyState` → `pinStateOutcome.ToolStderr` / `.Detail` →
  `trailRunReadings.PinnedPid`. The boundary is explicit and single: `pinClassifyState` is
  the one place that captures it, and it caps via `reachCapCommand`. The crossing is
  **structurally contained today** — `trailClassifyRun` reads `sighting.Value` and
  `sighting.Reason` and renders `PinnedPid` through nothing, so no artifact and no
  published Detail can reach these bytes. Verified against the classifier rather than
  assumed. Downstream holders are signalled by type (`pinStateOutcome`, whose doc names its
  string-bearing members) and by the field docs this spec mandates.
- **[Trust boundaries]** **OUT OF SCOPE → #1459.** No sweep covers the gather tier for this
  route. The exposure is a **future edit** that interpolates `pin.Detail` or
  `pin.ToolStderr` into a failure message — the natural mistake, and precisely what
  `TestTrailRunOutcomeCarriesNoCapturedBytes`' plant already anticipates one tier up.
  AC4's interim wording at two sites is the interim control: it makes the gap *stated*
  rather than silent, so a future editor cannot read the existing "no live leak route"
  sentences as a discharged obligation. **This is why the group-2 wording is not
  deferrable and why the split was rejected.**
- **[Subprocess / external command execution]** — No finding, but the near miss is worth
  naming. `pinReadState` execs `ps` with `pinStateArgs(pid)` — `-p <pid> -o <fixed
  columns>`, no `sh -c`, no shell interpretation. The pid is an `int` from
  `finLivePinReduce`'s projection of a `ps` row, never a string. The one shape in this
  instrument where an integer could be read as something other than an operand — a negative
  pid rendering as a `-1` **flag** — is rejected before the exec by `pinReadState`'s own
  guard. Trap 4 above (never call `pinReadState(0)` to represent "no read") keeps the
  caller from routing the empty case through that guard and mislabelling it as an
  instrument failure.
- **[Error messages, logs, telemetry]** — No finding. `pinStateOutcome.Detail` is capped by
  `pinDetail`, `ToolStderr` by `reachCapCommand`. `PinnedPid` reaches no artifact writer:
  `finRecordBuild` takes `readings.Liveness`, not `readings.PinnedPid`, and this spec's AC2
  test pins that the new reading never lands in `Liveness` — so the prohibition is enforced
  by a deterministic test and not only by the doc. The `t.Logf` publication channel in
  `finExitRunProbe` is field-by-field and covered by the two existing no-captured-bytes
  sweeps.
- **[Trust boundaries — type-level]** **SHOULD FIX, mitigated, not gated.** `PinnedPid` is
  the one classifier input with **no contract check over it** (the no-C10 note), so nothing
  downstream catches a malformed reading. The structural fix — a narrower type — is
  foreclosed by AC1: narrowing to `Verdict` would make #1459's sweep unbuildable. Mitigated
  by three fabrics of different material: the field doc naming `pinReadState` as the one
  admissible producer, AC2's whole-value equality (which a narrowing edit cannot compile
  past), and AC3's whole-`trailRunOutcome` equality (which catches a pass-through that
  defeats the single-sided guard). Code-review should confirm the pass-through performs no
  normalisation.
- **[File operations]** — Not applicable. This change opens, creates and writes no file.
  The artifact writer's inputs are unchanged.
- **[Cryptographic primitives]** — Not applicable. No randomness, no keys, no comparison
  against a secret.
- **[Network & I/O]** — Not applicable. No socket, no HTTP, no relay surface; the only I/O
  is the existing capped `ps` exec under `reachPSTimeout`.
- **[Concurrency]** — No finding. No goroutine, no lock, no shared mutable state. The one
  ordering constraint (read after the exit wait) is a *correctness-of-instant* obligation,
  not a race: unlike `h.ExitStatus`, `h.Pin.PGIDs` is written before the handle is returned
  and is not touched by the driver's `cmd.Wait` goroutine. Hoisting the read above the
  `select` would take an early reading, not a racy one — worth stating so a later reader
  does not infer a race guarantee this line does not carry.
- **[Threat model alignment]** — The relevant threat is the package's own standing one: an
  operator's `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` reaching an artifact destined
  for a public issue via verbatim `ps` output. This change moves captured bytes one field
  closer to that artifact and leaves the last hop closed. The residual is named, bounded to
  a future edit, assigned to #1459, and stated in the tree itself by AC4.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-11
