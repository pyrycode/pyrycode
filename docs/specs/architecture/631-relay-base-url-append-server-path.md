# Spec: daemon appends `/v1/server` to a base `relay_url` (or fails loudly) — #631

## Files to read first

- `internal/relay/connection.go:100-143` — `Connect`. The inline `url.Parse` +
  scheme-check block (lines 113-120) and the `tcfg.URL = cfg.RelayURL`
  assignment (line 128) are exactly what Option A changes. **Extract:** the
  current parse/validate flow so the new `resolveDialURL` helper preserves the
  same `ErrInvalidConfig` wrapping and ordering.
- `internal/relay/connection_test.go:1-32` — test infra (`testLogger`,
  `testServerID`); `:506-540` `TestConfig_Validation_TableDriven` (the scheme /
  parse error matrix that must stay green after the refactor); `:611-625`
  `TestConfig_AllowInsecureScheme` (the `Connect` + immediate-`Close` pattern, if
  a Connect-level smoke is wanted). **Extract:** assertion idioms + the error
  strings the existing table pins (`"wss"`, `"RelayURL parse"`).
- `internal/transport/wssclient.go:120-141` — sentinel-error block (add
  `ErrUpgradeRejected` here); `:181-255` — `Connect` dial loop, specifically the
  INFO `"transport: dial failed, backing off"` line (213) to branch; `:364-372`
  — `realDial` (capture the `*http.Response`). **Extract:** the dial-fail branch
  shape and the `dialFn` test seam (`c.dialFn = c.realDial`, line 165).
- `internal/e2e/internal/fakephone/fakephone.go:63-78` — `Dial` does
  `baseURL+"/v1/client"`. **This is the convention Option A mirrors** on the
  daemon side (`/v1/server`). The daemon should treat its configured URL as a
  base exactly as the phone does.
- `internal/e2e/internal/fakerelay/fakerelay.go:1-36` — `/v1/server` route +
  "rejections happen pre-upgrade as HTTP 400/409/503". **Extract:** the route
  the appended path must hit; the HTTP-status-on-dial-error behaviour.
- `internal/e2e/relay_test.go:90-124` (`TestRelay_1011`) — the canonical
  "daemon connected" e2e assertion: `StartInWithEnv` + `readPersistedServerID` +
  `fr.WaitBinary(ctx, serverID)`. **Extract:** the exact helper set the new
  base-URL e2e reuses.
- `internal/config/config.go:22-26` — `DefaultConfig().RelayURL =
  "wss://relay.pyrycode.dev"` is a base URL with no path: the shipped default is
  the footgun this ticket fixes.
- `docs/protocol-mobile.md` § Endpoints — `/v1/server`, `/v1/client` are the
  canonical, unchanged paths for both v1 and v2.
- `github.com/coder/websocket@v1.8.13/dial.go:144-168` (module cache) — confirms
  the Option-B signal: a **non-101 HTTP response** returns `(nil, resp, err)`
  with `resp != nil`; a **network failure** returns `(nil, nil, err)` with
  `resp == nil`. No string-matching needed.

## Context

The daemon dials its configured `relay_url` **verbatim**:
`relay.Connect` sets `transport.Config{URL: cfg.RelayURL}` with no path append.
The live relay serves only `/v1/server`, `/v1/client`, `/healthz`. So a **base**
`relay_url` (e.g. `wss://relay.pyrycode.dev` — the shipped default) makes the
daemon dial relay-root `/`, which 404s the WS upgrade and spins forever in
INFO-level backoff (`"transport: dial failed, backing off" attempt=39 …`).

The URL is **asymmetric**: `pyry pair` (`cmd/pyry/pair.go`) puts the base URL in
the QR and the phone appends `/v1/client` itself, so a base config is correct
there. The daemon dials verbatim, so it needs `/v1/server` baked in. The natural
single value (the base) silently breaks the daemon, and the e2e harness hid the
gap by always passing `fr.URL()+"/v1/server"` explicitly.

This spec implements **Option A + Option B** (the ticket's recommended
combination — "append the path **and** keep the clear error if the upgrade still
fails"):

- **A** removes the asymmetry: the daemon appends `/v1/server` to a base
  `relay_url`, mirroring the phone's `/v1/client`. Fixes the default out of the
  box; satisfies AC#2/AC#4 and the connect branch of AC#1.
- **B** makes any *remaining* non-101 upgrade (operator typed an explicit wrong
  path, relay misrouted) **fail loudly on the first failed upgrade** instead of
  burying it in silent backoff — the diagnostic branch of AC#1. This is the
  safety net for the cases A cannot prevent, and it restores the diagnostic
  experience that was the reported pain ("no hint that the path is wrong … it
  looks like a phone problem when it is a daemon-config problem").

The two changes respect the existing layering: **`relay` owns the `/v1/server`
knowledge** (Option A); **`transport` stays protocol-agnostic** (Option B's loud
log says "verify the relay URL path is correct" — no pyrycode-specific path).

## Design

Two production files. No exported-signature changes anywhere → no consumer
cascade (`relay.Connect`'s only production caller, `cmd/pyry/relay.go:122`,
passes `RelayURL` unchanged).

### Option A — `internal/relay/connection.go`

Replace `Connect`'s inline parse + scheme-validate block (lines 113-120) with a
single call to a new unexported pure helper that becomes the **one home for all
relay-URL handling** (parse → scheme → path-append):

```go
// resolveDialURL validates the relay URL's scheme and appends the binary's
// /v1/server endpoint when the URL carries no meaningful path, mirroring the
// phone's /v1/client convention. An operator-supplied path is preserved
// unchanged. Returns the dial URL, or a wrapped ErrInvalidConfig.
func resolveDialURL(raw string, allowInsecure bool) (string, error)
```

Contract (the only new logic — everything else is relocated from `Connect`):

- Parse failure → `fmt.Errorf("%w: RelayURL parse: %v", ErrInvalidConfig, err)`
  (byte-identical to today's message — keeps `TestConfig_Validation` green).
- Scheme not `wss` (and not `ws` under `allowInsecure`) →
  `fmt.Errorf("%w: RelayURL scheme must be wss (got %q)", …)` (unchanged).
- **Path append:** if `u.Path == "" || u.Path == "/"` → set
  `u.Path = "/v1/server"`; otherwise leave `u.Path` untouched (AC#4).
- Return `u.String()` (stdlib reconstruction preserves host, port, query,
  userinfo — e.g. `wss://h?x=1` → `wss://h/v1/server?x=1`).

`Connect` keeps its `cfg.RelayURL == ""` non-empty guard (line 107-109) so the
precise `"RelayURL is required"` message is preserved, then calls
`resolveDialURL(cfg.RelayURL, cfg.AllowInsecureScheme)` and assigns the result to
`tcfg.URL`. Net: ~8 lines move into the helper, +3 lines of new path logic.

| `relay_url` (config or `-pyry-relay`) | Dialed URL |
|---|---|
| `wss://relay.pyrycode.dev` | `wss://relay.pyrycode.dev/v1/server` |
| `wss://relay.pyrycode.dev/` | `wss://relay.pyrycode.dev/v1/server` |
| `wss://relay.pyrycode.dev/v1/server` | `wss://relay.pyrycode.dev/v1/server` (passthrough) |
| `wss://relay.pyrycode.dev/v2/server` | `wss://relay.pyrycode.dev/v2/server` (passthrough, AC#4) |
| `ws://127.0.0.1:54321` (e2e, insecure) | `ws://127.0.0.1:54321/v1/server` |

### Option B — `internal/transport/wssclient.go`

**New sentinel** in the existing `var (…)` block (lines 120-141):

```go
// ErrUpgradeRejected wraps a dial error where the relay returned a non-101
// HTTP response to the WebSocket upgrade (e.g. 404 on a wrong URL path), as
// distinct from a network-level dial failure (DNS, refused, TLS). Surfaced
// loudly by Connect; the HTTP status is in the wrapped error string.
var ErrUpgradeRejected = errors.New("transport: websocket upgrade rejected")
```

**`realDial` (lines 364-372)** captures the response and classifies the error
using the verified coder/websocket signal (`resp != nil` ⟺ server responded with
a non-101). Behaviour summary (≤6 lines of new code):

- `conn, resp, err := websocket.Dial(...)`.
- `err != nil && resp != nil` → return
  `fmt.Errorf("%w (HTTP %d): %w", ErrUpgradeRejected, resp.StatusCode, err)`.
- `err != nil && resp == nil` → return `fmt.Errorf("dial: %w", err)` (unchanged).
- success → unchanged (`SetReadLimit`, return conn).

**`Connect` dial-fail branch (around line 211-213)** logs loudly once per outage.
Declare `warnedUpgrade := false` before the `for` loop. In the dial-error branch:

- `errors.Is(err, ErrUpgradeRejected) && !warnedUpgrade` → set `warnedUpgrade =
  true` and `Logger.Warn("transport: relay rejected the WebSocket upgrade; "
  + "verify the relay URL path is correct", "attempt", attempt, "delay", delay,
  "err", err)`.
- otherwise → the existing `Logger.Info("transport: dial failed, backing off",
  …)` line (unchanged).
- **Re-arm:** reset `warnedUpgrade = false` on the successful-dial path (right
  where `"transport: connected"` is logged, ~line 225), so a *new* outage after
  a healthy connection gets its own first-failure WARN.

This satisfies AC#1's "fails loudly with an actionable diagnostic on the **first**
failed upgrade (not buried after dozens of silent retries)" while keeping the
supervisor's correct retry-forever posture (no daemon exit — see PROJECT-MEMORY
"Backoff cooldown/bail-out": retry-forever is the right default for a service
supervisor). Network failures (relay down) stay at INFO, which is correct — they
are transient, not a config error.

## Concurrency model

Unchanged. `resolveDialURL` is a pure synchronous helper called inside `Connect`
before `go c.run(ctx)` — no new goroutines, no shared state. `warnedUpgrade` is a
local variable in the single-goroutine `transport.Client.Connect` dial loop
(that loop is documented "run in its own goroutine"; the variable is never shared
across goroutines, so no mutex). The `dialFn` seam, backoff cadence, ping/pong,
and reconnect lifecycle are all untouched.

## Error handling

- **Config-time (synchronous, fail-fast):** bad scheme / unparseable URL still
  return wrapped `ErrInvalidConfig` from `Connect` (now via `resolveDialURL`);
  `cmd/pyry/relay.go startRelay` already surfaces these as a daemon-startup
  failure. Empty `RelayURL` still returns the precise `"RelayURL is required"`.
- **Dial-time non-101 (Option B):** classified as `ErrUpgradeRejected`, logged
  WARN once per outage with an actionable hint, then absorbed by the normal
  backoff loop (retry continues — the relay or config may be fixed live and the
  next dial succeeds, re-arming the warning).
- **Dial-time network failure:** unchanged — INFO backoff, retry forever.
- **No new terminal/fatal paths.** `ErrUpgradeRejected` is *not* added to
  `FatalCloseCodes` and does not unwind the daemon; it is a log-classification
  signal only. The only fatal close remains 4409 (server-id conflict).

## Testing strategy

Test-first (RED → GREEN, AC#5). `make check` (`vet test staticcheck
substrate-guard`) must be green; the substrate-guard is unaffected (no claude
screen-byte surface is touched).

**`internal/relay/connection_test.go`** — `TestResolveDialURL` (table, same-package):
- `""` path / no-path base → `…/v1/server`; `"/"` → `…/v1/server`.
- `/v1/server` → passthrough; `/v2/server` → passthrough; `/custom` → passthrough (AC#4).
- query preserved: `wss://h/?x=1` → `wss://h/v1/server?x=1`; `wss://h/v1/server?x=1` → unchanged.
- scheme rejection: `ws://…` with `allowInsecure=false` → wraps `ErrInvalidConfig`, message contains `"wss"`; with `allowInsecure=true` → accepted.
- unparseable (`"://broken"`) → wraps `ErrInvalidConfig`, message contains `"RelayURL parse"`.
- Existing `TestConfig_Validation_TableDriven` / `TestConfig_AllowInsecureScheme` continue to pass unchanged (they assert through `Connect`).

**`internal/transport/wssclient_test.go`** (same-package; reuse the existing
test-relay / `dialFn` / short-cadence helpers):
- `TestRealDial_UpgradeRejected` — point `realDial` at an `httptest` handler that
  returns `404` (never upgrades): assert the error `errors.Is` `ErrUpgradeRejected`
  and contains the status. A dial to a closed/unreachable address (`resp == nil`)
  → plain `dial:` error, **not** `ErrUpgradeRejected`. (Proves the
  coder/websocket signal classification.)
- `TestConnect_LoudOnFirstUpgradeReject` — substitute `dialFn` to return an
  `ErrUpgradeRejected`-wrapped error; run `Connect` in a goroutine with a short
  ctx and a **recording `slog.Handler`**; assert exactly **one WARN** record
  carrying the actionable phrase on the first attempt and INFO on subsequent
  attempts. (Proves AC#1's "loud on first failed upgrade, not buried".)

**`internal/e2e/relay_base_url_test.go`** (new; `//go:build e2e`) — closes the
gap the harness hid (AC#3):
- `fr := fakerelay.New(...)`; boot the daemon via `StartInWithEnv(t, home,
  []string{"PYRY_ALLOW_INSECURE_RELAY=1"}, "-pyry-relay="+fr.URL())` — **bare
  base URL, no `/v1/server`**.
- `serverID := readPersistedServerID(t, home)`; assert `fr.WaitBinary(ctx,
  serverID)` succeeds within a few seconds → the daemon connected through the
  appended `/v1/server` path end-to-end (`relay.Connect` → `resolveDialURL` →
  transport → fakerelay `/v1/server` route).
- Mirror `TestRelay_1011`'s harness/cleanup idioms.
- One-line doc tweak: `harness.go:304-308` `StartRotationWithRelay`'s comment
  says "relayURL is the /v1/server endpoint" — only update it if you route the
  new test through that helper; routing through `StartInWithEnv` (as above)
  leaves it untouched.

## AC coverage map

| AC | How satisfied |
|---|---|
| #1 no silent loop; connect or loud-on-first-upgrade | A → default base connects; B → WARN on first non-101. `TestConnect_LoudOnFirstUpgradeReject` + e2e connect. |
| #2 single base URL serves both `pyry pair` + daemon, no env override | A: `pyry pair` already uses the base; daemon now appends `/v1/server`. e2e proves the daemon side. |
| #3 explicit test of the base-URL config path | `TestResolveDialURL` (unit) + `relay_base_url_test.go` (e2e, bare `fr.URL()`). |
| #4 operator path passed through unchanged | `resolveDialURL` appends only when path is `""`/`"/"`. `TestResolveDialURL` passthrough rows. |
| #5 `make check` green (incl. substrate-guard), test-first | No screen-byte surface touched; tests written RED first. |

## Open questions

- **Log level for repeat upgrade-rejections.** Spec demotes post-first
  rejections to the existing INFO backoff line and re-arms the WARN after a
  successful connect. If operators would rather see a WARN every N attempts
  during a sustained outage, that's a trivial follow-up — left out here to avoid
  log spam (backoff caps at 30s, so even WARN-every-time is bounded, but
  once-per-outage matches the AC wording "on the **first** failed upgrade").
- **`/v1/server` as a constant.** The path is currently a string literal in
  `resolveDialURL`; the phone-side literal lives in `fakephone`. Not worth a
  shared constant for one daemon-side occurrence (the production phone is a
  separate repo). Inline literal is fine.
