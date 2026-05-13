# Spec — dispatcher auth-gate + WS 4401 close on reject (#308)

## Files to read first

- `internal/relay/auth.go:77-148` — `AuthenticateFirstFrame(env, token, reg, serverID, logger)`. The pure predicate already returns `AuthOutcome{Response, CloseConn}`; spec re-uses verbatim. Note `ErrMalformedHelloFrame` for the JSON-undecodable inner-frame path, `StatusUnauthorized=4401`, `MsgInvalidToken`.
- `internal/dispatch/dispatch.go:38-336` — full scaffold from #307. New auth-gate hook plugs into `Run`/`runConn` per-conn flow; `handleOne` (line 280) is the dispatch point that grows a "first frame" branch. Existing `sendError` helper (line 313) is the model for the gate's outbound build.
- `internal/dispatch/dispatch.go:149-152` — `connState{conn, input}`. Spec adds a per-conn `gateRun bool` to track first-frame-handled state.
- `internal/relay/connection.go:170-183` — current `Connection.Send(env protocol.RoutingEnvelope) error`. The new `CloseConn` method is a sibling that builds a close-intent routing envelope and forwards via the same `transport.Client.Send` path.
- `internal/protocol/envelope.go:40-43` — `RoutingEnvelope{ConnID, Frame}`. Spec extends with two optional fields. `omitempty` on both preserves backwards-compatible JSON for existing fixtures.
- `internal/devices/registry.go:25-53, 170-179` — `Registry.Load(path)` and `FindByTokenHash`. `internal/devices/auth.go:32-46` — `Registry.Validate(plain) (Device, bool)`; AuthenticateFirstFrame already composes this.
- `cmd/pyry/relay.go:57-146` — current `startRelay`. Spec inserts registry load + gate closure construction; the existing 3-goroutine wiring (dispatcher / forwarder / wait classifier) is unchanged. `cmd/pyry/pair.go:30-36` — `resolveDevicesPath(instanceName)` already returns `~/.pyry/<sanitized>/devices.json`.
- `internal/e2e/internal/fakerelay/fakerelay.go:269-337, 482-518` — `handlePhone` captures `x-pyrycode-token` from request headers; `phoneRecvPump` wraps phone frames as RoutingEnvelopes. Spec adds Token injection on the first frame per conn_id and CloseCode handling on the binary→phone path.
- `internal/e2e/internal/fakephone/fakephone.go:108-138` — `Receive` returns on conn close. Spec exposes the WS close status so the e2e can assert 4401.
- `internal/e2e/relay_test.go:55-92` — pattern for daemon-vs-fakerelay e2e (spawn pyry, `StartInWithEnv`, persisted server-id, `LastBinaryHello` poll). New auth-reject test mirrors this shape.
- `docs/protocol-mobile.md:85-122, 540-552` — phone→relay→binary handshake spec + WS close code table. Token plumbing and CloseCode are wire-spec additions documented here.
- `docs/PROJECT-MEMORY.md:20` — Refusal-to-wire-code mapping at the call site convention (auth gate honors it: handler returns sentinels, dispatcher emits `Code*` strings).

## Context

`internal/relay.AuthenticateFirstFrame` (#249) is a pure predicate that returns the accept/reject decision plus a fully-formed response envelope. No code calls it yet. This slice plugs it into the dispatcher scaffold from #307 so the dispatcher invokes the predicate on the very first frame for each new `conn_id`, forwards the response, and — on reject — also closes that phone's WS with code 4401 (`docs/protocol-mobile.md` § Error codes).

Two design questions are settled here that #249's doc-comment explicitly deferred:

1. **How the token reaches the binary.** Choice (a) from `AuthenticateFirstFrame`'s comment: **extended routing envelope**. The relay (production) and `fakerelay` (e2e) populate `RoutingEnvelope.Token` on the FIRST phone→binary frame for each `conn_id`; the field is empty on every subsequent frame and on every binary→phone frame. This keeps the phone-facing wire spec untouched (the phone already sends the token via the `x-pyrycode-token` HTTP header at WS upgrade) and concentrates the change on the binary↔relay leg, which is the only leg where the token needs to traverse application bytes.

2. **How the binary signals "close this conn_id with code N" to the relay.** The wire mechanism is an extension of the existing routing envelope, not a new envelope type: `RoutingEnvelope.CloseCode uint16` (optional, omitempty). When non-zero on a binary→relay frame, the relay forwards `Frame` (if non-nil) to the phone and then closes that phone's WS with `CloseCode`. This makes "write error then close" a single atomic wire op — no race between the error frame and the close — and avoids a sum-type breaking change to `dispatch.Outbound() <-chan protocol.RoutingEnvelope` from #307.

The Go-side surface that the AC mandates ("`Connection` exposes a per-`conn_id` close-with-code surface") lands as a new method `func (c *Connection) CloseConn(connID string, code uint16) error` — a thin wrapper that builds a close-only `RoutingEnvelope` (no `Frame`) with `CloseCode` set, then `client.Send`s it. The dispatcher's reject path doesn't itself call `CloseConn`; instead the dispatcher publishes one routing envelope with BOTH `Frame=<error>` AND `CloseCode=4401` to `Outbound`, and the existing forwarder (`conn.Send`) handles the wire op. `CloseConn` is the explicit surface for direct callers (the AC plus any future close-conn-with-no-payload paths, e.g. idle/inactivity sweep).

## Design

### Package placement

No new packages. Changes are spread across three existing leaf packages plus the cmd/pyry wiring file:

- `internal/protocol/envelope.go` — additive wire fields
- `internal/relay/connection.go` — new `CloseConn` method
- `internal/dispatch/dispatch.go` — first-frame gate hook + reject emission
- `cmd/pyry/relay.go` — load registry, construct gate closure, pass to dispatcher

Plus e2e infrastructure:

- `internal/e2e/internal/fakerelay/fakerelay.go` — Token injection + CloseCode honor
- `internal/e2e/internal/fakephone/fakephone.go` — expose received close status
- `internal/e2e/relay_auth_test.go` — new e2e test (auth-reject path)

### Wire-protocol additions

Two optional fields on `protocol.RoutingEnvelope`:

```go
type RoutingEnvelope struct {
    ConnID string          `json:"conn_id"`
    Frame  json.RawMessage `json:"frame"`

    // Token carries the phone's device-pairing token from the relay to
    // the binary on the FIRST phone→binary frame for a given ConnID
    // only. Empty on subsequent frames and on every binary→phone frame.
    // Populated by the relay from the phone's `x-pyrycode-token` HTTP
    // header at WS upgrade; never echoed back to the phone. Wire spec:
    // docs/protocol-mobile.md § Routing envelope.
    Token string `json:"token,omitempty"`

    // CloseCode, when non-zero on a binary→phone routing envelope, asks
    // the relay to forward Frame (if non-nil) to the phone and then
    // close that phone's WS with this WS close code. Zero on every
    // phone→binary frame. Wire spec: docs/protocol-mobile.md § Routing
    // envelope, § Error codes (close-code row 4401).
    CloseCode uint16 `json:"close_code,omitempty"`
}
```

Both fields are `omitempty` so existing JSON fixtures and wire bytes remain unchanged for non-auth, non-close paths. JSON decoders are non-strict (no `DisallowUnknownFields` anywhere in the codebase), so a binary running against an older relay tolerates missing fields and vice versa.

`docs/protocol-mobile.md` § Routing envelope grows two short paragraphs documenting these fields and their direction-restricted lifetimes (Token: relay→binary only, first frame only; CloseCode: binary→relay only, applies to the named phone WS). The wire example JSON in lines 104-109 and 115-119 stays as-is — the new fields are optional and the canonical example doesn't need them.

### `Connection.CloseConn`

New method on `internal/relay.Connection`. Signature:

```go
// CloseConn asks the relay to close the named phone conn with the
// given WS close code. Builds a close-only routing envelope (no Frame)
// with CloseCode set and forwards via transport.Client.Send. Returns
// transport.ErrDisconnected when the underlying WS is currently
// dropped (caller decides whether to retry; transport reconnect runs
// asynchronously). Wire mechanism: docs/protocol-mobile.md § Routing
// envelope (CloseCode).
//
// CloseConn does NOT block on the relay's close-frame being delivered
// to the phone — the request is fire-and-forget at this layer. The
// per-conn close ack is implicit (no more inbound frames will arrive
// for connID).
func (c *Connection) CloseConn(connID string, code uint16) error
```

Implementation: marshal `RoutingEnvelope{ConnID: connID, CloseCode: code}` (Frame stays the zero `json.RawMessage`, which `omitempty` would NOT strip because `[]byte(nil)` is non-zero — see Open Questions for the `json.RawMessage` zero-value subtlety), then `c.client.Send(raw)`. Document that `Frame` is intentionally omitted; only the relay-side wire reader uses `CloseCode`.

`CloseConn` is the AC-mandated surface. The dispatcher's reject path does NOT call it — instead the dispatcher publishes a single RoutingEnvelope with both Frame and CloseCode set to `Outbound`, which the existing forwarder (`conn.Send`) handles atomically. `CloseConn` is the right method for callers who want close-without-payload (none today; reserved for the idle/inactivity sweep hinted at in #307's Open Questions).

### Dispatcher — first-frame gate

`dispatch.Config` grows one optional field:

```go
type Config struct {
    Frames         <-chan protocol.RoutingEnvelope
    OutboundBuffer int
    Logger         *slog.Logger

    // FirstFrame, if non-nil, is invoked on the FIRST inbound frame for
    // every new conn_id, before normal handler-table dispatch. The
    // dispatcher uses the returned outcome to either (a) forward
    // Response and continue dispatching subsequent frames on this conn
    // normally, or (b) forward Response with CloseCode set and stop
    // the per-conn goroutine. Nil disables the gate (every frame goes
    // straight to the handler table — the pre-#308 behavior).
    FirstFrame FirstFrameGate
}

// FirstFrameGate is the per-conn first-frame interceptor. Called once
// per conn_id with the inbound envelope; returns the response envelope
// the dispatcher should forward plus a close-or-keep decision.
type FirstFrameGate func(ctx context.Context, env protocol.RoutingEnvelope) FirstFrameOutcome

type FirstFrameOutcome struct {
    // Response is the routing envelope to forward to the relay. The
    // dispatcher publishes Response verbatim (its ConnID is expected
    // to match the incoming env.ConnID; the gate owns ID/InReplyTo/TS
    // construction via relay.AuthenticateFirstFrame). Required when
    // Err is nil.
    Response protocol.RoutingEnvelope

    // CloseConn, when true, causes the dispatcher to set
    // Response.CloseCode = Code before publishing, and to stop the
    // per-conn goroutine after Response is sent.
    CloseConn bool
    Code      uint16 // WS close code; required when CloseConn is true

    // Err signals a gate-level failure (e.g. the inbound frame was
    // malformed). The dispatcher falls through to its existing
    // malformed-frame refusal path (protocol.malformed; no
    // in_reply_to) and does NOT publish Response. The per-conn
    // goroutine continues — the gate's "first frame" status is not
    // consumed on Err, so a retry from the phone gets another chance.
    Err error
}
```

The gate runs from inside `runConn`'s per-conn goroutine, which is what already serializes frames for a single conn_id. Existing `handleOne` is renamed and extended:

- New field on `connState`: `gateRun bool` (false at creation).
- `runConn`'s for-loop pulls a routing envelope from `st.input`, checks `st.gateRun`:
  - **First time** (`gateRun == false`): set `gateRun = true`; if `d.cfg.FirstFrame != nil`, invoke the gate, then act on the outcome. If nil, fall through to normal `handleOne` (preserves the #307 behavior for tests / future configurations).
  - **Subsequent frames**: call `handleOne` (the existing #307 path).
- On `outcome.CloseConn`: take `outcome.Response`, set `Response.CloseCode = outcome.Code`, push to `d.outbound`, then return from `runConn` (the per-conn goroutine exits; the dispatcher's WaitGroup decrement happens via the existing `defer wg.Done()`). The `st.input` channel is left intact — pending or future frames for this conn drain into a goroutine that's already gone, which would block the demux. To avoid that, the dispatcher MUST drain `st.input` after exit signal. See Concurrency.

The gate runs ON the per-conn goroutine, NOT on the demux goroutine. This preserves the single-writer invariant for the conn's outbound and isolates a slow gate to one conn (a slow gate cannot stall other conns' first frames). `relay.AuthenticateFirstFrame` is a pure in-memory call (no I/O, no goroutines), so "slow gate" is hypothetical, but the architectural property matters.

### Concurrency model — closed-conn drain

After a per-conn goroutine exits on `CloseConn`, the connState remains in `d.conns` so subsequent frames for the same `conn_id` don't re-create the goroutine and re-run the gate. The dispatcher MUST prevent the demux from blocking on `st.input <- env` when there's no reader.

Approach: mark `connState` as `closed bool` (set under `d.mu` from the per-conn goroutine immediately before exit) and have the demux's "send to st.input" branch check the flag — if `closed`, drop the frame silently (log at Debug with `conn_id`/`type` for diagnostics). The drop is safe: the relay has been asked to close that phone WS; any further inbound frames are racy stragglers and have nowhere to go.

Concretely, extend `connState`:

```go
type connState struct {
    conn    *Conn
    input   chan protocol.RoutingEnvelope
    gateRun bool
    closed  bool // set true after the per-conn goroutine exits on close-intent; demux drops further frames
}
```

The `gateRun` field is read/written only on the per-conn goroutine (single-writer), so it does not need the mutex. `closed` is written by the per-conn goroutine and read by the demux — accesses go through `d.mu` (the same lock that already protects `d.conns`). The lookup in the demux extends:

```go
d.mu.Lock()
st := d.conns[env.ConnID]
if st == nil {
    // existing getOrCreateConn path, atomic insert + go runConn
}
isClosed := st != nil && st.closed
d.mu.Unlock()
if isClosed {
    d.cfg.Logger.Debug("dispatch: drop frame for closed conn",
        "conn_id", env.ConnID, "len", len(env.Frame))
    continue
}
```

Per-conn goroutine sets `closed = true` under `d.mu` just before returning (after the close-intent envelope is queued onto `outbound`).

### Reject sequence (worked example)

1. Phone connects, fakerelay assigns `conn_id="c-1"`, captures `token="bogus"` from headers.
2. Phone sends `{"type":"hello","id":1,...}`. Fakerelay wraps as `RoutingEnvelope{ConnID:"c-1", Frame:<hello>, Token:"bogus"}` and forwards to binary.
3. Binary's `relay.Connection.Frames()` yields this envelope. Dispatcher demux routes to `runConn` for `c-1` (new goroutine).
4. `runConn` first iteration: `gateRun=false`. Invokes `d.cfg.FirstFrame(ctx, env)`.
5. Gate closure (built in cmd/pyry): extracts `env.Token`, calls `relay.AuthenticateFirstFrame(env, "bogus", registry, serverID, logger)`. Registry doesn't contain a device with `HashToken("bogus")` — returns `AuthOutcome{Response: <error envelope id=1, in_reply_to=1, code=auth.invalid_token>, CloseConn: true}`. Gate maps to `FirstFrameOutcome{Response: outcome.Response, CloseConn: true, Code: 4401}`.
6. `runConn` sees `outcome.CloseConn`. Sets `outcome.Response.CloseCode = 4401`. Pushes onto `d.outbound`.
7. cmd/pyry forwarder reads outbound, calls `conn.Send(env)` — marshals (Frame + CloseCode + ConnID) and writes to transport.
8. Fakerelay's `binaryRecvPump` decodes, sees `env.ConnID="c-1"` and `env.CloseCode=4401`. Forwards `env.Frame` to phone WS, then `_ = phoneConn.conn.Close(websocket.StatusCode(4401), "")`.
9. Phone's `Receive` returns the error envelope (decoded, code=auth.invalid_token); subsequent `Receive` returns an error whose `websocket.CloseStatus(err)` is 4401.
10. Binary's `runConn` marks `closed=true` under `d.mu` and exits. WaitGroup decrements.

### cmd/pyry/relay.go — registry plumb and gate closure

Insert two changes inside `startRelay`, between `relay.Connect` (line 79) and `dispatch.New` (line 90):

1. Load the device registry from `~/.pyry/<sanitized-instance>/devices.json`. Use the existing helper `resolveDevicesPath(instanceName)` (already in `cmd/pyry/pair.go`). Failure (registry file unreadable / malformed JSON) is a daemon-startup error — return wrapped `fmt.Errorf("load device registry: %w", err)`. A missing file is NOT an error (registry.Load returns an empty `*Registry` on `ENOENT`); the binary just rejects every phone until `pyry pair` runs.
2. Build the gate closure that bridges `dispatch.FirstFrameGate` and `relay.AuthenticateFirstFrame`:

```go
gate := func(ctx context.Context, env protocol.RoutingEnvelope) dispatch.FirstFrameOutcome {
    outcome, err := relay.AuthenticateFirstFrame(env, env.Token, registry, string(serverID), logger)
    if err != nil {
        // Today only ErrMalformedHelloFrame is reachable. Surface to
        // the dispatcher's malformed-frame fall-through.
        return dispatch.FirstFrameOutcome{Err: err}
    }
    out := dispatch.FirstFrameOutcome{Response: outcome.Response}
    if outcome.CloseConn {
        out.CloseConn = true
        out.Code = uint16(relay.StatusUnauthorized) // 4401
    }
    return out
}
```

3. Pass the gate to `dispatch.New(dispatch.Config{Frames: conn.Frames(), Logger: logger, FirstFrame: gate})`.

No other changes to `cmd/pyry/relay.go`. The 3-goroutine wiring (dispatcher / outbound forwarder / wait classifier) is intact.

### fakerelay changes

Two additions to `internal/e2e/internal/fakerelay/fakerelay.go`:

1. **Token injection** on the first phone→binary frame per `conn_id`. Add a `firstFrameSent bool` field to `phoneConn`. In `phoneRecvPump`, before constructing the routing envelope, capture the per-conn token (stored from headers at upgrade) on the first frame; subsequent frames leave `Token` empty:

   ```go
   token := ""
   pc.mu.Lock()
   if !pc.firstFrameSent {
       token = pc.token // captured from r.Header at upgrade time
       pc.firstFrameSent = true
   }
   pc.mu.Unlock()
   out, err := json.Marshal(protocol.RoutingEnvelope{
       ConnID: pc.connID,
       Frame:  json.RawMessage(data),
       Token:  token,
   })
   ```

   `phoneConn.token` is a new field; `phoneConn.mu` is a new sync.Mutex (the existing struct has no mutex because each phone has serial pumps; introducing one is the simplest way to write the field under the same lock the demux already holds for the outer `s.phones` map — or use sync.Once; pick whichever is cleaner). The doc-comment for `phoneConn` should note that `firstFrameSent` and `token` are wire-protocol artifacts not routing state.

2. **CloseCode honor** on binary→phone frames. In `binaryRecvPump`, after the existing decode of `RoutingEnvelope` (line 376), check `env.CloseCode`:

   ```go
   if env.CloseCode != 0 {
       // Forward Frame (if any) first, then close.
       if len(env.Frame) > 0 {
           select {
           case ph.sendCh <- env.Frame:
           case <-ctx.Done(): return ctx.Err()
           case <-ph.done: // phone gone; nothing to do
           }
       }
       _ = ph.conn.Close(websocket.StatusCode(env.CloseCode), "")
       continue
   }
   ```

   Place this branch BEFORE the existing "forward Frame to phone.sendCh" block. The close happens after the frame is queued onto `sendCh` so the phone's recv pump observes the error envelope before the WS read fails.

   Note the race: `ph.sendCh` is unbuffered (existing design). The select above queues the frame; the phone's send pump writes it to the WS; only THEN the close is issued. To guarantee the frame is flushed before close, either (a) wait for the phone's send pump to drain (no current synchronisation primitive — would need a per-frame ack), or (b) accept the small window where the close could race the write. The wire spec doesn't pin ordering at this granularity — the AC requires the phone observes BOTH the error envelope AND the 4401 close, not strict ordering. The race is acceptable; the test should tolerate either order in the phone's Read stream (i.e. assert both observations happen, not strict sequencing).

   Actually — the simpler approach is to do the close synchronously AFTER pushing to sendCh but only after the send pump has processed it. We can do this by directly invoking `pc.conn.Write` here under a lock instead of going through sendCh, then close. Defer this implementation detail to the developer; the spec just requires "phone observes error envelope, then 4401 close, in either order".

### fakephone changes

`fakephone.Client` needs to expose the received close status so the e2e can assert `4401`. Add a method:

```go
// LastCloseStatus returns the WS close status received from the relay
// when the conn was closed by the peer, and ok=true. ok=false if the
// conn was closed locally via Close() or has not yet observed a peer
// close. Set by Receive when Read returns a CloseError.
func (c *Client) LastCloseStatus() (websocket.StatusCode, bool)
```

Implementation: in `Receive`, when `Read` returns an error, extract `code := websocket.CloseStatus(err)`; if `code != -1`, store under mutex (`c.lastCloseStatus = code; c.lastCloseStatusSet = true`). `LastCloseStatus` reads under mutex.

`Receive` continues to return its existing error shape (no wire change to the public sentinels). The close-status capture is a side effect.

### e2e test

New file `internal/e2e/relay_auth_test.go` (or extend `relay_test.go`; new file is cleaner). Skeleton:

- Setup mirrors `TestRelay_Hello` (`fakerelay.New`, `shortHome`, `StartInWithEnv` with `PYRY_ALLOW_INSECURE_RELAY=1` + `-pyry-relay=...`).
- Wait for binary's hello/hello_ack handshake to complete (poll `fr.LastBinaryHello(serverID)`) so the binary↔relay leg is up before the phone connects.
- Dial fakephone against `fr.URL()` with a token NOT present in any registry on disk (e.g. `"unpaired-token-deadbeef"`). The test never runs `pyry pair` so the registry is empty; any token rejects.
- Phone sends a well-formed `hello` envelope (`HelloClientPayload{Role: "client", DeviceName: "test-phone", ...}`).
- Phone calls `Receive(2*time.Second)`. Assertions on first received envelope:
  - `env.Type == protocol.TypeError`
  - Decode `env.Payload` as `protocol.ErrorPayload`; `payload.Code == protocol.CodeAuthInvalidToken`
  - `env.InReplyTo != nil && *env.InReplyTo == 1`
- Phone calls `Receive` again (with a short deadline). Expects an error (the WS is now closed). Assert `phone.LastCloseStatus() == 4401`.
- Cleanup: `phone.Close()`, `h.Stop(t)`, `fr.Close()`.

Per `docs/PROJECT-MEMORY.md`'s `time.Time` round-trip discipline, the test does NOT compare envelope timestamps with `==`; it doesn't need to compare TS at all here.

### Wire-spec doc update

`docs/protocol-mobile.md` § Routing envelope (lines 100-122). After the existing two example JSON blocks, append two short paragraphs:

> **Relay-prepended fields on the binary↔relay leg only.** Two optional fields extend the routing envelope:
>
> - `token` *(string, phone→binary direction, first frame per `conn_id` only)*: the phone-supplied device-pairing token from the `x-pyrycode-token` HTTP header at WS upgrade. The relay sets this field exactly once per phone WS (on the first frame it forwards from the phone) and leaves it empty on every subsequent frame. The binary uses it to validate the phone via the local device registry; on mismatch, the binary replies with `error` (code `auth.invalid_token`) and asks the relay to close the phone WS with code `4401` (see Error codes).
> - `close_code` *(uint16, binary→relay direction)*: when non-zero, asks the relay to forward `frame` (if non-empty) to the phone and then close that phone's WS with this WS close code. Used today for the auth-reject path (4401); reserved for future binary-side close intents.
>
> Both fields are absent (JSON omitempty) on routing envelopes that don't use them. Implementations MUST tolerate unknown fields on the routing envelope to allow forward compatibility.

## Error handling

| Failure | Surface |
|---|---|
| Gate returns `FirstFrameOutcome{Err: ErrMalformedHelloFrame}` (inner frame is not JSON) | Dispatcher falls through to existing `protocol.malformed` reply (no `in_reply_to`); `gateRun` IS still set to true so the conn isn't re-gated forever — but the conn stays open. Subsequent frames go through normal handler dispatch. The phone is unlikely to recover, but that's its problem. |
| `Registry.Load` failure at daemon startup (malformed JSON) | `startRelay` returns wrapped error; `runSupervisor` fails fast. Same posture as `identity.LoadOrCreate` failure (line 70 today). |
| `Registry.Load` ENOENT | Empty `*Registry`. Every phone reject path. No log noise. |
| Phone disconnects mid-gate (relay closes Frames mid-handler) | The gate runs synchronously on the per-conn goroutine; if it returns, the dispatcher publishes the outcome. If `outbound` is full, the per-conn goroutine blocks until either the forwarder drains or ctx is cancelled. Standard backpressure. |
| `Conn.Send` ctx-cancel during reject emission | The error envelope is dropped; the close intent is never published; the relay leaves the phone WS open. On daemon shutdown, the relay's connection is torn down and the phone observes a clean close (1000). Acceptable for shutdown. |
| Phone sends a non-hello envelope as its first frame | `AuthenticateFirstFrame` decodes `env.Frame` to extract `helloID` regardless of type — `inner.ID` is the only field it reads. Then `reg.Validate(env.Token)` runs unconditionally; an empty Token rejects via `Validate("")→false`. So non-hello first frames produce an `auth.invalid_token` reject + 4401 close, same as bad-token. This is intentional: there's no protocol-valid first frame on a fresh conn except hello. |
| `Connection.Send` returns `transport.ErrDisconnected` during reject forwarding | Forwarder logs at Debug (existing behavior, line 112); the close intent is lost on the wire. Acceptable: the transport reconnect will trigger a fresh hello/hello_ack, the relay's conn state is gone, and the phone's WS is in an indeterminate state — but the security property holds because the binary never marked the conn authenticated. |

## Testing strategy

### `internal/dispatch/dispatch_test.go` (table-driven, stdlib only)

- **`TestFirstFrameGate_Accept`**: register a stub gate that returns `FirstFrameOutcome{Response: <hello_ack envelope>, CloseConn: false}` for the first frame and a tracked counter for subsequent calls. Send frame 1; assert outbound is the hello_ack (decode, check type+InReplyTo+ID=1). Send frame 2 (a `TypeSendMessage`); assert it does NOT go through the gate (counter stays at 1) and instead hits the empty-table fall-through (`protocol.unsupported`). This pins the "gate runs once per conn" contract.
- **`TestFirstFrameGate_Reject`**: gate returns `FirstFrameOutcome{Response: <error envelope>, CloseConn: true, Code: 4401}`. Send frame 1. Assert outbound is one envelope with `Frame=<error>` and `CloseCode == 4401`. Send frame 2 for the same `conn_id`; assert NO additional outbound (the conn is closed; demux drops). The test uses a small `time.After` (~50ms) to bound the "no additional outbound" wait.
- **`TestFirstFrameGate_RejectDoesNotAffectOtherConns`**: send a frame for `conn-a` that triggers reject. Then send a frame for `conn-b` that triggers accept. Assert both outcomes are observable on outbound (decode by ConnID). This pins the per-conn isolation property — one reject does not stall the dispatcher.
- **`TestFirstFrameGate_Err`**: gate returns `FirstFrameOutcome{Err: errors.New("test malformed")}`. Send frame 1. Assert outbound is one `error` envelope with code `protocol.malformed` and `InReplyTo == nil`. Subsequent frame 2 goes through normal handler dispatch (verify with a `Register`'d echo handler that publishes a known envelope on Send).
- **`TestFirstFrameGate_Nil`**: `Config.FirstFrame == nil`. Send any frame; assert it goes straight to handler-table dispatch (gates the #307 backwards-compat property).
- **`TestFirstFrameGate_ConcurrentConns`**: ten goroutines each send a first frame for distinct `conn_id`s; gate returns a unique Response per call (keyed on `env.ConnID`). Drain outbound, assert all ten responses arrive with their ConnIDs intact. Runs under `-race`.

### `internal/relay/connection_test.go`

- **`TestCloseConn_SendsCloseCodeEnvelope`**: stand up a small in-test transport (mirroring existing `connectWithClient` test seam); call `conn.CloseConn("c-7", 4401)`; assert the test transport observed one outbound frame whose JSON has `conn_id=="c-7"` and `close_code==4401` and `frame` is absent.
- **`TestCloseConn_PropagatesDisconnected`**: arrange the underlying transport client to be in disconnected state; assert `CloseConn` returns an error matching `transport.ErrDisconnected` (or whatever sentinel `client.Send` returns when not connected — verify via the same path that `Connection.Send` already uses).

### `internal/protocol/envelope_test.go` (or wherever the existing envelope round-trip tests live)

- **`TestRoutingEnvelope_OmitemptyToken`**: marshal `RoutingEnvelope{ConnID: "c-1", Frame: rawHello}`; assert the JSON does NOT contain `"token"`. Marshal with `Token: "abc"`; assert it does. Round-trip.
- **`TestRoutingEnvelope_OmitemptyCloseCode`**: same, for CloseCode (0 omits; non-zero serialises).

### e2e — `internal/e2e/relay_auth_test.go`

Single test `TestRelay_AuthReject_4401` (build tag `e2e`):

- Spin up `fakerelay` and `pyry` binary (via `StartInWithEnv`), wait for hello handshake.
- Dial a `fakephone` with token `"unpaired-token"` (no devices.json on disk → empty registry → reject).
- Phone sends a well-formed `hello` envelope (`HelloClientPayload`).
- Receive #1: assert `Type == TypeError`, payload `code == auth.invalid_token`, `in_reply_to == 1`.
- Receive #2: assert returns error (WS closed). Assert `phone.LastCloseStatus() == 4401`.
- Cleanup.

### Race coverage

All unit tests run under `go test -race ./...` as part of the AC. The dispatcher's `closed` flag access through `d.mu` and `gateRun` single-writer model are designed for race-free reads.

## Open questions

- **`json.RawMessage` zero-value and `omitempty`.** A `RoutingEnvelope{ConnID: "c-1", CloseCode: 4401}` with default `Frame` (`json.RawMessage(nil)`) marshals as `"frame":null` (NOT omitted), because `json.RawMessage` is `[]byte` and `omitempty` on a slice strips only the zero-length case, not nil-vs-non-nil. Test this empirically; if `null` appears on the wire, either (a) accept it (the relay's `binaryRecvPump` JSON-decodes into `RoutingEnvelope.Frame` and `len(env.Frame) > 0` already gates whether to forward), or (b) custom-marshal `RoutingEnvelope` to omit `frame` when `len == 0`. Option (a) is the smaller change and is preferred unless tests show it confuses an existing decoder.

- **Multi-route gate decomposition.** This slice hard-codes one gate per dispatcher. A future slice might want per-`Type` gates (e.g. a backfill rate-limit gate runs only on `TypeBackfillSince`). Generalising the hook to "list of middlewares" is out of scope; the auth gate is the only first-frame interceptor v1 needs.

- **`AuthenticateFirstFrame` rejection logging.** Today the predicate logs at Warn on every reject. A flood of bad tokens (an attacker probing) is a moderate log-pressure concern. Logging is the predicate's responsibility (#249), not this slice's; defer rate-limiting / sampling to a future hardening pass.

- **Token in routing envelope vs. hello payload.** This spec picks option (a) (`RoutingEnvelope.Token`) from #249's three deferred options. The alternative options (synthesized `connection_opened` control frame; amended hello payload) are documented in `internal/relay/auth.go:60-62` and remain available for a future protocol revision. The choice does not propagate into `AuthenticateFirstFrame`'s signature, which is carrier-agnostic by design.

- **Re-gating after Err.** Spec sets `gateRun=true` even on `Err` (so a malformed first frame doesn't loop the gate). An alternative is to NOT set `gateRun` and let the phone retry. Picked the strict path because (a) a phone whose first frame is malformed is buggy regardless, and (b) the conn isn't authenticated so the gate-skip just means the conn now goes through the regular handler table with an empty Token — which will reject every subsequent frame via the handler-table's existing unsupported/unknown-type fall-through. Net effect: the phone gets `protocol.malformed` then a series of `protocol.unsupported` on retry. Document the choice in the dispatcher doc-comment.

## Security review

This ticket has the `security-sensitive` label. Performing the adversarial pass before commit. See `agents/architect/security-review.md`.

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The auth gate is itself the trust boundary between unauthenticated phone bytes and the binary's handler table. The boundary is enforced at `runConn`'s first-frame branch: until the gate returns Accept, the inbound `protocol.Envelope` does NOT reach any registered handler. The empty-table state in #307 makes this trivially true today (no handlers registered); the post-#308 invariant is "no `handlers[env.Type]` lookup occurs for an unauthenticated conn." Pinned by `TestFirstFrameGate_Reject`'s second-frame assertion (no outbound from frame 2 because the conn is closed) and by the design: `runConn` exits before frame 2 is read on reject. **No finding.**

- **[Tokens, secrets, credentials]** Token plumbing introduces a NEW wire-traversal of the device token (binary↔relay leg). The token is already plaintext on the phone→relay HTTP header at upgrade; this slice extends the plaintext path one hop further (relay→binary, in `RoutingEnvelope.Token` for the first frame only). Both legs are TLS-terminated in production (wss://). The `Token` field is `omitempty` and stripped on subsequent frames, so the token does NOT remain in long-lived per-conn state on the wire.

  `AuthenticateFirstFrame` already documents (line 73-76) that the token is never logged, never wrapped into errors, never echoed to the phone. The dispatcher MUST preserve that posture: gate's wrapping closure in `cmd/pyry/relay.go` MUST NOT log `env.Token`; the dispatcher's existing log-policy convention (`conn_id`, `type`, `id` only; never payload bytes) extends to `Token` by the same rule. Add a doc-comment on `RoutingEnvelope.Token` reiterating this. **SHOULD FIX (note): dispatcher log policy explicitly excludes `Token`.**

  The pairing registry is loaded once at daemon startup and held in-memory; `Registry.Validate` runs under `Registry.mu` and the matched device's `LastSeenAt` is bumped in-memory only (no fsync on the auth hot path — by design, per `devices/auth.go:14-16`). A device revoked via `pyry pair revoke` after the daemon started will continue to authenticate until the daemon restarts OR until a registry-reload primitive lands. **Out-of-scope; documented in `internal/devices/auth.go`.**

- **[File operations]** `Registry.Load` is the only filesystem touch. Path is `~/.pyry/<sanitized-name>/devices.json` via `resolveDevicesPath` (existing). The sanitization defense (`sanitizeName`) is already in place against `PYRY_NAME=../../etc` and similar — see `cmd/pyry/pair.go:30-36`. The instance name flows from the `-pyry-name` flag through `runSupervisor` to `startRelay`; the same sanitization helper is reused. **No finding.**

- **[Subprocess / external command]** None. **N/A.**

- **[Cryptographic primitives]** The token hash compare lives in `devices.HashToken` + `Registry.Validate` (existing). The hash↔hash compare inside `Validate` is byte-exact (`r.devices[i].TokenHash == hash`); constant-time comparison is NOT used at that boundary because both sides are deterministically-derived SHA-256 hex strings of equal length. The plain↔hash compare boundary owns constant-time comparison and is in `VerifyToken` (also existing). This slice introduces no new comparison of attacker-controlled values to secrets. **No finding.**

- **[Network & I/O]** The new `CloseCode` wire field is binary→relay only and is acted on by the relay (production) / `fakerelay` (e2e). A malicious relay COULD send a `CloseCode` field on a phone→binary frame; the dispatcher MUST ignore it. Today the dispatcher never reads `env.CloseCode` on inbound frames — it just passes the inner Frame through. **SHOULD FIX (note): explicitly state in the dispatcher doc-comment that `CloseCode` is a binary→relay-only signal and is ignored on inbound; add a unit-test pinning the ignore behavior** (send a phone→binary frame with `CloseCode=4401` in the wrapper; assert it has no effect on dispatcher behavior — the gate runs as usual on the inner Frame).

  Frame-size enforcement: the new `CloseCode` and `Token` fields are bounded (`uint16` and a small hex string respectively); they don't change the frame-size threat model already enforced in `internal/transport/wssclient`'s WS read path.

  Head-of-line blocking: the gate runs on the per-conn goroutine, NOT the demux. A slow gate stalls one conn, not all. Unchanged from #307's posture.

- **[Error messages, logs, telemetry]** Two log points:
  - The dispatcher's per-conn-close debug log includes `conn_id` and the inbound frame's `type`/`id` if available, but NOT payload bytes. (Existing convention from #307 § Security review.)
  - The gate closure in cmd/pyry MUST NOT log `env.Token`. This is the load-bearing addition. **SHOULD FIX (note above).**

  Wire-side error envelopes carry only `code` + the static `MsgInvalidToken` message (no internal detail). `AuthenticateFirstFrame` already enforces this (lines 107-110). The dispatcher doesn't modify the response envelope — it just forwards with `CloseCode` set.

- **[Concurrency]** The `connState.closed` flag is the new shared-state surface. Read by the demux, written by the per-conn goroutine. Both accesses are under `d.mu` per the spec. The `gateRun` flag is single-writer (per-conn goroutine only); no race. The dispatcher's existing `started` atomic and `d.mu` invariants from #307 are preserved.

  Wider race: between "demux writes to st.input" and "per-conn goroutine sets st.closed and exits". If the demux has st.input <- env in flight when the goroutine returns, the channel send blocks (no reader). Mitigation: the per-conn goroutine SHOULD drain st.input one final time before returning OR the demux SHOULD select-with-ctx on the input write so it doesn't hang. The cleanest fix is to make st.input buffered enough that the post-close drop path's check (under d.mu) reliably catches the closed flag before the demux commits the write. Concretely: the demux's `select { case st.input <- env: ... }` should be guarded by reading `st.closed` under `d.mu` immediately before the send (as shown in the Concurrency section above). **MUST FIX: pin the "check closed under mutex, then send" sequence so a send into a dead per-conn goroutine cannot block the demux.** The Concurrency section already specifies this; flagging it explicitly here so the developer doesn't drop the check.

- **[Threat model alignment]** The auth-gate is the load-bearing primitive for the protocol-mobile.md threat model item #2 (relay compromise → token forgery). A compromised relay CAN inject any `Token` on the binary↔relay leg, but it CANNOT produce a `Token` that matches a hash in the binary's local `~/.pyry/<name>/devices.json`. The defense works because tokens are 256-bit random (`crypto/rand` in `pyry pair`) and only their SHA-256 hashes are persisted. A compromised relay sees the token in transit but cannot brute-force a new one. **No finding** beyond what's already documented in the protocol's threat model.

  Replay: a compromised relay COULD replay a captured token from a legitimately-paired phone. v1 has no nonce/timestamp defense in `AuthenticateFirstFrame` (it just validates the token). The mitigation is the device-revoke primitive (`pyry pair revoke`), which becomes effective on the next binary restart per the registry-reload caveat above. **Documented in `docs/protocol-mobile.md` § Threats and `internal/devices/auth.go`; out-of-scope for this slice.**

**MUST FIX summary:**

1. Pin the "check `connState.closed` under `d.mu` immediately before sending to `st.input`" sequence in the dispatcher demux. Without this, a frame arriving for a closed conn can deadlock the demux.

**SHOULD FIX summary (notes for developer + code-reviewer):**

1. Dispatcher doc-comment explicitly states `Token` is never logged at any level by the dispatcher or the gate closure.
2. Dispatcher doc-comment explicitly states `RoutingEnvelope.CloseCode` is a binary→relay-only signal and is ignored on inbound (phone→binary) frames; unit test pins the ignore behavior.

No SHOULD FIX item gates the spec. The MUST FIX item is already specified in the Concurrency section — flagging it here so the developer threads it into implementation review.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-13
