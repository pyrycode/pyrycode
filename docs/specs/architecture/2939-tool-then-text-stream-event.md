# #2939 — diagnose tool-then-text stream-event rejection

## Files read

- `internal/streamsup/parser_stream_event.go` → `emitStreamEvent`, `resetStreamEventState`: decoding, subtype and attribution rejection guards.
- `internal/streamsup/parser_assistant.go` → `emitAssistant`: completed text suppression and completed tool ownership.
- `internal/streamsup/parser.go` → `consumeLine`, `emitUnrecognized`: result reset and content-free diagnostics.
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
