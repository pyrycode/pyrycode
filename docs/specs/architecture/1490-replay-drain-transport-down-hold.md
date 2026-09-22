# #1490 — hold `drainReplayOnce` while the relay leg is down

Short plan: one guard and one extra wake signal, no new type, state or failure mode.

## Files read

- `internal/relay/v2session_replay.go` → `drainReplayOnce` — the one-event-per-pass replay pump; its abandon branch (`s.replayQueue = nil` on a `forwardEnvelope` error) is why the probe must sit here, not in `forwardEnvelope`.
- `internal/relay/v2session_replay.go` → `dropInlineReplyIfDown` — documents the same "probe at the call site" rule for the inline replies (#1526).
- `internal/relay/v2session.go` → `transportDown`, `drainOnce` — the #874 hold this mirrors (return before any pop, no re-signal).
- `internal/relay/v2session.go` → `(*V2SessionManager).Run`, the `<-m.cfg.Reconnect` arm — today re-signals only `drainCh` (#875).
- `internal/relay/v2session_replay_test.go` → `newInjectManager`, `offlineHandshake`, `TestV2Session_Reconnect_Paced_OnePerRunPass` — direct-drive pattern for the hold test.
- `internal/relay/v2session_test.go` → `gatedRecorder`, `TestV2Session_Push_FlushesOnReconnectSignal` — the Connected/Reconnect seams used by the Run-driven recovery test.

## Change

`drainReplayOnce` gains a first-line `if m.transportDown() { return }`: while the leg is down it pops nothing, seals nothing (no send-nonce spent), leaves `replayThrough` alone and does **not** re-signal `replayCh`, so a down leg cannot busy-spin Run. It is a hold like #874, not a drop like #1525/#1526 — the tail must survive. The probe stays out of `forwardEnvelope`, because a transport-down error there would take the abandon branch and throw the tail away.

Because the hold consumes the `replayCh` signal without re-arming it, something must wake the pump on recovery. The `Reconnect` arm in `Run` additionally does a non-blocking send on `replayCh` next to its existing `drainCh` send. Ordering stays correct with no further change: `drainOnce` already skips a conn whose `replayQueue` is non-empty, so live events queued during the outage wait behind the held tail, and `drainReplayOnce` re-signals `drainCh` once the tail empties.

Nothing else moves: `m.send`'s error contract is untouched, and the single-frame race at the up→down instant is accepted as in #874/#1525/#1526. Advancing `replayThrough` only after `Outbound` accepts is out of scope per the ticket.

## Testing strategy

Both in `internal/relay/v2session_replay_test.go`, written RED first.

- **Hold, direct drive** (`newInjectManager` + injected open session, `cfg.Connected` set to a down probe before any call, Run not started): several `drainReplayOnce` passes forward zero frames, leave `len(replayQueue)` and `replayThrough` unchanged, and leave `replayCh` empty. Then flip the probe up, drain, and decrypt every frame with the phone's recv `CipherState` from its start — the first frame opening proves no send-nonce was spent while down (no nonce accessor needed).
- **Mid-replay drop, Run-driven** (`startManager` with `Connected`, `Reconnect`, a replay ring of N events and a hello with `last_event_id=0`): the outbound flips the leg down from inside itself after K replay frames, so the drop lands exactly mid-replay. The test waits for a down probe, pushes one live event, flips up and signals `Reconnect` with no further Push. Assert the phone decrypts noise_resp + all N replay frames once each in ascending event-id order, then the live event, with no `resync`. Without the fix the post-drop frames are sealed into a failing outbound and the count never arrives.

## Documentation handoff

None required by the ticket. The documentation stage may note the replay-drain hold beside the #874 hold in `docs/knowledge/features/relay-package.md` (pending, documentation stage).
