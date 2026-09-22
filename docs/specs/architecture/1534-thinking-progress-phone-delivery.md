# #1534 — fake-tier phone-delivery proof for thinking_progress and its 64-token rate bound

Short plan: one new test file under `internal/e2e`, no production change, no new type outside the test.

## Files read

- `internal/e2e/relay_v2_stream_background_task_frames_test.go` → `driveBackgroundTaskFramesTurn`, `TestRelayV2_StreamBackgroundTaskFramesReachConnectedPhone` — the shape copied: replay fragment written before daemon start, `seedBoundConversation`, `StartStreamInteractiveWithRelay` with `PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST`, interactive handshake, drain to `turn_end`, milestones asserted first and fatally.
- `internal/streamsup/parser.go` → `emitThinkingProgress`, `minThinkingTokensPerEvent` — emits when a line's delta `>= bound - acc`; accumulates otherwise; frame carries the crossing line's own `estimated_tokens` / `estimated_tokens_delta`.
- `internal/protocol/interactive.go` → `ThinkingProgressPayload` — `conversation_id`, `estimated_tokens`, `estimated_tokens_delta`; no text, no truncation.
- `internal/e2e/realclaude/testdata/dropped_lines_v2.1.220.json` → `dropped_lines[]`, subtype `thinking_tokens` — the source of every fed line.

## Change

New file `internal/e2e/relay_v2_stream_thinking_progress_test.go` (build tag `e2e`). A drive helper `driveThinkingProgressTurn(t, thinkingLines []string)` replays a fragment of the given capture lines, then an assistant line carrying a needle, then a `result` line, and returns an observation: each `thinking_progress` payload (raw), an `unrecognized_message` count, and the needle / `turn_end` milestones. Two tests call it:

- **Below bound** — capture indices 31 (`estimated_tokens` 64, delta 1) and 32 (126, delta 62), consecutive in the capture: accumulated 63 = bound − 1. Want zero frames.
- **Crossing** — the same two lines plus index 36 (`estimated_tokens` 1, delta 1): accumulated reaches exactly 64 on the last line, so the crossing is on equality. Want exactly one frame equal to `{conversation_id: bound conv, estimated_tokens: 1, estimated_tokens_delta: 1}` — both differ from the accumulated 64 (and from the 63 carried in).

The bound is a test-side literal (`thinkingProgressBound = 64`) and the test asserts the two feeds' delta sums are `bound−1` and `bound` (decoded from the fed lines themselves), so the feeds cannot silently drift off the edge. A lowered parser constant reds the silence half; a raised one reds the crossing half; changing `>=` to `>` reds the crossing half. The crossing feed being a strict superset of the silent one on the same path is what makes the silent zero meaningful: a wrong env name, a malformed line or a drop before the parser would red the crossing run.

Both tests assert the needle, then `turn_end`, fatally before any count; both assert zero `unrecognized_message` frames. Ids are distinct from sibling specs (`15340000-…`).

## Testing strategy

The file is the test; there is no production change to fail first. Sensitivity check instead of RED: locally lower the parser's `minThinkingTokensPerEvent` to 63 and watch the silence test red, then restore it (not committed). Gate: `go test -race -tags e2e -run ThinkingProgress ./internal/e2e/`, `go vet ./...`, `go build ./cmd/pyry`.

## Documentation handoff

None — the ticket has no documentation-handoff section and no documentation acceptance criterion.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No new boundary. The fed lines are claude-authored child stdout. The daemon's existing boundary is streamsup's top-level decode in `emitThinkingProgress`, which reads two ints and nothing else. The test adds evidence at that boundary: the crossing frame's values are asserted exactly at the client.
- [Trust boundaries] SHOULD FIX, adopted in rework: decoding the frame into `protocol.ThinkingProgressPayload` cannot see an extra key. A frame that started carrying the fed line's `session_id` or `uuid` would pass. The crossing test now asserts the raw frame's key set is exactly `conversation_id`, `estimated_tokens` and `estimated_tokens_delta`, reusing the package's `assertExactKeys`. The silent test has no frame to check.
- [Tokens] No findings. The pairing token from `paireddevice.Setup` goes only to `fakephone.Dial` and `driveHandshakeToOpenDaemonInteractive`. No failure message formats it. The fed payloads hold only the `$SESSION_ID` placeholder and capture UUIDs, with no credentials (checked against `dropped_lines_v2.1.220.json` indices 31, 32 and 36).
- [File operations] No findings. The fragment path is test-owned under `t.TempDir()`, written once with mode `0o600` before the daemon starts. No user input reaches a path, and fakeclaude reads the file once at startup in `loadStreamReplay`.
- [Subprocess] No findings. One extra env entry reaches the daemon and its fakeclaude child through `StartStreamInteractiveWithRelay`'s existing env set. There is no `sh -c` and no argv built from fed bytes.
- [Crypto] No findings. The Noise handshake and CipherState pair come from the existing `driveHandshakeToOpenDaemonInteractive` and `sealedConnDriver`. One conn uses one state pair and decrypts in arrival order.
- [Network & I/O] No findings. One 30s drain deadline bounds every receive (`sealedConnDriver`'s `nextEnv`). The frame carries two ints and no claude-authored text, so there is no truncation or size-cap dimension, unlike #1533.
- [Error messages, logs] No findings. Failures print counts, the test's own literals and the decoded two-int payload. No raw fed line is printed on any path. The error-envelope print is daemon-authored error text, as in every sibling spec.
- [Concurrency] No findings. The test runs in a single goroutine. The harness, phone and fake relay close via `t.Cleanup`. Ordering closes the count race, because every fed line precedes `result`. There is no sleep.
- [Threat model] OUT OF SCOPE: the phone's handling of the frame is the mobile client's contract (docs/protocol-mobile.md § Interactive events, where `thinking_progress` is rate-bounded and its absence proves nothing). Forging a frame from nested tool-result text is refused by the parser's top-level decode and pinned by streamsup's unit tests. This ticket does not re-prove it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-23

## Revisions

- 2026-09-23 (rework 1, verifier finding "missing `## Security review` section"): added the security-review pass above, which the `security-sensitive` label requires. The pass adopted one SHOULD FIX into the code: the crossing test asserts the frame's exact key set as well as its values.
