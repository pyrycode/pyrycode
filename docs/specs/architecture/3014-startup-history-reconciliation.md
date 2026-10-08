# #3014 — Reconcile interrupted main work at daemon startup

## Files read

- `cmd/pyry/main.go` → `runSupervisor`: ownership precedes the single history store and delivery producers.
- `cmd/pyry/runtime_history.go` → `runtimeHistoryFact`, `closeRuntimeSource`, `trackRuntimeTool`: closure vocabulary, ordering and ordinary-tool exclusions.
- `cmd/pyry/history_projection.go` → `historyEntryShown`, `legacyHistoryType`: visibility and transport eligibility are independent.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`, `historyPageFailure`: shared metadata and content-free failure classification.
- `cmd/pyry/interactive_turn_v2.go` → `handleForSource`: main/child attribution and hidden thinking openings.
- `internal/history/log.go` → `Store.Page`, `Entry`, `SessionProvenance`: raw newest-first opaque-cursor walks do not create logs.
- `internal/conversations/registry.go` → `Registry.List`: no filter includes archived conversations.
- `internal/protocol/interactive.go`, `internal/protocol/messaging.go`: recorded turn/tool IDs and session delimiters.
- `internal/e2e/relay_v2_history_test.go` → `TestRelayV2_ConversationHistory`: paired-phone pagination fixture and raw-page boundary assertions, read during verifier rework.
- `docs/knowledge/features/history-package.md`, `history-package-producers.md`: terminate on AtStart; recovery must not fill missing provenance; lifecycle facts remain durable only.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: closure before hidden daemon-restart dividers.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: behavioral race tests and content-free log assertions.

## Context

Runtime closure exists, but daemon death leaves durable main work open. Recover only from the existing log before new producers run. This implements ADR 042 without a new decision record. Feature branches #2873 and #2882 do not overlap the proposed files.

Sizing: one deliverable, approximately 650 written lines including this plan and tests, no exported types, one consumer call site, four acceptance criteria, fewer than ten reconstruction reject branches. No API or dependency changes.

## Design

Add `reconcileStartupHistory(store, registry, logger, at)` in `cmd/pyry/startup_history.go`. Invoke synchronously immediately after the single store is constructed, after `ctrl.Listen`, before channel delivery, queues, relay or pool execution. Snapshot `Registry.List()` without filters.

Walk each conversation's raw `Store.Page` newest-first until `AtStart`, passing cursors unchanged. Retain identity/outcome state rather than historical content. Key turns by recorded provenance and turn ID; absent provenance is keyed within recorded session-delimiter scopes. Daemon-restart dividers do not split these scopes, so prior recovery interruptions still match. No registry-current attribution or invented turn/tool IDs.

Hidden main openings, main deltas and main tool rows identify turns. Main results, denials and recorded tool interruptions suppress unfinished calls; turn ends and recorded turn interruptions permanently close their matching turn even when later facts survive. Parent-attributed rows do not identify main work. Agent/Task launchers identify their main turn but never an ordinary interrupted tool. Background lifecycles remain #2969.

After a complete successful read, `closeStartupMainWork` appends ordinary tool interruptions before each turn interruption through `runtimeHistoryFact` and `appendConversationHistory`, retaining known provenance. Deterministic turn/tool ordering uses durable opening order and tool identity. This is the composable closure-before-divider point for #2969. Then append one daemon-authored restart divider for any nonempty raw log, with one startup timestamp and explicit hidden visibility. Empty/missing logs receive nothing.

## Concurrency model

No new goroutines. Reconciliation runs synchronously under instance ownership before producers start. Store and registry retain their existing internal locks. Each conversation is read completely before any append to it, avoiding cursor changes from our own writes.

## State transitions and identity reuse

| Event | Race-enabled coverage |
| --- | --- |
| Mixed open/ended main turns and completed/failed/denied/interrupted tools | `TestStartupHistoryReconciliation` |
| Thinking-only opening, parent/launcher/background exclusion | `TestStartupHistoryReconciliation` |
| Repeated starts and late facts for closed turns | `TestStartupHistoryReconciliation` |
| Reused turn/tool IDs across sources and legacy session delimiters | `TestStartupHistoryLegacyScopes` |
| Raw page/segment crossings | `TestStartupHistoryPagination` |
| Archived/multiple/empty/missing/new conversations | `TestStartupHistoryDiscovery` |
| Ownership refusal and closure before startup producer traffic, both relay modes | `TestStartupHistoryOrdering` |
| Failed read/write followed by healthy conversation | `TestStartupHistoryFailures` |

## Error handling

Read failure skips reconstruction and divider for that conversation; partial evidence cannot prove unfinished work. Log only canonical conversation ID, event and `historyPageFailure`. Writes use the existing best-effort append seam and do not abort startup or other conversations. Unknown types and unidentifiable legacy facts remain unchanged.

## Testing strategy

Write behavioral tests first and observe failure before implementation. Inspect raw entries, unchanged prefixes, IDs, timestamps, explicit visibility and provenance. Use real history stores for pagination and filesystem failures, existing startup delivery injection for ordering. Existing projection/runtime isolation tests cover the transport allowlist. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`; dispatcher runs `make check` and full-module gates.

## Open questions

None. Unknown legacy provenance stays absent; no transcript lookup is permitted.

## Documentation handoff

- Pending documentation stage: update `docs/knowledge/features/history-package-producers.md`, “Producers (#2114, #2115)” (linked from `history-package.md`): surviving-log discovery; main-work closure before hidden restart dividers and new traffic; no divider for empty/new logs; repeated-start closure idempotence; absent provenance for unresolved legacy facts.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: raw payloads cross into a minimal identity decoder in startup reconstruction; conversation selection comes from `Registry.List`, not payload conversation IDs. Child work and absent identities must not create main closures.
- Tokens/secrets: no credentials are read; recovery payloads carry only identities and outcomes, never historical text or tool inputs.
- File operations: reuse `Store.Page`/`AppendWithMetadata` containment, regular-file reads, O_NOFOLLOW writes and private modes; no new file paths or durable log.
- Subprocesses/cryptography/network: reconciliation starts none and introduces no key or socket operations; existing relay security remains unchanged.
- Errors/logs: SHOULD FIX: verify read/write failures never expose payloads, cursors, source IDs or filesystem error text; reuse content-free classifiers.
- Concurrency: synchronous pre-producer call avoids races; partial writes use existing store rollback and successful prior interruptions suppress duplicate recovery.
- Threat model: no new mobile authority or transport surface; `legacyHistoryType` excludes all new facts regardless of visibility. Agent/background closure is deliberately deferred to #2969.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-08

## Revisions

- 2026-10-08: reused untagged turn IDs across legacy delimiters require recovery to identify the original opening independently of the scope where its closure is appended. Add optional `runtimeHistoryFact.TurnOpenedEntryID`, referencing the earliest recorded main opening evidence. Startup closures retain this durable identity; the backward fold applies it across delimiter scopes without inventing provenance. Runtime facts omit it. This adds one local production field, keeping total work below 800 lines.
- 2026-10-08: legacy denials lack parent attribution. Track parent-attributed turn evidence to exclude child-only denial candidates while allowing an identifiable denial-only main turn. Validate registry IDs before logging them, since `Registry.Load` does not validate their shape. Failure tests distinguish actual read/write failures from this invalid-ID rejection.
- 2026-10-08: verifier finding 1 exposed an outdated boundary assertion in `internal/e2e/relay_v2_history_test.go` → `TestRelayV2_ConversationHistory`. Keep the six legacy seeds and assert the added hidden restart divider in raw history. Walk with two-entry raw pages (one legacy entry on the terminal page), an exact raw-log-sized page (empty terminal page), and one-entry raw pages (empty nonterminal divider page), preserving raw cursors, AtStart, newest-first seeded IDs and lifecycle exclusion. Production contracts remain unchanged. Run the named regression and the touched hermetic e2e package with the race detector; the dispatcher owns the full `make check` gate.
