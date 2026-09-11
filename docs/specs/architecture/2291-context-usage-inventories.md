# #2291 — carry context-usage inventories

## Files read

- `internal/streamsup/parser.go` → `contextUsageResponseLine`, `emitContextUsage`, `consumeLine`, `logControlResponse` — owns the child-stdout decode boundary, bounded event construction, and the content-free control-response log path.
- `internal/streamsup/context_usage_bound.go` → `boundContextUsageEntries`, `maxContextUsageEntries`, `maxContextUsageStringBytes` — supplies the shared stable ranking, 32-entry cap, 256-byte string rejection, ownership, and dropped-count contract.
- `internal/streamsup/context_usage_event_test.go` → `capturedContextUsagePayload`, `capturedContextUsageResponse`, `TestParser_ContextUsageCaptureReplay`, `TestParser_ContextUsageCategoriesAreBounded`, `TestParser_ContextUsageLogsOnlyExistingContentFreeRecord` — provides the existing capture replay, bounds proof, and log-leak regression surface this change extends.
- `internal/turnevent/event.go` → `ContextUsage`, `ContextUsageCategory` — defines the neutral event payload consumed downstream.
- `internal/e2e/realclaude/testdata/context_usage_v2.1.259.json` → `arms[].control_responses[].response.response` — is the byte-level authority for 34 MCP tool entries and the observed empty memory-file inventory.
- `docs/specs/architecture/2357-context-usage-event.md` → `Design`, `Error handling`, `Security review` — establishes the solicited pending-ID boundary and its deliberate no-log decode policy.
- `docs/knowledge/features/streamsup-package.md` → `Runner.RequestContextUsage` — records the current request ownership, bounding, and response-log invariants.
- `docs/knowledge/features/development-verification.md` → `Establish the change surface`, `Captured payloads and field mappings` — requires consumer searches plus independent decoding of committed captures and clearly labelled synthetic evidence where live bytes do not exist.

## Context

`ContextUsage` currently exposes Claude's scalar totals and combined category breakdown, but discards the two item-level inventories already present in the successful response. The committed Claude 2.1.259 capture proves the MCP-tool wire shape and cardinality; it records memory files as an empty array. This slice preserves both inventories without making individual system-prompt sections a prerequisite and uses synthetic data only for the otherwise-unrecorded nonempty memory-file mapping.

This is one deliverable and remains inside the one-ticket boundary: two production files, about 450–600 total written lines including this plan and tests, two new exported element types, no changed consumer call sites, four acceptance criteria, and no new state-machine reject branch. The event extension is additive; existing consumers may ignore the new fields.

## Design

Extend `turnevent.ContextUsage` with independently bounded MCP-tool and memory-file slices and one dropped count for each. `ContextUsageMCPTool` carries `Name`, `ServerName`, and `Tokens`; `ContextUsageMemoryFile` carries `Path`, `Type`, and `Tokens`. The producer contract documents that every string has passed the shared byte bound and each slice is descending by token count.

Extend `contextUsageResponseLine` with only the captured or ticket-specified JSON fields:

- `mcpTools[]`: `name`, `serverName`, `tokens`;
- `memoryFiles[]`: `path`, `type`, `tokens`.

In `emitContextUsage`, pass each decoded inventory separately through `boundContextUsageEntries`. MCP tools designate both name and server name as bounded fields; memory files designate both path and type. Each invocation therefore sorts before its own 32-entry cut and computes an independent count covering rejected strings plus valid entries omitted by the cap. Copy the retained entries into the neutral `turnevent` element types and emit them alongside the unchanged scalar and category fields.

An absent JSON key and an explicit empty array both decode to a nil/empty slice. The shared helper returns no retained entries and zero dropped values, so neither condition gates event emission. No individual system-prompt section is added to the decode target or checked by `emitContextUsage`.

## Concurrency model

No goroutine, lock, registry, or shutdown behavior changes. `emitContextUsage` continues to run synchronously on the parser writer after the pending request has been atomically retired and its successful write observed. Inventory sorting and copying operate only on decode-local slices before the event is handed to the existing sink.

## Error handling

The whole response remains one typed `encoding/json` decode: a wrong inventory field shape makes the already-retired reply non-emitting. Oversized inventory strings reject only their entry through `boundContextUsageEntries`; count overflow omits the lightest valid entries after stable descending ranking. Existing scalar handling remains unchanged.

The path must remain completely silent. It does not return or log decoder errors, raw response bytes, tool names, server names, or memory paths. The existing `control_response` arm may still produce its one content-free Debug record through `emitModelList`; its fixed message and numeric aggregate attributes contain none of the inventory values.

## Testing strategy

- Extend `TestParser_ContextUsageCaptureReplay` to decode `mcpTools` independently from the committed capture, rank it, compare the emitted top 32 fields and token counts, and assert the capture's two omitted MCP entries plus empty memory inventory are reported independently.
- Add a clearly named synthetic nonempty memory-file scenario that maps path, type, and tokens while omitting MCP tools and any individual system-prompt sections. Assert one event, descending memory order, and zero dropped values; do not describe the fixture as captured evidence.
- Extend the bounded-inventory test with more than 32 valid entries and entries whose first or second string field exceeds 256 bytes for both inventories. Assert independent order, caps, rejection-plus-omission counts, and absence of rejected sentinels.
- Extend `TestParser_ContextUsageLogsOnlyExistingContentFreeRecord` with tool-name, server-name, memory-path, and memory-type sentinels in valid and malformed responses, then inspect every log message and attribute for all sentinels, raw response vocabulary, and decode-error content.
- Run the required touched-scope gate: `go test -race ./internal/streamsup/...`, `go test -race ./internal/turnevent/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The ticket fixes both wire field names and the evidence boundary: MCP tools come from the committed capture, while nonempty memory files are synthetic and explicitly labelled.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/streamsup-package.md` in the `Runner.RequestContextUsage` section and `docs/knowledge/features/turnevent-package-outbound-event-variants.md` in the `ContextUsage` event description to document the two bounded inventories and their independent dropped counts.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — untrusted child stdout crosses one explicit typed boundary in `emitContextUsage`; the locally minted pending-ID check is retired before payload decoding, and `boundContextUsageEntries` bounds every inventory string before event construction.
- [Tokens, secrets, credentials] No findings — this ticket handles token counts, not authentication tokens or credentials, and introduces no secret generation, persistence, or lifecycle.
- [File operations] No findings — memory-file paths are descriptive strings from child stdout; this design never opens, resolves, joins, or writes them.
- [Subprocess execution] No findings — inventory strings never become commands, arguments, or environment variables; subprocess lifecycle is unchanged.
- [Cryptographic primitives] No findings — no randomness, keys, hashes, nonces, or comparisons are introduced.
- [Network & I/O] No findings — the existing bounded `Parser.Write` line reader remains the input-size boundary, and the new per-inventory 32-entry and 256-byte field limits bound retained event state.
- [Error messages, logs, telemetry] No findings — `emitContextUsage` discards decode errors and raw bytes without logging; the only sibling control-response record is fixed and content-free, and sentinel tests cover both fields of both inventories plus malformed input.
- [Concurrency] No findings — decode, sorting, copying, and emission remain synchronous after the existing pending-request handoff; no lock or goroutine is added.
- [Threat model alignment] No findings — this internal child-stdout mapping does not change relay, CLI authentication, filesystem access, or protocol trust decisions.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-11
