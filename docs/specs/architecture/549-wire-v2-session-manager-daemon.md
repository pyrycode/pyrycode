# Spec — cmd/pyry: wire `V2SessionManager` into the daemon (Mobile Protocol v2 cutover) (#549)

## Files to read first

Turn-1 reading list. Paths + line ranges + what to extract.

- `cmd/pyry/relay.go:64-207` — `startRelay`: the single branch point. The whole v1 path (`dispatch.New` + `authGate` + `Register`×3 + assistant-turn bridge + dispatcher/forwarder/wait goroutines + cleanup) lives here. You add a v2 branch around it; the v1 path is touched only to thread one new param and to share the `waitDone` goroutine.
- `cmd/pyry/relay.go:25-43` — `authGate`. **v1-only.** Do NOT reuse it on the v2 branch — v2 token auth happens *inside* `V2SessionManager` (via `Devices.Validate` on the hello early-data), not as a dispatcher first-frame gate.
- `cmd/pyry/main.go:481-488` — the **sole** `startRelay` call site. Where you read the new env switch (next to `allowInsecure`) and thread it in.
- `cmd/pyry/pair.go:60-71` — `resolveStaticKeyBaseDir()`. The daemon's v2 branch MUST call `keys.LoadOrCreate` with the **same** `(baseDir, sanitizeName(name))` pair that `pyry pair` uses, or the daemon's static private key won't match the public key the phone pinned at pairing.
- `cmd/pyry/pair.go:183-220` — how `pyry pair` loads the static key (`keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(parsed.instanceName))`) and emits `payload.ServerStaticPubkey = base64.StdEncoding(pub[:])`. This is the producer side of the key the daemon must consume.
- `internal/relay/v2session.go:246-372` — `V2SessionConfig` field contract + `NewV2SessionManager` validation (panics on nil `Frames`/`Logger`; returns error on missing `Outbound`/`Devices`/`ServerID` or wrong-length `StaticPriv`). This is the exact struct you fill.
- `internal/relay/v2session.go:383-401` — `(*V2SessionManager).Run` lifecycle: returns `nil` when `Frames` closes, `ctx.Err()` on cancel. No goroutines outlive `Run`.
- `internal/relay/v2session.go:281-298` — `V2SessionConfig.Handlers` doc: same `map[string]dispatch.Handler` shape and same handler values as v1; nil/empty → every open-state app envelope falls through to a sealed `protocol.unsupported`.
- `internal/e2e/relay_v2_handshake_test.go:55-198` — `startV2Harness` is the **inline** wiring to *mirror* (NOT to reuse for the new test — AC#4 demands a spawned daemon). The package-level helpers in this file ARE reusable: `sendNoiseInit`, `sendNoiseMsg`, `readInnerFrame`, `buildHelloEarly`, and `driveHandshakeToOpen` (the last takes `*v2Harness`; you write a thin daemon-flavoured variant that takes `pubKey []byte` directly).
- `internal/e2e/relay_roundtrip_test.go:31-154` — the daemon-level template: `RunBareIn "pair"` → `decodePairPayload` → seed `conversations.json` → start daemon w/ relay → wait binary hello → dial phone → `list_conversations` round-trip. Steps 1-2 are exactly what the v2 test mirrors (over the encrypted channel).
- `internal/e2e/relay_auth_test.go:22-111` — `StartInWithEnv` daemon-spawn pattern + `readPersistedServerID` + the WS-close assertion idiom. This test already proves the v1 path rejects unpaired tokens unchanged (relevant to AC#2).
- `internal/e2e/pair_test.go:272-290` — `decodePairPayload(t, stdout)` returns a `pair.Payload`; use `.Token` (for `Devices.Validate`) and `.ServerStaticPubkey` (base64-decode → the initiator's responder-static pubkey).
- `internal/keys/static_key.go:57` — `(*StaticKey).PrivateKey() [32]byte`. Slice it (`priv[:]`) for `StaticPriv`; `noise.KeyLen == 32`.
- `internal/e2e/harness.go:228-256` — `StartInWithEnv(t, home, extraEnv, extraFlags...)`: the env-injecting daemon spawn (claude = `/bin/sleep infinity`; no fake-claude needed for `list_conversations`).

Docs (codegraph won't surface these):
- `docs/knowledge/features/v2-session-manager.md` — the "not yet wired" line this ticket invalidates. Documentation phase updates it post-merge (NOT a developer deliverable — see Testing strategy).
- `docs/knowledge/codebase/436.md` — `pyry pair preflight`'s role as the operator pre-flip safety check.

## Context

Mobile Protocol v2 (Noise_IK E2E) is fully implemented and e2e-tested on the binary side (`internal/relay/v2session.go`, `internal/noise`; #430-436 / #450 / #453 / #462), but **never wired into the daemon**. `cmd/pyry/relay.go:startRelay` builds only the v1 path (`dispatch.New` + `authGate`). `relay.NewV2SessionManager` has no caller under `cmd/`. A real daemon therefore decodes a phone's `noise_init` as an unknown v1 envelope type and the phone can never complete its handshake.

This slice flips the cutover. It introduces **one explicit operator switch**; with the switch on, the daemon wires `V2SessionManager` against `conn.Frames()` instead of the v1 dispatcher, registering the **same three handlers**. With the switch off (default), the v1 path is byte-for-byte unchanged.

**Cross-repo prerequisite (not a GitHub blocker):** the relay↔binary `hello_ack` gap in the pyrycode-relay repo must have landed — a phone cannot reach the daemon until the relay completes the binary leg. Confirm before development; this is outside this repo's CI.

## Design

### The cutover switch — `PYRY_MOBILE_V2=1` (env var)

A single boolean env var, read in `runSupervisor` alongside `allowInsecure`, threaded into `startRelay` as a new `bool` parameter:

```
v2Enabled := os.Getenv("PYRY_MOBILE_V2") == "1"
```

**Why an env var, not config/flag, not device count:**

- **Mirrors `PYRY_ALLOW_INSECURE_RELAY=1`** — the established precedent for an operator-set relay switch (env-only, no `config.Config` field, no CLI flag). Cutover is a one-time operator decision that persists naturally in the systemd unit / launchd plist `Environment=`. Adding a `config.Config` field or a `-pyry-*` flag would be a wider surface than the decision warrants (Simplicity First). Default-unset = v1, satisfying AC#2's "disabled is the default."
- **MUST NOT be driven by `preflightVerdict(len(registry.List()))`** (the load-bearing constraint from the ticket). `pyry pair preflight` passes only when **zero** devices are paired — but a v2 phone must have a paired device in that same registry to authenticate (`Devices.Validate(token)` inside the handshake). Selecting the protocol off the device count would make a v2 phone impossible to ever authenticate. `pyry pair preflight` is the operator's **pre-flip safety check** (run it → exit 0 means no v1 pairings will break → then flip the switch and restart); it is not the runtime selector. Devices carry no protocol-version field today.

The switch lives at the daemon boundary only. The relay URL/route (`/v1/server` vs `/v2/server`) is part of the operator-configured `RelayURL` (see `resolveRelayURL`) — the daemon does not synthesise it, and the switch does not change it. (Open question below covers the binary↔relay `hello`'s hardcoded `ProtocolVersions:["v1"]`.)

### `startRelay` branch

Signature gains one parameter (mirrors `allowInsecure bool`):

```
func startRelay(ctx, logger, instanceName, relayURL, version string,
    allowInsecure, v2Enabled bool, shutdown context.CancelFunc,
    convReg *conversations.Registry, sess handlers.TurnWriter,
    sup *supervisor.Supervisor, bridge *supervisor.Bridge) (cleanup func(), err error)
```

The **shared prologue is unchanged**: empty-`relayURL` short-circuit, `identity.LoadOrCreate`, `devices.Load`, the `allowInsecure` log, and `relay.Connect` → `conn`. After `conn` is established, branch:

- **`v2Enabled` true** → build and run the v2 manager (new helper `startRelayV2`, below). No `dispatch.New`, no `authGate`, no assistant-turn bridge, no separate outbound forwarder.
- **else (default)** → the existing v1 body verbatim. No behavioural change (AC#2).

The `conn.Wait()` classifier goroutine (`waitDone`: `ErrServerIDConflict` → `shutdown()`; ctx-cancel → debug; other → warn) is **identical for both paths** — keep it shared, after the branch, so the v2 leg inherits the 4409-shutdown contract for free.

### `startRelayV2` helper (the new leg)

A helper returning a drain func, so `startRelay` stays readable:

```
func startRelayV2(ctx context.Context, logger *slog.Logger, instanceName string,
    conn *relay.Connection, registry *devices.Registry, serverID identity.ServerID,
    convReg *conversations.Registry, sess handlers.TurnWriter) (drain func(), err error)
```

Behaviour (contract, not implementation):

1. **Load the static key** — `keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(instanceName))`. Same args as `pyry pair`, so the loaded private key derives the public key the phone pinned. On error, wrap (`load static key: %w`) and return — fail fast at startup, mirroring the `identity.LoadOrCreate` / `devices.Load` posture in the prologue.
2. **Derive `StaticPriv`** — `priv := staticKey.PrivateKey()` (`[32]byte`); pass `priv[:]`.
3. **Build the handler table** — identical handler *values* to the v1 path (AC#3), keyed by the same `protocol.Type*` constants:
   - `protocol.TypeListConversations: handlers.ListConversations(convReg)`
   - `protocol.TypeRegisterPushToken: handlers.RegisterPushToken(registry, resolveDevicesPath(instanceName), logger)`
   - `protocol.TypeSendMessage: handlers.SendMessage(sess, logger)`
4. **Construct the manager** — `relay.NewV2SessionManager(relay.V2SessionConfig{Frames: conn.Frames(), Outbound: conn.Send, StaticPriv: priv[:], Devices: registry, ServerID: string(serverID), Logger: logger, Handlers: <table>})`. Returns an error on bad config (return wrapped → fail fast); panics only on nil `Frames`/`Logger`, both always non-nil here.
5. **Run it** — one goroutine: `go func(){ defer close(mgrDone); if err := mgr.Run(ctx); err != nil { logger.Debug("relay: v2 manager run returned", "err", err) } }()`.
6. **drain** — `func(){ <-mgrDone }`. The caller `Close()`s `conn` *before* calling drain.

No log line should ever carry the token, key bytes, payload, or ciphertext (the package already enforces this internally; the daemon must not add a logging site that does).

### Concurrency model

v2-path goroutines:

- `conn.run` — owned by `relay.Connection` (unchanged).
- `mgr.Run` — the manager's single dispatch goroutine; owns all `V2Session` state with no locks (the loop is the lock). Calls `conn.Send` synchronously from this goroutine — **there is no separate outbound-forwarder goroutine** (the v1 path has one to drain `d.Outbound()`; v2's `Outbound: conn.Send` removes the need).
- `waitDone` — the shared `conn.Wait()` classifier.
- Transient `time.AfterFunc` rekey-timer callback goroutines spawned *inside* the manager — bounded, tied to the manager's Run-derived ctx; no daemon concern.

**Shutdown sequence** (SIGINT/SIGTERM cancels `ctx`, or `cleanup` runs on daemon exit):

```
ctx cancel OR conn.Close()
  → conn.run returns → close(conn.frames)
  → mgr.Run's `<-Frames` sees closed (or `<-ctx.Done`) → Run returns → close(mgrDone)
  → drain unblocks; waitDone unblocks once conn.run completes
```

`cleanup` ordering on the v2 branch: `_ = conn.Close()` → `drain()` (`<-mgrDone`) → `<-waitDone`. No goroutine outlives cleanup. (The v1 branch keeps its existing bridge-cleanup → dispatcherDone → forwarderDone → waitDone ordering.)

### Non-goals — restated so they are not pulled in

Each is a candidate follow-up; pulling any in breaks S.

- **Assistant-turn `message` fan-out to v2 phones.** The v1 path wires `startAssistantTurnBridge(ctx, sup, bridge, d, logger)` against the v1 dispatcher; `V2SessionManager` exposes no broadcast surface. The v2 branch does **not** call it — `sup` and `bridge` go unused on that branch (they remain in `startRelay`'s signature for the v1 path). PTY-output streaming (#311) is silently absent on v2; the AC require only inbound `list_conversations`.
- **`(*V2SessionManager).Rekey` → control socket** (`control.Rekeyer` / `Server.SetRekeyer`). Out of scope.
- **Device-version-tagged v1↔v2 coexistence.** A `devices` registry schema change → always-split. Not here.
- **`relay.Connection.handshake`'s `ProtocolVersions:["v1"]`** (`connection.go:268`) is **not changed.** The proven v2 e2e harness (`startV2Harness`) connects through this unchanged `["v1"]` hello and the phone handshake still succeeds — the binary↔relay hello is not load-bearing for the phone's Noise leg. See Open questions.

## Error handling

| Failure | Handling |
|---|---|
| `keys.LoadOrCreate` error (corrupt key file) | wrap `load static key: %w`, return → daemon startup fails fast (mirrors `identity.LoadOrCreate`/`devices.Load`). Key file is created by `pyry pair`; if v2 was enabled without ever pairing, `LoadOrCreate` mints a fresh key — harmless, but the empty registry then rejects every phone (correct). |
| `NewV2SessionManager` config error | wrap, return → fail fast. Unreachable in practice (all fields populated). |
| Per-frame protocol errors (bad token 4401, AEAD/state 4421, IK 4426) | fully owned by the manager; emitted as WS close codes internally. **No daemon-side handling.** |
| `conn.Wait()` → `ErrServerIDConflict` | shared `waitDone` goroutine calls `shutdown()` — same as v1. |
| `conn.Send` error from manager's `Outbound` | manager logs at debug and drops (its documented posture; transport reconnect recovers). |

## Testing strategy

New file `internal/e2e/relay_v2_daemon_test.go` (`//go:build e2e`), in package `e2e` so it reuses the existing `relay_v2_handshake_test.go` helpers and `decodePairPayload`. Two subtests:

**Subtest 1 — `v2_enabled_list_conversations_round_trip`** (AC#1, AC#4): the spawned-daemon happy path.
- `RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")`; `decodePairPayload(t, r.Stdout)` → `payload.Token` + `base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)` → `pubKey []byte`.
- Seed `~/.pyry/test/conversations.json` with one known conversation row (mirror `relay_roundtrip_test.go:47-53`).
- `StartInWithEnv(t, home, []string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"}, "-pyry-name=test", "-pyry-relay="+fr.URL()+"/v2/server")`.
- Wait for the binary↔relay hello (`fr.LastBinaryHello(serverID)` poll, 5s).
- `fakephone.Dial(ctx, fr.URL(), serverID, payload.Token, "phone-a")`.
- Drive the Noise_IK handshake to open via a thin daemon helper — `driveHandshakeToOpenDaemon(t, phone, pubKey, payload.Token)` — that mirrors `driveHandshakeToOpen` but takes `pubKey` directly (build `noise.NewInitiator(initPriv.Bytes(), pubKey)`, `WriteInit(buildHelloEarly(t, token))`, `sendNoiseInit`, read `noise_resp`, `ReadResp` → `(initSend, initRecv)`).
- Seal a `list_conversations` envelope with `initSend.Encrypt`, send as `noise_msg` (`sendNoiseMsg`), read the reply `noise_msg`, `initRecv.Decrypt`, assert it decodes to a `conversations` envelope (`protocol.TypeConversations`) with `InReplyTo` → the request id and the seeded conversation id present.

**Subtest 2 — `v2_disabled_does_not_engage_v2`** (AC#2): proves the switch off keeps the v1 path.
- Pair a device, start the daemon **without** `PYRY_MOBILE_V2` (v1 default), relay URL `/v1/server`.
- Phone sends a `noise_init` `InnerFrameV2` (raw bytes via `SendBytes`).
- Assert the phone receives a **v1 `hello_ack` envelope** (the first-frame auth gate accepted the paired token and replied) and **never a `noise_resp` inner frame** — demonstrating the v2 manager is not engaged and the v1 dispatch path handled the frame exactly as today. (The unpaired-token reject at 4401 is already covered by `TestRelay_AuthReject_4401`; this subtest's distinct value is the v2-off signal.)

Both subtests use stdlib `testing`, `t.Cleanup` for the fakerelay/phone/daemon teardown, and the existing close-status assertion idiom. No fake-claude child is needed — `list_conversations` reads the seeded registry; the default supervised `/bin/sleep infinity` suffices.

AC#3 (same three handlers, no behavioural change) is asserted structurally by subtest 1's successful `list_conversations` round-trip over the encrypted channel plus the handler table being the same constructor values as v1 (a unit-level assertion is unnecessary — the round-trip exercises the registration).

**AC#5 (cutover behaviour captured in a knowledge doc):** captured in *this spec* (switch mechanism, `pyry pair preflight`'s pre-flip role, v1+v2 coexistence out of scope) for the developer's turn. The durable knowledge doc is the **documentation phase's** deliverable post-merge (it updates `docs/knowledge/features/v2-session-manager.md`'s "not yet wired" line and writes `docs/knowledge/codebase/549.md` from the merged diff). The developer's worktree mutates only code, tests, and this spec file — do **not** add a `docs/knowledge/**` edit as a developer AC.

## Acceptance criteria (developer deliverables)

1. `PYRY_MOBILE_V2=1` env switch read in `runSupervisor` and threaded into `startRelay` as a `bool` param; default-unset selects the v1 path.
2. `startRelay` branches on the switch: v2 → `startRelayV2` (loads the static key via the same `keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(name))` as `pyry pair`, constructs `V2SessionManager` with `Outbound: conn.Send` and the three handlers, runs it, drains on cleanup); the shared `conn.Wait()` classifier and the entire v1 path are otherwise unchanged.
3. The v2 handler table registers `list_conversations`, `register_push_token`, `send_message` using the **same** `dispatch.Handler` constructor values as the v1 path.
4. `internal/e2e/relay_v2_daemon_test.go` (`//go:build e2e`): subtest 1 drives a spawned daemon (v2 enabled) through a real Noise_IK handshake over `fakerelay` `/v2/server` and round-trips `list_conversations` → `conversations`; subtest 2 confirms the v2 manager is not engaged when the switch is unset.
5. `go build ./...`, `go vet ./...`, `go test -race ./...`, and `go test -tags=e2e ./internal/e2e/...` all pass.

## Open questions

- **Binary↔relay `hello` `ProtocolVersions`.** `relay.Connection.handshake` hardcodes `["v1"]` (`connection.go:268`). The phone-side handshake works through this unchanged (the inline v2 e2e proves it), so this slice leaves it alone. If the production relay ever routes or rejects the binary leg on advertised version, a follow-up makes `ProtocolVersions` switch-aware. Filed as a note, not changed here (evidence-based: no observed failure).
- **Static-key existence when v2 is enabled without prior pairing.** `keys.LoadOrCreate` mints a key if absent, so the daemon starts cleanly, but no phone can authenticate until `pyry pair` runs (which is the operator's expected sequence anyway). No guard added.
- **`pyry pair preflight` as a hard gate.** Today it is advisory (operator runs it manually before flipping). Whether to make the daemon refuse to start with v2 enabled + non-empty v1 pairings is a deliberate non-goal — the registry has no protocol-version field to distinguish v1 from v2 pairings, so such a gate cannot be precise yet.

## Security review

**Verdict:** PASS

This slice is *wiring*: the Noise_IK crypto, AEAD framing, token gating, peer-static continuity, and close-code discipline are all owned by `internal/noise` + `V2SessionManager` (reviewed in #433 / #445 / #446 / #453). The new security surface is narrow — the static-key load link, the cutover switch, the handler-table handoff, and goroutine lifecycle — so the walk concentrates there.

**Findings:**

- **[Trust boundaries]** No findings. The untrusted→trusted boundary (phone `noise_init`/`noise_msg` → daemon state) is entirely inside `V2SessionManager.handleFrame`/`handleNoiseInit` (existing, single-site, reviewed). This slice adds no new parse site — it hands `conn.Frames()` to the manager wholesale. The one new boundary is *secret material* crossing file→memory: the static private key via `keys.LoadOrCreate`, which validates at the package boundary (seven-step load check, #438) and is passed as an opaque `priv[:]` slice the daemon never inspects, logs, or re-derives.
- **[Tokens, secrets, credentials]** No findings for what this slice introduces. Device-token validation is constant-time (`devices.VerifyToken` → `crypto/subtle`) inside `Devices.Validate`, unchanged. The static private key is read-only here (created/stored 0600 by `pyry pair`/`internal/keys`). The sole new logging site — `logger.Debug("relay: v2 manager run returned", "err", err)` — carries only `mgr.Run`'s return (`nil` or `ctx.Err()`), never key/token/payload. SHOULD FIX (developer hygiene, not a spec gap): the developer must not add any `slog` field carrying `StaticPriv`, `payload.Token`, or `priv` during wiring — the spec forbids it; code-review should confirm.
- **[Tokens — revocation liveness]** OUT OF SCOPE (matches v1). The daemon snapshots `devices.json` once at startup (`devices.Load` in the shared prologue). A `pyry pair revoke` while the daemon runs does not invalidate an already-authenticated v2 session until restart — identical to the v1 path's auth-gate snapshot. Not introduced by this slice; a registry-liveness/hot-reload follow-up would own it for both protocols.
- **[File operations]** No findings. `keys.LoadOrCreate(resolveStaticKeyBaseDir(), sanitizeName(instanceName))` reuses `pyry pair`'s exact path resolution: `sanitizeName` + the `keys` package `validDaemonName` allowlist guard path traversal, and **#439 (landed) hardens the read** — `O_NOFOLLOW` against symlink swap, `Lstat` + `0o600` mode rejection (`ErrInsecureKeyFileMode`), and parent-dir mode re-stat. `pyry pair` (`pair.go:188`) is already a production consumer of `static_key.json` via the identical call, so the daemon-on-every-startup read introduces no new file-hardening exposure. No user input is concatenated into a path unsanitised.
- **[Subprocess / external command]** N/A — the design adds no `exec.Command`; the supervised claude child is untouched.
- **[Cryptographic primitives]** No findings. No crypto is authored here. The static key is used solely as the Noise responder identity (the same key `pyry pair` advertises as `ServerStaticPubkey`) — no key-for-two-purposes reuse; device tokens are a separate SHA-256-hashed credential. `noise.KeyLen == 32` and `NewV2SessionManager` length-validates `StaticPriv`, so `priv[:]` (slicing the by-value `[32]byte` from `PrivateKey()`) is correct-length and the backing array stays reachable via the manager's retained `cfg.StaticPriv` for the manager's lifetime.
- **[Network & I/O]** No findings new to this slice. Inbound size caps are pre-existing: `decodeInnerFrameV2`'s `maxNoisePayloadBytes` (65535) and transport's 1 MiB `SetReadLimit`. The daemon is a WS *client* to the relay (no `http.Server` surface), so server-timeout discipline does not apply; handshake (5s) and rekey-reply (30s) timeouts are owned by `relay.Connection`/the manager.
- **[Network — connection exhaustion]** OUT OF SCOPE (matches v1, relay-layer concern). The manager lazy-creates one `V2Session` map entry per `conn_id` with no cap — but the v1 dispatcher does the same (one goroutine + state per `conn_id`, #307), and `conn_id` allocation is the relay's responsibility. v2 is in fact lighter (single dispatch goroutine vs goroutine-per-conn). Per-server-id connection caps belong to the pyrycode-relay repo.
- **[Error messages, logs, telemetry]** No findings. The manager's internal logs already follow the no-secrets discipline (`device_name` on accept = operator-actionable; omitted on reject = anti-enumeration). This slice adds no log field carrying secrets.
- **[Concurrency]** No findings. Every spawned goroutine has a documented exit: `conn.run` (relay-owned), `mgr.Run` (returns on `Frames` close or `ctx` cancel), the shared `waitDone` classifier, and the manager's bounded `time.AfterFunc` rekey callbacks (Run-ctx-tied). The cleanup ordering `conn.Close()` → `<-mgrDone` → `<-waitDone` is leak-free: `conn.run`'s `defer close(c.frames)` runs before `defer close(c.done)`, so `mgr.Run` unblocks (frames closed) before `conn.Wait()` returns (done closed). The manager holds no locks (single-goroutine ownership); the shared `*devices.Registry` is mutex-guarded and touched only from the manager's goroutine (`Validate` + handlers run synchronously on it). No new lock-ordering edge.
- **[Threat model alignment]** No findings; one positive property worth recording. Per `docs/protocol-mobile.md` § Security model and ADR 024, v2 is a **hard cutover, no soft fallback**. The switch is binary and exclusive: with `PYRY_MOBILE_V2=1`, *only* the v2 manager consumes `conn.Frames()` (a stray v1 frame is decoded by `decodeInnerFrameV2`, fails as `malformed_inner_frame`, and closes at 4421); with the switch unset, *only* the v1 dispatcher runs (a `noise_init` is handled as an ordinary v1 first frame). There is no mixed-mode path, hence **no protocol-downgrade attack surface** — flipping the switch cannot be coerced at the wire by either party. The cutover-specific correctness/security constraint (daemon selection MUST NOT be driven by `preflightVerdict`/device count) is centred in the Design § cutover switch.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-30
