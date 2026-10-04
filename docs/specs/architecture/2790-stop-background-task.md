# Stop one live child's background task

## Files read

- `internal/streamsup/envelope.go` → `controlRequestInner`, `WriteMCPReconnect`: structured single-line control encoding and field omission.
- `internal/streamsup/runner.go` → `actuateMCP`, `QueryAppliedSettings`, `retireStdinGeneration`: child snapshot/register/recheck, context-bound writes and generation ownership.
- `internal/streamsup/parser.go` → `registerMCPActuation`, `claimMCPActuation`, `mcpActuations`, `retireParserChild`: first-match verdicts, write gate and lifecycle cleanup.
- `internal/streamsup/mcp_actuation_test.go`, `mcp_status_query_test.go`, `applied_settings_query_test.go`: correlation helpers and blocked-write doubles.
- `internal/streamsup/envelope_test.go`: existing byte-exact envelope assertions.
- `docs/knowledge/features/streamsup-package.md`: held-open stdin ownership and teardown.
- `docs/knowledge/features/streamsup-package-turn-io-envelope-write-stdout-parser.md` → control-response correlation: private claims precede shared decoding and logging; cancellation must bound the write itself.
- `docs/knowledge/features/development-verification.md` → lifecycle-neutral testing and privacy assertions: exercise idle and open turns, and distinctive secret sentinels.
- `CODING-STYLE.md`: Go error, concurrency and test conventions.

## Context

Claude accepts a `stop_task` control request, but the daemon cannot yet send it and learn acceptance. This ticket adds that primitive on the concrete Runner; acceptance does not prove task completion. Client authorization, membership, wiring and live proof belong to #2791/#2792. No decision record is needed.

One deliverable: a correlated task-stop primitive. Forecast: approximately 650 written lines including tests and this plan; zero exported types/interfaces, two existing helper consumers, four acceptance criteria, fewer than ten reject branches. No overlapping remote feature branches touch the production files. The completed plan remains within all five limits.

## Design

- Add nullable `TaskID` to `controlRequestInner`, populated only for stop requests; the pointer preserves empty string data while other subtypes retain their exact bytes.
- Add `WriteStopTask(w io.Writer, requestID, taskID string) error`: structured JSON, one write, one terminating newline, nil writer refusal and detection of a short write.
- Add `Runner.StopTask(ctx context.Context, taskID string) bool`. Forward the string unchanged; return true only for a completed write and the first exact-id success response on the captured live generation with a still-live caller context.
- Extract the common `actuateMCP` flow into a private control-actuation helper. MCP callers retain their eligibility gate and existing write behavior; stop requests omit MCP provenance and add the blocked-write context callback.
- Reuse `registerMCPActuation` and its existing `mcp-actuate-` namespace, write gate, first-match claims and lifecycle cleanup. No new prefix or correlator. Update comments to include task stops.
- Normal acceptance, refusal and response-wait cancellation never write interrupt/user envelopes or change the child or turn state. Keep the method off `sessions.Runner`; initialize affordances remain unchanged.

## Concurrency model

Snapshot stdin and generation under `Runner.mu`, register outside that leaf lock, then recheck the binding before writing. For stop writes only, install `context.AfterFunc` that retires the captured generation; stop or join it immediately after writing. Closing the captured pipe releases a blocked write; retirement cannot close a successor. Resolve the write gate before waiting for the private buffered result. Check the generation again before reporting acceptance, covering a reply already claimed during child retirement. Caller cancellation and lifecycle cleanup remove pending registrations; retired ids remain consumed without tombstone growth. No persistent goroutine is introduced.

## Error handling

All unavailable/refused outcomes collapse to false. No task id, writer error or Claude error text is logged or returned by `StopTask`. A silent live child waits only as long as the caller context permits; no invented timeout or mandatory deadline. Cancellation during a blocked write is the sole exception to preserving the captured child, using the existing generation-retirement primitive.

## Testing strategy

Write tests first and observe failure before implementation. Pin normal, empty and hostile-string request bytes, one-line escaping, short-write refusal, and omission of task_id on existing envelopes. Exercise success/error/missing/unknown subtype, unavailable states, pre-cancellation, silent-child deadlines, overlapping stops plus MCP, unrelated ids, duplicate/late replies and private logs/sink. Exercise normal idle/open-turn preservation, early reply versus failed write or ended context, exit/replacement boundaries, and deterministic blocked-write cancellation with successor preservation. Reuse existing test helpers. Run `go test -race ./internal/streamsup/...`, `go vet ./...`, and `go build ./cmd/pyry`; the verifier owns the full-module gate.

## Open questions

None. Live task completion is intentionally outside this acceptance-verdict contract.

## Documentation handoff

Pending for the documentation stage: in `docs/knowledge/features/streamsup-package-turn-io-envelope-write-stdout-parser.md`, extend the control-response correlation discussion beside `Runner.ReconnectMCPServer` / `Runner.SetMCPServerEnabled`. Describe `Runner.StopTask`, boolean acceptance versus task completion, shared private correlation, omission of MCP eligibility, and caller-context behavior including the blocked-write retirement exception. Client wire documentation remains with #2792.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `WriteStopTask` encodes task ids as JSON string data, including empty and hostile input; `claimMCPActuation` trusts only a locally minted exact id and success subtype. OUT OF SCOPE: client authorization and membership belong to #2792.
- [Tokens, secrets, credentials] No credentials are created or stored. Task ids and child/writer error text remain private through a boolean-only Runner result and payload-free ack decoding.
- [File operations] No new paths or filesystem operations; the existing captured stdin pipe is the only handle written or closed.
- [Subprocesses] No shell, argv or environment changes. Stop writes structured data to an existing child; only blocked-write cancellation can retire that generation.
- [Cryptography] Correlation ids are local sequencing, not authentication tokens. No cryptographic changes.
- [Network and I/O] No new socket reader or network surface. SHOULD FIX: detect short writes and bound blocked writes with the caller context; both are in the design and tests.
- [Errors, logs, telemetry] No logging in the new Runner path. Tests must feed emitting payloads and distinctive error sentinels to prove private claims precede shared logging and event delivery.
- [Concurrency] SHOULD FIX: a claimed response can leave the pending map before child cleanup; recheck captured generation before acceptance. Register/recheck preserves retirement coverage, locks are never nested, and the cancellation callback is stopped or joined.
- [Threat model] This concrete daemon primitive grants no new remote access. Network authorization and live task-completion evidence are deferred to #2792.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04

## Revisions

- 2026-10-04 — Verifier red-gate finding: `TestRelayV2_StreamAnnouncedResetFollowsClaude`
  ended its collection at `turn_end`, although `sessionTransitionEmitterV2.Run` and
  the turn emitter deliver frames from independent goroutines. The focused PR run
  reproduced zero transitions in three of ten runs. A test-only overlay delaying
  `sessionTransitionEmitterV2.broadcast` on the exact pre-change baseline reproduced
  the same failure, establishing that the assertion assumed an ordering the existing
  production contract never promised. Change only the test's collection window to
  await both the first turn's completion and its reset frame, in either order under
  the existing deadline. Preserve the exact transition-count, identity, usage and
  second-turn assertions. No stop API or production contract changes. Additional
  files read: `internal/e2e/relay_v2_stream_announced_reset_test.go` →
  `TestRelayV2_StreamAnnouncedResetFollowsClaude`, and `cmd/pyry/session_transition_v2.go`
  → `Enqueue`, `Run`, `broadcast`: asynchronous transition delivery. Verify the
  corrected test against the delayed baseline, repeat it against the PR, then run
  the race-enabled fake e2e tier and the builder-owned checks. The dispatcher owns
  the full-module gate. Total written work remains below 800 lines, with no new
  types, interfaces, production call sites or reject branches.
