# #2797 — advertise stop_background_task support

## Files read

- `internal/protocol/handshake.go` → `CapabilityMultiAgent`: placement for the new wire constant.
- `internal/protocol/handshake_test.go` → `TestCapability_Constants_MatchSpec`: literal drift detector.
- `internal/relay/v2session_handshake.go` → `supportedV2Capabilities`, `negotiateCapabilities`, `handleNoiseInit`: ordered intersection and value-specific interactive flag.
- `internal/relay/v2session_test.go` → `TestNegotiateCapabilities`, `TestV2Session_Handshake_CapabilityNegotiation`: existing negotiation tables.
- `internal/relay/v2session.go` → `dispatchAppFrame`: stop requires a wired seam and `s.interactive` alone.
- `internal/relay/v2session_stop_background_task_test.go` → `TestV2Session_StopBackgroundTask_InertGatesStayOnRun`, `TestV2Session_StopBackgroundTask_Contract`: noninteractive inertness and interactive-only actuation already covered.
- `cmd/pyry/background_task_stop.go`, `cmd/pyry/main.go`, `cmd/pyry/relay.go` → `StopBackgroundTask`, `runSupervisor`, `startRelayV2`: #2796 is merged and the live-child implementation is wired.
- `docs/knowledge/features/protocol-package.md`, `protocol-package-handshake-control-payloads.md`, `relay-package.md`, `v2-session-manager-state-machine-capability-negotiation-on-the-handshake.md`, `development-verification.md`: pin literal values separately and retain value-specific interactive checks.
- `docs/knowledge/decisions/037-capability-strings-not-version-numbers.md`, `docs/protocol-mobile.md` § Security model: capability detection grants no authorization.

## Change

Add `CapabilityStopBackgroundTask = "stop_background_task"` beside `CapabilityMultiAgent` and append it to `supportedV2Capabilities`. Its comment defines detection through hello/hello_ack intersection and the client stop-button signal. Keep the existing intersection algorithm and interactive-only verb gate. No new types, state, error branches, or call-site updates are needed. Remote feature branches checked after fetching: no overlaps in the four files to edit.

Sizing: one deliverable, two acceptance criteria, approximately 110 written lines including this plan and tests, zero new exported types/interfaces, zero simultaneous consumer updates, zero new reject branches; within all five limits. The #2172 analogue uses the same constant-plus-supported-entry pattern. No new decision record is needed.

## Testing strategy

Extend the literal drift detector and both existing negotiation tables. Cover the new string alone, an all-six reversed advertisement with a duplicate and unknown string, and echo omission through existing interactive-only/no-advertisement rows. The new handshake row must report noninteractive. Existing stop inert-gate tests prove noninteractive rejection, and the stop contract's interactive-only connection proves the new advertisement is unnecessary for actuation. Run the changed negotiation tests red before production changes, then `go test -race ./internal/protocol/... ./internal/relay/...`, `go vet ./...`, and `go build ./cmd/pyry` (output binary outside the worktree).

## Documentation handoff

Pending documentation stage: in `docs/protocol-mobile.md`, “Capability negotiation (v2)” and “Stop background task (v2)”, state that `stop_background_task` is detection only, echoed through intersection negotiation, and clients draw a per-task stop only when `hello_ack` echoes it.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `negotiateCapabilities` emits only daemon-supported constants; duplicates and unknown client strings cannot widen the set. `handleNoiseInit` derives interactive access specifically from `CapabilityInteractive`, and `dispatchAppFrame` retains that stop gate. Test the detection-only advertisement directly.
- [Tokens, secrets, credentials] The new literal is public vocabulary; this change adds no credentials or logging and leaves device validation in `handleNoiseInit` intact.
- [File operations] No production filesystem operations are added; the capability is an immutable package-initialized slice member.
- [Subprocesses] No process actuation changes; the existing `StopBackgroundTask` bound-child lookup and bounded wait remain behind the interactive gate.
- [Cryptography] No keys, nonces, comparisons or primitives change; the ack retains the existing Noise response transport.
- [Network and I/O] No parser, socket or endpoint changes; `negotiateCapabilities` remains a bounded supported-set scan over the decoded advertisement and transport retains `maxFrameBytes` read limits.
- [Errors, logs, telemetry] No new error or log paths, and unknown client strings remain absent from the ack.
- [Concurrency] No goroutines or locks added; `supportedV2Capabilities` stays read-only after initialization.
- [Threat model] Detection conveys implementation support, not authorization. The existing encrypted paired-device transport and interactive gate continue to constrain the stop verb; relay MITM, replay and token handling are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-05
