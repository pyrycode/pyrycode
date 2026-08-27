# #1722 — name the arm on the `initialize` capture record and mint its path through the arm-carrying namer

**Ticket:** https://github.com/pyrycode/pyrycode/issues/1722
**Size:** s · **Labels:** `enhancement`, `security-sensitive`
**Scope:** test-only, offline. No production file changes. No live claude.

---

## Files to read first

Everything below is in `internal/e2e/realclaude/` unless noted. All four files are behind the
`e2e_realclaude` build tag.

| File | Symbols | What to extract |
|---|---|---|
| `initialize_control_record_test.go` | `initControlFixtureRecord`, `initControlFullRecord`, `initControlFixtureFields`, `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken` | The 22-field contract, the fully-populated fixture and why it is deliberately incoherent, the hand-written listing and why it must never be reflection-generated, and the existing "does not survive slugging" subtest you are widening. |
| `initialize_control_writer_test.go` | `writeInitControlFixture`, `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`, `compactInitControlRawRows`, `TestInitControlFixture_WriterCapsStderrCapture` | The writer whose path minting you migrate; the round trip whose name assertion you re-point; the cap test that also writes through the writer (its per-row tempdir comment mentions the minted name). |
| `initialize_control_names_test.go` | `initControlArms`, `initControlArmFixtureName`, `initControlFixtureName`, `TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained` | The arm table you grow into rows, the two namers, and every `#1713` reference you re-point. The declaration's doc comment names itself as the growth point and forbids a second table. |
| `initialize_control_probe_test.go` | `runInitControlChild`, `initControlControlBudget`, `TestRealClaude_InitializeControl_Capture`, `TestInitControlSummarize_ReadsAllThreePlacements` | The one existing caller: where `setModeWaitFor`'s result is discarded into a log, where the record literal is built, and the precedent for an offline test living in an exec-ing file. |
| `set_permission_mode_probe_test.go` | `setModeFixtureRecord`, `setModeWaitFor`, `setModeArms`, `setModeFixtureName` | `setModeFixtureRecord.Arm` is the field whose tag and position you copy. `setModeWaitFor` returns `bool` — that return is the measurement AC 2 wants. `setModeArms` is the row-table shape to imitate; `setModeFixtureName` is the namer that interpolates its arm RAW (the anti-pattern). |
| `inband_bypass_revoke_arms_test.go` | `poolRevokeArms` | The second row-table precedent — a `[]poolRevokeArm` of `{name, flags…}` rows. |
| `permission_protocol_spike_test.go` | `versionSlug` | Lowercase → fold `[^a-z0-9._-]+` to `_` → clamp at 32. This is why the fixture's arm literal needs an uppercase run. |
| `offline_exec_ban_test.go` | `finOfflineExecBans`, `TestFinOfflineFilesReachNoExecHelper` | Read the three `initialize_control_*` entries. **You add no entry and change none** — but know that the record and names files are banned from all `os` read/write, which is why the new I/O-bearing test goes in the writer file. |
| `internal/e2e/internal/fakeclaude/initialize_control_test.go` | `initControlCaptureGlob`, `captureModelKeySets` | UNTAGGED, so `make check` runs it. It globs `initialize_control_v*.json` and walks `control_responses[].response.response.models[]` through generic maps. Confirm for yourself that new record fields are invisible to it — then leave it alone. |
| `docs/knowledge/features/e2e-realclaude.md` § `initialize_control_names_test.go` and § `initialize_control_record_test.go` | — | The 18-of-22 shared-field claim and #1712's three recorded lessons. **Read-only** — the documentation phase owns this file. |

---

## Context

`initControlFixtureRecord` (#1701) was shaped for one arm at one send point, and
`writeInitControlFixture` (#1702) mints its path from `initControlFixtureName(out.ClaudeVersion)`
— one input, no arm. #1712 landed the other half and deliberately stopped short of consuming it:
`initControlArmFixtureName(versionToken, arm)` puts both columns through `versionSlug`, and the
read-only `initControlArms` names the three send points. Nothing calls it.

This slice closes two gaps at once.

**A fixture on disk cannot say which measurement produced it.** Once #1715 drives three arms, three
captures land in one directory and only the filename distinguishes them — and today the filename
does not either, because all three mint the same path through the one-input namer. Migrating the
writer without also recording the arm inside the record would leave the fixture's identity resting
entirely on its filename, which is the thing a reviewer renames.

**"There are response bytes" and "the wait was satisfied" are two different facts, and only the
first survives the run.** `runInitControlChild` calls
`setModeWaitFor(rec.controlResponseCount, 1, initControlControlBudget)` and drops the return into a
`t.Logf`, then snapshots `ControlResponses` after `cmd.Wait()`. A response that arrives after the
budget expired but before the child exits still lands in the verbatim field, underneath a log line
that already claimed absence. The record has no field that can say so, and the verbatim bytes
cannot reconstruct it — a `control_response` carries no arrival timestamp relative to a budget the
harness chose.

No ADR. This is one more slice of the `initialize_control_*` family whose decisions already live in
`docs/specs/architecture/1696-*.md`, `1701-*.md`, `1702-*.md` and `1712-*.md`, and whose evergreen
prose lives in `docs/knowledge/features/e2e-realclaude.md`.

### The two collisions the ticket names, and how this design resolves them

1. **AC 3 vs AC 1 — the fixture's arm literal cannot be a declared arm.** All three identifiers in
   `initControlArms` are slug-clean, so `versionSlug` returns them unchanged and the round trip's
   name assertion goes 0-red on the arm column against both a raw-interpolating writer and a
   hardcoded-arm writer. Resolved in **S3**: `initControlFullRecord`'s arm literal carries an
   uppercase `-FIXTURE` run, exactly as its `claude_version` does, and a widened subtest pins it.
   AC 1 binds what a *live capture* records; the fully-populated fixture is a synthetic record, not
   a capture.
2. **AC 5 vs AC 2 — the non-zero property forbids the incoherent pair in the fully-populated
   fixture.** A bool is non-zero only when `true`, so `initControlFullRecord` carries
   within-the-wait `true` beside captured bytes: the coherent combination. AC 2's discriminating
   case gets its own record instance in **S5**, not a change to the fixture.

---

## Design

Seven changes across four test files. Nothing under `cmd/` or `internal/` outside
`internal/e2e/realclaude/`, and nothing outside `*_test.go`.

### S1 — `initControlArms` grows into a table of rows with a probed marker

**Why a row table and not a constant.** AC 4 needs the probe's arm decidable offline, without
spawning a child. The ticket forbids the two obvious shapes: `initControlArms[1]` depends on
positional order, and a standalone `const initControlProbeArm = "after_completed_turn"` is a second
spelling of an identifier the table already declares. Growing the declaration is the third shape,
and it is the one #1712's own doc comment names: *"if this slice needs per-arm data beyond the
identifier, the fields go onto that declaration and it becomes a table of rows, exactly as
`setModeArms` and `poolRevokeArms` are."* This slice needs exactly one datum beyond the identifier —
which arm the single-arm run drives — so the row struct arrives now, carrying it. #1712's spec left
this as an open question and handed the call here.

In `initialize_control_names_test.go`, replacing the `var initControlArms = []string{…}`
declaration in place:

```go
type initControlArm struct {
	id     string
	probed bool // exactly one row, until #1715 makes the rig three-armed
}

var initControlArms = []initControlArm{
	{id: "before_first_turn"},
	{id: "after_completed_turn", probed: true},
	{id: "control_no_request"},
}

// initControlProbedArm returns the id of the ONE row marked probed, and "" when
// the count is not exactly one.
func initControlProbedArm() string
```

Contract points the doc comments must carry:

- The identifiers, their order and their meaning are **unchanged** — this is a shape change, not a
  vocabulary change. `before_first_turn` / `after_completed_turn` / `control_no_request` each still
  name the arm's send point.
- **READ-ONLY** stays: never append, never reassign, ranged from `t.Parallel()` tests. Carry
  #1712's wording forward; a mutation would race.
- `probed` marks the arm the single-arm run in `initialize_control_probe_test.go` sends at. It is
  the growth point's first extra column and it **goes away with the single-arm run** when #1715
  ranges the table for real.
- **Returning `""` for a zero-marked and for a multi-marked table is deliberate**, not laziness.
  Collapsing both degenerate shapes to the empty string makes S6's non-emptiness assertion the sole
  red for both, instead of needing a separate "exactly one row is marked" count.
- The lock test's `hostileArms` are still that test's own literals and must **not** be appended to
  this declaration — carry #1712's warning through the shape change unchanged.
- The sentence *"A `[]string` and not a one-field struct: this slice needs identifiers and nothing
  else today"* is now false and is what this change acts on. Replace it; do not leave it standing.

**Rangers to update, both in `TestInitControlArmFixtureName_AvoidsCommittedNamesStaysDistinctAndContained`:**
the `arms` slice built with `append(arms, initControlArms...)`, and the distinctness subtest's
`for _, arm := range initControlArms`. Both now read `.id`. Do not otherwise touch that test —
it is green and this ticket does not own its properties.

### S2 — two fields on the record

In `initialize_control_record_test.go`, on `initControlFixtureRecord`:

| Field | Tag | Position | Notes |
|---|---|---|---|
| `Arm string` | `arm` | immediately after `ClaudeVersion` | **Copied from `setModeFixtureRecord.Arm` — same tag, same Go type, same position relative to the version pair.** That makes it the record's **nineteenth** shared field, and the record's doc claim about shared fields must move from "Eighteen of its twenty-two" to "Nineteen of its twenty-four". It also puts the two inputs `initControlArmFixtureName` is minted from adjacent in the declaration. |
| `ControlResponseWithinWait bool` | `control_response_within_wait` | immediately after `ControlResponseRequestIDMatched` | Sits with the response fields, not in the instrument-health block, for the same reason `ControlResponseRequestIDMatched` does: a response that arrived late is a finding about claude's latency, not a fault in the harness. |

Doc-comment obligations on the record type:

- `ControlResponseWithinWait` is **measured, not derived, and NOT derivable.** State the contrast
  with the two neighbouring paragraphs explicitly: `ControlResponseRequestIDMatched` is a function
  of two fields recorded verbatim beside it, and `ModelsPresent`/`ModelsCount`/`ModelsEntryFields`
  are populated from a response a live run holds. This field is a function of a **wait that has
  already expired** — no other field, and not the verbatim bytes, can reconstruct it, because a
  `control_response` carries no arrival time relative to a budget the harness chose. A reader who
  "tidies" it into `len(ControlResponses) > 0` deletes the measurement; S5 is what reddens.
- `Arm` names the send point the run **INTENDED**. Recording an arm is not a claim that the turn
  completed — `turn_boundaries` is what tells a reader that, and `runInitControlChild` already logs
  the case where the probe turn produced no `result` inside its budget. Say so; a later reader will
  otherwise read `after_completed_turn` as an assertion about the turn.
- `Arm` for a live capture is one of `initControlArms`' identifiers; for `initControlFullRecord` it
  is deliberately not (S3).
- The record's total is now **twenty-four**. This slice adds no raw-JSON-bearing field, so
  `compactInitControlRawRows`' "exactly the three raw-JSON rows" claim is untouched (a `string` and
  a `bool` are neither of the two types it switches on).

### S3 — the fully-populated fixture and its listing

In `initControlFullRecord`:

```go
Arm:                       "after_completed_turn-FIXTURE",
ControlResponseWithinWait: true,
```

- `after_completed_turn-FIXTURE` is chosen the way `2.1.220-FIXTURE` was, and its doc entry must
  say so as **the fourth load-bearing literal choice**: `-` is inside `versionSlug`'s
  `[a-z0-9._-]` class, so a clean `after_completed_turn` would survive slugging byte-identically
  and AC 3's name assertion would go 0-red on the arm column. The uppercase run is what makes the
  slug differ; the word FIXTURE is what stops a later reader "correcting" the literal into a
  declared arm. It is 28 bytes, comfortably inside the 32-character clamp.
- It is distinct from all seven existing record strings, and it is **not** any declared arm.
- `ControlResponseWithinWait: true` because non-zero for a bool means `true`. That is the coherent
  pair — captured bytes and a satisfied wait — and it is deliberately not AC 2's discriminating
  case, which lives in S5.

Add both rows to `initControlFixtureFields`, **in declaration order** — `{"arm", rec.Arm}` after
`claude_version`, `{"control_response_within_wait", rec.ControlResponseWithinWait}` after
`control_response_request_id_matched`. The listing stays hand-written; do not reach for reflection.

Widen the existing `"the version token does not survive slugging"` subtest to cover both literals
(rename the subtest, keep the two checks separate so each keeps its own hazard message). The arm
check's message must name what goes 0-red: the round trip's name assertion, on the arm column,
against both a writer interpolating the arm raw and a writer passing a hardcoded arm.

**Do not rename `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken`.** Its name is
incomplete after this change, not false — it still pins every field and the sluggable version token.
The rename would cascade into prose in `initialize_control_writer_test.go` and in
`finOfflineExecBans`' `initialize_control_record_test.go` entry for no behavioural gain, and every
`-run` filter in the family keys on the `TestInitControlFullRecord_` prefix. Widen the function's
doc comment instead.

### S4 — the writer mints through the arm-carrying namer

In `writeInitControlFixture`, the single path line becomes
`filepath.Join(dir, initControlArmFixtureName(out.ClaudeVersion, out.Arm))`.

- Both inputs come from the record and pass through **unmodified**. The writer formats no filename
  and slugs nothing itself — `initControlArmFixtureName` owns both.
- The doc comment's minting paragraph must move to two inputs and keep #1696's reasoning: a writer
  interpolating its own `"initialize_control_v%s_%s.json"` puts the committed artifact back inside
  the overwrite hazard the lock exists to close, with the lock's own test still green.
- The four `t.Fatalf` messages now name **claude_version and arm and nothing else**. Both are
  already in the filename, so both are safe to print; neither is child output. Update the doc
  sentence that currently says "naming `claude_version` and the error and NOTHING ELSE" — with a
  per-arm path, a failure that cannot say which arm failed is a real diagnosis gap the moment #1715
  writes three.
- Everything else about the writer is unchanged: the shallow copy, the `capFixtureCapture` call on
  `StderrCapture`, `os.MkdirAll`, and the temp-file-plus-rename.

In `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`, `wantName` becomes
`initControlArmFixtureName(rec.ClaudeVersion, rec.Arm)`, and the four-hazard comment above the
directory listing must be re-pointed: the "minted its target name some other way" hazard now names
the arm namer, and the non-vacuity sentence must say that **both** columns carry a literal
`versionSlug` rewrites — not just `claude_version`.

`TestInitControlFixture_WriterCapsStderrCapture` needs no behavioural change; its per-row
`t.TempDir()` comment ("both rows mint the SAME filename from the same `ClaudeVersion`") is now
"from the same `ClaudeVersion` and the same `Arm`" and the reason is unchanged.

**`initControlFixtureName` stays.** Its own lock still proves the one-input namer's properties, and
the arm namer's collision subtest compares the two namers' output against each other — deleting it
deletes that collision proof. It simply stops having a non-test caller.

### S5 — the incoherent pair, written and read back

New test in `initialize_control_writer_test.go` — the writer file, because it is the one file in the
family whose `finOfflineExecBans` entry permits `os.WriteFile` / `os.ReadFile`.

Suggested name: `TestInitControlFixture_RoundTripsAnUnansweredWaitBesideCapturedBytes`.

Scenario, not code:

- Start from `initControlFullRecord()` and set `ControlResponseWithinWait = false`, leaving
  `ControlResponses` exactly as the fixture carries it. This is the pair the record must be able to
  express and the fully-populated fixture cannot: bytes captured, wait not satisfied.
- **A `t.Fatalf` vacuity control before the write**, following `precheck`'s precedent in the cap
  test rather than a skip: `len(rec.ControlResponses)` must be non-zero. If a later edit empties
  the fixture's responses, this row silently stops discriminating while still passing, and a row
  that cannot discriminate is a broken instrument.
- Write through `writeInitControlFixture` into a `t.TempDir()`, read the file back, decode.
- Assert the read-back carries `ControlResponseWithinWait == false` **and** non-empty
  `ControlResponses`. Both halves are needed: the first is the claim, the second is what makes the
  first mean something other than "the field is always false".
- Failure message names the mutant it catches: a field derived from the response count instead of
  from the wait's own result reads back `true` here while the main round trip — which carries the
  coherent pair — stays green.
- Do not print the record or the response bytes. Report the two facts and the field names.

Why the live pair is reachable and this is not a hypothetical: `setModeWaitFor` returns when the
budget expires, and `ControlResponses` is snapshotted later, after `stdinPipe.Close()` and
`cmd.Wait()`. A response landing in that window is captured with the wait already false.

### S6 — the offline decision about the caller's arm

New test in `initialize_control_probe_test.go`, beside `TestInitControlSummarize_ReadsAllThreePlacements`
— the precedent for an offline test in an exec-ing file, and the file a developer changing the arm
actually edits. It spawns nothing, reads nothing off disk, and must PASS (not SKIP) with no claude
and no credentials.

Suggested name: `TestInitControlProbedArm_IsExactlyOneDeclaredNonEmptyArm`.

Scenario:

- `got := initControlProbedArm()`.
- Assert `got != ""`. **Sole red** for a table where no row carries `probed` and for one where two
  do — the selector collapses both, and the message must name both causes so the diagnosis is not
  ambiguous.
- Assert `got` is among the ids ranged out of `initControlArms`. **Sole red** for a selector that
  returns a string the table does not declare: a hardcoded fallback, a typo'd literal, a mangled
  return. That is the mutant AC 4 calls "a mistyped arm", and it is the assertion that keeps the
  selector's from-the-table property true against a future edit that reintroduces a literal.
- **Do not assert the literal `"after_completed_turn"`.** That is precisely the second spelling the
  ticket forbids; which arm is probed is fixed by the marked row and by the probe file's header,
  not by a string in a test.

Extend the probe file's runbook `-run` block to name both offline tests alongside the live one — it
currently names only `TestRealClaude_InitializeControl`, which already misses the summarize test.

### S7 — the caller records both facts

In `runInitControlChild`:

- Capture the bounded wait's result instead of discarding it:
  `withinWait := setModeWaitFor(rec.controlResponseCount, 1, initControlControlBudget)`, with the
  existing `if !withinWait { t.Logf(…) }` kept verbatim. The log stays; the record is what makes it
  durable.
- Take the arm once near the top: `arm := initControlProbedArm()`.
- The record literal gains `Arm: arm` and `ControlResponseWithinWait: withinWait`.
- Add `arm=%q` and `within_wait=%v` to the closing summary `t.Logf`. Both are safe — neither is
  child output. Still never `%+v` the record.
- The file header's "One arrangement, and it is not a guess" section should name the identifier the
  run now records and point at the marked row as the single place that decides it.

#1715 will turn `arm` into a parameter when it ranges the table for three arms; leaving it a local
call keeps this slice at one line and makes that change obvious.

### The re-pointing sweep

Two mechanical passes. Both are comment-and-message text; neither changes behaviour.

**`#1713` → this ticket.** Ten sites in `initialize_control_names_test.go` and none elsewhere in
the package. #1713 closed as a split and this slice performs the migration it named. Eight are doc
comments (the header's "Two namers, one family" section, `initControlArms`' doc, and
`initControlArmFixtureName`'s doc including its `CONTRACT for #1713's writer` line, plus the arm
lock's own doc and its `hostileArms` note); two are **failure-message strings** inside the
containment subtest, which the ticket calls out explicitly. Where the sentence describes work this
slice performs, rewrite it as performed rather than merely renumbering it — "#1713 is what migrates
`writeInitControlFixture` onto this one" is false in both halves once S4 lands.

**Count words.** These rot silently and a filename grep will not find them. Sweep for both the word
and the numeral form, and check ordinals too:

| Where | Now | After |
|---|---|---|
| `initControlFixtureRecord` doc | "Eighteen of its twenty-two fields" | Nineteen of its twenty-four |
| `initControlFullRecord` doc | "every one of the twenty-two fields" | twenty-four |
| `initControlFullRecord` doc | "all three bools are true" | all four |
| `initControlFullRecord` doc | "Three literal choices are load-bearing" | Four (the arm literal is the new one) |
| `initControlFixtureFields` doc | "lists rec's twenty-two fields once" | twenty-four |
| `initialize_control_names_test.go`, the `patterns` comment | "package-scope surface at five identifiers" | seven — `initControlFixtureName`, `initControlArmFixtureName`, `initControlArm`, `initControlArms`, `initControlProbedArm` and the two tests |

Nothing else in the family carries a count this change moves: `compactInitControlRawRows`' "three
raw-JSON rows", the record's "three verbatim-bytes fields" and the cap test's "two rows" are all
unaffected.

---

## Concurrency model

No goroutines are added, started or changed. Three points to preserve:

- `initControlArms` stays **read-only and ranged**, now from three files rather than two. Nothing
  hands the slice out, so no defensive copy is needed and none should be added. `initControlProbedArm`
  ranges it and returns a string — it must not sort, filter in place, or otherwise mutate.
- `initControlFullRecord` keeps returning a **fresh pointer per call** and must not become a
  package-level var. Both new tests mutate the record they receive (S5 sets a bool; the cap test
  already sets a string) from `t.Parallel()` subtests, which is exactly the `-race` hazard that
  doc comment predicts.
- S5's record mutation happens inside its own test function against its own `t.TempDir()`, so it
  shares nothing with the round trip's artifact.

`runInitControlChild`'s single reader goroutine, its `readerDone` join and its context budget are
untouched. `withinWait` is read on the same goroutine that wrote it, before the join.

---

## Error handling

Test-only code, so every failure path is a `t.Fatalf` or `t.Errorf`. Three rules hold across it:

- **The writer's failures name `claude_version` and `arm` and nothing else.** Both are already in
  the filename; a `%+v` of the record would move up to `stderrFixtureCap` bytes of child output out
  of the bounded file and into an unbounded run log.
- **A broken instrument fatals; a measurement records.** S5's vacuity control is a `t.Fatalf`
  because a row that cannot discriminate is not a passing test. `runInitControlChild`'s existing
  policy is unchanged: an unsatisfied wait is information and now lands in a field, not a fatal.
- **`initControlProbedArm` returns `""` rather than fataling.** It is a pure function with no
  `*testing.T` — the same discipline both namers hold — so the degenerate table surfaces as S6's
  red and as an empty `arm` in a live fixture, not as a helper reaching for a `t` it does not have.

---

## Testing strategy

Every mutant below must be reddened by the named assertion, and no assertion may be dead weight.
Verify by real edit-and-revert or by `go test -overlay=<abs-path json>` — `-overlay` is valid for
all of these because they are ordinary compiled-code mutants. It is **not** valid for
`TestFinOfflineFilesReachNoExecHelper`, which parses source off disk at run time; this slice adds
no ban entry, so that limit does not bite here.

| # | Mutant | Sole red |
|---|---|---|
| M1 | Writer left on `initControlFixtureName(out.ClaudeVersion)` | round trip's exactly-one-entry assertion (minted name carries `_<armslug>`, written name does not) |
| M2 | Writer interpolates its own `"initialize_control_v%s_%s.json"` with the arm raw | same assertion — `after_completed_turn-FIXTURE` vs the minted `after_completed_turn-fixture` |
| M3 | Writer passes a hardcoded arm instead of `out.Arm` | same assertion |
| M4 | `ControlResponseWithinWait` derived from `len(ControlResponses) > 0` | S5 only. The main round trip carries the coherent pair and stays green. |
| M5 | Fixture's arm "corrected" to a clean declared arm | the widened does-not-survive-slugging subtest. Without it M2 and M3 both go 0-red with everything else green. |
| M6 | Either new field left at its zero value in the fixture | the non-zero subtest |
| M7 | A new field added with no listing row | the listing-length subtest (`reflect.NumField` vs `len(rows)`) |
| M8 | `probed` removed from every row, or set on two | S6's non-emptiness assertion |
| M9 | `initControlProbedArm` grows a fallback returning a literal not in the table | S6's membership assertion |

**What this slice does not catch, stated rather than claimed away.** A misspelled `arm` or
`control_response_within_wait` JSON tag round-trips green — the read-back decodes through the struct
that wrote the file, and #1662's code review established that even a *unique* wrong tag survives,
since only a colliding tag makes `encoding/json` drop both. The instrument is a reviewer diffing the
hand-written `initControlFixtureFields` names against the struct's tags, which is why that listing
must stay hand-written. This matters more than usual here: `arm` is `setModeFixtureRecord`'s tag and
a divergence would be a gratuitous one in a field two records share.

**Commands.** The package is behind the `e2e_realclaude` tag, so `make check` never compiles it and
the suite exits 0 both on a build failure and on a full credentials skip. **Read the count of tests
that executed, never the exit code.**

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/

go test -tags e2e_realclaude -race -count=1 -v -run \
  'TestInitControlFullRecord_|TestInitControlFixture_|TestInitControlFixtureName_|TestInitControlArmFixtureName_|TestInitControlProbedArm_|TestInitControlSummarize_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/

make check
```

`go vet` is not optional: it is what compiles the tagged package, and the S1 shape change breaks
every ranger of `initControlArms` that was not updated. Every test in the `-run` set must report
PASS — none may SKIP or report "no tests to run". `make check` must stay green because
`internal/e2e/internal/fakeclaude`'s untagged `captureModelKeySets` globs the committed capture; it
walks `control_responses[].response.response.models[]` through generic maps and cannot see new
record fields, and this slice writes nothing into the real `testdata/`.

`TestRealClaude_InitializeControl_Capture` is live and is **not** run in this slice.

---

## Scope fence

- **Test-only. No production file changes.** Nothing under `cmd/` or `internal/` outside
  `internal/e2e/realclaude/*_test.go`.
- **Offline. Must PASS, not SKIP, with no claude and no credentials.** The writer settles against a
  `t.TempDir()`, which is what `dir` being a parameter exists for.
- **Nothing writes into the real `testdata/`.** `testdata/initialize_control_v2.1.239.json` stays
  exactly where it is and is **not** renamed into the arm-carrying shape. Its bytes carry no `arm`
  field, so a filename claiming an arm over them would misrepresent the capture — and moving or
  deleting it trips `captureModelKeySets`' "glob matched no file" fatal inside `make check`.
  Superseding it belongs to the live three-arm run. (The inherited claim that
  `initialize_control_v*` is matched by no glob was scoped to the `realclaude` package and is false
  unscoped as of #1692; do not carry the unscoped form forward.)
- **No `finOfflineExecBans` edit.** No file gains or loses a banned name, and no new file is added.
- **`initControlFixtureName` is not deleted.**
- **No second arm table**, and no separate send-point field on the record: the arm identifier
  already names the send point. The positional anchor into the recorded stdout stream is #1723's.
- **`probeOutcome` and the four committed `set_permission_mode_*` fixtures are untouched**; #1595's
  tests pass unmodified.
- **No knowledge-base doc.** The documentation phase folds this ticket's lessons into
  `docs/knowledge/features/e2e-realclaude.md` after code review. Do not edit it, and do not edit
  `docs/knowledge/INDEX.md`.

---

## Open questions

- **Whether `probed` survives #1715.** It should not: once the rig ranges the table for three arms,
  the arm is a parameter and no row is special. The field's doc comment should say so, so #1715
  deletes it rather than working around it.
- **Whether `runInitControlChild` should take the arm as a parameter now.** Left as a local call:
  it is one line today and #1715 changes the signature anyway when it ranges the table. Flagged so
  the choice reads as a decision rather than an oversight.
- **Nothing deterministic pins that the probe actually sends at the after-a-completed-turn point.**
  S6 decides that the recorded identifier is a declared, non-empty arm; that the send *happens*
  after the turn completes is a live property of `runInitControlChild`'s ordering, and
  `turn_boundaries` plus the existing "no result line within budget" log are what tell a reader
  whether the intended arrangement was reached on any given run. Out of scope here; it is the
  three-arm rig's to settle.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and the boundary this slice moves is the one the family
  exists around: an in-repo string becoming a filesystem path component. It stays **single and
  explicit** — `initControlArmFixtureName`, which puts *both* inputs through `versionSlug`, and
  `writeInitControlFixture` mints its path internally so no caller can hand a path in past the
  namer. What changes is that the arm column is now reached by a *record field* rather than only by
  a test literal, which widens who can populate it: the fully-populated fixture (a literal), and
  `runInitControlChild` via `initControlProbedArm` (a table row). Both are in-repo, developer-typed
  values — no child output, no environment, no filesystem read reaches either column. The untrusted
  data in this family (`StderrCapture`, `ControlResponses`, `StdoutEvents`) reaches the file's
  *contents* and never its name, and this slice does not change that.
- **[File operations — path traversal]** SHOULD FIX, already discharged by the design and by an
  existing lock. Traversal through the arm column is the live hazard rather than a hypothetical:
  `setModeFixtureName` in this same package interpolates its arm raw, so the wrong pattern is
  present to copy. S4 mandates the arm namer, whose containment subtest already ranges `a/b`,
  `../..` and `/abs` in **both** columns; #1712 measured that a raw-interpolating namer reddens
  exactly there. The developer must not add a membership or shape guard inside the writer — the
  ticket rules it out, and it would fatal the round trip on the fully-populated fixture's own
  synthetic arm. The guarantee is lexical and is about the name, not the filesystem: choosing `dir`
  stays the caller's job, and the one live caller passes `filepath.Join(packageDir(t), "testdata")`,
  which has no untrusted component.
- **[File operations — atomicity, permissions, TOCTOU]** No findings. `writeInitControlFixture`'s
  temp-file-plus-rename, its `0o644` mode and its `os.MkdirAll(dir, 0o755)` are unchanged; the
  artifact is public evidence for a public repo, not a secret. No `os.Stat`-then-open anywhere, no
  symlink decision, and no new file-creating code path. One consequence of S4 worth naming: the
  minted path now varies with `Arm`, so two records differing only in arm no longer overwrite each
  other — that is the *point* of the slice, and it strictly reduces silent-loss surface.
- **[Tokens, secrets, credentials]** No findings, and one hazard actively avoided. The two new
  fields carry an in-repo identifier and a boolean; neither can hold child output. `Arm` becoming
  printable in the writer's four `t.Fatalf` messages is safe for the reason `claude_version`
  already is — it is in the filename and it never came from the child. The prohibition that must
  survive this edit is the one on `%+v`-ing the record: `StderrCapture` holds up to
  `stderrFixtureCap` bytes of real claude stderr, and an auth failure is exactly what makes claude
  write a long credential-bearing message there. S7 adds two verbs to an existing `t.Logf` and must
  not widen it. `initControlScrubbed` still runs before the write on the live path and is untouched.
- **[Error messages, logs, telemetry]** SHOULD FIX, specified: S5's failure messages must report
  the two field names and the response *count*, never the response bytes. `initControlFullRecord`'s
  values are synthetic so printing them is safe in the round trip that already does it, but S5 is a
  new message site in a file whose sibling tests deliberately report lengths and 16-byte prefixes —
  the new test must not become the place that pattern is broken, because `runInitControlChild`
  fills the same record from a live child.
- **[Subprocess / external command execution]** No findings — this slice spawns nothing. S6 lives
  in an exec-ing file but calls only a pure selector, and the ticket's PASS-not-SKIP requirement is
  what keeps it honest: a test that reached `resolveClaudeBin` would skip on a credential-less
  machine and read as a pass. The three offline files' `finOfflineExecBans` entries are unchanged
  and still enforced over their ASTs.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against
  a secret. `initControlRequestID` remains a fixed correlation literal, and its doc already records
  why that is not a security token.
- **[Network & I/O]** Not applicable — no socket, no HTTP, no reader over an untrusted stream is
  added. The one existing input bound in the family, `setModeScanMax` on the child's stdout, is
  untouched, and `ScannerError` still surfaces an over-long line rather than truncating silently.
- **[Concurrency]** No findings. No goroutine is added or changed; `initControlArms` stays
  read-only under `t.Parallel()` ranging from one more file, and `initControlFullRecord`'s
  fresh-pointer-per-call contract is restated in this spec precisely because S5 adds a second
  parallel mutator of the returned record.
- **[Threat model alignment]** Not applicable to `docs/protocol-mobile.md` — no relay, no device,
  no wire format. The threat this family actually defends against is a repo-local one and is
  addressed above: committing a credential-bearing or misattributed artifact to a public
  repository. This slice reduces misattribution (an arm-named record and an arm-named path) and
  changes nothing about credential exposure.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
