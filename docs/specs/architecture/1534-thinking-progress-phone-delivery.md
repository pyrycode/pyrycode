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
