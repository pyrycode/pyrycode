# Spec #1006 — e2e: `request_debug_bundle` over the v2 wire (stream + decode + error no-leak)

**Size:** S · **Security-sensitive:** yes · Split from #962.

## Files to read first

- `internal/relay/v2session.go:2107-2266` — `handleDebugBundleRequest` + `debugBundleReplyError` + `bundleInFlight`. Extract the FIXED error contract the e2e asserts: `protocol.CodeServerBinaryOffline` + `msgDebugBundleUnavailable` (const value `"debug bundle unavailable"`, **unexported** → the e2e hardcodes the literal) + `Retryable: true`; and confirm the assemble-error branch logs the EVENT only (`"event":"v2.bundle.assemble_err"`), never the wrapped `err` — the no-leak property the error subtest proves.
- `internal/relay/v2bundlestream.go:20-153` — `bundleChunkBytes = 48000` (fake archive must exceed this for a multi-chunk stream); `bundleEnvelopes` (0-based ascending `Seq`, trailing `debug_bundle_done` with `Total == N`); `ReassembleBundle` — the receiver contract the test's reassembly mirrors (arrival-order concat, `Seq == chunks-seen`, `done.Total == chunks-seen`).
- `internal/protocol/messaging.go:261-285` — `DebugBundleChunkPayload{Seq int, Data []byte}` + `DebugBundleDonePayload{Total int}` JSON field tags for decode.
- `internal/protocol/codes.go:362-391` — `TypeDebugBundleChunk` / `TypeDebugBundleDone` / `TypeRequestDebugBundle` (intercepted pre-`dispatch.Route`) + `CodeServerBinaryOffline` string.
- `internal/e2e/relay_v2_promote_test.go` (whole, 168 lines) — the **error-path skeleton verbatim**: `pair` → `StartInWithEnv(home, {PYRY_ALLOW_INSECURE_RELAY=1, PYRY_MOBILE_V2=1}, …)` → `waitBinaryHello` → `fakephone.Dial` → `driveHandshakeToOpenDaemon` → seal request (`initSend.Encrypt`) + `sendNoiseMsg` → read one reply (`decryptInnerEnvelope(t, readInnerFrame(…), initRecv)`) → assert `TypeError` / `InReplyTo` / decode `ErrorPayload`.
- `internal/e2e/relay_v2_daemon_test.go:44-73` and `:378-…` — `driveHandshakeToOpenDaemon` returns `(initSend, initRecv)`; `decryptInnerEnvelope(t, inner, cs)` advances `cs` **once per frame** — the happy-path loop decrypts frames in strict arrival order with `initRecv`.
- `internal/e2e/relay_v2_handshake_test.go:166` and `:341` — `readInnerFrame(t, phone, timeout)` reads one inner frame (Fatals on timeout); `sendNoiseMsg(t, phone, ciphertext)`.
- `internal/relay/v2session_debugbundle_test.go:183-260` — the unit-tier template: the **bare** request envelope `{ID, Type: TypeRequestDebugBundle, TS}` (no payload); how chunks+done reassemble; and the `#911` in-flight-gate subtests that already cover AC-3 deterministically in-package (why the e2e defers it).
- `cmd/pyry/main.go:709-713`, `:761-762`, `:858-870` — `logRing` tees **every** daemon log line (⇒ the real archive is non-deterministic, hence the fake seam); `allowInsecure` is computed at :762; the `debugBundler` closure build site (:867) where the fake override wires in.
- `internal/e2e/harness.go:80-102`, `:227-245` — `Harness.Stderr *safeBuffer` (daemon-log capture for the no-leak assertion); `StartInWithEnv(t, home, extraEnv, extraFlags…)` env surface.

## Context

`handleDebugBundleRequest` streams a debug bundle to a paired phone as `debug_bundle_chunk*` frames followed by `debug_bundle_done`. Its design is *what does not leak*: the assemble-error path logs the event but never the wrapped err (which could quote a recording path), and the error reply is a FIXED code/message/`retryable`, so no attacker-influenced or assembly-error text reaches the wire. This ticket proves both paths **end-to-end over the encrypted v2 wire** — not just at the existing `internal/relay/v2session_debugbundle_test.go` unit tier.

**Why a fake bundler is required (overriding the ticket's "prefer existing harness surface" note).** The production `debugBundler` is `func() { a,_,e := debugbundle.Assemble(bundleRecordingsDir, logRing.Snapshot()); return a,e }` (`cmd/pyry/main.go:867`). `logRing.Snapshot()` tees **every** daemon log line (startup, handshake, connect — timestamps, conn-ids, durations), and a present recording contributes a timestamped `RecordingName`. The assembled archive is therefore **not byte-predictable**, so AC-1's "reassembles to the **exact known archive bytes**" is unreachable via the real path. The unit test computes a known archive only because in-package it controls the `logs` input; the e2e (subprocess daemon, plain `go build`, no build tags) cannot. The Technical-Notes preference assumed a config surface exists to inject the bytes — none does (`resolveRecordingsDir` is not env-overridable; `DebugBundler` is an in-daemon closure). A minimal env-gated fake seam is the correct, established mechanism (same shape as the `PYRY_CLAUDE_BIN` / `PYRY_ALLOW_INSECURE_RELAY` runtime knobs already shipped in `cmd/pyry`).

## Design

### Change 1 — env-gated fake bundler seam (production, minimal)

New file `cmd/pyry/debug_bundle_fake.go`, one self-contained helper:

```go
// fakeDebugBundler returns a test-only DebugBundler override, active ONLY when
// PYRY_ALLOW_INSECURE_RELAY=1 (the daemon's existing test/insecure marker) AND a
// fake-mode env var is set. Returns (nil, false) otherwise → the real
// debugbundle.Assemble path is left untouched. Belt-and-suspenders: even a stray
// PYRY_FAKE_DEBUG_BUNDLE_* in a production env is inert without insecure-relay.
func fakeDebugBundler() (func() ([]byte, error), bool)
```

Contract (helper reads its own env; no scope coupling):
- Gate: return `(nil, false)` unless `os.Getenv("PYRY_ALLOW_INSECURE_RELAY") == "1"`.
- **Happy mode** — `PYRY_FAKE_DEBUG_BUNDLE_FILE=<path>` set → return `func() ([]byte, error) { return os.ReadFile(path) }, true`. The file's bytes are the "known archive" the e2e round-trips.
- **Error mode** — else if `PYRY_FAKE_DEBUG_BUNDLE_ERR=<sentinel>` set → return `func() ([]byte, error) { return nil, errors.New(sentinel) }, true`. `sentinel` is a fake recording path the e2e asserts never leaks (mimics the real assembler's path-quoting err).
- Precedence: FILE beats ERR (they are mutually exclusive in tests).

Wiring at `cmd/pyry/main.go:867` — 3 additive lines after the real closure is built:

```go
if fake, ok := fakeDebugBundler(); ok {
    debugBundler = fake
}
```

No new exported types. No consumer cascade. The real bundler, the `DebugBundler` field, and `debugbundle.Assemble` are unchanged.

### Change 2 — the e2e test (new file `internal/e2e/relay_v2_debug_bundle_test.go`, `//go:build e2e`)

Two subtests, both built on the `relay_v2_promote_test.go` skeleton (pair → `StartInWithEnv` → handshake-to-open → seal + send → read). The conn is **non-interactive** (`driveHandshakeToOpenDaemon`) — the bundle is authorized by pairing, matching the unit test's deliberate choice. The request frame is **bare**: `protocol.Envelope{ID: reqID, Type: protocol.TypeRequestDebugBundle, TS: …}` (no payload; the handler ignores it).

**Cipher-order invariant (both subtests).** Every daemon→phone frame (reply OR push) is sealed with the daemon's send cipher = the phone's `initRecv`; nonces increment in lockstep. The phone MUST `decryptInnerEnvelope(…, initRecv)` each frame in strict arrival order, then classify by `Type`. An idle open conn emits nothing but the bundle/reply (see promote test), so no interleaving is expected; the loop still classifies-after-decrypt (tolerating a stray non-bundle frame like `ReassembleBundle` does).

#### Subtest A — happy path (stream + decode), AC-1

- Build a known archive of **> 48000 bytes** (≥ 3 chunks; e.g. ~120000). Fill with **position-dependent** content (so a hypothetical reorder changes the concatenation — the assertion must not mask an emission-order bug). Write it to a temp file; keep the slice as the oracle.
- `StartInWithEnv(home, {PYRY_ALLOW_INSECURE_RELAY=1, PYRY_MOBILE_V2=1, PYRY_FAKE_DEBUG_BUNDLE_FILE=<path>}, "-pyry-relay="+fr.URL()+"/v2/server")`.
- Send the bare request. Read frames in a loop, decrypting each with `initRecv` in arrival order; **append `(Seq, Data)` in arrival order** until a `debug_bundle_done` frame arrives; decode its `Total`.
- Assertions (**non-maskable** — do NOT sort by `Seq`):
  1. For each received chunk `i`: `chunk[i].Seq == i` (0-based contiguous ascending).
  2. `done.Total == len(chunks)` (count equals chunks received).
  3. `bytes.Equal(concat(chunk[i].Data in arrival order), knownBytes)` — exact reassembly.
- Chunk/done frames are pushes (`InReplyTo == nil`) — do **not** assert `InReplyTo` on them.

#### Subtest B — error path (no-leak), AC-2

- Choose a distinctive sentinel, e.g. `/pyry-e2e-canary/recording-DO-NOT-LEAK-<rand>.cast`.
- `StartInWithEnv(home, {PYRY_ALLOW_INSECURE_RELAY=1, PYRY_MOBILE_V2=1, PYRY_FAKE_DEBUG_BUNDLE_ERR=<sentinel>}, …)`. (Fake is non-nil ⇒ handler calls it ⇒ hits the **assemble-error** branch — the stronger wrapped-err path, not the nil path.)
- Send the bare request; read **one** frame, decrypt.
- Assertions:
  1. `reply.Type == protocol.TypeError`; `reply.InReplyTo != nil && *reply.InReplyTo == reqID`.
  2. Decode `protocol.ErrorPayload`: `Code == protocol.CodeServerBinaryOffline`; `Message == "debug bundle unavailable"` (hardcoded literal — const is unexported); `Retryable == true`.
  3. **No-leak (load-bearing):** the sentinel appears in neither the decrypted reply (`string(reply.Payload)` / the whole envelope) **nor** `h.Stderr.String()` (captured daemon logs). Non-vacuous: if the handler ever logged `"err", err`, the sentinel surfaces in stderr and this fails.
  4. The single received frame is the `TypeError` reply — the error branch never calls `StreamBundle`, so no `debug_bundle_chunk`/`done` is emitted (structurally provable; no trailing-frame poll needed).

**Log-ordering note:** the handler writes the `assemble_err` `Warn` **before** enqueuing the async error reply, so by the time the phone decrypts the reply the log line is already on the daemon's stderr fd. Read `h.Stderr.String()` *after* receiving the reply. (If the pipe-copy lag ever flakes, poll `h.Stderr` briefly for the `v2.bundle.assemble_err` event before the negative sentinel check — keep it simple unless it flakes.)

### AC-3 — `#911` in-flight gate: **deferred at the e2e tier (not cheap), noted**

Forcing a stable "prior bundle undrained" window over a real websocket is racy: `StreamBundle` pushes drain as fast as the fakephone reads the socket, so holding frames queued requires stopping the reader, filling the transport send buffer, *and* timing a second request into that window — no deterministic barrier exists. The gate is already covered **deterministically in-package** by `internal/relay/v2session_debugbundle_test.go` (it inspects the push queue directly). Per AC-3's explicit "if not cheap, defer and note" clause, the e2e omits it; this paragraph is the note.

## Concurrency model

No new goroutines. The test drives one fakephone over one WSS conn; the daemon's existing async push path (`Push → drainOnce → forwardEnvelope`) delivers the stream. The only ordering contract the test relies on is the Noise transport cipher's per-message nonce lockstep (decrypt in arrival order).

## Error handling / failure modes

- Handshake / dial failures → `t.Fatalf` (mirrors promote test).
- `readInnerFrame` Fatals on timeout — a missing chunk or missing `done` fails the test loudly (3s per read is ample for local delivery).
- Fake helper: `os.ReadFile` error in happy mode surfaces to the handler as an assemble error (would flip the happy subtest to a `TypeError` reply → visible failure). The test writes the file before spawn, so this can't happen in the green path.

## Testing strategy

- `make e2e` with `-race` green (AC-4). Both subtests under one top-level `TestRelayV2_DebugBundle` with `t.Run` sub-cases, matching the promote test's shape.
- Non-vacuity is structural: the happy oracle is the exact bytes the fake serves; the error sentinel is injected via the fake err and asserted absent from wire + logs. A regression that reorders chunks (A), truncates the stream (A), or logs the wrapped err / puts dynamic text on the wire (B) fails a specific assertion.
- Keep the reassembly loop's concat in **arrival order** and the `Seq`/`Total` checks separate — do not collapse them into a sort-then-compare.

## Open questions

- None blocking. If an idle open v2 conn is later found to emit an unsolicited frame before the bundle (none observed today), the decrypt-then-classify loop already tolerates it; no test change needed.

## Security review

Adversarial pass on this spec (security-sensitive label — mandatory, appended last).

**Trust boundaries.** The request crosses the authenticated + encrypted v2 boundary (Noise_IK, paired-device token). The e2e drives a legitimately-paired phone; no new inbound boundary is introduced — the handler is pre-existing and the request is a bare frame whose payload the handler ignores, so no new untrusted-content parsing surface is added. The daemon-side auth/pairing gate that admits the conn is unchanged.

**Outbound-content policy (the reason for the label).** The bundle carries the highest-value secret surface (recording + logs) outbound; it travels ONLY over the AEAD-sealed push path, unchanged by this ticket. The happy path proves faithful round-trip (no corruption/truncation) of an *authorized* transfer — not a leak. The error subtest is the load-bearing security proof: it asserts the FIXED `CodeServerBinaryOffline` / `"debug bundle unavailable"` / `retryable` reply and that the injected sentinel path appears on **neither** the wire **nor** the captured daemon logs. It is non-vacuous — the sentinel would surface if the handler regressed to logging `"err", err` or emitting dynamic error text. This directly exercises the outbound-content-policy invariant the label demands.

**New production surface — the env-gated fake bundler.** The only net-new production code. Threat model: could it exfiltrate an arbitrary file or forge a reply in production? Analysis: it is inert unless `PYRY_ALLOW_INSECURE_RELAY=1` **and** a `PYRY_FAKE_DEBUG_BUNDLE_*` var are both set; in any production deployment none are set, so `debugBundler` is byte-identical to today (real `Assemble`). An actor who can set the daemon's environment and drop a file already holds local process control — the same threat class the shipped `PYRY_CLAUDE_BIN` (points the daemon at an arbitrary child binary) and `PYRY_ALLOW_INSECURE_RELAY` (disables relay TLS pinning) knobs already grant. The belt-and-suspenders insecure-relay gate is a deterministic env compare (different fabric from any agent rule), ensuring a stray fake var in production is inert. Net production attack surface: none beyond existing env-control.

**Secrets / logging hygiene.** The fake serves test-controlled bytes to the paired phone only (a legitimate sink). No new log line emits archive bytes, the sentinel, or the fake file path. The e2e reads `h.Stderr` for the *negative* sentinel assertion; it does not print secrets.

**Verdict: PASS.** One new surface (fake seam), env-gated behind the existing insecure marker and inert in production; the e2e's error subtest is a non-vacuous outbound-content-policy proof; no new inbound trust boundary.
