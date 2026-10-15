# Final agent report markers

## Files read
- `internal/thread/agent.go`: `agentWork`, `addReport`, `applyAgent` and `associate` define scoped reports, companion pairing and both-owner repair.
- `internal/thread/shell.go`: `shellReportMatches`, `applyShellForeground` and `promoteShellEvidence` preserve ordinary foreground eligibility and original observations.
- `internal/thread/render_companions_test.go`: `TestReferenceEqualCompanion` pins equality, provenance and immutable publication recipes.
- `internal/thread/render_test.go`: `TestReferenceRetainedBytes` and `testReferenceFold` provide logical accounting and reference feeds.
- `docs/knowledge/features/thread-package-agents-and-background-work.md`: lifecycle, identity and parent-repair lessons require retaining reversible foreground completion and original observations.
- `docs/knowledge/features/thread-package-main-thread-folding.md`: validation/accounting boundaries and unchanged corpus expectations.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`, ADR 045: contract comments, independent proofs and small private state.

## Context
Reference folds retain every report even after a genuine final. Replace that collection with bounded projection facts for each scoped call/task, without changing legacy `New`. ADR 045 already records the architectural decision. Main turns, child snapshots and persistence remain separate tickets.

## Design
Use a private report-history container with pending continuation evidence and four marker slots: earliest result, earliest genuine non-result terminal, latest mapped nonterminal task report, latest mapped status-empty update. Reports remain content-free IDs, digest and lifecycle facts. Existing companion merging runs before reducing the candidate set. Terminal order remains distinct from content provenance.

Compact only after a genuine non-result terminal; task outcomes qualify, call launch results alone do not. For shells preserve mapped/durable result/denial continuation queues because a late mapped companion can change turn eligibility. Preserve unmatched nonterminal durable reports because later companions can change enrichment ordering. These are unresolved evidence, not final markers. Reassociation still recomputes both owners and markers travel with scoped task identity. First-lifetime shell promotion keeps original observation references.

The reference mode uses reduction; legacy mode retains its current collection and serves as the differential oracle. Projection gathers marker and pending candidates through one accessor. No exported API or format changes. No concurrent feature branch currently changes the target production files.

Sizing: one deliverable; approximately 650 written lines including focused regressions/accounting and this plan, zero exported types, four consumer functions, four acceptance criteria and no new rejection branches. Rechecked against the completed plan before commit.

## Concurrency model
The existing owner serializes Fold access; no goroutines or locks are added.

## State transitions and identity reuse
| Event | Race-enabled coverage |
| --- | --- |
| Genuine final then redundant/conflicting finals and empty enrichment | `TestReferenceFinalReportMarkers` |
| Equal companions in either order; content provenance changes without revision | existing `TestReferenceEqualCompanion` plus marker regression |
| Provisional result then late task link and reassociation | `TestReferenceFinalReportMarkers` |
| Reused source/lifetime/legacy identities and original recovery observations | existing agent/shell recovery tests, corpus and marker regression |
| Repeated fixed-shape finished turns | `TestReferenceFinalReportAccounting` |

## Error handling
No new failure modes. Existing validated ingestion and generic unavailable source handling remain authoritative. Markers carry no rendered content, paths or external effects.

## Testing strategy
First add a structural regression rejecting direct report slices and increasing terminal/enrichment counts for a fixed final identity. Compare complete items, observations, touches, visibility and watermarks with legacy across whole, chunked and single-entry feeds. Keep corpus/recorded goldens unchanged. Log logical bytes per completed fixed-shape turn at two history and payload sizes, separating final markers, recipes/metadata, active/unresolved state, other lifecycle state and source cache. Exclude container overhead/sharing; describe string identity and fixed-width ID effects.
Run `go test -race ./internal/thread ./cmd/pyry ./internal/relay`, `go vet ./...` and `go build ./cmd/pyry`. The dispatcher owns `make check`, including the full-module race suite; acceptance's full gate is pending there.

## Open questions
None.

## Documentation handoff
- Pending documentation stage: `docs/knowledge/features/thread-package-main-thread-folding.md`, **Validation and testing**: describe final agent-report marker retention, accounting method and measured boundary, distinguishing other uncompacted lifecycle state.
- Pending documentation stage: `docs/knowledge/features/thread-package-agents-and-background-work.md`, **Parent repair** and **Background shells**: explain that late links/reassociation can reverse provisional completion and terminal-looking rows alone do not authorize report compaction.

## Security review
**Verdict:** PASS
- Trust boundaries: `agentWork` still validates source, lifetime, lost identity and original observation references before report retention. Markers must remain within their owning group/task.
- Tokens/secrets: markers contain only existing identities, lifecycle words, digests and source IDs; no payload or new logging.
- Files/subprocesses/network: the change performs no I/O; rendering retains existing bounded source resolution and generic errors.
- Cryptography: existing SHA-256 content fingerprints establish equality, not authentication.
- Errors/telemetry: no new externally visible errors or payload logging.
- Concurrency: serialized Fold mutation; no new goroutines or shutdown paths.
- Threat model: SHOULD FIX in this build: structural and differential tests must detect retention growth and cross-identity reuse. Store/disk persistence is outside this ticket, assigned separately after #3478.

**Reviewer:** builder (self-review)
**Date:** 2026-10-15
