# Request partial messages on the interactive spawn

## Files read

- `internal/streamsup/runner.go` → `buildArgs`, `beginSpawn` — owns the fixed stream-json argv prefix used for both create and resume spawns.
- `internal/streamsup/runner_test.go` → `TestBuildArgs`, `TestBuildArgs_DoesNotMutateBase`, `TestBuildArgs_StableIDAcrossFirstAndResume` — pins the complete argv shape and the existing create/resume invariants.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` → `TestInteractiveStreamLiveness`, `drainForCompletedTurn` — drives a live attached conversation and drains every client frame through terminal idle.
- `internal/e2e/realclaude/interactive_stream_unrecognized_test.go` → `TestInteractiveStreamNoUnrecognizedOnToolTurn` — exercises the widest normal tool-event inventory and inherits the shared drain's fail-fast assertion for `unrecognized_message`.
- `cmd/pyry/interactive_turn_v2.go` → `coalesceWindow`, `flushDelta` — establishes that text chunks are already coalesced into client frames on a 250 ms window.
- `docs/knowledge/features/streamsup-package.md` → “Turn I/O — envelope write + stdout parser” — documents the partial `stream_event` mapping and settled-text suppression already delivered by #2270.
- `docs/knowledge/features/e2e-realclaude.md` → “Make target” — establishes that the tagged live suite is a separate dispatcher-run gate.
- `docs/knowledge/features/development-verification.md` → “Test execution and artifact survival” — requires executed-test evidence rather than treating an exit code as proof.

## Change

Add the static `--include-partial-messages` literal to `buildArgs`' fixed stream-json prefix, before caller-supplied base arguments and the create-or-resume identifier pair. Update every whole-argv row in `TestBuildArgs`, so both `--session-id` and `--resume` forms fail before the production change and pin the flag after it.

Retune `TestInteractiveStreamLiveness` to request a reply long enough for live generation to cross multiple `coalesceWindow` intervals. Keep `drainForCompletedTurn`'s one-delta contract for its ten existing callers, and add a narrow sibling entry point backed by the same drain loop that requires two non-empty, matching `assistant_delta` frames before accepting terminal idle. `TestInteractiveStreamNoUnrecognizedOnToolTurn` needs no code change: it already uses the shared drain, which fails immediately on `unrecognized_message` and otherwise requires terminal idle.

No production parser, relay-volume, protocol, concurrency, or error-handling behavior moves. The existing parser owns untrusted child output, and the existing interactive emitter owns chunk coalescing.

## Testing strategy

- RED: run `TestBuildArgs` before production editing; the complete argv expectations include the new flag and must fail against the old prefix.
- GREEN: run `go test -race ./internal/streamsup/...` after updating `buildArgs`.
- Compile the tagged live package without executing live-Claude tests, then run `go vet ./...` and `go build ./cmd/pyry`.
- The dispatcher runs the full tagged live suite for the `needs-real-claude` label. Its evidence must show `TestInteractiveStreamLiveness` receiving at least two non-empty matching deltas before idle, `TestInteractiveStreamNoUnrecognizedOnToolTurn` reaching idle without `unrecognized_message`, and a non-zero executed-test count.

## Documentation handoff

No pending shared documentation change; the issue specifies no documentation-only acceptance criterion.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — `buildArgs` adds only a compile-time literal; untrusted subprocess stdout continues to cross the existing single parsing boundary at `Parser.Write` and `consumeLine`.
- [Tokens, secrets, credentials] No findings — the flag creates, stores, logs, rotates, and revokes no token or credential, and the live tests reuse the existing authenticated harness without inspecting credentials.
- [File operations] No findings — the change adds no file access and leaves the existing transcript-selection behavior in `useCreateForm` untouched.
- [Subprocess / external command execution] No findings — `buildArgs` passes a static argument directly through `exec.CommandContext`; it introduces no shell and no caller-controlled interpolation.
- [Cryptographic primitives] No findings — the change adds no cryptography and does not alter the established Noise transport used by the live test.
- [Network & I/O] No findings — partial child events remain subject to the parser's existing line handling and the emitter's `coalesceWindow`; the change adds no listener, connection, read loop, or timeout.
- [Error messages, logs, telemetry] No findings — production adds no logging, and the live assertion records only delta sequence and byte length rather than model-generated reply text.
- [Concurrency] No findings — the change adds no goroutine, lock, channel, or shutdown path; existing parser-to-emitter sequencing is unchanged.
- [Threat model alignment] No findings — this is a static Claude subprocess capability flag, not a new CLI input or relay protocol surface; the existing authenticated and encrypted attached-client path remains unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

- 2026-09-11, verifier finding on `drainForCompletedTurnWithMinimumDeltas`: preserve `drainForCompletedTurn`'s pre-M1 behavior by ignoring a matching terminal idle while no non-empty delta has arrived. A queued idle can belong to the cancelled turn consumed by `drainForCancelledTurnEnd`, so it cannot identify the new turn. Once at least one non-empty delta identifies the active turn, terminal idle remains authoritative: the helper accepts it at the requested minimum and fails when the positive count is below that minimum.
