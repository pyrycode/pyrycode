# Compact summary task associations

## Files read

- `internal/thread/agent.go` → `agentWork`, `applyAgent`, `associate`: validated scoped association and lexicographic task selection; the last selected task owns the scalar.
- `internal/thread/render.go` → `contentRecipe`, `recipeField`: immutable computed scalars are separate from arbitrary launch JSON.
- `internal/thread/checkpoint_import.go` → `ImportCheckpoint`, `record`: validated recipes survive metadata-only restoration.
- `internal/thread/publication.go` → `publicationCapture.capture`, `restore`: prepare affected headers before publishing immutable metadata slots.
- `internal/thread/store_resume.go` → `initialPublicationIndex`: initialize all restored prefix rows, including unaffected rows.
- `internal/thread/child_headers.go` → `prepareChildHeader`, `childHeader.apply`: detached optional header facts.
- `internal/thread/child_pages.go` → `ChildSummary`, `Store.ChildPage`: bounded direct-child projection.
- `internal/thread/compact_queries.go` → `compactCollector`: roots consume the same prepared headers without bodies.
- `internal/protocol/thread_expansion.go` → `ThreadSummary`, `NewThreadSummary`: optional wire facts; constructor has no association authority.
- `internal/relay/v2session_catchup.go` → `threadSummaryFrom`: query wire translation.
- `cmd/pyry/daemon_thread_relay.go` → `daemonThreadSummary`: compact live wire translation.
- `internal/thread/child_headers_test.go`, `store_resume_test.go`, `agent_test.go`, `compact_queries_test.go`: publication, checkpoint and source fixtures.
- `docs/knowledge/features/thread-package-background-store-queries.md` → Revision-scoped direct-child summaries: initialize unaffected checkpoint rows and detach results.
- `docs/knowledge/features/thread-package-agents-and-background-work.md` → Agent lifecycle and scoped joins: links alone establish ownership; source/lifetime boundaries isolate reused IDs.
- `docs/knowledge/features/protocol-package.md`, `development-verification.md`, `CODING-STYLE.md`: validation and focused verification conventions.

## Context

Compact Mobile joins lose task navigation/counting because complete content carries
the canonical task scalar while summaries omit content. Expose one optional fact
from the existing fold authority. No new lifecycle, task inventory or consumer
implementation is needed. No decision record is needed for this additive field.

## Design

Add `TaskID *string` to `ChildSummary` and `ThreadSummary` (`task_id,omitempty`).
Nonempty is the exact computed identity; empty explicitly clears association at
the captured revision; nil means unknown, including older producers and generic
summary construction. Never normalize, truncate or interpret task identity.

Prepare the fact from the fold's immutable recipe, only for agent/tool rows with
a creating recipe. Only the computed `task_id` value has authority, never retained
launch content or headline text. Missing computed association means known empty.
Validated association scope remains conversation, recorded provenance/session and
producer lifetime or legacy scope, exactly as `applyAgent` computes it. Preserve
its deterministic greatest lexicographic task selection without an inventory.

Affected publication capture enriches prepared headers from matching recipes.
Initial index construction does the same for every restored public prefix row,
without history reads. `childHeader.apply` allocates detached string pointers.
Both existing wire translators copy the optional fact. `NewThreadSummary` stays
compatible and unknown because its full item does not prove association authority.
Complete content and all existing query/freshness/completion fences stay intact.

Overlaps: #3741 and #3849 edit other query-admission blocks in
`v2session_catchup.go`; this change is local to `threadSummaryFrom`.

Sizing: one observable producer contract; approximately 600–700 written lines,
zero new exported types/interfaces, four header preparation/translation sites,
four acceptance criteria and no new request rejection branches. Within all limits.

## Concurrency model

The existing serialized fold worker prepares immutable scalar headers; Store
capture remains under its existing mutex. Readers allocate detached optional
values and perform no association I/O. No goroutines or lock changes are added.

## State transitions and identity reuse

| Event | Race regression |
| --- | --- |
| Initial launch with no link; late link; terminal report; repeated report | `TestSummaryTaskLifecycle` |
| Several tasks per call; reassociation; old captured publication | `TestSummaryTaskLifecycle` |
| Absent/truncated or foreign conversation/session/lifetime evidence | `TestSummaryTaskScope` |
| Reused call/task IDs after a session/lifetime boundary | `TestSummaryTaskScope` |
| Full replay, checkpoint prefix/suffix resume, unload and reload | `TestSummaryTaskResume` |

## Error handling

No new external errors. Missing authoritative metadata stays unknown. Invalid
computed scalar data must not produce a guessed association. Existing fold and
checkpoint validation own source rejection; existing query errors/reset behavior
and integer limits below 2^53 remain unchanged.

## Testing strategy

First run a producer/query regression against the base and retain its missing
association failure. Reuse real checkpoint/store fixtures for compact newest,
older, change/live and child results; compare with complete folded content and
lifecycle metadata. Block history lookups after publication to prove summaries
require no bodies. Encode actual wire replies through both translations, checking
exact identity, absent/empty presence and no content/link/report/descendant keys.
Run affected Linux race tests, vet and the binary build. Complete configured
acceptance belongs to the dispatcher/verifier.

## Open questions

None. The refined contract selects `task_id`; the computed recipe supplies its
authority and checkpoint persistence without a schema migration.

## Documentation handoff

Pending documentation stage:

- `docs/protocol-mobile.md`, “Bounded expansion reads” (`ThreadSummary` presence table) and “Compact projection (declared)”: optional `task_id`, canonical evidence/deterministic selection, conversation/provenance/session/lifetime ownership, unknown versus known-empty replacement, exact strings and existing numeric bounds. Association proves neither detail availability nor coverage; existing row metadata controls placement/lifecycle without closed-body demand.
- `docs/knowledge/features/thread-package-background-store-queries.md`: association preparation and checkpoint reconstruction alongside direct-child/compact headers.
- Record independently accepted producer revision and contract link for Mobile #2431; Mobile implementation/integration remains its own acceptance.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: computed recipe scalar only; `agentWork` validates scoped source/link evidence and `ImportCheckpoint` validates restored recipe strings. Arbitrary launch `task_id` cannot acquire authority. Negative source/scope tests are required.
- Tokens/secrets: no credentials read or emitted; only canonical task identity already present in complete delivery is added. No content or report payload is copied.
- File operations: existing checkpoint schema and atomic persistence unchanged; no new path input or filesystem operation.
- Subprocesses: none added.
- Cryptography: unchanged Noise/authentication and encryption; no new primitive or key use.
- Network/I/O: existing row/envelope/decoder bounds and capability/projection checks remain; one scalar replaces no existing bound, and no descendant inventory is emitted.
- Errors/logging: no new logs/errors expose payloads or identifiers.
- Concurrency: scalar headers avoid shared mutable pointers; old captures and returned pointer mutation are tested. No new locks or goroutines.
- Threat model: relay remains untrusted; authorization and Noise remain existing boundaries. Task association conveys neither detail access nor completion authority. Mobile consumer work remains owned by Mobile #2431.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-24
