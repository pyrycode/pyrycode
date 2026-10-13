# Bounded encrypted hosted assets (#3315)

## Files read

- `internal/relay/v2session_hosted.go` → `routeHosted`, `hostedDeliver`, `retireHosted`: authenticated discovery, ordered sealing and cancellation of sealed output.
- `internal/relay/v2session_hosted_validation.go` → `parseHostedRequest`, `hostedJSON`: exact-key JSON, duplicate and envelope validation.
- `internal/relay/v2session_hosted_changes.go` → `hostedInbox`, `releaseHosted`: shared destination accounting and generation retirement.
- `internal/relay/v2session.go` → `Run`, `V2SessionManager`: single owner of sessions, IDs and Noise.
- `internal/relay/v2session_seams.go` → `V2SessionConfig`: optional providers; production readiness remains withheld.
- `internal/relay/v2session_hosted_transport_test.go` → `testHostedRealTransport`: encrypted transport and blocked-pump proof.
- `internal/apps/assets.go` → `Owner.PrepareAsset`: confined URL resolution and generation binding.
- `internal/apps/response.go`, `response_stage.go` → `PreparedResponse`, `stage`: checked stable response, EOF-probe reservation, disposal ownership.
- `internal/apps/manifest.go` → `validRelease`: canonical bounded release grammar.
- `internal/protocol/hosted_apps.go` → resource DTOs: metadata, chunk, credit and scoped cancellation shapes.
- `docs/specs/architecture/3120-hosted-app-contract.md` → capability/envelope, assets, bounded transfers, errors: normative contract.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Hosted discovery and ordered delivery: never continue a connection after failed sealed output; retain slots until readers exit.
- `docs/knowledge/features/apps-package-resource-serving.md` § Stable response ownership: EOF keeps body reservations; preparation progress cannot be inferred from reservation callbacks.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries and `CODING-STYLE.md`: prove omission on decrypted bytes, preserve bounded source and file scopes.

## Context

Assets already have stable, confined backend preparation but no authenticated relay producer. Add that producer and reusable resource budgets for dependent API routing #3316. Production activation and real daemon/relay proof belong to #3248. No new decision record is needed.

Sizing: approximately 1,250–1,450 written lines including production, focused tests and this plan; one exported provider interface, no required consumer migrations, five acceptance criteria. The state machine groups rejection into invalid input/credit, admission/capacity, backend failure, timeout and retirement. The 800-line ceiling is exceeded; verified parentage is #3123 → #3247 → #3315 and existing `needs-human:sizing` records the mandatory build-in-place exception. A standalone capacity/progress slice would serve only this sibling, violating the split floor. The hypothetical behavioural split is asset delivery first and exhaustive lifecycle/capacity proofs second; build both here under the exception. Recount before implementation: this plan changes no other limit. Feature #3240 adds unrelated permission-observation fields to the manager; keep shared-file edits local and additive.

## Design

Add optional `HostedAppsAssets.PrepareAsset(ctx, serverID, appID, release, AssetRequest)` provider, implemented by `apps.Owner`; nil fails resources explicitly. A private response/provider seam supports complete fake dependencies in tests. Resource identity includes the original session/inbox pointer, client request ID and app ID. Backend preparation pins the exact process generation. Add `PreparedResponse.Done()` for nonblocking retirement observation and optional positive-byte `Progress` callbacks on asset/response inputs, invoked for every underlying source read even inside `io.ReadFull`.

One worker prepares and checks metadata, sends it through Run, then reserves/copies/encodes at most one chunk, hands it to Run and awaits transport completion before another. Metadata contains exact status/headers/size/digest and ceiling-divided chunk count. Empty results end at metadata. Workers use checked Metadata/Read/Complete/Close. Every hosted plaintext envelope is measured before encryption.

A leaf resource-budget mutex owns admission counts (8/connection, 32/app, 128/daemon) and staged/copy bytes (16 MiB/connection, 64 MiB/daemon). Reservation callbacks acquire atomically before allocation/read; backend staging retains its EOF probe and complete-body reservation. Operation counts remain held until worker disposal finishes. Resource outputs get at most eight separate destination slots, while existing control/ordinary/callback accounting retains sixteen. Both share the existing FIFO and yield after each output.

Run owns active operation maps, granted credit and delivered chunk indices. Initial credit is index zero. An exact next index after its predecessor's delivery grants one chunk; duplicates do nothing and do not reset inactivity. Wrong app/future/malformed active credit terminates the original request. Retired credits are silent. Cancel only matches exact optional app scope; every usable-generation cancel receives an echoed acknowledgement. Unscoped list cancellation remains unchanged.

## Concurrency model

Run never prepares, reads, closes or waits for backend resources. Each admitted worker exits through backend error, delivery failure, timeout, response retirement, cancellation, disconnect or shutdown, and disposes its body before releasing its admission. Result handoff selects on the original inbox/manager cancellation. Workers/callbacks never access session state or Noise; positive reads update an atomic progress timestamp. Budget locks are leaf locks and never nest with hosted/push/backend locks. Generation cancellation invalidates pending handoff; asynchronous cleanup may finish later without admitting old output.

Run's existing sweep enforces thirty seconds from acceptance and five seconds since positive-byte read/new valid credit, including preparation. Progress cannot extend the acceptance deadline. Backend Done prevents retired metadata/chunks from being sealed; worker checks exact-generation metadata/read before producing each result. Remove unsealed outputs on retirement. An active sealed transfer cancellation/failure closes the generation, preserving nonce safety.

## State transitions and identity reuse

| Event | Race test |
|---|---|
| Metadata then multi-frame/empty/404 completion | `TestHostedAssetTransfer`, `TestHostedAssetRealTransport` |
| Initial credit, repeated credit, skipped/wrong-app credit | `TestHostedAssetCredit` |
| Held credit, cancel, duplicate/unknown/mismatched scope | `TestHostedAssetCancel` |
| Blocked sealed writer versus runnable control/other connection | `TestHostedAssetBlockedWriter` |
| Count/byte exhaustion and disposal recovery | `TestHostedResourceBudgets`, `TestHostedAssetAdmission` |
| Preparation/read progress versus inactivity | `TestHostedAssetTimeouts`, `TestResponseProgress` |
| Fixed deadline despite ongoing progress | `TestHostedAssetTimeouts` |
| Disconnect then same connection/request ID reuse | `TestHostedAssetReconnect` |
| Pair revocation, backend release/process retirement, shutdown | `TestHostedAssetRetirement` |

## Error handling

Select static correlated errors: invalid fields/path/credit → `app.invalid_request`; inaccessible → `app.not_found`; changed release → `app.release_changed`; unavailable generation → `app.unavailable`; byte/chunk overflow → `app.limit_exceeded`; count/reservation refusal → `app.busy` with retry-after one; inactivity/deadline → `app.timeout`; storage/metadata/I/O failure → `app.io_failed`. Ordinary HTTP 404 remains response metadata. Error/ack admission requires the original usable connection; sealed-delivery failure closes it. No backend diagnostics or request content reaches errors/logs.

## Testing strategy

Write focused failing parser/progress/transfer tests before implementing. Reuse encrypted peer and real transport fixtures, with local controllable fake responses and shortened test timeouts. Check bytes/digest/correlation, credit pacing, omission, capacity boundaries and recovery, progress and both deadlines, scoped/idempotent cancellation, sealed retirement and reconnect isolation. Run `go test -race ./internal/apps/... ./internal/relay/...`, `go vet ./...`, `go build -o /tmp/builder-3315/pyry ./cmd/pyry`, and diff checks. The verifier owns the full-module gate; #3248 owns production acceptance.

## Open questions

None. Optional fake-provider and duration seams do not attest production readiness.

## Documentation handoff

Pending for the documentation stage:

- `docs/protocol-mobile.md` § Hosted apps (v2): describe implemented asset metadata/chunks, credit and cancellation while retaining pending API routing.
- `docs/knowledge/features/v2-session-manager-concurrency.md` § Hosted discovery and ordered delivery: describe resource producers, capacity/credit ownership, timeout and retirement guarantees.
- `docs/knowledge/features/apps-package-resource-serving.md` § Stable response ownership: document optional positive-byte progress reporting.
- Retain pending production capability activation/native assembly, transport-delivery versus app-success, and uncertain-mutation wording.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] Exact authenticated `parseHostedRequest` validation ignores additive fields without granting authority; resource direction and pairing/capability checks precede work.
- [Tokens] Existing pairing lookup enforces revocation again at output admission; no new credential storage, logging or issuance.
- [Files] `Owner.PrepareAsset` retains descriptor-relative no-follow confinement and stable staging; no new path join, spool or persistent writes.
- [Subprocesses] No new launch path; exact existing backend generation is captured rather than selected by client process/port fields.
- [Cryptography] Run alone assigns sender IDs and Noise nonces; cancellation after sealing retires the connection. SHA-256 describes complete decoded bytes using existing backend hashing.
- [Network/I/O] SHOULD FIX: enforce fully serialized hosted plaintext cap for every hosted output, including discovery/control, and reserve decoded copies before read. Implement in `hostedDeliver` and the resource worker.
- [Errors/logs] Closed static classifications exclude paths, payloads, queries and backend error text.
- [Concurrency] SHOULD FIX: release counts only after worker disposal; remove queued output before releasing copy reservations, and never call blocking backend disposal on Run.
- [Threat model] Encrypted assets preserve relay blindness and paired host scope. Browser/native isolation and production activation remain OUT OF SCOPE, owned by client tickets and #3248; API mutation forwarding remains #3316.

**Reviewer:** builder (self-review)
**Date:** 2026-10-13

## Revisions

- 2026-10-13: Track positive progress as monotonic elapsed time, and give backend work and resource writes the fixed acceptance context deadline. Output/credit guards also check expiry before admission; late progress cannot revive inactivity-expired work. This closes the gap between sweep ticks without changing the thirty/five-second contract.
- 2026-10-13: Successful backend disposal closes `PreparedResponse.Done` too. Track final transport delivery on Run so cleanup cannot create a spurious unavailable error, and make finished-operation credit silent while cleanup completes. Preserve explicit backend completion errors.
- 2026-10-13: Resource workers wait for output disposal/delivery or original inbox retirement before releasing chunk reservations. A backend Done signal alone cannot release a still-queued copy. The real transport fixture now accepts legal outer chunk framing and exposes socket retirement for a publication fence.
- 2026-10-13: Lifecycle proof also includes `TestHostedAssetReadProgress`, `TestHostedAssetRealTransportBlockedWriter`, `TestHostedAssetQueuedCancellation`, `TestHostedAssetControlCapacity`, duplicate/malformed credit and backend error tables. Existing `TestResponseRetirement` covers real backend release/process retirement, while encrypted tests cover the returned response's retirement signal. Final written work is approximately 1,650 lines; counting stale/cancelled handoff and delivery guards separately also gives approximately fifteen resource state-machine rejection branches. Both ceilings exceed the earlier grouped sketch; the verified split-depth exception applies.
- 2026-10-13: Verifier finding 1 exposed reentrant delivery when `retireHostedResource` enqueues a terminal error while `hostedDeliver` selects an unusable queued head. Recheck the active writer after retirement before selecting or sealing another envelope; the nested delivery retains exclusive ownership of its output, context and cancellation. `internal/relay/v2session_hosted_asset_delivery_test.go` → `TestHostedAssetQueuedRetirementSingleWriter` covers queued backend retirement and timeout followed by an unrelated held control writer, ordered encrypted success and generation retirement on writer failure. The regression failed in all four scenarios before the guard. No new types, consumer changes or failure branches are introduced; this rework adds approximately 170 written lines under the existing split-depth exception. Documentation handoff and production activation ownership remain as specified above.
