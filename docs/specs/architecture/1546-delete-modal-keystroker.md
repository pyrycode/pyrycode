# #1546 — cmd/pyry: delete the modal keystroker seam left inert by #1348

## Files read

- `cmd/pyry/modal_resolve_v2.go` → `modalKeystroker`, `noopKeystroker`, `modalResolverV2` (the `kb` field), `newModalResolverV2`, `ResolveCancel`, `ResolveAnswerWithAlwaysAllow`, `classifyAnswer` — the seam being deleted; also `streamApprovalResolver`, `streamApprovalBridge.RemoteAnswerable` and `streamApprovalBridge.ResolveStream`, whose doc comments name the "tui keystroke arm".
- `cmd/pyry/relay.go` → the `newModalResolverV2(modalReg, noopKeystroker{}, logger)` call (the only production call) and the stale `ModalResolver:` comment in the relay config literal ("PTY mode passes w.sup …").
- `cmd/pyry/modal_resolve_v2_test.go` → `fakeKeystroker`, `errNoLiveSessionForTest`, `auditRecords`, `auditLogger`; 13 constructor calls; `TestModalResolverV2_Cancel_KeystrokeError`, `TestModalResolverV2_Answer_KeystrokeError` (deleted).
- `cmd/pyry/stream_approval_test.go` → 9 constructor calls; `TestModalResolverV2_Cancel_NonStreamRoutesEsc`, `TestModalResolverV2_Answer_NonStreamRoutesKeystroke` (replaced).
- `cmd/pyry/stdio_permission_test.go` → 1 constructor call.
- `internal/relay/*_test.go` — their `escCalls` / `answerCalls` belong to relay-side fakes (`Interrupter`, `ModalResolver`), out of scope and untouched.

## Change

Delete `modalKeystroker`, `noopKeystroker`, the `kb` field and parameter: `newModalResolverV2(reg *modalbridge.Registry, logger *slog.Logger)`. `classifyAnswer` drops the answer-digit return and becomes `(outcome devices.RemotePermissionOutcome, ok bool)`; membership in `o.Options` via `slices.IndexFunc` stays the forged/unknown-id gate, so rejection before consume is unchanged (the `strconv` import goes).

In both `ResolveCancel` and `ResolveAnswerWithAlwaysAllow`, the `!handled` branch (nil `streamApprovals`, or `ResolveStream` returning false) now emits exactly one Warn instead of a keystroke:

- cancel: `"relay: modal cancel reached no stream approval"`, `event=modal_cancel.unrouted`, `modal_id`
- answer: `"relay: modal answer reached no stream approval"`, `event=modal_answer.unrouted`, `modal_id`

No other fields — no body, prompt, title or option text. No new failure mode: consume, audit and the returned dismissal proceed exactly as before. The Warn is observability for a branch no production run has reached (the registry's only producer, `streamApprovalBridge.Surface`, always correlates), per the ticket.

Comments: rewrite the doc comments on `modalResolverV2`, `streamApprovalResolver`, `newModalResolverV2`, `ResolveCancel`, `ResolveAnswer`, `classifyAnswer`, `RemoteAnswerable`, `ResolveStream` and the stale `ModalResolver:` comment plus the constructor-site comment in `relay.go` so none describes a keystroke arm or supervisor seam. Comments outside these two files are left to #1544/#1515.

## Testing strategy

- Replace `TestModalResolverV2_Cancel_NonStreamRoutesEsc` and `TestModalResolverV2_Answer_NonStreamRoutesKeystroke` with `..._Cancel_UnroutedWarns` / `..._Answer_UnroutedWarns`, each table-driven over two arms: nil `streamApprovals`, and a wired bridge whose `ResolveStream` declines a directly recorded (non-Surface) modal. Assert: exactly one log line with the `*.unrouted` event at level WARN carrying `modal_id`; no `secretModalBody` / title in the output; the modal is consumed; one audit record with the unchanged outcome; the dismissal unchanged.
- Delete `TestModalResolverV2_Cancel_KeystrokeError`, `TestModalResolverV2_Answer_KeystrokeError`, `fakeKeystroker`, `errNoLiveSessionForTest`.
- Every other resolver / stream-approval test: drop the `kb` argument and only the keystroke assertions (`escCalls`, `answerCalls`, `routedNothing`). Forged-option, trust-id and ungated-device tests keep their consume/audit assertions, so rejection-before-consume stays pinned.
- Gate: `go test -race ./cmd/pyry/...`, `go vet ./...`, `go vet -tags e2e_realclaude ./...`, `go build ./cmd/pyry`.

## Documentation handoff (pending — documentation stage)

- `docs/knowledge/features/v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md`: the `ResolveAnswer` bullet ("else keystroker") and the passage on `modalKeystrokerOrNoop` / `noopKeystroker` / the `!handled` fallback — now the `*.unrouted` Warn.
- `docs/knowledge/features/acp-package-acp-permission-proxy-session-request-permission.md`, section "Routing seam — keystrokes, not `PermissionResponse`", and the `startPermissionProxy` paragraph naming `host` as the `modalKeystroker`: mark historical.

## Security review

**Verdict:** PASS

The pass walked the design as committed and the implementation it produced. The review was added after the fact; see Revisions.

**Findings:**

- [Trust boundaries] No findings. The untrusted inputs are the phone's `modal_id` and `option_id`, arriving from a Noise-authenticated device through `dispatchAppFrame`. The gate order in `ResolveAnswerWithAlwaysAllow` is unchanged: `reg.Lookup` (no consume), then `dev.MayAnswerRemotePermission`, then `streamApprovals.RemoteAnswerable`, then `classifyAnswer`, then `reg.Resolve` (consume), then `devices.AuthorizeRemotePermission`, then `ResolveStream`, then audit. Both the per-device gate and `RemoteAnswerable` still run before consume. The `ForgedOption`, `TrustOptionIDsRejected` and `UngatedDevice` tests pin the no-consume and no-audit assertions for each rejection.
- [Trust boundaries — the `classifyAnswer` rewrite] No findings. The deleted return value was the 1-based position from `slices.IndexFunc`, and its only consumer was `kb.Answer`. The membership check against `o.Options` stays the first statement, now as `slices.ContainsFunc`. It runs before the kind switch. Its `ok=false` still makes the caller reject before `reg.Resolve`. An id the modal never offered, or a valid kind the modal never surfaced, is rejected exactly as before. The outcome mapping (allow-once and allow-always to `OutcomeAllow`, reject-once and reject-always to `OutcomeDeny`, anything else rejected) is unchanged.
- [Trust boundaries — can removing the keystroke reach allow?] No findings. The only allow actuation is `ResolveStream(modalID, allow=true, …)`, and `allow` still comes from `devices.AuthorizeRemotePermission`, which re-checks eligibility. On the `!handled` path nothing is actuated, as before: the keystroke went to `noopKeystroker`, whose methods all returned nil. Any parked approval stays parked and falls to permbridge's deny-on-timeout, which fails closed. No new branch sets `allow` or calls `ResolveStream`.
- [Consume, audit and dismissal on `!handled`] No findings, one note. The `!handled` path still consumes the modal, writes one audit record and returns the same dismissal. The only difference is the Warn in place of the no-op keystroke. The `*_UnroutedWarns` tests pin all four facts for both arms (nil `streamApprovals`, and a bridge that declines a directly recorded modal). Note: in that branch an authorized allow is audited as `allowed` even though nothing actuated. This was equally true under `noopKeystroker`, so it is not a regression. The new Warn is what makes such a mismatch visible. Production cannot reach it, because the registry's only producer is `streamApprovalBridge.Surface`.
- [Tokens, secrets] No findings. `answerToken` is still discarded and never logged. The audit entry carries only `TokenHash` and the device name, as before.
- [Error messages, logs] No findings. `logUnrouted` emits `event` and `modal_id` only: no body, prompt, title, option text or `option_id`. By the time it runs, `modal_id` has passed `reg.Resolve`, so it is a daemon-minted `crypto/rand` nonce from `newModalID`, not attacker-authored text. It needs no truncation. The `*_UnroutedWarns` tests assert that the secret body and the class title are absent from the output.
- [Network & I/O — log amplification] No findings. The Warn fires only after a successful one-shot `reg.Resolve`. A client can trigger at most one Warn per modal the daemon itself surfaced, and a replay misses at Lookup or Resolve first.
- [File operations, subprocess, crypto] Not applicable. The change deletes an interface and a no-op type. It opens no file, spawns no process and adds no primitive.
- [Concurrency] No findings. No goroutine, lock or channel was added or removed. Both methods still run on the manager's single Run goroutine. The registry mutex and the bridge's own mutex are the only synchronisation, as before.
- [Threat model] No findings. The mobile protocol's gated-answer invariant, "nothing but a fully-authorized, valid answer may consume the modal or reach the parked approval", holds with the same ordering. `internal/relay`'s `Interrupter` and `SessionStarter` are out of scope per the ticket.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-23

## Revisions

- **2026-09-23 — security review added after implementation.** The verifier failed the first review because the ticket carries `security-sensitive` and the plan had no `## Security review`. The section above audits the committed design and the implementation. It found no MUST FIX items, so the design and code are unchanged. The same commit re-wraps two comment lines the verifier flagged as NITs: the `ResolveCancel` doc comment and the `TestModalResolverV2_Answer_Authorized` doc comment.
