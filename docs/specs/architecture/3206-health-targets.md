# Health-confirmed private app targets (#3206)

## Files read

- `internal/apps/owner.go` → `Start`, `launch`, `Stop`, `Shutdown`, `cleanup`: spawn acknowledgement, generation lifetime and group retirement.
- `internal/apps/lifecycle.go` → `CommitObservation`, `intentLocked`: conditional persistence preserves committed fields.
- `internal/apps/registry.go` → `changeLocked`, `Record`: registry-before-owner locking and synchronous callbacks.
- `internal/apps/manifest.go` → `strictJSON`: duplicate/trailing-value rejection.
- `internal/apps/owner_test.go`, `owner_process_test.go`, `owner_failure_test.go` → controlled-child fixtures and supported-runtime proof.
- `docs/knowledge/features/apps-package.md` § Lifecycle and restart state, Concurrency and persistence, Testing: active release wins over candidate manifest; child signals precede assertions; persisted state is no readiness proof.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions; Registration, observed state and safe publication: normative health and private routing contract.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: bounded I/O, behavior assertions and artifact survival.

## Context

Spawn success currently leaves the app starting. Confirm health before persisting running and making an owned target usable. No reconciliation, retries, migration, activation or daemon wiring is added. Existing split-depth exception applies: actual parent #3203 and grandparent #3122; `needs-human:sizing` is already set. Sketch and final-plan sizing: approximately 950 written lines, one exported target type, no existing production consumer migrations, five acceptance criteria and at most ten reject/error branches. The line ceiling is exceeded under the supplied grandchild exception. No fetched feature branch overlaps the planned production files.

## Design

Keep spawn acknowledgement separate from health. The launch worker supervises health and process exit. Production defaults are startup interval 250 ms, startup deadline 15 s from spawn, request timeout 1 s (including reply reads and bounded by remaining startup time), reply cap 4096 bytes and runtime interval 5 s. A 200 response must contain true readiness and exact app/active-release identity; reject malformed, duplicate or trailing JSON. Disable proxying, redirects and keep-alive reuse.

Running persistence uses the same conditional observation implementation as `Registry.CommitObservation`, under registry-before-owner locks with generation/context/liveness checks. Only after successful durable commit does the generation become healthy; save the returned record revision for later observations. Failed/rejected running persistence retires the group. Runtime success resets failures; the third consecutive failure retires readiness and conditionally observes failed. Exit, startup deadline and cancellation also retire readiness before cleanup. Retain five-second TERM grace, group KILL and direct-child wait.

`Owner.Lookup(serverID, appID)` returns an opaque `Target` or an unavailable error. `Target.Do(request)` validates registry locality, registration, available/running state, active release, live generation and established health on every use and connection admission. It replaces caller authority with the private endpoint, uses no proxies/redirects/reuse, and couples the request/body lifetime to generation cancellation. No raw endpoint accessor exists. Revalidation at dial prevents delayed use from addressing a replacement. Metadata-only commits do not invalidate established targets while release/intent/state changes do.

## Concurrency model

One launch worker per generation, one joined child-wait worker, serial bounded probes; all finish on cancellation or exit. Health probes run outside locks. Running commit and guarded dial admission take registry then owner locks. Callbacks must re-enter neither object. Target requests inherit caller cancellation and generation cancellation; closing the response body removes the cancellation hook. Never hold locks during HTTP response/body reads or group cleanup.

## State transitions and identity reuse

| Event | Race test |
|---|---|
| Spawn then identity-confirmed running; persistence before target | `TestOwnerHealthPersistence` |
| False/malformed/foreign health; startup expiry; bind collision | `TestOwnerHealthRejects`, `TestOwnerHealthBounds` |
| Runtime two failures, success reset, third failure | `TestOwnerHealthFailures` |
| Newer title/intent/active/pending release before health commit | `TestOwnerHealthPersistence` |
| Stop/shutdown/cancel, including failed stop writes | `TestOwnerTargetRetirement` |
| Exit after running persistence; group descendants | `TestOwnerHealthExit` |
| Retained target and delayed old probe/exit with reused endpoint | `TestOwnerTargetReuse` |

## Error handling

Use static `app.unavailable` health/start/exit metadata; diagnostics never become discovery messages. Failed running writes and cleanup errors remain available from Shutdown; Stop surfaces synchronous persistence/cleanup errors. Stale and removed observations cannot overwrite newer commits. No retries are scheduled.

## Testing strategy

Write focused tests first and observe failure before implementation. Use local real helper children with controlled HTTP responses and filesystem gates; shorten private timing fields while asserting production constants. Preserve and execute `TestOwnerSupportedNode` with an actual supported Node. Check request timeout through slow headers and bodies, startup decision separately from cleanup grace, capped replies, redirect/proxy refusal, current fields/revision preservation, failure reset, process-group cleanup and stale target rejection. Run `go test -race ./internal/apps`, `go vet ./...`, and `go build -o /tmp/builder-3206/pyry ./cmd/pyry`; verifier owns the full-module gate.

## Open questions

None. Target API is HTTP-only and keeps endpoint ownership private; publisher-exclusive cutover remains downstream.

## Documentation handoff

Pending for documentation stage: update `docs/hosted-apps.md` § Runtime and layout, Startup and health, Registration and observed state, and `docs/knowledge/features/apps-package.md` § Lifecycle and restart state. State exact startup/runtime timings and bounds, required health identity, spawn-versus-readiness distinction, persistence-before-readiness, failure-count reset/retirement, cleanup, guarded generation-owned private target lookup/use and safe state/error guarantees. Retain the execution slice's runtime/environment allowlist and stop timing. Distinguish callable health-confirmed supervision from pending automatic recovery (#3207), daemon wiring (#3204), publishing/rollback (#3125) and resource capability activation (#3123).

## Security review

**Verdict:** PASS

- Trust boundaries: bounded `probeHealth` validates exact health identity; target dial guards registry and private generation ownership.
- Tokens/secrets/cryptography: no tokens or cryptographic material introduced; existing exact child environment remains intact.
- File operations: existing atomic registry persistence and no-follow runtime preparation are reused; no new production paths.
- Subprocesses: preserve validated Node argv/environment, owned process groups, grace and child wait.
- Network/I/O: MUST FIX addressed in design: cap replies, include body reads in deadlines, reject redirects/proxies, disable reuse and guard delayed dials.
- Errors/telemetry: static health metadata only; child output remains discarded.
- Concurrency: MUST FIX addressed in design: lock generation validation with running persistence and dial admission, invalidate on cancellation even after failed writes, retain returned running revision.
- Threat model: trusted same-account app execution; loopback is not same-account authentication. Resource authorization/transfer limits belong to #3123; exclusive migrations/activation belong to #3125.

**Reviewer:** builder self-review. **Date:** 2026-10-11.
