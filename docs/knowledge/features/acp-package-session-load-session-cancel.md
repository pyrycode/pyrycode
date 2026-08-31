# `session/load` + `session/cancel` (#762)

The two **lifecycle** verbs that address a session the host already opened. Both
reduce to one primitive — resolve an ACP `sessionId` onto its pool session via
`Pool.Lookup` — plus one shared param decode, and both register in #761's existing
`register` closure. **No new transport wiring, one production file touched
(`cmd/pyry/acp.go`), zero new exported types.**

### `decodeSessionID` — the shared decode (the bootstrap-trap guard)

`decodeSessionID(params) (sessions.SessionID, *acp.Error)` unmarshals
`{"sessionId": string}`. A JSON error, a missing key, or an **empty** `sessionId`
→ `acp.NewError(acp.CodeInvalidParams, …)`. Returning `*acp.Error` (not a plain
`error`) gives `session/load` the exact `CodeInvalidParams` wire code, and because
`*acp.Error` also satisfies `error`, `session/cancel` passes it straight into its
logged-and-discarded return with no conversion.

**The empty-string rejection is load-bearing.** `Pool.Lookup("")` resolves to the
parked **bootstrap** session, *not* an error (the empty-id → default-session seam
is deliberate for the control plane). An empty/missing `sessionId` must be refused
*before* it reaches `Lookup`, or `session/load` would activate the bootstrap.

### `session/load` — resume by id (`Lookup` + `Activate`, never `GetOrCreate`)

`loadSessionHandler` does `decodeSessionID` → `pool.Lookup(id)` →
`pool.Activate(ctx, id)` → `newSessionResult{SessionID: string(id)}` (reuses #761's result struct → `{"sessionId":"<id>"}`).

- **`Lookup` = miss-is-an-error, no side effect.** An unknown non-empty id →
  `ErrSessionNotFound` → `CodeInvalidParams` ("unknown session"), **no `Activate`,
  no spawn** (AC-2).
- **`Activate` is idempotent on the already-active session → divergence 6.** The
  ACP pool is uncapped (`ActiveCap == 0`), so `Pool.Activate` delegates to
  `Session.Activate`, whose already-`stateActive` fast path returns immediately off
  the closed `activeCh` and signals no lifecycle goroutine. A session Created by
  `session/new` is already active, so loading it (once or twice) spawns **no
  second claude** (AC-1, AC-3). It re-activates an evicted session in place, same
  id. See [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md) and
  [`sessions-package.md`](sessions-package.md).
- **`GetOrCreate` is deliberately NOT used** — it *creates on miss*, spawning a
  fresh claude for an unknown id, which would break both AC-2 and divergence 6.

### `session/cancel` — the notification → supervisor seam (T9, #753)

`session/cancel` is an ACP **notification** (a frame with `method` but no `id`).
The routing is factored into a directly unit-testable resolver:

```
resolveCancelTarget(pool, params) (*supervisor.Supervisor, error)
  = decodeSessionID → pool.Lookup(id) → sess.Supervisor()
```

As shipped by #762, `cancelSessionHandler` called the resolver and returned
`(nil, nil)` on success, landing only the resolution seam to the per-session
`*supervisor.Supervisor`. **[#753](#sessioncancel-actuation--modeconfig-pin-753)
now actuates the abort keystroke** (`SendEsc`) on that resolved supervisor — see
the section below.

**The no-response guarantee is structural, not code.** The transport's
[`dispatchNotification`](#classification-decision-table-the-core-mechanism)
routes a notification to the same handler map but **discards every notification
handler's return** — success or error. So registering the handler is the whole
mechanism; a resolution error is returned only so the notification path logs it,
and it too emits no response frame. (Contrast the request path, where the
handler's return shapes the wire frame.)

### Scope: within-process boundary only (architect call)

`session/load` resolves ids **this `pyry acp` process** opened. `pyry acp` stands
up a fresh **in-memory** pool per invocation (#761 leaves
`RegistryPath`/`ClaudeSessionsDir` zero → no persistence, no reconcile) and is a
one-shot stdio subprocess bound to one host connection, so every addressable id is
already in memory. Cross-process reload (an id a prior process created) would need
`RegistryPath` + reconcile wired into the ACP standup — deferred, additive.

Not security-sensitive — resolves *existing* ids only, accepts no caller path.
Full per-ticket detail in [`codebase/762.md`](../codebase/762.md).
