# Fake Codex App-Server Binary

`internal/e2e/internal/fakecodex` (#2590) is a test-only `package main` that
stands in for `codex app-server` at Codex **0.156.1**, the version pyrycode
targets. It exists so Codex support (#2583 family — the `internal/codexsup`
client #2591, the notification mapping #2584, the pool runner #2620) can run
its tests with no Codex account. It is a sibling of
[fakeclaude](fakeclaude-binary.md), same layout convention: env-only
configuration, documented in the package comment.

It is not wired into `internal/e2e/harness.go` — each consumer ticket builds
it by import path instead, mirroring how `ensureFakeClaudeBuilt` builds
fakeclaude. `cmd/pyry/codex_runner_test.go` (#2620, the pool runner) does
this the same way `internal/codexsup`'s own tests (#2591) do.

## Wire

Line-delimited JSON on stdin/stdout, no `jsonrpc` field: a request carries
`id` and `method`, a notification carries only `method`, and the fake's own
server requests (`item/commandExecution/requestApproval`) carry both — the
client's reply to one of those is an `id`-only frame. `server.handle`
classifies an incoming frame this way and routes it to `handleRequest` or
the notification/response path.

## Configuration

- `CODEX_HOME` — echoed back as `initialize`'s `codexHome`. Defaults to
  `$HOME/.codex`, matching the real server.
- `FAKECODEX_TURN_LOG` (#2586) — a file path. Each `turn/start`'s raw params
  are appended to it as one line, before the response, so a test can read
  the model/effort/posture overrides a turn actually carried — the fake had
  no way to observe those before this. Written at `0600`; unset by default,
  so a test that doesn't need it pays nothing.

Per-turn behaviour is otherwise selected by markers in the turn's input text,
so one process serves every scenario a consumer's test needs.

## Markers

- `[fakecodex:approval]` — before the agent message, the turn opens a
  `commandExecution` item and sends one
  `item/commandExecution/requestApproval` server request (`turn.approval`).
  After the client answers, the fake sends `serverRequest/resolved` and
  completes the item: `accept`/`acceptForSession` → `completed` with exit
  code 0; any other decision → `declined`.
- `[fakecodex:withdraw]` (#2587) — like `approval`, but the fake sends
  `serverRequest/resolved` at once, before the client answers, and completes
  the item as `declined` itself. A response the client still sends for that
  request afterwards is not an error — the fake appends it to
  `FAKECODEX_TURN_LOG` as `{"lateResponse":<id>}` instead of rejecting it,
  so a consumer test can assert the client wrote *nothing* late by checking
  the log stays free of that line, rather than by racing the fake for a
  protocol error. This is the marker a withdrawn-approval test needs:
  `approval` always resolves *after* the answer, which cannot exercise a
  consumer's "Codex resolved it before I could" path at all.
- `[fakecodex:hold]` — after `turn/started` the turn blocks until
  `turn/interrupt` names it.

A turn without a marker never sends a server request, and streams straight
through to `turn/completed` with status `completed`.

### Interrupt only affects a turn that is waiting

`turn/interrupt` returns `{}` for any running turn id, but only a turn
parked in the hold marker or in `turn.approval`'s decision wait actually
selects on its interrupt channel and ends `turn/completed interrupted`. A
plain streaming turn (no marker) has already raced past that select by the
time an interrupt could arrive in practice, and if it somehow didn't, the
interrupt would still not touch it — the turn keeps streaming and completes
`completed` regardless. A consumer test that wants to exercise interrupt
must give the turn something to interrupt, i.e. use `[fakecodex:hold]` (or
catch it mid-approval-wait); interrupting an unmarked turn is not a
meaningful test of the interrupt path.

The fake writes the `turn/interrupt` response before the interrupted turn's
`turn/completed` (`handleRequest` replies, then runs the handler's
continuation that closes the interrupt channel). This is an implementation
detail of the fake, not an observed property of the real server — consumers
should not depend on the ordering between the two.

### A completing turn's removal and its `turn/completed` are one step (#2636)

That non-dependency doesn't extend to a turn `turn/interrupt` finds already
gone. `turn.run`'s completed path used to delete the turn from `s.turns` and
send `turn/completed` as two separate steps, only the delete under `s.mu`.
A `turn/interrupt` for that id, landing in the gap between them, found the
turn already absent and answered `-32602` before the client had read
`turn/completed` — overtaking the notification it was actually racing
against. `turn.run` now holds `s.mu` across both the delete and
`t.complete("completed")` (`send` only takes `writeMu`, so nesting it inside
`s.mu` doesn't invert lock order with `turnInterrupt`, which also takes
`s.mu` to look the turn up). `turnInterrupt` now either still finds the turn
(interrupt succeeds; the turn completes normally next) or finds it gone with
`turn/completed` already written first. A consumer that infers "the turn
already ended" from a `turn/interrupt` refusal — `codexRunner.Interrupt` is
the one that needed this — can now rely on that ordering; see
[codexsup-package.md's matching note](codexsup-package.md#production-wiring--the-cmdpyry-codex-runner-2620).

## Building a frame's payload: `json.RawMessage`, not `[]byte`

A server request's `id` and a response's echoed id must be pre-formed JSON
values embedded verbatim inside a `map[string]any` (e.g.
`serverRequest/resolved`'s `requestId`). Go's `encoding/json` treats a bare
`[]byte` field as binary data and base64-encodes it — `json.Marshal`'s
return type is `[]byte`, so passing that result straight into the map
produces a base64 string instead of the JSON it holds. Wrapping the value
as `json.RawMessage` (a `[]byte` with its own `MarshalJSON` that emits the
bytes unescaped) is what makes `map[string]any` encode it as intended, and
is why `newID`'s callers and `turn.approval`'s `reqID` construct their ids
as `json.RawMessage` rather than plain marshalled bytes. Anything else in
this codebase that assembles a JSON-RPC frame by hand — the `internal/codexsup`
client included — needs the same care.

## Testing

`main_test.go` builds the binary once per test binary (`TestMain`) and
drives it over real stdio with raw frames — no mocked transport. The
per-test helper (`fakeProc.read`/`write`) checks **every method that
actually crosses the wire, in both directions**, against the method groups
extracted live from the committed
`internal/codexsup/codex_app_server_protocol.schemas.json`
(`schemaMethods`), not just a hand-maintained list. `TestMethodNamesInSchema`
separately guards the fake's own declared surface — `requestHandlers`'
keys, `acceptedNotifications`, `emittedNotifications` and
`serverRequests` — against the same schema groups.

Checking only the declared lists would leave a gap: a handler could return
one method name while the code actually sends a different one, and the
static check would still pass. Because the wire-level check inspects every
frame as it is read or written, a test that wants to send a method the fake
doesn't recognize has to write raw bytes past the normal `fakeProc.write`
helper — there is no way to get an unlisted method onto the wire through
the ordinary call path without the check catching it.

## Related

- [fakeclaude-binary.md](fakeclaude-binary.md) — the layout and
  env-only-configuration convention this fake follows.
- `internal/codexsup/SCHEMA.md` — how the pinned 0.156.1 schema bundle is
  regenerated; not touched by this ticket.
- [codexsup-package.md § Approvals reach the permission modal
  (#2587)](codexsup-package.md#approvals-reach-the-permission-modal-2587) —
  `cmd/pyry/codex_approval_test.go` is the consumer that drives
  `[fakecodex:withdraw]`.
- [e2e-harness.md](e2e-harness.md) — where a consumer wires a fake binary
  into `Harness`.
