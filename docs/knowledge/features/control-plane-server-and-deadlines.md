# Control Plane — server construction and deadlines

Split from [control-plane.md](control-plane.md). This document owns dependency
installation, pairing/update providers and control request/response deadlines.

## Server Construction

```go
func NewServer(
    socketPath string,
    sessions   SessionResolver,
    logs       LogProvider,
    shutdown   func(),
    log        *slog.Logger,
    sessioner  Sessioner,
) *Server
```

`sessions` is the only required dependency that nil-panics at construction. Programmer error surfaces immediately, not on the first request from a future shell.

`logs`, `shutdown`, and `sessioner` are optional. When nil, the corresponding verb returns an error response — used in tests that care about isolated verbs. `sessioner` is wired in production to `*sessions.Pool` in `cmd/pyry/main.go` (#116); `*sessions.Pool` satisfies `Sessioner` directly because `Pool.Create` returns `sessions.SessionID`, matching the interface signature with no adapter (contrast with `poolResolver` for the read-side `Lookup`). Pre-#116 the call site passed `nil` and `VerbSessionsNew` returned `"sessions.new: no sessioner configured"`. See `docs/specs/architecture/75-control-sessions-new.md` for the seam design.

Dependencies whose implementations need daemon composition state are installed after construction rather than widening `NewServer`. `SetPairingProvider` installs the narrow `func(deviceLabel string, allowRemotePermissions bool) (string, error)` used by `VerbPairingMint`; the provider owns every identity, key, relay, registry, and persistence input that the request cannot supply. `handlePairingMint` copies the closure under `Server.mu` and releases the lock before invoking it, so a slow provider does not serialize unrelated control verbs behind the server lock.

For a relay-enabled daemon, `runSupervisor` installs that provider before
`Server.Serve` from the same `pairingMinterV2` that `startRelayV2` constructed
for the active relay leg. The closure therefore carries the running daemon's
already-resolved server id, relay URL, static public key, and registry path; it
does not reload saved configuration that may describe another service. Relay
setup failures never return a provider, and a relay-disabled daemon deliberately
leaves the seam nil.

The pairing seam is also a credential-redaction boundary. An absent provider returns exactly `pairing.mint: provider not configured`; a missing payload or any provider error returns exactly `pairing.mint: operation failed`. On the error branch, `handlePairingMint` discards both the provider's returned string and its error detail, emits no control-layer log, and leaves `Response.Pairing` absent. `MintPairing` likewise returns an empty string for every error, including the fixed `control: empty pairing.mint response` guard for a missing or empty success payload. Only a nil-error, non-empty pairing reaches the caller.

Operational failure detail from the concrete provider, including a registry
path, remains daemon-only. Its fixed success and failure events omit the
caller-supplied label, token, token hash, and encoded pairing, so copying the log
snapshot into a diagnostic bundle does not create another credential egress.

### Update-when-idle provider

`SetUpdateWhenIdleProvider(func() (UpdateWhenIdleResult, error))` installs the
optional release-selection and scheduling provider without changing `NewServer`.
It is safe to call concurrently; nil clears the provider. `handleUpdateWhenIdle`
copies it under `Server.mu`, unlocks, and invokes it exactly once for the request.
The provider owns release selection and eligibility. The payload-free request
exposes no binary path, release URL, version override or eligibility bypass:

```json
{"verb":"update.when-idle"}
```

`Response.UpdateWhenIdle` (JSON `updateWhenIdle`) carries an
`UpdateWhenIdleResult`: required `decision`, optional `reason`, and optional
`releaseTag`. Success returns one of these typed decisions:

| Decision | Go constant | Required detail |
| --- | --- | --- |
| `up-to-date` | `UpdateUpToDate` | None |
| `not-eligible` | `UpdateNotEligible` | Nonblank `reason` explaining the refusal |
| `will-install` | `UpdateWillInstall` | Nonblank `releaseTag` naming the selected or already-pending release |

For example, an accepted scheduling decision is:

```json
{"updateWhenIdle":{"decision":"will-install","releaseTag":"v9.8.7"}}
```

Both server and client use `validateUpdateWhenIdleResult` to reject a missing or
unknown decision, `not-eligible` without a reason, or `will-install` without a
tag. Whitespace-only reason/tag values are rejected; valid values are preserved
verbatim. A missing response result returns `update.when-idle: missing decision`;
an empty or unknown discriminant returns `update.when-idle: invalid decision`.
Missing required details return `update.when-idle: missing reason` or
`update.when-idle: missing release tag`.

An absent provider returns `Response.Error` with exactly
`update.when-idle: provider not configured`. Any provider error returns exactly
`update.when-idle: operation failed`, discarding both its error detail and any
returned result. Invalid provider results also produce only an error, with no
success payload. `UpdateWhenIdle(ctx, socketPath) (*UpdateWhenIdleResult, error)`
returns nil on every failure, including transport and validation failures; a
non-empty wire `error` takes precedence over any accompanying success payload.

`runSupervisor` installs the production `autoUpdater.request` provider before
the control server serves requests, with or without `-pyry-auto-update`.
`runUpdateArgs` calls `control.UpdateWhenIdle` for `pyry update --when-idle`.
The selected daemon owns metadata, eligibility, installation and restart;
explicit requests share the scheduler's active attempt and pending tag. Disabling
automatic scheduling prevents unsolicited checks/retries. See
[update flags](pyry-update-command.md#flags) and
[automatic update](pyry-update-command-automatic-update.md).

`will-install` accepts work; it does not report a completed installation. The
provider waits only for the bounded metadata/eligibility decision, with a
60-second production HTTP budget, never for idle, asset download, installation
or restart. Accepted work uses the daemon context, so disconnect or client
timeout does not retract it. Daemon shutdown cancels the work; `runSupervisor`
drains control handlers through `ctrlDone`, joins the scheduler, then joins
updater workers even when scheduling is disabled.

Publishing acceptance before installation alone does not prove the response was
written: an already-idle daemon can install and request restart immediately.
`autoUpdater.request` reads the published decision even after restart cancels
the daemon context, `Server.Serve` drains handlers through their response writes,
and `runSupervisor` joins that drain before exiting. For a connected caller
within the existing response deadline, this ordering preserves acceptance
across an update-triggered shutdown. A timing delay would not establish that
ordering. See the [contract spec](../../specs/architecture/2757-update-when-idle-contract.md)
and [integration spec](../../specs/architecture/2758-update-when-idle.md).

## Handshake Deadline: per-conn timeout and the session-verb extend (#865)

`handle` (the per-conn goroutine `Serve` spawns) sets `conn.SetDeadline(time.Now().Add(s.handshakeTimeout))` before decoding the client's JSON request — the bound that limits how long a connected-but-silent client can pin a per-conn goroutine. `s.handshakeTimeout` defaults to `defaultHandshakeTimeout` (5s), set once in `NewServer`'s struct literal; same-package tests may shrink it via the unexported `Server.handshakeTimeout` field (written once before `Serve` starts, read-only per-conn thereafter — no lock needed, same post-construction-override shape as `SetRekeyer`).

Handlers adjust the connection or write deadline after the handshake read has
already completed. The approval and session-verb policies are:

- `handleApprove` **clears** it (`conn.SetDeadline(time.Time{})`) for the length of a blocking approval wait — the conn stays open for however long the human decision takes, until `mcpApprovalTimeout` or a disconnect/shutdown watcher resolves it. (Until #1535, `handleAttach` was the other clearer, handing the conn to the bridge for the indefinite life of an attachment; that verb and its handler are gone.)
- `handleSessionsNew` / `handleSessionsRm` **extend** it to `sessionOpTimeout + sessionOpConnGrace` (30s + 5s = 35s) immediately before calling `Pool.Create` / `Pool.Remove`. Before #865, the deadline was left at its 5s handshake value while each handler's own ctx budgeted 30s for the op — once `Create`/`Remove` ran past 5s (routine on a cold claude spawn: documented 2-15s latency), the final `enc.Encode(Response{...})` failed with a silently-discarded deadline error and the client's read got EOF, even though the mutation had actually succeeded (an operator-visible orphan on `sessions.new`, a false failure on `sessions.rm`). Extending — not clearing — keeps the 30s op ctx as the binding budget on the normal path, while a write that's still stuck at 35s hits a hard upper bound rather than hanging the conn goroutine forever.

Both extend calls run strictly after `handle` has decoded the request, so the handshake-read bound is unaffected by either verb: a silent client (no request sent) is still cut off at `s.handshakeTimeout` before either handler is reached. See [`codebase/865.md`](codebase/865.md) for the fix and its regression tests.

`pairing.mint` needs a different two-sided bound. `MintPairing` derives the earlier of the caller's existing deadline and `time.Now().Add(DialTimeout)` before it calls `request`, so dial retry, encode, and decode consume one operation-wide budget; changing `request` globally would incorrectly shorten callers that intentionally choose a longer deadline. Server-side, `handlePairingMint` retains `handle`'s finite request-read deadline and installs a fresh `DialTimeout` response-write deadline before entering the provider. The write is therefore bounded even if the provider returns after the original handshake window.

The pairing provider closure is synchronous and has no context, so these I/O deadlines cannot cancel it after entry. A client may return on its deadline while the provider is still running; the handler attempts its already-bounded write only after the provider returns, and `Serve` continues to drain that in-flight handler during shutdown. Do not turn the deadline into a detached worker or describe it as a provider-execution timeout—the concrete provider must bound its own lock waits and local I/O.

`update.when-idle` has its own 70-second policy (`updateWhenIdleTimeout`).
`UpdateWhenIdle` derives a bounded context before dialing, so dial, request write
and response read share one operation-wide ceiling, shortened by any earlier
caller deadline. It sets the connection's absolute deadline and uses
`context.AfterFunc` to wake parked I/O promptly on caller cancellation, even
after the provider has entered. The watcher is stopped on return and the
connection is closed. These policies are scoped to this helper; `request`,
`requestPatient`, and other verbs retain their existing bounds.

After request decoding, `handleUpdateWhenIdle` installs a fresh 70-second
response-write bound with `SetWriteDeadline` before entering the provider. The
five-second handshake-read limit stays in place. A valid decision can therefore
arrive after five seconds, allowing the release metadata check's 60-second HTTP
budget. The client ceiling starts before dial, while the server write window
starts after decoding; neither is reset when the provider returns.

These are I/O bounds, not execution bounds. The synchronous update provider has
no context parameter and must bound its own release/metadata check. Cancellation
or expiry ends the client's wait without stopping the provider or accepted
installation work. If the provider returns after the write bound expires, its
response may never reach the caller; a failed exchange cannot establish that
scheduling was retracted. `Serve` still waits for that handler to return during
shutdown.
