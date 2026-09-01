# #1990 — refuse an outstanding question batch to a deny that tells claude to wait

## Files read

- `cmd/pyry/modal_resolve_v2.go` → `streamApprovalBridge`, `surfaceQuestion`,
  `retireQuestion`, `ResolveStream`, `broadcast`, `parkedToolUseIDs`,
  `ApprovalAnswerable` — the file this slice edits: the correlation map, the
  no-answer arbiter whose contract the refusal must sit inside, the
  resolve-without-deleting precedent, and (in `ApprovalAnswerable`'s doc block)
  the file's own record that `ActiveConns` deadlocks when called from the relay
  `Run` goroutine.
- `internal/questionbridge/registry.go` → `Registry.Resolve`, `Registry.Lookup` —
  the one-shot whose read-and-delete is a single critical section; the reason
  "exactly one broadcaster" is structural rather than agreed between slices.
- `internal/permbridge/permbridge.go` → `Registry.Resolve`, `Deny`, `Verdict`,
  `Pending.Await` — how a parked approval is handed a verdict, and the
  `reasonTimeout` precedent for a fixed daemon-owned deny message.
- `internal/relay/v2session_seams.go` → `QuestionResolver` — the seam #1986
  implements over this primitive: the bool contract ("a diagnostic, never a
  broadcast trigger"), the ban on the relay emitting `question_dismissed`, and
  the statement that both seam methods run on the manager's single `Run`
  dispatch goroutine.
- `internal/relay/v2session.go` → `ActiveConns`, `Run`, `Push` — `ActiveConns`
  funnels a request onto `Run`'s select, so a call from `Run` blocks until the
  daemon ctx is cancelled; `Push` is lock-based and safe from any goroutine.
- `internal/relay/v2session_question.go` → `handleQuestionRefusal` — what
  already logs the batch id on the relay leg, which is why this primitive logs
  nothing of its own.
- `docs/knowledge/features/questionbridge-package.md` — the registry's
  no-expiry / single-arbiter posture; the note that #1985's answer path must be
  the sole dismissal broadcaster for a batch it consumes.
- `docs/knowledge/features/v2-session-manager-state-machine-inbound-modal-control-deny-on-timeout.md`
  § "The question arm (#1973)" — `byQuestion` is a second map so that a lookup
  cannot become an authorization, and the lesson that an "inherits X for free"
  claim must be resolved at the seam X actually reads.
- `docs/protocol-mobile.md` § `question_dismissed` — the published `outcome` /
  `source` contract this slice extends, and the live prose it falsifies.

## Context

Claude's `AskUserQuestion` call parks on a human through the same mcp-approve
bridge a permission prompt does. `surfaceQuestion` records the batch, correlates
its nonce to claude's `tool_use_id` in `byQuestion`, and broadcasts
`question_shown`; `retireQuestion` is today the only thing that ever resolves a
batch, and it does so for the no-answer class only.

This slice adds the refusal verdict: the daemon-side primitive that resolves a
named batch as a deny carrying a fixed instruction to wait for the user's
message. It wires nothing — `relay.QuestionResolver` stays unimplemented and
`V2SessionConfig.QuestionResolver` stays nil at every construction site, because
the relay handler applies no authorization and the per-device gate is #1986's.

No ADR is warranted: this extends an established arbiter rather than deciding
anything new about it.

**Size, re-counted against this plan:** 1 production source file, 1 new method
and no new exported type, 0 consumer call sites to update simultaneously, 5
acceptance criteria, 3 reject branches (unknown correlation, consumed one-shot,
already-resolved approval). Written work is ~95 production lines, ~190 test
lines and ~25 doc lines — ~310, against the refiner's ~340. Every boundary
holds, and the deliverable does not decompose: the consume, the verdict and the
dismissal are one arbiter by AC-3, so a child carrying any one of them alone
would be a slice nothing outside this family calls.

## Design

One production file, `cmd/pyry/modal_resolve_v2.go`.

**Three new compile-time constants**, beside the existing
`outcomeQuestionUnanswered` / `sourceQuestionNoAnswer` pair:

- `reasonQuestionRefused` — the deny message. Daemon-owned like
  `reasonRemoteDeny` and `permbridge`'s `reasonTimeout`, so no host-derived or
  client-authored byte can reach claude through it. It must say that the user
  declined to pick an option, wants to discuss the question first, and that
  claude is to wait for their next message rather than answer it itself — an
  unadorned deny lets claude guess an answer and carry on.
- `outcomeQuestionRefused` — the dismissal outcome sentinel, distinct from
  `outcomeQuestionUnanswered` and carrying no claude-authored string.
- `sourceQuestionRemote` — the dismissal source, the value
  `docs/protocol-mobile.md` publishes as `remote`. A local constant rather than
  `string(audit.SourceRemote)` because this path writes no audit record at all
  (`retireQuestion` documents why: `audit.Entry` is the modal vocabulary), so
  nothing here needs the two spellings to stay one.

**One new method:**

```go
func (b *streamApprovalBridge) RefuseQuestion(batchID string) (consumed bool)
```

Exported like `ResolveStream` and for the same reason: it is the seam a later
composition-root type calls, not an internal closure. Its steps, in the order
the ticket makes load-bearing:

1. **Read `byQuestion[batchID]` under `mu` — no delete.** A miss returns false
   immediately: an unknown batch, or one whose `retireQuestion` already ran.
   `retireQuestion` stays the sole, unconditional deleter, exactly as
   `ResolveStream` resolves without deleting. The correlation read is also what
   makes a nil `b.questions` safe without a second guard — `byQuestion` is
   written only by `surfaceQuestion`, which `Surface` reaches only under a
   non-nil registry, so a nil registry implies an empty map and step 1 has
   already returned.
2. **`b.questions.Resolve(batchID)` — the one-shot consume.** A miss returns
   false with nothing done: the no-answer backstop, or an earlier refusal,
   already consumed and dismissed this batch. Reading the correlation first and
   consuming second is what keeps a refusal from winning the one-shot inside
   `retireQuestion`'s two-critical-section window and then finding no
   `tool_use_id` — which would leave claude undenied *and* the backstop silenced.
3. **`b.perm.Resolve(toolUseID, permbridge.Deny(reasonQuestionRefused))`.**
   Strictly after the consume: the control server's deferred `retireQuestion`
   runs the instant `Pending.Await` returns, so denying first would let the
   backstop win the one-shot and broadcast `unanswered` for a batch the operator
   actually refused. A `perm.Resolve` miss is the ordinary no-op — permbridge
   already resolved by its own timer — and needs no branch.
4. **Broadcast exactly one `question_dismissed`** carrying `batchID`,
   `outcomeQuestionRefused` and `sourceQuestionRemote`, through the existing
   `broadcast` helper with its own push-error event string.
5. Return true.

**The dismissal fans out on its own goroutine, and that is the one deliberate
deviation from `retireQuestion`.** `broadcast` calls `bcast.ActiveConns`, which
funnels a request onto the relay manager's `Run` select — so a caller already on
`Run` blocks there until the daemon ctx is cancelled, stalling the manager.
`retireQuestion` is safe because it runs on a control-server handler goroutine;
this primitive's only intended caller is #1986's `QuestionResolver`
implementation, whose methods `relay` documents as running on `Run`, and which
cannot hand the work off itself because it owes its caller the `consumed` bool
synchronously. `ApprovalAnswerable` already carries this file's record of the
same hazard. Steps 1–3 stay synchronous, so `consumed`, the one-shot and
claude's verdict are all settled before the method returns; only the fan-out is
detached.

## Concurrency model

- Steps 1–3 run on the caller's goroutine. `b.mu` is held only around the map
  read (leaf lock, unchanged), and is not held across `questions.Resolve`,
  `perm.Resolve` or the broadcast — so no new lock-ordering edge is introduced.
- One goroutine per *consumed* refusal, running `broadcast` and nothing else. It
  exits when the fan-out finishes, or at the latest when the daemon ctx is
  cancelled: `ActiveConns` returns nil on a done ctx and `broadcast`'s Push loop
  returns early on teardown. Nothing waits on it and it touches no state after
  the last Push.
- Interleavings that matter, all settled by the one-shot: refusal versus
  backstop (whichever calls `Resolve` first acts; the loser does nothing),
  refusal versus a second refusal (same), refusal versus `ResolveStream` (a
  batch id is absent from `byModal` by construction, so a `modal_answer` naming
  one still resolves nothing).

## Error handling

- Unknown batch id, already-retired batch, already-refused batch → `false`, no
  verdict, no broadcast, no state change.
- `perm.Resolve` returning false (claude's approval already resolved) is
  ignored: the batch is consumed, the client must still learn the panel is dead.
- `broadcast` keeps its existing marshal-failure and per-conn Push-failure
  tolerance; a torn-down conn re-syncs through the connect-time reconcile.
- **No log line is emitted by this path at all.** The relay handler already logs
  the outcome with the batch id (the only field `protocol` marks safe), so a
  record here would be a duplicate whose only new material is exactly what must
  not be logged. That makes AC-4 structural rather than a redaction rule.

## Testing strategy

`cmd/pyry/stream_approval_test.go`, over the existing `newQuestionFixture`.
Because the fan-out is detached, the fixture's broadcaster is wrapped in a thin
recorder that signals a channel after each inner `Push`; a `waitPush` helper
turns that into a deterministic, race-free read of the recorded pushes (the
channel receive is the happens-before edge). Tests asserting *no* broadcast need
no wait: those paths return before spawning anything.

- **Refusal denies and dismisses.** `Pending.Await` returns
  `{behavior: deny, message: reasonQuestionRefused}`; the constant itself is
  asserted to instruct claude to wait, so a future edit that guts the wording
  reddens. Exactly one `question_dismissed` with the batch's own id and the two
  sentinels; the registry entry is gone; `byQuestion` is *unchanged* (the
  deleter is still `retireQuestion`); `consumed` is true.
- **Refusal then retire broadcasts nothing more**, and the correlation is gone
  after retire.
- **Inert paths, table-driven:** an unknown batch id, and a batch the backstop
  already consumed. Both → `consumed` false, no push, and the parked approval
  left unresolved (a `perm.Lookup` hit) so no verdict escaped on a lost race.
- **Leak probe.** Four distinct claude-authored sentinels through
  `questionInput`, pushes forced to fail so the error arm runs, plus an
  assertion that no log byte carries the refusal message, and that neither
  dismissal field carries a sentinel.

Gate: `go test -race ./cmd/pyry/... ./internal/questionbridge/...`,
`go vet ./...`, `go build ./cmd/pyry`.

## Docs

`docs/protocol-mobile.md` only, bounded to what this slice falsifies:
§ `question_dismissed`'s `remote` carry-over row (now emitted), the `local`
row's false "lands with #1985", the "landed vocabulary is one pair" sentence,
the `outcome` row's and the section intro's `#1985` cites, plus the
`question_refused` row in § Application message types and § `question_refused`'s
intro. One changelog entry. Answer-side cites (§ `question_answer`, § Question's
intro, the Contract-bounds rows) are #1991's and stay untouched.

## Open questions

- Does #1986's resolver call this on the `Run` goroutine, or arrange otherwise?
  Resolved in the design above by making the primitive safe either way; the
  detached fan-out is what discharges it.
- Whether the answer sibling (#1991) reuses `sourceQuestionRemote`. It should —
  the AC there names the same `source: remote` — but this slice publishes only
  its own pair and takes no dependency on that.

## Security review

**Verdict:** PASS

**Findings:**

- [Trust boundaries] No findings. Nothing remote-authored crosses into this
  primitive: the only parameter is a `question_batch_id`, and it is used solely
  as a map/registry key — never rendered, never logged, never concatenated. The
  deny message travelling *toward* claude is a compile-time constant, so the
  inbound frame cannot influence a byte of it. The authorization boundary is
  explicitly not here: this method is unreachable from the wire because
  `V2SessionConfig.QuestionResolver` stays nil at every construction site, and
  #1986 owns the per-device gate that must precede the first call.
- [Trust boundaries] SHOULD FIX — the batch id is an unguessable `crypto/rand`
  nonce, but this primitive treats *possession of it* as sufficient to refuse.
  That is correct only while the gate above it exists; the method's doc block
  must say so in the imperative, so a later wiring site cannot read the
  primitive as self-guarding. Land the sentence in Phase B.
- [Tokens, secrets, credentials] No findings. No token is minted, stored or
  compared here. `QuestionRefusedPayload.answer_token` never reaches this
  primitive — it is idempotency, not authorization, and the real dedup is the
  one-shot consume, so a server-side token store is deliberately absent (the
  reasoning `ResolveAnswer`'s doc block already carries).
- [File operations], [Subprocess execution], [Cryptographic primitives] Not
  applicable: this path opens no file, execs nothing, and mints no randomness —
  the only nonce involved was minted by `questionbridge.Registry.Record`.
- [Network & I/O] No findings. No new inbound surface and no new size limit is
  owed: the outbound frame's three fields are all daemon-asserted constants or
  the daemon's own nonce, so its length is not subprocess- or client-influenced
  — the property § `question_dismissed`'s SECURITY paragraph publishes.
- [Error messages, logs, telemetry] No findings, by construction rather than by
  redaction: the path emits no log record of its own, and the only records it
  can reach are `broadcast`'s existing content-free marshal/push-error lines
  (event, conn_id, env_id). AC-4 is therefore not a rule someone must remember —
  there is no field to leak into. The leak probe pins it against a later edit.
- [Concurrency] No findings on locking: `b.mu` is taken only around the
  `byQuestion` read and released before every registry call and the broadcast,
  so it stays a leaf and adds no ordering edge. The check-then-act shape
  (correlation read, then consume) is deliberately *not* atomic and is safe in
  both directions — the one-shot `Resolve` is the single arbiter, so a
  concurrent `retireQuestion` either wins it (this call becomes a no-op) or
  loses it (it broadcasts nothing). The reverse order is the exploitable one and
  is what the design forbids.
- [Concurrency] SHOULD FIX — the detached fan-out goroutine's exit condition is
  daemon-ctx cancellation in the worst case (relay `Run` exited while the ctx is
  still live makes `ActiveConns` block until then). Bounded by the number of
  refusals, each of which required a parked approval, so it cannot be driven
  unboundedly by a client; state the bound in the doc block in Phase B rather
  than leaving a reader to derive it.
- [Threat model alignment] No findings. § Security model's threat 1 (prompt
  injection from claude's own words) does not land on this frame: all three
  dismissal fields are daemon-asserted, and the `outcome`-never-carries-a-label
  rule is honoured by using a sentinel — the exact rule the natural
  implementation of an answer path breaks. The opposite direction — a client
  string reaching claude — is out of scope here by construction (the refusal
  carries no text) and is #1991's to answer for the answer verdict.

**Reviewer:** builder (self-review per the security-review checklist)
**Date:** 2026-09-02
