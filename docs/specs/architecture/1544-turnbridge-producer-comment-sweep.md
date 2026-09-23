# #1544 — sweep Go comments naming the deleted turnbridge producer and mapper

Short plan: comment-only change, no new type, state or failure mode.

## Files read

- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2` doc (Handle "on the producer's single Run goroutine … turnbridge/producer.go", FlushSignal/OnFlush "wired by startInteractiveTurnStreamV2"), its `ring` field doc, and the `flushDelta` doc ("the producer's OnEvent / OnFlush"). The live driver is `startStreamTurnDrainV2`; the emitter is built once in `startRelayV2`.
- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2` doc, which compares itself to the PTY path's triple and "the PTY producer".
- `cmd/pyry/main.go` → `activeConversation` doc: the replay reader is the source `startRelayV2` registers via `SetReplaySource(emitter.ring, w.active.CurrentConversation)`; the goroutine reading it is the drain goroutine.
- `internal/turnevent/event.go` → `ThinkingProgress` doc, STALL DETECTION paragraph. Grep confirms no production `Stall{}` construction remains (only the interface assertion).
- `internal/streamsup/parser.go` → the block comment above `toolKind`.
- `internal/e2e/internal/fakeclaude/main.go` → the `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` and `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN` env docs, `interruptMarkerLine`, the Esc-ends-turn branch in the poll loop, `emitStructuredJSONLIfTriggered`, `appendTurnGrowth`, `appendTurnEnd`, `argvSessionID` (stem-validation note naming `resolveBoundSessionJSONL`, which no longer exists; `transcript.ValidStem` is still used by `streamsup` and `transcript` itself).
- `internal/e2e/realclaude/interactive_per_conversation_liveness_test.go` → the #996 scope paragraph.
- `internal/e2e/realclaude/interactive_stream_hook_blocked_banner_test.go` → the file doc's "mapEvent's turnevent.Banner case"; the live arm is the `turnevent.Banner` case of `MapEvent` in `internal/turnbridge/outbound.go`.

## Change

Rewrite each stale comment so it names the live code or says plainly that the consumer is gone. The emitter docs credit `startStreamTurnDrainV2` as the single goroutine calling `Handle` and routing `flushC` into `flushDelta`, and `startRelayV2` as the place the emitter (and its ring) is built. The drain doc drops the PTY comparison. `activeConversation` names the replay source `startRelayV2` registers. The STALL note says `turnevent.Stall` now has no producer in the repo, so this variant still neither masks nor triggers one. The parser note says the helpers are this package's own; their tui-driver-keyed twins in the turnbridge mapper were deleted (#1543), so there is nothing to mirror. In fakeclaude, every claim that a daemon producer/mapper reads the session JSONL is replaced with the truth: no daemon code maps the session JSONL to turn events any more (the stream-json drain consumes parsed `turnevent.Event`s from the parser), and no in-tree test sets `PYRY_FAKE_CLAUDE_JSONL_TRIGGER` or `PYRY_FAKE_CLAUDE_ESC_ENDS_TURN`; the `interruptMarkerLine` PROVENANCE block keeps the two absences but states that the consumer which made them load-bearing was deleted with the PTY path (#1543), and the Esc-ends-turn mode has no driver or consumer left. The #996 paragraph says the hermetic proof was deleted with the PTY path the bug lived on and cannot be re-pointed. The banner test cites `MapEvent`'s arm. No executable statement, declaration or literal changes.

Out of scope (ticket): `cmd/pyry/snapshot_usage.go`, `internal/transcript/transcript_test.go`; fakeclaude's `#798 modal producer` / `#708 producer` mentions (screen-classifier producers, not named by the ticket); the unreachable `Stall` arms; removing the Esc-ends-turn mode.

## Testing strategy

No new logic, no new test. Proof is the AC sweep (case-sensitive, word-boundary, over `*.go` excluding `.claude/worktrees/`) returning zero hits for each dead name with `\bMapEvent\b` still hitting; `git diff` showing only comment lines; `go vet ./...`, `go vet -tags e2e_realclaude ./internal/e2e/realclaude/`, `go build ./cmd/pyry`, and `go test -race` on the touched packages.

## Documentation handoff

None — the ticket carries no documentation requirement.
