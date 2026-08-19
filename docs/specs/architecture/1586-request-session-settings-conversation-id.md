# #1586 — `request_session_settings` names the conversation it asks about

**Size:** S (confirmed; PO sized S). 3 production source files, 1 new exported type, 1 new gate branch, ~320 lines of total written work.

## Files to read first

| File | Symbol | What to extract |
|---|---|---|
| `internal/relay/v2session_settings.go` | `handleRequestSessionSettings` | The handler being changed. Its numbered doc comment IS the contract — steps 2 and 3 both become wrong and must be rewritten, not appended to. |
| `internal/relay/v2session_replay.go` | `handleRequestSnapshot` | The decode + `KnownConversation` pattern to mirror: tolerated `_ = json.Unmarshal`, error never echoed. Note the one place we deliberately differ (below). |
| `internal/relay/v2session_seams.go` | `V2SessionConfig` fields `KnownConversation`, `BootstrapSessionID` | The seam contract. `BootstrapSessionID`'s doc says the three run-config seams move together — that paragraph stays true and must not be edited here. |
| `internal/protocol/snapshot.go` | `RequestSnapshotPayload` | The exact shape to mirror, including the file-level "no field carries omitempty" rationale. |
| `internal/protocol/settings.go` | `SessionSettingsPayload` | The reply shape (unchanged). Its "Scope:" paragraph needs one sentence about the new request field. |
| `internal/protocol/codes.go` | `TypeRequestSessionSettings` | Its trailing comment says "bare frame" — now wrong. |
| `internal/relay/v2session_settings_read_test.go` | `readSeams`, `allReadSeams`, `readManagerFor` | The fixture harness. `readSeams` **already** has `knownConv` and `readManagerFor` already wires it — no harness plumbing needed. |
| `internal/relay/v2session_settings_read_test.go` | `TestV2Session_RequestSessionSettings_IgnoresAnyPayload` | Assertion stays; its doc comment's stated reason no longer holds (AC #5). |
| `internal/protocol/snapshot_test.go` | `TestRequestSnapshotPayload_RoundTrip` | The round-trip test to mirror for the new payload. |
| `internal/e2e/relay_v2_stream_run_config_test.go` | `TestRelayV2_StreamRequestSessionSettings` | Comment-only edit. Assertions must not move. |
| `cmd/pyry/relay.go` | the `KnownConversation` closure in `startRelayV2` | Read-only. Confirms production already wires the seam — no new plumbing. |
| `cmd/pyry/relay_guard_test.go` | `interceptedTypes` map entry `"TypeRequestSessionSettings"` | Read-only. Classifies by Type, not payload — unaffected, do not edit. |
| `docs/protocol-mobile.md` | § Session settings → `request_session_settings`, `session_settings` | The two subsections to rewrite. |
| `CODING-STYLE.md` | § Comments — Citing Other Code | `make cite-guard` runs inside `make check`. Cite symbols, never `file.go:NNN`. |

## Context

`request_session_settings` is the read verb that tells a client which session to address a `set_session_settings` to. It is a bare frame: it carries no field, so a client cannot say which conversation it is asking about. The sibling read verb in the same package, `handleRequestSnapshot`, already carries a `conversation_id` and validates it through the `KnownConversation` seam before doing any work — and that seam is a field on the same `V2SessionConfig`, already wired in production by `startRelayV2`. So the field and its validation cost no new plumbing.

**Scope boundary — the single most important constraint on this spec.** This ticket adds the field and its gate *only*. **No reported value changes.** A request naming a known conversation must receive byte-identical values to what it receives today: the bootstrap session's id, model, effort, YOLO and usage figures. Making the reported values follow the named conversation is #1587, and the reported id and the reported values must move in one step or a client would read one session and write to another (see `BootstrapSessionID`'s doc in `v2session_seams.go`).

Two consequences of that boundary, both already settled in the ticket body and neither open for re-litigation here:

- **An absent or empty `conversation_id` is answered exactly as today.** Failing it closed would turn `TestRelayV2_StreamRequestSessionSettings` red on an assertion whose own failure message names desktop#491, and would make the run-config sheet inert before the user's first message — `conversations.Load` seeds nothing, so on a fresh daemon there is no conversation id in existence to name. The flip to fail-closed belongs in #1587.
- **An unresolvable conversation gets a zero-value `session_settings` reply, never an error frame.** Every seam on this verb already degrades to a zero value that the wire contract defines as a real answer (`session_id: ""` is "the daemon has no session to address"). An error reply would add a failure branch to a verb documented as always answering, and force a client to parse two shapes for one question.

The existence oracle this creates is **accepted, not mitigated**. After this change a known conversation gets a populated reply and an unknown one gets a zero reply, which is a perfect oracle. It discloses strictly less than the caller already has: the caller is an already-paired conn, and `startRelayV2` map-dispatches `list_conversations` to any paired conn with **no** interactive-capability gate, so that caller can enumerate the whole registry outright rather than probing it one id at a time. (`request_snapshot` on the same conn also already separates known from unknown by replying `conversation.not_found`.) **Do not write an acceptance test asserting indistinguishability — it cannot pass.**

## Design

### 1. Wire vocabulary — `internal/protocol/settings.go`

One new exported type, mirroring `RequestSnapshotPayload`:

```go
type RequestSessionSettingsPayload struct {
	ConversationID string `json:"conversation_id"`
}
```

**No `omitempty`.** This matches `RequestSnapshotPayload` and the file-level rule in `snapshot.go` — every field is always on the wire so a fixture pins the full shape and an empty `conversation_id` does not silently vanish. It is deliberately *unlike* the sibling `SetSessionSettingsPayload`, whose pointer-plus-omitempty fields encode a presence contract. There is no presence contract here: absent and empty are the same case (answered as today), so nothing needs to distinguish them.

The doc comment must state: (a) this is an inbound v2 control envelope intercepted before `dispatch.Route`, like `RequestSnapshotPayload`; (b) `ConversationID` is untrusted network input used only as an in-memory registry lookup key; (c) empty means "no conversation named", which the handler answers as it always has; (d) the reported values are still the bootstrap session's in this ticket — #1587 makes them follow the named conversation.

`internal/protocol/codes.go`: `TypeRequestSessionSettings`'s trailing comment says `bare frame (intercepted pre-dispatch.Route)`. Replace "bare frame" with a reference to the payload; keep the interception note.

### 2. Handler gate — `internal/relay/v2session_settings.go`

`handleRequestSessionSettings` gains one decode and one boolean gate. Ordering is load-bearing and must read, in this order:

1. **Capability gate** — `if !s.interactive { return }`. **Unchanged, and must stay first.** AC #4 requires that a non-interactive conn performs no decode and no registry lookup, so it cannot learn whether a conversation exists.
2. **Decode (new)** — `var p protocol.RequestSessionSettingsPayload; _ = json.Unmarshal(env.Payload, &p)`. A decode failure is **tolerated**: it leaves `ConversationID == ""`, which is the answered-as-today case. The decode error is never echoed to the client and never logged — `encoding/json` quotes attacker bytes into its error string. This is `handleRequestSnapshot`'s posture, and the *only* difference is what `""` means: there it is a rejection, here it is the answered-as-today case.

   **The whole backward-compatibility guarantee rests on this line's behaviour for a bare frame.** A bare frame has a nil `env.Payload`; `json.Unmarshal(nil, &p)` returns an "unexpected end of JSON input" error and leaves `p` zeroed — it does not panic. That is what makes an un-updated client's bare frame flow into the `""` short-circuit. Do **not** add a `len(env.Payload) == 0` guard, an error check, or a malformed-payload reply branch: each would either be dead code or turn the five must-stay-green tests red.
3. **Conversation gate (new)** — a single boolean:

   ```go
   addressable := p.ConversationID == "" ||
   	(m.cfg.KnownConversation != nil && m.cfg.KnownConversation(p.ConversationID))
   ```

   Short-circuit order matters twice over. `p.ConversationID == ""` first means an unnamed request never reaches the registry at all — that is what keeps `_ReportsRunConfig`, `_AnswersWithoutASnapshotter`, `_NilSeamsDegradeToZero`, `_IgnoresAnyPayload` and `TestRelayV2_StreamRequestSessionSettings` green with a nil seam. `m.cfg.KnownConversation != nil` second encodes the nil-seam decision below.
4. **Seam reads** — the three existing nil-guarded reads (`BootstrapSessionID`, `SnapshotSettings`, `SnapshotUsage`) move inside `if addressable { … }`. The `var` declarations stay at function scope so the non-addressable path falls through with every value at its zero. **No run-configuration seam may be called when `addressable` is false** (AC #2).
5. **Marshal + reply** — unchanged. One reply shape, one marshal, one forward, one defensive marshal-error branch. Do **not** add a second marshal/forward path for the zero reply; the whole point of the boolean is that both answers share the tail.

**Nil `KnownConversation` with a non-empty id ⇒ not addressable.** The seam is optional (foreground / unwired). If it is nil there is no way to validate a named id, so the named-id case fails closed — matching `handleRequestSnapshot`, where a nil seam rejects everything. The unnamed case is unaffected because step 3 short-circuits before the nil check, which is exactly why `allReadSeams()` leaving `knownConv` nil keeps all four named tests green.

**Logging: add nothing.** `conversation_id` is untrusted network input and must not reach a log line, an error string, or any filesystem path. The handler's existing single `Debug` log carries `conn_id` only, deliberately logging less than the write path because this verb fires on every sheet open. Do not add a reject-branch log, and do not add the id (or a derived field) to the existing one.

### Data flow

```
inbound noise frame
  → dispatchAppFrame's TypeRequestSessionSettings case (v2session.go, unchanged)
  → handleRequestSessionSettings
      ├─ !s.interactive ─────────────────────────────► return (no decode, no lookup, no reply)
      ├─ decode → ConversationID (failure tolerated ⇒ "")
      ├─ addressable?
      │    ├─ "" ────────────────────────────────────► true  (registry NOT consulted)
      │    ├─ seam nil ──────────────────────────────► false
      │    └─ KnownConversation(id) ─────────────────► its bool
      ├─ if addressable: BootstrapSessionID / SnapshotSettings / SnapshotUsage
      └─ marshal SessionSettingsPayload → forwardEnvelope   (one shape, always)
```

### 3. Documentation — `docs/protocol-mobile.md` § Session settings

Two subsections change. **Be precise about what is and is not true after this ticket** — the doc must not claim the reported values follow the named conversation, because they do not yet.

`#### request_session_settings` — delete the paragraph beginning "**The frame is bare — it carries no payload at all.**" and its justification ("The reported values are daemon-wide, so there is no field a client could use to select another session's data. Any payload a client does attach is never read."). That justification is what is now false: the field exists and gates the answer. Replace with a field table and the three answers:

| Field | Type | Meaning |
|---|---|---|
| `conversation_id` | string | The conversation being asked about. **Empty or absent = no conversation named**; answered as the daemon always has. |

...plus prose stating: a `conversation_id` naming a conversation the daemon hosts is answered with the current run configuration; one naming a conversation it does not host is answered with a `session_settings` whose every field is at its zero value — **never an error frame**, because `session_id: ""` is already the defined "no session to address" answer; and an empty or absent field is answered exactly as before, which is what keeps un-updated clients and pre-first-turn clients working. Update the example JSON to carry the payload.

`#### session_settings` — the "Scope:" paragraph currently says the values describe the bootstrap session and that keying by conversation is a deferred follow-up. That stays **true** and mostly stays as-is; add one sentence: the request now names a conversation, and that name currently gates *whether* an answer is populated, not *which* session it describes — #1587 makes the values follow the named conversation, at which point the values and `session_id` move together.

### 4. In-code comments that are now wrong (AC #5)

Three, and no more. Each is a real behavioural claim, not a stylistic touch-up:

- `handleRequestSessionSettings`'s doc comment, numbered step 2: "No decode step: the request frame is bare, so there is no untrusted field to parse … A client that sends a payload anyway is answered normally; the bytes are never read." Every clause is now false. Rewrite the step as the tolerated decode plus the conversation gate, and say why `""` is answered rather than rejected (the fresh-daemon case, #1587's flip).
- `TestV2Session_RequestSessionSettings_IgnoresAnyPayload`'s doc comment: "There is no decode step, so there is no malformed-payload branch and no field that could select another session's data." The malformed-payload half is still true (a decode failure is tolerated, never a branch); the "no field" half is not. The test's **assertions do not change** — its probe payload carries `session_id`, not `conversation_id`, so it still decodes to `ConversationID == ""` and still gets the seam's id. Keep the `"../../etc/passwd"` probe. Restate the comment as: an unrecognised field is still ignored, and a payload that is not this verb's payload cannot select another session's data.
- `TestRelayV2_StreamRequestSessionSettings`'s inline comment "A BARE frame: no payload at all. The reply is daemon-wide, so there is no field a client could use to select another session's data." Restate: the frame now carries an optional `conversation_id`; this test deliberately sends none, exercising the absent-field path, which is the fresh-daemon case where no conversation id exists to send. Its doc-comment paragraph beginning "No conversation is seeded and no turn is driven" stays — its claim is still exactly right and is the reason the absent case cannot be failed closed. **Assertions must not move.**

Do not sweep for other "bare" mentions. `TypeRequestSessionSettings`'s comment in `codes.go` is covered in § 1; nothing else in the tree makes the claim.

## Concurrency model

Nothing new. `handleRequestSessionSettings` runs on the manager's single `Run` dispatch goroutine (it is switch-intercepted in `dispatchAppFrame`, not handed to the per-conn worker), so the `s.interactive` read stays lock-free under the package's single-owner invariant and the added decode and seam call add no shared state.

`KnownConversation` is called synchronously on that goroutine, exactly as `handleRequestSnapshot` already calls it. Production's closure is one `w.convReg.Get`, which is **not** a map lookup: `conversations.Registry.Get` takes the registry's full `sync.Mutex` and linear-scans the slice comparing ids. That is bounded (no I/O, no filesystem) and already on this goroutine's hot path via `request_snapshot`, so the verb introduces no new lock, no new lock ordering, and no new contention class — but do not describe it in a comment as a map read, because it is not. The handler holds no relay lock across the seam call, which is what keeps the ordering trivially safe.

**Test-side concurrency is the one place to be careful.** The counting seams described below are written on the dispatch goroutine and read by the test goroutine after the barrier conn opens. Use `sync/atomic` (e.g. `atomic.Int64`) for the counters — a plain `int` will be flagged by `go test -race`, which `make check` runs.

## Error handling

| Condition | Answer | Rationale |
|---|---|---|
| Non-interactive conn | Nothing — no decode, no lookup, no reply | Authz boundary. Mirrors the write path. |
| Payload absent / not JSON / wrong shape | Answered as today (full run configuration) | Decode failure leaves `ConversationID == ""`, the answered-as-today case. The decode error is never echoed and never logged. |
| `conversation_id` empty | Answered as today | Fresh-daemon case: no conversation id exists to name. |
| `conversation_id` unknown to the registry | `session_settings` with every field at its zero value | The wire contract already defines the zero payload as a real answer; an error would add a failure branch to a verb documented as always answering. |
| `KnownConversation` seam nil, id non-empty | Same zero-value reply | No way to validate a named id ⇒ fail closed for the named case, matching `handleRequestSnapshot`. |
| `json.Marshal` of the reply fails | Existing defensive `server.binary_offline` branch | Unchanged. |

No new error code, no new reply Type, no new failure branch on the wire.

## Testing strategy

`make check` covers all of it — the relay unit tests, the protocol round-trip, and the fake-daemon e2e suite (which is what runs `TestRelayV2_StreamRequestSessionSettings`). No live-claude tier needed.

### `internal/protocol`

- New fixture `testdata/request_session_settings.json`, one line, mirroring `request_snapshot.json`'s shape (envelope with `id`/`type`/`ts` and a `payload` carrying `conversation_id`).
- `TestRequestSessionSettingsPayload_RoundTrip`, mirroring `TestRequestSnapshotPayload_RoundTrip`: unmarshal the fixture, assert `Type == TypeRequestSessionSettings`, assert the decoded `ConversationID`, then `roundTripEnvelope` for the byte-equal guard. The byte-equal round trip is what pins the no-`omitempty` decision — add `omitempty` and an empty-id fixture loses its key.
- No change to `compat_test.go`: its `v2OnlyTypes` map and `TestTypeConstants_V1V2Partition` key on the Type constant, which already exists.

### `internal/relay` — `v2session_settings_read_test.go`

One new test-local helper plus two new test functions. `readSeams` and `readManagerFor` already carry `knownConv`; do not touch them.

**Helper — counting seams.** A `readCounts` struct of four `atomic.Int64` (session-id reads, settings reads, usage reads, registry lookups) and a constructor returning `(readSeams, *readCounts)` whose closures return the same `readSessionID` / `readModel` / `readEffort` / `readUsedTokens` / `readWindowTokens` fixture constants that `allReadSeams` returns, while bumping their counters. Wiring the run-config seams to **non-zero** values is what makes AC #2's test discriminating: with them unwired the zero reply is the same reply whether or not the gate exists, so such a test would pass with the gate deleted and prove nothing.

**Test 1 — the gate decides the answer.** Table-driven over the conversation-id cases, each row driving one frame through the real `Frames`/`Run` loop on an interactive conn and asserting the decoded reply payload plus the counters:

| Row | Frame carries | `knownConv` wired? | Want payload | Want run-config reads | Want registry lookups |
|---|---|---|---|---|---|
| known conversation | `{"conversation_id":"<known>"}` | yes | the full fixture payload, byte-identical to today's | > 0 | 1 |
| unknown conversation | `{"conversation_id":"other"}` | yes | the **zero** `SessionSettingsPayload` | **0** | 1 |
| nil seam, named id | `{"conversation_id":"<known>"}` | **no (nil)** | the zero payload | 0 | 0 |
| empty id | `{"conversation_id":""}` | yes | the full fixture payload | > 0 | **0** |
| no payload at all | — | yes | the full fixture payload | > 0 | **0** |

Every row asserts exactly one outbound app frame, `Type == TypeSessionSettings` (never `TypeError`), and `InReplyTo` pointing at the request id. The two zero-run-config-read expectations are AC #2's "no run-configuration seam is consulted at all"; the two zero-lookup expectations pin that `""` short-circuits ahead of the registry, which is what makes the answered-as-today path independent of the seam.

**Test 2 — a non-interactive conn is inert and silent (AC #4).** One non-interactive conn sends a frame naming a *known* conversation. Assert zero outbound app frames, registry lookups == 0, and run-config reads == 0. The existing `_ReportsRunConfig` "non-interactive is inert" row already pins the no-reply half; this adds the no-lookup half, which is the new claim — the capability gate must sit ahead of both the decode and the lookup, so such a conn cannot learn whether a conversation exists.

**Stays green with assertions unmodified (AC #3).** `TestV2Session_RequestSessionSettings_ReportsRunConfig`, `_AnswersWithoutASnapshotter`, `_NilSeamsDegradeToZero`, `_IgnoresAnyPayload`, and `internal/e2e.TestRelayV2_StreamRequestSessionSettings`. All five send no `conversation_id` — the first four send a bare frame or a payload without that key, and the e2e sends a bare frame — so all five take the `""` short-circuit and never reach the registry. Comment edits to the last two are fine; assertion edits are not, and an assertion edit is the signal that the gate was built wrong.

### Mutation check (run this before opening the PR)

Two independent mutants, each of which must turn the new tests RED. Use `go test -overlay=<abs-path json>` so nothing is written to the worktree:

1. Force `addressable` to `true` unconditionally. The "unknown conversation" and "nil seam, named id" rows must fail on both the payload assertion *and* the run-config-read counters.
2. Drop the `p.ConversationID == ""` short-circuit (leave only the seam check). The "empty id" and "no payload at all" rows must fail, and so must `_ReportsRunConfig` / `_NilSeamsDegradeToZero` — which is the point: that clause is what protects the shipped desktop#491 fix.

If mutant 1 leaves the counters green, the counting seams are not wired into the row that needs them and AC #2 is not actually proven.

## Open questions

None blocking. Two decisions are recorded above rather than left open, because leaving them open is what sent this ticket back to PO once already:

- **Nil `KnownConversation` with a named id fails closed.** The alternative (nil ⇒ treat every id as known) would keep the four named tests green too, since they send no id, so the tests do not choose between them. Fail-closed is chosen for consistency with `handleRequestSnapshot` and because it makes the unwired case indistinguishable from the unknown case, which is the smaller surface.
- **The reported values stay bootstrap-scoped.** Any impulse to make them follow the named conversation while you are in the handler is #1587. Acting on it here would split the reported id from the reported values across two tickets, which the seam docs explicitly forbid.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. Exactly one new untrusted value (`conversation_id`, network-supplied, post-AEAD, from an already-paired device) crosses one explicit boundary: decoded into a named typed field in `handleRequestSessionSettings`, used as the sole argument to `KnownConversation`, then dead. No downstream code holds it, it is never stored, and it is never copied into the reply. The boundary is one function, not scattered. **This changes in #1587**, where the id starts selecting which session is reported — that ticket needs its own pass and must not inherit this verdict.
- **[Tokens, secrets, credentials]** Not applicable by design. No token is generated, stored, compared, or rotated. The `session_id` in the reply is a routing key, not a secret — `BootstrapSessionID`'s seam doc establishes this, and it already crosses the wire outbound on `session_transition` and inbound on `set_session_settings`. The reply's field set is unchanged.
- **[File operations]** No findings — **verified, not assumed.** The one concern worth checking was whether an attacker-controlled `conversation_id` reaches a path: `conversations.Registry.Get` performs no filesystem access at all (mutex, linear scan over an in-memory slice, id equality). So `_IgnoresAnyPayload`'s `"../../etc/passwd"` probe is inert as a traversal vector — it is compared as a string and not found. **Keep that probe** (it now also documents that a non-`conversation_id` key cannot select anything), and do not let a future change route this id into a path without re-running this pass.
- **[Subprocess / external command execution]** Not applicable by design. The id never reaches `exec.Command`, argv, or the claude settings template — it is consumed by the gate and discarded. Contrast the write path, where `validModel` / `validEffort` exist precisely because those values do reach argv. No shape validator is needed here for the same reason none is needed on `handleRequestSnapshot`'s id.
- **[Cryptographic primitives]** Not applicable. No RNG, key, nonce, or comparison-against-a-secret is added. The gate compares an untrusted id against non-secret registry ids, so constant-time comparison is not indicated.
- **[Network & I/O]** No findings. The new decode reads a payload already bounded by two existing caps: the transport's `maxFrameBytes` (1 MiB, applied via `SetReadLimit` in `internal/transport/wssclient.go`) and `maxNoisePayloadBytes` (65535, in `v2session_handshake.go`). The envelope was already fully parsed by `dispatchAppFrame`'s probe before the handler runs, so the marginal cost is one small `json.Unmarshal` of an already-resident `json.RawMessage` plus a bounded id comparison — the same marginal cost `request_snapshot` already pays per frame. **No amplification vector:** the reply is a fixed-size payload of two short strings, a bool and two ints regardless of the request id's length, so a 64 KiB id buys the sender nothing.
- **[Error messages, logs, telemetry]** No findings, but this is the category the design actively spends a decision on, so it is a MUST-NOT-REGRESS rather than a non-event. `conversation_id` must never reach a log line, an error string, or the wire. Three concrete rules the developer must hold: the decode error is discarded with `_ =` and never wrapped or logged (`encoding/json` quotes attacker bytes into its error text); **no reject-branch log is added** — the zero reply is silent, matching the handler's existing "logs less than the write path" posture for a verb that fires on every sheet open; and the existing `Debug` log keeps `conn_id` as its only field. The sole reachable error reply (`settingsReplyError` on a marshal failure) carries `msgSettingsUnavailable`, a fixed constant, and is unchanged.
- **[Concurrency]** No findings. The handler stays on the single `Run` dispatch goroutine, so no lock protects the added state (there is none). The seam call takes `conversations.Registry`'s mutex, but `handleRequestSnapshot` already takes it from this same goroutine, so no new lock, no new acquisition order, and no new contention class. The handler holds no relay lock across the call. The one real risk is test-side: the counting seams are written on the dispatch goroutine and read by the test goroutine, so they must be `sync/atomic`, not plain ints, or `go test -race` inside `make check` will flag them.
- **[Threat model alignment]** `docs/protocol-mobile.md` § Security model names no threat for conversation-id enumeration by a paired client, because a paired client is inside the trust boundary. The relevant listed threat is **#4, token leak via phone** (`severity: medium`, mitigated by per-device revocation) — an attacker holding a leaked device token becomes such a client. Against that attacker the oracle added here is **strictly weaker than a verb they already have**: `startRelayV2` map-dispatches `list_conversations` to any paired conn with no interactive-capability gate, so they can enumerate the registry wholesale instead of probing ids one at a time. Accepting the oracle therefore widens nothing, and the ticket's instruction not to write an indistinguishability AC is correct. **Out of scope, named:** making the reported values follow the named conversation, and the accompanying flip of absent-`conversation_id` to fail-closed, are #1587.

**Reviewer:** architect (self-review per `architect/security-review.md`)
**Date:** 2026-08-19
