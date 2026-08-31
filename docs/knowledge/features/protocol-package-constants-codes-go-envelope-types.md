# Envelope types

Wire values for `Envelope.Type` (spec § Message types). Two architectural partitions: 16 v1 application types (closed; consumed by `dispatch.Route` via `inboundAppTypeSet`; 13 from the original #256 catalog — its three v1 bulk-history backfill types were removed as dead code in #967, see [codebase/967.md](../codebase/967.md) — + `TypeRenameConversation` #820 + `TypeDeleteConversation` / `TypeConversationDeleted` #822) and the **v2-only** set whose members are **deliberately NOT** in `inboundAppTypeSet`. The v2-only set itself spans two flavours: **inbound control envelopes** (`TypeRekeyRequest` (#454), `TypeRequestSnapshot` (#617), `TypeModalAnswer` / `TypeModalCancel` (#701), `TypeDequeueMessage` (#720), and `TypeInterrupt` (#707)), intercepted at the v2 dispatch boundary (`internal/relay/v2session.go`'s `dispatchAppFrame`) before `dispatch.Route` is called; and **outbound binary → phone events** never dispatched inbound (the five #607 interactive types, `TypeStall` (#638), `TypeApiRetry` / `TypeCompacting` (#1074), `TypeUnrecognizedMessage`, `TypeScreenSnapshot` (#617), `TypeResync` (#647), `TypeSessionTransition` (#656), `TypeModalShown` / `TypeModalDismissed` (#701), `TypeQueueState` (#720), and `TypeSessionError` (#1007, unwired — no producer yet)). Adding either to `inboundAppTypeSet` would silently route the envelope to the v1 handler chain (or expose it to an old phone) — exactly the opposite of what's wanted. **`TypeAttachmentChunk` (#1752) fits neither flavour**: it is genuinely bidirectional (upload rides it client→daemon, retrieval rides it daemon→client) and ships with no dispatch on either leg yet, so `cmd/pyry/relay_guard_test.go`'s `excludedTypes` classifies it under its own reason rather than `"push"` — see § Drift detectors below.

**v1 application types** (19; spec § v1 Message types):

| Group | Constants |
|-------|-----------|
| Handshake / control | `TypeHello`, `TypeHelloAck`, `TypeError`, `TypeAck` |
| Messaging | `TypeSendMessage`, `TypeMessage` |
| Conversations | `TypeListConversations`, `TypeConversations`, `TypeCreateConversation`, `TypeConversationCreated`, `TypePromoteConversation`, `TypeConversationUpdated`, `TypeRenameConversation` (#820), `TypeDeleteConversation`, `TypeConversationDeleted` (#822) |
| Push | `TypeRegisterPushToken` |

**v2 control-envelope types** (#454; spec `docs/protocol-mobile.md` § Re-key):

| Group | Constants |
|-------|-----------|
| Re-key | `TypeRekeyRequest` |

**v2 interactive application-event types** (#607, extended #638; spec `docs/protocol-mobile.md` § Interactive events):

| Group | Constants |
|-------|-----------|
| Interactive | `TypeTurnState`, `TypeAssistantDelta`, `TypeToolUse`, `TypeToolResult`, `TypeTurnEnd` (#607), `TypeStall` (#638) |

These six live in their **own** const block (not merged into the `TypeRekeyRequest` block) so the doc comment can distinguish control envelopes from application events — but both are "v2-only" for the partition's purpose. `TypeStall` (#638) is the wire form of an internal-only `turnevent.Stall` signal; on the wire it is just another v2 capability-gated event, so it lives in this block with its ACP-shaped siblings (the internal-vs-ACP distinction is an adapter concern, invisible to the phone). `CapabilityInteractive = "interactive"` (the wire-vocabulary constant a phone advertises to opt into this stream) lives in `handshake.go` next to the `Capabilities` field, not here.

**v2 PTY-derived status-peer types** (#1074; spec `docs/protocol-mobile.md` § api_retry / § compacting):

| Group | Constants |
|-------|-----------|
| Status peers of stall | `TypeApiRetry`, `TypeCompacting` |

`TypeApiRetry = "api_retry"` and `TypeCompacting = "compacting"` share their own adjacent const block (not merged into the interactive block above) so the doc comment can name them explicitly as PTY-derived status peers of `TypeStall`, not turn-lifecycle events. Both are outbound binary → phone only, carry the named payload structs `ApiRetryPayload` / `CompactingPayload` (`interactive.go` — see [Interactive event payloads](#interactive-event-payloads-607-638-1074)), and stay out of `inboundAppTypeSet` (two `{"api_retry-rejected"/"compacting-rejected", …, ErrUnknownType}` rows in `compat_test.go` pin the v1 rejection). Unlike `stall`, both carry an explicit `active: false` falling edge — see the payload doc below. The consumer (`cmd/pyry/interactive_turn_v2.go`'s `Handle`) is `security-sensitive`: it forwards a screen-derived attempt counter across the tui-driver substrate seal. See [codebase/1074.md](../codebase/1074.md).

**v2 screen-snapshot types** (#617; spec `docs/protocol-mobile.md` § Screen snapshot):

| Group | Constants |
|-------|-----------|
| Screen snapshot | `TypeRequestSnapshot`, `TypeScreenSnapshot` |

These two live in their **own** cohesive const block, grouping the request/response pair so a reader greps "snapshot" and finds both adjacent with their shared rationale. The pair straddles both v2-only flavours — `TypeRequestSnapshot` is an inbound control envelope intercepted before `dispatch.Route` (like `TypeRekeyRequest`), `TypeScreenSnapshot` is an outbound binary → phone event — but `TestTypeConstants_V1V2Partition` is grouping-independent, so which block a constant lives in is purely a readability choice. Both stay out of `inboundAppTypeSet`. The interception, render (via tui-driver), and push are the consumer ticket's job (`security-sensitive`), not this package's.

**v2 reconnect-resync marker** (#647; spec `docs/protocol-mobile.md` § Interactive events / Reconnect replay & resync):

| Group | Constants |
|-------|-----------|
| Resync | `TypeResync` |

`TypeResync = "resync"` is an outbound binary → phone control marker the daemon emits when a reconnecting phone's advertised `last_event_id` aged out of the bounded event ring (it must full-reload). Its **own** const block; like `TypeRekeyRequest` it has **no named payload struct** — `internal/relay` marshals an inline `struct{ ConversationID string }` at emit time. Stays out of `inboundAppTypeSet` (an old phone must never receive it; `{"resync-rejected", TypeResync, false, ErrUnknownType}` in `compat_test.go` pins that v1 `IsKnownAppType` rejects it).

**v2 session-boundary marker** (#656; spec `docs/protocol-mobile.md` § Interactive events / session_transition):

| Group | Constants |
|-------|-----------|
| Session boundary | `TypeSessionTransition` |

`TypeSessionTransition = "session_transition"` is an outbound binary → phone marker the daemon emits when its session rotates (a `/clear`, an idle eviction, or a workspace change), so a phone renders a `ThreadItem.SessionBoundary` marker (`pyrycode-mobile#336`). Its **own** const block; **unlike** `TypeResync` it carries a real multi-field named payload (`SessionTransitionPayload` in `messaging.go` — see [Session-transition payload](#session-transition-payload-656)) rather than an inline struct. A **session boundary is distinct from the eight turn-stream events** and carries **no** `event_id`. Stays out of `inboundAppTypeSet` (an old phone must never receive it; `{"session_transition-rejected", TypeSessionTransition, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). The producer is sibling #657 (`security-sensitive`); this slice is wire vocabulary only.

**v2 modal vocabulary** (#701; spec `docs/protocol-mobile.md` § Modal):

| Group | Constants |
|-------|-----------|
| Modal | `TypeModalShown`, `TypeModalAnswer`, `TypeModalCancel`, `TypeModalDismissed` |

The four modal types share **one** const block with **one** rationale comment — the **mixed inbound+outbound** cluster precedent set by the `request_snapshot`/`screen_snapshot` pair. `TypeModalShown` / `TypeModalDismissed` are outbound binary → phone events; `TypeModalAnswer` / `TypeModalCancel` are inbound phone → binary **control** envelopes intercepted at `internal/relay/v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the `TypeRekeyRequest` / `TypeRequestSnapshot` precedent — **no `dispatch.Route` handler**). All four carry real named payload structs in `messaging.go` (`ModalShownPayload` / `ModalAnswerPayload` / `ModalCancelPayload` / `ModalDismissedPayload` — see [Modal v2 wire payloads](#modal-v2-wire-payloads-701)). All four stay out of `inboundAppTypeSet` (four `{"modal_*-rejected", …, ErrUnknownType}` rows in `compat_test.go` pin the v1 rejection). The producers are siblings #703 (control loop) / #706 (two-heads ownership) / #702 (per-device answer gate); this slice is wire vocabulary only, `security-sensitive` for the **shape** review (no handler ships).

**v2 queue vocabulary** (#720; spec `docs/protocol-mobile.md` § Queue):

| Group | Constants |
|-------|-----------|
| Queue | `TypeQueueState`, `TypeDequeueMessage` |

The two queue types share **one** const block with **one** rationale comment — the same **mixed inbound+outbound** cluster precedent as the modal block. `TypeQueueState` is an outbound binary → phone queued-backlog snapshot; `TypeDequeueMessage` is an inbound phone → binary **control** envelope intercepted at `v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the `TypeModalAnswer` / `TypeRequestSnapshot` precedent — **no `dispatch.Route` handler**). `TypeQueueState` carries the named `QueueStatePayload` / `QueuedItem` structs and `TypeDequeueMessage` the `DequeueMessagePayload` struct in `messaging.go` (see [Queue v2 wire payloads](#queue-v2-wire-payloads-720)). Both stay out of `inboundAppTypeSet` (two `{"queue_state-rejected"/"dequeue_message-rejected", …, ErrUnknownType}` rows in `compat_test.go` pin the v1 rejection). The producer is #722 / the handler is #723; this slice is wire vocabulary only — and **unlike** the modal block it is **not** `security-sensitive` (`queued_msg_id` is a plain per-conversation counter, not a nonce, and dequeuing is ungated — ADR 025 § Security model).

**v2 interrupt control** (#707; spec `docs/protocol-mobile.md` § Interrupt):

| Group | Constant |
|-------|----------|
| Interrupt | `TypeInterrupt` |

`TypeInterrupt = "interrupt"` is an inbound phone → binary **control** envelope in its **own** new const block, intercepted at `v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the `TypeModalCancel` / `TypeDequeueMessage` precedent — **no `dispatch.Route` handler**). It is the **remote interrupt** — a paired phone stops the running turn, the wire form of pressing Esc locally; the daemon maps it to the neutral `turnevent.Cancel` command and routes it to claude as a single Esc keystroke (#600 maps ACP `session/cancel` onto the same neutral shape). **Unlike every other v2 control envelope it carries NO named payload struct** — no `conversation_id`, no `modal_id` nonce, no `answer_token`, no idempotency key: a bare control frame (`internal/relay` switches on `type` alone; a replayed `interrupt` just sends another Esc, idempotent in effect). Stays out of `inboundAppTypeSet` (an `{"interrupt-rejected", TypeInterrupt, false, ErrUnknownType}` row in `compat_test.go` pins the v1 rejection). The interception, capability gate, and `SendEsc` routing are the consumer ticket's job in `internal/relay` (`security-sensitive` — the **first** inbound frame gated on the `interactive` capability itself); this leaf declaration makes no trust decision. See [codebase/707.md](codebase/707.md).

**v2 new_session control** (#831, split from #824; spec `docs/protocol-mobile.md` § New session):

| Group | Constant |
|-------|----------|
| New session | `TypeNewSession` |

`TypeNewSession = "new_session"` is an inbound phone → binary **control** envelope in its **own** new const block, intercepted at `v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the `TypeInterrupt` / `TypeRequestDebugBundle` precedent — **no `dispatch.Route` handler**). It is the **remote start-new-session** — a paired phone's equivalent of typing `/clear` at the local terminal; the daemon routes it directly to the supervised claude as a `/clear` via the sealed `supervisor.StartNewSession` seam (#830). **Unlike `interrupt` it maps to no neutral `turnevent` command** — there is no announced ACP counterpart requiring one, so it drives the relay seam directly (mirroring how `modal_cancel` routes to `ModalResolver.ResolveCancel` without constructing a `turnevent` value). Like `interrupt` / `request_debug_bundle` it carries **NO named payload struct** — no `conversation_id`, no nonce, no idempotency key: a bare control frame (a replayed `new_session` just drives another `/clear`, harmless and idempotent in effect). Stays out of `inboundAppTypeSet` (a `{"new_session-rejected", TypeNewSession, false, ErrUnknownType}` row in `compat_test.go` pins the v1 rejection). The interception, capability gate, and `StartNewSession` routing are the consumer ticket's job in `internal/relay` (`security-sensitive` — reuses the `interactive`-capability-is-the-authorization posture `interrupt` established; exempt from the per-device permission gate #702, since starting a fresh session in one's own paired session is a normal paired-phone action, not a tool-permission decision); this leaf declaration makes no trust decision. See [codebase/831.md](codebase/831.md).

**v2 set-session-settings vocabulary** (#844, split from #841; spec `docs/protocol-mobile.md` § Session settings):

| Group | Constants |
|-------|-----------|
| Session settings | `TypeSetSessionSettings`, `TypeSessionSettingsUpdated` |

The two share **one** const block with **one** rationale comment — the same **mixed inbound+outbound** cluster precedent as the modal and queue blocks. `TypeSetSessionSettings` is an inbound phone → binary **control** envelope intercepted at `v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the `TypeModalAnswer` / `TypeNewSession` precedent — **no `dispatch.Route` handler**); `TypeSessionSettingsUpdated` is an outbound binary → phone reply confirming the change, correlated via `Envelope.InReplyTo`. Both carry real named payload structs in the new `settings.go` (`SetSessionSettingsPayload` / `SessionSettingsUpdatedPayload` — see [Session settings payloads](#session-settings-payloads-844)). Both stay out of `inboundAppTypeSet` (two `{"set_session_settings-rejected"/"session_settings_updated-rejected", …, ErrUnknownType}` rows in `compat_test.go` pin the v1 rejection). The producer is handler sibling #845 (shipped — decodes into `sessions.Pool.UpdateSettings` (#840), see [Inbound set_session_settings](v2-session-manager.md#inbound-set_session_settings-845--settingsupdater-seam-validate-persist-reply)); this slice is wire vocabulary only — **not** `security-sensitive` (per the wire-vocab → handler split precedent, this leaf defines shape only, no nonce/token/capability primitive; the YOLO fail-safe lives in `sessions.SettingsUpdate` (#840) and the capability gate in #845). See [codebase/844.md](../codebase/844.md).

**v2 request-session-settings vocabulary** (#491/#1214, `conversation_id` added #1586; spec `docs/protocol-mobile.md` § Session settings):

| Group | Constants |
|-------|-----------|
| Session settings (read) | `TypeRequestSessionSettings`, `TypeSessionSettings` |

Own const block, separate from #844's — the READ half of the same settings
cluster, shipped later. `TypeRequestSessionSettings` is an inbound phone →
binary **control** envelope intercepted at `v2session.go`'s `dispatchAppFrame`
**before** `dispatch.Route` (the `TypeSetSessionSettings` / `TypeRequestSnapshot`
precedent — **no `dispatch.Route` handler**); `TypeSessionSettings` is an
outbound binary → phone reply, correlated via `Envelope.InReplyTo`. Both carry
real named payload structs in `settings.go` (`RequestSessionSettingsPayload` /
`SessionSettingsPayload` — see [Session settings read payloads](#session-settings-read-payloads-4911214-conversationid-field-1586)).
Both stay out of `inboundAppTypeSet` (two `{"request_session_settings-rejected"/…, …, ErrUnknownType}` rows in `compat_test.go` pin the v1 rejection). **#1586 is the second ticket to touch this block**: #491/#1214 shipped it payload-less on the request side; #1586 added `RequestSessionSettingsPayload.ConversationID` and the `KnownConversation` gate, with no change to `TypeSessionSettings`'s reply shape. The handler is documented in [v2-session-manager.md § Inbound request_session_settings](v2-session-manager.md#inbound-request_session_settings-4911214-extended-1586--the-read-half-of-the-844-cluster). See [codebase/1586.md](../codebase/1586.md).

**v2 session-error vocabulary** (#1007, split from #1001; spec `docs/protocol-mobile.md` § Error codes):

| Group | Constant |
|-------|----------|
| Session error | `TypeSessionError` |

`TypeSessionError = "session_error"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionTransition`. Unlike every other outbound v2 event it is **unsolicited and not `InReplyTo`-correlated**: it surfaces a daemon-side give-up (`internal/msgqueue`'s bounded persistent-failure drain, `OnGiveUp` seam, #1000) that has no originating client request to reply to, so the payload (`SessionErrorPayload` in `messaging.go`) carries the conversation identity itself rather than relying on correlation. It also carries a **terminal** code (`CodeSessionBlocked = "session.blocked"`) distinct from the transient `CodeServerBinaryBusy`, and the payload has structurally **no** `Retryable` / `RetryAfterS` fields — a client cannot mistake it for a retry hint the way it might a v1 `TypeError` / `ErrorPayload` push. Stays out of `inboundAppTypeSet` (an old phone must never receive it; `{"session_error-rejected", TypeSessionError, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). **Ships unwired this ticket** — declarations, the partition entry, and a golden fixture only; no producer, emission, or consumer. The producer that emits it on msgqueue give-up is blocked-by-this sibling #1008.

**v2 background-task vocabulary** (#1393; mapping + `docs/protocol-mobile.md` § background_task_started / _updated / _roster wired by #1394):

| Group | Constants |
|-------|-----------|
| Background task | `TypeBackgroundTaskStarted`, `TypeBackgroundTaskUpdated`, `TypeBackgroundTaskRoster` |

The three share **one** const block with **one** rationale comment, in their own block (not folded into the six-member interactive block or the two-member status-peer block above — both those blocks' doc comments hand-count their members, and a new block costs nothing while an edit there risks a wrong recount). All three are outbound binary → phone only, the wire form of `turnevent.BackgroundTask{Started,Updated,Roster}` (#1380/#1381/#1382), and carry the named payload structs `BackgroundTaskStartedPayload` / `BackgroundTaskUpdatedPayload` / `BackgroundTaskRosterPayload` + row type `BackgroundTask` in `interactive.go` (see [Background-task event payloads](#background-task-event-payloads-1393-mapping-wired-1394)). Stay out of `inboundAppTypeSet` (three `{"background_task_*-rejected", …, ErrUnknownType}` rows in `compat_test.go` pin the v1 rejection). **#1393 shipped the declarations, the partition entry, and fixtures unwired; #1394 wired `internal/turnbridge/outbound.go`'s `MapEvent`** — all three arms are additive, conversation-identity-only, not turn-scoped — so these now reach an interactive mobile client. See [codebase/1394.md](../codebase/1394.md).

**v2 thinking-progress vocabulary** (#1386; spec `docs/protocol-mobile.md` § `thinking_progress`):

| Group | Constant |
|-------|-----------|
| Thinking progress | `TypeThinkingProgress` |

`TypeThinkingProgress = "thinking_progress"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionTransition` / `TypeSessionError`. The wire form of `turnevent.ThinkingProgress` (#1385's rate-bounded translation of claude's `system/thinking_tokens` line), carrying the named payload struct `ThinkingProgressPayload` in `interactive.go` (see [Thinking-progress event payload](#thinking-progress-event-payload-1386)). Stays out of `inboundAppTypeSet` (`{"thinking_progress-rejected", TypeThinkingProgress, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). **Vocabulary and mapping shipped in the same ticket** — like #1074's `api_retry`/`compacting`, and unlike the background-task family's #1393/#1394 split: a single two-int frame with no producer dependency gap gave no reason to split it. `internal/turnbridge/outbound.go`'s `MapEvent` arm and `cmd/pyry/interactive_turn_v2.go`'s handler case + `eventKind` arm all landed with the constant, so this reaches an interactive mobile client immediately. See [codebase/1386.md](../codebase/1386.md).

**v2 rate-limited vocabulary** (#1405; mapping is #1410):

| Group | Constant |
|-------|----------|
| Rate limited | `TypeRateLimited` |

`TypeRateLimited = "rate_limited"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionTransition` / `TypeSessionError` / `TypeThinkingProgress`. The wire form of `turnevent.RateLimited` (#1404's bounded translation of claude's top-level `rate_limit_event` line), carrying the named payload struct `RateLimitedPayload` in `interactive.go` (see [Rate-limited event payload](#rate-limited-event-payload-1405-mapping-1410)). Stays out of `inboundAppTypeSet` (`{"rate_limited-rejected", TypeRateLimited, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection — a live security control, since a phone must never be able to send this type into `dispatch.Route`). **Shipped unwired in #1405**, following the background-task family's #1393/#1394 split rather than #1386's ship-together call: declarations, the partition entry, both fixtures, and the `docs/protocol-mobile.md` section landed there; `internal/turnbridge/outbound.go`'s `MapEvent` case landed in #1410. Unlike #1394, #1410 did not touch `docs/protocol-mobile.md` — it is disclaimed there — so the doc write-up could not wait for the mapping ticket the way the background-task family's did.

**v2 announced-model vocabulary** (#1616; mapping landed in #1638):

`TypeModelAnnounced = "model_announced"` is the wire form of `turnevent.ModelAnnounced` (#1600's translation of claude's `system/init` line), carrying `ModelAnnouncedPayload` in `interactive.go`. **Shipped unwired in #1616**, following the background-task/rate-limited split rather than #1386's ship-together call; `internal/turnbridge/outbound.go`'s `MapEvent` case landed in sibling #1638, along with `cmd/pyry/interactive_turn_v2.go`'s handler case, so this frame now reaches an interactive v2 mobile client.

Its wire field keeps the name `model` even though three other v2 payloads already carry one (`ScreenSnapshotPayload`, `SessionSettingsPayload`, `SetSessionSettingsPayload` — all three mean the per-session *override*; this one means what claude *announced*). Renaming the wire key to `announced_model` was considered and **rejected**: every field on this wire is scoped by its envelope type, so the collision is unambiguous to a decoder; and a rename would do nothing for the client author who never reads the `model_announced` row at all — only the doc cross-reference (`docs/protocol-mobile.md` § `screen_snapshot` / § `session_settings` / § `set_session_settings`, each now pointing at § `model_announced`) reaches them. The residual risk — a client that flat-merges frames into one session-state object and collapses the two `model` keys — is **hypothesized, not observed**, and loud rather than silent if it happens (`""` against a concrete identifier). Revisiting the wire-key choice was cheap while the type had no producer, and is expensive now that #1638 has shipped one and a client can be broken by a rename.

**v2 model-list vocabulary** (#1704; producer #1848/#1849, fixtures #1705):

| Group | Constant |
|-------|----------|
| Model list | `TypeModelList` |

`TypeModelList = "model_list"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionError` / `TypeThinkingProgress` / `TypeRateLimited`. The wire form of claude's `initialize` model inventory, carrying the named payload struct `ModelListPayload` + row type `ModelOption` in `interactive.go` (see [Model-list payload](#model-list-payload-1704-shape-1705-fixtures--docs-producer-18481849)). Stays out of `inboundAppTypeSet` (`{"model_list-rejected", TypeModelList, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). **No inbound request verb is declared alongside it** — `TestEveryInboundV2TypeHasHandler`'s Assertion #1 requires an inbound type to be wired into a real dispatch surface, and none has shipped for `model_list`; if a future ticket picks request/reply it declares the verb *with* its handler and this constant's `excludedTypes` classification moves from `"push"` to `"reply"`. Shipped and wired: `internal/turnbridge`'s `MapEvent` gained a mapping arm in #1848, `interactiveTurnEmitterV2.Handle` gained the calling arm in #1849, and #1845 proves the frame reaches a connected client end to end — #1705 closed the fixtures/docs gap first. See [Model-list payload](#model-list-payload-1704-shape-1705-fixtures--docs-producer-18481849) above.

**v2 slash-command-list vocabulary** (#1726; payload #1727, producer #1720, fixtures/docs #1718):

| Group | Constant |
|-------|----------|
| Slash command list | `TypeSlashCommandList` |

`TypeSlashCommandList = "slash_command_list"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionError` / `TypeThinkingProgress` / `TypeRateLimited` / `TypeModelList`. It shares `TypeModelList`'s source (the `initialize` control reply, whose `commands` array sits alongside `models`) but not its group: this is a capability inventory of *verbs* the operator may ask the session to do, where `model_list` inventories *identities*. Declared for the Actions-menu grey-out ([pyrycode-desktop#681](https://github.com/pyrycode/pyrycode-desktop/issues/681)) and a slash-command type-ahead ([pyrycode-desktop#694](https://github.com/pyrycode/pyrycode-desktop/issues/694)) — knowledge-capture is workspace-specific, and a menu that always offers it produces an "Unknown command" reply in most repositories. Stays out of `inboundAppTypeSet` (`{"slash_command_list-rejected", TypeSlashCommandList, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). **No inbound request verb is declared alongside it**, `TypeModelList`'s reasoning unchanged: `TestEveryInboundV2TypeHasHandler`'s Assertion #1 forces a handler on any inbound type this ticket does not ship, so this constant's `excludedTypes` classification is `"push"` and would move to `"reply"` only if #1720 picks request/reply. The payload struct landed in #1727 (see [Slash-command-list payload](#slash-command-list-payload-1727-shape-1718-fixtures--docs-producer-1720) above): `SlashCommandListPayload` / `SlashCommand` in `interactive.go`. No producer, no emit, no handler — #1720/#1718 own the rest.

The source is measured, not assumed: the control reply's `commands` carries 51 entries and 11 alias strings across 9 of them; the names-only `slash_commands` twin on the same `system`/`init` line carries the identical 51 names but **none** of the aliases, so a daemon forwarding the twin would omit `reset` (the desktop Actions menu's own entry, an alias of `clear`) from the grey-out list entirely — `commands` is the source because it is the only array carrying what the consumers need, not because it is the only candidate.

**The naming pin's checkable words invert the intuitive shorter-is-safer rule.** claude carries four words on this path (`initialize`, `commands`, `slash_commands`, `terminal_slash_commands`) against `TypeModelList`'s two, and `commands ⊂ slash_commands ⊂ terminal_slash_commands` by substring, so one `Contains(…, "commands")` check subsumes both longer plurals. But the *singular* subject nouns — `command`, `slash_command`, `slash` — are each a substring of the correct name `slash_command_list`, so a `Contains` check on any of them is **red against the correct name**; the checkable words are the plurals, not the singulars a reader reaches for first. This is the same trap `TypeModelAnnounced`'s and `TypeModelList`'s blocks record for their own subject noun — the trap word is singular `model`, never plural `models`, which is a live check in `TestModelListType_IsNotClaudesVocabulary` — and it is worth stating precisely here because the shipped `codes.go`/`interactive_test.go` doc comments for this ticket currently misattribute the trap as `models` (SHOULD FIX from code review on #1735, uncorrected as of #1726 landing): don't repeat that citation when #1727 writes its own naming-pin block against the same paragraph.

`TypeRekeyRequest` carries the doc-comment load-bearing instruction "MUST NOT be added to `inboundAppTypeSet` in `internal/protocol/envelope.go`"; a companion doc-comment **above** `inboundAppTypeSet` names `TypeRekeyRequest` as the canonical example of a v2-only type that must stay out; the interactive block carries the same MUST-NOT instruction. The advisory comments form the stochastic-rule rails; the deterministic rail is `TestTypeConstants_V1V2Partition` in `compat_test.go` (see drift detectors below).

**v2 attachment-chunk vocabulary** (#1752; cap + both-direction fixtures #1753, published contract #1751, reassembly #1741, storage #1743, dispatch #1744, retrieval #1746):

```go
type AttachmentChunkPayload struct {
    AttachmentID string `json:"attachment_id"`
    Index        int    `json:"index"`
    TotalChunks  int    `json:"total_chunks"`
    Filename     string `json:"filename"`
    MimeType     string `json:"mime_type"`
    Size         int64  `json:"size"`
    SHA256       string `json:"sha256"`
    Data         []byte `json:"data"`
}
```

`TypeAttachmentChunk = "attachment_chunk"` is the first `Type*` constant in this package that is genuinely **bidirectional**: upload (client → daemon) and retrieval (daemon → client) ride the same frame, the settled anti-drift decision — one shape, not two built months apart against each other. `AttachmentChunkPayload` lives in the new `attachments.go` (the package's one-file-per-concern convention; `attachments_test.go` — cap constant and both-direction fixtures — is sibling #1753). No completion peer of `DebugBundleDonePayload`: `TotalChunks` rides every chunk, so a receiver knows the expected count from the first one, earlier than the bundle stream (whose chunks carry only `Seq` and need a terminal frame to learn a count at all).

- **The `SECURITY:` doc block is the deliverable, not a courtesy** — six blocked slices code against it because a single Go type here carries client-authored data inbound and daemon-authored data outbound and cannot itself say which. `Filename`/`Size`/`SHA256` are the claims a reader expects; `AttachmentID`/`Index`/`TotalChunks` are the ones whose falsification costs something (accumulator sizing, buffer placement, and the upload's storage key respectively) and had to be named explicitly rather than left implied.
- **Never allocate from a claim.** `TotalChunks` and `Size` are attacker-chosen integers — `make([][]byte, TotalChunks)` on a claimed 2³¹−1 is a multi-gigabyte allocation from one ~60 KB frame — so a receiver range-checks both before sizing anything. The doc block states that check as a **strict equality**, `TotalChunks == max(1, ceil(Size / bound))`, raw-over-raw against `MaxAttachmentChunkBytes` (§ below): only the equality bounds `TotalChunks` from above, since a maximum chunk size gives a *lower* bound on the honest chunk count and `TotalChunks >= ceil(Size/bound)` caps nothing; the `max(1, …)` is what resolves `Size == 0`, where the bare `ceil` gives 0 against the documented `TotalChunks >= 1`. The zero-byte question resolved to [`attachments-package.md`](attachments-package.md)'s #1769 — one chunk carrying zero bytes is complete and yields zero bytes, not an error and not permanently incomplete. **The equality form itself is now enforced, by `attachments.CheckDeclaration` (#1776, see [`attachments-package.md`](attachments-package.md) § "Admission layer")** — the price is accepted: a client whose own stride yields a conforming count is fine, but one that declares a count its stride can't produce is refused, which is not a regression given every non-final chunk is already obliged to carry exactly `bound` raw bytes.
- **`AttachmentID` is validated for canonical shape before it becomes a path component** (`conversations.ValidID` is the precedent named) and is explicitly **not a capability** — not secret, not unguessable, never the sole gate standing between a caller and a file. **`SHA256` is integrity, not authenticity**: the same party supplies bytes and digest, so a match proves the transfer wasn't corrupted and nothing about content safety; it must never become a content-addressed access token.
- **`Index`/`TotalChunks`, not `Seq`/`Total`.** `DebugBundleChunkPayload.Seq` carries a strict-succession contract the bundle receiver enforces (next `Seq` must equal the count already seen); this frame's receiver addresses by index and rejects duplicates/out-of-range instead of requiring succession, so reusing the name `Seq` would have imported a contract that isn't true here. `TotalChunks` rather than `Total` because a bare "total" beside a "size" on the same struct reads as a byte count.
- **`Data` never aliases a reused connection read buffer** — `encoding/json` allocates a fresh slice decoding a base64 field, so #1741 may retain `Data` without copying. A non-obvious contract worth stating once here rather than each consumer rediscovering it (surfaced by code review, not in the shipped doc block itself).

`excludedTypes["TypeAttachmentChunk"] = "pending handler (#1744)"` in `cmd/pyry/relay_guard_test.go` is the first entry whose label is neither `"push"` nor `TypeHello`'s `"handshake"` — proof the label column really is free text (Assertion #3 checks membership, never the value), not a closed enum a new entry must fit. The `"push"` neighbours all justify themselves with *this slice declares no inbound request verb*, which is **false** for `TypeAttachmentChunk` — the upload leg is inbound — so copying that reason would have shipped a false statement past a green `make check`. Filing it in `inboundTypes` instead fails Assertion #1 (no `Handlers`/`dispatchAppFrame` entry ships in this slice). It sits in `excludedTypes` under its own reason — the inbound leg has no handler yet — with a written handoff: #1744 moves the entry to `inboundTypes` as `"switch-intercepted"` when it lands the dispatch case. Any future bidirectional or not-yet-wired type should follow this entry or `TypeHello`'s, never the `"push"` block.

**The cap + both-direction fixtures (#1753).** `attachments.go`'s new `const` block publishes `MaxAttachmentChunkBytes = 45000` — raw, pre-base64 bytes of `Data`, the first exported byte bound in this package — plus byte ceilings for the three metadata strings the doc block above leaves unbounded (`MaxAttachmentIDBytes = 64`, `MaxAttachmentFilenameBytes = 255` at POSIX `NAME_MAX`, `MaxAttachmentMimeTypeBytes = 255` at RFC 6838 §4.2). All four are declared contracts with no validator in this package, same posture as the payload type; #1741 enforces inbound, #1744/#1746 outbound.

- **Why this constant is exported where `maxV2AppEnvelope` (test-local, `interactive_test.go`) is not: enforcement location, not importance.** `maxV2AppEnvelope` measures a cap the *transport* enforces, so exporting it would claim an enforcement this package doesn't perform. `MaxAttachmentChunkBytes` is a producer-side contract every client must obey to chunk a file at all — readable from outside the package is the whole point, even though nothing here checks it either. The distinction to carry forward: exportedness tracks who must read the number, not whether this package verifies it.
- **A length ceiling is not a safety property, and `MaxAttachmentIDBytes` is the concrete case.** 64 bytes sits above `conversations.ValidID`'s 36-char UUIDv4 form so #1741/#1743 can choose the canonical shape without this constant constraining them — but 64 bytes also accommodates `../../../../etc/passwd` several times over, so it does nothing about the traversal hazard the canonical-shape check (not this bound) is responsible for. Worth re-stating whenever a byte-length constant sits next to a field the doc block also calls a path component: the two obligations don't imply each other.
- **Tracing a worst-case test fill to a *declared* bound, not a number invented in the test.** `TestAttachmentChunkPayload_FitV2EnvelopeCap` fills every unbounded field to its published constant, but two fills go beyond what the field's own contract allows and both are declared as such in the test's header rather than silently: `SHA256` is filled with 64 `<` rather than 64 hex characters (hex never escapes, so the honest worst case is 64 B, not the 384 B a hostile fill costs — the extra 320 B buys a proof that holds for a length-respecting non-hex value, which is what an inbound attacker actually sends), and `Index`/`TotalChunks`/`Size` are filled at `math.MinInt64` even though the doc block says `Index >= 0` etc — because nothing enforces that yet, and the *declared Go type* is the bound this fill traces to. A test that fills past a field's stated contract needs to say why in its own header, not leave a reader to wonder whether the fill is a mistake.
- **A byte guard's own source literal can silently lose the escape it exists to prove, and the round trip won't catch it.** The upload fixture's `Filename` (a `report ` + less-than-sign + `draft` + greater-than-sign + `.pdf`-shaped string) is the one place the committed bytes demonstrate `encoding/json`'s `SetEscapeHTML` default: the less-than sign becomes the six-byte sequence backslash-u-0-0-3-c on the wire. Writing that field's raw less-than-sign literal through an editing tool silently substituted the six-byte escaped form into three source-code spots meant to hold the raw character (a struct-literal want, a byte guard, and a comment), and every test still passed, because each assertion was internally consistent with whichever form it actually held — no fixture in the diff was wrong, only the test's *source* literals were. `roundTripEnvelope` proves the fixture round-trips; it proves nothing about whether the test's own Go-source literal is the character the test claims to be asserting against. After writing a character that JSON escapes into a Go source file, grep the file for the six-byte escape sequence to confirm none leaked into a spot meant to hold the raw byte, rather than trusting a green test run.
- **A sole-redness claim about an `,omitempty` mutant depends on which regeneration state you measured, and the two states can disagree.** As committed, the `index` field's `,omitempty` mutant reddens both `TestAttachmentChunkPayload_Upload_RoundTrip` (its fixture's `index` happens to be `0`, the field's own zero value) and `TestAttachmentChunkPayload_ZeroValue_RoundTrip`'s byte guard. Regenerate both fixtures under that same mutant and the upload round trip goes **green** — the mutant elides `index` from the regenerated upload JSON too, and decoding an absent `index` back to `0` still round-trips clean — while the zero-value byte guard, checking for the literal `"index":0`, stays red because that key is elided from the regenerated zero fixture as well. The upload round trip's coverage of `index` was real only pre-regeneration; only the byte guard survives both states. A "this test is the only one that catches mutant X" comment has to name *which* state — as-committed or post-regeneration — it was measured against, extending the sole-redness discipline [Slash-command-list payload](#slash-command-list-payload-1727-shape-1718-fixtures--docs-producer-1720) already records: re-run rather than predict, and now re-run in both states.

**The published client contract (#1751): `docs/protocol-mobile.md` § Attachments.** #1752/#1753 landed the Go above with nothing published for the mobile/desktop clients that code against the spec, not the Go — the doc actively contradicted the landed code in two places (§ Application-envelope size cap claimed chunking was "out of scope"; § Scope listed attachments as out of scope outright). #1751 published the section — the eight-field table, the sender's chunking arithmetic (`total_chunks = max(1, ceil(size / 45000))`), the receiver's index-addressed reassembly (deliberately weaker than `debug_bundle_chunk`'s strict `seq` succession — the neighbouring rule a reader would otherwise copy), retrieval's two terminal signals, and the trust/content-hygiene obligations — corrected both stale claims, and added the seven `attachment.*` codes documented above. Two lessons from that ticket generalize past it:

- **A `**bold**` paragraph lead is not a heading, and a markdown link to it renders as a dead anchor with nothing in this repo's build checking it.** `make cite-guard` only scans `//`-comment citations in Go source; a `[text](#some-bold-lead)` link inside `docs/protocol-mobile.md` itself is invisible to CI. Before linking to a spot in this document that isn't a `###`/`####` heading, check it actually has one — the § Application-envelope size cap paragraph this ticket linked to is a bold lead inside § Wire shapes, not its own heading.
- **A code-review SHOULD FIX against prose (not code or tests) can get deferred to "the documentation pass" instead of a rework lap, and if so it has to actually land here, not just get noted.** #1751's review flagged the § Application message types row's claim "the table's only bidirectional entry" as false — three rows (`ack`, `error`, `rekey_request`) are already `either` — and marked it non-blocking, explicitly for correction at the documentation stage. Verify a deferred-to-docs finding against the live file rather than assuming a PASS verdict means every finding landed.
