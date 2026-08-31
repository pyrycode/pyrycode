# #1915 — Report whether a parked approval still has anyone able to answer it

**Size:** S (confirmed — see § Size check)
**Labels:** `enhancement`, `size:s`, `security-sensitive`

---

## Files to read first

Read these before writing anything. Every entry names the **symbol** to read, not a line —
resolve names with `codegraph_search` / `codegraph_node`.

| File | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/modal_resolve_v2.go` | `ApprovalParked` (the whole method incl. its doc block) | **The twin, landed by #1919 in this same file.** Its shape, its doc-block claim list, its membership posture, and its snapshot-under-`mu`-then-ask sequence. This spec is deliberately its sibling; deviate only where § Design says why. |
| `cmd/pyry/modal_resolve_v2.go` | `streamApprovalBridge` (the type and its doc block) | The leaf-lock contract on `mu` — held around O(1) map ops only, never across `modal.Record`, `perm.Lookup`, `perm.Resolve` or a `Push`. That contract is what forbids holding `mu` across `ActiveConns`. Also: `bcast` and `ctx` are already fields. |
| `cmd/pyry/modal_resolve_v2.go` | `broadcast` | **The capability gate this report reuses verbatim**: `for _, c := range b.bcast.ActiveConns(ctx) { if !c.Interactive { continue } … }`. Note it takes `mu` *inside* the loop, not across `ActiveConns` — the existing precedent for the lock discipline below. |
| `cmd/pyry/modal_resolve_v2.go` | `Surface` | Where `byModal[modalID] = req.ToolUseID` is written, and that the `modal.Record` failure path stores **no** correlation and broadcasts nothing — the "never-surfaced" negative AC 3 names. |
| `cmd/pyry/modal_resolve_v2.go` | `retire` | The **sole** correlation deleter, unconditional, on every terminal `Await` return. This is what makes AC 3's "already-resolved" arm one line instead of four fixtures. |
| `cmd/pyry/modal_resolve_v2.go` | `newStreamApprovalBridge` | Its 6-parameter signature, and that `bcast` is already parameter 3. **Do not widen it** — see § Sizing constraint. |
| `cmd/pyry/modal_resolve_v2.go` | `ResolveStream` | That it runs on the relay manager's **`Run` goroutine**. It is the one bridge method from which this report must never be called — see § Concurrency model. |
| `cmd/pyry/interactive_turn_v2.go` | `interactiveBroadcaster` | The 2-method consumer-side interface (`ActiveConns` + `Push`) the bridge already holds as `bcast`. It does **not** grow a method here. |
| `internal/relay/v2session.go` | `ActiveConns`, `ActiveConn` | Three contract clauses this design leans on: the `V2StateOpen` gate (un-authenticated peers are never observable), the caller-goroutine constraint (funnelled onto `Run` via `m.snapshot` — safe from *any goroutine other than* `Run`), and "returns nil on ctx cancellation or once `Run` has exited". |
| `internal/permbridge/permbridge.go` | `Register` | Two facts: an empty id is refused with `ErrDuplicateID` **before** `Surface` ever runs (this bounds AC 3's empty arm — see § Error handling), and the `time.AfterFunc` timer closure is where #1912's consumer will sit. |
| `cmd/pyry/stream_approval_test.go` | `approvalReport`, `newApprovalReport`, `park` | **The #1919 fixture this ticket reuses rather than reimplements.** It already stands up permbridge + modalbridge + the bridge over `oneInteractiveConn("c1")` and exposes `bcast`, which is the only knob these tests need. |
| `cmd/pyry/stream_approval_test.go` | `bridgeLen`, `parkApproval`, `TestStreamApprovalBridge_ApprovalParked_LogsNothing` | The correlation-size guard, the raw park helper (needed for the never-surfaced arm), and the capturing-logger pattern AC 5's test copies. |
| `cmd/pyry/interactive_turn_v2_test.go` | `fakeInteractiveBcast` (the type **and its doc comment**) | Two behaviours that will bite otherwise: `ActiveConns` consumes one scripted `snapshots` entry per call and **reuses the last once exhausted**, and the double carries **no mutex** — both drive § Testing strategy's rules. |
| `cmd/pyry/queue_state_v2_test.go` | `oneInteractiveConn` | The one-interactive-conn constructor `newApprovalReport` already uses. |
| `cmd/pyry/modal_resolve_v2_test.go` | `auditLogger` | The `(*slog.Logger, *bytes.Buffer)` capturing pair AC 5's test needs. |
| `docs/knowledge/features/permbridge-package.md` | § "Why `internal/permbridge` is self-contained", § "Fail-closed / default-deny" | The zero-`internal/`-imports invariant that forces the ticket's shape note, and the fail-closed table this report must not perturb. |
| `docs/knowledge/features/streamsup-package-per-conversation-turn-busy-track-resolve-an-in-flight-tool-call.md` | the two `#1919` lessons at the end of the file | The "a membership conjunction has an ordering gap" lesson (which recurs here in a different place) and the fixture-sharing lesson (which is why § Testing reuses `approvalReport`). Short file; read the tail. |

---

## Context

Every approval parks in `internal/permbridge` under a fail-closed deadline; `Register`'s
`time.AfterFunc` resolves the entry to `Deny(reasonTimeout)` and the one-shot retires it. That
window is spent on elapsed time alone, because nothing tells the registry whether anybody is
still there to answer. The daemon already holds both halves of the answer and this slice joins
them into one report:

- **Still surfaced.** `streamApprovalBridge.byModal` holds the `modal_id ⇄ tool_use_id`
  correlation, which exists for exactly as long as a request is parked on a person.
- **Someone can answer.** `bcast.ActiveConns` enumerates open conns with their negotiated
  `Interactive` flag — the same #607 capability gate `broadcast` applies before pushing a
  `modal_shown` anyone could answer.

Both halves are load-bearing, which is why this is a **conjunction keyed on the approval** rather
than a bare "is anyone connected?". An approval can sit parked having never been surfaced:
`Surface`'s `modal.Record` failure path stores no correlation and broadcasts nothing, leaving
claude to time out to deny. A connectivity-only report would call that approval answerable while
no client has ever seen it; requiring both halves answers negative there, which is the fail-closed
direction.

This slice adds one method and nothing else. **No deadline moves**: an unanswered approval still
denies on `mcpApprovalTimeout`'s window with the existing fixed deny message, connected or not.
The consumer is #1912, which lands with its own behaviour test.

**No ADR is warranted.** Every decision here is a restatement of one already recorded in
`ApprovalParked`'s or `streamApprovalBridge`'s own doc block: membership-only posture, leaf-lock
discipline, resolve-on-read, no production caller. The documentation phase should fold the
lessons into `docs/knowledge/features/permbridge-package.md` (whose fail-closed table is what this
report will eventually feed) and/or the `streamsup` turn-busy overview that already carries
#1919's, rather than open a new decision record.

---

## Design

### The one new production symbol

```go
// ApprovalAnswerable reports whether approvalID is still parked on a person AND at
// least one interactive-capable client is connected to answer it.
func (b *streamApprovalBridge) ApprovalAnswerable(approvalID string) bool
```

`approvalID` is claude's `tool_use_id`, which is **also** permbridge's registry id (the control
server registers each approval under `payload.ToolUseID`) and **also** `byModal`'s value type.
One key, already shared by both sides — no new id vocabulary, and #1912's timer closure already
holds the right value in `id`.

Behaviour, in four steps and no more:

1. Under `b.mu`: scan `byModal`'s **values** for `approvalID`, recording a `parked bool`. Release
   `mu`.
2. `if !parked` ⇒ return `false` — **without calling `ActiveConns`**.
3. `for _, c := range b.bcast.ActiveConns(b.ctx)`: return `true` on the first `c.Interactive`.
4. Return `false`.

That is the whole method. It is a **conjunction across two membership sets**: the correlation is
parked *and* somebody who could have been shown its modal is connected. Either side going empty
makes the answer negative, which is the fail-closed direction for a deny deadline.

### No new fields, no new wiring

`bcast` and `ctx` are already constructor parameters of `newStreamApprovalBridge`, set at the one
production site in `cmd/pyry/relay.go` and at every test construction. This is the difference from
the twin: #1919 needed `toolCallInFlight` injected after construction and nil-guarded, because
`turnBusyTracker` has a different existence discriminant than the bridge. Nothing analogous
applies here.

**Do not add a `b.bcast != nil` guard.** `broadcast` already dereferences `b.bcast`
unconditionally on the `Surface` and `retire` paths, so a bridge constructed with a nil
broadcaster would already panic before this report could be reached. A guard here would add a
second, weaker answer to a question the existing code answers by construction, and would let a
wiring bug read as "nobody can answer" — the same failure mode #1917 refused a nil-receiver guard
to avoid.

### The interactive gate is the same gate, deliberately

Step 3 reuses `broadcast`'s `if !c.Interactive { continue }` — the #607 capability gate — because
"able to answer" must mean the same thing here as it does at the point a modal is *delivered*. A
conn that never negotiated the capability is never pushed a `modal_shown`, so it can never produce
a `modal_answer`; counting it would make the report claim an answerer that structurally cannot
answer. `ActiveConns` additionally excludes sessions still handshaking or handshake-complete-but-
token-unvalidated (the `V2StateOpen` gate), so an un-authenticated peer is never counted either —
inherited, not restated.

### Why the parked half is checked first

Step 2's short-circuit is not an optimisation flourish, and it is not an id-specific branch (see
§ Error handling). `ActiveConns` is **not a lock acquisition** — it is a blocking channel
round-trip that funnels the request onto the manager's `Run` goroutine via `m.snapshot`. The
eventual consumer sits in permbridge's `time.AfterFunc` timer goroutine, i.e. on the fail-closed
deny path, so the common negative — an approval id whose correlation is already gone — must not
pay a cross-goroutine round-trip to reach its answer. Cheap in-memory conjunct first; the one that
can block, second and only when it can change the answer.

### The shape does not foreclose #1912's injection

The ticket's shape note is a hard constraint: `internal/permbridge` imports **nothing** from
`internal/` (its overview states this as an invariant, not a hazard), so the consumer cannot call
into `cmd/pyry` — the report has to arrive as injected state. The method value
`bridge.ApprovalAnswerable` has type `func(string) bool`, a signature built entirely from
predeclared types, so permbridge can hold it in a field or option without importing anything at
all. That is why this report takes **no `context.Context` parameter** and returns **no error**:
either would widen the injected type for no behaviour this slice or #1912 needs, and an error
return would reintroduce the existence oracle (§ Error handling).

Two things #1912 inherits and must handle at *its* injection site, not here:

- **There is no nil-receiver guard**, mirroring `ApprovalParked`, `Busy` and `ToolCallInFlight`.
  A method value on a nil `*streamApprovalBridge` is a **non-nil func that panics on first call** —
  exactly the hazard #1919's `w.busy != nil` wiring guard exists to prevent, arriving through a
  method value. #1912 must guard the assignment, not receive a guard here that would make a
  wiring bug read as "nobody can answer".
- **The daemon runs the bridge only under `w.approvals != nil`.** Where no bridge exists there is
  no injection and permbridge must default to its present behaviour (deny on the existing window),
  not to a silent positive.

### Rejected alternatives, with the reason each was rejected

- **A reverse index (`byToolUse map[string]struct{}`) for an O(1) parked check.** Buys nothing:
  `byModal` is bounded by the approvals concurrently parked on a human, each holding a
  control-socket connection blocked in `Await`, so the scan is over a single-digit map. It costs a
  second map to keep in sync across `Surface` and `retire` — two more mutation points on the
  security-critical correlation store, for a slice whose whole point is to add a read.
- **Ask `ActiveConns` first, then check parked.** Pays the blocking `Run` round-trip on every
  negative, including on permbridge's deny-path timer goroutine. Same answer, worse failure mode.
- **A bare "is anyone connected?" report, not keyed on the approval.** Reports a never-surfaced
  approval (the `modal.Record` failure path) as answerable while no client has ever seen it —
  the fail-open direction. AC 1's conjunction and AC 3's never-surfaced arm both exist to forbid
  this.
- **Count conns, return the conn set, or return `(bool, error)`.** Each reintroduces the existence
  oracle AC 3 pins. See § Error handling.
- **Key the report by `modal_id`.** The consumer holds permbridge's registry id, which is the
  `tool_use_id`. Keying by modal id would force the consumer to acquire a vocabulary it has no
  access to.
- **Take a `context.Context` parameter.** `b.ctx` is already the right lifetime (daemon ctx,
  captured at construction, the same one `broadcast` passes) and its cancellation produces the
  fail-closed answer. A ctx parameter would widen the injected func type against #1912's needs.
- **Reach the report from the relay `Run` goroutine** (e.g. from inside `ResolveStream`).
  Deadlocks the manager — see § Concurrency model. Named here so nobody "simplifies" toward it.
- **Design against `modalDenyTimeout` / `ArmModalTimeout` (`internal/relay/v2session_modal.go`).**
  A second two-minute fail-closed modal deny that **nothing in production arms** — only tests call
  it, as a comment in that file records. Out of scope; do not treat it as a live deadline.

---

## Concurrency model

No new goroutines. No new channels. Nothing to shut down.

**Lock discipline is a hard requirement.** `streamApprovalBridge.mu` is documented as a leaf lock:
"`bridge.mu` → `registry.mu` never nests and there is no deadlock order to reason about." The
scan of `byModal`'s values happens under `mu`; `mu` is released **before** `ActiveConns` is
called. Holding it across the call would be worse than the twin's case, because `ActiveConns` is
not a lock acquisition at all but a blocking hand-off onto another goroutine — `bridge.mu` would
be held for the duration of the manager's scheduling latency, blocking every concurrent `Surface`,
`retire` and `ResolveStream`. `broadcast` already establishes exactly this discipline in this file
(it takes `mu` *inside* the `ActiveConns` loop, never around it); follow it.

**Caller-goroutine constraint — state it in the doc comment.** `ActiveConns` is documented safe
from any goroutine *other than* the manager's dispatch (`Run`) goroutine, because it funnels onto
`Run` via `m.snapshot`. Calling this report from `Run` self-deadlocks the manager: `Run` would be
blocked sending on `m.snapshot` with itself as the only receiver. `ResolveStream` runs on `Run`
today, so "another bridge method already does it" is *not* a licence. #1912's `time.AfterFunc`
timer goroutine is not `Run`, so its call is safe.

**Teardown is fail-closed by inheritance.** `ActiveConns` returns nil on ctx cancellation or once
`Run` has exited, which reads as "nobody connected" and so as a negative answer at daemon
teardown. Correct, not a gap to close: at teardown nobody can answer.

**The answer may go stale between step 1 and step 3, and that is correct rather than tolerated.**
A `retire` landing in the gap means the report answers about a correlation just deleted; a client
disconnecting in the gap means it answers about a conn just gone. Both are one-call-stale answers
to a question whose truth changes under any locking discipline — a consumer that read the value
under a global lock would still act on it after releasing. The report is a **level, not an edge**:
#1912 re-reads it, and there is no transition to miss. The `#1919` lesson already folded into the
turn-busy overview applies verbatim in a different place here: a conjunction across two
independently-updated stores can lag, and the lag direction is the fail-closed one.

Callers: any goroutine except the relay `Run` goroutine. Concurrent readers are already the
established shape — `ResolveStream` reads `byModal` off `Run` while `Surface` and `retire` mutate
it from control-server handlers.

---

## Error handling

The method returns `bool` and cannot fail. That is deliberate and load-bearing.

- **The signature IS the existence-oracle enforcement**, exactly as `Busy`, `ToolCallInFlight` and
  `ApprovalParked` document — not a runtime branch. A later widening to `(bool, error)`, or any
  variant handing back a modal id, a conn id or a count, reintroduces the oracle silently. AC 3 is
  what pins the posture.
- **Every negative class collapses through the same path.** Unknown, never-surfaced,
  already-resolved and empty approval ids all fail step 1's membership scan and reach step 2's one
  `return false`. There is **no id-specific branch** — no `if approvalID == ""`, no length check,
  no shape validation — and the four classes are indistinguishable from each other in the result.
  What the report *does* reveal is parked-ness for a connected daemon, which is its entire
  content; what it must not reveal is *why* a negative is negative.
- **Honesty note on the empty id — this is where this slice differs from its twin, and the
  developer must not copy #1919's fixture here.** `ApprovalAnswerable("")` reaches negative
  because `byModal` holds no empty value: `permbridge.Register` refuses an empty id with
  `ErrDuplicateID` before the control server ever reaches `Surface`. #1919's table could
  additionally plant an empty-`ToolUseID` correlation by calling `Surface` directly, because its
  report then asked `toolCallInFlight(conv, "")` and `setBusy` refuses an empty tool-call id — so
  the planted entry was harmless there. **Here it is not**: a planted empty correlation would
  *match* the scan and flip `ApprovalAnswerable("")` positive. Do not plant one, and do not "fix"
  the hypothetical with an `if approvalID == ""` guard — that guard is precisely the id-specific
  branch AC 3 forbids, and the invariant belongs at the write side (`Register`), which already
  holds it. § Security review records the residual and its bound.
- **Logging: none.** `ApprovalAnswerable` contains **zero log statements**. Every diagnostic worth
  emitting from it would carry a `tool_use_id`, a conn id, or the fact that a specific approval is
  outstanding — all of which the bridge's own SECURITY block and `clearForSession`'s
  routing-key-is-sensitive rule withhold. The correct count is zero, and AC 5's test makes it a
  mutation target.
- `permbridge` stays import-clean and log-free; nothing in this slice touches it.

---

## Testing strategy

**Home: `cmd/pyry/stream_approval_test.go`** — where the twin's tests and the fixture already
live.

**Reuse `newApprovalReport` / `park`; do not build a second fixture.** It already stands up
permbridge + modalbridge + the bridge over `oneInteractiveConn("c1")` and exposes `bcast`, which
is the only knob these tests turn. Its `tr` / `toolCallInFlight` half is inert for this report and
harmless. Extend its doc comment to say it now serves both reports; that is the whole edit.

**Do not attempt this from `internal/e2e`.** That suite builds `cmd/pyry` and runs it as a
separate process, so it observes wire and CLI behaviour only. This report has no production caller
and emits nothing on the wire, so nothing there can reach it until #1912 gives it a consumer.

**Two `fakeInteractiveBcast` behaviours that will bite — both are in its doc comment:**

- `ActiveConns` consumes one `snapshots` entry per call and **reuses the last once exhausted**.
  `Surface` → `broadcast` already consumes one call before any test calls the report, so a
  multi-entry scripted sequence is off by one and will drift further with every park. **Assign a
  fresh single-entry `snapshots` immediately before each report call** rather than scripting a
  sequence. (Steady-state reuse makes the already-advanced `callIdx` a non-issue.) A three-line
  helper on `approvalReport` that sets `snapshots` from a `...relay.ActiveConn` is worth writing
  once; anything larger is not.
- The double carries **no mutex**. Any test that calls the report more than once must keep those
  calls on one goroutine: **no `t.Parallel()` on the subtests** of the AC-3 table (the outer test
  keeps its `t.Parallel()`). This is the one place #1919's test shape must not be copied verbatim.

### T1 — the two answers for one still-parked approval (AC 1)

- Park and surface `tu-a1` via `f.park`. With the fixture's one interactive conn: assert
  `ApprovalAnswerable("tu-a1")` is **true**.
- Replace the snapshot with an empty conn set. Assert the **same** id is now **false**.
- **Assert `bridgeLen(f.bridge)` is still non-zero in the same test, and do not call retire.**
  This is load-bearing: it proves the negative came from the connectivity conjunct rather than the
  correlation conjunct, and it is the mirror of the twin's `GoesNegativeWhenTheCallEnds` trick.
  Without it, an implementation that silently dropped the correlation would pass.

### T2 — a connected client that never negotiated interactive does not count (AC 2)

Same fixture, one parked approval, three snapshots asked in sequence (**not** subtests, per the
note above):

| Conns connected | Expect |
|---|---|
| one conn, `Interactive: false` | false |
| the same conn id, `Interactive: true` | true |
| two conns: one non-interactive, one interactive | true |

Row 1 kills a `len(conns) > 0` implementation. Row 2 makes row 1 non-vacuous by showing the *only*
thing that changed is the flag. Row 3 kills the inverted gate (`if !c.Interactive { return false }`),
which rows 1–2 alone leave green.

### T3 — negatives collapse (AC 3)

One fixture, an **interactive conn connected throughout** (the fixture's default), holding a
**live positive control** so no row can pass merely by the report being universally negative:

| Arm | How it is built | Expect |
|---|---|---|
| the control: parked and surfaced | `f.park(t, "tu-a1")` | true |
| an unknown id, never registered anywhere | a literal that was never parked | false |
| parked in permbridge but **never surfaced** | `parkApproval(t, f.perm, "tu-unsurfaced", …)` and **do not** call `bridge.Surface` | false |
| already resolved | `retire := f.park(t, "tu-a2")`, then `retire()` | false |
| the empty approval id | `""` | false |

Notes the developer must carry into the test's doc comment rather than paper over:

- The **never-surfaced** arm is production-reachable: it is the state `Surface`'s `modal.Record`
  failure path leaves behind (no correlation stored, nothing broadcast, claude times out to deny).
  Building it by simply not calling `Surface` reproduces that state exactly and needs no RNG
  injection.
- The **already-resolved** arm drives `retire` and deliberately does **not** reconstruct the four
  terminal paths. A client's answer, the deadline, a lost caller and shutdown are four *callers*
  of one deleter: `retire` is the sole correlation deleter, its delete is unconditional, and it
  takes no path parameter — there is no mutant a per-caller fixture reddens that this one does not.
- The **empty** arm asserts against a fixture with **no** empty-`ToolUseID` correlation planted,
  and the reason is in § Error handling. Say so; the twin's fixture is not transferable here.
- The **"by the same path"** half of AC 3 is a structural claim about the implementation — the
  absence of an id-specific branch — and no fixture can distinguish it, because inserting
  `if approvalID == "" { return false }` is an *equivalent mutant* that changes no answer in the
  table. The rows pin the answers; code review reads `ApprovalAnswerable` for the absent branch.

### T4 — the report is log-free (AC 5)

- Stand the fixture up with `auditLogger()`, park and surface one approval.
- **Reset the buffer after the park**, so the assertion is scoped to the report and cannot be
  reddened by `Surface`'s own broadcast.
- Ask three arms — parked + connected (must be **true**, or the assertion is vacuous), parked +
  no conns, and an unknown id — then assert the buffer is **empty**.
- Genuine mutation target: any log line added inside `ApprovalAnswerable` reddens it, which is
  what makes AC 5 a pin rather than a claim about intent.

### AC 4 — nothing consults the report

Not a test, and deliberately not one. Three deterministic checks:

1. `git grep -n 'ApprovalAnswerable' -- cmd internal` returns the declaration in
   `cmd/pyry/modal_resolve_v2.go` and hits in `cmd/pyry/stream_approval_test.go` — **and nothing
   else**.
2. `make check` green, with **no edit to `mcpApprovalTimeout`, `approvalTimeout`,
   `envApprovalTimeout`, `internal/permbridge`, or the deny-reason constants**, and no production
   file touched beyond `cmd/pyry/modal_resolve_v2.go`. The existing suite staying green is what
   carries "an unanswered approval still denies on the existing window, with the existing fixed
   deny message."
3. `staticcheck` U1000 tolerates a test-only caller — **settled**: `ApprovalParked` landed with no
   production caller and `make check` is green on it. Do **not** wire a consumer to satisfy a
   lint; that breaks AC 4.

---

## Sizing constraint — do not widen the constructor

`newStreamApprovalBridge` has 16 call sites: one in `cmd/pyry/relay.go` and 15 in
`cmd/pyry/stream_approval_test.go`. Everything this report reads (`bcast`, `ctx`, `mu`, `byModal`)
is already a field, so the correct call-site cost is **zero**. If implementation appears to need a
seventh parameter, that is a signal the design drifted — stop and re-read § Design rather than
absorbing 15 mechanical edits.

---

## Size check

Re-counted against this written spec:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | **1** (`cmd/pyry/modal_resolve_v2.go`) |
| Total written work | ≤ 400 | **~290** (~60 production incl. the doc block, ~230 test incl. the fixture helper) |
| New exported types or interfaces | ≤ 5 | **0** (one method on an existing unexported type) |
| Consumer call sites needing simultaneous update | ≤ 10 | **0** (constructor untouched, no new field, no wiring) |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **1** (the not-parked short-circuit) |

Calibrated against #1919, the nearest analogue and the direct sibling: 369 insertions across three
files (`modal_resolve_v2.go` 106, `relay.go` 19, `stream_approval_test.go` 244) for a slice that
added one field, one method, one guarded wiring assignment and a new fixture. This slice adds one
method, **no** field and **no** wiring, and reuses that fixture — so it should land at or under
#1919's test half plus a smaller production half.

---

## Open questions

- **`ApprovalAnswerable` vs. a name closer to the twin.** Named for what it reports. `Answerable`
  alone reads ambiguously beside `ApprovalParked`; if code review prefers something else it is a
  rename with two call sites (declaration plus tests) and no design consequence.
- **Should the report bound how long `ActiveConns` may block?** Deliberately not, on
  evidence-based-fix grounds: no wedge has been observed, `broadcast` already carries the identical
  exposure on the control-server handler goroutine, and a wedged `Run` is already a whole-daemon
  liveness failure in which the approval could not have been answered anyway. Recorded in
  § Security review so #1912 inherits the exposure knowingly; if #1912 wants a bound, it belongs
  at that consumer's call site where a timeout has a meaningful fallback.
- **Does `retire` need to also drop the permbridge entry for the already-resolved arm?** No —
  outside this slice. The report reads only `byModal`, and `retire`'s existing deletion is
  sufficient for AC 3's arm.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] No findings — no new untrusted input crosses anything.** The report reads
  two existing daemon-side stores and adds one parameter, `approvalID`, supplied in-process by a
  future daemon-side caller (#1912's timer closure, which already holds permbridge's own registry
  id). It is compared only for equality against `byModal`'s values and is never used to construct
  a path, a command, a query or a wire frame. `byModal`'s values are claude-supplied
  `tool_use_id`s, already treated as untrusted correlation keys — permbridge's own overview states
  the id "is an opaque correlation key, **not** a capability or credential" — and this slice keeps
  that classification unchanged. The rejected design that *would* have moved a boundary is a
  caller-asserted key on the wire; it is rejected on the record in § Design.

- **[Trust boundaries] No findings — an un-authenticated peer cannot make an approval look
  answerable.** The connectivity conjunct is `ActiveConns`, which returns only sessions in
  `V2StateOpen` — authenticated and token-validated — and excludes `V2StateAwaitingInit` and
  `V2StateHandshakeComplete`. So a peer mid-handshake, or one whose token never validated, is
  never counted, and its negotiated `interactive` flag is never observable. That is the same gate
  `forwardEnvelope` enforces; the report inherits it rather than restating it.

- **[Trust boundaries] SHOULD FIX (bounded, recorded, no spec change) — the report answers about
  the daemon's *whole* conn set, not the approval's own audience.** A parked approval is not owned
  by a conn: `broadcast` fans `modal_shown` to every interactive conn and any of them may answer,
  so "at least one interactive conn is connected" is the correct question. The consequence to
  record is that a gated device connected for conversation B keeps an approval parked on
  conversation A reported answerable. This is not a leak — the report is read daemon-side and
  returns a bare bool — and it matches the delivery reality (that device *would* be shown A's
  modal and *may* answer it, which is #1080's authorization surface, unchanged). Recorded so
  #1912 inherits the semantics knowingly rather than assuming per-conversation scoping.

- **[Trust boundaries] SHOULD FIX (bounded, and the mitigation is a testing rule, not code) — a
  planted empty correlation would make the empty probe positive.** If a `byModal` value were ever
  the empty string, `ApprovalAnswerable("")` would match it and report positive whenever an
  interactive conn is connected. Reaching that state requires an in-process caller invoking
  `Surface` with a hand-built `permbridge.Request` — no untrusted input crosses into it, because
  `permbridge.Register` refuses an empty id with `ErrDuplicateID` before the control server can
  reach `Surface`. The mitigation is (a) **do not plant one in the AC-3 fixture** (§ Error
  handling and § Testing say so explicitly, because the twin's fixture *does* plant one and is not
  transferable), and (b) **do not add an `if approvalID == ""` guard**, which would be the
  id-specific branch AC 3 forbids while moving an invariant off the write side that already holds
  it. Named rather than silently inherited, because the twin's precedent points the wrong way here.

- **[Error messages, logs, telemetry] No findings, and it is pinned rather than asserted.**
  `ApprovalAnswerable` contains zero log statements, so no tool name, tool input, modal prompt,
  deny message, conn id or `tool_use_id` can leak from it (AC 5). It returns `bool` and no error,
  so there is no error string to leak into either, and the bridge's existing SECURITY block
  ("NEVER logs `req.Input`, the tool_name, the modal prompt/title, or a deny message beyond the
  fixed `reasonRemoteDeny` constant") continues to hold with nothing added under it. T4 makes this
  a mutation target: any added log line reddens it.

- **[Error messages, logs, telemetry] No findings — the timing side-channel is not a boundary
  here.** A not-parked id returns without a `Run` round-trip while a parked one pays for it, so
  the two are distinguishable by latency. Parked-ness is the report's entire content and is
  already distinguishable by the return value, and the four *negative* classes AC 3 enumerates all
  take the identical short-circuit and are indistinguishable from each other. The caller is
  in-process and daemon-side; no remote party can time this call.

- **[Concurrency] MUST-FIX-class hazard, addressed in the spec — never call this from the relay
  `Run` goroutine.** `ActiveConns` funnels its request onto `Run` via `m.snapshot` and is
  documented safe only from *other* goroutines; a call from `Run` deadlocks the manager outright —
  no frames dispatched, no pushes, no modals delivered, and every parked approval left to its
  deadline. `ResolveStream` is a bridge method that already runs on `Run`, so proximity makes this
  an easy mistake. § Design rejects that placement by name and § Concurrency model requires the
  doc comment to state the constraint, which is why this is recorded as addressed rather than
  open. #1912's `time.AfterFunc` timer goroutine is not `Run`, so the intended consumer is safe.

- **[Concurrency] No findings — the leaf-lock invariant is preserved by construction, and the
  alternative was audited.** `bridge.mu` is released **before** `ActiveConns` is called, so
  `bridge.mu` is never held across a blocking cross-goroutine hand-off. Holding it would block
  every concurrent `Surface`, `retire` and `ResolveStream` for the manager's scheduling latency
  and would retire the leaf-lock property `streamApprovalBridge`'s doc block claims outright.
  `broadcast` already establishes the correct discipline in this same file. The staleness window
  this creates is analysed in § Concurrency model: the report is a level, not an edge.

- **[Concurrency] SHOULD FIX (bounded, deferred to #1912 by name) — the call can block on the deny
  path.** `ActiveConns` blocks until `Run` services `m.snapshot` or `b.ctx` is done. #1912's
  consumer sits in permbridge's `time.AfterFunc` closure, so a wedged `Run` with a live ctx would
  stall the fail-closed deny rather than merely delaying an answer. Bound: a wedged `Run` is
  already a whole-daemon liveness failure in which the approval could not have been answered by
  anybody, and `broadcast` carries the identical exposure today on the control-server handler
  goroutine. No timeout is added here on evidence-based-fix grounds (§ Open questions); if #1912
  wants one it belongs at the consumer's call site, where a timeout has a meaningful fallback.

- **[Concurrency] No findings — no goroutines, no channels owned, nothing to shut down.** The
  method spawns nothing and owns nothing. Its one blocking operation borrows the manager's
  existing `snapshot` channel with the manager's documented contract, and daemon-ctx cancellation
  unblocks it with nil — the fail-closed answer.

- **[Concurrency — resource exhaustion] No findings.** The scan is over `byModal`, bounded by the
  approvals concurrently parked on a human, each of which requires a live control-socket
  connection blocked in `Await` — the control server's own concurrency, not an attacker-chosen
  number — and drained by `retire` on every terminal path. `ActiveConns` allocates one slice sized
  by the open-conn count, which the manager already bounds, and the report allocates nothing
  itself (it early-returns on the first interactive conn rather than collecting). No per-call
  amplification; the report is read once per deadline tick, not per frame.

- **[Tokens, secrets, credentials] Not applicable by design.** No token, key or credential is
  read, minted, compared, stored or transported. The modal ids `byModal` is keyed by are minted
  via `modalbridge.Record`'s `crypto/rand` path and this slice neither mints nor compares one — it
  reads the map's **values** (`tool_use_id`s), never its keys. Conn ids from `ActiveConns` are
  non-secret routing data and are not even read: only the `Interactive` bool is.

- **[File operations] Not applicable by design.** No path is constructed, opened, created or
  removed; nothing is persisted. The report is derived entirely from in-memory state, so path
  traversal, TOCTOU on the filesystem, file modes, symlinks and atomic writes have no surface.

- **[Subprocess / external command execution] Not applicable by design.** No `exec.Command`, no
  shell, no environment mutation, no signal handling. The claude child is upstream of the
  `tool_use_id`s this slice reads and is not touched by it.

- **[Cryptographic primitives] Not applicable by design.** No randomness, hashing, key derivation
  or comparison is introduced. String comparison happens only in the equality scan over `byModal`'s
  values, against a non-secret opaque correlation key that permbridge's own trust-boundary section
  documents as "not a capability or credential" — so there is no attacker-controlled comparison
  against a secret and `crypto/subtle.ConstantTimeCompare` has nothing to guard.

- **[Network & I/O] Not applicable by design, and AC 4 is what keeps it so.** Nothing is read from
  or written to a socket. No wire-format change, no new envelope type, no `Push`, no
  `pyry mcp-approve` flag. `ActiveConns` is an in-process snapshot request, not I/O. The report
  emits nothing and has no production caller in this slice, so it adds no network-reachable
  surface at all.

- **[Threat model alignment] No findings.** The relevant `docs/protocol-mobile.md` § Security model
  threats are a hostile gated device and a confused child. The device surface is read-only and
  unchanged: a device can influence this report exactly as much as it could influence delivery
  before — by connecting with the interactive capability, which is the #607-gated, `V2StateOpen`-
  authenticated path, and by answering a modal it holds the id for, which resolves the approval
  (the intended path, #1080's authorization surface). The confused-child surface is likewise
  unchanged: a child can mint `tool_use_id`s, but a child-minted id only ever becomes a `byModal`
  value through the approve lane it already controls, and the report hands back no id and no
  count. **Out of scope, named:** the dormant `modalDenyTimeout` / `ArmModalTimeout` lane
  (`internal/relay/v2session_modal.go`) has no production caller and never fires — nobody should
  design against it here; and what to *do* with a negative report, including whether to deny
  early, belongs to #1912.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
