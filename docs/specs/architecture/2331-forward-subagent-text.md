# Forward interactive subagent text

## Files read

- `internal/streamsup/runner.go` → `buildArgs`, `beginSpawn` — owns the fixed long-lived interactive Claude prefix and the create/resume selection used on every spawn.
- `internal/streamsup/runner_test.go` → `TestBuildArgs`, `TestBuildArgs_DoesNotMutateBase`, `TestUseCreateForm_ProbeDecidesIDFlag` — pins the complete prefix, caller-argument ordering, immutability, and both session-id forms.
- `internal/streamsup/parser.go` → `emitAssistant`, `emitStreamEvent`, `streamBlock` — maps attributed completed assistant text while consuming thinking and signature-bearing partial content without publishing it.
- `cmd/pyry/stream_turn_drain_test.go` → `feedLines`, `TestStreamTurnDrainV2_FullSingleTurn`, `decodeDelta` — provides the hermetic parser→session gate→interactive emitter→mobile-envelope proof seam.
- `cmd/pyry/interactive_turn_v2.go` → `Handle`, `flushDelta`, `ensureDeltaLane` — keeps main text on the empty-parent lane, assigns a child turn id per parent, and advances sequence numbers per lane.
- `cmd/pyry/interactive_turn_v2_parent_lane_test.go` → `TestInteractiveTurnEmitterV2_InterleavedParentLanes` — establishes the already-landed lane behavior that the new live test must observe rather than reimplement.
- `internal/e2e/realclaude/harness_daemon_test.go` → `sealSendMessage`, `driveHandshakeInteractive`, `spawnBootstrapDaemon` — supplies the authenticated production-daemon and encrypted mobile-wire harness.
- `internal/e2e/realclaude/interactive_stream_running_turn_test.go` → `startStreamRunningTurnHarness`, `perConvHarness` — provides a ready interactive streamsup daemon with permission bypass for a foreground Agent/Task call.
- `internal/e2e/realclaude/interactive_stream_liveness_test.go` → `drainForCompletedTurnWithMinimumDeltas` — supplies the current live-turn inventory, timeout, and error-handling patterns.
- `docs/knowledge/features/streamsup-package.md` § “Turn I/O” — documents the fixed interactive prefix and the parser rule that thinking, signature, and partial tool input are content-free.
- `docs/knowledge/features/e2e-realclaude.md` and `docs/knowledge/features/development-verification.md` — require a live gate that actually executes, continuously drains Noise frames, and makes shape assertions without depending on model wording.
- `docs/knowledge/features/protocol-package-interactive-event-payloads.md` and ADR 025 → `AssistantDeltaPayload` policy — define parent attribution and the confidentiality boundary that keeps chain-of-thought unpublished.
- `docs/specs/architecture/2330-parent-keyed-assistant-delta-lanes.md` — records the immediately preceding emitter design and explicitly leaves enabling the Claude forwarding opt-in to this ticket.

## Context

The parser, protocol payload, and emitter now preserve subagent attribution and isolate assistant-delta counters by parent tool-use id, but Claude does not emit subagent prose unless the interactive process opts into `--forward-subagent-text`. Consequently the complete downstream lane machinery is dormant in production. This ticket enables that fixed Claude option only on `streamsup`'s long-lived interactive spawn and proves both the positive child-prose path and the existing negative thinking/signature policy.

No ADR is needed: the change activates the behavior already assigned to ticket #2331 by the parent-attribution and parent-lane designs, without changing the protocol contract or confidentiality decision in ADR 025.

## Design

`buildArgs` adds `--forward-subagent-text` to the fixed prefix immediately after the partial-message opt-in. Because both create and resume forms share that prefix, both receive the option. The caller-owned `base` slice remains cloned into the result after the fixed prefix, byte-for-byte and in its original order, and the session selector remains the final pair. The separate `agent-run` composition is not touched.

The hermetic regression feeds one attributed completed assistant message through `streamsup.NewParser`, `streamTurnSink`, `startStreamTurnDrainV2`, and `interactiveTurnEmitterV2`. Its content contains distinctive thinking text, a distinctive signature field, and distinctive ordinary text. The captured mobile envelopes must contain neither confidential marker anywhere in their serialized bytes, while the ordinary text must arrive in an `assistant_delta` carrying the supplied parent id. This exercises the production mapping boundary rather than separately unit-testing the parser and emitter.

The live-Claude test reuses `startStreamRunningTurnHarness`, sends one instruction requiring exactly one foreground Agent/Task call, and drains the encrypted mobile stream through terminal idle. It records the main-thread Agent/Task `tool_use_id`, then partitions non-empty `assistant_delta` frames into the empty-parent main lane and the matching child lane. For each lane it asserts a stable turn id and consecutive lane-local sequence values beginning at zero; it also asserts distinct main and child turn ids. Unexpected attributed lanes, multiple Agent/Task calls, malformed payloads, and terminal idle before both lanes are witnessed fail loudly. The prompt asks both the child and main thread for enough prose to make sequence advancement observable without asserting any model-authored words.

Data flow:

```text
buildArgs fixed interactive prefix
  → live Claude emits attributed subagent assistant lines
  → streamsup.Parser produces TextChunk(parent)
  → stream turn drain selects the foreground session
  → interactive emitter selects parent-keyed lane
  → assistant_delta(parent, turn_id, lane-local seq)
```

## Concurrency model

No production concurrency changes. `buildArgs` remains pure and allocates its result. Both regressions use the existing single ordered parser/drain/emitter flow; the live receiver decrypts every Noise frame sequentially so cipher nonces and wire order remain intact. Existing context cancellation and test cleanup own daemon, relay, phone, drain, and child shutdown.

## Error handling

- A missing forwarding option is caught by the exact `buildArgs` table before the live gate.
- The hermetic regression fails on missing ordinary text, incorrect parent attribution, or either confidential marker appearing in any mobile envelope.
- The live gate uses a bounded deadline and reports whether the Agent/Task call, child lane, main lane, lane-local sequence, or terminal boundary was absent or malformed.
- Unknown or unrelated mobile envelopes are still decrypted in order and ignored only after their payload class is known, preserving the receive cipher state.
- No production error path or recovery policy changes.

## Testing strategy

- RED: extend `TestBuildArgs`'s whole-argv expectations with `--forward-subagent-text`; both create and resume rows fail against the current prefix.
- RED: add the hermetic attributed-content regression; it validates the full interactive mobile path and remains green only when ordinary text is published and thinking/signature markers remain absent.
- Add the live real-Claude gate under `e2e_realclaude`; per builder policy it is not executed locally. The dispatcher runs it through `make e2e-realclaude` and must report a non-zero executed-test count.
- GREEN: run `go test -race ./internal/streamsup/... ./cmd/pyry/...`, `go vet ./...`, and `go build ./cmd/pyry`.

## Open questions

- Whether Claude emits enough frames for both lane counters to advance beyond zero is deliberately tested by a long child response plus a long main response. If the live surface settles child output into one frame, retain the stronger invariant available from the observation: both lane-local first sequences are zero after interleaving, proving neither consumes the other's counter; record the measured resolution in Revisions.

## Documentation handoff

Pending for the documentation stage:

- Update `docs/knowledge/features/streamsup-package.md` in the fixed-prefix / Turn I/O sections to state that interactive streamsup spawns enable `--forward-subagent-text`.
- Update `docs/protocol-mobile.md` in the `assistant_delta` section to state that child prose uses parent-keyed lanes, main-thread deltas keep an empty parent id, and thinking/signature content remains unpublished under ADR 025.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — Claude stdout remains untrusted input at `Parser.Write`; the hermetic full-path test specifically pins `emitAssistant`'s distinction between publishable text and confidential thinking/signature fields before any mobile envelope is formed.
- [Tokens, secrets, credentials] No findings — the implementation neither creates nor handles credentials; the live harness reuses `WithWorktreeAuthenticated` and does not inspect, print, or persist its credential.
- [File operations] No findings — production adds no file access; existing isolated live-test fixtures retain `0700` directories and `0600` state files.
- [Subprocess execution] No findings — `buildArgs` adds one fixed literal to `exec.CommandContext`'s argument vector, uses no shell, preserves caller arguments verbatim, and changes neither environment inheritance nor the existing signal/reap lifecycle.
- [Cryptographic primitives] No findings — production cryptography is unchanged; the live proof reuses the established Noise harness and `crypto/rand` key generation.
- [Network & I/O] No findings — no listener, limit, deadline, or framing behavior changes; the live drain retains an explicit turn deadline and decrypts bounded application envelopes through the existing fake-relay path.
- [Error messages, logs, telemetry] No findings — the fixed option introduces no new logging, and the hermetic regression proves the distinctive thinking/signature bytes do not enter any mobile envelope. Existing parser logging remains content-free.
- [Concurrency] No findings — no goroutine or shared-state change; tests reuse established cancel-and-join cleanup and ordered receive loops.
- [Threat model alignment] No findings — the only newly reachable content is ordinary attributed prose. The test directly enforces ADR 025's requirement that thinking and signature material remain unpublished; relay authentication, authorization, replay protection, and size caps are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-11

## Revisions

- During implementation, the live assertion was resolved to require at least two main-lane deltas and at least one child-lane delta. Every observed lane still must start at zero, remain on one turn id, and advance consecutively. Requiring two child frames would turn model chunking of one valid forwarded response into an unstated product contract; the advancing main lane plus the child's independent zero proves the counters do not share state.
- The first dispatcher live run showed that Claude emits the delegated child prompt as a known `unrecognized_message` with `site=user_block` before the forwarded child response. The live proof now decrypts and ignores that unrelated envelope class, matching the design's receive-loop contract, while retaining strict failures for malformed payloads and unexpected attributed lanes.
