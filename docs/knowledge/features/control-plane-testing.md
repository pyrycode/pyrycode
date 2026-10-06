# Control plane testing

Tests for the [control plane](control-plane.md) cover wire contracts, provider
boundaries and daemon lifecycle ordering.

`server_test.go`, `logs_test.go` exercise the full surface with `fakeResolver` + `fakeSession` test doubles satisfying `SessionResolver` + `Session`. `recordingResolver` records both `Lookup` and `ResolveID` arguments.

`pairing_test.go` treats the bearer result as a boundary, not ordinary response data. `TestMintPairing_WireRoundTrip` compares exact raw JSON for both boolean values and the typed pairing reply, while `TestProtocol_SessionsRoundTripBackCompat` proves the new optional outer fields did not change older verb bytes. Provider tests assert exactly one call with both arguments and use distinct success-pairing and provider-error sentinels to prove that only the successful return value can contain the credential; neither sentinel may enter control logs, response errors, transport diagnostics, or any error-path result.

The daemon-side pairing tests add the construction proof that an isolated
control-server fake cannot provide: `TestLocalPairingProviderWiredFromRelayConstructionToControl`
pins the provider from `startRelayV2` through `startRelay` to
`runSupervisor`'s `SetPairingProvider` call. The companion two-socket test uses
distinct identities, keys, relay URLs, and registry paths. Its cross-registry
negative assertion searches by token hash rather than by device label; a
label-based lookup alone would stay green if the credential were written into
the wrong registry under another name. Lock, malformed-load, and save failures
also prove the fixed client error and inspect both daemon logs and diagnostic
bundle logs for credential-like sentinels.

Composition-root AST guards inspect `runSupervisor` itself. Moving its wiring
into a helper hides that wiring from their assertions; the private optional
delivery constructor lets shutdown tests pause I/O while preserving the guarded
production symbol and its call sites.

The pairing timeout tests cover both sides of the liveness contract: a silent peer must terminate at `DialTimeout` even when the caller allows longer, and an already-entered provider held past an earlier caller deadline must not keep the client blocked. The held provider is explicitly released so the synchronous server handler can drain; a green client-deadline assertion alone would not prove server shutdown remains finite.

For update providers, a concurrent status response alone cannot prove execution
outside `Server.mu`: the status arm never takes that lock.
`TestUpdateWhenIdle_ResponseOutlastsHandshake` also completes
`SetUpdateWhenIdleProvider` while the provider is held, then accepts its decision
after the five-second handshake window. This tests lock release as well as the
longer response window. `TestUpdateWhenIdle_SilentPeerCeiling` gives the caller a
longer deadline to prove the helper's own 70-second ceiling;
`TestUpdateWhenIdle_CallerStopsWaiting` tests earlier deadlines and cancellation
after provider entry. Held providers are released before draining the server,
and custom peers are drained too. Malformed-result tests check both provider and
wire boundaries, including error-plus-success replies; exact request/decision
encodings and `TestProtocol_SessionsRoundTripBackCompat` pin the wire contract.

## Startup readiness and ownership

`TestControlStartupReadinessCreation` uses a real Unix socket, persistent pool
and hermetic runners. Its held-readiness arm queues `sessions.new` after
`Listen` but before `Pool.Run`, requiring no response, bootstrap-only memory
and byte-identical registry contents. Starting the pool releases that same
request; the response id must exist in memory and exactly once on disk, alongside
the bootstrap. The already-ready arm covers immediate serving and creation.
Holding readiness open makes the startup boundary independent of pool scheduling;
a socket connection alone cannot prove handlers are safe to enter.

`TestControlStartupReadinessCancellation` leaves readiness open and cancels the
daemon context, requiring the startup join to return `context.Canceled`. A
replacement listener must still get `ErrInstanceRunning` until the original
server is explicitly closed; only then may it rebind. Returning from the wait
must not itself release ownership ahead of delivery writers.

Helper tests alone stay green if production stops calling the helper.
`TestControlStartupReadinessWiring` inspects `runSupervisor` through
`formattedGoFunc`, requiring `serveControlWhenReady(ctx, pool.Ready(), controlCtx,
ctrl)` and rejecting a direct `ctrl.Serve` call. The held-creation test also
fails against an ungated helper: `sessions.new` returns `ErrPoolNotRunning`
after memory and registry bytes have already changed. Pair this regression with
`TestChannelDelivery_ShutdownRetainsOwnershipUntilWritersStop` for the existing
[writer-before-control shutdown contract](control-plane.md#lifecycle).
