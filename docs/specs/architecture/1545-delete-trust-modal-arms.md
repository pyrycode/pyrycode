# #1545 — delete cmd/pyry's production-unreachable trust-class modal arms

## Files read

- `cmd/pyry/modal_resolve_v2.go` → `modalKeystroker`, `noopKeystroker`, `modalResolverV2` (fields `activeConv`, `notifyBlocked`), `classTrust`, `reasonFolderNotTrusted`, `emitFolderNotTrusted`, `ResolveAnswerWithAlwaysAllow` (trust-deny guard), `answerVerb`/`verbAnswer`/`verbAcceptTrust`/`verbEsc`, `optProceed`/`optExit`, `classifyAnswer`, `routeAnswerKeystroke`, `reasonRemoteDeny` doc, `streamApprovalBridge` field docs (`toolCallInFlight`, `questions`). The whole deletion surface.
- `cmd/pyry/relay.go` → `relayWiring.blockedNotify`; `startRelayV2` (resolver construction paragraph, the `ModalResolver:` config comment, the "same shape" comments beside `bridge.questions` / `bridge.toolCallInFlight`, the attachment-registry paragraph naming `modalResolver.activeConv`).
- `cmd/pyry/main.go` → the `blocked` closure and its comment; the `relayWiring` literal's `blockedNotify:` field; the `Pending` exemption comment (voice to follow for a deliberately removed #1014 seam).
- `cmd/pyry/pairing_mint_v2.go` → `classPairingMint` doc ("beside classTrust").
- `cmd/pyry/modal_resolve_v2_test.go` → `fakeKeystroker`, `recordTrustModal`, `TestModalResolverV2_Answer_Authorized`, `TestModalResolverV2_Answer_ForgedOption`, `blockedCall`/`blockedSpy`/`emittingResolver`, `TestModalResolverV2_TrustAnswer_EmitPolicy`, `TestModalResolverV2_NilEmitSeams_Safe`.
- `cmd/pyry/stream_approval_test.go` → two failure messages printing `kb.trustCalls`.
- `internal/modalbridge/modal.go` → `Registry.Record`, `buildPayload`: the outstanding modal's `Options` are exactly the request's option ids, so a permission-class record can carry `proceed`/`exit`.
- `internal/turnevent/permission.go` → `NewPermissionRequest`, `PermissionOption`.

## Context

Since #1348 the only production writer into the modal registry is `streamApprovalBridge.Surface`, which records permission-class modals only. Trust is pre-granted by `trustMark` before spawn. Every trust branch in `modalResolverV2` is unreachable, and it misleads readers into thinking a trust deny still surfaces a `session_error`. This is a pure deletion plus one pin test. No ADR needed.

## Change

1. **`modal_resolve_v2.go`**: delete `classTrust`, `reasonFolderNotTrusted`, `emitFolderNotTrusted`, the resolver's `activeConv`/`notifyBlocked` fields, the trust-deny emit guard in `ResolveAnswerWithAlwaysAllow`, `optProceed`/`optExit`, the `proceed`/`exit` cases of `classifyAnswer`, `answerVerb` and its three constants, `routeAnswerKeystroke`, and `modalKeystroker.AcceptTrust` with `noopKeystroker`'s implementation. New contract: `classifyAnswer(o, optionID) (outcome, choice string, ok bool)`; the keystroke arm calls `r.kb.Answer(choice)` directly. Any option id outside the four permission kinds falls to `default` → `ok=false`. Comments that cite the deleted seams (resolver field docs, `newModalResolverV2`'s doc, `reasonRemoteDeny`'s doc, `streamApprovalBridge.toolCallInFlight`/`questions` "precedent" sentences) are reworded to stand without them; `streamApprovalBridge.activeConv` is untouched.
2. **`relay.go`**: delete `relayWiring.blockedNotify` and the two resolver seam assignments; rewrite the construction paragraph (constructed ahead of the config literal because `streamApprovals` is assigned after the bridge exists); drop the `ModalResolver:` comment's last #1014 sentence only (rest is #1515's); reword the "same shape" / "same one modalResolver.activeConv reads" references to name the live precedent instead (the `streamApprovals` post-construction assignment / the follow-active cursor itself).
3. **`main.go`**: drop `blockedNotify: blocked` from the `relayWiring` literal; rewrite the `blocked` comment so its only sender is `msgqueue.Config.OnGiveUp`, recording (in the `Pending` exemption's voice) that the #1014 trust-deny sender was removed because trust is settled before spawn.
4. **`pairing_mint_v2.go`**: `classPairingMint` doc sits beside `classQuestion` only.

`internal/modalbridge` is untouched: it still models the trust class on the wire.

## Testing strategy

- **New pin (RED first):** `TestModalResolverV2_Answer_TrustOptionIDsRejected` — for each of `proceed`, `exit`: record a `turnevent.NewPermissionRequest` whose own `Options` carry that id (alongside `allow_once`) into `modalbridge.New()` as class `permission`; answer it from an eligible device; assert `ok=false`, zero dismissal, `routedNothing`, modal still outstanding by `Lookup`, zero audit records. Must be red on the pre-change tree for both ids (today they classify ok and route AcceptTrust/SendEsc).
- **Deleted with the arms:** the `proceed`/`exit` rows of `TestModalResolverV2_Answer_Authorized` (and its `trust` field), the trust-modal row and `trust` field of `TestModalResolverV2_Answer_ForgedOption`, `blockedCall`/`blockedSpy`/`emittingResolver`, `TestModalResolverV2_TrustAnswer_EmitPolicy`, `TestModalResolverV2_NilEmitSeams_Safe`, `recordTrustModal`, the fake's `AcceptTrust`/`trustCalls`, and the `trustCalls` mentions in failure messages (`stream_approval_test.go`, ungated-device test).
- **Unmodified and green:** `cmd/pyry/session_error_v2_test.go`, `internal/msgqueue/queue_test.go`, `internal/modalbridge`.
- Gate: `go test -race ./cmd/pyry/ ./internal/modalbridge/`, `go vet ./...`, `go vet -tags e2e_realclaude ./...`, `go build ./cmd/pyry`; symbol-absence grep over `cmd/pyry` for every name in AC-1.

## Documentation handoff (pending — documentation stage)

`docs/knowledge/features/modalbridge-package.md`: keep the sentence describing a trust `deny` yielding `session_error{session.blocked, "folder not trusted"}` as modalbridge's wire contract, and add that `cmd/pyry` no longer has a producer for it (#1545). Do not delete the sentence.

## Open questions

None.

## Revisions

- **2026-09-23 (verifier finding, rework 2):** the ticket carries `security-sensitive`, and the plan committed before the code had no `## Security review` section. The pass below was run against the plan and the shipped diff; its verdict is PASS and it changes no design decision. The same rework fixes the verifier's NIT: `modalKeystroker`'s doc said `*supervisor.Supervisor` satisfies "all three" methods, but the interface now has two.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. The untrusted input is a relay `modal_answer` frame (`modal_id`, `option_id`, `answer_token`, `always_allow`) arriving at `modalResolverV2.ResolveAnswerWithAlwaysAllow`. The order of its gates does not change: `Registry.Lookup` (a miss is a no-op), then the fail-closed `Device.MayAnswerRemotePermission` check (audited `denied_unauthorized`, modal left outstanding), then `streamApprovalBridge.RemoteAnswerable`, then `classifyAnswer`, and only after that the consume in `Registry.Resolve`. `classifyAnswer` still requires the id to be in the modal's own surfaced `Options` and to be one of the four `turnevent.PermissionOptionKind*` ids. Everything else, `proceed` and `exit` included, goes to `default` and returns `ok=false`: no keystroke, no consume, no audit, modal still outstanding. The change narrows the accepted set from six ids to four and adds none. `TestModalResolverV2_Answer_TrustOptionIDsRejected` pins this with `proceed`/`exit` in the fixture's own `Options`, and so reaches the option switch rather than the membership scan.
- [Trust boundaries — trust enforcement] No findings. Removing `emitFolderNotTrusted` does not weaken workspace trust. That emit only ran after a remote `exit` answer on a trust-class modal, and no production path records one. The only production writer into the modal registry is `streamApprovalBridge.Surface`, which calls `modalbridge.PermissionRequestForClass` with the literal `tuidriver.ModalClassPermission`. That returns wire class `permission` with the four permission options. The `tuidriver.ModalClassTrustFolder` arm of `PermissionRequestForClass` has no caller in `cmd/pyry`. `questionbridge` records into its own registry. Trust is settled before spawn by `trustMark` (`trust.MarkWorkdirTrusted`) in `main.go`'s spawn-directory paths, which this ticket does not touch. If a future change did record a trust-class modal, its `proceed`/`exit` answers would now fail closed: rejected, left outstanding, no keystroke. They would not auto-accept trust.
- [Tokens, secrets, credentials] No findings. No token is generated, stored or compared. `answer_token` is still discarded unread (`_ = answerToken`) and never logged. The audit record still carries only `Device.TokenHash` and `Device.Name` through `auditAnswer`.
- [File operations] Not applicable. The diff opens, writes and stats no file. `trustMark`'s file write is unchanged.
- [Subprocess execution] Not applicable. No `exec.Command` and no environment change. The deleted `AcceptTrust`/`SendEsc` routing sent keystrokes to a PTY that #1348 already removed. `noopKeystroker` still routes nothing.
- [Cryptographic primitives] Not applicable. No RNG, hashing or comparison against a secret is added or changed.
- [Network & I/O] No findings. There is no new read path. The frame size is still capped by the transport AEAD frame, and the terminal `session_error` send that was removed (`relayWiring.blockedNotify`) only ever went outbound.
- [Error messages, logs, telemetry] No findings. No log line is added. The `modal_answer.invalid_option` warning, which a rejected `proceed`/`exit` now reaches, still passes the attacker-controlled id through `truncateForLog(optionID, 64)`, and slog JSON-escapes it. That warning can already be triggered with any forged id, so the new rejection opens no new log-flood path. Removing the `folder not trusted` `session_error` takes away one outbound message a phone could see. It does not take one away from an operator, because the message could not be emitted in production.
- [Concurrency] No findings. The deletion removes two seams that were read without locks (`modalResolverV2.activeConv`, `modalResolverV2.notifyBlocked`) and adds no goroutine, lock or channel. `blocked` in `main.go` keeps its one live sender, `msgqueue.Config.OnGiveUp`, and nothing about its concurrency changes. `streamApprovalBridge.activeConv` is untouched.
- [Threat model alignment] OUT OF SCOPE. `internal/modalbridge` still defines the trust class and its `deny` → `session_error{session.blocked, "folder not trusted"}` wire contract (documented in `docs/knowledge/features/modalbridge-package.md`). Per the ticket it stays untouched. The Documentation handoff records that `cmd/pyry` no longer produces it. Removing the trust class from the wire contract would be a protocol change and needs its own ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-23
