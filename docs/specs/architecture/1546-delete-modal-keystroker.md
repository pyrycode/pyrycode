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
