# #1723 — record the `initialize` capture's send point and the observables after it

**Ticket:** https://github.com/pyrycode/pyrycode/issues/1723
**Size:** s · **Labels:** `enhancement`, `security-sensitive`
**Scope:** test-only, offline. No production file changes. No live claude. **One file changes.**

---

## Files to read first

Everything below is in `internal/e2e/realclaude/` unless noted. Every file here is behind the
`e2e_realclaude` build tag, so `make check` never compiles it.

| File | Symbols | What to extract |
|---|---|---|
| `initialize_control_record_test.go` | `initControlFixtureRecord`, `initControlFullRecord`, `initControlFixtureField`, `initControlFixtureFields`, `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken` | **This is the only file you edit.** The 24-field contract and its per-field doc paragraphs; the deliberately-incoherent fully-populated fixture and its numbered list of load-bearing literal choices; the hand-written listing and why it must never be reflection-generated; the four existing subtests you extend rather than replace. |
| `initialize_control_writer_test.go` | `TestInitControlFixture_RoundTripsEveryFieldIntoOneNamedEntry`, `compactInitControlRawRows`, `writeInitControlFixture` | **Read, do not edit.** The round trip zips `initControlFixtureFields` over both sides, so the new rows ride for free — that is what discharges AC 4's round-trip half. Its `wantTouched` subtest is the canary the ticket names: it must stay green **and unedited**. |
| `set_permission_mode_probe_test.go` | `setModeRecorder.add`, `probeOutcome`, `setModeTurnWindows`, `setModeProbeOutcome` | `add`'s switch is the definition of a `system`/`init` line and of a `result` line, and of the index units `turn_boundaries` records — the anchor's doc has to agree with it. `probeOutcome` is what the ticket forbids you to touch: read it to see why (it is embedded in four committed fixtures and compared by its own `equal`). |
| `inband_bypass_revoke_fixture_test.go` | `fixtureFieldNonZero` | The kind switch the non-zero property runs on: container kinds are judged on `Len()`, everything else on `IsZero()`. That is why an empty trailer slice reads zero and why a `float64` cost of `0` reads zero. |
| `initialize_control_probe_test.go` | `runInitControlChild`, `initControlSummarize` | **Read, do not edit.** The one live filler of this record. It builds the record with named fields, so new fields need no edit there and stay at their zero values until #1715 — which the ticket accepts explicitly. |
| `testdata/initialize_control_v2.1.239.json` | — | The committed capture. Its one `result` line carries `num_turns` and `total_cost_usd`; its `system`/`init` line carries `subtype:"init"`. This is where the two wire key names in the new nested type come from. Do not modify or rename it. |
| `offline_exec_ban_test.go` | `finOfflineExecBans` | Read the `initialize_control_record_test.go` entry only. **You add no name and remove none** — this slice introduces no import and no I/O in that file. |
| `docs/knowledge/features/e2e-realclaude.md` § `initialize_control_record_test.go` | — | Three recorded lessons, two of which bind here: a duplicate listing row reddens **more** subtests than predicted (so a mutant matrix is checked by failure *message*, not by red count), and a fixture-wide property can forbid the very pair a field exists to express. **Read-only** — the documentation phase owns this file. |

---

## Context

`initControlFixtureRecord` carries `stdout_events` verbatim and `turn_boundaries` — the index, in
`stdout_events` positions, of every `result` line. Everything about the run is reconstructable from
those two by hand, and nothing in the record reads what the session did **after** the control
request was written. #1715 drives three arms and compares them; a comparison whose reads exist only
in a reviewer's head is not a recorded measurement.

This slice fixes the shape those reads land in, and does **not** populate them. That is the same
move #1701 made for the fields #1688 fills, and the ticket states the consequence and accepts it:
until #1715 lands, a live re-run writes the new fields at their zero values, and a zero anchor is a
legitimate value rather than a marker, so such an artifact is not self-describing. The committed
`initialize_control_v2.1.239.json` already predates `arm` and `control_response_within_wait`.

Three properties of the existing family constrain everything below:

1. **The window needs one anchor, not three.** Every read AC 2 asks for covers
   `stdout_events[anchor:]`. Recording the window per read would let two reads disagree about which
   window they measured.
2. **No read may be a second recording of a figure the record already carries.** The whole-run
   `result` count is `len(turn_boundaries)`; the window's is the entry list's own length. Neither
   gets a field. (This is the defect PO caught in the first draft of AC 2.)
3. **`probeOutcome` is the wrong carrier**, and the ticket says why: it is embedded in four
   committed `set_permission_mode_*` fixtures and in `poolRevokeFixtureRecord`, and compared
   whole-value by `equal`. A field added there rewrites committed artifacts. The turn-and-cost read
   is additive and separate.

No ADR. This is one more slice of the `initialize_control_*` family whose decisions already live in
`docs/specs/architecture/1696-*.md`, `1701-*.md`, `1702-*.md`, `1712-*.md` and `1722-*.md`, and
whose evergreen prose lives in `docs/knowledge/features/e2e-realclaude.md`.

---

## Design

Five changes, **all in `initialize_control_record_test.go`**. No other file — production or test —
is edited.

### S1 — the nested per-trailer type

Declared immediately **before** `initControlFixtureRecord`, under the same `--- the record ---`
section header, so a reader meets the element type before the field carrying it.

```go
// One recorded `result` trailer inside the send-point window.
type initControlResultTrailer struct {
	NumTurns            int     `json:"num_turns"`
	TotalCostUSD        float64 `json:"total_cost_usd"`
	TotalCostUSDPresent bool    `json:"total_cost_usd_present"`
}
```

Decisions the doc comment must carry:

- **The two read-from fields mirror claude's own key names.** A real `result` line carries
  `num_turns` and `total_cost_usd` — check the committed capture. Mirroring makes the record's
  provenance checkable against the `stdout_events` bytes sitting in the same file, and makes the
  read site unambiguous for #1715.
- **`TotalCostUSDPresent` is POPULATED from the trailer's raw bytes and is never derived from
  `TotalCostUSD`.** This is AC 3's contract obligation and the paragraph the ticket demands. A
  trailer that carried no cost field is a different fact from one that carried zero, and a reader
  who tidies the flag into `TotalCostUSD != 0` deletes the measurement. State the precedent by name:
  `initControlSummarize` decides `ModelsPresent` on raw bytes for exactly this reason
  (`"models":[]` and an absent `models` both decode to nil), and `ModelsPresent`/`ModelsCount` is
  the pairing this one copies.
- **State the limit honestly, in the doc, rather than implying coverage this slice does not have.**
  Nothing computes this flag in this slice, so the derived-from-the-value mutant is not reachable
  here — the contract is the doc, the fixture carries one entry of each shape, and #1715 is where
  the mutant becomes reachable. Do not build a test that pretends otherwise.
- **No `omitempty` on any field**, matching every other tag in this family. Say what that buys and
  what it does not: it keeps `total_cost_usd: 0` and `total_cost_usd_present: false` visible as
  present keys in a committed artifact a human reads. It is **not** what makes the round trip pass —
  `reflect.DeepEqual` over the decoded slice holds either way, because both sides decode through
  the same struct.
- **No `json.RawMessage` anywhere in this type.** The ticket names wrapping the cost "to be safe" as
  precisely the edit that trips the compactor's touched-name assertion. A `float64` survives
  `MarshalIndent` and decode exactly, so `reflect.DeepEqual` over the row holds with no normaliser.

### S2 — three fields on the record

On `initControlFixtureRecord`, as one new group immediately **after** `TurnBoundaries` and before
`StdinWriteErrors` — beside the fields whose index units the anchor borrows.

| Field | Tag | Notes |
|---|---|---|
| `SendPointIndex int` | `send_point_index` | The anchor. |
| `AfterSendPointSystemInitCount int` | `after_send_point_system_init_count` | Window-scoped count of `system`/`init` lines. |
| `AfterSendPointResultTrailers []initControlResultTrailer` | `after_send_point_result_trailers` | One entry per `result` line in the window, in arrival order. |

Doc-comment obligations on the record type — these are the deliverable, not decoration:

- **`SendPointIndex` is the number of `stdout_events` lines already recorded when the arm reached
  its send point**, in the same index units `turn_boundaries` uses (`setModeRecorder.add` assigns
  both). The window every field below reads is `stdout_events[anchor:]`.
- **Both edges are legitimate values, not errors.** `0` means the send point preceded every recorded
  line — which is `before_first_turn`'s real value, not a sentinel. `len(stdout_events)` means
  nothing followed it. **DO NOT ADD A PRESENCE FLAG BESIDE THE ANCHOR**, and say so in the doc: the
  ticket rules it out, the symmetry with `TotalCostUSDPresent` is the trap, and a flag would make
  `before_first_turn`'s honest reading indistinguishable from a bug.
- **The `control_no_request` arm carries the anchor of the equivalent point its drive sequence
  reached.** It writes no request; it still has a point in its sequence corresponding to where the
  other arms write, and that is what it records, so the three arms' reads cover comparable windows.
  This is the contract #1715 implements; nothing here enforces it.
- **No whole-run figure is recorded a second time.** The whole-run `result` count is
  `len(turn_boundaries)` and the window's is `len(after_send_point_result_trailers)`; neither is a
  field. Name both so the next slice does not add one.
- **A `system`/`init` line is `"type":"system"` with `"subtype":"init"`** — the same pair
  `setModeRecorder.add` switches on. Point at that switch rather than restating the JSON in a second
  place.
- **These fields are POPULATED, never computed here**, and until #1715 a live re-run leaves all
  three at their zero values. Say that plainly: an artifact carrying `send_point_index: 0` today is
  an unpopulated field, not a `before_first_turn` capture, and only the ticket that fills them makes
  the two distinguishable. Recording this in the doc is what keeps a later reader from
  over-reading the committed artifact.

The record's field total moves from twenty-four to **twenty-seven**. Nineteen shared with
`setModeFixtureRecord` is unchanged — none of the three is a `setModeFixtureRecord` field.

### S3 — the fully-populated fixture

In `initControlFullRecord`, matching the declaration order above:

```go
SendPointIndex:                4,
AfterSendPointSystemInitCount: 5,
AfterSendPointResultTrailers: []initControlResultTrailer{
	{NumTurns: 6, TotalCostUSD: 0.0731, TotalCostUSDPresent: true},
	{NumTurns: 9, TotalCostUSD: 0, TotalCostUSDPresent: false},
},
```

The fixture's numbered list of load-bearing literal choices grows by two entries (and its count word
with it — see § The count sweep):

- **The two new ints must not be 1, 2 or 3, and must differ from each other.** #1701's
  same-typed-non-bool distinctness subtest binds every `int` row against `models_count` (2),
  `non_json_line_count` (3) and `exit_code` (1). A "tidier" value collides and the subtest reddens.
  `duration_ms` is `int64`, a different `reflect` type, so it constrains nothing here.
- **The trailer list carries one entry of each cost shape** — one with the flag true beside a
  non-zero cost, one with the flag false beside a zero cost. That is AC 3's discriminating pair, and
  it is why AC 3 needs no second record instance: unlike #1722's bool, a slice can hold both shapes
  at once. `NumTurns` is non-zero in both for realism; AC 4 only requires non-zero in at least one.
- The cost literal is an ordinary decimal. Do not reach for `NaN` or `+Inf` (`json.Marshal` errors
  on both) or a value whose shortest representation is exponential; a plain decimal marshals and
  decodes exactly, which is what keeps the round-trip row honest with no normaliser.

**The fixture stays deliberately incoherent and must not be "fixed".** An anchor of `4` sits past
the end of the two-element `stdout_events`, exactly as `TurnBoundaries: []int{0, 7}` already does.
Cite that existing literal in the doc so the incoherence reads as inherited practice rather than an
oversight. Coherence here is not free: it costs the distinctness the whole fixture exists to carry.

**The 0-edge is NOT demonstrated by the fixture, and needs no second record instance.** The non-zero
property forces `SendPointIndex` non-zero, so the fixture cannot show the legitimate `0`. That is
the same shape as #1722's bool — but the resolution is different, and the doc must say why:
#1722 needed a second instance because a *derived* field would have read back wrong there, and this
slice computes nothing. A second instance here would pin `0 == 0` through a writer that never
touched the value. Do not build one.

### S4 — the two listings

Add three rows to `initControlFixtureFields`, in declaration order, after `turn_boundaries`:
`{"send_point_index", rec.SendPointIndex}`, `{"after_send_point_system_init_count", …}`,
`{"after_send_point_result_trailers", …}`. Hand-written, as the rest — the listing's whole job is to
be a second independent copy of the tags, and its doc already says why reflection destroys that.

Then a second listing for the nested type, beside `initControlFixtureFields` and reusing
`initControlFixtureField`:

```go
// initControlTrailerFields lists tr's fields once, in declaration order.
func initControlTrailerFields(tr initControlResultTrailer) []initControlFixtureField
```

- **By value, not by pointer** — three scalars, and the caller ranges a slice.
- Its doc carries the same hand-written obligation, and one more the record's does not:
  `reflect.TypeOf(initControlFixtureRecord{}).NumField()` counts the record's **own** fields, so the
  nested type's fields are invisible to every existing property. This listing plus S5's two subtests
  are the only thing standing between a field added to `initControlResultTrailer` and no coverage at
  all.
- Also state what does **not** bind here: the record's distinctness property groups by
  `reflect.TypeOf`, and `[]initControlResultTrailer` has no same-typed sibling, so distinctness
  gives the row nothing and nothing at all binds the fields **inside** the type. Its coverage is
  S5's length assertion plus the non-zero-in-at-least-one-entry property.

### S5 — three subtests on the existing test

All three go inside `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken`, beside
its four existing subtests. **Do not add a new top-level test function and do not rename this one**:
every `-run` filter in the family keys on the `TestInitControlFullRecord_` prefix, the parent
already computes `rec`, and #1722 recorded the same decision for the same reason. Each is
`t.Parallel()` and reads the parent's `rec` without mutating it.

Scenarios, not code:

1. **"the trailer listing covers every trailer field exactly once"** — take
   `initControlTrailerFields` over any one entry; assert its length equals
   `reflect.TypeOf(initControlResultTrailer{}).NumField()`, and that no row name repeats. Both
   halves, for the record listing's stated reason: length alone is green against a listing that
   names one field twice and omits another. The failure message must say that the record's own
   `NumField` check cannot see this type, so a field added here with no row is unchecked everywhere.

2. **"every trailer field is non-zero in at least one entry"** — OR `fixtureFieldNonZero` across the
   entries, per row index, and report every field that was zero in all of them. Row order is stable
   across entries because the listing is hand-written in declaration order.
   - **A `t.Fatalf` vacuity control first**: an empty `AfterSendPointResultTrailers` makes the
     property vacuously true. Fatal, not skip — `precheck`'s precedent in
     `TestInitControlFixture_WriterCapsStderrCapture`.
   - The message must state why this is per-field-across-entries rather than per-entry: the record's
     own non-zero property, applied per entry, would forbid the absent-cost entry AC 3 requires.

3. **"the trailer list carries an entry of each cost shape"** — assert at least one entry with
   `TotalCostUSDPresent` true and at least one with it false. **Scoped to the flag alone, not to the
   flag plus a non-zero cost**: pairing it with the cost value would make this subtest fire as
   collateral whenever subtest 2 fires, and each subtest here is meant to be the sole red for its
   own mutant. Its message names what a collapsed fixture empties: the round trip stops proving that
   two entries survive carrying *different* flags, which is the whole of AC 3's round-trip half.

### The count sweep

Count words rot silently and a filename grep will not find them. Sweep for the word form, the
numeral, and ordinals.

| Where | Now | After |
|---|---|---|
| `initControlFixtureRecord` doc | "Nineteen of its twenty-four fields" | twenty-seven (nineteen is unchanged) |
| `initControlFullRecord` doc | "every one of the twenty-four fields" | twenty-seven |
| `initControlFullRecord` doc | "Four literal choices are load-bearing" | six, with the two new items appended |
| `initControlFixtureFields` doc | "lists rec's twenty-four fields once" | twenty-seven |

Three claims that look adjacent and **stay true** — do not "fix" them:

- "Arm is the nineteenth" — the new fields are not `setModeFixtureRecord`'s.
- "all four bools are true" (both in `initControlFullRecord`'s doc and in the distinctness subtest's
  comment) — a bool inside the nested type is not one of the record's own fields.
- The distinctness subtest's "`json.RawMessage`, `[]json.RawMessage` and `[]string` are three
  groups" — an example of how `reflect.TypeOf` separates named types, not a count of the record's
  groups. Extending it to name `[]initControlResultTrailer` is optional and harmless; deleting or
  renumbering it is not an improvement.

Nothing outside this file carries a count this change moves. In particular
`compactInitControlRawRows`' "three raw-JSON rows", the round trip's `wantTouched`, the record's
"three verbatim-bytes fields" and the cap test's "two rows" are all unaffected — the new fields are
`int`, `float64`, `bool` and a struct slice, none of which the type switch matches.

---

## Concurrency model

No goroutine is added, started or changed; this slice touches no code that runs while a child is
alive.

- `initControlFullRecord` keeps returning a **fresh pointer per call** and must not become a
  package-level var. It now carries a slice, which makes that contract stricter rather than looser:
  `TestInitControlFixture_WriterCapsStderrCapture` and
  `TestInitControlFixture_RoundTripsAnUnansweredWaitBesideCapturedBytes` already mutate the returned
  record from parallel tests, and a shared var would hand them one backing array.
- S5's three subtests **read** the parent's `rec` and mutate nothing, so sharing it under
  `t.Parallel()` stays safe exactly as the four existing subtests do.
- `writeInitControlFixture`'s `out := *rec` is a shallow copy that shares every slice header with
  the caller. This slice adds a fourth slice-valued field but caps nothing new, so the existing
  caveat is unchanged and no defensive copy should be added. Do not widen the cap to the new field:
  the trailer list is a handful of numbers, not free text from a process this repo does not control.
- Snapshotting the line count at the send point while the recorder runs concurrently with the stdin
  write is a live-path concern and belongs to #1715, not to this record.

---

## Error handling

Test-only code, so every failure path is a `t.Fatalf` or a `t.Errorf`, and two rules carry over:

- **A broken instrument fatals; a measurement records.** S5's empty-list control is a `t.Fatalf`
  because a property that is vacuously true is not a passing test. Everything else is a `t.Errorf`,
  so one bad row does not hide the others.
- **Printing the new values is safe here and only here.** Every value in this file is a synthetic
  literal, so a `%v` of a trailer row is fine — it renders as `[{6 0.0731 true} {9 0 false}]`, not
  as the decimal byte dump a `json.RawMessage` row produces. The prohibition that survives unchanged
  is the one on `%+v`-ing the record anywhere: `StderrCapture` holds up to `stderrFixtureCap` bytes
  of real claude stderr on the live path.

---

## Testing strategy

No new test function; three new subtests and the existing round trip.

**Mutants and their sole red.** Verify by real edit-and-revert or by
`go test -overlay=<abs-path json>` — `-overlay` is valid for all of these because they are ordinary
compiled-code mutants. It is **not** valid for `TestFinOfflineFilesReachNoExecHelper`, which parses
source off disk at run time; this slice adds no ban entry, so that limit does not bite.

| # | Mutant | Sole red |
|---|---|---|
| M1 | A new record field added with no `initControlFixtureFields` row | the listing-covers-every-field subtest's length check |
| M2 | Any new record field left at its zero value in the fixture (anchor `0`, count `0`, empty list) | the non-zero subtest |
| M3 | `SendPointIndex` set to `2` | the same-typed-distinctness subtest (collides with `models_count`) |
| M4 | A field of `initControlResultTrailer` with no `initControlTrailerFields` row, or a row deleted | S5.1 |
| M5 | `TotalCostUSD` zeroed in **both** entries (likewise `NumTurns`) | S5.2 |
| M6 | The two entries collapsed to the same cost shape | S5.3 |
| M7 | `TotalCostUSDPresent` dropped or tagged `json:"-"` | the round trip's `after_send_point_result_trailers` row |
| M8 | `TotalCostUSD` wrapped in a `json.RawMessage` "to be safe" | the round trip's `wantTouched` subtest — the canary the ticket names |

**Read the failure MESSAGE, not the red count.** #1701's recorded lesson: a duplicate listing row
reddened two subtests where the matrix predicted one, and that was invisible to anyone reading only
red/green. Two rows above are known to overlap and must be checked that way — M2 with an emptied
trailer list also trips S5.2's vacuity fatal and S5.3, and M5 on `NumTurns` reddens nothing else.
A "sole red" claim in the table is a claim about which message names the cause.

**What this slice cannot catch, stated rather than claimed away:**

- **A misspelled JSON tag round-trips green.** The read-back decodes through the struct that wrote
  the file, and #1662's code review established that even a *unique* wrong tag survives — only a
  colliding tag makes `encoding/json` drop both. The instrument is a reviewer diffing the two
  hand-written listings against the tags. This matters twice over here: `num_turns` and
  `total_cost_usd` are claude's own key names, and a divergence would make the record's provenance
  claim false against the `stdout_events` bytes in the same file.
- **The "presence derived from the value" mutant is not reachable in this slice.** Nothing computes
  `TotalCostUSDPresent`; the fixture's two entries are literals, and a derived implementation would
  agree with both of them. The contract is the doc, and #1715 is where the mutant becomes reachable
  and where the discriminating case (a trailer carrying `"total_cost_usd": 0` in its raw bytes) can
  exist at all.

**Commands.** The package is behind the `e2e_realclaude` tag, so `make check` never compiles it and
the suite exits 0 both on a build failure and on a full credentials skip. **Read the count of tests
that executed, never the exit code.**

```
go vet -tags e2e_realclaude ./internal/e2e/realclaude/

go test -tags e2e_realclaude -race -count=1 -v -run \
  'TestInitControlFullRecord_|TestInitControlFixture_|TestInitControlProbedArm_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/

make check
```

Every test in the `-run` set must report PASS — not SKIP, not "no tests to run" — on a machine with
no claude and no credentials. `make check` must stay green because `internal/e2e/internal/fakeclaude`'s
untagged `captureModelKeySets` globs the committed capture; it walks
`control_responses[].response.response.models[]` through generic maps and cannot see new record
fields, and this slice writes nothing into the real `testdata/`.

`TestRealClaude_InitializeControl_Capture` is live and is **not** run in this slice.

---

## Scope fence

- **One file: `internal/e2e/realclaude/initialize_control_record_test.go`.** No production file, no
  other test file. If you find yourself editing a second file, stop and re-read this section.
- **`probeOutcome` is untouched**, the four committed `set_permission_mode_*` fixtures stay
  byte-identical, and #1595's tests pass unmodified. The turn-and-cost read is additive and lives on
  the `initialize` record alone.
- **No populator, and no change to `runInitControlChild`.** #1715 fills these fields. A live re-run
  writes them at their zero values in the meantime and the ticket accepts that explicitly.
- **No new raw-JSON-bearing field.** The compactor's touched-name assertion stays green **and
  unedited** — including the third-cause wording code review flagged as non-blocking on #1702, which
  is not this ticket's to fix.
- **No presence flag beside the anchor**, and no second record instance for the anchor's `0` edge.
- **No `finOfflineExecBans` edit.** This slice introduces no import, no `os` call and no exec into
  the record file.
- **Nothing writes into the real `testdata/`.** `initialize_control_v2.1.239.json` stays where it
  is, is not renamed, and is not regenerated — moving or deleting it trips `captureModelKeySets`'
  "glob matched no file" fatal inside `make check`.
- **No knowledge-base doc.** The documentation phase folds this ticket's lessons into
  `docs/knowledge/features/e2e-realclaude.md` after code review. Do not edit it, and do not edit
  `docs/knowledge/INDEX.md`.

---

## Open questions

- **Whether `AfterSendPointSystemInitCount` survives contact with a real three-arm run.** It is a
  count and not a list of the init lines' contents, on the ticket's instruction that no verbatim
  field is added. If #1715 finds it needs the `session_id` or `permissionMode` off those lines, that
  is a field it adds, not a shape this slice got wrong — `stdout_events` holds the bytes either way.
- **Whether the `control_no_request` arm's "equivalent point" is well-defined enough to compare.**
  The record states the contract; only #1715's drive sequence can make the three windows genuinely
  comparable, and the ticket says so ("whether two arms' windows are then comparable is #1715's to
  arrange and not this record's").
- **Whether the two hand-written listings should ever be one.** They cannot be — the row type is
  shared but the field sets are different, and a single reflection-driven listing is exactly what
  both doc comments forbid. Flagged so the duplication reads as a decision.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings, and this slice moves none. The family's one boundary is an
  in-repo string becoming a filesystem path component, enforced in `initControlArmFixtureName` over
  `ClaudeVersion` and `Arm`; the three new fields reach the file's *contents* only and are never
  minted into a name. The other direction is worth stating because it is where the next slice will
  live: `stdout_events` is child output and therefore untrusted, and the new fields are **reads of
  it** — `AfterSendPointSystemInitCount` and the trailer list will, in #1715, be derived from bytes
  claude wrote. In this slice they are literals in `initControlFullRecord`, so no untrusted byte
  reaches them; #1715 inherits the obligation to decode defensively, and the record's doc naming
  them POPULATED is where that is handed over.
- **[Tokens, secrets, credentials]** No findings, and one hazard actively avoided. Every new field is
  a number or a bool: `int`, `float64`, `bool`. **None can hold free text**, which is what keeps them
  outside `stderrFixtureCap`'s concern entirely — an auth failure makes claude write a long
  credential-bearing message to *stderr*, and this slice adds no field that could absorb one.
  `initControlScrubbed` still runs before the write on the live path and is untouched. The
  prohibition that must survive is the existing one on `%+v`-ing the record; S5's messages report
  field names and counts, and this file's existing licence to print its own synthetic literals is
  scoped to this file by its own comment and must not be widened into
  `initialize_control_probe_test.go`.
- **[File operations — path traversal, atomicity, permissions, TOCTOU]** No findings, and no
  file-creating code path is added or edited. `writeInitControlFixture`'s minted path, its
  temp-file-plus-rename, its `0o644` mode and its `os.MkdirAll(dir, 0o755)` are all unchanged, and
  the two inputs the name is minted from (`ClaudeVersion`, `Arm`) are not touched. One consequence
  worth naming: the artifact grows by a bounded, small number of numeric fields — the trailer list
  is one entry per `result` line in a window of a run already bounded by `initControlChildBudget`
  and `setModeScanMax` — so no new unbounded growth reaches a committed file. The record file itself
  performs no I/O in either direction and its `finOfflineExecBans` entry, which bans `os.ReadFile`,
  `os.WriteFile`, `os.Create`, `os.ReadDir` and `filepath.Glob` there, is unchanged and still
  enforced over its AST.
- **[Subprocess / external command execution]** Not applicable — this slice spawns nothing, edits no
  exec-ing file, and adds no argv element. The record file's ban on `resolveClaudeBin`,
  `WithWorktreeAuthenticated`, `WithWorktree`, `probeClaudeVersion` and `captureClaudeVersion` is
  unchanged; the PASS-not-SKIP requirement is what keeps it honest, since a test that reached
  `resolveClaudeBin` would skip on a credential-less machine and read as a pass.
- **[Cryptographic primitives]** Not applicable — no randomness, no hashing, no comparison against a
  secret. `initControlRequestID` remains a fixed correlation literal and is untouched.
- **[Network & I/O — input size limits]** No findings, and the relevant bound already exists and is
  unchanged: `setModeScanMax` caps the child's per-line read and `ScannerError` surfaces an over-long
  line rather than truncating silently. Named here because #1715 will populate the new fields by
  walking `stdout_events`, and the cap on what can enter that slice is what bounds the trailer list.
  Two decode hazards this slice's *shape* hands to that walk, both already precedented in
  `initControlSummarize` and worth carrying: a `result` line whose `total_cost_usd` is a string
  rather than a number must not abort the whole read (decode per-field, skip on error), and the
  presence read must be over raw bytes, which is what the flag exists for.
- **[Error messages, logs, telemetry]** No findings. S5's three subtests report field names, a
  boolean shape and a count. The two facts that make printing safe in this file — every value is a
  synthetic literal, and the file reaches no live child — are already stated in its own comments and
  are unchanged. No telemetry, no metrics, no user-identifiable data.
- **[Concurrency]** No findings. No goroutine is added or changed. The one live hazard in the area —
  snapshotting a line count while the recorder goroutine is still appending — is explicitly **out of
  scope** and belongs to #1715, which the ticket states and this spec repeats in § Concurrency
  model. `initControlFullRecord`'s fresh-pointer-per-call contract is restated because the record now
  carries a fourth slice, and two existing parallel tests mutate the record it returns.
- **[Threat model alignment]** Not applicable to `docs/protocol-mobile.md` — no relay, no device, no
  wire format, no network peer. The threat this family actually defends against is repo-local:
  committing a credential-bearing or misattributed artifact to a public repository. This slice adds
  only numeric fields, so it cannot carry a credential, and it improves attribution by recording
  which window a later comparison read.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
