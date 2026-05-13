# 296 — `net/e2e`: fakephone harness package

## Files to read first

- `docs/protocol-mobile.md:85-122` — § Phone → relay → binary. The exact header set the `/v1/client` upgrade must carry (`x-pyrycode-server`, `x-pyrycode-token`, `x-pyrycode-device-name`); the relay performs no token validation; phones never see the routing envelope wrapper.
- `docs/protocol-mobile.md:177-203` — § Message envelope. The wire shape (`id`, `type`, `ts`, `payload`, `in_reply_to`, `payload_encrypted`) and the "one envelope per WS text frame, UTF-8" framing rule. The harness round-trips this shape via the structs in `internal/protocol`.
- `internal/protocol/envelope.go` (all 95 lines) — the `Envelope` struct this package marshals/unmarshals. Note that `Payload` is `json.RawMessage` (deferred decode), so the harness needs no awareness of per-type payload structs. Also note `RoutingEnvelope` exists in this package but is **out of scope here** — the phone speaks raw envelopes; the wrap/unwrap is the relay's job.
- `internal/e2e/internal/fakeclaude/main.go` (all 92 lines) — precedent for the `internal/e2e/internal/` shape: minimal surface area, no global state, single production file. fakeclaude is `package main`; fakephone is `package fakephone` (importable library), so the production file is `fakephone.go`, not `main.go`. The "one prod .go + one _test.go" shape carries over.
- `internal/e2e/internal/fakerelay/fakerelay.go:39-52` — import set the sibling harness uses (`github.com/coder/websocket`, `internal/protocol`). Same library, same pin (v1.8.13 in `go.mod`). Required for header interop with the fakerelay's `/v1/client` handler.
- `internal/e2e/internal/fakerelay/fakerelay.go:233-253` — what the relay reads off the phone upgrade request: `r.Header.Get("X-Pyrycode-Server" | "X-Pyrycode-Token" | "X-Pyrycode-Device-Name")`. The fakephone must set those header keys (Go's `http.Header` is canonicalised — set as `x-pyrycode-server` or `X-Pyrycode-Server`, both Get-equivalent).
- `internal/transport/wssclient.go:355-365` — the `realDial` helper. Shows the exact `websocket.DialOptions{HTTPHeader: ...}` shape and `websocket.Dial(ctx, url, opts)` call site this package mirrors. The fakephone's Dial is a one-call equivalent (no reconnect loop, no backoff, no ping pump — those are transport-layer concerns this harness deliberately omits).
- `CODING-STYLE.md` § Naming, § Error wrapping, § Testing — public API surface stays small; sentinel errors are package-level `var Err* = errors.New(...)`; tests use stdlib `testing` only with `httptest` + `coder/websocket` server-side.
- `docs/specs/architecture/295-fakerelay-harness.md` § "Public API surface", § "Why match `coder/websocket` instead of mixing libraries" — restates the library pin and "constructed running, no Start/Stop state machine" ergonomics that the sibling adopts and this ticket mirrors.

## Context

Phase 3 Track C, e2e tooling. Sibling slice to #295 (the fake relay) and consumed alongside it by the still-to-come daemon-side roundtrip test (third split from #254). The wire-protocol implementation landed across #246, #256, and the #271–#275 split, but there is no daemon-side test that exercises envelopes over a real WS endpoint. The roundtrip test needs both ends of the relay mocked in-process:

- #295 — the **fake relay**: in-process WS server that speaks the routing seam.
- **#296 (this ticket)** — the **fake phone**: in-process WS *client* that speaks the phone↔relay envelope protocol.
- The third ticket — the **roundtrip test**: stands up fakerelay + fakephone, points the daemon's WSS client at the fakerelay, asserts the appendix-flow envelope sequence end-to-end.

This ticket ships the client half of the seam in isolation. No consumers are wired up yet. Independent of #295: this slice can ship before, after, or in parallel with the fakerelay slice.

The library choice is fixed by #247: `github.com/coder/websocket` (already pinned in `go.mod`). The fakephone uses `websocket.Dial` on the client side and `conn.Read` / `conn.Write` / `conn.Close` — the same surface the existing transport client and fakerelay server use.

Scope guardrails restated from the ticket body:

- The harness has **no** knowledge of the routing envelope wrapper (`{conn_id, frame}`). The phone speaks raw `protocol.Envelope` JSON; wrapping/unwrapping is the relay's job (handled in #295).
- No `hello`/`hello_ack` handshake handling, no reconnect loop, no ping pump, no backfill state. The harness is a thin Send/Receive ergonomics layer; envelope sequencing is the consumer test's responsibility.
- No TLS. The harness dials the `ws://` URL exposed by `fakerelay.Server.URL()` (or any `httptest.NewServer`-backed URL) and reports the same in errors.

## Design

### Package layout

```
internal/e2e/internal/fakephone/fakephone.go        (new, ~70 production LOC)
internal/e2e/internal/fakephone/fakephone_test.go   (new, ~150 test LOC)
```

One production file. The package is small enough — one `Client`, four methods, two sentinel errors — that splitting adds navigation cost without legibility gain (mirrors fakerelay's single-file decision).

One test file. Helpers (an inline echo / scripted WS server via `httptest` + `websocket.Accept`) live alongside the cases; keeping them in one file avoids exporting them.

### Public API surface

Four exported names plus two sentinel errors:

```go
// Client is a WS client wired to the fake-relay's /v1/client endpoint.
// Not safe for concurrent Send or concurrent Receive (use one goroutine
// for each direction in tests). Close is safe to call concurrently from
// any goroutine; it unblocks an in-flight Receive.
type Client struct { /* unexported fields */ }

// Dial opens /v1/client at baseURL with the three required headers set
// verbatim from the arguments. baseURL is the bare ws://host:port form
// (e.g. as returned by fakerelay.Server.URL()); Dial appends the path.
// Returns an error if the upgrade fails (handshake error, non-101 status,
// network failure).
func Dial(ctx context.Context, baseURL, serverID, token, deviceName string) (*Client, error)

// Send marshals env and writes it as a single WS text frame. Returns
// ErrClosed if the client has been Closed; returns the underlying write
// error otherwise (with context wrapping).
func (c *Client) Send(env protocol.Envelope) error

// Receive reads one WS text frame and unmarshals it into a
// protocol.Envelope. On deadline expiry returns ErrReceiveTimeout
// (typed, matches with errors.Is). After Close, or if Close races with
// an in-flight Receive, returns ErrClosed.
func (c *Client) Receive(timeout time.Duration) (protocol.Envelope, error)

// Close shuts down the WS connection cleanly. Idempotent. Returns nil
// on first call; subsequent calls return nil. After Close, every Send
// and Receive returns ErrClosed.
func (c *Client) Close() error

// Sentinel errors. Both are matchable via errors.Is.
var (
    ErrReceiveTimeout = errors.New("fakephone: receive timeout")
    ErrClosed         = errors.New("fakephone: client closed")
)
```

Why `Dial` and not `New`: Dial signals "this function performs network I/O" and matches stdlib `net.Dial` / `websocket.Dial` ergonomics. The AC's signature pin makes this explicit.

Why `Receive(timeout time.Duration)` rather than `Receive(ctx context.Context)`: the AC pins the duration form. The implementation derives a `context.WithTimeout` internally, so the caller doesn't lose expressive power; they just don't carry a context for this specific call. Cancellation of the broader test is handled by `Close` (which unblocks Receive), not by an external context.

No logger field. Unlike fakerelay (which has multiple per-conn goroutines and benefits from Debug events), this harness is single-conn and synchronous from the test's point of view. Failures surface as returned errors. Tests use `t.Log` for diagnostics.

### Internal state

```go
type Client struct {
    conn *websocket.Conn

    mu     sync.Mutex
    closed bool
}
```

Two state items: the connection, and a `closed` flag guarded by the mutex. The mutex protects only the closed flag — not `conn.Read` / `conn.Write` (the `coder/websocket` library permits one concurrent reader and one concurrent writer, plus concurrent `Close`).

No `closeOnce`: a `sync.Mutex` + bool gives the same idempotency plus the read-the-flag-from-Send/Receive use, which `sync.Once` does not cover ergonomically.

No `recvCh` / `sendCh` / pump goroutines. Unlike `internal/transport.Client`, which needs pumps because it bridges async lifecycle events (reconnect, ping) with synchronous send/receive, this harness is synchronous end-to-end: each method blocks on its own I/O. Adding pumps would double the surface area for no consumer benefit.

### Method bodies (contracts, not implementations)

`Dial`:

1. Build `targetURL = baseURL + "/v1/client"`. No URL parsing; treat `baseURL` as opaque prefix.
2. Construct `http.Header` with `x-pyrycode-server`, `x-pyrycode-token`, `x-pyrycode-device-name` set verbatim from the arguments. Do not validate emptiness — the harness forwards what it's given so consumer tests can probe the relay's rejection paths.
3. Call `websocket.Dial(ctx, targetURL, &websocket.DialOptions{HTTPHeader: hdr})`. On error, return the wrapped error (`fmt.Errorf("fakephone dial: %w", err)`). The `*http.Response` second return value is ignored — error wrapping suffices for tests to assert handshake failures.
4. Set `conn.SetReadLimit(maxFrameBytes)` to match fakerelay's per-frame ceiling (constant `maxFrameBytes = 1 << 20`).
5. Return `&Client{conn: conn}`.

`Send(env)`:

1. Acquire `c.mu`; if `closed`, release and return `ErrClosed`.
2. Release `c.mu` (do NOT hold across I/O).
3. `data, err := json.Marshal(env)`; on error return `fmt.Errorf("fakephone marshal: %w", err)` — should not happen for well-formed Envelopes, surfaces caller bugs.
4. `err = c.conn.Write(context.Background(), websocket.MessageText, data)`.
5. If err != nil: re-acquire `c.mu`; if `closed`, return `ErrClosed`; otherwise return the wrapped write error.

`Receive(timeout)`:

1. Acquire `c.mu`; if `closed`, release and return zero-envelope + `ErrClosed`.
2. Release `c.mu`.
3. `ctx, cancel := context.WithTimeout(context.Background(), timeout)`; defer cancel.
4. `_, data, err := c.conn.Read(ctx)`.
5. If err != nil:
   - Re-acquire `c.mu`; if `closed`, return zero + `ErrClosed`.
   - Else if `errors.Is(err, context.DeadlineExceeded)` OR `ctx.Err() == context.DeadlineExceeded`, return zero + `ErrReceiveTimeout`. (Check both: `coder/websocket` may surface deadline as a wrapped error or as a custom one depending on path; both forms must map to the sentinel.)
   - Else return zero + wrapped error.
6. `var env protocol.Envelope; json.Unmarshal(data, &env)` — on unmarshal error return zero + wrapped error.
7. Return env, nil.

`Close`:

1. Acquire `c.mu`; if already `closed`, release and return nil (idempotent).
2. Set `closed = true`; release `c.mu`.
3. `_ = c.conn.Close(websocket.StatusNormalClosure, "phone closing")`. The `coder/websocket` library guarantees this unblocks any concurrent `Read` / `Write` on the same conn with an error; that error path in Receive sees `closed == true` and maps to `ErrClosed`.
4. Return nil.

Order matters in step 2 → step 3: set the flag **before** closing the conn so that a Receive/Send that loses the race observes `closed == true` when it post-checks the flag after the I/O error.

### Concurrency model

```
Test goroutine                Phone Client
─────────────                  ─────────────
Dial(...)         ──────►     websocket.Dial → *websocket.Conn
Send(env)         ──────►     conn.Write(text frame)
Receive(timeout)  ──────►     conn.Read(ctx-with-deadline)
                                    │
                              (blocked here)
Close() ──────────────►       conn.Close → Read unblocks with err
                                    │
                              Receive sees closed=true, returns ErrClosed
```

Two concurrency rules the docstring on `Client` makes explicit:

- One goroutine calls Send, one goroutine calls Receive (may be the same). `coder/websocket` permits one concurrent reader and one concurrent writer; we inherit that.
- Close is safe from any goroutine, including concurrently with an in-flight Send/Receive.

No pumps, no channels, no per-conn ctx tree. The only synchronisation primitive is `c.mu` protecting one bool.

### Error handling

Failure modes by source:

1. **Dial fails (network, non-101, TLS)** → `Dial` returns wrapped error. No Client created.
2. **Send on closed Client** → returns `ErrClosed` (pre-check before Write).
3. **Write fails after a concurrent Close** → returns `ErrClosed` (post-check after Write).
4. **Write fails for any other reason** → returns wrapped write error.
5. **Marshal of caller's envelope fails** → returns wrapped marshal error (caller bug; e.g. a `time.Time` with an unrepresentable value — won't happen in practice but surfaced rather than panicking).
6. **Receive on closed Client** → returns `ErrClosed` (pre-check before Read).
7. **Receive's deadline expires** → returns `ErrReceiveTimeout`. The conn remains usable; the next Receive can succeed.
8. **Read fails after a concurrent Close** → returns `ErrClosed` (post-check after Read).
9. **Read fails for any other reason** → returns wrapped read error.
10. **Unmarshal of received frame fails** → returns wrapped unmarshal error. Conn remains usable.

`Close` returns nil unconditionally. The underlying `conn.Close` error is intentionally discarded — by the time the test's goroutine calls Close, the WS state is past the point where a close-frame error matters; the only consumer of the return is "did we panic?" and we don't.

### Why match `coder/websocket` instead of mixing libraries

#247 pinned `github.com/coder/websocket`; #295 reuses it on the relay-server side. Using a different library here (`gorilla/websocket`, `nhooyr.io/websocket`) would introduce a second WS dependency, double the surface area to learn, and risk interop quirks (close-code formatting, header canonicalisation) between the test phone and the test relay. The AC mentions "library choice must match #247 / #295" explicitly.

## Testing strategy

One file (`fakephone_test.go`), stdlib `testing` only. Each test wires the client to an inline `httptest.NewServer` whose handler upgrades via `websocket.Accept` and runs a scripted server-side loop (echo, send-one-then-idle, capture-headers-then-idle, etc.) — **not** to a `fakerelay.Server`. Cross-harness testing belongs in the roundtrip ticket, not here.

Helper (test-internal): a small `newEchoServer(t, headerCapture *http.Header)` factory that returns `(baseURL string, cleanup)`. The optional `headerCapture` lets a single test assert the headers the upgrade carried.

Test cases (one `Test*` function each unless noted):

- **`TestDial_ForwardsHeaders`** — capture headers in the handler; Dial with `serverID="alpha"`, `token="secret"`, `deviceName="Juhana's Pixel 8"`; assert the captured headers contain those values verbatim (use `r.Header.Get`, which canonicalises automatically). Covers AC bullet 1.
- **`TestSend_RoundTripsThroughEcho`** — Dial → Send an Envelope with `id=1, type="hello", ts=<fixed>, payload=json.RawMessage("{}")` → echo server reads back the frame → assert bytes match what the phone sent (or unmarshal both sides and compare via `reflect.DeepEqual` on a normalised Envelope; remember the `time.Time.Equal` rule from PROJECT-MEMORY for the `TS` field — compare via .Equal, not ==). Covers AC bullet 2.
- **`TestReceive_DecodesEnvelope`** — server sends a single marshaled Envelope text frame; phone calls Receive(2s); assert returned Envelope.ID/Type/Payload match. Covers AC bullet 3.
- **`TestReceive_TimeoutReturnsSentinel`** — server upgrades but never writes; phone Receive(50ms); assert `errors.Is(err, ErrReceiveTimeout)`. Then call Receive again with a longer deadline after server sends a frame; assert it succeeds (proves the conn is still usable after a timeout). Covers AC bullet 3, second clause.
- **`TestClose_UnblocksReceive`** — server upgrades, never writes; start `Receive(5s)` in a goroutine; from the main goroutine call `Close()`; assert the receive returns with `errors.Is(err, ErrClosed)` within a short bound (~500ms). Then call Send and Receive again; assert both return `ErrClosed`. Covers AC bullet 4.
- **`TestClose_IsIdempotent`** — call Close twice; assert second call returns nil and does not panic. (One-liner; can be a subtest of the previous case.)
- **`TestDial_FailsOnHandshakeError`** — point Dial at an `httptest.NewServer` whose handler returns `http.StatusForbidden` (no upgrade); assert Dial returns a non-nil error. Validates the error path without asserting on the specific text.

What NOT to test:

- `coder/websocket` framing, fragmentation, or close-code semantics — library contract.
- Header canonicalisation rules — `net/http` contract.
- TLS — out of scope (the harness uses `ws://`).
- Integration with `fakerelay.Server` — belongs in the third (roundtrip) ticket.
- Concurrent Send-by-two-goroutines behaviour — the doc-comment forbids it; the test doesn't need to police it.

## Out of scope (do not implement here)

- The `hello` / `hello_ack` handshake. The harness ships raw `Send`/`Receive`; envelope sequencing is the consumer test's responsibility.
- Reconnect logic, ping/pong heartbeat, backoff. Transport-layer concerns owned by `internal/transport`.
- Routing-envelope wrap/unwrap. The phone speaks raw envelopes; the relay does the wrap (covered in #295).
- An exported `Config` struct, dial-timeout knobs, or context for Send/Receive. Add when a consumer ticket needs it.
- A logger field. Failures surface as returned errors; tests use `t.Log`.
- Backfill state, `last_seen_ts` handling, push-token registration — application-layer concerns.

## Open questions

None. Every AC maps to a code path:

- "constructed via `Dial(ctx, baseURL, serverID, token, deviceName) (*Client, error)`, where Dial performs the `/v1/client` upgrade with `x-pyrycode-server`, `x-pyrycode-token`, and `x-pyrycode-device-name` headers set verbatim" → § Method bodies / Dial steps 1–5; `TestDial_ForwardsHeaders` and `TestDial_FailsOnHandshakeError`.
- "`Send(env)` marshals a v1 envelope … and writes exactly one WS text message" → § Method bodies / Send; `TestSend_RoundTripsThroughEcho`.
- "`Receive(timeout) (env, error)` reads one WS text message, unmarshals … on deadline exceeded returns a typed error the caller can match against" → § Method bodies / Receive; `TestReceive_DecodesEnvelope` and `TestReceive_TimeoutReturnsSentinel`.
- "`Close()` closes the WS cleanly; any in-flight `Receive` unblocks with a recognisable error, and subsequent `Send`/`Receive` calls return that same error rather than panicking" → § Method bodies / Close; `TestClose_UnblocksReceive` and `TestClose_IsIdempotent`.
