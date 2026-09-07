# #1529 — reject a pairing token whose redemption window has elapsed

## Files read

- `internal/devices/device.go` → `Device.RedeemBy`, `RedemptionWindow` — the field this slice starts reading, and the "nothing reads this field yet" paragraph the ticket asks to retire. Confirms the semantics: zero means "no deadline", set-and-past means "minted, never redeemed, window gone".
- `internal/devices/auth.go` → `Validate` — the predicate to widen. Its `(Device, bool)` shape cannot express "matched but stale", and its `LastSeenAt = time.Now()` stamp is *inside* the same critical section as the hash match, which is why the deadline check must land in that region rather than beside it (AC-1's "leaves `LastSeenAt` untouched").
- `internal/devices/registry.go` → `List`, `FindByTokenHash`, `Remove`, `ClearRedeemBy` — `Remove` is the only deleter and nothing on the auth path calls it, which is what makes AC-5 structural rather than a new guard.
- `internal/relay/v2session_handshake.go` → `handleNoiseInit` — the sole production caller of `Validate`, and the owner of the reject envelope (`sealError` + `m.send(respFrame)` + `closeWith(StatusUnauthorized, errFrame)`) that AC-3 requires be reused wholesale.
- `internal/relay/v2session_redemption.go` → `recordRedemption` — the accept-tail clear. Its early return on `dev.RedeemBy.IsZero()` means an accepted-and-cleared device never re-enters the new reject arm, and its doc already forward-references this slice as the enforcement that makes a failed persist fail closed.
- `internal/relay/auth.go` → `MsgInvalidToken` — the message AC-3 pins as identical across both reject reasons.
- `internal/devices/auth_test.go` → `TestRegistry_Validate_IgnoresExpiredRedeemBy` — the unit inertness pin to invert; its non-vacuity assertions (fixture deadline is non-zero and genuinely in the past) are the scaffolding the enforcement test keeps.
- `internal/relay/v2session_redeemby_test.go` → `TestV2Session_ExpiredRedeemBy_StillHandshakes` — the handshake leg of the same pin, to invert.
- `internal/relay/v2session_redemption_test.go` → `redemptionFixture` — every call site passes either a *future* deadline or the zero time, so enforcement breaks none of #1528's tests. Checked explicitly; a past-dated fixture there would have been collateral damage.
- `internal/e2e/relay_v2_handshake_test.go` → `TestRelayV2_Handshake`, `testV2BadToken`, `startV2Harness`, `buildHelloEarly` — the subtest table AC-3/AC-4 join, the unknown-token reject whose assertions the expired case must match, and the `opts ...func(*relay.V2SessionConfig)` hook that lets one subtest swap the manager's logger.
- `internal/e2e/safebuffer.go` → `safeBuffer` — an existing mutex-guarded `io.Writer`; AC-4's log assertion needs one and does not need a new helper.
- `docs/knowledge/features/devices-package.md` § "Surface", § "Out of scope (deferred)" — records the #1527/#1528/#1529 sequence and the `omitzero` reasoning; confirms this slice is the last of the three and that no fourth consumer of `RedeemBy` is planned.
- `docs/knowledge/features/devices-registry.md` § "`ClearRedeemBy` — redemption clear (#1528)" — the reload-inside-the-lock discipline that stops a redemption write resurrecting a revoked row. Not changed here, but it is why the accept path and the reject path can disagree about a record without either corrupting disk.

## Context

`pyry pair` writes the hashed token to `devices.json` *before* rendering the QR, and `(*Registry).Validate` is a bare hash lookup. The trust-on-first-use window therefore opens the moment the token is printed and closes only on a manual `pyry pair revoke`. A photographed terminal, a scrollback buffer or a tmux capture is an indefinite bearer credential.

#1527 added `Device.RedeemBy`, stamped once at mint time as `PairedAt + RedemptionWindow`. #1528 added `(*Registry).ClearRedeemBy` and the relay's `recordRedemption`, which zeroes that deadline on the handshake accept tail. Both are merged. This slice is the enforcement — the one that actually closes the hole.

The zero value must keep authenticating forever, and that is the migration path rather than an omission: a record written before the field existed decodes to the zero value with no way to reconstruct whether it was ever redeemed, and since #1528 the zero value *also* means "already redeemed". One rule covers both populations.

No ADR is warranted. The design adds no new persistence, no new lock, no new wire shape, and no new failure mode the reader cannot infer from the field doc; ADR 029 already covers reload-at-handshake, which is the only cross-cutting decision in the neighbourhood.

## Design

### The predicate — `internal/devices`

`Validate`'s `(Device, bool)` cannot express "matched, but stale", which AC-4 requires the caller to distinguish. Widen the return rather than adding a sibling predicate: there is exactly one production caller, and a second entry point would let a future caller pick the narrow one and silently skip the check.

```go
// ValidateResult is why Validate accepted or refused. The zero value denies.
type ValidateResult int

const (
    ValidateUnknownToken  ValidateResult = iota // no row matches (also the empty plain)
    ValidateAccepted                            // matched, deadline not elapsed; LastSeenAt advanced
    ValidateWindowElapsed                       // matched, but the redemption deadline has passed
)

func (r *Registry) Validate(plain string) (Device, ValidateResult)
```

`ValidateUnknownToken` is the `iota` zero so a default-constructed result denies, matching the `RemotePermissionOutcome` precedent already in this file. `Validate` returns `Device{}` for both refusals — a populated `Device` alongside a refusal invites a caller to read it.

The boundary test is its own unexported predicate so it is table-testable without injecting a clock into `Registry`:

```go
// redemptionWindowElapsed reports whether d's redemption deadline has passed at
// now. A zero RedeemBy never elapses. The deadline is exclusive: at exactly
// RedeemBy the record has stopped being acceptable.
func (d Device) redemptionWindowElapsed(now time.Time) bool
```

Ordering inside `Validate`'s critical section is the whole of AC-1: hash match → `redemptionWindowElapsed(time.Now())` → *only then* the `LastSeenAt` stamp. A rejected attempt must not refresh the one column that would betray a never-scanned token in `pyry pair list`.

`Validate` still never removes a row (AC-5): `Remove` is the only deleter and nothing on the auth path reaches it. That stays structural, asserted rather than enforced.

Doc changes this forces: `Validate`'s comment grows the new miss condition in its opening enumeration, and `Device.RedeemBy`'s comment loses its "Nothing reads this field yet" paragraph.

### The handshake — `internal/relay`

`handleNoiseInit` switches on the result instead of a bool. The reject *body* is untouched — same `sealError(protocol.CodeAuthInvalidToken, MsgInvalidToken, helloID)`, same `m.send(respFrame)` then `closeWith(StatusUnauthorized, errFrame)`, same ordering. The only thing that varies is the `event` field on the `Warn` line:

| result | log `event` | wire |
|---|---|---|
| `ValidateUnknownToken` | `v2.handshake.reject.invalid_token` | noise_resp, sealed `auth.invalid_token`, 4401 |
| `ValidateWindowElapsed` | `v2.handshake.reject.redemption_window_elapsed` | *byte-identical to the row above* |

Selecting only the event string, then falling into the one shared body, is what makes "rejected identically" true by construction rather than by two code paths that happen to agree today. A distinct close code or error code would turn the handshake into an oracle confirming a captured token was once real — that is the security-relevant half of the ticket.

The log line keeps exactly the fields it has (`event`, `conn_id`, `close_code`): neither the plain token nor the hash, per the package's standing SECURITY rule.

### Data flow

```
phone hello(token)
   └─ handleNoiseInit
        ├─ Devices.Reload           (unchanged, fail-closed)
        ├─ Devices.Validate(token) ──> ValidateAccepted      -> accept tail -> recordRedemption -> V2StateOpen
        │                              ValidateWindowElapsed -> event=…redemption_window_elapsed ─┐
        │                              ValidateUnknownToken  -> event=…invalid_token             ─┤
        └───────────────────────────────────────────────────────── one shared reject body ───────┘
                                                                    (4401 + auth.invalid_token)
```

## Concurrency model

No new goroutines, no new locks, no change to lock ordering. The deadline check runs inside the existing `Registry.mu` critical section in `Validate`, so the read of `RedeemBy` and the conditional `LastSeenAt` write are one atomic region against `Add` / `Remove` / `ClearRedeemBy` / `Save` snapshots — the same guarantee `Validate` already documents for concurrent callers of the same token.

The accept path's `recordRedemption` is unaffected: it runs after `Validate` returned `ValidateAccepted`, and its own `WithLock` region re-decides correctness against disk. A `ClearRedeemBy` racing a `Validate` is resolved by `Registry.mu` either way — the loser reads one side of the clear and both outcomes are correct (cleared → accept forever; not yet cleared but still in-window → accept).

## Error handling

`Validate` has no error path and gains none; it is a total predicate over three outcomes. The handshake's reject arm is best-effort in exactly the way it already is: a `sealError` failure drops the error frame and still emits the 4401 close.

One failure mode worth naming because #1528's doc forward-references it: if `recordRedemption`'s `Save` fails, memory is cleared and disk is not, and this daemon will not retry. On the next restart the deadline is re-read from disk; if that restart lands after the window, this slice rejects the device and the operator re-pairs. That is fail-closed and already announced by `recordRedemption`'s `Warn`.

## Testing strategy

**`internal/devices/auth_test.go` — AC-1, AC-2, AC-5 (pure predicate, no relay, no clock injection).**

- Invert `TestRegistry_Validate_IgnoresExpiredRedeemBy` into an enforcement test: an hour-past deadline now yields `ValidateWindowElapsed`; keep its two non-vacuity assertions (fixture deadline non-zero, genuinely in the past) by reading the record back off the registry. Add: `LastSeenAt` unchanged from the seeded value, and the row still present in `List()` — the same accessor `pyry pair list` reads.
- New: a forward-dated deadline yields `ValidateAccepted` and *does* advance `LastSeenAt`; a zero-value deadline on a record paired long ago yields `ValidateAccepted`.
- New: a table over `redemptionWindowElapsed` covering zero deadline, deadline in the past, deadline in the future, and `now` exactly at the deadline (the boundary the exported path cannot pin without a fake clock).
- Update the seven existing `.Validate(` call sites across `auth_test.go` and `registry_test.go` to compare against `ValidateAccepted`.

**`internal/relay/v2session_redeemby_test.go` — the handshake leg.**

Invert `TestV2Session_ExpiredRedeemBy_StillHandshakes` into its enforcement twin: the same back-dated fixture must now *fail* to reach open. Keep the existing non-vacuity guard that the fixture still carries the back-dated deadline after the attempt — which doubles as the AC-5 witness that the reject removed nothing.

**`internal/e2e/relay_v2_handshake_test.go` — AC-3 and AC-4 over a real handshake.**

- Extract the frame-sequence assertions of `testV2BadToken` into a helper that drives a handshake with a given token and asserts the whole observable reject: noise_resp first, then a `noise_msg` whose sealed envelope decodes to `auth.invalid_token` with `relay.MsgInvalidToken`, then a 4401 WS close. Both subtests call it, so "rejected identically" is structural — the two cases cannot drift apart without the shared helper failing.
- New subtest `expired_token_4401`: registry holding the *correct* token with an hour-past deadline, driven through the same helper. Additionally swaps the manager's `Logger` for a `safeBuffer`-backed one via `startV2Harness`'s existing opts hook and asserts the captured line carries `v2.handshake.reject.redemption_window_elapsed`, does *not* carry `v2.handshake.reject.invalid_token`, and contains neither the plain token nor its `devices.HashToken` (AC-4's "carries neither the plain token nor the token hash").

**Regression surface checked ahead of time:** every `redemptionFixture` call in `internal/relay/v2session_redemption_test.go` passes a future deadline or the zero time, and no other test fixture in the repo sets `RedeemBy`. Enforcement breaks none of them.

## Open questions

1. **Boundary direction at exactly `RedeemBy`** — resolved in this plan as *exclusive* (at the instant itself the record has stopped being acceptable), because the field doc defines `RedeemBy` as "the instant at which an UNREDEEMED pairing record stops being acceptable". Pinned by the `redemptionWindowElapsed` table rather than left to the wall clock.
2. **Whether the reject line should also carry a `reason` field** — deliberately not added. AC-4 asks for an event name of its own, and the existing `invalid_token` line in `handleNoiseInit` carries no `reason`; adding one to only the new arm would make the two lines differ in shape as well as in event, for no operator gain.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No new boundary. The existing one is `handleNoiseInit`'s hello decode → `Validate(helloPayload.Token)`; this slice narrows what crosses it (a matched-but-stale record no longer becomes an authenticated `*Device`) and never widens it. The `ValidateResult` zero value is `ValidateUnknownToken`, so a caller that forgets to assign — or a future `Validate` path that returns early without setting a result — denies. Downstream code is unchanged: `s.device` is still only assigned on the accept branch, so no handler can observe a device that failed the deadline.
- **[Tokens, secrets, credentials]** This *is* the lifecycle finding the ticket exists to fix: expiry was the missing fourth leg of create/store/revoke/expire. Two residual observations, neither exploitable as designed. (a) The window is enforced against `time.Now()` with no monotonic anchoring, so an operator who moves the system clock backwards re-opens a closed window — accepted, because the attacker in the threat model has a photo of a terminal, not local root, and an actor who can set the daemon's clock can also rewrite `devices.json`. (b) A downgrade to a binary predating the field re-saves the record without `redeem_by` and fails open; already documented on `Device.RedeemBy` and unchanged here, same rationale.
- **[Tokens — non-extension]** Checked that the reject arm cannot become a redemption oracle by side effect: `recordRedemption` is called only on the accept branch, and the reject arm's `Validate` returns before the `LastSeenAt` stamp. So a rejected attempt mutates *nothing* — not memory, not disk. That is what makes AC-1's "`LastSeenAt` untouched" a security property and not a cosmetic one: `pyry pair list` remains an honest witness that the token was never scanned.
- **[File operations]** No finding — no new path handling, no new file write, no new mode decision. The reject path performs no I/O at all. The pre-existing `Devices.Reload` on the handshake is untouched and keeps its fail-closed-on-error behaviour.
- **[Subprocess / external command execution]** Not applicable — the design executes nothing and touches no `exec.Command` call site.
- **[Cryptographic primitives]** No finding. The comparison this slice adds is `time.Time` against `time.Time`, not a secret comparison, so constant-time handling is irrelevant to it. The token comparison itself is unchanged — still `HashToken` plus the existing equality on a 64-char hex digest, and the deadline check runs strictly *after* that match, so it introduces no branch on token content and no new timing signal about which hash matched.
- **[Timing side channel — the one deliberately accepted]** A rejected-for-expiry handshake does marginally less work than an accepted one (it skips the `LastSeenAt` write, `recordRedemption`, and the whole open tail). An attacker who already holds a real token could in principle time the difference — but they learn nothing they do not already know, since they hold the token either way and the wire tells them the outcome. The reverse direction, the one that matters, is safe: expired and unknown both skip the same work and emit byte-identical frames, so timing does not separate "was once real" from "never existed" any more than the wire does.
- **[Network & I/O]** No finding — no new frames, no new read, no size cap to set. Frame count, order and close code on the reject path are unchanged from the unknown-token case by construction (one shared body), which is AC-3.
- **[Error messages, logs, telemetry]** The one new log line is the deliberate server-side discriminator AC-4 asks for. It carries `event`, `conn_id`, `close_code` and nothing else — no plain token, no hash, no device name, no `RedeemBy` value (a timestamp would narrow *when* the token was minted for anyone reading logs). The client-visible side is unchanged and carries no new information: same code, same message, same close. Asserted, not assumed — the e2e subtest greps the captured line for both the plain token and its hash.
- **[Concurrency]** No finding. No new lock, no new goroutine, no change to lock ordering. The check-and-stamp stays one critical section under `Registry.mu`, so there is no TOCTOU between reading `RedeemBy` and writing `LastSeenAt`. A `ClearRedeemBy` racing a `Validate` serialises on the same mutex and both orderings yield a correct accept.
- **[Threat model alignment]** Addresses `docs/protocol-mobile.md` § Security model's bearer-token exposure directly: the pairing token stops being an indefinite credential. Explicitly OUT OF SCOPE and named by the ticket: `handleRekeyInit` does not re-run `Validate`, so an already-open session is not re-checked mid-life. Bounding a live session is a separate concern and no ticket owns it yet; the idle sweep (`armIdleTimer`) and the rekey timer already bound a session's *keys*, not its authorisation.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-07
