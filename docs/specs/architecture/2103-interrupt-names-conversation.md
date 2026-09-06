# #2103 — `interrupt` names the conversation it stops

Plan for `feature/2103`, written against `main` at `57c120df`.

`interrupt` is the last bare per-conversation control frame. It carries no
conversation id, so the daemon actuates whatever its process-wide follow-active
cursor points at — a chat the operator may not be looking at. This ticket gives
the frame an optional `conversation_id`, the shape #2099 landed for `new_session`,
and leaves the bare frame byte-identical for un-upgraded clients.

## Files read

The reading list behind the design. `codegraph_context` seeded it; the entries
below are the ones that changed a decision.

- `internal/relay/v2session_modal.go` → `handleInterrupt` — the handler this
  ticket widens: capability gate, nil-seam guard, one seam call, tolerated warn.
  `handleNewSession` beside it is the shape to copy verbatim (tolerant decode,
  courier posture, never echo the decode error).
- `internal/relay/v2session.go` → `dispatchAppFrame`'s `protocol.TypeInterrupt`
  case — the one routing line that must start passing the envelope.
- `internal/relay/v2session_seams.go` → `Interrupter` — the interface to widen,
  and `SessionStarter` right below it, whose doc block is the contract wording to
  mirror. Also `V2SessionConfig.Interrupter`, whose "Production wires
  `*supervisor.Supervisor`" claim has been false since #1121.
- `cmd/pyry/main.go` → `activeInterrupter.SendEsc` — the production seam; `SendEsc`
  is the only body that changes. `resolveBoundRunner` beside it already takes a
  `convID` and already carries the #678 isolation guard, so AC-3's "never the
  bootstrap session" is preserved by leaving that function alone.
  `activeSessionStarter.StartNewSession` is the arm-order template, and
  `boundedConvID` / `maxLoggedConvID` are the log bound #2099 added for exactly
  this arm — reused, not re-invented.
- `cmd/pyry/main.go` → `interruptRunner`, `interruptArm` — the pure dispatcher and
  its operator-facing arm values; unchanged, but its `armNone` record is one of the
  arms `SendEsc` must keep emitting.
- `internal/streamsup/envelope.go` → `WriteInterrupt`, and
  `(*streamsup.Runner).Interrupt` in `internal/streamsup/runner.go` — **the premise
  the whole no-liveness-guard decision rests on.** `WriteInterrupt` checks `w == nil`
  first and returns `ErrNoLiveChild` having written nothing; `Runner.Interrupt` is
  `WriteInterrupt(r.Stdin(), …)`. Verified by reading, not inherited from the ticket.
- `cmd/pyry/stream_turn_drain.go` → `startStreamTurnDrainV2` — **the finding that
  reshapes AC-4.** Its active-session gate drops every turn event whose producing
  session is not the *cursor* conversation's bound session, recording
  `stream_turn.not_active` with `kind` + `session_id`. `busy.observe` runs before
  the gate; `emitter.Handle` runs after it.
- `cmd/pyry/relay.go` → the `w.streamSink != nil` branch — where that gate is wired
  (`boundSessionIDForActive`), and where the single shared
  `interactiveTurnEmitterV2` is built over the one global cursor.
- `internal/protocol/messaging.go` → `NewSessionPayload` — the optional-payload
  convention (one `omitempty` string; absent / empty / undecodable are one value)
  and its doc block, the direct template.
- `internal/protocol/codes.go` → `TypeInterrupt`'s doc block, which states "it
  carries NO payload — no conversation_id … a bare control frame". This ticket
  makes that false. `TypeInterrupt` stays in `v2OnlyTypes`; the partition detector
  in `internal/protocol/compat_test.go` needs no change.
- `internal/protocol/messaging_test.go` → the four `NewSessionPayload` round-trip
  tests and their three `testdata/*.json` fixtures — the template for the new
  payload's wire pins.
- `cmd/pyry/new_session_starter_test.go` → `starterProbe`, `restartFreshRunner`,
  `newStarter` — the composition-test template. **`activeInterrupter` has no unit
  test anywhere in the repo** (`cmd/pyry/dispatch_arms_test.go` covers only
  `interruptRunner`, the pure dispatcher, and `interrupt_routing_test.go`, cited in
  the e2e's doc comment, no longer exists), so this ticket writes one from nothing.
- `internal/e2e/relay_v2_stream_interrupt_test.go` → the bare-path proof and its
  vacuous-pass guard (fakeclaude's interrupt mode emits no `result` on a user turn,
  so a `turn_end` can only come from the interrupt).
- `internal/e2e/relay_v2_stream_new_session_named_test.go` → the worked
  two-conversation setup: `seedBoundConversation` before daemon start, mint B over
  `create_conversation`, drive both from one phone, `sendNewSessionFrameFor`,
  `readEntryByLabel`, `waitForLogLineAll`.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`
  — #2099's package overview. Two things reached this design from it: the
  capability-probe-vs-liveness lesson (a type assertion answers "could", a state
  read answers "has"), and its closing ask that **#2103 decide the cursor-vs-named
  asymmetry once for both frames** rather than re-discover it. Answered under
  *Design* below.
- `docs/protocol-mobile.md` → § Interrupt (v2), § New session (v2), § Changelog.
  The New-session section is #2099's already-corrected wording to mirror.

## Context

`interrupt` and `new_session` are both survivors of the terminal era, when one
live claude meant a stop was one Esc keystroke. The stream path resolves a
*specific* conversation's bound runner and interrupts that child, so a
per-conversation action that cannot name its conversation is wrong by construction
under several open chats: pressing Stop on chat B stops chat A's turn when the
shared cursor happens to point at A, or stops nothing when it points at an idle
chat, while B keeps running. The desktop sidebar makes switching without sending
the normal case, so the cursor is routinely stale.

`dequeue_message` names its conversation, `request_attachment` names its
conversation, #2098 put the id on the upload leg and #2099 put it on `new_session`.
This finishes the set.

No ADR is warranted: #2099 already settled the argument this ticket repeats
(naming a conversation is a validated lookup key, not authorization, and any paired
interactive device could already reach any conversation with a two-frame dance), and
the package overview at
`docs/knowledge/features/v2-session-manager-state-machine-inbound-new-session-sessionstarter-seam.md`
already carries it. The documentation phase should fold this ticket's lessons in
beside that section rather than open a decision record.

## Size

Two lines of the § A1 table are exceeded, deliberately, and the floor is what keeps
this one ticket:

| Boundary | Limit | This ticket |
|---|---|---|
| Production source files | ≤ 5 | **6** |
| Total written work | ≤ 800 | **~1100** |
| New exported types | ≤ 5 | 1 (`InterruptPayload`) |
| Consumer call sites needing simultaneous update | ≤ 10 | 3 (one impl, one call site, one test double) |
| Acceptance criteria | ≤ 5 | 5 |
| Distinct reject branches | ≤ 10 | 4 |

The six production files are `internal/protocol/messaging.go`,
`internal/protocol/codes.go`, `internal/relay/v2session_seams.go`,
`internal/relay/v2session.go`, `internal/relay/v2session_modal.go` and
`cmd/pyry/main.go` — the same six #2099 touched (1234 insertions across `ab8951f0`
and `77246d74`).

Every available cut produces a slice that cannot stand alone, so the floor rule
("floor beats ceiling: merge, state the overage, build") applies. Re-derived rather
than inherited from the ticket body:

- **Wire payload alone** — `InterruptPayload`'s only consumer is the handler slice.
  One consumer is not a ticket.
- **Seam widening alone** — does not compile. Changing `Interrupter.SendEsc`'s
  signature breaks `activeInterrupter` and `fakeInterrupter` in the same commit; Go
  offers no seam to cut here.
- **Production change vs. its e2e** — the fix and the test that reddens without it
  stay in one ticket by rule.
- **Docs alone** — no owning pipeline phase.

Split depth is therefore moot: there is no split to propose.

The call-site count is the one that would have bound a refactor-shaped change, and
it is small: `relay.Interrupter` has exactly one implementation
(`activeInterrupter`), one call site (`handleInterrupt`) and one test double
(`fakeInterrupter`, used in three tests). `cmd/pyry/modal_resolve_v2.go`'s
`modalKeystroker` is a **different** interface that happens to declare a method of
the same name — `activeInterrupter` does not satisfy it (no `Answer` / `AcceptTrust`)
and is never assigned to it. It is not touched.

## File overlap

`git fetch origin --prune` then a scan of every `origin/feature/<n>` branch against
the ten files this ticket touches found one hit: `origin/feature/449` on
`internal/protocol/codes.go` and `internal/relay/v2session.go`. It is **not**
in-flight work — issue #449 is CLOSED, the branch's last commit is 2026-05-17, and
no PR exists for it. No blocker set, no `needs-rework`.

## Design

### Wire — `InterruptPayload`

A new optional payload beside `NewSessionPayload` in `internal/protocol/messaging.go`:

```go
type InterruptPayload struct {
    ConversationID string `json:"conversation_id,omitempty"`
}
```

One `omitempty` optional string, so absent payload, absent field, explicit `""` and
a body that does not decode at all are **one value with one meaning**: interrupt the
conversation the daemon's own cursor points at. That is the pre-#2103 behaviour
verbatim, and it is what keeps an un-upgraded client working. `omitempty` also keeps
`{}` the canonical bare form a correct client emits, so the wire has exactly one
shape for "nothing named".

`TypeInterrupt`'s doc block in `internal/protocol/codes.go` currently asserts the
opposite as design and is corrected in place. The constant stays in `v2OnlyTypes`
and out of `inboundAppTypeSet`; the partition detector in `compat_test.go` is
unaffected.

### Seam — `Interrupter` gains the id

```go
type Interrupter interface{ SendEsc(conversationID string) error }
```

The doc block gains the obligations `SessionStarter`'s already carries: the string
is untrusted, `internal/relay` validates nothing about it (it imports neither
`internal/conversations` nor `internal/sessions`, so it *cannot*), an implementation
MUST shape-check before any use and MUST NOT let the string become a path component,
and the empty string is not an error but the cursor path.

Two stale claims are corrected while widening: the type's own doc says
`*supervisor.Supervisor` satisfies it, and `V2SessionConfig.Interrupter` says
production wires one. Both have been false since #1121 made
`cmd/pyry`'s `activeInterrupter` the sole implementation, and `cmd/pyry/relay.go`'s
own comment at the wiring site already says so.

The method keeps the name `SendEsc`. Renaming it to match the actuation would touch
the whole relay package for no behavioural gain, and the seam doc already abstracts
`SendEsc` as "claude's own interrupt" (#1121's decision).

### Handler — a courier, exactly like `handleNewSession`

`dispatchAppFrame`'s `TypeInterrupt` case starts passing the envelope:
`m.handleInterrupt(s, probeEnv)`. The handler's order is load-bearing and unchanged
from today, with the decode inserted after the two gates so a non-interactive conn's
bytes are never parsed:

1. `!s.interactive` → inert, `v2.interrupt.non_interactive` at Info (unchanged).
   Naming a conversation is not a way around the capability gate.
2. `m.cfg.Interrupter == nil` → inert, `v2.interrupt.inert` at Debug (unchanged).
3. Tolerant decode: `_ = json.Unmarshal(env.Payload, &p)`. A decode failure leaves
   the zero value, whose empty `ConversationID` *is* the cursor path, so a malformed
   or absent body degrades to the old behaviour rather than dropping the frame. The
   decode error and the payload bytes are never echoed to the client and never
   logged — `encoding/json` quotes attacker bytes into its error string.
4. `SendEsc(p.ConversationID)`, best-effort: an error is Warn-logged as
   `v2.interrupt.keystroke_err` with `conn_id` and the error, and tolerated. The
   `conversation_id` is **not** logged here — it is client-supplied and unbounded
   until the seam's shape check has run, and the seam records it there under its own
   bound.

Validation deliberately does not live in this handler. `internal/relay` cannot tell
a valid conversation id from an invalid one; moving the check here would move the
trust boundary into the package least able to enforce it.

### Production seam — `activeInterrupter.SendEsc`

The only body that changes in `cmd/pyry`. The wiring literal in `startRelay`'s
`relayWiring` is untouched: `currentConv` and `resolveRunner` already have the right
shapes, and `resolveRunner` is already `func(convID string) (sessions.Runner, bool)`.

```
SendEsc(conversationID):
  convID := conversationID
  switch:
    convID == ""            → convID = currentConv(); if still "" → v2.interrupt.no_active_conv, inert
    !conversations.ValidID  → v2.interrupt.invalid_conv_id (boundedConvID), inert
  resolveRunner(convID) fails → v2.interrupt.no_bound_runner, inert
  interruptRunner(runner) → armNone → v2.interrupt.no_actuator, return err
                          → armInterrupt → v2.interrupt.dispatched{conversation_id, arm}, return err
```

Three properties of that order are load-bearing:

- **The empty-string branch runs before `conversations.ValidID`.** `ValidID("")` is
  false, so reversing the two would refuse every un-upgraded client's bare frame and
  silently break backward compatibility. The cursor's own id is daemon-authored and
  is deliberately *not* shape-checked: checking it would change behaviour on the one
  path this branch exists to preserve.
- **`resolveBoundRunner` is not touched.** Its `conv.CurrentSessionID == ""` guard is
  the #678 isolation enforcement point — without it `Pool.Lookup("")` returns the
  **bootstrap** session — so AC-3's "never resolves to the bootstrap session" is
  preserved by leaving that function alone rather than by rebuilding it. The unknown
  id and the known-but-unbound id share one record because `resolveBoundRunner`
  refuses both identically; the non-distinction also denies a paired client an
  existence oracle over conversation ids.
- **Every arm records at Info.** This is where #2103 departs from #2099, which logs
  its named arms at Debug. Every existing `activeInterrupter` arm is Info by the
  explicit #1192/#1193 decision, whose stated contract is that a wired daemon leaving
  no `v2.interrupt.*` record means the frame never arrived. A Debug refusal arm would
  break that closure at the daemon's default `LevelInfo`. The cost is that a hostile
  paired client can now emit Info-level records naming ids it chose; `boundedConvID`
  caps each at 64 bytes plus an elision marker, and the volume question is recorded
  under *Security review* below.

### No liveness guard — and this is the answer #2099 asked for

#2099's package overview closes by asking #2103 to decide the cursor-vs-named
asymmetry once for both frames. **The answer for `interrupt` is that no asymmetry
arises, because the two paths already agree.**

#2099 needed `named && runner.State().ChildPID == 0` because `RestartFresh` on a
childless runner is observable damage with nothing to show for it: it rekeys the
pool, persists `sessions.json`, rebinds the conversation and broadcasts a
`session_transition` for a chat that never had a turn. Interrupt has no such hazard.
`streamRunner.Interrupt()` → `(*streamsup.Runner).Interrupt()` →
`WriteInterrupt(r.Stdin(), …)`, and `WriteInterrupt` checks `w == nil` **first**,
returning `ErrNoLiveChild` having written nothing and mutated nothing. A named
conversation with no live child is therefore **already inert by construction**,
taking the same arm the bare path takes today: the error propagates to
`handleInterrupt`'s tolerated `keystroke_err` warn.

Copying #2099's `ChildPID` probe here would be strictly harmful. It would add a
refusal the bare path does not have today, which is precisely what AC-2 exists to
prevent — and it would introduce the very cursor-vs-named disagreement #2099 flagged
as its own accepted cost. So the guard is not copied, and the reason is recorded
here rather than left to be re-derived.

The generalisation worth carrying: **a blocker's late fix is not automatically the
twin's requirement.** #2099's guard protects a state-mutating actuator; this one's
actuator refuses without writing. Trace the twin's actuation to its own write site
before copying a guard across.

### AC-4 cannot be met literally — what the e2e proves instead

AC-4 asks for: *A and B both mid-turn, cursor on A, frame names B — B's `turn_end`
arrives carrying B's conversation id with `StopReason: "cancelled"`.*

**That frame cannot reach the phone in that state, and no change in this ticket
would make it.** `startStreamTurnDrainV2` gates every turn event on the *cursor*
conversation's bound session (`activeSession()` over `boundSessionIDForActive`) and
drops the rest with a content-free `stream_turn.not_active` record carrying `kind`
and `session_id`. There is one shared `interactiveTurnEmitterV2` over one global
cursor, and `emitter.Handle` sits below that gate. So with the cursor on A, B's
`turn_end` is dropped before it is ever shaped into a wire payload — it has no
`conversation_id` and no `StopReason` to assert on, because it never becomes an
envelope. Fixing that is a different ticket about background-conversation turn
events; this one must not smuggle it in (§ Scope Discipline).

The cursor also cannot be moved back to A once A is mid-turn: only a successful
`sessionRouter.Route` stamps it, and a second `send_message` to a conversation whose
turn is in flight blocks on `streamTurnHoldTimeout` (15 minutes). So A's send must be
the last one, which is what fixes the ordering below.

The substitute is a matched trio, each member covering the others' blind spot:

1. **`v2.interrupt.dispatched` carrying B's conversation id**, from the daemon's own
   log. Proves the frame arrived, resolved B, and dispatched to B's bound runner.
   Under the pre-#2103 daemon the identical record carries **A's** id, so the id in
   the record is the discriminator, not the record's existence.
2. **`stream_turn.not_active` with `kind=turn_end` and B's session id**, also from
   the log. This is AC-4's `turn_end` for B, observed at the one point the
   architecture lets it be observed. It is not a proxy for the actuation — it *is*
   B's turn ending, and since fakeclaude's interrupt mode emits no `result` on a user
   turn, the only possible source of a `TurnEnd` for B is its response to the
   interrupt `control_request` (the existing test's structural-causality guard,
   reused). B's session id comes from `readEntryByLabel(regPath, convB)`.
3. **No `turn_end` for A on the wire**, over a settle window. A is the *active*
   session, so a `turn_end` for A would drain to the phone. Under the defect the
   interrupt hits A and `turn_end{convA, cancelled}` arrives. This is a live,
   on-the-wire discriminator, and it is the direct assertion that A's turn is still
   in flight.

This is a deviation from AC-4's literal wording, decided rather than overlooked, and
it is flagged in the PR body for Juhana. The alternative — asserting a frame the
daemon provably drops — would be a test that can only fail.

## Concurrency model

No new goroutines, no new shared state, no new locks.

`handleInterrupt` runs on the manager's single `Run` dispatch goroutine (it is
intercepted in `dispatchAppFrame` before `dispatch.Route`, not handed to a conn's
`appFrameWorker`), so the `s.interactive` read stays lock-free under the package's
single-owner invariant. `SendEsc` therefore runs on that goroutine too and must
return in bounded time: it does — a registry `Get`, a pool `Lookup`, and one small
write to the child's stdin pipe.

`activeInterrupter` stays a value receiver over three injected seams and holds no
mutable state, so the new parameter adds no sharing. `interruptRunner` stays pure.
The follow-active cursor is **read** here and never written — only
`sessionRouter.Route` stamps it — which is what makes AC-1's "the cursor does not
move" a structural property rather than an assertion.

## Error handling

Every ambiguous state is inert and returns `nil`: no actuation, no error frame, no
reply. The frame stays fire-and-forget, as it has always been; the error-reply
alternative is Juhana's to choose and is out of scope.

| State | Record (Info) | Return |
|---|---|---|
| bare frame, no active conversation | `v2.interrupt.no_active_conv` | `nil` |
| named, fails `conversations.ValidID` | `v2.interrupt.invalid_conv_id` + bounded id | `nil` |
| named or cursor, unknown / unbound conversation | `v2.interrupt.no_bound_runner` + id | `nil` |
| bound runner exposes no `Interrupt` | `v2.interrupt.no_actuator` + id | `interruptRunner`'s err (nil) |
| dispatched | `v2.interrupt.dispatched` + id + arm | actuation's err |

Only the actuation's own error propagates, and `handleInterrupt` Warn-logs it as
`v2.interrupt.keystroke_err` and tolerates it — there is nothing to roll back and no
reply is owed. `ErrNoLiveChild` arrives on that path for a conversation with no live
child, on both the bare and named routes alike.

## Testing strategy

Failing-first, then implementation, per phase B.

**`internal/protocol/messaging_test.go`** — four wire pins mirroring #2099's block,
over three new `testdata` fixtures (`interrupt.json`, `interrupt_bare.json`,
`interrupt_empty_conversation.json`):

- round-trip of a named payload against a committed fixture;
- absent-field and explicit-`""` decode to the same value (only the bare fixture
  round-trips byte-stably — `omitempty` normalises `""` back to `{}`, so asserting a
  stable round-trip on the empty-string fixture would assert the opposite of the
  property);
- the zero value marshals to `{}`, so the bare fixture is what a correct client
  actually sends;
- malformed bodies are a decode error and never a panic, and leave the zero value —
  the property the handler's discarded error depends on.

**`internal/relay/v2session_interrupt_test.go`** — `fakeInterrupter` gains the
parameter and records the ids it was handed. The three existing tests keep their
assertions. One new table-driven test drives named / bare / undecodable-body frames
through the real `Frames`/`Run` loop and asserts the exact string that reached the
seam, with the existing second-conn barrier making the negative rows sound.

**`cmd/pyry/active_interrupter_test.go`** (new file) — the seam's composition test,
built on `new_session_starter_test.go`'s `starterProbe` shape. `activeInterrupter`
has no unit test today, and the id it resolves is now client-supplied, which makes
its arm selection the whole security surface. Scenarios, each one row of the error
table above:

- named + valid + bound → every seam is asked about **B** and B alone, with the
  cursor parked on a *different bound* conversation A. Against an empty cursor a
  regression that ignored the named id would resolve `""` and land inert, reading as
  a pass; against A it actuates A, which is the actual defect and reddens.
- bare → follows the cursor, unchanged.
- bare + empty cursor → inert, no resolve call.
- named + malformed shape → inert, and `resolveRunner` is **never called**
  (asserted on the probe's question log, not on the answer: an inert return is
  indistinguishable from a refused lookup).
- named + unknown/unbound → inert.
- bound runner with no `Interrupt` method → `no_actuator` arm.
- a named conversation whose child is not live → the actuation is *attempted* and
  returns `ErrNoLiveChild`; there is no extra guard. This is the pin that would
  redden if someone later copies #2099's `ChildPID` probe here.
- log hygiene: an over-long named id appears bounded, with the elision marker, and
  the raw string does not appear in full.
- the cursor is never written on any arm.

`conversations.ValidID` is not injected — it is a pure function of a string, so the
tests exercise the real validator rather than a stand-in that could disagree with it.

**`internal/e2e/relay_v2_stream_interrupt_test.go`** — gains the two-conversation
case, built from the #2099 named-new_session setup:

```
M1  seed A bound to bootstrap before daemon start; start under
    interactive_runner:"stream-json" with PYRY_FAKE_CLAUDE_STREAM_INTERRUPT=1
M2  mint B over create_conversation
M3  send to B  → cursor=B, B's child spawns, echo observed, B's turn in flight
M4  send to A  → cursor=A, echo observed, A's turn in flight
      (this order is forced: a second send to A would block on the 15-minute
       turn hold, so A's send must be last)
M5  interrupt NAMING B
      (a) v2.interrupt.dispatched carrying B's id           — arrival + routing
      (b) stream_turn.not_active, kind=turn_end, B's session — B's turn ended
      (c) no turn_end for A on the wire over a settle window — A still in flight
```

The bare-path test in the same file is untouched and keeps proving what an
un-upgraded client sends.

**Not run:** the live-claude suite. Nothing in the acceptance criteria needs a real
claude, so this ticket is deliberately not `needs-real-claude`, and
`internal/e2e/realclaude/interactive_stream_interrupt_test.go` gains no case.

**Verification gate (§ B2):** `go test -race` on `./internal/protocol/...`,
`./internal/relay/...`, `./cmd/pyry/...`, plus the `e2e`-tagged
`./internal/e2e/...`; `go vet ./...`; `go build ./cmd/pyry`. The whole-module race
suite is the verifier's gate, not this run's.

## Protocol document

`docs/protocol-mobile.md` § Interrupt (v2) is rewritten to mirror § New session
(v2)'s already-corrected wording. Two sentences state the current behaviour as
design and both go: *"It carries **no payload** — a bare control frame, with no
`conversation_id` …"* and *"there is no per-connection conversation binding for
interrupt"*. The section gains the field table, the absent-field compatibility
promise, the inert set, and the "any interactive paired phone can interrupt any
conversation it can name, and that is not a widening" paragraph. A `## Changelog`
entry names this ticket.

## Open questions

1. **Should `interrupt` grow a liveness guard for parity with `new_session`?**
   Resolved in *Design*: no. `WriteInterrupt`'s nil-writer check already makes a
   childless conversation inert without mutating anything, and adding a guard would
   change the bare path AC-2 exists to preserve. This is the decision #2099's
   package overview asked #2103 to make once for both frames.
2. **Can AC-4's literal observable be produced?** Resolved in *Design*: no, and the
   substitute trio is specified there. Flagged in the PR body.
3. **Is Info the right level for an arm that logs a client-chosen string?**
   Resolved in *Design*: yes, per the #1192/#1193 closure contract, with
   `boundedConvID` as the bound. The log-volume consequence is recorded as a
   SHOULD-FIX-adjacent note under *Security review*.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** The boundary is explicit and single: `activeInterrupter.SendEsc`
  in `cmd/pyry`. `handleInterrupt` is a courier that decodes and forwards, and
  `internal/relay` cannot be the boundary — it imports neither `internal/conversations`
  nor `internal/sessions`, so it can neither shape-check the id nor resolve it. Downstream
  of `conversations.ValidID` the string is a canonical 36-byte id and nothing else reads
  the raw form. **Named weakness, accepted:** the obligation lives in the seam's doc block,
  not in the type — `SendEsc(conversationID string)` is a bare `string`, so a future second
  implementation gets no compiler help. Identical to `SessionStarter.StartNewSession`, and
  the mitigation is the same: the seam's doc states the contract in the interface rather
  than leaving a caller to assume the string arrived validated.
- **[Tokens, secrets, credentials]** Not applicable, with reason: no token, key, or
  credential is read, minted, stored, compared, or logged on this path. A conversation id
  is a routing key, not a secret — it already crosses the wire outbound on
  `session_transition` and inbound on `send_message` and `dequeue_message` — so naming one
  publishes nothing that was not already published.
- **[File operations]** No findings, traced rather than assumed. The client string reaches
  `resolveBoundRunner` → `convReg.Get` (an in-memory registry scan) and stops there: the
  session id handed to `Pool.Lookup` comes from the daemon's own registry record
  (`conv.CurrentSessionID`), never from the client. Nothing on this path opens, creates,
  stats, or names a file, so path traversal, TOCTOU, file mode, symlink following and
  atomic-write questions have no expression here. The contrast is deliberate:
  `attachments.EnsureDir` *does* make a conversation id a path component, which is why
  `handleAttachmentChunk` must discharge `KnownConversation` first; this seam has no
  filesystem reach at all and therefore carries no such precondition.
- **[Subprocess / external command execution]** No findings, and the property is worth
  stating because the frame's whole job is to poke a child process: **no client-controlled
  byte reaches the claude child.** `interruptRunner` type-asserts and calls
  `Interrupt()`, which is `WriteInterrupt(r.Stdin(), r.nextControlID())`, and
  `marshalInterruptEnvelope` builds a fixed-literal `control_request` whose only variable
  field is a locally minted request id. No `exec.Command`, no argv, no env, no `sh -c`.
- **[Cryptographic primitives]** Not applicable, with reason: the frame rides the existing
  Noise-sealed v2 transport unchanged. No new key material, no nonce minted or burned, no
  RNG, and no comparison of an attacker-controlled value against a secret — the id is
  matched against a registry of routing keys, so constant-time comparison buys nothing.
- **[Network & I/O]** No findings on bounds; one concrete non-widening argument on
  exhaustion. The payload is bounded upstream by the application-envelope AEAD frame cap,
  and decoding is a one-field `json.Unmarshal` whose work is linear in those already-capped
  bytes — there is no client-chosen count sizing any buffer (contrast
  `QuestionAnswerPayload`'s entry list). A paired client can send interrupts freely, but the
  per-frame cost does not rise: an *invalid* id now short-circuits **before** the registry,
  so hostile spam is strictly cheaper than the pre-#2103 bare frame, and a valid-but-unknown
  id costs the one registry scan a bare frame already cost. That every intercepted control
  frame is handled on the manager's single `Run` goroutine, so a flood of any of them stalls
  the manager, is pre-existing and untouched by this ticket — **OUT OF SCOPE**, and it
  belongs to whoever takes inbound rate limiting for the v2 manager as a whole, not to one
  verb.
- **[Error messages, logs, telemetry]** Two findings, both accepted with stated mitigations.
  (a) The decode error and the payload bytes are never echoed to the client and never
  logged — `encoding/json` quotes attacker bytes into its error string, so the discard in
  `handleInterrupt` is load-bearing and the handler must not log `p.ConversationID` either,
  since at that point it is unbounded and unchecked. (b) **Log volume: a hostile paired
  device can drive unbounded Info-level records carrying up to 64 bytes of content it
  chose**, via `v2.interrupt.invalid_conv_id`. This is the one place an arbitrary
  client-supplied string reaches a log call on this path; every later arm logs a string that
  passed `ValidID` and is provably 36 bytes. `boundedConvID` caps it and **copies** the
  prefix, so a buffered record cannot pin the whole decoded frame's backing array, and the
  elision marker stops a truncated id reading as a complete one. The level is Info rather
  than #2099's Debug by the explicit #1192/#1193 closure contract (a wired daemon leaving no
  `v2.interrupt.*` record means the frame never arrived), which does not survive at the
  daemon's default `LevelInfo` if a refusal arm is invisible. The residual cost is log-ring
  churn — the debug bundle's ring is fixed-size, so a flood evicts genuine records — and
  there is no rate limit on this path today. **OUT OF SCOPE** as a rate-limiting concern
  (same owner as the `Run`-goroutine finding above); the content bound is in scope and is
  implemented. No metric or telemetry is emitted.
- **[Concurrency]** One concrete finding, pre-existing and not widened. `Runner.Stdin`
  returns the handle under `r.mu` but the **write happens outside that lock**, so a turn
  write and an interrupt write to the same child can interleave at the pipe. The interrupt
  envelope is well under `PIPE_BUF` and therefore atomic; a large turn envelope is the one
  that could tear. This ticket does not widen it: the same two writers per child existed
  before, the named path merely selects a possibly-different child, and `SendEsc` runs on
  the manager's single `Run` goroutine so two interrupts can never be in flight at once.
  Lock ordering is unchanged and non-nested — `resolveBoundRunner` takes the registry mutex,
  releases it, then takes the pool lock. The TOCTOU between `Get` and `Lookup` (a rebind or
  an idle eviction landing in the gap) yields a stale runner or a refusal, both inert, and
  is identical to today's bare path. No goroutine is spawned, so none can leak.
- **[Threat model alignment]** Addressed against ADR 025 § Security model, which states that
  a user's paired devices are one trust domain and that `interrupt` is exempt from the
  per-device permission gate (#702) because interrupting one's own paired session is a
  normal paired action. Naming a conversation moves *which* conversation one frame reaches
  from "the cursor's" to "any", **not the trust boundary**: a hostile-but-paired device
  could already reach any conversation with a two-frame dance — route a `send_message` to
  move the shared cursor, then send the bare frame — so the field removes a dance only a
  *benign* client was unable to perform. The `interactive` capability gate is unchanged and
  runs before the decode, so naming a conversation is not a way around it. Nothing here
  weakens the merged-refusal posture: unknown and unbound share one record and there is no
  reply at all, so the frame remains no existence oracle over conversation ids, and with no
  reply there is no timing channel either.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-06
