# ADR 041 — Bind a pairing to the first install's Noise static key

## Status

Accepted (#2734). Implemented in `internal/devices` (`Device.StaticKey`, `Registry.Validate`, `Registry.BindStaticKey`) and `internal/relay` (`handleNoiseInit`, `recordStaticKey`). Spec: [`docs/specs/architecture/2734-pair-bind-noise-static-key.md`](../../specs/architecture/2734-pair-bind-noise-static-key.md).

## Context

[ADR 024](024-noise-ik-mobile-e2e.md) adopted Noise_IK for the v2 mobile protocol and gave each paired phone its own device-static keypair, generated at pair time and kept in the Android Keystore (or iOS Keychain). `docs/protocol-mobile.md` § "Static keys — mobile side" recorded a deliberate follow-on choice: the binary would not persist or even retain that public key across connections. It learns the key fresh from Noise_IK message 1 on every handshake, so remembering it between connections was unnecessary — doing so would have required growing `devices.json` with a field to keep in sync, in exchange for nothing the protocol needed. The binary's only persistent record of a paired phone stayed the device-token hash.

That design treats a pairing token as the sole credential identifying an install. It is not: a token is a 256-bit secret that can be typed, copied, or scanned by more than one device. On pyrycode-mobile#1573, a phone (Juhana's CPH2415) and a Pixel 8 emulator both held the same MacBook pairing token from one 2026-09-30 pairing. The daemon had no way to tell them apart — both authenticated as the same `Device` row, and `Registry.UpdatePushRegistration` overwrote `Name` and the push address on every registration, so they flipped between the two devices and push wakes went to whichever had registered most recently. The daemon already sees a per-install identity on every connection (`handleNoiseInit` captures the initiator's static key as `s.peerStatic` right after `Responder.ReadInit` succeeds, per [ADR 024](024-noise-ik-mobile-e2e.md)'s re-key continuity check) — it just wasn't using that identity for anything but re-key continuity within one already-open session.

## Decision

The first accepted v2 connection for a device record with no stored key binds that record to the connection's Noise static public key, persisted as `Device.StaticKey` (`static_key` in `devices.json`, `omitzero`). This covers a fresh redemption and a record redeemed before this change alike — binding does not require `RedeemBy` to be set or already cleared.

A later connection presenting the same token with a *different* key is refused before `V2StateOpen`, with exactly the reject an unknown token gets: sealed `auth.invalid_token`, close `4401`, the same frame order, and an ack carrying no `workspace_root`. The only observable difference is server-side: the Warn log line names the device and the specific reason (`bound_to_other_key` from `Validate`, or `bind_race_lost` when two connections raced to bind one record). A connection whose key matches the stored one is accepted unchanged.

The bind-if-unbound decision (`Registry.BindStaticKey`) is atomic under `Registry.mu`, runs only after the token and the client-version gate have both admitted the connection, and runs before the `hello_ack` payload is built — so a connection that loses a bind race gets no `workspace_root`, the same ack an unknown token gets. `pyry pair revoke` removes the stored key along with the rest of the record, so re-pairing is how an install change is made.

## Rationale

### Why reverse "do not persist" rather than add a separate per-install identifier

The ticket's own technical notes name the alternative — "per-install ids beyond the Noise key" — and rule it out as out of scope. The daemon already authenticates a per-install public key on every connection; minting a second identifier would duplicate what Noise_IK already proves and would need its own issuance and storage story. Persisting the key the handshake already produces is the smaller change.

### Why this does not reopen the mobile-side-rotation-is-invisible property

The original "do not persist" paragraph protected one thing: a phone could rotate its Keystore-held static key without the binary needing to notice or coordinate. But mobile-side rotation was never actually wired to happen independently — `docs/protocol-mobile.md` § "Static keys — mobile side" already states rotation is tied to re-pairing, and revoking a device invalidates its token *and* its static key together. Since rotation never happens mid-pairing in practice, persisting the first-seen key costs nothing a rotation would have needed anyway: a re-pair mints a new token hash with an empty `StaticKey`, so the new key binds on its own first connection exactly as a fresh pairing does.

### Why the client-visible reject must be identical to an unknown token's

[#1529](../../specs/architecture/1529-pair-reject-elapsed-redemption-window.md) established this pattern for an elapsed redemption window and the reasoning carries over unchanged: a distinct wire signal for "this token is real but bound to someone else" would tell whoever holds a copied or leaked token that the token itself is genuine, narrowing their search. `ValidateKeyMismatch` and a lost `BindStaticKey` race both fall into the same reject body as `ValidateUnknownToken`, selected only by which log event string is chosen server-side.

### Why the bind runs after the client-version gate, not before

A connection refused at `4412` (unsupported client version) never reaches `V2StateOpen` and should not consume the first-bind opportunity — an old build probing the token should not be able to squat the binding ahead of the real install's next, version-compliant connection. Running `BindStaticKey` only after both gates pass keeps "accepted" and "bound" in lockstep: the handshake's eventual accept-tail persist (`recordStaticKey`) only ever fires for a connection that is actually about to open.

## Alternatives Considered

### A. Surface the binding in `pyry pair list` before shipping it

Would let an operator see which install a token is bound to and catch a leaked-token race after the fact. Explicitly out of scope per the ticket and the security review's accepted residual risk: the binding itself is valuable with no UI, and adding the column is a small follow-on that does not change the binding's behavior.

### B. Rebind on an explicit operator command instead of only via `pyry pair revoke`

Would let an operator move a binding to a new install without minting a new token. Rejected for the same reason the ticket rules it out: the pairing flow already has an unbind-and-rebind primitive (revoke + re-pair), and a separate rebind verb would be a second way to reach the same state with its own authorization question (who may rebind someone else's device?).

### C. Reject a key mismatch with a distinct error code

Would let the daemon log (and the app surface) a clearer "this pairing belongs to another device" message. Rejected because it creates exactly the oracle #1529 already closed for the redemption-window case: a distinct response confirms to whoever holds a copy of the token that the token is real, which is the information an attacker most wants and the legitimate operator least needs the protocol to leak (the Warn log already tells the operator what happened).

## Consequences

- **A pairing token two installs already share (as on #1573) binds to whichever connects first; the other is refused and needs its own `pyry pair`.** This is the intended outcome, not a bug to fix later — the ticket names it explicitly.
- **A record unbound at upgrade time, or a fresh pairing still inside its redemption window, binds to whichever holder connects first.** A leaked token that reaches the daemon before the real install does takes the record; the real install is then refused until it re-pairs. `docs/protocol-mobile.md` § Security model narrows "token leak via phone" accordingly: the threat now applies only to a token not yet bound.
- **`devices.json` gains one more field**, following the `RedeemBy`/`omitzero` precedent so a legacy record (and a downgrade to a binary predating this field) round-trips without it.
- **Losing the bound phone itself is unaffected.** The static private key never leaves the Android Keystore / iOS Keychain; this ticket binds the *public* key the daemon already sees, and changes nothing about key-extraction risk on the phone side.
- **Test harnesses that modeled "many installs, one token" had to change.** Roughly 80 relay tests and every e2e handshake helper drew a fresh initiator key per connection while reusing one token — the daemon now refuses that as a second install. `v2TestInstallPriv` (relay) and `fakephone.InstallKey(token)` (e2e) fix one key per token; see [`fakephone-harness.md`](../features/fakephone-harness.md).

## Related

- [ADR 024](024-noise-ik-mobile-e2e.md) — adopted Noise_IK and the per-paired-phone device-static keypair this ticket binds to; the asymmetry section there (one binary key, many phone keys) is what makes "first accepted key wins" the correct binding granularity.
- `docs/protocol-mobile.md` § "Static keys — mobile side" — the reversed "do not persist" paragraph, now describing the bind.
- `docs/protocol-mobile.md` § Security model, threat 4 ("Token leak via phone") — narrowed to a token not yet bound.
- [`devices-registry-validate.md`](../features/devices-registry-validate.md) — `Validate`'s widened signature and `ValidateKeyMismatch`.
- [`devices-registry-redemption-and-binding.md`](../features/devices-registry-redemption-and-binding.md) — `BindStaticKey` / `BindResult`.
- [the noise_init happy-and-failure-path doc](../features/v2-session-manager-state-machine-noise-init-happy-and-failure-path.md) — the handshake call site, ordering, and `recordStaticKey`.
- [`docs/specs/architecture/1529-pair-reject-elapsed-redemption-window.md`](../../specs/architecture/1529-pair-reject-elapsed-redemption-window.md) — the identical-client-visible-reject pattern this ticket reuses.
- pyrycode-mobile#1573 — the observed incident that motivated this reversal.
