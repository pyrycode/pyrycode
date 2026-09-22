# #1526 — Probe transport-down at the six inline reply seals

## Files read

- `internal/relay/v2session.go` → `transportDown`, `forwardAppReply` (the #1525 branch whose drop shape this copies), `drainOnce` (#874 hold), `forwardEnvelope` (the shared seal primitive that must stay guard-free).
- `internal/relay/v2session_replay.go` → `handleRequestSnapshot`, `snapshotReplyError`, `emitResync`, `replayMissed` (the gap branch that reaches `emitResync` at the handshake tail), `drainReplayOnce` (untouched; its abandon branch is why the guard cannot live in `forwardEnvelope`).
- `internal/relay/v2session_settings.go` → `handleSetSessionSettings`, `settingsReplyError`. The `v2.settings.updated` log names no model/effort/YOLO/mode values (#833); the new log must not either.
- `internal/relay/v2session_debugbundle.go` → `debugBundleReplyError`, reached on-Run from `handleDebugBundleRequest` and from `handleBundleReady` after the off-Run assembly (the widened window).
- `internal/relay/v2session_appframe_test.go` → `TestV2Session_AppReply_TransportDown_BurnsNoNonce`, `dropLinesContaining`, `prolificHandler`, `waitForConnNoiseMsg` — the template.
- `internal/relay/v2session_test.go` → `gatedRecorder`, `driveToOpen`, `decryptAppFrame`, `sealAppFrame`, `fakeSnapshotter`, `bufferLogger`, `waitForLogContains`.
- `internal/relay/v2session_replay_test.go` → `buildHelloEarlyDataReplay`, `appendRingEvents` — the gap handshake for the resync row.
- `internal/relay/v2session_settings_test.go` → `fakeSettingsUpdater`.

## Context

Six inline replies on `Run` call `forwardEnvelope` directly. `forwardEnvelope` seals under `s.send` with no liveness probe, and `m.send` swallows the `Outbound` error, so a reply sealed while the relay leg is down burns a send-nonce for a frame that never arrives. The phone's recv `CipherState` never advances past it, the next delivered frame fails AEAD, and a healthy session dies at 4421. #874, #912 and #1525 fixed the same defect elsewhere by probing `transportDown()` before the seal. This ticket applies that shape to the remaining six sites.

No ADR needed: this is the fourth application of an established pattern.

File-overlap check: `origin/feature/449` touches `internal/relay/v2session.go`. The design therefore does **not** edit `v2session.go`; the helper lives in `v2session_replay.go`.

## Design

One unexported helper in `v2session_replay.go`:

```go
// dropInlineReplyIfDown reports whether an inline reply must be dropped
// unsealed because the relay leg is down, logging the drop at Debug.
func (m *V2SessionManager) dropInlineReplyIfDown(s *V2Session, event string) bool
```

Returns `false` when `transportDown()` is false (including the nil `Connected` seam). Otherwise logs one Debug line — `event=<per-site slug> conn_id=<id> reason=transport_down`, nothing else — and returns `true`. `event` is always a string constant at the call site.

Each of the six sites calls it immediately before building-to-sealing handoff and returns on `true`:

| Site | Placement | Event slug |
|---|---|---|
| `handleRequestSnapshot` | before the `v2.snapshot.served` Info (which would otherwise claim a delivery that did not happen) | `v2.snapshot.dropped_transport_down` |
| `snapshotReplyError` | directly before `forwardEnvelope` | `v2.snapshot.err_dropped_transport_down` |
| `emitResync` | before the `v2.replay.resync` Info (same reason as snapshot) | `v2.replay.resync_dropped_transport_down` |
| `handleSetSessionSettings` | after the `v2.settings.updated` Info (the update DID persist), directly before `forwardEnvelope` | `v2.settings.updated_dropped_transport_down` |
| `settingsReplyError` | directly before `forwardEnvelope` | `v2.settings.err_dropped_transport_down` |
| `debugBundleReplyError` | directly before `forwardEnvelope` | `v2.bundle.err_dropped_transport_down` |

Per-site slugs (not one shared slug) make each drop observable on its own, which is what lets each test row prove its own site guarded.

The down path is a drop, not a park — identical to #1525. Nothing is held, so recovery needs no flush. `forwardEnvelope` and `drainReplayOnce` are untouched. Side effects that precede the reply (settings persistence, snapshot render, bundle-marker clear via `handleBundleReady`'s defer) are unchanged; only the seal is skipped.

## Concurrency model

All six sites run on `Run`, the single owner of `s.send`. `transportDown()` reads a construction-time func; no lock, no atomic. The single-frame TOCTOU at the up→down instant carries over from #874/#912/#1525 unchanged and is not defended further.

## Error handling

The drop is the error handling: Debug, content-free (slug, conn-id, reason). No payload, plaintext, ciphertext, key bytes, error text, or settings values. A dropped resync marker leaves the phone un-told to reload; that frame was undeliverable anyway (m.send would discard it), so the drop only stops paying a nonce for the non-delivery.

## Testing strategy

New file `internal/relay/v2session_inlinereply_test.go`, one table-driven test `TestV2Session_InlineReply_TransportDown_BurnsNoNonce` with six rows. Shared per-row harness:

- `gatedRecorder` behind an `attempts` counter (counts every `Outbound` call, up or down — the #1525 non-vacuity rule).
- Row-specific config seams; every row also wires `Handlers{TypeListConversations: prolificHandler()}` for the recovery frame.
- A local handshake driver (mirrors `driveToOpen`, but takes the hello bytes and a pre-handshake hook so the resync row can call `SetReplaySource` and advertise a `last_event_id`).
- Trigger with the leg down, wait for **that row's** slug in the log, assert: exactly one drop line, carries `conn_id` and `reason=transport_down`, carries none of the forbidden substrings (`payload`, `in_reply_to`, `model`, `effort`, `yolo`, `permission`, `ciphertext`, `err=`); `attempts` unchanged from the post-handshake baseline.
- Recover (`up=true`), send `list_conversations`, and `decryptAppFrame` the first noise_msg under `sess.initRecv` — the nonce oracle. A burned nonce makes this MAC-fail.

Rows and triggers (each reaches exactly one of the six sites):

1. **snapshot** — `KnownConversation` accepts, `fakeSnapshotter{live:true}`; send `request_snapshot`.
2. **snapshot error** — `KnownConversation` nil; send `request_snapshot`.
3. **resync** — ring with events for a conversation, cursor returns it, hello advertises a `last_event_id` beyond the id space (gap). Outbound flips the leg down right after recording the noise_resp, so `emitResync` runs down in the same Run pass.
4. **settings updated** — `fakeSettingsUpdater{err:nil}`; send a valid `set_session_settings`.
5. **settings error** — `SettingsUpdater` nil; send a valid `set_session_settings`.
6. **bundle error** — `DebugBundler` returns an error; send `request_debug_bundle` → off-Run assembly → `handleBundleReady` → `debugBundleReplyError`.

Isolation (AC2): each row waits for its own slug, so removing one site's guard fails only that row (slug never logged → `waitForLogContains` fatal; and the attempts/nonce assertions would fail too). Verified in Phase B by mutating each call site in turn and confirming exactly one row fails.

AC5 (up / nil seam unchanged): every existing `internal/relay` test runs unmodified.

## Open questions

- Does the resync row's "flip down inside Outbound" run before `replayMissed`? Yes: `handleNoiseInit` sends the noise_resp, then calls `replayMissed` at the very tail of its success path in the same Run pass (confirmed against `v2session_handshake.go` while planning). The connect-time reconciles between them are no-ops with their seams unwired.

## Documentation handoff (pending — documentation stage)

`docs/knowledge/features/v2-session-manager-out-of-scope-deferred.md`, bullet **"Extending the `transportDown()` guard beyond `drainOnce`"**: mark the inline reply seals (#1526) done, name them as six sites, and note that the settings-report path is covered by #1525 through `forwardToRun`.

## Revisions

### 2026-09-23 — two existing tests asserted the defect (AC5 "unmodified" not met)

`TestV2Session_DebugBundle_RejectsSecondWhileQueued` and `TestV2Session_DebugBundle_PerConnIsolation` (both built on `bundleGatedManagerFor`) hold `Connected` false to keep a bundle's chunks queued, then expected the busy-reject `debugBundleReplyError` to be sealed and delivered anyway. Their helper's comment said so: forwardEnvelope "does not consult the #874 transport-down hold". That is exactly the burned nonce AC1 forbids, so AC1 and AC5's "every existing test passes unmodified" cannot both hold. AC5's own scope sentence covers the transport-up and nil-`Connected` cases, and both tests run with `Connected` reporting down, so AC1 wins.

Change: both tests now log to a buffer and observe each busy reject through its `v2.bundle.err_dropped_transport_down` drop line, via a new `waitBusyRejectDrops` helper. `RejectsSecondWhileQueued` also asserts that no noise_msg reaches the wire for the conn. Their #911 assertions (queue depth does not grow, bundler not re-invoked, B served while A is busy) are unchanged. The busy reply's wire shape with the leg up (code, message, retryable, InReplyTo) remains pinned by `TestV2Session_DebugBundle_RejectsSecondWhileAssembling`. The `bundleGatedManagerFor` comment is updated to match.

Phase B isolation evidence: removing one site's guard at a time (`if false && m.dropInlineReplyIfDown(...)`) failed exactly that site's row, six out of six. A helper that logs but returns `false` (log-then-seal) failed every row on `Outbound attempts = 2` and on the AEAD MAC check, so the nonce oracle is not vacuous.
