# Bundled registered app creation (#3264)

## Files read

- `internal/apps/manifest.go` → `ValidateManifest`, `validText`: exact manifest and title contract.
- `internal/apps/registry.go` → `Register`, `changeLocked`, `Registry`: atomic persistence, capacity, tombstones and notifications.
- `internal/apps/storage.go` → `Open`, `writeSnapshot`: owned root/host identity and durable reopen.
- `internal/apps/lifecycle.go` → `cloneRecord`, `Subscribe`: detached results and synchronous commit notifications.
- `internal/apps/runtime.go` → `privateDir`, `regularAt`: descriptor-based no-follow patterns.
- `internal/apps/registry_test.go` → `testRoot`, `testOpen`, `TestPersistenceAndVisibility`: portable resolved temporary paths and failure injection.
- `templates/hosted-app/scaffold.mjs` → `scaffold`, `files`: maintained copy inventory and exclusive destinations.
- `templates/hosted-app/manifest.json`: fixed manifest template.
- `templates/hosted-app/test/scaffold.test.mjs`: standalone generation behavior.
- `docs/knowledge/features/apps-package.md` § Validation and stored identity, Template toolchain: exact schema before struct decoding; explicit inventories must include shared modules and dotfiles.
- `docs/knowledge/features/development-verification.md` § Establish the change surface, Prove that tests distinguish the change: qualified boundaries and independent negative scenarios.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Orchestrator walkthrough, Implementation responsibilities and handoff: unpublished local create contract.
- `CODING-STYLE.md`: Go, errors, concurrency and persistence conventions.

## Context

Join source-only scaffolding with durable registration without requiring a checkout,
Node, a hosting account or a service port. One deliverable: a registered editable
app and identity-based inspection. No new architectural decision record is needed.
Sizing: about 720 written lines including plan/tests, one exported result type,
zero existing consumer migrations, four acceptance criteria, at most ten reject
categories. No fetched feature branch overlaps the proposed files.

## Design

- Move scaffold's explicit inventory into `templates/hosted-app/inventory.json`; both standalone JavaScript and Go consume it. Add a small Go package there embedding the inventory, manifest and original source tree (including dotfiles), with no second asset copy.
- `Registry.Create(ctx, title) (Source, error)` chooses a cryptographically random canonical UUIDv4, uses only the registry's root/host and returns stable identities, absolute source/manifest paths, detached record and committed host revision. It offers no identity/destination selector, link or readiness field.
- Stamp a manifest from bundled bytes with release `1.0.0`; validate through `ValidateManifest` before any filesystem mutation. Reject ill-formed UTF-8 before JSON encoding can replace it. Generate the literal JSON title separately.
- Under the registry lock reject live/tombstoned IDs, capacity and revision exhaustion before writing. Exclusively claim the app directory; any existing destination including dangling links is unavailable. Generate through pinned directory descriptors with no-follow opens and exclusive regular-file writes; directories are 0700, files 0600. Create a missing owned root through its existing parent, never through a symlink.
- Re-read and validate the generated manifest before calling the same locked registration primitive used by `Register`. Only persistence success publishes state/revision/notifications. Retain incomplete source unregistered on failure; never delete or overwrite retained bytes.
- `Registry.Inspect(ctx, serverID, appID) (Source, error)` validates local identity and looks up committed state under the lock. Return the same derived locations and current detached record/revision, without touching source. Foreign, malformed, unknown and tombstoned identities fail.
- Private per-call mint/generation seams permit deterministic failure tests without exposing selectors to consumers.

## Concurrency model

No new goroutines or processes. Create and inspect share the registry mutex with
all existing mutations/readers. Generation and commit occur within that lock;
callbacks retain the existing prohibition against calling registry methods.
Context cancellation is checked before work, between generated files and before
commit. All opened descriptors close on return. One registry owner per root remains
required; hostile same-account directory renames are outside that ownership model.

## State transitions and identity reuse

| Event | Race test |
|---|---|
| Independent/repeated create uses fresh identities and distinct trees | `TestCreateAndInspect` |
| Concurrent create serializes durable results | `TestConcurrentCreate` |
| Reused live/tombstoned/filesystem ID is rejected without mutation | `TestCreateFailures` |
| Reopen and repeated inspect preserve editable bytes and identity | `TestCreateAndInspect` |
| Failed generation/commit retains unregistered files; retry cannot overwrite | `TestCreateFailures` |
| Foreign/unknown/tombstoned inspection fails | `TestInspectRejectedIdentity` |

## Error handling

Use existing manifest/identity/capacity/exhaustion/not-found/removed/foreign errors.
Add distinct static, actionable errors for ID minting, occupied destination,
source generation and registry persistence. Never return a success result on failure;
source errors disclose no contents, title, raw path or process diagnostics.
Cancellation wraps no success. A later attempt mints a fresh ID; retained incomplete
source is available for operator inspection and is never silently registered.

## Testing strategy

Write focused Go tests first and observe the absent API compile failure. Cover two
creations, exact generated inventory and byte content versus maintained files,
matching stamped title/manifest, defaults, detached results, reopen and unchanged
edited source; deterministic negative cases check registry bytes, revisions,
notifications, tombstones and retained content. Execute the compiled test binary in
a separate working directory with empty PATH to prove bundled creation uses no
checkout paths or commands. Test symlink rejection, cancellation and concurrent
creates. Run `go test -race ./internal/apps/... ./templates/hosted-app/...`, focused
standalone scaffold tests (no install/build), `go vet ./...`, and `go build ./cmd/pyry`
with output outside the tree. The verifier owns the full-module gate.

## Open questions

None. Local-control exposure (#3265), daemon wiring (#3266), channel tools (#3126)
and publication are downstream work.

## Documentation handoff

Pending for the documentation stage: `docs/hosted-apps.md` § Template and saved
preferences and § Create, publish and update: document daemon-minted identity,
bundled-source creation, identity-based inspection and the unpublished
`available`/`stopped` state with no active release or open link. Distinguish standalone
scaffold generation from local registration and identify local-control/channel
exposure as downstream work.

## Security review

PASS. Trust boundary: only title enters `Create`; `ValidateManifest` enforces its
contract before writes, and identities/destinations come from the daemon. File
operations: MUST FIX addressed in design by exclusive app/file creation and
no-follow descriptor-relative opens, including each directory component; retain
partial attempts rather than overwrite on retry. Crypto: UUIDv4 uses `crypto/rand`,
with no credential or key introduced. Errors: static actionable stage errors do
not disclose source contents or caller titles. Concurrency: one registry lock
covers check/generate/commit, with no new lock ordering or goroutine. Subprocess,
network, token lifecycle and viewer isolation do not apply: creation executes
no template commands and exposes no runtime resource. Hostile same-account root
renames and pre-existing registry persistence hardening are out of scope; daemon
state ownership is the same trusted boundary as the existing registry/runtime.

## Revisions

### 2026-10-12 — Verifier finding 1: compiled template boundary

**Design:** Replace the Design section's whole-source-tree embedding with exact
maintained asset paths in `Assets`. Go embedding does not honor `.gitignore`;
filtering generation through `inventory.json` happens after compilation and cannot
prevent private working-tree bytes entering binaries. Keep the original assets
and shared inventory without a second asset copy. `TestBundleInventory` checks
that the compiled filesystem contains exactly the inventory plus the manifest
and inventory JSON, including required dotfiles. Inventory changes must also
update the explicit embed paths.

**Security review:** PASS with the packaging MUST FIX addressed. The added trust
boundary is working checkout to distributed executable: compile only exact
maintained files, never directories or globs. Credentials in `.env`/`.env.*`,
SQLite data and sidecars, dependencies, build output and unlisted adjacent source
must remain outside the bundle even when present at build time. This introduces
no runtime input, process, network, crypto or concurrency changes.

**Testing:** `TestBundleExcludesWorkingFiles` recompiles the unchanged production
declaration in a temporary package seeded with synthetic private files and runs
`TestBundleInventory` against that compiled filesystem. It fails with the original
recursive declaration. Existing creation tests continue to verify maintained
asset contents, literal titles, no commands and creation outside the checkout.
