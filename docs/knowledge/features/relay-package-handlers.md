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
| `DeviceName` over `protocol.MaxDeviceNameBytes`, or `DeviceName`/`Platform` carries a C0 control (LF/CR/ESC included), DEL, or a C1 control (#2219) | `error`: `Code=protocol.malformed`, `Retryable=false`, static per-branch message (value NOT echoed) | none — checked before the dedupe comparison, see § below |
| Payload `(Platform, Token, DeviceName)` equals snapshot `(Platform, PushToken, Name)` | `ack` | **none — does NOT call `UpdatePushRegistration`, does NOT call `Save` (the dedupe contract); takes no lock** |
| In-region reload drops this conn's device (revoked mid-conn, #1532) so `reg.UpdatePushRegistration` reports no match | `error`: `Code=auth.invalid_token`, `Retryable=false` (same UX as unauth) | none — no `Save` runs |
| The devices lock is not acquired within `pushRegistryLockWait` (#1532) | `error`: `Code=server.binary_busy`, `Retryable=true` | none — `fn` never runs; `devices.json` untouched, not even opened |
| `reg.Save` (or the sidecar mkdir/open) fails inside the acquired region | `error`: `Code=server.binary_busy`, `Retryable=true` | **in-memory IS mutated (reload + `UpdatePushRegistration` already ran); disk is not** (phone retries; the retry genuinely re-attempts the write — see § below) |
| Triple differs, no concurrent revoke, and `Save` succeeds | `ack` | in-memory + disk both updated |

The outer-frame malformed branch is gone from the contract — the
dispatcher's `handleOne` decodes `env protocol.Envelope` from the
routing frame before invoking the handler and replies
`protocol.malformed` upstream. Only the **inner-payload** decode
remains the handler's responsibility.

**Reconcile, mutation and save run as one `devices.WithLock` region (#1532),
acquired before the reconcile's read.** Before #1532 this handler was the last
`devices.json` writer that did not hold the cross-process lock: it called
`Reload` and then `Save` with nothing held between them, so a `pyry pair`
committing in that window was erased by the handler's whole-file `Save` —
permanently, since `Reload` reconciles memory *from* disk. See
[`features/devices-registry.md`](devices-registry.md) § *Two-writer clobber
guard* for the full cross-process picture and
[ADR 029](../decisions/029-devices-registry-reload-at-handshake.md) § *Superseded
(2026-09-15, #1532)* for why the reload is not redundant under the lock: the
lock excludes a writer from committing *during* the region, but says nothing
about one that already committed between this conn's handshake reload and this
acquisition.

**The reconcile now runs before the mutation**, which is what makes the
gone-mid-conn row above reachable for a revoke that lands *during* this
handler's lifetime, not just before it — the reload drops the revoked row,
`UpdatePushRegistration` reports no match, and nothing is saved. The dedupe row
and the three display-safety rows all stay *outside* the locked region and take
no lock at all, so a deduped or refused frame creates no `.lock` sidecar
either — a strictly stronger "the region was never entered" witness than "no
`Save` ran" (`devices.WithLock` creates the sidecar before running anything
handed to it).

### Dedupe is load-bearing

Spec § Phone background behaviour has the phone re-register on every WS connect (~100 bytes, self-heals registry drift). Without dedupe, every WS connect rewrites `devices.json` — flash wear and i/o churn for what's typically a no-op. The handler's "no write occurred" contract is structurally enforced: the dedupe branch returns the ack **before** calling `UpdatePushRegistration` or `Save`. Test pinpoints this via `errors.Is(os.Stat(path), fs.ErrNotExist)` with the file pre-state set to "never existed" — if dedupe were broken, Save would have created it.

### `Name` is part of the triple

The protocol's `device_name` makes the phone the source of truth for self-reported name (an iOS Settings rename should propagate). So the dedupe comparison is `(Platform, PushToken, Name)`, not just `(Platform, PushToken)`. The registry mutator (`devices.Registry.UpdatePushRegistration`) overwrites all three fields together — see [`features/devices-registry.md`](devices-registry.md).

### Save-failure leaves in-memory mutated; a busy lock leaves nothing touched

Documented post-condition, mirroring `Validate`'s `LastSeenAt` pattern: in-memory is the runtime source of truth; the next successful Save catches disk up. Test pins `reg.FindByTokenHash(...).PushToken == "new-fcm"` after the failed call. This holds only for a failure *inside* the region (the reload and mutation already ran before `Save` failed); a failure to acquire the lock at all means `fn` never ran, so neither memory nor disk is touched — the two retryable-error rows in the contract table above share a reply but not a post-condition, and only the log event (`save_failed` vs. `lock_busy`) tells them apart.

**The retry genuinely re-attempts the write — this handler's doc comment said the opposite until #1532.** `msgBinaryBusy`'s comment used to claim a retry costs nothing because in-memory is already updated by the time the phone re-sends. That conflates the dedupe comparison with the connection's auth snapshot: dedupe compares the payload against `dev := c.Auth()`, which is `s.device` in `internal/relay/v2session_handshake.go` — taken once at handshake and never updated by any write on that connection. A retry therefore does not dedupe away regardless of what the registry now holds, which is exactly what makes a busy-lock refusal *meaningfully* retryable rather than a no-op: the contending writer holds a sub-millisecond region (every other production `WithLock` caller does), so the retry typically finds the lock free.

### Display-safety gate on `device_name` and `platform` (#2219)

`Device.Name` is remote-authored — the phone sets its own name via this frame, and
`UpdatePushRegistration` (see [`devices-registry.md`](devices-registry.md#phase-3-foundation-250))
assigns it verbatim. Nothing checked its shape until #2219, so a device naming itself
`"kitchen\nfake log line"` could forge a line in the daemon's log, an `audit.Entry.DeviceLabel`
entry, or a `pyry pair list` row. Three guards run **after decode and before the dedupe
comparison**:

1. `len(p.DeviceName) > protocol.MaxDeviceNameBytes` → refuse.
2. `DeviceName` carries a C0 control (including LF, CR, ESC), DEL, or a C1 control (U+0080–U+009F) → refuse.
3. `Platform` carries the same character classes → refuse (no byte bound — `platform` has none to anchor; its ceiling stays the ~65519-byte application-envelope cap).

**The predicate is a third restatement, not a shared import.** `internal/relay` →
`mintLabelIsDisplaySafe` (see
[the mint-pairing seam](v2-session-manager-state-machine-inbound-mint-pairing-pairingminter-seam.md))
already refuses the identical set, and `internal/sessions` → `admissibleClientField` refuses
a superset (adds `"` and invalid-UTF-8, for reasons specific to a system-prompt rendering).
Importing either was rejected: `handlers` importing `internal/relay` puts a child
package's dependency on its own parent in place for a six-line loop, and exporting the
predicate from `internal/protocol` would break that package's pure-DTO posture (see
`RegisterPushTokenPayload` below). The three copies must move together — the refused set
is specified once, by `mintLabelIsDisplaySafe`'s doc block, and the other two restate it.

**The ordering is load-bearing, not stylistic.** The dedupe branch acks without writing
when the payload triple equals the stored one. A device whose name was stored *before*
this gate existed could otherwise repeat that unsafe name forever and be acked — never
reaching a guard — if the guards ran after the comparison. Running them first makes "an
unsafe value is refused" true of every frame, not just every *changed* frame. This is the
one property in the file no other test can distinguish (see Test surface below).

**A test value spelling a C0 control must not also spell a substrate escape sequence.**
The first draft's ESC rows embedded a full CSI run (`\x1b[31m`, an SGR color code) to look
like a realistic terminal-injection payload. `cmd/substrate-guard` bans the ESC-then-`[`
source byte sequence outside a two-path allowlist that a handlers test file does not
join, so both rows reddened the merge gate — after every test tier and `staticcheck` had
already passed. The predicate refuses ESC as a C0 control on its own; the CSI tail
exercised no additional branch, so the fix drops it (`kitchen\x1bpad`, not
`kitchen\x1b[31mpad`) with no loss of coverage. The `\u`-escape spelling of ESC also clears
the scan, and was rejected for the same reason a passing-for-the-wrong-reason test is
rejected elsewhere in this file: it evades the gate rather than satisfying it.

Storage stays verbatim on the accept path — no truncation, escaping or repair — matching
`MintPairingPayload`'s refuse-never-truncate posture for the same label
(see [`protocol-package-types-pairing-payloads.md`](protocol-package-types-pairing-payloads.md)).
The published contract is in
[`docs/protocol-mobile.md` § Application message types](../../protocol-mobile.md#application-message-types).

**Closing this door is not the same as making `Device.Name` display-safe as a type
invariant**, and the `pairingMinterV2.MintPairing` seam comment this ticket corrects makes
exactly that distinction rather than flipping to "handled": a name written to
`devices.json` before this gate existed is read back unchecked (out of scope), and
`pyry pair --name` stays deliberately ungated — `mintLabelIsDisplaySafe`'s own doc block
states why that check does not live in the shared mint step. See
[the mint-pairing seam doc](v2-session-manager-state-machine-inbound-mint-pairing-pairingminter-seam.md)
for the full correction.

### Sub-package isolation

`handlers` imports `internal/devices`, `internal/dispatch`, and `internal/protocol`. **It does NOT import `internal/relay`.** The new `internal/dispatch` edge (added #319) is cycle-free because `internal/dispatch`'s only handler-direction dependency is the `Handler` function type, which `handlers` consumes structurally. `auth.go` stays in `internal/relay` proper alongside the live Noise_IK handshake that now consumes its two WS close-code constants (see [`relay-package.md` § Auth](relay-package.md#auth-ws-close-code-constants-authgo)) — it is no longer a per-type handler or a dispatch gate.

The `wrap(connID, inReplyTo, nextID, envType, payload)` file-local helper from #250 is gone — `c.Reply(ctx, env, type, payloadJSON)` is the central stamping path now (#319). Two file-local helpers (`replyAck`, `replyError`) marshal the payload and call `c.Reply`.

### Logging discipline

| Event | Level | Fields |
|---|---|---|
| `relay: register_push_token write` | Info | `event=register_push_token.write`, `conn_id`, `device_name=payload.DeviceName`, `platform` |
| `relay: register_push_token dedupe` | Debug | `event=register_push_token.dedupe`, `conn_id`, `device_name=device.Name` |
| `relay: register_push_token reload failed` | Warn | `event=register_push_token.reload_failed`, `conn_id`, `device_name`, `path` — **never `err`** (#1532; the in-region reload is best-effort, see § below) |
| `relay: register_push_token save failed` | Warn | `event=register_push_token.save_failed`, `conn_id`, `device_name`, `err` |
| `relay: register_push_token devices lock busy` | Warn | `event=register_push_token.lock_busy`, `conn_id`, `path`, `err` — **no `device_name`** (#1532; a brand-new branch takes #2219's strict posture, not this handler's older one) |
| `relay: register_push_token device gone mid-conn` | Warn | `event=register_push_token.gone_mid_conn`, `conn_id`, `device_name` |
| `relay: register_push_token unauth` | Warn | `event=register_push_token.unauth`, `conn_id`, `code=auth.invalid_token` |
| `relay: register_push_token malformed` | Warn | `event=register_push_token.malformed`, `conn_id` — **no `err` field (#2219)** |
| `relay: register_push_token unsafe field` (3 branches: oversize name, unsafe name, unsafe platform) | Warn | `event`, `conn_id` only — no field value, no length, no bound |

Push token (FCM/APNs registration id) is opaque infrastructure data, not a secret on par with the phone-side device token — but is still NOT logged (no operational signal worth the noise). Device-side token from auth is NEVER read or logged. Device name IS logged on every path that has one (write/dedupe/save-failed/reload-failed/gone-mid-conn) — the inverse of #249's reject-path discipline, because the handler runs post-auth: the caller has already cleared the auth gate, so there is nothing to enumerate. Unauth (`device == nil`) by definition has no name to log. The one exception is the new `lock_busy` branch (#1532), which deliberately withholds `device_name` even though the connection is authenticated — it is new code, so it inherits #2219's strict posture instead of grandfathering in the older branches' habit of logging the pre-gate residual.

**Lock-busy gets a distinct log event for an identical reply, on purpose.** A busy lock, a sidecar mkdir/open failure and a `Save` failure all map to the same retryable `server.binary_busy` reply — the phone cannot act on the distinction, so nothing about the wire contract changes. The distinct `lock_busy` event exists for the operator reading the log, who can tell "another writer held it" from "the disk write itself failed" for free, since a log event is not a reply.

**Why `Reload`'s failure never logs `err`.** `readDevicesFile` wraps a decode failure that can echo `devices.json` bytes, and a corrupt registry may carry a `token_hash` — the same SECURITY rule #782 established for this handler's pre-#1532 reload. The failure is consumed *inside* the locked closure and never returned from it, so it structurally cannot reach a log field; what the closure *can* return is `devices.WithLock`'s own errors (which name only the lock path, by its documented contract), `Save`'s wraps (a path or a fixed step word), and the static `errPushDeviceGone` — a closed set, safe to log unconditionally on the `save failed` branch.

**The malformed branch lost its `err` field in #2219, and the three new reject branches were never given one.** `encoding/json` quotes offending input into its error text, and a type error midway through a well-formed object returns an error with fields already populated from supplied bytes — so logging the decode error was itself a display-safety hole, on the exact branch meant to catch unsafe input. The three unsafe-field branches name neither the field, the value, its length, nor the bound: which field failed is not an oracle worth withholding value over, but distinguishing them buys the client nothing it did not already hold, since it authored both fields — the same reasoning `pairing.not_permitted` publishes for its own refusal.

### Test surface (rewritten #319, extended #2219, locked #1532)

`internal/relay/handlers/register_push_token_test.go` — stdlib only, package `handlers`, all under `-race`. The fixture uses `dispatch.NewTestConn(testConnID, out, dev)` to build a `*dispatch.Conn` with a buffered outbound channel the test drains; `_ = c.NextID()` is called once after construction so the first handler reply observes `id=2` (mirroring the gate's `hello_ack=1` accounting). The lock-timing tests are deliberately **not** `t.Parallel()` — they retune or read `pushRegistryLockWait` and rely on a grace window, and Go resumes a package's parallel tests only after the sequential ones finish.

- `TestRegisterPushToken_FirstTimeRegister_WritesAndAcks` — happy-path write + reload via `devices.Load` and assert the triple.
- `TestRegisterPushToken_ReregisterIdentical_NoWriteAndAcks` — dedupe spy: file deliberately not pre-Saved, asserts `errors.Is(os.Stat, fs.ErrNotExist)` after the call.
- `TestRegisterPushToken_ReregisterChanged_WritesAndAcks` — pre-Save, change one field, content-equality post-call (sidesteps CI mtime-resolution flakes).
- `TestRegisterPushToken_ReloadPreventsClobberOfNewlyPairedDevice` — the non-racing reload case (a device added to disk since this conn's handshake is preserved by the in-region reload); kept passing untouched by #1532.
- `TestRegisterPushToken_SurvivesWriteCommittedWhileParkedOnLock` (#1532) — the flagship interleaving test, covering both directions at once: park a lock holder, start the handler, assert it has *not* completed after a grace (proving a live holder actually blocks it), commit a second write from under the holder (adds one device, revokes another), release, then assert the handler's own device survived with its push registration, the concurrently added device is present, and the concurrently revoked one is not. Reddens on the mutant that narrows the region to `Save` alone — verified by mutation-check, not assumed: with `Reload` and `UpdatePushRegistration` hoisted outside the lock, both directions fail at once (the added device is erased, the revoked one is resurrected) while the grace assertion alone still passes, which is the concrete proof a busy-lock-only test could not have caught it.
- `TestRegisterPushToken_ReconcileDropsDevice_RefusedNotAcked` (#1532) — memory has this conn's device, disk does not (revoked out from under the conn); asserts the non-retryable `auth.invalid_token` reply and a back-dated mtime proving no `Save` ran. This is the one test that tells reconcile-before-mutate apart from the old mutate-before-reconcile ordering, which would ack and write instead.
- `TestRegisterPushToken_LockBusy_EmitsServerBinaryBusyWithoutWriting` (#1532) — park a holder, run the handler, let `pushRegistryLockWait` elapse; asserts `server.binary_busy`/`Retryable=true`, a back-dated mtime, the distinct `register_push_token.lock_busy` log event, and — the no-leak half — that the captured log contains neither the plain token nor its hash.
- `TestRegisterPushToken_GoneMidConn_EmitsAuthInvalidToken` — `dev`'s TokenHash absent from the registry forces `UpdatePushRegistration` to return false; asserts `auth.invalid_token`, `Retryable=false`.
- `TestRegisterPushToken_SaveFailure_EmitsServerBinaryBusy` — **repaired in #1532.** Before #1532 this blocked `Save` with a regular file at the parent (`MkdirAll` failure); wrapping the region in `WithLock` would have made that fail at the sidecar open instead, one layer earlier, and stay green while no longer exercising `Save`. The repair pre-creates the `.lock` sidecar at `0600` before `chmod 0500`-ing the parent directory (`O_CREATE` on an existing file needs only `x` on the directory, so the lock still opens), and asserts the captured log contains `Save`'s own step word, `registry: create temp` — the assertion that stops the test re-hollowing silently. `cmd/pyry`'s `TestRunPairRevoke_SaveFailure` was repaired identically; see [`pyry-pair-command.md`](pyry-pair-command.md) § *Tests*.
- `TestRegisterPushToken_UnauthenticatedConn_EmitsAuthInvalidTokenNoWrite` — `auth=nil`; asserts `auth.invalid_token` shape + no file + unchanged registry.
- `TestRegisterPushToken_MalformedPayload_EmitsProtocolMalformed` — `env.Payload = []byte("not-json")`; asserts `Code=protocol.malformed`, `Retryable=false`. New case; replaces the deleted `TestHandle_MalformedFrame_ReturnsSentinel` (the dispatcher owns the malformed-frame path now).
- `TestRegisterPushToken_UnsafeFieldValues_EmitProtocolMalformedNoWrite`, `TestRegisterPushToken_DeviceNameByteBound_RefusesOverAcceptsAt`, `TestRegisterPushToken_UnsafeNameMatchingStored_RefusedNotDeduped`, `TestRegisterPushToken_AdmissibleFieldValues_StoredVerbatim` (#2219) — the display-safety gate table, its byte-bound boundary, and the ordering test proving a stored-then-repeated unsafe name is refused rather than deduped.

Every reject/dedupe/malformed test above creates no `.lock` sidecar — asserted directly (`assertNoSidecar`, folded into a shared `assertRejectedNoWrite` helper) as the strictly stronger claim that the fast paths never enter the locked region at all (#1532).

`newTestConn(t, dev)`, `makeRequest(t, payload)`, `capturePushLogger()` and `holdPushLock` (the lock-timing tests' background-holder idiom, mirroring `holdLock` in `internal/devices/lock_test.go`) are the file-local helpers; the `assertEnvelopeShape` / `equalRouting` / `makeRegisterRouting` helpers from #250 are gone with the sentinel.

#### Display-safety gate tests (#2219)

Table-driven additions covering the three new reject branches: every C0 control (LF, CR,
ESC included), DEL, and every C1 control, each on both `device_name` and `platform`; the
`MaxDeviceNameBytes` boundary (exactly at the bound stored, one byte over refused); the
character-set edges (U+001F refused / U+0020 accepted, U+007E accepted / U+007F refused,
U+009F refused / U+00A0 accepted, `"` accepted — the last pins that the predicate does
**not** inherit `admissibleClientField`'s extra quote rule); and the ordering test, an
unsafe `Name` already stored pre-gate that repeats verbatim in the payload, which must be
refused rather than deduped. Four mutants — guards moved after the dedupe comparison, the
byte-bound off-by-one, the predicate copy-pasting `admissibleClientField`'s `"` rule, and
a C1-range off-by-one — were each confirmed to redden exactly the test row written for it,
via `go test -overlay` (no worktree writes).

#### e2e (`internal/e2e/register_push_token_test.go`, new #319)

Build tag `e2e`. `TestRelay_RegisterPushToken_AckAndPersists` pairs a device via `RunBareIn(t, home, "pair", "-pyry-name=test", "--name=phone-a")`, decodes the plaintext token via `decodePairPayload`, boots the daemon against a `fakerelay` with `PYRY_ALLOW_INSECURE_RELAY=1`, dials a `fakephone` with the paired token, sends `hello` → expects `hello_ack` (`*InReplyTo == 1`), sends `register_push_token` with `ID: 2` → expects `ack` (`*InReplyTo == 2`, `env.ID >= 2` — strictly-greater leaves room for future dispatcher-side replies between `hello_ack` and this ack), then reloads `~/.pyry/test/devices.json` and asserts the `(Platform, PushToken, Name)` triple is persisted.

## `send_message` grows a sixth seam: conversation auto-naming (#2159)

`SendMessage` widened again — `reg ConversationAutoNamer, announce ConversationAnnouncer` — to derive and store a conversation's name from its first accepted message, then push the updated row through the existing `conversation_updated` fan-out. See [`conversation-session-binding-routing.md` § Enqueue-and-ack (#721)](conversation-session-binding-routing.md#enqueue-and-ack-721) for the ordering lesson this ticket produced: the step runs *after* `replyAck`, not before, because anything sitting between `EnqueueDelivery` and the ack races the drain goroutine it just handed the turn to.

**A narrow interface earns a separate name even when its method set is identical to a sibling's.** `ConversationAutoNamer` (`Update` + `Save` + `WorkspaceLabel`) has the exact method set of `ConversationRenamer`, and this package already tolerates that kind of overlap (`ConversationPromoter`, `ConversationArchiver`, `ConversationRenamer` all share most of their shape). It is still declared as its own type rather than reused, because the doc comment on a consumer-declared interface carries that *consumer's* contract, not just its shape: `ConversationAutoNamer`'s comment records "writes `Name` only over a nil one," which `ConversationRenamer` must never claim. A reviewer pattern-matching on method sets alone will read the duplication as redundant; it isn't — the two interfaces document two different promises about the same registry method.
