# Generation-bound stable responses (#3257)

## Files read

- `internal/apps/health.go` → `Owner.usableLocked`, `Target.valid`, `targetBody.Read`: registry-then-owner validity and post-read retirement checks.
- `internal/apps/owner.go` → `generation`, `Stop`, `Shutdown`: generation cancellation and process-exit ownership.
- `internal/apps/reconcile.go` → `reconcileRecords`: metadata preserves ownership; release/intent changes retire it.
- `internal/apps/registry.go`, `lifecycle.go` → `List`, `Subscribe`, `notify`: committed identity, synchronous nonblocking callbacks and detached records.
- `internal/apps/health_test.go` → `healthFixture`, `healthTarget`, `TestOwnerTargetRetirement`, `TestOwnerTargetReuse`: controlled process and restart fixtures.
- `docs/knowledge/features/apps-package.md` § Lifecycle and restart state: revision changes alone must not invalidate a healthy generation; retained targets need checks after reads.
- `docs/knowledge/features/development-verification.md`, `CODING-STYLE.md`: behavioral race tests and bounded source/evidence checks.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Asset/API fields and HTTP semantics, Bounded encrypted transfers and retirement, Errors and lifecycle test obligations: shared response contract.

## Context

Assets and API forwarding need one stable-body primitive whose lifetime cannot escape its authenticated host/app/release/process binding. This adds preparation only; callers own delivery policy. No additional decision record is needed.
Sizing: one deliverable, five acceptance criteria, about 740 written lines (330 production, 340 tests, 70 plan), three exported types, zero consumer migrations, eight failure classes, no new owner lifecycle states. No fetched feature branch overlaps the new response files.

## Design

Add `response.go` and `response_stage.go` in apps. `Owner.Prepare(ctx, serverID, appID, release, ResponseInput)` resolves committed local accessibility, release and a usable generation before staging. Unknown/removed/foreign hosts return `ErrNotFound`, release mismatch returns `ErrReleaseChanged`, and absent readiness returns `ErrUnavailable`. Existing target callers stay unchanged.
`ResponseInput` supplies identity-encoded `io.ReadCloser`, size hint (-1 unknown), decoded limit, status, headers and caller reservation/release callbacks. Source ownership transfers on invocation; callbacks must be prompt and must not reenter the result. Every failure closes the source. Positive reservations are atomic all-or-nothing; release returns precisely previously acquired capacity.
`PreparedResponse.Metadata` returns detached `ResponseMetadata` with status, headers, exact size and lowercase SHA-256; `Read` provides serialized checked consumption. `Complete(deliveryErr)` reports successful or failed delivery and disposes; `Close` is idempotent disposal. EOF alone retains capacity until delivery completion. Consumers must not use earlier copied metadata after retirement; their transport must obey its own cancellation boundary.
Stage immutable memory chunks, reserving before allocation/read. Reserve the declared size before any body allocation/read and reserve incremental growth before additional reads. Fill chunks rather than retaining a chunk per short read. Read through EOF, including a reserved one-byte overflow probe at the decoded limit; this probe may need one byte of temporary capacity even for an exact-limit body. Release unused reservations at EOF and hold exactly the body size until disposal. No private disk spool is created.
Validate status 200–599 and lowercase allowlisted headers, valid control-free UTF-8 values <=1024 bytes, <=8 entries and <=4096 JSON bytes, accepting an empty object. Copy headers before staging. Consumer-specific status/no-body policy stays outside apps.

## Concurrency model

One watcher per owned preparation selects caller cancellation, generation cancellation, process exit or completion. A registry subscription cancels the preparation on incompatible committed changes without taking result/owner locks; subscription precedes resolution to close the registration race. The watcher closes the source before taking the staging mutex, unblocking a pending read. Sources must allow concurrent Close to unblock Read (or independently obey caller cancellation). Source closure uses a separate once guard; result cleanup is serialized and idempotent, removes subscriptions, drops chunks and returns capacity. Result operations take result then registry then owner locks; registry callbacks only cancel. Completion stops and joins the watcher.

## State transitions and identity reuse

| Event | Race test |
|---|---|
| Unknown, removed, foreign host, mismatched release, absent healthy owner | `TestResponseResolution` |
| Removal/release replacement/stop/exit/generation cancellation during staging or after partial consumption | `TestResponseRetirement` |
| Same-release stop/start cannot revive retained response | `TestResponseRestart` |
| Metadata-only commit preserves healthy prepared response | `TestResponseStable` |
| Caller cancellation, concurrent consumption, repeated completion/close | `TestResponseCancellation`, `TestResponseDisposalRace` |

## Error handling

Return static sentinels for inaccessible app, release change, unavailable generation, invalid metadata, decoded limit, reservation refusal and source/delivery I/O; caller cancellation preserves standard context errors. Never wrap caller/source errors containing native paths, endpoints or content. Ordinary HTTP error status is successful preparation. Reject invalid sizes/limits/callbacks as invalid metadata. Zero-progress readers fail as I/O after a bounded number of reads.

## Testing strategy

Write tests first and observe missing API failures. Reuse controlled health processes and readers/budgets to prove empty/binary/HTTP-error stability, exact/oversized/unknown/growing bodies, capacity refusal before reads, metadata byte/UTF-8/control bounds, cleanup on read/close/delivery errors, blocked-source cancellation, lifecycle retirement and concurrent disposal. Run `go test -race ./internal/apps`, `go vet ./...`, `go build -o /tmp/builder-3257/pyry ./cmd/pyry` and diff checks. The verifier owns the full-module gate; no live-Claude or relay proof is required.

## Open questions

None.

## Documentation handoff

Pending for the documentation stage: `docs/hosted-apps.md` § Client resource bridge / Asset/API fields and HTTP semantics, and `docs/knowledge/features/apps-package.md` § resource-serving: describe generation-bound stable response preparation, reservation/disposal ownership and cancellation, including source-unblocking and delivery-failure caller obligations. Preserve the family handoff for confined callable assets and navigation behavior when #3258 lands; those remain pending at this child's landing. State paired routing/capability activation is still pending.

## Security review

**Verdict:** PASS
**Findings:**
- Trust boundaries: `Prepare` resolves authenticated caller identity against committed registry state; it does not authenticate connections. Header validation and cloning establish the metadata boundary.
- Tokens/secrets: no credentials are created or stored. Static sentinels suppress source/callback/delivery diagnostic content.
- File operations/subprocesses: memory-only staging introduces neither paths nor process execution; existing owner process confinement is preserved.
- Cryptography: standard SHA-256 fingerprints complete bytes, including empty bodies; this is integrity metadata, not authentication.
- Network/I/O: SHOULD FIX: reserve the EOF overflow probe before reading it; otherwise an exact-limit growing reader could bypass reservation coverage. Implement the reserved probe and test refusal without further I/O.
- Errors/logging: no request content or native diagnostics enter returned failures; no new content logging.
- Concurrency: cancellation must close the source before waiting for its read mutex, and subscriptions must never acquire result locks. Tests must hold a read blocked until Close to expose inversion.
- Threat model: OUT OF SCOPE: transport ceilings/credit/deadlines/authentication and late-wire-delivery suppression belong to #3247; asset confinement to #3258; API projection to #3246.
**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12

## Revisions

2026-10-12: Chunk filling uses `io.ReadFull` behind an error-sanitizing, zero-progress-bounded reader: raw source `io.ErrUnexpectedEOF` must remain an I/O failure, while EOF-derived short reads finish staging normally. Source Close must promptly unblock Read on every retirement; caller-only cancellation is insufficient. Same-release restart coverage is consolidated into `TestResponseRetirement/stop` to reuse its blocked-source and partially consumed result assertions. Final sizing: 800 written lines, three exported types, zero consumer migrations and no new owner lifecycle states.

2026-10-12 (verifier finding 1): `io.ReadFull` discards even sanitized source errors when their accompanying bytes fill a chunk. `PreparedResponse.stage` records non-EOF read failures independently and rejects the chunk before retaining it. `TestResponseIOAtChunkBoundary` covers raw `io.ErrUnexpectedEOF` and generic errors at known-size and incremental chunk boundaries, asserting sanitized `ErrResponseIO`, no prepared result, no further reads, one source close and complete reservation release. EOF-derived short reads remain successful; ownership and lifecycle checks are unchanged. Security re-review: the failure marker retains no source diagnostics and introduces no additional I/O, resources, goroutines or trust boundary.
