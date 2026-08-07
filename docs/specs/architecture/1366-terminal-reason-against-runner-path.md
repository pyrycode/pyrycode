# #1366 — Probe instrument: classify a result trailer's `terminal_reason` against what the observed runner path owes (offline)

**Size:** S (confirmed, not overridden). One new file, three small edits elsewhere. See § Size check.

## Files to read first

| path | what to extract |
|---|---|
| `internal/e2e/realclaude/trailer_key_names_test.go:1-59` | The file-header shape this ticket's new file copies: the offline declaration, the `go test -race -tags e2e_realclaude -run '^TestTrail'` line, and the "what the fixed decode cannot answer" argument. Do not re-derive that argument — cite it. |
| `internal/e2e/realclaude/trailer_key_names_test.go:72-106` | `trailKeyNames`' contract: sorted, top-level only, **no value crosses**. The `[]string` signature is why this predicate can take key names without inheriting a leak obligation. |
| `internal/e2e/realclaude/trailer_key_names_test.go:158-170` | `trailKeyNamesNoTerminalReason()` / `trailKeyNamesEmptyTerminalReason()` — AC2's absent/present-empty pair. Both carry `"type":"result"`, so `trailScan` matches them. |
| `internal/e2e/realclaude/trailer_key_names_test.go:243-291` | `TestTrailKeyNamesSeparatesAbsenceFromZeroValue` — the shape AC2's new test mirrors one layer up, including its non-vacuity fatals (`State == trailSeen`, `Trailer != nil`) before it reads through the pointer. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:98-137` | `trailScanResult`: `KeyNames` is the presence reading; `Trailer` is the nil-unless-seen pointer trap. This predicate takes **neither the record nor the pointer** — see § Design, "Three scalars, never the record". |
| `internal/e2e/realclaude/result_trailer_observation_test.go:303-338` | `trailFixtureTrailer` (eleven keys, `"terminal_reason":"completed"` — the present-and-non-empty shape) and `trailNeedle`. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:87-208` | The three things this file copies: the `gate-`-style value-space prefix argument, the two-field result record, and `trailDetail` (the capped Detail formatter). |
| `internal/e2e/realclaude/trailer_admissibility_test.go:487-512` | `trailIsGateValue` / `trailIsAdmitValue` — the membership-helper shape `trailIsReasonValue` copies verbatim. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:597-656` | `TestTrailAdmissibilityConstantsAreClosed` — the union map this ticket extends, and its own comment that it catches a **colliding** value and never an **unhandled** one. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:973-1028` | `TestTrailAdmissibilityRecordsCarryNoCapturedBytes` — the exact shape AC5's sweep copies (non-vacuity fatal, `json.Marshal`, `bytes.Contains`). |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:772-790` | `tdnRunnerFromArgv` — all five constant answers, including the **three distinct** `indeterminate (...)` variants. The table's path readings are produced by calling this, never hand-typed. |
| `internal/e2e/realclaude/finding_run_record_test.go:256-313` | `finRecordRunnerLabel` (the shipped reduction, AC3) and `finRecordRunnerAgreement`'s doc — the paragraph that says prefix-matching **two unknowns** is wrong and that a reduced label against a **known-expected** one is fine. This predicate is the allowed case. |
| `internal/e2e/realclaude/finding_run_record_test.go:231-242` | `finRecordInputs.ClaudeCommand`'s rule: the argv is reduced to a constant answer and never retained. AC5's admissibility argument for taking the reading is this rule, one layer up. |
| `internal/e2e/realclaude/finding_live_staging_test.go:485`, `:498`, `:576`, `:597`, `finding_run_record_test.go:823` | The five shipped `finRecordRunnerLabel(…) != "ptyrunner"`-shaped comparison sites. Bare string literals, no constants for the two runner labels — the switch in § Design follows this. |
| `internal/agentrun/streamrunner/runner.go:170-176` | The passthrough. Cited **at V2's doc comment** as the reason claude can produce the same reading, per AC5. Read it so the cite is true. |

## Context

`resultTrailer.TerminalReason` is a plain `omitempty` string, so after the fixed decode an absent `terminal_reason` and one emitted as `""` are the same value. #1357 landed the reading that separates them — `trailScanResult.KeyNames`, read off the full line before the 512-byte cap. Nothing consumes it.

Absence means opposite things on the two runner paths. On ptyrunner every trailer is pyry's and the field is present and non-empty by construction (`emitter.go:383-391` substitutes the recorded detail or `"unclassified"`). On the headless stream-json path `streamrunner.Run` passes claude's bytes through unchanged, so on a healthy run the trailer is claude's own `result` line and carries no `terminal_reason` at all. Neither the decoded scalar nor the key names alone can say which; the pair can.

This ticket ships the predicate and its tests only. Wiring it into `trailGate` is #1368's; carrying the path reading to the gate's callers is #1367's. That split is load-bearing rather than tidy — `trailClassifyRun`'s switch over gate values has **no default arm**, and a sixth gate value would fall through to step 2 (where a proof is awarded) rather than be caught (`trail_run_outcome_test.go:476-484`). Nothing here touches `trailGate` or `trailClassifyRun`.

## Size check

Counted before writing, not after:

| item | lines | files |
|---|---|---|
| new `trailer_terminal_reason_test.go` (header, 6 value constants + docs, key-name constant, record, predicate + doc, membership helper, fixtures, four tests) | ~480–520 | 1 new |
| `trailer_admissibility_test.go` — six union-map entries + the doc-comment amendment | ~11 | 1 edited |
| two inbound cites re-pointed (precomputed in § The cite sweep) | 2 | 2 edited |
| **total written** | **~495–535** | **1 new, 3 edited** |

Against the red lines: 1 new file (≤3); ~535 total lines (≤~600); 0 exported types; **0 consumer call sites** — nothing consumes the predicate by design, and the three edits outside the new file are cite/registry lines, not call sites; 5 acceptance criteria (≤5); **6 arms and no contract-guard branch** (≤10 reject branches — AC1 forbids a seventh value, so there is no out-of-contract arm to cost).

Sized against the nearest analogue rather than by feel: **#1357** (`45ef577`) is the same package, the same family, the same shape — one instrument plus its tests — and shipped **463 insertions across 2 files**, plus a 25-line cite sweep across 7. This ticket is that plus three more arms and seven more table rows, minus the field-addition cascade #1357 caused. No rationalization was needed to reach S: no count was re-described as smaller than it is.

Production source files the spec prescribes content for, excluding `*_test.go`: **0**.

## Design

### Where it lives

One new file, `internal/e2e/realclaude/trailer_terminal_reason_test.go`, under `//go:build e2e_realclaude`. Its header follows `trailer_key_names_test.go:1-59`: it reaches no verdict about pyry, takes no measurement, and runs offline — no live claude, no credentials, no daemon, no exec, no clock, no goroutine, no env gate, no `t.Skip`. State that, as the other four trail files do.

### The value space — six constants, `reason-` prefixed

The prefix is load-bearing for the same reason `gate-` is (`trailer_admissibility_test.go:95-101`): this space's words mean nearly what the gate's, the admit's and the run outcomes' words mean, so a copy-paste between spaces must be a visible mistake rather than a plausible line. All six strings are pairwise distinct from the twenty-nine shipped constants by prefix alone.

| constant | string | means |
|---|---|---|
| `trailReasonAbsentOwesNone` | `reason-absent-on-owes-none-path` | Absent on a path that owes none — that path's documented healthy shape. |
| `trailReasonPresentOwesNone` | `reason-present-on-owes-none-path` | Present on a path that owes none. See the claim limit below. |
| `trailReasonAbsentOwesOne` | `reason-absent-on-owes-one-path` | Absent on a path that owes one. |
| `trailReasonBlankOwesOne` | `reason-blank-on-owes-one-path` | Present-and-empty on a path that owes one — the `emitter.go:383-391` chokepoint's guarantee violated. |
| `trailReasonNamedOwesOne` | `reason-named-on-owes-one-path` | Present-and-non-empty on a path that owes one — that path's documented healthy shape. |
| `trailReasonPathUnnamed` | `reason-path-names-no-runner` | The path reading names no runner. |

`Absent` / `Blank` / `Named` are deliberately three different words rather than three inflections of "present": the space's whole purpose is refusing to collapse absence into emptiness, and two identifiers one tab-completion apart would make the collapse a plausible typo. Spellings may be adjusted only if pairwise distinctness (identifier **and** string) and the semantic content above are preserved.

**`trailReasonPresentOwesNone`'s doc comment is where AC5's claim limit lives.** It must say what the reading supports — *the line is not that path's documented healthy shape* — and must say explicitly that it never claims pyry wrote it, citing `internal/agentrun/streamrunner/runner.go:170-176` as the reason claude can produce the same reading. Pyry's own synthesis on that path is unconditional when it happens (`streamrunner/watchdog.go:253`, `:280`), but that is a statement about what pyry writes, never about what claude cannot. The cite belongs in the doc comment above the constant, and the same weaker claim (without the file cite, which is optional there) belongs in that arm's `Detail`.

**Emptiness is not distinguished on the owes-none path.** A present-and-empty `terminal_reason` from streamrunner is as much "not that path's documented healthy shape" as a non-empty one, so `trailReasonPresentOwesNone` absorbs both. Say so at the constant, so a later reader does not "fix" it into two values.

### The key-name constant

```go
// trailReasonKeyName is resultTrailer.TerminalReason's json tag name
// (tool_loop_test.go:201), i.e. the top-level key trailKeyNames reports.
const trailReasonKeyName = "terminal_reason"
```

No reflect pin against the struct tag is wanted, and that is a decision rather than an omission: AC2's test drives the absent/present-empty pair under one fixed path reading and asserts they reach **different** values, so a misspelled constant makes presence false for both, collapses them onto one value, and goes red there. A second mechanism would guard the same drift.

### The record

```go
type trailReasonResult struct {
	Value  string `json:"value"`
	Detail string `json:"detail"`
}
```

Two fields, `trailAdmitResult`'s shape (`trailer_admissibility_test.go:194-197`). No pointer into any input, no quote of any captured string.

### The predicate

```go
func trailReasonAgainstPath(runnerReading string, keyNames []string, decodedReason string) trailReasonResult
```

Pure over its three inputs: no exec, no clock, no filesystem, no `*testing.T`, never fails a test — the same contract as `trailScan`, `trailGate`, `trailAdmitAttribution`, `trailClassifyRun`, `tdnClassifyReapLog`, `pinReadState` and `fifoLiveRead`.

**Three scalars, never the record.** It takes neither `trailScanResult` nor `trailObservation`, for two reasons that are both AC5's: taking the record would drag `Line` (verbatim model output, operator-review-before-paste) into reach, and it would inherit the nil-`Trailer` pointer trap that `trailGate` had to answer with a contract check. Three inputs the caller has already reduced is what makes this function have no unsafe input at all. It makes no claim that the key names and the decoded scalar came from the same line — that is the caller's obligation, and it lands on #1367/#1368.

**Parameter order is not accidental.** `runnerReading` and `decodedReason` are both `string`; `keyNames []string` sits between them so the two are not adjacent. The compiler still cannot catch a transposition (unlike `finRecordInputs`' named fields, `finding_run_record_test.go:205-212`), but nothing here is on opposite sides of a leak boundary — a transposition produces a wrong value, not a publication. Record that reasoning; do not add a struct for three parameters.

Behaviour, in three steps:

1. Reduce the reading with the shipped helper: `finRecordRunnerLabel(runnerReading)`. Never re-parse, never prefix-match. This is the case `finRecordRunnerAgreement`'s doc explicitly allows — a reduced label compared against a **known-expected** literal, not two unknowns prefix-matched (`finding_run_record_test.go:294-300`).
2. Read presence from the key names alone: `slices.Contains(keyNames, trailReasonKeyName)`. A nil slice is absence, correctly and without a guard. **The decoded scalar is never consulted for presence** — that is AC2, and taking presence from `decodedReason != ""` would merge the first two rows of the owes-one path because their decoded values are identical.
3. Switch on the label:

| label | presence | decoded | value |
|---|---|---|---|
| `"streamrunner"` | absent | *not consulted* | `trailReasonAbsentOwesNone` |
| `"streamrunner"` | present | *not consulted* | `trailReasonPresentOwesNone` |
| `"ptyrunner"` | absent | *not consulted* | `trailReasonAbsentOwesOne` |
| `"ptyrunner"` | present | `""` | `trailReasonBlankOwesOne` |
| `"ptyrunner"` | present | non-empty | `trailReasonNamedOwesOne` |
| anything else | any | any | `trailReasonPathUnnamed` |

The two runner labels are **bare string literals**, following the five shipped comparison sites; there are no constants for them and this ticket does not introduce any. `finRecordRunnerIndeterminate` is not needed in the switch — the default arm covers it — but that arm's `Detail` should name it as the shipped answer that lands there.

**Why the default arm is not the fall-through catch-all the family forbids.** `trailGate`, `trailAdmitAttribution` and `trailClassifyRun` all put their out-of-contract value in a guard at the top, precisely so no unrecognised input reads as an answer about pyry. There is no out-of-contract value here, and AC1 forbids adding one. That is sound because `trailReasonPathUnnamed`'s meaning **is** "the path reading names no runner" — a positive, true statement about the reading, and equally true of `tdnRunnerFromArgv`'s three `indeterminate (...)` answers and of any label outside the two runners. It makes no claim about pyry, so it cannot launder one. Write that argument into the constant's doc comment; it is the first thing a reviewer will ask.

### The Detail rule — no input byte interpolates, at all

Each arm's `Detail` is **fixed prose**, formatted through `trailDetail` (`trailer_admissibility_test.go:206`) and interpolating only: this file's own six constants, `finRecordRunnerIndeterminate`, the two bare runner-label literals, and file cites. It must never interpolate `runnerReading`, its reduced label, any key name, or `decodedReason`.

This is stronger than AC5 requires — AC5 rules the reading admissible because it is one of `tdnRunnerFromArgv`'s constant answers — and it is chosen because it makes the guarantee **structural rather than a discipline**, the same doctrine that made `trailKeyNames` return `[]string`. A hand-built reading is not one of those constant answers, and a Detail that echoed the label would publish whatever a caller passed. The consumer's record already publishes the reading separately (`finRecordRun.RunnerFromArgv`), so nothing is lost.

`trailDetail` is still the formatter even with constant arguments, because the family's rule is that every retained operator-visible string is capped. Each Detail must fit under `reachMaxCommandBytes` (512) with margin — the long-form argument belongs in the doc comment, which no cap applies to. The table test asserts this on the output rather than trusting it (see § Testing).

### Membership helper

```go
func trailIsReasonValue(v string) bool
```

A `switch` over the six, `trailIsGateValue`'s shape verbatim (`trailer_admissibility_test.go:493-500`). It exists for the family's stated reason: a value a reader of the published record cannot look up is a verdict they cannot interpret. #1367/#1368 must call it rather than re-switch — that is the family's convention for `trailIsGateValue`, `trailIsAdmitValue`, `trailIsBoundFrom` and `pinIsVerdict`.

### Where closure is checked

Two places, because they check different things and the ticket's Technical Notes flag the gap:

1. **Collision** — the six join `TestTrailAdmissibilityConstantsAreClosed`'s union map (`trailer_admissibility_test.go:620-656`) as a labelled block, `// #1366's terminal-reason-against-path values.` This is the family's single registry; a fourth space that stayed out of it would be invisible to the cross-space check that map exists for. Amend that test's doc comment in the same edit: its "EVERY VALUE IN THIS MAP HAS AN ARM IN ITS CONSUMER — … for the first two spaces, `trailClassifyRun` for the third" sentence must name `trailReasonAgainstPath` for the fourth.
2. **Unhandled** — the map explicitly cannot catch a value with no arm. The nine-row table test closes that: it collects the values its rows reach and asserts the set is **exactly** the six. A seventh constant with no arm, or a sixth that no row reaches, goes red there.

No new closure test. A second union map would be a fork waiting to drift.

**Do not add a fourth zero-record check** to that test's `zeroGate` / `zeroAdmit` / `zeroRun` loop. Those exist because each of those records has a *second* discriminating field whose unfilled state could read as filled (`zeroGate.Reason`, `zeroRun.Bounded` / `.BoundFrom`). `trailReasonResult` has only `Value` and `Detail`, and all six constants are non-empty, so a `zeroReason.Value == value` comparison is unfalsifiable by construction. Adding it would be three more lines of shift for no check.

## Concurrency model

None. The predicate is pure and holds no state; `go test -race` runs this package's tests in parallel, which imposes exactly one rule: **any fixture returning a slice must be a function, never a package-level `var`**, or one row's mutation reaches another through the shared backing array (`trailer_key_names_test.go:115-118`, `trail_run_outcome_test.go:608-610`). The key-name slices in the table come from `trailScan` calls, which return fresh slices, so this bites only if a helper is added later.

## Error handling

The predicate returns no error and cannot fail. There is no out-of-contract value and no arm that reports instrument breakage — an unrecognised label is a fully-answered case (`trailReasonPathUnnamed`), not a failure. `keyNames == nil` is absence, not an error. This is deliberate and is the one structural difference from `trailGate`; § Design records why.

## Testing strategy

Four tests, all offline, all under `^TestTrail`. Scenarios, not code:

### 1. `TestTrailReasonAgainstPath` — AC4's nine rows

- The three trailer shapes come from **real scans of shipped fixtures**, never hand-built key-name slices: `trailScan(trailKeyNamesNoTerminalReason())` (absent, decoded `""`), `trailScan(trailKeyNamesEmptyTerminalReason())` (present, decoded `""`), `trailScan(trailFixtureTrailer)` (present, decoded `"completed"`). Fatal first if any of the three is not `trailSeen` or carries a nil `Trailer` — a shape that scanned to nothing would make every row below vacuous.
- The three path readings come from **calling `tdnRunnerFromArgv`**, never hand-typed prose: an argv carrying only `--session-id`, one carrying only `--input-format`, and one carrying neither. Hand-typing bare tokens instead would make **M5 green** — that mutant is only red because the rows carry the full parenthesised readings.
- The nine rows are the **cross product** of the three shapes and the three paths, generated in code with an explicit 3×3 expected matrix indexed `[path][shape]` in the ticket's R1–R9 order. Exhaustive by construction: a row cannot be pruned without deleting a shape or a path, and an assertion that the generated count is 9 catches that. Sub-test names carry the R-number so failure output maps to the ticket's table.
- The assertion is `got.Value != want`, printing both. **Not** "is / is not `trailReasonPathUnnamed`": M2 and M3 share a red row-set (R7–R9) and are told apart only by the *wrong value each yields*, so a coarser assertion loses the distinction in the failure output. Say this in the test's doc comment.
- Rows R1, R4 and R6 are red only under M5 and are the ones most likely to look prunable. Note that at the table, so nobody prunes them.
- Per row, also assert the Detail is non-empty and does **not** end in `reachTruncationMarker` — a Detail over 512 bytes loses its tail to the cap, and the point of these Details is the tail.
- After the loop: the set of reached values equals exactly the six, and every reached value satisfies `trailIsReasonValue`. Plus one control — `trailIsReasonValue(trailGateUsable)` is false — so the green above is a property of the predicate and not of a helper that returns true for everything.
- One sub-test for AC3's "including when the trailer's shape would otherwise qualify": all **three** `indeterminate (...)` variants `tdnRunnerFromArgv` can produce (empty argv; both flags; neither flag) driven against the present-and-non-empty shape — the shape that reaches `trailReasonNamedOwesOne` on ptyrunner — must all reach `trailReasonPathUnnamed`. This is the direct evidence that the reading was reduced rather than compared against one variant.

### 2. `TestTrailReasonPresenceComesFromTheKeyNames` — AC2

- Both fixtures scanned; fatal if either is not `trailSeen` or has a nil `Trailer`, before anything reads through the pointer.
- Both fed to the predicate under **one fixed path reading**: `tdnRunnerFromArgv` on a `--session-id` argv (ptyrunner). Assert they reach different values — `trailReasonAbsentOwesOne` and `trailReasonBlankOwesOne` respectively. The streamrunner side of the same distinction is already R1 vs R2.
- In the **same test**, assert both decode through `resultTrailer` to the same `TerminalReason`, and that it is `""`. That second half is what establishes the decode could not have answered this; without it the first half proves only that two inputs differ.

### 3. `TestTrailReasonResultCarriesNoCapturedBytes` — AC5

`TestTrailAdmissibilityRecordsCarryNoCapturedBytes`'s shape (`trailer_admissibility_test.go:982-1028`): non-vacuity fatal, `json.Marshal`, `bytes.Contains`. Three **distinct** needles, one per input position, from a `trailReasonNeedles()` map — a shared needle could not say which position leaked, which is `trailKeyNamesNeedles`' rule (`trailer_key_names_test.go:125-137`).

- Sub-test A: `decodedReason` is the decoded-position needle and the key-name slice is `trailFixtureTrailer`'s real names plus one extra name carrying the key-name needle; the reading is the real ptyrunner answer. Fatal unless the value is `trailReasonNamedOwesOne` — a predicate that answered `trailReasonPathUnnamed` here would leak nothing and pass everything. Then marshal and sweep both needles.
- Sub-test B: the reading is `"ptyrunner (" + <reading needle> + ")"` — a legitimate label with a needled reason, so the label reduces to `"ptyrunner"` and the row reaches a **real** arm rather than the default one. Same shape as A otherwise. This is what proves the reduction dropped the parenthesised tail instead of the whole string being retained.

### 4. Extension of `TestTrailAdmissibilityConstantsAreClosed`

Six entries added to the existing union map, plus the doc-comment amendment. No new test function.

### Running it

```
go test -race -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/
```

Green offline in a few seconds with no credentials, no `t.Skip` and no env gate. `make check` must also stay green.

## The cite sweep

Adding ~11 lines to `trailer_admissibility_test.go` at the union map shifts every line after it. Two inbound cites point past the insertion; both name `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`' doc comment:

| citing site | cites | note |
|---|---|---|
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:700` | `trailer_admissibility_test.go:973` | Correct today. Re-point to the post-edit line. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:1135` | `trailer_admissibility_test.go:915` | **Already stale by 58 lines** — the doc comment starts at 973, and no shift of this size opens a 58-line gap, so this is pre-existing debt rather than something this diff creates. Re-point it to the same correct post-edit line, not to `915 + shift`. |

Verify by **target text**, not by arithmetic: after the edit, confirm the cited line is the `// TestTrailAdmissibilityRecordsCarryNoCapturedBytes makes AC5's` comment line. Then re-check for bare `(:NNN)` cites near those two sites that inherit `trailer_admissibility_test.go` as their last-named file and point past the insertion — the census above covers the named form only. A clean grep for the filename is not evidence the file is clean.

No other file's line numbers move: the new file is new, and nothing else is edited.

## Out of scope, named

- **`trailGate` / `trailClassifyRun` are not touched.** #1368 wires the predicate in; growing `trailGate`'s value space is the documented landmine (`trail_run_outcome_test.go:476-484`) and is kept out of this ticket deliberately.
- **No caller.** Nothing consumes `trailReasonAgainstPath` after this ticket. That is intended; #1367 carries the path reading to the gate's callers.
- **`docs/knowledge/codebase/1366.md` is not a deliverable here.** The documentation phase writes it from this spec plus the merged diff.
- **`reachRunnerPathFromArgv` is not used**, and the new file's header should say why in one line: it keys on `--append-system-prompt-file`, which both builders pass, so it labels a correctly-wired stream run `ptyrunner` and has no `streamrunner` answer at all.

## Open questions

1. **Does #1368 want the Reason certified alongside the value?** `trailGateResult` carries a certified `Reason`; `trailReasonResult` deliberately does not, because carrying the decoded scalar out would breach AC5's "no value from the trailer enters the predicate's output". If #1368's wiring turns out to need the reason, it should take it from `trailGateResult.Reason`, which already publishes it, rather than widen this record.
2. **Should the three `indeterminate (...)` variants ever reach three different values?** No, on today's evidence: all three mean "the argv was not read to a runner", and splitting them would put instrument provenance into a space that answers a question about the trailer. Recorded here so a future ticket makes that a decision rather than a discovery.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The design has exactly one untrusted input — the trailer's key names, which arrive from claude's output via `trailKeyNames` — and it crosses a boundary that is structural rather than conventional: `trailKeyNames` discards its `map[string]json.RawMessage` internally and returns `[]string` (`trailer_key_names_test.go:87-106`), so no value from the line can reach this predicate at all. The other two inputs are a `tdnRunnerFromArgv` constant answer and an already-decoded scalar. The predicate reads the key names only through `slices.Contains` against a compile-time constant, and returns a value from its own closed set. A hostile trailer controls whether `terminal_reason` is on the line and what it decodes to — i.e. it can steer the answer between `trailReasonAbsentOwesNone` and `trailReasonPresentOwesNone` — and the spec addresses that directly: § Design requires `trailReasonPresentOwesNone` to claim only *not that path's documented healthy shape*, never that pyry wrote it, with `streamrunner/runner.go:170-176` cited at the value. That is the strongest claim the reading supports; a stronger one would let claude's own output name pyry as its author.
- **[Error messages, logs, telemetry]** No findings, and this is the category the `security-sensitive` label is for on this family. Both published strings are attacker-adjacent by construction: `trailScanResult.Line` is verbatim model output marked OPERATOR-REVIEW-BEFORE-PASTE, and this record is destined for a public GitHub issue. The design closes it two ways. Structurally, § "The Detail rule" forbids interpolating **any** of the three inputs — stronger than AC5, which would have permitted echoing the reading — so no input byte has a path into the output even from a hand-built caller. Behaviourally, test 3 plants three distinct needles (decoded scalar, an extra key name, and the reading's parenthesised tail) and sweeps the marshalled record, with a non-vacuity fatal before each sweep so a predicate that answered `trailReasonPathUnnamed` cannot pass by leaking nothing. Sub-test B is the one that earns its place: it needles the part `finRecordRunnerLabel` strips, so a reduction that retained the whole string goes red. Details are additionally capped through `trailDetail`, and the table test asserts no Detail was truncated — the #1284 defect, where house-style prose ate the headroom and let a containment check pass against a leaking implementation, cannot recur here because nothing interpolates in the first place.
- **[Subprocess / external command execution]** No findings, and the category is not vacuous here — it is the reason `reachRunnerPathFromArgv` is banned. Claude's argv is the most sensitive string this family handles (it carries operator paths), and the design never touches it: the predicate takes `tdnRunnerFromArgv`'s already-reduced constant answer, which is the rule `finRecordRun` states for `ClaudeCommand` (`finding_run_record_test.go:235-238`). The new file itself execs nothing, spawns nothing, and reads no environment.
- **[Concurrency]** No findings. The predicate is pure and stateless. The one real hazard in this package under `go test -race` is a package-level `var` returning a shared slice backing array, which lets one parallel row mutate another's fixture; § Concurrency model names it and the rule (fixtures returning slices are functions), and the table's key-name slices come from `trailScan`, which returns fresh slices per call.
- **[Network & I/O]** No findings — no socket, no file, no reader. Input size is already bounded upstream: the key names come from a line `bufio.Scanner`'s 64 KiB default capped inside `trailScan`, and the per-name bounds land at the publishing tier (#1363/#1364), not here. `trailScanResult` is published by nothing at this tier, so this predicate introduces no new rendering surface.
- **[File operations]** Not applicable — the design opens, creates, stats and renames nothing. No path is constructed from any input.
- **[Tokens, secrets, credentials]** Not applicable — no credential is read, generated, compared or stored. The suite runs with no credentials by design and this file adds no env read.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against a secret. The only comparisons are a `slices.Contains` against a compile-time constant and a `switch` over two string literals, neither of which guards anything secret, so constant-time comparison is not in question.
- **[Threat model alignment]** Not applicable to `docs/protocol-mobile.md` — nothing here touches the relay, the wire protocol or a device. The relevant threat is this probe family's own, stated in `trailer_key_names_test.go:45-59`: model-chosen bytes reaching a public issue. Addressed under Error messages above.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-07
