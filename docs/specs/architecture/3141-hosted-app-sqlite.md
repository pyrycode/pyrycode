# Hosted-app SQLite preferences (#3141)

## Files read

- `templates/hosted-app/source/src/service/{main,migrate}.ts`: unfinished entries to replace; CodeGraph has no template symbols indexed.
- `templates/hosted-app/source/src/service/runtime.mjs` → `assertRuntime`: retain actual-executable rejection in every entry.
- `templates/hosted-app/source/{package.json,tsconfig.service.json,scripts/build.mjs}`: ESM compilation and public-only web build.
- `templates/hosted-app/{scaffold.mjs,test/scaffold.test.mjs}`: generated identity and fresh toolchain/release-copy proof.
- `templates/hosted-app/source/test/app.test.mjs`: retain frontend and runtime assertions.
- `docs/knowledge/features/apps-package.md` § Template toolchain and Testing: preserve early npm runtime rejection and literal-title checks.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: omissions, nulls and wrong types need separate cases.
- `docs/specs/architecture/3120-hosted-app-contract.md` § Runtime, roots and service conventions; `docs/hosted-apps.md` § Runtime and layout; ADR 043: private persistent SQL, fixed runtime and loopback health contracts.
- Node v24.15.0 SQLite documentation → `DatabaseSync`, `setAuthorizer`: available defensive mode and native SQL authorization.

## Context

Replace placeholder entries with one app-shared preference record that survives client, process and release replacement. Migration/startup correctness belongs to that persistence deliverable; browser rendering remains #3142. No new decision record is needed beyond ADR 043.

Sizing before commitment: approximately 740 total written lines (340 service/SQL/configuration, 320 tests, 80 plan), no exported types/interfaces, no existing consumer signature changes, four acceptance criteria, and no new state-machine reject branches. #1105 is a service/test analogue, but native SQLite authorization removes a separate SQL parser. No fetched feature branch overlaps target files. #3140 is merged; actual parent is #3124, with no grandparent.

## Design

Shared `environment` validation derives release root from the compiled module location, validates runtime, all eight supplied environment values, canonical UUIDv4/release bounds and manifest identity. Resolve existing path ancestors before rejecting release/source/staging/build placement or DB symlink redirection. No start/health path creates data; migration alone creates private directory/file, using 0700/0600 modes.

Shared SQL helpers open file-backed `DatabaseSync` with foreign keys, defensive mode and extension loading disabled. A transaction helper owns BEGIN/COMMIT/ROLLBACK. Schema validation checks the preference and ledger tables, saved record and database integrity. The singleton preference schema constrains its identity, version and enums. API validation constructs only the supported record, uses defaults for omitted properties and ignores unknown fields; parameter-bound replacement commits before 204.

Migration inventories regular release-relative SQL files, checks exact filename shapes and unique numeric versions, hashes raw bytes, and validates all applied versions before pending work. One transaction creates/updates the ledger and applies every pending file, validating schema before commit. While executing migration SQL, SQLite authorization denies transaction/savepoint control, attach/detach, PRAGMA, external-capable operations/functions and ledger tampering; runner SQL executes outside that policy.

The HTTP service binds exactly the supplied loopback endpoint after opening an existing validated database. Streamed bodies are counted up to 16384 bytes; malformed or invalid preferences return 400, overflow 413, SQL failures 503. Health freshly checks SQL/schema before returning bounded ready identity JSON. Header, request and idle deadlines are explicit. `npm run health` compiles to an independent entry that validates environment and performs one no-redirect HTTP request with an absolute one-second deadline and 4096-byte response cap.

## Concurrency model

One Node event loop owns synchronous SQL operations and transactions; asynchronous request collection cannot interleave a transaction. HTTP sockets and timers belong to the server. SIGTERM stops admission and closes active/idle sockets and SQL immediately, within the five-second allowance. Migration runs as a separate one-shot process; publisher serializes it against service writers.

## State transitions and identity reuse

| Event | Deterministic Node test coverage (no Go state touched) |
|---|---|
| Repeated save, omitted values, rejected mutation | preferences replacement, validation and body boundaries |
| Restart and replacement release with same app/database | preferences survive restart and release replacement |
| Repeated migration, changed/missing applied file, pending failure/retry | migration ordering, ledger validation and atomic rollback |
| Startup failure, readiness loss, endpoint collision | startup and readiness failures |
| SIGTERM with incomplete active request | health command and outstanding-client shutdown |

## Error handling

Startup/migration/health failures report a clear diagnostic and nonzero exit, never select a fallback endpoint/runtime. HTTP failures use static responses without input or SQL/path disclosure. All failed transaction work rolls back; rejected requests never open a write transaction. Startup never repairs or migrates an unavailable schema.

## Testing strategy

Write generated-app integration tests first; compile service before `npm test` independently of a build. Use isolated temporary release copies and SQLite files, child processes and actual HTTP requests. Exercise defaults, shared reads, enum/type/null/omission validation, exact byte boundary and chunked overflow, commit failure, restart/release replacement, ordered/idempotent migrations, checksum/missing-file rejection before pending work, whole-pending rollback and forbidden SQL. Cover every environment value, unsafe/symlink paths, missing schema/database, readiness loss, bind collision, health mismatch/status/redirect/byte/deadline failures and shutdown with an outstanding request. Update scaffold release-copy assertions and retain public-web output checks. Record executed/passed/skipped counts. Run fresh npm ci, typecheck, test, build in that order, template scaffold suite, git diff --check, go vet ./... and scratch-output go build ./cmd/pyry. Full-module gate belongs to verifier; no live Claude/native proof is required.

## Open questions

None.

## Documentation handoff

Pending for documentation stage: `docs/hosted-apps.md` § Template and saved preferences: record and enums; replacement/default/unknown-field policy; 400/413 and 16384-byte limit; app-shared scope; migration filenames/checksum ledger/atomic rollback; every startup environment value and migrate/start/health commands. Replace unfinished-service wording, preserve #3140 scaffold workflow, explain persistent SQL outside replaceable builds and migration before startup, and state scaffold/service health does not register, publish or return a ready app link.

## Security review

**Verdict:** PASS

- [Trust boundaries] `environment` validates supplied identity/paths before effects; request validation constructs an allowlisted record before SQL.
- [Tokens] No credentials generated, read or returned; preference data is private 0600 SQL storage.
- [File operations] MUST FIX addressed in design: canonicalize absent-path ancestors before creation and reject DB redirection into release/source/staging/build trees. Trusted publisher owns release/data directories; hostile same-account filesystem mutation is outside ADR 043's isolation model.
- [Subprocesses] Only tests invoke argv-based child processes; service starts no children.
- [Cryptography] SHA-256 hashes migration bytes for immutability, not authentication; no secret comparison or custom crypto.
- [Network and I/O] Streamed request and probe response caps plus absolute deadlines prevent unbounded socket reads. Loopback is routing privacy, not same-account authentication.
- [Errors/logs] Static HTTP errors; entry diagnostics exclude request bodies and environment dumps.
- [Concurrency] Transactions execute synchronously; signal handling closes owned sockets and SQL; retries do not replay mutations.
- [Threat model] Native hostile-content isolation and publisher backup/recovery remain assigned to native-viewer tickets and #3125; daemon supervision is #3122.

**Reviewer:** builder (self-review); **Date:** 2026-10-11
