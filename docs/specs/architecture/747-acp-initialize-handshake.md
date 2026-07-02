# Spec #747 — ACP `initialize` handshake declaring minimal client capabilities

**Ticket:** [#747](https://github.com/pyrycode/pyrycode/issues/747) — feat(acp): initialize handshake declaring minimal client capabilities
**Epic:** [#600](https://github.com/pyrycode/pyrycode/issues/600) — `pyry acp` as a thin adapter over the shared remote-head core
**Size:** **XS** (downgraded from S — the minimal-safe capability set makes the production surface trivial: one constant, ~4 small wire structs, two tiny handlers, one registration helper, a one-line edit to `serveACP`). Not security-sensitive (local trusted-host stdio surface; the `authenticate` handler is a credential-free no-op stub — see § Non-goals).

---

## Files to read first

- `internal/acp/acp.go:53-59` — `Handler` type: `func(ctx, params json.RawMessage) (result any, err error)`. This is the exact contract both new handlers implement.
- `internal/acp/acp.go:118-129` — `Register(method, h)`: register-before-Serve, panics on duplicate/after-start. The seam both handlers bind against.
- `internal/acp/acp.go:221-244` — `dispatchRequest`: how a handler's `(result, nil)` becomes a success frame and how a returned `*Error` maps to the wire error. Confirms you return a *value to marshal*, not a pre-encoded frame.
- `internal/acp/jsonrpc.go:8-39` — `Code*` constants + `Error`/`NewError`. Use `acp.CodeInvalidParams` for a malformed `initialize` params object.
- `cmd/pyry/acp.go:56-95` — `serveACP`: the composition root. The single edit site (register handlers between `acp.New(...)` and `t.Serve(ctx)`); note the stale `// registers NO handlers (AC#2)` comment to update.
- `cmd/pyry/acp_test.go:13-51` — `testLogger` helper + the `serveACP`-driven single-shot test style to mirror (feed a reader, run to EOF, assert on the `stdout` buffer). New tests append here.
- Vault: `Structured-Event Bridge — internal model and ACP mapping` § "ACP taxonomy" + "Divergence 5" (QMD `second-brain/.../structured-event-bridge-acp-mapping.md`) — the **interim capability reference** (ADR #745 is still OPEN; reconcile when it lands). Divergence 5 is the point of this ticket: pyry needs no `fs/*` / `terminal/*` client capabilities.
- `docs/knowledge/features/acp-package.md` — transport concurrency model, diagnostics discipline, the "internal/acp knows nothing about any concrete ACP method" boundary that drives the *where-do-handlers-live* decision below.

---

## Context

Epic #600 builds `pyry acp` — a thin ACP adapter over a supervised interactive claude session. ACP opens every connection with an `initialize` request that negotiates the protocol version and exchanges capabilities, optionally followed by `authenticate`. #755/#756 landed the JSON-RPC 2.0 transport + dispatch table and the `pyry acp` subcommand that serves it, registering **no** handlers. This ticket registers the first two: `initialize` and `authenticate`.

**Divergence 5 is the whole point.** pyry runs claude on the daemon's own machine, with its own disk and terminal. It never asks the host to read/write files (`fs/*`) or create/run terminals (`terminal/*`). The handshake must declare pyry needs no such client capabilities and advertise only the agent capabilities whose backing methods this epic has actually delivered.

**Stateless.** `initialize` succeeds before any session exists and holds no session state. No claude is spawned. Session creation is #748.

---

## Design

### Where the handlers + wire types live: `package main` (`cmd/pyry`)

`internal/acp` is **deliberately method-agnostic** — its package doc states it "owns framing, ids, and error codes — it knows nothing about any concrete ACP method (session/*). Later tickets register handlers against the dispatch table as pure registrations." Putting `initialize`/`authenticate` wire types or bodies *inside* `internal/acp` would violate that boundary. Two tiny handlers do not justify a new package (simplicity-first; a future ticket can extract one when the handler count warrants it).

→ New file **`cmd/pyry/acp_handshake.go`** (package `main`) holds the constant, the wire structs (all unexported — no cross-package consumer), the two handler funcs, and a `registerHandshake` helper. `serveACP` calls the helper. Tests append to `cmd/pyry/acp_test.go` and drive frames **through the transport** via `serveACP`.

### Registration seam

`serveACP` gains one call between construction and serve:

```go
t := acp.New(pr, stdout, logger)
registerHandshake(t) // initialize + authenticate (was: "registers NO handlers")
err := t.Serve(ctx)
```

```go
// registerHandshake binds the ACP handshake methods on the transport. Called
// once by serveACP before Serve. Kept as a helper so the composition root reads
// as a list of capability registrations as later session/* tickets add theirs.
func registerHandshake(t *acp.Transport)
```

Update the stale `// registers NO handlers (AC#2)` comment at `cmd/pyry/acp.go:80`. `TestServeACP_EOFReturnsNil` / `_BlankLinesThenEOF` stay green (no matching input frame ⇒ still empty stdout), but their assertion messages ("no handlers registered…") are now inaccurate — reword them to "no request frame fed ⇒ empty stdout".

### Protocol version

```go
// SupportedProtocolVersion is the single ACP protocol version pyry speaks.
// ACP protocolVersion is an integer (u16 on the wire); v1 is current.
// Reconcile the literal with ADR #745 when it merges (interim source: the vault
// ACP-mapping doc).
const SupportedProtocolVersion = 1
```

The `initialize` handler returns `SupportedProtocolVersion` **unconditionally**. Version *negotiation* (returning `min(client, agent)` once pyry speaks more than one version) is deferred — there is exactly one version to negotiate today, and every real client advertises `>= 1`, so returning `1` is always a valid response (≤ the client's version). Evidence-based: no second version exists to clamp against. Note this as an open question, not a TODO in code.

### Wire types (contract sketch — exact ACP camelCase field names)

Request params (decode only what we use; `json.Unmarshal` silently ignores the rest, which *is* how pyry "tolerates a host that offers `fs`/`terminal` and never uses them"):

```go
// initializeParams is the subset of the ACP InitializeRequest pyry reads. The
// host's clientCapabilities (fs/terminal) are intentionally NOT modelled: an
// unmodelled field is ignored on decode, which is exactly "accept and never use".
type initializeParams struct {
    ProtocolVersion int `json:"protocolVersion"`
}
```

Result (the only fields pyry emits — see § Divergence 5 below for why there is no client-capability field here):

```go
type initializeResult struct {
    ProtocolVersion   int               `json:"protocolVersion"`
    AgentCapabilities agentCapabilities `json:"agentCapabilities"`
    AuthMethods       []authMethod      `json:"authMethods"` // always [] today, never nil
}

type agentCapabilities struct {
    LoadSession        bool               `json:"loadSession"`        // false — session/load is #748
    PromptCapabilities promptCapabilities `json:"promptCapabilities"` // all false — rich prompt content is #748
}

type promptCapabilities struct {
    Image           bool `json:"image"`
    Audio           bool `json:"audio"`
    EmbeddedContext bool `json:"embeddedContext"`
}

type authMethod struct { // no methods advertised today; kept so authMethods marshals as a typed []
    ID          string `json:"id"`
    Name        string `json:"name"`
    Description  string `json:"description,omitempty"`
}
```

`authMethods` must marshal as `[]`, not `null` — initialize it to an empty non-nil slice (`[]authMethod{}`). A test asserts the literal `[]`.

`mcpCapabilities` and any `set_mode`/`set_config_option` flags are **omitted** — their backing methods (#748/#753) have not landed, and the minimal-safe rule says advertise only what's delivered. When those tickets land they amend this struct.

### Divergence 5 is structural, not a field

ACP's `InitializeResponse` has **no** "agent requires client capability X" field. The agent never declares required client capabilities; it simply uses whatever the client offered, or doesn't. So "pyry requires no `fs/*`/`terminal/*`" is expressed two ways, both structural:

1. `initializeResult` carries *only* `protocolVersion` + `agentCapabilities` + `authMethods` — there is no place in it to request a client capability.
2. pyry issues no `fs/*` or `terminal/*` outbound `Call`s (nothing in this ticket, nothing in the epic's plan for the local-disk runtime).

AC-2(b) ("no host filesystem or terminal capability is requested") is therefore tested by asserting the marshalled result contains no `fs`/`terminal`/`readTextFile`/`writeTextFile` keys — which holds by construction — alongside the positive capability-set assertions in AC-2(a).

### `initialize` handler behaviour

- If `params` is non-empty, `json.Unmarshal` into `initializeParams`; on failure return `acp.NewError(acp.CodeInvalidParams, "invalid initialize params")`. Empty/absent params is tolerated (uses zero-value defaults). The transport already guarantees `params` is valid JSON or nil (see `handleLine`), so this only rejects a well-formed-but-wrong-shape object.
- Return `initializeResult{ProtocolVersion: SupportedProtocolVersion, AgentCapabilities: <minimal set above>, AuthMethods: []authMethod{}}`, nil.
- Holds no state; safe to call before any session exists (AC-1).

### `authenticate` handler behaviour

Local pyry needs no authentication. The handler ignores params and returns success:

```go
type authenticateResult struct{} // marshals to {} — a spec-compliant AuthenticateResponse (all-optional-fields object)
```

Return `authenticateResult{}, nil`. A code comment documents the deliberate stub (AC-3): *local `pyry acp` speaks to a co-located, user-launched host over stdio; there is no token, secret, or credential in play, so authentication is a no-op that always succeeds.* Empty-object result (`{}`) is chosen over `null` for forward-compatibility with ACP's object-shaped `AuthenticateResponse`.

---

## Concurrency model

None introduced. Both handlers are pure functions of their `params` — no goroutines, no shared state, no locks. The transport dispatches them **inline on its single read-loop goroutine** (see `acp-package.md` § Concurrency), serially, one frame at a time. Neither handler blocks (no I/O, no claude, no outbound `Call`), so neither can stall the read loop.

---

## Error handling

| Condition | Outcome |
|---|---|
| `initialize` with malformed params object | `*Error{CodeInvalidParams}` → `-32602` on the wire (id echoed) |
| `initialize` with absent/empty params | Success with defaults (tolerated) |
| `initialize` well-formed | Success frame with the minimal capability result |
| `authenticate` (any params) | Success frame with `{}` |
| unknown method (e.g. `session/new` before #748) | Transport returns `-32601` method-not-found — unchanged, not this ticket's concern |

Handlers never panic and never log `params` content (transport diagnostics discipline — params may carry user material; here they don't, but the rule stands). A plain (non-`*Error`) error would map to `-32603`; neither handler returns one.

---

## Testing strategy

Append to `cmd/pyry/acp_test.go` (reuse `testLogger`; mirror the `serveACP(ctx, strings.NewReader(line), &stdout, logger)` → parse-`stdout`-frame style). Drive frames **through the transport**, never call the handler funcs directly (per the ticket's registration-seam note). Each test feeds one request line, runs `serveACP` to EOF, and unmarshals the single response object off the `stdout` buffer.

Scenarios:

- **initialize → well-formed result (AC-1, AC-2a):** feed `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true}}}`. Assert: response id `1`; `result.protocolVersion == 1`; `agentCapabilities.loadSession == false`; `promptCapabilities.{image,audio,embeddedContext}` all `false`; `authMethods` is the empty array `[]` (not `null`). Note the host *offered* fs+terminal here — the test proves pyry tolerates and ignores them.
- **initialize → no client-capability request (AC-2b):** on the same response, assert the raw result JSON contains none of the substrings `"fs"`, `"terminal"`, `"readTextFile"`, `"writeTextFile"` — pyry's result requests no host capability.
- **initialize before any session / stateless (AC-1):** the above run already exercises "succeeds with no session in existence"; assert simply that the call returns a success frame (no `error` member) with fresh transport state.
- **initialize with empty params tolerated:** feed `…"method":"initialize"` with no `params` (or `params:{}`) → still a success frame with the same capability set.
- **initialize with malformed params → -32602:** feed `…"params":[1,2,3]` (array, not object) → error frame `code == -32602`, id echoed.
- **authenticate → success (AC-3):** feed `{"jsonrpc":"2.0","id":2,"method":"authenticate","params":{"methodId":"whatever"}}` → success frame, id `2`, `result == {}` (empty object, no `error` member).
- **regression:** confirm the two existing `TestServeACP_*` EOF/blank-line tests still pass (registration doesn't emit frames absent a request) after rewording their stale assertion messages.

`make check` (vet, `-race`, staticcheck, substrate-guard) green (AC-4). Substrate-guard stays trivially green — `cmd/pyry/acp_handshake.go` names no claude-TUI substrate literals (it drives no claude), so no `cmd/substrate-guard` allowlist entry is needed.

---

## Non-goals / scope boundaries

- **No claude, no session** — stateless handshake only. Session lifecycle is #748.
- **No version negotiation logic** — single supported version returned unconditionally; the `min(client, agent)` clamp lands when a second version exists.
- **No real authentication** — deliberate no-op stub; the epic's security boundary lives on the mobile wire and in the permission proxy (#752), not this handshake. Confirmed not security-sensitive (transport-floor local-trusted-host decision, #746/#755/#756).
- **No `mcpCapabilities` / `set_mode` / `set_config_option` advertisement** — backing methods (#748/#753) haven't landed; the delivering ticket amends `agentCapabilities`.
- **No knowledge-base doc** — `docs/knowledge/codebase/747.md` is the documentation phase's deliverable, not the developer's.

---

## Open questions

1. **Protocol version literal.** Pinned to `1` from the vault interim reference; ADR #745 is OPEN. If #745 fixes a different integer (or a versioning scheme), reconcile `SupportedProtocolVersion` when it merges. Low risk — v1 is the current ACP wire version.
2. **`authMethods` shape once auth exists.** Empty today. If a later ticket ever adds a real auth method, it populates this array *and* the `authenticate` handler stops being a no-op. Out of scope now; the `authMethod` struct is shaped for that future without over-building.
3. **Version-negotiation clamp.** Deferred (see Design). Revisit only when pyry speaks a second protocol version — then the handler returns `min(requested, SupportedProtocolVersion)` and gains a downgrade test.
