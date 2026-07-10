# #903 — e2e: a modal missed while disconnected is re-delivered exactly once and answerable exactly once on reconnect

**Size:** S · **Security-sensitive:** yes · **Package:** `internal/e2e` (test-only; no production change) · **Build tag:** `//go:build e2e`

Split from #829. This is the **modal half's** cross-layer proof for the reconnect-reconcile contract (ADR 025 → "Backpressure / replay"). It exercises the producer wired by **#877** (connect-time modal reconcile) end-to-end over a real (fake) daemon + Noise transport + relay routing + fakeclaude-raised permission modal, and — because the answer semantics are a security property — genuinely drives the double-answer path and observes it rejected.

## Files to read first

- `internal/e2e/relay_v2_modal_answer_test.go:46-183` — `modalHarness` + `bringUpModalHarness`: the exact daemon+relay+pairing recipe to **mirror** (pair `phone-a` `--allow-remote-permissions`; align sessions dir + pre-create `<initialUUID>.jsonl`; `StartRotationWithRelay(..., "PYRY_MOBILE_V2=1", "PYRY_FAKE_CLAUDE_MODAL_TRIGGER="+modalTrig)`; dial; handshake; build `sealSend`/`nextEnv`). Extract: the whole bring-up shape and the `sealSend`/`nextEnv` closure bodies.
- `internal/e2e/relay_v2_modal_answer_test.go:189-348` — `awaitModalShown` + `TestRelayV2_RemotePermissionAnswered`: the `modal_shown` capture, the fixed four option-ID assertion, the answer→`modal_dismissed`→keystroke-in-stdin-log oracle, and the **replay-rejection negative** with the "positives gate the negative" ordering. This is the double-answer template to reuse for AC3.
- `internal/e2e/relay_v2_two_head_modal_test.go:63-201` — `bringUpTwoHeadModalHarness`: the house pattern of **deliberately duplicating** the #791 bring-up (rather than mutating the byte-frozen `bringUpModalHarness`) when a variant needs more from the daemon. Mirror this to expose the reconnect ingredients.
- `internal/e2e/relay_two_phone_structured_test.go:456-511` — `driveHandshakeToOpenDaemonInteractive(t, phone, pubKey, token) (*noise.CipherState, *noise.CipherState)`: the **reconnect re-handshake** helper; asserts the interactive grant in the `hello_ack` early data (the precondition the reconcile rides).
- `internal/e2e/internal/fakephone/fakephone.go:66-78, 133-168, 182-193` — `Dial`, `ReceiveBytes` (deadline → `ErrReceiveTimeout`; **a timed-out conn cannot be reused** — coder/websocket closes it), `Close` (idempotent, unblocks an in-flight `Receive`). The reconnect = `Close()` + fresh `Dial()`.
- `internal/e2e/internal/fakerelay/fakerelay.go:22-35, 285-353` — fresh `conn_id` ("c-1","c-2",…) minted per `/v1/client` upgrade, and the **"No 30-second grace period"** deviation (line 33). The basis for the AC1 both-modes reasoning below.
- `internal/relay/v2session.go:1375-1418` — `handleNoiseInit` success tail: `s.interactive`/`s.state = V2StateOpen` set, push queue created, then `m.reconcileModals(ctx, s)` (the producer under test — fires on **every** fresh interactive open).
- `cmd/pyry/interactive_modal_v2.go:93-139, 214-246` — `handleModalShown` (Record mints the `modal_id`, arms the deny-timeout, then `broadcastInteractive`; **a raise with zero interactive conns still Records + arms**) and `broadcastInteractive` (raise-time only, `!c.Interactive` gate). Establishes the two facts the test's isolation rests on: (a) the modal is Recorded even when no phone is watching; (b) the live broadcast fires **only at raise**, so a phone that connects *after* the raise can receive the modal **only** via reconcile.
- `docs/knowledge/codebase/877.md` — the mechanism under test: reconcile is *"the first mechanism that reaches a brand-new WS connection, distinct from #874's within-grace hold and #875's eager flush (both of which only cover a surviving conn)."* This is the AC1 both-modes anchor.
- `docs/knowledge/decisions/025-mobile-remote-head-interactive-session.md` § "Backpressure / replay" (line 130) + § "Remote-permission model" (line 55) + the Security-model prose (line 168) — the ADR contract this test protects: control events never drop; one-time nonce; deny-on-timeout never auto-grants.
- Reusable freestanding helpers (call, do not redefine): `mustJSON` (`relay_auth_test.go:108`), `decryptInnerEnvelope` (`relay_v2_daemon_test.go:388`), `sendNoiseInit`/`readInnerFrame`/`sendNoiseMsg` (`relay_v2_handshake_test.go:149,166,341`), `relayTestLogger` (`relay_test.go:50`), `readPersistedServerID` (`relay_test.go:35`), `waitBinaryHello` (`relay_v2_daemon_test.go:78`), `encodeWorkdir` (`rotation_test.go:19`), `shortHome` (`relay_test.go:25`), `RunBareIn` (`harness.go:733`), `decodePairPayload` (`pair_test.go:277`), `StartRotationWithRelay` (`harness.go:319`).

## Context

`modal_shown` is broadcast exactly once, at raise time, to the interactive conns open at that instant (`broadcastInteractive`), with `EventID == nil` so it never enters the turn-event replay ring. A phone that connects or reconnects *after* the raise never learns a permission prompt is pending — it would silently ride the daemon's 2-minute deny-on-timeout window and be denied unseen. #877 fixed that: on a v2 session reaching `V2StateOpen` with the `interactive` capability, `reconcileModals` unicasts the still-outstanding `modal_shown` (same `modal_id`, same payload) to that connection.

#877 carries a relay-unit test. What no test yet carries is the **cross-layer proof** that a real fake client — disconnected, then reconnected over a fresh Noise handshake — sees the prompt exactly once and can answer it exactly once, with a second answer to the re-sent prompt genuinely rejected. Because the answer is bound to a one-time nonce that must never be spent twice (ADR 025 §3), a *vacuous* assertion here would falsely certify that reconnect cannot cause a double-answer. This ticket is that non-vacuous proof for the modal path; it protects mobile and desktop at once (both drive the same daemon reconcile).

### AC1 "both reconnect modes" — how one daemon-layer scenario covers both

AC1 asks the re-delivery to hold "in both reconnect modes: within the relay's 30s grace (same session) and after it (fresh attach)." At the **daemon layer this reduces to a single observable path**, and the reduction is a design fact, not a shortcut:

1. **Modal reconcile is grace-agnostic.** `reconcileModals` fires on *every* fresh interactive Noise handshake (`handleNoiseInit` success tail), regardless of how long the client was away. It reads current registry truth; a still-pending modal is re-sent, a resolved one is absent. There is no "how long was the client gone" branch to distinguish.
2. **The two product modes are the daemon's two *other* mechanisms — not this one.** Per `codebase/877.md`, the within-grace / same-session path (a *surviving* conn whose uplink blipped) is **#874's hold + #875's eager flush**; `modal_shown` never rides them because it is raised while the conn is absent and never enters the replay ring. #877 is specifically the brand-new-conn (fresh-attach) mechanism. So "within grace (same session)" for the modal signal is #874/#875's contract, owned and tested there; "after grace (fresh attach)" is #877's — this ticket's.
3. **The harness models only the brand-new-conn path, by design.** `fakerelay` mints a fresh `conn_id` per `/v1/client` upgrade and explicitly implements **no 30-second grace** (fakerelay.go:33). The reconnect recipe the ticket prescribes — `phone.Close()` + fresh `fakephone.Dial()` + re-handshake — is inherently a brand-new conn with fresh ephemeral Noise keys. A literal "same session within grace" reconnect (conn-identity preserved across the WS blip) is not expressible with `fakephone`/`fakerelay` and would require a live relay — out of scope for a daemon-layer test.

**Therefore:** this test drives the one daemon-observable reconnect path (fresh interactive handshake → reconcile), which is exactly the #877 mechanism this ticket is scoped to verify. It satisfies AC1's intent — *no modal is dropped when the client (re)connects after being absent at raise* — for every path this harness can reach. The spec states plainly (here and in Open questions) that the literal within-grace same-session mode is #874/#875's surviving-conn hold/flush, unreachable by this harness and covered by those tickets. We deliberately do **not** add a cosmetic "within grace" subtest over a graceless harness — a test labelled for a mode it cannot actually produce is exactly the false-certification this security-sensitive ticket exists to prevent.

## Design

One new file, `internal/e2e/relay_v2_modal_reconnect_test.go` (`//go:build e2e`, `package e2e`). No production change. Three test-local additions plus one test function; everything else reuses the freestanding helpers listed above.

### 1. `bringUpReconnectModalHarness` — daemon+relay+pairing, reconnect-ready

Mirrors `bringUpModalHarness` (the deliberate-duplication house pattern of `bringUpTwoHeadModalHarness`, keeping the frozen #791 harness untouched), but returns the **reconnect ingredients** instead of a single pre-dialed phone, so the test owns connect/disconnect/reconnect:

```go
type reconnectModalHarness struct {
    fr        *fakerelay.Server
    serverID  string
    pubKey    []byte
    token     string   // the paired phone-a token; reused verbatim on reconnect
    stdinLog  string   // fakeclaude keystroke oracle
    modalTrig string   // drop this file to raise the one modal
    h         *Harness // daemon handle (SocketPath, Stderr)
}

func bringUpReconnectModalHarness(t *testing.T) *reconnectModalHarness
```

Behaviour: identical setup to `bringUpModalHarness` up to (and including) `waitBinaryHello` — pair `phone-a` **`--allow-remote-permissions`** (the #702 device gate; without it the reconnected answer denies at the gate and the AC3 negatives go vacuous), align the sessions dir + pre-create `<initialUUID>.jsonl`, start the modal-trigger fakeclaude with `PYRY_MOBILE_V2=1`. It does **not** dial a phone. Returns the struct.

### 2. `openInteractivePhone` — one interactive conn + its seal/drain closures

Factors the `sealSend`/`nextEnv` closures (today inlined in `bringUpModalHarness`) into a reusable seam so a reconnect is a one-liner:

```go
type phoneSession struct {
    phone    *fakephone.Client
    sealSend func(env protocol.Envelope)
    nextEnv  func(deadline time.Time) (protocol.Envelope, bool)
}

// openInteractivePhone dials a fresh phone, drives the interactive Noise
// handshake with token, and returns the bound session. Reconnect is:
//   old.phone.Close(); ps := openInteractivePhone(t, h, "phone-a")
// with the SAME token — a brand-new conn_id + fresh CipherStates.
func openInteractivePhone(t *testing.T, h *reconnectModalHarness, deviceName string) *phoneSession
```

Behaviour: `fakephone.Dial(dialCtx, h.fr.URL(), h.serverID, h.token, deviceName)` → `t.Cleanup(phone.Close)` → `driveHandshakeToOpenDaemonInteractive(t, phone, h.pubKey, h.token)` → build `sealSend`/`nextEnv` over the returned CipherStates (bodies copied from `bringUpModalHarness:133-174` verbatim — they are frozen and correct). `nextEnv` keeps the single-long-deadline back-to-back-read discipline (never a short poll: a timed-out `fakephone` `Receive` closes the WS).

### 3. `assertModalShownFor` — decode one `modal_shown`, assert it is the outstanding permission modal

A thin helper mirroring `awaitModalShown`'s decode+guard, parameterised by an *expected* `modal_id` (empty ⇒ "capture, don't compare"): drains `nextEnv` under one deadline until a `TypeModalShown` arrives, decodes `ModalShownPayload`, asserts non-empty `modal_id` (and, when an expected id is given, equality), `Class == "permission"`, and the fixed four option IDs (`allow_once`/`allow_always`/`reject_once`/`reject_always`). Returns the payload. `t.Fatal` on an intervening `TypeError` or deadline. Used both to capture the original id (phone #1) and to assert the re-delivered id (phone #2).

### 4. Test flow — `TestRelayV2_ModalReconcileOnReconnect`

The isolation argument that makes the whole test non-vacuous: **phone #2 connects strictly after the modal is Recorded, so `broadcastInteractive` (raise-time only) never targeted it — reconcile is its sole possible delivery path.** Phone #1's *live* observation of the modal is the deterministic "modal Recorded & outstanding" fence and the source of the true original `modal_id` (the daemon logs no `modal_id` on the happy path, so a live phone observation is the only way to both fence the raise and learn the original nonce).

Steps (each a hard precondition of the next; assertion order is load-bearing, per the #791 "positives gate the negative" doctrine):

1. **Establish a prior session (phone #1) and raise the modal.** `ps1 := openInteractivePhone(...)`. Drop `modalTrig`; drain `ps1.nextEnv` until `modal_shown`; `assertModalShownFor(..., "")` captures the **original** `modalID` (X) and asserts Class + the four option IDs. This proves the modal is Recorded, armed, and outstanding.
2. **Disconnect (no client connected).** `ps1.phone.Close()`. The modal stays outstanding — a phone disconnect resolves nothing (only local/remote answer, remote cancel, or the deny-timeout resolve a modal). We are now well inside the 2-minute deny window (the reconnect is sub-second), so `Snapshot` will still hold X.
3. **Reconnect (fresh attach, the mode under test).** `ps2 := openInteractivePhone(...)` — same token → same device identity → brand-new `conn_id` + fresh Noise session → `handleNoiseInit` → `reconcileModals`.
4. **Re-delivered exactly once, same nonce (AC1 + AC2).** Drain `ps2.nextEnv`; `assertModalShownFor(..., X)` asserts the re-delivered `modal_shown` carries `modal_id == X` and the full permission payload (Class + option IDs). Then drain `ps2` for a short window and assert **no second** `modal_shown` for X — reconcile unicasts once, and no raise-time broadcast can reach ps2.
5. **Answerable exactly once — the answer routes (AC3, positive that gates the negative).** `ps2.sealSend` a `modal_answer{ModalID: X, OptionID: allow_always, AnswerToken: "e2e-903-answer"}`. Drain for `modal_dismissed{ModalID: X, Source: "remote", Outcome: "allow_always"}`. Then read `stdinLog` and require exactly one `"2\r"` (the chosen allow_always keystroke) and **no** `"1\r"` (the default) — proving the reconnected conn's answer actually reached claude. `t.Fatal` here *before* the double-answer negative, so the negative can never pass vacuously.
6. **Double-answer rejected (AC3, the security negative).** `ps2.sealSend` a **second** `modal_answer{ModalID: X, ...}`. Assert, under a short deadline, **no** second `modal_dismissed` and **no** `TypeError`, and that `stdinLog` is byte-for-byte unchanged from step 5 (the second answer routed nothing). The nonce was consumed by the first answer's `Registry.Resolve`; the re-send opened no second answer window.

## Concurrency model

Test-only; no new production goroutines. The test drives one real daemon subprocess through the relay. Discipline reused from the existing modal e2e:

- **Single-long-deadline drains.** Every wait is one deadline with back-to-back `nextEnv` reads, never a short poll on a `fakephone` conn (a timed-out `Receive` closes the WS and makes the conn unusable — fakephone.go:114-116). Delivery/dismissal waits use ~20 s; negative-window drains use ~3 s.
- **Happens-after fences.** `modal_dismissed` is the fence that the answer keystroke is already on disk (fakeclaude fsyncs stdin per write before the clear that triggers the broadcast), so step 5's `stdinLog` read is race-free. Phone #1's live `modal_shown` is the fence that the modal is Recorded before phone #2 handshakes.
- **Reconnect ordering.** `ps1.phone.Close()` fully returns before `openInteractivePhone` dials ps2, so ps2 is unambiguously a later, distinct `conn_id`. No shared mutable state between the two sessions (each owns its CipherStates).

## Error handling

Failure modes and the guard that keeps each from silently passing:

- **Modal never raised / not classified permission** → step 1 `t.Fatal` (`awaitModalShown`-style "harness produced no modal") before any reconnect.
- **Reconcile didn't fire / delivered nothing** → step 4 deadline `t.Fatal` naming the reconcile path.
- **Re-delivered id differs from X** → step 4 equality `t.Fatal` (would mean a re-mint, not a re-send — AC2 violation).
- **Answer didn't route** → step 5 keystroke-count `t.Fatal`, gating the negative.
- **Double-answer produced a dismissal or a stdin change** → step 6 `t.Fatal` (the double-answer security property is broken).
- **Flake guards:** run `-count=1` friendly (no wall-clock dependence beyond the deterministic sub-second reconnect vs. the 2-minute deny window — a >100× margin); `t.Skipf` is inherited only where the reused bring-up already skips (none PTY-dependent here — this test has no local attach head).

## Testing strategy

The deliverable **is** the test; "testing strategy" here is the mapping from ACs to assertions and the non-vacuity contract.

- **AC1 (delivered exactly once on reconnect, both modes):** steps 3-4 — reconcile re-delivers on the fresh interactive handshake; the short post-delivery drain proves *exactly once*. Both product modes collapse to this daemon path (see § "AC1 both reconnect modes"); the within-grace surviving-conn mode is #874/#875's, out of harness scope.
- **AC2 (same `modal_id` as original):** step 4 asserts `modal_id == X`, where X was captured from phone #1's *live* modal_shown — a true original, not a self-referential comparison.
- **AC3 (answerable exactly once; nonce preserved; no second window / no timer reset):**
  - *nonce preserved across the re-send* — the re-delivered id equals X and is answerable (step 5), so it is the real minted nonce, not a fresh or stub id.
  - *answerable exactly once* — step 5 (dismissal + exactly one chosen keystroke) then step 6 (no second dismissal, stdin unchanged). Ordering is load-bearing.
  - *no second answer window* — step 6 is exactly the observation that the window did not reopen.
  - *deny-on-timeout clock not reset* — **structural**, not separately asserted here: `reconcileModals` calls no arm/resolve path (proven by #877's unit test "No timer re-arm"). It is **not** e2e-distinguishable in this harness because the re-send happens sub-second after the raise, so any deny-timeout observation would fire at ≈2 min from *both* the raise and the re-send — a coarse timer cannot separate reset-from-not-reset. Re-proving it would need a live relay that can delay the re-send far from the raise; out of scope. The e2e's unique contribution is the cross-layer *exactly-once-answerability* proof, which the #877 unit test cannot give.
- **AC4 (daemon-layer e2e over the fake client, `//go:build e2e`, pre-ship gate no CI):** the whole file. Non-vacuity of the security-relevant negatives is enforced by the "positives gate the negative" ordering above.

Run: `go test -tags e2e -run TestRelayV2_ModalReconcileOnReconnect -race -count=1 ./internal/e2e/...`, plus `go vet ./...`. The e2e-tagged test must be run with `-tags e2e` and confirmed non-vacuous (it drives a real modal, a real answer, and a real rejected second answer — not a skipped or empty pass).

## Open questions

- **Within-grace / same-session reconnect (AC1's first clause), literally.** Not expressible with `fakephone`/`fakerelay` (no grace; fresh conn_id per upgrade) and is #874/#875's surviving-conn mechanism, not #877's. A literal proof would need a live relay that preserves conn identity across a WS blip — a live-stack operator run, tracked by whatever disposition #829 receives. This ticket asserts the daemon-observable fresh-attach path that both product modes converge on.
- **A second interactive *device* (not the same device reconnecting) receiving the re-send.** The #877 unit test's "only the opening conn receives it, not other open conns" already pins unicast at the relay layer; adding a second pairing at e2e would double the harness for marginal coverage. Deferred unless a reviewer wants the fresh-*device* flavor distinct from the fresh-*conn* one.
- **#878 queue twin.** The `queue_state` reconnect e2e is a sibling ticket (split from #829, same shape over the queue signal); this file does not pre-build any shared reconnect scaffolding for it beyond `openInteractivePhone`, which it may lift verbatim.

## Security review

**Verdict:** PASS

This ticket carries `security-sensitive` because it is the **security-property test** of the #829 split (per the split rationale: sec rides the vacuous-would-false-certify test, not the correctness one). The subject under review is unusual — the artifact is a *test*, and the adversarial question is not "does this code open an attack surface" but **"can this test pass while the guarantee it certifies is broken?"** A test that green-lights a broken one-time-nonce guarantee is itself the vulnerability. Each finding cites a concrete anchor in the design above.

**Findings:**

- **[Non-vacuity / false-certification — the crux]** No MUST-FIX; the design is fail-safe by construction, and the guardrail is the load-bearing assertion order.
  - *The double-answer negative cannot pass unless the first answer provably succeeded.* Step 5 `t.Fatal`s on (a) no `modal_dismissed{remote, allow_always}` and (b) not exactly one `"2\r"` keystroke, **before** step 6 sends the second answer. So "no second dismissal" can never be satisfied by a modal that was never answerable, never surfaced, or answered by a coincidental default — the exact vacuity mode the ticket warns about. This mirrors the proven ordering of `TestRelayV2_RemotePermissionAnswered` (relay_v2_modal_answer_test.go:217-347).
  - *The reconcile path is genuinely isolated.* Phone #2 connects *after* the modal is Recorded (step 1's live observation is the fence), and `broadcastInteractive` fans only at raise time to conns open *then* (interactive_modal_v2.go:222-246). So no live broadcast can reach ps2 — a green test proves the *reconcile*, not an accidental re-broadcast. Without this fence the test could pass on the wrong mechanism.
  - *"Same nonce" is compared to a true original.* X is captured from phone #1's live `modal_shown` (step 1), not fabricated, so step 4's equality is a real AC2 check.
- **[Tokens/secrets]** No MUST-FIX — the property under test, preserved end-to-end.
  - The `modal_id` is a one-time opaque nonce (#716); the test never mints one and asserts the daemon re-sends the *same* one and consumes it on the *first* answer (`Registry.Resolve` deletes on consumption). The second `modal_answer` for X is driven for real and observed inert — a live, non-vacuous demonstration that reconnect cannot re-open answerability.
  - `AnswerToken` is a client-minted test literal (`"e2e-903-answer"`) on the *inbound* `ModalAnswerPayload` only; the outbound re-sent `modal_shown` carries none (messaging.go:127-143). The test forges nothing on the outbound path because there is nothing to forge.
  - The daemon-side gates the test relies on are the real ones: Noise_IK auth (the reconnect re-handshakes; an unpaired/wrong-token peer never reaches the reconcile) and the `--allow-remote-permissions` device gate (the reconnect reuses the *same* token/device, so the answer is authorized exactly as a same-device reconnect should be). The test does not weaken, stub, or bypass either.
- **[Deny-on-timeout window]** No MUST-FIX. The re-send arms no timer (reconcile calls no `ArmModalTimeout`, structural per #877). The test does not shrink `modalDenyTimeout` (it cannot — the daemon is a subprocess), and its sub-second reconnect sits >100× inside the 2-minute window, so the modal is provably still outstanding at reconnect without the test racing the deny path. The one facet not e2e-distinguishable — "clock not reset" — is documented (Testing strategy, AC3) as structurally guaranteed and unfalsifiable-by-coarse-timer here, not silently dropped.
- **[Error messages, logs, telemetry]** No MUST-FIX. Failure output echoes the stdin log (per the reused pattern's own caution) and payloads; the only bytes in play are the ASCII answer keystroke `"2\r"`, the opaque nonce X, and test-literal tokens — no secret, no key material, no modal body beyond what the existing modal e2e already surfaces on failure. The file stays substrate-clean (no CSI `0x1b '['` form; the answer digit and CR only), matching relay_v2_modal_answer_test.go's file-level discipline.
- **[Network, I/O & DoS] / [Cryptographic primitives] / [File operations] / [Subprocess execution]** N/A for new surface — the test adds no production code, no new socket/TLS surface, no crypto (it reuses the transport's Noise handshake via `driveHandshakeToOpenDaemonInteractive`), and drives the same fakeclaude subprocess the existing e2e already spawns. The reconnect is `Close()` + `Dial()` on the in-process fakerelay; bounded, single-conn, no amplification.
- **[Concurrency]** No finding. Reconnect is strictly sequential (`ps1.phone.Close()` returns before ps2 dials); the two sessions share no mutable state; drains use the frozen single-deadline discipline that avoids the fakephone timed-out-conn-unusable hazard.
- **[Threat model alignment]** In scope and addressed: the ADR-025 risk that a reconnecting client double-answers a permission prompt (network reorder / replay onto a re-sent modal) is exactly what step 6 drives and refutes; the risk that reconnect silently drops the prompt (leaving claude to be denied-on-timeout unseen) is what steps 3-4 refute. Out of scope, each with an owner: the literal within-grace same-session mode (#874/#875 + a live-stack run); the fresh-*device* variant (#877 unit test pins unicast); the `queue_state` twin (#878). No new attack surface is introduced by a test-only change.

**Reviewer:** architect (self-review; `security-review.md` guidance not present in worktree — modelled on the #877 spec's security-review section and the pipeline's `## Security review`-last convention)
**Date:** 2026-07-10
