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

### 2026-10-18 — prospective rebuild after verifier findings 1 and 2

Finding 1 cannot be repaired retroactively. Commit `671607e0` withdraws the complete implementation from `a4116ccf`, restoring `composer_stop_test.go` byte-for-byte to its pre-implementation state. The earlier implementation and retrospective plan remain visible in history. This revision is the prospective design and security review for a replacement implementation; commit it separately before rebuilding. No prior-design claim is made for the withdrawn work.

The replacement retains the Design above: exact foreground Bash input, PID arrival file, absent release file and live non-zombie shell before interrupt. Preserve every background identity, reader, cancellation and exact-task completion assertion. Reintroduce the native hold/release and premature-exit regression first, then implement their witness. A missing witness is a build failure, so also use the replacement gate's PID contract to demonstrate a failing runtime assertion before adding the witness checks.

For finding 2, `composerForegroundHeld` must return a contextual error wrapping the underlying `ps` execution error with `%w`. Empty status and zombie status remain a separate fail-closed error. Add `TestComposerForegroundHeldInspectionError` with a live test-owned PID and an empty test-local PATH: `errors.Is(err, exec.ErrNotFound)` must survive the wrapper. This regression must fail with the earlier combined error branch before that branch is repaired. No new abstraction or subprocess injection is required.

Sizing recheck: one observable staging repair with its regression and diagnosis; approximately 450 total written lines including the withdrawn/rebuilt work and plan, zero new exported types/interfaces, at most five local consumer sites, three acceptance criteria and fewer than ten rejection branches. No concurrent `origin/feature/<ticket>` branch touches `composer_stop_test.go`. The original #3589 branch/base `no-reader` failures and #3602's independent-cause refinement evidence were reread; historical failures remain failures.

Run uncached affected offline tests on macOS with the race detector and `e2e_realclaude`, plus tagged package vet, `go vet ./...` and `go build ./cmd/pyry` to scratch output. Retain red and green logs and per-test counts in the PR. Complete verification and the named live acceptance remain dispatcher-owned; no new skip or Linux check is introduced. Documentation handoff above remains pending.

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

### Prospective rework security review — 2026-10-18

**Verdict:** PASS, before replacement implementation.

- [Trust boundaries] Arrival alone remains insufficient: the replacement requires the exact observed Bash input, an unreleased gate and a live non-zombie shell. Private rig state grants no product authority.
- [Tokens] No authentication or credential changes; native regressions use no Claude login and print no environment.
- [File operations] Rig-generated paths stay within the private workspace, shell-quoted; release writes remain mode `0600` and idempotent.
- [Subprocesses] The rig authors the shell command; PID parsing precedes fixed `ps` argv and signal-zero inspection. Tests cancel and join their own child. The missing-`ps` regression inspects its own PID without starting another process.
- [Cryptography] Existing Noise and key handling are unchanged.
- [Network and I/O] Existing wire/read deadlines and bounded child cleanup remain authoritative; no endpoint or unbounded I/O is added.
- [Errors/logs] Finding 2 is a SHOULD FIX implemented in this rebuild: wrap `ps` errors with operation context and preserve their cause. The missing-`ps` regression checks the cause without printing environment or generated conversation text.
- [Concurrency] No production worker or lock is added. Context cancellation and joined Wait workers bound failure cleanup; test-local PATH restoration is serial.
- [Threat model] Cancelled foreground turn, post-Stop background reader and same-task completion only after rig release remain mandatory. No assertion or skip is weakened.

**Reviewer:** rework builder (self-review per the security-review checklist).
