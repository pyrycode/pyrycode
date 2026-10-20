# Cancelled session-resume worker accounting

## Files read

- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: outer launch capabilities and final worker audit.
- `internal/e2e/realclaude/lifecycle_recording_test.go` → `readLifecycleRecordingConfig`, `TestLifecycleTapeHelperProcess`: source hashing precedes registration.
- `internal/e2e/realclaude/fallback_worker_recording_test.go` → `lifecycleRecordingFiniteWorker`, `lifecycleWorkerObservations`: exclusive job claims and actual production Waits.
- `internal/e2e/internal/claudetape/fallback_worker.go` → `ReadLifecycleWorkers`: strict native exchange inventory and exact Wait cardinality.
- `internal/e2e/internal/claudetape/fallback_worker_test.go`: native cancellation, foreign evidence and cleanup rewrite controls.
- `internal/e2e/internal/claudetape/worker_diagnostic.go`: bounded private failed evidence.
- `docs/knowledge/features/development-verification.md` and its controlled-regression topic: a focused pass cannot erase inherited failed evidence.
- `docs/knowledge/features/e2e-harness.md`, `e2e-realclaude.md`: original assertions and bounded process cleanup.

## Context

Production starts a process before its Go recording helper hashes its own binary
and publishes a job. Cancellation in that interval can leave a production Wait
without a native exchange. The original suite must continue rejecting unknown
Waits; a Wait alone supplies no launch authority. The supplied HP directory is
absent here and the fleet evidence lookup returned no bundles. Preserve new Linux
baseline evidence under `/tmp/builder-3667/baseline` and establish the sequence
with an independent current-process observer before enabling the repair.

One useful behaviour ships: honest accounting of cancelled launches, with its
regression and original replay proof. Estimated total written work 650 lines,
one exported type, no production consumers, three criteria and at most ten new
reject categories. No overlapping remote feature branch touches the target files.

## Design

Add an outer-owned launch audit for session-resume. On Linux it samples the
recorded daemon's current children, validating direct parent, own process group,
current helper executable, exact helper invocation, closed fallback argv and
private empty cwd. Record kernel start identity and whether job registration is
observed or proved absent. Missing observations stay unknown and cannot pass.
The audit starts before the original process and stops after original cleanup.

`ReadLifecycleWorkers` optionally accepts this closed audit capability. A cancelled
launch requires independently observed ownership, absent registration, exactly
one completed production Wait with real group cancellation/SIGKILL, and process
and group death. It contributes to the two-attempt limit but creates no job,
tape, native receipt or reviewed exchange. Every remaining Wait and registered
attempt follows the existing strict reader. The original observations remain
intact. Replay comparison still requires exactly the saved native exchanges.
Other platforms retain strict existing accounting without Linux observations.
Persist content-free numeric launch/registration/cancellation/Wait evidence in
the requested private evidence directory, including on final audit failure.

## Concurrency model

One outer sampler goroutine owns its launch map. Stop closes its cancellation
channel and joins it before any reader or diagnostic accesses the map. Sampling
does no signalling. Existing production cancellation and cleanup stay unchanged.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Launch, cancelled before registration, actual Wait | `TestFallbackCancelledLaunchBoundary` synchronizes sampling before kill |
| Duplicate/foreign/unknown Wait or missing cancellation | `TestFallbackCancelledLaunchBoundary` rejection cases |
| Reused PID/start identity or later registration | audit refuses ambiguous identity; existing native inventory remains strict |
| Registered native completion and cancellation | `TestFallbackWorkerNativeProcessBoundary`, `TestFallbackWorkerInventoryRejectsForeignEvidence` |
| Cleanup rewrites receipts | `TestFallbackWorkerCompletionRejectsCleanupRewrite` |

## Error handling

Unobserved processes, inaccessible ownership, changed kernel identities and
partial jobs cannot be promoted to cancelled launches. They retain the existing
failing inventory result. Audit overflow and identity ambiguity fail closed.
Diagnostics contain no environment, argv payload, transcript or arbitrary log.

## Testing strategy

Run the original Linux race-enabled session-resume selection before and after.
Write the synchronized cancellation regression first; observe failure at the
existing total-Wait cardinality boundary before enabling accounting. Run affected
claudetape cancellation/inventory controls, tagged compilation, vet and build.
Retain source identities, toolchain, counts and raw JSON evidence under
`/tmp/builder-3667/`; commit a compact evidence record under the harness testdata.
Full normal verification belongs to the dispatcher.

## Open questions

- Confirm actual live launch and absent registration before cancellation using
  the independent sampler; do not enable an exception based on Wait alone.

## Documentation handoff

Pending documentation stage: in `docs/testing/daemon-recordings.md`, under
“Idle eviction and literal native resume inputs”, document the demonstrated
cancellation boundary and resulting launch-to-Wait accounting guarantee.
Distinguish a proved cancelled launch from a complete native worker exchange;
retain the strict reviewed-worker and cleanup guarantees.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: SHOULD FIX: a numeric Wait cannot authorize a launch. Mint audit entries only from current kernel parent/group/executable/argv/cwd checks; keep audit fields private and bind the capability to home/scenario/source.
- Tokens: no credential reads or retention; diagnostics exclude environment and payloads.
- Files: reuse private control readers; inspect only closed daemon/job paths, bounded proc files and private evidence writes. Require the observed cwd's private empty directory.
- Subprocesses: observer launches no process and sends no signals. Regression uses owned helper processes with bounded kill/Wait cleanup.
- Cryptography: retain existing source SHA and native receipt digests; kernel start identities are observations, not cryptographic credentials.
- Network/I/O: no new network endpoint; proc reads are bounded and polling stops with the root.
- Errors/logs: retain only numeric identities and closed state booleans, never command text or native I/O.
- Concurrency: sampler joins before map access; PID reuse cannot overwrite a prior launch identity.
- Threat model: this is private replay proof infrastructure. Product attachment, provider credentials and relay behaviour are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-20

## Revisions

- 2026-10-20: synchronized current-process control established the missing-job
  cancellation boundary and failed at `total-wait-cardinality`. The original
  diagnostic run also exposed the faster-start arm: the helper reaches the
  zero-reviewed-workers check and exits as an unexpected invocation before
  production cancellation. For session-resume's empty reviewed inventory, keep
  that owned replay process silent and bounded until actual production
  cancellation; never start a broker or publish a job/receipt for it. The outer
  independent launch audit and real Wait must still prove its cancellation.
- 2026-10-20: original-root failures must also retain production Wait observations
  after daemon stop. Remove the diagnostic cleanup's failed-root early return;
  this preserves evidence and does not change the failed verdict. Bind the audit
  capability to its source revision/hash, outer parent and actual daemon, and
  retain unknown or previously observed registration as non-absent.
- 2026-10-20: source `17aefe75` passed the unchanged original root (1 executed,
  1 passed, no skips), then reproduced `total-wait-cardinality`. The private
  launch audit independently observed PIDs 56648 and 56718 under daemon 56518,
  with registration absent and distinct kernel starts. Both had exactly one
  completed production group-cancellation SIGKILL Wait; no jobs/native exchanges
  existed. Evidence: `/tmp/builder-3667/observed-wait-red/`. This resolves the open
  question and establishes the boundary before enabling final accounting.
- 2026-10-20: implementation `65382c1d` passed the original race-enabled replay
  with 2/2 outer test events and 1/1 original daemon root, zero failures/skips.
  Both actual launches remained registered as absent and joined exactly once to
  real completed group-cancellation Waits; native inventory stayed exactly empty.
  A prior same-source prefix failure remains failed and is tracked independently
  in #3668 (Backlog, priority:low). Affected controls passed 77/77 events (73 leaf
  tests), with vet, tagged vet and build green. Committed evidence record:
  `internal/e2e/testdata/3667-cancelled-worker-evidence.json`. Full verification and
  the documentation handoff remain pending with their owning stages.
