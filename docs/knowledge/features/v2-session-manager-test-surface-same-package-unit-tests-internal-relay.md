# Same-package unit tests (`internal/relay/v2session_test.go`, no WS)

Each test constructs a `V2SessionManager` with an in-memory `outbound` recorder (mutex-guarded slice; goroutine-safe) and a `devices.Registry` built inline.

`driveToOpen` returns only after completed admission. A recorded, authenticated
`noise_resp` alone is insufficient: `handleNoiseInit` publishes it before
persistence, `V2StateOpen` and push-queue registration, so an immediate `Push`
can otherwise return `ErrConnNotFound`. After asserting exactly one handshake
envelope and authenticating it with `Initiator.ReadResp`, the fixture calls
`waitForOpenAdmission` with a two-second context derived from `t.Context()`.
One `ActiveConns` snapshot, serialized through `Run` after the handshake handler,
must contain the connection; cancellation or missing admission fails the fixture.
Manager cleanup is registered immediately after startup, before any assertion
can fail. The caller signature and returned cipher states are preserved; keep
decrypting captured application frames in capture order to authenticate the
nonce sequence. See the [controlled admission regression](v2-session-manager-test-surface-same-package-unit-tests-internal-relay-handshake-and-rekey.md#admission-readiness).

A later handshake proves only `Run` progressed, not that a silent worker handler
finished. Wait for a subsequent reply on the same connection's FIFO worker before
asserting no calls or replies; `TestV2Session_StopBackgroundTask_Contract` uses a
`send_message` ack (#2791).

Rejection fixtures must start with one intended device record: `Registry.Add`
appends, and `Registry.Validate` uses the first matching token hash. Appending
an expired or differently key-bound record beside an existing valid record
still authenticates against the first one. Assert the expected rejection event
as well as the encrypted error and close shape, so an omission assertion cannot
pass without reaching the intended denial. `TestV2Session_HelloAckWorkspaceRoot_Rejected`
uses this pattern for token, install-binding and version refusals (#2760).

Reproducing a load-sensitive flake in this suite: a `GOMAXPROCS`/`-count` contention ladder is unreliable, and a whole-package `-count=10` run under `GOMAXPROCS=1` can hit Go's own 10-minute `-test.timeout` before it ever reproduces. A production-side `time.Sleep` mutant that widens the specific hazard window, run via `go test -overlay` pointed at a pre-fix test file (`git show <ref>:<file>`), reproduces deterministically and names the mechanism directly instead of waiting for the scheduler to expose it.

## Test surfaces

| Document | Coverage |
| --- | --- |
| [Handshake, rekey and peer-close tests](v2-session-manager-test-surface-same-package-unit-tests-internal-relay-handshake-and-rekey.md) | Authentication, encrypted dispatch, rekey timers and peer-close ordering. |
| [Push delivery, reconnect recovery and inbound-control tests](v2-session-manager-test-surface-same-package-unit-tests-internal-relay-delivery-and-control.md) | Push/backpressure, capabilities, explicit reconnect recovery, complete live-state deadlines and inbound controls. |
| [Worker isolation and request-fixture tests](v2-session-manager-test-surface-same-package-unit-tests-internal-relay-workers-and-request-fixtures.md) | Handler isolation, asynchronous request waits, poisoned refusals and cleanup ordering. |

### E2E (`internal/e2e/relay_v2_handshake_test.go`, build tag `e2e`)

Spins up `fakerelay` (now with both `/v1/server` and `/v2/server`), wires `relay.Connect` + `V2SessionManager` **inline** (no daemon — this is the manager-in-isolation harness; the daemon-level wiring is covered separately by `relay_v2_daemon_test.go`, [#549](../codebase/549.md)), dials a `fakephone` against `/v1/client` (unchanged routing wire under v2), and drives a Noise_IK handshake from the phone side.

- `testV2HappyPath` — paired device → phone observes a `noise_resp` frame, decrypts hello_ack, then no further traffic.
- `testV2BadToken` — unpaired device → phone reads the AEAD-sealed `auth.invalid_token` `noise_msg`, then `Read` errors with `LastCloseStatus() == 4401`.
- `testV2IKReject` — phone sends an invalid noise_init (random bytes, no real IK message 1) → phone's next read errors with close code 4426. No prior frame from binary.
- `testV2EncryptedEchoRoundTrip` (#446) — paired-device handshake to open with a stub handler registered against `TypeListConversations`; phone-side AEAD-seal request, read one inner frame back, decrypt with `initRecv`, assert the inner envelope's `Type`/`InReplyTo`/`Payload` match the handler's reply.
- `testV2TamperedNoiseMsg_4421` (#446) — phone sends a `noise_msg` with one byte flipped after handshake; phone observes `LastCloseStatus() == 4421`. The "fresh `noise_init` on the same `conn_id`" assertion lives in the unit test layer because `fakerelay` assigns a new `conn_id` per dial.

The gating-invariant test and the post-AEAD-failure fresh-handshake test are unit-shape only — the e2e suite covers the natural inbound flows.
