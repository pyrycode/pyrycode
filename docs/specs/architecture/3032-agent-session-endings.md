# Agent and background-work session endings

## Files read

- `cmd/pyry/runtime_history.go` → `closeRuntimeSource`: captured source, epoch and incarnation closure under publication coordination.
- `cmd/pyry/agent_history.go` → `observeAgentHistory`, `observeTaskIdentity`, `observeGoneRoster`: retained identities and irreversible task/denial evidence; foreground results need separate retention.
- `cmd/pyry/interactive_turn_v2.go` → `convTurnState`, `endTurn`, `releaseConversation`: agent evidence survives main-turn completion and sealed late traffic.
- `cmd/pyry/startup_history.go` → `readStartupMainWork`, `reconcileStartupHistory`: raw-page failure atomicity and closure-before-divider point.
- `cmd/pyry/history_projection.go` → `historyEntryShown`: raw visibility is independent of transport eligibility.
- `cmd/pyry/legacy_runtime_receipts.go` → `legacyRuntimeReceipt`, `legacyHistoryReader`: validated nonvisual receipts and independent legacy unread accounting.
- `cmd/pyry/agent_history_gone_test.go` → lifetime and late-link scenarios; `startup_history_test.go` → raw-store recovery fixtures.
- `docs/knowledge/features/history-package.md` and producer lifecycle/legacy compatibility topics: never infer outcomes from launch reports; restart references must address original work across delimiters.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Sessions, agents, messages, read marks: inferred endings precede dividers without adding app content.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md`: behavioral race tests, content-free failures, no source-line citations.

## Context

Unfinished Claude agents and background tasks currently disappear at teardown without an ending fact. Recovery handles only main turns. This adds one closure behavior at runtime and restart using the existing history log; no additional decision record is needed.

Sizing: approximately 390 production lines, 300 test lines and 75 plan lines (765 total); zero exported types/interfaces, at most four updated consumers, four acceptance criteria and fewer than ten distinct reject branches. This remains one independently verifiable session-closure behavior. No other fetched feature branch overlaps the target files.

## Design

Add shown `agent_ended_with_session` facts carrying available call/task/parent/tool identities, optional durable lifetime, cause and occurrence time. Recovery additionally carries separate original call/task observation entry IDs, so a saved closure cannot end a reused identity in another legacy scope. Missing identities and provenance are never minted.

Retain foreground result status separately from permanent denials/task endings. At closure, a complete task link takes precedence over launch results. Emit tasks first with linked call attribution, then remaining unfinished calls, marking both identities closed. Invoke closure from `closeRuntimeSource` even without an open main turn, before retirement and divider publication. Keep existing epoch/incarnation selection and main-work behavior.

Startup reads compact attribution projections from raw `Store.Page`, retaining no prose, and folds oldest-first. Groups use recorded source plus durable lifetime when supplied, otherwise legacy session scope. Modern facts precede their mapped reports and establish the active lifetime for that source; mapped legacy reports supply attribution only where durable facts are unavailable. Delimiters bound legacy identities; daemon-restart dividers do not. Saved observation references suppress original work independently of append scope. Complete both startup reads before any reconciliation write, then append agent endings before the restart divider. No live/replay publication at startup.

Extend receipt validation to accept modern lifetime attribution or explicit original recovery references, a usable work identity, conversation, cause and occurrence time. Raw facts remain excluded; receipts use successful durable IDs/timestamps and existing unread accounting.

## Concurrency model

No new goroutines or locks. Runtime evidence belongs to the drain's selected state under existing publication coordination. Startup runs under instance ownership before producers; reads finish before writes for each conversation.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Main turn ends while background work survives; repeated boundary and late reports | `TestAgentSessionEndings_Runtime` |
| Result before/after link, denial, terminal task report and gone | `TestAgentSessionEndings_Runtime` |
| Same-session child exit, stale exit, replacement incarnation and other source/conversation | `TestAgentSessionEndings_Isolation` |
| Restart twice, old legacy scope, reused IDs, later delimiters and saved closure references | `TestAgentSessionEndings_Startup` |
| Raw pagination/segment rollover, missing/truncated identities, unavailable attribution | `TestAgentSessionEndings_Startup` |

## Error handling

Use existing best-effort append and content-free discriminants. Nil/failed storage cannot publish a receipt. Any startup read failure skips all closures and the divider for that conversation. Codex sources receive no agent endings.

## Testing strategy

Write closure-hook tests first and observe missing endings. Use real history stores, reopened reads, explicit ordering/metadata assertions, late links/reports, lifecycle isolation and legacy receipt/unread assertions. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-3032/pyry ./cmd/pyry`. The dispatcher owns `make check`.

## Open questions

None.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/history-package-producers.md`, “Producers (#2114, #2115)”, and linked `history-package-producers-runtime-lifecycle.md` runtime/startup and agent/task sections with ended-with-session causes/identities, linked-work deduplication, retained work after main-turn end, closure-before-divider ordering, exactly-once recovery and legacy/unavailable attribution.

Pending documentation stage: update linked `history-package-producers-legacy-compatibility.md`, “Legacy eligibility and explicit visibility (#2965)”, with shown visibility and validated receipt compatibility under #3026/#3029, distinguishing raw visibility, legacy unread targets and foreground presentation.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: SHOULD FIX: truncated task/call IDs must be rejected independently in startup projections; source/lifetime/scope must isolate joins, and saved references must target original evidence.
- Tokens and cryptography: no new credentials or cryptography; existing minted lifetime is retained, never regenerated by recovery.
- File operations: existing `Store.Page` and append containment/permissions are reused; IDs never become paths outside that boundary.
- Subprocesses: no child launch or command construction changes.
- Network/I/O: existing bounded parsed events and raw page sizes are reused; compact projections exclude historical content. No new network reads.
- Errors/logs: SHOULD FIX: failures use existing content-free classifiers; no payload, source ID, cursor or filesystem error text in new logs.
- Concurrency: existing single drain owner and startup-before-producers prevent concurrent evidence mutation; partial durable writes are recovered from the surviving facts.
- Threat model: receipt validation must exclude malformed facts, preserving legacy barriers; inferred status remains descriptive and never authorizes actions.

**Reviewer:** builder (self-review)
**Date:** 2026-10-09

## Revisions

- 2026-10-09: Startup uses a second raw-page read of compact attribution projections, completed before any writes, instead of adding projection state to `readStartupMainWork`. This preserves the existing main-turn reader's interface and error behavior. `closeStartupAgentWork` runs immediately after main closure and before the divider. Existing startup and sealed-gone assertions now include agent endings and prohibit gone after a saved session ending. `TestAgentSessionEndings_DrainOrdering`, `TestAgentSessionEndings_ReceiptValidation` and `TestAgentSessionEndings_ReportedRecovery` add ordering, malformed-receipt and saved-closure/late-report proofs.
