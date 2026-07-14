# Spec #942 — Migrate the phone-dependent v1 e2e tests to the v2 Noise handshake

**Ticket:** [#942](https://github.com/pyrycode/pyrycode/issues/942)
**Size:** S (architect-confirmed — 3 test-file migrations + one dead-helper deletion; no production change)
**Security-sensitive:** No (no `security-sensitive` label; test transport swap, no new design surface — [[security-sensitive-label-tracks-design-not-lineage]]). Security-review pass skipped.
**Blocked by:** #941 (relocate/delete sibling) — **CLOSED and merged** (PR #945, `982eb6b`). `blockedBy` count is 0; the relocated helpers are on `main` in `internal/e2e/harness.go`. Unblocked.
**Prerequisite for:** #913 (removes the v1 relay dispatch branch). This ticket + #941 are the last two before #913.

---

## Files to read first

- `internal/e2e/relay_v2_daemon_test.go:44-73` — **`driveHandshakeToOpenDaemon(t, phone, pubKey, token) (initSend, initRecv *noise.CipherState)`**: the exact non-interactive Noise_IK handshake driver all three tests reuse. `initSend` seals phone→daemon; `initRecv` opens daemon→phone.
- `internal/e2e/relay_v2_daemon_test.go:89-186` — `testV2DaemonListConversationsRoundTrip`: the canonical **spawn → pair → decode pubkey → dial → handshake → sealed round-trip** template. Copy its shape. Proves an inbound verb round-trips over a *non-interactive* conn.
- `internal/e2e/relay_v2_daemon_test.go:249-266` — the local `roundTrip` closure (seal → `sendNoiseMsg` → `readInnerFrame` → `decryptInnerEnvelope`): the inline sealed request/reply idiom to mirror.
- `internal/e2e/relay_v2_daemon_test.go:373-396` — `decryptInnerEnvelope(t, inner, cs)`: sealed inner-frame → `protocol.Envelope`. Asserts the frame is `noise_msg`; does **not** assert the application `Type` — the caller does.
- `internal/e2e/relay_v2_handshake_test.go:149-177,341` — `sendNoiseInit`, `readInnerFrame`, `sendNoiseMsg` signatures.
- `internal/e2e/relay_v2_handshake_test.go:179-201` — `buildHelloEarly(t, token)`: the **non-interactive** v2 hello the driver embeds (ProtocolVersions `["v2"]`, token, **no `Capabilities`**). Confirms the migration preserves the original tests' no-capability semantics.
- `internal/e2e/harness.go:856-912` — relocated helpers. **Keep** `shortHome` (856), `readPersistedServerID` (866), `relayTestLogger` (881), `mustJSON` (913) — all have many surviving v2 callers. **`recvEnvelope` (894-912) → DELETE**: its only two callers are the v1 `per_conversation` tests migrating away, and its own doc comment already states *"v2 tests cannot use this — their frames are Noise-encrypted."*
- `internal/e2e/harness.go:208-260` — `StartIn`, `StartInWithEnv(t, home, extraEnv []string, extraFlags ...string)`, `spawnWith`/`spawnOpts` — the daemon-start seams (unchanged; only the env/route args flip).
- `internal/e2e/respawn_after_eviction_test.go` — target 1. Lines to touch: `79-80` (route), `149-169` (Phase-3 dial+hello → handshake), `176-214` (Phase-4 send+drain → sealed), `266-267` (env).
- `internal/e2e/register_push_token_test.go` — target 2. Lines: `41-45` (env+route), `59-90` (dial+hello → handshake), `92-121` (register send/recv → sealed).
- `internal/e2e/per_conversation_eviction_test.go` — target 3. Lines: `85`,`178` (routes), `112-131` (Test-A send_message → sealed), `258-260` (env), `291-362` (rewrite `dialHelloPhone` + `createConversationViaPhone` to thread cipher states).
- `cmd/pyry/relay.go:455-466` — the **v2 `Handlers` map**: `send_message`, `create_conversation`, `register_push_token` are registered **unconditionally** (identical to `list_conversations`). No interactive-capability gate on inbound dispatch → the non-interactive handshake is sufficient. **Do not modify** (production, owned by #913).
- `docs/knowledge/codebase/941.md` — what the sibling relocated and why.

---

## Context

The legacy v1 relay leg (`PYRY_MOBILE_V2=0`, `/v1/server`, plaintext `hello`/`hello_ack`) is being retired in #913. Three e2e tests still **dial a phone and complete a handshake** before exercising daemon behaviour; once the v1 branch is gone a v1-handshaking phone can never open, so each test would hang. This ticket re-authors their dial + handshake over the sealed v2 Noise_IK path so their behaviour coverage survives:

1. `respawn_after_eviction_test.go` — idle-evict → inbound `send_message` respawn → wire ack.
2. `per_conversation_eviction_test.go` — two sub-tests (idle eviction, cross-discussion cap eviction) driving `create_conversation` / `send_message`.
3. `register_push_token_test.go` — verb ack + on-disk persistence of the `(Platform, PushToken, Name)` triple. **No v2 twin exists** — migrating (not deleting) is what preserves this coverage.

After this ticket, **no** e2e test uses `/v1/server`, `PYRY_MOBILE_V2=0`, or `ProtocolVersions: ["v1"]` (AC-4). The v1 auth-gate / fakephone / fakerelay production plumbing is untouched (owned by #913).

---

## Design

### Central decision 1 — the phone stays; only its transport migrates

The Technical Note asks whether the two session-lifecycle tests can **drop** the phone entirely (drive eviction/respawn phone-free).

**Evaluated → phone stays.** Eviction's *trigger* (idle timer / active cap) is already phone-free — the tests just wait and poll the registry. But each test's load-bearing assertion is the **inbound wire verb → dispatcher reply** round-trip: `send_message` → `ack` (respawn), `create_conversation` → `conversation_created` (per-conv), `register_push_token` → `ack` (push token). A phone-free driver (e.g. a local control-plane `attach`) would exercise a *different* reactivation entrypoint and forfeit exactly the "wire ack/verb path" coverage the Note says must not be lost. So the phone is retained; only its transport flips v1 → v2 Noise. This is the intended shape: [[architect-939-splits-relocate-set-vs-noise-migrate-set]].

### Central decision 2 — non-interactive handshake for all three

Use **`driveHandshakeToOpenDaemon`** (the non-interactive driver), *not* `driveHandshakeToOpenDaemonInteractive`. Rationale:

- The interactive capability gates only the **outbound structured turn stream** (`interactiveTurnEmitterV2` / `interactiveBroadcaster`), i.e. which conns *receive* assistant-turn / `message` envelopes. It does **not** gate inbound verb dispatch — `internal/dispatch` is capability-agnostic, and the v2 `Handlers` map (`relay.go:455-466`) registers all three verbs unconditionally, exactly as it does `list_conversations` (which already round-trips over a non-interactive conn in `relay_v2_daemon_test`).
- This matches the original v1 tests' semantics — they advertised **no capabilities** (`buildHelloEarly` embeds none).
- **It makes the reads single-frame.** A non-interactive conn receives **no unsolicited outbound** — pinned by `relay_two_phone_structured_test.go`'s phone-B negative (a non-interactive phone never receives a structured or coarse `message` envelope; the v2 coarse fan-out was removed in #699). So each request's reply is the first and only sealed frame, and the receive-nonce stays in lockstep. This is why the v1 tests' spinner/`message`-drain workaround (`respawn_after_eviction_test.go:191-214`, `recvEnvelope`) is **no longer needed** — there is nothing to drain.

> **Invariant the developer must respect:** Noise receive CipherState nonces are sequential — **every** binary→phone `noise_msg` frame must be `Decrypt`-ed in capture order or the next decrypt desyncs. On the non-interactive path the daemon emits exactly one frame per request, so one `decryptInnerEnvelope` per reply keeps the nonce aligned. Do not read-and-discard a frame without decrypting it.

### Central decision 3 — delete the now-dead `recvEnvelope`

After migration, `recvEnvelope` (plaintext drain, `harness.go:894`) has zero callers. Delete it. Note: this repo runs `staticcheck ./...` **untagged** (Makefile:65), so U1000 does not fire on `//go:build e2e` files, and `go test -tags e2e` compiles fine with an unused func — the deletion is not a hard gate-unblocker, but leaving dead test infra is a smell and the ticket's Technical Note anticipates its removal. Delete it as part of AC-4/AC-5 cleanup. (`mustJSON`, `shortHome`, `readPersistedServerID`, `relayTestLogger` keep their many v2 callers — do **not** touch them.)

### Per-file migration

The uniform transport swap (every target):

| From (v1) | To (v2) |
|---|---|
| env `PYRY_MOBILE_V2=0` | `PYRY_MOBILE_V2=1` |
| route `fr.URL()+"/v1/server"` | `fr.URL()+"/v2/server"` |
| `payload.Token` only | also `pubKey, _ := base64.StdEncoding.DecodeString(payload.ServerStaticPubkey)` |
| plaintext `hello` build + `phone.Send` + `phone.Receive` for `hello_ack` | `initSend, initRecv := driveHandshakeToOpenDaemon(t, phone, pubKey, payload.Token)` |
| `phone.Send(env)` (verb) | `ct, _ := initSend.Encrypt(mustMarshal(env)); sendNoiseMsg(t, phone, ct)` |
| `phone.Receive(...)` / `recvEnvelope(...)` (reply) | `reply := decryptInnerEnvelope(t, readInnerFrame(t, phone, timeout), initRecv)` then assert `reply.Type` |

All existing non-wire assertions (registry state, PID checks, eviction WARN fields, on-disk persistence, LRU victim selection, binding distinctness) are **unchanged** — they never depended on the transport.

New imports per target: `encoding/base64` (pubkey decode). `per_conversation` additionally needs `github.com/pyrycode/pyrycode/internal/noise` (its rewritten helpers name `*noise.CipherState`); the other two can rely on `:=` type inference and need no `noise` import. Drop no existing import that stays referenced (`protocol`, `fakephone`, `fakerelay`, `mustJSON`, etc. all remain).

**1. `register_push_token_test.go` (simplest — copy `relay_v2_daemon_test` shape):**
- After `decodePairPayload`, decode `pubKey`.
- `StartInWithEnv(t, home, []string{"PYRY_ALLOW_INSECURE_RELAY=1", "PYRY_MOBILE_V2=1"}, "-pyry-relay="+fr.URL()+"/v2/server")`.
- Replace the hello block (66-90) with the handshake driver.
- Seal the `register_push_token` envelope, `sendNoiseMsg`, then `ack := decryptInnerEnvelope(...)`; keep the `InReplyTo == reqID`, `ack.ID >= 2`, and `devices.Load` persistence assertions verbatim.

**2. `respawn_after_eviction_test.go`:**
- `startEvictionHarness` (244-293): env `PYRY_MOBILE_V2=1`; the route arrives via the `relayURL` parameter, so change the **call site** (`79-80`) to `/v2/server`.
- After `decodePairPayload` (50), decode `pubKey`.
- Phases 1-2 (capture PID, eviction WARN, PID-gone) and Phase 5 (new PID) unchanged.
- Phase 3 (132-169): keep `readPersistedServerID` + `fr.WaitBinary` + `fakephone.Dial`; replace the hello build/send/receive with `initSend, initRecv := driveHandshakeToOpenDaemon(...)`.
- Phase 4 (176-214): seal the `send_message`, `sendNoiseMsg`, then a **single** `ack := decryptInnerEnvelope(t, readInnerFrame(t, phone, 15*time.Second), initRecv)` — the 15s `readInnerFrame` timeout preserves the documented respawn-latency upper bound; the multi-envelope drain loop collapses (no interleaving on the non-interactive path). Keep `ack.InReplyTo == reqID` and the latency assertion.

**3. `per_conversation_eviction_test.go` (the helper rewrite):**
- `startPerConvHarness` (240-284): env `PYRY_MOBILE_V2=1`; call sites (85, 178) → `/v2/server`.
- Introduce a file-local session struct bundling the client + both cipher states (mirrors the existing `queuePhoneSession` / `modalPhoneSession` pattern in the v2 suite):

  ```go
  // contract only — developer writes the body
  type noisePhone struct {
      client *fakephone.Client
      send   *noise.CipherState // phone → daemon (seal)
      recv   *noise.CipherState // daemon → phone (open, sequential nonce)
  }
  ```

- **`dialHelloPhone` → returns `*noisePhone`.** Signature gains `pubKey []byte`: `dialHelloPhone(t, home, fr, pubKey, pairToken) *noisePhone`. Body: keep `readPersistedServerID` + `WaitBinary` + `fakephone.Dial`; replace the hello block (309-329) with `send, recv := driveHandshakeToOpenDaemon(t, phone, pubKey, pairToken)`; return `&noisePhone{phone, send, recv}`. (Rename to `dialNoisePhone` optional — 2 call sites; keep churn minimal.)
- **`createConversationViaPhone(t, np *noisePhone, reqID) string`.** Seal the all-null `create_conversation` via `np.send`, `sendNoiseMsg`, then `env := decryptInnerEnvelope(t, readInnerFrame(t, np.client, 15*time.Second), np.recv)`; assert `env.Type == protocol.TypeConversationCreated` and `InReplyTo == reqID`; unmarshal `ConversationCreatedPayload`, return `p.ID`. (Replaces the `recvEnvelope` drain — single frame now.)
- Both test bodies: decode `pubKey` after `decodePairPayload`; `phone := dialHelloPhone(t, home, fr, pubKey, pairPayload.Token)`; pass `phone` (now `*noisePhone`) to `createConversationViaPhone`.
- Test A's `send_message` block (112-131): seal via `phone.send`, `sendNoiseMsg`, then `ack := decryptInnerEnvelope(t, readInnerFrame(t, phone.client, 15*time.Second), phone.recv)`; keep `InReplyTo` + `waitForSessionState(...,"active")` + `assertEvicted(boundB)`.
- The registry/binding helpers (`boundSessionID`, `assertEvicted`, `assertActive`, `assertDistinctIDs`, `waitForSessionState`, `waitForBootstrap`) are transport-independent — unchanged.

### 4. `harness.go`

Delete `recvEnvelope` (894-912) and its now-unused `deadline`/loop. No other change.

---

## Concurrency model

No new goroutines. The tests drive a real spawned `pyry` daemon over a `fakerelay`. The only concurrency contract that changes is the **Noise transport sequencing**: within one phone conn, seals (send-nonce) and opens (recv-nonce) each advance monotonically; because non-interactive conns receive exactly one daemon→phone frame per request, request/reply pairs stay in lockstep and no reordering window exists. The eviction machinery (idle timer, cap eviction, `Pool.Activate` respawn) runs entirely daemon-side and is untouched.

---

## Error handling

All failure paths remain `t.Fatalf`/`t.Errorf` (helpers are `t.Helper()`s). New failure surfaces introduced by the migration, all already handled by the reused primitives:

- Handshake failure → `driveHandshakeToOpenDaemon` fatals (`NewInitiator`, `WriteInit`, `ReadResp`, wrong inner type).
- Seal/open failure → `Encrypt`/`decryptInnerEnvelope` fatal.
- Wrong reply type → the caller's explicit `env.Type == want` assert fatals with the payload dumped (matches existing v2 tests).
- Reply timeout → `readInnerFrame`'s deadline (3s for handshake/verb replies; **15s** for the respawn/create-spawn replies that wait on `Pool.Activate`).

No new sentinel or wire code. No production error path touched.

---

## Testing strategy

The migrated tests **are** the deliverable; they self-verify.

- **Run:** `make e2e` (`go test -tags e2e -race -count=1 ./internal/e2e/...`). All three targets green, plus the rest of the e2e suite unbroken by the `recvEnvelope` deletion.
- **AC-4 sweep (deterministic):** after the change,
  ```
  grep -rn '/v1/server\|PYRY_MOBILE_V2=0\|ProtocolVersions: \[\]string{"v1"}' internal/e2e/*.go
  ```
  must return **no test-drive hits** (the fakerelay/fakephone plumbing definitions under `internal/e2e/internal/` and the harness's v1 auth support stay — they are production-parity plumbing owned by #913, not test drives).
- **Full gates (AC-5):** `make check` (`go vet ./...`, `go test -race ./...`, `staticcheck ./...`, substrate-guard) green; `go build` green. These run untagged, so they exercise the production tree only — the swap is test-only, so a green `make check` plus a green `make e2e` is the complete gate.
- **Behavioural equivalence to confirm** (highest-value manual check during review): each migrated test asserts the **same** post-conditions as before (respawn PID change + ack `InReplyTo`; per-conv LRU victim + binding distinctness; push-token triple on disk) — only the transport differs. A green run over the sealed path proves the daemon behaviour is identical whether the phone speaks v1 or v2.

---

## Open questions

1. **Helper name.** Keep `dialHelloPhone` (v1 vocabulary but only 2 call sites) or rename to `dialNoisePhone`? Recommendation: rename for honesty; it is cheap (2 sites, one file). Developer's call — not a correctness issue.
2. **Single-frame vs defensive drain.** The spec uses single-frame sealed reads, justified by the non-interactive no-unsolicited-outbound invariant. If — contrary to the `relay_two_phone_structured_test` guarantee — the developer *observes* an interleaving frame during implementation, the correct fix is a small local **decrypt-in-order** drain (decrypt every frame, skip non-matching, return on `want`), **never** a read-and-discard (which desyncs the recv nonce). Do not pre-build the drain; single-frame is correct for the non-interactive path.
3. **`register_push_token` interactive?** Confirmed non-interactive is sufficient (device-management verb, no structured stream). No capability advertisement needed — matches the original test.
