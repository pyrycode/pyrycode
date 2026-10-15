# Reference pending outcomes and inherited endings

## Files read
- `internal/thread/main.go` → `mainOutcome`, `foldWork`, `applyTerminal`: first-terminal selection and revision floors.
- `internal/thread/child.go` → `childWork`, `restoreChild`, `resolveChildren`: pending reports and independent intrinsic/previous snapshots.
- `internal/thread/fold.go` → `Fold`, `New`: reference versus compatibility ownership.
- `internal/thread/render.go` → `contentRecipe`, `renderShared`, `recentSource`: shared rendering and bounded source resolution.
- `internal/thread/agent.go` → `agentReport`, `addReport`, `applyAgent`: companion order/content provenance and deferred inline agent content.
- `internal/thread/shell.go` → `applyShellForeground`, `shellReportMatches`: foreground outcome producer and report validation.
- `internal/thread/publication.go` → `publicationCapture.capture`: immutable rendered publications and selected candidates.
- `internal/thread/render_test.go`, `render_work_test.go`, `publication_test.go`, `corpus_golden_test.go`: retention, failure/retry, publication and unchanged golden oracles.
- `docs/knowledge/features/thread-package.md` § Reusable fold regression corpus: preserve inputs and both raw/canonical digests.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` § Parent repair: propagate original ancestors and keep intrinsic terminals independent.
- `docs/knowledge/features/thread-package-main-thread-folding.md` § Scoped joins, pending outcomes and closure: first terminal, source scopes and late repair.
- `docs/knowledge/decisions/045-thread-index-over-history.md`: history is the sole production content source.

## Context
Pending outcomes and derived ancestor endings still retain JSON outside history.
This converts that one content-retention boundary; agent/shell launch/report storage
remains #3453, publication retention #3386, resumability #3335. No new ADR is needed.
Overlap: #3386 extracts the shared renderer and changes publication/query capture;
its changes are local and do not supply an API prerequisite. Integration must pass
the captured root's ancestor items and recipes, never the mutable worker fold.

## Design
`mainOutcome` stores terminal order, content-source ID, status and field. A lazy
compatibility map stores raw outcomes only for `New`, never reference folds.
Companion reports retain their mapped content ID independently of earliest order;
decode shell eligibility and turn identity at arrival rather than on reuse.
Production terminal enrichment uses recipe entry references, including foreground
shell outcomes. Inline agent/shell lifecycle payload conversion remains deferred.
Inherited endings use an ancestor item ID in recipes and `Fold.childEndings`.
Propagate the original intrinsically ended ancestor through provisional agents.
Keep inline serialization caches only in legacy compatibility state.
Restore/capture independent intrinsic and previous recipes for inline agents too.
The shared renderer resolves item dependencies from one supplied item/recipe view,
including ancestors outside selected output, before returning any candidate.
Resolve dependency history through the existing recent source. Public Content is
detached; immutable captured publications remain unchanged after later feeds.

## Concurrency model
No new goroutines or locks. Fold owners continue serializing Feed and rendering.
Publications retain immutable rendered values; future #3386 integration must use
captured item/recipe roots with an authoritative source for delayed output.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Pre-creation reports/endings; first terminal and creation revision floor | `TestReferenceEndingOutcomes` |
| Durable/mapped companion IDs, shell lifetime adoption | `TestReferenceShellCompanion` |
| Nested provisional closure, late text, link reopening after eviction | `TestReferenceInheritedEnding` |
| Missing dependency, repair and Store Retry | `TestStoreReferenceEndingRetry` |
| Retained captured view across later feeds | `TestReferenceInheritedEnding` |
Existing scoped/lifetime reuse and every golden feed partition remain mandatory.

## Error handling
Missing/unreadable/malformed referenced content fails the entire render with
`ErrUnavailable`; observations publish only generic `StateUnavailable`. No source
error, content or path reaches callers. Store Retry rebuilds after repair.

## Testing strategy
Write focused regressions first and observe failure. Compare every Item field,
observations/touches and watermarks with legacy output, including selected children
without ancestors. Extend unchanged corpus retention accounting to zero copied
outcomes/endings across live/private recipe snapshots and child caches. Run thread
race tests, module vet and binary build. The verifier owns `make check` (including
the issue's required full offline gate); do not duplicate it in this builder run.
Sizing: approximately 700 written lines, no exports, fewer than ten internal
consumers, four acceptance criteria and fewer than ten new reject branches.

## Open questions
None. Separate order/content IDs and inline ancestor lookup are required contracts.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/thread-package-agents-and-background-work.md`, **Parent repair**: describe ID-backed pending reports, inherited parent-content references, propagation through provisionally closed agents, independent intrinsic/previous recipes and reopening.
- Pending documentation stage: `docs/knowledge/features/thread-package-main-thread-folding.md`, **Scoped joins, pending outcomes and closure**: replace copied pending-outcome/parent-ending claims with ID-backed outcomes and inherited references, explaining distinct terminal-order/content IDs while retaining scoped joins, first-terminal and repair rules.

## Security review
**Verdict:** PASS
- Trust boundaries: `mainDecode` and arrival-time shell validation continue rejecting malformed identities before lifecycle decisions; render dependencies must decode as objects.
- Tokens/secrets: no credential handling; payloads remain inert JSON and never enter diagnostics.
- File operations: no new paths or writes; use existing history Lookup and its bounded cache.
- Subprocesses/cryptography/network: no changes to these boundaries, framing or credentials.
- Errors/logs: MUST FIX addressed in design: all dependency failures become generic `ErrUnavailable`, withholding the complete candidate.
- Concurrency: no new goroutines; renderer dependencies must belong to the same captured view as the selected child.
- Threat model: recursive agent/shell payload retention guard is deliberately deferred to #3453; this ticket proves its own pending/ancestor byte boundary.
**Reviewer:** builder (self-review)
**Date:** 2026-10-15
