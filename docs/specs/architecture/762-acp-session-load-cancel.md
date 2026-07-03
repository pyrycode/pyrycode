# Spec: `session/load` + `session/cancel` lifecycle over the embedded pool (#762)

Epic #600 — `pyry acp` as a thin adapter over the shared remote-head core. Builds directly on #761 (`session/new` + the embedded pool standup).

## Files to read first

Turn-1 data load. Read these before writing any code. Everything you touch lives in `cmd/pyry/acp.go`; the rest is contract you consume unchanged.

- `cmd/pyry/acp.go` (whole, 207 lines) — the file you extend. Key seams:
  - `serveACPWithPool:106-108` — the `register` closure. You add two `t.Register(...)` lines here alongside `session/new`. **No other structural change to this function.**
  - `newSessionHandler:134-142` — the handler shape to mirror (`func(pool) acp.Handler`, returns a closure over `pool`).
  - `newSessionResult:120-122` — the `{"sessionId": string}` result struct. **Reuse it for `session/load`'s success result** (zero new types).
- `internal/acp/acp.go:53-59` — `Handler` contract: `func(ctx, params json.RawMessage) (result any, err error)`. A returned `*acp.Error` controls the wire code; any other error → `CodeInternalError` (detail logged, not leaked); `(nil, nil)` = "nothing".
- `internal/acp/acp.go:246-258` — `dispatchNotification`. **Load-bearing for AC-4:** a notification (method, no `id`) routes to the same handler map but **never emits a response regardless of the handler's return** — the handler's error is only logged. This is why `session/cancel` needs no response plumbing: registering it is enough.
- `internal/acp/acp.go:221-244` — `dispatchRequest`. What `session/load` (a request, has `id`) returns: `*acp.Error` → `writeErrorWithData` (typed code); plain error → `CodeInternalError`; success → `writeSuccess`.
- `internal/acp/jsonrpc.go:12-18` — error codes. `CodeInvalidParams = -32602` is the code for a missing/malformed/unknown `sessionId`. `NewError(code, msg)` at :37 constructs the `*acp.Error`.
- `internal/sessions/pool.go:713-729` — `Pool.Lookup(id)`. **Read the empty-id trap:** `Lookup("")` returns the **bootstrap** session (the parked evicted placeholder), *not* an error. A non-empty unknown id returns `ErrSessionNotFound`. The decode must reject empty `sessionId` before it reaches `Lookup`, or a host could resolve/activate the bootstrap.
- `internal/sessions/pool.go:33` — `var ErrSessionNotFound`. The sentinel `Lookup` returns on an unknown non-empty id → map to `CodeInvalidParams` (AC-2).
- `internal/sessions/pool.go:1216-1241` — `Pool.Activate(ctx, id)`. ACP leaves `ActiveCap == 0` (uncapped), so this delegates straight to `sess.Activate(ctx)` — **the capped-path `stateActive` early-return does not run**; idempotency comes from `Session.Activate` itself (next entry).
- `internal/sessions/session.go:209-229` — `Session.Activate`. **The AC-3/divergence-6 linchpin:** when the session is already `stateActive`, it sends nothing on `activateCh`, the shared `activeCh` is already closed so the receive returns immediately, then `WaitForPTY` returns against the live PTY. **No second claude spawns.** Read this to confirm the idempotency claim first-hand.
- `internal/sessions/session.go:118-122` — `Session.Supervisor() *supervisor.Supervisor`. Already exposed. The cancel seam resolves onto this; T9 (#753) delivers the abort keystroke against it.
- `cmd/pyry/acp_test.go` (whole, 278 lines) — the test file you extend. `fakeClaudeScript:153-164` (argv-recording fake claude, one line per spawn) and `TestACP_SessionNew_SpawnsOneInteractiveClaude:171-278` (the full transport-driven harness: paired `io.Pipe`s, `serveACPWithPool`, request write, response read, argv-line count). **This is the divergence-6 harness to extend** — the argv-line-count assertion IS the "exactly one claude" proof.
- `internal/sessions/id.go:18` (`type SessionID string`) and `:43` (`func ValidID(string) bool`) — id type + canonical-shape validator (optional; see Open Questions).
- `CODING-STYLE.md` § Testing — table-driven, stdlib `testing` only.

## Context

#761 delivered the first ACP method that does real work (`session/new`) and the composition root it needs: an embedded, trimmed `internal/sessions` pool (bootstrap evicted, service-mode bridge, no persistence/relay/control/queue). `session/new` mints a fresh id and spawns exactly one supervised interactive claude per call.

Now the host needs to **address sessions it already opened**:

- `session/load` — resume an existing session by id and return a success result.
- `session/cancel` — an ACP **notification** (no response) that aborts the running turn. The full inbound-`Cancel`→internal-command mapping (#707) is **T9 (#753)**; this ticket lands only the **lifecycle routing** cancel needs: locate the session by id and reach its per-session supervisor. T9 drops the abort keystroke onto that seam.

Both verbs reduce to one primitive — resolve an ACP `sessionId` onto its pool session via `Pool.Lookup` — plus one shared param decode. Both register in the existing `register` closure; **no new transport wiring**.

**Divergence 6 holds.** `session/load` resumes the existing supervised claude for that id; it must NOT start a second. There is exactly one live interactive claude per ACP session throughout.

### Two facts that shape the design (verified against live code)

1. **`Pool.Activate` on an already-active session is a no-op that spawns no claude.** ACP's pool is uncapped (`ActiveCap == 0`), so `Pool.Activate` delegates to `Session.Activate`, whose already-active fast path returns immediately off the closed `activeCh` without signalling the lifecycle goroutine. A session Created by `session/new` is already active, so `session/load`-ing it (once or twice) adds no claude. This is what makes AC-3 hold. (`session.go:209-229`.)
2. **`Lookup("")` resolves to the bootstrap, not an error.** The empty-id → default-session seam (`pool.go:721-722`) is deliberate for the control plane, but here it is a trap: a `session/load`/`session/cancel` carrying an empty (or missing) `sessionId` must be rejected as `CodeInvalidParams` **before** it reaches `Lookup`, or it would resolve — and `session/load` would activate — the parked bootstrap. The shared decode enforces non-empty.

### Scope decision — the "existing session" boundary (architect call)

The ticket delegates the within-process-vs-across-process boundary to the architect. **Decision: within-process only.** Rationale:

- `pyry acp` (#756/#761) stands up a fresh, **in-memory** pool per invocation: #761's standup leaves `RegistryPath`/`ClaudeSessionsDir` **zero** → no persistence, no reconcile. There is nothing on disk for a later process to reload.
- `pyry acp` is a one-shot stdio subprocess bound to a single host connection; it exits on EOF. The sessions a host can address are exactly the ones it opened via `session/new` **in this process** — all live in memory. Across-process reload has no consumer here.
- Wiring `RegistryPath` + reconcile to support cross-process reload is **new persistence work**, out of scope, and would blow S. The ticket's own instruction covers this: "if `session/load` turns out to require new persistence work, route back." It does not — the within-process boundary needs **zero** persistence work and satisfies the behavioural AC ("resume an existing session by id") for every id this process can produce. Cross-process reload is deferred (a future ticket wires `RegistryPath` + reconcile into the ACP standup; additive, no redesign).

## Design

All changes are additive and confined to **`cmd/pyry/acp.go`** (1 production file modified, 0 new production files, 0 new exported types). Both handlers reuse existing `internal/sessions` / `internal/acp` primitives verbatim — no changes to those packages.

### 1. Shared param decode — `decodeSessionID`

The single seam both verbs share (the ticket's "one shared decode covers both verbs").

```
func decodeSessionID(params json.RawMessage) (sessions.SessionID, *acp.Error)
```

- Behaviour: unmarshal `params` into a one-field struct `{ SessionID string \`json:"sessionId"\` }`. On a JSON error, a missing key, or an **empty** `sessionId`, return `acp.NewError(acp.CodeInvalidParams, "…")` (the empty-string rejection is what closes the `Lookup("")` bootstrap trap). Otherwise return `sessions.SessionID(v)`, nil.
- Returns `*acp.Error` (not a plain `error`) so `session/load` gets the exact `CodeInvalidParams` wire code; `*acp.Error` also satisfies `error`, so the cancel path can pass it straight into its (logged, discarded) error return with no conversion.
- Diagnostics discipline (per `internal/acp` package doc): never log the params bytes — a `sessionId`-bearing frame is low-risk, but keep to method-name/error-kind logging like the rest of the transport.

### 2. `session/load` handler — `loadSessionHandler(pool) acp.Handler`

```
func loadSessionHandler(pool *sessions.Pool) acp.Handler
// closure: func(ctx, params) (any, error)
```

Behaviour (contract, not body):

1. `id, aerr := decodeSessionID(params)` — on `aerr != nil` return `(nil, aerr)` → `CodeInvalidParams`.
2. `sess, err := pool.Lookup(id)` — on `errors.Is(err, sessions.ErrSessionNotFound)` return `(nil, acp.NewError(acp.CodeInvalidParams, "unknown session"))`. **No `Activate`, no spawn** (AC-2). (`Lookup` returns only `ErrSessionNotFound` or nil; a defensive `else if err != nil` → wrapped internal error is fine but unreachable today.)
3. `if err := pool.Activate(ctx, id); err != nil` → return wrapped (`fmt.Errorf("session/load: %w", err)`) → `CodeInternalError`. `Activate` is idempotent on the already-active Created session (no second claude — AC-1, AC-3) and re-activates in place for the theoretical evicted case (same id).
4. Return `newSessionResult{SessionID: string(id)}, nil` → `{"sessionId":"<id>"}` (AC-1). **Reuse `newSessionResult`** — no new type.

Why `Pool.Lookup` + `Pool.Activate`, never `GetOrCreate`: `GetOrCreate` *creates on miss*, so an unknown id would spawn a fresh claude — breaking AC-2 and divergence 6. `Lookup` gives miss-is-an-error with no side effect; `Activate` gives idempotent resume.

### 3. `session/cancel` seam — `resolveCancelTarget` + `cancelSessionHandler`

The cancel routing is factored into a small, directly unit-testable resolver so AC-4's "resolves onto the correct session's supervisor" is provable without T9's abort logic and so T9 has a named extension point.

```
// resolveCancelTarget maps a session/cancel notification's params onto the
// per-session supervisor. Returns the supervisor handle T9 (#753) will signal,
// or an error when sessionId is missing/malformed/empty or names no session.
func resolveCancelTarget(pool *sessions.Pool, params json.RawMessage) (*supervisor.Supervisor, error)
```

- Behaviour: `decodeSessionID` → `pool.Lookup(id)` (wrap `ErrSessionNotFound`) → return `sess.Supervisor(), nil`.

```
func cancelSessionHandler(pool *sessions.Pool) acp.Handler
// closure: func(ctx, params) (any, error)
```

- Behaviour: `sup, err := resolveCancelTarget(pool, params)`; on `err != nil` return `(nil, err)` — **the notification path logs it and emits no response** (`dispatchNotification`). On success, the abort keystroke is **T9's** job; this ticket returns `(nil, nil)`. A one-line comment marks `sup` as the T9 seam. (`sup` is consumed by `resolveCancelTarget`'s unit test today and by T9's handler tomorrow — not dead.)

The no-response guarantee (AC-4) is **structural**: the transport's `dispatchNotification` discards every notification handler's return. This ticket adds no response-suppression code — registering the handler is the whole mechanism.

### 4. Registration

Two additive lines in the existing `register` closure (`serveACPWithPool:106-108`):

```
register := func(t *acp.Transport) {
    t.Register("session/new", newSessionHandler(pool))     // #761, unchanged
    t.Register("session/load", loadSessionHandler(pool))   // this ticket
    t.Register("session/cancel", cancelSessionHandler(pool)) // this ticket
}
```

`Register` must be called before `Serve` (panics otherwise) — this closure already runs at the right point (`acp.go:106-109`). No new import beyond `internal/supervisor` (already imported by `acp.go`) and `errors` (already imported).

### Data flow

```
session/load (request, has id):
  host ──▶ serveACP ──▶ Transport.dispatchRequest("session/load")
                              │ loadSessionHandler
                              ▼
              decodeSessionID ─(empty/malformed)─▶ CodeInvalidParams
                              │ non-empty id
                              ▼
              pool.Lookup(id) ─(ErrSessionNotFound)─▶ CodeInvalidParams  (no spawn — AC-2)
                              │ found
                              ▼
              pool.Activate(ctx,id)  (idempotent: already-active ⇒ no 2nd claude — AC-1/AC-3)
                              ▼
              {"sessionId":"<id>"} ──▶ writeSuccess ──▶ host

session/cancel (notification, no id):
  host ──▶ serveACP ──▶ Transport.dispatchNotification("session/cancel")
                              │ cancelSessionHandler → resolveCancelTarget
                              ▼
              decodeSessionID → pool.Lookup(id) → sess.Supervisor()   (T9 aborts here)
                              ▼
              (nil, nil)  ──▶  NO response frame  (AC-4, guaranteed by dispatchNotification)
```

## Concurrency model

Unchanged from #761. No new goroutines, no new locks.

- Both handlers run **inline on the transport's single read-loop goroutine** (`Serve`), same as `newSessionHandler`. `Pool.Lookup`/`Pool.Activate`/`Session.Supervisor` are already goroutine-safe (`Lookup`/`Activate` take `Pool.mu`/`capMu`; `Supervisor()` returns an immutable handle). No handler holds a lock across a write or a read.
- `session/load`'s `Activate` uses the handler `ctx` (the transport Serve ctx) for its `WaitForPTY` wait; on shutdown it surfaces `ctx.Err()`. For an already-active session `WaitForPTY` returns immediately (PTY live), so no shutdown-race window.
- `session/cancel` does no blocking work (Lookup + Supervisor are non-blocking), so it cannot stall the read loop even though it dispatches inline.

## Error handling

| Failure | Verb | Behaviour |
|---|---|---|
| `params` absent / not JSON / `sessionId` missing / non-string / **empty** | load | `decodeSessionID` → `*acp.Error{CodeInvalidParams}` → error frame, no spawn. |
| same | cancel | `resolveCancelTarget` returns the `*acp.Error`; handler returns it → **logged, no response** (`dispatchNotification`). |
| Unknown non-empty id | load | `Lookup` → `ErrSessionNotFound` → `CodeInvalidParams` error frame, **no `Activate`, no claude spawned** (AC-2). |
| Unknown non-empty id | cancel | `Lookup` → `ErrSessionNotFound` → wrapped error → **logged, no response**. |
| `Activate` error (e.g. ctx cancelled mid-spawn) | load | wrapped → `CodeInternalError`, detail logged not leaked. Not reachable for an already-active session. |
| Known id, healthy | load | `{"sessionId":"<id>"}`, exactly one claude (the existing one). |
| Known id, healthy | cancel | `(nil,nil)` → no response; supervisor resolved for T9. |

`ErrPoolNotRunning` cannot reach these handlers: `serveACPWithPool` gates serving on `pool.Ready()` (#761), so `Lookup`/`Activate` always run against a live pool. Neither verb calls `Create`, so the #761 stuck-session concern does not arise.

## Testing strategy

Table-driven where natural, stdlib `testing`, extend the existing `cmd/pyry/acp_test.go` harness (paired `io.Pipe`s + `serveACPWithPool` + `fakeClaudeScript` argv-line counting). No real claude, no TTY. All new tests; no production test-only helpers added to non-test files.

**`resolveCancelTarget` unit test (direct, no transport)** — the strongest AC-4 proof:
- Stand up a fake-claude pool, `session/new`-equivalent via `pool.Create(ctx,"")` (or reuse the transport to mint one), get `id`.
- `resolveCancelTarget(pool, {"sessionId":id})` returns a non-nil `*supervisor.Supervisor` **identical** (pointer equality) to `pool.Lookup(id).Supervisor()` → "resolves onto the correct session's supervisor".
- Unknown id → error; empty/missing/malformed `sessionId` → error (an `*acp.Error` with `CodeInvalidParams`).

**`decodeSessionID` unit test (table-driven)**: cases — valid `{"sessionId":"x"}` → id, nil; `{}` (missing) → `CodeInvalidParams`; `{"sessionId":""}` (empty, the bootstrap-trap guard) → `CodeInvalidParams`; `{"sessionId":5}` (non-string) → `CodeInvalidParams`; `null`/garbage params → `CodeInvalidParams`.

**`session/load` end-to-end over the transport** (extend the `TestACP_SessionNew…` harness):
- *Known id resumes, one claude (AC-1):* `session/new` → id; then `session/load` `{"sessionId":id}` → success response `{"sessionId":id}`; argv file still has **exactly one** line (no second spawn).
- *Divergence 6 (AC-3):* after the above, a **second** `session/load` of the same id → success; argv file **still exactly one** line. (The argv-line count IS the "exactly one claude for that id" assertion — same mechanism as #761's divergence-6 test.)
- *Unknown id errors, no spawn (AC-2):* `session/load` `{"sessionId":"<random-uuid>"}` on a fresh pool (no `session/new` first) → error response with `code == CodeInvalidParams`; argv file has **zero** lines (bootstrap evicted, nothing spawned).
- *Malformed/empty sessionId:* `session/load` with `{}` and with `{"sessionId":""}` → error response `CodeInvalidParams`; no spawn.

**`session/cancel` end-to-end (AC-4)** — assert the *absence* of a response:
- `session/new` → id. Send `session/cancel` as a **notification** (frame with `method` + `params`, **no `id`**). Then send a follow-up request that does reply (e.g. a second `session/new` with `id:2`, or a `session/load` with `id:2`) and assert the **next** frame read carries `id:2`, not a cancel reply — proving the notification emitted nothing. (Reading-with-a-timeout-and-expecting-nothing is racy; the "next frame is the follow-up's reply" pattern is deterministic.)
- Known-id cancel logs no handler error (assert the stderr buffer has no `notification handler error` line); unknown-id cancel logs one (assert it does) — and **neither** writes a frame to stdout.

## Out of scope (explicit)

- The abort keystroke delivery for cancel (`Cancel` internal command, #707) — **T9 (#753)**. This ticket lands only the resolution seam (`resolveCancelTarget` → `*supervisor.Supervisor`).
- Cross-process `session/load` (reloading an id a prior `pyry acp` process created) — deferred; needs `RegistryPath` + reconcile wired into the ACP standup (a future ticket; additive).
- Honouring a caller-supplied `cwd` — deferred with its `security-sensitive` flip (#761's note). This ticket accepts no caller path; it resolves existing ids only. **Not security-sensitive.**
- `initialize` handshake, turn delivery, transcript reconciliation, rotation/eviction — none wired.

## Open questions

- **`session/load` success-result shape.** Reusing `newSessionResult` echoes `{"sessionId":id}`, which is a well-formed ACP success result (satisfies AC-1). ACP's `LoadSessionResponse` may formally be an empty/mode-bearing object; if host integration shows a stricter shape is required, it's a trivial result-struct swap — no design change. Flagged, not blocking.
- **`sessionId` validation depth.** The decode rejects empty; any non-empty string flows to `Lookup`, which gives the correct miss semantics for garbage ids (unknown → error, no spawn). Calling `sessions.ValidID` at decode to reject non-canonical shapes earlier is optional and cosmetic — it changes the error code source (`CodeInvalidParams` at decode vs `CodeInvalidParams` after `Lookup`) but not the observable behaviour. Left out for simplicity; add only if a host needs a distinct "malformed" vs "unknown" signal.
- **Cancel sent as a request (with `id`).** ACP defines `session/cancel` as a notification, so conformant hosts omit `id`. If a host sent it *with* an `id`, the transport would route it to `dispatchRequest` and reply `{"result":null}` — harmless, and not this ticket's concern (the AC is scoped to the notification path, which the transport guarantees is response-free). No guard needed.
