# #1950 — the AskUserQuestion shape assertion's multi-select key presence check

One file changes: `internal/e2e/realclaude/ask_user_question_shape_test.go`. No
production file, no new file, no `finOfflineExecBans` edit — that table is keyed by
filename and #1951's entry already covers this one. Everything settles offline: no
claude binary, no credentials, no capture file, no directory, no child process.

The slice adds **one** check, **one** name constant, **one** listing entry, **one**
table row, and the corrections AC 4 requires to the sentences those additions falsify.
It is the smallest child of this family, and almost all of its written lines are the
corrections rather than the check.

## Files to read first

This is the turn-1 data load; the design below assumes you have it.

- `internal/e2e/realclaude/ask_user_question_shape_test.go` — **read the whole file
  before the first edit.** It is the only file you change and every one of AC 4's six
  correction sites is a doc comment in it. Specifically: the file header (its opening
  paragraph, its `# The limit of this file` section, and its three-counts paragraph),
  `askQuestionQuestion`, `askQuestionCheckNames`, `askQuestionShapeFindings`,
  `askQuestionShapeRecord`, `requireAskQuestionShape`, and
  `TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput` —
  including the shared comment sitting on its first content row, which is correction
  site 6.
- `internal/e2e/realclaude/ask_user_question_record_test.go` → `askQuestionFixtureInput`
  — the positive control's input. Confirm for yourself that it already carries
  `"multiSelect":false`, because that is what satisfies AC 1's second half with **no new
  row**. **Read its doc comment, not just the literal**: the two constraints it states
  (keys unsorted at two levels; no `<`, `>` or `&`) are why you must not reorder or
  "tidy" it, and the multi-select key sits third in that unsorted order. Also
  `askQuestionFullRecord` and `askQuestionFixtureRecord` — the four-field record, which
  gains no fifth field here.
- `internal/e2e/realclaude/ask_user_question_writer_test.go` → `askQuestionPlantedInput`,
  `askQuestionPlantedPath`, `askQuestionPlantedKeyPrefix` — read them so you recognise
  them and leave them alone. `askQuestionPlantedInput` is the package's only other
  one-option `AskUserQuestion` input and it *also* carries `"multiSelect":false`, which
  makes it look even more like the right base for this slice's fixture than it did for
  #1952's. It is not. See § Security review, [Tokens].
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans`, the
  `"ask_user_question_shape_test.go"` entry — read it to confirm you need **no** edit
  here, and to read the one sentence in it that constrains this slice: "The only
  in-package helper it calls is `askQuestionFullRecord`, which is pure literals." Your
  new fixture keeps that true by being a flat literal.
- `docs/knowledge/features/e2e-realclaude-ask-user-question-shape-test-go.md` — #1951's
  and #1952's folded lessons. Three bind here: exact equality turns a per-check mutation
  matrix into something a reviewer checks by inspection; a collided-constant mutant
  reddens the vacuity control alone; and **batch-width independence is unpinned by
  construction** — every row carries exactly one question, so nothing in the table would
  catch a mutant that looped over `in.Questions`. That last one is named at #1950 by
  name in that document. This slice does not close it and must not pretend to.
- `docs/specs/architecture/1952-ask-user-question-shape-content-checks.md` → its
  "The five negative fixtures" and "The mutation enumeration AC 4 asks you to grow"
  sections — the shape your one new row and your one new enumeration bullet copy.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — every comment you add cites a
  symbol, never a line. `make cite-guard` is diff-scoped and fails on any `//` citation
  that resolves to a declaration, at any depth, ranges included.

## Context

`askQuestionShapeFindings` reports eight names today and says nothing about the
multi-select key. A claude release that dropped the key entirely would pass the
assertion silently, and #1938 (the live run) and #1939 (the offline reader) are both
wired blocked-by *this* ticket rather than #1951 or #1952 precisely because an assertion
missing this check is the blind spot they would trust.

**Presence and truth are different checks, and only one of them survives a Go `bool`.**
A field decoded into `bool` reads `false` whether claude sent `false` or sent nothing at
all. #1951 typed `askQuestionQuestion`'s multi-select field `json.RawMessage` and not
`bool` for exactly this slice, and said so at the declaration: an absent key stays `nil`,
a present `false` is the four bytes `false`. **That decision is consumed here, not
retaken.** Nothing about the decode target changes — see § Design, "What must not
change".

Two things this slice inherits pre-paid, so it writes less than its ticket number
suggests:

- **The positive control already carries `"multiSelect":false`**, as do all five of
  #1952's content rows. AC 1's second half ("a record whose key is set to `false`
  passes") is therefore satisfied by rows that already exist, and no existing row
  changes. #1951 and #1952 paid this deliberately; the design below spends it.
- **The check-name distinctness control already covers whatever is added to
  `askQuestionCheckNames`.** AC 3's "joins the reported-names listing so the existing
  distinctness control covers it" is one line of work, not a new subtest.

**No ADR is warranted.** The one decision with reach past this ticket — presence rather
than truth, and `json.RawMessage` as the mechanism that keeps the two distinguishable —
was made and argued by #1951 at `askQuestionQuestion`, and this slice only stops it
being a forward reference. It is a test-harness shape, not a system-design commitment.

**The #1942 trap, restated so it does not happen.** Thirteen shipped comments in this
package name #1942. Nine are in `ask_user_question_writer_test.go` (seven) and
`ask_user_question_record_test.go` (two), and they misdescribe it as a live-capture
slice. They are **not yours to correct**: #1938 owns that correction, this package's
convention for a forward reference. The other four are in
`ask_user_question_shape_test.go` itself, under its `# This file is #1942's offline
successor and execs nothing` heading; those four are correct about this file's own
lineage and stay untouched. A `grep -rn '#1942'` while editing surfaces all thirteen —
leave all thirteen alone. **One thing you will notice and must not chase:** that heading's
own sentence says "eight describe it as a live-capture slice" where the ticket says nine
are wrong. That one-count discrepancy lives inside a sentence #1938 rewrites wholesale.
It is not falsified by this slice and it is not yours.

## Design

### The three counts, which the header must keep reconciled

Fix these first; everything else is easier once they are settled. After this slice lands
the file carries:

| | Before | After | What it is |
|---|---|---|---|
| Shape checks | 7 | **8** | #1951's two, #1952's five, this slice's one |
| Reported names | 8 | **9** | the eight plus `tool_input_decodes`, which the header already states is not a shape check "though its negative row is additional" |
| Table rows | 9 | **10** | the nine negatives plus the positive control |

AC 4 names all three explicitly. The sole-redness property is over the whole table, the
guard's row included — not over any one slice's rows.

**Nine reported names is nine of the ten reject branches the pipeline's sizing boundary
allows a single ticket.** Nothing to do here, but #1938 and #1939 should not assume a
tenth check fits alongside their own work.

### The one check

**Name and slug**, extending the existing eight:

```go
askQuestionCheckMultiSelectPresent = "multi_select_present"
```

`_present` and not `_nonempty`, for the reason the file already gives for `option_count`
carrying no `_nonempty` suffix: this is a key-presence test, not an emptiness test over a
value. It is singular because it reports on one key of one question, unlike the two
plural option names. At 33 characters it is shorter than
`askQuestionCheckOptionDescriptionsNonEmpty`, so `gofmt` re-aligns nothing.

**The check itself** is a length test over the raw field:

```go
if len(first.MultiSelect) == 0 {
    findings = append(findings, askQuestionCheckMultiSelectPresent)
}
```

An absent key leaves `json.RawMessage` nil, so `len` is 0. A present value is at least
one byte — the JSON scanner skips leading whitespace before handing the token over, so
there is no "present but empty" raw value to worry about.

**Append it last**, in all four places: the constant block, `askQuestionCheckNames`, the
emit site in `askQuestionShapeFindings` (after the two option appends, before
`return findings`), and the table. Appending rather than inserting in "semantic" order is
deliberate and worth one sentence in the code:

- It keeps #1952's five content rows **contiguous**, so the shared comment sitting on the
  first of them — which says "the five content rows below" — stays true without being
  rewritten around a row wedged into the middle of the group.
- Emit order is documentary here and pins nothing: every row trips exactly one check, so
  no row's `want` has two names whose order could matter. `askQuestionCheckNames`'s doc
  says "in emit order", and appending in all four places is what keeps that claim true.

The new check sits **after** both early returns, so an undecodable input and an empty
batch still return before it ever runs. That is what keeps those two rows at one finding
each with no edit — see the trap below.

### What must not change

Four things a well-meaning edit would break, each already argued in the file:

- **`askQuestionQuestion`'s multi-select field stays `json.RawMessage`.** Retyping it
  `bool` collapses absent into `false` and makes this check unwriteable. The compiler is
  the backstop — `len()` does not build against a `bool` — but the *reason* lives in that
  field's doc comment, which is correction site 4.
- **No fifth field on `askQuestionQuestion` and none on `askQuestionFixtureRecord`.** A
  separate presence field duplicates a distinction the raw field already carries, and the
  record's four fields are pinned by a hand-written name literal in
  `ask_user_question_record_test.go`. `askQuestionShapeRecord`'s doc says both.
- **`askQuestionFixtureInput`'s key order is untouched.** Its keys run header, question,
  multiSelect, options, which is not sorted order, and #1943's wire-order vacuity control
  reddens if you alphabetise them. The multi-select key sitting third is the thing this
  slice reads about most and the thing it must least touch.
- **`requireAskQuestionShape` is untouched.** Its `t.Fatalf` prints the count and the
  names and is correct at nine names as it was at eight. Its `#1951:` prefix stays; this
  slice mints **no new failure message** and therefore no string carrying `#1950:`. If
  you find yourself adding a subtest to have somewhere to put one, stop — the ticket
  number belongs in the comments.

Do not rename
`TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput` either: the
name is still accurate and the header's `-run 'TestAskQuestionShape|…'` line depends on
the prefix.

### The one new fixture

A **flat one-line literal in its own table row**, matching the five content rows. Not a
builder, not a new helper: five-plus-one independent statements are what a reviewer diffs
against the control, and a new in-file helper would make the ban entry's "the only
in-package helper it calls is `askQuestionFullRecord`" sentence stale — #1952 already
recorded that.

It is the content rows' base shape with **exactly one difference**: the `multiSelect` key
is gone. Everything else stays — one question, keys otherwise in
`askQuestionFixtureInput`'s order (header, question, options), two options each with a
non-empty label and description, `-FIXTURE` markers throughout for #1701's reason.

| Row | The one difference | `want` |
|---|---|---|
| an absent multi-select key | no `"multiSelect"` key at all; header, question text and both options intact | `askQuestionCheckMultiSelectPresent` |

Its comment carries three things a future reader cannot reconstruct:

1. **Only the key is missing.** A fixture that also blanked a header or dropped an option
   would be an over-determined reject row pinning neither reason, would return two
   findings, and would fail its own exact match on unmutated code.
2. **The key is absent, not `null` and not `false`.** `"multiSelect":false` is what the
   positive control carries and must pass; `"multiSelect":null` decodes to the four bytes
   `null` and is therefore **present** by this check, which is correct — see the stated
   limit below.
3. **It is a local literal, deliberately not `askQuestionPlantedInput`.** See § Security
   review, [Tokens].

### The trap: two existing rows carry no multi-select key and must not gain one

The ticket says every existing row already carries `"multiSelect":false`. That is true of
the seven rows that reach the new check, and **not** of the other two:

- the empty-batch row is `{"questions":[]}` — no question object exists to carry a key
- the undecodable row is `{"questions":` — it does not parse at all

Both return early, before `first` is ever taken, so the new check never runs on them and
both stay at exactly one finding. **Do not add a multi-select key to either.** Doing so
would change nothing about the result and would falsify the comment on the empty-batch
row, which explains that an *explicitly empty* batch is the realistic malformed capture.

### The stated limit this slice adds

The file's `# The limit of this file` section keeps a job after AC 4's correction. Three
limits now belong in it:

- **Only the FIRST question's key is checked**, deliberately — `askQuestionShapeFindings`
  already argues why (checking each would make the finding count depend on batch width).
  Point at that argument rather than restating it.
- **Presence, not truth.** The check does not judge the value. `false` passes, `true`
  passes, and an explicit `null` passes because the key is present. Judging the value is
  not this assertion's job: the tool's own semantics decide what a `multiSelect` value
  means, and a shape assertion that rejected `false` would redden on a perfectly
  well-formed capture.
- **Nesting versus flattening is still unmeasured** — unchanged, and #1938's capture
  settles it.

### AC 4 — the six sentences this slice falsifies

Each is a doc comment. Correct in place; do not restructure the file. **Correct them, do
not merely delete them** — each states a real property whose value changed.

1. **The header's opening paragraph** — "#1951 and #1952 — … and SEVEN shape checks with
   a negative row apiece." Name the three slices in landing order and say **eight**.
2. **The header's `# The limit of this file` section** — "SEVEN SHAPE CHECKS SHIP HERE:
   … The multiSelect key is the slice after this one, and nothing here asserts on it."
   Both halves are false. Eight shape checks ship here, and the multi-select key's
   presence is one of them. Replace the forward-reference sentence with the three limits
   listed above.
3. **The header's three-counts paragraph** — SEVEN / EIGHT / NINE becomes EIGHT / NINE /
   TEN, and "not over the five rows #1952 added" becomes a statement about the whole
   table rather than about one slice's rows.
4. **`askQuestionQuestion`'s doc** — two paragraphs, both stale. "MULTISELECT ALONE IS
   DECLARED-AND-UNREAD, deliberately rather than by oversight, because the multiSelect
   slice decodes this same object…": every field is read now, and the argument that the
   target was declared whole rather than grown one field per slice has been *vindicated*
   rather than refuted — say that. Then "MULTISELECT IS `json.RawMessage` AND NOT A
   `bool` … Nothing here asserts on it; the field exists so that distinction survives to
   the slice that reads it": the type decision stays and its reason stays, but it is no
   longer a promise to a future slice. State it as the live constraint it now is — the
   presence check is a length test over the raw bytes, so retyping the field `bool` does
   not compile, and that is the strongest form this rule can take.
5. **`askQuestionCheckNames`'s doc** — "#1952 appended its five names here and inherited
   that control for free, so it now covers all EIGHT rather than the original three."
   Nine, and this slice inherits the same control the same way, which is AC 3.
6. **The shared comment on the first content row** — "EVERY ONE OF THEM CARRIES
   `"multiSelect":false`. No check here reads it and it is not key-order decoration: the
   multiSelect slice adds a check over that key, and a row omitting it would trip that
   check too, turning all five of these rows over-determined and forcing that slice to
   rewrite them." The forward reference has arrived. A check here reads it now, carrying
   the key is what keeps these five rows at one finding each, and the prediction held —
   no row was rewritten. Keep the "five content rows below" framing accurate by
   appending this slice's row after the group rather than inside it.

Two sentences that look stale and are **not**, so you do not spend churn on them:

- `askQuestionShapeFindings`'s "ONLY THE FIRST QUESTION IS CHECKED" already covers the
  new check and needs no edit; AC 1's "the first question, not each" is satisfied by
  pointing at it.
- `askQuestionShapeFindings`'s "IT RETURNS `nil`, NEVER `make([]string, 0, 3)`" names a
  mutant's shape, not a count of checks. Not falsified. Leave it.

## Concurrency model

No goroutines, no channels, no shared mutable state — unchanged from #1951 and #1952.
`askQuestionShapeFindings` stays pure over its parameter; the new check reads one field
of a function-local value and appends a package-level constant. The new row mints its own
record through `askQuestionShapeRecord`, which calls `askQuestionFullRecord` and gets a
fresh pointer per call, so the `t.Parallel()` subtests share nothing. No package-level
state is added; the fixture is an inline untyped string constant.

`t.Fatalf` requires the test goroutine, so `requireAskQuestionShape` must not be called
from #1938's stdout reader — already documented at the wrapper and unchanged here.

## Error handling

No error return anywhere in this slice, and no new failure mode reaches the caller. The
table of what produces what, extending #1952's by one row:

| Failure | Handled by | Result |
|---|---|---|
| `rec.ToolInput` does not parse | the decode guard | one finding, everything downstream **skipped** |
| the batch carries no question | the batch-length check | one finding, the six per-question checks **skipped** |
| the first question's text or header is empty | its own check | one finding each |
| the first question carries fewer than two options | the option-count check | one finding |
| any option's label or description is empty | the option loop | **one** finding per class, whatever the batch width |
| the first question carries no multi-select key | **the new check** | one finding |

Arbitrary hostile bytes are still handled by `json.Unmarshal` returning an error rather
than panicking, and the two early returns keep the function **total** over any record: no
index, no loop and no field read runs on a batch that is absent, empty, or undecodable.
There is still no byte cap on `rec.ToolInput` and none belongs here — **the cap is
#1938's**, at the read that fills the field.

## Testing strategy

No new test function, no new subtest, no new failure message. One row joins the existing
table; the vacuity control over `askQuestionCheckNames` picks up the new name for free,
which is AC 3's whole point.

Comparison stays `reflect.DeepEqual(got, tt.want)` — exact equality, never containment —
and the existing `t.Errorf` printing both slices and both lengths is unchanged.

### The mutation enumeration AC 4 asks you to grow

The uniform bullet absorbs the new check with two number changes: **delete any one
check's append and that check's own row compares an empty result against a one-name
`want` and reddens, while the other nine rows return exactly their own findings and stay
green.** The positive control stays green under all nine, because deleting a check can
only remove findings.

Two mutants specific to this slice are worth naming individually, and one of them is
honest about not being a sole red:

- **Weaken presence into truth** — decode the field and require it to be `true`, or
  compare the raw bytes against `true`. The absent-key row still reports its own name and
  stays green; the **positive control** reddens, and so do all five content rows, each
  returning two findings. Six rows red, not one. State it as what it is: this mutant is
  caught loudly rather than precisely, and the row that names it is the positive control,
  because a fixture carrying `"multiSelect":false` and expecting **no** finding is the
  only thing that can distinguish presence from truth. That is AC 1's second half and it
  needs no row of its own.
- **Retype the field `bool`** — a **build failure**, not a red row: `len()` does not
  compile against a `bool`. Worth stating in the enumeration for the same reason #1952
  states the batch-length panic there: it is a red that does not look like a table row
  failing, and a reader who expects every mutant to surface as one row would misread it.

The four structural mutants #1951 and #1952 enumerated stay exactly as they are. This
slice adds no fixture that changes any of them.

Over-determination remains unshippable for #1951's reason: a fixture tripping two checks
returns two findings and fails its exact match on unmutated code, so such a row cannot
reach the branch. The one place it could have entered this slice is the new fixture, and
the design pins it to dropping the key and nothing else.

**Batch-width independence stays unpinned**, as the package overview records under #1950
by name. Every row carries one question, so a mutant looping over `in.Questions` and
appending per question returns identical findings on all ten rows and stays green. Do not
add a multi-question row to close it: a tenth negative row contradicts the ten-row count
AC 4 pins, and a multi-question fixture belongs with whichever slice first has a reason to
carry one. Leave the property named and unpinned.

### Gate

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestAskQuestionShape|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Every one must report PASS — not SKIP, not "no tests to run" — on a machine with no
claude and no credentials, which is AC 4's first clause. **Read the count of tests that
executed, never the exit code.** `make check` never compiles this package, and the suite
exits 0 both on a build failure and on a full credentials skip; that `-run` invocation
compiling and running is your build proof, and `make preship` is the project's gate for
it. Run `make check` as well for the rest of the tree, and `make cite-guard` for the
comments you add and edit — AC 4 makes you rewrite six comment blocks, which is six
diff-scoped chances to write a line citation.

No `-overlay` run is required for AC 2's sole-redness. The argument holds by construction
from exact equality, which is #1951's measured lesson and the reason the assertion is
shaped this way.

## Line budget

**~120–160 total, one file, and the boundary is 400.** Derived from #1952's own commit
(`5e5ecdfe`: 199 insertions and 25 deletions for *five* checks, five rows and six
corrections) rather than by eye. Rough allocation: the constant, the listing entry and the
check with their prose ~30; the fixture literal, its row and its three-part justification
~25; AC 4's six corrections ~60, which is the bulk of this slice and where two of the six
are full paragraph rewrites rather than number swaps; the grown mutation enumeration ~20.

This family's comment density does not scale down with the size of the thing under test —
#1943 overran its spec's budget by 42% on that alone. If you overrun, cut correction sites
1, 3 and 5 to the minimum true sentence; do **not** cut the reasoning at correction site 4
(`askQuestionQuestion`'s type argument) or at the new row, which are the two places a
future reader cannot reconstruct the why.

## Open questions

- **Nesting versus flattening.** Whether claude nests options under each question or
  flattens them across the batch is still unmeasured, and #1938's capture settles it.
  Write the check against the documented shape; do not add a second accepted form to
  absorb the other spelling in advance, because a check accepting both cannot redden on
  either.
- **Whether a present-but-`null` multi-select key should pass.** This slice says yes — the
  key is present, and judging the value is not a shape assertion's job. If #1938's capture
  shows claude emitting `null` for a single-select question, that is a finding for #1939
  to act on, not a reason to tighten this check retroactively. Named here so the decision
  is visible rather than inferred from the `len` test.
- **The `newDropcapScanner` gap in the seventeen banned names.** Inherited from #1951's
  security review unchanged, since this slice does not touch the `finOfflineExecBans`
  entry. Closing it is table-wide work.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, and the boundary is unmoved. `askQuestionShapeFindings`
  remains the only function that decodes `rec.ToolInput`, and it is the single explicit
  crossing from claude-authored bytes to trusted in-process values. The new check is the
  first that reads the raw, *undecoded* bytes of a claude-supplied field rather than a
  decoded Go string — but it reads only their **length**, and a length is not the bytes.
  In this slice every record is a synthetic literal so nothing untrusted actually crosses;
  the boundary that matters is the future one, where #1938 calls
  `requireAskQuestionShape` over a live child's bytes.
- **[Tokens, secrets, credentials]** No open finding — one hazard, closed in the design
  rather than left to the developer, and **sharper here than it was for #1952**. The new
  fixture must be a local literal and must not call `askQuestionPlantedInput`. That helper
  is the package's only other one-option `AskUserQuestion` input, and it additionally
  carries `"multiSelect":false` in its own literal, which makes it look like a
  near-complete base for a multi-select fixture. Its signature is
  `askQuestionPlantedInput(plant string)` and every call site hands it
  `askQuestionPlantedKeyPrefix` (an `sk-ant-`-prefixed synthetic credential) or
  `askQuestionPlantedPath` (a synthetic operator home path). Calling it here would put a
  credential-shaped literal inside a record `requireAskQuestionShape` is designed to be
  handed, in a file whose stated defining property is carrying none, and would couple this
  row to `ask_user_question_writer_test.go`'s deliberately narrower fourteen-name ban
  entry. Had the design not settled it this would have been a MUST FIX. Inherited
  unchanged from #1951: no environment read anywhere in the file, enforced by the
  `os.Getenv` / `os.Environ` / `os.LookupEnv` names in its `finOfflineExecBans` entry.
- **[File operations]** No finding. This slice adds no filesystem call and no path
  construction; the fixture is an in-source literal. The `packageDir` group,
  `filepath.Glob` and the four `os` names in the file's existing ban entry still enforce
  it, and the entry needs no edit because `TestFinOfflineFilesReachNoExecHelper` keys on
  the filename. The hazard peaks in exactly this slice's shape and is worth restating: "a
  real capture would settle whether claude actually sends the key" is the natural next
  thought when writing a presence check, `go test` runs in the package source directory,
  so a relative `os.ReadFile("testdata/…")` reaches the committed captures while naming no
  wrapper at all. It is banned, and it would be red on arrival anyway — no committed
  `permission_protocol_*` capture holds an `AskUserQuestion` `tool_use` block, only the
  tool's name in the `system`/`init` tools array.
- **[Subprocess / external command execution]** No finding. Nothing execs, and this slice
  adds no call to anything that could. The sharper hazard the first five banned names
  close is a **skip**, not an exec: `resolveClaudeBin` and `WithWorktreeAuthenticated`
  skip inside the test body after `=== RUN` prints, and a skip exits 0, which reads as a
  pass. AC 4's "does not skip" clause is that property.
- **[Cryptographic primitives]** Not applicable by design decision rather than omission.
  No randomness is generated. The new check is a length comparison against zero, not a
  comparison of an attacker-influenced value against a secret, so constant-time comparison
  has no site here. `reflect.DeepEqual` compares returned findings against fixed in-file
  constants.
- **[Network & I/O]** No finding, and the property the category protects is *strengthened*
  by the check's shape. `len(first.MultiSelect)` reads a length and appends a fixed
  constant, so the findings slice grows by at most one entry regardless of how many bytes
  claude put in that field — a ten-megabyte multi-select value produces the same
  nine-name ceiling as a four-byte one. No loop, no allocation per byte:
  `json.Unmarshal` already materialised the field before the check runs. There is still no
  byte cap on `rec.ToolInput` and none belongs here; **the cap is #1938's**, at the read
  that fills the field, named again so #1938 does not assume this slice capped it.
- **[Error messages, logs, telemetry]** No finding, and this is the category the ticket
  correctly identifies as most at risk. The multi-select field is claude-supplied raw
  bytes sitting exactly where a diagnostic wants them — "the key was `%s`" is one edit
  away and would be the first appearance of child-derived bytes in a finding. The design
  forbids it structurally: the check appends `askQuestionCheckMultiSelectPresent` and
  nothing else. **Not the raw bytes, not an excerpt, not the length, not the decode
  error.** Even the length is claude-derived and must not reach a finding. #1938 calls
  this over a live child's bytes and the message reaches a salvaged run log.
  `requireAskQuestionShape` is untouched and still prints only the count, the finding
  names, `ClaudeVersionSlug` and `ToolName`; `%v`, `%+v` and `%#v` on the record remain
  banned because `%+v` prints `ToolInput`. The table's `t.Errorf` prints the returned
  findings and the row name only, and in this slice every value is synthetic.
- **[Concurrency]** No finding. No goroutines, no locks, no shared mutable state; the new
  check reads a field of a function-local value, the fixture is an inline constant, and
  the row's record is a fresh pointer from `askQuestionFullRecord()` by way of
  `askQuestionShapeRecord`. `t.Fatalf` requires the test goroutine, so
  `requireAskQuestionShape` must not be called from #1938's stdout reader — already
  documented at the wrapper and unchanged here.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model is relay-scoped
  and does not apply. The applicable model is the one `offline_exec_ban_test.go`'s header
  states for this package: the process environment carries `CLAUDE_CODE_OAUTH_TOKEN` and
  `ANTHROPIC_API_KEY`, agent run logs are salvaged by the pipeline, and `testdata/` holds
  committed captures a relative write would overwrite. All three stay addressed — no
  environment read (ban), no claude-derived value in any message (construction, reinforced
  above), no filesystem call at all (ban).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
