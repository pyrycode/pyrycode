# #1533 — fake-tier phone-delivery proof for the per-task background-task frames

Short plan: one new hermetic test file, no production change, no fakeclaude change.

## Files read

- `internal/e2e/relay_v2_stream_session_facts_test.go` → `driveSessionFactsTurn` — the driver this test copies: `paireddevice.Setup`, `seedBoundConversation`, interactive handshake, drain to `turn_end`, milestones returned as values.
- `internal/e2e/relay_v2_stream_model_list_reconcile_test.go` → `sealedConnDriver` — the shared seal-and-send / decrypt-next factory; reused instead of re-transcribing the two closures.
- `internal/e2e/harness.go` → `StartStreamInteractiveWithRelay` (variadic `extraEnv` reaches the child), `seedBoundConversation`.
- `internal/e2e/internal/fakeclaude/main.go` → `runStreamJSONConfigured`, `loadStreamReplay`, `writeStreamFragment`, `writeStreamResponse`, `writeAssistantEcho`, `outResult`, `streamSessionID` — replay writes the FIRST fragment byte for byte in place of the first user turn's canned reply, and completes immediately when no second fragment is set.
- `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md` § "Controlled raw-output replay (#2503)" — the env contract.
- `internal/streamsup/parser.go` → `emitBackgroundTaskStarted`, `emitBackgroundTaskUpdated`, `emitBackgroundTaskNotification`, `emitBackgroundTaskProgress`, `minTaskToolCallsPerEvent` (2), `maxTaskFieldID` (256) — the field mapping and the progress rate rule (untracked task, seen=1 → accumulate and store nothing; seen=2, prev=0 → emit with the line's own values).
- `internal/turnbridge/outbound.go` → `MapEvent` arms for `turnevent.BackgroundTaskStarted` / `BackgroundTaskUpdated` / `BackgroundTaskProgress` — conversation id supplied by the bridge, every field copied verbatim.
- `internal/protocol/interactive.go` → `BackgroundTaskStartedPayload`, `BackgroundTaskUpdatedPayload`, `BackgroundTaskProgressPayload` — the wire shapes the assertions decode into.
- `internal/e2e/realclaude/testdata/task_notification_v2.1.259.json` (`frames[]` subtypes `task_started`, `task_notification`), `dropped_lines_v2.1.220.json` (`dropped_lines[]` subtype `task_updated`), `parent_tool_use_v2.1.259.json` (`frames[]` the two `task_progress`) — the fed payloads.

## Change

Add `internal/e2e/relay_v2_stream_background_task_frames_test.go` (build tag `e2e`, so `make check` runs it) with one driver and one test.

**Fragment.** Six lines plus the reply, each newline-terminated, written to a file under `t.TempDir()` and passed as `PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST=<path>` through `StartStreamInteractiveWithRelay`'s extra env:

1. `task_started` (task `buwavm27r`) — verbatim from `task_notification_v2.1.259.json`
2. `task_updated` (task `bybi8g8i8`, `patch` `{"is_backgrounded":true}`) — verbatim from `dropped_lines_v2.1.220.json`
3. `task_progress` tool_uses 1, then tool_uses 2 (task `a8eec1cd5e109aa38`) — verbatim from `parent_tool_use_v2.1.259.json`
4. `task_notification` (task `buwavm27r`, `completed`, `cat $FIFO`) — verbatim from `task_notification_v2.1.259.json`
5. an assistant text line carrying a distinctive needle and a `result`/`success` line, in `writeAssistantEcho` / `writeStreamResponse`'s shape (`session_id` `fake-stream`).

The five system lines are raw-string literals copied byte for byte, `$SESSION_ID` and `$FIFO` included; the parser never decodes `uuid` or `session_id`. The expected values are separate named constants so the assertions read against names, not against substrings of the fed lines.

**Driver** (`driveBackgroundTaskFramesTurn`) — `driveSessionFactsTurn`'s shape with `sealedConnDriver` in place of its inline closures. It returns an observation struct: raw payload slices per frame type (started, updated, progress), the unrecognized count, and the two milestones (`sawNeedle`, `sawTurnEnd`). It fatals only on transport/decode faults and an error envelope; on the drain deadline it logs counts and returns.

**Test** (`TestRelayV2_StreamBackgroundTaskFramesReachConnectedPhone`):

1. Fatal: the needle's `assistant_delta` arrived, then `turn_end` arrived (AC 1). Every fed line precedes `result`, so `turn_end` at the client implies every line went through the parser — the counts below are race-free.
2. Counts: started 1, updated 2, progress 1, unrecognized 0 (AC 2).
3. Started frame: `conversation_id` bound id, `task_id`, `tool_call_id` (claude's `tool_use_id` under the daemon's key), `description`, `task_type`, `truncated_fields` nil (AC 3).
4. Updated frames, told apart by `task_id` (fatal if either id is missing or repeated): `bybi8g8i8` carries `patch` `{"is_backgrounded":true}` with empty `status`/`summary`; `buwavm27r` carries `status` `completed`, `summary` `cat $FIFO`, empty `patch`. Both carry the bound conversation id and nil `truncated_fields` (AC 3).
5. Progress frame: the SECOND line's values — `task_id`, `description` `Reading beta.txt`, `subagent_type`, `last_tool_name`, `total_tokens` 16246, `tool_uses` 2, `duration_ms` 4546; bound conversation id; nil `truncated_fields` (AC 4). The first line (tool_uses 1) is below `minTaskToolCallsPerEvent` and produces no frame — pinned by the count of exactly one plus these values.

6. Non-leak key set (from the security review below): each of the four frames' top-level key set equals exactly its payload struct's declared keys. The fed lines carry `session_id`, `uuid`, `output_file`, `is_backgrounded`, `subagent_type`-adjacent `usage` and (on the updated and progress lines) `tool_use_id`; a frame that grew any of them would pass every decoded-field assertion above and fail only here, because decoding into the payload type discards what it does not declare. Mirrors the key-set assertion in `TestRelayV2_StreamSessionFactsReachesConnectedPhone`.

Failure messages print counts, ids and the test's own literals only, never a raw payload dump, on the roster test's rule for claude-authored command lines (these are fixture bytes here, but the shape must not be copied into realclaude). The pairing token is passed to `fakephone.Dial` and the handshake, never printed.

Nothing else moves: no production file, no fakeclaude change, no shared helper edit.

## Testing strategy

The new test is the proof. RED first: run it once with the replay env name deliberately misspelled (fakeclaude falls back to its canned echo), confirming the milestones fail fatally on a missing needle rather than any count reading green. Then GREEN with the real env. Touched-scope gate: `go test -race -tags e2e -run TestRelayV2_StreamBackgroundTaskFrames ./internal/e2e/`, `go vet ./...`, `go build ./cmd/pyry`. Byte fidelity of the five literals is checked once during the build against the capture files with `jq` (not committed).

## Open questions

- The ticket's Technical Notes say the non-`task_updated` lines' task is `buwavm27r`. The two `task_progress` lines in `parent_tool_use_v2.1.259.json` actually carry task `a8eec1cd5e109aa38`. The distinguishing claim the note makes — the two `background_task_updated` frames differ by `task_id` (`bybi8g8i8` vs `buwavm27r`) — still holds; the progress frame is asserted against `a8eec1cd5e109aa38`, the fed value. Resolved in the plan; no design change.

## Documentation handoff

The ticket has no Documentation handoff section and no documentation acceptance criterion. Nothing pending for the documentation stage beyond folding any PR lessons.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No new boundary. The fed lines are claude-authored child stdout, and the daemon's existing boundary is the top-level-only decode in `emitBackgroundTaskStarted`, `emitBackgroundTaskUpdated`, `emitBackgroundTaskNotification` and `emitBackgroundTaskProgress`. The test adds evidence at that boundary rather than a boundary: `description` and `summary` carry the literal shell command `cat $FIFO` and are asserted byte for byte at the client, so any layer that expanded or rewrote them reddens.
- [Trust boundaries] SHOULD FIX, adopted into the plan as Change step 6: decoding into the payload structs cannot see an extra key, so a frame that started carrying claude's `session_id`, `uuid` or `output_file` (a host path by contract) would pass. Assert each frame's key set equals its struct's declared keys, as the session-facts test does. The verifier checks it landed.
- [Tokens] No findings. The pairing token from `paireddevice.Setup` goes only to `fakephone.Dial` and `driveHandshakeToOpenDaemonInteractive`; no failure message formats it.
- [File operations] No findings. The fragment path is test-owned under `t.TempDir()`, written once with mode `0o600` before the daemon starts; no user input reaches a path, and fakeclaude reads it once at startup in `loadStreamReplay`.
- [Subprocess] No findings. One extra env entry reaches the daemon and its fakeclaude child through the existing `spawnWith` env set; no `sh -c`, no argv built from fed bytes. fakeclaude writes the fragment with `writeStreamFragment` without decoding or interpreting it.
- [Crypto] No findings. Noise handshake and CipherState pair come from the existing `driveHandshakeToOpenDaemonInteractive` and `sealedConnDriver`; one conn, one state pair, decrypt in arrival order.
- [Network & I/O] No findings. One 30s drain deadline bounds every receive (`sealedConnDriver`'s `nextEnv`). Every fed value is under its producer cap (longest id 30 B vs `maxTaskFieldID` 256; description, patch and summary far under 4096), which is what makes the nil `truncated_fields` assertions valid.
- [Error messages, logs] No findings. Failures print counts, task ids and the test's own expected literals; no raw payload dump on any path, and an undecodable payload fails without printing its bytes. The copied error-envelope print is daemon-authored error text, as in every sibling spec.
- [Concurrency] No findings. Single test goroutine; the harness, phone and fake relay close via `t.Cleanup`. The count race is closed by ordering (every fed line precedes `result`), not by sleeping.
- [Threat model] OUT OF SCOPE, with owners: a phone rendering `description`/`summary`/`patch` as inert text is the mobile client's contract (docs/protocol-mobile.md background-task sections), not testable here. Forging a task frame from nested tool-result text is refused by the parser's top-level-only decode and pinned by streamsup's unit tests; this ticket does not re-prove it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-22
