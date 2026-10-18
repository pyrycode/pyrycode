# #3604: Hold foreground work through Composer Stop staging

## Files read

- `internal/e2e/realclaude/composer_stop_test.go` → `TestRealClaudeComposerStopPreservesBackgroundTask`, `composerForegroundGate`, `composerFileRendezvous`, `TestComposerForegroundGate`: exact-command staging, independent background identity and the existing controlled file-release gate.
- `docs/knowledge/features/e2e-realclaude-test-infrastructure.md` → “Composer Stop survival needs a witness”: completion must belong to the same background task after rig release.
- `docs/specs/architecture/2775-composer-stop.md`: existing foreground file-gate alternative and failure attribution.

## Context

The foreground FIFO arrival could precede a `no-reader` result, before Stop was sent. Repair test staging while preserving all product cancellation and exact-background-task assertions. This is one test-infrastructure behaviour and changes no production code, interface or dependency.

## Design

Reuse `composerForegroundGate` for foreground execution. Match its exact Bash command on the client wire, then observe its arrival file. Record the shell PID in that file; `composerForegroundHeld` requires the release file absent and a live non-zombie shell before interrupt. Keep the background FIFO, `stopHeldTaskSetup` identity join, cancelled foreground turn, post-Stop reader check and exact task's `completed` after background release.

## Concurrency model

No new production goroutine or lock. The live test and native helper tests retain bounded contexts and existing runner cleanup. Gate release is idempotent and registered for failure cleanup. Offline premature-exit cleanup joins its Wait worker before return.

## State transitions and identity reuse

| Event | Proof |
| --- | --- |
| Foreground starts and remains held until rig release | `TestComposerForegroundGate` |
| Arrival file survives an exited shell | `TestComposerForegroundGateDetectsPrematureExit` rejects it |
| Repeated release and failure cleanup | `TestComposerForegroundGate` requires harmless repeated release |
| Stop cancels foreground, retains same background task, then background completes | `TestRealClaudeComposerStopPreservesBackgroundTask` |

## Error handling

Fail closed on absent/invalid PID, premature shell exit, release already present or process-inspection failure. Existing bounded file rendezvous, runner shutdown and background FIFO release remain authoritative. Never treat another task's completion, retained roster or interrupt acknowledgement as survival.

## Testing strategy

Focused offline race checks execute `TestComposerForegroundGate`, `TestComposerForegroundGateDetectsPrematureExit` and `TestComposerHasBashCall`. Complete verification belongs to the dispatcher. Required macOS live acceptance is `github.com/pyrycode/pyrycode/internal/e2e/realclaude.TestRealClaudeComposerStopPreservesBackgroundTask`, executed with zero failures/skips and retained per-test counts. No assertion is weakened and no skip is added.

## Open questions

None.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/e2e-realclaude-test-infrastructure.md`, paragraph beginning “Composer Stop survival needs a witness”, to describe the exact foreground command, file arrival and unreleased live-shell witness while retaining exact-task completion and background-reader proof.

## Revisions

Review follow-up: this missing plan was added after implementation commit `a4116ccf`; the original history is preserved. This commit records the existing reviewed design and required acceptance without claiming the plan preceded the code. No implementation or acceptance change accompanies the plan.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The file arrival is not trusted alone. `TestRealClaudeComposerStopPreservesBackgroundTask` first matches the exact foreground Bash command on the existing client wire, and `composerForegroundHeld` requires an unreleased gate and live non-zombie shell. The witness operates in a fresh private harness workspace; it grants no product permission or cross-session authority.
- [Tokens] Existing authenticated harness and dedicated Claude login handling are unchanged. No credential is copied, persisted or added to diagnostics by this repair.
- [File operations] `composerForegroundGate` places the started and release files under the rig workdir and shell-quotes both paths. Its release file uses mode `0600`; repeated writes and cleanup are harmless. There is no caller-controlled path or production file operation added.
- [Subprocesses] The gate command is rig-authored, and the client observation must match it exactly. The inspection command uses fixed `ps` arguments and a parsed positive integer PID without a shell. Signal zero only observes the witness process. The premature-exit regression kills only its own command and joins its Wait worker.
- [Cryptography] The existing Noise handshake, keys and frame handling are reused unchanged; no new key, nonce or comparison is introduced.
- [Network and I/O] Existing bounded client reads, foreground rendezvous and turn budgets remain authoritative. Process-inspection uncertainty fails staging instead of weakening the cancellation assertion.
- [Errors/logs] Diagnostics name rig state and inspection failure without credentials or generated conversation prose. The test does not print the gate command's process environment.
- [Concurrency] No production worker or lock is added. Gate release remains idempotent, native tests bound and join their children, and live harness shutdown retains its existing cancellation path.
- [Threat model] Composer Stop must still cancel the foreground turn while the exact held background task survives. Existing post-Stop reader and same-task completion-after-release assertions remain unchanged; an acknowledgement or another task cannot substitute for survival.

**Reviewer:** repair builder (self-review per the security-review checklist)
**Date:** 2026-10-18
