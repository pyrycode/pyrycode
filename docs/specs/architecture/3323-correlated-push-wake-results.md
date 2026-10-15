# #3323 — compatible correlated invalid-token push results

## Files read

- `docs/protocol-mobile.md` → Routing envelope / `push_wake`, Wire shapes, Versioning and Security model: canonical legacy shapes, unknown-field tolerance and the relay trust boundary.
- `docs/specs/architecture/2565-push-wake-envelope.md` → Documentation handoff: precedent for reviewing protocol wording before documentation-stage publication.
- `docs/knowledge/features/protocol-package.md` → Security posture: outer framing does not establish semantic validity.
- `docs/knowledge/features/relay-package-push-wake.md` → Daemon-side trigger: wakes use authenticated device identity; cancellation and token-free diagnostics remain essential.
- `docs/knowledge/features/relay-package-handlers.md` → Reconcile, mutation and save run as one `devices.WithLock` region: reload must precede mutation to preserve concurrent pair/revoke writes.
- `docs/knowledge/features/development-verification.md` → Protocol boundaries: literal JSON and malformed live bindings distinguish real rejection from an unbound no-op.
- `cmd/pyry/push_wake.go` → `pushWaker.wakeAbsent`: currently emits uncorrelated wakes and coalesces locally for 30 seconds.
- `internal/relay/connection.go` → `marshalPushWake`, `SendPushWake`, `run`, `forwardFrames`: separate relay-addressed request shape; the inbound channel persists across reconnects.
- `internal/relay/v2session.go` → `V2SessionManager.handleFrame`: currently drops inbound envelopes without a phone `conn_id`.
- `internal/devices/registry.go` → `Reload`, `reconcileDevices`, `UpdatePushRegistration`, `Save`: membership reconciliation preserves surviving in-memory fields; separate find/update calls do not make compare-and-clear atomic.
- `internal/relay/handlers/register_push_token.go` → `RegisterPushToken`: existing registration writer and cross-process persistence boundary.
- Relay `internal/relay/forward.go` → `StartBinaryForwarder`: outer wake dispatch must retain the actual originating connection for feedback.
- Relay `internal/relay/push_wake.go` → `PushWaker.Request`, `send`, `pushWakeJob`: complete-token coalescing crosses daemon identities, with fixed 10-second retention, 45-second jobs and 32 retained jobs.
- Relay `internal/relay/push.go` → `FCMSender.Send`, `ErrFCMUnregistered`: only provider `UNREGISTERED` identifies a permanently dead registration.
- Relay `internal/relay/envelope.go` → `parsePushWake`: ordinary wake validation is platform `fcm` and a nonempty token; added correlation must not change it.
- Relay `internal/relay/ws_conn.go` → `WSConn.Send`, `Read`: the existing serialized writer has a 10-second timeout; feedback needs its own bounded, cancellable delivery contract.
- Core #3423 and relay #213: separate consumer/delivery implementations depend on canonical publication; neither blocks this specification.

## Context

The existing request has no reply contract. A dead FCM registration can therefore
keep spending wake allowance, while clearing a device by name or current
server-id would risk deleting a replacement registration. This ticket defines
one additive outer protocol contract, following #2565's plan-to-documentation
publication pattern. It changes no production code or test harness.

Sizing, checked against the sketch and final plan: about 300 plan/handoff lines,
no exported types, no consumer migrations, three acceptance criteria and no
implemented state machine. The canonical edit belongs to the later documentation
stage; total proposed written work remains below 800 lines. Eighteen remote
feature branches were checked; none overlaps this plan or the canonical path.
Relay source was inspected at `9939bc1ff8afec5021c09862e6f14dadba33ad42`.
The canonical protocol section records this decision; a separate ADR is unnecessary.

## Design

An optional request-local `request_id` opts into one sanitised `invalid_token`
result. Relay #213 captures each request's original connection and maintains
bounded subscriptions on shared complete-token jobs. Core #3423 binds each local
correlation to a connection generation, paired identity and registration snapshot,
then conditionally clears only a still-matching push registration.

All normative requirements, examples and companion conformance scenarios are in
the Documentation handoff below, so publication need not invent further decisions.

## Concurrency model

This branch adds no goroutines, locks or runtime state. Companion implementations
must keep provider work and result writes off the binary read loop, count queued
delivery against the same feedback caps, and cancel/join delivery on shutdown.
Daemon compare-and-clear must serialize with registration mutation and use the
existing cross-process lock/reconcile/mutate/save boundary. Connection-generation
changes invalidate pending work before any result can mutate a registry.

## State transitions and identity reuse

None implemented here: this branch contains specification text only. The handoff's
scenarios C1–C10 specify the companion lifecycle proofs, including coalescing,
repeated requests/results, replacement registration, revoke/re-pair, disconnect,
reconnect, expiry, job eviction and shutdown. They are pending companion tests,
to run with `go test -race`; this PR makes no claim that they already pass.

## Error handling

Malformed correlation disables feedback without changing otherwise-valid wake
admission. Feedback overflow, expiry, delivery failure and non-`UNREGISTERED`
provider outcomes never imply invalidity. Malformed or unbound results are inert.
Storage errors follow existing registry persistence semantics, with no unlocked
fallback or token-bearing diagnostics; Core #3423 owns implementation and tests.

## Testing strategy

Review literal examples and every conformance outcome against current source and
the refined contract. Check JSON syntax, field bounds, Markdown/diff hygiene and
allowed paths. Run `go vet ./...` and build `./cmd/pyry` outside the worktree.
There are no touched Go packages requiring a race suite or new executable tests.
The verifier runs the full-module gate; the documentation stage runs the applicable
documentation gate after the canonical edit. Companion proofs and operator
activation are explicitly pending, rather than treated as results from this PR.

## Open questions

None. Queued writes retain accounting after coalescing-cache eviction; they do not
keep that job available for new subscribers or extend its provider deadline.
The 5-second feedback budget includes queue/writer wait, preventing the existing
10-second `WSConn.Send` timeout from being mistaken for this new contract.

## Documentation handoff

**Status: published in [the canonical protocol](../../protocol-mobile.md#push_wake_result).**
The publication carries the complete contract
below in `docs/protocol-mobile.md`, **Routing envelope (binary↔relay leg)** /
**push_wake**, before #3323 closes or either companion implements it. It preserves
the legacy request, unknown-field tolerance and payload-free FCM wake semantics.
Under **Wire shapes**, it distinguishes the phone-addressed `{conn_id, frame, ...}`
wrapper from the relay-addressed request and daemon-addressed result, linking
both subsections. The result is an outer routing envelope, never an inner type.

### Proposed canonical text: opt-in request

A daemon MAY add `push_wake.request_id` to an otherwise valid legacy request:

```json
{"push_wake":{"platform":"fcm","token":"<opaque FCM registration token>","request_id":"wake-A-1"}}
```

`request_id` is an opaque, token-independent string of **1–64 ASCII bytes** drawn
only from `A–Z`, `a–z`, `0–9`, `_` and `-`. It MUST NOT encode a device token,
token hash, credential or application content. A daemon MUST use a fresh identifier
for each new request and MUST NOT reuse it on the same relay connection.
Its presence with valid syntax opts that request into feedback; omission requests
legacy behavior with no result. An empty, overlong, non-ASCII, wrong-typed or
otherwise invalid correlation disables feedback for an otherwise valid wake.
It MUST NOT close the connection or change ordinary wake admission/coalescing.
Syntactically malformed JSON remains an ordinary malformed envelope.

The existing fields remain `platform: "fcm"` and a nonempty opaque registration
`token` passed verbatim. Correlation adds no new platform/token admission rule.
Old wake-capable relays ignore `request_id`; pre-wake relays may drop the request.
Neither case produces feedback or breaks the relay leg. Unknown fields continue
to be tolerated. There is no capability handshake or guaranteed feedback support.

### Proposed canonical text: `push_wake_result`

A feedback-capable relay MAY send this relay→originating-daemon result:

```json
{"push_wake_result":{"request_id":"wake-A-1","code":"invalid_token"}}
```

The result object has only `request_id`, using the same **1–64 ASCII-byte** syntax,
and `code`, whose sole allowlisted value is **`invalid_token` (13 ASCII bytes)**.
Senders MUST omit `conn_id`, `frame` and `token`; results are never Noise-sealed
and MUST NOT be forwarded to phones. They travel only on the captured originating
daemon's `/v1/server` WebSocket. Receivers tolerate unknown fields but MUST ignore
malformed required fields and unknown codes without changing a registration.

Only a terminal provider **FCM `UNREGISTERED`**, represented by relay
`ErrFCMUnregistered`, permits `invalid_token`. Success, other 4xx responses,
authentication failure, transient errors, retry exhaustion, cancellation and wake
admission rejection produce **no invalid-token result**. The result is not a
generic delivery status; silence never classifies a registration.

Results and feedback errors, logs and metric labels MUST NOT contain raw device,
admission or access tokens, token hashes, OAuth credentials, FCM reply text or
inner-frame content. Correlation is not permission to echo other request data.
Provider classification and diagnostics remain sanitised and content-blind.

### Proposed canonical text: bounds and overflow

| State or operation | Hard maximum / expiry |
|---|---|
| Daemon local pending correlations | 128 per originating relay connection; expire 60 seconds after request submission |
| Relay subscription lifetime | No later than 60 seconds after acceptance |
| Relay feedback records for one original daemon connection | 128 |
| Relay feedback records for one retained coalesced job | 64 |
| Relay feedback records across the relay process | 2048 |
| Feedback delivery, including queue and writer wait | 5 seconds, also bounded by remaining subscription lifetime and connection/shutdown cancellation |

Relay pending subscriptions, queued results and duplicate-suppression records
share these allowances; they are not independent pools. A transition from pending
to queued or seen MUST NOT release accounting while its feedback record survives.
Existing provider limits remain **32 retained jobs**, an immutable **45-second job
deadline**, the original **10-second coalescing window**, and existing attempt
budgets/retry accounting. Feedback MUST NOT restart or extend them.

On feedback-state overflow, retain accepted subscriptions and decline feedback
for the new correlation. Ordinary wake admission and coalescing remain unchanged;
there is no overflow result or eviction of an accepted subscriber to make room.
A daemon at its own pending cap MAY send an uncorrelated legacy wake instead.
Repeats/coalescing MUST NOT renew either expiration clock. At expiry, discard
the entry; silence or expiry MUST NOT clear a registration.

Result writes are best effort and cannot stall request reads or create unbounded
queues, goroutines or retained state. Queued delivery keeps the original
connection/job accounting until completion or cancellation, even if that job's
coalescing-cache entry has been evicted. It cannot accept new subscriptions,
restore that cache or extend the provider job. Shutdown cancels and joins result
delivery, releases pending state and starts no new writes.

### Proposed canonical text: coalescing and cardinality

Requests for the same **complete FCM token** share the existing provider job,
including requests from different daemon connections. Each distinct accepted
correlation attaches to its exact originating connection. On permanent-invalid
completion the relay attempts **at most one result per accepted
`(originating connection, request_id)` in that retained job**. An active duplicate
allocates no second record, produces no second result and extends no deadline.
Equal request IDs on two different connections are independent bindings.

During the original completed-job window, keep only the sanitised terminal outcome
and bounded deduplication metadata. A new eligible correlation may receive cached
`invalid_token` without another FCM send; an already-seen correlation gets no
second result, including after a failed write. Success/transient outcomes never
become invalid feedback. Evict this cache/duplicate metadata once both the active
provider send has ended and the original 10-second window has elapsed; expiry
may remove a subscriber sooner. Completion after that window can still attempt
bounded feedback for existing subscribers, but does not open a new cache window.

There is no replay after eviction, disconnect or restart. An already-enqueued
original result may finish within its delivery budget; it is not cache replay.
A fresh job after eviction is a separate ordinary wake attempt. There is no
durable, guaranteed or exactly-once delivery; the daemon must tolerate missing
and duplicate network results independently of relay attempt cardinality.

### Proposed canonical text: connection and registration safety

Subscriptions and queued writes MUST capture the original connection identity;
they MUST NOT route by looking up the current connection for a server-id. Drop
them when that connection disconnects or is replaced. A replacement claiming the
same server-id receives no stale result. Reconnect starts with empty feedback
state on both sides; the underlying provider job may continue under its original
budget, but the departed subscriber has no replay rights.

A daemon accepts only a live, unexpired local pending correlation on the same
connection generation, consuming it at most once. Unknown, expired, duplicate,
malformed or stale-generation results are inert. A send failure invalidates its
local pending binding. Register the binding before a result can arrive, and fence
both sending and receiving to that connection generation; a persistent frame
channel alone does not supply this identity guarantee.

The local binding includes the **paired device identity and exact push-registration
snapshot**, including platform and token. Never identify the target by mutable
device name or by the relay's result alone. Only a still-matching registration
may be cleared. Recheck and conditionally clear atomically against in-process
registration updates, inside the existing `devices.WithLock` → `Registry.Reload`
→ registry mutation → `Registry.Save` region. Preserve pairing, names, unrelated
fields/devices and replacement registrations; a revoked/re-paired device is a
different binding even if its display name is reused. Persist a successful clear.
Lock/reconcile/save failures follow existing persistence semantics, without an
unlocked fallback, resurrecting a revoked pairing or claiming an unsaved clear
is durable. The companion consumer specifies and tests those failure paths.

This result is an **advisory relay report**, not cryptographic proof of provider
behavior. A malicious relay can falsely classify a still-matching registration
and deny push service; it gains no Noise key, phone authorization or content.

### Proposed canonical text: compatibility cases

| Daemon / relay | Observable behavior |
|---|---|
| Old daemon / feedback-capable relay | Legacy request has no `request_id`; wake behavior is unchanged and no unsolicited result is sent. An old daemon ignores an unexpected no-`conn_id` result. |
| New daemon / old wake-capable relay | Added field is ignored; ordinary wake works, no result arrives, correlation expires without clearing a registration. |
| New daemon / pre-wake relay | Request may be dropped; relay leg stays open, correlation expires without clearing a registration. |
| Opted-in new daemon / new relay | An accepted live correlation may receive sanitised invalid feedback; only a still-matching registration clears. |
| New daemon / new relay, omitted or invalid correlation | Ordinary wake admission/coalescing is unchanged; no feedback for that request. |

### Proposed canonical text: portable synthetic conformance

These are **required companion implementation scenarios, not passing results from
protocol publication**. Use two independent fake daemon connections A/B, temporary
registries, a held synthetic sender/provider reply, controlled clocks and barriers.
Exercise raw literal JSON through the outer routing boundary, inspect results on
each connection and prove persisted registry state by reloading disk. No
credentials, live FCM, real Claude or host-specific paths are required. Repeat
concurrency scenarios under the race detector. Count provider jobs/attempts,
result attempts/deliveries and retained records separately.

| Scenario | Procedure and observable expected outcome |
|---|---|
| C1 — cross-connection coalescing and replacement | A/B request the same synthetic token T with `wake-A-1` / `wake-B-1` while the sender is held. Exactly one provider job starts. Replace A's registration with R, keep A connected, then release `UNREGISTERED` before expiry. With healthy writes, exactly one result reaches each origin; B clears T in memory/on disk, A retains R, and both pairings and unrelated fields survive. Also use the same request ID on A/B to prove connection-local identity. |
| C2 — active duplicates and cached completion | Repeat A's original correlation while held: no new record, send or result. Complete at t=2 seconds. Before t=10, a fresh eligible correlation receives one cached invalid result, the repeated original receives zero additional results, and provider invocation count remains one. Repeat with cached success and transient outcomes: zero invalid results. Duplicate input to the daemon causes zero additional mutations or saves. |
| C3 — disconnect, replacement, reconnect | Hold a request from A, disconnect it or replace it with A2 claiming the same server-id, then complete `UNREGISTERED`. A2 receives zero old results; its registration/pairing survives. Inject an old-generation result after reconnect: zero mutations. B's independent live subscription still gets at most one result. No queued write is redirected to A2. |
| C4 — non-invalid outcomes and admission | Separately return success, non-UNREGISTERED 4xx, OAuth/authentication failure, transient failure/exhaustion and cancellation; separately refuse ordinary wake admission. Each produces zero invalid results and zero consumer clears. Retry/job deadlines and attempt charges match the existing policy. |
| C5 — expiry and unknown/malformed replies | Inject valid replies into live matching local bindings just before 60 seconds and at/after 60 seconds: only the pre-expiry binding may clear. Unknown IDs, consumed duplicates, absent/wrong-typed/overlong IDs and unknown codes cause zero mutation/save, even when another required field names a live binding. Relay state is empty by subscription expiry; the 45-second job deadline still cancels held provider work, so testing 60-second daemon expiry uses injected delayed replies rather than extending a real job. |
| C6 — exact feedback capacities | Admit 64 distinct correlations to one held job; the 65th gets no feedback record/result and no extra provider send. Admit 128 on one connection across two 64-subscriber jobs; the 129th, targeting a third otherwise-admissible job, declines feedback without evicting earlier records. A daemon's own 129th wake may be uncorrelated and cannot later clear via feedback. Reach 2048 relay-wide records with 32 jobs × 64 subscriptions distributed across enough connections; a 2049th is refused feedback. Keep queued records counted after cache eviction to isolate the global overflow probe on an otherwise-admissible new job, without raising the 32-job cap. Counts never exceed any cap; accepted subscriptions remain eligible. |
| C7 — compatibility and syntax bounds | Exercise every compatibility-table row. IDs of 1 and 64 allowed ASCII bytes opt in; empty, 65-byte, non-ASCII, disallowed-character, null and wrong-typed IDs produce zero feedback while an otherwise-valid wake follows ordinary admission. Unknown fields preserve compatibility. A result contains only the two specified fields and no forbidden routing/content/credential fields. |
| C8 — eviction and late completion | Complete before t=10 and request at the exact t=10 boundary: cache is gone and ordinary admission may start a new job. Keep another job active past t=10: duplicates still coalesce without resetting its 45-second deadline. On later invalid completion, original eligible subscribers may receive bounded results, with no renewed completed window. Restart/eviction never replays results. |
| C9 — persistence and registration interleavings | Hold comparison against registration updates and concurrent pair/revoke writes with barriers. Only the matching snapshot clears; replacement and revoke/re-pair with a reused name survive. Compare-and-clear has no find-then-update gap. Check pairing membership and unrelated fields after disk reload. Lock failure changes nothing; reconcile/save failures obey the documented persistence postconditions and expose no secret/provider text. A failed wake submission leaves no actionable correlation. |
| C10 — slow writes, cleanup and hygiene | Hold result delivery, including writer contention, while ordinary request/frame reads continue. Delivery terminates within 5 seconds or earlier expiry/disconnect/shutdown. Queued/seen records remain counted and cannot evade the caps; cancellation/Close joins delivery and releases state. Embed distinctive synthetic secrets/provider text in inputs and inspect results, errors, captured logs and metrics: none contains forbidden values. |

### Proposed canonical text: publication and activation

This contract is published by [Core #3323](https://github.com/pyrycode/pyrycode/issues/3323).
Feedback delivery is [relay #213](https://github.com/pyrycode/pyrycode-relay/issues/213);
safe daemon consumption is [Core #3423](https://github.com/pyrycode/pyrycode/issues/3423).
**Protocol publication does not activate feedback or token removal.** Both
companions wait for the canonical publication, then own their ordinary code,
review and hermetic tests; they are joint activation prerequisites rather than
reciprocal implementation blockers.

Coordinated operator activation requires the published protocol revision,
completed compatible relay/consumer revisions and recorded evidence for new/new,
mixed versions, matching/stale registrations, disconnect/replacement/reconnect and
expiry conformance. Keep activation off until these prerequisites have evidence.
Deployment and real-daemon checks are operator-owned. Publication and hermetic
tests alone MUST NOT be described as proof of production activation. The
documentation stage runs the applicable documentation gate on the canonical edit.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No unresolved findings. `StartBinaryForwarder` receives untrusted outer data; optional malformed correlation cannot change wake admission. Consumer results remain advisory and require a live local binding, valid code and atomic snapshot comparison before any mutation. Unknown-field tolerance does not excuse malformed required fields.
- [Tokens, secrets, credentials] No unresolved findings. IDs are token-independent, not authentication credentials. Raw tokens remain only in the existing wake request/local registration binding; results and feedback diagnostics exclude tokens, hashes, OAuth data, provider text and inner content. C7/C10 specify literal-wire and distinctive-secret assertions.
- [File operations] OUT OF SCOPE: Core #3423 implements persistence through `devices.WithLock`, `Reload` and `Save`, preserving existing 0600 atomic-file/0700 parent creation semantics. No correlation becomes a path. C9 requires preservation of replacement registrations, membership and unrelated fields across reconcile/save failures.
- [Subprocesses] No findings. The protocol introduces no child processes, shell execution or externally selected executables; conformance uses hermetic peers/provider replies.
- [Cryptography] No findings. No key, nonce, secret comparison or cryptographic primitive is added. WSS protects the outer leg; feedback supplies no Noise authentication of the provider report. Fresh correlation identity is a correctness rule, not a new authorization mechanism.
- [Network and I/O] OUT OF SCOPE: relay #213 must implement the 5-second cancellable delivery budget including writer wait; merely calling the existing 10-second `WSConn.Send` cannot satisfy it. Existing socket read caps remain in place (`NewWSConn` sets its supplied limit before reads; daemon transport caps messages at 1 MiB). Added fields are bounded to 64/13 bytes, retained records to 128/64/2048, and lifetimes to 60 seconds without changing provider limits.
- [Errors, logs, telemetry] No unresolved findings. Fixed `invalid_token` is the only emitted code; success/transient/authentication/admission failures cannot be promoted to permanent invalidity. No provider body or reflected decode error belongs in feedback diagnostics; C4/C5/C10 specify the negative proofs.
- [Concurrency] OUT OF SCOPE: Core #3423 implements connection-generation fencing and compare-and-clear under the registry mutation lock, inside the cross-process persistence region. Relay #213 captures original connections, keeps queued records charged after cache eviction and cancels/joins delivery. C1–C3/C6/C8–C10 cover reuse and shutdown rather than trusting the persistent `Frames` channel or a find-then-update sequence.
- [Threat model] No unresolved findings. Server-id replacement (threat 2), malicious relay/metadata exposure (3), token exposure (4), invalid-input bugs (5), replay (6) and DoS (7) have explicit routing, redaction, inert-result and resource rules. A malicious relay can still falsely invalidate a matching token: this denial of push service is named as residual risk. Prompt injection (1), Noise/static-key compromise (8), device-token ownership proof and provider attestation remain outside this additive content-blind contract; neither companion claims to solve them.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15
