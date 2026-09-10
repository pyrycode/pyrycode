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

`TypeApiRetry = "api_retry"` and `TypeCompacting = "compacting"` share their own adjacent const block (not merged into the interactive block above) so the doc comment can name them explicitly as PTY-derived status peers of `TypeStall`, not turn-lifecycle events. Both are outbound binary → phone only, carry the named payload structs `ApiRetryPayload` / `CompactingPayload` (`interactive.go` — see [Interactive event payloads](#interactive-event-payloads-607-638-1074-2237-2233)), and stay out of `inboundAppTypeSet` (two `{"api_retry-rejected"/"compacting-rejected", …, ErrUnknownType}` rows in `compat_test.go` pin the v1 rejection). Unlike `stall`, both carry an explicit `active: false` falling edge — see the payload doc below. The consumer (`cmd/pyry/interactive_turn_v2.go`'s `Handle`) is `security-sensitive`: it forwards a screen-derived attempt counter across the tui-driver substrate seal. See [codebase/1074.md](../codebase/1074.md).

**v2 tool-denial vocabulary** (#2233; spec `docs/protocol-mobile.md` § `tool_denied`):

| Group | Constant |
|-------|----------|
| Tool denied | `TypeToolDenied` |

`TypeToolDenied = "tool_denied"` is an outbound binary → phone event in its **own** new
const block — the same single-type-own-block precedent as `TypeCompactionBoundary` /
`TypeRateLimited` / `TypeModelList`. The wire form of `turnevent.ToolCallDenied` (#2232's
translation of claude's `system/permission_denied` line), carrying the named payload
struct `ToolDeniedPayload` in `interactive.go` (see [Interactive event payloads](#interactive-event-payloads-607-638-1074-2237-2233)).
**Turn-scoped, unlike its `compaction_boundary`/`rate_limited` single-block siblings** —
it carries a `turn_id` because it joins the `tool_use`/`tool_result` frames for the same
call on a byte-identical `tool_use_id`, the reason it is a frame of its own rather than
two fields on `tool_result` (the denial line arrives before the result, and #2234's
result-line recovery reports denials whose `tool_result` has already shipped). Stays out
of `inboundAppTypeSet` (`{"tool_denied-rejected", TypeToolDenied, false, ErrUnknownType}`
in `compat_test.go` pins the v1 rejection — the structural guarantee that no inbound
re-authorize path exists for a blocked call). Shipped wired end to end in one ticket, the
`#1386`/`#2237` ship-together call rather than the background-task/rate-limited split: no
producer dependency gap, since #2232 already landed the parser half, so
`internal/turnbridge`'s `MapEvent` arm and `cmd/pyry/interactive_turn_v2.go`'s `Handle` +
`eventKind` arms all landed with the constant. **The report-slice rename lesson** — the
daemon's `tool_call_id` token becomes this frame's `tool_use_id` key at the bridge, the
first `MapEvent` arm to rewrite slice *contents* rather than pass them through — is
recorded in [turnbridge-package.md](turnbridge-package.md); the payload's own shape and a
doc-comment ordering trap are recorded in
[Interactive event payloads](#interactive-event-payloads-607-638-1074-2237-2233).

**v2 compaction-boundary vocabulary** (#2237; spec `docs/protocol-mobile.md` § `compaction_boundary`):

| Group | Constant |
|-------|----------|
| Compaction boundary | `TypeCompactionBoundary` |

`TypeCompactionBoundary = "compaction_boundary"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionError` / `TypeThinkingProgress` / `TypeRateLimited` / `TypeModelList` / `TypeSlashCommandList` — **not** folded into the `api_retry`/`compacting` status-peer block one paragraph up, even though claude's `compact_boundary` line is a sibling of the `system/status` line `compacting` maps. The reason is ordering, not taste: claude states the boundary's trigger and token counts on a line that arrives *after* `compacting`'s falling edge has already shipped (the committed capture's turn order), so the two frames cannot share a payload and do not share a const block either. The wire form of `turnevent.CompactionBoundary` (`internal/streamsup`'s `emitCompactionBoundary`), carrying the named payload struct `CompactionBoundaryPayload` in `interactive.go` (see [Interactive event payloads](#interactive-event-payloads-607-638-1074-2237-2233)). Stays out of `inboundAppTypeSet` (`{"compaction_boundary-rejected", TypeCompactionBoundary, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). Shipped wired end to end in one ticket, `#1386`'s ship-together call rather than the background-task/rate-limited split: no producer dependency gap, so `internal/turnbridge`'s `MapEvent` arm and `cmd/pyry/interactive_turn_v2.go`'s `Handle` + `eventKind` arms all landed with the constant. See [turnevent-package.md](turnevent-package.md) and [streamsup-package-system-maps-per-subtype-since-2026-08-07.md](streamsup-package-system-maps-per-subtype-since-2026-08-07.md).

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

**v2 session-facts vocabulary** (#2253; producer #2254):

`TypeSessionFacts = "session_facts"` is the wire form of `turnevent.SessionFacts` (#2252's report of claude's own build version and the permission posture claude says the child is running under, from the same `system/init` line `model_announced`'s value comes from), carrying `SessionFactsPayload` in `interactive.go`. **Shipped unwired in #2253**, the same split as `model_announced`/`model_list` rather than #1386's ship-together call — but with the halves in the opposite order from that pair: `cmd/pyry`'s emitting `Handle` case landed early, with the producer in #2252, and only `internal/turnbridge`'s `MapEvent` arm was left outstanding when #2253 froze the wire type. #2254 landed that arm, so this frame now reaches an interactive v2 mobile client. The two sections cross-link in `docs/protocol-mobile.md`.

**The naming pin has no fallback containment word, unlike every sibling above — this is the one where the substring trap closes completely.** `session`, the obvious subject noun, is a substring of the correct name `session_facts` itself, so `Contains(…, "session")` is red against the correct value — the same trap `TypeModelAnnounced`'s block records for singular `model` and `TypeSlashCommandList`'s for `command`/`slash_command`/`slash`, but here there is no shorter safe word left to fall back to. `session` is also claude's own word, via the `session_id` key on the same init line. The checkable negatives are claude's *subtype* (`init`) and claude's two *keys* (`claude_code_version`, `permissionMode`), none a substring of `session_facts`. A second pin this family alone needs: three shipped constants already begin `session_` (`TypeSessionTransition`, `TypeSessionSettings`, `TypeSessionError`), so separating from them has to be an **equality** against each sibling constant, the `TypeQuestionDismissed`/`TypeModalDismissed` shape above, not a containment on the shared prefix.

**A "declared exactly once" attribution claim is itself prone to multiplying, even inside a single PR.** The payload's doc comment asserted its own "nothing emits it yet" sentence was the *one* place that claim was made in the tree — the precaution #1616's twelve scattered copies (#1639 had to fix) exists to avoid — but code review found the same claim independently restated in `codes.go`'s block, the `docs/protocol-mobile.md` section's attribution sentence, and the application-message-type table row. None of the four was wrong, but the "one place" self-description was, and a later editor trusting it would under-edit the way #1639 had to correct. Grep for the claim's substance across all four sites (Go doc comment, `codes.go` block, doc section, table row) rather than trusting one sentence's count of itself.

**v2 model-list vocabulary** (#1704; producer #1848/#1849, fixtures #1705):

| Group | Constant |
|-------|----------|
| Model list | `TypeModelList` |

`TypeModelList = "model_list"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionError` / `TypeThinkingProgress` / `TypeRateLimited`. The wire form of claude's `initialize` model inventory, carrying the named payload struct `ModelListPayload` + row type `ModelOption` in `interactive.go` (see [Model-list payload](#model-list-payload-1704-shape-1705-fixtures--docs-producer-18481849)). Stays out of `inboundAppTypeSet` (`{"model_list-rejected", TypeModelList, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). **#2125 later declared the inbound request verb this paragraph once said was missing** (`TypeRequestModelList`, § below) — and `TypeModelList`'s own `excludedTypes` classification did **not** move from `"push"` to `"reply"` the way this paragraph predicted: `TypeModelList` stays a push-classified outbound type, because it is still the live turn lane's and the connect-time reconcile's unsolicited frame too, and #2125's reply reuses it unchanged rather than minting a `TypeRequestModelList`-only response — the `TypeHistoryPage` precedent this paragraph expected does not apply here since that type has no unsolicited producer of its own. Shipped and wired: `internal/turnbridge`'s `MapEvent` gained a mapping arm in #1848, `interactiveTurnEmitterV2.Handle` gained the calling arm in #1849, and #1845 proves the frame reaches a connected client end to end — #1705 closed the fixtures/docs gap first. See [Model-list payload](#model-list-payload-1704-shape-1705-fixtures--docs-producer-18481849) above.

**v2 request-model-list vocabulary** (#2125, split from #2084; consumed same ticket):

| Group | Constant |
|-------|----------|
| Request model list | `TypeRequestModelList` |

`TypeRequestModelList = "request_model_list"` is an inbound phone → binary **control** envelope in its **own** new const block, intercepted at `v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the `TypeRequestHistory` / `TypeRequestSessionSettings` precedent — **no `dispatch.Route` handler**). It joins the five inbound *ask the daemon for X* verbs already on this wire rather than inventing a sixth idiom for the same act. Carries the one-field `RequestModelListPayload` (`interactive.go` — see [Model-list payload](#model-list-payload-1704-shape-1705-fixtures--docs-producer-18481849)); the reply is `TypeModelList` / `ModelListPayload` **unchanged**, correlated via `Envelope.InReplyTo` (no request-id key, the `TypeAttachmentStored` decision transferred), carrying no `EventID`. Stays out of `inboundAppTypeSet` (a v2-only control envelope; `IsKnownAppType` rejecting it is the structural bar against a v1 client pushing one into the v1 handler chain). **Never sits in `excludedTypes` as "pending handler"**: unlike `TypeRequestHistory` (#2113→#2116) and `TypeRequestAttachment` (#2052→#2054), the declaration's only daemon-side consumer is the handler landing in this same ticket, so it is filed in `inboundTypes` as `"switch-intercepted"` from the moment it exists. See [Inbound `request_model_list`](v2-session-manager-state-machine-inbound-request-model-list-modellistfor-seam.md) for the handler and [Error codes](protocol-package-constants-codes-go-error-codes-21.md) for the retryable `model_list.unavailable` it mints.

**v2 conversation-system-prompt-read vocabulary** (#2152, the read half of the #2151 cluster):

| Group | Constant |
|-------|----------|
| Request system prompt | `TypeRequestSystemPrompt` |
| System prompt | `TypeSystemPrompt` |

`TypeRequestSystemPrompt = "request_system_prompt"` is an inbound phone → binary
**control** envelope, intercepted at `v2session.go`'s `dispatchAppFrame` **before**
`dispatch.Route` — the `TypeRequestModelList` / `TypeRequestSessionSettings`
precedent, no `dispatch.Route` handler. Carries the one-field
`RequestSystemPromptPayload` (`system_prompt.go` — see
[Conversation system-prompt read payloads](protocol-package-types-system-prompt-payloads.md)).
`TypeSystemPrompt = "system_prompt"` is the reply, correlated via
`Envelope.InReplyTo` (no request-id key, no `conversation_id` either — the
`TypeAttachmentStored` decision transferred), carrying no `EventID`. Both stay
out of `inboundAppTypeSet` (v2-only control/reply). Neither ever sits in
`excludedTypes` as "pending handler": the declaration's only daemon-side
consumer is the handler landing in this same ticket, so `TypeRequestSystemPrompt`
is filed in `inboundTypes` as `"switch-intercepted"` and `TypeSystemPrompt` in
`excludedTypes` as `"reply"` from the moment both exist — `TypeRequestModelList`'s
precedent, not `TypeRequestHistory`'s pending-handler window. **Its write-half
sibling, `TypeSetSystemPrompt`, is `dispatch.Route`-dispatched** (filed
`"map-dispatched"`) rather than switch-intercepted, and the split is deliberate:
the write verb carries no capability gate, and this read verb is inert without
the interactive capability. See
[Inbound `request_system_prompt`](v2-session-manager-state-machine-inbound-request-system-prompt-systempromptfor-seam.md)
for the handler; mints no error code and has no reject path.

**v2 slash-command-list vocabulary** (#1726; payload #1727, producer #1720, fixtures/docs #1718):

| Group | Constant |
|-------|----------|
| Slash command list | `TypeSlashCommandList` |

`TypeSlashCommandList = "slash_command_list"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionError` / `TypeThinkingProgress` / `TypeRateLimited` / `TypeModelList`. It shares `TypeModelList`'s source (the `initialize` control reply, whose `commands` array sits alongside `models`) but not its group: this is a capability inventory of *verbs* the operator may ask the session to do, where `model_list` inventories *identities*. Declared for the Actions-menu grey-out ([pyrycode-desktop#681](https://github.com/pyrycode/pyrycode-desktop/issues/681)) and a slash-command type-ahead ([pyrycode-desktop#694](https://github.com/pyrycode/pyrycode-desktop/issues/694)) — knowledge-capture is workspace-specific, and a menu that always offers it produces an "Unknown command" reply in most repositories. Stays out of `inboundAppTypeSet` (`{"slash_command_list-rejected", TypeSlashCommandList, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). **No inbound request verb is declared alongside it**, `TypeModelList`'s reasoning unchanged: `TestEveryInboundV2TypeHasHandler`'s Assertion #1 forces a handler on any inbound type this ticket does not ship, so this constant's `excludedTypes` classification is `"push"` and would move to `"reply"` only if #1720 picks request/reply. The payload struct landed in #1727 (see [Slash-command-list payload](#slash-command-list-payload-1727-shape-1718-fixtures--docs-producer-1720) above): `SlashCommandListPayload` / `SlashCommand` in `interactive.go`. No producer, no emit, no handler — #1720/#1718 own the rest.

The source is measured, not assumed: the control reply's `commands` carries 51 entries and 11 alias strings across 9 of them; the names-only `slash_commands` twin on the same `system`/`init` line carries the identical 51 names but **none** of the aliases, so a daemon forwarding the twin would omit `reset` (the desktop Actions menu's own entry, an alias of `clear`) from the grey-out list entirely — `commands` is the source because it is the only array carrying what the consumers need, not because it is the only candidate.

**The naming pin's checkable words invert the intuitive shorter-is-safer rule.** claude carries four words on this path (`initialize`, `commands`, `slash_commands`, `terminal_slash_commands`) against `TypeModelList`'s two, and `commands ⊂ slash_commands ⊂ terminal_slash_commands` by substring, so one `Contains(…, "commands")` check subsumes both longer plurals. But the *singular* subject nouns — `command`, `slash_command`, `slash` — are each a substring of the correct name `slash_command_list`, so a `Contains` check on any of them is **red against the correct name**; the checkable words are the plurals, not the singulars a reader reaches for first. This is the same trap `TypeModelAnnounced`'s and `TypeModelList`'s blocks record for their own subject noun — the trap word is singular `model`, never plural `models`, which is a live check in `TestModelListType_IsNotClaudesVocabulary` — and it is worth stating precisely here because the shipped `codes.go`/`interactive_test.go` doc comments for this ticket currently misattribute the trap as `models` (SHOULD FIX from code review on #1735, uncorrected as of #1726 landing): don't repeat that citation when #1727 writes its own naming-pin block against the same paragraph.

`TypeRekeyRequest` carries the doc-comment load-bearing instruction "MUST NOT be added to `inboundAppTypeSet` in `internal/protocol/envelope.go`"; a companion doc-comment **above** `inboundAppTypeSet` names `TypeRekeyRequest` as the canonical example of a v2-only type that must stay out; the interactive block carries the same MUST-NOT instruction. The advisory comments form the stochastic-rule rails; the deterministic rail is `TestTypeConstants_V1V2Partition` in `compat_test.go` (see drift detectors below).

**v2 attachment vocabulary family** (`attachment_chunk` #1752, `attachment_stored` #1895, `request_attachment` #2052) — split into its own document, [Attachment envelope types](protocol-package-constants-codes-go-envelope-types-attachments.md), once this document crossed the 50000-byte search-chunking cap. Covers the bidirectional chunk frame and its cap/fixture lessons, the upload success reply, and the retrieval request verb closing the hole this section named out loud.

**v2 question-batch vocabulary** (#1962, split from #1926; payload #1963, parse #1965, dismissal #1974, producer #1973):

| Group | Constants |
|-------|----------|
| Question batch | `TypeQuestionShown`, `TypeQuestionDismissed`, `TypeQuestionAnswer` (#1983), `TypeQuestionRefused` (#1983) |

`TypeQuestionShown = "question_shown"` is an outbound binary → phone event in its **own** new const block — the same single-type-own-block precedent as `TypeResync` / `TypeSessionError` / `TypeThinkingProgress` / `TypeRateLimited` / `TypeModelList` / `TypeSlashCommandList`. claude's clarifying-question tool (`AskUserQuestion`) rides the same approval bridge as a permission prompt — `handleApprove` parks it in `internal/permbridge` keyed by `tool_use_id`, `streamApprovalBridge.Surface` raises it — but is declared as a **new frame family rather than a grown `ModalShownPayload`**, and the deciding argument is security rather than taste: `denyByClass` in `internal/modalbridge/modal.go` makes `ModalShownPayload.DefaultOptionID` the DENY option, and `default_option_id` MUST equal one of `options[].id` is a **total** invariant on the permission surface today. A clarifying question has no deny option and no safe default, so growing the modal payload would demote that invariant to class-conditional — every asserting site would need an exemption on exactly the field whose purpose is fail-safe. Stays out of `inboundAppTypeSet` (`{"question_shown-rejected", TypeQuestionShown, false, ErrUnknownType}` in `compat_test.go` pins the v1 rejection). `TypeQuestionDismissed = "question_dismissed"` joins the same const block: **its own type rather than a reused `TypeModalDismissed`**, settling the deferral this block itself used to carry — `ModalDismissedPayload` identifies what it clears by `modal_id`, so a `question_batch_id` arriving there would clear the wrong panel or none. Nothing constructs or emits either frame in this slice: #1963 is the batch payload, #1974 the dismissal payload (see [Question-batch payload](protocol-package-question-batch-payload.md)), #1965 the batch parse, #1975 the nonce mint, and #1973 the producer of both frames.

**The inbound leg joined in #1983**, its own const block beside this one: `TypeQuestionAnswer = "question_answer"` / `TypeQuestionRefused = "question_refused"`, two types rather than one nullable flag, mirroring `TypeModalAnswer` / `TypeModalCancel`. Between #1983 and #1984 neither went in `inboundAppTypeSet` (same v2-control interception argument as the pair above) nor in `TestEveryInboundV2TypeHasHandler`'s `inboundTypes` — the counterintuitive part: #1983 declared the vocabulary with no `dispatchAppFrame` case, and `inboundTypes` requires one (Assertion #1), so filing a handler-less *inbound* type there would have failed by construction, the same way filing it under `excludedTypes["push"]` would be false for a type that genuinely is inbound. Both constants sat in `excludedTypes` as `"pending handler (#1984)"` in the interim, the second confirmed case of the `TypeAttachmentChunk` (#1744, split into #1896/#1897) pattern above: the classification key is **whether a handler exists**, not which direction the frame travels. **#1984 gave `dispatchAppFrame` a case for each**, so both constants now sit in `inboundTypes` as `"switch-intercepted"`, beside `TypeModalAnswer` / `TypeModalCancel` — leaving them in `excludedTypes` would trip Assertion #2 and redden `make check`. See [Drift detectors](protocol-package-drift-detectors.md) and [Inbound question control](v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md) for the handler. Payload shape, the positional-index design and the testing lessons: [Question-batch payload § `QuestionAnswerPayload` / `QuestionRefusedPayload`](protocol-package-question-batch-payload.md#questionanswerpayload--questionrefusedpayload-1983). #1907, this pair's parent ticket, split into #1983 (this vocabulary), #1984 (routing) and #1985 (resolution against the daemon's parked batch) — #1985 itself later split into #1990 (refusal, landed) and #1991 (answer).

**The naming pin surfaced a general trap beyond the singular/plural one the model-list and slash-command-list siblings already record.** `TestQuestionShownType_IsNotClaudesVocabulary` keeps both an equality check (`== "AskUserQuestion"`) and a `Contains(…, "ask")` check, and a mutant run confirmed the equality is **not** redundant with the containment the way the earlier siblings' equality/containment pair is (there, anything equal to `initialize` also contains `init`). `strings.Contains` is case-sensitive, so the equality check alone is green against `ask_user_question_shown` — the most plausible wrong name a reader reaches for — and only the `ask` containment check catches it. Predicting which check is load-bearing from a sibling's shape gets this backwards; verify with a mutant run rather than assume redundancy carries over. The `ask` check is itself safe only by accident of the current vocabulary — `ask` is a substring of `task`, so a future frame in this family named with a task word would make it red by construction, the same trap as the singular subject noun one step removed — a rename has to re-check it, not assume it stays inert.

**The dismissal's naming pin (#1974) adds an equality-not-containment trap of its own.** `dismissed` is a substring of both `question_dismissed` and `modal_dismissed`, so `Contains(…, "dismissed")` is red against the *correct* name; distinguishing the two constants has to be `!= TypeModalDismissed`, an equality on the sibling constant rather than a containment on the shared word. See [Question-batch payload § Testing](protocol-package-question-batch-payload.md) for the fixture and mutant-coverage lessons the same slice measured.
