# Inbound `request_snapshot` handler — `handleRequestSnapshot`, render arm deleted (#2540)

`request_snapshot` is a v2 **control** envelope (phone → binary), intercepted in `dispatchAppFrame`'s discriminator switch **before** `dispatch.Route` (the same boundary `rekey_request` uses). It backed ADR 025's always-available, parser-independent live-view escape hatch — the floor of the safe-degradation strategy — from #618 until #2540 deleted the render arm.

**#2540 deleted the render arm and its three seams**, because production could no longer reach it: #2539 had already stopped wiring the daemon-side render seam (`startRelayV2` left `Snapshotter` nil), so `handleRequestSnapshot` never got past the `Snapshotter == nil` branch in any deployed binary. Deleted: the `ScreenSnapshotter` interface, the `(*supervisor.Supervisor).ScreenSnapshot` render seam, and the `V2SessionConfig.Snapshotter` / `.SnapshotSettings` / `.SnapshotUsage` fields — along with the bootstrap-scoped "leak detector" test fixtures in `v2session_settings_read_test.go` that existed only to wire the latter two (`bootstrapReads` and the disjoint `bootstrapRead*` values; with the fields gone the compiler enforces what the counter guarded). `protocol.RequestSnapshotPayload` / `ScreenSnapshotPayload` and the `Type*` constants stay declared — see [protocol-package-screen-snapshot-payloads.md](protocol-package-screen-snapshot-payloads.md) — as does `KnownConversation`, which `handleMCPStatusRequest` also depends on.

**The handler.** `handleRequestSnapshot(ctx, s, env)` runs on the single `Run` dispatch goroutine and now does two things: decode the payload (a decode failure is tolerated — it leaves `ConversationID == ""`, which the membership check rejects as not-found), then apply the `KnownConversation` gate and reply:

| Condition | Reply | Code | Retryable |
|---|---|---|---|
| Malformed payload / empty `conversation_id` | `error` | `conversation.not_found` | false |
| Unknown / foreign `conversation_id` | `error` | `conversation.not_found` | false |
| `KnownConversation == nil` (optional seam) | `error` | `conversation.not_found` | false |
| Known `conversation_id` | `error` | `server.binary_offline` | true |

The gate runs **before** the offline reply (pinning the #1101 ordering, now enforced by test assertions on `Code`/`Retryable` rather than by a `TypeScreenSnapshot`-vs-`TypeError` switch, since both arms now return `TypeError`). Error replies carry only a static message constant (`msgSnapshotConvNotFound` / `msgSnapshotOffline`); the `conversation_id` and any decode error are never echoed or logged. Both replies go through `m.forwardEnvelope` (the seal-and-forward path, never the public `Push`) via `snapshotReplyError`.

**Security.** `TestV2Session_OpenState_RequestSnapshot_NeverLogsScreenText` was deleted along with the two other success-reply tests (`_Repeat`, `_UsageShrinks`): deleting it is safe because no rendered screen text exists in the process anymore to leak, and both surviving error replies carry only the two static constants. The five surviving rows of `TestV2Session_OpenState_RequestSnapshot` (foreign id, nil `KnownConversation`, empty id, malformed payload, known id) each assert `Code` **and** `Retryable` unconditionally, closing the gap a type-only check would leave now that both arms answer `TypeError`.

**Concurrency.** Unchanged: no new goroutine, channel, or shutdown step. The handler runs only on `Run`; `KnownConversation` takes a `conversations.Registry` RLock (leaf, bounded).

`TestV2Session_RequestSessionSettings_AnswersWithoutASnapshotter` — the desktop#491 assertion that one manager answers `request_session_settings` with the run configuration while `request_snapshot` reports offline — is renamed `TestV2Session_RequestSessionSettings_AnswersWhileSnapshotOffline`, since there is no longer a `Snapshotter` to name.

**Reading this handler's history:** [`codebase/618.md`](../codebase/618.md) (original render arm + seams), [`codebase/848.md`](../codebase/848.md) / [`codebase/857.md`](../codebase/857.md) (settings/usage side-load, now deleted), [`codebase/1101.md`](../codebase/1101.md) (the gate-before-offline ordering these tests still pin). Clients read run configuration from [Inbound `request_session_settings`](v2-session-manager-state-machine-inbound-request-session-settings-the-rea.md) instead.
