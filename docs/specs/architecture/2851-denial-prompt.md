# #2851: Bound the live permission-denial prompt

## Files read

- `internal/e2e/realclaude/interactive_stream_permission_deny_test.go` → `driveInteractiveStreamPermissionDeny`, `denyModalsUntilIdle`, `restartLiveChild`: three callers share attributed rejection, bounded retries, terminal idle and the file-absence witness; the respawn arm waits for settings acknowledgement and a changed PID.
- `internal/e2e/realclaude/harness_modal_test.go` → `writeFileTrigger`, `raiseRealPermissionModal`: the shared prompt has only a completion path; the modal helper requires a genuine permission modal.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `TestInteractiveStreamStdioCancelDeniesParkedPermission`: the #2416 denial instruction to reuse locally.
- `docs/knowledge/features/e2e-realclaude.md` § Test infrastructure: denial prompts must prevent retries that can look like a permission-path hang.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: count non-skipped results; the dispatcher owns the full live gate.
- `CODING-STYLE.md`: existing behavioral tests and symbol-based citations.

## Change

Append a denial-specific instruction to the `writeFileTrigger` result inside `driveInteractiveStreamPermissionDeny`: if denied, do not retry, do not use another tool, and reply with one short word. Keep the shared trigger unchanged for allow tests. Keep the four-retry cap, reply deadline, genuine-modal check, remote/reject_once attribution, idle check and recursive file-absence assertion. Keep the settings acknowledgement and PID-confirmed respawn sequence unchanged.

The [refinement evidence](https://github.com/pyrycode/pyrycode/issues/2851#issuecomment-6000600746) identifies missing denial guidance as the test defect supported by current logs. In `2026-10-05T18-06-38-008Z_real-claude-gate_#2832.log`, first denial resolves at 18:13:11.585Z, four retries are denied, and the fifth retry trips the cap at 18:13:25.599Z. The subsequent cleanup timeout is an effect of failure. The same-tree rerun log reaches idle at 18:22:51.071Z with zero retries. This demonstrates variable model retry behavior; no external defect or blocker is established.

One deliverable, approximately 55 written lines including this plan, no exported types, no consumer signature updates, two acceptance criteria, and no new reject branches. No other fetched feature branch touches the changed file.

## Testing strategy

The existing failed #2832 run supplies the pre-change failure evidence; a literal prompt edit needs no mirrored unit assertion. Run race-enabled offline tests for the tagged realclaude package, `go vet ./...`, tagged package vet and `go build ./cmd/pyry` with its output in scratch storage. Use the restricted live launcher for five consecutive non-skipped passes of `TestInteractiveStreamPermissionDenyAfterSettingsRespawn` and one non-skipped pass of each sibling, `TestInteractiveStreamPermissionDeny` and `TestInteractiveStreamStdioPermissionDeny`. Existing assertions prove the modal, attribution, terminal idle, witness absence and respawn. Keep `needs-real-claude`; the full live and full-module gates remain dispatcher-owned.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The prompt is test-authored input, not an authorization rule. `raiseRealPermissionModal` and the dismissal checks in `driveInteractiveStreamPermissionDeny` retain independent proof that a real gate surfaced and the authorized remote rejection resolved it.
- [Tokens, secrets, credentials] No credential or token changes. Live runs use the restricted launcher, which obtains and redacts credentials within its child process.
- [File operations] The nonce-named target remains in the isolated harness workspace; `requireTriggerFileAbsent` still walks it after idle. No new file operations or path input are introduced.
- [Subprocesses] The existing harness and `restartLiveChild` retain process ownership and cleanup. No new command, environment inheritance or shell interpretation is added.
- [Cryptography] No primitive, key or Noise nonce changes; existing frame helpers remain in use.
- [Network and I/O] No new socket or parsing behavior. `denyModalsUntilIdle` retains its retry cap and wall-clock deadline.
- [Errors, logs, telemetry] Existing failure diagnostics remain; the prompt contains only a test filename and instructions, with no sensitive input.
- [Concurrency] No goroutines or state transitions are added. Settings acknowledgement and changed-PID polling still precede the denial driver.
- [Threat model] Remote-denial fail-open and timeout false-pass scenarios remain covered by the genuine modal, remote/reject_once attribution and post-idle file-absence checks. This prompt cannot replace daemon authorization.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
