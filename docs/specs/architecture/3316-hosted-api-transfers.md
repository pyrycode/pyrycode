# Bounded encrypted hosted APIs (#3316)

## Files read

- `internal/relay/v2session_hosted_validation.go` → `parseHostedRequest`, `hostedJSON`: exact JSON types, duplicates, correlation and forbidden metadata.
- `internal/relay/v2session_hosted.go` → `routeHosted`, `hostedDeliver`: pairing/capability gates and ordered Run-owned sealing.
- `internal/relay/v2session_hosted_resources.go` → resource worker, budget and retirement: shared admission, reservations, credit, deadlines and cleanup.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`: optional provider wiring; production readiness remains withheld.
- `internal/apps/api.go` → `ForwardAPI`, `prepareAPI`: once-only opaque forwarding, URL/header semantics and empty HTTP responses.
- `internal/apps/response.go`, `response_stage.go` → stable checked response and positive-source-read progress.
- Existing hosted asset, capacity, retirement and real-transport tests → reuse encrypted fixtures and disposal barriers.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Hosted discovery and ordered delivery: sealed cancellation must retire the connection.
- `docs/knowledge/features/apps-package-resource-serving.md` § Stable response ownership: reservations are not progress; EOF does not release response ownership.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Capability and envelope vocabulary, Asset/API fields and HTTP semantics, Bounded encrypted transfers and retirement, Errors and lifecycle test obligations: normative contract.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries and `CODING-STYLE.md`: test required/omitted/null fields and serialized envelopes.

## Context

Connect callable API forwarding to the landed encrypted resource lane without another transfer state machine. Production activation and real-daemon acceptance belong to #3248. No decision record is needed. No fetched feature branch overlaps the five planned production files.

Sizing: approximately 750 written lines (230 production, 430 tests, 90 plan), one exported provider interface, two optional consumer additions, four acceptance criteria and at most ten added reject branches. The verified grandchild chain #3123 → #3247 → #3316 and existing `needs-human:sizing` require building here if final coverage exceeds the line ceiling; hypothetical split seams are request validation/accounting and transfer/lifecycle proof, but progress plumbing serves only this transfer consumer.

## Design

Extend exact-key parsing to `app_api_request`, preserving common identity, envelope and optional-query checks. Require string method/body and object string-valued headers. Validate the entire standard padded base64 alphabet, padding and unused bits before decoded allocation; compute exact length and distinguish malformed encoding from request overflow. Ignore additive unknown fields. Pairing and negotiated hosting remain independent gates.

Add optional `HostedAppsAPI.ForwardAPI(ctx, serverID, appID, release, APIRequest)` implemented by `apps.Owner`, plus a private fake seam. Carry optional `APIRequest.Progress` into `ResponseInput.Progress`. The backend retains URL/method/header validation and once-only forwarding without retries or redirect following.

Generalize the existing asset worker to select asset/API preparation and the corresponding 8 MiB/1 MiB response limit. After shared admission, reserve API request bytes before decoding and retain ownership until worker disposal. Responses reuse checked metadata/read/completion, existing chunk-copy reservations, ordered output, one-chunk credit and independent control capacity. Response limit implies at most 32 API chunks. No production capability activation is added.

## Concurrency model

Run owns connection/resource maps, Noise and sender IDs. One admitted worker prepares/reads/disposes off Run, using the original connection inbox context, fixed 30-second deadline and atomic positive-read progress for five-second inactivity. Reservation callbacks do not report progress. All worker exits dispose response/request ownership before releasing admission; the shared budget mutex remains a leaf. Existing result and delivery guards suppress stale work and retire already-sealed cancelled output. No cancellation, disconnect or timeout replays a possibly committed mutation.

## State transitions and identity reuse

| Event | Race test |
|---|---|
| Binary request, metadata, multiframe or empty HTTP completion | `TestHostedAPITransfer`, `TestHostedAPIRealTransport` |
| Mixed admission/byte exhaustion, failed preparation and cancellation recovery | `TestHostedAPIMixedCapacity` |
| Positive/stalled preparation and fixed deadline despite progress | `TestHostedAPIProgress`, `TestAPIProgress` |
| Mutation committed then cancel/disconnect without replay; reused connection/request ID | `TestHostedAPIMutationReconnect` |
| Same-release backend retirement and stale credit/completion | `TestHostedAPIRetirement`, existing `TestAPIRetirement` |
| Cancel already-sealed blocked transport output | `TestHostedAPIRealTransportBlockedWriter` |
| Duplicate/invalid/retired credit | existing asset credit tests exercise the same shared worker/Run state machine |

## Error handling

Malformed body/fields or backend API semantics map to static `app.invalid_request`; decoded/request/response overflow maps to `app.limit_exceeded`. Shared admission/reservation refusal is retryable `app.busy` with retry-after one. Existing identity, release, unavailable, timeout and I/O mappings remain. Ordinary 200–599 HTTP results are response data; HEAD/204/205/304 have empty bodies. Never expose request/backend diagnostics.

## Testing strategy

Write failing exact-parser/body and API-progress tests first, then implement and add encrypted binary/HTTP/accounting/lifecycle proofs using existing fixtures. Run race tests for `internal/apps` and `internal/relay`, `go vet ./...`, and build `cmd/pyry` into scratch. The verifier owns the full-module suite; #3248 owns production acceptance.

## Open questions

None. Fake readiness attestation is test-only.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` § Hosted apps (v2): implemented API request-body validation, shared transfer/credit/cancellation and HTTP/empty-response semantics.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Hosted discovery and ordered delivery: API producers and aggregate request/response capacity ownership.
- `docs/knowledge/features/apps-package-resource-serving.md` § Stable response ownership: progress reporting through API preparation.
- Retain pending production capability activation/native assembly, transport-delivery versus app-success, and uncertain-mutation/no-replay wording.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Exact `parseHostedRequest` checks preserve pairing, safe IDs, app/release identity and forbidden metadata; `ForwardAPI` validates URL/method/header semantics before service I/O.
- [Tokens] Existing pairing revalidation enforces revocation at request/output admission; no new credentials or token logging.
- [Files] No new filesystem path or spool; backend generation ownership and stable memory staging are reused.
- [Subprocesses] No new launch or environment handling; only the existing private generation target receives requests.
- [Cryptography] Run exclusively seals ordered Noise output; failed/cancelled sealed delivery retires the connection. Existing SHA-256 covers decoded response bytes.
- [Network/I/O] Validate strict base64 and exact size before request allocation; reserve request/staging/chunk bytes in the shared budget. Fixed deadline and genuine read inactivity bound preparation and delivery.
- [Errors/logs] Static classifications omit bodies, paths, headers and backend diagnostics.
- [Concurrency] SHOULD FIX: retain request ownership until disposal and admission until response cleanup completes, including failed preparation and late completion; verify recovery with mixed operations.
- [Threat model] Relay blindness, paired host scope and no mutation replay are preserved. Native viewer isolation and production activation are OUT OF SCOPE for client tickets and #3248.

**Reviewer:** builder (self-review)
**Date:** 2026-10-13
