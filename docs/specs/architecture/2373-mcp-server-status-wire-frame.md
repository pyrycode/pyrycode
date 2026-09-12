# Ticket #2373: MCP server-status wire frame

## Files read

- `internal/turnevent/event.go` → `MCPStatus`, `MCPServerStatus` — the bounded neutral event and its exact retained fields are the source contract for the new wire DTO.
- `internal/streamsup/parser.go` → `mcpStatusResponseLine`, `mcpStatusServerLine` — confirms that config, tools, request metadata, the raw response and `serverInfo.name` are discarded before the event exists.
- `internal/protocol/interactive.go` → `SessionFactsPayload`, `ModelListPayload`, `ModelListPayload.MarshalJSON` — establishes outbound interactive DTO placement, provenance comments and nil-list normalization without mutating callers.
- `internal/protocol/interactive_test.go` → `roundTripEnvelope`, `TestModelListPayload_RoundTrip`, `TestModelListPayload_ZeroValue_RoundTrip`, `TestSessionFactsType_IsNotClaudesVocabulary` — supplies the decoded-payload re-encode, zero-key and exclusion-test patterns.
- `internal/protocol/testdata/model_list.json` → populated model-list envelope — provides the nearest complete outbound-list fixture shape, including a non-zero dropped count.
- `internal/protocol/codes.go` → `TypeModelList`, `TypeSessionFacts` — locates the v2 outbound report constants and their direction comments.
- `internal/protocol/envelope.go` → `IsKnownAppType`, `inboundAppTypeSet` — confirms that v1 accepts only its explicit inbound map and rejects a new v2 type as `ErrUnknownType`.
- `internal/protocol/compat_test.go` → `TestIsKnownAppType`, `v2OnlyTypes`, `TestTypeConstants_V1V2Partition` — identifies all protocol-local classification registries that must include the new constant.
- `cmd/pyry/relay_guard_test.go` → `excludedTypes`, `TestEveryInboundV2TypeHasHandler` — provides the independent AST-backed direction classification for an outbound-only frame.
- `docs/knowledge/features/protocol-package.md` and `docs/knowledge/features/protocol-package-drift-detectors.md` — record the package's pure-data boundary and the independent type-classification gates.
- `docs/knowledge/features/protocol-package-model-list-payload.md` — records the nearest analogue's list normalization, dropped-count and fixture coverage lessons.
- `docs/knowledge/features/development-verification.md` → `Protocol boundaries` — requires field assertions, exact key sets and re-marshalling the decoded payload rather than passing raw JSON through.

## Context

`turnevent.MCPStatus` now holds a bounded, projection-only view of Claude's MCP server report, but clients do not yet have one declared wire contract for it. This ticket reserves the single outbound v2 `mcp_status` frame shared by later live and on-demand producers. It declares only the payload, discriminator and direction classifications; request handling, event mapping and publication remain in #2374, #2375 and #2276.

The slice remains within one ticket: two production files, three exported declarations, no consumer migrations, three acceptance criteria and no state-machine reject branches. Estimated written work is about 430 lines including tests, fixture and this plan, below the 800-line boundary. The stale remote `feature/449` branch also touches `internal/protocol/codes.go`, but issue #449 closed on 2026-05-17 and has no open PR, so there is no in-flight overlap.

## Design

Add `TypeMCPStatus = "mcp_status"` beside the outbound interactive report constants. Classify it in the protocol test's v2-only partition and in the relay guard's outbound `push` set. Add an explicit `TestIsKnownAppType` row expecting `ErrUnknownType`; no `TypeRequestMCPStatus` or other inbound verb is introduced.

Declare these contracts in `internal/protocol`:

```go
type MCPStatusPayload struct {
    ConversationID string
    Servers        []MCPServerStatus
    DroppedServers int
}

type MCPServerStatus struct {
    Name    string
    Status  string
    Error   string
    Scope   string
    Version string
}
```

All fields use explicit snake-case JSON tags without `omitempty`, making every scalar key present at its zero value. `MCPStatusPayload.MarshalJSON` uses a value receiver and a local alias to normalize nil `Servers` to an empty slice without mutating the caller. There is no server-entry marshaller because ordinary string encoding already preserves the required empty keys.

The DTO is intentionally narrower than Claude's response: it has no config, tools, `serverInfo.name`, request id or raw response. `DroppedServers` is a verbatim future mapping of `turnevent.MCPStatus.DroppedServers`, not a value reconstructed from `len(Servers)`. All five entry strings remain untrusted Claude-authored text; this layer adds neither validation nor sanitization, and the payload is a report rather than an actuator.

Data flow after the deferred producer tickets land:

```text
bounded turnevent.MCPStatus
        -> later mapper copies retained fields and dropped count
        -> MCPStatusPayload
        -> Envelope{Type: TypeMCPStatus}
        -> client renders strings as inert text
```

## Concurrency model

This is pure data and deterministic JSON encoding. It starts no goroutines, holds no mutable package state and takes no locks. The value-receiver normalizer operates on a struct copy and does not modify the caller's slice.

## Error handling

The custom marshaller returns `json.Marshal` errors unchanged, matching sibling DTO marshallers; the payload contains only JSON-native strings, integers and slices. Inbound v1 classification returns the existing `ErrUnknownType`. No new validation error or request refusal is introduced because this ticket declares no inbound path.

## Testing strategy

- Commit one populated `mcp_status.json` envelope fixture with distinct values for every server string and a non-zero `dropped_servers`. Decode the envelope and payload, assert every field, pin the exact top-level and per-server key sets, and re-marshal the decoded payload through `roundTripEnvelope` to preserve JSON keys, order and value types.
- Add separate constructed zero-value coverage. Marshal payload value and pointer forms to prove nil `Servers` becomes `[]`, `DroppedServers` is numeric `0`, every payload key remains present, and the caller is not mutated. Marshal a zero server entry and inspect every raw JSON value to prove all five string keys remain present as `""`.
- Extend `TestIsKnownAppType` to prove `mcp_status` is rejected by v1, add it to `v2OnlyTypes` and the complete constant list, and classify it as outbound `push` in `excludedTypes`. The absence of an inbound constant and handler entry is deliberate.
- Run the new protocol tests red before adding production declarations, then run `go test -race ./internal/protocol/...`, `go vet ./...` and `go build ./cmd/pyry` after implementation.

## Open questions

None. The ticket fixes the frame name, direction, field set, normalization and deferred producer boundaries.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md` with an `mcp_status` section documenting `conversation_id`, `servers`, every per-server field and `dropped_servers`; explain the dropped-count meaning, exclusion of config/tool fields, and the inert client-render boundary. Record both in the protocol table and section text that this ticket declares the frame before its interactive producer lands.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `MCPStatusPayload` and `MCPServerStatus` retain the subprocess provenance instead of promoting any string to trusted data; later mappers must copy the already-bounded `turnevent.MCPStatus` projection only.
- [Tokens, secrets, credentials] No findings — the DTO structurally excludes config, tools, `serverInfo.name`, request ids and the raw response, while exact key-set tests make any newly exposed field visible.
- [File operations] No findings — production code performs no file operations; the committed JSON fixture is read only by tests through the existing fixture helper.
- [Subprocess execution] No findings — this slice neither starts a process nor passes a field to one. `MCPServerStatus` is explicitly report-only.
- [Cryptographic primitives] No findings — the change creates, stores and compares no secrets or cryptographic material.
- [Network and I/O] No findings — the package adds no reader, writer or socket path. The later mapping and publication work remains in #2374, #2375 and #2276; this DTO neither weakens the producer's bounds nor introduces a second cap.
- [Errors, logs, telemetry] No findings — no logs or telemetry are added. The Claude-authored `Error` string remains payload data, never an internal error message, and its client render boundary is explicit.
- [Concurrency] No findings — the value-receiver normalizer mutates only its copy, and no goroutine, lock or shared mutable state is introduced.
- [Threat model alignment] No findings — the frame is outbound-only, declares no authority or request capability, and requires clients to render all server strings as inert text. Publication and on-demand authorization concerns remain with the deferred producer tickets.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-12

## Revisions

None.
