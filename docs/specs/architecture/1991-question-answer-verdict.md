# #1991 — resolve an outstanding question batch to claude's answer verdict

## Files read

- `cmd/pyry/modal_resolve_v2.go` → `RefuseQuestion`, `surfaceQuestion`,
  `retireQuestion`, `ResolveStream`, `broadcast`, `streamApprovalBridge` — the
  file this slice edits. `RefuseQuestion` is the template rather than a
  neighbour: same primitive, same struct, same file, and it already landed
  `sourceQuestionRemote` and the dismissal-vocabulary constant block this
  slice's third sentinel joins.
- `internal/questionbridge/registry.go` → `Registry.Lookup`, `Registry.Resolve`
  — `Lookup` is the non-retiring read (cloned) this slice validates against;
  `Resolve` is the one-shot whose read-and-delete is a single critical section.
- `internal/questionbridge/questionbridge.go` → `Parse`, `toolInput`,
  `inputQuestion` — the trust boundary `Surface` reaches `surfaceQuestion`
  through, and the proof that the `questions` extraction below cannot fail.
  `inputQuestion`'s `multiSelect` tag is the key `protocol.Question` spells
  `multi_select`, which is why the passthrough is claude's bytes.
- `internal/permbridge/permbridge.go` → `Registry.Lookup`, `Registry.Resolve`,
  `Allow`, `Verdict.UpdatedInput`, `Request.Input` — `Lookup` is the comma-ok
  read of claude's original bytes; `Allow` is the verdict carrying them back.
- `internal/protocol/questions.go` → `QuestionAnswerEntry`, `Question`,
  `QuestionShownPayload`, `QuestionDismissedPayload` — the untrusted inbound
  entry (index carried, never range-checked; values client-authored and never
  compared to labels) and the outbound dismissal's daemon-asserted fields.
- `internal/relay/v2session_seams.go` → `QuestionResolver.ResolveAnswer` — the
  seam #1986 implements over this primitive: the bool is a diagnostic and never
  a broadcast trigger, and both seam methods run on the manager's `Run`
  goroutine, which is why the fan-out is detached.
- `docs/specs/architecture/1990-question-refusal.md` — the ordering argument and
  the detached-fan-out argument, both of which carry over unchanged.
- `docs/knowledge/features/questionbridge-package.md` — the registry's
  single-arbiter posture: the answer path must be the sole dismissal
  broadcaster for a batch it consumes.
- `docs/protocol-mobile.md` § `question_dismissed`, § `question_answer` — the
  published `outcome`/`source` contract this slice extends, the three #1990
  forward references it discharges, and the eight live `#1985` cites it
  re-points.

## Context

Claude's `AskUserQuestion` call parks on a human through the same mcp-approve
bridge a permission prompt does. `surfaceQuestion` records the batch and
correlates its nonce to claude's `tool_use_id`; `retireQuestion` is the
no-answer backstop, and #1990 added the refusal. This slice adds the third and
last verdict: given a batch id and the operator's picks, resolve the parked
approval as an **allow** whose updated input carries claude's own `questions`
array and an `answers` object.

It wires nothing. `relay.QuestionResolver` stays unimplemented and
`V2SessionConfig.QuestionResolver` stays nil at every construction site, because
the relay handler applies no authorization and the per-device gate is #1986's.

No ADR is warranted: this completes an established arbiter's vocabulary rather
than deciding anything new about it.

**Size, re-counted against this plan:** 1 production source file, 1 new method,
0 new exported types, 0 consumer call sites, 5 acceptance criteria, 9 reject
branches (unknown correlation; parked input unretrievable; batch not
outstanding; entry count ≠ question count; index out of range; duplicate index;
no values; >1 value for a single-select; one-shot lost). Every boundary holds
except total written work, which the refiner stated at ~750 and this plan does
not shrink. **The ticket is not split**: it is a grandchild (#1907 → #1985 →
#1991), so the split-depth gate bars a third level, and `needs-human:sizing` is
already on the issue as the marker for that judgement. Independently, the
deliverable does not decompose — validation, assembly, consume, verdict and
dismissal are one arbiter by AC-3 and AC-4, so any child would be a slice
nothing outside this family calls.

## Design

One production file, `cmd/pyry/modal_resolve_v2.go`.

**Two new compile-time constants**, joining #1990's dismissal-vocabulary block:

- `outcomeQuestionAnswered` — the third `outcome` sentinel, distinct from
  `outcomeQuestionUnanswered` and `outcomeQuestionRefused`, and carrying no
  claude-authored string. `sourceQuestionRemote` is reused as-is; #1990's plan
  anticipated exactly this and took no dependency on it.
- `answersInputKey` / the updated-input struct's tags — claude's own key
  spellings, stated once so the re-marshal hazard below has a single home.

**One new method:**

```go
func (b *streamApprovalBridge) AnswerQuestion(batchID string, answers []protocol.QuestionAnswerEntry) (consumed bool)
```

`AnswerQuestion` and not `ResolveAnswer`: that name is already taken on this
struct by the modal path, exactly as `RefuseQuestion` had to sidestep
`ResolveCancel`. Exported like `ResolveStream` and `RefuseQuestion`, for the
same reason — it is the seam a later composition-root type calls.

Its steps, in the order the ticket makes load-bearing:

1. **Read `byQuestion[batchID]` under `mu` — no delete.** A miss returns false:
   an unknown batch, or one whose `retireQuestion` already ran.
   `retireQuestion` stays the sole, unconditional deleter, as `ResolveStream`
   sets the resolve-without-deleting precedent. This read is also why a nil
   `b.questions` needs no guard, `RefuseQuestion`'s argument unchanged.
2. **`b.perm.Lookup(toolUseID)` — comma-ok, and its miss is a real branch.** A
   miss means permbridge already resolved this approval on its own timer, so
   there are no input bytes to pass through. Return having done **nothing**:
   the control server's deferred `retireQuestion` is about to run and owes every
   client its `unanswered` dismissal. This is the one place #1990's shape does
   not carry over — a `Deny` needs no input, so the refusal resolves
   unconditionally.
3. **`b.questions.Lookup(batchID)` — the non-retiring read** the validation and
   the `answers` keys are both built from. Validating against `Lookup` rather
   than `Resolve` is what leaves a rejected answer's batch answerable for a
   corrected one (AC-3), and it is the same look-up-then-consume ordering
   #1986's per-device gate needs.
4. **Validate, then assemble** (both below). Any rejection returns false with
   nothing consumed, nothing resolved and nothing broadcast.
5. **`b.questions.Resolve(batchID)` — the one-shot consume.** A miss returns
   false: the backstop, or a refusal, or an earlier answer already consumed it.
   Consuming *before* the verdict is what keeps the deferred `retireQuestion` —
   which runs the instant `permbridge.Pending.Await` returns — from winning the
   one-shot and broadcasting `unanswered` for a batch the operator answered.
   The modal path keeps the same order: `ResolveAnswer` consumes the
   `modalbridge` entry before calling `ResolveStream`.
6. **`b.perm.Resolve(toolUseID, permbridge.Allow(updated))`.** A miss is the
   ordinary no-op and needs no branch — the batch is consumed either way, and
   the client must still learn the panel is dead.
7. **Broadcast exactly one `question_dismissed`** carrying `batchID`,
   `outcomeQuestionAnswered` and `sourceQuestionRemote`, **on its own
   goroutine** — `RefuseQuestion`'s deviation, for its reason verbatim:
   `broadcast` calls `ActiveConns`, which funnels onto the relay manager's `Run`
   select, and this primitive's only intended caller is #1986's resolver, which
   relay documents as running on `Run`. Steps 1-6 and the return value stay
   synchronous; only the fan-out is detached.
8. Return true.

### Validation — contract, not implementation

Every rule below rejects the **whole** answer; there is no partial resolution.
No value is ever compared against the offered labels, because claude's contract
permits free text anywhere (`QuestionAnswerEntry`'s doc block).

- **Coverage is exactly-once, and the entry count is the bound.**
  `len(answers) != len(batch.Questions)` rejects first, which is also what keeps
  per-frame work bounded by the parked batch (1-4 questions) rather than by an
  arbitrarily long payload. With the counts equal, "every index in range and
  none repeated" gives exactly-once by pigeonhole, so a `seen` bitmap over the
  batch's questions is the whole check.
- **Index range.** `0 <= QuestionIndex < len(batch.Questions)`, checked before
  any subscript. `protocol` carries the index and never range-checks it, and the
  consequence of skipping this is a panic rather than a wrong answer.
- **A question with no values rejects**; an entry selects nothing otherwise.
- **A single-select question with more than one value rejects.** Read from the
  parked question's `MultiSelect`, never from how many values arrived.

### The updated input — where each half comes from

Both halves come from copies the daemon itself holds; nothing the client echoed
back reaches claude.

- **`questions` is claude's own bytes.** `permbridge.Request.Input` is the
  parked original; the `questions` **value** is extracted from it as a
  `json.RawMessage` and spliced in. That extraction cannot fail — `Surface`
  reaches `surfaceQuestion` only through a `questionbridge.Parse` that already
  succeeded on these exact bytes, and `byQuestion` correlates that same
  `req.ToolUseID` — but a failed or absent extraction is folded into step 2's
  "parked input no longer retrievable" branch rather than ignored, so the
  structurally-unreachable case still cannot reach claude with a missing key.
  **Re-marshalling the parked batch is the trap this avoids**:
  `protocol.Question.MultiSelect` is tagged `multi_select` where claude's tool
  input uses `multiSelect`, so that route hands claude a key it does not read,
  silently, since the call is allowed either way. `Parse` also drops keys it
  does not model, which is the second reason.
- **`answers` is keyed by the parked batch's question text**, which agrees with
  those bytes because `Parse` rejects rather than truncates. The value is the
  client's `Values`: a bare string for a single-select question, the array for a
  multiSelect one, decided from the parked `MultiSelect`. Free text is carried
  verbatim — never the word "Other", never checked against a label.
- **Two questions with identical text collapse to one `answers` key, and that
  is not a reject branch.** Keying by text is claude's own contract, so the
  daemon cannot do better than the shape allows; rejecting would strand the
  operator with a batch they can never answer, which is strictly worse than the
  contract's own ambiguity. Nothing about it is client-controlled.

`json.Marshal` over a `{questions json.RawMessage; answers map[string]any}`
carrier re-emits the raw value compacted — whitespace between tokens only, with
every string byte, key spelling and array order preserved. That is the
"byte-for-byte" the AC means, as against a semantic re-encode through
`protocol.Question`.

## Concurrency model

- Steps 1-6 run on the caller's goroutine. `b.mu` is held only around the
  `byQuestion` map read (leaf lock, unchanged) and is not held across
  `perm.Lookup`, `questions.Lookup`, `questions.Resolve`, `perm.Resolve` or the
  broadcast, so no new lock-ordering edge appears.
- One goroutine per *consumed* answer, running `broadcast` and nothing else. It
  exits when the fan-out finishes, or at the latest when the daemon ctx is
  cancelled (`ActiveConns` returns nil on a done ctx, `broadcast`'s Push loop
  returns early on teardown). At most one exists per consumed answer, and an
  answer requires a batch parked on a human, so a client cannot drive the count.
- The check-then-act between step 3's `Lookup` and step 5's `Resolve` is
  deliberately **not** atomic, and is safe in both directions because the
  one-shot is the single arbiter: a concurrent `retireQuestion` or
  `RefuseQuestion` either wins the one-shot (this call returns false having
  resolved nothing) or loses it (it broadcasts nothing). Validating against a
  batch a concurrent caller then consumes costs only wasted work.
- Interleavings settled by the same one-shot: answer vs backstop, answer vs
  refusal, answer vs a second answer, and answer vs `ResolveStream` (a batch id
  is absent from `byModal` by construction).

## Error handling

- Nine reject branches, all identical in effect: `false`, no consume, no
  verdict, no broadcast, batch left outstanding for a corrected answer or for
  the no-answer backstop.
- `perm.Resolve` returning false after the consume is ignored: the batch is
  consumed, the client must still learn the panel is dead.
- `json.Marshal` of the carrier cannot fail on a `map[string]any` of strings and
  `[]string` plus a validated `RawMessage`; its error is nevertheless handled as
  a reject-before-consume rather than dropped, keeping the "nothing half-lands"
  property total.
- `broadcast` keeps its existing marshal-failure and per-conn Push-failure
  tolerance; a torn-down conn re-syncs through the connect-time reconcile.
- **No log line is emitted by this path at all** (AC-5). The relay handler
  already logs the outcome with the batch id — the one field `protocol` marks
  safe, alongside the answer token — so a record here would duplicate it while
  its only new material is question text, an answer value or the batch body,
  none of which may ever be logged. That makes AC-5 structural rather than a
  redaction rule.

## Testing strategy

`cmd/pyry/stream_approval_test.go`, over the existing `newQuestionFixture`,
`questionInput`, `waitPush` and `isolate` harness #1990 built. `questionInput`
already parks a two-question batch whose first question is multiSelect and whose
second is not, which is exactly the pair both emitted shapes need.

- **Answer allows with the assembled input.** `Pending.Await` returns
  `{behavior: allow}`; the updated input decodes to exactly `questions` +
  `answers`; `answers` maps each question's text to a bare string for the
  single-select question and an array for the multiSelect one; a client-supplied
  free-text value that matches no offered label arrives verbatim.
- **AC-2's mutant killer, its own test:** the updated input's `questions` value
  is byte-equal to the compacted `questions` value of the parked
  `permbridge.Request.Input`, **and** carries the key `multiSelect` and not
  `multi_select`. A re-marshal of the parked batch passes a loose shape
  assertion and reddens here.
- **Reject table**, one row per branch — index out of range (negative and
  over-large), duplicate index, missing index, entry count over the batch size,
  empty values, two values for the single-select question. Each row asserts
  `consumed` false, no push, the batch **still outstanding** (`qreg.Lookup`
  hits), and the approval still parked (`perm.Lookup` hits) so no verdict
  escaped.
- **Unretrievable parked input** — `perm` resolved on its own timer before the
  answer arrives: `consumed` false, no push, and the batch still outstanding so
  the deferred `retireQuestion` can still broadcast `unanswered`. This is the
  branch that would silence the backstop if the consume came first.
- **Single arbiter, both directions:** answer-then-retire broadcasts nothing
  more; a batch the backstop already consumed answers nothing; a refused batch
  answers nothing and an answered batch refuses nothing.
- **Dismissal shape:** exactly one `question_dismissed` with the batch's own id,
  `sourceQuestionRemote`, and an `outcome` asserted distinct from both
  `outcomeQuestionUnanswered` and `outcomeQuestionRefused` — and asserted not to
  equal any offered label from the parked batch, which is the AC-4 trust-tier
  rule stated as a test rather than as a comment.
- **Leak probe.** Four claude-authored sentinels through `questionInput` plus a
  client-authored answer sentinel, pushes forced to fail so the error arm runs,
  and the log buffer asserted **empty** — the strongest form, available because
  this path emits no record of its own.

Gate: `go test -race ./cmd/pyry/... ./internal/questionbridge/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Docs

`docs/protocol-mobile.md` only, all above the `## Changelog` boundary:

- **Add** the third `outcome` sentinel to § `question_dismissed`, beside
  `unanswered` and `refused`. A new row; #1990's is not rewritten.
- **Discharge** the three forward references #1990 left pointing here: the
  section intro, the `outcome` row, and the `remote` carry-over row.
- **Re-point the eight live `#1985` cites**: the `question_dismissed` and
  `question_answer` rows in § Application message types, § Question's intro,
  § `question_answer`'s intro, its three Contract-bounds rows and its SECURITY
  paragraph. The three Contract-bounds rows and the SECURITY paragraph's
  `values`-reach-claude sentence are **discharged** rather than merely
  re-pointed.
- § `question_answer`'s *"resolved by nothing yet"* claim stays **true** and is
  not touched beyond its cite: the resolver seam is nil at every construction
  site until #1986.
- One changelog entry. No `#### ` heading is added or removed, so the live
  counts stay checkable.

## Open questions

- Does the emitted `answers` value for a multiSelect question with exactly one
  value stay an array? **Yes** — decided from the parked `multi_select`, never
  from arrival count, so the shape matches claude's own examples per question
  kind. Resolved in the design; no revision expected.
- Does #1986 unpack `QuestionAnswerPayload` before calling this, or does this
  take the payload? **It unpacks.** This primitive takes `(batchID, answers)` so
  it never holds an `answer_token` it has no business reading —
  `questionbridge.Parse` taking the pair rather than the `Request` is the same
  argument. The token is idempotency, not authorization.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings on the boundary's *location*: it is a single
  explicit one, the validation block inside `AnswerQuestion`, and everything
  downstream of it holds either daemon-held bytes (`permbridge.Request.Input`,
  the parked batch) or client `Values` that are *deliberately* untrusted and
  documented as sitting at `send_message`'s tier. The batch id crosses in as a
  key only. Nothing the client echoed back is used to build the verdict — the
  `questions` passthrough and the `answers` keys both come from daemon copies,
  which is AC-2 stated as a boundary rather than as a preference.
- [Trust boundaries] MUST FIX **(addressed in this plan before commit)** — an
  earlier sketch validated against `Resolve`'s returned batch, which both
  consumed the one-shot before the answer was known good *and* left a rejected
  answer's batch unanswerable. Step 3 now reads through `Lookup`, and step 5
  consumes only after validation and assembly both pass. Re-walked after the
  revision: no MUST FIX remains.
- [Trust boundaries] SHOULD FIX — as with `RefuseQuestion`, this primitive
  treats *possession of the batch id* as sufficient to answer, which is correct
  only while #1986's per-device gate sits above it. The doc block must say so in
  the imperative so a later wiring site cannot read the primitive as
  self-guarding. Land the sentence in Phase B.
- [Tokens, secrets, credentials] No findings, and one is avoided by shape: the
  `answer_token` never reaches this primitive at all (Open questions), so there
  is no token to store, compare or log here. The real dedup is the one-shot
  consume of the batch id — a `crypto/rand` nonce minted by
  `questionbridge.Registry.Record`, not by this path.
- [File operations], [Subprocess execution], [Cryptographic primitives] Not
  applicable by construction: this path opens no file, execs nothing, and mints
  no randomness. The one nonce involved was minted upstream.
- [Network & I/O] No findings, and the input-size question is answered rather
  than deferred. The inbound `answers` array is unbounded on the wire by design
  (`protocol` enforces no bound), and the bound this slice owes is the *first*
  validation rule: `len(answers) != len(batch.Questions)` rejects before any
  walk, so per-frame work is bounded by the parked batch's 1-4 questions and an
  arbitrarily long array is O(1) to reject. Individual value length is
  deliberately uncapped — a cap here would reject a legal free-text answer, and
  the transport's AEAD frame cap already bounds total bytes. Outbound, the
  dismissal's three fields are daemon-asserted constants plus the daemon's own
  nonce, so its length is neither client- nor subprocess-influenced.
- [Error messages, logs, telemetry] No findings, by construction rather than by
  redaction: this path emits no log record of its own, so there is no field for
  question text or an answer value to leak into. The only records reachable are
  `broadcast`'s existing content-free marshal/push-error lines (event, conn_id,
  env_id). The leak probe pins it against a later edit, asserting the buffer
  **empty** rather than merely sentinel-free.
- [Concurrency] No findings on locking: `b.mu` is taken only around the
  `byQuestion` read and released before every registry call and the broadcast,
  so it stays a leaf and adds no ordering edge. The check-then-act between
  `Lookup` and `Resolve` is non-atomic by design and safe in both directions —
  the one-shot is the single arbiter, so a concurrent backstop or refusal either
  wins it (this call is inert) or loses it (it broadcasts nothing). The
  exploitable ordering is the reverse one, consume-then-validate, and the design
  forbids it.
- [Concurrency] SHOULD FIX — the detached fan-out goroutine's worst-case exit
  condition is daemon-ctx cancellation (relay `Run` gone while the ctx is still
  live makes `ActiveConns` block until then). Bounded by the number of consumed
  answers, each of which required a batch parked on a human, so a client cannot
  drive it unboundedly. State the bound in the doc block in Phase B rather than
  leaving a reader to derive it.
- [Threat model alignment] No findings, and this is the frame where the rule
  bites hardest. § Security model's threat 1 (claude's own words as injection)
  is what § `question_dismissed`'s `outcome`-never-carries-a-label rule defends,
  and **the natural implementation of an answer path is exactly what breaks
  it**: the obvious dismissal reports which option was chosen, and an option is
  identified by a claude-authored `label`, which would move a frame published as
  daemon-asserted into the batch's trust tier. The design uses a sentinel and
  the test asserts the outcome equals no offered label. The opposite direction —
  client text reaching claude — is in scope here and grants nothing new: an
  answer sits at `send_message`'s tier, which `QuestionAnswerEntry`'s doc block
  already records.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
