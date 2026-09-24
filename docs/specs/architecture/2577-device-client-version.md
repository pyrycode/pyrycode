# #2577 — record each device's last app version and show it in `pyry pair list`

## Files read

- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the accept tail: the `recordRedemption` call after the accept envelope, and the `retainedClientField` retention of `helloPayload.ClientVersion`. The new write goes into this tail.
- `internal/relay/v2session_redemption.go` → `recordRedemption`, `redemptionLockWait`, `errRedemptionReloadFailed` — the persist pattern this ticket mirrors (fast path before the lock, `WithLock` → `Reload` → mutate → `Save`, log-and-swallow, no client bytes or reload error in the log).
- `internal/relay/v2session.go` → `retainedClientField`, `ActiveConn` — the MUST-NOT-log obligation on `ClientVersion`.
- `internal/sessions/systemprompt.go` → `admissibleClientField`, `admitClient`, `maxClientVersionBytes` — the one filter the stored value must pass.
- `internal/devices/device.go` → `Device` — `omitempty`/`omitzero` precedent for keeping absent fields off disk.
- `internal/devices/registry.go` → `ClearRedeemBy`, `UpdatePushRegistration`, `Reload`/`reconcileDevices` — mutator shape (returns "changed"), and memory-wins reconcile for a field the daemon solely writes.
- `cmd/pyry/pair.go` → `renderPairList`, `runPairList` — the listing reads `devices.Load` from disk in a separate process.
- `internal/relay/v2session_redemption_test.go` → `redemptionFixture`, `startRedemptionManager`, `diskDevice`, `backdate`, `assertNotRewritten`; `internal/relay/v2session_client_identity_test.go` → `buildHelloIdentityEarlyData`; `internal/relay/v2session_modal_test.go` → `openModalConn` — the handshake-driving helpers the new tests reuse.

In-flight overlap: #2569 edits `internal/relay/v2session_handshake.go` (renames `workspaceRoot`). Not a dependency; my edit there is an additive call plus a new function below `handleNoiseInit`.

## Context

The daemon knows a live session's `client_version`, but nothing persists it, so the operator cannot tell whether any paired device still runs an old app version. `pyry pair list` is a separate process reading `devices.json`, so the value has to be written to disk at the accepted hello.

## Design

### Shared filter — `internal/sessions`

Add one exported function next to `admitClient`:

```go
// AdmitClientVersion returns v when admissibleClientField admits it as a
// version (maxClientVersionBytes), else "".
func AdmitClientVersion(v string) string
```

`admitClient` calls it for its version field, so the version rule (character set AND bound) exists once. `internal/relay` imports `internal/sessions` directly; `relay` already depends on `sessions` transitively (via `control`) and `sessions` imports only `conversations`, so no cycle.

### Record — `internal/devices`

- `Device.ClientVersion string` with tag `json:"client_version,omitempty"` — a record with no version keeps the key off disk, and a pre-change record decodes to "".
- `(*Registry).SetClientVersion(tokenHash, version string) bool` — sets the field on the matching device; returns true iff a device matched AND the value changed (so the caller's Save decision lives here, as with `ClearRedeemBy`). No filtering: the registry stores what it is given; the relay admits before calling.

### Write — `internal/relay`

New method in `v2session_handshake.go` (kept in the handshake file so the ticket stays within five production files):

```go
func (m *V2SessionManager) recordClientVersion(connID string, dev devices.Device, reported string)
```

- `version := sessions.AdmitClientVersion(reported)`.
- Fast path (no lock, no sidecar): return when `m.cfg.DevicesPath == ""` or `version == dev.ClientVersion` (the snapshot `Validate` returned).
- Region under `devices.WithLock(path, redemptionLockWait, …)`: `Reload` (a failure is replaced by a static sentinel `errClientVersionReloadFailed`) → `SetClientVersion(dev.TokenHash, version)` → `Save` only if it returned true. A device revoked between Validate and the region is dropped by Reload, so `SetClientVersion` reports false and nothing is written (no resurrection).
- Any error: one `Warn` with `event=v2.devices.client_version_persist_failed`, `conn_id`, `path`, `err` — never the version.
- Called from `handleNoiseInit` right after `recordRedemption`, i.e. after the accept envelope and before `V2StateOpen`, for the same reasons that call sits there (phone never waits on the lock; the open transition orders the write for tests).

`redemptionLockWait` is reused: its bound is the Run-goroutine stall, which is the same property here.

A version that fails the filter is stored as "" — so a device that goes from a valid version to an invalid one has its stored version cleared (AC 2 "stored as empty").

### Display — `cmd/pyry/pair.go`

`renderPairList` header becomes `NAME  PAIRED  LAST SEEN  VERSION  TOKEN-PREFIX`; the cell is `d.ClientVersion` verbatim, empty when unset.

## Concurrency model

No new goroutines. The write runs on the manager's Run goroutine, bounded by `redemptionLockWait`, exactly like `recordRedemption`. The devices file lock excludes `pyry pair` / `pyry pair revoke` / the push-token handler; `Registry.mu` serialises in-process access. Lock order is file lock → `Registry.mu` (inside `Reload`/`SetClientVersion`/`Save`), the order every existing acquirer uses.

## Error handling

Best effort: lock timeout, reload failure, or save failure is logged once and swallowed; the handshake has already been accepted. A Save failure leaves memory updated and disk not; the fast path then skips until the version changes again or the daemon restarts (restart reloads from disk). Same accepted posture as `recordRedemption`.

## Testing strategy

- `internal/sessions`: table test for `AdmitClientVersion` — valid semver passes verbatim; 33 bytes, control char, DEL/C1, double quote, invalid UTF-8, blank → "". Exactly 32 bytes passes.
- `internal/devices`: `SetClientVersion` returns changed/unchanged/no-match correctly; a device with "" saves without a `client_version` key; a pre-change JSON record (no key) loads with "" and round-trips byte-identical.
- `internal/relay` (real handshakes against a wired `DevicesPath`):
  - accepted hello with `1.4.0` → `devices.Load` (fresh process view) shows `1.4.0`; a second conn with `1.5.0` replaces it.
  - same version again → no rewrite (`backdate` + `assertNotRewritten`), and no lock sidecar for a device whose stored version already matches.
  - inadmissible reported version (e.g. with a double quote) over a stored valid one → stored "".
  - `redemptionFixture` seeds `ClientVersion: "v2-test"` (the standard test hello's version) so the existing redemption tests keep asserting redemption-only writes and lock-free no-deadline behaviour.
- `cmd/pyry`: update the `renderPairList` byte-exact expectations for the new column; add a row with a version and one without.

## Open questions

- None blocking. Column header name: `VERSION` (the value is the raw app-reported string, not parsed).

## Documentation handoff

Pending for the documentation stage: update the `pyry pair list` sample output in `docs/knowledge/features/pyry-pair-command.md` to show the new `VERSION` column between LAST SEEN and TOKEN-PREFIX.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings — the client-supplied string crosses into durable state at exactly one place, `recordClientVersion`, which passes it through `sessions.AdmitClientVersion` (the single copy of `admissibleClientField` + `maxClientVersionBytes`) before `SetClientVersion`. `SetClientVersion` does not filter; its doc comment says the caller admits. The write targets only the authenticated device (`dev.TokenHash` from `Validate`'s accept), so one device cannot write another's record, and nothing runs for a rejected hello.
- [Trust boundaries / display] SHOULD FIX (handled in Phase B) — `renderPairList` writes the value to the operator's terminal. `admissibleClientField` refuses C0, DEL and C1, so no escape sequence or tab/newline can reach the terminal or break tabwriter columns. Unicode formatting characters (bidi overrides, zero-width) pass the filter; they can visually disorder one row but not execute anything. Accepted: same exposure the NAME column already has, and a second filter is forbidden by the ticket.
- [Tokens] No findings — the version is not a secret, the token is never touched; `helloPayload` is read by value and not retained (existing rule).
- [File operations] No findings — writes go through `Registry.Save` (temp file 0600 in the same dir, fsync, rename) under `devices.WithLock`; the path is daemon config, not client input.
- [Subprocess] N/A — no exec.
- [Crypto] N/A — no new primitives or comparisons against secrets.
- [Network & I/O] No findings — the hello is already size-capped by the frame decoder; the stored value is capped at 32 bytes by the filter. An authenticated device alternating versions forces one fsync per accepted handshake; bounded by the handshake rate of an operator-paired device — accepted.
- [Logs] No findings — the failure log carries `event`, `conn_id`, `path`, `err`; the version never appears. The in-region Reload error (which can echo file bytes) is replaced by the static `errClientVersionReloadFailed` before it can reach the log, mirroring `errRedemptionReloadFailed`.
- [Concurrency] No findings — Reload inside the lock makes disk authoritative for membership (no dropped concurrently-paired device, no resurrected revoked device); lock order file-lock → `Registry.mu` matches existing acquirers; lock wait bounded by `redemptionLockWait`. Interrupted write: rename is atomic, so disk holds the old or new file.
- [Threat model] No findings — no protocol change; the value is display-only and stays off the wire.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24
