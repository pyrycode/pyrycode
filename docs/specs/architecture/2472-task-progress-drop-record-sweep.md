# #2472 — assert the task_progress drop over captured records, not a rendered dump

One test function changes. No production file is touched.

## Files read

- `internal/streamsup/parser_test.go` → `TestParser_TaskProgressDropIsLoggedContentFree` — the flaking
  assertion: it substring-scans a rendered `slog.TextHandler` dump whose leading `time=` attribute can
  contain `99` (`.991`, `.399`, `.099`).
- `internal/streamsup/parser_test.go` → `logRecorder`, `capturedRecord`, `logRecorder.all`,
  `logRecorder.withMessage` — the capture already in this file. Records carry `msg`, `level` and
  attr key/value pairs and nothing else; there is no timestamp in the swept surface.
- `internal/streamsup/parser_test.go` → `TestParser_HarnessNudgeDropIsLoggedContentFree` — the shape to
  mirror: a positive control on the expected record, then a sweep of every captured record for the
  forbidden content.
- `internal/streamsup/parser_test.go` → `taskProgressLineFixture` — where the `99` comes from. It is the
  fixture's `usage.total_tokens` argument, today duplicated as a hand-typed `"99"` in the forbidden list.
- `internal/streamsup/parser_test.go` → `undecodableSystemLineMsgFixture` — the existing named literal for
  the drop message, whose doc comment says it exists so a test can select the record to assert on.
- `internal/streamsup/parser.go` → `emitBackgroundTaskProgress` — the code under test. Exactly one of its
  five reject arms logs (`p.log.Debug` with `"subtype", "task_progress"` and nothing else); the other four
  are silent. Its doc comment states the rule the test pins: nothing derived from the line is logged on
  any path.
- `docs/knowledge/features/streamsup-package.md` § "Testing" — the package's test conventions; no lesson
  there bears on this change beyond table-driven + `-race`.

## Change

`TestParser_TaskProgressDropIsLoggedContentFree` swaps its `bytes.Buffer` + `slog.NewTextHandler` for the
file's own `logRecorder`, and asserts over what the parser logged instead of over what a handler rendered.
The forbidden set is unchanged in intent — task id, description, progress counter — but the counter token
is now derived from the fixture constant with `strconv.Itoa` rather than hand-typed, so the guard cannot
drift away from the value the fixture actually sends. Each captured record is swept for all three tokens
across its message, its attr values **and its attr keys** (see the security review's first finding: the
rendered dump covered keys, and dropping them would be a real narrowing of a redaction guard). The three
rows are otherwise untouched, so all three still reject all three tokens.

One assertion is added rather than moved: a per-row `wantDrops` count checked with
`withMessage(undecodableSystemLineMsgFixture)` — 1 for the `undecodable` row, 0 for the two silent rows.
Without it the sweep is vacuous on a mis-wired recorder (nothing captured, nothing to scan, green), which
is the same class of failure as the cheap wrong fix the ticket's criterion 2 rules out. It is the positive
control the mirrored sibling already carries.

Nothing else moves. `bytes` and `strconv` both stay in the file's imports — ten and fifteen other uses
respectively. The three sibling rendered-dump sites named in the ticket body are left alone.

## Testing strategy

This ticket's deliverable *is* the test, so the proof is the test's own behaviour on both sides:

- `go test -race -count=200 -run TestParser_TaskProgressDropIsLoggedContentFree ./internal/streamsup`.
  Measured on this worktree before the change: many failures across the 200 iterations, every one the
  `undecodable` row matching `99` inside `time=…`. Must be clean after.
- Anti-vacuity, checked by hand during Phase B and not committed: temporarily add the task id to the drop
  log's attrs and confirm each of the three rows reddens; temporarily break the recorder wiring and
  confirm the `wantDrops` control reddens. Criterion 2 is what these check.
- `go test -race ./internal/streamsup/...`, `go vet ./...`, `go build ./cmd/pyry` per § B2.

## Documentation handoff

None. The ticket carries no documentation acceptance criterion, and the change alters no documented
behaviour — `docs/knowledge/features/streamsup-package.md` describes the package's content-free logging
rule, which is unchanged; only its proof gets sturdier.

## Security review

**Verdict:** PASS (after one revision — the first pass found a coverage narrowing, addressed in the Change
section above before this plan was committed).

**Findings:**

- [Error messages, logs, telemetry] **MUST FIX — addressed by revision before commit.** The rendered dump
  the test scans today covers the *whole* record, attr keys included. A sweep of only `msg` + attr values,
  which is the literal wording of criterion 1 and what the mirrored `TestParser_HarnessNudgeDropIsLoggedContentFree`
  does, silently drops the key surface: `p.log.Debug(msg, tl.TaskID, "x")` inside `emitBackgroundTaskProgress`
  would put claude's task id in an attr *key* and pass. Narrowing a redaction guard while fixing its flake
  is the worst version of this ticket. The plan now sweeps keys alongside values; the extra check is one
  `strings.Contains`.
- [Error messages, logs, telemetry] No further findings. The failure text no longer echoes the full
  rendered record, only the offending token — which is the test's own fixture — plus the record message
  and attr key. The one place a whole record can still reach test output is the `wantDrops` mismatch's
  `%+v`, deliberate and harmless: every value in play (`secretID`, `secretDescription`, the counter) is a
  synthetic literal declared in the test, never a real secret, and the sibling prints the same thing for
  the same diagnostic reason.
- [Trust boundaries] The boundary is `Parser.Write` → `emitBackgroundTaskProgress`, where claude's stdout
  becomes daemon state, and this test is a guard *on* that boundary rather than a change to it. The
  boundary stays explicit and single-sited: the decode is taken from the top-level line bytes only, as
  `emitBackgroundTaskProgress`'s doc comment requires so a tool result whose text is literally a
  task_progress line cannot forge one. Untouched here.
- [Threat model alignment] The threat the guard answers: a hostile or compromised claude child emits a
  task_progress line whose `description` names a sensitive path (the fixture, `Reading
  /Users/somebody/secrets.txt`, is exactly that shape), and a daemon that logged it would carry that
  content into the daemon log, which is readable over the control plane's `logs` verb. Keeping the drop
  arms content-free is what makes that unreachable, so a guard that can be green for the wrong reason —
  either flaking away, or narrowed — is the risk this ticket removes. OUT OF SCOPE, per the ticket body:
  the same rendered-dump shape at `internal/streamsup/mcp_status_policy_test.go`,
  `internal/relay/v2session_interrupt_test.go` and `internal/relay/v2session_appframe_test.go`. Those
  forbid word tokens no timestamp can produce and no flake has been observed there; no follow-on ticket is
  filed, by the refiner's explicit decision.
- [Concurrency] No findings, and the change strictly improves the position. `logRecorder` is mutex-guarded
  and `all()` returns a copy, so the sweep cannot race a late `Handle`; the `bytes.Buffer` it replaces had
  no lock and was safe only because the parser logs synchronously inside `p.Write` on the test's own
  goroutine. Each parallel subtest builds its own recorder and its own parser, sharing nothing. AC3's
  `-race -count=200` is what covers the assumption that no parser goroutine logs after `Write` returns.
- [Tokens, secrets, credentials] Not applicable, by design rather than by luck: nothing on this path
  generates, stores, compares or rotates a credential. The values the test calls "secret" are
  claude-derived identifiers and a file path — content, not credentials — and they are covered above
  under logging and trust boundaries.
- [File operations] Not applicable. The test opens, stats and creates nothing: no `t.TempDir`, no capture
  file read, no path built from input. `Reading /Users/somebody/secrets.txt` is a string literal that is
  never resolved as a path.
- [Subprocess / external command execution] Not applicable. The parser is driven by a direct `p.Write`;
  the package's `GO_STREAMSUP_HELPER` fake-child harness is not used by this test and no process is
  spawned.
- [Cryptographic primitives] Not applicable. No randomness, hashing or comparison of attacker-controlled
  values against a secret anywhere in the changed code.
- [Network & I/O] Not applicable to the change. The input-size discipline on this path —
  `emitBackgroundTaskProgress`'s `truncateField` bounds on `task_id` and the parser's `maxBuf` — is
  production code this ticket does not touch and has its own tests.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-16
