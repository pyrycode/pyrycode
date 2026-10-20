# Composer Stop recurrence

## Files read

- `internal/e2e/internal/claudetape/fallback_worker.go` → `ReadLifecycleWorkers`: inventory runs after the original root succeeds.
- `internal/e2e/internal/claudetape/worker_launch.go` → `cancelled`, `Save`: independently owned cancellations and known-state diagnostics.
- `internal/e2e/internal/claudetape/worker_launch_linux.go` → `sample`, `observe`, `ownDaemon`: current ancestry and registration observations.
- `internal/e2e/internal/claudetape/lifecycle_broker.go` → `RunLifecycleBroker`: restoration, physical observer and actual write share the absolute forwarding check.
- `internal/e2e/internal/claudetape/lifecycle_timeline.go` → `forwardLifecycleOutput`: unchanged 100 ms drift checks.
- `internal/e2e/internal/claudetape/daemon_binding.go` → `Frame`: restoration of declared identities and paths.
- `internal/e2e/internal/claudetape/physical_protocol.go` → `BeforeOutput`: parser and current physical operation checks.
- `internal/e2e/realclaude/lifecycle_recording_test.go` → `TestLifecycleTapeBrokerProcess`: installs the physical observer and retains typed timing errors.
- `internal/e2e/recorded_lifecycle_suite_test.go` → `runRecordedLifecycleSuite`: external evidence and final audits.
- `internal/e2e/internal/claudetape/lifecycle_timeline_test.go` → timing rejection controls.
- `docs/knowledge/features/e2e-harness.md` → recorded lifecycle timing and settlement: Wait cannot prove launch or completion.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`, `docs/testing/daemon-recordings.md` → causal proof, artifact survival and physical guarantees.

## Context

The original #3683 full log is recoverable on pyrybox, with its expected SHA256
`24eaed58ed9a2132ab8a5cef066644945aefaa4181c945911a5db17e9705d591`.
At candidate `4b1e799a50b3c25fe89b5eeca4934c0e4e203b2e`, Composer Stop failed
before inventory: original root 1/0/1/0, bootstrap forwarding drift at output 68,
anchor 3, due 34330542 ns, current 267210453 ns, lateness 232879911 ns.
The matching retained audit independently observed worker 499676, daemon 499563,
absent registration, one completed SIGKILL cancellation Wait, and zero registered
workers. This is not a demonstrated inventory rejection. Historical process/group
death is not independently recoverable from a later live census.

Preserve this recurrence and the original 2026-10-20T07:59:47.077Z sighting; the
older lost inventory counts stay unknown. A current-main focused replay passed
1/1 original root and 2/2 outer events without skips. It does not explain the
historical failure. No decision record is needed.

## Design

One behavior slice: diagnose and repair the demonstrated Composer Stop boundary
with its causal regression. First retain numeric elapsed work for restoration,
BeforeOutput, replay transformation and actual forwarding in the existing typed
timing error. Diagnose before changing acceptance. If necessary retain bounded
maximum stage durations on success too. These observations confer no authority.
Record the measured cause and repair contract prospectively in Revisions before
behavior changes. An external required repair follows its existing owner.

Preserve fixture bytes/cardinality, current source and peer checks, literal I/O
and EOF, two attempts, original physical assertions and bounded cleanup. No retry,
timeout, tolerance, epoch or schedule changes. Forecast <=600 written lines,
zero new exported types/interfaces, <=3 consumers, three criteria and no new
state-machine reject branches; reassess once the cause is measured.

Overlap: #3673 adds local observation hooks to `RunLifecycleBroker`; #3686 changes
the direct foreground-witness fixture. Neither supplies a required dependency.
The #3668 branch also adds an independent terminal-observation hook to the broker; this
diagnostic instrumentation does not call or extend it.

## Concurrency model

The existing broker loop owns elapsed measurements. No new goroutine or lock.
The existing sampler is joined before immutable audit reads and publication.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Owned cancellation before registration, completed Wait and death | Existing `TestFallbackCancelledLaunchBoundary` and `TestFallbackCancelledLaunchOwnership` |
| Duplicate/foreign/unmatched Wait or unknown ownership/registration | Existing cancellation and worker inventory rejection controls |
| Late input, scheduling overshoot, final-forward backpressure | Existing `TestLifecycleTimingDiagnostic`, `TestLifecycleTimelineUsesAbsoluteDueTimes`, `TestLifecycleTimelineRejectsLateFinalForward` |
| Original foreground stop, background survival, final worker/artifact audit | `TestRecordedClaude/composer-stop` |

The new causal regression is named in the prospective repair revision after diagnosis.

## Error handling

All current rejection and completion checks remain mandatory. Numeric elapsed
observations are diagnostic only; missing historical observations remain unknown.
Retain reports externally and commit a content-free manifest with exact hashes.

## Testing strategy

Use Linux managed admission, race detection and disabled caching. The launcher
refused the ticket's anchored parent/subtest selector before execution; supported
`-run '^TestRecordedClaude$'` with the exact one-scenario environment allowlist
selects the same leaf. Record both commands honestly. Run the affected claudetape
package, focused replay, module vet and binary build. Dispatcher owns full-suite
acceptance; macOS and fresh live capture are outside this ticket's scope.

## Open questions

- Which forwarding substage accounts for the recovered 232879911 ns lateness?
- Can the same boundary be forced synchronously without relaxing timing checks?

## Documentation handoff

Pending documentation stage: update `docs/testing/daemon-recordings.md`,
“Physical background-task boundary”, with the newly demonstrated boundary and
retained evidence, distinguishing controlled proof from historical attribution.
Preserve that cancelled launches consume attempts without completed native
exchanges, and offline replay establishes no new live-provider behavior.

## Security review

**Verdict:** PASS

- Trust boundaries: elapsed measurements cannot authorize a launch, receipt or completion; `ReadLifecycleWorkers` retains its independent joins.
- Tokens: no credential or environment inspection; diagnostic additions contain numeric durations only.
- Files: existing private, bounded, exclusive retention remains; evidence manifests include only reviewed metadata and hashes.
- Subprocesses: no command or process-group changes; actual owned cancellation and Wait remain required.
- Cryptography: existing SHA256 provenance only; no key or nonce changes.
- Network/I/O: unchanged literal pipe forwarding and size/deadline limits; no network changes.
- Errors/logs: SHOULD FIX: retain only fixed names and numbers, never raw frame or arbitrary I/O error text, in added timing diagnostics.
- Concurrency: measurements stay in the single broker loop; no shared mutable state is introduced.
- Threat model: offline evidence ownership is the boundary; live-provider behavior and relay authorization are unchanged.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-20

## Revisions

### 2026-10-20 — recovered failure and focused diagnostic limit

The original candidate log and matching retained bootstrap receipt both identify
`forwarding`, output 68, anchor 3. The original root timed out after that failure;
inventory was never reached. Its audit contains one independently owned jobless
launch and one actual completed cancellation Wait, not an unmatched Wait count.
The original 07:59 sighting separately retains `total-wait-cardinality` at
`c98f455bcf14be5b7c2b171e7de3771373bc5b63`; its original launch/Wait counts
remain unknown. The two historical failures are preserved as different stages.

Current main `73f826e2` and diagnostic source `3e0e649d` both passed Composer Stop
and the original root without skips. Both observed an independently owned
unregistered helper, actual completed cancellation and zero registrations, and
passed final worker/artifact joins. The original same-tree comparison also has
one owned jobless cancellation, recovered from its retained audit. Thus this
recurrence supplies no evidence for broadening inventory authority.

The new diagnostic records per-output restoration, physical observer, replay
projection and actual forwarding durations, plus bounded per-stage maxima and
their output ordinals. This changes no acceptance, schedule or process behavior.
At `3e0e649d`, output 68 took 14101131 ns restoring and 70917595 ns observing;
the largest actual-forward duration was 5392594 ns at output 62. These passing
measurements cannot attribute the original 232879911 ns lateness.

`TestComposerControlResponseDiagnostic` uses the exact reviewed output 68 to
separate duplicate-key validation, privacy validation, envelope decoding,
compaction and production parsing. It verifies this control response creates
no physical operation or facts. Its content-free measurements likewise do not
reproduce the historical miss. This diagnostic test is added to Files read and
the touched surface; it does not replace a synchronized causal regression.

The affected package passed 1008/1008 test events, zero failures/skips, including
the existing cancellation, ownership, inventory and timing rejects. The isolated
response diagnostic passed 1/1. These are diagnostic passes, not a reliability
repair. No demonstrated behavioral repair or failing-before/passing-after
regression has been selected. Both Open questions remain unresolved.

Request managed recovery assessment of the historical preceding scenario sequence
on the pushed diagnostic revision, with bounded substage evidence on failure.
The full original sequence has Composer Stop at position 68, immediately after
fallback-suggestion; a fresh one-scenario process lacks that preceding state.
Historical cache/load contributions remain unknown. The request executes zero
tests and is pending, as are the causal repair and complete-suite acceptance.
Documentation must preserve this explicit attribution limit.

### 2026-10-20 — diagnostic handoff, repair remains incomplete

Pushed diagnostic candidate `9813f95df7541fc939905d009e94da1924b3c5a5` passed
1009/1009 package test events (910/910 leaves), zero failures/skips. Composer
cancellation and ownership families passed 13/13 and 7/7 events. Its focused
replay passed 2/2 outer events and 1/1 original root, including final audits;
the audit again records one current launch and one completed cancellation Wait,
zero registrations. Module vet, tagged vet for both harness consumers and the
binary build passed on Linux Go 1.26.9. The final recording commit changes only
evidence, this plan and diagnostic comments, not executable behavior or tests.

Recovery assessment `3ad252474069132d5cba62de808ce884e92c15b8752361ee9ab8689e1f87d6f2`
targets that pushed diagnostic candidate. It is queued with zero tests executed,
requesting the adjacent fallback-suggestion → composer-stop sequence first.
The manifest `internal/e2e/internal/claudetape/testdata/composer_recurrence_3677.json`
records commands, source/fixture/binary identities, counts and hashed report paths.
The original 232879911 ns miss is still failed and unattributed. Causal repair,
synchronized regression and full acceptance remain pending; this diagnostic PR
must not be treated as a completed reliability repair.

### 2026-10-20 — prospective preparation-boundary repair

The managed adjacent fallback-suggestion → composer-stop sequence at `36e3a91c`
passed both original roots and all three outer events without skips. Composer
restoration peaked at 25114440 ns at output 68; observation peaked at 57297041 ns
at output 121. Its current audit knows zero launches, registrations and Waits.
This pass does not attribute the historical 232879911 ns miss or job-publish order.

Source establishes a wasted scheduling boundary: `writeOutput` waits until due
before restoring immutable bytes with `DaemonBinding.Frame`. That pure transform
can use the interval before due without advancing any physical event. Separate
preparation from delivery with `forwardPreparedLifecycleOutput`: restore once
before the existing absolute wait; run BeforeOutput, replay projection, actual
forwarding and final-write checks at delivery. Capture is unchanged. No epoch,
offset, allowance, retry, fixture, process or completion contract changes.
Remove the redundant outer duplicate-key check in BeforeOutput only, because
safeCLIJSON independently performs that same check before decoding every carrier.

First extract the existing ordering unchanged, then run
`TestComposerOutputPreparationSchedule` against it. Use the exact reviewed output
68 and offset, a channel-gated real restoration and deterministic clock charged
120 ms for preparation. Waiting first causes forwarding drift; preparing first
uses the existing gap and still meets due + 100 ms. Reject 200 ms preparation,
late input, scheduler overshoot, physical-observer delay, final-write backpressure
and preparation errors. No observer or write may run early or after preparation
failure. Existing physical/privacy rejection controls remain mandatory; extend
duplicate/nested-credential rejection if necessary. This proves a controlled
cause at the actual restoration/forwarding boundary; historical substage and
scheduler contributions remain unknown.

Retain a bounded ring of 64 numeric output samples in LifecycleDiagnostic:
ordinal, anchor, due, entry, restoration, observation, projection, actual write,
completion, all elapsed nanoseconds relative to the selected unchanged anchor.
Together with existing stage maxima this distinguishes work from unused gaps
without granting completion authority. No new goroutine in the broker; test
gates release and join their worker on every path.

Files additionally read/touched: `lifecycle_timeline.go` (new private scheduling
seam), `composer_output_diagnostic_test.go` (exact reviewed response), and
`lifecycle_diagnostic.go` (numeric sample ring). No feature branch currently
overlaps the three implementation files. Forecast including preserved work:
under 800 written lines, zero exported types/interfaces, two consumers, three
criteria, no new state-machine rejection branches. This remains one repair with
its regression. Both original sightings remain in the evidence manifest.

Security amendment: PASS. Pure restoration neither reads current process state
nor launches/signals workers; physical validation remains at delivery. Privacy
and duplicate-key validation still precede parsing/forwarding. Ring entries use
only fixed numeric slots, with bounded cardinality and existing private retention.
Worker ownership, cancellation, Wait, source, peer, I/O/EOF and final audits are
unchanged. Documentation handoff remains pending with the attribution distinction.
