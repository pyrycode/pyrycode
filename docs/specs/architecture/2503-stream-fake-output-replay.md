# Spec #2503 — controlled stream fake output replay

## Files read

- `internal/e2e/internal/fakeclaude/main.go` → `main`, `runStreamJSON`, `waitForTriggerFile`, and the stream environment constants — stream mode must keep bypassing all PTY/session setup while gaining one bounded replay state.
- `internal/e2e/internal/fakeclaude/stream_detect_test.go` → `userTurnLine`, `parseEmitted`, and the existing `TestRunStreamJSON_*` cases — these are the source-compatible unit seam and production-parser proof to preserve.
- `internal/e2e/internal/fakeclaude/jsonl_trigger_test.go` → the filesystem-trigger test pattern — establishes temp-file signaling without involving a real PTY transcript.
- `internal/streamsup/parser.go` → `Parser` — the production consumer that replay tests must use for valid fixture lines while raw byte assertions protect intentionally malformed or unknown lines.
- `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md` → “Stream-json mode” and its rider sections — records the no-PTY/no-transcript invariant, default-off convention, and current single-goroutine loop.
- `docs/knowledge/features/e2e-harness.md` → stream fake guidance — confirms stream-mode fakeclaude establishes no transcript and must remain separate from PTY JSONL triggers.
- `docs/knowledge/features/development-verification.md` → “Establish the change surface” and “Prove that tests distinguish the change” — requires preserving the direct test seam and making the new tests fail against the pre-change canned response.
- `CODING-STYLE.md` → concurrency and testing conventions — every new goroutine needs a shutdown path and output behavior should be verified through the real consumer.

## Context

The deterministic mobile harness can now select the production stream-json runner, but fakeclaude can only synthesize its canned echo/result pair there. Tests that need precise child stdout still depend on the PTY transcript trigger, so they do not exercise the production stream runner. This ticket adds an opt-in, one-turn stream replay that writes raw fixture fragments without parsing or normalizing them and without entering any PTY or session-JSONL path.

The change stays inside the fakeclaude test binary and its untagged unit tests. Mobile harness migration remains Mobile #613. No ADR is warranted: this is a bounded test-fixture rider following fakeclaude’s existing default-off environment contract.

## Design

### Configuration

Three stream-only environment variables define the replay contract:

- `PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST` names the required first-fragment file and enables replay when non-empty.
- `PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND` optionally names the second-fragment file.
- `PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE` names the filesystem signal paired with the optional second fragment.

`main` loads fragment bytes before entering the stream loop. The loader returns no replay when the first path is empty. When enabled, an unreadable first or second file is fatal, and the optional second path and release path must either both be present or both be absent. Reading fragments up front makes later emission byte-for-byte and keeps the release file a signal rather than a second data channel.

The stream gate remains the first mode selected by `main`, before `mustEnv` and every PTY/session setup. Replay therefore cannot enable a PTY, open a pretend transcript, or invoke the PTY JSONL trigger.

### Source-compatible stream configuration

The existing `runStreamJSON` signature remains as the compatibility wrapper for its direct unit-test callers. It maps its current arguments into an unexported `streamRunConfig` and delegates to a configured loop. `main` calls that configured loop directly so it can add the optional replay and the closable stdin handle without changing the existing call sites.

The configured loop owns an unexported replay state with immutable fragment bytes and three state transitions:

```text
waiting for first user
        |
        v
first emitted ---- no second ----> complete
        |
        +---- second configured ---> waiting for release ---> second emitted / complete
```

The first recognized user envelope in the initial state writes the first fragment directly and bypasses every canned user-response rider. While waiting for release, later user envelopes are consumed without a canned reply. Recognized control requests continue through the existing dispatch arms. A successful release writes the second fragment once, removes the signal best-effort, and marks replay complete. User envelopes received after completion return to the ordinary canned behavior, making the rider exactly one scripted turn.

Neither fragment is decoded or line-split by fakeclaude. The writer receives the configured bytes exactly, including missing trailing newlines, malformed JSON, and unknown stream-json variants needed by negative downstream tests.

## Concurrency model

Single-fragment replay and replay-absent execution retain the current synchronous `bufio.Reader` loop. A configured second fragment needs to observe a filesystem release without requiring another stdin envelope, so only that mode starts one bounded reader goroutine:

- the reader goroutine performs the existing `ReadString` operation and sends complete read results to the main stream loop;
- the main loop remains the sole stdout writer and selects between input results and a `pollInterval` release ticker only while the scripted turn awaits release;
- control requests are dispatched by that same main loop, preserving output ordering and avoiding concurrent writes;
- the configured input closer interrupts a blocked read on every early return; a stop channel also releases a goroutine blocked while delivering a read result, and the loop waits for the reader to exit before returning;
- normal EOF closes the input-result channel and ends the loop as today.

No goroutine is started on the replay-absent path, so existing behavior and teardown remain unchanged.

## Error handling

- Missing or unreadable configured fragment files fail startup through `fatalf`; a selected deterministic fixture must not silently fall back to canned output.
- A second fragment without a release path, or a release path without a second fragment, is rejected as an incomplete configuration.
- Fragment write errors and short writes stop the stream loop, matching existing stdout-write teardown behavior.
- Release-path stat errors mean “not released yet,” matching `waitForTriggerFile`; release-file removal is best-effort after a successful write because in-memory replay state is the deterministic one-shot guard.
- Input EOF or read error stops the loop. If it occurs before release, no second fragment is emitted because the child no longer has a live control/input channel.

## Testing strategy

Add an untagged `stream_replay_test.go` beside the existing stream tests.

- Single-fragment replay: send one user envelope, assert the output equals the configured raw bytes exactly instead of the canned reply, then feed the valid lines through `parseEmitted` and assert their `TextChunk`/`TurnEnd` events. Include malformed or unknown lines in the raw fragment so parser-based assertions cannot replace the byte proof.
- Two-fragment replay: drive the loop over a closable pipe, observe the first fragment before creating the release signal, send an additional user envelope plus a recognized control request while held, and assert the user creates no canned reply while the control answer does arrive. Create the signal, assert the second raw fragment follows the first/control bytes and finishes the parser-visible turn, then recreate the signal and prove the second fragment remains one-shot.
- Replay absent: keep an explicit regression assertion through the compatibility `runStreamJSON` seam that one user envelope still produces the existing canned echo and success result. Existing stream tests continue to cover initialize, interrupt, set-model, set-permission-mode, and other control paths outside replay.
- Run the package under the race detector, then the builder-owned repository vet and binary build gates.

The replay tests must redden against the pre-change loop: it emits canned output instead of the supplied fragments and cannot observe a release without another input line.

## Open questions

None. The environment names, incomplete-configuration behavior, one-turn completion semantics, and reader shutdown ownership are fixed above.

## Documentation handoff

Pending for the documentation stage: update `docs/knowledge/features/fakeclaude-binary-stream-json-mode.md`, in the “Stream-json mode” area, with the opt-in raw-fragment replay contract; first-user-turn start; at-most-two-fragment and one-shot bounds; `PYRY_FAKE_CLAUDE_STREAM_REPLAY_FIRST`, `PYRY_FAKE_CLAUDE_STREAM_REPLAY_SECOND`, and `PYRY_FAKE_CLAUDE_STREAM_REPLAY_RELEASE`; filesystem release semantics; default-off/no-PTY/no-transcript behavior; and the exact configuration Mobile #613 should consume.

## Size check

- Production source files: 1 (`internal/e2e/internal/fakeclaude/main.go`)
- Total expected written work: about 560 lines across production code, tests, and this plan
- New exported types or interfaces: 0
- Consumer call sites requiring simultaneous updates: 1 (`main`); the existing direct `runStreamJSON` callers retain their signature
- Acceptance criteria: 4
- Distinct new error/reject branches: 5 (two file reads, incomplete optional pair in two directions, fragment write failure)

All six counts remain within the one-ticket boundary.

## Revisions

None.
