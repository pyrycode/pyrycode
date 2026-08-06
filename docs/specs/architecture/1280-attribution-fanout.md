# #1280 — Fan the reap-log attribution across every pinned process group

**Size:** S (confirmed, not overridden). One new file, no production change, no consumer call sites.
**Prefix:** `finAttribute*`. Census at eb33eea: `\bfin[A-Z]` = 0 hits, control `\btrail[A-Z]` = 764. Re-run both before starting.
**File:** `internal/e2e/realclaude/finding_attribution_fanout_test.go` (new, `//go:build e2e_realclaude`).

---

## Files to read first

Read these before writing anything. Every design decision below rests on one of them.

- `internal/e2e/realclaude/trailer_admissibility_test.go:127-160` — the seven admissibility constants and what each one *means*. The selection order below is a total order over exactly these; you cannot argue the order without their Detail prose.
- `internal/e2e/realclaude/trailer_admissibility_test.go:190-197` — `trailAdmitResult`. Documented **trap-free by construction**: two strings, no pointer into either input, no quote of `tdnReapOutcome.Line`. This is why an entry may carry it whole and AC5 stays true by construction.
- `internal/e2e/realclaude/trailer_admissibility_test.go:356-485` — `trailAdmitAttribution`, the whole function. Note precisely which arms are group-dependent (`tdnReapHeldPGIDKilled` / `tdnReapHeldPGIDAbsent`, `:437-484`) and which fire before the verdict is read at all (`certified == ""` at `:413`, budget at `:425`). That split is the argument for why only three of the seven values can ever co-occur in one fan-out.
- `internal/e2e/realclaude/trailer_admissibility_test.go:199-208` — `trailDetail`. Reuse it; do not define a `finDetail` (§ Detail helper below).
- `internal/e2e/realclaude/trailer_admissibility_test.go:504-512` — `trailIsAdmitValue`, the membership predicate. **Call it**, do not re-switch.
- `internal/e2e/realclaude/trailer_admissibility_test.go:530-536` — `trailReapLine(count int, pgids string)`. `pgids` is spliced **raw** at the end of the line; that is what makes AC5's needle position work.
- `internal/e2e/realclaude/trailer_admissibility_test.go:973-1028` — `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`. AC5's test is this shape: premise assertion first, then `json.Marshal` + `bytes.Contains`.
- `internal/e2e/realclaude/teardown_liveness_test.go:112-127` — `tdnReapOutcome`, including `Line` at `:126`. The type the record must **not** carry.
- `internal/e2e/realclaude/teardown_liveness_test.go:144-219` — `tdnClassifyReapLog`. Read `:147` (the `heldPGID <= 1` guard AC3 exists to keep unreached), `:160-167` (anchored-line skip *then* `Line` fill — the AC5 vacuity trap), and `:191-198` (`LineCount == 0` → `tdnReapNoLine`).
- `internal/e2e/realclaude/teardown_liveness_test.go:231-265` — `tdnParsePGIDs`. It stops at the first `]` and, for an **unquoted** value, checks nothing after it. That is the licence for the needle's position.
- `internal/e2e/realclaude/trail_run_outcome_test.go:171-219` — `trailRunReadings`, field by field. `Admit`'s doc comment at `:183-185` states the zero value means "not classified".
- `internal/e2e/realclaude/trail_run_outcome_test.go:390-400` — C3, and `:587-593` — Step 8. The two arms AC4's consumer-side rows land on.
- `internal/e2e/realclaude/trail_run_outcome_test.go:605-631` — `trailRunWellFormed()` and `trailRunProofReadings()`. The vary-one-thing base, and the precedent for staging an `Admit` on a `trailRunReadings` by literal.
- `internal/e2e/realclaude/trail_run_outcome_test.go:1132-1188` — `TestTrailRunOutcomeCarriesNoCapturedBytes`. The structural half (decode to `map[string]json.RawMessage`, reject `command`-shaped keys) is the pattern AC5's test extends.
- `internal/e2e/realclaude/process_pin_liveness_test.go:120-135` — `pinScan`, and `background_reach_probe_test.go:162-168` — `reachProc`. Read them to see what you are **not** taking as input: `Matches` holds `Command`, verbatim argv.
- `internal/e2e/realclaude/result_trailer_observation_test.go:300` — `const trailNeedle`. Reuse it; do not invent a second needle.
- `internal/e2e/realclaude/background_reach_probe_test.go:123` (`reachMaxCommandBytes = 512`), `:945-950` (`reachCapCommand`) — the cap every retained string passes through.
- `internal/agentrun/reap.go:52` and `:64-65` — the skip list, the emit guard, and the slog call `trailReapLine` renders. Read `:52` closely: it skips `pgid <= 1 || pgid == self || pgid == rootPid`. § The filter is exactly `<= 1` explains why only the first clause is mirrored here.

---

## Context

`trailAdmitAttribution` takes **one** `tdnReapOutcome`, which is the classification of pyry's reap log against **one** held process group. The probe does not have one group — `pinScanArgv` returns `Matches` as a slice, deliberately refusing to resolve "the" pid. Reducing that set to the single `trailAdmitResult` that `trailRunReadings.Admit` accepts is new logic, and it is where a wrong rule silently costs the one finding the probe exists to produce.

This ticket builds that reduction and proves it. It is pure over `(stderr []byte, pgids []int, certified string)`: no process, no FIFO, no gather, no live claude, no credentials, no turn. `#1281` builds the gather that calls it and is natively blocked by this.

---

## Design

### The two named conditions

Two constants, in the file's own `finAttribute-` namespace. Both are **record-level conditions**, never selectable values: `trailRunReadings.Admit` accepts only what `trailIsAdmitValue` accepts, and an eighth locally-invented value there lands every such run on `trailOutcomeOutOfContract` via C3.

```go
const (
	// finAttributeGroupUnreportable: a pinned group the reaper can never report.
	// reap.go:52 skips pgid <= 1 before it kills anything, so no reap line can
	// carry one. Surfaced rather than classified: handing it to
	// tdnClassifyReapLog would trip that function's own :147 guard and return
	// tdnReapInstrumentFailed, blaming the instrument for a consumer that failed
	// to capture a pgid.
	finAttributeGroupUnreportable = "fin-attribute-group-unreportable"
	// finAttributeNoGroups: no reportable distinct group remained — a nil input,
	// or every group hitting the condition above. A STAGING FAULT, and the
	// reason Selected is left zero rather than filled with either of the two
	// falsehoods documented on that field.
	finAttributeNoGroups = "fin-attribute-no-groups"
)
```

### The records

```go
// finAttributeEntry is one distinct group's attribution: the pgid it was decided
// for, and the shipped predicate's result. NOTHING ELSE — no tdnReapOutcome
// (its Line is pyry's own stderr), no reachProc (its Command is verbatim argv),
// no command string. That is what makes the no-captured-bytes property true by
// construction rather than by an ordering discipline a later edit can break.
type finAttributeEntry struct {
	PGID  int              `json:"pgid"`
	Admit trailAdmitResult `json:"admit"`
}
```

```go
type finAttributeRecord struct {
	// Conditions holds the finAttribute* names that fired. THE FIELD A CONSUMER
	// BRANCHES ON; the two below say which groups they fired for.
	Conditions []string `json:"conditions,omitempty"`
	// Unreportable is the distinct pgids finAttributeGroupUnreportable fired for,
	// ascending. Present in the record and absent from Entries.
	Unreportable []int `json:"unreportable_pgids,omitempty"`
	// Entries is one attribution per distinct REPORTABLE group, ascending by pgid.
	Entries []finAttributeEntry `json:"entries,omitempty"`
	// Selected is the reduction under finAttributeOrder. THE ZERO trailAdmitResult
	// exactly when Entries is empty, which is exactly when Conditions holds
	// finAttributeNoGroups. A caller must branch on Conditions and must NOT copy a
	// zero Selected into trailRunReadings.Admit: under a certifying gate that
	// reaches trailOutcomeOutOfContract via C3, a caller bug dressed as a reading.
	Selected trailAdmitResult `json:"selected"`
	Detail   string           `json:"detail"`
}
```

### The fan-out

```go
func finAttributeFanOut(stderr []byte, pgids []int, certified string) finAttributeRecord
```

Behaviour, in order:

1. **Distinct, ascending.** Dedupe `pgids` through a `map[int]bool`, collect, `sort.Ints`. **Never publish a map range order** — Go randomizes it, so a record built straight off the range would make the order-independence assertion below *flaky* rather than deterministically red, which is the worst of both. The sort is what makes it deterministic. The count of attributions is therefore the count of distinct groups, not of pgids passed (AC1).
2. **Partition.** `pgid <= 1` goes to `Unreportable` and appends `finAttributeGroupUnreportable` to `Conditions`; everything else is reportable (AC3).
3. **Empty check.** No reportable group ⇒ append `finAttributeNoGroups`, leave `Entries` nil and `Selected` zero, build `Detail`, return (AC4).
4. **Classify.** For each reportable pgid ascending: `trailAdmitAttribution(tdnClassifyReapLog(stderr, pgid), certified)` → one `finAttributeEntry`. Nothing reaching `Entries` is built by struct literal.
5. **Select.** The entry whose `Admit.Value` has the lowest index in `finAttributeOrder`. Ties (equal values from two groups) resolve to the lower pgid — free, because step 1 already sorted.
6. **Detail.** One `trailDetail` call. Its content rule is pinned rather than left to judgement, in `trailRunOutcome.Detail`'s shape (`trail_run_outcome_test.go:225-232`): it **MAY** name `finAttribute*` condition names, admissibility values, pgids, and the three counts (pgids passed, distinct, unreportable). It **MAY NEVER** quote the stderr, a `tdnReapOutcome.Line`, an entry's `Admit.Detail`, or the `certified` string. Quoting `Selected.Detail` is the likeliest slip — it reads as helpful context, and it both duplicates a string the record already carries and spends the 512-byte cap on it.

**Why sorted rather than input order.** The input order is `pinScan.Matches`' order, which is `ps` output order — an ambient fact about the process table, not a fact about the run. Sorting makes the whole record a pure function of the *set*, so AC2's "identically under either input order" is structural rather than a property a test hopes for. It does not resolve "the" pid (nothing here collapses the set); it only fixes the record's order.

### The selection order, and its argument

```go
// finAttributeOrder ranks the seven admissibility values strongest-evidence
// first. Total over trailIsAdmitValue's space, so a lookup always lands.
//
// A FUNCTION rather than a package-level var, for trailRunWellFormed's reason
// (trail_run_outcome_test.go:609-610): a shared backing array is reachable from
// every test in a 48-file package, and this slice is read on every fan-out call.
// trailRunOutcomeValues (:1194) is the same shape for the same reason.
func finAttributeOrder() []string {
	return []string{
		trailAdmitProof,
		trailAdmitVoidNotOneReapLine,
		trailAdmitVoidGroupUnnamed,
		trailAdmitVoidNoLine,
		trailAdmitVoidInstrument,
		trailAdmitVoidBudgetFired,
		trailAdmitOutOfContract,
	}
}
```

The argument the developer must write into the code, in full:

- **Only the first three can ever co-occur in one fan-out.** For one `(stderr, certified)`, `trailAdmitOutOfContract` (`:413`) and `trailAdmitVoidBudgetFired` (`:425`) fire before the verdict is read, so they are decided by `certified` alone and are identical for every group. `trailAdmitVoidNoLine` and `trailAdmitVoidInstrument` are decided by the stderr alone — the line count and whether the list parsed — and are likewise uniform. **The `heldPGID <= 1` route to `tdnReapInstrumentFailed` is filtered out in step 2**, so the only group-dependent split is `tdnReapHeldPGIDKilled` vs `tdnReapHeldPGIDAbsent`. Ranks 4–7 are there for totality, not for a contested case.
- **Proof first, and it is the load-bearing half.** `trailClassifyRun` reads `Admit.Value` for a decision in exactly two places: C5 (`:416`) and Step 2 (`:523`), both keyed on `trailAdmitProof`. So a rule that let one group's void suppress another group's proof would cost the probe its only finding, while the order *below* proof cannot change the run's outcome at all — it changes only what the published record says the reap log showed.
- **Below proof, rank by proximity to proof — never under-report the reap log.** `trailAdmitVoidNotOneReapLine` says a pinned group **was** named and only line multiplicity defeated the ordering argument; `trailAdmitVoidGroupUnnamed` says no pinned group was named. Both are voids and neither can manufacture a finding, so the choice is purely about information: reporting `GroupUnnamed` for a stderr in which a pinned group *was* named discards the strongest thing observed and reads as though the reap log never mentioned the pinned set. Proof is simply the top of this same order, which is why step 5 is one ranked scan and not a special case plus a tie-break.

### The filter is exactly `<= 1`

`reap.go:52` skips `pgid <= 1 || pgid == self || pgid == rootPid`. Mirror **only the first clause**. `tdnClassifyReapLog`'s guard (`:147`) is `heldPGID <= 1` and nothing more, so `<= 1` is precisely the set that would trip it. Extending the filter to `self` / `rootPid` would (a) require reading the process table, which this ticket forbids, and (b) be a second opinion about groups the shipped classifier is willing to answer for.

### Detail helper

Call `trailDetail`, do not define a `finDetail`. `trailDetail`'s own comment (`:200-205`) explains it is deliberately not `tdnDetail` because "the trail\* family stays out of the tdn\* teardown classifier's reach". That argument does not transfer: `trailDetail` is a `fmt.Sprintf` plus `reachCapCommand` and carries no decision, and this file is *by design* inside the trail family's reach — it embeds `trailAdmitResult`, calls `trailAdmitAttribution`, and calls `trailIsAdmitValue`. Reusing it keeps the 512-byte cap single-sourced. Say this in the file header so a reviewer does not read it as a doctrine violation.

### What crosses into the record, and the one channel AC5 cannot cover

Three inputs, and exactly one of them reaches the record as a string.

- **`pgids []int`** — integers. This is the closure of the credential channel, and it is the signature that closes it, not a check: `reachProc.Command` is verbatim argv read off the ambient process table, and a `ps` column is how `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY` reach an artifact destined for a public issue. **Take `[]int`.** #1281's gather holds `pinScan.Matches` and will feel the pull to pass rows straight through; converting at that call site is the point. A `[]reachProc` signature that recorded only `.PGID` would pass every test in this spec while reopening the channel, so this is a code-review item on the *signature*, not on the record.
- **`stderr []byte`** — reaches the record only through `tdnClassifyReapLog` → `tdnReapOutcome` → `trailAdmitAttribution` → `trailAdmitResult`, and that last type is documented trap-free by construction (`trailer_admissibility_test.go:190-197`). The predicate's Details format only `HeldPGID`, `PGIDs`, `LineCount` and `Verdict` from the outcome — ints and closed-set strings. `Line` never crosses. This is the channel AC5's needle test proves shut.
- **`certified string`** — **crosses verbatim.** `trailAdmitAttribution` splices it with `%q` into two of its Details: the budget arm (`:428`) and the proof arm (`:469`). It is `trailGate`'s certified `Reason`, i.e. the trailer's `terminal_reason`, which pyry's emitter chokepoints to a recorded detail or `"unclassified"` before marshalling. The shipped code already treats it as publishable — `trailClassifyRun` puts `readings.Gate.Reason` into its own Details (`:493`, `:530`), and `TestTrailRunOutcomeCarriesNoCapturedBytes` deliberately does not plant the needle in `Reason`.

  **So do not plant the needle in `certified` in AC5's test.** It would go red, and the only "fix" would be to stop carrying `trailAdmitResult` whole — which AC1 and AC5 both mandate. The ticket has already decided this: the entry carries the result whole. Two consequences worth recording rather than fixing: the exposure is pre-existing and unchanged in *content*, but the fan-out **multiplies it by the number of distinct groups** — where `trailRunReadings.Admit` held one copy of `certified`, `finAttributeRecord.Entries` holds one per group. Each is capped at 512 bytes by `reachCapCommand`, so the growth is bounded per entry and unbounded only in the entry count.

### Cost model

`tdnClassifyReapLog` splits the whole stderr on `\n` and walks every line, and the fan-out calls it **once per distinct reportable group**: the work is `O(len(stderr) × distinct groups)` and the record's size is `O(512 × entries)`. Both are fine for the live shape — a probe run's capture against a handful of matched groups — and **no cap is specified**, because no run has produced a set large enough to matter and a cap would cost another named condition. Stated so #1281, which supplies the real pgid set, knows what it is paying for.

### Two things deliberately not built

- **No contract check on an empty `certified`.** `trailAdmitAttribution` already answers it (`:413`) with `trailAdmitOutOfContract`, uniformly for every group, and that value is accepted by `trailIsAdmitValue`. A `finAttribute*` condition for it would be a second opinion about a value the shipped predicate documents. The fan-out passes `certified` straight through; the *rows* are what may not pass an empty one.
- **No entry in `TestTrailAdmissibilityConstantsAreClosed`'s union map** (`trailer_admissibility_test.go:619-656`). Following that precedent would put four sibling branches (#1278/#1281/#1282 and this one) into the same map literal and produce exactly the merge conflict a branch-overlap check exists to prevent. Collision is structurally impossible here anyway: `fin-attribute-` shares no prefix with `gate-`, `admit-`, `run-`, or the scan's input states. Whether the `fin*` family joins that map is #1278's call, once the family is complete — recorded in § Open questions.

---

## Testing strategy

Four tests. Scenarios as bullets; write them in the package's table-driven idiom.

### `TestFinAttributeFanOut` — the table

Case shape: `name`, `stderr []byte`, `pgids []int`, `certified string`, `wantSelected string`, `wantEntries int`, `wantConditions []string`. Every row drives the real functions; **no row hands the fan-out a `trailAdmitResult`** — the signature makes that impossible, which is AC1's enforcement.

**Every row runs twice**, once with `pgids` as given and once with the slice reversed, asserting the two records are identical (`reflect.DeepEqual`, or compare the marshalled JSON). That discharges AC2's "under either input order" for all rows at once, and is stronger than duplicating three rows.

Two fixture-aliasing rules, both from `trailRunWellFormed`'s stated reason (`:609-610`): `finAttributeCases()` is a **function**, not a package-level var, matching `trailGateCases()` and `trailRunCases()`; and the runner reverses into a **fresh slice**, never in place. Reversing in place mutates the row's own `pgids` and, on a shared var, would leak that mutation into every later test in the binary.

**Invariants asserted in the runner, on every row** — these fold AC4's third row and the record's mutual-exclusion contract into the loop rather than adding cases:

- `len(Entries) == 0` ⟺ `Selected.Value == ""` ⟺ `Conditions` contains `finAttributeNoGroups`.
- `len(Unreportable) > 0` ⟺ `Conditions` contains `finAttributeGroupUnreportable`.
- For every entry, `trailIsAdmitValue(entry.Admit.Value)`.
- When `Selected.Value != ""`, `trailIsAdmitValue(Selected.Value)`. *(This is AC4's third requirement, applied to every row instead of one. Say so in the test's doc comment so it does not read as a dropped AC.)*
- `Entries` is strictly ascending by `PGID`; `Unreportable` is ascending.

Rows. Every one passes a non-empty `certified` (use `"completed"`); an empty one measures `trailAdmitAttribution`'s contract block instead of this rule.

- **AC1 — distinct, not rows.** `stderr = trailReapLine(1, "[7788]")`, `pgids = []int{7788, 4242, 7788, 4242}` → 2 entries, not 4. Selected `trailAdmitProof`.
- **AC2 direction 1 — no void suppresses a proof.** One anchored line naming X and not Y: `trailReapLine(1, "[7788]")`, `pgids = {7788, 4242}` → entries `{7788: proof, 4242: void-group-unnamed}`, selected `trailAdmitProof`. (The reversed-order half of the runner is what makes this AC's "either input order" true.)
- **AC2 direction 2 — no composition of voids manufactures a proof.** Same single line, `pgids = {1234, 4242}` — neither named → both `trailAdmitVoidGroupUnnamed`, selected `trailAdmitVoidGroupUnnamed`, and explicitly `!= trailAdmitProof`.
- **AC2 direction 3 — the stated order over a genuinely differing pair.** Two anchored lines naming X and not Y: `trailReapLine(1, "[7788]") + "\n" + trailReapLine(1, "[7788]")`, `pgids = {7788, 4242}`. X → `trailAdmitVoidNotOneReapLine` (killed, `LineCount == 2`), Y → `trailAdmitVoidGroupUnnamed` (absent). Selected `trailAdmitVoidNotOneReapLine`. **Assert both entries' values too**, not just the selection — the row's whole point is that the two genuinely differ, and asserting only the winner would pass over a fan-out that gave both groups the same value.
- **AC3 — an unreportable group alongside a reportable one.** `pgids = {0, 7788}` against the single-line stderr → `Unreportable = [0]`, one entry `{7788: proof}`, `Conditions` holds `finAttributeGroupUnreportable`, selected `trailAdmitProof`. The pin: the reportable group's attribution is byte-identical to the same row without the `0`. Add a second row with a negative pgid (`{-5, 7788}`) so the guard is not read as an `== 0` check.
- **AC4 — nil input.** `pgids = nil` → `Conditions = [finAttributeNoGroups]`, no entries, zero `Selected`.
- **AC4 — every group unreportable.** `pgids = {0, 1}` → `Unreportable = [0, 1]`, `Conditions` holds **both** names, no entries, zero `Selected`.
- **Uniform-void coverage** (cheap, and it pins ranks 4–6 as reachable rather than decorative): a stderr with no anchored line → every group `trailAdmitVoidNoLine`; `certified = trailBudgetTerminalReason` → every group `trailAdmitVoidBudgetFired`; an anchored line whose `pgids=` value is unparseable → every group `trailAdmitVoidInstrument`.

### `TestFinAttributeOrderCoversTheAdmitSpace`

The deterministic guard against an eighth admissibility value ranking silently last. Follows `TestTrailRunOutcomeValuesAgreeWithThePredicate`'s shape (`trail_run_outcome_test.go:1214-1233`).

- `len(finAttributeOrder) == 7`, with a message saying 7 is `trailIsAdmitValue`'s space.
- Every element is accepted by `trailIsAdmitValue`.
- No duplicates.
- Each of the seven named constants appears. *(Yes, this restates the list. It is the only thing that catches a value added to the predicate and not to the order, and #1271 accepted the same shape for the same reason.)*

### `TestFinAttributeEmptySetAlternativesArePublishedFalsehoods`

AC4's two consumer-side rows. These are **the other layer**: they stage an `Admit` on a `trailRunReadings` by literal and hand it to `trailClassifyRun`. They are inputs to a different function, not attributions this fan-out produced — the same way `trailRunProofReadings` (`:623-631`) stages one. AC1's no-literal rule does not reach them, and the test's doc comment must say so explicitly.

Both rows start from `trailRunWellFormed()` and vary **only `Admit`** — leave `MatchCount` at 0, or the second row lands on Step 7 instead of Step 8 and asserts nothing about the empty set.

- `in.Admit = trailAdmitResult{}` (unset, what "leave the selected value unset" produces) → `trailClassifyRun` returns `trailOutcomeOutOfContract` via C3 (`:394`). Assert the outcome value, and that the `Detail` is C3's — a caller bug dressed as a reading.
- `in.Admit = trailAdmitResult{Value: trailAdmitOutOfContract, Detail: "…"}` → returns `trailOutcomeNoRowMatched` via Step 8 (`:588`). Assert the value. A clean negative published about a run whose attribution never happened.

### `TestFinAttributeRecordCarriesNoCapturedBytes`

AC5, in `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`'s shape, reusing `trailNeedle`.

- **Plant position is the whole test.** Drive with `stderr = trailReapLine(1, "[7788] "+trailNeedle) + "\n"`. `trailReapLine` splices `pgids` raw, and `tdnParsePGIDs` stops at the first `]` and checks nothing after it on an unquoted value (`teardown_liveness_test.go:246-254`), so the list still parses. The whole line is ~163 bytes, well inside `reachMaxCommandBytes` (512), so `Line` (`:165-167`) captures the needle rather than truncating it.
- **A needle on a non-anchored line makes this test vacuous** and must not be used: `:161-163` skips every line not containing `tdnReapMessage` *before* any field is filled, so the needle would enter no field at all and the test would go green over a record that recorded the whole outcome. Write that sentence into the test.
- **Premise first, and it doubles as the non-vacuity proof.** Assert `record.Entries[0].Admit.Value == trailAdmitProof` for `pgids = {7788}`, `certified = "completed"`. That value is reachable only if the needle-bearing line was recognised as anchored, parsed, and found to name 7788 — so a plant that stopped being anchored turns this into a `t.Fatalf`, not a silent pass.
- `json.Marshal(record)`, then `!bytes.Contains(encoded, []byte(trailNeedle))`.
- **The structural half**, following `trail_run_outcome_test.go:1170-1187`: decode to `map[string]json.RawMessage` and assert no top-level key contains `command`, `args`, `comm`, `argv`, `line`, or `stderr`. `line` and `stderr` are added over #1271's list because the channel this record is exposed to is `tdnReapOutcome.Line`, not a `ps` column.
- **Plant the needle in the stderr only.** #1271's test says "the needle goes into EVERY string-bearing input the classifier can see", and following that here would put it into `certified` — which crosses verbatim by design (§ What crosses into the record). That would go red for a reason the ticket has already decided against fixing. Pass `certified = "completed"` and say in the test's doc comment why the third input is excluded, so the omission reads as a decision rather than a gap.

---

## Error handling

The fan-out takes no `*testing.T`, returns no error, and never fails a test — the same contract as `trailGate`, `trailAdmitAttribution`, `tdnClassifyReapLog`, `pinReadState` and `fifoLiveRead`. Every way of not producing an attribution has a name in the record: `finAttributeGroupUnreportable`, `finAttributeNoGroups`, and the seven values the shipped predicate already documents. Nothing falls through to a default arm.

Panics: none reachable. The only indexing is into `finAttributeOrder` (bounded by the rank lookup) and into `Entries` (guarded by step 3's empty return).

---

## Concurrency model

None. `finAttributeFanOut` is pure over its three arguments: no goroutine, no exec, no clock, no file read, no process-table read. That purity is what lets every arm be driven offline with no credentials and no turn, and it is the property `#1281`'s live gather will depend on.

---

## Out of scope

No gather (`#1281`), no staged turn (`#1282`), no outcome tier naming a run that never staged (`#1278`). No process, no FIFO, no live claude. Do not re-derive the eleven-outcome set, do not re-parse pyry's stderr for the reap line, and do not edit `trail_run_rig_test.go` — #1268's `nil` stderr literal is structural to that file and correct there. **Never `ps -E` / `-Eww`**: this ticket needs no process-table read at all. **No `t.Skip`** — the file runs offline under `make e2e-realclaude`, and a skip exits 0 and reads as a pass (#1168).

---

## LOC budget

Target: **~215 production, ~300 test, ~515 total, one file.** This package's record is the reason the number is stated rather than left implicit — #1270 shipped 1028 LOC and #1271 shipped 1233 under the same `size:s` label, and both inflated in the same three places. If you are running over, cut in this order:

1. **The file header comment.** Cap it at ~45 lines. It needs: what the file does, that it is offline and takes no measurement, the reused-not-rebuilt list, and the `trailDetail` argument. Not a restatement of the ordering argument, which belongs on `finAttributeOrder`.
2. **Detail prose.** One sentence per Detail. The record has exactly one `Detail`; the per-group Details come from the shipped predicate and are not yours to write.
3. **Test rows.** The uniform-void coverage rows are the first to go if something must; the five AC-mandated rows and the two consumer-side rows are not.

What must **not** be cut to save lines: the ordering argument on `finAttributeOrder`, the vacuity paragraph in the AC5 test, and the runner's reversed-order half.

---

## Open questions

1. **Does the `fin*` family join `TestTrailAdmissibilityConstantsAreClosed`'s union map?** Deferred to #1278 deliberately (§ Two things deliberately not built): four sibling branches editing one map literal is a guaranteed merge conflict, and the `fin-attribute-` prefix makes collision structurally impossible in the meantime. #1278 is the last of the family and can add all of them in one edit.
2. **`Selected` is a discriminated optional and this ticket ships no enforcement of the discrimination.** The zero `trailAdmitResult` is meaningful (`Conditions` holds `finAttributeNoGroups`), and a consumer that copies it into `trailRunReadings.Admit` under a certifying gate publishes a caller bug as a reading. The record's doc comment states the obligation and `TestFinAttributeEmptySetAlternativesArePublishedFalsehoods` prices both wrong moves, but nothing structurally prevents the copy. **#1281 is the first consumer and must branch on `Conditions` before reading `Selected`** — that ordering obligation belongs in #1281's own AC set, not here, because the check has to live at the assignment site.
3. **Ranks 4–7 are ranked for totality, not for an observed contested case.** If a future input can make `trailAdmitVoidNoLine` co-occur with a group-dependent value in one fan-out — it cannot today, because line count is a property of the stderr — the argument for their relative order needs revisiting. Recorded so a later reader knows the ordering below rank 3 was never load-bearing.

---

## Security review

**Verdict:** PASS

The operative threat model for this package is not the relay's. `docs/threat-model.md` does not exist and `docs/protocol-mobile.md` § Security model is relay-transport-specific, neither of which reaches an offline pure classifier. What governs here is the package's own obligation: **a published record is pasted into a public GitHub issue**, so it must carry no credential (`CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` via a `ps` column) and no operator-review-before-paste string (verbatim model output, pyry's own stderr). Categories 1, 2 and 7 are where that lives; the rest are not applicable for structural reasons named below rather than skipped.

**Findings:**

- **[Trust boundaries]** No MUST FIX, and the walk changed the spec. Three inputs, and the boundary is per-input rather than per-function — the shape #1270's own review found this file family getting wrong. `pgids []int` and `stderr []byte` are shut (§ What crosses into the record). `certified string` **crosses verbatim** into `trailAdmitResult.Detail` on two arms (`trailer_admissibility_test.go:428`, `:469`); pre-existing and already treated as publishable by shipped code, but the fan-out multiplies it by the distinct-group count. Now documented on the record, with the explicit instruction not to plant AC5's needle there — without it a developer follows #1271's "needle into EVERY string-bearing input" comment, goes red, and "fixes" it by mangling the entry that AC1 and AC5 both require be carried whole.
- **[Trust boundaries — inheritance]** No finding. `finAttributeEntry` carries `trailAdmitResult` whole, so a future captured-bytes field on that type would be inherited silently. Two independent guards already catch it: #1270's own `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`, and this ticket's AC5 test, whose needle sits in the stderr and would reach any new field fed from `tdnReapOutcome`.
- **[Tokens, secrets, credentials]** SHOULD FIX, folded into the spec as a code-review item. The credential channel is closed by the **signature** (`[]int`, never `[]reachProc`), not by a check — and a `[]reachProc` signature that recorded only `.PGID` would pass every test in this spec while reopening it. #1281's gather holds `pinScan.Matches` and will feel exactly that pull. Named in § What crosses into the record so code-review checks the signature, not just the record. No token is generated, stored, compared or logged here.
- **[File operations]** Not applicable, structurally: the fan-out opens no path, reads no file and writes none. `stderr []byte` arrives already captured. Purity is asserted in § Concurrency model and is the property #1281 depends on.
- **[Subprocess / external command execution]** Not applicable, structurally: no `exec.Command`, no `sh -c`, no signal handling, no process staged. The `ps -E` / `-Eww` ban is restated in § Out of scope; this ticket needs no process-table read at all.
- **[Cryptographic primitives]** Not applicable — no RNG, no hashing, no comparison against a secret. One adjacent hazard was found and fixed: Go randomizes map iteration, so a record built off the dedupe map's range order would make the order-independence assertion *flaky* rather than deterministically red. § The fan-out step 1 now requires collect-then-`sort.Ints` and names that failure direction.
- **[Network & I/O]** No finding, and the limit is stated rather than capped. Work is `O(len(stderr) × distinct groups)` — `tdnClassifyReapLog` walks the whole stderr once per group — and record size is `O(512 × entries)`, each string capped by `reachCapCommand`. No cap is specified: no run has produced a set large enough to matter, and a cap would cost another named condition. Per **Evidence-Based Fix Selection**, an unobserved failure mode does not get a defence; § Cost model records it for #1281, which supplies the real set.
- **[Error messages, logs, telemetry]** MUST FIX, fixed inline before this verdict. The spec originally said `finAttributeRecord.Detail` "names no captured string" — the same vague per-function phrasing #1270's review caught, and the version a developer converts into "I'll quote `Selected.Detail` for context". Replaced with an explicit MAY / MAY-NEVER list in `trailRunOutcome.Detail`'s pinned shape (`trail_run_outcome_test.go:225-232`), naming `Selected.Detail` as the likeliest slip.
- **[Concurrency]** Two findings, both fixed inline. No goroutine, no lock, no shared mutable state at runtime — but the spec had specified `var finAttributeOrder = []string{...}`, a package-level slice reachable from all 48 test files and read on every fan-out call; now a function, matching `trailRunOutcomeValues()` (`:1194`) and `trailRunWellFormed()`'s stated reason (`:609-610`). Second: the runner's reversed-order half would have mutated its own fixture in place, so `finAttributeCases()` is now specified as a function and the reversal as a fresh slice.
- **[Threat model alignment]** No finding. The relay threat model is out of scope for an offline classifier and is named as such above rather than silently skipped. The package-level obligation it is replaced by is discharged by categories 1, 2 and 7 and made executable by AC5's enforcing test.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
