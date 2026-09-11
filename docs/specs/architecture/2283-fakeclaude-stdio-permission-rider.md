# #2283 — fakeclaude stdio permission rider

## Files read

- `internal/e2e/internal/fakeclaude/main.go` → `main`, `runStreamJSON`, `inControlRequest`,
  `writeJSONLine`, `writeAssistantToolUse`, `writeVerdictResponse` — owns stream-json rider
  selection, the single-reader loop, correlated control responses, and the established
  allow/deny transcript vocabulary.
- `internal/e2e/internal/fakeclaude/approve_test.go` → `TestWriteVerdictResponse`,
  `TestRunStreamJSONApprove` — pins the existing permission verdict lines and blocking rider pattern.
- `internal/e2e/internal/fakeclaude/initialize_control_test.go` →
  `TestRunStreamJSON_InitializeAnswer` — supplies the independently constructed control-envelope
  test pattern.
- `internal/streamsup/parser.go` → `CanUseToolRequest`, `decodeCanUseTool` — authority for the
  current inner request field vocabulary and the daemon-side trust boundary.
- `internal/streamsup/envelope.go` → `permissionAllowResult`, `permissionDenyResult`,
  `marshalCanUseToolAllow`, `marshalCanUseToolDeny` — authority for nested response correlation,
  allow `updatedInput`, and deny behavior.
- `internal/streamsup/can_use_tool_test.go` → `TestParser_CanUseTool_AllFieldsAndRawShapes`,
  `TestMarshalCanUseToolAllow`, `TestMarshalCanUseToolDeny` — byte-exact fixtures for request fields,
  raw JSON preservation, optional presence, and both response paths.
- `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md` → stream-json rider conventions —
  default-off behavior and the rule that fake protocol shapes mirror the production codec.
- `docs/knowledge/features/development-verification.md` → “Protocol boundaries” — requires raw-key
  assertions where absence differs from an explicit zero value.

## Context

The fake-daemon suite can currently exercise the MCP approval path, but it cannot originate the
`can_use_tool` stdio exchange decoded and encoded by `streamsup`. This ticket adds that test-only
producer and answer consumer so #2284 can drive the whole path hermetically.

This is an extension of the existing fakeclaude rider family and does not warrant an ADR. The
required in-flight branch comparison found no feature branch touching `main.go` or the new test.

### Size boundary

- Deliverables: one default-off stdio-permission rider.
- Production source files: 1 (`internal/e2e/internal/fakeclaude/main.go`).
- Total written work: estimated 650–780 lines including this plan and a new package test.
- New exported types or interfaces: 0.
- Consumer call sites needing simultaneous updates: 0. `runStreamJSON` keeps its signature because
  it has more than 10 existing test callers; it loads the rider configuration internally.
- Acceptance criteria: 4.
- Distinct new reject branches: invalid/unrecognised configuration disables the rider; unmatched
  response ids are ignored.

## Design

Add `PYRY_FAKE_CLAUDE_STREAM_CAN_USE_TOOL`, read only in stream-json mode. A loader decodes its value
as a JSON object, rejects malformed input, non-objects, a caller-supplied `subtype`, and keys outside
the field vocabulary declared by `streamsup.CanUseToolRequest`. Rejection returns a disabled rider,
so unset and invalid values take the existing byte-identical response path.

The loader retains each configured value as `json.RawMessage` in a map. It does not decode optional
fields into Go zero values: marshalling the map therefore preserves object/array/scalar shape, keeps
an explicit `false`, and omits keys that were absent. The request writer adds only the fixed
`subtype:"can_use_tool"` and wraps the result in a `control_request` with a deterministic non-empty
per-turn request id.

`runStreamJSON` loads the configuration once before its read loop. On each user turn, when the rider
is enabled, it writes exactly one ask and records that turn's request id, message id, requested tool
name, tool-use id, and input. It then continues reading stdin without emitting an assistant or result
line. A minimal response decoder recognizes only a nested successful `control_response`, returning
its request id, behavior, and optional `updatedInput`. Other lines and responses for other ids are
consumed without resolving the ask. Since the loop is sequential, at most one ask is outstanding.

A matching allow emits one assistant `tool_use` line as the permitted-call oracle, using
`updatedInput` when present and otherwise the request's original input, then reuses the existing
allow verdict and successful result lines. A matching deny emits the existing distinct deny verdict
and successful result without a `tool_use`, so denied output cannot report the call as permitted.
Unknown behavior on a matching response remains unresolved rather than being treated as an allow.

```text
user turn -> control_request(can_use_tool, request id) -> wait
                                                       |
                         unrelated response -----------+-- ignore
                         matching allow ---------------+-- tool_use + allow + result
                         matching deny ----------------+-- deny + result
```

## Concurrency model

No goroutine, channel, mutex, or shutdown path is added. `runStreamJSON` remains one synchronous
reader and one synchronous writer. Waiting means the same loop keeps consuming stdin until the
outstanding request id is answered; EOF or a write error ends the process as today.

## Error handling

- Unset, malformed, non-object, unknown-key, or caller-supplied-subtype configuration disables the
  rider and preserves the existing stream output.
- Request or terminal-output marshal/write failure ends the loop, matching all existing fake writers.
- A response with another request id, a non-success envelope, or an unknown behavior is consumed and
  ignored; no terminal line is emitted and a later matching allow or deny may still resolve the ask.
- Missing requested input is represented as absent raw JSON. The permitted-tool writer emits JSON
  `null` only when it needs an input value and neither the request nor the allow supplied one.

## Testing strategy

- RED: a valid all-field configuration emits one byte-exact newline-terminated request whose raw
  object/array fields retain shape, whose explicit false booleans remain present, and whose omitted
  optional fields stay absent.
- RED: unset, malformed, non-object, unknown-key, and subtype-bearing configurations produce exactly
  the existing assistant/result transcript.
- RED: a user turn followed by a mismatched response produces only the ask; adding a later matching
  response produces terminal output, proving correlation and the wait.
- RED: matching allow uses `updatedInput` when supplied and requested input otherwise; matching deny
  has the distinct deny needle and no permitted `tool_use` line.
- Required gate: `go test -race ./internal/e2e/internal/fakeclaude/...`, `go vet ./...`, and
  `go build ./cmd/pyry`.

## Open questions

None. #2282 fixes the field and response vocabulary, and the ticket fixes presence semantics and both
terminal paths.

## Documentation handoff

Pending for the documentation stage:

- Update `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md`, in the stream-json rider
  section, with `PYRY_FAKE_CLAUDE_STREAM_CAN_USE_TOOL`, its presence-preserving JSON configuration,
  correlation wait, and allow/deny transcript behavior.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `loadStdioPermissionRider` is the single environment-to-typed-map
  boundary. It accepts only the exact `CanUseToolRequest` field vocabulary; all values remain
  untrusted JSON and are re-encoded by `encoding/json` before reaching stdout.
- [Tokens, secrets, credentials] No findings — the request id is a deterministic test correlation id,
  not a capability or secret. It is process-local and never logged.
- [File operations] No findings — the rider adds no file read, write, path join, or permission change.
- [Subprocess / external command execution] No findings — configured tool input is emitted as test
  protocol data only; fakeclaude does not execute it, pass it to a shell, or add it to child argv.
- [Cryptographic primitives] No findings — the rider adds no key, nonce, digest, or security-sensitive
  randomness.
- [Network & I/O] No findings — this test-only mode uses the existing stdio loop and OS-bounded
  environment value. Structured marshalling prevents raw JSON from injecting a second physical line;
  no listener or network input is added.
- [Error messages, logs, telemetry] No findings — invalid configuration disables silently and no
  request, input, path, description, or response value is logged or interpolated into an error.
- [Concurrency] No findings — the single loop owns the outstanding request and all output; no shared
  mutable state or goroutine is introduced.
- [Threat model alignment] No findings — this is hermetic test infrastructure. The production
  subprocess-output trust boundary remains in `decodeCanUseTool`; #2284 owns authenticated client
  surfacing and answer authorization.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11
