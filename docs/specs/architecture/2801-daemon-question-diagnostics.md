# Daemon question resolution diagnostics (#2801)

## Files read

- `cmd/pyry/question_resolve_v2.go` → `questionResolverV2`, `admit`, `auditQuestion`: eligibility ordering and audit ownership.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge`, `AnswerQuestion`, `RefuseQuestion`, `answerVerdict`, `retireQuestion`: validation before one-shot consumption and sole dismissal ownership.
- `cmd/pyry/relay.go` → `startRelayV2`: concrete resolver already installed unconditionally; actuator assigned before dispatch starts.
- `cmd/pyry/question_resolve_v2_test.go` → `questionArms`, `gatedResolver`: authorization, audit and content-free tests to retain.
- `cmd/pyry/stream_approval_test.go` → `questionFixture`, `surfacedQuestion`, parked-input-gone and single-arbiter tests: reuse real registries and dismissal synchronization.
- `cmd/pyry/session_memory_search_test.go` → `TestMemorySearchFor_SettingsAlwaysCarryReport`: existing authenticated relay handshake pattern.
- `internal/relay/v2session_seams.go` → `DiagnosticQuestionResolver`: optional diagnostic contract merged by #2800.
- `internal/relay/v2session_question.go` → `handleQuestionAnswer`, `handleQuestionRefusal`: one selected resolution attempt; relay owns receipt/completion logging.
- `internal/questionbridge/registry.go` → `Lookup`, `Resolve`, `Record`: no tombstones; consume is atomic under the registry mutex.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md` → Diagnostic logging and Testing: reasons must be constants; failed delegates must skip auditing.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md` → The question arm: detached dismissal avoids deadlocking relay dispatch.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: test precedence with competing failures and use distinctive content sentinels.
- `docs/protocol-mobile.md` → Security model: authenticated input, replay defense and content confidentiality remain the existing boundaries.
- `CODING-STYLE.md`: table-driven tests, leaf mutexes and error handling.

## Context

An unanswered phone attempt currently supplies only a boolean to the relay. This
slice identifies the actual first daemon check that declined consumption, using
#2800's terminal record. Diagnosis, a hang fix and live proof remain with #2802.
No decision record is needed. No overlapping feature branches touch the planned files.

Sizing: one deliverable; four acceptance criteria; approximately 650 written lines
including this plan and tests; no new exported types/interfaces; no consumer call
sites requiring simultaneous changes outside these two production files; nine
distinct refusal checks, within the ten-branch limit. Existing bool bridge callers
remain usable, including the analogue #1986's fixtures and behavior tests.

## Design

`ResolveAnswerDiagnostic` and `ResolveRefusalDiagnostic` return `(bool, string)`
for one attempt. Their bool counterparts call them once and discard the reason.
`admit` returns eligibility and a reason; the actuator consumer uses diagnostic
bridge methods, which likewise own the implementation behind bool wrappers.
There is no fallback second attempt and no new lower-layer reason log.

Both arms preserve this ordered prefix: missing actuator → `no_actuator`;
registry miss → `unknown_or_retired_batch`; failed device gate →
`unauthorized_device`. Lookup cannot distinguish never-known from retired IDs.
Answers then check allow authorization → `allow_authorization_failed`;
question/tool-use correlation → `missing_correlation`; parked permission request
→ `missing_parked_request`; batch validation lookup →
`missing_batch_during_validation`; answer verdict → `verdict_rejected`;
one-shot consume → `resolution_lost`. Refusals check correlation and then consume
with the same respective codes. Success returns `resolved`.

Audits stay at their current checks and successful decisions. Validation failure
leaves the batch answerable. A permission resolve miss after batch consumption
remains successful and dismisses. The bridge remains the sole dismissal arbiter.
Narrow the bridge's question registry field to its existing Record/Lookup/Resolve
operations so tests can deterministically simulate concurrent retirement between
checks, and expiration after consumption, without timing-dependent race tests.

## Concurrency model

No new production goroutines or locks. Relay resolution runs synchronously on
the Run goroutine. Registry and correlation mutexes remain leaf locks. Existing
detached broadcasts finish after fan-out or daemon cancellation. Check-then-act
is intentionally non-atomic; the registry consume remains the single arbiter.

## Error handling

Each decline returns the fixed code for its first failed check. No payload or
decoder error becomes a reason. Permission expiration after consume is not a
failed resolution. Nil actuator remains inert before any registry access.

## Testing strategy

Write table-driven reason tests first. Cover both gate arms, unknown/retired/empty
IDs, each reachable bridge miss, invalid answers remaining answerable, precedence
with multiple failures, lost one-shot and successful consume with expired
permission. Compare bool and diagnostic verdicts, audits and one dismissal.
Use a registry wrapper for deterministic validation/consume races, and an actuator
double to prove failed delegation performs exactly one attempt and no audit.
An authenticated relay-facing test supplies the real daemon resolver for both
frame kinds, success and misses including empty IDs; assert one receipt and one
terminal Info record, IDs, literal reasons and absent content sentinels.
Run focused tests red before implementation, then race tests on `cmd/pyry` and
`internal/relay`, `go vet ./...`, and `go build ./cmd/pyry` with output outside
the worktree. The dispatcher owns the full-module gate.

## Open questions

None. The defensive allow check cannot fail after today's admission predicate;
give it a reason without adding an artificial authorization hook.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md`, `Diagnostic logging`: list the implemented daemon reason codes, their checks and answer/refusal applicability, explain the unknown-or-retired limitation, and replace the statement that the daemon still uses the legacy path.
- Pending documentation stage: `docs/knowledge/features/v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md`, `The question arm`: describe diagnostic reporting for inert misses and preserve the single-arbiter explanation.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings: `admit` retains eligibility before delegate consumption; `answerVerdict` retains the index/count/shape boundary for untrusted answers.
- [Tokens, secrets, credentials] No findings: diagnostic methods never inspect answer tokens; codes are literal strings and tests retain distinctive token/content sentinels.
- [File operations] No findings: no production file I/O is added.
- [Subprocesses] No findings: no subprocess launch or environment change is added.
- [Cryptography] No findings: existing authenticated Noise dispatch and registry nonce generation are unchanged.
- [Network and I/O] No findings: existing relay framing bounds and dispatch remain unchanged; the methods add only bounded local return values.
- [Errors, logs, telemetry] No findings: relay alone logs the reason with escaped correlation IDs, including empty IDs; audits remain separate. No raw input or error text is included.
- [Concurrency] No findings: no new lock ordering; registry Resolve remains the one-shot authority and detached broadcasts retain cancellation paths.
- [Threat model] No findings: diagnostics confer no authorization and send no additional reply. Existing prompt-injection, metadata and DoS limitations remain unchanged. Investigation and live proof are owned by #2802.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
