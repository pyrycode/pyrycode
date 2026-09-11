# Fakeclaude context-usage control response (#2289)

## Files read

- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSON`, `decodeControlRequest`, `controlRequestID`, `writeInitializeAck`, and `writeSetPermissionModeAck` — the existing unconditional control-request dispatch and captured-shape success writers this answer extends.
- `internal/e2e/internal/fakeclaude/initialize_control_test.go` → `answerInitialize` and `TestRunStreamJSON_InitializeControlAnswer` — the hermetic pattern for proving one answer in both stream modes, exact response nesting, request-ID correlation, and silence for an unknown subtype.
- `internal/e2e/internal/fakeclaude/set_permission_mode_control_test.go` → `answerSetPermissionMode` and `TestRunStreamJSON_SetPermissionModeAnswer` — the richer canned-response analogue and its emitted-byte assertions.
- `internal/e2e/internal/fakeclaude/stream_detect_test.go` → `TestRunStreamJSON_NonUserLinesIgnored` and `TestRunStreamJSON_InterruptAckRider` — existing guards for malformed input, default-mode silence, and the interrupt response bytes.
- `internal/e2e/realclaude/testdata/context_usage_v2.1.259.json` → the `summary` and `full` arms' `control_responses` — the transport source of truth for success-envelope nesting and context-usage field spelling.
- `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md` → “Initialize control request answer” and “Set-permission-mode control request answer” — establishes that daemon-originated startup controls are answered unconditionally beside the interrupt rider and written through `writeJSONLine`.
- `docs/knowledge/features/e2e-realclaude-context-usage-capture-test-go.md` → captured `get_context_usage` findings — establishes that `summary` and `full` share the same response hierarchy and payload contract.
- `docs/knowledge/features/development-verification.md` → protocol and capture verification guidance — requires tests to compare consumer-visible JSON shape and keep committed captures read-only.
- `CODING-STYLE.md` → testing and citation conventions — keeps the implementation table-driven, stdlib-only, race-safe, and cited by symbol.

## Context

The daemon will request context usage during ordinary fake-daemon runs. Fakeclaude currently ignores that subtype, so downstream context-usage slices cannot be exercised by the hermetic gate. The fake needs one deterministic response that preserves Claude's captured envelope and field vocabulary while deliberately stressing downstream list-count and entry-byte limits.

This is one deliverable. It modifies one production source file, adds one colocated test file, creates no exported type, changes no signature or consumer call site, has four acceptance criteria, and adds one recognized dispatch arm without a new error branch. Estimated written work is about 620–720 lines including this plan and tests, within the ticket's stated ~700-line estimate and the one-ticket boundary.

## Design

Add `get_context_usage` to the named fakeclaude subtype constants and match it in `runStreamJSON` with `controlRequestID`, beside the existing unconditional `initialize`, `set_permission_mode`, and `set_model` arms. Both request detail values intentionally select the same writer; fakeclaude recognizes the subtype, not a detail allow-list, matching the captured equality and avoiding a second policy boundary in the test fake.

`writeContextUsageAck(w, requestID)` writes one newline-terminated JSON value through `writeJSONLine`. Its shape is:

```text
control_response
└── response: {subtype, request_id, response}
    └── response: {model, totals, percentage, categories, mcpTools, memoryFiles}
```

The payload uses small local structs for repeated entries and a fresh map/slice composition per response. It contains the captured scalar spellings `model`, `totalTokens`, `maxTokens`, `rawMaxTokens`, `autocompactSource`, and `percentage`; category entries preserve `name`, `tokens`, `color`, and optional `isDeferred`; MCP entries preserve `name`, `serverName`, `tokens`, and `isLoaded`; memory-file entries use `path` and `tokens` in the same inner payload.

The canned data is fictional and deterministic. MCP tools contain 33 entries, including one name longer than 256 bytes. Memory files use only a synthetic `/fixture/...` root. Token counts intentionally rise and fall rather than arriving in descending order, so a downstream implementation must sort before applying its 32-entry cap.

No existing writer changes. Therefore existing recognized subtypes retain their emitted bytes by construction, while malformed and unknown lines continue falling through without output.

## Concurrency model

No goroutine or shared mutable state is added. `runStreamJSON` remains a single sequential read/dispatch/write loop. The canned response is built per call, so parallel tests and separate fakeclaude processes cannot share mutable slices or maps.

## Error handling

`writeContextUsageAck` returns the first JSON marshal or writer error from `writeJSONLine`; `runStreamJSON` stops on that error, matching every existing answer arm. Malformed JSON, non-control envelopes, and unknown subtypes do not call the writer and emit no bytes.

## Testing strategy

- Table-drive `summary` and `full` across default and interrupt modes. For every row, assert exactly one physical line, captured success-envelope nesting, the exact echoed request ID, and identical nested payloads.
- Decode the payload independently and assert the captured scalar fields and entry key sets, all three required lists, more than 32 MCP entries, a name or path exceeding 256 bytes, and a token sequence that is not already descending.
- Walk every canned memory path, require the synthetic root, and reject any path rooted under `os.UserHomeDir()`.
- Feed malformed JSON and an unknown control subtype and assert zero output. Existing unmodified fakeclaude tests remain the byte-level regression guards for every recognized pre-existing arm.
- Run RED before production edits, then `go test -race ./internal/e2e/internal/fakeclaude/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

None. The capture settles envelope depth and spelling; the ticket explicitly permits fictional stress values and requires both detail arms to answer.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md` with a “Context-usage control request answer” section documenting the unconditional `get_context_usage` dispatch, captured nested success shape, and deliberate 32-entry/256-byte stress fixture. No shared knowledge document is edited by the builder.

## Revisions

None.
