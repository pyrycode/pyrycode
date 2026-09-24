# `internal/codexsup` — Codex app-server connection and thread lifecycle

Standalone client for `codex app-server` (#2591, target Codex **0.156.1**), the
first slice of Codex support (#2583 family). It spawns the process, drives the
`initialize`/`initialized` handshake over stdio, and starts, resumes and
interrupts turns on one Codex thread. Nothing in `cmd/pyry` imports it yet —
mapping server notifications into [`turnevent`](turnevent-package.md) is #2584,
and crash backoff, respawn and pooling multiple clients is #2585.

It reuses [`internal/acp`](acp-package.md)'s `Transport` unchanged for framing
and dispatch, and is tested against
[the fake app-server](fakecodex-binary.md) plus an in-memory peer, with no
Codex account. The wire contract is the committed
`internal/codexsup/codex_app_server_protocol.schemas.json` (`internal/codexsup/SCHEMA.md`
covers regeneration).

## Deny-by-default is the central property

No default path — the one taken when `Config.OnServerRequest` is nil — ever
answers a server request with an acceptance. `serverrequest.go`'s
`declineFor` is a closed switch over the five approval methods with a
schema-shaped decline/denied/empty-grant result; every other server request
(the other five methods at 0.156.1: `item/tool/requestUserInput`,
`mcpServer/elicitation/request`, `item/tool/call`,
`account/chatgptAuthTokens/refresh`, `attestation/generate`) gets a JSON-RPC
`-32601` error. `cancel`/`abort` are deliberately not used as a decline —
both also interrupt the running turn, which a passive default should not do.

Because every method in `methods.go`'s `serverRequests` is registered on the
transport before `Serve` starts, an unlisted future server request cannot
reach this table at all: `acp.Transport` answers an unregistered request
method-not-found on its own. The decline table can only get narrower or wider
deliberately, at a version bump, never by omission.

**Who answers approvals is still open.** The runner that owns posture
(#2585) will decide whether it wires `OnServerRequest` to a real operator
prompt or leaves the default decline in place for some deployment shapes.
The deny-by-default posture may be worth an ADR once that lands.

### The ticket's own literal for the legacy approvals was schema-invalid

`applyPatchApproval` and `execCommandApproval` predate the newer
`item/*/requestApproval` methods and take a `ReviewDecision`, not a
`{"decision":"decline"}` shape. The ticket and the first design both wrote
their default as `{"decision":"denied"}` — a plausible guess that turned out
not to exist in the 0.156.1 schema: `ReviewDecision`'s string arms are
`approved`, `approved_for_session`, `approved_mcp_policy_amendment`,
`timed_out` and `abort`, and a denial is the object arm
`DeniedReviewDecision` (`additionalProperties: false`, `rejection`
required). The verifier caught this on PR #2606 as a MUST FIX; the fix
(`b00fe20e`) is `{"decision":{"denied":{"rejection":"…"}}}`.

The lesson that outlives the ticket: a hand-written literal for a schema
response, even one that reads plausibly and even one written into the
architecture doc, is not evidence it matches the schema. `serverrequest_test.go`'s
`TestDefaultDeclinesMatchSchema` now resolves each server-request method's
response definition from the committed schema (`XParams` → `XResponse` via
the `ServerRequest` arm) and validates the literal wire value against it with
a small draft-07-subset validator (`$ref`, `allOf`/`anyOf`/`oneOf`, `enum`,
`type`, `properties`, `required`, `additionalProperties:false`, `items`);
`TestSchemaValidatorRejectsBareDenied` is the negative control proving the
validator would have caught the original shape. Any package hand-assembling
a JSON-RPC response against a committed schema should validate the literal
against the schema in a test, not just against a human reading of it.

## `acp.Transport.Call` does not fail when `Serve` returns

`Transport.Call` blocks only on its own `ctx` — it has no idea the
connection died, so a call in flight when the app-server process exits (or
the stream breaks) would hang forever without help from this package. The
fix lives entirely in `client.go`, not in `acp`: `Client` keeps its own
`exitCtx`, cancelled once the lifecycle goroutine (`run`) has drained
`Serve` and recorded the exit. Every call wraps its caller `ctx` with
`context.AfterFunc(exitCtx, cancel)` before handing it to `Transport.Call`,
so an in-flight request is cancelled the moment the process is known gone,
and a call issued after exit is cancelled immediately (`AfterFunc` on an
already-done context fires at once). The returned error always wraps
`ErrExited` plus the process's exit error, distinguishing "the process
died" from an ordinary call failure.

One consequence documented on `call` rather than hidden: a response that
lands on the wire just as the process exit is being observed can still be
reported as `ErrExited`, because `Transport.Call`'s `select` may pick the
cancelled context over the delivered result. A caller that retries a failed
call against a respawned process (#2585's job) has to tolerate the
possibility that the original request actually ran.

## Stdout goes through an `io.Pipe`, not straight to the transport

`cmd.Stdout` is a pipe writer; the transport reads the pipe reader. A
separate wait goroutine calls `cmd.Wait()` and only then closes the pipe
writer, so the transport reaches EOF strictly after every byte the child
wrote has been delivered — no trailing frame (e.g. the last `turn/completed`
before an expected exit) is lost to a race between `Wait` returning and the
last read. `kill()` (used both on handshake failure and on a broken stream)
closes the pipe reader with `ErrExited` so `os/exec`'s stdout copier can't
block forever if the transport has already stopped reading; `cmd.WaitDelay`
(5s) bounds the SIGTERM-to-SIGKILL escalation the same way, in case a
descendant process is still holding stdout open.

## Testing

`client_test.go` builds `fakecodex` by import path, the same `TestMain`
pattern `fakecodex` itself uses to build against its own dependencies (see
[fakecodex-binary.md](fakecodex-binary.md)). Two harnesses:

- Against the fake binary: handshake version, thread start/resume,
  interrupt (needs `[fakecodex:hold]` — see fakecodex's own note on why an
  unmarked turn can't exercise interrupt), approval with no handler and with
  a deferred handler, and killing the child externally to prove `Done`/`Err`
  surface a real exit.
- Against an in-memory peer (`io.Pipe` pairs, no process): every frame the
  client sends is checked against the schema's `ClientRequest`/
  `ClientNotification` groups (the wire half of the method-name guarantee —
  `TestMethodNamesInSchema` is the static half, mirroring `fakecodex`'s own
  test of the same name), default declines for all ten `ServerRequest`
  methods table-driven, and a pending call across a peer close proving it
  returns an `ErrExited`-wrapped error instead of hanging.

No test makes a live Codex call — the whole point of building against the
committed schema and the fake is that this package's tests run with no
Codex account.

## Related

- [acp-package.md](acp-package.md) — the reused `Transport`; its classifier,
  `Responder`/`ErrDeferred` deferred-answer mechanism, and the "`Call` must
  not run on `Serve`'s goroutine" rule all apply here unchanged.
- [fakecodex-binary.md](fakecodex-binary.md) — the test double this
  package's tests run against, including the `json.RawMessage` framing
  pitfall for anything assembling a JSON-RPC frame by hand.
- `internal/codexsup/SCHEMA.md` — how the pinned 0.156.1 schema bundle is
  regenerated.
