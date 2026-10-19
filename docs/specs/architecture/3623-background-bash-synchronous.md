# Synchronous child Bash in background reply capture

## Files read

- `internal/e2e/realclaude/background_reply_completion_test.go` → `TestRealClaude_BackgroundReplyCompletion`, `backgroundReplyBoundary`, `backgroundReplyRelease`: staging, matching tool/token and recorder-locked release.
- `internal/e2e/realclaude/background_reply_controls_test.go` → `TestBackgroundReplyWitness`, `TestBackgroundReplyReleaseBoundary`: wrong attribution, unsuccessful results and pre-release text controls.
- `internal/e2e/realclaude/background_reply_replay_test.go` → `backgroundReplyReplay`: persistent production-parser equivalence before publication.
- `internal/e2e/realclaude/parent_tool_use_capture_test.go` → `ptucArgs`: retain Sonnet, permission posture and Agent availability.
- `cmd/pyry/interactive_turn_v2_completion_test.go` → `TestRecordedBackgroundReplyCompletion`: unchanged ownership/history/checkpoint consumer.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md` § Test infrastructure: loss-free source order and private stdout must survive.
- `docs/knowledge/features/development-verification-captures-and-live-evidence.md`: only an actual retained live capture can replace fixture bytes.
- `origin/feature/3619` → `TestBackgroundReplyHelperProcess`: existing in-flight PreToolUse hook pattern; physical attribution remains #3619's work.

## Context

Historical #3610 branch, focused rerun and unchanged base each exhausted 180 seconds with `child_result=-1`. Structural inspection of all three private captures confirms the exact held Bash command requested background execution, omitted timeout and returned a successful async-launch notice. Later text cannot replace that command's own successful release-token result. These failures remain failed; host load is not established as a cause. No decision record is needed.

One deliverable: keep Bash synchronous inside the genuinely backgrounded Agent, with its regression and returned replay. Estimate: 400–550 total written lines including plan, controls and eventual capture, zero exported APIs, one runner call site, three acceptance criteria and fewer than ten rejection branches. This stays below all five builder ceilings, including after this plan. Analogue: #3589's structural replay validation and separately promoted capture.

Overlap: #3619 edits this capture family for physical identity; its pending hook can compose this execution-mode constraint. It neither supplies this behavior nor requires a redesign of this local hook addition. #3620 owns wording independence. No dependency is needed.

## Design

Add `background_reply_sync_test.go` in the tagged realclaude package. `backgroundReplySyncArgs(t, work) []string` copies `ptucArgs`, installs a private executable hook launcher and appends a JSON `--settings` value. Only `PreToolUse` matching Bash is configured; Agent tools remain available and backgrounded. The launcher re-execs the current test binary in a dedicated helper-process test.

The hook reads at most 1 MiB of JSON stdin, accepts only a Bash object with string command, and emits `hookSpecificOutput` / `PreToolUse` / `updatedInput`. Preserve command and other fields without evaluating them; overwrite `run_in_background=false` and `timeout=240000` milliseconds. The timeout exceeds the existing 180-second capture window so automatic timeout handoff cannot preempt the witness. Do not disable background tasks globally or change model/permissions. A pass-through version of the helper will establish the red regression before adding the overrides.

Keep token equality, child/tool attribution, release ordering, loss/partial rejection, sanitization and parser-equivalence assertions unchanged. Add an explicit async-notice witness rejection control. Live capture continues to run normally and publish its deny-scanned replay even when an older fixture exists. The old committed replay remains historical until the dispatcher returns usable replacement bytes.

## Concurrency model

No new goroutines or shared mutable state. Each hook process handles one input and exits. Existing runner cancellation and bounded join remain; hook and regression subprocesses have bounded contexts. Hook files belong to the isolated test workspace, with cleanup after runner exit.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Repeated hook invocation with true, false or absent background flag | `TestBackgroundReplySyncHook`: independent processes retain command/metadata and consistently force synchronous execution. |
| Parent Agent remains backgrounded while Bash settings are installed | `TestBackgroundReplySyncHook`: Bash-only matcher and unchanged base argv; `TestBackgroundReplyWitness`: actual background launch boundary. |
| Async notice followed by another tool's token or pre-release final | `TestBackgroundReplyWitness`: cannot replace the matching successful result; existing release-order cases retained. |

## Error handling

Malformed, oversized, missing or wrong-shaped hook inputs terminate nonzero without echoing input or decoder errors. Launcher creation/encoding failures stop the test with fixed diagnostics. No command string is executed by the hook. Existing bounded runner cleanup handles capture failures.

## Testing strategy

First run the new helper-process regression with pass-through input and observe that explicit background=true remains true; then add both execution overrides and require success. Exercise true/false/absent input, metadata/command preservation, malformed/oversized/missing fields and shell-sensitive launcher paths. Run affected tagged `TestBackgroundReply*` controls and recorded completion/child pipeline tests with race detection on macOS. Run tagged vet to compile all live helpers, `go vet ./...` and `go build ./cmd/pyry`. Full-module verification and live acceptance belong to the dispatcher.

The dispatcher must execute `TestRealClaude_BackgroundReplyCompletion` and retain executed/passed/failed/skipped counts. On artifact return, commit only its identified deny-scanned `internal/e2e/realclaude/testdata/background_reply_completion.json` and rerun `TestRecordedBackgroundReplyCompletion`. No live pass or replacement capture is claimed in this first handoff; use `needs-live-artifacts` and retain `needs-real-claude`. Linux is outside this ticket's verification scope.

## Open questions

None for implementation. Live hook integration and fresh replay remain pending dispatcher evidence, rather than an assumed pass.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, under “Test infrastructure”, state that the Agent is backgrounded while its held Bash operation executes synchronously, and that an async-launch notice cannot prove successful release-token output. Preserve the existing private-capture, source-order and loss requirements.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The hook accepts model-supplied JSON only as data. Validate Bash/object/string shape and a 1 MiB cap before emitting an update; it does not establish physical attribution (owned by #3619).
- [Tokens] Existing `rand.Text` release token stays private; hook neither reads nor logs it. Published replay uses the existing scrubber and deny-scan.
- [File operations] Launcher uses a rig-owned path in isolated workspace, mode 0700. No model field selects a path. No new persistent state or atomic publication contract.
- [Subprocesses] SHOULD FIX: shell-quote the test-binary path and test spaces/apostrophes/metacharacters. Never interpolate model input into the launcher; re-exec with fixed helper selection and a bounded context in controls.
- [Cryptography] No new cryptographic operation; preserve existing random token generation. Equality is an internal capture witness, not an authentication oracle.
- [Network and I/O] No network listener or credential change. Reject oversized input before decoding and emit no raw input on errors.
- [Errors/logs] Fixed helper failures and content-free test assertions prevent command/token leakage into reports. Original stdout remains private.
- [Concurrency] Existing recorder lock/release ordering is untouched. Stateless helper exits after one request; runner retains cancellation and bounded join.
- [Threat model] Test staging only, with existing authenticated isolated HOME and unchanged permissions. Production relay/mobile authorization and physical witness work are outside this repair; #3619 owns the latter.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-19
