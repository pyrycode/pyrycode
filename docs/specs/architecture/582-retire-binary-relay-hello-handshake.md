# Spec #582 — Retire the binary↔relay hello/hello_ack handshake

**Size:** S (confirmed; not overridden to XS — the production change is a trivial deletion, but the `connection_test.go` rework — restructuring the test relay from a hello/hello_ack responder into a content-blind forwarder, migrating ~6 tests' readiness sync off `HelloEnv`, and adding the no-ack regression test — is real, bounded, single-file work that keeps this an honest S.)

**One production source file modified:** `internal/relay/connection.go`. Everything else is test files (`connection_test.go`, `internal/e2e/relay_test.go`) and one doc (`docs/protocol-mobile.md`).

## Files to read first

- `internal/relay/connection.go:1-13` — package doc comment; describes the "one-shot hello/hello_ack handshake on every fresh conn" that this ticket retires (doc-comment AC).
- `internal/relay/connection.go:39-43` — `handshakeTimeout` package var (delete; AC #1).
- `internal/relay/connection.go:232-261` — `run()`; the `case <-c.client.Connected():` branch is the surgical call site (AC #1).
- `internal/relay/connection.go:263-315` — `handshake()`; delete in full (AC #1). Note its `protocol.HelloServerPayload` construction (264-269) is the only thing the AC names for removal — the *type* stays (still defined in `internal/protocol/handshake.go`, still referenced by the v2 phone↔binary path; its removal is the separate follow-up cleanup, out of scope here).
- `internal/relay/connection.go:163-183` — `Frames` / `Send` doc comments mentioning "a fresh hello/hello_ack handshake" (doc-comment AC). `CloseConn` (185-210) does **not** mention the handshake — verify, leave it.
- `internal/relay/connection.go:344-351` — `classifyTransportErr`; the 4409→`ErrServerIDConflict` mapping. Read-only confirmation it's independent of the hello round-trip (AC #3) — do **not** touch it.
- `internal/relay/connection_test.go:35-233` — `shortenHandshakeTimeout` helper + `testRelay` (behaviors enum, the hello-read + hello_ack-send block, `helloEnv`/`HelloEnv`). This is the bulk of the rework.
- `internal/relay/connection_test.go:277-663` — the test cases: which to delete, which to repurpose, which to re-sync. See § Test rework.
- `internal/e2e/relay_test.go:52-93` — `TestRelay_Hello`; delete this function + its doc comment only. `TestRelay_4409` (95-128) and `TestRelay_1011` (130-176) stay and share the file's helpers — leave them and the helpers intact.
- `internal/e2e/internal/fakerelay/fakerelay.go:195-271` — `handleBinary`: registers `s.binaries[serverID]` on WS upgrade (line 266), header-based, **before any hello**. This is why `WaitBinary` keeps working after the binary stops sending a hello. Read-only — fakerelay is NOT modified here (its dead hello-receiving path is the named follow-up).
- `docs/protocol-mobile.md:17-29` — the "v2 changes from v1" table; the **Endpoints** row (line 20) is the one to amend (AC #5).
- `docs/protocol-mobile.md:312-326` — § Authentication / Binary → relay; already header-based + "no Noise on this leg". This is where the "established on WS upgrade, no relay-originated hello_ack, route names carry no protocol meaning" note lands.

## Context

The v2 mobile wire is a hard cutover and the relay is **content-blind**: `pyrycode-relay` registers a binary's server-id from the `x-pyrycode-server` request header, claims the slot on WS upgrade, and signals a server-id clash with WS close 4409. It **never** sends a `hello_ack` — under v2 a `hello_ack` is AEAD-sealed application data the relay holds no key for, and registration is already header-based.

The binary's outbound connection (`internal/relay/connection.go` `handshake()`) still runs a v1 ceremony: it sends a `hello` advertising `ProtocolVersions: ["v1"]` and blocks up to 5s for a wrapped `hello_ack` the production relay never produces, fails with `hello_ack timeout after 5s`, and recycles the conn. That is the lone blocker to a real binary completing its relay leg against the live relay (the 2026-05-29 Noise spike only worked around it with an in-process fakerelay that synthesises the ack).

This retires the design question behind `pyrycode-relay#105` (demoted because under v2 the relay cannot author a `hello_ack`).

**Out of scope (do not touch):**
- The phone↔binary `hello`/`hello_ack` — survives, carried as Noise_IK early-data, E2E-encrypted, relay-blind (`docs/protocol-mobile.md` § Handshake).
- The fakerelay hello-receiving path (`handleBinaryDirect` hello branch + `LastBinaryHello`) — now-dead, but its removal is a separate follow-up cleanup. Leave it.
- The `protocol.HelloServerPayload` / `HelloAckPayload` / `TypeHello` / `TypeHelloAck` *types* — still used by `internal/relay/auth.go`, `internal/relay/v2session.go`, and the fakerelay. Only the *construction* of `HelloServerPayload` inside `connection.go` is removed.
- The `/v1/...` → `/v2/...` route rename in the protocol doc — explicitly NOT performed here (AC #5).
- No `pyrycode-relay` code change. Closing `pyrycode-relay#105` as moot is a ship-time action, not part of this diff.

## Design

Pure deletion in production. The `run()` loop's connection lifecycle collapses from "on fresh conn, handshake then forward" to "on fresh conn, forward".

### `internal/relay/connection.go`

**1. `run()` — the `case <-c.client.Connected():` branch (AC #1).** Replace the handshake-then-forward block with a straight call to `forwardFrames(ctx)`. Target shape (contract, not full body):

```go
case <-c.client.Connected():
    c.cfg.Logger.Info("relay: conn established", "server_id", string(c.cfg.ServerID))
    c.forwardFrames(ctx)
```

- The `Info("relay: conn established", ...)` line is **recommended** (not strictly required by an AC): it preserves the ops breadcrumb the deleted `handshake()`'s `"relay: handshake complete"` Info log provided, and gives AC #2 ("reaches frame-forwarding") an observable signal. Keep it to one line; drop it if it complicates the diff, but prefer keeping it.
- `c.client.DropConn()` was called **only** from the now-deleted handshake-failure path. After this change it has no caller in `internal/relay` — that is expected and fine. `DropConn` remains a public method on `transport.Client` (`internal/transport/wssclient.go:333`) for other/future callers; do not delete it.
- Reconnect stays transparent: `Connected()` re-fires once per fresh conn, so each reconnect now goes straight to `forwardFrames` with no ceremony.

**2. Delete `handshake()` (lines 263-315) in full (AC #1).** This removes the `hello` send, the 5s `Receive` wait, the `hello_ack`-timeout error, and the `protocol.HelloServerPayload` construction.

**3. Delete the `handshakeTimeout` package var + its doc comment (lines 39-43) (AC #1).**

**4. Import hygiene.** After deletion, confirm `time` is still imported (still used by `WriteTimeout: 10 * time.Second` in `Connect`, line 133) — it is, so the import stays. `protocol`, `errors`, `encoding/json`, `context` all retain other uses. `protocol.Envelope` becomes unreferenced in this file but `protocol` stays imported for `RoutingEnvelope`; no unused-import breakage. Run `go build ./internal/relay/` to confirm.

**5. Doc comments (doc-comment AC):**
- Package doc (lines 1-13): drop "runs the one-shot hello/hello_ack handshake on every fresh conn"; replace with the content-blind framing — the package opens the WSS via `internal/transport`, treats the conn as established on WS upgrade (the relay registers the binary's server-id from the `x-pyrycode-server` header and sends no `hello_ack`), and exposes inbound frames via `Frames()`.
- `Frames` comment (163-167): drop "(a fresh hello/hello_ack handshake runs first, then frames resume on the new conn)"; reconnects are transparent — frames resume on the new conn directly.
- `Send` comment (170-176): drop "and a fresh hello/hello_ack handshake" from the reconnect clause; transport reconnect alone happens asynchronously.

### AC #3 — conflict path preserved (no production change)

The 4409→`ErrServerIDConflict` mapping in `classifyTransportErr` rides the transport's `FatalCloseCodes: [4409]` (`Connect`, line 135) and `errors.Is(err, transport.ErrFatalClose)` — it never depended on the hello round-trip. It survives untouched. AC #3 is satisfied by *confirming* the existing test still fires (see § Test rework, `TestServerIDConflict_FatalNoReconnect`), not by editing production code.

## Concurrency model

Unchanged. `run()` keeps its single select loop over `ctx.Done()` / `closed` / `transportErrCh` / `Connected()`. `forwardFrames` keeps its single read loop. Removing `handshake()` removes one synchronous `Receive` with a derived `context.WithTimeout` — strictly fewer moving parts, no new goroutines, no new channels. Shutdown sequence (`Close` → `client.Close` → `transportErrCh`/`closed` unblocks `run` → `done`/`frames` close) is unaffected.

## Error handling

- The `hello_ack timeout after 5s` and `recv hello_ack` / `decode … envelope` / `expected hello_ack` error paths are deleted along with `handshake()`. The conn-recycle-on-handshake-failure path (`Logger.Warn(...)` + `DropConn` + `continue`) is gone — a fresh conn now always proceeds to `forwardFrames`.
- Terminal classification is unchanged: `ErrServerIDConflict` (fatal 4409), `ctx.Err` (graceful), wrapped transport error (unexpected halt). `forwardFrames` still exits on transport `ErrDisconnected` (→ `run` re-enters `Connected` on the next conn) / `ErrClosed` / `ctx.Err`.

## Test rework — `internal/relay/connection_test.go`

The test relay currently impersonates a v1 relay (read hello → send hello_ack). It must become a content-blind v2 relay (register on upgrade → forward frames; never read a hello, never send an ack). Most tests sync readiness on `relay.HelloEnv(0)`; that signal disappears, so they re-sync on the connection itself.

**Test-relay machinery:**
- Delete `shortenHandshakeTimeout` (35-40) and the package-var comment block (24-26) — `handshakeTimeout` no longer exists.
- Delete the `helloEnv` field (60), the `HelloEnv` accessor (93-100), and the hello-read + capture block in the handler (157-167).
- In the handler, after registering the conn (`connCount.Add(1)` + `connectedCh` signal + header capture), go **straight** into the frame-forwarding path (the existing happy-path dead-reader goroutine + `outboundFrames` pump, lines 204-228) for the default behaviour. No hello read, no `hello_ack` send.
- Behaviors enum (45-51): keep `behaviorCloseImmediately4409` (4409 case). Replace `behaviorHappyPath` with a "register + forward" default (suggested name `behaviorForward`; update the now-wrong `// hello → hello_ack` comment). Replace `behaviorDropDuringHandshake` with a "drop right after upgrade" behaviour (suggested name `behaviorDropOnConnect`: `CloseNow()` immediately on accept, before forwarding). **Delete** `behaviorSilentNoAck` (nothing to be silent about) and `behaviorSendBadType` (no handshake frame to corrupt). Update `newTestRelay`'s default to the forward behaviour.
- Add a small readiness helper (suggested `waitConnCount(t, relay, n, timeout)`) that polls `relay.ConnCount() >= n` — this replaces the inlined `HelloEnv(0)` poll loops across the cases below and keeps the churn DRY. (Alternatively block on the existing buffered `connectedCh`; the polling helper mirrors the existing style.)

**Tests to delete (AC #4):**
- `TestHandshake_AckTimeout` (347-384) — the hello_ack-timeout case.
- `TestHandshake_UnexpectedFrame` (386-408) — the unexpected-frame-during-handshake case.

**Test to repurpose into the new AC #2 case:**
- `TestConnect_HappyPath` (277-315) currently asserts the hello envelope's contents — its entire subject is the hello the binary no longer sends. Replace it with **`TestConnect_ReachesForwardingNoAck`** (or similar), the AC #2 regression guard:
  - Scenario: default forward relay (never sends a hello_ack). Connect the binary. Push one frame via `relay.outboundFrames` and assert it arrives on `c.Frames()` with the expected `conn_id` — proving the binary reached `forwardFrames` without waiting on an ack.
  - Assert the conn was **not recycled**: after a short beat, `relay.ConnCount() == 1` (the old hello_ack-timeout path would have produced a second conn within ~5s). Frame delivery + `ConnCount == 1` together pin "reaches frame-forwarding, no ack, no recycle".

**Tests to re-sync (swap `HelloEnv(i)` readiness → `ConnCount()`/`waitConnCount`), behaviour otherwise unchanged:**
- `TestTransportDropPostHandshake_ReHandshakes` (460-523) → rename to drop "ReHandshakes" (e.g. `TestTransportDropPostConnect_Reconnects`). First-conn readiness: `waitConnCount(…, 1, …)`; second-conn assertion: `ConnCount() >= 2`. The post-reconnect frame-delivery assertion stays as the proof the rebuilt pipeline works. Drop the "wait for the relay to send hello_ack" sleep/comment.
- `TestFrames_DeliversPostHandshakeInOrder` (525-590) → optionally rename "PostHandshake"→"AfterConnect"; swap readiness to `waitConnCount`; update the "after sending hello_ack" comment on the 100ms settle sleep to "after entering the forward loop".
- `TestClose_ShutsDownCleanly` (592-631) → swap readiness to `waitConnCount`.
- `TestContextCancel_ShutsDownCleanly` (633-663) → swap readiness to `waitConnCount`.
- `TestTransportDropDuringHandshake` (436-458) → rename (e.g. `TestTransportDropOnConnect_Reconnects`), point at `behaviorDropOnConnect`, assert `ConnCount() >= 2` (drop-then-reconnect). Still a valid reconnect-on-early-drop test.

**Tests to keep unchanged (confirm still green):**
- `TestServerIDConflict_FatalNoReconnect` (410-434) — **AC #3.** No hello dependency; uses `behaviorCloseImmediately4409` + asserts `Wait()` returns `ErrServerIDConflict` and `ConnCount == 1`. Confirm it still fires; do not weaken it.
- `TestHeaders_Set` (317-345) — already header-based (`relay.Headers(0)`), no hello dependency.
- `TestConfig_Validation_TableDriven` (665-703), `TestCloseConn_WireShape` (705-733), `TestCloseConn_PropagatesNotConnected` (735-763), `TestConfig_AllowInsecureScheme` (765-786) — no hello dependency.

**Import hygiene in the test file:** after removing the hello/ack marshalling, `protocol.HelloServerPayload` / `HelloAckPayload` / `TypeHello` / `TypeHelloAck` uses disappear from this file; `protocol.Envelope`, `RoutingEnvelope`, `TypeMessage`, `TypeError`(only if `behaviorSendBadType` is gone — it is, so `TypeError` may drop too) need a final check. Let `go vet ./internal/relay/` flag any now-unused import.

## Test rework — `internal/e2e/relay_test.go`

Delete `TestRelay_Hello` (52-93) and its doc comment. Its entire subject is the binary↔relay hello the binary no longer sends; it's the change here that breaks it, so the deletion belongs here. `TestRelay_4409` and `TestRelay_1011` stay (both header/`WaitBinary`-based, no hello dependency) and keep sharing `shortHome` / `readPersistedServerID` / `relayTestLogger` — leave those helpers.

## Documentation — `docs/protocol-mobile.md`

Surgical, additive. Do **not** mass-rewrite the doc's many `/v2/server` references (AC #5 forbids the rename).

1. **Endpoints row (line 20)** — change the v2 cell so routes stay `/v1/server`, `/v1/client` (matching the v1 cell), with an inline note that the path label carries no protocol meaning. E.g. the v2 cell becomes: `` `/v1/server`, `/v1/client` (unchanged — the relay is content-blind; the route path carries no protocol meaning) ``.
2. **§ Authentication / Binary → relay (around 314-326)** — add a sentence stating the binary↔relay leg is **established the moment the WS upgrade completes**: there is no relay-originated `hello`/`hello_ack` handshake on this leg (under v2 a `hello_ack` would be AEAD-sealed application data the relay holds no key for), and server-id registration is purely header-based via `x-pyrycode-server`. Add the route-naming clarification here too: the `/v1/...` vs `/v2/...` path label is cosmetic — the relay registers from the header regardless of path, so references to `/v2/server` elsewhere in this doc denote the same content-blind endpoint, and the `/v1` labels are retained until a future cosmetic rename.
3. Optional: a one-line entry in the doc's `## Changelog` (line 733) noting the binary↔relay hello/hello_ack retirement (#582). Keep it to one line; skip if it complicates the diff.

These three edits satisfy "no relay-originated `hello_ack` on the binary↔relay leg" + "established on WS upgrade with header-based registration" + "route names carry no protocol meaning" + "Endpoints row amended, `/v2` rename not performed".

## Testing strategy

- `go build ./...` then `go vet ./...` — catch unused imports / dead references from the deletions.
- `go test -race ./internal/relay/` — the reworked unit suite. The AC #2 case is the key new assertion: frame delivered + `ConnCount == 1` (no recycle). AC #3: `TestServerIDConflict_FatalNoReconnect` still returns `ErrServerIDConflict`.
- `go test -race -tags e2e ./internal/e2e/` — confirm `TestRelay_4409` / `TestRelay_1011` and the other `WaitBinary`-based e2e tests still pass after the binary stops sending a hello (the fakerelay registers on upgrade, so they should be unaffected). Confirm `TestRelay_Hello`'s deletion left the file compiling.
- Ultimate validation (per AC #2, manual / out-of-CI): a real `pyry` binary completes its relay leg against the live `pyrycode-relay` with no `hello_ack timeout` and no recycle.

## Open questions

- **Keep or drop the `"relay: conn established"` Info log?** Recommended to keep (ops breadcrumb parity + observable AC #2 signal). Developer's call if it complicates the diff. Not gated by an AC.
- **Rename churn vs. minimal diff for the re-synced tests.** The renames above (`…ReHandshakes` → `…Reconnects`, `…DuringHandshake` → `…OnConnect`) are recommended for accuracy but optional; the binding requirement is the readiness-sync swap and the behaviour-enum cleanup, not the names. Prefer the renames; don't let them balloon the diff.
- **`behaviorDropOnConnect` retention.** Kept because "conn drops immediately after upgrade → transport reconnects" is still a real path worth covering. If the developer judges it fully redundant with `TestServerIDConflict`'s close path, it may be dropped — but it tests reconnect (non-fatal) vs. fatal-close, which are distinct, so keep it.

## Security review

**Verdict:** PASS

This ticket is `security-sensitive`. The adversarial re-read assumed the spec has holes and asked, per category, "what could a hostile relay / buggy caller / confused developer trigger?" The deletion's central risk — *does removing the handshake bypass authentication or weaken a trust boundary?* — was traced to ground and answered no.

**Findings:**

- **[Trust boundaries] No findings.** The removed `handshake()` was never a trust boundary. On the binary↔relay leg, the relay is content-blind and authenticates the binary by **header-based server-id registration** (`x-pyrycode-server`) + first-claim-wins + 4409 conflict (`connection.go:125-136, 344-351`) — none of which read the hello. The hello (`HelloServerPayload`) advertised only non-secret fields (`Role`, `ServerID`, `BinaryVersion`, `ProtocolVersions`); the relay already learns the version from the `x-pyrycode-version` header, so the hello conveyed nothing the header didn't. The *real* auth boundary — the per-phone-conn Noise_IK handshake + device-token validation (`docs/protocol-mobile.md` § Token-validation gating, line 185) — lives downstream of `internal/relay` ("a future ticket: supervisor wiring + per-message handlers", package doc) and is explicitly out of scope and untouched. Reaching `forwardFrames` without a relay-leg ack does **not** admit unauthenticated traffic: each forwarded `RoutingEnvelope` still must pass the downstream Noise + token state machine before any application dispatch, exactly as before. The handshake removal moves no boundary.
- **[Tokens, secrets, credentials] No findings.** No token/secret is touched. The device-token travels only in the phone↔binary Noise early-data (relay-blind), never in the binary↔relay hello. The deleted `HelloServerPayload` carried no secret; `ServerID` is non-secret by design (protocol doc § Identifiers: "sent unencrypted on WS upgrade — yes"). The recommended `Info("relay: conn established", "server_id", …)` log emits only `server_id`, a MUST-log non-secret routing field (protocol doc § Security review logging policy); no payload, header, or token is logged.
- **[File operations] N/A.** No filesystem path is read, written, or constructed by this change — it is network-leg deletion plus test/doc edits.
- **[Subprocess / external command execution] N/A.** None involved.
- **[Cryptographic primitives] No findings (crypto-inert by design).** The binary↔relay leg has no crypto — the relay is not a Noise peer (protocol doc § Authentication: "The binary does not authenticate to the relay via Noise"). The deletion touches no key, nonce, or AEAD state; the inner frames `forwardFrames` relays stay AEAD-sealed end-to-end and are neither decrypted nor re-keyed here. The Noise/re-key machinery on the phone↔binary leg is untouched.
- **[Network & I/O] No findings; one pre-existing item named out of scope.** Removing the 5s `handshakeTimeout` does not introduce an unbounded read: `forwardFrames` already exists and reads via the same `c.client.Receive` under the transport's read limit — the change only reaches it without a prior bounded handshake `Receive`. Removing the 5s gate does not regress idle-conn liveness: that gate only ever governed the (now-deleted) handshake window; in steady state the binary already blocked indefinitely in `forwardFrames` holding the long-lived conn open (protocol doc § Connection lifecycle / Binary step 3 — "Hold open for inbound phone connections"), so the idle posture is **identical before and after**. Liveness of an idle/dead conn is the transport's WS-native ping/pong job (protocol doc § Heartbeat: 30s ping, 60s dead-conn detection) — a deterministic transport-layer net, not the application handshake. Confirming the transport actually enables WS ping/pong or a read deadline is a **pre-existing** concern (the steady state had no app-layer timeout under the old code either) and is **out of scope** for #582; file separately if unverified.
- **[Error messages, logs, telemetry] No findings.** Deleted error strings (`hello_ack timeout after 5s`, etc.) were internal `slog` Warn lines with no secret content and no external recipient (they fed local logs + `Wait()`, consumed by the supervisor). No new external-facing error is introduced.
- **[Concurrency] No findings.** The change removes one synchronous `Receive` with a derived `context.WithTimeout` from `run()`; it adds no goroutine, lock, or shared-state access. `forwardFrames` and the shutdown sequence (`Close`/ctx-cancel/transport-err → `done`/`frames` close) are unchanged. The test-relay rework removes the hello-read but preserves the existing dead-reader-goroutine pattern in the forward path — no new lifecycle.
- **[Threat model alignment] No findings; aligns with v2 model.** Of the relevant threats in protocol-mobile.md § Security model: **#2 Server-id race** is preserved — the 4409→`ErrServerIDConflict` path (`classifyTransportErr`, AC #3) is header-based at the relay and never depended on the hello; `TestServerIDConflict_FatalNoReconnect` confirms it still fires. **#3 Relay operator MITM** is unaffected — mitigated by Noise on the untouched phone↔binary leg; removing the redundant plaintext hello marginally *reduces* relay-visible metadata, never expands relay capability. **#5 Implementation bugs** — deleting dead ceremony code shrinks attack surface. The change is a net alignment with the content-blind-relay design, introducing no new threat.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-06-07
