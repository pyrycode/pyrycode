# Private app API forwarding

## Files read
- `internal/apps/health.go` → `Target.Do`, `privateClient`, `targetBody`: private routing, cancellation and redirect/compression policy.
- `internal/apps/response.go` → `Owner.Prepare`, `validResponseMetadata`: stable bytes, generation checks and reservation/disposal contract.
- `internal/apps/response_stage.go` → `PreparedResponse.stage`: bounded staging and sanitized I/O failures.
- `internal/apps/health_test.go` → `healthFixture`, `healthTarget`: owned service-generation fixtures.
- `internal/apps/response_test.go` → `responseBudget`, `responsePrepared`: existing preparation/cleanup proofs reused.
- `docs/knowledge/features/apps-package.md` § Resource-serving and Lifecycle and restart state: release equality cannot protect against same-release process replacement; EOF retains capacity until disposal.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Asset/API fields and HTTP semantics, Bounded encrypted transfers and retirement, Errors and lifecycle test obligations: normative HTTP boundary.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: source checks, race tests and bounded helper-process fixtures.

## Context
Expose one callable behavior: bounded API reads/writes to the registered app's exact health-confirmed generation. Encrypted routing remains #3247. No publishing or lifecycle changes.
Sizing: approximately 730 total written lines, one exported request type, no consumer migrations, four criteria and nine package failure classes; no new lifecycle state machine. Rechecked against this plan before committing, within all five builder limits.
Overlap: #3258 edits `response.go` and proposes the same expected-target seam; it is unmerged. Supply that compatible seam locally without depending on asset serving, keeping the change local and additive. URL validation stays in the API family until common helpers land.

## Design
`APIRequest` carries method, escaped path, optional raw query, lowercase headers, decoded body and reservation callbacks. `Owner.ForwardAPI(ctx, serverID, appID, release, request)` returns `(*PreparedResponse, error)`; authenticated host identity is supplied by the caller.
Resolve registration, exact release and readiness atomically under registry-then-owner locks before backend I/O. Extract `responseTarget` and an internal `prepare` accepting an expected target, preserving `Owner.Prepare` callers and matching #3258's seam. Carry the selected target through preparation, including empty results.
Validate seven exact methods, empty GET/HEAD bodies, 32768-byte opaque bodies, three permitted lowercase headers and serialized/value bounds before I/O. Reject forbidden URL forms, escapes, controls and traversal; preserve validated path/query spellings with `url.URL.RawPath`/`RawQuery`, decoding once only for validation.
Use `Target.Do` once. Sanitize network failures; preserve caller context errors. Project four response headers, reject unsupported status and non-identity content encoding. Close the backend body on every path. HEAD/204/205/304 use an empty source and zero size; all others stage with the 1048576-byte cap, exact digest and existing caller-owned reservations.

## Concurrency model
No new production goroutine family. `Target.Do` joins caller/generation cancellation through its body ownership; preparation's existing watcher exits on completion, disposal, caller cancellation or generation retirement. Registry callbacks only cancel. Locks remain registry then owner; reservations must be prompt and cannot reenter the result.

## State transitions and identity reuse
| Event | Race-detector coverage |
|---|---|
| Caller cancellation/deadline during headers or stalled body | `TestAPICancellation` |
| Removal, stop, exit, release replacement during backend I/O and retained consumption | `TestAPIRetirement` plus existing `TestResponseRetirement` |
| Same-release replacement after backend response before empty staging | `TestAPIEmptyReplacement` |
| Repeated disposal/cancellation, no retained capacity | `TestAPIResponses`, `TestAPICancellation`, existing `TestResponseDisposalRace` |
| Uncertain mutation | `TestAPICancellation`: one backend request, never replayed |

## Error handling
`ErrNotFound`, `ErrReleaseChanged`, `ErrUnavailable` preserve identity/readiness distinctions. Add static `ErrAPIRequest` and `ErrAPILimit` for malformed requests and body overflow. Reuse response metadata/limit/capacity/I/O errors. Caller cancellation/deadline remains a standard context error. HTTP 200–599 is ordinary response data. Never return endpoint/request diagnostics, replay a mutation or claim rollback.

## Testing strategy
Write failing tests before production code. Extend the existing health helper's optional API behavior locally for exact RequestURI/body/header capture, mutation/readback, controlled stalled headers/body and response variants. Table-driven boundary tests cover forbidden paths/query/headers/methods, exact/over limits, projection, identity encoding and no-body/error/redirect responses; reuse shared preparation cleanup proofs.
Run `go test -race ./internal/apps`, `go vet ./...`, and `go build ./cmd/pyry` (binary output outside the worktree). The verifier owns the full-module gate; no live-Claude or relay proof is required.

## Open questions
None. The unmerged asset seam is reproduced compatibly; no asset dependency is required.

## Documentation handoff
Pending documentation stage: `docs/hosted-apps.md`, Client resource bridge / Asset/API fields and HTTP semantics, and `docs/knowledge/features/apps-package.md`, Resource-serving: describe callable private API forwarding, permitted methods/headers and byte limits, identity encoding, no-body responses, reservation/disposal ownership, generation cancellation and uncertain mutations without replay or rollback claims. Distinguish implemented backend behavior from pending encrypted routing.

## Security review
**Verdict:** PASS

**Findings:**
- [Trust boundaries] `ForwardAPI` validates caller fields before `Target.Do`; `responseTarget` prevents foreign registrations and wrong releases from reaching service I/O. Retain expected generation through empty staging.
- [Tokens, secrets, credentials] Only three request headers enter a fresh request; no cookies, credentials or bearer/proxy headers are admitted. No token generation/storage changes.
- [File operations] URL text is never a filesystem path. Test control files are private fixture artifacts; production performs no new file operations.
- [Subprocesses] Reuse existing owned process lifecycle; no caller fields become argv/environment.
- [Cryptography] Reuse preparation's standard SHA-256 for complete bytes; no keys/nonces or secret comparisons added.
- [Network and I/O] Strict URL containment, bounded request/response/header data, disabled proxies/redirects/compression and retained generation are required in this implementation. Caller cancellation covers headers and body. OUT OF SCOPE: admission ceilings and fixed deadlines/inactivity enforcement belong to #3247.
- [Errors, logs, telemetry] Return static package errors/context errors only; no new logging of headers, bodies, URLs or private endpoints.
- [Concurrency] Resolve under registry then owner locks and compare expected generation before staging. Existing cancellation watcher closes stalled sources and releases reservations exactly once.
- [Threat model] Trusted app services run under the daemon account; opaque private targets prevent callers selecting arbitrary services. OUT OF SCOPE: encrypted connection authentication, wire parsing and native viewer containment belong to #3247 and the viewer tickets under #3120.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-12

## Revisions
- 2026-10-13 — Verifier finding 1: reran the complete gates on `e18103dbbcdbc8d8de3e26ecc895556e5a409d89`, including merged #3258. No implementation defect or design change was identified.
  - `make check` exited 0: vet, full unit race suite, staticcheck, substrate-guard and uncached fake-daemon e2e all passed. There were 62 uncached package pass results and one package without tests; `cmd/pyry` passed in 207.531s, `internal/apps` in 43.532s and `internal/e2e` in 181.309s.
  - Then `make build BIN=/tmp/builder-3246-rework-535f994b/pyry` exited 0. Only the binary destination differs from the default build target.
  - Complete logs: `/tmp/builder-3246-rework-535f994b/check.log` (SHA-256 `5fb0bf2ba32036cafc638fb4568adcc91394ca48c532012fcc65787bea719282`) and `build.log` (SHA-256 `e93352d226789ed01c0c66f3c1883360cf3de622cffeb93b2354a151a99c79de`). Documentation and encrypted routing remain the handoffs recorded above.
