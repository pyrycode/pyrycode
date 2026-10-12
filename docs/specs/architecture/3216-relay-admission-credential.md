# Selected daemon admission credential

## Files read

- `internal/relay/connection.go` → `Config`, `Connect`, `Connection.run`, `classifyTransportErr`: header construction, lifecycle and terminal classification.
- `internal/relay/connection_test.go` → `newTestConnection`, `TestConfig_Validation_TableDriven`: existing helpers bypass `Connect`, so admission tests require its public boundary.
- `internal/transport/wssclient.go` → `Config`, `Client.Connect`, `UpgradeRejectionError`: selected HTTP statuses terminate independently of the close counter; sanitized errors retain numeric classifications only.
- `internal/transport/upgrade_rejection_test.go` → `TestTerminalUpgradeRejection`, `TestUpgradeRetryRecovery`, `assertSafeTransportError`: reusable recovery proofs and recursive error-chain inspection pattern.
- `cmd/pyry/relay.go` → `startRelay`: current caller omits the new input; non-conflict terminal errors already leave local daemon service running. Private configuration/wiring belongs to #3217.
- `docs/protocol-mobile.md` § Authentication → Binary → relay → Daemon admission (declared): authoritative header, token shape and first-401 contract.
- `docs/knowledge/features/relay-package.md` § Headers, Reconnect semantics, Daemon admission security boundary: preserve phone authentication and distinguish library implementation from activation.
- `docs/knowledge/features/transport-package.md` § HTTP status recovery and diagnostic safety: never wrap raw peer errors under sanitized text.
- `docs/knowledge/features/development-verification.md` § Verify inherited premises, Test execution and artifact survival: use merged transport behavior and count actual tests.
- `CODING-STYLE.md`: local additive edits, stdlib tests, injected logging and goroutine shutdown.

## Context

#3196 declared identity-bound daemon admission; merged #3215 supplies safe transport rejection handling. This slice delivers a caller-selected credential and terminal HTTP 401 classification at the relay library boundary. It adds no configuration I/O or production enforcement. No additional decision record is needed.

## Design

Add optional `Config.AdmissionToken string`; empty means absent. `Connect` validates supplied bytes as exactly 64 ASCII lowercase hexadecimal characters before creating a transport or launching goroutines. Invalid input returns fixed text wrapping `ErrInvalidConfig`, without retaining input.

Construct exactly one `x-pyrycode-admission-token` header with the unchanged supplied string; omit it for empty input. Existing identity, version and user-agent headers remain. The transport retains these headers across all retries. Credentials enter no URL, routing or application payload.

Set `transport.Config.TerminalUpgradeStatuses` to HTTP 401 for every connection, including absent-token connections. `classifyTransportErr` maps numeric HTTP 401 in `UpgradeRejectionError` to new public sentinel `ErrAdmissionFailed` with static text and no transport cause. Preserve the existing 4409 classification and eight-consecutive-close policy; other transport errors pass through. No feature branches currently overlap the planned files.

Sizing after plan: approximately 450 total written lines, zero new exported types/interfaces, zero required simultaneous consumer updates, four acceptance criteria and fewer than ten rejection branches. One deliverable: the relay admission contract.

## Concurrency model

No new production goroutines, locks or retry state. `Connection.run` owns result publication through `done`; transport owns retries and exits on its first 401. Existing context cancellation/`Close` stops transport and forwarding. Tests bound each connection with a deadline, close sockets and join `Wait`; captured records and attempt counts use a mutex/atomic operations.

## State transitions and identity reuse

| Event | Race-enabled proof |
|---|---|
| Initial supplied/absent credential, transient 503, successful upgrade | `TestAdmissionHeadersAndReconnect` checks every request and recovery. |
| Established socket drops and same identity reconnects | `TestAdmissionHeadersAndReconnect` checks unchanged headers and outbound frame bytes on both connections. |
| Initial HTTP 401 with supplied/absent token | `TestAdmissionUnauthorizedTerminal` checks one attempt, static distinct result and closed frames. |
| Successful connection followed by reflected close reason and HTTP 401 | `TestAdmissionUnauthorizedTerminal` gates the close after the established edge; checks exactly two attempts, independent of the conflict budget. |
| Invalid selected token | `TestAdmissionTokenValidation` checks fixed invalid-config result and zero loopback requests. |

## Error handling

Validation rejects malformed credentials synchronously without logging input. HTTP 401 returns only `ErrAdmissionFailed` from `Wait`; there is no token fallback or retry. Sanitized transport errors/logs continue protecting reflected HTTP bodies/headers and WebSocket close reasons. Network/5xx and 4409 policy remains owned by transport.

## Testing strategy

Write focused loopback tests through `Connect` first and observe their failure before implementing. Exercise lowercase hexadecimal boundaries and reject wrong length, uppercase, non-hex, whitespace, control bytes, Unicode and comma lists. Inspect header multiplicity on every request, URLs and outgoing routing bytes; exercise supplied and absent credentials. Capture structured logs and traverse single/multi-cause error chains for reflected synthetic tokens and peer diagnostics.

Run `go test -race ./internal/relay/...`, reuse transport recovery/4409 tests with `go test -race ./internal/transport/...`, and run existing Noise tests with `go test -race ./internal/noise/...`. Run `go vet ./...` and `go build -o /tmp/builder-3216/pyry ./cmd/pyry`. The dispatcher/verifier owns full-module `make check`; no live test is required.

## Open questions

None. Public input is `AdmissionToken`, terminal classification is `ErrAdmissionFailed`; empty string means omission.

## Documentation handoff

Pending documentation stage: in `docs/knowledge/features/relay-package.md`, update **Surface**, **Headers (locked at wire-spec)** and **Reconnect semantics** with the optional credential input, exact header shape and distinct terminal HTTP 401 outcome. Under **Daemon admission security boundary (declared)**, distinguish this implemented library contract from pending #3217 daemon configuration/wiring, Relay enforcement and operator activation; preserve the phone-token visibility caveat.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Connect` validates caller input before transport creation; `classifyTransportErr` trusts only a numeric rejection status, never peer text.
- [Tokens] Credentials remain immutable startup strings delivered only in one upgrade header. Generation, private storage and rotation selection are OUT OF SCOPE (#3217 and operator provisioning); no real credentials are used in tests.
- [File operations] No file access is added; configuration persistence belongs to #3217.
- [Subprocesses] No subprocesses or environment access are added.
- [Cryptography] This client performs shape validation, not secret equality or generation. TLS remains the production transport; plaintext is explicitly limited to the existing test seam. Relay enforcement/equality is OUT OF SCOPE (Relay #167).
- [Network and I/O] Exact length/alphabet blocks header injection and comma lists. Existing transport read limits, deadlines and TLS behavior remain. Tests use bounded loopback sockets only.
- [Errors, logs, telemetry] Return a sentinel without retaining the HTTP error; invalid-token errors are fixed value-free text. Inspect all log error attributes recursively, as sanitized outer text alone could conceal a raw nested cause.
- [Concurrency] No new production shared state; selected headers are constructed once. Test cleanup cancels/closes and joins lifecycles before reading final records.
- [Threat model] Library delivery does not enforce identity admission. Activation and stolen-token/compromised-relay risks remain with Relay #167 and operator rollout; phone-token visibility, pairing and Noise remain unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-12
