# Bounded restart-safe thread catch-up

## Files read
- `internal/thread/store.go` → `run`, `publish`, `Unload`: complete recovery, immutable publication and joined retirement.
- `internal/thread/queries.go` → `queryIndex`, `query`: ordered/active/parent lookup and detached query ownership.
- `internal/thread/fold.go` → `Feed`, `Items`: public membership and committed entry progression.
- `internal/thread/observations.go` → `recordShown`, `difference`: first public appearance and removal evidence.
- `internal/thread/child.go` → `resolveChildren`: late joins and revision stamping.
- `internal/thread/sends.go` → `sendFact`: claimed delivery suppression differs from hiding.
- `internal/history/forward.go` → `Walk`, `Tail`: complete chronological replay independent of chunk boundaries.
- `internal/thread/queries_test.go`, `store_test.go`: counted 36,000-entry fixtures and lifecycle barriers.
- `docs/knowledge/features/thread-package.md` § Cache and epochs: final revisions cannot reconstruct first publication; cache items never seed continuation.
- `docs/knowledge/features/thread-package-background-store.md` § Bounded queries: readiness, query costs and captured publication ownership.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, ADR 042 § Update messages: contracts and evidence requirements.

## Context
Reconnect requires current full states of touched public rows, active rows and parents, including after clean recovery. Retained live `Changes` batches cannot establish durable continuity. Query data remains disposable history-derived state; no persistent schema or folding-rule change is needed. ADR 042 already records the decision.
One deliverable; estimated 730 written lines (200 production, 450 tests, 80 plan), zero new exported types, one changed internal call site, four acceptance criteria, fewer than ten reject branches. No production overlap; #3094 changes existing store tests only, which this ticket reuses without editing.

## Design
`Store.CatchUp(ctx, id, epoch, afterVersion, limit) (QueryResult, error)` requires caller authorization. Add `FromVersion` and `ResetRequired` to `QueryResult`. Successful nonzero results certify `(FromVersion, Version]` for one epoch/publication; page fields remain zero. Reset results carry usable state and current epoch/version, but no items, covered range or page metadata. Nonusable states carry only state/generic error.
`MaxCatchUpGap = 1024` bounds history-ID distance, inclusive. Nonzero queries ignore the window limit. Empty/mismatched epochs, future versions, oversized gaps, missing complete evidence or suppression after the requested version reset. `afterVersion == Version` immediately returns active rows/parents.
Version zero accepts empty/matching epochs and delegates to the captured newest-window selection, with positive-limit validation and the existing 256 ordered-row clamp. A nonempty mismatched epoch resets.
Extend entry-by-entry public membership observation with latest public touch by permanent ID and latest suppression version. Record revision changes and first/repeated public appearance even when creation/revision is old; hidden/dropped remain public. Removing a previously public projection records suppression. Unsupported/malformed/foreign entries advance consumed version without inventing touches.
At publication prepare immutable sorted latest-touch lookup alongside existing identity/order/active indexes, copying scalar evidence from the fold. Complete replay through H and evidence for each current public row certify coverage; missing evidence resets. Selecting latest touches greater than V is equivalent to selecting every currently public row touched in `(V,H]`. Any omitted previously public row requires reset through suppression evidence. Request work is bounded by index seeks, selected/active/unique-parent rows and content copying; no history read or conversation scan. Recovery/publication preparation remains proportional to the fold and is measured separately.

## Concurrency model
The existing worker exclusively owns mutable folding/evidence. Index construction occurs outside the global mutex; publication installs snapshot/index together. Queries capture both under that mutex and traverse immutable data outside it. No new goroutines. Existing worker cancellation and joined unload/shutdown remain unchanged; a canceled query discards its result without canceling the worker.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Repeated/no-item touches and arbitrary saved versions; more than 64 publications | `TestCatchUpPublications` |
| Private child first appearance and claimed delivery suppression | `TestCatchUpEvidence` |
| Clean restart and compatible unload/reload with retained epoch | `TestCatchUpBoundedRecovery` |
| Publication replacement and retirement while a captured query runs | `TestCatchUpIsolation` |
| Rebuilding/failure/retry/closed readiness and query cancellation | `TestCatchUpReadiness`, `TestCatchUpIsolation` |

## Error handling
Validate canonical conversation IDs; validation/context errors return separately with empty results. Readiness follows existing queries. Resets never expose partial items or usable range/page metadata. Failed recovery withdraws all query evidence. Preserve cache validation, epoch generation and `Snapshot`/`Observe`/`Changes` contracts.

## Testing strategy
Write failing behavioral tests first. Prove exact/above gap boundaries, epoch errors, suppression, first-publication children, zero-window parity/clamping, deduplication/full content/detachment, active/shared/multilevel parents, non-boundary versions and immediate H reads. Count actual history reads and item/index visits across 36,000 committed entries; measure cold preparation separately and repeat after restart/reload. Barrier tests cancel during traversal and hold captured queries through replacement/retirement while writers and another conversation progress. Run `go test -race ./internal/thread ./internal/history`, `go vet ./...`, `go build ./cmd/pyry`; the dispatcher owns the standard full hermetic gate.

## Open questions
None.

## Documentation handoff
Pending documentation stage: `docs/knowledge/features/thread-package-background-store.md`, “Bounded queries”: catch-up inputs, numeric gap bound, current-state/active/parent closure, zero-version newest-window behavior and reset/unavailable conditions including projection suppression.
Pending documentation stage: `docs/knowledge/features/thread-package.md`, “Cache and epochs”: distinguish reconstructed reconnect continuity from retained `Store.Changes` live ranges; clean epoch reuse supports in-bound persisted versions only after usable recovery; query data remains derived from history.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: `CatchUp` validates canonical IDs and documents authorization as a caller precondition. Fold decoding/ownership validation remains the history boundary; private items never enter query indexes.
- Tokens/cryptography: existing random epochs are reused only through existing validated recovery; they are identity, not authorization. No new secrets or crypto operations.
- File operations: requests perform no file I/O; recovery uses existing contained history and atomic no-follow cache operations without changing formats or permissions.
- Subprocesses/network/I/O: no subprocess/socket introduced. `MaxCatchUpGap` bounds requested continuity; result copying is proportional to required content, active rows and parents. Wire batching and open-conversation authorization are OUT OF SCOPE, owned by #2963.
- Errors/logs/telemetry: generic readiness errors and reset metadata contain no source paths, payloads or credentials; no new logging.
- Concurrency: immutable captured indexes prevent mixed-version reads; cancellation drops partial results, and existing lifecycle joins own workers.
- Threat model: no new relay capability is enabled; authentication, capability negotiation and encrypted wire replies remain #2963.
**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10
