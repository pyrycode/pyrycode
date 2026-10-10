# Hosted-app contract v1 (#3120)

## Files read

- `internal/protocol/envelope.go` → `Envelope`: connection-local correlation; app frames must not acquire conversation/session metadata.
- `internal/protocol/handshake.go` → `HelloClientPayload`, `HelloAckPayload`, `ErrorPayload`: additive negotiation and existing error shape.
- `internal/relay/v2session_handshake.go` → `negotiateCapabilities`: supported-set intersection; advertising support grants no authorization.
- `internal/dispatch/dispatch.go` → `Route`: authenticated handler boundary, known-type validation and static errors.
- `internal/protocol/attachments.go` → `AttachmentChunkPayload`, `MaxAttachmentChunkBytes`: base64/chunk sizing precedent, not reused app DTOs.
- `internal/identity/server_id.go` → `ParseServerID`: canonical host UUIDv4 identity.
- `docs/protocol-mobile.md` § Application-envelope size cap and Security model: 65519 plaintext bytes, Noise authentication and relay blindness.
- `docs/knowledge/features/protocol-package.md` and `dispatch-package.md`: semantic validation belongs to consumers; handlers must send their own errors.
- `docs/knowledge/decisions/037-capability-strings-not-version-numbers.md`: features use literal capability strings, independently of release versions.
- `docs/knowledge/features/development-verification.md` § Protocol boundaries: examples must prove required, omitted and null fields distinctly.
- `CODING-STYLE.md`: persistence, error and file-scope conventions.
- Issue #3120 approved prototype and implementation map: responsive shell, SQL display preferences and fleet-service ownership.
- Node release policy and Node 24 SQLite API: Node 24 LTS; SQLite binding stability 1.2 (release candidate) from 24.15.0.

## Context

Define one shared, normative v1 contract before daemon, template and native-viewer implementations proceed. Apps are registered host resources, independent of channel sessions. The orchestrator is the first consumer and maintained test app. This design deserves a later decision record for the Node/SQLite runtime and native web-content isolation boundary; the documentation stage owns that record.

Sizing: four acceptance criteria; approximately 650–750 total written lines, including this plan and embedded examples; zero new exported types, zero consumer migrations, zero executable reject branches. The shipped #2576 compatibility spec is the contract analogue, with this ticket adding resource streaming and app persistence. Recount before handoff; remain below 800 written lines. No other fetched feature branch edits this artifact.

## Design

Append the normative contract below this committed plan. Define manifest identity/version/path rules, durable registration versus observed state, separate source/build/data roots, startup and SQL migration guarantees. Fix additive `hosted_apps_v1` negotiation and named `Envelope` payloads for discovery, notifications, assets, API requests, streaming credit and cancellation; use the existing `error` payload. Specify per-view host/app/release binding in Electron and Android, resource URL behavior, permitted HTTP semantics and bounded encrypted transfers. Embed parseable JSON examples as the common fixtures for downstream consumers, and assign implementation responsibilities using the issue's family roadmap.

## Concurrency model

This slice starts no processes, workers or goroutines. The contract will specify per-app serialized publish/migration, bounded request workers, streaming backpressure and cancellation ownership for downstream implementations.

## State transitions and identity reuse

None in this implementation: the only artifact is a specification. Its normative lifecycle/reconnect/update matrix will name the scenarios and implementation tickets that must supply race and native-viewer tests; those tests belong to the implementations rather than this prose-only slice.

## Error handling

Specify static wire errors and distinguish HTTP application responses from bridge failures. Uncertain mutations are never replayed on reconnect. A failed update preserves the previous release and usable SQL data; unrecoverable restoration is reported as failed, never successful rollback.

## Testing strategy

Before appending examples, run a scratch validator that fails on their absence. After writing, extract every JSON fence and check parsing, exact required/optional/null rules, message vocabulary, request/reply correlation, decoded bytes, chunk ordering/completion, and maximum-frame arithmetic independently of the prose. Review the four acceptance criteria and responsibility map. Run `git diff --check`, `go vet ./...` and a scratch-output `go build ./cmd/pyry`; no Go package is touched, so there is no scoped race suite. The verifier owns the full-module gate; live-agent/native acceptance belongs to #3133 and the client tickets.

## Open questions

None: the normative contract must settle all fields, paths, limits, isolation and rollback decisions before this stage ends.

## Documentation handoff

Pending for the documentation stage:

- `docs/hosted-apps.md` § Manifest and identity, Runtime and layout, Client resource bridge, Create, publish and update: fold in the approved contract, preserving exact fields, limits, runtime choice, isolation boundary and failure/data guarantees; state that the contract is defined ahead of hosting implementation.
- `docs/protocol-mobile.md` § Hosted apps (v2): link the contract, list its capability/message vocabulary, and state that app frames are host-scoped, encrypted and independent of conversation routing.
- Consider a decision record for the runtime and isolation boundary described in Context.

## Normative contract

The following sections are the v1 design deliverable. Hosting is not implemented by this ticket.

### 1. Scope, versions and identity

MUST, MUST NOT and SHOULD are normative. This contract defines hosted apps v1 ahead of hosting implementation. React content runs in an isolated native viewer; compiled TypeScript services run as trusted code under the daemon's OS account. There is no new conversation type, public marketplace, arbitrary WebSocket tunnel or claim of an OS sandbox. V1 dashboard freshness uses bounded API polling.

Three versions answer different questions:

| Value | Meaning |
|---|---|
| `contract_version: 1`, capability `hosted_apps_v1` | This manifest/resource contract and its fixed field semantics. |
| `release_version`, e.g. `1.0.0` | One immutable app release; changes when that app is published again. |
| Transport `v2` | Existing Noise-encrypted transport; neither an app release nor a runtime version. |

The existing persisted `server_id` is the host identity (`ParseServerID`). An app's daemon-minted `app_id` is a canonical lowercase UUIDv4, distinct from channel/session IDs. The key is `(server_id, app_id)`. Neither field is a credential. Titles, source directories, service ports, releases and channel membership may change without changing this key. Copying an app to another host creates a new registration/app ID; importing a foreign manifest requires explicit restamping by local tools. Deleted app IDs are never reassigned.

The stable link is exactly `pyrycode://hosts/<server_id>/apps/<app_id>` (no credentials, release, service address, query or fragment). Clients resolve it through their paired host records and fresh discovery. A missing pairing shows the existing host-pairing entry point; a missing app shows unavailable. A link never pairs a host, changes keys, registers an app or grants access. The host and app label remain in native chrome outside web content.

### 2. Manifest and validation

`app.json` is UTF-8 JSON, at most 16384 bytes, containing one object, no duplicate keys or trailing values. All table fields are required unless marked optional. Required fields cannot be null. Optional fields are omitted when unset; explicit null is invalid. Reject unknown manifest fields in v1 (including nested objects), so misspelled build/entry fields cannot silently pass. A future incompatible manifest requires a new contract version; its capability is negotiated separately.

| Field | JSON type | V1 constraint |
|---|---|---|
| `contract_version` | integer | Exactly `1`. |
| `server_id` | string | Canonical UUIDv4; must equal this daemon's persisted identity. |
| `app_id` | string | Canonical UUIDv4 minted by create; updates must match the registered app. |
| `title` | string | 1–128 UTF-8 bytes; no C0/C1 controls, leading/trailing whitespace or all-whitespace value. Render as text. |
| `description` | string, optional | 1–512 UTF-8 bytes, no C0/C1 controls; display metadata only. |
| `release_version` | string | Three decimal components `MAJOR.MINOR.PATCH`, each 0–2147483647, no leading zeros except `0`, suffixes or `v`; at most 32 bytes. |
| `runtime` | object | Fields in the runtime table. |
| `frontend` | object | Fields in the frontend table. |
| `service` | object | Fields in the service table. |
| `build` | object | Fields in the build table. |

| Object.field | JSON type | V1 value / meaning |
|---|---|---|
| `runtime.node` | string | Exactly `>=24.15.0 <25.0.0`; Node.js 24 LTS only. |
| `runtime.sqlite` | string | Exactly `node:sqlite`; file-backed SQLite. |
| `frontend.root` | string | Exactly `dist/web`; public asset root in the published release. |
| `frontend.entry` | string | Exactly `index.html`, relative to `frontend.root`; SPA entry. |
| `service.entry` | string | Exactly `dist/service/main.js`; compiled ECMAScript-module service entry. |
| `service.migrate` | string | Exactly `dist/service/migrate.js`; compiled, one-shot migration entry. |
| `service.health_path` | string | Exactly `/_pyry/health`; private readiness endpoint. |
| `service.migrations` | string | Exactly `sql/migrations`; relative directory of migration SQL. |
| `build.package` | string | Exactly `package.json`; `type` must be `module`. |
| `build.lockfile` | string | Exactly `package-lock.json`, committed and consistent with package metadata. |
| `build.install` | array of strings | Exactly `["npm", "ci"]`. |
| `build.typecheck` | array of strings | Exactly `["npm", "run", "typecheck"]`. |
| `build.test` | array of strings | Exactly `["npm", "test"]`. |
| `build.compile` | array of strings | Exactly `["npm", "run", "build"]`. |

These fixed paths are release-relative, not native service addresses. Tools validate that every required entry is a regular file inside its selected root; entry/public-asset/migration paths permit no symlink component, `..`, absolute path or device file. Locked runtime dependencies may contain package-internal symlinks only when their entire resolution remains inside that build's `node_modules`; publication validates and inventories those targets. Frontend output MUST contain only public assets: no source secrets, SQL database, service code, lockfile, environment file or `node_modules`. Validation covers runtime version, manifest identity, lockfile, command exit status, compiled entries and migration filenames. A release version is unique per app: a reused version with different bytes is rejected, and publishing identical previously recorded bytes is an idempotent no-op. New updates increase the version numerically; rollback may reactivate an older recorded release.

### 3. Runtime, roots and service conventions

Use Node.js 24 LTS, version 24.15.0 or newer in the 24.x line, on Linux/macOS. Check the actual executable before install/build/migration/start; an unavailable or mismatched runtime is a validation/start error, never an automatic fallback to another major or SQLite package. The exact Node patch, npm version, lockfile digest and build digest are recorded with the release for reproducibility. The runtime selection follows [Node's release policy](https://nodejs.org/en/about/previous-releases). `node:sqlite` has **stability 1.2, release candidate**, from 24.15.0; this is an explicit dependency, not a stable-API promise. See the [Node 24 SQLite API](https://nodejs.org/download/release/latest-v24.x/docs/api/sqlite.html). Use `DatabaseSync` with parameter binding, foreign keys, defensive mode and extension loading disabled. SQL runs on the app-service side, never through a native-client SQL API.

The daemon selects an absolute `APPS_ROOT` below its private state directory; it is not a field supplied by app content. Directory modes are 0700, registry/database/backup files 0600. Platform state-directory resolution follows the daemon's existing configuration. Stable structure below this root:

```text
APPS_ROOT/
  registry.json                       durable registrations and publish recovery journal
  <app_id>/source/app.json             editable source manifest
  <app_id>/source/package.json         scripts and pinned dependencies
  <app_id>/source/package-lock.json    dependency lock
  <app_id>/source/src/web/main.tsx     React frontend entry; router base /
  <app_id>/source/src/service/main.ts  TypeScript service entry
  <app_id>/source/src/service/migrate.ts
  <app_id>/source/sql/migrations/0001_preferences.sql
  <app_id>/source/test/                unit/service/frontend contract tests
  <app_id>/staging/<build_id>/          private candidate tree; never client-visible
  <app_id>/builds/<build_id>/app.json   immutable published manifest
  <app_id>/builds/<build_id>/dist/web/index.html
  <app_id>/builds/<build_id>/dist/web/assets/
  <app_id>/builds/<build_id>/dist/service/main.js
  <app_id>/builds/<build_id>/dist/service/migrate.js
  <app_id>/builds/<build_id>/sql/migrations/
  <app_id>/builds/<build_id>/node_modules/  locked service dependencies
  <app_id>/data/app.sqlite             persistent SQL file, never inside a build
  <app_id>/data/                       WAL/SHM and app-owned durable files
  <app_id>/backups/<publish_id>/       consistent pre-update data snapshot
```

`build_id` is the lowercase SHA-256 digest of a UTF-8 JSON inventory array sorted by relative-path UTF-8 bytes, serialized without whitespace and with fixed key order: each file is `{path, kind:"file", size_bytes, sha256}` and each permitted dependency link is `{path, kind:"symlink", target}` (its exact relative target). Strings escape only quotes/backslashes and controls (controls as lowercase `\u00xx`); other UTF-8 bytes stay literal. The inventory excludes itself; directories have no entry. The publisher assigns it, not the browser. Builds become read-only before activation; edits occur only in source/new staging. Runtime output belongs in data or daemon-managed logs, never a build. The registry maps app/release identities to validated roots and digests; network fields are never joined to arbitrary host paths. Registry writes use same-directory temporary file, sync, close and atomic rename; activation/data restoration have a recoverable journal. Builds and app data persist across channel stop, `/clear`, idle eviction, daemon restart and client reconnect.

The template owns `npm run typecheck` (TypeScript checks both frontend and service without emitting), `npm test` (noninteractive, deterministic tests against temporary SQL files), and `npm run build` (frontend bundle, compiled service/migration JS and copied SQL). No runtime TS loader, Node type stripping or dev server is part of publication. Install/build scripts execute as trusted app code; publishing from a channel uses the existing assistant/operator authorization boundary. These commands are argv arrays executed in the selected source/staging tree, not shell text assembled from wire fields.

The daemon starts `node dist/service/main.js` with cwd equal to the immutable build directory. It supplies only a documented minimal environment plus explicitly configured app-service integrations; client tokens, Noise keys and daemon-control credentials are never supplied. The mandatory values are:

| Environment variable | Meaning |
|---|---|
| `PYRY_APP_ID`, `PYRY_SERVER_ID` | Registered identity. |
| `PYRY_APP_RELEASE` | Candidate/active release version. |
| `PYRY_APP_DATA_DIR` | Absolute persistent data directory. |
| `PYRY_APP_DB_PATH` | Absolute `data/app.sqlite` path. |
| `PYRY_APP_BIND_HOST` | Exactly `127.0.0.1`. |
| `PYRY_APP_PORT` | Daemon-reserved ephemeral loopback TCP port, never from a manifest/request. |
| `NODE_ENV` | Exactly `production`. |

The reservation must prevent port reuse by another app during launch; bind collision fails startup and gets a fresh endpoint on retry. The service binds only that host/port and exposes `/api/…` plus `/_pyry/health`. Neither endpoint nor environment paths are returned to a client. The daemon resolves API targets from its private live-process record; it disables HTTP redirects, proxies and connection reuse across process generations. Loopback privacy is a routing boundary, not authentication against other processes under the same OS account.

Migrations run first using `node dist/service/migrate.js` with the same identity/data environment, exit 0 only on completed schema validation. Ordered SQL files match `^[0-9]{4}_[a-z0-9_]+\.sql$`, have unique increasing prefixes, and are immutable once applied. The SQL `pyry_schema_migrations` table records `version INTEGER PRIMARY KEY` and `sha256 TEXT NOT NULL`; an applied checksum mismatch fails validation. Missing new migrations are applied in one explicit SQLite transaction; failures roll back the transaction. Nontransactional SQL and external side effects are forbidden in migrations. The migration process has a 60-second deadline.

A service is ready only when `GET /_pyry/health` returns HTTP 200 and JSON `{ "ready": true, "app_id": "…", "release_version": "…" }` matching the launched identity, and the process remains alive. Probe every 250 ms, each with a 1-second timeout, for at most 15 seconds after service spawn. Health is checked every 5 seconds while running; three consecutive failures transition to failed and stop that process. SIGTERM allows 5 seconds to close HTTP/SQL and children; then the daemon kills the owned process group and waits. Startup/stop are independent of session supervision. Read-header timeout 5 seconds, request read/write timeout 30 seconds, idle timeout 30 seconds and private health reply cap 4096 bytes are required service conventions.

### 4. Registration, observed state and safe publication

A durable registration survives a stopped or broken process. `desired` is exactly `available` or `stopped`. `available` asks the supervisor to reconcile to a running ready service; `stopped` suppresses starts. Registration is not proof of readiness. New create registers the identity with no active release; initial publish confirms readiness before exposing an openable link. Management/create/validate/publish/stop actions belong to local publishing tools (#3125/#3126), not web content or a generic relay execution verb.

Discovery records have these exact fields; unknown wire fields are ignored for additive compatibility. Required nulls carry absence explicitly. Manifest optionality does not change record nullability.

| Record field | JSON type | Meaning |
|---|---|---|
| `app_id` | string | Canonical local registered UUIDv4. |
| `title` | string | Same bound as manifest; current committed title. |
| `desired` | string | `available` or `stopped`, durable intent. |
| `state` | string | `starting`, `running`, `stopped` or `failed`, observed state. |
| `active_release` | string or null | Previously confirmed release; null until first successful publish. |
| `pending_release` | string or null | Candidate being validated/started; null otherwise. |
| `last_error` | object or null | Null on clean operation; otherwise `code` (error vocabulary below) and static `message` (1–160 bytes). No paths, logs or stacks. |
| `revision` | integer | Durable host-wide change sequence, 1–9007199254740991; this record's latest change. |

`running` requires an active release and confirmed health. `starting` covers reconciliation when no active process is ready. `stopped` covers intentional stop or a not-yet-published registration. `failed` means desired availability cannot be met. If the old service keeps running during candidate build, state stays `running` with a pending release. During exclusive migration/cutover state becomes `starting`, requests are quiesced, and the active release still denotes the last confirmed release. Successful cutover atomically updates active release/title, clears pending/error and reports running. Failed candidate startup/build restores running on the previous release with `last_error` set; an initially failed publish has null active release and state failed. A stopped registration may retain its active release; opening it shows stopped and does not silently start it.

The host change sequence advances for registration, desired/state/release/error changes and removal; persist before announcing. It never resets on reconnect/restart or wraps; exhaustion refuses changes rather than reusing revisions. Updates are whole records. `app_removed` carries a tombstone revision and removes only the registration; deletion does not silently erase data, and explicit data deletion is outside v1. No event-ring or conversation-history replay is used for apps.

Observed state is recomputed after daemon restart: a persisted running record is never readiness evidence. Mark an available app with an active release starting before launching/probing it; a registration without a published release stays stopped until initial publish. Crash/health-failure reconciliation uses exponential retry delays of 1, 2, 4, 8, 16, then 30 seconds, resets after 60 seconds healthy, and stops immediately when desired becomes stopped. Every launched process has a new private generation even when app ID/release/port are reused.

Publication is serialized per app, with at most one candidate and one writer to its SQL data. Build/typecheck/tests happen in private staging while the old service remains available. Before any migration, stop accepting that app's API work, cancel/retire pending reads/writes without replay, drain or stop the old service, and take a consistent recoverable snapshot of **all** app data including a SQLite backup that incorporates WAL state. Do not copy a live database file alone. Verify the snapshot before mutation. No fleet-side mutation may run as a startup/health side effect.

Run candidate migrations and startup against persistent data. Commit activation only after health succeeds. Build/start failure retains the old immutable build. Migration/start failure after data mutation stops the candidate and restores the verified pre-update data/schema before restarting and health-checking the old service. Sync the verified backup and a journal of candidate/backup/previous release before the first data mutation. Activation commits the active-release pointer and journal decision atomically in the registry. Restart before that commit restores the snapshot and old release; restart after it starts the committed new release without reapplying completed migrations. Recovery completes before accepting API requests. A successful rollback announcement requires old-release health plus validated restored data; failed restoration stays failed with `app.io_failed` and no openable readiness claim. Keep the previous working build and recovery snapshot until activation/recovery is durable. These guarantees assume trusted service code follows the data-directory and side-effect conventions; they cannot roll back arbitrary OS or fleet-service effects.

### 5. Capability and envelope vocabulary

Add the literal `hosted_apps_v1` to the supported capability intersection in `negotiateCapabilities`, advertised by both viewers via `HelloClientPayload.capabilities` and acknowledged via `HelloAckPayload.capabilities`. The daemon advertises it only when v1 registration, supervision and resource routing are wired and available. It grants no authorization: require the authenticated, nonrevoked paired device and host identity for every operation. V1 uses existing host-level pairing authorization for all registered apps, without a new per-app ACL; native page access is further restricted to the selected app. Revocation retires work through the existing authenticated-session close path. Apps do not require `interactive`, `thread` or an active channel session.

Without the negotiated capability, clients show that host's Apps feature as unsupported and send no app requests; hosts emit no app list/state/resource pushes to that peer. An old host may return existing `protocol.unknown_type`; a supporting host receiving an app request without negotiation returns `error` with `protocol.unsupported`. Other host connections/conversations continue normally. A new contract version uses a new capability and vocabulary for incompatible semantics, never reinterprets these enums.

All messages below use `internal/protocol.Envelope`: required `id`, `type`, `ts` (RFC3339 UTC), `payload`; optional `in_reply_to` only on replies. IDs are unique in each sender's connection lifetime, and hosted-app IDs/correlations stay within the JSON safe-integer range 0–9007199254740991 (reconnect before exhaustion). A reply's `id` is its sender's ID, not the request's; `in_reply_to` is the original request ID. Hosted frames omit `conversation_id`, `session_id`, `event_id`, `history_entry_id`, `session_state_cleared` and `payload_encrypted`. Noise wraps the complete plaintext envelope; `payload_encrypted` is not how v2 app encryption is signaled. Host is implicit in the authenticated connection, not a caller-selectable payload field.

For every hosted wire table, fields are required unless explicitly optional; null is permitted only where stated, and optional fields are omitted rather than null. Require exact JSON types (booleans are not integers), no duplicate keys/trailing values and valid UTF-8. Ignore unknown wire fields within the envelope cap; they grant no routing or authority. Invalid known fields fail validation. Payloads are objects, never null. `request_id` uses the same safe-integer range as envelope IDs; `next_index` uses the chunk-index range and the active request's expected index.

| Envelope type | Direction | Reply/correlation and payload |
|---|---|---|
| `list_apps` | client → daemon | Request with optional `cursor`; first page uses `{}`. |
| `apps` | daemon → client | Reply to list ID: `revision`, `items` array of records, `next_cursor` string or null. |
| `app_updated` | daemon → client | Unsolicited whole record; no `in_reply_to`. |
| `app_removed` | daemon → client | Unsolicited `{app_id, revision}`; no `in_reply_to`. |
| `app_asset_request` | client → daemon | `{app_id, release_version, path, navigation, query?}`. GET semantics. |
| `app_api_request` | client → daemon | `{app_id, release_version, path, query?, method, headers, body}`. |
| `app_response` | daemon → client | Once, replying to asset/API ID; response metadata below. |
| `app_response_chunk` | daemon → client | Ordered body chunks, each replying to that same asset/API ID. |
| `app_response_credit` | client → daemon | `{app_id, request_id, next_index}`; authorizes one next chunk of that client's request. No reply. |
| `app_cancel` | client → daemon | `{request_id, app_id?}`; app ID required for asset/API work, omitted for list work. |
| `app_cancelled` | daemon → client | Reply to cancel ID, `{request_id, app_id?}`; echoes cancel scope, no assertion that a mutation was rolled back. |
| `error` | daemon → client | Existing `ErrorPayload`, reply to failing request ID; terminal for that request. |

`Route`/the v2 routing boundary must recognize the vocabulary and enforce direction before dispatch. A client-sent response/notification is invalid. App work is performed by bounded cancellable workers; a stalled disk/service/credit wait cannot block that connection's cancellation or unrelated message handling. No relay changes or hosted-app parsing occur on the relay.

Discovery uses up to 16 records per page, sorted by `app_id`, including all desired/observed states. Cap registered apps at 256 per host in v1; create beyond that fails without overwriting anything. `items` is always an array, including `[]`. `apps.revision` is the host sequence captured for the page, 0–9007199254740991 (0 only before any registration). `next_cursor` is required and null at the end. A nonempty opaque cursor is at most 256 ASCII bytes and binds host, revision and last app ID; it expires after 30 seconds. Empty/null request cursors are invalid. The daemon validates it without using it as a path. If revision changed or cursor expired, return `apps.changed` and start a fresh walk; do not combine pages from different revisions. Fresh list is authoritative: reconcile records/tombstones by revision, buffer newer notifications during a walk (at most 256, then restart), discard older notifications and restart on a gap. Hosts emit updates in revision order to negotiated peers. At most one list walk/request is pending per connection; discovery/cancel/credit queues are bounded to 16 control envelopes per connection and remain runnable independently of resource workers. Reconnect always starts a new list walk.

### 6. Asset/API fields and HTTP semantics

| Request field | JSON type | Required rule |
|---|---|---|
| `app_id` | string | Registered app from the selected native view, resolved again by daemon. |
| `release_version` | string | Active release pinned when the native view was opened; exact match required. |
| `path` | string | Origin-relative absolute URL path starting `/`, at most 2048 ASCII bytes after percent encoding; never a host filesystem path. |
| `navigation` | boolean, asset only | Native-stamped top-level document navigation; permits the SPA fallback. False for subresources/Fetch. |
| `query` | string, optional | At most 1024 ASCII bytes, without leading `?`; omitted if empty. Preserve encoded query bytes, parameter order, duplicates and empty values. |
| `method` | string, API only | Exactly `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE` or `OPTIONS`. |
| `headers` | object, API only | Lowercase permitted header names mapped to string values; `{}` is valid. |
| `body` | string, API only | Required standard RFC4648 base64 with padding, empty string for no bytes; never null. Decoded size at most 32768 bytes. |

An asset path cannot enter `/api`, `/_pyry`, service/data/build-registry roots or another view origin. An API path must begin `/api/` and is forwarded unchanged to the selected active service, with its query and decoded body. GET/HEAD bodies must be empty, and services must implement GET/HEAD/OPTIONS as side-effect-free reads; mutations use POST/PUT/PATCH/DELETE. HTTP request bodies are opaque bytes, including JSON, form data or binary; `FormData` is serialized by the shim with an explicit content type/boundary. No upload streaming beyond the 32768-byte body limit in v1. No cookies, client credentials, bearer headers, service-address override, SQL primitive or durable-operation-key field is accepted.

Both native and daemon boundaries validate URL syntax and containment: reject schemes/authority, `//`, backslashes, NUL/control bytes, raw `?`/`#` in paths, invalid percent escapes, encoded path separators, encoded percent signs (no double encoding), `.`/`..` segments and decoded traversal. Decode once for containment/asset lookup; forward the validated escaped API path without another normalization/decode step. Queries are bounded opaque URL query text with valid percent escapes and no raw fragment/control bytes, never used for filesystem lookup. Validate the resolved asset handle within the pinned immutable public root, disallow symlinks and nonregular files, and prevent check/open races. Browser fragments are local SPA state and never travel on the wire.

Allowed request headers are `accept`, `content-type`, `if-none-match`; response headers are `content-type`, `cache-control`, `etag`, `last-modified`. Each header object is at most 4096 serialized UTF-8 bytes, at most 8 entries; each value at most 1024 UTF-8 bytes with no CR/LF/control characters. Unknown request or wire-response header names fail validation; daemon projection drops unpermitted backend response headers. `authorization`, `cookie`, `set-cookie`, `host`, `origin`, `referer`, proxy/hop-by-hop/connection headers, `location` and CORS headers never pass the bridge. Header names/values do not control native security settings. Native synthesizes content length from validated response size. Byte bodies are identity encoded; service compression is disabled and `content-encoding` is not forwarded.

| Response/chunk field | JSON type | Rule |
|---|---|---|
| `app_response.app_id`, `release_version` | string | Match original request and pinned release. |
| `app_response.status` | integer | API: 200–599. Asset: 200–299 or 400–599. No protocol upgrades, informational responses or automatic redirects. |
| `app_response.headers` | object | Permitted response headers above; required, may be `{}`. |
| `app_response.size_bytes` | integer | Exact decoded body length, 0–8388608 for assets, 0–1048576 for APIs. |
| `app_response.total_chunks` | integer | 0 for empty body; otherwise 1–256 (assets), 1–32 (APIs). |
| `app_response.sha256` | string | Required lowercase 64-hex SHA-256 of the complete decoded body, including empty body. |
| `app_response_chunk.app_id` | string | Match original request. |
| `app_response_chunk.index` | integer | Zero-based consecutive index, starting at 0. |
| `app_response_chunk.data` | string | Nonempty standard padded base64; decodes to 1–32768 bytes. |

There is no separate success-completion frame. Completion requires exactly the declared chunk count, byte length and digest; a zero-body response completes at metadata after verifying the empty digest. HEAD and HTTP 204/205/304 have zero body. HTTP errors such as 404/409/500 use `app_response` and ordinary body chunks, not bridge `error`. API redirects are exposed through the Fetch shim as HTTP responses with no redirect following or Location header. Fetch returns a Response with status/headers/body once complete, including non-2xx, using a null body for HEAD/204/205/304. Native resource loaders accept only 200–299/400–599; an API GET made by a resource loader that yields 3xx fails that load without following it. Static assets do not use conditional requests/304; native caches complete immutable bodies itself. This common loader subset follows [Android WebResourceResponse's status restrictions](https://developer.android.com/reference/android/webkit/WebResourceResponse). Native reason phrases come from a local status table, with `App response` for unknown codes; backend reason text is not forwarded. Incomplete bodies are never delivered as successful resources/Fetch results.

### 7. Bounded encrypted transfers and retirement

All byte limits use decoded bytes unless explicitly serialized. Check bounds before allocation/base64 decode/file read and before spawning work. `MaxAttachmentChunkBytes` (45000) is a precedent only; hosted chunks use **32768**, so base64 data occupies at most 43692 bytes. Every fully escaped/serialized hosted-app envelope MUST be at most **65519 bytes** before encryption; the Noise 16-byte tag brings ciphertext to at most **65535 bytes**. Existing outer base64/routing-envelope limits still apply independently. Producers measure final serialization, not just payload length; future additive fields cannot consume unchecked headroom.

| Limit | V1 value / outcome |
|---|---|
| Pending asset/API operations | 8 per authenticated connection, 32 per app across connections, 128 per daemon; admission beyond any limit returns `app.busy`. |
| Request body | 32768 bytes decoded; oversized input returns `app.limit_exceeded`, never truncates. |
| Response body | 8 MiB asset / 1 MiB API; chunk count 256 / 32; oversized declared/backend body returns `app.limit_exceeded`. |
| Response staging/assembly reservation | 16 MiB decoded per client connection and 64 MiB per daemon, reserved from declared lengths; exhaustion returns `app.busy`. |
| Native queued operations | 16 per view, including active operations; excess fails locally without creating a wire request. |
| End-to-end deadline | 30 seconds from native enqueue (also daemon acceptance independently); no reset on new chunks or credit. |
| Credit/read inactivity | 5 seconds without progress; cancel with `app.timeout`. |
| Queued response chunks | At most one 32768-byte decoded chunk per operation; metadata/control frames retain bounded separate capacity. |
| Path/query/headers | 2048 / 1024 ASCII bytes and 4096 serialized header bytes as above. |

The daemon stages a bounded stable response (memory or private temporary spool), determines exact size/digest, and reserves capacity before sending metadata. Reserve incrementally before every staging read/write when size is not yet known; the per-connection/daemon ceilings include disk spools, request bodies and queued decoded chunks, not just memory. A growing file/API stream cannot bypass these limits. Native likewise reserves assembly capacity before accepting metadata. Temporary spools are private and removed on completion, timeout, cancellation, disconnect or shutdown. Serialization buffers remain bounded (one envelope per worker). Queue exhaustion must cancel/retire work or close the affected connection rather than block unrelated cancellation indefinitely.

Initial request grants credit for chunk 0 only. After validating and consuming each nonfinal chunk, native sends `app_response_credit` naming the original client request ID and exactly the next index. It grants one chunk, never a window or unlimited stream. Duplicate credit for an already granted index is ignored; future/skipped credit is invalid and terminates that operation. Credits/requests are isolated by connection generation plus request ID and app ID. Cancellation/control frames bypass chunk waits; outgoing workers yield after each chunk, preserving other transport traffic. A producer may not enqueue the whole response in advance.

Accept metadata once before chunks. Wrong app/release/correlation, duplicate/out-of-order chunks, invalid base64, a byte/count overflow or inconsistent completion is a terminal bridge failure. Retire and release the whole operation; discard partial bytes. SHA-256 verifies assembly, while Noise supplies authentication. Credit frames receive no ack/error loop after an operation is retired; discard late chunks, metadata and credits without allocating or reviving state. Invalid active credit terminates its original request with `app.invalid_request`.

`app_cancel.request_id` is the original client asset/API/list request ID (not a daemon reply ID). A cancellation affects only work owned by that connection and matching app scope; a mismatched app scope is an idempotent no-op. List cancellation omits `app_id`. Duplicate/unknown/finished cancels reply `app_cancelled` with exactly the supplied request ID and the same optional app ID. No cross-connection lookup or ownership disclosure occurs. Native AbortSignal, closed tab, release replacement, disconnect and host switch immediately retire pending promises and partial assembly; if connected, send cancellation. Daemon cancels backend I/O and blocks future worker completions from being enqueued for the retired generation. Queued late replies may arrive and are discarded by native. Keep the cancel reply as a separate pending control operation, never revive its retired target.

Cancellation/timeout/disconnect cannot prove that POST/PUT/PATCH/DELETE (or any effectful app operation) did not commit. Fetch reports an abort/network failure and the viewer marks the result uncertain; it never retries a mutation automatically, even when an error is retryable. After reconnect/host switch, use new connection/view generations, renegotiate, refresh discovery and allow fresh reads. A request ID reset cannot revive old work; keys include the native connection generation and daemon connection/process generation. User-initiated reconciliation reads can determine saved state, and an explicit new mutation is a new request. V1 has no generic exactly-once/durable-operation key.

### 8. Electron and Android resource/Fetch bridge

Each native viewer creates a fresh unpredictable virtual origin `https://app-<32-lowercase-hex-random>.invalid/` bound outside page JavaScript to one authenticated host, registered app, active release and connection/view generation. This synthetic origin never resolves through the public network. Electron uses a dedicated isolated session with an [HTTPS protocol handler registered on that session](https://www.electronjs.org/docs/latest/api/protocol). Android uses [WebView resource interception](https://developer.android.com/reference/android/webkit/WebViewClient) for assets/modules and a narrowly scoped injected Fetch adapter for methods/bodies absent from [WebResourceRequest](https://developer.android.com/reference/android/webkit/WebResourceRequest). Both implement identical wire semantics. The page-facing adapter accepts only path/query/method/permitted headers/body/AbortSignal; native stamps app/release/request IDs and selects the connection. Raw envelopes and host/app/key selectors are never exposed to page code. Native enforces bounded serialization for Blob/FormData/string bodies before making a wire request.

Serve `/` and top-level SPA navigations as the pinned `index.html`. Relative resources, `<script type="module">`, dynamic imports, CSS URLs and root-relative URLs resolve under that same virtual origin. Emit frontend bundles with base `/`, no CDN or cross-origin imports. Strip fragments; preserve URL queries as specified. Only `navigation:true` outside `/api/`, with no matching asset and no file extension, permits SPA-entry fallback. A missing subresource or asset with an extension receives HTTP 404, never HTML masquerading as JS. `/_pyry` and other reserved paths never fall back. Emit correct MIME types for HTML/JS/CSS/JSON/images/fonts/Wasm, including `text/javascript` for module JS. Browser history refresh/back/forward retain the binding. A GET to `/api/…` goes through the API route, including when made by a resource loader; non-GET API methods use Fetch. Resource interception rejects raw non-GET traffic before dispatch, so bypassing the Fetch shim cannot silently lose a POST body. Fetch to assets permits GET only. XMLHttpRequest, sendBeacon, EventSource and WebSocket forwarding are unsupported; block them without a direct-network fallback. Form submission is disabled; app writes use Fetch. Range requests and streaming Response bodies are unsupported in v1 and fail locally; apps split large application datasets into bounded API reads.

Native denies every other automatic network route/scheme (`file:`, arbitrary HTTP(S), WS, device-local services), cross-view origins, popups, nested frames, downloads, workers/service workers, native-file access and unrestricted JS/native interfaces. A top-level external HTTPS anchor may open through the client's existing system-browser handler only after a user gesture; it receives no native credentials or bridge. OS permissions/daemon controls are not page APIs. Electron has `nodeIntegration: false`, `contextIsolation: true`, renderer sandbox enabled and no remote module/general IPC exposure. Android disables file/content access and mixed content; the narrow message bridge checks the sender's exact main-frame origin and binding on every invocation, using [origin-restricted WebMessageListener](https://developer.android.com/reference/androidx/webkit/WebViewCompat), not an unrestricted `addJavascriptInterface`. If that WebView feature is unavailable, refuse to open the app with an unsupported-viewer state. Native controls URL interception/navigation regardless of page cooperation.

The native viewer injects/enforces this policy as resource response headers: `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; worker-src 'none'` and `X-Content-Type-Options: nosniff`. The template therefore uses external JS/CSS with no inline handlers, eval or inline style dependency. Blob/data origins cannot access the bridge. API MIME types cannot override navigation policy. Isolation is tested with hostile app HTML/JS, not established merely by CSP.

No device token, Noise/static key, paired-host registry, client filesystem or generic native API enters the renderer. Cookies/HTTP authentication are disabled and Fetch credentials are omitted. Browser storage is disposable UI cache; durable preferences live in app SQL. Client asset caches are keyed by host/app/release and never shared across views/hosts; cache only validated complete bodies. Initial open/reconnect validates discovery; on an active-release change retire the old view and reload into a new bound generation. Never mix JS/assets/services from two releases. Native connection loss shows disconnected chrome and prevents new mutations until refreshed discovery confirms running; cached content is not readiness evidence.

The daemon independently resolves registered app and active service for every request and rejects foreign/stale app IDs, unavailable states or release mismatch. A hostile paired native client may submit app IDs, but still cannot select arbitrary paths/services or cross authenticated hosts. A hostile page cannot even select a different registered app through the native adapter. Trusted app services may use configured fleet integrations and their OS-account permissions; web isolation does not sandbox those services or hide secrets from trusted service code.

### 9. Errors and lifecycle test obligations

Bridge failures use existing `error` with required `code`, static `message` (1–160 UTF-8 bytes), `retryable` boolean and optional `retry_after_s` integer 1–30 when retryable. Other `ErrorPayload` fields are omitted. Correlation is solely `in_reply_to`; clients retain host/app context in their pending table. Never echo request bodies, headers, paths, service endpoints, tokens or stack traces. Internal logs may record validated app/host IDs, connection/request IDs, safe reason codes and byte counts; content, full URLs/queries and secrets are excluded.

| Code | Retryable | Meaning / recovery |
|---|---|---|
| `protocol.unsupported` | false | App capability not negotiated; show unsupported. |
| `app.invalid_request` | false | Malformed/forbidden field, path, header, direction or active stream; correct request. |
| `app.not_found` | false | Unknown or inaccessible registration; same response, refresh list. |
| `app.release_changed` | false | Requested release is no longer active; refresh discovery and replace view. |
| `app.unavailable` | true | Starting/stopped/failed service; show state, fresh read only when available. |
| `app.limit_exceeded` | false | Byte/chunk/create limit; reduce payload or dataset. |
| `app.busy` | true | Bounded concurrency/assembly capacity full; advisory retry after 1 second. |
| `app.timeout` | true | Deadline/inactivity/health timeout; abandon partial bytes and reconcile uncertain writes. |
| `app.io_failed` | true | Build/start/read/migration/restore failed; use observed state and safe local diagnostics. |
| `apps.changed` | true | Discovery cursor expired or host revision changed; restart first page. |

Retryable is advisory, never authority to replay an uncertain mutation. App-specific HTTP rejection stays an HTTP response. Native-only stream corruption surfaces a generic resource/network failure and sends cancellation, without exposing raw diagnostic content to the page.

The following scenarios are required tests in the implementation tickets, run with `go test -race` for daemon logic and equivalent native tests. This prose-only slice does not claim those tests passed.

| Event / reused identity | Required scenario and owner |
|---|---|
| Create repeated, duplicate release | Same operation preserves app ID; different bytes under same version rejected (#3121/#3125). |
| Stop/start, crash/restart | Same registration/data; desired stopped never launches; process generation rejects stale health/completion (#3122). |
| Channel clear/eviction, daemon restart | Apps persist independently; reconcile desired state and preserve preferences (#3121/#3122/#3133). |
| Back-to-back publish / cutover crash | Serialize writers; journal restores consistent active build/data; no port/worker reuse (#3125/#3122). |
| Failed build/start/migration/restore | Old release runs only with usable restored schema; restore failure never advertises rollback (#3125). |
| Chunk credit / duplicate cancel | One-credit bounded queue; cancellation escapes blocked writer; unknown cancel is idempotent (#3123). |
| Disconnect/reconnect with ID reuse | Old replies/credits cannot enter new assembly; list refreshed, no write replay (#3123, desktop #1927, mobile #2063). |
| Host/app/view switch / release update | Pending bodies released; no cross-host/app resources or mixed releases (both viewer tickets). |
| Discovery paging / remove and reopen | Revision drift restarts walk; tombstones remove app; deleted identity is not reused (#3121 and sidebar tickets). |
| Malicious HTML/paths/headers | Bridge cannot select keys/other apps/local files/services; API bodies work on Android; relative imports/SPA work on both viewers (#3123 and both viewer tickets). |

### 10. Shared JSON examples

These JSON fences are normative cross-client fixtures. Arrays contain successive envelopes in one example, not a new batch wire format. Timestamps and sample UUIDs are synthetic. IDs are local to each example's connection. Both clients and daemon tests should extract these examples without changing keys or null/omission rules. The compact small chunks demonstrate ordering; production may use any nonempty chunk up to 32768 bytes within count/byte limits.

Manifest:

```json
{
  "contract_version": 1,
  "server_id": "11111111-1111-4111-8111-111111111111",
  "app_id": "22222222-2222-4222-8222-222222222222",
  "title": "Orchestrator",
  "release_version": "1.0.0",
  "runtime": {"node": ">=24.15.0 <25.0.0", "sqlite": "node:sqlite"},
  "frontend": {"root": "dist/web", "entry": "index.html"},
  "service": {"entry": "dist/service/main.js", "migrate": "dist/service/migrate.js", "health_path": "/_pyry/health", "migrations": "sql/migrations"},
  "build": {
    "package": "package.json", "lockfile": "package-lock.json",
    "install": ["npm", "ci"], "typecheck": ["npm", "run", "typecheck"],
    "test": ["npm", "test"], "compile": ["npm", "run", "build"]
  }
}
```

Negotiation (hello travels in existing encrypted handshake early data; hello_ack in Noise response early data). An unrecognized advertised name is absent from the ack:

```json
[
  {"id":1,"type":"hello","ts":"2026-10-10T18:00:00Z","payload":{"role":"client","device_name":"Example phone","client_version":"pyrycode-mobile/1.0.0","protocol_versions":["v2"],"capabilities":["hosted_apps_v1","future_example"],"token":"synthetic-example-token"}},
  {"id":1,"type":"hello_ack","ts":"2026-10-10T18:00:00Z","in_reply_to":1,"payload":{"protocol_version":"v2","server_id":"11111111-1111-4111-8111-111111111111","conn_id":"example-connection","capabilities":["hosted_apps_v1"]}}
]
```

Discovery plus lifecycle update, including required nulls:

```json
[
  {"id":2,"type":"list_apps","ts":"2026-10-10T18:00:01Z","payload":{}},
  {"id":2,"type":"apps","ts":"2026-10-10T18:00:01Z","in_reply_to":2,"payload":{"revision":7,"items":[{"app_id":"22222222-2222-4222-8222-222222222222","title":"Orchestrator","desired":"available","state":"starting","active_release":null,"pending_release":"1.0.0","last_error":null,"revision":7}],"next_cursor":null}},
  {"id":3,"type":"app_updated","ts":"2026-10-10T18:00:02Z","payload":{"app_id":"22222222-2222-4222-8222-222222222222","title":"Orchestrator","desired":"available","state":"running","active_release":"1.0.0","pending_release":null,"last_error":null,"revision":8}}
]
```

Two-chunk asset body decodes to exactly `abcdef` (6 bytes); credit permits the second chunk:

```json
[
  {"id":3,"type":"app_asset_request","ts":"2026-10-10T18:00:03Z","payload":{"app_id":"22222222-2222-4222-8222-222222222222","release_version":"1.0.0","path":"/assets/example.txt","navigation":false,"query":"v=1&v=2"}},
  {"id":4,"type":"app_response","ts":"2026-10-10T18:00:03Z","in_reply_to":3,"payload":{"app_id":"22222222-2222-4222-8222-222222222222","release_version":"1.0.0","status":200,"headers":{"content-type":"text/plain"},"size_bytes":6,"total_chunks":2,"sha256":"bef57ec7f53a6d40beb640a780a639c83bc29ac8a9816f1fc6c5c6dcd93c4721"}},
  {"id":5,"type":"app_response_chunk","ts":"2026-10-10T18:00:03Z","in_reply_to":3,"payload":{"app_id":"22222222-2222-4222-8222-222222222222","index":0,"data":"YWJj"}},
  {"id":4,"type":"app_response_credit","ts":"2026-10-10T18:00:03Z","payload":{"app_id":"22222222-2222-4222-8222-222222222222","request_id":3,"next_index":1}},
  {"id":6,"type":"app_response_chunk","ts":"2026-10-10T18:00:03Z","in_reply_to":3,"payload":{"app_id":"22222222-2222-4222-8222-222222222222","index":1,"data":"ZGVm"}}
]
```

API write stores `{"period":"24h"}` in SQL; HTTP 204 confirms the write with an empty-body digest:

```json
[
  {"id":5,"type":"app_api_request","ts":"2026-10-10T18:00:04Z","payload":{"app_id":"22222222-2222-4222-8222-222222222222","release_version":"1.0.0","path":"/api/preferences","method":"POST","headers":{"content-type":"application/json"},"body":"eyJwZXJpb2QiOiIyNGgifQ=="}},
  {"id":7,"type":"app_response","ts":"2026-10-10T18:00:04Z","in_reply_to":5,"payload":{"app_id":"22222222-2222-4222-8222-222222222222","release_version":"1.0.0","status":204,"headers":{},"size_bytes":0,"total_chunks":0,"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}}
]
```

Cancellation of a new in-flight read; the cancellation ID differs from the retired request ID:

```json
[
  {"id":6,"type":"app_api_request","ts":"2026-10-10T18:00:05Z","payload":{"app_id":"22222222-2222-4222-8222-222222222222","release_version":"1.0.0","path":"/api/computers","query":"period=24h","method":"GET","headers":{},"body":""}},
  {"id":7,"type":"app_cancel","ts":"2026-10-10T18:00:05Z","payload":{"app_id":"22222222-2222-4222-8222-222222222222","request_id":6}},
  {"id":8,"type":"app_cancelled","ts":"2026-10-10T18:00:05Z","in_reply_to":7,"payload":{"app_id":"22222222-2222-4222-8222-222222222222","request_id":6}}
]
```

Bridge error is distinct from HTTP failure:

```json
[
  {"id":8,"type":"app_asset_request","ts":"2026-10-10T18:00:06Z","payload":{"app_id":"22222222-2222-4222-8222-222222222222","release_version":"1.0.0","path":"/","navigation":true}},
  {"id":9,"type":"error","ts":"2026-10-10T18:00:06Z","in_reply_to":8,"payload":{"code":"app.busy","message":"app request capacity is full","retryable":true,"retry_after_s":1}}
]
```

Removal is host-scoped and does not remove saved data:

```json
{"id":10,"type":"app_removed","ts":"2026-10-10T18:00:07Z","payload":{"app_id":"22222222-2222-4222-8222-222222222222","revision":9}}
```

### 11. Responsive shell and orchestrator walkthrough

The reusable React shell has native-independent page header/title, wrapping tab navigation, responsive main content, labeled filters, cards/tables and a polite status/error region. Use the [approved prototype](https://github.com/pyrycode/pyrycode/issues/3120#issuecomment-6100756272) as layout/behavior reference: Computers, Projects, Boards, Runs, Performance, Allowances. Match theme using local CSS variables; readable contrast, keyboard focus, accessible controls and safe-area padding work at desktop widths and a 320-pixel phone viewport. Computer cards use three columns above 760 px and one below; boards use four columns above 760 px, two below and one below 420 px. Charts/trends stack below 650 px; wide tables scroll horizontally. Coarse-pointer controls have at least 44-pixel touch height. No global horizontal page overflow or hover-only chart interaction.

The orchestrator's combined CPU/memory graphs have independent per-computer selectors defaulting to 24 hours. Runs puts active work above history in one tab with common project/computer filters. Performance keeps averages and model-combined trends beneath each agent role in one view. Boards show ticket labels. Ticket drain finishes all remaining stages of already-owned tickets; agent drain finishes current runs then pauses. Fleet service owns claims, runs, telemetry, participation, capacity limits, drain state and allowances. The app service adapts configured fleet APIs; it does not duplicate fleet ownership in app SQL or let browser content select integration URLs.

Store display preferences (selected tab, per-computer chart periods, project/computer filters, performance range) in `data/app.sqlite` via bounded `/api/preferences` reads/writes. Keep versioned preferences with defaults for missing/unknown fields. Saved preferences are app-scoped in v1 and shared by its authorized viewers; devices do not silently create competing fleet state. Opening from another client and updating the app must recover those preferences. SQL writes use prepared statements/transactions; fleet controls go through the fleet adapter with its authorization and outcome semantics. Test controls point only at isolated fixture services.

1. **Create:** In a channel, request an orchestrator app. Assistant invokes create through local tools, mints/stamps this host's stable app ID, creates the editable template, and records originating channel plus app identity for follow-up targeting. The registration exists but is not yet openable; no success link is returned.
2. **Validate:** Assistant edits source and runs runtime/manifest/lock checks, install, typecheck, tests and compile. Preference tests use temporary SQLite; frontend output is self-contained and public-only. Validation failure reports safe actionable channel feedback and leaves any existing active release serving.
3. **Publish:** Publisher snapshots/serializes the update, runs migrations and service health, and durably commits the activated release. Tools return structured readiness confirmation with matching `server_id`, `app_id`, active release and current revision. A queued build or a PID alone is not confirmation.
4. **Open:** Only after that confirmed readiness, assistant returns `pyrycode://hosts/11111111-1111-4111-8111-111111111111/apps/22222222-2222-4222-8222-222222222222` to the originating channel. Apps discovery also lists it under the connected host. Desktop/mobile validate running state, bind the view and load relative modules/API data over encrypted transport. Stop/removal after confirmation may make the link unavailable; the link remains the same identity.
5. **Update:** A follow-up request targets the same registered app, increments release to `1.0.1`, and repeats checks/publish. Preserve app ID, stable link and SQL preferences. Clients retire the old release view and reopen on confirmation. Failed build/start/migration retains or restores the prior working release/data as specified; channel response reports failure rather than a new readiness claim. Offline clients refresh discovery on reconnect and never replay an uncertain preference/fleet write.

### 12. Implementation responsibilities and handoff

| Ticket | Responsibility fixed by this contract |
|---|---|
| [#3121](https://github.com/pyrycode/pyrycode/issues/3121) | Manifest/registration DTOs and validation, durable desired state/revisions, discovery and lifecycle notifications, negotiated capability with readiness wiring. |
| [#3122](https://github.com/pyrycode/pyrycode/issues/3122) | Independent service supervision, selected private endpoints, health/stop/restart generations and shutdown tests. |
| [#3123](https://github.com/pyrycode/pyrycode/issues/3123) | Resource/API DTOs/handlers, encrypted request routing, containment, limits, chunk credit, errors and connection teardown. |
| [#3124](https://github.com/pyrycode/pyrycode/issues/3124) | React/compiled-TypeScript/node:sqlite scaffold, lockfile/scripts, service/migration/health conventions and responsive shell. |
| [#3125](https://github.com/pyrycode/pyrycode/issues/3125) | Local create/validate/publish/update tools, immutable builds, serialized SQL snapshot/migrations, atomic activation/recovery and readiness result. |
| [#3126](https://github.com/pyrycode/pyrycode/issues/3126) | Channel assistant tool exposure, stable app targeting and readiness-only link response to originating channel. |
| [Desktop #1927](https://github.com/pyrycode/pyrycode-desktop/issues/1927), [mobile #2063](https://github.com/pyrycode/pyrycode-mobile/issues/2063) | Bound isolated viewers, resource/Fetch adapters, shared examples, cancellation/reconnect/security tests. |
| [Desktop #1928](https://github.com/pyrycode/pyrycode-desktop/issues/1928), [mobile #2064](https://github.com/pyrycode/pyrycode-mobile/issues/2064) | Host-scoped Apps sidebar/list, state, stable-link opening and native chrome. |
| [#3127–#3132](https://github.com/pyrycode/pyrycode/issues/3127) | Orchestrator/fleet adapter, approved responsive views and SQL display preferences; detailed assignment stays in the issue's implementation map. |
| [#3133](https://github.com/pyrycode/pyrycode/issues/3133), [desktop #1929](https://github.com/pyrycode/pyrycode-desktop/issues/1929), [mobile #2065](https://github.com/pyrycode/pyrycode-mobile/issues/2065) | Maintained orchestrator acceptance app, live channel create/update, isolated fleet controls, both native transports and persistence/failure proof. |

All design decisions above are resolved. Runtime implementation, executable DTO/fixture tests, scaffold/dashboard and live-agent/native acceptance remain in these tickets. Documentation updates remain pending exactly as listed in this plan's Documentation handoff; this contract must not be described as shipped hosting.
