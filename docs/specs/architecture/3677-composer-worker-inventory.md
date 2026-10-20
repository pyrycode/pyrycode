# Composer Stop worker inventory

## Files read

- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: original root, final audits and session-resume-only launch audit wiring.
- `internal/e2e/internal/claudetape/fallback_worker.go` → `ReadLifecycleWorkers`: joins registered workers and independently proved cancellations to actual production Waits.
- `internal/e2e/internal/claudetape/worker_launch.go` → `FallbackLaunchAudit.cancelled`, `Save`: immutable outer-owned evidence and bounded private diagnostics.
- `internal/e2e/internal/claudetape/worker_launch_linux.go` → `sample`, `observe`, `ownDaemon`: current kernel ancestry, executable and registration observations.
- `internal/e2e/internal/claudetape/worker_launch_linux_test.go` → `TestFallbackCancelledLaunchBoundary`: synchronized cancellation and rejection controls.
- `internal/e2e/internal/claudetape/worker_launch_ownership_linux_test.go` → `testLaunchOwner`, `TestFallbackCancelledLaunchOwnership`: independently owned process trees and completed real Wait.
- `docs/knowledge/features/e2e-harness.md` → recorded lifecycle timing and settlement: Wait alone cannot prove launch; unknown ownership must fail.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: focused race proof and process cleanup contracts.
- `docs/testing/daemon-recordings.md` → Physical background-task boundary: original physical assertions and literal worker exchanges remain strict.

## Context

The refined issue separates reset (#3672) from Composer Stop. Retained #3667
bundle `312ce13f13ca0e33755e80fddd014b734492f5acf00130a232feb468ff02f1e6`
contains the same-tree rerun (Composer Stop passed) and historical log tails.
The refiner read the full original log before it became remote-only: Composer
Stop's original root and artifact audit passed, then inventory rejected
`total-wait-cardinality`. Counts from its deleted home remain unknown. Reset
failed earlier at the provider boundary and is not repaired here.

#3667 is merged at this branch's base `998460bc`. #3672 overlaps only the outer
suite file; its provider repair is independent, so keep local edits additive.
One useful behavior is honest Composer Stop worker accounting with its proof.
Forecast: at most 600 written lines, no new exported types/interfaces, at most
three consumer edits, three acceptance criteria and existing reject branches.
Documentation is handed off; no decision record is needed.

## Design

First extend bounded diagnostic retention to Composer Stop without granting it
cancellation authority. Observe current launches using the existing Linux audit,
retain registration states, current identity chain and actual Waits, and add
bounded registered-worker identities/counts to `Save`. Run a fresh focused replay
with external private evidence. Retain hashes, revision and counts. No raw argv,
protocol, environment or credentials enter the diagnostic.

If the measured mismatch is the existing cancelled-before-registration boundary,
admit Composer Stop to the same closed scenario allowlist in `sample` and
`cancelled`, and pass its independently owned audit to `ReadLifecycleWorkers`.
The two-attempt ceiling and all original native exchange, source, receipt,
process/group death and broker completion checks remain unchanged. A cancellation
adds zero native exchanges. Extend the synchronized cancellation and ownership
controls to Composer Stop before changing acceptance. If evidence demonstrates
another boundary, record a prospective revision before implementing its repair.

## Concurrency model

One existing sampler observes without signalling or delaying production. `Stop`
joins it before reading evidence; `Save` and inventory read immutable state.
Every helper control uses bounded context cancellation, process-group kill and
Wait. No new production goroutine or lock is introduced.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Owned launch before registration, group cancellation, completed Wait and death | `TestFallbackCancelledLaunchBoundary/composer-stop` |
| Duplicate/foreign Wait, unknown launch/registration, missing cancellation or exit | `TestFallbackCancelledLaunchBoundary/composer-stop` rejection cases |
| Reused PID or changed root/daemon/outer identity | `TestFallbackCancelledLaunchBoundary/composer-stop`, `TestFallbackCancelledLaunchOwnership/composer-stop` |
| Unrelated daemon with otherwise valid helper | `TestFallbackCancelledLaunchOwnership/composer-stop` |
| Original stop, background survival, FIFO release, cleanup, final inventories | `TestRecordedClaude/composer-stop` |

## Error handling

Unreadable or partial registration stays unknown. Missing independent ownership,
unmatched Waits and duplicate observations reject. Diagnostic writes are bounded,
private and exclusive; write failure fails the test. Diagnostics confer no
native completion authority and do not relax fixture cardinality or timeouts.

## Testing strategy

Write the extended synchronized controls first and observe the scenario guard
reject their valid owned cancellation. Diagnose focused Linux replay before
repairing authority, then run uncached race-enabled Composer Stop and affected
claudetape ownership/inventory controls through managed admission. Record original
root and outer counts, commands, helper/daemon/fixture hashes and evidence paths.
Run `go vet ./...` and `go build ./cmd/pyry`. Full-suite acceptance belongs to the
dispatcher; macOS is outside this ticket's scope. No live capture is required.

## Open questions

- Does focused evidence demonstrate an unregistered cancelled launch? Resolve
  before broadening acceptance; passing reruns alone do not answer this.

## Documentation handoff

Pending documentation stage: update `docs/testing/daemon-recordings.md`,
“Physical background-task boundary”, to describe the demonstrated worker-accounting
boundary and its ownership/cancellation guarantees. State that cancelled launches
do not count as completed native worker exchanges and that offline replay evidence
does not establish new live-provider behavior.

## Security review

**Verdict:** PASS

- Trust boundaries: `ownDaemon` binds current kernel ancestry to the actual outer-owned root; `cancelled` still requires that capability, never a Wait alone.
- Tokens: no credentials or token handling changes; diagnostics contain only bounded process identities, registration states, hashes and Wait flags/statuses.
- File operations: SHOULD FIX: added registration diagnostics must use bounded `ReadLifecycleControl`, preserve unknown on invalid files, and publish exclusively through `publishClosedFile` at private permissions.
- Subprocesses: existing allowlisted helper argv, executable identity and private cwd checks remain; real process-group death and completed Wait remain mandatory.
- Cryptography: existing SHA256 provenance only; no new primitive or secret comparison.
- Network/I/O: no transport changes; diagnostic JSON stays capped at 16384 bytes and does not copy worker inputs.
- Errors/logs: static failures and numeric/status diagnostics only; no raw protocol, argv, environment or private-home contents.
- Concurrency: sampler is joined before Save or acceptance; repeated Stop remains safe and helper tests retain bounded cleanup.
- Threat model: offline test evidence ownership is the relevant boundary; relay/mobile authentication and live-provider behavior are unchanged.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-20

## Revisions

### 2026-10-20 — controlled boundary versus historical attribution

The synchronized Composer Stop control at `f9aefcc3` failed: its real current
owned helper was observed before registration, then group-killed and reaped;
the unaudited inventory rejected one Wait versus zero registered workers, and
the supplied independent audit rejected at the scenario guard. This demonstrates
the cancelled-before-registration accounting boundary with actual process evidence.
Two fresh ordinary replays (`f9aefcc3`, `9af02f24`) passed their original roots
and recorded zero launches, zero registrations and zero Waits. These passes do
not establish the exact cause of the historical gate occurrence, whose counts
are unavailable. The implementation will admit only the independently proved
boundary; every unmatched or unknown observation continues to fail. No helper
delay, new replay attempt, timeout change or fixture edit is required.

Historical attribution remains open and will use the managed investigation
request with the retained refinement bundle and current diagnostic reports.
Request a Composer Stop replay with its original preceding scenario sequence
under gate contention, rather than executing a complete suite in this builder.
The source/worker repair and focused rejection proofs remain builder work;
wider diagnostic execution and complete-suite acceptance remain pending handoff.
