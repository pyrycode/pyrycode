# `internal/relay/handlers` — per-envelope-type processors

Split out of [`relay-package.md`](relay-package.md) (parent doc — see there for the connection/transport layers this sub-package sits behind). This child covers the `handlers/` sub-package itself: its first inhabitant in depth, and the conventions later handlers in the package are expected to follow.

## Handlers: per-envelope-type processors (`handlers/`, #250)

Sub-package `internal/relay/handlers`. Each handler is a pure function: routing envelope in, routing envelope out, plus side effects on the registries it is passed. The dispatcher (future, in `internal/relay`) owns conn state, per-conn id allocation, and conn lifecycle; handlers know only payload semantics.

First inhabitant: `register_push_token` (`register_push_token.go`). #319 rewrote the original #250 pure handler against `dispatch.Handler`
and registered it in `cmd/pyry/relay.go` alongside
`list_conversations`. The pre-#319 `Handle` signature (routing-envelope
in/out, self-stamping `id`/`ts`/`in_reply_to`, sentinel
`ErrMalformedFrame`) is gone.

```go
package handlers

func RegisterPushToken(reg *devices.Registry, registryPath string,
                       logger *slog.Logger) dispatch.Handler
```

The returned closure has signature
`func(ctx context.Context, c *dispatch.Conn, env protocol.Envelope) error`.
`reg`, `registryPath`, and `logger` are captured — no globals; the
third argument diverges from `ListConversations`'s two-arg shape
because this handler logs on every branch. The authenticated device is
read via `c.Auth()` (populated by the gate per #318); the dispatcher
allocates the response id via `c.NextID()` (called inside `c.Reply`)
and stamps `in_reply_to` from `env.ID` and `ts` from `time.Now().UTC()`.
The handler never touches those fields itself.

### Behavioural contract

| Input case | Response envelope | Side effect |
|---|---|---|
| `c.Auth() == nil` (dispatcher routed an unauth conn here — bug; defence-in-depth) | `error`: `Code=auth.invalid_token`, `Retryable=false` | none |
| `env.Payload` not JSON-decodable as `RegisterPushTokenPayload` | `error`: `Code=protocol.malformed`, `Retryable=false`, static message (decode-error text NOT echoed) | none |
| Payload `(Platform, Token, DeviceName)` equals snapshot `(Platform, PushToken, Name)` | `ack` | **none — does NOT call `UpdatePushRegistration`, does NOT call `Save` (the dedupe contract)** |
| `reg.UpdatePushRegistration` returns `false` (concurrent revoke between auth-accept and frame arrival) | `error`: `Code=auth.invalid_token`, `Retryable=false` (same UX as unauth) | none |
| `reg.Save` returns non-nil | `error`: `Code=server.binary_busy`, `Retryable=true`, `RetryAfterS=nil` | **in-memory IS mutated; disk is not** (phone retries; dedupe will succeed on retry) |
| Triple differs and Save succeeds | `ack` | in-memory + disk both updated |

The outer-frame malformed branch is gone from the contract — the
dispatcher's `handleOne` decodes `env protocol.Envelope` from the
routing frame before invoking the handler and replies
`protocol.malformed` upstream. Only the **inner-payload** decode
remains the handler's responsibility.

### Dedupe is load-bearing

Spec § Phone background behaviour has the phone re-register on every WS connect (~100 bytes, self-heals registry drift). Without dedupe, every WS connect rewrites `devices.json` — flash wear and i/o churn for what's typically a no-op. The handler's "no write occurred" contract is structurally enforced: the dedupe branch returns the ack **before** calling `UpdatePushRegistration` or `Save`. Test pinpoints this via `errors.Is(os.Stat(path), fs.ErrNotExist)` with the file pre-state set to "never existed" — if dedupe were broken, Save would have created it.

### `Name` is part of the triple

The protocol's `device_name` makes the phone the source of truth for self-reported name (an iOS Settings rename should propagate). So the dedupe comparison is `(Platform, PushToken, Name)`, not just `(Platform, PushToken)`. The registry mutator (`devices.Registry.UpdatePushRegistration`) overwrites all three fields together — see [`features/devices-registry.md`](devices-registry.md).

### Save-failure leaves in-memory mutated

Documented post-condition, mirroring `Validate`'s `LastSeenAt` pattern: in-memory is the runtime source of truth; the next successful Save catches disk up. Test pins `reg.FindByTokenHash(...).PushToken == "new-fcm"` after the failed call.

### Sub-package isolation

`handlers` imports `internal/devices`, `internal/dispatch`, and `internal/protocol`. **It does NOT import `internal/relay`.** The new `internal/dispatch` edge (added #319) is cycle-free because `internal/dispatch`'s only handler-direction dependency is the `Handler` function type, which `handlers` consumes structurally. `auth.go` stays in `internal/relay` proper alongside the live Noise_IK handshake that now consumes its two WS close-code constants (see [`relay-package.md` § Auth](relay-package.md#auth-ws-close-code-constants-authgo)) — it is no longer a per-type handler or a dispatch gate.

The `wrap(connID, inReplyTo, nextID, envType, payload)` file-local helper from #250 is gone — `c.Reply(ctx, env, type, payloadJSON)` is the central stamping path now (#319). Two file-local helpers (`replyAck`, `replyError`) marshal the payload and call `c.Reply`.

### Logging discipline

| Event | Level | Fields |
|---|---|---|
| `relay: register_push_token write` | Info | `event=register_push_token.write`, `conn_id`, `device_name=payload.DeviceName`, `platform` |
| `relay: register_push_token dedupe` | Debug | `event=register_push_token.dedupe`, `conn_id`, `device_name=device.Name` |
| `relay: register_push_token save failed` | Warn | `event=register_push_token.save_failed`, `conn_id`, `device_name`, `err` |
| `relay: register_push_token device gone mid-conn` | Warn | `event=register_push_token.gone_mid_conn`, `conn_id`, `device_name` |
| `relay: register_push_token unauth` | Warn | `event=register_push_token.unauth`, `conn_id`, `code=auth.invalid_token` |

Push token (FCM/APNs registration id) is opaque infrastructure data, not a secret on par with the phone-side device token — but is still NOT logged (no operational signal worth the noise). Device-side token from auth is NEVER read or logged. Device name IS logged on every path that has one (write/dedupe/save-failed/gone-mid-conn) — the inverse of #249's reject-path discipline, because the handler runs post-auth: the caller has already cleared the auth gate, so there is nothing to enumerate. Unauth (`device == nil`) by definition has no name to log.

### Test surface (rewritten #319)

`internal/relay/handlers/register_push_token_test.go` — seven flat tests, stdlib only, package `handlers`, all under `-race`. The fixture uses `dispatch.NewTestConn(testConnID, out, dev)` to build a `*dispatch.Conn` with a buffered outbound channel the test drains; `_ = c.NextID()` is called once after construction so the first handler reply observes `id=2` (mirroring the gate's `hello_ack=1` accounting).

- `TestRegisterPushToken_FirstTimeRegister_WritesAndAcks` — happy-path write + reload via `devices.Load` and assert the triple.
- `TestRegisterPushToken_ReregisterIdentical_NoWriteAndAcks` — dedupe spy: file deliberately not pre-Saved, asserts `errors.Is(os.Stat, fs.ErrNotExist)` after the call.
- `TestRegisterPushToken_ReregisterChanged_WritesAndAcks` — pre-Save, change one field, content-equality post-call (sidesteps CI mtime-resolution flakes).
- `TestRegisterPushToken_GoneMidConn_EmitsAuthInvalidToken` — `dev`'s TokenHash absent from the registry forces `UpdatePushRegistration` to return false; asserts `auth.invalid_token`, `Retryable=false`.
- `TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy` — regular file at `<tempdir>/blocker` makes `MkdirAll` fail on `<blocker>/devices.json`. Pins error code/retryable + in-memory-still-mutated post-condition.
- `TestRegisterPushToken_UnauthenticatedConn_EmitsAuthInvalidTokenNoWrite` — `auth=nil`; asserts `auth.invalid_token` shape + no file + unchanged registry.
- `TestRegisterPushToken_MalformedPayload_EmitsProtocolMalformed` — `env.Payload = []byte("not-json")`; asserts `Code=protocol.malformed`, `Retryable=false`. New case; replaces the deleted `TestHandle_MalformedFrame_ReturnsSentinel` (the dispatcher owns the malformed-frame path now).

`newTestConn(t, dev)` and `makeRequest(t, payload)` are the file-local helpers; the `assertEnvelopeShape` / `equalRouting` / `makeRegisterRouting` helpers from #250 are gone with the sentinel.

#### e2e (`internal/e2e/register_push_token_test.go`, new #319)

Build tag `e2e`. `TestRelay_RegisterPushToken_AckAndPersists` pairs a device via `RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")`, decodes the plaintext token via `decodePairPayload`, boots the daemon against a `fakerelay` with `PYRY_ALLOW_INSECURE_RELAY=1`, dials a `fakephone` with the paired token, sends `hello` → expects `hello_ack` (`*InReplyTo == 1`), sends `register_push_token` with `ID: 2` → expects `ack` (`*InReplyTo == 2`, `env.ID >= 2` — strictly-greater leaves room for future dispatcher-side replies between `hello_ack` and this ack), then reloads `~/.pyry/test/devices.json` and asserts the `(Platform, PushToken, Name)` triple is persisted.

## `send_message` grows a sixth seam: conversation auto-naming (#2159)

`SendMessage` widened again — `reg ConversationAutoNamer, announce ConversationAnnouncer` — to derive and store a conversation's name from its first accepted message, then push the updated row through the existing `conversation_updated` fan-out. See [`conversation-session-binding-routing.md` § Enqueue-and-ack (#721)](conversation-session-binding-routing.md#enqueue-and-ack-721) for the ordering lesson this ticket produced: the step runs *after* `replyAck`, not before, because anything sitting between `EnqueueDelivery` and the ack races the drain goroutine it just handed the turn to.

**A narrow interface earns a separate name even when its method set is identical to a sibling's.** `ConversationAutoNamer` (`Update` + `Save` + `WorkspaceLabel`) has the exact method set of `ConversationRenamer`, and this package already tolerates that kind of overlap (`ConversationPromoter`, `ConversationArchiver`, `ConversationRenamer` all share most of their shape). It is still declared as its own type rather than reused, because the doc comment on a consumer-declared interface carries that *consumer's* contract, not just its shape: `ConversationAutoNamer`'s comment records "writes `Name` only over a nil one," which `ConversationRenamer` must never claim. A reviewer pattern-matching on method sets alone will read the duplication as redundant; it isn't — the two interfaces document two different promises about the same registry method.
