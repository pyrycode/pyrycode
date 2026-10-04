# Update-when-idle control contract (#2757)

## Files read

- `internal/control/protocol.go` → `Request`, `Response`, `PairingResult`: additive optional result fields preserve older wire encodings.
- `internal/control/server.go` → `handle`, `SetPairingProvider`, `handlePairingMint`, `Serve`: provider snapshot outside execution, handshake/write deadlines, handler draining.
- `internal/control/client.go` → `MintPairing`, `request`, `requestPatient`, `exchange`: operation budget and cancellation watcher are distinct policies.
- `internal/control/dial.go` → `dialWithRetry`: caller context bounds dial attempts and startup retry.
- `internal/control/pairing_test.go` → pairing socket tests: analogue for result/error projection and releasing held providers before draining.
- `internal/control/client_test.go` → `startMisbehavingServer`: malformed-response tests need explicit peer draining.
- `internal/control/sessions_new_test.go` → `TestProtocol_SessionsRoundTripBackCompat`: older verb byte assertions.
- `docs/knowledge/features/control-plane.md` → Server Construction, Handshake Deadline, Testing: I/O deadlines cannot bound synchronous providers; held fakes must be released.
- `docs/knowledge/features/development-verification.md` → Establish the change surface, Protocol boundaries: inspect actual consumers and decoded payloads.
- `CODING-STYLE.md`: stdlib table tests, context cancellation, goroutine cleanup.

## Context

Expose a scheduling decision over the local control socket without waiting for idle or installation. #2758 owns release policy, binary operations, daemon provider wiring and CLI. No decision record is needed for this additive seam. QMD is unavailable; repository search supplements the owning overview. Fetched feature branches have no overlap with the proposed production files.

Sizing before implementation: one deliverable, four acceptance criteria, two new exported types, zero existing consumers needing updates, at most seven rejection branches, approximately 520 written lines including plan and tests. The #2388 analogue added 471 control lines; this adds cancellation and longer-window proofs. All five limits remain below their boundaries after planning.

## Design

- Add payload-free `VerbUpdateWhenIdle` (`update.when-idle`), with no new request field.
- Add `UpdateDecision` string constants `UpdateUpToDate` (`up-to-date`), `UpdateNotEligible` (`not-eligible`), `UpdateWillInstall` (`will-install`).
- `UpdateWhenIdleResult` carries `Decision`, optional `Reason`, optional `ReleaseTag` as JSON `decision`, `reason`, `releaseTag`. Add optional `Response.UpdateWhenIdle` as `updateWhenIdle`.
- `SetUpdateWhenIdleProvider(func() (UpdateWhenIdleResult, error))` installs or clears a provider without changing `NewServer`. The provider selects the release and schedules accepted work; it must bound its own synchronous release check and return before idle/download/install/restart work.
- `handleUpdateWhenIdle` snapshots the provider under `Server.mu`, invokes it once after unlocking, validates the result, and writes one response. An absent provider returns `update.when-idle: provider not configured`. A provider error discards its result and detail, returning `update.when-idle: operation failed`.
- One shared validator rejects missing result/decision, unknown decision, not-eligible without a reason, or will-install without a tag. Reject whitespace-only reason/tag; preserve valid values verbatim.
- `UpdateWhenIdle(ctx, socketPath) (*UpdateWhenIdleResult, error)` returns nil on every failure. Wire errors take precedence over any result; valid results are returned typed.
- Use one unexported 70-second timeout shared by server and client. The client derives a bounded context before dial, sets its absolute connection deadline, and watches cancellation using `context.AfterFunc`. Do not change `request` or `requestPatient` policies.
- After decoding this verb, install a fresh 70-second write deadline before provider execution. Retain the existing five-second handshake read limit and other verbs' policies.

## Concurrency model

Existing `Serve` goroutines own each one-request/one-response connection and drain on shutdown. Provider execution stays synchronous outside `Server.mu`; unrelated control requests and setter calls can proceed. Client cancellation pokes the connection deadline, stops the watcher on return and closes the connection. It does not cancel/retract accepted provider work. Tests always release blocked providers before draining the server, and drain custom peers explicitly.

## Error handling

Transport errors return nil results; provider failures never carry success payloads or detailed errors. Malformed results fail closed on both sides with fixed validation errors. Expired write bounds may prevent a response even after acceptance; callers cannot infer scheduling retraction from disconnect. Providers own their execution deadlines; I/O bounds cannot stop a stuck provider.

## Testing strategy

Write tests first and observe missing-contract compilation failure, then implement. Table-driven Unix-socket tests cover all three decisions, exact request/response bytes, one invocation, absent provider, provider error plus result, invalid provider results and malformed peer responses including error-plus-success. Run the existing byte compatibility assertions. Hold a provider beyond five seconds to prove both response windows; while held, complete status and a setter operation. Verify silent-peer failure near 70 seconds with a longer caller deadline, earlier caller deadlines, explicit cancellation after provider entry, transport failure with nil result, and finite handshake reads. Release and drain every fake. Checks: `go test -race ./internal/control/...`, `go vet ./...`, `go build ./cmd/pyry` (build output outside worktree). The verifier owns the full-module gate; no live Claude required.

## Open questions

None. The exact typed/wire names above settle this slice's contract; provider policy stays in #2758.

## Documentation handoff

Pending for documentation stage: `docs/knowledge/features/control-plane.md`, **Server Construction**, **Handshake Deadline** and the verb inventory: document `update.when-idle`, the shipped request/response shape, optional provider, required reason/tag fields, decision/error outcomes and the 70-second client/response-write bounds. State that acceptance is a scheduling decision and installation continues independently of the connection; distinguish I/O bounds from provider execution. At this stage the production daemon provider remains unwired.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Request` adds no update inputs, so binary paths, release URLs, version overrides and bypasses cannot enter the provider. `handleUpdateWhenIdle` and `UpdateWhenIdle` validate decision discriminants and required fields; errors win over results.
- [Tokens, secrets, credentials] This verb generates/stores no secrets. Generic provider errors prevent accidental error-detail exposure; no payload is logged.
- [File operations] Only the existing local socket is used, with `Listen`'s existing 0600 permissions; no binary/filesystem operation is added.
- [Subprocesses] No process is spawned; installation and restart belong to #2758.
- [Cryptography] No crypto/key/nonce operations or secret comparisons are added.
- [Network and I/O] The existing five-second handshake limit remains; new client exchange and response writes are bounded at 70 seconds. The inherited JSON decoder has no byte cap and the socket has no connection-count cap; broader same-user socket resource hardening is outside this contract. #2758 must bound provider metadata HTTP and keep asset work out of this response.
- [Errors, logs, telemetry] SHOULD FIX: a result returned alongside provider error must be discarded before constructing `Response`; malformed values must never be echoed. Implement fixed errors and test both failure projections.
- [Concurrency] Copy the provider under one lock, execute unlocked; the cancellation watcher exits after firing or is stopped on return. Tests release synchronous providers and drain handlers even when clients cancel.
- [Threat model] The same-user Unix socket is the only entry point; no relay/mobile verb is added. Release authenticity, eligibility and installation security remain with #2758.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
