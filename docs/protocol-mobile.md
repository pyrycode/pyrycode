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
| **Endpoints** | `/v1/server`, `/v1/client` | `/v2/server`, `/v2/client` |
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
| `hello` | phone → binary | yes (in `noise_init`) | Includes device-token, last_seen_ts. |
| `hello_ack` | binary → phone | yes (in `noise_resp`) | Includes `conn_id`. |
| `send_message` | phone → binary | no | |
| `message` | binary → phone | no | |
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
    "last_seen_ts": "2026-05-08T08:14:02Z"
  }
}
```

Binary validates the token after decrypting the handshake message. If invalid, the binary sends an AEAD-sealed `error` envelope (code `auth.invalid_token`) inside a `noise_msg` and asks the relay to close with `4401`. The `noise_resp` may or may not have already been sent at this point — implementations should send it first (so the AEAD channel exists), then immediately send the auth error.

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

Before flipping the v2 release flag on any binary deployment, run `pyry pair list` and confirm it is empty (no paired devices). Pairing data from v1 is not v2-compatible (no `server_static_pubkey` was ever exchanged, no mobile-side Keystore alias was provisioned), and v2 has no migration tooling. A non-empty `pyry pair list` at release time means existing paired devices will fail on first v2 connect with `4426` and the user has no recovery path other than `pyry pair revoke && pyry pair` for every device.

This check is automated as a CI gate on the release workflow — see the implementation child ticket for the pre-flight verb.

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

- `2026-05-16`: v2 draft (this document). Adds end-to-end encryption via Noise_IK. Hard cutover from v1; v1 doc preserved in git history only.
- `2026-05-08`: v1 initial draft (superseded; see git history).
