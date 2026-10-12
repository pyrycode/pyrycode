# Daemon-owned hosted app lifetimes (#3204)

## Files read
- `cmd/pyry/main.go` → `runSupervisor`: registry construction precedes fallible startup and relay composition.
- `cmd/pyry/daemon_apps.go` → `openDaemonApps`, `newDaemonAppsManager`: shared host/root and committed discovery subscription.
- `internal/apps/owner.go`, `health.go`, `reconcile.go` → `NewOwner`, `Stop`, `Shutdown`, `Lookup`: asynchronous reconciliation, conditional observations, private targets and joined group cleanup.
- `cmd/pyry/daemon_apps_test.go` → `TestDaemonAppsComposition`: discovery and capability attestation remain separate.
- `internal/e2e/hosted_apps_test.go`, `harness.go` → restart/channel tests, `StartInWithEnv`, `RestartStreamInteractiveWithRelay`, `Stop`: exclusive seeding ownership and real-daemon evidence.
- `templates/hosted-app/source/src/service/` → `environment`, `database`, service/migration entries: compiled template with pre-migrated file-backed SQL.
- `docs/knowledge/features/apps-package.md` § Lifecycle and restart state, Testing: never reopen evidence; owner observations use committed revisions; runtime selection has no fallback.
- `docs/knowledge/features/e2e-harness.md` § Known Limitations: build a race-instrumented daemon explicitly for acceptance.
- `docs/knowledge/features/development-verification.md`: current source and independently observed evidence.
- `CODING-STYLE.md`, `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions; Registration, observed state and safe publication: process, SQL and capability contracts.

## Context
Discovery storage already lives outside conversations; the merged app owner is not installed in the daemon. This change owns one owner's lifetime and proves that service generations and SQL survive unrelated channel events and restart. Publication/migrations/rollback and encrypted resource routing remain separate tickets. No new decision record is needed.

## Design
Add `newDaemonAppsOwner(reg, node, logger) *apps.Owner`: nil storage exposes no owner; construction failures log a static reason and preserve normal startup. `runSupervisor` constructs it once from the same registry, selecting `node`, immediately defers joined shutdown with static failure logging, and retains the local owner for future publishers. No pool/relay/client context is passed into the owner. No resource readiness attestation, management verb or publisher is introduced.

Hermetic composition and fake-daemon tests select a controlled executable through a test-only argument or PATH. Seed committed releases only while stopped, then inspect daemon-written JSON directly. Existing storage assertions allow asynchronous owner observations while preserving identity, metadata, intent, release selection, isolation and monotonic revisions. A focused opt-in SQL test requires an explicitly selected actual supported runtime and fixture dependencies; it compiles the maintained template, migrates/seeds before startup, and uses its unmodified service. A test-only runtime launcher records PIDs/ports and adds a cooperative descendant, without modifying the service implementation.

No overlapping feature branches touch the planned files. Estimated written work: 740 lines including this plan, no exported types/interfaces, one composition consumer, four acceptance criteria, no new process state machine/reject branches. Recount before implementation commit.

## Concurrency model
`NewOwner` owns reconciliation, per-app workers, retries, probes and children. Its subscription only coalesces wakeups. The daemon defer calls `Shutdown`, which detaches the owner subscription and joins all workers/owned groups on normal exit and every subsequent startup error. Relay subscription teardown remains owned by existing relay composition. Test children have signal-driven shutdown and bounded HTTP/test waits.

## State transitions and identity reuse
| Event | Race-enabled coverage |
|---|---|
| Construction followed by startup failure / normal shutdown | `TestDaemonAppsOwnerLifetime`, `TestHostedAppsStartupFailureCleanup` |
| Explicit local stop, repeated stop, sibling remains available | `TestDaemonAppsOwnerLifetime` |
| Crash, failed observation, retry delay, starting, fresh running | `TestHostedAppsRecoverySQL` |
| Restart with same host/app/release, fresh processes; stopped/unpublished remain unlaunched | `TestHostedAppsRestartIsolation`, `TestHostedAppsRecoverySQL` |
| Client close, relay reconnect, channel stop, clear, idle eviction | `TestHostedAppsChannelIndependence` |

## Error handling
Rejected storage and owner construction leave apps unavailable without stopping the session pool. Unsupported/missing runtime and per-app start/health errors use the owner's existing committed failed observations and backoff; no new registry state. Shutdown logs only a static reason on failure and always joins. SQL acceptance fails if explicitly selected runtime/preparation is unsupported or broken; a default skipped opt-in test is never acceptance evidence.

## Testing strategy
Write assertions before production wiring and observe failure. Composition covers asynchronous launch, discovery observations, local stop/descendants, and intent-preserving shutdown. Fake-daemon tests run running apps with a real fake-Claude channel and direct registry JSON reads. SQL proof saves preferences via HTTP 204, then checks service readback and direct `node:sqlite` queries after crash/recovery and restart, including sibling/channel isolation and child cleanup. Compile the daemon with race instrumentation; run touched package race tests, focused e2e family, `go vet ./...`, and `go build ./cmd/pyry`. The verifier owns the full-module gate; no live Claude is required.

## Open questions
None. Actual runtime/version and proof counts will be recorded in the PR after execution.

## Documentation handoff
Pending for documentation stage:
- `docs/hosted-apps.md` § Runtime and layout and Registration and observed state: daemon reconciliation is independent of channel/client/relay lifetime, preserves desired intent and SQL across restart, and cleans up owned children on stop/shutdown. Keep resource-capability prerequisites and publisher-owned migration/rollback limitations explicit.
- `docs/knowledge/features/apps-package.md` § Lifecycle and restart state: the same ownership, restart and cleanup guarantees. Record SQL proof's actual runtime and executed/passed/failed/skipped counts without claiming live-Claude or resource-routing acceptance.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] `apps.Open` validates local storage/host identity; `prepareRelease` validates committed local builds. Reuse both rather than deriving paths or releases from client data.
- [Tokens] No credential creation or new credential flow. The owner's eight-variable environment excludes daemon/client secrets.
- [Files] Existing private root/registry modes and atomic writes remain in `openDaemonApps`/`Registry`; no new production file access. Test seeding ends before daemon ownership; direct JSON reads cannot normalize evidence.
- [Subprocesses] `Owner` selects a single executable, validates its actual version and runs fixed argv with isolated environment. SHOULD FIX: assert owned descendants are gone after local stop and daemon exits, including startup failure.
- [Cryptography] No crypto changes; persisted host identity is reused, never minted by the owner.
- [Network and I/O] Reuse bounded private health and `Target.Do`; no new production listener/resource route. Tests use loopback and bounded requests.
- [Errors/logs] SHOULD FIX: owner construction/shutdown logging must use static reason codes, never paths or subprocess diagnostics.
- [Concurrency] Registry-then-owner locking remains internal. Immediate daemon defer joins construction's workers after any later error. No channel/relay cancellation can cancel the owner.
- [Threat model] Trusted local service isolation is unchanged. OUT OF SCOPE: encrypted resource authorization belongs to #3123; publisher migration/rollback belongs to #3125/#3133. Production hosting capability stays withheld.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-12
