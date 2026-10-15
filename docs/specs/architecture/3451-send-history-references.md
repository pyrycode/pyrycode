# Accepted sends from history references

## Files read

- `internal/thread/sends.go` → `sendFact`, `deliveredSendContent`: lifecycle validation, claim atomicity and sender-field merge contract.
- `internal/thread/fold.go` → `addItem`, `Feed`: metadata, inline compatibility and version consumption.
- `internal/thread/render.go` → `contentRecipe`, `renderShared`, `recentSource`: shared enrichment and bounded source resolution.
- `internal/thread/main.go` → `setContent`: inline enrichment compatibility.
- `internal/thread/observations.go` → `FeedChanges`, `recordShown`: detached changes, touches and shown versions.
- `internal/thread/render_test.go` → `testStoreReferenceRetry`, delivery failure/eviction cases: source fault injection and repair.
- `internal/thread/render_work_test.go` → reference/legacy incremental comparison pattern.
- `internal/thread/corpus_golden_test.go` → `testFoldGoldenCheck`: unchanged generated/shadow expectations and all six partitions/modes.
- `internal/thread/sends_queue_handle_test.go` → acceptance-owned presence and case variants.
- `docs/knowledge/features/thread-package-main-thread-folding.md` § Accepted sends and durable outcomes: stable internal indexes and receiving provenance must survive suppression.
- `docs/knowledge/features/thread-package.md`, `thread-package-background-store.md`: production installation, snapshots and retry ownership.
- `docs/knowledge/features/development-verification.md`: compare every field and keep source errors generic.
- `docs/knowledge/decisions/045-thread-index-over-history.md`, `CODING-STYLE.md`: index-only target and implementation conventions.

## Context

Production folds still copy accepted-send content and summaries, including merged delivery and terminal payloads. Complete this one lifecycle conversion using the shared renderer. Legacy `New` keeps inline storage. Agent/shell and inherited endings, Store publication retention (#3386), and resumable state (#3335) remain separate. No new decision record is needed.

Sizing: one deliverable, approximately 390 written lines including tests and plan; zero new exported types, zero consumer signature changes, four acceptance criteria and fewer than ten lifecycle reject branches. This remains below every builder limit. No other remote feature branch currently touches the planned files.

## Design

Allow accepted user messages through `addItem` reference conversion. Keep only existing lifecycle metadata and recipe IDs; no retained send Content, text-derived Summary or recipe payload literals.

Extend `contentRecipe` with a delivery entry ID. `renderShared` resolves it through the existing source together with acceptance and outcome IDs. Apply unchanged `deliveredSendContent` to acceptance/delivery bytes before shared outcome enrichment. Derive summaries from acceptance text for queued/drop/loss and delivery text for delivered sends. Preserve exact legacy JSON encoding, unknown fields and sender case precedence.

Before a delivery claim mutates either row, render acceptance and delivery candidates to establish source availability. Only legacy folds retain the merged result/summary; reference folds record the delivery ID. Receiving provenance remains the previously ingested message metadata. Terminal outcomes use `referenceField` entry IDs in production and `setContent` in legacy mode. Recipes remain copy-on-write and compatible with immutable captured views.

## Concurrency model

No goroutines or locks added. The Fold owner continues to serialize access; Store owns worker lifetime and Retry. Recipe field maps use existing copy-on-write updates, and scalar recipe updates replace map values.

## State transitions and identity reuse

| Event | Required behavior | Race test |
| --- | --- | --- |
| Acceptance then delivery, including eviction between events | Permanent acceptance identity, delivery order/provenance, suppressed separate message identity | `TestReferenceSendRendering`, `TestReferenceDeliveryEvicted` |
| Drop/loss, duplicate/conflicting terminals, reused queue handle/app ID | First valid terminal wins; original visibility, acceptance content and provenance | `TestReferenceSendRendering`, existing send lifecycle tests |
| Invalid links or failed acceptance/delivery resolution | No partial claim or suppression | `TestReferenceDeliveryFailure`, `TestReferenceSendSourceFailure` |
| Eviction then unavailable acceptance/delivery/outcome, repair and Retry | Entire candidate withheld, fresh replay restores usable view | `TestReferenceSendSourceFailure`, `TestStoreReferenceSendRetry` |
| Repeated rendering and mutation of returned content | Returned items remain detached | `TestReferenceSendRendering` |

## Error handling

Missing/unreadable sources return only `ErrUnavailable`; observations/Store withhold all content and usable watermarks. Reject malformed source JSON before merge, avoiding partial content or a nil-map merge. Invalid history facts retain existing version-only behavior. Store Retry rebuilds from repaired authoritative history.

## Testing strategy

Write focused send retention/equivalence and source-failure tests first and observe the retention assertion fail. Compare complete Items, observations/live changes, touches and versions against legacy after every lifecycle event and after eviction, using varied sender casing, queue presence, provenance and escaping. Reuse Store failure injection for all three referenced entries, replay/tail repair and Retry. Existing query/wire and generated/shadow golden suites run unchanged; do not regenerate fixtures or alter the shared retention accounting.

Run focused tests, `go test -race ./internal/thread`, `go vet ./...` and `go build ./cmd/pyry`. Per builder role, the verifier owns `make check` and the full-module gate requested by the ticket. All evidence is offline.

## Open questions

None.

## Documentation handoff

Pending for documentation stage: update `docs/knowledge/features/thread-package-main-thread-folding.md`, **Accepted sends and durable outcomes**, to replace merged-inline/copied-outcome claims with history-backed send-merge recipes and render-time summaries, retaining the existing join, queue-handle, receiving-provenance and atomic failure/retry rules.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `sendFact` retains existing typed validation and earlier-entry joins. `renderShared` treats history as inert JSON, validates merge inputs and withholds the whole candidate on failed resolution. SHOULD FIX: cover malformed lookup payloads as well as missing IDs so a nil delivery object cannot panic in `deliveredSendContent`.
- Tokens, secrets and credentials: no generation or credential APIs; payloads remain display data and are never logged by this change.
- File operations: no new paths or writes; IDs resolve through the existing conversation-bound source, not payload-derived paths.
- Subprocesses and cryptography: no subprocess or cryptographic operations added or changed.
- Network and I/O: no network admission changes; preserve current-chunk/recent-cache/history lookup and its inclusive 1 MiB cache bound.
- Errors, logs and telemetry: all source errors collapse to `ErrUnavailable`; tests inject private error text and require no partial output or disclosed source details.
- Concurrency: no new workers; recipe maps remain copy-on-write and public content detached.
- Threat model: existing history ownership and rendering boundary apply. OUT OF SCOPE: residual agent/shell content and Store publication retention belong to the sibling lifecycle work and #3386.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-10-15

## Revisions

- 2026-10-15: Source-failure tests also inject malformed JSON and null objects before delivery claims and after eviction. Shared rendering now rejects non-object referenced enrichments and user-message sources with the generic unavailable result, addressing the security review's SHOULD FIX finding.
- 2026-10-15: Store Retry covers acceptance/delivery failures in replay and tail, and outcome failures in replay. An immutable outcome's only terminal tail publication resolves it from the current chunk; ignored tail entries do not reread unchanged publications. Fold rendering separately tests outcome failure after eviction. The reuse helper defers send outcomes until after tail eviction rather than injecting an unreachable tail lookup fault.
