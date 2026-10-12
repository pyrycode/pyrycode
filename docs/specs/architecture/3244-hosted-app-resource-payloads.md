# Hosted-app resource transfer payloads (#3244)

## Files read

- `docs/specs/architecture/3120-hosted-app-contract.md` → Capability and envelope vocabulary, Asset/API fields and HTTP semantics, Bounded encrypted transfers and retirement, Shared JSON examples: normative fields, correlations and limits.
- `internal/protocol/hosted_apps.go` → `AppCancelPayload`, `AppsPayload.MarshalJSON`: existing cancellation scope and required empty-collection precedent.
- `internal/protocol/hosted_apps_test.go` → `testHostedExamples`, `testHostedRoundTrip`, `testHostedPayloadKeys`: unchanged shared fixtures and typed re-encoding assertions.
- `internal/protocol/envelope.go` → `Envelope`, `inboundAppTypeSet`: optional correlation and host-scoped metadata omission; v1 exclusion.
- `internal/protocol/handshake.go` → `ErrorPayload`: reuse the existing bridge-error shape.
- `internal/protocol/codes.go` → hosted-app constants: additive declaration-only vocabulary.
- `internal/protocol/compat_test.go` → `v2OnlyTypes`, `TestTypeConstants_V1V2Partition`: classify every new name without widening v1 dispatch.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler`: explicitly classify pending requests and replies.
- `docs/knowledge/features/protocol-package.md`: pure-data package and consumer-owned semantic validation.
- `docs/knowledge/features/development-verification.md` → Protocol boundaries: inspect original and typed re-encoded bytes; decoded zero values alone do not prove required presence.
- `CODING-STYLE.md` and `docs/protocol-mobile.md` → Security model: Go conventions and authenticated encrypted transport boundary.

## Context

Declare the resource vocabulary shared by native viewers and daemon without activating production hosting. Discovery and cancellation DTOs already exist. This is one independently verifiable wire-contract deliverable, with three acceptance criteria. No new decision record is needed.

Sizing: approximately 500 total written lines including plan, tests and guard edits; exactly five exported payload types, zero consumer migrations, zero executable state-machine reject branches. The #3137 analogue supports this estimate. Rechecked against the written design: all five builder limits fit. No fetched feature branch overlaps the planned files.

## Design

Add five `TypeApp*` constants in `codes.go` and five structs in `hosted_apps.go`: `AppAssetRequestPayload`, `AppAPIRequestPayload`, `AppResponsePayload`, `AppResponseChunkPayload`, `AppResponseCreditPayload`. Fields follow the contract tables exactly. Strings carry padded base64 bodies/chunks unchanged; header objects use `map[string]string`. Only optional query has `omitempty`. Navigation, body, headers, size, chunk count and indices always serialize, including their valid zero/empty values. Request and response marshal methods normalize nil header maps to `{}` without mutating the caller.

Use `uint64` for request IDs and unsigned sizes/counts/indices, and `int` for HTTP status. DTOs do not enforce required presence, safe integers, exact JSON types, path/header validity, byte limits or authorization. Consumers own those checks.

Reuse `Envelope`, `ErrorPayload` and `AppCancelPayload`. Replies correlate through `InReplyTo` to the resource request; credit names it through payload `RequestID`, with no envelope correlation. Cancellation replies instead correlate to the distinct cancel ID. All hosted envelopes omit conversation/session/replay metadata and `payload_encrypted`.

Extend the existing payload-key helper for the new types and add focused resource tests in `internal/protocol/hosted_apps_resources_test.go`. Add names to both the v2-only map and partition enumeration. Classify request/credit handlers and response/chunk producers as pending #3247 in `excludedTypes`; leave guard assertions and `inboundAppTypeSet` intact. Capability activation stays pending #3248.

## Concurrency model

Pure data and serialization only; no goroutines, locks, I/O or mutable global state. Marshal methods use value receivers and replace nil maps only on the copy.

## State transitions and identity reuse

None: declarations hold no lifecycle state. Boundary tests cover IDs/correlations at 0 and 9007199254740991 and indices at 0 and 255 without claiming runtime retirement or reconnect enforcement.

## Error handling

Use standard JSON errors; introduce no bridge error codes or validators. Header normalization must not erase populated headers. Consumer validation and bounded routing remain #3247 responsibilities.

## Testing strategy

Write tests before DTO implementation and observe missing-declaration failures. Extract unchanged shared asset, API-write and cancellation JSON; assert every decoded field, original/re-encoded exact key sets, distinct correlations, opaque binary base64, empty-body SHA-256, integer boundaries and required empty header objects/body. Include a navigation request with omitted query. Measure fully serialized asset/API requests and response chunks, using 32768 decoded bytes, bounded maximum path/query, permitted headers and JSON escaping, within 65519 plaintext bytes.

Run `go test -race ./internal/protocol/... ./cmd/pyry/...`, `go vet ./...`, `go build -o /tmp/builder-3244/pyry ./cmd/pyry` and `git diff --check`. The verifier owns the full-module gate. No live-Claude proof or artifacts are needed for these declarations.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage: `docs/protocol-mobile.md`, `Hosted apps (v2)`, and its five resource vocabulary rows (`app_asset_request`, `app_api_request`, `app_response`, `app_response_chunk`, `app_response_credit`): mark Go-backed payloads declared ahead of routing producers; preserve direction, correlation, omission and byte limits. Resource producers (#3247) and final production capability activation (#3248) remain pending.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] OUT OF SCOPE: decoded DTOs are untrusted. Presence, exact types, safe integers, paths, headers, app/release ownership and authorization need consumer checks in #3247. Explicit comments preserve this boundary; `inboundAppTypeSet` stays unchanged.
- [Tokens, secrets, credentials] No credential fields or credential operations are introduced. App IDs and correlations convey no authorization.
- [File operations] No production filesystem access; request paths remain URL strings. Containment and safe asset opening belong to #3247.
- [Subprocesses] No process launch or environment inheritance.
- [Cryptography] Base64 strings preserve opaque bytes; SHA-256 is response metadata, not authentication. Existing Noise authenticates complete envelopes; no key/nonce changes.
- [Network and I/O] SHOULD FIX: prove final serialization fits 65519 bytes even with escaped headers/query and maximum API/chunk bytes. Implement focused size tests. Runtime admission, allocation and transfer limits are OUT OF SCOPE for #3247.
- [Errors, logs, telemetry] No new logging, telemetry or wire errors. Existing `ErrorPayload` remains the consumer's static-error contract.
- [Concurrency] Marshal methods do not mutate caller maps; no workers or shared state. Runtime credit/cancellation/reconnect isolation is OUT OF SCOPE for #3247.
- [Threat model] Declarations grant no routing or capability. v1 rejection and composition-root classifications remain checked; production activation belongs to #3248 and native-view isolation to the viewer implementation tickets.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12
