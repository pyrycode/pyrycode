# `internal/acp` transport + `pyry acp` subcommand (#755, #756, #757, #761, #762)

The **bidirectional transport** for the Agent Client Protocol (ACP,
Zed-stewarded): line-delimited JSON-RPC 2.0 over one `io.Reader` (inbound) plus
one `io.Writer` (outbound), a method dispatch table for host→agent requests, and
an **outbound-request primitive** for agent→client calls. Part of epic
[#600](https://github.com/pyrycode/pyrycode/issues/600) — the `pyry acp`
subcommand that lets an ACP host (e.g. Zed) drive a supervised claude session.
As of #761 the subcommand hosts an **embedded `internal/sessions` pool** and
serves its first real method, [`session/new`](#sessionnew-and-the-embedded-pool-761),
which spawns exactly one supervised interactive claude per ACP session. #762 adds
the two **lifecycle** verbs that address a session the host already opened —
[`session/load`](#sessionload--sessioncancel-762) (resume by id) and
[`session/cancel`](#sessionload--sessioncancel-762) (a notification that resolves
onto the session's supervisor for the future abort keystroke).

ACP is bidirectional. Inbound: the host writes one JSON object per line to the
agent's stdin; the agent replies and streams notifications one JSON object per
line to stdout. Outbound: the agent issues its own request to the client —
`session/request_permission`, the held `session/prompt` return path — and blocks
for the id-correlated response ([`Transport.Call`](#outbound-request-primitive-transportcall-757)).
stderr carries human-readable diagnostics only, **never protocol frames**.

#755 built the inbound floor: the package, the framing, the dispatch table, the
diagnostics. #756 added the [`pyry acp` subcommand](#subcommand-pyry-acp-756)
that serves this transport over real stdio. #757 added the
[outbound direction](#outbound-request-primitive-transportcall-757): id
generation, a pending-call registry, the blocking `Call`, and routing of inbound
*response* lines to their waiters — replacing #755's response-frame log-and-drop.
#761 delivered the **first real ACP method** and the composition root it needs:
[`session/new`](#sessionnew-and-the-embedded-pool-761) over an embedded
`internal/sessions` pool — the subcommand now *drives claude*. #762 added the
[`session/load` + `session/cancel`](#sessionload--sessioncancel-762) lifecycle
routing (resume-by-id and the cancel→supervisor resolution seam). Still deferred
to later epic-#600 tickets: the cancel **abort keystroke** itself (T9, #753), turn
delivery, and the outbound `session/request_permission` / held-`session/prompt`
return paths that issue through `Call`.

Greenfield, stdlib-only package: imports `bufio`, `context`, `encoding/json`,
`errors`, `fmt`, `io`, `log/slog`, `sync`, `sync/atomic` — **no repo packages**.
Deliberately **not** coupled to the mobile-wire envelope types in
`internal/protocol`: ACP requires the JSON-RPC 2.0 shape
(`{"jsonrpc":"2.0","method":…,"params":…,"id":…}`), which is not the mobile
custom envelope, so `internal/acp` defines its own wire types.

## Hard cost invariant (stated, not enforced here)

The package doc comment records it so the constraint stays visible at the
transport layer: **when a later ticket wires claude behind an ACP method,
`pyry acp` MUST drive a real *interactive* `claude` session billed under the
interactive subscription — never the non-interactive `claude -p` path and never
the metered Agent SDK.** #755's transport ships no claude driving and cannot
violate it. **#761 is the first ticket that honours it in code:**
[`session/new`](#sessionnew-and-the-embedded-pool-761)'s `Pool.Create` spawns
`claude --session-id <uuid>` through the tui-driver (no `-p`/`--print`, no SDK),
and a test asserts the spawn argv is exactly the interactive path.

## Exported surface (3 types — under the 5-type sizing line)

#757 added **one new exported method** (`Call`) but **zero new exported types**:
it reuses the existing `*Error` for the mapped error response, so the surface
stays at three types.

```go
package acp

// Handler is the contract every later ACP-method ticket registers against.
// Returns a result to marshal into "result", or an error. A notification
// handler returns (nil, nil) — "nothing". A returned *Error controls the wire
// error code; any other error maps to CodeInternalError (-32603) with a generic
// message, the detail logged to stderr and never leaked to the client.
type Handler func(ctx context.Context, params json.RawMessage) (result any, err error)

// Transport owns the framing, the inbound dispatch table, and the outbound
// pending-call registry.
type Transport struct { /* unexported */ }

func New(r io.Reader, w io.Writer, log *slog.Logger) *Transport
func (t *Transport) Register(method string, h Handler) // before Serve only
func (t *Transport) Serve(ctx context.Context) error

// Call issues an agent→client JSON-RPC request with a freshly-generated id and
// blocks until the matching response is read, returning the raw result or the
// mapped *Error (#757). ctx cancellation returns ctx.Err() and reclaims the
// pending slot. MUST be called from a goroutine other than the one running
// Serve. See "Outbound-request primitive" below.
func (t *Transport) Call(ctx context.Context, method string, params any) (json.RawMessage, error)

// Error is a JSON-RPC error a handler may return to control the wire code, and
// the type Call returns for a mapped error response.
type Error struct { Code int; Message string; Data any } // Data omitempty
func (e *Error) Error() string
func NewError(code int, message string) *Error

// JSON-RPC 2.0 error codes.
const (
    CodeParseError     = -32700
    CodeInvalidRequest = -32600
    CodeMethodNotFound = -32601
    CodeInvalidParams  = -32602 // for handlers that validate params
    CodeInternalError  = -32603
)
```

- `New` — `r` and `w` are **required** (panics on nil — programmer error); `log`
  is optional (nil → `slog.Default()`). Diagnostics go only to `log`, never to
  `w`. The encoder is bound to `w` with `SetEscapeHTML(false)` so protocol
  content (`<`, `>`, `&`) is not HTML-escaped on the wire.
- `Register` — binds a handler by method name. Panics on a **duplicate method**
  or if called **after `Serve` has started** (both programmer errors — mirrors
  [`internal/dispatch.Register`](dispatch-package.md#register-before-run-is-enforced-not-advisory)).
- `Serve` — reads frames until EOF (returns `nil`) or ctx cancellation **between
  frames** (returns `ctx.Err()`). A structurally broken stream (over-long line /
  read error) returns a wrapped error. **Never panics on malformed input.**

The transport itself produces `-32700/-32600/-32601/-32603`; `-32602` is exported
for handlers that validate their own params.

## Classification decision table (the core mechanism)

`handleLine([]byte)` classifies each line, after trimming JSON's four
insignificant whitespace bytes (space/tab/CR/LF). An **empty / whitespace-only
line is skipped** — no output, no dispatch.

| Condition (checked in order) | Outcome | Response `id` |
|---|---|---|
| `!json.Valid(line)` | parse error | `-32700`, `null` |
| first non-ws byte is `[` | batch — **unsupported** | `-32600`, `null` |
| first non-ws byte is not `{` (non-object JSON value: `42`, `"x"`, `true`) | invalid request | `-32600`, `null` |
| `json.Unmarshal` into `rpcMessage` fails (valid object, wrong field types, e.g. `{"method":5}`) | invalid request | `-32600`, `null` |
| `method` present **and** `id` present | **request** → dispatch | one response, echoed id |
| `method` present, `id` absent | **notification** → dispatch | none written |
| `method` absent, `id` present, (`result` or `error` present) | **response frame** → **routed** to the `Call` awaiting this id (#757); unknown / reclaimed id → logged debug + dropped | none written |
| object, none of the above | invalid request | `-32600`, `null` |

**Handler mapping for a request:**
- `(result, nil)` → success response with `result` (a nil result marshals to
  `null`).
- `(nil, *Error)` → error response with that code/message/data.
- `(nil, plainErr)` → `-32603` internal error, generic message; the real error
  is logged to stderr, **never leaked on the wire**.
- **no** handler registered → `-32601` method not found, echoed id.

**Handler mapping for a notification:** dispatch if registered; **never write a
response** regardless of return. A non-nil error (or an unregistered
notification) is logged at debug/warn; nothing reaches the writer.

### Absent-vs-present-`null` is the whole classifier's input

The single decode-by-shape `rpcMessage` detects field presence by **nil-ness**:
an absent JSON key leaves the `*string` / `json.RawMessage` at nil; a present
key — **even a literal `null`** — is non-nil. This is exactly what separates a
null-id *request* (`{"jsonrpc":"2.0","method":"m","id":null}` — dispatched,
response id `null`) from a *notification* (no `id` key — no response). A one-type
decoder (over separate request/notification/response structs) holds the exported
surface to three types.

### The response-frame case (filled by #757)

A well-formed JSON-RPC **response** frame (an `id` plus a `result`/`error`, no
`method`) is **routed to its waiter** — `handleLine` calls `t.routeResponse(&msg)`
(see [Outbound-request primitive](#outbound-request-primitive-transportcall-757)).
Either way it produces **no** writer output. #755 originally left this as a
tolerated log-and-drop (a documented scope boundary), pinned by a test so #757
changed the behaviour against a known baseline; #757 replaced the drop with
response routing. An **unknown or already-reclaimed id** keeps the log-and-drop
behaviour, now *inside* `routeResponse`.

### Batch rejection (recorded in the package doc comment)

A top-level JSON array (a JSON-RPC batch) is rejected with a **single** Invalid
Request (`-32600`, id `null`); batching is **not supported**. ACP does not use
JSON-RPC batching, so this deviation from the spec's per-element batch handling
is inert in practice and keeps the transport a strict one-frame-per-line reader.

## Concurrency model

- **The read loop is one goroutine.** `Serve` runs the read loop and dispatches
  each handler **serially, inline** on that goroutine, and (since #757) runs
  `routeResponse` to deliver an inbound response. Simplest correct model for a
  single host connection — no handler-scheduling machinery. A slow handler blocks
  the loop; acceptable for the transport floor (no real handlers yet), revisited
  only on an observed need.
- **Outbound `Call` runs on a *separate* caller goroutine** (#757) — it must, or
  it would block the read loop that reads the response it awaits. This makes the
  transport's first cross-goroutine state necessary: the pending-call registry.
  See [Outbound-request primitive](#outbound-request-primitive-transportcall-757).
- **Writer serialization.** All writes go through one unexported
  `writeMessage(v any) error` guarded by a leaf `sync.Mutex` (`writeMu`), using a
  `*json.Encoder` bound to `w` (one compact object + `\n` per `Encode`). The
  mutex was redundant under #755's single-goroutine dispatch but is **load-bearing
  since #757**: a `Call` and an inbound reply can now race on `w`. Both already
  funnel through `writeMessage`, so no change was needed — this is exactly the
  seam #755 pre-built. Leaf lock: never held across a handler call, a read, or
  `pendingMu`.
- **Register/Serve ordering.** An `atomic.Bool` `started` flag: `Serve` sets it;
  `Register` panics if it is set. The handler map is therefore
  **write-once-before-Serve, read-only during Serve** — no map mutex needed.
  Mirrors [`internal/dispatch`](dispatch-package.md#register-before-run-is-enforced-not-advisory).

## Read loop & framing

`Serve` uses a `bufio.Scanner` over `r` with a raised buffer —
`scanner.Buffer(make([]byte, 0, initialBufCap), maxLineBytes)` — reusing
`internal/agentrun/jsonl` sizing (`initialBufCap = 8192`,
`maxLineBytes = 16 << 20`). A scanner (over `json.Decoder`) is chosen because
line-delimited framing **is** the protocol contract: one `[]byte` line per
iteration, consumed directly by the classifier. Between lines `Serve` checks
`ctx.Err()` and stops if cancelled. On `scanner.Err()` (includes
`bufio.ErrTooLong` for a pathological over-long line) it logs the error **kind**
only and returns it wrapped (`fmt.Errorf("acp: serve: %w", err)`) — the stream is
structurally broken. EOF → `Serve` returns nil.

`scanner.Bytes()` is only valid until the next `Scan`. Retention is safe because
`json.Unmarshal` **copies** `json.RawMessage` fields out of the scanner buffer
(so a handler's `params` never aliases the reused buffer); handlers also run
inline before the next `Scan` as a secondary guard.

> **ctx vs a blocked `Read`.** A `Read` already blocked on a quiet reader cannot
> be unblocked by ctx alone (Go limitation) — the ctx check happens between
> frames. In-memory callers terminate naturally at EOF; the subcommand
> ([#756](https://github.com/pyrycode/pyrycode/issues/756)) unblocks a real
> blocked stdin by **closing the reader** on shutdown. It does **not** close
> `os.Stdin` directly (a `Read` parked in-kernel on the non-pollable stdin fd
> can't be woken by `Close` — lesson #78); it bridges real stdin through an
> in-memory `io.Pipe` and calls `pr.CloseWithError` on cancel. See the
> [`pyry acp` subcommand](#subcommand-pyry-acp-756) section.

## Diagnostics discipline

The transport writes **protocol frames only to `w`** and **diagnostics only to
`log`**. It logs *method names, error kinds, and structural facts — never the
line bytes or `params` content*: an ACP request's `params` may carry user prompt
material (mirrors `internal/agentrun/jsonl`'s no-content-logging rule). Handler
errors surfaced as `-32603` log the real detail to stderr and put only a generic
message on the wire.

## Outbound-request primitive (`Transport.Call`) (#757)

ACP is bidirectional: besides answering host requests, the agent issues its own
request to the client. `Call` is a textbook JSON-RPC **pending-call registry** —
the transport's first cross-goroutine state.

### The registry

Three fields on `Transport`, all touched only under the discipline below:

```go
nextID    atomic.Uint64              // Add(1) ⇒ 1,2,3,… never 0. Lock-free id gen.
pendingMu sync.Mutex                 // guards pending. Leaf lock.
pending   map[uint64]chan callResult // id ⇒ waiter's cap-1 channel
```

`pending` is **written by the caller's goroutine** (in `Call`) and
**read/deleted by the read-loop goroutine** (in `routeResponse`) — the
synchronisation `pendingMu` provides is the real cost of #757, and is why AC-2
demands `-race`. `nextID` is atomic so id generation needs no lock. **No lock
nesting:** `pendingMu` and `writeMu` are never held simultaneously.

**Id namespace.** Outbound ids come from `nextID`. Inbound requests are answered
by echoing the host's id verbatim, so the transport generates no inbound ids and
an outbound id cannot collide with one.

### `Call(ctx, method, params) (json.RawMessage, error)`

Ordering is load-bearing (documented at the call site):

1. **Marshal `params` first**, before touching shared state — a marshal failure
   (`fmt.Errorf("acp: marshal params: %w", …)`) returns immediately, so no id is
   burned into a dangling slot. `nil` params → the field is omitted from the
   wire; a `json.RawMessage` round-trips verbatim.
2. `id := nextID.Add(1)`.
3. **Register the waiter before writing** — `ch := make(chan callResult, 1)`;
   under `pendingMu`, `pending[id] = ch`. Registering before the write closes the
   race where a fast response arrives before the waiter exists.
4. `writeMessage(request{...})` — reuses the `writeMu` seam **unchanged**. On a
   write error: reclaim the slot, return the wrapped error.
5. `select` on `ctx.Done()` (→ reclaim slot, return `ctx.Err()`) and the waiter
   channel (→ return `r.result` or `r.err`).

**`Call` MUST be issued from a goroutine other than the one running `Serve`.**
Inbound handlers dispatch inline on the read loop, so a `Call` that blocked that
loop would deadlock — only the read loop reads the response `Call` awaits. This
is documented in `Call`'s doc comment; enforcing it (the handler re-entrancy
question) is a T8 consumer concern, explicitly out of scope.

### `routeResponse` — delivery on the read loop (must never block)

Replaces #755's response-frame log-and-drop. On the read-loop goroutine:

1. Decode the id; non-numeric / out-of-range → **unknown id: log Debug + drop**.
2. Under `pendingMu`: look up `ch`, `delete` if present.
3. `!ok` → **unknown or already-reclaimed id: log Debug + drop** — the *normal*
   fate of a cancelled call's late response (Debug, not Warn, to avoid log spam).
4. Build `callResult`: an error frame → `&Error{Code,Message,Data}` (a malformed
   error object synthesises `&Error{CodeInternalError,"malformed error response"}`
   — deterministic, never blocks); a success frame → `result = msg.Result` (may
   be JSON null).
5. `ch <- r` — the **cap-1 buffer guarantees this send returns immediately**.

### Why the read loop never stalls (AC-3)

- **Cap-1 waiter channel.** Exactly one response exists per id, so the buffer
  always has room; `routeResponse`'s send returns immediately whether the waiter
  is still blocked, has been reclaimed (cancellation), or the id was unknown
  (which never reaches the send). The read loop is never stalled by an absent or
  slow waiter — this is the "no goroutine or pending-slot leak" guarantee.
- **Idempotent reclaim resolves every cancel-vs-deliver ordering.** Both the
  cancellation-`delete` and the delivery-`lookup+delete` run under `pendingMu`.
  If the read loop delivers first, the caller's cancel-path `delete` is a no-op
  and the buffered value is GC'd unread; if the caller reclaims first, the read
  loop's lookup misses → unknown-id drop. No double-delivery, no leak, either
  order.
- **No defensive copy needed across goroutines.** `json.RawMessage.UnmarshalJSON`
  copies its input into a fresh backing array, so `msg.Result` is already
  independent of the reused `bufio.Scanner` buffer and is safe to hand to the
  caller goroutine (`-race` confirms).

### Shutdown

`Serve`-exit does **not** drain pending waiters. If `Serve` returns while a
`Call` is blocked and its `ctx` never fires, the call blocks until the ctx does —
consumers (T7/T8) always pass a session-lifetime context, so there is no observed
need to fail-fast pending calls on `Serve` return. If a later ticket needs it,
the read loop can range over `pending` and deliver a sentinel error on exit —
additive, no wire change.

Full per-ticket detail: [`codebase/757.md`](../codebase/757.md).

## Subcommand: `pyry acp` (#756)

The composition root that serves this transport over real stdio ships in
[#756](https://github.com/pyrycode/pyrycode/issues/756) — `cmd/pyry/acp.go`. It
registers the `pyry acp` verb (no flags, no positional args), constructs
`acp.New(os.Stdin, os.Stdout, logger)` and calls `Serve(ctx)`, and exits **zero**
on both EOF (host closes stdin) and SIGINT/SIGTERM. **As shipped in #756** it
registered **no** handlers and drove **no** claude — a bare transport floor.
[#761](#sessionnew-and-the-embedded-pool-761) extended it: `runACP` now stands up
an embedded pool and registers `session/new`, and `serveACP` gained a
`register func(*acp.Transport)` seam (nil ⇒ register nothing, preserving #756's
behaviour). The `io.Pipe` stdin bridge / closer / clean-exit machinery below is
unchanged by #761.

- **Plain stderr logger, no ring buffer.** `slog.NewTextHandler(os.Stderr, …)` —
  deliberately **not** `runSupervisor`'s `control.SlogTee` + `NewRingBuffer`. The
  ACP host captures this subprocess's stderr and keeps its own diagnostics ring;
  there is no `pyry logs` consumer for `pyry acp`, so the tee/ring is dead weight.
- **The blocked-read closer (AC#4).** The ticket's suggested `os.Stdin.Close()` is
  a trap — a `Read` parked in-kernel on the non-pollable stdin fd can't be woken
  by `Close` (lesson #78). The subcommand instead **bridges real stdin through an
  in-memory `io.Pipe`**: a closer goroutine (`<-ctx.Done()` →
  `pr.CloseWithError`) synchronously unblocks the blocked `PipeReader.Read`, and a
  bridge goroutine (`io.Copy(pw, os.Stdin)`) surfaces host EOF as a pipe close.
  `Serve` reads the in-memory pipe, never `os.Stdin` directly.
- **Clean-exit guard is `ctx.Err() != nil`**, not `errors.Is(err, context.Canceled)`
  — the forced-pipe-close path surfaces a wrapped `os.ErrClosed`, so guarding on
  "did we deliberately cancel?" absorbs both shutdown error shapes as exit-0 while
  still surfacing a genuine stream break (only reachable with `ctx.Err() == nil`).

Full detail, concurrency model, and the one-shot bridge-goroutine-leak rationale
in [`codebase/756.md`](../codebase/756.md).

## `session/new` and the embedded pool (#761)

The first ACP method that does real work, plus the composition root it needs: an
**embedded `internal/sessions` pool** inside the `pyry acp` subprocess, trimmed
to the ACP subset — **no relay, control socket, conversations registry, or
message queue**. ACP maps one session onto exactly one running interactive
`claude` (**divergence 6**); `session/new` allocates one pool session, starts
one supervised interactive claude, and returns a fresh id the host addresses in
later calls — **no more, no fewer** claudes.

### Standup (`runACP` → `serveACPWithPool`)

`runACP` trims `runSupervisor`'s spine to the ACP subset before serving:
`confineWorkdirToHome("")` (process cwd, confined to `$HOME`) → the shared
cmd-layer `trustMark` (marks the workdir trusted in `~/.claude.json` so claude
never wedges on the workspace-trust modal) → `sessions.New` with **two
load-bearing settings**:

- **`BootstrapEvicted: true`** — the pool always eager-spawns a bootstrap claude
  (`sessions.New` forces the bootstrap `stateActive`; `pool.Run` supervises it
  immediately). A naive standup that then `Pool.Create`s per `session/new` would
  yield **two** claudes for one ACP session — a divergence-6 and hard-cost
  violation. `BootstrapEvicted` parks the bootstrap in `runEvicted` (PID 0, no
  claude), so `Pool.Create` is the sole spawn. See
  [`sessions-package.md`](sessions-package.md#configbootstrapevicted--poolready-761)
  and [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md).
- **`Bootstrap.Bridge: supervisor.NewBridge(logger)`** — **service mode is
  mandatory.** Foreground `supervisor.runOnce` copies claude's PTY output to
  `os.Stdout`, which ACP owns for the JSON-RPC frame stream. Service mode routes
  each Created session's output to a per-session Bridge (discarded with no
  attacher/observer), keeping stdout clean for frames. The Bridge is the
  supervisor's I/O mediator — **not** one of the prohibited subsystems.

`serveACPWithPool` backgrounds `pool.Run` on a buffered(1) `poolErr` channel and
`select`s on `pool.Ready()` before serving — the **readiness gate** that closes
the unrecoverable first-`Create → ErrPoolNotRunning` startup race (a stranded,
never-supervised session with a non-empty id and no recovery; see
[`sessions-package.md`](sessions-package.md#configbootstrapevicted--poolready-761)).
On EOF/signal it cancels and joins `<-poolErr` so no supervisor goroutine
outlives the call.

### The handler

`newSessionHandler(pool)` satisfies `acp.Handler`. It calls `pool.Create(ctx,
"")` (empty label → a fresh UUID), and returns `newSessionResult{SessionID:
string(id)}` marshalling to `{"sessionId": "<uuid>"}`. `Pool.Create` spawns
exactly one `claude --session-id <uuid>` through the tui-driver — the
**interactive path, no `-p`/`--print`, no Agent SDK** (the hard cost invariant).
A `Create` error is wrapped (`fmt.Errorf("session/new: %w", …)`) → the mapped
`CodeInternalError` on the wire, detail logged not leaked.

`newSessionResult` is an **unexported** type with one exported json-tagged field
— zero new exported types.

### `cwd` is deliberately ignored (why NOT security-sensitive)

ACP `session/new` params carry `cwd`/`mcpServers`; the handler **reads neither**.
The spawn reuses the daemon's own workdir + the shared `trustMark` seam, so no
caller-supplied path reaches it — a local, same-user, trusted stdio host like the
control socket's session verbs. **Flip condition:** if a later ticket honours a
caller-supplied `cwd` that reaches the spawn *bypassing* the trust seam, the
cwd-confinement surface returns and the work becomes `security-sensitive`
(deferred to #762+).

Full per-ticket detail in [`codebase/761.md`](../codebase/761.md).

## `session/load` + `session/cancel` (#762)

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
`pool.Activate(ctx, id)` → `newSessionResult{SessionID: string(id)}` (reuses
#761's result struct → `{"sessionId":"<id>"}`).

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

`cancelSessionHandler` calls it and returns `(nil, nil)` on success. **Delivering
the abort keystroke is T9's (#753) job** — this ticket lands only the resolution
seam that reaches the per-session `*supervisor.Supervisor`.

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

## Deferred / scope boundaries

- **Serve-exit pending-call drain** ([#757](https://github.com/pyrycode/pyrycode/issues/757)) —
  `Serve`-exit does not fail-fast blocked `Call`s; the caller's `ctx` liberates
  them. Additive if a later ticket needs it (range `pending`, deliver a
  sentinel). See [Outbound-request primitive § Shutdown](#shutdown).
- **Handler re-entrancy** — whether an inbound handler may itself issue a blocking
  `Call` (it runs inline on the read loop → deadlock) is a T8 consumer concern;
  `Call` documents the "call from a different goroutine" constraint but does not
  enforce it.
- **Concurrent dispatch** — serial-inline chosen for simplicity. Moving dispatch
  to a worker (if a future long-running ACP method must not block inbound
  framing) needs no wire-contract change — the `writeMu` seam already serializes
  concurrent writers. Deferred until observed need.
- **Richer `data` on transport-generated errors** — the `-32700/-32600/-32601`
  frames omit `data`; only handler-returned `*Error` values may carry it. No
  requirement drives a richer transport-side `data` today.

## Test surface (`internal/acp/acp_test.go`)

Same-package, stdlib `testing` only, `t.Parallel()`, `-race`-clean. **Inbound**
tests use the single-shot `run`/`runErr` harness (`strings.NewReader` fed whole,
`Serve` runs to EOF, output parsed). **Outbound** tests (#757) use a separate
**live-Serve harness** (`liveTransport`) — see below.

- `TestTransport_Request_SingleResponseMatchingID` / `_NullIDIsARequest` —
  request → exactly one framed response, echoed id; the null-id-is-a-request edge.
- `TestTransport_Notification_NoResponse` — notification runs, `w` empty.
- `TestTransport_UnknownMethod_MethodNotFound` — `-32601`, echoed id.
- `TestTransport_ParseError` — non-JSON → `-32700`, id `null`.
- `TestTransport_InvalidRequest` — non-object JSON and a valid non-frame object
  → `-32600`, id `null`.
- `TestTransport_ResponseFrameTolerated` — response-shaped line → nothing written.
- `TestTransport_BatchRejected` — top-level array → single `-32600`, id `null`.
- `TestTransport_HandlerError_RPCvsPlain` / `_ErrorData` — `*Error` → verbatim
  code on the wire; plain error → `-32603` with detail in the diagnostics sink,
  **not** on `w`; handler `*Error` `Data` reaches the wire.
- `TestTransport_DiagnosticsIsolation` — every line on `w` is a `"jsonrpc":"2.0"`
  frame; error detail appears only in the diagnostics sink.
- `TestTransport_NoPanic` — adversarial inputs (empty/whitespace-only, truncated
  JSON, deeply-nested, wrong field types) → `Serve` returns without panicking.
- `TestTransport_BlankLinesSkipped_EOFReturnsNil` / `_NoTrailingNewline` /
  `_OverlongLine_ReturnsWrappedError` / `_ContextCancelledBetweenFrames` —
  framing edges.
- `TestTransport_RegisterGuards` / `TestNew_NilArgs` — programmer-error posture.

**Outbound `Call` tests (#757)** run against `liveTransport`: `Serve` on its own
goroutine over paired `io.Pipe`s; the test reads request frames off the writer to
learn the generated id, then feeds a matching response on the reader. `Call` is
issued from a spawned goroutine and its outcome collected over a channel, so
`t.Fatalf` only ever runs on the test goroutine. The diagnostics sink is a
mutex-guarded `syncBuffer` (a bare `bytes.Buffer` would `-race` against the Serve
goroutine — the `docs/lessons.md` gotcha).

- `TestTransport_Call_ResultPath` — request has a numeric id + method + params;
  the matching-id `result` response resolves `Call` to that result, nil error.
- `TestTransport_Call_ErrorPath` — nil params is omitted from the wire; a
  matching-id `error` response maps to an `*Error` with the code/message/`Data`
  (`errors.As`).
- `TestTransport_Call_ConcurrentDistinctIDs` — 32 concurrent `Call`s get distinct
  ids and each resolves to its *own* response (each echoes its own params). Under
  `-race` — the AC-2 race-safety proof.
- `TestTransport_Call_UnknownIDDropped` — a response with no outstanding call is
  dropped + logged ("no waiter" in the diag sink); a subsequent real call still
  resolves (Serve stayed live, registry uncorrupted).
- `TestTransport_Call_ContextCancelledReclaims` — cancel before feeding a
  response → `context.Canceled`; a late response for the reclaimed id is dropped;
  a fresh call with a new id still resolves (no waiter lingers).
- `TestTransport_Call_MalformedErrorObject` — an `error` that is not an object →
  a synthesized `*Error{CodeInternalError}`, `Call` does not hang.
- `TestTransport_Call_MarshalParamsError` — an unmarshallable param (a channel) →
  `Call` returns a marshal error before any id is burned or frame written.

`make check` (vet, race, staticcheck, substrate-guard) is green. Substrate-guard
is trivially green — `internal/acp` names no claude-TUI substrate literals (it
drives no claude), so it needs no allowlist entry in `cmd/substrate-guard`.

## Related

- Per-ticket records: [`codebase/755.md`](../codebase/755.md) (transport floor),
  [`codebase/756.md`](../codebase/756.md) (`pyry acp` subcommand),
  [`codebase/757.md`](../codebase/757.md) (outbound-request primitive),
  [`codebase/761.md`](../codebase/761.md) (`session/new` + embedded pool),
  [`codebase/762.md`](../codebase/762.md) (`session/load` + `session/cancel` lifecycle)
- Specs: [`specs/architecture/755-acp-transport.md`](../../specs/architecture/755-acp-transport.md),
  [`specs/architecture/756-acp-subcommand.md`](../../specs/architecture/756-acp-subcommand.md),
  [`specs/architecture/757-acp-outbound-request.md`](../../specs/architecture/757-acp-outbound-request.md),
  [`specs/architecture/761-acp-session-new-embedded-pool.md`](../../specs/architecture/761-acp-session-new-embedded-pool.md),
  [`specs/architecture/762-acp-session-load-cancel.md`](../../specs/architecture/762-acp-session-load-cancel.md)
- Cancel abort keystroke (the seam `resolveCancelTarget` exposes): T9
  [#753](https://github.com/pyrycode/pyrycode/issues/753), consuming the neutral
  `Cancel` command from [`turnevent-package.md`](turnevent-package.md) (#707).
- Embedded-pool design: [ADR 026](../decisions/026-embedded-acp-pool-exact-one-claude.md)
  (`BootstrapEvicted` over adopting `Default()`; the `Ready()` correctness gate),
  [`features/sessions-package.md`](sessions-package.md#configbootstrapevicted--poolready-761)
- Dispatch-table precedent: [`features/dispatch-package.md`](dispatch-package.md)
  (`Register`-before-`Run` `atomic.Bool` gate, carrier-agnostic handler table).
  `internal/acp` mirrors the shape but defines its own JSON-RPC wire types —
  it does **not** import `internal/protocol`.
- Stderr-diagnostics convention: `internal/control` `SlogTee` / `NewRingBuffer`
  (used by `runSupervisor`). The `pyry acp` subcommand (#756) deliberately does
  **not** reuse it — the ACP host captures this subprocess's stderr and keeps its
  own ring, so a plain `slog.NewTextHandler(os.Stderr, …)` suffices.
- No-content-logging discipline: [`features/jsonl-reader.md`](jsonl-reader.md)
  (`internal/agentrun/jsonl` — offsets and error kinds only, never line bytes).
- Epic: [#600](https://github.com/pyrycode/pyrycode/issues/600) `pyry acp`. The
  neutral daemon-owned turn model the future ACP method adapter maps onto —
  [`features/turnevent-package.md`](turnevent-package.md).
