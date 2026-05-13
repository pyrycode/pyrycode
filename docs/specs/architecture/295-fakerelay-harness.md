# 295 — `net/e2e`: fakerelay harness package

## Files to read first

- `docs/protocol-mobile.md:67-122` — § Authentication. The wire-spec for the `/v1/server` and `/v1/client` upgrade contracts: which headers are required, first-claim-wins for server-ids, the `4409`/`4404` close codes. The fake relay models the **routing** half of this contract; it does NOT model token validation, the 30-second grace period on server-id release (out of scope per AC: "while the first holder's connection is open"), or the binary-side hello/hello_ack dance.
- `docs/protocol-mobile.md:100-122` — § Routing envelope. The exact JSON shape (`{"conn_id": "...", "frame": ...}`) carried on every binary↔relay frame. Phone↔relay frames are raw (no wrapper). The fake relay's job is the wrap/unwrap transform at the routing seam.
- `internal/e2e/internal/fakeclaude/main.go` (all 92 lines) — precedent for an `internal/e2e/internal/` package: minimal surface area, env-only or constructor-only config, no global state, no transitive deps beyond stdlib + one library. fakeclaude is a `package main` binary because it re-execs; fakerelay is an importable library (`package fakerelay`), so the file is `fakerelay.go` not `main.go`, but the "one production .go + one _test.go" shape carries over.
- `internal/transport/wssclient.go:1-35` — package doc-comment style and the `coder/websocket` import surface. This is the project's first WS user; #295 is the second. Same library, same dependency pin.
- `internal/transport/wssclient_test.go:102-154` (`newTestRelay`) — existing pattern for an `httptest.NewServer` + `websocket.Accept` upgrader. The new package generalises this from "echo server" to "two-endpoint routing server"; the file structure (`httptest.NewServer` wrapper, `t.Cleanup` shutdown, `*atomic.*` for cross-goroutine state) is the precedent to follow.
- `internal/transport/wssclient.go:337-396` — the recv/send/ping pump triplet and the cancel-then-drain shutdown pattern. The fake relay reuses the recv-pump + send-pump shape per accepted conn (no ping loop — `coder/websocket` auto-responds to pings on the server side, and this harness doesn't need to originate them).
- `CODING-STYLE.md` § Concurrency, § Naming, § Logging — `*slog.Logger` injected via constructor, `context.Context` everywhere, channels for coordination, `sync.Mutex` for state, `t.Cleanup` for shutdown in tests. No global state.
- `docs/protocol-mobile.md:711-735` — the worked example sequence diagram (binary upgrades, phone upgrades, relay assigns `conn_id`, frames flow wrapped). Read this once to anchor the data-flow diagram in § Design.

## Context

Phase 3 Track C, e2e tooling. The wire-protocol implementation landed across #246–#250, #256, #271–#275 with unit/integration coverage per ticket but **no daemon-side test against a real WS endpoint**. The daemon-side e2e coverage requires both ends of the relay mocked in-process:

- This ticket (#295) — the **fake relay**: an in-process WS server that speaks the mobile↔relay routing protocol (server upgrade, client upgrade, `{conn_id, frame}` wrap/unwrap).
- A sibling ticket — the **fake phone**: an in-process WS client that speaks the phone↔relay envelope protocol.
- A third ticket — the **roundtrip test**: stands up fake relay + fake phone, points the daemon's WSS client at the fake relay, asserts the appendix-flow envelope sequence end-to-end.

This ticket ships the routing seam in isolation; no consumers wire it up yet.

The library choice (`github.com/coder/websocket`) is **fixed** by #247 and already pinned in `go.mod` (v1.8.13). The fake relay uses `websocket.Accept` on the server side and the same library appears on the test-client side for symmetry.

Scope guardrails restated from the ticket body:

- No TLS termination (the harness binds over loopback HTTP and reports `ws://…`, not `wss://…`).
- No persistence (server-ids and conn-ids live in maps for the Server's lifetime).
- No rate limiting.
- No token validation (the relay does not inspect `x-pyrycode-token` beyond non-emptiness — the binary owns that check per #249).
- No server-id grace period (the AC pins "while the first holder's connection is open"; release is immediate on disconnect).

## Design

### Package layout

```
internal/e2e/internal/fakerelay/fakerelay.go        (new, ~180 production LOC)
internal/e2e/internal/fakerelay/fakerelay_test.go   (new, ~200 test LOC)
```

One production file. The package is small enough — one `Server`, two HTTP handlers (`/v1/server`, `/v1/client`), one routing core, helpers — that splitting `binary.go`/`phone.go`/`routing.go` adds navigation cost without legibility gain (matches `fakeclaude`'s decision to keep everything in one file).

One test file. Helpers (a test WS-client wrapper that wraps `websocket.Dial` with the required headers; a `mustReadJSON` decoder) are shared across cases; keeping them in one file avoids exporting them.

### Public API surface

Three exported names:

```go
type Server struct { /* unexported fields */ }

// New returns a running fake relay bound to a random localhost port.
// The logger receives lifecycle and routing events at Debug. Required.
// The returned Server is ready for connections; Close shuts it down.
func New(logger *slog.Logger) *Server

// URL reports the base ws:// URL (no trailing path). Callers append
// "/v1/server" or "/v1/client". Example: "ws://127.0.0.1:54123".
func (s *Server) URL() string

// Close shuts down the listener and all in-flight conns. Idempotent.
func (s *Server) Close() error
```

Mirror of `httptest.Server` ergonomics (`URL` is a method returning the running address; `Close` is idempotent), constrained to the one purpose the harness has.

The `Server` is constructed running. The alternative ("`New(logger)` returns an unstarted Server; caller calls `Start()`") matches `http.Server` but adds a state machine for no consumer benefit — every test wants a running relay immediately.

`Config` is deferred. The AC's surface is "boot it, get a URL, close it"; adding a `Config` struct with one nullable field is premature. If a future ticket needs to inject a `conn_id` minter or override timeouts, a `NewWithConfig` constructor adds itself non-breakingly.

### Internal data model

```go
type Server struct {
    log  *slog.Logger
    http *httptest.Server  // owns the listener and net.Conn handoff

    mu       sync.Mutex
    binaries map[string]*binaryConn   // serverID -> binary
    phones   map[string]*phoneConn    // connID    -> phone
    connSeq  uint64                   // monotonic source for c-N ids

    closeOnce sync.Once
    closeCh   chan struct{}
    wg        sync.WaitGroup
}

type binaryConn struct {
    serverID string
    conn     *websocket.Conn
    sendCh   chan []byte         // wrapped {conn_id, frame} JSON
    done     chan struct{}       // closed when this conn's serve loop returns
}

type phoneConn struct {
    serverID string
    connID   string
    conn     *websocket.Conn
    sendCh   chan []byte         // unwrapped frame bytes
    done     chan struct{}
}
```

Why two maps, not one combined registry: the lookup keys are different (`serverID` for binary, `connID` for phone), and a phone connection without its binary is invalid (`/v1/client` rejects with 503 if the binary's gone). Separate maps make the invariants local. Both maps are guarded by the single `s.mu` — there's no ordering graph (only ever take one).

`connSeq` is a `uint64` counter incremented under `s.mu` for deterministic, test-friendly `conn_id` values (`c-1`, `c-2`, …). UUIDs would also satisfy the AC ("opaque string"), but determinism makes the consumer test's assertions readable.

### Endpoint contracts

#### `GET /v1/server` — binary upgrades

1. Read `x-pyrycode-server` from the request. If empty, respond `400 Bad Request` (no upgrade).
2. Under `s.mu`, check if `binaries[serverID]` exists. If yes, respond `409 Conflict` (no upgrade); release the lock.
3. Otherwise, call `websocket.Accept(w, r, nil)`. On error, return (Accept has already written the response).
4. Construct the `binaryConn`, install it in `binaries[serverID]` under `s.mu`, increment `wg`, spawn serve goroutine, return from the handler.
5. The serve goroutine runs `recvPump` + `sendPump` until one returns; cancels the other; cleans up.
   - On exit: under `s.mu`, delete `binaries[serverID]`, **close every phone whose `serverID` matches** (so phones don't hang on a dead binary — the fake relay models the relay's behavior of dropping phones whose binary went away). Closing a phone is: cancel its serve ctx, the phone's serve loop unwinds and removes itself from `phones`.

   AC parallel: the ticket pins "no goroutine leaks under `go test -race`." Closing dependent phones on binary loss is the only way to satisfy this without leaking phones whose binary disappeared.

The 409 mechanism (pre-upgrade HTTP status, not post-upgrade `4409` WS close) is the fake relay's choice. The AC ("rejects subsequent server upgrades") is satisfied either way; pre-upgrade rejection is observable in `websocket.Dial`'s returned error (it returns the HTTP status) and is simpler than implementing the post-upgrade close-code dance. Deviation from production wire codes is documented in the package comment so a future maintainer doesn't mistake the fake for the spec.

#### `GET /v1/client` — phone upgrades

1. Read `x-pyrycode-server`, `x-pyrycode-token`, `x-pyrycode-device-name`. If any is empty, respond `400 Bad Request`.
2. Under `s.mu`: check `binaries[serverID]`; if absent, release the lock and respond `503 Service Unavailable`.
3. Otherwise, allocate `connID = fmt.Sprintf("c-%d", s.connSeq+1)`, increment `connSeq`, release the lock.
4. Call `websocket.Accept(w, r, nil)`. On error, return.
5. Construct `phoneConn{serverID, connID, …}`, install in `phones[connID]` under `s.mu`, increment `wg`, spawn serve goroutine, return from handler.
6. The serve goroutine runs `recvPump` + `sendPump` until one returns; cancels the other; cleans up:
   - Under `s.mu`, delete `phones[connID]`.

The 503 status on no-binary-online directly maps to the AC bullet 5. Tests assert this via the error returned by the test client's `websocket.Dial`.

### Routing rules

Phone → binary (one phone's frame reaches the bound binary):

1. Phone's `recvPump` reads a frame `b` from the phone WS conn (text message).
2. Construct the JSON envelope: `{"conn_id": "c-N", "frame": <raw b>}`. Implementation note: this requires that `b` is itself valid JSON, because the wrapper places it as a JSON value. The fake relay does NOT validate this — if the phone sends a non-JSON text frame, the wrapper places the raw bytes as a JSON string (via `json.RawMessage`) only if they're valid JSON; otherwise we marshal `frame` as a base64 string OR fail. **Simplification chosen**: the fake relay treats `frame` as `json.RawMessage` and assumes the phone sends well-formed JSON envelopes. If the phone sends invalid JSON, the wrap fails, the recv-pump logs at Debug and closes the phone. This matches reality — the production relay shuttles bytes opaquely, but for test ergonomics requiring valid JSON makes the wrapped output legible to the binary-side test.
3. Look up the binary via `s.mu` + `binaries[phone.serverID]`. If the binary is gone (race with binary disconnect), close the phone.
4. Send the wrapped bytes to the binary's `sendCh`. If the send-pump is full (unbuffered channel), block briefly; if the binary's done channel closes, abandon and close the phone.

Binary → phone (binary addresses a frame to a specific phone via `conn_id`):

1. Binary's `recvPump` reads a frame `b` from the binary WS conn (text).
2. Unmarshal as `{"conn_id": string, "frame": json.RawMessage}`. If unmarshal fails, log at Debug and continue (skip the frame — the production relay would close the binary with a protocol error, but for a test harness "drop and continue" surfaces bugs in the binary's wrapping without hard-killing the test).
3. Look up `phones[connID]` under `s.mu`. If absent (stale conn_id, phone gone), log at Debug and continue.
4. Send `frame` (the unwrapped raw bytes) to the phone's `sendCh`.

Why "log and continue" instead of "close the binary" on malformed input: the consumer tests want to assert the routing behavior, not the relay's protocol-violation handling. A malformed frame is a test bug; surfacing it as a Debug log + continued operation lets the test fail on the missing expected receive rather than on a relay-side shutdown that hides the cause.

### Concurrency model

```
                  ┌─────────────────────────────────────────┐
                  │ httptest.Server (one accept goroutine)   │
                  │   /v1/server → spawn binary serve        │
                  │   /v1/client → spawn phone serve         │
                  └─────────────────────────────────────────┘
                                    │
              ┌─────────────────────┴──────────────────────┐
              ▼                                             ▼
   ┌──────────────────────┐                     ┌──────────────────────┐
   │ binaryConn serve     │                     │ phoneConn serve      │
   │   recvPump (binary)  │                     │   recvPump (phone)   │
   │     → unwrap, lookup │                     │     → wrap with cid  │
   │     → phone.sendCh   │                     │     → binary.sendCh  │
   │   sendPump (binary)  │                     │   sendPump (phone)   │
   │     ← binary.sendCh  │                     │     ← phone.sendCh   │
   └──────────────────────┘                     └──────────────────────┘
```

Two goroutines per accepted conn (recv + send). With `coder/websocket`'s built-in ping/pong, no ping-loop goroutine is needed on the server side (the library auto-responds to pings). All goroutines share a per-conn ctx derived from `request.Context()` cancelled when serve returns; `s.closeCh` closing causes `Server.Close` to additionally force-close each `*websocket.Conn`, which unblocks any blocked `Read`/`Write`.

Lock order: only `s.mu`. Held only for map mutations + `connSeq` increment + binary/phone lookup; never held across `conn.Read`, `conn.Write`, or channel sends.

The `binary.sendCh` and `phone.sendCh` are **unbuffered**. A slow binary that doesn't read from its WS conn → its sendPump blocks on `conn.Write` → phones writing to it block on `binary.sendCh`. This is intentional backpressure; the consumer test should drive both sides. If a test wants to stall the binary, the failure mode is observable as a stuck phone send rather than silently dropped frames.

### Shutdown sequence

`Server.Close()`:

1. `closeOnce.Do(...)` — closes `s.closeCh` (idempotent).
2. `s.http.Close()` — shuts down the listener and waits for in-flight HTTP handlers to return. New connections are rejected.
3. Under `s.mu`, collect every `binaryConn` and `phoneConn`; for each, call `conn.Close(websocket.StatusNormalClosure, "server closing")`. This unblocks blocked Reads/Writes on the per-conn pumps.
4. `s.wg.Wait()` — wait for every serve goroutine to return.
5. Return `nil`.

The pumps observe shutdown via `conn.Read` / `conn.Write` returning an error after the force-close; they don't observe `s.closeCh` directly. Single signal path: close the WS conn, the pumps unwind, the serve goroutine cleans up. No race between Close and an in-flight upgrade because `s.http.Close()` waits for the upgrade handler to return before step 3 (the handler has already added the conn to the map and spawned the goroutine by the time it returns).

The one subtle case: a `/v1/server` handler that calls `websocket.Accept` and then races with `Server.Close()`. `httptest.Server.Close` waits for handlers. After Accept returns, the handler installs the conn under `s.mu`, spawns the goroutine, returns. Now the goroutine is running. If `Server.Close` was called concurrently, its `s.http.Close` returns after the handler does; then step 3 sees the new conn and force-closes it; step 4 waits for its goroutine. No leak.

### Error handling

Failure modes by source:

1. **Missing/empty required headers** → handler responds `400` before upgrade. No conn registered.
2. **Server-id already claimed** → handler responds `409`. No conn registered.
3. **Phone connects for unknown server-id** → handler responds `503`. No conn registered.
4. **`websocket.Accept` fails** (TLS error, hijack failure, etc.) → handler returns; Accept has already written the response. No conn registered.
5. **`recvPump` read error** (peer close, frame > library default limit, ctx cancel) → pump returns; serve cancels the other pump; cleanup runs.
6. **`sendPump` write error** (peer gone, ctx cancel) → same as #5.
7. **Phone tries to send a non-JSON frame** → wrap fails; phone is closed by the recv-pump. Logged at Debug.
8. **Binary sends a malformed wrapper or unknown conn_id** → drop the frame; continue serving. Logged at Debug.
9. **`Server.Close` called** → force-closes all conns; pumps unwind; `wg.Wait` returns.

The package emits no errors upward via the public surface beyond `Close() error` returning `nil`. Per-conn protocol violations are logged at Debug (so tests under `-v` can diagnose) and silently dropped; this matches the harness role of "exercise the routing seam, surface bugs as missing receives in the consumer."

### Why match `coder/websocket` instead of mixing libraries

#247 pinned `github.com/coder/websocket` for the binary's outbound client. The fake relay is the test counterpart and must accept those connections. Using a different library on the server side (e.g. `gorilla/websocket`) would introduce a second WS dependency, double the surface area to learn, and risk interop quirks (close-code formatting, ping-handling differences). The ticket's "matching #247's choice" is explicit; the only call site is `websocket.Accept(w, r, nil)` and `conn.Read`/`conn.Write`/`conn.Close` — same surface the existing test relay uses.

## Testing strategy

One file (`fakerelay_test.go`), stdlib `testing` only, table-driven where applicable. All tests use a single helper that constructs a `*Server` + `t.Cleanup(s.Close)` and a small `dialBinary`/`dialPhone` helper that wraps `websocket.Dial` with the required headers.

Test cases (each is a single `Test*` function):

- **`TestBinaryUpgrade_RequiresServerHeader`** — `dialBinary` with no `x-pyrycode-server` header; assert dial error wraps HTTP 400.
- **`TestBinaryUpgrade_FirstClaimWins`** — dial binary A with `serverID = "alpha"` (succeed); dial binary B with `serverID = "alpha"` (assert HTTP 409). Then close binary A; dial binary C with `serverID = "alpha"` (assert it succeeds — release is immediate, no grace period). Catches the "first holder open" semantics.
- **`TestPhoneUpgrade_RequiresAllHeaders`** — three sub-cases, each omitting one of the three required headers; assert HTTP 400. Table-driven.
- **`TestPhoneUpgrade_NoBinaryOnline`** — `dialPhone` with `serverID = "ghost"` (no binary registered); assert dial error wraps HTTP 503.
- **`TestPhoneToBinary_FrameWrappedWithConnID`** — connect binary; connect phone (assert conn_id is `"c-1"` via the first wrapped frame received by the binary); phone sends `{"id":1,"type":"hello"}` as a JSON text frame; binary reads next frame; assert it unmarshals to `{"conn_id":"c-1","frame":{"id":1,"type":"hello"}}`.
- **`TestBinaryToPhone_FrameUnwrapped`** — same setup; binary sends `{"conn_id":"c-1","frame":{"id":2,"type":"reply"}}`; phone reads next frame; assert it equals `{"id":2,"type":"reply"}`.
- **`TestConnIDIncrementsPerPhone`** — connect binary; connect two phones in sequence; binary should see wrapped frames whose `conn_id`s are `"c-1"` and `"c-2"`.
- **`TestPhoneClosedWhenBinaryGoes`** — connect binary + phone; close binary; assert the phone's `conn.Read` returns an error within a short timeout (the relay drops the phone). This proves the cleanup-cascades-on-binary-disconnect path.
- **`TestServerClose_NoGoroutineLeaks`** — connect binary + 2 phones; capture `runtime.NumGoroutine()` baseline; call `Server.Close()`; assert (a) `Close` returns `nil` within 1s, (b) `NumGoroutine` returns within ±2 of the baseline within 500ms after Close. Run under `t.Parallel()` so the assertion isn't fooled by the test harness's own goroutines.

What NOT to test:

- `coder/websocket` framing/ping/pong semantics — library contract.
- TLS — out of scope (the harness binds plain `ws://`).
- Token contents validity — the relay deliberately accepts any non-empty token.
- Rate limiting, request throttling — out of scope.
- The 30s grace period on server-id release — explicitly excluded by the AC.

## Out of scope (do not implement here)

- Token validation. The relay forwards any non-empty token bytes; the binary owns the validity check (#249).
- The 30-second server-id release grace period from `docs/protocol-mobile.md` § Authentication. The AC pins immediate release.
- Production WS close codes (`4409`, `4404`, `4401`). The harness uses HTTP 409/503 pre-upgrade for ergonomics. Documented in the package comment as a deliberate deviation.
- `hello`/`hello_ack` envelope handling. The harness is the **routing seam**; envelope dispatch is a separate layer.
- The fake-phone client (sibling ticket).
- The roundtrip e2e test (third ticket).
- TLS termination, certificate pinning, or any HTTPS surface.
- A `Config` struct with overrides for `connSeq` start value, send-channel buffer size, etc. Add when a consumer ticket needs it.
- A `Server.Connections()` method or any inspection API. Tests assert behavior via the WS endpoints, not via reflection on the Server's internal maps.

## Open questions

None. Every AC maps to a code path:

- "boots an in-process WS server bound to a random localhost port" → `New` constructs `httptest.NewServer`; `URL()` returns its `ws://…` form.
- "accepts `/v1/server` upgrades and validates `x-pyrycode-server` is non-empty; rejects subsequent server upgrades with the same `server-id` while the first holder's connection is open" → § Endpoint contracts / `/v1/server` steps 1–2 + serve-cleanup step that removes the binary on disconnect.
- "accepts `/v1/client` upgrades and validates `x-pyrycode-server`, `x-pyrycode-token`, and `x-pyrycode-device-name` are non-empty; assigns a fresh `conn_id` per phone connection" → § Endpoint contracts / `/v1/client` steps 1–3.
- "routes frames per § Routing envelope: phone→binary frames are wrapped as `{conn_id, frame}` JSON to the binary side; binary→phone frames are unwrapped" → § Routing rules.
- "phone upgrades with a `server-id` that has no binary connection are rejected at the WS layer" → § Endpoint contracts / `/v1/client` step 2 (HTTP 503).
- "`Server.Close()` shuts down the listener and all active connections cleanly with no goroutine leaks under `go test -race`" → § Shutdown sequence + `TestServerClose_NoGoroutineLeaks`.
- "self-contained tests in `main_test.go` that connect a plain `gorilla/websocket` (or `nhooyr.io/websocket`, matching #247's choice) client to assert…" → § Testing strategy. Note: the file is `fakerelay_test.go` (library, not binary) and the library is `coder/websocket`; the AC's "or `nhooyr.io/websocket`, matching #247" resolves to this. Flag in PR description so PO can update the ticket body wording.
- "no TLS termination, no persistence, no rate limiting" → § Context scope guardrails + § Out of scope.
