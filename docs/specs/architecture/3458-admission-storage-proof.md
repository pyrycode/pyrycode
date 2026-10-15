# Admission storage setup and rejection proof

## Files read

- `internal/config/admission_test.go` → `TestRelayAdmissionStorage`, `admissionConfigFile`, `TestRelayAdmissionOwnership`: native setup, early fatal assertion and metadata injection pattern.
- `internal/config/admission.go` → `readConfig`, `validateAdmission`, `privateAdmissionStorage`: handle-bound metadata and exact full-mode/effective-UID boundary.
- `internal/config/config.go` → `Load`: zero config on rejection.
- `internal/config/memory.go` → `UpdateMemory`, `updateMemory`: shared validation before any write.
- `docs/knowledge/features/config-package.md` § Relay admission credentials: legacy searchable-parent compatibility and no permission repair.
- `CODING-STYLE.md` and `docs/knowledge/features/development-verification.md` § Prove that tests distinguish the change: independent predicates and visible prerequisite skips.

## Context

The native setgid fixture assumes successful chmod establishes the requested mode.
At base `59b84ee89fd98455daaa8e296ac449e1fd114a6b`, the bounded race run on
physical TMPDIR `/private/tmp/builder-3458/physical` reproduced the setgid leaf
failure. A native probe inherited GID 0 with effective UID 501/GID 20 and observed
0700 after a successful setgid chmod. Outside the sandbox, assigning the owned
fixture to effective GID 20 allowed setgid to persist. This demonstrates a current
fixture prerequisite failure, not the initiating cause of the historical run.
No production defect has been demonstrated; preserve the admission contract.
No decision record is needed. No in-flight feature branch overlaps the planned files.

## Design

Keep all implementation changes in `internal/config/admission_test.go`.
Retain the inherited-group native case and add an effective-group control using
unprivileged group assignment on its own fixture. Observe requested/actual modes,
UID/GID, process IDs/groups, supplied/resolved TMPDIR and fixture paths, filesystem
type/identity and chmod/group-setup outcomes. Each attempt records both `Load`
and `UpdateMemory` before assertions or a native-prerequisite skip.

When the requested native mode exists, both APIs must reject setgid storage.
Rejected updates preserve exact bytes and complete file/parent modes. A missing
native mode is a specific setup skip confined to that case, after API outcomes
are checked against the actual observed storage. Other storage and legacy rows
continue to execute. Setup I/O failures remain failures except an unavailable
native setgid/group prerequisite, which is reported with observed metadata.

Add deterministic full-mode metadata injection beside the existing ownership
injection: an effective-user-owned regular 0600 file and directory 0700 accept;
the same metadata with parent setgid rejects. This always runs, regardless of
native support. Do not mask special bits or change production permission handling.

Sizing: one deliverable, three acceptance criteria, approximately 280 written
lines including this plan, zero exported types or consumer migrations and fewer
than ten assertion/setup branches; below all builder limits.

## Concurrency model

Tests use independent temporary fixtures and retain existing parallel execution.
No new goroutines, environment mutation or shared production state.

## State transitions and identity reuse

None: synchronous reads and one update per isolated fixture; no lifecycle identity.

## Error handling

Record both API results before reporting mismatches; safe-error assertions remain.
Inspect bytes and full modes after rejected updates. Report exact missing native
prerequisites with setup errors and metadata, without skipping deterministic tests.

## Testing strategy

Retain the failing bounded baseline JSON and stderr in `/tmp/builder-3458`.
Write deterministic metadata coverage and diagnostic assertions first, observe
the native setup failure, then repair the fixture/prerequisite handling.
Run the complete config suite with physical TMPDIR and
`go test -race -count=1 -json ./internal/config/...`, without concurrency overrides.
Run `go vet ./...` and build `./cmd/pyry` to a scratch output. PR evidence retains
revision, exact commands, named outcomes, counts including parents/subtests and
all skip reasons. Full aggregate gates belong to the verifier; the historical
uncached aggregate stopped before staticcheck, substrate-guard and fake e2e.
No live Claude check is required.

## Open questions

None. Native capability is an observation per attempt, never inferred from chmod
success or a later passing rerun.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] `validateAdmission` remains the common boundary; deterministic metadata must test full mode equality, not only permission bits.
- [Tokens] Fixtures use empty maps; diagnostics record metadata and errors, never config contents or real credentials.
- [File operations] SHOULD FIX: record both bytes and full modes after rejection so silent permission repair cannot pass. Native setup changes only owned disposable fixtures; production handle-bound checks and symlink policy remain intact.
- [Subprocesses] No subprocesses are added to tests.
- [Cryptography] No crypto or token lifecycle changes.
- [Network and I/O] No networking or new production I/O; filesystem diagnostics are bounded per fixture.
- [Errors/logs] Retain safe-error assertions; exact API errors concern only synthetic fixtures.
- [Concurrency] Isolated fixtures, no new shared state or goroutines.
- [Threat model] Credential storage remains exactly effective-user-owned 0600/0700 with no special bits. No authentication or relay protocol change.

**Reviewer:** builder (self-review per security-review checklist)
**Date:** 2026-10-15
