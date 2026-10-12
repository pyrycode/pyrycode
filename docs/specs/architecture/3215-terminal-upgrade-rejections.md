# Terminal relay upgrade rejections

## Files read

- `internal/transport/wssclient.go` → `Config`, `realDial`, `Connect`, `serve`, `recvPump`, `sendPump`, `pingLoop`: classification, retries and diagnostic boundaries.
- `internal/transport/wssclient_test.go` → `newClientForTest`, `recordingHandler`, `TestConnect_LoudOnFirstUpgradeReject`, `TestFatalCloseThreshold_DialPathCounts`, `TestFatalCloseThreshold_CounterResetsOnHealthyConn`, `TestBackoff_ResetAfterStableConnection`: reuse cadence and existing counter proofs.
- `internal/transport/send_context_test.go` → `TestClientSendContextDelivery`, `TestClientSendContextPumpShutdown`: cancellation and ordered write completion.
- `internal/relay/connection.go` → `Connect`: existing production config remains unchanged; policy selection belongs to #3216.
- `docs/knowledge/features/transport-package.md` → Surface, Lifecycle and goroutines, Error model: close-status preference and bounded grace must survive sanitization.
- `docs/knowledge/features/development-verification.md` → Prove that tests distinguish the change: use dial counts and inspect complete error chains.
- `docs/protocol-mobile.md` → Admission failure and reconnect, Security model: first 401 is terminal only for the opted-in daemon leg; hostile Relay can reflect secrets.
- `CODING-STYLE.md`: fixed contract comments, focused race tests, small files.
- Dependency `github.com/coder/websocket` → `Dial`, `verifyServerResponse`: failed 101 validation can embed raw response headers in errors; response presence alone does not imply a non-101 status.

## Context

Supply optional terminal HTTP policy without enabling production admission. Raw library errors can carry reflected credentials or close reasons. No new decision record is needed: #3196 already declares the contract.
One deliverable: safe upgrade classification and its retry policy. Estimate approximately 650 written lines including plan and tests, one exported type, zero consumer migrations, four acceptance criteria and fewer than ten reject branches. The #1072 analogue wrote 340 added transport lines plus nine deletions; broader safety tests explain the increase from the refiner's 450-line hypothesis. No other fetched feature branch touches transport.

## Design

- Add `Config.TerminalUpgradeStatuses []int`; empty retains retries. Only non-101 HTTP upgrade rejections can select the terminal policy. The first selected rejection returns immediately without applying the WebSocket threshold.
- Add `UpgradeRejectionError` with numeric `StatusCode` and `Unwrap() error` returning only `ErrUpgradeRejected`. `errors.As` recovers the status on `realDial` and terminal `Connect` errors; `errors.Is` compatibility remains.
- Rebuild diagnostics from fixed operation categories, numeric HTTP/close codes, known transport sentinels and canonical cancellation values. Never retain original library errors, response objects or peer reasons. Reconstruct a reason-free `websocket.CloseError` to retain `websocket.CloseStatus`.
- Sanitize at dial and pump boundaries before logging or returning, and at the injected dial boundary in `Connect`. Ping logs and socket acknowledgments use sanitized errors. Keep the once-per-outage warning and consecutive-close/backoff logic otherwise intact.

## Concurrency model

No new goroutines or shared lifecycle state. `Connect` owns counters and policy selection. Existing three pumps remain child-context bounded and joined by `serve`; sanitization creates immutable errors. Caller configuration remains static while the client runs.

## State transitions and identity reuse

| Event | Race test |
|---|---|
| Initial selected HTTP rejection ends after one dial | `TestTerminalUpgradeRejection` |
| Successful connection drops, next rejection terminates without another dial | `TestTerminalUpgradeRejection` |
| Unselected HTTP/network failure retries and recovers | `TestUpgradeRetryRecovery` |
| Empty policy retries, cancellation/Close interrupts outage, warning re-arms after connection | `TestUpgradeRetryRecovery`, `TestUpgradeRetryShutdown`, existing `TestConnect_LoudOnFirstUpgradeReject` |
| 4409 observations cross dial/serve, success alone retains count, non-4409 cycle resets | `TestSafeFatalCloseDiagnostics`, existing `TestFatalCloseThreshold_DialPathCounts`, `TestFatalCloseThreshold_CounterResetsOnHealthyConn` |
| Stable uptime resets backoff independently | existing `TestBackoff_ResetAfterStableConnection` |
| Peer closure during receive, ping or accepted send | `TestSafePumpDiagnostics`, existing `TestClientSendContextPumpShutdown` |

## Error handling

Selected HTTP statuses return typed upgrade rejection, distinguishable from fatal close and cancellation. Non-selected and network failures retain cancellable backoff. Failed 101 validation is safe but cannot be selected as a terminal HTTP rejection. Preserve known sentinel/cancellation matching and close codes while discarding all original text and objects.

## Testing strategy

Write focused tests first and observe failure. Use loopback response body/header and close-reason markers, count dials, recursively inspect all unwrap branches and captured slog attributes. Exercise below-threshold and fatal close logs, ping and SendContext errors. Reuse existing warning, backoff and threshold tests. Run `go test -race ./internal/transport/...`, `go vet ./...`, and `go build -o /tmp/builder-3215/pyry ./cmd/pyry`; full-module suite belongs to verifier.

## Open questions

None. API names and invalid-101 policy are settled above.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/transport-package.md`, reconcile **Surface**, **Lifecycle and goroutines** and **Error model** (including **#631 additions — loud on a rejected upgrade**) to describe caller-selected terminal HTTP statuses, programmatic status recovery, and exclusion of raw response/close-reason text from diagnostics and error chains. State that the default still retries, configured 4409 retains its consecutive threshold, and that counter is separate from uptime-based backoff reset.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `realDial` and the pump error boundaries rebuild trusted diagnostics from numeric status and fixed sentinels; failed 101 header validation must also discard raw errors.
- [Tokens] No credentials are generated/stored; reflected body/header/close-reason markers must be absent from every error branch and log attribute.
- [File operations] No production filesystem operations are added.
- [Subprocesses] No subprocess or environment handling is added.
- [Cryptography] Existing TLS and jitter remain unchanged; no crypto or secret comparisons are added.
- [Network and I/O] Existing 1 MiB read cap, write deadline, heartbeat and dial cancellation remain. Library owns response-body closure. No server is added.
- [Errors/logs/telemetry] SHOULD FIX: reconstruct close errors with empty reasons rather than wrapping originals, including send acknowledgments and ping warnings. Test all unwrap branches, not just top-level strings.
- [Concurrency] Existing close-status preference and grace protect classification when pumps race; no locks or goroutines are added.
- [Threat model] Malicious Relay reflection is addressed. Admission credentials/policy wiring are OUT OF SCOPE, owned by #3216 and #3217; phone authentication and Noise remain unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11
