# #1931 — keep a still-answerable approval parked past its window (`internal/permbridge`)

Registry primitive only. Ships **unwired**: nothing injects the report in this slice, so
daemon behaviour is byte-identical when it lands and both e2e tiers stay green untouched.
`internal/permbridge` shipped the same way in #1103.

## Files to read first

| Where | Symbols | What to extract |
|---|---|---|
| `internal/permbridge/permbridge.go` | `Register` | The single line this ticket changes — the `time.AfterFunc` arming site. The whole insert happens under `mu`; that happens-before is why a 0-duration timer cannot fire before install. |
| `internal/permbridge/permbridge.go` | `resolve` | The security core. `delete` under `mu` is the sole arbiter of who writes `p.ch`. **Do not add a branch to it.** Read its doc comment before writing anything. |
| `internal/permbridge/permbridge.go` | `pending`, `Registry` | The struct that holds the timer, and the leaf mutex. Note `p.timer.Stop()` runs *outside* `mu` in `resolve` — that placement constrains the extension design (§ Concurrency model). |
| `internal/permbridge/permbridge.go` | package doc, `Await` | The two claims AC 5 rewrites, verbatim. Read both before writing code, not after. |
| `internal/permbridge/permbridge_test.go` | `awaitWithin`, `waitRetired`, `registryLen`, `sampleRequest` | Reuse all four. This package tests timers with short real durations and polling, not a fake clock — do not introduce one. |
| `internal/permbridge/permbridge_test.go` | `TestRegistry_AllowVsTimeoutRace` | The one-shot proof shape scenario 5 extends. |
| `internal/permbridge/permbridge_test.go` | `TestRegistry_TimeoutPathDenies`, `TestRegistry_LostCallerSelfCleans` | The no-report default. Both must stay green **unedited** — that is the "byte-identical unwired" claim's own test. |
| `cmd/pyry/modal_resolve_v2.go` | `ApprovalAnswerable` | The production report this seam is shaped for. Read its doc for four things: the signature (`func(string) bool`, no ctx, no error), the level-not-edge contract that AC 3 leans on, the parked ∧ connected conjunction, and the "never from the relay `Run` goroutine" rule. |
| `internal/control/server.go` | `handleApprove`, `watchApproveConn` | The only production `Register` caller, and the *second* deny lane: caller-disconnect and daemon-shutdown already deny outside the timer. The timer is not the only bound, and this ticket does not touch that lane. |
| `cmd/pyry/main.go` | `mcpApprovalTimeout`, `approvalTimeout` | The window in production: 10 minutes, `PYRY_APPROVAL_TIMEOUT`-overridable. Its doc already argues "waiting is not the unsafe state" — the premise this ticket builds on. |
| `docs/knowledge/features/permbridge-package.md` | § "Fail-closed / default-deny", § "The one-shot: `resolve`" | The invariant table this change adds rows to, and the **reused-id NIT** contract (§ Error handling below says what happens to it). Read-only — the documentation phase owns this file. |
| `docs/specs/architecture/1915-parked-approval-answerable-report.md` | § "The shape does not foreclose #1912's injection" | Why the injected type is exactly `func(string) bool`, and why the nil-bridge panic guard belongs at the wiring site rather than here. |

## Context

Every approval parked in `permbridge` arms a fail-closed deadline in `Register`. When it
fires, the entry resolves to `Deny(reasonTimeout)` and is deleted; the registry is a
one-shot, so a decision arriving afterwards finds nothing and is discarded.

Waiting is not the unsafe state. A non-YOLO headless `claude` blocks on the registered MCP
tool's verdict for the whole time an approval is outstanding — the tool does not execute
while parked — so denying on the deadline prevents nothing that remaining parked was not
already preventing. `mcpApprovalTimeout`'s own doc makes this argument. What the deadline
actually bought was turn termination, and #1911 took that job over: msgqueue's `Pending`
exemption is gated on #1919's `ApprovalParked`, so a message queued behind a waiting turn is
no longer abandoned. The cost of the deadline is already visible downstream —
pyrycode-desktop#510 exists because an answer sent while disconnected decayed into a
deny-on-timeout.

This slice adds the seam through which a caller reports whether a parked approval still has
somebody able to answer it, and spends the window only on approvals nobody can answer.
The fail-closed core is untouched: allow remains reachable by exactly one path.

**No ADR needed.** This does not choose between architectures; it narrows one existing
invariant and the package doc is where that narrowing belongs. The documentation phase
should fold the two new rows into `permbridge-package.md` § "Fail-closed / default-deny"
and update its § "Trust boundary" to cover the injected report as a second trusted input.

## Design

### The seam

Two additions to the exported surface, and nothing else:

```go
// AnswerableFunc reports whether the approval parked under id still has somebody
// able to answer it. Consulted only when a window elapses; a true reading buys
// exactly one more window and is then re-asked.
type AnswerableFunc func(id string) bool

// SetAnswerable installs the liveness report, or clears it with nil.
func (r *Registry) SetAnswerable(ask AnswerableFunc)
```

Four properties of the shape, each load-bearing:

- **`func(string) bool`, no `context.Context`, no `error`.** `streamApprovalBridge.ApprovalAnswerable`
  already has exactly this signature and its `approvalID` already *is* `permbridge`'s
  registry id (the control server registers under `Request.ToolUseID`). The method value
  assigns straight to `AnswerableFunc` — no adapter, and none needed in #1932. Widening it
  either way breaks that and is out of scope.
- **A value handed in, never an import.** `permbridge` imports only the standard library and
  nothing from `internal/`; a named func type built from predeclared types keeps that exactly
  true. Do not add an import.
- **Settable after `New()`.** The composition root builds the registry long before the bridge
  that can answer the question exists. `SetAnswerable` is therefore a setter, not a `New`
  option, and must tolerate being called after entries are already parked — the report is read
  at expiry, not captured at `Register`.
- **Absent reads as "nobody can answer".** A registry whose `SetAnswerable` was never called,
  or was called with nil, denies at the window exactly as today. This is what makes the
  unwired behaviour identical (AC 2) and it is also the fail-closed direction.

`SetAnswerable` writes the field under `Registry.mu` and the expiry path reads it under `mu`;
last write wins, and calling it concurrently with live entries is safe. It does not validate
its argument — nil is a meaningful value, not an error.

### The expiry path

`Register` changes by one line: the timer callback becomes `r.expire(id, timeout)` instead of
`r.resolve(id, Deny(reasonTimeout))`. The window the callback re-arms with is the same
`timeout` `Register` was handed, captured in the closure — **no new field on `pending`**.

`expire(id string, window time.Duration)`, in order:

1. Under `mu`: is `id` still in the map, and what is `r.answerable`? Release `mu`.
2. **Entry absent → return.** Already resolved by a winning `Resolve`. The report is not
   consulted for a dead entry — the same reason `ApprovalAnswerable` puts its cheap conjunct
   first: the common negative must not pay a cross-goroutine round-trip to reach an answer it
   cannot change.
3. **No report, or `window <= 0` → `r.resolve(id, Deny(reasonTimeout))`, return.**
4. Call `ask(id)` with **no lock held**.
5. **false → `r.resolve(id, Deny(reasonTimeout))`, return.**
6. **true →** under `mu`: if `id` is *still* present, `p.timer.Reset(window)`. Release, return.

Four branches, all in a new unexported method. `resolve` gains nothing and loses nothing —
AC 4's "exactly one caller writes each verdict" is held by the same `delete`-under-`mu`
arbiter it is held by today, and the extension path writes no verdict at all.

### Why `Reset` on the same timer, not a fresh `AfterFunc`

Arming a new timer would mean assigning `p.timer`, and `resolve` reads that field to call
`Stop()` **outside** `mu`. A field write racing that read is a data race `-race` would catch.
`Reset` mutates the timer object instead, leaving the field written once in `Register` and
read-only thereafter. Do not restructure `resolve` to move `Stop()` under the lock — the
extension design is built so it does not have to.

### Why at most one `expire` is ever in flight per entry

`time.Timer.Reset` on an `AfterFunc` timer documents that it "neither waits for the prior `f`
to complete… nor guarantees that the subsequent goroutine running `f` does not run
concurrently with the prior one." That hazard is structurally unreachable here: the re-arm in
step 6 happens **after** `ask` returns, so the next firing cannot be scheduled while the
previous callback is still inside the report. A slow report stretches the effective window
rather than overlapping with itself. The step ordering is the reason — do not hoist the
`Reset` above the `ask` as an "arm early" optimisation.

### Why the same window, and why it is re-asked

Re-arming with `Register`'s own `timeout` keeps the primitive parameter-free: the window
stays the single knob, `approvalTimeout` stays its only production setter, and the semantics
become "the timeout is a re-check interval, not a hard deadline." Each positive reading buys
one window and one window only — AC 3. An approval answerable at the window whose client then
disconnects is denied at the next expiry, so a single positive reading can never buy an
unbounded wait.

An approval whose answerer stays connected and simply never decides *does* stay parked
indefinitely. That is the intent, not a gap: interactive `claude` has no deadline and the
Agent SDK's permission callback "can stay pending indefinitely," and the client-liveness
report — not elapsed time — is the thing that says nobody is coming.

### Why a non-positive window is never extended

`Register`'s doc already promises that a non-positive timeout fires ≈immediately and resolves
to deny, and that promise must survive. It also has to: re-arming a non-positive timer with an
always-true report is an unbounded spin that calls the report — a cross-goroutine round-trip
onto the relay manager in production — as fast as the scheduler allows. Step 3 folds the guard
in beside the no-report case, where it costs no extra branch and keeps an existing documented
sentence true.

## Concurrency model

No new goroutines. The four existing roles are unchanged (caller/verb, resolver, per-entry
timer, `Lookup` reader); the timer role gains the report call.

- **`mu` stays a leaf.** It is held around O(1) map ops and the `r.answerable` read, and is
  **never held across `ask`**. In production `ApprovalAnswerable` is a blocking round-trip onto
  the relay manager's `Run` goroutine; holding a leaf mutex across it would stall every
  concurrent `Register`, `Resolve` and `Lookup` for the manager's scheduling latency, and would
  retire the leaf-lock property outright. This is the snapshot-release-ask discipline
  `ApprovalAnswerable` itself runs on. Scenario 6 pins it.
- **Where the report runs.** On `permbridge`'s `time.AfterFunc` goroutine, which is *not* the
  relay `Run` goroutine — the one goroutine `ApprovalAnswerable` documents as a deadlock.
  #1932 inherits this; say nothing new about it here.
- **The check-then-mutate across steps 1→6 is deliberately not atomic**, because `ask` can
  block. The re-check under `mu` in step 6 is what makes the mutation safe: a `Resolve` landing
  during the ask deletes the entry, step 6 finds it gone and skips the `Reset`, so no timer is
  ever re-armed for a deleted entry. The reverse order is equally safe: the `Reset` lands under
  `mu` before the delete, and `resolve`'s subsequent `Stop()` cancels it.
- **A stale reading is correct, not tolerated.** `ApprovalAnswerable` is documented as a level
  rather than an edge, on the explicit expectation that its consumer re-reads it. Step 6's
  re-arm is that re-read.

## Error handling

`AnswerableFunc` returns no error and the expiry path has no failure mode of its own. Every
path out of `expire` is either a deny or a bounded extension.

| Situation | Behaviour |
|---|---|
| No report set (the default, and this slice's shipped state) | Deny at the window with `reasonTimeout` — today's behaviour, byte-identical. |
| Report set to nil | Same as never set. |
| Report answers false | Deny at the window with `reasonTimeout`, the existing fixed message. |
| Report answers true | Entry survives; one more window; question re-asked at the next expiry. |
| Report answers true, then false at a later window | Denied at that window with `reasonTimeout`, no further extension. |
| `Resolve` lands during the ask | It wins the one-shot; step 6 finds the entry gone and does not re-arm. |
| Non-positive `Register` timeout | Deny ≈immediately, never extended. |
| Unknown / duplicate / already-resolved id | Unchanged, and the report is never consulted — it is only reachable from an expiry. |

**A dishonest report removes the bound.** A report that always answers true converts the
fail-closed deadline into an unbounded park, including for a lost caller that never `Await`s.
That is inherent to delegating the liveness question and is why `AnswerableFunc`'s doc comment
must state the contract explicitly: **answer false once nobody is waiting on this approval.**
The production report satisfies it through its parked half — `retire` deletes the
`byModal` correlation on every terminal path of `handleApprove`, so a lost caller reads
unanswerable. `TestRegistry_LostCallerSelfCleans` stays green because it installs no report.

**A panicking report kills the daemon.** `time.AfterFunc` has no recovery, and a method value
on a nil `*streamApprovalBridge` is a non-nil func that panics on first call — a hazard #1915's
spec names by hand. **Do not add `recover()` here.** Mapping a panic to "not answerable" would
turn a wiring bug into every approval silently denying, which is precisely the reading #1915
argues the guard must not produce. The guard belongs at #1932's injection site.

**The reused-id NIT is unchanged in kind.** The closure still captures `id` rather than the
`*pending`, so a stale timer firing as a *reused* id is re-`Register`ed could touch the new
entry. Step 6 cannot widen that gap — it re-arms only while the entry is present, so no timer
survives its own entry's deletion. The existing contract simplifies rather than tightens:
do not re-`Register` an id until its predecessor has resolved. Do not add an identity check;
it would close half a gap `resolve` still leaves open, for no reachable case (claude's
`tool_use_id`s are unique).

## Testing strategy

Same-package, stdlib `testing`, `go test -race`. Short real durations plus polling, matching
the file's existing idiom — **no fake clock, no new time abstraction.** One shared fixture: a
scripted report with an atomic call counter and a programmable answer sequence, so every
scenario can assert *how many times* the question was asked. Every wait is bounded
(`awaitWithin` / `waitRetired`); never a bare `Await`, because a lock-discipline regression
shows up as a hang.

1. **Extension parks the approval past its window, and a later allow lands as allow (AC 1).**
   Register with a short window and an always-true report. After several windows have
   elapsed: the entry is still in the registry, the ask counter is ≥ 2, and `Resolve(id,
   Allow(input))` returns true with `Await` yielding allow and the input byte-verbatim. The
   counter assertion is the non-vacuity guard — "still parked" would otherwise pass if the
   timer had never fired at all.
2. **No report denies at the window, with the existing message (AC 2).** Table over three
   setups differing in exactly one dimension: never `SetAnswerable`d, `SetAnswerable(nil)`, and
   a report that always answers false. Each: `Deny`, `Message == reasonTimeout`, entry retired
   within a bounded wait. The first two rows must also assert the ask counter is 0.
3. **Re-asked, not settled once (AC 3).** A report that answers true for the first two asks and
   false thereafter. Assert: the verdict is `Deny(reasonTimeout)`; the ask counter at deny time
   is ≥ 3 (so the entry demonstrably survived the first window); and **the counter does not
   grow afterwards** — sample it, wait two further windows, sample again, require equality.
   That last assertion is what pins "denied without further extension" and it is the one a
   re-arm-on-the-deny-path mutant would redden.
4. **The report is never consulted off the expiry path (AC 4, first half).** With an always-true
   report installed: `Resolve` of an id never registered returns false; a second `Register` of a
   live id returns `ErrDuplicateID` and leaves the first entry resolvable; `Resolve` of an
   already-resolved id returns false. Ask counter 0 throughout — use a window long enough that
   no expiry can fire during the test.
5. **Exactly one verdict, however many extensions (AC 4, second half).** Extend
   `TestRegistry_AllowVsTimeoutRace`'s shape: a ~1 ms window, a report that answers true for its
   first two asks and false after, and a goroutine racing `Resolve(Allow)`. Per iteration assert
   the same internal consistency the existing test asserts — allow iff `Resolve` won, deny
   otherwise — plus an empty channel buffer afterwards, proving no second delivery. Run it in
   the same iteration count band as the existing race test.
6. **The report is called with `mu` released.** Install a report that re-entrantly calls
   `r.Lookup(id)` and returns whether it found the entry. Assert the extension happens and the
   re-entrant `Lookup` saw the entry present. A design that held `mu` across the ask self-
   deadlocks here, so the whole scenario must sit behind a bounded wait that fails rather than
   hangs.
7. **A report slower than its window produces one expiry, not two.** A report that blocks for
   several windows and then answers false. Assert: while it is blocked the ask counter stays at
   1; exactly one verdict is delivered; it is `Deny(reasonTimeout)`; the entry is retired.
   This pins the re-arm-after-ask ordering from § Concurrency model.

Scenarios 2's first row, plus `TestRegistry_TimeoutPathDenies` and
`TestRegistry_LostCallerSelfCleans` left **unedited**, are together the evidence for
"byte-identical unwired." If either existing test needs a change to stay green, the design has
drifted — stop and re-read § The expiry path rather than editing the test.

## Doc rewrites (AC 5)

Four claims in `internal/permbridge/permbridge.go` state the old unconditional bound. Rewrite
each **where it stands** — do not renumber, do not delete the half that is still true. This is
#1909's `mcpApprovalTimeout` pattern.

1. **Package doc — "no entry outlives its deadline and no allow can ever land after a deny."**
   The second clause is still exactly true and stays. The first becomes the conditional bound:
   the window is spent on approvals nobody can answer; a positive reading from the injected
   report buys one more window and is re-asked; with no report injected the window is a hard
   deadline as before. Add one sentence naming the report as a second trusted input beside the
   trusted caller.
2. **Package doc — "every other terminal path — a deadline that elapses, a lost caller, an
   unknown id — yields deny."** Still true, but "a deadline that elapses" now means a window
   that elapses with nobody able to answer. Tighten that clause; leave the list otherwise.
3. **`Await` — "guaranteed to return within the Register timeout."** Becomes: within the
   `Register` timeout when no `AnswerableFunc` is installed (the default), and otherwise within
   one window of the first reading that says nobody can answer.
4. **`pending` — "timer is the fail-closed deadline, stopped by a winning Resolve."** Add that
   it is re-armed in place by the extension path, and that `p.timer` is written once in
   `Register` and never reassigned — the reason `resolve` may read it outside `mu`.

`Register`'s existing sentence about a non-positive timeout stays true as written and needs no
edit; extend its doc by one sentence naming the extension and the window's new role as a
re-check interval.

**Not edited by this ticket:** `internal/control/server.go`'s `handleApprove` carries
`// guaranteed to return within timeout` on its `Await` call. That claim is still true after
this slice, because nothing injects a report — it becomes conditional only when #1932 wires
one, which is where it should be rewritten. Named here rather than edited, so this ticket stays
at one production file and introduces no claim that is false on the day it lands.

## Open questions

- **Should the re-check interval eventually be decoupled from `Register`'s timeout?** A shorter
  re-check would notice a disconnect faster than one 10-minute window. Deferred deliberately:
  it adds a second knob to a primitive for a latency nobody has measured, and `watchApproveConn`
  already denies on the mcp-approve caller's own disconnect. #1932 is where the real latency
  becomes observable.
- **The nil-`*streamApprovalBridge` method-value panic** is #1932's to guard at the injection
  site, per #1915's spec. Flagged, not solved here.
- **Nothing distinguishes "extended five times then denied" from "denied at the window"** in any
  log or wire message. That is the log-free package invariant working as intended; if the
  extension turns out to need operational visibility, the consumer logs it.

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] Finding, addressed in the design.** This adds one new boundary: the
  registry now takes a *behavioural* input from outside. It is explicit and singular —
  `SetAnswerable` is the only writer, `expire` the only reader, and the value is process-internal
  Go chosen by the composition root, never anything a network peer supplies. `id` is unchanged:
  an opaque correlation key, not a credential. The registry now trusts two things instead of one
  — its caller, and the report's honesty. § Error handling makes that a written contract clause
  on `AnswerableFunc` ("answer false once nobody is waiting on this approval") and names how the
  production report satisfies it via `ApprovalAnswerable`'s parked half. Not a MUST FIX: the
  dishonest-report case is unreachable by any actor outside the daemon's own composition root.
- **[Tokens, secrets, credentials] No findings.** Nothing is generated, stored, hashed or
  compared. The report takes the id the caller already holds and returns a bool; it mints
  nothing and the deny message stays the fixed `reasonTimeout` constant.
- **[File operations] Not applicable.** The package touches no filesystem and this change adds
  no import — stdlib only, as the package doc states.
- **[Subprocess execution] No findings, and the direction is the safe one.** Nothing here execs.
  The indirect effect is that a parked tool call stays *un-executed* longer: `claude` blocks on
  the verdict for the whole park, so extending the park cannot cause a tool to run. Removing the
  deadline would be dangerous only if pending meant "running," and it does not.
- **[Cryptographic primitives] Not applicable.** No randomness, no secret comparison, no keys.
- **[Network & I/O] SHOULD FIX at the wiring ticket — resource holding, not exhaustion.** An
  extended approval holds a control-socket conn, a blocked `handleApprove` goroutine and a
  `watchApproveConn` watcher for as long as somebody can answer, rather than for at most
  `approvalTimeout`. Three things bound it: `handleApprove` already clears the conn deadline, so
  no socket timeout is being defeated; `watchApproveConn` still denies on caller disconnect and
  daemon shutdown, a lane this ticket does not touch; and in production an unbounded park
  requires a live interactive client, because `ApprovalAnswerable` conjoins parked with
  connected. A hostile local process with control-socket access could park approvals and hold
  them while a phone is connected — but it can already hold them for the full window today, each
  park costs it a conn, and a process that can open the control socket has strictly larger
  powers than this. No registry-size cap exists today and this ticket does not add one; #1932
  should re-examine it against real concurrent-approval counts. Report call cost is negligible:
  one round-trip per extended approval per window, with the production window at ten minutes.
- **[Error messages, logs, telemetry] No findings.** The package stays log-free. The report
  receives only the id — which is the report's own key, already in its possession — and returns
  a bool. The extension is invisible on the wire: an extended approval's eventual deny is
  byte-identical to today's, carrying the same fixed `reasonTimeout`. The diagnostic cost of
  that invisibility is recorded in § Open questions.
- **[Concurrency] Findings, all addressed in § Concurrency model.** (a) `mu` stays a leaf and is
  never held across `ask` — scenario 6 pins it with a re-entrant `Lookup` that self-deadlocks
  against a regression. (b) The check-then-mutate across steps 1→6 is deliberately non-atomic
  because `ask` can block; step 6's re-check under `mu` means no timer is re-armed for a deleted
  entry in either interleaving, and `resolve`'s `delete`-under-`mu` remains the sole arbiter of
  the verdict, so no TOCTOU here can produce a second delivery or an allow after a deny. (c) No
  new goroutines, and at most one `expire` in flight per entry because the re-arm follows the
  ask — the `Timer.Reset` concurrent-`f` hazard is structurally unreachable. (d) A panicking
  report kills the process on the timer goroutine; `recover()` is deliberately refused here
  because it would make a wiring bug read as "nobody can answer", and the guard is assigned to
  #1932's injection site. (e) Mid-extension shutdown converges on deny from both lanes:
  `watchApproveConn` denies on `s.closedCh`, and `ApprovalAnswerable`'s `ActiveConns` returns
  nil once the daemon context is cancelled, which reads as nobody connected.
- **[Threat model alignment] No findings.** The threats this primitive exists to hold are "a
  tool executes without a human decision" and "an allow lands after a deny." Neither is
  reachable: allow still requires a winning `Resolve` through the unchanged one-shot, and the
  extension path writes no verdict at all — it only declines to write one. The threat the change
  deliberately trades against is the inverse — "an approval is denied while a person is still
  deciding" — which is observed, not hypothetical (pyrycode-desktop#510). The residual, "an
  approval is never denied because the report always answers true," is bounded by the contract
  clause above and, in production, by the parked ∧ connected conjunction. `ArmModalTimeout` /
  `modalDenyTimeout` in `internal/relay/v2session_modal.go` is a separate dormant deny lane with
  no production caller; explicitly out of scope and not a competing deadline.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-09-01
