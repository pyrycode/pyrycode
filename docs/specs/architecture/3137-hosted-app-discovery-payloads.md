# Hosted-app discovery and lifecycle payloads (#3137)

## Files read

- `docs/specs/architecture/3120-hosted-app-contract.md` → Registration, observed state and safe publication; Capability and envelope vocabulary; Errors and lifecycle test obligations; Shared JSON examples: normative wire shapes and examples.
- `internal/protocol/envelope.go` → `Envelope`, `IsKnownAppType`: correlation and optional metadata; no new routing acceptance.
- `internal/protocol/handshake.go` → `HelloClientPayload`, `HelloAckPayload`, `ErrorPayload`: reuse handshake and bridge-error DTOs.
- `internal/protocol/codes.go` → `TypeReplySuggestion`: declaration precedent and AST-visible type vocabulary.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`: classify all six frames as v2 only.
- `internal/protocol/handshake_test.go` → `TestCapability_Constants_MatchSpec`: literal capability drift check.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`: decoded payload must be marshalled, not the original raw payload.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler`: declare pending producers/handlers without installing them.
- `internal/relay/v2session_handshake.go` → `supportedV2Capabilities`: hosting capability stays unadvertised.
- `docs/knowledge/features/protocol-package.md`, `protocol-package-drift-detectors.md`, `protocol-package-types-history-payloads.md`, `protocol-package-reply-suggestion-payload.md`: typed re-encoding and separate nil-slice encoding proof are necessary.
- `docs/knowledge/features/development-verification.md` → Protocol boundaries; `docs/knowledge/decisions/037-capability-strings-not-version-numbers.md`; `docs/protocol-mobile.md` → Security model; `CODING-STYLE.md`: validation, capability and testing boundaries.

## Context

Provide one data-only discovery/lifecycle contract for paired-client implementers and downstream producers #3138/#3123. Hosting remains pending. No additional decision record is needed. One deliverable, three acceptance criteria, approximately 450–600 written lines, five exported types, zero consumer migrations, zero state-machine reject branches. Recount against the finished plan: all limits remain satisfied. No fetched feature branch overlaps the planned files.

## Design

Add `hosted_apps.go` with `CapabilityHostedAppsV1` and five types: `HostedAppRecord` (also the whole `app_updated` payload), `ListAppsPayload`, `AppsPayload`, `AppRemovedPayload`, and `AppCancelPayload` (shared by cancel and cancelled). Record release pointers and its two-field anonymous error object have no `omitempty`; page `NextCursor` is also required nullable. Optional request cursor and cancellation app ID omit when unset. Use `uint64` for revisions/request IDs, preserving exact JSON integers. `AppsPayload.MarshalJSON` normalizes nil items to `[]` without mutating the receiver.

Add six frame constants in `codes.go`, and classify them in the protocol partition and composition-root guard with their actual directions and pending producer/handler owners. Add a literal capability drift assertion. Reuse `Envelope` and `ErrorPayload`; add no hosted metadata wrapper or registration dependency. Neither `inboundAppTypeSet` nor `supportedV2Capabilities` changes.

## Concurrency model

DTOs and value-receiver serialization only; no goroutines, I/O or shared mutable state. Tests run in parallel and under the race detector.

## State transitions and identity reuse

None: these declarations carry lifecycle data but own no lifecycle, connection or identity state. Producer generation, repeated cancellation and paging reconciliation tests belong to #3138/#3123.

## Error handling

Marshal returns encoding errors. The embedded record error has only code/message; bridge errors reuse `ErrorPayload`. Decoding establishes DTO structure, not validation: safe-integer bounds, required-key presence, cursor semantics, direction, forbidden metadata and static safe messages remain handler obligations.

## Testing strategy

Write tests first and observe a missing-declaration failure. Extract unchanged relevant JSON examples from the committed #3120 contract: negotiation, discovery/update, cancellation, bridge error and removal. Decode each typed payload and compare its complete expected value, marshal it into its envelope, and inspect both original and re-encoded key sets/nulls without float64 conversion. Additional table cases cover empty/zero-value pages, cursor present/absent, active/pending/error present, list and asset/API cancellation, and integer endpoints 0/max (record/tombstone 1/max). Verify distinct cancel versus target correlations, notification omission and all forbidden metadata omissions. These are offline DTO checks, not live negotiation evidence.

Run `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`, scratch-output `go build ./cmd/pyry` and `git diff --check`. The verifier owns the full-module gate.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage: in `docs/protocol-mobile.md` under `Hosted apps (v2)`, identify these Go-backed discovery/lifecycle/cancellation shapes and their direction, correlation and nullability rules. Mark the six affected protocol table rows and explanatory text as declared ahead of their producers. Explicitly say producers (#3138/#3123) and capability activation are pending implementation; declaration alone is not shipped hosting support.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] OUT OF SCOPE: DTO decoding is not authenticated or validated input. #3138/#3123 own required-field presence, safe integers, cursor validity, direction and forbidden metadata checks. The declarations add no accepted routing type.
- [Tokens, secrets, credentials] No findings: new payloads carry no credentials; handshake tests use only the contract's synthetic token. No credential lifecycle changes.
- [File operations / subprocesses] No findings: production declarations perform no filesystem operations or process execution. Test fixture reads use a fixed repository path.
- [Cryptography] No findings: reuse `Envelope` inside existing Noise transport; no crypto changes and no `payload_encrypted` flag on app examples.
- [Network and I/O] OUT OF SCOPE: frame caps, deadlines and bounded workers belong to #3138/#3123. The capability is absent from `supportedV2Capabilities`, so declarations enable no network feature.
- [Errors, logs, telemetry] No findings: no logging/telemetry added; record error deliberately excludes `ErrorPayload`'s retry and contextual fields. OUT OF SCOPE: producers enforce static safe messages under #3138/#3123.
- [Concurrency] No findings: value-receiver nil-array normalization avoids mutation; no workers or synchronization are introduced.
- [Threat model] OUT OF SCOPE: paired-device authorization, host/app binding, cancellation and resource containment remain #3138/#3123; service readiness remains #3122. Data declarations grant no authorization or hosting support.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-10
