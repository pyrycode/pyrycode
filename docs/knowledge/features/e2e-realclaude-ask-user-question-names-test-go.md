# ask_user_question_names_test.go

`ask_user_question_names_test.go` (#1944) — the name half of the
`AskUserQuestion` capture family, following
[ask_user_question_record_test.go](e2e-realclaude-ask-user-question-record-test-go.md)
(#1943, the record) and preceding #1941 (the writer) and #1938 (the live
capture). `askQuestionFixtureName(versionToken string) string` is one pure
`fmt.Sprintf` over `versionSlug`, minting `ask_user_question_v<slug>.json`
with a literal prefix no input can reach — copying
[initialize_control_names_test.go](e2e-realclaude-initialize-control-names-test-go.md)'s
`initControlFixtureName` shape rather than its arm-carrying half, since this
capture has no arm dimension. The lock table grew a golden `want` column
beyond that precedent's three subtests, because a namer that stopped calling
`versionSlug` is otherwise invisible to them: `2.1.239 (Claude Code)` and a
64-byte token are the only two rows where that column is the sole red. The
pattern table carries four rows, not three — `initControlArmFixtureGlob`
(#1764) is required here as a foreign family to fence out, where in its own
file it is barred for the opposite reason (see below). Registered in
`finOfflineExecBans` with the same seventeen names
`ask_user_question_record_test.go` bans, copied whole. Zero production files
touched.

**Lessons that outlive this ticket:**

- **This family's comment-density floor held a third time, and it is a floor
  rather than something that scales down with dimension count.** The spec
  sized this file at ~370 lines by scaling #1696's 332 *up* for the golden
  column and the fourth pattern row — the correct direction, and still 20%
  short: it landed at 401 (plus 44 in `offline_exec_ban_test.go`). The
  overrun wasn't new argument, it was the same per-mechanism paragraphs
  costing what they cost regardless of how many dimensions the namer has.
  Three data points now exist in this family: #1696 at 332, #1943's record at
  567, this ticket at 445. A fourth name-lock or record ticket here should
  size against those measurements, not against #1696 alone.
- **A "do not add this glob" instruction in a sibling file's doc comment is
  scoped to the family that owns it, not to every reader of that glob.**
  `initControlArmFixtureGlob`'s doc comment forbids adding it to the
  family-glob tables in `initialize_control_names_test.go` — correctly, since
  there it is that family's *own* glob and every arm name matches it by
  design, so a row asserting non-collision would be red against correct code.
  Here it is a foreign glob, so AC 3 required the opposite: adding it, to
  keep the four `initialize_control_*` captures it protects from going
  unguarded by this lock. Read whose family the instruction's author was
  writing for before obeying or ignoring it elsewhere.
- **Two doc-comment claims in this file describe a property the declaration
  doesn't have, and code review flagged both as non-blocking (SHOULD FIX /
  NIT) rather than as build-breaking, so they shipped uncorrected.** This is
  the same failure mode #1943 shipped once already (a false "the mutant
  compiles" claim) and it recurred rather than being closed off by that
  precedent:
  - `askQuestionNameRow`'s doc comment says it is function-local and that
    keeping it so "holds this file's package-scope surface at two
    identifiers"; the type is in fact declared at package scope, making three.
    No collision exists today (nothing else in the package declares the
    name), so this is latent rather than active, but #1941 lands in this same
    package next.
  - The comment on the `base == "." || base == ".."` assertion (in
    `TestAskQuestionFixtureName_AvoidsCommittedFamiliesAndStaysContained`'s
    containment subtest) claims it "pins the `ask_user_question_v` prefix
    against a later edit that drops it." Measured against the spec's own
    prefix-dropping mutant, that assertion stays green — token `..` mints
    `...json`, which is neither `.` nor `..`. The rest of that paragraph
    (strictly implied, cannot be sole red, doesn't catch a mis-slugged token)
    is correct; only this one clause overclaims.

  Neither defect changes runtime behaviour, and both were left as the file's
  own future-reader hazard rather than fixed at review time. A reader who has
  learned to distrust this family's self-description (already true for
  `ask_user_question_record_test.go`) should distrust these two spots in this
  file as well until a later ticket corrects them.

See `docs/specs/architecture/1944-*.md` for the full design, the per-mutant
table, and the security review. The writer is #1941; the live capture is
\#1938.
