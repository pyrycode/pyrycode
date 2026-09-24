# Fake Codex App-Server Binary

`internal/e2e/internal/fakecodex` (#2590) is a test-only `package main` that
stands in for `codex app-server` at Codex **0.156.1**, the version pyrycode
targets. It exists so Codex support (#2583 family — the `internal/codexsup`
client #2591, the notification mapping #2584, the pool runner #2585) can run
its tests with no Codex account. It is a sibling of
[fakeclaude](fakeclaude-binary.md), same layout convention: env-only
configuration, documented in the package comment.

It is not wired into `internal/e2e/harness.go` yet — that is each consumer
ticket's job, mirroring how `ensureFakeClaudeBuilt` builds fakeclaude by
import path.

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

No other env knob. Per-turn behaviour is selected by markers in the turn's
input text instead, so one process serves every scenario a consumer's test
needs.

## Markers

- `[fakecodex:approval]` — before the agent message, the turn opens a
  `commandExecution` item and sends one
  `item/commandExecution/requestApproval` server request (`turn.approval`).
  After the client answers, the fake sends `serverRequest/resolved` and
  completes the item: `accept`/`acceptForSession` → `completed` with exit
  code 0; any other decision → `declined`.
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
- [e2e-harness.md](e2e-harness.md) — where a consumer wires a fake binary
  into `Harness`.
