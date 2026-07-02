# Spec #755 — `internal/acp`: JSON-RPC 2.0 line-delimited transport + method dispatch table

**Ticket:** #755 (split from #746, sub-issue of epic #600)
**Size:** S — 2 production files, 3 exported types, greenfield package, zero consumer fan-out.
**Security-sensitive:** No (not labelled; no auth/crypto/network/untrusted-remote surface — a local editor speaks to this over stdio).

## Files to read first

Read these before writing code. Each line says what to extract.

- `internal/control/logs.go:23-93` — `RingBuffer` + `SlogTee` stderr-diagnostics convention. **What to extract:** the injected-`*slog.Logger` pattern. #755 only needs to *accept* a logger and write diagnostics to it; the ring-buffer/tee wiring is the subcommand's concern (#756), not this ticket. Do NOT build a ring buffer here.
- `internal/control/server.go:436-491` — `handle`: single-connection `json.NewDecoder`/`json.NewEncoder` decode→switch→encode loop. **What to extract:** the "decode one frame, switch on its shape, encode exactly one reply" idiom and the verbatim-error-to-response discipline. `internal/acp` mirrors this shape but over an `io.Reader`/`io.Writer` (not a `net.Conn`) and with JSON-RPC framing instead of the control verb enum.
- `internal/agentrun/jsonl/reader.go:1-132` — line-buffer sizing (`maxLineBytes = 16<<20`, `initialBufCap = 8192`), the `ErrLineTooLarge` stance, and the **"MUST NOT log line contents"** caution in the package doc. **What to extract:** buffer-cap constants to reuse, and the discipline that the read loop logs *offsets and error kinds only, never the line bytes* — an ACP request's `params` may carry user prompt content.
- `internal/dispatch/dispatch.go` — the mobile-wire `Handler` + dispatch-table + `Register`/`Run` precedent (Register-before-Run enforced via `atomic.Bool`, duplicate-Register panics, carrier-agnostic: imports only its own wire types). **What to extract:** the registration + running-guard shape to mirror. **Do NOT import `internal/protocol` or any mobile envelope type** — `internal/acp` defines its own JSON-RPC wire types (Technical Notes).
- `internal/control/server.go:253-269` — `NewServer` nil-guard + default-logger construction pattern (`panic` on required-nil, `slog.Default()` on optional-nil logger). **What to extract:** the constructor validation idiom `New` should mirror.
- `cmd/substrate-guard/main.go:34-36` — the allowlist + scan scope. **What to extract:** confirmation that `internal/acp` is trivially green — it names no claude-TUI substrate literals (it drives no claude in this ticket), so AC 5's substrate-guard requirement needs no allowlist entry.
- `CODING-STYLE.md` §§ "Interface Design", "Error Handling", "Testing" — func-type handler idiom, `fmt.Errorf("%w")` wrapping, `errors.Is`, table-driven stdlib-only tests, `t.Parallel()`.

## Context

Epic #600 makes `pyry acp` speak the Agent Client Protocol (Zed-stewarded, JSON-RPC 2.0, line-delimited over stdio): the host writes one JSON object per line to the agent's stdin; the agent replies and streams notifications one JSON object per line to stdout; stderr carries human-readable diagnostics only, never protocol frames.

#755 is the **inbound transport floor** and nothing else: the package, the framing, the dispatch table, the diagnostics. It ships **no subcommand** (#756), **no outbound-request primitive** (#757), **no claude driving**, and **no real ACP method**. Later tickets register handlers against the dispatch table this ticket builds.

**Hard cost invariant (state, do not implement).** The package doc comment MUST record: when later tickets wire claude, `pyry acp` drives a real *interactive* `claude` session billed under the interactive subscription — never the non-interactive `claude -p` path or the metered Agent SDK. This ticket ships no claude driving and cannot violate the invariant; the doc comment makes the constraint visible at the transport layer.

## Design

### Package layout

```
internal/acp/
  acp.go        package doc (incl. hard cost invariant), Transport, New, Register, Serve, read loop, classify+dispatch, write helpers
  jsonrpc.go    wire types (decode-by-shape + response encode), Error type, NewError, error-code constants
  acp_test.go   table-driven framing/dispatch tests + diagnostics-isolation + no-panic
```

Two production files, one test file. `internal/acp` imports only stdlib (`bufio`, `bytes`, `context`, `encoding/json`, `io`, `log/slog`, `sync`) — no repo packages, no mobile-wire types.

### Exported surface (3 types — under the 5-type line)

```go
// Transport owns the line-delimited JSON-RPC 2.0 framing over one
// io.Reader (inbound) + io.Writer (outbound) and the method dispatch table.
type Transport struct { /* unexported */ }

// New constructs a Transport reading JSON-RPC frames from r and writing
// them to w. r and w are required (panics if nil — programmer error).
// log is optional (nil → slog.Default()); diagnostics go only to log,
// never to w.
func New(r io.Reader, w io.Writer, log *slog.Logger) *Transport

// Register binds a handler to a method name. Panics on a duplicate method
// or if called after Serve has started (programmer errors — mirrors
// internal/dispatch.Register). Must be called before Serve.
func (t *Transport) Register(method string, h Handler)

// Serve reads frames until the reader reaches EOF (returns nil) or ctx is
// cancelled between frames (returns ctx.Err()). A structurally broken
// stream (over-long line / read error) returns a wrapped error. Never
// panics on malformed input.
func (t *Transport) Serve(ctx context.Context) error

// Handler is the contract every later ACP-method ticket registers against.
// It receives the raw params (may be nil) and returns a result to marshal
// into the JSON-RPC "result", or an error. A notification handler returns
// (nil, nil) — "nothing". A returned *Error controls the wire error code;
// any other error maps to Internal error (-32603) with the detail logged
// to stderr, never leaked verbatim to the client.
type Handler func(ctx context.Context, params json.RawMessage) (result any, err error)

// Error is a JSON-RPC error a handler may return to control the wire code.
type Error struct {
    Code    int
    Message string
    Data    any // optional; omitted from the wire when nil
}
func (e *Error) Error() string
func NewError(code int, message string) *Error
```

Exported error-code constants (in `jsonrpc.go`): `CodeParseError = -32700`, `CodeInvalidRequest = -32600`, `CodeMethodNotFound = -32601`, `CodeInvalidParams = -32602`, `CodeInternalError = -32603`. Handlers and tests reference these; the transport itself produces `-32700/-32600/-32601/-32603`.

### Wire types (unexported, in `jsonrpc.go`)

**One decode-by-shape struct** (Technical Notes: prefer one message type over separate request/notification/response structs — holds the surface minimal):

```go
type rpcMessage struct {
    Jsonrpc string          `json:"jsonrpc"`
    Method  *string         `json:"method"`  // nil ⇒ absent
    Params  json.RawMessage `json:"params"`
    ID      json.RawMessage `json:"id"`      // nil ⇒ absent; present even if literal null
    Result  json.RawMessage `json:"result"`  // response-frame detection only
    Error   json.RawMessage `json:"error"`   // response-frame detection only
}
```

Presence is detected by nil-ness: an absent JSON key leaves `*string`/`json.RawMessage` at nil; a present key (even `null`) is non-nil. This is the whole classifier's input.

Two small encode structs for replies (success carries `result`, error carries `error` — never both, per JSON-RPC 2.0). `id` is echoed as `json.RawMessage` verbatim; a nil `json.RawMessage` marshals to `null` (used when the id is unknown — parse/invalid/batch errors). An unexported `rpcError{Code int; Message string; Data any (omitempty)}` is the nested error object.

### Classification decision table (the core mechanism)

Per line, after trimming leading/trailing whitespace; an **empty line is skipped** (no output, no dispatch):

| Condition (checked in order) | Outcome | Response (`id`) |
|---|---|---|
| `!json.Valid(line)` | parse error | `-32700`, id `null` |
| first non-ws byte is `[` | batch — unsupported | `-32600`, id `null` |
| first non-ws byte is not `{` (non-object JSON value) | invalid request | `-32600`, id `null` |
| `json.Unmarshal` into `rpcMessage` fails (valid object, wrong field types) | invalid request | `-32600`, id `null` |
| `method` present **and** `id` present | **request** → dispatch | one response, echoed id (see handler mapping) |
| `method` present, `id` absent | **notification** → dispatch | none written |
| `method` absent, `id` present, (`result` or `error` present) | **response frame** → tolerated | none written (dropped, logged debug) — #757 seam |
| anything else (object but none of the above) | invalid request | `-32600`, id `null` |

Handler mapping for a **request**:
- registered handler returns `(result, nil)` → success response with `result` (a nil result marshals to `null`).
- returns `(nil, *Error)` → error response with that code/message/data.
- returns `(nil, plainErr)` → `-32603` Internal error, generic message; the real error is logged to stderr (do not leak it on the wire).
- **no** handler registered → `-32601` method not found, echoed id.

Handler mapping for a **notification**: dispatch if registered; **never write a response** regardless of return. A non-nil error (or an unregistered notification) is logged to stderr at debug/warn; nothing reaches the writer.

**Batch decision — record in the package doc comment:** a top-level JSON array is rejected with a single Invalid Request (`-32600`, id `null`); batching is not supported. ACP does not use JSON-RPC batching, so this deviation from the spec's per-element batch handling is inert in practice and keeps the transport a single-frame-per-line reader.

The developer implements this table as a `handleLine([]byte)` method. ~7 branches — under the 10-branch red line, and every branch is a facet of the one classify-and-reply mechanism (not independent features).

### Read loop & framing

`Serve` uses a `bufio.Scanner` over the reader with a raised buffer: `scanner.Buffer(make([]byte, 0, initialBufCap), maxLineBytes)` reusing the `internal/agentrun/jsonl` sizing (`initialBufCap = 8192`, `maxLineBytes = 16<<20`). Rationale over `json.Decoder`: line-delimited framing is the protocol contract, and a scanner gives one `[]byte` line per iteration, which the classifier consumes directly. Between lines, check `ctx.Err()` and stop if cancelled. On `scanner.Err()` (includes `bufio.ErrTooLong` for a pathological over-long line), log the error kind to stderr and return it wrapped — the stream is structurally broken. EOF → `Serve` returns nil.

> A blocked `Read` cannot be unblocked by ctx alone (Go limitation). #755 is tested against in-memory pipes where EOF terminates naturally; the subcommand (#756) unblocks a real blocked stdin read by closing the reader on shutdown. Documented as an Open question, not solved here.

### Data flow

```
r (host stdin)                                         w (host stdout)
   │                                                        ▲
   │ one JSON object per line                               │ one JSON object per line
   ▼                                                        │
bufio.Scanner ──► handleLine(line) ──► classify ──┬─ request ──► dispatch ─► writeMessage(response)
                                                  ├─ notification ─► dispatch ─► (no write)
                                                  ├─ response frame ─► drop + log
                                                  └─ parse/invalid/batch ─► writeMessage(error, id=null)

log (stderr) ◄── diagnostics only (offsets, error kinds — never line bytes, never params content)
```

## Concurrency model

- **One goroutine.** `Serve` runs the read loop and dispatches each handler **serially, inline** on that goroutine. Simplest correct model for a single host connection; no handler-scheduling machinery. A slow handler blocks the loop — acceptable for the transport floor (no real handlers yet); revisit only if a future method needs it.
- **Writer serialization.** All writes go through one unexported `writeMessage(v any) error` guarded by a leaf `sync.Mutex` (`writeMu`), using a `*json.Encoder` bound to `w` (one compact object + `\n` per `Encode`; set `SetEscapeHTML(false)` so protocol content isn't HTML-escaped). The mutex is redundant under today's single-goroutine dispatch, but it is the seam #757's outbound-request writer shares — adding it now means #757 adds an agent→client caller without reworking the write path. Leaf lock: never held across a handler call or a read.
- **Register/Serve ordering.** An `atomic.Bool` "started" flag: `Serve` sets it; `Register` panics if it is set (register-after-Serve) or on a duplicate method. Mirrors `internal/dispatch`. The handler map is therefore write-once-before-Serve, read-only during Serve — no map mutex needed.

## Error handling

- **Malformed input never panics** (AC 3). Every non-JSON / wrong-shape / batch line produces a JSON-RPC error frame (or a silent drop for response frames); a `recover` is *not* used — the classifier is total by construction (`json.Valid` gate + byte-peek + typed unmarshal all return errors, never panic). A no-panic test drives adversarial inputs to prove it.
- **Unknown id on error frames.** Parse/invalid/batch errors have no recoverable id → `id: null` per JSON-RPC 2.0.
- **Handler errors.** `*Error` → verbatim code/message/data. Plain error → `-32603` with a generic message; detail logged to stderr. Never leak internal error text to the writer.
- **Diagnostics isolation (AC 4).** The transport writes protocol frames only to `w` and diagnostics only to `log`. It logs *error kinds and offsets, never line bytes or `params` content* (host params may carry user prompt material — mirror `internal/agentrun/jsonl`'s no-content-logging rule).
- **Stream break.** Over-long line / read error → wrapped error out of `Serve` (`fmt.Errorf("acp: serve: %w", err)`); the loop stops.

## Testing strategy

Same-package `acp_test.go`, stdlib `testing` only, `t.Parallel()`, table-driven where the shape allows. Drive the transport against in-memory pipes: an `io.Reader` built from a scripted input string (`strings.NewReader` or `io.Pipe`) and a `bytes.Buffer` as `w`; a separate `bytes.Buffer`-backed `slog` handler (or the `internal/control` `discardWriter` idiom) as the diagnostics sink.

Scenarios (bullet-pointed; the developer writes them in the project idiom):

- **Request → one response, matching id.** Register a handler that echoes params; feed `{"jsonrpc":"2.0","method":"m","params":{...},"id":1}`; assert exactly one line on `w`, valid JSON-RPC, `id == 1`, `result` present.
- **Notification → no response.** Feed the same frame without `id`; assert the handler ran and `w` is empty.
- **Unknown method → -32601.** Feed a request for an unregistered method; assert one error frame, `error.code == -32601`, echoed id.
- **Parse error → -32700.** Feed a non-JSON line (`not json`); assert `-32700`, `id: null`.
- **Invalid request → -32600** for: a valid-JSON non-object (`42`, `"x"`, `true`); and a valid object that is neither request/notification/response (e.g. `{"jsonrpc":"2.0"}` — no method, no id). Assert `-32600`, `id: null`.
- **Response frame tolerated.** Feed `{"jsonrpc":"2.0","id":7,"result":{}}` (and a variant with `error` instead of `result`); assert **nothing** is written to `w`.
- **Batch rejected.** Feed a top-level array `[{...}]`; assert a single `-32600` frame, `id: null`.
- **Handler `*Error` vs plain error.** One handler returns `NewError(-32602, "bad params")` → assert `-32602` on the wire; another returns a plain `errors.New("boom")` → assert `-32603` and that `"boom"` appears in the diagnostics sink but **not** on `w`.
- **Diagnostics isolation.** Run a scripted mix of the above through one exchange; assert every line on `w` unmarshals to a frame with `"jsonrpc":"2.0"`, and that the diagnostics sink is the only place error detail appears.
- **No panic.** Feed adversarial lines (empty line, whitespace-only, truncated JSON, deeply-nested JSON, an object with wrong field types like `{"method":5}`) and assert `Serve` returns without panicking.
- **EOF terminates `Serve` with nil; blank/whitespace lines are skipped** (no output).
- **Register guards.** Duplicate method → panic; `Register` after `Serve` started → panic (mirror `internal/dispatch` gate tests).

`make check` (vet, race, staticcheck, substrate-guard) must be green (AC 5). Substrate-guard is trivially green — no claude-TUI literals.

## Open questions

- **ctx cancellation vs a blocked `Read`.** #755 checks `ctx.Err()` between frames only; a `Read` blocked on a quiet stdin is unblocked by closing the reader, which is the subcommand's (#756) responsibility. If a later ticket needs mid-read cancellation inside the transport, it adds a closer seam then — out of scope here.
- **Serial vs concurrent dispatch.** Serial-inline is chosen for simplicity. If a future ACP method is long-running and must not block inbound framing, dispatch can move to a worker without touching the wire contract (the `writeMu` seam already serializes concurrent writers). Deferred until an observed need.
- **`data` field on outbound errors.** The transport-generated errors (`-32700/-32600/-32601`) omit `data`; only handler-returned `*Error` values may carry it. No requirement drives a richer transport-side `data` today.
