# #1452 — The live gather's trailer gate reads the runner path the run actually took

**Size:** S · **Offline throughout** — no live claude, no credentials, no `make e2e-realclaude`.
**Build tag:** everything here is behind `e2e_realclaude`, which `make check` does not compile.

---

## Files to read first

Turn-1 data load. Read these before writing anything; each line says what to extract.

| Path | What to extract |
|---|---|
| `internal/e2e/realclaude/finding_run_gather_test.go:206-270` | `finGatherInputs` header doc + the six fields. The zero-polarity argument at `:226-238` is the template the seventh field must match. |
| `internal/e2e/realclaude/finding_run_gather_test.go:537-640` | `finGatherReadings` body. The gate call is `:551-552`; its comment `:546-550` is the first stale claim. `:622-637` is the "carried WHOLE, no fill-in-if-unset" doctrine you must *distinguish* from, not follow. |
| `internal/e2e/realclaude/finding_run_gather_test.go:775-799` | `finGatherAssertContract`'s C2 block. `:785-787` is stale; `:788-789` is the whole-struct equality that must move to the row's own reading. |
| `internal/e2e/realclaude/finding_run_gather_test.go:1247-1332` | `TestFinGatherReturnsNoCapturedBytes` — the existing three-return sweep. AC4's new plant extends this shape (premise → marshal → `bytes.Contains`). |
| `internal/e2e/realclaude/finding_exit_path_probe_test.go:259-284` | `finExitRunProbe`'s gather call. `:264-272` is the needle prohibition — **untouched by this ticket**. `:313-322` is where `h.Pin.ClaudeCommand` already reaches `finRecordBuild`. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:307-333` | `trailGateResult.RunnerPath` — `json:"runner_path,omitempty"` and the "an unfilled reading reads as `""` here" paragraph. This is why the gather, not the gate, owns the normalisation. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:504-608` | The gate's absence branch: three return sites, and the two Detail markers (`the observed path owes one` / `the reading names no runner`) that discriminate the two rows sharing `gate-out-of-contract`. |
| `internal/e2e/realclaude/trailer_admissibility_test.go:969-992` | `trailRunnerUnread()` = `tdnRunnerFromArgv("")`. Its doc carries **two** stale paragraphs (`:979-981`, `:983-988`) and a wrong cite (`:980` says `:778`; the equality is at `:788-789`). |
| `internal/e2e/realclaude/trailer_terminal_reason_test.go:208-279` | `trailReasonAgainstPath` — the fall-through at `:272-278` that ignores presence, and the `:208-221` statement that no arm's Detail interpolates the reading. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:757-790` | `tdnRunnerFromArgv` — the only admissible reader. Five constant answers, echoes no argv. |
| `internal/e2e/realclaude/teardown_liveness_probe_test.go:891-906` | `tdnFixturePtyArgv` / `tdnFixtureStreamArgv` — the two argv fixtures the new rows reduce from. |
| `internal/e2e/realclaude/trailer_key_names_test.go:158-165` | `trailKeyNamesNoTerminalReason()` — the absence *line* (carries `"type":"result"`, no `terminal_reason` key). This is the new rows' stdout seed. |
| `internal/e2e/realclaude/process_pin_liveness_test.go:648-690` | `TestPinScanArgv_ExcludesTheInstrumentsOwnProcess` — the shipped self-matching-needle idiom (`os.Args[0]`, empty guard, membership-not-count). AC2's population test copies its shape and its warning. |
| `internal/e2e/realclaude/finding_run_record_test.go:256-272` | `finRecordRunnerLabel` — returns `""` for `""`, which is why an unfilled reading *routes* rather than misroutes. |
| `internal/e2e/realclaude/finding_stage_held_group_test.go:409-454` | `finStageRun` — the shipped caller that supplies nothing. It must keep working untouched. |
| `internal/e2e/realclaude/trail_run_rig_test.go:150-180` | `trailRigGather` — runs no claude (`:40`), so it has no producer. Its `:157-160` comment delegates its reason to a paragraph this ticket falsifies. |

---

## Context

`finGatherReadings` types `trailRunnerUnread()` into the gate's `RunnerPath` at
`finding_run_gather_test.go:551-552`. Under that reading `trailReasonAgainstPath`
falls through to `trailReasonPathUnnamed` **without consulting presence at all**
(`trailer_terminal_reason_test.go:272-278`), so the gate's absence branch always
takes its path-unnamed site and answers `trailGateOutOfContract`.

On the headless `PYRY_USE_STREAMJSON=1` path a healthy trailer is claude's own
`result` line and carries no `terminal_reason` key by construction. The gate has a
value for exactly that — `trailGateAbsentOwesNone` — and it is the sole entry to
the evidence route #1439 → #1440 → #1446 → #1447 → #1448 built for a path that
writes no reap log on a clean exit. Today no live run can reach it.

#1414 was right about the mechanism it named: the needle prohibition at
`finding_exit_path_probe_test.go:264-272` stands, because the gather has no
`finLivePinReduce` and claude's own row would land in the classifier's match-count
arms and in the published liveness list. **The reading does not have to come from
the gather's scan.** The caller already holds it: `finLivePinReduce` fills
`Pin.ClaudeCommand` from the driver's own two-needle scan, and the probe already
hands that value to `finRecordBuild` at `finding_exit_path_probe_test.go:322`.
This ticket routes the reading the caller already has to the gate as well.

---

## Design

### The reading crosses as a reduced string, reduced at the caller

`finGatherInputs` gains a **seventh field**:

```go
// RunnerPath is the runner-path reading, ALREADY REDUCED by the caller.
RunnerPath string
```

**The field's doc comment must name the channel it keeps shut**, in the shape
`Pinned` (`:249-251`) and `ClaudeState` (`:255-269`) already use — a plain `string`
cannot tell a reduced label from verbatim argv, so the prohibition lives in the doc
and is checked by the AC4 sweep. It must state: the only admissible producer is
`tdnRunnerFromArgv`; **never** `Pin.ClaudeCommand` itself, which is verbatim argv and
is marked INPUT ONLY — NEVER PUBLISHED (`finding_live_pin_test.go:68`); this value
crosses the gather unvalidated and is republished as `runner_path`, so raw argv here
would put an operator's `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` into an
artifact destined for a public issue — the channel `Pinned` is `[]int` to keep shut.
`""` is admissible and means "the caller staged no reading"; the gather validates
nothing else, and that is a statement about the gather rather than a licence for its
caller.

**The reduction happens at the call site, never inside the gather.** This is the
same doctrine `Pinned []int` already states at `finding_run_gather_test.go:518-522`
("a caller holding `pinScan.Matches` converts at its own call site"): a field typed
to hold only the reduced value cannot carry verbatim argv, so the credential channel
stays shut *structurally* rather than by a check. A design that took
`ClaudeCommand string` and reduced inside the gather would put verbatim argv — and
with it an operator's `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` — into
`finGatherInputs`, one struct field away from an artifact destined for a public
issue. Rejected on those grounds; `Pin.ClaudeCommand` is marked INPUT ONLY — NEVER
PUBLISHED at `finding_live_pin_test.go:68`.

The only admissible producer is `tdnRunnerFromArgv`
(`teardown_liveness_probe_test.go:772`): streamrunner-positive on `--input-format`,
ptyrunner-positive on `--session-id`, indeterminate for both-or-neither, five fixed
answers that echo no argv. **Do not reach for `reachRunnerPathFromArgv`** — it keys
on `--append-system-prompt-file`, which both argv builders emit, so it labels a
correctly-wired stream run `ptyrunner`. Write no second reader.

### The normalisation is a publication fix, not a classification repair

Contract sketch — one new symbol in `finding_run_gather_test.go`:

```go
// finGatherRunnerPath normalises an unstaged reading to the shipped "not read"
// answer, so an unfilled field publishes what it means instead of nothing.
func finGatherRunnerPath(reading string) string
```

Behaviour: returns `reading` when non-empty; returns `trailRunnerUnread()` when
`""`. Total, and information-preserving — `trailRunnerUnread()` **is**
`tdnRunnerFromArgv("")`, the shipped reader's own answer to an unread argv, so the
two spellings of "nothing was read" collapse onto the one the record can publish.

Why this is not the `ClaudeState` doctrine the same function refuses at
`finding_run_gather_test.go:622-637`. That rule ("no default, no zero-value rewrite,
no fill-in-if-unset") exists because C7 is a shipped classifier check over the claude
verdict, so a second opinion in the gather would repair the exact record C7 exists to
reject. **There is no analogous checker here.** `finRecordRunnerLabel("")` returns
`""` (`finding_run_record_test.go:267-272`), which matches neither runner label, so
`""` and `trailRunnerUnread()` reach the *same* `trailReasonPathUnnamed` answer and
the *same* gate value. The normalisation therefore changes **no decision** — it
changes only whether `runner_path` survives into the marshalled record, because
`trailGateResult.RunnerPath` is `json:"runner_path,omitempty"`
(`trailer_admissibility_test.go:333`). An unfilled reading would silently drop the
field, and a reader of the artifact cannot tell a dropped field from a field that was
never there.

**Why the gather and not the gate.** The gate's own doc argues at
`trailer_admissibility_test.go:320-332` that `""` is safe there *because it routes*,
and its nine `trailGateCases()` rows plus
`TestTrailGateReadsTheRunnerPathOnlyWhereARowDeclaresIt` are keyed on the gate
copying its input through unchanged. Normalising there would make the gate lie about
what it was handed. The distinction between "unfilled" and "read and indeterminate"
is the composer's to make.

**One symbol, two callers.** `finGatherReadings` and C2's recomputation both go
through `finGatherRunnerPath` and neither carries its own copy of the rule — the
`finBoundKeyNames` doctrine this file already states at `:575-577`: two hand-written
copies of a fill required to agree is how two fills stop agreeing.

### Data flow

```
finExitRunProbe (live)                     finStageRun / trailRigGather (fixtures)
  h.Pin.ClaudeCommand  (verbatim argv,          (no claude staged — no producer)
   INPUT ONLY, never published)                          │
        │ tdnRunnerFromArgv  ← reduction AT THE          │ RunnerPath omitted
        ▼   CALL SITE                                    ▼
  finGatherInputs.RunnerPath  ────────────────────►  "" (zero)
        │                                                │
        └──────────────► finGatherRunnerPath ◄───────────┘
                                │  "" → trailRunnerUnread()
                                ▼
              trailGate(trailGateInput{Scan: …, RunnerPath: …})
                                │
                                ▼
              trailGateResult.RunnerPath  (published as runner_path)
```

### What does not change

- **The gather's scan.** `pinScanArgv(in.Needles, nil)` still runs over `in.Needles`
  **verbatim**. `RunnerPath` is a reduced string and never a needle; the gather adds
  nothing to the needle set. `finding_exit_path_probe_test.go:264-272`'s prohibition
  is untouched and its `Needles: []string{h.FIFOPath}` is unchanged.
- **`finStageRun`** (`finding_stage_held_group_test.go:423`) supplies nothing and
  stays as written. Its `trailFixtureTrailer` seed carries a named `terminal_reason`,
  so under `trailRunnerUnread()` it still reaches `trailGateUsable` certifying
  `finStageTerminalReason` and its `:439` guard is unaffected.
- **`trailRigGather`** (`trail_run_rig_test.go:162`) stays a literal
  `trailRunnerUnread()`. It runs no claude process at all (`trail_run_rig_test.go:40`),
  so it has no producer for any reading; a parameter would be an argument none of its
  five callers can fill. Its **comment** must change (see the sweep) — today it states
  the rig's own fill and then delegates its full reason to
  `trail_ptyrunner_composition_test.go:19-26`, so it goes stale *by delegation* even
  though the code does not move.
- **`trailGateCases()`** does not grow. Its nine rows and the "row nine" ordinal are
  load-bearing at `trailer_admissibility_test.go:530`, `:979`, `:1069`, `:1217`,
  `:2052`, `:2063-2107`, `:2210`, `:2536`, `:2663` and
  `trail_ptyrunner_composition_test.go:50`.
- **`finGatherCases()`** does not grow either — past three rows its own cardinality
  and ordinal claims move (`finding_run_gather_test.go:666`, `:751`, `:762-765`,
  `:1000`, `:1770`). The new rows go in a **new table** whose cardinality is fresh and
  has no claims to sweep.
- **`finRecordBuild`'s** own `tdnRunnerFromArgv(h.Pin.ClaudeCommand)` at
  `finding_exit_path_probe_test.go:322` stays. There will be two reductions of the
  same input in one function — the builder owns the record's `RunnerFromArgv`, the
  gather's is the gate's input. Both are pure over the same string, so they agree by
  construction; do **not** hoist one into the other, because `finRecordInputs` takes
  the argv and the gather takes the label, and changing either signature to share a
  value is scope this ticket does not have.

### Concurrency model

None introduced. `trailGate`, `trailReasonAgainstPath`, `finGatherRunnerPath` and
`tdnRunnerFromArgv` are all pure — no exec, no clock, no filesystem. The gather's
existing concurrency surface (`probeSyncBuffer`'s mutex-guarded `Bytes()`, the
`pinScanArgv` exec) is unchanged. New tables are **functions, not package-level
vars**, per `trailRunWellFormed`'s stated reason (`trail_run_outcome_test.go:1112-1113`):
rows carry slices and `go test -race` runs this package in parallel.

### Error handling

No new failure modes. The one new branch is `finGatherRunnerPath`'s empty check, and
both of its arms are named readings rather than errors. `tdnRunnerFromArgv` cannot
fail — every branch returns a non-empty constant, including for `""`.

---

## Testing strategy

All offline. Everything below lives in `finding_run_gather_test.go`.

### 1. `finGatherRunnerPathCases()` + `TestFinGatherGateReadsTheRunnerPathTheRunTook` (AC3)

New table, four rows. Row fields: `name`, `seed` (the stdout line), `reading` (what
the caller supplies), `wantValue`, `wantReason`, `marker` (a Detail substring).

Every row asserts the whole `trailGateResult` against **the shipped gate's own output
over the row's own reading**:

> `want := trailGate(trailGateInput{Scan: trailScan(seed), RunnerPath: finGatherRunnerPath(tc.reading)})`

and never against a typed-in `trailGateResult`. Then pins `wantValue`/`wantReason`
against the row, and — for the two rows that share a value — the `marker`.

| Row | `reading` | `seed` | Reaches | Discriminated by |
|---|---|---|---|---|
| R1 | `tdnRunnerFromArgv(tdnFixtureStreamArgv)` | `trailKeyNamesNoTerminalReason()` | `trailGateAbsentOwesNone` | the value is unique — **this is the value the ticket exists to make reachable** |
| R2 | `tdnRunnerFromArgv(tdnFixturePtyArgv)` | `trailKeyNamesNoTerminalReason()` | `trailGateOutOfContract` at the **owes-one** site (`trailer_admissibility_test.go:572-580`) | marker `the observed path owes one` |
| R3 | `""` (caller supplies nothing) | `trailKeyNamesNoTerminalReason()` | `trailGateOutOfContract` at the **path-unnamed** site (`:591-597`) | marker `the reading names no runner` — today's behaviour for every live run |
| R4 | `tdnRunnerFromArgv(tdnFixturePtyArgv)` | `trailFixtureTrailer` | `trailGateUsable` certifying `"completed"` | `Reason == "completed"` — **this row is why #1337's recorded ptyrunner finding and the shipped ptyrunner probe keep their classification**, and it names that as its reason |

Non-vacuity, to be stated in the test's doc:

- **R2 and R3 share `gate-out-of-contract`**, so a check on the value alone
  discriminates nothing. The marker is the discriminator, and the two markers are
  distinct in shipped prose.
- **R3 is the sole red for a missing normalisation.** A gather that passed
  `in.RunnerPath` through raw would produce `RunnerPath: ""` while the recomputation
  produces `trailRunnerUnread()`; `trailGateResult` is four strings compared with
  `==`, so R3 goes red and R1/R2/R4 stay green.
- **R1/R2/R4 are the sole reds for a gather that ignored `in.RunnerPath`** and kept
  typing `trailRunnerUnread()` in: R1 would fall to `gate-out-of-contract`, R2 to the
  path-unnamed site, R4 stays `trailGateUsable` but its `RunnerPath` string differs,
  so the whole-struct equality catches it.
- Verify each of the four claims above with `go test -overlay` against a mutated
  tree rather than asserting them in prose (`docs/knowledge/codebase/` records this
  package's overlay idiom; run from the worktree in one call).

### 2. `TestFinGatherRunnerPathDoesNotReachTheScan` (AC2)

Run the gather over **byte-identical** inputs — same seed, same stderr, same pinned
set, same needles — varying **only** `RunnerPath`, and assert the argv leg's
population does not move. The design below is deliberately *not* the obvious one; the
obvious one is flaky and weaker. Both facts belong in the test's doc.

**The needle is `finGatherNeedles(t)`** — a `t.TempDir()` path under which nothing is
ever staged, so `MatchCount == 0` and `Liveness` is empty **deterministically**.

**The fourth row is the instrument.** Three rows carry the ordinary readings
(streamrunner / ptyrunner / `""`); the fourth carries a deliberately abusive one,
`RunnerPath: os.Args[0]` — this binary's own path, which **is** a matching needle.
A gather that routed the reading into its needle set would take that row's
`MatchCount` from 0 to ≥1 and put this process into the published liveness list.
Under the correct gather it stays 0. `os.Args[0]` needs the empty guard from
`process_pin_liveness_test.go:662-665`.

**The control is what stops the negative being vacuous.** `MatchCount == 0` on the
abusive row proves nothing unless `os.Args[0]` is demonstrably a needle that matches.
So the test runs `pinScanArgv([]string{os.Args[0]}, nil)` **itself** and asserts
`os.Getpid()` is among its matches and `RowsScanned > 0`. Only then does the abusive
row's zero mean "the gather did not route the reading into the scan" rather than
"nothing matches anything here".

Per-row assertions: `ArgvScanErrored == false`, `RowsScanned > 0`, `MatchCount == 0`,
`len(Liveness) == 0`.

**Do NOT use `os.Args[0]` as the gather's needle and compare `MatchCount` across
rows.** That is the shape this design rejects, and the reason is measured: this
package makes 29 `t.Parallel()` calls and re-execs itself at
`fixtures_test.go:119`, `:228`, `:478` and `teardown_reap_capture_test.go:219`, `:254`.
Those children carry `os.Args[0]` as their own argv[0], and `reachMatchArgvRows`
matches any needle as a substring of the **full** command line
(`background_reach_probe_test.go:884-895`). A sibling test's child appearing or
exiting between two of the four gather calls moves the count, so a cross-row equality
would be testing the harness — the same warning `process_pin_liveness_test.go:658-660`
records, and the reason that test asserts membership rather than a count.

**State what this test does not kill.** It goes red against any design that lets the
reading reach `in.Needles`. It does **not** kill "the gather appends
`tdnClaudeNeedle`", because offline no claude process exists for that needle to
match. That clause of AC2 is discharged **structurally** — the field is a reduced
string, and the gather still calls `pinScanArgv(in.Needles, nil)` with `in.Needles`
verbatim — and the doc must say so rather than let a later reader over-trust a green.

**No exec is added.** `finding_run_gather_test.go` reaches `ps` only through
`pinScanArgv` and `pinReadState`, and the file forbids exec by its own header (the
obligation is stated in the gather at `:572-575`). Concretely: do not add an
`os/exec` import to this file, and do not call `exec.Command`, `exec.CommandContext`,
`exec.LookPath`, `os.StartProcess` or `syscall.Exec` from it. `os.Args[0]` here is
read as a **string used as a content needle**, never as a process to start — the
package does re-exec itself elsewhere (the five sites above), and this spec's use of
`os.Args[0]` must not be read as licence to.

### 3. Captured-bytes sweep over the new route (AC4)

`runner_path` **is** marshalled, and `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`
has only ever swept it under `trailRunnerUnread()` — the empty-argv answer — so no
shipped sweep has run it under a reading derived from a real argv. Add a plant that
does, in `finding_run_gather_test.go` (the gather is the new route's owner).

Three rungs, in order — the middle one is what stops the sweep being vacuous:

1. **The plant.** Reduce an argv carrying the needle:
   `tdnRunnerFromArgv(tdnFixtureStreamArgv + " " + trailNeedle)`, and hand the result
   to the gather as `RunnerPath`.
2. **The route is real.** Assert the marshalled `readings` contains the literal
   `runner_path` and that `readings.Gate.RunnerPath` equals the reading handed in. A
   sweep over a field that got dropped by `omitempty` measures nothing.
3. **The needle would have travelled.** Assert `strings.Contains(argvWithNeedle,
   trailNeedle)` — so had the reduction echoed its input, the needle would be in
   `RunnerPath`, and by rung 2 `RunnerPath` is in the bytes. Only now is rung 4's
   negative meaningful.
4. **The negative.** `trailNeedle` is absent from the marshalled `readings`,
   `record` and `sighting`, and absent from `readings.Gate.Detail` and
   `readings.Admit.Detail`.
5. **A second fabric, against argv generally rather than the planted needle.** Also
   assert `/opt/node/bin/node` — the leading substring **both** argv fixtures carry
   (`teardown_liveness_probe_test.go:897`, `:902`) and which appears in none of
   `tdnRunnerFromArgv`'s five constant answers — is absent from the marshalled
   `readings`. The needle rung catches a reduction that echoed the *tail* it was
   handed; this one catches one that echoed the *head*, and it fires even against a
   future caller who passes raw argv carrying no needle at all.

Reuse `TestFinGatherReturnsNoCapturedBytes`' marshal-and-`bytes.Contains` shape and
its `finGatherForbiddenKeyPaths` key walk; do not build a second artifact-writing
surface.

### 3b. Detail-budget invariance (a premise the new rows make cheap to check)

`trailDetail` caps at `reachMaxCommandBytes` (512) and `reachCapCommand` **truncates
and marks** rather than failing (`background_reach_probe_test.go:945-950`), so an
overlong Detail ships a sentence severed past its own marker while still satisfying a
marker assertion. #1417 measured the three absence Details at 461 / 464 / 468 B and
the presence Detail at 427 B — all under `trailRunnerUnread()`.

Those measurements stay valid **because no Detail interpolates the reading**: verified
at `trailer_admissibility_test.go:307-311` (the gate copies `RunnerPath` to the field
and nowhere into prose) and `trailer_terminal_reason_test.go:208-221` (every arm's
Detail is fixed prose over its own file's constants). R1/R2/R3 drive the three absence
Details under three *different* readings, so the check is one line per row and it is
the deterministic net for that claim: assert each row's `readings.Gate.Detail` is
shorter than `reachMaxCommandBytes` and carries no truncation marker. If a future
edit interpolates the reading into a Detail, the budget becomes a function of the
reading and these rows go red rather than shipping a severed sentence.

### 4. Verification commands

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/
go test -tags e2e_realclaude -run '^TestFinGather' ./internal/e2e/realclaude/
go test -tags e2e_realclaude -run '^TestTrail' ./internal/e2e/realclaude/
go test -tags e2e_realclaude -run '^TestFinStage' ./internal/e2e/realclaude/
```

Baseline vet is clean at `45cd2a8`. No live claude and no credentials for any check.
`make check` does **not** compile these files — a green `make check` is not evidence
here (a `done:qa` green over a build-tagged diff has shipped uncompiled before).

---

## The prose sweep (AC5) — verified populations

I re-ran both sweeps against `main` at `45cd2a8`; the ticket's lists are complete and
the two sites it flags as easily-missed are real. Sweep by **sense**, not by phrasing —
`-i` plus `[ -]?` on numeral and ordinal residuals, because a `\bsix\b` grep misses
"sixth" and "six-field".

### Population A — the runner-path claim (11 sites, 5 files)

Each says either *both gathers supply the not-read reading* or *a live run always
reaches the path-unnamed case*. Both halves are false for the finding gather after
this change; both stay true for `trailRigGather`.

| Site | What is now false |
|---|---|
| `trail_ptyrunner_composition_test.go:19-26` | "both shipped gathers fill … with `trailRunnerUnread()`". This is the paragraph three other sites delegate to — fix it first. |
| `trailer_admissibility_test.go:143-147` | `trailGateAbsentOwesNone` "is unreachable" over a live run. It is now the value this ticket makes reachable. |
| `trailer_admissibility_test.go:176-180` | same claim for `trailGatePresentOwesNone` — also now reachable, on a stream run whose trailer carries a reason. |
| `trailer_admissibility_test.go:519-522` | "an indeterminate reading is precisely what both shipped gathers supply". |
| `trailer_admissibility_test.go:979-981` | "Both gathers … go through this one function" — **and its cite is wrong**: it says `finding_run_gather_test.go:778` for C2's whole-struct equality; `:778` is blank and the equality is at `:788-789`. The block is being rewritten; correct it rather than carry it forward. |
| `trailer_admissibility_test.go:983-988` | **the second paragraph of the same doc**, below `:979-981`, and the one that actually writes "a live run reads no runner and the gate's absence arm reaches only its path-unnamed case". A sweep that stops at the first paragraph misses it. |
| `trailer_admissibility_test.go:1673-1678` | R3's row comment: "the only one of the three a live run reaches today". |
| `trail_run_outcome_test.go:778-780` | "both fill the gate's runner-path field with `trailRunnerUnread()` by construction". The *surrounding* claim — that no shipped gather stages `Ordering` or `PinnedPid` — **stays true**; change only the runner-path premise, and re-derive the no-C10 conclusion from `trailRigGather` alone. |
| `finding_run_gather_test.go:546-550` | the gate call's own comment. |
| `finding_run_gather_test.go:785-787` | C2's "both sides go through `trailRunnerUnread()`". |
| `trail_run_rig_test.go:157-160` | states the rig's own fill and **delegates its full reason** to `trail_ptyrunner_composition_test.go:19-26`. Stale by delegation even though the rig's code does not move — give it its own structural reason (`:40`: this rig runs no claude). |

**Verified as staying true — do not touch:** `trail_ptyrunner_composition_test.go:50`
and `trailer_admissibility_test.go:1817`, `:2536`, `:2663` all speak about
`trailGateCases()` rows or the sweep's own rows, and neither table grows.
`trail_run_outcome_test.go:1183` and `trail_ordering_premises_test.go:191` are the
`Ordering`/`PinnedPid` staging claim, which this ticket does not touch.

### Population B — the derived set whose cardinality moves

`finGatherInputs` goes from six fields to seven. Three shipped comments count them:

- `finding_live_run_test.go:111` — "finGatherInputs' six fields need neither"
- `finding_run_gather_test.go:206-208` — "#1281's four values, plus the two #1282 left staged"
- `finding_run_gather_test.go:217` — "Across six positional arguments"

The new field also owes the zero-polarity argument its two predecessors make at
`finding_run_gather_test.go:226-238` — that is where the "a caller that supplies
nothing still publishes today's record" criterion is discharged in prose. Add the
third bullet there: `RunnerPath`'s zero is `""`, which `finGatherRunnerPath` maps to
the shipped "not read" answer, so an incompletely-filled `finGatherInputs` degrades to
a named nothing-was-read rather than to a claim.

**Sweep the derived set too, not just the headline set.** #1448's review FAILed for
exactly this: a commit that swept one set perfectly missed a second set the same
commit grew. Before committing, re-grep for count words (`six`, `seven`, `6`, `7`,
`fourth`, `seventh`, plus hyphenated forms) anywhere near `finGatherInputs`.

### The cite pass — the known failure mode on this route

`finding_run_gather_test.go` is cited **26 times from 12 other files** and carries
**35 in-file bare `(:NNN)` refs**. Adding a field at `:239-270` plus a helper, a
table and three tests shifts everything below by several different deltas. The last
three tickets on this route (#1446, #1447, #1448) each FAILed code review on stale
cites and nothing else. Treat the cite pass as a deliverable, not as cleanup:

- Do it **last**, after the file's final shape is fixed. Any later edit invalidates it.
- The deltas are **not uniform** — there are multiple insertion points, so a cite's
  shift depends on which ones it sits below. Do not `replace_all` a single offset.
- Cites come in **three forms**, and a filename grep finds only the first:
  `finding_run_gather_test.go:NNN`, bare `(:NNN)` inheriting the last-named file, and
  symbol-anchored `<TypeName>:NNN`. Sweep for all three.
- Verify each re-pointed cite by resolving it against the post-edit tree and checking
  the line says what the prose claims — pair on the **normalised prose**, not on the
  line number.

---

## Open questions

1. **Where the AC4 plant lives.** Specified above as a new test in
   `finding_run_gather_test.go`. If the developer finds it reads better as a fourth
   sub-test of `TestTrailAdmissibilityRecordsCarryNoCapturedBytes`, that is
   acceptable — but the plant must go through the **gather**, not through `trailGate`
   directly, since the gather is the route this ticket opens.
2. **`trailRigGather`'s shape** is settled as unchanged above. If a later ticket gives
   that rig a claude process, its literal becomes a parameter then — not now.
3. **Killing the `tdnClaudeNeedle` mutant offline** has no answer in this spec: with
   no claude process running, an extra needle matches nothing, so no offline test can
   redden it. Discharged structurally here (§ Testing 2). If a later ticket wants a
   live red for it, the honest route is a staged process carrying the needle, which is
   a rig this package does not have — file it rather than fake it.

---

## Security review

**Verdict:** PASS (second pass — the first pass found one MUST FIX, fixed inline in
§ Testing 2 before this section was written)

**Findings:**

- **[Trust boundaries] SHOULD FIX — mitigated in-spec.** The design's one boundary is
  `tdnRunnerFromArgv` (`teardown_liveness_probe_test.go:772-790`), placed at the
  *caller's* call site so `finGatherInputs` never holds verbatim argv. I re-read all
  five of its branches: every one returns a source-authored string literal; none
  concatenates or formats `claudeCommand`. The residual is that `RunnerPath` is a
  plain `string` and so cannot, by type, distinguish a reduced label from raw argv —
  a future caller could pass `Pin.ClaudeCommand` directly and it would compile. The
  correct structural fix (a named reading type) would change `tdnRunnerFromArgv`'s
  return type across 20+ consumers, an edit cascade well past this ticket's size. The
  mitigation is three-layered and the layers are different fabric: the field doc names
  the channel in the shape `Pinned` (`:249-251`) and `ClaudeState` (`:255-269`)
  already use; AC4's needle rung catches a reduction that echoes its input's *tail*;
  and the new `/opt/node/bin/node` rung catches one that echoes its *head*, firing
  even against raw argv carrying no needle. Not gated on — no such caller exists, and
  the field does not yet.

- **[Tokens, secrets, credentials] No new findings.** Nothing is generated, stored,
  rotated or revoked. The only secret in reach is a `CLAUDE_CODE_OAUTH_TOKEN` or
  `ANTHROPIC_API_KEY` appearing in claude's argv, and the exposure question is
  entirely the one above. Recorded so it is not mistaken for a control:
  `reachCapCommand`'s 512-byte cap would *truncate* a leaked argv, not prevent the
  leak, and this spec claims no mitigation from it.

- **[File operations] N/A — no file surface added.** The reading never reaches a path.
  Artifacts are still written by the shipped `finWriteArtifacts`; the only new path is
  `finGatherNeedles`' existing `t.TempDir()`, used as a content needle and never
  opened.

- **[Subprocess execution] No findings, and one ambiguity closed.** No exec is added;
  § Testing 2 names the forbidden symbols individually (`exec.Command`,
  `exec.CommandContext`, `exec.LookPath`, `os.StartProcess`, `syscall.Exec`, and the
  `os/exec` import) rather than saying "no exec" — a bare `exec.` grep reads clean in
  this package because every legitimate route execs inside a helper. Positively:
  needles are **never** passed to `ps`. `reachScanArgv` builds a fixed
  `ps -axww -o pid=,ppid=,pgid=,command=` argv and matches needles in-process against
  its output (`background_reach_probe_test.go:876-895`), so an attacker-shaped reading
  has no argv- or shell-injection surface. The spec's use of `os.Args[0]` is stated as
  needle-only with the package's five real re-exec sites named, so it cannot be
  misread as licence to re-exec.

- **[Cryptographic primitives] N/A.** No randomness, no hashing, no key material. The
  one new comparison is `reading == ""` against the empty string, not against a
  secret, so constant-time comparison does not apply.

- **[Network & I/O] N/A — no sockets, no HTTP, no timeouts to set.** On input size:
  the new field carries one of five constants (≤ ~90 B) or `""`, so no unbounded
  string reaches the record by this route. The 512-byte Detail budgets #1417 and #1433
  measured (461 / 464 / 468 / 427 B) stay valid, and this is verified rather than
  assumed — no Detail interpolates the reading
  (`trailer_admissibility_test.go:307-311`, `trailer_terminal_reason_test.go:208-221`),
  so the budget is invariant under it. § Testing 3b turns that from a prose claim into
  a per-row assertion on the output, which matters because `reachCapCommand` truncates
  and marks rather than failing.

- **[Error messages, logs, telemetry] No findings.** `runner_path` is genuinely
  published — it marshals on `trailGateResult`, which is embedded in
  `trailRunReadings.Gate`, and `finExitRunProbe` publishes readings via `t.Logf`
  (the artifact record itself carries only the trailer sub-record's outcome string,
  `finding_exit_path_probe_test.go:329-331`). Transcript and marshalled record carry
  the same obligation, and AC4 sweeps the value both reach. No error strings are
  added; `finGatherRunnerPath` has no failure mode.

- **[Concurrency] MUST FIX — found and fixed in the first pass.** The spec originally
  prescribed `os.Args[0]` as the *gather's* needle with `MatchCount` compared across
  rows. That is unsound in this package: it makes 29 `t.Parallel()` calls and re-execs
  itself at `fixtures_test.go:119`, `:228`, `:478` and
  `teardown_reap_capture_test.go:219`, `:254`; those children carry `os.Args[0]` as
  their own argv[0], and `reachMatchArgvRows` matches needles as a substring of the
  full command line. A sibling's child appearing or exiting between two gather calls
  moves the count, so the test would have been flaky *and* weaker. § Testing 2 now
  uses a `t.TempDir()` needle (population 0 regardless of siblings) and makes
  `os.Args[0]` the *abusive reading* of a fourth row, with a self-run
  `pinScanArgv([]string{os.Args[0]}, nil)` as the control proving that reading is a
  needle that would have matched. Deterministic, non-vacuous, and it kills the mutant
  the flaky version could not. Nothing else concurrent is introduced: no goroutines,
  no locks, no shared state; new tables are functions per
  `trail_run_outcome_test.go:1112-1113`.

- **[Threat model alignment] Aligned.** The governing threat here is the family's own
  — a value published into an artifact destined for a public issue must carry no
  captured bytes (`finding_run_gather_test.go:518-522`, `:260-268`;
  `finding_live_pin_test.go:68`; `finding_attribution_fanout_test.go:195-202`). The
  design keeps the reduction at the call site, which is where that family already puts
  the `pinScan.Matches` → `[]int` conversion for the same reason. No relay or network
  surface is touched, so `docs/protocol-mobile.md` § Security model does not apply to
  this ticket.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-10
