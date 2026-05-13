# Fake-Phone Harness

`internal/e2e/internal/fakephone` is an in-process WebSocket **client**
that speaks the phone half of the mobile↔relay protocol
(`docs/protocol-mobile.md` § Authentication, § Message envelope). It
exists so daemon-side e2e tests can script the appendix flow as if a
phone were connected, without depending on a real device or platform
stack.

Phase: #296 ships the package in isolation (no consumers wired). Sibling
of #295 (fake-relay server); together they're consumed by the
still-to-come daemon-side roundtrip test.

## Surface

```go
package fakephone

func Dial(ctx context.Context, baseURL, serverID, token, deviceName string) (*Client, error)
func (*Client) Send(env protocol.Envelope) error
func (*Client) Receive(timeout time.Duration) (protocol.Envelope, error)
func (*Client) LastCloseStatus() (websocket.StatusCode, bool) // #308
func (*Client) Close() error

var (
    ErrReceiveTimeout = errors.New("fakephone: receive timeout")
    ErrClosed         = errors.New("fakephone: client closed")
)
```

- `baseURL` is the bare `ws://host:port` form (e.g. `fakerelay.Server.URL()`); `Dial` appends `/v1/client`.
- `serverID` / `token` / `deviceName` are set verbatim into the upgrade
  request headers (`x-pyrycode-server` / `x-pyrycode-token` /
  `x-pyrycode-device-name`). No emptiness validation — the harness
  forwards what it's given so consumer tests can probe the relay's
  rejection paths.
- Both sentinels are `errors.Is`-matchable.

## Method contracts

### `Dial`

1. Builds `http.Header` with the three required keys.
2. Calls `websocket.Dial(ctx, baseURL+"/v1/client", &websocket.DialOptions{HTTPHeader: hdr})`.
3. On error returns `fmt.Errorf("fakephone dial: %w", err)`; the `*http.Response` is discarded.
4. `conn.SetReadLimit(1<<20)` — matches `internal/transport` and `fakerelay`.

### `Send(env)`

1. Pre-checks `closed` under `mu`; returns `ErrClosed` if set.
2. `json.Marshal(env)` → write as a single `MessageText` frame via
   `conn.Write(context.Background(), ...)`.
3. On write error, re-checks `closed` under `mu`: if Close raced in,
   returns `ErrClosed`; otherwise wraps the error.

### `Receive(timeout)`

1. Pre-checks `closed` under `mu`; returns zero + `ErrClosed` if set.
2. Derives a `context.WithTimeout` and calls `conn.Read(ctx)`.
3. On read error: capture peer close status via `websocket.CloseStatus(err)`
   when not `-1` into `lastCloseStatus` under `mu` (#308); then re-checks
   `closed` (→ `ErrClosed`); else checks both
   `errors.Is(err, context.DeadlineExceeded)` AND
   `ctx.Err() == context.DeadlineExceeded` (the library may surface
   either) → `ErrReceiveTimeout`; else wraps.
4. On success, `json.Unmarshal` into `protocol.Envelope`.

### `LastCloseStatus` (#308)

Returns the WS close status code captured by the most recent
`Receive` whose read failed with a `CloseError`; `ok=false` when no
peer-side close has been observed (still open, or closed locally via
`Close`). Used by the `TestRelay_AuthReject_4401` e2e to assert the
auth-reject `4401` close code.

### `Close`

Sets `closed=true` under `mu`, then force-closes the conn with
`StatusNormalClosure`. Idempotent. Always returns `nil`. The underlying
`conn.Close` error is intentionally discarded — by the time Close runs,
the only consumer of the return value is "did we panic?"

## Concurrency

```
test goroutine                fakephone.Client
──────────────                ────────────────
Dial(...)         ─────►     websocket.Dial → *websocket.Conn
Send(env)         ─────►     conn.Write(text frame)
Receive(timeout)  ─────►     conn.Read(ctx-with-deadline)
                                   │ (blocked)
Close() ──────────►          conn.Close → Read unblocks with err
                                   │
                             Receive's recheck sees closed=true → ErrClosed
```

Two rules:

- One goroutine calls Send, one (possibly the same) calls Receive.
  `coder/websocket` permits one concurrent reader plus one concurrent
  writer; the harness inherits that.
- Close is safe from any goroutine, including concurrently with an
  in-flight Send/Receive.

The only synchronisation primitive is `mu` protecting the `closed` bool.
No pumps, no channels, no per-conn ctx tree.

## Race-recheck pattern

Each of Send/Receive opens and closes `mu` twice: once before I/O
(`closed` pre-check) and once after on the I/O-error path (`closed`
re-check). This is what maps a "Close racing with in-flight I/O" into
the `ErrClosed` sentinel instead of a wrapped low-level WS error.
`Close` writes `closed = true` BEFORE force-closing the conn, so the
post-I/O recheck sees the flag set.

## Library trade-off: timed-out Receive

`coder/websocket` closes the underlying conn when the Read context is
cancelled. A `Receive` that hits its deadline therefore makes the
`Client` unusable — subsequent calls will see a broken conn. Consumer
tests that anticipate timeouts should construct a fresh Client per
attempt. Documented on `Receive`'s godoc.

The AC ("on deadline exceeded returns a typed error the caller can
match against") is fully covered; only the spec's optional "conn
remains usable after timeout" sub-assertion was dropped.

## What's NOT modeled (deliberate)

- **Routing-envelope wrap/unwrap.** The phone speaks raw
  `protocol.Envelope` JSON; wrapping is the relay's job (handled in
  `fakerelay` / `protocol.RoutingEnvelope`).
- **`hello` / `hello_ack` handshake** — envelope sequencing is the
  consumer test's responsibility.
- **Reconnect, ping pump, backfill, push-token registration** —
  application-layer concerns owned by `internal/transport` (heartbeat)
  and the future mobile-client code.
- **TLS termination.** Plain `ws://` only.
- **Config struct, dial-timeout knobs, logger.** Add when a consumer
  ticket needs them.

## Layout

```
internal/e2e/internal/fakephone/
  fakephone.go        ~150 LOC, package fakephone, no build tag
  fakephone_test.go   ~290 LOC, no build tag (stdlib testing)
```

No build tag: the double-`internal/` placement visibility-fences the
package to e2e callers.

## Related

- Per-ticket implementation notes: [`codebase/296.md`](../codebase/296.md).
- Wire-spec source-of-truth: `docs/protocol-mobile.md` § Authentication,
  § Message envelope, § Message types.
- Sibling harness (server side):
  [`fakerelay-harness.md`](fakerelay-harness.md) (#295).
- Library + lifecycle precedent:
  [`transport-package.md`](transport-package.md) (#247 — same
  `coder/websocket` dep).
- Wire types consumed: [`protocol-package.md`](protocol-package.md)
  (#255 / #271–#275 — `Envelope` and per-type payload structs).
- Harness-package precedent:
  [`fakeclaude-binary.md`](fakeclaude-binary.md) (#122 — same
  `internal/e2e/internal/` placement convention).
- Consumer roadmap: roundtrip e2e test (third split from #254) consumes
  this + `fakerelay` together; not wired yet.
