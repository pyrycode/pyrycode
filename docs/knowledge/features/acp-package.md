# `internal/acp` transport + `pyry acp` subcommand (#755, #756, #757, #761, #762, #747, #750, #753, #752, #749)

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
onto the session's supervisor for the future abort keystroke). #747 adds the two
**handshake** methods every connection opens with —
[`initialize`](#initialize--authenticate-handshake-747) (negotiate protocol
version, declare pyry's minimal capabilities) and `authenticate` (a no-op stub).
#750 adds the **outbound streaming half** — [`Transport.Notify`](#outbound-notification-primitive-transportnotify-750)
(the write-only sibling of `Call`) and the stateless
[`acpTurnStream`](#outbound-streaming-adapter-acpturnstream-750) sink that turns a
session's neutral turn-event stream into `session/update` notifications. #753
finishes the inbound **control** surface —
[`session/cancel`](#sessioncancel-actuation--modeconfig-pin-753) now *actuates*
the interrupt (`SendEsc`) onto #762's resolution seam, and
[`session/set_mode` / `session/set_config_option`](#sessioncancel-actuation--modeconfig-pin-753)
get defined answers under a **single-mode pin** (ADR 027 Open Item #1, resolved).

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
routing (resume-by-id and the cancel→supervisor resolution seam); #753 dropped the
**abort keystroke** onto that seam and added the mode/config pin (below). #749 adds
the **main call** — [`session/prompt`](#sessionprompt--the-held-turn-call-749)
delivers the host's content blocks into claude as a user turn and **holds the
in-flight call open** for the whole turn via #765's deferral primitive. Still
deferred to later epic-#600 tickets: **resolving** the held `session/prompt` call
with its `stopReason` on `TurnEnd` (T7 #751), and the outbound
`session/request_permission` return path that issues through `Call`.

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

## Outbound-notification primitive (`Transport.Notify`) (#750)

`session/update` is a JSON-RPC **notification** — a method + params with **no
`id`** and no response — so `Call`'s request/pending machinery does not fit. #750
adds `Notify`, `Call`'s write-only sibling, in a **new file** `internal/acp/notify.go`
(zero edits to `acp.go`, keeping the package conflict-free with the in-flight
#765 branch — the same "new outbound concern → new file" precedent as `Call`'s
future `responder.go`).

```go
func (t *Transport) Notify(method string, params any) error
```

- **Marshal `params` first**: nil → the `params` key is omitted; a marshal failure
  returns a wrapped error and **writes nothing** (no partial frame). Then one
  `writeMessage` under `writeMu` — the same leaf lock serialising `reply` and
  `Call`. Adding a third outbound writer is safe by the existing discipline
  (`writeMu` is never held across a handler call or a read).
- **A distinct unexported `notification` wire struct** (`{jsonrpc, method,
  params omitempty}`) is required because `request.ID` is a **non-omitempty
  `uint64`** and would always serialise an `id` — which a notification must never
  carry. `request` cannot be reused.
- Never awaits a response; safe from any goroutine. **Zero new exported types**
  (`Notify` is a method; `notification` is unexported).

The sole caller is the streaming adapter below; the unit tests
(`notify_test.go`) are the contract — a generic-map decode asserts `id` is
**absent** (not `0`), nil-params omission, and no partial frame on marshal
failure.

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

## `initialize` + `authenticate` handshake (#747)

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

## Outbound streaming adapter (`acpTurnStream`) (#750)

The **streaming consumer** (epic #600 T6) that turns a session's neutral
[`turnevent.Event`](turnevent-package.md) stream into `session/update`
notifications. It lives in `cmd/pyry/acp_turn_stream.go` (**package `main`**,
keeping `internal/acp` method-agnostic) and is the ACP analogue of the mobile
head's `interactiveTurnEmitterV2` — but **far thinner**: ACP's host owns the
spinner and turn pacing, so none of the mobile emitter's lifecycle machine,
delta coalescing, capability fan-out, replay ring, or conversation cursor apply.
The mapping half is [#769](acpbridge-package.md)'s pure `acpbridge.MapUpdate`;
this ticket owns the subscription sink, the transport emit, `TurnEnd`→T7
signalling, and `Stall`→stderr.

`acpTurnStream` is a **stateless sink** — no per-event state, no goroutine, no
clock read. Its `Handle(ev turnevent.Event)` signature **is**
`turnbridge.Config.OnEvent` exactly, so the turn owner
([#751](https://github.com/pyrycode/pyrycode/issues/751)/T7) wires
`OnEvent: stream.Handle` with no closure. `turnbridge` invokes `Handle` serially
on its single `Run` goroutine, so no synchronisation is needed.

### `Handle` dispatch (ADR 027 divergences 1 & 3)

`TurnEnd` and `Stall` are type-switched **explicitly, before** `MapUpdate`,
because `MapUpdate` collapses both to `ok == false` and the adapter must
distinguish them (a signal vs a stderr drop):

| Event | Action |
|---|---|
| `TurnEnd{Reason}` | **No** `session/update` (divergence 1). Call `onTurnEnd(string(Reason))` — the ACP `stopReason` **is** `string(Reason)` by identity (`TurnEndReason` values already are the ACP strings). |
| `Stall{}` | **No** `session/update` (divergence 3). `logger.Warn` on stderr, content-free (`event`, `session_id` only). |
| `TextChunk` / `ThoughtChunk` / `ToolStart` / `ToolUpdate` | `acpbridge.MapUpdate(ev)` → `Transport.Notify(MethodSessionUpdate, sessionUpdateParams{sessionID, update})`. |
| nil / future variant | `MapUpdate` returns `ok == false` → defensive `Debug`-logged drop (unreachable for the four above). |

`sessionUpdateParams{SessionID, Update}` is the `{sessionId, update}` params
wrapper — **this consumer's**, not `acpbridge`'s (the mapper is pure
value-to-value; session addressing is the consumer's). The `sessionUpdate`
discriminant rides *inside* `Update`.

### `TurnEnd` → the T7 (#751) signal seam

`onTurnEnd func(reason string)` is invoked **synchronously on the producer's
single `Run` goroutine** when `TurnEnd` arrives. #751 supplies a callback that
resolves the held `session/prompt` call with `reason` as the `stopReason`; **that
callback must not block** the producer goroutine (buffered hand-off or immediate
resolve — e.g. #765's `Responder`). A callback (not a channel) keeps #750
agnostic about #751's hold-and-resolve mechanism. `onTurnEnd` is **nil-tolerant**
(Debug-log + no-op) so the adapter can be constructed for pure emit tests.

### Chunk grouping is arrival order, not coalescing

The `msgID` from `MapUpdate` is intentionally **discarded**. ACP's
`agent_message_chunk` carries content only — there is no per-message wire
delimiter — so chunks sharing a `MessageID` stream as **separate**
`agent_message_chunk` notifications *in arrival order* and the host concatenates
them. The adapter is stateless w.r.t. `MessageID` and does **not** coalesce
(contrast the mobile emitter's `MessageID`-keyed delta coalescing, #609, which
ACP neither needs nor supports).

### Producer wiring is #751's (the flagged finding)

`acpTurnStream` is a pure **sink**, unit-tested against a scripted
`turnevent.Event` sequence — no live producer or claude. Standing up a
`turnbridge` producer over a session's supervisor and feeding this sink belongs to
the `session/prompt` turn owner (#751/T7), because streaming happens *inside* the
held `session/prompt` call (divergence 1). **#761's embedded pool does NOT wire a
producer for ACP sessions** (`session/new` just calls `pool.Create`; the producer
is wired only for the mobile head, in `runSupervisor`) — recorded here so #751
need not re-discover it. Every building block #751 needs is shipped:
`sess.Supervisor()` satisfies `turnbridge.SessionHost`;
`turnbridge.NewTargetSubscriber` with a fixed-session by-id `TargetResolver`
(`Switch: nil` — ACP has no active-conversation follow);
`sessions.DefaultClaudeSessionsDir(bootstrapWorkdir)` as the JSONL dir.

**Not security-sensitive** — outbound-only over local stdio to the host process,
no untrusted inbound parsing, no auth/crypto (the inbound handlers #749/#752 carry
that label). Full per-ticket detail in [`codebase/750.md`](../codebase/750.md).

## `session/cancel` actuation + mode/config pin (#753)

The inbound **control** surface (epic #600 T9). Two independent small changes in
`cmd/pyry` (`acp.go`, `acp_handshake.go`): `session/cancel` actuates the interrupt
onto #762's resolution seam, and `session/set_mode` / `session/set_config_option`
get defined, advertised answers under a **single-mode pin** — resolving
[ADR 027](../decisions/027-acp-mapping.md) Open Item #1. No new files, no new
exported types. **Not security-sensitive** — inbound control over local stdio to a
co-located, user-launched host.

### `session/cancel` → actuate the interrupt

A consumer-declared one-method seam in package `main`, mirroring `relay.Interrupter`,
keeps the actuation unit-testable while resolution correctness stays in its own
(#762) test:

```go
type interrupter interface{ SendEsc() error } // *supervisor.Supervisor satisfies it (#726)
```

`cancelSessionHandler`'s signature changed from `(pool *sessions.Pool)` to an
**injected resolver** `(resolve func(json.RawMessage) (interrupter, error))`. On a
resolve error it returns the error (the notification path logs it once, no frame —
unchanged from #762); on success it actuates `_ = intr.SendEsc()` **best-effort**
and returns `(nil, nil)`. `resolveCancelTarget` is **unchanged** (still returns
`*supervisor.Supervisor`); the composition root wires the resolver and returns a
**true `nil` interface on error** (`resolveCancelTarget`'s typed
`*supervisor.Supervisor` would otherwise wrap a nil pointer in a non-nil
`interrupter`). `SendEsc` is the same lever the mobile `interrupt` / `modal_cancel`
frames route through (`relay.handleInterrupt`).

**A `SendEsc` failure is swallowed, not returned.** No live turn / mid-teardown
yields `ErrNoLiveSession`: there is nothing to roll back and a notification owes no
reply. Returning it would make [`dispatchNotification`](#classification-decision-table-the-core-mechanism)
log a *second* handler error on the known-id path whenever a session isn't
keystroke-ready — breaking the `count == 1` assertion of
`TestACP_SessionCancel_NotificationEmitsNoResponse` and adding non-determinism.
(Silent-swallow, vs `relay.handleInterrupt`'s `Warn`-then-tolerate — the handler
has no logger in scope; observability is deferred, evidence-based.) The interrupt
is **not** routed through the neutral `turnevent.Cancel`
([turnevent-package.md](turnevent-package.md), #707) — that type is declared
vocabulary consumers are told not to construct; the handler calls the concrete
`SendEsc()` lever directly.

### Mode/config — single-mode pin (ADR 027 Open Item #1)

No neutral-model mode source exists, and no tui-driver lever toggles claude's
plan/edit mode (the [#726](../codebase/726.md) keystroke seam is
`SendEsc`/`Answer`/`AcceptTrust` only), so a real `set_mode` would need new
tui-driver + supervisor surface — out of scope. **Decision: pin one mode.**

- **The pin is advertised in the session responses, not the handshake.** ACP
  `initialize`/`agentCapabilities` has no mode flag, so the ticket's original
  `acp_handshake.go` advertisement pointer is corrected: `newSessionResult` (shared
  by `session/new` + `session/load`) gains a `Modes sessionModeState` field,
  populated from `pinnedModeState()` at both sites — `currentModeId: "default"`
  with a single self-referential `availableModes` entry. One entry offers a
  compliant host no alternative to switch to (the faithful "not a switch that does
  nothing"). The handshake struct is **unchanged**.
- **`setModeHandler`** (stateless, no pool — the pin is process-global): decode
  `{sessionId, modeId}`; unmarshal failure → `CodeInvalidParams`; `modeId ==
  "default"` → empty-`{}` success (`setModeResult{}`); any other `modeId` →
  `CodeInvalidParams` ("unsupported session mode").
- **`setConfigOptionHandler`** (stateless): rejects **every** request with
  `CodeInvalidParams` ("no configurable options") — pyry exposes no config surface.
  Deliberate asymmetry with mode (a real, nameable mode but no config surface).
- Both are ACP **requests** carrying an id → `dispatchRequest` writes exactly one
  response (empty result on accept, or the `CodeInvalidParams` frame). Registered
  in `serveACPWithPool`'s existing `register` closure alongside the other
  `session/*` methods.

Because no mode/config ever changes, the sourceless outbound `current_mode_update`
/ `config_option_update` reflections are **never emitted** — their
synthesize-vs-omit call stays parked in
[ADR 027](../decisions/027-acp-mapping.md) Open Item #2 (outbound cluster,
#750/#769).

### Out of scope

The end-to-end **cancel → `stopReason: cancelled`** assertion is
[#751](https://github.com/pyrycode/pyrycode/issues/751)'s (T7): the held
`session/prompt` call is resolved with the mapped stop reason on `TurnEnd`, and
there is no held prompt to resolve until #749/#765 land. This ticket asserts only
that the interrupt reaches the supervisor. Full per-ticket detail in
[`codebase/753.md`](../codebase/753.md).

## `session/prompt` — the held turn call (#749)

The **main call** (epic #600). A `session/prompt` request carries a user turn as
an ordered list of content blocks; the handler resolves the target session from
the [#761](#sessionnew-and-the-embedded-pool-761) pool, maps the blocks to a
user-turn payload, delivers it into the supervised **interactive** claude via
`Session.WriteUserTurn` → `Supervisor.WriteUserTurn` → the tui-driver
`DeliverPrompt` path (the hard cost invariant — no `claude -p`, no SDK), and
**holds its own JSON-RPC call open** for the whole turn via #765's deferral
primitive. An ACP turn is one `session/prompt` request that streams `session/update`
notifications and then returns a `stopReason`, so deliver-in and hold-open are
**one handler** — a `session/prompt` that returned synchronously would violate the
turn model. One new file `cmd/pyry/acp_prompt.go` (package `main`) + a ~15-line
edit to `serveACPWithPool`, **0 new exported types**. **security-sensitive**
(host content reaches claude) — architect security pass **PASS**.

### The handler (read loop → deferral → delivery goroutine)

`promptHandler(holds, resolve, logger)` runs inline on the read loop and does only
cheap, race-free work before deferring:

1. `decodeSessionID(params)` — the shared #762 guard; empty/missing id →
   `CodeInvalidParams` **before** `Pool.Lookup` (the bootstrap trap: `Lookup("")`
   resolves the parked bootstrap, not an error).
2. `mapPromptContent(params)` — content blocks → payload bytes (below).
3. `resolve(id)` = `pool.Lookup(id)` — `ErrSessionNotFound` → `CodeInvalidParams
   "unknown session"` (mirrors `loadSessionHandler`); other error → `CodeInternalError`.
4. `acp.ResponderFrom(ctx)` — a `session/prompt` sent as a **notification** carries
   no responder → plain error, no hold, no spawn (a turn whose `stopReason` can
   never be returned must not start).
5. `holds.begin(sessionId, resp)` — the per-session in-flight guard. A **concurrent
   second prompt** for the same session while one is pending → `CodeInvalidRequest`
   ("a prompt is already in flight"), no hold registered, no goroutine spawned.
6. `go deliverPrompt(...)` then **`return acp.ErrDeferred`** — the transport writes
   nothing and the read loop scans the next frame immediately.

`WriteUserTurn` blocks for seconds (WaitReady + commit-confirm, ~10s), so it runs
on a spawned goroutine bounded by `promptDeliverTimeout = 15s` (deliberately >
the supervisor budget, so a genuine supervisor failure surfaces first; the timeout
is only the outer backstop). Running it inline would stall classification of the
very concurrent-second-prompt reject and `session/cancel` this handler must keep
live — **this is the core reason #765 exists.** On success the call stays held
(T7 resolves it); on a non-nil `WriteUserTurn` error, `deliverPrompt` logs a
sentinel (never the payload) and calls `holds.fail`, resolving the held call with a
generic `CodeInternalError "prompt delivery failed"` and freeing the slot.

### `promptHolds` — per-session in-flight registry

A `sync.Mutex`-guarded `map[string]*acp.Responder`, at most one held call per
session id, the **sole owner** of held-call resolution:

| Method | Role |
|---|---|
| `begin(id, resp) bool` | Atomic check-and-insert inline on the read loop; `false` if one is already in flight (caller rejects, registers nothing). |
| `end(id, stopReason)` | **T7 [#751](https://github.com/pyrycode/pyrycode/issues/751) seam** — resolve with `{"stopReason":…}` and free the slot. |
| `fail(id, *acp.Error)` | Delivery failed — resolve with an error frame and free the slot (next prompt for that session is accepted). |

`end`/`fail` **capture the responder under `mu`, delete, then release `mu` before**
calling `Reply`/`ReplyError` (those take the transport `writeMu`) — so `mu` is a
pure leaf and no `mu → writeMu` nesting exists; the store is never touched by the
transport write path, so no cycle is possible. A missing entry is a **no-op**
(idempotent), so `end` is safe even if `fail` already fired and vice-versa.
Exactly-one-frame across every resolve race is #765's `Responder.done` CAS;
`promptHolds` adds slot bookkeeping, not a second frame guard. The store is
constructed once per `pyry acp` process in `serveACPWithPool` and closed over by
the handler (same pattern as `newSessionHandler(pool)`).

### `mapPromptContent` — content blocks → payload (fail-closed, AC-3)

Decodes `{prompt:[{type,text}]}`, joins `text` with `"\n"` **between** blocks
(bracketed-paste-safe per tui-driver v1.3.0 — an embedded newline does not submit
the turn early), then passes the text through **verbatim** (no escaping, no
transformation). No length cap — the transport's 16 MiB per-line bound already caps
it. It **fails closed** with `CodeInvalidParams` on:

- a **non-text block** (`image`/`audio`/`resource`/…), naming the kind —
  `promptCapabilities` is all-false, so any non-text block is a host protocol
  violation; rejecting never drops host content silently and never reinterprets it;
- a **disallowed control byte** — see the security boundary below;
- an **empty** assembled payload / absent `prompt` → `"empty prompt"`;
- **malformed** params JSON → `"invalid params"`.

### Security boundary — content delivered strictly as a user turn

The security property: `prompt[].text` is delivered as a user turn into claude and
**never** interpreted as pyry control input, shell input, a path, or a terminal
control sequence. `mapPromptContent`'s `[]byte` has exactly one sink
(`WriteUserTurn` → `DeliverPrompt`, a bracketed paste into claude's PTY) — the bytes
never reach `os/exec`, the control-socket verb dispatch, or a filesystem path.
Shell injection is **structurally absent** (fixed argv set at spawn, no `sh -c`,
content never an argv), so `$(…)`, backticks, `;`, `../` are inert.

**The MUST-FIX vector was terminal-escape injection.** Because delivery frames the
payload as a bracketed paste, a raw `ESC` — e.g. an embedded paste terminator
`ESC[201~` — could break out of paste framing and reach claude's TUI as
keystrokes / control sequences. `hasDisallowedControl` rejects any C0
(`0x00–0x1f`) or DEL (`0x7f`) byte **except** `\t \n \r`. It scans **bytes, not
runes**, which is both correct and UTF-8-safe: valid multi-byte UTF-8 uses only
bytes ≥ 0x80, so a byte < 0x20 or == 0x7f is never a continuation byte → no false
positive on international text. A text-only host never legitimately sends a raw
control byte, so fail-closed rejection loses no valid content. This is the
deterministic code guard at the trust boundary (belt of different fabric — not a
second stochastic layer). No host content is logged (method / session-id /
sentinel only); `conversationID` passed to `WriteUserTurn` is the **resolved pool
id**, not a host string (and isn't consulted — the ACP pool sets
`ValidateConversation == nil`).

### The concurrent-second error code (a judgment call)

ACP defines no dedicated "session busy" code, so the second-prompt reject uses
`CodeInvalidRequest` (`-32600`, "invalid in current state") — the params were
valid, the *state* was not, so `CodeInvalidParams` would misdescribe it. The
binding constraint is *a well-formed ACP error that is not `CodeInvalidParams`*; if
a later ACP revision adds an idiomatic busy code, swapping it is a one-line change.

### Out of scope

Resolving the held call on real `TurnEnd` with the mapped `stopReason` is T7
[#751](https://github.com/pyrycode/pyrycode/issues/751), via `holds.end`. Until T7
lands, a delivered prompt holds until host disconnect; tests drive `end` through a
**placeholder-end** path. Producer wiring (a `turnbridge` producer + the #750
`acpTurnStream` sink for ACP sessions) is also #751's. Full per-ticket detail —
including the two test-harness lessons (resolve a held call off the reader
goroutine; build `ESC[201~` at runtime to satisfy `substrate-guard`) — in
[`codebase/749.md`](../codebase/749.md).

## ACP permission proxy (`session/request_permission`) (#752)

The **highest-risk seam in the epic** (T8, [ADR 027](../decisions/027-acp-mapping.md) divergence 2).
`acpPermissionProxy` (`cmd/pyry/acp_permission.go`, two new files, unwired) answers claude's on-screen
permission modal by asking the ACP host: on a permission-class modal it issues a blocking
`session/request_permission` `Call` to the host, then routes the host's choice back into claude's live
prompt as the correct keystroke — the interactive path, **no** `claude -p` / Agent SDK mechanism (the
hard cost invariant). **security-sensitive**; an architect security pass (spec § Security review pass,
verdict PASS) and code-review security goggles both cleared it. It is the ACP analogue of the mobile
`interactiveModalEmitterV2` but far thinner — one blocking `Call` in place of the broadcast + nonce
registry.

### Where permission surfaces — its own modal-event subscription

A permission modal is a **tui-driver PTY-state event** (`tuidriver.EventKindPtyModalShown` / `…Hidden`
with `ev.Modal == ModalClassPermission`), **not** a `turnevent.Event` (`turnbridge.mapEvent` drops
modal kinds). So the adapter takes its **own** modal-event subscription off the live session;
`Session.Events(...)` mints a fresh channel + merge goroutine per call, so it does not contend with the
T6/T7 turn-event subscription ([outbound streaming adapter](#outbound-streaming-adapter-acpturnstream-750)).

### Two goroutines, one atomic arbiter

`Handle(ctx, ev)` is the modal-event sink, called serially on the **drain goroutine** (the deferred
wiring's). A permission `ModalShown` → `handleShown`; any `ModalHidden` → `retireInflight`; everything
else (a non-permission `ModalShown` included) → no-op. It takes no `screenText` — `session/request_permission`
carries no prompt body and the option set is class-fixed, so the wiring never sources `ScreenSnapshot`.

- **`handleShown`** builds the four-option set via `modalbridge.PermissionRequestForClass(ev.Modal, "")`
  (option order + ACP `kind`s share one source of truth), stores a `permissionRoundTrip`, and **spawns
  one round-trip goroutine** — the "off the Serve read loop" guarantee (AC-5; a `Call` on the read loop
  deadlocks, since only it reads the response).
- **`runRoundTrip`** blocks in `Call`, then claims the one-shot (`rt.resolved.CompareAndSwap(false,true)`)
  and routes **at most one** keystroke; `defer rt.cancel()` releases the deadline timer.
- **`retireInflight`** (drain goroutine) claims + clears the round-trip; if it wins the CAS it cancels
  the in-flight `Call`, so `runRoundTrip` unblocks, sees the failed CAS, and routes nothing (AC-4).

`inflight` is drain-goroutine-confined (no lock); the only cross-goroutine state is the `atomic.Bool`
one-shot + `rt.cancel` (written before spawn). Retiring on **any** `ModalHidden` (vs the mobile
surfacer's class-match) is safe: an `inflight` round-trip only ever exists for a `ModalClassPermission`
modal, and tui-driver's single-modal invariant means no unrelated `Hidden` fires while it shows.

### Default-safe resolution (`route`) — deny is the failure mode, not a branch

The **only** path to an allow keystroke is a decoded `outcome:"selected"` whose `optionId` is a
**member of the daemon-built option set** → `Answer(digit)`, `digit = strconv.Itoa(idx+1)` (the index
is the single source of truth for wire option order and the keystroke digit — `classifyAnswer`'s
discipline over the neutral `[]turnevent.PermissionOption`). Every other outcome routes deny
(`SendEsc`): call error / ctx-deadline timeout / teardown-cancel, undecodable body, forged/unknown
`optionId`, `outcome:"cancelled"`, unknown/empty outcome. A forged id is simply not locatable in the
set, so it can never route an allow.

### Routing seam — keystrokes, not `PermissionResponse`

The host's selection routes through the supervisor **keystroke seam** (`modalKeystroker`:
`Answer`/`SendEsc`; `*supervisor.Supervisor` satisfies it, #726). There is **no** `turnevent.PermissionResponse`
consumer — the neutral type stays unwired and the adapter does not construct one. This refines
[ADR 027](../decisions/027-acp-mapping.md) divergence 2, whose prose sketched a `PermissionResponse`
feed-back; the built adapter routes the keystroke directly.

### Correlation — transport-owned, doubly guarded

`Transport.Call`'s pending map matches reply↔request by outbound id and drops a no-waiter reply, so a
stale/replayed/mismatched id never reaches the adapter (AC-4). The `atomic.Bool` one-shot adds the
modal-retirement guarantee on top — belt-and-suspenders, both deterministic. The `Call` takes its own
outbound id space and cannot resolve or touch the held inbound `session/prompt` (#749/#765), which
resolves only on `TurnEnd` (AC-5).

### Wire types + trust class

Consumer-owned `requestPermissionParams{sessionId, toolCall?, options[]}` mirroring
`acp_turn_stream.go`'s local params. `toolCall` is **omitted** (`omitempty`) — the modal-event path
yields no gating tool-call id; reserved for a future `ToolStart`-correlation ticket. The response
decodes the **nested** `{"outcome":{"outcome","optionId"}}` form; the decode is default-safe (shape
mismatch ⇒ `Outcome` zero ⇒ deny), so a wrong guess fails **closed**. The composition root pre-trusts
the workdir (`trustMark`), so a **trust** modal does not surface; the adapter gates on
`ModalClassPermission` only (`AcceptTrust` unused) — evidence-based, avoiding an untested trust→ACP
mapping for a modal that cannot appear. Full per-ticket detail in [`codebase/752.md`](../codebase/752.md).

## Deferred / scope boundaries

- **Live wiring of the permission proxy** ([#752](https://github.com/pyrycode/pyrycode/issues/752)) —
  the adapter ships **unwired**; subscribing it to `Session.Events()` and driving `Handle` per modal
  event belongs to the modal-event wiring follow-up (T7 #751 territory), exactly as #708 defers
  `interactiveModalEmitterV2`'s wiring.
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
  [`codebase/762.md`](../codebase/762.md) (`session/load` + `session/cancel` lifecycle),
  [`codebase/747.md`](../codebase/747.md) (`initialize` + `authenticate` handshake),
  [`codebase/750.md`](../codebase/750.md) (`Transport.Notify` + outbound streaming adapter),
  [`codebase/753.md`](../codebase/753.md) (`session/cancel` actuation + mode/config pin),
  [`codebase/752.md`](../codebase/752.md) (permission proxy via `session/request_permission`),
  [`codebase/749.md`](../codebase/749.md) (`session/prompt` — content blocks + held call)
- Specs: [`specs/architecture/755-acp-transport.md`](../../specs/architecture/755-acp-transport.md),
  [`specs/architecture/756-acp-subcommand.md`](../../specs/architecture/756-acp-subcommand.md),
  [`specs/architecture/757-acp-outbound-request.md`](../../specs/architecture/757-acp-outbound-request.md),
  [`specs/architecture/761-acp-session-new-embedded-pool.md`](../../specs/architecture/761-acp-session-new-embedded-pool.md),
  [`specs/architecture/762-acp-session-load-cancel.md`](../../specs/architecture/762-acp-session-load-cancel.md),
  [`specs/architecture/747-acp-initialize-handshake.md`](../../specs/architecture/747-acp-initialize-handshake.md),
  [`specs/architecture/750-acp-outbound-streaming-adapter.md`](../../specs/architecture/750-acp-outbound-streaming-adapter.md),
  [`specs/architecture/753-acp-cancel-mode-config.md`](../../specs/architecture/753-acp-cancel-mode-config.md),
  [`specs/architecture/752-acp-permission-proxy.md`](../../specs/architecture/752-acp-permission-proxy.md),
  [`specs/architecture/749-acp-session-prompt.md`](../../specs/architecture/749-acp-session-prompt.md)
- Outbound mapping layer (the pure `MapUpdate` the streaming adapter consumes):
  [`features/acpbridge-package.md`](acpbridge-package.md) (#769),
  [ADR 027](../decisions/027-acp-mapping.md) (§ Outbound table, divergences 1 & 3).
- Cancel abort keystroke (the seam `resolveCancelTarget` exposes) — **built** by
  T9 [#753](https://github.com/pyrycode/pyrycode/issues/753): `cancelSessionHandler`
  actuates `SendEsc` best-effort. The neutral `Cancel` command from
  [`turnevent-package.md`](turnevent-package.md) (#707) is declared vocabulary and
  deliberately **not** constructed — the handler calls the concrete lever directly.
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
