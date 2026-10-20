# Darwin cancelled worker observations

## Files read

- `internal/e2e/internal/claudetape/worker_launch.go` → `StartFallbackLaunchAudit`, `cancelled`, `Save`: immutable audit, cancellation join and bounded private metadata.
- `internal/e2e/internal/claudetape/worker_launch_linux.go` → `ownDaemon`, `observe`: independent ownership and registration rules to preserve.
- `internal/e2e/internal/claudetape/worker_launch_darwin.go` → `sample`, `bindRoot`: missing native boundary.
- `internal/e2e/internal/claudetape/fallback_worker.go` → `ReadLifecycleWorkers`: closed inventory, exact Wait/exchange cardinality and death requirements.
- `internal/e2e/internal/claudetape/worker_launch_linux_test.go`, `worker_launch_ownership_linux_test.go` → cancelled-launch controls and synchronized real process tree.
- `internal/e2e/internal/claudetape/worker_launch_diagnostic_test.go` → bounded content-free diagnostics.
- `internal/e2emcp/peer_darwin.go` → `currentPeer`: existing kernel argument and ancestry API pattern.
- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: existing audit wiring and evidence retention.
- `docs/knowledge/features/e2e-harness.md` § Recorded lifecycle timing and settlement: Waits alone never authorize ownership; controlled proof does not attribute historical failures.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: native race proof and evidence survival.
- macOS SDK `sys/proc_info.h`, `libproc.h`, and Apple's XNU `proc_info`/`proc_pidinfo`: kernel observation ABI.

## Context

Darwin's empty sampler leaves a completed cancellation Wait unjoined. Supply one
observable behaviour: independently prove a current owned helper before its
registration, preserving the already merged accounting contract. No daemon or
provider behaviour changes. Historical failures remain failed; their launch timing
is not established by this repair. No decision record is required.

## Design

Implement the Darwin audit using existing x/sys/unix sysctls and bounded proc_info
queries, without adding a dependency or invoking external commands. Bind the outer
process and its actual newly started process-group root. Reconstruct and recheck
the full bounded daemon ancestry to that root. Discover children from kernel
process inventory, never from Waits or jobs.

Require a live process with stable PID/start time/parent/group, actual executable
path and mapped executable vnode identity matching the bound image, exact helper
invocation and closed worker arguments, and a kernel-observed private worker cwd.
Recheck child and ancestry after reads. Preserve absent/registered/unknown states,
sticky uncertainty, PID reuse rejection and the scenario attempt ceiling. Native
buffers are bounded and never logged. Retain only existing audit metadata.

Add an audit mutex around sampling so the native regression can synchronize with
the normal sampler without racing map reads. No consumer API changes. No feature
branch changes the two Darwin paths at planning time; shared audit edits are local.
Estimated written work: 700–780 lines including plan, tests and evidence; zero new
exports, zero migrated consumers, three criteria, no new accounting state machine.

## Concurrency model

The existing sampler goroutine owns observation mutations under the audit mutex.
Tests inspect under the same mutex; Stop joins it before accounting or Save.
Observation never signals or waits for production. Test process trees have bounded
contexts, pipe barriers, actual Waits and cleanup restricted to their own commands.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Repeated sampling of held helper, then cancel/reap before registration | `TestDarwinCancelledLaunch` (both scenarios): one launch/Wait, zero exchanges/receipts |
| Duplicate/unmatched Wait; missing cancellation, Wait, exit or death | `TestDarwinCancelledLaunch` rejection controls |
| Unrelated daemon tree, unknown chain, changed root/daemon/outer/worker lifetime | `TestDarwinLaunchOwnership` |
| Unreadable/unknown registration and source mismatch | `TestDarwinCancelledLaunch` rejection controls |

## Error handling

Unreadable, missed, dying or partial native observations remain unknown; changed
retained identities invalidate the audit. Existing accounting rejects missing or
ambiguous joins. Native failures emit no argv, environment or protocol content.

## Testing strategy

Write synchronized native tests first and retain the stub failure. Hold a real
helper until normal sampling proves it, then obtain an actual SIGKILL group Wait.
Run focused uncached macOS race controls and the original session-resume replay
through shared admission with external private evidence. Retain exact revisions,
commands, toolchain, binary/fixture hashes and executed/pass/fail/skip counts in
an evidence record under the affected test package. Run vet and binary build.
Complete suites belong to the dispatcher; Linux/live-provider checks are outside
this repair's verification scope. No retries or timing allowance changes.

## Open questions

None. Native ABI layouts will be checked against the installed SDK and exercised
by real process observation; incomplete observations fail closed.

## Documentation handoff

Pending documentation stage: update `docs/testing/daemon-recordings.md`, “Idle
eviction and literal native resume inputs” and “Physical background-task boundary”,
and cancelled-launch paragraphs in `docs/knowledge/features/e2e-harness.md` to cover
native macOS alongside Linux with identical ownership, absent-registration,
cancellation/Wait and death requirements. Preserve cancelled-attempt versus native
exchange distinctions, historical attribution limits, and offline versus live proof.

## Security review

**Verdict:** PASS

- Trust boundaries: kernel ancestry, lifetime and executable vnode grant authority;
  control files only select candidates. Wait/job identities grant none.
- Tokens/cryptography: no credentials, keys or new cryptographic operations.
- Files: existing private control readers/publication are reused; cwd must be
  canonical private 0700 with kernel vnode matching the inspected directory.
- Subprocesses: observation starts/signals none. The owning test daemon cancels
  the worker group before its sole Wait releases the child PID. Closing the
  parent's input also requests this cancellation; reported evidence never grants
  cleanup authority. Owned survivor commands are separately killed and reaped.
  Root cleanup is bounded by its command context; no shell interpolation.
- Network/I/O: no network; fixed-size native buffers and bounded argument parsing.
- Errors/logs: native argv/environment are ephemeral and excluded from diagnostics.
- Concurrency: mutex protects sampler/test reads; Stop joins before consumption.
- Threat model: local offline test ownership only; provider protocol metadata is
  opaque and never authorizes observation or cleanup. No deferred finding.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-20

## Revisions

- 2026-10-20: Native ABI proof confirms the raw executable-path query returns
  zero on success; accept its terminated path only after executable-vnode checks.
  `TestDarwinLaunchOwnership/live-group` independently rejects a reaped worker
  whose actual owned group member survives. No accounting or timer changes.
  Child discovery uses fixed-buffer `PROC_PPID_ONLY`, rejecting a full buffer.
- 2026-10-20 rework, verifier findings 1 and 2: replace the deprecated direct
  Darwin syscall boundary with libproc's `proc_pidinfo`, `proc_pidpath` and
  `proc_listchildpids` wrappers through cgo. Preserve bounded buffers, strict
  native return checks and all identity rechecks. Without cgo these queries
  return unknown, preserving buildability and fail-closed inventory accounting;
  native acceptance uses cgo on macOS. No new dependency or consumer API.
  Remove cleanup's reported-worker-PID signal: the prior security claim was
  incorrect because a completed Wait could release that PID before a decoder
  timeout. Parent pipe closure now reaches the daemon, which cancels before
  reaping even on EOF or its bounded context expiry. No worker context callback
  may signal after that Wait. Cover unreadable evidence before cancellation and
  after reaping, including an owned surviving group member and an unrelated
  live group. `TestDarwinLaunchCleanup` must pass with uncached race checks.
  These repairs restore the existing observation/ownership invariant; they add
  no independently useful behavior. Estimated repair: 200–260 written lines,
  zero exported types, zero migrated consumers, no new accounting branches.
  Native files have no concurrent feature-branch overlap at rework planning.
  Run affected package race tests, original replay, staticcheck, vet and build;
  preserve all earlier failed evidence. Complete gates remain dispatcher-owned.
