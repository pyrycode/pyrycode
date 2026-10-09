# #2973 — Retain remote prompt answers

## Files read
- `cmd/pyry/modal_resolve_v2.go` → `Surface`, `ResolveStream`, question diagnostics: existing surface and parked-approval one-shots; preserve dismissal semantics.
- `cmd/pyry/question_resolve_v2.go` → resolution gate: authentication remains above bridge actuation.
- `cmd/pyry/relay.go` → `startRelayV2`: singleton store and session-harness wiring.
- `cmd/pyry/history_projection.go`, `conversation_history.go`, `legacy_runtime_receipts.go`: visibility, bounded pages and legacy exclusion.
- `internal/permbridge/permbridge.go` → `Resolve`, `AllowAlways`: winning delivery and effective session grant.
- `internal/questionbridge/registry.go`, `questionbridge.go`: bounded parsed batches and cloned surface context.
- `internal/history/log.go` → `SessionProvenance`, `AppendWithMetadata`: absent provenance and shown watermarks.
- `internal/e2e/realclaude/interactive_stream_modal_resolution_test.go`, `interactive_stream_question_answer_test.go`, `interactive_stream_question_refusal_test.go`: existing actual-resolution proofs and isolated daemon home.
- `docs/knowledge/features/history-package-producers.md`: best-effort append, captured provenance and transport-safe projection lessons.
- `docs/knowledge/features/permbridge-package.md`, `questionbridge-package.md`, `development-verification.md`, `CODING-STYLE.md`, ADR 042: arbitration, race-safe broadcast fixtures and evidence boundaries.

## Context
ADR 042 decision 6 requires saved answers while open prompts remain live state. This producer adds the missing remote-resolution fact; folding and local control-socket capture remain downstream. No new decision record is needed.
One deliverable; estimate approximately 740 written lines, no exported types/interfaces, fewer than ten simultaneous consumer updates, five acceptance criteria, fewer than ten new failure branches. No overlapping remote feature branches touch the planned files.

## Design
Add a private prompt-answer fact and safe projection in `cmd/pyry/prompt_answer_history.go`. Capture original conversation, asking routing ID, optional Claude/Codex provenance, permission tool/class or cloned questions when surfaced. Keep immutable ownership beside existing correlations under the bridge mutex, and remove it on retirement.
Wire the existing history store and session-harness lookup into the bridge after construction in `startRelayV2`; constructors remain compatible. `ResolveStream` and question diagnostics append only after `permbridge.Registry.Resolve` returns true. Cancellation carries a distinct operator decision through a private bridge entry point while preserving the existing deny verdict and resolver fallback.
The fact includes correlation, remote source, resolution timestamp, decision, effective verdict and applied session-grant boolean. Its context projection preserves question indices/text, multiselect, selected labels/descriptions or free text. Encode only these allowlisted fields; never serialize transport, tokens, permission input/context or grant rules. Bound serialized projection to 16 KiB, flag truncation and leave child replies untouched.
Classify the new fact as shown. Append directly without live/replay publication; the existing legacy type and receipt allowlists exclude it and preserve raw page boundaries.

## Concurrency model
No new goroutines. Existing manager resolution and handler retirement may race. Snapshot ownership under the bridge leaf mutex before resolving; append outside bridge and registry locks. Existing dismissal goroutines retain daemon-context shutdown behavior. A successful resolution can outlive surface retirement because its ownership snapshot is immutable. Writes are best effort, with no retry or crash-atomic delivery promise.

## State transitions and identity reuse
| Event | Race test |
| --- | --- |
| Surface then cursor/rebinding/agent change | `TestPromptAnswerHistoryPermission` and `TestPromptAnswerHistoryQuestion` |
| Answer followed by duplicate/cancel, or refusal followed by duplicate | same tests: exactly one durable fact |
| Expiry/teardown before answer; approval lost after question validation | `TestPromptAnswerHistoryLosingResolution` |
| Retirement after winning answer | permission/question tests: saved ownership survives retirement |
| Reuse parked tool ID after retirement | `TestPromptAnswerHistoryPermission`: fresh surface owns fresh fact |
| Nil/failed store after winning resolution | `TestPromptAnswerHistoryStorage` |
Open surface, disconnect/retirement and reconnect snapshots never append.

## Error handling
Unauthorized/invalid/stale/unrouted resolutions preserve existing gates. A losing parked resolution retains existing dismissal behavior but writes nothing. Nil storage is silent; failed storage uses `appendConversationHistory` fixed failure discriminants. Remove forged option content from the invalid-option log. No answer content or raw errors reach operational logs.

## Testing strategy
Write deterministic tests first through real production resolvers/bridge with the real history store and synchronized broadcasters. Cover Claude/Codex permission allow/deny/cancel, applied/unavailable grants, question answer/refusal, multiselect/free text, provenance absence, losing resolution, storage failures and serialized escaping/truncation without child changes. Check shown metadata and warm/reopened watermarks, legacy pages/receipts and unchanged surface/dismissal publication. Extend the four named live round trips to inspect raw durable facts using the existing daemon home.
Run focused tests, `go test -race ./cmd/pyry/...`, `go vet ./...`, `go build -o /tmp/builder-2973/pyry ./cmd/pyry`, and compile/vet `internal/e2e/realclaude` with `e2e_realclaude`. Full-module and live execution belong to dispatcher gates; live counts remain pending.

## Open questions
None. A selected option is identified by the existing answer value matching its surfaced label; unmatched values remain free text. Unknown agent provenance remains absent.

## Documentation handoff
Pending documentation stage: in `docs/knowledge/features/history-package-producers.md`, “Producers (#2114, #2115)” (linked by `history-package.md`), document saved-answer fields, original ownership/provenance, winning-approval boundary, open-prompt exclusion, denial/cancel/refusal handling, 16 KiB projection cap and security omissions. State writes are best effort, facts are shown for the future fold but excluded from legacy transports, and capture covers remote resolution rather than local control-socket approvals.

## Security review
**Verdict:** PASS
- Trust boundaries: existing modal/question resolvers authenticate and validate; persistence additionally requires the parked registry's winning result.
- Tokens/credentials: projection is an allowlist with no token, device, permission-context, grant-rule or tool-input fields.
- File operations: reuse the existing validated history store, modes and symlink protections; no new path construction.
- Subprocesses/cryptography: no new child process, shell, key or nonce handling; child verdict bytes remain unchanged.
- Network/I/O: reuse existing frame limits; explicitly cap escaped JSON projection to 16 KiB.
- Logs: SHOULD FIX addressed by design: invalid-option logging currently echoes forged content; retain fixed discriminants only. Storage failures reuse content-free classification.
- Concurrency: ownership snapshot before actuation and winning Resolve guard avoid retirement races and losing-answer facts; no nested locks or new goroutines.
- Threat model: existing authenticated encrypted relay and authorization contracts remain; crash-atomic child delivery and future fold are outside this slice under ADR 042.
**Reviewer:** builder self-review. **Date:** 2026-10-09.

## Revisions
- 2026-10-09: `ResolveCancel` preserves its existing nil-device deny/dismissal behavior. Self-review found that this compatibility path must not invent an authenticated remote operator: suppress its answer fact while keeping child delivery unchanged. `TestPromptAnswerHistoryUnauthenticatedCancel` covers it. `TestPromptAnswerHistoryRetirement` additionally forces question-owner retirement before parked resolution and races permission retirement after delivery.
