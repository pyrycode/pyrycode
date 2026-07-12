# Spec — e2e: re-key over the v2 wire harness (#920)

Scheduled happy path + timer re-base + phone-initiated spontaneous re-key + transport-down-at-boundary, exercised end-to-end over the fakerelay + fakephone harness.

Size: **S**. One new e2e test file + one small additive production seam in `internal/relay/v2session.go`. Security-sensitive (crypto rotation over the wire — see § Security review, the last section).

---

## Files to read first

- `internal/e2e/relay_v2_handshake_test.go:42-201` — `v2Harness`, `startV2Harness` (opts pattern), `dialPhone`, `sendNoiseInit`, `sendNoiseMsg`, `readInnerFrame`, `buildHelloEarly`. **This is the harness you extend.** `startV2Harness` already takes `opts ...func(*relay.V2SessionConfig)` — the seam for injecting rekey knobs + the down-shim.
- `internal/e2e/relay_v2_handshake_test.go:356-389` — `driveHandshakeToOpen`: runs a paired handshake from the phone side, returns `(initSend, initRecv *noise.CipherState)`. Reuse verbatim to reach V2StateOpen; the returned CipherStates are the "old keys" the rotation must retire.
- `internal/e2e/relay_v2_handshake_test.go:693-722` — `testV2IKReject`: the canonical "phone observes WS close code 4426" assertion shape (`phone.ReceiveBytes` → non-timeout err → `phone.LastCloseStatus()`). Case 2's corroborating wire assertion mirrors this.
- `internal/relay/v2session_test.go:1333-1446` — `TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys`: the **in-process non-vacuity pattern to port to the wire**. Note lines 1388-1428: fresh `initiator2.ReadResp` → `initSend2`/`initRecv2` → round-trip *under the new CipherStates*. This round-trip succeeding is the rotation proof (a non-rotated responder 4421s a new-key frame).
- `internal/relay/v2session_test.go:1882-2004` — `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder`: the in-process scheduled-emit + re-arm test. Lines 1883-1891 show the global-var shrink idiom (`rekeyInterval = 20ms`, non-parallel, save-and-restore). Lines 1919-1931 assert the emitted inner is `rekey_request{reason:"scheduled"}`. Your AC1/AC2 are the wire version.
- `internal/relay/v2session.go:1012-1036` — `armRekeyTimer` / `armRekeyReplyTimer`: the **only two functional read sites** of the `rekeyInterval` / `rekeyReplyTimeout` package vars (verified: nothing else reads them). These are the two functions the config-field override edits.
- `internal/relay/v2session.go:555-600` — `V2SessionConfig` struct + the existing optional `Connected func() bool` / `Reconnect` fields (the precedent for "optional field, zero/nil ⇒ default behaviour"). Your two new fields follow this exact pattern.
- `internal/relay/v2session.go:964-1010` — `handleWake`: `wakeRekeyEmit → emitRekeyRequest(...,"scheduled")`; `wakeRekeyReplyTimeout → Warn(event=noise.rekey_failed, close_code=4426) → closeWith(4426)`. This is the failure path case 2 asserts on `main`.
- `internal/relay/v2session.go:2916-3006` — `emitRekeyRequest`: on this `main` it seals (`s.send.Encrypt`, line ~2951 — burns the nonce) with **no** `transportDown()` gate, then `send()`s via `cfg.Outbound`. Confirms #912 has **not** landed here; AC4 asserts the `main` shape.
- `internal/relay/v2session.go:3026-3087` — `closeWith` + `send`: the 4426 close is a routing envelope forwarded via `cfg.Outbound`; a down leg drops it (debug log). Governs whether the phone can observe the 4426.
- `internal/relay/v2session.go:3220-3249` — `transportDown()` (`cfg.Connected != nil && !cfg.Connected()`) and `drainOnce`'s hold-on-down precedent (#874). #912 will gate the rekey emit on this same helper; case 2 wires `Connected` so that gate has its signal post-#912.
- `internal/relay/connection.go:196-220` — `(*Connection).Send` (returns `ErrDisconnected` when the leg is down), `Connected()`, `Reconnected()`. The manager consumes the binary↔relay leg **solely** through these; that is why the down-shim (below) is a faithful double.
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md:30,63-67,115` — Re-key policy: full Noise_IK re-run, **initiator-driven (phone always starts)**, 1-hour cadence as the rotation canary. This ticket is the CI enforcement of the "keep the rotation path exercised" rationale.
- `internal/e2e/internal/fakephone/fakephone.go:48,81-165` — `Client.SendBytes` / `Receive` / `ReceiveBytes` / `LastCloseStatus` / `ErrReceiveTimeout`. The phone-side primitives.

---

## Context

The v2 e2e suite (`internal/e2e/relay_v2_*_test.go`) exercises handshake, modal, queue, dequeue, interrupt, and two-head modal over the fakerelay + fakephone wire harness — but **re-key** is covered only in-process (`internal/relay/v2session_test.go`, at the `V2SessionManager` boundary). The scheduled hourly re-key is the one v2 mechanism that fires on a timer against a live session (ADR 024 § "1-hour re-key cadence" — the rotation canary), so a rotation regression surfaces late and in the field. This ticket adds wire-level coverage so the regression is caught in CI.

Four ACs across two harness shapes:

- **Case 1** (AC1 scheduled happy path + AC2 timer re-base) — the timer fires, the daemon emits `rekey_request{scheduled}`, the phone re-handshakes, traffic continues **under new keys**, and a second `rekey_request` fires at the shrunk interval (proving re-arm).
- **Case "1b"** (AC3 phone-initiated spontaneous) — a cold `noise_init` in the open state (no preceding `rekey_request`) is accepted and rotates keys. This is the wire twin of the in-process happy-path test.
- **Case 2** (AC4 transport-down-at-boundary) — the binary↔relay leg is down when the timer fires. On this `main`: nonce burned, session torn down at 4426, `noise.rekey_failed` logged for what was a transport outage (#912's target defect). Post-#912: no emit while down, retry after recovery, session survives. **Rebase point** — whichever of #912/#920 lands second flips this assertion (§ Open questions).

---

## Design

### The one non-obvious production decision: exposing the timing knobs to `package e2e`

The ticket's premise ("override the existing test-overridable `rekeyInterval`/`rekeyReplyTimeout` package vars via the save-and-restore idiom") holds **only for `package relay`** — `v2session_test.go` is same-package and mutates the unexported globals directly (`internal/relay/v2session_test.go:1886-1891`). The e2e tests live in `package e2e` and **cannot touch `internal/relay`'s unexported vars**. There is no other trigger for the scheduled path — `emitRekeyRequest(...,"scheduled")` fires only from the `rekeyTimer` `AfterFunc` at `rekeyInterval` — so AC1/AC2/AC4 are untestable from `package e2e` without an exported knob. The PO's "zero production code" estimate is therefore not achievable; a small additive seam is required.

**Chosen seam — two optional `V2SessionConfig` fields (not an exported `SetForTest` global).** This matches the struct's existing optional-field idiom (`Connected`, `Reconnect`: zero/nil ⇒ default), is per-manager (no global mutation, no cross-test interference, e2e can run these serially without disturbing the in-process suite), and puts no `ForTest` symbol in the production API — these are honest timing config.

Contract (add to `V2SessionConfig`, `internal/relay/v2session.go`):

- `RekeyInterval time.Duration` — optional. Zero ⇒ the `rekeyInterval` package-var default (1h).
- `RekeyReplyTimeout time.Duration` — optional. Zero ⇒ the `rekeyReplyTimeout` package-var default (30s).

Wiring (the only two functional read sites, verified):

- `armRekeyTimer` (`v2session.go:1017`): read `m.cfg.RekeyInterval` when `> 0`, else the package var, and pass that to `time.AfterFunc`.
- `armRekeyReplyTimer` (`v2session.go:1029`): same against `m.cfg.RekeyReplyTimeout` / `rekeyReplyTimeout`.

The package vars stay as the fallback defaults. Production wiring (`cmd/pyry`) sets neither field → behaviour byte-identical to today. The in-process `v2session_test.go` suite keeps mutating the globals → unaffected. **No consumer cascade** (additive optional fields; existing constructor call sites untouched). Invariant asserted by: `internal/relay/v2session_test.go` timer tests must still pass unchanged; the new e2e tests read the shrunk interval via config.

> Belt-and-suspenders note: this is a deterministic-code seam (config field read by the timer arm), not a stochastic rule — the appropriate fabric for a timing override.

### Harness extension (all in the new test file)

Reuse `startV2Harness(t, reg, handlers, opts...)` and `driveHandshakeToOpen`. Add three per-test knobs via `opts` closures over `*relay.V2SessionConfig` (no new harness constructor):

1. **Shrunk timing** — `opt` sets `cfg.RekeyInterval` and `cfg.RekeyReplyTimeout`. Use a *comfortable* interval (≈1s), not the in-process 20ms: the e2e path has real WS round-trips, and the case-2 ordering (below) leans on a wide margin between the synchronous down-flip and the timer fire. `rekeyReplyTimeout` ≈300-500ms.
2. **Capturing logger** — case 2 asserts `event=noise.rekey_failed`. `relayTestLogger()` writes to `os.Stderr` (not capturable). Add a small local capturing `*slog.Logger` (`slog.NewTextHandler` over a mutex-guarded `bytes.Buffer`; the existing `internal/e2e/auto_attach.go:67` `safeBuffer` is the precedent). Provide it via an `opt` that sets `cfg.Logger`, and expose the buffer to the test for `strings.Contains(buf, "noise.rekey_failed")`.
3. **Down-shim** (case 2 only) — see below.

### Case 1 — AC1 scheduled happy path + AC2 timer re-base

Flow (bullets; developer writes the wire code in the suite's idiom):

- `startV2Harness` with a paired registry, an echo handler keyed by `protocol.TypeListConversations` (reuse the `testV2EncryptedEchoRoundTrip` handler shape), and the shrunk-timing opt.
- `driveHandshakeToOpen` → `(initSend, initRecv)`. Optionally round-trip one app request under these keys first, to bank a "traffic flows under the ORIGINAL keys" baseline.
- **AC1:** `readInnerFrame` (timeout > `RekeyInterval`, e.g. 3s) → expect a `noise_msg`; decrypt with `initRecv`; assert the inner envelope is `rekey_request` with `reason:"scheduled"`.
- Phone re-handshakes: build a fresh `noise.NewInitiator(initPriv, h.pubKey)` **reusing the same `initPriv`** (peer-static continuity), `WriteInit(nil)` (empty early-data per ADR 024 § Re-key), `sendNoiseInit`. Read the `noise_resp`, `initiator2.ReadResp` → `(earlyAck, initSend2, initRecv2)`; assert `len(earlyAck)==0`.
- **Non-vacuity (load-bearing):** round-trip an app request sealed under `initSend2`, expect a reply that decrypts cleanly under `initRecv2` with `InReplyTo` = request id. Sealing under the NEW keys and getting a clean reply proves the responder retired the old CipherStates and swapped to the new ones — a non-rotated responder would 4421 the new-key frame. (This is the wire port of `v2session_test.go:1396-1428`.)
- **AC2 (re-arm):** after the first re-key completes (which calls `rekeyComplete` → re-arms `rekeyTimer` at the shrunk interval), `readInnerFrame` again → expect a SECOND `rekey_request{scheduled}` at ~`RekeyInterval` later. Its arrival proves the timer re-armed rather than firing once. (Decrypt with `initRecv2` — the second request is sealed under the post-rotation send key.)

### Case 1b — AC3 phone-initiated spontaneous re-key

- Same harness (shrunk interval large enough that the scheduled timer does NOT fire during this subtest — pick an interval comfortably longer than the subtest, or run this subtest fast).
- `driveHandshakeToOpen` → `(initSend, initRecv)`.
- Phone sends a cold fresh `noise_init` (same `initPriv`, `WriteInit(nil)`) with **no** preceding `rekey_request`.
- Assert: responder accepts (replies `noise_resp`, no 4426), `initiator2.ReadResp` → new keys, round-trip under `initSend2`/`initRecv2` succeeds. Confirms the responder's `handleRekeyInit` accepts a spontaneous rotation (see `rekeyComplete`'s "no-op if not set — a spontaneous..." comment, `v2session.go:401`).

### Case 2 — AC4 transport-down at the re-key boundary

**Down primitive — a test-controlled shim over `Outbound`/`Connected`, NOT `fakerelay.ForceCloseBinary`.** The manager observes the binary↔relay leg only through `cfg.Connected()` and `cfg.Outbound()` (`connection.go:196-220`). Wrapping those two with a test-flipped `down atomic.Bool` reproduces "leg down" **at the exact boundary the manager cares about**, and is *deterministic* — whereas `ForceCloseBinary` reintroduces the transport reconnect-backoff race (~1s) that the ticket flags as the hard part ("timing the cut to land just before the timer fires is the design surface"). The shim is the right test double (CODING-STYLE: "simple test doubles").

Shim contract (in the case-2 `opt`, closing over the real `conn`):

- `cfg.Connected = func() bool { return !down.Load() && conn.Connected() }`
- `cfg.Outbound = func(e) error { if down.Load() { signalDrop(); return errSimulatedDown }; return conn.Send(e) }`

Wiring `Connected` is required for the post-#912 gate to have a signal; on this `main` `emitRekeyRequest` ignores it, so it is harmless. `signalDrop` is a non-blocking send on a cap-1 channel the test waits on (proves an emit was *attempted while down* — during the down window nothing else is sent). Inbound (`Frames`) stays live; that is faithful for this scenario because the phone is passive during the down window (it has no `rekey_request` to answer).

Deterministic ordering (the crux):

1. `startV2Harness` with shrunk timing (`RekeyInterval` ≈1s giving a wide margin, `RekeyReplyTimeout` ≈300-500ms) + capturing logger + the down-shim opt. `down` starts `false` (handshake needs the leg up — the `noise_resp` goes through `Outbound`).
2. `driveHandshakeToOpen` → open (leg up). The `rekeyTimer` is armed at open for ~1s from now.
3. **`down.Store(true)`** immediately (synchronous, sub-ms) — well inside the ~1s margin before the timer fires. This is the "cut just before the timer fires," made deterministic by the wide margin (the same timing-margin assumption the in-process timer tests rely on, `v2session_test.go:1887`).
4. Timer fires (~1s) → `handleWake` → `emitRekeyRequest`. On `main`: seals `rekey_request` (nonce burned), `send()` → `Outbound` returns `errSimulatedDown` → dropped + `signalDrop`. `awaitingRekeyReply=true`, reply timer armed. The phone receives nothing.
5. Test waits on the drop signal (proves the emit fired while down), then **`down.Store(false)`** — so the subsequent 4426 close reaches the phone. The phone never received the `rekey_request`, so it never re-handshakes; `awaitingRekeyReply` stays true. The daemon does **not** re-emit (emit is one-shot).
6. `RekeyReplyTimeout` elapses → `wakeRekeyReplyTimeout` → `Warn(event=noise.rekey_failed, close_code=4426)` → `closeWith(4426)`. Leg is up now → the phone observes WS close 4426.

**AC4 assertions on `main`:**
- Captured log contains a line with `event=noise.rekey_failed` and `close_code=4426` (evidences both the mislabelling and the close code — emitted locally, independent of wire delivery).
- Phone observes WS close code **4426** (mirror `testV2IKReject`: `ReceiveBytes` → non-timeout err → `LastCloseStatus()==4426`).
- **Non-vacuity / positives-gate-the-negative:** during the down window the phone receives **no** decryptable `rekey_request` (`ReceiveBytes` → `ErrReceiveTimeout` while down), contrasted structurally with case 1 where it does. This pins that the failure is "request never delivered," not a test that simply missed a success.

**Rebase point (see § Open questions)** — post-#912 the assertions flip to: no `rekey_request` emitted while down (drop signal never fires / no seal), the emit retried after `down.Store(false)`, the phone completes the re-key, session survives (no 4426, no `noise.rekey_failed`). Structure the assertion block so this is a single, clearly-commented `// AC4 REBASE POINT (#912)` region.

---

## Concurrency model

- **Timer → wake → Run goroutine.** Unchanged. `armRekeyTimer`/`armRekeyReplyTimer` `AfterFunc` callbacks push `wakeSignal`s onto `m.wake`; the single-owner Run goroutine services them. The config-field change only alters the `AfterFunc` *duration* argument — no new goroutine, channel, or lock.
- **Down-shim atomics.** `down atomic.Bool` is written by the test goroutine and read by the manager's Run goroutine (inside `Connected`/`Outbound`); `atomic.Bool` is the correct primitive. The `signalDrop` channel is cap-1, non-blocking send, single observer (the test).
- **Capturing logger buffer** is written by the Run goroutine (via `slog`) and read by the test goroutine after the failure is observed; guard with a mutex (`safeBuffer` precedent) — `slog.TextHandler` is not concurrency-safe over a bare `bytes.Buffer` under `-race`.
- **Non-parallel.** These tests wait on real timers and (case 1) assert exact emit counts. Do **not** `t.Parallel()` them — matching the in-process timer tests' rationale.

---

## Error handling / failure modes

- **Flake risk — timer vs. test wall-clock.** The ≈1s `RekeyInterval` gives a wide margin over the sub-ms `down.Store` and over WS round-trip latency; keep read timeouts generously above the configured interval (e.g. 3s reads for a 1s interval). Never assert "no second emit" with a timeout shorter than the interval.
- **Wrong-frame interleave.** After open, the first binary→phone frame in case 1 is the `rekey_request`; there is no app traffic racing it (unlike v1's spinner). A straight `readInnerFrame` is safe. If a baseline round-trip precedes it, drain that reply first.
- **Case 2 double-close.** `closeWith` is idempotent (`state==V2StateClosed` guard, `v2session.go:3027`); the reply-timeout close is the only close. No special handling.
- **`errSimulatedDown`** is a local sentinel in the test file; the manager only checks `err != nil` in `send()` (`v2session.go:3079`), so any non-nil value drops-and-debug-logs exactly like `transport.ErrDisconnected`.

---

## Testing strategy

One new file `internal/e2e/relay_v2_rekey_test.go` (`//go:build e2e`, `package e2e`). One top-level `TestRelayV2_Rekey` with subtests, mirroring `TestRelayV2_Handshake`'s structure:

- `scheduled_happy_path_and_rearm` (AC1 + AC2) — one flow: first scheduled `rekey_request{scheduled}` → phone re-handshake → round-trip under new keys → second scheduled `rekey_request` at the shrunk interval.
- `phone_initiated_spontaneous` (AC3) — cold `noise_init` in open state → accepted → round-trip under new keys.
- `transport_down_at_boundary` (AC4) — the down-shim flow above; `main`-shape assertions with the `// AC4 REBASE POINT (#912)` block.

Run: `go test -race -tags e2e ./internal/e2e/ -run TestRelayV2_Rekey`. The reviewer must run the `e2e`-tagged build and confirm non-vacuity (green only when rotation actually occurred). Also run `go test -race ./internal/relay/` to confirm the config-field seam did not regress the in-process timer suite.

Production seam edits: `internal/relay/v2session.go` only (2 struct fields + 2 one-line reads). No `cmd/pyry` change, no `fakerelay` change.

---

## Open questions

1. **#912 rebase direction (AC4).** This spec writes AC4 for the current `main` (#912 not landed — confirmed: `emitRekeyRequest` has no `transportDown()` gate). If #912 lands first, the developer flips the `// AC4 REBASE POINT` block to the survive-and-retry shape. No blocking edge is wired (ticket's explicit design intent; the file regions are disjoint — #912 edits `emitRekeyRequest`/`handleWake`/`handleManualRekey`, this ticket edits the config struct + `armRekeyTimer`/`armRekeyReplyTimer`, so no textual conflict is expected even if both land). The overlap check flagged only `feature/449`, which is **CLOSED** (a stale orphan branch of the already-merged responder ticket) — a documented false positive, not in-flight work.
2. **Case-2 4426 wire observation vs. log-only.** The design delivers *both* (log line carries `close_code=4426`; the phone also observes the WS close after `down.Store(false)`). If the wire-4426 delivery proves timing-fragile in CI, the captured-log assertion (`event=noise.rekey_failed`, `close_code=4426`) alone is sufficient and fully deterministic — treat the wire-4426 as the corroborating, not the primary, assertion.

---

## Security review

This ticket is `security-sensitive` because it exercises **crypto key rotation over the wire** (a full Noise_IK re-handshake + atomic CipherState swap + peer-static continuity, ADR 024 § Re-key). The label tracks the design surface — the tests assert a security property (rotation actually happens; the old channel is retired). Walking the categories:

**Trust boundaries / input validation.** No new untrusted input reaches production. The two new `V2SessionConfig` fields are set only by in-process test/harness code (Go call sites in the same binary), never from the wire, a file, or an operator-supplied string. Production (`cmd/pyry`) leaves both zero → the 1h/30s package defaults. There is no parse, no bounds surface, no new wire vocabulary. The re-key protocol itself (`handleRekeyInit`, peer-static continuity at `v2session.go:1506+`) is **unchanged** — this ticket adds a caller (the e2e test), not a new code path.

**Crypto posture — the load-bearing concern.** The security *value* of this ticket is non-vacuity: a re-key test that stays green when no rotation happened would let a rotation regression ship silently (the exact WireGuard-style failure ADR 024 § "1-hour re-key cadence" exists to prevent). The design pins rotation with the **round-trip-under-new-keys** assertion — sealing under `initSend2` and decrypting the reply under `initRecv2`. This is load-bearing precisely because a responder that failed to swap CipherStates would reject the new-key frame at 4421; a green round-trip is therefore proof the old keys were retired. Peer-static continuity is exercised implicitly (the phone reuses `initPriv`, so the responder's continuity check passes; the negative — different static → 4426 — is already covered in-process at `v2session_test.go:1455` and is out of scope here). Nonce discipline: case 2 reproduces the #912 nonce-burn faithfully (the emit still seals under `main` before the down-drop), though the burn is asserted via its *symptom* (4426 + `noise.rekey_failed`) since the CipherState nonce is not observable from `package e2e` — the direct nonce assertion belongs to the in-process #874/#912 tests.

**New attack surface from the config fields.** None that the existing default does not already permit. `RekeyInterval` is a timing knob, not a key or a policy gate: a small value only *increases* rotation frequency (strictly more forward secrecy, more overhead); there is no value that disables rotation more than the shipped 1h default already implies, and production never sets it. It does not touch key material, the peer-static pin, the AEAD, or the fail-closed defaults. Net-neutral on the production security posture.

**DoS / resource.** No new unbounded allocation; the tests create one session each and tear it down via `t.Cleanup`. The down-shim's cap-1 signal channel and mutex-guarded log buffer are bounded. Timers are stopped by `closeWith`/`rekeyComplete` as today.

**Error handling / info leak.** The capturing logger is test-local (never shipped). The `noise.rekey_failed` log line asserted here is the existing production line (`v2session.go:985-989`); this ticket does not add or alter any log. No secret is logged (the line carries `conn_id` + `close_code`, no key material).

**Verdict: PASS.** The one production change is an additive, test-only-populated timing seam with no wire exposure and no effect on the default security posture; the test design is non-vacuous on the rotation property it certifies.
