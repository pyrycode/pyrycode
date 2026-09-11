# Modal v2 wire payloads (#701)

The wire vocabulary for a **modal** the supervised `claude` surfaces over the
encrypted mobile wire (`docs/protocol-mobile.md` § Modal; epic #597 Phase 3,
[ADR 025]). Lifecycle `modal_shown` → `modal_answer` / `modal_cancel` →
`modal_dismissed`. Six exported types in `messaging.go` (a modal is a
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
    ConversationID  string          `json:"conversation_id"`
    ModalID         string          `json:"modal_id"`
    Class           string          `json:"class"`
    Title           string          `json:"title"`
    Prompt          string          `json:"prompt"`
    Options         []ModalOption   `json:"options"`           // ordered: array order is display/selection order
    DefaultOptionID string          `json:"default_option_id"` // MUST equal one of Options[].ID (documented invariant)
    AlwaysAllow     AlwaysAllowPayload `json:"always_allow"`   // always present, including unavailable
    Reason          json.RawMessage `json:"reason,omitempty"`
    ReasonType      string          `json:"reason_type,omitempty"`
    BlockedPath     string          `json:"blocked_path,omitempty"`
    Description     string          `json:"description,omitempty"`
    DefaultToNo     bool            `json:"default_to_no,omitempty"`
}

type AlwaysAllowPayload struct {
    Offered bool     `json:"offered"`
    Rules   []string `json:"rules"`
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

- **The original modal fields and `AlwaysAllow` carry no `omitempty`; the five Claude-authored
  permission-context fields do** (#2346). This preserves the established payload
  for zero-context producers while allowing stdio permission asks to pass through
  `reason` (open-shape JSON), `reason_type` (open string vocabulary),
  `blocked_path`, `description`, and the `default_to_no` client-selection hint.
  None is derived from tool input or used as permission authority.
- **Always-allow publication is all-or-nothing** (#2364). An offerable stdio
  suggestion batch becomes ordered display strings in `Rules`; every invalid,
  unsupported, suppressed, or over-bound batch becomes the explicit unavailable
  value `{offered:false,rules:[]}`. Approval-MCP and other zero-context producers
  use that same shape. `Rules` is never null and never contains a valid prefix of
  a rejected batch. The outstanding modal retains the validated value, so an
  initial broadcast and reconnect snapshot serialize the same truth instead of
  parsing or reconstructing it twice. The strings are Claude-authored untrusted
  display content, not authorization input and not safe log attributes.
- **`modal_id` is the sole inbound correlation key; `conversation_id` is outbound
  scope only.** The daemon resolves `modal_id` against its **own** outstanding-modal
  state and never trusts a phone-asserted conversation; `option_id` maps against
  the daemon's own recorded option list. `modal_answer` and `modal_cancel` carry no
  `conversation_id`, so an inbound frame cannot present a disagreeing pair for the
  daemon to adjudicate. (Producer obligation: `modal_id` minted from `crypto/rand`,
  globally unique across concurrently-outstanding modals.)
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

The flat payload round-trips in `messaging_test.go` include populated and
unavailable `AlwaysAllow` shapes. `TestModalShownPayload_RoundTrip` pins option
order, the default, and bare/content-bearing rendered rules; the unavailable
fixture pins a non-null empty rule array. The answer and dismissal round-trips
also pin `AnswerToken` and `Source=="remote"`. All use the shared
`roundTripEnvelope` helper and single-line fixtures authored in **struct-field
order**. See
[codebase/701.md](../codebase/701.md).
