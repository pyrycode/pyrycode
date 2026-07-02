# `internal/acp` transport + `pyry acp` subcommand (#755, #756)

The **inbound transport floor** for the Agent Client Protocol (ACP,
Zed-stewarded): line-delimited JSON-RPC 2.0 over one `io.Reader` (inbound) plus
one `io.Writer` (outbound), and a method dispatch table. Part of epic
[#600](https://github.com/pyrycode/pyrycode/issues/600) — the `pyry acp`
subcommand that lets an ACP host (e.g. Zed) drive a supervised claude session.

The host writes one JSON object per line to the agent's stdin; the agent replies
and streams notifications one JSON object per line to stdout; stderr carries
human-readable diagnostics only, **never protocol frames**.

#755 built the floor and nothing above it: the package, the framing, the dispatch
table, the diagnostics. #756 added the [`pyry acp` subcommand](#subcommand-pyry-acp-756)
that serves this transport over real stdio. Still deferred to later epic-#600
tickets: the **outbound-request primitive** ([#757](https://github.com/pyrycode/pyrycode/issues/757)),
**claude driving**, and every **real ACP method** — later tickets register
handlers against the dispatch table this package builds.

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
the metered Agent SDK.** This ticket ships no claude driving and cannot violate
the invariant.

## Exported surface (3 types — under the 5-type sizing line)

```go
package acp

// Handler is the contract every later ACP-method ticket registers against.
// Returns a result to marshal into "result", or an error. A notification
// handler returns (nil, nil) — "nothing". A returned *Error controls the wire
// error code; any other error maps to CodeInternalError (-32603) with a generic
// message, the detail logged to stderr and never leaked to the client.
type Handler func(ctx context.Context, params json.RawMessage) (result any, err error)

// Transport owns the framing + the method dispatch table.
type Transport struct { /* unexported */ }

func New(r io.Reader, w io.Writer, log *slog.Logger) *Transport
func (t *Transport) Register(method string, h Handler) // before Serve only
func (t *Transport) Serve(ctx context.Context) error

// Error is a JSON-RPC error a handler may return to control the wire code.
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
| `method` absent, `id` present, (`result` or `error` present) | **response frame** → **tolerated** | none — dropped, logged debug (#757 seam) |
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

### The `#757` response-frame tolerance seam

A well-formed JSON-RPC **response** frame (an `id` plus a `result`/`error`, no
`method`) is **tolerated** — dropped with no writer output, logged at debug —
rather than rejected as invalid request. This is the scope boundary with the
outbound sibling ([#757](https://github.com/pyrycode/pyrycode/issues/757)): when
#757 makes the transport bidirectional (adds the agent→client outbound-request
primitive), it replaces this drop with response routing. AC 3 pins the
observable: a response-shaped line produces **no** writer output.

### Batch rejection (recorded in the package doc comment)

A top-level JSON array (a JSON-RPC batch) is rejected with a **single** Invalid
Request (`-32600`, id `null`); batching is **not supported**. ACP does not use
JSON-RPC batching, so this deviation from the spec's per-element batch handling
is inert in practice and keeps the transport a strict one-frame-per-line reader.

## Concurrency model

- **One goroutine.** `Serve` runs the read loop and dispatches each handler
  **serially, inline** on that goroutine. Simplest correct model for a single
  host connection — no handler-scheduling machinery. A slow handler blocks the
  loop; acceptable for the transport floor (no real handlers yet), revisited only
  on an observed need.
- **Writer serialization.** All writes go through one unexported
  `writeMessage(v any) error` guarded by a leaf `sync.Mutex` (`writeMu`), using a
  `*json.Encoder` bound to `w` (one compact object + `\n` per `Encode`). The
  mutex is **redundant under today's single-goroutine dispatch** but is the seam
  #757's outbound-request writer shares — adding an agent→client caller then
  needs no rework of the write path. Leaf lock: never held across a handler call
  or a read.
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

## Subcommand: `pyry acp` (#756)

The composition root that serves this transport over real stdio ships in
[#756](https://github.com/pyrycode/pyrycode/issues/756) — `cmd/pyry/acp.go`. It
registers the `pyry acp` verb (no flags, no positional args), constructs
`acp.New(os.Stdin, os.Stdout, logger)` and calls `Serve(ctx)`, and exits **zero**
on both EOF (host closes stdin) and SIGINT/SIGTERM. It registers **no** handlers
and drives **no** claude — later epic-#600 tickets register `session/*` handlers
against it.

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

## Deferred / scope boundaries

- **Outbound-request primitive** — [#757](https://github.com/pyrycode/pyrycode/issues/757).
  Makes the transport bidirectional; replaces the response-frame drop with
  response routing; reuses the `writeMu` write seam.
- **Concurrent dispatch** — serial-inline chosen for simplicity. Moving dispatch
  to a worker (if a future long-running ACP method must not block inbound
  framing) needs no wire-contract change — the `writeMu` seam already serializes
  concurrent writers. Deferred until observed need.
- **Richer `data` on transport-generated errors** — the `-32700/-32600/-32601`
  frames omit `data`; only handler-returned `*Error` values may carry it. No
  requirement drives a richer transport-side `data` today.

## Test surface (`internal/acp/acp_test.go`)

Same-package, stdlib `testing` only, `t.Parallel()`, `-race`-clean. Driven
against in-memory pipes (`strings.NewReader` / `io.Pipe` for `r`, `bytes.Buffer`
for `w`, a separate `bytes.Buffer`-backed slog handler as the diagnostics sink).

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

`make check` (vet, race, staticcheck, substrate-guard) is green. Substrate-guard
is trivially green — `internal/acp` names no claude-TUI substrate literals (it
drives no claude), so it needs no allowlist entry in `cmd/substrate-guard`.

## Related

- Per-ticket record: [`codebase/755.md`](../codebase/755.md)
- Spec: [`specs/architecture/755-acp-transport.md`](../../specs/architecture/755-acp-transport.md)
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
