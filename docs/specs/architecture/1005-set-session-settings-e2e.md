# Spec — #1005 `set_session_settings` e2e (persist + reply + reject-invalid)

**Size:** S (single new `//go:build e2e` test file; **0 production files**; ~250 LOC).
**Security-sensitive:** yes — this test IS the wire-boundary proof of the argv-injection defense (`validModel`/`validEffort` reject an untrusted `model`/`effort` before it can reach the claude spawn argv). Security-review pass appended at the bottom (verdict PASS).

The production code (`handleSetSessionSettings` + the `settingsUpdaterAdapter` wiring) already shipped in **#845/#840/#833**. This ticket adds **only** an end-to-end test that exercises that already-wired path over the encrypted v2 wire. No production edits.

---

## Files to read first

Turn-1 data load. Read these before writing a line; do not re-discover them by grepping.

**The contract under test (production, read-only):**
- `internal/relay/v2session.go:2745-2830` — `handleSetSessionSettings`: the exact 5-step order the test asserts against — interactive gate → decode → `validModel`/`validEffort` (BEFORE persist) → nil-seam guard → `UpdateSettings` + reply `session_settings_updated`. Reply built at :2818; success log at :2827 logs **conn_id + session_id only, never model/effort/yolo**.
- `internal/relay/v2session.go:2704-2712` — the three fixed reject constants. `msgSettingsMalformed = "malformed set_session_settings request"` is what AC-3 asserts. **It is package-private (`internal/relay`) — the e2e cannot import it; assert the literal string.**
- `internal/relay/v2session.go:2887-2923` — `validModel` (a **shape** check: 1..64 bytes, first byte alphanumeric, charset `[A-Za-z0-9._-]`; `""` allowed) and `validEffort` (a **closed enum** `{"", low, medium, high, xhigh, max}`). **This drives the choice of non-vacuous invalid values (see § Design).**
- `internal/relay/v2session.go:2847-2875` — `settingsReplyError`: emits `TypeError` with an `ErrorPayload{Code, Message, Retryable}` correlated by `InReplyTo`; the reject log records `code + conn_id` only, never the value.
- `internal/protocol/settings.go` — `SetSessionSettingsPayload{SessionID string; Model,Effort *string; YOLO *bool}` (pointer = presence contract) and `SessionSettingsUpdatedPayload{SessionID string}` (reply echoes only the id).
- `internal/protocol/handshake.go:81` — `ErrorPayload` shape (decode the reject reply into this).
- `internal/protocol/codes.go` — `TypeError = "error"` (:44), `CodeProtocolMalformed = "protocol.malformed"` (:10), `TypeSetSessionSettings`/`TypeSessionSettingsUpdated` (:453-454).

**The e2e harness to ride (do NOT extract new infra):**
- `internal/e2e/relay_v2_promote_test.go` — **the reply-bearing template.** Copy its shape: `json.Marshal(Envelope{...})` → `initSend.Encrypt` → `sendNoiseMsg` → `decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)` → assert `Type` + `InReplyTo == reqID` → decode payload → read the on-disk registry back with a **local anonymous decode struct** (lines 141-167 — mirror this exactly; do not touch shared types).
- `internal/e2e/relay_v2_new_session_test.go:49-178` — **the interactive-verb template.** Reuse: `StartRotationWithRelay(... "PYRY_MOBILE_V2=1")` with a **never-created trigger path**, `driveHandshakeToOpenDaemonInteractive` (the interactive grant `handleSetSessionSettings` requires), `waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)` as the precondition, and the `regPath := filepath.Join(home, ".pyry", "test", "sessions.json")` read pattern. **Do NOT copy its `PYRY_FAKE_CLAUDE_CLEAR_ROTATES` / stdin-log machinery — this ticket does not rotate.**
- `internal/e2e/relay_two_phone_structured_test.go:501-543` — `driveHandshakeToOpenDaemonInteractive`: drives the Noise_IK handshake advertising the `interactive` capability and asserts the daemon granted it. Returns `(initSend, initRecv)` CipherStates.
- `internal/e2e/harness.go:309-366` — `StartRotationWithRelay` signature/semantics (pre-seeds the bootstrap registry at `initialUUID`, sets `PYRY_ALLOW_INSECURE_RELAY`, spawns fakeclaude). `Harness.Stderr` is a `*safeBuffer` (:102) with `.String()` — **this is the daemon-log capture the AC-3 no-leak assertion scans.** `seedBootstrapRegistry` (:~380) seeds the bootstrap with **no settings** → the on-disk baseline is `model:"" effort:"" yolo:false`.
- `internal/e2e/rotation_test.go:36,111,143,158` — `claudeSessionsDir`, `waitForBootstrapID`, `readBootstrap`, `readBootstrapIfPresent`. Reuse.
- `internal/e2e/restart_test.go:17-29` — the shared local `registryEntry`/`registryFile` decode structs. **They lack `Model`/`Effort`/`YOLO` — do NOT extend them** (shared across tests; editing them is a file-overlap risk). Define a small **local** settings-aware decode struct in the new file instead (see § Design).
- `internal/sessions/registry.go:23-38` — the authoritative on-disk JSON tags: `model,omitempty` / `effort,omitempty` / `yolo,omitempty`. Match these in the local decode struct.
- `internal/sessions/pool_update_settings_restart_test.go` — the **unit-tier** persistence shape this e2e mirrors (`diskSettings`, `SessionSettings{Model,Effort,YOLO}`). Read for the shape only; it is same-package and its helpers are not reachable from `internal/e2e`.

---

## Context

The `set_session_settings` v2 control verb lets a paired phone change a session's `model` / `effort` / `yolo`. It is **interactive-gated** and **request/reply** (unlike the fire-and-forget verbs). Its design is a validate-at-the-wire-boundary argv-injection defense: an untrusted `model`/`effort` is checked by `validModel`/`validEffort` **before** any persistence, so a value shaped like a claude flag (`--dangerously-skip-permissions`, `--foo`) can never reach the spawn argv. Invalid values, decode failures, and unknown ids each yield a deterministic `TypeError` whose message is a **fixed constant** — no payload byte or value ever reaches the wire or a log.

Today this is proven only at the `handleSetSessionSettings` unit tier. This ticket proves it **end-to-end over the encrypted v2 wire**: a real spawned daemon, a real Noise_IK handshake, a sealed frame in, a sealed reply out, and the persisted change read back off disk — plus the rejection path exercising the argv-injection boundary. Split from #962 (four-verb batch, ~4× S); this is the `set_session_settings` slice.

The change takes effect on the session's **next spawn** (#833's argv path). Making a *running* session pick it up is the separate live-restart ticket **#842 — out of scope**. Asserting next-spawn argv is optional/if-cheap; the load-bearing observables are **reply + on-disk persistence + rejection**.

---

## Design

**One new file:** `internal/e2e/relay_v2_settings_test.go` (`//go:build e2e`, `package e2e`).

### Harness (mirror `relay_v2_new_session_test.go`)

```
StartRotationWithRelay(t, home, sessionsDir, initialUUID, neverCreated, stdinLog,
        fr.URL()+"/v2/server", "PYRY_MOBILE_V2=1")   // no CLEAR_ROTATES — no rotation here
```

- `PYRY_MOBILE_V2=1` is what wires the v2 manager and therefore `SettingsUpdater` (`cmd/pyry/relay.go:525`, `settingsUpdaterAdapter` over `*sessions.Pool`). Without it the handler's nil-seam guard fires and the reply is `server.binary_offline`/"unavailable" — not what we test.
- `initialUUID` is a fixed canonical UUIDv4 stem (e.g. `"77777777-7777-4777-8777-777777777777"`); `StartRotationWithRelay` pre-seeds the bootstrap registry at it, so the running bootstrap session's id **is known ahead of time** — this is the `SessionID` the frame addresses.
- `trigger` points at a never-created path (nothing rotates in this test); `stdinLog` is an unused temp path.
- Pair one device, `waitBinaryHello`, dial phone, then `driveHandshakeToOpenDaemon**Interactive**` → `(initSend, initRecv)` on an **interactive** conn (the capability the handler requires).
- **Precondition:** `waitForBootstrapID(t, regPath, initialUUID, 5*time.Second)` — the Pool must have registered the bootstrap at `initialUUID` before the frame, else `UpdateSettings` returns `ErrSessionNotFound` → `session.not_found` reply (not `session_settings_updated`). This wait also makes the persistence assertions non-vacuous.

### One test function, two phases on one daemon/conn

**`TestRelayV2_SetSessionSettings`** — valid-then-invalid against a single daemon (cheapest; strongest non-vacuity, because the invalid phase asserts against the **known** persisted state from the valid phase, not against the trivially-equal seeded default).

**Phase 1 — valid (AC-1, AC-2).** Send a sealed `set_session_settings` frame:
- `SetSessionSettingsPayload{SessionID: initialUUID, Model: ptr("opus"), Effort: ptr("high"), YOLO: ptr(true)}` on a distinct envelope `ID` (e.g. `reqID = 41`).
- Read the reply via `decryptInnerEnvelope(t, readInnerFrame(t, phone, 3*time.Second), initRecv)`. Assert:
  - `reply.Type == protocol.TypeSessionSettingsUpdated`
  - `reply.InReplyTo != nil && *reply.InReplyTo == reqID`  ← **AC-1 correlation**
  - decode `SessionSettingsUpdatedPayload`; `SessionID == initialUUID`
- Read `sessions.json` back with the local decode struct (below); find the bootstrap row (`bootstrap == true`, or id `== initialUUID`) and assert `model == "opus" && effort == "high" && yolo == true`.  ← **AC-2 persistence**, matching the `internal/sessions` on-disk shape.
- Note: this update live-restarts the bootstrap fakeclaude (fire-and-forget, #842/ADR 031) — harmless async side effect; `saveLocked` is atomic (temp+rename) so the disk read never sees a torn file, and the settings are persisted before the reply is sent.

**Phase 2 — invalid, table-driven (AC-3).** On the SAME interactive conn, for each case send a sealed frame and assert the rejection. **Values are chosen to be well-formed JSON strings that pass `json.Unmarshal` and reach `validModel`/`validEffort`, and are rejected THERE — not short-circuited by decode or the capability gate:**

| case | payload | why non-vacuous |
|---|---|---|
| `model-argv-injection` | `Model: ptr("--pyry-e2e-not-a-model")` | valid JSON string → decode succeeds → `validModel` rejects (first byte `-` is non-alnum: the exact argv-injection shape the defense bars). Sentinel never appears in any legitimate argv/log. |
| `effort-out-of-set` | `Effort: ptr("pyry-e2e-not-an-effort")` | valid JSON string → decode succeeds → `validEffort` rejects (out of the closed enum). Sentinel never appears in any legitimate argv/log. |

For each case assert, on a fresh distinct envelope `ID`:
- `reply.Type == protocol.TypeError` and `*reply.InReplyTo == reqID`
- decode `protocol.ErrorPayload`; `Code == protocol.CodeProtocolMalformed` and `Message == "malformed set_session_settings request"` (the literal `msgSettingsMalformed`; not importable).  ← **AC-3(a)**
- **no persistence** (AC-3(b)): re-read `sessions.json`; the bootstrap row still shows `model=="opus" && effort=="high" && yolo==true` (unchanged from Phase 1 — a bypass would have written the sentinel).
- **no leak** (AC-3(c)): the sentinel value string appears **nowhere** in the reply frame nor the daemon logs — `!bytes.Contains(rawReplyBytes, []byte(value))` (assert on the decrypted envelope JSON and `reply.Payload`) **and** `!strings.Contains(h.Stderr.String(), value)`.

**Non-vacuity is structural, not incidental** (call it out in a code comment): the invalid values are well-formed JSON strings, so `json.Unmarshal` into `*string` *cannot* fail → the malformed reply *cannot* originate at the decode step; the conn is interactive → it *cannot* originate at the capability gate; therefore it originates at `validModel`/`validEffort` — the argv-injection boundary. Phase 1 (a valid `model` string flowing decode → `validModel`(pass) → persist) proves the *same* decode path succeeds for a well-formed value, so the divergence is provably at the validator. **Do NOT pick a value that fails `json.Unmarshal` (e.g. a JSON number for `model`) — that would false-pass via the decode branch, which emits the identical reply.** **Do NOT pick a plain out-of-set model like `"gpt-4"` — `validModel` is a shape check, not an allowlist, so it would PASS and persist, failing the test.**

### Local decode struct (do not touch shared `registryEntry`)

Define in the new file only — the shared `restart_test.go` `registryEntry` lacks settings fields and must not grow them here:

```go
type settingsRow struct {
    ID        string `json:"id"`
    Bootstrap bool   `json:"bootstrap"`
    Model     string `json:"model"`
    Effort    string `json:"effort"`
    YOLO      bool   `json:"yolo"`
}
// { "version":1, "sessions":[ settingsRow, ... ] }
```

A small `readBootstrapSettings(t, regPath) settingsRow` helper decodes `sessions.json` and returns the `Bootstrap == true` row. Local to this file.

### Frame-send helper

A small local `sendSettingsFrame(t, phone, cs, reqID, payload)` (marshal `Envelope{ID, Type: protocol.TypeSetSessionSettings, TS: time.Now().UTC(), Payload: mustJSON(t, payload)}` → `cs.Encrypt` → `sendNoiseMsg`) — mirrors `sendNewSessionFrame`. `ptr[T](v T) *T` is a one-line generic helper if not already present in the package (grep first; reuse if it exists).

---

## Concurrency model

- The phone conn is **single-writer** (the test's main goroutine). The verb is strict request/reply: **send one frame, read its one reply, before sending the next.** This keeps the Noise send/recv nonce sequences aligned (each `Encrypt`/`Decrypt` increments a nonce; interleaving sends without reading replies would desync `initRecv`).
- Each frame uses a **distinct envelope `ID`** so `InReplyTo` correlation is meaningful (a repeated id would make the correlation assertion vacuous).
- The valid-phase `UpdateSettings` fires a **fire-and-forget** bootstrap restart on a daemon goroutine (#842/ADR 031). The test never waits on it; the persisted settings and the reply are both produced before it matters, and atomic registry writes prevent torn reads.

---

## Error handling (failure modes the test must be robust to)

- **Bootstrap not yet registered:** guarded by `waitForBootstrapID(... initialUUID ...)` before the first frame. Skipping it risks a `session.not_found` reply instead of `session_settings_updated`.
- **Reply-read timeout:** `readInnerFrame(t, phone, 3*time.Second)` fails loudly (as in the promote test) — no silent hang.
- **Log-leak negative asserted too early:** the value is never logged by design, so absence is timing-independent; still, read `h.Stderr.String()` *after* the reject reply lands (the frame is fully processed by then). An optional short bounded re-read closes any cross-goroutine flush window (mirrors the sibling stdin-log poll) — belt-and-suspenders, not required.
- **Torn registry read:** impossible — `saveLocked` uses temp-file + `os.Rename`; a read sees either the pre- or post-write complete file (both carry the Phase-1 settings by Phase 2).

---

## Testing strategy

The test **is** the deliverable. Verification: `make e2e` green with `-race` (AC-4). Run locally:

```
go test -race -tags e2e -run TestRelayV2_SetSessionSettings ./internal/e2e/
make e2e   # full tagged suite, -race
```

Scenarios (bullet form; write in the project idiom, not pre-written here):
- **AC-1/AC-2 valid:** interactive handshake → `set_session_settings{opus,high,true}` → `session_settings_updated` reply with `InReplyTo == reqID` and `SessionID == initialUUID`; `sessions.json` bootstrap row == `opus/high/true`.
- **AC-3 reject (model argv-injection):** `Model:"--pyry-e2e-not-a-model"` → `TypeError` / `protocol.malformed` / `"malformed set_session_settings request"`; disk unchanged (`opus/high/true`); sentinel absent from reply frame + `h.Stderr`.
- **AC-3 reject (effort out-of-set):** `Effort:"pyry-e2e-not-an-effort"` → same reject assertions; disk unchanged; sentinel absent.

---

## Open questions

- **Table vs. two functions.** Recommended: one function, valid-then-table-driven-invalid, one daemon (cheapest, strongest local non-vacuity). If the developer prefers failure isolation, splitting the invalid cases into a second function against a fresh daemon is acceptable but doubles the daemon spawn (CI cost). Either satisfies the ACs.
- **Next-spawn argv assertion.** Optional/if-cheap per the ticket. The fakeclaude restart argv is not trivially observable without the rotation/argv-recorder machinery this test deliberately omits; **skip it** — reply + on-disk persistence are the load-bearing observables. Do not pull in argv-recorder infra for it.
- **`ptr` helper location.** Grep the `e2e` package first; a generic `ptr[T]` may already exist. Reuse over redefine.

---

## Security review

**Verdict:** PASS

This is a **test-only** spec (0 production files). It adds no new trust surface — it *asserts* an existing one: the inbound-untrusted `model`/`effort` → claude spawn argv boundary defended by `validModel`/`validEffort` in `handleSetSessionSettings` (#845, already shipped). The adversarial lens here is: does the test actually exercise that boundary, or a decoy that greens without proving anything?

**Findings:**

- **[Trust boundaries]** No findings. The untrusted input (`SetSessionSettingsPayload.Model`/`Effort`, phone-supplied) crosses the boundary sealed under Noise_IK, is decoded at `internal/relay/v2session.go:2751`, and is gated by `validModel`/`validEffort` at :2760-2767 **before** `UpdateSettings` (:2778). The test drives real values across this exact boundary **over the encrypted wire** (not a unit call), on an **interactive** conn so the negative cases reach the validator instead of short-circuiting at the capability gate (:2746) — satisfying the ticket's "exercise that boundary over the wire" requirement.
- **[Subprocess / external command execution]** No findings — and this is the security heart. The verb's whole purpose is keeping an untrusted value out of `exec.Command` argv. The `model-argv-injection` case (`"--pyry-e2e-not-a-model"`, a value shaped like a claude CLI flag) asserts (a) rejection with the fixed message and (b) **no persistence** (disk still shows the Phase-1 value), which proves the value never reaches the next-spawn argv. Asserted, not assumed. The test spawns the daemon only via the existing harness (`StartRotationWithRelay` → `spawnWith`, fixed argv, no `sh -c`).
- **[Error messages, logs, telemetry]** No findings — this is directly pinned by AC-3(c): the untrusted value must appear in **neither** the reply frame **nor** the daemon logs (`h.Stderr.String()`). That asserts the handler's no-echo-on-error contract (`encoding/json` quotes attacker bytes into decode-error strings; the handler never forwards them, and the reject log records only `code + conn_id`). Sentinel values are chosen to be unique strings that cannot collide with any legitimate argv/log token — **explicitly not `--dangerously-skip-permissions`**, which the valid `YOLO:true` spawn emits legitimately — so the no-leak assertion can neither false-pass on a coincidental match nor false-fail on a legitimate one.
- **[Non-vacuity — the dominant adversarial concern for this test]** No findings. The failure mode is a rejection firing at the *wrong* stage (decode or capability gate), which yields the identical `TypeError`/`protocol.malformed` reply and a green-but-meaningless test. The design forecloses it structurally: invalid values are **well-formed JSON strings** (`json.Unmarshal` into `*string` cannot fail → decode cannot be the rejector) sent on an **interactive** conn (gate cannot be the rejector) ⇒ the rejection provably originates at `validModel`/`validEffort`; Phase 1 proves the same decode path admits a valid `model`. Explicit "do not pick a JSON number" and "do not pick a shape-valid out-of-set model like `gpt-4`" guards are in § Design.
- **[Cryptographic primitives]** No findings. The test reuses the existing Noise_IK handshake helpers (`driveHandshakeToOpenDaemonInteractive` → `noise.NewInitiator`, `ecdh.X25519().GenerateKey(rand.Reader)`, `CipherState.Encrypt`/`Decrypt`). Nonce safety is addressed in § Concurrency: strict send-one/read-its-reply ordering on a single-writer conn keeps the send/recv nonce sequences aligned — no reuse. No hand-rolled crypto.
- **[Tokens, secrets, credentials]** No findings. The test obtains the pairing token via the existing `RunBareIn(... "pair")` / `decodePairPayload` helpers (production `pair` verb owns generation/storage). No new token handling; the token is not logged or echoed beyond the shared helpers.
- **[File operations]** No findings. The test's only filesystem interaction is a **read-only** decode of `sessions.json` at a fixed path (`filepath.Join(home, ".pyry", "test", "sessions.json")` — no untrusted-input concatenation, no traversal) and a read of captured stderr. No test-side writes; no TOCTOU (the daemon's `saveLocked` is temp-file+rename, so the read sees a complete file).
- **[Network & I/O]** No findings — no new server/socket surface. Every wire read is bounded (`readInnerFrame(t, phone, 3*time.Second)`); input-size caps and timeouts are the daemon's existing, unchanged concern.
- **[Concurrency]** No findings. Single-writer phone conn, strict request/reply. The Phase-1 update fires a daemon-side fire-and-forget bootstrap restart (#842/ADR 031); the test never waits on it, and atomic registry writes prevent torn reads. The test spawns no goroutines of its own beyond the harness's; teardown is via `t.Cleanup`.
- **[Threat model alignment]** No findings. `docs/protocol-mobile.md` § Security model requires untrusted mobile-client input never reach privileged execution; this test is the wire-level proof of exactly that threat for the settings verb. **OUT OF SCOPE:** making a *running* session pick up the change (live restart) — owned by **#842**, named out of scope in § Context.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-07-16
