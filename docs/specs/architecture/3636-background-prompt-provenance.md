# Background permission prompt provenance

## Files read

- `cmd/pyry/mcp_approve.go` → `runMCPApprove`, `toolsCall`: MCP producer currently drops the spawned session identity.
- `cmd/pyry/mcp_files.go` → `envSessionID`, `runMCPFiles`: existing process-owned session attribution precedent.
- `cmd/pyry/streamsup_runner.go` → `mapStreamsupConfig`, `newStreamRunnerFactory`: spawned environment and separate correctly tagged stdio approval producer.
- `internal/control/protocol.go`, `internal/control/server.go` → `ApprovePayload`, `handleApprove`: local approval forwarding into the parked registry and surfacer.
- `cmd/pyry/prompt_answer_history.go`, `cmd/pyry/modal_resolve_v2.go` → `promptOwner`, `Surface`, `conversationFor`: immutable prompt ownership, eligibility and legacy cursor fallback.
- `cmd/pyry/daemon_control_live.go`, `cmd/pyry/daemon_live_state.go`, `cmd/pyry/daemon_live_relay.go` → `captureLocked`, `admitLocked`, `daemonRelaySnapshot`, `daemonLiveCurrent`: detached source and generation/revision retention.
- `internal/relay/v2session_livestate.go`, `internal/relay/v2session_handshake.go`, `internal/relay/v2session_thread.go` → `forwardLiveState`, `handleNoiseInit`, `threadWithheld`: clear/fresh ordering and thread bypass of legacy reconciliation.
- `cmd/pyry/daemon_live_relay_test.go`, `cmd/pyry/agent_switch_frames_test.go`, `cmd/pyry/mcp_approve_test.go` → authenticated wire and MCP/control test helpers.
- `docs/knowledge/features/permbridge-package.md`: MCP requests currently lack the session tagging already present on stdio/Codex requests.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md` § Relay delivery: preserve unresolved versus supplied provenance; never reconstruct it at relay delivery.
- `docs/knowledge/features/development-verification.md`, `docs/protocol-mobile.md` § Session-scoped live state and Security model: producer-to-wire proof and permission boundary.

## Context

Both preserved Mobile prompt failures remain failed. Retrieved bundle
`f94853a5b69bfb77ed4fed0ed68bdb4222bc9549093e16667a496c77fc1d1830`
(digest `18dfb03a90d2594504885a55934ab85ee56302703651fa09b66f215affb0ca1f`)
shows the failing turn using the MCP permission tool and the control approval
resolution. Unlike stdio approvals, MCP forwards no session identity. Retention
therefore correctly retains an unresolved source, which Mobile refuses.
One behaviour ships here: preserve the MCP prompt's producer through reconnect.
No new decision record is needed. No overlapping remote feature branch touches
the intended production paths at planning time.

## Design

Read the existing `PYRY_SESSION_ID` in `runMCPApprove`, passing it explicitly to
`newMCPApproveServer`. Keep the server immutable. Add optional `SessionID` to the
local `control.ApprovePayload`; after decoding tool arguments, unconditionally
replace that field with the process-owned ID, including when empty. Model input
cannot select its producer. `handleApprove` copies the field into
`permbridge.Request.SessionID` before registering and surfacing it.

Existing `promptOwner` then resolves the owning conversation and captures its
session/generation once. Retention, snapshot, eligibility and relay delivery keep
their current contracts and original modal ID. Do not alter Mobile, infer a
session from the active cursor, or route thread peers through `reconcileModals`.
Unbound legacy callers retain omitted provenance and existing behavior.

Sizing after this plan: approximately 430 written lines, zero new exported
types/interfaces, four existing constructor callers to update, three acceptance
criteria, no new state-machine rejection branches. Regression and native proof
belong to the same behaviour, preserving the verification floor.

## Concurrency model

No new production goroutine or lock. The MCP server holds immutable attribution;
the existing control handler owns register/surface/await/retire. Existing owner
locks and relay Run serialize capture and delivery. Test subprocesses use a
cancellable context, close stdin after receiving the verdict, and are joined;
test control and publisher workers are cancelled and joined.

## State transitions and identity reuse

| Event | Race-enabled proof |
| --- | --- |
| Prompt parked with phone absent, then reconnects while another conversation is active | `TestDaemonLiveRelayMCPPromptReconnect`: actual MCP process, control registry, retained snapshot and authenticated decrypted clear then fresh. |
| Second reconnect with same outstanding modal | `TestDaemonLiveRelayMCPPromptReconnect`: same payload ID and source; registry remains pending. |
| Generation advances while old prompt/cursor remains captured | `TestDaemonLiveRelayMCPPromptReconnect`: reuse session ID, replay old reading and reconnect; no old prompt restored. |
| Answer/retire | `TestDaemonLiveRelayMCPPromptReconnect`: explicit deny resolves the original parked request and joins producer. Existing prompt answer/retirement tests cover allow and concurrent registry winners. |
| Tool arguments attempt to select another session, including an unbound process | `TestMCPApprove_ProducerSession`: only process-owned identity reaches the control socket. |

## Error handling

Existing malformed/unknown-tool/socket/cancellation branches still fail closed.
No new allow path, retries, persistence, logging or payload rewriting. An empty
process identity remains unresolved; an obsolete nonempty identity cannot bind
to a different current session through `captureLocked`.

## Testing strategy

Write the actual MCP/control/reconnect regression first and observe missing
fresh-modal provenance before production edits. Assert every modal-family wire
frame, including the clear, with independent conversation/session expectations;
compare payload, timestamp, captured generation/revision and modal identity.
Check stale-generation suppression with a delivery barrier, then second reconnect.
Add a focused forwarder test for spoofed identity and empty producer identity.
Run affected `cmd/pyry` and `internal/control` tests with race detection via managed
admission on Linux, plus `go vet ./...` and `go build ./cmd/pyry`.
The verifier owns complete suites. Dispatcher owns final real-Claude/real-FCM
acceptance of the unchanged Mobile prompt method at preserved composition
`e95c4d6ba363d6dce1d3f55563186ada652db958` (or diagnostic derivative
`4a06c43f8c1987244c64f0fe5c2a36819515046e`) against the pushed Core candidate.
Count XML 1/1/0/0 and retain wake/reconnect/accepted-modal/exactly-one-alert evidence
with exact binary digest and supporting revision/device/Claude identities.

## Open questions

- Confirm the controlled MCP producer reproduces the wire omission before repair.
- Determine the managed request route for this exact Mobile method: the listed
  `mobile-scripted-evidence` recipe runs unrelated fixed checks. Do not substitute
  those for prompt acceptance; retain unsupported-selection diagnosis and hand
  off the exact pending method to the dispatcher if necessary.

## Documentation handoff

Pending documentation stage: in
`docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md`,
**Relay delivery**, state that outstanding prompts retain source
conversation/session, identity and ordering through reconnect clear/fresh
delivery, without reconstructing provenance from legacy payloads or consumer
bindings. Link the regression and document only the demonstrated cause.

## Security review

**Verdict:** PASS

- [Trust boundaries] The subprocess-owned environment supplies attribution.
  MUST FIX incorporated in design: overwrite any decoded `session_id` in
  `approveServer.toolsCall`, including with an empty process ID; model arguments
  must not route a prompt. The local control socket already grants same-user
  daemon access. This metadata grants no remote answer permission.
- [Tokens/secrets] No new token or credential. Session identity stays out of new
  logs/errors. Pairing and device authorization are unchanged.
- [File operations] No new production file access or writes. Existing spawned
  environment is reused; no per-session config file or path input is introduced.
- [Subprocesses] No new production subprocess; fixed MCP entry point reads its
  inherited environment. Regression uses a fixed test-helper command and joins it.
- [Cryptography] Existing Noise authenticated handshake/sealing remains the only
  remote delivery path. No keys, nonces or primitives change.
- [Network/I/O] Optional field crosses the already bounded local control request;
  retained envelope size validation and remote membership/capability gates remain.
- [Errors/logs] Existing fixed fail-closed errors remain; no prompt content or
  raw captures enter repository/public evidence.
- [Concurrency] Immutable server field; no new lock order or mutable production
  state. Source capture remains under existing transition/owner boundaries.
- [Threat model] Preserve remote permission eligibility, authenticated device
  authority and encrypted transport. Producer attribution cannot authorize an
  answer. Existing protocol residual threats are unchanged by this repair.

**Reviewer:** builder (self-review)
**Date:** 2026-10-19
