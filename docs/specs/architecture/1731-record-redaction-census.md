# #1731 — record which redaction classes fired on the `initialize` capture

**Size:** s (re-checked against this spec; see § Size check.)
**Scope:** test-only, `internal/e2e/realclaude`, behind the `e2e_realclaude` build tag. No production file changes.

---

## Files to read first

Read these before writing anything. Every entry is a symbol, resolvable with
`codegraph_search` / `codegraph_node`; there are deliberately no line numbers.

| File | Symbol | What to extract |
|---|---|---|
| `internal/e2e/realclaude/initialize_control_record_test.go` | `initControlFixtureRecord` | The struct and its doc comment. Two things: the field count spelled as a word, and the standing instruction that a string-bearing field added here must be visited by `redactInitControlRecord` — AC5 edits both. |
| " | `initControlFullRecord` | The fixture literal and its six numbered literal-choice notes. You add a value; none of the six constrain it, and the record must stay path-free. |
| " | `initControlFixtureFields` | The hand-written listing. One row added, in declaration order. **Do not regenerate by reflection** — its doc explains why at length. |
| " | `initControlTrailerFields` | The precedent for a nested-type listing, and the reason this slice must *not* add one for `dropcapSubstitution` (see § Design, "What this slice deliberately does not add"). |
| " | `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken` | The three record properties AC3 requires to stay green unmodified. Read `fixtureFieldNonZero`'s use in the non-zero subtest and the `reflect.TypeOf` grouping in the distinctness subtest. |
| `internal/e2e/realclaude/initialize_control_probe_test.go` | `runInitControlChild` | The fill site. The record literal, then the pass, then the write. The census is already computed here and thrown away. Read the comment block above the pass call — it is the one piece of prose this slice falsifies. |
| `internal/e2e/realclaude/initialize_control_redaction_test.go` | `redactInitControlRecord` | The return contract (`[]dropcapSubstitution`) and its "# It REPORTS the census, it does not STORE it" section, which names #1731 as the slice that lands this. |
| " | `newInitControlRedactor` | Four path parameters, no ambient reads. The new test constructs one exactly as the byte-identity row does. |
| " | `TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical` | AC4. Read it closely: it is the sole red for a census stored inside the pass, and it must stay green with no edit. Its four `initControl*Value` constants are what the new test reuses. |
| `internal/e2e/realclaude/initialize_control_writer_test.go` | `writeInitControlFixture` | `out := *rec` — a shallow copy that preserves nil-ness. That is the property AC2's new row rides on. |
| " | `compactInitControlRawRows` | Selection is by Go TYPE (`json.RawMessage`, `[]json.RawMessage`). Confirm for yourself that a `[]dropcapSubstitution` row is invisible to it, which is what keeps the touched-name set at exactly three. |
| " | `TestInitControlFixture_RoundTripsAnUnansweredWaitBesideCapturedBytes` | The placement precedent the new row copies verbatim: a row lands in this file rather than the record's *because it writes and reads back*, and this is the file whose `finOfflineExecBans` entry permits `os.WriteFile` and `os.ReadFile`. |
| `internal/e2e/realclaude/dropped_line_capture_test.go` | `dropcapSubstitution` | The row type: `Class`/`Replacement`/`Count`, tags `class`/`replacement`/`count`. |
| " | `dropcapRedactor.substitutions` | Returns a **non-nil empty** slice when nothing fired, sorted by class. That non-nil-ness is the entire mechanism AC2 protects. |
| " | `dropcapRecord` / `dropcapWriteRecord` | The sibling's `Redaction []dropcapSubstitution` field — copy its **spelling**, not its **placement**. `dropcapWriteRecord` assigns inside the writer; that is the counter-example. |
| `internal/e2e/realclaude/offline_exec_ban_test.go` | `finOfflineExecBans` | The per-file entries. Confirm the writer file's entry permits the os read/write group and that nothing this slice adds is on it. **No edit to this file.** |
| `CODING-STYLE.md` § "Comments — Citing Other Code" | — | Symbol names, never line numbers. `make cite-guard` is diff-scoped and has no depth or range exemption. |

---

## Context

`redactInitControlRecord` rewrites the operator home, the run's temp home, the
child's working directory and the system temp directory out of every
string-bearing field of `initControlFixtureRecord`, in three spellings each. It
returns the classes it applied and how often, and stores nothing. `runInitControlChild`
already binds that census and logs it, then drops it.

A committed fixture that carries no census cannot distinguish two very different
facts: *the redactor ran and found nothing* and *the redactor never ran*. Putting
the census on the record makes the artifact self-describing, and — because
`substitutions()` returns a **non-nil empty** slice while an unfilled field is
**nil** — makes those two facts differ in the marshalled bytes as `[]` versus
`null`.

That distinction is one `omitempty` tag away from gone, and one nil-normalising
helper in the writer away from gone. Nothing in the package notices either today.
AC2 is the instrument that has to.

**No ADR.** This is one field on one test-only record inside a family whose
conventions are already settled and already documented at length in the files
themselves. The documentation phase will carry the two stale numerals in
`docs/knowledge/features/e2e-realclaude.md` (see § Out of scope).

---

## Design

### 1. The record gains one field

`initControlFixtureRecord` gains, as its **last** field (declaration order is the
listing's order, and the census is the last thing the capture learns):

```go
Redaction []dropcapSubstitution `json:"redaction"`
```

Tag spelling and Go type are the sibling family's, unchanged — `dropcapRecord`
carries the identical pair. **No `omitempty`**, for the reason the record's own
doc already gives for every other tag *and* for AC2's reason on top of it: the
tag is what carries the `[]`-versus-`null` distinction into the file.

Its doc-comment paragraph (in `initControlFixtureRecord`'s doc, beside the
paragraphs for the other populated fields) states three things and no more:

- It is **POPULATED at the fill site**, from what `redactInitControlRecord`
  returns, exactly as `ModelsPresent`/`ModelsCount`/`ModelsEntryFields` are
  populated rather than computed. The pass returns it; the pass does not store
  it, and `TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical` is
  what keeps that true.
- **An empty census is not an absent one.** `[]` means the redactor ran and
  nothing matched; `null` means it never ran. A reader of a committed fixture
  must be able to tell those apart, and
  `TestInitControlFixture_DistinguishesAnEmptyCensusFromAnAbsentOne` is what
  stops the distinction being tidied away by an `omitempty` or by a helper that
  normalises nil to empty on the way to the file.
- **An empty census is not a clean artifact.** This is the sentence the field
  most needs and the one a reader is most likely to supply wrongly on their own.
  `[]` says the redactor *ran*; it says nothing about whether the artifact is
  free of operator paths. `dropcapRedactor.add` returns early on an empty value,
  so a class armed with `""` — or with a wrong or transposed path —
  installs no rule at all, matches nothing, and the census then honestly reports
  `[]` while the committed file still carries the real path under a class the
  table never armed. The census is an audit trail over the redactor, not a clean
  bill of health over the artifact; #1729's deny-scan is the fail-closed net that
  makes the second claim, and this field must not be read as making it.
- **`dropcapSubstitution` becomes a committed shape for this family.** Until this
  slice it reached the initialize family only through a `t.Logf`. After it, every
  field of that type is marshalled into `testdata/` and committed to git — and
  per § 2 the type is not visited by `redactInitControlRecord`. That is safe
  today because every field is harness-minted: `Class` comes from the four
  `dropcapClass*` constants, `Replacement` from the `$`-prefixed literals in
  `newInitControlRedactor`'s `addPath` calls, and `Count` is an int. It stops
  being safe the moment that type — which belongs to the *dropcap* family, whose
  next author is not reading this file — gains a field carrying a matched value,
  a sample or a path. Nothing in the package reddens when it does. Say so here,
  because prose is the only guard, and see § Open questions.
- **It needs no cap, and the reason is structural.** The record's doc states that
  nothing in that file caps anything — `StderrCapture` is bounded by the writer's
  `capFixtureCapture` and by nothing here. The census needs no equivalent: its
  length is bounded by the number of armed classes, at most the four
  `newInitControlRedactor` installs, and `Count` is an int. It cannot grow with
  child output, so it is not the shape a cap exists for. Do not add one.
- The **exception to the standing redaction instruction** — see § 2.

### 2. The standing instruction gains its exception (AC5)

The record's doc carries a standing instruction — *a string-bearing field added
here must be visited by `redactInitControlRecord`*. `Redaction` bears strings
and is deliberately **not** visited, so the instruction must record it as its one
exception or the next field added here is directed straight into reddening AC4.

The exception's reason, stated in the doc:

- Its strings are the **redactor's own vocabulary** — class identifiers and
  placeholders (`$HOME`, `$WORKDIR`, `$TEMP_HOME`, `$TMPDIR`) minted by
  `newInitControlRedactor`'s table. They are never child output, never a path,
  and never derived from the environment. Rewriting a placeholder is meaningless.
- The field is assigned **from what the pass returns**, so a pass that visited it
  would have to read a value that does not exist until the pass returns. The
  obligation is circular, not merely unnecessary.

Word it so the exception is scoped to this one field, not to "fields the author
judges safe" — the instruction's value is that it admits no judgement call.

### 3. The fixture carries a non-zero census

`initControlFullRecord` gains a value for the new field. Constraints, in order of
how easy each is to get wrong:

- **At least one entry.** `fixtureFieldNonZero` judges container kinds by length,
  so `[]dropcapSubstitution{}` fails the non-zero property. One entry is enough;
  there is no discriminating-pair argument here of the kind that gives
  `AfterSendPointResultTrailers` two entries, because every substitution has the
  same shape and nothing in this file asserts the census's ordering.
- **The record must stay path-free.** `TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical`
  rests on it. A class name and a `$`-placeholder carry no path; do not put a
  realistic path in `Replacement` "for realism".
- **Literal-choice note 2 does NOT apply.** `<`, `>` and `&` are forbidden in the
  three *raw-JSON* literals because `MarshalIndent` escapes them inside a
  `json.RawMessage`. `dropcapSubstitution` is an ordinary struct of ordinary
  strings, which round-trip unchanged. Stated so nobody adds a fourth constraint
  to that list.
- **Distinctness constrains this row not at all**, and neither does the nested
  `Count`. The distinctness subtest groups by `reflect.TypeOf` of the *row value*,
  which is `[]dropcapSubstitution` — a type with no sibling on this record — and
  it never sees inside the slice, so `Count` cannot collide with `models_count`,
  `non_json_line_count` or `exit_code`. Stated so nobody counts on it either way.

Recommended literal: one entry naming a real class constant with its real
placeholder and a plainly-synthetic count, e.g. `{Class: dropcapClassWorkdir,
Replacement: "$WORKDIR", Count: 3}`. Nothing keys on any of the three, so no
`-FIXTURE` marker is needed — unlike `ClaudeVersion` and `Arm`, whose markers
exist because `versionSlug` runs over them.

### 4. The listing gains exactly one row

`initControlFixtureFields` gains `{"redaction", rec.Redaction}` in declaration
order, i.e. last. The listing's length assertion reads
`reflect.TypeOf(initControlFixtureRecord{}).NumField()` and needs no edit.

**Hand-written. Do not regenerate by reflection.** The listing's own doc gives
the reason: it is the second, independent copy of the JSON tags, and it is the
only instrument in the family that catches a misspelled tag.

### 5. The fill site assigns the census

In `runInitControlChild`, the binding that currently discards the census becomes
an assignment onto the record, and the log line reads the record's own field:

```go
record.Redaction = redactInitControlRecord(red, record)
```

This is the construction-site precedent from the sibling family
(`CredentialScanApplied: scanner.applied()`), **not** `dropcapWriteRecord`'s
`rec.Redaction = red.substitutions()`, which assigns inside the writer and writes
through to its caller's record. `writeInitControlFixture` takes an `out := *rec`
copy and must go on receiving a record it only copies.

The pass's position is unchanged: after the record literal, before the write. The
existing per-log-site placement reasoning above the call — that `scanner_error`
is safe because the summary line reads `record.ScannerError`, and the response
loop is safe only because it ranges `record.ControlResponses` — stays exactly as
it is. Do not move the pass.

**The comment block above that call is falsified by this slice and must be
rewritten.** It currently asserts "The census is REPORTED, never stored: the
record gains no field in this slice — that is #1731". Replace with prose that
keeps the half that stays true — the log line carries class names, replacements
and counts only, never a value, so it cannot leak one, and this is not a widening
of the never-`%+v`-the-record rule — and states the half that changed: the census
is now on the record, assigned here rather than in the pass or the writer.

Keep the *reason* that `%+v` is safe, not just the fact. "Class names,
replacements and counts only, never a value" now constrains the **committed file**
as well as a salvaged run log, because the same values go both places. A rewrite
that drops that clause as stale prose deletes the only statement of why this
`%+v` is permitted where the record's own is not.

While you are in `redactInitControlRecord`'s doc for § 6, tense-correct its
"#1731 is the slice that puts the census on the record" to past tense. Its
surrounding claims — that the pass gains no field, and that a field added *there*
reddens the byte-identity row — stay true and stay put.

### 6. What this slice deliberately does not add

State these in the spec's own record (the doc comments), so a reviewer does not
read them as omissions:

- **No per-field listing for `dropcapSubstitution`.** `initControlTrailerFields`
  exists because `initControlResultTrailer` is this family's own type with no
  other home. `dropcapSubstitution` belongs to the dropcap family. Its three
  fields have no per-field listing anywhere in the package — a pre-existing gap
  in that family, not this slice's to close, and closing it here would put a
  listing for one family's type in another family's file.
- **No edit to `offline_exec_ban_test.go`.** The new row lands in
  `initialize_control_writer_test.go`, whose entry already permits the os
  read/write group because writing and reading back is that file's entire
  subject. The row references `newInitControlRedactor`, `redactInitControlRecord`
  and the four synthetic `initControl*Value` constants; none is on that entry, and
  none reaches `realHome` or `os.TempDir` from this file's syntax. Do not add
  those two names to the writer file's entry "for symmetry" — that entry's
  siblings document at length why a ban list must not carry names the file has no
  route to, and `TestInitControlRedactorArmsOnlyItsCallersValues` is the
  instrument for the constructor.
- **No change to `compactInitControlRawRows`.** It selects by Go type; a
  `[]dropcapSubstitution` row is neither `json.RawMessage` nor
  `[]json.RawMessage`, so it rides free and the touched set stays exactly
  `control_request_sent`, `control_responses`, `stdout_events`.

---

## The AC5 word-count sweep

The count moves **27 → 28**, so the word moves **twenty-seven → twenty-eight**.

Three doc comments in `initialize_control_record_test.go` spell it, one each on
`initControlFixtureRecord`, `initControlFullRecord` and `initControlFixtureFields`.
**Re-run the sweep at implementation time rather than trusting this paragraph** —
#1729 adds a field to this same record and is ordered after this slice.

```bash
grep -in 'twenty[ -]\?seven' internal/e2e/realclaude/initialize_control_record_test.go
```

Afterwards that command must return nothing.

Three traps, each of which a sweep gets wrong in a way no test reports:

1. **Only the total moves.** `initControlFixtureRecord`'s doc reads "Nineteen of
   its twenty-seven fields are setModeFixtureRecord's". The new field is not one
   of those nineteen. **Nineteen stays nineteen**, and so does "Arm is the
   nineteenth" two lines below. A sweep that harmonises both numbers is wrong.
2. **`28` already appears in that file** — "28 bytes, comfortably inside
   `versionSlug`'s 32-character clamp", in literal-choice note 3 about the `Arm`
   literal's byte length. It is not the field count. A numeral sweep for `28`
   after the edit will hit it; leave it alone.
3. **Check for ordinal and numeral residuals, not just the cardinal word.** A
   `\btwenty-seven\b` sweep misses "twenty-seventh" and misses `27`. Sweep
   `-i 'twenty'` and `'\b27\b'` over the file and read every hit.

---

## Testing strategy

### AC2's new row — `TestInitControlFixture_DistinguishesAnEmptyCensusFromAnAbsentOne`

**Home: `initialize_control_writer_test.go`.** Not the record file and not the
redaction file, and the reason is structural rather than aesthetic: the row writes
and reads back, and those two files perform no I/O in either direction — the
record file's `finOfflineExecBans` entry bans `os.WriteFile`/`os.ReadFile`, and the
redaction file's own header declares "no writer, no reader and no fixture". This
is the same placement argument the writer file's header already makes for #1722's
row, in the same words.

**Name: the `TestInitControlFixture_` prefix is load-bearing.** The writer file's
header documents a `-run 'TestInitControlFixture_|TestFinOfflineFilesReachNoExecHelper'`
filter; a row named outside that prefix is silently not run by it.

**Why through the writer rather than a bare `json.Marshal`.** AC2 names two
mutants — an `omitempty` on the tag, and a helper that normalises nil to empty
*on the way to the file*. A marshal-and-compare catches only the first. Writing
both records through `writeInitControlFixture` and comparing the two files
catches both with one instrument, because the writer's own marshal is what
produces the bytes a reviewer reads.

Shape:

- Build one record with `initControlFullRecord`, one redactor with
  `newInitControlRedactor` over the four synthetic `initControl*Value` constants —
  the same construction the byte-identity row uses, so the two rows stay in step.
- Run the pass **once**, keeping its return value.
- **Vacuity precheck, `t.Fatalf` rather than skip** (the family's precedent is the
  trailer precheck in `TestInitControlFullRecord_PinsEveryFieldAndTheSluggableVersionToken`).
  Two distinct failures, worth separate messages:
  - the census is `nil` → `substitutions()` stopped returning a non-nil empty
    slice, and the whole distinction this row measures no longer exists;
  - the census is non-empty → `initControlFullRecord` acquired a path value, so
    the row compares a *populated* census against nil and says nothing at all
    about the empty case. That would also have reddened the byte-identity row, and
    saying so in the message saves a diagnosis.
- Take **two shallow copies of the post-pass record**. Assign the census to one
  and `nil` to the other. Copying after the pass is what makes the two records
  differ in exactly one field, with no reliance on any other test's byte-identity
  claim.
- Write each into its **own `t.TempDir()`** — both records carry the same
  `claude_version` and `arm`, so `initControlArmFixtureName` mints the same
  filename for both and one directory would have them overwrite each other.
- Read both files back and assert the bytes differ. On failure print both, as the
  byte-identity row does; every value in this record is a synthetic literal and
  the diagnostic is worth more than the bytes.

Deliberately **no** `bytes.Contains` assertion pinning `"redaction": []` against
`"redaction": null`. Inequality already reddens for every mutant in the class —
`omitempty` drops the key from both sides, a nil-normaliser renders `[]` on both,
`json:"-"` drops it from both — and a literal-spelling assertion would additionally
pin `MarshalIndent`'s whitespace, which is not this row's subject.

**What this row does and does not measure.** It catches a writer-side helper that
turns a nil census non-nil. It does **not** catch a writer that overwrites a
non-nil census with a different value; nothing in this slice does, and that half
rests on `writeInitControlFixture`'s copy-and-don't-mutate contract with #1729's
byte-identity criterion as its future instrument. Say so in the row's doc rather
than letting a reader credit it with more than it proves.

### The four properties that must stay green *unmodified* (AC3, AC4)

Run them and read the result; do not edit them.

- covers-every-field-once — the listing gains a row, `NumField()` moves with it.
- non-zero — satisfied by the fixture's one-entry census.
- same-typed distinctness — the new row's type has no sibling; it is skipped.
- the round trip, **including** its touched-set assertion of exactly
  `control_request_sent`, `control_responses`, `stdout_events` — the new row is
  neither raw-JSON type, so `compactInitControlRawRows` does not touch it.
- `TestInitControlRedactRecord_LeavesAPathFreeRecordByteIdentical` — green iff the
  census stayed out of the pass.

If any of these needs an edit to pass, the design has drifted; stop and re-read
this section rather than adjusting the test.

### Running it

The package is behind the `e2e_realclaude` build tag, so `make check` never
compiles it and the suite exits 0 both on a build failure and on a full
credentials skip. **Read the count of `=== RUN` lines, never the exit code.**

```bash
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestInitControlFixture_|TestInitControlFullRecord_|TestInitControlRedactRecord_|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Every one of those must report **PASS — not SKIP, not "no tests to run"** — on a
machine with no claude and no credentials.

`make check` still has to pass, and `gofmt` is non-negotiable.

---

## Concurrency model

Nothing concurrent is introduced. Two points worth stating because they are
easy to break by accident:

- `dropcapRedactor`'s counters are **unlocked**. All redaction here runs on the
  test goroutine, and `runInitControlChild` calls the pass after the child has
  exited. Assigning the returned census onto the record adds no new reader.
- **The counters also accumulate across calls**, which is why `runInitControlChild`'s
  doc already requires #1715 to mint a fresh redactor per arm inside its loop.
  This slice raises what a shared redactor would cost: it used to mean one arm's
  counts appearing in another arm's *log line*, and now it means a **false audit
  trail written into a committed artifact**. Do not weaken that requirement, and
  do not hoist the redactor's construction out of the per-run path.
- `initControlFullRecord` returns a **fresh pointer per call** and must never
  become a package-level var — `TestInitControlFixture_WriterCapsStderrCapture`
  mutates the returned record across parallel subtests. The new row takes two
  shallow copies of one record and mutates only its own copies' new field, so it
  inherits that safety; it must not mutate the shared backing arrays of any slice
  field.

---

## Error handling

Test-only, so "error handling" is failure-reporting discipline:

- The vacuity precheck is `t.Fatalf`, never `t.Skip`. A property that cannot
  discriminate is a broken instrument, not a passing test.
- Failure messages name the ticket (`#1731:`) and say what the failure *means*,
  in this family's house style — what a reader who arrived here after "tidying"
  something has actually emptied.
- Never `%+v` the record into a log or a fatal message. Printing the two
  synthetic fixture blobs in this row's own failure message is the same
  narrowly-scoped exception the byte-identity and round-trip rows already take,
  and it must not be read as widening the rule.

---

## Out of scope

- `docs/knowledge/features/e2e-realclaude.md` carries **two numeral spellings of
  the count** that the word sweep cannot see, and one of them additionally asserts
  that `initControlFixtureRecord` gains no field — which this slice falsifies.
  That file belongs to the documentation phase. **Do not edit it here.**
- No knowledge-base doc is a deliverable of this slice.
- #1729's deny-scan and its byte-identity criterion; #1715's per-arm loop.
- Any production file. This slice touches only `*_test.go` under
  `internal/e2e/realclaude`.

---

## Size check

Re-applied to this written spec, not to the sketch:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created/modified | ≤ 3 | **0** — every file is `*_test.go` |
| Total written work | ≤ 400 | **≈ 150** (record file ≈ 65 — mostly doc-comment prose the security pass added, writer file ≈ 60 incl. doc, probe file ≈ 20, redaction file ≈ 2) |
| New exported types or interfaces | ≤ 5 | **0** |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** — every `initControlFixtureRecord` construction is a keyed literal and compiles unchanged |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **0** |

Within `s` on every line.

**File-overlap check (§ 1.5):** `git fetch origin --prune` then a sweep of every
`origin/feature/<n>` branch's diff against `origin/main` for the five files this
spec touches returned no overlap. No `blockedBy` set.

---

## Open questions

1. **`dropcapSubstitution`'s three fields have no per-field coverage anywhere in
   the package, and this slice raises what that costs.** The record's properties
   count the record's own fields, so `Class`, `Replacement` and `Count` are
   invisible to all of them — the same gap #1723 closed for
   `initControlResultTrailer` with a second listing. Nothing asserts
   `reflect.TypeOf(dropcapSubstitution{}).NumField()` either, so a field added to
   that type reddens nothing anywhere.

   Before this slice that was a coverage gap. After it, the type is marshalled
   into a committed artifact and is exempt from `redactInitControlRecord` (§ 2),
   so a value-bearing field added by the dropcap family would ride into git
   unredacted with no test reporting it. It is still **not** exploitable as
   designed — every field of that type today is harness-minted — and #1729's
   deny-scan is the fail-closed net that catches an unpredicted value in the
   written bytes, which is exactly this shape. Flagged rather than fixed here: the
   type belongs to the dropcap family, a listing for it in this family's file is
   the wrong home, and no such field has ever been added. A `NumField` pin in
   `dropped_line_capture_test.go` is the cheap deterministic guard if anyone wants
   one; it is a follow-up ticket, not this slice.
2. **Field position.** This spec puts `Redaction` last, which reads as "the last
   thing the capture learns" and keeps the listing's declaration order trivially
   correct. If #1729's field lands first and a grouping emerges that reads better
   with the two adjacent, that is #1729's call to make, not a reason to move this
   one pre-emptively.

---

## Security review

**Verdict:** PASS (first pass was FAIL on two MUST FIX items; both were addressed
inline and the checklist re-run from the top over the revised spec.)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed.** The boundary is claude's
  stdout/stderr → the record → `redactInitControlRecord` → `writeInitControlFixture`
  → a `testdata/` artifact committed to git. The new field enters on the *trusted*
  side and is deliberately exempt from the pass. The first draft justified the
  exemption but never stated the hazard the field itself creates: a reader will
  take `"redaction": []` as "this artifact is clean", and it does not mean that.
  `dropcapRedactor.add` returns early on an empty value, so a class armed with
  `""` — or with a wrong or transposed path — installs no rule, matches nothing,
  and the census then honestly reports `[]` while the file still carries the real
  path under a class the table never armed. § Design now requires the record's doc
  to say the census is an audit trail over the redactor and not a clean bill of
  health over the artifact, and to name #1729's deny-scan as the thing that makes
  the second claim.
- **[Trust boundaries] MUST FIX — addressed.** This slice promotes
  `dropcapSubstitution` from a `t.Logf`-only shape to a **committed** one for this
  family, while § 2 exempts it from redaction. It is safe today — `Class` comes
  from the four `dropcapClass*` constants, `Replacement` from the `$`-prefixed
  literals in `newInitControlRedactor`'s `addPath` calls, `Count` is an int, and
  none is derived from the environment or from child output. It stops being safe
  if that type, which belongs to the *dropcap* family, gains a field carrying a
  matched value or a path — and nothing in the package reddens when it does. The
  spec now states this as a doc obligation and carries it in § Open questions with
  #1729's deny-scan named as the fail-closed net. Not exploitable as designed, so
  the residual is SHOULD FIX rather than a gate.
- **[Tokens, secrets, credentials] No findings.** Nothing is generated, stored,
  rotated or compared. The credential guard on this path is `initControlScrubbed`,
  which fatals and writes nothing when a `CLAUDE_CODE_OAUTH_TOKEN` or
  `ANTHROPIC_API_KEY` value appears in the child's stderr. The census cannot carry
  a credential: the redaction table arms **path** classes only, and the census
  reports class identifiers and placeholders, never a matched value. A credential
  reaching a field other than stderr is outside this slice and is #1729's subject.
- **[File operations] No findings.** The new row writes only into `t.TempDir()`,
  through `writeInitControlFixture`, which already uses temp-file-plus-rename.
  Mode `0644` is correct — the artifact is a public committed fixture, not a
  secret. No path component is caller-controlled: the directory is a tempdir and
  the filename is minted by `initControlArmFixtureName` from the fixture's own
  synthetic literals. The committed `testdata/` cannot be clobbered, twice over —
  the target is a tempdir, and `initControlFullRecord`'s `2.1.220-FIXTURE` version
  mints a different name from the committed artifact. The two-separate-tempdirs
  requirement in § Testing strategy is also what stops the two writes racing on
  one path.
- **[Subprocess / external command execution] No findings — not applicable by
  design.** This slice spawns nothing and changes no argv. The new row is offline
  by construction, enforced by `finOfflineExecBans`'s entry for
  `initialize_control_writer_test.go` and run by `TestFinOfflineFilesReachNoExecHelper`.
- **[Cryptographic primitives] No findings — not applicable.** No randomness, no
  hashing, no comparison against a secret. One adjacent property worth recording:
  `dropcapRedactor.substitutions` sorts by class, so the census is deterministic
  and the committed bytes are stable across runs despite Go's randomized map
  iteration.
- **[Network & I/O] SHOULD FIX — addressed.** The record's doc says nothing in
  that file caps anything, so a reader may ask whether the census needs a bound
  like `capFixtureCapture`. It does not, and the reason is structural rather than
  a judgement call: the slice's length is bounded by the number of armed classes,
  at most four, and `Count` is an int — it cannot grow with child output. § Design
  now states this so no cap gets invented and no reviewer asks for one.
- **[Error messages, logs, telemetry] SHOULD FIX — addressed.** The fill site's
  `t.Logf("…%+v", …)` over the census was previously justified as log-safe. The
  same values now also reach a committed file, so § Design requires the rewritten
  comment to keep the *reason* ("class names, replacements and counts only, never
  a value") rather than dropping it as stale prose — it is the only statement of
  why that `%+v` is permitted where the record's own is forbidden. The new test's
  printing of both record blobs on failure is the same narrowly-scoped exception
  the byte-identity and round-trip rows already take, over synthetic literals only.
- **[Concurrency] SHOULD FIX — addressed.** `dropcapRedactor`'s counters are
  unlocked and accumulate across calls. `runInitControlChild`'s doc already
  requires #1715 to mint a fresh redactor per arm; this slice raises what
  violating it costs, from one arm's counts appearing in another arm's log line to
  a **false audit trail in a committed artifact**. § Concurrency model now says so
  and forbids hoisting the construction out of the per-run path. No new goroutine,
  no new lock, no shared mutable state: the new row takes two shallow copies of
  one record and mutates only its own copies' new field.
- **[Threat model alignment] OUT OF SCOPE, named.** No relay surface, so
  `docs/protocol-mobile.md` § Security model does not apply. The applicable model
  is this package's own § Redaction framing — redaction guards values the harness
  knows it produced; the deny-scan is the fail-closed net for values it did not.
  This slice adds neither. It adds the audit trail over the first and must not be
  read as making the second's claim; the residual belongs to **#1729**.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-24
