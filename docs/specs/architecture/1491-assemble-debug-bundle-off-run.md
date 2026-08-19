# #1491 — assemble the debug bundle off the `Run` goroutine

**Ticket:** [#1491](https://github.com/pyrycode/pyrycode/issues/1491) · **Size:** S · `bug`, `security-sensitive`

## Files to read first

Symbols, not line numbers — resolve each with `codegraph_search` / `codegraph_node`, then Read.

| Where | Symbol | What to extract |
|---|---|---|
| `internal/relay/v2session_debugbundle.go` | `handleDebugBundleRequest` | The function being restructured. Note the branch order: nil-seam → gate → assemble → stream → log. |
| `internal/relay/v2session_debugbundle.go` | `bundleInFlight` | The queue-derived half of the gate. Its doc comment states the TOCTOU argument that must survive this change **unchanged**. |
| `internal/relay/v2session_debugbundle.go` | `debugBundleReplyError`, `msgDebugBundleUnavailable` | The deterministic reply. It stays on `Run` and stays byte-identical — do not add a second reply path. |
| `internal/relay/v2session.go` | `V2SessionManager.Run` | The select loop you add one arm to. Copy the arm shape from the `m.modalTimeout` / `m.pushOverflow` arms. |
| `internal/relay/v2session.go` | `appFrameWorker`, `routeAppFrame`, `forwardToRun`, `forwardAppReply`, `appReplyMsg` | **The precedent (#965).** Work moves off `Run`; the seal comes back. Mirror this posture, not a new one. |
| `internal/relay/v2session.go` | `handleWake`, `wakeSignal` | The pattern for carrying a `*V2Session` across a goroutine boundary and dereferencing it **only** on `Run`, behind a `V2StateOpen` check. `handleBundleReady` is this shape. |
| `internal/relay/v2session.go` | `handleModalTimeout`, `handlePushOverflow` | Naming + arm shape for a `Run` handler fed by an off-`Run` producer. |
| `internal/relay/v2session.go` | `closeWith` | Sets `V2StateClosed`, closes `s.done`, deletes from `m.sessions` **and** `m.queues`. This is why a per-session marker cannot leak. |
| `internal/relay/v2session.go` | `V2Session.done`, `V2Session.appFrames` | The precedent for a session field an off-`Run` goroutine may read (created on `Run`, closed once, never reassigned). |
| `internal/relay/v2session.go` | `wakeBufferSize` | The buffer size every off-`Run`→`Run` signal channel in this package uses. |
| `internal/relay/v2bundlestream.go` | `StreamBundle`, `bundleEnvelopes` | Enqueue-only, returns on enqueue not delivery. Unchanged by this ticket, and stays on `Run`. |
| `internal/relay/v2session_seams.go` | `V2SessionConfig.DebugBundler` | Seam signature `func() ([]byte, error)` — **no `ctx`**. Drives the § Open questions residual. |
| `internal/relay/v2session_modal.go` | `pushQueue.enqueue` | The two soft-overflow comments that credit the queue-derived gate. Their claim must stay true. |
| `internal/relay/v2session_debugbundle_test.go` | `bundleGatedManagerFor`, `requestBundle`, `assertBusyReject`, `waitQueueLen`, `waitCallCount`, `fakeBundler` | Every fixture the new tests need already exists. Do not build a new harness. |
| `internal/relay/v2session_debugbundle_test.go` | `TestV2Session_DebugBundle_ErrorReplies` | **The one existing test this change breaks.** Its barrier assumes a synchronous reply — see § Testing strategy. |
| `internal/relay/v2session_appframe_test.go` | `TestV2Session_SlowHandler_DoesNotStallOtherConn`, `TestV2Session_SlowHandler_ModalTimeoutStillFires`, `blockingHandler` | **The template for AC #1's test.** Copy the structure; swap the blocked handler for a blocked `DebugBundler`. |
| `internal/relay/v2session_test.go` | `queueLen`, `assertQueueDrains`, `waitForLogContains` | Shared polls the new tests reuse. |
| `docs/knowledge/features/v2-session-manager.md` | § "Inbound debug-bundle request (#813)" and the #965 paragraph asserting control arms stay on `Run` because they are "fast" | The doc claims this ticket falsifies — for the documentation phase, not the developer. |

## Context

`handleDebugBundleRequest` calls the `DebugBundler` seam inline on `V2SessionManager.Run`. In production that seam is a closure over `debugbundle.Assemble`, which reads the newest `.cast` in full and gzips the archive in memory. For that whole duration `Run` sits inside one select arm and services nothing else: another conn's application frame, the `m.drainCh` push drain, `m.wake` rekey/idle timers, `m.modalTimeout`.

This is the follow-up #813 named and deferred. **Its trip-wire has not fired** — no stall has been observed in production; the ticket comes from the 2026-08-16 static review, and promoting it to Backlog was the human's call to spend the turns anyway (Evidence-Based Fix Selection: recorded, overridden deliberately, not silently).

#965 already did this exact move for application frames and is the constraint as much as the precedent: handlers went off `Run` to a per-conn worker, but **every `s.send.Encrypt` stayed on `Run`**, with replies funnelled back via `m.appReply`. The design below is the same shape applied to one control verb, and deliberately introduces no new concurrency idiom.

Two traps the ticket names, and how the design answers them:

**Trap 1 — the gate goes blind during assembly.** `bundleInFlight` is *derived from the push queue*: it reads true only once `StreamBundle` has enqueued frames. Move assembly off `Run` naively and the window between the gate check and the first `Push` is unguarded — a second `request_debug_bundle` sees an empty queue, passes, and starts a second concurrent assembly. That restores #911's per-retry growth *and* adds concurrent full-size archives in memory. Answered by § The gate's two halves.

**Trap 2 — the failure reply cannot be sealed off `Run`.** `debugBundleReplyError` goes through `forwardEnvelope`, which reads the `Run`-owned `m.sessions` and calls `s.send.Encrypt`. Answered by never attempting a failure reply off `Run` at all — see § Design.

## Design

**Only the seam call moves.** The off-`Run` goroutine calls `m.cfg.DebugBundler()` and does nothing else — it funnels the `(archive, err)` result back to `Run`, which then performs every existing step (`StreamBundle`, the error reply, both log lines) exactly where they run today.

```
Run ── handleDebugBundleRequest ──┬── nil seam        → debugBundleReplyError  (on Run, unchanged)
                                  ├── gate busy       → debugBundleReplyError  (on Run, unchanged)
                                  └── accept: s.bundleAssembling = true
                                              go assembleBundle(ctx, s, env.ID)
                                                        │
                                            OFF RUN ────┤  archive, err := m.cfg.DebugBundler()
                                                        │  (touches nothing else)
                                                        ▼
                                              m.bundleReady <- bundleResult{...}
                                                        │
Run ── handleBundleReady ─────────────────────────────── ┤
       defer s.bundleAssembling = false                  │
       ├── s.state != V2StateOpen → drop (debug log)
       ├── res.err != nil         → warn(event only) + debugBundleReplyError
       └── StreamBundle(ctx, s.connID, res.archive) + the "served" info log
```

This is why the design is small: the failure reply, the success stream, the seal, and both log calls are **untouched code in an untouched place**. Nothing new can leak, drop, or reorder, because nothing new sends.

### New state

Four additions, all unexported.

**`V2Session.bundleAssembling bool`** — the explicit marker covering `[accept → enqueue complete]`. `Run`-owned in the strongest sense: written **only** in `handleDebugBundleRequest` (set) and `handleBundleReady` (clear), both on `Run`. The off-`Run` goroutine never touches it. Same single-writer regime as `state` / `replayThrough` — **no lock, no atomic, and `pushMu` is not involved**, so `pushMu`'s "taken alone, never held across an `Encrypt`, `m.send`, or any channel op" invariant is untouched by construction rather than by discipline.

Living on `V2Session` (not a manager-level map) is load-bearing for two properties, not a stylistic choice:
- **No lockout after teardown.** `closeWith` deletes the session; a reconnecting `conn_id` gets a *fresh* `V2Session` whose marker is the zero value. There is no cleanup to forget.
- **No leak.** The marker is reclaimed with the session. A `map[string]bool` on the manager would need a delete in `closeWith` and would grow on any path that misses it.

**`V2SessionManager.bundleReady chan bundleResult`** — buffered at `wakeBufferSize`, matching `wake` / `modalTimeout` / `pushOverflow`. Initialised in `NewV2SessionManager` alongside them. Not closed on `Run` exit; in-flight producers unblock via `ctx` / `s.done`, exactly as `manualRekey` and `snapshot` document.

**`bundleResult`** — the value carried back. Lives in `v2session_debugbundle.go` beside the verb's other machinery (the #1025 carve-out keeps the verb together; the *field* must be on the manager struct in `v2session.go`, so the two are split — note the cross-reference in both comments).

```go
type bundleResult struct {
    s         *V2Session // carried, never dereferenced off Run — same contract as wakeSignal.s
    inReplyTo uint64     // the rejected/served request's env.ID, for the reply correlation
    archive   []byte
    err       error      // selects the reply branch ONLY — never logged, never sent
}
```

The `err` field comment must state the ban explicitly. It is the one new place an assembly error string could reach a log line if a future edit is careless; the deterministic suspenders is `TestV2Session_DebugBundle_ErrorReplies`, which greps the captured log buffer for the error text (different fabric: a comment is advisory, the test is a build gate).

**`Run` arm** — one case, mirroring the `m.modalTimeout` arm:

```go
case res := <-m.bundleReady:
    m.handleBundleReady(runCtx, res)
```

### New functions

**`assembleBundle(ctx context.Context, s *V2Session, inReplyTo uint64)`** — the goroutine body. Calls the seam, then funnels the result:

- Blocking send on `m.bundleReady`, with `<-ctx.Done()` and `<-s.done` as the escape arms.
- **The send must block, not drop.** A `drainCh`-style non-blocking send would discard the result and leave `bundleAssembling` set forever — the permanent lockout AC #2 forbids. This is `armIdleTimer`'s reasoning, not `Push`'s: this goroutine exists only to carry one result, so blocking it costs nothing that matters.
- Reads exactly two things: `m.cfg.DebugBundler` (immutable after `NewV2SessionManager`; `Push` already reads `m.cfg.Logger` off-`Run`) and `s.done` (created on `Run` at open, closed once, never reassigned — the field `appFrameWorker` already reads off-`Run`, classified by the #965 doc as a safe unsynchronized channel op). It dereferences **no** `Run`-owned session field.

**`handleBundleReady(ctx context.Context, res bundleResult)`** — the `Run` arm handler. Clears the marker via `defer` (so every early return clears it), then: drop if `res.s.state != V2StateOpen`; on `res.err != nil` warn the event and call `debugBundleReplyError`; otherwise `StreamBundle` + the "served" info log. Behaviour asserted by `TestV2Session_DebugBundle_ServedAfterFailedAssembly` and the amended `TestV2Session_DebugBundle_ErrorReplies`.

The **deferred** clear is the mechanism, not a style choice: the ticket's "the failure path must clear it too, or the conn is locked out permanently" becomes structural rather than a branch a future edit can miss.

`handleBundleReady` reads `res.s.state` and `res.s.connID` — both on `Run`, both fine. It does **not** consult `m.sessions`: a torn-down session is already `V2StateClosed`, and a reconnected same-`conn_id` session is a *different* pointer whose marker is false, so the stale result is dropped by the state check alone.

### The gate's two halves compose gaplessly

```
request accepted ──────────────────────────────────────────► drained
│                                                                  │
├── s.bundleAssembling ────────────────────┤                       │
│   set on Run at accept                   cleared on Run after    │
│                                          StreamBundle returns    │
│                          ├── bundleInFlight (queue scan) ────────┤
│                          true from the first Push
```

The overlap is real, not adjacent: `handleBundleReady` calls `StreamBundle` (which enqueues every frame) **before** the deferred clear runs, both inside one `Run` pass. No other `Run` arm can interleave between them, so there is no instant at which both halves read false while a bundle is live.

The check becomes:

```go
if s.bundleAssembling || m.bundleInFlight(s.connID) {
    m.debugBundleReplyError(ctx, s, env.ID)
    return
}
```

Placement is unchanged — still **before** the seam is reached, so a rejected retry still skips assembly entirely, not merely the enqueue. `bundleInFlight` itself is not modified; its TOCTOU argument ("the only concurrent mutator is an off-`Run` `Push`, which can only append") still holds, because the new goroutine performs no `Push`.

Per-conn isolation is preserved: `bundleAssembling` is a per-session field and `bundleInFlight` scans one conn's queue, so conn A's assembly cannot reject conn B (`TestV2Session_DebugBundle_PerConnIsolation`).

### What does not change

- **No wire-format change.** Same frames, same order, same `InReplyTo`.
- **`StreamBundle` stays on `Run`.** It operates on the *compressed* archive and is dominated by the read+gzip this ticket moves; splitting it too would buy little and would put a `Push` loop off-`Run` for no AC.
- **`debugBundleReplyError` is untouched** and remains the only reply path for this verb. There is deliberately **no** `Push`-based error reply — see § Alternatives considered.
- **`bundleInFlight` is untouched.**
- **The seam signature is untouched.**

## Concurrency model

| Goroutine | Spawned by | Touches | Exits on |
|---|---|---|---|
| `Run` | caller | everything `Run`-owned | `Frames` close / `ctx` |
| `assembleBundle` | `handleDebugBundleRequest`, one per accepted request | `m.cfg.DebugBundler`, `m.bundleReady`, `s.done` | successful send, `runCtx` cancel, or `s.done` close |

**Count.** At most one `assembleBundle` per conn at a time — that is precisely what the marker enforces. Not "bounded in practice": bounded by the same guard the ticket requires for memory.

**AC #4, stated as invariants:**
- No off-`Run` path reads or writes `s.send`, `s.recv`, `s.state`, `m.sessions`, `m.queues`, or any timer field. `assembleBundle`'s body reaches two identifiers, listed above.
- No off-`Run` path reads or writes `bundleAssembling`. It is `Run`-owned, single-writer, lock-free — and that is *why* no lock is needed, not an excuse for omitting one.
- `pushMu` gains no new acquisition site and no new nesting. It remains a leaf lock taken alone.
- No `WaitGroup`, no join — matching #965's residual and the timer-callback posture.

**Teardown, all three ways:**
- *`Run` exits* — `runCtx` cancels, the goroutine's send escapes via `ctx.Done()`. The manager is dead; the marker is irrelevant.
- *Conn tears down mid-assembly* — `closeWith` closes `s.done`; the goroutine escapes there. The marker is never cleared but dies with the session, and a reconnect gets a fresh one. No lockout.
- *Conn tears down after the send but before `Run` reads it* — `handleBundleReady` sees `V2StateClosed` and drops. No seal under a dead session, no burned nonce.

**Ordering.** A bundle's frames may now interleave differently with other frames on the same conn than before, because the accept and the stream are two `Run` passes rather than one. This is safe and is the point: `StreamBundle` still enqueues all chunks plus the `done` marker in one atomic-with-respect-to-`Run` call, so the *bundle's own* frames stay contiguous and ordered, which is the only property `ReassembleBundle` requires.

## Error handling

| Failure | Path | Result |
|---|---|---|
| `DebugBundler == nil` | on `Run`, before spawn | one `debugBundleReplyError`; no goroutine, no marker |
| Gate busy (assembling **or** queued) | on `Run`, before spawn | one `debugBundleReplyError`; seam not invoked |
| Assembly returns an error | off `Run` → `handleBundleReady` | warn the **event only** (`event`, `conn_id` — never `res.err`), then one `debugBundleReplyError` sealed on `Run`; marker cleared → next request served |
| Session closed during assembly | `handleBundleReady` | dropped at the `V2StateOpen` check, debug log, no reply |
| `StreamBundle` returns an error | `handleBundleReady`, unchanged | existing debug log, dropped; marker cleared → next request served |
| Queue trips `pushQueueByteCeiling` | unchanged (#1505) | `overflowed` latches, `handlePushOverflow` tears the conn down |
| Seam never returns | goroutine parks in the seam | one parked goroutine for that conn; the marker keeps it at one. See § Open questions. |

Every reply on every path is still the same fixed triple — `protocol.CodeServerBinaryOffline`, `msgDebugBundleUnavailable`, `retryable: true` — correlated on `InReplyTo`. Exactly one reply per request, because each request produces at most one `bundleResult` and each `bundleResult` takes exactly one branch.

## Testing strategy

Scenarios, not code. All new tests live in `internal/relay/v2session_debugbundle_test.go` (its fixtures are already there) and run under `-race`.

**New fixture — `blockingBundler`.** A `DebugBundler` double mirroring `blockingHandler`: signals on an `entered` channel, blocks on a `release` channel, then returns an injectable `(archive, err)`. The seam takes no `ctx`, so **`release` must be closed from `t.Cleanup`** or the goroutine outlives the test — call this out in the fixture's own comment.

**`TestV2Session_DebugBundle_AssemblyDoesNotStallRun`** (AC #1) — the load-bearing new test. Structure it on `TestV2Session_SlowHandler_DoesNotStallOtherConn` and `..._ModalTimeoutStillFires`.
- Conn A requests a bundle; wait on `entered` so the assembly is provably in flight.
- With A's assembly blocked, assert all three arms are serviced: conn B's application frame gets a sealed reply (register `prolificHandler` on `protocol.TypeListConversations`); a `Push` to B reaches the wire (the drain arm advances); and an `ArmModalTimeout` safe-deny fires and delivers `modal_dismissed` (the timer arm) — reuse `fakeModalResolver` and shrink `modalDenyTimeout` as `..._ModalTimeoutStillFires` does, which means **no `t.Parallel()`** for that assertion.
- Release; assert A's bundle then streams and round-trips through `ReassembleBundle`.
- **Must fail on the pre-fix tree**: with the seam on `Run`, none of the three arms advance and every wait times out. The developer should confirm this by running the test against a stashed tree, or via `go test -overlay` with the pre-fix `handleDebugBundleRequest` — a green-on-both test proves nothing here.

**`TestV2Session_DebugBundle_RejectsSecondWhileAssembling`** (AC #2, the marker half — the trap the queue-derived gate alone cannot catch).
- Block the bundler; request #1 → wait on `entered`.
- Request #2 **while the queue is still empty** — assert `assertBusyReject` on the reply, and assert the bundler call count stays 1 (the marker, not the queue, is what rejected it).
- Assert `queueLen == 0` at rejection time, so the test is non-vacuous: it fails if a developer relies on `bundleInFlight` alone.
- Release; assert exactly one bundle streams.

**`TestV2Session_DebugBundle_ServedAfterFailedAssembly`** (AC #2 tail + AC #3).
- Bundler returns an error → assert exactly one deterministic reply, correlated on `InReplyTo`, carrying only `msgDebugBundleUnavailable`, with the error text absent from both wire and log buffer.
- Then flip the bundler to succeed and request again → served with a fresh assembly and a full stream. This is the anti-lockout assertion; it fails if the deferred clear is missed on the error branch.

**One existing test must be amended — `TestV2Session_DebugBundle_ErrorReplies`.** Its barrier comment ("the error reply is forwarded synchronously (`forwardEnvelope`), so once a later paired conn opens the reply is already recorded") becomes false for the *assembly-error* subtest, whose reply now arrives one `Run` pass later. Replace the bare `noiseMsgsForConn` snapshot with a poll for ≥ 1 reply, then keep the barrier conn afterwards and assert the count is still exactly 1. Update the stale comment. The nil-bundler subtest is unaffected but shares the body, so both go through the poll. This test is **not** on the ticket's stay-green-unmodified list — amending it is expected, not a scope violation.

**Stay green unmodified** — do not touch these; if one turns red the design is wrong, not the test:
`TestV2Session_DebugBundle_RejectsSecondWhileQueued`, `..._ServedAfterDrain`, `..._PerConnIsolation` (AC #2), `..._NeverLogsContentEncryptedOnly` (AC #5), plus `..._CaptureOn`, `..._CaptureOff`, `..._UnpairedRefused`, `..._JustUnderCeilingStreams`, `..._PastCeilingTearsDownConn`. All of them already synchronise by polling (`waitNoiseMsgs`, `waitQueueLen`, `waitCallCount`, `waitForLogContains`), which is why the extra hop is invisible to them.

**Gate:** `make check`. This package is not behind a build tag, but note that `internal/e2e` is (`e2e` tag) — `make check`'s target flags cover it; a bare `go test ./internal/e2e/` runs zero tests.

## Alternatives considered

**(a) Assemble *and* `StreamBundle` off `Run`, with the failure reply via `Push`** — the ticket's filed fix direction. Rejected on three counts. It needs a second reply mechanism for one verb (`Push` alongside `forwardEnvelope`), so "exactly one deterministic reply" stops being structural. It forces the marker into a manager-level map guarded by `pushMu`, since the clear would happen off `Run` — putting weight on the invariant the ticket explicitly warns about. And it changes the reply's latency and queue position, which is what breaks `TestV2Session_DebugBundle_ErrorReplies` more deeply than the one-line amendment above. Its only gain — `StreamBundle` off `Run` — is work on the *compressed* archive, far below the read+gzip this ticket targets.

**(b) Route the verb onto the existing `appFrameWorker`** — the ticket's second shape. Rejected: `s.appFrames` is a `chan []byte` and the worker hands plaintext straight to `dispatch.Route`, which has no handler for `request_debug_bundle`, so the frame would take the unknown-verb error path. Making it work means either widening the channel's element type (touching `dispatchAppFrame`, `appFrameWorker`, `closeWith`) or re-probing the JSON in the worker — more machinery than the chosen shape, not less, and it serialises assembly behind the conn's unrelated app frames.

**The chosen shape (c)** is (a) minus the parts that create new sending paths: it is the narrowest change that satisfies AC #1, and it satisfies AC #3 by *not moving the reply at all*.

## Doc claims this invalidates

For the documentation phase — **not** developer ACs, and not to be written during implementation:

- `docs/knowledge/features/v2-session-manager.md`, the #965 paragraph asserting `dispatchAppFrame`'s control arms "stay on `Run` — fast". False for `debug_bundle` as of this ticket.
- The same file's § "Inbound debug-bundle request (#813)" narrative and its §-summary clause describing the #911 gate as a `bundleInFlight` scan — the gate is now two halves.
- The two `pushQueue.enqueue` soft-overflow comments in `internal/relay/v2session_modal.go` that pin the bound to a queue-derived gate. The bound survives; its mechanism gained a second half.
- `docs/specs/architecture/813-serve-debug-bundle-to-paired-client.md`'s deferred follow-up is now discharged.

## Open questions

1. **A seam that never returns parks one goroutine per conn.** `DebugBundler` is `func() ([]byte, error)` — no `ctx`, so a hung `debugbundle.Assemble` cannot be cancelled. The marker bounds this to one goroutine per conn, and the production seam always returns (bounded `io.CopyN` + in-memory gzip). Adding `ctx` to the seam would touch `cmd/pyry/relay.go`, `cmd/pyry/debug_bundle_fake.go`, and every test double — out of scope here. Named as an accepted residual; file a follow-up only if a hang is ever observed.
2. **`bundleReady` capacity.** `wakeBufferSize` (16) is the house default and is generous: it is only pressured by >16 conns finishing assembly simultaneously while `Run` is busy, and the blocking send with `ctx`/`s.done` escapes makes that case correct rather than lossy. Not worth a bespoke constant.
3. **`bundleResult` placement.** It lives in `v2session_debugbundle.go` (with the verb) while its channel field lives on the manager struct in `v2session.go` — the split the #1025 carve-out forces. Cross-reference it from both comments; if a reviewer prefers it beside `appReplyMsg` / `wakeSignal`, that is a free move.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The trust boundary is unmoved: it is the AEAD decrypt in `handleNoiseMsg`, upstream of everything here. `request_debug_bundle` is a **bare** frame — no `conversation_id`, no path, no id — so there is no attacker-controlled field to reach assembly, and the bundle is daemon-global. The only new value crossing a goroutine boundary is `env.ID`, an already-validated `uint64` used solely as `InReplyTo`, and it travels *away* from the untrusted side. Authorization (pairing at the Noise handshake) is checked in the same place, on `Run`, before any of this runs.
- **[Tokens, secrets, credentials]** No findings — no token is read, minted, stored, or compared. `s.peerStatic` and the device token are not touched.
- **[File operations]** No findings *in this spec* — it moves the *call* to `debugbundle.Assemble`, not its body. Path handling, the recordings-dir scan, and the `stat`-then-`CopyN` read are unchanged and remain #811's surface. Worth naming: the archive size stays uncapped, which is this ticket's *premise*, not a regression — `pushQueueByteCeiling` (#1505) is what bounds the downstream consequence, and it is untouched.
- **[Subprocess]** Not applicable — no `exec`, no argv construction, no environment read.
- **[Cryptographic primitives]** No findings, and this is the category the design is built around. Every `s.send.Encrypt` stays on `Run`: the seal for the success stream (`drainOnce` → `forwardEnvelope`) and for the failure reply (`debugBundleReplyError` → `forwardEnvelope`) both run exactly where they run today. The new goroutine performs no cryptographic operation and holds no reference to a `CipherState`. **This is the nonce-reuse hazard the ticket is closest to, and the design answers it by not moving the seal at all** — a concurrent `Encrypt` would reuse a nonce and corrupt the send counter, so any variant that seals off `Run` is a MUST FIX. Confirmed absent here.
- **[Network & I/O]** No findings. No new socket, no new read, no new timeout surface. Frame sizes stay bounded by `bundleChunkBytes` and `TestStreamBundle_EveryFrameWithinCap`.
- **[Error messages, logs, telemetry]** One **SHOULD FIX**, addressed in-spec. `bundleResult.err` is a new place an assembly error — which can quote a recording path or filename — could reach a log line. The spec mandates: the field comment states the ban; `handleBundleReady`'s warn logs `event` + `conn_id` only, never `res.err`; and the reply carries only the static `msgDebugBundleUnavailable`. The deterministic backstop is `TestV2Session_DebugBundle_ErrorReplies`, which asserts the error text is absent from the captured log buffer, and `TestV2Session_DebugBundle_NeverLogsContentEncryptedOnly` (stay-green, AC #5) covers the success path's content-free `conn_id` + byte count. Code-review must check the warn call's field list explicitly.
- **[Concurrency]** No findings, walked adversarially:
  - *Lock ordering* — no new lock, and `pushMu` gains no acquisition site. The marker is `Run`-owned rather than lock-guarded, so `pushMu`'s "taken alone, never held across an `Encrypt`/`m.send`/channel op" invariant cannot be broken by this change.
  - *TOCTOU on the gate* — the check-and-set is a single `Run` pass with no channel op between them, so no second request can interleave. The two halves overlap (§ The gate's two halves), so the blind window Trap 1 names does not exist. **This is the finding that would have been MUST FIX in the naive design**, and it is the reason the marker exists.
  - *Use-after-teardown* — `handleBundleReady` gates on `V2StateOpen` before touching `res.s`, so a result arriving for a closed session is dropped, not sealed. A reconnected same-`conn_id` conn is a different pointer with a false marker, so a stale result cannot be mistaken for its request.
  - *Goroutine lifecycle* — one goroutine per accepted request, capped at one per conn by the marker, exiting on send / `runCtx` / `s.done`. Verified under `-race` per AC #4.
  - *Lockout as a denial-of-service* — a marker stuck set would permanently deny that conn its own debug bundle. Made structural by the `defer`ed clear plus per-session ownership (a teardown discards the marker with the session); asserted by `TestV2Session_DebugBundle_ServedAfterFailedAssembly`.
- **[Threat model alignment]** Aligned with `docs/protocol-mobile.md` § Security model on the two relevant threats. *Resource exhaustion by a paired phone*: unchanged — one bundle in flight per conn (now enforced across the assembly window too, which is strictly tighter than before), and `pushQueueByteCeiling` still bounds the queue. *Plaintext bundle exposure*: unchanged — the archive travels only over the AEAD-sealed push path and never reaches a log. A hostile paired phone gains no new capability from this change; the surface it can drive is identical, and the concurrent-assembly amplification a naive fix would have opened is closed by the marker. Out of scope, unchanged from #813: an unpaired peer cannot reach this verb at all (handshake gate), and a paired-but-hostile phone requesting its own bundle is the accepted #1505 residual.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
