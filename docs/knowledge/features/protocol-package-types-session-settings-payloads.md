# Session settings payloads (#844, permission mode #1687)

The wire vocabulary for changing a session's per-session model / reasoning
effort / permission mode / YOLO (`docs/protocol-mobile.md` § Session settings; split from #841). **Wire vocabulary only** — the handler that intercepts
`set_session_settings` at `v2session.go`'s `dispatchAppFrame` **before**
`dispatch.Route` (the `TypeModalAnswer` / `TypeNewSession` precedent — **no
`dispatch.Route` handler**), gates on the `interactive` capability, validates,
persists via `sessions.Pool.UpdateSettings` (#840), and emits the reply
shipped in sibling #845 (see [Inbound set_session_settings](v2-session-manager.md#inbound-set_session_settings-845--settingsupdater-seam-validate-persist-reply)).
See [codebase/844.md](../codebase/844.md).

```go
type SetSessionSettingsPayload struct {
    SessionID      string  `json:"session_id"`
    Model          *string `json:"model,omitempty"`
    Effort         *string `json:"effort,omitempty"`
    YOLO           *bool   `json:"yolo,omitempty"`
    PermissionMode *string `json:"permission_mode,omitempty"`
}

type SessionSettingsUpdatedPayload struct {
    SessionID string `json:"session_id"`
}
```

- **The four settings fields are pointers with `omitempty` — the presence
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
- **`PermissionMode` breaks the pattern its three siblings set: present-at-`""`
  is not a settable value.** For `Model`/`Effort`, `*""` means "run at
  claude's own default" — a real value the presence contract distinguishes
  from omitted. The default *posture* is itself a nameable mode (`"default"`),
  so an explicit `""` names nothing; `validPermissionMode` in
  `internal/relay` refuses it before persistence, same reply as any other
  out-of-vocabulary value. A field family sharing one presence-contract
  paragraph can still diverge on what "present at zero" means per field —
  don't assume a sibling's reading carries over.
- **Mirrors `sessions.SettingsUpdate{Model, Effort *string; YOLO *bool;
  PermissionMode *string}` (#840, #1687) field-for-field** — the #845
  handler decodes this payload straight into that seam.
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
`set_session_settings_full.json` (all three original fields present at zero
value) vs `set_session_settings_omitted.json` (only `session_id`), asserting
pointer nil-ness then a byte-equal re-marshal — the regression guard for the
`omitempty` decision — plus a `session_settings_updated.json` round-trip for
the reply. `PermissionMode` could not extend `set_session_settings_full.json`:
that fixture also carries `yolo`, which the relay's conflict check rejects,
and its "present at zero" reading (`""`) is a refusal rather than a value for
this field alone. It got its own fixture, `set_session_settings_mode.json`
(`{"session_id":"sess-a","permission_mode":"plan"}`), while the two existing
fixtures — unchanged — now also pin `PermissionMode == nil` on the absent
half. When a new field's presence-contract reading diverges from its
siblings', reach for a fixture of its own rather than extending the "all
fields present" one.
