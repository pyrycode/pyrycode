# #3620 — recognize background completion independently of wording

## Files read

- `internal/e2e/realclaude/background_reply_completion_test.go` → `backgroundReplyBoundary`, `backgroundReplyRelease`, live scenario: recognition and recorder-locked release order.
- `internal/e2e/realclaude/background_reply_replay_test.go` → `backgroundReplyReplay`: persistent parser comparison and privacy normalization.
- `internal/e2e/realclaude/background_reply_controls_test.go` → `TestBackgroundReplyWitness`, replay controls: correlated tool evidence and partial-block suppression.
- `internal/e2e/realclaude/background_reply_identity_test.go` → `TestBackgroundReplyIdentity`: physical operation/Agent/Bash identity checks remain intact.
- `internal/streamsup/parser_assistant.go` → `emitAssistant`: intrinsic completion requires one nonblank attributed assistant text block; an open partial companion can suppress it.
- `cmd/pyry/interactive_turn_v2_completion_test.go` → `backgroundCompletionRecord`, `TestRecordedBackgroundReplyCompletion`, `testChildCompletionPipeline`: retain canonical text and end-to-end identity/order checks.
- `internal/e2e/realclaude/testdata/background_reply_completion.json`: historical Linux physical-hold capture remains historical until fresh artifact return.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md` § Test infrastructure: preserve physical witness, private capture, full-sequence parsing and loss accounting.
- `docs/knowledge/features/development-verification-captures-and-live-evidence.md`: observed bytes and synthetic replay values need truthful provenance; capture output must survive the gate checkout.

## Context

Exact model prose can reject valid background completion. This is a source-audit risk, not a reproduced live failure. #3619 / PR #3626 is merged in the starting tree and supplies the synchronous physical hold. One useful behavior ships here: recognize and retain genuine completion independent of wording, with its regression controls. No production semantics or decision record changes.

Sizing: approximately 450–550 written lines including plan, controls, implementation and eventual replacement artifact; 0 new exported types/interfaces, 0 simultaneous consumer signature updates, 4 acceptance criteria, at most 10 distinct recognition reject categories (identity, main failure/order, result correlation, blank/missing response, malformed shape, parser incompletion, partial suppression, repeated response identity, release order, invalid replay selection). Existing analogue `be88f46c` added 114 control/replay lines. Remote feature branch inspection found no overlapping in-flight edits to the three target test files.

## Design

Keep `backgroundReplyBoundary`'s interface. Feed the entire captured sequence through one production parser per inspection. Recognize a final only from a nonblank `TextChunk` with intrinsic completion, matching the attributed assistant frame's message and spawning Agent. Retain the first response observation per child message identity, so an earlier response cannot gain a later source position through replay. Main success must follow launch and must not carry `is_error`; preserve physical witness and successful private-token Bash result correlation. `backgroundReplyReleased` continues enforcing recorder-lock release, main-result, child-result and final ordering.

Keep persistent original/scrubbed parsers in `backgroundReplyReplay`. At the selected final source index require the original completed child text event, then substitute its single assistant text with `pyry-3589-finished`. Normalize only that selected completed text event to the same synthetic value when comparing traces; preserve message/parent identity, completion, event multiplicity, order and all other relevant fields under the existing privacy normalization. Require the scrubbed parser to emit the corresponding completed event at that same index. Other observed prose remains private under the existing scrubber. Reject an absent/out-of-range selection. Leave the canonical consumer assertions unchanged.

Remove the live prompt's exact child phrase requirement. Mark canonical completion text synthetic in newly produced provenance; do not alter historical capture provenance. Preserve token validation, deny-scanning, physical correlation, loss accounting and durable gate output.

## Concurrency model

No new goroutines or shared mutable state. Each boundary/replay invocation owns its parser and response-identity tracking. The existing runner is cancelled and joined during cleanup. `backgroundReplyRelease` retains the recorder mutex across source-boundary observation and release-file creation.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Main turn succeeds and held child produces a release-token result, followed by varied nonblank response | `TestBackgroundReplyWitness`, wording controls |
| Response arrives before release/result and the same message is replayed later | `TestBackgroundReplyWitness`, wording controls |
| Partial block remains open, then closes before settled assistant response | `TestBackgroundReplyReplayOpenPartial`, wording controls |
| Wrong child/tool/result or failed main/result cannot establish completion | `TestBackgroundReplyWitness`, `TestBackgroundReplyIdentity`, wording controls |
| Private observed completion becomes synthetic text at the same source index | wording replay controls and `TestBackgroundReplyReplayTaskContext` |
| Full recorded sequence reopens through history/checkpoint with identical terminal identity | `TestRecordedBackgroundReplyCompletion` |

## Error handling

Unusable shapes and incomplete parser events cannot establish a completion boundary. Replay failures use content-free errors with source indices only; never print observed text, tokens or parser payloads. Malformed captured JSON cannot publish a replay. Retain all original live evidence privately even on failure.

## Testing strategy

Write focused controls first and observe failures for punctuation/narration/other wording against the old recognizer. Cover empty/whitespace/missing/wrong-child/malformed/multi-block/incomplete and partial-suppressed replies, failed/wrong-token/wrong-tool results, main failure/order, pre-release replies and duplicate message reuse. Positive replay controls assert exact canonical text, private-prose exclusion and source/identity preservation. Retain existing physical-operation controls.

On Linux run admitted tagged race tests selected by `^TestBackgroundReply`, the admitted `^TestRecordedBackgroundReplyCompletion$` race regression in `cmd/pyry`, tagged compilation via `go vet -tags e2e_realclaude ./internal/e2e/realclaude/...`, ordinary `go vet ./...` and `go build ./cmd/pyry` (output outside the worktree). Full-module suites belong to verifier/dispatcher; macOS is outside this ticket's verification scope.

Dispatcher live acceptance remains pending: execute `TestRealClaude_BackgroundReplyCompletion` on Linux at the candidate revision; retain OS/revision and executed/passed/failed/skipped counts with nonzero execution and no skipped scenario. Retain deny-scanned `PYRY_GATE_EVIDENCE_DIR/background_reply_completion.json`. On builder return commit its exact usable bytes to `internal/e2e/realclaude/testdata/background_reply_completion.json`, rerun the focused recorded regression and push before removing `needs-live-artifacts`.

## Open questions

None. Canonical replay text stays `pyry-3589-finished`; it is not a requirement on observed model output.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, “Test infrastructure”, state that completion is determined by attribution, nonblank text, correlated release/tool ordering and intrinsic parser completion rather than exact model wording; published canonical text is synthetic, not verbatim observed prose. Preserve physical-witness and private-capture requirements.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `backgroundReplyBoundary` trusts only parser-confirmed attributed completion plus the existing physical/Agent/Bash and release-token evidence. Replay normalization must preserve ownership, completion and source ordering; controls cover wrong-child and partial suppression.
- [Tokens, secrets, credentials] `rand.Text` supplies the private release token. Keep it only in existing private capture/release files; replace it with `rig-release-observed` only after exact validation. No new credentials or token lifecycle.
- [File operations] Existing rig/private directories are `0700`, capture/release files `0600`, and fixture/evidence paths are test-owned. No new caller-controlled paths, symlink behavior or persistent production writes.
- [Subprocesses] The rig's existing hook and synchronous physical operation remain unchanged; only the rig-authored model instruction changes. Existing cancellation/join cleanup remains responsible for the runner.
- [Cryptography] No cryptographic changes. Token comparison is local test evidence, not an authentication endpoint.
- [Network and I/O] No new network surfaces. Existing recorder caps and loss rejection remain mandatory before publication.
- [Errors, logs, telemetry] SHOULD FIX: varied completion prose must never pass through the fixed vocabulary as observed public text at the selected source. Explicit canonical replacement and sentinel privacy controls cover this; fresh provenance must label it synthetic. Errors remain content-free and the deny scanner remains mandatory.
- [Concurrency] Parser/identity state is invocation-local; preserve recorder-lock release ordering and existing runner shutdown.
- [Threat model] Test-evidence spoofing and accidental publication of private model prose are relevant here and covered above. Production relay/mobile authentication and cryptography are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-19
