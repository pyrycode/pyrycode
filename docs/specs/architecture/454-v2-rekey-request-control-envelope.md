# 454 — v2 `rekey_request` control-envelope discriminator

Intercept AEAD-decrypted envelopes whose `type` is `rekey_request` at the v2 dispatch boundary so they are recognised as a Mobile Protocol v2 control envelope and short-circuited away from the application handler chain. The binary is always the IK responder (ADR 024) and takes no transport action on receipt — the envelope is informational; the design is logs-only with INFO/WARN classification of `payload.reason`.

Split from #449; sibling of #452 (peer-static accessor, landed) and #453 (re-key responder swap). Independent of #453 — both touch different functions in `internal/relay/v2session.go` and can ship in parallel.

## Files to read first

- `internal/relay/v2session.go:597-627` — `dispatchAppFrame`: the exact seam where the probe-and-switch lands (between `s.recv.Decrypt` and `dispatch.Route`).
- `internal/relay/v2session.go:560-580` — `handleNoiseMsg` `V2StateOpen` branch: the caller of `dispatchAppFrame`; AEAD-failure path stays unchanged.
- `internal/relay/v2session.go:107-159` — `V2SessionConfig`: synchronous-handler invariant doc-comment; the new `handleRekeyRequest` honours the same single-goroutine ownership.
- `internal/protocol/codes.go:36-62` — `Type*` constant block: insertion site for `TypeRekeyRequest`.
- `internal/protocol/envelope.go:101-118` — `v1TypeSet` map literal: the load-bearing absence — `TypeRekeyRequest` MUST NOT be added here.
- `internal/protocol/envelope.go:80-99` — `IsV1Compatible`: the predicate `dispatch.Route` consults; pinning the asymmetry below.
- `internal/dispatch/dispatch.go:555-585` — `Route`: the function the probe short-circuits past on a recognised `rekey_request`.
- `internal/relay/v2session_test.go:737-905` — open-state dispatch-test scaffold from #446: `openSession`, `driveToOpen`, `sealAppFrame`, `decryptAppFrame`, `v2Recorder`, `waitForEnvelopes`, `silentLogger`. New tests reuse these unchanged.
- `internal/relay/v2session_test.go:907-979` — `TestV2Session_OpenState_TamperedNoiseMsg_4421`: shape to mirror for the `atomic.Bool` handler-not-called pin (AC #4 sub-test "Control envelope intercepted").
- `internal/conversations/sweep_loop_test.go:43-46` — buffer-logger pattern (`slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})`) the unknown-reason test reuses to capture the WARN line.
- `docs/protocol-mobile.md:195-235` — § Re-key: the canonical `rekey_request` envelope shape + the three `reason` values.
- `docs/knowledge/codebase/446.md` — `dispatchAppFrame` seam introduced; the synchronous-handler ownership pattern this slice extends. § Patterns established names the contracts the probe-and-switch slots into.
- `docs/knowledge/decisions/024-noise-ik-mobile-e2e.md` — ADR 024: the binary is always the IK responder. Justifies "no transport action" on `rekey_request` receipt.

## Context

Mobile Protocol v2 (`docs/protocol-mobile.md` § Re-key) defines `rekey_request` as a control signal either side may emit to nudge the peer toward initiating a re-key handshake. Per ADR 024 the binary is always the IK responder; an incoming `rekey_request` therefore takes no transport action — the phone re-keys by sending `noise_init` directly. But the envelope shape exists on the wire (the binary itself emits `rekey_request` to nudge the phone — sibling ticket #450), so the binary must be prepared to receive one defensively.

#446 landed the open-state application dispatch path: `s.recv.Decrypt` → `dispatch.Route` → handler chain. `dispatch.Route` consults `v1TypeSet` to validate `Envelope.Type` and rejects unknown types with a sealed `protocol.unknown_type` reply. Two seams are possible for the discriminator:

1. **In `dispatchAppFrame`, before `Route`** — re-decode the plaintext as an `Envelope`, switch on `Type`, intercept `rekey_request`, otherwise call `Route` exactly as today.
2. **Inside `Route`** — extend the v1-or-not check to a third bucket "v2-control."

The ticket specifies seam (1) explicitly. Two reasons:

- `Route` is the v1 dispatch ladder; adding v2-control awareness inside it would invert the layering (`dispatch` would import nothing new, but its surface would carry v2 knowledge). The v2 manager already owns v2 concerns; pushing the discriminator into the manager keeps `internal/dispatch` v1-only and pure.
- Adding `TypeRekeyRequest` to `v1TypeSet` (the alternative seam shape) would make `Route` invoke the handler chain — exactly the opposite of what's wanted. The asymmetry "v2 control type exists as a `Type*` constant but does NOT appear in `v1TypeSet`" is load-bearing and must be pinned in both source-of-truth files via cross-referencing doc-comments.

The probe is intentionally a re-decode: `dispatch.Route` decodes the same plaintext a second time. The cost is one extra small JSON parse per application frame; the alternative — refactoring `Route` to accept a pre-decoded `Envelope` — touches the v1 dispatcher and its tests for no measurable benefit at this slice's traffic levels. Architect chose against the refactor.

## Design

### Constant: `protocol.TypeRekeyRequest`

Add one constant + doc-comment to `internal/protocol/codes.go`, in a new "v2 control" group placed after the v1 `Push` group (line 62):

- Name: `TypeRekeyRequest`
- Value: `"rekey_request"` (wire string from `docs/protocol-mobile.md` § Re-key)
- Doc-comment: states that this is a v2 control envelope and **MUST NOT** be added to `v1TypeSet` in `internal/protocol/envelope.go`; cross-references the v1TypeSet site so a future contributor adding "v2 control type" sees both halves of the asymmetry. One-line rationale: a leak into `v1TypeSet` would route the envelope to `dispatch.Route`'s handler chain in violation of AC #2.

The constant lives in `codes.go` with the other `Type*` constants — co-location is the project convention — but `envelope.go`'s `v1TypeSet` is the gate. Add a companion comment **above** the `v1TypeSet` map literal (`internal/protocol/envelope.go:101`) naming `TypeRekeyRequest` as the canonical example of a v2-only type that must stay out of this set. This is the "different fabric for belt-and-suspenders" pattern from the pipeline-wide principles: the constant's doc says "do not add me to v1TypeSet"; the v1TypeSet's doc says "v2-only types like TypeRekeyRequest stay out." Either edit alone is sufficient to catch the mistake at code-review time.

The `compat_test.go` drift detector at `internal/protocol/compat_test.go` (per `system-overview.md` "v1TypeSet covers all Type* constants") needs updating: the existing assertion that every `Type*` constant appears in `v1TypeSet` must be modified to exclude the new v2-control set. The simplest shape is an explicit allowlist literal inside the test of v2-only `Type*` constants that are deliberately absent from `v1TypeSet`. Today the allowlist is `{}`; this slice makes it `{TypeRekeyRequest}`. The test then asserts: every `Type*` constant is either in `v1TypeSet` OR in the v2-only allowlist; nothing in both. This is the codegen-of-the-asymmetry: any future v2 control type forces a contributor to amend two places — the type constant AND the test allowlist — both of which sit feet from the `v1TypeSet` definition.

### Probe-and-switch in `dispatchAppFrame`

Insert, at the **top** of `(*V2SessionManager).dispatchAppFrame` (immediately after the function signature, before the `outbound := make(...)` line on `v2session.go:598`), a five-line block:

- `json.Unmarshal(plaintext, &probeEnv)` into a local `protocol.Envelope`.
- If decode succeeds **and** `probeEnv.Type == protocol.TypeRekeyRequest` → call `m.handleRekeyRequest(ctx, s, probeEnv)` and `return`.
- Otherwise (decode failed, or type is anything else): fall through to the existing `outbound := make(chan ...)` body unchanged.

JSON decode failure deliberately falls through. The existing `dispatch.Route` then re-decodes, hits its own malformed branch (`Route` line 557-562), and emits the sealed `protocol.malformed` reply. The probe MUST NOT consume the malformed-envelope reply path that #446 established — pin this with an explicit comment on the probe block referencing `dispatch.Route`'s malformed handler.

The probe is single-decision and lives at the top of `dispatchAppFrame` rather than as a helper function: the cost of hoisting it (one new method, three lines of plumbing) exceeds its readability win at five lines inline.

### `handleRekeyRequest` method

New method on `*V2SessionManager`. Signature:

```
func (m *V2SessionManager) handleRekeyRequest(ctx context.Context, s *V2Session, env protocol.Envelope)
```

Body, in order:

1. Decode `env.Payload` into a local anonymous-or-named struct `struct{ Reason string `json:"reason"` }`. If decode fails, treat as empty reason (the spec allows `payload.reason` to be missing-or-bad and still classifies the frame as informational) — note that we deliberately do NOT emit a sealed `protocol.malformed` reply for a broken `rekey_request` payload because the envelope took no transport action either way; emitting a reply would be surprising.
2. Switch on the decoded `Reason`:
   - `"scheduled"`, `"manual"`, `"compromise"` → log at INFO with fields `event="v2.rekey.request.received"`, `conn_id=s.connID`, `reason=<value>`.
   - default (including empty, including the JSON-decode-failure case from step 1) → log at WARN with fields `event="v2.rekey.request.received"`, `conn_id=s.connID`, `reason=<raw value or "" on decode failure>`.
3. Return. No `m.send`, no `m.closeWith`, no `s.state` mutation, no outbound frame, no `*dispatch.Conn` allocation.

`ctx` is accepted in the signature for parity with `dispatchAppFrame` / `handleNoiseMsg` (the manager's dispatch-goroutine context) but is unused — the method does no work that needs cancellation. This matches the existing style in this file (`handleNoiseInit` takes `ctx` and only forwards it to `closeWith`).

The forward-compat for unknown reasons is deliberate: mobile may add a `reason` value before the binary catches up. Logging at WARN (rather than rejecting) is the correct posture for "future protocol value the binary doesn't recognise yet."

**Concurrency:** `handleRekeyRequest` runs on the manager's single dispatch goroutine — the same goroutine that owns `s.send` / `s.recv` and the `m.sessions` map. No new mutex, no new goroutine. The method satisfies the existing V2SessionManager invariant ("Run is the only goroutine the manager owns; sessions is mutated exclusively by Run") without modification.

### What does NOT change

- `v1TypeSet` in `internal/protocol/envelope.go` — **must stay byte-identical**. Pinned by AC #1 and by the asymmetry rationale above. Any future PR that adds `TypeRekeyRequest` here regresses this slice.
- `dispatch.Route` / `internal/dispatch/dispatch.go` — untouched. The v1 dispatcher remains v1-pure.
- `V2SessionConfig.Handlers` — handler-table shape unchanged. `rekey_request` is NOT a handler; the application handler chain never sees the envelope.
- `handleNoiseMsg`'s `V2StateOpen` branch — the AEAD-decrypt + `dispatchAppFrame(ctx, s, plaintext)` call is unchanged. The probe lives one frame down the stack, inside `dispatchAppFrame`.
- `closeWith` / `sealError` — no new close-code path. `rekey_request` is informational; tear-down does not apply.
- The `peerStatic` field added by #452 — read only by #453's continuity check; this slice never touches `s.peerStatic`.

## Testing strategy

All tests land in `internal/relay/v2session_test.go` under a new `// --- v2 control-envelope tests (#454) ---` header. They reuse the `driveToOpen` / `sealAppFrame` / `decryptAppFrame` / `v2Recorder` / `waitForEnvelopes` helpers established by #446 (`v2session_test.go:739-827`) — no new helpers required beyond a small buffer-logger.

Three test functions:

### 1. `TestV2Session_OpenState_RekeyRequest_ScheduledIntercepted`

Goal: AC #4 sub-test "Control envelope intercepted" — handler chain unreachable, session stays open, no outbound close.

- Drive to `V2StateOpen` with `driveToOpen`. Register a stub handler keyed on `TypeListConversations` whose body sets `var handlerCalled atomic.Bool; handlerCalled.Store(true); return nil` (mirroring `TestV2Session_OpenState_TamperedNoiseMsg_4421:919-924`).
- Seal envelope `{ID: 42, Type: "rekey_request", TS: now, Payload: {"reason":"scheduled"}}` via `sealAppFrame`. Send it.
- `waitForEnvelopes(t, rec, 1)` — exactly one envelope captured (the handshake's noise_resp). No second envelope: no close, no AEAD-sealed reply.
- Assert `handlerCalled.Load() == false`.
- `sess.stop()` then assert `sess.mgr.sessions[v2TestConnID] != nil && State() == V2StateOpen`.

### 2. `TestV2Session_OpenState_RekeyRequest_UnknownReasonTolerated`

Goal: AC #4 sub-test "Unknown reason tolerated" — WARN log line captured with the raw reason value, no close, no outbound frame, session stays open.

- Set up a buffer-logger: `var logBuf bytes.Buffer; logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))`. Pass into `V2SessionConfig.Logger` instead of `silentLogger()`.
- Drive to `V2StateOpen`. Seal envelope `{type: "rekey_request", payload: {"reason":"lunar-eclipse"}}` via `sealAppFrame`. Send it.
- `waitForEnvelopes(t, rec, 1)` — only the noise_resp. No close, no reply.
- `sess.stop()`. Assert `logBuf.String()` contains the substring `level=WARN` (or stdlib's `level=WARN` exact text) AND `event=v2.rekey.request.received` AND `reason=lunar-eclipse`. The substring assertion is sufficient — the line format is stable for `slog.TextHandler` and the existing helper-buffer tests (`internal/conversations/sweep_loop_test.go`) use the same shape.
- Assert session entry still present with `State() == V2StateOpen`.

### 3. `TestV2Session_OpenState_RekeyRequest_RecognisedReasons`

Goal: AC #4 sub-test "Recognised reasons" — parameterised across `scheduled`, `manual`, `compromise`; each logs at INFO with the correct `reason` field; no close, no outbound frame.

- Table-driven subtests over `[]string{"scheduled", "manual", "compromise"}` using `t.Run(reason, ...)`.
- Per subtest: buffer-logger; drive to open; send sealed `rekey_request` with the parameter as `reason`; assert exactly one outbound envelope (the noise_resp); assert `logBuf.String()` contains `level=INFO`, `event=v2.rekey.request.received`, and `reason=<value>`; session stays open.
- Each subtest constructs its own `V2SessionManager` via `driveToOpen` — the manager is single-shot and the cost is small (~five additional handshakes total). Sharing a manager across subtests would require multi-`conn_id` orchestration that exceeds the test's behavioural value.

### Drift-detector test update (`internal/protocol/compat_test.go`)

The existing assertion that every `Type*` constant is registered in `v1TypeSet` (per `system-overview.md`'s description of `compat_test.go`) must accept `TypeRekeyRequest` as a deliberate exception. Smallest change: introduce a local `v2OnlyTypes = map[string]bool{TypeRekeyRequest: true}` literal in the test, then assert `every Type* constant is in v1TypeSet OR in v2OnlyTypes; nothing in both`. This converts the test from a "drift detector for the v1 set" into a "drift detector for the v1/v2 partition" — the architectural asymmetry now has a structural test pin.

### What the tests do NOT cover

- **JSON-decode failure of `payload.reason`** is not exercised by a dedicated test. The empty-reason WARN branch is covered by the unknown-reason test pattern (the switch's default arm); a separately-crafted "missing reason field" or "reason is not a string" frame would test the same default arm and add no behavioural coverage.
- **`compromise` is not escalated to a higher severity** beyond INFO — out of scope per the ticket. The test asserts INFO for all three recognised values without per-value severity discrimination.
- **Rate-limiting** is out of scope. No test budget.
- **The binary's own emit of `rekey_request`** is sibling ticket #450's scope. No test budget here.
- **The orphan `feature/449` branch** still exists with stale commits referencing this work, but issue #449 is CLOSED (split into #452/#453/#454) and its branch has no PR. The branch will be deleted out-of-band; no merge collision is structurally possible because no PR exists. The file-overlap check flagged it as a false positive; documenting here so the developer doesn't worry when grep turns up old code on a dead branch.

## Concurrency model

`handleRekeyRequest` runs synchronously on the manager's single dispatch goroutine (the loop in `(*V2SessionManager).Run` line 210-222). The probe block in `dispatchAppFrame` runs on the same goroutine. No new goroutines, no channels, no mutexes. The manager's existing "Run is the only goroutine the manager owns" invariant (`v2session.go:165-171`) is preserved without modification.

The probe's `json.Unmarshal` and `handleRekeyRequest`'s log emission together are O(1) per frame — well below the existing per-frame `noise.Decrypt` cost (~100µs). No head-of-line blocking concern.

## Error handling

- **JSON decode of the inbound envelope fails** → probe falls through to the existing `dispatch.Route` path; `Route`'s malformed branch emits the sealed `protocol.malformed` reply unchanged.
- **JSON decode of the inbound envelope succeeds, type is anything other than `rekey_request`** → probe falls through to the existing `dispatch.Route` path; the v1 ladder handles `Type`-based routing as today.
- **JSON decode of `env.Payload` (the inner `{reason: ...}` struct) fails inside `handleRekeyRequest`** → treated as empty reason, logged at WARN with `reason=""`. No sealed `protocol.malformed` reply — the envelope took no transport action either way; emitting an error reply for a malformed control payload would be a surprise behaviour change. Note: in practice the inner-payload decode of `struct{Reason string}` against any well-formed JSON object succeeds with an empty `Reason` field when the field is absent or non-string; only top-level non-object JSON would fail outright.
- **Recognised reason** → INFO log; no close; no outbound frame.
- **Unrecognised non-empty reason** → WARN log; no close; no outbound frame.

No new sentinel errors. No new close codes. No new outbound frame shapes.

## Open questions

- **Should the constant live in a new `internal/protocol/v2_codes.go` file rather than be appended to `codes.go`?** Architect chose: append to `codes.go` in a new "v2 control" group. Rationale: there is one (1) v2-control constant in v1's source-of-truth set today; a separate file is premature. When a third v2-control type lands, the constants graduate to a sibling file. This is the same growth path as `internal/sessions` (`id.go`, `session.go`, `pool.go` were one file each from the start; the directory is now ten files).
- **Should `compat_test.go` grow a third assertion that every member of `v2OnlyTypes` has a paired `noise_msg`-shaped consumer (i.e., the probe-and-switch in `dispatchAppFrame` covers each)?** Architect chose: no, not in this slice. The probe is currently single-decision (one `Type` checked); a generalised "v2-control discriminator table" is a refactor that should land when the third v2-control type does, not when the second lands. Tracked informally for whoever picks up the next v2-control envelope (the binary's own re-key emit per #450 reuses the same `TypeRekeyRequest` constant for its outbound shape, so no new v2 type).
- **`compat_test.go`'s line offsets.** I haven't verified the exact line numbers of the existing drift-detector assertion against the current code; the developer should grep `v1TypeSet` in `internal/protocol/compat_test.go` to locate the assertion before editing. The shape of the update is described above; the file:line pin is left to the developer to confirm.

## Files

- `internal/protocol/codes.go` — append v2-control constant block with `TypeRekeyRequest = "rekey_request"` + the load-bearing doc-comment cross-referencing `v1TypeSet`.
- `internal/protocol/envelope.go` — add a companion doc-comment above `v1TypeSet` naming `TypeRekeyRequest` as the canonical example of a v2-only type deliberately excluded. **No code change** to the map literal.
- `internal/protocol/compat_test.go` — extend the drift detector to accept the v1/v2 partition (see Testing strategy).
- `internal/relay/v2session.go` — five-line probe-and-switch at top of `dispatchAppFrame`; new `handleRekeyRequest` method (~25 LOC including doc-comment).
- `internal/relay/v2session_test.go` — three new tests under `// --- v2 control-envelope tests (#454) ---` header (~120-150 LOC including a small `bufferLogger()` helper that returns `(*slog.Logger, *bytes.Buffer)`).

Estimated size: ~40 LOC production + ~150 LOC tests + ~10 LOC drift-detector update. Total written work ~200 LOC across 5 file edits (3 production source + 1 test + 1 drift-detector test). Within XS.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings.** The probe runs on AEAD-decrypted plaintext — the AEAD layer (`s.recv.Decrypt` at `v2session.go:561`) is the trust boundary, and the plaintext has already crossed it before `dispatchAppFrame` is reached. The probe's `json.Unmarshal` is shape-validation on trusted bytes, not a boundary check. The asymmetry "constant exists, `v1TypeSet` membership does not" is the architectural boundary between v1 application traffic and v2 control traffic; it's pinned by the new `compat_test.go` partition assertion. A future contributor cannot accidentally route `rekey_request` to the handler chain without (a) adding to `v1TypeSet` AND (b) updating `v2OnlyTypes` in `compat_test.go` to remove the entry — two code-review moments, in adjacent files.

- **[Tokens, secrets, credentials] No findings.** `rekey_request` carries no token, no secret, no credential. `payload.reason` is a string from a small enum; logging it at INFO/WARN does not leak user data.

- **[File operations] No findings.** Slice does not touch the filesystem.

- **[Subprocess execution] No findings.** Slice does not execute subprocesses.

- **[Cryptographic primitives] No findings.** Slice does not introduce or consume crypto primitives. The AEAD-decrypt before the probe is unchanged from #446. The probe runs after MAC verification has already succeeded — a tampered `rekey_request` ciphertext is rejected by #446's `closeWith(StatusProtocolMismatch, nil)` branch before `dispatchAppFrame` is ever called. No new key, no new nonce, no new comparison.

- **[Network & I/O] No findings.** No new socket Reads, no new size caps required (the plaintext size is bounded by the existing `maxNoisePayloadBytes = 65535` cap enforced at the inner-frame decode boundary, `v2session.go:42`). No new outbound frames — `handleRekeyRequest` emits nothing on the wire. Slow-loris / amplification / resource-exhaustion vectors do not apply: a flood of `rekey_request` envelopes burns one re-decode + one log call per frame, well within the manager's existing per-frame cost envelope.

- **[Error messages, logs, telemetry] SHOULD FIX nothing — but pin explicitly.** The log line emits three fields: `event` (string literal, no input), `conn_id` (a routing identifier, opaque, log-safe per the per-`conn_id` discipline established in `v2session.go`), and `reason` (a string from the AEAD-decrypted payload). The `reason` field is bounded in value-space (the spec defines three enum values) but unbounded in attacker-controlled bytes (a malicious phone with a valid handshake could craft any UTF-8 string as `reason`). Logging an attacker-controlled string is generally acceptable when the consumer is a structured log handler (slog escapes special chars in `TextHandler` and quotes the value in `JSONHandler`), but the spec explicitly **caps** what the field can leak: `payload.reason` is a small enum at protocol level, and the binary's log lines are operator-facing, not user-facing. No PII risk. No token-leak risk (no token in the payload). Pinning here so a future contributor doesn't add `payload.full_bytes` or `payload.* dump`-shaped fields under the assumption "we already log the payload — what's one more field": **`reason` is the only payload field that may be logged; the rest of `env.Payload` is opaque control bytes and stays out of the log channel** per the package's no-payload-in-logs discipline.

- **[Concurrency] No findings.** Probe + `handleRekeyRequest` run on the manager's single dispatch goroutine. No new mutex, no new goroutine, no new channel. Existing single-writer-per-`conn_id` invariant satisfied without modification.

- **[Threat model alignment] No findings.** `docs/protocol-mobile.md` § Security model treats `rekey_request` as informational from the binary's perspective (the binary is always the IK responder per ADR 024). The threat "phone emits unsolicited `rekey_request`" is exactly what this slice handles: log and continue. The threat "phone emits `rekey_request` with malicious reason value" is bounded by the WARN-and-tolerate posture — the binary does no work in response, so there is no DoS vector. The threat "phone emits `rekey_request` to mask a re-key attempt" is structurally impossible: re-key requires `noise_init`, which #453 routes to `handleRekeyInit` independent of any `rekey_request` envelope. The two paths are decoupled at the discriminator layer.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-05-17
