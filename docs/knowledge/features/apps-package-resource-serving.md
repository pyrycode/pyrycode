# `internal/apps` — resource-serving

Part of the [apps package overview](apps-package.md).

## Resource-serving

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

Bodies must support concurrent `Close` that promptly unblocks a pending `Read`,
including on generation retirement. Caller-context cancellation alone cannot
satisfy this obligation. See [concurrency](apps-package-concurrency-and-testing.md#concurrency-and-persistence) for cleanup
ordering and [testing](apps-package-concurrency-and-testing.md#testing) for the source-error regression.

Confined callable assets, MIME/path handling and navigation/SPA fallback remain
pending in [#3258](https://github.com/pyrycode/pyrycode/issues/3258). API forwarding,
backend header projection and HEAD/204/205/304 no-body semantics belong to
[#3246](https://github.com/pyrycode/pyrycode/issues/3246); delivery admission,
connection ceilings, credit, deadlines, wire serialization and transport error
mapping belong to [#3247](https://github.com/pyrycode/pyrycode/issues/3247).
Paired resource routing and `hosted_apps_v1` capability activation remain pending
under [#3123](https://github.com/pyrycode/pyrycode/issues/3123).

