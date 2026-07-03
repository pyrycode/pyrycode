# Spec — #753: ACP `session/cancel` interrupt + `session/set_mode` / `session/set_config_option` (single-mode pin)

Epic [#600](https://github.com/pyrycode/pyrycode/issues/600) — `pyry acp` as a thin adapter over the shared remote-head core. This ticket finishes the **inbound control** surface: `session/cancel` actuates the interrupt, and `session/set_mode` / `session/set_config_option` get defined, advertised answers under a single-mode pin. It builds directly on #762 (which wired `resolveCancelTarget` and left `cancelSessionHandler` a resolve-then-discard stub) and #747 (the `initialize`/`authenticate` handshake).

**Not security-sensitive** (no `security-sensitive` label; this is inbound control over local stdio to a co-located, user-launched host — no auth/crypto/network/remote-frame surface). No security-review pass.

## Files to read first

- `cmd/pyry/acp.go:197-229` — `resolveCancelTarget` (the #762 seam, returns `*supervisor.Supervisor`) + `cancelSessionHandler` (the resolve-then-`(nil,nil)` stub this ticket fills). **This is the primary edit site for cancel.**
- `cmd/pyry/acp.go:106-119` — the `register` closure inside `serveACPWithPool` where handlers are bound; `logger` is in scope here. Add `session/set_mode` / `session/set_config_option` registration and the cancel resolver closure here.
- `cmd/pyry/acp.go:121-145` — `newSessionResult` type + `newSessionHandler`; `loadSessionHandler:178-195` also returns `newSessionResult`. Both construction sites gain the `modes` field.
- `cmd/pyry/acp.go:147-169` — `decodeSessionID`: the empty-`sessionId` rejection pattern to mirror for `set_mode` param decoding; note *why* empty is load-bearing (`Pool.Lookup("")` → bootstrap).
- `cmd/pyry/acp_handshake.go` (whole file, ~110 lines) — `initializeResult` / `agentCapabilities` / `registerHandshake`; the stateless-handler idiom (`initializeHandler`, `authenticateHandler`) the two new mode/config handlers mirror, and the empty-struct-result pattern (`authenticateResult{}` → `{}`).
- `internal/relay/v2session.go:376-388` + `1725-1741` — the established `Interrupter interface{ SendEsc() error }` consumer-declared seam and `handleInterrupt`'s **best-effort** `SendEsc` treatment (nil-guard + tolerate error). This ticket mirrors that pattern for ACP cancel.
- `internal/sessions/session.go:118-122` — `Session.Supervisor()` returns `s.sup` (never nil, even when evicted — `PhaseStopped`); `SendEsc` on an evicted/mid-teardown session self-reports `ErrNoLiveSession`, which we swallow.
- `internal/supervisor/modal.go:64` + system-overview "safe-answer seam (#726)" — `SendEsc()` is a single non-blocking keystroke actuator; no `context`, best-effort, `ErrNoLiveSession` when no child is live.
- `internal/acp/acp.go:221-258` — `dispatchRequest` (writes exactly one response; `*acp.Error` → error frame with its code; plain `error` → `CodeInternalError`, detail logged not leaked) vs `dispatchNotification` (never writes a response; logs a returned error once as `"acp: notification handler error"`). Determines how `set_mode`/`set_config_option` (requests) and `cancel` (notification) behave.
- `internal/acp/jsonrpc.go:9-40` — `acp.Error`, `acp.NewError(code, msg)`, `CodeInvalidParams`; `acp.Handler = func(ctx, json.RawMessage) (any, error)`.
- `cmd/pyry/acp_test.go:288-447` — `newFakeClaudePool`, `newACPHarness`/`send`/`read`/`shutdown`, `acpReply` (`Result *newSessionResult` — adding `Modes` decodes automatically), `sessionIDParams`, `unknownSessionID`. Test scaffolding to reuse.
- `cmd/pyry/acp_test.go:492-546` + `646-690` — `TestResolveCancelTarget` (pointer-equality routing proof — **keep, it is half of AC-1**) and `TestACP_SessionCancel_NotificationEmitsNoResponse` (no-response + one-log-on-unknown-id — **stays green unchanged**, see Testing).
- `internal/relay/v2session_interrupt_test.go:24` / `cmd/pyry/modal_resolve_v2_test.go:32` — existing `fakeInterrupter` / `fakeKeystroker` doubles to copy for the cancel actuation test.
- `docs/knowledge/decisions/027-acp-mapping.md` — ADR 027; **Open Item #1 is resolved by this spec's architect run** (already edited on this branch). The mode/config decision + rationale live there and in this spec (AC-4).

## Context

`session/cancel` is an ACP **notification** (no response) that aborts the running turn. The interrupt lever already exists — `supervisor.Supervisor.SendEsc()` ("cancel an in-flight turn", #726) — and #762 already resolves the target session's supervisor via `resolveCancelTarget`. This ticket drops the interrupt onto that seam.

Mode and config are ADR 027 Open Item #1: expose a real `set_mode` (plan vs edit) or pin one mode. There is no neutral-model mode source and no cheap tui-driver lever to toggle claude's plan/edit mode (the #726 keystroke seam is `SendEsc`/`Answer`/`AcceptTrust` only). A real mode switch would need new tui-driver + supervisor surface — out of scope, past size. **Decision: single-mode pin** (recorded in ADR 027 + Technical Notes below).

The hard cost invariant (ADR 025/026) holds: cancel routes one keystroke into the live interactive `claude`; it introduces no `claude -p` and no Agent SDK path.

## Design

Two independent, small changes in `cmd/pyry` (`acp.go`, `acp_handshake.go`). No new files, no new exported types (every new type/const is unexported in package `main`).

### 1. `session/cancel` → actuate the interrupt

Consumer-declared one-method seam, mirroring `relay.Interrupter`, so the actuation is unit-testable with a double while resolution correctness stays in its own (existing) test:

```go
// package main (cmd/pyry)
type interrupter interface{ SendEsc() error }   // *supervisor.Supervisor satisfies it (#726)
```

`cancelSessionHandler` takes an injected resolver instead of the raw pool:

- **Contract:** `cancelSessionHandler(resolve func(json.RawMessage) (interrupter, error)) acp.Handler`.
- **Behavior:** resolve the target; on resolve error return `(nil, err)` (dispatched-as-notification → logged once, no response frame — unchanged from #762); on success call `SendEsc()` **best-effort** (`_ = intr.SendEsc()`) and return `(nil, nil)`. A `SendEsc` failure (no live turn / mid-teardown) is swallowed: there is nothing to roll back and a notification owes no reply — identical to `handleInterrupt`'s treatment. Invariant asserted by the actuation test (below).

`resolveCancelTarget` is **unchanged** (still returns `*supervisor.Supervisor`). Composition root (`serveACPWithPool` register closure) wires the resolver, returning a true `nil` interface on error to avoid the typed-nil-in-interface trap:

```go
t.Register("session/cancel", cancelSessionHandler(func(p json.RawMessage) (interrupter, error) {
    sup, err := resolveCancelTarget(pool, p)
    if err != nil {
        return nil, err
    }
    return sup, nil
}))
```

**Why swallow `SendEsc` errors instead of returning them:** returning the error would make `dispatchNotification` log a second `"acp: notification handler error"` on the known-id path whenever the fake-claude session isn't keystroke-ready — breaking the existing `count == 1` assertion and adding non-deterministic noise. Best-effort matches the relay and keeps the known-id path silent. (Observability of a swallowed keystroke failure is deferred — evidence-based: no observed need, and the host learns the turn's real outcome via the outbound stop reason, owned by #751.)

### 2. Mode/config — single-mode pin

**Decision (ADR 027 Open Item #1, resolved):** pin one mode; advertise it where ACP carries mode state — the `modes` (`SessionModeState`) field of the `session/new` **and** `session/load` responses. The ticket's pointer at `acp_handshake.go` for the *advertisement* is corrected: ACP `initialize`/`agentCapabilities` has no mode flag, so the handshake struct is **unchanged**; the pin surfaces in the session responses. The stateless mode/config *handlers* may live beside the handshake handlers (they need no pool).

Unexported types + a pinned constant (place the types/helper in `acp_handshake.go` as "advertised surface"; reference from `newSessionResult`):

```go
const pinnedModeID = "default"

type sessionModeState struct {
    CurrentModeID  string        `json:"currentModeId"`
    AvailableModes []sessionMode `json:"availableModes"`
}
type sessionMode struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}
// pinnedModeState() sessionModeState — returns {currentModeId:"default", availableModes:[{id:"default", name:"Default"}]}
```

`newSessionResult` (shared by `session/new` + `session/load`) gains one field:

```go
type newSessionResult struct {
    SessionID string           `json:"sessionId"`
    Modes     sessionModeState `json:"modes"`   // populated from pinnedModeState() at both sites
}
```

A single self-referential `availableModes` entry offers a compliant host no alternative to switch to — the faithful "not a switch that does nothing." (`acpReply.Result` is already `*newSessionResult`, so existing decoders absorb the new field; no existing test breaks.)

Two stateless handlers (no pool — the pin is process-global; `sessionId` is not resolved, a deliberate simplification since the mode is fixed for every session):

- **`setModeHandler`** — decode `{sessionId, modeId}`; unmarshal failure → `acp.NewError(CodeInvalidParams, "invalid params")`; `modeId == pinnedModeID` → success with empty result (`setModeResult{}` → `{}`); any other `modeId` → `acp.NewError(CodeInvalidParams, "unsupported session mode")`. (Accept-the-pinned-mode + reject-any-other, AC-2.)
- **`setConfigOptionHandler`** — always `acp.NewError(CodeInvalidParams, "no configurable options")`: pyry advertises no config options, so any option is unknown. No params decode needed. (Asymmetric with mode by design — pyry has a nameable mode but no real config surface; recorded in ADR 027.)

Register both in the `serveACPWithPool` register closure alongside the other `session/*` methods:

```go
t.Register("session/set_mode", setModeHandler)
t.Register("session/set_config_option", setConfigOptionHandler)
```

Both are ACP **requests** (carry an id): `dispatchRequest` writes exactly one response — the empty result on accept, or the error frame carrying `CodeInvalidParams` on reject. That is the "defined result" AC-2 requires.

## Concurrency model

None added. Handlers dispatch inline on the transport read loop (existing `internal/acp` contract). `SendEsc` is a single non-blocking `pty.Write` behind the supervisor's `sessMu`-then-release discipline — no goroutine, no context, no new lock. The mode/config handlers are pure functions over their params. No shared mutable state is introduced.

## Error handling

| Path | Result |
|---|---|
| `session/cancel`, unknown/malformed/empty `sessionId` | resolve error returned → `dispatchNotification` logs once (`"acp: notification handler error"`), **no** response frame. Unchanged from #762. |
| `session/cancel`, known id, live turn | `SendEsc` delivers ESC; `(nil,nil)`; no frame. |
| `session/cancel`, known id, no live turn (evicted/teardown) | `SendEsc` → `ErrNoLiveSession`, **swallowed**; `(nil,nil)`; no frame, no log. |
| `session/set_mode`, `modeId == "default"` | empty-object success response. |
| `session/set_mode`, other/empty `modeId` | `CodeInvalidParams` error response ("unsupported session mode"). |
| `session/set_mode`, non-object params | `CodeInvalidParams` error response ("invalid params"). |
| `session/set_config_option`, any params | `CodeInvalidParams` error response ("no configurable options"). |

`*acp.Error` returns carry the exact wire code through `dispatchRequest`; no params bytes are logged (diagnostics discipline).

## Testing strategy

Bullet scenarios (developer writes them in the table-driven, stdlib-only idiom; reuse `newACPHarness`/`newFakeClaudePool`/`sessionIDParams`/`unknownSessionID`):

- **Cancel actuation (AC-1, actuation half).** New `fakeInterrupter{escCalls int; err error}` (copy `internal/relay/v2session_interrupt_test.go:24`). Drive `cancelSessionHandler` with a resolver returning `&fakeInterrupter{}`: assert `escCalls == 1`, result `nil`, err `nil`. Drive with a resolver returning `(nil, someErr)`: assert `escCalls == 0` (no actuation) and the error is returned. Drive with a resolver whose interrupter returns an error: assert `escCalls == 1` and result `(nil, nil)` (best-effort swallow).
- **Cancel routing (AC-1, routing half).** Keep `TestResolveCancelTarget` as-is — it proves the production resolver maps params → the *correct* session's supervisor by pointer equality. Together with the actuation test, this proves "the interrupt reaches the correct session's supervisor" without a brittle live-PTY keystroke observation (belt-and-suspenders, both halves deterministic).
- **Cancel no-response unchanged.** `TestACP_SessionCancel_NotificationEmitsNoResponse` stays green with **no edit**: known-id cancel now calls `SendEsc` (swallowed, logs nothing), unknown-id still logs exactly one handler error. Confirm the `count == 1` assertion holds after wiring.
- **`set_mode` (AC-2).** Through the harness: `modeId:"default"` → a success frame with an empty/`{}` result and no error; `modeId:"plan"` → error frame, `code == CodeInvalidParams`; malformed params (e.g. `{"modeId":5}` or a JSON array) → `CodeInvalidParams`.
- **`set_config_option` (AC-2).** Any `session/set_config_option` request → error frame, `code == CodeInvalidParams`.
- **Advertised pin (AC-3).** `session/new` reply `Result.Modes` equals the pin: `currentModeId == "default"`, exactly one `availableModes` entry with `id == "default"`. Assert the same on the `session/load` reply (resumed session carries the same pin). May extend the existing session/new + session/load happy-path tests rather than adding standalone tests.
- `make check` green (`go vet`, `staticcheck`, `go test -race`) — AC-5.

## Scope / sizing

**S (small S).** Total written work ≈ 200 LOC across **3 existing files** (`cmd/pyry/acp.go`, `cmd/pyry/acp_handshake.go`, `cmd/pyry/acp_test.go`) — production ≈ 60–75 LOC, tests ≈ 130 LOC. Red-line tally: **0** new files; **0** new exported types (all `main`-package-unexported); reject/decision branches ≈ 5 (cancel resolve-err/ok, set_mode accept/reject/badparams, set_config reject) — far under 10; edit fan-out: `cancelSessionHandler`'s signature change has **1** call site (its registration), `newSessionResult` gains a field at **2** construction sites (no test cascade — the field is additive and `acpReply.Result` already decodes it). Developer ACs are **AC-1, AC-2, AC-3, AC-5** (four).

**Not downgraded to XS:** the pin is not a pure no-op — it advertises `SessionModeState` in two session responses, adds accept/reject mode logic, a reject-all config handler, and the cancel actuation seam. That is a legitimate small-S, above the "trivial no-op" bar the ticket set for an XS downgrade.

**No real mode lever found** (the escape the ticket flagged): the tui-driver keystroke seam exposes no mode toggle, so wiring a real `set_mode` is out of scope — the single-mode pin is the design, no route-back to PO.

## Technical Notes — mode/config decision (AC-4)

**AC-4 is satisfied by this architect run, not the developer.** The decision + rationale are recorded in **ADR 027 Open Item #1** (edited on this branch) and here. The developer's worktree mutates only code + tests (per the code/test/spec-only rule); it does **not** edit ADR 027.

Decision: **single-mode pin.** `pyry acp` pins one session mode (`"default"`), advertised via `SessionModeState` in the `session/new`/`session/load` responses (one self-referential `availableModes` entry). `session/set_mode` accepts `"default"` (no-op success), rejects any other `modeId` with `CodeInvalidParams`. `session/set_config_option` is rejected wholesale (`CodeInvalidParams`, "no configurable options") — pyry exposes no config surface, an asymmetry with mode that is deliberate. ACP `initialize`/`agentCapabilities` is unchanged (ACP carries no mode flag there); the ticket's `acp_handshake.go` advertisement pointer is corrected to the session responses. Because no mode/config ever changes, the sourceless outbound `current_mode_update`/`config_option_update` reflections are never emitted — their synthesize-vs-omit call stays parked in ADR 027 Open Item #2 (outbound cluster, #750/#769), out of scope here.

Rationale for the pin over real handlers: no neutral-model mode source, and no cheap lever to change claude's plan/edit mode through the tui-driver path — a real switch needs new tui-driver + supervisor surface, past this ticket's size and outside its inbound-control scope.

## Open questions

- **Pinned mode id string.** `"default"` matches claude's own default permission-mode name and reads honestly as "the one mode pyry offers." If a reviewer prefers a more explicit id (e.g. `"interactive"`), it is a one-constant change with no wire-shape impact.
- **`set_mode` session resolution.** The handler does not resolve `sessionId` against the pool (the pin is process-global, so the answer is identical for every session). If a future ticket makes mode per-session, `set_mode` would gain a `Pool.Lookup` like `resolveCancelTarget`. Not needed today.
- **End-to-end cancel → `stopReason: cancelled`.** Out of scope (Technical Notes in the ticket): the held `session/prompt` resolution with the mapped stop reason is #751's, and there is no held prompt to resolve until #749/#765 land. This ticket asserts only that the interrupt reaches the supervisor.
