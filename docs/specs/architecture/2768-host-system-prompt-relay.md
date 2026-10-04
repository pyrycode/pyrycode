# #2768 — Daemon-wide host system prompt relay access

## Files read

- `internal/sessions/daemoninstructions.go` → `DaemonInstructions`, `DefaultDaemonInstructions`, `SetDaemonInstructions`: validating, durable pool API from #2766.
- `internal/sessions/daemoninstructions_test.go` → `TestDaemonInstructionsNextStarts`, `TestDaemonInstructionsComposition`: existing lifecycle and composition proof to retain.
- `internal/protocol/host_system_prompt.go` → host prompt payloads: required write pointer and always-present reply strings from #2767.
- `internal/relay/handlers/set_system_prompt.go` → `SetSystemPrompt`: static refusals and correlated replies; its best-effort persistence is unsuitable here.
- `cmd/pyry/relay.go` → `relayWiring`, `startRelayV2`: app-map registration without an interactive gate.
- `cmd/pyry/main.go` → `runSupervisor`: the existing pool owns conversation starts and will supply instructions access.
- `cmd/pyry/relay_guard_test.go` → `TestEveryInboundV2TypeHasHandler`: move both pending verbs to inbound classification.
- `cmd/pyry/session_router_test.go` → `stubRunner`: existing fake runner surface for a consumer lifecycle test.
- `internal/e2e/relay_v2_daemon_test.go` → `driveHandshakeToOpenDaemon`: paired client without interactive negotiation.
- `internal/e2e/harness.go` → stream daemon start/restart helpers: preserve durable state across restart.
- `internal/e2e/relay_v2_stream_new_session_dormant_test.go` → `noiseWire`: encrypted request/reply helpers.
- `docs/knowledge/features/relay-package.md`, `sessions-package.md`, `e2e-harness.md`, `development-verification.md`: handler conventions, harness isolation and meaningful boundary tests.
- `docs/knowledge/features/sessions-package-key-types-writesystemprompt-systemprompttext.md` → durable instructions and mint-window sections: refresh at activation, never rewrite active files.
- `docs/protocol-mobile.md` → Security model: pairing grants remote control; transport encryption does not remove prompt injection risk.
- `CODING-STYLE.md`: error translation at consumers and stdlib tests.

## Context

Expose the merged daemon-wide setting to paired clients through the pool already starting conversations. One deliverable: durable relay access. No new decision record is needed. Feature #2753 touches different cmd files; no overlapping target files or unmerged dependency was found.

## Design

Add consumer interface `HostSystemPromptStore` with `DaemonInstructions() string`, `DefaultDaemonInstructions() string`, and `SetDaemonInstructions(string) error`. `*sessions.Pool` satisfies it. Thread that narrow surface through `relayWiring` from the existing pool. Register `RequestHostSystemPrompt` and `SetHostSystemPrompt` beside the conversation setter in the authenticated application-handler map.

Read decodes the request DTO; write decodes the required string pointer, then calls the pool setter. Validation and storage stay in the pool. Both successful paths project the current/default pair and use `dispatch.Conn.Reply`, with no broadcast or session lookup. Remove pending-handler source comments and update the inbound guard classification.

Sizing before commit: approximately 620 written lines including plan, production and tests; one new exported interface, two wiring consumers, four acceptance criteria, and at most six reject branches. All five limits hold.

## Concurrency model

No new goroutines. Handlers run on existing authenticated dispatch. Pool instructions locking serializes durable writes and reads independently of session lifecycle locks. A success snapshot reflects the current committed value; a concurrent later set may supersede it.

## Error handling

Malformed JSON and missing/null/non-string writes receive correlated non-retryable `protocol.malformed`. Map both distinguishable pool validation sentinels to that code. Other setter errors receive retryable `host_system_prompt.unavailable`, with no success acknowledgement. Log only static event names and connection identity, never payloads or returned error strings. Transport reply failures return through existing dispatch.

## Testing strategy

Write tests first and observe failure before implementation. Handler cases cover fresh read, verbatim/empty/default and inclusive multibyte boundaries, malformed payloads on both verbs, wrapped validation sentinels and storage errors with secret-bearing messages, exact correlation/cardinality and prompt-free logs. A real-pool consumer test induces storage failure and checks memory/disk retention, then proves handler writes preserve an active runner identity/file while pre-minted and later-started conversations see the latest setting with their own text last. Retain #2766's full lifecycle tests.

A fake-daemon/fakeclaude relay test uses two paired clients without interactive negotiation, no conversation session, and request barriers to prove requester-only read/write replies. Read fresh defaults, set custom text, restart/read, clear, restart/read, and set the returned default. Inspect daemon logs for the distinctive prompt sentinel. Run race tests on touched packages (including tagged e2e), `go vet ./...`, and build `cmd/pyry`; the dispatcher owns the full-module gate.

## Open questions

None. JSON's standard string decoder repairs invalid UTF-8; inject the pool sentinel to prove its error mapping without adding another content validator.

## Documentation handoff

Pending for the documentation stage: in `docs/protocol-mobile.md`, under “Daemon-wide host system prompt” and in the application-message table, replace pending-handler wording with shipped routing and acknowledgement behavior. Confirm examples match actual read/set replies, state that success follows durable persistence, and retain paired-client access without an interactive gate, requester-only replies, reset-via-set, explicit empty surviving restart, and next-session-start activation with per-conversation text last.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] App-map handlers run only after paired Noise authentication. The required write pointer distinguishes missing/null from explicit empty; pool validation remains the single content boundary.
- [Tokens, secrets, credentials] No credential generation or storage changes. Prompt bytes may be sensitive: only the requester receives the current/default pair.
- [File operations] No client path is accepted. `SetDaemonInstructions` owns the fixed daemon-store path and 0600 same-directory temporary write/sync/close/rename, with 0700 directory creation.
- [Subprocesses] Handlers receive no session-control methods and launch no subprocess. Instructions flow through existing prompt files at next start.
- [Cryptography] Existing Noise encryption and key lifecycle are reused unchanged.
- [Network and I/O] Existing v2 application-envelope size cap (65519 bytes) bounds dispatch; pool enforces the inclusive 8192-byte content limit. No listener or timeout changes.
- [Errors, logs, telemetry] Static refusal messages and events only; never log parser or storage errors that could echo prompt text. Tests inject secret-bearing errors to pin this.
- [Concurrency] Dedicated pool instructions lock covers persistence before publication. Failed writes retain old memory/disk; no new goroutines or lock order.
- [Threat model] Paired clients intentionally gain daemon-wide instruction editing, including without interactive capability. Existing pairing/revocation and encryption protect access. Prompt injection remains the protocol's documented residual risk; no new renderer or execution surface is introduced.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-04
