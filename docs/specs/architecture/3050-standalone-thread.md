# Standalone conversation thread fold (#3050)

## Files read
- `internal/history/log.go` → `Entry`, `SessionProvenance`, `MaxEntryID`: raw input, explicit no-child and safe identities.
- `cmd/pyry/history_projection.go` → `historyEntryShown`, `legacyHistoryType`: visibility defaults and attachment-offer exclusion.
- `cmd/pyry/runtime_history.go` → `runtimeHistoryFact`, `runtimeDivider`: predecessor attribution, cause and handoff facts.
- `cmd/pyry/session_transition_v2.go` → `broadcast`, `transitionProvenance`, `toWirePayload`: successor provenance and mirrored eviction IDs.
- `cmd/pyry/prompt_answer_history.go` → `promptAnswerFact`, `recordPromptAnswer`: saved answers, not open prompts.
- `internal/protocol/{messaging,interactive,interactive_status,interactive_session,attachments}.go` → standalone payload DTOs: field types and optional token counts.
- `docs/knowledge/features/history-package{,-shape,-producers,-producers-runtime-lifecycle}.md`: unknown provenance cannot default to Claude; restart does not split legacy scope.
- `docs/knowledge/decisions/042-daemon-built-thread.md`: common item contract and six approved decisions.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: deterministic offline evidence and package-scoped race checks.

## Context
Create the conversation-owned deterministic fold underlying ADR 042. This slice adds only standalone messages, boundaries, compaction and notices. Main work, accepted sends, agents, cache and wiring remain downstream. No additional decision record is needed. No other fetched feature branch touches the planned files.

## Design
- Two production files: `internal/thread/fold.go` and `boundaries.go`; matching tests in the package.
- `New(conversationID string) *Fold` binds one owner; `Feed([]history.Entry) error` consumes strictly increasing valid IDs, retaining state across chunks. Invalid/out-of-order IDs return a content-free error before consuming that entry; earlier entries in the chunk remain consumed. Malformed/unsupported payloads consume version but change nothing else.
- `Version() uint64` and `Items() []Item` expose version and detached snapshots. `Item` holds ID/kind/order/rev, session/agent, explicit `NoChild`, optional turn/parent, status/active/shown/summary, subtype and copied raw JSON content. Raw kind-specific content preserves client metadata, boundary outcomes, answers and absent/zero counts without a new protocol contract.
- Typed DTO decoding plus required discriminating fields validates supported objects before changing fold state. Missing provenance remains unknown. Explicit provenance wins; only recorded Claude/Codex facts identify agents. Summaries contain one plain line; retained source content stays inert.
- Only user-role messages create delivered user items; other standalone kinds are done/inactive. Notice subtype is its source type; Codex reroutes remain banners. Explicit shown overrides defaults only for supported item-producing types. Open prompts, live readings and deferred facts never create items.
- Boundary pending queues use normalized payload occurrence time, previous/new IDs and legacy reason. Raw reset/clear/switch map to clear; sleep/capacity map to mirrored idle_evict. Matching consumes one pending raw boundary without modifying its item or rev, even across chunks. Other legacy boundaries stand alone.
- Raw divider item attribution uses its predecessor; future untagged items use its recorded successor. Legacy clear provenance can supplement the current matching successor, never rewrite earlier items or a newer boundary's fallback. Eviction and successor-free restart clear fallback. Each non-restart raw divider or standalone transition increments private legacy join scope; a redundant pair and restart do not.

## Concurrency model
No goroutines or locks. One conversation owner serializes Feed and snapshot access; independent folds share no mutable state. Input and returned payload bytes are not retained by reference.

## State transitions and identity reuse
| Event | Race-enabled evidence |
| --- | --- |
| Chunk split, including raw/legacy pair | `TestFoldReplay` |
| Repeated matching keys and duplicate legacy boundaries | `TestBoundaryPairing` |
| Consecutive switches, old pair after new boundary, eviction then ID reuse | `TestBoundaryAttribution` |
| Restart clears successor, preserves legacy scope | `TestBoundaryAttribution` |
| Equal entry IDs in separate conversations | `TestFoldReplay` |
| Repeated/invalid entry ID and detached snapshots | `TestFoldOwnership` |

## Error handling
Unsupported types, malformed objects, invalid field types, unsupported boundary causes/reasons and mismatched payload ownership create no items or boundary state. ID errors return generic errors. No payload logging, I/O or recovery side effects.

## Testing strategy
Write offline scenario tables before production code and observe the missing API failure. Assert whole snapshots, source content and expected identities/provenance/scope independently; compare every two-chunk partition with replay. Cover Claude, Codex, metadata-free and explicit none. Run `go test -race ./internal/thread`, `go vet ./...`, and `go build ./cmd/pyry` (output outside worktree). The dispatcher runs `make check` and full-module tests.

## Open questions
None. Raw content is the internal kind-specific representation until the downstream wire contract.

## Documentation handoff
Pending documentation stage: create `docs/knowledge/features/thread-package.md`, section “Main-thread folding”, describing standalone source-entry mapping, common identity/order/rev/version, visibility, boundary deduplication, session fallback, legacy scope, explicit no-child versus unknown provenance and live-state exclusions. State that main-work and accepted-send folding follow separately. Link it from `docs/knowledge/INDEX.md` under “Features” and `docs/knowledge/CATALOG.md` in the feature inventory.

## Security review
**Verdict:** PASS
**Findings:**
- Trust boundaries: SHOULD FIX in `Feed`: validate supported object shapes/ownership before mutating attribution or joins; unsupported shown=true never bypasses the item allowlist. Tests pin malformed boundary neutrality.
- Tokens/secrets: no credential handling; copied saved content is inert, with no logs or execution. Summaries use plain text with control characters removed.
- File operations, subprocesses, cryptography, network/I/O: no such operations in this package; supplied entries are in-memory facts. Persistence and authenticated daemon ownership belong to #3048/#3049.
- Errors/logs/telemetry: ID errors contain no payload, provenance or conversation data; no telemetry or logs.
- Concurrency: serialized owner and detached snapshots avoid mutable aliases; no goroutines or shutdown paths.
- Threat model: source content remains untrusted display data, never authorization, executable code or paths. Transport size/auth checks are outside this fold and owned by future thread protocol/wiring.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-09

Sizing rechecked: one deliverable, four acceptance criteria, two exported types, no consumers, at most ten rejection categories; approximately 740 written lines including plan and tests, below 800.

## Revisions
- 2026-10-09: producer review of `promptAnswerFact.SessionID` showed saved answers can retain an asking-session ID even when harness metadata is absent. Keep that recorded ID with unknown agent ahead of successor fallback; explicit entry provenance still wins. Metadata-free legacy dividers likewise retain their recorded session ID independently of whether they supply a successor. `TestBoundaryMalformedNeutrality` covers these cases and malformed boundary/visibility neutrality.
- 2026-10-09: verifier finding 1 showed DTO unmarshalling accepts null scalar fields and array elements as zero values. `decode` now checks all present known DTO fields recursively, including case-insensitive keys and nested saved answers, before any item or boundary mutation. Nullable pointers and nil slices remain valid; unknown fields remain inert. `TestMalformedRecordedFields` checks version-only consumption, pending-pair preservation, legacy scope, successor attribution and replay across chunks; `TestNullableRecordedFields` preserves legitimate nullable source content.
