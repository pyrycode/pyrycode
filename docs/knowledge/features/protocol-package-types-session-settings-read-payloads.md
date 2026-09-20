# Session settings read payloads (#491/#1214, `ConversationID` field #1586, conversation-keyed reply #1610, confirmed permission mode #2510, effective-effort vocabulary #2515)

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
    SessionID       string         `json:"session_id"`
    Model           string         `json:"model"`
    Effort          string         `json:"effort"`
    EffectiveEffort NullableString `json:"effective_effort,omitzero"`
    YOLO            bool           `json:"yolo"`
    PermissionMode  string         `json:"permission_mode"`
    UsedTokens      int            `json:"used_tokens"`
    WindowTokens    int            `json:"window_tokens"`
}
```

- **`ConversationID` was added by #1586; the frame was genuinely bare before
  it.** It names the conversation the client is asking about. Untrusted
  network input, resolved through the handler's conversation-keyed
  `RunConfigFor` seam (#1610) rather than a membership check — the seam
  resolves-and-refuses in one call, so an unknown or unbound conversation
  never distinguishes itself from any other unaddressable case. It reaches no
  log line, no error string, no filesystem path, and not the reply.
- **No omission tag on the request or any original reply field**, matching
  `RequestSnapshotPayload` / `ScreenSnapshotPayload` and deliberately unlike
  the sibling `SetSessionSettingsPayload` above, whose per-field pointers
  encode a presence contract. There is no presence contract on the request:
  an absent and an empty `conversation_id` are the **same** case — "no
  conversation named" — so nothing needs to tell them apart, and keeping the
  field always on the wire lets a fixture pin the full shape. On the reply,
  every original field is always a real answer, not an absence: `SessionID ""`
  means "nothing to address", `Model`/`Effort` `""` mean "inherited default,
  no per-session override", and `WindowTokens 0` means the daemon has no
  trustworthy window reading. `Effort` remains the saved per-session choice;
  it must not be populated with Claude's applied value. `YOLO false` means only
  that the current child has not confirmed bypass; paired with
  `PermissionMode ""`, it is an unavailable posture rather than proof that
  permissions are enforced. The window can be unavailable
  because the usage seam
  is unwired, or (#2100) the used count came out above the window the daemon
  believed, which disproves the belief; read it against `UsedTokens` to tell
  the two apart (`UsedTokens 0` against a non-zero `WindowTokens` is a genuine
  fresh session; a non-zero `UsedTokens` against `WindowTokens 0` is the
  disproved case, and that used figure is still the true context size). See
  [contextwindow-package.md](contextwindow-package.md#context-window-size--a-believed-default-not-an-asserted-fact).
- **`EffectiveEffort` is the one optional reply field because its absence is
  itself information.** A present string is Claude's confirmed applied level,
  present `null` means Claude reported no effort parameter, and omission means
  the reading is unavailable or unsupported. A plain `*string` is insufficient:
  JSON decoding maps both an omitted key and explicit `null` to nil. A
  `json.RawMessage` would preserve presence but would accept values outside the
  string-or-null vocabulary and make `SessionSettingsPayload` non-comparable.
  The comparable `NullableString` wrapper keeps all three states: its zero value
  is omitted via `omitzero`, while a constructed present value holds either a
  string pointer or nil. When a protocol field needs null and omission to stay
  distinct after decoding, test the decoded presence bit as well as the emitted
  bytes; marshal-only coverage cannot catch the collapse.
- **The field gates *which* session the reply describes (#1610).** A
  `conversation_id` naming a conversation this daemon hosts, with a live
  bound session, is answered with **that conversation's own** values — never
  the shared bootstrap session's. A dormant binding is also resolved: it keeps
  the persisted session id, model, and effort while the permission pair and
  context figures are unavailable. An absent/empty `conversation_id`, one
  naming a conversation this daemon does not host, or one bound to no live or
  dormant session is answered with a zero-valued `SessionSettingsPayload`,
  never an error frame. The reported id and values still arrive through one
  `RunConfig`, so a client can never read one session's values and write to
  another. There is no bootstrap-scoped fallback for this verb.
- **`PermissionMode` (#1687) reports a sixth value the write half's
  `permission_mode` field cannot accept: `bypassPermissions`.** Since #2510,
  the pair comes only from `Runner.ConfirmedPermissionMode` for the exact
  current child, never from stored settings or launch argv. Confirmed bypass
  reports `permission_mode: "bypassPermissions"` and `yolo: true`; any live
  child without a confirmation, and every dormant session with no child,
  reports `permission_mode: ""` and `yolo: false` while preserving the
  resolved id and stored model/effort. The empty mode therefore means
  "confirmation unavailable", not necessarily "nothing resolved", and
  `yolo: false` alone is not evidence that permissions are enforced.

Golden round-trips in `settings_test.go`: `TestRequestSessionSettingsPayload_RoundTrip`
against `testdata/request_session_settings.json` (non-empty fixture id — this
one does **not** pin the no-`omitempty` decision, since the key survives
omission when non-empty) plus `TestRequestSessionSettingsPayload_EmptyConversationID`,
the sole pin on that decision (asserts the empty key stays on the wire rather
than being dropped); and `TestSessionSettingsPayload_RoundTrip` against
`testdata/session_settings.json` for the reply — updated to carry
`"permission_mode":"default"` by #1687, which fails the byte-equal
re-marshal until the fixture catches up: the "no `omitempty`" rule on this
struct means a fixture missing a new field is a drift the test itself
catches, not something a new always-present field needs its own guard for.
`effective_effort` is deliberately different: focused raw-JSON coverage uses
different saved and effective values and exercises string, explicit null, and
omitted forms through marshal, decode, and re-marshal. That separate coverage
is what detects a field swap or a decoder that collapses null into omission.
