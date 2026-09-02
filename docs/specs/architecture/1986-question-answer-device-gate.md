# #1986 — Gate the inbound question resolution per device, audit it, and wire the seam

## Files read

- `internal/relay/v2session_seams.go` → `QuestionResolver`, `V2SessionConfig` — the
  interface this slice implements, its two-method shape, and the ordering obligation
  ("nothing may be wired until the per-device answer gate exists") this ticket discharges.
- `internal/relay/v2session_question.go` → `handleQuestionAnswer`, `handleQuestionRefusal` —
  the callers. They decode, apply **no** authorization, pass the connection's device
  through, and use the returned bool only to pick between two log records.
- `cmd/pyry/modal_resolve_v2.go` → `modalResolverV2.ResolveAnswer` — the shape to mirror:
  Lookup → fail-closed gate → (classify) → consume → actuate → audit. Its `auditAnswer`
  is the record-writing helper this slice's twin copies.
- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge.AnswerQuestion`, `RefuseQuestion` —
  the two primitives delegated to. Both document "IT APPLIES NO AUTHORIZATION AND IS NOT
  SELF-GUARDING… the per-device answer gate (#1986) sits above it", and both broadcast their
  single `question_dismissed` from a detached goroutine because their intended caller runs
  on the relay `Run` goroutine.
- `cmd/pyry/modal_resolve_v2.go` → `retireQuestion` — the no-answer backstop whose one-shot
  `Resolve` is the single dismissal arbiter, and the reason a gate refusal must leave the
  batch outstanding.
- `cmd/pyry/relay.go` → `startRelayV2` — the composition root. `questionReg` is minted
  before `NewV2SessionManager`; the bridge is built *after* the manager because the manager
  is its broadcaster. `modalResolver.streamApprovals = bridge` is the precedent for the
  late assignment this slice needs.
- `internal/devices/auth.go` → `MayAnswerRemotePermission`, `AuthorizeRemotePermission` —
  the fail-closed eligibility predicate (nil receiver / unset opt-in bit both deny) and the
  allow conjunction the modal path calls as defence in depth.
- `internal/audit/audit.go` → `Entry`, `Outcome`, `Log` — the vocabulary. `OutcomeAllowed`,
  `OutcomeDenied` and `OutcomeDeniedUnauthorized` already span this path's three cases;
  `Entry` carries a prompt id, a prompt class and non-secret identity and has no field that
  can hold a secret.
- `internal/protocol/questions.go` → `QuestionAnswerPayload`, `QuestionAnswerEntry`,
  `QuestionRefusedPayload` — the untrusted inbound shapes. `question_batch_id` and
  `answer_token` are the only two fields marked safe to log; the index is carried and never
  range-checked; values are remote-authored free text.
- `internal/questionbridge/` (via `docs/knowledge/features/questionbridge-package.md`) →
  `Registry.Lookup`, `Registry.Resolve` — `Lookup` is a pure clone-on-read; `Resolve`'s
  read-and-delete is the one-shot that makes "exactly one broadcaster" structural.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-question-control-questionresolver-seam.md`
  — the seam's published contract: the bool is a diagnostic, never a broadcast trigger.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md`
  § "The question arm" — carries three lessons this slice must honour: `byQuestion` is a
  second map so a lookup can never become an authorization; the detached-broadcast
  push-failure arm and an empty-log-buffer claim are **mutually exclusive** and must stay
  two tests; and "no audit record is written on `retireQuestion`" — a dismissal has no
  security decision behind it, which is exactly what makes a *resolution* different.
- `cmd/pyry/stream_approval_test.go` → `newQuestionFixture`, `surfacedQuestion`,
  `goodAnswers`, `updatedInput`, `waitPush`, `lastQuestionDismissed`, `questionLen`,
  `auditLogger`, `auditRecords`, `testDevice`, `eligibleDevice` — the same-package test
  surface this slice's tests are built from; nothing new is needed.
- `cmd/pyry/relay_guard_test.go` → `TestOutstandingQuestionsWiredToSurfacerRegistry` — the
  structural guard asserting `questionbridge.New()` is called exactly once in `relay.go`.
  The wiring below mints no second registry, so the guard stays green and remains the
  second-registry safety net this slice needs no new guard for.

## Context

Every half of the inbound question path is in the tree and none of it is reachable from
the wire. The relay intercepts `question_answer` / `question_refused` and hands them to a
nil-able `QuestionResolver` seam (#1984); `streamApprovalBridge.RefuseQuestion` (#1990)
and `AnswerQuestion` (#1991) resolve an outstanding batch to the refusal deny and to
claude's answer verdict. Nothing implements the seam, and `V2SessionConfig.QuestionResolver`
is nil at every construction site, so the frames are inert.

This slice implements the seam with a per-device gate in front of both primitives, records
each security decision in the audit sink, and wires it at the composition root. It is the
authorization boundary for an internet-sourced frame that resolves claude's blocked tool
call: everything upstream is deliberately inert, everything downstream assumes the caller
was authorised.

No ADR is warranted — this installs an existing gate (`MayAnswerRemotePermission`, ADR 025
§ "Security model") on a second frame family rather than deciding anything new.

## Design

One new file, `cmd/pyry/question_resolve_v2.go`, beside `modal_resolve_v2.go`, plus a
wiring hunk in `cmd/pyry/relay.go`.

### The actuator seam

```go
// questionActuator is the pair of daemon-side primitives a gated resolution
// delegates to. Declared at the consumer (CODING-STYLE); *streamApprovalBridge
// is the production implementer.
type questionActuator interface {
	AnswerQuestion(batchID string, answers []protocol.QuestionAnswerEntry) (consumed bool)
	RefuseQuestion(batchID string) (consumed bool)
}
```

Two methods rather than one, because the two arms carry different payloads and neither is
derivable from the other. `streamApprovalResolver` beside it is the precedent for declaring
the bridge's surface at the consumer so the resolver's tests drive it without the real
bridge.

### The resolver

```go
type questionResolverV2 struct {
	reg    *questionbridge.Registry
	bridge questionActuator // nil ⇒ no stream-approval bridge; both arms inert
	logger *slog.Logger
}

func newQuestionResolverV2(reg *questionbridge.Registry, logger *slog.Logger) *questionResolverV2

func (r *questionResolverV2) ResolveAnswer(p protocol.QuestionAnswerPayload, dev *devices.Device) bool
func (r *questionResolverV2) ResolveRefusal(p protocol.QuestionRefusedPayload, dev *devices.Device) bool
```

The registry is a **constructor parameter** and the bridge is assigned **after
construction** — the split falls straight out of the composition root's ordering:
`questionReg` exists before `NewV2SessionManager`, the bridge does not.
`newModalResolverV2(reg, kb, logger)` + `modalResolver.streamApprovals = bridge` is the
same split for the same reason.

### Ordering — the load-bearing part

Both arms run the same four steps; the answer arm has one extra. Mirrors
`modalResolverV2.ResolveAnswer` step for step, and the shared prefix lives in one
unexported helper so the two arms cannot drift.

1. **No actuator ⇒ inert.** A nil `bridge` returns false before anything else. This is the
   deterministic guard behind AC #5: `bridge` is an interface field, so a method call on it
   would panic in a daemon with no stream-approval bridge (foreground / PTY). It precedes
   even the lookup, so no registry content can route around it.
2. **Lookup, never Resolve.** `reg.Lookup(batchID)` is a pure read. A miss — unknown batch,
   or one an earlier resolution or the backstop already consumed — returns false with **no
   verdict, no broadcast and no audit**: no security decision was made (AC #4). The looked-up
   payload is **discarded**; the resolver never holds a byte of question text.
3. **Fail-closed eligibility gate, before the consume.** `dev.MayAnswerRemotePermission()`
   — a nil device, an unauthenticated one, and one with the opt-in bit unset all deny.
   A denial writes one `denied_unauthorized` record and returns false, leaving the batch
   outstanding for a legitimate answer or the no-answer backstop (AC #2). This is the whole
   reason step 2 is `Lookup`: consuming first and gating second would burn the batch and
   leave claude blocked until the approval window elapsed.
4. **Delegate.** `bridge.AnswerQuestion(p.QuestionBatchID, p.Answers)` or
   `bridge.RefuseQuestion(p.QuestionBatchID)`. The primitive owns the consume, claude's
   verdict and the single `question_dismissed` — this slice adds no second broadcaster.
   A false return (the one-shot lost to a concurrent retire/refusal, or entries the answer
   primitive rejected) is inert here for step 2's reason: nothing was consumed and nothing
   was decided, so nothing is audited. `classifyAnswer`'s forged-option arm is the precedent
   — "no keystroke, no consume, no audit… it is a malformed client frame".
5. **Audit the accepted decision.** One record: `OutcomeAllowed` for an accepted answer,
   `OutcomeDenied` for an accepted refusal (an authorised operator's explicit refusal, the
   meaning `audit.Outcome` already publishes for that constant).

The answer arm inserts `devices.AuthorizeRemotePermission(dev, devices.OutcomeAllow)`
between steps 3 and 4, as the modal path does: the fail-closed conjunction stays in its
single unit-tested place, and the call is what keeps denying correctly if the gate above is
ever moved. It is **not** applied to the refusal arm — the primitive reads as "allow the
tool call", so it is false for an eligible device refusing, which is the intended outcome
rather than a gate failure.

### Audit

```go
func (r *questionResolverV2) auditQuestion(dev *devices.Device, batchID string, outcome audit.Outcome)
```

`auditAnswer` verbatim with two substitutions: `ModalID` carries the batch id and
`ModalClass` carries a new package constant `classQuestion = "question"`, so a forensic
reader can tell a question record from a modal one. Identity is `dev.TokenHash` /
`dev.Name`, empty for a nil device (`ResolveCancel`'s pattern). Source is always
`audit.SourceRemote`.

Adding a question-specific field to `internal/audit` is out of scope: it is a third
production file and moves that package's shared no-leak test. Reusing the two id fields is
the ticket's decision, and it is why `retireQuestion` writing no record is not a
contradiction — a dismissal has no security decision behind it; a resolution does.

**No field can carry question text, an answer value, an option label or a device token**:
`audit.Entry` has no field that could hold one, the resolver never reads the batch payload,
and it never touches `p.AnswerToken`.

### Logging

The resolver emits **no log record of its own** on any path. The relay handler already logs
the outcome with `conn_id` and `question_batch_id` — the two fields `internal/protocol`
marks safe — so a record here would duplicate it while its only new material (the batch
body, the answer values) is exactly what may never be logged. `RefuseQuestion` and
`AnswerQuestion` hold the same posture for the same reason. The audit record is the one
write this path makes, and it goes through `audit.Log`'s fixed attribute set.

### Wiring (`cmd/pyry/relay.go`)

Three edits inside `startRelayV2`:

1. Construct beside `modalResolver`, after `questionReg` is minted:
   `questionResolver := newQuestionResolverV2(questionReg, logger)`.
2. `QuestionResolver: questionResolver` in the `V2SessionConfig` literal.
3. `questionResolver.bridge = bridge` inside the existing `if w.approvals != nil` branch,
   beside `modalResolver.streamApprovals = bridge` — set before `mgr.Run`'s goroutine
   starts, so there is no data race on the field.

The resolver is constructed **unconditionally and assigned unconditionally**, and the
alternative was rejected on a concrete hazard: a `var questionResolver *questionResolverV2`
left nil under `w.approvals == nil` and assigned into the interface field is a **typed nil**
— `m.cfg.QuestionResolver != nil` reads true, the handler's nil guard never fires, and the
methods are called on a nil receiver. That is the hazard `bridge.toolCallInFlight`'s guard
documents, arriving through an interface instead of a method value. Inertness therefore
lives in step 1 of the resolver, where it is a plain field compare.

Two consequences of always installing it, named because they are observable:

- In foreground / PTY mode the handler's `v2.question.*.inert` Debug record is replaced by
  `v2.question.*.noop`. Both are content-free Debug lines.
- An unwired daemon now decodes the inbound payload where #1984 left it doing zero parsing
  of remote-authored bytes. The decode is `encoding/json` into a closed struct, bounded by
  the transport AEAD frame cap, and the handler rejects a decode error without echoing a
  byte of it. Accepted; see § Security review.

`questionbridge.New()` is still called exactly once in the file and the surfacer's arm is
still fed the same identifier, so `TestOutstandingQuestionsWiredToSurfacerRegistry` stays
green — and it remains the second-registry guard, which is why this slice adds none.

## Concurrency model

No goroutine is created here. Both methods run on the relay manager's single `Run` dispatch
goroutine (the seam's documented obligation), and both return in bounded time: a map read
under the registry's own mutex, a pure predicate, one delegated call, one `slog` write.

The delegated primitives detach their `question_dismissed` fan-out onto their own goroutine
precisely because this caller sits on `Run` and `broadcast`'s `ActiveConns` funnels a
request back onto `Run` — so a synchronous fan-out from here would deadlock the manager.
That goroutine ends when the fan-out finishes or when the daemon ctx is cancelled; at most
one exists per consumed resolution, and a resolution requires a batch parked on a human, so
a client cannot drive the count.

`reg` and `logger` are written once at construction; `bridge` is written once at wiring time
before `mgr.Run`'s goroutine starts. No lock is taken here, so no lock ordering is
introduced — the registry's mutex stays a leaf.

The `Lookup`-then-delegate window is deliberately not atomic and is safe in both directions,
`AnswerQuestion`'s own check-then-act argument verbatim: the registry one-shot is the single
arbiter, so a concurrent `retireQuestion` or refusal either wins it (this call returns false
having resolved nothing) or loses it (it broadcasts nothing). The exploitable ordering is
the reverse one, consume-then-gate, which step 3 exists to forbid.

## Error handling

| Failure | Behaviour |
|---|---|
| No stream-approval bridge (foreground / PTY) | false, no panic, no audit, no broadcast |
| Unknown / already-resolved batch id (incl. the empty id a `null` payload decodes to) | false, no audit, no broadcast |
| Ineligible device (nil / unauthenticated / opt-in unset) | false, batch left outstanding, one `denied_unauthorized` record |
| Entries the answer primitive rejects (count, index range, duplicate, per-question shape) | false, batch left answerable, no audit — the primitive owns every one of those rules and none is re-implemented above it |
| Delegate lost the one-shot to a concurrent retire / refusal | false, no audit — nothing was consumed |
| Parked input already gone (permbridge resolved on its own timer) | false, no audit — the backstop still owes its `unanswered` dismissal |

Nothing returns an error: the seam's signature is a bool, and the bool is a diagnostic for
the handler's log record, never a broadcast trigger. No reply goes back to the answering
client on any arm — the relay's question handlers are fire-and-forget and there is no wire
type for a rejection.

## Testing strategy

New file `cmd/pyry/question_resolve_v2_test.go`, same package, built entirely on the
existing question fixtures (`newQuestionFixture`, `surfacedQuestion`, `goodAnswers`,
`updatedInput`, `waitPush`, `lastQuestionDismissed`, `questionLen`, `auditLogger`,
`auditRecords`, `testDevice`, `eligibleDevice`).

- **Answer, gated device (AC #1).** Eligible device → true; the verdict read off the parked
  approval's own handle is an allow carrying the assembled input; exactly one
  `question_dismissed` with `answered`/`remote`; the batch is gone from the registry; one
  `allowed` audit record.
- **Refusal, gated device (AC #1).** Eligible device → true; the verdict is a deny carrying
  `reasonQuestionRefused`; exactly one `question_dismissed` with `refused`/`remote`; one
  `denied` audit record.
- **Gate denies before the consume (AC #2).** Table over {answer, refusal} × {opt-in unset,
  nil device}: false; the approval is **still parked**; the batch is **still outstanding**
  in the registry; zero pushes; exactly one `denied_unauthorized` record. Each row then
  drives a legitimate eligible answer through the same fixture and asserts it resolves —
  the half that proves "left outstanding" means *still answerable* rather than merely
  *still present*.
- **Three distinguishable, content-free records (AC #3).** One test driving all three arms:
  the three `outcome` values are pairwise distinct; each record carries the batch id in
  `modal_id`, `question` in `modal_class`, and the device's hash and label; and the whole
  buffer contains none of the four claude-authored strings, neither answer value, and
  neither the plain device token nor the `answer_token`.
- **Unknown / already-resolved is inert on every count (AC #4).** Table over {answer,
  refusal} × {never-surfaced id, backstop already ran}: false, zero pushes, zero audit
  records, and for the unknown-id rows the approval is still parked.
- **No bridge is a safe no-op (AC #5).** A resolver with `bridge` unset: both arms return
  false, nothing panics, no audit record, no push — driven with an *outstanding* batch in
  the registry so the row cannot pass vacuously.

Two deliberate scope calls:

- **The empty-log-buffer claim and the push-failure arm stay separate.** The question arm's
  package overview records that forcing a `Push` failure both fails `-race` and leaves the
  buffer non-empty, because the error line is written by the detached goroutine *after* the
  `Push` a test's wait signal observes. This slice's leak assertions therefore run with the
  push arm unforced, and the push-failure arm stays covered where #1990/#1991 already cover
  it.
- **"Every interactive client" is not re-proved.** `broadcast`'s capability-gated fan-out is
  the delegated primitives' behaviour and is already tested there; the new claim is that a
  gated resolution produces **exactly one** dismissal frame, which is what these tests
  assert.

Gate: `go test -race ./cmd/pyry/... ./internal/relay/...`, `go vet ./...`,
`go build ./cmd/pyry`.

Not provable at the `internal/e2e` fake tier: fakeclaude's approve rider hardcodes `Bash`
as the gated tool, so surfacing a batch through the fake daemon means extending the harness.
The wire-level proof is #1987, which carries `needs-real-claude` and is blocked on this
ticket.

## Open questions

1. **Does an eligible device's rejected answer deserve an audit record?** Resolved in the
   design above: no. The gate admitted the device but nothing was consumed and no verdict
   reached claude, so there is no decision to record — `classifyAnswer`'s forged-option arm
   is the in-tree precedent for exactly this shape. Revisit only if a forensic reader ever
   needs to distinguish "answered badly" from "never answered", which the batch id in the
   relay handler's own log record already supports.
2. **Should the gate live behind one shared helper or be spelled twice?** Resolved: one
   unexported helper carrying steps 1-3, so the two arms cannot drift and a future third
   arm inherits the ordering. The answer arm's extra conjunction sits in the arm, not the
   helper, because it is genuinely arm-specific.

## Size

Six boundaries re-counted against this plan: 2 production source files (`question_resolve_v2.go`
new, `relay.go` modified), 0 new exported types, 0 consumer call sites needing simultaneous
update, 5 acceptance criteria, 6 reject/inert branches. Total written work is estimated at
~700 lines (~200 production, ~300 tests, this plan) — **over the 400-line guideline**, and
shipped as one ticket because no valid split exists:

- A resolver without the wiring has exactly one consumer, the wiring — the floor rule merges
  it back.
- Wiring without the gate is what `QuestionResolver`'s ordering obligation forbids in
  writing.
- A per-arm split cannot land the wiring in either child: `relay.QuestionResolver` has two
  methods, so a resolver implementing one arm cannot be assigned to the field at all, and
  stubbing the other arm to return false would ship a dead arm that reads as "unknown batch"
  in the relay's log.
- Splitting the audit off separates a security decision from its record and leaves the
  remainder at ~400 anyway.

Calibration against the two nearest analogues, both single `size:s` tickets in this family
that landed without exhausting a budget: #1990 shipped 113 production + 231 test lines;
#1991 shipped 232 production + 403 test + 380 spec = 1015 lines of builder-written work.
This slice's production half is smaller than #1991's (the verdict assembly already exists;
this adds a gate, an audit record and a delegation), so the run sits between the two.
`needs-human:sizing` is applied as a marker for that judgement, per the builder's
depth-capped procedure — the count is over, the split is unavailable, and the work proceeds.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries]** No findings. The untrusted→trusted crossing is explicit and
  single-sited on each axis: *authorization* is `questionResolverV2`'s gate step and
  nowhere else (the relay handler applies none by design, and `RefuseQuestion` /
  `AnswerQuestion` both document that they apply none and are not self-guarding);
  *entry validation* is `answerVerdict` and nowhere else. This slice deliberately
  re-implements none of the latter above the gate, so there is no second, drifting copy
  of the count / index-range / duplicate rules. The resolver is reachable only from
  `dispatchAppFrame` on an open, token-validated session, so an unpaired peer never
  reaches the gate at all, and a connection with no authenticated device presents a nil
  `*devices.Device`, which `MayAnswerRemotePermission`'s nil receiver denies structurally.
- **[Trust boundaries]** No finding, checked rather than assumed: the hostile
  `QuestionIndex` that `protocol.QuestionAnswerEntry`'s doc block warns panics on
  subscript is range-checked in `answerVerdict` **before** the subscript, and the
  entry-count equality is checked first of all, so an arbitrarily long `answers` array is
  O(1) to reject. Passing `p.Answers` through unexamined is therefore safe; the
  alternative — a length bound in the resolver — would be a second bound to keep in
  agreement with the batch.
- **[Tokens, secrets, credentials]** No findings on this slice's own surface.
  `p.AnswerToken` is never read, never compared and never logged: it is a client-minted
  idempotency key, the daemon's dedup is the batch one-shot, and `ResolveAnswer`'s doc
  block records why a server-side token store is deliberately not built (it would
  re-broadcast a prior result and break the single-dismissal property while adding
  unbounded state). `audit.Entry` has no field that can hold a plain token, and the two
  identity fields written here are the SHA-256 hash and the operator-set label the modal
  path already writes.
- **[Tokens, secrets, credentials]** OUT OF SCOPE — **mid-session revocation does not
  propagate.** `devices.Registry.Validate` returns the `Device` by value, so the
  connection's `s.device` is a connect-time copy: clearing a device's
  `AllowRemotePermissions` bit (or removing the device) does not deny an
  already-connected client until it reconnects. This is inherited verbatim from the modal
  arm — `handleModalAnswer` gates on the same `s.device` — and is not introduced or
  widened here; threat #4's per-device revocation is therefore next-connect-granular for
  both frame families. Named rather than silently inherited; a ticket that wants
  live revocation must change the perimeter, not this resolver.
- **[File operations]** Not applicable, by design rather than by omission: the resolver
  opens, creates, reads and names no path. Its only write is `audit.Log` through the
  injected `*slog.Logger`, whose sink the daemon owns.
- **[Subprocess / external command execution]** No findings. Nothing here execs. The
  remote-authored answer *values* do reach claude, as the allowed tool call's updated
  input over the existing mcp-approve control socket — as JSON, never as `exec.Command`
  arguments — and that is threat #1's surface at the trust tier
  `protocol.QuestionAnswerEntry` states: a paired client can already put arbitrary text
  into the conversation with `send_message`, so an answer grants nothing new. The one
  string that travels the *other* way, toward claude on a refusal, is the compile-time
  `reasonQuestionRefused` constant, so nothing an operator or a device authored can ride
  it.
- **[Cryptographic primitives]** No findings. No RNG here; the batch id is a `crypto/rand`
  UUIDv4 minted by `questionbridge.Registry.Record`. No constant-time comparison is owed:
  `Registry.Lookup` compares a daemon-minted routing key, not a secret, and possession of
  a batch id is explicitly **not** sufficient to resolve one — the gate is the
  authorization, which is the property that makes the map lookup's timing uninteresting.
- **[Network & I/O]** No findings. No socket is read and no server is started here, so
  there are no timeouts to set. Per-frame work is bounded twice over: the transport AEAD
  frame cap bounds the payload, and the first check inside `answerVerdict` rejects a
  count mismatch before touching an entry. The `Registry.Lookup` clone this slice adds per
  frame is bounded by `questionbridge`'s own limits (1-4 questions, 2-4 options,
  `maxInputBytes`).
- **[Network & I/O]** No finding, resolved at the seam that actually reads it rather than
  at the seam that raised the claim: the ticket asserts a parked question inherits
  `mcpApprovalTimeout`'s ten-minute window and #1912's re-arm with nothing
  question-specific owed. That re-arm's `AnswerableFunc` is `ApprovalAnswerable`, which
  scans `parkedToolUseIDs` — and that helper covers `byQuestion` as well as `byModal`, so
  the inheritance is real. A gate refusal leaves the batch parked and therefore leaves
  that window running, which is the intended fail-closed direction: the no-answer backstop
  still denies claude and still dismisses the panel.
- **[Error messages, logs, telemetry]** No findings. The resolver emits no log record on
  any path, so there is no field for question text, an answer value, an option label or a
  token to leak into; the audit record's attribute set is fixed by `audit.Log`. The
  handler above it logs `conn_id` and `question_batch_id` only, the two fields
  `internal/protocol` marks safe, and never echoes a decode error. `device_label` is
  operator-set and could carry escape bytes, but it reaches the log through `slog.String`
  exactly as every existing modal audit record does — inherited, not introduced.
- **[Error messages, logs, telemetry]** No finding, stated because the shape invites the
  opposite reading: an ineligible device probing batch ids produces an audit record only
  for ids that name a real outstanding batch, which is an existence oracle *in the local
  audit log* — but none of it reaches the client. Both arms return the same `false`, the
  handler logs the same `noop` Debug record for both, no reply is sent and no frame is
  broadcast, so a probing client cannot distinguish a real batch id from an invented one.
  The log-volume amplification is one line per frame on top of the handler's existing one
  line per frame; rate limiting belongs at the transport, not here.
- **[Concurrency]** No findings, and the happens-before edge was checked at the site
  rather than asserted: `questionResolver.bridge` is assigned inside `startRelayV2`'s
  `w.approvals != nil` branch, which precedes the `go func() { mgr.Run(ctx) }()` that
  starts the only reader — the same edge `modalResolver.streamApprovals` relies on. No
  lock is taken in the resolver, so no ordering is introduced and the registry mutex stays
  a leaf. The non-atomic `Lookup`-then-delegate window is safe in both directions because
  the registry one-shot is the single arbiter; the exploitable ordering is the reverse
  one, consume-then-gate, which the design forbids and AC #2 tests.
- **[Concurrency]** No findings on goroutine lifecycle: this slice spawns none. The
  detached `question_dismissed` fan-out belongs to `RefuseQuestion` / `AnswerQuestion`,
  ends when the fan-out finishes or the daemon ctx is cancelled, and is bounded at one per
  consumed resolution — and a resolution requires a batch parked on a human, so a client
  cannot drive the count.
- **[Threat model alignment]** Threat #1 (prompt injection): unchanged and explicitly
  accepted at the `send_message` tier, above. Threat #4 (token leak via phone): the gate
  is the per-device mitigation this slice installs for the question family; its
  next-connect granularity is the OUT OF SCOPE finding above. Threat #6 (replay): a
  replayed answer or refusal finds the batch already consumed and is inert on every count,
  which is the one-shot doing the work — this slice adds no second dedup and no
  `answer_token` store. Threats #2, #3 and #5 are transport/supply-chain concerns this
  slice neither touches nor weakens.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
