# Exported surface (3 types — under the 5-type sizing line)

\#757 added **one new exported method** (`Call`) but **zero new exported types**:
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
