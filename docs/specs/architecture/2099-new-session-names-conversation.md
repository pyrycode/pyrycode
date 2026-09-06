# #2099 — `new_session` names the conversation it restarts

Plan for [#2099](https://github.com/pyrycode/pyrycode/issues/2099). Written Phase A, committed before any implementation code.

## Files read

Cite by symbol throughout — no line numbers, per the repo's citation rule.

| Path | Symbols | Why it matters |
|---|---|---|
| `internal/relay/v2session.go` | `dispatchAppFrame`'s `TypeNewSession` arm | The one call site that must start passing the envelope; the `TypeDequeueMessage` arm beside it is the shape to copy. |
| `internal/relay/v2session_modal.go` | `handleNewSession`, `handleDequeueMessage` | The handler to widen, and the tolerant-unmarshal + debug-no-op precedent to copy verbatim. |
| `internal/relay/v2session_seams.go` | `SessionStarter`, `V2SessionConfig`'s `SessionStarter` field, `KnownConversation` and its doc block | The seam to widen; `KnownConversation`'s doc records why a pure membership check is the WRONG tool here (it reads a known-but-unbound conversation as addressable). |
| `cmd/pyry/main.go` | `activeSessionStarter`, `startFreshRunner`, `beginRotationOrNoop`, `resolveBoundSession`, `activeInterrupter` + its `logger`, `activeConversation` | The production starter to change; `resolveBoundSession` is where the #678 bootstrap isolation lives; `activeInterrupter` is the logger-field precedent (`logger()` nil fallback, per-arm records). |
| `internal/sessions/transition.go` | `Pool.RotateForNewSession`, `Pool.notifyTransition`, `Pool.rebindConversation` | **Load-bearing for AC-2**: `notifyTransition` rebinds the conversation to the new session id *before* firing the observer, so the emitted transition resolves to the rotated conversation with no new work. |
| `cmd/pyry/relay.go` | `conversationForSession`, the `startSessionTransitionStreamV2` wiring | Confirms the transition's `conversation_id` is derived from the *new session id* via the registry, not from the cursor — so naming B emits B. |
| `cmd/pyry/session_transition_v2.go` | `sessionTransitionEmitterV2.broadcast`, `resolveConv` | The consumer of that resolution; broadcasts to every interactive conn, which is AC-2's "reaches connections that did not send the frame". |
| `internal/protocol/codes.go` | `TypeNewSession` and its doc block | Doc asserts the frame "carries NO payload — no `conversation_id`"; that claim becomes false. |
| `internal/protocol/messaging.go` | `DequeueMessagePayload` | The sibling payload struct and doc shape to mirror. |
| `internal/conversations/id.go` | `ValidID` | **Returns false for the empty string** (its own doc says so) — the absent-field branch must come *before* the shape check. |
| `internal/conversations/registry.go` | `Registry.Get`, `Registry.RebindSession` | The registry lookup `resolveBoundSession` performs; membership + binding in one refusal. |
| `internal/relay/v2session_newsession_test.go` | `fakeSessionStarter` | The relay-side double to extend so it records the id it was handed. |
| `internal/e2e/relay_v2_stream_new_session_test.go` | `TestRelayV2_StreamNewSessionRotatesAndRestartsFresh`, `childStdinLog`, `childReceivedTurn`, `childStdinLogs` | The bare-path e2e proof and its child-attribution helpers, reused by the new two-conversation case. |
| `internal/e2e/relay_v2_stream_interrupt_test.go` | its `create_conversation` mint-and-drain block | The precedent for minting a second conversation over the wire in this harness. |
| `internal/protocol/messaging_test.go`, `internal/protocol/envelope_test.go` | the `dequeue_message` round-trip/malformed pair, `readFixture` | The test shape to copy; `readFixture` reads a **committed** fixture, and `testdata/` has no `new_session.json` today. |
| `docs/protocol-mobile.md` | § New session (v2), § Application message types' `new_session` row, § Changelog | The three edits AC-5 asks for. |
| `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`, `…-inbound-dequeue-message-queueremover-sea.md` | — | Package overviews for the two handlers; confirm the inert-frame conventions this plan follows. |
| `CODING-STYLE.md` | — | Consumer-side interfaces, no over-DRY, structured `log/slog`. |

## Context

`new_session` is the last per-conversation control frame that cannot name its conversation. The daemon resolves it against `activeConversation`, a single process-wide cursor stamped only by a successful `sessionRouter.Route`. A client that opens chat B and presses **New session** before sending anything to B therefore restarts chat A's claude — mid-work if A was working. `dequeue_message` and `request_attachment` already name their conversation, and #2098 put one on the upload leg; this closes the last gap.

The ask: `new_session` carries an **optional** `conversation_id`. Present ⇒ rotate that conversation. Absent ⇒ behave exactly as today, so an un-upgraded client keeps working.

Decisions the ticket already took, not re-opened here: the frame stays fire-and-forget with no reply (matching `dequeue_message`'s no-op on an unknown id); `interrupt`'s identical defect is sibling #2103 and is not touched.

**No ADR is warranted.** This adopts a rule the protocol document already states for `request_attachment` and `send_message` — a client-named `conversation_id` is a registry-validated lookup key, not authorization — rather than minting a new one. The documentation phase should fold the finding below into `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`.

## Size-boundary overage — stated, not hidden

Two lines of the size-S table are exceeded. Both were measured against this written plan, not the sketch, and both are the refiner's stated figures re-derived independently:

| Boundary | Limit | This ticket |
|---|---|---|
| Production source files | ≤ 5 | **6** |
| Total written work | ≤ 800 | **~950** |
| New exported types | ≤ 5 | 1 (`protocol.NewSessionPayload`) |
| Consumer call sites | ≤ 10 | 3 (`handleNewSession`, `activeSessionStarter`, `fakeSessionStarter`) |
| Acceptance criteria | ≤ 5 | 5 |
| Reject branches | ≤ 10 | 4 inert arms, all pre-existing guards but one |

**The floor wins over the ceiling, which is the stated rule for exactly this disagreement.** Every available cut produces a child that fails the sizing floor or does not compile:

- *Protocol vocabulary from handler.* `NewSessionPayload` has **one** reader, `handleNewSession`. #2142's `AttachmentChunkPayload.ConversationID` could be split off because it had readers on the upload leg, the retrieval leg and the offered frame; this has one. Floor violation.
- *Seam from implementation.* Go does not compile a widened one-method interface whose sole implementation still has the old signature. Not a cut at all.
- *Relay side from daemon side.* Ships a slice where the id is decoded and threaded but the starter still reads the cursor: nothing observable changes while the defect stands and the ticket looks fixed.
- *Happy path from the inert cases.* Ships an unvalidated client-supplied id into a registry lookup, on a `security-sensitive` ticket.

Split depth was checked: `parent none grandparent none`, so a split was available and was rejected on the merits, not blocked. The nearest analogue, #2143, shipped **1090 builder-written insertions across 7 production files as one ticket** (`aafc777e` 410 + `ac898003` 580 + `addea306` 100).

Two things hold this inside one budget rather than merely asserting it: **most of the reject set is free** (see Error handling — three of the four inert arms land on guards that already exist and already return inert), and **AC-2 is structurally free** (the transition already derives its `conversation_id` from the rotated session id).

## File-overlap check

`git fetch origin --prune` then a scan of every `origin/feature/<N>` branch's diff against `origin/main`, over all ten files this plan touches. One hit: `origin/feature/449` on `internal/protocol/codes.go` and `internal/relay/v2session.go`. Re-verified here rather than inherited — last commit `2026-05-17`, **no PR in any state**, issue #449 **CLOSED** (squash-merged, so `origin/main...branch` shows its old diff forever). Dead. No live overlap; no `blockedBy` set.

## Design

### Wire vocabulary — `internal/protocol/messaging.go`

One new exported type beside `DequeueMessagePayload`:

```go
type NewSessionPayload struct {
	ConversationID string `json:"conversation_id,omitempty"`
}
```

`omitempty` so a client that has nothing to name emits `{}` rather than an explicit empty string; both decode identically and both take the cursor path. The doc block states: untrusted client input; a lookup key validated against the daemon's registry, never authorization and never a path component; absent or empty means "the daemon's current conversation", which is today's behaviour verbatim.

`TypeNewSession` itself is unchanged, so `internal/protocol/compat_test.go`'s v2-control partition needs no edit. `codes.go`'s doc block claim that the frame "carries NO payload — no `conversation_id`" is corrected in place; the `/clear` framing in that same block is corrected too, since the block is already being edited (the `/clear` framing in `v2session_modal.go`, `v2session_seams.go` and elsewhere is explicitly **out of scope** — #2103 touches two of those files).

Three committed fixtures under `internal/protocol/testdata/`, one per wire shape AC-3 distinguishes, following #2142's `attachment_chunk_upload` / `_retrieval` / `_zero` precedent: `new_session.json` (id present), `new_session_bare.json` (`{}`, field absent), `new_session_empty_conversation.json` (explicit `""`). They are `git add`ed with the code — a fixture written but not committed goes out with the run's worktree.

### Relay — decode and hand off, decide nothing

`dispatchAppFrame`'s `TypeNewSession` arm starts passing the envelope, exactly as its `TypeDequeueMessage` neighbour does.

`SessionStarter` gains the id:

```go
type SessionStarter interface{ StartNewSession(conversationID string) error }
```

Contract, fixed in the seam's doc: `conversationID` is **client-supplied and unvalidated at this boundary**; the empty string means "the conversation the daemon's cursor points at", which is the pre-#2099 behaviour and the un-upgraded client's path. `internal/relay` imports neither `internal/conversations` nor `internal/sessions` — a deliberate boundary the seams file already states — so **the relay validates nothing and resolves nothing**; the string crosses the seam as-is and `cmd/pyry` owns every check.

`handleNewSession(s *V2Session, env protocol.Envelope)` keeps its existing order and adds one step, mirroring `handleDequeueMessage`:

1. non-interactive conn ⇒ inert (unchanged capability gate);
2. nil `SessionStarter` ⇒ inert with the existing debug record (unchanged);
3. **new** — tolerant `json.Unmarshal` of the payload into `NewSessionPayload`, error deliberately discarded: a decode failure leaves the zero value, which is the empty string, which is today's cursor behaviour. Neither the decode error nor the payload bytes are echoed to the phone or into a log (`encoding/json` quotes attacker bytes into its error string) — the never-echo discipline `handleDequeueMessage` and `handleRequestSnapshot` already keep;
4. `StartNewSession(p.ConversationID)`, still best-effort: an error is Warn-logged with the conn id and tolerated, no reply owed.

A nil/absent `env.Payload` is the same path: `json.Unmarshal(nil, …)` errors, the zero value stands, the cursor rotates. That is AC-3's "no payload at all" input.

### Daemon — the one place that validates

`activeSessionStarter` gains a `log *slog.Logger` field and a `logger()` nil-fallback method copied from `activeInterrupter` (it is a constructor-less bag of injected seams built as a named-field literal, so an omitted field is a reachable state and a nil `*slog.Logger` panics on first use — on a remotely-driven path that is a latent crash on a rarely-hit inert arm). Production wires `log: logger` beside the existing `activeInterrupter` literal.

`StartNewSession(conversationID string) error` resolves in this order — **the ordering is the design**:

1. `conversationID == ""` ⇒ take the existing cursor path unchanged: `a.currentConv()`, and `""` there is still inert. **This branch comes before the shape check** because `conversations.ValidID` returns false for the empty string; reversing the two makes every un-upgraded client's bare frame inert and silently breaks AC-3.
2. otherwise `conversations.ValidID(conversationID)` — the shape check, and the only genuinely new guard. Called directly rather than injected: it is a pure function of a string, so a malformed id in the composition test exercises the real one. Failure ⇒ inert, one debug record carrying the id.
3. `a.resolveBound(convID)` — unchanged, and this is where **both** of AC-4's registry rows land: `resolveBoundSession` refuses an unknown conversation and a conversation whose `CurrentSessionID` is empty **identically**, returning `(nil, "", false)` and never falling through to the bootstrap session `Pool.Lookup("")` would return (the #678 isolation enforcement point). No second set of guards is built beside it.
4. the runner's rotation capability is probed and the inert arm recorded, then `startFreshRunner(runner, oldID, a.rotate)` — unchanged — rotates and respawns.

The cursor is **never written** by any of this: `activeConversation.set` is called only from `sessionRouter.Route` on a successful route. AC-1's cursor-immobility clause is therefore a non-regression pin, not new behaviour.

`cmd/pyry/relay.go`'s comment describing the adapter's resolution chain as "active → CurrentSessionID → Pool.Lookup → runner" goes stale (it becomes the absent-field path only) and is **deliberately left**: it is a seventh production file and #2103 has the identical situation one field above, so both siblings leave `relay.go` alone and a later pass fixes both.

### AC-2 needs no code

`Pool.RotateForNewSession` fires `notifyTransition`, which calls `rebindConversation(previousID, newID)` **before** invoking the observer. The emitter then resolves the wire `conversation_id` via `conversationForSession(convReg, payload.NewSessionID)`. So rotating B's session rebinds B and the broadcast transition carries B's id, reaching every interactive conn through the existing fan-out. Nothing about this is new; the plan asserts it as a property to test, not to build.

## Concurrency model

No new goroutines, no new locks, no new shared state.

`handleNewSession` runs on the manager's single `Run` dispatch goroutine, so the `s.interactive` read stays lock-free under the package's single-owner invariant — unchanged by adding a decode. `StartNewSession` is called from that goroutine and is safe from any goroutine: `activeConversation.CurrentConversation` takes its own leaf mutex, `Registry.Get` and `Pool.Lookup` take theirs, and `RotateForNewSession` runs its fan-out off `Pool.mu` (the established leaf-callback discipline). Lock order is unchanged because the call sequence is unchanged — only the *argument* to `resolveBound` differs.

One pre-existing race is worth naming because the named path reaches it the same way the cursor path already does: `RotateForNewSession` can return `ErrSessionNotFound` when the binding vanishes between resolve and rotate (two `new_session` frames racing). `startFreshRunner` already handles it — it disarms the rotation gate it armed and propagates the error for `handleNewSession` to Warn-log. Naming a conversation neither widens nor narrows that window.

## Error handling

Every failure is **inert**: no rotation, no respawn, no spawn of a child that did not exist, no reply frame, and never a fall-through to bootstrap. AC-4's four rows and where each lands:

| Case | Guard | New? | Record |
|---|---|---|---|
| Id fails the shape check | `conversations.ValidID` in `StartNewSession` | **yes** — the only new guard | debug, with the id |
| Id not in the registry | `resolveBoundSession`'s `Get` miss | no | debug, with the id |
| Bound session id empty | `resolveBoundSession`'s `CurrentSessionID == ""` guard (#678) | no | debug, with the id — *the same record as the row above* |
| Named conversation has no live child | the optional `RestartFresh` capability probe | no (the assertion exists in `startFreshRunner`) | debug, with the id |

The two registry rows **deliberately share one record**. `resolveBoundSession` refuses the unknown and the unbound identically and returns `(nil, "", false)`, so the caller structurally cannot distinguish them — that non-distinction *is* the #678 isolation property, not an oversight, and it also denies a paired-but-hostile client an existence oracle over conversation ids. AC-4 asks that each case produce a debug line carrying the id, not that each produce a distinct line.

The fourth row's probe is a one-line type assertion at the starter, duplicating the one inside `startFreshRunner`. That duplication is deliberate and is `activeInterrupter`'s shape (its `armNone` record does the same job through `interruptRunner`'s return): `startFreshRunner` keeps its own assertion because that assertion is what orders the #1330 gate arming below the inert return, and because `dispatch_arms_test.go` and `inbound_deliver_rotation_test.go` drive it directly. Widening `startFreshRunner`'s signature to report the arm would touch a shared helper and its other callers for one caller's logging need.

Log posture: the id is client-supplied, so it is recorded at **debug** only — matching `handleDequeueMessage`, which logs its client-supplied `conversation_id` at debug on the no-op path. `activeInterrupter` logs at Info, but what it logs is its own *arm identity* (#1192/#1193), which is daemon-authored; AC-4 asks for debug and this half of the record is the client's. Payload bytes and decode errors are never logged.

**The invalid-shape arm logs a bounded rendering of the id, and it is the only arm that needs one.** The other three arms log a string that has already passed `ValidID`, so it is provably 36 bytes of `[0-9a-f-]`. The shape-check failure arm is the sole place an arbitrary client string reaches a log call, and it is bounded only by the application-envelope cap — kilobytes of attacker-chosen bytes per frame, repeatable. A small unexported helper in `cmd/pyry` returns a copied prefix (an explicit copy, not a slice of the decoded payload, so nothing pins the frame's allocation) with an elision marker when it truncates. This is the security review's one actionable finding; it is folded in here rather than deferred.

## Testing strategy

RED before GREEN in each package. Scope for § B2: `internal/protocol`, `internal/relay`, `cmd/pyry`, `internal/e2e`.

**`internal/protocol/messaging_test.go`** — the `dequeue_message` round-trip/malformed pair is the shape. Scenarios:

- round-trip `new_session.json` → id decodes; re-marshal is byte-stable against the fixture.
- `new_session_bare.json` (`{}`) and `new_session_empty_conversation.json` (`""`) → both decode to the empty string, pinning that a decoder cannot be asked to tell them apart and that neither is a distinct wire meaning.
- malformed body → decode error, zero value.
- `omitempty` on marshal: a zero-value struct emits `{}`, matching the bare fixture.

**`internal/relay/v2session_newsession_test.go`** — `fakeSessionStarter` records the ids it was handed (guarded by its existing mutex). Scenarios:

- frame carrying an id → the seam receives exactly that string; the relay neither validates nor rewrites it.
- bare frame / `{}` payload / nil payload → the seam receives `""`, one call each.
- a payload that is not valid JSON → still exactly one call, with `""` — the tolerant-decode contract.
- non-interactive conn carrying an id → **zero** calls (the capability gate still precedes the decode).

**`cmd/pyry` — new starter-composition test.** `activeSessionStarter` appears in no `_test.go` file today; this is a write, not an extend. Its three seams are plain func fields, so the fakes are trivial. It is the cheapest home for AC-4's whole inert set. Scenarios, table-driven:

- named valid id, bound with a rotatable runner → `resolveBound` is called with **the named id, never the cursor's**, and `rotate` fires once with that conversation's bound session id.
- empty id, cursor set → `resolveBound` called with the cursor's id (AC-3 at the composition level).
- empty id, cursor empty → inert, no `resolveBound` call.
- malformed id (several shapes: not a UUID, wrong version nibble, uppercase, 35 chars) → inert, **`resolveBound` never called**, one debug record.
- `resolveBound` reports `!ok` → inert, no `rotate`.
- bound runner exposes no `RestartFresh` → inert, no `rotate`.
- a nil `log` field → the arms still run (the `logger()` fallback), pinning that an unwired literal cannot panic on a remotely-driven path.

**`internal/e2e/relay_v2_stream_new_session_test.go`** — the new two-conversation case, AC-1 and AC-2's proof. Since #2085 `create_conversation` no longer spawns a child, so the sequence matters or the test passes vacuously:

1. mint conversation B over the wire (the `interrupt` test's create-and-drain block is the precedent);
2. send a message to B — brings B's child up;
3. send a message to A — moves the cursor **back to A**;
4. send `new_session` naming **B**.

Assertions: B's `session_transition` arrives carrying **B's** conversation id (not A's); A's session id is unchanged and A's child is untouched (the `childStdinLog` / `childStdinLogs` attribution helpers already in the file); a post-rotation turn to B reaches the *new* child. The existing bare-path test is left intact as AC-3's e2e proof.

**Live suite** — `internal/e2e/realclaude/interactive_stream_new_session_test.go` proves the bare path, which this ticket leaves working exactly as today. No new case, no `needs-real-claude`.

## Open questions

1. **Does `sess.Runner()` return nil for a created-but-unmessaged conversation after #2085, or a non-nil runner lacking `RestartFresh`?** Either way the capability probe is inert and AC-4's fourth row holds, and the composition test covers it deterministically with an injected fake. To confirm in Phase B only so the e2e comment describes the real state; it cannot change the design.
2. **Does `codes.go`'s doc correction disturb any drift detector?** `TypeNewSession`'s string is unchanged and it stays in `v2OnlyTypes`, so the expectation is no. Confirm by running `internal/protocol`'s tests; if a detector does key on the prose, that is a real finding to record here.

Both are resolved in Phase B and any design consequence is recorded under `## Revisions`.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings — the boundary is explicit and single. The untrusted string enters at `handleNewSession`'s `json.Unmarshal` and is validated in exactly one place, `activeSessionStarter.StartNewSession`, by `conversations.ValidID`. It is *deliberately unvalidated in between*, because `internal/relay` imports neither `internal/conversations` nor `internal/sessions`, so the check cannot live there. The risk that creates is a future reader of `SessionStarter` assuming a validated value; the mitigation is that the seam's doc block states the contract in the interface itself ("client-supplied and unvalidated at this boundary; empty means the cursor"), which is where a caller reads it. No downstream consumer of the string exists between the two points — `handleNewSession` passes it straight through and does nothing else with it.
- **[Tokens, secrets, credentials]** No findings, and the reason is structural rather than incidental: **the client names a conversation, never a session.** The id claude is respawned under is minted inside `Pool.RotateForNewSession` from `crypto/rand` via `NewID`, so no client-chosen value can become a session id, an allocated-UUID skip-set entry, or a `--session-id` argument. Nothing in this path creates, stores, compares or rotates a credential.
- **[File operations]** No findings — **the id never becomes a path component.** The only two disk writes on the path (`Pool.saveLocked` → `sessions.json`, `rebindConversation` → `Registry.Save` at `p.convRegistryPath`) use daemon-owned paths fixed at composition, and the transcript file claude creates is named by the daemon-minted new session id. The adversarial question this category actually turns on is whether a *named-but-unknown* id can cause a create: it cannot — `resolveBoundSession` reaches the registry only through `Registry.Get`, a read, and there is no code path on which `new_session` mints a conversation or spawns a child that did not exist. That is AC-4's "no spawn of a child that did not exist", enforced by a refusal *before* any spawn rather than by a check after one.
- **[Subprocess / external command execution]** No findings — the respawn is `RestartFresh(string(newID))` and `newID` is the daemon-minted rotation id. The `oldID` fed to `startFreshRunner` comes from `conv.CurrentSessionID`, i.e. registry state, not from the frame. The client string reaches no `exec.Command` argument, no environment variable, and no shell. Verified by tracing every value `startFreshRunner` receives back to its origin, not by inspecting the call site alone.
- **[Cryptographic primitives]** No findings — no primitive is selected, reused or compared here. `ValidID` is a shape check on a non-secret identifier, so constant-time comparison is not applicable; treating it as a secret would be the error, since the id is one the client already knows.
- **[Network & I/O]** No findings — this ticket adds no read from a socket. The decoded payload is bounded upstream by the existing application-envelope cap, and the decode is a `json.Unmarshal` into a closed one-field struct, which allocates no unbounded intermediate. The one place that bound is load-bearing is the log record, handled below.
- **[Error messages, logs, telemetry]** **SHOULD FIX — folded into the design rather than deferred.** The invalid-shape arm is the only place an arbitrary client-chosen string reaches a log call, and it is bounded only by the envelope cap: kilobytes per frame, repeatable, at debug. `handleDequeueMessage` has the same unbounded exposure today; inheriting it would be copying a defect. § Error handling now specifies a bounded, *copied* prefix (a copy so the record cannot pin the frame's allocation) with an elision marker. Log *injection* is separately non-applicable: `slog`'s TextHandler quotes and cmd/pyry's JSONHandler escapes, so control bytes cannot forge a record. Nothing on this path reaches the wire — the frame is fire-and-forget with no reply — so no error message leaks anything to a client at all.
- **[Concurrency]** No findings. No new goroutine, lock or shared state; the call sequence is unchanged and only the argument differs, so lock order cannot change. Two frames naming the *same* conversation race exactly as two bare frames do today: one wins, the loser takes `RotateForNewSession`'s `ErrSessionNotFound`, and `startFreshRunner` disarms the #1330 gate it armed rather than leaving it to outlive a rotation that never happened. Two frames naming *different* conversations are newly possible; both serialise on `Pool.mu` inside `RotateForNewSession` and touch disjoint pool entries.
- **[Threat model alignment]** No findings, on the two threats that actually reach this path. **Threat 1 (attacker-authored content reaching a render surface) does not land**, and the check is not the obvious one: the `session_transition` this ticket causes *does* carry a `conversation_id`, but it is resolved by `conversationForSession` from the daemon's own registry keyed on the new session id — **the client's string is never echoed to any outbound frame**, so the field stays daemon-asserted exactly as it is today. The per-device permission gate (#702) exemption is unchanged: `new_session` remains a normal paired-phone action.
- **[Threat model alignment — the widening that looks real and is not]** Stated explicitly because it is the finding a reviewer will reach for. Naming a conversation *does* widen the set of conversations one frame can reach from "the cursor's" to "any". It does not widen the **trust boundary**, because that set was already reachable: `send_message` names its own conversation, a successful route stamps the cursor, and a bare `new_session` then rotates it. A paired but hostile device could already restart any conversation with two frames; this removes the dance, not a barrier — and it removes a *misfire*, since the two-frame form is exactly what a benign client cannot perform. The trust level is the one § Security model already publishes for `request_attachment` and, since #2142, for `attachment_chunk`: a client-named `conversation_id` is a registry-validated lookup key, never authorization.
- **[Threat model alignment — existence oracle]** No findings, and the merged record is what buys it. An unknown id and a known-but-unbound one produce the *same* daemon-local debug record and the *same* wire behaviour (nothing), so the frame answers no question about which conversation ids exist. The residual signal — a valid, bound id produces a broadcast `session_transition` and an invalid one does not — discloses nothing new, since every attached client is a paired device that can already enumerate conversations through existing verbs.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06

## Revisions

### 2026-09-06 — Phase B

The design shipped as planned. Both Open Questions resolved without changing it; two implementation details are recorded because they cost a cycle each and would cost the next reader the same.

1. **Open question 2 — no drift detector keyed on the prose.** `internal/protocol`'s full suite is green with `TypeNewSession`'s doc block corrected; the constant's string is unchanged and it stays in `v2OnlyTypes`, so the partition test never noticed.
2. **Open question 1 — not separately probed, and it could not change anything.** Whether `Session.Runner()` returns a nil interface or a runner without `RestartFresh` for a created-but-unmessaged conversation, the capability probe is inert either way; `TestActiveSessionStarter_InertArms` pins the arm deterministically with an injected `baseRunner`. The question does not arise in the e2e case at all, because M3 sends a message to B specifically to bring its child up — a conversation with no live child would make that test vacuous, which is the trap the ticket flagged.
3. **The relay table's "truncated body" row became a wrong-typed one.** `Envelope.Payload` is a `json.RawMessage`, so a syntactically broken body cannot be marshalled into a frame by the test harness at all — `json.Marshal` rejects it before it reaches the wire. A well-formed body whose `conversation_id` is a number is the reachable undecodable shape and exercises the same tolerated-error arm.
4. **The e2e case was verified against a mutant, and the first two attempts at that verification were themselves wrong.** With `StartNewSession`'s named id forced to `""` the test fails at M5 naming the defect exactly ("the rotation landed on conversation A, the CURSOR's conversation"). Getting there took two false greens worth recording: `go test -overlay` does **not** reach this suite, because the harness shells out to its own `go build` for the `pyry` binary and the overlay applies only to the test package's compilation; and even with the source edited in place, `go test` served a **cached** pass, because nothing the cache tracks had changed — the daemon is a subprocess. `-count=1` plus an in-place edit is the only combination that actually re-runs the daemon under a mutation.

### 2026-09-06 — Rework (verifier FAIL on PR #2157)

**The plan's Error-handling table was wrong on one row, and the entry above repeated the error.** § Error handling routed AC-4's fourth case — "the named conversation has no live child" — to `startFreshRunner`'s optional `RestartFresh` capability probe and marked it "no" under *New?*. Revision entry 2 then closed Open Question 1 with "the capability probe is inert either way". **Both are false.** The probe asks what the runner *can* do, and production's answer is always yes: `streamRunner` exposes `RestartFresh` unconditionally and `Pool.buildSession` assigns it at mint time, so a conversation created but never messaged reaches the probe with a perfectly capable runner whose child has simply never spawned. `create_conversation` binds `CurrentSessionID`, so `resolveBoundSession` resolves it too. Control reached `Pool.RotateForNewSession`, which checks only that the session is in the pool — never that a child is live.

The observable damage was real, not theoretical: the pool was rekeyed, `sessions.json` persisted, the conversation rebound, and a `session_transition` broadcast telling every interactive client to render a delimiter for a chat that had never had a turn — while `RestartFresh` spawned nothing, because it only records the pending id when no child is live. So the ticket shipped a rotation with no session to show for it, and two published artifacts (`docs/protocol-mobile.md` § New session and its `## Changelog` bullet) asserted the opposite. The plan's "why the inert set is mostly free" paragraph is what carried the error forward: three of the four rows genuinely land on pre-existing guards, and the fourth was assumed to by association.

**The design change.** `activeSessionStarter.StartNewSession` gains a fifth arm below the capability probe: a named conversation whose bound runner reports no live child is inert, with its own `v2.new_session.no_live_child` debug record carrying the id. The liveness signal is `sessions.Runner.State().ChildPID`, whose own field doc defines it that way ("PID of the running child, or 0 when none") — unstarted, backing-off, evicted and stopped all report 0, and all four are states with nothing to restart fresh. A spawn that has started but not yet published its pid also reads 0 and is refused, which is the fail-safe direction the whole reject set takes and is re-sendable.

**The arm is named-only, and that asymmetry is the decision.** AC-4 requires a *named* childless conversation to be inert; AC-3 requires the bare frame to behave exactly as before #2099, where an evicted cursor conversation rotates and comes back up under the fresh id. A guard on both paths would buy the first at the cost of the second. `TestActiveSessionStarter_UnnamedFollowsTheCursor` now drives a runner with **no** live child specifically to pin that, and reddens against a mutant that drops the `named &&` conjunct.

**The unit double was the reason the defect survived, so it changed too.** AC-4's row injected `baseRunner{}`, which lacks `RestartFresh` — a shape production never produces behind a bound conversation. The row greened on the capability arm and the real arm was never exercised. `restartFreshRunner` now offers `RestartFresh` always and carries liveness in `State()`, exactly as `streamRunner` does, and the table has two rows where it had one: a capability row (`baseRunner{}` → `no_restart`) and AC-4's actual row (a capable runner with `childPID == 0` → `no_live_child`). Both mutants — guard disabled, and guard un-scoped — redden exactly one row each.

**The e2e case grew an M2.5 milestone** between minting B and messaging it, which is precisely the created-but-unmessaged state. It asserts the registry first and the daemon's record second, in that order deliberately: a daemon without the guard rotates within milliseconds, so the registry check fires first and the failure names the rotation itself rather than a missing log line. M2.5 and M5 now send the identical frame naming the identical conversation with M3 as the only variable, so neither verdict can be vacuous — M5's rotation is not unconditional because M2.5 changed nothing, and M2.5's refusal is not a dead wire because M5 rotates.

**A transport constraint worth recording, because it cost a cycle and the first M2.5 was built on top of it.** `fakephone.ReceiveBytes` reads under a `context.WithTimeout`, and a cancelled read **closes the websocket** — so a receive timeout is terminal for that phone, not a benign "nothing arrived". Any "assert nothing arrives" step over this transport must therefore observe the daemon's log (`waitForLog` / `waitForLogLineAll`, whose own doc records the same constraint) rather than wait on the wire. M5's re-send-on-cadence loop had the same latent hazard — a quiet 500 ms would have killed the conn and then reported the death as a send failure — and is now one send against one deadline, which the four round-tripped milestones before it make safe.

**Plan compliance.** The two-conversation e2e case lives in a new `internal/e2e/relay_v2_stream_new_session_named_test.go` rather than in the existing `relay_v2_stream_new_session_test.go` the plan named; the existing file is 1387 lines and already carries the bare-path proof end to end. Recorded here because the previous leg made the choice without recording it. Two helpers also moved to where they belong: `sendNewSessionFrameFor` from the shared-helpers file (whose grouping is historical, not thematic) to its one caller, and `internal/relay`'s `mustPayload` deleted in favour of the byte-identical `mustMarshal` already in that package.
