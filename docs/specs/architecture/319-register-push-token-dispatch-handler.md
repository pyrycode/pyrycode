# #319 — `register_push_token`: rewrite handler against `dispatch.Handler`, wire into the relay dispatcher, add e2e

**Sibling of #313** (which rewired `list_conversations`). Split from #305. Depends on slice A (#318 — per-conn auth slot, landed).

## Files to read first

- `internal/dispatch/dispatch.go:54-134` — the `Handler` alias, `Conn.ConnID/Auth/NextID/Send/Reply` surface. `Reply` (lines 124–134) is load-bearing: it stamps `id`, `in_reply_to`, `ts` so handlers never touch them.
- `internal/dispatch/dispatch.go:382-472` — `runConn`/`runGate`/`handleOne`. Pins (a) `setAuth` runs before the first handler dispatch on accept, (b) the gate advances `NextID` past `hello_ack`'s id=1 so the first handler reply lands at id=2 (lines 431–437), (c) the dispatcher already does `IsV1Compatible` and envelope-decode upstream of the handler.
- `internal/relay/handlers/register_push_token.go` (full file) — the existing pure handler; what gets rewritten. Keep the logging strings, the dedupe/write/save/error branching, and the `msgUnauthorized` / `msgBinaryBusy` constants. Drop `wrap()` and `ErrMalformedFrame`.
- `internal/relay/handlers/list_conversations.go` (full file) — the factory shape (`func ListConversations(reg ConversationLister) dispatch.Handler`) and `c.Reply(ctx, env, type, payloadJSON)` idiom this spec mirrors. Same package, identical signature shape.
- `internal/relay/handlers/register_push_token_test.go` (full file) — current unit coverage to preserve. The test plumbing (`testConnID`, `testRequestID`, `testNextID`, `assertEnvelopeShape`, `makeRegisterRouting`, `freshRegistryWithDevice`) is the seam to rewrite. Coverage to retain: dedupe (no disk touch), write+ack, mid-conn-removed race → `auth.invalid_token`, save-fail → `server.binary_busy`, nil-device → `auth.invalid_token` (no disk touch). Drop the `TestHandle_MalformedFrame_ReturnsSentinel` case — payload-decode failure now flows through `c.Reply` with `protocol.malformed`.
- `cmd/pyry/relay.go:128-134` — `dispatch.New(...)` site and the `d.Register(protocol.TypeListConversations, handlers.ListConversations(convReg))` line; add the new register call immediately after.
- `cmd/pyry/relay.go:104-108` — where `registry` (the `*devices.Registry`) is loaded; the same value is what the new factory needs. The registry on-disk path is `resolveDevicesPath(instanceName)` — call it once and pass the resolved string into the factory alongside `registry`.
- `internal/protocol/push.go` — `RegisterPushTokenPayload{Platform, Token, DeviceName}` shape.
- `internal/protocol/codes.go:41-61` — `TypeAck`, `TypeError`, `TypeRegisterPushToken`, `CodeAuthInvalidToken`, `CodeServerBinaryBusy`, `CodeProtocolMalformed`.
- `internal/devices/devices.go` — `Registry.UpdatePushRegistration(tokenHash, platform, token, deviceName) bool`, `Registry.Save(path) error`, `Device{Platform, PushToken, Name, TokenHash}`.
- `internal/e2e/relay_auth_test.go` (full file) — the closest existing template: spawn a daemon, point it at a `fakerelay`, dial a `fakephone`, send `hello`, assert reply. The new e2e differs in two ways: (a) pair a device first so the gate accepts (use `RunBareIn(t, home, "pair", "--name=...")` and `decodePairPayload` to capture the plaintext token, mirroring `pair_test.go:30-53`), (b) after `hello_ack` send `register_push_token` and assert the `ack`.
- `internal/e2e/internal/fakephone/fakephone.go` — `Dial`, `Send`, `Receive` surface the test will drive.
- `internal/e2e/pair_test.go:27-60` — pairs a phone via the CLI, decodes the payload's plaintext token via `decodePairPayload`, and loads the registry off disk. The new e2e uses the same shape to obtain a valid token.
- `docs/PROJECT-MEMORY.md` § "Project-level conventions" — atomic-write recipe for registries (already obeyed by `devices.Registry.Save`); refusal-mapping-at-consumer rule (this handler is the consumer that maps the registry's behaviour to `auth.invalid_token` / `server.binary_busy` wire codes).

## Context

The pure handler at `internal/relay/handlers/register_push_token.go` is well-tested but signature-incompatible with `dispatch.Handler`:

- Its `(routing protocol.RoutingEnvelope, ..., nextID uint64, ...) (protocol.RoutingEnvelope, error)` shape re-decodes the inner envelope, allocates its own response id, and stamps `id`/`ts`/`in_reply_to` via a private `wrap()` helper — a parallel implementation of what `dispatch.Conn.Reply` does centrally. That's the load-bearing invariant `dispatch.Conn.Reply` exists to enforce; bypassing it loses the per-conn id monotonicity and `in_reply_to` echo guarantees.
- It receives the authenticated `*devices.Device` as a parameter, but #318 just installed the per-conn auth slot (`Conn.Auth()`) as the canonical seam. The handler should consume that slot, not its own parameter.
- It is not currently registered against any dispatcher. The relay dispatcher's handler table has exactly one entry today (`list_conversations`); this slice adds `register_push_token` alongside it and exercises the full phone → binary → registry → ack path end-to-end.

#313 explicitly deferred this: "a sibling ticket should adapt it." This is that sibling.

## Design

### Factory + signature

A single factory in `internal/relay/handlers/`:

```
func RegisterPushToken(reg *devices.Registry, registryPath string) dispatch.Handler
```

The returned closure has signature `func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error`. `reg` and `registryPath` are captured in the closure — no globals, matching `ListConversations`'s shape. The handler is stateless beyond that capture; reentrant from concurrent per-conn goroutines because `Registry.UpdatePushRegistration` and `Registry.Save` are already mutex-serialized.

### Branches inside the handler

In order:

1. **Nil-device guard.** `dev := c.Auth(); if dev == nil { … }`. Build a `protocol.ErrorPayload{Code: protocol.CodeAuthInvalidToken, Message: msgUnauthorized, Retryable: false}`, marshal, return `c.Reply(ctx, env, protocol.TypeError, payloadJSON)`. Log at WARN with `event=register_push_token.unauth`, `conn_id=c.ConnID()`, `code=auth.invalid_token` (no device name — there is none). Defence-in-depth: the gate rejects unauthenticated conns upstream, but the handler must still reply coherently if `Auth()` ever returns nil. Do NOT touch `reg` on this branch.
2. **Payload decode.** `var p protocol.RegisterPushTokenPayload; if err := json.Unmarshal(env.Payload, &p); err != nil { … }`. Reply with `protocol.CodeProtocolMalformed` + `msgMalformed = "malformed register_push_token payload"`, `Retryable: false`. Log at WARN with `event=register_push_token.malformed`, `conn_id`, `device_name=dev.Name`, `err`. (The dispatcher's `IsV1Compatible` and outer-envelope decode run upstream; inner-payload decode is the handler's responsibility.)
3. **Dedupe.** If `p.Platform == dev.Platform && p.Token == dev.PushToken && p.DeviceName == dev.Name`, marshal `protocol.AckPayload{}` and `c.Reply(ctx, env, protocol.TypeAck, payloadJSON)`. Log at DEBUG with `event=register_push_token.dedupe`, `conn_id`, `device_name=dev.Name`. Do NOT touch disk.
4. **Mid-conn-removed race.** `if ok := reg.UpdatePushRegistration(dev.TokenHash, p.Platform, p.Token, p.DeviceName); !ok { … }`. Same error payload as branch (1): `CodeAuthInvalidToken` + `msgUnauthorized` + `Retryable: false`. Log at WARN with `event=register_push_token.gone_mid_conn`, `conn_id`, `device_name=dev.Name`.
5. **Save failure.** `if err := reg.Save(registryPath); err != nil { … }`. Payload `{Code: CodeServerBinaryBusy, Message: msgBinaryBusy, Retryable: true}`. `RetryAfterS` left nil (matches existing behaviour). Log at WARN with `event=register_push_token.save_failed`, `conn_id`, `device_name=dev.Name`, `err`.
6. **Success.** Marshal `AckPayload{}` and `c.Reply(ctx, env, protocol.TypeAck, payloadJSON)`. Log at INFO with `event=register_push_token.write`, `conn_id`, `device_name=p.DeviceName`, `platform=p.Platform`. Push token is NEVER logged at any level.

Every branch returns whatever `c.Reply` returns (the dispatcher logs it at WARN via `handleOne`'s error path; the handler need not synthesize a wrapper error).

### What goes away

- `wrap(connID, inReplyTo, nextID, envType, payload)` — deleted; `c.Reply` is the replacement.
- `ErrMalformedFrame` sentinel + the outer `json.Unmarshal(routing.Frame, &inner)` block — the dispatcher's `handleOne` already decoded the envelope before invoking the handler. The inner *payload* decode survives; it now replies via `c.Reply` with `protocol.malformed` instead of returning a sentinel.
- The `routing protocol.RoutingEnvelope`, `device *devices.Device`, `nextID uint64`, `logger *slog.Logger` parameters. `env` arrives decoded, `dev` comes from `c.Auth()`, `id` is dispatcher-allocated via `c.NextID()` (called inside `c.Reply`), and the logger is captured from the factory closure.

The factory needs a `logger *slog.Logger` parameter too — match `ListConversations`'s shape, which today takes only the registry. Diverge here: this handler logs on every branch, so the closure must capture a logger. Add `logger *slog.Logger` as the third factory argument: `RegisterPushToken(reg *devices.Registry, registryPath string, logger *slog.Logger) dispatch.Handler`. The dispatcher passes the same logger to handlers across the package; `cmd/pyry/relay.go` already has it in scope.

### Wiring in `cmd/pyry/relay.go`

Inside `startRelay`, immediately after the existing `d.Register(protocol.TypeListConversations, handlers.ListConversations(convReg))` line, add:

```
d.Register(protocol.TypeRegisterPushToken, handlers.RegisterPushToken(registry, resolveDevicesPath(instanceName), logger))
```

`registry` is the `*devices.Registry` already loaded above (`relay.go:104-108`). `resolveDevicesPath(instanceName)` is the canonical on-disk path — call it once at the registration call site so the factory captures a stable string. No other wiring changes.

### Concurrency

Same as today. The per-conn goroutine inside `dispatch.Dispatcher` runs the handler serially per `conn_id`. `Registry.UpdatePushRegistration` and `Registry.Save` each take the registry's mutex; the documented "in-memory mutates even when Save fails" post-condition is preserved.

### Error handling

The handler returns `c.Reply`'s error. `c.Reply` returns ctx-cancel or `c.Send` errors (failed JSON marshal of the envelope, or backpressure cancellation). The dispatcher's `handleOne` logs this at WARN; no close-conn semantics today. The handler does not need to wrap or distinguish these errors.

## Testing strategy

### Unit tests (`internal/relay/handlers/register_push_token_test.go`, rewritten)

The test fixture switches from invoking `Handle(...)` to driving a `dispatch.Handler` against a real `*dispatch.Conn`. The conn needs an outbound channel the test can drain to recover the response envelope. Sketch:

- Helper `newTestConn(t)` returns `(c *dispatch.Conn, recv func() protocol.Envelope)`. Constructs a `Conn` with `id=testConnID` and an outbound `chan protocol.RoutingEnvelope` of buffer 4. To install `dev` into `c.Auth()` without exporting `setAuth`, the test sits in package `handlers` and reaches the unexported method via the existing visibility (same package as the production handler is `handlers`, but `setAuth` is unexported on `dispatch.Conn` — see note below). Drive `NextID` once before the test so the first reply observes id=2 (mimicking the dispatcher's hello_ack accounting); test asserts the reply's id matches this.
- **Test-only seam for `Auth`.** `dispatch.Conn.setAuth` is unexported and only callable from the `dispatch` package. The handler test sits in `internal/relay/handlers` and cannot reach it. Two options:
  1. Add an exported `dispatch.NewConnForTest(id string, outbound chan<- protocol.RoutingEnvelope, auth *devices.Device) *Conn` constructor under a `_test.go`-only file (Go does NOT propagate `_test.go` symbols across packages; this won't work).
  2. Add an exported `dispatch.NewTestConn(...)` constructor in a non-`_test.go` file, documented as test-only. **Chosen approach.** It mirrors the `dispatch.FirstFrameGate` and `Conn.Reply` surface that's already exported, and is the smallest seam that lets verb-package tests drive a real `Conn` without dependency injection.
  3. The handler test stays in package `handlers_test` (black-box) and uses the new constructor.

  Spec the constructor as: `func NewTestConn(id string, outbound chan<- protocol.RoutingEnvelope, auth *devices.Device) *Conn` — returns a `*Conn` with `id`/`outbound`/`auth` set and `nextID` at zero (so the first `c.NextID()` call returns 1; tests that want to simulate post-hello_ack state call `c.NextID()` once before invoking the handler). Implementation is three field assignments. Document on the symbol that it's for test fixtures only.
- Recv helper drains one routing envelope, unmarshals its `Frame` into a `protocol.Envelope`, asserts `ConnID == testConnID`, returns the envelope. Callers assert `Type`, `ID`, `*InReplyTo`, and payload contents.

Test cases (all bullet-point scenarios — no full function bodies in the spec):

- **first-time register writes and acks** — seed `Registry` with a `Device` lacking `Platform/PushToken`; call handler with payload `{Platform: "fcm", Token: "fcm-token-abc", Name: "Juhana's Pixel 8"}`; assert (a) reply is `ack` with `InReplyTo == request.ID` and `ID == 2`, (b) `os.Stat(path)` succeeds, (c) `devices.Load(path).FindByTokenHash(...)` returns the device with the new triple.
- **dedupe replies ack, no write** — seed `Device` whose `(Platform, PushToken, Name)` already matches the payload; call handler; assert (a) reply is `ack`, (b) `os.Stat(path)` returns `fs.ErrNotExist` (Save was never called).
- **changed-triple writes and acks** — seed `Device` with `PushToken: "old-fcm"` and pre-write the registry to disk; call handler with `Token: "new-fcm"`; assert reply is `ack` and the on-disk row shows the new token.
- **mid-conn-removed race → auth.invalid_token** — seed an empty registry but pass a `dev` whose `TokenHash` is NOT in the registry (so `UpdatePushRegistration` returns false); assert reply is `error` with `Code: CodeAuthInvalidToken`, `Retryable: false`.
- **save failure → server.binary_busy** — point `registryPath` at a path whose parent is a regular file (forces `MkdirAll` to fail, as in the existing test); assert reply is `error` with `Code: CodeServerBinaryBusy`, `Retryable: true`; assert in-memory registry IS mutated (post-condition preserved).
- **nil-device → auth.invalid_token, no disk touch** — construct `Conn` with `auth=nil`; assert reply is `error` with `Code: CodeAuthInvalidToken`, `Retryable: false`; assert `os.Stat(path)` returns `fs.ErrNotExist` and `len(reg.List())` is unchanged.
- **malformed payload → protocol.malformed** — set `env.Payload = []byte("not-json")`; assert reply is `error` with `Code: CodeProtocolMalformed`, `Retryable: false`. (New case; replaces the deleted `TestHandle_MalformedFrame_ReturnsSentinel`.)

Drop the `equalRouting` helper and the malformed-routing-envelope test entirely; the dispatcher owns that path now.

### e2e (`internal/e2e/register_push_token_test.go`, new file)

Build tag `//go:build e2e`, package `e2e`. Single test function `TestRelay_RegisterPushToken_AckAndPersists`. Scenario:

1. `home := shortHome(t)`. Run `RunBareIn(t, home, "pair", "--name=phone-a")` to mint a device; decode the stdout via `decodePairPayload` (helper already in `internal/e2e`) to capture the plaintext `token`.
2. Start a fakerelay with `relayTestLogger()`, deferred `Close`.
3. `StartInWithEnv(t, home, []string{"PYRY_ALLOW_INSECURE_RELAY=1"}, "-pyry-relay="+fr.URL()+"/v1/server")` — boots the binary against the fakerelay.
4. Read `serverID` via `readPersistedServerID(t, home)`; wait up to 5s for `fr.LastBinaryHello(serverID)` to confirm the binary↔relay handshake completed.
5. `fakephone.Dial(ctx, fr.URL(), serverID, token, "phone-a")` — phone WS opens.
6. Phone sends `hello` (`HelloClientPayload{Role: "client", DeviceName: "phone-a", ClientVersion: "0.0.1-test", ProtocolVersions: []string{"v1"}}`) with `ID: 1`.
7. Phone receives → expect `hello_ack` (the gate's accept reply). Assert `Type == TypeHelloAck`, `InReplyTo == &1`. (Don't assert `ID == 1` — that's the gate's contract, not this slice's surface.)
8. Phone sends `register_push_token` with `ID: 2` and payload `{Platform: "fcm", Token: "fcm-token-xyz", DeviceName: "phone-a"}`.
9. Phone receives → expect `ack`. Assert (a) `Type == TypeAck`, (b) `InReplyTo == &2` (echoes the request's id), (c) `ID >= 2` — the dispatcher consumed id=1 for the hello_ack and advanced past it (`runGate`'s `_ = c.NextID()` at `dispatch.go:436`), so the first handler-originated reply lands at id=2 or higher; strictly-greater leaves room for any future dispatcher-side replies between hello_ack and this ack without churning the assertion.
10. Re-open the on-disk registry: `devices.Load(filepath.Join(home, ".pyry", "pyry", "devices.json"))`. Find the device by token hash (using the captured plaintext via `devices.HashToken(token)` or by `Name == "phone-a"`). Assert `Platform == "fcm"`, `PushToken == "fcm-token-xyz"`, `Name == "phone-a"`.
11. Phone `Close()`. Daemon stops via `t.Cleanup`'s `h.Stop(t)`.

The test exercises: gate accept → `c.Auth()` populated → handler reads `Auth` → `c.Reply` stamps `id`/`in_reply_to`/`ts` → relay forwards ack to phone → registry persisted with the new triple. Same harness shape as `TestRelay_AuthReject_4401`; differs only in (a) the device IS paired, (b) two send/receive rounds instead of one, (c) the on-disk assertion at the end.

### Logging discipline assertions

No test asserts log strings (none of the existing handler tests do). The logging contract is documented in this spec and reviewed at PR time — push tokens never appear in log fields under any of the seven branches.

## Open questions

- **`dispatch.NewTestConn` ergonomics.** This spec adds a small exported test-only constructor on `dispatch.Conn`. The alternative is moving the handler test into the `dispatch` package or driving the handler through a full `Dispatcher` (heavier: requires `FirstFrameGate`, a frames channel, goroutine lifecycle). The constructor is the lightest seam. If the developer prefers, they may instead add a `func NewConn(...)` (without the `Test` suffix) and call it the canonical Conn constructor — the existing `routeConn` would also use it. Either is fine; the `Test`-suffix version preserves the current "Dispatcher is the only Conn factory" invariant more explicitly. Recommended: `NewTestConn` (smaller surface change).
- **Handler signature mid-conn race vs. nil-device race code reuse.** Both branches emit identical `auth.invalid_token` payloads. Extract a `replyUnauthorized(ctx, c, env)` local helper? The existing handler inlines both; mirror that. Defer the extraction.
- **The `ErrMalformedFrame` consumer.** No production code calls into `handlers.ErrMalformedFrame`; the tests are the only callers. Deletion is safe — confirm with `codegraph_callers ErrMalformedFrame` if the developer wants to be sure.

## Size

Production code:

- `internal/relay/handlers/register_push_token.go` — net negative; the rewrite shrinks the file (no `wrap()`, no `ErrMalformedFrame`, simpler signature). ~70 lines after.
- `cmd/pyry/relay.go` — one new `d.Register(...)` line.
- `internal/dispatch/dispatch.go` — one new `NewTestConn` constructor (~10 lines + doc comment).

Tests:

- `internal/relay/handlers/register_push_token_test.go` — full rewrite of the existing file. Roughly same length.
- `internal/e2e/register_push_token_test.go` — new file, ~80 lines (close to `TestRelay_AuthReject_4401`'s shape).

Two files net new (the e2e), three modified (handler, dispatch, relay.go) plus the unit test rewrite. Well inside the XS envelope (≤100 production lines, ≤3 files of substantive production change, no new exported types beyond `dispatch.NewTestConn`). Edit fan-out is minimal: the only consumer of the old `handlers.Handle` signature is the existing unit test, which is rewritten wholesale; no other call sites in the tree.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** Single explicit boundary: the dispatcher decodes `env protocol.Envelope` from the WS frame upstream; the handler treats `env.Payload`, `p.Platform`, `p.Token`, `p.DeviceName` as untrusted until/unless persisted. `dev := c.Auth()` is trusted (populated by the gate from `devices.Registry.MatchTokenHash`). The nil-device guard makes the boundary defensive — handler refuses to act on untrusted-only state.
- **[Tokens]** No findings on the device-auth token (handler never reads `env.Token`; it's the relay's prepended-only field, scrubbed before the dispatcher even sees it per `dispatch.go:32-37`). Push tokens (`p.Token`, `dev.PushToken`) are not credentials on par with the auth token (per the existing in-file comment) and follow the established INFO-on-write / DEBUG-on-dedupe policy — never directly logged as a field, but their *change* is implied by the write event. Lifecycle: creation = first `register_push_token`; storage = on disk in `devices.json`; rotation = next `register_push_token`; revocation = `pair revoke` rewrites the registry. All four addressed by pre-existing surface; this slice changes none of it.
- **[File operations]** `registryPath` is `resolveDevicesPath(instanceName)` — daemon-constructed, never user-controlled. No path-traversal vector. `Registry.Save` already obeys the atomic temp+rename recipe documented in `docs/PROJECT-MEMORY.md` and sets the file mode (`0600`) used by every registry in the tree. This slice does not change that recipe.
- **[Subprocess]** N/A — handler executes no subprocess.
- **[Cryptographic primitives]** N/A — no new crypto. `devices.HashToken` (SHA-256, pre-existing) is the only crypto adjacent to this path and is consumed by `Registry.UpdatePushRegistration`'s key lookup, not by this handler directly.
- **[Network & I/O]** Inbound frame size is capped by `internal/transport`'s WS read limit (1 MiB, mirrored in `fakephone.maxFrameBytes`). The handler does not re-enforce; per `dispatch.go:25-26` that's the established policy. Per-field length caps on `Platform`/`Token`/`DeviceName` — **SHOULD FIX in a follow-up, not in this slice.** A 1 MiB frame can push ~1 MiB of strings into `devices.json` for a single registered device, bloating the registry. The existing handler doesn't enforce caps either; flagging this for a future ticket (file under "deferred from #319" when raised). Not in scope here because adding caps now would expand the spec beyond the rewrite and the existing behaviour is preserved-but-not-worsened.
- **[Error messages, logs, telemetry]** Logs carry `event`, `conn_id`, `device_name`, `platform`, and `err` (on save-fail) — no push tokens, no auth tokens, no full payloads. `slog`'s `TextHandler` and `JSONHandler` both escape control characters in string values, so a malicious `payload.DeviceName` containing `\n` / ANSI escapes cannot inject forged log lines. Error envelopes returned over the wire carry only the `Code*` string + a static `Message` (no untrusted echo). Confirmed against `dispatch.go:34-37` policy.
- **[Concurrency]** `c.Auth()` is happens-before-safe (gate writes on per-conn goroutine before first handler dispatch; per the docstring at `dispatch.go:67-76`). `Registry.UpdatePushRegistration` and `Registry.Save` each take the registry mutex internally; two concurrent `register_push_token` flows for the same `TokenHash` from different conns interleave at the mutex boundary with documented last-writer-wins semantics (preserved from existing code). No new lock acquisitions; no ordering hazard introduced.
- **[Threat model alignment]** Aligns with `docs/protocol-mobile.md` § Security model: phone tokens are plaintext credentials handled only at the gate, never reach this handler; push tokens are non-secret infrastructure data with the established log policy; the dispatcher's `protocol.malformed` reply for payload-decode failure matches the documented refusal shape (Code string + static Message, no decode-error text echoed).
- **[`dispatch.NewTestConn` exposure]** SHOULD FIX in implementation, not in the spec: the new constructor is necessarily an exported non-`_test.go` function (Go does not propagate `_test.go` symbols across packages). It does NOT poke `setAuth` on a Conn the dispatcher owns — it only constructs a fresh Conn whose outbound channel is supplied by the caller — so it cannot grant fake auth to a real session, nor leak frames onto the dispatcher's real outbound. The realistic abuse is a future developer reaching for it in production wiring. Mitigation in the developer's work: doc comment on `NewTestConn` opens with "Test fixtures only. Do not call from production code." Code-review checks no `cmd/` package calls it.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-13
