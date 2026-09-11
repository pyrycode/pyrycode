# #2342 — prove the stdio permission prompt round trip

Ticket: <https://github.com/pyrycode/pyrycode/issues/2342>

## Files read

- `internal/e2e/realclaude/permission_protocol_spike_test.go` → `TestRealClaude_PermissionProtocol_Spike`, `captureClaudeVersion` — the direct-Claude pipe skeleton and historical stdio argv whose closed stdin and non-asserting outcome this test must replace.
- `internal/e2e/realclaude/ask_user_question_capture_test.go` → `TestRealClaude_AskUserQuestion_CapturesTheCall`, `askQuestionCaptureRead` — the current authenticated direct-Claude spawn, generated MCP-config transcription, bounded reader, and differentiated failure style.
- `internal/e2e/realclaude/mcp_status_capture_test.go` → `mcapConfigDocument`, `mcapArgs`, `mcapAwait` — the two-server production config shape and held-open stdin control exchange.
- `internal/agentrun/streamrunner/args.go` → `BuildClaudeArgs`, `ArgsParams` — the canonical stream-json, system-prompt, model, effort, turn, allowlist, and permission argument order.
- `cmd/pyry/mcp_config.go` → `permissionArgs`, `renderMCPServersConfig` — the production non-YOLO permission flags and generated `pyry_approve` / `pyry_files` registrations this test mirrors, changing only the prompt-tool value to `stdio`.
- `internal/streamsup/parser.go` → `CanUseToolRequest`, `decodeCanUseTool` — the expected `control_request` envelope and the request fields whose presence gates the response.
- `internal/streamsup/envelope.go` → `WriteCanUseToolDeny`, `marshalCanUseToolDeny` — the production response encoder used to write a correlated deny without inventing a second wire shape.
- `docs/knowledge/features/permission-protocol-spike.md` → `--permission-prompt-tool stdio` protocol spike — the Claude 2.1.143 null result this compatibility gate must overturn or confirm without erasing history.
- `docs/knowledge/features/e2e-realclaude.md` and `docs/knowledge/features/development-verification.md` → live-gate and capture guidance — normal gate reachability, redacted diagnostic censuses, witness-based proof, and the prohibition on treating a timeout as success.

## Context

Claude 2.1.143 silently ignored `--permission-prompt-tool stdio`: no permission request appeared and Bash executed. The planned production migration in #2343 assumes a newer operator Claude treats `stdio` as a bidirectional control endpoint. This ticket adds the live compatibility gate that must prove that premise before production wiring changes.

The change has one independently verifiable deliverable: one always-on `e2e_realclaude` test. It modifies no production source, introduces no exported API, updates no consumers, covers four acceptance criteria, and is expected to add roughly 250–320 test lines plus this plan, within the ticket boundary.

## Design

Add `internal/e2e/realclaude/stdio_permission_prompt_test.go` with one authenticated live test and small file-local protocol helpers.

The test will create an isolated working directory, a test-owned witness filename, an empty system-prompt file, and a mode-`0600` MCP config that mirrors `renderMCPServersConfig`'s two registrations. It launches the resolved operator Claude directly with the canonical `BuildClaudeArgs` surface: stream-json input/output, verbose output, default permission mode, generated MCP config, strict config, system prompt, model, effort, turn cap, and a read-only `Read` allowlist. The only permission-contract substitution is `--permission-prompt-tool stdio`; `--dangerously-skip-permissions` is absent.

The user turn explicitly requests one Bash command that creates the witness. Stdin remains open after that turn is written. A single stdout reader decodes only envelope metadata and the minimum permission/result fields needed by the assertions. On the first `control_request` whose subtype is `can_use_tool`, the test requires a non-empty request id, tool name `Bash`, and non-empty tool-use id, verifies the witness is still absent, then calls `streamsup.WriteCanUseToolDeny` with that request id and a fixed test-authored message.

The reader continues until the turn's `result` envelope. The test then closes stdin, waits for the child and reader, and requires a completed result plus a permission-denial entry correlated to the observed Bash tool-use id. Finally it requires the witness to remain absent. This ties the emitted ask, matching response, refusal, completed turn, and filesystem effect together without retaining or printing tool input.

Diagnostics are a content-free summary: Claude version, exit/deadline state, total decoded and malformed line counts, envelope type census, control subtype census, number of permission requests, number of result envelopes, and stderr byte count. No raw stdout, stderr, tool input, command text, MCP config, working directory, or witness path is logged.

## Concurrency model

The test owns one stdout-reader goroutine. It sends the first decoded permission request and the terminal result through buffered channels and writes all censuses into reader-owned state. The test goroutine reads that state only after joining the reader, so no mutex is needed. The subprocess context bounds the whole run; stdin is closed on every terminal path, cancellation kills a stalled child, and the reader is joined after `cmd.Wait` closes stdout.

## Error handling

- Missing Claude or credentials retains the package's existing explicit prerequisite skips; the test itself has no opt-in environment gate.
- Missing, malformed, wrong-subtype, empty-id, wrong-tool, or empty-tool-use-id permission requests fail and include the redacted diagnostic summary.
- A witness present before the response fails immediately as a prompt-tool bypass.
- A deny write failure, absent terminal result, uncorrelated denial, deadline, or final witness presence fails; none is converted into a skip or successful timeout.
- Subprocess stderr is counted but never reproduced because it can contain host paths or other operator data.

## Testing strategy

RED is established by first adding the live test with the old spike behavior still observable as a failure: no `can_use_tool` request cannot reach the deny/write success path. Since the builder environment cannot run live-Claude tests, the dispatcher-owned `needs-real-claude` gate supplies the live RED/GREEN evidence.

Offline helper tests in the same tagged package will cover decoding/census and diagnostic redaction with synthetic envelopes if those helpers contain branching logic. Touched-scope verification is:

- compile and run the tagged package's non-live helper tests without invoking a live Claude;
- `go test -race ./internal/streamsup/...` because the live test imports the production response writer but does not modify it;
- `go vet ./...`;
- `go build ./cmd/pyry`.

The dispatcher separately runs the real-Claude gate and the verifier's full-module race suite.

## Open questions

None. The request shape and deny response encoder are already represented by `CanUseToolRequest` and `WriteCanUseToolDeny`; the live test determines whether the current Claude binary honors them.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/permission-protocol-spike.md` under a new current-version finding with the observed Claude version, the exact argv differences from the preserved Claude 2.1.143 spike, and whether the correlated `can_use_tool` request/response round trip succeeded. Preserve the historical 2.1.143 finding.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — subprocess stdout crosses one explicit decoder boundary in the new test; only envelope metadata, correlation identifiers, and denial fields become assertion state, while tool input remains unretained.
- [Tokens, secrets, credentials] No findings — `WithWorktreeAuthenticated` supplies existing operator credentials to the isolated child, and neither credentials nor child output are written to logs or artifacts.
- [File operations] No findings — all paths are test-authored beneath temporary directories, the MCP config and system prompt use mode `0600`, and the witness is only checked for existence rather than opened after a caller-controlled path lookup.
- [Subprocess execution] No findings — the test executes the resolved Claude and built Pyry binaries with structured argv, never a shell; prompts and child-provided values are not interpolated into argv. Context cancellation bounds the child.
- [Cryptographic primitives] No findings — the test creates no security token or cryptographic material; correlation uses Claude's opaque non-empty request id exactly as received.
- [Network & I/O] No findings — stdout scanning has an explicit 1 MiB per-line cap, the whole child has a deadline, stdin remains open only for the bounded protocol exchange, and no network listener is introduced.
- [Error messages, logs, telemetry] No findings — diagnostics are restricted to the Claude version, counts, enum-like type/subtype values, exit/deadline state, and stderr byte count. Raw stdout, stderr, tool input, command text, config bytes, and host paths are forbidden.
- [Concurrency] No findings — one reader goroutine exits at stdout EOF, the process context and stdin close provide shutdown, and the test joins the reader before reading its state.
- [Threat model alignment] No findings — this is a local CLI compatibility test, not a relay producer. It proves fail-closed denial and makes no production trust-boundary change; #2343 owns production routing after this gate succeeds.

**Reviewer:** builder (self-review per the security-review checklist)  
**Date:** 2026-09-11
