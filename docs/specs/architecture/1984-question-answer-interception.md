# #1984 — intercept the inbound question answer and refusal, route them to a resolver seam

Slice of #1907. Relay leg only: intercept, decode, hand off. Resolves nothing, broadcasts nothing.

## Files read

- `internal/relay/v2session.go` → `dispatchAppFrame` — the control switch this slice extends; its
  doc block fixes the "probe re-decode, no `default` arm, decode failure falls through to
  `dispatch.Route`" contract that makes the two new cases the whole behaviour change.
- `internal/relay/v2session_modal.go` → `handleModalCancel`, `handleModalAnswer` — the discipline the
  ticket names: intercepted before `dispatch.Route`, on the single `Run` dispatch goroutine,
  fire-and-forget, nil resolver ⇒ inert. Also `handleDequeueMessage`, whose `Remove(...) bool` is
  consumed *only* to pick between two log records — the precedent for a reporting seam with no
  broadcast.
- `internal/relay/v2session_seams.go` → `ModalResolver`, `QueueRemover`, `V2SessionConfig` — the
  consumer-side seam declaration shape and the optional-field/nil-behaviour convention
  (`OutstandingQuestions` is #1979's instance of the same pattern).
- `internal/protocol/questions.go` → `QuestionAnswerPayload`, `QuestionAnswerEntry`,
  `QuestionRefusedPayload` — the decoded shapes, and the declaring type's own rule that overrides
  the modal analogy: *"a decode failure MUST be a rejected frame, never an empty-but-successful
  answer, and its error must not embed the raw payload."* Also the sentence marking
  `question_batch_id` / `answer_token` safe to log, written expressly for this handler.
- `internal/protocol/codes.go` → `TypeQuestionAnswer`, `TypeQuestionRefused` — the constants, and the
  two doc paragraphs this slice falsifies (the guard-classification one and "DECLARING THESE
  CONSTANTS CHANGES NO RUNTIME PATH").
- `internal/dispatch/dispatch.go` → `Route`, `sendError` — where an un-intercepted frame goes today:
  `IsKnownAppType` ⇒ `ErrUnknownType` ⇒ a sealed `protocol.unknown_type` reply. That reply
  disappearing is this slice's observable delivery.
- `cmd/pyry/relay_guard_test.go` → `inboundTypes`, `excludedTypes` — the guard AST-reads
  `dispatchAppFrame`'s switch, so the two entries must move the moment the cases land; Assertion #2
  reddens `make check` otherwise.
- `internal/relay/v2session_modal_test.go` → `fakeModalResolver`, `openModalConn`,
  `sealAppFrameConn`, `TestV2Session_ModalControl_NilResolver` — the fake-resolver + real-handshake
  test shape, and `noiseMsgsForConn` as the "no reply reached the client" assertion.
- `internal/relay/v2session_test.go` → `bufferLogger` — resolves at `slog.LevelDebug`, so a
  `Debug`-level inert record is observable by `waitForLogContains` (a level mismatch would make an
  inert-path pin vacuous).
- `docs/protocol-mobile.md` § Application message types, § Question — the six live claims to correct,
  found by wording per the ticket; the dated changelog entries are historical and stay untouched.
- `docs/knowledge/features/v2-session-manager.md`, `.../questionbridge-package.md` (skimmed for the
  question family's prior lessons) — the load-bearing one carried here: the reconcile/dismissal
  broadcast lives on the `cmd/pyry` side (`streamApprovalBridge.retireQuestion` is the sole
  broadcaster), which is why relay-side routing must not broadcast.

## Context

The daemon surfaces claude's `AskUserQuestion` batch as `question_shown` (#1973), retires it with
`question_dismissed`, and re-asserts outstanding batches on reconnect (#1979/#1980). #1983 declared
the inbound vocabulary and deliberately shipped no route.

Today a `question_answer` therefore falls past `dispatchAppFrame`'s control switch — which has no
case for it and no `default` arm — into `dispatch.Route`, where `IsKnownAppType` rejects it and the
client receives a sealed unknown-type error reply. **This slice changes that reply, and changes it
even with nothing wired behind the seam.** That is the deliverable on its own.

The verdict that reaches claude (#1985) and the per-device gate that guards it (#1986) are separate
tickets. This design must be complete and correct without either, and it is: the seam is nil in
every existing wiring site, and no `V2SessionConfig` construction changes.

**No ADR is warranted.** The nil-able-optional-seam decision this follows is already recorded
(ADR 025 § Safe degradation, and the `ModalResolver` / `OutstandingQuestions` field docs); this
slice is the fourth instance of an established pattern, not a new one.

### Sizing — re-derived, and `needs-human:sizing` concurred with

The ticket is over the 400-line ceiling (estimate ~600) and already carries `needs-human:sizing`.
Re-derived independently rather than inherited: `git show --numstat` on the #1979 analogue
(`91985bce`, PR #1981) gives **599 insertions, 0 deletions, 4 files** — same package, same
seam+handler+test shape, shipped in one run. This slice's shape is that plus two comment-only
`internal/protocol` edits, a two-entry `cmd/pyry` test-list move, and ~70 lines of protocol prose.

Split depth reads `parent 1907, grandparent none`, so depth does not bar a split — but the merits do.
Each candidate cut is barred by a rule rather than by a rationalization:

1. **Seam / handlers.** The seam's only consumer in this slice is the handlers themselves — the
   sizing floor's definition of lines inside a ticket rather than a ticket of their own. The
   restatement "intercept-and-drop first, seam second" is the same cut and fails the same way.
2. **`question_answer` / `question_refused`.** Two cases in one switch, one file, one commit, one
   test run. Splitting on the conjunction is the #1940 mistake.
3. **Code / docs.** A docs-only child has no owning pipeline phase.

No fourth cut exists, so the ticket is built as it stands. The label is the marker; this section is
the measurement behind it.

Re-counted against this written plan rather than against the opening sketch, **two boundaries are
exceeded, not one**: total written work (~640 vs 400) and production source files (**5** vs 3 —
`v2session.go`, `v2session_question.go`, `v2session_seams.go`, and the two comment-only
`internal/protocol` files). The file count is recorded rather than argued away: the two
`internal/protocol` edits carry no design surface, but they are production `.go` files and they
count. They also cannot land anywhere else — they are the prose stating the state this slice
falsifies, and a docs-only child has no owning pipeline phase. Routing back to the refiner would
return this same ticket, which is the recursive-split failure mode (#1925 → #1937 → #1940) rather
than a smaller ticket.

## Design

Three production files, mirroring #1979's shape.

### 1. `internal/relay/v2session_seams.go` — the seam

A new consumer-side interface declared beside `ModalResolver`, plus one optional
`V2SessionConfig` field.

```go
type QuestionResolver interface {
	ResolveAnswer(p protocol.QuestionAnswerPayload, dev *devices.Device) bool
	ResolveRefusal(p protocol.QuestionRefusedPayload, dev *devices.Device) bool
}
```

- **The whole typed payload crosses, not exploded fields.** `ModalResolver` explodes because its
  payloads are flat strings; `QuestionAnswerPayload` carries a nested array, so exploding it into
  `(batchID, token, []QuestionAnswerEntry)` is the same thing spelled longer and invites a caller to
  reassemble it wrongly. `internal/relay` already imports `internal/protocol`, so no new import.
- **`*devices.Device` crosses**, exactly as `ModalResolver.ResolveCancel` takes it. The handler
  passes the connection's device through and makes no decision about it.
- **The `bool` is a diagnostic, never a broadcast trigger.** It reports whether the implementer
  consumed the batch, and the handler's *only* use for it is choosing between two content-free log
  records — `QueueRemover.Remove`'s exact role in `handleDequeueMessage`. The relay must not
  broadcast `question_dismissed` on it: `cmd/pyry`'s `streamApprovalBridge.retireQuestion` is the
  sole broadcaster of that frame, and a second one would be a second arbiter. This is the one place
  the `ModalResolver` precedent is deliberately not followed — `(dismissal, bool)` exists there
  *because* the manager broadcasts, and here it must not.
- `ResolveRefusal`, not `ResolveRefused`: `ResolveCancel` ↔ `TypeModalCancel` sets the verb-form
  convention, and the noun for `question_refused` is a refusal.

**The interface doc block is the trust boundary's only signpost, so it carries the obligations
explicitly** (raised as a MUST FIX by § Security review and folded in here before this plan was
committed). Go's type system cannot say "remote-authored", and an implementer reading
`ResolveAnswer(p, dev) bool` alone would reasonably index the parked batch with `p.Answers[i].
QuestionIndex` — which panics on a hostile index, remotely triggerable. `QuestionAnswerEntry`'s own
doc block states the three obligations; the seam is where #1985 first meets them, so it restates
them at the crossing:

- every field is remote-authored and **validated by nothing** on the way in — this handler decodes
  and rejects malformed bytes, and judges nothing else;
- `QuestionIndex` is carried, **never range-checked**, so the implementer owes an explicit check
  before subscripting the parked batch;
- indices may **duplicate or be missing**, and `Answers` may be **arbitrarily long** — neither a
  silently partial answer nor unbounded per-frame work is acceptable;
- the payload's fields **must not be logged** by the implementer beyond the two ids
  `internal/protocol` marks safe.

**Ordering obligation, recorded at the seam rather than only on a ticket:** nothing may be wired to
this field until the per-device answer gate (#1986) exists. This slice is fail-safe *because* the
seam is nil at every construction site — a paired-but-untrusted client's answer reaches no actuator.
Wiring a resolver (#1985) ahead of the gate would open a window in which any paired device can
answer. The doc block says so, so whoever wires it reads it there.

New field `QuestionResolver QuestionResolver`, documented optional: **nil ⇒ both frames are inert**
— consumed by the interception, nothing decoded, nothing handed off, nothing broadcast. Its own
field rather than a method grown onto `ModalResolver`, following #1979's reasoning for
`OutstandingQuestions`: a question batch is its own frame family (#1962), and growing a neighbour
would force every `ModalResolver` implementer to grow with it. **No existing wiring site changes.**

### 2. `internal/relay/v2session_question.go` (new) — the handlers

Two methods on `*V2SessionManager`, beside `v2session_modal.go` in the file layout:

- `handleQuestionAnswer(s *V2Session, env protocol.Envelope)`
- `handleQuestionRefusal(s *V2Session, env protocol.Envelope)`

No `ctx`: neither handler does cancellable work — there is no `Push`, no `forwardEnvelope`, no
broadcast. That is `handleDequeueMessage`'s established `(s, env)` deviation from the `(ctx, s, env)`
siblings, not a new one.

Order inside each handler, and each step is load-bearing:

1. **Nil-seam guard first** ⇒ inert, `Debug` record, return. Mirrors `handleModalCancel`'s guard, and
   buys a security property: an unwired daemon performs *zero* parsing of remote-authored bytes.
2. **Decode, and reject on error** ⇒ the seam is not called at all, `Warn` record, return. This is
   the one place the modal handlers are deliberately not copied. They do `_ = json.Unmarshal(...)`
   and let a failure fall through as an empty `modal_id`, harmless for flat strings.
   `QuestionAnswerPayload` carries a nested `answers` array and Go's decoder populates the fields it
   read *before* the one that failed, so tolerating the error can hand the seam a **valid batch id
   with nil answers** — precisely the empty-but-successful answer `QuestionAnswerPayload`'s own doc
   block forbids.
3. **Hand off** to `ResolveAnswer` / `ResolveRefusal` with the decoded payload and `s.device`.
   Log the returned discriminant. Return. No reply, no broadcast.

**No authorization here, by instruction and by precedent.** No `interactive` check and no per-device
check: `handleModalAnswer` applies neither, leaving both to the resolver, and #1986 states the same
boundary from the other side. See § Security review, which records this as the deliberate deferral it
is rather than an omission.

**A cleanly-decoding payload that names an unknown batch is not judged here.** It goes to the seam,
whose implementer is the sole arbiter. That covers `"payload": null` and an all-empty object, both
of which decode successfully into a zero-value payload: an empty batch id is an unknown batch, not a
decode failure, and adding an empty-id reject here would install a second arbiter — the exact thing
AC #3's second sentence forbids.

### 3. `internal/relay/v2session.go` — two switch cases

Two cases added to `dispatchAppFrame`'s existing control switch, beside `TypeModalAnswer` /
`TypeModalCancel`, each calling its handler and returning. Nothing else in that function changes.
Placement inside *this* switch is not cosmetic: `cmd/pyry/relay_guard_test.go` AST-reads this exact
function by name, and a handler dispatched from anywhere else would fail the guard's coverage
assertion.

### 4. Prose corrections (no design surface)

- `cmd/pyry/relay_guard_test.go`: `TypeQuestionAnswer` / `TypeQuestionRefused` move from
  `excludedTypes` (`"pending handler (#1984)"`) to `inboundTypes` (`"switch-intercepted"`), beside
  the modal pair. Its stale excluded-block comment goes with them. Enforced deterministically —
  leaving them behind trips Assertion #2 and reddens `make check`.
- `internal/protocol/codes.go`, `internal/protocol/questions.go`: comment-only. Sweep every clause
  each slice falsifies, not just the ones the ticket enumerates — the guard-classification
  paragraph, the "DECLARING THESE CONSTANTS CHANGES NO RUNTIME PATH" paragraph, the two trailing
  `(no handler yet — #1984)` markers, `questions.go`'s file-header "NOTHING CONSTRUCTS OR DECODES
  THEM" and its `#1984` forward reference, `QuestionAnswerPayload`'s "NOTHING INTERCEPTS IT YET",
  and `QuestionRefusedPayload`'s "intercepted by nobody yet".
- `docs/protocol-mobile.md`: six live sites, found by wording (*vocabulary only*, *nothing
  intercepts*, *nothing decodes*, *unknown-type reply*, *falls through to the ordinary*) — the
  `question_answer` and `question_refused` rows in § Application message types, § Question's intro,
  its inbound-capability paragraph, and the two `####` bodies. Dated changelog entries are left
  alone and one new dated entry is added, per that file's own convention.

## Concurrency model

No new goroutine, no new channel, no new lock. Both handlers run on the manager's single `Run`
dispatch goroutine — `dispatchAppFrame` is called from `handleNoiseMsg` on `Run`, and both handlers
return before the frame is released, so `s.device` and `s.connID` are read lock-free under the
package's single-owner invariant.

The seam call happens *on* `Run`. That inherits the same obligation `ModalResolver` carries: an
implementer must return in bounded time or it stalls the manager. #1985 owns honouring it; recorded
here so the constraint is not discovered later.

Shutdown: nothing to sequence. No goroutine is spawned, no timer armed, no state retained across
frames.

## Error handling

| Condition | Behaviour |
|---|---|
| Nil `QuestionResolver` | Frame consumed, inert. `Debug` record. No decode, no seam call, no reply. |
| Payload fails to decode | Frame consumed, **rejected**. `Warn` record carrying no `err` field and no payload byte. Seam not called. Nothing echoed to the client. |
| Decodes cleanly, seam reports consumed | `Info` record. No reply, no broadcast. |
| Decodes cleanly, seam reports not-consumed | `Debug` record. No reply, no broadcast. Not an error — an unknown or already-resolved batch is success of a valid request. |
| Envelope itself malformed JSON | Unreached: `dispatchAppFrame`'s probe fails first and falls through to `dispatch.Route`'s `protocol.malformed` reply, unchanged. |

**The decode error is never wrapped, logged, or replied.** `encoding/json` quotes offending input
into its error string, those bytes are remote-authored, and nothing on this path strips terminal
escape sequences — `QuestionAnswerPayload`'s doc block states the rule and this is the handler it was
written for. Log fields are `event`, `conn_id`, and — on the paths where it is trustworthy —
`question_batch_id`, which that same doc block marks explicitly safe to log so this handler neither
invents a redaction rule nor assumes one exists. `answer_token` is likewise safe but is not logged:
nothing correlates on it daemon-side today. On the reject path there is no trustworthy batch id (the
decode is precisely what failed), so that record carries `event` + `conn_id` only.

## Testing strategy

New file `internal/relay/v2session_question_test.go`, built on the modal fixtures
(`openModalConn`, `sealAppFrameConn`, `noiseMsgsForConn`, `waitForLogContains`, `bufferLogger`), with
a `fakeQuestionResolver` recording every call under a mutex — `fakeModalResolver`'s shape, since the
`Run` goroutine writes and the test goroutine reads. Every case drives a real Noise handshake and a
real sealed frame, so the interception is proven end-to-end rather than by calling the handler
directly. Scenarios, table-driven over the two frame types where they share an arm:

- **Answer reaches the seam.** A well-formed `question_answer` arrives; the seam sees the batch id,
  the answer token, the full nested entries, and the conn's device. Pins that `answers` survives
  transit intact — the nested array is what the tolerant decode would have silently emptied.
- **Refusal reaches the seam**, with its two ids and the device; `ResolveAnswer` is not called.
- **Consumed even with nothing wired** (both types, nil resolver). Asserts the inert record and
  **zero outbound `noise_msg`** for the conn — the assertion that no unknown-type reply was sent, and
  the direct pin of the behaviour change this slice delivers. The session stays `V2StateOpen`.
- **Decode failure is rejected, not tolerated.** A `question_answer` whose `answers` array is
  well-formed JSON of the wrong shape, so the decoder populates `question_batch_id` *first* and then
  fails — the exact partial-population hazard. Asserts the seam is called **zero** times, zero
  outbound frames, and the reject record. A payload that merely produced an empty batch id would let
  a tolerant `_ = json.Unmarshal` pass this test green, so the fixture must carry a **non-empty**
  batch id ahead of the bad array, and the assertion must be on the call count rather than on the
  received value.
- **No log line carries an answer value or a raw payload byte.** Scan the captured buffer across all
  three paths — inert, rejected, handed off — for the distinctive answer strings and for the raw
  payload fragment. The fixture values must be strings that cannot occur incidentally in a log line.
  The same arm asserts that a batch id carrying a terminal escape sequence emits **no raw `\x1b`
  byte** into the buffer (§ Security review, SHOULD FIX): `slog`'s `TextHandler` is expected to quote
  and escape it, and that is a property to check rather than assume, since this is the first inbound
  handler to log a field an attacker fully controls the bytes of.
- **Unknown batch is not this handler's to judge.** A cleanly-decoding payload naming a batch the
  fake does not know still reaches the seam (which reports not-consumed); no reply, no broadcast.
- **No other frame's handling changes.** An unrelated unknown type still draws its unknown-type
  reply, so the interception is proven narrow rather than a blanket `default` arm.

`go test -race` on `./internal/relay/...` and `./cmd/pyry/...` (the latter for the guard move) plus
`go vet ./...` and `go build ./cmd/pyry` is the touched-scope gate. The full-module race suite and
`make check` are the verifier's.

## Open questions

1. **Does the seam return a `bool` at all, or nothing?** Resolved at design time in favour of the
   `bool`: the ticket asks the seam to report enough that its implementer stays the sole arbiter, and
   `handleDequeueMessage` is the in-tree precedent for a boolean consumed only by logging. Recorded
   because the alternative — a pure fire-and-forget seam — is defensible and a reader will wonder.
   If Phase B finds the discriminant buys nothing, dropping it is a one-line change and belongs in a
   `## Revisions` entry.
2. **Does `handleQuestionAnswer` need `ctx`?** Expected no, on `handleDequeueMessage`'s reasoning.
   Confirm during implementation that no path added for logging or the seam call wants one.
3. **Is `Warn` the right level for the decode reject, given a hostile paired client can trigger it at
   will?** Provisionally yes: `dispatch.Route` already `Warn`s per malformed inner frame, so this
   adds no new flood vector to an already-authenticated peer. Revisit only if the security pass
   below disagrees. (It did not — see § Security review, [Network & I/O].)

## Security review

**Verdict:** PASS (first pass returned FAIL on one MUST FIX; the plan was revised inline and the
checklist re-walked before this section was written or the plan committed.)

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in § Design before commit.** The boundary is explicit and
  single-function per frame type (`json.Unmarshal` inside `handleQuestionAnswer` /
  `handleQuestionRefusal`), and nothing downstream re-parses. But the seam is where untrusted data
  leaves this package, and Go's type system cannot signal "remote-authored": an implementer reading
  `ResolveAnswer(p, dev) bool` alone would index the parked batch by `QuestionIndex`, which
  `QuestionAnswerEntry`'s own doc block warns **panics** on a hostile index — remotely triggerable
  by any paired client. The plan now requires the interface doc block to restate the three
  obligations (no range check, duplicate/missing indices, unbounded `Answers`) plus the never-log
  rule at the crossing.
- **[Trust boundaries] No further findings.** A cleanly-decoding payload naming an unknown batch is
  passed through deliberately, not through oversight: judging it here would install a second arbiter
  beside the seam's implementer, which AC #3 forbids. `"payload": null` and an empty object decode
  to a zero-value payload whose empty batch id is an *unknown* batch, not a decode failure — the
  hazard `QuestionAnswerPayload`'s doc names is the **partial** decode (batch id read, nested array
  failed), and rejecting on `err != nil` kills exactly that.
- **[Tokens] No findings.** Nothing is minted here. `question_batch_id` arrives as an echo of the
  nonce #1975 mints with `crypto/rand`; this handler treats it as an opaque key and mints, stores,
  rotates and expires nothing. `answer_token` is a client-minted idempotency key that is explicitly
  **not** the authorization — the dedup is the one-shot consume of the batch id, and that is #1985's.
  Both are marked safe to log by `QuestionAnswerPayload`'s doc block, written for this handler, so
  logging `question_batch_id` invents no rule and skipping `answer_token` redacts nothing.
- **[Tokens] SHOULD FIX — the logged batch id is attacker-controlled bytes, not the daemon's own
  nonce.** This is the first inbound handler to log a field whose bytes an attacker fully controls,
  and the log reaches the debug bundle. `slog`'s `TextHandler` is expected to quote and escape
  control characters, so a terminal escape sequence should not reach a bundle reader's terminal raw —
  but that is an assumption about a stdlib detail, and § Testing strategy now pins it with an
  assertion rather than asserting it here. **Length is deliberately left unbounded**: an unbounded
  inbound string in a log field is exactly `handleDequeueMessage`'s existing posture for
  `conversation_id`, the AEAD envelope cap (65519 B) already bounds one frame, and a first-of-its-kind
  bound on an inbound log field is a repo-wide decision, not this slice's to make unilaterally.
- **[File operations] Not applicable, structurally.** The handlers open, create, stat and write
  nothing; no field is joined into a path. The only outputs are one seam call and one log record, so
  there is no path-traversal, TOCTOU, permission, symlink or atomic-write surface to audit.
- **[Subprocess] OUT OF SCOPE — #1985.** Nothing here reaches `exec.Command`. The `values` do
  eventually reach claude's context when #1985 feeds them back as the blocked tool call's result;
  that is message content at `send_message`'s trust tier, which
  `docs/protocol-mobile.md` § `question_answer` already states grants nothing new, and it is #1985's
  path, not this one's.
- **[Cryptographic primitives] No findings, and one load-bearing negative.** No primitive is chosen,
  no key or nonce is derived or reused, and no constant-time comparison is owed (nothing here is
  compared against a secret). The frame arrives already AEAD-decrypted under `s.recv` on the `Run`
  goroutine. The handler touches **neither `s.send` nor `s.recv`** — and it cannot drift into doing
  so, because it emits no reply at all. That absence is what keeps the single-owner-cipher invariant
  intact without this slice having to reason about it.
- **[Network & I/O] No findings.** No socket is read and no server is configured, so no cap, header
  check, timeout or TLS policy is this slice's. The one live question is per-frame work on the `Run`
  goroutine: this is the family's first control payload with a nested array, so its decode costs more
  than the flat-string siblings'. It stays far below the existing worst case on the same goroutine —
  `request_snapshot` renders a screen and `request_debug_bundle` assembles an archive (which is why
  #911 gated *that* one per-conn) — and one frame's input is bounded by the envelope cap before the
  decode begins. On the reject log level: `Warn` matches `dispatch.Route`'s existing per-malformed-
  frame `Warn` for the same authenticated peer, so it opens no flood vector that peer does not
  already have.
- **[Errors, logs, telemetry] No findings.** The decode error is never wrapped, logged or replied —
  `encoding/json` quotes offending input into its error string, and those bytes are remote-authored.
  MUST-log is `event` + `conn_id` on every path. The reject record carries **no** batch id, and that
  is the deliberate half: a partially-populated id comes from a frame that was refused, so logging it
  would attribute rejected bytes to a batch. No answer value, no entry count and no payload byte
  appears on any of the three paths, and § Testing strategy scans the buffer to prove it rather than
  asserting it.
- **[Concurrency] No findings.** No goroutine, channel, lock or timer is added, so there is no lock
  order to define, no goroutine lifecycle to bound and no partial state to recover at shutdown. Both
  handlers run to completion on the single `Run` dispatch goroutine, so `s.device` / `s.connID` are
  read lock-free under the package's single-owner invariant, and nothing is mutated, so there is no
  check-then-mutate window. Handing `*devices.Device` across the seam is `ModalResolver`'s existing
  contract, unchanged. The one inherited obligation — a seam implementation blocking `Run` stalls the
  manager — is `ModalResolver`'s too and is recorded in § Concurrency model for #1985.
- **[Threat model] No findings; one deferral, named.** `docs/protocol-mobile.md` § Security model's
  threat 1 (prompt injection, subprocess → render surface) does **not** land here: no field on either
  frame is claude-authored, which the positional design is what buys. The two obligations that
  document places on #1984 by name are both met — a decode failure is a rejected frame, and its error
  never embeds the raw payload.
- **[Threat model] OUT OF SCOPE — #1986, with an ordering obligation.** This handler applies neither
  the `interactive` capability gate nor the per-device answer gate (#702), by instruction and
  matching `handleModalAnswer`, which applies neither for the same reason: the decision belongs in
  one place, the resolver. That leaves the path currently ungated, and what makes it fail-safe is
  structural rather than argued — **the seam is nil at every construction site in this slice**, so
  no answer reaches any actuator, exactly as `handleModalAnswer` shipped in #727 against a
  deferred-no-op `ResolveAnswer` before #717 filled it. The obligation that follows is that nothing
  may be wired to the seam before #1986 lands; § Design now puts that sentence in the seam's own doc
  block, where whoever wires it will read it.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02

## Revisions

### 2026-09-02 — implementation

The three § Open questions all resolved as expected and changed no design: the seam keeps its
`bool` (implemented exactly as `handleDequeueMessage` uses `QueueRemover.Remove` — two content-free
log records, no broadcast), neither handler needs a `ctx`, and the reject record stays at `Warn`,
which the security pass agreed with.

**One thing the plan did not anticipate, and it changed the test set.** § Design already fixed the
behaviour for a `"payload":null` frame — it decodes cleanly into a zero value, so it is an unknown
batch for the seam to judge and not a rejected frame. What implementation found is that this shape
is *unreachable as a decode failure through the test harness at all*: `json.Marshal` validates a
`json.RawMessage`, so an envelope carrying malformed payload bytes cannot be constructed, and
`protocol.Envelope.Payload` has no `omitempty`, so an omitted payload marshals to `null` rather than
to nothing. Every decode failure reachable over the wire is therefore a **type mismatch inside valid
JSON** — which is the partial-population hazard, the case that matters. Consequences:

- The planned "absent payload" reject case was dropped; it does not exist.
- `TestV2Session_QuestionControl_NullPayload_NotJudgedHere` was added to pin the pass-through
  positively, since it is the sharpest edge of the reject rule and nothing else covers it.
- The distinction — the rule is about a decode *error*, not about an empty result — was written into
  `QuestionAnswerPayload`'s doc block and into `docs/protocol-mobile.md` § `question_answer`. That is
  an addition to those files rather than one of the falsified claims § Design listed, and it is there
  because the next reader of that contract would otherwise re-derive it.

### 2026-09-02 — rework (verifier review of PR #1989)

No design change. Three SHOULD FIX findings, all one defect: § Design step 4's sweep covered every
claim the ticket and this plan *enumerated*, but not the claims the diff itself falsified in files it
was already opening. The step's own words ("sweep every clause each slice falsifies, not just the
ones the ticket enumerates") already asked for the wider reading; what was missing is that a claim
becomes false by virtue of the diff, not only by virtue of being listed. Corrected:

- `internal/relay/v2session_seams.go` file header — "six seam interfaces" and their list; this slice
  adds `QuestionResolver` as the seventh. (The stale `~260-line V2SessionConfig` figure in the same
  header was already wrong on `main` and is left alone as out of scope.)
- `internal/relay/v2session.go` → `dispatchAppFrame` doc block — its parenthetical enumerated the
  eight control families handled inline on `Run`; this slice adds a ninth to that same switch.
- `internal/protocol/codes.go` → the `TypeQuestionShown` / `TypeQuestionDismissed` block's "no
  inbound request verb" paragraph — it still described the interim state (`excludedTypes` under a
  pending-handler label, `TypeAttachmentChunk`'s precedent, "#1984 supplies the cases" in the future
  tense) and so contradicted the paragraph below it that this slice had already past-tensed.
