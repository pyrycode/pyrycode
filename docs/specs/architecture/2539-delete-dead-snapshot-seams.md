# #2539 — stop wiring the dead screen-snapshot seams, delete the bootstrap usage reader

Short plan: a deletion of dead wiring with no new type, state or failure mode.

## Files read

- `cmd/pyry/relay.go` → `startRelayV2` (the `snapshotUsage` binding, the `runConfig` comment, the `V2SessionConfig` literal's `Snapshotter` / `KnownConversation` / `SnapshotSettings` / `SnapshotUsage` entries, the stream-mode branch comment, the doc's turn-stream gate paragraph); `relayWiring` fields `claudeSessionsDir`, `bootstrapIDFn`, `snapshotSettings`, and the `modelWindows` / `sessionTranscriptDir` field docs; `runConfigFor` doc.
- `cmd/pyry/snapshot_usage.go` → `snapshotUsageFor` (stays; doc names the deleted symbols), `fixedTranscriptDir`, `bootstrapSnapshotUsage` (both deleted).
- `cmd/pyry/main.go` → the `snapshotSettings` closure and the three `relayWiring` assignments; `boundRunSettings` doc. Local `claudeSessionsDir` stays (`sessions.Config.ClaudeSessionsDir`, `sessionTranscriptDir`).
- `cmd/pyry/attach_file.go` → `fileAttacher` doc (compares against `snapshotSettings`).
- `cmd/pyry/session_transcript_dir.go` → `sessionTranscriptDir` doc (compares against `bootstrapSnapshotUsage`).
- Tests naming deleted symbols: `snapshot_usage_test.go` (`fixedTranscriptDir` callers, `TestBootstrapSnapshotUsage*`), `relay_guard_test.go` (two guards cite `TestBootstrapSnapshotUsage`'s doc for "startRelayV2 has no test"), `run_config_test.go` → `TestRunConfigFor_NoSessionsDirectoryStillResolves`, `session_model_window_lookup_test.go`, `session_transcript_dir_test.go` → `TestSessionTranscriptDir_NoDaemonDirectoryBuildsNoResolver`, `internal/e2e/relay_v2_stream_model_window_test.go` → `TestRelayV2_StreamSessionSettingsReportsTheObservedWindow`.
- `internal/relay/v2session_replay.go` → `handleRequestSnapshot`: with `Snapshotter` nil it returns `server.binary_offline` after the `KnownConversation` gate, before reading either other seam. Not edited.

## Change

In `startRelayV2`, drop `Snapshotter: nil`, `SnapshotSettings` and `SnapshotUsage` from the `V2SessionConfig` literal (all three become their zero value, which is what the handler already saw for `Snapshotter` and never reached for the other two). `KnownConversation` stays; its comment keeps the gate-before-offline ordering and the #1101 existence-oracle reason and loses only the `Snapshotter` sentences — reworded to "no snapshotter is wired, so the request lands in the offline arm after this gate". Delete the `snapshotUsage` binding and its comment, and trim the `runConfig` comment of the "called a second time" and "fixed folder" paragraphs. Delete `bootstrapSnapshotUsage` and `fixedTranscriptDir`, the `relayWiring` fields `claudeSessionsDir` / `bootstrapIDFn` / `snapshotSettings`, their assignments in `main.go` and main.go's `snapshotSettings` closure.

Comments that used a deleted symbol as a comparison keep their point and drop the comparison: `snapshotUsageFor`'s doc (now "the reader behind `RunConfigFor`'s usage half"), `runConfigFor`'s doc (the nil-resolve build-time rule stands on its own; the either-half contrast becomes one against `snapshotUsageFor`'s nil-folder rule), the `modelWindows` and `sessionTranscriptDir` field docs, `fileAttacher`'s, `boundRunSettings`', `sessionTranscriptDir`'s, and the test comments above. The two `relay_guard_test.go` guards keep "startRelayV2 has no test and cannot cheaply get one" without the citation. The `startRelayV2` doc paragraph and stream-mode comment that say the turn stream gates / does not gate on `claudeSessionsDir` are rewritten to say the stream-mode drain reads parsed events from the sink, not an on-disk transcript.

`snapshotUsageFor` and its `TestSnapshotUsageFor_*` tests stay; the tests get a test-local one-folder resolver (`oneFolder(dir)`, nil for `""`) in place of `fixedTranscriptDir`. `TestBootstrapSnapshotUsage` and `TestBootstrapSnapshotUsage_ReportsTheObservedWindow` are deleted.

`internal/relay` is untouched; the declared fields are a follow-up. `screenSnapshotterOrNil` cites are out of scope.

## Testing strategy

No new logic, so no new proof. Nothing observable changes: `handleRequestSnapshot` never reached the two removed seams, and `Snapshotter` was already nil. Existing coverage: `TestSnapshotUsageFor_*` (reader), `TestRunConfigFor_*` (live usage path), the e2e `request_snapshot` / session-settings specs under `internal/e2e`. Gate: `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`, plus `grep -rnE 'bootstrapSnapshotUsage|fixedTranscriptDir|bootstrapIDFn' cmd internal/e2e` empty and a hand check of every `claudeSessionsDir` / `snapshotSettings` hit in `cmd/pyry`.

## Documentation handoff (pending — documentation stage)

`docs/knowledge/features/contextwindow-package.md` names `bootstrapSnapshotUsage`; update it to say the only usage reader left is the conversation-keyed one behind `RunConfigFor`.
