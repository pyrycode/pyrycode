# #1286 — Render the run record into a pasteable artifact and prove it leaks no captured bytes

**Size:** S (confirmed — see § Sizing). **New files:** 1 test file. **Production `.go` files touched:** 0.
**Line references verified at `4bc5f5b`**, with #1290 and #1291 merged.

---

## Files to read first

The whole design lives in one Go package (`internal/e2e/realclaude`, build tag `e2e_realclaude`), so every
symbol below is file-local and directly callable. Read these before writing anything — the design is almost
entirely *reuse*, and each entry names the one thing to extract.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_record_test.go:1-80` | The file header this ticket's header must mirror: the offline discipline, and — critically — the **forbidden exec routes named by file:line rather than by symbol**, so the forbidden-symbol grep reports on code and cannot be defeated by prose. |
| `internal/e2e/realclaude/finding_run_record_test.go:113-199` | `finRecordProc` (three ints) and `finRecordRun` (ten fields, four of them struct- or slice-valued). This is the type the census walks. |
| `internal/e2e/realclaude/finding_run_record_test.go:222-242` | `finRecordInputs` — the eight named fields the fixture fills. Note `Rows` and `ClaudeCommand` are the two argv-bearing inputs. |
| `internal/e2e/realclaude/finding_run_record_test.go:360-392` | `finRecordBuild` — the two argv reduction points (`:372-374`, `:379`) and the Detail format. **M1's mutation goes inside this Detail format.** |
| `internal/e2e/realclaude/finding_run_record_test.go:400-494` | Shipped fixtures: `finRecordMatchedRows(suffix)`, `finRecordEnvDelta()`, `finRecordProofAttribution()`, `finRecordSeenTrailer()`, the four pid constants. |
| `internal/e2e/realclaude/finding_run_record_test.go:731-756` | `finRecordInputReaches` — the shipped recursive **type** walker. Reused verbatim by AC4's structural check; do not write a second one. |
| `internal/e2e/realclaude/finding_run_record_test.go:919-1061` | `TestFinRecordCarriesNoCapturedBytes` — the sweep this ticket extends. Copy its non-vacuity precondition, its per-row headroom assertion and its per-channel naming. Its comment at `:926-927` and `:934-944` scopes exactly what is left here. |
| `internal/e2e/realclaude/background_reach_probe_test.go:820-855` | `writeReachArtifacts` — the shape AC1 asks for, **and the anti-pattern**: it writes `rec.rawPS` to a second file. That sidecar is the leak this ticket exists to prevent. |
| `internal/e2e/realclaude/background_reach_probe_test.go:117-126, :945-950` | `reachMaxCommandBytes = 512` and `reachCapCommand`. Single-sourced; never restate 512. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:98-121` | `trailScanResult` — `Line` is the capped verbatim line (**dropped** by `finTrailerBuild`); `Trailer` is the decode of the **full** line. |
| `internal/e2e/realclaude/result_trailer_observation_test.go:295-315` | `trailNeedle` (42 bytes) and `trailPaddedTrailer(pad)`. **The comment on `trailNeedle` says "placed PAST the cap" — against `trailPaddedTrailer(0)` that is not what happens.** See § The trailer plant. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:142-155, :203-233` | `finTrailerRecord` (ten scalars, no `Line`) and `finTrailerBuild`. Confirms the trailer line is dropped, not capped. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:623-697` | `TestFinTrailerRecordCarriesNoCapturedBytes` — the non-vacuity precondition idiom (`:632-640`) and the **flat** key scan (`:684-696`) AC3 generalises. |
| `internal/e2e/realclaude/finding_trailer_evidence_test.go:430-470` | `TestFinTrailerRecordReadsTheDecodedTrailer`'s "the four fields survive a cap that destroys terminal_reason in the line" subtest. **Already merged — AC4 references it and must not restate it.** |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:79-125` | `finAttributeEntry` / `finAttributeRecord` — the five keys the census expects, and which of them are `omitempty`. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:209-296` | `finAttributeFanOut` — Step 2 (`pgid <= 1` → `Unreportable` + `finAttributeGroupUnreportable`) is what lets one call fill every `omitempty` field. |
| `internal/e2e/realclaude/finding_attribution_fanout_test.go:729-767` | `TestFinAttributeRecordCarriesNoCapturedBytes` — the reap-stderr plant (`trailReapLine(1, "[7788] "+trailNeedle)`) reused verbatim, and the second flat key scan (`:758`). |
| `internal/e2e/realclaude/process_pin_liveness_test.go:239-253` | `pinStateOutcome` — seven fields, three of them `omitempty`. The census fixture must fill all seven. |
| `internal/e2e/realclaude/tool_loop_test.go:194-203` | `resultTrailer` — **no `result` member**. AC4's structural check walks this type. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:199-208, :531-537` | `trailAdmitResult` (two fields) and `trailReapLine(count, pgids)`. |
| `internal/e2e/realclaude/teardown_liveness_test.go:113-127` | `tdnReapOutcome.Line` — pyry's own captured stderr, the fourth plant channel, dropped by the fan-out. |

---

## Context

#1291 shipped `finRecordRun`: one probe run's publishable record. It is built, it is tested, and it is
clean. What it is not yet is **rendered** — there is no writer that puts it on disk in a form an operator
pastes into a public issue, and no proof that the *act of rendering* is as safe as the record.

Those are two different claims, and the gap between them is exactly `writeReachArtifacts`
(`background_reach_probe_test.go:823`): it marshals a clean record into `reach.json` and then writes
`rec.rawPS` — the verbatim process table, full argv, every operator command line — into `reach.ps.txt`
beside it. A clean JSON blob next to a raw-bytes sidecar publishes the raw bytes. **The record's safety
property does not transfer to the directory.**

This ticket closes that gap: a writer, and four enforcing tests over what the writer actually wrote.

### The record carries no verbatim model output at all

Earlier revisions of this ticket assumed the record retained exactly one such field — the capped trailer
line — and built their proof around marking it for operator review. It does not. `finTrailerRecord`
(`finding_trailer_evidence_test.go:142`) is ten scalars with no `Line`, and `finRecordInputs`
(`finding_run_record_test.go:222`) carries neither a `trailObservation` nor a `trailScanResult`, so
`trailScanResult.Line` is unreachable from this record at any depth — a property
`TestFinRecordEmbedsTrailerRecordWhole` (`:761`) already pins with `finRecordInputReaches`.

Every field the record carries is a count, an integer, an enumerated verdict, a decoded trailer scalar,
or a rig-authored string. The artifact's claim is therefore the **stronger** one — *this record carries no
verbatim model output* — and it needs a different proof than "the one retained field is marked".

---

## Design

One new file: **`internal/e2e/realclaude/finding_artifact_write_test.go`**, build tag `e2e_realclaude`,
identifier prefix **`finWrite*`** (census at `4bc5f5b`: `finWrite[A-Z]` = 0 in the worktree and on every
`origin/feature/*` branch; control `trail[A-Z]` = 1098).

### The writer

```go
// finWriteArtifacts renders one built record into dir as a pasteable artifact.
func finWriteArtifacts(t *testing.T, dir string, rec finRecordRun)
```

Writes exactly two files:

- **`run.json`** — `json.MarshalIndent(rec, "", "  ")` plus a trailing newline, mode `0o600`, in
  `writeReachArtifacts`'s shape (`background_reach_probe_test.go:826-851`).
- **`run.md`** — the pasteable note: the standing safety claim, then a fenced ```json block holding the
  same bytes, then one summary line formatted from **derived scalars of `rec` only**.

**The signature is the design.** `finWriteArtifacts` takes the built record and nothing else. That is
`finRecordProc`'s doctrine applied one layer out (`finding_run_record_test.go:92-112`): prefer the shape
that cannot be got wrong over the discipline that must not be. `writeReachArtifacts` takes `*reachRecord`,
whose unexported `rawPS` field is what produces `reach.ps.txt` — the writer here has no such parameter and
`finRecordRun` has no unexported field, so **there is nothing raw in the writer's reach to write out.**
Do not widen this signature to `finRecordInputs`, and do not add a second `[]byte` parameter; M2 below is
the only place that widening is allowed, and it is a mutation to be reverted.

Errors follow `writeReachArtifacts`: `t.Errorf` and continue, never `t.Fatalf` — a lost artifact is loud
but does not abort the remaining cleanups.

`run.md`'s note must **not** contain the phrase `operator-review-before-paste` (or `OPERATOR-REVIEW`).
The record has no such field, and a note carrying the caveat would train a reader to look for a field that
is not there. The *explanation* of why the caveat is absent belongs in the writer's doc comment and in the
test comment — neither of which is written into the artifact. Scoping the rule by layer rather than by
"anywhere in the file" is #1280's lesson.

The claim sentence is a package constant so the writer and the test pin the same bytes rather than two
literals that can drift:

```go
const finWriteSafetyClaim = "This record carries no verbatim model output and no verbatim argv: " +
    "every field it holds is a count, an integer, an enumerated verdict, a decoded trailer scalar, " +
    "or a string this rig authored."
```

### One fixture, always planted

```go
// finWriteInputs returns the maximal input set: every omitempty field non-zero,
// and trailNeedle planted in every input the pipeline reduces or drops.
func finWriteInputs() finRecordInputs
```

All four tests build from this one fixture, write into their own `t.TempDir()`, and read the directory
back. One build, one write, four claims. The fixture is **always planted** — the plants land only in
inputs the pipeline drops, so they change nothing about *which* keys render, and the census (AC1) is
therefore unaffected by them.

Field by field, and which of the two categories each falls in:

| `finRecordInputs` field | Value | Category |
|---|---|---|
| `ExitCode` | `0` | reduced-to-itself; renders (no `omitempty`) |
| `Rows` | `finRecordMatchedRows(" " + trailNeedle)` | **reduced** → three ints per row. Plant #1. |
| `Liveness` | one hand-built `pinStateOutcome` with **all seven fields non-zero** | **carried whole** — no plant. |
| `Attribution` | `finAttributeFanOut(finWritePlantedReapLog(), []int{1, finRecordSharedPGID}, "completed")` | the stderr is **dropped**. Plant #4. |
| `Trailer` | `finTrailerBuild(trailOutcomeVoidBudgetFired, obs over trailScan(trailPaddedTrailer(0)))` | the scan's `Line` is **dropped**. Plant #3. |
| `RunnerFromEnv` | `reachRunnerPathFromEnv(finRecordEnvDelta())` | carried whole — no plant. |
| `ClaudeCommand` | `tdnFixturePtyArgv + " " + trailNeedle` | **reduced** to one of three constants. Plant #2. |
| `ClaudeVersion` | `"2.1.220 (Claude Code)"` | carried whole (capped) — no plant. |

**Plant only where the pipeline reduces.** This is the trap that makes an over-broad plant list red
against a *correct* build. `Liveness`, `Attribution`, `Trailer`, `RunnerFromEnv` and `ClaudeVersion` are
carried into the record **whole, by design** — `finding_run_record_test.go:137-151` states it and
`TestFinRecordEmbedsTrailerRecordWhole` pins it. A needle in `pinStateOutcome.ToolStderr`, in
`finAttributeRecord.Detail`, or in `finTrailerRecord.Subtype` **will** appear in the artifact, correctly,
and AC2 would go red against a build with no defect. `finding_run_record_test.go:996-1014` asserts the two
embedded sub-records clean *before* building, for exactly this reason; **carry that precondition forward**
— the sub-records here are built from planted *inputs*, and the assertion is that the plant did not
survive the sub-builder, so a red AC2 names this ticket's writer rather than a sibling's builder.

The `Liveness` fixture is hand-built. That is not a shortcut: `pinReadState`
(`process_pin_liveness_test.go:265`) execs `ps`, which this file forbids, so a shipped-producer fixture is
unavailable here. Say so in the fixture's comment. Its `ToolStderr` must be **non-empty and
rig-authored** (e.g. `"ps: no such process"`) — non-empty so `omitempty` renders the key for the census,
rig-authored because the field is carried whole and a needle there is plant-in-the-wrong-place.

### The trailer plant lands INSIDE the cap, and the shipped comment is wrong about it

`trailNeedle`'s own comment (`result_trailer_observation_test.go:297-300`) says it is "placed PAST the
cap". Against `trailPaddedTrailer(0)` that is not what happens, and the difference decides whether AC2
proves anything. Measured at `4bc5f5b`:

| `pad` | line bytes | needle at | needle ends | inside the 512-byte cap? |
|---|---|---|---|---|
| 0 | 385 | 104 | 146 | **yes** |
| 200 | 585 | 304 | 346 | yes (but `terminal_reason` is cut from `Line`) |
| 366 | 751 | 470 | 512 | yes, exactly |
| 367+ | 752+ | 471+ | 513+ | no |

**Use `pad = 0`.** `trailScan` records the needle into `Line` intact
(`result_trailer_observation_test.go:176-182`), so the needle's absence downstream proves that
`finTrailerBuild` **dropped a line it actually held**. A needle planted past the cap never reaches
`Line` in the first place, so its absence downstream is a green assertion about nothing — #1290 AC4
established this same plant position for this same reason. State the reasoning in the test, not just the
`pad` argument, and assert it rather than trusting the table: reuse
`finding_trailer_evidence_test.go:632-640`'s precondition shape —

- `strings.Index(line, trailNeedle) + len(trailNeedle) > reachMaxCommandBytes` → `t.Fatalf` (needle past
  the cap; the test would be vacuous)
- `!strings.Contains(scan.Line, trailNeedle)` → `t.Fatalf` (the scan did not retain it; likewise vacuous)

### Three helpers

```go
// finWriteReadDir reads every regular file in dir, keyed by base name.
func finWriteReadDir(t *testing.T, dir string) map[string][]byte

// finWriteDeclaredKeys collects every json tag name reachable from typ,
// recursing through struct fields and slice/array/pointer element types.
func finWriteDeclaredKeys(typ reflect.Type, seen map[reflect.Type]bool) map[string]bool

// finWriteObservedKeys collects every key present in a decoded JSON value,
// recursing through objects and arrays.
func finWriteObservedKeys(raw json.RawMessage, into map[string]bool) error
```

`finWriteReadDir` uses `os.ReadDir` and reads **every** entry it finds — never a hard-coded list of the
two file names. That is the whole point: a later edit that adds a third file inherits the sweep for free,
and `reach.ps.txt` is the proof that "a later edit adds a third file" is a thing that happens.

`finWriteDeclaredKeys` is the *type* walk; `finWriteObservedKeys` is the *value* walk. Both are recursive
for the reason `finding_run_record_test.go:934-944` states: `finRecordRun` has four struct- or
slice-valued fields, so a top-level scan inspects ten keys and misses every nested one — "vacuous coverage
is worse than none". `finRecordInputReaches` (`:731`) is the shipped precedent for the `seen` map closing
the walk; do not write a second type walker where that one already answers the question (AC4 calls it
directly).

---

## Acceptance criteria → tests

Four tests, one file. Each names its AC in its doc comment.

### AC1 — `TestFinWriteArtifactRendersEveryDeclaredField`

Renders `finWriteInputs()` into a `t.TempDir()`, decodes `run.json`, and asserts
`finWriteDeclaredKeys(reflect.TypeOf(finRecordRun{}), …)` and `finWriteObservedKeys(run.json)` are
**set-equal**, reporting the two directions separately:

- *declared ∖ observed* — "a field the record declares is absent from the artifact; an operator reads
  absence as 'not measured'". This is AC1's stated purpose.
- *observed ∖ declared* — "the artifact renders a key the record's type does not declare".

Equality rather than containment costs nothing and is strictly stronger.

The fixture is what makes this bite. The `omitempty` fields — `matched_rows`, `liveness`, `conditions`,
`unreportable_pgids`, `entries`, `ppid`, `state_column`, `tool_stderr` — are all non-zero in
`finWriteInputs()`, and each is one a lazier fixture would silently drop. `#1291`'s liveness fixture is
`{Verdict, PID}`, which renders four of `pinStateOutcome`'s seven keys; that gap is precisely what this
census closes.

Add a **non-vacuity precondition**: `len(declared) >= 30` with a message saying the walk collected
implausibly few keys and the equality below would then be trivially satisfiable. (The real count at
`4bc5f5b` is the union across `finRecordRun` (10), `finRecordProc` (3), `pinStateOutcome` (7),
`finAttributeRecord` (5), `finAttributeEntry` (2), `trailAdmitResult` (2) and `finTrailerRecord` (10),
less the names shared across types — `detail`, `pid`, `ppid`, `pgid`, `value`. Assert the floor, not the
exact number, so adding a field to a sibling's record is not a failure here.)

This census is also what stops AC2 from passing against a writer that renders nothing.

### AC2 — `TestFinWriteArtifactsCarryNoCapturedBytes`

The enforcing sweep. Order matters:

1. **Non-vacuity, per plant channel, before the build.** Four checks, each `t.Fatalf` with its own
   message naming the channel: each input row's `Command` carries the needle; the claude argv carries it;
   the trailer scan's `Line` carries it *and ends inside the cap* (both checks, per § The trailer plant);
   the reap stderr carries it and `finAttributeFanOut` returned one entry reading `trailAdmitProof`
   (`finding_attribution_fanout_test.go:737-741`'s premise-doubles-as-non-vacuity idiom — the value is
   reachable only if the needle-bearing line was recognised, parsed and matched).
2. **The two embedded sub-records asserted clean before the build**, per
   `finding_run_record_test.go:996-1014`, so a red sweep names this ticket's writer.
3. **Per-row Detail headroom, asserted on the rendered output.** For `rec.Detail` and for every `Detail`
   the artifact carries — the record's, the attribution's, each entry's `Admit.Detail`, the selected
   `Admit.Detail`, the trailer's, and each liveness outcome's — assert
   `reachMaxCommandBytes - len(detail) >= len(trailNeedle)` (i.e. **under 470 bytes**). #1291 asserts this
   on the built record (`:1036`); this ticket asserts it on what was actually written. That is #1284's
   shipped defect: house-style multi-sentence Details ate the same 512-byte budget, so a leak was
   truncated away before the needle and the sweep passed against a leaking writer. Name the offending
   Detail's JSON path in the failure message.
4. **The sweep.** For every file in `finWriteReadDir(dir)`, `bytes.Contains(content, []byte(trailNeedle))`
   must be false. Iterate the map — never the two known names — and name the file in the failure.
5. **State why zero, in the test.** The record carries no trailer line in any form, capped or otherwise
   (`finTrailerRecord` has no `Line`; `finRecordInputs` carries no `trailScanResult`), so a single
   occurrence means a reduction was widened back into a retention. And state why the trailer plant is
   in-cap: past the cap the needle never reaches `trailScanResult.Line` and its absence downstream would
   prove nothing.

**The mandated mutations.** A redaction assertion that stays green when the redaction is removed is a
failure mode this family has shipped once. Both mutations below must be applied, observed **RED**, and
reverted; name them and their observed result in the test's doc comment.

- **M1 — the pipeline sweep is live.** In `finRecordBuild` (`finding_run_record_test.go:386-390`), change
  the Detail format to interpolate `in.Rows[0].Command` — the `%v`-on-an-input slip that
  `finRecordRun`'s own comment names at `:168-173`. **Inject it inside the format string, not appended
  after `trailDetail` returns.** Appended, `reachCapCommand` has already run and the needle survives
  regardless, which proves nothing about #1284's defect; injected inside, the cap applies and step 3's
  headroom assertion is what keeps the needle visible. Expected: RED, naming **both** `run.json` and
  `run.md`. Red on only one file means the directory walk is not reading every file.
- **M2 — the directory walk is live, and does not rest on key names.** Temporarily widen
  `finWriteArtifacts` to take `in finRecordInputs` alongside the record and write a third file
  `run.notes.txt` containing `fmt.Sprintf("evidence: %s", in.ClaudeCommand)`. This is `reach.ps.txt` in
  miniature. Expected: RED, naming `run.notes.txt` — a file no test was told about. The key is
  **`evidence`** deliberately: it matches none of AC3's shape list, so a green result could not have been
  resting on key names. Revert the signature widening; the writer ships taking the record alone.

### AC3 — `TestFinWriteArtifactCarriesNoCapturedByteShapedKey`

Walks `run.json`'s keys **recursively** via `finWriteObservedKeys` and asserts no key is argv-, line- or
stderr-shaped. Forbidden substrings — the union of the two flat scans this family ships
(`finding_trailer_evidence_test.go:688`, `finding_attribution_fanout_test.go:758`), less the redundant
`trailer_line`:

```
command, args, comm, argv, line, stderr, result, raw
```

**Two exact-key exemptions, each with its own stated reason.** Both are exemptions by *exact key* and
never by a prefix or substring rule, so a later field genuinely named for a captured column still trips
the scan:

- **`tool_stderr`** — a shipped and permitted key on the carried `pinStateOutcome`
  (`process_pin_liveness_test.go:252`). #1281 settled that a forbidden-key sweep meeting this key defuses
  by exact-key exemption, and `finding_run_record_test.go:148-151` names the sweep as this ticket's
  problem rather than a reason to strip the field.
- **`runner_from_argv`** — matches the substring `argv`, and it is not named in the ticket body. Its
  reason is different from `tool_stderr`'s and must be stated as such: the key names the **reading**, not
  the argv. Every arm of `tdnRunnerFromArgv` returns a constant
  (`teardown_liveness_probe_test.go:773-789`), so no input byte reaches its value; the field's space is
  the closed set `{ptyrunner …, streamrunner …, indeterminate …}`, and AC2's plant #2 is the enforcing
  proof of that. **This exemption is required for the test to be green against a correct record** — the
  family's shipped list would otherwise fail on a field that carries no bytes.

Add a **non-vacuity precondition** for the exemption mechanism itself: assert both exempted keys are
actually present in the observed set. An exemption for a key the artifact does not render is an exemption
that could be quietly hiding a stricter rule's failure; if `tool_stderr` stops rendering (an empty
`ToolStderr` under `omitempty`), that must be a loud failure here, not a silent widening of the exemption.

State in the test why the walk is recursive: a top-level scan on `finRecordRun` inspects ten keys and
misses every nested one (`finding_run_record_test.go:934-944`).

### AC4 — `TestFinWriteArtifactPublishesNoVerbatimModelOutput`

Three subtests, over the same rendered artifact.

1. **Structural — `resultTrailer` has no `result` member.** Walk `reflect.TypeOf(resultTrailer{})`'s
   fields and assert no field's json tag (name before the first comma) is `result`. This claim is stated
   in prose in three places today (`result_trailer_observation_test.go:114`,
   `finding_trailer_evidence_test.go:38`, `:616`) and **checked nowhere** — this is the new work. The
   failure message must say what the addition would mean: a `result` member would put the last assistant
   message — roughly 415 of the trailer's retained bytes — into the decode, and from there into the four
   published scalars, in a record whose whole value is that it can be pasted unreviewed.
2. **Behavioural — the four decoded scalars crossed verbatim, the needle beside them did not.** In
   `run.json`, `trailer.subtype == "error_max_turns"`, `trailer.terminal_reason == "max_turns"`,
   `trailer.is_error == true`, `trailer.stop_reason == "end_turn"` — the wire values of
   `trailPaddedTrailer(0)`, whose `result` field held the needle AC2 proved absent. That pair in one
   artifact is the claim: the four fields cross by design, and the payload they sat beside does not.
   **Do not restate #1290's cap subtest.** "The decode ran against the full line rather than the capped
   `Line`" is already pinned by `TestFinTrailerRecordReadsTheDecodedTrailer`
   (`finding_trailer_evidence_test.go:430-470`, using `pad = 200` so `terminal_reason` is cut from
   `Line`). Reference it by file:line in the comment; re-deriving a merged sibling's test is out of scope.
3. **The artifact states the claim.** `run.md` contains `finWriteSafetyClaim`, and contains **neither**
   `operator-review-before-paste` **nor** `OPERATOR-REVIEW`. State in the test why the negative half is
   there and why it is not red-by-construction: the note may not *explain* the absent caveat using the
   caveat's own words — that explanation lives in the writer's doc comment and here, neither of which is
   written into the artifact.

---

## Concurrency model

None. `finWriteArtifacts` is synchronous, spawns no goroutine, takes no lock and has no shutdown
sequence. Every test writes into its own `t.TempDir()`, so `go test -race` running the package's tests in
parallel shares no path and no backing array. The fixture is a **function**, not a package-level var, for
`trailRunWellFormed`'s stated reason (`trail_run_outcome_test.go:608-610`): a shared backing array is
reachable from every test in this package.

## Error handling

| Failure | Behaviour |
|---|---|
| `json.MarshalIndent` fails | `t.Errorf` and return, per `writeReachArtifacts:827-830`. Unreachable in practice — every field is a stdlib type with no `MarshalJSON` — but loud rather than silent. |
| `os.WriteFile` fails | `t.Errorf` naming the path, and **continue to the next file**. A lost artifact is loud but does not abort the remaining writes. |
| `finWriteReadDir` hits a read error | `t.Fatalf` — this is the test's own instrument, and a sweep that silently skipped a file it could not read would report clean on the one file that leaked. |
| Decode of `run.json` fails in a test | `t.Fatalf` — same reason. |

Note the asymmetry, and state it in the file header: the **writer** never fails a test (it is the
instrument under measurement, and an instrument failure is a datum), while the **sweep's own reads** fail
loudly (a sweep that cannot read is a sweep that cannot report). This mirrors the family's pure-builder
contract (`finding_run_record_test.go:320-325`).

## Testing strategy

```
go test -race -tags e2e_realclaude -run '^TestFinWrite' -v ./internal/e2e/realclaude/
make e2e-realclaude
```

Both must pass **offline, in full** — no live claude, no credentials, no network, no daemon, no subject
process, no process-table read, no env gate.

**No `t.Skip`, and no exec.** The file header must follow `finding_run_record_test.go:1-40`: name every
forbidden exec route **by file and line rather than by symbol**, so the forbidden-symbol grep reports on
this file's own code and cannot be defeated by this file's own prose. The routes to name: the version
probe (`background_trigger_probe_test.go:606`), the claude-binary resolver (`resilience_test.go:282` —
it calls `t.Skipf` when claude is absent, and a skip that exits 0 reads as a pass), the worktree
credentials gate (`fixtures.go:96`), the per-pid state read (`process_pin_liveness_test.go:275`, execs at
`:293`), the exit-1 borrow (`:1088`), the argv scan (`:191`), the process snapshot
(`background_trigger_probe_test.go:867`), the teardown scan (`teardown_liveness_probe_test.go:482`) and
the FIFO hold (`background_trigger_probe_test.go:663`).

Verification greps — run both, and note that the second needs the `\b…\(` anchoring because a bare
`t.Skip` matches the header's own prose sentence (#1290's spec shipped that defect):

```bash
grep -nE '\bt\.Skipf?\(' internal/e2e/realclaude/finding_artifact_write_test.go   # expect 0 matches
grep -nE '\bexec\.'      internal/e2e/realclaude/finding_artifact_write_test.go   # expect 0 matches
```

Writing files under `t.TempDir()` is expected and is not an exec. `os.ReadDir`, `os.ReadFile` and
`os.WriteFile` are pure filesystem calls on a directory this test created.

**Verify before starting** (a check that cannot report clean is as useless as one that cannot fail):

```bash
grep -rn '\bfinWrite[A-Z]' internal/e2e/realclaude/ | wc -l   # expect 0
grep -rn '\btrail[A-Z]'    internal/e2e/realclaude/ | wc -l   # control: non-zero (1098 at 4bc5f5b)
```

## Sizing

| Red line | Threshold | This ticket |
|---|---|---|
| New files | > 3 | **1** (+ this spec) |
| Total written lines | > ~600 | **~535** (~340 code, ~195 comment) |
| New exported types | > 5 | **0** (test package, all unexported) |
| Consumer call sites needing simultaneous update | > 10 | **0** — purely additive; no existing symbol's signature or name changes |
| Acceptance criteria | > 5 | **4** |
| Distinct error/reject branches | ≥ 10 | **2** (`json.MarshalIndent`, `os.WriteFile`); no state machine |
| Production source files (`*.go` excluding `*_test.go`) | ≥ 5 | **0** |

Calibrated against measured siblings in this family: #1280, #1284 and #1290 each shipped one new
build-tagged test file with 5 ACs at ~750 total lines / ~354–411 code lines and closed clean at `size:s`
in 3-commit PRs. This ticket is 4 ACs, 4 tests, one fixture, three helpers, no fan-out. **S, no split.**

**File-overlap check** (branch-based, `git fetch origin --prune` at `4bc5f5b`): in-flight
`origin/feature/1260`, `origin/feature/1281` and `origin/feature/363` touch
`internal/e2e/realclaude/`, but none touches a file this ticket touches — this ticket modifies **no
existing file** and adds one new one. `finWrite[A-Z]` is free on every remote feature branch, so the
prefix cannot collide at merge either. No `addBlockedBy` needed.

## Open questions

- **`run.md` vs `run.txt`.** Markdown is chosen because the deliverable is "pasteable into a public
  issue" and GitHub renders the fenced block. If the developer finds the fence interacts badly with the
  marshalled record's own braces, plain text with the same content is an acceptable substitute — the
  tests key on `finWriteSafetyClaim` and on the directory contents, not on the extension.
- **The `>= 30` declared-key floor** is a plausibility guard, not a specification. If the actual union
  comes out lower, lower the floor to the real number minus a small margin and say so in the comment.
  Do not pin the exact count — that turns a sibling's field addition into a failure here.
- **`trailNeedle`'s comment is wrong.** `result_trailer_observation_test.go:297-300` says the needle is
  "placed PAST the cap"; against `trailPaddedTrailer(0)` it ends at byte 146 of 385. Correcting that
  comment is out of scope for this ticket (it is a sibling's file and the ticket adds no reason to touch
  it), but this spec records the measurement so a later reader is not misled. Worth a follow-up issue.

## Out of scope

The record type and its builders (#1290, #1291 — both merged); the staging-gate outcome tier; any live
claude or staged turn; re-deriving the eleven-outcome set; restating #1291's argv-conversion sweep;
re-parsing pyry's stderr for the reap line (`tdnClassifyReapLog`, `teardown_liveness_test.go:144`, owns
that read); restating #1290's full-line-decode cap subtest. The headless `PYRY_USE_STREAMJSON=1` path is
#1237; turn accounting is #1234; the interactive turn-state surface is #1227; what survives the reap after
pyry's exit is #1231.

Per the architect's standing rule, `docs/knowledge/codebase/1286.md` is **not** a developer deliverable —
the documentation phase writes it from this spec plus the merged diff.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[1 — Trust boundaries]** No MUST FIX. This ticket adds exactly one boundary and it is explicit: the
  `finWriteArtifacts(t, dir, rec finRecordRun)` signature. Everything untrusted — verbatim argv from the
  ambient process table, pyry's captured stderr, the model's `result` payload — is reduced or dropped
  *upstream* of it by `finRecordBuild` (`finding_run_record_test.go:372-379`), `finAttributeFanOut` and
  `finTrailerBuild`, each with its own merged enforcing test. The boundary is a type, not a convention:
  `finRecordRun` has no field able to hold captured bytes and no unexported field, so the writer has
  nothing untrusted in reach. The spec forbids widening the signature (§ The writer) and M2 exists
  specifically to demonstrate what the widened version leaks.
- **[2 — Tokens, secrets, credentials]** No MUST FIX, one concrete threat addressed by construction. The
  real credential exposure in this package is `ps -E` / `-Eww`, which dumps `CLAUDE_CODE_OAUTH_TOKEN` and
  `ANTHROPIC_API_KEY` into an artifact bound for a public issue (`process_pin_liveness_test.go:222-232`,
  `background_reach_probe_test.go` § redaction). **This ticket takes no process read at all** — the
  liveness fixture is hand-built precisely because `pinReadState` execs `ps`, and the file header names
  every exec route by file:line so the forbidden-symbol grep reports on code rather than prose. No token
  is generated, stored, rotated or logged here.
- **[3 — File operations]** SHOULD FIX, addressed in the spec. Path traversal is not reachable: both
  paths are `filepath.Join(dir, <literal>)` with `dir` from `t.TempDir()` — no caller-supplied component
  in either segment. File mode is specified as **`0o600`** in § The writer, matching
  `writeReachArtifacts:848`; the developer must not drop to `0o644` on the grounds that the content is
  "already proven clean" — the proof is what the tests assert, not what the mode may assume. No TOCTOU
  (no stat-then-open), no symlink following (`os.WriteFile` on a fresh temp dir), and no atomic-write
  requirement: these are per-test scratch artifacts, not a registry, and a partial write is visible to
  the sweep rather than persisted.
- **[4 — Subprocess execution]** No findings, and this is the category the design is built around. Zero
  `exec.Command` calls, zero `sh -c`, no environment inherited into any child because there is no child.
  Enforced by two greps in § Testing strategy, one of them anchored (`\bt\.Skipf?\(`) because the
  unanchored form matches the file's own header prose — the defect #1290's spec shipped. `resolveClaudeBin`
  is named as forbidden by file:line, since its `t.Skipf` would exit 0 and read as a pass.
- **[5 — Cryptographic primitives]** Not applicable, and the reason is structural rather than incidental:
  nothing in this design is randomised, keyed, hashed or compared against a secret. `trailNeedle` is a
  fixed 42-byte literal, not a generated token, and it is compared with `bytes.Contains` — a leak-sweep
  over a known plant, not a secret comparison, so constant-time comparison is not the right primitive
  here.
- **[6 — Network & I/O]** Not applicable — no socket, no listener, no HTTP server, no timeout surface.
  Input size is nonetheless capped: every retained operator-visible string passes `reachCapCommand`'s
  512-byte bound (`background_reach_probe_test.go:945`), and § AC2 step 3 asserts the resulting headroom
  on **what was written** rather than on what was built.
- **[7 — Error messages, logs, telemetry]** SHOULD FIX, addressed. Two concrete leak channels through
  failure messages, both closed in the spec. (a) AC2's sweep failure prints the offending file's
  *name*, not its contents — printing the content of a file that just failed a leak sweep would write
  the leak into CI logs. (b) AC1/AC3 failures print JSON *paths and key names*, never values. The
  headroom failure (step 3) names the Detail's path and its byte length, following
  `finding_run_record_test.go:1036-1041`'s shape, which reports lengths rather than the string. No
  telemetry, no metrics, no user-identifiable aggregation.
- **[8 — Concurrency]** No findings. No goroutine is spawned, so there is nothing to leak; no lock is
  taken, so there is no ordering to document; no shared state is mutated, so there is no TOCTOU. The one
  real hazard in this package — a package-level fixture var whose backing array is shared across
  parallel tests (`trail_run_outcome_test.go:608-610`) — is avoided by specifying `finWriteInputs` as a
  **function**, and each test writes into its own `t.TempDir()`.
- **[9 — Threat model alignment]** Aligned. The threat this instrument family exists against is
  *"an operator pastes a probe artifact into a public GitHub issue and publishes bytes they did not
  choose"* — model-authored `result` text, verbatim argv from the ambient process table, pyry's captured
  stderr, and (worst case) an OAuth token or API key from a process environment column. This ticket
  addresses it at the **directory** level, which is where the family's one shipped near-miss lives:
  `writeReachArtifacts` writes a clean `reach.json` beside a raw `reach.ps.txt`, and AC2's
  read-the-whole-directory sweep plus M2's third-file mutation are the direct countermeasure. Two
  residual exposures are deliberately unchanged and named: `pinStateOutcome.ToolStderr` and
  `trailAdmitResult.Reason` (the trailer's `terminal_reason`) are carried whole and are already treated
  as publishable by shipped code (`finding_attribution_fanout_test.go:719-728`) — planting a needle in
  either would go red against a correct build, and narrowing them belongs to the tickets that own those
  records, not here.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-04
