# ADR 024 — Noise_IK over X25519/ChaChaPoly/BLAKE2s for mobile E2E encryption

## Status

Accepted (#430). Spec landed in [`docs/protocol-mobile.md`](../../protocol-mobile.md); implementation tickets filed as #431, #432, #434, #435, #436.

## Context

The v1 mobile protocol (WSS phone↔relay↔binary) leaves the inner mobile-protocol envelope readable by the relay's process memory. WSS protects the wire against network attackers but not against relay-operator compromise — a malicious or compromised relay sees every byte of every conversation, including device-tokens transiting in the routing-envelope `token` field and message bodies in `payload`.

v1 reserved a `payload_encrypted: true` flag for a future hardening; v2 turns that placeholder into a cryptographic guarantee so relay content-blindness stops being a policy commitment.

Two structural facts shape the choice:

1. **Pairing already exchanges out-of-band data** (server-id, relay URL, device-token) via QR / paste. Adding one more field (the binary's static public key) costs nothing UX-wise.
2. **The relay's two-layer envelope is already opaque-inside-routing.** The relay only reads `{conn_id, frame}`; it never deserialises `frame`. Encrypting the whole inner frame piggybacks on this contract — no routing rewrite needed, and frame metadata (envelope type, in_reply_to, etc.) is concealed for free.

## Decision

Adopt **Noise Protocol Framework**, pattern **`Noise_IK`**, cipher suite **`Noise_IK_25519_ChaChaPoly_BLAKE2s`**:

- **DH:** Curve25519
- **AEAD:** ChaCha20-Poly1305
- **Hash:** BLAKE2s

AEAD scope: the **entire inner mobile-protocol envelope** (every byte inside the relay's outer `frame` field). The outer routing envelope (`conn_id`, `frame`, optional `close_code`) stays plaintext so the relay can route.

Static-key disposition: one static keypair per binary, shared across all paired phones on that server-id, stored at `~/.pyry/<daemon-name>/static_key.json` with `0600` enforced at process start. The phone holds a per-paired-binary device-static keypair in Android Keystore (or iOS Keychain). Ephemeral keys process-memory only.

Re-key policy: time-based every **1 hour** plus explicit on-demand re-key via the `rekey_request` envelope. Re-key is a full Noise_IK handshake re-run, initiator-driven (phone always starts). Per-message-counter rotation is not used; Noise's 2⁶⁴ transport-message counter is not a practical limit.

Version negotiation: **hard cutover, no soft fallback.** v1 has no installed user base; a pre-flight `pyry pair list`-empty check (filed as #436) gates the v2 release flag.

## Rationale

### Why Noise_IK specifically

The pairing flow gives the initiator (phone) the responder's (binary's) static public key out-of-band. That is exactly the precondition Noise_IK is designed for. The IK pattern completes in **one round-trip** (one message each way) and carries arbitrary early-data payloads in both messages, which v2 uses to piggyback the v1-shaped `hello` and `hello_ack` envelopes. No separate authentication round-trip; no `noise_ack` envelope to define.

The IK pattern also provides the v2-load-bearing property at the relay-MITM surface: even if a malicious relay successfully races the binary at server-id claim, it cannot impersonate the binary to a paired phone because it does not hold the binary's static private key. The phone's `ReadMessage` fails at MAC verification on `noise_resp`; the connection closes with `4426`; no plaintext leaks.

### Why this cipher suite

`Noise_IK_25519_ChaChaPoly_BLAKE2s` is a standards-grade choice with multiple production-scale precedents:

- Tailscale's control protocol uses this exact cipher suite (via its own in-tree Noise implementation at `tailscale.com/control/noise`, not via `flynn/noise`).
- WireGuard transport uses a closely-related construction (IKpsk2 with the same DH + AEAD primitives).
- The Noise specification publishes test vectors for this suite, and the chosen Go library (`flynn/noise`, see ADR rationale below) asserts against them in CI.

ChaCha20-Poly1305 over AES-GCM: ChaChaPoly has constant-time software implementations on mobile (no AES-NI dependency), avoiding power-side-channel and timing-channel concerns on phones without dedicated AES hardware. BLAKE2s over SHA-256: standard Noise practice, ~30% faster on 32-bit platforms with no security trade-off.

X25519 is unambiguous — Curve25519 is the universal Noise DH primitive and the only one the Go reference library supports out of the box.

### Why encrypt the whole inner frame, not just `payload`

v1 reserved `payload_encrypted: true` for encrypting only the `payload` field, leaving envelope-level fields (`type`, `id`, `in_reply_to`, `ts`) in cleartext. v2 rejects this scope for two reasons:

1. **Metadata leakage.** A relay operator who sees `type: "send_message"` followed by `type: "ack"` followed by repeated `type: "message"` knows the conversation pattern even without bodies — a meaningful intelligence leak. Encrypting the whole envelope removes this side channel for free.
2. **No routing benefit lost.** The relay routes on `conn_id` (outer-envelope field), not on inner envelope fields. The outer layer stays plaintext; nothing the relay reads is concealed.

The `payload_encrypted` flag becomes structurally redundant in v2 — every transport frame is a `noise_msg` — so the flag is **removed**, not renamed. This is the resolution to architect-question #4 in the parent ticket.

### Why a 1-hour re-key cadence

Frequent re-keys keep the rotation path **exercised**. A regression in the re-key code that silently corrupts CipherStates or fails to switch over surfaces within ~1 hour of session uptime, not after 24h+ of silent breakage. The wire cost is one round-trip per hour (negligible against the WS ping/pong every 30s) and ~100µs of crypto. The cost of *not* exercising rotation is the failure mode WireGuard initially shipped with: rotation bugs that only surfaced under long-session production traffic, after the test matrix had moved on.

Per-message-counter rotation is the only obvious alternative. Rejected because Noise's per-CipherState 2⁶⁴ counter is not a practical limit at any realistic message rate (10⁹ messages/sec for ~580 years), and adding per-message rekey would multiply crypto cost by the message rate with no observable security benefit.

### Why hard cutover, no v1↔v2 negotiation

Decided at the ticket-locking step: v1 has no shipped install base; `pyry pair list` is empty across all paired binaries before the v2 release flag flips. A negotiation path (`protocol_versions: ["v1", "v2"]`) would require the binary to support both wire shapes simultaneously, which means the v1 envelope's `payload_encrypted: false` path must keep working — and the relay's v1 token-on-routing-envelope plaintext path must keep working — which means the v2 confidentiality property is undermined by the very fallback that exists to support it. Hard cutover keeps the v2 attack surface clean.

The pre-flight check (#436) makes this safe: if `pyry pair list` is non-empty at release time, the release is gated until either the operator revokes the v1 pair records or the architect re-opens the migration-tooling question.

### Why per-binary, not per-phone, static keys on the binary side

A binary serves multiple paired phones for one server-id. Two alternatives were considered:

- **Per-phone static keypair on the binary side.** Symmetric with the mobile side but doubles the binary's on-disk key material per paired phone and requires a key-lifecycle protocol on revoke (delete that phone's keypair). No security benefit — Noise_IK authenticates the *binary* to *each phone*; whether the binary uses one keypair for all phones or N is invisible to the threat model.
- **Per-binary static keypair, shared across phones (chosen).** One file, one mode check at startup, no per-phone lifecycle. Phone-side revoke (`pyry pair revoke <device>`) drops the device-token row in `devices.json`; the binary's static key is unaffected.

The phone side is the asymmetric case: each paired phone has its own device-static keypair in Keystore so that revoking one phone doesn't invalidate the others' sessions. The asymmetry is structural — the binary is the responder (one identity, N sessions); the phone is the initiator (its own identity per binary).

## Alternatives Considered

### A. TLS 1.3 mutual authentication (mTLS) between phone and binary

Would require either issuing per-phone client certificates from the binary (CA + revocation infrastructure) or pinning the binary's certificate on the phone (no rotation story). Both shift the trust anchor onto X.509 PKI, which is heavier than what the pairing-already-exchanges-static-keys flow needs. mTLS also doesn't compose cleanly with the relay's plaintext-routing-envelope contract — mTLS is end-to-end at the TLS layer, which means the relay would have to be either out-of-band or an mTLS-aware proxy.

### B. Signal Protocol (X3DH + Double Ratchet)

Over-engineered for the threat model. Double Ratchet provides forward secrecy and post-compromise security at per-message granularity — useful when the channel is long-lived and the message rate is high (Signal: chat between two phones, sessions can last years). Our channel is a WS connection bounded by network connectivity; sessions are minutes-to-hours, not years. Per-handshake forward secrecy (Noise_IK's ephemeral DH gives this) plus 1-hour re-key is the matched threat model.

Signal also requires a server-side prekey bundle store; we have no such store and don't want one.

### C. WireGuard transport (not as VPN — as protocol)

WireGuard's transport is essentially `Noise_IKpsk2`. Equivalent properties to our choice. Rejected because (a) WireGuard's reference is a kernel module, not a Go library, and adapting `wireguard-go` to non-tunnel framing is more work than `flynn/noise`'s direct API, and (b) WireGuard's PSK adds complexity (where does the PSK come from? what does it mean if it leaks?) without buying anything beyond what `Noise_IK` already gives us.

### D. Application-layer AEAD with a pre-shared key from pairing

Use the device-token directly as a symmetric AEAD key. Simple but has no forward secrecy (compromise of the token compromises every past message), no rekey story, and conflates *authentication* with *encryption* — revoking a device's token means re-issuing it, which means renegotiating its encryption key on every revoke. Noise_IK gets forward secrecy from ephemeral DH for free and keeps the token in its authentication-only role.

### E. Defer E2E to v3, ship v2 as a hardened-v1

Considered briefly. Rejected because the deferral cost compounds — every v1 feature shipped without E2E becomes a v3 retrofit, and the relay-operator-trust framing was already creating friction in conversations about deploying the relay on shared infrastructure. v2 closes the gap before the gap calcifies into a constraint.

## Consequences

- **Relay-operator compromise no longer leaks plaintext.** The high-severity threat #3 in [`docs/protocol-mobile.md`](../../protocol-mobile.md) § "Relay operator MITM" moves from `severity: high, mitigation: trust-based` to `severity: low, mitigation: cryptographic`. The relay's `threat-model.md` § "Deploy security" entry in `pyrycode-relay` correspondingly moves from policy framing to guarantee framing once the relay-side accept-and-forward change lands.
- **Static-key compromise is a new threat (#8, severity: high).** Did not exist in v1. Mitigation is file mode `0600` enforced at startup, no logging, no backups, generated by `crypto/rand` via the Noise library. Rotation verb (`pyry rotate-static-key`) deferred to v3. Per-binary blast radius — one server-id's worth of paired phones.
- **The relay sees less.** Device-tokens no longer transit the relay's process memory (v1 put them on the routing envelope's `token` field; v2 ships them inside the AEAD-sealed `hello` early-data). Inner envelope types (`send_message`, `message`, `ack`, etc.) are invisible to the relay; only the outer `noise_init` / `noise_resp` / `noise_msg` discriminator is observable. The relay's contract gets *easier* to write correctly because there are fewer load-bearing fields to handle.
- **Application-envelope size cap drops to 65519 bytes** (Noise's 65535-byte transport-message limit minus 16-byte AEAD tag). v1's 1 MiB `message.too_long` cap is superseded. Large payloads (attachments) need envelope-level chunking, out of scope for v2.
- **Pre-flight gate is load-bearing.** `pyry pair list` empty before v2 release flag flip is the only thing standing between "ship v2" and "every existing paired phone breaks at the next connect with `4426`." Filed as #436; CI-gated on the release workflow.
- **Re-key timer is the canary for the rotation path.** A regression that breaks re-key surfaces within ~1 hour of session uptime, not the next day. Cheap, loud, and operationally invisible when it works.
- **Static key never rotates in v2.** A rotation verb is v3 work. If a static-key compromise is suspected before v3, the operational recovery is `pyry pair revoke` for every device + manual `static_key.json` regeneration + manual re-pair. Documented as an accepted v2 risk.

## Related

- Spec: [`docs/protocol-mobile.md`](../../protocol-mobile.md) — the wire-protocol document this ADR rationalises.
- Codebase note: [`codebase/430.md`](../codebase/430.md) — what was built for this ticket.
- ADR 021: [`021-pair-cli-order-of-operations.md`](021-pair-cli-order-of-operations.md) — pairing-verb ordering; v2 extends the QR payload, ordering invariants unchanged.
- Implementation children (filed by this ticket): #431 (Go Noise library wiring + static-key generation/storage), #432 (QR-payload extension), #434 (per-conn-id state machine + handshake), #435 (re-key timer + `rekey_request` envelope), #436 (pre-flight `pyry pair list`-empty check).
- Go Noise library: `github.com/flynn/noise`, pinned in #431.
- Cipher-suite precedent: Tailscale's `tailscale.com/control/noise` (same suite, separate implementation); WireGuard transport (closely-related IKpsk2 construction).
