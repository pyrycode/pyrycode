# Durable history ids through reconnect replay (#2909)

## Files read

- `internal/eventring/ring.go` → `Event`, `Ring.Append`, `Ring.After`: atomic publication, value-copy reads, unchanged id and retention policy.
- `cmd/pyry/interactive_turn_v2.go` → `interactiveTurnEmitterV2.emit`: one logical timestamp, ring append, history append and recipient fan-out.
- `cmd/pyry/operator_message_v2.go` → `operatorMessageEmitterV2.broadcast`: the committed operator message already carries optional history metadata.
- `cmd/pyry/conversation_history.go` → `appendConversationHistory`: successful append provenance; absent/failed storage returns nil with content-free logging.
- `internal/relay/v2session_replay.go` → `replayMissed`, `drainReplayOnce`: daemon-resolved conversation, bounded replay, one sealed frame per pass.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: paired-device validation precedes reconnect replay.
- `internal/protocol/envelope.go` → `Envelope.HistoryEntryID`: optional wire field, stale replay-omission comment.
- `cmd/pyry/live_history_id_test.go` → `TestLiveProducers_HistoryEntryID`: distinct connection/ring/history ids and storage/recipient scenarios to extend.
- `cmd/pyry/channel_post_v2_test.go` → `TestChannelPostEmitterV2_FrameShape`: preserve channel-post omission.
- `internal/eventring/ring_test.go` → ring append/retention tests: metadata must survive normal retention and value ownership.
- `internal/relay/v2session_replay_test.go` → `reconnectScenario`, paced replay tests: authenticated Noise replay harness and ordering/pacing coverage.
- `docs/knowledge/features/protocol-package.md`, `protocol-package-types-envelope.md`: history ids convey positions, never authorization; id namespaces cannot be equated.
- `docs/knowledge/features/eventring-package.md`: ring-wide ids remain independent from per-conversation retention and history ids.
- `docs/knowledge/features/relay-package.md`, `v2-session-manager-state-machine-reconnect-replay-hello-last-event-id-rin.md`: replay uses the existing seal path and Run ownership.
- `docs/knowledge/features/history-package.md`, `streamsup-package-draining-turnevents-into-the-interactive-emitter.md`: one append per logical event and operator commit placement.
- `docs/knowledge/features/cli-verb-dispatch.md`, `development-verification.md`, `CODING-STYLE.md`: package practices, raw optional-key assertions, meaningful distinct fixture values.
- `docs/protocol-mobile.md` → Security model: authenticated encrypted delivery, replay-window bounds and existing residual risks.

## Context

Live history-backed frames carry the successful append's durable per-conversation id,
but replay loses that metadata. Retaining it lets reconnecting clients advance the
shared read mark without fetching history solely to join the event to a durable id.
This is one metadata-propagation deliverable; no decision record is needed.
No overlapping remote feature branch touches the planned files.

## Design

Add scalar `Event.HistoryEntryID` (zero means absent), and
`Ring.AppendWithHistoryID(convID, typ, payload, ts, historyEntryID *uint64) uint64`.
It snapshots the optional id into the event before publication under the ring mutex.
Keep the existing `Append` signature, delegating with nil metadata: no fixture or
consumer migration. Payload ownership, ring ids, timestamps and retention stay intact.

Interactive emission appends history first, then publishes the event and its result
together to the ring, before recipient enumeration. Failed/absent storage still
publishes the ring event. Operator broadcast passes the existing committed id without
another history write. Replay constructs a non-nil envelope pointer only for nonzero
retained ids. Replay writes no history. Channel posts keep using `Append`; session
transitions remain outside the ring. Correct the envelope comment accordingly.

## Concurrency model

No new goroutines or locks. The emitter retains its existing single-writer model;
operator broadcast keeps its mutex. The ring mutex publishes the complete event.
Scalar metadata avoids aliasing a producer-owned pointer or an `After` result back
into retained state. Replay remains owned by the manager Run goroutine and retains
its one-frame-per-pass pacing and existing context shutdown path.

## Error handling

Reuse `appendConversationHistory`'s nil result for absent/failed storage. No additional
reject branches, retries or fallible operations. Replay and fan-out error handling
remain unchanged. Zero metadata omits the key entirely.

## Testing strategy

Extend the existing producer matrix to compare retained metadata with the stored
entry across successful, absent/failed storage and no-recipient scenarios. Preserve
channel-post omission. Add ring coverage for nil, zero and successful metadata,
pointer snapshots, copied reads and retention. Extend the reconnect harness to expose
decrypted raw JSON without migrating callers. A real temporary history store with
seeded ids proves authenticated missed-event replay matches a fresh stored entry,
with history and ring ids deliberately distinct, no new history entry, and unchanged
payload/timestamp. Raw replay JSON proves omission for unbacked events. Existing
ring and replay tests cover ids, ordering and pacing.

Run focused tests red before implementation, then race tests for `cmd/pyry`,
`internal/eventring`, `internal/relay`, and `internal/protocol`; `go vet ./...`;
`go build ./cmd/pyry` with output outside the worktree. The verifier owns the full gate.

## Open questions

None. Estimated written work is about 350 lines including this plan and tests;
zero new exported types/interfaces, two producer call sites updated, four acceptance
criteria, and zero new reject branches. All five sizing limits are satisfied.

## Documentation handoff

Pending for the documentation stage: update `docs/protocol-mobile.md`, “Wire shapes” /
“Application envelope” (table and prose), “A history entry”, and “Joining a page to the
live stream”. State that replay of history-backed ring events carries the original
successful append's durable id; absent/failed storage and non-history-backed events
still omit it and require history/list fallback. Remove current-replay omission
claims; preserve older-daemon fallback and the distinction from connection and ring ids.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `appendConversationHistory` provides daemon-derived successful append ids; `handleNoiseInit` validates the paired device before `replayMissed`. The remote cursor selects a position in the daemon-resolved conversation, never a history id or authorization capability.
- [Tokens, secrets, credentials] No findings. Only a numeric position is retained; tokens and keys stay in the existing handshake. No credential generation, persistence or lifecycle changes.
- [File operations] No findings. Existing `Store.Append` owns path validation and file permissions; no new file operations or replay writes. Hermetic fixtures use temporary directories.
- [Subprocesses] No findings. No child process or shell invocation is introduced in production or tests.
- [Cryptography] No findings. `drainReplayOnce` continues through `forwardEnvelope` on the single Noise send owner, with fresh authenticated reconnect keys. Metadata is inside the sealed application envelope.
- [Network and I/O] No findings. No socket reader, server or frame parser changes. `Ring.After` bounds replay by existing retention; a numeric field does not widen client-directed work.
- [Errors, logs, telemetry] No findings. Reuse content-free history failure handling; add no payload, credential or path logging and no new external errors.
- [Concurrency] No findings. History append completes before atomic ring publication. The ring snapshots the optional pointer to a scalar; metadata needs no post-publication mutation or extra lock. No new goroutines.
- [Threat model] No findings. Existing paired-device authentication, Noise confidentiality/integrity, daemon-scoped replay and transport-down nonce protection remain in force. This position does not authorize reading or marking another conversation; read-mark handlers retain their own validation. Existing security-model residual risks are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-07
