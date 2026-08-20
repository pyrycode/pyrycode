# `internal/protocol` — wire-format envelope, routing, error codes, v1 predicate

Pure-data leaf package. Declares the wire-format types for the mobile WebSocket protocol v1 — outer envelope, relay↔binary routing wrapper, error-code constants, type-name constants, and the `IsKnownAppType` predicate. No I/O, no goroutines, no `context`, no `slog`. Spec source-of-truth is `docs/protocol-mobile.md`.

Landed in #255. Per-type payload structs (the catalog the 16 type discriminators select) are #256 sibling tickets and slot into `Envelope.Payload (json.RawMessage)` via a second-pass `json.Unmarshal` at the dispatcher; first slice (`RegisterPushTokenPayload`) landed in #275, second slice (messaging payloads) landed in #272 — it also shipped the v1 bulk-history backfill payloads (`backfill_since` / `message_chunk` / `backfill_done`), removed as dead code (zero emitters, zero handlers) in #967 — third slice (conversations-read payloads) landed in #273, fourth slice (conversations-write payloads) landed in #274, fifth slice (handshake/control: `HelloServerPayload` / `HelloClientPayload` / `HelloAckPayload` / `ErrorPayload` / `AckPayload`) landed in #271.

## Files

```
internal/protocol/
├── envelope.go                  Envelope, RoutingEnvelope, ErrUnknownType / ErrUnsupported, IsKnownAppType, inboundAppTypeSet
├── codes.go                     13 Code* string constants + 16 v1 Type* + v2-control Type* (TypeRekeyRequest, #454) + v2-interactive Type* (turn_state … turn_end, #607; + stall, #638) + v2-status-peer Type* (api_retry / compacting, #1074) + v2-snapshot Type* (request_snapshot / screen_snapshot, #617) + v2-resync Type* (TypeResync, #647) + v2-session-boundary Type* (TypeSessionTransition, #656) + v2-modal Type* (modal_shown / modal_answer / modal_cancel / modal_dismissed, #701) + v2-queue Type* (queue_state / dequeue_message, #720) + v2-interrupt Type* (TypeInterrupt, #707, payload-less inbound control) + v2-debug-bundle Type* (debug_bundle_chunk / debug_bundle_done / request_debug_bundle, #812/#813) + v2-new_session Type* (TypeNewSession, #831, payload-less inbound control) + v2-session-settings Type* (set_session_settings / session_settings_updated, #844) + v2-session-settings-read Type* (request_session_settings / session_settings, #491/#1214, conversation_id gate #1586) + v2-session-error Type* (TypeSessionError, #1007, unwired vocabulary only) + v2-background-task Type* (background_task_started / _updated / _roster, #1393 shape, #1394 bridge) + v2-thinking-progress Type* (thinking_progress, #1386, own const block, vocabulary + mapping shipped together) + v2-rate-limited Type* (rate_limited, #1405, own const block; the turnbridge mapping + cmd/pyry handler case are #1410) + Session errors group (CodeSessionNotFound, #844; CodeSessionBlocked, #1007)
├── push.go                      RegisterPushTokenPayload (#275) — register_push_token body
├── messaging.go                 SendMessagePayload, MessagePayload (#272); SessionTransitionPayload (#656, v2 session-boundary marker body); ModalOption + ModalShownPayload / ModalAnswerPayload / ModalCancelPayload / ModalDismissedPayload (#701, v2 modal vocabulary bodies); QueuedItem + QueueStatePayload / DequeueMessagePayload (#720, v2 queue vocabulary); DebugBundleChunkPayload / DebugBundleDonePayload (#812, v2 debug-bundle streaming bodies); SessionErrorPayload (#1007, v2 unsolicited conversation-scoped terminal give-up body — unwired, producer is sibling #1008)
├── conversations_read.go        ListConversationsPayload, ConversationsPayload, ConversationSummary (#273)
├── conversations_write.go       CreateConversationPayload, ConversationCreatedPayload, PromoteConversationPayload, ConversationUpdatedPayload (#274); RenameConversationPayload (#820); DeleteConversationPayload, ConversationDeletedPayload (#822); ArchiveConversationPayload (#881, shared by archive_conversation + unarchive_conversation); ChangeWorkspacePayload (#823, reply reuses ConversationUpdatedPayload — no new reply type)
├── handshake.go                 HelloServerPayload, HelloClientPayload, HelloAckPayload, ErrorPayload, AckPayload (#271); Capabilities []string on the two phone-facing hello payloads + CapabilityInteractive const (#607); LastEventID *uint64 on HelloClientPayload (#647, inbound reconnect-replay cursor)
├── interactive.go               TurnStatePayload, AssistantDeltaPayload, ToolUsePayload, ToolResultPayload, TurnEndPayload (#607), StallPayload (#638), ApiRetryPayload / CompactingPayload (#1074), BackgroundTaskStartedPayload / BackgroundTaskUpdatedPayload / BackgroundTaskRosterPayload / BackgroundTask (#1393 shape, #1394 bridge), ThinkingProgressPayload (#1386, wired same ticket), RateLimitedPayload (#1405 shape, #1410 bridge) — v2 interactive binary→phone event bodies
├── snapshot.go                  RequestSnapshotPayload, ScreenSnapshotPayload (#617) — v2 screen-snapshot request (phone→binary) / response (binary→phone) bodies
├── settings.go                  SetSessionSettingsPayload, SessionSettingsUpdatedPayload (#844) — v2 set-session-settings request (phone→binary) / reply (binary→phone) bodies; wire vocabulary only, handler is #845; RequestSessionSettingsPayload, SessionSettingsPayload (#491/#1214, ConversationID field added #1586) — the READ half: request (phone→binary) / reply (binary→phone) bodies
├── envelope_test.go             golden round-trip for Envelope (full + minimal) and RoutingEnvelope
├── compat_test.go               truth-table for IsKnownAppType + drift detectors
├── push_test.go                 golden round-trip for RegisterPushTokenPayload via Envelope.Payload
├── messaging_test.go            golden round-trip for each of the five #272 payloads via Envelope.Payload; + SessionTransitionPayload round-trip (#656); + four modal payload round-trips (#701); + SessionErrorPayload round-trip (#1007)
├── conversations_read_test.go   golden round-trip for ListConversationsPayload / ConversationsPayload via Envelope.Payload
├── conversations_write_test.go  golden round-trip for each of the four #274 payloads + RenameConversationPayload (#820) + DeleteConversationPayload / ConversationDeletedPayload (#822) + ArchiveConversationPayload for both archive_conversation / unarchive_conversation envelopes (#881) via Envelope.Payload (ChangeWorkspacePayload, #823, has NO round-trip test/fixture here — a divergence from the rest of the slice; see codebase/823.md § Lessons learned)
├── handshake_test.go            per-type round-trip for handshake/control payloads (#271) + capabilities round-trips (#607)
├── interactive_test.go          golden round-trip for each of the five #607 interactive payloads + the #638 stall payload + the #1074 api_retry / compacting payloads + the four #1393 background-task payloads + the #1386 thinking_progress payload + the #1405 rate_limited payload (populated + zero-value fixtures) via Envelope.Payload, + TestBackgroundTaskRosterPayload_NilTasksNormalises (direct marshal, no fixture) + TestBackgroundTaskPayloads_FitV2EnvelopeCap (AC #4, table-driven, `<`-filled at producer caps) + TestThinkingProgressType_IsNotClaudesSubtype (anti-drift substring check on the constant) + TestRateLimitedType_IsNotClaudesVocabulary (anti-drift substring check + regression pins against claude's key names and the excluded identity fields)
├── snapshot_test.go             golden round-trip for the two #617 snapshot payloads + empty-conversation_id boundary
├── settings_test.go             golden round-trip for the SetSessionSettingsPayload request (present-at-zero vs omitted, table-driven) + the SessionSettingsUpdatedPayload reply (#844); golden round-trip + empty-conversation_id boundary for RequestSessionSettingsPayload + golden round-trip for SessionSettingsPayload (#491, extended #1586)
└── testdata/                    envelope_full.json, envelope_minimal.json, routing_envelope.json,
                                 register_push_token.json, send_message.json, message.json,
                                 list_conversations.json, conversations.json,
                                 create_conversation.json, conversation_created.json,
                                 promote_conversation.json, conversation_updated.json, rename_conversation.json,
                                 delete_conversation.json, conversation_deleted.json,
                                 archive_conversation.json, unarchive_conversation.json,
                                 hello_server.json, hello_client.json, hello_ack.json, error.json, ack.json,
                                 turn_state.json, assistant_delta.json, tool_use.json, tool_result.json, turn_end.json, stall.json,
                                 api_retry.json, compacting.json,
                                 request_snapshot.json, screen_snapshot.json,
                                 modal_shown.json, modal_answer.json, modal_cancel.json, modal_dismissed.json,
                                 set_session_settings_full.json, set_session_settings_omitted.json, session_settings_updated.json,
                                 request_session_settings.json, session_settings.json,
                                 session_error.json,
                                 background_task_started.json, background_task_updated.json,
                                 background_task_roster.json, background_task_roster_empty.json,
                                 thinking_progress.json,
                                 rate_limited.json, rate_limited_zero.json
```

Ten production files. `envelope.go` carries the package's behaviour surface (two structs, two sentinels, one predicate). `codes.go` carries the wire-string constants (pure data, grouped by spec table order). `push.go` + `messaging.go` + `conversations_read.go` + `conversations_write.go` + `handshake.go` carry the v1 per-type payload DTOs, one file per spec-section group — the full #256 catalog is wired — `interactive.go` (#607) carries the first v2 additive application-event DTOs, `snapshot.go` (#617) carries the v2 screen-snapshot request/response DTOs, and `settings.go` (#844) carries the v2 set-session-settings request/reply DTOs.

## Types

### `RegisterPushTokenPayload` (#275)

Body of a `register_push_token` frame (`docs/protocol-mobile.md` § Message types → `register_push_token`). Phone → binary, sent on every WS connect; the future dispatch handler persists `(platform, token, device_name)` to `devices.json` and de-duplicates against the stored triple.

```go
type RegisterPushTokenPayload struct {
    Platform   string `json:"platform"`
    Token      string `json:"token"`
    DeviceName string `json:"device_name"`
}
```

- `Platform` is one of `"fcm"` (Android) or `"apns"` (iOS). Stays `string`, not an enum — an enum would force a converter at every internal call site for no observable wire-format gain, and per-spec the dispatcher is the validation point.
- All three fields are required (no `omitempty`, no pointers). Encode-side absence surfaces as zero-value `""` on the wire, which the dispatcher rejects via shape validation.
- Pure DTO: no methods, no constructors, no `Validate()`. The dispatcher (future ticket) owns validation and is the only legitimate consumer; logging `Payload` is forbidden (may contain tokens) per the security posture below.

Golden round-trip test in `push_test.go` decodes the spec example through `Envelope` → `Envelope.Payload` → `RegisterPushTokenPayload` and re-marshals byte-equivalently against `testdata/register_push_token.json`. The decode-from-`Envelope.Payload` path (not decode-from-raw-payload-bytes) exercises the exact composition the dispatcher will use.

This is the first slice of the #256 per-type payload catalog. Sibling slices for the remaining 15 v1 type discriminators land in their own tickets and own `*.go` files.

### Messaging payloads (#272)

Bodies of the two conversation-flow envelopes (`docs/protocol-mobile.md` § Message types → `send_message` / `message`). `send_message` is phone → binary; `message` is binary → phone. (This slice originally also shipped the v1 bulk-history backfill payloads — `BackfillSincePayload` / `MessageChunkPayload` / `BackfillDonePayload`, for `backfill_since` / `message_chunk` / `backfill_done` — but that flow had zero emitters and zero handlers on either v2 dispatch surface; removed as dead code in #967. The v2 reconnect path, `request_snapshot` + `hello.last_event_id` bounded-ring replay (ADR 025 / #646/#647), replaced it entirely. See [codebase/967.md](../codebase/967.md).)

```go
type SendMessagePayload struct {
    ConversationID string `json:"conversation_id"`
    MessageID      string `json:"message_id"`
    Text           string `json:"text"`
}

type MessagePayload struct {
    ConversationID string `json:"conversation_id"`
    MessageID      string `json:"message_id"`
    Role           string `json:"role"`
    Text           string `json:"text"`
}
```

- **`MessagePayload.Role` stays `string`, not a named `Role` enum.** Spec defines a closed set (`"user"`, `"assistant"`, `"system"`) but the binary already treats role-strings as `string`-typed elsewhere; a typed `Role` would force a converter at every internal call site for no observable wire-format gain, and the closed-set guarantee belongs at the dispatcher. Matches `RegisterPushTokenPayload.Platform`'s rationale.
- **All required fields are non-pointer, no `omitempty`.** Encode-side absence surfaces as zero-value `""` on the wire; the dispatcher rejects malformed frames via shape validation. Empty `text` is wire-legitimate (semantic validation lives at the dispatcher).
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to `RegisterPushTokenPayload`. Required-field validation, role-set enforcement, ID monotonicity, clock-skew bounds — all dispatcher concerns.

Golden round-trip tests in `messaging_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against the matching fixture. The package's canonical `*string`-WITHOUT-`omitempty` "literal `null` on the wire" idiom is now `SessionTransitionPayload.WorkspaceCwd` (#656, below) — the original example, `BackfillSincePayload.ConversationID`, was removed with the rest of the backfill flow in #967.

### Session-transition payload (#656)

Body of an `Envelope` whose `Type == TypeSessionTransition` (`docs/protocol-mobile.md` § session_transition). Binary → phone; the wire form of a session boundary the phone renders as a `ThreadItem.SessionBoundary` marker (`pyrycode-mobile#336`) when the daemon's session rotates (a `/clear`, an idle eviction, or a workspace change) — instead of inferring the boundary from message fields that do not exist. Lives in `messaging.go` (not `interactive.go`: a session boundary is not a turn-stream event, and `messaging.go` already houses the `time.Time` + `*string`-no-omitempty precedents this struct copies). **Wire shape only** — the producer that emits it is sibling #657 (`security-sensitive`, blocked on #656).

```go
type SessionTransitionPayload struct {
    ConversationID    string    `json:"conversation_id"` // #740: routing key; plain string, no omitempty — mirrors the sibling interactive payloads
    PreviousSessionID string    `json:"previous_session_id"`
    NewSessionID      string    `json:"new_session_id"`
    Reason            string    `json:"reason"`
    OccurredAt        time.Time `json:"occurred_at"`
    WorkspaceCwd      *string   `json:"workspace_cwd"` // *string + no omitempty: literal `null` for non-workspace_change reasons
}
```

- **`ConversationID` is the routing key — plain `string`, no `omitempty`, first field (#740).** Mirrors the four sibling interactive payloads (`SendMessagePayload` / `MessagePayload` / `QueueStatePayload` / `DequeueMessagePayload`) that all lead with `ConversationID string json:"conversation_id"` and route by it, so the phone folds the boundary marker into the correct conversation's thread (`pyrycode-mobile#336`). Deliberately a plain `string`, **not** `*string` — unlike `WorkspaceCwd` it has **no** literal-null ("all conversations") semantics; it is a present-or-empty routing key like `MessagePayload.ConversationID`. **Wire-vocabulary half only** in #740: the producer (`toWirePayload`, `cmd/pyry/session_transition_v2.go`) uses keyed composite literals, so #740 compiled it untouched, emitting a transient `conversation_id: ""`. [#741](../codebase/741.md) then **populated** it — the producer's `broadcast` resolves the transitioning session's owning conversation from the maintained registry binding (`conversationForSession`, `CurrentSessionID` + `SessionHistory`) and stamps the key, dropping the whole event fail-closed when unresolvable (never an empty/guessed routing key on the wire). See [codebase/740.md](../codebase/740.md) (field) and [codebase/741.md](../codebase/741.md) (producer); [conversation-session-binding.md § Reading the binding](conversation-session-binding.md#reading-the-binding-to-stamp-conversation_id-741).
- **`WorkspaceCwd` is `*string` WITHOUT `omitempty` — encodes a cross-field invariant on the wire.** Non-nil **iff** `Reason == "workspace_change"` (the new workspace dir), literal JSON `null` for `clear` / `idle_evict`. `omitempty` would drop the key and lose the "absent vs null vs value" distinction, so the *workspaceCwd-non-null-iff-`workspace_change`* invariant is decodable from the wire alone. This is now the package's canonical `*string`-without-`omitempty` example — the original, `BackfillSincePayload.ConversationID` (#272), was removed as dead code in #967 (see [codebase/967.md](../codebase/967.md)); `CreateConversationPayload` / `ConversationSummary.Name` and other siblings below cite this field as precedent. The byte-equal round-trip is the regression detector against an accidental `omitempty` re-add.
- **`Reason` stays a plain `string`, not a named enum** — `MessagePayload.Role` / `TurnEndPayload.StopReason` precedent. Closed wire set `{clear, idle_evict, workspace_change}`; the closed-set guarantee belongs at the decoder. The mobile enum names (`Clear`/`IdleEvict`/`WorkspaceChange`) map to the lowercase-snake wire values by the mobile decoder. **The type admits `workspace_change` even though the producer (#657) cannot emit it** until a server-side workspace-change source exists — so the mobile decoder stays exhaustive and the invariant is expressible (type child carries the full vocabulary; producer child defers the unemittable value).
- **`OccurredAt` is `time.Time` (RFC3339Nano on the wire) per the envelope timestamp rule.** Marshal strips the monotonic clock; tests compare with `.Equal`, never `==`. Same discipline as `Envelope.TS`.
- **No `event_id` field.** `event_id` is an `Envelope`-level field (#649) stamped by the producer on structured-stream frames; a session boundary is **not** a structured turn-stream event and carries no `event_id`.

`TestSessionTransitionPayload_RoundTrip` (`messaging_test.go`) is table-driven over two fixtures authored in **struct-field order** (`canonical()` compacts but does not sort keys, so struct field order == fixture `payload` key order == doc-table row order, all now **leading with `"conversation_id":""`** — #740): `session_transition.json` (cwd-unset, `reason: "idle_evict"`, `"workspace_cwd": null` — the `omitempty`-regression guard) and `session_transition_workspace.json` (cwd-set, `reason: "workspace_change"`, `"workspace_cwd": "/home/user/project"`). The `wantConvID` column pins the transient `""` zero value. See [codebase/656.md](../codebase/656.md) and [codebase/740.md](../codebase/740.md).

### Modal v2 wire payloads (#701)

The wire vocabulary for a **modal** the supervised `claude` surfaces over the
encrypted mobile wire (`docs/protocol-mobile.md` § Modal; epic #597 Phase 3,
[ADR 025]). Lifecycle `modal_shown` → `modal_answer` / `modal_cancel` →
`modal_dismissed`. Five new exported types in `messaging.go` (a modal is a
control/boundary concern, not a turn-stream event, so `messaging.go` not
`interactive.go`), mapping to the four `Type*` constants. `modal_shown` /
`modal_dismissed` are outbound binary → phone events; `modal_answer` /
`modal_cancel` are **inbound phone → binary control** envelopes the v2 session
manager intercepts at `v2session.go`'s `dispatchAppFrame` **before**
`dispatch.Route` (the `RequestSnapshotPayload` / `TypeRekeyRequest` precedent —
**no `dispatch.Route` handler**). **Wire shape only** — the minting/dedup/
validation/fan-out runtime is the producer's (#703, with #706/#702 building
ownership/gating).

```go
type ModalOption struct { // a single ordered choice
    ID    string `json:"id"`
    Label string `json:"label"`
}

type ModalShownPayload struct { // binary → phone
    ModalID         string        `json:"modal_id"`
    Class           string        `json:"class"`
    Title           string        `json:"title"`
    Prompt          string        `json:"prompt"`
    Options         []ModalOption `json:"options"`           // ordered: array order is display/selection order
    DefaultOptionID string        `json:"default_option_id"` // MUST equal one of Options[].ID (documented invariant)
}

type ModalAnswerPayload struct { // phone → binary, inbound control
    ModalID     string `json:"modal_id"`
    OptionID    string `json:"option_id"`
    AnswerToken string `json:"answer_token"` // client-minted idempotency key
}

type ModalCancelPayload struct { // phone → binary, inbound control
    ModalID string `json:"modal_id"`
}

type ModalDismissedPayload struct { // binary → phone
    ModalID string `json:"modal_id"`
    Outcome string `json:"outcome"` // selected option id, or producer-defined cancel/timeout sentinel
    Source  string `json:"source"`  // closed set {remote, local, timeout}
}
```

- **No `omitempty` on any field** — the same deliberate inverse as the #607
  interactive and #617 snapshot payloads. Every field is always present so the
  fixtures pin the full shape and boundary values (an empty `default_option_id`,
  an empty `option_id`) cannot silently vanish. No `time.Time` field — the
  envelope's `ts` covers timing — so **no new import**.
- **`modal_id` is the sole correlation key — there is no `conversation_id`.** The
  daemon resolves `modal_id` against its **own** outstanding-modal state and never
  trusts a phone-asserted conversation; `option_id` maps against the daemon's own
  recorded option list. A shape carrying both `conversation_id` and `modal_id`
  would admit a disagreeing pair the daemon must adjudicate — the single-key shape
  forecloses cross-conversation `modal_id` confusion structurally. (Producer
  obligation: `modal_id` minted from `crypto/rand`, globally unique across
  concurrently-outstanding modals.)
- **`answer_token` is an idempotency key, not a credential.** Uniqueness and
  stability matter; secrecy does not. It lets the daemon collapse a replayed/
  reordered `modal_answer` to a no-op via `(modal_id, answer_token)`. It is **not**
  the authorization — that is `modal_id` validity (#706) + the per-device answer
  gate (#702, default OFF); `answer_token` only deduplicates among already-
  authorized answers.
- **`Options` is ordered + `DefaultOptionID ∈ Options[].ID`.** JSON-array order is
  the canonical display/selection order; the default-in-options invariant is
  documented (the producer enforces it).
- **`Source` is the closed set `{remote, local, timeout}`**; `Class` / `Outcome`
  stay plain strings whose exhaustive vocabularies the producer (#703) owns
  (documented, not enforced) — the `MessagePayload.Role` /
  `SessionTransitionPayload.Reason` leaf-data convention. Only `source` is pinned
  to a closed set because it is fully determined by the resolution mechanism.
- **`security-sensitive` rides the *shape* review, not code** — no handler ships,
  but this is the new inbound (phone→daemon) control surface for a high-consequence
  action. Architect security pass verdict **PASS**; it forecloses the
  cross-conversation-confusion class and keeps validity-gate vs dedup-key separate.

Four flat **one-func-per-type** round-trips in `messaging_test.go`
(`TestModalShownPayload_RoundTrip` asserts `len(Options)==2` + positional ids +
`DefaultOptionID`; `TestModalAnswerPayload_RoundTrip` asserts `AnswerToken`
round-trips per the AC; `TestModalDismissedPayload_RoundTrip` asserts
`Source=="remote"`), each on the shared `roundTripEnvelope` helper, over four
single-line fixtures authored in **struct-field order**. See
[codebase/701.md](../codebase/701.md).

### Queue v2 wire payloads (#720)

The wire vocabulary for the **queued-message backlog** over the encrypted mobile
wire (`docs/protocol-mobile.md` § Queue; epic #597 Phase 3, [ADR 025]). A phone
that types while `claude` is busy has its turn buffered in `internal/msgqueue`
(#704, extended #719); the phone can **view** the backlog (`queue_state`, daemon →
phone, the wire form of `msgqueue.Snapshot(convID)`) and **cancel** an entry
(`dequeue_message`, phone → daemon, driving `msgqueue.Remove(convID, id)`). Three
new exported types in `messaging.go` (a queue snapshot is daemon **state**, not a
turn-stream event, so `messaging.go` not `interactive.go`; it already houses the
`time.Time` + nested-array precedents), mapping to two `Type*` constants.
`queue_state` is an outbound binary → phone event; `dequeue_message` is an
**inbound phone → binary control** envelope the v2 session manager intercepts at
`v2session.go`'s `dispatchAppFrame` **before** `dispatch.Route` (the
`ModalAnswerPayload` / `RequestSnapshotPayload` precedent — **no `dispatch.Route`
handler**). **Wire shape only** — the emit-on-change/fan-out producer is #722, the
intercept/resolve-convID/remove handler is #723.

```go
type QueuedItem struct { // one element of QueueStatePayload.Queued
    QueuedMsgID uint64    `json:"queued_msg_id"` // plain per-conversation counter (≥1), NOT a nonce
    Text        string    `json:"text"`          // untrusted, phone-originated transit content
    TS          time.Time `json:"ts"`            // enqueue time, RFC3339Nano
}

type QueueStatePayload struct { // binary → phone; wire form of msgqueue.Snapshot(convID)
    ConversationID string       `json:"conversation_id"` // daemon's own resolved id (#722), never attacker-derived
    Queued         []QueuedItem `json:"queued"`          // ordered FIFO/enqueue order (the options []ModalOption precedent)
}

type DequeueMessagePayload struct { // phone → binary, inbound control (intercepted pre-dispatch.Route, no handler)
    ConversationID string `json:"conversation_id"` // untrusted phone input; #723 resolves to an authorized conversation
    QueuedMsgID    uint64 `json:"queued_msg_id"`   // the id to remove (msgqueue.Remove(convID, id))
}
```

- **No `omitempty` on any field** — the same deliberate inverse as the #607
  interactive / #617 snapshot / #701 modal payloads. Every field is always present
  so the fixtures pin the full shape. `package protocol` already imports `time`
  (for `Envelope.TS`) — **no new import**.
- **`queued_msg_id` is a plain `uint64` per-conversation counter (≥ 1), NOT a
  nonce.** It matches `msgqueue.QueuedMessage.ID` exactly so the producer maps
  `QueuedMessage.ID → QueuedMsgID` with no translation. The mobile client (#429)
  must decode it as a JSON **integer**, not a string. This is the discriminator
  from #701's `modal_id` (an unguessable nonce) — there is no secrecy property, so
  this slice is **unlabelled** (see below).
- **`queued` ordering + empty-backlog `[]` vs `null`.** Array order is canonical
  FIFO/enqueue order (the `Options []ModalOption` precedent). `[]QueuedItem(nil)`
  marshals to `"queued":null`, a non-nil empty slice to `[]`; the leaf type cannot
  force non-nil, so the contract is only "`queued` always present". The docs
  **recommend** the producer (#722) emit `[]` (not `null`) for an empty backlog so
  the mobile decoder keeps `queued` a plain array — not enforced here; both
  round-trip.
- **`TS` is `time.Time` (RFC3339Nano on the wire)** per the envelope timestamp
  rule; marshal strips the monotonic clock so tests compare with `.Equal`, never
  `==` / `reflect.DeepEqual`. Same discipline as `Envelope.TS` /
  `SessionTransitionPayload.OccurredAt`.
- **Unlabelled (`security-sensitive`: no) — mirrors #656, not #701.** Dequeuing is
  **ungated** for any paired phone (ADR 025 § Security model); only answering a
  permission-class modal is gated. No nonce, no per-device gate. `Text` and the
  inbound `ConversationID` are **untrusted, phone-originated** content (never log
  `Text`; resolve `ConversationID` to an authorized conversation before acting) —
  but the slice stores/inspects neither; that discipline lives in the
  `security-sensitive` siblings #722/#723/#721.
- **No `turnevent`/`turnbridge` neutral hop.** Queue backlog is daemon state, not a
  turn-stream event, so the producer builds the `protocol.*Payload` directly from
  engine state and the inbound frame is decoded at `dispatchAppFrame` — neither
  direction routes through `turnevent`/`turnbridge` (that path is reserved for
  tui-driver turn-stream events). See [codebase/720.md](../codebase/720.md) for the
  mechanism rationale and the two-opposite-direction-sums note.

`TestQueueStatePayload_RoundTrip` (`messaging_test.go`) asserts `len(Queued)==2` +
positional `QueuedMsgID`/`Text` + per-item `TS` via `.Equal`, then byte-equal
round-trips via `roundTripEnvelope`; `TestDequeueMessagePayload_RoundTrip` covers
the inbound control; table-driven `TestDequeueMessagePayload_Malformed` pins the
AC's "rejected cleanly (error, no panic)". Two fixtures (`queue_state.json` with
N=2 items, `dequeue_message.json`) authored in **struct-field order**. See
[codebase/720.md](../codebase/720.md).

### Debug-bundle streaming payloads (#812)

The byte-generic wire bodies for streaming a large debug bundle over the encrypted
mobile channel (`docs/protocol-mobile.md` § Debug bundle; split from #803). A
content-bearing bundle (assembled by [`internal/debugbundle`](debugbundle-package.md),
#811) routinely exceeds one 65535-byte AEAD frame, so the daemon streams it as
ordered, cap-respecting chunks ending in a completion marker. **Binary → phone
direction; wire vocabulary only** — the chunker, the streaming primitive
(`StreamBundle`), and the reassembly reference (`ReassembleBundle`) live in
[`internal/relay/v2bundlestream.go`](v2-session-manager.md#debug-bundle-streaming-812--streambundle--bundleenvelopes--reassemblebundle);
the request verb that drives a stream is sibling #813 — `TypeRequestDebugBundle =
"request_debug_bundle"`, an inbound phone → binary **bare control type with no
payload struct** (the bundle is daemon-global, so there is no field to carry;
mirrors `TypeInterrupt`), added in the same v2-only const block and registered in
the three `compat_test.go` drift-detector sites. See [codebase/813.md](../codebase/813.md).

```go
type DebugBundleChunkPayload struct {
    Seq  int    `json:"seq"`
    Data []byte `json:"data"`
}

type DebugBundleDonePayload struct {
    Total int `json:"total"`
}
```

- **`Seq` is 0-based, contiguous, ascending across a stream.** The receiver
  (`ReassembleBundle` / the phone) requires the next chunk's `Seq` to equal the
  count of chunks already seen, so a reorder, gap, or duplicate **fails cleanly**
  rather than corrupting output — the structural half of the two-net integrity
  contract (AEAD guarantees per-frame content integrity; `Seq`+`Total` add gap /
  reorder / truncation detection).
- **`Data []byte` auto-encodes as standard base64 via `encoding/json`** (the phone
  base64-decodes). It is **content-bearing bundle bytes — never logged** (AC#4);
  the base64 expansion (×4/3) is why `bundleChunkBytes` is set conservatively
  under the frame cap, not at it.
- **`DebugBundleDonePayload.Total` is the exact chunk count.** The receiver uses it
  to detect a truncated stream: a `done` whose `Total` ≠ the number of chunks
  actually received is a count-mismatch error, never accepted as complete. An empty
  blob is a valid stream — 0 chunks + `done{total:0}`, reassembling to empty.
- **Pure DTOs, no `omitempty`** (the queue/interactive-payload posture). Golden
  round-trips `TestDebugBundleChunkPayload_RoundTrip` / `TestDebugBundleDonePayload_RoundTrip`
  against `testdata/debug_bundle_chunk.json` / `debug_bundle_done.json`. Both
  `Type*` constants are registered in the `compat_test.go` drift detector
  (`v2OnlyTypes`, the partition `all` list, the `-rejected` cases) and are **not**
  in `inboundAppTypeSet` — an old phone must never receive these outbound events. See
  [codebase/812.md](../codebase/812.md).

### Session settings payloads (#844)

The wire vocabulary for changing a session's per-session model / reasoning
effort / YOLO (`docs/protocol-mobile.md` § Session settings; split from
#841). **Wire vocabulary only** — the handler that intercepts
`set_session_settings` at `v2session.go`'s `dispatchAppFrame` **before**
`dispatch.Route` (the `TypeModalAnswer` / `TypeNewSession` precedent — **no
`dispatch.Route` handler**), gates on the `interactive` capability, validates,
persists via `sessions.Pool.UpdateSettings` (#840), and emits the reply
shipped in sibling #845 (see [Inbound set_session_settings](v2-session-manager.md#inbound-set_session_settings-845--settingsupdater-seam-validate-persist-reply)).
See [codebase/844.md](../codebase/844.md).

```go
type SetSessionSettingsPayload struct {
    SessionID string  `json:"session_id"`
    Model     *string `json:"model,omitempty"`
    Effort    *string `json:"effort,omitempty"`
    YOLO      *bool   `json:"yolo,omitempty"`
}

type SessionSettingsUpdatedPayload struct {
    SessionID string `json:"session_id"`
}
```

- **The three settings fields are pointers with `omitempty` — the presence
  contract.** `nil` means "leave unchanged"; a non-nil pointer means "set to
  this value", including a non-nil `*""` (`Model`/`Effort`) or `*false`
  (`YOLO`), which are thereby distinguishable from omitted. This is the
  **opposite** of the sibling `*string`-without-`omitempty` payload
  `SessionTransitionPayload`, which encodes a literal `null` sentinel. Here an unset field must be *absent*, not `null` —
  the minimal shape a client changing one setting naturally produces. An
  absent `yolo` can never masquerade as a sent `false`, and an absent `model`
  can never masquerade as an instruction to clear a stored value. The struct
  doc comment flags the divergence explicitly so a future contributor
  doesn't "fix" it by copying the sibling style.
- **Mirrors `sessions.SettingsUpdate{Model, Effort *string; YOLO *bool}`
  (#840) field-for-field** — the #845 handler decodes this payload straight
  into that seam.
- **`SessionID` is the addressing key** — matches
  `sessions.Pool.UpdateSettings(id sessions.SessionID, …)` and is already
  carried to clients in the `session_transition` marker's `new_session_id`
  field, so a client already knows it. Plain `string`, always required, no
  `omitempty`.
- **The reply does not echo the applied settings** — it only identifies the
  confirmed session; the request↔reply correlation rides `Envelope.InReplyTo`
  at the handler layer, and the client already knows what it sent.
- **Not `security-sensitive`** (per the wire-vocab → handler split precedent
  #701→#703 / #720→#723 / #812→#813 / #656→#657): this leaf defines shape
  only, no nonce/token/capability primitive. `yolo` is a shape, not a gate —
  the fail-safe (nil never enables bypass) lives in `sessions.SettingsUpdate`
  (#840); the capability gate and persistence are handler sibling #845,
  which carries the label.

Golden round-trips in `settings_test.go`: a table-driven test over
`set_session_settings_full.json` (all three fields present at zero value)
vs `set_session_settings_omitted.json` (only `session_id`), asserting pointer
nil-ness then a byte-equal re-marshal — the regression guard for the
`omitempty` decision — plus a `session_settings_updated.json` round-trip for
the reply.

### Session settings read payloads (#491/#1214, `ConversationID` field #1586, conversation-keyed reply #1610)

The READ half the #844 cluster shipped without: `set_session_settings`
changes the values and `session_settings_updated` only echoes the id back, so
a client had no way to ask what the current run configuration *is*, nor which
session id to address a change to. Before this pair the only sources were
`screen_snapshot`'s side-load (values) and the unsolicited
`session_transition` marker (id, fired only on clear/idle-eviction — never on
session creation). Handler is [`handleRequestSessionSettings`, documented in
v2-session-manager.md](v2-session-manager.md#inbound-request_session_settings-4911214-extended-1586-conversation-keyed-1610--the-read-half-of-the-844-cluster).

```go
type RequestSessionSettingsPayload struct {
    ConversationID string `json:"conversation_id"`
}

type SessionSettingsPayload struct {
    SessionID    string `json:"session_id"`
    Model        string `json:"model"`
    Effort       string `json:"effort"`
    YOLO         bool   `json:"yolo"`
    UsedTokens   int    `json:"used_tokens"`
    WindowTokens int    `json:"window_tokens"`
}
```

- **`ConversationID` was added by #1586; the frame was genuinely bare before
  it.** It names the conversation the client is asking about. Untrusted
  network input, resolved through the handler's conversation-keyed
  `RunConfigFor` seam (#1610) rather than a membership check — the seam
  resolves-and-refuses in one call, so an unknown or unbound conversation
  never distinguishes itself from any other unaddressable case. It reaches no
  log line, no error string, no filesystem path, and not the reply.
- **No `omitempty` on either struct**, matching `RequestSnapshotPayload` /
  `ScreenSnapshotPayload` and deliberately unlike the sibling
  `SetSessionSettingsPayload` above, whose per-field pointers encode a
  presence contract. There is no presence contract on the request: an absent
  and an empty `conversation_id` are the **same** case — "no conversation
  named" — so nothing needs to tell them apart, and keeping the field always
  on the wire lets a fixture pin the full shape. On the reply, every field is
  always a real answer, not an absence: `SessionID ""` means "nothing to
  address", `Model`/`Effort` `""` mean "inherited default, no per-session
  override", `YOLO false` means permissions are enforced, and `WindowTokens 0`
  means the usage seam is unwired (`UsedTokens 0` against a non-zero
  `WindowTokens` is a genuine fresh session).
- **The field gates *which* session the reply describes (#1610).** A
  `conversation_id` naming a conversation this daemon hosts, with a live
  bound session, is answered with **that conversation's own** values — never
  the shared bootstrap session's. An absent/empty `conversation_id`, one
  naming a conversation this daemon does not host, or one with no live bound
  session is answered with a zero-valued `SessionSettingsPayload`, never an
  error frame: `session_id: ""` is already the defined "no session to
  address" answer, so the reply shape stays constant. The reported id and the
  reported values always move together, because both come from the single
  `RunConfig` `RunConfigFor` returns — a client can never read one session's
  values and write to another. There is no bootstrap-scoped fallback for this
  verb; that route was retired with `BootstrapSessionID` (#678 AC#4).

Golden round-trips in `settings_test.go`: `TestRequestSessionSettingsPayload_RoundTrip`
against `testdata/request_session_settings.json` (non-empty fixture id — this
one does **not** pin the no-`omitempty` decision, since the key survives
omission when non-empty) plus `TestRequestSessionSettingsPayload_EmptyConversationID`,
the sole pin on that decision (asserts the empty key stays on the wire rather
than being dropped); and `TestSessionSettingsPayload_RoundTrip` against
`testdata/session_settings.json` for the reply.

### Conversations-read payloads (#273)

Bodies of the conversation-listing request/response pair (`docs/protocol-mobile.md` § Message types → `list_conversations` / `conversations`). `list_conversations` is phone → binary; `conversations` is the binary's reply with `in_reply_to` set to the request's id. `ConversationSummary` is the row type, exported because it is the element type of `ConversationsPayload.Conversations`.

```go
type ListConversationsPayload struct{}

type ConversationsPayload struct {
    Conversations []ConversationSummary `json:"conversations"`
}

type ConversationSummary struct {
    ID            string    `json:"id"`
    Name          *string   `json:"name"` // *string + no omitempty: spec wire shows literal `null`; omitempty would drop the key.
    IsPromoted    bool      `json:"is_promoted"`
    Cwd           string    `json:"cwd"`
    LastMessageTS time.Time `json:"last_message_ts"`
    LastUsedAt    time.Time `json:"last_used_at"`
}
```

- **`ListConversationsPayload` is `struct{}`.** Spec shows `{}` on the wire; the type exists so the dispatcher can decode into a concrete value rather than `json.RawMessage`.
- **`ConversationSummary.Name` is `*string` WITHOUT `omitempty` — same discipline as `SessionTransitionPayload.WorkspaceCwd`.** Spec example shows literal `"name": null` on one of the two rows (an unnamed scratch conversation). `*string` distinguishes "null on wire" (nil pointer) from "absent" and from "empty string"; dropping `omitempty` keeps the `null` literal on re-marshal (byte-identical to the spec fixture). The AC body said "`*T` + `omitempty`"; honouring that literally would silently break the byte-equivalent round-trip the same AC requires — spec wire shape wins. A multi-line WHY comment on the field is mandatory (the only field-level comment in `conversations_read.go` under the "default to no comments" rule); `TestConversationsPayload_RoundTrip`'s byte-equal check is the regression detector. The fixture carries both branches (one row `name=<string>`, one row `name=null`) so the round-trip exercises both.
- **`ConversationsPayload.Conversations` order is preserved verbatim from the wire — this type does not reorder.** Doc comment notes that the binary is the source of truth for ordering (e.g. most-recently-used first); a `Sort` / `SortMRU` helper or any ordering predicate is explicitly out of scope.
- **`LastMessageTS` / `LastUsedAt` are `time.Time` (RFC3339Nano-on-the-wire envelope rule).** Spec example uses `"2026-05-08T10:31:02Z"` (no fractional seconds); `time.Time.MarshalJSON` emits RFC3339Nano which omits the fractional component when none is present, so the round-trip is byte-identical with no custom marshaller. Tests use `time.Time.Equal`, never `==`. Same discipline as `Envelope.TS`.
- **Other required fields are non-pointer, no `omitempty`.** `ID` / `Cwd` are required `string`; `IsPromoted` is required `bool` (fixture covers both `true` and `false`). Validation that `IsPromoted == true` implies `Name != nil`, ID uniqueness, ordering invariants — all dispatcher / registry concerns.
- **`ConversationSummary` field declaration order matches the fixture's per-row key order** (`id, name, is_promoted, cwd, last_message_ts, last_used_at`); Go's `encoding/json` emits in declaration order, so this is what makes the byte-equal round-trip survive.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to `RegisterPushTokenPayload` and the messaging slice. The future dispatch handler reads `internal/conversations.Registry`, maps each `Conversation` row to a `ConversationSummary`, and sends a `conversations` envelope with `in_reply_to` set to the request id — registry-to-payload mapping is a downstream concern, not this package's.

Golden round-trip tests in `conversations_read_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against `testdata/list_conversations.json` / `testdata/conversations.json`. `TestConversationsPayload_RoundTrip` asserts both rows: row 0 has a non-nil `Name` pointer; row 1 has `Name == nil` (NOT `*c1.Name == ""` — would panic on nil deref AND be the wrong check). The `conversations.json` envelope rides with `in_reply_to: 3`, the first protocol fixture pinning `in_reply_to` alongside an array-carrying payload.

### Conversations-write payloads (#274, + `RenameConversationPayload` #820, + `DeleteConversationPayload` / `ConversationDeletedPayload` #822, + `ArchiveConversationPayload` #881, + `ChangeWorkspacePayload` #823)

Bodies of the conversation create/promote/rename/delete/archive/change-workspace lifecycle (`docs/protocol-mobile.md` § Message types → `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated` / `rename_conversation` / `delete_conversation` / `conversation_deleted` / `archive_conversation` / `unarchive_conversation` / `change_workspace`). Phone → binary: `create_conversation`, `promote_conversation`, `rename_conversation`, `delete_conversation`, `archive_conversation`, `unarchive_conversation`, `change_workspace`. Binary → phone: `conversation_created` (reply, rides `in_reply_to`), `conversation_updated` (reply to `promote_conversation`, `rename_conversation`, `archive_conversation`, `unarchive_conversation`, and `change_workspace`, rides `in_reply_to`; documented elsewhere as a broadcast type but every producer — #820's rename, #881's archive/unarchive, #823's change_workspace — replies only to the requester, no live fan-out), `conversation_deleted` (reply to `delete_conversation`, rides `in_reply_to`, requester only — no live fan-out, see [codebase/822.md](../codebase/822.md)).

```go
type CreateConversationPayload struct {
    IsPromoted *bool   `json:"is_promoted"` // *T + no omitempty (see WHY comment on the struct)
    Name       *string `json:"name"`
    Cwd        *string `json:"cwd"`
}

type ConversationCreatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    Cwd        string    `json:"cwd"`
    Name       *string   `json:"name"`
    LastUsedAt time.Time `json:"last_used_at"`
}

type PromoteConversationPayload struct {
    ConversationID string `json:"conversation_id"`
    Name           string `json:"name"`
    Cwd            string `json:"cwd"`
}

// RenameConversationPayload (#820). Deliberately NOT a reuse of
// PromoteConversationPayload — promote also carries a required Cwd, which a
// rename neither has nor means.
type RenameConversationPayload struct {
    ConversationID string `json:"conversation_id"`
    Name           string `json:"name"`
}

// DeleteConversationPayload (#822). Hard (permanent) delete — the reversible
// path is archive/unarchive (#880/#881), a distinct verb.
type DeleteConversationPayload struct {
    ConversationID string `json:"conversation_id"`
}

// ConversationDeletedPayload (#822). Carries only the deleted id — unlike
// ConversationUpdatedPayload, there is no surviving record to project
// name/cwd/last_used_at from.
type ConversationDeletedPayload struct {
    ID string `json:"id"`
}

// ArchiveConversationPayload (#881) is the body of BOTH archive_conversation
// and unarchive_conversation — a symmetric toggle of one durable flag, so one
// id-only payload serves both verbs. Deliberately NOT a reuse of
// DeleteConversationPayload despite the identical shape (semantic coupling /
// false dependency).
type ArchiveConversationPayload struct {
    ConversationID string `json:"conversation_id"`
}

// ChangeWorkspacePayload (#823). "Workspace" IS the conversation's cwd — this
// codebase has no separate workspace-id concept — so the target is an untrusted
// filesystem path, not an id. Deliberately NOT a reuse of
// PromoteConversationPayload — promote also carries a required Name, which a
// workspace change neither has nor means.
type ChangeWorkspacePayload struct {
    ConversationID string `json:"conversation_id"`
    Cwd            string `json:"cwd"`
}

type ConversationUpdatedPayload struct {
    ID         string    `json:"id"`
    IsPromoted bool      `json:"is_promoted"`
    IsArchived bool      `json:"is_archived"` // #881 — always serialized, no omitempty (see below)
    Name       *string   `json:"name"`
    Cwd        string    `json:"cwd"`
    LastUsedAt time.Time `json:"last_used_at"`
}
```

- **`*T` WITHOUT `omitempty` for every spec-optional field whose example wire shows `null`** — `CreateConversationPayload.{IsPromoted, Name, Cwd}`, `ConversationCreatedPayload.Name`, `ConversationUpdatedPayload.Name`. Same discipline as `SessionTransitionPayload.WorkspaceCwd` (#656) and `ConversationSummary.Name` (#273). The rationale is documented once in detail on `CreateConversationPayload` and cross-referenced from the others; this is the only struct in the slice with three optional pointers in a row, so it's the natural home for the comment block. `omitempty` on a nil pointer would drop the key entirely and break byte-equivalent round-trip with the spec example.
- **`CreateConversationPayload.IsPromoted` is `*bool` — pointer-to-zero round-trips as the scalar.** Wire `false` survives as a pointer-to-false (NOT collapsed to nil); wire `null` would survive as nil. The test pins the pointer-to-false branch (spec example is `"is_promoted": false`); the wire-null branch is covered by `Name` / `Cwd` on the same struct, so the `*bool` shape is exercised end-to-end across the slice.
- **Field declaration order matches each spec example verbatim** — `_created` has `{ID, IsPromoted, Cwd, Name, LastUsedAt}`, `_updated` has `{ID, IsPromoted, Name, Cwd, LastUsedAt}` (note `Name` / `Cwd` swap). Go's `encoding/json` emits fields in declaration order; the byte-equal round-trip enforces the swap is correct.
- **`LastUsedAt` is `time.Time` (RFC3339Nano-on-the-wire envelope rule).** Spec example values (`"2026-05-08T10:34:01Z"` / `"2026-05-08T10:34:30Z"`) have no fractional seconds; `time.Time.MarshalJSON` emits RFC3339Nano which omits the fractional component when none is present, so the round-trip is byte-identical with no custom marshaller. Padding fixtures with `.000Z` would break it. Tests use `time.Time.Equal`, never `==`.
- **`PromoteConversationPayload` is the only fully-required struct in the slice.** All three fields (`ConversationID`, `Name`, `Cwd`) are non-pointer `string`, no `omitempty`. Promotion requires a name and an effective cwd, and the conversation_id must resolve to an existing row — semantic gates the dispatcher / registry (`Registry.Promote`'s `ErrPromotion*` sentinels) enforce.
- **`RenameConversationPayload` (#820) is fully-required, two fields, no pointers.** Both `ConversationID` and `Name` are non-pointer `string` — a rename must name a target and a new title; there's no optional-field case to distinguish (contrast `CreateConversationPayload`'s three nullable fields). Empty/whitespace-only `Name` and an unresolvable `ConversationID` are semantic gates the dispatch handler enforces (`internal/relay/handlers.RenameConversation`), not this layer — same posture as `PromoteConversationPayload`.
- **`DeleteConversationPayload` (#822) is one required field, no pointers** — mirrors `PromoteConversationPayload` / `RenameConversationPayload`'s value-typed `ConversationID`. An unresolvable id is not a decode-layer concern: the dispatch handler (`internal/relay/handlers.DeleteConversation`) treats a miss as `conversation.not_found`, not a payload-shape error.
- **`ConversationDeletedPayload` (#822) is the minimal ack — one `id` field.** Deliberately not a reuse of `ConversationUpdatedPayload`: post-delete there is no row left to source `name`/`cwd`/`last_used_at` from. See [codebase/822.md](../codebase/822.md) for why `conversation_updated` doesn't fit.
- **`ArchiveConversationPayload` (#881) is one required field, no pointers — shared by both verbs.** Same value-typed `ConversationID` shape as `DeleteConversationPayload`, kept as a distinct type rather than a reuse: archive/unarchive and delete are semantically unrelated (reversible vs. permanent), so sharing a type would be a false dependency. An unresolvable id is not a decode-layer concern — the dispatch handler (`internal/relay/handlers.ArchiveConversation`) treats a miss as `conversation.not_found`.
- **`ChangeWorkspacePayload` (#823) is two required fields, no pointers — the family's first payload carrying an untrusted filesystem path.** `Cwd` here is not a display string like `RenameConversationPayload.Name`; it becomes the conversation's spawn working directory, so the dispatch handler (`internal/relay/handlers.ChangeWorkspace`) confines it to `$HOME` (symlink-resolved) before storing — this is why the ticket carries `security-sensitive` where its rename/delete siblings do not. Empty/whitespace `Cwd` and a path that escapes `$HOME` are semantic gates the handler enforces, not this layer. See [codebase/823.md](../codebase/823.md).
- **`ConversationUpdatedPayload.IsArchived` (#881) has no `omitempty`, unlike the on-disk `Conversation.IsArchived`.** A client must read the flag on active rows too (value `false`) to partition active vs. archived state — an absent key couldn't distinguish "restored to active" from "old daemon." Placed immediately after `IsPromoted` to mirror `ConversationSummary`'s field order (#880) and group the two state bools; the fixture (`testdata/conversation_updated.json`) and both producers (`rename_conversation.go`, `archive_conversation.go`) were updated in lockstep so an archived conversation renamed still reports `is_archived: true` correctly.
- **`conversation_created.json` is the only fixture in the slice carrying `in_reply_to`** (`in_reply_to: 4`, matching the `create_conversation` frame at id 4). The test pins `env.InReplyTo != nil && *env.InReplyTo == 4`.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to #275, #272, #273. Required-field validation, name uniqueness, ID resolution, broadcast fan-out — all dispatcher / registry concerns.

Golden round-trip tests in `conversations_write_test.go` decode each spec example through `Envelope` → `Envelope.Payload` → per-type struct and re-marshal byte-equivalently against the matching fixture. Flat test functions (no table-driven; `TestRenameConversationPayload_RoundTrip` #820, `TestDeleteConversationPayload_RoundTrip` / `TestConversationDeletedPayload_RoundTrip` #822, and two `ArchiveConversationPayload` round-trips — one per envelope fixture (`archive_conversation.json` / `unarchive_conversation.json`), differing only in `type` — #881 added alongside the original four), each follows the sibling-slice template. **`ChangeWorkspacePayload` (#823) breaks this pattern** — no `TestChangeWorkspacePayload_RoundTrip` and no `testdata/change_workspace.json` fixture were added; its JSON shape is exercised only indirectly, via `json.Unmarshal` inside `internal/relay/handlers/change_workspace_test.go`. See [codebase/823.md](../codebase/823.md) § Lessons learned.

### Workspace-folder payloads (`workspace.go`, #887)

Body of `create_workspace_folder` / `workspace_folder_created` (`docs/protocol-mobile.md` § `create_workspace_folder`). A **new file**, not an addition to `conversations_write.go`: unlike every verb in the conversations-write slice above, this one names no conversation and carries no `conversation_id` — it creates a directory on the daemon host and returns its path.

```go
type CreateWorkspaceFolderPayload struct {
    Parent string `json:"parent"`
    Name   string `json:"name"`
}

type WorkspaceFolderCreatedPayload struct {
    Path string `json:"path"`
}
```

- **Both fields required, value-typed strings — no pointers, no `omitempty`.** Same discipline as `PromoteConversationPayload` / `RenameConversationPayload`: no optional-field branch to distinguish.
- **`Parent` is an untrusted directory path; `Name` is untrusted and must be a single clean path element** — no separator, no `..`, not absolute, non-empty. Confinement to `$HOME` (via the reused `confineWorkdirToHomeCreating`) and the name-shape check are two independent, deterministic gates enforced by the dispatch handler (`internal/relay/handlers.CreateWorkspaceFolder`), not this layer: confinement alone does not guarantee the folder lands *directly* under `Parent` (a name like `sub/dir` stays inside `$HOME` yet escapes that guarantee). See [codebase/887.md](../codebase/887.md).
- **`WorkspaceFolderCreatedPayload.Path` is the canonical (symlink-resolved) absolute path of the created folder** — a fresh reply type, not a reuse of `ConversationUpdatedPayload` (there is no conversation row to project `name`/`cwd`/`last_used_at` from). Sent `in_reply_to` the request, requester only, no broadcast.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Same posture as the rest of the package.

Golden round-trip tests in `workspace_test.go` (`TestCreateWorkspaceFolderPayload_RoundTrip`, `TestWorkspaceFolderCreatedPayload_RoundTrip`) against `testdata/create_workspace_folder.json` / `testdata/workspace_folder_created.json` — this slice does **not** repeat `ChangeWorkspacePayload`'s round-trip-test gap.

### Recent-workspaces payloads (`workspace.go`, #888)

Body of `recent_workspaces` / `recent_workspaces_list` (`docs/protocol-mobile.md` § `recent_workspaces`). Appended to the same file as the `create_workspace_folder` payloads above (same domain — workspace wire messages), not a new file.

```go
type RecentWorkspacesPayload struct{}

type RecentWorkspacesListPayload struct {
    Workspaces []RecentWorkspace `json:"workspaces"`
}

type RecentWorkspace struct {
    Path       string    `json:"path"`
    LastUsedAt time.Time `json:"last_used_at"`
}
```

- **`RecentWorkspacesPayload` is empty by spec** — like `ListConversationsPayload`, it exists only so the dispatcher decodes a concrete value instead of a `json.RawMessage`.
- **`RecentWorkspacesListPayload.Workspaces` is always non-nil** (`make([]RecentWorkspace, 0, n)` at the handler), so an empty result marshals as `"workspaces":[]`, never `null`.
- **Neither `RecentWorkspace` field carries `omitempty`** — reply-side discipline: the client reads `path` and `last_used_at` on every row. `path` matches `WorkspaceFolderCreatedPayload.Path`; `last_used_at` matches `Conversation.LastUsedAt` / `ConversationSummary.LastUsedAt`.
- **Ordering is the source of truth, not a client-side sort key.** Entries are most-recent-first by the max `LastUsedAt` across the conversations sharing that `Cwd`, deduped so each distinct path appears exactly once — computed by `internal/relay/handlers.RecentWorkspaces`, not this layer. See [codebase/888.md](../codebase/888.md).
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Same posture as the rest of the package.

Golden round-trip tests in `workspace_test.go` (`TestRecentWorkspacesPayload_RoundTrip`, `TestRecentWorkspacesListPayload_RoundTrip`) against `testdata/recent_workspaces.json` / `testdata/recent_workspaces_list.json`.

### `Envelope`

The outer wire shape every application frame conforms to (`docs/protocol-mobile.md` § Message envelope, lines 177–201). Field order matches the spec table verbatim.

```go
type Envelope struct {
    ID        uint64          `json:"id"`
    Type      string          `json:"type"`
    TS        time.Time       `json:"ts"`
    Payload   json.RawMessage `json:"payload"`
    InReplyTo *uint64         `json:"in_reply_to,omitempty"`

    // EventID — durable per-conversation event id (eventring); #649.
    EventID *uint64 `json:"event_id,omitempty"`

    PayloadEncrypted bool `json:"payload_encrypted,omitempty"`
}
```

- `TS` is `time.Time` (not `string`) — the dispatcher needs typed time for the binary's 7-day-back / 5-min-forward clock-skew cap (spec § Clock-skew handling) without re-parsing on every read. Marshals as RFC 3339 nano; round-trip caveat: `time.Time` carries a monotonic-clock reading stripped by JSON marshal, so tests compare via `time.Time.Equal`, never `==` or `reflect.DeepEqual` (per `docs/PROJECT-MEMORY.md:1071`).
- `Payload` is `json.RawMessage` to enable deferred decode: the dispatcher reads `Type` from the outer envelope, then unmarshals `Payload` into the per-type struct that `Type` selects. Also lets a malformed payload of a known type fail-loud at `protocol.malformed` with the offending envelope's `id` intact, instead of failing the outer parse.
- `InReplyTo`, `EventID`, and `PayloadEncrypted` are `omitempty`. `payload_encrypted: false` MUST be omitted on the wire (the `envelope_full.json` fixture pins this).
- **`EventID *uint64` (#649)** is the durable, per-conversation event id from the `internal/eventring` ring (`eventring.Ring.Append`'s return) — distinct from `ID`, the per-conn envelope counter that resets each reconnect. It is stamped **only** by the interactive structured-stream emitter (`cmd/pyry/interactive_turn_v2.go`'s `emit`), so a reconnecting phone can advertise the latest one it saw as `last_event_id` (`HelloClientPayload.LastEventID`, #647); the inbound consumer that accepts and replays from it is sibling #647 (`security-sensitive`; daemon code not yet merged — see [codebase/647.md](../codebase/647.md)). **Pointer + `omitempty`, mirroring `InReplyTo` exactly:** every other `Envelope{...}` construction site (v1 messaging, dispatch, non-interactive) leaves it nil → omitted → byte-identical wire ("absent, not null/0"). Ring ids are always ≥ 1, so a non-nil pointer never encodes `0`. `TestEnvelope_EventIDOmitempty` pins the omit/round-trip shape; the unchanged `envelope_full.json` / `envelope_minimal.json` fixtures are the byte-stability regression guard. See [codebase/649.md](../codebase/649.md) and [eventring-package.md](eventring-package.md).

### `RoutingEnvelope`

The relay-prepended `{conn_id, frame}` wrapper used on the binary↔relay leg only (spec § Routing envelope, lines 100–122). Phones never see it. The relay strips it before forwarding to phones and prepends it before forwarding to the binary.

```go
type RoutingEnvelope struct {
    ConnID    string          `json:"conn_id"`
    Frame     json.RawMessage `json:"frame"`
    Token     string          `json:"token,omitempty"`        // #308; phone→binary, first frame per conn_id only
    CloseCode uint16          `json:"close_code,omitempty"`   // #308; binary→relay only
}
```

`Frame` is `json.RawMessage` so the relay can splice without parsing payloads — a structural property of the design (the relay holds zero per-user state). The `routing_envelope.json` round-trip test pins the byte-preservation invariant: a future change to typed `*Envelope` for `Frame` would surface as a fixture mismatch.

`Token` (#308) carries the phone's device-pairing token from the relay to the binary on the **first** frame for a given `ConnID` only. Empty on subsequent frames and on every binary→phone frame. Populated by the relay from the `x-pyrycode-token` HTTP header at WS upgrade; its original consumer, `relay.AuthenticateFirstFrame` via the dispatcher's `FirstFrameGate` (#308), was deleted by #1040 (zero production callers since #913 slice 1) — the field is retained on the wire type with no live consumer, its own removal deferred as separate follow-on scope (see [codebase/1040.md](../codebase/1040.md)). **SECURITY:** plaintext credential material — no layer may log it. `TestRoutingEnvelope_TokenOmitempty` pins the omitempty wire shape.

`CloseCode` (#308), when non-zero on a binary→relay routing envelope, asks the relay to forward `Frame` (if non-empty) to the phone and then close that phone's WS with this WS close code. Zero on every phone→binary frame; the dispatcher ignores `CloseCode` on inbound frames (a malicious relay cannot induce a self-close). Used today for the auth-reject path (4401); reserved for future binary-side close intents. `TestRoutingEnvelope_CloseCodeOmitempty` pins the omitempty wire shape.

## Handshake / control payloads (#271)

Five DTOs that slot into `Envelope.Payload (json.RawMessage)` once the dispatcher reads `Envelope.Type`. Pure data — no methods, no constructors, no validation. Spec source: `docs/protocol-mobile.md` § Message types — `hello`, `hello_ack`, `error`, `ack`.

```go
type HelloServerPayload struct {
    Role             string   `json:"role"` // always "server"
    ServerID         string   `json:"server_id"`
    BinaryVersion    string   `json:"binary_version"`
    ProtocolVersions []string `json:"protocol_versions"`
}

type HelloClientPayload struct {
    Role             string     `json:"role"` // always "client"
    DeviceName       string     `json:"device_name"`
    ClientVersion    string     `json:"client_version"`
    ProtocolVersions []string   `json:"protocol_versions"`
    LastSeenTS       *time.Time `json:"last_seen_ts,omitempty"`
    Token            string     `json:"token,omitempty"`        // #308; in-band device-pairing token under v2 (plaintext — MUST NOT be logged)
    Capabilities     []string   `json:"capabilities,omitempty"` // #607; phone's advertised feature set, e.g. [CapabilityInteractive]
    LastEventID      *uint64    `json:"last_event_id,omitempty"` // #647; durable event_id the phone last saw, for mid-turn reconnect replay (untrusted — consumer range/ring-bounds it)
}

type HelloAckPayload struct {
    ProtocolVersion string   `json:"protocol_version"`
    ServerID        string   `json:"server_id"`
    ConnID          string   `json:"conn_id"`
    Capabilities    []string `json:"capabilities,omitempty"` // #607; daemon's supported feature set (intersection with the phone's claim — enforced in #608)
}

type ErrorPayload struct {
    Code        string `json:"code"`
    Message     string `json:"message"`
    Retryable   bool   `json:"retryable"`
    RetryAfterS *int   `json:"retry_after_s,omitempty"`
}

type AckPayload struct{}
```

Conventions:

- **Two `Hello*Payload` structs, not a union.** The binary's hello and the phone's hello share only the envelope type name (`"hello"`) and dispatch site; field sets diverge. `role` is the discriminator. Modelling as a single struct with mostly-optional fields would lose type-level encoding of which fields belong with which role and force every consumer to validate role-field consistency by hand.
- **Optional fields are `*T` + `omitempty`; required fields are non-pointer.** Only `LastSeenTS` and `RetryAfterS` carry `omitempty`. `time.Time` zero-value as sentinel for `LastSeenTS` was rejected — `time.Time{}` marshals as `"0001-01-01T00:00:00Z"`, which would pollute the wire.
- **`AckPayload` is `struct{}`.** `json.Marshal(AckPayload{})` emits `{}` byte-for-byte, matching the spec's `"payload": {}`.
- **Field declaration order matches the spec example order.** The JSON encoder emits fields in struct-declaration order; that's what the round-trip byte-equivalence check verifies. Reordering breaks tests.
- **No constructors, no methods, no validation.** Runtime enforcement of `Role` discriminators (a phone sending `role: "server"`, etc.) is the dispatcher's concern (#248–#250). The `Role` constant is documented in struct comments only.
- **`Capabilities []string` is additive + `omitempty` (#607).** Both phone-facing hello payloads gained it: the phone advertises its understood features in `hello`, the daemon echoes its supported set in `hello_ack`. `omitempty` is the byte-identical lever — a nil/empty slice drops the key (absent, not `null`), so the unedited `hello_client.json` / `hello_ack.json` fixtures round-trip byte-identically (same precedent as `RoutingEnvelope.Token`). The single defined value is `CapabilityInteractive = "interactive"` (the wire-vocabulary constant lives in `handshake.go` next to the field). This is **advertisement only** — the daemon intersecting the phone's claimed set with its own (echoing only what *it* supports, never blindly mirroring) and the capability-gated fan-out are the consumer's trust decision (#608), not this layer's. `TestHelloClientPayload_CapabilitiesRoundTrip` / `TestHelloAckPayload_CapabilitiesRoundTrip` pin both the round-trip and the omit shape; the pre-existing fixture round-trips stay unchanged as the byte-stability regression guard.
- **`LastEventID *uint64` is additive + `omitempty` (#647).** The phone's inbound reconnect-replay cursor: the durable `event_id` (the `Envelope.EventID` #649 surfaces outbound) it last saw, advertised on mid-turn reconnect so the daemon replays the missed tail from the `internal/eventring` ring or emits a `resync` marker. **Pointer + `omitempty` is load-bearing** — ring ids are always ≥ 1, so a non-nil pointer never encodes `0` and a nil pointer is omitted; a phone advertising none keeps the v1 hello byte-identical (key absent, not `null`). Same shape as `LastSeenTS`. This wire-type layer does **no enforcement** — `LastEventID` is **untrusted remote input**, and the consumer (`internal/relay`, #647, `security-sensitive`) range/shape-validates it and bounds replay by the ring. `TestHelloClientPayload_LastEventIDRoundTrip` pins the omit/round-trip shape. **Implementation caveat:** the #647 daemon consumer carries an unresolved code-review MUST FIX and is not yet merged — see [codebase/647.md](../codebase/647.md). The wire field itself is stable.

Five fixture files under `testdata/` (one per type, each a complete `Envelope` with the payload inlined) drive five per-type `*_RoundTrip` tests in `handshake_test.go`. The tests reuse `readFixture` and `canonical` helpers from `envelope_test.go`. The byte-equivalence check (`canonical(out) == canonical(raw)`) is the load-bearing assertion; per-type field asserts exist to localise failure messages. The `hello_client.json` fixture's `last_seen_ts: "2026-05-08T08:14:02Z"` (no fractional seconds) pins the `time.RFC3339Nano` no-fractional round-trip behaviour.

Sibling payload slices not yet landed: messaging (`send_message` / `message`), conversations (`list_conversations` / `conversations` / `create_conversation` / `conversation_created` / `promote_conversation` / `conversation_updated`), push (`register_push_token`).

## Interactive event payloads (#607, #638, #1074)

The **v2 additive application events** — the wire representation of
`internal/turnevent`'s neutral turn-event model (#606). All eight are **binary →
phone only**, sent **only** to a phone whose `interactive` capability was echoed in
`hello_ack`; an old phone never sees them and keeps the coarse v1 `message`
fan-out. Spec source: `docs/protocol-mobile.md` § Interactive events. They map 1:1
to the `Type*` constants `TypeTurnState` / `TypeAssistantDelta` / `TypeToolUse` /
`TypeToolResult` / `TypeTurnEnd` (all #607), `TypeStall` (#638), and `TypeApiRetry`
/ `TypeCompacting` (#1074). The first five are the wire form of ACP-shaped turn
events; `stall`, `api_retry`, and `compacting` are the wire form of
**internal-only** signals (no ACP equivalent) — `stall` added in #638, the other
two in #1074 as PTY-derived status peers of `stall`.

```go
type TurnStatePayload struct {
    ConversationID string `json:"conversation_id"`
    State          string `json:"state"` // "thinking" | "responding" | "idle"
}

type AssistantDeltaPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    Seq            int    `json:"seq"`  // per-turn, non-negative, resets each turn
    Text           string `json:"text"` // coalesced chunk, not per-token
}

type ToolUsePayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    ToolUseID      string `json:"tool_use_id"`
    Name           string `json:"name"`
    InputSummary   string `json:"input_summary"` // human-readable précis, not raw input
}

type ToolResultPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    ToolUseID      string `json:"tool_use_id"` // matches the tool_use this completes
    IsError        bool   `json:"is_error"`
    ResultSummary  string `json:"result_summary"` // human-readable précis, not raw output
}

type TurnEndPayload struct {
    ConversationID string `json:"conversation_id"`
    TurnID         string `json:"turn_id"`
    StopReason     string `json:"stop_reason"` // turnevent.TurnEndReason values, verbatim
}

// #638 — the wire form of the internal-only turnevent.Stall onset marker.
type StallPayload struct {
    ConversationID string `json:"conversation_id"`
}

// #1074 — the wire form of turnevent.ApiRetry, a PTY-derived status peer of
// Stall. Active is the show (true) / clear (false) edge; Current/Total are the
// parsed `attempt N/M` counter ({0,0} when unparsed).
type ApiRetryPayload struct {
    ConversationID string `json:"conversation_id"`
    Active         bool   `json:"active"`
    Current        int    `json:"current"`
    Total          int    `json:"total"`
}

// #1074 — the wire form of turnevent.Compacting, a PTY-derived status peer of
// Stall. Banner-only: Active is the only field beyond ConversationID because
// tui-driver streams no compaction progress payload.
type CompactingPayload struct {
    ConversationID string `json:"conversation_id"`
    Active         bool   `json:"active"`
}
```

- **No `omitempty` on any field — the deliberate inverse of the handshake/optional
  discipline.** Every field is always present on the wire so the fixtures pin the
  full shape and boundary zero-values can't silently vanish: `assistant_delta` with
  `seq: 0` and `tool_result` with `is_error: false` are pinned exactly. Pick the tag
  by whether a field's absence is meaningful — here it never is.
- **`State` and `StopReason` stay plain `string`, not named enums.** Same
  `MessagePayload.Role` precedent: the closed-set guarantee belongs at the consumer,
  not in the wire type. `State` is documented (`thinking` / `responding` / `idle`)
  in the struct doc comment; #608 picks the exact internal-event → state mapping.
- **`StopReason` carries the `turnevent.TurnEndReason` strings verbatim** (`end_turn`
  / `max_tokens` / `max_turn_requests` / `refusal` / `cancelled`) **without
  importing `internal/turnevent`** — `protocol` stays a stdlib-only leaf, and #608
  produces the field via `string(turnevent.TurnEnd.Reason)`. The wire-value/taxonomy
  alignment is documented, not enforced by a shared type. ADR 025's base `turn_end`
  shape is `{conversation_id, turn_id}`; `stop_reason` is the #607 extension per the
  ticket title, following the "spec follows the code" convention (ADR 025
  § Consequences).
- **`Seq` is `int`, not `uint64`.** A per-turn counter that resets each turn (the
  package count-field idiom: `DebugBundleDonePayload.Total`); `uint64` is reserved
  for the session-monotonic `Envelope.ID`.
- **`StallPayload` (#638) carries `conversation_id` only — no `turn_id`.** Like
  `turn_state`, a stall is a coarse conversation-level signal, not turn-scoped. It
  is the wire form of the internal-only `turnevent.Stall` (an onset-only marker:
  no clearing field — the phone self-clears on the next turn activity). The
  internal `Stall` carries no identity, so the bridge (#608 / #624-B) supplies
  `ConversationID` at wire-mapping time. Same no-`omitempty` discipline as its five
  predecessors. `internal/protocol` does **not** import `internal/turnevent` — the
  two layers are decoupled, bridged only at the string value `"stall"`.
- **`ApiRetryPayload` / `CompactingPayload` (#1074) are PTY-derived status peers
  of `StallPayload`, not onset-only.** Unlike `stall`, both carry an explicit
  `active` clear edge (`false`) so a remote head can dismiss the indicator once
  claude recovers — `stall` has no such field because the phone self-clears it on
  the next turn activity instead. `ApiRetryPayload`'s `current`/`total` are the
  only screen-derived fields in this section: both are bounded ints the mapper
  reads from tui-driver's already-parsed `ApiRetryAttempt{Current, Total int}` —
  never a string, so raw banner/screen text is structurally unable to reach the
  wire. Same no-`omitempty`, no-`turn_id`, bridge-supplies-`ConversationID`
  discipline as `StallPayload`.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to
  every v1 slice. The intersection-of-capabilities trust decision, the
  internal-event → envelope mapping, and the capability-gated push all live in the
  consumer (#608).

Eight golden round-trip tests in `interactive_test.go` decode each fixture through
`Envelope` → `Envelope.Payload` → per-type struct, assert each field (incl. the
boundary `Seq == 0` / `IsError == false`, `StopReason == "end_turn"`, and the
`api_retry` fixture's non-zero `current`/`total`), then re-marshal
byte-equivalently. The shared `roundTripEnvelope` helper re-marshals the
**decoded payload struct** (not the original `RawMessage`) back into the envelope —
that is what pins struct → wire shape, since a missing or reordered json tag only
surfaces when the bytes are actually re-encoded (the original-`RawMessage`-passthrough
variant cannot catch it).

## Background-task event payloads (#1393; mapping wired #1394)

The v2 wire shape for the three **background-task events** — work claude starts that outlives the turn that started it (a `local_bash` command backgrounded on timeout). The first frames in the vocabulary whose subject is turn-independent: #1240 found `turn_state{idle}` emitted, and `turn_end.stop_reason == "end_turn"`, while a backgrounded command was still alive, with nothing on the wire to tell a client the two cases apart. `internal/streamsup/parser.go` produces the source `turnevent.BackgroundTask{Started,Updated,Roster}` variants (#1380/#1381/#1382); this ticket (#1393) gave them the wire shape below, 1:1 against `TypeBackgroundTaskStarted` / `TypeBackgroundTaskUpdated` / `TypeBackgroundTaskRoster` (their own const block in `codes.go`, next to `TypeUnrecognizedMessage`). #1394 wired `internal/turnbridge/outbound.go`'s `MapEvent` to actually emit them and wrote `docs/protocol-mobile.md` § `background_task_started` / `_updated` / `_roster` — see [codebase/1394.md](../codebase/1394.md).

```go
type BackgroundTaskStartedPayload struct {
    ConversationID  string   `json:"conversation_id"`
    TaskID          string   `json:"task_id"`
    ToolCallID      string   `json:"tool_call_id"`
    Description     string   `json:"description"`
    TaskType        string   `json:"task_type"`
    TruncatedFields []string `json:"truncated_fields"`
}

type BackgroundTaskUpdatedPayload struct {
    ConversationID  string   `json:"conversation_id"`
    TaskID          string   `json:"task_id"`
    Patch           string   `json:"patch"` // claude's patch object, WHOLE + unparsed — not json.RawMessage; truncation can leave it invalid JSON
    TruncatedFields []string `json:"truncated_fields"`
}

type BackgroundTaskRosterPayload struct { // custom MarshalJSON — see below
    ConversationID string           `json:"conversation_id"`
    Tasks          []BackgroundTask `json:"tasks"`
    DroppedTasks   int              `json:"dropped_tasks"`
}

type BackgroundTask struct { // roster row — no tool_call_id, no patch: the roster line carries neither
    TaskID          string   `json:"task_id"`
    TaskType        string   `json:"task_type"`
    Description     string   `json:"description"`
    TruncatedFields []string `json:"truncated_fields"`
}
```

- **`conversation_id` is present and unfilled by this ticket.** All nine pre-existing v2 interactive payloads carry it first; no `turnevent` variant carries one at all — the bridge (#1394) supplies it at mapping time, the same seam `StallPayload`/`ApiRetryPayload` use. **No `session_id` on any of the three** — claude's session identity is not the daemon's conversation identity.
- **`ToolCallID` names claude's `tool_use_id` in the daemon's own vocabulary** (matching `ToolUsePayload.ToolUseID`'s wire role), not claude's subtype name — the whole point of translating rather than passing claude's vocabulary straight through (one claude rename would otherwise break every client at once).
- **`TruncatedFields` (per record) and `DroppedTasks` (roster-level count) are load-bearing, not decoration.** A payload that dropped them would present claude's cut text as complete. Each rides where its dimension is decided: a text cut is a property of one entry, `DroppedTasks` is a property of the roster as a whole (`len(Tasks) + DroppedTasks` is the roster's true size) — so the roster payload carries no top-level `truncated_fields` of its own.
- **`Patch` is a plain `string`, never `json.RawMessage`** — `UnrecognizedMessagePayload.Raw`'s precedent. The producer truncates it at construction (`maxTaskPatch`), and a truncated JSON object is no longer valid JSON; typing it as raw JSON would lie to consumers and break marshalling. Pinned by a fixture carrying a deliberately unparseable fragment.
- **`BackgroundTaskRosterPayload.MarshalJSON` normalises a nil `Tasks` to `[]BackgroundTask{}`** so an empty roster always serialises `"tasks":[]`, never `"tasks":null` — the file's only custom marshaller. `omitempty` was out (AC #3 forbids eliding the key entirely); an empty roster is a *positive* signal ("nothing is alive," exactly #1240's missing case), and `[]` is the better client contract than `null` (no branch on a non-optional array type). `truncated_fields` stays un-normalised on purpose — nil and `[]` mean the same thing there, so there's no signal to protect.
- **No new truncation.** Every string is already byte-capped by the producer at construction (`internal/streamsup/parser.go`'s `maxTaskFieldID`/`maxTaskDescription`/`maxTaskPatch`/`maxTaskRosterDescription`, entry count `maxTaskRosterEntries`); a payload-side cap here would be dead code and would risk disagreeing silently with the producer's.
- **Per-field caps don't compose into an envelope guarantee — measured separately (AC #4).** `TestBackgroundTaskPayloads_FitV2EnvelopeCap` fills every field to its producer cap with `<` (not `a` — `encoding/json`'s default `SetEscapeHTML` turns `<`/`>`/`&` into 6-byte escapes, and an ASCII fill under-reports by ~5×) inside a populated `Envelope`, and asserts `< 65519` (a local literal, commented to `docs/protocol-mobile.md` § Application-envelope size cap — no exported production constant, since nothing in this package enforces the cap). Measured: roster (binding case, 8-entry cap) 50 557 B / 77.2%; started 45.6%; updated 40.8% — ~15 KB of headroom on the worst case.
- **Pure DTOs except the one marshaller above: no constructors, no `Validate()`.** Identical posture to every prior slice; accepting/rejecting a malformed frame is the v2 session manager's job, not this package's.

Four golden round-trip tests in `interactive_test.go` follow the `TestApiRetryPayload_RoundTrip` template, over `background_task_started.json` (all six fields, `description` containing `<`/`>`/`&` so the escaped form is visible in the fixture bytes), `background_task_updated.json` (`patch` a deliberately-truncated, unparseable fragment), `background_task_roster.json` (2+ entries, mixed `truncated_fields` — one populated, one `null`, `dropped_tasks` non-zero), and `background_task_roster_empty.json` (`"tasks":[]`, `dropped_tasks: 0` — the AC's key case). `TestBackgroundTaskRosterPayload_NilTasksNormalises` closes the gap a fixture structurally can't: unmarshalling `[]` always yields a non-nil slice, so the nil-`Tasks` path — the one #1394's bridge will actually take, since `turnevent.BackgroundTaskRoster.Tasks` is nil both for an empty roster and an omitted key — is only reachable by constructing the payload directly. See [codebase/1393.md](../codebase/1393.md) for the full implementation note, including one unfixed SHOULD FIX (the roster row's `SECURITY:` doc comment claims to repeat a warning it doesn't actually restate).

## Thinking-progress event payload (#1386)

The v2 wire shape for claude's **only mid-turn proof of life** on the stream-json surface. During a long assistant turn nothing else crosses the wire, so a phone showing "thinking" cannot separate a slow answer from a wedged session. `internal/streamsup`'s parser has translated claude's `system/thinking_tokens` line into `turnevent.ThinkingProgress{EstimatedTokens, EstimatedTokensDelta}` since #1385, rate-bounded at one event per 64 accumulated tokens (`streamsup.minThinkingTokensPerEvent`); this ticket (#1386) gave it wire shape **and** wired `internal/turnbridge/outbound.go`'s `MapEvent` in the same slice — unlike the background-task family's #1393/#1394 split, there was no reason to split a single two-int frame with no producer dependency gap.

```go
type ThinkingProgressPayload struct {
    ConversationID       string `json:"conversation_id"`
    EstimatedTokens      int    `json:"estimated_tokens"`
    EstimatedTokensDelta int    `json:"estimated_tokens_delta"`
}
```

- **Conversation-scoped, not turn-scoped — no `turn_id`, matching `StallPayload`/`ApiRetryPayload`.** The bridge supplies `ConversationID`; `turnevent.ThinkingProgress` carries no identity of its own. `tc.TurnID`/`tc.Seq` are read by `MapEvent`'s signature but ignored for this variant, the same posture as `Stall`/`ApiRetry`/`Compacting`/`Unrecognized`.
- **No `session_id`.** claude's session identity is not the daemon's conversation identity, and the parser drops `session_id`/`uuid` before the event exists (#1380/#1385) — the payload has no field capable of carrying either, so the leak this note exists to prevent is structurally impossible, not merely avoided.
- **No `TruncatedFields`, and its absence is a decision, not an omission.** Unlike every text-carrying sibling above (`BackgroundTask*`, `UnrecognizedMessagePayload`), this payload carries **no claude-authored text at all** — both fields are ints, nothing is ever cut, and a permanently-nil field would claim a bound that does not exist. Do not pattern-match the truncation discipline across; the architect's security review flagged this as the trap a spec author could fall into.
- **Both ints cross verbatim, including the zero value `{0,0}`.** A legitimate reading, exactly as `ApiRetryPayload`'s `{0,0}` is "count unknown" — no suppression branch, since a second filter here would silently diverge from the producer's own rate bound.
- **Two consumer hazards are documented once, not twice.** `EstimatedTokens` is not monotonic across a turn (it restarts near zero at every inference-request boundary) and the `EstimatedTokensDelta` values a client receives do not sum to the turn's total (the rate bound drops most lines and no field reports the residue). Both are measured with numbers in `turnevent.ThinkingProgress`'s doc comment, the single source of truth, and restated for a wire consumer in `docs/protocol-mobile.md` § `thinking_progress` — not here, to avoid a third copy drifting from the other two.
- **The wire name is the daemon's variant name, not claude's subtype.** `TypeThinkingProgress = "thinking_progress"`, never `thinking_tokens` or a string derived from it — `TestThinkingProgressType_IsNotClaudesSubtype` pins this with a substring check on the constant (scoped to the type only; the field names legitimately contain `tokens`), not just an exact-literal match, so a rename-by-pattern-matching regresses loudly.
- **Pure DTO: no methods, no constructor, no `Validate()`.** Same posture as every payload in this file.

One golden round-trip test in `interactive_test.go` over `testdata/thinking_progress.json`, plus the anti-drift substring check above. See [codebase/1386.md](../codebase/1386.md).

## Rate-limited event payload (#1405; mapping #1410)

The v2 wire shape for claude's **usage-limit report**. `internal/streamsup`'s parser has translated claude's top-level `rate_limit_event` line into a bounded `turnevent.RateLimited{Status, LimitType, ResetsAt, TruncatedFields}` since #1404 — the fifth mapping in the `turnevent` family and the first that is not a `system` subtype — but that variant was internal only, so a turn that stalled because of a usage limit had nothing on the wire saying why. #1405 gave it wire shape; unlike #1386's single-slice thinking-progress frame it follows the background-task family's #1393/#1394 split, so the mapping landed separately in **#1410** — `internal/turnbridge/outbound.go`'s `MapEvent` case plus `cmd/pyry/interactive_turn_v2.go`'s handler case, which is where this frame **started reaching an interactive mobile client**. Unlike #1394, #1410 did not touch `docs/protocol-mobile.md`, so the doc section landed with the shape instead of with the mapping.

```go
type RateLimitedPayload struct {
    ConversationID  string   `json:"conversation_id"`
    Status          string   `json:"status"`
    LimitType       string   `json:"limit_type"`
    ResetsAt        int64    `json:"resets_at"`
    TruncatedFields []string `json:"truncated_fields"`
}
```

- **Conversation-scoped, not turn-scoped — no `turn_id`, matching `ThinkingProgressPayload`/`StallPayload`/`ApiRetryPayload`.** The bridge (#1410) supplies `ConversationID`; `turnevent.RateLimited` carries no identity of its own. A usage-limit window is orthogonal to whichever turn happened to observe it, so receiving one neither opens nor closes a turn.
- **No `session_id`, no `uuid`.** Neither is the daemon's conversation identity, and — the family's #1380 precedent, reused by #1404's nested decode target — neither ever enters the decode target, so this payload cannot carry them even by accident. Pinned by `TestRateLimitedType_IsNotClaudesVocabulary`'s byte check on the marshalled payload.
- **Wire names track the daemon's field, not claude's key, except where the two coincide.** claude's keys are `status`, `rateLimitType`, `resetsAt`, nested under `rate_limit_info`. `limit_type` and `resets_at` are `turnevent.RateLimited`'s own field names in snake_case, so a claude rename does not move them. `status` coincides with claude's spelling, but it is the daemon's chosen name — it is what the producer's `bound()` reports it as — and a generic English word, not a vocabulary import.
- **`status`'s value set beyond the one measured-benign status (`"allowed"`) is unmeasured.** A plain `string`, not a closed enum, deliberately: no capture of a limit actually in force exists on any claude version on record, so the wire carries claude's raw string precisely so the set gets measured the first time a real limit fires. The producer's gate is loud in the same direction — the benign status is silent, any other non-empty status emits — so dropping `Status` from this payload would silence that signal one layer later. A client MUST NOT branch security-relevant behaviour on it.
- **`ResetsAt` is claude's number, not the daemon's clock, unbounded and unvalidated in both directions.** `int64` unix seconds, not `time.Time` — converting would invent a claim the bytes don't make. `0` means claude did not report it, not the epoch. A consumer must not assume it lies in the future or in a sane range at all; formatting it as a date without a range check is the realistic client bug.
- **`TruncatedFields` names wire fields, not Go fields, and the two possible members are forced by the producer.** `internal/streamsup/parser.go`'s two `bound()` calls literally construct the report with `"status"` and `"limit_type"`, in that order — any other JSON tag on those two payload fields would make the report name fields that don't exist on the wire. `nil` when nothing was cut, never `[]`, which is why — unlike `BackgroundTaskRosterPayload` — **this type has no `MarshalJSON`**: the nil-normalising guard exists for `Tasks`, where an empty roster is a positive statement, and does not generalise to a field whose whole meaning is "nothing was cut." The zero-value fixture (`rate_limited_zero.json`, `"truncated_fields":null`) is the enforcement mechanism, not a fixture-only convention — a guard added here would diverge the round-trip bytes and go red, confirmed by mutation (see [codebase/1405.md](../codebase/1405.md)).
- **No new cap.** Both strings are already byte-capped by the producer at construction (`internal/streamsup/parser.go`'s `maxRateLimitField = 256`); a payload-side cap here would be a second place the limit is decided, and the two could disagree silently.
- **No envelope-cap test, and the arithmetic says why.** Worst case is two 256 B strings at the 6-bytes-per-byte escape multiplier (3 072 B) plus an `int64`, a conversation id, and a closed two-member `truncated_fields` set (`bound()` appends nothing else, so it cannot grow) — roughly 3.3 KB, ~5% of the 65519 B v2 envelope cap. No producer change short of a twenty-fold `maxRateLimitField` increase could approach it, so `TestBackgroundTaskPayloads_FitV2EnvelopeCap`'s pattern is not repeated here.
- **SECURITY: `Status` and `LimitType` are claude-authored strings that crossed the subprocess trust boundary.** Safe to render as inert text; never fed to an HTML sink, an attribute, or a URL. The daemon bounds but does not sanitize them. The constraint follows the data onto the wire: this frame is a report, never a control input — a client must not key any behaviour on it.
- **Pure DTO: no methods, no constructor, no `Validate()`.** Same posture as every payload in this file.

Two golden round-trip tests in `interactive_test.go`: `TestRateLimitedPayload_RoundTrip` over `rate_limited.json` (populated; `status` deliberately set to the sentinel `<unmeasured>` rather than any captured value, since every capture on record reads the one silenced status, and the angle brackets double as an HTML-escaping pin), and `TestRateLimitedPayload_ZeroValue_RoundTrip` over `rate_limited_zero.json` (every field at its zero value, with byte-level guards that the fixture carries `"truncated_fields":null` and `"resets_at":0` rather than an elided key) — plus `TestRateLimitedType_IsNotClaudesVocabulary`, the anti-drift check. See [codebase/1405.md](../codebase/1405.md) for the full implementation note.

## Screen-snapshot payloads (#617)

The request/response pair behind ADR 025's always-available, parser-independent
**screen snapshot** — the floor of the safe-degradation strategy (ADR 025 § Safe
degradation). The phone may ask for a one-shot text picture of the current claude
screen at any time; because the snapshot depends on no screen parser it survives any
parser break and backs the stall fallback. Spec source: `docs/protocol-mobile.md`
§ Screen snapshot. The pair maps 1:1 to the `Type*` constants `TypeRequestSnapshot`
(phone → binary control) and `TypeScreenSnapshot` (binary → phone event).

```go
type RequestSnapshotPayload struct {
    ConversationID string `json:"conversation_id"`
}

type ScreenSnapshotPayload struct {
    ConversationID string    `json:"conversation_id"`
    Text           string    `json:"text"` // plain rendered text only; never raw control codes
    TS             time.Time `json:"ts"`
    Model          string    `json:"model"`  // #847: bootstrap session's per-session model override; "" = inherited default
    Effort         string    `json:"effort"` // #847: bootstrap session's per-session effort override; "" = inherited default
    YOLO           bool      `json:"yolo"`   // #847: bypass-permissions on/off; false = permissions enforced (fail-safe)
    UsedTokens     int       `json:"used_tokens"`   // #857: current context size on the latest usage-bearing entry, NOT a running total
    WindowTokens   int       `json:"window_tokens"` // #857: context-window size (200000 today); 0 = usage seam not wired
}
```

- **`request_snapshot` is an inbound v2 *control* envelope, not an application event.**
  Structurally like `TypeRekeyRequest`: the v2 session manager intercepts it at the
  dispatch boundary **before** `dispatch.Route`. There is **no `dispatch.Route`
  handler** for it — the doc comment says so explicitly so the next reader does not
  look for a handler that isn't there. The interception, the render via tui-driver,
  and the push of `screen_snapshot` back are the consumer ticket's job.
- **`ScreenSnapshotPayload.Text` is plain rendered text only, NEVER raw terminal
  control codes.** This is the load-bearing invariant: it preserves ADR 025's
  no-raw-bytes guarantee and the substrate seal — the snapshot is a literal-screen
  picture rendered to text, not a stream of escape sequences. The struct doc comment
  states this.
- **`TS` is `time.Time` (RFC3339Nano on the wire).** Records when the snapshot was
  rendered. The monotonic-clock reading strips on JSON marshal, so tests compare with
  `time.Time.Equal`, never `==` or `reflect.DeepEqual` — same discipline as
  `Envelope.TS` and every other `time.Time` payload field.
- **No `omitempty` on any field — the same deliberate inverse as the #607 interactive
  payloads.** Every field is always present on the wire so the fixtures pin the full
  shape and boundary values (an empty `conversation_id`, a zero `ts`) cannot silently
  vanish. `TestSnapshotPayloads_EmptyConversationID` pins the empty-`conversation_id`
  boundary for both payloads.
- **Pure DTOs: no methods, no constructors, no `Validate()`.** Identical posture to
  every prior slice. Accepting the inbound frame, rendering the screen, and returning
  content to the remote party are the consumer's trust decision — that consumer (the
  screen-snapshot handler child) carries the `security-sensitive` label; this leaf
  declaration does not.
- **#847 adds `Model`/`Effort`/`YOLO`, always present (no `omitempty`), after `TS`.**
  They reflect the bootstrap session's persisted `SessionSettings` (`sessions.Pool.DefaultSettings()`,
  [sessions-package.md § `Pool.DefaultSettings`](sessions-package.md)) so the phone can
  render the current model / reasoning-effort / permissions posture. Empty `Model`/`Effort`
  = inherited daemon default (no per-session override); `YOLO: false` = permissions
  enforced (the fail-safe default). Field order is load-bearing: `roundTripEnvelope`
  compares `json.Compact`ed bytes (key order preserved, not sorted), so the fixture's
  payload key order must match struct declaration order exactly — `screen_snapshot.json`
  carries representative non-default values (`model:"opus"`, `effort:"high"`, `yolo:true`).
  Shipped unwired here (the handler serialized the three fields at their zero values);
  wired by #848, which populates them from `Pool.DefaultSettings()` via the optional
  `SnapshotSettings` seam on `V2SessionConfig`
  ([v2-session-manager.md § Inbound screen-snapshot handler](v2-session-manager.md)).
  Not `security-sensitive` — read-only reflection of existing, non-secret session config.
  See [codebase/847.md](../codebase/847.md) and [codebase/848.md](../codebase/848.md).
- **#857 adds `UsedTokens`/`WindowTokens`, always present (no `omitempty`),
  after `YOLO`.** They reflect the bootstrap session's current context-window
  occupancy from [`internal/contextwindow.Read`](contextwindow-package.md)
  (#856): `UsedTokens` is the current context size on the transcript's latest
  usage-bearing entry (input + cache-read + cache-creation + output), **not**
  a running total — a post-compaction snapshot reports a smaller figure with
  no dedicated marker, since the reader is last-usage-wins. `WindowTokens` is
  the context-window size (200000 for every current model); `window_tokens:0`
  means the usage seam was not wired (foreground / unwired), so a client
  should treat "X of Y" as unavailable rather than divide by zero — this is
  distinct from a wired-but-fresh session, which reports `(0, 200000)`. The
  two fields are sufficient for a client to compute "N% used (X of Y)" as
  `used_tokens / window_tokens` (pyrycode-desktop#182). Shipped unwired at
  #856 (the handler serialized both fields at their zero values); wired by
  #857 via the optional `SnapshotUsage` seam on `V2SessionConfig`
  ([v2-session-manager.md § Inbound screen-snapshot handler](v2-session-manager.md)).
  Not `security-sensitive` — read-only reflection of two non-secret aggregate
  integers; the transcript content itself never crosses the wire.
  See [codebase/856.md](../codebase/856.md) and [codebase/857.md](../codebase/857.md).

Two golden round-trips in `snapshot_test.go` decode each fixture through `Envelope`
→ `Envelope.Payload` → per-type struct and re-marshal byte-equivalently via the shared
`roundTripEnvelope`. `screen_snapshot.json` carries a **multi-line** `text`
(`"line one\nline two\nline three\n"`, asserted with `strings.Contains(…, "\n")`) so
the canonical compare pins the escaped multi-line shape, and a whole-second payload
`ts` (`"2026-05-08T10:33:14Z"`) so the re-marshal is byte-identical — `time.Time`'s
RFC3339Nano output trims trailing fractional zeros, so a `.120Z` value would re-emit
as `.12Z` and break the compare (the same fixture-`ts` gotcha that governs the
envelope's own timestamp).

## Predicate: `IsKnownAppType`

```go
func IsKnownAppType(env Envelope) error
```

Returns:
- `nil` when `env.Type` is in the v1 type set and `env.PayloadEncrypted` is false.
- `ErrUnsupported` when `env.PayloadEncrypted` is true (reserved for v2; spec § Reserved for v2, lines 684–699).
- `ErrUnknownType` when `env.Type` is empty or not in the v1 set.

**Check order is pinned: `PayloadEncrypted` first, `Type` second.** A frame failing both checks reports as `ErrUnsupported` — the stricter rejection wins. The order is observable through `errors.Is` at the call site; the truth-table test row `encrypted-with-unknown-type` pins it.

`inboundAppTypeSet` is a package-private `map[string]bool` initialised at package init from the 16 `Type*` constants. The map is read-only after init; concurrent reads of an unmutated Go map are race-free per the Go memory model.

### What the predicate does NOT validate

- `Envelope.ID` non-zero or monotonic — connection-state, not framing.
- `Envelope.TS` skew bounds — clock-skew enforcement is the dispatcher's.
- `Envelope.Payload` shape — owned by the per-type structs (#256).
- `InReplyTo` references a real prior `id` — connection-state.
- Role-restricted types (e.g. a phone sending `hello_ack`) — dispatch concern.

These exclusions are restated in the predicate's doc-comment so a future regression can't widen the surface by accident.

## Sentinels and wire-code mapping

```go
var (
    ErrUnknownType = errors.New("protocol: unknown envelope type")
    ErrUnsupported = errors.New("protocol: unsupported envelope feature")
)
```

The package returns Go sentinels; **the dotted-string wire codes live at the call site**, not here. This follows the convention pinned in `docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the consumer's job, NOT the primitive's." `internal/conversations` already exports `ErrConversationNotFound` / `ErrConversationAlreadyPromoted` and lets the consumer (CLI, wire layer) map them. `internal/protocol` follows the same idiom.

Returning `string` from `IsKnownAppType` would couple this package to wire format. The cost of the convention is a single switch at the dispatcher (#248):

```go
if err := protocol.IsKnownAppType(env); err != nil {
    code := protocol.CodeProtocolMalformed
    switch {
    case errors.Is(err, protocol.ErrUnsupported):
        code = protocol.CodeProtocolUnsupported
    case errors.Is(err, protocol.ErrUnknownType):
        code = protocol.CodeProtocolUnknownType
    }
    return sendError(env.ID, code, err.Error(), false)
}
```

Sentinel error strings carry no input bytes and no payload contents — a malformed-envelope error returned upward never leaks token-shaped or PII-shaped data via the message.

## Constants (`codes.go`)

### Error codes (14)

Wire values for the `code` field of error payloads (spec § Error codes, lines 525–542). Naming convention: `Code<Category><Reason>` mirrors the dotted-string `category.reason` shape.

| Constant | Wire string |
|----------|-------------|
| `CodeProtocolUnknownType` | `protocol.unknown_type` |
| `CodeProtocolMalformed` | `protocol.malformed` |
| `CodeProtocolUnsupported` | `protocol.unsupported` |
| `CodeAuthInvalidToken` | `auth.invalid_token` |
| `CodeAuthTokenRevoked` | `auth.token_revoked` |
| `CodeServerBinaryOffline` | `server.binary_offline` |
| `CodeServerBinaryBusy` | `server.binary_busy` |
| `CodeConversationNotFound` | `conversation.not_found` |
| `CodeConversationAlreadyPromoted` | `conversation.already_promoted` |
| `CodeMessageTooLong` | `message.too_long` |
| `CodeRelayNoServer` | `relay.no_server` |
| `CodeRelayServerIDConflict` | `relay.server_id_conflict` |
| `CodeSessionNotFound` | `session.not_found` |
| `CodeSessionBlocked` | `session.blocked` |

### Envelope types

Wire values for `Envelope.Type` (spec § Message types). Two architectural partitions: 16 v1 application types (closed; consumed by `dispatch.Route` via `inboundAppTypeSet`; 13 from the original #256 catalog — its three v1 bulk-history backfill types were removed as dead code in #967, see [codebase/967.md](../codebase/967.md) — + `TypeRenameConversation` #820 + `TypeDeleteConversation` / `TypeConversationDeleted` #822) and the **v2-only** set whose members are **deliberately NOT** in `inboundAppTypeSet`. The v2-only set itself spans two flavours: **inbound control envelopes** (`TypeRekeyRequest` (#454), `TypeRequestSnapshot` (#617), `TypeModalAnswer` / `TypeModalCancel` (#701), `TypeDequeueMessage` (#720), and `TypeInterrupt` (#707)), intercepted at the v2 dispatch boundary (`internal/relay/v2session.go`'s `dispatchAppFrame`) before `dispatch.Route` is called; and **outbound binary → phone events** never dispatched inbound (the five #607 interactive types, `TypeStall` (#638), `TypeApiRetry` / `TypeCompacting` (#1074), `TypeUnrecognizedMessage`, `TypeScreenSnapshot` (#617), `TypeResync` (#647), `TypeSessionTransition` (#656), `TypeModalShown` / `TypeModalDismissed` (#701), `TypeQueueState` (#720), and `TypeSessionError` (#1007, unwired — no producer yet)). Adding either to `inboundAppTypeSet` would silently route the envelope to the v1 handler chain (or expose it to an old phone) — exactly the opposite of what's wanted.

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

`TypeRekeyRequest` carries the doc-comment load-bearing instruction "MUST NOT be added to `inboundAppTypeSet` in `internal/protocol/envelope.go`"; a companion doc-comment **above** `inboundAppTypeSet` names `TypeRekeyRequest` as the canonical example of a v2-only type that must stay out; the interactive block carries the same MUST-NOT instruction. The advisory comments form the stochastic-rule rails; the deterministic rail is `TestTypeConstants_V1V2Partition` in `compat_test.go` (see drift detectors below).

## Drift detectors

The v1 type list appears three times: in the `Type*` constants block (`codes.go`), in the `inboundAppTypeSet` map literal (`envelope.go`), and in two test slices (`compat_test.go`). The triple-copy is **deliberate** — explicit drift detectors fail loudly in CI when a new constant lands without the corresponding map entry:

- `TestIsKnownAppType` — runs every v1 `Type*` constant through `IsKnownAppType` and asserts `nil` (catches "added a v1 `Type*` const, forgot the map").
- `TestInboundAppTypeSet_CoversAllExportedTypeConstants` — asserts every v1 application `Type*` constant is keyed in `inboundAppTypeSet`.
- `TestTypeConstants_V1V2Partition` (#454, extended #607/#617/#638/#647/#656/#701/#720/#707/#812/#813/#831/#844/#1007/#1074/#1393/#1386/#1405) — every exported `Type*` constant must be in `inboundAppTypeSet` **OR** in the test-local `v2OnlyTypes` allowlist; never both, never neither. The allowlist now holds thirty-two entries (`TypeRekeyRequest` + the five interactive types + `TypeStall` + `TypeApiRetry` / `TypeCompacting` (#1074) + the two snapshot types + `TypeResync` + `TypeSessionTransition` + the four modal types + the two queue types + `TypeInterrupt` + the two debug-bundle-stream types + `TypeRequestDebugBundle` + `TypeNewSession` + the two set-session-settings types + `TypeSessionError` + the three background-task types (#1393) + `TypeThinkingProgress` (#1386) + `TypeRateLimited` (#1405)), so the partition size assertion is `len(inboundAppTypeSet) + len(v2OnlyTypes) == 16 + 32 == 48` (inboundAppTypeSet's contribution reduced by the three backfill types removed in #967). Forces a future contributor adding any v2-only type to amend the allowlist explicitly — adding a `Type*` constant without partitioning it fails the build. The `v2OnlyTypes` literal lives in the test rather than as an exported production symbol so production callers cannot accidentally import it for dispatch logic — v2 dispatch switches on individual constants, not on partition membership. **A second, independent drift detector lives outside this package**: `cmd/pyry/relay_guard_test.go`'s `TestEveryInboundV2TypeHasHandler` classifies every `Type*` constant into a handler bucket or `excludedTypes` (outbound-only pushes get `"push"`); #1074's spec named only the `compat_test.go` trio, and the developer had to discover this second detector at build time — see [codebase/1074.md](../codebase/1074.md) § Lessons learned. #1393 registered its three types in both detectors from the start; #1386 added its `excludedTypes["TypeThinkingProgress"] = "push"` entry in the same commit as the constant; #1405 did the same for `TypeRateLimited`, mandatory from the moment the constant exists rather than from the moment #1410 emitted it.
- `TestErrorCode_Constants_MatchSpec` — exact-string match for each `Code*` constant against the spec's dotted string. Catches the "fat-fingered `protocol.unkown_type`" regression at the lowest possible cost.

Reflection over `go/types` was considered and rejected — heavier than explicit assertions for a closed set. If the v1 type set ever grows past ~50 entries (no plausible path under the protocol's versioning policy), revisit.

## Concurrency

Pure-data package. No goroutines, no locks, no shared-mutable state. `IsKnownAppType` is a pure function: same input, same output, allocation-free on the rejection path (returns one of three pre-existing values: `nil`, `ErrUnsupported`, `ErrUnknownType`). `inboundAppTypeSet` is initialised at package init and never mutated.

## What's deliberately NOT in the package

- `Envelope.Validate()` method or `NewEnvelope(...)` constructor — `Envelope` has no construction-time invariants (every field independently settable, optional fields zero-valuable). Struct-literal-with-named-fields is the canonical shape.
- `AllV1Types []string` exported slice — no consumer needs it; YAGNI.
- `go:generate`-driven membership check — overkill for a 16-entry closed set.
- A `[]string` slice + linear scan for membership — duplicates the constant names twice (slice + constants); the map literal duplicates them once at the same indentation as the constants block, making drift visible at code review.
- Per-type payload structs beyond the now-complete #256 catalog — `RegisterPushTokenPayload` (#275), the two messaging payloads (#272; its three backfill-flow payloads were removed as dead code in #967), the conversations-read pair plus row type (#273), the four conversations-write payloads (#274), and the handshake/control payloads (#271). All slices are wired; no more #256 sub-tickets pending.
- **The interactive bridge, push, and capability trust decision (#607's consumer surface)** — mapping `turnevent` events → the five interactive payloads, the actual push/fan-out, and the daemon intersecting the phone's advertised capabilities with its own supported set all live in #608, never in this leaf package. `interactive.go` is wire vocabulary only.
- **The screen-snapshot intercept, render, and push (#617's consumer surface)** — intercepting `request_snapshot` at the v2 dispatch boundary (before `dispatch.Route`), rendering the current screen to text via tui-driver, and pushing `screen_snapshot` back all live in the consumer (the screen-snapshot handler child, which carries `security-sensitive`), never in this leaf package. `snapshot.go` is wire vocabulary only; the trust boundary — accepting a remote inbound frame and returning rendered screen content — is the consumer's, not this declaration's.
- **The modal control-loop runtime (#701's consumer surface)** — the four modal wire types landed in #701 as wire vocabulary (`ModalShownPayload` / `ModalAnswerPayload` / `ModalCancelPayload` / `ModalDismissedPayload` above). The runtime that mints `modal_id` nonces, emits `modal_shown`, intercepts `modal_answer` / `modal_cancel` at `dispatchAppFrame` before `dispatch.Route` (→ tui-driver keystroke), dedups by `answer_token`, and runs deny-on-timeout is #703 (with #706 two-heads ownership, #702 the per-device answer gate) — all `security-sensitive`, never in this leaf package. `messaging.go`'s modal structs are wire vocabulary only.
- **The background-task bridge (#1393's consumer surface)** — the three background-task wire types landed in #1393 as wire vocabulary (`BackgroundTaskStartedPayload` / `BackgroundTaskUpdatedPayload` / `BackgroundTaskRosterPayload` + `BackgroundTask` above), deliberately split from the mapping so #1394 never had to reopen this file — and it didn't: #1394 wired `internal/turnbridge/outbound.go`'s `MapEvent` and `docs/protocol-mobile.md` § background_task_* without touching any file in this leaf package. See [codebase/1394.md](../codebase/1394.md). (`thinking_progress` (#1386) took the opposite, #1074-style call: one frame, no producer dependency gap, so vocabulary and mapping shipped together rather than split — see [Thinking-progress event payload](#thinking-progress-event-payload-1386).)
- **The rate-limited bridge (#1405's consumer surface)** — `RateLimitedPayload` above landed in #1405 as wire vocabulary only, following the background-task split rather than `thinking_progress`'s ship-together call. `internal/turnbridge/outbound.go`'s `MapEvent` case for `turnevent.RateLimited`, plus `cmd/pyry/interactive_turn_v2.go`'s handler case and the missing test for its `eventKind` arm (a #1404 code-review SHOULD FIX — the arm itself shipped in #1404, only its test was outstanding), landed in **#1410**. Both proof tiers for the gate's silence — the hermetic e2e feeding the captured benign `rate_limit_event` plus its non-benign arrival control, and the live standing sentinel in `drainForCompletedTurn` (#1404's Open Question 1) — landed in **#1411**. None of them is this leaf package.
- **Other v2 event/control types** — `queue_state` and the remaining phone → binary control verbs (`interrupt`, …) are deliberately out of scope here; they belong to other #596 children and Phase 3 (#597). (`request_snapshot` / `screen_snapshot` landed in #617 as wire vocabulary; their consumer is the separate `security-sensitive` ticket above. The `stall` event landed in #638 as wire vocabulary — `StallPayload` above; its bridge consumer, mapping tui-driver's `stall_detected` → `turnevent.Stall` → `stall` and gating the fan-out, is #624-B, which carries `security-sensitive`. The `api_retry` / `compacting` status peers landed in #1074 as wire vocabulary AND their full bridge consumer in the same ticket — `ApiRetryPayload` / `CompactingPayload` above; unlike `stall`'s split, #1074 threaded all five layers — mapper, outbound adapter, and `cmd/pyry` fan-out — in one slice, `security-sensitive` throughout.)
- WS close codes (`1000`/`1011`/`4401`/`4404`/`4409`) — transport concern, lives with #247 (WSS dial+handshake).
- Auth/dispatch wiring (`hello_ack`-on-connect, role-based type restriction) — #248–#250.
- A `Validate(*Envelope)` that gates on payload shape, ID monotonicity, or TS skew — those are dispatcher obligations, named in the predicate's doc-comment as out-of-scope.

## Security posture

Trust boundary is `json.Unmarshal` at the dispatcher. After the unmarshal succeeds, an `Envelope` value is "structurally well-formed JSON" but **not** "semantically validated." `IsKnownAppType` is the next gate after unmarshal, but it intentionally checks only the framing bits (`PayloadEncrypted`, `Type`).

Downstream obligations the dispatcher (#248) inherits:
- Apply a max-frame-size cap at the WS read boundary BEFORE this package's types are constructed; unbounded `[]byte` reaching `json.Unmarshal` is a DoS vector.
- Run `IsKnownAppType` on every decoded envelope.
- Decode `Payload` against the per-type struct selected by `Type` (#256 catalog).
- Enforce clock-skew caps on `TS`.
- Track `ID` monotonicity per connection.
- Never log `Envelope.Payload` (may contain tokens or PII).

The `payload_encrypted: true` v2 reservation is rejected via `ErrUnsupported` before any v2-shaped data could be processed.

## Consumers (deferred)

No production consumers in this slice. Future:
- `internal/relay-client` (binary→relay WS connection) — marshals `Envelope`, wraps in `RoutingEnvelope` for the relay leg.
- `internal/dispatch` (#248) — calls `IsKnownAppType`, maps sentinels to wire codes, decodes per-type payloads from #256's catalog.
- `cmd/pyry-relay` (future) — splices `RoutingEnvelope.Frame` byte-for-byte without parsing.
- Mobile clients — consume the JSON wire format directly (no Go binding); the test fixtures under `testdata/` double as the cross-language schema reference.

## Related

- Spec: `docs/protocol-mobile.md` — single source of truth for field names, optionality, wire semantics
- Convention: `docs/PROJECT-MEMORY.md` § "Refusal-to-wire-code mapping is the consumer's job"
- Sentinel-pattern precedent: `internal/conversations` (`ErrConversationNotFound` etc.)
- [ADR 025](../decisions/025-mobile-remote-head-interactive-session.md) — § Decision 2 (v2-additive + capability negotiation) and § Wire-protocol extension; the decision the #607 interactive payloads + `capabilities` field implement. § Safe degradation pins the parser-independent screen-snapshot floor the #617 `request_snapshot` / `screen_snapshot` pair backs.
- [turnevent-package.md](turnevent-package.md) — the neutral internal turn-event model (#606) the interactive payloads are the wire representation of; `stop_reason` carries its `TurnEndReason` strings verbatim
- [codebase/967.md](../codebase/967.md) — the #967 implementation note (removal of the dead v1 bulk-history backfill flow: `BackfillSincePayload` / `MessageChunkPayload` / `BackfillDonePayload`, the `TypeBackfillSince` / `TypeMessageChunk` / `TypeBackfillDone` constants, and their `inboundAppTypeSet` entries)
- [codebase/607.md](../codebase/607.md) — the #607 implementation note (interactive payloads + capabilities negotiation)
- [codebase/617.md](../codebase/617.md) — the #617 implementation note (screen-snapshot wire types + v2 partition)
- [codebase/638.md](../codebase/638.md) — the #638 implementation note (the `stall` wire type + its internal-only `turnevent.Stall` peer; the sixth member of the v2 interactive partition)
- [codebase/1074.md](../codebase/1074.md) — the #1074 implementation note (`api_retry` / `compacting` PTY-derived status-peer wire types + their bridge consumer, threaded through all five layers in one ticket; the second `cmd/pyry`-side drift detector discovered as a lesson learned)
- [codebase/844.md](../codebase/844.md) — the #844 implementation note (`set_session_settings` / `session_settings_updated` wire vocabulary + the presence-contract design)
- [codebase/845.md](../codebase/845.md) — the #845 implementation note (the daemon-side handler: capability gate, wire-boundary model/effort validation, `SettingsUpdater` seam, deterministic reply)
- [codebase/649.md](../codebase/649.md) — the #649 implementation note (the additive `Envelope.EventID *uint64` field surfacing the eventring durable id on the interactive stream; producer half of mid-turn reconnect)
- [codebase/647.md](../codebase/647.md) — the #647 implementation note (`HelloClientPayload.LastEventID` + `TypeResync`; the inbound reconnect-replay consumer — `security-sensitive`, carries an unresolved code-review MUST FIX, not yet merged)
- [codebase/656.md](../codebase/656.md) — the #656 implementation note (the `session_transition` wire type; the vocab→producer split this slice and #701 both mirror)
- [codebase/701.md](../codebase/701.md) — the #701 implementation note (the four modal wire types + `ModalOption`; the `modal_id` nonce / `answer_token` idempotency contract; `security-sensitive` for the wire-shape security pass, producers #703/#706/#702)
- [codebase/1393.md](../codebase/1393.md) — the #1393 implementation note (three background-task wire types + `BackgroundTask` row; the nil→`[]` roster normalisation and why a fixture alone can't pin it; the envelope-cap measurement; one unfixed SHOULD FIX)
- [codebase/1394.md](../codebase/1394.md) — the #1394 implementation note (the `MapEvent` bridge + `interactive_turn_v2.go` handler/`eventKind` arms that wire #1393's vocabulary to the mobile wire; the `docs/protocol-mobile.md` § background_task_* write-up and the two-literal envelope-count correction)
- [codebase/1405.md](../codebase/1405.md) — the #1405 implementation note (`TypeRateLimited` + `RateLimitedPayload`, wire vocabulary only; the forced `status`/`limit_type` wire names; why no `MarshalJSON` guard; the `docs/protocol-mobile.md` § `rate_limited` write-up and the two-literal envelope-count correction, landing with the shape rather than the mapping)
- [turnevent-package.md](turnevent-package.md) / [streamsup-package.md](streamsup-package.md) — `turnevent.BackgroundTask{Started,Updated,Roster}` (#1380/#1381/#1382) and `turnevent.RateLimited` (#1404), the source these payloads mirror, and the `system`-subtype / top-level-line parser that produces them
- [codebase/1240.md](../codebase/1240.md) — the live-probe finding this vocabulary exists to fix (`turn_state{idle}` while a backgrounded command is still alive; `stop_reason` carries no signal for it)
- [codebase/1404.md](../codebase/1404.md) — the #1404 implementation note (`rate_limit_event`'s gated mapping to `turnevent.RateLimited`; the three-rung gate; the `session_id`/`uuid`/`overage*` exclusions this payload inherits)
- Future consumers: `internal/dispatch` (#248), `internal/relay-client`, the interactive event-stream bridge + capability enforcement (#608), the screen-snapshot handler (intercept `request_snapshot` + render + push `screen_snapshot`; `security-sensitive`). The background-task bridge mapping `turnbridge.MapEvent` for the three #1393 variants shipped in #1394. #1410 wired the equivalent bridge case for #1405's `TypeRateLimited`.
