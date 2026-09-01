# ask_user_question_reader_test.go

`ask_user_question_reader_test.go` (#1939) — the read half of the
`AskUserQuestion` capture family, and the last slice of it: a deterministic,
credential-free pass over whatever
`askQuestionFixtureGlob` (`testdata/ask_user_question_v*.json`) matches. Per
match it reads the file, scans the **raw bytes** (never a re-marshal — see
below) through `dropcapScanner{needles: dropcapFixedNeedles()}`, decodes into
[`askQuestionFixtureRecord`](e2e-realclaude-ask-user-question-record-test-go.md),
and calls
[`requireAskQuestionShape`](e2e-realclaude-ask-user-question-shape-test-go.md).
A zero-match glob and a malformed pattern both fatal by name, matching
`permission_protocol_regression_test.go`'s "a deleted fixture set must be
loud" idiom. The scan runs *before* the decode so a truncated or unreadable
file fails on its own, never reaching the shape assertion as a zero-valued
record. Its `finOfflineExecBans` entry is composed from
`initialize_control_compare_test.go`'s rather than copied from a sibling in
this family — `filepath.Glob` and `os.ReadFile` stay callable, and
`scanAskQuestionFixture`, `newDropcapScanner` and `realHome` are added to it,
closing the two routes (a re-marshal scan; a scanner built from live
`os.Getenv`/home lookups) that would make the offline reader inherit a
live or vacuous check for the file it's meant to pin. Zero production files
touched. The live half that fills the capture this file pins is
[ask_user_question_capture_test.go](e2e-realclaude-ask-user-question-capture-test-go.md)
(#1938).

**Lessons that outlive this ticket:**

- **This family's doc-comment claims keep shipping unmeasured, and code
  review keeps finding them non-blocking rather than the file finding them
  itself — now the fifth and sixth instance** (after #1943's false "the
  mutant compiles", two in
  [ask_user_question_names_test.go](e2e-realclaude-ask-user-question-names-test-go.md),
  and one in
  [ask_user_question_writer_test.go](e2e-realclaude-ask-user-question-writer-test-go.md)).
  Both findings here are the same shape: a correct mechanism, described by a
  wrong reason. The decode-before-shape ordering is right and is AC 2's
  explicit requirement, but the doc comment justifying it claimed a
  zero-valued record reaching `askQuestionShapeFindings` reports `tool_name`
  and *nothing else*, attributing the silence to "the decode guard...
  returns early on the empty `ToolInput`." Measured: it reports **two**
  findings, not one — `askQuestionCheckToolName` for the empty name, then
  `askQuestionCheckToolInputDecodes` because `json.Unmarshal` over a nil
  `json.RawMessage` returns `unexpected end of JSON input` on its way out of
  that same guard. The guard's early return is what *adds* the second
  finding, not what suppresses the rest. Separately, the `finOfflineExecBans`
  entry's comment claimed "NOT copied from any of the four
  `ask_user_question_*` entries above... those four ban `filepath.Glob` and
  `os.ReadFile`" — true of three, but the writer entry bans neither (it
  legitimately reads `os.ReadFile`/`os.ReadDir` against `t.TempDir()`), and
  the same comment two paragraphs later correctly pulls `newDropcapScanner`
  and `realHome` *from* the writer entry, contradicting its own "four" a
  sentence away. Both survived as code-review SHOULD FIX rather than MUST
  FIX and shipped unfixed — the mechanism each comment defends is correct,
  only the stated reason is wrong, so neither blocked the merge. A header
  whose narrative claims are countable (a finding count, a "those four")
  is exactly the shape that's cheap to falsify by hand before it ships;
  this family has now shipped six unmeasured ones across four different
  files without any of them being caught by the file's own tests, because a
  doc comment cannot fail a test.
- **The offline/live split for a fixture-consuming file is "does it read a
  fixed relative glob, or does it need `os.Getenv`/a live child" — not
  "does it touch `testdata/`."** This is the second file in the package with
  a legitimate reason to read `testdata/` while filing no exec-ban exemption
  for it (`initialize_control_compare_test.go` was the first); its ban entry
  proves the pattern generalizes rather than being a one-off carve-out. The
  trap named in this ticket held: a sibling's ban entry (any of the other
  four `ask_user_question_*` entries) cannot be copied whole into a new
  entry without checking which of `filepath.Glob`/`os.ReadFile` that sibling
  bans *and why* — the writer entry bans neither for a different reason
  (it operates under `t.TempDir()`, not `testdata/`) than this file leaves
  them open (it globs `testdata/` on purpose).

See `docs/specs/architecture/1939-*.md` for the full design, the mutant
table and the security review.
