# ask_user_question_record_test.go

`ask_user_question_record_test.go` (#1943) — the record half of the
`AskUserQuestion` capture family, the clarifying-question tool that sits
beside the permission-prompt tool on the daemon's real spawn but that no
committed `permission_protocol_*` capture has ever recorded a call to.
`askQuestionFixtureRecord` carries exactly four fields (`ClaudeVersionRaw`,
`ClaudeVersionSlug`, `ToolName`, `ToolInput json.RawMessage`), pinned by a
hand-written ordered-name literal so a fifth field reddens rather than being
silently absorbed — see
[initialize_control_record_test.go](e2e-realclaude-initialize-control-record-test-go.md)
for the shape this copies at one seventh the field count. `ClaudeVersionSlug`
(not `claude_version`) deliberately diverges from the family's shared tag: it
holds `versionSlug`'s output, a different quantity from the raw token the
sibling records tag `claude_version`. Registered in `finOfflineExecBans`
(`offline_exec_ban_test.go`) with the same seventeen names
`initialize_control_record_test.go` bans, copied whole. Zero production files
touched; no writer, no filename and no live claude land here — those are
\#1941, #1944 and the live run that follows.

**Lessons that outlive this ticket:**

- **A `go test -overlay` mutant cannot exercise a check that reads its
  subject off disk.** `TestFinOfflineFilesReachNoExecHelper`
  (`offline_exec_ban_test.go`) calls `parser.ParseFile` on a relative
  filename, which a build overlay does not intercept — the only way to prove
  a `finOfflineExecBans` entry is non-vacuous is to write the banned call
  into the real worktree file, run the one subtest, and revert. Any future
  ban-entry ticket in this package that "proves" its entry via overlay has
  proved nothing.
- **A single-site raw-JSON normaliser does not survive a map-typed mutant
  merely because the field's type changed — the call site itself has to be
  deleted, and that changes what "the mutant compiles" means.** This
  ticket's spec predicted `ToolInput map[string]any` "compiles" verbatim; it
  doesn't — `compactRawMessages(t, []json.RawMessage{rec.ToolInput})` is a
  hard type error once the field is a map, so the faithful mutant must also
  delete the two lines that call it. Once it does, the mutant behaves as
  designed (round-trip row green, wire-order subtest sole red). Code review
  caught that the false "compiles" claim shipped uncorrected in the file's
  own doc comment and in the round-trip subtest's comment — read this note,
  not that comment, before reusing this file's mutant table for #1941,
  #1942 or #1944.
- **This family's comment density does not scale down with field count.**
  #1701's 29-field record cost ~22 comment lines per field; this 4-field
  record cost ~77 — the file overran its own spec's line budget by 42% even
  though the correctness argument is a strict subset of the same reasoning.
  A future spec in this family sizing a file off "N fields, therefore
  N/29 of #1701's line count" will underestimate; budget a comment floor
  independent of field count.
- `askQuestionFixtureRecord`'s field *declaration order* is unpinned:
  nothing in the round trip, the ordered-name literal or the distinctness
  check would catch two fields being reordered, even though
  `askQuestionFixtureFields`'s doc comment claims "in declaration order."
  Worth knowing for the live fill site (#1938, below) before relying on it —
  #1941's own round trip (see the writer's document) reuses this same
  listing and inherits the same gap, unpinned by declaration order.

See `docs/specs/architecture/1943-*.md` for the full design and field-value
table. The write half —
[ask_user_question_writer_test.go](e2e-realclaude-ask-user-question-writer-test-go.md)
(#1941) — refuses to write by scanning this record's marshalled bytes, but
mints nothing from a live call; it is offline, with no fill site. The live
run that fills `ToolInput` from a real child is
[ask_user_question_capture_test.go](e2e-realclaude-ask-user-question-capture-test-go.md)
(#1938), which also found that claude's wire key order (`question, header,
options, multiSelect`) does not match this file's `askQuestionFixtureFields`
listing (`header, question, multiSelect, options`) — nothing asserts over
captured key order today, so nothing here needs to change, but the offline
reader (#1939) should not assume the two orderings match.
