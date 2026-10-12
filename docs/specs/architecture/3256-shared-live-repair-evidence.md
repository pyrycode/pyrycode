# Shared live permission/question failure repair evidence

## Files read

- `cmd/pyry/prompt_answer_history.go` → `recordPromptAnswer`, `promptAnswerPublisher.enqueue`, `promptAnswerPublisher.deliver`: landed recording/delivery boundary.
- `cmd/pyry/prompt_answer_relay_test.go` → `TestPromptAnswerRelayRemainsResponsive`, `TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer`: five decisions, continued relay progress, held delivery, cancellation and receipt identity.
- `cmd/pyry/prompt_answer_receipts_test.go` → `TestPromptAnswerReceiptsPublication`, `TestPromptAnswerReceiptsConcurrent`, `waitPromptReceipts`: existing ordered publication/replay proof.
- `cmd/pyry/modal_resolve_v2.go` → `resolveCapturedStreamDecision`, `AnswerQuestionDiagnostic`, `RefuseQuestionDiagnostic`: only winning parked resolutions record answers.
- `cmd/pyry/relay.go` → `startRelayV2`: publisher/ring/bridge installation before manager dispatch starts.
- `internal/relay/v2session_conns.go` → `ActiveConns`, `activeConns`: snapshot requests require the manager dispatch loop.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go` → `startPermissionModalResolutionHarness`, `startObservedPermissionHarnessWithOperatorBypass`: shared real permission/question stack with authorized phone and isolated child.
- `internal/e2e/realclaude/harness_modal_test.go` → `spawnPermissionDaemon`: optional readiness binary/MCP fixture overrides change suite inputs.
- `docs/knowledge/features/development-verification.md` § Test execution and artifact survival: compare suite/fixture inputs and count executed tests rather than exit codes.
- `docs/knowledge/features/history-package-producers.md` § Resolved remote prompt answers (#2973), and `history-package-producers-legacy-compatibility.md`: original-owner and winning-resolution contracts; synchronous publication prose needs the documentation handoff below.
- `docs/knowledge/features/e2e-realclaude.md` § Build tag, Test infrastructure, Make target: tagged compilation is separate from authenticated live acceptance.
- `CODING-STYLE.md`: source-symbol citations, scoped tests and concurrency cleanup conventions.

## Context

The [original #3071 gate report](https://github.com/pyrycode/pyrycode/issues/3071#issuecomment-6122421712)
records 2082 executed leaves, 2066 passed, 16 failed and 29 skipped on candidate
`9fa054eaf04f55133c742971c7e01f379fbf37ff` against base
`e59635679fe668f18b199944410c9ca1544956b9`. Fourteen failures also reproduced on
base; the two readiness failures belong to #3071.

The [recovery finding](https://github.com/pyrycode/pyrycode/issues/3071#issuecomment-6130386447)
establishes the common boundary: manager dispatch handles modal/question answers,
`recordPromptAnswer` synchronously calls `publishLegacyHistory`, and that fan-out
waits for `ActiveConns` on the same manager loop. The answer reaches the child,
but the manager cannot return to dispatch dismissals, further traffic or snapshots.
The old producer blob is identical on candidate and base:
`994ebb831fd11c74ffe6d6309bd9adde7f28767b`.

[PR #3279](https://github.com/pyrycode/pyrycode/pull/3279) repaired that boundary at
`9c9cd5a25c337666bdbed47c29202e695a172733`. The starting checkout and fetched main
are `8d1281c3b56954bf070db8cff1e9abbbd1686dc1`, include that merge by ancestry,
and retain its producer blob `bf170cac1ce4d461532b5b1988e8cdf6f3081bb5` unchanged.
Its existing regressions already distinguish the deadlock for all five decisions.
No same-cause residual has been evidenced, so this ticket reuses the repair rather
than introducing speculative production changes or duplicate tests. No new ADR is needed.

The original full-suite and baseline reruns had different suite composition;
candidate added readiness tests/captures and changed `spawnPermissionDaemon` to
honor `PYRY_E2E_READINESS_BIN` / `PYRY_E2E_READINESS_MCP_CONFIG`. The modal harness
and producer are unchanged between those historical revisions. Portable reports
do not preserve the complete runtime fixture/environment inputs or Claude version;
the host logs are absent locally. This supports the independently localized source
deadlock, but does not establish that every historical failure had that cause or
that repaired current main passes the fourteen live cases. The full gate must
identify the actual tested daemon revision, including any binary override.

## Design

Keep #3279's contract: serialize one durable append and replay admission under
the publisher mutex, enqueue into the bounded ordered mailbox, and fan out via one
worker outside that mutex. Overflow drops only live delivery; durable history and
replay remain. Existing receipt bytes, original history/event identities, device
authorization, child verdicts and one-shot arbitration remain unchanged.

Commit this plan before validation. Record counted hermetic results in a subsequent
plan revision and open a PR linking #3256. Change production or add a focused test
only if validation exposes an evidenced residual in this boundary. Never weaken,
skip or extend timeouts in the named live tests. Independent failures remain
separate tickets only after their cause is established.

Sizing: one repair-evidence deliverable, three acceptance criteria, approximately
150 written lines, zero new exported types, zero consumer updates and zero new
reject branches. The ~250-line refiner estimate allows a residual if evidenced.
The feature-branch overlap check found no edits to the producer, its two regression
files or shared modal harness. This evidence-only design has no dependency on
unmerged work.

## Concurrency model

No new goroutines or locks. Existing `recordPromptAnswer` holds only publisher
serialization across append/replay admission. Its sole delivery worker releases
that lock before `ActiveConns` / `Push`, owns the envelope counter, and exits on
empty mailbox or context cancellation. Manager snapshots can therefore complete.

## State transitions and identity reuse

No new lifecycle state; retain and rerun the existing race coverage:

| Event | Race-detector test |
| --- | --- |
| Permission allow/deny/cancel and question answer/refusal, then another connection | `TestPromptAnswerRelayRemainsResponsive` |
| Second answer while connection discovery is held | `TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer` |
| Cancel held delivery while retaining both history/replay entries | `TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer` |
| Ordered original history/event identity after releasing delivery | `TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer` |
| Concurrent duplicate answers and original ownership after rebinding | `TestPromptAnswerReceiptsConcurrent`, `TestPromptAnswerReceiptsPublication` |

## Error handling

Keep existing best-effort storage, fixed overflow warning and cancellable delivery.
Do not retry a consumed answer or invent a receipt after a failed append. Report
hermetic failures with named results; a missing live result remains pending and
cannot be represented as a pass.

## Testing strategy

Run both named repair regressions with `go test -race -count=1 -json`, count executed
leaves, then `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build` for
`./cmd/pyry` with output outside the worktree. Compile/vet the tagged real-Claude
package offline. The verifier owns the full-module hermetic gate.

The dispatcher owns the full real-Claude gate on the pushed candidate based on
repaired current main. Keep `needs-real-claude`. Require per-name PASS, no skip or
absence, actual source revision and Claude version, and executed/passed/failed/skipped
counts for all fourteen exact leaves below in `internal/e2e/realclaude`:

- `TestInteractiveStreamDefaultPostureOutsideWorkspaceRead`
- `TestInteractiveStreamStdioAlwaysAllowIsSessionScoped`
- `TestInteractiveStreamSessionSettingsReportsConfirmedPermissionMode`
- `TestInteractiveStreamStdioModalResolution/absent`
- `TestInteractiveStreamStdioModalResolution/working_directory_reason`
- `TestInteractiveStreamStdioCancelDeniesParkedPermission`
- `TestInteractiveStreamStdioModalAllow`
- `TestInteractiveStreamPermissionDeny`
- `TestInteractiveStreamModalResolution`
- `TestInteractiveStreamPermissionDenyAfterSettingsRespawn`
- `TestInteractiveStreamQuestionAnswer`
- `TestInteractiveStreamStdioPermissionDeny`
- `TestInteractiveStreamSendFile`
- `TestInteractiveStreamQuestionRefusal`

A focused pass or merged repair cannot satisfy that live acceptance criterion.

## Open questions

Does scoped hermetic validation evidence a same-cause residual? Resolve during
this build. Whether all fourteen live leaves pass remains dispatcher-owned work.

## Documentation handoff

Satisfied by the documentation stage on 2026-10-12:

- In `docs/knowledge/features/history-package-producers.md`, **Resolved remote prompt answers (#2973)** / **Serialize successful append and publication together**, replace synchronous fan-out with the landed contract: append/replay admission stay serialized; one bounded ordered worker delivers outside the recording lock; mailbox overflow drops only live delivery, retaining history/replay; cancellation frees delivery without blocking later answers.
- In `docs/knowledge/features/history-package-producers-legacy-compatibility.md`, the paragraph beginning **Prompt answers also bypass `emit`**, make the same contract correction.
- Both updates must distinguish the repair evidence from pending live acceptance.

Both passages now describe the landed worker contract and distinguish the
race regressions from separately counted live acceptance. The binary/fixture
override lesson is also recorded in `development-verification.md` under
**Test execution and artifact survival**. No documents were added or removed,
so the catalog and startup map need no change.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings: `modalResolverV2` and `questionResolverV2` retain device authorization before bridge actuation; this evidence-only change adds no admission path or permission bypass.
- [Tokens, secrets, credentials] No findings: no credential acquisition or changes; the dispatcher owns authenticated live testing. Only source IDs and counted results enter the plan/PR.
- [File operations] No findings: only this committed architecture artifact is written in the product tree; check output uses a scratch directory. History append/storage contracts stay intact.
- [Subprocesses] No findings: only normal Go checks run here, with no user input passed to a shell or new child lifecycle. The live harness retains its isolated daemon cleanup.
- [Cryptography] No findings: no primitive, key, nonce or comparison changes; existing encrypted-phone regression exercises the authenticated manager path.
- [Network and I/O] No findings: no listener/parser changes. `enqueue` retains its 256-entry pending bound and durable/replay admission before overflow handling.
- [Errors, logs, telemetry] No findings: the existing fixed overflow warning contains no prompt/answer content; this branch introduces no operational logging or raw report dump.
- [Concurrency] No findings: the recording lock is released before manager snapshot/push; existing held-delivery/cancellation tests cover the localized failure and worker termination.
- [Threat model] No findings: unchanged encrypted transport/device permission boundaries from `docs/protocol-mobile.md` § Security model; this plan adds no external capability.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-12

## Revisions

2026-10-12 — Validation at `071cb419cde985de419edb9eb7a170d3a8d66f6c`, using
Go 1.26.2 on Linux/amd64. Product sources are unchanged from starting main;
producer and both repair-test files are also unchanged from #3279. No evidenced
prompt-answer residual required production changes or another regression.

| Race regression | Executed leaves | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| `TestPromptAnswerRelayRemainsResponsive` (allow, deny, cancel, answer, refusal) | 5 | 5 | 0 | 0 |
| `TestPromptAnswerBlockedDeliveryDoesNotBlockNextAnswer` (both cancellation arms) | 2 | 2 | 0 | 0 |

`go vet ./...`, `go build -o /tmp/builder-3256/pyry ./cmd/pyry`, and offline
`go vet -tags e2e_realclaude ./internal/e2e/realclaude/...` passed.

The first uncached `cmd/pyry` race suite executed 15286 leaves: 15285 passed,
one failed and three skipped. `TestHostedAppsStartupFailureCleanup` failed with
`app lifecycle deadline` during hosted-process disappearance checks. This is the
existing main flake tracked by [#3251](https://github.com/pyrycode/pyrycode/issues/3251),
whose retained repeated-main probe reports the same failure. Its fixture runs
without relay configuration and checks app teardown, outside prompt-answer
publication. Its focused race rerun executed one test and passed with zero skips.
Do not fix or suppress that independent failure in this ticket.

The three package skips are existing opt-in `TestRunAgentRun_RealClaude` and
`TestCodexApprovalLive`, plus `TestMemoryPathsCaseInsensitive` on this
case-sensitive filesystem. Neither prompt repair regression skipped. Full live
acceptance for the fourteen named cases remains pending for the dispatcher;
no Claude version or live results are claimed by these offline checks.

One full uncached package rerun passed: 15286 leaves executed, 15286 passed,
zero failed and the same three skipped. This completes the builder's scoped
race check without claiming that #3251's intermittent failure is repaired.

2026-10-12 — Documentation-stage check of the dispatcher's subsequent
[full live gate](https://github.com/pyrycode/pyrycode/issues/3256#issuecomment-6134604398).
It tested `feature/3256` at `db3ce1ad8c93660509adab436cc97f1f6e1cbaa4`
merged with `origin/main` at `bd00b250fbf4`, building the daemon from that tested
tree. This includes #3279; no prompt-answer residual repair was introduced.
The retained Go-JSON report at
`/work/Projects/pyrycode-agents/logs/2026-10-12T23-22-56-595Z_real-claude-gate_#3256.log`
records Claude **2.1.280 (Claude Code)** in the composer-stop version outputs
and the initialize-control probe.

| Live scope | Executed leaves | Passed | Failed | Skipped |
| --- | ---: | ---: | ---: | ---: |
| Full configured real-Claude suite | 1987 | 1987 | 0 | 27 |
| All fourteen exact leaves listed above, including both modal-resolution subtests | 14 | 14 | 0 | 0 |

Each of the fourteen names has its own `run` and `pass` action in that report;
none is absent or skipped. The earlier pending-live statements describe the
offline validation stage. This separate dispatcher run supplies the named live
proof; it does not retroactively establish a single cause for every historical
failure or accept #3071's separate readiness/Android work.
