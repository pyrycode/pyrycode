# Retained immutable app builds (#3288)

## Files read
- `internal/apps/manifest.go` → `ValidateManifest`, `validID`: strict host/manifest boundary; compare expected app separately.
- `internal/apps/runtime.go` → `privateDir`, `regularAt`, `prepareRelease`: descriptor-based access patterns; ordinary startup stays separate.
- `internal/apps/manifest_test.go` → `testManifest`: valid local fixture and identity constants.
- `internal/apps/releases.go` → `Release`, `RecordRelease`: metadata does not establish successful preparation/readiness.
- `docs/knowledge/features/apps-package.md` → validation, release metadata and runtime/root lessons: exact identity and no startup inventory wiring.
- `docs/knowledge/features/development-verification.md` → verification boundaries and filesystem fixtures: prove actual rejection and avoid case-only fixture collisions.
- `docs/specs/architecture/3120-hosted-app-contract.md` → Manifest and validation; Runtime, roots and service conventions: normative inventory and layout.
- `docs/knowledge/decisions/043-hosted-app-runtime-and-isolation.md`: dependency links are artifact input; services remain trusted OS-account code.
- `CODING-STYLE.md`: error values, same-package fixtures and descriptor cleanup.

## Context
Preparation (#3289) and publication (#3262) need an independent acceptance gate for retained bytes. This adds no build execution, release history, registry mutation or activation. Existing contract/ADR suffice; no new decision record is needed.

## Design
- Add `ValidateArtifacts(ctx, staging, serverID, appID) (RetainedBuild, error)`, `InstallBuild(ctx, appsRoot, staging, serverID, appID) (RetainedBuild, error)` and `VerifyBuild(ctx, appsRoot, serverID, appID, buildID) (RetainedBuild, error)`.
- `RetainedBuild` returns validated manifest, build ID, detached canonical inventory bytes and ordered migration paths; it conveys artifact validity only. Installation always validates its own input rather than trusting returned metadata.
- A shared descriptor walker retains three root files and only `dist/web`, `dist/service`, `sql/migrations`, `node_modules`. Staging extras are excluded; installed extras are rejected. Required files and tree roots must exist; migrations are flat regular SQL files with unique four-digit prefixes, ordered numerically.
- Open each directory component with `O_NOFOLLOW`; files use `regularAt` with a single component. Never traverse dependency links while reading. Record each real entry, then resolve link components against the recorded dependency graph, enforcing relative UTF-8 targets, containment at every traversal step and termination at an existing regular file/directory. Directories have no inventory record.
- Public output rejects environment/secret names, key material, database/SQL names, service/SQL/dependency subtrees and package/lock metadata. It does not claim to discover arbitrary secrets embedded in otherwise valid frontend code.
- Sort relative path strings by bytes. Hand-encode the fixed-key-order JSON array, escaping only quote, backslash and C0 bytes as lowercase Unicode escapes. Hash the exact regular-file bytes and canonical inventory with SHA-256. Store `inventory.json` outside its own inventory.
- Installation copies bytes (never hardlinks) into a fresh private sibling directory under a pinned builds parent. Files become 0400, executable dependencies retain owner execute (0500); directories become 0500. Verify the complete sealed temporary tree before an atomic no-replace rename, then verify the installed destination before success. Linux/Darwin helpers supply native exclusive rename semantics.
- Existing destinations are independently verified and reused only for the same canonical digest. Empty, partial, changed or writable builds fail without repair. Verification performs no writes or directory creation.
- Source and APPS_ROOT are daemon-selected absolute paths; identity/digest checks precede path joins. App/build parent creation uses descriptor-relative mkdir/open. Temporary cleanup is descriptor-relative and touches only the new private tree.
- New files: `artifacts.go`, `artifact_install.go`, platform rename helpers and focused tests. No current consumers change. Feature-branch overlap check found no overlapping target files.

## Concurrency model
Synchronous context-aware filesystem operations; no goroutines or shared mutable state. Exclusive rename arbitrates concurrent installers without replacing a winner. Caller guarantees staging build children have finished; subsequent edits cannot alter copied retained files.

## State transitions and identity reuse
| Event | Race-enabled proof |
|---|---|
| Repeated installation of identical identity/digest | `TestRetainedInstall` verifies reuse and staging independence. |
| Concurrent installs of the same digest | `TestRetainedConcurrentInstall` verifies one complete destination and consistent results. |
| Missing/partial/changed existing digest | `TestRetainedVerification` rejects without creation or repair. |
| Dependency link chain or directory-component reuse | `TestRetainedLinks` accepts contained chains and rejects dangling/cyclic/escaping resolutions. |

## Error handling
Return contextual filesystem errors or existing identity/storage/manifest sentinels. Cancellation fails before commit; failed validation removes only temporary installation work. Post-rename verification failure leaves the destination for diagnosis, never repairs it. No app code, subprocess or network operation occurs.

## Testing strategy
Write local fixture tests before implementation and observe compile/test failure. Independently authored inventory bytes/digest cover byte order, Unicode, quotes/backslashes, every C0 escape and literal HTML-sensitive characters. Table-driven rejection covers identity, required entries, unsafe public output, nonregular/symlink components, migration names/prefixes and invalid UTF-8. Installation tests cover immutable modes, excluded staging files, source independence, concurrent reuse and added/missing/changed files, links, inventory and digest. Run `go test -race ./internal/apps/...`, `go vet ./...`, `go build -o /tmp/builder-3288/pyry ./cmd/pyry`; cross-compile the Darwin apps test binary offline. Full-module tests belong to the verifier.

## Open questions
None. Public filename policy checks recognizable artifact classes; content provenance and successful build commands belong to checked preparation.

## Documentation handoff
Pending for documentation stage: `docs/hosted-apps.md`, “Runtime and layout” and “Create, publish and update”: document retained release contents, exact canonical immutable inventory/build identity, read-only installation and rejection of missing or changed retained builds. State that artifact validation alone establishes neither recorded candidate success nor health-confirmed activation/readiness; checked preparation and publication follow separately.

## Security review
**Verdict:** PASS. **Reviewer:** builder; **Date:** 2026-10-12.
- Trust boundaries: MUST FIX addressed in design: validate expected host/app and never trust caller-supplied inventory as an installation permit.
- Tokens/secrets: recognizable secret/environment/key artifacts rejected in public output; no credentials generated, inherited or logged.
- File operations: MUST FIX addressed: component-wise descriptor access, graph-contained dependency links, copy independence and native no-replace rename; sealed files/directories checked during verification.
- Subprocesses/network: none; app code/dependency install/migrations/health remain downstream.
- Cryptography: stdlib SHA-256 identities; no secrets or nonces used.
- Errors/telemetry: local error values only; no byte contents logged or stored in registry.
- Concurrency: exclusive rename prevents overwrites; temporary trees never use the digest name; no goroutines.
- Threat model: ADR 043 trusted services may alter their own account's files; verification detects changed retained bytes rather than claiming OS isolation. Resource limits/build-command provenance belong to #3289 and publication to #3262.

Sizing recheck: one retained-build acceptance deliverable; approximately 780 total written lines (460 production/helpers, 250 tests, 70 plan), one exported type, zero consumer updates, four acceptance criteria, no state-machine reject branches.
