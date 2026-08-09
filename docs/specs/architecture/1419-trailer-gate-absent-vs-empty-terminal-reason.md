# #1419 — The trailer gate tells an absent `terminal_reason` from a present-and-empty one (offline)

**Size:** S (confirmed, not overridden). One file modified, zero production source files, ~180 total LOC, no consumer cascade.

## Files to read first

Every path is under `internal/e2e/realclaude/` unless stated. Everything here is `//go:build e2e_realclaude`, so `go vet` and `staticcheck` in `make check` never analyse it — see § Verification.

| Path | What to extract |
|---|---|
| `trailer_admissibility_test.go:315-402` | `trailGate`'s seven return sites; the `reason == ""` arm at `:367-378` is the one that splits. Note every arm ends `RunnerPath: in.RunnerPath`. |
| `trailer_admissibility_test.go:294-314` | The `# The budget arm keys on terminal_reason alone` and `# The runner path is CARRIED, never read` doc blocks. `:303` holds the arm count. |
| `trailer_admissibility_test.go:642-710` | `trailGateCases()` — its provenance doc (`:642-646`), its row-count claim (`:651`), and the two rows the repair touches (`:703-708`). |
| `trailer_admissibility_test.go:844-902` | `TestTrailGate` and the `the out-of-contract details name their own sub-case` sub-test. `:874` holds the three-inputs premise; `:883-894` is the fixture repaired by AC3 and the assertion rewritten by AC2. |
| `trailer_admissibility_test.go:931-1031` | `TestTrailGateIgnoresTheRunnerPath`. `:951-953` is the totality clause; `:980` pins `trailGateResult` at 4 fields (unchanged); `:996-1001` is clause B, the carriage assertion. |
| `trailer_admissibility_test.go:1234-1295` | `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`. Its gate row (`:1244-1269`) leaves `KeyNames` nil and reaches `trailGateUsable`, which is exactly why AC5 needs a second sub-test. |
| `trailer_key_names_test.go:158-170` | `trailKeyNamesNoTerminalReason()` / `trailKeyNamesEmptyTerminalReason()` — the shipped pair differing in one key, both carrying `"type":"result"`. |
| `trailer_key_names_test.go:243-291` | `TestTrailKeyNamesSeparatesAbsenceFromZeroValue` — already proves both fixtures scan to `trailSeen`, differ in `KeyNames`, and collapse to the same decoded `""`. The new arms inherit that premise; do not re-derive it. |
| `trailer_terminal_reason_test.go:128-137` | `trailReasonKeyName` — the shipped literal. Do not re-type `"terminal_reason"`. |
| `trailer_terminal_reason_test.go:196-216` | The presence doctrine (`presence must not come from `decodedReason != ""``) and the `slices.Contains(keyNames, trailReasonKeyName)` idiom to copy. |
| `result_trailer_observation_test.go:120-135` | `trailScanResult.KeyNames`' doc — the field's own statement that claude's own result line is the healthy headless shape. |
| `result_trailer_observation_test.go:192-213` | `trailScan`'s `trailSeen` return: `KeyNames: trailKeyNames(scanner.Bytes())`, filled off the full line before the cap. |
| `background_reach_probe_test.go:117-124`, `:945-950` | `reachMaxCommandBytes = 512`, `reachTruncationMarker`, and `reachCapCommand`. This is the binding constraint on the new Details — see § The 512-byte Detail budget. |
| `internal/agentrun/streamrunner/runner.go:177-179`, `:250-253` | The tee passthrough and the watchdog-only synthesis. Both cites verified accurate at `a3f23c2`; they are what the absence Detail rests on. |
| `trail_run_outcome_test.go:1074-1131` | `TestTrailRunComposesWithGateCases` — the fourth consumer of the slice. Map-keyed, so a duplicate gate value across rows is fine; `predicateCalls != 2` is the counter an added row must not disturb. |
| `trail_ptyrunner_composition_test.go:36-42` | The totality claim over `trailGateCases()`. Stays **true and untouched** provided the added row carries `trailRunnerUnread()`. |

Codegraph note: `codegraph_context` returns only `trailScanResult` for this area — the `e2e_realclaude`-tagged files are not fully indexed, so the reading list above came from direct reads. Do not expect `codegraph_callers` to find `trailGate`'s call sites.

## Context

`trailGate`'s empty-`terminal_reason` arm (`trailer_admissibility_test.go:367-378`) collapses two shapes and publishes one sentence about both. That sentence ends `NO LIVE REPRO EXISTS`, which is true of a trailer whose `terminal_reason` key is on the line with a blank value — `streamjson/emitter.go:383-391` is a chokepoint pyry cannot render past — and **false** of a trailer with no `terminal_reason` key at all, which is what every healthy `PYRY_USE_STREAMJSON=1` run produces: `streamrunner.Run` passes claude's bytes through unchanged and synthesises a trailer only when the idle-stall watchdog fired with no result seen.

The discriminator is already shipped. `trailScanResult.KeyNames` carries the line's top-level key names precisely so absence and present-and-empty stop being the same `""`, and `trailReasonAgainstPath` already reads presence as `slices.Contains(keyNames, trailReasonKeyName)`. This ticket calls that reading at the gate; it derives nothing new.

**No answer changes.** Both new arms return `trailGateOutOfContract` with an empty `Reason`, so the five-value gate space, the seven-value admit space, `trailClassifyRun`'s C1/C2, and every downstream consumer are untouched.

## Design

### 1. The arm split — one nested branch, two return sites

The existing `if reason == ""` guard stays as the outer condition; the split happens inside it. That keeps the two new arms unreachable from any input the decode says carries a named reason, and keeps the diff to one block.

```go
reason := in.Scan.Trailer.TerminalReason
if reason == "" {
    if !slices.Contains(in.Scan.KeyNames, trailReasonKeyName) {
        return trailGateResult{Value: trailGateOutOfContract,
            Detail: trailDetail(<absence prose>, trailSeen), RunnerPath: in.RunnerPath}
    }
    return trailGateResult{Value: trailGateOutOfContract,
        Detail: trailDetail(<present-and-empty prose>, trailSeen), RunnerPath: in.RunnerPath}
}
```

Three rules the shape encodes, each load-bearing:

- **Presence is membership, never the decoded value.** `slices.Contains(in.Scan.KeyNames, trailReasonKeyName)` — the same read `trailReasonAgainstPath` opens with (`trailer_terminal_reason_test.go:216`). `decodedReason != ""` is the collapse the key-name reading was landed to prevent, and it is always `false` inside this block anyway, which is what makes it a *silent* defect rather than a loud one.
- **Presence is membership, never cardinality.** `len(KeyNames) > 0` reads a scan-produced absence — six names merely missing `terminal_reason` — as *present*. AC1's second mutation is exactly this.
- **Both arms still carry `RunnerPath` and read no path.** Neither Detail interpolates the reading; both are fixed prose over `trailSeen` and file cites. `TestTrailGateIgnoresTheRunnerPath` keeps its name, its doc's claim and its green.

Add `"slices"` to the import block (`trailer_admissibility_test.go:5-12`); it is not there today.

**No new constants.** Both arms answer `trailGateOutOfContract`, so `TestTrailAdmissibilityConstantsAreClosed`'s twenty-nine-constant map (`:721`) is untouched, and `trailGateResult`'s four-field pin (`:980`) is untouched.

### 2. The 512-byte Detail budget — the binding constraint

`trailDetail` is `reachCapCommand(fmt.Sprintf(...))`, and `reachCapCommand` truncates at **512 bytes** and appends `reachTruncationMarker`. The tail of a Detail is where the load-bearing clause lands, so this is not a cosmetic limit: a natural first draft of the absence prose measures **587 bytes** and loses the whole *which absence case is not decided here* clause to the cap, silently.

Two wordings were measured on this tree and both fit. They are **exemplars, not mandates** — reword freely, but re-measure, and keep the four substring properties in the table below, because the tests key on them.

| Arm | Rendered bytes | Headroom |
|---|---|---|
| present-and-empty | 442 | 70 |
| absent | 480 | 32 |

**Present-and-empty** (`%s` = `trailSeen`):

> state trailer-seen carries a trailer whose terminal_reason is empty — the key IS on the line and its value is blank. Certifying it would reintroduce, one layer up, the defect the nil Trailer pointer was chosen to prevent. Today's pyry cannot render a blank one — emitter.go:383-391 is a chokepoint substituting the recorded detail or "unclassified" before marshalling — so for this shape, and not for an absent key, NO LIVE REPRO EXISTS

**Absent**:

> state trailer-seen carries a trailer with NO terminal_reason key on the line. Nothing is certified. Unlike a present-and-empty one this shape HAS a live repro: on PYRY_USE_STREAMJSON=1 streamrunner passes claude's bytes through unchanged (runner.go:177-179) and synthesises one only when the idle-stall watchdog fired and claude emitted no result (:250-253), so a healthy run's trailer is claude's own result line. WHICH absence — owed or not owed — is NOT DECIDED AT THIS ARM

The `(:250-253)` bare cite inherits `runner.go` from the cite before it, which is this file's own idiom — the usable arm's Detail already reads `(runner.go:479-485, :398)` at `:398`.

Substring properties the tests depend on:

| Substring | present-and-empty | absent |
|---|---|---|
| `terminal_reason is empty` | present | **absent** |
| `NO LIVE REPRO EXISTS` | present | **absent** |
| `not for an absent key` | present | absent |
| `NOT DECIDED AT THIS ARM` | absent | present |

The first row is why the shipped assertion at `:886-889` needs no edit and becomes a live discriminator: if the repaired present-and-empty fixture misroutes to the absence arm, that assertion fails on its own. The last row is a free cap guard — the phrase sits at the tail of the absence Detail, so an overlong reword goes red at the assertion rather than shipping a truncated record. (`!strings.HasSuffix(detail, reachTruncationMarker)` is the one-line diagnostic if a clearer failure message is wanted; it is not required.)

### 3. Two fixture helpers, shared by the rows and the sub-test

```go
func trailGateAbsentReasonScan() trailScanResult  // trailScan of trailKeyNamesNoTerminalReason() + "\n"
func trailGateEmptyReasonScan() trailScanResult   // trailScan of trailKeyNamesEmptyTerminalReason() + "\n"
```

Functions rather than package-level vars, for the reason `trailExpectedKeyNames()` and `trailRunnerUnread()` already state in this family: the value holds a `[]string` and a `*resultTrailer`, and `go test -race` runs these tests in parallel.

`trailGateAbsentReasonScan`'s doc carries the reason the scan is a **requirement and not a preference**: it yields `KeyNames` of six names merely missing `terminal_reason`, and a hand-built `trailScanResult` with `KeyNames` nil is read as absent by the `len(KeyNames) > 0` mutant too — which would leave AC1's second mutation red nowhere and the row proving nothing.

Both helpers are called from four sites: the two `trailGateCases()` rows and the two Detail sub-test inputs. Sharing them is what makes AC3's *"the repaired fixtures reach the present-and-empty arm"* an identity rather than an inference across two textually-similar literals.

### 4. `trailGateCases()` — repair one row, add one

- **Repair** the row at `:703-708`: its hand-built `trailScanResult{State: trailSeen, Trailer: &resultTrailer{Type: "result"}}` becomes `trailGateEmptyReasonScan()`. Same `want`, same absent `reason`.
- **Add** a ninth row, `trailGateAbsentReasonScan()`, `want: trailGateOutOfContract`, no `reason`, `RunnerPath: trailRunnerUnread()` — the last is what keeps `trail_ptyrunner_composition_test.go:38`'s totality claim true and untouched.

Both rows are scan-produced, so provenance goes from four/four to **six scan-produced, three hand-built**.

The added row must be green in all four consumers at once, and is:

- `TestTrailGate` (`:845`) — value `trailGateOutOfContract`, reason `""`, certification invariant holds, Detail non-empty.
- `TestTrailGateIgnoresTheRunnerPath` (`:986`) — Detail is fixed prose, byte-identical across all five readings.
- `TestTrailGateThenAdmit` (`:1193`) — certifies nothing, so `calls` stays 2 (`:1218`).
- `TestTrailRunComposesWithGateCases` (`trail_run_outcome_test.go:1090`) — `want` is map-keyed, so a fifth row reaching `trailGateOutOfContract` composes to `trailOutcomeOutOfContract` like the other four; `predicateCalls` stays 2 (`:1121`); the `"the trailer gate itself reported"` Detail check (`:1108`) is satisfied by `trailClassifyRun`'s own gate arm, independent of which gate arm fired.

### 5. Totality of the carriage assertion is restored, not narrowed

Clause B of `TestTrailGateIgnoresTheRunnerPath` (`:996-1001`) is what stops an arm that forgot `RunnerPath: in.RunnerPath` from hiding behind an arm that carried it, and it is total only because the fixture rows reach every return site. After the split there are eight sites; after the repair alone no row would be absence-shaped, leaving one site uncovered.

The added row restores it by coverage rather than by a bespoke assertion. Verified enumeration of the nine rows against the eight sites:

| Return site | Row |
|---|---|
| unrecognised state | *a state nobody defined* / *the zero scan result* |
| `trailAbsent` | *ordinary stream-json with no trailer* |
| `trailAborted` | *a line past bufio.Scanner's default* |
| nil trailer | *a seen state carrying a nil trailer* |
| **absent key (new)** | **the added row** |
| **present-and-empty (new)** | *a seen state whose terminal_reason is empty* (repaired) |
| budget fired | *a max_turns trailer* |
| usable | *an ordinary trailer* |

Eight of eight. No separate carriage assertion is needed, and `:951-953` restates the same guarantee with new numbers rather than a new caveat.

### 6. Count claims — the exact set, re-derived

Swept at `a3f23c2` with a word-level grep over `\b(three|four|seven|eight|nine)\b` in `trailer_admissibility_test.go`; the hits are exactly the seven the ticket names plus five belonging to other closed sets (`:131` the admit allowlist, `:721` the constants map, `:738` `trailReasonAgainstPath`'s rows, `:756` the run-level voids, `:909-913` the runner readings). Those five are **untouched**.

| Site | Today | After | Moved by |
|---|---|---|---|
| `:303` | "Not one of the **seven** arms consults it" | eight | arm split |
| `:874` | "**Three** inputs reach one value … the **three** arms are indistinguishable" | four / four | arm split |
| `:951-953` | "the gate's **seven** return sites — the **eight** rows reach all **seven**" | eight / nine / eight | arm split + added row |
| `:642-646` | "The **four** rows … through trailScan … the **four** that trailScan CANNOT emit" | six / three | repair + added row |
| `:1035` | "TestTrailGate routes **four** rows through trailScan" | six | repair + added row |
| `:633` | "all **eight** fixture rows" | nine | added row |
| `:651` | "these same **eight** rows under all five readings" | nine | added row |
| `trail_ptyrunner_composition_test.go:38` | "**every** `trailGateCases()` row carries `trailRunnerUnread()`" | **unchanged** | totality claim; stays true because the added row carries it |

`:874`'s prose widens beyond the number: it currently closes *"a reader cannot tell a nil pointer from an unfilled reason,"* and the sub-case it names as the far end of the range is now one of two. Say what the four arms are.

`:951-953`'s clause keeps saying totality is a guarantee about carriage. Do not soften it to "reach most of".

### 7. The leak sweep gains a rung that reaches the new arms

The key names are a new rendering surface. They come from claude, they are deliberately unbounded at this tier (`trailer_key_names_test.go:55-59` defers the per-name cap to #1363 *because `trailScanResult` is published by nothing*), and `trailGateResult` **is** published. The absence arm's most natural Detail — one that says which keys the line did carry — would publish them.

The shipped sweep cannot see this: its gate row (`:1244-1269`) leaves `KeyNames` nil and carries `TerminalReason: "completed"`, so it reaches `trailGateUsable` and neither new arm. And *"no value from the trailer enters the record"* does not forbid it, because **a key name is not a value** — the distinction `trailKeyNames` was built on.

Add a second sub-test to `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`, two rows, both planting `trailNeedle` **as a key name**:

- *absent-shaped*: `State: trailSeen`, `Trailer: &resultTrailer{Type: "result"}`, `KeyNames` containing `trailNeedle` and **not** `trailReasonKeyName`; `Line` and `Detail` also needled, as the shipped row does.
- *present-and-empty-shaped*: the same, plus `trailReasonKeyName` in `KeyNames`.

Each row asserts, in order: `Value == trailGateOutOfContract` **and** the Detail contains that row's arm marker (the non-vacuity precondition — a row that reached a different out-of-contract arm would sweep the wrong Detail), then `json.Marshal` and `!bytes.Contains(encoded, trailNeedle)`.

Keep it a **separate sub-test** rather than widening the shipped one: that one's premise is a well-formed input reaching `trailGateUsable`, and the new rows must reach the new arms. Both existing sweeps (`:1243`, `trail_run_outcome_test.go:1142`) stay green.

## Concurrency model

None. `trailGate`, `trailScan` and `trailKeyNames` are pure over their inputs — no exec, no clock, no filesystem, no goroutine, no `*testing.T`. The only concurrency consideration is `go test -race` running subtests in parallel, which is why the two new fixtures are **functions** returning fresh values rather than package-level vars: `trailScanResult` holds a `[]string` and a `*resultTrailer`, and a shared backing array would let one row's mutation reach another's (the hazard `trail_run_outcome_test.go:608-610` documents).

`TestTrailGateIgnoresTheRunnerPath` copies each row's `trailGateInput` per reading, and a struct copy copies the *pointer* — all five copies of a row alias one `*resultTrailer` and one `KeyNames` backing array. Nothing is written through either, and nothing added here changes that. The existing doc at `:955-962` already states it; it needs no edit.

## Error handling

`trailGate` returns no error and cannot fail — the contract it shares with `trailScan`, `trailAdmitAttribution`, `trailClassifyRun`, `tdnClassifyReapLog`, `pinReadState` and `fifoLiveRead`. The two new arms are fully-answered cases, not breakages.

Failure modes and how they are answered:

- **A `nil` `KeyNames`** (hand-built record, or a scan that somehow recorded none) → `slices.Contains` on a nil slice is `false` → the absence arm. That is the correct reading of *the field is off the line* for the only records that can produce it, and the arm's own prose says which absence case is undecided rather than asserting one.
- **`KeyNames` present but `Trailer` nil** → answered earlier, at `:355`, unchanged.
- **A trailer carrying a named `terminal_reason` on a path that owes none** → still `trailGateUsable`. A different defect, and explicitly not this ticket's; keep both new arms' docs silent about it.
- **Whether an absence should be *admitted* rather than reported out of contract** → not decided here. That is a later slice and a value in two closed sets.
- **A reworded Detail overrunning 512 bytes** → the tail-phrase assertions go red. See § 2.

## Testing strategy

Everything runs offline. No live claude, no credentials, no `t.Skip`, no env gate.

**Widen the `the out-of-contract details name their own sub-case` sub-test** (`:873-901`) from three inputs to four. Scenarios:

- *nil trailer* — unchanged.
- *unknown state* — unchanged.
- *present-and-empty*, driven from `trailGateEmptyReasonScan()`: Detail contains `terminal_reason is empty` (the shipped assertion at `:886-889`, unedited); Detail contains the scoped no-live-repro phrase (the rewritten `:890-894`, whose failure text says the claim is true of a blank value and false of an absent key on the stream path, so an unscoped one sends a reader hunting for a run that does not exist while mis-describing the one that does); Detail does **not** contain `NOT DECIDED AT THIS ARM`.
- *absent*, driven from `trailGateAbsentReasonScan()`: Detail contains the absence marker and `NOT DECIDED AT THIS ARM`; Detail does **not** contain `terminal_reason is empty`; Detail does **not** contain `NO LIVE REPRO EXISTS`, with failure text naming the scope — this is the assertion that catches the old prose copy-pasted onto the new arm.

Each row asserting *both* its own markers and the other arm's absence is what makes it red on its own when its own case misroutes, rather than only detecting that the two Details differ.

**Mutations — both required, both verified, no worktree writes.** Use `go test -overlay=<abs-path json>` to run a mutated copy of `trailer_admissibility_test.go`; run from the worktree in a single call (`cd <worktree> && go test -overlay=... -count=1 -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/`). Record the observed pass/fail per row in the PR body.

| Mutation | Expected sole red | Why |
|---|---|---|
| presence taken from the decoded value (`in.Scan.Trailer.TerminalReason == ""` in place of the membership test) | **present-and-empty row only** | inside this block the decoded value is always `""`, so every input routes to the absence arm; the absent row is unaffected. |
| presence read as cardinality (`len(in.Scan.KeyNames) == 0` in place of `!slices.Contains(...)`) | **absent row only** | the scan-produced absence carries six names, so it reads as present and routes to the present-and-empty arm; the present-and-empty row carries seven and is unaffected. |

If the absent row's fixture is ever changed to a hand-built `trailScanResult` with `KeyNames` nil, the second mutation goes green everywhere and the row proves nothing. That is the failure the helper's doc exists to prevent.

**No new fixture is needed for cap interaction.** `KeyNames` is read off `scanner.Bytes()` before the cap (`result_trailer_observation_test.go:199-208`), and `TestTrailKeyNamesReadsTheFullLine` already pins that ordering against a line ~1.9 KiB past the cap.

**Verification commands.** `make check` does **not** analyse this package — `go vet` and `staticcheck` run untagged, so a compile error or an unused import here is invisible to it. Run explicitly:

```
gofmt -l internal/e2e/realclaude/
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -count=1 -race -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/
make check
```

The suite was re-run at `a3f23c2` while writing this spec: green offline in 3.367 s, no credentials.

## Scope — what does not move

- Every comment asserting that no arm of the gate reads the runner path (`:190-196`, `:226-229`, `:238-244`, `:301-314`, `:648-651`, `:931-962`) stays true. Only the arm count at `:303` changes.
- The three comments naming the cancelled #1374 (`:193`, `:243`, `:314`) and the sweep's `:933` are **not** corrected here.
- `:606`, `:635`, `finding_run_gather_test.go:548` and `trail_run_rig_test.go:158` are #1414's and are not touched.
- `trailReasonAgainstPath` is **not** called from the gate. It takes a path reading; no arm added here has one.
- No `docs/knowledge/` file is a deliverable of this ticket. The documentation phase writes `docs/knowledge/codebase/1419.md` from the merged diff.

## Open questions

1. **Two pre-existing stale cites were observed and are deliberately left alone.** `trailReasonAgainstPath` cites the stream passthrough as `streamrunner/runner.go:170-176` (`trailer_terminal_reason_test.go:225`, `:234`); at `a3f23c2` those lines are the `childCtx` block and the passthrough is `:177-179`. Separately, `finding_run_record_test.go:660` cites `trailer_admissibility_test.go:538-542` for `trailGateCases()`' provenance rule, which lives at `:642-646` — a ~104-line gap. Both predate this ticket and neither is in scope under AC5. The new absence Detail uses the **correct** `:177-179`, so the file will briefly hold two different cites for the same passthrough; that is the right way round.
2. **Line-cite drift into this file is expected and is not a defect of this PR.** Thirty-one filename-anchored cross-file cites point at `trailer_admissibility_test.go:<line>`, and bare chained `:NNN` refs inheriting that anchor add more. Insertions here shift everything below them by roughly +20 lines above `:400`, +35 above `:900`, and +90 to +125 in the `:984`–`:1252` range. Chasing them is a cite-sweep ticket, not this one. Code-review should attribute a gap by magnitude: a ~40-line shift is this PR's, a 100+-line gap (e.g. `:538-542`, or `trail_run_outcome_test.go:1136`'s `:984`) is pre-existing.
3. **Whether the absence arm should eventually be *admitted* rather than reported out of contract** is left undecided on purpose, and so is which absence case a given line represents. Both need the observed runner path, which the follow-on slice reads. The arm's Detail says exactly that and no more.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in § 7 before this verdict.** The design opens a new path from attacker-influenced data to a published artifact: `KeyNames` holds top-level JSON key names taken from claude's own output, they are deliberately unbounded at this tier (`trailer_key_names_test.go:55-59` defers the per-name cap to #1363 on the explicit ground that `trailScanResult` is published by nothing), and `trailGateResult` **is** published. The absence arm's most natural Detail — one naming the keys the line did carry — would cross that boundary, and the shipped sweep cannot see it: `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`' gate row leaves `KeyNames` nil and reaches `trailGateUsable` (`:1244-1269`), and the existing rule *"no value from the trailer enters the record"* does not forbid a key **name**. Resolved by § 7's two-row sub-test, which plants `trailNeedle` as a key name on inputs that reach the two new arms, with an arm-marker precondition so neither row can pass by reaching a different arm. Both new arms' Details are fixed prose over `trailSeen` and file cites, with no `%v` of any slice.
- **[Trust boundaries] No further findings.** The boundary itself is unchanged and structural: `trailKeyNames` discards its `map[string]json.RawMessage` internally and returns `[]string`, so no *value* can cross (`trailer_key_names_test.go:87-106`), and `TestTrailScanResultReachesNoRawMessageMap` keeps that reachable-type ban live. `trailGateResult` gains no field, so the four-field pin at `:980` still bounds what can be marshalled.
- **[Error messages, logs, telemetry] SHOULD FIX — mitigated, not gated.** These Details are the published record, so an overlong reword is a real disclosure-adjacent hazard in the opposite direction: `reachCapCommand` truncates at 512 bytes silently, and a Detail that lost its tail publishes a *partial* claim. Mitigated deterministically by asserting the tail phrase of each Detail (§ 2), which fails loudly rather than shipping truncated prose. Both measured wordings fit with 70 and 32 bytes of headroom.
- **[Subprocess / external command execution] N/A by design.** `trailGate` is pure over its input — no exec, no clock, no filesystem — and this ticket calls no argv reader: not `tdnClaudeCommand`, not `reachProc.Command`, not `reachRunnerPathFromArgv`. The two new arms decide on the scan alone. Nothing here starts, signals, or inspects a process.
- **[File operations] N/A by design.** No path is constructed, opened, or written. The only new I/O in the whole ticket is `go test`'s own.
- **[Cryptographic primitives] N/A.** No randomness, no comparison against a secret, no key material. The one comparison added is `slices.Contains` over a name set, where a timing side channel has no meaning — the names are not secret and the answer is published in the record anyway.
- **[Network & I/O] N/A at this tier, with the upstream bound named.** The input is a byte slice already in memory. Its size is bounded upstream by `bufio.Scanner`'s 64 KiB default in `trailScan`, which is deliberately not raised (`result_trailer_observation_test.go:177-179`), and the number of key names is bounded by that same line length. Nothing here reads a socket.
- **[Concurrency] No findings.** No goroutine, no lock, no shared mutable state. The one live hazard — five copies of a row aliasing one `*resultTrailer` and one `KeyNames` backing array inside `TestTrailGateIgnoresTheRunnerPath` — is pre-existing, documented at `:955-962`, and stays safe because nothing is written through either pointer. The two new fixtures are functions rather than package-level vars specifically so `go test -race` cannot let one subtest's slice reach another's.
- **[Tokens, secrets, credentials] N/A.** No credential is read, stored, or logged; the suite runs with none.
- **[Threat model alignment] Aligned.** The relevant threat for this family is *a published probe artifact carries bytes an operator has not reviewed*. This ticket's own AC5 raises it from a discipline to a check for the one surface the ticket newly exposes. **OUT OF SCOPE:** bounding the key names themselves (length, count) — that is #1363's, at the tier that publishes them, and this ticket's guard is that no name is echoed at all, which is strictly stronger than a cap for these two arms.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-09
