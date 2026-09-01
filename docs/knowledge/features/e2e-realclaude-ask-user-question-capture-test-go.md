# ask_user_question_capture_test.go

`ask_user_question_capture_test.go` (#1938) — the live half of the
`AskUserQuestion` capture family, and the last unbuilt slice of it apart from
the offline reader (#1939). `TestRealClaude_AskUserQuestion_CapturesTheCall`
spawns a real claude with the daemon's own permission flags
(`--permission-prompt-tool mcp__pyry_approve__approve`, `--mcp-config`,
`--strict-mcp-config`, `--permission-mode default`, deliberately no
`--allowed-tools`), drives it to call `AskUserQuestion`, and taps the call on
the approve path rather than on stdout: a stub control-socket server
(`askQuestionCaptureServe`) answers the real `pyry mcp-approve` child in place
of the daemon, publishes a copy of the wanted payload, and denies every call
unconditionally — the wanted one included, so no gated tool executes and the
run leaves nothing behind. The stdout reader (`askQuestionCaptureRead`)
records only the first `system`/`init` line's `tools` array, which is what
lets the run tell "claude declined to ask" apart from "this claude release
stopped offering the tool" (`askQuestionCaptureOffers`). On the success path
the payload is assigned straight into
[`askQuestionFixtureRecord`](e2e-realclaude-ask-user-question-record-test-go.md)'s
(#1943) `ToolInput` — never decoded and re-marshalled, since `encoding/json`
would sort the call's own keys — written through
[`writeAskQuestionFixture`](e2e-realclaude-ask-user-question-writer-test-go.md)
(#1941) armed with `newDropcapScanner` rather than the offline fixed-only
form, and then measured by
[`requireAskQuestionShape`](e2e-realclaude-ask-user-question-shape-test-go.md)
(#1951/#1952/#1950). Zero production files touched.

## What the capture measured

- **The premise holds.** `AskUserQuestion` does route through
  `--permission-prompt-tool` on claude 2.1.239, and 2.1.239 still offers the
  tool under a prompt-tool spawn — both were open questions in the spec, not
  assumptions.
- **Options are nested under each question, not flattened across the
  batch.** `askQuestionInput`'s decode target was written against the
  documented shape with this exact ambiguity unmeasured; the committed
  `testdata/ask_user_question_v2.1.239.json` settled it, all eight shape
  checks passed with no divergence, and the target needed no widening.
- **The wire key order is not the fixture's.** claude emits `question,
  header, options, multiSelect`;
  [`askQuestionFixtureFields`](e2e-realclaude-ask-user-question-record-test-go.md)
  lists `header, question, multiSelect, options`. Nothing asserts over
  captured key order today, so this changes nothing on its own — but #1939
  should not assume a synthetic literal's field order matches what a real
  call sends.
- The captured batch holds one question, so the batch-width limitation this
  family's shape doc already flags (only `Questions[0]` is ever checked)
  remains genuinely untested — this capture could not have exercised it
  without deliberately asking a multi-part question, which was not this
  ticket's job.

## Lessons that outlive this ticket

- **A live-spawn probe whose only wake-up signals are "the wanted event" and
  "the deadline" pays the full deadline when the child dies at startup.**
  Measured under a `go test -overlay` pointing `--mcp-config` at a
  nonexistent path: claude rejected the config and exited in under a second,
  but with only those two `select` arms the run sat for the entire budget
  before reporting a spawn that had been dead the whole time.
  `askQuestionCaptureRead`'s `readerDone` channel closing at stdout EOF is
  the missing third arm, and it costs one line. It also exposes a race worth
  closing rather than arguing away: the wanted payload is published before
  the stub encodes its deny, and claude cannot exit before reading that
  deny, so on a successful run the payload arm and the EOF arm can both be
  ready at once and `select` picks between them at random. A non-blocking
  re-read of the payload channel after the join is what keeps a call that
  was in fact recorded from being reported as "claude exited with nothing."
- **`cmd.Wait()` before joining a stdout reader is deliberate here, against
  the usual instinct to join first.** `Wait` closes the stdout pipe, which
  is what unblocks a reader still parked on a descriptor a grandchild
  (claude's own MCP child, `pyry mcp-approve`) holds open. The `system`/
  `init` line this run actually needs arrives long before either exit or
  join, so the reader's truncated tail costs this class of probe nothing.
- **A fixed deny-scan needle class turns the elicitation prompt into a
  correctness concern, not a stylistic one.** The scanner's fixed
  `/var/folders/` class is armed unconditionally, and on macOS a
  `t.TempDir()`-backed worktree lives under exactly that path. A prompt that
  invites claude to quote its own working directory makes
  `scanAskQuestionFixture` refuse, writes nothing, and spends the spawn for
  no artifact. Keeping the elicitation subject abstract — two named
  approaches, no paths, no filenames, no "in this repo" — is what keeps the
  run's bytes writable at all, and `askQuestionCapturePrompt` is built that
  way on purpose.

See `docs/specs/architecture/1938-ask-user-question-live-capture.md` for the
full design, the concurrency model and the security review. The record half
([`ask_user_question_record_test.go`](e2e-realclaude-ask-user-question-record-test-go.md))
and the writer half
([`ask_user_question_writer_test.go`](e2e-realclaude-ask-user-question-writer-test-go.md))
both carried a stale "the live capture is #1942's" forward reference; both
now name this ticket, and this file is the fill site they were pointing at.
