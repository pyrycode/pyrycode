# Question-control receipt and diagnostic outcomes (#2800)

## Files read

- `internal/relay/v2session_question.go` → `handleQuestionAnswer`, `handleQuestionRefusal`: nil-before-decode, reject-on-error, and no-reply behavior.
- `internal/relay/v2session_seams.go` → `QuestionResolver`, `V2SessionConfig.QuestionResolver`: preserve the bool-only consumer contract and wiring.
- `internal/relay/v2session_question_test.go` → `startQuestionConn`, `fakeQuestionResolver`: reuse authenticated sealed-frame tests and no-broadcast proofs.
- `internal/relay/v2session_test.go` → `bufferLogger`, `syncLogBuffer`: synchronized TextHandler capture.
- `cmd/pyry/question_resolve_v2.go` → `questionResolverV2.ResolveAnswer`, `ResolveRefusal`: eligibility precedes consume; leave daemon migration to #2801.
- `cmd/pyry/relay.go` → `startRelayV2`: existing resolver remains wired through the original field.
- `docs/knowledge/INDEX.md`, `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: scoped verification, content sentinels, and observable ordering.
- `docs/knowledge/features/v2-session-manager.md` and `v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md`: partial decode must call no resolver; valid `null` reaches it; dismissal has one daemon owner.
- `docs/protocol-mobile.md` § Security model: authenticated control, confidential payloads, and unchanged Noise boundaries.

## Context

Phone answers sometimes leave Claude's question pending without a dismissal. Relay logs must distinguish receipt from resolution and expose content-free resolver outcomes. This slice provides observability only; #2801 migrates the daemon resolver and #2802 owns diagnosis, repair and live proof. No decision record is needed.

## Design

Keep `QuestionResolver` and `V2SessionConfig.QuestionResolver` unchanged. Add optional `DiagnosticQuestionResolver` with `ResolveAnswerDiagnostic` and `ResolveRefusalDiagnostic`, each accepting the same typed payload and device and returning `(consumed bool, reason string)` from one resolution attempt. Implementations must supply stable, content-free reason codes, never interpolated input or errors, and retain the existing validation, eligibility-before-consume and bounded-time obligations.

Both handlers emit `v2.question.received` at Info on entry with `frame_kind` and `conn_id`, without a batch ID. Each defers one `v2.question.completed` Info record, with the same identifiers and a `reason`. Add `question_batch_id` only after decode succeeds, including an empty decoded identifier. Use structured slog fields to escape identifiers. Replace all previous outcome records.

Terminal reasons are `no_resolver`, `decode_rejected`, `resolved`, and `legacy_not_consumed`; diagnostic non-consumption preserves the supplied reason. A consumed diagnostic attempt always reports `resolved`. After successful decode, select the diagnostic interface when implemented; otherwise invoke the original method. Never invoke both, and emit no reply or dismissal.

Concurrent #2796 only adjusts an unrelated configuration comment in `v2session_seams.go`; keep additions local and build through that overlap.

Sizing: one deliverable, three acceptance criteria, approximately 400 written lines including tests and this plan, one new exported interface, zero required consumer migrations, and four handler outcome branches per frame kind (eight total). This remains below all five limits; #1984's larger implementation established the harness already reused here.

## Concurrency model

Handlers continue running inline on the manager's existing dispatch goroutine. No new goroutines, locks, queues or shutdown paths. Test callbacks observe receipt before resolver actuation using the existing synchronized log buffer.

## Error handling

Nil resolver remains before parsing. Decode failure rejects without calling either resolver path or logging the partially populated batch identifier, payload or decoder error. Clean `null` and `{}` still reach the wired resolver. Resolver reasons only inform logging, never authorization, audit verdicts or dismissal.

## Testing strategy

Write tests first and observe failures against the old receipt/outcome behavior. Extend the sealed-frame harness with legacy and diagnostic consumed/non-consumed cases for both kinds, exact two-record/Info/identifier/reason assertions, receipt-before-actuation callbacks and selected-path call counts. Cover nil with valid and invalid-shape payloads, partial decode failure, and both zero-value payload shapes. Scan for sentinel question text, labels, answer values/tokens, raw payload and decoder-error text; verify escaped identifiers. Retain existing no-reply/no-broadcast checks.

Run `go test -race ./internal/relay/...`, `go vet ./...`, and `go build ./cmd/pyry` (output binary outside the worktree). The verifier owns the full-module gate.

## Open questions

None. Diagnostic reason vocabulary beyond relay's fixed outcomes is owned by #2801.

## Documentation handoff

Pending documentation stage: update `docs/knowledge/features/v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md` under a `Diagnostic logging` heading to describe receipt and terminal Info records, safe identifier handling, legacy fallback and diagnostic reporting without a second resolution or broadcaster. Reconcile the existing `Logging` description with the new levels and records. State that daemon-specific reasons arrive with #2801.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Handlers still reject decode errors and forward only typed payloads; decoded identifiers remain untrusted correlation fields, escaped by slog, not authorization evidence.
- [Tokens, logs] SHOULD FIX: tests must reject answer tokens, question text, option labels, answer values, raw payload and decoder errors on every outcome; log only fixed metadata, successfully decoded batch identifiers and implementation-owned content-free reason codes. The optional interface explicitly forbids deriving reasons from client content or errors.
- [File operations, subprocesses] No new filesystem or child-process operations; handlers only decode, call the existing seam and log.
- [Cryptography] Existing handshake and AEAD verification stay unchanged; no new keys, random values or secret comparisons.
- [Network and I/O] Existing Noise per-message cap (`maxNoisePayloadBytes`, 65535) and authentication remain upstream; no new network reads, replies or broadcasters. Receipt/terminal writes use the injected logger.
- [Concurrency] No new goroutines or state mutation; diagnostic selection makes one attempt and retains bounded-time resolver obligations.
- [Threat model] Prompt injection, server-ID race, MITM, token compromise, replay and static-key protections remain unchanged. Implementation-bug risk is addressed by sealed-frame and confidentiality tests. Rate limiting remains the protocol's deferred DoS posture; #2802 owns the observed hang investigation and live proof.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-04
