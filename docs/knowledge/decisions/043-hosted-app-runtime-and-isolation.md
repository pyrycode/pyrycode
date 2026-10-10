# 043. Hosted apps use Node 24 and SQLite behind an isolated native viewer

## Context

The [hosted-app v1 contract](../../specs/architecture/3120-hosted-app-contract.md#normative-contract)
must give daemon, template, Electron and Android implementations one runtime,
persistent layout and trust boundary. Apps survive channel/session teardown and
updates. A browser origin alone cannot authorize host resources, and a saved
browser preference cannot provide durable SQL state across devices or releases.
This decision is accepted as a design ahead of hosting implementation.

## Decision

Use Node.js 24 LTS, **24.15.0 or newer within 24.x**, compiled TypeScript ESM
services and file-backed SQLite through `node:sqlite` on Linux/macOS. The manifest
fixes `runtime.node` to `>=24.15.0 <25.0.0` and `runtime.sqlite` to `node:sqlite`.
Editable source, immutable published builds and persistent app data have separate
roots. The daemon chooses private loopback endpoints and supplies identity,
data and health conventions; content cannot select a service address.

Native viewers bind a fresh synthetic HTTPS origin to one authenticated host,
registered app, active release and connection/view generation. A narrow
resource/Fetch bridge stamps identity outside page JavaScript and carries bounded
requests over existing encrypted v2 transport. Web content receives no client
keys, arbitrary host paths/services or general native APIs. Compiled app services
and install/build scripts execute as trusted code under the daemon's OS account;
this does **not** create an OS sandbox for them.

## Rationale

The [Node release policy](https://nodejs.org/en/about/previous-releases) provides
the LTS runtime line. The built-in binding avoids a separate SQLite package,
but the [Node 24 SQLite API](https://nodejs.org/download/release/latest-v24.x/docs/api/sqlite.html)
has stability **1.2, release candidate**, from 24.15.0. That qualification is an
explicit dependency, not a stable-API promise. Compiling services before publish
keeps runtime TypeScript loaders and development servers out of activation.

Android resource interception does not expose request bodies, and its resource
responses reject 3xx statuses. Using interception as the whole Fetch bridge would
lose writes or fail otherwise valid HTTP responses. Both clients therefore use
the same body-capable Fetch adapter and the resource-loader status subset;
see [client resource bridge](../../hosted-apps.md#client-resource-bridge).

## Consequences

Runtime mismatch fails validation/start instead of selecting another Node major
or SQLite package. Releases record the actual Node patch, npm version, lockfile
digest and build digest. Native isolation needs hostile-content tests in both
viewers; CSP alone is insufficient evidence.

Preferences live in app SQL, while fleet ownership remains in the fleet service.
Copying a live SQLite file alone can omit WAL state: publication must verify a
consistent snapshot of all app data before mutation and restore usable data/schema
before health-checking an old release and announcing rollback. Failed restoration
stays failed, and arbitrary trusted-service OS/fleet effects cannot be rolled back.
See [publication and recovery](../../hosted-apps.md#create-publish-and-update).

The [responsibility map](../../specs/architecture/3120-hosted-app-contract.md#12-implementation-responsibilities-and-handoff)
assigns runtime/template, supervisor, publisher, transport and native conformance
work to their implementation tickets. This decision does not report those checks
or live acceptance as passed.
