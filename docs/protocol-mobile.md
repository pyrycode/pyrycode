# Mobile wire protocol — `v2`

Wire-format reference for the WebSocket protocol that mobile clients (Android / iOS) use to communicate with a pyrycode binary via the stateless `pyrycode-relay`. This is a separate concern from the [control-socket protocol](protocol.md) (Unix socket, local-only).

This document is the single source of truth. The pyry binary, the relay, and the mobile client implement against it.

## Status

`v2` — **draft.** No wire is live yet. v2 supersedes the v1 draft via hard cutover; v1 envelope shapes are not supported on the wire at any point. The pre-flight gate (`pyry pair list` empty, see [Pre-flight](#pre-flight-pyry-pair-list-empty-check) and #436) MUST pass before flipping the v2 release flag on any deployment, since v1 pair records have no `server_static_pubkey` and cannot complete a v2 handshake.

The v1 draft is preserved in git history (`git show HEAD~1:docs/protocol-mobile.md` at the point this rewrite landed) for archaeological reference only — it is not an implementation target.

## v2 changes from v1

v2 layers end-to-end encryption over the v1 wire while preserving the relay topology and the application-level message types. The changes are:

| Area | v1 | v2 |
|---|---|---|
| **E2E encryption** | None. Inner frames readable by the relay's process memory. | **Noise_IK over X25519/ChaChaPoly1305/BLAKE2s.** Inner frame is AEAD-sealed; relay sees ciphertext + opaque routing fields only. |
| **Endpoints** | `/v1/server`, `/v1/client` | `/v1/server`, `/v1/client` (unchanged — the relay is content-blind; the route path carries no protocol meaning, so it is not renamed to `/v2/...`) |
| **Inner-frame discriminator** | `type` is the application message type. | `type` is one of `noise_init` / `noise_resp` / `noise_msg`. Application message types are inside the AEAD-sealed payload of `noise_msg`. |
| **`payload_encrypted` flag** | Reserved; v1 rejects `true` with `protocol.unsupported`. | **Removed.** Encryption is structural in v2 — every transport frame is `noise_msg`. |
| **Pairing QR payload** | `{server, relay, token}` | `{server, relay, token, server_static_pubkey}` |
| **Binary-side key storage** | `devices.json` (token hashes only) | `devices.json` (unchanged) **+** `static_key.json` (per-binary X25519 keypair, `0600`) |
| **Mobile-side key storage** | `EncryptedSharedPreferences` (device tokens only) | `EncryptedSharedPreferences` (tokens) **+ Android Keystore** (per-paired-binary device-static keypair) |
| **Re-key** | None. | Time-based every **1 hour** + explicit `rekey_request` envelope. |
| **Version negotiation** | `protocol_versions: ["v1"]` in `hello`. | **Hard cutover.** Field retained shape-compatibly with `["v2"]`; v2 implementations do not negotiate down to v1. |

The application-level message types (`send_message`, `message`, `list_conversations`, `conversations`, `create_conversation`, `conversation_created`, `promote_conversation`, `conversation_updated`, `backfill_since`, `message_chunk`, `backfill_done`, `register_push_token`, `hello`, `hello_ack`, `error`, `ack`) are **unchanged** in v2. They simply live inside the AEAD-sealed payload of `noise_msg` frames.

## Scope

In scope:

- Topology: `phone <─WSS─> relay <─WSS─> binary` (unchanged from v1).
- **Noise_IK handshake** between mobile client and binary, terminating end-to-end through the relay.
- AEAD-sealed transport for all post-handshake application traffic.
- Re-key policy and on-wire shape.
- Static keypair generation, storage, and pairing-flow integration.
- v1 application message types, encapsulated.

Out of scope (v2):

- **Attachments.** v2 is the encryption layer; first attachment release rides on top.
- **Voice / WebRTC.** Phase 6 concern; signalling channel will be added later as new envelope types inside the AEAD channel.
- **Multi-device key sharing.** Each paired phone has its own Noise session and its own device-static keypair. No cross-device key sync.
- **Per-message-counter rotation.** Noise's 2⁶⁴ transport-message counter is not a practical limit; time-based + explicit-rekey is sufficient.
- **Push notification payload format.** Out-of-band channel; APNs/FCM payloads remain plaintext.
- **v1↔v2 fallback / migration tooling.** Hard cutover; pre-flight check is `pyry pair list` empty before flipping the v2 release flag.
- **Permission scoping.** Mobile-originated messages still execute with the same authority as the desktop; tiered scopes are a v3 concern.

## Topology recap

```
┌────────┐     WSS     ┌──────────┐     WSS     ┌────────────────┐
│ phone  │ ──────────> │  relay   │ <────────── │ pyrycode binary│
│ (N)    │             │(stateless)│             │ (1 per server) │
└────────┘             └──────────┘             └────────────────┘
                       (sees ciphertext +
                        routing fields only)
```

- The **binary** opens a long-lived outbound WSS connection to the relay (NAT-friendly).
- **Phones** open separate WSS connections to the relay, addressed to a particular server-id.
- The **relay** holds two connection maps: `server-id → binary connection` (1:1) and `server-id → [phone connections]` (1:N). It pipes WS frames between the two with a header read and a routing-envelope read; **it never inspects the inner frame**.

The binary owns canonical state (conversations registry, sessions, message history). The relay holds zero per-user state and, in v2, zero ability to inspect or modify per-user content.

## TLS

Unchanged from v1: the relay terminates TLS via Let's Encrypt autocert. v2 does not weaken TLS — it adds an inner E2E layer on top.

- Relay binds `:443` for production WSS and `:80` for the ACME http-01 challenge.
- Certificates auto-issued and auto-renewed; cached under `--cert-cache` (default `~/.pyrycode-relay/certs`).
- Domain is configured via `--domain`; non-matching Host headers receive `421 Misdirected Request`.

Reverse-proxy fronting (Caddy / nginx terminating TLS, plain WS internally) is supported via `--insecure-listen <addr>`; v2 makes this strictly safer than v1 because the inner E2E layer protects content regardless of who terminates TLS.

The binary connects with standard TLS verification — no pinning in v2. (Pinning is unnecessary because the Noise_IK channel is authenticated end-to-end by the binary's static public key, which the phone learned out-of-band at pairing time.)

## End-to-end encryption

### Cipher suite

`Noise_IK_25519_ChaChaPoly_BLAKE2s` — the same suite Tailscale's control protocol uses, and a strict superset of WireGuard's transport authentication (which uses IKpsk2).

- DH: Curve25519
- Cipher: ChaCha20-Poly1305 (AEAD)
- Hash: BLAKE2s

Rationale: the IK pattern is a natural fit for our pairing flow — the initiator (phone) already knows the responder's (binary's) static public key from the QR payload. IK fits a single round-trip handshake (one message each way) and carries arbitrary early-data payloads in both messages, which we use to piggyback the application-level `hello` and `hello_ack` (see [Handshake](#handshake)).

### Static keys — binary side

Each binary owns **one** Noise static keypair, shared across all paired phones for that server-id.

- Generated by `pyry pair` on first invocation if `static_key.json` does not exist, or loaded from disk if it does. Subsequent `pyry pair` invocations reuse the existing key.
- Stored at `~/.pyry/<daemon-name>/static_key.json` with mode `0600` enforced at process start. The parent directory `~/.pyry/<daemon-name>/` is enforced at mode `0700` at the same boundary. On mode-mismatch (file or directory), the binary refuses to start and logs a loud error, mirroring the pattern in `pyrycode-relay/internal/relay/tls.go:16-55` (which enforces `0700` on the cert cache directory).
- The `<daemon-name>` path component is canonicalised against an allowlist (lowercase alphanumerics plus `-` and `_`, no `..`, no `/`) before path construction. An untrusted daemon name MUST NOT be able to redirect the read/write to a different daemon's key file.
- File is opened with `O_NOFOLLOW` (where supported) on the read path so a symlink swap cannot redirect to an attacker-controlled location after the mode check.
- File format (JSON):
  ```json
  {
    "version": 1,
    "algorithm": "Noise_25519",
    "private_key": "<base64 raw 32 bytes>",
    "public_key": "<base64 raw 32 bytes>",
    "created_at": "2026-05-16T08:00:00Z"
  }
  ```
- The public key is emitted as `server_static_pubkey` in the QR pairing payload (see [Pairing flow](#pairing-flow)).
- Rotation is out of scope for v2. A rotation verb (`pyry rotate-static-key`) is a future concern; rotation invalidates all paired devices and forces re-pair.

### Static keys — mobile side

Each paired phone owns one Noise static keypair per paired binary, generated at pair time.

- Generated client-side at QR-scan time, before sending the first WS frame.
- **Stored in Android Keystore** under alias `pyrycode.device_static.<server-id>`. Hardware-backed where the device supports it; software-backed fallback otherwise. The public key is also mirrored to `EncryptedSharedPreferences` for fast read on connection startup; the private key never leaves the Keystore.
- iOS equivalent: Keychain entry with `kSecAttrAccessibleAfterFirstUnlock`. Hardware backing where Secure Enclave is available.
- Rotation: tied to re-pair. Revoking a device (via `pyry pair revoke`) invalidates that device's token AND its static key — the binary forgets both.

The binary side does **not** persist or even retain the mobile device-static public key across pairings — it learns the key from the first handshake message of each connection (Noise_IK transmits the initiator's static key on message 1, encrypted under the responder's static key). The binary's only persistent record of a paired phone is the device-token hash in `devices.json`; the device-static public key is in-memory-only for the duration of each connection.

This is deliberate: it keeps mobile-side key rotation invisible to the binary (no protocol coordination needed) and avoids growing `devices.json` with another field that must be kept in sync.

### Ephemeral keys

Both sides generate fresh ephemeral X25519 keypairs per handshake. Ephemeral private keys live in process memory only and are zeroised when the handshake completes (Noise's `CipherState` discards them automatically). They are never persisted.

### Pairing flow

`pyry pair` generates the QR/paste-string payload:

```json
{
  "server": "8f7e...",
  "relay": "wss://relay.pyrycode.dev",
  "token": "f0r...",
  "server_static_pubkey": "<base64 raw 32-byte X25519 public key>"
}
```

The `server_static_pubkey` field is the binary's persistent Noise static public key (see [Static keys — binary side](#static-keys--binary-side)). The phone:

1. Parses the QR payload.
2. Generates its own Noise static keypair (stored in Android Keystore as described above).
3. Stores `(server, relay, token, server_static_pubkey)` locally.
4. On every subsequent WS connect, uses `server_static_pubkey` as the responder's static key when constructing the Noise_IK initiator state.

Pairing UI is otherwise unchanged from v1 — same QR layout, same scan flow, same `pyry pair` CLI verb. The warning text mandated by v1's [UX implications](#ux-implications) section remains required.

### Handshake

Per Noise_IK:

```
Phone (initiator)                              Binary (responder)
─────────────────                              ──────────────────
                                                static key rs already known to phone (QR)

generate ephemeral e                           generate ephemeral e
write IK message 1:                            
  e, es, s, ss                                 
  + early payload (initiator's hello)
                       ─── noise_init ──→     read IK message 1, extract initiator's
                                                static key, early payload (hello)
                                              
                                              write IK message 2:
                                                e, ee, se
                                                + early payload (binary's hello_ack)
                       ←── noise_resp ───     
read IK message 2,                            
extract early payload (hello_ack)             

both sides derive (k_send, k_recv) CipherStates
─────────────── transport, AEAD-sealed ──────────────────────
                       ←── noise_msg ───→     (application traffic)
```

The handshake completes in **one round-trip** (one message from each side). The early-data payloads carry the v1-shaped `hello` and `hello_ack` envelopes verbatim, encoded as UTF-8 JSON. After the handshake, both sides hold paired AEAD CipherStates and switch to transport mode; all subsequent frames are `noise_msg`.

**Authentication.** Noise_IK gives the initiator (phone) cryptographic assurance that it is talking to the holder of the binary's static private key — no second binary can impersonate the real one even if the relay is compromised, because the relay never holds the binary's static private key. The binary, in turn, learns the phone's device-static public key from message 1 but uses the **device-token** (sent inside the encrypted `hello` early-payload, as in v1) for authorisation. The token-validation step is unchanged from v1 — only its transport changed (token now travels encrypted, not via the `token` field on the routing envelope; see [Routing envelope](#routing-envelope)).

**Token-validation gating.** Implementations MUST treat token validation as a precondition for application dispatch: any `noise_msg` received between handshake completion and successful token validation MUST be rejected (sealed `error` with `auth.invalid_token`, then `4401` close). In particular, the per-conn-id state machine MUST distinguish a `handshakeComplete` substate (CipherStates exist, token not yet validated) from `open` (validated), and the application handler chain in `internal/relay/handlers/` MUST NOT be reached from `handshakeComplete`.

**Failure modes.**

- **Phone presents wrong `server_static_pubkey`.** Handshake fails at `ReadMessage` on the binary side (MAC verification fails because `es` / `ss` produce wrong DH outputs). Binary closes the WS with code `4426` (handshake failure; see [Error codes](#error-codes)) without emitting an error envelope (no shared key to encrypt one). Phone surfaces "pair record may be stale; re-pair from binary."
- **Binary presents wrong static key.** Same outcome, mirror-image: phone's `ReadMessage` of `noise_resp` fails; phone closes WS with `4426`. This is the relay-impersonation case — Noise_IK is designed to detect it.
- **Token validation fails (after handshake completes).** Binary sends an AEAD-sealed `error` envelope (code `auth.invalid_token`) and asks the relay to close with `4401`. Identical UX to v1 from the phone's perspective.

### Transport

Once both sides hold CipherStates, every application frame is wrapped as a `noise_msg`. The plaintext payload (before AEAD-sealing) is exactly a v1-shaped application envelope (JSON, UTF-8). The sealed-and-base64-encoded ciphertext is the `data` field of the `noise_msg` outer frame.

**Associated data: empty.** AEAD sealing is performed with empty associated-data (`EncryptWithAd(nil, ...)` / `DecryptWithAd(nil, ...)`). The outer routing envelope (`conn_id`, `frame`) is intentionally NOT bound to the AEAD because (a) per-handshake key derivation isolates one session's ciphertext from another (replay across sessions fails MAC), and (b) per-session nonce-counter discipline isolates frames within a session (replay within session fails MAC). Binding `conn_id` into the AD would force a re-key on every relay-side routing change with no security benefit; the relay's threat model already excludes ciphertext tampering as a meaningful capability (MAC failure → connection close, no plaintext leak). Implementations MUST NOT pass a non-empty AD without a corresponding spec amendment.

Nonce management: Noise's CipherState carries a monotonic 64-bit counter. Both sides start at counter 0 after the handshake; each `EncryptWithAd` / `DecryptWithAd` increments. The wire does **not** carry the nonce — both sides derive it deterministically from their own counter. WebSocket gives ordered, lossless delivery within a session, so counter drift is not possible without a programming error or malicious frame injection (which AEAD detection would catch as a MAC failure → connection close).

Replay across sessions is impossible because handshakes are per-connection: new connection = new ephemeral keys = new CipherStates. An attacker who captures a v2 frame from session 1 cannot replay it into session 2 because the session-2 receiver has different keys.

### Re-key

Time-based re-key fires every **1 hour** of session uptime, measured from handshake completion. Either side may also request an immediate re-key at any time via the `rekey_request` envelope.

Mechanism: re-key is a full Noise_IK handshake re-run, **initiated by the phone** (since IK requires the initiator to start). The wire flow:

1. Either side decides re-key is due (1-hour timer elapsed, or operator triggered it).
2. If the binary initiates, it sends a `rekey_request` envelope (AEAD-sealed under the current CipherState, as a `noise_msg`). The phone receives it and proceeds to step 3.
   If the phone initiates (its own timer fired), it proceeds directly to step 3.
3. Phone sends a fresh `noise_init` frame (plaintext at the outer-frame layer; the IK initiator's static key authenticates it). **The early-data payload of a re-key `noise_init` is empty** (zero-length plaintext, AEAD-sealed under the handshake-derived key per Noise_IK rules). This mirrors WireGuard / Tailscale re-key flows: the handshake itself is the signal; no application-layer marker is needed inside it. Responder distinguishes re-key from initial handshake by `conn_id` state (already `open` or `awaitingRekeyInit`) — see #434's per-conn-id state machine and #435's `awaitingRekeyInit` substate.
4. Binary responds with `noise_resp`.
5. Both sides derive new `(k_send, k_recv)` CipherStates from the new handshake.
6. **Atomic switchover:** the first frame either side sends after the new handshake uses the new keys. The old CipherStates are zeroised. Any in-flight frame received under old keys after switchover is rejected (AEAD MAC failure) and the connection closes; this is acceptable because the WS is full-duplex and the switchover signal is the new handshake completing, not a wire marker.

The 1-hour cadence is deliberately short. The goal is to keep the rotation path exercised — any regression that breaks re-key will surface within ~1 hour of session uptime, not after 24 hours of silent breakage. The cost is negligible: a single round-trip's worth of WS frames and ~100µs of crypto, once per hour.

`rekey_request` envelope shape (inside the AEAD-sealed payload of a `noise_msg`):

```json
{
  "id": 42,
  "type": "rekey_request",
  "ts": "2026-05-16T09:00:00Z",
  "payload": {
    "reason": "scheduled"
  }
}
```

| Field | Type | Notes |
|---|---|---|
| `payload.reason` | string | One of `scheduled` (1-hour timer), `manual` (operator-triggered via `pyry rekey <conn_id>`), `compromise` (key-leak suspicion; same effect as `manual` but logged at higher severity). |

There is no `rekey_ack` envelope. The next successful AEAD round-trip under the new keys is the implicit ack — if the new handshake didn't take, the receiver gets an AEAD MAC failure on the first new-key frame and the connection closes; either side then reconnects from scratch.

### Wire shapes

The relay's outer routing envelope is **unchanged from v1**. The relay still wraps every binary↔relay leg in `{conn_id, frame, token?, close_code?}` and forwards `frame` opaquely. v2 only changes what `frame` looks like.

**Inner frame discriminator (`frame.type`):**

| `type` | Direction | Plaintext or AEAD-sealed? | When |
|---|---|---|---|
| `noise_init` | phone → binary | Plaintext outer; Noise IK message 1 inside | First frame after WS upgrade; also on every re-key |
| `noise_resp` | binary → phone | Plaintext outer; Noise IK message 2 inside | Reply to `noise_init` |
| `noise_msg` | both | AEAD-sealed payload | All post-handshake application traffic, including `rekey_request` |

Shape of `noise_init` and `noise_resp`:

```json
{
  "v": 2,
  "type": "noise_init",
  "data": "<base64 raw bytes from flynn/noise WriteMessage>"
}
```

```json
{
  "v": 2,
  "type": "noise_resp",
  "data": "<base64 raw bytes from flynn/noise WriteMessage>"
}
```

Shape of `noise_msg`:

```json
{
  "v": 2,
  "type": "noise_msg",
  "data": "<base64 ChaCha20-Poly1305 ciphertext, including 16-byte AEAD tag>"
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `v` | int | yes | Protocol major version. v2 sets `2`. Receivers MUST reject mismatched values with `4421` (protocol mismatch). |
| `type` | string | yes | One of `noise_init`, `noise_resp`, `noise_msg`. Unknown values → `protocol.unknown_type` envelope (when a key is available) or `4421` close (when not). |
| `data` | string | yes | Base64-encoded payload using **`base64.StdEncoding`** (standard alphabet, with padding). Raw bytes for `noise_init`/`noise_resp` (Noise framework's own framing). AEAD ciphertext for `noise_msg`. Decoded length cap: 65535 bytes (the Noise framework's per-message limit). |

**Application envelope (decrypted payload of a `noise_msg`)** — identical to v1's envelope, minus the `payload_encrypted` flag (which v2 removes):

```json
{
  "id": 42,
  "type": "send_message",
  "ts": "2026-05-16T10:33:14.012Z",
  "payload": { "conversation_id": "...", "message_id": "...", "text": "..." },
  "in_reply_to": null
}
```

Envelope-level fields beyond the v1 set:

| Field | Type | Required | Notes |
|---|---|---|---|
| `event_id` | int | no (omitempty) | Durable, per-conversation event id for the replay cursor (#649). Present **only** on interactive structured-stream frames (binary → phone; see [Interactive events](#interactive-events-v2-capability-gated)); absent on every other frame. Distinct from `id` (the per-conn envelope counter that resets each reconnect). Strictly increasing per conversation, stable across reconnects — the latest one a phone observes is a valid `last_event_id` to advertise on reconnect. Always ≥ 1 when present, so absence is unambiguous (omitted, not `null`/`0`). |

Encoding: line-delimited JSON over WS text frames. One outer envelope per frame. UTF-8.

**Application-envelope size cap.** Because every transport frame fits inside a single Noise transport message (65535 bytes including 16-byte AEAD tag), the decrypted application envelope is capped at **65519 bytes**. v1's 1 MiB `message.too_long` cap is **superseded** in v2; v2 implementations enforce the 65519-byte cap and emit `message.too_long` for any application envelope that, after JSON serialisation, exceeds it. Large payloads (e.g. attachments, deferred to a later v2 feature release) require an envelope-level chunking scheme that is out of scope for this spec.

## Identifiers

| Identifier | Format | Generated by | Public? | Notes |
|---|---|---|---|---|
| `server-id` | UUIDv4 | binary on first run | yes — in QR codes, sent unencrypted on WS upgrade | Relay's only routing key. |
| `device-token` | 256-bit random, hex-encoded | binary at `pyry pair` time | no — only in the QR / paste payload, then encrypted on wire | Plaintext on QR only; on wire it lives inside the AEAD-sealed `hello` early-data. Binary stores `sha256(token)` in `devices.json`. |
| `server_static_pubkey` | 32 raw X25519 bytes, base64 | binary on first `pyry pair` | yes — in QR codes (out-of-band trust anchor) | Authenticates the binary to the phone. Persistent across binary restarts. |
| `device_static_pubkey` | 32 raw X25519 bytes | phone at QR-scan time | no — sent encrypted under server_static_pubkey on first handshake message | Per-paired-binary on the phone. Not persisted by the binary. |
| `conversation-id` | UUIDv4 | binary or phone | no | Stable identifier for a Conversation entity. |
| `message-id` | UUIDv4 | sender | no | Stable per-message id; used for delivery acks and dedup. |
| `envelope-id` | uint64, monotonic per session | sender | no | Used for `ack` correlation. Resets to 1 after each new handshake (including post-re-key). |

## Authentication

### Binary → relay

Unchanged from v1. The binary opens `wss://<relay>/v2/server` with these request headers:

| Header | Required | Notes |
|---|---|---|
| `x-pyrycode-server` | yes | The server-id this binary is claiming. |
| `x-pyrycode-version` | yes | Binary's pyry version, e.g. `0.11.0`. |
| `user-agent` | yes | `pyry/<version>` for ops debugging. |

First-claim-wins. Conflict → `4409` close. 30-second grace period on disconnect.

**The leg is established the moment the WS upgrade completes.** There is no relay-originated `hello`/`hello_ack` handshake on the binary↔relay leg — under v2 a `hello_ack` would be AEAD-sealed application data the relay holds no key for, and server-id registration is purely header-based via `x-pyrycode-server` (the slot is claimed on upgrade). The binary goes straight to forwarding frames once the upgrade fires; it does not send a `hello` and does not wait for an ack. (The phone↔binary `hello`/`hello_ack` is a different leg — it survives as Noise_IK early-data, E2E-encrypted and relay-blind; see § Handshake.)

The route path label carries no protocol meaning. The relay registers the binary from the `x-pyrycode-server` header regardless of path, so `/v1/server` and `/v2/server` denote the same content-blind endpoint; the `/v1` labels are retained (and the `/v2/server` references elsewhere in this document denote the same endpoint) until a future cosmetic rename.

The binary does not authenticate to the relay via Noise — the relay isn't a Noise peer. The relay-issued admin token (deferred from v1) is still a separate future hardening and is not part of v2.

### Phone → relay → binary

The phone opens `wss://<relay>/v2/client` with:

| Header | Required | Notes |
|---|---|---|
| `x-pyrycode-server` | yes | Target server-id. |
| `x-pyrycode-device-name` | recommended | Human label. |
| `user-agent` | yes | `pyrycode-mobile/<version>`. |

**Changed in v2:** the `x-pyrycode-token` header is **removed**. The device-token now travels inside the AEAD-sealed early-data payload of the Noise_IK handshake (in the `hello` envelope). The relay never sees the token in v2.

This is a meaningful security improvement: the v1 design exposed the device-token to the relay's process memory on every connect (in the routing envelope's `token` field, which the relay added). In v2, the relay cannot extract device tokens even if its process memory is dumped or its operator is compromised.

### Routing envelope (binary↔relay leg)

Unchanged shape from v1:

```json
{
  "conn_id": "c-7f3a...",
  "frame": { /* the noise_init / noise_resp / noise_msg outer frame */ }
}
```

**Changed in v2:** the `token` field on the routing envelope is **deprecated and unused**. Relay implementations MUST NOT set it. Binary implementations MUST ignore it if present (for forward-compat with mixed-version deployments during the cutover window, even though no such window is supported on the wire — defensive coding only).

The `close_code` field on the binary→relay direction is unchanged.

Implementations MUST tolerate unknown fields on the routing envelope for forward compatibility.

## Connection lifecycle

### Binary

1. **Load static keypair** from `static_key.json` (or refuse to start if the file is missing or has wrong mode; do not auto-generate at daemon start — keys are only generated by `pyry pair`).
2. **Connect** WSS to `/v2/server` with headers above.
3. **Hold open** for inbound phone connections. The binary itself does not initiate a Noise handshake — it is always the Noise responder.
4. **For each phone WS forwarded by the relay**, expect a `noise_init` as the first frame within 10 seconds; otherwise close with `4421`.
5. **On disconnect from relay**, reconnect with exponential backoff (unchanged from v1).

### Phone

1. **Load device-static keypair** from Android Keystore. If missing, abort and surface "pair record corrupted; re-pair from binary."
2. **Connect** WSS to `/v2/client` with headers above.
3. **Send `noise_init`** as the first frame, with the v1-shaped `hello` envelope as the early-data payload. Include `last_seen_ts` in the `hello` payload as in v1 to trigger backfill.
4. **Await `noise_resp`** within 10 seconds; on timeout close with `4421`.
5. **Derive CipherStates**, switch to transport mode.
6. **Send / receive `noise_msg`** frames as the user interacts.
7. **Re-key timer** starts at handshake completion; fires every 1 hour.
8. **On disconnect**, reconnect with exponential backoff. The first frame on every reconnect is `noise_init` — there is no session resumption in v2 (deferred; see [Out of scope](#out-of-scope-v2)).

### Phone background behaviour

Unchanged from v1. The phone closes its WS when backgrounded; push-to-wake reconnects. Each reconnect performs a fresh Noise_IK handshake. Battery cost is unaffected — the handshake is ~one round-trip's wire cost plus ~100µs of crypto.

### Heartbeat

Unchanged from v1: WS-native ping/pong every 30s idle; 60s worst-case dead-connection detection. Ping/pong are WS control frames and are NOT routed through the Noise transport layer (they sit below the application protocol).

### Reconnect

Unchanged from v1: exponential backoff with ±20% jitter, capped at 30s, reset to attempt 1 after any successful connection lasting ≥ 60 seconds.

## Application message types

Unchanged from v1 except where noted. Every type below is sent as the **decrypted payload of a `noise_msg`** (post-handshake) or as the **early-data payload of a `noise_init` / `noise_resp`** (during handshake — only `hello` and `hello_ack` ride there).

| Type | Direction | Carries handshake early-data? | Notes |
|---|---|---|---|
| `hello` | phone → binary | yes (in `noise_init`) | Includes device-token, last_seen_ts; optional `last_event_id` for mid-turn reconnect replay (#647). |
| `hello_ack` | binary → phone | yes (in `noise_resp`) | Includes `conn_id`. |
| `send_message` | phone → binary | no | |
| `message` | binary → phone | no | v1 / dispatch-leg coarse assistant-turn type. Not minted on the v2 interactive path — the v2 coarse `message` fan-out was removed in #699; v2 assistant output flows only through the structured interactive stream below. |
| `list_conversations` | phone → binary | no | |
| `conversations` | binary → phone | no | |
| `create_conversation` | phone → binary | no | |
| `conversation_created` | binary → phone | no | |
| `promote_conversation` | phone → binary | no | |
| `conversation_updated` | binary → phone | no | |
| `backfill_since` | phone → binary | no | |
| `message_chunk` | binary → phone | no | |
| `backfill_done` | binary → phone | no | |
| `register_push_token` | phone → binary | no | |
| `ack` | either | no | |
| `error` | either | no | |
| **`rekey_request`** | either | no | **New in v2.** See [Re-key](#re-key). |
| **`turn_state`** | binary → phone | no | **New in v2** (interactive, capability-gated). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`assistant_delta`** | binary → phone | no | **New in v2** (interactive, capability-gated). |
| **`tool_use`** | binary → phone | no | **New in v2** (interactive, capability-gated). |
| **`tool_result`** | binary → phone | no | **New in v2** (interactive, capability-gated). |
| **`turn_end`** | binary → phone | no | **New in v2** (interactive, capability-gated). |
| **`stall`** | binary → phone | no | **New in v2** (interactive, capability-gated). |
| **`request_snapshot`** | phone → binary | no | **New in v2.** On-demand screen-snapshot request. See [Screen snapshot](#screen-snapshot-v2). |
| **`screen_snapshot`** | binary → phone | no | **New in v2.** See [Screen snapshot](#screen-snapshot-v2). |
| **`resync`** | binary → phone | no | **New in v2.** Mid-turn-reconnect resync marker — the advertised `last_event_id` aged out of the ring; phone must full-reload (#647). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`session_transition`** | binary → phone | no | **New in v2** (interactive, capability-gated). Session-boundary marker for `pyrycode-mobile#336` (#656). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`modal_shown`** | binary → phone | no | **New in v2** (interactive, capability-gated). Modal surfaced to the phone (#597 Phase 3). See [Modal](#modal-v2). |
| **`modal_answer`** | phone → binary | no | **New in v2.** Inbound control — phone answers a modal. See [Modal](#modal-v2). |
| **`modal_cancel`** | phone → binary | no | **New in v2.** Inbound control — phone cancels a modal. See [Modal](#modal-v2). |
| **`modal_dismissed`** | binary → phone | no | **New in v2.** Modal resolution notice. See [Modal](#modal-v2). |
| **`queue_state`** | binary → phone | no | **New in v2** (interactive, capability-gated). Queued-message backlog snapshot (#597 Phase 3). See [Queue](#queue-v2). |
| **`dequeue_message`** | phone → binary | no | **New in v2.** Inbound control — phone cancels a queued message. See [Queue](#queue-v2). |

Payload shapes for unchanged types are identical to v1. The relevant per-type schemas are preserved in git history (the v1 doc has them); they are not duplicated here because v2 adds no fields and removes no fields. Implementations MUST tolerate unknown fields in payloads for forward compatibility.

### `hello` (v2-specific note)

Sent in the `noise_init` early-data payload. The `payload.token` field carries the device-token (hex-encoded), now encrypted under the Noise_IK channel before it reaches the wire.

```json
{
  "id": 1, "type": "hello", "ts": "...",
  "payload": {
    "role": "client",
    "token": "f0r...",
    "device_name": "Juhana's Pixel 8",
    "client_version": "pyrycode-mobile 0.1.0",
    "protocol_versions": ["v2"],
    "last_seen_ts": "2026-05-08T08:14:02Z",
    "last_event_id": 42
  }
}
```

`last_event_id` (optional, omitempty — absent, not `null`, when the phone has no
position; key-absent keeps the v1 hello byte-identical) is the durable
per-conversation `event_id` (see [Interactive events](#interactive-events-v2-capability-gated))
the phone last saw on the interactive stream. On mid-turn reconnect the phone
advertises it so the daemon can replay the missed tail from the event ring or
emit a `resync` marker (#647). It is **untrusted input** — the daemon
range/shape-validates it (`*uint64` decode) and bounds replay by the ring; the
phone can never address a conversation other than the daemon's current one.

Binary validates the token after decrypting the handshake message. If invalid, the binary sends an AEAD-sealed `error` envelope (code `auth.invalid_token`) inside a `noise_msg` and asks the relay to close with `4401`. The `noise_resp` may or may not have already been sent at this point — implementations should send it first (so the AEAD channel exists), then immediately send the auth error.

### Capability negotiation (v2)

> **Superseded as a requirement — 2026-06-22 (ADR 025 amendment).** This tool is self-hosted with a single operator who controls both ends and ships the app and daemon together, so there is no old-app install base. The daemon assumes every phone is `interactive`; the non-interactive coarse `message` fan-out has been removed (#699). The `capabilities` field below stays as a harmless additive field, but it carries no backward-compatibility obligation and no future work should treat old-phone interop as a requirement.

Both `hello` and `hello_ack` carry an optional `capabilities: []string` field (omitempty — absent, not `null`, when empty, so a v1 phone's `hello` stays byte-identical). The phone advertises the features it understands in its `hello`; the daemon echoes the features *it* supports in `hello_ack`.

| Field | Type | On | Meaning |
|---|---|---|---|
| `capabilities` | `[]string` (omitempty) | `hello` (phone → binary) | Features the phone understands, e.g. `["interactive"]`. |
| `capabilities` | `[]string` (omitempty) | `hello_ack` (binary → phone) | Features the daemon supports and has agreed to. |

Defined capability strings:

| Value | Meaning |
|---|---|
| `interactive` | The phone can render the structured interactive event stream below. |

The daemon MUST echo only what it itself supports — the agreed set is the **intersection** of the phone's advertised set with the daemon's own, never a blind mirror of the phone's claims. A phone that does not advertise `interactive` (or whose `interactive` is not echoed back) simply does not receive the structured interactive event stream; there is no separate non-interactive `message` fan-out on v2 (it was removed in #699). The intersection logic — the daemon-side trust decision computing advertised ∩ supported, echoing it in `hello_ack`, and recording the negotiated `interactive` flag per connection — is implemented in #626 (`internal/relay` v2 session manager; `negotiateCapabilities` + the capability-aware `ActiveConns` enumeration). The capability-gated fan-out that routes the interactive event stream only to granted connections shipped in #632/#633.

### Interactive events (v2, capability-gated)

These six envelope types form the structured live-session stream. They are sent **binary → phone only**, and **only** to a phone whose `interactive` capability was echoed in `hello_ack`; an old phone never receives them. They are the wire representation of the daemon's neutral internal turn-event model. All *payload* fields are always present (no omitempty) so boundary values like `seq: 0` and `is_error: false` are explicit on the wire.

**Replay cursor (`event_id`, #649).** Every frame in this stream additionally carries an envelope-level `event_id` (the optional `Envelope` field above) — the durable, per-conversation id the daemon assigns to each structured event as it records it in a bounded per-conversation event ring (ADR 025 § Backpressure / replay). It is **not** the same as the envelope's `id`: `id` is a per-connection counter that resets each reconnect, whereas `event_id` is connection-independent, identical across all interactive connections for a given logical event, and strictly increasing in emit order per conversation. A phone records the latest `event_id` it has seen and, on mid-turn reconnect, advertises it as `last_event_id` in its `hello`; the daemon then replays the missed tail from the ring (or emits a `resync` marker if it fell off the bounded window). The **producer** side (the daemon stamping `event_id` outbound) landed in #649; the reconnect **consumer** (`hello.last_event_id`, ring replay, and the `resync` marker) landed in #647 — see [Reconnect replay & resync](#reconnect-replay--resync-consumer-647) below.

#### `turn_state`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation this turn belongs to. |
| `state` | string | Coarse turn lifecycle: `thinking`, `responding`, or `idle`. |

#### `assistant_delta`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation this turn belongs to. |
| `turn_id` | string | Identifies the turn the delta belongs to. |
| `seq` | int | Per-turn, non-negative delta-ordering counter; resets each turn. |
| `text` | string | Incremental assistant text, coalesced (not per token). |

#### `tool_use`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation this turn belongs to. |
| `turn_id` | string | Identifies the turn the tool call belongs to. |
| `tool_use_id` | string | Correlates this call with its later `tool_result`. |
| `name` | string | Tool name. |
| `input_summary` | string | Human-readable précis of the tool input (not the raw input). |

#### `tool_result`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation this turn belongs to. |
| `turn_id` | string | Identifies the turn the result belongs to. |
| `tool_use_id` | string | Matches the `tool_use` this result completes. |
| `is_error` | bool | Whether the tool invocation failed. |
| `result_summary` | string | Human-readable précis of the result (not the raw output). |

#### `turn_end`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation this turn belongs to. |
| `turn_id` | string | Identifies the turn that ended. |
| `stop_reason` | string | Why the turn ended; one of `end_turn`, `max_tokens`, `max_turn_requests`, `refusal`, `cancelled`. These mirror the ACP turn-end reasons. |

ADR 025's base `turn_end` shape is `{conversation_id, turn_id}`; `stop_reason` is added here per the implementing ticket (#607), following the "spec follows the code" convention (ADR 025 § Consequences).

#### `stall`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation that stalled. |

`stall` is the wire form of an internal-only daemon signal (a one-shot stall-onset marker; no ACP equivalent). Like `turn_state`, it is a coarse conversation-level signal and carries no `turn_id`. It is onset-only — there is no clearing event; the phone self-clears on the next turn activity.

#### `session_transition`

Direction **binary → phone** (outbound v2 session-boundary marker; not in `v1TypeSet` — an old phone never receives it). This is a **session-boundary marker, distinct from the six turn-stream events above** — it does not belong to the structured live-session stream and carries no `event_id`. It is the wire form of `pyrycode-mobile#336`'s `ThreadItem.SessionBoundary`: the daemon's session rotated, so the phone renders a boundary marker instead of inferring one from message fields that do not exist.

| Field | Type | Meaning |
|---|---|---|
| `previous_session_id` | string | The session id that ended. Always present (a transition sits between two sessions). |
| `new_session_id` | string | The session id that began. |
| `reason` | string | Why the session rotated. Closed set: `clear`, `idle_evict`, `workspace_change`. |
| `occurred_at` | string | When the transition occurred (RFC3339Nano). |
| `workspace_cwd` | string \| null | The new workspace directory — non-null **iff** `reason == workspace_change`; literal `null` for `clear` and `idle_evict`. |

**Invariant:** `workspace_cwd` is non-null **if and only if** `reason` is `workspace_change`. The field is always present on the wire (literal `null`, never absent) so the invariant is decodable directly.

The **producer** is **#657**. Until a server-side workspace-change source exists, the producer emits only `clear` and `idle_evict` — yet the type admits `workspace_change` so the mobile decoder stays exhaustive and the invariant above is expressible. This ticket (#656) defines the wire shape only.

#### Reconnect replay & resync (consumer, #647)

The inbound half of the [replay cursor](#interactive-events-v2-capability-gated). On mid-turn reconnect a phone advertises the latest `event_id` it saw as `hello.last_event_id` (see [`hello`](#hello-v2-specific-note)). The daemon resolves the conversation to replay from its **own** current cursor — never from anything the phone sends — and queries the bounded per-conversation event ring (ADR 025 § Backpressure / replay):

- **In-ring tail.** Events with `id > last_event_id` are replayed on that connection, in ascending order, **before** the live stream resumes. Each replay frame carries the same `event_id` it had originally (so the phone advances its cursor), and is AEAD-sealed under the freshly-handshaked session keys.
- **Caught-up.** `last_event_id` at or beyond the newest retained event → no replay; the live stream resumes normally.
- **Aged out → `resync`.** `last_event_id` predates the oldest retained event (the position fell off the bounded window) → the daemon emits a single `resync` marker instead of a partial, gap-ful replay, so the phone is never left with a silent gap. The phone responds with a full reload of the named conversation.
- **Absent.** A phone that sends no `last_event_id` receives no replay — just the normal live stream.

A phone that advertises no `last_event_id`, and any v1 phone, are unaffected (key absent → byte-identical hello).

`last_event_id` is **untrusted remote input**: it is range/shape-validated (`*uint64` decode rejects non-integers), the replay is bounded by what the ring retains (`MaxEventsPerConversation`), and it is scoped to the daemon-resolved conversation — a phone can never address another conversation's events. The phone SHOULD additionally de-duplicate replayed events by `event_id` (defence in depth; the two layers are independent).

##### `resync`

Direction **binary → phone** (outbound v2 control marker; not in `v1TypeSet` — an old phone never receives it).

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation the phone must full-reload. The daemon's own resolved id; never attacker-derived. |

The marker carries no `event_id` (it is not a structured event). On receipt the phone discards its `last_event_id` cursor for that conversation and performs a full reload (today via a fresh subscription; the dedicated `backfill_since` reload handler is a deferred follow-up — it needs a message-history store that does not exist yet).

> **Implementation status (2026-06-17).** The reconnect-replay daemon code shipped via PR #651 (merged 2026-06-08) with a code-review **MUST FIX** outstanding — the *caught-up* path set the per-connection dedup watermark from the untrusted `last_event_id`, which could silently suppress the live stream after a `/clear`-rotated reconnect or a hostile-large `last_event_id`. That defect is **resolved**: #663 clamps the caught-up watermark to the conversation's newest retained id (`min(afterID, NewestID(convID))`), so the wire contract above is now the shipped daemon guarantee. Fix record: [`docs/knowledge/codebase/663.md`](knowledge/codebase/663.md); defect history: [`docs/knowledge/codebase/647.md`](knowledge/codebase/647.md#️-known-issue--unresolved-code-review-must-fix-do-not-merge-as-is).

### Screen snapshot (v2)

The screen snapshot is the always-available, parser-independent **floor** of ADR 025's safe-degradation strategy (ADR 025 § Safe degradation). At any time the phone may ask for a one-shot text picture of the current claude screen; because the snapshot depends on no screen parser it survives any parser break and backs the stall fallback. The request/response pair is `request_snapshot` → `screen_snapshot`. All fields are always present (no omitempty).

#### `request_snapshot`

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session manager before `dispatch.Route` — it is not a `dispatch.Route` handler; the interception, render, and push live in the consumer ticket.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation whose current screen to snapshot. |

#### `screen_snapshot`

Direction **binary → phone**. The one-shot text picture answering a `request_snapshot`.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation this snapshot belongs to. |
| `text` | string | The current screen rendered to **plain text only — never raw terminal control codes** (preserves ADR 025's no-raw-bytes invariant). Multi-line. |
| `ts` | RFC3339 | When the snapshot was rendered. |

### Modal (v2)

When the supervised claude surfaces a modal — a permission prompt, a plan-approval, a tool-confirmation — the daemon describes it to the phone, the phone answers, and the daemon drives that answer back into claude (#597 Phase 3). The lifecycle is `modal_shown` → `modal_answer` / `modal_cancel` → `modal_dismissed`. `modal_shown` rides the `interactive` capability (#607): **viewing a modal is ungated**, but **answering is gated separately, per-device, default OFF** in the [Security model](#security-model) (#702) — that gate is not a wire capability. All fields are always present (no omitempty). This section is wire vocabulary only; the minting, dedup, validation, and fan-out runtime is the producer's (#703, with #706/#702 building ownership/gating).

#### `modal_shown`

Direction **binary → phone** (outbound v2 modal-surfaced event; not in `v1TypeSet` — an old phone never receives it).

| Field | Type | Meaning |
|---|---|---|
| `modal_id` | string | One-time opaque nonce minted per surfaced modal. The **sole correlation key** — see the security note below. |
| `class` | string | Modal kind over a closed wire set. Plain string, not a named enum; the exhaustive vocabulary is the producer's. The outbound surfacer (#716) ships the first concrete values: **`permission`** and **`trust`** (mapped from tui-driver's `ModalClassPermission` / `ModalClassTrustFolder`); non-permission/trust classes produce no `modal_shown`. |
| `title` | string | Short modal title (a fixed per-class label, e.g. `Permission required` / `Trust this folder?`). |
| `prompt` | string | The modal's body/question text — claude's rendered modal screen in plain text (ANSI/OSC-free, defensively length-bounded; #716). |
| `options` | array | Ordered list of `{id, label}` choices. **Array order is the canonical display/selection order** (claude's display order, allow-first). For `permission` the ids are the four `turnevent.PermissionOptionKind`s (`allow_once`/`allow_always`/`reject_once`/`reject_always`); for `trust`, `proceed`/`exit`. |
| `default_option_id` | string | The `id` of the default/highlighted option. **Invariant:** MUST equal one of `options[].id`. **Fail-safe convention (#716):** the producer sets this to the **deny** option (`reject_once` / `exit`), *not* `options[0]`, so a careless confirm on this remote surface denies rather than allows. Display order (allow-first) and the highlighted default are deliberately decoupled. This is UI pre-selection only — answering is gated separately (#702) and deny-on-timeout is the resolution half's (#717). |

#### `modal_answer`

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session manager before `dispatch.Route` — it is not a `dispatch.Route` handler; the interception, validation (against the daemon's current outstanding `modal_id`, #703/#706), and dedup live in the producer.

| Field | Type | Meaning |
|---|---|---|
| `modal_id` | string | The modal being answered. Validated against the daemon's current outstanding `modal_id`; a stale one is rejected (#706, first-answer-wins). |
| `option_id` | string | The selected `options[].id`. |
| `answer_token` | string | Client-minted idempotency key — see the security note below. |

#### `modal_cancel`

Direction **phone → binary** (inbound v2 control). Intercepted before `dispatch.Route` like `modal_answer`; no `dispatch.Route` handler.

| Field | Type | Meaning |
|---|---|---|
| `modal_id` | string | The modal to cancel/dismiss from the phone. |

#### `modal_dismissed`

Direction **binary → phone** (outbound v2 modal-resolution event; not in `v1TypeSet` — an old phone never receives it).

| Field | Type | Meaning |
|---|---|---|
| `modal_id` | string | The modal that was resolved. |
| `outcome` | string | The selected `options[].id` when answered, or a producer-defined sentinel for cancel/timeout. Plain string; the sentinel vocabulary is the producer's (#703), documented not enforced. |
| `source` | string | What resolved it. Closed set: `remote` (a phone `modal_answer`/`modal_cancel`), `local` (answered/cancelled at the desktop TTY), `timeout` (deny-on-timeout fired). |

**Security & validation contract.** `modal_id` is a one-time, **opaque, unguessable** nonce minted per surfaced modal, and it is the **sole correlation key**: these payloads carry no `conversation_id`. The daemon resolves `modal_id` against its **own** outstanding-modal state — it never trusts a phone-asserted conversation, and maps `option_id` against its own recorded option list. The daemon **rejects** an inbound `modal_answer`/`modal_cancel` whose `modal_id` is not the current outstanding one (#703/#706, first-answer-wins), so a stale or guessed `modal_id` resolves nothing. `answer_token` is a **client-minted idempotency key** (its uniqueness and stability matter; secrecy does not) that lets the daemon collapse a replayed or reordered `modal_answer` to a no-op. It is **not** the authorization: authorization is `modal_id` validity (#706) plus the per-device answer gate (#702, default OFF); `answer_token` only deduplicates among already-authorized answers. The minting + dedup + validation runtime is **#703/#706**; the answer gate is **#702**.

### Queue (v2)

A phone that types while claude is busy has its turn buffered in the daemon's queued-message backlog (#597 Phase 3, `internal/msgqueue`). `queue_state` (view) lets the phone see that backlog and `dequeue_message` (cancel) lets it drop an entry it no longer wants. Unlike the [Modal](#modal-v2) cluster — where answering is gated per-device — **both viewing and dequeuing are ungated for any paired phone** (ADR 025 [Security model](#security-model)): there is no per-device gate and no nonce; `queued_msg_id` is a plain per-conversation counter. All fields are always present (no omitempty). This section is wire vocabulary only; *when* the daemon emits `queue_state` and *how* the handler applies `dequeue_message` is the producer's (#722) / handler's (#723) runtime, documented there.

#### `queue_state`

Direction **binary → phone** (outbound v2 queued-backlog snapshot; not in `v1TypeSet` — an old phone never receives it).

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation this backlog belongs to. The daemon's own resolved id (#722), never attacker-derived. |
| `queued` | array | Ordered backlog (FIFO/enqueue order) of `{queued_msg_id, text, ts}`. **Always present**; the producer (#722) emits `[]` (not `null`) for an empty backlog so the mobile decoder keeps `queued` a plain array. Each element: `queued_msg_id` (integer, stable per-conversation counter ≥ 1), `text` (string, the queued message), `ts` (RFC3339Nano, enqueue time). |

**Emission (#722).** The daemon pushes a `queue_state` for a conversation whenever that conversation's backlog **changes** — a message is enqueued (a phone `send_message` buffered while claude is busy), drains to claude (the FIFO head delivered), or is removed before draining (`dequeue_message`). An empty backlog emits `queued: []` so the phone can clear its view. Each push carries **only** the changed conversation's items: the producer snapshots the single conversation named by the change and never bundles another conversation's queued text into the same payload (the `conversation_id` and `queued` are derived from one id, so they cannot desync).

`queue_state` fans **only** to connections that negotiated the `interactive` capability; a non-interactive connection never receives it (the same gate as the rest of the structured stream). The fan-out reaches *every* interactive connection, each payload stamped with its own `conversation_id` — there is no per-connection conversation binding, so a phone attributes each `queue_state` by id (consistent with the [Security model](#security-model): a user's paired devices are one trust domain). It is **not** part of the reconnect-replay ring; a phone that reconnects after missing a `queue_state` sees the current backlog only on the next change.

#### `dequeue_message`

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session manager before `dispatch.Route` — it is not a `dispatch.Route` handler. It is **ungated**; resolving `conversation_id` to an authorized conversation and applying the removal (`msgqueue.Remove`) is the handler's (#723) job.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation to dequeue from. Untrusted phone input — the handler (#723) resolves it to an authorized conversation. |
| `queued_msg_id` | integer | The id to remove (the `queued_msg_id` from a `queue_state` entry). |

## Backfill semantics

Unchanged from v1. All backfill frames ride inside `noise_msg`.

## Error codes

Application-level error codes (carried in `error` envelopes inside `noise_msg` payloads) are unchanged from v1, with these additions:

| Code | Retryable | Notes |
|---|---|---|
| `noise.handshake_failed` | no | Reported only to local logs — wire-level handshake failure closes the WS with `4426` and no AEAD-sealed envelope can be sent. Included here for completeness. |
| `noise.rekey_failed` | yes | The peer's `rekey_request` was rejected (e.g. rate-limited) or the subsequent handshake didn't complete; sender may retry after a backoff. |

WS close codes used at the transport layer:

| Code | Meaning | Sent by |
|---|---|---|
| `1000` | Normal closure | either |
| `1011` | Server error | either |
| `4401` | Unauthorized (bad device token) | binary (forwarded by relay) |
| `4404` | No server with that server-id | relay |
| `4409` | Server-id already claimed | relay (to a binary) |
| `4421` | Protocol mismatch (unknown `type`, bad `v`, malformed envelope) | either |
| **`4426`** | **Noise handshake failure** | either; v2-new |

`4426` is sent at the WS-close layer because the AEAD channel doesn't yet exist when a handshake fails — there is no shared key under which to send an `error` envelope.

## Security model

This protocol enables remote control of a machine running `claude` with broad permissions (filesystem read/write, shell execution, network access). The threat surface is therefore meaningfully larger than a typical chat protocol — a single auth bug or design flaw can yield arbitrary code execution on the user's machine. This section names the threats v2 was designed against, the residual risk after v2's mitigations, and what is still deferred.

### Threats

#### 1. Prompt injection — `severity: high`, `mitigation: partial` (unchanged from v1)

Phone messages become user-role input to `claude`. v2 changes nothing here; encrypting the channel doesn't make malicious prompts less malicious. The v1 mitigations apply unchanged (system-prompt prefix identifying mobile-originated messages; pairing-flow warnings).

#### 2. Server-id race — `severity: medium`, `mitigation: partial` (unchanged from v1)

Server-ids are still the only routing key on the relay. **However, v2 strengthens this in one important way:** even if an attacker successfully claims a server-id at the relay (winning the race against a real binary), they cannot impersonate the binary to a paired phone because they do not hold the binary's static private key. The Noise_IK handshake fails (the phone closes with `4426`). The phone surfaces "pair record may be stale; re-pair" but no plaintext is leaked.

In v1, a successful server-id race let the attacker harvest plaintext. In v2, it just denies service to that one server-id until the legitimate binary reconnects.

**Deferred:** Relay-issued admin token (independent of v2; orthogonal hardening).

#### 3. Relay operator MITM — `severity: high → low`, `mitigation: cryptographic`

**This is what v2 closes.**

In v1, a malicious relay operator (or a compromise of our VPS, the relay binary's supply chain, or our deploy keys) saw every byte of every conversation in plaintext. The mitigation was operational ("the relay MUST NOT log message bodies") — a policy commitment, not a guarantee.

In v2, the relay sees only:

- WS Upgrade headers (server-id, version, user-agent, device-name on the phone leg).
- The outer routing envelope (`conn_id`, `frame` as opaque base64-shaped JSON).
- The outer `frame.type` field (`noise_init` / `noise_resp` / `noise_msg`), enough to know whether to forward a frame but not what it contains.
- The opaque ciphertext payload.

The relay cannot decrypt any content. Even if the relay's entire process memory is dumped, no plaintext message text, no `cwd` paths, no conversation names, and no device tokens are recoverable.

**Residual risk:** Low. A malicious relay can still:

- **Refuse to forward.** This is a denial of service, not a confidentiality breach. The phone surfaces "binary offline" and retries.
- **Forward selectively or with delay.** Same — DoS class.
- **Forge `conn_id` mappings** to misroute frames between phones. Effect is that a frame sent by phone A may reach phone B's binary path, but phone B is talking to a different binary or no binary at all — the misrouted frame will fail AEAD verification on the receiving binary's CipherState and the connection is torn down. No plaintext leaks; no impersonation succeeds.
- **Observe metadata.** Connection counts, frame sizes, frame timing, device names, server-ids. v2 does not provide metadata privacy beyond what TLS provides on the network path. (Padding and timing obfuscation are out of scope for v2.)

**Residual risk before v2 closes the gap:** High. **Residual risk after:** Low.

#### 4. Token leak via phone — `severity: medium`, `mitigation: per-device revocation` (improved in v2)

v1 mitigations apply unchanged. **v2 adds:** the device-token never travels through the relay's process memory in plaintext — it lives inside the AEAD-sealed Noise channel from the moment it leaves the phone. A relay compromise no longer exposes tokens. (A compromise of the phone OR the binary still does, since both endpoints handle plaintext tokens.)

#### 5. Implementation bugs — `severity: variable`, `mitigation: defense-in-depth` (unchanged from v1)

`gosec`, `govulncheck`, `crypto/rand` for token generation, code-review on auth/crypto/networking changes. **v2 adds**: the Noise library (`flynn/noise`) is itself a dependency surface; `govulncheck` MUST be run against the pinned version on every release; the chosen pinned version is documented in `internal/encryption/README.md` (created as part of implementation children).

#### 6. Replay attacks — `severity: low → very low`, `mitigation: AEAD nonce`

v1 used per-connection envelope IDs (within-session replay detection) and WSS (wire encryption). v2 adds Noise's AEAD nonce-counter discipline: any replay within a session fails AEAD MAC verification at the receiver and tears down the connection. Across sessions, per-handshake-derived keys make replay structurally impossible (the captured ciphertext won't verify under the new key).

#### 7. Denial of service — `severity: low-medium`, `mitigation: deferred` (unchanged from v1)

Rate-limiting is orthogonal to encryption. Same posture as v1.

#### 8. Static-key compromise (new in v2) — `severity: high`, `mitigation: file mode + rotation verb`

If the binary's `static_key.json` leaks, an attacker can impersonate the binary to any paired phone for the lifetime of the leaked key. This is the v2-specific risk that didn't exist in v1.

**v2 mitigations:**

- File mode `0600` enforced at process start, with loud-failure if the file is world-readable. Mirror of `pyrycode-relay/internal/relay/tls.go:16-55`'s `0700` check.
- Static key generated by `crypto/rand` (Noise library handles this) and never logged.
- Static key not duplicated to any other on-disk location. No backups.

**Residual risk:** Medium. A compromise of the host filesystem (root, or the same user running the binary) extracts the key. Per-binary blast radius — one server-id's worth of paired phones.

**Deferred (v3):**

- Hardware-backed key storage (TPM, Secure Enclave on macOS) for the binary's static key.
- A rotation verb (`pyry rotate-static-key`) that generates a new key, invalidates the old one, and prompts all paired phones to re-pair.
- Detection of static-key exposure (e.g. anomalous handshakes from never-paired sources).

### UX implications

All v1 UX implications apply unchanged. **v2 adds:**

- The mobile pairing flow MUST display the binary's `server_static_pubkey` fingerprint (BLAKE2s(pubkey)[:8], hex) on the "Confirm pairing" screen so users can verify it against what `pyry pair` prints on the desktop. v1's QR-trust-on-first-use posture is unchanged; the fingerprint provides a secondary out-of-band verification path for paranoid users.
- The desktop `pyry pair` output MUST print the same fingerprint immediately under the QR, with a one-line hint: "verify this matches the fingerprint shown on your phone after scanning."

### Out of scope (security)

Unchanged from v1.

---

## Versioning

- Major version (`v1`, `v2`, …) appears in the URL path (`/v2/server`, `/v2/client`) and in the `v` field of every inner frame.
- Breaking changes to envelope structure or required fields require a new major version.
- Additive changes (new optional envelope types, new optional payload fields) stay within the same major version. Implementations MUST ignore unknown optional fields.
- v2 is shipped via **hard cutover**, not soft negotiation. The `protocol_versions` array in the `hello` payload is retained for forward-compat shape, but v2 implementations do not honour `v1` entries and v1 implementations are out of service when v2 lands.

### Pre-flight: `pyry pair list` empty check

Before flipping the v2 release flag on any binary deployment, run `pyry pair preflight` and confirm it exits 0. Pairing data from v1 is not v2-compatible (no `server_static_pubkey` was ever exchanged, no mobile-side Keystore alias was provisioned), and v2 has no migration tooling. A non-empty device registry at release time means existing paired devices will fail on first v2 connect with `4426` and the user has no recovery path other than `pyry pair revoke && pyry pair` for every device.

The `pyry pair preflight` verb is a dedicated, opt-in release gate (it does not alter the default `pyry pair list` output, so out-of-tree scripts that parse the table are unaffected). Its contract:

- **Exit 0** — registry empty. Gate passes; release tooling may proceed.
- **Exit 1** — registry I/O error or malformed `devices.json`. Wrapped error printed via `pyry: pair preflight: …`. Release tooling should treat this as "the check itself failed" (investigate the binary's state, retry).
- **Exit 2** — one or more paired devices exist. Single stderr line `pyry pair preflight: <N> paired device(s); v2 release gate requires zero.`. Release tooling should treat this as "the gate caught what it was supposed to catch" — do not flip the flag; the operator must `pyry pair revoke` per device first.

Stdout is silent on every branch. Same 0/1/2 convention as `grep(1)`.

Intended invocation in a release workflow:

```sh
if ! pyry pair preflight; then
    echo "v2 release gate failed; aborting" >&2
    exit 1
fi
```

A `case $?` block can distinguish the exit-1 (check failed) and exit-2 (gate fired) cases when finer-grained reporting is needed.

The pyrycode release tooling does not yet have a feature-flag mechanism distinct from version bumping (#436), so this documented invocation is the load-bearing artefact until one lands; the CLI exit-code behaviour is already in `pyry` itself.

## Reserved for future versions

- New top-level envelope fields starting with `x_` are reserved for experimentation. Receivers MUST NOT error on `x_`-prefixed fields they don't recognise; they MAY log them.
- Session resumption (à la TLS session tickets) is deferred. v3 candidate if reconnect cost (currently one round-trip + ~100µs of crypto per reconnect) becomes a measured battery-life problem on mobile.
- Per-message-counter rotation is deferred. Noise's 2⁶⁴ counter is not a practical limit; revisit if a real-world abuse case appears.
- Metadata privacy (padding, timing obfuscation) is deferred. v2's relay-can't-decrypt posture covers the high-severity threat; metadata observability is a residual concern.

## Locked decisions called out

- **Cipher suite: `Noise_IK_25519_ChaChaPoly_BLAKE2s`.** Aligns with Tailscale control-protocol and WireGuard transport. Single round-trip handshake.
- **Encrypt the whole inner frame, not just the application payload.** Outer routing envelope (`conn_id`, `frame`) stays plaintext for the relay; everything inside `frame` is AEAD-sealed (handshake frames are Noise-framed plaintext that authenticate via the IK pattern). This gives metadata privacy for free without a relay-routing rewrite.
- **Re-key cadence: 1 hour, time-based, + explicit `rekey_request` envelope.** Short cadence keeps the rotation path exercised; explicit verb covers on-demand cases.
- **Hard cutover, no negotiation.** No soft fallback to v1. Pre-flight `pyry pair list` empty check before the release flag flips.
- **Per-binary static keypair, shared across paired phones.** Stored at `~/.pyry/<daemon-name>/static_key.json` with `0600` enforcement at startup. Per-phone device-static keypair stored in Android Keystore on the mobile side. Ephemeral handshake keys process-memory only.
- **Pairing flow extended in QR payload only.** QR adds `server_static_pubkey`; UI, verb, and ergonomics unchanged.
- **No v1↔v2 fallback.** v1 has no shipped install base; nothing to migrate.
- **Multi-device echo: yes.** Same wire-load fan-out as v1. (Unchanged.)
- **Phone idle: close-on-background, push-to-wake.** Each reconnect performs a fresh Noise handshake. (Unchanged in shape; new in cost.)

## Open questions

None as of 2026-05-16. The four architect-level questions ((a) Go Noise library, (b) Kotlin Noise story, (c) re-key envelope shape, (d) `payload_encrypted` flag naming) are resolved and folded into the sections above. See [Implementation library choices](#implementation-library-choices) for the Go/Kotlin recommendations.

## Implementation library choices

### Go (binary side) — recommendation: `github.com/flynn/noise`

- **Status:** Maintained. Latest release v1.1.0 (Feb 2024). It is the canonical third-party Noise Protocol implementation in Go. (Note: Tailscale's control protocol uses the same cipher suite v2 requires but ships its own implementation at `tailscale.com/control/noise` rather than depending on flynn/noise; the parity v2 inherits is cipher-suite-level, not library-level.)
- **Noise_IK support:** First-class. `noise.HandshakeIK` constant exposed; the canonical cipher suite is constructed via `noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)`.
- **Early-data payloads:** `HandshakeState.WriteMessage(out, payload)` accepts an early-data payload on every handshake message. Returns the resulting wire bytes plus, on the final message, the paired `CipherState`s for transport.
- **Test vectors:** The library ships Noise specification test vectors (`vectors.txt`) and asserts against them in CI — confidence-inspiring.
- **Dependency footprint:** Pure Go, no cgo, no transitive dependencies beyond `golang.org/x/crypto`.

This is the no-controversy choice. The architect-spike requirement (validate that the library handles IK + early data correctly under the chosen cipher suite) is satisfied by the published API surface (`HandshakeIK` constant + `WriteMessage`/`ReadMessage` early-data payload semantics) and by the library's own CI assertion against Noise specification test vectors. Production deployments of the same cipher suite (Tailscale's own Noise implementation; WireGuard via `wireguard-go`) demonstrate the cipher choice itself is well-trodden, even though those deployments do not depend on flynn/noise directly.

**No alternatives considered viable.** `perlin-network/noise` is a P2P networking framework, not a Noise Protocol implementation (despite the name). No other mature Go Noise library exists.

### Kotlin / Android (mobile side) — three viable options, mobile team's pick

The architect's job here is to surface viable options. The mobile team commits to one when they pick up the mobile-side ticket cluster (filed in `pyrycode/pyrycode-mobile`, not in this repo).

**Option A (recommended): `rweather/noise-java`** — plain Java implementation of the Noise Protocol Framework, uses JCE primitives with fallback implementations for missing providers (relevant on older Android versions). Mature, longest-running of the JVM options. Trade-off: Java not Kotlin (purely a developer-ergonomics concern; interop is seamless).

**Option B: JNI wrapper to `noise-c`** — smallest binary footprint, native-speed crypto, used by Lightning Network mobile clients (eclair-mobile). Trade-off: NDK build complexity, native library shipping per ABI (x86_64, arm64, armeabi-v7a), CI complexity. Justified if v2's mobile build is sensitive to APK size or to JCE provider drift across Android versions.

**Option C: `sander/noise-kotlin`** — pure Kotlin, KMP-compatible. **Not recommended for v2.** Explicitly unaudited; requires the user to supply platform-specific implementations of the `Cryptography` interface (it doesn't ship its own ChaChaPoly1305 or X25519). More skeleton than library at the current release.

Recommendation: **start with A** for fastest path to shipping. Revisit B if APK-size analysis on the v2 mobile beta surfaces noise-java's footprint as a problem.

## Appendix: example flow — first pairing + first message (v2)

```
# Setup
$ pyry pair                          # binary generates token + ensures static_key.json exists
==> Server-id:           8f7e...
==> Relay:               wss://relay.pyrycode.dev
==> Token:               f0r...                                          # one-time display
==> Static-key fp:       a3:9b:c1:de:5f:0e:71:8c                          # BLAKE2s(pubkey)[:8], 64-bit, displayed for verify
==> Scan QR or paste this:
==> {
==>   "server":"8f7e...",
==>   "relay":"wss://relay.pyrycode.dev",
==>   "token":"f0r...",
==>   "server_static_pubkey":"<base64 32 bytes>"
==> }

# Phone scans, generates its own device-static keypair into Keystore (alias pyrycode.device_static.8f7e...).
# Phone displays "verify fp: a3:9b:c1:de:5f:0e:71:8c" — user confirms it matches the desktop.

# Binary's long-running WS to relay is already open (no Noise on this leg):
binary -> relay: WSS upgrade /v2/server
  headers: x-pyrycode-server: 8f7e..., x-pyrycode-version: 0.11.0
relay accepts, holds the connection.

# Phone connects:
phone -> relay: WSS upgrade /v2/client
  headers: x-pyrycode-server: 8f7e..., x-pyrycode-device-name: "Juhana's Pixel 8"
relay maps phone connection to server-id, assigns conn_id "c-7f3a..."

# Phone runs Noise_IK initiator step 1, with hello as early-data:
phone -> relay -> binary:
  binary receives: {
    conn_id: "c-7f3a...",
    frame: {
      v: 2, type: "noise_init",
      data: "<base64 IK message 1 bytes — includes phone's ephemeral, phone's static (encrypted),
              and early-data payload containing the hello envelope with token + last_seen_ts>"
    }
  }

# Binary runs Noise_IK responder step 1: reads IK message 1, extracts phone's static key
# and the hello early-data. Validates the device-token from the hello payload. Token valid.
# Binary writes IK message 2 with hello_ack as early-data:
binary -> relay -> phone:
  phone receives: {
    v: 2, type: "noise_resp",
    data: "<base64 IK message 2 bytes — includes binary's ephemeral and early-data payload
            containing the hello_ack envelope>"
  }

# Both sides now hold paired CipherStates. Phone starts re-key timer (1 hour).

# Phone asks for conversations (AEAD-sealed):
phone -> relay -> binary:
  binary receives: { conn_id: "c-7f3a...", frame: {
    v: 2, type: "noise_msg",
    data: "<base64 ciphertext of {id:2, type:'list_conversations', payload:{}}>"
  }}
binary decrypts under the receiving CipherState, processes, encrypts the response:
binary -> relay -> phone:
  phone receives: { v: 2, type: "noise_msg",
    data: "<base64 ciphertext of {id:2, type:'conversations', in_reply_to:2, payload:{...}}>"
  }

# Phone sends a message:
phone -> relay -> binary: noise_msg containing {id:3, type:"send_message", payload:{...}}
binary -> relay -> phone: noise_msg containing {id:3, type:"ack", in_reply_to:3, payload:{}}

# Claude responds; binary emits noise_msg with the assistant message envelope.

# 1 hour in, phone's timer fires:
phone -> relay -> binary: { v: 2, type: "noise_init", data: "<fresh IK message 1>" }
binary -> relay -> phone: { v: 2, type: "noise_resp", data: "<fresh IK message 2>" }
# Both sides rotate to new CipherStates atomically. Next noise_msg uses new keys.
```

## Security review

**Verdict:** PASS (after inline revisions on 2026-05-16).

This document is itself the architecture artefact for #430 (ticket carries `security-sensitive`), so the architect's adversarial self-review pass per `agents/architect/security-review.md` is recorded here rather than in a separate spec file.

**Findings:**

- **[Trust boundaries]** SHOULD FIX → FIXED — added "Token-validation gating" paragraph to §Authentication. Pre-validation `noise_msg` frames MUST NOT reach the application handler chain; the per-conn-id state machine has a distinct `handshakeComplete` substate that gates dispatch.
- **[Tokens, secrets, credentials]** No new findings — device-token handling is structurally improved (relay no longer sees plaintext tokens); static private key is generated by `crypto/rand` (via the Noise library), stored at `0600`, never logged. Static-key rotation is explicitly deferred to v3.
- **[File operations]** SHOULD FIX → FIXED — §Static keys — binary side now mandates (a) parent dir mode `0700`, (b) `<daemon-name>` allowlist canonicalisation to defeat path traversal, (c) `O_NOFOLLOW` on the read path. Atomic-write recipe inherited from project-level convention.
- **[Subprocess / external command]** N/A — v2 introduces no new subprocess paths.
- **[Cryptographic primitives]** SHOULD FIX → FIXED — fingerprint truncation standardised on **BLAKE2s(pubkey)[:8]** (64-bit) throughout, defeating 2³² preimage search. Cipher suite (`Noise_IK_25519_ChaChaPoly_BLAKE2s`) is a standards-grade choice; the same suite is used in production by Tailscale's control protocol and (in a closely-related form) by WireGuard transport, though both ship their own Noise implementations rather than depending on flynn/noise. Empty associated-data is now explicitly documented and justified in §Transport, with the rule that implementations MUST NOT pass a non-empty AD without a spec amendment.
- **[Network & I/O]** SHOULD FIX → FIXED — base64 alphabet pinned to `base64.StdEncoding`; per-frame size cap of 65535 decoded bytes named explicitly (matches the Noise framework's transport-message limit); §Application envelope size cap supersedes v1's 1 MiB cap with **65519 bytes** to fit inside the Noise transport message.
- **[Error messages, logs, telemetry]** No findings — handshake failures close at WS layer (`4426`) without emitting an `error` envelope (no oracle); private static key MUST NOT be logged (named explicitly in #431 AC); device-token transit moves out of relay process memory entirely.
- **[Concurrency]** No new findings — per-conn-id state machine (specified in #434) owns CipherStates on a single dispatch goroutine; flynn/noise CipherStates are not goroutine-safe and this constraint is documented in the child ticket. Re-key swap is atomic at the dispatch-goroutine level (#435).
- **[Threat model alignment]** No findings — every relevant threat in §Security model is named, with v2's change to its severity/mitigation surface explicitly stated. Threat #3 (relay operator MITM) moves from severity:high to severity:low with cryptographic-not-policy mitigation; threat #8 (static-key compromise) is new in v2 and addressed by file mode + rotation-deferred-to-v3.

**Reviewer:** architect (self-review per `agents/architect/security-review.md`)
**Date:** 2026-05-16

## Changelog

- `2026-06-07`: Retired the binary↔relay `hello`/`hello_ack` ceremony (#582). That leg is established on WS upgrade with header-based server-id registration; the relay sends no `hello_ack`. Endpoints stay `/v1/server`, `/v1/client` (route path carries no protocol meaning; `/v2` rename not performed).
- `2026-05-16`: v2 draft (this document). Adds end-to-end encryption via Noise_IK. Hard cutover from v1; v1 doc preserved in git history only.
- `2026-05-08`: v1 initial draft (superseded; see git history).
