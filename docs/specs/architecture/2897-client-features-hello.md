# Optional client feature self-report in hello (#2897)

## Files read

- `internal/protocol/handshake.go` → `HelloClientPayload`: plain-data fields and omission conventions.
- `internal/protocol/handshake_test.go` → `TestHelloClientPayload_RoundTrip`, `TestHelloClientPayload_CapabilitiesRoundTrip`: existing fixture stability and optional-field assertions.
- `internal/protocol/envelope_test.go` → `canonical`, `readFixture`: byte comparisons compact JSON without changing field order or escaping.
- `internal/protocol/testdata/hello_client.json`: legacy hello bytes that must remain unchanged.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit`: decodes the DTO; negotiates only `Capabilities` and retains selected identity fields by value.
- `internal/transport/wssclient.go` → `maxFrameBytes`, `realDial`: existing 1 MiB WebSocket read limit.
- `docs/knowledge/features/protocol-package.md` and `protocol-package-handshake-control-payloads.md`: pure-data boundary, optional strings, declaration-order stability.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: assert decoded values and remarshal the DTO; raw-payload identity alone is insufficient.
- `docs/protocol-mobile.md` § `hello` (v2-specific note), Security model: additive handshake vocabulary and untrusted prompt content.
- `CODING-STYLE.md`: table-driven stdlib tests and Go formatting.

## Change

Add `ClientFeatures string` with JSON tag `client_features,omitempty` beside `DeviceName` and `ClientVersion` in `HelloClientPayload`. It is optional self-reported plain text within v2, unrelated to negotiated capabilities. Preserve decoded strings verbatim, including whitespace and control characters; absent and empty values both mean no description and omit the key when marshalled. Document the trust boundary in the field comment. No new type, state, goroutine, validator, handshake gate or consumer is needed; #2898 owns retention and prompt admission. No overlapping feature branches touch either implementation file. This is one deliverable, estimated at about 120 written lines including this security-reviewed plan and tests: zero new exported types/interfaces, zero required consumer updates, two acceptance criteria, zero new reject branches, all below the sizing limits.

## Testing strategy

Add a table-driven `TestHelloClientPayload_ClientFeaturesRoundTrip` before implementation. Decode absent, empty, descriptive Unicode/markup, whitespace-only and control-character cases, compare the complete expected DTO, inspect the marshalled key for omission or verbatim value, and decode it again. Include capabilities separately so the self-report cannot substitute for their advertisement. Existing `TestHelloClientPayload_RoundTrip` and other hello fixtures continue to prove byte stability. Watch the new test fail before adding the field. Run `go test -race ./internal/protocol/...`, `go vet ./...`, and `go build -o /tmp/builder-2897/pyry ./cmd/pyry`; the dispatcher owns the full-module gate.

## Documentation handoff

- Pending documentation stage: in `docs/protocol-mobile.md`, under `hello` (v2-specific note), describe `client_features` as optional self-reported plain text, empty by default, additive within v2, and unrelated to negotiated capabilities. Mark prompt consumption pending #2898.
- Pending documentation stage: update the `HelloClientPayload` listing in `docs/knowledge/features/protocol-package-handshake-control-payloads.md` accordingly.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `HelloClientPayload.ClientFeatures` remains an untrusted client claim after `handleNoiseInit` decodes it. The field comment will state provenance and separation from `Capabilities`; no consumer promotes it to trusted instructions.
- [Tokens, secrets, credentials] No credential lifecycle changes. `Token` stays separate; the implementation adds no logging, storage or whole-payload retention.
- [File operations] The DTO and JSON tests perform no new production filesystem operations or path interpretation.
- [Subprocesses] The string is not passed to a subprocess or shell; prompt consumption is deferred to #2898.
- [Cryptography] Existing Noise early-data handling is unchanged; the field introduces no keys, nonces, randomness or comparisons.
- [Network and I/O] `realDial` retains the `maxFrameBytes` 1 MiB WebSocket cap. This field adds no socket reads or per-field limits; content admission is outside this DTO contract.
- [Errors, logs, telemetry] No new diagnostics reflect the self-report; JSON decoding follows existing structural string typing without content validation.
- [Concurrency] No new goroutines, locks or shared mutable state; ordinary DTO values follow existing ownership.
- [Threat model] OUT OF SCOPE: Security model threat 1 (prompt injection) requires admission and attributed rendering when consumption lands in #2898. This slice accepts data without consuming it; threat 3's Noise confidentiality and authentication gates remain unchanged.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-06

## Revisions

- 2026-10-06 — Verifier finding 1 on `7eb54069`: repair only
  `TestActiveSessionStarter_ResetReleasesForTheNextFrame` in
  `cmd/pyry/new_session_reset_test.go`. `asyncRunner.RestartFresh` signals its
  rotation before `activeSessionStarter.resetThenRotate` returns and runs its
  deferred `conversationReset.release`; a second frame in that window is correctly
  dropped. After each rotation, poll `conversationReset.begin` until it accepts a
  fresh claim, release that probe claim, then send the next frame. The bounded wait
  still fails if the guard is never released. The production reset and hello
  contracts stay unchanged; the original security review still applies.
  `cmd/pyry/main.go` (`resetThenRotate`, `start`) and `cmd/pyry/session_reset.go`
  (`begin`, `release`) establish this ordering; the new-session seam overview and
  `development-verification.md` § Prove that tests distinguish the change warn
  against treating an early asynchronous observation as completion. Check the
  named regression 50 times normally and with a delayed `RestartFresh` overlay;
  remove the tail's deferred release in another overlay to prove the repaired test
  still rejects a stuck guard. Run race tests for `cmd/pyry` and `internal/protocol`,
  vet, build, staticcheck and the three static guards; the dispatcher owns the
  full-module and fake-daemon gates. No feature-branch overlap touches this test.
