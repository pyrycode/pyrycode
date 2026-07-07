# Spec: Serve the debug bundle to a paired client on request (#813)

**Ticket:** #813 · **Size:** S · **Labels:** `size:s`, `security-sensitive`
**Chosen mechanism:** a new inbound v2 control verb `request_debug_bundle`, intercepted in `internal/relay/v2session.go`'s `dispatchAppFrame` (like `request_snapshot` / `interrupt` / `dequeue_message`), that assembles the bundle via an injected `DebugBundler` closure (over #811's `debugbundle.Assemble`) and delivers it via #812's `(*V2SessionManager).StreamBundle`. The capstone that wires #811 (producer) and #812 (transport) into one paired-client request/response flow. No new authorization gate — pairing is enforced structurally at the Noise IK handshake (an unpaired device is refused with 4401 and never reaches `dispatchAppFrame`).

## Files to read first

- `internal/relay/v2session.go:1477-1539` — `dispatchAppFrame`: the probe-switch that routes control types (`TypeRequestSnapshot`, `TypeInterrupt`, `TypeDequeueMessage`, …) to manager methods **before** `dispatch.Route`. Extract: add one `case protocol.TypeRequestDebugBundle:` arm here; this is the interception seam, NOT the `Handlers` map.
- `internal/relay/v2session.go:1590-1707` — `handleRequestSnapshot` + `snapshotReplyError`: the exact structural template. A manager method that runs on the Run goroutine, replies via `m.forwardEnvelope` (never `c.Send`), and never logs content. Extract: mirror its shape for `handleDebugBundleRequest`; reuse the `forwardEnvelope` seal-and-forward path for the error reply.
- `internal/relay/v2bundlestream.go:71-108` — `StreamBundle(ctx, connID, blob)`: the async delivery mechanism. Its docstring already states it is "safe to call from any goroutine, including a future handler on the Run goroutine" — this ticket IS that caller. Extract: the call contract (returns on enqueue, not delivery; first `Push` error stops).
- `internal/debugbundle/bundle.go:66-124` — `Assemble(recordingsDir, logs) → (archive, Manifest, err)` and `DefaultRecordingsDir()`. Extract: the producer the wired closure calls; the archive is self-describing (manifest.json inside marks recording present/absent), so the handler never inspects the Manifest.
- `internal/protocol/codes.go:253-278` — the `TypeInterrupt` / `TypeDebugBundleChunk` const block + the "MUST NOT be added to v1TypeSet" discipline. Extract: where to add `TypeRequestDebugBundle` and the doc-comment conventions to mirror.
- `internal/protocol/compat_test.go:67-144` — the v1/v2 partition drift detector. Extract: the FOUR edits a new v2 control type forces (rejected-case in `TestIsV1Compatible`, `v2OnlyTypes` map, `TestTypeConstants_V1V2Partition` list). Missing any fails the build.
- `internal/relay/v2session_test.go:700-860` — `openSession`, `driveToOpen`, `sealAppFrame`, `decryptAppFrame`, `waitForEnvelopes`. Extract: the full harness for driving a paired handshake to open, sending a sealed app frame, and decrypting captured outbound frames. `initSend` seals phone→binary; `initRecv` decrypts binary→phone.
- `internal/relay/v2session_test.go:761-798` — `driveToOpenCaps` + the unpaired-token / handshake-reject tests nearby. Extract: how to drive an *unpaired* handshake (AC3) — reuse the existing 4401-reject fixture; do not build a new gate.
- `cmd/pyry/relay.go:276-351` — `startRelayV2` + the `NewV2SessionManager(V2SessionConfig{…})` literal. Extract: where the new `DebugBundler` config field is set; mirror `Snapshotter`/`KnownConversation` optional-seam wiring.
- `cmd/pyry/main.go:707-711`, `:838` — `logRing := control.NewRingBuffer(200)` and the `startRelay(…)` call. Extract: `logRing.Snapshot()` is the log source; build the `DebugBundler` closure here and thread it through `startRelay` → `startRelayV2`.
- `internal/control/logs.go:58-73` — `RingBuffer.Snapshot() []string` (oldest-first copy). Extract: the exact log-snapshot call the closure wraps (same source `pyry logs` returns).

## Context

Third and final slice of the debug-bundle feature (split from #803):

- **#811** (merged) — `internal/debugbundle.Assemble`: the in-memory archive producer. Unwired.
- **#812** (merged) — `(*V2SessionManager).StreamBundle` + `debug_bundle_chunk` / `debug_bundle_done` wire vocabulary: the cap-respecting chunked transport. Unwired (no request verb drives it).
- **#813** (this) — the request verb that ties them together: a paired client sends `request_debug_bundle`, the daemon assembles and streams the bundle back over the encrypted channel.

A paired desktop/mobile client wants the current session's evidence — recent daemon logs plus the terminal recording (when debug capture was on) — to diagnose a pyrycode fault without SSHing into the host. This ticket makes that a one-request round-trip over the already-encrypted v2 channel.

**Why the `Handlers` map is the wrong seam** (resolving the ticket's Technical Notes tension). The ticket says "register on the `Handlers` seam" AND "do not return the bundle through the 8-frame synchronous reply path" AND "deliver it via #812's `StreamBundle`." These are mutually exclusive: a `dispatch.Handler` (the `Handlers` map's element type) receives only a `*dispatch.Conn`, whose sole reply path is `c.Send`/`c.Reply` → the per-frame `outbound` channel (`handlerOutboundBuf = 8`) drained only *after* the handler returns (`dispatchAppFrame:1510-1538`). A bundle produces many chunks; the 9th `c.Send` blocks forever (the drain hasn't started) — the exact deadlock the ticket forbids. `StreamBundle` lives on `*V2SessionManager` and a `Handlers` closure cannot reach it (chicken-and-egg: the manager is constructed *with* the `Handlers` map as a config field). The established pattern for a verb needing manager state is the `dispatchAppFrame` interception (`request_snapshot` needs `Snapshotter`; `interrupt` needs `Interrupter`; this needs `StreamBundle`). The wiring the ticket references ("in `cmd/pyry/relay.go`") is still accurate — the new `DebugBundler` config field is set there, beside the `Handlers` map.

## Design

### 1. Wire type — `internal/protocol/codes.go`

Add one const beside `TypeInterrupt`, with a doc comment mirroring the "inbound phone → binary control, intercepted pre-`dispatch.Route`, MUST NOT be added to `v1TypeSet`" conventions:

```go
// TypeRequestDebugBundle is a phone → binary v2 control envelope: a paired
// client asks for the current session's debug bundle. Intercepted in
// dispatchAppFrame before dispatch.Route (like TypeInterrupt); NO dispatch.Route
// handler. Bare frame — no payload. The daemon replies by streaming the
// assembled bundle back as debug_bundle_chunk* + debug_bundle_done (#812).
TypeRequestDebugBundle = "request_debug_bundle" // phone → binary, inbound v2 control
```

It is a **bare frame** (no payload struct): the bundle is daemon-global (whole 200-line log ring + newest recording across all sessions — the ring has no per-session key, per #811), so there is no `conversation_id` or other field to carry. This mirrors `TypeInterrupt`, which also carries no payload.

**No new payload type; no new response type.** The response is the existing `debug_bundle_chunk*` + `debug_bundle_done` stream from #812; the manifest (recording present/absent) travels *inside* the archive as `manifest.json`.

### 2. Handler — `internal/relay/v2session.go`

**Config field** (optional seam, beside `Snapshotter` / `KnownConversation` in `V2SessionConfig`):

```go
// DebugBundler assembles the current session's debug bundle (recent daemon
// logs + newest recording when present) as one in-memory archive. Optional:
// nil ⇒ request_debug_bundle replies with a deterministic unavailable error,
// never a silent drop. Production wires a closure over
// debugbundle.Assemble(recordingsDir, logRing.Snapshot).
//
// SECURITY: the returned bytes are the plaintext bundle (recording + logs).
// They MUST NOT be logged. handleDebugBundleRequest streams them only over the
// AEAD-sealed push path and logs byte/chunk counts, never content.
DebugBundler func() (archive []byte, err error)
```

The closure returns only `(archive, err)` — the manager never needs the `Manifest` (it is inside the archive), which keeps `internal/relay` from importing `internal/debugbundle`.

**`dispatchAppFrame` interception** — one arm added to the probe switch (`v2session.go:1488-1507`):

```go
case protocol.TypeRequestDebugBundle:
    m.handleDebugBundleRequest(ctx, s, probeEnv)
    return
```

**`handleDebugBundleRequest(ctx, s, env)`** — new method, structural twin of `handleRequestSnapshot`. Behavior summary (contract, not implementation):

1. `DebugBundler == nil` → deterministic error reply (feature unavailable), return. (Optional seam; foreground/unwired.)
2. `archive, err := m.cfg.DebugBundler()`; on `err` → log the error **class only** (no content, no bytes) at warn, send a deterministic error reply, return. (Honours #811's "read-failure honesty" — a recording that exists but fails to read surfaces as an error, not a false-absent.)
3. `m.StreamBundle(ctx, s.connID, archive)` — enqueue the chunk stream. A `StreamBundle` error (only `ErrConnNotFound`, unreachable for an open `s` on the Run goroutine) is logged at debug and dropped (the package's outbound-drop posture).
4. On success, one **content-free** info log: `conn_id` + `len(archive)` bytes only — never the archive or any member.

The error reply reuses the `m.forwardEnvelope` seal-and-forward path with a **static** message constant and an existing `protocol.Code*` (e.g. `CodeServerBinaryOffline`, `retryable: true`) — mirror `snapshotReplyError` (`v2session.go:1679-1707`); a small dedicated `debugBundleReplyError` helper or direct reuse of the generic body, developer's choice. The message MUST be a constant — never the decode/assembly error text.

**No interactive-capability gate.** Unlike the turn-stream verbs, the bundle is not gated on `CapabilityInteractive`: any device that completed the Noise handshake (reached `V2StateOpen`) is a paired device, which is the authorization the ticket specifies. A non-interactive paired client (e.g. a desktop diagnostic tool) can request a bundle.

### 3. Wiring — `cmd/pyry/relay.go` + `cmd/pyry/main.go`

- `main.go`: resolve `recordingsDir, _ := debugbundle.DefaultRecordingsDir()` (independent of the `DebugCapture` flag — old recordings persist and are readable even after capture is turned off), build the closure, and pass it to `startRelay`:
  ```go
  debugBundler := func() ([]byte, error) {
      archive, _, err := debugbundle.Assemble(recordingsDir, logRing.Snapshot())
      return archive, err
  }
  ```
- `startRelay` / `startRelayV2`: thread one new `debugBundler func() ([]byte, error)` parameter (the v1 leg ignores it — debug bundle is v2-only) and set `DebugBundler: debugBundler` in the `V2SessionConfig` literal.

### Data flow

```
phone ──request_debug_bundle (sealed noise_msg)──▶ handleNoiseMsg(V2StateOpen)
                                                        │ AEAD-decrypt
                                                        ▼
                                              dispatchAppFrame probe switch
                                                        │ TypeRequestDebugBundle
                                                        ▼
                                              handleDebugBundleRequest
                                          ┌─────────────┴──────────────┐
                              DebugBundler() → (archive, err)          err/nil → forwardEnvelope(TypeError)
                                          │
                                          ▼
                              StreamBundle(ctx, connID, archive)
                                          │ bundleEnvelopes → Push (per-frame)
                                          ▼
                              drainOnce: seal each as noise_msg → m.send
                                          ▼
phone ◀── debug_bundle_chunk*, debug_bundle_done (sealed) ── reassemble → untar → manifest.json + logs.txt [+ recording.cast]
```

## Concurrency model

No new goroutines, channels, or locks. `handleDebugBundleRequest` runs synchronously on the manager's single Run dispatch goroutine (the same goroutine that owns `s.send`/`s.recv`), reached via `handleNoiseMsg` → `dispatchAppFrame`. This is safe because:

- `StreamBundle` → `Push` **never blocks** and never touches `s.send` (it enqueues onto the per-session `pushQueue` under the `pushMu` leaf lock; the Run goroutine drains and seals later via `drainOnce`). This is exactly why `StreamBundle`'s docstring green-lights the on-Run-goroutine caller — it sidesteps both the frame-cap wall and the 8-frame handler-buffer wall.
- `DebugBundler()` is `debugbundle.Assemble`, a synchronous single-goroutine function that shares no state (#811 § Concurrency); calling it inline on the Run goroutine briefly blocks *this conn's* dispatch for the assembly duration (glob + stat + one `io.CopyN` of the recording + gzip). A large recording could hold the Run goroutine for the copy — see Open questions; acceptable for the current single-daemon-single-operator scale, and no worse than `handleRequestSnapshot`'s inline render.
- Bundle chunks are control-class (`Type != TypeAssistantDelta`), so the `pushQueue` drop policy never evicts them — every chunk is delivered, in order (#812 invariant).

## Error handling

| Failure | Handling |
|---|---|
| `DebugBundler == nil` (unwired/foreground) | Deterministic error reply (unavailable), `retryable: true`. No drop, no panic. |
| `Assemble` returns error (e.g. selected recording fails to open — #811 read-failure honesty; `O_NOFOLLOW` symlink reject) | Log error **class** only at warn (no content); deterministic error reply. |
| `StreamBundle` returns `ErrConnNotFound` | Unreachable for open `s` on Run goroutine; debug-log + drop (outbound-drop posture). |
| Unpaired device sends anything | Never reaches this handler — refused at the Noise handshake (4401), `s` never reaches `V2StateOpen`. Structural (AC3). |
| Duplicate/racing request on same conn | Each `StreamBundle` streams a self-contained `seq 0..N-1` + `done{N}`; per-conn Push FIFO preserves order. Reassembler stops at first `done`. Degenerate client behavior; not an AC. |

Every error reply carries a static message constant; no assembly-error text, no decode-error text, no attacker-influenced bytes ever reach the wire or the log.

## Testing strategy

New test file `internal/relay/v2session_debugbundle_test.go`, same-package, stdlib `testing`, `t.Parallel()`, reusing the `openSession` harness (`driveToOpen`, `sealAppFrame`, `decryptAppFrame`, `waitForEnvelopes`). A small `reassembleFromRecorder(t, rec, initRecv, n)` helper: wait for `n` outbound frames, `decryptAppFrame` each, feed the decrypted `[]protocol.Envelope` to `relay.ReassembleBundle`, then gunzip+untar the returned archive into `map[string][]byte`. Scenarios (bullet-level; developer writes the bodies):

- **AC1 — capture on: logs + recording.** `DebugBundler` returns a known small archive assembled from a `t.TempDir()` recordings dir containing one `.cast` with sentinel bytes plus a few log lines. Drive to open (paired), send a sealed `request_debug_bundle` bare frame, collect the `debug_bundle_chunk` + `debug_bundle_done` frames, reassemble, untar. Assert: `recording.cast` present with the sentinel bytes; `logs.txt` present; `manifest.json` decodes with `recording_present: true`. (Size the archive < `bundleChunkBytes` = 48000 so the frame count is deterministically 2: one chunk + one done.)
- **AC2 — capture off: logs only, absent-marked.** Same, but the `DebugBundler` archive is assembled from an **empty** recordings dir. Reassemble + untar. Assert: **no** `recording.cast` member; `logs.txt` present; `manifest.json` has `recording_present: false`, `recording_bytes: 0`. This proves "capture-off actually withholds the recording" structurally (there is no recording member to withhold), not merely a flag.
- **AC3 — unpaired refused.** Drive a handshake with an **unpaired** token (reuse the existing 4401-reject fixture). Assert the conn is refused at the handshake (never reaches `V2StateOpen`) and a `DebugBundler` that flips a `called` flag is **never invoked**. Verifies the inherited gate; builds no new gate.
- **AC4 — no bundle content in any log; encrypted-only.** Seed the archive + log lines with a sentinel secret string; run the full AC1 flow with a capturing logger (slog → `bytes.Buffer`; grep the existing tests for the buffer-logger helper). Assert: the captured log output never contains the sentinel or any archive bytes (only counts/conn_id). Separately assert the bundle bytes never appear **unencrypted** on the `frames`/recorder channel — every outbound bundle frame is a `noise_msg` whose `data` is AEAD ciphertext (the reassembly path must `decryptAppFrame` to recover them; a plaintext scan of the raw routing frames finds no sentinel).
- **Error path — assembly failure.** `DebugBundler` returns `(nil, err)`. Assert: exactly one outbound frame, decrypts to a `TypeError` envelope with a static message (not the error text), `InReplyTo` = the request id; **no** bundle chunks; the captured log carries no content.
- **Unavailable — nil bundler.** `DebugBundler == nil`. Assert a single deterministic `TypeError` reply; no chunks; no panic.
- **Protocol partition (`compat_test.go`).** Add `TypeRequestDebugBundle` to the three drift-detector sites: the rejected case in `TestIsV1Compatible`, the `v2OnlyTypes` map, and the `TestTypeConstants_V1V2Partition` `all` list. (Build fails otherwise.)

**Non-vacuity note:** the reassemble helper must assert real member bodies (byte-compare `recording.cast`, decode `manifest.json`), not merely that the archive is non-empty; and AC4's log assertion must run over a logger that actually captured the flow's emissions.

## Open questions

- **Large-recording Run-goroutine hold.** Assembling inline blocks this conn's dispatch for the copy duration of the newest recording (unbounded — a long session is every PTY byte). Same posture as `handleRequestSnapshot`'s inline render and acceptable at current scale; if a multi-hundred-MB recording ever stalls other conns' liveness, move `Assemble` behind a bounded worker (a follow-up, not this ticket). Not an observed failure (Evidence-Based Fix Selection).
- **Bundle memory under the control-never-drop policy.** A very large archive → many control-class chunks admitted past the 256 `pushQueueCap` soft cap (#812), so peak memory ≈ archive × ~4/3 (base64) held in the queue until drained. Inherited from #812/#811's "not a remote DoS vector" finding — a paired client cannot inflate the local session's output. No in-handler size cap added for an unobserved OOM.
- **`docs/protocol-mobile.md § Debug bundle`** documents the chunk/done vocabulary; the `request_debug_bundle` request verb should be added there by the documentation phase (out of the developer's code+test+spec scope).

## Security review

**Verdict:** PASS (full pass below — `security-sensitive` label is the gate, per `architect/security-review.md`).

This verb is the **first wire-reachable path** that emits the recording — "the highest-value secret surface in the system" (#811) — to a remote client. Two ticket-mandated strict requirements: (a) capture-off must *withhold* the recording, not merely flag it; (b) no bundle content may be written to any log. Each category walked against "could this leak content, bypass pairing, or let a hostile actor obtain a bundle without a paired device's credential?"

**Findings:**

- **[Trust boundaries]** No MUST FIX. The one inbound trust boundary is the `request_debug_bundle` frame, which is only reachable **after** the Noise IK handshake authenticated the device's pairing token and advanced the session to `V2StateOpen` (`handleNoiseMsg`'s `V2StateOpen` arm → `dispatchAppFrame`). An unpaired device is refused at `ReadInit` / token-validate with WS 4401 and its session never leaves `V2StateAwaitingInit`/`HandshakeComplete`, so `dispatchAppFrame` — and this handler — is structurally unreachable for it (AC3). The frame is a **bare** control envelope: no payload, so there is no attacker-controlled field (no `conversation_id`, no path, no id) that flows into assembly or the wire. The verb takes no argument that could select a different session's data — the bundle is daemon-global by construction.
- **[Tokens, secrets, credentials]** N/A here — no token is generated, stored, or compared in this handler; the pairing token check is inherited, unchanged, at the handshake. The recording *is* the packaged secret; its handling is covered under File operations (never persisted — #811) and Errors/logs (never logged — below).
- **[File operations]** No MUST FIX. This handler creates/opens **no** file: it calls the injected `DebugBundler` closure, which is #811's `Assemble` — read-only glob/stat/`O_RDONLY|O_NOFOLLOW`, output to an in-memory `bytes.Buffer`, **never** written to any synced/backed-up path (#811 § File operations, satisfied structurally). This ticket adds no disk write. The recordings dir is the fixed non-synced `~/.local/share/pyry-recordings` (#802); the `O_NOFOLLOW` TOCTOU hardening is inherited.
- **[Subprocess / external command execution]** N/A — no `exec`, no shell, no env handling. Pure in-process assemble + enqueue.
- **[Cryptographic primitives]** No finding — the bundle is transported **only** as AEAD-sealed `noise_msg` frames: `StreamBundle` → `Push` → `drainOnce` seals every chunk under the session's `s.send` CipherState before `m.send`. The handler never emits a plaintext frame; there is no code path that writes the archive bytes to the wire outside the seal. Confidentiality in transit is the Noise channel's (inherited, unchanged). The AC4 test asserts the sentinel never appears unencrypted on the raw routing-frame channel.
- **[Network & I/O]** OUT OF SCOPE (owner #812, inherited). Cap-respecting chunking (each frame's ciphertext < `maxNoisePayloadBytes`) is #812's charter and is exercised by `TestStreamBundle_EveryFrameWithinCap`. No remote-controlled size here — a paired client requests whatever the local session already produced; it cannot inflate the recording. Memory bound noted under Open questions; #811/#812 already ruled this not a remote DoS vector.
- **[Error messages, logs, telemetry]** No MUST FIX — this is the AC4 surface and the primary new risk. Enforcement: (1) the injected `DebugBundler` is `debugbundle.Assemble`, which makes **zero** slog calls (#811 AC5, by construction), so the assembly step cannot leak; (2) `handleDebugBundleRequest` logs only `conn_id` + `len(archive)` (a count) on success and the error **class** on failure — never the archive, a member, a log line, or a recording byte; (3) every error reply carries a **static** message constant, never the assembly/decode error text. `StreamBundle`'s own debug log is content-free (counts only, #812). The AC4 test pins this against a capturing logger with a seeded sentinel.
- **[Concurrency]** No finding — the handler runs on the single Run dispatch goroutine (same owner as `s.send`/`s.recv`); `StreamBundle`/`Push` enqueue under the `pushMu` leaf lock without touching `s.send`, so no new lock order and no data race. No goroutine is spawned; a mid-flight process kill loses only the in-memory buffer + queued chunks (nothing partial persists — there is no disk write).
- **[Threat model alignment]** No finding — the two strict requirements are met: **capture-off withholds** (AC2 — the archive has no `recording.cast` member when no recording exists; withholding is structural, not a flag the client could ignore), and **no content logged** (AC4 — content-free logging end-to-end). Authorization is pairing, enforced at the handshake (no new gate, per the ticket). The bundle reaches only the requesting authenticated conn (`StreamBundle(ctx, s.connID, …)` addresses exactly `s`), never broadcast.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-07
