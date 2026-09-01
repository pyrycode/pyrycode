# ask_user_question_writer_test.go

`ask_user_question_writer_test.go` (#1941) — the writer half of the
`AskUserQuestion` capture family, following
[ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md)
(#1943, the record) and
[ask_user_question_names_test.go](e2e-realclaude-ask-user-question-names-test-go.md)
(#1944, the namer), and preceding the live capture (#1942/#1938).
`scanAskQuestionFixture` marshals the caller's record and refuses via
`dropcapScanner.scan` before any filesystem call — its signature carries no
directory, so it cannot create an entry under one even in principle;
`writeAskQuestionFixture` calls it first, then `os.MkdirAll` → write `.tmp` →
`os.Rename`, copying `initialize_control_writer_test.go`'s
(`writeInitControlFixture`/`scanInitControlFixture`, #1702) shape. Unlike that
precedent, the scan step does **not** shallow-copy the record before
marshalling: this record carries no capture-cap field, so `out := *rec` would
mutate nothing today while silently sharing `ToolInput`'s backing array — a
copy that looks defensive and isn't. `scan`'s second return, `notApplied`, is
discarded in the writer and asserted on only in the offline test: refusing on
it in the writer would fail closed against every live capture, since
`newDropcapScanner`'s two credential classes are legitimately unapplied on a
subscription-login machine. Registered in `finOfflineExecBans` with the
sibling writer's fourteen names. Zero production files touched.

**Lessons that outlive this ticket:**

- **This family's doc-comment claims keep shipping unmeasured, and code
  review keeps finding them non-blocking rather than the file finding them
  itself.** This is the fourth such claim across the family (#1943's false
  "the mutant compiles", two in
  [ask_user_question_names_test.go](e2e-realclaude-ask-user-question-names-test-go.md)),
  and it recurred despite this ticket's own spec naming the pattern and
  requiring measurement first. `writeAskQuestionFixture`'s doc comment says
  the write is byte-for-byte "with nothing appended — not even a trailing
  newline, which would break the read-back's field equality by one byte."
  Measured false: an overlay mutant appending `'\n'` before the rename stays
  **green**, because `json.Unmarshal` skips trailing whitespace, so the byte
  never reaches any decoded field and the exactly-one-entry assertion doesn't
  see it either. The instruction (append nothing) is still correct; only the
  justification is wrong. Nothing in this file currently reddens on an
  appended trailing byte — that would need a raw-byte comparison against
  `os.ReadFile(path)` taken before decoding, which this file doesn't do.
- **The round trip's `want` snapshot is taken after the write call, so it
  checks the writer against itself rather than against what the caller
  handed in.** `TestAskQuestionFixture_RoundTripsEveryFieldIntoOneNamedEntry`
  builds `wantRec := *rec` below the `writeAskQuestionFixture` call, so a
  mutant that mutates the caller's record inside `scanAskQuestionFixture`
  before marshalling (confirmed with `rec.ToolName = "MutatedTool"`) passes
  the round trip: "want" is read from whatever the writer left behind, not
  from what was passed in. AC 4's "byte-identical to the one written"
  currently holds only in the weaker sense of "identical to whatever the
  writer decided to write" — the test cannot yet catch the in-place mutation
  hazard `scanAskQuestionFixture`'s own doc comment warns a future
  capture-cap change would introduce. Hoisting the snapshot above the write
  call closes it (and was measured to); even then, a copy still shares
  `ToolInput`'s backing array, so an in-place rewrite of those bytes (as
  opposed to a slice-header change) would stay invisible.

See `docs/specs/architecture/1941-*.md` for the full design and the security
review. The live capture that fills `ToolInput` from a real child is
#1942/#1938; the stale "#1941's fill site" forward references in
`askQuestionFullRecord`'s doc comment and in
[ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md)
belong to that ticket, not this one — this slice is offline and mints
nothing from a live call.
