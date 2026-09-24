# #2591 — codexsup: Codex app-server connection and thread lifecycle

## Files read

- `internal/acp/acp.go` → `Transport`, `New`, `Register`, `Serve`, `Call`, `handleLine`, `dispatchRequest`, `dispatchNotification` — the reused JSON-RPC transport. Classifier routes `id`+`method` to the request path; unregistered notifications are dropped at debug, unregistered requests get method-not-found; `Call` waits only on its own ctx (does not fail when `Serve` returns).
- `internal/acp/responder.go` → `Responder`, `ErrDeferred`, `ResponderFrom` — the deferred-answer mechanism a later operator answer rides on; exactly one frame per id.
- `internal/acp/notify.go` → `Transport.Notify` — sends the `initialized` notification.
- `internal/e2e/internal/fakecodex/main.go` → `requestHandlers`, `turn.run`, `turn.approval` — the test double: markers `[fakecodex:approval]` and `[fakecodex:hold]`, exits 0 on stdin EOF, `userAgent` `pyry_fakecodex/0.156.1`.
- `internal/e2e/internal/fakecodex/main_test.go` → `TestMain`, `schemaMethods`, `TestMethodNamesInSchema` — build-by-import-path pattern and the schema method-group extraction this package's method-name test mirrors.
- `internal/codexsup/codex_app_server_protocol.schemas.json` → `InitializeParams`/`ClientInfo`/`InitializeResponse`, `v2.ThreadStartParams`, `v2.ThreadResumeParams` (`excludeTurns`), `v2.TurnStartParams` (`input`, `model`, `effort`), `v2.TurnInterruptParams`, the five approval `*Response` shapes, `ServerRequest` (10 methods), `ServerNotification` (82 methods).
- `internal/streamsup/runner.go` → `spawnAndWait`; `internal/agentrun/streamrunner/runner.go` — the `exec.CommandContext` + `cmd.Cancel` (SIGTERM) + `cmd.WaitDelay` shutdown idiom mirrored here.
- `docs/knowledge/features/fakecodex-binary.md` § "Interrupt only affects a turn that is waiting" — interrupt tests must use `[fakecodex:hold]`; § "Building a frame's payload" — ids are `json.RawMessage`.

No in-flight feature branch touches `internal/codexsup/` or `internal/acp/`.

## Context

S2a of Codex support (#2583). A standalone client for `codex app-server` (target 0.156.1): spawn, handshake, one thread, turns, interrupt, server-request routing with a deny-by-default posture. Nothing in `cmd/pyry` imports it. Notification→`turnevent` mapping is #2584; crash backoff/respawn/pool is #2585. The deny-by-default server-request posture may deserve an ADR once the runner (#2585) settles who answers approvals — documentation phase to decide.

## Design

Three production files in `package codexsup`:

- `client.go` — `Config`, `Client`, `Start`, lifecycle, handshake, thread/turn methods.
- `serverrequest.go` — `ServerRequest`, the default-decline table.
- `methods.go` — unexported method-name constants and the four method lists (`clientRequests`, `clientNotifications`, `serverRequests`, `serverNotifications`).

### Exported surface

```go
type Config struct {
    Binary, Dir, CodexHome string       // required
    ClientVersion string                // clientInfo.version; "" → "dev"
    OnNotification  func(method string, params json.RawMessage) // may be nil
    OnServerRequest func(req *ServerRequest)                    // may be nil → default decline
    Stderr io.Writer                    // nil → discarded
    Log    *slog.Logger                 // nil → slog.Default()
}

func Start(ctx context.Context, cfg Config) (*Client, error)
func (c *Client) Version() string            // parsed from userAgent
func (c *Client) UserAgent() string
func (c *Client) Done() <-chan struct{}      // closed after the process exited and all output was read
func (c *Client) Err() error                 // exit error; valid after Done
func (c *Client) Stop(ctx context.Context) error
func (c *Client) StartThread(ctx context.Context) (string, error)
func (c *Client) ResumeThread(ctx context.Context, threadID string) error
func (c *Client) ThreadID() string
func (c *Client) StartTurn(ctx context.Context, in TurnInput) (turnID string, err error)
func (c *Client) Interrupt(ctx context.Context, turnID string) error

type TurnInput struct{ Text, Model, Effort string } // Model/Effort omitted when ""

type ServerRequest struct{ Method string; Params json.RawMessage; /* unexported responder */ }
func (r *ServerRequest) Respond(result any) error // from any goroutine, once
func (r *ServerRequest) Decline() error           // the default answer for r.Method

var ErrExited   // wrapped by every call failing because the process is gone
var ErrNoThread // StartTurn before StartThread/ResumeThread
```

Four exported types. `Config` validation: `Binary`, `Dir`, `CodexHome` non-empty, else `Start` errors without spawning.

### Spawn and I/O wiring

`exec.CommandContext(procCtx, cfg.Binary, "app-server")`, `Dir = cfg.Dir`, `Env = os.Environ()` + `CODEX_HOME=<cfg.CodexHome>` (appended last; `os/exec` keeps the last duplicate). `cmd.Cancel` sends SIGTERM; `cmd.WaitDelay` (5 s) escalates to SIGKILL and bounds a descendant holding stdout. `procCtx` is the client's own, cancelled only by `Stop` or a broken read loop — the `Start` ctx bounds only the handshake.

Stdout goes through an `io.Pipe`: `cmd.Stdout = pw`, the transport reads `pr`. A wait goroutine runs `cmd.Wait()` then `pw.Close()`, so the transport sees EOF only after every byte the child wrote has been delivered — no trailing frame is lost, and `Wait` is never racing a pipe read.

The transport part is process-agnostic (`newClient(cfg, r, w, wait, kill)`), which is the seam the in-memory peer tests use.

### Lifecycle goroutine (one per client)

```
serveErr := t.Serve(context.Background())   // returns at EOF, or on a broken stream
if serveErr != nil { kill() }               // over-long line etc.: stop the child, unblock the copier
waitErr := wait()                           // process exit error (nil on exit 0)
c.err = errors.Join(serveErr, waitErr)
cancelExit(); close(done)
```

`kill` for the process path = cancel `procCtx` and `pr.CloseWithError` (so a copier blocked on `pw` unblocks).

### Calls fail on exit

Every request goes through `c.call(ctx, method, params, out)`: it derives a ctx cancelled by `context.AfterFunc(exitCtx, …)`, calls `Transport.Call`, and when `exitCtx` is done returns `fmt.Errorf("codexsup: %s: %w", method, c.exitError())` where `exitError` wraps `ErrExited` and the exit error. A call issued after exit fails immediately (`AfterFunc` on a done ctx fires at once). `exitCtx` is cancelled after `Serve` drained, so a response already on the wire is still delivered.

### Handshake

`initialize` with `{"clientInfo":{"name":"pyrycode","title":"Pyrycode","version":…}}` → decode `userAgent`; version = text after the first `/` up to the first space or `(` (empty if no `/`). Then `Notify("initialized", nil)`. Any failure: `kill`, wait `Done`, return the error.

### Thread and turn

- `StartThread` → `thread/start {cwd}` → `result.thread.id` (error if empty); stored as the current thread.
- `ResumeThread(id)` → `thread/resume {threadId, cwd, excludeTurns:true}` → stores `result.thread.id`.
- `StartTurn` → `turn/start {threadId, input:[{type:"text",text}], model?, effort?}` → `result.turn.id`.
- `Interrupt(turnID)` → `turn/interrupt {threadId, turnId}`.

Thread id guarded by a mutex. Posture fields (`approvalPolicy`, `sandbox`) are not sent — the runner (#2585) owns posture.

### Notifications and server requests

Before `Serve`, every method in `serverNotifications` is registered to a handler that calls `cfg.OnNotification(method, params)` inline on the read loop (order preserved; the callback must not block or call `Client` methods — that would deadlock the read loop, same rule as `acp.Transport.Call`). Every method in `serverRequests` is registered to a handler that builds a `ServerRequest` around `acp.ResponderFrom(ctx)`; with `OnServerRequest` set it hands it over and returns `acp.ErrDeferred`, otherwise it returns the default decline synchronously.

Default decline (`declineFor(method)`):

| Method | Answer |
|---|---|
| `item/commandExecution/requestApproval`, `item/fileChange/requestApproval` | `{"decision":"decline"}` |
| `item/permissions/requestApproval` | `{"permissions":{}}` |
| `applyPatchApproval`, `execCommandApproval` | `{"decision":"denied"}` — superseded, schema-invalid; see Revisions |
| any other server request | JSON-RPC error `-32601` "unsupported server request" |

No default path produces an acceptance.

## Concurrency model

- Goroutines per client: the lifecycle goroutine (runs `Serve`, exits at EOF/broken stream), and in the process path the wait goroutine (exits when `cmd.Wait` returns). `os/exec` adds its stdout copier, bounded by `WaitDelay`.
- `Stop(ctx)`: close stdin (app-server exits on EOF); if `Done` has not closed when `ctx` is done, cancel `procCtx` (SIGTERM → SIGKILL after `WaitDelay`); then wait `Done`, return `Err()`. Idempotent.
- Public calls must not run on the read loop (the callbacks). Deferred `ServerRequest` answers may come from any goroutine; `acp.Responder` guarantees one frame per id.

## Error handling

- Spawn failure: returned from `Start`, nothing left running.
- Handshake failure / ctx expiry: child killed, `Done` awaited, error returned.
- Process exit (stopped or died): `Done` closes, `Err()` carries the `exec` exit error (nil on exit 0); pending and later calls return an error wrapping `ErrExited`.
- Broken stream (line over acp's 16 MiB cap): child killed, `Err()` carries the serve error.
- Response decode failures: wrapped with the method name. Params are never logged (they carry prompt text and commands).

## Testing strategy

`client_test.go`, `TestMain` builds fakecodex by import path (mirroring `fakecodex`'s own `TestMain`).

Against the fake:
- Handshake: `Version()` is `0.156.1`; `Stop` → `Done` closed, `Err()` nil.
- Thread + turn: `StartThread` returns a non-empty id; a plain turn delivers `turn/started`, `item/started`, `item/agentMessage/delta`, `item/completed`, `turn/completed` in order to `OnNotification`.
- Resume: a second client resumes the first's thread id; `ThreadID()` matches.
- Interrupt: `[fakecodex:hold]` turn, `Interrupt` → `turn/completed` status `interrupted`.
- Approval, no handler: `[fakecodex:approval]` → command item completes `declined`.
- Approval, deferred handler: handler stashes the request, a separate goroutine `Respond`s `accept` → item completes `completed`.
- Died on its own: kill the child externally → `Done` closes, `Err()` non-nil.

Against an in-memory peer (`io.Pipe` pairs; every frame the client sends is checked against the schema's `ClientRequest`/`ClientNotification` groups — this is the wire half of the method-name criterion):
- Params shapes: `thread/resume` carries `excludeTurns:true`; `turn/start` carries `model`/`effort`; `userAgent` with a platform suffix parses to the bare version.
- Default decline for all ten `ServerRequest` methods (table-driven) — the five approval results above, errors for the rest.
- Pending call on exit: peer never answers, then closes; the call returns an error wrapping `ErrExited` instead of hanging.

Static: `TestMethodNamesInSchema` — `clientRequests`/`clientNotifications` ⊆ schema groups; `serverRequests` and `serverNotifications` equal the schema groups exactly (so a pinned-version bump that adds a method fails loudly).

No live Codex call.

## Open questions

- Does real 0.156.1 exit on stdin EOF? The fake does; `Stop` falls back to SIGTERM on ctx expiry either way.
- Real `userAgent` format: assumed `<originator>/<version> (<platform>) …`; the parser tolerates a missing suffix.

## Documentation handoff

None required by the ticket. Pending for the documentation stage: a `docs/knowledge/features/codexsup-package.md` overview (new package).

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] The app-server's stdout is untrusted input. It crosses at `acp.Transport.handleLine` (framing, 16 MiB line cap); `Client` decodes only the fields it needs (`userAgent`, `thread.id`, `turn.id`) into typed structs. Notification and server-request params pass to callers raw as `json.RawMessage` — documented on `Config.OnNotification` and `ServerRequest.Params` as untrusted. No finding.
- [Approvals / posture] The central property: no default path answers a server request with an acceptance. Enforced by `declineFor` returning only decline/denied/empty-grant/error, tested for all ten `ServerRequest` methods. An unknown future server request cannot reach the default table (unregistered → acp method-not-found). `cancel`/`abort` deliberately not used. No finding. **Revised in rework 1 (see Revisions):** the original review did not check the literal shapes against the schema, and the `applyPatchApproval`/`execCommandApproval` default `{"decision":"denied"}` was schema-invalid (`ReviewDecision` has no plain `denied` string; its deny arm is the object `DeniedReviewDecision`). Corrected to `{"decision":{"denied":{"rejection":"…"}}}`; every default result is now validated against its method's `*Response` definition by `TestDefaultDeclinesMatchSchema`, so the property rests on a deterministic check rather than a hand-written literal.
- [Tokens, secrets] The package handles none. `CODEX_HOME` points at Codex's own auth store; the path is passed, never read. No finding.
- [File operations] None — the package opens no files. `Dir` and `CodexHome` are handed to the child untouched.
- [Subprocess] `exec.CommandContext(binary, "app-server")` with fixed argv, no shell; `Binary` is caller configuration, not remote input. OUT OF SCOPE — environment scrubbing (the child inherits the daemon's environment, including any credentials in it) and the approval/sandbox posture sent on `thread/start`: both belong to the runner that decides posture, #2585. OUT OF SCOPE — reaping descendant process groups on stop (the claude runners' `reapDescendantGroupsFn`) is #2585's respawn/teardown work; here `WaitDelay` bounds a descendant holding stdout so `Done` still closes.
- [Cryptographic primitives] None used.
- [Network & I/O] Stdio only. Line size capped by acp (`maxLineBytes`); a broken stream kills the child rather than leaving it writing into a full pipe (SHOULD FIX made concrete in the design: `kill` also closes `pr` so the copier unblocks).
- [Errors / logs] Params are never logged (prompt text, commands, file contents); logs carry method names only, matching acp's discipline. Codex stderr is discarded unless the caller supplies `Stderr`.
- [Concurrency] Every goroutine has an exit: the lifecycle goroutine at EOF (guaranteed by `Wait` → `pw.Close`, bounded by `WaitDelay`); the wait goroutine at process exit. Pending calls cannot hang past exit (`AfterFunc` on `exitCtx`). One lock (thread id), leaf. Double answers are prevented by `acp.Responder`.
- [Threat model] No relay or network exposure; the package is not wired into the daemon.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24

## Revisions

### 2026-09-24 — rework 1 (verifier MUST FIX on PR #2606)

- **Legacy approval decline shape.** The ticket and the Design table wrote the `applyPatchApproval`/`execCommandApproval` default as `{"decision":"denied"}`. At 0.156.1 both responses take a `ReviewDecision`, whose string arms are `approved`, `approved_for_session`, `approved_mcp_policy_amendment`, `timed_out` and `abort`; a denial is the object arm `DeniedReviewDecision` (`additionalProperties: false`, `rejection` required). New contract: `declineFor` answers these two with `{"decision":{"denied":{"rejection":"declined by pyrycode: no approval was given"}}}`. This departs from the ticket's literal, which contradicts its own "as the schema defines it". `abort` stays out: it also stops the turn.
- **Schema-validated defaults.** `serverrequest_test.go` adds `TestDefaultDeclinesMatchSchema`: for each method with a default result it finds the response definition from the `ServerRequest` arm's params ref (`XParams` → `XResponse`) and validates the wire value with a small draft-07-subset validator (`$ref`, `allOf`/`anyOf`/`oneOf`, `enum`, `type`, `properties`, `required`, `additionalProperties:false`, `items`). `TestSchemaValidatorRejectsBareDenied` is the negative control proving the validator rejects the old shape. `TestDefaultDeclines` pins the corrected bytes.
- **Doc note on `call`** (verifier NIT): a response racing the exit may be reported as `ErrExited`, since `acp.Transport.Call`'s select can pick the cancelled ctx; recorded for #2585's respawn. The double-wrapped `ErrExited` in a forced stop's `Err()` (NIT) is left as is — cosmetic, `errors.Is` holds.
