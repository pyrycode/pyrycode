# `internal/apps` — resource-serving

Part of the [apps package overview](apps-package.md).

## Resource-serving

`Owner.PrepareAsset` takes `AssetRequest` (escaped path/query, native-stamped
navigation, reservation callbacks and optional read progress) and returns
`PreparedResponse` under the ownership contract below. `responseTarget` captures
committed readiness before lookup; expected-generation checking before staging rejects a same-release
restart. Release equality alone would silently rebind earlier lookup work.

`selectBuild` pins the unique build whose validated manifest matches
`Record.ActiveRelease`, never the candidate release, without commands or data
mutation. `assetFile` walks
only that build's `dist/web` using descriptor-relative no-follow opens. Only
genuine absence permits fallback; unsafe builds/entries return `ErrInvalidStorage`.
Opening an empty final component could misclassify an existing directory as
missing: inspect trailing-slash entries and reject them rather than serving HTML.

`validateAssetURL` bounds escaped path/query to 2048/1024 ASCII bytes, decodes the
path once, and rejects unsafe syntax or reserved roots with static `ErrAssetPath`:
`/api`, `/_pyry`, `/service`, `/dist/service`, `/data`, `/registry.json`, `/source`,
`/staging`, `/builds`, `/backups`, `/sql`, `/node_modules`, including escaped
spellings and descendants. Queries never select files. `/` selects `index.html`;
only missing extensionless native navigation falls back. Other misses yield
empty text/plain HTTP 404. Assets return 200 with exact bytes and deterministic
HTML/CSS/JSON/image/font/Wasm MIME, including `text/javascript` for `.js`/`.mjs`
and octet-stream for unknown extensions; no redirects or conditional 304.
Shared staging enforces 8 MiB through EOF, including growth. See the
[asset reference](../../hosted-apps.md#callable-confined-assets) for URL bounds,
the MIME table and safe failures.

### Private API forwarding

`Owner.ForwardAPI(ctx, serverID, appID, release, APIRequest)` resolves authenticated
host identity, registration, exact active release and health-confirmed ownership
before service I/O, preserving the identity/readiness errors below. The caller
supplies context and required `Reserve`/`Release` callbacks. Requests go once
through the opaque `Target.Do`; no caller-selected service/URL/port, proxies,
redirect following, credentials, cookies or automatic retries are admitted.
Cancellation of an uncertain mutation cannot establish rollback or justify replay.

Methods are exactly GET, HEAD, POST, PUT, PATCH, DELETE and OPTIONS. Opaque
decoded bodies preserve binary/form/JSON bytes, are bounded to 32768 bytes and
must be empty for GET/HEAD. Request headers allow only lowercase `accept`,
`content-type`, `if-none-match`; `{}` is valid. Both request and projected response
header objects allow at most 8 entries, 4096 serialized JSON UTF-8 bytes and
1024 bytes per control-free, valid UTF-8 value. Body overflow is `ErrAPILimit`;
malformed/forbidden requests or missing callbacks are `ErrAPIRequest`, before I/O.

`apiURL` requires an escaped `/api/` path of at most 2048 ASCII bytes and optional
raw query of at most 1024 ASCII bytes without leading `?`. It rejects URL/authority
forms, repeated separators, raw spaces/backslashes/controls/fragments, malformed
escapes, encoded path separators/percent signs and raw or decoded dot segments.
Decoded paths must be valid UTF-8 and control-free. Validated path/query bytes
retain percent-escape case, parameter order, duplicates and empty values.
Setting `url.URL.RawPath` alone would let Go silently re-escape an invalid spelling;
`apiURL` requires `EscapedPath()` to equal the supplied path before forwarding.
Queries are escape-validated but never parsed or normalized. See the
[API reference](../../hosted-apps.md#callable-private-api-forwarding).

`prepareAPI` projects only lowercase `content-type`, `cache-control`, `etag` and
`last-modified`, joining repeated values with `, ` and dropping other headers.
HTTP 200–599, including errors/redirects, is ordinary response data; upgrades and
unsupported statuses fail metadata validation. Compression negotiation and
automatic decompression are disabled; absent or identity encoding is accepted,
and non-identity `Content-Encoding` returns `ErrResponseMetadata`. HEAD/204/205/304
close/discard the backend body and stage zero bytes with the empty-body SHA-256.
Other bodies use shared preparation with a 1048576-byte limit, exact size/digest
and the reservation/disposal contract below. Metadata, response-limit, capacity
and backend I/O errors remain distinct, sanitized package failures. Backend bodies
close on every outcome; partial bodies never become successful results.

The pre-I/O target is passed to expected-generation preparation, including empty
responses: they have no backend body read to detect retirement. Removal, stop,
exit, release replacement or same-release restart cancels in-flight/retained
results without rebinding. Caller cancellation/deadlines cover headers and stalled
body reads and retain context errors. `Metadata`/`Read`, delivery completion and
exactly-once cleanup follow the shared contract below. See the
[API fixture and empty-result regressions](apps-package-testing.md#testing).

### Stable response ownership

`Owner.Prepare(ctx, serverID, appID, release, ResponseInput)` resolves committed
local registration and active release before staging a caller-supplied
identity-encoded body. The authenticated boundary supplies `serverID`; this
method does not authenticate connections. Unknown, removed and foreign-host apps
return `ErrNotFound`; an accessible app with another active release returns
`ErrReleaseChanged`; a matching release without a live, health-confirmed owned
generation returns `ErrUnavailable`. Preparation and every `Metadata`/`Read`
remain bound to that process generation. Removal, release replacement, stop,
exit or retirement invalidates in-flight and retained results; even a restart
of the same release cannot revive them. Healthy metadata-only commits preserve
the generation.

`ResponseInput` supplies `Body`, `Size` (-1 for unknown), nonnegative decoded
`Limit`, `Status`, `Headers`, and required `Reserve`/`Release` callbacks. A nonnil
body transfers to preparation immediately on invocation, including identity or
metadata failure, and is closed exactly once before successful return. Do not
mutate the source or headers during preparation. Afterwards, source/header edits
cannot alter the staged bytes, metadata or digest. `ResponseMetadata` contains
status, detached headers, exact size and lowercase 64-hex SHA-256 of the complete
body, including empty, binary and HTTP-error bodies.

`ResponseInput.Progress` is optional; `AssetRequest.Progress` and
`APIRequest.Progress` pass it through `prepareAsset` and `prepareAPI` into
staging. `Owner.ForwardAPI` preserves the callback while preparing the backend
response. It reports every underlying source read returning
positive bytes, including individual reads combined by `io.ReadFull`, even when
the read also returns an error. Zero-byte reads, EOF without bytes and reservation
activity do not report progress. The callback must return promptly and never
reenter the response: staging invokes it while holding the response mutex.
Leaving it nil preserves existing preparation and cleanup behavior.
Reservations cannot substitute for this callback: a known-size body is reserved
before reading, so preparation may continue making progress for more than five
seconds without another reservation. The relay uses positive reads to enforce
[read inactivity independently of its fixed deadline](v2-session-manager-concurrency-hosted-discovery-and-ordered-delivery.md#hosted-discovery-and-ordered-delivery).
`TestResponseProgress` checks known/unknown sizes, zero-byte reads and multiple
source reads per staging chunk; `TestAssetProgress` checks asset forwarding and
the empty 404 path. `TestAPIProgress` checks API forwarding for ordinary and HTTP
error bodies, no progress for discarded 204/205/304 bodies, and retained
reservations after response consumption through EOF. Discarding a HEAD or
204/205/304 body does not count as a positive source read.

Status must be 200–599; only lowercase `content-type`, `cache-control`, `etag`
and `last-modified` are accepted. Header objects allow at most 8 entries and
4096 serialized JSON UTF-8 bytes; values must be valid, control-free UTF-8 of at
most 1024 bytes. Empty/nil input headers become `{}`. Invalid metadata, size,
limit or missing callbacks returns `ErrResponseMetadata`. Ordinary HTTP errors
are successful preparation; consumer-specific policy is described in the
[HTTP reference](../../hosted-apps.md#assetapi-fields-and-http-semantics).

Staging uses immutable memory chunks, with no private disk spool. A known size
above the limit fails with `ErrResponseLimit` before body allocation/read; other
known sizes are reserved before either. Unknown sizes and growth reserve
incrementally before allocation/read and are checked through EOF, never truncated
into success. `Reserve(n)` atomically acquires all positive `n` bytes or refuses;
refusal returns `ErrResponseCapacity` before further unreserved I/O. Both callbacks
must return promptly and never reenter the result. Proving EOF at the decoded
limit requires a reserved probe byte: even an empty or exact-limit body may need
one temporary byte beyond its final size. Refusing that reservation must stop
before the probe read. EOF releases unused reservations, retaining exactly the
stable body size until delivery completion/disposal, even after consumption.

The caller owns delivery. `PreparedResponse.Metadata` returns a detached snapshot
while usable; `Read` serializes checked consumption. Copied metadata expires on
retirement, and the delivery boundary must suppress retired work. After successful
delivery call `Complete(nil)`; report delivery failure with `Complete(deliveryErr)`;
use `Close` to abandon delivery. EOF alone does not release capacity. Source
read/close failures and delivery failures reported while usable yield static
`ErrResponseIO`, exposing no source path, endpoint or request diagnostics. Caller
cancellation preserves standard context errors. Preparation failure, cancellation,
generation retirement and disposal close the source and release acquired reservations exactly once;
cleanup also reaches retained, unconsumed results. `Close` is idempotent, and
disposal may race metadata access, consumption and cancellation safely.

`PreparedResponse.Done` exposes the response context's cancellation channel
without locks or backend I/O. It closes on caller cancellation, backend
retirement or disposal, including successful `Complete(nil)` and `Close`.
It is an invalidation signal, not a delivery verdict or a cleanup-completion
barrier; `Complete` and `Close` join cleanup. A consumer that interprets every
closed signal as backend failure can emit an unavailable error after a complete
response. Track final transport delivery separately and preserve explicit
`Complete` errors. The relay's `TestHostedAssetTransfer` sends a trailing control
request to detect such terminal output; a bytes/digest-only assertion would
remain green.

Bodies must support concurrent `Close` that promptly unblocks a pending `Read`,
including on generation retirement. Caller-context cancellation alone cannot
satisfy this obligation. See [concurrency](apps-package-concurrency-and-testing.md#concurrency-and-persistence) for cleanup
ordering and [testing](apps-package-concurrency-and-testing.md#testing) for the source-error regression.

Callable assets and backend API forwarding are implemented independently of
encrypted delivery. The relay implements paired asset/API routing, shared
admission and decoded-byte budgets, credit-paced chunks, fixed 30-second deadlines,
5-second read/credit inactivity and transport error mapping; see the
[wire behavior](../../protocol-mobile.md#asset-transfers-credit-and-cancellation).
The relay reserves decoded API request bytes before allocation and retains them
until response disposal; `APIRequest.Reserve`/`Release` account for response
staging within that same budget. Request reservation activity does not report
preparation progress. The callable APIs honor supplied context deadlines and
caller capacity policy. Production `newDaemonAppsManager` installs both providers
on discovery/supervision's existing owner/registry and attests the selected runtime
before message processing; see
[infrastructure readiness](apps-package-lifecycle-and-restart-state.md#lifecycle-and-restart-state).
The [counted #3440 proof](../../hosted-apps.md#implementation-and-validation-ownership)
records **13 executed, 13 passed, 0 failed, 0 skipped** focused fake-relay tests
and subtests with a race-built daemon and synthetic `node` reporting `v24.15.0`.
The same evidence records passing real-relay and actual Node/template arms
(#3441); native assembly, publisher and live-channel acceptance remain downstream.

A zero-body 204 finishes at response metadata, so withholding chunk credit cannot
establish a disconnect after commit but before response. `TestHostedAppsPairedResources`
uses a backend commit/response barrier, waits for old-request cancellation, then
renegotiates and refreshes discovery before submitting a read with the old request
ID. Only then may the old handler return. This forces late old-generation output
to compete with the replacement read, rather than letting a pre-reconnect cleanup
hide misrouting. Exact readback and mutation invocation counts establish preserved
committed state without replay; the service process and saved identity/data must
also survive transfer retirement.
