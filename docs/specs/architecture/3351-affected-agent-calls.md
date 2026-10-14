# Affected agent-call recomputation (#3351)

## Files read
- `internal/thread/agent.go` → `agentWork`, `applyAgent`, `agentAddReport`: scoped validation, pending evidence, companion reconciliation and lifecycle selection.
- `internal/thread/shell.go` → `registerShell`, `promoteShellEvidence`, `applyShellForeground`: ordinary ownership and first-lifetime adoption.
- `internal/thread/fold.go` → `Fold`, `Feed`: serialized fold ownership and private test counters.
- `internal/thread/agent_test.go` → lifecycle permutations and companion tests: existing enrichment proofs.
- `internal/thread/child_work_test.go` → `TestChildAffectedWork`, `TestChildTargetedRepair`: inspected-work accounting and intrinsic state hooks.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` → Parent repair: preserve intrinsic child state, original shell parent lanes and original recovery references.
- `docs/knowledge/features/thread-package.md` → reusable corpus and watermark contract.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: contract comments, meaningful independent assertions and scoped validation.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → item model: identity and projection remain unchanged.

## Context
Every agent entry currently applies every call in its group; every application sorts all group task IDs. #3350 is merged and supplies unchanged offline baseline digests. This is one deliverable: bound agent recomputation to affected ownership. No decision record is needed.

## Design
Keep validation and group identities unchanged. Collect affected call IDs after validation: explicit supported call evidence, a reported task's owner, both owners of reassociation, and usable roster-row owners. Deduplicate before applying. Pending calls without items remain valid evidence holders.

Maintain a group-local map of lexicographically sorted task IDs by owner. Association changes remove the old edge and insert the new edge; `applyAgent` reads only its owner's tasks. Preserve stable report ordering and companion reconciliation. First-lifetime shell promotion transfers the ownership index with evidence and returns affected moved owners; it remains a one-time migration per source and uninterrupted legacy scope. Existing child restore/save/schedule hooks remain intact.

Use private optional candidate counters for tests. Count lookup, skipped candidate, report collection/comparison, association maintenance and promotion work, including saved observations and pending main-call candidates. No exported API changes or new dependencies.

Sizing before commit: approximately 650 written lines including plan, instrumentation, production and tests; zero exported types, two `applyAgent` callers, three acceptance criteria, no new reject branches. No overlapping feature-branch edits were found in the three production files.

## Concurrency model
All changes run synchronously under the existing serialized `Fold` owner. No goroutines, locks or shutdown paths are added; test counters belong to each fold.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Report, task link and reassociation including repeated links | `TestAgentAffectedWork`, `TestAgentOwnershipOrder` |
| Bounded roster with unusable and repeated rows | `TestAgentAffectedWork`, `TestAgentOwnershipOrder` |
| Pending launch evidence and late durable/mapped companion | Existing `TestAgentLifecyclePermutations`, `TestAgentCompanionPendingFinals` |
| First lifetime adopts shell/pending evidence; subsequent ordinary reports | `TestAgentPromotionWork`, existing first-lifetime shell tests |
| Source/lifetime/legacy identity reuse and original references | Existing agent/shell scope and recovery tests |
| Ancestor closure, late link reopening and late parent repair | Existing `TestChildReclassification`, `TestChildTargetedRepair`, corpus checkpoints |

## Error handling
Retain current malformed/foreign/reference rejection before mutation. No new errors or recovery contract. Associations with absent launches remain pending and create no public items.

## Testing strategy
Write candidate-count and ownership tests first and observe failure against whole-group recomputation. Grow unrelated same-group calls/tasks/reports from 4 to 16 while fixing affected history and input IDs/payloads; require less than 2x work for result, task report, reassociation and roster. Assert actual state and lexicographic tie ordering independently. Measure first promotion separately and require subsequent reports to avoid rescanning saved evidence. Run unchanged corpus baselines and existing lifecycle tests with whole/chunked/single-entry feeds. Run `go test -race ./internal/thread ./cmd/pyry`, `go vet ./...`, and build the binary outside the worktree. The verifier runs `make check` and the full-module suite.

## Open questions
None.

## Documentation handoff
Pending for documentation stage: update `docs/knowledge/features/thread-package-agents-and-background-work.md`, "Agent lifecycle" and "Background shells", to state that reports and task ownership changes recompute affected calls, retain lexicographic associated-task order and preserve pending/promoted evidence. In "Parent repair", replace the reference to whole-group agent recomputation with affected-call recomputation while retaining targeted descendant repair.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: indexes are populated only after `agentWork` validates supported identity fields, source, lifetime and original references. MUST FIX addressed in design: reassociation must remove the old edge; otherwise unrelated evidence could close a previous owner. Test both owners and isolated groups.
- Tokens/secrets: no credential handling or logging is introduced; retained payloads remain inert copied JSON.
- File operations: no new production file I/O; committed corpus inputs remain unchanged.
- Subprocesses/cryptography/network: no subprocess invocation, cryptographic operations, network reads or new I/O boundaries in production.
- Errors/logs/telemetry: optional private counters count candidates only, without payloads or identities; existing errors remain unchanged.
- Concurrency: serialized `Fold` ownership covers indexes and counters; no shared global instrumentation.
- Threat model: this changes local history projection only; relay authorization and transport limits are untouched. No deferred security finding.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-14

## Revisions
- 2026-10-14: Increase the work controls from 4/16 to 16/64 unrelated scenarios, each including a created and an uncreated call. This keeps affected entries fixed while making even one filtered group scan exceed the 2x ceiling despite the fixed serialization work. Add ownership-index invariants after reassociation, repeated roster rows and first-lifetime promotion. Production design is unchanged.
- 2026-10-14: Verifier finding 1 is a full-gate evidence hold, with no demonstrated production regression. The dispatcher must rerun complete `make check` with Unix-socket access, at least a twenty-minute inner Go test timeout and enough outer time for all remaining tiers, followed by `make build`. Builder-scoped race checks with a longer timeout do not establish that full-gate pass. Production code and corpus expectations remain unchanged.
- 2026-10-14: Clarify the pending documentation handoff for `docs/knowledge/features/thread-package-agents-and-background-work.md`, "Parent repair": retain the separate observation and Store-publication scope when replacing broader agent recomputation with affected-call recomputation.
- 2026-10-14: Resolve verifier finding 1 with the completed socket-capable dispatcher gates recorded below. This supersedes the pending verification handoff; production code and corpus expectations remain unchanged.

## Verification evidence

On 2026-10-14, the dispatcher's `runVerifierGates` with its production `spawnGateCommand` executed the complete gate pair on pushed candidate `ba6c9bee552f296d873ec2d52aed294988181323`, with Unix-socket access. The run started at `20:42:49.208Z` and finished at `20:58:14.570Z`.

- `make check GO='sh /tmp/builder-3351-full-gate/go-timeout.sh'`: exit 0, including vet, the complete race-enabled module suite, staticcheck, substrate-guard and fake-daemon e2e.
- Then `make build BIN=/tmp/builder-3351-full-gate/pyry`: exit 0.
- The scratch Go wrapper adds `-timeout=20m -json -count=1` only to test invocations; other Go commands pass through unchanged. Each dispatcher gate had a thirty-minute outer allowance. No gate tiers or tests were filtered out.

| Tier | Top-level passed / skipped | All named passed / skipped | Failed |
| --- | --- | --- | --- |
| Full-module race suite | 4,837 / 18 | 26,464 / 19 | 0 |
| Fake-daemon e2e race suite | 259 / 2 | 695 / 2 | 0 |

Within the full-module suite, `internal/thread` passed all 126 top-level tests (740 named results) in 587.354 seconds, with no skips. `cmd/pyry` passed 1,497 top-level tests (16,457 named results) in 402.695 seconds, with three opt-in live/platform skips. `TestFoldBaselines` passed generated and shadow checkpoints; `TestThreadRecordedBaselines` passed all three recordings. All seven named results from `TestAgentAffectedWork`, `TestAgentOwnershipOrder` and `TestAgentPromotionWork` passed. Inputs and golden digests were not regenerated.

Other unit skips concern opt-in live captures, platform/filesystem conditions, root ownership substitution, supported hosted-app Node tooling and the separate production-duration gate. E2e skipped only the harness's child-only helper and its hosted-app SQL proof requiring optional Node/template inputs. These skips provide no evidence for those separate checks.

The exact logs, dispatcher result, parsed counts and scratch launcher are retained under `/Users/juhanailmoniemi/.codex/publish/pyrycode/builder-3351-full-gate-ba6c9bee/`. SHA-256 of `verifier-gate_#3351_1.log` is `c81dcfb4d25fa61223b01877a9c5b5052ee3f12e9ddb58cdde825e7504a30173`; SHA-256 of `verifier-gate_#3351_2.log` is `82e1f7694c011ebc77fd1029912b0efed58161f2a11f846b078b1c79af56f939`. Both stderr logs are empty. This evidence-only revision adds no production or test changes to the verified candidate.
