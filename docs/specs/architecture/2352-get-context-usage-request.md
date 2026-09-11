# #2352 — write a `get_context_usage` control request

## Files read

- `internal/streamsup/envelope.go` → `controlRequest`, `controlRequestInner`, `WritePermissionMode`, `WriteInitialize` — defines the structured control-line shape, validation-before-live-child precedent, and value-free wrapped write errors.
- `internal/streamsup/runner.go` → `(*Runner).RequestInitialize`, `(*Runner).nextControlID`, `(*Runner).Stdin` — supplies the concrete runner method pattern and the shared atomic request-ID sequence.
- `internal/streamsup/envelope_test.go` → `TestMarshalPermissionModeEnvelope_AllowedModes`, `TestWritePermissionMode_RefusalOutranksNilWriter`, `TestWriteInitialize_WriteError` — proves byte order, refusal precedence, and write-error classification at the free-function boundary.
- `internal/streamsup/interface_test.go` → `TestRunner_RequestInitialize_LiveChildDelivers`, `findEchoedLines` — provides the live helper-child proof and shared-sequence discriminator at the runner boundary.
- `internal/streamsup/context_usage_capture_test.go` → `TestContextUsageCaptureFixture` — confirms the committed capture is already read hermetically and identifies its two request arms.
- `internal/e2e/realclaude/testdata/context_usage_v2.1.259.json` → `arms[].control_request_sent` — byte-level source of truth for the accepted `summary` and `full` requests.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” — establishes held-open stdin, structured line encoding, and helper-child test conventions.
- `docs/knowledge/features/development-verification.md` → “Protocol boundaries” and “Captures and live evidence” — requires fixture-backed field/order assertions and a committed-fixture reader that does not skip.
- `docs/specs/architecture/2287-get-context-usage-capture.md` → `get_context_usage` request shape and conclusions — records the measurement behind the accepted detail vocabulary.
- `docs/specs/architecture/1839-initialize-ask-per-child.md` → `RequestInitialize` design — supplies the closest writer-only control-request analogue without importing its later trigger behavior.

## Context

Claude 2.1.259 accepts `get_context_usage` requests with either `detail:"summary"` or `detail:"full"`; #2287 committed both exact request objects. Callers currently would have to construct protocol bytes and request IDs themselves. This ticket adds only a validated write primitive and a concrete `Runner` operation. Response decoding and automatic turn-boundary triggering remain with later slices.

The change stays within one ticket: two production files, two test files, no new exported type or interface, no consumer migration, three acceptance criteria, and two refusal branches. Estimated written work including this plan is about 475 lines. No ADR is warranted because the change extends the existing control-request contract without changing package boundaries or lifecycle ownership.

## Design

Extend `controlRequestInner` with `Detail string` after `Subtype`, tagged `json:"detail,omitempty"`. Declaration order makes `subtype` precede `detail`; `omitempty` keeps every existing control subtype byte-identical.

Add an `ErrUnsupportedContextUsageDetail` sentinel and a closed predicate admitting exactly `summary` and `full`. `marshalContextUsageEnvelope(requestID, detail)` remains a pure structured encoder. `WriteContextUsage(w, requestID, detail)` validates detail first, then rejects a nil writer, then marshals and performs exactly one write. Its error contexts name only the operation, never the request ID or detail.

Add `(*Runner).RequestContextUsage(detail string) error`. It obtains the current stdin and mints an ID through `nextControlID`, then delegates to `WriteContextUsage`. Validation remains inside the writer so both direct and runner callers receive the same permanent refusal, including while no child is live. The method remains concrete on `*streamsup.Runner`: this slice has no consumer in `internal/sessions`, and widening `sessions.Runner` would create an unused cross-package contract and test-double cascade.

Data flow:

```text
caller detail
  -> Runner.RequestContextUsage
  -> Runner.Stdin + Runner.nextControlID
  -> WriteContextUsage allow-list
  -> marshal controlRequest
  -> one newline-terminated child-stdin write
```

## Concurrency model

No goroutine or lock is added. `nextControlID` retains its atomic sequence shared by every control subtype. `Stdin` snapshots the live writer under the runner mutex and releases it before the potentially blocking write, matching the existing control methods. A concurrent teardown can therefore surface the original write error; it is not reclassified as a refusal.

## Error handling

- Unsupported detail returns bare `ErrUnsupportedContextUsageDetail` before checking `w`; it is permanent, value-free, and distinguishable with `errors.Is`.
- A supported detail with `w == nil` returns bare `ErrNoLiveChild` and writes nothing.
- Marshal errors are wrapped with a fixed `streamsup: marshal context usage` prefix.
- Child-stdin errors are wrapped with a fixed `streamsup: write context usage` prefix, preserving their `errors.Is` cause without including the detail or minted ID.

## Testing strategy

- Read the committed #2287 fixture, compact each `control_request_sent`, replace only its capture ID, and compare both marshalled `summary` and `full` lines byte for byte plus the trailing newline. This pins the source evidence, one-line encoding, allowed values, and `subtype`-before-`detail` order.
- At the writer boundary, prove an unknown value outranks a nil writer and leaves a supplied buffer empty; prove supported values with no writer return only `ErrNoLiveChild`; prove a sentinel write failure remains discoverable with `errors.Is`, is neither refusal sentinel, writes once, and leaks neither a distinctive ID nor detail in its error text.
- At the runner boundary, start the existing echo helper, request both details, then send an interrupt as a FIFO barrier. Assert exactly one line per request, captured shape for each detail, non-empty distinct IDs, and IDs distinct from the interrupt to prove all operations share `nextControlID`.
- Run the required touched-scope gate: `go test -race ./internal/streamsup/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The capture fixes the detail vocabulary and wire shape; the existing initialize and permission-mode writers fix method placement, error ordering, and concurrency semantics.

## Documentation handoff

No documentation-only acceptance criterion or explicit Documentation handoff was included in #2352. The later documentation stage may update `docs/knowledge/features/streamsup-package.md` to list `RequestContextUsage` alongside the other control-request operations.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `WriteContextUsage` is the single boundary for caller-supplied detail and admits only the two captured values before any child-state lookup or write.
- [Tokens, secrets, credentials] No findings — `nextControlID` produces a non-secret monotonic correlation ID; neither it nor the detail appears in returned error text, logs, or persistent storage.
- [File operations] No findings — production performs no file operation; the test reads one fixed committed fixture path and creates nothing.
- [Subprocess / external command execution] No findings — no command, argument, or environment changes; the operation writes structured JSON to the already-owned child stdin.
- [Cryptographic primitives] No findings — the correlation ID is not an authentication token and needs uniqueness within one runner, not cryptographic unpredictability.
- [Network & I/O] No findings — `json.Marshal` plus one appended newline bounds the request to one small physical line; this ticket adds no socket, read loop, listener, or response buffering.
- [Error messages, logs, telemetry] No findings — permanent, no-child, marshal, and write errors use fixed text; tests use distinctive values to prove request ID and detail do not leak.
- [Concurrency] No findings — the design preserves `Stdin`'s lock-free write phase and `nextControlID`'s atomic sequence; teardown races return the underlying write cause without adding goroutines or shared state.
- [Threat model alignment] OUT OF SCOPE — caller authorization and relay exposure belong to the consumer in #2293; automatic triggering belongs to #2353. This ticket exposes only the validated daemon-internal primitive.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

None.
