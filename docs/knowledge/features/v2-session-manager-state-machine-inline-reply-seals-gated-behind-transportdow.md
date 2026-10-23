# Inline reply seals gated behind `transportDown()` (#1526, #2530)

`dropInlineReplyIfDown(s, event)` probes transport availability on `Run`
immediately before an inline reply calls `forwardEnvelope`. A down transport
drops the reply without encryption or a send nonce. The helper and static
snapshot refusals now live in `v2session_snapshot.go`; the replay file,
`emitResync` and the replay pump are retired (#3738).

## Placement and side effects

Keep the probe at the inline call site. Ordinary pushes hold their queued head
unsealed until transport returns, while an undeliverable inline reply is dropped;
`forwardAppReply` has its own worker-reply probe. A shared seal primitive alone
cannot express these different consumption rules. Snapshot requests still
validate conversation membership before answering a static not-found/offline
error; deleting replay must not delete these unrelated refusals.

**`debugBundleReplyError`'s widened window is why #1526 stopped being deferrable, and `newSessionReplyWorkspaceRefused`'s is why #2530 was the strongest of the four it left behind.** Most sites run microseconds after their inbound frame, on `Run`, the same narrow window #874 originally deferred as low-risk. `debugBundleReplyError` is also reached from `handleBundleReady`, fired asynchronously after the bundle assembly completes off-`Run` — the same shape of widening that made #1525 worth doing once #965 moved handler execution off `Run` entirely. `newSessionReplyWorkspaceRefused` widens further still: its **late** path is the `LateSessionStarter` callback (#2477), which can fire anywhere across a whole workspace rotation rather than after one bounded assembly, so the down-window it exposes spans the rotation, not a bundle build. No live repro was recorded for these original sites; both fixes shipped on the strength of the pattern, not an observed failure.

**Dropped, not parked — identical posture to #1525.** A reply sealed while the leg is down was already undeliverable (`m.send` discards the `Outbound` error at Debug), so the guard only stops paying a nonce for a non-delivery that was already happening. Side effects that precede the reply are unaffected: a settings update still persists even when its ack is dropped, and `handleBundleReady`'s deferred marker-clear still runs. (`handleRequestSnapshot` has had no side effect to preserve here since #2540 deleted its render arm — every reply on this path is now an error.)

**Two existing tests asserted the defect this ticket fixes, and had to change.** `TestV2Session_DebugBundle_RejectsSecondWhileQueued` and `TestV2Session_DebugBundle_PerConnIsolation` (`bundleGatedManagerFor`) hold `Connected` false to keep a bundle queued, and — before this ticket — expected the busy-reject `debugBundleReplyError` to be sealed and delivered anyway despite the leg being down. That is exactly the burned-nonce AC1 forbids, so "every existing test passes unmodified" and "no nonce spent while down" could not both hold; the ticket's own scope note (transport-up / nil-seam only) resolves the conflict in AC1's favor. Both tests now observe the busy reject through its `v2.bundle.err_dropped_transport_down` drop line instead of decrypting a sealed reply; the wire shape itself (code, message, retryable, `InReplyTo`) stays pinned by `TestV2Session_DebugBundle_RejectsSecondWhileAssembling`, which runs with the leg up. **Before reusing this guard shape again, check whether a test's `Connected` seam and its `Outbound` recorder already disagree on purpose** — that disagreement is itself a sign the test is depending on a since-fixed nonce-burning seal.

**Log line:** one Debug line per site on the down path — `event=<site slug>`, `conn_id`, `reason=transport_down` — no payload, `InReplyTo`, ciphertext, plaintext, settings values, or key bytes (#833). Snapshot refusals log no delivered-success claim on a drop; at
`handleSetSessionSettings` the drop follows `v2.settings.updated`, because the
update genuinely persisted.

**TOCTOU and byte-identity, same posture as #874/#912/#1525.** Inline sealing sites run on `Run`, the single owner of `s.send`; the probe is a plain read of `m.cfg.Connected`, needing no lock. The residual single-frame up→down race carries over unchanged and is not defended further — including on the new_session late path, where the race window is the callback firing, not a whole-rotation race. `transportDown()` reads "up" when `Connected` is nil, so every pre-existing test that doesn't wire the seam is byte-identical to pre-#1526 behavior.

**Security invariant.** The guard only skips a seal, so it cannot cause nonce
reuse. A post-send `Outbound` error is too late: the send cipher has already
advanced and the phone's next receive would fail authentication. The retired
resync row is removed from the nonce table; supported snapshot, settings,
bundle, new-session, system-prompt and model-list reply assertions remain.

**The `session_settings` report uses the worker-reply guard.** `handleRequestSessionSettings` was in #1526's original scope but now runs on the per-conn worker and returns through `forwardToRun` → `forwardAppReply`, which #1525 already guards; it dropped out of #1526 before the plan was written. `settingsReplyError` (reached from `handleSetSessionSettings`, still `Run`-side) remained in scope.

Tests: `internal/relay/v2session_inlinereply_test.go` — `TestV2Session_InlineReply_TransportDown_BurnsNoNonce` covers supported inline sites, each waiting on its own drop slug before asserting the `Outbound`-attempt count is unchanged, then recovering and decrypting the next frame under the phone's unchanged `initRecv` (the nonce oracle, the same shape as `TestV2Session_AppReply_TransportDown_BurnsNoNonce`). The original guard isolation was confirmed by mutation: disabling one site's guard at a time failed exactly that site's row, ten of ten; making the helper log-then-seal-anyway failed every row on the AEAD MAC check, showing the oracle isn't vacuous.

**#2530 generalized the row harness for a genuinely async off-Run reply, and narrowed the content-free check.** `debugBundleReplyError`'s widened window was still triggerable synchronously in the test (`DebugBundler` returns its error inline), so #1526 needed no new harness shape. The new_session late row is not: `LateSessionStarter`'s callback is real off-Run work that the test must let start, then flip the leg down, then release — sending the trigger and dropping the leg are no longer one step. That needed a new `inlineReplyRow.late func(t *testing.T, down func())` field, decoupling "send the trigger" from "drop the leg" for the first time in this harness; reuse it, not `downAtOpen`, for any future site whose reply reaches `Run` from real asynchronous work rather than a synchronous handler return. Separately, the content-free check lowercases the whole drop line and rejects tokens including `model`; a per-site slug like `v2.modellist.…` trips its own check. The fix strips the known `event=<slug>` token before scanning rather than weakening the token list — every other field, including a stray `conv-` id, is still scanned for every forbidden token.
