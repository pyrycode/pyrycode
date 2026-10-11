# #2939 — diagnose tool-then-text stream-event rejection

## Files read

- `internal/streamsup/parser_stream_event.go` → `emitStreamEvent`, `resetStreamEventState`: decoding, subtype and attribution rejection guards.
- `internal/streamsup/parser_assistant.go` → `emitAssistant`: completed text suppression and completed tool ownership.
- `internal/streamsup/parser.go` → `consumeLine`, `emitUnrecognized`: result reset and content-free diagnostics.
- `internal/streamsup/parser_tool_then_text_capture_test.go` → `TestParser_ToolThenTextIsolatedCapture`: retained isolated evidence, ordered text attribution and completed-tool cardinality.
- `internal/e2e/realclaude/stream_event_capture_test.go` → `TestRealClaude_StreamEventCapture`, `secapFixturePath`: fixture-absence admission and ordered capture.
- `internal/e2e/realclaude/dropped_line_capture_test.go` → `dropcapRecorder`, `dropcapRedactor`, `dropcapScanner`: bounded recording and deny-scan.
- `internal/e2e/realclaude/effort_init_capture_test.go` → `effortInitPass.fill`: dynamic messaging-socket redaction and matching deny needles.
- `docs/knowledge/features/streamsup-package-turn-io-envelope-write-stdout-parser.md` → Turn I/O: result attribution reset and isolated reset assertions.
- `docs/knowledge/features/e2e-realclaude-stream-event-capture-test-go.md` → whole-turn promotion: zero drops and socket redaction are required.
- `docs/knowledge/features/development-verification.md` → Captures and live evidence: nonzero event count is not an unrecognized census; temporary artifacts need durable promotion.

## Context

The mobile tool-then-text method failed on daemon `6019328b378cad587f69b7bc94de37febbdf8556`, mobile `5de4f4459`, Claude 2.1.280. Its sentinel does not identify the rejecting row. Recovery from mobile #1888 comments and PR #1926 finds no raw ordered sequence or effective model/effort/permission settings. No mobile checkout is available in this runtime. Historical settings remain unknown. The 2.1.259 fixture cannot establish this cause.

One deliverable: a cause-backed parser repair. The design is bounded to approximately 720 written lines, zero exported types, one capture entry point, three acceptance criteria and at most ten rejection branches. The fetched feature branches have no overlap with the planned parser/capture files. No decision record is needed before diagnosis establishes a cause.

## Design

Adapt `TestRealClaude_StreamEventCapture` to invoke a scenario-specific probe in a separate file. Preserve the #2270 fixture and helpers. The new probe arms on absence of `testdata/tool_then_text_stream_event_v*.json` and honours `PYRY_PROBE_STREAM_EVENT_CAPTURE=1`.

Drive the exact historical prompt in a fresh authenticated HOME and empty workdir. Do not select the old probe's haiku/bypass settings. With unavailable mobile settings, the first isolated attempt uses Claude's default model/effort and explicit Bash admission; record this assumption, observed init settings, actual argv, daemon revision and observed Claude version. This attempt cannot establish equivalence to the historical mobile configuration.

Tee stdout to the production parser and then recorder, explicitly bind `Config.ControlParser`, and request initialize on spawn. Capture every ordered row through result, parser rejection indices, init settings, capture-drop counters and tail length. Redact dynamic paths and consistent identity substitutions before writing; retain a deny-scanned recovery copy outside the worktree and promote only complete zero-drop records to a separate validated version path. Metadata and rows are compact to keep the fixture within the written-work budget.

Replay the captured sequence to identify the exact rejecting guard. Repair only a demonstrated guard/state defect; add a failing-before/passing-after regression. If the direct-child run does not reject, retain its evidence and prepare `needs-live-artifacts` for the unchanged mobile drive, naming the required sequence/settings/revision/counts. Do not infer a subtype or change production classification from a passing attempt.

## Concurrency model

The runner owns child stdout forwarding. Parser callbacks and recorder writes are serial in that forwarder; recorder snapshot uses its existing mutex. Cancel and join the runner before final recording/redaction. The capture goroutine exits on context cancellation and is joined with a bounded shutdown wait. No production goroutines are added.

## State transitions and identity reuse

| Event | Race-detector proof |
| --- | --- |
| Stream message/block identities repeat or change across rows | `TestToolThenTextCaptureRedaction` retains consistent substitutions and JSON index shape |
| Captured result closes the ordered recording | `TestToolThenTextCaptureAdmission` refuses loss, incomplete tail or missing result |
| Result clears parser attribution | Existing `TestParser_StreamEventResultClearsAttribution`; extend focused replay after cause is known |
| Completed text follows deltas and completed tool follows partial JSON | Focused captured replay after diagnosis must assert exactly-once delivery and attribution |

## Error handling

Fail loudly on unusable capture, no authentication, missing result, dropped rows or denied data. An authenticated non-rejecting capture is explicitly an inconclusive diagnosis, not a parser repair. Keep visible unsafe/unknown rejection and content-free thinking. Production logs never receive model text.

## Testing strategy

Write offline probe checks first: dynamic socket scanner detects raw data and accepts redacted data; identities/index JSON shapes survive; incomplete/lossy records cannot promote. Run the forced admitted capture and retain executed/passed/failed/skipped counts. For a demonstrated cause, run focused parser replay under race detection, then race tests on touched packages, `go vet ./...`, and build pyry into scratch storage. The full-module and subsequent full live suites belong to the dispatcher.

Run the unchanged named mobile method against this branch when the admitted mobile environment is available. Otherwise carry its exact remaining proof through the normal live-artifact handoff, including the tested binary revision and counted XML results.

## Open questions

- Which raw row and guard reproduce the rejection? Resolve from capture/replay; no guessed production patch.
- What are the effective mobile settings? Historical values are unavailable; obtain them from the named mobile drive if the isolated attempt is inconclusive.
- Is the cause within the planned two production files? Reassess through refinement if the demonstrated fix exceeds scope.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/streamsup-package-turn-io-envelope-write-stdout-parser.md`, under `Turn I/O — envelope write + stdout parser`, describe the established event shape/order and resulting classification or attribution behavior, link the committed regression fixture, and describe only the cause demonstrated by replay.

## Security review

**Verdict:** PASS

- [Trust boundaries] Capture stdout remains untrusted JSON; production parser selects events and the probe records raw shapes without widening admissibility.
- [Tokens] Reuse the admitted launcher and authenticated HOME helper; never copy login credentials manually. The existing scanner uses credential values only as deny needles.
- [File operations] Write recovery records at 0600 in a test-owned temporary directory; use `secapFixturePath`'s validated version token with a distinct constant family prefix. No subprocess value names arbitrary files.
- [Subprocesses] Use the production runner, exact test-owned echo prompt and empty workdir; cancel and join it before promotion. Record Bash admission as an explicit reproduction assumption.
- [Cryptography] No new cryptographic operation or credential generation.
- [Network and I/O] Reuse the 8 MiB recorder cap and refuse any capture loss or unterminated tail.
- [Errors/logs] SHOULD FIX: register dynamic messaging-socket paths with both redactor and scanner before promotion; test both directions. Emit counts/class names without model payloads.
- [Concurrency] Parser-to-recorder ordering ensures result observation follows parsing; join shutdown before snapshot/redaction to avoid cross-goroutine counters.
- [Threat model] This capture changes no relay/mobile trust boundary or production settings. Credential and path leakage in durable evidence is guarded by deny-scan before any write.

**Reviewer:** builder (self-review)
**Date:** 2026-10-11

## Revisions

- 2026-10-11: The forced isolated capture ran on Claude 2.1.280 with observed `claude-sonnet-5`, permission `default`, explicit Bash admission and no model/effort override; init omitted effort. The first diagnostic run executed and failed in the fixture writer (1 executed / 0 passed / 1 failed / 0 skipped). A second run retained 30 ordered rows through result with zero loss, no tail and no rejection (1 executed / 1 passed / 0 failed / 0 skipped). This does not establish the mobile cause. Add `TestParser_ToolThenTextIsolatedCapture` to retain that observed valid behavior, and hand off the named mobile drive's rejecting ordered capture with `needs-live-artifacts`; production parser behavior stays unchanged until a rejecting guard is demonstrated. Historical mobile settings remain unknown.
- 2026-10-11: Offline redaction negatives exposed two instrument traps: a root path's slug can corrupt negative JSON numbers, and global short-identity replacement can change string-valued indices. Ignore nondynamic root paths and substitute identities only in identity-valued JSON fields, decoding numbers with `UseNumber`. Final evidence must come from a fresh forced capture after these instrument corrections.
- 2026-10-11: Final forced capture at committed probe `98b937b6f45cf5fe6dff4f8280d1b2e713789edd` again executed and passed (1/1/0/0), retaining 30 rows, three text chunks, one completed tool start and one successful tool completion. It reproduced no rejection. The fixture's dirty-tree flag reflects the earlier untracked generated fixture; the parser and runner source were committed. `TestParser_ToolThenTextIsolatedCapture` proves this fixture's ordered, exactly-once delivery; existing stream-event refusal/thinking/reset tests remain green under race detection. The missing rejecting mobile sequence and unchanged focused mobile pass are pending through the live-artifact handoff, not claimed as completed acceptance.
- 2026-10-11 (verifier rework, findings 1 and 2): Replace object decode/re-encode redaction with JSON-token string-span edits. Preserve duplicate keys, field order, numeric spellings and trailing bytes; recovery stores ordered rows as JSON strings so malformed rows remain recoverable and fail promotion. Retain the earlier isolated fixture, and write fresh wire evidence under `testdata/tool_then_text_wire_stream_event_v<version>.json` with a coupled replay reader. Count original live parser rejections and original-row replay before redaction; require retained replay to reproduce the original rejection rows/sites/kinds before promotion. Apply the shared identity mapping to `wire_tool_inputs` keys. `TestToolThenTextCaptureWireRejections` covers duplicate-index rejection followed by attribution rejection and invalid suffix recovery; `TestToolThenTextCaptureRedaction` pins tool-ID/key equality. Security re-review: token edits never reinterpret structure, malformed input is retained as inert strings, deny-scan still precedes all writes, no new credentials/subprocesses/goroutines. PASS. Fresh forced capture remains required after these corrections. Rework raises the branch to approximately 1050 written lines; the capture and its coupled replay are one-consumer proof for the same deliverable, so the handback floor rule keeps them together. Zero new exports or production call-site changes; no additional product rejection branches.


- 2026-10-11 (rework evidence): Forced capture at clean `bc6e1e9ba5c99d8ec20a4a8183a40f6df4e90bda` executed/passed/failed/skipped 1/1/0/0 on Claude 2.1.280; observed sonnet-5/default and absent effort, with explicit Bash admission. Fresh string-row fixture retains 30 rows, zero loss/tail, three text chunks, one tool start/completion and zero original rejections; retained replay matches both original replay and the live parser. Offline race checks passed 51/51/0/0; scoped streamsup race checks passed 1475/1475/0/1 (existing optional slow deadline probe skipped). Vet, tagged vet and build passed. Portable evidence is `internal/e2e/realclaude/testdata/tool_then_text_capture_rework_evidence.json`; recovery copy is `/tmp/pyry-2939-capture-1606281665/tool_then_text_record.json`. The mobile cause and unchanged focused mobile pass remain pending through the live-artifact return contract.

### Live-artifact return contract

Keep `needs-live-artifacts` and `needs-real-claude` until the return work is committed and verified. The named mobile drive must retain every ordered stdout row from init/control and preceding message/block attribution through result, its effective model/effort/permission settings, actual daemon binary revision, mobile revision, Claude version, explicit zero-drop/tail counters and counted XML results. A sentinel-only log or a passing rerun does not supply the missing rejecting sequence.

On return, promote the deny-scanned mobile sequence to `internal/e2e/realclaude/testdata/tool_then_text_mobile_stream_event_v<observed-version>.json` and portable counted evidence to `internal/e2e/realclaude/testdata/tool_then_text_mobile_evidence_v<observed-version>.json`. Preserve the serial #2270 fixture and this isolated fixture. Add its coupled focused reader in `internal/streamsup/parser_tool_then_text_mobile_capture_test.go`, identify the exact rejecting guard, and commit only the demonstrated parser repair with failing-before/passing-after race proof. Then actually run and pass the unchanged `InteractiveStreamE2ETest#interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy` against the repaired branch, including its status-band and parser-gap assertions. The subsequent full live suite remains dispatcher-owned and pending separately.
