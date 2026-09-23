# #2540 — relay: delete the ScreenSnapshotter seam and the render arm of request_snapshot

Short plan: a pure deletion. No new type, no new state, no new failure mode.

## Files read

- `internal/relay/v2session_replay.go` → `handleRequestSnapshot` (loses the render arm; keeps the `KnownConversation` gate, then `snapshotReplyError(..., CodeServerBinaryOffline, msgSnapshotOffline, true)`), `snapshotReplyError`, the file header.
- `internal/relay/v2session_seams.go` → `ScreenSnapshotter` (deleted), `V2SessionConfig.Snapshotter` / `.SnapshotSettings` / `.SnapshotUsage` (deleted); comment-only edits on the header list and count, `Interrupter`, `SessionStarter`, `ModalResolver`, `RunConfig`, `KnownConversation`, `RunConfigFor`, `OutstandingModals`.
- `internal/relay/v2session_modal.go` → `handleNewSession`'s `LateSessionStarter` note (drops the "ScreenSnapshotter-style" comparison, keeps the optional-capability point).
- `internal/relay/v2session.go` → `forwardEnvelope` doc (names `handleRequestSnapshot` as a direct caller; after the deletion only `snapshotReplyError` calls it). Comment only.
- `internal/relay/v2session_test.go` → `fakeSnapshotter`, `TestV2Session_OpenState_RequestSnapshot` and its `_Repeat`, `_UsageShrinks`, `_NeverLogsScreenText` siblings.
- `internal/relay/v2session_inlinereply_test.go` → the "screen snapshot" row (deleted); "snapshot error" row kept.
- `internal/relay/v2session_settings_read_test.go` → `readSeams`, `allReadSeams`, `readCounts`, `countingReadSeams`, `readManagerFor`, `bootstrapReads`, `TestV2Session_RequestSessionSettings_AnswersWithoutASnapshotter`.
- `internal/relay/v2session_mint_pairing_test.go`, `internal/e2e/relay_v2_stream_run_config_test.go`, `internal/e2e/relay_v2_stream_model_window_test.go`, `internal/e2e/relay_v2_change_workspace_test.go`, `internal/e2e/relay_v2_create_workspace_folder_test.go` → comment-only fixes (nil-`Snapshotter` wording; stale `screenSnapshotterOrNil` cite → `startRelayV2` in `cmd/pyry/relay.go`, where `resolveWorkspaceDir` / `resolveWorkspaceFolder` are wired).
- `cmd/pyry/relay.go` → `startRelayV2` (verified: #2539 already stopped setting the three fields; no `cmd/pyry` consumer remains — the `snapshotUsageFor` hits there are a cmd/pyry function, out of scope).

## Change

Delete `ScreenSnapshotter` and the three `V2SessionConfig` fields. `handleRequestSnapshot` decodes the payload, applies the `KnownConversation` gate (nil or unknown → `conversation.not_found`, `retryable:false`), then replies `server.binary_offline` (`retryable:true`) unconditionally. Its success path — the `ScreenSnapshotPayload` marshal, the `v2.snapshot.served` log, the `v2.snapshot.dropped_transport_down` drop event — goes, so the handler becomes the only-error reply path and `protocol.TypeScreenSnapshot` loses its only producer. The verb, its `dispatchAppFrame` interception, `snapshotReplyError`, both message constants, `KnownConversation`, and the protocol declarations stay. Comments that used a deleted symbol as a comparison keep their point and drop the comparison.

In `v2session_settings_read_test.go` the bootstrap-scoped "leak detector" fixtures (`bootstrapRead*` constants, `readSeams.settings` / `.usage` / `.snapshotter`, `readCounts.settings` / `.usage`, `bootstrapReads` and its two assertions) drop out: with the fields deleted there is no bootstrap-scoped run-configuration source the verb could read, so the compiler now enforces what the counter guarded. The payload assertions stay.

Nothing else moves: `cmd/pyry` sets none of the fields since #2539, and no other package reads them.

## Testing strategy

- `TestV2Session_OpenState_RequestSnapshot` keeps five rows — foreign id, nil `KnownConversation`, empty id, malformed payload, known id — and drops the `snap` / `settings` / `usage` / `want{Text,Model,Effort,YOLO,Used,Window}` columns and the `TypeScreenSnapshot` switch arm. Every row asserts `Code` and `Retryable` unconditionally (no switch on `wantType`), so the #1101 ordering (gate before offline) stays pinned now that both arms return `TypeError`.
- `TestV2Session_RequestSessionSettings_AnswersWithoutASnapshotter` → renamed `TestV2Session_RequestSessionSettings_AnswersWhileSnapshotOffline`; same two-verb, one-manager desktop#491 assertion.
- `_Repeat`, `_UsageShrinks`, `_NeverLogsScreenText` deleted (they assert the success reply). The last is a security test; it is safe to delete because no rendered text remains to leak and both error replies carry only the static constants — stated in the PR body.
- RED/GREEN: this is a deletion, so the "red" is a compile failure — deleting the fields first breaks every test still wiring them; the retained rows go green against the shortened handler.
- Gate: `go test -race ./internal/relay/... ./internal/e2e/...` (e2e build only needs to compile the comment edits; run what fits), `go vet ./...`, `go build ./cmd/pyry`, and `grep -rnE 'Snapshotter|SnapshotSettings|SnapshotUsage' internal/relay internal/e2e` empty.

## Documentation handoff (pending — documentation stage)

- `docs/protocol-mobile.md`: message-type table rows for `request_snapshot` and `screen_snapshot`, and the "Screen snapshot" section — the daemon no longer emits `screen_snapshot`; `request_snapshot` answers only `conversation.not_found` / `server.binary_offline`; clients read settings and usage from `request_session_settings`.
- `docs/knowledge/features/v2-session-manager*.md`, chiefly the inbound screen-snapshot handler topic and `v2-session-manager-surface.md`: the seams were removed.
- `docs/knowledge/features/protocol-package-screen-snapshot-payloads.md`: the payload type has no producer.
