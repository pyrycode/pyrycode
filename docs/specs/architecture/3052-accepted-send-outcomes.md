# Accepted sends and durable outcomes (#3052)

## Files read
- `internal/thread/fold.go` → `Feed`, `standalone`, `addItem`, `decode`, `Items`: ingestion, validation, copied snapshots and attribution.
- `internal/thread/main.go` → `mainWork`, `closeText`, `setContent`: indexes must stay stable; delivered messages split assistant text.
- `internal/thread/fold_test.go`, `internal/thread/main_test.go` → `TestExcludedAndMalformed`, `TestMainTextRuns`, `testMainReplay`: extend expectations and reuse partition checks.
- `cmd/pyry/queued_send_history.go` → `queuedSendFact`, `accepted`, `delivered`, `terminal`: safe acceptance schema and durable links, supported terminal reasons.
- `cmd/pyry/operator_message_history.go` → `operatorMessageHistory`: message append precedes the linked outcome and contains safe receiving content.
- `cmd/pyry/startup_send_history.go` → `closeStartupSends`: loss is an explicit restart record, not inferred absence.
- `internal/protocol/messaging.go` → `MessagePayload`: safe delivered content and client metadata.
- `docs/knowledge/features/thread-package.md` → Main-thread folding: null scalar validation, explicit no-child versus unknown, stable internal indexes and detached snapshots.
- `docs/knowledge/features/history-package-producers.md` → Accepted sends and linked outcomes; Operator delivery provenance: acceptance and receiving source differ.
- `docs/knowledge/decisions/042-daemon-built-thread.md` → Item model, Own and queued messages, decision 2: permanent acceptance identity and unordered queued/lost rows.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md` → contracts, package checks and validation boundaries.

## Context
One accepted send must remain one permanent user item through delivery, drop or recorded loss. The producers already supply the required durable entry references; only the conversation-local fold changes. No new decision record is needed. Fetched feature branches #2873 and #2882 do not touch `internal/thread`.

## Design
Add `sends.go` with a private recorded-fact DTO validated through `decode` and a `sendFact` fold method. `Feed` dispatches sends after ownership validation, before standalone processing. An acceptance creates an active, shown-by-default `user_message` at its entry ID/rev, with order zero and copied source content; normal `addItem` provenance and visibility apply.

Maintain conversation-owned maps from valid acceptance IDs and valid standalone user-message IDs to stable internal item indexes. A claimed message is marked consumed in its map and omitted from `Items`, rather than deleting storage and invalidating main-work indexes. Unmatched messages remain ordinary delivered rows and still close assistant text when ingested; acceptance does not close text.

A terminal must name an earlier valid acceptance that is still queued. Delivery additionally names an earlier valid, unclaimed user-message entry. No timestamps, text, app IDs or queue IDs participate. There is no pending forward reference: an invalid reference changes version only and does not reserve either entry. The first valid terminal wins permanently.

Delivery keeps the acceptance ID/kind/visibility, adopts the message's summary, receiving attribution and delivery-entry order, and becomes inactive/delivered with outcome-entry rev. Content starts from the delivered message JSON, preserving acceptance `device_id`, `message_id`, `accepted_at` and `client_sent_at` when recorded (case-insensitive field matching follows `decode`). Save the validated raw outcome under `outcome` to retain reason, occurrence time and links. Drop/loss retain acceptance content and provenance, save the same outcome, become inactive with order zero and outcome rev; drop is hidden, loss shown. Explicit outcome visibility cannot override these terminal semantics.

## Concurrency model
The existing owner serializes `Feed`, `Items` and `Version`. No goroutines, locks, producers or I/O are added. Input and output JSON stay detached.

## State transitions and identity reuse
| Event | Race-enabled test |
| --- | --- |
| Acceptance then message then delivery, including a chunk boundary before outcome | `TestSendDelivery` |
| Repeated/conflicting terminals; invalid link followed by valid link | `TestSendLinksAndTerminals` |
| Two acceptances attempting to claim one message; equal app IDs across devices | `TestSendLinksAndTerminals` |
| Recorded drop/loss and unresolved acceptance | `TestSendTerminalStates` |
| Independent owners reusing durable IDs, source changes, detached snapshots | `TestSendOwnershipAndProvenance` |
| Malformed input followed by valid resolution without changed joins/attribution | `TestSendMalformedNeutrality` |
| Acceptance during assistant text, then delivered message splitting text | `TestSendTextRuns` |
Every scenario compares replay with all two-chunk partitions where applicable.

## Error handling
Unsupported reason, malformed object/known field, invalid acceptance reference, invalid message reference, or already-terminal/claimed entry consumes version only. Existing entry-ID errors remain generic. Missing outcomes never infer loss or nonreceipt.

## Testing strategy
Write scenario tests first and confirm failure before implementation. Cover Claude, Codex, metadata-free and explicit no-child sources, full item/content contracts, link bounds/kinds/conversations, timestamp independence, repeated outcomes, visibility and detached state. Update the former accepted-send exclusion expectations. Run `go test -race ./internal/thread`, `go vet ./...` and `go build -o /tmp/builder-3052/pyry ./cmd/pyry`. The dispatcher runs `make check` and the full-module suite at the verifier gate.

Sizing rechecked: one deliverable, approximately 600 written lines (production, tests and this plan), zero new exported types/interfaces, zero consumer migrations, four acceptance criteria and at most ten reject branches.

## Open questions
None.

## Documentation handoff
Pending for documentation stage: extend `docs/knowledge/features/thread-package.md`, “Main-thread folding”, with permanent acceptance identity, durable linkage, queued/delivered order and provenance, retained safe sender fields, drop/loss visibility and first-valid-terminal/conflicting-link behavior. Replace the pending accepted-send statement with implemented behavior, and distinguish recorded loss from proof of nonreceipt.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] `Feed` enforces conversation ownership before `sendFact`; `decode` validates known scalars including nulls, time fields, arrays and links before any mutation. Durable maps contain only valid acceptance/user-message entries; arbitrary IDs cannot consume other kinds or another conversation's rows.
- [Tokens, secrets, credentials] Only already-recorded inert safe payloads are copied; no credentials are generated, accessed or logged.
- [File operations] No production filesystem operations or path interpretation; attachment IDs remain inert JSON.
- [Subprocesses] No production subprocesses or shell interpretation.
- [Cryptography] No cryptographic primitives or key handling are added.
- [Network and I/O] Fold takes supplied history in memory; transport/storage bounds and authenticated ownership remain upstream, with no new reader or endpoint.
- [Errors, logs, telemetry] Reuse generic ID errors; malformed facts silently advance version without echoing payloads or logging sender data.
- [Concurrency] Existing serial-owner contract covers maps and item mutation; no asynchronous work or partial durable writes.
- [Threat model] Claimed-message and first-terminal guards prevent supplied conflicting facts from reusing delivery evidence. Fold does not authenticate payload sender IDs; existing authenticated producers own that boundary. No new relay/mobile surface.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-09
