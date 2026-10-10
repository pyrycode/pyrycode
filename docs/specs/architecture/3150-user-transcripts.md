# Delivered user transcripts

## Files read
- `cmd/pyry/main.go` → `runSupervisor`: shared history store, startup workspace and shutdown joins.
- `cmd/pyry/memory_roots.go` → `resolveEffectiveMemory`: validated daemon-owned destination, independent of search roots.
- `cmd/pyry/thread_shadow.go` → `discover`, `follow`: registered-chat discovery without delivery-path I/O.
- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: persists safe conversational text, excluding composed instructions and host attachment paths.
- `internal/history/forward.go` → `Forward`, `Walk`: bounded chronological replay, positioned retry and cancellation.
- `internal/thread/fold.go` → `Feed`, `Items`: inert content and recorded attribution.
- `internal/thread/boundaries.go` → `boundary`: paired dividers and successor fallback; eviction is not replacement.
- `docs/knowledge/features/history-package-shape.md` § Forward consumption: poll catch-up also covers direct writers without Tail notifications.
- `docs/knowledge/features/thread-package-main-thread-folding.md` § Accepted sends and durable outcomes: delivery Order survives suppression of the standalone row.
- `docs/knowledge/features/memorysearch-package.md`: export must not depend on search installation.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: atomic publication and controlled concurrency evidence.

## Context
History remains authoritative. Saved valid memory settings enable a derived user-only Markdown view; assistant replies belong to #3151. No capture or indexing service participates. #3089 overlaps `main.go` in unrelated wiring; edits remain local and additive. No decision record is needed.

## Design
Production stays in `cmd/pyry`: startup/discovery worker, transcript grouping/rendering, and private atomic publication. Startup resolves settings using the existing startup workspace base. Absent settings do nothing; invalid settings log a fixed diagnostic.
Each registered chat gets one serialized owner of a positioned ForwardReader, Fold and delivery timestamps. Poll every 10 seconds, immediately on discovery, using Walk through the available committed log rather than relying on Tail wakeups. Successful replay advances independently of publication; every pass renders the complete snapshot, so failed/missing files retry without losing earlier messages.
Only delivered `user_message` items are eligible, excluding explicit no-child records. Identity is conversation plus recorded routing session and recorded replacement generation; unknown provenance uses a separate namespace. Message identity uses delivery Order and timestamp uses the corresponding delivery entry TS. Decode text from Content, preserving whitespace; metadata is JSON-quoted and text uses a fence longer than any recorded backtick run.
Process Items in durable order. Distinct recorded predecessor/successor replacement IDs close the matching predecessor at the divider ID and start the successor. Paired dividers are already suppressed by Fold. Eviction, restart and same-ID respawn do not close anything. Unknown groups without proven predecessor identity stay open. Late predecessor deliveries select their own routing-session group; closure and greatest delivered-message Order are independent.
Hash structured identity into a fixed basename. Publication traverses directories without following symlinks, holds a directory descriptor, rejects unsafe existing targets, and uses descriptor-relative temporary creation and rename. New directories are 0700, files 0600; temporary files are synced and closed before rename. Only transcript storage is writable.

## Concurrency model
One discovery goroutine starts per-chat workers and sends coalesced tick requests. Each worker performs replay/publication off chat paths. Cancellation interrupts replay and stops workers; the returned startup cleanup cancels and joins discovery and all workers. No callback from a chat producer waits for export.

## State transitions and identity reuse
| Event | Race-enabled coverage |
| --- | --- |
| Delivery then later reconciliation | `TestMemoryTranscriptReplay` |
| Replacement, paired divider, late predecessor delivery | `TestMemoryTranscriptSessions` |
| Eviction/reactivation, restart and same-ID recovery | `TestMemoryTranscriptSessions` |
| Repeated replacement/reused routing ID | `TestMemoryTranscriptSessions` |
| New chat and interleaved commits during export | `TestMemoryTranscriptWorker` |
| Restart/missing derived files and publication retry | `TestMemoryTranscriptReplay`, `TestMemoryTranscriptStorage` |
| Cancellation during replay/publication, joined shutdown | `TestMemoryTranscriptWorker` |

## Error handling
History/fold failures preserve complete published files and retry. Publication errors preserve the old file and retry full snapshots. Cancellation discards unpublished work. Diagnostics contain fixed event/reason strings only, never errors, paths, content or opaque IDs.

## Testing strategy
Write focused production-composition tests first and observe their failure. Reuse memory settings helpers and real history/registry/Fold. Compare rebuilds, linked-message identity/timestamp, exact whitespace and unchanged source bytes. Controlled ticks demonstrate discovery and catch-up within the 60-second bound; gates exercise cancellation/join and writes during export. Storage tests cover symlinks, permissions, unsafe targets and retry. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`; full-module tests belong to the verifier. No live test is required.

## Open questions
None. Size sketch: approximately 365 production + 335 tests/helpers + 85 plan lines = 785 total written lines; zero new exports, one existing composition consumer, five acceptance criteria, fewer than ten worker error/retry branches. Recount before implementation handoff.

## Documentation handoff
Pending documentation stage:
- `docs/guide.md` § Memory configuration: add “Recent conversation transcripts”; automatic location and activation after restart, delivered user-text support/exclusions, 60-second bound, reset versus process lifecycle, complete open retention, closed retention and history as source of truth. Completed assistant text is pending #3151; search additionally requires managed index and agent-search integration.
- `docs/knowledge/features/history-package-shape.md`: add “Memory transcript view”, linking the exporter and explaining recorded attribution, stable identities, independent closure/message boundaries, late-record recovery and atomic derived publication without modifying history.

## Security review
**Verdict:** PASS
- Trust boundaries: Fold validates persisted DTOs; registry supplies membership/title only. Render only decoded text, never whole payloads.
- Tokens: no token handling; recorded text is private 0600 data and never diagnostic content.
- File operations: MUST FIX addressed in design: string-path checks leave symlink-swap races. Use descriptor-relative no-follow traversal/publication; hash identities, reject nonregular/unsafe targets, sync and close before rename.
- Subprocesses: none added.
- Cryptography: SHA-256 is a basename identity digest, not authorization or encryption.
- Network/I/O: no network surface added; reuse bounded history-reader chunks and cancel between chunks.
- Errors/logs: fixed content-free event/reason fields; underlying errors remain unlogged.
- Concurrency: serialized folds, context cancellation and explicit worker joins; no delivery locks acquired.
- Threat model: derived local files are private; no new relay authorization or credential capability. Existing history retention is unchanged.
**Reviewer:** builder (self-review)
**Date:** 2026-10-10

## Revisions
- 2026-10-10: final sizing is 362 production, 369 tests/helpers and 69 plan lines (800 total); no interface or lifecycle contract changed. Composition tests use a separate valid startup vault because a vault containing transcript storage is rejected by the existing resolver.
- 2026-10-10: security review found that destination canonicalization can hide a storage symlink before no-follow publication. Startup also compares the effective destination to the canonical home's fixed daemon-owned transcript location; redirects disable export with a content-free storage diagnostic. Reuse the existing polling helper and compare all source segment bytes; final sizing is 368 production + 360 tests/helpers + 70 plan lines = 798 written lines.
- 2026-10-10: clarify restart closure: a restart retaining its routing ID or lacking a recorded successor stays open, while a recorded distinct successor is a replacement and closes the predecessor. `TestMemoryTranscriptSessions` covers that distinction alongside routing ID reuse. Final written work: 799 lines.
