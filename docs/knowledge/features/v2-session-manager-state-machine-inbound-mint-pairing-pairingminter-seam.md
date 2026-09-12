# Inbound `mint_pairing` (#2127) — `PairingMinter` seam

`mint_pairing` is a v2 **control** envelope (phone → binary), intercepted in
`dispatchAppFrame`'s discriminator switch **before** `dispatch.Route`, and
routed to the conn's `appFrameWorker` rather than handled inline on `Run` —
the same reason [`request_history`](v2-session-manager-state-machine-inbound-request-history-historypager-seam.md)
and [`request_attachment`](v2-session-manager-state-machine-inbound-request-attachment-attachmentresolve.md)
are: answering it takes the `devices.json` file lock and rewrites the file.
It serves the wire contract [#2126 declared](protocol-package-types-pairing-payloads.md)
— a paired client holding the remote-permissions flag can mint a pairing for
a second device without a shell on the daemon's host. `security-sensitive`.
See [`docs/specs/architecture/2127-wire-mint-pairing.md`](../../specs/architecture/2127-wire-mint-pairing.md).

`internal/relay` stays a courier. Minting needs `crypto/rand`,
`internal/pair`, `internal/keys`, `internal/identity` and `internal/audit`,
none of which this package imports, plus the daemon's own relay URL and
server id. The decision and the audit record it must produce both live at the
implementation, `cmd/pyry`'s `pairingMinterV2`, behind a `PairingMinter`
interface carrying an outcome discriminant (`PairingMintOK` /
`PairingMintUnauthorized` / `PairingMintFailed`) rather than an error — the
errors behind a failure wrap the absolute `devices.json` path, and a seam
whose whole discipline is holding no host path should not be hand-carrying
one across a package boundary. `V2SessionConfig.PairingMint` is nil-safe:
unset ⇒ the frame is consumed but inert, matching every other optional seam
in this family.

The mint itself — CSPRNG draw, hash, `device-<hash8>` fallback, one clock
read, locked load-mutate-save — is `cmd/pyry`'s `mintDevice`, shared by
`runPairDefault`, the local control provider, and this remote path. See [`pyry pair`'s operation
order](pyry-pair-command.md#pyry-pair-bare--operation-order) for the shared
step; this document covers only what the wire caller adds on top of it.

`pairingMinterV2` also implements the local `pairing.mint` provider installed
on the mode-0600 control socket. `startRelayV2` constructs one minter only after
the relay identity, resolved URL, registry, and static key are fixed, installs
it as the session manager's remote `PairingMinter`, and returns its
`MintLocalPairing` method through `startRelay` to `runSupervisor`. Both daemon
entry points consequently encode from the same immutable provenance rather
than reloading saved service state. The local caller is the host operator: it
may choose `AllowRemotePermissions` and supplies no grantor hash. The remote
phone path remains narrower: it rechecks the grantor inside the registry lock
and always passes literal `false`, so every remotely minted device is
unprivileged.

The two daemon entry points share `encodePairing`, and encoding happens only
after `mintDevice` has persisted the hash. A local failure returns no pairing
and is projected by the control server to a fixed error; remote failures retain
their existing outcome mapping and audit record. Local success and failure logs
are content-free, while remote audit and revocation behavior remain unchanged.

## A connection-scoped privilege check goes stale the moment it's used to authorize a write

The first draft gated the mint on `s.device.MayAnswerRemotePermission()` —
the record `handleNoiseInit` bound at handshake — and stopped there. Security
review caught the gap: v2 revocation is connection-scoped, since
`handleNoiseInit` only reloads `devices.json` before *each handshake's*
`Validate`, so a device's already-open session keeps whatever privilege it
had when it connected until it reconnects. For every other gated verb that
staleness costs an operator one more modal answered by a device that should
already be gone. Here it costs more: it lets a device the operator has just
revoked mint a **fresh** credential through the very session the revoke was
meant to kill — defeating the only remedy the operator has for a stolen
pairing, which is the exact threat this ticket exists to widen exposure to.

The fix is not a second stochastic gate or a shorter reconnect interval. It's
one deterministic re-read inside the lock already being taken:
`mintRequest.grantorHash`, when non-empty, confirms — against the *same*
registry snapshot the append is about to mutate — that a device with that
token hash is still present and still carries `AllowRemotePermissions`,
returning `errGrantorRevoked` and writing nothing otherwise
(`mintDevice`, `registryGrants`). It costs one more linear
scan already paid for by the load that was going to happen anyway. `pyry
pair` and local control pass `grantorHash: ""` and skip the check — the host
operator doesn't have a remote session that can go stale.

**The generalizable point:** a privilege check read from a value bound once,
earlier in the connection's life, is a check against *stale* state the moment
it gates something a revoke is supposed to stop immediately rather than
eventually. Re-derive the check inside the same critical section as the
write it gates, against the snapshot that write is about to mutate — not
against whatever the caller was handed when the session opened. The broader
posture (connection-scoped revocation generally) is unfiled and predates
this ticket; only the write this ticket adds needed closing.

## An "unreachable" check is worse than no check, and it hides behind an ordinary-looking test row

The label gate, `mintLabelIsDisplaySafe`, refuses a `device_name` carrying a
C0 control character, DEL, or a C1 control before the value can be stored,
rendered by `pyry pair list`, or logged. The first draft also rejected
invalid UTF-8 — plausible, since the value is remote-authored, and the plan's
test table carried a row for it. It shipped anyway: `encoding/json` replaces
every invalid byte and unpaired surrogate with U+FFFD while decoding a JSON
string, so a decoded Go string is valid UTF-8 by construction, and the gate
in this handler is only ever handed one. No input could have reddened that
branch, so the test row would have passed for a reason that had nothing to do
with the code it claimed to cover — an assertion no test can fail reads as
coverage of a hazard that was actually closed somewhere else entirely (the
decoder), which is a worse state than having no assertion at all. Dropped,
with the decoder's guarantee recorded in `mintLabelIsDisplaySafe`'s own doc
block, where the next person to touch this function meets it.

**The generalizable point:** before writing a validity check on a value that
already passed through `encoding/json`, ask what shape guarantee the decode
step already made. A check for a condition the decoder cannot produce is not
defense in depth — it is untested code with no path to ever redden, and the
test written to cover it proves nothing about the property it names.

## The requester's own name skipped the gate the minted name got, on a premise that was false — closed by #2219, not by upgrading this comment

**Resolved.** `RegisterPushToken` now runs the same character-class check
`mintLabelIsDisplaySafe` runs here, before `UpdatePushRegistration` and before the dedupe
comparison — see [`relay-package-handlers.md` § Display-safety
gate](relay-package-handlers.md#display-safety-gate-on-device_name-and-platform-2219). The
paragraph below is kept as written because the mistake it documents (a security review's
"no prior actor could do X" premise) is the reusable lesson; the fix it names is done.

**What #2219 did *not* upgrade this comment to say, deliberately:** closing the
`register_push_token` door is not the same as making `Device.Name` display-safe as a type
invariant. A name written to `devices.json` before the #2219 gate existed is read back
unchecked, and `pyry pair --name` — operator-authored — stays deliberately ungated, for
the reason this function's own doc block states. `pairingMinterV2.MintPairing`'s comment
now says exactly that rather than flipping "tracked, not closed" to "handled": rewriting
a known residual into an invariant claim is the same false-confidence shape the original
paragraph below argues against, one level up.

The plan's security review closed the log-injection question with "no client
could author a device label before this ticket" — true of the *minted*
device (this ticket is what lets a remote party name one), false of the
*requester*. `register_push_token` → `Registry.UpdatePushRegistration` has
assigned a phone's self-reported `device_name` straight to `Device.Name`
since #250, deliberately, and nothing on that path checks its shape (see
[`devices-registry.md`](devices-registry.md#phase-3-foundation-250)). So the
success log's `requesting_device` field and `auditMint`'s `audit.Entry.DeviceLabel`
— both populated from `s.device.Name`, both required by AC-4 — could carry a
control character through a door this ticket didn't open and couldn't have
closed by gating only the field it was already touching. Corrected in the
plan and the code rather than patched with a second gate at this one call
site: the same unchecked value already reaches a daemon log from
`RegisterPushToken` itself and the rekey handler, and an audit record from
`auditQuestion` and both `modalResolverV2` sites, so one predicate here would
have read as though the hazard were handled while leaving four other sinks
exactly as exposed. Filed as #2219 against the write site
(`UpdatePushRegistration`), which is the one place a single gate covers every
consumer.

**The generalizable point:** a security review's "no prior actor could do X"
premise is a claim about the *whole codebase's* write paths to the value in
question, not about the one path the current ticket happens to be adding.
Grep for other writers before trusting it, especially when the value being
newly gated is a field — like a display name — that reads as operator-owned
by convention but has a second, less obvious remote writer already wired in.

## Related

- [`pyry pair` — CLI device-pairing verb family](pyry-pair-command.md) — the
  shared `mintDevice` step, the local control sibling, and why both host
  operator paths pass `grantorHash: ""`.
- [Control plane](control-plane.md) — the local `pairing.mint` request and its
  fixed error/redaction boundary.
- [`devices.json` Registry](devices-registry.md) — `UpdatePushRegistration`,
  gated one step upstream by #2219, not inside the registry itself.
- [Pairing request/reply payloads (#2126)](protocol-package-types-pairing-payloads.md) —
  the frozen wire shapes this handler answers.
- [Error codes](protocol-package-constants-codes-go-error-codes-21.md) — the
  two `pairing.*` codes minted here and why neither is `auth.invalid_token`.
- [`internal/audit`](audit-package.md) — the mint's audit record, and
  `ModalID` left empty as the first of three callers with nothing to put
  there.
- [Inbound `request_history` (#2116)](v2-session-manager-state-machine-inbound-request-history-historypager-seam.md) —
  the outcome-discriminant seam shape this one copies.
- [Concurrency](v2-session-manager-concurrency.md) — the `appFrameKind`
  widening this ticket's dispatch case reuses.
