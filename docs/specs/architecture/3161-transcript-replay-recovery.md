# Assistant transcript replay and recovery gate

## Files read
- `cmd/pyry/memory_transcripts.go` → `startMemoryTranscripts`, `memoryTranscriptHooks`: serialized per-chat polling, replay and joined shutdown hooks.
- `cmd/pyry/memory_transcript_fold.go` → `feed`, `files`: first-text stamps, recorded grouping, independent delivery/text/closure boundaries.
- `cmd/pyry/memory_transcript_files.go` → `publishMemoryTranscript`: private descriptor-confined atomic replacement and cancellation before rename.
- `cmd/pyry/memory_transcripts_test.go` → `TestMemoryTranscriptWorker`, `TestMemoryTranscriptStorage`: persisted fixtures and existing storage/security checks to reuse.
- `cmd/pyry/memory_transcript_replies_test.go` → `testReplyEntries`, `TestMemoryTranscriptReplyTerminals`, `TestMemoryTranscriptReplyScopes`, `TestMemoryTranscriptReplyBoundaries`: first-terminal semantics and local fixture vocabulary.
- `internal/history/forward.go` → `ForwardReader.Walk`: bounded persisted callbacks and positioned replay; another Store supplies no wakeup.
- `docs/knowledge/features/history-package-shape.md` § Memory transcript view: eligibility, recorded attribution, stable identity and polling contract.
- `docs/knowledge/features/thread-package-main-thread-folding.md` § Scoped joins, pending outcomes and closure: tool closure is not reply completion; source and opening generations isolate evidence.
- `docs/knowledge/features/history-package.md`, `thread-package.md`, `development-verification.md`: owning maps and independent expected-result/barrier requirements.
- `CODING-STYLE.md`: local stdlib tests, joined goroutines and contract comments.

## Context
Merged #3159 supplies completion/rendering proofs. This ticket adds one hermetic persisted replay/recovery gate through the production startup worker. No production redesign, public API, capture, indexer, network or agent execution is needed. No decision record or product documentation change is required.

## Design
Add `cmd/pyry/memory_transcript_replay_test.go` with local fixtures and channel barriers. Two registered chats carry interleaved Claude/Codex text/tool/text facts; one history exceeds `history.MaxPageEntries` with inert status padding, placing a reply across actual persisted reader callbacks. Explicit expected message IDs, timestamps, text, recorded sessions and delivery/text/closing boundaries constrain complete Markdown and deterministic filenames before any reconstruction comparison.
Hold replay while committing completion/reconciliation facts; hold publication while committing boundaries/successor work. Separate Store instances append sequentially on the same root, avoiding unsupported concurrent writers. Verify first eligible controlled cycles, unfinished and interruption-first withholding, representative exclusions, mismatched scope/generation endings, reset/switch isolation, unknown provenance despite registry binding, and repeated late predecessor deltas without successor changes.
A recovery scenario holds replay and publication across shutdown, verifies stop joins, retries a recoverable publication failure on the same worker, and deletes/reconstructs derived files. Snapshot all source-history files after fixture commits and assert exact path/byte equality after recovery.
Sizing: one gate, three acceptance criteria, approximately 550–650 total written lines including this plan; zero exported types/interfaces, zero consumer migrations and zero new production reject branches. Existing remote feature branches #2873/#2882 do not overlap planned files.

## Concurrency model
Use existing discovery/per-chat goroutines only. Local hooks communicate through channels with bounded test deadlines. Publication holds expose staged complete files before rename; release and exact disk equality establish completion. Tests send only one tick per asserted cycle. Hook cleanup and worker cancellation release/join every hold before replacement startup; no timing sleep proves progress.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Persisted multi-chunk replay and commit during held replay | `TestMemoryTranscriptPersistedReplay` |
| Qualifying/error/cancelled completion after unrelated endings | `TestMemoryTranscriptPersistedReplay` |
| Commit during publication and first subsequent eligible tick | `TestMemoryTranscriptPersistedReplay` |
| Reset/agent switch; interrupted lifetime followed by conflicting ending; reused opening generation | `TestMemoryTranscriptPersistedReplay` |
| Late predecessor text followed by extension under stable first-text identity | `TestMemoryTranscriptPersistedReplay` |
| Stop during held replay/publication and replacement startup | `TestMemoryTranscriptPublicationRecovery` |
| Publication failure, later-tick retry, deletion and complete reconstruction | `TestMemoryTranscriptPublicationRecovery` |

## Error handling
Existing storage refusal/atomic publication tests remain the exhaustive security matrix. Inject a recoverable unsafe destination mode after staging to reject replacement while preserving prior bytes; restore mode and retry with the same running worker. Cancel a staged publication and assert previous complete files survive. Channel timeouts fail with the missing phase; cleanup must still release held callbacks.

## Testing strategy
Run focused new tests under `go test -race`; use temporary local production mutations, restored before commit, to demonstrate independently failing withholding and publication expectations. Run `go test -race ./cmd/pyry/...`, `go vet ./...`, and `go build -o /tmp/builder-3161/pyry ./cmd/pyry`. The verifier owns the full-module gate. Existing storage, diagnostic and completion matrices run in the touched package.

## Open questions
None. Documentation was delivered with #3159; no pending documentation or live proof.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] Explicit persisted metadata and inert payload fixtures cross `history.Store` into `startMemoryTranscripts`; expected unknown attribution and excluded child/capture text guard against trusting registry binding or arbitrary payloads.
- [Tokens] No credentials or tokens used; fixtures contain synthetic text only.
- [File operations] Reuse `TestMemoryTranscriptStorage` for containment, no-follow symlink refusal, 0700 directories, 0600 files and atomic rename. Recovery mutations affect only private test-home derived files; source-history path/byte equality is asserted.
- [Subprocesses] No subprocess, shell fixture or agent execution is introduced.
- [Cryptography] Deterministic SHA-256 expected filenames follow the existing identity contract; no cryptographic production change.
- [Network and I/O] Hermetic filesystem/channels only; replay crosses the existing bounded reader callback ceiling.
- [Errors/logs/telemetry] Reuse content-free diagnostic checks; worker failure logs use fixed reasons and tests use synthetic content.
- [Concurrency] Channel holds have explicit release/cancellation and cleanup; stop must join before replacement starts. Sequential independent Store writes avoid unsupported overlapping append owners.
- [Threat model] Local derived transcript integrity/attribution is the tested threat. Relay/authentication surfaces are unchanged and no new threat is deferred.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-11

## Revisions
- 2026-10-11: the unsharded package race run reached Go's default ten-minute timeout in the unchanged `TestMemoryTranscriptReplyShellObservation` partition matrix, without an assertion failure. Validation now enumerates all 1,429 top-level tests/examples/fuzz seeds and runs four disjoint race-enabled shards with a twenty-minute per-run limit; no test is excluded. The verifier still owns the unsharded full-module gate. The implementation and product contract are unchanged.
