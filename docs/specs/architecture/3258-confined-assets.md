# Confined active-release assets (#3258)

## Files read
- `internal/apps/runtime.go` → `prepareRelease`, `privateDir`, `regularAt`: release selection and descriptor confinement; extract selection without Node/data side effects.
- `internal/apps/response.go`, `response_stage.go` → `Owner.Prepare`, `PreparedResponse.stage`: stable bytes, reserved EOF probe, source disposal and checked delivery.
- `internal/apps/health.go`, `reconcile.go` → `usableLocked`, `Target.valid`, `currentLocked`: committed readiness and generation identity.
- `internal/apps/owner.go` → `launch`: sole production release-preparation consumer.
- `internal/apps/owner_test.go`, `health_test.go`, `response_test.go` → `ownerBuild`, `responseOwner`, `responseBudget`: controlled real child generations and reusable staging proofs.
- `docs/knowledge/features/apps-package.md` § Resource-serving: reservation precedes allocation/read, EOF retains ownership, concurrent Close must unblock source reads.
- `docs/specs/architecture/3120-hosted-app-contract.md` §§ Runtime, roots and service conventions; Asset/API fields and HTTP semantics; Bounded encrypted transfers and retirement; Electron and Android resource/Fetch bridge: normative asset boundary.
- `CODING-STYLE.md`, `docs/knowledge/features/development-verification.md`: Go conventions and behavior proofs.

## Context
Serve only the authenticated app's committed active build while its owned generation is healthy. Generic preparation exists; callable assets are the one deliverable. No schema, publisher, relay or capability changes. No decision record needed. Feature-branch check found no overlap in the planned files.

## Design
- Add `AssetRequest` containing escaped Path, optional Query, native-stamped Navigation and the existing reservation callbacks. `Owner.PrepareAsset(ctx, serverID, appID, release, request)` returns `*PreparedResponse` or a safe package error. Authentication/navigation stamping belong to the caller.
- Share committed identity/release/readiness resolution with `Prepare`. An internal preparation entry accepts an expected Target and refuses replacement generations, preserving the public API.
- Extract side-effect-free unique validated build selection from `prepareRelease`, returning the selected directory descriptor plus its name. Asset selection retains that descriptor through opening `dist/web`; runtime preparation retains its existing Node/data behavior.
- Validate ASCII path/query bounds and escapes. Decode path once, reject encoded separators/percent, dot segments and decoded controls/backslashes; reject every reserved root before file access. Queries remain opaque and never participate in lookup.
- Walk each component beneath the pinned web descriptor with `openat`, `O_NOFOLLOW`, directory-only intermediates and nonblocking regular-file final opening. Only ENOENT means missing. Unsafe entries and other I/O failures return a sanitized storage error.
- `/` selects `index.html`. Missing extensionless native navigation selects it too; other missing paths produce empty HTTP 404. Use a deterministic MIME table including text/javascript and application/octet-stream fallback.
- Stat the opened handle for the size hint, then shared preparation enforces 8 MiB before staging and through EOF. Generated headers are bounded content-type only. Shared checked metadata/reads and Complete/Close own delivery and cleanup.

## Concurrency model
No new goroutine. Reuse preparation's cancellation/retirement watcher and disposal. Registry then owner locks resolve identity and expected generation; release scanning and descriptor I/O run outside these locks. Preparation rechecks the captured Target under the same lock order before staging. All temporary directory/file handles close on every return; successful preparation owns and closes the source.

## State transitions and identity reuse
| Event | Race-detector proof |
| --- | --- |
| Remove, replace active release, stop, exit, retire after preparation | `TestAssetRetirement` plus existing `TestResponseRetirement` |
| Same-release restart between lookup and staging | `TestAssetLookupRestart` |
| Metadata-only update preserves generation | `TestAssetStable` |
| Cancellation/refusal during staging and retained delivery | `TestAssetLimits` plus existing `TestResponseCancellation`, `TestResponseDisposalRace` |
| Intermediate/final replacement with symlink | `TestAssetConfinement` |

## Error handling
Preserve ErrNotFound/ErrReleaseChanged/ErrUnavailable precedence for committed resolution. Invalid/reserved URL syntax yields static ErrAssetPath. Missing/ambiguous/unsafe builds or unsafe handles yield static ErrInvalidStorage; asset read/close failures use ErrResponseIO. Context cancellation, metadata/capacity and limit errors retain shared semantics. Genuine missing public files alone yield HTTP 404/fallback, never package diagnostics. No native paths/request contents enter errors.

## Testing strategy
Write focused tests first and observe failure before production edits. Temporary active/candidate builds prove manifest selection, MIME/binary/modules, query independence, navigation/404/reserved paths and hostile URL syntax. Descriptor tests prove intermediate/final symlink and nonregular refusal, replacement confinement and handle closure. Asset-sized boundary tests cover empty/404 digest, exact 8 MiB, oversize, actual file growth, capacity refusal and cancellation; reuse generic preparation proofs. Run `go test -race ./internal/apps`, `go vet ./...`, `go build -o /tmp/builder-3258/pyry ./cmd/pyry`. Full-module gate belongs to verifier. No live Claude required.

## Open questions
None. Empty 404 body has text/plain MIME and the standard empty SHA-256. Size estimate: about 720 written lines, one exported request type, one existing production consumer affected, five acceptance criteria, at most eight failure classes; all limits fit.

## Documentation handoff
Pending documentation stage: update `docs/hosted-apps.md`, `Client resource bridge / Asset/API fields and HTTP semantics`, and `docs/knowledge/features/apps-package.md`, `Resource-serving`: describe implemented confined callable assets, reserved paths, MIME and navigation/404 behavior, stable preparation, reservation/disposal ownership and cancellation. Preserve source-unblocking and delivery-failure obligations documented for #3257. State paired routing/capability activation remains pending.

## Security review
PASS after adversarial review:
- Trust boundary: shared committed resolution precedes asset access; native must supply authenticated host and navigation flag. MUST FIX addressed in design: expected-generation check prevents same-release restart rebinding.
- Files: MUST FIX addressed in design: keep the selected build descriptor (never re-open its path) and walk each asset component with no-follow openat; only ENOENT enables fallback. Nonregular descriptors fail before staging.
- Network/I/O: 2048/1024 ASCII path/query bounds, bounded manifest reads and 8 MiB response cap through EOF; caller reservations precede body I/O. No network server introduced.
- Errors/telemetry: static package failures suppress paths/content; no new logs or metrics.
- Concurrency: registry→owner locking, immutable generation binding, existing watcher exits on disposal; no new workers.
- Tokens/subprocesses/cryptography: no credentials or command execution in asset path; SHA-256 reuses standard preparation integrity hashing, not authentication.
- Threat model: host-file exposure and cross-release delivery are addressed here. Native isolation and connection admission/serialization remain out of scope under #3123/#3247.
