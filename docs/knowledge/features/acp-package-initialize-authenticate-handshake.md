# `initialize` + `authenticate` handshake (#747)

The two methods every ACP connection opens with, before any session exists.
`initialize` negotiates the protocol version and declares pyry's capabilities;
`authenticate` is an optional follow-up. Both are **stateless pure functions** of
their params — no pool, no goroutines, no shared state — living in a new
`cmd/pyry/acp_handshake.go` (**package `main`**, not `internal/acp`, keeping the
transport method-agnostic). `registerHandshake(t)` binds both in `serveACPWithPool`'s
existing `register` closure, ahead of the `session/*` handlers.

**Divergence 5 is the whole point.** pyry runs claude on the daemon's own machine,
so it never asks the host to read/write files (`fs/*`) or create terminals
(`terminal/*`). The handshake declares pyry needs no such client capabilities —
expressed **structurally, not as a wire field**, because ACP's `InitializeResponse`
has *no* "agent requires client capability X" field. Two ways: (1) the result
carries only `protocolVersion` + `agentCapabilities` + `authMethods`, with nowhere
to request a host capability; (2) pyry issues no `fs/*`/`terminal/*` outbound
`Call`s. The host's `clientCapabilities` are intentionally **not modelled** in the
params struct — an unmodelled field is dropped on decode, which *is* "tolerate a
host that offers fs/terminal and never use them."

- **`SupportedProtocolVersion = 1`, returned unconditionally.** One supported
  version ⇒ nothing to negotiate; every real client advertises `>= 1`, so `1` is
  always a valid (`<=` client) response. The `min(client, agent)` clamp is deferred
  until pyry speaks a second version (evidence-based). Reconcile the literal with
  ADR #745 (OPEN) when it merges.
- **`initializeParams` decodes only `protocolVersion`, as a shape gate.** A params
  value that isn't a JSON object (e.g. an array) fails to unmarshal → `-32602`
  `CodeInvalidParams`; empty/absent params is tolerated (zero-value defaults).
- **Minimal-safe agent capabilities — all false today.** ACP capabilities are
  feature flags, not a method list; core methods (`session/new`, `session/prompt`)
  are baseline-mandatory and *never* capability-gated. An *optional* flag is turned
  on only when its backing ticket has landed the full contract. `loadSession: false`
  and `promptCapabilities.{image,audio,embeddedContext}: false`; `mcpCapabilities`
  is omitted entirely (backing method #748 not landed), and `set_mode` /
  `set_config_option` have **no `agentCapabilities` flag at all** — ACP carries no
  mode/config flag in `initialize`, so #753's single-mode pin is advertised in the
  `session/new`/`session/load` responses instead (see
  [session/cancel actuation + mode/config pin](#sessioncancel-actuation--modeconfig-pin-753)),
  leaving this handshake **unchanged**. `authMethods` is a non-nil `[]authMethod{}`
  so it marshals to `[]`, not `null`.
- **`loadSession` stays `false` despite `session/load` already being registered
  (#762) — a deliberate, honest under-advertisement.** ACP's `loadSession`
  capability promises the agent *replays conversation history via `session/update`
  on load*. That replay is unwired (Phase 2 / #596), so #762's `session/load` is a
  partial within-process `Lookup`+`Activate`, not the full contract. **Coordination
  point: #748 flips `loadSession` to `true` when it lands the replay semantics.**
- **`authenticate` is a no-op stub.** Local `pyry acp` speaks to a co-located,
  user-launched host over stdio — no token/secret/credential in play — so it ignores
  params and returns `authenticateResult{}` → `{}` on the wire (object-shaped
  `AuthenticateResponse`, chosen over `null` for forward-compatibility). A code
  comment marks the deliberate stub.

Stateless ⇒ the handlers register **without a pool**, which is the structural proof
of "succeeds before any session exists, holds no session state." **Not
security-sensitive** — local trusted-host surface; the credential-free stub adds no
gate and the capability declaration only *minimizes*. Full per-ticket detail in
[`codebase/747.md`](../codebase/747.md).
