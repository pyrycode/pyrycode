# Ticket #2273: MCP control request writers

## Files read

- `internal/streamsup/envelope.go` → `controlRequestInner`, `WriteInterrupt`, `WriteModel`, `WritePermissionMode`, `WriteInitialize`, and `WriteContextUsage` — defines the shared ordered request shape and the established nil/marshal/write contract for a held-open child stdin.
- `internal/streamsup/envelope_test.go` → `TestMarshalInterruptEnvelope`, `TestWriteInitialize_WritesEnvelope`, and `TestWriteContextUsage_WriteError` — supplies the byte-exact schema, one-write/no-close, refusal, escaping, and wrapped-error proof patterns.
- `internal/e2e/realclaude/mcp_status_capture_test.go` → `mcapControlLine` and `TestMcapControlLineCarriesEachVerbsOwnFields` — records the Claude 2.1.259 bundled-schema spellings for `mcp_status`, `mcp_reconnect`, `mcp_toggle`, `serverName`, and `enabled`; this is schema provenance, not live compatibility evidence.
- `docs/knowledge/features/streamsup-package.md` → “Held-open stdin” and “Turn I/O” — confirms that writers receive an `io.Writer`, never own or close it, and return `ErrNoLiveChild` when `Runner.Stdin` is absent.
- `docs/knowledge/features/development-verification.md` → “Protocol boundaries” and “Captures and live evidence” — requires exact key-set/schema tests while keeping schema assertions distinct from capture-backed compatibility claims.
- `CODING-STYLE.md` → “Error Handling” and “Testing” — requires contextual wrapping, table-driven tests, and touched-package race verification.

## Context

The daemon needs write-side primitives for three MCP management requests before later tickets can add reply decoding and route authorized callers. The request spellings come from the schema bundled with Claude 2.1.259 and transcribed by the existing capture probe. Per the approved scope decision, this ticket can ship the write-only schema without a committed live capture; live compatibility evidence remains required by the reply and end-to-end control-path tickets.

No ADR is needed. This extends the established control-request envelope and writer pattern without changing package boundaries, ownership, or lifecycle.

## Design

Extend `controlRequestInner` with only the two MCP-specific fields:

- `ServerName string` encoded as `serverName` and omitted when empty, after `Subtype` and the existing fields.
- `Enabled *bool` encoded as `enabled` and omitted only when absent. Each toggle marshal call takes the address of its local boolean so both `true` and `false` are present on the wire; all other subtypes leave it nil.

Add one private marshaler and one exported writer for each request:

- `marshalMCPStatusEnvelope(requestID string)` / `WriteMCPStatus(w, requestID)` emit only `subtype: mcp_status` inside `request`.
- `marshalMCPReconnectEnvelope(requestID, serverName string)` / `WriteMCPReconnect(w, requestID, serverName)` add `serverName`.
- `marshalMCPToggleEnvelope(requestID, serverName string, enabled bool)` / `WriteMCPToggle(w, requestID, serverName, enabled)` add `serverName` and the explicit boolean.

Each marshaler constructs the typed `controlRequest`, uses `encoding/json`, and appends exactly one newline. `Subtype` remains the first declared inner field, preserving the required wire order. Each writer mirrors `WriteInitialize`: reject nil with `ErrNoLiveChild`, marshal, call `Write` once, wrap any failure with a fixed operation context, and return without reading, closing, or retaining the writer.

Server names are deliberately not validated here. They cross this layer as untrusted string data and remain JSON string data; structured marshaling escapes quotes, backslashes, newlines, and control bytes so they cannot create another physical input line. Server membership and authorization are caller-boundary responsibilities in later work.

Existing writers continue to use the same shared struct. `omitempty` on both new fields keeps their serialized bytes unchanged, including the field-free interrupt and initialize requests and the existing detail/mode/model requests.

## Concurrency model

The new functions create no goroutines and hold no state. A caller obtains the current child writer through the existing lifecycle seam; concurrent write coordination remains the caller/runner's existing responsibility. Each function performs one synchronous `Write` and returns.

## Error handling

- A nil writer returns the existing `ErrNoLiveChild` sentinel before marshaling or writing.
- JSON marshal failures are wrapped with a fixed request-kind context. They are defensive because all values are supported scalar types.
- Writer failures are wrapped with a fixed request-kind context while preserving the underlying error for `errors.Is`.
- Error strings do not include the request ID, server name, or toggle value.
- No error path closes the writer or retries a partial write, matching the existing control-request writers.

## Testing strategy

Add offline tests in `internal/streamsup/envelope_test.go` that:

- Compare each marshaler's bytes with its exact expected newline-terminated JSON, including toggle rows for both boolean values.
- Decode each request and assert its exact inner key set, proving status and reconnect do not inherit fields from other subtypes.
- Exercise a hostile server name containing quotes, backslashes, newlines, and control characters; assert one physical line and JSON round-trip equality.
- Run each exported writer against a counting, non-closable buffer and assert one write with the exact marshaled bytes; the `io.Writer` contract makes closing unavailable.
- Run each writer with nil and assert `ErrNoLiveChild`.
- Run each writer against a failing writer and assert the underlying error remains discoverable, the error has the correct fixed operation context, and caller data is absent.
- Keep all existing byte-exact control-request tests green to prove the shared struct extension did not alter interrupt, permission mode, initialize, model, or context-usage output.

Verification is limited to the required builder gate: `go test -race ./internal/streamsup/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The ticket fixes the wire spellings, writer ownership contract, validation boundary, and capture-evidence scope.

## Documentation handoff

No documentation change is required by the ticket. The later documentation stage may update the `internal/streamsup` package overview to list the three new write-side primitives after code review.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No finding — `WriteMCPReconnect` and `WriteMCPToggle` are the single boundary where an untrusted server-name string enters the child protocol. They preserve it as data through `encoding/json`; server membership and device authorization are explicitly deferred to the caller work in #2278.
- [Tokens, secrets, credentials] No finding — the writers do not create, store, rotate, or inspect credentials. Locally supplied request IDs are correlation values, are structured-encoded, and are excluded from returned error text.
- [File operations] Not applicable — the design performs no filesystem access.
- [Subprocess / external command execution] No finding — the functions write structured protocol data to an already-running child and do not create commands, arguments, environments, or signals. A hostile server name cannot become a shell argument at this layer.
- [Cryptographic primitives] Not applicable — the design performs no cryptographic operation or secret comparison.
- [Network & I/O] No finding — each request is bounded by caller-supplied scalar lengths and performs one synchronous write to the existing held-open pipe. Structured JSON escaping makes the appended terminator the only physical newline, preventing an injected second command. Broader input-size limits are not introduced by this sibling of existing writers and belong at the external caller boundary in #2278.
- [Error messages, logs, telemetry] No finding — fixed marshal/write contexts omit request IDs, server names, and enabled values; these functions add no logging or telemetry.
- [Concurrency] No finding — the design adds no goroutine, lock, shared state, retry, or check-then-mutate sequence. A mid-write failure is returned without retry or close, matching existing pipe ownership and avoiding duplicate commands.
- [Threat model alignment] OUT OF SCOPE — device authorization, server membership, reply correlation, and complete remote routing are deferred to #2274 and #2278. This ticket only makes the write-side encoding primitive injection-safe.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

None.
