# #301 — Wire `relay.Connect` into the pyry daemon startup

## Files to read first

- `cmd/pyry/main.go:387-499` — `runSupervisor` startup path; this is where the relay goroutine attaches.
- `cmd/pyry/main.go:78-109` — `resolveSocketPath` / `resolveRegistryPath` family; `resolveServerIDPath` follows the same shape.
- `cmd/pyry/pair.go:38-104` — `resolveServerIDPath`, `resolveConfigPath`, `resolveRelay` precedence helper to mirror.
- `cmd/pyry/pair.go:166` — `identity.LoadOrCreate(resolveServerIDPath(...))` — the load path to reuse.
- `internal/relay/connection.go:96-139` — `Connect`, `Config`; the wss-only check at line 113-116 is what the test seam relaxes.
- `internal/relay/connection.go:163-179` — `Wait` / `Close` lifecycle; the goroutine drives off these.
- `internal/relay/connection.go:183-212` — `run` loop: shows transport reconnect happens internally, terminal classification surfaces via `Wait`.
- `internal/relay/connection.go:295-302` — `classifyTransportErr`; `ErrServerIDConflict` is the only fatal classification today.
- `internal/relay/connection_test.go:250-273` — `connectWithClient` test seam; the new `AllowInsecureScheme` field replaces it for production-shaped Connect calls.
- `internal/config/config.go` — the on-disk schema; `RelayURL` field + `DefaultConfig()` precedence base.
- `internal/identity/server_id.go`, `store.go` — `LoadOrCreate` returns canonical UUIDv4 `ServerID`; never overwrites on existing-file path.
- `internal/transport/wssclient.go:181-220` — `Connect` reconnect-on-non-fatal-close behaviour confirms architect's "no goroutine-side reconnect logic" decision.
- `internal/e2e/internal/fakerelay/fakerelay.go:37-229` — current fakerelay; § "Deviations from the production wire spec" calls out HTTP 409 vs WS 4409.
- `internal/e2e/internal/fakerelay/fakerelay.go:151-229` — `handleBinary`; the conflict path becomes mode-switchable, hello dispatch is appended.
- `internal/e2e/internal/fakerelay/fakerelay.go:332-373` — `binaryRecvPump` / `binarySendPump`; the new hello detection slots in alongside the existing routing-envelope path.
- `internal/e2e/harness.go:200-220, 318-394` — `StartIn` + `spawnWith`/`spawnOpts` for the e2e test (`extraEnv` + `extraFlags` patterns).
- `cmd/pyry/main.go:444-475` — `signal.NotifyContext` `cancel` is the single shutdown lever; the relay goroutine reuses it.
- `docs/protocol-mobile.md` § Error codes line 552 — `4409` is the server-id-already-claimed close code.

## Context

`internal/relay.Connect` ships as a pure library; nothing in `cmd/pyry` or `internal/supervisor` imports it, so the daemon never opens a relay connection. The `RelayURL` field in `config.Config` is consumed only by `cmd/pyry/pair.go` to mint the QR payload.

This slice wires the dial into the daemon startup path and adds the test seam so e2e tests can point at a plain-`ws://` fake relay. No phone-frame handling, no per-conn dispatcher, no handlers — `Connection.Frames()` is drained-and-discarded in this slice (the dispatcher slice consumes it).

## Design

### Package-level changes

#### `internal/relay` — `AllowInsecureScheme` test seam (~5 LOC)

Add one boolean field to `Config` and relax the scheme check accordingly:

```go
// In Config:
//   AllowInsecureScheme, when true, accepts ws:// in addition to wss://.
//   Test-only seam (e2e against an httptest fakerelay). Production callers
//   leave this false; the daemon enables it from PYRY_ALLOW_INSECURE_RELAY=1.
AllowInsecureScheme bool
```

`Connect`'s scheme check (lines 113-116) becomes:

```go
if parsedURL.Scheme != "wss" && !(cfg.AllowInsecureScheme && parsedURL.Scheme == "ws") {
    return nil, fmt.Errorf("%w: RelayURL scheme must be wss (got %q)",
        ErrInvalidConfig, parsedURL.Scheme)
}
```

The existing `connectWithClient` test seam (`connection.go:144`) stays — it bypasses the headers/transport build entirely, which is heavier than what production-shaped tests need. The new field unlocks a production-shaped path through `Connect` for callers that just need ws://.

`TestConfig_Validation_TableDriven` keeps its `"ws scheme rejected"` row (validates the production default path, `AllowInsecureScheme=false`). One new row asserts ws:// passes when `AllowInsecureScheme=true`.

#### `cmd/pyry/relay.go` — new file (~45 LOC)

Holds the relay-startup helpers. Keeping them out of `main.go` mirrors `cmd/pyry/pair.go`'s split.

Exports nothing; package-internal:

```go
// resolveRelayURL returns the first non-empty value among:
//   1. flagValue   (from -pyry-relay)
//   2. envValue    (from PYRY_RELAY_URL)
//   3. cfg.RelayURL (from ~/.pyry/config.json — config.Load already
//      overlays DefaultConfig, so this leg covers both the operator
//      file and the built-in default)
// Returns "" only if all three are empty (config.Load's overlay makes
// that effectively unreachable in production).
func resolveRelayURL(flagValue, envValue string, cfg config.Config) string

// startRelay opens the binary↔relay leg in a supervisor-owned goroutine.
// Returns nil immediately when relayURL == "" (relay disabled). Otherwise
// loads identity, calls relay.Connect, and spawns one goroutine that:
//   - drains conn.Frames() (this slice; dispatcher consumes in next slice)
//   - blocks on conn.Wait()
//   - on ErrServerIDConflict: logs the conflict and calls shutdown() to
//     unwind the daemon
//   - on any other terminal error: logs at warn (transport-internal
//     reconnect already handled non-fatal closes; reaching this path
//     means a non-classified terminal error)
//   - on ctx.Err: logs at debug; returns
//
// Returns the relay.Connection (or nil if disabled) so runSupervisor can
// Close() it during teardown. The returned cleanup func is idempotent.
func startRelay(
    ctx context.Context,
    logger *slog.Logger,
    instanceName, relayURL, version string,
    allowInsecure bool,
    shutdown context.CancelFunc,
) (cleanup func(), err error)
```

`startRelay` does NOT swallow `relay.Connect`'s synchronous errors (invalid scheme, missing identity). Those are programmer/config errors that should surface as a daemon startup failure — return wrapped, let `runSupervisor` fail fast. Lifecycle errors (post-Connect) flow through the goroutine.

The cleanup func calls `conn.Close()` then `<-doneCh` so teardown waits for the goroutine to drain.

#### `cmd/pyry/main.go` — wiring (~25 LOC changed)

In `runSupervisor`'s flag set:

```go
relayFlag := fs.String("pyry-relay", "", "relay URL override (default: $PYRY_RELAY_URL or ~/.pyry/config.json)")
```

Add `"pyry-relay"` to `pyryFlagValues` so `splitArgs` recognises it.

After identity load and after `signal.NotifyContext`/`cancel` exist but before `pool.Run`:

```go
cfg, err := config.Load(resolveConfigPath())
if err != nil {
    return fmt.Errorf("loading config: %w", err)
}
relayURL := resolveRelayURL(*relayFlag, os.Getenv("PYRY_RELAY_URL"), cfg)
allowInsecure := os.Getenv("PYRY_ALLOW_INSECURE_RELAY") == "1"
relayCleanup, err := startRelay(ctx, logger, *name, relayURL, Version, allowInsecure, cancel)
if err != nil {
    return fmt.Errorf("relay start: %w", err)
}
defer relayCleanup()
```

`startRelay` reuses `resolveServerIDPath(*name)` (already in `cmd/pyry/pair.go`) for `identity.LoadOrCreate`. No new resolver — same on-disk file as `pyry pair`.

Help text in `printHelp()` gains one line under the pyry-flags block:

```
  -pyry-relay string    relay URL (default: $PYRY_RELAY_URL or ~/.pyry/config.json)
```

### Concurrency model

One supervisor-owned goroutine per daemon lifetime. Lifecycle:

```
runSupervisor                 startRelay                  Connection.run
─────────────                 ──────────                  ──────────────
relay = Connect(ctx, cfg) ──► spawn drain+wait goroutine ──► transport.Connect (loop)
pool.Run(ctx) (blocks)        drain Frames() until close
                              <- conn.Wait()
                                 case ErrServerIDConflict:
                                   shutdown()  ──► cancel() ──► pool.Run unwinds
                                 case ctx.Err / other:
                                   log; return
relayCleanup()
  conn.Close()
  <-goroutineDone
```

Two channels are drained in the goroutine:

1. `conn.Frames()` in a `for range` loop — discards every envelope. `Connection.run` closes the channel when its lifecycle terminates, so the range loop exits cleanly.
2. `conn.Wait()` for terminal classification — runs in a sibling sub-goroutine OR after Frames closes. The simpler shape: range over Frames first, then call Wait — the channel close is the natural signal that Wait is about to return.

The shutdown decision happens in this goroutine, not in `Connection`. The daemon owns the policy ("4409 → shut down"); `Connection` only owns the classification.

### Error handling and recovery strategy

| Source                                | Classification by `Connection`        | Goroutine action               | Daemon impact                      |
|---------------------------------------|---------------------------------------|--------------------------------|------------------------------------|
| Transient network drop                | Handled inside `transport.Connect`    | Never observed                 | None (transport reconnects)        |
| Non-fatal WS close (e.g. 1011)        | Handled inside `transport.Connect`    | Never observed                 | None (transport reconnects)        |
| WS close 4409                         | `ErrServerIDConflict` from `Wait`     | Log conflict; call `shutdown`  | Daemon exits cleanly via ctx-cancel|
| `ctx.Err()` (SIGTERM/SIGINT)          | `ctx.Err()` from `Wait`               | Log debug; return              | None (was already shutting down)   |
| Other terminal transport error        | Wrapped `transport.ErrFatalClose` etc.| Log warn; return               | None (claude child stays running)  |

The "other terminal transport error" path satisfies the AC's "exit vs reconnect — architect's call": **the goroutine exits without restarting**. Rationale: the transport already retries every transient failure; a terminal classification means something genuinely unrecoverable surfaced (e.g. a future fatal close code added to `FatalCloseCodes`). Restart loops at this layer would mask the underlying fault. The supervisor's claude child stays running because the relay goroutine's exit doesn't propagate to `pool.Run` — only the explicit `shutdown()` call (4409 path) does.

The `relay.Config.RelayURL == ""` case is handled at the cmd/pyry layer: `startRelay` returns immediately with a no-op cleanup. This is the empty-precedence-chain branch (operator deliberately blanked all three sources). It must remain non-fatal — the daemon must boot without a relay configured for local-only `claude` supervision. Logged at info.

### `internal/e2e/internal/fakerelay` — handshake + fatal-close support (~50 LOC)

Two additions to `fakerelay.go`. Both are additive to existing tests (existing assertions hold).

**(a) Hello/hello_ack dispatch on `/v1/server`.** `binaryRecvPump` distinguishes a routing envelope (has `conn_id`) from a binary-direct envelope (top-level `type` field, no `conn_id`). When the latter arrives with `type == "hello"`:

- Capture the inner envelope under `s.mu` for test introspection (new accessor `LastBinaryHello(serverID) (protocol.Envelope, bool)`).
- Synthesize a hello_ack as `RoutingEnvelope{ConnID: "-", Frame: <Envelope{Type: "hello_ack", InReplyTo: <hello.id>, Payload: HelloAckPayload{ProtocolVersion:"v1", ServerID:<id>, ConnID:"-"}}>}`.
- Send it back on the binary's send pump.

The detection rule: parse incoming JSON twice — first as `RoutingEnvelope`; if `env.ConnID == ""` AND the same bytes parse as `protocol.Envelope` with a known top-level type, dispatch as binary-direct. This avoids touching the existing routing-envelope path and keeps backward compatibility with `TestPhoneToBinary_FrameWrappedWithConnID` (the phone→binary frame still flows through the routing path because its `conn_id` is set by `phoneRecvPump` before reaching `binaryRecvPump`).

Defer non-`hello` binary-direct envelopes to a future slice (log at debug, drop). The dispatcher slice will replace this stub.

**(b) Server-id conflict via WS close 4409 (opt-in).** Add `Server.RejectNextBinaryWith4409()`. On the next `/v1/server` upgrade:

- Accept the WS upgrade (don't pre-upgrade-fail).
- Immediately close with `websocket.StatusCode(4409)`, reason `"server-id already claimed"`.
- The flag clears after one use — subsequent connects follow normal logic.

The existing first-claim-wins path stays HTTP 409 for the unit tests in `fakerelay_test.go` (no churn). The new method adds a second mode for the e2e test that needs a true WS 4409.

Implementation sketch (placement in `handleBinary`, before the duplicate-claim branch on line 166):

```go
s.mu.Lock()
fail4409 := s.rejectNextBinaryWith4409
if fail4409 {
    s.rejectNextBinaryWith4409 = false
}
s.mu.Unlock()
if fail4409 {
    conn, err := websocket.Accept(w, r, nil)
    if err != nil { return }
    _ = conn.Close(websocket.StatusCode(4409), "server-id already claimed")
    return
}
```

`Server.RejectNextBinaryWith4409()` is a public test method (lowercase package, no API contract beyond the e2e tree).

### Testing strategy

#### `internal/relay` (unit, no -tags=e2e)

Augment `TestConfig_Validation_TableDriven` (`connection_test.go:665`):

- Add `{"ws scheme accepted with AllowInsecureScheme", func(c *Config) { c.RelayURL = "ws://example.invalid/"; c.AllowInsecureScheme = true }, ""}` — expects no `ErrInvalidConfig`. (The case loop's `errors.Is` check inverts: assert `err == nil` OR factor a separate test.)

A standalone happy-path test is overkill — the field-level behaviour is one branch.

#### `cmd/pyry` (unit, no -tags=e2e)

`cmd/pyry/relay_test.go` table tests for `resolveRelayURL`:

- flag wins over env wins over cfg
- env wins over cfg when flag empty
- cfg.RelayURL when flag and env empty
- empty when all three empty (config.Load defaulting is exercised by config's own tests, not duplicated here)

No tests for `startRelay` itself — it's a thin wiring layer; coverage is via the e2e test below. Unit-testing `startRelay` would require either heavy mocking of `relay.Connect` (which has no interface) or an in-process httptest server, both of which the e2e covers more meaningfully.

#### `internal/e2e/internal/fakerelay` (unit, existing build tags)

Two new tests in `fakerelay_test.go`:

- `TestBinaryHello_GetsHelloAck` — dial as binary, send `{"id":1,"type":"hello","payload":{...HelloServerPayload}}`, expect a routed `hello_ack` back with `conn_id == "-"` and `in_reply_to == 1`.
- `TestRejectNextBinaryWith4409` — set the flag, dial as binary, expect WS close with code 4409.

Existing tests stay green (the new branches don't disturb the routing-envelope path or the HTTP-409 first-claim path).

#### `internal/e2e` (build tag `e2e`)

New file `internal/e2e/relay_test.go` with two test functions:

- **`TestRelayHandshake_HappyPath`**:
  1. Spin up `fakerelay.New(testLogger)`; defer Close.
  2. `e2e.StartIn(t, home, extraFlags=["-pyry-relay=" + fr.URL() + "/v1/server"])`. Inject env `PYRY_ALLOW_INSECURE_RELAY=1` via a new `extraEnv` parameter on `StartIn` OR by adding a thin `StartInWithEnv` (architect prefers extending `StartIn`; see below).
  3. Wait for fakerelay to observe the binary connect (poll `fr.LastBinaryHello(serverID)`).
  4. Assert `payload.Role == "server"`, `payload.ServerID` matches the daemon's persisted server-id (read from `<home>/.pyry/test/server-id`).
  5. Assert daemon logs include "relay: handshake complete" (read `h.Stderr.String()`; the relay package logs this at info per `connection.go:263`).
  6. `h.Stop(t)` cleanup.

- **`TestRelayConflict_4409_ShutsDownDaemon`**:
  1. Spin up fakerelay; call `fr.RejectNextBinaryWith4409()` BEFORE starting pyry.
  2. Spawn pyry with the relay URL + insecure-scheme env.
  3. Wait up to 5s for `h.doneCh` to close (daemon process exit).
  4. Assert exit code 0 (clean shutdown) and stderr contains the conflict log line ("server-id conflict" or "close 4409" — pin via `relay.ErrServerIDConflict.Error()` substring).

A third test for "non-fatal close, claude stays running" is desirable per the AC's "must be covered by an explicit test" clause:

- **`TestRelay_NonFatalClose_DaemonStaysUp`**:
  1. Spin up fakerelay.
  2. Spawn pyry pointing at it; wait for first binary connect (via `fr.LastBinaryHello`).
  3. Force-close that conn from the relay side with `websocket.StatusInternalError` (1011).
  4. Wait for fakerelay to observe a second binary connect (transport reconnect).
  5. Assert daemon process is still alive (`h.PID` is reachable; `os.FindProcess` + `Signal(0)` or check `h.doneCh` not closed).
  6. Add a public method `fr.ForceCloseBinary(serverID)` to support step 3.

To inject `PYRY_ALLOW_INSECURE_RELAY=1`, extend `e2e.StartIn` with a variadic `WithEnv` option OR add a sibling `StartInWithEnv(t, home, extraEnv, extraFlags...)`. Architect prefers a small additive helper that takes `spawnOpts`-shaped vars to avoid disturbing every existing `StartIn` call site:

```go
// StartInWithEnv behaves like StartIn but also appends extraEnv (each "K=V")
// to the child's environment. extraFlags semantics are unchanged.
func StartInWithEnv(t *testing.T, home string, extraEnv []string, extraFlags ...string) *Harness
```

Internally just calls `spawnWith(t, home, spawnOpts{extraEnv: extraEnv, extraFlags: extraFlags})`, then mirrors the `StartIn` Harness construction + readiness wait.

### Acceptance Criterion → test mapping

| AC                                                                       | Covered by                                                               |
|--------------------------------------------------------------------------|--------------------------------------------------------------------------|
| `-pyry-relay` > `PYRY_RELAY_URL` > `cfg.RelayURL` > `DefaultConfig`      | `cmd/pyry/relay_test.go::TestResolveRelayURL_Precedence`                 |
| `relay.Config` test seam for ws://                                       | `internal/relay/connection_test.go::TestConfig_Validation_TableDriven` (new row) |
| 4409 → daemon logs conflict, exits cleanly                               | `internal/e2e/relay_test.go::TestRelayConflict_4409_ShutsDownDaemon`     |
| Non-fatal close → claude child stays running (architect's choice + test) | `internal/e2e/relay_test.go::TestRelay_NonFatalClose_DaemonStaysUp`      |
| e2e: hello observed, hello_ack observed, 4409 shuts daemon down          | All three `relay_test.go` tests                                          |

## Security review (security-sensitive)

The `security-sensitive` label gates this section; the spec is not committable until it passes.

**Trust boundaries.** The relay URL crosses a trust boundary: it is operator-supplied (flag/env/file). The `Connect` validator (`internal/relay/connection.go:109-116`) parses via `url.Parse` and rejects schemes other than `wss`. The `AllowInsecureScheme` field weakens that to also accept `ws`, but only when the field is true. The cmd/pyry-side gate is `PYRY_ALLOW_INSECURE_RELAY=1`. **Risk:** a future change wires this field from config-file or flag input — operators could downgrade to plaintext WSS unintentionally.

**Mitigation:** the field stays env-gated (no `-pyry-allow-insecure-relay` flag, no `allow_insecure_relay` config-file key in this slice). The env var name explicitly says "insecure". Logged at info on startup when active so the operator-visible boot log records the downgrade.

**Identity.** The server-id is read via `identity.LoadOrCreate` against `~/.pyry/<name>/server-id`. That loader's contract: never overwrites an existing file, validates as canonical UUIDv4 (per `internal/identity/server_id.go` and the lessons from #56's pairing-bind decision). No new attack surface added — same file, same loader, same call shape as `cmd/pyry/pair.go:166`.

**Secret handling.** Headers `x-pyrycode-server`, `x-pyrycode-version`, `user-agent` are all non-secret. ServerID is a UUIDv4 — not a credential. No tokens, hashes, or keys flow through this slice. (Tokens are phone-side; the per-conn dispatcher slice will handle them.)

**Failure modes.**

- **4409 reconnect-storm DoS.** If the relay goroutine looped on `ErrServerIDConflict`, two pyry instances racing for the same server-id would generate sustained relay load. Mitigated by AC#3 design: 4409 is terminal — goroutine triggers daemon shutdown rather than reconnecting. The transport's existing `FatalCloseCodes` machinery (per `internal/transport/wssclient.go:181-220`) already prevents the lower-layer reconnect on 4409, so even a buggy goroutine can't spin.
- **Non-fatal close storm.** Transport's exponential backoff (1s base, ±20% jitter, capped — confirmed in `internal/transport/wssclient.go`) bounds the reconnect rate. No new reconnect logic added at the daemon layer. Out of scope: a hostile relay could still drain backoff state by repeatedly accepting+closing; that's a relay-side concern.
- **Resource leak on daemon shutdown.** `relayCleanup()` is `defer`red AFTER `pool.Run` returns. The cleanup func calls `conn.Close()` which closes both `c.closed` and the underlying transport client; the goroutine drains and exits. The cleanup blocks on `<-goroutineDone` so the daemon process doesn't exit while a goroutine still holds a WS conn. Verified by `TestRelayConflict_4409_ShutsDownDaemon`'s clean-exit assertion.
- **Logging leakage.** The relay URL is logged at info on startup (operator diagnostics). Server-id is logged at info on handshake-complete (already done at `internal/relay/connection.go:264`). Neither is sensitive. The relay error path's `Wait()` return value is logged at warn — confirm it does not contain operator-supplied URL bytes that an adversary could weaponise. `transport.ErrFatalClose` wraps `coder/websocket`-supplied close-frame text, not URL fragments. **No log filtering needed.**
- **Goroutine leak on `Connect` synchronous failure.** If `Connect` returns an error (invalid scheme, missing identity), no goroutine is spawned. `startRelay` returns the error; `runSupervisor` aborts before reaching `pool.Run`. No lingering state.

**Verdict: PASS.** The change preserves the production wss-only invariant via the env-gated test seam, doesn't expand secret surface, doesn't introduce new reconnect-storm risks, and shutdown is clean.

## Open questions

- **`Wait()`-return ordering vs Frames close.** `Connection.run` (`connection.go:184-185`) defers `close(c.done)` then `close(c.frames)` — which means `Wait()` may return before `Frames()` channel is closed, with the goroutine racing to drain. The implementation should call `Wait` AFTER the Frames range loop exits, not in parallel, to avoid a stale receive. Confirm by reading the deferred close order; if the order is wrong, file a follow-up. (Not blocking this slice — single-goroutine drain order is the safe shape regardless.)
- **`-pyry-relay=""` vs flag-unset.** Operators may want to explicitly disable the relay even when config.json sets one. Today an empty flag falls through to the env, then config. To support "explicit-empty disables", either change the flag default to a sentinel string or add `-pyry-no-relay`. Defer to a follow-up unless an operator hits this; current design is "configured everywhere → connect, configured nowhere → don't".
