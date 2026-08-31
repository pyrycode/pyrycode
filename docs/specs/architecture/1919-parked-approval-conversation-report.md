# #1919 — Report which conversation a parked approval belongs to

**Size:** S (confirmed — see § Size check)
**Labels:** `enhancement`, `size:s`, `security-sensitive`

---

## Files to read first

Read these before writing anything. Every entry names the **symbol** to read, not a line —
resolve names with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/modal_resolve_v2.go` | `streamApprovalBridge` (the type and its doc block) | The leaf-lock contract on `mu` — held around O(1) map ops only, never across `modal.Record`, `perm.Lookup`, `perm.Resolve` or a `Push`. This is the constraint the new method must not break. |
| `cmd/pyry/modal_resolve_v2.go` | `Surface` | Where `byModal[modalID] = req.ToolUseID` is written, and that `activeConv()` is used only to stamp the modal's `conversation_id` — not as a delivery key. |
| `cmd/pyry/modal_resolve_v2.go` | `retire` | The **sole** correlation deleter, unconditional, running on every terminal `Await` return. This is what makes AC 2's "by any path" one fixture instead of four. |
| `cmd/pyry/modal_resolve_v2.go` | `newStreamApprovalBridge` | Its 6-parameter signature. **Do not widen it** — see § Sizing constraint. |
| `cmd/pyry/modal_resolve_v2.go` | `streamApprovalResolver`, and the `streamApprovals` / `activeConv` / `notifyBlocked` fields on `modalResolverV2` | The in-tree precedent for an optional dependency assigned *after* construction, and the interface that must **not** grow a second method. |
| `cmd/pyry/stream_turn_busy.go` | `ToolCallInFlight` | The exact contract this report delegates to: membership only, both ids collapse to one `false`, **no nil-receiver guard** and why. |
| `cmd/pyry/stream_turn_busy.go` | `Busy` | The signature this report's posture mirrors — `bool`, never `(bool, error)`, never handing back an id. |
| `cmd/pyry/stream_turn_busy.go` | `clearForSession` | The conversation-id-is-sensitive log rule AC 5 names ("resolved daemon-side, stamped on the wire, never logged"). |
| `cmd/pyry/stream_turn_busy.go` | `setBusy` | That an empty tool-call id is silently no delta, and that `inflight` moves with `busy` under one acquisition — the reason the report's tracker half can go negative while `byModal` is still populated. |
| `cmd/pyry/relay.go` | the `if w.approvals != nil` block that calls `newStreamApprovalBridge`, and the `modalResolver.activeConv` / `modalResolver.notifyBlocked` assignments above it | The single production wiring site, and the adjacent assign-after-construct precedent. `w.busy` is in scope at both. |
| `cmd/pyry/main.go` | the `var turnBusy *turnBusyTracker` block, and the `approvals := permbridge.New()` line | Proof that the two discriminants differ: `approvals` is minted **unconditionally**, `turnBusy` only when `streamSink != nil`. |
| `cmd/pyry/stream_approval_test.go` | `bridgeLen`, `parkApproval`, `lastModalShown`, and `TestStreamApproval_NoBodyLeakInLogs` | The bridge stand-up idiom (13 existing constructions), the park helper, and the capturing-logger pattern AC 5's test reuses. |
| `cmd/pyry/stream_turn_busy_test.go` | `stubBusyResolve`, and `TestTurnBusyTracker_ToolCallInFlightNegativesCollapse` | The resolve stub and the `tr.observe("sess-a", turnevent.ToolStart{ToolCallID: …})` drive idiom. Same package — reuse directly, do not re-declare. |
| `internal/permbridge/permbridge.go` | `Register` | That an empty id is refused with `ErrDuplicateID` **before** `Surface` runs. This bounds what the AC-3 empty-tool-id fixture can honestly claim. |
| `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-resolve-an-in-flight-tool-call.md` | whole file (short) | Why retention is nested rather than flat, and why a distinct-id independence fixture cannot separate the two. That property is #1917's and is **already pinned there** — do not re-pin it here. |

---

## Context

`streamTurnHoldTimeout` bounds the delivery hold at 15 minutes, and msgqueue's give-up then
abandons the message queued behind the waiting turn. That constant's own comment records the
gap: there is deliberately no `Pending` analogue for the stream path, because such an exemption
"resets the give-up streak forever, which a HUMAN decision may legitimately need and a running
turn should not." It names staleness as the better discriminator and notes it needs a
per-conversation timestamp `turnBusyTracker` does not hold.

Staleness is an inference from silence. **Outstanding-ness is a direct answer, and the daemon
already holds it.** `streamApprovalBridge` owns the `modal_id ⇄ tool_use_id` correlation in
`byModal`, which exists for exactly as long as a request is parked on a person. #1917 supplies
the conversation-keyed membership question as `ToolCallInFlight`. This slice joins the two into
one report and does nothing else — no deadline moves, no give-up changes, and no production
caller. The consumer is #1911, which lands with its own behaviour test.

**No ADR is warranted.** The design decisions here (resolve-on-read, membership-only posture,
inject-don't-widen) are all restatements of decisions already recorded in `turnBusyTracker`'s and
`streamApprovalBridge`'s own doc blocks. The documentation phase should fold the lessons into
`docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-resolve-an-in-flight-tool-call.md`
(which already names #1919 as "the intended consumer") rather than open a new decision record.

---

## Design

### The one new production symbol

```go
// ApprovalParked reports whether conversationID currently has an approval parked
// awaiting a human decision.
func (b *streamApprovalBridge) ApprovalParked(conversationID string) bool
```

Behaviour, in four steps and no more:

1. `b.toolCallInFlight == nil` ⇒ return `false` **without taking `mu` and without calling out**.
2. Under `b.mu`: copy the *values* of `byModal` into a `[]string` sized `len(b.byModal)`. Release `mu`.
3. For each `toolUseID` in that snapshot: `b.toolCallInFlight(conversationID, toolUseID)` — return
   `true` on the first positive.
4. Return `false`.

That is the whole method. It is a **conjunction across two membership sets**: the correlation is
parked *and* the call is in flight on the asked-about conversation. Either side going empty makes
the answer negative, which is the fail-closed direction for a hold.

### The one new field

A sixth injected dependency on `streamApprovalBridge`, placed with the other
immutable-after-construction dependencies (above `mu`, alongside `perm` / `modal` / `bcast` /
`activeConv`):

```go
toolCallInFlight func(conversationID, toolCallID string) bool
```

Its doc comment must carry three claims, because each is a decision a later reader will otherwise
undo:

- **Set after construction at the one production site, not through a constructor parameter** —
  the same shape `modalResolver.activeConv` and `modalResolver.notifyBlocked` already use in the
  adjacent `relay.go` block. § Sizing constraint says why.
- **`nil` ⇒ negative for every conversation, without calling.** The bridge and the tracker have
  *different* non-nil discriminants: the bridge is built under `w.approvals != nil`, and
  `approvals` is minted unconditionally at the composition root, while `turnBusy` exists only
  alongside `streamSink`. So in the daemon's PTY mode the bridge is **live and holds no tracker**.
  Answering negative there is what keeps PTY mode semantically unchanged.
- **This short-circuit lives in the bridge, deliberately, and is not a nil-receiver guard on
  `ToolCallInFlight`.** #1917 refused that guard on the record, because it "would be the first
  step toward a consumer silently reading false in PTY mode instead of failing loudly." Adding one
  now would retire that decision. The absent *dependency* answering negative and the absent
  *tracker* failing loudly are different questions with different answers.

It is read without `mu` because it is written once, at wiring time, before `mgr.Run`'s goroutine
starts — identical to `perm`, `modal`, `bcast`, `activeConv` and `ctx`, none of which `mu` guards
either. `mu` guards `byModal` and `nextID`, and nothing else.

### Wiring (`cmd/pyry/relay.go`, inside the existing `if w.approvals != nil` block)

Between the `newStreamApprovalBridge` call and `modalResolver.streamApprovals = bridge`:

- `if w.busy != nil { bridge.toolCallInFlight = w.busy.ToolCallInFlight }`

**The `w.busy != nil` guard is mandatory, not defensive padding, and the comment must say why.**
A method value on a nil `*turnBusyTracker` is a **non-nil func** that panics on its first call —
`ToolCallInFlight` takes `t.mu` immediately and carries no receiver guard. An unguarded
`bridge.toolCallInFlight = w.busy.ToolCallInFlight` would therefore defeat the nil short-circuit
entirely and convert PTY mode from "reports negative" into "panics on the first consumer read".
This is the same typed-nil-in-an-interface hazard `observe`'s doc block names, arriving through a
method value instead of an interface.

Placement is already correct: the whole block runs before `mgr.Run` starts, so the field is set
before any goroutine could read it. No data race, no synchronisation needed.

### Rejected alternatives, with the reason each was rejected

- **Stamp the conversation into `byModal` at `Surface` time.** Reintroduces a race the ticket
  names: the `ToolStart` travels child → parser → sink → drain in-process while the approve
  travels claude → `pyry mcp-approve` → control socket, and nothing orders the two. Resolving on
  read removes the race outright and lets `retire` — the sole deleter, on every terminal path —
  carry "reports negative again" with no counter and no fifth terminal path to pin.
- **Stamp `activeConv()`.** Unsound as a *delivery* key. `activeConversation.set` has one
  production caller, `sessionRouter.Route`, which runs at enqueue rather than at delivery, so a
  message enqueued for B while A sits parked moves the cursor to B and misattributes every
  approval A parks afterwards. Good enough for scoping a modal to a client's view; wrong for a
  report the delivery hold trusts. **A fixture pins this** (§ Testing, T1).
- **Carry a session or conversation id on `control.ApprovePayload`.** Makes the delivery
  decision's key caller-asserted, which `turnBusyTracker`'s SECURITY note documents as
  disqualifying. No wire-format change and no `pyry mcp-approve` flag.
- **Key the report by modal or tool_use id.** The consumer (#1911's `waitIdleForDelivery`) holds a
  conversation id and no approval id, so that shape would force an enumeration or a widening at
  the consumer. `Busy(conversationID) bool` is the existing report of the right shape.
- **Also gate on `Busy(conversationID)`.** Redundant under the invariant `ToolCallInFlight`
  already documents — a non-empty `inflight` entry implies a busy one — and would add a lookup and
  a branch for no behaviour change.
- **Add the method to `streamApprovalResolver`.** That interface exists for `ResolveAnswer`'s
  verdict arm and has one method for a reason. The report has a different consumer; widening it
  would drag the new method into `modalResolverV2`'s 18 nil constructions for nothing.

---

## Concurrency model

No new goroutines. No new channels. Nothing to shut down.

**Lock discipline is the whole concurrency story, and it is a hard requirement.**
`streamApprovalBridge.mu` is documented as a leaf lock: "bridge.mu → registry.mu never nests and
there is no deadlock order to reason about." `ToolCallInFlight` takes `turnBusyTracker.mu`.
Calling it under `bridge.mu` would create the first nesting order this file has, and it would be a
lock order no other call site establishes — the classic shape that becomes a deadlock the day a
second edge appears in the other direction.

So the sequence is **snapshot under `mu` → release `mu` → ask**, and the method's doc comment must
say so in those terms. The snapshot allocation is the price of keeping the lock a leaf, and it is
cheap: `byModal` is bounded by the approvals concurrently parked on a human, which is a
single-digit set in practice and bounded above by the control server's own concurrency.

**The snapshot may go stale between step 2 and step 3, and that is correct rather than tolerated.**
A `retire` landing in the gap means the method asks about a correlation that has just been
deleted; the tracker then answers about a call that is either still in flight (→ a positive one
microsecond after the approval resolved) or already swept (→ negative). A `Surface` landing in the
gap means a brand-new approval is missed for this call. Both are one-call-stale answers to a
question whose truth is changing under any locking discipline — a consumer that read the value
under a global lock would still act on it after releasing. The report is a **level**, not an edge:
#1911 re-reads it, and there is no transition to miss.

Callers: any goroutine. The existing mutators are `Surface` and `retire`, both on the
control-server handler goroutine; `ResolveStream` reads `byModal` on the relay `Run` goroutine
today, so a concurrent reader is already the established shape.

---

## Error handling

The method returns `bool` and cannot fail. That is deliberate and load-bearing:

- **The signature IS the existence-oracle enforcement**, exactly as `Busy` and `ToolCallInFlight`
  document. A later widening to `(bool, error)`, or any variant handing back a conversation id, a
  modal id or a count, reintroduces the oracle silently. AC 3 is what pins the posture.
- **Every failure mode collapses into `false` through the same path.** Unknown conversation,
  never-seen conversation, empty conversation, a parked correlation whose tool call was never
  observed, a parked correlation with an empty tool_use id, an absent tracker, and an empty
  `byModal` all reach the same `return false`. There is **no id-specific branch** — no
  `if conversationID == ""`, no `if toolUseID == ""`. The empty conversation key is never inserted
  into `inflight` (`observe` refuses an unresolvable session) and the empty tool-call id is never
  inserted (`setBusy` refuses it), so both empties are answered by the identical two map lookups
  inside `ToolCallInFlight` and need no guard here.
- **Logging: none.** `ApprovalParked` contains **zero log statements**. Every diagnostic worth
  emitting from it would carry either a conversation id — which `clearForSession` deliberately
  withholds as "a routing key treated as sensitive" — or a tool_use id, so the correct count is
  zero. AC 5's test is a real mutation target for this (§ Testing, T4).
- `permbridge` must stay import-clean and log-free; nothing in this slice touches it.

---

## Testing strategy

**Home: `cmd/pyry/stream_approval_test.go`** — it already stands the bridge up thirteen times and
sits in the same package as `stubBusyResolve` and `newTurnBusyTracker`.

**Do not attempt this from `internal/e2e`.** That suite builds `cmd/pyry` and runs it as a
separate process, so it observes wire and CLI behaviour only. This report has no production caller
and emits nothing on the wire, so there is no fake-tier assertion that can reach it until #1911
gives it a consumer. Budget spent there is budget lost.

Standard fixture for every test below: a `turnBusyTracker` from `newTurnBusyTracker` with
`stubBusyResolve(map[string]string{"sess-a": convA, "sess-b": convB})`, a bridge from
`newStreamApprovalBridge`, and `bridge.toolCallInFlight = tr.ToolCallInFlight`. Calls are put in
flight with `tr.observe("sess-a", turnevent.ToolStart{ToolCallID: "tu-a1", Title: "Read"})`, and
approvals are parked with the existing `parkApproval` helper followed by `bridge.Surface(req)`.
A tiny package-local helper that returns `(bridge, tracker)` is worth writing once; anything
larger is not.

### T1 — the report is conversation-keyed, and the key is not the cursor (AC 1)

- `tu-a1` in flight on `convA`; `tu-b1` in flight on `convB` — so **`convB` is itself busy with
  its own turn and its own tool call**, and simply has no approval parked.
- One approval parked and surfaced for `tu-a1`.
- **Construct the bridge with `activeConv` returning `convB`.** This is the load-bearing detail:
  an implementation that stamped `activeConv()` at `Surface` time reports the answers exactly
  inverted, so this fixture is what separates the shipped design from the rejected one. Without
  it, the rejected design passes.
- Assert `ApprovalParked(convA)` is true and `ApprovalParked(convB)` is false.
- The `convB` assertion is also what kills a `len(byModal) > 0` implementation.

### T2 — negative again after resolution, and only after the last (AC 2)

- Two calls in flight on `convA` (`tu-a1`, `tu-a2`), two approvals parked and surfaced, two retire
  closures held.
- Positive → run the first retire → **still positive** → run the second retire → negative.
- **Drive `retire`. Do NOT reconstruct the four terminal paths.** A client's answer, the deadline,
  a lost caller and daemon shutdown are four *callers* of one deleter: `retire` is documented as
  the sole correlation deleter, its delete is unconditional, and it takes no path parameter. There
  is no mutant that a per-caller fixture reddens and this one does not, and reconstructing the
  control server's `Await` lane four times over is exactly how this ticket's budget gets spent on
  nothing. State the argument in the test's doc comment instead.

### T3 — the conjunction is two-sided (AC 2, the tracker half)

The same fixture as T1, then `tr.observe("sess-a", turnevent.TurnEnd{…})` **without retiring the
approval**: the turn's close sweeps `inflight` for `convA` while `byModal` still holds the
correlation, and the report must go negative. Nothing else in the suite pins that half — T1 and T2
both move the `byModal` side — and the negative direction is the fail-closed one for a delivery
hold. Assert `bridgeLen(bridge)` is still non-zero in the same test, so the assertion cannot be
satisfied by an accidental correlation delete.

### T4 — the report is log-free (AC 5)

- Stand the fixture up with a capturing logger (the `bytes.Buffer` + slog handler pattern
  `TestStreamApproval_NoBodyLeakInLogs` already uses).
- **Reset the buffer after `Surface`**, so the assertion is scoped to the report and cannot be
  reddened by the surrounding wiring.
- Call `ApprovalParked` on both a positive and a negative conversation; assert the buffer is
  **empty**.
- This is a genuine mutation target: any log line added inside `ApprovalParked` reddens it, which
  is what makes AC 5 a pin rather than a claim about intent.

### T5 — negatives collapse (AC 3)

Table-driven over one fixture that also holds a **live positive control**, so the table cannot
pass vacuously:

| Arm | Expect |
|---|---|
| `convA` (parked, in flight) — the control | true |
| a never-seen conversation id | false |
| an unknown-but-plausible conversation id | false |
| `""` | false |
| `convA` with the parked approval's tool call never observed by the tracker | false |
| `convA` with a correlation whose `ToolUseID` is `""` | false |

Two honesty notes the developer must carry into the test's doc comment rather than paper over:

- The **empty-`ToolUseID`** arm is **not production-reachable through the approve lane**:
  `permbridge.Register` refuses an empty id with `ErrDuplicateID`, and the control server returns
  on that before it ever reaches `Surface`. Build the arm by calling `bridge.Surface` directly
  with a hand-built `permbridge.Request`, and label it a **posture pin** — it asserts the report
  cannot be turned into an oracle if that entry ever becomes reachable, not that a live bug
  exists.
- The **"by the same path"** half of AC 3 is a **structural claim about the implementation** — the
  absence of an id-specific branch — and no fixture can distinguish it, because inserting
  `if conversationID == "" { return false }` is an *equivalent mutant*: it changes no answer in
  the table. The tests pin the answers; code review reads `ApprovalParked` for the absent branch.
  Say this in the doc comment rather than implying the table proves more than it does.

### AC 4 — nothing consults the report

Not a test, and deliberately not one. Three deterministic checks instead:

1. `git grep -n 'ApprovalParked' -- cmd internal` returns the declaration in
   `cmd/pyry/modal_resolve_v2.go` and hits in `cmd/pyry/stream_approval_test.go` — **and nothing
   else**.
2. `make check` green, with **no edit to `streamTurnHoldTimeout` or `mcpApprovalTimeout`** and no
   production file touched beyond `modal_resolve_v2.go` and `relay.go`. The existing suite staying
   green is what carries "a message queued behind a waiting turn still meets the same give-up" and
   "an unanswered approval still denies on the existing window."
3. `staticcheck ./...` analyses test files by default, so the test-only caller satisfies U1000.
   **Confirm this; do not wire a production caller to satisfy it** — that breaks AC 4. If U1000
   does fire, the escape is a `//lint:ignore U1000` directive naming #1911 as the pending
   consumer, never a call site.

---

## Sizing constraint — do not widen the constructor

`newStreamApprovalBridge` has 14 call sites: one in `cmd/pyry/relay.go` and 13 in
`cmd/pyry/stream_approval_test.go`. A seventh parameter cascades into all 13 and pushes this past
the size-S call-site limit on mechanical edits alone — the exact failure shape that exhausted #75's
budget. The assign-after-construction form keeps the blast radius at **one site**, and the
precedent is the adjacent `modalResolver.activeConv` / `modalResolver.notifyBlocked` pair in the
same `relay.go` block.

If implementation reveals the widening is genuinely unavoidable, **stop and route back with a
split proposal** rather than absorbing 13 edits.

---

## Size check

Re-counted against this written spec:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **2** (`cmd/pyry/modal_resolve_v2.go`, `cmd/pyry/relay.go`) |
| Total written work | ≤ 400 | **~330** (~70 production incl. doc blocks, ~10 wiring, ~250 test) |
| New exported types or interfaces | ≤ 5 | **0** (one method on an existing unexported type) |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** (constructor untouched) |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **1** (the nil-dependency short-circuit) |

Calibrated against #1917, the nearest analogue: 564 insertions across the same two-file shape
(`stream_turn_busy.go` + its test), for a slice that built the whole nested retention structure, a
new pure classifier and three mutation points. This slice adds one field, one method and one
guarded assignment, and delegates the membership question wholesale.

---

## Open questions

- **Does the bridge method need a nil-receiver guard?** Left deliberately **without** one, mirroring
  `Busy` / `WaitIdle` / `ToolCallInFlight`. `modalResolverV2` already gates its own use of the
  bridge on `r.streamApprovals != nil`, so the nil bridge is never dereferenced today. #1911 owns
  the question of how its consumer reaches a bridge, and it should reach a non-nil one or check —
  not be handed a guard here that would make a wiring bug read as "no approval parked".
- **`ApprovalParked` vs. a name closer to `Busy`.** Named for what it reports rather than for its
  sibling. If code review prefers a different name, it is a rename with two call sites (declaration
  plus tests) and no design consequence.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — but the boundary moved, and the spec names where.** This
  slice adds **no new untrusted input**. Both existing inputs keep their provenance: `byModal`'s
  values are claude-supplied `tool_use_id`s already treated as untrusted (that is precisely why
  `turnBusyTracker.inflight` is nested), and `inflight`'s conversation keys are resolved
  daemon-side from the registry against the runner's construction-time session tag, never taken
  from the wire. The **one new input** is `ApprovalParked`'s `conversationID` parameter, and it
  crosses no boundary: it is supplied in-process by a future daemon-side caller that has already
  resolved it. The rejected `control.ApprovePayload` alternative is the design that *would* have
  moved the boundary, by making the key caller-asserted; it is rejected on the record in § Design.

- **[Trust boundaries] No findings — the cross-conversation read is confined, and here is the
  argument.** `ApprovalParked(A)` iterates **every** parked `toolUseID`, including ones belonging
  to conversation B, and asks the tracker `(A, thatID)`. Because `inflight` is nested per
  conversation, B's parked id is absent from A's inner set and the answer is `false`. The caller
  therefore learns nothing about B: no existence signal, no count, no id. This is the property
  that makes the iterate-and-ask shape safe, and it is inherited from #1917's nested retention —
  under the flat `map[toolCallID]conversationID` #1917 rejected, this loop would have leaked
  cross-conversation membership on every call.

- **[Trust boundaries] SHOULD FIX (bounded, inherited, no spec change) — a hostile child can
  produce a false positive about its own conversation.** A confused or hostile child on
  conversation C can emit a `tool_use` block reusing an id genuinely in flight on A; nested
  retention lands that under C's own key, so `ApprovalParked(C)` can report positive while C has
  no approval parked. `ApprovalParked(A)` is **unaffected** — A's own entry and A's parked
  correlation are both untouched — so no conversation can flip another's answer. The impact is
  exactly #1917's stated bound ("the worst it achieves is a false positive about itself"), and the
  eventual consequence at #1911 is that C's own child extends C's own delivery hold: a
  self-inflicted delay, not a cross-tenant effect. Recorded so #1911's consumer inherits the bound
  knowingly rather than discovering it; no change here, because the alternative (validating a
  child-minted id) is the flat-map design already rejected.

- **[Error messages, logs, telemetry] No findings, and it is pinned rather than asserted.**
  `ApprovalParked` contains zero log statements, so no tool name, tool input, modal prompt, deny
  message or conversation id can leak from it (AC 5). The conversation id is specifically withheld
  per `clearForSession`'s "routing key treated as sensitive" rule, which is the strictest rule in
  the neighbourhood. T4 makes this a mutation target: any added log line reddens it. The report
  returns `bool` and no error, so there is no error string to leak into either.

- **[Concurrency] No findings — the leaf-lock invariant is preserved by construction, and the
  alternative was audited.** `bridge.mu` is snapshotted and released **before** the tracker is
  asked, so `bridge.mu → tracker.mu` is never established. Holding `bridge.mu` across
  `ToolCallInFlight` would have created this file's first nesting order — the shape that becomes a
  deadlock the day an edge appears in the other direction — and the leaf-lock property is what
  `streamApprovalBridge`'s doc block currently claims outright. The snapshot's staleness window is
  analysed in § Concurrency model: the report is a level rather than an edge, so a one-call-stale
  answer is not a correctness gap for a consumer that re-reads.

- **[Concurrency] No findings — no goroutines, no channels, no shutdown sequence.** The method is
  a synchronous read on its caller's goroutine and cannot block: two map operations, one slice
  allocation, and N lookups under a leaf lock the tracker itself takes and releases. Nothing to
  leak, nothing to cancel, nothing that can wedge a caller's goroutine.

- **[Concurrency] MUST-FIX-class hazard, addressed in the spec — the nil method value.**
  `bridge.toolCallInFlight = w.busy.ToolCallInFlight` on a nil `*turnBusyTracker` produces a
  **non-nil** func that panics on first call, because `ToolCallInFlight` takes `t.mu` immediately
  and carries no receiver guard by #1917's explicit decision. Unguarded, that converts the daemon's
  PTY mode from "reports negative" into "panics on the first consumer read" — a remote-triggerable
  daemon crash once #1911 wires the consumer, since delivery is driven by an inbound
  `send_message`. § Design mandates the `if w.busy != nil` guard at the wiring site and requires
  the comment to state this reason, which is why this is recorded as addressed rather than open.

- **[Tokens, secrets, credentials] Not applicable by design.** No token, key or credential is
  read, minted, compared, stored or transported. The modal ids the correlation is keyed by are
  already minted via `modalbridge.Record`'s `crypto/rand` path and this slice neither mints nor
  compares one — it only iterates the map's **values** (`tool_use_id`s), never its keys.

- **[File operations] Not applicable by design.** No path is constructed, opened, created or
  removed; nothing is persisted. The report is derived entirely from in-memory state and adds no
  on-disk footprint, so path traversal, TOCTOU, file modes, symlinks and atomic writes have no
  surface here.

- **[Subprocess / external command execution] Not applicable by design.** No `exec.Command`, no
  shell, no environment mutation, no signal handling. The claude child is upstream of every input
  this slice reads and is not touched by it.

- **[Cryptographic primitives] Not applicable by design.** No randomness, hashing, key derivation
  or comparison is introduced. String comparison happens only inside Go map lookups against
  non-secret ids; there is no attacker-controlled comparison against a secret, so
  `crypto/subtle.ConstantTimeCompare` has nothing to guard.

- **[Network & I/O] Not applicable by design, and AC 4 is what keeps it so.** Nothing is read from
  or written to a socket. No wire-format change, no `pyry mcp-approve` flag, no new envelope type,
  no `Push`. The report emits nothing and has no production caller in this slice, so it adds no
  network-reachable surface at all — the ruled-out `control.ApprovePayload` field is precisely the
  change that would have added one.

- **[Concurrency — resource exhaustion] No findings.** The snapshot allocates `len(byModal)`
  strings per call. `byModal` is bounded by the approvals concurrently parked on a human, each of
  which requires a live control-socket connection blocked in `Await` — so the bound is the control
  server's own concurrency, not an attacker-chosen number, and the entries are drained by `retire`
  on every terminal path. No unbounded growth and no per-call amplification.

- **[Threat model alignment] No findings.** The relevant `docs/protocol-mobile.md` § Security model
  threats are a hostile gated device and a confused child. The device surface is untouched: the
  report is read-only over state a device can already influence exactly as much as it could before
  (by answering a modal it holds the id for, which resolves the approval — the intended path, and
  the same #1080 authorization surface). The confused-child surface is bounded above in the third
  finding. **Out of scope, named:** the dormant `modalDenyTimeout` / `ArmModalTimeout` lane
  (`internal/relay/v2session_modal.go`) has no production caller and never fires — nobody should
  design against it here; and the consumer-side decision of what to *do* with a positive report
  belongs to #1911 and #1915.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
