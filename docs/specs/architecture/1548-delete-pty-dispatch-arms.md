# #1548 — Delete `interruptRunner`'s SendEsc arm and `startFreshRunner`'s StartNewSession arm

**Size:** S (PO's estimate, re-verified — see § Size check).
**Packages touched:** `cmd/pyry` only.

## Files to read first

| Path | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/main.go` | `interruptArm`, `armInterrupt`, `armSendEsc`, `armNone` | The const block and its doc. Note the doc's operator-facing claim (#1193) and the **trailing comment on each constant** — those are three separate identifier-class sites in one block. |
| `cmd/pyry/main.go` | `interruptRunner` | The type switch and its doc. Four load-bearing claims to preserve; three false-by-arithmetic claims to rewrite (§ Comment sweep S2). |
| `cmd/pyry/main.go` | `activeInterrupter.SendEsc` | The **live** consumer: `arm, err := interruptRunner(r)` then the `arm == armNone` branch. Confirms the return contract this ticket must not change. This method survives untouched (AC2). |
| `cmd/pyry/main.go` | `startFreshRunner` | The type switch, the rotate-error `abort()` path, and the #1330 ordering paragraph. |
| `cmd/pyry/main.go` | `beginRotationOrNoop` | The optional-assertion shape this spec adopts for both dispatchers, and the stale justification naming two deleted stubs. |
| `cmd/pyry/streamsup_runner.go` | `streamRunner.Interrupt`, `streamRunner.RestartFresh`, `streamRunner.BeginRotation` | The three concrete methods reached by assertion. `BeginRotation`'s doc already states the gate-less-runner contract that re-grounds `beginRotationOrNoop`. |
| `cmd/pyry/streamsup_runner.go` | `var _ sessions.Runner = streamRunner{}` | Its doc comment. **Read it before trusting the ticket's description of it** — see § Stale premise. |
| `cmd/pyry/relay.go` | `startRelayV2` → the `V2SessionConfig` literal's `Interrupter:` and `SessionStarter:` fields | The two wiring comments. Both sit in one literal; the `ModalResolver:` comment three fields above belongs to **#1546** — do not touch it. |
| `cmd/pyry/inbound_deliver_rotation_test.go` | `baseRunner` | The stub the new test embeds. Seven trivial `sessions.Runner` methods, value receivers. |
| `cmd/pyry/inbound_deliver_rotation_test.go` | `rotatingRunner`, `TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild` | The existing coverage of `startFreshRunner`'s surviving `RestartFresh` arm — **do not duplicate it**. `rotatingRunner`'s own doc is the source for `beginRotationOrNoop`'s re-grounded justification. |
| `docs/knowledge/features/streamsup-package.md` | § dispatch case (search `new_session_routing_test.go`) | Documentation-phase-owned; read-only context on why the assertion is optional. |

## Context

`#1348` deleted `internal/supervisor` outright. Two type-switch arms in `cmd/pyry`'s
runner dispatchers existed only for `*supervisor.Supervisor` and are now **type-dead**:
no type in the module satisfies `interface{ SendEsc() error }` as a `sessions.Runner`,
and none satisfies `interface{ StartNewSession() error }` as one.

Verified at `main` `c13eeb9`:

- `armSendEsc` has exactly two references in the module — its own declaration and
  `interruptRunner`'s use of it. No test names it; the wire string `"send_esc"` appears
  nowhere else in `cmd/` or `internal/`.
- `interruptRunner` has one production call site (`activeInterrupter.SendEsc`) and **no
  test in the repo** — `interrupt_routing_test.go` went with #1348.
- `startFreshRunner` has one production call site (`activeSessionStarter.StartNewSession`)
  and one test driver (`rotatingRunner`), which reaches the `RestartFresh` arm.

No runtime failure. The cost is that a maintainer reasons about a documented
mutual-exclusion invariant against a type that does not exist, and that a future runner
accidentally growing `SendEsc` would silently take a path nothing exercises.

### Stale premise in the ticket body — read before editing `streamsup_runner.go`

The ticket says the `var _ sessions.Runner = streamRunner{}` comment "says it *mirrors*
`var _ Runner = (*supervisor.Supervisor)(nil) in internal/sessions/runner.go`". **That is
no longer what it says.** Commit `5372d78` (#1580) already corrected it; it now reads that
this is "the ONLY conformance assertion for the interface, not a mirror of one in
`internal/sessions`", and explicitly notes the assertion it used to point at never existed
after #1348.

The line still needs editing — AC4's post-check demands **zero** `supervisor.Supervisor`
hits in `streamsup_runner.go`, and the corrected text still quotes the dead declaration —
but the edit is "drop a historical aside", not "fix a false claim". Do not rewrite the
surrounding sentences as if they were wrong.

Independently verified: `git grep -n -F 'var _' -- internal/sessions/` returns only a
*comment* in `internal/sessions/runner.go`, no declaration. That comment is **#1515's**;
leave it alone. Do not fix both ends of the reference.

### Deferred: `docs/knowledge/features/v2-session-manager.md` (ticket AC5's second clause)

AC5 asks for a correction inside `docs/knowledge/features/v2-session-manager.md`. **That
file is owned by the documentation phase and is not a developer deliverable** — the
developer's worktree mutates `cmd/`, `internal/`, and this spec file only. It is recorded
here so the documentation phase lands it after code review.

The paragraph is the one whose bold lead-in is `**#1193 closes the invariant.**` (the AC's
"lines 973-979" is stale; it now sits ~35 lines lower). Two clauses need correction:

1. "the type switch that dispatches to `streamRunner.Interrupt()` or
   `*supervisor.Supervisor.SendEsc()`" → describe the single surviving actuation.
2. "the `interruptArm` it dispatched to (`armInterrupt` / `armSendEsc` / `armNone`)" →
   drop `armSendEsc`.

The rest of that file's `*supervisor.Supervisor` mentions describe the relay seam and the
historical record and belong to **#1515**.

### No ADR

This is a residue deletion under an already-recorded decision (#1121/#1125 dispatch
placement, #1077 un-widened `sessions.Runner`). Nothing new is decided.

## Design

### D1 — Dispatch shape: replace both type switches with an optional assertion

AC1 leaves the surviving shape to the architect. **Use an `if`-assertion with an early
return in both dispatchers, not a one-case type switch.**

Rationale, in order of weight:

1. **The package already spells "optional capability" this way.** `beginRotationOrNoop`,
   twenty lines below `startFreshRunner`, is exactly `if g, ok := r.(interface{ … }); ok`.
   With one arm left there is no dispatch — there is a capability check, and the file has
   an idiom for that.
2. **A one-case type switch reads as "more arms exist."** That false impression is the
   whole defect this ticket removes; preserving the switch preserves the invitation to
   re-add a case.
3. `startFreshRunner`'s body already nests an error branch inside its case. Early return
   flattens it one level.

Contract sketches (shape only — the bodies below are unchanged logic):

```go
func interruptRunner(r sessions.Runner) (interruptArm, error) {
	if v, ok := r.(interface{ Interrupt() error }); ok {
		return armInterrupt, v.Interrupt()
	}
	return armNone, nil
}
```

```go
func startFreshRunner(r sessions.Runner, oldID sessions.SessionID,
	rotate func(sessions.SessionID) (sessions.SessionID, error)) error {
	v, ok := r.(interface{ RestartFresh(string) })
	if !ok {
		return nil
	}
	abort := beginRotationOrNoop(r)
	// … unchanged: rotate(oldID); on error abort() and return err; else
	//     v.RestartFresh(string(newID)); return nil
}
```

**Load-bearing detail — and the one security-relevant hazard in this diff:**
`beginRotationOrNoop(r)` must stay **after** the `RestartFresh` check. Today the gate is
armed only inside the matched case. Hoisting it above the early return — the obvious
"reduce nesting" simplification once the switch becomes an `if` — would arm the rotation
gate for a runner that then rotates nothing, and the early `return nil` skips the `abort()`
disarm. An armed-with-no-disarm gate makes every subsequent `WriteUserTurn` on that
conversation return `ErrNoLiveChild` until the next respawn, and the arming is reachable
from a remote `new_session` frame — i.e. a wedge a paired phone could trigger against a
conversation. Keep the guard first. Row 4 of the test pins it: `rotate` must never be
called for an unrecognised runner.

Signatures are unchanged. `interruptRunner` still returns `(interruptArm, error)`; the
`arm == armNone` branch in `activeInterrupter.SendEsc` keeps working untouched.

### D2 — Constants

Delete `armSendEsc` and its trailing comment. `armInterrupt` and `armNone` keep their
exact wire strings `"interrupt"` and `"none"` (AC1) — they are operator-facing through the
`v2.interrupt.dispatched` record's `arm` field (#1193).

`armNone`'s trailing comment currently reads `// neither method — inert`. "Neither" is
false once one method remains; it becomes "no interrupt method" or equivalent. This is the
kind of site a function-name sweep misses — see § Comment sweep.

### D3 — Nothing else moves

`activeInterrupter.SendEsc`, `activeSessionStarter.StartNewSession`,
`internal/relay/v2session_seams.go`, `beginRotationOrNoop`'s *body*, the #1330 rotation
gate, and the modal keystroker seam (`modal_resolve_v2.go`, #1546) are all untouched.
`git diff --stat` must show no `modal_resolve_v2.go` and no `internal/relay/` entry.

## Comment sweep

Nine comments across three files. The ticket counts eight of them as "seven sites"
(it treats `relay.go`'s pair as one site) and adds the `var _` line separately; work from
this table, not from the count.

For each: **delete** the PTY clause, **preserve** the load-bearing claim verbatim or nearly
so, **re-ground** what the deletion orphans.

| # | Site | Delete | Must survive (AC4) |
|---|---|---|---|
| S1 | `interruptArm` const block, `main.go` | `armSendEsc` + its `// *supervisor.Supervisor.SendEsc()`; "neither method" in `armNone`'s trailing comment | "The constant VALUES are operator-facing … part of the record contract" (#1193). Consider qualifying the doc's bare "SendEsc" as `activeInterrupter.SendEsc` — with the other `SendEsc` gone, the reference is now ambiguous where it used to be clear. |
| S2 | `interruptRunner` doc, `main.go` | "the SendEsc-vs-Interrupt dispatch"; the whole "two arms are mutually exclusive … so the switch is unambiguous" sentence; "Interrupt is matched first so any future runner that grows both prefers the stream-json `control_request` over a PTY Esc" | (a) "An unknown runner is inert (nil) — no actuation beats wrong actuation"; (b) the return contract — arm + the chosen method's error, "armNone always pairs with a nil error"; (c) "The dispatcher itself stays pure: no logger, no ambient state"; (d) **the #678 paragraph in full** — "the arm is an OBSERVABILITY value and nothing may branch on it beyond selecting a record — in particular `armNone` must NOT trigger a fallback actuation …". |
| S3 | `startFreshRunner` doc, `main.go` | "The `*supervisor.Supervisor` arm keeps today's PTY behavior: type `/clear` and let the fsnotify watcher drive the pool-side rotation (…)"; "RestartFresh is matched first so any future runner that grows both prefers the direct stream-json path" | **The #1330 ordering paragraphs in full** — both "Ordering is load-bearing: rotate() completes … BEFORE RestartFresh spawns" and "That order is PRESERVED at #1330 …". Also "an unknown runner is inert (nil) — no actuation beats wrong actuation (#1121)". |
| S4 | `beginRotationOrNoop` doc, `main.go` | "Widening it would silently re-route `restartFreshStub` and `bothMethodsStub` (`new_session_routing_test.go`) to the inert default arm, turning existing subtests from assertions into vacuities without a single failure"; "and would change the documented 'RestartFresh is matched first' dispatch contract" (that contract no longer exists after S3) | "An OPTIONAL assertion, deliberately, rather than widening `startFreshRunner`'s case to `interface{ RestartFresh(string); BeginRotation() func() }`"; "Treating the gate as a CAPABILITY is also how Interrupt and RestartFresh are already treated one layer up" (still true — both are now `if`-assertions). Re-grounding: see § The `beginRotationOrNoop` trap. |
| S5 | `relay.go`, `Interrupter:` wiring comment | "then dispatches SendEsc (PTY) or Interrupt (stream-json, #1120) by runner type" → keep only the Interrupt half, conditioned on the runner exposing it | "it satisfies `Interrupter` via `SendEsc`, the seam's abstract 'claude's own interrupt' name" — **this `SendEsc` is `activeInterrupter.SendEsc`, which AC2 requires to survive.** Do not delete it as PTY residue. |
| S6 | `relay.go`, `SessionStarter:` wiring comment | ", while a `*supervisor.Supervisor` keeps the `/clear` path the watcher rotates (#830)" | "for a `*streamsup.Runner` rotates the pool-side id (`Pool.RotateForNewSession`) and RestartFreshes into `--session-id <newID>` with NO `/clear`". |
| S7 | `streamRunner.Interrupt` doc | ", exactly as `(*supervisor.Supervisor).SendEsc` is reached for the PTY runner" | The un-widened-interface rationale (#1077) and "the #1121 interrupt dispatch (`interruptRunner` in `main.go`) reaches by type assertion". |
| S8 | `streamRunner.RestartFresh` doc | ", mirroring how the PTY runner's `(*supervisor.Supervisor).StartNewSession` is reached for `/clear`" | Same #1077 rationale, and the `#1125` dispatch reference. |
| S9 | `var _ sessions.Runner = streamRunner{}` doc | the quoted `var _ Runner = (*supervisor.Supervisor)(nil)` and the "never existed after #1348" aside | "This is the ONLY conformance assertion for the interface"; "`internal/sessions` declares no assertion of its own because the sole production implementation lives here." Read § Stale premise first. |

### Two false-by-arithmetic claims a clause-level sweep will miss

Both live in `interruptRunner`'s doc (S2) and neither contains the string
`supervisor.Supervisor`, so AC4's mechanical post-check will not catch them:

- **"the only package that sees both concrete runner types"** — there is one concrete
  runner type now. Re-ground it: this stays in `cmd/pyry` because `Interrupt` is off
  `sessions.Runner` (#1077) and the `streamRunner` adapter lives here, so `internal/sessions`
  cannot reach the method at all.
- **"the only other runner to try is the bootstrap supervisor"** — "bootstrap supervisor"
  here means the bootstrap *session's runner*, not the deleted package. The #678 claim
  must survive (AC4), so **do not delete this sentence**; tightening the noun to "the
  bootstrap session's runner" removes the last reading of "supervisor" as the dead package.

### The `beginRotationOrNoop` trap

The ticket suggests re-grounding S4 as "widening … would route a gate-less `RestartFresh`
runner to the inert arm — the shape `rotatingRunner` deliberately offers as optional."
**Do not write that `rotatingRunner` is a gate-less runner.** It declares both
`RestartFresh` and `BeginRotation`; its own doc says "It exposes both unconditionally.
Whether the gate is ARMED is the dispatch's decision, not the fixture's." Naming it as the
stub that widening would re-route would replace one false comment with another.

Ground the justification in the **contract**, which is true independent of which stubs
exist:

- Widening the case to require both methods would send a runner that has `RestartFresh`
  but no gate to the inert arm — it would rotate *nothing*, where today it rotates without
  a gate. `streamRunner.BeginRotation`'s doc already states this ("a runner that exposes
  `RestartFresh` without a gate keeps today's dispatch exactly"); point at it.
- `rotatingRunner` is the evidence that the *optionality* is load-bearing to the existing
  test, not evidence of a gate-less runner: it offers the gate and lets the real
  `startFreshRunner` decide, which is what keeps
  `TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild` a real question
  rather than true by construction.

### Adjacent residue — leave it

`activeInterrupter.SendEsc`'s doc contains "logging a wrapped supervisor/streamsup sentinel
twice". `internal/supervisor` is gone, so the noun is stale — but it is not one of AC4's
enumerated sites, not a `supervisor.Supervisor` hit, and not this ticket's. Leave it
exactly as it is. Same for `interactive_turn_v2.go`'s "without a real
`*supervisor.Supervisor`" note (unowned residue, per the ticket's scope caution).

## Testing strategy

One new file, `cmd/pyry/dispatch_arms_test.go` (name is the developer's call; keep it out
of `inbound_deliver_rotation_test.go`, which is #1330's).

### Stubs

Three new stubs, each embedding `baseRunner` from `inbound_deliver_rotation_test.go` (AC3:
embed it, do not add a second base). Pointer receivers so the call counters mutate; pass
`&stub{}`. Row 3's "exposes neither" runner is a bare `baseRunner{}` — no new type.

- `interruptOnlyRunner` — `Interrupt() error` returns a package-level sentinel.
- `sendEscOnlyRunner` — `SendEsc() error` increments a counter **and returns a distinct
  sentinel**.
- `startNewSessionOnlyRunner` — `StartNewSession() error` increments a counter **and
  returns a distinct sentinel**.

**Why the never-called stubs return a non-nil sentinel rather than `nil`:** the row asserts
`err == nil`. If the stub returned `nil`, a mutation that calls the dead method but still
reports `armNone` would leave the error assertion green and only the counter would catch
it. A sentinel makes the counter and the error independently discriminating, at the cost of
one `errors.New`.

### Rows

Bullet-pointed scenarios; write them in the package's table-driven idiom, or as separate
`t.Run` subtests if the two dispatchers' signatures make one table awkward (they do —
`startFreshRunner` takes three arguments; two small tables is fine).

**`interruptRunner`:**

1. `&interruptOnlyRunner{}` → arm is `armInterrupt`; the returned error `errors.Is` the
   stub's sentinel; `string(armInterrupt) == "interrupt"`.
2. `&sendEscOnlyRunner{}` → arm is `armNone`, error is `nil`, **and the `SendEsc` counter
   is still 0**. Compare the arm against the constant `armNone`, not the literal `"none"`.
3. `baseRunner{}` → arm is `armNone`, error is `nil`, and `string(armNone) == "none"`.

**`startFreshRunner`:**

4. `&startNewSessionOnlyRunner{}`, with a `rotate` func that increments its own counter and
   returns a fresh id → returns `nil`; the `StartNewSession` counter is 0; the `rotate`
   counter is 0.

Do **not** add a fifth row for the surviving `RestartFresh` arm.
`TestInboundDeliver_RotationInProductionOrder_DeliversToFreshChild` already drives the real
`startFreshRunner` through it end-to-end, including the #1330 gate ordering.

### Sole-red mapping

Each row is the sole red for at least one distinct mutation. Verify with
`go test -overlay=<abs-path json>` so no mutation is written into the worktree.

| Mutation | Sole red |
|---|---|
| Re-add `case interface{ SendEsc() error }: return armSendEsc, v.SendEsc()` to `interruptRunner` | Row 2 (arm **and** counter) |
| Re-add `case interface{ StartNewSession() error }` to `startFreshRunner` | Row 4 (counter) |
| `armInterrupt`'s wire string changed from `"interrupt"` | Row 1 |
| `armNone`'s wire string changed from `"none"` | Row 3 (row 2 compares against the constant, so it stays green) |
| `interruptRunner` swallows the error: `return armInterrupt, nil` | Row 1 |

Rows 2 and 3 both redden if the fall-through's *arm* changes; that is fine — the
requirement is that each row is sole-red for *some* distinct mutation, and each is.

**Red before / green after:** rows 2 and 4 fail on an unmodified tree (today the dead arms
match, so the arm is `armSendEsc` and both counters reach 1) and pass after. They are the
regression pins AC3 asks for. Rows 1 and 3 are green both before and after — they pin the
wire strings AC1 freezes.

## Error handling

No new failure modes. Both dispatchers keep their existing contracts:

- `interruptRunner` returns `(armNone, nil)` for an unrecognised runner — inert, never a
  fallback actuation (#678).
- `startFreshRunner` returns `nil` for an unrecognised runner and never calls `rotate`, so
  no pool-side id is minted for a runner that cannot consume it.
- The `rotate`-error path keeps its `abort()` disarm and its generation-stamped semantics
  (#1330) unchanged.

## Concurrency model

Unchanged. Both dispatchers are synchronous, called on the relay's inbound-frame path via
`activeInterrupter.SendEsc` / `activeSessionStarter.StartNewSession`. No goroutines, no
locks, no shutdown sequence in this diff. The `beginRotationOrNoop` → `rotate` →
`RestartFresh` ordering that *does* carry a concurrency invariant is preserved byte-for-byte
in behaviour (§ D1).

## Verification

```
make check                       # includes vet, -race unit tests, staticcheck, cite-guard
go vet -tags e2e_realclaude ./...
git grep -c -F 'supervisor.Supervisor' -- cmd/pyry/     # 12 → ≤5
git diff --stat                  # no modal_resolve_v2.go, no internal/relay/
go test ./internal/relay/        # passes with zero edits to that package
```

The `supervisor.Supervisor` survivors must be exactly: two in `relay.go` (the `streamSink`
/ `w.sup` mode comment → #1515, and the `ModalResolver` keystroker comment → #1546), two in
`modal_resolve_v2.go` (→ #1546), one in `interactive_turn_v2.go` (unowned residue). Fewer
only if a sibling landed first. Zero in `main.go` and `streamsup_runner.go`.

**cite-guard:** every rewritten comment cites *symbols*, never `file.go:NNN` and never a
bare `:NNN`. The existing text already follows this; keep it that way. `cite-guard` is
diff-scoped, so only lines this branch adds or modifies are checked — but every line in
§ Comment sweep is such a line.

**Do not treat a green gate as a sweep.** `cite-guard` flags `file.go:NNN` citations only;
the residue here is bare filenames (`new_session_routing_test.go`) and bare symbol names
(`restartFreshStub`, `bothMethodsStub`). Grep for those four names in `cmd/pyry/` after the
edits — all must be gone.

### Deterministic survival check for the three preserved invariants

AC4 names three claims that must survive the rewrite. "Preserve this paragraph" is prose
guidance and prose guidance is stochastic, so pair it with a count that either holds or
does not. Measured at `c13eeb9`, before any edit:

```
git grep -c -F '#678'  -- cmd/pyry/main.go   # 9
git grep -c -F '#1330' -- cmd/pyry/main.go   # 2
git grep -c -F '#1193' -- cmd/pyry/main.go   # 4
```

**None of these may decrease.** No clause marked for deletion in § Comment sweep carries
any of the three references — the deleted text cites `#1121`, `#1125`, `#830` and `#1077`
instead — so a drop means a load-bearing paragraph went out with the PTY prose. `#1121`
counts are deliberately *not* frozen: S2's placement rationale is genuinely reworded.

## Open questions

None blocking. Two judgement calls left to implementation:

- Whether S2's "bootstrap supervisor" is tightened to "the bootstrap session's runner". The
  #678 claim must survive either way (AC4); the tightening is recommended, not required.
- The new test file's name and whether the two dispatchers share one table. Both are
  cosmetic.

## Size check

Re-applied against this written spec, not the sketch:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created or modified | ≤ 3 | 3 — `main.go`, `relay.go`, `streamsup_runner.go` |
| Total written work | ≤ 400 | ≈ 260 — ~8 lines of code deleted, ~65 lines of comment rewritten across nine sites, ~190 lines of new test incl. house-style docs |
| New exported types or interfaces | ≤ 5 | 0 |
| Consumer call sites needing simultaneous update | ≤ 10 | 0 — `armSendEsc` has two references, both inside the deleted block; both dispatchers keep their signatures, so their three call sites are untouched |
| Acceptance criteria | ≤ 5 | 5 |
| Distinct error/reject branches in a state machine | ≤ 10 | 2 per dispatcher |

No boundary exceeded; ships as one `size:s` ticket. The edit fan-out check does not apply —
nothing is renamed and no signature changes, so there is no consumer cascade.

**File-overlap check:** `git fetch origin --prune` then a scan of every `origin/feature/N`
branch's diff against `origin/main` for `cmd/pyry/main.go`, `relay.go`,
`streamsup_runner.go`, `inbound_deliver_rotation_test.go` — **no overlap**. No blocker set.

## Security review

**Verdict:** PASS (one MUST FIX found and fixed inline before this section was written —
see Concurrency).

**Findings:**

- **[Trust boundaries]** No findings, and the reason is structural rather than a check:
  **no untrusted value reaches either dispatcher.** `interruptRunner` takes a
  `sessions.Runner` resolved from the daemon's own registry; `startFreshRunner` takes that
  plus a `sessions.SessionID` read out of `conversations.Registry` and a daemon-supplied
  `rotate` func. The wire-supplied `convID` is consumed one frame earlier, by
  `activeInterrupter.SendEsc` / `activeSessionStarter.StartNewSession`, and never crosses
  into these functions. The boundary this diff must not move is `resolveBoundRunner` /
  `resolveBoundSession`'s `CurrentSessionID == ""` guard (#678) — untouched, upstream, and
  its in-code statement is on AC4's must-survive list.
- **[Concurrency]** **MUST FIX, fixed inline.** § D1 originally stated the
  `beginRotationOrNoop`-after-the-guard ordering as a correctness note without its
  consequence. Hoisting the gate arming above the early return — a plausible
  reduce-nesting move once the type switch becomes an `if` — arms the #1330 rotation gate
  on a path whose `return nil` skips the `abort()` disarm, wedging every subsequent
  `WriteUserTurn` on that conversation until the next respawn. It is reachable from a
  remote `new_session` frame. § D1 now names the DoS and Row 4 of the test pins it
  (`rotate` never called for an unrecognised runner). Beyond that: neither dispatcher takes
  a lock, and the #1330 skip-set-published-under-`Pool.mu` happens-before is preserved
  verbatim by AC4.
- **[Error messages, logs, telemetry]** No findings, but this is the category the ticket
  actually perturbs: deleting `armSendEsc` removes a value from the operator-facing
  `v2.interrupt.dispatched` record's `arm` field (#1193). Verified `git grep 'send_esc' --
  cmd/ internal/` returns exactly one hit — the constant itself — so nothing in the module
  parses the value and no mobile wire contract in `internal/protocol` carries it. No
  observability is lost: a runner that exposed `SendEsc` could never be constructed, and a
  hypothetical future one now lands on `v2.interrupt.no_actuator` at `Info` rather than
  vanishing, so #1193's "every path emits" invariant survives. The surviving `arm` values
  are two compile-time constants, never attacker-influenced, so the record cannot be made
  to carry injected text.
- **[Subprocess / external command execution]** No findings. The surviving `RestartFresh`
  arm does eventually respawn `claude` with `--session-id <newID>`, but `newID` is minted
  by `Pool.RotateForNewSession` from daemon state; this diff introduces no new value into
  any argv and changes no `exec.Command` construction. No `sh -c` anywhere on the path.
- **[Tokens, secrets, credentials]** Not applicable, by design: no code path in this diff
  generates, stores, reads, logs, or rotates a credential. `interruptArm` values are fixed
  string constants, not derived material.
- **[File operations]** Not applicable: the diff constructs, opens, and writes no path.
  The one on-disk effect anywhere downstream is the session-registry rotation inside
  `Pool.RotateForNewSession`, which is unchanged and is not called on any new path.
- **[Cryptographic primitives]** Not applicable: no primitive, RNG, key, nonce, or
  comparison is touched.
- **[Network & I/O]** Not applicable to the diff — no socket read, size cap, or timeout is
  added or moved, and AC2 requires `internal/relay` to compile and pass with zero edits.
  Noted as **pre-existing, not introduced**: inbound `interrupt` / `new_session` frames
  from a paired interactive phone are not rate-limited, so repeated `new_session` frames
  force repeated rotations. This diff strictly narrows what such a frame can actuate (one
  arm instead of two) and so cannot worsen it; a rate limit belongs to a relay-side ticket,
  not here.
- **[Threat model alignment]** The relevant threat is #678 cross-conversation isolation —
  a remote frame must never actuate a child other than the active conversation's bound
  one. Enforcement is upstream and untouched. The residual risk this pass identified is
  *documentation* rather than code: a developer trimming "PTY residue" could delete the
  paragraph that states the threat in code. Mitigated with different fabric than the prose
  instruction — § Verification freezes `git grep -c -F '#678' -- cmd/pyry/main.go` at 9,
  alongside `#1330` at 2 and `#1193` at 4.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
