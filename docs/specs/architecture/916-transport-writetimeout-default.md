# Spec: default `transport.Config.WriteTimeout` when unset (#916)

**Size:** XS (confirmed — 1 production file, 1 test file, purely additive, no signature change, no consumer cascade).

## Files to read first

- `internal/transport/wssclient.go:28-63` — the `const` block and `Config` struct. The new default constant lives here; `Config.WriteTimeout`'s doc comment (currently line 46-47, on the struct-level comment) gets a field-level doc.
- `internal/transport/wssclient.go:151-175` — `New`. The defaulting site: mirror the "mutate the local `cfg` before the struct literal" posture used by sibling constructors. The Logger nil-panic (line 153-155) is the anchor to insert after.
- `internal/transport/wssclient.go:514-528` — `sendPump`. The `context.WithTimeout(ctx, c.cfg.WriteTimeout)` at line 520 is where the zero value bites. **Read-only** — the fix does NOT go here (see Design § "Why default in `New`").
- `internal/transport/wssclient_test.go:414-489` — `TestSmoke_HttptestEchoServer`. The exact template for the end-to-end send test (poll-send until live, then echo-receive). The new test copies this but **omits** `WriteTimeout` from the `Config`.
- `internal/transport/wssclient_test.go:29-69` — `newClientForTest` / `testOpts`. The construction helper the new test reuses; note it does NOT touch `WriteTimeout`, so a `cfg` with the field unset flows straight through `New` and picks up the default.
- `internal/relay/connection.go:127-133` — the sole production caller, sets `WriteTimeout: 10 * time.Second`. Confirms the default value to match. **Do NOT modify this file** (see Open questions).
- Convention reference (no need to open, but this is the established pattern): `internal/dispatch/dispatch.go:283` (`if cfg.OutboundBuffer <= 0 { cfg.OutboundBuffer = 32 }`) and `internal/agentrun/budget/budget.go:87` (`if cfg.GracePeriod == 0 { cfg.GracePeriod = defaultGracePeriod }`) both default a config knob by mutating the local `cfg` inside the constructor. This spec follows that shape.

## Context

`sendPump` wraps every write in `context.WithTimeout(ctx, c.cfg.WriteTimeout)` (wssclient.go:520). `WithTimeout(ctx, 0)` returns an already-expired context, so when `Config.WriteTimeout` is the zero value every `conn.Write` fails immediately with deadline-exceeded → `sendPump` returns → `serve` tears the conn down → the client reconnect-loops. Logs read `connected` then instantly `disconnected`, pointing at the network rather than the config — expensive to misdiagnose.

`New` already defaults the ping/pong/backoff cadence knobs (from package constants) and panics on a nil `Logger`, but leaves `WriteTimeout` untouched. This ticket brings `WriteTimeout` under the same defaulting posture so a zero-value config is safe. The only production caller already passes `10s`, so this changes no shipped behaviour — it protects a new consumer or a test that omits the field.

## Design

Three edits, all in `internal/transport/wssclient.go`.

### 1. New package constant

Add a `defaultWriteTimeout = 10 * time.Second` constant. It matches the production caller (`relay/connection.go:130`).

**Placement note:** do NOT fold it under the existing `// Wire-spec constants` comment (line 28) — `WriteTimeout` is a local send-I/O bound, not a protocol-doc-derived cadence value (the `Config.WriteTimeout` doc already states "it is NOT an inactivity timeout"). Give it its own short `const` declaration (or a clearly-commented line) noting it is the default applied by `New` when the field is unset, kept in sync with the production caller's value.

### 2. Default in `New`

Insert, immediately after the existing Logger nil-panic (wssclient.go:153-155) and before the `c := &Client{...}` literal:

```go
if cfg.WriteTimeout <= 0 {
    cfg.WriteTimeout = defaultWriteTimeout
}
```

- Mutates the local `cfg` before it is copied into `c.cfg` (the struct literal already does `cfg: cfg`), so the constructed `Client` observes the defaulted value — satisfies "observable on the constructed `Client`".
- `<= 0` (not `== 0`) so a negative duration is also treated as unset. AC2's "non-positive" framing.
- A caller-supplied positive value skips the branch untouched → preserved.

### Why default in `New` (not at the `sendPump` call site)

Per the Technical Notes and the package's own posture: defaulting once in the constructor means the value is fixed and observable on the `Client`, and every reader (`sendPump` today, any future reader) sees a sane value without repeating the guard. Defaulting inside `sendPump` would re-evaluate per frame and leave `c.cfg.WriteTimeout` reading `0` to any inspector. **Do not touch `sendPump`.**

### 3. Document the field

Promote `WriteTimeout` to a field-level doc comment on the `Config` struct stating: it bounds per-frame send I/O (not an inactivity timeout — that is ping/pong); and **when left unset (non-positive), `New` substitutes a 10s default.** Keep the existing struct-level sentence about it not being an inactivity timeout, or migrate it to the field doc — either is fine, don't duplicate.

## Concurrency model

Unchanged. This is a construction-time value substitution; no goroutines, channels, or locks are added or altered. `c.cfg` is written once in `New` and read-only thereafter (already the case).

## Error handling

No new failure modes. The change removes a failure mode (instant deadline-exceeded on every send under a zero-value config). `New` does not gain an error return — it stays a `*Client`-only constructor, defaulting silently like its sibling knobs.

## Testing strategy

White-box (`package transport`), so tests may read `c.cfg.WriteTimeout` directly.

- **Unit — default applied / positive preserved (AC1, AC2).** Table-driven over `New`'s handling of `WriteTimeout`:
  - unset (`0`) → constructed `Client` has `cfg.WriteTimeout == defaultWriteTimeout` (10s).
  - negative (e.g. `-1`) → defaulted to 10s (the `<= 0` branch).
  - positive (e.g. `3 * time.Second`) → preserved exactly.
  Construct with `New(Config{Logger: testLogger(t), WriteTimeout: <case>})` and assert on `c.cfg.WriteTimeout`. Deterministic, no network.
- **End-to-end — send succeeds with `WriteTimeout` unset (AC3).** Model on `TestSmoke_HttptestEchoServer` (wssclient_test.go:414). Differences:
  - Build `Config` **without** `WriteTimeout` (leave it zero) — this is the whole point.
  - Stand up `newTestRelay` (echoes frames), construct via `newClientForTest` with the short cadence `testOpts`, `Connect` in a goroutine, poll-`Send` a frame until it stops returning `ErrNotConnected`, then `Receive` and assert the echoed bytes come back.
  - **Non-vacuity:** under the bug, the zero `WriteTimeout` makes `sendPump`'s first `Write` fail instantly → conn drops → no echo ever returns → `Receive` would return `ErrDisconnected`/time out. A successful round-trip is therefore a genuine regression catch; assert the received frame equals what was sent.
- **Field doc (AC4)** is verified by inspection, no test.

Run: `go test -race ./internal/transport/`.

## Open questions / non-goals

- **Do not refactor `relay/connection.go` to drop its explicit `10 * time.Second`.** The two 10s values are intentional and independent: the package default is a safety net for unset configs; the production caller stays explicit and self-documenting. Removing the explicit set is out of scope (Simplicity First — touch only what's necessary) and would couple the caller to the package default's exact value. Leave line 130 as-is.
- Keeping `defaultWriteTimeout` numerically in sync with the caller's `10s` is a doc-comment note, not enforced in code — matching the existing posture where the wire-spec constants are kept in sync with `docs/protocol-mobile.md` by comment, not by a shared symbol. No enforcement is warranted (no observed drift).
