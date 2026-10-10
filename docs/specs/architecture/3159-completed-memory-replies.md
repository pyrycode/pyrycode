# Completed main replies in memory transcripts

## Files read
- `cmd/pyry/memory_transcript_fold.go` → `feed`, `files`: delivery identity, grouping and immutable closure.
- `cmd/pyry/memory_transcripts.go` → `startMemoryTranscripts`: serialized polling, retry and joined shutdown.
- `cmd/pyry/memory_transcript_files.go` → `publishMemoryTranscript`: confined private atomic publication.
- `cmd/pyry/memory_transcripts_test.go` → replay, worker and storage tests: reuse helpers and security/lifecycle proofs.
- `internal/thread/main.go` → `mainDecode`, `mainWork`, `closeText`: raw source/generation joins; text closure is not completion.
- `internal/thread/fold.go` → `Feed`, `Items`, `decode`: malformed/foreign rejection and raw text preservation.
- `internal/thread/boundaries.go` → `boundary`: legacy scope advances once per paired boundary, except restart.
- `cmd/pyry/conversation_handover.go` → `switchRecentExchanges`: assistant-only intervals must not inherit a previous exchange.
- `docs/knowledge/features/thread-package-main-thread-folding.md` → main-work and scoped closure: display fallback never joins tagged and legacy work.
- `docs/knowledge/features/history-package-shape.md` → Memory transcript view: late text must not move closure.
- `docs/knowledge/features/thread-package.md`, `cli-verb-dispatch.md`, `development-verification.md`, `CODING-STYLE.md`: replay, memory activation and focused proof conventions.

## Context
Export completed conversational Claude/Codex replies through the existing recent-transcript worker, independent of clients, search and capture. Thread status and revision cannot prove completion or the greatest contributing text ID. Retain raw evidence beside the fold without changing history or thread APIs. No decision record is needed.

## Design
Add private source/turn lifetime evidence in `cmd/pyry/memory_transcript_replies.go`. Recorded metadata distinguishes agent/session sources from legacy boundary scopes. Explicit openings create generations; valid main work establishes implicit legacy openings. Recovery references select only a matching original opening. A lifetime's first valid terminal wins: `turn_end` permits export, interruption withholds permanently. Error/cancellation `turn_end` still permits export.

Validate evidence as recorded objects, including identity-loss markers, required fields, scalar nulls and conversation/parent ownership. Keep raw timestamps and nonempty delta IDs independently from revisions. `Fold.Items` remains the authority for main text runs and delivered users; render `Content.text` with existing safe fences. Match each run's creating ID to its lifetime, and qualify that lifetime only through a delivered conversational user in the same raw source interval. Consume the pending exchange on a new lifetime so a later assistant-only turn cannot reuse it.

Use that delivered user's existing group to anchor replies to the correct logical session generation, including late predecessor runs after routing-ID reuse. Assistant identity is conversation plus creating ID; timestamp is its first delta's raw timestamp. Rows stay in durable creation/delivery order. Keep user rendering and transcript hashes unchanged. Add `Last text entry` as the greatest contributing delivered-user or assistant delta ID, separately from user-only `Last delivered entry` and immutable `Closing entry`.

No changes to polling, activation, storage, retention or publication. Other feature branches do not overlap the planned files. Estimate: approximately 730 written lines (350 production, 300 tests, 80 plan); zero exported types/interfaces, zero migrated consumers, four criteria, at most ten rejection categories. Rechecked against this plan: within all limits.

## Concurrency model
Each existing worker exclusively owns reader, fold and evidence. No new goroutines or locks. Existing cancellation joins discovery and workers; failure rebuilds evidence and fold together before retrying publication.

## State transitions and identity reuse
| Event | Race-enabled proof |
| --- | --- |
| Tool/user run closure without ending; later recorded completion | `TestMemoryTranscriptReplies`, `TestMemoryTranscriptReplyWorker` |
| Repeated/conflicting terminals; error/cancellation; interruption first | `TestMemoryTranscriptReplyTerminals` |
| Fresh opening with reused turn/source IDs; reference to an old opening | `TestMemoryTranscriptReplyScopes` |
| Tagged/legacy/source/conversation/child isolation and unknown provenance | `TestMemoryTranscriptReplyScopes` |
| Assistant-only wrap-up after exchange, excluded facts | `TestMemoryTranscriptReplyExclusions` |
| Reset/switch, late predecessor run/delta, routing reuse | `TestMemoryTranscriptReplyBoundaries` |
| Split replay/reconstruction of evidence and stable first-text rows | `TestMemoryTranscriptReplies` |
Existing worker/storage tests cover restart, retry and joined shutdown; broader interleaved worker recovery remains #3161.

## Error handling
Malformed, unrelated and unresolved evidence supplies no completion or exchange permission. Existing history/fold failure prevents publication and reconstructs from history; publication failure preserves complete files and retries. Logs remain fixed content-free reasons.

## Testing strategy
Write focused table-driven tests first and observe failures before implementation. Assert exact run text/whitespace, stable IDs/timestamps and distinct text/delivery/closure boundaries. A controlled-tick production worker smoke test proves withholding then publication within the configured 60-second scheduling bound. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and build `./cmd/pyry` with its artifact outside the worktree. Full-module verification belongs to the verifier; no live run is required.

## Open questions
None; implementation details remain private to the exporter.

## Documentation handoff
Pending for documentation stage:
- `docs/guide.md`, “Memory configuration” → “Recent conversation transcripts”: include completed Claude/Codex main replies and text around tools, 60 seconds after recorded completion, unfinished withholding, first-terminal/error/cancellation semantics and exclusions (reasoning, tools, child agents, injected/reset-wrap-up instructions and assistant-only replies, system/status, background capture, queued/dropped/lost/no-child). Remove assistant deferral; preserve automatic activation/location, lifecycle boundaries, complete open-session and closed-file retention, history as truth and managed-index/agent-search prerequisites. Explain separate user-delivery, eligible-text and closure boundaries.
- `docs/knowledge/features/history-package-shape.md`, “Memory transcript view”: scoped completion, stable first-text identity/timestamp and late assistant reconstruction; preserve recorded attribution and atomic publication.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: evidence validation and `Fold.Feed` reject malformed/foreign facts; raw sources, not display fallback, authorize joins. Parent-only and assistant-only lifetimes cannot inherit user permission.
- Tokens/secrets: no credentials are accessed. Conversation text is intentionally retained privately; no payload reaches diagnostics.
- File operations: existing hashed names and `publishMemoryTranscript` retain held-directory/no-follow operations, 0700 directories, 0600 files and synced atomic replacement; text and opaque IDs never become paths.
- Subprocesses: no new subprocess or shell execution.
- Cryptography: existing SHA-256 structured transcript identity only; no keys/nonces/comparisons added.
- Network/I/O: no network boundary added; history reads retain bounded ForwardReader chunks.
- Errors/logs/telemetry: failures retain fixed reasons without content, host paths or opaque IDs.
- Concurrency: serialized evidence has no lock ordering; existing joined cancellation and retryable snapshots preserve complete publication.
- Threat model: local history/subprocess content is inert fenced text, never instructions to the exporter. Index/search integration is owned by #3099/#3103; broader recovery proof is #3161.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-10
