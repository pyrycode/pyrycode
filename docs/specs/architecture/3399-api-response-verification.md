# API response rejection diagnosis and offline verification

## Files read

- `internal/apps/api_test.go` → `apiOwner`, `apiHelper`, `apiWrite`, `TestAPIResponses`: shared live target and response assertions.
- `internal/apps/health_test.go` → `healthFixture`, `TestHealthHelperProcess`: 100 ms probes, 40 ms runtime interval, finite HTTP deadlines.
- `internal/apps/health.go` → `supervise`, `Target.Do`, `targetBody.Read`: three failed probes retire readiness; request and body guards preserve generation identity.
- `internal/apps/api.go` → `ForwardAPI`, `prepareAPI`: request errors become I/O or unavailable; preparation preserves rejection classes.
- `internal/apps/response.go`, `response_stage.go` → `prepare`, `checkLocked`, `stage`: bounded staging, cancellation, reservation release.
- `internal/apps/owner_test.go`, `registry_test.go` → `ownerFixture`, `testOpen`, `testRoot`: registry roots already resolve symlinks.
- `docs/knowledge/features/apps-package-testing.md`, `apps-package-resource-serving.md`: distinguish backend, preparation, and encrypted-routing proofs; retain all byte and lifecycle assertions.
- `docs/knowledge/features/development-verification.md`: passing reruns do not establish historical attribution; record named counts and source revision.
- `Makefile` → `check`, `build`: aggregate gate tiers and uncached invocation.

## Context

At baseline `8a17a77d`, `/tmp/verifier-3387/baseline-check.log` records overflow and capacity returning `apps: service unavailable` instead of their rejection errors. Truncated also returned unavailable, followed by the final header request. The managed evidence lookup returned no bundles. These observations identify invalidation, not its initiating cause; passing canonical-root reruns cannot prove a TMPDIR cause.

Dependencies #3394–#3398 are closed and their changes are in this checkout. No other fetched feature branch touches the planned files. This is one deliverable: a discriminating, reliable response proof plus its offline evidence. Estimated total written work is 300–450 lines, zero exports, zero production call-site updates, four acceptance criteria and fewer than ten controlled failure classes. No production repair or decision record is planned.

## Design

Keep `apiOwner` and shared health defaults unchanged. Add a response-specific fixture using the existing `healthTicks` seam, installed before startup, to put runtime probes under test control while retaining real startup health confirmation. `TestAPIResponses` uses that fixture with no runtime ticks; all existing assertions remain.

Add controlled failure-class scenarios through the actual private HTTP backend: overflow, zero capacity, connection closure before headers, truncated response body, and health invalidation during a withheld response. Record exact error, elapsed time, request count, budget and target validity. Explicitly deliver three failed health probes, waiting for each observed failure or retirement, to reproduce invalidation and the subsequent unavailable capacity arm without relying on scheduler delays. The helper adds only an opt-in connection-close fault. Improve existing unexpected-error diagnostics with target validity, current lifecycle, elapsed time and request/budget counts.

## Concurrency model

Existing owner/process and prepared-response workers retain their shutdown paths. The controlled invalidation arm starts one forwarding worker with a finite caller deadline, awaits the backend request marker, retires through health probes, and joins the worker. Cleanup closes responses and shuts down the owner. Channels control probes; owner state reads hold its mutex. No production synchronization changes.

## State transitions and identity reuse

| Event | Race test |
| --- | --- |
| Startup health confirmation, then response acceptance/rejection | `TestAPIResponses`, `TestAPIResponseFailureClasses` |
| Three failed probes invalidate a withheld request | `TestAPIResponseFailureClasses/health_invalidation` |
| Subsequent capacity request cannot reuse invalid target | `TestAPIResponseFailureClasses/health_invalidation` |
| Repeated close and reservation cleanup | `TestAPIResponses`, `TestAPIResponseFailureClasses` |

## Error handling

Match errors with `errors.Is`; assert nil prepared results on failure, exactly one backend request and zero budget after disposal. Invalidation's subsequent request forwards zero times. Connection/header I/O and truncated body must return `ErrResponseIO` while the target remains valid. Health invalidation must return `ErrUnavailable`. Preserve finite server deadlines and use bounded experiment waits. Do not attribute a historical initiating cause without additional evidence.

## Testing strategy

Write failure-class tests first and observe the header-drop arm fail before adding the helper fault. Run race-enabled controlled experiments and retain outcomes. For a private physical TMPDIR and its deliberate symlink alias, run `go test -race -json -count=20 -run '^TestAPIResponses$' ./internal/apps` and `go test -race -json -count=1 ./internal/apps`. Record supplied/resolved roots, exact commands, revisions, named counts and skip reasons.

The ticket explicitly requires the aggregate gate, overriding the builder's ordinary prohibition on duplicating it: run uncached `make check` with normal concurrency, JSON test output via a scratch Go wrapper, and `make build`. Record actual executed tiers and failures honestly; file or reuse unrelated follow-ups. No live-Claude check is required. Append results to this plan and the PR; logs stay in scratch.

## Open questions

- Can current baseline reproduce invalidation without injected faults? Resolve with repeated race runs; preserve attribution limits if not.
- Does aggregate offline verification reveal unrelated failures? Record the actual gate and follow-up rather than claiming success.

## Security review

**Verdict:** PASS

**Findings:**

- Trust boundaries: `ForwardAPI`, `responseTarget`, and staging guards remain unchanged; no authorization or generation check is bypassed.
- Tokens/cryptography: only synthetic fixture data is used; no credentials, key generation or crypto behavior changes.
- File operations: existing canonical private fixture roots and 0600 files remain; the symlink alias is a test-owned TMPDIR input, not a production path exception.
- Subprocesses: reuse the health helper's controlled test-binary launcher and existing owner cleanup; no new shell inputs or inherited credentials.
- Network/I/O: preserve helper HTTP deadlines and production byte caps; the new connection-close fault is test-only. Caller deadlines bound withheld-response experiments.
- Errors/logs: record error classes, durations, counts and synthetic lifecycle metadata only, never request bodies or headers.
- Concurrency: install manual ticks before startup, lock shared state reads, and join forwarding workers before cleanup.
- Threat model: offline fixture work does not change relay/mobile trust or production isolation.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-15
