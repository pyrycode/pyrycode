# ask_user_question_shape_test.go

`ask_user_question_shape_test.go` — the shape half of the `AskUserQuestion`
capture family, the contract that #1938's live run and #1939's offline reader
will both measure a capture against, built once here against synthetic
literals so the two consumers don't drift into separate copies.
`askQuestionShapeFindings(rec *askQuestionFixtureRecord) []string` takes
[ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md)'s
(#1943) record and **returns** the name of every failed check instead of
failing the test from inside the helper, so a negative row can assert on the
result; `requireAskQuestionShape` is the thin `t.Fatalf` wrapper other tests
call. The decode target is declared whole — header, question text, options,
and a multi-select key typed `json.RawMessage` rather than `bool` so an
absent key stays distinguishable from `false`. It now reports **seven shape
checks** (tool name and at least one question from #1951; question text,
header, option count ≥ 2, every option's label and every option's
description from #1952) across **eight reported names** (the seven plus the
decode guard, which is not itself a shape check but whose negative row
counts) and **nine table rows** (the eight negatives plus the positive
control). The multi-select key stays unchecked — that's the slice after
#1952. Registered in `finOfflineExecBans` with the record file's seventeen
names, not the fourteen-name entry beside it that
[ask_user_question_writer_test.go](e2e-realclaude-ask-user-question-writer-test-go.md)
(#1941) carries for writing a directory. Zero production files touched.

**Lessons that outlive this ticket:**

- **Comparing returned findings by exact equality (`reflect.DeepEqual`
  against a `want []string`), not by containment, turns a per-check mutation
  matrix into something a reviewer can check by inspection instead of
  running.** Deleting a check makes that check's own row compare a non-empty
  `want` against an empty result and redden, while every other row still
  returns its own finding and stays green — sole-redness holds by
  construction. Over-determination becomes unshippable for the same reason:
  a fixture that trips two checks returns two findings and fails its own
  exact match on unmutated code, so it can't reach the table at all — and
  with nine rows checked in, "no row is over-determined" needed no mutant to
  establish: it comes free the moment the whole table is green, since a
  fixture tripping a second check would already fail its own match on
  *unmutated* code. Overlay mutants remain useful for confirming
  sole-redness, not for over-determination.
- **A collided-constant mutant reddens the vacuity control alone, and
  nothing else** — because every row compares identifier against identifier,
  not derived text. That is the argument for `askQuestionCheckNames`
  existing as its own function with its own pairwise-distinct-and-non-empty
  subtest, rather than being inlined into the table rows: without it, two
  checks silently sharing a name would leave every row green.
- **Not every early return is pinnable the same way, and a row can only
  prove the shape of the failure it actually produces.** The decode guard's
  early return is pinned by a row that reports two names without it — a
  clean redden a table can describe. The batch-length check's early return
  is not pinnable the same way: drop it and the empty-batch row *panics* on
  `in.Questions[0]` rather than reddening on a mismatch, taking the whole
  package down with it. Both are reds under `-overlay`; only one shows up as
  a table row failing.
- **Per-option checks pin option-*width* independence by construction (one
  flag-then-append loop, one name however many options are wrong); the
  *batch*-width form of the same property is still construction-only.** Every
  row in the table carries exactly one question, so nothing here would catch
  a mutant that looped over `in.Questions` and appended a finding per
  question instead of reading only the first — it would return identical
  findings on all nine rows and stay green. Flagged in #1952's code review as
  a deliberate non-fix: a tenth row would contradict the nine-row count the
  header states, and a multi-question fixture belongs with whichever slice
  first has a reason to carry one. #1950 and #1938 should know this property
  is unpinned before assuming it's covered.

See `docs/specs/architecture/1951-*.md` and
`docs/specs/architecture/1952-ask-user-question-shape-content-checks.md` for
the full design and security reviews. The live capture that fills
`ToolInput` from a real child is #1938 alone; the stale "#1942 is a
live-capture slice" forward references in `ask_user_question_writer_test.go`
and
[ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md)
belong to that ticket, not this one — this slice reads no file, resolves no
directory, and starts no child process.
