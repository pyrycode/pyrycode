# `session/cancel` actuation + mode/config pin (#753)

The inbound **control** surface (epic #600 T9). Two independent small changes in
`cmd/pyry` (`acp.go`, `acp_handshake.go`): `session/cancel` actuates the interrupt
onto #762's resolution seam, and `session/set_mode` / `session/set_config_option`
get defined, advertised answers under a **single-mode pin** — resolving
[ADR 027](../decisions/027-acp-mapping.md) Open Item #1. No new files, no new
exported types. **Not security-sensitive** — inbound control over local stdio to a
co-located, user-launched host.

### `session/cancel` → actuate the interrupt

A consumer-declared one-method seam in package `main`, mirroring `relay.Interrupter`,
keeps the actuation unit-testable while resolution correctness stays in its own
(#762) test:

```go
type interrupter interface{ SendEsc() error } // *supervisor.Supervisor satisfies it (#726)
```

`cancelSessionHandler`'s signature changed from `(pool *sessions.Pool)` to an
**injected resolver** `(resolve func(json.RawMessage) (interrupter, error))`. On a
resolve error it returns the error (the notification path logs it once, no frame —
unchanged from #762); on success it actuates `_ = intr.SendEsc()` **best-effort**
and returns `(nil, nil)`. `resolveCancelTarget` is **unchanged** (still returns
`*supervisor.Supervisor`); the composition root wires the resolver and returns a
**true `nil` interface on error** (`resolveCancelTarget`'s typed
`*supervisor.Supervisor` would otherwise wrap a nil pointer in a non-nil
`interrupter`). `SendEsc` is the same lever the mobile `interrupt` / `modal_cancel`
frames route through (`relay.handleInterrupt`).

**A `SendEsc` failure is swallowed, not returned.** No live turn / mid-teardown
yields `ErrNoLiveSession`: there is nothing to roll back and a notification owes no
reply. Returning it would make [`dispatchNotification`](#classification-decision-table-the-core-mechanism)
log a *second* handler error on the known-id path whenever a session isn't
keystroke-ready — breaking the `count == 1` assertion of
`TestACP_SessionCancel_NotificationEmitsNoResponse` and adding non-determinism.
(Silent-swallow, vs `relay.handleInterrupt`'s `Warn`-then-tolerate — the handler
has no logger in scope; observability is deferred, evidence-based.) The interrupt
is **not** routed through the neutral `turnevent.Cancel`
([turnevent-package.md](turnevent-package.md), #707) — that type is declared
vocabulary consumers are told not to construct; the handler calls the concrete
`SendEsc()` lever directly.

### Mode/config — single-mode pin (ADR 027 Open Item #1)

No neutral-model mode source exists, and no tui-driver lever toggles claude's
plan/edit mode (the [#726](../codebase/726.md) keystroke seam is
`SendEsc`/`Answer`/`AcceptTrust` only), so a real `set_mode` would need new
tui-driver + supervisor surface — out of scope. **Decision: pin one mode.**

- **The pin is advertised in the session responses, not the handshake.** ACP
  `initialize`/`agentCapabilities` has no mode flag, so the ticket's original
  `acp_handshake.go` advertisement pointer is corrected: `newSessionResult` (shared
  by `session/new` + `session/load`) gains a `Modes sessionModeState` field,
  populated from `pinnedModeState()` at both sites — `currentModeId: "default"`
  with a single self-referential `availableModes` entry. One entry offers a
  compliant host no alternative to switch to (the faithful "not a switch that does
  nothing"). The handshake struct is **unchanged**.
- **`setModeHandler`** (stateless, no pool — the pin is process-global): decode
  `{sessionId, modeId}`; unmarshal failure → `CodeInvalidParams`; `modeId ==
  "default"` → empty-`{}` success (`setModeResult{}`); any other `modeId` →
  `CodeInvalidParams` ("unsupported session mode").
- **`setConfigOptionHandler`** (stateless): rejects **every** request with
  `CodeInvalidParams` ("no configurable options") — pyry exposes no config surface.
  Deliberate asymmetry with mode (a real, nameable mode but no config surface).
- Both are ACP **requests** carrying an id → `dispatchRequest` writes exactly one
  response (empty result on accept, or the `CodeInvalidParams` frame). Registered
  in `serveACPWithPool`'s existing `register` closure alongside the other
  `session/*` methods.

Because no mode/config ever changes, the sourceless outbound `current_mode_update`
/ `config_option_update` reflections are **never emitted** — their
synthesize-vs-omit call stays parked in
[ADR 027](../decisions/027-acp-mapping.md) Open Item #2 (outbound cluster, #750/#769).

### Out of scope

The end-to-end **cancel → `stopReason: cancelled`** assertion belongs to
[#751](https://github.com/pyrycode/pyrycode/issues/751) (now landed): the held
`session/prompt` call resolves with the mapped stop reason on `TurnEnd` — a
`cancel`-driven `TurnEnd{Reason: cancelled}` returns `stopReason: "cancelled"` (see
[held-call resolution](#turnend--held-call-resolution-751)). This ticket asserts only
that the interrupt reaches the supervisor. Full per-ticket detail in
[`codebase/753.md`](../codebase/753.md).
