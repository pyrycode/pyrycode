# #2274 — Decode Claude's MCP status reply into one capped turn event

## Files read

- `internal/streamsup/parser.go` → `streamLine`, `Parser.consumeLine`, `controlResponseLine`, `Parser.emitModelList`, `Parser.logControlResponse`, `truncateField` — establishes the top-level-only control-response routing, the shape-based success gate, the existing sole log record, and the valid-UTF-8 byte-cut helper.
- `internal/streamsup/mcp_status_capture_test.go` → `mcpStatusCapture`, `mcpStatusServersFrom`, `TestRealClaudeMCPStatusCaptureServerKeysArePinned` — identifies the final captured `mcp_status` response without trusting the probe's derived labels and proves the committed fixture's provenance and server-key union.
- `internal/e2e/realclaude/testdata/mcp_status_v2.1.259.json` → final `mcp_status`, `mcp_reconnect`, and `mcp_toggle` response frames — the measured Claude 2.1.259 shapes and values the parser tests replay.
- `internal/turnevent/event.go` → `Event`, `ModelList`, `ContextUsage`, and their element types — the sealed internal event contract and the neighbouring list-event documentation conventions.
- `cmd/pyry/stream_turn_busy.go` → `turnMarkFor` — its whitelist makes a new status event lifecycle-neutral without a consumer edit.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.Handle`, `eventKind` — confirms an unpublished event is dropped through a content-free `unknown` kind rather than serialised or logged by value.
- `internal/turnbridge/outbound.go` → `MapEvent` — confirms the new internal event has no wire mapping in this slice.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” and the `emitModelList` material — records the single-writer parser model, response-decode isolation, and no-payload-log rule.
- `docs/knowledge/features/turnevent-package-outbound-event-variants.md` → event inventory and `ContextUsage` / `ModelList` entries — records the internal event boundary, producer-owned bounds, and the requirement to inspect defaulting consumers when adding a variant.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface”, “Prove that tests distinguish the change”, and “Captures and live evidence” — requires direct checks of event consumers, independent predicate rows, and replay of committed evidence.

## Context

Claude 2.1.259 now has committed evidence for the reply to `mcp_status`: a top-level
`control_response`, a nested success subtype, and an `mcpServers` array in the nested
payload. The daemon currently consumes and content-free logs that line but carries no
typed representation of the server list. This change adds that internal representation
at the existing subprocess-stdout boundary. It does not correlate the status content to
a request, alter acknowledgement correlation, retain the event, or publish a wire frame.
No ADR is warranted: this is another variant of the established shape-recognition and
bounded-event pattern in `Parser.emitModelList`.

## Design

### Internal event contract

`internal/turnevent` gains two exported value types:

- `MCPServerStatus` carries `Name`, `Status`, `Error`, `Scope`, and `Version`.
- `MCPStatus` implements `Event`, carries `Servers []MCPServerStatus`, and reports
  count-cap omissions in `DroppedServers`.

All five strings remain Claude-authored and unsanitized. `Error` is display prose and is
cut to at most 256 bytes with valid UTF-8. The other fields are copied as the status
reply's tokens or names. The event may contain a present, non-nil empty `Servers` slice:
that is the positive report corresponding to a present `mcpServers:[]`, unlike a missing
or unusable payload, which emits no event.

### Parser decode

`internal/streamsup/parser.go` gains a dedicated `mcpStatusResponseLine` target. It
declares only the nested subtype and `mcpServers` as `json.RawMessage`; the entry target
declares only the five carried fields and `serverInfo.version`. Consequently request ID,
config, tools, `serverInfo.name`, and future sibling keys are structurally unavailable to
the constructed event.

`decodeMCPStatus(line) (turnevent.MCPStatus, bool)` is a pure function with no logger.
It:

1. decodes from the complete top-level line bytes and requires the nested subtype to be
   exactly `success`;
2. requires the raw `mcpServers` key to decode as a non-nil array, preserving the
   distinction between present `[]` and missing/null/non-array input;
3. computes `DroppedServers` from the decoded length, retains the first 16 entries, and
   allocates the result from only that capped prefix; and
4. maps entries in order while passing `Error` through `truncateField` at 256 bytes.

The `control_response` arm calls the decoder after `Parser.emitModelList`. That ordering
leaves the existing content-free `Parser.logControlResponse` record ahead of every event
and preserves its exactly-once ownership. A successful status decode emits exactly one
`MCPStatus`; every false result emits none. The independent calls to
`Parser.emitContextUsage`, `Parser.noteControlAck`, and `Parser.emitModelList` retain
their current targets and semantics.

```text
Parser.consumeLine (top-level type == control_response)
  -> emitContextUsage / noteControlAck / emitModelList (unchanged)
  -> decodeMCPStatus(top-level line)
       -> false: no status event
       -> true:  emit one turnevent.MCPStatus
```

No downstream switch is widened. `turnMarkFor` keeps the event lifecycle-neutral through
its default, `MapEvent` keeps it off the wire, and `eventKind` returns its content-free
default. Publication and named downstream handling belong to later slices.

## Concurrency model

No goroutine, lock, channel, or parser state is added. `decodeMCPStatus` runs synchronously
inside `Parser.consumeLine` on the parser's existing single writer. The sink continues to
receive events serially and in source-line order.

## Error handling

| Input | Result |
| --- | --- |
| Whole-line or status-target decode failure | No `MCPStatus`; no new log |
| Nested subtype missing or not `success` | No `MCPStatus`; no new log |
| `mcpServers` missing, null, or not an array | No `MCPStatus`; no new log |
| `mcpServers:[]` | One `MCPStatus` with an empty non-nil server slice |
| More than 16 entries | First 16 in order; exact omitted tail count in `DroppedServers` |
| Retained error over 256 bytes | Byte-cut and scrubbed to valid UTF-8 by `truncateField` |
| Optional field absent | Corresponding event string is empty |

Failures are intentionally silent in the new decoder. `Parser.emitModelList` remains the
only owner of the line's Debug record and emits only daemon-authored keywords and counts;
no decoder error is wrapped, returned, or logged.

## Testing strategy

Add `internal/streamsup/mcp_status_event_test.go` and drive every assertion through a
real `Parser`, not the pure decoder alone:

- Read the committed capture, identify the final `mcp_status` request and its matching
  response frame, replay that payload, and assert exactly one event with the three
  servers in captured order and only the five selected values.
- Table the positive empty array and each negative predicate independently: non-success,
  absent subtype, missing payload, null payload, non-array payload, and a control-shaped
  JSON string nested in an assistant text block. Count only `MCPStatus` on the nested case
  so the block's ordinary event does not masquerade as parser silence.
- Build an over-cap list with distinct ordered names, assert 16 retained, the exact tail
  drop count, and result capacity no greater than the cap. Cover exact-boundary,
  over-boundary, and mid-rune `Error` values and require valid UTF-8.
- Prove the model-list reply, permission-mode success, permission-mode rejection,
  interrupt acknowledgement, and captured reconnect and toggle acknowledgements each
  emit no `MCPStatus` from their own bytes.
- Feed success, rejection, and status-decode-failure lines carrying distinctive sentinels
  through a recording logger. Assert exactly the existing control-response record, its
  closed attribute set, and absence of every request/payload/error sentinel.

Verification is `go test -race ./internal/streamsup/...`,
`go test -race ./internal/turnevent/...`, `go vet ./...`, and
`go build ./cmd/pyry`.

## Open questions

None. The ticket fixes recognition, field selection, caps, and the absence of request
correlation and wire publication.

## Documentation handoff

Pending for the documentation stage:

- Add `MCPStatus` / `MCPServerStatus` to the event inventory in
  `docs/knowledge/features/turnevent-package-outbound-event-variants.md`, including the
  internal-only/no-wire state and producer-owned count/error bounds.
- Extend the control-response portion of “Turn I/O — envelope write + stdout parser” in
  `docs/knowledge/features/streamsup-package.md` with `decodeMCPStatus`, its top-level
  shape gate, selected fields, and the unchanged content-free log owner.
- Do not update `docs/protocol-mobile.md`: this slice deliberately publishes no wire
  frame.

## Revisions

None.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — subprocess stdout crosses into typed values at the
  single pure `decodeMCPStatus` boundary. Its decode targets structurally exclude config,
  tools, `serverInfo.name`, request ID, and raw bytes. The event docs keep every retained
  string classified as Claude-authored and unsanitized rather than promoting it to a
  daemon fact.
- [Tokens, secrets, credentials] No findings — no token is generated or stored. The
  captured reply's config can contain command paths, arguments, or environment values,
  but the decode target has no config or request-ID field, so neither can enter the event
  or a diagnostic.
- [File operations] No findings — production performs no file operation. The test reads
  the already committed fixture at `mcpStatusCapturePath`; it creates or updates no file
  and relies on the existing provenance test rather than accepting a caller path.
- [Subprocess / external execution] No findings — the change consumes bytes already read
  from Claude and adds no command, argument, environment, signal, or process-lifecycle
  behaviour.
- [Cryptographic primitives] Not applicable — no randomness, secret comparison,
  cryptographic material, or primitive is introduced.
- [Network & I/O] No findings — no socket or wire path is added. Retained cardinality is
  capped at 16 before result allocation, the free-form error is capped at 256 valid-UTF-8
  bytes, and config/tool arrays are omitted. The parser's existing input-buffer boundary
  remains the outer transient-input bound.
- [Error messages, logs, telemetry] No findings — `decodeMCPStatus` has no logger and
  returns only `(event, bool)`. `Parser.logControlResponse` remains the sole record for
  the line, and sentinel tests close its attributes against payload, request ID, raw
  response, and decoder-error leakage on success, rejection, and decode failure.
- [Concurrency] No findings — the pure decode and sink call remain on `Parser`'s
  established single-writer goroutine. No shared state, lock, check-then-mutate sequence,
  or goroutine is introduced.
- [Threat model alignment] No findings — this slice stops at the internal event boundary
  and deliberately adds no relay, client, command, or filesystem actuator. A future wire
  publication must separately review rendering and envelope bounds; nothing in this
  change grants authority based on a reported status value.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-12
