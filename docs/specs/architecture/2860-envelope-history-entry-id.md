# Optional envelope history entry id (#2860)

## Files read

- `internal/protocol/envelope.go` → `Envelope`: pointer plus `omitempty` precedent in `EventID`.
- `internal/protocol/envelope_test.go` → `TestEnvelope_EventIDOmitempty`, `TestEnvelope_RoundTrip_Full`, `TestEnvelope_RoundTrip_Minimal`: optional-field assertions and compacted fixture byte stability.
- `internal/protocol/history.go` → `HistoryEntry.ID`: durable per-conversation uint64 namespace.
- `docs/knowledge/features/protocol-package.md`, `protocol-package-types-envelope.md`, `protocol-package-types-history-payloads.md`: pure-data boundary and distinct ring/history namespaces; current code supersedes the envelope overview's old per-conversation ring claim.
- `docs/knowledge/features/development-verification.md` → protocol evidence guidance: assert raw wire keys as well as decoded values.
- `docs/protocol-mobile.md` → Wire shapes / Application envelope and Security model: optional fields and existing encrypted transport boundary.
- `CODING-STYLE.md`: stdlib table-driven tests and formatting.

## Change

Add `Envelope.HistoryEntryID *uint64` with JSON tag `history_entry_id,omitempty` beside `EventID`. It identifies `HistoryEntry.ID`, the durable per-conversation entry used by `mark_conversation_read.up_to`, independently of connection `ID` and daemon-wide ring `EventID`. Real entries start at 1. Nil means absent and requires history/list fallback. This declaration adds no producer, validation, goroutine, I/O or new error path; emission belongs to #2861. No concurrent feature branch touches the target files after fetching origin.

Sizing: one wire-contract deliverable, approximately 120 written lines including tests and this plan, zero new exported types/interfaces, zero consumer updates, two acceptance criteria, zero new reject branches. All five limits remain within budget.

## Testing strategy

Add table-driven encode/decode/re-encode cases with distinct envelope/ring/history ids, asserting the numeric JSON key and preserved values (including a uint64 above the signed range). A nil case asserts complete key omission. Existing full/minimal legacy fixture tests assert a nil decoded pointer and continue proving byte-identical compacted round trips. First run the new tests against the old declaration and observe the missing-field compile failure, then implement the field. Run `go test -race ./internal/protocol/...`, `go vet ./...`, and `go build ./cmd/pyry` (output binary in scratch). The verifier owns the full-module gate.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md`, § Wire shapes, “Application envelope”: add the optional uint64 to the field table and explain its durable per-conversation read-mark meaning, distinct from `id` and `event_id`; real entries are >= 1. Both table and prose must state emission awaits #2861 and absence requires history/list fallback.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `json.Unmarshal` structurally decodes `Envelope` without authenticating an id. `HistoryEntryID` is metadata, never a capability or an authorization decision; no consumer is added.
- [Tokens, secrets, credentials] This numeric history lookup id carries no credential; no credential paths are touched.
- [File operations] The production change is a struct field; it cannot resolve paths or read/write history files.
- [Subprocesses] `Envelope` is pure data and starts no process.
- [Cryptography] The field stays inside the existing application envelope; no cipher, nonce, key or comparison is changed.
- [Network and I/O] No reader or allocation from this value is added. Existing transport size limits continue to apply; stdlib JSON enforces uint64 representation.
- [Errors, logs, telemetry] No logging, metrics or error construction is added; payload bytes remain opaque.
- [Concurrency] No shared state or goroutine is added. Pointer ownership follows the existing `EventID` convention.
- [Threat model] Existing encrypted transport and content sanitisation obligations in Security model remain applicable. OUT OF SCOPE: #2861 owns producer provenance and associating the id with the actual stored conversation entry.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06
