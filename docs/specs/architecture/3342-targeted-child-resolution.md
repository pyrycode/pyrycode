# Targeted child resolution

## Files read
- `internal/thread/fold.go` → `Feed`, `Items`, `New`: entry ordering, private state and omission.
- `internal/thread/child.go` → `childWork`, `saveChild`, `restoreChildren`, `resolveChildren`: intrinsic state and scoped older-parent joins.
- `internal/thread/agent.go` → `agentWork`, `applyAgent`: late parent evidence, task reclassification and first finals.
- `internal/thread/shell.go` → `registerShell`, `applyShellForeground`, `promoteShellEvidence`: preserve original child lane across lifecycle promotion.
- `internal/thread/main.go` → `foldWork`, `closeText`, `applyTerminal`, `setContent`: intrinsic mutation sites.
- `internal/thread/child_test.go` → `TestChildNested`, `TestChildReclassification`, `TestChildNestedLinkRepair`: semantic regression scenarios.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` § Parent repair: closure must remain reversible; early reports and original groups must survive.
- `docs/knowledge/features/thread-package.md`, `development-verification.md`, `CODING-STYLE.md`, ADR 042 § Item model: observation, evidence and immutable identity contracts.

## Context
Every entry currently restores all children, scans all agent calls for registration, copies all child bases and walks all items for resolution. One affected-tree optimization preserves the existing public item contract. No new decision record is needed.

## Design
Index children by original `agentKey` and parent call; keep reverse agent-item identity for descendant scheduling. Reparenting removes the old edge. Retain unresolved edges so late enrichment can repair them without searching history.
Replace global restoration with idempotent restoration immediately before intrinsic child mutations. A per-entry dirty set drives base capture and resolution. Expand only dirty agents' indexed descendants, then process in creation order, retaining the strictly older parent rule. Cache effective agent endings, including inherited endings, so a fixed subtree needs no unrelated ancestor/history walk.
Agent recomputation remains outside this ticket: compute against intrinsic state in a local item, publish only actual intrinsic changes, and schedule registration/resolution only for changed agents. Foreground shell enrichment uses targeted ordinary mutation hooks. Existing source/lifetime promotion never changes ordinary child parenting.
Optional per-fold test counters count registration candidates, restoration/base copies, descendant edges and resolution items before skip decisions. They provide deterministic bounds without timing assertions.
Sizing: one deliverable, approximately 700 written lines including tests and this plan; zero exported types, at most 10 integration sites, three acceptance criteria and fewer than 10 reject branches. No overlapping remote feature branches touch the planned files at planning time.

## Concurrency model
Existing serialized Fold ownership; no goroutines, locks or shutdown paths added.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Ancestor final, nested closure, ending enrichment, repeated/conflicting finals | `TestChildAffectedWork`, existing `TestChildNested` |
| Late task link removes provisional closure, retaining intrinsic terminals | `TestChildAffectedWork`, existing `TestChildReclassification` |
| Late parent enrichment/reparenting and unresolved descendants | `TestChildTargetedRepair`, existing `TestChildNestedLinkRepair`, `TestChildRepair` |
| Reused source/lifetime/scope/call and shell evidence promotion | Existing `TestChildLifetimeReuse`, `TestChildScopes`, `TestShellFirstLifetimePendingCreation` |
| Whole/chunked/single-entry replay | New targeted scenarios plus existing `testMainReplay` semantic cases |

## Error handling
Keep existing validators and version-only invalid-fact behavior. Unknown parents stay private; self/newer parents cannot join. No new returned errors or external operations.

## Testing strategy
Write deterministic work-count tests first. Grow unrelated standalone history and independent trees in the same agent group by at least 4x with a fixed affected nested tree. Main-only appends must record zero work; ancestor changes, reopening and child registration must have fixed nonzero bounded counts, including inspected skipped edges/items. Assert exact items/content/revisions and detached state, then run `go test -race ./internal/thread ./internal/history`, offline recorded replay tests, `go vet ./...` and `go build` to scratch. The verifier owns `make check`.

## Open questions
None; broader agent scans, observation scans and publication costs remain assigned to #3327/#3328.

## Documentation handoff
Pending documentation stage: update `docs/knowledge/features/thread-package-agents-and-background-work.md`, “Parent repair”, to describe affected-parent/descendant resolution and targeted intrinsic restoration instead of recomputing every child on each feed. Retain closure/reopening rules, unresolved-child omission and original-source/lifetime/scope joins.

## Security review
**Verdict:** PASS

**Findings:**
- Trust boundaries: `Feed`, `mainDecode` and `agentWork` remain the validation boundary. Index keys must use original `agentKey`; strictly older creating IDs enforce acyclic edges even for malicious parent facts. Existing malformed/foreign and scope tests cover rejection.
- Tokens/secrets and cryptography: no credential handling, randomness or cryptographic operations added; retained content remains inert JSON.
- Files and subprocesses: production changes are memory-only; no paths or commands are derived from content.
- Network/I/O: no new I/O boundary or changed admission limits. Broad non-child costs remain in #3327/#3328.
- Errors/logs/telemetry: counters are private opt-in test state; no payload logging or external error changes.
- Concurrency: serialized Fold ownership covers indexes and cached endings; no background work added.
- Threat model: preserve isolation and omission for hostile history identities; no relay/mobile access contract changes.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-14
