# #1539 — delete the orphaned relay modal deny-on-timeout arm

Short plan: this is a deletion. No new type, no new state, no new failure mode.

## Files read

- `internal/relay/v2session_modal.go` → `modalDenyTimeout`, `ArmModalTimeout`, `handleModalTimeout` (deleted); `reconcileModals` doc (cites `modalDenyTimeout`); `handleModalCancel`, `handleModalAnswer`, `broadcastModalDismissed`, `ModalDismissal` (unchanged).
- `internal/relay/v2session.go` → `V2SessionManager.modalTimeout` field, its `make` in `NewV2SessionManager`, the `Run` select arm (deleted); `idleTimeout`, `pushOverflow`, `bundleReady`, `newSessionDone` docs (cite it as precedent).
- `internal/relay/v2session_seams.go` → `ModalResolver.ResolveTimeout` (deleted). The `Interrupter` doc says `ModalResolver.Resolve*` — a glob that stays true with two methods, so it needs no edit. `OutstandingModals` doc ("neither re-arms the deny-on-timeout") is about the permbridge window and stays.
- `cmd/pyry/modal_resolve_v2.go` → `(*modalResolverV2).ResolveTimeout` (deleted); `modalResolverV2` doc ("Both methods"); `classTrust`, `emitFolderNotTrusted`, `ResolveAnswerWithAlwaysAllow` comments naming the #725 timeout; `(*streamApprovalBridge).retire` audit comment citing "the vocabulary ResolveTimeout uses".
- `cmd/pyry/relay.go` → `relayV2Wiring.blockedNotify` doc, the `modalReg` and `newModalResolverV2` wiring comments in `startRelayV2`.
- `internal/relay/v2session_modal_test.go` → `fakeModalResolver` timeout surface; `TestV2Session_ModalTimeout_FanOut`, `_AlreadyResolved_NoBroadcast`, `_NilResolver` (deleted); `waitForResolverCall` (kept).
- `internal/relay/v2session_appframe_test.go` → `TestV2Session_SlowHandler_ModalTimeoutStillFires` (retargeted); `blockingHandler` (its `entered` channel is the sync point); `TestV2Session_SlowHandler_DoesNotStallOtherConn` (Frames arm) and `_RekeyStillEmits` (manualRekey arm) — the arms already covered in the file.
- `internal/relay/v2session_debugbundle_test.go` → `TestV2Session_DebugBundle_AssemblyDoesNotStallRun` arm (3) (retargeted).
- `cmd/pyry/modal_resolve_v2_test.go` → `Timeout_*`, `TrustTimeout_*`, `PermissionTimeout_NoEmit` (deleted); `TestModalResolverV2_NilEmitSeams_Safe` (drops its "trust timeout" subtest, keeps "trust deny answer"); `blockedSpy`/`emittingResolver` (kept: `TrustAnswer_EmitPolicy` uses them).

A2 overlap: `origin/feature/449` touches `v2session.go`, but it is a May branch for a CLOSED issue with no PR — not in flight. No block.

## Context

`ArmModalTimeout` has no production caller since the #1348 stream cutover, so the whole relay-side deny-on-timeout chain is dead. The live deny-on-timeout is permbridge's own timer (`time.AfterFunc` → `expire`), proven hermetically by the `timeout`/`stdio_timeout` cases of `TestRelayV2_StreamModalPermissionRoundTrip`. No ADR needed — removal of dead code.

## Change

**Production (deletion + comments).** Delete `modalDenyTimeout`, `ArmModalTimeout`, `handleModalTimeout`, the `modalTimeout` field/`make`/`Run` arm, `ModalResolver.ResolveTimeout` and `(*modalResolverV2).ResolveTimeout`. Rewrite every comment that describes the machinery as live or cites it as a precedent:
- `idleTimeout`: "same posture as `rekeyInterval`" (a surviving test-overridable package var — check it is one; otherwise name whichever surviving timer var is).
- `pushOverflow`, `bundleReady`, `newSessionDone`: precedent becomes the chan-string/buffered sibling that survives (`pushOverflow` → `wake`'s off-Run AfterFunc → Run bridge; the others drop `modalTimeout` from their lists).
- `reconcileModals`: drop the "NOT this file's modalDenyTimeout" contrast.
- `cmd/pyry`: comments naming the #725 timeout or `ResolveTimeout` now name permbridge's deny-on-timeout (#1103) as the only one; the trust-class emit is now only on a remote deny. `relay.go` wiring comments lose "including deny-on-timeout" and the `ResolveTimeout` mention (the broken sentence fragment there is repaired in passing, same comment block).

Acceptance grep: `modalTimeout|modalDenyTimeout|ArmModalTimeout|handleModalTimeout|ResolveTimeout` over `*.go` (excluding `.claude/worktrees/`) matches nothing, bar an explicitly historical note if one is needed.

**Tests.**
- Delete the three `v2session_modal_test.go` timeout tests and `fakeModalResolver`'s `ResolveTimeout`, `timeoutSnapshot`, `timeoutOKFor`, `timeoutDismissal`, `timeoutCalls`.
- Delete the cmd/pyry timeout-only tests; drop the "trust timeout" subtest from `NilEmitSeams_Safe`.
- **Appframe retarget** → `TestV2Session_SlowHandler_PushStillDrains`: block conn A's worker with `blockingHandler(entered, release)`, wait on `entered` (deterministic: the handler is running), then `mgr.Push` a message to A; the drain arm must seal and forward it (A's first noise_msg decrypts to `TypeMessage`) while the handler is still parked. The drain arm is not otherwise covered in this file. `t.Parallel` becomes possible (no package var mutated).
- **Debugbundle arm (3)** → the manual-rekey arm: `mgr.Rekey(ctx 2s, connD)` returns nil and conn D's one noise_msg decrypts to `TypeRekeyRequest`. The test's three arms become Frames, drain, manualRekey; D no longer needs the interactive cap, the resolver goes, the package-var mutation goes and the test can run `t.Parallel`.

## Testing strategy

Mutation runs (recorded in the PR, reverted before commit):
1. `dispatchAppFrame` calls `routeAppFrame` inline instead of enqueueing on `s.appFrames` → `PushStillDrains` goes red (Run parked in the handler never services `drainCh`). Unmutated → green.
2. `DebugBundler` seam invoked inline on `Run` (pre-#1491 shape) → the test times out/fails at arm (1) already; to prove arm (3) itself is non-vacuous, also run with arms (1)(2) temporarily skipped so arm (3) is the first to face the mutation → `Rekey` returns a deadline error. Unmutated → green.

Gate: `go test -race ./internal/relay/... ./cmd/pyry/...`, `go vet ./...`, `go build ./cmd/pyry`. The e2e `TestRelayV2_StreamModalPermissionRoundTrip` is untouched (zero-line diff); the verifier's `make check` runs it.

## Documentation handoff (pending — documentation stage)

After this ticket each must say the relay arm / `ResolveTimeout` was removed and permbridge's timer is the only deny-on-timeout:
- `docs/knowledge/features/modalbridge-package.md`: the numbered step `armer.ArmModalTimeout(ctx, modalID)`, and the note that "nothing arms the relay-side modalDenyTimeout".
- `docs/knowledge/features/v2-session-manager-surface.md`: the `ModalResolver` listing with `ResolveTimeout`.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md`: the mechanism walkthrough.
- `docs/knowledge/features/v2-session-manager-test-surface-same-package-unit-tests-internal-relay.md`: the deny-on-timeout test entry (and the renamed appframe test / retargeted debugbundle arm).
- `docs/knowledge/features/acp-package-acp-permission-proxy-session-request-permission.md`: the `modalDenyTimeout` citation.

## Security review

**Verdict:** PASS

The ticket deletes a fail-closed path, so the question is whether anything that path was guarding is now unguarded. The base fact is that `ArmModalTimeout` has no production caller, so the relay arm was never armed in a running daemon. Deleting it cannot change the runtime behaviour of a production build. The findings below check that this is true for every modal the registry can hold, not only that the call graph is empty.

**Findings:**

- [Trust boundaries / fail-closed coverage] No findings. The only production writers of `modalbridge.Registry` are in `(*streamApprovalBridge).Surface`: `modal.RecordWithContext` for a permission modal, and its question arm `surfaceQuestion`, which writes a separate question registry. `Surface` runs only for an approval that is already parked in `permbridge.Registry`. Both production `Register` sites, the approve handler in `internal/control/server.go` and the stdio permission handler in `cmd/pyry/streamsup_runner.go`, arm `time.AfterFunc` → `expire` in `(*permbridge.Registry).Register` before any modal exists. So every recorded modal already has permbridge's deny (#1103), and no recorded modal relied only on the relay arm. On the RNG-failure degrade, `Surface` records nothing and permbridge's timer still denies.
- [Trust boundaries / trust-class emit] No findings. `Surface` builds every modal from `modalbridge.PermissionRequestForClass(tuidriver.ModalClassPermission, …)`, so no production path records a `trust`-class modal. Deleting the timeout-path `emitFolderNotTrusted` call therefore removes an emit that could not fire. A remote trust deny still emits through the `classTrust` check in `ResolveAnswerWithAlwaysAllow`, and `TestModalResolverV2_TrustAnswer_EmitPolicy` and the kept "trust deny answer" subtest of `TestModalResolverV2_NilEmitSeams_Safe` still cover it.
- [Concurrency / unbounded park] No findings for this ticket. `ResolveAnswerWithAlwaysAllow` step 2 leaves an ungated device's answer outstanding. The wait is bounded by permbridge's window (`mcpApprovalTimeout` for the approve path, the runner's `timeout` for stdio). `expire` denies when no `AnswerableFunc` is installed, when the window is non-positive, or when `(*streamApprovalBridge).ApprovalAnswerable` reports false. `ApprovalAnswerable` extends the window only while the approval is parked, eligible and at least one interactive conn is connected, and it re-checks at each window. That extension already exists and does not depend on the relay arm. Since the relay arm was never armed, it never shortened a wait. This ticket leaves the bound unchanged. A disconnect also still denies through `watchApproveConn`.
- [Error messages, logs, telemetry / audit] No findings. `(*streamApprovalBridge).retire` still writes `audit.OutcomeDeniedTimeout` / `audit.SourceTimeout` and broadcasts the matching `modal_dismissed` on every timeout, disconnect or shutdown return. `TestRelayV2_StreamModalPermissionRoundTrip`'s `timeout` and `stdio_timeout` cases and the `denied_timeout`/`timeout` assertion in `cmd/pyry/stream_approval_test.go` cover it, and none of them are edited. The deleted `ResolveTimeout` wrote the same vocabulary, so the audit loses no outcome class. `ModalDismissal.Source`'s closed set `{remote, local, timeout}` is still correct, because `timeout` now comes only from `retire`.
- [Tokens, secrets, credentials] Not applicable. No token is created, stored, compared or logged. The deleted code handled only modal ids.
- [File operations] Not applicable. No file is opened, created or renamed.
- [Subprocess execution] Not applicable. No `exec` path is touched. The deleted `ResolveTimeout` sent a keystroke through `modalKeystroker`, and that seam's remaining callers are unchanged.
- [Cryptographic primitives] Not applicable. Modal id minting (`newModalID`, crypto/rand) is untouched.
- [Network & I/O] No findings. One arm leaves `Run`'s select. No reader, size cap or deadline changes. The surviving arms keep their off-Run proofs through the retargeted `TestV2Session_SlowHandler_PushStillDrains` and debugbundle arm (3), each checked against its regression by mutation.
- [Concurrency / goroutines] No findings. The deleted arm spawned no goroutine of its own. Its `time.AfterFunc` sender lived only in `ArmModalTimeout`, which had no production caller, so no timer is left with nobody to receive it. Removing the `modalTimeout` channel removes a buffered send site and no receiver is left orphaned.
- [Threat model alignment] No findings. `docs/protocol-mobile.md` § Security model requires that an unanswered approval denies. Permbridge's timer meets that requirement on its own. This ticket does not change the wire vocabulary.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-23

## Revisions

- **2026-09-23, verifier FAIL on the missing security review.** The ticket is `security-sensitive`, and the plan was committed without the § Security review pass. That section is added above, and it answers the verifier's four points: fail-closed coverage per registry producer, the trust-class emit, the bound on an ungated answer's wait, and the surviving audit vocabulary. The verdict is PASS, and the design and code are unchanged.
