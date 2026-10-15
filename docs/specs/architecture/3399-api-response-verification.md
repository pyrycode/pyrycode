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

## Revisions

### 2026-10-15 — diagnosis and retained experiment

Starting tree: `0ed5ab084d940164ffd487a703daf52a063d54d8`; candidate code: `f0331115d7418e156307c3a2a9b74e113cde55fa`. Host: macOS arm64, Go 1.26.2. The merged prerequisite repairs are `2300ff66c` (#3394), `2663a9fb0` (#3395), `bcabeaa08` (#3396), `34d709e26` (#3397), and `93a37f131` (#3398). Their changes are fixture-only and do not alter the API product contract.

The historical baseline log identifies `ErrUnavailable` for overflow, capacity, truncated, and the final request. It contains no health-probe or target-state diagnostics establishing the initiating event. The managed `fleet-task.py evidence 3387` lookup returned `bundles: []`; the local log was available. Twenty baseline repetitions at plan revision `bae708fe` (unchanged production/tests) passed 360/360 test/subtest records, zero failures/skips. Historical invalidation was not reproduced without injection; neither TMPDIR nor a particular timeout is established as its cause.

The red header-I/O control used `go test -race -json -count=1 -run '^TestAPIResponseFailureClasses/request_io$' ./internal/apps` before the disconnect fault existed: it returned nil error and a prepared result, failing with `got <nil> want apps: response I/O failed`. Two records executed/failed (parent and arm), zero passed/skipped. An earlier provisional control incorrectly used HTTP status zero and passed via the helper's invalid-status failure; using status 200 removed that false positive. The final regression independently checks staging reservation/read callbacks to distinguish header failure from body failure.

Candidate controlled outcomes from the physical-root complete apps run:

| Scenario | Exact error | Valid target | Requests | Reservation calls / positive reads | Elapsed |
| --- | --- | --- | --- | --- | --- |
| overflow, 1048577 bytes | `apps: response byte limit exceeded` | true | 1 | 33 / 33 | 65.133 ms |
| capacity, zero budget | `apps: response capacity unavailable` | true | 1 | 1 / 0 | 1.658 ms |
| close before headers | `apps: response I/O failed` | true | 1 | 0 / 0 | 2.257 ms |
| truncated body | `apps: response I/O failed` | true | 1 | 1 / 1 | 2.542 ms |
| three explicit failed probes during withheld headers | `apps: service unavailable` | false | 1 | — | 28.722 ms |
| subsequent capacity request on that invalid target | `apps: service unavailable` | false | 0 additional | — | synchronous rejection |

All retained budgets were zero. The controlled probes reproduce the historical *error pattern*, not its initiating historical cause. `TestAPIResponses` now holds runtime health probes at the existing manual-tick seam; startup health and production invalidation checks remain active. No timeout was lengthened: the controlled caller deadline and existing health wait are four seconds, helper I/O deadlines remain one/two seconds, and owner probe timeout remains 100 ms. The source change adds failure diagnostics and test-only fault injection; no production files change.

### Temporary-root repetition evidence

Scratch logs are under `/tmp/builder-3399/`. The supplied physical root is `/private/tmp/builder-3399/physical`; its resolved root is identical. The supplied deliberately created alias is `/private/tmp/builder-3399/alias`, resolving to `/private/tmp/builder-3399/physical`. Both directories are test-owned. `GOFLAGS` and `GOMAXPROCS` were unset; no package-concurrency override was used.

For each supplied root, command: `TMPDIR=<root> go test -race -json -count=20 -run '^TestAPIResponses$' ./internal/apps`.

| Setup | Executed / passed / failed / skipped test+subtest records |
| --- | --- |
| physical | 360 / 360 / 0 / 0 |
| alias | 360 / 360 / 0 / 0 |

Each named arm executed/passed 20 times in each setup: binary, redirect, error, last_status, head, 204, 205, 304, upgrade, unsupported, encoding, metadata, control, exact, overflow, capacity, truncated. The parent executed/passed 20 times too. There were no skip reasons because nothing skipped. The exact-success body remains 1048576 bytes; overflow remains 1048577 bytes; capacity remains zero; all original request-count and cleanup assertions remain.

### 2026-10-15 — aggregate gate ownership correction

The Testing strategy above incorrectly treated the ticket's aggregate requirement as overriding the builder role's explicit prohibition on `make check` and full-module race runs. Those commands belong to the verifier. No aggregate command was executed here. The PR must hand off an uncached complete `make check` with normal package concurrency and named counts/skips after these merged prerequisite repairs, and report that aggregate result as pending/incomplete until its owning stage executes it. This corrects the execution plan, not the product/test design. Builder-owned `go vet ./...`, both complete scoped apps race suites, and `make build` remain required here.

### Complete scoped suite and build results

Both scoped commands used candidate code `f0331115`, with no concurrent test workload started by this run. Command per root: `TMPDIR=<root> go test -race -json -count=1 ./internal/apps`.

| Setup | Executed / passed / failed / skipped (including subtests) | Top-level counts | Package result |
| --- | --- | --- | --- |
| physical | 972 / 956 / 2 / 14 | 202 / 191 / 1 / 10 | FAIL, 207.577 s |
| alias | 972 / 958 / 0 / 14 | 202 / 192 / 0 / 10 | PASS, 244.006 s |

Each complete run passed all 24 records belonging to `TestAPIResponses` and `TestAPIResponseFailureClasses`, without failures or skips. The physical run's sole failing leaf was `TestPublishRetirement/health/stop`, reporting `operation not permitted`; the second failure record is its parent. This matches the already-open native cleanup investigation [#3418](https://github.com/pyrycode/pyrycode/issues/3418), which independently reproduced that same error on unchanged main. No publication/cleanup source or fixture was changed here. The alias pass does not erase the physical failure or establish its cause.

Both suites had the same fourteen skips:

- Supported real Node/npm was not selected (the existing real-NPM probe reported Node v25.8.0 / npm 11.11.0): `TestBackupSQLiteCancellation`, `TestBackupSQLiteStorageErrors`, `TestBackupSQLiteWAL`, `TestCandidateSupportedTemplate`, `TestOwnerSupportedNode`, `TestPublishTemplate`, `TestUpdateTemplateRollback`, `TestRestoreSQLiteWAL`, `TestRestoreSQLiteGenericAndCancellation`, `TestStartupTemplateRecovery`. Their messages request `HOSTED_APP_NODE` or supported Node/npm and explicitly state that skips supply no SQL, launch, publication, rollback or restart evidence.
- The filesystem rejects invalid UTF-8 filenames with EILSEQ: `TestBackupAliasedTempRoot/states/nested`, `TestBackupStates/nested`, `TestRestoreStates/nested`, `TestRetainedValidation/invalid_UTF8`.

`go vet ./...` passed (exit 0). `TMPDIR=/private/tmp/builder-3399/physical make build` passed (exit 0; recipe `go build -o ./pyry ./cmd/pyry`). `git diff --check` passed. No binary was installed or swapped.

Open questions are resolved as follows: baseline repeated runs did not reproduce historical invalidation, and the retained controlled experiment distinguishes its error pattern without asserting its historical cause; the scoped suite found the existing #3418 failure. The aggregate gate remains **pending/incomplete**, owned by the verifier: run uncached `make check` at the final pushed revision with normal package concurrency, a recorded supplied/resolved TMPDIR, and named counts/skips for full-module race tests and fake-daemon e2e, plus vet, staticcheck and substrate-guard results. `make build` is already green here. No live-Claude evidence is required.
