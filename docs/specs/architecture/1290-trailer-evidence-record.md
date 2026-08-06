# #1290 — The run verdict and its trailer evidence, read behind the discriminated optional

**Ticket:** [#1290](https://github.com/pyrycode/pyrycode/issues/1290) · split from #1285
**Size:** S · **Labels:** `size:s`, `security-sensitive`
**Deliverable:** one new file, `internal/e2e/realclaude/finding_trailer_evidence_test.go`. No production code, no edits to existing files.

---

## Files to read first

Everything below is in `package realclaude` under the `e2e_realclaude` build tag, so every symbol is directly callable from the new file. Read in this order; do not grep for these, the line refs are verified at `3582765`.

| Path | Extract |
|---|---|
| `internal/e2e/realclaude/result_trailer_observation_test.go:98-137` | `trailScanResult` and `trailObservation` — the input types. Note `Line` is capped and `Trailer` is a pointer that is nil unless `State == trailSeen`. |
| `…/result_trailer_observation_test.go:57-90` | The two closed spaces: `trailSeen` / `trailAbsent` / `trailAborted`, and `trailBoundFromMiss` / `trailBoundFromStart` / `trailBoundNone`. Call these constants; never restate the sets. |
| `…/result_trailer_observation_test.go:164-208` | `trailScan` — the only way to build a real scan result. `Line: reachCapCommand(...)` at `:182`, decode against the **full** line. |
| `…/result_trailer_observation_test.go:242-274` | `trailWaitForTrailer` — where each `BoundFrom` value comes from. The two `trailBoundNone` return sites (the `trailAborted` arm at `:264-265` and the deadline arm at `:270`) leave `Staleness` zero; that fact is load-bearing for AC1's synthetic row. |
| `…/result_trailer_observation_test.go:282-317` | Fixtures: `trailFixtureTrailer` (342 B), `trailFixtureNoTrailer`, `trailNeedle`, `trailPaddedTrailer(pad)`, `trailOverlongPad`. |
| `…/result_trailer_observation_test.go:477-525` | The already-shipped padded-trailer subtest. **Do not restate it** — this ticket's AC2 test pins the four fields on the *built record*, not on the scan result. |
| `internal/e2e/realclaude/tool_loop_test.go:194-210` | `resultTrailer` — the four fields to copy, their Go types (`IsError` is a `bool` at `:200`), and the absence of a `result` member. |
| `internal/e2e/realclaude/trail_run_outcome_test.go:221-250` | `trailRunOutcome` — the record shape to mirror. `Bounded` at `:242-246` is the exact rule AC1 repeats; the Detail content rule at `:225-232` is the model for this record's. |
| `…/trail_run_outcome_test.go:254-268`, `:1189-1210` | `trailIsRunOutcome` and `trailRunOutcomeValues` — call both; never re-derive the eleven. |
| `internal/e2e/realclaude/finding_staging_gate_test.go:198-232` | `finOutcomeResult` (a value and a detail and nothing else), `finOutcomeIsValue`, `finOutcomeValues` — the staging tier's seven. |
| `…/finding_staging_gate_test.go:1-40`, `:60-81` | The file-header shape this family uses, and the settled reason `trailDetail` is reused instead of a `finDetail` twin. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:76-92` | `finAttributeEntry` — the "trap-free by construction" argument, and how it is worded on the type. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:199-208` | `trailDetail` — `fmt.Sprintf` + `reachCapCommand`. The only Detail formatter this file may use. |
| `internal/e2e/realclaude/background_reach_probe_test.go:115-125`, `:945-951` | `reachMaxCommandBytes = 512`, `reachTruncationMarker`, `reachCapCommand`. The comment at `:117-122` is the threat model this record's needle sweep serves. |
| `internal/agentrun/streamjson/emitter.go:428-437` | `wireFields` — one `ExitReason` renders `subtype`, `terminal_reason` and `is_error`. Note the `default` arm (`:434-435`) returns `terminal_reason == ""` on a **seen** trailer. |
| `internal/agentrun/streamjson/emitter.go:456-468` | The pinned wire order: `result` sixth, `terminal_reason` last. |
| `internal/agentrun/streamjson/emitter.go:205-225` | `e.lastStopReason = entry.Message.StopReason` at `:210` — `stop_reason` is forwarded from the model's own message, unvalidated. |

---

## Context

The probe measures whether `pyry agent-run` on the ptyrunner default path reaches its normal exit while a command it launched is still running. This ticket builds **one half of that probe's published record**: the run's outcome value together with the trailer evidence behind it. #1291 embeds this record whole into the run record; #1286 owns the artifact writer and its multi-input no-captured-bytes sweep.

Everything here is driven from synthetic readings. No live claude, no credentials, no turn, no clock.

Three constraints shape the whole design:

1. **The four trailer fields sit behind a discriminated optional.** `trailScanResult.Trailer` is `*resultTrailer`, nil unless `State == trailSeen`, and deliberately a pointer: a consumer that derefs without checking State panics loudly, which was chosen over a value type handing back `TerminalReason == ""` and letting an empty terminal reason pass as a real one (`result_trailer_observation_test.go:108-118`).
2. **The fields must come from `Trailer`, never from `Line`.** `Line` is capped at 512 bytes; `terminal_reason` is **last** on the emitter's pinned wire order and `result` is sixth, so any truncation takes `terminal_reason` first. `Trailer` is the decode of the full line.
3. **The record carries no trailer line at all.** `Line` holds ~415 bytes of model-chosen text and is marked OPERATOR-REVIEW-BEFORE-PASTE. Every published record in this family excludes it; this one does too.

---

## Design

### The output type

One new file-local type. Field order is part of the design: `Staleness` sits immediately after `BoundFrom` so that "never published without its discriminator beside it" is true positionally in the rendered JSON, not merely by convention.

```go
type finTrailerRecord struct {
	Outcome        string        `json:"outcome"`
	State          string        `json:"trailer_state"`
	Bounded        bool          `json:"lateness_bounded"`
	BoundFrom      string        `json:"lateness_bound_from"`
	Staleness      time.Duration `json:"staleness_ns"`
	Subtype        string        `json:"subtype"`
	IsError        bool          `json:"is_error"`
	TerminalReason string        `json:"terminal_reason"`
	StopReason     string        `json:"stop_reason"`
	Detail         string        `json:"detail"`
}
```

**No `omitempty` on any field, and that is a decision rather than an oversight.** `wireFields`' `default` arm (`emitter.go:434-435`) returns `terminal_reason == ""` on a genuinely *seen* trailer. Under `omitempty` such a record would render byte-identically to a `trailAbsent` record's zero — collapsing "the trailer was read and its terminal reason is empty" into "there was no trailer", which is precisely the collapse the nil pointer was chosen to prevent one tier down. The same argument kills `omitempty` on `IsError` (drops `false`) and on `Staleness` (drops the honest zero that pairs with `trailBoundNone`). `State` is the discriminator a reader consults; every field is always present beside it.

**Deliberate omissions, so a later edit does not add them casually:**

- No `Line`, no `*resultTrailer`, no `trailScanResult`, no `trailObservation` — nothing from which the capped line or the trailer pointer is reachable.
- No `ObservedAt`. `trailRunOutcome` carries no instant either; if #1291 wants one it adds it at its own tier.
- No out-of-contract value and no validation of `Outcome` (see below).

**Trap-freedom, stated precisely — the two claims are different and the spec keeps them apart:**

- *The output type is trap-free by construction.* Ten scalar fields, no pointer, no slice, no embedded struct. Copying it costs nothing and aliases nothing; #1291 can embed it whole with no ordering discipline to maintain. This matches `finAttributeEntry`'s own wording (`finding_attribution_fanout_test.go:80-88`).
- *The builder is not.* Its input **does** carry the capped line (`obs.trailScanResult.Line`), so the no-leak property of `finTrailerBuild` is held by the Detail's content rule plus AC4's test — not by construction. Say so on the builder, so nobody reads the type's construction argument as covering the function.

Copying the four fields **by value** at build time also severs the pointer: two records built from one observation cannot alias a shared `*resultTrailer`.

### The Detail content rule

Pinned on the type, in `trailRunOutcome.Detail`'s shape (`trail_run_outcome_test.go:225-232`) rather than left to judgement:

- **MAY** name: the outcome value, the scan state, `BoundFrom`, `Bounded` as a boolean, and the four decoded fields.
- **MAY NEVER** quote `trailScanResult.Line` or interpolate any part of it — including a length, a byte count derived from it, a prefix, or a hash.

The four decoded fields are permitted because the record already publishes them as fields; the exposure decision is the type's, and the Detail adds nothing to it.

**Length budget.** Every Detail must leave `len(trailNeedle)` = 42 bytes of headroom under `reachMaxCommandBytes`, i.e. stay under **470 bytes** on every row. This is not stylistic: `trailDetail` caps at 512, so a house-style multi-sentence Detail can eat the budget and let a leak be truncated away, turning AC4's sweep green against a record that did leak. That is the exact defect #1284 shipped and then had to fix. The test asserts the headroom per row (see Testing strategy); the budget is stated here so the developer writes to it the first time.

### The builder

```go
// finTrailerBuild projects one run's verdict and its trailer evidence onto a
// published record. Pure over its inputs: no exec, no clock, no filesystem, no
// *testing.T, and it never fails a test.
func finTrailerBuild(outcome string, obs trailObservation) finTrailerRecord
```

Behaviour, in eleven or so lines:

1. Fill `Outcome` from the parameter **as handed** — no validation, no renaming, no re-derivation, no cross-check against `State`.
2. Fill `State`, `BoundFrom` and `Staleness` from the observation verbatim.
3. `Bounded = obs.BoundFrom == trailBoundFromMiss` **and nothing else.** Never `Staleness != 0`: `trailBoundFromStart` carries a real duration that bounds nothing, so that derivation would publish a non-bound wearing a bound's label (`trail_run_outcome_test.go:230-234`).
4. Compute one boolean, `carriesTrailer := obs.State == trailSeen && obs.Trailer != nil`. The `State` operand comes **first**; Go's `&&` short-circuits left to right, which is what makes "the State is consulted before the pointer is dereferenced" a property of the source and not of a comment. Only under this boolean are the four fields copied.
5. `Detail` via `trailDetail(...)`, one shape per arm of that boolean.

**Why the nil operand as well as the State check.** `trailGate` (`trailer_admissibility_test.go`) is this trap's first consumer and already documents that it answers a `State == trailSeen` / `Trailer == nil` pair before anything reads through the pointer — the inconsistency is a hand-built fixture's, and hand-built fixtures are all this file ever sees. Unlike `trailGate`, this record has **no out-of-contract value** to report it with, so the honest behaviour is to publish the zero fields under whatever `State` was handed and let the Detail name which arm fired. One `if`, two Detail shapes; no third branch, no reject arm.

**Why the outcome is an input and never a derivation.** The value comes from one of two closed sets built elsewhere — `trailClassifyRun`'s eleven and the staging-gate seven — and both are caller-supplied. "A no-trailer run records `trailOutcomeVoidNoTrailer`" is a statement about what the *caller* hands in; `trailClassifyRun` already returns it for such a run. Deriving it here from `State` would give the field two sources, which this family's doctrine forbids. It also means an assertion on an *agreeing* row is a tautology — see AC3's testing notes.

**Why the field is not asked to reject a non-member.** No builder in this family validates its value: `trailRunOutcome` (`:233`) and `finOutcomeResult` (`:198`) are plain structs. Membership lives in the reader-facing predicates, whose job is that "a value a reader of a published record cannot look up is a verdict they cannot interpret".

### What the four trailer fields are worth

The record's doc must say this, because it is not visible from the field names:

- `subtype`, `is_error` and `terminal_reason` are **one `ExitReason` rendered three ways** by `wireFields` (`emitter.go:428-437`). Their agreement is one value seen thrice, not three corroborating reads.
- `terminal_reason` is **pyry's own synthesis** on this path — claude never emitted it.
- `stop_reason` alone is independently sourced, forwarded from the model's last message unvalidated (`emitter.go:210`). It is bounded by the wire's own shape and by nothing this record does. Naming that on the type is what keeps #1286's multi-input sweep from later planting a needle in a field this record must carry whole. See also the Security review's finding 1.

### Data flow

```
claude stdout bytes ──> trailScan ──> trailScanResult{State, Line(capped), Trailer(*resultTrailer)}
                                            │                 │              │
                     trailWaitForTrailer ───┴──> trailObservation{+ObservedAt, Staleness, BoundFrom}
                                                          │
   outcome (trailClassifyRun's 11 | staging tier's 7) ────┤
                                                          ▼
                                              finTrailerBuild(outcome, obs)
                                                          │
                                                          ▼
                     finTrailerRecord{Outcome, State, Bounded, BoundFrom, Staleness,
                                      Subtype, IsError, TerminalReason, StopReason, Detail}
                                      └─ no Line, no pointer, no embedded observation ─┘
```

`Line` enters the builder and reaches no field of the output. That edge is the one AC4's test exists to hold.

### Reused, not rebuilt

Called, never re-derived or hand-copied. State this list in the file header, in the shape `finding_staging_gate_test.go:66-80` uses:

| Symbol | Source | Why not a local twin |
|---|---|---|
| `trailScan`, `trailFixtureTrailer`, `trailFixtureNoTrailer`, `trailPaddedTrailer`, `trailOverlongPad`, `trailNeedle` | `result_trailer_observation_test.go` | The shipped scan and its fixtures; a second scanner of the same bytes that disagreed would be worse than either. |
| `trailSeen`/`trailAbsent`/`trailAborted`, `trailBoundFrom*` | `…:57-90` | Shipped closed spaces with predicates beside them. |
| `trailIsRunOutcome`, `trailRunOutcomeValues` | `trail_run_outcome_test.go:258`, `:1194` | The eleven. Re-switching them forks the set. |
| `finOutcomeIsValue`, `finOutcomeValues` | `finding_staging_gate_test.go:210`, `:223` | The seven. Same reason. |
| `trailDetail` | `trailer_admissibility_test.go:206` | **Do not define `finDetail`.** Settled twice on the same reasoning (`finding_attribution_fanout_test.go:37-44`, `finding_staging_gate_test.go:73-81`): it carries no decision, and a file calling `trailIsRunOutcome` is by design inside the `trail*` family's reach. A twin would only fork the 512-byte cap. |
| `reachMaxCommandBytes`, `reachCapCommand` | `background_reach_probe_test.go:123`, `:945` | The single-sourced cap. |

**Identifier prefix `finTrailer*`.** Census re-run at `3582765` against a known-taken control:

```
grep -rn '\bfinTrailer' internal/e2e/realclaude/ | wc -l   # 0     (free)
grep -rn '\btrail[A-Z]'  internal/e2e/realclaude/ | wc -l   # 935   (control non-zero)
```

`finAttribute*`, `finOutcome*` and `finDetail` are on main; `finGather*` is on the in-flight `origin/feature/1281` branch — checked, no `finTrailer*` there. Sibling tickets own `finRecord*`, `finWrite*`, `finGather*`, `finStage*`: do not define those here.

---

## Concurrency model

None, and that is the design. `finTrailerBuild` is pure over its inputs: no goroutine, no lock, no clock, no channel, no `context.Context`. Every AC1 row is a hand-filled `trailObservation` — a `trailScan` result with `Staleness` and `BoundFrom` set by hand, per the ticket's Technical Notes.

The one concurrency property worth naming on the type: because the four fields are copied **by value**, a built record shares no memory with the observation it came from. Records built from one observation cannot alias a mutated `*resultTrailer`.

No test in this file starts a goroutine, so `-race` adds nothing here beyond hygiene — run it anyway to match the family's run line.

---

## Error handling

The builder has no error path and returns no error, matching `trailScan`, `trailGate`, `trailAdmitAttribution`, `trailClassifyRun`, `finOutcomeStagingGate`, `tdnClassifyReapLog` and `pinReadState`: **an instrument failure observed mid-turn is a datum to publish, not a reason to abort the turn.**

The three failure modes that would otherwise want an error, and how the design answers each:

| Mode | Answer |
|---|---|
| No trailer was written | The caller's outcome value already names it (`trailOutcomeVoidNoTrailer`). The record carries `State == trailAbsent` and four zero fields. No panic, no error. |
| `State == trailSeen` with a nil `Trailer` (hand-built fixture only) | The `&&` guard's second operand. Four zero fields, Detail names the arm. No out-of-contract value is invented for it — inventing one is out of scope per AC3. |
| An outcome value outside both closed sets | Carried as handed. Validation is explicitly out of scope; the reader-facing predicates are where membership lives. |

There is deliberately **no** consistency check between `Outcome` and `State`. A disagreement between them has no value in either closed set, and AC3's enforcing rows depend on the builder passing such pairs through untouched.

---

## Testing strategy

Four test functions, all offline, all table- or subtest-driven per `CODING-STYLE.md`. Run line for the file header:

```
go test -race -tags e2e_realclaude -run '^TestFinTrailer' -v ./internal/e2e/realclaude/
```

Write scenarios, not pre-written bodies. Each bullet is one row or one subtest.

### 1. `TestFinTrailerRecordCarriesTheBoundAndItsDiscriminator` (AC1)

Four rows, each a hand-filled `trailObservation` over a real `trailScan` result. Assert `State`, `BoundFrom`, `Staleness` and `Bounded` on the built record.

- **seen + `trailBoundFromMiss` + 250 ms** → `Bounded` true. The only row on which it is.
- **seen + `trailBoundFromStart` + 250 ms** → `Bounded` false. A *live* pairing: `trailWaitForTrailer:257` sets a real duration on the first-poll-matched path, and it bounds nothing.
- **absent (`trailFixtureNoTrailer`) + `trailBoundNone` + 0** → `Bounded` false. The shape a real timed-out poll returns.
- **aborted (`trailPaddedTrailer(trailOverlongPad)`) + `trailBoundNone` + 250 ms** → `Bounded` false. **Synthetic:** both `trailBoundNone` return sites (`:264-265`, `:270`) leave `Staleness` zero, so no run produces this row. Keep it — it is what kills a `Staleness != 0` derivation — and say in the row's own comment that it is a contract check on a builder pure over its inputs, not a claim that a run produces it.

The three `State` values and the three `BoundFrom` values are each covered; the two non-`Miss` discriminators each appear with a non-zero `Staleness`, which is the pairing that bites.

**Mutation check the developer must run:** change `Bounded` to `obs.Staleness != 0` and confirm rows 2 and 4 go red. If they do not, the rows are not doing their job.

### 2. `TestFinTrailerRecordReadsTheDecodedTrailer` (AC2)

Two subtests.

- **A no-trailer observation returns rather than panics.** Build from `trailScan([]byte(trailFixtureNoTrailer))` (State `trailAbsent`, `Trailer` nil). Assert the build returns at all — a build that dropped the State check panics on this input, so reaching the assertions *is* the assertion — and that the four fields hold their zero values: `IsError` false (`tool_loop_test.go:200`), the three strings empty. These two are the load-bearing assertions in this test, because the outcome is an input rather than a derivation.
- **The four fields survive a cap that destroys `terminal_reason` in `Line`.** Drive `trailScan(trailPaddedTrailer(2000) + "\n")` — the pad the family already plants with at `result_trailer_observation_test.go:482`. **Assert the precondition on the scan result, do not assume it:** `Line` no longer contains `terminal_reason` (true from pad 142 up; at pad 2000 it is far past). Then pin, **on the built record**, `Subtype == "error_max_turns"`, `IsError == true`, `TerminalReason == "max_turns"`, `StopReason == "end_turn"`. The scan result's own survival is already pinned at `:477-505`; do not restate it.

### 3. `TestFinTrailerRecordOutcomeIsConsumedAsHanded` (AC3)

- **Two disagreement rows**, the only rows that bite:
  - `finOutcomeReadyToClassify` (a `stage-*` value) beside a `trailAbsent` scan.
  - `trailOutcomeVoidNoTrailer` beside a `trailSeen` scan.

  Assert `rec.Outcome` comes back exactly as handed. A builder that derived the outcome from `State` changes the value on precisely these rows and on no others; an agreeing row would pin nothing. Say in the test that these are contract checks on a projection pure over its inputs, not claims that such a run occurs.
- **Every carryable value is one a reader can look up, and the two sets are disjoint.** Range over `trailRunOutcomeValues()` and `finOutcomeValues()`, calling the two shipped predicates. Do not restate either set.
  - each of the eleven: `trailIsRunOutcome` accepts, `finOutcomeIsValue` rejects;
  - each of the seven: `finOutcomeIsValue` accepts, `trailIsRunOutcome` rejects;
  - the union deduped into a set has **18** members, so a carried value resolves to exactly one set.
- **A coverage loop** builds a record for each of the 18 against one fixed observation and pins `rec.Outcome` per value, so no value is silently unexercised.

### 4. `TestFinTrailerRecordCarriesNoCapturedBytes` (AC4)

The non-vacuous leak proof. One subtest, driven from `trailPaddedTrailer(0)`.

- **Plant inside the cap.** `trailPaddedTrailer(0)` renders **385 bytes** against the 512-byte cap, putting `trailNeedle` at offset **104–146** — verified by computation at `3582765`, and the numbers are a property of the pad, so the test asserts rather than trusts them.
- **Assert the precondition on the scan result:** `strings.Contains(scan.Line, trailNeedle)` is true. Without this the whole test is theatre.
- **Then pin zero occurrences anywhere in the built record**: marshal it and assert the needle is absent from the bytes, and assert it separately against `rec.Detail` so the failure message names the channel.
- **Headroom, asserted per row rather than argued in prose:** `reachMaxCommandBytes - len(rec.Detail) >= len(trailNeedle)`. A leak would therefore have *fit* inside the cap rather than being truncated away. This is the #1284 defect made impossible.
- **State in the test why a padded plant would not do:** the needle's offset is `104 + pad`, so the pads this family plants with — 2000 (`result_trailer_observation_test.go:482`, `trailer_admissibility_test.go:553`) and `trailOverlongPad` — carry it past the cap, and a past-the-cap plant passes against a record that kept the capped `Line`. That would be a green proof of a false claim.
- **`Line` is the only channel swept.** The four decoded fields cross into the record verbatim *by design* (AC2), so a needle planted in them would be pinning against AC2 rather than for it. Say this in the test.

**Mutation check the developer must run, and this one is not optional.** #1284 shipped a needle test whose guard mutations reddened while the *security* mutation silently passed. Before committing:

1. Temporarily append ` %s` + `obs.Line` to the seen arm's Detail. Re-run. **The test must go red.** If it goes green, the Detail is over budget and the cap ate the needle — shorten the Detail, do not weaken the assertion.
2. Temporarily add a `Line string` field to `finTrailerRecord` filled from `obs.Line`. Re-run. **The test must go red.**
3. Revert both.

### AC5 — offline, structurally

Discharged the way the siblings discharge it (`finding_staging_gate_test.go:12`, `finding_attribution_fanout_test.go:11`, `trail_run_outcome_test.go:11`): a header line stating *no live claude, no credentials, no daemon, no subject process, no process-table read, no env gate, no `t.Skip`*, plus the absence of the imports that would make it false. Neither `probeClaudeVersion` (`background_trigger_probe_test.go:606`) nor `resolveClaudeBin` (`resilience_test.go:282`) is called. Code-review can check it in one line:

```
grep -nE 't\.Skip|os/exec|exec\.Command|probeClaudeVersion|resolveClaudeBin' \
  internal/e2e/realclaude/finding_trailer_evidence_test.go   # expect no matches
```

A `t.Skip` here would exit 0 and read as a pass under `make e2e-realclaude` — the failure mode the AC exists to prevent.

---

## Scope

**In:** one new file; one record type; one builder; four tests.

**Out** (each already owned):

| Not here | Owner |
|---|---|
| Exit code, matched rows, per-pid liveness, reap attribution, claude version, runner path | #1291, the ticket that embeds this record whole |
| The artifact writer and its full multi-input no-captured-bytes sweep | #1286 — AC4's single targeted proof is this record's own construction claim and does not restate that sweep |
| Re-deriving the eleven or the seven | Their shipped predicates |
| Re-parsing pyry's stderr for the reap line | `tdnClassifyReapLog` (`teardown_liveness_test.go:144`) |
| Validating the outcome field; capping or sanitising the four decoded fields | Nobody — explicitly out of scope, published as read |
| Any live claude or staged turn; `PYRY_USE_STREAMJSON=1` (#1237); turn accounting (#1234); interactive turn state (#1227); post-exit reap survival (#1231) | as listed |

**No `docs/knowledge/codebase/1290.md` AC.** The documentation phase writes it after merge.

---

## Scope self-check

Production source files (non-test `.go`) created or modified by this spec: **0**. New test files: **1**. Well under the 5-file gate.

Red lines, counted rather than argued:

| Red line | This ticket |
|---|---|
| > 3 new files | 1 |
| > ~600 lines total written | projected **~500–600**, all of it test code (there is no production half to undercount) |
| > 5 new exported types/interfaces | 0 exported; 1 struct + 1 func, both package-private |
| > 10 consumer call sites needing simultaneous update | **0** — purely additive; nothing on main references `finTrailer*` |
| > 5 acceptance criteria | exactly 5 |
| ≥ 10 distinct error/reject branches | **1** branch (`carriesTrailer`); no reject arms, no out-of-contract value, no validation |

The LOC projection is the only line near its bound, so it is projected rather than asserted: header ~45, imports ~8, type + doc ~55, builder + doc ~50, and four tests at ~90/85/95/70. The nearest sibling by shape, `result_trailer_observation_test.go` (617 lines), ships **two** functions including a polling loop, two record types and a goroutine-driven timing test; this ticket is strictly simpler, so 617 is a ceiling rather than a target. The turn budget is not at risk from an edit cascade because there is none: one new file, zero edits to existing files, zero call sites.

**Operator calibration note (carried forward from #1285's split, unchanged).** This family's merged files run 594–1301 lines at `size:s` with no `max_turns` (median 790). The ~600-line red line therefore splits every ticket in the chain even where the turn cost is demonstrably low, because the cost driver in this family is Edit-cascade rather than line count and these tickets have no cascade. #1290 was already produced by applying that red line to #1285; the only further seam available is a field-wise cut of a single record, which would put child B in child A's struct and tests — the exact cascade shape the red lines exist to prevent — and would leave AC4's whole-record leak proof unprovable until the sibling landed. Not split. Flagging the rule-vs-actuals gap for the operator, not resolving it here.

---

## Open questions

1. **Detail wording per arm.** The spec fixes the content rule and the 470-byte budget but not the prose. Developer's call, inside those two constraints.
2. **Pad choice in AC2's second subtest.** 2000 matches the family's existing plant at `:482`. Any pad ≥ 142 satisfies the precondition, and the test asserts it rather than assuming — so a later fixture change surfaces as a failed precondition rather than as a silently weaker test. Worth knowing: pads in **142–366** satisfy *both* AC2's precondition and AC4's (needle inside the cap, `terminal_reason` outside it). The ACs mandate separate tests with separate pads, and this spec follows them; the overlap is recorded only so a future consolidation does not have to rediscover it.
3. **`stop_reason`'s unbounded bytes.** See Security review finding 1. Naming it on the type is in scope; capping it is not, and is #1286's decision to revisit if it ever wants one.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] SHOULD FIX — name `stop_reason` as the one model-influenced field that crosses uncapped.** The boundary is single and explicit: `finTrailerBuild` is the only function that reads the observation, and the output type has no field that can hold a line. But `stop_reason` is forwarded from claude's own message unvalidated (`emitter.go:210`), is not derived from pyry's `ExitReason` like the other three, and is published **verbatim and uncapped** into a record an operator pastes into a public issue. A trailer carrying an over-long or attacker-chosen `stop_reason` would ride through. Not MUST FIX: capping the four decoded fields is explicitly out of scope per the ticket, the threat needs a hostile or compromised claude (past this probe's threat model, the same framing `reachMaxCommandBytes`' own comment uses at `background_reach_probe_test.go:117-122`), and AC2 requires the field be carried whole. **Required in the spec and already in the Design:** the type's doc names `stop_reason` as the sole independently-sourced field and states that its bytes are bounded by the wire's shape and by nothing this record does — so #1286's multi-input sweep does not later plant a needle in a field this record must carry, and so a future capping decision has a documented starting point.
- **[Error messages, logs, telemetry] SHOULD FIX — the Detail is the log surface, and its cap can hide a leak.** `trailDetail` truncates at 512 bytes. If a Detail interpolates the four decoded fields (permitted) and one of them is long, `len(Detail)` grows and the cap can swallow leaked bytes, turning AC4's sweep green against a record that leaked — the defect #1284 shipped. Mitigated in-spec by the **per-row** headroom assertion (`reachMaxCommandBytes - len(rec.Detail) >= len(trailNeedle)`), the stated 470-byte budget, and the mandatory Detail-interpolation mutation check. Not MUST FIX because the mitigation is an AC requirement rather than a hoped-for developer habit.
- **[Trust boundaries — the primary one] No further findings.** The untrusted→published edge is `claude stdout → trailScan → Line → (must reach no field)`. It is severed structurally in the output type (no `Line`, no `*resultTrailer`, no embedded observation — ten scalars) and held in the builder by the Detail content rule plus AC4's non-vacuous needle test with an in-cap plant. The spec explicitly separates "the type is trap-free by construction" from "the builder has the line in reach", so a reader cannot mistake the former for covering the latter.
- **[Network & I/O — input size limits] No findings.** Every ingress is already capped upstream: `bufio.Scanner`'s 64 KiB per line (whose breach is the `trailAborted` state rather than a silent truncation), `reachCapCommand`'s 512 bytes on `Line`, and `trailDetail`'s 512 on the Detail. The cap is single-sourced through `reachMaxCommandBytes`; the spec forbids a `finDetail` twin precisely so it stays that way. The one uncapped surface is finding 1.
- **[Subprocess / external command execution] Not applicable by construction, not by assertion.** No `os/exec` import, no `exec.Command`, no `probeClaudeVersion`, no `resolveClaudeBin`, no `t.Skip` — AC5, discharged by the file's import list and checkable with the one-line grep in the Testing strategy. A `t.Skip` would exit 0 and read as a pass under `make e2e-realclaude`, which is why the grep is written into the spec for code-review rather than left to reading.
- **[File operations] Not applicable.** The builder performs no I/O: no path is constructed, opened, stat-ed or written. The artifact writer that does touch the filesystem is #1286.
- **[Tokens, secrets, credentials] Not applicable, with the reason stated.** Nothing in the file reaches a credential: no env read, no `CLAUDE_CODE_OAUTH_TOKEN`, no `HOME`, no daemon socket. The fixtures carry a synthetic `session_id` (`11111111-2222-3333-4444-555555555555`) and no captured transcript, so no operator path, hostname or token can enter a record built here.
- **[Cryptographic primitives] Not applicable.** No randomness (no `math/rand`, no `crypto/rand` — AC1's rows are fixed), no key material, and no comparison against a secret. `trailNeedle` is a public test constant, so `strings.Contains` is the right comparison and constant-time comparison is irrelevant.
- **[Concurrency] No findings.** The builder is pure: no goroutine, no lock, no channel, no clock, no shared state, so there is no lock ordering, no TOCTOU and no goroutine to leak. One property worth the doc line it gets: the four fields are copied **by value**, so a built record shares no memory with the `*resultTrailer` it was read from and two records built from one observation cannot alias.
- **[Threat model alignment] Aligned; the residue is named.** The threat this record serves is the one `reachMaxCommandBytes`' own comment states — attacker-chosen text reaching an artifact an operator pastes into a public issue. Addressed: no line in any form, an in-cap plant so the proof is non-vacuous, and per-row headroom. Residual and named: `stop_reason` (finding 1), and the full multi-input sweep across every artifact input, which is **#1286's** and is not restated here.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
