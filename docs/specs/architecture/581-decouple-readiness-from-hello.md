# Spec #581 — Decouple e2e test readiness from the binary↔relay hello (migrate `LastBinaryHello` → `WaitBinary`)

**Ticket:** #581 (split from #569) · **Size:** S · **Type:** test-only mechanical refactor · **Security-sensitive:** no (no `security-sensitive` label; no production code, no wire change)

## Files to read first

The developer needs all nine e2e files plus the fakerelay accessor. Read these first; every edit point is one contiguous block inside one of them.

- `internal/e2e/internal/fakerelay/fakerelay.go:602-642` — **the contract.** `LastBinaryHello(serverID) (Envelope, bool)` (hello-recording, stays) vs. `WaitBinary(ctx, serverID) bool`. Note: `WaitBinary` has **no internal deadline** — it blocks until the binary registers *or* `ctx` is done. The caller owns the timeout. `context.Background()` would block forever on a never-registering binary; you must pass a `context.WithTimeout`.
- `internal/e2e/internal/fakerelay/fakerelay_test.go:478-532` — the canonical `WaitBinary` call idiom (`if !s.WaitBinary(ctx, …) { t.Fatal(…) }`) and the `TestWaitBinary` happy/timeout coverage. **Leave this file unchanged** (AC); it's your idiom reference and the helper's regression guard.
- `internal/e2e/relay_test.go:60-92` — `TestRelay_Hello`. The `LastBinaryHello` read at **line 72 is EXCLUDED** (payload assertion: `role=server` / `server_id` — the *subject* of the test, a bool can't replace it). Leave it.
- `internal/e2e/relay_test.go:132-159` — `TestRelay_1011`. The migrating site (lines 145-154). **`relay_test.go` does NOT import `context`** — this site needs the import added (see § Per-site notes).
- `internal/e2e/relay_v2_daemon_test.go:75-87,127,222` — the `waitBinaryHello` helper (2 callers). Migrate the helper body once.
- `internal/e2e/relay_v2_handshake_test.go:55-99` — `startV2Harness`. The readiness block (85-94) is inside the harness, so one edit covers every v2-handshake subtest.
- `internal/e2e/respawn_after_eviction_test.go:95-137` — has **two** poll loops; only the `LastBinaryHello` readiness one migrates (see § Per-site notes).
- `internal/e2e/register_push_token_test.go:48-61`, `relay_assistant_turn_test.go:75-86`, `relay_auth_test.go:36-47`, `relay_roundtrip_test.go:71-83`, `relay_send_message_test.go:73-84` — the five uniform inline sites.

## Context

The e2e suite proves "the binary's outbound relay connection is up" by polling `fr.LastBinaryHello(serverID)` — a fakerelay map populated only when the binary *sends* a relay-leg `hello` envelope. That couples the suite's readiness gates to an application-level handshake. #582 will retire that handshake; if the suite still keys readiness off the hello, retiring it cascades `t.Fatal`s across nine files.

`fr.WaitBinary(ctx, serverID)` already detects the same condition independently of the hello: it returns true once the binary's WebSocket upgrade has registered its server-id (read from the `x-pyrycode-server` request header), inserted into `s.binaries`. Migrating every readiness gate from `LastBinaryHello` polling to `WaitBinary` removes the coupling. **Nothing on the wire changes** — the binary still sends the hello, the fakerelay still records it and synthesizes `hello_ack`; only the *test's readiness probe* moves off the hello.

This is the consumer-migration slice of #569's Strangler Fig: **#581 migrates readers → #582 retires the handshake → #583 removes the now-dead `LastBinaryHello` accessor.**

## Design

### The migration idiom

Each readiness gate today is a contiguous block: a `deadline`, a poll loop that breaks on a positive `LastBinaryHello`, and a companion negative pre-check that `t.Fatal`s on timeout. Replace the whole block with a single `WaitBinary` call carrying its own timeout context.

**Before** (representative — `register_push_token_test.go:50-60`):

```go
deadline := time.Now().Add(5 * time.Second)
for time.Now().Before(deadline) {
    if _, ok := fr.LastBinaryHello(serverID); ok {
        break
    }
    time.Sleep(20 * time.Millisecond)
}
if _, ok := fr.LastBinaryHello(serverID); !ok {
    t.Fatal("binary hello not observed within 5s")
}
```

**After** (the contract every site converges to):

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if !fr.WaitBinary(ctx, serverID) {
    t.Fatal("binary connection not registered within 5s")
}
```

Rules that hold at every site:
- **Preserve the existing per-site timeout duration** (5s everywhere except `TestRelay_1011`, which uses 4s — keep 4s).
- **Construct a fresh `context.WithTimeout`.** No site has a usable `ctx` in scope at the readiness point (the dial `ctx` is always created *after* it). `defer cancel()` is fine in both test bodies and helpers — `WaitBinary` returns synchronously before the defer runs.
- **Update the `t.Fatal` message** to describe WS registration rather than the hello (e.g. "binary connection not registered within Ns"). Cosmetic; not load-bearing.
- `time` stays imported in all nine files (`N*time.Second` survives in the new timeout, and every file uses `time` elsewhere) — so removing the poll loop never orphans the `time` import.

### Migration sites

Nine edit regions; each is one contiguous block → one edit. Two are helper-centralized (one edit covers multiple call sites).

| # | File | Edit region | Timeout | Notes |
|---|------|-------------|---------|-------|
| 1 | `register_push_token_test.go` | ~50-60 | 5s | uniform inline |
| 2 | `relay_assistant_turn_test.go` | ~77-85 | 5s | uniform inline |
| 3 | `relay_auth_test.go` | ~38-46 | 5s | uniform inline |
| 4 | `relay_roundtrip_test.go` | ~73-82 | 5s | uniform inline |
| 5 | `relay_send_message_test.go` | ~75-83 | 5s | uniform inline |
| 6 | `relay_test.go` (`TestRelay_1011`) | ~145-154 | **4s** | **add `context` import**; leave line 72 in `TestRelay_Hello` |
| 7 | `relay_v2_daemon_test.go` (`waitBinaryHello`) | ~77-87 | 5s | migrate helper body; 2 callers unchanged; update doc comment |
| 8 | `relay_v2_handshake_test.go` (`startV2Harness`) | ~85-94 | 5s | `serverID` is typed — keep the `string(serverID)` cast: `WaitBinary(ctx, string(serverID))` |
| 9 | `respawn_after_eviction_test.go` | ~128-136 | 5s | **migrate only the `LastBinaryHello` poll**; leave the idle-eviction WARN scanner above it |

### Per-site notes (the non-uniform parts)

These are the only deviations from the pure find-and-replace; the rest is the idiom above.

- **#6 `relay_test.go` — import add.** This file imports `time` but **not** `context`. The new `context.WithTimeout` requires adding `"context"` to its import block. **Build-break risk:** because the e2e suite is `//go:build e2e`-tagged, `go test -race ./...` (what CI runs) will **not** compile this file, so a missing import won't redden CI — it only surfaces under `-tags e2e`. Validate with the tagged build (§ Testing strategy).
- **#7 `relay_v2_daemon_test.go` — helper.** Migrate the body of `waitBinaryHello(t, fr, serverID)` in place; its 2 callers (lines 127, 222) stay verbatim. After migration the name is mildly inaccurate (it no longer waits for a *hello*). **Recommended: keep the name, update the doc comment (lines 75-76)** to describe WS-registration semantics — renaming adds call-site fan-out for no behavioral benefit and the AC is decoupling, not naming. (Rename to e.g. `waitBinaryUp` is a clean 3-site option if the developer prefers semantic accuracy; optional.)
- **#8 `relay_v2_handshake_test.go` — typed server-id.** `serverID` here is a typed value passed as `string(serverID)` to `LastBinaryHello`. `WaitBinary` takes a plain `string`, so keep the cast.
- **#9 `respawn_after_eviction_test.go` — two loops, migrate one.** The function has an idle-eviction WARN scanner (≈100-119, scans logs — unrelated, **leave it**) *and* the `LastBinaryHello` readiness poll (≈128-136, **migrates**). Touch only the latter.

## Concurrency model

No goroutines introduced. The only concurrency contract is `WaitBinary`'s: it polls `s.binaries` on a 2 ms ticker and returns on first registration or `ctx.Done()`. Correctness depends entirely on the caller passing a `ctx` with a finite deadline — the single reason every site constructs `context.WithTimeout` rather than reusing `context.Background()`.

## Error handling

- **Timeout path:** `WaitBinary` returns `false` → `t.Fatal(...)`, identical failure semantics to the pre-check it replaces (the test stops at the readiness gate). The only change is the *signal* (WS registration vs. hello-envelope receipt).
- **No new error branches**, no production error handling — test-only.

## Testing strategy

No new tests; this migrates the readiness probe under existing tests. Validation is "everything still passes, including the bits we deliberately left alone."

- **`go test -tags e2e ./internal/e2e/...` — the real gate.** This is the only command that compiles the migrated files (the `e2e` build tag). Run it; it must pass with **no readiness-related flakes**. This is also what catches the `relay_test.go` `context` import (a plain `go build ./...` won't, because it skips tagged files). A `go vet -tags e2e ./internal/e2e/...` is a fast pre-check for the import.
- **`go test -race ./...` (AC)** — passes regardless (e2e files not compiled); run it to confirm nothing outside e2e regressed.
- **Behavior-unchanged guards that must stay green, untouched:**
  - `TestRelay_Hello` (`relay_test.go`) — its line-72 `LastBinaryHello` payload assertion proves the binary still sends the hello and the fakerelay still records it.
  - `fakerelay_test.go` `LastBinaryHello` / `WaitBinary` (`TestWaitBinary`) coverage — proves both accessors still behave.
- **The `LastBinaryHello` accessor and the `lastBinaryHello` recording in `fakerelay.go` stay in place** (AC) — #583 removes them once no test reads them. Do not delete them here.

## Open questions

1. **`waitBinaryHello` rename.** Spec recommends keep-name + update-comment (minimal). Developer may rename to `waitBinaryUp` (3 sites) if preferring semantic accuracy. Either is acceptable; not a blocker.
2. **Timeout normalization.** `TestRelay_1011` uses 4s, all others 5s. Spec says preserve per-site durations (no behavior change). Normalizing to 5s is harmless but out of scope — don't.

## Out of scope (explicit)

- `internal/relay/connection_test.go` — uses its own in-file `testRelay` double (with `HelloEnv` accessors), **not** the e2e `fakerelay`; `WaitBinary`/`LastBinaryHello` aren't importable there. Its hello/hello_ack cases are handshake-*subject* tests that #582 updates when retiring the handshake.
- Any production code. `internal/relay/connection.go` is untouched. The binary's wire behavior is unchanged.
- Removing `LastBinaryHello` — that's #583.
