# Worker isolation and request-fixture tests

[Same-package test overview](v2-session-manager-test-surface-same-package-unit-tests-internal-relay.md) describes the shared harness.

Handler execution offload (#965) — extends `v2session_appframe_test.go`, reusing the #909 harness (`prolificHandler`, `driveToOpen`, `v2Recorder`, `sealAppFrame`, `waitForOutboundCount`, `decryptAppFrame`): `TestV2Session_SlowHandler_DoesNotStallOtherConn` (**AC-1(a)**) drives two open conns on one manager by reusing the #727 `openModalConn` conn-aware handshake helper (no new harness variant needed — the spec's speculative `driveToOpenConn` wasn't required), blocks conn A's handler on a test-controlled channel, and asserts conn B's `list_conversations` reply is sealed + emitted promptly with no dependence on releasing A; `TestV2Session_SlowHandler_PushStillDrains` (**AC-1(b)**, renamed from `…_ModalTimeoutStillFires` by #1539 when its borrowed deny-on-timeout arm was deleted) blocks conn A's handler with `blockingHandler(entered, release)`, waits on `entered` so the handler is provably running, then `mgr.Push`es to the same conn A and asserts the drain arm (`m.drainCh`) still seals and forwards it (A's first `noise_msg` decrypts to `TypeMessage`) — a `Push` sent without waiting on `entered` first can pass vacuously, because the drain can beat `Run` to the blocking frame even under the #965 mutation, so the wait is load-bearing, not cosmetic. This was the drain arm's only coverage in this file; no package var is mutated any more, so the test runs `t.Parallel()`; `TestV2Session_SlowHandler_RekeyStillEmits` (**AC-1**, rekey witness) blocks an app handler and asserts a scheduled/manual rekey still emits; `TestV2Session_AppFrame_PerConnSerialization` (**AC-2**) sends two app frames on one conn and asserts (a) no two handlers for that conn run concurrently and (b) the two sealed replies decrypt via `initRecv` — which decrypts strictly in send-counter order, so a reorder surfaces as an AEAD auth failure — with `in_reply_to` in arrival order; `TestV2Session_AppFrame_QueueOverflow_ClosesConn` fills one conn's `s.appFrames` past `appFrameQueueDepth` behind a permanently-blocked handler and asserts that conn closes at 4421 while an unrelated conn is unaffected; `TestV2Session_OpenState_ProlificHandler_NoDeadlock` / `…_EmissionOrder` (both pre-existing #909 tests) pass unchanged, pinning that `routeAppFrame` preserves the concurrent-Route-drain shape verbatim. AC-3 (every seal stays on `Run`) is pinned by `-race` across all of the above, not a separate fixture — an off-`Run` seal or concurrent CipherState touch fires the race detector and breaks the ordered-decrypt assertions. See [codebase/965.md](../codebase/965.md).

## Pass-through request fixtures

The `mcp_status_request` tests (#2381) expose two proof traps that apply to any
request-keyed pass-through resolver. First, a success fixture whose requested
`conversation_id` equals the resolver result's `ConversationID` stays green if
the handler wrongly overwrites the daemon-authored result with the remote-authored
lookup key; use deliberately distinct values when proving “forwarded unchanged.”
Second, searching logs only for payload sentinels does not prove decoder errors
are absent: Go's type-mismatch text can name the target type without repeating the
array contents, and a success/unknown-only sequence never exercises a poisoned
false resolver result. A complete log proof asserts the decoder-error attribute or
text is absent and drives the unavailable arm with a non-zero poisoned payload.
These are known test-strength gaps, not observed implementation leaks.

The `mcp_reconnect` / `mcp_toggle` tests (#2419, `v2session_mcpactuate_test.go`)
run every scenario against **both** verbs rather than writing one and asserting
the other looks the same by inspection — the two share all four handler helpers,
so a scenario proven on one but not mirrored on the other is exactly the kind of
drift a shared implementation invites. They also carry the #2381 pair of proof
traps forward with a third specific to a comma-ok refusal seam: the accepted
fixture's `ConversationID` deliberately differs from the requested id, the log
proof asserts decoder-error text is absent rather than just scanning for payload
sentinels, and the refusal-arm test drives a **poisoned** `MCPStatusPayload`
back from the seam alongside `ok=false` — a refusal arm returning a zero payload
cannot prove the handler ignores the payload on `false`, only a refusal that
hands back something worth leaking can. The handler-behaviour tests passed on
first run; that was not itself evidence, since the RED before them was
structural (`TestEveryInboundV2TypeHasHandler` reddening for an unclassified
constant, then for a type registered on no dispatch surface) rather than
behavioural. Two mutations confirmed the assertions are not vacuous: ignoring
the seam's `accepted` bool reddens the merged-refusal-code test on both verbs,
and logging the decoder error on the malformed arm reddens the log-absence test
— the second is precisely the gap a sentinel-only log search leaves open, so a
green first run on tests like these is worth mutation-checking rather than
trusting.

## Debug-bundle assembly

Off-`Run` debug-bundle assembly ([#1491](../../specs/architecture/1491-assemble-debug-bundle-off-run.md)) — extends `v2session_debugbundle_test.go`, reusing `bundleManagerFor` / `requestBundle` / `assertBusyReject` / `queueLen` / `waitCallCount` plus a new `blockingBundler` double (mirrors `blockingHandler`: signals on `entered`, blocks on `release`, then returns an injectable `(archive, err)`). `TestV2Session_DebugBundle_AssemblyDoesNotStallRun` is the load-bearing proof for AC #1, built on the `TestV2Session_SlowHandler_*` (#965) template: with one conn's assembly parked mid-flight, asserts a second conn's application frame still gets a sealed reply (the `Frames` arm), a queued `Push` to a third conn still drains (the `drainCh` arm), and `mgr.Rekey` against a fourth conn still returns (the `manualRekey` arm, retargeted from the deleted `modalTimeout` arm by #1539 — `Rekey` blocks on `Run`'s reply, and the fourth conn's `noise_msg` decrypts to `TypeRekeyRequest`) — none of them waiting on the release; on the pre-#1491 tree, with the seam inline, all three time out instead (arm (3) alone fails with a `Rekey` deadline error). `TestV2Session_DebugBundle_RejectsSecondWhileAssembling` pins the accept-side gate half specifically: a second request arriving while the bundler is blocked and the queue is still empty (asserted via `queueLen == 0` and `!mgr.bundleInFlight(connID)`) is rejected with the bundler call count staying at 1 — non-vacuous proof that `s.bundleAssembling`, not the queue-derived scan, did the rejecting. `TestV2Session_DebugBundle_ServedAfterFailedAssembly` pins the deferred clear: an assembly error yields exactly one deterministic reply with the error text absent from both wire and log, then a fresh request on the same conn is served with a new assembly — failing if the marker clear is missed on the error branch. `TestV2Session_DebugBundle_ErrorReplies` was **amended**, not left stay-green: its assembly-error subtest now polls for the reply instead of taking a bare snapshot, because the reply lands one `Run` pass later than the old inline-synchronous assumption. `TestV2Session_DebugBundle_RejectsSecondWhileQueued`, `…_ServedAfterDrain`, `…_PerConnIsolation`, and `…_NeverLogsContentEncryptedOnly` pass unchanged — all already synchronise by polling, so the extra `Run` hop is invisible to them.

## Context-usage requests

The `request_context_usage` tests (#2431, `v2session_contextusage_test.go` plus
`cmd/pyry`'s resolver tests) add one lesson to the `mcp_status_request` /
`mcp_reconnect` proof-trap list above and one lesson specific to a collapsing
seam. First, a planned nil-seam handler test was dropped rather than written:
`handleMCPStatusRequest`, the precedent this handler copies, has no
handler-side nil check at all — the dispatch gate is the only one — so a
handler-side test would have pinned code the dispatch arm makes unreachable.
The inert dispatch-gate posture is covered instead
(`TestV2Session_RequestContextUsage_InertGates`). Second, on the `cmd/pyry`
side, proving the collapse map is bounded by hosted conversations rather than
by request volume needs an assertion on the map itself, not just on the reply:
`TestContextUsageResolver_UnresolvableInstallsNoFlight` refuses an unresolvable
id and then asserts `len(r.flights) == 0` — a test that only checked the reply
shape would pass identically whether or not a resolver keyed on the raw
request string had gone ahead and installed an entry.

The `request_context_usage` e2e leg (`internal/e2e`) needed two corrections a
prior ticket's own leg had already documented, and both cost a cycle here
anyway. **A "requester-only reply" assertion is vacuous when an unsolicited
frame of the same type already shares the lane.** `context_usage` is published
automatically after every turn (#2371), so a leg that merely waits for one and
checks its shape proves nothing about correlation — it has to put an
unsolicited frame in flight *first*, then identify the reply to its own ask by
`InReplyTo` alone, with `EventID == nil` proving the reply skipped the #647
replay ring the unsolicited frame enters. **A frame solicited *by* `turn_end`
always arrives *after* it**, so a drain loop that stops at `turn_end` collects
zero of these every time and misreads as a regression rather than as the
producer ordering `turnEndContextUsageRequester` guarantees; the fix drains
past `turn_end` through a settle window, exactly as #2371's own leg documents.

## Background request waits

Moving the seam wait off `appFrameWorker` onto a per-ask goroutine (#2563, same
file) added three tests and two fixture traps worth knowing before touching
this suite again. `TestV2Session_RequestContextUsage_WaitDoesNotBlockLaterFrames`
blocks a fake `ContextUsageFor` on a channel, sends the ask, then sends a
v1-routed `send_message` on the same conn and asserts its reply arrives while
the seam is still blocked — proof of AC-1 that fails on the pre-fix inline
tree, since the `send_message` would queue behind the parked worker.
`TestV2Session_RequestContextUsage_WaitingAsksAreBounded` holds four asks
blocked in the seam, sends a fifth, and asserts it is refused at once
(retryable, correlated to its own `in_reply_to`) without touching the seam,
then releases the four and asserts each is answered exactly once — pinning
`maxContextUsageAsksPerConn` without a timing race, since the refusal is
synchronous on the worker. `TestV2Session_RequestContextUsage_TeardownEndsTheWait`
has the fake seam return a *successful* reading only after its `ctx` is
cancelled, then tears the conn down (or stops the manager) and asserts no
`context_usage` is ever sealed for it — pinning that a late reply is dropped by
`connCtx`, not merely delayed. First fixture trap: the harness's
`noiseMsgsForConn` helper (used to collect a conn's replies for counting)
calls `t.Fatalf` on a close envelope, because a close carries no inner frame to
decode — a test that tears a conn down and then counts its replies has to
filter the close envelope out itself before handing the slice to that helper,
or the count assertion never runs. Second: the manager-stop half of the
teardown test was already green on the pre-fix tree, because stopping the
manager cancels `runCtx`, which the seam was already waiting on before this
ticket; only cancelling a single conn's wait via `s.done` is new, so the
conn-teardown subtest specifically — not the manager-stop one — is the half
that reproduces red without the fix. A teardown test that covers only the
manager-stop path would pass on both trees and prove nothing about this
ticket.

Moving `mcp_status_request`'s wait off the worker the same way (#2702,
`v2session_mcpstatus_test.go`) reused this shape rather than rediscovering it,
and added one trap of its own. `TestV2Session_MCPStatusRequest_WaitDoesNotBlockLaterFrames`
mirrors `…_RequestContextUsage_WaitDoesNotBlockLaterFrames` exactly: a
`send_message` sent on the same conn while the seam waits gets its reply
first, failing on the pre-fix inline tree.
`TestV2Session_MCPStatusRequest_WaitingAsksAreBoundedAndEndOnTeardown` merges
what #2563 split into two tests into one, since both needed the same
`blockingMCPStatus` double: it fills `maxMCPStatusAsksPerConn` slots, refuses
a fifth ask at once, then tears the conn down and asserts exactly one sealed
reply exists on the conn — the overflow refusal — even though the fake seam
deliberately answers every held ask with a **successful** status after its
`ctx` is cancelled rather than a refusal. That choice is the trap worth
keeping: a seam answering `false` after cancellation would let a handler that
wrongly sealed a reply for a dead connection pass for a correctly-behaved
refusal, since a test that only inspected one reply's shape cannot tell a
true refusal from a late success sent by mistake. Counting every sealed frame
on the conn and asserting there is exactly one, instead of inspecting a
single reply, is what makes a wrongly-sealed late reply visible. On the
`cmd/pyry` side, `TestResolveBoundMCPStatus_BoundsTheChildWait`'s second half
(a short caller deadline yielding `(zero, false)`) proves only that the query
inherits *some* deadline from its caller — the first half, asserting the
deadline handed to a background-context call falls within
`(0, mcpStatusQueryTimeout+1s]`, is what actually pins `mcpStatusQueryTimeout`
as the bound's source. Noted on review as a test-strength gap, not an
implementation leak.

Two fixture traps surfaced building the above, worth knowing before touching this file again. `blockingBundler.release` must be closed from a `t.Cleanup` **registered after** the manager's `stop` cleanup — `t.Cleanup` runs LIFO, and release-before-stop unparks the seam before `stop` waits for `Run` to exit; registered the other way round, a pre-fix tree *hangs* in cleanup instead of failing, turning the RED proof AC #1 demands into a timeout with no diagnostic. And a pre-fix failure and a mutation-kill can redden the same test for unrelated reasons: `…_RejectsSecondWhileAssembling` also fails pre-fix, but only because `Run` is parked and never reaches the second request at all — that says nothing about the marker specifically. Only a mutant that drops `s.bundleAssembling` from the gate while leaving assembly off `Run` isolates the accept-side half, and it reddens as a missing-reply count rather than an obviously-wrong value — the absent reply *is* the two-concurrent-assemblies bug, read backwards.
