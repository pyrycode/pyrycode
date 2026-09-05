# Mobile wire protocol — `v2`

Wire-format reference for the WebSocket protocol that mobile clients (Android / iOS) use to communicate with a pyrycode binary via the stateless `pyrycode-relay`. This is a separate concern from the [control-socket protocol](protocol.md) (Unix socket, local-only).

This document is the single source of truth. The pyry binary, the relay, and the mobile client implement against it.

## Status

`v2` — **shipped and the only wire the daemon speaks.** The `PYRY_MOBILE_V2=0` v1 dispatch escape hatch was removed from `startRelay` in [#913](knowledge/codebase/913.md) — `cmd/pyry` now runs the v2 leg unconditionally, with no runtime switch back to v1. v2 supersedes the v1 draft via hard cutover; v1 envelope shapes are not supported on the wire at any point. The pre-flight gate (`pyry pair list` empty, see [Pre-flight](#pre-flight-pyry-pair-list-empty-check) and #436) MUST pass before enabling v2 on any deployment that still carries v1 pair records, since those records have no `server_static_pubkey` and cannot complete a v2 handshake.

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

The application-level message types (`send_message`, `message`, `list_conversations`, `conversations`, `create_conversation`, `conversation_created`, `promote_conversation`, `conversation_updated`, `register_push_token`, `hello`, `hello_ack`, `error`, `ack`) are **unchanged** in v2. They simply live inside the AEAD-sealed payload of `noise_msg` frames.

## Scope

In scope:

- Topology: `phone <─WSS─> relay <─WSS─> binary` (unchanged from v1).
- **Noise_IK handshake** between mobile client and binary, terminating end-to-end through the relay.
- AEAD-sealed transport for all post-handshake application traffic.
- Re-key policy and on-wire shape.
- Static keypair generation, storage, and pairing-flow integration.
- v1 application message types, encapsulated.

Out of scope (v2):

- **Attachments — the daemon-side implementation.** The *wire contract* is published in this document ([Attachments](#attachments)): the shared `attachment_chunk` frame, its chunking and reassembly rules, and the `attachment.*` reject vocabulary. What is still out of scope is everything behind it — nothing in the daemon emits, accepts, stores or enforces any of it yet.
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
| `event_id` | int | no (omitempty) | Durable event id for the replay cursor (#649), **unique daemon-wide** (#2022). Present **only** on interactive structured-stream frames (binary → phone; see [Interactive events](#interactive-events-v2-capability-gated)); absent on every other frame. Distinct from `id` (the per-conn envelope counter that resets each reconnect). Strictly increasing in the daemon's emit order across **all** conversations, and therefore ascending **but not contiguous** within any one of them — a conversation's own ids have another conversation's in between, and the first id a conversation is ever assigned is normally well above 1. Stable across reconnects; the latest one a phone observes is a valid `last_event_id` to advertise on reconnect. Always ≥ 1 when present, so absence is unambiguous (omitted, not `null`/`0`). A single scalar cursor over this id space is now correct: **no future event in any conversation can carry an id at or below one already observed**. Ids do **not** survive a daemon restart (the ring is in-memory) — that boundary is the `resync` marker's job. |

Encoding: line-delimited JSON over WS text frames. One outer envelope per frame. UTF-8.

**Application-envelope size cap.** Because every transport frame fits inside a single Noise transport message (65535 bytes including 16-byte AEAD tag), the decrypted application envelope is capped at **65519 bytes**. v1's 1 MiB `message.too_long` cap is **superseded** in v2; v2 implementations enforce the 65519-byte cap and emit `message.too_long` for any application envelope that, after JSON serialisation, exceeds it. Large payloads are carried by an **envelope-level chunking scheme**, not by a bigger envelope: a file is split into `attachment_chunk` frames each carrying at most **45000 raw bytes** of file data before base64, which is what keeps a chunk's serialised envelope under this cap — see [Attachments](#attachments). That per-chunk bound is a **producer-side contract with no validator**; an application envelope that exceeds 65519 bytes after serialisation is rejected by the transport with `message.too_long`, never with an `attachment.*` code.

## Identifiers

| Identifier | Format | Generated by | Public? | Notes |
|---|---|---|---|---|
| `server-id` | UUIDv4 | binary on first run | yes — in QR codes, sent unencrypted on WS upgrade | Relay's only routing key. |
| `device-token` | 256-bit random, hex-encoded | binary at `pyry pair` time | no — in the QR / paste payload, then on wire as an opaque header plus AEAD-sealed early-data | Plaintext on QR only. The binary validates it from the AEAD-sealed `hello` early-data; a copy also rides the required, relay-opaque `x-pyrycode-token` header at WS upgrade (see [Phone → relay → binary](#phone--relay--binary)). Binary stores `sha256(token)` in `devices.json`. |
| `server_static_pubkey` | 32 raw X25519 bytes, base64 | binary on first `pyry pair` | yes — in QR codes (out-of-band trust anchor) | Authenticates the binary to the phone. Persistent across binary restarts. |
| `device_static_pubkey` | 32 raw X25519 bytes | phone at QR-scan time | no — sent encrypted under server_static_pubkey on first handshake message | Per-paired-binary on the phone. Not persisted by the binary. |
| `conversation-id` | UUIDv4 | binary or phone | no | Stable identifier for a Conversation entity. |
| `message-id` | UUIDv4 | sender | no | Stable per-message id; used for delivery acks and dedup. |
| `envelope-id` | uint64, per-connection counter | sender | no | The envelope's `id`. A per-connection counter for `ack` / `in_reply_to` correlation only. It resets on every reconnect and is **not** globally monotonic, so it is **not** a durable dedup key. Many daemon-originated frames do not even carry a meaningful incrementing `id` (the phone correlates those on `in_reply_to`, `event_id`, `modal_id`, or `type`). Clients MUST NOT dedup by `id`. For durable, cross-reconnect ordering and dedup, use `event_id`, which **is** unique daemon-wide and monotonic in emit order (#2022) — see [Interactive events](#interactive-events-v2-capability-gated). |

## Authentication

### Binary → relay

Unchanged from v1. The binary opens `wss://<relay>/v1/server` with these request headers:

| Header | Required | Notes |
|---|---|---|
| `x-pyrycode-server` | yes | The server-id this binary is claiming. |
| `x-pyrycode-version` | yes | Binary's pyry version, e.g. `0.11.0`. |
| `user-agent` | yes | `pyry/<version>` for ops debugging. |

First-claim-wins. Conflict → `4409` close. 30-second grace period on disconnect. The binary treats a `4409` as **fatal only when it persists**: it retries through the standard backoff ladder and unwinds only after 8 consecutive `4409` closes (≥ ~73s of backoff even at minimum jitter — longer than the relay's 60s worst-case dead-connection detection plus grace reclaim). A transient self-conflict — the binary reconnecting after a drop before the relay has noticed its old connection is dead — clears within that window; a genuine duplicate binary keeps conflicting and the loser shuts down (#1072).

**The leg is established the moment the WS upgrade completes.** There is no relay-originated `hello`/`hello_ack` handshake on the binary↔relay leg — under v2 a `hello_ack` would be AEAD-sealed application data the relay holds no key for, and server-id registration is purely header-based via `x-pyrycode-server` (the slot is claimed on upgrade). The binary goes straight to forwarding frames once the upgrade fires; it does not send a `hello` and does not wait for an ack. (The phone↔binary `hello`/`hello_ack` is a different leg — it survives as Noise_IK early-data, E2E-encrypted and relay-blind; see § Handshake.)

The route path label carries no protocol meaning. The relay registers the binary from the `x-pyrycode-server` header regardless of path. The path stays `/v1/server` even though the application protocol is v2, because the relay is protocol-agnostic: the version lives in the `v` field of every inner frame, not in the route. The relay routes only `/v1/server` and `/v1/client`; there is no `/v2/*` route.

The binary does not authenticate to the relay via Noise — the relay isn't a Noise peer. The relay-issued admin token (deferred from v1) is still a separate future hardening and is not part of v2.

### Phone → relay → binary

The phone opens `wss://<relay>/v1/client` with:

| Header | Required | Notes |
|---|---|---|
| `x-pyrycode-server` | yes | Target server-id. |
| `x-pyrycode-token` | yes | The device-pairing token. The relay hard-requires it: a tokenless upgrade is rejected with HTTP 400. The relay treats the value as opaque (it never parses, compares, logs, or forwards it), but it does receive it; the binary validates the token from the AEAD-sealed `hello` early-data. |
| `x-pyrycode-device-name` | recommended | Human label. |
| `user-agent` | yes | `pyrycode-mobile/<version>`. |

**Changed in v2:** the binary validates the device-token from the AEAD-sealed early-data payload of the Noise_IK handshake (the `token` field of the `hello` envelope), not from a relay-forwarded routing-envelope field as in v1. The `x-pyrycode-token` header is **still required today**: the relay hard-requires it at WS upgrade and rejects a tokenless connect with HTTP 400. The relay treats the header value as opaque — it never parses, compares, logs, or forwards it — but it does receive it, so the token is present in relay process memory at upgrade time.

> **NOT YET IMPLEMENTED (transitional).** The intended v2 end-state drops the `x-pyrycode-token` header entirely, so the token would travel only inside the AEAD-sealed handshake and never touch the relay. The deployed relay has not reached that state: the header is required and its value passes through the relay opaquely. A client author MUST send the `x-pyrycode-token` header today.

This still improves on v1: v1 exposed the device-token to the relay as a value the relay parsed and forwarded to the binary (in the routing envelope's `token` field the relay added); v2 makes the header opaque to the relay and moves token *validation* into the AEAD-sealed `hello`. But it is **not** true that the relay cannot see the token — the header is required, so a memory dump taken during a phone upgrade contains the opaque token. The relay simply never parses, logs, or persists it.

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
2. **Connect** WSS to `/v1/server` with headers above.
3. **Hold open** for inbound phone connections. The binary itself does not initiate a Noise handshake — it is always the Noise responder.
4. **For each phone WS forwarded by the relay**, expect a `noise_init` as the first frame within 10 seconds; otherwise close with `4421`.
5. **On disconnect from relay**, reconnect with exponential backoff (unchanged from v1).

### Phone

1. **Load device-static keypair** from Android Keystore. If missing, abort and surface "pair record corrupted; re-pair from binary."
2. **Connect** WSS to `/v1/client` with headers above.
3. **Send `noise_init`** as the first frame, with the v1-shaped `hello` envelope as the early-data payload — the device token is what the payload must carry. A phone reconnecting mid-turn additionally includes `last_event_id` to be replayed the tail it missed (see [Reconnect replay & resync](#reconnect-replay--resync-consumer-647)). The payload's `last_seen_ts` is **accepted and ignored**: nothing reads it, so sending it triggers no backfill and changes nothing.
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

Unchanged from v1: exponential backoff with ±20% jitter, capped at 30s, reset to attempt 1 after any successful connection lasting ≥ 60 seconds. A close code listed as fatal (`4409`) does not terminate the loop on first observation: it is retried on the same ladder and becomes terminal only after 8 consecutive fatal closes (see § Authentication → Binary → relay; #1072). This subsection covers reconnect **timing** only; the **application-layer** reconcile-on-connect contract — what control and transcript state the daemon re-asserts once the handshake completes — lives in [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics).

## Application message types

Unchanged from v1 except where noted. Every type below is sent as the **decrypted payload of a `noise_msg`** (post-handshake) or as the **early-data payload of a `noise_init` / `noise_resp`** (during handshake — only `hello` and `hello_ack` ride there).

| Type | Direction | Carries handshake early-data? | Notes |
|---|---|---|---|
| `hello` | phone → binary | yes (in `noise_init`) | Includes the device-token; optional `last_event_id` for mid-turn reconnect replay (#647). Also accepts `last_seen_ts`, which **no daemon code reads** — it is decoded and dropped, drives no backfill, and a `hello` carrying it behaves identically to one omitting it (#2090). |
| `hello_ack` | binary → phone | yes (in `noise_resp`) | Includes `conn_id`. |
| `send_message` | phone → binary | no | Carries an **optional** `attachment_ids` — the uploaded attachments this message references, so the daemon names them instead of inferring the set from upload order or arrival timing (#2036). Each element is a lowercase UUIDv4 per [The `attachment_id` shape](#the-attachment_id-shape); **at most 32 per message**, counting elements rather than distinct ids. A message naming none **omits the key entirely** — `null` and `[]` are also accepted and a receiver cannot tell the three apart. **Consumed since #2038**: the daemon names each attachment's on-host path in `claude`'s prompt, refuses an over-bound list with `protocol.malformed`, and deduplicates a repeated id. See [Naming a message's attachments](#naming-a-messages-attachments). |
| `message` | binary → phone | no | v1 / dispatch-leg coarse assistant-turn type. Not minted on the v2 interactive path — the v2 coarse `message` fan-out was removed in #699; v2 assistant output flows only through the structured interactive stream below. |
| `list_conversations` | phone → binary | no | |
| `conversations` | binary → phone | no | |
| `create_conversation` | phone → binary | no | |
| `conversation_created` | binary → phone | no | |
| `promote_conversation` | phone → binary | no | |
| `conversation_updated` | binary → phone | no | |
| `rename_conversation` | phone → binary | no | Renames an existing conversation; replies with the reused `conversation_updated` record. |
| `delete_conversation` | phone → binary | no | Permanently removes a conversation (hard delete; the reversible path is `archive_conversation`); replies with `conversation_deleted`. |
| `conversation_deleted` | binary → phone | no | Acknowledges a `delete_conversation`, correlated by `in_reply_to`; carries only the deleted conversation's `id` (the record no longer exists, so no name/cwd is projected). |
| `archive_conversation` | phone → binary | no | Sets a conversation's durable archived flag (`IsArchived = true`); replies with the reused `conversation_updated` record. Symmetric restore is `unarchive_conversation` (shared payload). |
| `unarchive_conversation` | phone → binary | no | Clears a conversation's durable archived flag (restore, `IsArchived = false`); replies with the reused `conversation_updated` record. |
| `change_workspace` | phone → binary | no | Moves a conversation to a client-chosen workspace folder (updates its `cwd`, confined to `$HOME`); replies with the reused `conversation_updated` record. |
| `create_workspace_folder` | phone → binary | no | Creates a new folder on the daemon host under a client-supplied parent path (confined to `$HOME`); touches no conversation registry. Replies with `workspace_folder_created`. |
| `workspace_folder_created` | binary → phone | no | Reply to `create_workspace_folder`, correlated by `in_reply_to`; carries the created folder's canonical (symlink-resolved) absolute path. |
| `recent_workspaces` | phone → binary | no | Read verb (like `list_conversations`); requests the distinct set of recently-used workspace folders. Empty request payload. Replies with `recent_workspaces_list`. |
| `recent_workspaces_list` | binary → phone | no | Reply to `recent_workspaces`, correlated by `in_reply_to`; carries the distinct workspace paths, most-recent-first, each with its most-recent `last_used_at`. |
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
| **`api_retry`** | binary → phone | no | **New in v2** (interactive, capability-gated). claude's live API-error retry state (#1074). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`compacting`** | binary → phone | no | **New in v2** (interactive, capability-gated). claude's auto-compaction banner (#1074). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`unrecognized_message`** | binary → phone | no | **New in v2** (interactive, capability-gated). The stream parser met claude output it has no mapping for — a gap in our mapping, not a claude sub-state. See [Interactive events](#interactive-events-v2-capability-gated). |
| **`background_task_started`** | binary → phone | no | **New in v2** (interactive, capability-gated). claude started work that outlives the turn that spawned it (#1394). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`background_task_updated`** | binary → phone | no | **New in v2** (interactive, capability-gated). A background task claude already started changed (#1394). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`background_task_roster`** | binary → phone | no | **New in v2** (interactive, capability-gated). Snapshot of the background tasks claude is tracking; an empty list says nothing is alive (#1394). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`thinking_progress`** | binary → phone | no | **New in v2** (interactive, capability-gated). claude is actively reasoning, and roughly how much — its only mid-turn proof of life on the stream-json surface (#1386). Rate-bounded; absence proves nothing. See [Interactive events](#interactive-events-v2-capability-gated). |
| **`rate_limited`** | binary → phone | no | **New in v2** (interactive, capability-gated). claude's usage-limit window is in a state other than the one measured-benign one — why, which limit, and when claude says it lifts (#1405). Shape declared by #1405, emitted since #1410. See [Interactive events](#interactive-events-v2-capability-gated). |
| **`model_announced`** | binary → phone | no | **New in v2** (interactive, capability-gated). The model claude named for the current turn on its `system/init` line (#1616). **Not** the per-session override the three `model` fields elsewhere in this document carry. Shape declared by #1616, emitted since #1638. See [Interactive events](#interactive-events-v2-capability-gated). |
| **`request_snapshot`** | phone → binary | no | **New in v2.** On-demand screen-snapshot request. See [Screen snapshot](#screen-snapshot-v2). |
| **`screen_snapshot`** | binary → phone | no | **New in v2.** See [Screen snapshot](#screen-snapshot-v2). |
| **`resync`** | binary → phone | no | **New in v2.** Mid-turn-reconnect resync marker — the advertised `last_event_id` aged out of the ring; phone must full-reload (#647). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`session_transition`** | binary → phone | no | **New in v2** (interactive, capability-gated). Session-boundary marker for `pyrycode-mobile#336` (#656). See [Interactive events](#interactive-events-v2-capability-gated). |
| **`model_list`** | binary → phone | no | **New in v2** (interactive, capability-gated). The menu of models claude will accept for a conversation, from its `initialize` control reply — identifiers, labels, per-model effort levels and auto-mode support (#1704). **Not** the per-turn announcement [`model_announced`](#model_announced) carries. Shape declared by #1704, fixtures and section by #1705, the mapping onto this wire shape by #1848 and the **producer** by #1849, proven end to end by #1845 — it is emitted on the live interactive turn lane, once per child spawn to whatever clients are connected at that instant, and **best-effort rather than guaranteed**: several loss points mean a client may see none at all, so never block a model menu on it. See [Interactive events](#interactive-events-v2-capability-gated). |
| **`slash_command_list`** | binary → phone | no | **New in v2** (interactive, capability-gated). The slash commands this session's working directory will accept, from the `commands` array of the same `initialize` control reply — names, argument hints, descriptions and aliases (#1727). The sibling [`model_list`](#model_list) inventories *identities* from that reply; this one inventories *verbs*. Consumers are pyrycode-desktop#681 (Actions-menu grey-out) and pyrycode-desktop#694 (slash-command type-ahead). Type declared by #1726, shape by #1727, fixtures and section by #1718, the mapping onto this wire shape by #2001 with its frame-level byte bound by #2002, and the **producer** by #2003, proven end to end by #2008 — it is emitted on the live interactive turn lane, once per child spawn to whatever clients are connected at that instant, and **best-effort rather than guaranteed**. It also has a **connect-time snapshot** (#2006/#2007, proven by #2009), so a client that missed the live frame is brought current on its next connect. See [Interactive events](#interactive-events-v2-capability-gated). |
| **`question_shown`** | binary → phone | no | **New in v2** (interactive, capability-gated). One whole batch of the clarifying questions claude's `AskUserQuestion` tool asks — questions, their options and a per-question multi-select flag, in claude's own order (#1962). The consumer is pyrycode-desktop#849. Type declared by #1962, shape by #1963, fixtures and section by #1964; the parse is #1965 and the producer #1973, which emits it the moment claude asks. **Not** a [`modal_shown`](#modal_shown): that frame is one prompt with flat options, this one is a batch with **two nesting levels** and a **two-verb** inbound half ([`question_answer`](#question_answer) / [`question_refused`](#question_refused), #1983) rather than the modal pair's single answer. See [Question](#question-v2). |
| **`question_dismissed`** | binary → phone | no | **New in v2** (interactive, capability-gated). The frame that retires a [`question_shown`](#question_shown) batch, so a client clears the panel instead of rendering an ask that is already dead (#1974). Carries no claude-authored string. Its own type and **not** a [`modal_dismissed`](#modal_dismissed), which identifies what it clears by `modal_id` and routes to the modal panel. #1973 emits it on every no-answer terminal path, #1990 on a refusal and #1991 on an answer — all three terminal outcomes. See [Question](#question-v2). |
| **`question_answer`** | phone → binary | no | **New in v2** (interactive, capability-gated). The operator's selections for a [`question_shown`](#question_shown) batch (#1983) — the batch nonce, a client-minted `answer_token`, and an ordered `answers` array whose entries name their question **by index** and carry opaque client-authored values. The [`modal_answer`](#modal_answer) of this family, forked because one batch has many questions and a question may be multi-select where a modal has exactly one `option_id`. **Intercepted and decoded** as of #1984, which hands it to a resolver seam; #1991 landed the daemon-side resolution against the parked batch, and #1986 is what wires the seam to it. See [Question](#question-v2). |
| **`question_refused`** | phone → binary | no | **New in v2** (interactive, capability-gated). The operator declined to choose, so the batch resolves with no selection (#1983). Carries the batch nonce and the `answer_token` and **nothing else**. Its own type rather than an [`question_answer`](#question_answer) with an empty array, exactly as [`modal_cancel`](#modal_cancel) is its own type beside [`modal_answer`](#modal_answer). **Intercepted and decoded** as of #1984, like its sibling; **#1990** landed what it resolves to — the batch is consumed, claude's blocked call is denied with a fixed instruction to wait for the operator's message, and one [`question_dismissed`](#question_dismissed) goes out — though nothing reaches that path from the wire until the per-device gate (#1986) is wired. See [Question](#question-v2). |
| **`modal_shown`** | binary → phone | no | **New in v2** (interactive, capability-gated). Modal surfaced to the phone (#597 Phase 3). See [Modal](#modal-v2). |
| **`modal_answer`** | phone → binary | no | **New in v2.** Inbound control — phone answers a modal. See [Modal](#modal-v2). |
| **`modal_cancel`** | phone → binary | no | **New in v2.** Inbound control — phone cancels a modal. See [Modal](#modal-v2). |
| **`modal_dismissed`** | binary → phone | no | **New in v2.** Modal resolution notice. See [Modal](#modal-v2). |
| **`queue_state`** | binary → phone | no | **New in v2** (interactive, capability-gated). Queued-message backlog snapshot (#597 Phase 3). See [Queue](#queue-v2). |
| **`dequeue_message`** | phone → binary | no | **New in v2.** Inbound control — phone cancels a queued message. See [Queue](#queue-v2). |
| **`interrupt`** | phone → binary | no | **New in v2.** Inbound control — phone interrupts the running turn (remote Esc). Interactive-capability-gated; exempt from the permission gate. See [Interrupt](#interrupt-v2). |
| **`new_session`** | phone → binary | no | **New in v2.** Inbound control — phone starts a fresh session (remote `/clear`). Interactive-capability-gated; exempt from the permission gate. See [New session](#new-session-v2). |
| **`debug_bundle_chunk`** | binary → phone | no | **New in v2.** Outbound — one ordered, cap-respecting slice of a streamed debug bundle (#812). See [Debug bundle](#debug-bundle-v2). |
| **`debug_bundle_done`** | binary → phone | no | **New in v2.** Outbound — completion marker after the last `debug_bundle_chunk`, carrying the exact chunk count (#812). See [Debug bundle](#debug-bundle-v2). |
| **`request_debug_bundle`** | phone → binary | no | **New in v2.** Inbound control (bare, no payload) — a paired client requests the current session's debug bundle; the daemon streams it back as `debug_bundle_chunk*` + `debug_bundle_done` (#813). See [Debug bundle](#debug-bundle-v2). |
| **`set_session_settings`** | phone → binary | no | **New in v2.** Inbound control — a paired client changes one session's per-session model / effort / permission mode / YOLO. Interactive-capability-gated (enforced by the handler #845). See [Session settings](#session-settings-v2). |
| **`session_settings_updated`** | binary → phone | no | **New in v2.** Outbound reply confirming a `set_session_settings`, correlated by `in_reply_to` (#845). See [Session settings](#session-settings-v2). |
| **`request_session_settings`** | phone → binary | no | **New in v2.** Inbound control — a paired client asks for the run configuration of the conversation it names in `conversation_id`. A request that names no conversation names no session, and is answered with the all-zero reply. Interactive-capability-gated. See [Session settings](#session-settings-v2). |
| **`session_settings`** | binary → phone | no | **New in v2.** Outbound reply carrying the current run configuration, correlated by `in_reply_to` (#491). See [Session settings](#session-settings-v2). |
| **`session_error`** | binary → phone | no | **New in v2.** Unsolicited, conversation-scoped terminal session-error frame — the daemon gave up delivering a conversation's queued backlog (`session.blocked`; #1007). Carries `conversation_id`, `code`, `message`; NOT `in_reply_to`-correlated. See [Error codes](#error-codes). |
| **`attachment_chunk`** | either | no | **New in v2.** One slice of one attachment's bytes, carrying the whole transfer's metadata on every chunk (#1752). The table's first genuinely bidirectional **payload** frame — `ack`/`error`/`rekey_request` above are also `either` but carry no application payload: upload rides this one phone → binary and retrieval rides it binary → phone, and declaring exactly one type is what stops the two legs drifting. Nothing emits, accepts or enforces it yet. See [Attachments](#attachments). |
| **`attachment_stored`** | binary → phone | no | **New in v2.** The upload leg's **success reply** — the transfer completed, its claims were checked, and the bytes are stored under the `attachment_id` the client chose (#1895). Correlated by `in_reply_to`, which names the chunk **whose arrival completed the transfer** rather than the last one sent. Carries that one id and nothing else: no host path, no directory component, no stored filename. Nothing emits it yet (#1897). See [Attachments](#attachments). |
| **`request_attachment`** | phone → binary | no | **New in v2.** Inbound control — a paired client asks for a stored attachment, naming the conversation and the attachment and nothing else (#2052). The `conversation_id` is a **lookup key validated against the daemon's registry**, not a value trusted as sent, and naming a conversation is **not authorization**. Correlation rides `in_reply_to`, so the payload carries **no request-id key**; the answer is a stream of [`attachment_chunk`](#attachment_chunk) frames, or `attachment.not_found` / `attachment.stream_aborted`. **Nothing answers it yet** — the handler is #2054 and the stream #2053. See [Attachments](#attachments). |

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

`last_seen_ts` (optional) appears in the block above because it is **still
accepted vocabulary** — the daemon decodes it and a decoder that rejected it
would be wrong. It is **inert**: no daemon code reads the decoded value, so it
drives no backfill, no replay and no other behaviour, and a `hello` carrying it
is answered exactly as one omitting it. The field is deliberately neither
removed nor deprecated here (#2090); whether real history should exist at all,
and what would source it, is #2091's decision. A client wanting the tail it
missed sends `last_event_id` instead. The same key appears in the § Worked
example wire trace below for the same reason — those are bytes genuinely on the
wire, not a capability.

`last_event_id` (optional, omitempty — absent, not `null`, when the phone has no
position; key-absent keeps the v1 hello byte-identical) is the durable
`event_id` (see [Interactive events](#interactive-events-v2-capability-gated)),
unique daemon-wide since #2022, the phone last saw on the interactive stream —
one scalar over one ordering, not a per-conversation cursor. The daemon still
resolves the conversation to replay from its own current cursor, so an advertised
id belonging to a conversation the daemon has since rotated away from is simply
an id the resolved conversation never had, and classifies as a gap.
On mid-turn reconnect the phone
advertises it so the daemon can replay the missed tail from the event ring or
emit a `resync` marker (#647). It is **untrusted input** — the daemon
range/shape-validates it (`*uint64` decode) and bounds replay by the ring; the
phone can never address a conversation other than the daemon's current one.

Binary validates the token after decrypting the handshake message. If invalid, the binary sends an AEAD-sealed `error` envelope (code `auth.invalid_token`) inside a `noise_msg` and asks the relay to close with `4401`. The `noise_resp` may or may not have already been sent at this point — implementations should send it first (so the AEAD channel exists), then immediately send the auth error.

### Capability negotiation (v2)

> **Superseded as a requirement — 2026-06-22 (ADR 025 amendment).** This tool is self-hosted with a single operator who controls both ends and ships the app and daemon together, so there is no old-app install base. The daemon assumes every phone is `interactive`; the non-interactive coarse `message` fan-out has been removed (#699). The `capabilities` field below stays as a harmless additive field, but it carries no backward-compatibility obligation and no future work should treat old-phone interop as a requirement.

**Still live: build detection — 2026-09-02 (#2020).** The amendment above retires *old-app interop* as a requirement, and that stands: no future work should keep a code path alive for a phone build that no longer exists. It does not retire the field's other use. Daemon and app ship together but are *installed* separately, so a machine can be running a daemon that predates a wire feature the client already implements, and `pyry --version` reports a commit sha that carries no ordering. A capability string is what closes that gap: the client advertises the string, and a daemon too old to know it drops it in the intersection below — so its absence from the `hello_ack` is a positive, checkable stale-daemon signal rather than an inference. Prefer one string per user-facing wire feature over an orderable version number; a string survives cherry-picks and backports, and it says what is supported rather than when it was built. A cross-repo wire feature is expected to add one, and a feature that ships without one leaves its clients unable to tell which daemon they are talking to.

Both `hello` and `hello_ack` carry an optional `capabilities: []string` field (omitempty — absent, not `null`, when empty, so a v1 phone's `hello` stays byte-identical). The phone advertises the features it understands in its `hello`; the daemon echoes the features *it* supports in `hello_ack`.

| Field | Type | On | Meaning |
|---|---|---|---|
| `capabilities` | `[]string` (omitempty) | `hello` (phone → binary) | Features the phone understands, e.g. `["interactive"]`. |
| `capabilities` | `[]string` (omitempty) | `hello_ack` (binary → phone) | Features the daemon supports and has agreed to. |

Defined capability strings:

| Value | Meaning |
|---|---|
| `interactive` | The phone can render the structured interactive event stream below. |
| `question` | The client understands the clarifying-question batch types — it can render a `question_shown` batch, answer it with `question_answer` / `question_refused`, and handle `question_dismissed`. **Detection only: this string grants no access.** The batch fan-out and the connect-time question reconcile gate on `interactive` alone, so a client advertising `question` by itself negotiates as non-interactive and receives none of them (#2020). |

The daemon MUST echo only what it itself supports — the agreed set is the **intersection** of the phone's advertised set with the daemon's own, never a blind mirror of the phone's claims. A phone that does not advertise `interactive` (or whose `interactive` is not echoed back) simply does not receive the structured interactive event stream; there is no separate non-interactive `message` fan-out on v2 (it was removed in #699). The intersection logic — the daemon-side trust decision computing advertised ∩ supported, echoing it in `hello_ack`, and recording the negotiated `interactive` flag per connection — is implemented in #626 (`internal/relay` v2 session manager; `negotiateCapabilities` + the capability-aware `ActiveConns` enumeration). The capability-gated fan-out that routes the interactive event stream only to granted connections shipped in #632/#633.

### Interactive events (v2, capability-gated)

These fifteen envelope types form the structured live-session stream. They are sent **binary → phone only**, and **only** to a phone whose `interactive` capability was echoed in `hello_ack`; an old phone never receives them. They are the wire representation of the daemon's neutral internal turn-event model. All *payload* fields are always present (no omitempty) so boundary values like `seq: 0` and `is_error: false` are explicit on the wire.

**Replay cursor (`event_id`, #649).** Every frame in this stream additionally carries an envelope-level `event_id` (the optional `Envelope` field above) — the durable id the daemon assigns to each structured event as it records it in a bounded per-conversation event ring (ADR 025 § Backpressure / replay). It is **not** the same as the envelope's `id`: `id` is a per-connection counter that resets each reconnect, whereas `event_id` is connection-independent, identical across all interactive connections for a given logical event, and strictly increasing in the daemon's emit order. **Retention is per conversation; the id space is not** (#2022). One ring-wide counter assigns every id, so an id is never shared by two conversations and a conversation's own ids ascend without being contiguous — ids belonging to other conversations sit between them, and the first id a conversation is assigned is normally far above 1. **A client may therefore keep one scalar cursor**: no event the daemon emits later, in any conversation, can carry an id at or below one already seen. A client that keys its cursor per conversation is equally correct and unaffected. A phone records the latest `event_id` it has seen and, on mid-turn reconnect, advertises it as `last_event_id` in its `hello`; the daemon then replays the missed tail from the ring (or emits a `resync` marker if it fell off the bounded window). The **producer** side (the daemon stamping `event_id` outbound) landed in #649; the reconnect **consumer** (`hello.last_event_id`, ring replay, and the `resync` marker) landed in #647 — see [Reconnect replay & resync](#reconnect-replay--resync-consumer-647) below.

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
| `input` | object (string → string) | The tool input's own top-level fields, each value the input's value verbatim. Always present, never `null`. |

`input` is what a client shows to say what a call *acts on* — an `Edit`'s `file_path` beside its replaced text rather than buried inside it — where `input_summary` is the whole input compacted onto one line and cut short. Both are sent; `input_summary` keeps its exact meaning and value.

Each value is the input's own value: a JSON string arrives decoded (a path is a path, an embedded newline is a newline), any other JSON type arrives as its compact JSON form (so `null`, `true`, `[1,2]` and `{"x":1}` are those literal strings). Nothing is normalised — no path is rewritten to a workspace-relative form.

**Bounds.** Each value is capped at **4000 runes** (runes, not bytes), and the map as a whole at **8500 runes** of keys plus values across at most **16 fields**. A value the daemon shortened ends in `…`; a value that legitimately ends in `…` is indistinguishable from a cut one, which is an accepted cost of the marker. A field may be **absent** because the total bound dropped it — dropped fields are not listed anywhere, and `input_summary` remains the whole-input fallback. Key order on the wire is alphabetical, a marshalling artefact rather than the input's own order, so display order is the client's choice.

**Empty cases.** An input that is absent, an empty object, or not a JSON object at all all yield `"input":{}` — never `null`, and never an error. The frame does not distinguish the three.

**Values are display strings, not capabilities.** They are model-authored text the daemon neither resolved nor validated: a `file_path` is not canonicalised and may be relative or traversing, and a `Bash` `command` value is a literal shell command line. Render them as inert text. Never open one as a path on your own filesystem, execute or re-shell one, or feed one to an HTML sink, an attribute, or a URL.

#### `tool_result`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation this turn belongs to. |
| `turn_id` | string | Identifies the turn the result belongs to. |
| `tool_use_id` | string | Matches the `tool_use` this result completes. |
| `is_error` | bool | Whether the tool invocation failed. |
| `result_summary` | string | Human-readable précis of the result (not the raw output). |

**Bounds.** `result_summary` is capped at **10000 runes** (runes, not bytes). A result the daemon shortened ends in `…`; a result that legitimately ends in `…` is indistinguishable from a cut one, which is an accepted cost of the marker. **`is_error` does not change the bound** — an error result is truncated at exactly the same 10000 runes a success result is, so a client must not expect a failing tool's output to arrive whole.

The number is fixed by the envelope, not by taste. `encoding/json` escapes HTML by default, so `<`, `>`, `&` and every control byte without a short escape each cost **six bytes** on the wire, while a multi-byte rune is emitted raw at 4 bytes or fewer — six bytes per rune is therefore the ceiling for any rune count. 10000 × 6 = 60000 B of escaped content, measured at a worst case of **61363 B** against the 65519-byte application-envelope cap (§ Application-envelope size cap) with hostile identity fields. A frame over that cap is **lost, not truncated**, and `tool_result` is never-droppable control class (§ Error codes, `4413`), so the operator would see an empty row rather than a shortened one. Do not read `unrecognized.raw`'s "escaping is mild in practice" argument onto this field: that one rests on the payload already being JSON text with pre-escaped control characters, where a tool result is raw command output or file contents, and reading a TSX or HTML file is an ordinary `<`-dense result.

**`result_summary` is a display string, not a capability.** It is model-authored text the daemon neither resolved nor validated — `Bash` output is a command's stdout verbatim, a file read is a file's contents. Render it as inert text. Never feed it to an HTML sink, an attribute, or a URL, and never execute or re-shell any of it. This is the hazard § `tool_use` states for its input values, and here the `<`-dense case is the ordinary one rather than the contrived one.

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

#### `api_retry`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation currently in claude's API-error retry state. |
| `active` | bool | Rising edge (`true`, claude entered retry) or falling edge (`false`, claude recovered — clear the indicator). |
| `current` | int | Parsed `attempt N/M` counter's `N`. `0` when claude's on-screen counter did not parse (a legitimate "retrying, count unknown" state, not an error). |
| `total` | int | Parsed `attempt N/M` counter's `M`. `0` alongside `current: 0` for the same unparsed case. |

`api_retry` (#1074) is a **PTY-derived status peer of `stall`** — like `stall` it
is a coarse conversation-level signal, not turn-scoped (no `turn_id`), and
emitting it never opens, closes, or alters a turn. Unlike `stall` it is **not**
onset-only: it has an explicit falling edge (`active: false`) so a remote head
can dismiss the indicator once claude recovers. tui-driver re-fires the rising
edge whenever the parsed count climbs (e.g. `3/10` → `4/10`); each re-fire is
just another `active: true` frame with an updated counter — there is no dedup
and no per-tick flood (tui-driver only re-fires on an actual count change). The
falling edge carries the last-known counter (copied verbatim from the rising
edge's last value) so the final render stays coherent; a phone ignores
`current`/`total` when `active` is `false`. No field ever carries raw banner or
screen text — `current`/`total` are the only screen-derived data, and both are
bounded, pre-sanitized ints.

#### `compacting`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation currently in claude's auto-compaction pass. |
| `active` | bool | Rising edge (`true`, compaction started) or falling edge (`false`, compaction finished — clear the indicator). |

`compacting` (#1074) is the same PTY-derived status-peer shape as `api_retry`,
but **banner-only**: tui-driver streams no compaction progress payload, so
beyond the conversation id and the edge bool there is nothing to carry. Without
this event a remote head sees a frozen screen for the tens of seconds
compaction can take; the `active: true` frame is the daemon's only signal that
something is happening, not stalled.

#### `unrecognized_message`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation whose stream carried the unmappable output. |
| `site` | string | Where the parser met it. Closed set: `line_type`, `assistant_block`, `user_block`, `undecodable`. |
| `message_type` | string | The offending message or content-block `type`. **Empty** when `site` is `undecodable` — nothing decoded, so no type was ever read. |
| `raw` | string | The offending JSON, verbatim, truncated by the daemon to a fixed byte cap. A **string**, not nested JSON: a truncated blob is no longer valid JSON. |
| `truncated` | bool | Whether the daemon cut `raw` to fit the cap. |

`unrecognized_message` is **not a claude sub-state** — the one way it differs
from its `stall` / `api_retry` / `compacting` neighbours above, which all report
what claude is doing. This reports a gap in **our own** mapping.

The stream-json line parser recognises three top-level message types from claude
(`assistant`, `user`, `result`) and a fixed set of content blocks. Everything
else used to be dropped with a debug log, and because the production daemon runs
at info level, that drop left no trace anywhere and no client was told. Fine for
the types we ignore on purpose. Not fine for a type we have never seen: a claude
version that moved something meaningful into a new message type would show
nothing, everywhere, with nothing saying why.

So the parser has **two tiers**, and the split is the whole design:

- **Known and deliberately ignored, from this frame's perspective** — `system`
  and `rate_limit_event` never produce `unrecognized_message`. `system` is
  claude's catch-all namespace and its highest-rate emitter (`system/init`
  fires once per turn, `system/thinking_tokens` roughly ten times), so treating
  every subtype as surfacing-worthy by default would put a row on every turn
  and make the frame worthless noise. As of #1380–#1385 and #1600 the daemon
  parser maps **five** `system` subtypes internally (`task_started`,
  `task_updated`, `background_tasks_changed`, `thinking_tokens`, `init`) to
  daemon-owned `turnevent` types — see
  [streamsup-package.md](knowledge/features/streamsup-package.md). **All five
  reach this wire** under their own daemon-owned names, and all five are
  documented below: `background_task_started`, `background_task_updated` and
  `background_task_roster` (#1394), `thinking_tokens` as
  `thinking_progress` (#1386), and `init` as
  [`model_announced`](#model_announced) — the last of the five to get there, its
  shape declared ahead of its producer (#1616 declares, #1638 emits). As of
  #1404 the parser also maps `rate_limit_event` — a **top-level line type, not
  a `system` subtype**, so the count of five above is unaffected — to
  `turnevent.RateLimited`, which reaches this wire as
  [`rate_limited`](#rate_limited) (#1405), documented below. Every other `system`
  subtype is still silently dropped exactly as before, and none of this changes what
  surfaces as `unrecognized_message` — a subtype the parser doesn't recognize
  at all still falls through to the silent-drop tier, not this frame.
- **Genuinely unrecognized** — everything else. Surfaces as this frame.

The ignored list is **measured, not guessed**: claude was driven directly on the
bare stream-json surface, three turns each on two models, one turn per run
calling tools. The same measurement settled whether claude echoes the delivered
prompt back as a `user` message holding a `text` block, as it does on the
agent-run surface. **It does not**, so a `user`/`text` block appearing in future
is a real change and surfaces.

It carries **no `turn_id`** — unlike every turn-stream event above. That is not
an oversight: the daemon could not parse the message well enough to attribute a
turn to it honestly. It also **opens and closes no turn**. Opening one would
wedge the conversation, because no turn end follows a message we could not
understand.

`raw` is capped at **16 KiB at construction**, so an oversized payload never
enters the event stream or any log. That is roughly a quarter of the 65519-byte
application-envelope cap above — note this is the v2 cap, not v1's superseded
1 MiB — leaving room for the envelope's other fields plus the JSON escaping the
blob picks up. Escaping is mild in practice because the payload is already JSON
text, so its control characters arrive pre-escaped as printable pairs.

**Repeats are never coalesced.** A repeat is a real repeat, and how often this
fires is exactly the number that tells an operator to go fix something.

**Consumer safety.** `raw` and `message_type` are the least trustworthy strings
on this wire: unbounded, model-adjacent output the daemon could not interpret. A
client MUST render them as plain text only — never through an HTML sink, an
attribute, or a URL.

**Regression alarm.** The daemon's real-claude test suite asserts that a normal
turn produces **zero** of these frames. It goes red the day claude adds a
message type, in the pre-ship gate rather than in front of a user. Red there
does not mean something broke; it means the measured ignore list needs
re-deciding.

#### `background_task_started`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation whose turn spawned the task. |
| `task_id` | string | claude's opaque handle for the task. The join key every later `background_task_updated` and roster row carries. |
| `tool_call_id` | string | The tool call that spawned the task — claude's `tool_use_id`, under the name `tool_use` and `tool_result` already use for it. |
| `description` | string | The task's label. For `task_type: local_bash` this is the **literal command line**. |
| `task_type` | string | claude's kind for the task (`local_bash` is the only observed value). An open string, not a closed set — one observation does not earn one. |
| `truncated_fields` | array of string \| null | Names of the fields the daemon cut to fit their caps, using the field names in this table: `task_id`, `tool_call_id`, `description`, `task_type`. `null` when nothing was cut. |

Like every frame in this section it is **binary → phone only**, reaches only a
phone whose `interactive` capability was echoed in `hello_ack`, and carries an
envelope-level `event_id` for replay.

`background_task_started` (#1394) announces work that **outlives the turn that
spawned it**. That is the whole reason the frame exists: a turn can report
`turn_end` with `stop_reason: end_turn`, and `turn_state` can go `idle`, while a
command claude started is provably still running. Before this frame nothing
reaching a phone separated that from a genuine finish.

It carries **no `turn_id`**, and it **opens and closes no turn** — a background
task's lifecycle is orthogonal to its turn's, so attributing it to one would be a
claim the daemon cannot honestly make. A client should render it as its own
thread of activity, not as part of the turn it appeared in.

Because `tool_call_id` is the same identifier `tool_use` and `tool_result` carry,
a client joins all three with no vocabulary lookup: the `tool_use` that launched
the command, its `tool_result`, and the background task it left running.

`truncated_fields` is load-bearing, not decoration. A client that ignores it
presents claude's cut text as complete. Every string here was bounded by the
daemon **at construction**, so an oversized value never reaches this wire; the
report is how a client knows which of them lost characters.

**SECURITY.** `description` is, for the `local_bash` task type, the literal
command line claude ran. It is safe to **render as inert text** and never to
execute, re-shell, or feed to an HTML sink, an attribute, or a URL. The daemon
bounds it but does not sanitize it — it stays untrusted, model-influenced text
all the way to the client.

#### `background_task_updated`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation whose turn spawned the task. |
| `task_id` | string | The join key back to the `background_task_started` that opened the task. |
| `patch` | string | What **changed** about the task: claude's patch object carried whole and unparsed, as a **string**. One key has been observed (`is_backgrounded`). Empty when claude sent none. |
| `truncated_fields` | array of string \| null | Names of the fields the daemon cut: `task_id`, `patch`. `null` when nothing was cut. |

Like its sibling it is **binary → phone only**, `interactive`-gated, and carries
an envelope-level `event_id`.

`background_task_updated` (#1394) is the peer of `background_task_started`: that
frame opens the task, this one reports what happened to it afterwards. Join the
two on `task_id`. Like its sibling it carries no `turn_id` and opens and closes
no turn.

`patch` is carried **whole and unparsed** — the daemon enumerates no keys inside
it, because a mapping that listed the keys it knew would silently discard every
key claude ships next. A client should treat it the same way: read the keys it
understands, and pass the rest through or ignore it.

**SECURITY.** `patch` is a **string, not nested JSON, and a client MUST NOT
assume it parses.** The daemon truncates it at construction to fit a cap, and a
truncated object is no longer valid JSON — typing it as raw JSON on this wire
would be a lie that broke decoding. Feed it to a JSON parser only behind an error
branch that falls back to rendering it as text.

The render-never-execute rule is stated here rather than delegated to the sibling
section: a patch's structured shape makes it the more tempting thing to feed
somewhere that runs it, and a patch key may carry command text exactly as
`description` does. Render it as inert text; never execute, re-shell, or feed it
to an HTML sink, an attribute, or a URL.

One upstream limitation worth knowing: the daemon also scrubs invalid UTF-8 from
`patch` by **deleting** the offending bytes, and `truncated_fields` reports the
cap cut **only**. So `patch` can differ from claude's bytes without appearing in
`truncated_fields`. It is a display blob whose JSON validity was never guaranteed
anyway, so a client cannot act differently either way.

#### `background_task_roster`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation the roster belongs to. |
| `tasks` | array of object | The tasks claude is tracking at this moment, in claude's own order. **Always present, never `null`** — see below. |
| `dropped_tasks` | int | How many entries claude sent beyond the daemon's entry cap that this frame does **not** carry. `0` when nothing was dropped. |

Each element of `tasks`:

| Field | Type | Meaning |
|---|---|---|
| `task_id` | string | The join key back to the `background_task_started` that opened the task. |
| `task_type` | string | claude's kind for the task (`local_bash` is the only observed value). |
| `description` | string | The task's label, under a **tighter** cap than `background_task_started.description` — here it is one label in a list whose length claude chooses, and the full-length copy already crossed the wire on the `background_task_started` this row joins back to. |
| `truncated_fields` | array of string \| null | Names of **this row's** cut fields: `task_id`, `task_type`, `description`. `null` when nothing was cut. Each row reports its own; there is no hoisted or flattened list. |

Like its two siblings it is **binary → phone only** and `interactive`-gated. A
frame emitted on the **live turn lane** carries an envelope-level `event_id`; a
frame produced by the **connect-time reconcile** below deliberately carries
**none** — see that note for what follows from it.

`background_task_roster` (#1394) is the aggregate peer of the two scalar frames:
they report what happened to **one** task, this reports what is alive. Like them
it carries no `turn_id` and opens and closes no turn.

**Reconcile on (re)connect.** On any (re)connection the daemon unicasts one
`background_task_roster` for **every** conversation whose bound session has
reported a roster (#2077 landed the per-session retention, #2078 the reconcile,
#2079 the daemon-side enumeration) — this is match-and-replace by stable id (see
[§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)), keyed on
`conversation_id`: a re-sent roster for a known conversation **replaces** that
view in place rather than appending, and because the frame is snapshot-shaped
full state rather than a delta, re-applying it on every connect is safe by
construction. **#2080** proves a client that connects *after* the session
reported receives the retained roster, with its rows and `dropped_tasks` intact,
driving no turn and sending nothing. Correlate by `conversation_id` and **never
by position** in the burst: the daemon walks its conversation registry in
insertion order, that order is not a contract, and every envelope in the burst
carries the same non-load-bearing envelope id.

**The empty case is the part you cannot infer from the neighbours, and it is the
reverse of [`slash_command_list`](#slash_command_list)'s rule in this same
document.** Two different silences must not be collapsed:

- A conversation whose session reported a roster that is **empty** is reconciled
  as an **explicit empty snapshot** — a frame arrives carrying `"tasks": []`.
  Nothing filters it out, because an empty roster is a *positive* statement that
  nothing is alive (point 2 above), and that statement is exactly what a
  reattaching client needs. `slash_command_list` filters its zero-length list;
  this frame deliberately does not.
- A conversation whose session has **never reported one** is simply **absent**
  from the reconcile. No frame arrives for it, and none ever will until claude
  changes that session's roster.

So **absence means "nothing has been reported", not "nothing is alive"**, and the
two are not interchangeable: render no background-task state at all for a
conversation you received no frame for, and render an explicitly empty panel for
one you received an empty frame for. A client that treats absence as an empty
roster claims a fact the daemon did not state; one that waits for a frame before
showing anything renders a spinner forever on a conversation that has nothing
alive and never will.

**The reconciled frame carries no `event_id`, and that is deliberate.** It is a
current-state snapshot rather than a turn event, so it is kept out of the #647
replay ring entirely: it is not replayed to a reconnecting client, it does not
advance any `last_event_id` cursor, and a client's cursor-based dedup is inert
for it. Do not key a reconciled roster on an event id, and do not treat the
absence of one as a malformed frame — the live-lane frame described above still
carries one, and the two paths are told apart by exactly this.

Three things a client will otherwise get wrong:

**1. It is a snapshot, not a delta.** The frame's name is the daemon's, not
claude's — claude's own line is named for the *trigger* (something changed),
while the payload is a complete picture of one moment. Treat each frame as
replacing your view of what is running, not as amending it.

**2. `tasks` is always present and never `null`, and an empty `[]` is a positive
statement that nothing is alive.** That is the signal #1240's symptom needs, so
the daemon forwards an empty roster rather than filtering it — an empty array
here is the reassurance that the turn really is finished, not an absence of
information. A client decoding into a non-optional array type never has to branch
on `null`.

**3. `dropped_tasks` is the roster's only truncation report.** There is
deliberately no top-level `truncated_fields` on this frame, so a client grepping
for that name finds nothing and would silently believe a capped roster is the
whole roster. The roster's true size is `len(tasks) + dropped_tasks`. A count is
carried rather than a name because a name-only report loses *how many* were lost;
per-row text cuts are a property of one row and ride that row's own
`truncated_fields`.

**There is no terminal, finish, or completion event in this family, and that is
deliberate.** A task's disappearance from a later roster is the *available*
finish signal, but that transition has never been observed, and the daemon does
not report a finish it cannot detect. Diffing successive snapshots is a
legitimate thing for a **client** to do on its own terms — it is simply not an
inference the daemon makes on your behalf, so anything a client shows as
"finished" is the client's own conclusion.

Ordering within a turn is claude's, not the daemon's: a roster can arrive before
or after the `background_task_started` for a task it lists. Join on `task_id`
rather than assuming an order.

**SECURITY.** Each row's `description` carries the same literal command line as
`background_task_started.description`, under a tighter cap. The
render-never-execute rule is repeated here per row rather than delegated because
a **list** of command lines is a more tempting shape to feed somewhere structured
than a single one. Render every row as inert text; never execute, re-shell, or
feed it to an HTML sink, an attribute, or a URL.

#### `thinking_progress`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation whose turn is reasoning. |
| `estimated_tokens` | int | claude's estimate of the tokens it has spent thinking as of the emitting line. Cumulative within **one inference request**, not within a turn — see below. |
| `estimated_tokens_delta` | int | claude's per-line increment, exactly as it appeared on the line that produced this frame. |

Like every frame in this section it is **binary → phone only**, reaches only a
phone whose `interactive` capability was echoed in `hello_ack`, and carries an
envelope-level `event_id` for replay.

`thinking_progress` (#1386) exists because it is claude's **only** mid-turn proof
of life on the stream-json surface: during a long assistant turn nothing else
crosses the wire, so a client showing "thinking" for three minutes cannot
otherwise separate a slow answer from a wedged session. The frame's name is the
daemon's, not claude's — claude's line is `system/thinking_tokens`, and the
daemon translates it so that a claude rename lands in one place rather than
breaking every client at once. Do not key a client on claude's vocabulary.

It carries **no reasoning text**. The frame says only *that* claude is reasoning
and roughly how much; the content of claude's thinking is never forwarded on this
wire (ADR 025). A client that tries to render it as text has nothing to render.

Five things a client will otherwise get wrong. Each is measured, not inferred,
and the numbers below come from one committed capture of a single turn.

**1. It is conversation-scoped, not turn-scoped.** There is no `turn_id`, and
receiving one **neither opens nor closes a turn** — it drives no turn lifecycle
at all. It is a *reading*, not a state transition: the turn's thinking state is
already reported by `turn_state: thinking`. The daemon emits these during an
inference request that may not have produced any assistant content yet, so a turn
opened on one would have no guaranteed end.

**2. The frames are rate-bounded and do not enumerate claude's lines.** The
daemon emits at most one frame per **64 tokens of accumulated delta**, so strictly
fewer frames cross this wire than claude emits lines: on the committed capture,
**33 lines became 8 frames**. Do not treat a frame as "claude produced one line",
and do not count frames to count anything of claude's.

**3. `estimated_tokens` is not monotonic.** It restarts near zero at **every
inference-request boundary**, which happens repeatedly inside a single turn — the
committed capture's one turn contains **four restarts**: the reading ran 5→184,
then 4→167, then 3→126, then 1→197. Two readings must therefore **never be
subtracted expecting a non-negative result**. Treat it as a progress reading, not
as a turn total, and not as a counter you can difference.

**4. The `estimated_tokens_delta` values a client receives do not sum to the
turn's total.** The rate bound in point 2 drops most of claude's lines, and their
increments go with them: on the committed capture the turn's **674 tokens of
delta arrived as 243 across 8 frames**. **No field reports the residue** — there
is no "dropped tokens" count on this frame, deliberately, because it is a rate
reading rather than an accumulator input. Summing the deltas undercounts by an
amount the wire does not disclose.

**5. Absence proves nothing, for two distinct reasons — both of them apply.**

- **The surface may not emit them at all.** Only the stream-json parser produces
  this event. On the PTY surface claude emits **zero** `thinking_tokens` lines
  (measured, and enforced by the daemon's pre-ship gate), so a phone attached to a
  PTY-driven session will **never** receive one, no matter how long claude thinks.
- **Even on the emitting surface, a gap between two frames may mean nothing is
  wrong.** The rate bound means a quiet window may only be one in which the
  accumulated delta has not yet crossed 64 tokens. The gap is not evidence that
  thinking stopped.

A client MUST NOT infer a stall from either. **This frame is proof of life when
present and says nothing when absent** — it is not a universal liveness
indicator, and a "thinking stalled" inference built on the gap between two frames
is invalid on both surfaces. The daemon's separate stall signal is
[`stall`](#stall), which has its own producer and is untouched by this frame in
both directions.

#### `rate_limited`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation whose turn observed the usage-limit report. |
| `status` | string | **Why** the frame fired: claude's own status for the usage-limit window, verbatim. An **open string with a mostly unmeasured value set**, and **it does not imply the turn was blocked** — see below. |
| `limit_type` | string | **Which** limit the report concerns (`five_hour` and `seven_day` are the observed values). An open string, not a closed set — two observed values do not earn one. |
| `resets_at` | int | When claude says the limit lifts, as **unix seconds**. `0` means claude did not report it — **not** the epoch. Unvalidated in both directions; see below. |
| `truncated_fields` | array of string \| null | Names of the fields the daemon cut to fit its cap, using the field names in this table: `status`, `limit_type`. `null` when nothing was cut. |

Like every frame in this section it is **binary → phone only**, reaches only a
phone whose `interactive` capability was echoed in `hello_ack`, and carries an
envelope-level `event_id` for replay.

**Emitted since #1410.** The shape was declared by #1405 so a client could be
written against it; #1410 wired the producer — `internal/turnbridge`'s `MapEvent`
maps `turnevent.RateLimited` outbound and `cmd/pyry`'s interactive turn emitter
pushes the frame, so live traffic carries it.

`rate_limited` exists because a turn that stops making progress because of a
usage limit otherwise says nothing about why. claude reports the usage-limit
window **once per run whatever its state**, so a 1:1 translation would put a row
on every healthy turn and make the frame worthless noise. The daemon gates it
instead: the one **measured-benign** status is silent, and **any other non-empty
status emits**. That direction is deliberate — an unrecognised status surfaces
and a human looks, rather than a real limit vanishing.

**`status` is an open string, and what a client may do with it is bounded.** Its
value set beyond the benign one is **almost entirely unmeasured**: exactly one
non-benign value is on record, and **no capture of a limit actually in force
exists** on any claude version. It is claude's raw string, carried precisely so
the set gets measured the first time a real limit fires. Render it as an **opaque
label**. A client **MUST NOT branch security-relevant behaviour on it**, and must
not treat it as a closed set — doing so is a bug waiting for claude's next
release.

**A frame is not proof that anything was blocked, and this is the realistic
client bug.** The one measured non-benign value is `allowed_warning`, seen
2026-08-22 on claude 2.1.239 against `limit_type` `seven_day`: the account was
inside its weekly warning band and **every turn still ran normally**. So the
frame's plain reading is "claude said something about the usage window worth
repeating", not "you are rate limited", and a client that renders it as the
latter will tell the user they are blocked while their turns keep working.
Warning ahead of the wall is the frame's most useful moment — it is the only one
where the user can still act — so the fix is wording that does not overclaim, not
suppression. Both the daemon and the live drain
(`internal/e2e/realclaude`'s `warnRateLimitStatus`) treat this value as expected
rather than as a fault.

**`resets_at` is claude's number, not the daemon's clock**, and it is unvalidated
in **both** directions. A consumer must not assume it lies in the future, and must
not assume it lies in a sane range at all: negative, zero and year-40000 values
are all representable and none is rejected, because rejecting one would be a
validation rule with no captured negative case behind it. **Formatting it as a
date without a range check is the realistic bug.**

It is **conversation-scoped**: there is no `turn_id`, and receiving one **neither
opens nor closes a turn**. A usage-limit window is orthogonal to whichever turn
happened to observe it, so attributing it to one would be a claim the daemon
cannot honestly make.

`truncated_fields` is load-bearing, not decoration. A client that ignores it
presents claude's cut text as complete. Both strings were bounded by the daemon
**at construction**, so an oversized value never reaches this wire; the report is
how a client knows which of them lost characters.

**SECURITY.** `status` and `limit_type` are claude-authored strings that crossed
the subprocess trust boundary. They are safe to **render as inert text** and never
to feed to an HTML sink, an attribute, or a URL. The daemon bounds them but does
not sanitize them — they stay untrusted, model-influenced text all the way to the
client. This frame is a **report, never a control input**: nothing in the daemon
keys a behaviour on it (no backoff, throttle, retry, turn suspension or reconnect
delay), and a client should hold the same line. That is what keeps a wrong — or
hostile — status value costing at most one misleading row.

#### `model_announced`

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation whose turn carried the announcement. |
| `model` | string | The model identifier **claude announced for this turn**, verbatim. **Never the empty string.** Not the per-session override — see below. |
| `truncated` | bool | Whether the daemon cut `model` to fit its cap. |

Like every frame in this section it is **binary → phone only**, reaches only a
phone whose `interactive` capability was echoed in `hello_ack`, and carries an
envelope-level `event_id` for replay.

**Emitted since #1638.** The shape was declared by #1616 so a client could be
written against it; #1638 wired the producer — `internal/turnbridge`'s `MapEvent`
maps `turnevent.ModelAnnounced` outbound and `cmd/pyry`'s interactive turn emitter
pushes the frame, so live traffic carries it. This is the same sequencing
`rate_limited` used (#1405 declared, #1410 emitted).

`model_announced` exists because the daemon knows what it **asked for** and only
claude knows what it **got**. The per-session override is often unset, in which
case the daemon publishes an empty string while claude has named a concrete model
on every turn.

**This is not the `model` field you already know.** Three payloads in this
document carry a wire field named `model` —
[`screen_snapshot.model`](#screen_snapshot),
[`session_settings.model`](#session_settings) and
[`set_session_settings.model`](#set_session_settings) — and **all three mean the
per-session override**, where `""` means "inherited daemon default, no override".
The third is simply a client's write of the value the first two report. **This one
means what claude announced for the turn**, which is a different thing, and in the
ordinary case the two **disagree**: the override is `""` while claude has named a
concrete model. A client that merges them into one value shows the wrong one.

**A lookup miss is ordinary, not an error.** claude echoes an identifier **at
least as specific as the one it was given**: it dates a bare family alias
(`haiku` → `claude-haiku-4-5-20251001`) and passes through anything already fully
formed (`claude-haiku-4-5`; `claude-sonnet-5` for a machine default). So the value
is **not reliably dated**, and it **need not appear in any published
[model list](#model_list)** — `claude-haiku-4-5` does not. That frame is the
**menu** of what claude will accept for the conversation; this one is the
**per-turn announcement** of what it actually ran, so the two are joined on
`display_name` rather than assumed equal. Treat a miss against any list as normal,
render the string as given, and do not repair it: the daemon does not,
deliberately.

**Once per turn, not once per session.** claude emits `system/init` on every turn,
so one session produces several of these and they need not agree — a `/model` turn
emits its own `init` and that one still reports the **old** model. A client that
**latches the first** announcement shows a stale value; one that renders the
**latest** has no problem to solve. The daemon does not dedup.

It is **conversation-scoped**: there is no `turn_id`, and receiving one **neither
opens nor closes a turn**. A per-turn announcement is not a turn boundary.

`truncated` is load-bearing, not decoration. A client that ignores it presents
claude's cut text as complete. The value was bounded by the daemon **at
construction**, so an oversized identifier never reaches this wire; the flag is how
a client knows this one lost characters.

**SECURITY.** `model` is a claude-authored string that crossed the subprocess trust
boundary. It is safe to **render as inert text** and never to feed to an HTML sink,
an attribute, or a URL. The daemon **bounds it but does not sanitize it** — nothing
on this path strips control characters or terminal escape sequences — so it stays
untrusted, model-influenced text all the way to the client, and **the render
boundary that owes the sanitization is the client's, not the daemon's**. Nor is the
value charset-checked: only a **phone-supplied override** is (a deliberately
different rule, since applying it here would reject identifiers claude legitimately
announces), and a `--model` flag or a config default never is. This frame is a
**report, never a control input**: nothing in the daemon keys a behaviour on it, and
a client should hold the same line — in particular it must not be used to select
code paths, endpoints, or pricing without validating it against a list the client
itself owns.

#### `session_transition`

Direction **binary → phone** (outbound v2 session-boundary marker; not in `v1TypeSet` — an old phone never receives it). This is a **session-boundary marker, distinct from the fifteen turn-stream events above** — it does not belong to the structured live-session stream and carries no `event_id`. It is the wire form of `pyrycode-mobile#336`'s `ThreadItem.SessionBoundary`: the daemon's session rotated, so the phone renders a boundary marker instead of inferring one from message fields that do not exist.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation this session-boundary marker belongs to (routing key, matching every other interactive event). |
| `previous_session_id` | string | The session id that ended. Always present (a transition sits between two sessions). |
| `new_session_id` | string | The session id that began. |
| `reason` | string | Why the session rotated. Closed set: `clear`, `idle_evict`, `workspace_change`. |
| `occurred_at` | string | When the transition occurred (RFC3339Nano). |
| `workspace_cwd` | string \| null | The new workspace directory — non-null **iff** `reason == workspace_change`; literal `null` for `clear` and `idle_evict`. |

**Invariant:** `workspace_cwd` is non-null **if and only if** `reason` is `workspace_change`. The field is always present on the wire (literal `null`, never absent) so the invariant is decodable directly.

The **producer** is **#657**. Until a server-side workspace-change source exists, the producer emits only `clear` and `idle_evict` — yet the type admits `workspace_change` so the mobile decoder stays exhaustive and the invariant above is expressible. This ticket (#656) defines the wire shape only.

#### `model_list`

Direction **binary → phone** (outbound v2 model inventory; not in `v1TypeSet` — an old phone never receives it). This is a **conversation-scoped menu, distinct from the fifteen turn-stream events above** — it is not a `turnevent` variant and is not one of those events, though it rides the same live interactive lane they do and carries an `event_id` like them, which is what the delivery window below turns on. It carries what claude returns from a `control_request` with subtype `initialize` on the control channel the daemon already writes to, so it arrives on a `control_response` rather than on the turn stream; receiving one **neither opens nor closes a turn**. It is a **snapshot** of what claude will accept for the conversation, not a delta. That same `initialize` reply publishes a second per-conversation menu, [`slash_command_list`](#slash_command_list): this frame inventories the **identities** claude will run as, that one inventories the **verbs** the working directory will accept.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation the menu belongs to (routing key, matching every other interactive event). |
| `models` | array of object | The models claude will accept for this conversation, in claude's own order. **Always present, never `null`** — an empty `[]` is a positive statement that claude offered nothing, so a client decoding into a non-optional array type never has to branch. |
| `dropped_models` | int | How many entries the producer cut that this frame does **not** carry; `0` when nothing was dropped. **The count is real**: the producer bounds the entry count and reports what it cut — see below. |

Each element of `models`:

| Field | Type | Meaning |
|---|---|---|
| `resolved_model` | string | What `value` resolves to **right now**: the concrete identifier. Published *before* the first turn, so a client can show which model a family currently means instead of inferring it from an announcement after the fact. |
| `value` | string | The argument you pass to select this model. **Not a dated identifier** — below. Sendable back on [`set_session_settings`](#set_session_settings), bracketed variant rows included (#1838); a published value is still re-validated there rather than trusted, and `effort_levels` is the field that still gets refused — below. |
| `display_name` | string | claude's human label for the row, and the intended join key against [`model_announced`](#model_announced). |
| `effort_levels` | array of string | The reasoning-effort levels this model supports. **Always present, never `null`**; `[]` means the model exposes no effort control, which is the one position this wire states for both claude's *absent* and *empty* list. |
| `supports_auto_mode` | bool | Whether claude accepts `auto` permission mode for this model. claude refuses the request per model, so a client greys the option out when this is `false` (pyrycode-desktop#682). Absent in claude's reply decodes to `false`, which is the correct reading. |
| `truncated_fields` | array of string \| null | Names of **this row's** cut fields, in producer order: `resolved_model`, `value`, `display_name`, `effort_levels`. `null` when nothing was cut. Each row reports its own; there is no hoisted or flattened list. `effort_levels` is the one name reporting on a list rather than a scalar — it covers an element cut to fit, the list itself shortened, or both, appearing at most once per row in every case. |

**`dropped_models` means what its name promises, and `len(models) + dropped_models` is the menu's true size.** The producer bounds the entry count at **ten** (#1812) and reports here how many entries it cut, so a client can render "10 of 40" rather than presenting a shortened menu as complete. The producer cuts only the overflow, so a non-zero `dropped_models` always arrives beside exactly ten entries; a shorter list is always a complete one. The count reaches the wire intact: the decode records the overflow where the cut happens, the mapping onto this frame carries the number **verbatim rather than recomputing it from `len(models)`** (#1848), and the producer passes it through (#1849). Two properties bound how a client may use it. The list is truncated **from the tail**, so the entries you receive are claude's first ten in claude's own order. And ten is a **daemon-side producer cap, not a wire constant** — it may change without any change to this contract, so a client must never hardcode it, treat a list of exactly ten as a signal, or derive the cap from anything but the number in this field.

**This frame is emitted.** The shape was declared by **#1704**, the committed fixtures and this section by **#1705**, the mapping onto this wire shape by **#1848**, and the **producer** by **#1849**; **#1845** proves it reaches a connected client end to end. This is the same declare-then-emit sequencing `rate_limited` used (#1405 declared, #1410 emitted) and `model_announced` used (#1616 declared, #1638 emitted), and this frame has now completed it.

**The delivery window is narrower than "emitted" suggests, and a client must build for the narrow one.** What the daemon performs on a schedule is an **ask, not a delivery**: it runs **one `initialize` exchange per claude child spawn** and emits whatever comes back on the **live interactive turn lane**, arriving on a `control_response` as above, to whatever interactive connections exist **at that instant**. Nothing bounds the rate on either side. Delivery is **best-effort rather than guaranteed**, and **the losses are not limited to load** — three sit between that emit and a client:

- **No conversation is routed yet.** The daemon spawns its first child eagerly at startup, before any conversation has been routed, and an event the daemon has no conversation to address is dropped. That child's menu is lost **unconditionally**, so a client attaching to an already-running daemon has not merely missed the frame — it was never sent one.
- **The session is busy.** The frame is classed droppable at the daemon's fan-in, so a loaded session can refuse it. **Missing it is not an error**: nothing is retried, and no error frame says a menu was lost.
- **The emitting child is not the active conversation's bound session.** After a session rotation the fresh child's events no longer match the lane's session binding, so **a rotation does not deliver a fresh menu** — even though it does start a new child, and therefore a new ask.

What reliably arrives is the case #1845 proves end to end: a child spawned **while a conversation is already routed and a client is connected** — a respawn after the child died, for instance.

**A client that missed the frame has no snapshot to ask for.** There is **no connect-time snapshot today**: a client **attaching** — a fresh connection, or one whose menu was lost at any of the three points above — has **no way to ask for one** and will not be sent one, which is why `model_list` is deliberately absent from [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)' Mode B list. One narrower case does recover, and only one: because the frame carries an `event_id` like every other event on this lane, a **reconnecting** client whose `hello` advertises a `last_event_id` predating it is replayed it along with everything else it missed — Mode A, a cursor backfill rather than a snapshot. That path needs both a cursor from a previous connection and a frame that was actually emitted, so it recovers nothing for a first attach and nothing for a menu that was never sent. The consequence is the actionable part: **never block a model menu on this frame**, and render a usable UI without one rather than waiting for a frame that may never arrive.

Four things a client will otherwise get wrong:

**1. `value` is not a dated identifier.** It is what you *pass*: an alias (`sonnet`), a bracketed variant (`opus[1m]`), or `default`. A client cannot derive a family by splitting it on `-`, and must not present it as a version. `resolved_model` is the dated one — what the alias resolves to right now.

**2. A lookup against this list can miss, and that is ordinary rather than an error.** claude announces an identifier **at least as specific** as the one it was given, so a [`model_announced`](#model_announced) value need not appear here at all (`claude-haiku-4-5` does not). `display_name` is the intended join, **not** `resolved_model` — the announcement names a concrete dated identifier while a client's rows are alias families. That frame is the per-turn announcement of what claude ran; this one is the menu of what it will accept.

**3. A published value is not automatically sendable back, and `effort_levels` is where that still bites.** The only inbound path that accepts a model is [`set_session_settings`](#set_session_settings), whose rule — widened at #1838 for exactly the rows below — accepts `""` or, within a 64-byte bound, a value whose first byte is alphanumeric, whose remaining bytes are in `[A-Za-z0-9._-]`, and which may carry **one trailing bracket group**: `[`…`]` as the value's final element, non-empty, and drawn from that same closed byte class. Every `value` claude has been measured to publish now passes, the bracketed variant rows (`opus[1m]`, `claude-fable-5[1m]`) included. The group is bounded that way rather than by adding two bytes to the charset because the rule is #845's argv-injection defense: a leading, unbalanced, empty or nested bracket is still rejected, as is a second group or any suffix after one, and so is a leading dash, a shell metachar, whitespace, a control byte and any byte at or above `0x80`. That closure matters **twice**, because an accepted value reaches two sinks — the claude argv, where `--model` and the value are separate `execve` elements no shell parses, and the **live child's turn text**, since a model change on a running session is written as `/model <value>` on one line and an accepted value must therefore stay a single whitespace-free token. **`effort_levels` carries the identical hazard in the same direction and is deliberately not widened**: the inbound effort enum is **closed** and accepts all five levels claude returns today, so a level claude adds later would be published here and refused inbound. So a client must still read a published value as a candidate rather than a guarantee, and must handle the refusal.

**4. `truncated_fields` is load-bearing, not decoration.** A client that ignores it presents claude's cut text — or a cut list — as complete, and would offer back a `value` it was never told was truncated.

**SECURITY.** `resolved_model`, `value`, `display_name` and **every string in `effort_levels`** are claude-authored strings that crossed the subprocess trust boundary. They are safe to **render as inert text** and must never be fed to an HTML sink, an attribute, or a URL. The daemon **bounds them but does not sanitize them** — nothing on this path strips control characters or terminal escape sequences — so they stay untrusted, model-influenced text all the way to the client, and **the render boundary that owes the sanitization is the client's, not the daemon's**. The frame is a **report, never a control input**, with one amendment the sibling frames do not need: `value` is the first field in this family a client is meant to send **back**, and publishing it does not make it trusted. It is still claude's text arriving on an inbound path, and the daemon re-validates it (property 3 above) rather than trusting that it came from a list the daemon itself published.

#### `slash_command_list`

Direction **binary → phone** (outbound v2 slash-command inventory; not in `v1TypeSet` — an old phone never receives it). This is a **conversation-scoped menu, distinct from the fifteen turn-stream events above** — it is not a `turnevent` variant and is not one of those events, though the live-lane emission rides the same interactive lane they do and carries an `event_id` like them, which is what the delivery window below turns on; the connect-time copy deliberately carries none. It carries the `commands` array claude returns from a `control_request` with subtype `initialize`, the same reply [`model_list`](#model_list) is drawn from, so it arrives on a `control_response` rather than on the turn stream; receiving one **neither opens nor closes a turn**. It is a **snapshot** of the commands this session *in this working directory* will accept, not a delta. `model_list` inventories the **identities** claude will run as; this one inventories the **verbs**.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | Conversation the menu belongs to (routing key, matching every other interactive event). |
| `commands` | array of object | The slash commands claude will accept for this conversation, in claude's own order. **Always present, never `null`** — an empty `[]` is a positive statement that claude offered nothing, so a client decoding into a non-optional array type never has to branch. |
| `dropped_commands` | int | How many entries the producer cut that this frame does **not** carry; `0` when nothing was dropped. **The count is real**: two producer cuts feed it and it is carried rather than recomputed — see below. |

Each element of `commands`:

| Field | Type | Meaning |
|---|---|---|
| `name` | string | The command's name, without the leading `/`. **Not an identifier** — one name in the measured capture is `__remote-workflow`, so no charset assumption belongs in a client. |
| `argument_hint` | string | What the command expects after it (`[name]`, `key=value`, `<model>`). **Always present**, and **empty on 33 of the capture's 51 entries** — an empty hint is the ordinary case rather than missing data. |
| `description` | string | The command's one-line summary, except when it is not one line — see property 3. |
| `aliases` | array of string | Other names that invoke this command. **Always present, never `null`**; `[]` is the one position this wire states for both claude's *absent* and *empty* list, so a client never branches on absent-vs-empty to match an alias. |
| `truncated_fields` | array of string \| null | Names of **this row's** cut fields (`name`, `argument_hint`, `description`, `aliases` — the wire names), `null` when nothing was cut. Each row reports its own; there is no hoisted or flattened list. |

**`dropped_commands` means what its name promises, and `len(commands) + dropped_commands` is the menu's true size.** That inverts what this paragraph said until #2010, and it is the correction most likely to change what a client does: the old text forbade the one arithmetic that now works. **Two cuts feed the number, and the second adds to the first rather than replacing it.** The decode bounds the entry count at **128** (#1826) and records how many entries it cut; the mapping onto this wire shape then applies a **frame-level bound over the serialised `commands` array** (#2002) and adds whatever *it* drops on top, carrying the total **verbatim rather than recomputing it from `len(commands)`**. Both cut **from the tail**, so the entries you receive are claude's first N in claude's own order.

**The sibling's shortcut does not transfer, and that is what to carry away from the comparison.** [`model_list`](#model_list) has one cutter that removes only the overflow, so a non-zero `dropped_models` there always arrives beside exactly ten entries and a shorter list is always a complete one. Here the byte bound can fire before the entry cap is reached, so **a non-zero `dropped_commands` arrives beside any number of entries** and a short list is not evidence of a complete one. No "N of M" illustration is published for this frame because no single pair is representative — read the field, never the length. Neither number is a wire constant either: both are **daemon-side producer caps** that may change without any change to this contract, so a client must never hardcode one, treat a list of exactly 128 as a signal, or derive a cap from anything but the number in this field.

**This frame is emitted.** The wire type was declared by **#1726**, the shape by **#1727**, the committed fixtures and this section by **#1718**, the mapping onto this wire shape by **#2001** with its frame-level byte bound by **#2002**, and the **producer** by **#2003**; **#2008** proves it reaches a connected client end to end. This is the same declare-then-emit sequencing `rate_limited` used (#1405 declared, #1410 emitted), `model_announced` used (#1616 declared, #1638 emitted) and `model_list` used (#1704 declared, #1848 mapped and #1849 emitted), and this frame has now completed it.

**The delivery window is narrower than "emitted" suggests, and a client must build for the narrow one.** What the daemon performs on a schedule is an **ask, not a delivery**: it runs **one `initialize` exchange per claude child spawn** and emits whatever comes back on the **live interactive turn lane**, arriving on a `control_response` as above, to whatever interactive connections exist **at that instant**. Nothing bounds the rate on either side. **The two delivery paths differ on `event_id`, and the difference is client-visible.** A live-lane frame is appended to the per-conversation event ring before the per-conn fan-out, so it carries an `event_id`, and a **reconnecting** client whose `hello` advertises an earlier `last_event_id` is replayed it along with everything else it missed — Mode A, a cursor backfill. The connect-time snapshot below deliberately carries **no** `event_id`: it never enters the ring, is never replayed, and never advances a client's cursor.

Delivery on the live lane is **best-effort rather than guaranteed**, and **the losses are not limited to load** — three sit between that emit and a client:

- **The reporting child is not the active conversation's bound session.** The daemon drops every event tagged with any other session before it reaches the emitter, and two ordinary situations land there. The **bootstrap child** is spawned at startup before any conversation is routed and owns no conversation record, so nothing ever binds it and **its inventory is lost unconditionally** — a client attaching to an already-running daemon has not merely missed the frame, it was never sent one. And a **session rotation** re-keys the session in place while the reporting child's own tag is fixed when its runner is built, so the two are no longer the same id and **a rotation delivers no fresh inventory** — even though it does start a new child, and therefore a new ask.
- **The session is busy.** The frame is classed droppable at the daemon's fan-in, so a loaded session can refuse it. **Missing it is not an error**: nothing is retried, and no error frame says an inventory was lost.
- **No interactive connection exists at that instant.** The emit fans out to the connections that are open and interactive-granted right then; a client connecting a moment later receives nothing from this path.

**What is *not* a loss point, stated because the daemon's own comments count it as one:** the reconnect-replay dedup ([`hello.last_event_id`](#reconnect-replay--resync-consumer-647)) drops only an envelope a connection **already received in its replay**. The replay watermark is clamped to the newest event retained at handshake for the conversation that replay was taken from, and since #2022 `event_id` is unique daemon-wide, so **any** frame emitted afterwards carries a higher id — whatever conversation it belongs to — and the guard cannot cost a client a frame it had not already been given. **The conversation scope that qualified this claim is gone**, and it was not a formality: while ids were counted per conversation, each counter starting at 1, and the watermark was a single per-connection value, a `/clear` or a conversation switch after an in-range reconnect muted the new conversation's live stream up to the watermark for the life of that connection — a real defect, reported and fixed in #2022, not a caveat. And the guard is inert for the reconciled frame for a second reason, since that one carries no `event_id` at all.

**Reconcile on (re)connect.** On any (re)connection the daemon unicasts one `slash_command_list` for **every** conversation whose bound session currently holds an inventory (#2006 landed the reconcile, #2007 wired the daemon-side enumeration to it, over the per-conversation resolver #2005) — this is match-and-replace by stable id (see [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)), keyed on `conversation_id`: a re-sent `slash_command_list` for a known conversation **replaces** that menu in place rather than appending, and because the frame is snapshot-shaped full state rather than a delta, re-applying it on every connect is safe by construction. **#2009** proves a client that connects *after* the child reported receives the retained list, driving no turn and sending nothing. Correlate by `conversation_id` and **never by position** in the burst: the daemon walks its conversation registry in insertion order, that order is not a contract, and every envelope in the burst carries the same non-load-bearing envelope id.

**What the snapshot does not cover, and none of it announces itself.** A conversation whose bound session is **gone**, or which is **unbound**, or whose session has **reported nothing**, is simply **absent** from the reconcile. An **empty inventory is never retained in the first place** — the producer emits no event at all for a zero-length list (#1877) — so "no frame for this conversation" and "claude offered no commands" are the same observation from a client's side. A connection that handshakes while **nothing at all is retained receives no frame**, and the handshake still completes normally. And the reconcile is gated on the negotiated **`interactive` capability**, like the rest of the structured stream. One property cuts the other way and is worth stating: the enumeration is **unfiltered**, so an **archived** conversation whose bound session still holds an inventory does contribute, and the client decides what to show. The consequence is the actionable part: **never block a command menu on the live frame** — render a usable UI without one and let the next connect fill it in.

Four things a client will otherwise get wrong:

**1. A name-only match misses aliases, and the cheaper source cannot repair it.** Nine of the capture's 51 entries carry aliases — **11 aliases in all**: `code-review` → `review`; `doctor` → `checkup`; `loop` → `proactive`; `schedule` → `routines`; `clear` → `reset`, `new`; `config` → `settings`; `rename` → `name`; `usage` → `cost`, `stats`; `list-agents` → `peers`. The desktop Actions menu's own **reset** entry is *that alias* — `reset` is not a command name — so a client matching against `name` alone greys out a command that works. **The same capture carries a names-only twin, and a client will meet it first:** the `system`/`init` stdout line's `slash_commands` holds the identical 51 names in the identical order as bare strings, with no descriptions, no argument hints and **not one of the 11 aliases**. That is the measured reason a client cannot be told to just read the init line. (`terminal_slash_commands` on that same line — 2 entries, `doctor` and `color` — is a third array again and is neither of these.) One blind spot follows from `aliases` collapsing absent and empty into the same `[]`: a `truncated_fields` naming `aliases` is the **only** thing distinguishing "cut to nothing" from "none", and a client must read it as *unknown*, never as *no aliases*.

**2. The count is workspace- and version-dependent, so a client may not cache one.** 51 entries against claude 2.1.239 in this repository; an earlier hand count against 2.1.220 in a different working directory reported **74**. A client may not cache a count across working directories, assume a floor, or treat a small list as an error. That variation is the feature's whole point — the list is per session and per working directory precisely because a repository defines its own commands.

**3. The strings are workspace-authored: bounded, but not sanitized.** A command defined in a repository was written by whoever wrote that repository, which is a **lower-trust origin** than the claude-authored strings [`model_list`](#model_list) warns about. `claude-api`'s description in the capture carries **embedded newlines**, and `0x0a` is the **only** sub-`0x20` byte anywhere across the 51 entries' four string fields — so newlines are *the* control character on this path rather than one class among several, and a type-ahead row that assumes one line per description will not get one. 14 of the 51 descriptions contain non-ASCII, and one name is `__remote-workflow`, outside any obvious identifier charset. Render them as **inert text**; feeding them to an HTML sink, an attribute or a URL is not safe.

**4. A cut is reportable and must be read, and the size has to be quoted with its unit.** The capture's 51 entries serialise to **14,277 bytes of compact UTF-8**; the same array with its non-ASCII `\u`-escaped is **14,371**, and the two disagree precisely because 14 of the 51 descriptions carry non-ASCII. The wire is neither number exactly: Go's encoder escapes `<`, `>`, `&` and U+2028/U+2029 (of which the capture contains none) and passes every other non-ASCII rune through as raw UTF-8, so a real frame is the UTF-8 form plus 6 bytes per escaped `<`/`>`/`&`. The committed fixture shows both behaviours side by side — `model`'s `<model>` hint against `claude-api`'s raw em dashes — which is why the unit is named rather than implied. The longest single description is **1,145 bytes / 1,135 runes**, the longest argument hint 121 B / 115 runes, the longest name 24 B; the mean description is 207 B, the median 69, and **10 of 51 exceed 256** in both units. A per-field bound will therefore cut real rows, and a client that ignores `truncated_fields` presents cut text as complete.

**SECURITY.** `name`, `argument_hint`, `description` and **every string in `aliases`** are **workspace-authored** strings that crossed the subprocess trust boundary. That strengthens `model_list`'s claude-authored warning rather than restating it: the author is whoever wrote the repository, not claude. They are safe to **render as inert text** and must never be fed to an HTML sink, an attribute, or a URL. The daemon **bounds them but does not sanitize them** — nothing on this path strips control characters or terminal escape sequences, and property 3 names the one that actually occurs — so they stay untrusted text all the way to the client, and **the render boundary that owes the sanitization is the client's, not the daemon's**. The frame is a **report, never a control input**, with one amendment: a client is meant to send a `name` **back**, as the text of an ordinary message, because sending the slash command *is* the feature. Publishing a name does not make it trusted. It arrives inbound as ordinary message text, on a path that does not treat it as a command vocabulary and does not consult this list, and no field here reaches a child process as an argv element. This frame declares **no inbound verb**.

#### Reconnect replay & resync (consumer, #647)

The inbound half of the [replay cursor](#interactive-events-v2-capability-gated). On mid-turn reconnect a phone advertises the latest `event_id` it saw as `hello.last_event_id` (see [`hello`](#hello-v2-specific-note)). The daemon resolves the conversation to replay from its **own** current cursor — never from anything the phone sends — and queries the bounded per-conversation event ring (ADR 025 § Backpressure / replay):

- **In-ring tail.** Events with `id > last_event_id` are replayed on that connection, in ascending order, **before** the live stream resumes. Each replay frame carries the same `event_id` it had originally (so the phone advances its cursor), and is AEAD-sealed under the freshly-handshaked session keys.
- **Caught-up.** `last_event_id` exactly at the newest retained event → no replay; the live stream resumes normally.
- **Aged out → `resync`.** `last_event_id` predates the oldest retained event (the position fell off the bounded window) → the daemon emits a single `resync` marker instead of a partial, gap-ful replay, so the phone is never left with a silent gap. The phone responds with a full reload of the named conversation.
- **Beyond the id space → `resync`.** `last_event_id` *past* the newest id the **resolved conversation** was ever assigned names an id this daemon never issued to it → the same single `resync` marker (#1494). This is what a daemon restart looks like from the phone's side: the ring is purely in-memory, so the ring-wide counter restarts at 1 while the phone still holds a high cursor. It is also what a **rotation** looks like when the phone's cursor came from the conversation the daemon has since left — that id is above the new conversation's own high-water mark, so it lands here rather than silently deduping. Treating either as caught-up would produce exactly the silent gap the bullet above forbids — the phone dedups durably on `event_id` (see below), so it would drop every live event until the ids climbed back past its cursor. Since #2022 that "climb back" can only happen across a restart, never within one daemon lifetime: ids are unique daemon-wide and never reused.
- **Absent.** A phone that sends no `last_event_id` receives no replay — just the normal live stream.

A phone that advertises no `last_event_id`, and any v1 phone, are unaffected (key absent → byte-identical hello).

`last_event_id` is **untrusted remote input**: it is range/shape-validated (`*uint64` decode rejects non-integers), the replay is bounded by what the ring retains (`MaxEventsPerConversation`), and it is scoped to the daemon-resolved conversation — a phone can never address another conversation's events. The phone SHOULD additionally de-duplicate replayed events by `event_id` (defence in depth; the two layers are independent).

##### `resync`

Direction **binary → phone** (outbound v2 control marker; not in `v1TypeSet` — an old phone never receives it).

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation the phone must full-reload. The daemon's own resolved id; never attacker-derived. |

The marker carries no `event_id` (it is not a structured event). On receipt the phone discards its `last_event_id` cursor for that conversation and performs a full reload (today via a fresh subscription).

> **Implementation status (2026-06-17).** The reconnect-replay daemon code shipped via PR #651 (merged 2026-06-08) with a code-review **MUST FIX** outstanding — the *caught-up* path set the per-connection dedup watermark from the untrusted `last_event_id`, which could silently suppress the live stream after a `/clear`-rotated reconnect or a hostile-large `last_event_id`. That defect is **resolved**: #663 clamps the caught-up watermark to the conversation's newest retained id (`min(afterID, NewestID(convID))`), so the wire contract above is now the shipped daemon guarantee. Fix record: [`docs/knowledge/codebase/663.md`](knowledge/codebase/663.md); defect history: [`docs/knowledge/codebase/647.md`](knowledge/codebase/647.md#️-known-issue--unresolved-code-review-must-fix-do-not-merge-as-is). Superseded in part by #1494: an out-of-range / hostile `last_event_id` no longer reaches the caught-up branch at all — it classifies as a gap and earns a `resync` — so the clamp now covers only the window between the daemon's own newest-id read and its classification.

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
| `model` | string | Bootstrap session's per-session model override; **empty string = inherited daemon default** (no override). This is the **override**, not what claude announced for the turn — see [`model_announced`](#model_announced). |
| `effort` | string | Bootstrap session's per-session reasoning-effort override; **empty string = inherited daemon default** (no override). |
| `yolo` | bool | Bypass-permissions (`--dangerously-skip-permissions`) on/off; **`false` = permissions enforced** (the fail-safe default). |
| `used_tokens` | int | Bootstrap session's context-window tokens consumed by the latest turn (#857). Was shipped in the binary but missing from this table. |
| `window_tokens` | int | Context-window size (#857). **`0` = the usage reader is unwired**, not an empty window. Was shipped in the binary but missing from this table. |

> **Prefer [`session_settings`](#session_settings) for the four run-configuration fields above.** They are carried here as a side-load (#848, #857) and predate the dedicated read route. This reply is refused with `server.binary_offline` whenever there is no terminal to photograph — which is always, on the stream-json interactive runner — so a client that sources its run configuration from here gets nothing on the runner in production. That was `pyrycode-desktop#491` and `#1214`. The copies stay for the shipped mobile client; new clients should not read them.

### Modal (v2)

When the supervised claude surfaces a modal — a permission prompt, a plan-approval, a tool-confirmation — the daemon describes it to the phone, the phone answers, and the daemon drives that answer back into claude (#597 Phase 3). The lifecycle is `modal_shown` → `modal_answer` / `modal_cancel` → `modal_dismissed`. `modal_shown` rides the `interactive` capability (#607): **viewing a modal is ungated**, but **answering is gated separately, per-device, default OFF** in the [Security model](#security-model) (#702) — that gate is not a wire capability. All fields are always present (no omitempty). This section is wire vocabulary only; the minting, dedup, validation, and fan-out runtime is the producer's (#703, with #706/#702 building ownership/gating).

**Reconcile on (re)connect.** On any (re)connection the daemon re-asserts the still-outstanding modal by **unicasting `modal_shown` with the original `modal_id`** (#877) — this is match-and-replace by stable id (see [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)): a re-sent `modal_shown` for a known `modal_id` updates the modal in place and **never double-shows**. Answer semantics are **unchanged under re-delivery**: the one-time `modal_id` nonce plus the client-minted `answer_token` idempotency key keep a prompt answerable **exactly once**, re-sent or not, and **deny-on-timeout is armed once, at raise time, and is never re-armed by a re-send**. Those invariants already live in the **Security & validation contract** note below and in ADR 025 § Security model (1–4); this note only ties them to reconcile-on-connect, it does not restate them.

#### `modal_shown`

Direction **binary → phone** (outbound v2 modal-surfaced event; not in `v1TypeSet` — an old phone never receives it).

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation whose bound session raised this modal (#1065). **Outbound routing/scoping key only** — the daemon asserts it from its own active-conversation cursor so a client filters display by conversation and a permission prompt for one conversation is never rendered by a client viewing another; it is not part of inbound answer resolution — see the security note below. |
| `modal_id` | string | One-time opaque nonce minted per surfaced modal. The **sole inbound correlation key** — see the security note below. |
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

**Security & validation contract.** `modal_id` is a one-time, **opaque, unguessable** nonce minted per surfaced modal, and it is the **sole inbound correlation key**: `modal_answer`/`modal_cancel` carry no `conversation_id` (unchanged by #1065 — see below). The daemon resolves `modal_id` against its **own** outstanding-modal state — it never trusts a phone-asserted conversation, and maps `option_id` against its own recorded option list. The daemon **rejects** an inbound `modal_answer`/`modal_cancel` whose `modal_id` is not the current outstanding one (#703/#706, first-answer-wins), so a stale or guessed `modal_id` resolves nothing. `answer_token` is a **client-minted idempotency key** (its uniqueness and stability matter; secrecy does not) that lets the daemon collapse a replayed or reordered `modal_answer` to a no-op. It is **not** the authorization: authorization is `modal_id` validity (#706) plus the per-device answer gate (#702, default OFF); `answer_token` only deduplicates among already-authorized answers. The minting + dedup + validation runtime is **#703/#706**; the answer gate is **#702**.

**`modal_shown`'s `conversation_id` is outbound-only (#1065) and does not loosen the above.** It is a daemon-asserted **scoping** stamp — sourced from the same active-conversation read that picked the rendered screen, so the two cannot diverge — that lets a client with several open conversations filter which one a given prompt belongs to; it plays no role in resolving `modal_answer`/`modal_cancel`, which remain keyed on `modal_id` alone. A reconnect's re-sent `modal_shown` (#877, above) carries the same `conversation_id` as the original broadcast.

### Question (v2)

When the supervised claude calls its `AskUserQuestion` tool it is asking the operator to pick among named options before it continues. The daemon describes that whole batch to the phone as a single `question_shown` frame (#1962 declared the wire type, #1963 the shape, #1964 the fixtures and this section), and retires it with a `question_dismissed` frame (#1974). All fields on both are always present (no omitempty), so an empty header or an unset `multi_select` is a real answer rather than a vanished one. Both frames are emitted (#1973). The **inbound half is `question_answer` and `question_refused`** (#1983, documented below), and the daemon now **intercepts and decodes both** (#1984): each is caught by the v2 control switch before the ordinary application dispatch, decoded into its typed payload, and handed to a resolver seam together with the connection's device. **Nothing acts on either frame yet** — the seam is unwired, so the daemon consumes them and does nothing else. The daemon-side resolvers behind it have both landed (#1990 the refusal, #1991 the answer), and #1986 is what wires the seam to them behind the per-device answer gate. What changed for a client is that neither frame draws the unknown-type reply any more.

The batch is modelled **whole, in one frame**, and that is a decision rather than a convenience. The desktop consumer steps one question at a time with header tabs that jump between questions and a Previous button, so every question has to be in hand at once; a sequence of single-question frames cannot serve that. It is a distinct family rather than a grown [`modal_shown`](#modal_shown) — the argument is in `TypeQuestionShown`'s doc block in `internal/protocol/codes.go`.

**Reconcile on (re)connect.** On any (re)connection the daemon re-asserts every still-outstanding batch by **unicasting `question_shown` with the original `question_batch_id`** (#1979 landed the reconcile, #1980 wired the daemon's batch store to it) — this is match-and-replace by stable id (see [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)): a re-sent `question_shown` for a known `question_batch_id` updates that batch in place and **never double-shows**. Answer semantics are **unchanged under re-delivery**: the one-time `question_batch_id` nonce is minted once, at raise time, and the daemon consumes its own parked copy exactly once, so a batch re-sent across any number of reconnects stays answerable **exactly once** — the reconcile mints no nonce, retires no batch, and **re-arms no approval window**. A batch answered or [dismissed](#question_dismissed) while the client was away is **not** re-sent: it is simply absent from the next reconcile, which is [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)' reset-on-reconnect rule rather than a separate mechanism. The reconcile is **enumerate-all, not conversation-scoped** — every outstanding batch is unicast to the opening connection, each carrying its own `conversation_id` to filter display by, exactly as the raise-time broadcast does. Correlate a reconciled batch by `question_batch_id` and **never by its position** in the burst: the daemon walks an unordered store, so arrival order is not a contract.

#### `question_shown`

Direction **binary → phone** (outbound v2 clarifying-question batch; not in `v1TypeSet` — an old phone never receives it). Interactive-capability-gated, like its neighbours.

**Provenance is per field, and a client must not flatten it.** The two ids are **daemon-asserted** — the daemon fills them from its own state and they never come from claude's tool input. The four strings are **claude-authored** and crossed the subprocess trust boundary; they are the ones the SECURITY paragraph below binds.

| Field | Type | Provenance | Meaning |
|---|---|---|---|
| `conversation_id` | string | daemon-asserted | The conversation whose bound session raised this batch. **Outbound routing/scoping key only**, exactly as [`modal_shown`](#modal_shown)'s (#1065): a client with several open conversations filters display by it, and a question raised for one conversation is never rendered by a client viewing another. |
| `question_batch_id` | string | daemon-asserted | One-time, opaque, **unguessable** nonce minted per surfaced batch — `modal_id`'s role exactly, and all four of those properties carry across. It is the key an inbound answer would be resolved against **server-side**, rather than trusting anything a phone asserts, which is what obliges the minting to `crypto/rand` (#1975; the same obligation #703 carries for `modal_id`). It is `question_batch_id` and not `question_id` because the payload also declares a nested question type that carries **no id at all**, so a `question_id` beside a `questions` array would misread as that type's key. **The fixtures' ids are placeholders** — neither their length nor their shape is a contract, and nothing about a real nonce may be sized from them. |
| `questions` | array of object | — | The batch's questions, in claude's own order — the JSON-array order **is** the canonical display order. **Always present, never `null`**; a batch carrying none serialises as `[]`, so a client decoding into a non-optional array type never has to branch. Unlike [`model_list`](#model_list)'s `models`, an empty array here is **not a positive statement**: it is out of contract (below) and means a producer bug, not "claude asked nothing". |

Each element of `questions`:

| Field | Type | Provenance | Meaning |
|---|---|---|---|
| `question` | string | claude-authored | The question text. The wire key is claude's own `question`; the Go field is named `Text` only because `Question.Question` stutters. |
| `header` | string | claude-authored | Short label for the question — what a client shows on a tab. **The documented 12-character cap is contradicted by claude's own output; see below before sizing anything for it.** |
| `options` | array of object | — | The offered choices, ordered. **Always present, never `null`** — same `[]` normalisation and the same out-of-contract reading as `questions`. |
| `multi_select` | bool | claude-authored | Whether more than one option may be chosen. claude's camelCase `multiSelect`, snake-cased for this wire exactly as `argument_hint` snake-cases claude's `argumentHint`. Always present, so `false` is a stated position rather than an absent key. |

Each element of `options`:

| Field | Type | Provenance | Meaning |
|---|---|---|---|
| `label` | string | claude-authored | The option's short display text. **This is also its identity** — see the inbound note below. |
| `description` | string | claude-authored | The longer line under the label. |

`label` and `description` are the **complete** per-option key set, and that is measured rather than assumed: claude's contract gives an option an optional `preview` carrying an HTML fragment, emitted only when `toolConfig.askUserQuestion`'s `previewFormat` is set, and pyry never sets it — neither name appears anywhere under `cmd/` or `internal/` (verified 2026-09-01). The field is absent **by construction**, not dropped.

**Contract bounds — the two count bounds are enforced; the two length bounds are not.** claude's contract (<https://code.claude.com/docs/en/agent-sdk/user-input>) states one to four questions per batch and two to four options per question. Checked against the tree as it stands **2026-09-01**, not against what a sibling ticket intends:

| Bound | Enforced by |
|---|---|
| 1–4 questions per batch | **`questionbridge.Parse`** (#1965). An explicit count check inside the parse, taken **after** the decode, over the whole batch. A batch failing it is **rejected whole** — never truncated, never partially built — so an out-of-contract batch reaches no client at all. `internal/protocol` still enforces no bound by design; the enforcement is the parse's alone. |
| 2–4 options per question | **`questionbridge.Parse`** (#1965), the same mechanism one level down: an explicit count check after the decode, taken **once per question**, and a batch with any question outside the range is rejected whole rather than having that question dropped. |
| `header` at most 12 | **Nothing, and this one is expected to stay that way** — see below. |
| A maximum length for any of the four strings | **Nothing, and no number is published here either.** There is still **no per-string bound**: what #1965 landed is a single **pre-decode bound in bytes over the whole raw tool input**, which is a different thing — it caps the batch, not any one string, so no figure here would describe it. Neither is client-observable either way, because an out-of-bounds batch never reaches a client at all. Publishing a figure the code does not enforce is what [§ Attachments](#attachments) (#1752) exists to prevent, so a client learns the limit by being rejected rather than from this table. |

Naming a gap as a gap is deliberate for the two rows that are still gaps: a bound documented as if enforced is worse than a gap named as one — [§ Attachments](#attachments)' rule (#1752), and what the `2026-08-27` changelog entry below had to correct for `model_list`. The phrasing was copied from § [`slash_command_list`](#slash_command_list), whose own gap has since closed and whose wording #2010 inverted; what carries across is the rule, not the sentence.

**The header cap is documented 12, observed 14, and a client should size for 14.** The vendor page says *"Short label for the question (max 12 characters)"*. The only header in the committed capture `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json` is `Write strategy` — **14**. So the cap is a generation-side guideline claude does not itself hold to, not a wire invariant, and a client that sizes a header field for 12 and truncates past it clips the only real header anyone has measured. **The unit is runes, not bytes**, and this matters because the two diverge on the first non-ASCII header claude emits: the observed header is pure ASCII, so its 14 runes and 14 bytes coincide, and **nothing committed anywhere separates the two units yet** — that coincidence is not a measurement, and a client must not read it as one. #1965 was additionally forbidden from enforcing 12 fail-closed, because its own acceptance pinned it against this same capture: a 12-rune reject branch would have made the two unsatisfiable together. It landed **no header bound at all**, and no rune-count bound of any kind — its one cap counts bytes over the whole raw tool input — so the header row above stays exactly as written and this contradiction stays a client-side sizing instruction rather than a wire invariant. The question and option **counts** are not contradicted — the capture's one question and two options sit inside the documented 1–4 and 2–4.

**The frame is emitted.** The wire type was declared by **#1962**; the shape (`QuestionShownPayload`, `Question`, `QuestionOption` and their two `MarshalJSON` normalisers) by **#1963**; the committed fixtures and this section by **#1964**; the parse that fills the shape from claude's tool input by **#1965**; the dismissal frame below by **#1974**; the nonce mint by **#1975**; and **#1973** landed the producer, which raises the batch from the same approval surfacer a permission prompt goes through and retires it on every no-answer terminal path. The consumer is [pyrycode-desktop#849](https://github.com/pyrycode/pyrycode-desktop/issues/849), which mirrors this section field for field to write a fail-closed decode. This completes the same declare-then-emit sequencing `rate_limited` used (#1405 declared, #1410 emitted), `model_announced` used (#1616 declared, #1638 emitted) and `model_list` used (#1704 declared, #1848 mapped, #1849 emitted); [`slash_command_list`](#slash_command_list) has since completed its own (#1726 declared, #2001 mapped, #2003 emitted).

**An option's identity is a claude-authored string, and the inbound half is built so that string never makes the return trip.** `options` entries carry **no id** — unlike [`modal_shown`](#modal_shown)'s `{id, label}` — because claude's answer protocol selects an option by its **`label`**. The obvious inbound design would therefore echo that subprocess-authored string back across the boundary, and **publishing a string here does not make it trusted when it returns**. [`question_answer`](#question_answer) (#1983) avoids the question entirely: a selection names its question by **index** into this batch's `questions` array and carries values the *client* authored, so no claude-authored byte travels inbound at all. Resolution stays server-side against the daemon's own recorded batch, keyed on `question_batch_id`, exactly as `modal_answer` is against `modal_id`. A **dismissal** frame also exists and is documented below: its **own type** (#1974) rather than a reused [`modal_dismissed`](#modal_dismissed), because that frame identifies what it clears by `modal_id` and a client decoding it routes to the modal panel, so a `question_batch_id` arriving in that field clears the wrong panel or none.

**Intercepting the inbound frames grants no inbound capability.** #1983 added two type names and two payload shapes and nothing else — a Go constant is not a registry, so while that slice stood alone both frames fell through to the ordinary application dispatch and got its unknown-type reply. #1984 added the `dispatchAppFrame` cases, which is what removed that reply; it deliberately added **no authorization of its own** — no `interactive` check and no per-device check lives in the relay handler, exactly as none lives in [`modal_answer`](#modal_answer)'s, so the decision stays in one place. What keeps that fail-safe is structural rather than argued: **the resolver seam is nil at every wiring site**, so an answer reaches no actuator. The gating is **the resolver's to apply and defaults to deny** — the `interactive` capability gate, and separately the per-device answer gate (#702, default OFF) that `modal_answer` carries, which #1986 installs before anything is wired. Whether #702 extends to a question answer is that ticket's call, not this section's.

**SECURITY.** `question`, `header` and **every option's `label` and `description`** are claude-authored strings that crossed the subprocess trust boundary to a client render surface — [`model_list`](#model_list)'s trust tier, not [`slash_command_list`](#slash_command_list)'s lower workspace-authored one. This is [§ Security model](#security-model)'s threat 1 (prompt injection, `severity: high`, `mitigation: partial`) arriving on a remote render surface: claude's own words become text a phone draws. They are safe to **render as inert text** and must never be fed to an HTML sink, an attribute, or a URL. The daemon neither bounds nor sanitizes them today — nothing on this path strips control characters or terminal escape sequences — so they stay untrusted text all the way to the client, and **the render boundary that owes the sanitization is the client's**. The frame is a **report, never a control input**. One consequence is unique to this shape and a client must know it: there is **no `truncated_fields` here**, unlike `SlashCommand` and `ModelOption`, so a producer that cuts an over-long question or description has nowhere to report the cut. That is deliberate, and its cost falls on #1965 — an over-long field must be **rejected fail-closed rather than silently truncated**, or this shape grows the field — because cutting silently would present claude's truncated text to a client as complete.

#### `question_dismissed`

Direction **binary → phone** (outbound v2 question-batch resolution event; not in `v1TypeSet` — an old phone never receives it). Interactive-capability-gated, like the batch it retires. Declared by **#1974**; **#1973** landed the no-answer terminal paths that emit it, **#1990** the refusal and **#1991** the answer, so all three of the batch's terminal outcomes now emit it.

It clears a [`question_shown`](#question_shown) batch, so a client takes the panel down instead of rendering an ask that is already dead. It is its **own type** and not a [`modal_dismissed`](#modal_dismissed): that frame identifies what it clears by `modal_id` and a client routes it to the modal panel, so a `question_batch_id` arriving in that field clears the wrong panel or none.

**Field for field with [`modal_dismissed`](#modal_dismissed), including the absences.** There is **no `conversation_id`**, though `question_shown` carries one — the batch id is the sole correlation key, and a shape carrying both would admit a disagreeing pair someone has to adjudicate. A client holding the batch already knows its conversation. All fields are always present (no omitempty).

| Field | Type | Provenance | Meaning |
|---|---|---|---|
| `question_batch_id` | string | daemon-asserted | The batch being cleared — the same nonce the [`question_shown`](#question_shown) frame carried, echoed back. Match on it and clear nothing when the value is unrecognised. It is echoed to exactly the capability-gated audience that received the batch, so this discloses nothing new; and it is **dead once this frame lands**. Receiving it is **not a capability**: a retired batch resolves nothing server-side, the way a stale `modal_id` resolves nothing under first-answer-wins (#703/#706). |
| `outcome` | string | daemon-asserted | How the batch ended — a **producer-defined sentinel**, plain string, vocabulary owned by #1973/#1990/#1991 and documented rather than enforced, exactly as [`modal_dismissed`](#modal_dismissed)'s is #703's. **It never carries a claude-authored option label**, and that is a contract rather than a coincidence — see below. A client that needs the chosen option's label reads it from the batch it already holds. |
| `source` | string | daemon-asserted | What resolved the batch. **Not a closed set here**, unlike `modal_dismissed`'s — see below for which of that frame's values carry over and why the set is short. Read an unrecognised value as *resolved, cause unknown*, **never as an answer**. |

**`source` does not inherit `modal_dismissed`'s closed set, and a client must not assume it does.** That frame pins `source` to `{remote, local, timeout}`. Here:

| `modal_dismissed` value | Carries over? |
|---|---|
| `timeout` | **Structurally yes, but nothing emits it either** — and that is #1973's finding rather than a deferral. The producer's dismissal arbiter is one closure the control server defers on every `Await` return, so it cannot tell the approval window elapsing from a caller disconnect or a daemon shutdown. Naming `timeout` would state a cause wrong on two of the three paths, so all three emit `no_answer` (below). |
| `remote` | **Yes, and it is now emitted by both client-resolved outcomes.** A refusal ([`question_refused`](#question_refused), #1990) broadcasts `remote` paired with `outcome: refused`, and an answer ([`question_answer`](#question_answer), #1991) broadcasts the same `remote` paired with `outcome: answered`. It is the value for a batch the operator resolved from a client, and `outcome` is what separates the two — a client must not read `source` alone as "answered". |
| `local` | **Structurally yes, but nothing emits it and no slice in flight will.** A question parks in the same approval registry a permission does, so a desktop-TTY resolution would be the same path — but both landed resolutions arrive from a client and carry `remote`. Whoever builds the local one owns this value. |

And two of the producer's three terminal paths — **the caller disconnecting** and **the daemon shutting down** — have **no member in that set at all**. Neither is a timeout and neither is an answer. So `source` is published as a plain string whose vocabulary the producer owns, the `outcome`/`class` posture rather than the modal `source` one, and the gap is named instead of a set being published that is already known to be short — [§ Attachments](#attachments)' rule (#1752).

**The producer's landed vocabulary is three pairs, and a client should recognise all five values.** #1973 emits `source: no_answer` with `outcome: unanswered` for the whole no-answer class, because its arbiter runs identically on all three of those paths and carries nothing that separates them. `no_answer` says exactly what the daemon knows — the batch died with nobody having answered it — where `timeout` would have claimed a cause. **#1990 adds `source: remote` with `outcome: refused`**: the operator saw the batch and declined to choose, so claude's blocked call was denied with a fixed daemon-authored instruction to wait for their next message rather than left to guess or to time out. **#1991 adds `source: remote` with `outcome: answered`**, the third and last of the batch's terminal outcomes: the operator chose, so claude's blocked call was *allowed* carrying their selections. It shares `remote` with the refusal because both are a batch resolved from a client, so `outcome` is the field that separates them and a client reading `source` alone cannot tell an answer from a refusal. **`answered` is a sentinel and carries no chosen label** — that is the `outcome` rule below, and this is the pair that would break it: a client wanting the chosen option reads it from the batch it already holds. The three pairs are deliberately distinguishable — a panel that closed because nobody was there, one the operator closed on purpose and one they answered are different events to report — and no value existed before its own slice landed, which is why the unrecognised-value reading above is fail-closed: reading an unknown `source` as an answer renders a daemon safe-deny as the operator's own choice.

**SECURITY — this frame carries no claude-authored byte, and that is the whole point of the `outcome` rule.** Unlike [`question_shown`](#question_shown), whose four strings crossed the subprocess trust boundary, all three fields here are daemon-asserted, so [§ Security model](#security-model)'s threat 1 does **not** land on this frame. That holds only while `outcome` stays a sentinel, and the natural implementation of an answer path violates it: `options` entries carry **no id** and claude's answer protocol selects by **`label`**, so a producer reporting which option was chosen reaches for that claude-authored string first. Putting it in `outcome` would move this frame to the batch's trust tier while its published provenance still read *daemon-asserted*, and a client would render it as trusted chrome. Two further properties fall out of the same rule and would be lost with it: no field can carry a byte of the parked tool input into a log, and the frame's length is daemon-determined rather than subprocess-influenced — which matters in a family that ships **no `truncated_fields`** and so could not report a cut.

#### `question_answer`

Direction **phone → binary** (inbound v2 control; not in `v1TypeSet` — an old phone never sends one, and `IsKnownAppType` rejects it, which is the structural bar against a v1 client pushing one into the application dispatch chain). Declared by **#1983**.

**This frame is intercepted and decoded, and resolved by nothing yet.** Like [`modal_answer`](#modal_answer) it is caught by the v2 session manager *before* the application dispatch chain, and there is no chain handler for it; that interception is **#1984**'s, the resolution against the daemon's parked batch is **#1991**'s and the wiring that makes it reachable is **#1986**'s. Sent today it is consumed — decoded into its typed payload and handed to a resolver seam that is unwired, so nothing happens and **nothing is replied**. The unknown-type reply it drew before #1984 is gone, and that is the one client-visible change: a sender can no longer distinguish "consumed and dropped" from "consumed and acted on", and must not treat silence as either. A **payload that fails to decode is rejected outright** rather than tolerated — the seam is never called with a batch id and no answers — and nothing about the failure is echoed back, so an undecodable frame and a decodable one look identical from the client side. The shape was published ahead of all of this so [pyrycode-desktop#853](https://github.com/pyrycode/pyrycode-desktop/issues/853) could be written against a contract rather than a guess, the same declare-then-serve sequencing `attachment_chunk` used (#1752 declared, #1744 dispatched).

**Provenance is per field.** The two ids are **echoed or client-minted**; the `values` are **client-authored free text**. Nothing in this frame is claude-authored — that is a property of the positional design, not a coincidence, and it is what keeps [§ Security model](#security-model)'s threat 1 off the inbound leg.

| Field | Type | Provenance | Meaning |
|---|---|---|---|
| `question_batch_id` | string | echoed | The batch being answered — the nonce [`question_shown`](#question_shown) carried, echoed back. The **sole correlation key**: there is deliberately **no `conversation_id`**, exactly as on [`modal_answer`](#modal_answer), so the daemon resolves against its own outstanding-batch state and never trusts a phone-asserted conversation. A shape carrying both would admit a disagreeing pair someone has to adjudicate. |
| `answer_token` | string | client-minted | Idempotency key, carried over from [`modal_answer`](#modal_answer) with its meaning intact: its **uniqueness and stability matter, its secrecy does not**, and it is **not the authorization**. The daemon's actual dedup is the one-shot consume of `question_batch_id`, exactly as `modal_answer`'s is of `modal_id`. Neither id here is a secret; both are safe to log. |
| `answers` | array of object | — | The selections, one entry per question answered. **Always present, never `null`** — an empty array serialises as `[]`, so a client decoding into a non-optional array type never has to branch. An **empty array is out of contract**: a client with nothing to say sends [`question_refused`](#question_refused), which is why that is its own frame. **Array order is not the correlation** — see `question_index` below. |

Each element of `answers`:

| Field | Type | Provenance | Meaning |
|---|---|---|---|
| `question_index` | number | client-asserted | The **0-based index into this batch's `questions` array** — the canonical display order `question_shown` publishes — identifying which question this entry answers. It is an index rather than the question text because `options` entries carry no id and claude selects by `label`, so echoing text back would carry a claude-authored string inbound for no gain; the daemon reads the question from its own parked copy. It is `question_index` and not `index` so an entry quoted on its own does not read as *"the index of this answer"*. **Emit entries in batch order as a courtesy, but never infer the question from an entry's array position** — this field is what selects. |
| `values` | array of string | client-authored | The strings chosen for this question, ordered. More than one is the `multi_select` case; one is the ordinary one. **Always present, never `null`**, same `[]` normalisation as `answers`. **These are never checked against the question's offered `label`s**, and that is deliberate: claude's contract permits free text anywhere and requires no value to be one of the offered labels, so a daemon rejecting an unlisted value would reject a legal answer. |

**The vendor `response` field is deliberately not carried.** claude's contract (<https://code.claude.com/docs/en/agent-sdk/user-input>) offers an optional top-level freeform reply that replaces `answers` entirely. Both this repo and pyrycode-desktop decided on **2026-08-31** not to use it. It stays available later with no wire change, so nothing here forecloses it — but a client must not send it today, because nothing would read it.

**Contract bounds — none are enforced by the shape, and no number is published.** `internal/protocol` enforces no bound on this shape by design, the same design that leaves the outbound batch's bounds to `questionbridge.Parse`; three of the four are now enforced by the **resolver** instead (#1991), which is where they belong, since each is a bound against *the parked batch* rather than a figure this contract could name. A sender still learns them by being rejected, and the daemon-side resolver is not reachable from the wire until #1986 wires the seam:

| Bound | Enforced by |
|---|---|
| `answers` entry count | **The resolver (#1991), and the bound is the parked batch rather than a published number.** An entry count that does not *equal* the batch's question count is rejected, and that comparison runs **first**, so an array far longer than the batch is O(1) to reject and per-frame work stays bounded by the batch's own 1–4 questions. This shape still declares no number of its own; the transport's encrypted-frame cap remains the only *byte* limit. |
| `question_index` within `0 … len(questions)-1` | **The resolver (#1991), explicitly and before any subscript** — this was the sharp one, since a negative or over-large index **panics** rather than answering wrongly. The value is still *carried* here, never validated by the shape: an out-of-range index rejects the whole answer at the resolver. |
| `question_index` unique across entries | **The resolver (#1991).** Coverage is exactly-once: with the entry count equal to the question count, every index in range and none repeated, a duplicate or a missing index rejects the whole answer rather than resolving to a silently partial or last-write-wins one. |
| A maximum length for any value | **Nothing, and no figure is published here either** — [§ Attachments](#attachments)' rule (#1752): a bound documented as if enforced is worse than a gap named as one, so a client learns the limit by being rejected. |

**SECURITY — this is the family's first frame whose bytes travel *toward* claude.** [§ Security model](#security-model)'s threat 1 describes the opposite direction and does **not** land here: no field is claude-authored. What `values` do reach is claude's context, since #1991 feeds them back as the blocked tool call's result — carried **verbatim**, since free text is legal anywhere and nothing compares a value to an offered label. **That grants nothing new** — a paired client can already put arbitrary text into the conversation with `send_message`, so an answer sits at exactly that trust tier, and it is stated because the shape invites the opposite reading, that an answer is somehow more constrained than a message. Two obligations fell on the decoder rather than on the sender, and **#1984's handler discharges both**: a **decode failure is a rejected frame, never an empty-but-successful answer** — the seam is not called at all, so nothing can reach it carrying a batch id with no answers — and its error **does not embed the raw payload**, which is never wrapped, logged or replied, since those bytes are remote-authored and nothing on this path strips terminal escape sequences. They stay stated as obligations because they bind every future decoder of this shape, not only the first; and note that the rule is about a decode *error*, so a `null` payload, which decodes cleanly into the zero value, is an unknown batch for the resolver to judge rather than a rejected frame.

#### `question_refused`

Direction **phone → binary** (inbound v2 control; not in `v1TypeSet`, rejected by `IsKnownAppType` for its sibling's reasons). Declared by **#1983**; **intercepted and decoded by #1984**, and **resolved by #1990** — which consumes the daemon's parked batch, denies claude's blocked call with a fixed instruction to wait for the operator's message, and broadcasts one [`question_dismissed`](#question_dismissed) carrying `outcome: refused` / `source: remote`. **Nothing reaches that resolution from the wire yet**: the resolver seam stays nil until the per-device gate (#1986) is wired, so the sibling's paragraph on what a client observes applies here unchanged.

The operator declined to choose, so the batch resolves with no selection. It is its **own type** rather than a [`question_answer`](#question_answer) carrying an empty `answers` array or a nullable flag, mirroring [`modal_cancel`](#modal_cancel) beside [`modal_answer`](#modal_answer) and following the precedent [`question_dismissed`](#question_dismissed) set (#1974): a distinct meaning gets a distinct type, so a client and a daemon both route on the frame's **name** rather than on a value's **shape**. A shape able to express *"answered with nothing"* would need somebody to adjudicate it against a genuine refusal.

| Field | Type | Provenance | Meaning |
|---|---|---|---|
| `question_batch_id` | string | echoed | The batch being refused. Sole correlation key, **no `conversation_id`**, for [`question_answer`](#question_answer)'s reasons. |
| `answer_token` | string | client-minted | Idempotency key, carried for the same reason its sibling carries it: a refusal is as replayable as an answer, and the daemon's dedup is the same one-shot consume of `question_batch_id`. Not the authorization. |

**And nothing else.** All fields are always present (no omitempty), and the payload carries **no free text at all** — both fields are ids the client echoes or mints — which makes it the narrowest surface in this family.

### Queue (v2)

A phone that types while claude is busy has its turn buffered in the daemon's queued-message backlog (#597 Phase 3, `internal/msgqueue`). `queue_state` (view) lets the phone see that backlog and `dequeue_message` (cancel) lets it drop an entry it no longer wants. Unlike the [Modal](#modal-v2) cluster — where answering is gated per-device — **both viewing and dequeuing are ungated for any paired phone** (ADR 025 [Security model](#security-model)): there is no per-device gate and no nonce; `queued_msg_id` is a plain per-conversation counter. All fields are always present (no omitempty). This section is wire vocabulary only; *when* the daemon emits `queue_state` and *how* the handler applies `dequeue_message` is the producer's (#722) / handler's (#723) runtime, documented there.

#### `queue_state`

Direction **binary → phone** (outbound v2 queued-backlog snapshot; not in `v1TypeSet` — an old phone never receives it).

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation this backlog belongs to. The daemon's own resolved id (#722), never attacker-derived. |
| `queued` | array | Ordered backlog (FIFO/enqueue order) of `{queued_msg_id, message_id, text, ts}`. **Always present**; the producer (#722) emits `[]` (not `null`) for an empty backlog so the mobile decoder keeps `queued` a plain array. Each element: `queued_msg_id` (integer, stable per-conversation counter ≥ 1), `message_id` (string, see below), `text` (string, the queued message), `ts` (RFC3339Nano, enqueue time). |

**`message_id` (#2092)** is the id the client minted on the [`send_message`](#send_message) that produced this item, **relayed verbatim** — byte-for-byte what the client sent, never trimmed, lower-cased or re-encoded. It is `""` when the client sent none (a legal value: the field is non-omitempty and the daemon validates nothing about it), and the daemon **never mints one**. It exists so a client can recognise a queued item as one of its own sent messages: a client posts an optimistic echo the moment the operator hits send — it must, because in interactive mode the daemon streams no user-message event, so that echo is the client's only record of its own message — and without a shared key the client draws the same message twice, once as a delivered bubble and once as a queued row. Worse, dropping the queued message then removes only the queued row, leaving the echo reading as a message claude received when it never did.

`message_id` **addresses nothing**. [`dequeue_message`](#dequeue_message) still resolves `conversation_id` + `queued_msg_id` and nothing else, and no daemon path reads `message_id` to route, authorize, match or dedupe. It is a correlation key for the client, not an identifier on the wire — uniqueness is enforced nowhere, and two items may legally carry the same one.

**Emission (#722).** The daemon pushes a `queue_state` for a conversation whenever that conversation's backlog **changes** — a message is enqueued (a phone `send_message` buffered while claude is busy), drains to claude (the FIFO head delivered), or is removed before draining (`dequeue_message`). An empty backlog emits `queued: []` so the phone can clear its view. Each push carries **only** the changed conversation's items: the producer snapshots the single conversation named by the change and never bundles another conversation's queued text into the same payload (the `conversation_id` and `queued` are derived from one id, so they cannot desync).

`queue_state` fans **only** to connections that negotiated the `interactive` capability; a non-interactive connection never receives it (the same gate as the rest of the structured stream). The fan-out reaches *every* interactive connection, each payload stamped with its own `conversation_id` — there is no per-connection conversation binding, so a phone attributes each `queue_state` by id (consistent with the [Security model](#security-model): a user's paired devices are one trust domain).

**Multi-device rule for `message_id`.** Because the fan-out reaches every interactive connection, a client routinely sees items carrying `message_id`s **it never minted** — another paired device's. Two consequences, both required:

- An item whose `message_id` matches no local echo renders as a **plain queued row** and is **never dropped**. It is a real queued message belonging to another device; hiding it would misrepresent the backlog.
- A client merges an item **only against echoes it minted itself**, never against a store shared across devices. `message_id` is client-chosen and uniqueness across devices is enforced nowhere, so a colliding id must not let one device attribute another device's queued message to its own echo.

`queue_state` is **not** part of the #647 reconnect-replay ring (`EventID` nil). Instead, on (re)connect the daemon reconciles current queue truth by **unicasting a `queue_state` snapshot for every non-empty backlog** (#878) — a current-state snapshot, not ring replay. Because `queue_state` is already snapshot-shaped full state (`queued: []` clears a view), this connect-time re-send is idempotent by construction; it **complements** the change-driven push above (change-time push + connect-time reconcile), and the phone match-and-replaces each backlog by `conversation_id` plus `queued_msg_id` (see [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)).

#### `dequeue_message`

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session manager before `dispatch.Route` — it is not a `dispatch.Route` handler. It is **ungated**; resolving `conversation_id` to an authorized conversation and applying the removal (`msgqueue.Remove`) is the handler's (#723) job.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation to dequeue from. Untrusted phone input — the handler (#723) resolves it to an authorized conversation. |
| `queued_msg_id` | integer | The id to remove (the `queued_msg_id` from a `queue_state` entry). |

### Interrupt (v2)

A paired phone sends `interrupt` to stop the running turn — the remote equivalent of pressing **Esc** at the local terminal (#597 Phase 3, #707). The daemon maps it to the neutral `turnevent.Cancel` command and routes it to the supervised claude as a single Esc keystroke (claude's own interrupt). #600 maps ACP `session/cancel` onto the same neutral shape.

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session manager before `dispatch.Route` — it is not a `dispatch.Route` handler (like [`modal_cancel`](#modal-v2) / [`dequeue_message`](#queue-v2)).

It carries **no payload** — a bare control frame, with no `conversation_id`, no `modal_id` nonce, no `answer_token`, and no idempotency key. A replayed `interrupt` simply sends another Esc (an Esc with no running turn is a no-op in claude), so no nonce or dedup is needed.

`interrupt` is **gated on the `interactive` capability**: a non-interactive connection's `interrupt` is inert (no Esc). It is **exempt from the per-device permission gate** ([#702](#security-model)) because interrupting one's own paired session is a normal paired-phone action, not a tool-permission decision. Any interactive paired phone can interrupt the single live claude — there is no per-connection conversation binding for interrupt, consistent with the broadcast fan-out model (a user's paired devices are one trust domain, per the [Security model](#security-model)). It is **not** part of the reconnect-replay ring and needs no correlation key.

### New session (v2)

A paired phone sends `new_session` to start a fresh session — the remote equivalent of typing **`/clear`** at the local terminal (#597 Phase 3, #831). The daemon routes it to the supervised claude as a `/clear` via the sealed supervisor `StartNewSession` seam (#830); unlike `interrupt` it maps to no neutral `turnevent` command.

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session manager before `dispatch.Route` — it is not a `dispatch.Route` handler (like [`interrupt`](#interrupt-v2) / [`modal_cancel`](#modal-v2)).

It carries **no payload** — a bare control frame, with no `conversation_id`, no nonce, and no idempotency key. A replayed `new_session` simply drives another `/clear` (starting a fresh session again is harmless), so no nonce or dedup is needed.

`new_session` is **gated on the `interactive` capability**: a non-interactive connection's `new_session` is inert (no `/clear`). It is **exempt from the per-device permission gate** ([#702](#security-model)) because starting a fresh session in one's own paired session is a normal paired-phone action, not a tool-permission decision. Any interactive paired phone can start a new session on the single live claude — there is no per-connection conversation binding, consistent with the broadcast fan-out model (a user's paired devices are one trust domain, per the [Security model](#security-model)).

The client observes the resulting break through the existing [`session_transition`](#interactive-events-v2-capability-gated) marker (`reason: clear`, #656/#657) — **there is no synchronous ack.** `new_session` is fire-and-forget, like `interrupt`; it is **not** part of the reconnect-replay ring and needs no correlation key.

### Debug bundle (v2)

A paired client can request a **debug bundle** — a session's evidence archive (the
most-recent terminal `.cast` recording plus a daemon log-ring snapshot), assembled
in-memory by the daemon (#811). The bundle is content-bearing and routinely
exceeds one AEAD frame (a real `.cast` recording alone exceeds 65535 bytes), so
the daemon **streams** it as ordered, cap-respecting chunks ending in a completion
marker rather than trying to fit it in a single reply. Split from #803; the
streaming transport is #812, the request verb that triggers it is #813.

#### `request_debug_bundle`

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session
manager before `dispatch.Route` — it is not a `dispatch.Route` handler (like
[`interrupt`](#interrupt-v2) / [`dequeue_message`](#queue-v2)).

It carries **no payload** — a bare control frame, with no `conversation_id` and no
other field. The bundle is **daemon-global** by construction (the whole log ring,
which has no per-session key, plus the newest recording across all sessions), so
there is nothing for the request to name and no field an attacker could use to
select another session's data.

**Authorization is pairing**, enforced structurally at the Noise IK handshake: an
unpaired device is refused at the handshake (WS 4401) and never reaches this
handler, so there is no per-verb authorization gate. Unlike `interrupt` /
`dequeue_message`, `request_debug_bundle` is **not** gated on the `interactive`
capability — any paired, open connection (including a non-interactive desktop
diagnostic tool) may request a bundle. The daemon replies by **streaming** the
assembled bundle back to the requesting connection (`debug_bundle_chunk*` +
`debug_bundle_done`); it is never broadcast. If the bundle cannot be assembled the
daemon replies with a single `error` envelope carrying a static message and a
`server.binary_offline` code (`retryable: true`) — never the assembly error text.

The stream rides the manager's own **asynchronous** push path (`StreamBundle` →
`Push` → drain), the same path every other unsolicited binary → phone frame uses —
**not** the synchronous request/reply handler path, whose 8-slot outbound buffer
(drained only after the handler returns) would deadlock on a multi-chunk send.
Both frame types are outbound **binary → phone** and are **not** in `v1TypeSet` —
an old phone never receives them.

#### `debug_bundle_chunk`

Direction **binary → phone** (outbound). One ordered slice of the bundle.

| Field | Type | Meaning |
|---|---|---|
| `seq` | integer | 0-based, contiguous, ascending chunk index. The receiver requires the next chunk's `seq` to equal the count of chunks already seen, so a reorder, gap, or duplicate is detected — never silently accepted. |
| `data` | string (base64) | The raw bundle slice for this chunk, standard-base64-encoded. The phone base64-decodes and appends it. Each chunk's raw size is bounded (≈48000 B) so the sealed `noise_msg` ciphertext — base64 ×4/3 + envelope wrapper + 16 B AEAD tag — stays under the 65535-byte cap. |

#### `debug_bundle_done`

Direction **binary → phone** (outbound). The completion marker, sent after the
last `debug_bundle_chunk`.

| Field | Type | Meaning |
|---|---|---|
| `total` | integer | The exact number of `debug_bundle_chunk` frames in this stream. The receiver uses it to detect a **truncated** stream: a `done` whose `total` ≠ the count of chunks actually received is a count-mismatch error, never accepted as complete. |

**Reassembly & integrity.** The receiver reassembles by appending each chunk's
decoded `data` in `seq` order, stopping at `debug_bundle_done` once `total` chunks
have arrived. It **fails cleanly** (never emits corrupted or partial output) on an
out-of-order / gap / duplicate `seq`, a `total` mismatch, or a missing marker.
Two independent integrity nets guard the stream: the AEAD (ChaChaPoly) already
guarantees per-frame *content* integrity on the wire, and `seq` + `total` add
*structural* gap / reorder / truncation detection. A **one-chunk** stream is valid
(a small bundle emits one `debug_bundle_chunk` + one `debug_bundle_done{total:1}`);
an empty bundle emits zero chunks + `debug_bundle_done{total:0}`. Interleaved
non-bundle frames (e.g. an `assistant_delta`) are filtered by type and ignored.

**Content hygiene.** The bundle carries session content (recording + logs); the
chunk framing and completion marker are sealed under the Noise channel like every
other `noise_msg`, and the daemon never writes the streamed bytes to its logs (not
even the assembly step, which makes zero log calls — #811). Capture-off
**withholds** the recording structurally: when no recording exists the archive has
no `recording.cast` member and the manifest marks it absent — the recording cannot
leak regardless of how the client reads the manifest. *Who* may request a bundle is
settled by [`request_debug_bundle`](#request_debug_bundle) above (pairing);
the streaming transport faithfully seals whatever blob it is handed to an
already-authenticated, open conn.

### Attachments

A client uploads a file to the daemon, and retrieves one back, as a stream of
**`attachment_chunk`** frames. A file crossing the encrypted mobile channel
routinely exceeds one AEAD frame, so the sender splits it and the receiver
reassembles it; the relay stays transport-only, with no blob endpoint. Split from
#1740 — the frame and its payload are #1752, the per-chunk byte bound is #1753,
and this section plus the `attachment.*` reject vocabulary is #1751.

**One frame carries both directions.** Upload (phone → binary) and retrieval
(binary → phone) ride the same `attachment_chunk`, and declaring exactly one
shape is what stops the two legs drifting as they are built months apart: there
is no second shape to keep in step. What differs between the legs is not the
shape but the **trust** — see **Trust and content hygiene** below.

**Nothing emits, accepts or enforces any of this yet.** The contract is published
ahead of its implementation, the same declare-then-publish sequencing
[`model_list`](#model_list) and [`slash_command_list`](#slash_command_list) used:
reassembly and claim-checking are #1741, storage is #1743, the inbound dispatch
is #1897, and retrieval is three slices — the request verb #2052, the outbound
stream #2053, and the handler that joins them #2054.

**The section now publishes every frame in the transfer.** The one thing it used
to hold back — the **retrieval request verb** — is
[`request_attachment`](#request_attachment) below (#2052), so a client no longer
has to guess at a verb no document named. **Nothing answers it yet**: the
outbound stream that replies with the bytes has landed (#2053) but ships
**unwired**, and #2054 is the handler that will join the two.

**The upload success reply is now declared** — [`attachment_stored`](#attachment_stored)
below (#1895), together with the [`attachment_id` shape](#the-attachment_id-shape)
a client must obey. That closes the gap this section used to name: a client that
uploaded was told what went wrong on every failure path and got silence on the one
that worked. Nothing emits the frame yet; the dispatch that does is #1897, and
#1898 observes it end to end. (#1744, which this section previously credited for
both the dispatch and the reply, was split into those two and no longer exists as
work — the repair [`model_list`](#model_list)'s changelog entry made for #1693.)

#### `attachment_chunk`

Direction **phone ↔ binary**. One slice of one attachment's bytes, plus the whole
transfer's metadata repeated on every chunk. **Every field is always present in
both directions** — no field is ever elided, so a decoder may rely on all eight.

| Field | Type | Meaning |
|---|---|---|
| `attachment_id` | string | The transfer this chunk belongs to, repeated identically on every chunk: the key a receiver accumulates under, and the identifier that later resolves to a file. At most 64 bytes — but that is a **ceiling, not the shape**, and a client that picks any short string within it has its upload refused at the end. The rule is **[The `attachment_id` shape](#the-attachment_id-shape)** below; read it before minting one. **Not a capability** — not secret, not unguessable, and never the only thing standing between a caller and a file. |
| `index` | integer | 0-based position of this chunk within the attachment, in `[0, total_chunks)`. It **decides where the bytes land**: a receiver addresses by it and never appends. |
| `total_chunks` | integer | How many chunks the whole attachment splits into: ≥ 1, and identical on every chunk of one transfer. Because it rides every chunk, this stream needs **no completion frame**. |
| `filename` | string | The client's own name for the file: a display string and a sanitiser input, **never a path**. At most 255 bytes (POSIX `NAME_MAX` — one path component). |
| `mime_type` | string | **Inbound**, the client's declared media type. **Outbound**, a value the daemon *sniffs from the stored bytes* — the declared one is never stored, so there is nothing to echo. Either way a display and dispatch hint and **not a safe property of the bytes**: see **Trust and content hygiene** below. At most 255 bytes (RFC 6838 § 4.2's two 127-character halves). |
| `size` | integer | Declared byte length of the **whole file**, not of this chunk. |
| `sha256` | string | Lowercase hex sha256 of the **whole file**, not of this chunk; always 64 characters. |
| `data` | string (base64) | This chunk's raw bytes, standard-base64 (`base64.StdEncoding`, padded). At most **45000 raw bytes before encoding** — see **Chunking** below. |

The three metadata bounds (64 / 255 / 255) count **bytes, not runes**, and a
client must obey them for its own frames to fit the envelope cap. A length
ceiling on `attachment_id` is **not** a safety property: 64 bytes accommodates
`../../../../etc/passwd` several times over, so containment is the receiver's
resolution check and never the bound.

There is **no `conversation_id`**, and the omission is a security property rather
than an oversight. An upload lands in the conversation the authenticated session
is already on, decided daemon-side from session context, so a client cannot steer
bytes into another conversation's directory by naming one. Retrieval's request
verb does name a conversation, but that is a different frame —
[`request_attachment`](#request_attachment) (#2052) — and what makes *it* safe is
that the id is a lookup key validated against the daemon's registry, never a
value trusted as sent.

**Chunking (the sender's obligation).** The per-chunk bound is **45000 raw bytes
of `data` before base64** — not base64 characters, not payload bytes, not
envelope bytes. A sender that mistakes it for base64 characters produces frames
that fit but wastes a quarter of every one. The rule is arithmetic a client can
implement directly:

- every chunk but the last carries **exactly 45000** raw bytes; the last carries
  the remainder;
- `total_chunks = max(1, ceil(size / 45000))`. The `max(1, …)` is what defines
  the **zero-byte file**: one chunk carrying zero bytes, consistent with
  `total_chunks ≥ 1`.

45000 is chosen so that a chunk's serialised envelope — base64 ×4/3, plus every
metadata field at its bound with worst-case JSON escaping — stays under the
[65519-byte application-envelope cap](#wire-shapes). That is a
**producer-side contract with no validator**: an envelope exceeding 65519 bytes
after serialisation is rejected by the transport with **`message.too_long`**,
never with an `attachment.*` code. The two size codes say different things —
`message.too_long` means **one envelope** was too big, `attachment.too_large`
means the **whole transfer** exceeds the receiver's per-upload bound.

**Reassembly & integrity (the receiver's rules).** The receiver stores each
chunk's decoded `data` **at its `index`**, so **chunks may arrive in any order**.
That is deliberately weaker than [`debug_bundle_chunk`](#debug_bundle_chunk)'s
`seq`, which demands strict succession — the neighbouring rule is the obvious
thing to copy and it is the wrong one here.

- A **duplicate `index`**, an `index` outside `[0, total_chunks)`, or a
  `total_chunks` disagreeing with the stream's earlier chunks: the stream is
  discarded and the receiver answers `attachment.invalid_chunk`.
- The transfer is **complete** when every index in `[0, total_chunks)` has
  arrived exactly once. Only then does the receiver compare the assembled length
  against `size`, and `sha256(assembled)` against `sha256`, as **lowercase hex
  for exact equality**. A case-insensitive or prefix comparison is a hole, while
  a comparison that rejects an uppercase-sending client is an availability bug —
  so the canonical form is lowercase and clients send it that way.
- Either mismatch → `attachment.integrity_failed`. A receiver **never emits
  partial or corrupted output**.
- `sha256` is **integrity, not authenticity**. The same party supplies the bytes
  and the digest, so a match proves the transfer was not corrupted and proves
  nothing about whether the content is safe. It is also **not a fetch key**:
  content-addressed retrieval ("know the hash, fetch the blob") would promote a
  non-secret claim into a capability, and retrieval names a conversation and an
  attachment, never a hash.
- **Never allocate from a claim.** On the inbound leg `total_chunks` and `size`
  are attacker-chosen integers: sizing a buffer from a claimed `total_chunks` of
  2³¹−1 is a multi-gigabyte allocation driven by a single ~60 KB frame. A
  receiver range-checks both, and cross-checks them against each other through
  the 45000-byte bound, **before** anything is sized — a check available from the
  **first** chunk, before one byte is accumulated. A transfer over the receiver's
  per-upload byte bound is refused with `attachment.too_large`; one arriving
  while too many uploads are already in flight is refused with
  `attachment.too_many_uploads`. Both of those bounds are **receiver-configured
  and unpublished** — a client learns them by being rejected, not by reading this
  document.

**Retrieval, and its two terminal signals.** Retrieval is the same frame,
daemon-authored, flowing binary → phone in reply to
[`request_attachment`](#request_attachment) (#2052), correlated to it by
`in_reply_to`.

- **Completion** is `total_chunks` distinct indices received. There is **no
  completion frame**, and none is coming:
  [`debug_bundle_done`](#debug_bundle_done) exists because bundle chunks carry
  only `seq` and the count is unknowable until the end, whereas here the count
  rides frame one — so a truncated stream is detectable *earlier* rather than
  later.
- **Abandonment** is `attachment.stream_aborted`, an `error` envelope correlated
  by `in_reply_to` — not a second attachment frame. On receiving it a client
  **MUST discard everything accumulated for that transfer** and MUST NOT present
  the partial bytes as the file. With no completion frame this is the stream's
  only negative signal, and a client that keeps its buffer renders a truncated
  file as a whole one.
- A stream that simply **stops**, with no abort frame (the session died), is
  detected by the client's own timeout. The protocol offers no frame for it.
- A request that yields no bytes at all is `attachment.not_found` — one code for
  every such outcome; see [Error codes](#error-codes).

**Trust and content hygiene.** **Inbound, every field is a claim, not a fact**,
and the daemon validates each before use. **Outbound every field is
daemon-authored, and none is a stored client string echoed back verbatim** — but
that improves their *provenance*, not their *trustworthiness*, and the
difference is the whole of this paragraph.

Nothing stores what a client declared. The upload's `mime_type` is discarded
outright, and its `size` and `sha256` are checked against the assembled bytes at
admission and then dropped; only the bytes are kept, under a **sanitised**
filename. So on the retrieval leg the daemon derives what it did not store:
`size` and `sha256` from the stored bytes, `mime_type` **from the bytes too** —
so that a file whose name and client-declared type disagree with its content is
described by its content — and `filename` is the sanitised single path component
the bytes are stored under, never the client's own string.

**That does not make any of them safe to act on.** `mime_type` is still computed
from bytes an attacker chose, so a *sniffed* `text/html` is exactly as dangerous
to render as a declared one, and a sanitised filename is still attacker-shaped
text. Every client obligation below therefore stands unchanged: a client
**MUST** sanitise `filename` before rendering it, **MUST NOT** use it as a path
or a filesystem name unsanitised, and **MUST NOT** dispatch on `mime_type` in any
way that grants the content privileges — no rendering an attacker-chosen
`text/html` as markup. A client
should also **bound what it allocates** from an outbound `size` / `total_chunks`
against its own memory budget and refuse a transfer larger than it can hold
rather than attempt it: the daemon is trusted here, but a fixed-budget client
still has a budget.

`data` is **content-bearing and never logged** — a stronger rule than the debug
bundle's, because those are daemon-authored diagnostics and these are a user's
own private file bytes. `filename` gets the same treatment for two independent
reasons: a filename is often private in itself, and a client-supplied string in a
line-oriented log is a log-injection shape. Log the attachment id, the index and
the total; never the bytes, and never a raw filename. The rule binds both ends of
the channel, not just the daemon.

**Authorization is pairing**, enforced structurally at the Noise IK handshake,
exactly as [`request_debug_bundle`](#request_debug_bundle) records: an unpaired
device is refused at the handshake (WS `4401`) and never reaches these paths.
There is no per-verb authorization gate on either leg, and none is invented here.
That holds for `attachment_stored` below unchanged — it grants nothing, so there
is nothing extra to gate.

#### `attachment_stored`

Direction **binary → phone** (outbound v2 reply; not in `v1TypeSet` — an old phone
never receives one, and `IsKnownAppType` rejects it, which is also the structural
bar against a phone sending one and asserting that bytes it never uploaded are
stored). Declared by **#1895**; **nothing emits it yet** — the inbound dispatch
that does is #1897, and #1898 observes it end to end.

The upload leg's **single positive terminal signal**: the transfer completed,
every claim on it was checked, and the bytes are stored under the `attachment_id`
the client chose. Before it, a client that uploaded was told what went wrong on
each of the seven [`attachment.*`](#error-codes) failure paths and got **silence**
on the path that worked. It is named as the positive of `attachment.storage_failed`
so the family's terminal outcomes read as one set — but it is not narrow: it says
the **whole transfer** succeeded, not merely that the storage step did.

**Correlation rides `in_reply_to`, and it names the chunk *whose arrival completed
the transfer*.** That is not necessarily the chunk with the highest `index`: this
section publishes that chunks may arrive in **any order** and that the transfer is
complete when every index in `[0, total_chunks)` has arrived exactly once, so the
completing chunk is whichever one closed the set. **A client cannot predict which
of its envelope ids that will be**, which is exactly why `attachment_id` also
rides the payload. The two are not redundant: `in_reply_to` says *which frame this
answers*, the payload says *which transfer this concludes*, and only the second is
a value the client chose and can look up. Match on the payload id; treat
`in_reply_to` as provenance rather than as your index key.

**Every field is daemon-asserted**, which is the whole difference from the frame
it answers. `attachment_chunk` rides both legs and nothing in it reports which
direction a value came from, so its fields are claims inbound and daemon-derived
outbound — derived from the stored bytes rather than echoed from what a client
declared, since nothing a client declared is stored. Here there is nothing to
disambiguate — hence the provenance column.

| Field | Type | Provenance | Meaning |
|---|---|---|---|
| `attachment_id` | string | daemon-asserted (the client's own id, echoed) | The attachment that was stored — the id repeated on every chunk of the upload, echoed back after the receiver validated its shape. **Not a capability**: not secret, not unguessable, and never the only thing between a caller and a file; it is echoed to exactly the authenticated session that uploaded the bytes, so this discloses nothing new. Conclude nothing on an id you do not recognise, and never present bytes you did not upload. |

**And nothing else — every absence is a decision.**

- **No host path, no directory component, no on-disk filename.** [§ Error codes](#error-codes) already forbids `attachment.storage_failed` from carrying the host path or the underlying filesystem error, either of which discloses the daemon's layout; a success frame leaking what the failure frame is guarded against would undo that mitigation from the other side.
- **No stored filename in particular.** The daemon folds a client's `filename` into one path component, and the result is **neither unique nor an identifier** — distinct client names collide, and a case-insensitive host folds them further — so echoing it would hand a client something it cannot rely on. Retrieval addresses by `attachment_id`, and the client already knows the name it sent.
- **No `conversation_id`**, for `attachment_chunk`'s reason: the upload landed in the conversation the authenticated session is already on, and a client holding the transfer already knows it.
- **No `size`, `sha256` or `total_chunks`.** The client sent all three and they were checked against the assembled bytes before this frame can be emitted, so echoing them back confirms nothing a client could act on.

The field is always present (no omitempty). This frame carries **no client-authored
and no claude-authored byte**, so [§ Security model](#security-model)'s threat 1
does not land on it, and — unlike `filename`, `sha256` and `data`, which this
section forbids logging — **it is safe to log whole**.

##### The `attachment_id` shape

**A client mints the id, so the client has to get it right**, and the rule below
binds every frame in this section — `attachment_chunk` on both legs and
`attachment_stored`. The `attachment_id` field row's *"at most 64 bytes"* is a
**ceiling for the envelope-size arithmetic, not the shape**, and reading it as
permission to use any short string is the mistake this paragraph exists to
prevent.

The canonical form is a **lowercase UUIDv4 string**, exactly:

- **36 bytes**, no more and no less;
- `-` at offsets **8, 13, 18 and 23**;
- `4` at offset **14**;
- one of `8`, `9`, `a`, `b` at offset **19**;
- **lowercase hex** (`0`–`9`, `a`–`f`) everywhere else.

`3f2a1c40-9b7e-4d16-a5c3-0e8f1b2d4a67` is a conforming id.

**Lowercase is load-bearing, not cosmetic.** The id becomes a **directory name** on
the host, and the lowercase-only alphabet is what keeps the id-to-directory mapping
**injective on a case-insensitive filesystem** — which APFS, the macOS default, is.
A client that mints uppercase ids gets **two distinct attachments resolving to one
directory** on a Mac, and their writes cross-contaminate with no symlink involved.
The same alphabet is also why a conforming id contains no `/`, no `.`, no NUL and
no `..`, so it cannot spell any path component other than itself — but note that
**containment is a consequence of the shape, never of the length ceiling**: 64
bytes accommodates `../../../../etc/passwd` several times over.

**Nothing checks the shape at admission today**, and a client should know what that
costs. A receiver keys an in-flight upload on the **raw string**, so a
non-canonical id is accepted, every chunk is transmitted, and the transfer is
refused only when the completing chunk reaches storage. Until an admission-time
check exists, **a whole transfer is the price of learning this rule by being
rejected** — which is why it is published here rather than left to be discovered.

##### Naming a message's attachments

Uploading a file says nothing about **which message it belongs to**. Declared by
**#2036**: [`send_message`](#application-message-types) carries an optional
`attachment_ids`, so the daemon names a message's attachments instead of
inferring the set from upload order or arrival timing. **#2038 is the consumer**:
the daemon resolves each named id to its on-host path and composes a prompt that
names those paths and directs `claude` to read them — it does **not** build a
native image content block, since once the bytes are on disk inlining them would
duplicate them into the transcript the path form keeps compact.

| Field | Type | Meaning |
|---|---|---|
| `attachment_ids` | array of string, **optional** | The uploaded attachments this message references, in the client's own presentation order. Each element obeys **[The `attachment_id` shape](#the-attachment_id-shape)** above — the same lowercase-UUIDv4 rule binding every id in this section. Array **order is not a correlation**: ids identify attachments, positions identify nothing. |

**At most 32 ids per message**, and the bound **counts elements, not distinct
ids** — a list may repeat one id, and it is the raw element count that is
measured. **Enforced since #2038**, which counts the list before it counts
anything else; it was a published-but-unchecked number until then. This is a
published **number**, the same producer-side-contract idiom as the 45000-byte
chunk bound and the 64-byte id ceiling. It is deliberately *not* the section's
other bound idiom — the per-upload byte bound and the concurrency bound behind
`attachment.too_large` / `attachment.too_many_uploads` are receiver-configured
and unpublished, learned by being rejected. A client composing a message needs
this one **before** it sends.

32 is arithmetic rather than taste. A canonical id is 36 bytes and costs 39
inside a JSON array, so 32 of them are 1248 B — **under 2% of the
[65519-byte envelope cap](#wire-shapes)** — and the bound never competes with
`text` for envelope budget. That is also why it is not derived *from* the cap,
which would allow roughly 1680 ids and leave no room for the message; the binding
constraint is the **resolution work** each named id costs, not bytes. **The bound
is not a DoS mitigation and is not offered as one:** the envelope cap already
limits an unbounded list to ~1680 elements, and a paired device may already send
messages that spawn `claude` turns — far more expensive than 1680 directory
resolutions. It is contract clarity and bounded work.

**A message naming no attachments omits the key entirely.** That is the canonical
empty form, and it is simply what shipping clients already send: `send_message`
is a v1-compatible type that predates this field. A receiver **also** accepts
`null` and `[]`, normalises all three, and **cannot tell them apart** — so a
client may send any of the three and no daemon behaviour can ever depend on which.
The always-present-never-null encoding used by `question_shown`,
`background_task_roster`, `model_list` and `tool_use` deliberately does **not**
apply here: every one of those is an outbound, daemon-authored v2 frame, and
publishing "always present" for a key half this wire's population never sends
would state a contract the wire does not honour.

**Every element is a claim, not a fact**, exactly as on the upload leg. Two
independent hazards follow, and **one canonical-shape check answers both**:

- An element **becomes a directory component** beneath the resolved conversation
  directory. Validate the shape *before* it reaches a path join — and note again
  that **containment is a consequence of the shape, never of the length
  ceiling**.
- An element **becomes prompt content**. Unlike `attachment_stored`, which carries
  no client-authored byte and is exempt, these are client-authored and reach
  `claude` through #2038, so [§ Security model](#security-model)'s **threat 1
  (prompt injection)** lands squarely on them. A shape-validated id draws from
  `[0-9a-f-]` only and **cannot carry injection text**; an unvalidated one is an
  arbitrary string. The check is therefore not optional even for a consumer that
  never touches the filesystem.

Like `filename`, an element is **loggable only after its shape is validated** —
raw, it is a client-supplied string in a line-oriented log, the log-injection
shape this section already forbids.

**Resolution is confined to the message's own conversation**, the one the
authenticated session is already on, never a client-asserted one. This is the
security property that makes the field safe, and it is the same reasoning behind
`attachment_chunk` having no `conversation_id`: without it the field would read
"name any id, get its bytes into your prompt", promoting an identifier this
document repeatedly calls **not a capability** into exactly that. Nothing about
the id's shape or its randomness does this work — **confinement does**, and a
client must not read "UUIDv4" as a claim of unguessability.

**An id that does not resolve under that conversation is
[`attachment.not_found`](#error-codes)** — the retrieval leg's code, widened to
span both verbs rather than a new one minted, because the predicate, the
retryability, the static message and the client's repair are identical. Its
message stays static and, where a message names several ids, **does not say which
one failed**: a per-id answer would turn one `send_message` into a batch
existence-probe for up to 32 ids and rebuild the path-existence oracle that
code's merge exists to prevent.

**An over-bound list is refused with [`protocol.malformed`](#error-codes), not
retryable** (#2038, the first thing to count one). Refused rather than
**truncated**: truncation drops files a person attached and gives the client no
signal it happened, so the message ships looking complete — the same
reject-never-drop posture the backlog cap and every other bound in this family
take. `protocol.malformed` rather than a code minted for this path, because 32 is
a producer-side contract a client knows **before** it sends, so a frame naming 33
is not a conforming `send_message` in the same sense an ill-typed
`attachment_ids` is not, and that already answers this code. It is **not**
`message.too_long`, which says one *envelope* was oversized — 32 ids are under 2%
of the envelope cap, so the two conditions are independent — and it is **not**
`attachment.not_found`, since nothing was resolved and no id failed.

**A repeated id is deduplicated on first occurrence**, and the message is
accepted (#2038). Each distinct id is resolved once and its path named once, at
the position the client first listed it, so array order still reads as
first-occurrence order. Refusing a repeat would punish a client for something
harmless, and naming a path twice would tell `claude` to read one file twice.
**The dedup happens after the bound is counted**, never before: the bound counts
elements, so 33 copies of one id is over bound and is refused. A client still
must not *rely* on deduplication — the receiver's answer to a repeat is defined
here, but sending each id once remains the conforming shape.

#### `request_attachment`

Direction **phone → binary** (inbound v2 control; not in `v1TypeSet` — an old
phone never sends one, and `IsKnownAppType` rejects it, which is the structural
bar against a v1 client pushing one into the application dispatch chain).
Declared by **#2052**.

**Nothing answers it yet.** The outbound stream that replies with the bytes has
landed (**#2053**) but ships **unwired** — nothing calls it — and the handler
that resolves a request and drives it is **#2054**. Sent today the frame reaches
no dispatch surface at all. This is the section's last unpublished
frame — it was named as a gap here (*"no such type exists in the daemon today"*)
so a client author would not invent a verb, and it is published ahead of its
handler for the same reason `attachment_chunk` (#1752) and `attachment_stored`
(#1895) were: the wire string is the contract, and a name chosen twice is a name
chosen wrong once.

**It names a conversation and an attachment, and nothing else.** **Every field is
client-asserted** — this frame travels in one direction only, so unlike
[`attachment_chunk`](#attachment_chunk) there is no leg on which its fields are
daemon-authored and no provenance to disambiguate.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation whose attachment is wanted. A **lookup key validated against the daemon's own registry before it resolves anything**, never a value trusted as sent — and **naming a conversation here is not authorization**. This is the only place in this section where a client names a scope, which is why the rule is stated rather than assumed: it is exactly the property [Naming a message's attachments](#naming-a-messages-attachments) publishes for `attachment_ids`, where **confinement**, not the id's shape or randomness, is what does the work. Obeys **[The `attachment_id` shape](#the-attachment_id-shape)** — the same lowercase-UUIDv4 rule, which is also what the daemon enforces for conversation ids. |
| `attachment_id` | string | The attachment wanted **within that conversation**: the client's own id, the one it repeated on every chunk of the upload. Obeys **[The `attachment_id` shape](#the-attachment_id-shape)**. **Not a capability** — knowing an id is not permission to fetch it, and the id is neither secret nor unguessable. |

**Both fields are always present** (no `omitempty`), so a decoder may rely on
both.

**Correlation rides `in_reply_to`, so there is no request-id key** — the decision
[`attachment_stored`](#attachment_stored) already took, and both terminals of
this leg follow it: the answering `attachment_chunk` frames and an
`attachment.stream_aborted` `error` alike name this request's envelope id. A
client matches the answer to the ask through the envelope, not through a payload
field.

**An absent or empty id resolves nothing.** Every key is optional to a JSON
decoder, so a truncated or hostile payload decodes to two empty strings rather
than to an error; the empty string is not a valid id under any shape this section
publishes, so it names no conversation and no attachment. A receiver must not
join it into a path — an empty component resolves to the **conversation directory
root**, not to an error — and must reject the frame rather than treat it as a
request for nothing.

**No bound is published here**, deliberately. This shape carries no count and no
length field, so there is nothing to allocate from, and the encrypted-frame cap
is the only byte limit that applies. Any limit the receiver puts on **concurrent
retrievals** is receiver-configured and unpublished, learned by being rejected —
the same posture `attachment.too_large` and `attachment.too_many_uploads` take on
the upload leg, and the #1752 rule against publishing a figure ahead of the code
that enforces it.

**Three answers**, and the reject vocabulary is the existing one rather than
anything minted here:

- **The file**, as a stream of [`attachment_chunk`](#attachment_chunk) frames
  correlated by `in_reply_to`, complete when `total_chunks` distinct indices have
  arrived. There is **no completion frame** — see **Retrieval, and its two
  terminal signals** above.
- **[`attachment.not_found`](#error-codes)**, for every request that yields no
  bytes: an unknown id, an id whose canonical shape is invalid, and an id
  resolving outside the named conversation's directory are **deliberately
  indistinguishable**, and the message is static and never echoes the requested id
  or the resolved path. Two distinguishable answers would make this verb a
  path-existence oracle for a traversal probe — which is the whole reason that
  code is merged.
- **[`attachment.stream_aborted`](#error-codes)**, if the daemon abandons a
  retrieval mid-stream. The client MUST discard everything accumulated.

**Sending this frame is not a capability, and neither is receiving an answer.**
Authorization is **pairing**, enforced structurally at the Noise IK handshake
exactly as for both existing legs; there is no per-verb gate on this one and none
is invented here. What bounds a paired but hostile client is confinement to the
conversation it named *and* the daemon's validation of that name — not the
secrecy of an id. Both fields are client-supplied strings and are **loggable only
after their shape is validated**, the rule this section already applies to
`filename`; the payload carries no content-bearing bytes, so once validated it is
safe to log whole. Nothing in it is claude-authored and nothing in it becomes
prompt content, so [§ Security model](#security-model)'s threat 1 does not land
here — the opposite of `send_message`'s `attachment_ids`, which does reach
`claude`.

```json
{
  "id": 91, "type": "request_attachment", "ts": "...",
  "payload": {
    "conversation_id": "9d4e7a21-8c05-4f3b-b6e2-1a7c9e30d5f4",
    "attachment_id": "7c1d5e92-4a30-4b8f-9e21-6d4c3b0a8f55"
  }
}
```

### Session settings (v2)

A paired client sends `set_session_settings` to change one session's **per-session settings** — its model, reasoning effort, permission mode, and YOLO (bypass-permissions) — and the daemon confirms with `session_settings_updated` (#597 Phase 3, #844, permission mode #1687). This section defines only the wire vocabulary; the handler that intercepts the request — gating on the negotiated `interactive` capability, validating, and persisting the change via `sessions.Pool.UpdateSettings` (#840) — is sibling #845.

#### `set_session_settings`

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session manager before `dispatch.Route` — it is not a `dispatch.Route` handler (like [`modal_answer`](#modal-v2) / [`new_session`](#new-session-v2)).

`session_id` is the **addressing key** — it names the session to change, matching the `sessions.Pool.UpdateSettings(id, …)` seam.

A client learns it from [`session_settings`](#session_settings) below, the request/response route it can drive at any time, or observes a change to it on the [`session_transition`](#interactive-events-v2-capability-gated) marker. **The marker alone is not a sufficient source**, and this document used to say it was. The daemon fires `session_transition` only on a clear or an idle eviction, never on session creation, so a client that had done neither never learned an id and could not write at all. That was the whole of `pyrycode-desktop#491`: the run-configuration UI rendered and stayed permanently inert. Drive `request_session_settings` when you need the current value; treat the marker as an update to it.

The four settings fields are **optional** and encode a *presence contract*: a field that is **absent** from the payload means "leave that setting unchanged", while a field that is **present** — including at its zero value (`""` for `model`/`effort`, `false` for `yolo`) — means "set it to this value". An **absent** `yolo` can therefore never be read as a sent `false`, and an absent `model` can never be read as an instruction to clear the stored value. (On the wire this is `omitempty` over pointer fields: an absent key, not a literal `null`.)

`permission_mode` is the one field for which **present-at-`""` is not a settable value**. `model` and `effort` accept `""` as "run at claude's own default" — a real value. The default *posture* is a nameable mode (`"default"`), so an explicit `""` names nothing and is rejected.

| Field | Type | Meaning |
|---|---|---|
| `session_id` | string | The session to change. Always present. |
| `model` | string (optional) | New model; absent = leave unchanged. This writes the per-session **override**, not what claude announced for the turn — see [`model_announced`](#model_announced). |
| `effort` | string (optional) | New reasoning effort; absent = leave unchanged. |
| `yolo` | boolean (optional) | New bypass-permissions (YOLO) state; absent = leave unchanged. An absent `yolo` never enables bypass. |
| `permission_mode` | string (optional) | New permission mode; absent = leave unchanged. One of `default`, `acceptEdits`, `plan`, `auto`, `dontAsk`. Anything else — including `""` and `bypassPermissions` — is rejected. **Must not be sent together with `yolo`.** |

##### `permission_mode` (#1687)

Claude has six permission modes, and `yolo` can spell only two of them. This field carries the other four, so a client can offer what claude actually supports instead of an on-or-off switch.

| Mode | What it does |
|---|---|
| `default` | Prompts before anything dangerous. |
| `acceptEdits` | Accepts file edits without asking. |
| `auto` | A model classifier approves or denies each prompt. Only some models support it — read `supports_auto_mode` on the model list and grey the option out rather than asking. |
| `plan` | Analysis only, runs no tools. |
| `dontAsk` | Never prompts; denies anything not already approved. |

Two rules a client must build against, both enforced at the wire boundary:

- **The field joins `yolo`, it does not replace it.** `yolo` keeps its meaning and is not going away in this version; a client that only ever sends `yolo` keeps working unchanged.
- **A frame carrying both `permission_mode` and `yolo` is rejected as malformed**, whatever the two say. They are two spellings of one posture — `yolo: true` is `bypassPermissions`, `yolo: false` is `default` — so a frame carrying both is redundant or contradictory. The daemon refuses rather than picking a winner, so there is no precedence rule to get wrong: letting the mode win would silently downgrade an escalation, and letting `yolo` win would grant one from a frame that said `false`. **Send one or the other, never both.**

**`bypassPermissions` is rejected on this field.** The bypass posture stays reachable only through `yolo: true`, so it keeps exactly one spelling on the wire. Note the asymmetry with the read half below, which *does* report `bypassPermissions` when the session is in it — a client labels its menu from the reported value and sends `yolo` to change that particular posture.

A rejected value produces the same `protocol.malformed` reply as any other malformed request, echoing no part of what was sent, and **nothing is persisted** — the check runs before the daemon touches its session registry.

Example (a client switching a session into plan mode):

```json
{
  "id": 813, "type": "set_session_settings", "ts": "...",
  "payload": { "session_id": "sess-a", "permission_mode": "plan" }
}
```

Example (a client changing only the reasoning effort — `model` and `yolo` omitted):

```json
{
  "id": 811, "type": "set_session_settings", "ts": "...",
  "payload": { "session_id": "sess-a", "effort": "high" }
}
```

#### `session_settings_updated`

Direction **binary → phone** (outbound). The daemon's confirmation that a `set_session_settings` was applied. It carries only the `session_id` it confirms; the request↔reply correlation rides `in_reply_to` (#845). The client already knows what it sent, so the reply does not echo the applied settings.

| Field | Type | Meaning |
|---|---|---|
| `session_id` | string | The session the change was applied to. |

Example:

```json
{
  "id": 44, "type": "session_settings_updated", "ts": "...", "in_reply_to": 811,
  "payload": { "session_id": "sess-a" }
}
```

#### `request_session_settings`

Direction **phone → binary** (inbound v2 control). Intercepted by the v2 session manager before `dispatch.Route`. Interactive-capability-gated: a non-interactive conn is fully inert and gets no reply, so it cannot learn whether a session exists.

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation being asked about. **Empty or absent = no conversation named**, which names no session and so is answered with the all-zero reply. |

Three answers, and all three are a `session_settings` reply — **never an error frame**:

- A `conversation_id` naming a conversation this daemon hosts, with a live bound session, is answered with **that session's** run configuration.
- A `conversation_id` naming a conversation it does **not** host — or one bound to no live session — is answered with a `session_settings` whose every field is at its zero value. That is not an error dressed up as a reply: `session_id: ""` is already the defined "the daemon has no session to address" answer, so the reply shape stays constant and a client parses one thing rather than branching on two. Both unresolvable cases produce the identical reply, so it distinguishes neither from the other.
- An **empty or absent** `conversation_id` is answered with that same all-zero reply. A request that names no conversation names no session, so there is nothing for the daemon to describe — and there is nothing the client could be configuring either: `send_message` already refuses an unknown conversation with `conversation.not_found` and an unbound one with a retryable `server.binary_offline`, and never falls through to a shared session. `pyrycode-desktop#491`'s actual case — a sheet opened on a conversation the user has never sent a message in — stays fixed, because `create_conversation` mints **and binds** a dedicated session before it replies, so a conversation is addressable from the instant the client learns its id.

**The field selects which session the reply describes.** It is not a populated/all-zero switch over a daemon-wide answer: the reported `session_id` and the reported values come from one session, the one bound to the named conversation — see the `session_settings` scope note below.

Answered by `session_settings` below, correlated by `in_reply_to`.

```json
{
  "id": 812, "type": "request_session_settings", "ts": "...",
  "payload": { "conversation_id": "c1" }
}
```

#### `session_settings`

Direction **binary → phone** (outbound). The current run configuration: which session to address, what is in force on it, and how full its context window is. All fields are always present (no omitempty), so **every zero value is a real answer rather than an absence**.

This is the **read half** the settings cluster shipped without. Before it, a client scraped these values off [`screen_snapshot`](#screen_snapshot), which still carries copies of them. Do not do that in new clients, and prefer this route in existing ones: `screen_snapshot` is a picture of the terminal, and a daemon running the stream-json interactive runner has no terminal, so it answers `server.binary_offline` and takes the settings — which have nothing to do with a terminal — down with it. This route is gated on nothing but the interactive capability and answers on both runners.

| Field | Type | Meaning |
|---|---|---|
| `session_id` | string | The session to address a `set_session_settings` to. **Empty string = the daemon has no session to address**; a client must treat the settings as read-only rather than sending an empty id, which would be rejected. |
| `model` | string | Model override in force; **empty string = inherited daemon default** (no override). This is the **override**, not what claude announced for the turn — see [`model_announced`](#model_announced). |
| `effort` | string | Reasoning-effort override in force; **empty string = inherited daemon default** (no override). |
| `yolo` | bool | Bypass-permissions (`--dangerously-skip-permissions`) on/off; **`false` = permissions enforced** (the fail-safe default). |
| `permission_mode` | string | The posture in force (#1687) — one of the five write-half modes, or `bypassPermissions`. **`""` means no session resolved**, not an unnamed mode: it occurs only in the all-zero reply, alongside `session_id: ""`. This is the value a client labels its permission menu from, rather than the request it last sent. |
| `used_tokens` | int | Context-window tokens consumed by the latest turn. `0` against a non-zero `window_tokens` is a genuine fresh session. |
| `window_tokens` | int | Context-window size. **`0` = the usage reader is unwired**, not an empty window — do not render a percentage from it. |

`permission_mode` and `yolo` always agree, because the daemon stores them so they cannot disagree: a session in bypass reports `permission_mode: "bypassPermissions"` **and** `yolo: true`. So this reply can name a posture the write half refuses to accept on its own `permission_mode` field — that is deliberate, and it is why the read half exists: the menu's label comes from the daemon's state, not from what the client last sent.

Scope: the values describe the session bound to the **conversation the request named**, and `session_id` names that same session, so a client reads and writes the same place. The whole set is keyed by conversation and moves as one — the daemon resolves the id and reads that session's settings under a single acquisition, so no field can describe a session another field does not, even against a concurrent idle eviction. A request that resolves to no session gets every field at its zero value; it is never answered with some other session's.

Example:

```json
{
  "id": 45, "type": "session_settings", "ts": "...", "in_reply_to": 812,
  "payload": {
    "session_id": "sess-a", "model": "opus", "effort": "high", "yolo": false,
    "permission_mode": "default", "used_tokens": 12480, "window_tokens": 200000
  }
}
```

## Reconnect / Backfill semantics

Every reconnect performs a fresh Noise_IK handshake (there is no session resumption in v2), and on every (re)connection the daemon brings the client back to **current truth**. Reconciliation runs in **two modes, keyed on the kind of data — not on how long the client was away**. The axis of difference is data type, not outage length: the interactive event stream keeps a cursor backfill; control state is always a cheap current-state snapshot.

**Mode A — the interactive event stream → cursor backfill (replay of past events).** The mid-turn live stream reconnects by *replaying past events* from a cursor. **Mode A is exactly one mechanism, and it carries no history and no transcript content** (#2090):

- `hello.last_event_id` drives the #647 mid-turn event-ring replay, with the `resync` marker as the snapshot-fallback when the advertised cursor has aged off the bounded per-conversation ring — see [Reconnect replay & resync (consumer, #647)](#reconnect-replay--resync-consumer-647). All replay frames ride inside `noise_msg`. Three properties bound what this recovers, and a client that plans around it must build for all three:
  - **In-memory.** The event ring lives in the daemon process and **does not survive a daemon restart**. It does survive a supervised claude-child respawn and any number of phone reconnects, because the daemon stays up across both. Across a restart the phone's cursor names an id the daemon never issued, which classifies as a gap and answers with `resync`.
  - **Bounded per conversation.** Each conversation retains at most `MaxEventsPerConversation` = **1024** events, and the bound is **not a plain FIFO age-out**: the oldest `assistant_delta` is evicted first (deltas are lossy and coalescable per ADR 025) and control-class events are retained in preference. A position that has fallen off the window answers with a single `resync` marker rather than a partial, gap-ful replay.
  - **Daemon-resolved conversation only.** The daemon replays the conversation **its own** cursor names, never one the client names — `last_event_id` is a position, not an address, and a phone can never reach another conversation's events.

**There is no bulk-history backfill.** A client that wants past messages the ring no longer holds has no verb to ask for them and receives none; the answer is a full reload of the conversation the `resync` marker names. Real history — a daemon-owned log rather than a replay window — is #2091's, and does not exist today.

**Mode B — control state → current-state snapshot (the reconcile-on-connect rule).** Control state reconnects by *re-asserting current truth*, replaying no past events. This is the single written contract every client builds against:

- **Reconcile on connect.** On any (re)connection the daemon brings the client to **current control truth** — the still-outstanding modal (#877), the current queued backlog (#878), every still-outstanding clarifying-question batch (#1979/#1980), the retained slash-command menus (#2006/#2007), and the retained background-task rosters (#2078/#2079) — and **replays no past control events**. The still-outstanding `modal_shown` (original `modal_id`) is unicast, a `queue_state` snapshot is unicast for every non-empty backlog, a `question_shown` is unicast for every outstanding batch, each carrying the original `question_batch_id`, a [`slash_command_list`](#slash_command_list) is unicast for every conversation whose bound session currently holds one, and a [`background_task_roster`](#background_task_roster) is unicast for every conversation whose bound session has reported one. The exactly-once end-to-end behaviour is exercised in #829/#903/#904, the slash-command menu's late-connect delivery in #2009, and the background-task roster's in #2080.
- **Match-and-replace by stable id, applied idempotently on every connect.** Reconciliation is keyed on stable identity — `modal_id` for a modal; `conversation_id` plus the per-message `queued_msg_id` for the queue backlog; `question_batch_id` for a question batch; `conversation_id` for a slash-command menu; `conversation_id` for a background-task roster — so the same connect-time re-assertion is safe to apply every time:
  - a re-delivered `modal_shown` for a **known** `modal_id` **updates the existing modal in place** and **never double-shows**; likewise a re-sent `queue_state` for a known `conversation_id` **replaces** that backlog view rather than appending, a re-sent `question_shown` for a known `question_batch_id` **replaces that batch in place**, a re-sent `slash_command_list` for a known `conversation_id` **replaces that menu in place**, and a re-sent `background_task_roster` for a known `conversation_id` **replaces that roster in place** — it is snapshot-shaped full state, so replacing is the same rule the live lane already asks for;
  - an id the client has **already resolved** is a **no-op**;
  - control state the daemon does **not** re-assert after a fresh handshake is **gone** (reset-on-reconnect): each reconnect is a fresh Noise_IK handshake, so the client **resets** its control state and rebuilds it from whatever the daemon re-asserts — anything resolved while the client was disconnected simply does not reappear.

The two modes are **complements, not alternatives**: on one and the same connect a client takes a cursor backfill for the interactive event stream and a current-state snapshot for control state. The mechanism is chosen by *what kind of data* it carries, never by *how long the client was away*. Mechanism internals live in #877 (modal reconcile), #878 (queue reconcile), #1979 (question reconcile), #2006 (slash-command reconcile) and #2078 (background-task-roster reconcile); the answer-once security invariants are stated in [§ Modal](#modal-v2) and [§ Question](#question-v2), and are unchanged under re-delivery.

The list above names **five** frames while the daemon runs **six** reconciles. The missing one is `model_list`'s (#1863/#1867), which shipped and never reached this file; § [`model_list`](#model_list)'s *"no connect-time snapshot today"* is stale for the same reason. Both are left standing deliberately — they belong to the sibling family and are not this section's to fix — so a client reading the count as complete would be wrong about exactly one frame.

## Error codes

Application-level error codes (carried in `error` envelopes inside `noise_msg` payloads) are unchanged from v1, with these additions:

| Code | Retryable | Notes |
|---|---|---|
| `noise.handshake_failed` | no | Reported only to local logs — wire-level handshake failure closes the WS with `4426` and no AEAD-sealed envelope can be sent. Included here for completeness. |
| `noise.rekey_failed` | yes | The peer's `rekey_request` was rejected (e.g. rate-limited) or the subsequent handshake didn't complete; sender may retry after a backoff. |
| `session.not_found` | no | The `set_session_settings` target `session_id` names no live session. Returned by the handler (#845). |
| `session.blocked` | no | Terminal — the daemon gave up delivering a conversation's queued backlog after repeated failures (msgqueue give-up, #1000). Carried in a `session_error` frame, not an `error` envelope; the emitting producer is #1008. A client attaches it to the `conversation_id` and MUST NOT retry (contrast the transient `server.binary_busy`). |
| `attachment.invalid_chunk` | no | An `attachment_chunk`'s framing claims are inconsistent or out of range: a duplicate `index`, an `index` outside `[0, total_chunks)`, or a `total_chunks` disagreeing with the stream's earlier chunks (#1741). The receiver discards the whole in-flight stream; resending the same frames reproduces it, so the repair is to re-chunk. See [Attachments](#attachments). |
| `attachment.integrity_failed` | no | The assembled bytes do not match the declared `sha256`, **or** the assembled length does not match the declared `size` (#1741). One code for both mismatches: a client's repair for either is to re-derive the metadata from the file and re-upload, never to retry the same bytes against the same claims. |
| `attachment.too_many_uploads` | yes, after a backoff | The receiver's bound on **concurrent in-flight uploads** is hit (#1741). The one bound in this family that clears on its own — it clears when *other* uploads finish, so a client MUST back off rather than resend immediately: an immediate retry both fails and consumes the capacity it is waiting for. Contrast the permanent `attachment.too_large`. |
| `attachment.too_large` | no | **One upload** exceeds the receiver's per-upload byte bound, detected either from the declared `size` on the first chunk or from accumulated bytes later (#1741). Permanent for that file — the same bytes fail the same way every time, so a client shrinks the file rather than retrying. Not `message.too_long`, which says one **envelope** was oversized. |
| `attachment.storage_failed` | yes, after a backoff | A verified attachment could not be written to the host (#1743, surfaced by #1744). Carries a **static** message — never the host path and never the underlying filesystem error, either of which discloses the daemon's layout. The host condition may not clear at all, so a client MUST back off and MUST NOT hot-loop the re-upload. |
| `attachment.not_found` | no | An attachment id did not resolve to a file inside the named conversation's directory. **Both verbs, as of #2036**: a [`request_attachment`](#request_attachment) (declared by #2052, answered by #2054), and a [`send_message`](#naming-a-messages-attachments) whose `attachment_ids` names an id that does not resolve under the message's own conversation. One code rather than a second minted for the message path, because the predicate, the retryability, the static message and the client's repair are identical, and the merge argument below applies with *more* force to `send_message` — the cheaper probe of the two. **Deliberately indistinguishable** across an unknown id, an id whose canonical shape is invalid, and an id resolving outside that directory — a disclosure decision, not an imprecision: two codes would make the asking verb a path-existence oracle for a traversal probe. Nothing is lost by the merge, because all of those outcomes mean the same thing to a client — re-list the conversation's attachments — so there are no sub-cases to branch on. The message is static and never echoes the requested id or the resolved path; where a request names **several** ids it also **never says which one failed**, since a per-id answer would rebuild the oracle as a batch probe. |
| `attachment.stream_aborted` | yes, after a backoff | The daemon abandoned a retrieval **mid-stream** — the stream is #2053's and the `error` frame reporting it is #2054's to emit. An `error` envelope correlated via `in_reply_to` (naming the [`request_attachment`](#request_attachment) that asked), never a second attachment frame. The client **MUST discard everything accumulated for that transfer** and MUST NOT present the partial bytes as the file — with no completion frame this is the only negative signal the stream has. A re-request re-runs the same resolution work, so retry after a backoff, never immediately. |

WS close codes used at the transport layer:

| Code | Meaning | Sent by |
|---|---|---|
| `1000` | Normal closure | either |
| `1011` | Server error | either |
| `4401` | Unauthorized (bad device token) | binary (forwarded by relay) |
| `4404` | No server with that server-id | relay |
| **`4408`** | **Idle-session timeout (no inbound frame within the idle window; encrypted session swept)** | binary (forwarded by relay); v2-new |
| `4409` | Server-id already claimed | relay (to a binary) |
| **`4413`** | **Push-queue byte ceiling exceeded (session torn down to free retained payload bytes)** | binary (forwarded by relay); v2-new |
| `4421` | Protocol mismatch (unknown `type`, bad `v`, malformed envelope) | either |
| **`4426`** | **Noise handshake failure** | either; v2-new |
| **`4429`** | **Per-server phone cap hit (too many phones for this server-id)** | relay |

`4408` is sent by the binary (forwarded by the relay) when an open v2 session receives no inbound frame within its idle window and the daemon's in-repo idle sweep tears it down — dropping the session's Noise cipher states and its armed rekey timer rather than letting them linger up to the 1-hour rekey interval. It echoes HTTP 408 (Request Timeout). The relay↔binary leg is a single multiplexed WebSocket with no per-connection disconnect frame, so a phone that drops or backgrounds (phones close-on-background, then push-to-wake) is only detectable by inbound-frame silence. A phone whose session was swept simply re-connects and performs a fresh Noise handshake; sending `4408` to a conn whose phone has already gone is a harmless relay no-op.

`4413` is sent by the binary (forwarded by the relay) when one session's server→phone push queue exceeds its retained-payload-byte ceiling and the daemon tears that session down rather than hold the bytes until the idle sweep. It echoes HTTP 413 (Content Too Large). Two things can reach the ceiling: a multi-minute relay outage during a live turn, where the daemon keeps producing never-droppable control events (`tool_result`, `tool_use`, `turn_state`, `turn_end`) that nothing can drain; and a single `request_debug_bundle` whose archive is large enough that its `debug_bundle_chunk` stream alone crosses the ceiling. It is strictly per-session — one conn hitting it never affects another. No `error` envelope accompanies it: the ceiling is typically reached precisely because the transport is down, so sealing one would burn a Noise send-nonce on a frame that cannot arrive and gap the phone's receive nonce. **A phone whose transport was down therefore never sees the `4413` close frame at all** — it learns on its next inbound frame, which lands on a session the daemon no longer has; this is the same property `4408` has, and it fires under the same conditions. Recovery is a fresh Noise handshake, which is also what restores what was dropped: the re-handshake replays the missed turn events from `hello.last_event_id` and re-asserts outstanding modal and queue state. A debug bundle is not replayed — the operator re-requests it after reconnecting, and an archive large enough to trip the ceiling will trip it again.

`4426` is sent at the WS-close layer because the AEAD channel doesn't yet exist when a handshake fails — there is no shared key under which to send an `error` envelope.

`4429` is sent by the relay when a phone tries to register against a server-id that already has the maximum number of phones connected (relay `ErrPhonesAtCap`). It is not a transient error that a fast retry clears: the cap is still full on the next attempt. A client that receives `4429` MUST back off and stop reconnecting rather than hot-loop the connect. The condition clears only when another phone for that server-id disconnects.

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

The relay cannot decrypt any content. Even if the relay's entire process memory is dumped, no plaintext message text, no `cwd` paths, and no conversation names are recoverable. The device-token is a partial exception: the relay still requires the opaque `x-pyrycode-token` header (see [Phone → relay → binary](#phone--relay--binary)), so that header value is transiently in relay memory at upgrade time. The relay never parses, logs, or persists it, but a memory dump taken during a phone upgrade would still contain it. Dropping the header entirely is a not-yet-implemented follow-up.

**Residual risk:** Low. A malicious relay can still:

- **Refuse to forward.** This is a denial of service, not a confidentiality breach. The phone surfaces "binary offline" and retries.
- **Forward selectively or with delay.** Same — DoS class.
- **Forge `conn_id` mappings** to misroute frames between phones. Effect is that a frame sent by phone A may reach phone B's binary path, but phone B is talking to a different binary or no binary at all — the misrouted frame will fail AEAD verification on the receiving binary's CipherState and the connection is torn down. No plaintext leaks; no impersonation succeeds.
- **Observe metadata.** Connection counts, frame sizes, frame timing, device names, server-ids. v2 does not provide metadata privacy beyond what TLS provides on the network path. (Padding and timing obfuscation are out of scope for v2.)

**Residual risk before v2 closes the gap:** High. **Residual risk after:** Low.

#### 4. Token leak via phone — `severity: medium`, `mitigation: per-device revocation` (improved in v2)

v1 mitigations apply unchanged. **v2 adds:** the relay no longer parses or forwards the device-token — token validation moved into the AEAD-sealed `hello` early-data, so the relay never reads it. The `x-pyrycode-token` header is still required and still passes through the relay opaquely (see [Phone → relay → binary](#phone--relay--binary)), so a relay compromise at upgrade time can still capture that opaque token from process memory; what changed is that the relay no longer needs to inspect it, and dropping the header entirely is a not-yet-implemented follow-up. (A compromise of the phone OR the binary still exposes plaintext tokens, since both endpoints handle them.)

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

- Major version (`v1`, `v2`, …) appears in the `v` field of every inner frame. It does **not** appear in the URL path: the relay routes only `/v1/server` and `/v1/client` and never renames the route across protocol versions, because it is protocol-agnostic and the version lives in the frames, not the route.
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
binary -> relay: WSS upgrade /v1/server
  headers: x-pyrycode-server: 8f7e..., x-pyrycode-version: 0.11.0
relay accepts, holds the connection.

# Phone connects:
phone -> relay: WSS upgrade /v1/client
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

- `2026-09-05`: **Corrected the published claim that `last_seen_ts` drives a bulk-history backfill** (#2090). It drives nothing. `LastSeenTS` occurs in exactly two Go files — its declaration on `HelloClientPayload` and that struct's round-trip test — with **no consumer in `internal/relay` or anywhere else**, so the field is decoded and dropped and a `hello` carrying it is answered byte-for-byte as one omitting it. The failure was **silent**, which is what made the document the whole defect: the claim was read as a working daemon capability during a 2026-09-04 desktop investigation and used to size client work as *"the daemon half already exists"*. **The field is not removed or deprecated** — it is still accepted vocabulary, a decoder rejecting it would be wrong, and its `omitempty` declaration is untouched; every surviving mention now names it as accepted-and-ignored. Whether real history should exist, and what would source it, is **#2091's** decision, already settled there as a **daemon-owned log** rather than claude's on-disk transcripts. **§ Reconnect / Backfill semantics needed more than the one deleted bullet**, because that bullet was what made Mode A's framing true: the section's intro, Mode A's heading and its closing two-modes paragraph each asserted "bulk transcript content" independently, and all three are corrected together so no surviving sentence describes Mode A as history. **Mode A keeps the name "cursor backfill"** — a rename was rejected, because § [`model_list`](#model_list) and § [`slash_command_list`](#slash_command_list) each cite *"Mode A, a cursor backfill"* in live prose, both accurately describe the `last_event_id` ring replay, and both are left untouched; a rename would have orphaned them. What Mode A publishes now is **one mechanism** and its three bounds, stated because a client plans around each: the ring is **in-memory** and does not survive a daemon restart (though it survives a claude-child respawn and any number of phone reconnects); retention is **bounded at `MaxEventsPerConversation` = 1024 per conversation** and is **not a plain FIFO age-out** — the oldest `assistant_delta` is evicted first and control-class events are retained in preference — with `resync` as the answer once a position falls off; and replay is scoped to the **daemon-resolved** conversation, so `last_event_id` is a position and never an address. The two-mode split itself is unchanged, keyed on data class rather than outage length. The `hello` example block and the § Worked example wire trace **keep the key**, with § `hello`'s prose — which documented `last_event_id` at length and `last_seen_ts` not at all — gaining the paragraph that owns the inertness for both. `internal/protocol`'s `HelloClientPayload` doc comment carried the same false claim and is corrected in the same pass, **comment-only**. Earlier changelog entries are historical and were left untouched, as are § Reconnect / Backfill semantics' deliberate note about the missing sixth (`model_list`) reconcile, which belongs to a sibling family. The desktop repo's mirror claim is pyrycode-desktop#1068 and is not this repo's to fix. Documentation only — no wire field added or removed, no fixture, test or behaviour changed.
- `2026-09-05`: **`background_task_roster` now has a connect-time snapshot, and its empty case is published** (#2080). #2077 landed the per-session retention, #2078 the reconcile and #2079 the daemon-side enumeration; this slice proves the chain end to end and publishes the contract. The frame **joins [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)' Mode B list**, keyed on `conversation_id`, and § [`background_task_roster`](#background_task_roster) gains the **Reconcile on (re)connect** note its Mode B neighbours carry. **The empty case is the part a client author cannot infer, and it is the reverse of [`slash_command_list`](#slash_command_list)'s rule in this same document**: a reported-**empty** roster is reconciled as an explicit `"tasks": []` snapshot, while a roster that was **never reported** is simply absent — so absence means *nothing has been reported*, not *nothing is alive*, and the two must not be collapsed. That is the statement that stops a reader generalising from the neighbour and rendering a spinner forever on a conversation that has nothing alive and never will. The section's blanket **`event_id` claim is qualified** rather than left to mislead: the live-lane frame carries one, the reconciled frame deliberately carries none, is kept out of the #647 replay ring, advances no cursor and is inert to cursor-based dedup. **The proof is hermetic and its non-vacuity is the acceptance criterion, not a nicety** — two producers emit this exact variant, so a test that merely waits for a frame passes against a completely dead reconcile; #2080's e2e is therefore verified against the one-line mutant that unsets the enumeration seam, and reports the frame count on the minting connection beside the observing one so a miss names which producer failed. It also pins what the reconciled frame does **not** do: no turn opens or closes on the receiving connection, and the connection sends nothing to trigger the frame. `dropped_tasks` is driven **non-zero** on purpose, since a fixture whose count is always `0` cannot distinguish a carried count from one that was never populated. **Two things are deliberately left standing**, both the sibling family's: § [`model_list`](#model_list)'s *"no connect-time snapshot today"* and its absence from the Mode B list are still stale (#1863/#1867 shipped that reconcile and never reached this file), which is now **stated in the section itself** rather than left implied — the list names five frames while the daemon runs six. No behaviour changed; the only production change is a fake-claude test rider that scripts claude's mid-turn `system`/`background_tasks_changed` line, which no existing suite enables.
- `2026-09-03`: **Corrected where the retrieval leg's fields come from** (#2053), alongside the outbound stream that emits them. The section said `filename` and `mime_type` *"arrive attacker-chosen at upload time, are stored, and are echoed back verbatim on the retrieval leg"*, and each clause was wrong in its own way: **`mime_type` is not stored at all** — `Intake.Receive` stores the bytes under the sanitised filename and discards the declared media type outright, while the declared `size` and `sha256` are checked against the assembled bytes at admission and then dropped — and **`filename` is stored *sanitised***, so what the retrieval leg carries is one safe path component and never the client's own string. The claim was restated in three places (the trust paragraph, the `mime_type` field row, and [`attachment_stored`](#attachment_stored)'s *"claims inbound and laundered client input outbound"*), and a reader acting on any of the three went looking for a stored media type that is not there. All three now say what actually happens: the daemon **derives** `size`, `sha256` and `mime_type` from the stored bytes, sniffing the media type **from the content** so a file whose name and client-declared type disagree with its bytes is described by its bytes. **This improves provenance, not trust, and the correction is deliberately narrow about that.** The value becomes genuinely daemon-authored rather than an attacker-chosen string handed back — and it is still computed from bytes an attacker chose, so a *sniffed* `text/html` is exactly as dangerous to render as a declared one. The **MUST NOT dispatch on `mime_type` in any way that grants the content privileges** rule and the sanitise-before-rendering rule on `filename` both survive the edit unchanged; only the sentences saying where the values come from were wrong. Also updated: the two status lines calling #2053 future work. The stream has landed and **ships unwired** — nothing calls it, exactly as #812 shipped `StreamBundle` ahead of #813 — so **nothing answers `request_attachment` yet** remains true and remains, with #2054 the handler that will join them.
- `2026-09-03`: **Published the retrieval request verb** (#2052), closing the one hole this section named out loud — *"the retrieval request verb ... (no such type exists in the daemon today)"* — and with it the last unpublished frame of the attachment transfer. A client that wanted a file back had nothing to send, and [pyrycode-desktop#687](https://github.com/pyrycode/pyrycode-desktop/issues/687) was parked on exactly that question. [`request_attachment`](#request_attachment) is that frame, published in both places a frame is published, with a **three-column** field table rather than [`attachment_stored`](#attachment_stored)'s four: this one travels in **one direction only**, so every field is client-asserted and a provenance column would repeat a single value where [`question_answer`](#question_answer)'s varies. **The name was fixed here rather than left to the handler**, because a wire type string *is* the contract — the client writes its sender against the published name, and a name chosen twice is a name chosen wrong once. It follows the three inbound *ask the daemon for X* verbs already on this wire (`request_snapshot`, `request_debug_bundle`, `request_session_settings`) instead of inventing a fourth idiom. **It names a conversation and an attachment and nothing else**, which this section had already committed to: [`attachment_chunk`](#attachment_chunk) carries no `conversation_id` because an upload lands in the conversation the authenticated session is already on, so naming one there would only let a client steer bytes elsewhere — while a retrieval must be able to say which conversation's file it wants. That makes this **the only frame in the section where a client names a scope**, so the safety is published with the field rather than left to its first implementer: the `conversation_id` is a **lookup key validated against the daemon's registry before it reaches a path join**, never a value trusted as sent, and **naming a conversation is not authorization** — the [`attachment_ids`](#naming-a-messages-attachments) rule that **confinement**, not an id's shape or randomness, does this work. Both ids obey the existing [`attachment_id` shape](#the-attachment_id-shape) rather than a new rule. **Correlation rides `in_reply_to`, so the payload carries no request-id key** — `attachment_stored`'s decision, and the one the code already owed an answer for: the committed retrieval chunk fixture rides `in_reply_to` while its test recorded the question as open, so inventing a request-id key here would have left a landed fixture describing a different scheme. That fixture's `91` is now the envelope id of a committed `request_attachment` fixture, so the pair describes **one retrieval** and the decision appears in bytes rather than only in prose. **An absent or empty id resolves nothing**, stated because the failure is silent and specific: every key is optional to a JSON decoder, so a hostile payload decodes to two empty strings, and joining an empty component addresses the **conversation directory root** rather than erroring. **No bound is published** and none is minted — the shape carries no count or length field, so there is nothing to allocate from, and any concurrency limit stays receiver-configured and learned by being rejected (#1752's rule). The reject vocabulary is the **existing** one, pointed at rather than restated, so `attachment.not_found`'s deliberate merge cannot be diluted by a second telling. **Nothing emits, accepts or dispatches the frame yet**: it is filed in the relay guard under a **pending-handler** label rather than as an inbound type (which the guard would fail with no handler) or as a push (which would be false for an inbound frame), the same filing `question_answer` / `question_refused` and `attachment_chunk` each carried before their own handlers landed. Declaration only — **no admission-time check**, which with the registry validation belongs to #2054, the handler; #2053 is the stream it answers with. The scope fence, the no-`conversation_id` argument, the retrieval paragraph and the two `attachment.*` error rows are repaired accordingly, their **#1746** cites split across #2052 / #2053 / #2054 — that ticket no longer exists as work, the repair this file made for #1744 and #1693.
- `2026-09-03`: **A message's stored attachments now reach `claude`** (#2038), the slice that delivers the user story the whole [Attachments](#attachments) family exists for. `attachment_ids` had been declared (#2036) and the id → path resolver built (#2037), with nothing joining them: a client could upload a file, name it on a message, and `claude` never heard about it. The daemon now resolves each named id **against the message's own conversation** and delivers a prompt carrying the user's text followed by the attachments' on-host paths and a direction to read them. The **path form, not an inlined content block**, is the decision on record since 2026-05-16 — once persistence puts the bytes on disk, inlining duplicates them into the transcript, and the accepted cost is one extra turn per attachment. Three things this section had deliberately left open are now **decided and published rather than merely implemented**: the **32-id bound is enforced** for the first time (it counts raw **elements**, before any deduplication, exactly as published); an **over-bound list is refused with `protocol.malformed`, not retryable** — refused rather than truncated, because truncation drops a person's files while the message ships looking complete, and reusing the malformed code rather than minting one because 32 is a contract a client knows *before* it sends; and a **repeated id is deduplicated on first occurrence** and the message accepted, since refusing punishes a client for something harmless and naming a path twice tells `claude` to read one file twice. The security posture is inherited rather than re-derived: every id goes through `attachments.ResolvePath`, so the **canonical-shape check happens before any path join**, and **confinement** — not the id's shape or randomness — is what keeps a documented non-capability from becoming one, discharged by validating the client-asserted `conversation_id` against the registry binding **before** any resolve. A named id that does not resolve refuses the **whole** message with `attachment.not_found`, whose static message still **never says which of the ids failed**. **No host path reaches the wire**: what a client reads back through [`queue_state`](#queue-v2) — on the enqueue push and on connect-time reconcile alike — carries the user's own text, enforced by the daemon's queue carrying the composed prompt as a separate delivery payload its snapshot type cannot project, rather than by a rule a consumer has to remember. And **no log line carries a filename, a path, or a shape-unvalidated id** at any level; the resolver's error, which formats a raw client-supplied id into its message, is discarded at the one adapter that ever holds it. That `claude`, handed a path, actually opens the file is a claim about a live model and is #2039's.
- `2026-09-02`: **Published which attachments a message carries** (#2036). The upload leg was complete — bytes reassembled, verified, filed and answered with [`attachment_stored`](#attachment_stored) — and **nothing on the wire said which message an uploaded attachment belonged to**, so a daemon would have had to infer the set from upload order or arrival timing. [`send_message`](#application-message-types) now carries an optional `attachment_ids`, published in both places this document publishes a field: the § Application message types row, whose Notes cell was empty, and § Attachments → [Naming a message's attachments](#naming-a-messages-attachments). Elements obey the existing [`attachment_id` shape](#the-attachment_id-shape) rather than a new rule, and the **bound is 32 ids per message, counting elements rather than distinct ids** — a published, **unchecked** number in the `MaxAttachmentChunkBytes` idiom (a client needs it *before* it sends), deliberately not the receiver-configured-and-unpublished idiom `attachment.too_large` uses. The bound is stated as contract clarity and bounded work, **not** as a DoS mitigation: the envelope cap already limits an unbounded list to ~1680 elements and a paired device may already spawn `claude` turns. **The empty case is the key omitted entirely** — what shipping v1-compatible clients already send — with `null` and `[]` also accepted and all three made **indistinguishable to any consumer**; the always-present-never-null encoding the four outbound slice-valued payloads carry is a shape precedent and not a placement one, and applying it here would have published "always present" for a key half the wire's population never sends. Security posture is published with the field rather than left to the consumer: every element is a **claim** that becomes both a **directory component** and **prompt content**, so § Security model's threat 1 lands on it and **one canonical-shape check answers both hazards**; elements are loggable only after validation; and **confinement to the message's own conversation** — not the id's shape or its randomness — is what keeps a documented non-capability from becoming one. `attachment.not_found` is **widened to span both verbs** rather than a code minted for this path, since the predicate, retryability, static message and repair are identical — and its static message now also never says *which* of several named ids failed, which would rebuild the path-existence oracle as a batch probe. Nothing produces, consumes or validates the field yet; #2038 does, and no code is published for an over-bound list because whatever first counts one owns that decision.
- `2026-09-02`: **Published the attachment upload's success reply and the `attachment_id` shape** (#1895), closing a gap this file previously named as one: *"No success frame is declared here, and a client must not invent one."* A client that uploaded was told what went wrong on each of the seven `attachment.*` failure paths and got **silence** on the path that worked. [`attachment_stored`](#attachment_stored) is that path's single positive terminal — the transfer completed, every claim on it was checked, the bytes are stored — published in both places a frame is published, with the four-column **provenance** table [`question_dismissed`](#question_dismissed) uses rather than [`attachment_chunk`](#attachment_chunk)'s three-column one, because every field here is daemon-asserted and that is the whole difference from the frame it answers. **Correlation rides `in_reply_to`**, matching `session_settings_updated` and the reject half of this same leg (`attachment.stream_aborted` is an `error` correlated the same way) — but the id it names is the chunk **whose arrival completed the transfer**, not the last one sent, and since chunks may arrive in any order **a client cannot predict which of its envelope ids that will be**. That is why `attachment_id` also rides the payload, and the two are published as non-redundant: the envelope field says which frame this answers, the payload says which transfer it concludes, and only the second is a value the client chose and can look up. The payload carries **that one id and nothing else** — no host path, no directory component, no stored filename, no `conversation_id`, no echoed `size` / `sha256` / `total_chunks`. The filename's exclusion is argued rather than assumed: the daemon folds it into one path component whose result is **neither unique nor an identifier**, so echoing it would hand a client something it cannot rely on; and keeping it out is also what makes this the one frame in the section **safe to log whole**, since it carries none of the three fields the section forbids logging. Leaking a host path here would have undone `attachment.storage_failed`'s existing prohibition from the other side. **The [`attachment_id` shape](#the-attachment_id-shape) is published for the first time**, and it was already decided rather than open — the receiver validates it as a **lowercase UUIDv4**: 36 bytes, `-` at 8/13/18/23, `4` at 14, one of `89ab` at 19. The field table's *"at most 64 bytes"* was a **ceiling for the envelope arithmetic and never the shape**, and read as permission to use any short string it cost a whole transfer to correct: nothing checks the shape at **admission**, so a non-canonical id is accepted, every chunk is transmitted, and the upload is refused only when the completing chunk reaches storage. **Lowercase is load-bearing rather than cosmetic** — the id becomes a directory name, and the lowercase-only alphabet is what keeps the id-to-directory mapping **injective on a case-insensitive filesystem**, which APFS is by default, so uppercase ids give two attachments one directory on macOS and their writes cross-contaminate with no symlink involved. Containment follows from the shape and **never from the length ceiling**, which accommodates `../../../../etc/passwd` several times over. **Nothing emits the frame yet** (the inbound dispatch is #1897, the end-to-end observation #1898), the same declare-then-publish sequencing #1752 → #1751 used twice already in this family. The § Attachments scope fence is rewritten accordingly and its two **#1744** cites repaired — that ticket was split into #1895 and #1897 and no longer exists as work, the repair this file made for #1693. The retrieval request verb remains the one thing the section does not publish (#1746). Declaration only: no producer, no consumer, no validator, and **no admission-time check** — that is #1897's, which owns the reject path and the code to answer with.
- `2026-09-02`: **`event_id` is now unique daemon-wide** (#2022), which changes the guarantee this file publishes and fixes a silent-mute defect the previous entry's own text had to work around. **What changed:** the daemon's event ring counted ids **per conversation**, each starting at 1, so the id spaces overlapped. A connection that reconnected advertising an **in-range** `last_event_id` for conversation A had its replay watermark clamped to A's newest id; when the daemon then rotated to conversation B — a `/clear`, or a switch — B's low ids fell at or below that watermark and **every one of them was dropped before reaching the wire**: no frame, no error, no `resync`, for the life of that connection. One ring-wide counter now assigns every id, so no future event in any conversation can carry an id at or below one already issued, and the drop guard is correct by construction rather than by a scope argument. **What a client must know:** `event_id` is still a `uint64`, still ≥ 1, and still strictly increasing within a conversation — but it no longer restarts at 1 per conversation, and within one conversation it is now **ascending rather than contiguous**, since other conversations take ids in between. A conversation's first id is normally far above 1. **A client keying a single scalar cursor on `event_id` — which is what § [Reconnect replay & resync](#reconnect-replay--resync-consumer-647) tells clients to do — was carrying the same defect and is fixed by this with no client change**; a client keying its cursor per conversation was already correct and is unaffected. **No wire shape, field, type or frame changed**, and no client is required to do anything. Corrected here: the [`event_id`](#envelope) and `envelope-id` field-table rows, § [Interactive events](#interactive-events-v2-capability-gated)' *Replay cursor* paragraph, § [`hello`](#hello-v2-specific-note)'s `last_event_id` paragraph, § *Reconnect replay & resync*' *Beyond the id space* bullet, and § [`slash_command_list`](#slash_command_list)'s *What is not a loss point* paragraph — the last of which stated the per-conversation counting as a live caveat, which is how the defect was found. **Statements about the ring's retention are untouched and still true:** `MaxEventsPerConversation`, the per-conversation event ring and its per-conversation eviction policy all still hold — retention is per conversation, the id space is not. **Deliberately not done:** the replay drop guard was not made conversation-aware. Unique ids remove the failure, and a second mechanism for the same failure would need the conversation plumbed from the emitter through the push queue to the drop site. Earlier changelog entries are historical and were left untouched, including the previous entry's account of what it left standing.
- `2026-09-02`: **Corrected § [`slash_command_list`](#slash_command_list)'s published claim that nothing emits the frame** (#2010) — #1860's correction for the sibling, one frame later, and for the reason that entry named when it deliberately left these statements standing: at that time the frame genuinely had no producer, and that reason expired with this family. **The frame is emitted**: #2001 added the mapping onto this wire shape, #2002 the frame-level byte bound inside it, #2003 the producer on the live interactive turn lane, and #2008 proves it reaches a connected client end to end — so the application-message-types row, § `slash_command_list`'s own paragraph and § [`question_shown`](#question_shown)'s precedent sentence now name those slices instead of **#1720**, a ticket that was split and no longer exists as work. **`dropped_commands` counts**, and this is the correction most likely to change what a client does, since the old text forbade the one arithmetic that now works: the decode's 128-entry cap (#1826) is the base, #2002's serialised-byte bound **adds its own drop on top rather than recomputing**, and both statements that denied it — the field-table row and the paragraph — are inverted, so `len(commands) + dropped_commands` now yields the menu's true size. **The sibling's shortcut is explicitly not transferred**: with two cutters a non-zero `dropped_commands` arrives beside *any* number of entries, so no "N of M" illustration is published and a short list is not evidence of a complete one. The **delivery window** is stated for the first time. What runs on a schedule is an **ask, not a delivery** — one `initialize` exchange per child spawn — and the live lane is **best-effort** at three loss points **re-derived from the code rather than copied**, because the three descriptions that existed disagreed: a report from any session that is not the active conversation's bound session is dropped, which loses the bootstrap child's inventory **unconditionally** and delivers no fresh inventory across a **rotation**; a busy session can refuse the frame at the fan-in; and no interactive connection may exist at that instant. The reconnect-replay dedup is **published as not a loss point** — it drops only what a connection already received in its replay, scoped to the conversation whose newest id its watermark was clamped from — which is a deliberate departure from `reconcileSlashCommandLists`' own doc block, whose enumeration counts it as one of three. This frame now has a **connect-time snapshot** (#2005 resolves, #2006 reconciles, #2007 enumerates, #2009 proves a late-connecting client receives it), so `slash_command_list` **joins § [Reconnect / Backfill semantics](#reconnect--backfill-semantics)' Mode B list** and the section gains the reconcile-on-connect note its Mode B neighbours carry, keyed on `conversation_id`; the four cases the snapshot does **not** cover are stated rather than left implied, and the live-lane frame's `event_id` is distinguished from the reconciled frame's deliberate absence of one. **Three things are deliberately left standing.** § `model_list`'s *"no connect-time snapshot today"* and its *"deliberately absent from the Mode B list"* are **both stale** — #1863 landed that reconcile and #1867 its enumeration, and neither ever reached this file — but they belong to the sibling family and are untouched here, which is why the Mode B list above names four frames while the daemon runs five, and why **no ordinal is published** for this frame. The Go comments carrying the same false claims are likewise untouched: the sibling paid for those separately (#1861/#1862), #2003 left this frame's standing on purpose, and no ticket owns them yet. And § [Attachments](#attachments)' sentence citing `model_list` and `slash_command_list` as declare-then-publish precedent is still true and was not swept. The changelog entries below are historical and were **left untouched** — #1860's own entry records which statements it left standing and why, and rewriting it would destroy that record. No behaviour, fixture or test changed.
- `2026-09-02`: **An answered question batch now resolves to an allow carrying the operator's choices** (#1991), the third and last of the batch's terminal outcomes and the one that finishes the family's `outcome` vocabulary. Given a batch id and the client's entries, the daemon validates them against its own parked batch, consumes the registry one-shot, allows claude's blocked `AskUserQuestion` call with an updated input, and broadcasts one [`question_dismissed`](#question_dismissed) carrying the new **`outcome: answered` / `source: remote`** pair. **`answered` is a sentinel and carries no chosen label**, which is this frame's sharpest rule rather than a naming preference: `options` entries carry no id and claude selects by `label`, so the natural implementation reports that claude-authored string and moves a frame published as daemon-asserted into the batch's trust tier — a client wanting the label reads it from the batch it already holds. It shares `remote` with the refusal, so **`outcome` is what separates the two** and `source` alone must not be read as "answered". **The reply shape is claude's own contract** (<https://code.claude.com/docs/en/agent-sdk/user-input>): the updated input pairs the original `questions` array, required for tool processing, with an `answers` object keyed by each question's **text** whose value is the chosen label — an **array** for a `multi_select` question and a **bare string** otherwise, decided from the parked question rather than from how many values arrived. **Both halves come from copies the daemon holds**, never from anything the client echoed back, and the `questions` half is claude's own bytes spliced out of the parked tool input rather than a re-marshal of the wire-shaped batch: `protocol.Question` tags `multi_select` where claude's tool input uses `multiSelect`, so a re-marshal hands claude a key it does not read — **silently**, since the call is allowed either way — and the parse drops keys it does not model. **Free text is carried verbatim** and no value is ever compared against the offered labels, since the contract permits free text anywhere. **A malformed answer resolves nothing**: an index out of range, a repeated or a missing one, an entry count unequal to the batch's question count, no value for a question, or more than one for a single-select question all reject the whole answer — no verdict, no broadcast, and the batch left outstanding for a corrected answer or for the no-answer backstop. That discharges the three Contract-bounds rows § `question_answer` had been carrying as gaps, the sharpest being the range check whose absence **panics**; the entry-count comparison runs first, so an arbitrarily long array is O(1) to reject and per-frame work stays bounded by the batch's 1–4 questions. **Two questions with identical text collapse to one `answers` key and that is not a reject branch** — keying by text is claude's contract, so rejecting would strand the operator with a batch they can never answer, which is worse than the contract's own ambiguity, and nothing about it is client-controlled. **The one place the refusal's shape does not carry over** is that claude's parked input is read *before* the one-shot is consumed, and a miss ends the call having done nothing: a deny needs no input, but an allow does, and consuming without a verdict would silence the deferred retire closure that owes every client its `unanswered` while leaving claude denied by the timer with no client told why. Validation likewise reads through a **non-retiring** look-up, so a rejected answer leaves the batch answerable. **Nothing is reachable from the wire yet** — the resolver seam is still nil at every construction site, so § `question_answer`'s *"resolved by nothing yet"* stays true, and #1986 is what wires it behind the per-device gate; **this reply shape has never been confirmed against a live claude**, and that confirmation belongs to #1986, where the path runs end to end. **The path logs nothing at all**, so no question text and no answer value can reach a record. **Live-prose corrections in the same pass:** the three #1990 forward references in § `question_dismissed` (intro, `outcome` row, `remote` carry-over row) are discharged, the *"landed vocabulary is two pairs"* paragraph now publishes three, and the **eight remaining live `#1985` cites** are re-pointed — the `question_dismissed` and `question_answer` rows in § [Application message types](#application-message-types), § Question's intro, § `question_answer`'s intro, its three Contract-bounds rows and its SECURITY paragraph — plus that table's preamble, which read *"none are enforced anywhere"* and is now false for three of its four rows. The dated entries below are historical and stay untouched. **No live count moved**: no `#### ` heading was added or removed, so the three sentences reading **fifteen** stay checkable by counting `turn_state` through `model_announced`, and § `unrecognized_message`'s **five** is untouched.
- `2026-09-02`: **A refused question batch now resolves to a deny that tells claude to wait** (#1990), which is the first thing that ever resolves a batch on purpose — every path before it was the no-answer backstop. Given a batch id, the daemon consumes the batch registry's one-shot, denies claude's blocked `AskUserQuestion` call, and broadcasts one [`question_dismissed`](#question_dismissed) carrying the new **`outcome: refused` / `source: remote`** pair, which is what makes the `remote` row of § `question_dismissed`'s carry-over table emitted rather than merely structural. **The deny carries an instruction, not a reason string**, and that is the whole point rather than a nicety: a bare denial leaves claude free to answer its own question and carry on, which is exactly what the operator declined to let it do, so the message says they want to discuss the question and that claude is to wait for their next message. It is a **compile-time daemon constant** like `modal_answer`'s deny reason — never host-derived, never client-supplied — so nothing an operator or a paired device authored reaches claude through it. **The refusal and the backstop share one arbiter and cannot both fire**: the refusal consumes the same registry one-shot the retire closure does, so a batch either is refused (backstop silent) or was already retired (refusal inert), and an unknown batch id is inert on both counts. **The ordering inside that is load-bearing in both directions** and is stated because either mistake is silent: the correlation to claude's tool call is read *before* the one-shot is consumed, or a refusal could win the one-shot inside the retire closure's window and leave claude undenied *with* the backstop silenced; and the one-shot is consumed *before* claude is handed the verdict, or the retire closure — deferred by the control server and run the instant the approval unblocks — would win it and broadcast `unanswered` for a batch the operator actually refused. **Nothing is reachable from the wire yet.** The resolver seam is still nil at every construction site, so this is the daemon-side primitive only, and the per-device answer gate (#1986) is what wires it; the answered verdict and its own third sentinel are #1991's. **The path logs nothing at all** — the relay handler already records the outcome with the batch id, the one field this family marks safe to log, so a record here would duplicate it while its only new material is the batch body or the deny message, neither of which may ever be logged. **Live-prose corrections in the same pass:** § `question_dismissed`'s intro, its `outcome` row and both carry-over rows (`remote` now emitted, `local` now recorded as owned by nobody in flight — the `#1985` claim there was false twice over, since that ticket is closed and split and neither child emits it), the *"landed vocabulary is one pair"* paragraph now publishing two, plus the `question_refused` row in § [Application message types](#application-message-types) and § `question_refused`'s intro. **The answer-side cites were deliberately left alone** — § `question_answer`, § Question's intro and the Contract-bounds rows name work #1991 does, not this slice. The dated entries below are historical and stay untouched. **No live count moved**: no `#### ` heading was added or removed, so the three sentences reading **fifteen** stay checkable by counting `turn_state` through `model_announced`, and § `unrecognized_message`'s **five** is untouched.
- `2026-09-02`: **The inbound question frames are routed** — `question_answer` and `question_refused` are now intercepted by `dispatchAppFrame`'s v2 control switch before the ordinary application dispatch, decoded into their typed payloads, and handed to a nil-able resolver seam together with the connection's device (#1984). **The client-visible change is the reply that stopped arriving**, and it lands even with nothing wired behind the seam: the entry below recorded that both frames fell through to the ordinary dispatch and drew its unknown-type reply, and that is what the two switch cases remove. A sender can therefore no longer distinguish *consumed and dropped* from *consumed and acted on*, and must not read silence as either — the acting half is #1985. **The decode is the one place the modal handlers are deliberately not copied.** `modal_answer` / `modal_cancel` tolerate a decode failure and let it fall through as an empty `modal_id`, which is harmless for a payload of flat strings; `question_answer` carries a nested `answers` array and Go's decoder populates the fields it read *before* the one that failed, so tolerating the error can hand the seam a **valid batch id with nil answers** — precisely the empty-but-successful answer the shape's own contract forbids. So a decode failure is **rejected**: the seam is not called at all, and nothing about the failure is echoed, so an undecodable frame and a decodable one look identical from the client side. The rule is about a decode *error*, and a `null` payload decodes cleanly into the zero value, so it is an **unknown batch for the resolver to judge** rather than a rejected frame — judging it in the handler would install a second arbiter, which is the same reason the relay **broadcasts no `question_dismissed`**: that frame has one broadcaster, on the daemon side. **No authorization was added, deliberately.** The handler applies neither the `interactive` gate nor the per-device answer gate (#702), exactly as `modal_answer`'s applies neither, so the decision stays in one place; what makes that fail-safe is structural rather than argued — **the seam is nil at every wiring site**, so no answer reaches an actuator, and #1986 installs the per-device gate before anything is wired. **Nothing is logged that a payload authored**: no answer value and no raw payload byte on any path — inert, rejected or handed off — and no wrapped decode error, since `encoding/json` quotes offending input into its message. `question_batch_id` is logged where it is trustworthy, which the shape's contract marks safe expressly so this handler invents no redaction rule; the **reject** record carries none, because the decode is what failed and a partially-populated id would attribute refused bytes to a batch. The guard classification moved with the cases, from `excludedTypes`' *"pending handler"* to `inboundTypes`' *"switch-intercepted"* beside `modal_answer` / `modal_cancel` — enforced deterministically rather than by reading, since leaving it behind reddens the build. **Live-prose corrections in the same pass:** every claim that neither frame is intercepted or decoded — both rows in § [Application message types](#application-message-types), § Question's intro, its inbound-capability paragraph, and the two `#### ` bodies — plus the decoder obligations in § `question_answer`'s security paragraph, now marked discharged rather than pending while staying stated as rules that bind every later decoder. The dated entries below are historical and were left untouched. **No live count moved**: no `#### ` heading was added or removed, so the three sentences reading **fifteen** stay checkable by counting `turn_state` through `model_announced`, and § `unrecognized_message`'s **five** is untouched.
- `2026-09-02`: **The question family has an inbound half** — added `question_answer` and `question_refused` (phone → binary, interactive-capability-gated, #1983), closing the gap § [Question](#question-v2) had been stating in its own words since #1962. **Vocabulary only:** nothing intercepts or decodes either frame, and that is checkable rather than asserted — the v2 control switch has no case for either name and no `default` arm, so both fall through to the ordinary application dispatch and get its unknown-type reply. A Go constant is not a registry; the daemon's behaviour is byte-identical to before they existed. #1984 adds the interception, #1985 the resolution, and [pyrycode-desktop#853](https://github.com/pyrycode/pyrycode-desktop/issues/853) is the sender written against the published shape. **Two types, not one nullable flag**, mirroring `modal_answer` / `modal_cancel` and following the precedent `question_dismissed` set: a refusal carries no answers, and a shape able to express *"answered with nothing"* would need somebody to adjudicate it against a genuine refusal. **The selection is positional, and that is the security-load-bearing choice.** `options` entries carry no id and claude selects by `label`, so the obvious design echoes a subprocess-authored string back inbound; instead an entry names its question by **index** into the batch's `questions` array and carries values the *client* authored, so **no claude-authored byte makes the return trip at all** and the daemon reads the question from its own parked copy. The `values` are **never checked against the offered labels** — claude's contract permits free text anywhere and requires no value to be one of them, so a validator would reject a legal answer. Neither payload carries a `conversation_id`: the batch id is the sole correlation key, `modal_answer`'s posture unchanged, so the daemon never trusts a phone-asserted conversation. `answer_token` carries over intact — **idempotency, not authorization**; the real dedup is the one-shot consume of `question_batch_id`. The vendor top-level `response` field is **deliberately not carried** (decided 2026-08-31 in both repos; available later with no wire change). **Every bound on this shape is a gap, and each is named with its owner rather than published as a number** — § [Attachments](#attachments)' rule (#1752) — the sharpest being that `question_index` is *carried, never range-checked*, so subscripting the parked batch with a hostile index **panics** and #1985 owes an explicit check; entry count and index uniqueness are its too, and the only limit today is the transport's frame cap, which bounds bytes and not entries. **Declaring the vocabulary grants no inbound capability**: both the `interactive` gate and the per-device answer gate (#702, default OFF) remain the handler's to apply, default deny, and whether #702 extends to a question answer is #1984/#1985's call. Two fixtures pin the encoding and **three marshalled-zero tests pin what no fixture can reach**: #1964's arithmetic recurs one level down, because an empty `answers` array reaches none of the entry's keys and `"values":[]` bytes are produced only by marshalling a constructed nil, never by decoding — so a populated fixture alone would have shipped an unpinned `omitempty` on `question_index`, whose zero value `0` is the *most likely* index a client sends. The fixtures' values are pairwise distinct for #1974's reason (canonical-byte comparison lets a reordering mutant pass green whenever two swapped keys share a value). **Live-prose corrections in the same pass:** every claim that no inbound answer verb exists — the `question_shown` and `question_dismissed` rows in § [Application message types](#application-message-types), § Question's intro, and the *"No inbound verb is declared"* paragraph — and every live `#1907` cite re-pointed to #1983 (vocabulary), #1984 (routing) or #1985 (resolution), since #1907 is closed and split. The dated entries below are historical and were left untouched, including the one that misnames the answer frame as #1927's. **No live count moved**: the two new `#### ` headings sit under § Question, past `model_announced`, so the three sentences reading **fifteen** stay checkable by counting `turn_state` through `model_announced`, and § `unrecognized_message`'s **five** is untouched.
- `2026-09-01`: **An outstanding `question_shown` batch now survives a (re)connect** (#1980). #1979 landed the reconcile and left its daemon-side source unbound, so nothing reconciled in production; the daemon's batch store is now wired to it, and on any (re)connection every still-outstanding batch is unicast to the opening connection carrying its original `question_batch_id`. This makes `question_shown` the fourth control-state frame in [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)' Mode B, whose list now names it alongside the outstanding modal (#877) and the queued backlog (#878), and § [Question](#question-v2) gains the **Reconcile on (re)connect** note § Modal and § Queue already carried. The wiring reads the store **without retiring anything**, so the re-send changes nothing about answerability: the nonce is still minted once at raise time and consumed exactly once, and a batch answered or dismissed while the client was away is absent from the reconcile rather than re-sent. It matters more than a plain missed frame because the daemon counts an approval answerable while *any* interactive connection is open — before this, a reconnected client that had never received the batch re-armed the approval window at every expiry while being structurally unable to answer it.
- `2026-09-01`: **`question_shown` and `question_dismissed` are now emitted** (#1973). Both frames existed as wire vocabulary with no producer; the daemon now raises the whole batch as one `question_shown` the moment claude's `AskUserQuestion` call parks, carrying the daemon-asserted `conversation_id` and a `crypto/rand` `question_batch_id`, and retires it with one `question_dismissed` on every no-answer terminal path — the approval window elapsing, the caller disconnecting, the daemon shutting down. Those three share one arbiter, the batch registry's one-shot consume, so exactly one dismissal is broadcast per outstanding batch and the answered half (#1907) can never double-broadcast with it. That arbiter is also why `source` is `no_answer` rather than `timeout`: it cannot tell the three paths apart, and § [Question](#question-v2)'s carry-over table records the reasoning. A question no longer surfaces as a permission modal named after the tool; permission and trust approvals are unchanged. The **Contract bounds** table above moves with it — the two count bounds are enforced by #1965's parse, the two length bounds are still not.
- `2026-09-01`: Added `question_dismissed` (binary → phone, interactive-capability-gated, #1974), **settling the deferral the entry below left open**. That entry recorded that no dismissal frame existed and that whether one would be its own type or reuse `modal_dismissed` was #1927's design call; #1927 is closed and split, and the call landed here. It is **its own type**, because `modal_dismissed` identifies what it clears by `modal_id` and a client decoding that frame routes it to the modal panel — a `question_batch_id` arriving in that field clears the wrong panel or none, making the routing depend on a value's shape instead of on the frame's name. The payload is `{question_batch_id, outcome, source}`, field for field with `modal_dismissed`'s **including the absences**: no `conversation_id` (the batch id is the sole correlation key, and a shape carrying both admits a disagreeing pair) and no `omitempty`. **One fixture, not #1964's three** — the payload is flat, so every key is reachable from one all-zero struct and the nesting-depth arithmetic that forced three fixtures on the batch does not arise. Seven mutants (`omitempty` on each of the three keys, three key renames, one field reordering) were **run** over a `go test -overlay` scratch copy rather than predicted, and all seven turn at least one test red. The two tests are **sole-red for disjoint classes**, which is the result worth carrying: the round trip catches all three renames and the reordering and is **blind to every `omitempty`** — the fixture's three values are all non-empty, so nothing is elided — while the marshalled-zero key-presence check catches all three `omitempty` mutants. A populated fixture alone would have shipped an unpinned `omitempty` on all three keys. The fixture's three values are also **pairwise distinct on purpose**: `roundTripEnvelope` compares canonical bytes, so a reordering mutant re-encodes identically and passes green whenever the two swapped keys share a value. **What `source` inherits is stated per value rather than left to assumption**, which is the correction most likely to change what a client builds: `modal_dismissed` pins `source` to the closed set `{remote, local, timeout}`, and that set is **provably short here** — two of the producer's three terminal paths, a caller disconnect and a daemon shutdown, have no member in it, and neither is a timeout nor an answer. Of the three, only `timeout` will be emitted by a slice in flight (#1973); `remote` and `local` are answered outcomes belonging to the answer half (#1907). So `source` is published as a plain string whose vocabulary the producer owns — the `outcome`/`class` posture, not the modal `source` one — with the gap named instead of a short set published, § Attachments' rule (#1752), and with a **fail-closed reading rule**: an unrecognised `source` means *resolved, cause unknown* and never an answer, since reading it as one renders a daemon safe-deny as the operator's own choice. **`outcome` never carries a claude-authored option label**, and that rule is published positively because the natural implementation of an answer path violates it: `options` entries carry no id and claude's protocol selects by **`label`**, so a producer reporting the chosen option reaches for that subprocess-authored string first, which would move this frame to the batch's trust tier while its field table still read *daemon-asserted*. Three properties depend on the rule holding and are stated together: § Security model's threat 1 does **not** land on this frame (unlike `question_shown`, it carries no claude-authored byte), no field can carry a byte of the parked tool input into a log, and the frame's length stays daemon-determined in a family shipping no `truncated_fields`. The `question_batch_id` row adds the two properties the batch's row could not: the nonce is **dead once this frame lands**, and receiving it is **not a capability** — a retired batch resolves nothing server-side, as a stale `modal_id` resolves nothing under first-answer-wins. **Four live-prose `#1927` cites in § Question were re-pointed in the same pass** (the `question_shown` row and the *"Nothing emits this frame yet"* paragraph to the producer #1973, the `crypto/rand` mint obligation to #1975, and the inbound answer to #1907); a live cite to a split-away ticket is what #1860 spent a whole ticket correcting. The changelog entries below are historical and were left untouched. **No live count moved**: the new `#### ` heading sits under § Question, well past `model_announced`, so the three sentences reading **fifteen** stay checkable by counting `turn_state` through `model_announced` (re-counted 2026-09-01: fifteen), and § `unrecognized_message`'s **five** is untouched.
- `2026-09-01`: Added `question_shown` (binary → phone, interactive-capability-gated, #1964). The wire type landed in #1962 and the shape in #1963 with **no fixtures and nothing published here** — the string `question_shown` did not appear in this file at all — so pyrycode-desktop#849, which mirrors this wire field for field to write a fail-closed decode, had only Go structs to read. This entry lands the committed bytes and the prose together, exactly as #1705 did for `model_list` and #1718 for `slash_command_list`. **Three fixtures pin the encoding**, and the values are hand-authored on purpose: the committed capture `internal/e2e/realclaude/testdata/ask_user_question_v2.1.239.json` is one question, two options and an explicit `multiSelect:false`, so it settles none of the three arms that matter — a **populated** batch therefore carries two questions, `multi_select: true`, and a question with three options; an **empty** batch carries no questions at all, with `questions` present as `[]` rather than `null` or elided; and a **zero-value** payload carries one all-zero question holding one all-zero option, which is the only route to the two nested types' keys, since a batch with no questions reaches neither. The bytes are the package encoder's own output rather than a hand transcription. Adding `omitempty` to each of the nine wire keys in turn, plus three key renames and one field reordering, was **run** over a scratch overlay rather than reasoned about, and all thirteen turn at least one test red. Two results are recorded because a later reader would otherwise guess them wrong in opposite directions: the zero-value fixture is the **sole** thing reddening six of the nine (`conversation_id`, `question_batch_id`, `question`, `header`, `label`, `description`), while the new empty fixture is **not** sole-red for `questions` — the constructed-value normalisation test catches that mutant too, so the empty fixture's contribution is the **decode** side of the `[]` guarantee, which nothing pinned before, rather than a mutant nothing else catches. And `"options":[]` appears in **no** fixture: decoding `[]` always yields a non-nil slice, so the only value producing those bytes is a constructed nil, and the nested normalisation test is where that lives — the two-level siblings `model_list_zero.json` and `slash_command_list_zero.json` reach their empty-array key from their all-zero entry and this shape, one level deeper, does not. **Every documented bound is enforced nowhere**, checked against the tree rather than against what a sibling intends: 1–4 questions and 2–4 options per question have no enforcer (#1965's fail-closed parse is unmerged; `internal/protocol` enforces none by design), and neither does any maximum length for the four strings — deliberately published **without a number**, for § Attachments' reason that a figure ahead of its enforcer is worse than none. **The header cap is published as documented 12, observed 14**, which is the correction most likely to change what a client builds: the vendor page says "max 12 characters" while the capture's only header, `Write strategy`, is 14, so a client sizing for 12 and truncating clips the one real header ever measured. The **unit is named as runes**, with the caveat that the observed header is pure ASCII — 14 runes and 14 bytes coincide there and **nothing committed separates the two units**, so the coincidence is not a measurement. That bound is expected to stay unenforced: #1965 is forbidden from rejecting at 12 fail-closed, since its own acceptance pins it against the same capture. Four further things a client gets wrong by default are stated: **provenance is per field**, so the tables mark the two ids daemon-asserted and the four strings claude-authored rather than leaving a client to infer the trust boundary from the security paragraph; `question_batch_id` is a **one-time, opaque, unguessable** nonce with `modal_id`'s four properties and a `crypto/rand` minting obligation on #1927, and the **fixtures' ids are placeholders** whose length and shape are not a contract; **no inbound verb is declared**, and because an option carries no id — claude's answer protocol selects by **`label`** — whatever answer frame #1927 designs returns a claude-authored string, which publishing here does not make trusted on the way back; and there is **no `truncated_fields`** in this family, unlike `SlashCommand` and `ModelOption`, so a cut cannot be reported and #1965 must reject an over-long field fail-closed rather than truncate it silently. The strings are claude-authored at `model_list`'s trust tier and are **neither bounded nor sanitized** today, so § Security model's threat 1 lands on a remote render surface and the sanitization is owed by the client. **No live count moved**: the section lands after § Modal, adds no `#### ` heading under § Interactive events, and is not a `turnevent` variant — so all three sentences reading **fifteen** stay checkable by counting those headings (counted 2026-09-01: `turn_state` through `model_announced`, fifteen), § `unrecognized_message`'s **five** is untouched, and § `slash_command_list` still reads **fourth instance** while this frame is the fifth. Apart from the one new application-message-types row, the whole diff to this file is additive.
- `2026-08-27`: **Corrected § `model_list`'s published claim that nothing emits the frame** (#1860). Three statements in this file's live prose had gone false and the file carried no record of it. **The frame is emitted**: #1848 added the mapping onto this wire shape, #1849 added the producer on `cmd/pyry`'s interactive turn lane, and #1845 proves it reaches a connected client end to end — so the application-message-types row, § `model_list`'s own paragraph and § `slash_command_list`'s precedent citation now name those slices instead of **#1693**, a ticket that was split and no longer exists as work (#1690, which the `dropped_models` paragraph credited, is the same). **`dropped_models` counts**: #1812 landed a ten-entry producer cap, the mapping carries the number **verbatim rather than recomputing it**, and the producer passes it through, so the sentence that forbade `len(models) + dropped_models` is **inverted** — that arithmetic now yields the menu's true size, which is the correction most likely to change what a client does, since the old text told a client not to do the one thing that now works. And the **delivery window** is stated for the first time, because "emitted" on its own promises more than a client gets. What runs on a schedule is an **ask, not a delivery** — one `initialize` exchange per child spawn, emitted on the live interactive turn lane to whatever clients are connected at that instant — and delivery is **best-effort rather than guaranteed** at **three** loss points, only one of which is load: the bootstrap child's menu is lost **unconditionally**, since the daemon spawns that child before any conversation is routed and drops an event it cannot address; a busy session can refuse the frame at the fan-in, and that is not an error; and a **session rotation delivers no fresh menu**, because the new child's events no longer match the lane's session binding. Naming only the load point would have re-published a promise the daemon does not keep in the two cases a client most naturally expects it — attaching to a running daemon, and refreshing the menu after a rotation — which is the second half of this ticket's own user story. There is **no connect-time snapshot today** for a client that missed it, with the one narrower recovery named rather than elided: the frame carries an `event_id`, so a **reconnecting** client with a `last_event_id` predating it is replayed it (Mode A, a cursor backfill), which does nothing for a first attach or for a frame that was never emitted. A client must never block its model menu on the frame's arrival. `model_list` is therefore **deliberately not added** to [§ Reconnect / Backfill semantics](#reconnect--backfill-semantics)' Mode B list: that absence is a decision rather than an oversight, and the list gains a third bullet only when a connect-time reconcile becomes observable to a client. The section's opening no longer says the frame *"does not belong to the structured live-session stream"* — a client read that as *carries no `event_id`*, which is exactly what hid the Mode A recovery above; the frame is not one of the fifteen turn-stream events, but it rides their lane and carries their id. Left standing on purpose: § `slash_command_list`'s and § `attachment_chunk`'s own "nothing emits it yet" statements, since those frames genuinely have no producer (#1720; #1741/#1743/#1744/#1746), and § `slash_command_list`'s `dropped_commands` qualification, which remains true for the sibling frame — nothing counts it. **No live count moved**: this frame is not a `turnevent` variant, so every sentence reading **fifteen** and § `unrecognized_message`'s **five** are untouched, and § `slash_command_list` still reads **fourth instance**, because re-pointing a precedent's ticket numbers adds no precedent. Documentation only — no behaviour, no fixture and no test changed.
- `2026-08-27`: **Superseded by the `2026-08-27` (#1860) entry above** in its closing claim that *"nothing emits this frame yet remains true"* — that sentence was correct when written, in the window between #1848 landing and #1849 landing, and #1849 closed the window the same day. The rest of this entry still holds: the `truncated_fields` enumeration in producer order and the pure, verbatim 1:1 carry with `dropped_models` sourced from the decode. § `model_list`'s `truncated_fields` row now enumerates the four cut-field names in producer order (`resolved_model`, `value`, `display_name`, `effort_levels`) instead of leaving them unstated (#1848). `internal/turnbridge`'s `MapEvent` gained the arm mapping `turnevent.ModelList` onto this wire shape in the same ticket — a pure, verbatim 1:1 carry with `dropped_models` sourced from the decode — but `Handle` still has no arm routing a decoded model list to it, so **nothing emits this frame yet** remains true; that's the sibling transport slice.
- `2026-08-27`: [`set_session_settings`](#set_session_settings) now accepts the **bracketed variant values** claude publishes in its own model menu (#1838). [`model_list`](#model_list) offers a row per identity claude will run as, and two of the five values measured on 2026-08-21 — `opus[1m]` and `claude-fable-5[1m]` — were refused by the daemon's inbound validator, so a client menu built from the frame had rows that failed on click: the same defect class pyrycode-desktop#682 reports for permission modes. The rule is now published as a **grammar** rather than as a byte set, because a widening is only reviewable if what it still refuses is as legible as what it newly admits: `""`, or a value within the unchanged 64-byte bound whose first byte is alphanumeric, whose remaining bytes are in `[A-Za-z0-9._-]`, and which may carry **one trailing bracket group** — non-empty, balanced, unnested, the value's final element, and drawn from that same closed byte class. That is deliberately **narrower than adding `[` and `]` to the charset**: a leading, unbalanced, empty or nested bracket is rejected, as is a second group or any suffix after one, and every shape the old rule existed to reject still is — a leading dash, a shell metachar, whitespace, a control byte, a byte at or above `0x80`, and anything past the bound. The closure is **machine-checked across all 256 byte values in each of the three positions** rather than exemplified, because the accepted value reaches **two** sinks and each depends on a different half of it: the claude argv, where `--model` and the value are two separate `execve` elements and no shell parses either, so the bar that matters there is the first-byte-alphanumeric rule that stops a value posing as a flag; and the **live child's turn text**, which is the branch a menu click actually travels since #1581 stopped restarting the child for a model change — the daemon writes `/model <value>` onto the running child's stdin as one line, so an accepted value must stay a single whitespace-free token that can neither end that line nor open a second word or a second slash command. Neither bracket is a line terminator, a separator or a command sigil, which is why the group can be admitted without weakening either sink. `effort_levels` is **not** widened alongside it, and § `model_list`'s numbered property is **rewritten rather than deleted** for exactly that reason: the inbound effort enum is closed, accepts all five levels claude returns today, and would refuse a level claude adds later, so the general caution that a published value need not be sendable back survives the correction and now points at the field that still carries it. The `value` row of the per-model field table and the `2026-08-22` entry below are corrected in the same direction; the entry is marked superseded in its two inbound-validator claims rather than rewritten, following the `2026-08-19` entry's convention for a dated record that has become partly false.
- `2026-08-25`: Published [Attachments](#attachments) and the seven `attachment.*` reject codes (#1751). #1752 landed the shared `attachment_chunk` frame and #1753 its 45000-raw-byte per-chunk bound with **nothing published here**, so a client author had to reverse-engineer both legs from Go struct comments — and this document actively contradicted the landed code in two places, **both corrected**: § Application-envelope size cap claimed large payloads "require an envelope-level chunking scheme that is out of scope for this spec", and § Scope listed attachments as out of scope outright (now narrowed to the daemon-side implementation, which really is unbuilt). The section publishes the one frame carrying **both** directions — upload phone → binary, retrieval binary → phone — with its eight always-present fields, the sender's chunking arithmetic (`total_chunks = max(1, ceil(size / 45000))`, every chunk but the last exactly at the bound, the `max(1, …)` defining a zero-byte file as one empty chunk), the receiver's **index-addressed** reassembly where chunks may arrive in **any order** — deliberately weaker than `debug_bundle_chunk`'s strict `seq` succession, which is the neighbouring rule a reader would otherwise copy — exact-lowercase-hex `sha256` compared as **integrity, not authenticity** and explicitly not a fetch key, and the **never allocate from a claim** rule with its first-chunk cross-check. The reject vocabulary is declared in one place so #1741, #1743, #1744 and #1746 answer from a shared list instead of each inventing a name months apart, and two of its decisions are published as contracts rather than left to those implementations. `attachment.not_found` is **one code for every retrieval that yields no bytes** — unknown id, non-canonical id, and an id resolving outside the named conversation's directory alike, with a static message that never echoes the id or the resolved path — because two distinguishable codes would make the retrieval verb a path-existence oracle for a traversal probe; nothing is lost, since all of those outcomes mean the same thing to a client. And the receiver's resource bound **splits into two codes on retryability**: "too many concurrent uploads" clears when *other* uploads finish, while "this upload is over the byte bound" is permanent for that file, so a single code would tell a client either to re-upload an oversized file in a loop or to give up on a bound that clears in seconds. All three retryable rows carry an explicit **back off, never resend immediately** obligation, matching the `4429` row's existing language, because `retryable: yes` alone turns a conforming client into a hot loop. Neither the per-upload byte bound nor the concurrency bound is given a **number** — those are #1741's to pick, and publishing a figure ahead of the code that enforces it is what #1752 existed to prevent; a client learns them by being rejected. **Nothing emits, accepts or enforces any of this yet** (reassembly #1741, storage #1743, inbound dispatch #1744, retrieval #1746), the same declare-then-publish sequencing #1704→#1705 and #1726→#1718 used. `attachment_chunk` also gains the application-message-types table's first genuinely **bidirectional** row.
- `2026-08-24`: **Superseded by the `2026-08-27` (#1860) entry above** in its `model_list` precedent citation only — "#1704 ahead of #1693" is now #1704 ahead of #1848 and #1849, both landed. The rest of this entry still holds, including its own **Nothing emits the frame yet** for `slash_command_list`, whose producer is still #1720, and its `dropped_commands` clause, which is about the sibling frame and remains true. Added `slash_command_list` (binary → phone, interactive-capability-gated, #1718). The wire type — `TypeSlashCommandList` — was declared by #1726 and the shape — `SlashCommandListPayload`, `SlashCommand` and their two `MarshalJSON` normalisers — by #1727, both with no fixtures and no section here, so a client author had to write a decoder by reading Go structs; this entry lands the committed bytes and the prose together, exactly as #1705 did for `model_list` and #1405 and #1616 each did before it. Two consumers are blocked on the shape rather than on the producer: pyrycode-desktop#681, the Actions-menu grey-out that matches its menu entries against the list, and pyrycode-desktop#694, a slash-command type-ahead that renders each row as a name, an argument hint and a description. **Nothing emits the frame yet**: the producer is #1720, the fourth instance of the declare-then-emit sequencing #1405 used ahead of #1410, #1616 ahead of #1638 and #1704 ahead of #1693. Three fixtures pin the encoding — a **populated** frame carrying five rows drawn in claude's own order from the committed capture `internal/e2e/realclaude/testdata/initialize_control_v2.1.239.json` (claude 2.1.239, re-measured 2026-08-24), covering both of the capture's key sets, an empty argument hint, a multi-element and a single-element alias array, the `<model>` hint that pins Go's HTML escaping, `claude-api`'s two embedded newlines beside raw non-ASCII, and one row reporting a cut `description` beside four reporting `null`; an **empty** frame carrying no commands at all, where `commands` is present as `[]` rather than elided; and a **zero-value** payload whose single all-zero entry is the only route to five of the eight wire keys, since a frame carrying no entries cannot reach `SlashCommand`'s keys at all. Adding `omitempty` to any one of those eight keys, one at a time, was **run** rather than reasoned about — each mutant scoped to one struct over a scratch overlay, since `json:"conversation_id"` alone appears 17 times in the file — and all eight turn at least one test red, with the five that reach no other assertion in the tree (`conversation_id`, `dropped_commands`, `name`, `argument_hint`, `description`) reddened by the fixtures this entry commits. Four client-facing properties are stated because a client gets each wrong by default: **a name-only match misses aliases** (11 across 9 of the 51 entries, and the desktop Actions menu's own `reset` is an alias of `clear`, not a name), and the cheaper source cannot repair it because the same capture's `system`/`init` line carries a names-only `slash_commands` twin — identical 51 names, identical order, not one of the 11 aliases; **the count is workspace- and version-dependent** (51 here against 2.1.239, an earlier hand count against 2.1.220 in a different working directory reported 74), so no client may cache it, assume a floor, or treat a small list as an error; **the strings are workspace-authored, bounded but not sanitized**, a lower-trust origin than claude's own, with `0x0a` the only sub-`0x20` byte anywhere across the 51 entries' four string fields, so a type-ahead row assuming one line per description will not get one; and **a cut is reportable and must be read**, with the size quoted with its unit named (14,277 bytes of compact UTF-8 against 14,371 `\u`-escaped, and the wire exactly neither because Go escapes only `<`, `>`, `&` and U+2028/U+2029). Because `aliases` collapses absent and empty into the same `[]`, a `truncated_fields` naming `aliases` is the only signal separating "cut to nothing" from "none", and the section says a client must read it as *unknown*. `dropped_commands` is documented with `model_list`'s honest qualification copied: nothing counts it yet, so `len(commands) + dropped_commands` is not the menu's true size today and no entry cap is enforced — #1719 and #1720 own the bound. § `model_list` gains one sentence and a link so the two frames reach each other from either arrival point: both publish a per-conversation menu from the same `initialize` reply, one inventorying identities and the other verbs. **No live count moved**: this frame is not a `turnevent` variant and is not one of the turn-stream events, so all three sentences carrying that count — § Interactive events, `session_transition` and `model_list` — still read **fifteen**, and landing the subsection **after** § `model_list` is what keeps every one of them checkable by counting `####` headings. § `unrecognized_message`'s count of five is likewise untouched — it counts `system` subtypes the parser maps internally, and this frame arrives on a `control_response`. Apart from that single added sentence in § `model_list`, the whole diff to this file is additive.
- `2026-08-22`: **Superseded twice.** By the `2026-08-27` (#1838) entry above in its two inbound-validator claims — the two bracketed values are no longer rejected, and the charset is no longer left un-widened. And by the `2026-08-27` (#1860) entry above in its two `model_list` claims: **"the producer is #1693"**, which names a ticket that was split and no longer exists as work — the frame is emitted, by #1848 and #1849 — and **"nothing counts it yet"** about `dropped_models`, which #1812's ten-entry producer cap made false, so `len(models) + dropped_models` **is** the menu's true size today. The rest of this entry still holds: the shape, the three fixtures and what each pins, the nine-key `omitempty` mutation sweep, the other three client-facing properties, and the unmoved live counts. Added `model_list` (binary → phone, interactive-capability-gated, #1705). The shape — `TypeModelList`, `ModelListPayload`, `ModelOption` — was declared by #1704 with no fixtures and no section here, so a client author had to write a decoder by reading Go structs; this entry lands the committed bytes and the prose together, as #1405 and #1616 each did. The consumer is pyrycode-desktop#561, blocked since 2026-08-19, which derives its model menu from the identifiers and its effort segments from the per-model levels; pyrycode-desktop#682 is the second consumer, for `supports_auto_mode` — claude refuses `auto` permission mode per model, so a permission-mode menu needs the flag to grey the option out. **Nothing emits the frame yet**: the producer is #1693, the same declare-then-emit sequencing #1405 used ahead of #1410 and #1616 ahead of #1638. Three fixtures pin the encoding — a **populated** menu carrying the five rows measured live against claude 2.1.220 on 2026-08-21 (including Haiku's, whose effort list is empty because claude's reply omits the key entirely, and one row reporting a cut `value` beside four reporting `null`); an **empty** frame carrying no models at all, where `models` is present as `[]` rather than elided; and a **zero-value** payload whose single all-zero entry is the only route to five of the nine wire keys, since a frame carrying no entries cannot reach `ModelOption`'s keys at all. Adding `omitempty` to any one of those nine keys, one at a time, was **run** rather than reasoned about, and each of the nine turns at least one test red. Four client-facing properties are stated because a client gets each wrong by default: `value` is **not a dated identifier** (an alias, a bracketed variant, or `default`), so it cannot be split on `-` to derive a family; a lookup against this list **can miss and that is ordinary**, since claude announces an identifier at least as specific as the one it was given, and `display_name` rather than `resolved_model` is the intended join back to `model_announced`; **two of the five measured values are rejected by the daemon's own inbound validator** (`opus[1m]` and `claude-fable-5[1m]` — the bracket is outside the charset), so a menu row is not necessarily sendable back, and the charset is **not** widened to close the gap because it is #845's argv-injection defense; and the strings are claude-authored, **bounded but not sanitized**, a report and never a control input, with the amendment that `value` is the first field in this family a client is meant to send back and publishing it does not make it trusted. `dropped_models` is documented with its honest qualification: nothing counts it yet, so `len(models) + dropped_models` is not the menu's true size today and no entry cap is enforced. `model_announced` gains one sentence and a link so the two frames reach each other from either arrival point. **No live count moved**: this frame is not a `turnevent` variant and is not one of the turn-stream events, so § Interactive events and `session_transition` both still read **fifteen**, and landing the subsection **after** `session_transition` is what keeps both sentences checkable by counting `####` headings. § `unrecognized_message`'s count of five is likewise untouched — it counts `system` subtypes the parser maps internally, and this frame arrives on a `control_response`. The three existing rows carrying a wire field named `model` are **not** re-pointed: unlike #1616's collision the name is not literally shared here (this payload has `models`, `value` and `resolved_model`), and the distinction that needed drawing is the one between the two frames that publish model identity.
- `2026-08-20`: `request_session_settings` now answers for the conversation the client **named** (#1610). The reply's `session_id` and its five reported values all describe the session bound to that conversation; before this they described the shared **bootstrap** session whichever conversation was named, and the client then put that bootstrap id on its `set_session_settings` — so an operator changing the model, effort or **bypass-permissions** posture from a sheet was silently reconfiguring a background session they were not looking at. The id and the values move as one because the daemon resolves them together, which is the property the previous shape could only assert in prose. **An absent or empty `conversation_id` now gets the all-zero reply**, inverting the `2026-08-19` entry below: a request that names no conversation names no session, and the only alternative would be to hand-build an explicit fallback to the shared session, which is the route the multi-session work forbids. Still never an error frame, and still no failure branch — the all-zero `session_settings` is the answer for every unresolvable case, and an unknown conversation is indistinguishable from an unbound one. **Client companion change, not lock-step:** an un-updated client that sends the older bare frame now gets a visibly inert sheet until it puts `{"conversation_id": "<the conversation the sheet is for>"}` on the payload. That is the accepted trade in this direction — an inert sheet beats one that silently writes a bypass-permissions choice into someone else's session — and the field has existed since #1586, so a client that starts sending it **today**, before this lands, is answered identically. The reply's `session_id` then already addresses the right session, so the existing `set_session_settings` write path needs no change. `screen_snapshot`'s side-loaded copies of these values are untouched and remain bootstrap-scoped; they were already deprecated in favour of this route.
- `2026-08-19`: Added `model_announced` (binary → phone, interactive-capability-gated, #1616). The daemon has parsed claude's `system/init` line into a `turnevent` variant since #1600, but that variant is internal — `turnbridge.MapEvent`'s `default` drops it — so no client could see which model claude actually ran. The shape is declared **ahead of its producer**: nothing emits this frame until #1617, the same sequencing #1405 used ahead of #1410 and #1393 ahead of #1394. It is conversation-scoped, carries no `turn_id` and drives no turn lifecycle. The hazard the section spends most of its words on is the **name collision**: three payloads already carry a wire field called `model` (`screen_snapshot`, `session_settings`, `set_session_settings`) and all three mean the per-session **override**, where `""` is "inherited default"; this one means **what claude announced for the turn**, and in the ordinary case they disagree because the override is `""` while claude has named a concrete model. Each of those three rows now points here, so reading any one of the four is enough to learn the other meaning exists. Three further properties are stated because a client gets each wrong by default: claude echoes an identifier **at least as specific** as the one it was given, so the value is **not reliably dated** and **need not appear in any published model list** (`claude-haiku-4-5` does not) — a lookup miss is **ordinary**, not an error; the announcement is **once per turn, not once per session**, so latching the first one shows a stale value; and `truncated` is load-bearing, since a client ignoring it presents claude's cut text as complete. The value is **never the empty string**, and the daemon does not repair it. It is bounded but **not sanitized** — no control-character or terminal-escape stripping on this path — so the render boundary owing the sanitization is the **client's**, and the frame is a **report, never a control input**. Also **corrected three stale counts**: § Interactive events and `session_transition` both said "fourteen" turn-stream events; both now read fifteen. And § `unrecognized_message` said the parser maps **four** `system` subtypes internally, which had been **wrong since #1600** — that ticket added the `init` arm and never touched this file, so `init` was neither counted nor listed while the surrounding prose still read as though it were silently dropped. That count now reads **five** and attributes #1600. The sentence about how many reach the wire deliberately **stays four**: `init` is declared here and emitted in #1617, so it is documented below without reaching the wire yet. The #1404 clause's back-reference is re-anchored to five; `rate_limit_event` remains a top-level line type rather than a `system` subtype, so the claim itself is unchanged.
- `2026-08-19`: **Superseded by the `2026-08-20` entry above** in its scoping claims — the reported values no longer stay bootstrap-scoped, and an absent or empty `conversation_id` is no longer answered as it was. The rest of this entry (why the field exists, and that an unhosted conversation is answered with zeros rather than an error) still holds. `request_session_settings` gained a `conversation_id` (#1586). The frame used to be bare, and this document said so in as many words, justifying it with "the reported values are daemon-wide, so there is no field a client could use to select another session's data". Both halves are now gone: the field exists, and the daemon reads it. It matters because this verb's reply carries the `session_id` a client must put on every `set_session_settings`, so the read verb decides which session each client write lands on and a client had no way to say which conversation it meant. **The change is additive on the wire and no reported value moved with it.** A named conversation the daemon hosts is answered exactly as before; one it does not host is answered with an all-zero `session_settings` rather than an error, because `session_id: ""` is already the defined "no session to address" answer and a verb documented as always answering should not grow a failure branch; and an **empty or absent** field is answered as it always was, which is what keeps un-updated clients working and what keeps the sheet alive on a freshly started daemon, where the registry seeds nothing and there is no conversation id in existence to send. The field therefore gates **whether** the answer is populated, not **which** session it describes — the reported values stay bootstrap-scoped, and making them follow the named conversation is #1587, where the values and `session_id` must move in one step.
- `2026-08-09`: Added `rate_limited` (binary → phone, interactive-capability-gated, #1405). The daemon has translated claude's top-level `rate_limit_event` line into a `turnevent` variant since #1404, but that variant is internal, so a turn that stops making progress because of a usage limit still had nothing on the wire saying why. The shape is declared **ahead of its producer**: `turnbridge.MapEvent` has no case for the variant, so nothing emits this frame until #1406 — the same sequencing #1393 used ahead of #1394. It is conversation-scoped, carries no `turn_id` and drives no turn lifecycle. Three things a client gets wrong by default are stated in the section: `status` is an **open string with a mostly unmeasured value set** (no capture of a limit actually in force exists, so it must be rendered as an opaque label and never branched on for security-relevant behaviour); `resets_at` is **claude's number, unvalidated in both directions**, so formatting it as a date without a range check is the realistic bug, and `0` means "not reported", not the epoch; and `truncated_fields` is load-bearing, since a client ignoring it presents claude's cut text as complete. The frame is a **report, never a control input** — nothing in the daemon keys a behaviour on it. Also **corrected two stale counts**: § Interactive events and `session_transition` both said "thirteen" turn-stream events; both now read fourteen. The `system`-subtype count of four is deliberately unchanged — `rate_limit_event` is a top-level line type, not a `system` subtype.
- `2026-08-09`: Added `thinking_progress` (binary → phone, interactive-capability-gated, #1386). The daemon had translated claude's `system/thinking_tokens` line into a `turnevent` variant since #1385, but `turnbridge.MapEvent` had no case for it, so it stopped at the daemon boundary and a client showing "thinking" for three minutes still could not separate a slow answer from a wedged session. It is conversation-scoped, carries no `turn_id`, drives no turn lifecycle, and carries **no reasoning text** — only two of claude's integer readings. Four consumer hazards are stated in the section because a client gets each wrong by default: the frames are **rate-bounded** (one per 64 tokens of accumulated delta — 33 lines became 8 frames on the committed capture) and do not enumerate claude's lines; `estimated_tokens` is **not monotonic** (four restarts in the capture's single turn), so two readings must never be subtracted; the deltas received **do not sum** to the turn's total (674 arrived as 243) and no field reports the residue; and **absence proves nothing** for two separate reasons — the PTY surface emits none at all, and on the emitting surface a gap may only mean the bound has not been crossed. Also **corrected two stale counts**: § Interactive events and `session_transition` both said "twelve" turn-stream events; both now read thirteen.
- `2026-08-09`: Added the three background-task frames — `background_task_started`, `background_task_updated`, `background_task_roster` (binary → phone, interactive-capability-gated, #1394). The daemon had translated claude's `system/task_started`, `system/task_updated` and `system/background_tasks_changed` lines internally since #1380–#1382 and given them wire types in #1393, but nothing mapped them outbound, so a phone still could not tell #1240's symptom — `turn_end` with `end_turn` and state `idle` while a command claude started is provably still running — from a genuine finish. None of the three carries a `turn_id` or drives any turn lifecycle. The roster is a **snapshot, not a delta**, its `tasks` is always present and never `null` (an empty `[]` positively says nothing is alive), and its `dropped_tasks` count is its **only** truncation report. No terminal/finish event exists in the family, deliberately: that transition has never been observed, so the daemon does not report a finish it cannot detect. Also **corrected two stale counts**: § Interactive events and `session_transition` both said "eight" turn-stream events when there were nine; both now read twelve.
- `2026-07-27`: Added `unrecognized_message` (binary → phone, interactive-capability-gated). The stream-json parser used to drop any claude output it had no mapping for into a debug log the production daemon does not print, so an unknown message type left no trace anywhere and no client was told. It now splits two ways: a **measured** known-ignored list (`system/*`, `rate_limit_event`) stays silent, and everything else surfaces as this frame carrying the drop site, the offending type, and the raw JSON capped at 16 KiB. Carries no `turn_id` and drives no turn lifecycle. Also **fixed a #1074 omission**: `api_retry` and `compacting` shipped without rows in the application-message-types table above; both are now listed.
- `2026-07-27`: Added the `request_session_settings` → `session_settings` read pair (#491, #1214), the read half the settings cluster shipped without. A client can now ask what the current run configuration is and which session to address a change to, instead of scraping both off `screen_snapshot`. That side-load is refused with `server.binary_offline` whenever there is no terminal to photograph, which is always on the stream-json interactive runner, so it left the desktop run-configuration UI with no values, no session id and no context figure at all. Corrected the `set_session_settings` claim that a client already knows its session id from the `session_transition` marker: the daemon fires that marker only on a clear or an idle eviction, never on session creation. Also filled in the `screen_snapshot` field table, which never listed the `used_tokens` / `window_tokens` fields shipped with #857.
- `2026-07-03`: Docs-only drift correction against the deployed code. Marked v2 as shipped and the daemon's current default (v1 is the deprecated `PYRY_MOBILE_V2=0` fallback). Normalised every endpoint reference to `/v1/server` / `/v1/client` — the relay routes only those, and the version lives in the `v` frame field, not the route. Added WS close code `4429` (per-server phone cap). Corrected the envelope `id` row: `id` is a per-connection counter that resets on reconnect and is not a durable dedup key; durable ordering/dedup is via `event_id`. Corrected the token contract: the `x-pyrycode-token` header is still required today and passes through the relay opaquely, so the "header removed / relay never sees the token" end-state is marked not yet implemented.
- `2026-06-07`: Retired the binary↔relay `hello`/`hello_ack` ceremony (#582). That leg is established on WS upgrade with header-based server-id registration; the relay sends no `hello_ack`. Endpoints stay `/v1/server`, `/v1/client` (route path carries no protocol meaning; `/v2` rename not performed).
- `2026-05-16`: v2 draft (this document). Adds end-to-end encryption via Noise_IK. Hard cutover from v1; v1 doc preserved in git history only.
- `2026-05-08`: v1 initial draft (superseded; see git history).
