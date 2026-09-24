# #2578 — Reject an app below the minimum version with the app-too-old error

## Files read

- `docs/specs/architecture/2576-app-compatibility-policy.md` → decisions 3–7 and the 2026-09-24 revisions (32-byte cap, ack still sent, no `workspace_root`, no redemption) plus its `## Security review` — the contract this ticket implements. The revisions win where they differ.
- `docs/protocol-mobile.md` § `hello` (v2-specific note, the `client_version` format note) and § Compatibility — the grammar the parser is table-tested against.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the token-failure arm (noise_resp, then one routing envelope carrying the sealed error plus the close; close-only on seal failure) that the new arm mirrors; `recordRedemption` / `recordClientVersion` and the `s.device` / `s.clientVersion` assignments that a version-rejected handshake must not reach.
- `internal/relay/v2session.go` → `sealError` (sets no `MinClientVersion`), `NewV2SessionManager` (where config is validated), the close-code block holding `StatusClientUpdateRequired` (4412).
- `internal/relay/v2session_seams.go` → `V2SessionConfig` — the test-only-seam posture of `RekeyInterval` is the precedent for a field production fills from a constant.
- `internal/relay/auth.go` → `MsgInvalidToken` — the static-message constant shape.
- `internal/protocol/codes.go` → `CodeClientUpdateRequired`; `internal/protocol/handshake.go` → `ErrorPayload.MinClientVersion`, `HelloClientPayload.ClientVersion`.
- `cmd/pyry/relay.go` → the single production `relay.V2SessionConfig{…}` literal.
- `internal/relay/v2session_test.go` → `TestV2Session_BadToken_AEADErrorThen4401`, `decodeRespFrame`, `decodeNoiseMsg`, `decodeHelloAck`; `internal/relay/v2session_redemption_test.go` → `redemptionFixture`, `startRedemptionManager`, `diskDevice`; `internal/relay/v2session_client_version_test.go` → `openVersionConn`; `internal/relay/v2session_client_identity_test.go` → `buildHelloIdentityEarlyData` — the helpers the new tests reuse.

In-flight overlap: `origin/feature/2569` renames `workspaceRoot` to `WorkspaceRoot` inside `handleNoiseInit`, one line next to the one this ticket edits. No dependency; the later merge touches that line.

## Context

#2576 wrote the policy and reserved `client.update_required`, `min_client_version` and close 4412 with no sender. This ticket adds the parser, a per-app minimum carried as build constants (both empty in this build), and the rejection arm in the IK responder. No ADR needed; the policy is #2576's.

## Design

### `internal/protocol/client_version.go` (new)

- `AppMobile = "pyrycode-mobile"`, `AppDesktop = "pyrycode-desktop"` — the two defined app names.
- `type Version struct{ Major, Minor, Patch uint64 }` with `Compare(o Version) int` (-1/0/+1, MAJOR then MINOR then PATCH) and `String() string` (`"1.4.0"`).
- `ParseVersion(s string) (Version, bool)` — `MAJOR.MINOR.PATCH`: exactly three `.`-separated non-empty runs of ASCII digits, no leading zero unless the run is exactly `0`, each accumulated into `uint64` with an overflow check (overflow → false). Length checked first (> 32 bytes → false).
- `ParseClientVersion(s string) (app string, v Version, ok bool)` — checks `len(s) > 32` **first**, before any scan; then a single byte scan for exactly one `/`; app must be non-empty, start with `a-z`, rest `a-z0-9-`; the remainder goes through `ParseVersion`. No regexp, no `strings.Split` allocations needed.
- The 32-byte cap is a named unexported const with a comment pointing at `internal/sessions`' `maxClientVersionBytes` for why 32.

### Build constants and config (`internal/relay`)

- `v2session.go`: `MinMobileClientVersion = ""`, `MinDesktopClientVersion = ""` (exported consts next to `StatusClientUpdateRequired`; empty means no minimum) and `ShippedMinClientVersions() map[string]string` keyed by `protocol.AppMobile` / `protocol.AppDesktop`. `MsgClientUpdateRequired` — the static message.
- `v2session_seams.go`: `V2SessionConfig.MinClientVersions map[string]string` — app name → `MAJOR.MINOR.PATCH`. Optional: nil or an empty value means no minimum for that app. Production passes `ShippedMinClientVersions()`; tests pass their own map, so the shipped constants never change for a test.
- `NewV2SessionManager` validates every non-empty entry by parsing `app + "/" + value` through `protocol.ParseClientVersion` (one grammar for the app key and the version) and **returns an error** on any failure — a malformed minimum never becomes "no minimum". The parsed result is stored once as `m.minClientVersions map[string]protocol.Version` (nil when no minimum is set) and never mutated after construction.
- `cmd/pyry/relay.go`: one line, `MinClientVersions: relay.ShippedMinClientVersions()`.

### The check (`v2session_handshake.go`)

- `checkClientVersion(reported string) clientVersionReject` on the manager. `clientVersionReject{reason, app, min string}`, zero value = accept.
  - `len(m.minClientVersions) == 0` → accept (whatever `reported` says).
  - unparsable → `reason: "unparsable"`, `min` empty.
  - parsed app with no minimum → accept.
  - `v.Compare(min) < 0` → `reason: "below_minimum"`, `app`, `min: min.String()`.
- In `handleNoiseInit`, called **only when `tokenResult == devices.ValidateAccepted`**. `workspace_root` is set only when the token is accepted **and** the verdict is accept. The ack is built and `WriteResp` runs as today.
- New arm right after the token-failure arm, same shape: seal the error, log, `m.send` the `noise_resp`, then `closeWith(StatusClientUpdateRequired, errFrame)`, or close-only at 4412 when sealing fails. It returns before `recordRedemption`, `recordClientVersion`, `s.device`, `s.clientName`/`s.clientVersion` and the `V2StateOpen` transition.

### Sealing with the minimum (`v2session.go`)

`sealError` becomes a thin wrapper over a new `sealErrorPayload(s, protocol.ErrorPayload, inReplyTo)` that holds today's body; the two existing callers are unchanged. The new arm calls `sealErrorPayload` with `Code: CodeClientUpdateRequired`, `Message: MsgClientUpdateRequired`, `Retryable: false`, `MinClientVersion: verdict.min` (empty → omitted), `in_reply_to` = the hello id.

## Concurrency model

No new goroutine or lock. `m.minClientVersions` is written once in `NewV2SessionManager` and only read afterwards on the Run goroutine, so each handshake sees one immutable snapshot.

## Error handling

- Malformed configured minimum → `NewV2SessionManager` error → daemon start fails loudly.
- Version reject → sealed `client.update_required` + 4412; seal failure → 4412 close-only, same as the token arm's fallback.
- Log line: event `v2.handshake.reject.client_update_required`, `conn_id`, `close_code`, `reason`; `app` and `min_client_version` only on `below_minimum`. Never the raw `client_version`, the token, or `helloPayload`.

## Testing strategy

`internal/protocol/client_version_test.go`:
- Table for `ParseClientVersion`: well-formed mobile/desktop/other-app values, `0.0.0`, exactly 32 bytes, `MaxUint64` components → ok; `""`, `"1.0"`, `"0.1.0"`, `"pyrycode-mobile 0.1.0"`, 33 bytes, `01` leading zero, `-beta`, `+42`, `v1.4.0`, two slashes, empty app, uppercase app, app starting with a digit or `-`, `1..0`, trailing dot, four components, `MaxUint64+1` overflow → unparsable.
- Table for `Version.Compare` (major/minor/patch decide, equal = 0) and `String`.

`internal/relay/v2session_min_version_test.go`:
- No minimum set: `"1.0"`, `"0.1.0"`, `""`, `"pyrycode-mobile/0.0.1"` all reach `V2StateOpen`.
- With minimums, table over (minimums, version) → accept or reject: mobile below → reject with `min_client_version` `"1.4.0"`; at and above → accept; desktop minimum doesn't touch mobile and vice versa; an app with no minimum (`pyrycode-android/0.0.1`) → accept; unparsable forms (`"1.0"`, space form, 33 bytes, leading zero, suffix, overflow, `""`) → reject with `min_client_version` omitted.
- Reject wire shape: exactly two envelopes; `noise_resp` with no close, whose ack decodes with no `workspace_root`; second carries close 4412 and a sealed `error` with `code`, `retryable: false`, `in_reply_to` = 1.
- No side effects: on a disk-backed registry with a `RedeemBy` deadline, a rejected hello leaves `RedeemBy` and `ClientVersion` on disk unchanged, and `ActiveConns` is empty.
- Bad token + minimum set + unparsable version → `auth.invalid_token` / 4401.
- Log: the reject line carries the event and reason and not the raw version string.
- `NewV2SessionManager` rejects `"1.0"`, `"01.0.0"`, `"1.0.0-rc"`, and a malformed app key; accepts an empty value; accepts `ShippedMinClientVersions()` — the check that fails `make check` if a shipped constant is malformed. A second assertion parses each non-empty shipped value with `protocol.ParseVersion`.

## Open questions

None.

## Documentation handoff (pending — documentation stage)

- `docs/protocol-mobile.md`: remove "reserved, not yet sent" from the `client.update_required` row, the 4412 row and the 4412 paragraph under § Error codes. In § Compatibility, say the minimums are build constants carried by each daemon release, not operator configuration. Correct the claim that the relay's 4429 phone cap bounds an old build's re-dial loop: per #2576's decision-8 revision, 4429 is not a rate limit; the old build's own reconnect backoff bounds the loop, at the same cost a revoked device's 4401 loop has.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. `client_version` is remote-authored and crosses into the version gate at exactly one function, `protocol.ParseClientVersion`; `checkClientVersion` compares only the parsed `(app, Version)` and never re-reads the raw string. The prompt boundary is unchanged: `retainedClientField` and `sessions.AdmitClientVersion` still own what reaches the session and the devices file, and a rejected hello reaches neither.
- [Trust boundaries] No findings. The minimum is a compatibility guard, not a security control (#2576 review): a client can claim any version. Nothing downstream relies on the gate for authorisation.
- [Tokens, secrets] No findings. The check runs only inside `tokenResult == devices.ValidateAccepted`, so a peer without a valid token always takes the unchanged 4401 arm, whose frames do not depend on `client_version`; the version is not even parsed on that path, so timing does not vary with it either. A tested case pins bad token + unparsable version → 4401. The reject log follows the token arm's `SECURITY` discipline: no token, no hash, no `helloPayload`.
- [Tokens, secrets] No findings. `min_client_version` is daemon-authored — `Version.String()` of the parsed configured minimum for the parsed app — never an echo of client bytes; omitted when unparsable.
- [Tokens, secrets] SHOULD FIX (implemented in this plan) — a malformed build minimum must fail closed-loudly, not read as "no minimum". `NewV2SessionManager` returns an error, and a unit test constructs a manager with `ShippedMinClientVersions()`, so a bad constant reddens `make check` before it ships.
- [File operations] No findings. No file is read or written by the new code; the reject arm returns before `recordRedemption` and `recordClientVersion`, the only writers on this path, which the no-side-effects test pins on disk.
- [Subprocess execution] Not applicable — no subprocess.
- [Cryptographic primitives] No findings. The arm reuses `sealErrorPayload` over the responder's send `CipherState`: one sealed frame, the same nonce use as the token arm. No new primitive. The version comparison involves no secret, so no constant-time requirement.
- [Network & I/O] No findings. The parser checks `len > 32` before scanning or converting; it is a single linear byte scan with overflow-checked accumulation and no regexp. The input is already bounded by the application-envelope cap at decode. Re-dial cost equals a revoked device's 4401 loop (#2576 decision-8 revision). OUT OF SCOPE: a per-device rejection cooldown, deferred until a re-dial storm is observed.
- [Error messages, logs] No findings. Static message `MsgClientUpdateRequired`. The log carries event, `conn_id`, `close_code`, closed-set `reason`, and on `below_minimum` only the parsed app (grammar-restricted) and the daemon-authored minimum. A test asserts the raw version is absent from the log.
- [Concurrency] No findings. The minimum map is immutable after construction and read only on the Run goroutine; no lock, no goroutine.
- [Threat model] No findings. Relay stays blind (the version rides inside Noise; route and relay unchanged). No new pre-authentication response. A rejected build never reaches `V2StateOpen`, so it gets no handler dispatch, no push queue and no host metadata (`workspace_root` withheld).

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-24
