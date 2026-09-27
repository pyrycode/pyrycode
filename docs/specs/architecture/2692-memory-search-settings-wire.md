# 2692 — Memory search status in session settings wire

## Files read

- `internal/protocol/settings.go` → `SessionSettingsPayload`, `SessionCapabilities`: the existing settings shape, optional-report precedent, and comparable payload constraint.
- `internal/protocol/settings_test.go` → `TestSessionSettingsPayload_RoundTrip`, `TestSessionSettingsPayload_CapabilitiesPresence`: envelope round-trip and omission patterns.
- `internal/protocol/testdata/session_settings.json` → older reply fixture: preserves the pre-report JSON shape.
- `internal/memorysearch/detect.go` → `Availability`, `Provider`, `Result`, `Detect`: authoritative state vocabulary and missing-row semantics.
- `docs/knowledge/features/protocol-package-types-session-settings-read-payloads.md` → `SessionSettingsPayload` contract: the old fields are always present and the optional pointer keeps comparisons compiling.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: tests must decode, compare typed fields, and re-marshal the envelope.

## Context

Ticket #2689 supplies one agent-and-workspace detection result. This ticket defines its optional settings wire shape; #2693 will populate it. The protocol package stays a data-only leaf. The report concerns search access, not knowledge capture. No in-flight feature branch overlaps the protocol source or tests in this design.

## Design

- Add `MemorySearch *MemorySearchReport` to `SessionSettingsPayload` with `json:"memory_search,omitempty"`. Nil means no report and leaves older replies unchanged. The pointer preserves `SessionSettingsPayload` comparability.
- `MemorySearchReport` carries `availability` and `providers`. `MemorySearchProvider` carries `id`, `display_name`, `installed`, `enabled`, and `availability`. All inner keys remain present, including false booleans. Use strings for the detector's `available`, `unavailable`, `absent`, and `unknown` vocabulary, without coupling `internal/protocol` to detection logic. The producer in #2693 maps the detector result.
- Normalize a nil `Providers` slice to an empty array when marshaling a present report. A missing provider has no row; an empty report can carry aggregate `absent` or `unknown` according to the detector's evidence. Installation is separate from effective availability, so a disabled installed row keeps `installed: true` and `enabled: false`.
- Add shared full-envelope fixtures for available, disabled, absent, and incomplete-evidence unknown. Keep `session_settings.json` as the older omitted-report fixture.

## Concurrency model

Pure value types and JSON marshaling; no goroutines or shared mutable state.

## Error handling

This is a wire DTO, so it adds no detector or request validation. JSON type errors remain `encoding/json` errors. The future producer owns detection errors and deciding whether to omit the report.

## Testing strategy

- Table-driven fixture round-trips decode the full envelope, compare the entire typed payload with distinct expected values, and re-marshal into the envelope for byte-equivalent JSON.
- Cover all four aggregate states, the installed-but-disabled row, and omission in the older fixture. Check that every row field survives and that a present report with a nil or empty provider slice emits `[]`.
- Run race tests for `internal/protocol`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The producer and client documentation are separate handoffs.

## Documentation handoff

Pending documentation stage: update `docs/protocol-mobile.md` § Session settings → `session_settings` with the exact `memory_search` object, all four aggregate states, provider state meanings and missing-row rule, and compatibility when `memory_search` is omitted.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No finding: `SessionSettingsPayload` only decodes a client-visible JSON value. It does not grant access or mark an inbound value trusted; `Detect` and the #2693 producer own evidence and mapping.
- [Tokens and secrets] No finding: the new fields contain state labels and provider identity, with no credentials, search content, or capture data.
- [File operations] No finding: the runtime design reads and writes no files. Shared fixtures are static test inputs.
- [Subprocess] No finding: protocol marshaling invokes no command.
- [Cryptography] No finding: the design changes no encryption, keys, or random values.
- [Network and I/O] No finding: the envelope transport and its existing frame limits are unchanged. The new fields do not read from a socket themselves.
- [Errors and logs] No finding: there are no new logs or error strings containing payload content.
- [Concurrency] No finding: the types are value data and create no goroutines or locks.
- [Threat alignment] No finding in this DTO: status is advisory for UI, not authority for enabling a search provider. The existing authenticated transport controls who receives `session_settings`; #2693 owns producer scoping to the selected session.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-27
