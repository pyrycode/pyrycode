# Scoped main-work folding (#3051)

## Files read
- `internal/thread/fold.go` → `Feed`, `decode`, `Item`: ownership, validation, immutable identity and detached snapshots.
- `internal/thread/boundaries.go` → `boundary`: legacy scope and display-only successor attribution.
- `internal/thread/fold_test.go` → `TestMalformedRecordedFields`, `TestFoldReplay`: neutrality and chunk equivalence.
- `internal/protocol/interactive.go` → `AssistantDeltaPayload`, `ToolUsePayload`, `ToolResultPayload`, `ToolDeniedPayload`, `TurnEndPayload`: recorded payload contracts.
- `cmd/pyry/runtime_history.go` → `runtimeHistoryFact`, `trackRuntimeTool`: interruptions and ordinary-call exclusions.
- `cmd/pyry/startup_history.go` → `readStartupMainWork`, `closeStartupMainWork`: earliest implicit opening evidence and explicit recovery references.
- `cmd/pyry/history_projection.go` → `historyEntryShown`: normal-end visibility.
- `docs/knowledge/features/thread-package.md` → validation and attribution lessons: null scalar validation and fallback must not become provenance.
- `docs/knowledge/features/history-package-producers-runtime-lifecycle.md` → Hidden openings and Startup reconciliation: references must never fall back to append-time scope.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Item model and Sessions, agents, messages, read marks: stable rows and main/child separation.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: Go conventions and validation boundaries.

## Context
Extend the supplied-entry fold with one main-work behavior. No consumers, storage or wire APIs change. ADR 042 already records the decision. Concurrent branches #2873 and #2882 do not touch the planned files.
Sizing: four acceptance criteria, zero new exports or migrated consumers, at most ten reject categories; approximately 720 written lines including tests and this plan, below the 800-line limit.

## Design
Add private main-work state in `internal/thread/main.go`; minimally extend `Feed` and extract its item-finalization helper in `fold.go`. Keys use recorded session provenance and turn ID, or legacy boundary scope and turn ID when metadata is absent. Successor attribution is display-only. Calls add their recorded call ID to that scope. Keep item indices rather than pointers into the growing item slice.
Assistant content retains the creating payload with accumulated `text`; tool content retains the creating payload and nested raw `result`, `denial` or `interruption` evidence. End rows retain the raw ending. All saved JSON is copied. Attribution and visibility are established at creation and retained on updates.
Text runs close on a scoped main launch, delivered user message, or matching ending. Delivered messages split open text runs in the conversation. Child traffic and accepted sends do nothing. Claude/legacy Agent and Task calls split text but create no ordinary call; Codex ordinary tools are not classified as Claude launchers by name alone.
Keep the first terminal report per scoped call, including reports preceding creation. A turn ending closes running text and calls without manufacturing result content. Late creations remain inactive and late reports cannot replace terminal state. Revisions are at least the creating ID. Hidden openings record evidence without rows; earliest implicit main evidence also establishes an opening. Referenced recovery validates original turn/source and targets its opening state exclusively; unresolved/mismatched references never alter append-time joins. A new explicit opening after an established opening starts a fresh generation for reused turn/call IDs.
Validate DTOs and common identity/parent/truncation fields before mutating any state. Missing, empty or dropped/truncated identity cannot join; malformed facts consume version only. Unsupported live/child lifecycle facts remain excluded even when shown.

## Concurrency model
The existing owner serializes all access. No locks, goroutines, subprocesses or shutdown paths are added.

## State transitions and identity reuse
| Event | Race-enabled coverage |
| --- | --- |
| Repeated deltas; launch/user/ending splits; child interleaving | `TestMainTextRuns` |
| Early result/denial/interruption; duplicates and conflicting terminals | `TestMainToolTerminals` |
| Early ending and late work; normal/abnormal visibility | `TestMainEndings` |
| Reused source/turn/call identities and legacy boundaries | `TestMainScopeAndRecovery` |
| Recovery references to explicit/implicit openings and mismatches | `TestMainScopeAndRecovery` |
| Chunk boundaries, independent owners and detached content | `TestMainReplayAndOwnership` |

## Error handling
Reuse generic entry-order errors. Malformed or unsupported content consumes valid IDs without altering joins, rows or attribution. Unresolved recovery references retain an ending row where applicable, with no closure target. No payload logging is introduced.

## Testing strategy
Write table-driven offline tests first and observe failure before implementation. Assert item identity, status, activity, revision and saved evidence for Claude, Codex and absent metadata. Compare every two-chunk partition with full replay around early facts, creation and closure. Run `go test -race ./internal/thread`, `go vet ./...` and `go build -o /tmp/builder-3051/pyry ./cmd/pyry`. The dispatcher owns full-module `make check`.

## Open questions
None. Accepted sends and child/background folding remain separate tickets.

## Documentation handoff
Pending documentation stage: extend `docs/knowledge/features/thread-package.md`, “Main-thread folding”, replacing the pending main-work statement with source mapping for assistant runs, ordinary calls and endings; text splits, scoped/early joins, first-terminal-wins, recovery opening references and child/Agent/Task exclusions. State that accepted-send linkage and agent/background folding remain separate extensions.

## Security review
**Verdict:** PASS
**Findings:**
- [Trust boundaries] SHOULD FIX: DTO decoding alone misses optional parent/truncation identity fields. Validate these in the main-work decoder before joins; dropped/truncated IDs must not identify work. Recorded source keys never use successor fallback.
- [Tokens] Content is inert recorded evidence; no credentials are generated, interpreted or logged.
- [File operations] The fold performs no filesystem operations or path construction.
- [Subprocesses] No subprocess execution is added.
- [Cryptography] No cryptographic operations or secret comparisons are added.
- [Network and I/O] Supplied-entry folding adds no network reads; transport limits remain outside this package.
- [Errors, logs, telemetry] Keep generic order errors and add no payload logs or metrics.
- [Concurrency] Single-owner serialization and copied JSON avoid shared mutable input/snapshot state.
- [Threat model] Conversation identity is caller-established and checked by `Feed`; joins are display data, never authority. Daemon/wire integration and its request limits remain downstream of this ticket.
**Reviewer:** builder (self-review)
**Date:** 2026-10-09
