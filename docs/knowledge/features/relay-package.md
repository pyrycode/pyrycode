# `internal/relay` — binary side of the binary↔relay wire protocol

## What it is

The binary side of the binary↔relay wire protocol. Three surfaces:

1. **Outbound dial, established on WS upgrade** (`connection.go`, #248; binary↔relay handshake retired #582) — wraps `internal/transport.Client` (#247, the generic WSS primitive): builds the upgrade headers, treats the conn as established the moment the WS upgrade fires (the relay is content-blind — it registers the binary's server-id from the `x-pyrycode-server` header and sends no `hello_ack`), classifies WS close-code `4409` as terminal (server-id conflict), and exposes inbound frames as `protocol.RoutingEnvelope` values via `Frames()`. No relay-originated `hello`/`hello_ack` ceremony on this leg. Knows nothing about per-envelope dispatch or supervisor lifecycle.
2. **Shared WS close-code constants** (`auth.go`) — `StatusUnauthorized` (4401) and `MsgInvalidToken`, consumed by the live Noise_IK v2 handshake in [`v2-session-manager.md`](v2-session-manager.md). `auth.go` originally (#249) held a pure v1 first-frame token-validation predicate, `AuthenticateFirstFrame`; that predicate's production caller was removed in #913 slice 1, the never-wired dead machinery it left behind (`dispatch.FirstFrameGate`/`Dispatcher`) was deleted in #1039, and `AuthenticateFirstFrame` itself (plus `AuthOutcome`, `ErrMalformedHelloFrame`, and the transitively-dead `buildResponse` helper) was deleted in #1040 — see [codebase/1040.md](../codebase/1040.md). The two close-code constants are all that remains; see § Auth below.
3. **Per-envelope-type handlers** (`handlers/`, #250, signature rewrite #319) — sibling sub-package (`internal/relay/handlers`) of `dispatch.Handler` closures, one per inbound phone-traffic envelope type. Inhabitants today: `ListConversations` (#303) for `list_conversations`, `CreateConversation` (#666, mint+bind #677, per-`Cwd` spawn #685) for `create_conversation`, `RenameConversation` (#820) for `rename_conversation`, `PromoteConversation` (#949) for `promote_conversation`, `DeleteConversation` (#822) for `delete_conversation`, `ArchiveConversation` (#881) for both `archive_conversation` and `unarchive_conversation`, `RegisterPushToken` (#250 logic, #319 signature) for `register_push_token`, and `SendMessage` (#322 + Activate ordering #396 + per-conversation routing #678 + enqueue-and-ack #721 + auto-naming #2159) for `send_message`. `RenameConversation` clones `CreateConversation`'s handler shape but is simpler (no session mint, no cwd): decode → empty-title guard → `conversations.Registry.Update` (single locked callback both renames and snapshots the reply, no find-then-read) → eager `Save` → reply with the reused `conversation_updated` record via its own consumer-declared `ConversationRenamer` interface (`Update` + `Save` + `WorkspaceLabel`, #2210) — see [codebase/820.md](../codebase/820.md); proven at the daemon boundary over the encrypted v2 wire by `TestRelayV2_Rename` (#974, split from the #960 verb-coverage sweep) — see [codebase/974.md](../codebase/974.md). `PromoteConversation` (#949) clones `RenameConversation`'s shape with one extra step: `conversations.Registry.Promote` returns only an `error`, not the updated row, so the handler reads it back via `Get` before building the `conversation_updated` reply, via its own consumer-declared `ConversationPromoter` interface (`Promote` + `Get` + `Save` + `WorkspaceLabel`, #2210). The payload's required `Cwd` is deliberately NOT consumed — the promoted channel inherits the scratch conversation's existing (already `$HOME`-confined) `Cwd` rather than the untrusted wire value, so unlike `change_workspace` this verb opens no untrusted-path surface — see [codebase/949.md](../codebase/949.md). `DeleteConversation` clones the same shape for a **hard, permanent** delete (the reversible path is archive/unarchive, #880/#881, a distinct verb): decode → `conversations.Registry.Delete` (a miss is `conversation.not_found`, idempotent-on-miss, no state change) → eager `Save` → reply `conversation_deleted` carrying only the deleted id via its own consumer-declared `ConversationDeleter` interface (`Delete` + `Save`) — no `conversation_updated` reuse, since the row no longer exists to project name/cwd/last_used_at from. Its malformed-decode branch deliberately diverges from the rename/create template: it logs `conn_id` only, never the decode `err` or `conversation_id`, since a `*json.SyntaxError` or a partially-decoded struct can carry attacker payload bytes — see [codebase/822.md](../codebase/822.md); proven at the daemon boundary over the encrypted v2 wire by `TestRelayV2_Delete` (#975, split from the #960 verb-coverage sweep, structure copied from the merged #974 rename test) — see [codebase/975.md](../codebase/975.md). `ArchiveConversation` (#881) is **one parameterized factory** (`func(reg, registryPath, logger, archived bool) dispatch.Handler`) registered twice — the reversible, **symmetric toggle** counterpart to `DeleteConversation`'s permanent removal: decode → `conversations.Registry.SetArchived(id, archived)` (#880's deterministic single-field mutator, a miss is `conversation.not_found`) → `Get` to snapshot the post-flip record for the reply (a concurrent-delete race surfacing as a `Get` miss also folds into `conversation.not_found`) → eager `Save` → reply with the reused `conversation_updated` record (now carrying `is_archived`) via its own consumer-declared `ConversationArchiver` interface (`SetArchived` + `Get` + `Save` + `WorkspaceLabel`, #2210). Diverges from `RenameConversation`'s snapshot approach deliberately: it flips via `SetArchived` + a follow-up `Get` rather than an `Update` closure, honoring #880's single-field mutator instead of a general closure. Copies `DeleteConversation`'s malformed-branch logging divergence (`conn_id` only) and carries the same `security-sensitive` label — see [codebase/881.md](../codebase/881.md); proven at the daemon boundary over the encrypted v2 wire by `TestRelayV2_Archive` (#976, split from the #960 verb-coverage sweep, structure copied from the merged #974 rename test): a single archive → unarchive round-trip sent as two frames over one Noise_IK-handshaked channel (no re-handshake between them), plus a not-found subtest — see [codebase/976.md](../codebase/976.md). Both verbs reply only to the requester, no live fan-out, same posture as rename/delete. `CreateConversation` mints a server-side id (`conversations.NewID`), **mints and binds a dedicated claude session for the conversation via the sessions pool** (#677 — eager bind: the conversation's `CurrentSessionID` points at a `Pool.Create`'d session so each discussion is isolated instead of sharing the bootstrap claude; the mint is bounded by a 30s timeout, and a mint failure replies `server.binary_offline` retryable with **no** row created), records a registry row with the effective cwd / promoted / name (server defaults for `null` fields) **plus the bound session id**, **eagerly** Saves so the row survives a daemon restart (diverging from the lazy archive-tick sweep, matching the `pyry pair` Add→Save precedent), and replies `conversation_created` (the wire reply is unchanged — the binding is internal registry state) — see [codebase/666.md](../codebase/666.md), [codebase/677.md](../codebase/677.md), [features/conversation-session-binding.md](conversation-session-binding.md). Each is a factory: `func(deps...) dispatch.Handler` returning a closure with `func(ctx, *dispatch.Conn, protocol.Envelope) error`. The handler reads the authenticated device via `c.Auth()` (#318), allocates response ids via `c.NextID()` (called inside `c.Reply`), and never stamps `id`/`in_reply_to`/`ts` itself. The sub-package imports `internal/conversations` + `internal/devices` + `internal/dispatch` + `internal/protocol` only — no import of `internal/relay` or `internal/sessions` — keeping it cycle-free; the `send_message` handler depends on its own `handlers.TurnWriter` interface (`Activate` + `WriteUserTurn`) that `*sessions.Session` satisfies structurally, and the `create_conversation` handler likewise depends on its own `handlers.SessionCreator` interface (`Create(ctx, label, spawnDir string) (string, error)`, #677; `spawnDir` added #685) — adapted from `*sessions.Pool.CreateIn` at the `cmd/pyry` boundary (`sessionMinter`, mirroring `poolResolver`) so the sub-package still imports no `internal/sessions`. `sessionMinter` is also the **sole validator** of the phone-requested spawn workdir: `resolveSpawnDir` confines a set `spawnDir` to `$HOME` + trust-marks the realpath (#685, mirroring the daemon bootstrap's confine→trust posture) before `CreateIn`, and an escape (wrapping `handlers.ErrSpawnDirRejected`) maps to a non-retryable `protocol.malformed` reply with no half-bound row. The `send_message` handler gains a third consumer-declared seam, `handlers.SessionRouter` (`Route(conversationID) (TurnWriter, error)`, #678), which *returns* a `TurnWriter` rather than any `internal/sessions` type — adapted at `cmd/pyry` by `sessionRouter`/`boundSession` beside `sessionMinter`, preserving the import-clean property. `SendMessage` first resolves the frame's `ConversationID` to its bound session via `SessionRouter` (#678 — rejecting an unbound conversation *before* any `Pool.Lookup`, since `Pool.Lookup("")` would otherwise hand back the shared bootstrap; the routing target `CurrentSessionID` is server-stored, never phone-writable). Since #721 it then **enqueues** the turn into the daemon's `internal/msgqueue` backlog and **acks on acceptance** rather than delivering synchronously: the `Activate`-then-`WriteUserTurn` step (with the `#396` lazy-respawn of an idle-evicted **bound** session, re-activated through the cap-enforcing `Pool.Activate`) moved to the daemon's `msgqueue` drain (`cmd/pyry.newInboundDeliver`), which delivers one message at a time paced by claude reaching idle. The handler gained a fourth consumer-declared seam, `handlers.Enqueuer` (`EnqueueDelivery(conversationID, messageID, text, delivery string) uint64` — widened from a bare `Enqueue(conversationID, text)` by #2038's `delivery` and #2092's `messageID` — satisfied by `*msgqueue.Queue`, defined consumer-side so `handlers/` imports no `internal/msgqueue`). The synchronous reject mapping is unchanged and runs **before enqueue**: an unknown conversation → `conversation.not_found` (not retryable); an unbound/dangling session → `protocol.CodeServerBinaryOffline` with `Retryable=true`; a routable send → `ack`. A post-ack delivery failure is absorbed and retried by the drain, not surfaced as a wire reply (see [features/conversation-session-binding-routing.md § Enqueue-and-ack (#721)](conversation-session-binding-routing.md#enqueue-and-ack-721), [features/msgqueue-package.md](msgqueue-package.md), [codebase/721.md](../codebase/721.md)). The outbound assistant-turn bridge still taps the bootstrap PTY — an explicit phased-migration asymmetry, see [features/conversation-session-binding.md](conversation-session-binding.md#edge-cases--limitations).

**`SendMessage` joins a message's `attachment_ids` to claude's prompt (#2038).** A fifth consumer-declared seam, `handlers.AttachmentResolver` (`func(conversationID, attachmentID string) (path string, ok bool)`, comma-ok rather than `(string, error)`), is adapted at `cmd/pyry/relay.go` over `attachments.ResolvePath` bound to `resolveInstanceDirPath(w.instanceName)` — the same value `attachmentIntake` already gets. **Comma-ok is a logging control, not a style choice:** `ResolvePath`'s shape-invalid refusal formats the raw client-supplied id into its error, so dropping the error at the adapter makes it *impossible* for the handler to log it, rather than merely a rule to remember; a `nil` resolver refuses every id (fail-closed). Check order is load-bearing: the published `MaxAttachmentIDsPerMessage = 32` bound is enforced (raw element count, before any dedup) *before* `SessionRouter.Route`, and `Route` runs *before* any id is resolved — `Route` is what discharges `ResolvePath`'s precondition that `conversationID` be the conversation the session is already on, never one a client asserted, so validating the bound first (a stateless frame-shape rule) avoids moving daemon state (`Route` stamps the cursor) on a frame that already violates a published contract. A repeated id is deduplicated on first-occurrence, resolved once, and named once in the prompt — refusing a repeat would punish a client for something harmless, and dedup bounds resolution to at most 32 directory reads regardless of list length; the bound is still checked against the *raw* count first, since `MaxAttachmentIDsPerMessage` publishes that it counts elements, not distinct ids (33 copies of one id is over bound). An id that fails to resolve — unknown, non-canonical, or filed under a different conversation, all one sentinel — refuses the **whole message**, all-or-nothing, with the same widened `attachment.not_found` (#2036) naming no id, no count and no path (a per-id answer would turn one `send_message` into a batch existence-probe for up to 32 ids). A resolving list is composed into a delivery string — the user's text verbatim, then a blank line, then a daemon-authored block naming each path and directing a read — and travels to `msgqueue.EnqueueDelivery` (not `Enqueue`) as the delivery argument, so what a client reads back via `queue_state` stays the user's own text; see [features/msgqueue-package.md § Security](msgqueue-package.md#security). Both AC decisions (refuse-over-bound, dedupe-on-repeat) are published in `docs/protocol-mobile.md` § Naming a message's attachments. **Confirmed live (#2039):** a real claude opens this path — outside its own workspace cwd — with no permission modal, and echoes small file contents byte-exact; see [e2e-realclaude-interactive-stream-attachment-read-test-go.md](e2e-realclaude-interactive-stream-attachment-read-test-go.md).

Wire-spec source-of-truth: `docs/protocol-mobile.md` § Authentication, § Connection lifecycle, § Worked example. When that document changes, this package changes.

## Surface

```go
package relay

type Config struct {
    ServerID      identity.ServerID // caller resolves via identity.LoadOrCreate
    RelayURL      string            // must be wss:// (ws:// accepted only when AllowInsecureScheme=true)
    BinaryVersion string
    Logger        *slog.Logger      // required

    // AllowInsecureScheme, when true, lets RelayURL use ws:// in addition
    // to wss://. Test-only seam for e2e suites pointing the daemon at an
    // httptest-hosted fakerelay over plaintext. Production callers leave
    // this false; cmd/pyry flips it only when the operator sets
    // PYRY_ALLOW_INSECURE_RELAY=1 (#301).
    AllowInsecureScheme bool
}

type Connection struct { /* opaque */ }

func Connect(ctx context.Context, cfg Config) (*Connection, error)

func (*Connection) Frames() <-chan protocol.RoutingEnvelope // closes on lifecycle exit
func (*Connection) Send(env protocol.RoutingEnvelope) error  // binary→relay outbound
func (*Connection) CloseConn(connID string, code uint16) error // #308; close one phone conn
func (*Connection) Connected() bool                          // #874; level poll, passthrough to transport.Client.IsConnected
func (*Connection) Reconnected() <-chan struct{}              // #875; edge signal, fires once per fresh conn (initial + every reconnect)
func (*Connection) Wait() error                              // blocks until exit
func (*Connection) Close() error                             // idempotent

var (
    ErrServerIDConflict = errors.New("relay: server-id conflict (close 4409)")
    ErrInvalidConfig    = errors.New("relay: invalid config")
)
```

Run pattern:

```go
conn, err := relay.Connect(ctx, relay.Config{
    ServerID:      sid,                      // identity.LoadOrCreate
    RelayURL:      cfg.RelayURL,             // from internal/config
    BinaryVersion: version,                  // build-time ldflag
    Logger:        log,
})
if err != nil {
    return fmt.Errorf("relay.Connect: %w", err) // ErrInvalidConfig
}
defer conn.Close()

go func() {
    for env := range conn.Frames() {
        dispatch(env) // future ticket
    }
}()

if err := conn.Wait(); err != nil {
    if errors.Is(err, relay.ErrServerIDConflict) {
        // another pyry holds this server-id; operator escalation, exit non-zero
        return err
    }
    // ctx.Err() or wrapped transport error
    return err
}
```

## Headers (locked at wire-spec)

Built inside `Connect` from `Config`; the caller does NOT supply them:

| Header | Value |
|---|---|
| `x-pyrycode-server` | `string(cfg.ServerID)` (UUIDv4 from `internal/identity`) |
| `x-pyrycode-version` | `cfg.BinaryVersion` |
| `user-agent` | `pyry/<cfg.BinaryVersion>` |

Source: `docs/protocol-mobile.md` § Authentication. The relay accepts on first-claim-wins; if the server-id is already claimed it closes with status `4409` and the package surfaces `ErrServerIDConflict` from `Wait()`.

## Connection establishment (on WS upgrade)

The binary↔relay leg is **content-blind** — there is no application-layer `hello`/`hello_ack` ceremony on it (retired #582). The relay registers the binary's server-id from the `x-pyrycode-server` request header and claims the slot on WS upgrade; under v2 a `hello_ack` would be AEAD-sealed application data the relay holds no key for. So the conn is established the moment the upgrade fires, and `run()` goes straight to forwarding.

```
Connect()              → goroutine spawns
   │
   └── on every fresh transport conn (signalled by transport.Client.Connected()):
         │
         ├── log Info "relay: conn established" (server_id)
         │
         └── forwardFrames(): Receive → unmarshal RoutingEnvelope → c.frames
                                │
                                └── any err → return; outer select catches next Connected → re-enter forwardFrames
```

WS close-code `4409` (server-id conflict) is classified terminal independently of any frame exchange — it rides the transport's `FatalCloseCodes: [4409]` and surfaces as `ErrServerIDConflict` from `Wait()` (see Reconnect semantics).

> The *phone↔binary* `hello`/`hello_ack` is a different leg and survives — it is carried E2E-encrypted as Noise_IK early-data, relay-blind, and validated by the Noise_IK v2 handshake in [`v2-session-manager.md`](v2-session-manager.md) (see § Auth below). Only the binary↔relay leg ceremony was retired. Encryption is not the device-authorisation boundary: a rejected-token initiator still receives and can decrypt the ack needed to establish the encrypted rejection channel, so [`handleNoiseInit`](v2-session-manager-state-machine-noise-init-happy-and-failure-path.md) validates the device token before adding daemon-host metadata such as `workspace_root`; rejected acks omit it.

## Reconnect semantics

| Cause | Behaviour |
|---|---|
| Transport drop (`1011`, `1006`, network error) | Inherits `internal/transport`'s backoff (1s/2s/4s/8s/16s/30s cap ±20% jitter, reset after ≥60s uptime). On each fresh conn, re-enters forwarding directly (no handshake). Frames flow on the SAME `Frames()` channel before and after reconnect — consumers see a contiguous in-order stream. |
| WS close `4409` (server-id conflict) | `Wait()` returns `ErrServerIDConflict`. NO reconnect. Operator escalation: another pyry holds the same server-id, or a stale connection on the relay side has not yet been reaped (relay's 30-second grace window). |
| Malformed JSON on an inbound frame | Logged WARN; frame dropped at the trust boundary; loop continues. Single bad frame does NOT tear the conn. |
| `ctx` cancelled / `Close()` called | Clean shutdown. `Frames()` closes; `Wait()` returns `ctx.Err()` or `nil`. |

### Reconnect-state accessors for the v2 push drain (#874 `Connected`, #875 `Reconnected`)

`internal/relay`'s [`V2SessionManager`](v2-session-manager.md) needs to know the binary↔relay transport's live-conn state to avoid burning a Noise send-nonce on a dead link ([`Transport-down hold on the push drain`](v2-session-manager.md#transport-down-hold-on-the-push-drain-874--connected-probe--transportdown)). It cannot observe `transport.Client.Connected() <-chan struct{}` directly — that channel is documented single-observer, and `Connection.run()` is already its sole consumer (it re-enters `forwardFrames` on each fire). So `Connection` re-exposes the transport's live-conn state one layer down, in two complementary shapes:

- **`Connected() bool` (#874)** — a synchronous level poll, one-line passthrough to `transport.Client.IsConnected()`. Answers "is the leg up *right now*" at an arbitrary call site.
- **`Reconnected() <-chan struct{}` (#875)** — an edge-triggered signal, cap-1 drop-on-full, re-broadcasting `client.Connected()`'s fresh-conn edge from inside the same `run()` arm that already consumes it (right after the "conn established" log, before the blocking `forwardFrames` call). Fires on the *initial* connect too, not only reconnects — harmless, since it triggers at most one empty drain pass.

Both are wired in `cmd/pyry/relay.go`'s `V2SessionConfig` literal (`Connected: conn.Connected`, `Reconnect: conn.Reconnected()`) and are otherwise unconsumed inside this package — `Connection` itself never reads either. See [`codebase/874.md`](../codebase/874.md) / [`codebase/875.md`](../codebase/875.md).

## Error model

| Method | Returns |
|---|---|
| `Connect` | `nil` on success after sync validation; `ErrInvalidConfig` (wrapped, names the missing field or wrong scheme) on bad config. Never blocks — the `run` goroutine handles the dial. |
| `Frames` | `(env, ok)`. `ok=false` when the lifecycle exits. |
| `Wait` | `ErrServerIDConflict` (fatal 4409), `ctx.Err()` (graceful shutdown), `nil` (Close called), or a wrapped `transport` error (unexpected halt). |
| `Close` | Always `nil`. Idempotent. |

Sentinels are distinguished via `errors.Is`. `ErrServerIDConflict`'s string contains no dynamic content — no token / server-id leakage via error messages.

## Configuration constraints

- **`RelayURL` must be `wss://`** in production. Non-wss schemes are rejected as `ErrInvalidConfig` at `Connect` time. Server-id is sent in a request header; a `ws://` misconfiguration would disclose it in cleartext. Server-id is not a credential per `docs/protocol-mobile.md` § Security model Threat 2, but the cleartext-disclosure defense is cheap and structural.
- **`AllowInsecureScheme = true` is the explicit test-only opt-in** (#301). Relaxes the scheme check to accept `ws://` in addition to `wss://`. `cmd/pyry` flips it only when the operator sets `PYRY_ALLOW_INSECURE_RELAY=1` (env-gated; no flag, no config-file key). Production default stays `wss://`-only.
- **`RelayURL` is treated as a *base* — the daemon appends `/v1/server` when it carries no meaningful path** (#631). The package owns the binary's endpoint knowledge: an unexported `resolveDialURL(raw, allowInsecure)` is the single home for relay-URL handling (parse → scheme-check → path-append), called inside `Connect` before the dial. If `u.Path == "" || u.Path == "/"` it sets `u.Path = "/v1/server"`; any operator-supplied path (`/v1/server`, `/v2/server`, `/custom`) passes through unchanged. `u.String()` reconstruction preserves host/port/query/userinfo (`wss://h/?x=1` → `wss://h/v1/server?x=1`). This mirrors the phone's `/v1/client` convention (`fakephone.Dial` does `baseURL+"/v1/client"`), so the single base `relay_url` in `~/.pyry/config.json` serves **both** `pyry pair` (uses it as a base for the QR) and the daemon (dials it) with **no `PYRY_RELAY_URL` override** required. The shipped default `DefaultConfig().RelayURL = "wss://relay.pyrycode.dev"` is itself a base URL — before #631 it silently 404-looped the daemon forever (the relay serves only `/v1/server`, `/v1/client`, `/healthz`). The `/v1/server` literal lives only here; `transport` stays protocol-agnostic. See [codebase/631.md](../codebase/631.md).
- **All four required `Config` fields must be set.** `ServerID` is caller-resolved via `internal/identity.LoadOrCreate` before `Connect` — the relay package never touches the on-disk store, keeping it free of pairing/storage concerns. `Logger` is required (nil → `ErrInvalidConfig`); structured slog only.

## Logging discipline

| Event | Level | Fields |
|---|---|---|
| `relay: conn established` | Info | `server_id` |
| `relay: malformed routing envelope; dropping` | Warn | `err` |
| `relay: forwardFrames exiting` | Debug | `err` |

Forbidden everywhere: `token`, `payload`, raw `frame` bytes, full `Headers` map (would leak `x-pyrycode-server` on every line; `server_id` is the operator-actionable subset). The transport's existing lifecycle logs (`transport: connected`, `transport: dial failed, backing off`, `transport: disconnected`) cover the conn lifecycle.

## Edge cases and gotchas

- **`Frames()` channel is unbuffered.** A slow consumer applies back-pressure all the way to the relay's send buffer. The dispatcher (future ticket) must keep `Frames()` drained continuously.
- **Reconnects are invisible to `Frames()` consumers.** The same channel persists across reconnects; frames resume on the new conn directly (no handshake).
- **`Connect` is sync-validate, async-run.** It returns immediately after `Config` validation. The connection is NOT yet established; observe `Frames()` to consume inbound frames, or call `Wait()` to block on terminal classification.
- **Caller is responsible for `Close()`** during shutdown to release resources; `ctx` cancellation also drains the lifecycle.
- **Race between `Connected` signal and conn drop:** if the conn drops right after the relay observes `Connected`, `forwardFrames`'s first `Receive` returns `ErrDisconnected`, `run` loops and catches the next `Connected` or `transportErrCh`. No stuck state.

## Test surface

`internal/relay/connection_test.go` (~700 LOC, stdlib + `coder/websocket`):

- `newTestRelay(t)` — `httptest.NewServer` + `websocket.Accept` upgrader for a **content-blind forwarder**: it registers each conn on upgrade (`ConnCount()` / `connectedCh` / header capture) and pumps `outboundFrames` to the binary; it never reads a hello or sends a `hello_ack`. Behaviors: `behaviorForward` (default — register + forward), `behaviorCloseImmediately4409` (close 4409 on accept), `behaviorDropOnConnect` (`CloseNow` right after upgrade → transport reconnects).
- `testLogger(t)` — discarding `slog.Logger`.
- `connectWithClient(ctx, cfg, client) *Connection` — unexported test seam that wraps a `*transport.Client` (typically wired to the `httptest` URL via a custom `dialFn`) and bypasses production's URL/scheme validation. Production callers use `Connect`. The newer `Config.AllowInsecureScheme` field (#301) is a *production-shaped* path through `Connect` for callers that just need `ws://` (e2e); two seams, two purposes — keep both.
- `waitConnCount(t, relay, n, timeout)` — readiness helper polling `relay.ConnCount() >= n`; replaced the `HelloEnv(0)` poll loops when the binary-sent hello was retired (#582).

Pinned behaviour:

- `TestConnect_ReachesForwardingNoAck` (#582) — against a relay that never sends a `hello_ack`, a frame pushed via `relay.outboundFrames` arrives on `c.Frames()` AND `ConnCount == 1` (no recycle); pins "reaches frame-forwarding, no ack, no recycle".
- `TestHeaders_Set` — relay introspects `x-pyrycode-server`, `x-pyrycode-version`, `user-agent: pyry/<version>`.
- `TestServerIDConflict_FatalNoReconnect` — 4409 → `Wait()` returns `ErrServerIDConflict`; relay's `connCount` stays at 1.
- `TestTransportDropOnConnect_Reconnects` (#582) — relay `CloseNow`s right after upgrade; transport reconnects (`ConnCount >= 2`).
- `TestTransportDropPostConnect_Reconnects` (#582) — proves post-reconnect frames flow on the SAME `Frames()` channel (pins the `ErrDisconnected` wedge fix in the transport additions; without it, `forwardFrames` would wedge in the previous-conn `Receive` and never let `run` observe the next `Connected`).
- `TestFrames_AfterConnect_InOrder` — three frames delivered in arrival order.
- `TestClose_ShutsDownCleanly` — `Close()` drains `Frames()` and `Wait()` returns; goroutines exit.
- `TestContextCancel_ShutsDownCleanly` — `cancel(ctx)` drains the lifecycle.
- `TestConfig_Validation_TableDriven` — each missing required field; `ws://` / `http://` / unparseable schemes → `ErrInvalidConfig` (with `AllowInsecureScheme=false`).
- `TestResolveDialURL` (#631) — table pinning the relay-URL handling: base / `"/"` → `/v1/server` appended; `/v1/server` / `/v2/server` / `/custom` passthrough (AC#4); query preserved on both append and passthrough; `ws://`/`http://` → wraps `ErrInvalidConfig` (message contains `"wss"`); `ws://` with `allowInsecure=true` → accepted + appended; `"://broken"` → wraps `ErrInvalidConfig` (message contains `"RelayURL parse"`).
- `TestConfig_AllowInsecureScheme` (#301) — pins that `ws://` passes `Connect` when `AllowInsecureScheme=true`; `Close` cancels the lifecycle before the bogus URL's async dial surfaces.
- `TestCloseConn_WireShape` (#308) — `CloseConn("c-7", 4401)` produces one outbound frame whose JSON has `conn_id=="c-7"`, `close_code==4401`, and no `frame` key.
- `TestCloseConn_PropagatesNotConnected` (#308) — pre-Connect state returns `transport.ErrNotConnected` verbatim.
- `TestTransportReconnect_SignalsReconnected` (#875) — mirrors `TestTransportDropPostConnect_Reconnects`; asserts `conn.Reconnected()` receives a signal on the initial connect and again after a post-connect drop→reconnect.

## Close-conn surface (`CloseConn`, #308)

`(*Connection).CloseConn(connID string, code uint16) error` asks the relay to close the named phone conn with the given WS close code. Builds a close-only `RoutingEnvelope{ConnID, CloseCode}` (no `Frame`), marshals, and forwards via `transport.Client.Send`. Returns `transport.ErrNotConnected` / `ErrDisconnected` / `ErrClosed` verbatim — fire-and-forget at this layer; the per-conn close ack is implicit (no further inbound frames will arrive for `connID`).

The dispatcher's auth-reject path (#308) does NOT call `CloseConn`. Instead it publishes a single `RoutingEnvelope` with `Frame=<error>` AND `CloseCode=4401` onto `dispatch.Outbound()`; the existing forwarder's one `conn.Send(env)` is the atomic wire op. `CloseConn` is the surface reserved for direct callers that want close-without-payload (none today; the idle/inactivity sweep hinted at in #307's Open Questions is the anticipated future consumer).

## Push-wake surface (`SendPushWake`, #2564)

`(*Connection).SendPushWake(platform, token string) error` asks the relay to send a platform push to an absent device — the daemon-authored half of `docs/protocol-mobile.md` § `push_wake`. Unlike `CloseConn` and `Send`, the envelope is **relay-addressed, not conn-addressed**: it carries no `conn_id` and no `frame`. It can't reuse `protocol.RoutingEnvelope` for this — that type's `conn_id` and `frame` fields have no `omitempty`, so it always serializes both keys, which is off-spec for an envelope meant to carry neither. `SendPushWake` marshals its own unexported `pushWakeEnvelope{PushWake: pushWakeRequest{Platform, Token}}` via `marshalPushWake`, then forwards through the same `transport.Client.Send` path as `CloseConn`, returning the transport sentinels unchanged. A marshal failure returns a fixed sentinel rather than a wrapped encoder error, so no error from this method can carry the token; neither send path logs the token. The envelope is never sealed in a Noise session — the relay is its only reader.

### Daemon-side trigger (`cmd/pyry`'s `pushWaker`, #2564)

The daemon wakes an absent phone from the two existing fan-out points rather than a new observer:

- `interactiveTurnEmitterV2.Handle`'s `turnevent.TurnEnd` arm, right after `endTurn()` — connected phones already got the `turn_end` from that fan-out; the wake reaches the ones that don't have an open session.
- `streamApprovalBridge.Surface`'s live `modal_shown` broadcast, right after it fires. Reconnect replay (`reconcileModals`) pushes outstanding modals directly and never passes through `Surface`, so replaying a modal on reconnect never triggers a wake.

Both call a nil-safe `Trigger()` on a `pushWaker` — a non-blocking, cap-1 channel send — so neither fan-out ever waits on a device-registry read, an open-session snapshot, or a relay write. The actual pass runs on the waker's own goroutine, started in `startRelayV2` beside `conn`/`registry`/`mgr` and exiting on the daemon ctx.

A device is woken when its `Platform` is `fcm` with a non-empty `PushToken`, no open v2 session belongs to it, and it was not already woken inside a 30 s window — a failed send does not start that window, so the next trigger retries it. "Belongs to it" is decided on `ActiveConn.DeviceTokenHash` (added by this ticket: the `TokenHash` of the device the handshake authenticated, filled in `handleActiveConns` from `s.device`), **never** the hello-claimed `ActiveConn.DeviceName` — a phone could otherwise silence another device's wake by claiming that device's name in `hello`. See [`v2-session-manager-surface.md`](v2-session-manager-surface.md) for the `ActiveConn` field list.

**Lesson: a nil `ActiveConns` snapshot after ctx cancellation is not "nobody is connected."** `V2SessionManager.ActiveConns` returns `nil` once its `ctx` is cancelled or `Run` has exited. Every prior consumer (broadcast, per-conn push) reads that as "reach nobody," which is harmless — an empty send target sends nothing. The push-wake trigger reads the *absence* of a device from that same snapshot as the positive signal "no open session, so wake it," which inverts the harmless case into "wake every device" at exactly the moment (shutdown) it must not. `wakeAbsent` re-checks `ctx.Err()` immediately after taking the snapshot and returns without sending if it is non-nil. Any future `ActiveConns` consumer that treats *absence from the snapshot* as a trigger for action, rather than as a filter that only narrows an existing intent, needs the same check.

## Auth: WS close-code constants (`auth.go`)

`auth.go` today holds only two exported constants, both consumed by the live Noise_IK v2 handshake in [`v2-session-manager.md`](v2-session-manager.md):

```go
const StatusUnauthorized websocket.StatusCode = 4401
const MsgInvalidToken = "device token not recognised; re-pair via pyry pair on the binary"
```

`StatusUnauthorized` is the WS close code `v2session_handshake.go` sends when the Noise_IK-embedded device token fails `devices.Registry.Validate`; `MsgInvalidToken` is the accompanying error message. Both are documented at `docs/protocol-mobile.md` § Error codes and reused unchanged since #249. No design change here — see [`v2-session-manager.md`](v2-session-manager.md) for the live auth contract, logging discipline, and concurrency model.

### History (removed, #1040)

`auth.go` originally (#249) also held a pure v1 first-frame token-validation predicate, `AuthenticateFirstFrame(env, token, reg, serverID, logger) (AuthOutcome, error)`, invoked once per phone conn on receipt of the first frame. #308 wired it via `internal/dispatch`'s `FirstFrameGate`; #318 extended it to thread the matched `*devices.Device` into the per-conn auth slot. That production wiring (the v1 relay dispatch branch) was removed in #913 slice 1, orphaning `FirstFrameGate`/`Dispatcher` with zero callers; #1039 deleted that dead machinery; #1040 deleted `AuthenticateFirstFrame` itself along with `AuthOutcome`, `ErrMalformedHelloFrame`, and the transitively-dead `buildResponse` helper, and `internal/relay/auth_test.go` in full — see [codebase/249.md](../codebase/249.md), [codebase/308.md](../codebase/308.md), [codebase/318.md](../codebase/318.md), [codebase/913.md](../codebase/913.md), [codebase/1039.md](../codebase/1039.md), [codebase/1040.md](../codebase/1040.md). The one kept-symbol test, `TestStatusUnauthorized_Value`, wasn't re-homed — it's subsumed by the live v2 close-code assertions in `v2session_test.go` / `v2session_debugbundle_test.go`.

## Handlers: per-envelope-type processors (`handlers/`, #250)

Split out to [`relay-package-handlers.md`](relay-package-handlers.md): the sub-package's first inhabitant (`register_push_token`) in full depth — behavioural contract, dedupe, logging discipline, test surface — plus `send_message`'s #2159 auto-naming seam and the deliberate-interface-duplication convention it established.

## Consumers and roadmap

- **Supervisor wiring** (#301): `cmd/pyry/main.go` + `cmd/pyry/relay.go` resolve the relay URL with precedence `-pyry-relay` > `PYRY_RELAY_URL` > `cfg.RelayURL` > `DefaultConfig`, load the server-id via `identity.LoadOrCreate(resolveServerIDPath(name))` (same on-disk file as `pyry pair`), call `relay.Connect`, and spawn one supervisor-owned goroutine that drains `Frames()` and reads `Wait()`. On `ErrServerIDConflict` the goroutine calls the shared `signal.NotifyContext` cancel, unwinding `pool.Run`; on any other terminal error it logs warn and exits without restart (transport-internal reconnect already absorbed all non-fatal closes); empty `relayURL` is the disabled-relay branch (info log, no goroutine). See [`codebase/301.md`](../codebase/301.md) for the full wiring + e2e harness extensions.
- **Outbound sending** (#307, landed): `(*Connection).Send(env protocol.RoutingEnvelope) error` marshals the routing envelope and forwards via `transport.Client.Send`. Caller wraps the inner `protocol.Envelope` in `RoutingEnvelope` (the dispatcher's `Conn.Send` does this from the inside). Returns `transport.ErrDisconnected` / `ErrNotConnected` / `ErrClosed` verbatim when the underlying conn is dropped — frames sent during a disconnected window are lost, which is consistent with the protocol's connection-lifecycle semantics (reconnect re-establishes the conn on WS upgrade, so per-conn state on the relay is implicitly the wrong frame of reference for retry). First consumer is `internal/dispatch` via the dispatcher's `Outbound()` forwarder in `cmd/pyry/relay.go`.
- **Relay-conn wiring (#308 + #318) — removed.** The v1 first-frame gate this bullet described (`dispatch.FirstFrameGate` extracting `RoutingEnvelope.Token`, calling `AuthenticateFirstFrame`, atomically publishing the close+error envelope) was production-live only until #913 slice 1; the orphaned machinery was deleted in #1039 and #1040. Token-based device auth today happens on the Noise_IK v2 handshake path — see [`v2-session-manager.md`](v2-session-manager.md) and § Auth above.
- **Per-message dispatch** (`internal/dispatch`, #307+#308+#318): consumes `Frames()`, runs the optional `FirstFrame` gate, decodes the inner `protocol.Envelope` and branches on `Type`, routing to the registered handler in `internal/relay/handlers/`. `list_conversations` (#303), `create_conversation` (#666), `rename_conversation` (#820), `promote_conversation` (#949), `delete_conversation` (#822), `archive_conversation` / `unarchive_conversation` (#881), `change_workspace` (#823), `create_workspace_folder` (#887), `recent_workspaces` (#888), `register_push_token` (#319), and `send_message` (#322) are wired in `cmd/pyry/relay.go`'s `V2SessionConfig.Handlers` map (`startRelayV2`) — the **sole** dispatch table since #913 removed the legacy v1 `d.Register` block and the `PYRY_MOBILE_V2` switch (`startRelay` is now a thin wrapper that calls `startRelayV2` directly); the rest of the #256 catalog (the assistant-turn-delivery half of `send_message`) is deferred; the `backfill_since` wire flow that was once part of it has since been removed as dead code, never wired to a handler — see [codebase/967.md](../codebase/967.md). (`rename_conversation`, `promote_conversation`, `delete_conversation`, `archive_conversation`/`unarchive_conversation`, `change_workspace`, `create_workspace_folder`, and `recent_workspaces` were not part of the original #256 catalog — later additions, #820/#949/#822/#881/#823/#887/#888. `promote_conversation` was the last member of the family to define its full wire surface (payload, reply type) without a registered handler — #949 closed that gap; every write verb in the group now reuses `conversation_updated` as its reply. `change_workspace` is the first of the group whose untrusted input is a filesystem path rather than a display string or bare id — see [codebase/823.md](../codebase/823.md); proven at the daemon boundary over the encrypted v2 wire by `TestRelayV2_ChangeWorkspace` (#980, split from the #960 verb-coverage sweep, structure copied from the merged #974 rename test), one of the group's two e2e tests that certify a path-confinement containment property — a round-trip asserting the reply/on-disk `Cwd` is the confined **realpath** (not the raw path sent), a rejection subtest asserting neither the raw nor `EvalSymlinks`-resolved form of an escaping path leaks on the wire, and a not-found subtest using a *valid* target so confinement doesn't mask the lookup miss — see [codebase/980.md](../codebase/980.md). `promote_conversation` (#949) deliberately does NOT follow `change_workspace`'s confine pattern despite also carrying a `Cwd`-shaped field: the payload `Cwd` is read and discarded, the channel inherits the scratch conversation's existing cwd — see [codebase/949.md](../codebase/949.md). `create_workspace_folder` (#887) is the first of the group that touches **no conversations registry at all** — it creates a directory and replies with a brand-new type, `workspace_folder_created`, rather than reusing `conversation_updated` — see [codebase/887.md](../codebase/887.md); proven at the daemon boundary over the encrypted v2 wire by `TestRelayV2_CreateWorkspaceFolder` (#981, split from the same #960 verb-coverage sweep, structure copied from the merged #980 change_workspace test), the group's other path-confinement e2e and the first to certify **two independent** fail-closed guards (confinement to `$HOME` and the name-shape "directly under parent" check) in one ticket — see [codebase/981.md](../codebase/981.md). `recent_workspaces` (#888) is the group's first **read** verb since `list_conversations` — takes the registry directly with no `cmd/pyry` adapter (unlike every write verb in the group), and is the only member of the group explicitly labeled not-security-sensitive — see [codebase/888.md](../codebase/888.md); proven at the daemon boundary over the encrypted v2 wire by `TestRelayV2_RecentWorkspaces` (#982, split from the same #960 verb-coverage sweep, structure copied from the merged #980/#981 templates minus their not-found and on-disk-reassertion blocks — this verb is a pure read with no error path and Saves nothing), the group's first read-verb e2e — see [codebase/982.md](../codebase/982.md). `TestEveryInboundV2TypeHasHandler` (`cmd/pyry/relay_guard_test.go`, #950) is a structural, always-on guard against the #949 failure class recurring: it AST-reads the actual `Handlers` map keys in `relay.go` plus the actual `case protocol.Type*:` labels of `dispatchAppFrame`'s switch (`internal/relay/v2session.go`, the pre-`dispatch.Route` interception surface below) and fails, naming the type, if any inbound v2 verb is registered in neither — see [codebase/950.md](../codebase/950.md).)

- **Reconnect-state accessors** (#874 `Connected` + #875 `Reconnected`, landed): sole consumer is `internal/relay.V2SessionManager` via the `V2SessionConfig.Connected` / `.Reconnect` optional seams wired in `cmd/pyry/relay.go`'s `startRelayV2`. See [§ Reconnect-state accessors](#reconnect-state-accessors-for-the-v2-push-drain-874-connected-875-reconnected) above.
- **Push-wake trigger** (#2564, landed): `cmd/pyry`'s `pushWaker` is `SendPushWake`'s sole caller, fed by `interactiveTurnEmitterV2` and `streamApprovalBridge`'s two fan-out points; it also reads `V2SessionManager.ActiveConns` for the open-session snapshot and `devices.Registry.List` for the paired-device set. See [§ Push-wake surface](#push-wake-surface-sendpushwake-2564) above.

## Dependencies

- `internal/transport` (#247) — generic WSS client. The additions #248 landed (`Config.FatalCloseCodes`, `Connected()`, `ErrDisconnected`, `ErrFatalClose`, `DropConn()`) are documented under [`features/transport-package.md`](transport-package.md).
- `internal/protocol` (#255 + #271) — `Envelope`, `RoutingEnvelope`, `TypeHello` / `TypeHelloAck` / `TypeError` constants, `CodeAuthInvalidToken`, `HelloAckPayload`, `ErrorPayload`. (`HelloServerPayload` is no longer referenced by this package since #582 retired the binary↔relay handshake — it now has no consumer anywhere in the repo.)
- `internal/devices` (#208 + #210) — `Registry.Validate(plain)` predicate (two-state, bumps `LastSeenAt` under `reg.mu`) consumed by `AuthenticateFirstFrame`. The plain→hash boundary lives in `devices.HashToken`; this package never hashes.
- `internal/identity` (#206 / #207) — `ServerID` newtype. `LoadOrCreate` is the caller's responsibility, not this package's.
- `github.com/coder/websocket` — only for the `StatusCode` type (typed-locally as `statusServerIDConflict` for 4409 and exported as `StatusUnauthorized` for 4401); consumers in `cmd/pyry` don't pull this transitively for headers alone.

## Out of scope

See [`codebase/248.md`](../codebase/248.md) § Out of scope for the deferred list.
