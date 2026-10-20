# Replay responses across owned predecessor exit

## Files read

- `internal/e2e/internal/claudetape/lifecycle_broker.go` → `RunLifecycleBroker`: matching, forwarding, action acknowledgement and independent EOF gates.
- `internal/e2e/internal/claudetape/lifecycle_exec.go` → `execLifecycleChild`: real stand-in PID and inherited provider pipes.
- `internal/e2e/internal/claudetape/lifecycle_server.go` → `handle`, `Join`: kernel peer, seven distinct pipes and owned process cleanup.
- `internal/e2e/internal/claudetape/tape.go` → `Player.Input`, `Complete`: strict input matching and released responses.
- `internal/e2e/internal/claudetape/lifecycle_artifacts.go` → `AuditLifecycleArtifacts`, `ValidateLifecycleLaunch`: authoritative inventory, stop witnesses and receipts.
- `internal/e2e/internal/claudetape/lifecycle_test.go` → `TestLifecycleProbeProcess`, lifecycle controls: reusable child process and real cancellation proof.
- `internal/e2e/realclaude/lifecycle_recording_test.go` → `TestLifecycleTapeBrokerProcess`: generic receipts and source-bound original helper.
- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: outer audit, original root and broker join.
- `docs/knowledge/features/e2e-harness.md`, `e2e-realclaude.md`, `development-verification.md` and its artifact-survival topic: child execution counts and retained failure attribution.
- `docs/testing/daemon-recordings.md` → “Session rotation and native transcript snapshots”: fresh identity, original reset edges and recall contract.
- `/tmp/builder-3665/diagnostic-only.patch`, `evidence-index.json`: withdrawn diagnosis and retained failing run; originals remain untouched.

## Context

One behaviour: complete strict replay across owned predecessor termination. Its controlled regression and content-free retained evidence stay with the repair. Historical diagnostic source `7d8e087f18f7cceb8b789aeae01ea887e7fac932` advanced the bound player to 83/83 but failed writing final matched input to the terminated stand-in, before forwarding its recorded response. Passing reruns alone cannot resolve that boundary. No production daemon changes or fixture changes. No ADR needed.

## Design

Replay matches each complete input with `Player.Input`, runs existing input/completion witnesses and delivers every released response. It does not forward matched bytes into the non-provider stand-in: that process exists to preserve PID, pipe ownership and real teardown, not to generate replay responses. Capture continues forwarding every input with bounded writes and rejecting pipe failures. Sealed live-prefix forwarding stays unchanged.

After authorized kill/rotation/eviction and both provider output EOFs, an incomplete replay player may still receive its final strictly matched request. Wait within the existing broker deadline until matching completes or original input EOF exposes incomplete completion. Never treat an action acknowledgement, player exhaustion or diagnostic as teardown authority. Preserve pending-frame checks, terminal callbacks, actual stopped observations and outer artifact audit.

Restore the diagnostic callback/retention pattern locally from the withdrawn reference, with explicit completion state and input/output progress. Static stages and error classes plus numeric counts/booleans only; absent observations remain unknown. Retain one terminal broker record outside HOME via stdout, never in the authoritative artifact directory. Copy only fixed existing control witness paths after abort/join cleanup, using private bounded reads and exclusive private destinations. Record source/daemon hashes and fixture digest in the evidence manifest.

Overlaps: #3666 adds timing measurement around other broker operations; #3667 adds worker accounting in the outer suite. Neither supplies a required symbol or rewrites this response boundary; build through with local additive edits.

Sizing rechecked against this plan: approximately 700–790 written lines including tests, evidence retention and plan; one exported diagnostic type, two existing broker consumers, zero signature migrations, three acceptance criteria, fewer than ten added rejection branches. One independently useful repair; numeric ceilings all hold.

## Concurrency model

Single broker loop retains state ownership. Diagnostic callback runs synchronously on return. Existing outer-owned broker server handles process shutdown and join. Retention runs after broker cleanup and before disposable HOME removal. Test children and readers are joined; no new production goroutine.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Owned predecessor dies before final query; multiple released responses survive | `TestLifecycleReplayAfterProviderExit` |
| Action acknowledgement and player exhaustion while outputs remain open; each output EOF separately | `TestLifecycleReplayAfterProviderExit` |
| EOF precedes the final query; incomplete/unexpected/partial input and foreign action fail | `TestLifecycleReplayAfterProviderExit` rejection arms plus existing lifecycle controls |
| Actual cancellation, prefix seal, failed exec and blocked capture pipes | `TestLifecycleExecKeepsActualPIDAndJournalsAfterKill`, `TestLifecycleBlockedPipesFailWithinWriteDeadline`, `TestLifecycleFailedExecReapsDetachedBroker` |
| Same conversation rotates to fresh session and successor recalls original note | `TestRecordedClaude/clear-reset` (all 83 bound frames, outer audit) |

## Error handling

Keep existing bounded deadlines and strict capture errors. Replay pipe independence is structural, not blanket error suppression. Input EOF with unconsumed frames, partial input, unauthorized teardown and terminal callback failures remain errors. Diagnostics classify wrapped errors without exposing their text. Retention failure is visible and cannot make an invalid receipt successful.

## Testing strategy

Commit the regression before repair and retain its uncached Linux race failure. A real owned child holds provider pipes; kill and reap it before the final query, independently hold each output writer, and assert response delivery plus no early completion. Test late final query after both EOFs, changed/partial/missing input, missing/foreign actions and failed terminal checks. Run affected lifecycle controls uncached under race detection, focused original reset replay with evidence outside HOME, tagged harness vet, module vet and binary build. Retain commands, source revisions, hashes, fixture identity, counts and evidence paths in the PR. Full suites remain dispatcher-owned; macOS is outside scope. Earlier sequence/cold-state/load attribution remains unknown.

## Open questions

None: the repair contract follows the demonstrated failure; unrelated #3670 remains a failed separate control and is not a prerequisite.

## Documentation handoff

Pending documentation stage: update `docs/testing/daemon-recordings.md`, “Session rotation and native transcript snapshots”, to describe the demonstrated replay lifetime contract: matched final control responses survive owned predecessor termination; action acknowledgement and player exhaustion do not substitute for both provider output EOFs, terminal receipts and the outer audit. Distinguish offline replay proof from live provider behavior.

## Security review

**Verdict:** PASS

- Trust boundaries: preserve `ValidateLifecycleLaunch`, kernel peer and closed role/pipe validation. `Player.Input` remains the only source of matched replay responses; diagnostics never feed audit decisions.
- Tokens/secrets: no generation or credential changes. Replay retains credential scrubbing. SHOULD FIX in build: test static classification with hostile payload/error text so no arbitrary content enters diagnostics.
- File operations: retain only fixed role/control suffixes with existing bounded private no-symlink readers, exclusive 0600 destinations and 0700 directories; no native transcript copies or authoritative inventory expansion.
- Subprocesses: preserve direct exec, owned PID and real cancellation; tests kill only their own `exec.Cmd` children. No new shell or environment forwarding.
- Cryptography: existing SHA-256 source/fixture identities only; no new keys or secret comparisons.
- Network/I/O: existing Unix peer, message/descriptor bounds and deadlines unchanged. One bounded terminal diagnostic per broker; partial records after forced termination remain unknown.
- Errors/logs: static stage/class vocabulary and numeric progress only; do not serialize protocol, argv, environment, arbitrary error strings or credentials.
- Concurrency: broker-local state, synchronous callback and outer join before retention; output EOFs remain independent from response progress.
- Threat model: offline test infrastructure only; no production relay/mobile boundary change. No deferred security findings.

**Reviewer:** builder self-review; **Date:** 2026-10-20.
