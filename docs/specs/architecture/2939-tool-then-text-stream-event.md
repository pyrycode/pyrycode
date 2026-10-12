# #2939 — diagnose tool-then-text stream-event rejection

## Files read

- `internal/e2e/realclaude/testdata/tool_then_text_mobile_stream_event_v2.1.280.json` and its mobile-evidence companion: the completed managed diagnostic and coupled counted XML.
- `internal/e2e/realclaude/stream_event_tool_then_text_mobile_capture_test.go` → `tttMobileAdmit`, `tttReadMobileFixture`, `tttMobileProofComplete`: faithful source, replay and XML admission remains separate from cause proof.
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


### 2026-10-11 — mobile evidence-production rework

The returned Go gate skipped the isolated producer and never ran the named mobile method. Its records cannot satisfy the mobile return contract. Implement a recorder compiled only with `e2e_realclaude` at `newStreamRunnerFactory`, with an inert default-build seam. It wraps the exact parser input and its event sink, explicitly binds `ControlParser`, keeps the existing posture/initialize configuration, and adds no control requests or model overrides. Each child gets an exclusive 0600 bounded raw file and a manifest after stdout has joined; raw files stay in a test-owned 0700 recovery directory outside the worktree. The manifest records actual build revision/dirty state, observed Claude version, actual spawn argv, parser rejection census, byte loss and unterminated tail. No raw content enters application logs.

A portable Python driver under `internal/e2e/realclaude/` invokes the unchanged named method through the mobile admitted launcher with `PYRYCODE_SRC` and inherited `GOFLAGS=-tags=e2e_realclaude`. It refuses a preselected `PYRY_BIN`, retains fresh counted XML, source/binary revisions and recovery paths, and invokes the coupled tagged Go reader to redact/deny-scan and validate records. The reader requires mobile provenance, the exact executed method, matching clean daemon/mobile revisions and zero-loss records; isolated fixture presence never disarms mobile admission. Diagnostic failure XML is retained as failure evidence, never promoted as a focused pass. Missing mobile evidence fails the scenario gate with an executable handoff instead of skipping because isolated bytes exist. A rejecting mobile sequence must still identify the exact guard before any product parser change.

Files read additionally: `cmd/pyry/streamsup_runner.go` (`newStreamRunnerFactory`), `cmd/pyry/queue_give_up_realclaude.go` (tagged test-control precedent), mobile `scripts/android-test-gate.py` and mobile-agents launcher (`run_on_device`, test-only build from `PYRYCODE_SRC`), and the unchanged `interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy`. The mobile launcher inherits GOFLAGS and capture environment and builds a separate test binary. This host has no adb/emulator; live mobile execution remains a named portable handoff, with both live markers retained.

Concurrency: the stdout forwarder records synchronously; the event sink and spawn logger use one mutex; `OnChildExit` finalizes after stdout joins and resets per-child capture state. No recorder goroutines. Race coverage: `TestToolThenTextDaemonCapture` proves chunk boundaries, exact bytes, per-child replacement and rejection census; `TestToolThenTextMobileAdmission` proves missing/isolated evidence, skipped/wrong-method execution, revision mismatch, loss/tail and stale XML refusal. Existing wrapped-stdout correlation and posture/initialize tests remain required.

Security re-review: PASS. The recorder is absent from normal builds; the tagged opt-in requires an existing owned 0700 directory, creates exclusive 0600 files, bounds captured bytes, and records I/O loss explicitly. Raw recovery is private and never promoted directly. Promotion reuses string-span redaction and the existing credential/dynamic-path deny-scan. Launcher authentication stays with the admitted Android tool; no login/configuration copying or production binary replacement. Errors contain fixed instrument categories only. Build/manifest source values are untrusted and validated by the reader.

Sizing: approximately 700 additional written lines, zero exported types, one factory consumer, three acceptance criteria. Branches #3071 and #3163 overlap package wiring elsewhere but do not touch this factory/capture block. The capture and admission reader have one consumer in this deliverable; the handback floor keeps the accumulated rework together despite the branch total exceeding 800 lines.


- 2026-10-11 (mobile recorder implementation): diagnostic artifact admission and completed proof are separate. A counted mobile failure can retain a deny-scanned diagnostic fixture; a passing rerun still cannot complete the scenario without the guard established by the coupled regression. `EstablishedGuard` stays unset by the producer. The ordinary capture gate now fails loudly when mobile evidence is missing, and fresh captures preserve an existing mobile sequence rather than overwriting it. The portable driver retains original counted XML and passes source revisions to the reader; embedded binary revision, stdout byte count/hash, live/replay rejection equality and zero loss/tail are independently checked. Closure binding occurs after the factory installs its existing child-exit hook, so recording cannot replace permission or retention cleanup. Rework is approximately 950 additional written lines including tests and driver, zero exports and one factory consumer; the one-consumer handback floor still applies. No parser guard changes or mobile assertion changes were made.

Offline validation: recorder/admission/regression race checks pass; the two portable-driver tests pass, including a counted diagnostic failure retained through the admitted command boundary. Scoped default race checks pass for `cmd/pyry` and `internal/streamsup` outside the sandbox's local-socket restriction. Missing-mobile admission deliberately executed and failed once (1 executed / 0 passed / 1 failed / 0 skipped), demonstrating that retained isolated evidence no longer disarms the gate. The actual named Android method has not run on this host (no adb/emulator); this failure is instrument admission, not mobile diagnosis. Live return remains the exact named drive, observed effective settings in init/control/argv, mobile fixture/evidence, coupled cause-backed parser reader/repair and subsequent executed focused mobile pass. Both live markers remain; the subsequent full live suite and documentation stay pending separately.

### 2026-10-12 — managed Android evidence request

The returned gate's sole failure is missing mobile evidence in `TestRealClaude_StreamEventCapture`; it supplies no rejecting parser input. The managed `android-tool-then-text` / `android-emulator` task `f98172ae8af747690d9b6ab50e9cee53ba05e11287d573d3bb36d3d1428597d7` requests the unchanged named method and recorder on pushed candidate `7d1a4a6371aa72c072d20407bb85750c88a8e95c`. It is queued, not a failed or passed native test. Retrieve its exact result with `fleet-task.py await <task-id> --out <fresh-directory>` before promoting any mobile fixture or claiming diagnosis. No parser guard changed in this audit.

Focused offline checks at that candidate: 34/34/0/0 capture/admission race checks, 22/22/0/0 parser replay/reset/refusal/thinking race checks, 1/1/0/0 daemon recorder race check, and 2/2 portable-driver tests. These do not discharge mobile evidence or the subsequent dispatcher-owned full live gate. Security review remains PASS: the request uses the fixed admitted recipe, and artifact promotion still requires the existing redactor, deny-scan and source/binary/XML/loss validation.

### 2026-10-12 — completed managed mobile diagnostic retained

Task `f98172ae8af747690d9b6ab50e9cee53ba05e11287d573d3bb36d3d1428597d7` completed the unchanged named method against daemon `7d1a4a6371aa72c072d20407bb85750c88a8e95c` and mobile `83a277afe045ddc734d513f47aad6aa6e2aeeb63`. The counted XML records 1 executed, 1 passed, 0 failed and 0 skipped. The admitted producer wrote the version-pinned mobile record and its evidence companion. Retain those exact bytes in this branch. The coupled record contains 48 ordered rows through result, zero rejected rows and zero recording loss. Its recorded XML hash and companion agree.

This is diagnostic evidence. The producer left the rejecting guard unestablished. Its existing admission check failed deliberately on the remaining cause requirement. The recorded focused pass does not prove that the historical rejection is repaired. No production parser guard, mobile assertion or proof-completion condition was changed. Preserve the two earlier non-rejecting managed captures in their native task records. Diagnose the original full-run context from these retained inputs before requesting another identical standalone capture.


### 2026-10-12 — historical-context replay and faithful mobile reader

Read additionally the exact historical `Parser.emitStreamEvent` and decode targets in `internal/streamsup/parser.go` at daemon `6019328b378cad587f69b7bc94de37febbdf8556`, and mobile `InteractiveStreamE2ETest.interactiveTurn_toolThenText_statusBandNeverEmptyWhileBusy`, `UnrecognizedRowSentinel`, `UnrecognizedRowRecorder`, `TappingConversationRepository`, `scripts/android-test-gate.py` and `scripts/e2e-emulator.sh` at `5de4f4459`. The stream-event file did not yet exist there: its declarations moved out of `parser.go` in `5b1add10`. Historical decode shapes and rejecting guards match the current handler; parent attribution for content-free thinking is the later difference. The named mobile method is byte-identical at managed mobile `83a277afe045ddc734d513f47aad6aa6e2aeeb63`.

Add a coupled default-build mobile replay reader for the retained 48-row diagnostic. Share the existing isolated replay assertions, retaining separate source/version admission. Check ordered exactly-once text and message/parent attribution, one completed tool start/completion, silent partial tool JSON, content-free retained thinking, no model text in logs and full result-state reset. This proves the captured valid behavior, not a demonstrated parser repair. Run the same reader against a scratch Go overlay containing the exact historical stream-event declarations/functions, without modifying production source. Record the result and exact source provenance in a compact portable diagnostic evidence file.

The original full run selected 64 methods in one shared harness; the named method creates a new chat but its process-global sentinel records every unrecognized row delivered by any collected thread during that scenario. `TappingConversationRepository.observeMessages` records whole lists, including existing rows. Resetting the recorder per method does not establish a row's producer or generation time. Source alone cannot decide whether the historical row came from this prompt, another live child, or a previously retained row. Do not change that sentinel or infer attribution corruption. Historical operator model/effort/permission remain unavailable; all three managed standalone mobile captures used observed haiku/default with effort absent and had zero rejection. Preserve the earlier managed captures in their exact external native task bundles rather than overwriting the version-pinned committed recording.

Scope: about 180 added/changed lines, zero exports, no production call sites, no new rejection branches; the coupled reader has one evidence consumer. No fetched feature branch overlaps the reader/admission files. Concurrency is unchanged: replay writes synchronously. `TestParser_ToolThenTextMobileCapture` covers sequential message replacement, completed text after deltas, tool completion and result reset under race detection. Existing refusal/thinking/reset tests remain required. Open question: obtain an actual rejecting ordered child sequence and its settings/provenance from the original full-run context; standalone passes cannot identify its guard. The subsequent full suite remains dispatcher-owned.

Security re-review: PASS. Historical source and native bundles are read only; raw recovery remains private outside the worktree. The reader consumes only the already deny-scanned fixture; it adds no credentials, subprocesses, network, raw logging or goroutines. Evidence contains bounded revision/settings/count fields and source links only. Keep both live markers and cause admission unchanged. Documentation handoff remains pending and must describe only a cause established by rejecting replay.


### 2026-10-12 — diagnostic results and remaining reproduction gap

`TestParser_ToolThenTextMobileCapture` now consumes the exact committed mobile fixture (SHA-256 `a8e7a5bf5be5581778a8ef559506f1200bd502daaefd063bdb1012946ed5c046`), pins mobile provenance separately from the older source-less isolated records, and shares independent raw-row expectations with their replay reader. `TestToolThenTextMobileRetainedDiagnostic` verifies its coupled XML/revisions and ensures this diagnostic never completes cause admission. The reader's initial race run failed at its old isolated-version admission; this is a reader extension negative control, **not** failing-before/passing-after evidence of a production parser repair.

Current focused parser checks: 20 executed / 20 passed / 0 failed / 0 skipped. Historical-handler overlay: 12/12/0/0, including the mobile reader, both isolated replays and stream-event capture/refusal/result reset. The overlay replaces only the exact historical stream-event decode targets and `emitStreamEvent`/reset functions in the current test harness; it is not execution of the full historical daemon. The original mobile method body SHA-256 is `e34ca90ae4283b6c8842d4aebcd266a0dfb0ce8a1be1f6e85e4c374b2b03d93c` and matches the managed mobile revision byte-for-byte. This rules out missing handler coverage for these retained rows, not for the absent original row.

Scoped `internal/streamsup` race suite: 1476/1476/0/1 (existing opt-in slow deadline probe skipped). Tagged capture/admission race checks: 31/31/0/0. Default vet, tagged realclaude vet and daemon build passed. Counted results are leaf tests, excluding parent subtest aggregates. Rerunning `TestRealClaude_StreamEventCapture` through the admitted launcher executed 1/0/1/0 and failed at `tttMobileProofComplete`'s unresolved-cause requirement after fixture admission/replay; it no longer fails for missing mobile evidence. No Claude child or Android method is launched by that assertion-only rerun.

Recovered the two earlier managed native bundles exactly through `fleet-task.py await`, preserving their terminal failed outcomes and private raw records:

| Native task | Daemon | Ordered rows | Named mobile executed/passed/failed/skipped | Rejections/loss/tail |
| --- | --- | --- | --- | --- |
| `ea8e56e59a3c77fe9f7c59b9ca67b33850220b0ec959e0727d8250c29f3c474a` | `ad751f3c873cd5e2849ca25e39824921861c411a` | 46 | 1/1/0/0 | 0/0/0 |
| `3ef4b193b6241b83592662b124274862ca08afaae0441cccfabd779c1809711e` | `7d1a4a6371aa72c072d20407bb85750c88a8e95c` | 45 | 1/1/0/0 | 0/0/0 |
| `f98172ae8af747690d9b6ab50e9cee53ba05e11287d573d3bb36d3d1428597d7` | `7d1a4a6371aa72c072d20407bb85750c88a8e95c` | 48 | 1/1/0/0 | 0/0/0 |

All three use mobile `83a277afe045ddc734d513f47aad6aa6e2aeeb63`, Claude 2.1.280, observed `claude-haiku-4-5-20251001`/default permission, absent effort, one completed tool start/completion and zero original/retained rejections. Native task failure reports mean cause admission failed; they do not negate the passing counted XML. Portable bundle paths are `mobile/mobile-redacted.json`, `mobile/mobile.xml`, `mobile/driver.json`, and `mobile/daemon-*.stdout` with matching manifests. The two earlier records remain in their native task bundles and private recovery copies; the 48-row fixture and its XML companion remain byte-for-byte unchanged in Git.

Concrete remaining reproduction gap: original `live-nu5iihoe`'s offending ordered child input, producing session/conversation and effective settings are unavailable. The [original diagnosis](https://github.com/pyrycode/pyrycode-mobile/issues/1888#issuecomment-6043311668) supplies only sentinel metadata and full-run counts (64/62/2/0); it cannot identify a stream-event subtype or guard. The shared-harness and whole-thread sentinel context is absent from three independent standalone passes. A relevant next diagnostic must either recover that original sequence or record the full-run context, including all child streams and effective settings, while leaving the sentinel unchanged. The existing opt-in recorder can capture all those child streams; the current fixed managed recipe selects only the named method and cannot establish equivalence to that context. Do not request a fourth identical standalone capture or weaken `tttMobileProofComplete`. Keep both live markers. Cause-backed repair and its failing-before/passing-after race witness remain builder-owned and incomplete; documentation and the subsequent full live gate remain pending separately.
