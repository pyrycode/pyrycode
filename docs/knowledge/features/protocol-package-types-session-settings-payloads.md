# Session settings payloads (#844)

The wire vocabulary for changing a session's per-session model / reasoning
effort / YOLO (`docs/protocol-mobile.md` § Session settings; split from #841). **Wire vocabulary only** — the handler that intercepts
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
