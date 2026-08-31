# `internal/protocol` — wire-format envelope, routing, error codes, v1 predicate

Pure-data leaf package. Declares the wire-format types for the mobile WebSocket protocol v1 — outer envelope, relay↔binary routing wrapper, error-code constants, type-name constants, and the `IsKnownAppType` predicate. No I/O, no goroutines, no `context`, no `slog`. Spec source-of-truth is `docs/protocol-mobile.md`.

Landed in #255. Per-type payload structs (the catalog the 16 type discriminators select) are #256 sibling tickets and slot into `Envelope.Payload (json.RawMessage)` via a second-pass `json.Unmarshal` at the dispatcher; first slice (`RegisterPushTokenPayload`) landed in #275, second slice (messaging payloads) landed in #272 — it also shipped the v1 bulk-history backfill payloads (`backfill_since` / `message_chunk` / `backfill_done`), removed as dead code (zero emitters, zero handlers) in #967 — third slice (conversations-read payloads) landed in #273, fourth slice (conversations-write payloads) landed in #274, fifth slice (handshake/control: `HelloServerPayload` / `HelloClientPayload` / `HelloAckPayload` / `ErrorPayload` / `AckPayload`) landed in #271.

## Types

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

## Concurrency

Pure-data package. No goroutines, no locks, no shared-mutable state. `IsKnownAppType` is a pure function: same input, same output, allocation-free on the rejection path (returns one of three pre-existing values: `nil`, `ErrUnsupported`, `ErrUnknownType`). `inboundAppTypeSet` is initialised at package init and never mutated.

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


## Sections

This overview is split across the documents below. Each is kept small so
search can reach it.

- [Files](protocol-package-files.md) — internal/protocol/ ├── envelope.go Envelope, RoutingEnvelope, ErrUnknownType / ErrUnsupported, IsKnownAppType, inboundAppTypeSet ├──…
- [`RegisterPushTokenPayload` (#275)](protocol-package-types-registerpushtokenpayload.md) — Body of a `register_push_token` frame (`docs/protocol-mobile.md` § Message types → `register_push_token`). 
- [Messaging payloads (#272)](protocol-package-types-messaging-payloads.md) — Bodies of the two conversation-flow envelopes (`docs/protocol-mobile.md` § Message types → `send_message` / `message`). 
- [Session-transition payload (#656)](protocol-package-types-session-transition-payload.md) — Body of an `Envelope` whose `Type == TypeSessionTransition` (`docs/protocol-mobile.md` § session_transition). 
- [Modal v2 wire payloads (#701)](protocol-package-types-modal-v2-wire-payloads.md) — The wire vocabulary for a **modal** the supervised `claude` surfaces over the encrypted mobile wire (`docs/protocol-mobile.md` § Modal;…
- [Queue v2 wire payloads (#720)](protocol-package-types-queue-v2-wire-payloads.md) — The wire vocabulary for the **queued-message backlog** over the encrypted mobile wire (`docs/protocol-mobile.md` § Queue; epic #597 Phase…
- [Debug-bundle streaming payloads (#812)](protocol-package-types-debug-bundle-streaming-payloads.md) — The byte-generic wire bodies for streaming a large debug bundle over the encrypted mobile channel (`docs/protocol-mobile.md` § Debug…
- [Session settings payloads (#844)](protocol-package-types-session-settings-payloads.md) — The wire vocabulary for changing a session's per-session model / reasoning effort / YOLO (`docs/protocol-mobile.md` § Session settings;…
- [Session settings read payloads (#491/#1214, `ConversationID` field #1586, conversation-keyed reply #1610)](protocol-package-types-session-settings-read-payloads.md) — The READ half the #844 cluster shipped without: `set_session_settings` changes the values and `session_settings_updated` only echoes the id…
- [Conversations-read payloads (#273)](protocol-package-types-conversations-read-payloads.md) — Bodies of the conversation-listing request/response pair (`docs/protocol-mobile.md` § Message types → `list_conversations` /…
- [Conversations-write payloads (#274, + `RenameConversationPayload` #820, + `DeleteConversationPayload` / `ConversationDeletedPayload` #822, + `ArchiveConversationPayload` #881, + `ChangeWorkspacePayload` #823)](protocol-package-types-conversations-write-payloads.md) — Bodies of the conversation create/promote/rename/delete/archive/change-workspace lifecycle (`docs/protocol-mobile.md` § Message types →…
- [Workspace-folder payloads (`workspace.go`, #887)](protocol-package-types-workspace-folder-payloads-workspace-go.md) — Body of `create_workspace_folder` / `workspace_folder_created` (`docs/protocol-mobile.md` § `create_workspace_folder`). 
- [`Envelope`](protocol-package-types-envelope.md) — The outer wire shape every application frame conforms to (`docs/protocol-mobile.md` § Message envelope, lines 177–201). 
- [`RoutingEnvelope`](protocol-package-types-routingenvelope.md) — The relay-prepended `{conn_id, frame}` wrapper used on the binary↔relay leg only (spec § Routing envelope, lines 100–122). 
- [Handshake / control payloads (#271)](protocol-package-handshake-control-payloads.md) — Five DTOs that slot into `Envelope.Payload (json.RawMessage)` once the dispatcher reads `Envelope.Type`. 
- [Interactive event payloads (#607, #638, #1074)](protocol-package-interactive-event-payloads.md) — The **v2 additive application events** — the wire representation of `internal/turnevent`'s neutral turn-event model (#606). 
- [Background-task event payloads (#1393; mapping wired #1394)](protocol-package-background-task-event-payloads.md) — The v2 wire shape for the three **background-task events** — work claude starts that outlives the turn that started it (a `local_bash`…
- [Thinking-progress event payload (#1386)](protocol-package-thinking-progress-event-payload.md) — The v2 wire shape for claude's **only mid-turn proof of life** on the stream-json surface. 
- [Rate-limited event payload (#1405; mapping #1410)](protocol-package-rate-limited-event-payload.md) — The v2 wire shape for claude's **usage-limit report**. 
- [Model-list payload (#1704 shape, #1705 fixtures + docs; producer #1848/#1849, second production path #1857)](protocol-package-model-list-payload.md) — The v2 wire shape for claude's **model inventory** — the `models` array a `control_request` with subtype `initialize` returns on the…
- [Slash-command-list payload (#1727 shape, #1718 fixtures + docs; producer #1720)](protocol-package-slash-command-list-payload.md) — The v2 wire shape for claude's **slash-command inventory** — the `commands` array the same `initialize` control reply carries alongside…
- [Screen-snapshot payloads (#617)](protocol-package-screen-snapshot-payloads.md) — The request/response pair behind ADR 025's always-available, parser-independent **screen snapshot** — the floor of the safe-degradation…
- [Error codes (21)](protocol-package-constants-codes-go-error-codes-21.md) — Wire values for the `code` field of error payloads (spec § Error codes). 
- [Envelope types](protocol-package-constants-codes-go-envelope-types.md) — Wire values for `Envelope.Type` (spec § Message types). 
- [Drift detectors](protocol-package-drift-detectors.md) — The v1 type list appears three times: in the `Type*` constants block (`codes.go`), in the `inboundAppTypeSet` map literal (`envelope.go`),…
- [What's deliberately NOT in the package](protocol-package-what-s-deliberately-not-in-the-package.md) — see the document
- [Related](protocol-package-related.md) — see the document
