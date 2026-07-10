# Spec #911 — Bound `request_debug_bundle` to one in-flight bundle per conn

**Ticket:** [#911](https://github.com/pyrycode/pyrycode/issues/911)
**Size:** S (confirmed — 1 production file, additive gate + helper + comment fix)
**Labels:** `security-sensitive` (security review appended at end)

## Files to read first

Read these before writing code. Line ranges are the current state; the change is small and additive.

- `internal/relay/v2session.go:2028-2066` — **`handleDebugBundleRequest`**, the method to modify. The gate goes in here, after the `DebugBundler == nil` check and **before** `m.cfg.DebugBundler()`.
- `internal/relay/v2session.go:2068-2100` — **`debugBundleReplyError`**, the deterministic error reply to reuse verbatim for the busy-reject (fixed `server.binary_offline` + `msgDebugBundleUnavailable` + `Retryable: true`). Do not add a new wire code or message.
- `internal/relay/v2session.go:144-210` — **`queuedEnv`** + **`pushQueue.enqueue`**. The soft-overflow comment (lines ~178-184, "unreachable in practice … the phone cannot drive control volume") is the AC5 target. `queuedEnv.env.Type` is what the new scan reads.
- `internal/relay/v2session.go:231-238` — **`pushQueueCap`** comment; it also asserts the "unreachable-in-practice all-control saturated case." Correct it in the same edit for consistency (AC5).
- `internal/relay/v2session.go:816-829` — **`pushMu` + `queues`** doc: the invariant the new scan must honour — *"Held only around map lookup + enqueue/pop … NEVER held across an Encrypt, m.send, or any channel op … always taken alone."* A pure read of `q.items` under `pushMu` satisfies this.
- `internal/relay/v2session.go:3189-3218` — **`Push`**: how `StreamBundle`'s frames land in `q.items` (under `pushMu`), and that off-Run producers also call `Push`, which is *why* the scan needs `pushMu`.
- `internal/relay/v2session.go:3235-3326` — **`drainOnce`**: pops one frame per Run pass; the **transport-down HOLD** (`transportDown()`, lines 3225-3249) is the mechanism the reject-on-busy test exploits to keep chunks queued.
- `internal/relay/v2bundlestream.go:71-108` — **`StreamBundle`**: enqueues `ceil(len/48000)` `debug_bundle_chunk` frames + one `debug_bundle_done`, all control-class (never `TypeAssistantDelta`), so the drop policy never evicts them.
- `internal/protocol/codes.go:361-362` — **`TypeDebugBundleChunk`** (`"debug_bundle_chunk"`) and **`TypeDebugBundleDone`** (`"debug_bundle_done"`), the two types the scan matches.
- `internal/relay/v2session_debugbundle_test.go` (whole file) — where the new tests live. Reuse `fakeBundler` (call-count double), `bundleManagerFor`, `waitNoiseMsgs`, `openModalConn`, `sealAppFrameConn`, `decryptAppFrame`, `msgDebugBundleUnavailable`, `TestV2Session_DebugBundle_ErrorReplies` (the error-reply assertion shape to copy).
- `internal/relay/v2session_test.go:4615-4680` — **`gatedRecorder`** (`up.Store(true/false)` toggles `Connected`), **`queueLen(mgr, connID)`**, `assertHeldQueued`, `assertQueueDrains` — the transport-stall + queue-depth harness the busy / recovery tests reuse. Note the manager must be built with `Connected: gated.connected` (see the `TestV2Session_Push_HeldWhileTransportDown_*` setup around line 4695) — `bundleManagerFor` does **not** wire `Connected`, so the new tests need a manager built with the gated recorder + `Connected` (and `Reconnect` if you want an immediate flush).
- `internal/relay/v2bundlestream_test.go:239-320` — `TestStreamBundle_MultiChunk` shows `patternBlob(3*bundleChunkBytes)` producing a deterministic multi-chunk (3 + done = 4-frame) blob; feed that as the `fakeBundler.archive` so the queued bundle is visibly multi-frame.

## Context

`request_debug_bundle` (#813) is answered by `handleDebugBundleRequest` → `StreamBundle` → `Push`, which enqueues `ceil(bundle/48000)` `debug_bundle_chunk` envelopes plus one `debug_bundle_done` onto the conn's per-session `pushQueue`. Every bundle frame is **control-class** (not `TypeAssistantDelta`), so `pushQueue.enqueue`'s drop policy never evicts them and admits them past `pushQueueCap` (256) as documented "soft overflow."

That soft overflow was justified by a comment asserting the all-control saturated state is "unreachable in practice … the phone cannot drive control volume (push is server→phone only)." **`StreamBundle` (#812) falsifies that premise**: one small inbound `request_debug_bundle` frame drives an arbitrarily large batch of never-droppable control frames.

**Failure mode.** The relay leg is up but slow (each drained frame bounded by the 10 s `WriteTimeout`; `drainOnce` forwards one frame per Run pass). The phone requests a bundle; a multi-megabyte recording yields dozens-to-hundreds of queued chunks. The phone retries because nothing arrived; **each retry stacks another full bundle's chunks** — unbounded per-retry memory growth (~4/3 × archive size per stacked bundle, since each chunk's `Payload` is a fresh base64 `json.Marshal`, not an alias into the archive). A mobile client with a retry loop is the realistic trigger; no hostile actor required.

This is an amplification / resource-exhaustion path on the internet-exposed v2 relay surface — hence `security-sensitive`.

## Design

**Chosen fix: request-time in-flight gate (Technical Notes shape #1), not atomic-drop-at-enqueue (shape #2).**

Rationale for choosing shape #1 over shape #2:

- **Reuse over new machinery.** The handler already runs on the manager's single Run dispatch goroutine and already owns the deterministic reply helper (`debugBundleReplyError`). The gate is a pure read of `q.items` plus a call to that helper — no new queue primitive, no threading a reject signal back up through `StreamBundle` → `Push` → `pushQueue.enqueue`.
- **Shape #2 fights the drop-policy contract.** Atomic drop-and-error at enqueue would have to distinguish "whole bundle can't fit" from the count-based cap, touch the carefully-specified ADR-025 `enqueue` drop semantics, and add a byte/reservation concept the queue doesn't have. Shape #1 leaves `enqueue` untouched.
- **Deterministic, on the owning goroutine.** The gate check and the queue pop (`drainOnce`) both run on Run, so they are serialized — the check is never racing its own conn's drain.

### The gate

Add one unexported method on `*V2SessionManager`:

```
func (m *V2SessionManager) bundleInFlight(connID string) bool
```

Contract (define, don't implement here):
- Locks `pushMu`, looks up `m.queues[connID]`, scans `q.items` for any entry whose `env.Type` is `protocol.TypeDebugBundleChunk` **or** `protocol.TypeDebugBundleDone`, unlocks, returns whether one was found.
- **Both** types matter: `StreamBundle` enqueues all N chunks + the done marker in one handler invocation, and `drainOnce` pops one per pass, so during the drain the queue holds a shrinking suffix that always includes the trailing `debug_bundle_done` until the very last pop. Checking either type covers the whole in-flight window; checking both is the clear, AC-matching form (AC4's recovery is "all chunks **plus** the `debug_bundle_done` marker forwarded").
- Unknown conn (`!ok`) → `false`. An unknown conn has no queue and cannot be bundle-busy; false is the correct and safe direction (it does not admit a second bundle, because a non-open conn's later `StreamBundle`/`Push` returns `ErrConnNotFound` anyway).
- `pushMu` is taken **alone** for a pure `O(len(items))` read (≤ soft-overflow count) and released before any reply — preserving the "never held across an external call" invariant. Same lock-discipline as `enqueue`'s own drop scan and the `queueLen` test helper.

### Gate placement in `handleDebugBundleRequest`

Insert immediately after the existing `if m.cfg.DebugBundler == nil` block and **before** `archive, err := m.cfg.DebugBundler()`:

```
if m.bundleInFlight(s.connID) {
    // A prior bundle's chunks are still queued for this conn; a retry against a
    // slow/stalled transport must not stack a second bundle (unbounded per-retry
    // memory, #911). Reply deterministically and assemble/stream nothing.
    m.debugBundleReplyError(ctx, s, env.ID)
    return
}
```

Placing it before `DebugBundler()` satisfies AC1's "**no second bundle is assembled or enqueued**" — the expensive on-disk assembly is skipped, not just the enqueue.

The reject reuses `debugBundleReplyError` unchanged: `server.binary_offline` / `msgDebugBundleUnavailable` / `Retryable: true`. The retryable semantics are exactly right — the phone should retry *later*, and once the prior bundle drains (AC4) the retry succeeds. **No new wire code, no new message string** (avoids leaking queue-depth or a distinguishable "busy" signal).

### Comment correction (AC5)

Correct two comments that assert the now-false premise:
1. `pushQueue.enqueue`'s soft-overflow paragraph (the "unreachable in practice … the phone cannot drive control volume" sentences).
2. `pushQueueCap`'s "unreachable-in-practice all-control saturated case" sentence.

New wording should state: `StreamBundle` (#812) enqueues control-class `debug_bundle_chunk`/`debug_bundle_done` frames, so the all-control saturated state **is** reachable; `handleDebugBundleRequest`'s per-conn in-flight gate (#911) bounds it to at most one bundle's chunks per conn at a time, so a single bundle may still ride past nominal cap (bounded, ~4/3 × archive) but retries can no longer stack. The soft-overflow branch itself is unchanged.

### What this deliberately does NOT do

- It does **not** cap a single bundle's chunk count. One large bundle can still exceed `pushQueueCap` via the existing soft overflow — that is bounded (one bundle ≈ 4/3 × archive) and out of scope. #911 eliminates *stacking across retries*, not single-bundle overflow.
- It does not change `enqueue`, `Push`, `drainOnce`, or `StreamBundle` behaviour. Only `handleDebugBundleRequest` gains the gate; `bundleInFlight` is new.

## Concurrency model

- `handleDebugBundleRequest` and `drainOnce` both run on the single Run goroutine, so the gate check is serialized against this conn's own pops — no check-then-act race on drain.
- `bundleInFlight` still needs `pushMu`: off-Run producers (the #632 structured-event emitter) call `Push`, which mutates `q.items` under `pushMu` concurrently with Run. The scan is a pure read under the same leaf lock; taken alone, released before the reply.
- No new lock, no new goroutine, no new channel. Lock ordering is unchanged (`pushMu` still orders below nothing).
- The reject reply seals under `s.send` on the Run goroutine via `forwardEnvelope` — identical to the existing nil-bundler / assembly-error reply paths. **No new Noise send-nonce semantics** are introduced.

## Error handling

- Unknown/torn-down conn: `bundleInFlight` returns `false`; the subsequent `StreamBundle`/`forwardEnvelope` fail-closed exactly as today (`ErrConnNotFound` / `ErrSessionNotOpen`, dropped at debug).
- Reject path: reuses `debugBundleReplyError`, whose own `forwardEnvelope` failure is already logged at debug and dropped. No new failure branch.

## Testing strategy

New tests in `internal/relay/v2session_debugbundle_test.go`. Build the manager with a `gatedRecorder` and `Connected: gated.connected` so the transport can be held down to keep bundle frames queued (`bundleManagerFor` doesn't wire `Connected`; add a small local setup or extend it). Use a multi-chunk `fakeBundler.archive` (e.g. `patternBlob(3*bundleChunkBytes)`) so the queued bundle is visibly > 2 frames. Run under `-race`.

Scenarios (bullet form — developer writes them in the project idiom):

- **Reject on busy — no second assemble, no growth (AC1/AC2).** Open a paired conn; `gated.up.Store(false)`. Fire `request_debug_bundle` #1 → assert `queueLen(mgr, conn)` settles at N+1 (held, using the `assertHeldQueued`-style settle). Fire `request_debug_bundle` #2 → assert (a) `queueLen` is unchanged (no stacking), (b) `fakeBundler.callCount()` is still 1 (assembly skipped), (c) exactly one `TypeError` reply recorded for the conn, `InReplyTo` == req#2's id, `Code == CodeServerBinaryOffline`, `Message == msgDebugBundleUnavailable`, `Retryable == true` (copy the assertions from `TestV2Session_DebugBundle_ErrorReplies`). Fire a #3 while still held → `queueLen` still unchanged, `callCount` still 1.
- **Recovery after drain (AC4).** From the busy state, `gated.up.Store(true)` (and signal `Reconnect` if wired) → `assertQueueDrains` to 0 (all chunks + done forwarded). Fire another `request_debug_bundle` → assert `callCount` increments and the bundle streams again (`waitNoiseMsgs` for the fresh chunk+done). Proves the gate clears exactly when the prior bundle (including the done marker) has fully drained.
- **Per-conn isolation (AC3).** `gated.up.Store(false)`; open two paired conns A and B. Fire a request on A → A's queue holds bundle frames. Fire a request on B → assert B is **served** (its queue receives bundle frames / `callCount` incremented), i.e. B is not rejected because A is busy. Simultaneously assert a repeat request on A **is** rejected. Proves the scan is scoped to the requesting conn's own queue.
- **AC5 comment correction** is not independently testable; it is verified by review of the diff.

## Security review

**Verdict:** PASS

Walked adversarially against this spec, default-FAIL. The change adds one `pushMu`-guarded read (`bundleInFlight`) and one reuse of the existing `debugBundleReplyError` on a new (busy) branch of `handleDebugBundleRequest`. It is a *reduction* in the attack surface it targets, so the audit focused on (a) whether the bound actually holds, (b) whether the gate can be raced open, and (c) whether the reject path leaks or introduces new crypto/nonce behaviour.

**Findings:**

- **[Trust boundaries] No findings.** The untrusted inbound `request_debug_bundle` frame already crosses into trusted state at the existing handshake + `V2StateOpen` + pairing gate, upstream of this code. The new `bundleInFlight` scan reads only server-owned values — `q.items[i].env.Type`, set by `StreamBundle`/internal emitters, never attacker input — and `s.connID` (Run-owned). The only untrusted field consumed is `env.ID`, echoed back as `InReplyTo` exactly as the pre-existing error branches already do. No new boundary, no attacker-controlled value used as a key or path.

- **[Resource exhaustion / Network & I/O] No findings — this is the fix, and the bound holds.** While any `debug_bundle_chunk`/`debug_bundle_done` frame is queued for conn X, every further `request_debug_bundle` on X is rejected before `DebugBundler()` and before any `Push`, so X's queue holds at most one bundle's frames at a time. Recovery is exact: the gate clears only when the trailing `debug_bundle_done` has also been popped (both types scanned). Cross-conn amplification is bounded by the authenticated-open-conn count (each queued bundle requires its own valid paired handshake; the idle sweep caps conn lifetime) — the factor is one bundle per authenticated conn, not unbounded per inbound frame. Residual: a *single* bundle can still soft-overflow past `pushQueueCap` (bounded ≈ 4/3 × archive); documented and out of scope — #911 eliminates per-retry stacking, not single-bundle overflow.

- **[Concurrency] No findings — check-and-act is serialized, and the only concurrent mutator can only make the gate fire.** `bundleInFlight` and `drainOnce` (the sole queue-drainer) both run on the single Run goroutine, so nothing empties this conn's queue between the scan and the reply. The only off-Run mutator is `Push`, which can only *append* frames — it can turn a false into a true (more conservative), never clear a true. So there is no TOCTOU that admits a second bundle. `pushMu` is taken alone for an O(len) read and released before the reply seal, preserving its "never held across an external call" invariant. No new lock, goroutine, or channel; lock ordering unchanged.

- **[Error messages, logs, telemetry] No findings.** The busy-reject reuses `debugBundleReplyError`'s fixed `server.binary_offline` / `"debug bundle unavailable"` / `Retryable: true` — no queue depth, conn-id-on-wire, or archive bytes. `bundleInFlight` logs nothing. The reply is byte-identical to the nil-bundler and assembly-error branches, so the phone cannot distinguish "busy" from "offline"/"assemble-failed" — no oracle, and the only party that could observe it is the paired owner of the conn whose own bundle is draining (not a secret).

- **[Cryptographic primitives] No new nonce/key semantics — inherited property noted OUT OF SCOPE.** The reject reply seals under the existing `s.send` CipherState on the Run goroutine via `forwardEnvelope`, exactly like the two pre-existing error branches of the same handler. `forwardEnvelope` (unlike the async `drainOnce`) does not consult the #874 transport-down hold, so a synchronous reply burns one send nonce even if the relay leg is momentarily down — but this is a *pre-existing, shared* property of every `*ReplyError` helper (`snapshotReplyError`, `settingsReplyError`, `debugBundleReplyError`) and `handleRequestSnapshot`, not introduced here. #911 does not change it and, under the retry-storm it targets, strictly *reduces* frames sealed (one reject reply vs a whole re-streamed bundle). A cross-cutting "hold synchronous reply seals while transport down" hardening, if ever wanted, is a separate ticket spanning all reply helpers — out of scope for #911.

- **[File operations / Subprocess / Tokens] Not applicable.** The busy path skips `DebugBundler()` entirely (the only file-touching step), touches no subprocess, and reads/stores no token or secret. The gate reads *less* secret data than the baseline (it prevents a second assembly of the plaintext bundle).

- **[Threat model alignment] Addressed.** `docs/protocol-mobile.md` § Security model / ADR-025 § Backpressure treat resource exhaustion on the internet-exposed v2 relay as an in-scope threat. This closes the specific "one small untrusted inbound frame → unbounded never-droppable outbound batch" amplification for the debug-bundle path. No other amplification vector is claimed fixed.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-10

Existing debug-bundle tests (`CaptureOn/Off`, `UnpairedRefused`, `NeverLogsContentEncryptedOnly`, `ErrorReplies`) must stay green — the gate only fires when a bundle is already queued, so the single-request happy paths are unaffected (their queue is empty at request time).

## Open questions

- **None blocking.** One note for the developer: `bundleManagerFor` builds the manager without `Connected`, so a nil `Connected` means `drainOnce` never holds and the queue drains immediately — that is fine for the existing single-shot tests but makes the "busy" window non-deterministic. The three new tests therefore need a manager wired with the gated recorder's `Connected` (mirror the `TestV2Session_Push_HeldWhileTransportDown_ReflushContiguous` setup). Decide whether to extend `bundleManagerFor` with an optional `Connected` or add a small dedicated helper; either is fine.
