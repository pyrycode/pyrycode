# Answerable questions in bypass mode

## Files read

- `cmd/pyry/streamsup_runner.go` → `withApprovalArgs`, `newStreamRunnerFactory`, `settingsInstaller.shape`, `stdioPermissionHandler`: shared startup shaping, parser installation and origin-bound responses.
- `cmd/pyry/streamsup_runner_test.go` → `TestWithApprovalArgs`, installer tests: existing permission and MCP exposure boundaries.
- `cmd/pyry/stdio_permission_test.go` → question, timeout, child-exit and reused-ID tests: existing asynchronous response cleanup.
- `cmd/pyry/stream_approval_test.go` → question answer/refusal/retirement tests: one-shot batch correlation and cleanup.
- `cmd/pyry/question_resolve_v2_test.go` → `TestQuestionResolverV2_GateDeniesBeforeConsume`: device admission stays ahead of resolution.
- `internal/e2e/realclaude/interactive_stream_question_answer_test.go` → `raiseRealQuestionBatch`, `drainAnsweredQuestionTurn`: actual surfaced requests and correlated continuation.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → configurable live daemon harness.
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon`: operator-bypass harness currently supplies stdio itself, masking missing daemon shaping.
- `docs/knowledge/features/streamsup-package.md`, `questionbridge-package.md`, `config-package.md`: transport selection and registry ownership.
- `docs/knowledge/features/e2e-realclaude-interactive-stream-question-answer-test-go.md`: explicitly request multiSelect, select from actual offered labels, drain dismissal and continuation together.
- `docs/knowledge/features/development-verification-captures-and-live-evidence.md`: distinguish actual invocation and answer from tool-list presence; no new retained fixture is required.

## Context

Bypass currently removes the response connection Claude requires to offer selectable questions. Keep host responses independent of permission posture without adding daemon MCP servers to bypass children. This is one behavior change; no decision record is needed.

Sizing: about 650–750 written lines including tests and this plan; no new exported types/interfaces, two production shaping consumers, four acceptance criteria, no new lifecycle state machine. This fits all five builder limits. Overlap: #2939 adds capture wiring elsewhere in `newStreamRunnerFactory`; retain that additive wiring when merged, with no dependency.

## Design

`withApprovalArgs` keeps stored/operator bypass posture and adds exactly one `--permission-prompt-tool stdio` pair, independent of the transport toggle. Normalize existing prompt-tool flags (split/joined) before adding the selected transport so operator arguments cannot compete. Bypass adds no MCP config or permission-mode override. Non-bypass retains its selected MCP/stdio transport and mode.

Install `stdioPermissionHandler` for every Claude stream runner, even when its initial non-bypass transport is MCP: a later settings change can enter bypass without reconstructing the parser. It remains dormant when Claude uses MCP. Keep the existing registry, surfacer, resolver, authorization and child-exit paths. `settingsInstaller.shape` continues to reuse the same function for restart and next spawn; no signature changes.

Remove the live harness's operator-supplied stdio shortcut. New live cases exercise stored/operator bypass crossed with both toggle values, real single/multiselect batches, option answers and free text. Observe init tool availability and confirmed running posture, a correlated answer/dismissal and resumed text, plus a harmless ordinary tool witness with no permission modal. Reuse existing live helpers and capture readers where applicable; transient observations need no committed capture.

## Concurrency model

No new production goroutines or locks. Existing parser registration runs on stdout forwarding; the handler awaits each registry verdict asynchronously and writes only to the captured originating stdin. Child exit resolves outstanding requests; the registry timer and one-shot resolution end waiters. The mode cell stays protected by the installer mutex.

## State transitions and identity reuse

| Event | Race-detector coverage |
| --- | --- |
| Initial/restored startup and either toggle | New `TestBypassQuestionArgs` and `TestStreamRunnerFactory_QuestionHandlerAvailable` |
| Enter/leave bypass, settings restart or next spawn | `TestStreamRunnerInstall_ShapesWithTheLivePosture`, `TestStreamRunnerInstall_ReappliesApprovalArgs`, new argument matrix |
| Respawn/replacement and reused child request identity | New installed-handler test plus existing `TestStdioPermissionHandler_ChildExitRetiresWithoutReplacementWrite`, `TestStdioPermissionHandler_ReusedIDRemainsTrackedForChildExit` |
| Answer/refusal, timeout or disconnected answerer | Existing question bridge single-arbiter/retire tests plus new installed-handler lifecycle cases; registry answerability remains unchanged |
| Late answer after batch retirement | New installed-handler lifecycle cases; existing resolver gate and unknown-batch tests |

## Error handling

Retain fail-closed malformed/duplicate request handling, timeout/disconnect retirement and child-exit denial. No new error branch. Unknown stored modes keep the existing non-bypass enforcement default. Do not auto-approve ordinary tool requests at the daemon: bypass remains Claude's permission policy.

## Testing strategy

Write deterministic regressions first and observe missing stdio shaping/handler failures. Cover both bypass sources and toggle values, split/joined existing prompt flags, unchanged input ownership, and both install paths. Drive the factory-installed parser handler through a helper child and the real bridge, checking answer/refusal/retirement and late-answer isolation. Reuse existing race tests for authorization and registry arbitration.

Run Linux `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build -o /tmp/builder-3515/pyry ./cmd/pyry`, and compile/vet the tagged live package. The dispatcher owns full-module and credentialed live execution, including the new bypass family and existing non-bypass MCP/stdio cases. Require nonzero executed counts; pending live evidence is not a pass. macOS suites are outside this Linux ticket's verification scope.

## Open questions

None. Real Claude bypass question semantics remain a required dispatcher live gate, not an assumed result.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/config-package.md`, section “`stdio_permission_prompt` — interactive permission transport”: state that the setting continues to select ordinary non-bypass approval transport, while stored/operator bypass retain a stdio question-response connection in either setting without enabling ordinary approval prompts or the daemon MCP servers.
- Pending documentation stage: `docs/release-tooling.md`, new section “Selectable questions in bypass mode”: document a post-release smoke check for Desktop on the Mac and Mobile on pyrybox. Inspect each deployed daemon's actual child arguments/effective bypass mode; answer and cancel both single-choice and multiselect questions, including multiple selections and free text, and confirm continuation/cleanup and absence of ordinary approval prompts. Explicitly mark physical smoke results as pending until performed; this checklist is not a requirement to run a second OS suite during this ticket.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `stdioPermissionHandler.handle`, `questionbridge.Parse` and `questionResolverV2` remain the child-input, shape-validation and authenticated-device boundaries; no bypass of remote admission or new wire vocabulary.
- Tokens/secrets: no new credentials or tokens. Existing question registry mints opaque batch IDs; new live observations retain only test-owned answers and selected init metadata.
- File operations: production adds no file operations. Test-owned witness/capture files stay in private temporary directories; no user paths are introduced.
- Subprocesses: MUST FIX addressed in design: adding stdio flags without a parser handler strands questions. Install the handler independent of startup posture; normalize competing prompt flags. Keep bypass MCP exposure unchanged and use argv arrays, never shell interpretation of user input.
- Cryptography: no changes to keys, nonces or AEAD transport.
- Network/I/O: reuse existing bounded stream parser and encrypted relay; no new listener, read loop or socket policy.
- Errors/logs: do not log child question bodies or answers; quote/bound any model-authored diagnostic labels.
- Concurrency: origin-bound stdin and registry one-shot resolution remain authoritative across child exit and repeated IDs; test late answers and actual replacement handling.
- Threat model: existing authenticated encrypted question frames preserve protocol-mobile Security model boundaries; no change to device privileges, relay visibility or rendering trust.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-16

## Revisions

- 2026-10-16: The live harness needs a pre-spawn home preparation hook to seed the existing bootstrap ID with persisted bypass, and opt-in init metadata in `permissionObservation` to observe the actual tool list/posture. Added those local test seams rather than a new capture infrastructure or fixture. Removed the operator harness's injected stdio argument so the daemon supplies the tested connection. Updated the related running-permission test comment to reflect that ownership.
- 2026-10-16: The installed-handler lifecycle test uses a synchronized broadcaster because old-child retirement and replacement-child surfacing may overlap. It drives a connected expiry check followed by disconnect and timeout, in addition to replacement/refusal and late-answer rejection. The original implementation fails all bypass argument rows and the two toggle-false factory rows with the corrected helper launcher; the fixed startup group passes under the race detector. Security findings above are addressed by the design and implementation; no unresolved MUST FIX remains.
- 2026-10-16: Exercise `RestartFresh` for the parked question's replacement and assert the handler's asking-session identity changes; the following ordinary restart reuses that new identity. This covers session replacement and same-session respawn through the installed handler.
