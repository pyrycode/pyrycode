# Session settings read payloads (#491/#1214, `ConversationID` field #1586, conversation-keyed reply #1610, permission mode #1687)

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
    SessionID      string `json:"session_id"`
    Model          string `json:"model"`
    Effort         string `json:"effort"`
    YOLO           bool   `json:"yolo"`
    PermissionMode string `json:"permission_mode"`
    UsedTokens     int    `json:"used_tokens"`
    WindowTokens   int    `json:"window_tokens"`
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
- **`PermissionMode` (#1687) reports a sixth value the write half's
  `permission_mode` field cannot accept: `bypassPermissions`.** A resolved
  session's `PermissionMode` and `YOLO` always agree, because
  `sessions.canonicalSettings` derives both together at every construction
  and update site — a session in bypass reports `permission_mode:
  "bypassPermissions"` and `yolo: true`. Deliberate, not a gap: the read
  half exists so a client's menu label comes from the daemon's own state,
  and suppressing the true posture would make that label lie. `""` on this
  field means "nothing resolved", the same as `session_id: ""` — never an
  unnamed mode, since `canonicalSettings` normalises every resolved session
  to one of the six.

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
catches, not something a new field needs its own guard for.
