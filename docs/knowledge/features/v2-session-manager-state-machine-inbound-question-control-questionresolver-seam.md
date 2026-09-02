# Inbound question control (#1984) — `QuestionResolver` seam

`question_answer` / `question_refused` are v2 **control** envelopes (phone →
binary), intercepted in `dispatchAppFrame`'s discriminator switch **before**
`dispatch.Route` — the same boundary `modal_answer` / `modal_cancel` use, and
there is **no** `dispatch.Route` handler. This is the **inbound** half of the
daemon-side question bridge: the outbound half surfaces a batch to phones via
`question_shown` ([`internal/questionbridge`](questionbridge-package.md),
\#1973); this slice lets a phone answer or refuse it. It intercepts, decodes
and hands off — it resolves nothing and broadcasts nothing. `security-sensitive`:
an inbound untrusted frame reaches a consumer seam over the internet-exposed
relay (spec-stage security review, verdict PASS). See
[`specs/architecture/1984-question-answer-interception.md`](../../specs/architecture/1984-question-answer-interception.md).

**Unlike modal control, this seam must never broadcast.** `question_dismissed`
has one arbiter, `streamApprovalBridge.retireQuestion` on the `cmd/pyry` side
(see [the question arm](v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md#the-question-arm-1973--a-second-discriminant-ahead-of-the-permission-path)) —
a second broadcaster here would be a second arbiter of whether a batch was
consumed. `QuestionResolver`'s returned `bool` is a diagnostic only, consumed
solely to pick between two content-free log records — `QueueRemover.Remove`'s
exact role in `handleDequeueMessage`, not `ModalResolver`'s `(dismissal, bool)`
shape. This is the one place the `ModalResolver` precedent is deliberately not
followed: that shape exists there *because* the manager broadcasts on it, and
here it must not.

- **`QuestionResolver` carries the whole typed payload, not exploded fields.**
  `ModalResolver` explodes its payloads because they're flat strings;
  `QuestionAnswerPayload` carries a nested `answers` array, so exploding it
  into scalar params is the same thing spelled longer and invites a caller to
  reassemble it wrongly. `*devices.Device` crosses too, exactly as
  `ModalResolver.ResolveCancel` takes it — the handler passes the connection's
  device through and decides nothing about it.
- **`V2SessionConfig.QuestionResolver` stayed nil at every construction site
  until #1986 implemented and wired it.** The field's own doc comment carried
  the ordering obligation this slice discharged (nothing may be wired ahead of
  the per-device gate) so whoever wired it read that at the crossing, and it
  restates the obligations Go's type system can't express: `QuestionIndex` is
  carried but never range-checked and panics on a hostile index, indices may
  duplicate or be missing, `Answers` may be arbitrarily long, and nothing
  beyond `question_batch_id` / `answer_token` may be logged. `cmd/pyry`'s
  `questionResolverV2` (#1986) is now that implementation: `admit` runs the
  Lookup → fail-closed-gate steps both arms share, gating strictly before the
  delegate consumes the batch — burning the batch on an ineligible device's
  frame first would leave claude blocked until the approval window elapsed —
  and `AuthorizeRemotePermission` re-checks the same eligibility as defence in
  depth on the answer arm only, since it reads as "allow the tool call" and is
  therefore false, correctly, for an eligible device's refusal. `relay.go`
  constructs and assigns it **unconditionally** rather than leaving it nil
  under `w.approvals == nil`: a nil `*questionResolverV2` behind the
  interface field is a **typed nil** — the handler's `!= nil` guard reads true
  and calls a method on a nil receiver — so the actuator itself, not the
  resolver, is what goes nil-and-inert on a foreground/PTY daemon.
- **`handleQuestionAnswer` / `handleQuestionRefusal`** take `(s, env)` with no
  `ctx` — `handleDequeueMessage`'s established deviation from the `(ctx, s,
  env)` siblings, since neither handler does cancellable work (no `Push`, no
  broadcast). No `interactive` capability check and no per-device check:
  matches `handleModalAnswer`, which applies neither and leaves the decision to
  the resolver — #1986 states the same boundary from the other side.

## A tolerant decode is safe only for a flat payload

`handleModalCancel` / `handleModalAnswer` do `_ = json.Unmarshal(...)` and let
a decode failure fall through as an empty `modal_id`, harmless because
`ModalCancelPayload` / `ModalAnswerPayload` are flat strings. Copying that
idiom here would not be harmless: `encoding/json` populates the struct fields
it reads *before* the one that fails, so a malformed `answers` array can leave
`QuestionAnswerPayload.QuestionBatchID` populated with a real batch id while
`Answers` stays nil — a well-formed-looking answer to a real batch that
chooses nothing. `QuestionAnswerPayload`'s own doc block forbids exactly this
("a decode failure MUST be a rejected frame, never an empty-but-successful
answer"), and both handlers reject on `err != nil` instead: the seam is not
called at all on a decode failure. **The rule generalizes past this ticket:**
whether a modal-style tolerant decode is safe depends on whether the payload
nests a slice the failing field could sit inside — check the declaring type's
own doc block before copying a sibling's decode idiom, not just its handler
shape.

**The reject rule is about a decode *error*, not an empty result, and the two
come apart at `null`.** `json.Unmarshal([]byte("null"), &p)` returns no error
and leaves `p` at its zero value, so a `"payload":null` frame is not rejected
— it decodes into an empty-batch-id payload, which is an *unknown* batch for
`QuestionResolver`'s implementer to judge, exactly as AC #3 requires.
Rejecting it here on an empty id would install a second arbiter beside the
seam. `TestV2Session_QuestionControl_NullPayload_NotJudgedHere` pins the
pass-through side; `TestV2Session_QuestionControl_DecodeFailure_Rejected`
pins the reject side, both against `handleQuestionAnswer`.

**That `null`/absent-payload shape is unreachable as a genuine decode failure
through the relay test harness.** `protocol.Envelope.Payload` is a
`json.RawMessage` with no `omitempty`, so `json.Marshal` both validates it (a
harness cannot construct literally-malformed bytes on the wire) and encodes an
omitted payload as the literal `null` rather than eliding the key. Every
decode failure reachable over the wire is therefore a **type mismatch inside
otherwise-valid JSON** — `question_batch_id` decodes first, then a
wrong-shaped `answers` fails — never truncated or unparsable bytes. Worth
knowing before writing a fixture for "malformed payload" against any inbound
v2 payload in this package: aim for a type mismatch, not garbled bytes, or the
fixture can't be constructed at all.

**A "the seam never gets a tolerant decode" assertion has to bind on the
resolver's call count, not the value it received.** A fixture whose bad
`answers` array still happens to decode to the *right* batch id would let a
tolerant `_ = json.Unmarshal` pass a check that only inspects the received
payload — the batch id would be correct either way. The binding assertion is
`ResolveAnswer`/`ResolveRefusal` called **zero** times, with the fixture's
`question_batch_id` populated ahead of the malformed array so the case
actually exercises the partial-decode hazard rather than an already-empty id.

## Logging

Same discipline as modal control: `event` + `conn_id` on every path, plus
`question_batch_id` only where the decode succeeded (never on the reject
path — a partially-populated id would attribute refused bytes to a batch).
The decode error itself is never wrapped, logged, or replied — `encoding/json`
quotes offending input into its error string, and those bytes are
remote-authored. `answer_token` is never logged (nothing correlates on it
daemon-side). `TestV2Session_QuestionControl_LogsCarryNoPayload` scans the
captured buffer across the inert, rejected and handed-off paths for the
distinctive answer values and for a raw payload fragment, and separately
pins that an escape-bearing `question_batch_id` reaches the log as `slog`'s
`TextHandler`-escaped form rather than a raw `0x1b` byte — checked, not
assumed, since this is the first inbound handler to log a field an attacker
fully controls the bytes of.

## Testing

`cmd/pyry/question_resolve_v2_test.go`'s six tests prove all five ACs at the
`admit`/delegate boundary, but one branch inside `ResolveAnswer` /
`ResolveRefusal` has no test that isolates it: a **false** return from
`bridge.AnswerQuestion` / `RefuseQuestion` *after* `admit` already passed —
the one-shot lost to a concurrent retire or refusal, or entries the answer
primitive rejected — must skip the audit write. Every fixture that reaches
the delegate in the existing suite also gets a **true** back from it, so an
overlay mutation deleting `if !r.bridge.{Answer,Refuse}Question(...) { return
false }` and letting the audit call run unconditionally survives the whole
`cmd/pyry` package under `-race` (found by #1986's own verifier pass; shipped
as a SHOULD FIX, not closed, because the code reads correctly and the gap is
in coverage, not behaviour). Closing it needs a `questionActuator` stub that
returns `false` from an eligible-device call — the real `streamApprovalBridge`
in the existing fixtures cannot be coaxed into that return without also
losing the one-shot, which is a different, already-covered branch.

## Related

- [Inbound modal control](v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md) — the discipline this slice mirrors (interception point, single dispatch goroutine, fire-and-forget) and the one place it deliberately diverges (no broadcast)
- [Question-batch payload § `QuestionAnswerPayload` / `QuestionRefusedPayload`](protocol-package-question-batch-payload.md#questionanswerpayload--questionrefusedpayload-1983) — the wire shapes and the positional-index / never-range-checked contract this handler reads by
- [questionbridge-package.md](questionbridge-package.md) — the registry #1990's refusal and #1991's answer path resolve against
- [Concurrency](v2-session-manager-concurrency.md) — both handlers run on the single `Run` dispatch goroutine, alongside the other inline control-envelope arms
- `cmd/pyry/question_resolve_v2.go` (#1986) — `questionResolverV2`, the seam's implementation: the per-device gate (`admit`), the terminal-decision audit record, and the composition-root wiring in `cmd/pyry/relay.go`. See [audit-package.md](audit-package.md) for the `classQuestion` record and [the question arm](v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md#the-question-arm-1973--a-second-discriminant-ahead-of-the-permission-path) for the delegates it gates.
