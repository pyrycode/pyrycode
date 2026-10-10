# Hosted-app contract v1 (#3120)

## Files read

- `internal/protocol/envelope.go` → `Envelope`: connection-local correlation; app frames must not acquire conversation/session metadata.
- `internal/protocol/handshake.go` → `HelloClientPayload`, `HelloAckPayload`, `ErrorPayload`: additive negotiation and existing error shape.
- `internal/relay/v2session_handshake.go` → `negotiateCapabilities`: supported-set intersection; advertising support grants no authorization.
- `internal/dispatch/dispatch.go` → `Route`: authenticated handler boundary, known-type validation and static errors.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload`, `MaxAttachmentChunkBytes`: base64/chunk sizing precedent, not reused app DTOs.
- `internal/identity/server_id.go` → `ParseServerID`: canonical host UUIDv4 identity.
- `docs/protocol-mobile.md` § Application-envelope size cap and Security model: 65519 plaintext bytes, Noise authentication and relay blindness.
- `docs/knowledge/features/protocol-package.md` and `dispatch-package.md`: semantic validation belongs to consumers; handlers must send their own errors.
- `docs/knowledge/decisions/037-capability-strings-not-version-numbers.md`: features use literal capability strings, independently of release versions.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: examples must prove required, omitted and null fields distinctly.
- `CODING-STYLE.md`: persistence, error and file-scope conventions.
- Issue #3120 approved prototype and implementation map: responsive shell, SQL display preferences and fleet-service ownership.
- Node release policy and Node 24 SQLite API: Node 24 LTS; SQLite binding stability 1.2 (release candidate) from 24.15.0.

## Context

Define one shared, normative v1 contract before daemon, template and native-viewer implementations proceed. Apps are registered host resources, independent of channel sessions. The orchestrator is the first consumer and maintained test app. This design deserves a later decision record for the Node/SQLite runtime and native web-content isolation boundary; the documentation stage owns that record.

Sizing: four acceptance criteria; approximately 650–750 total written lines, including this plan and embedded examples; zero new exported types, zero consumer migrations, zero executable reject branches. The shipped #2576 compatibility spec is the contract analogue, with this ticket adding resource streaming and app persistence. Recount before handoff; remain below 800 written lines. No other fetched feature branch edits this artifact.

## Design

Append the normative contract below this committed plan. Define manifest identity/version/path rules, durable registration versus observed state, separate source/build/data roots, startup and SQL migration guarantees. Fix additive `hosted_apps_v1` negotiation and named `Envelope` payloads for discovery, notifications, assets, API requests, streaming credit and cancellation; use the existing `error` payload. Specify per-view host/app/release binding in Electron and Android, resource URL behavior, permitted HTTP semantics and bounded encrypted transfers. Embed parseable JSON examples as the common fixtures for downstream consumers, and assign implementation responsibilities using the issue's family roadmap.

## Concurrency model

This slice starts no processes, workers or goroutines. The contract will specify per-app serialized publish/migration, bounded request workers, streaming backpressure and cancellation ownership for downstream implementations.

## State transitions and identity reuse

None in this implementation: the only artifact is a specification. Its normative lifecycle/reconnect/update matrix will name the scenarios and implementation tickets that must supply race and native-viewer tests; those tests belong to the implementations rather than this prose-only slice.

## Error handling

Specify static wire errors and distinguish HTTP application responses from bridge failures. Uncertain mutations are never replayed on reconnect. A failed update preserves the previous release and usable SQL data; unrecoverable restoration is reported as failed, never successful rollback.

## Testing strategy

Before appending examples, run a scratch validator that fails on their absence. After writing, extract every JSON fence and check parsing, exact required/optional/null rules, message vocabulary, request/reply correlation, decoded bytes, chunk ordering/completion, and maximum-frame arithmetic independently of the prose. Review the four acceptance criteria and responsibility map. Run `git diff --check`, `go vet ./...` and a scratch-output `go build ./cmd/pyry`; no Go package is touched, so there is no scoped race suite. The verifier owns the full-module gate; live-agent/native acceptance belongs to #3133 and the client tickets.

## Open questions

None: the normative contract must settle all fields, paths, limits, isolation and rollback decisions before this stage ends.

## Documentation handoff

Pending for the documentation stage:

- `docs/hosted-apps.md` § Manifest and identity, Runtime and layout, Client resource bridge, Create, publish and update: fold in the approved contract, preserving exact fields, limits, runtime choice, isolation boundary and failure/data guarantees; state that the contract is defined ahead of hosting implementation.
- `docs/protocol-mobile.md` § Hosted apps (v2): link the contract, list its capability/message vocabulary, and state that app frames are host-scoped, encrypted and independent of conversation routing.
- Consider a decision record for the runtime and isolation boundary described in Context.

## Normative contract

The following sections are the v1 design deliverable. Hosting is not implemented by this ticket.
