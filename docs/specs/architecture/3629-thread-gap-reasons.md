# #3629 Thread gap retirement reasons

## Files read

- `cmd/pyry/daemon_thread_relay.go` → `daemonThreadPublisher.gap`, `publish`, `deliver`: five gap call sites in two consumers, existing recipient retirement and cursor reset.
- `cmd/pyry/daemon_thread_relay_test.go` → `TestDaemonThreadContinuity`, `TestDaemonThreadUnusable`: retained-range recovery and the no-cursor status guard.
- `internal/protocol/thread_encoding.go` → `EncodeThreadUpdate`, `threadEncodePayload`: invalid DTO/content encoding fails without returning input data; successful frames are JSON-encoded envelopes.
- `docs/knowledge/features/thread-package.md` and `thread-package-shadow-lifecycle-and-evidence.md` § Shadow lifecycle and evidence: query-only consumers must retire too; status is a hint and recovery acquires metadata without broadcasting history.
- `docs/knowledge/features/development-verification.md` → change surface and inherited-premise guidance; `CODING-STYLE.md` → injected structured logging and tests.
- `docs/protocol-mobile.md` § Security model: private application content must remain outside operational logs.

## Change

Add a fixed reason argument to `daemonThreadPublisher.gap` and emit one Warn with a static message, event `daemon.thread.gap` and that reason before existing retirement. `publish` supplies `state_not_usable` at its status guard, `baseline_required` for unusable/baseline-required `Changes`, and `compact_reset_required` for unusable/reset-required `CompactChanges`. Both encoding and envelope-decoding failure paths in `deliver` supply `encode_failure`; replace its separate encoding warning with the common gap warning. No raw errors, IDs, paths, history or payload values enter the record. Recipient selection, retirement, cancellation and cursor reset/reacquisition remain unchanged. No new types, state, goroutines or failure modes are introduced. No concurrent feature branch touches the planned files.

This is one observable behavior with its privacy and retirement regression proof. Estimated total written work: 230 lines including this plan and tests; under 30 production lines, zero exported types/interfaces, five call sites in two consumers, two acceptance criteria, no new reject branches. All five sizing ceilings pass. The named analogue `d5db10a4` is a similarly local guard, but this ticket needs more scenario coverage.

## Testing strategy

Add table-driven `TestDaemonThreadGapReasons` in `cmd/pyry/daemon_thread_gap_test.go` using a deterministic `daemonThreadStore` double. Enter through `publish` for each status/Changes/CompactChanges guard, and through `deliver` with invalid content for `encode_failure`. Assert exactly one Warn, the static message/event/reason and only normal time/level/message metadata plus event/reason in JSON logs. Include private-looking fixture values and a retirement error. The encoder returns valid JSON envelopes on success, so the defensive envelope-decoding branch receives the same reason by direct code review without adding a production injection seam.

Each scenario verifies unique retirement of tracked and currently eligible query-only peers, exclusion of ineligible peers, empty cursor/peer state at retirement completion, and subsequent metadata-only acquisition without another warning or historical push. Baseline-required cases must reacquire immediately; other cases reacquire on the following pass. Also prove an unusable status without an existing cursor stays silent. Run the new test red before implementation, then focused thread publisher tests under the race detector on Linux, `go vet ./...`, and `go build -o /tmp/builder-3629/pyry ./cmd/pyry`. Full-module testing belongs to the verifier gate.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `publish` and `deliver` select literal reasons from control flow; no store result or peer identifier becomes a logging attribute.
- [Tokens, secrets, credentials] No credential generation/storage changes. Private-looking IDs, content and error text in tests must never appear in the exact allowed record.
- [File operations] No new file operations; the injected logger retains its existing output destination and permissions.
- [Subprocesses] No subprocess creation or environment changes.
- [Cryptography] No cryptographic operations or key/nonce handling changes.
- [Network and I/O] No new network reads/writes; `gap` keeps existing retirement I/O and eligibility checks. One fixed-size log per actual gap adds bounded work per invocation.
- [Errors, logs, telemetry] SHOULD FIX during build: consolidate the existing encoding Warn into the gap Warn and enforce the exact JSON record allowlist, preventing duplicate warnings and accidental error/content disclosure.
- [Concurrency] Existing serialized publication owns the same cursor and peer map; logging introduces no shared state, locks or goroutines.
- [Threat model] Operational disclosure is the relevant protocol threat here; static messages and literal attributes keep application plaintext out of the journal. Authentication, routing and encryption contracts are unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-19
