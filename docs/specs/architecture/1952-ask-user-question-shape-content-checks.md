# #1952 — the AskUserQuestion shape assertion's five per-question content checks

One file changes: `internal/e2e/realclaude/ask_user_question_shape_test.go`. No
production file, no new file, no `finOfflineExecBans` edit — that table is keyed by
filename and #1951's entry already covers this one. Everything settles offline: no
claude binary, no credentials, no capture file, no directory.

The slice adds five checks over the **first** question, five negative rows that fail
one check apiece, five appends to the check-name listing, and the corrections AC 4
requires to the sentences those additions falsify.

## Files to read first

This is the turn-1 data load; the design below assumes you have it.

- `internal/e2e/realclaude/ask_user_question_shape_test.go` — **read the whole file
  before the first edit.** It is the only file you change, and four of AC 4's six
  correction sites are doc comments in it. Specifically: the file header (its opening
  line, its `# The limit of this file` section, and its `# This file is #1942's
  offline successor` section), `askQuestionQuestion`, `askQuestionOption`,
  `askQuestionCheckNames`, `askQuestionShapeFindings`, `askQuestionShapeRecord`,
  `requireAskQuestionShape`, and
  `TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput`.
- `internal/e2e/realclaude/ask_user_question_record_test.go` → `askQuestionFixtureInput`
  — the positive control's input. Confirm for yourself that its one question already
  satisfies all five new checks: a non-empty `header`, non-empty `question`, two
  options, each with a non-empty `label` and `description`. **Read its doc comment, not
  just the literal**: the two constraints it states (keys unsorted at two levels; no
  `<`, `>` or `&`) are why you must not reorder or "tidy" it. Also
  `askQuestionFullRecord` and `askQuestionFixtureRecord` — the four-field record, which
  gains no fifth field here.
- `internal/e2e/realclaude/ask_user_question_writer_test.go` → `askQuestionPlantedInput`,
  `askQuestionPlantedPath`, `askQuestionPlantedKeyPrefix` — read them so you recognise
  them and leave them alone. `askQuestionPlantedInput` returns a one-option
  `AskUserQuestion` input and is the obvious thing to reach for for this slice's
  single-option row. Its signature takes a `plant string` and every call site hands it
  one of those two credential-shaped literals. See § Security review, [Tokens].
- `internal/e2e/realclaude/offline_exec_ban_test.go` → `finOfflineExecBans`, the
  `"ask_user_question_shape_test.go"` entry — read it to confirm you need **no** edit
  here, and to read the one sentence in it that constrains this slice: "The only
  in-package helper it calls is `askQuestionFullRecord`, which is pure literals." Your
  new fixtures keep that true.
- `docs/knowledge/features/e2e-realclaude-ask-user-question-shape-test-go.md` — #1951's
  two lessons. Both bind: exact equality turns a per-check mutation matrix into
  something a reviewer checks by inspection, and a collided-constant mutant reddens the
  vacuity control alone.
- `docs/knowledge/features/e2e-realclaude-ask-user-question-record-test-go.md` — #1943's
  measured lesson that this family's comment density does not scale down with the size
  of the thing under test. It is why the line budget below is what it is.
- `docs/specs/architecture/1951-ask-user-question-shape-assertion-envelope.md` → its
  "Why AC 2's sole-redness holds by construction" section — the four bullets AC 4 makes
  you grow.
- `CODING-STYLE.md` § "Comments — Citing Other Code" — every comment you add cites a
  symbol, never a line. `make cite-guard` is diff-scoped and fails on any `//` citation
  that resolves to a declaration, at any depth, ranges included.

## Context

`askQuestionShapeFindings` currently reports three names — the tool name, the decode
guard, and the batch length — and none of them says anything about what is *in* a
question. A capture holding one entirely empty question passes it today. #1938 (the
live run) and #1939 (the offline reader) will both read that silence as evidence, and a
shape assertion that cannot fail is worse than none. This slice closes the gap for the
first question's contents; the multi-select key is #1950's and stays out.

Two decisions bind the shape of the work and neither is negotiable downstream:

- **The five checks read claude-supplied strings, and the return value must still carry
  none of them.** #1951 made "findings carry only fixed constants" the design's security
  property, structurally rather than by convention. This slice is the first one where
  the checks actually *touch* attacker-influenced text, so it is the first one where
  that property can be broken. See § Security review, [Error messages].
- **Exact equality is what makes AC 2 hold by construction.** Deleting check K makes
  row K compare an empty result against a one-name `want` and redden, while every other
  row returns its own single finding and stays green. Keep
  `reflect.DeepEqual(got, tt.want)`; a containment assertion would need a nine-run
  overlay matrix to reach the same confidence, growing by one run per future check.

**No ADR is warranted.** The one decision with reach past this ticket — the option
checks scan every option but report one name each, so the findings slice's length is
independent of claude-supplied input — is argued at `askQuestionShapeFindings` and
inherited by #1950 and #1938 through that same function. It is a test-harness shape,
not a system-design commitment.

**The #1942 trap, stated so it does not happen.** Thirteen shipped comments in this
package name #1942, the closed ticket this family descends from. Nine are in
`ask_user_question_writer_test.go` and `ask_user_question_record_test.go`, and eight of
those nine call it a live-capture slice. They are wrong about this work — the live
capture is #1938 alone — and they are **not yours to correct**: #1938 corrects them
when it lands, this package's convention for a forward reference. The other four are in
`ask_user_question_shape_test.go` itself, under its `# This file is #1942's offline
successor and execs nothing` heading; those four are correct, describe this file's own
lineage, and stay. A `grep -rn '#1942'` run while editing will surface all thirteen.
Leave all thirteen alone.

## Design

### The three counts, which the header must keep reconciled

Everything in this slice is easier to get right if these are fixed first. After it
lands the file carries:

| | Count | What it is |
|---|---|---|
| Shape checks | **7** | #1951's two (tool name, batch non-empty) plus this slice's five |
| Reported names | **8** | the seven plus `tool_input_decodes`, which the header already states is not a shape check "though its negative row is additional" |
| Table rows | **9** | the eight negatives plus the positive control |

AC 2's mutation property is over the whole table, the decode guard's row included — not
over the five rows this slice adds.

### The five checks

All five read `in.Questions[0]` and no other element. The batch may carry several
questions in a real capture; checking only the first is what keeps the finding count
independent of batch width, the same reason the option checks report one name each.

Emit order — and therefore constant-declaration order and
`askQuestionCheckNames` order — is the order the file header already names:

1. the first question's `Question` is non-empty
2. its `Header` is non-empty
3. it carries **two or more** options
4. **every** option's `Label` is non-empty — one finding for the whole batch
5. **every** option's `Description` is non-empty — one finding for the whole batch

Suggested constants and slugs, extending the existing three:

```go
askQuestionCheckQuestionTextNonEmpty       = "question_text_nonempty"
askQuestionCheckHeaderNonEmpty             = "header_nonempty"
askQuestionCheckOptionCount                = "option_count"
askQuestionCheckOptionLabelsNonEmpty       = "option_labels_nonempty"
askQuestionCheckOptionDescriptionsNonEmpty = "option_descriptions_nonempty"
```

The `_nonempty` suffix matches `questions_nonempty`; `option_count` carries none because
it is a bound, not an emptiness test. The last two are **plural** deliberately — the
plural is the reader's cue that one finding covers the whole option batch. Expect
`gofmt` to re-align the three existing constants when the longer names land; that
re-alignment is not churn to avoid.

### Where the checks go in `askQuestionShapeFindings`, and why it is not negotiable

The batch-length check becomes an **early return**, matching the decode guard directly
above it:

```go
if len(in.Questions) == 0 {
    return append(findings, askQuestionCheckQuestionsNonEmpty)
}
first := in.Questions[0]
// the three scalar checks, then the option loop
```

**This is forced, not stylistic.** The tempting alternative — guard the index with
`var first askQuestionQuestion; if len(in.Questions) > 0 { first = in.Questions[0] }`
and let the checks run on the zero value — makes the existing empty-batch row report
**four** names on unmutated code (`questions_nonempty`, plus question text, header and
option count against a zero-valued question). That row would then be over-determined,
which AC 2 forbids, and it would destroy sole-redness for three of the five new checks
at once, since deleting any of them would redden that row as well as its own. The early
return is what keeps the empty-batch row at exactly one name.

The two remaining checks read every option through **one loop with two flags**, not by
appending inside the loop:

```go
var labelMissing, descriptionMissing bool
for _, opt := range first.Options {
    if opt.Label == "" { labelMissing = true }
    if opt.Description == "" { descriptionMissing = true }
}
```

Three properties come out of that shape and each is load-bearing:

- **One name each, whatever the batch width.** Appending inside the loop makes the
  findings slice — and therefore `requireAskQuestionShape`'s `t.Fatalf` message — grow
  with the number of claude-supplied options. A capture with ten thousand options would
  put ten thousand entries into a salvaged run log. See § Security review, [Network].
- **Emit order is fixed.** With the flags, label always precedes description. Appending
  inside the loop would emit `description, label` for an input whose first option lacks
  a description and whose second lacks a label, breaking every exact-equality row that
  trips both.
- **No index of an offending option is ever computed**, so there is no positional
  attacker-derived value in scope for a future edit to fold into a finding.

The `< 2` bound is written as a bound (`len(first.Options) < 2`), not as `== 0` or
`== 1`; the single-option row below is what pins it.

### Nothing else in the function changes

The tool-name check keeps reading `rec.ToolName` rather than the decoded input — that
is what keeps the undecodable row at exactly one finding. The decode guard keeps its
early return. The function keeps returning `nil` rather than a pre-allocated empty
slice: `reflect.DeepEqual(nil-slice, []string{})` is **false**, and a pre-allocated
return reddens the positive control with a message reading `[] != []`.

`requireAskQuestionShape` is **untouched**. Its `t.Fatalf` already prints the count and
the names and is correct at eight names as it is at three. Do not rename
`TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput` either —
the name is still accurate, and the header's `-run 'TestAskQuestionShape|…'` line
depends on the prefix.

This slice writes **no new failure message**, so no string in it carries a `#1952:`
prefix. The five new rows join the existing table and share its `t.Errorf`, whose
`#1951:` prefix names the assertion that wrote it and stays. If you find yourself
minting a subtest to have somewhere to put a `#1952:` string, stop — the ticket number
belongs in the comments.

### The five negative fixtures

Each is an inline one-line string literal in its own table row, matching the two
existing negative rows. **Not a builder helper**: five flat literals are five
independent statements a reviewer diffs against the control and against each other,
where a builder centralises the mistake; and a new in-file helper would make the ban
entry's "the only in-package helper it calls is `askQuestionFullRecord`" sentence stale.

All five share one base shape, and each degrades exactly one value from it:

- one question, keys in the order `header, question, multiSelect, options`
- each option's keys in the order `label, description`
- **`"multiSelect":false` present in all five.** No check here reads it, and it is not
  key-order decoration: #1950 adds a check over that key, and a fixture omitting it
  would trip #1950's check too, turning all five of these rows over-determined and
  forcing that slice to rewrite them. Carrying the key is what keeps this slice from
  painting the next one into a corner.
- string values carry `-FIXTURE` markers, #1701's discipline, as the existing
  wrong-tool-name row does

The key order is not asserted by anything here — #1943's wire-order control covers
`askQuestionFixtureInput` alone — but matching it is what lets a reader diff a row's
literal against the control and see the one difference.

The five rows, each with its `want` of exactly one name:

| Row | The one degraded value | `want` |
|---|---|---|
| empty question text | `"question":""`, header and both options intact | question text |
| empty header | `"header":""`, question text and both options intact | header |
| a single option | exactly **one** option, its label and description both non-empty | option count |
| empty option labels | **both** options' `"label":""`, both descriptions non-empty | option labels |
| an empty option description | the **second** option's `"description":""` only, both labels non-empty | option descriptions |

Four of those five choices need their reason in the row's comment:

- **The empty-question-text and empty-header rows are the pair that is easy to get
  wrong.** A literal blanking both trips two checks, is over-determined, fails its own
  exact match on unmutated code, and pins neither. Each row blanks one and leaves the
  other populated. Write the emptiness as a present-and-empty string rather than an
  omitted key, the convention the empty-batch row's comment already states.
- **The single-option row carries one option, not zero.** Zero options would also trip
  option-count alone (the label and description checks are vacuously satisfied over an
  empty loop), but one option is what pins the bound: mutate `< 2` to `< 1` and this row
  returns nothing and reddens alone. Write a **local** literal — do not reach for
  `askQuestionPlantedInput`, see § Security review, [Tokens].
- **The empty-label row blanks both labels.** It still fails exactly one check, so it
  satisfies AC 2, and it additionally pins "one name each": the append-inside-the-loop
  mutant returns two findings on this row and reddens alone. A single blank label leaves
  that mutant green.
- **The empty-description row blanks the second option's description only.** It pins
  that the loop scans past index 0: narrow the loop to `first.Options[:1]` and this row
  returns nothing and reddens alone. Pairing it with the both-blank label row above
  covers the loop's two failure shapes with two rows and no tenth row.

### AC 4 — the six sentences this slice falsifies

Each is a doc comment. Correct in place; do not restructure the file.

1. **The header's opening line** — currently attributes the file to #1951 and says it
   ships "the first TWO shape checks with a negative row apiece". It now covers both
   slices: seven shape checks, eight reported names, nine rows.
2. **The header's `# The limit of this file` section** — currently "EXACTLY TWO SHAPE
   CHECKS SHIP HERE", the five content checks assigned to #1952, and a paragraph
   reconciling #1952's "seven checks" with the decode guard. Rewrite it as the limit
   that now applies: seven shape checks ship here, the decode guard is still not one of
   them though its negative row is additional, and the three counts in the table above
   are what a reader should reconcile against. **The sentence assigning the multiSelect
   key to the following slice must survive this edit** — it is still true and #1950
   depends on it.
3. **`askQuestionQuestion`'s doc** — "ALL FOUR FIELDS ARE DECLARED NOW even though this
   slice reads only `len(Questions)` — Header, Question, MultiSelect and every option
   field are declared-and-unread here". Header, Question and both option fields are now
   read; `MultiSelect` alone remains declared-and-unread. The `MULTISELECT IS
   json.RawMessage AND NOT A bool` paragraph below it, including "Nothing here asserts
   on it", stays exactly as it is — still true, and #1950 depends on the field's type.
4. **`askQuestionOption`'s doc** — "#1952's five content checks read option labels and
   descriptions; leaving the element type opaque here would force that slice to redo
   this decode rather than extend it." A forward reference that has arrived: the checks
   in `askQuestionShapeFindings` read both fields now.
5. **`askQuestionCheckNames`'s doc** — "and for #1952, which appends five entries and
   inherits that control for free." Same: the five are appended and the distinctness
   control covers all eight.
6. **The test's mutation enumeration** — the four bullets in
   `TestAskQuestionShape_ReportsEachMissedCheckAndSkipsAfterAnUndecodableInput`'s doc.
   Grow it with the rows added here, per the list in § Testing strategy.

Two sentences that look stale and are **not**, so you do not spend churn on them:

- `askQuestionShapeFindings`'s "IT RETURNS `nil`, NEVER `make([]string, 0, 3)`" names a
  mutant's shape, not a count of checks. It is not falsified by this slice. Leave it.
- The four `#1942` comments under the header's offline-successor heading are correct
  about this file's lineage. Leave them.

## Concurrency model

No goroutines, no channels, no shared mutable state — unchanged from #1951.
`askQuestionShapeFindings` stays pure over its parameter; the two option-loop flags are
function-local. Each row's record is minted by its own `askQuestionFullRecord()` call,
which returns a fresh pointer per call, so the `t.Parallel()` subtests share nothing.
The five new rows inherit that; they add no package-level state, and their fixture
literals are untyped string constants inline in the table.

## Error handling

No error return anywhere in this slice, and no new failure mode reaches the caller. The
table of what produces what, extending #1951's:

| Failure | Handled by | Result |
|---|---|---|
| `rec.ToolInput` does not parse | the decode guard | one finding, everything downstream **skipped** |
| the batch carries no question | the batch-length check | one finding, the five content checks **skipped** |
| the first question's text or header is empty | its own check | one finding each |
| the first question carries fewer than two options | the option-count check | one finding |
| any option's label or description is empty | the option loop | **one** finding per class, whatever the batch width |

Arbitrary hostile bytes are still handled by `json.Unmarshal` returning an error rather
than panicking, and the two early returns are what make the function **total** over any
record: no index and no loop runs on a batch that is absent, empty, or undecodable.
There is still no byte cap and none is needed here — the bytes are in memory before the
check runs. **The cap belongs to #1938's reader**, at the point that fills the field.

## Testing strategy

No new test function, no new subtest. The five rows join the existing table; the
vacuity control over `askQuestionCheckNames` picks up the five new names for free,
which is AC 3's whole point.

Comparison stays `reflect.DeepEqual(got, tt.want)` — exact equality, never containment —
and the existing `t.Errorf` printing both slices and both lengths is unchanged.

### The mutation enumeration AC 4 asks you to grow

The eight check-deletion mutants are uniform and should be stated as one bullet rather
than eight: **delete any one check's append and that check's own row compares an empty
result against a one-name `want` and reddens, while the other eight rows return exactly
their own findings and stay green.** The positive control stays green throughout,
because deleting a check can only remove findings.

The structural mutants are the ones worth enumerating individually, because each is
pinned by a specific fixture choice a later editor could undo:

- **Append inside the option loop instead of flagging** → the both-blank-labels row
  returns two findings against a `want` of one and reddens alone.
- **Narrow the loop to the first option** (`first.Options[:1]`) → the second-option
  empty-description row returns nothing and reddens alone.
- **Loosen the option-count bound to `< 1`** → the single-option row returns nothing and
  reddens alone.
- **Drop the batch-length check's early return, keeping its finding** → the empty-batch
  row panics on `in.Questions[0]` rather than reddening on a mismatch. State this
  honestly in the doc: it is a red, but one that takes the package down with it, so that
  gate's early return is load-bearing in a way the decode guard's is not. #1951's four
  existing bullets stay as they are.

Over-determination remains unshippable for #1951's reason, and the five new rows are
where it would have entered: a fixture tripping two checks returns two findings and
fails its exact match on unmutated code, so such a row cannot reach the branch.

### Gate

```
go test -tags e2e_realclaude -race -count=1 -v \
  -run 'TestAskQuestionShape|TestFinOfflineFilesReachNoExecHelper' \
  ./internal/e2e/realclaude/
```

Every one must report PASS — not SKIP, not "no tests to run" — on a machine with no
claude and no credentials. **Read the count of tests that executed, never the exit
code.** `make check` never compiles this package, and the suite exits 0 both on a build
failure and on a full credentials skip; that `-run` invocation compiling and running is
your build proof, and `make preship` is the project's gate for it. Run `make check` as
well for the rest of the tree, and `make cite-guard` for the comments you add.

No `-overlay` run is required for AC 2. The sole-redness argument above holds by
construction from exact equality, which is #1951's measured lesson and the reason the
assertion is shaped this way.

## Line budget

**~175–205 total, one file, and the boundary is 400.** Derived from #1951's own commit
(`5b73eda2`: 402 insertions across this file and the ban table, 211 comment lines to 118
code lines) rather than by eye. Almost all of that is fixed per-file cost this child does
not pay again. Rough allocation: five constants and five listing appends ~10; the five
checks including the option loop ~20 code lines and ~55 with their prose; five fixture
literals with five rows and their "and it trips no other check" justifications ~55; AC
4's six corrections ~35; the grown mutation enumeration ~20.

#1943's file overran its spec's budget by 42% on comment density alone. If you overrun,
cut the AC 4 corrections down to the minimum true sentence — do not cut the reasoning at
the five rows, which is where a future reader cannot reconstruct why each fixture blanks
what it blanks.

## Open questions

- **Nesting versus flattening.** Whether claude nests options under each question or
  flattens them across the batch is still unmeasured, and #1938's capture settles it.
  Write the five checks against the documented shape; do not add a second accepted form
  to absorb the other spelling in advance, because a check accepting both cannot redden
  on either.
- **The `newDropcapScanner` gap in the seventeen banned names.** Inherited from #1951's
  security review unchanged, since this slice does not touch the entry. Closing it is
  table-wide work.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No finding, and this is the category the slice moves. #1951's
  checks read `rec.ToolName` and `len(in.Questions)`; these five are the first that read
  claude-authored *text* — `Header`, `Question`, and every option's `Label` and
  `Description`. The boundary stays explicit and single: `askQuestionShapeFindings` is
  the only function that decodes `rec.ToolInput`, and it hands nothing derived from
  those strings to any caller. In this slice every record is a synthetic literal, so
  nothing untrusted actually crosses; the boundary that matters is the future one, where
  #1938 calls `requireAskQuestionShape` over a live child's bytes.
- **[Tokens, secrets, credentials]** No open finding — one hazard, closed in the design
  rather than left to the developer: the single-option row writes a **local** literal
  and does not call `askQuestionPlantedInput`. Had the spec not settled it, this would
  have been a MUST FIX. That helper is the obvious thing to reach for — it is the
  package's only other one-option `AskUserQuestion` input — but its signature is
  `askQuestionPlantedInput(plant string)` and every call site hands it
  `askQuestionPlantedKeyPrefix` (an `sk-ant-`-prefixed synthetic credential) or
  `askQuestionPlantedPath` (a synthetic operator home path). Calling it here would put a
  credential-shaped literal inside a record that `requireAskQuestionShape` is designed
  to be handed, in a file whose stated defining property is carrying none, and would
  couple these rows to `ask_user_question_writer_test.go`'s deliberately narrower
  fourteen-name ban entry. The spec names the helper in the reading list precisely so
  the developer recognises and refuses it. Inherited unchanged from #1951: no
  environment read anywhere in the file, enforced by the `os.Getenv` / `os.Environ` /
  `os.LookupEnv` names in its `finOfflineExecBans` entry.
- **[File operations]** No finding. This slice adds no filesystem call and no path
  construction; the five fixtures are in-source literals. The `packageDir` group,
  `filepath.Glob` and the four `os` names in the file's existing ban entry still enforce
  it, and the entry needs no edit because `TestFinOfflineFilesReachNoExecHelper` keys on
  the filename. The hazard that peaks in a shape-assertion file is unchanged and worth
  restating: `go test` runs in the package source directory, so a relative
  `os.ReadFile("testdata/…")` reaches the committed captures while naming no wrapper.
  "Prove the new checks against a real capture" is the natural next thought here and is
  banned; no committed `permission_protocol_*` capture holds an `AskUserQuestion`
  `tool_use` block anyway.
- **[Subprocess / external command execution]** No finding. Nothing execs, and this
  slice adds no call to anything that could. The sharper hazard the first five banned
  names close is a **skip**, not an exec: `resolveClaudeBin` and
  `WithWorktreeAuthenticated` skip inside the test body after `=== RUN` prints, and a
  skip exits 0, which reads as a pass.
- **[Cryptographic primitives]** Not applicable by design decision rather than
  omission. No randomness is generated; `reflect.DeepEqual` compares returned findings
  against fixed in-file constants, so no attacker-influenced value is ever compared
  against a secret and constant-time comparison has no site here.
- **[Network & I/O]** One finding, addressed in the design rather than left to the
  developer. The option checks iterate a slice whose length is entirely
  claude-controlled. Appending a finding per offending option — the natural first
  implementation, and the one the ticket's "one name each" note warns against on
  fixture-width grounds — makes the findings slice, and therefore
  `requireAskQuestionShape`'s `t.Fatalf` message, grow linearly with attacker-supplied
  input, into a run log this pipeline salvages. The flag-then-append shape bounds the
  message at eight names regardless of input. The loop itself adds no new resource
  exhaustion surface: `json.Unmarshal` already allocated the slice before the loop runs,
  the loop allocates nothing per option, and Go's decoder bounds nesting depth and
  returns an error the guard converts into one named finding. There is still no byte cap
  on `rec.ToolInput` and none belongs here — the cap is #1938's, at the read that fills
  the field, named so #1938 does not assume this slice capped it.
- **[Error messages, logs, telemetry]** No finding, and it is the property most at risk
  in this slice. Every one of the five new checks reads claude-authored text and every
  one appends a **fixed constant** — not the field value, not an excerpt, not the index
  or count of offending options, not the decode error. A finding spelling out which
  option's label was empty, or echoing a header, would break the property #1951
  established structurally, and #1938 calls this over a live child's bytes. The
  flag-based loop reinforces it: no positional value derived from the input is ever
  computed, so there is nothing in scope for a later edit to fold into a message.
  `requireAskQuestionShape` is untouched and still prints only the count, the finding
  names, `ClaudeVersionSlug` and `ToolName`; `%v`, `%+v` and `%#v` on the record remain
  banned because `%+v` prints `ToolInput`. The table's `t.Errorf` prints the returned
  findings and the row name only, and in this slice every value is synthetic — the rule
  is set here because here is where it can be set.
- **[Concurrency]** No finding. No goroutines, no locks, no shared mutable state; the
  two loop flags are function-local, the five fixtures are inline constants, and each
  row's record is a fresh pointer from `askQuestionFullRecord()`. `t.Fatalf` requires
  the test goroutine, so `requireAskQuestionShape` must not be called from #1938's
  stdout reader — already documented at the wrapper and unchanged here.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model is
  relay-scoped and does not apply. The applicable model is the one
  `offline_exec_ban_test.go`'s header states for this package: the process environment
  carries `CLAUDE_CODE_OAUTH_TOKEN` and `ANTHROPIC_API_KEY`, agent run logs are salvaged
  by the pipeline, and `testdata/` holds committed captures a relative write would
  overwrite. All three stay addressed — no environment read (ban), no claude-derived
  value in any message (construction, reinforced above), no filesystem call at all
  (ban).

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
