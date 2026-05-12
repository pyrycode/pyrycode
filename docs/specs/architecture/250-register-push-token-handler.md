# Spec: register_push_token handler (#250)

## Files to read first

- `internal/relay/auth.go:77-148` — `AuthenticateFirstFrame` is the existing pattern this handler mirrors: routing envelope in → routing envelope out, build inner Envelope + wrap with json.Marshal, log accept/reject with structured fields, never log secrets. The handler's `buildResponse` helper is local to auth.go (lowercase) and intentionally not shared yet; the new handler duplicates the wrap pattern rather than importing it across the new sub-package boundary.
- `internal/devices/auth.go:32-46` — `Registry.Validate(plain) (Device, bool)` semantics: the snapshot returned to the caller is a value copy (not a pointer into the registry slice). This is why the new handler takes `*devices.Device` (a snapshot the relay-conn layer cached at first-frame auth) AND needs a new registry mutator to durably update the stored row.
- `internal/devices/registry.go:111-128, 145-154` — `Add` / `Remove` / `FindByTokenHash` are the existing exported mutators. `UpdatePushRegistration` (this ticket adds) lives alongside, identifies the target row by TokenHash (the only stable key the handler already has), and follows the same `r.mu.Lock` / range-and-mutate pattern.
- `internal/devices/registry.go:63-107` — `Save` is the atomic write primitive (temp file → fsync → rename). The handler calls it unmodified; the only new failure surface is "Save returned non-nil", mapped to `server.binary_busy`.
- `internal/devices/device.go:24-43` — `Device` fields, including the `omitempty`-tagged `Platform` and `PushToken` introduced by #282. The dedupe comparison is over `(Platform, PushToken, Name)` exactly — empty-string equals empty-string is the no-write path on a phone that has never registered before but resends an empty payload (theoretical; not a tested case).
- `internal/protocol/push.go:14-18` — `RegisterPushTokenPayload` (Platform, Token, DeviceName) is the wire shape. All three fields required per spec; the handler does not validate them (no protocol.malformed synthesis here — the dispatcher owns that path).
- `internal/protocol/envelope.go:23-43` — `Envelope` and `RoutingEnvelope` outer shapes. The handler unmarshals `routing.Frame` into `Envelope`, then unmarshals `env.Payload` into `RegisterPushTokenPayload`.
- `internal/protocol/codes.go:14-15, 19, 37-41, 61` — `CodeAuthInvalidToken` (unauth-conn case), `CodeServerBinaryBusy` (write-failure case), `TypeAck` / `TypeError` / `TypeRegisterPushToken` wire constants.
- `internal/protocol/handshake.go:39-48` — `ErrorPayload` (Code, Message, Retryable, RetryAfterS) and `AckPayload` (empty) wire shapes the handler emits.
- `internal/relay/auth_test.go:23-64, 182-221` — test patterns for fixture-paired registry and ack/error response assertions; reused conceptually (not literally — the new test file is in `internal/relay/handlers/` and cannot import from `internal/relay`).
- `docs/protocol-mobile.md` § `register_push_token` (lines 480–499) — wire shape, dedupe contract sentence ("if the registered triple is identical to the stored one, no-op"), and the `ack` response choice.
- `docs/protocol-mobile.md` § Error codes (lines 525–542) — `server.binary_busy` row (retryable: yes; honour retry_after_s) and `auth.invalid_token` row (no retry).
- `docs/specs/architecture/249-relay-inbound-token-validation.md` — the ticket body's "from `OnNewPhoneConnection` result" wording is stale; #249 actually shipped `AuthenticateFirstFrame` returning a `Device` value. This spec follows what #249 actually shipped, per the ticket's own carve-out ("If #249 lands a different shape than the AC's wording assumes, follow what #249 actually shipped.").

## Context

Phase 3 Track C — the binary's inbound handler for the phone's `register_push_token` frame. Composes A4 (devices.json registry, #209), A5 (`Validate`, #210), C1 (envelope types, #275), C4 (#249 — first-frame auth returning a matched `Device`), and #282 (Device gains `Platform` and `PushToken`). Per `docs/protocol-mobile.md` § Phone background behaviour (line 144), `register_push_token` is load-bearing for the mobile UX: a backgrounded phone closes its WS and is woken via APNs/FCM using the persisted token.

The phone re-registers on every WS connect (~100 bytes, self-heals registry drift). The binary MUST de-duplicate: identical `(platform, token, device_name)` triple → no disk write, ack only. Without dedupe, every WS connect rewrites `devices.json`, amplifying flash wear and i/o churn for what's typically a no-op.

The handler is dispatcher-agnostic. The future relay-conn ticket (not yet open) owns:
1. Per-conn `Device` caching after `AuthenticateFirstFrame` accepts.
2. Per-conn envelope-id counter starting at 2 (auth's hello_ack is 1).
3. Type-dispatch from inbound `RoutingEnvelope` → `Handle` for `register_push_token`.

This ticket only defines the handler's pure function: routing envelope in, routing envelope out, with one new registry mutator to back it.

## Design

### Package

Add `internal/relay/handlers/` as a new sub-package, sibling to `internal/relay/auth.go` and `internal/relay/connection.go`. Rationale:

- The ticket body explicitly names this path; it is the seam where forward type-dispatched handlers live (`send_message`, `list_conversations`, etc. will join it).
- `handlers` depends on `internal/devices` and `internal/protocol` only — no import of `internal/relay`, which keeps the dispatcher (future, in `internal/relay`) free to import `handlers` without cycles.
- `auth.go` stays in `internal/relay` because it is the *gate* into the dispatcher, not a per-type handler; the dispatcher will reach for it directly during conn setup before any frame dispatch.

New files:
- `internal/relay/handlers/register_push_token.go` — `Handle` + module-doc comment.
- `internal/relay/handlers/register_push_token_test.go` — five test cases per AC.

Modified files:
- `internal/devices/registry.go` — adds one new exported method, `UpdatePushRegistration`.
- `internal/devices/registry_test.go` — adds one new test for `UpdatePushRegistration` (hit, miss, idempotent overwrite).

### Registry mutator (new)

```go
// UpdatePushRegistration sets Platform, PushToken, and Name on the device
// whose TokenHash equals tokenHash. Returns true iff a matching device was
// found and mutated. Caller is responsible for persisting via Save.
//
// The Name overwrite is intentional: the protocol's
// register_push_token.device_name is the phone's current self-reported
// name, and pyrycode treats the phone as the source of truth (a user
// renaming their device in iOS Settings should propagate). The original
// pairing-time Name carries no protocol-level invariants.
//
// Concurrency: serialized under Registry.mu; safe to call from any
// goroutine.
func (r *Registry) UpdatePushRegistration(tokenHash, platform, pushToken, name string) bool
```

Implementation: lock `r.mu`, linear scan for matching `TokenHash`, mutate the three fields in place, unlock, return true. No match → return false. No logging (the package never logs; that's the caller's job).

`Name` is mutated alongside `Platform` and `PushToken` — see the doc-comment rationale. The handler's dedupe check therefore covers all three fields together (and the test for "re-register with different `device_name` only" is implicit in the "changed triple" test).

### Handler signature

```go
// Package handlers implements per-envelope-type processors for the
// binary's inbound phone-traffic dispatch. Each handler is a pure
// function: routing envelope in, routing envelope out (plus side effects
// on the devices/conversations registries it is passed). Handlers know
// payload semantics; the dispatcher (future internal/relay) owns conn
// state, per-conn id allocation, and conn lifecycle.
package handlers

// Handle processes a register_push_token frame from the phone and
// returns the routing envelope to send back through the binary→relay
// leg. The frame's inner Envelope.Type is assumed to be
// TypeRegisterPushToken (the dispatcher already type-dispatched);
// Handle does not re-verify.
//
// `routing` is the inbound RoutingEnvelope wrapping the phone's frame.
// Handle decodes routing.Frame into a protocol.Envelope (for the
// in_reply_to echo) and decodes its payload into a
// RegisterPushTokenPayload.
//
// `device` is the authenticated phone's Device entry — the snapshot the
// relay-conn layer cached at first-frame auth (the value returned by
// AuthenticateFirstFrame). A nil pointer means the dispatcher routed an
// unauthenticated conn into this handler; Handle responds with an
// auth.invalid_token error envelope and does NOT touch the registry.
//
// `reg` and `registryPath` are passed through to UpdatePushRegistration
// and Save when the dedupe check fails (i.e. the triple is different).
// The handler never touches registryPath on the dedupe path.
//
// `nextID` is the envelope id the dispatcher allocated for this response
// from its per-conn counter (auth's hello_ack used id 1; this is id ≥ 2).
// The handler stamps it onto every envelope it builds (ack, error).
//
// SECURITY:
//   - The push token is opaque infrastructure data (FCM/APNs registration
//     id); not a secret on par with the device token. It is logged at
//     INFO level for traceability when a write happens, and at DEBUG when
//     dedupe skips the write. The phone-side device token (used at auth)
//     is NEVER read or logged by this handler.
//   - The Device snapshot's name IS logged on every path (write, dedupe,
//     unauth-reject). Unauth-reject is safe to name-log here because the
//     "no device" path is the only one without a name — there is nothing
//     to enumerate.
//
// Concurrency: the handler is stateless. reg's mutex serializes
// UpdatePushRegistration and Save (independently); two concurrent calls
// for the same TokenHash interleave at the mutex boundary, and the
// "last writer wins" memory state is whichever lock acquisition was
// later. Disk-level last-writer-wins is enforced by Save's atomic rename.
func Handle(
    routing protocol.RoutingEnvelope,
    device  *devices.Device,
    reg     *devices.Registry,
    registryPath string,
    nextID  uint64,
    logger  *slog.Logger,
) (protocol.RoutingEnvelope, error)
```

The `error` return is reserved for genuine programmer errors (json.Marshal on a struct cannot fail in practice — but we propagate rather than panic, matching auth.go's pattern). All protocol-level outcomes (success, dedupe-success, write-failure, unauth) are conveyed via the returned `RoutingEnvelope` with `nil` error. The dispatcher does not need to dispatch on the error return for these AC cases.

### Errors / sentinels

One new sentinel:

```go
// ErrMalformedFrame is returned by Handle when routing.Frame cannot be
// JSON-decoded as a protocol.Envelope, or when the inner payload cannot
// be decoded as RegisterPushTokenPayload. The dispatcher maps this to
// its existing protocol.malformed handling (a sibling ticket owns the
// response shape); this handler does not synthesize an error envelope
// for it.
var ErrMalformedFrame = errors.New("handlers: malformed register_push_token frame")
```

Rationale matches `relay.ErrMalformedHelloFrame`: structural JSON errors are dispatcher concerns, not per-handler concerns. The five AC test cases never exercise this path; one happy-grouping test pins the sentinel on a `[]byte("not-json")` `routing.Frame`.

### Behavioural contract

Given inputs `(routing, device, reg, registryPath, nextID, logger)`:

1. Decode `routing.Frame` → `protocol.Envelope`. On failure → return `(RoutingEnvelope{}, ErrMalformedFrame)`.
2. Let `requestID := envelope.ID` (for `in_reply_to`).
3. **Unauth check.** If `device == nil`:
   - Build error envelope with payload `ErrorPayload{Code: CodeAuthInvalidToken, Message: MsgUnauthorized, Retryable: false}`, wrap in routing envelope, echo `routing.ConnID`, return `(resp, nil)`.
   - Log WARN: `event=register_push_token.unauth`, `conn_id=routing.ConnID`. No further state changes.
   - `MsgUnauthorized` is a new file-local constant: `"not authenticated; handshake required before register_push_token"`. Defined as a string literal in `register_push_token.go`; not exported. The phone should never reach this path in practice — auth.invalid_token is closed with WS code 4401 by the time #249's chain runs — but the handler emits a coherent envelope for the (dispatcher-bug) case.
4. Decode `envelope.Payload` → `RegisterPushTokenPayload`. On failure → return `(RoutingEnvelope{}, ErrMalformedFrame)`.
5. **Dedupe check.** Compare `(payload.Platform, payload.Token, payload.DeviceName)` against `(device.Platform, device.PushToken, device.Name)` (byte-exact equality, three fields, in that order — order matters only for the short-circuit; the result is logically a 3-way `&&`).
   - **All three equal:**
     - Build ack envelope with payload `AckPayload{}`, wrap, echo `routing.ConnID`, return `(resp, nil)`.
     - Log DEBUG: `event=register_push_token.dedupe`, `conn_id=routing.ConnID`, `device_name=device.Name`. Crucially, do NOT call `UpdatePushRegistration` and do NOT call `Save`. The AC's "no write occurred" assertion turns on this branch.
   - **Any differ:** continue to step 6.
6. Call `reg.UpdatePushRegistration(device.TokenHash, payload.Platform, payload.Token, payload.DeviceName)`.
   - If returns `false` (device matched at auth but is gone now — concurrent revoke): build error envelope `CodeAuthInvalidToken` (same UX as unauth), log WARN `event=register_push_token.gone_mid_conn`, return `(resp, nil)`. This case is NOT in the AC's required test set; it is a defensive branch and is covered implicitly by the registry-mutator's own unit test.
7. Call `reg.Save(registryPath)`.
   - If returns non-nil: build error envelope `ErrorPayload{Code: CodeServerBinaryBusy, Message: MsgBinaryBusy, Retryable: true, RetryAfterS: nil}`, wrap, return `(resp, nil)`. Log WARN: `event=register_push_token.save_failed`, `conn_id=routing.ConnID`, `device_name=device.Name`, `err=<wrapped err>`. The in-memory state stays mutated (consistent with `Validate`'s LastSeenAt pattern: in-memory is the runtime source of truth; the next successful Save catches disk up). The phone retries per the retryable contract.
   - `MsgBinaryBusy` is a file-local string: `"registry save in progress; retry"`. No exported test pin needed (the AC test asserts code + retryable, not message text).
   - `RetryAfterS` is left as `nil` (no advisory wait). The dispatcher / phone may add backoff in a future ticket; this handler emits no hint.
8. **Write path success.** Build ack envelope, wrap, return `(resp, nil)`. Log INFO: `event=register_push_token.write`, `conn_id=routing.ConnID`, `device_name=payload.DeviceName`, `platform=payload.Platform`. NOTE: log the *new* `device_name` (from payload), not the stale Device snapshot — the snapshot might still hold the pre-rename name.

### Envelope construction

For all three outbound shapes (ack, error-unauth, error-save-failed), the structure is identical to `relay.buildResponse`'s pattern; reproduced inline in the handler. Sketch:

```go
func wrap(connID string, inReplyTo uint64, nextID uint64, envType string, payload any) (protocol.RoutingEnvelope, error) {
    payloadJSON, err := json.Marshal(payload)
    if err != nil { return protocol.RoutingEnvelope{}, fmt.Errorf("marshal %s payload: %w", envType, err) }
    e := protocol.Envelope{
        ID:        nextID,
        Type:      envType,
        TS:        time.Now().UTC(),
        Payload:   payloadJSON,
        InReplyTo: &inReplyTo,
    }
    envJSON, err := json.Marshal(e)
    if err != nil { return protocol.RoutingEnvelope{}, fmt.Errorf("marshal %s envelope: %w", envType, err) }
    return protocol.RoutingEnvelope{ConnID: connID, Frame: envJSON}, nil
}
```

Lowercase `wrap` in the handlers package, file-local. Not exported. A future cross-handler refactor may lift this into a `handlers/internal/wire` helper; for one handler it is premature.

### Data flow

```
relay-conn dispatcher
  │
  │  routing := <inbound RoutingEnvelope, Envelope.Type == register_push_token>
  │  device  := perConn.device  (set at AuthenticateFirstFrame accept; nil otherwise)
  │  nextID  := atomic.AddUint64(&perConn.idCounter, 1)
  │
  ▼
handlers.Handle(routing, device, reg, registryPath, nextID, logger)
  │
  │  1. decode routing.Frame → requestID
  │  2. device == nil? → error envelope (auth.invalid_token), return
  │  3. decode payload
  │  4. payload triple == device triple? → ack envelope, NO WRITE, return
  │  5. reg.UpdatePushRegistration(tokenHash, p, t, n)  ── in-memory mutate
  │  6. reg.Save(registryPath)
  │       err? → error envelope (server.binary_busy, retryable), return
  │  7. ack envelope, return
  │
  ▼
RoutingEnvelope (or ErrMalformedFrame on structural JSON failure)
  │
  ▼
dispatcher writes response back through relay.Connection
```

## Concurrency model

No goroutines spawned. No channels owned. The handler is a synchronous call. The shared state it touches:

- `reg.UpdatePushRegistration` — single critical section under `reg.mu`.
- `reg.Save` — takes its own snapshot under `reg.mu`, releases the lock, then does file I/O (existing primitive; unchanged).

Two concurrent `Handle` calls for the same conn (a misbehaving phone re-sending) would interleave at the registry mutex. Each performs an independent UpdatePushRegistration + Save; "last writer wins" both in-memory and on disk (Save's atomic rename is the commit point). The dispatcher serializes per-conn frame processing in practice (single inbound frame loop per conn), so this case is largely theoretical.

## Error handling

- **`ErrMalformedFrame`** — JSON-undecodable `routing.Frame` or `envelope.Payload`. Dispatcher routes to its existing protocol.malformed flow; handler synthesizes no envelope. Sentinel matches the pattern of `relay.ErrMalformedHelloFrame`.
- **Unauth (device == nil)** — `auth.invalid_token` error envelope, retryable: false. Defensive branch; should be unreachable in production once the dispatcher honours auth state.
- **Mid-conn revoke (UpdatePushRegistration returns false)** — `auth.invalid_token` error envelope, retryable: false. Same UX as a revoked-token reject. Defensive; not covered by AC tests but covered indirectly by the registry-mutator's "miss returns false" unit test.
- **Save failure (`reg.Save` returns non-nil)** — `server.binary_busy` error envelope, retryable: true. The phone retries; on the retry, dedupe will succeed (in-memory is already updated) and no further write attempt occurs. This is the **only retryable** error path in the handler.
- **`json.Marshal` failures** — propagated as `error` return (wrapped with `fmt.Errorf("marshal %s payload: %w", ...)`. Cannot happen in practice for these struct types; matches auth.go's defensive style.

## Testing strategy

New file `internal/relay/handlers/register_push_token_test.go`. Package `handlers` (not `handlers_test`) so the test can read the file-local `MsgUnauthorized` / `MsgBinaryBusy` constants for assertion if needed (alternative: assert on Code + Retryable only and skip Message). Stdlib `testing` only. Each test is its own function (matching auth_test.go's style); no table-driven loop because the test bodies diverge on which assertions run.

### Shared helpers (file-local)

```go
const (
    testConnID      = "c-test"
    testRequestID   = uint64(8)
    testPlatform    = "fcm"
    testPushToken   = "fcm-token-abc"
    testDeviceName  = "Juhana's Pixel 8"
    testNextID      = uint64(2)
    testTokenHash   = "..."  // devices.HashToken("plain-token")
)

func testLogger(t *testing.T) *slog.Logger {
    t.Helper()
    return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func makeRegisterRouting(t *testing.T, payload protocol.RegisterPushTokenPayload) protocol.RoutingEnvelope { ... }

func freshRegistryWithDevice(t *testing.T, d devices.Device) (*devices.Registry, string) {
    // Returns a registry seeded with d AND a registryPath inside t.TempDir().
    // The path's parent exists and is writable by default; the
    // save-failure test overrides it to a non-writable path.
}
```

### Test cases (one per AC requirement)

1. **`TestHandle_FirstTimeRegister_WritesAndAcks`**
   - Seed registry with a Device whose `Platform=""`, `PushToken=""`, `Name="old-name"`.
   - Build payload `{platform: "fcm", token: "fcm-token-abc", device_name: "new-name"}`.
   - Call Handle. Assert:
     - Returned envelope type is `ack`, `in_reply_to == testRequestID`, `id == testNextID`.
     - `os.Stat(registryPath)` succeeds (file was created by Save).
     - Read the file back via `devices.Load`; the Device now has Platform/PushToken/Name set to the new values.

2. **`TestHandle_ReregisterIdentical_NoWriteAndAcks`**
   - Seed registry with a Device whose triple already equals the incoming triple. **Important:** do NOT call `reg.Save` before the test (so no file exists on disk).
   - Call Handle. Assert:
     - Returned envelope type is `ack`.
     - `_, err := os.Stat(registryPath); errors.Is(err, fs.ErrNotExist)` — the file must NOT exist. This is the spy: if dedupe failed, Save would have created it.

3. **`TestHandle_ReregisterChanged_WritesAndAcks`**
   - Seed registry with a Device whose `Platform="fcm"`, `PushToken="old-fcm"`, `Name="phone"`. Save the file once so it exists with the old state.
   - Stat the file's mtime, record it.
   - Build payload with a different token (`"new-fcm"`).
   - Sleep 10ms (necessary on filesystems with second-precision mtime — alternatively, compare file contents directly; prefer content comparison to avoid timing flakes).
   - Call Handle. Assert:
     - Returned envelope is `ack`.
     - File contents (parsed via `devices.Load`) now show the new token.
     - **Content-based assertion preferred over mtime** to avoid CI flakes on filesystems with coarse mtime resolution.

4. **`TestHandle_SaveFailure_EmitsServerBinaryBusy`**
   - Seed registry with a Device differing from the payload (so we hit the write path).
   - Set `registryPath` to a path whose parent directory cannot be created — e.g., `filepath.Join(t.TempDir(), "blocker", "devices.json")` then `os.WriteFile(filepath.Join(t.TempDir(), "blocker"), []byte("file-not-dir"), 0o600)`. `Save` will fail at MkdirAll because a regular file blocks the parent path.
   - Call Handle. Assert:
     - Returned envelope is type `error`.
     - `ErrorPayload.Code == "server.binary_busy"`, `Retryable == true`, `RetryAfterS == nil`.
     - In-memory state IS mutated (call `reg.FindByTokenHash` and verify the new triple is present) — this is the documented post-condition; the disk failure does not roll back memory.

5. **`TestHandle_UnauthenticatedConn_EmitsAuthInvalidTokenNoWrite`**
   - Seed registry with one Device unrelated to the conn.
   - Call Handle with `device = nil` and any payload.
   - Assert:
     - Returned envelope is type `error`, `Code == "auth.invalid_token"`, `Retryable == false`.
     - `os.Stat(registryPath)` returns `fs.ErrNotExist` — no write.
     - The registry's in-memory device count is unchanged.

6. **`TestHandle_MalformedFrame_ReturnsSentinel`** (additional, off-AC but pins the sentinel)
   - `routing := protocol.RoutingEnvelope{ConnID: testConnID, Frame: []byte("not-json")}`.
   - Call Handle. Assert:
     - `errors.Is(err, ErrMalformedFrame)`.
     - Returned `RoutingEnvelope` is the zero value (no response synthesized).

### Registry mutator test (additive, in `internal/devices/registry_test.go`)

`TestRegistry_UpdatePushRegistration`:
- Seed two devices (A, B).
- Update A's row: assert returns `true`, A's fields are now set, B is untouched.
- Update by an unknown TokenHash: assert returns `false`, both devices unchanged.

Stdlib `testing`, table-driven where natural. Sits next to the existing `TestRegistry_FindByTokenHash` (registry_test.go:159).

## Open questions

None blocking. The following are deferred to sibling tickets and noted only so they do not re-emerge as architectural debate during implementation:

- **Dispatcher seam** — the relay-conn dispatcher (per-conn id allocation, Device cache, type-dispatch table) is a separate, larger ticket. `Handle`'s `nextID` parameter is the seam; the dispatcher fills it in.
- **`server.binary_busy` retry-after advisory** — left `nil` here. If we later observe phones retrying in tight loops on save failures, a sibling ticket can add a Config-driven default (e.g., 1–5s). No evidence of this failure yet; deferred per "Evidence-Based Fix Selection".
- **APNs/FCM dispatch** — emphatically out of scope. The push-delivery trigger is "binary detects offline phone → wakes via stored token" and is its own ticket once the message-dispatch chain exists.

## Size & file-overlap check

- **Production lines:** ~85 (handler ~70 + registry mutator ~15). Within S budget.
- **Files touched:** 4 (`internal/relay/handlers/register_push_token.go` new, `internal/relay/handlers/register_push_token_test.go` new, `internal/devices/registry.go` +15 lines, `internal/devices/registry_test.go` +one test).
- **Edit fan-out:** zero (handler is greenfield; no existing call sites to update).
- **In-flight branches checked (2026-05-12):** `origin/feature/55`, `origin/feature/58`, `origin/feature/59`, `origin/feature/251` — none touch `internal/relay/`, `internal/devices/`, or `internal/protocol/`. No overlap.

All green; spec ships as one ticket, no split.
