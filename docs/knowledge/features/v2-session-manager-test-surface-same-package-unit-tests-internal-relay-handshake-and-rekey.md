# Handshake, rekey and peer-close tests

[Same-package test overview](v2-session-manager-test-surface-same-package-unit-tests-internal-relay.md) describes the shared harness.

- `TestV2Session_HappyPath` — paired-device `hello` in early-data → state advances to `V2StateOpen`; `noise_resp` envelope on `Outbound` carries hello_ack; CipherStates non-nil; no close-code emitted.
- `TestV2Session_BadToken_AEADErrorThen4401` — unknown-token hello → exactly one outbound envelope with `CloseCode == 4401`, frame is a `noise_msg`-wrapped AEAD-sealed error envelope; the initiator side decrypts the wrapped envelope and the test asserts `Code == auth.invalid_token`. State = closed.
- `TestV2Session_IKReject_4426` — `noise_init` carrying random bytes (no real IK message 1) → exactly one outbound envelope with `CloseCode == 4426`, no Frame body. State = closed.
- (Removed in #453: `TestV2Session_NoiseInitAfterOpen_4421` pinned the behaviour that re-key responder #453 intentionally changes; replacement coverage is the three `TestV2Session_RekeyResponder_*` tests below.)
- `TestV2Session_Gating_NoiseMsgInHandshakeComplete_4401` — directly assign `s.state = V2StateHandshakeComplete`, `s.send/recv = <CipherStates from a real adjacent handshake>`, feed a `noise_msg` whose plaintext is a non-hello envelope. Asserts: exactly one outbound envelope with `CloseCode == 4401`, frame is AEAD-sealed `error{auth.invalid_token}`. **Structurally proves the "handler chain unreachable from handshakeComplete" invariant** — the regression guard for any future refactor that might add a v2→handler edge.
- `TestV2Session_OutOfStateRejections` — table-driven over the remaining cells, each case now carrying its own `wantCode` rather than one shared expectation (#2488): malformed JSON / unknown `Type` / bad `v` / unexpected `noise_resp` → 4421 in each state; `noise_msg_before_handshake` (an `awaitingInit` conn receiving a `noise_msg`) → the retryable 4410 (`StatusSessionGone`), its one case that differs from the rest of the table.
- `TestNewV2SessionManager_ConfigValidation` — panics on nil `Frames` / nil `Logger`; wrapped errors on nil `Outbound` / nil `Devices` / empty `ServerID` / wrong-length `StaticPriv`. `Handlers` is optional — no new validation case.
- `TestV2Session_SweptThenNoiseMsg_4410_ThenReHandshakes` (#2488) — the end-to-end proof that a client whose session the daemon dropped can recover on its own: `setIdleTimeout` to a sub-second value, `driveToOpen` on one conn id, wait for the `4408` sweep via `waitForConnClose`, then send a frame sealed under the initiator's still-live send `CipherState` (`sealAppFrame`/`wrapInnerFrame`) on the SAME conn id and assert a `4410` close with `Frame == nil`, then `handshakeConnToOpen` a SECOND conn id against the same still-running manager to prove it still serves a fresh handshake. In-package because `idleTimeout` is a package var only an in-package test can shrink; not `t.Parallel()`, matching the other `setIdleTimeout` tests. Deterministic rather than timing-dependent: `closeWith` deletes from `m.sessions` before the close envelope is emitted, and both the sweep and frame dispatch run on `Run`, so waiting on the recorded `4408` proves the delete already happened before the sealed frame is sent — it is guaranteed to hit `handleFrame`'s lazy-create path into a fresh `V2StateAwaitingInit` session, not the `V2StateClosed` drop-silently branch, which would have made the test a silent no-op.

## Admission readiness

`TestDriveToOpen_WaitsForAdmission` holds the first outbound callback after
recording `noise_resp`, before returning to `handleNoiseInit`. Within a
`testing/synctest` bubble, 10 ms of virtual time lets the recorder poll run and
`synctest.Wait` establishes durable blocking: the fixture must still be waiting
for admission. This schedules the observation without a wall-clock sleep as a
readiness condition. Release permits admission; two subsequent pushes must
authenticate and decrypt in order with the returned receive cipher.

If the fixture returns early, the hold excludes teardown and orders direct
inspection of the Run-owned session; queue presence is inspected under `pushMu`.
Removing only the admission wait demonstrated a present
`V2StateHandshakeComplete` session, absent queue and `ErrConnNotFound` on the
first push. An absent queue by itself cannot distinguish admission from teardown.
Cleanup always releases outbound before joining the fixture and stopping/joining
the manager, including on failure. `TestWaitForOpenAdmission_Context` uses no
running manager and virtual time to check both the two-second deadline and
earlier caller cancellation.

The [investigation evidence](../../specs/architecture/3485-relay-fixture-admission.md#revisions)
retains 20/20 controlled failures without the barrier and repaired passes.
The original transport test still passed 20/20 without the barrier; this proves
the fixture dependency while leaving the historical failure's cause unresolved.

## Open-state dispatch

- `TestV2Session_OpenState_EncryptedRoundTrip` — paired-device happy path through `dispatchAppFrame`. Stub handler keyed by `TypeListConversations` replies via `c.Reply`; phone-side decrypt of the captured `noise_msg` matches the handler's payload, `InReplyTo` echoes the request id, session state stays `V2StateOpen`.
- `TestV2Session_OpenState_TamperedNoiseMsg_4421` — flip one byte of a real ciphertext → exactly one outbound envelope with `CloseCode == 4421` and nil `Frame`, the registered handler's `atomic.Bool` flag stays false (handler chain structurally unreachable), and `mgr.sessions[v2TestConnID]` is absent (AC #3 — `closeWith` deletion).
- `TestV2Session_OpenState_FreshNoiseInitAfterAEADClose` — companion to the prior test. After 4421+cleanup, a second `noise_init` on the same `conn_id` completes a fresh handshake; a ciphertext sealed under the OLD `initSend` fails against the new session's `s.recv` (deterministic proof that the post-cleanup session is fresh `awaitingInit`-then-`open` with no carry-over CipherStates).
- `TestV2Session_OpenState_UnknownEnvelopeType_SealedUnsupportedReply` — open-state envelope with `Handlers = nil` → AEAD-sealed `Envelope{Type: TypeError, Payload.Code: CodeProtocolUnsupported}`. State stays `open`.
- `TestV2Session_OpenState_MalformedInnerEnvelope_SealedMalformedReply` — open-state envelope whose AEAD plaintext is raw garbage → AEAD-sealed `Envelope{Type: TypeError, Payload.Code: CodeProtocolMalformed}`. State stays `open`.
- `TestV2Session_OpenState_HandlerAuthDevice` — handler captures `c.Auth().Name` from inside the dispatch closure; asserts the matched-device snapshot captured during handshake (`s.device`) reaches the handler via `*dispatch.Conn.Auth()`.

## Peer-static capture

- `TestV2Session_InitialHandshake_CapturesPeerStatic` — drives a paired-device handshake to `V2StateOpen` via `driveToOpen`, then white-box-asserts `mgr.sessions[v2TestConnID].peerStatic == initPub` and `len(...) == noise.KeyLen`. The length check is the regression guard against an empty-slice silently passing a future `bytes.Equal(nil, nil)` comparison if the capture site is skipped. Pins the capture invariant for the inert-in-this-slice field that #453 will read.

## Control-envelope interception

- `TestV2Session_OpenState_RekeyRequest_Intercepted` — paired-device handshake to open → AEAD-sealed `{type: "rekey_request", payload: {reason: "scheduled"}}` → stub handler's `atomic.Bool` stays false (application chain unreachable), session stays `V2StateOpen`, no outbound close envelope. Structural proof that the probe in `dispatchAppFrame` runs before `dispatch.Route`.
- `TestV2Session_OpenState_RekeyRequest_UnknownReasonTolerated` — `{reason: "lunar-eclipse"}` → buffer-logger captures `level=WARN`, `event=v2.rekey.request.received`, `reason=lunar-eclipse`. Session stays open, no outbound frame. Forward-compat posture for unknown reasons.
- `TestV2Session_OpenState_RekeyRequest_RecognisedReasons` — table-driven over `{scheduled, manual, compromise}`; each subtest asserts `level=INFO`, the correct `reason` field, no close, no outbound frame, state stays open.

## Responder rekey

- `TestV2Session_RekeyResponder_HappyPath_RoundTripUnderNewKeys` — drives the paired-device handshake to open, constructs a SECOND `noise.Initiator` reusing the SAME `initPriv` (peer-continuity invariant), feeds a fresh `noise_init` with empty early-data, asserts the rekey `noise_resp` returns under `CloseCode == 0`, then AEAD-seals a `TypeListConversations` request under the NEW `initSend2` and asserts the reply decrypts cleanly under the NEW `initRecv2`. Post-stop assertions: `state == V2StateOpen`, `s.device.Name` preserved, `bytes.Equal(s.peerStatic, initPub)` (#452 lifetime contract honoured across re-key). Pins AC #1 + AC #5 — both directions of the swap are wired and the device + peer-static snapshots survive.
- `TestV2Session_RekeyResponder_DifferentPeerStatic_4426` — drives to open, then feeds a fresh `noise_init` from a DIFFERENT keypair. Exactly one additional outbound envelope with `CloseCode == 4426` and nil Frame; `mgr.sessions[v2TestConnID]` absent; log buffer contains `event=v2.handshake.reject.ik_failure` + `reason=rekey_peer_static_mismatch`; **the reject log line specifically does NOT contain `device_name`** (per-line substring check — a global check would false-positive on the initial `v2.handshake.accept`). **Security-load-bearing test** for the Threat #3 residual-risk claim.
- `TestV2Session_RekeyResponder_OldKeyFrameAfterSwap_4421` — drives to open, stashes a ciphertext sealed under `sess.initSend` BEFORE the re-key, completes a successful re-key with the same static, then feeds the stashed stale frame. The inherited #446 tampered-frame branch fires against the post-swap `s.recv` → `CloseCode == 4421`, nil Frame, session removed. **No new code path** — verifies the inheritance at the new authenticated-state boundary.

## Initiator rekey

- `TestV2Session_RekeyInitiator_Emit_ReArmViaResponder` — joint coverage of AC #5 bullets 1 + 2. Substitutes `rekeyInterval = 20*time.Millisecond` / `rekeyReplyTimeout = 500*time.Millisecond`; drives to open; waits for the FIRST emit and decodes the inner envelope under `sess.initRecv` (`Type == protocol.TypeRekeyRequest`, `payload.reason == "scheduled"`); constructs a second `noise.Initiator` reusing the SAME `initPriv` and feeds a fresh `noise_init`; reads the rekey `noise_resp` via `initiator2.ReadResp` to derive `initRecv2`; waits for the SECOND emit and decodes under the post-swap `initRecv2`. State assertions after stop: `state == V2StateOpen`, `awaitingRekeyReply == true` (no second responder cycle ran).
- `TestV2Session_RekeyInitiator_ReplyTimeout_4426` — substitutes `rekeyReplyTimeout = 40*time.Millisecond`; uses `bufferLogger()`; drives to open; does NOT feed a noise_init reply; waits for the close envelope via the new `waitForOutboundCount(t, rec, n, deadline)` helper. Asserts: third outbound envelope has `CloseCode == uint16(StatusHandshakeFailure)` and nil Frame; `mgr.sessions[v2TestConnID]` absent after stop; log buffer contains `event=noise.rekey_failed`, `close_code=4426`, `conn_id=…`; the `noise.rekey_failed` line specifically does NOT contain `err=` (per-line substring check — anti-leakage of flynn-noise error text).
- `TestV2Session_RekeyInitiator_TimerCleanup_NoGoroutineLeak` — two sub-tests: `close_via_manager_exit` (drive to open, stop immediately before the rekeyTimer fires) and `close_via_reply_timeout` (drive to open, wait for the close envelope after the reply-timeout, then stop). Both assert `runtime.NumGoroutine()` returns to within +1 of the pre-test baseline after `runtime.Gosched(); runtime.GC(); time.Sleep(20ms)`. Pins the goroutine-lifetime invariant for `time.AfterFunc` callbacks under the wake-channel design.

## Manual rekey

- `TestV2Session_RekeyManual_HappyPath_EmitsManualReason` — drives `driveToOpen`, calls `mgr.Rekey(ctx, v2TestConnID)`, waits for the second envelope (initial `noise_resp` + manual emit), AEAD-decrypts the second envelope under `sess.initRecv`, asserts `Type == protocol.TypeRekeyRequest` and `payload.reason == "manual"`, and asserts the log buffer contains `event=v2.rekey.emit` + `reason=manual` + `conn_id=<v2TestConnID>`. Substitutes long `rekeyInterval`/`rekeyReplyTimeout` (10 s each) so the scheduled boundary cannot fire during the test window.
- `TestV2Session_RekeyManual_UnknownConn_ReturnsErrConnNotFound` — manager with `Run` started but `m.sessions` empty; `mgr.Rekey(ctx, "this-conn-does-not-exist")` returns an error that satisfies BOTH `errors.Is(err, relay.ErrConnNotFound)` AND `errors.Is(err, control.ErrConnNotFound)` (the wire-mapping invariant — the `%w` wrap survives both levels). `rec.snapshot()` is empty (no outbound side-effect).
- `TestV2Session_RekeyManual_AlreadyAwaitingReply_ReturnsErrSessionNotOpen` — long `rekeyReplyTimeout` so the first emit's awaiting-reply window doesn't auto-close; first `Rekey` succeeds → wait for the manual emit to land in `rec` → second `Rekey` returns `errors.Is(err, relay.ErrSessionNotOpen)`. Asserts only ONE manual emit envelope is recorded (no double emit), session state remains `V2StateOpen`, and `s.awaitingRekeyReply` remains true.
- `TestV2Session_RekeyManual_RebasesScheduledTimer` — substitutes `rekeyInterval = 100ms` / `rekeyReplyTimeout = 1s`. Drives to open, immediately calls `Rekey`, waits for the manual emit at T ≈ small_delta and asserts `payload.reason == "manual"`. Drives a fresh responder cycle (same `initPriv` → peer-static continuity passes), captures `initRecv2`. **Original-boundary check**: at T ≈ rekeyInterval + jitter (130 ms after open — past the original scheduled boundary, well before T_rekeyComplete + rekeyInterval), asserts `rec.snapshot()` length is still 3 (manual + responder reply + nothing else). **New-boundary check**: at T_rekeyComplete + rekeyInterval + jitter, asserts a fourth envelope arrives whose payload decodes (under post-swap `initRecv2`) to `TypeRekeyRequest` with `reason == "scheduled"`. The original-boundary check is the load-bearing negative assertion — it directly pins "no stale scheduled emit lands between manual emit and the re-based scheduled boundary."

All four #462 tests are explicitly NOT `t.Parallel()` — they mutate package-level `rekeyInterval` / `rekeyReplyTimeout` vars (same posture as the other rekey tests).

## Peer-close teardown

Peer-close teardown (#2601): `TestV2Session_PeerClose_TearsDownWithoutReply` opens two conns, sends a close notice (carrying a junk `frame`) for one, and asserts `ActiveConns` reports only the untouched conn, nothing beyond the two `noise_resp`s is ever recorded (the junk frame would have earned a 4421 if it were decoded), and `m.sessions`/`m.queues` drop the key after stop; `TestV2Session_PeerClose_UnknownConnIgnored` asserts a notice for a conn with no session creates none and sends nothing. Both use an **unbuffered** `Frames` channel deliberately: with a buffered channel, `Run`'s select could service an `ActiveConns` snapshot request before it drains a queued close notice, making "send notice, then read `ActiveConns`" racy. An unbuffered send returns only once `Run` has taken the envelope, and `Run` finishes handling it before selecting again, so the ordering the test depends on is real rather than assumed.
