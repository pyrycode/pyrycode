# Bound question envelopes after escaping (#3109)

## Files read

- `internal/questionbridge/questionbridge.go` → `Parse`, `maxInputBytes`: raw resource bound and fail-closed mapping.
- `internal/questionbridge/questionbridge_test.go` → `TestParse`: exact mapping, ordering, rejection and captured 14-rune header coverage.
- `internal/questionbridge/registry.go` → `Record`, `Lookup`, `Snapshot`, `Resolve`: daemon identity and clone/one-shot contracts.
- `internal/protocol/questions.go` → `QuestionShownPayload`, `Question.MarshalJSON`: actual output keys and escaping.
- `internal/protocol/envelope.go` → `Envelope`: source/correlation fields and clear flag.
- `internal/protocol/thread_encoding.go` → `MaxThreadEnvelopeBytes`, `threadStampBudget`: plaintext cap and maximum timestamp/stamp widths.
- `cmd/pyry/daemon_control_live_test.go` → `TestControlLiveQuestionEnvelopeBound`: skipped accepted-boundary admission regression.
- `cmd/pyry/daemon_live_state.go` → `validDaemonLiveEnvelope`, `admitLocked`: full shown/clear admission backstop and session stamp.
- `cmd/pyry/modal_resolve_v2.go` → `Surface`, `surfaceQuestion`, `ApprovalAnswerable`: refusal fallback, correlation and answerability remain unchanged.
- `cmd/pyry/codex_runner.go` → `codexQuestionRequest`: shared parser acceptance and original answer correlation.
- `docs/knowledge/features/questionbridge-package.md` § Bounds and Lessons: reject whole batches, retain the observed header, preserve the raw guard before decode.
- `docs/knowledge/features/protocol-package-question-batch-payload.md`: header has no individual cap; content stays inert text.
- `docs/knowledge/features/streamsup-package-draining-turnevents-into-the-interactive-emitter-daemon-retained-live-state.md` § Control and on-demand readings: original answer IDs and clear semantics.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: derive the accepted producer boundary and marshal actual mapped envelopes.
- `CODING-STYLE.md` and `docs/protocol-mobile.md` § Security model: contract comments, bounded resources and inert untrusted content.

## Context

Raw HTML-sensitive characters expand sixfold during JSON encoding. A batch below
the raw 16384-byte limit can therefore exceed the 65519-byte plaintext envelope
cap and disappear at owner admission. One deliverable is producer acceptance that
guarantees deliverability, with the existing owner regression enabled. No decision
record is needed. No other fetched feature branch touches the planned files.

## Design

Retain `maxInputBytes = 16384` before decoding. After existing shape validation and
mapping, marshal the actual `QuestionShownPayload` with unset daemon IDs. Accept
only when its encoded size is at most `protocol.MaxThreadEnvelopeBytes - 4096`
(61423 bytes). Reserve 4096 bytes for two 36-byte IDs (432 bytes under worst-case
escaping), a 256-byte producing-session string (1536 escaped bytes), all envelope
keys, four maximum-width uint64 fields, the 35-character RFC3339Nano timestamp
with non-UTC offset, and boolean flags. Full-envelope tests independently verify
the reserve using actual protocol types. A `{}` clear is smaller than the shown
payload even after adding `session_state_cleared: true`.

Keep signatures and wire types unchanged. Return the mapped payload without
truncation or IDs; `Registry.Record` continues assigning them outside `Parse`.
Shrink comments on touched declarations to their current contract.

Sizing rechecked against this plan: about 350 written lines (production ~35,
tests/helpers ~220, plan ~95), zero new exported types/interfaces, zero consumer
migrations, three acceptance criteria, and two added rejection conditions (marshal
error or encoded oversize), within all five limits. Existing parser guards remain.

## Concurrency model

`Parse` remains synchronous and stateless, with only local mapping/encoding data.
No goroutines, locks or registry mutations are added.

## State transitions and identity reuse

| Event | Race-detector coverage |
|---|---|
| Largest accepted batch is recorded, repeatedly looked up and snapshotted, then resolved once and resolved again | `TestParseEnvelopeBoundaryRegistryIdentity` |
| Mapped accepted batch reaches owner admission with original batch ID | `TestControlLiveQuestionEnvelopeBound` |

Other session transitions and identity allocation retain their existing behavior;
the producer adds no lifecycle state.

## Error handling

Decode/shape/raw-size failures retain zero payload and false. Encoded oversize or
marshal failure also returns zero payload and false. No partial batch, logging,
fallback change or owner-only acceptance decision is introduced.

## Testing strategy

Write boundary tests and enable the owner regression first; observe oversized
accepted envelopes fail before implementation. Binary-search acceptance separately
for `<`, `>` and `&` in question text, header, label and description, and distributed
through a four-question/four-option batch. Check the largest acceptance and adjacent
zero-payload rejection, proving the encoded guard binds before the raw cap. Marshal
actual mapped shown and `{}` clear envelopes using fully escaped IDs/session,
maximum integer fields and longest timestamp. Compare accepted content and verify
registry identity through reads and one-shot resolution. Existing parser/capture,
answer/refusal and Codex question tests run with the touched-package race suites.
Run `go test -race ./internal/questionbridge/... ./cmd/pyry/...`, `go vet ./...`, and
`go build ./cmd/pyry` (output binary in scratch). The verifier owns the full-module
gate. No live Claude turn is required.

## Open questions

None: reserve correctness is checked against complete current envelope encoding.

## Documentation handoff

- Pending documentation stage: `docs/knowledge/features/questionbridge-package.md`, “Bounds”: distinguish the raw pre-decode limit from the full encoded-envelope guarantee, publish the implemented acceptance budget and metadata assumptions, and state that oversized input yields a zero payload and refusal/fallback without truncation.
- Pending documentation stage: `docs/knowledge/features/protocol-package-question-batch-payload.md`, header-cap note: reference both bounds rather than only the raw cap.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `Parse` bounds subprocess-authored question content after actual wire mapping; daemon IDs remain unfilled in its result. No content becomes authority.
- [Tokens, secrets, credentials] No new credential flow. `Registry.Record` retains crypto/rand batch identity; sizing neither mints nor logs an identity.
- [File operations] The implementation performs no filesystem operations; fixtures are hermetic in-memory values and the existing capture is read by existing tests.
- [Subprocesses] The implementation starts no subprocess and evaluates no content. Existing caller refusal/fallback paths retain their contracts.
- [Cryptography] No cryptographic changes. The bound uses the protocol plaintext cap before encryption; routing wrappers are outside that cap.
- [Network and I/O] The existing raw guard remains before `json.Unmarshal`; actual mapped encoding is bounded in memory by that guard. The 4096-byte reserve covers worst-case late metadata; tests marshal shown and clear envelopes independently.
- [Errors, logs, telemetry] Every new failure returns zero and false, without echoing question data. The package remains log-free.
- [Concurrency] Only local payload values and serialized bytes are introduced. Registry locking and one-shot resolution remain unchanged.
- [Threat model] The change addresses oversized question control frames (implementation/resource risk). Existing client inert-text rendering, relay authentication, encryption and replay controls remain their current owners' contracts.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-11
