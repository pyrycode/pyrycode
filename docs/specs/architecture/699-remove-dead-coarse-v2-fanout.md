# Spec #699 — Remove the dead coarse / non-interactive `message` fan-out (v2)

**Ticket:** [#699](https://github.com/pyrycode/pyrycode/issues/699)
**Size:** S (deletion-dominated; held at PO's `size:s`)
**Labels:** `security-sensitive` — a security-review pass is included below (§ Security review).

---

## Files to read first

Read these before editing. Line ranges are current as of this spec.

- `cmd/pyry/assistant_turn_v2.go` (whole file, 218 lines) — **the deletion target.** The coarse v2 emitter (`assistantTurnEmitterV2`, `newAssistantTurnEmitterV2`, `startAssistantTurnBridgeV2`) and the `v2Broadcaster` interface. Its gate `if c.Interactive { continue }` (line 151) is the complement of the structured filter; the whole file is dead.
- `cmd/pyry/relay.go:257-273` — `startRelayV2` doc comment; the paragraph describing the v2 assistant-turn bridge is removed.
- `cmd/pyry/relay.go:334-342` — the coarse-bridge wiring block (`var bridgeCleanup func()` + the `startAssistantTurnBridgeV2` call) — removed.
- `cmd/pyry/relay.go:369-382` — the `startRelayV2` return-cleanup; the `bridgeCleanup` call is removed and the comment reworded.
- `cmd/pyry/assistant_turn_v2_test.go` (whole file) — **deletion target.** Defines `pushCall`, `stubV2Broadcaster`, `newStubV2Broadcaster`, `nonInteractive`, `drainPushes`, `msgIDOf`, and the `TestAssistantTurnEmitterV2_*` tests. **Verified: none of these symbols are referenced by any other `cmd/pyry` test** — clean deletion. (`stubCursor`, `discardLogger`, `testConvID`, `testChunk` are shared helpers but are *defined elsewhere*, in `assistant_turn_test.go`; they survive.)
- `internal/e2e/relay_two_phone_coarse_test.go` (whole file) — **deletion target, but with a helper-relocation wrinkle (§ Design, step 4).** Defines `TestTwoPhoneCoarse_NonInteractiveOnly` (delete) **and two helpers the structured e2e still uses**: `buildHelloEarlyInteractive` (line 278) and `driveHandshakeToOpenDaemonInteractive` (line 307).
- `internal/e2e/relay_two_phone_structured_test.go:187, 397` — calls `driveHandshakeToOpenDaemonInteractive`; its comment at :397 references the coarse file. This is the surviving consumer of the two helpers above.
- `cmd/pyry/interactive_turn_v2.go:334` and `cmd/pyry/session_transition_v2.go:129` — the structured-path guards `if !c.Interactive { continue }`. **These stay** (see § Design, "What we keep" and § Security review). They are the reason `ActiveConn.Interactive` is *not* dead after this change.
- `internal/relay/v2session.go:1984-2082` — `ActiveConn` struct, `ActiveConns`, `handleActiveConns`; the `Interactive` flag plumbing. **Untouched** — `internal/relay` is not in this ticket's diff.
- `docs/protocol-mobile.md:411, 468-485` — the `message` wire-type row and the capability-negotiation section; the live-routing-path prose for the coarse fan-out is trimmed (AC#5).
- `cmd/pyry/assistant_turn.go:18-24` — `assistantTurnQueueSize` + `cursorReader` are defined **here (the v1 file)**, not in the file being deleted. Confirms the deletion is self-contained.

---

## Context

Per the 2026-06-22 amendment to **ADR 025**, pyrycode ships the app and daemon together; there is no old-app install base, so every phone always negotiates the `interactive` capability. That makes the **v2 (Noise)** coarse / non-interactive `message` fan-out dead code:

- The coarse v2 emitter (`assistantTurnEmitterV2`) gates with `if c.Interactive { continue }` — it fans a coarse `message` only to **non-interactive** conns. With no non-interactive conn ever existing, its delivery loop body is unreachable.
- It is the exact complement of #632's interactive-only structured filter, so the two delivery paths were always mutually exclusive per conn. Removing the coarse half leaves the structured stream as the single assistant-turn delivery path.

This is the **v2** path only. The pre-v2 dispatch-leg coarse bridge (`cmd/pyry/assistant_turn.go` / `startAssistantTurnBridge`, wired when `v2Enabled == false`) is a different, older mechanism, has no capability gate, still produces `message`, and is **out of scope**. Consequently the `TypeMessage` / `MessagePayload` wire constants in `internal/protocol` **stay** — they are still produced by the v1 bridge, `internal/turnbridge/outbound.go`, and `internal/relay/handlers/send_message.go`.

---

## Design

A purely subtractive change across two production files, two test files, and one doc. No new types, no signature changes, no consumer cascade.

### What we remove

1. **`cmd/pyry/assistant_turn_v2.go`** — delete the whole file. `startAssistantTurnBridgeV2` has exactly **one** call site (`relay.go:341`, removed in step 2); `assistantTurnEmitterV2` / `newAssistantTurnEmitterV2` / `v2Broadcaster` are referenced only from this file and from *doc comments* in sibling files (cosmetic — see step 5).

2. **`cmd/pyry/relay.go`** — three edits inside `startRelayV2`:
   - Remove the doc-comment paragraph (≈ lines 257-261) describing the v2 assistant-turn bridge.
   - Remove the wiring block (≈ lines 334-342): the `// Tap the PTY output…` comment, `var bridgeCleanup func()`, and the `if bridge != nil { bridgeCleanup = startAssistantTurnBridgeV2(...) }`.
   - In the return-cleanup (≈ lines 369-382): remove `if bridgeCleanup != nil { bridgeCleanup() }` and reword the leading comment (it currently says "Stop both producers"; after removal the cleanup stops the structured-stream producer and the session-transition producer).
   - **Verify after editing:** `sup` and `bridge` are still used (`sup` is the `Snapshotter` at ~line 316 and is passed to `startInteractiveTurnStreamV2`; `bridge != nil` still gates the structured stream at ~line 351). No parameter becomes unused — no signature change to `startRelayV2`.

3. **`cmd/pyry/assistant_turn_v2_test.go`** — delete the whole file (the coarse emitter unit tests). Clean: no other `cmd/pyry` test references its locally-defined helpers.

4. **`internal/e2e/relay_two_phone_coarse_test.go`** — delete the coarse two-phone e2e **but relocate the two shared helpers first.** `buildHelloEarlyInteractive` and `driveHandshakeToOpenDaemonInteractive` are defined here yet consumed by `relay_two_phone_structured_test.go:187`. Deleting the file wholesale breaks the structured test's compile.
   - **Move both helpers into `relay_two_phone_structured_test.go`** (their sole surviving consumer), then delete `relay_two_phone_coarse_test.go`.
   - Reconcile imports: the structured file already calls `driveHandshakeToOpenDaemonInteractive`, so the `noise` / `fakephone` imports it needs are largely present; add any the moved helper bodies require and drop any the coarse file no longer justifies.
   - Update the stale cross-file comment at `relay_two_phone_structured_test.go:397` (it points at `relay_two_phone_coarse_test.go`).

5. **`docs/protocol-mobile.md`** — trim the coarse fan-out so it is no longer a live routing path (AC#5):
   - **§ Capability negotiation (line ~485):** remove/rewrite the sentence *"A phone that does not advertise `interactive` … continues to receive the coarse v1 `message` fan-out only."* Per the ADR amendment every v2 phone is interactive; there is no non-interactive coarse routing. Keep the intersection/`negotiateCapabilities` description (the `capabilities` field still round-trips, AC#3) and the existing 2026-06-22 superseded-requirement note (line 470).
   - **§ Application message types (line 411):** annotate the `message` binary→phone row so it no longer reads as a live v2 delivery path — e.g. note it is the v1 / dispatch-leg coarse type, not minted on the v2 interactive path. Do **not** delete the row (the wire constant still exists). Editorial judgment; AC#5 is the contract.

   *(Cosmetic, optional, not required for AC: doc comments in `interactive_turn_v2.go` / `session_transition_v2.go` that say "mirrors `assistantTurnEmitterV2`" now reference a deleted type. Go does not validate comment references; leave or lightly reword — do not spend turns on it.)*

### What we keep (the architect's ADR call — plumbing stays inert)

The ADR/ticket leave it to the architect whether to also strip the now-inert capability plumbing (`ActiveConn.Interactive`, `V2Session.interactive`, and the structured-path guards `if !c.Interactive { continue }`). **Decision: keep all of it inert; do not touch `internal/relay`.** Three reasons:

1. **`ActiveConn.Interactive` is not dead after this change.** It is still read by the two surviving structured-path guards (`interactive_turn_v2.go:334`, `session_transition_v2.go:129`). Removing the field forces removing those guards *and* the `V2Session.interactive` / `handleActiveConns` plumbing in `internal/relay/v2session.go` — a multi-package refactor crossing into a security-sensitive surface, past `size:s`. The ticket explicitly says: if stripping turns this into a multi-package refactor, ship it as a follow-up and leave the guards/field inert here.
2. **The guards are deterministic safety nets** (belt-and-suspenders). They remain correct: they are permanent no-ops *only because* every conn is interactive today. Removing them would change behaviour — a hypothetical non-interactive conn would begin receiving the structured stream — with no observed failure demanding it (evidence-based fix selection). Keeping them preserves the exact delivery boundary (§ Security review).
3. **The capability machinery stays regardless.** `capabilities` must keep round-tripping (AC#3), so `negotiateCapabilities` and the `interactive` flag it records stay alive anyway. Stripping the consumer-side guards while keeping the producer-side negotiation would be a half-measure.

A future follow-up *may* strip the inert guards + field once it is worth a dedicated `internal/relay` change; this spec does not file it (PO's call) and does not depend on it.

---

## Concurrency model

No change. The deleted emitter owned one goroutine (`assistantTurnEmitterV2.Run` draining a buffered channel, started by `startAssistantTurnBridgeV2`); removing the wiring removes that goroutine and its `SetOutputObserver` tap. The surviving producers (`startInteractiveTurnStreamV2`, `startSessionTransitionStreamV2`) and their cleanups are unchanged. `startRelayV2`'s drain ordering is preserved minus the `bridgeCleanup()` call.

One thing to confirm during implementation: the v2 `Bridge.SetOutputObserver` is single-slot. After this change only the **v1** path (`startAssistantTurnBridge`, out of scope) installs an output observer, and only when `v2Enabled == false`. On the v2 leg nothing now taps `Bridge.Write`'s output observer — that is correct: the structured stream consumes the supervisor session / JSONL, not the raw PTY output observer.

---

## Error handling

No new failure modes (purely subtractive). The deleted emitter's defensive branches (cursor-empty drop, `crypto/rand` failure, marshal failure, per-conn push error) and its PTY-bytes-never-logged contract disappear with the file. No surviving code depends on them.

---

## Testing strategy

- **Deletion of unit + e2e tests** is itself an AC (AC#4). After deleting `assistant_turn_v2_test.go` and `relay_two_phone_coarse_test.go` (with the helper relocation), grep the tree to confirm no remaining test asserts a coarse `message` fan-out to a non-interactive conn:
  - `grep -rn "Interactive: false" cmd/pyry internal/e2e` should surface only the surviving structured/session-transition negative tests (which assert non-interactive conns get **nothing**), not a coarse-delivery assertion.
- **Regression guard for the e2e relocation:** `go test ./internal/e2e/...` must still compile and pass — this is the canary that `driveHandshakeToOpenDaemonInteractive` / `buildHelloEarlyInteractive` were relocated correctly.
- **AC#2 (no v2 conn left without assistant output):** covered by the surviving `relay_two_phone_structured_test.go` (interactive phone receives the structured stream). No new test needed — the structured path is unchanged.
- **AC#3 (`capabilities` round-trips):** unchanged wire behaviour; existing `internal/relay` capability-negotiation tests already cover it and are untouched.
- **Gate:** `make check` stays green — `go vet`, `go test -race`, `staticcheck`, and `cmd/substrate-guard`. Run it as the final step; a deletion that leaves a dangling reference fails `vet`/build immediately.

---

## Security review (security-sensitive)

**Mindset:** treat the v2 phone as an untrusted, internet-exposed peer. The memory rule "deleting a routing/dispatch gate = changing it" applies — the coarse path's `if c.Interactive { continue }` is a delivery gate. Walk what reaches a phone before vs. after.

**Trust boundaries / data flows:**

- **Sealed delivery surface.** The coarse emitter read the supervisor cursor, minted a `message`, and `Push`ed it **sealed under the session CipherState** to every open *non-interactive* conn. Its security contract was "PTY bytes never logged; chunk reaches the phone only via the sealed `MessagePayload.Text`." Removal deletes a delivery surface — it opens none. No new bytes flow to any phone.
- **No widening of who receives what.** This is the load-bearing check, and the reason we **keep** the structured-path guards (`if !c.Interactive { continue }`):
  - *Interactive conn:* already received only the structured stream (the coarse gate skipped it). Unchanged.
  - *Non-interactive conn:* received the coarse `message`. After removal it receives nothing from the assistant-turn path. Crucially, because we leave the structured-path guards intact, a non-interactive conn still receives **nothing** from the structured stream either. The delivery boundary is byte-for-byte preserved. Had we removed those guards (the rejected refactor), a non-interactive conn would suddenly receive the structured stream — a confidentiality-relevant widening. We do not.
- **Authentication / session state untouched.** `internal/relay` (handshake, token validation, `V2StateOpen` gating, `ActiveConns` enumeration) is not in the diff. The set of conns eligible for any fan-out is computed exactly as before.
- **Wire shape preserved (AC#3).** `capabilities` in `hello` / `hello_ack` still round-trips; `negotiateCapabilities` is untouched. No protocol field added or removed; no `omitempty`/byte-compat regression. `TypeMessage` / `MessagePayload` constants stay (still produced by the out-of-scope v1 path).
- **Logging.** The deleted file held the only v2 PTY-chunk-never-logged surface for this path; removing it cannot introduce a leak. No surviving log call gains a chunk/secret field.
- **Untrusted-input surface.** None added. The `last_event_id` replay path, snapshot gating, and send-message handling are all untouched.

**Verdict: PASS.** The change is strictly subtractive on the daemon→phone delivery side, removes one sealed delivery surface, opens none, and preserves the exact per-conn delivery boundary by leaving the complementary structured-path guards in place. No trust boundary is relaxed; no untrusted-input surface is added; the wire shape is unchanged.

---

## Open questions

- **Doc `message` row (line 411):** keep-and-annotate vs. drop. Recommendation: **keep and annotate** — the `message` envelope type still exists on the wire (v1 / dispatch leg). Final wording is the developer's editorial call against AC#5 ("no longer presents the coarse … fan-out as a *live routing path*").
- **Inert-plumbing follow-up:** stripping `ActiveConn.Interactive` + the two structured-path guards + `V2Session.interactive` is a deliberate non-goal here (keeps the change in-package and within S, and the guards are cheap deterministic safety nets). Whether to ever file that cleanup is PO's call; it has no functional urgency.
