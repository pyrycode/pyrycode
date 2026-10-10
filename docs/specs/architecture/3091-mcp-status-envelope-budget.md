# MCP status envelope budget

## Files read

- `internal/turnbridge/outbound.go` → `MapEvent`, `maxSlashCommandListBytes`, `admitContextUsageRow`: MCP currently copies every row; siblings charge encoded wire rows.
- `internal/protocol/interactive_session.go` → `MCPStatusPayload.MarshalJSON`, `MCPServerStatus`: nil becomes [], and every retained row publishes five string keys.
- `internal/protocol/envelope.go` → `Envelope`: source-session metadata and four uint64 identities need reserved space.
- `internal/streamsup/parser_queries.go` → `decodeMCPStatus`: producer retains 16 rows and caps only Error at 256 UTF-8 bytes.
- `cmd/pyry/session_router.go` → `resolveBoundMCPStatus`: requester-only readings share MapEvent with automatic publication.
- `cmd/pyry/daemon_live_size_test.go` → `TestDaemonLiveMappedInventoryBounds`: hostile MCP admission is currently skipped; fresh and clear forms need coverage.
- `internal/streamsup/mcp_status_event_test.go` → `requireOneMCPStatus`, `mcpStatusLineFixture`: decoder fixtures and producer bounds.
- `docs/knowledge/features/turnbridge-package-outbound-adapter-map-event.md`: budget encoded wire rows, preserve prefixes, and leave normalization to payload types.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` § Daemon-retained live state: retention rejects oversized readings independently of mapping.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: prove complete envelopes and separately test producer limits; assert the wire keys.
- `CODING-STYLE.md`: new MCP helper/test files keep this family out of already oversized outbound files.

## Context

Producer count/Error limits do not bound MCP's other strings or JSON escaping. A hostile 16-row report cannot be sent or retained. This ticket delivers one mapping contract, with no API change or retention redesign. No decision record is needed. No overlapping feature branches touch the planned files.

## Design

MapEvent delegates MCP payload construction to a package-private helper in `internal/turnbridge/outbound_mcp_status.go`. The helper retains the longest whole-row prefix whose encoded servers array is at most 63000 bytes, including two brackets, separators and encoding/json escaping. All five retained values cross verbatim. Stop at the first rejected row, including an individually oversized first row. Nil/empty input still maps successfully; MCPStatusPayload owns nil-to-empty wire normalization.

The 2519-byte envelope reserve covers a 36-byte conversation ID and 256-byte producing-session ID under sixfold escaping, maximal valid timestamp representation, max uint64 ID/correlation/event/history counters, payload syntax and max int dropped count. Tests marshal the complete Envelope at an exactly full array budget, including omitted and null session variants. The helper adds len(source)-len(retained) to upstream omissions using overflow-safe saturation at math.MaxInt, without changing the source slice or its rows.

## Concurrency model

Pure synchronous mapping with fresh retained slice; no goroutines, locks or shared mutable state.

## State transitions and identity reuse

None: the mapper stores no lifecycle or identity state. Existing owner lifecycle remains unchanged; `TestDaemonLiveMappedInventoryBounds` under go test -race proves fresh MCP admission and its family clear.

## Error handling

A wire-row marshal failure stops prefix admission and accounts the tail as omitted. The current flat string row cannot produce a marshal error. Oversized input maps to an empty or shorter successful reading; drop-count overflow saturates rather than wraps.

## Testing strategy

Write failing hermetic tests first. Table-driven mapper tests cover hostile 16-row reports, each oversized field, exact and one-byte-over boundaries, separators, escaping/multibyte text, prefix stopping, ordinary/empty payloads, upstream counts, saturation and event immutability. Full-envelope tests use maximal metadata on every case. Add decoder-to-mapper coverage proving producer plus mapping omissions and Error's upstream UTF-8 cap. Remove the owner MCP skip and assert fresh/clear envelope sizes; existing arbitrary oversized-envelope rejection stays covered. Run race tests for turnbridge, streamsup and cmd/pyry, go vet ./..., and go build ./cmd/pyry. Full-module verification belongs to the verifier; no live Claude turn is required.

## Open questions

None. Sized at roughly 400 written lines, zero exported types, zero simultaneous API consumer changes, three acceptance criteria and no state-machine reject branches; all five limits are within the ticket ceiling.

## Documentation handoff

Pending for the documentation stage:
- `docs/protocol-mobile.md` § mcp_status and the MCPStatus row in `docs/knowledge/features/turnbridge-package-outbound-adapter-map-event.md`: state that mapping retains whole rows in source order within a 63000-byte encoded-array budget, adds omissions to the producer count and saturates an unrepresentable count. Retained fields remain verbatim after existing producer limits.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter.md` § Daemon-retained live state: replace the pending #3091/skipped-assertion paragraph with the completed MCP budget and fresh/clear regression contract.

## Security review

**Verdict:** PASS

**Findings:**
- [Trust boundaries] MCP strings remain untrusted subprocess reports. `decodeMCPStatus` bounds producer count/Error; mapping only bounds wire size, granting no authority and preserving inert retained text.
- [Tokens, secrets, credentials] No credential generation, storage or lifecycle changes; the helper neither logs nor exposes excluded source fields.
- [File operations] No production file I/O or path handling; every row field stays inert text.
- [Subprocesses] No execution or argument changes; this only maps existing typed events.
- [Cryptography] No keys, nonces, randomness or encryption changes.
- [Network and I/O] Encoded-array admission reserves source metadata under MaxThreadEnvelopeBytes; exact-budget hostile envelope tests protect against escaping-based oversize. Existing transport/owner rejection remains in force.
- [Errors, logs, telemetry] No log or telemetry additions. Row rejection counts omissions without including report contents in an error.
- [Concurrency] Fresh slice and read-only source access retain the mapper's pure contract; no locks or goroutines added.
- [Threat model] Addresses the observed oversized-report denial of delivery. Remote-actuation authentication, replay and relay confidentiality boundaries remain in existing consumers; this mapper makes no capability decisions.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10
