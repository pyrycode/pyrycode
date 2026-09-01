# ask_user_question_shape_test.go

`ask_user_question_shape_test.go` (#1951) — the shape half of the
`AskUserQuestion` capture family, the contract that #1938's live run and
#1939's offline reader will both measure a capture against, built once here
against synthetic literals so the two consumers don't drift into separate
copies. `askQuestionShapeFindings(rec *askQuestionFixtureRecord) []string`
takes
[ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md)'s
(#1943) record and **returns** the name of every failed check instead of
failing the test from inside the helper, so a negative row can assert on the
result; `requireAskQuestionShape` is the thin `t.Fatalf` wrapper other tests
call. The decode target is declared whole — header, question text, options,
and a multi-select key typed `json.RawMessage` rather than `bool` so an
absent key stays distinguishable from `false` — even though this slice's two
checks (tool name, at least one question) read only the batch length; the
five content checks over the first question are #1952, the multi-select key
is the slice after that. Registered in `finOfflineExecBans` with the record
file's seventeen names, not the fourteen-name entry beside it that
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
  exact match on unmutated code, so it can't reach the table at all. Five
  `-overlay` mutants confirmed this rather than discovering it (tool-name
  check deleted, questions check deleted, the decode guard's early return
  dropped, the guard deleted outright, two check-name constants collided) —
  with a `contains` assertion those five runs would have been the only way
  to know, one overlay per check, every time the check list grows.
- **A collided-constant mutant reddens the vacuity control alone, and
  nothing else** — because every row compares identifier against identifier,
  not derived text. That is the argument for `askQuestionCheckNames`
  existing as its own function with its own pairwise-distinct-and-non-empty
  subtest, rather than being inlined into the table rows: without it, two
  checks silently sharing a name would leave every row green.

See `docs/specs/architecture/1951-*.md` for the full design and the security
review. The live capture that fills `ToolInput` from a real child is #1938
alone; the stale "#1942 is a live-capture slice" forward references in
`ask_user_question_writer_test.go` and
[ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md)
belong to that ticket, not this one — this slice reads no file, resolves no
directory, and starts no child process.
