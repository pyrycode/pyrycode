# #1547 — Delete the never-assigned `streamCleanup` / `modalStreamCleanup` in `startRelayV2`

**Size:** XS (confirmed — see § Size re-check). **Files touched:** `cmd/pyry/relay.go`, and nothing else.
**Security review:** not run — #1547 carries `size:xs`, `done:po`, `wip:architect`; no `security-sensitive` label.

---

## Files to read first

All of these are in `cmd/pyry`. Resolve every symbol with `codegraph_search` / `codegraph_node` — the line
numbers in the ticket body were measured at `main` 2e33ffe and the file has since drifted by roughly ninety
lines, so **do not navigate by them**.

| Where | Symbol | What to extract |
|---|---|---|
| `cmd/pyry/relay.go` | `startRelayV2` | The whole function. It is the only file the diff may touch. Four comment blocks inside it and one `var` block are the work. |
| `cmd/pyry/relay.go` | `relayWiring` | The **live** field list. `streamSink`, `approvals`, `claudeSessionsDir`, `active`, `busy` exist; **`sup` and `bridge` do not** — that matters for site 3 below. |
| `cmd/pyry/relay.go` | `startStreamTurnDrainV2` call inside `startRelayV2` | The sole assigner of `streamDrainCleanup`, sitting under the `w.streamSink != nil` guard. This is why AC2's nil-guard stays. |
| `cmd/pyry/stream_turn_drain.go` | `startStreamTurnDrainV2` | Its doc comment already states the true contract ("cleanup blocks until the goroutine exits"). Mirror that vocabulary when rewriting site 2. **Its own body carries residue mentions of `startInteractiveTurnStreamV2` — out of scope, leave them.** |
| `cmd/pyry/modal_resolve_v2.go` | `streamApprovalBridge.Surface` | The **only live producer** of `modalReg` entries (it calls `modalbridge.Registry.Record`). Site 1's rewrite names this instead of the deleted modal stream. |
| `cmd/pyry/modal_resolve_v2.go` | `newStreamApprovalBridge` | Constructed inside `startRelayV2` **after** `modalReg` and before `mgr.Run` — so a forward "below" reference retargeted at it is still accurate. |
| `cmd/pyry/relay.go` | `newModalResolverV2` call + the `OutstandingModals` field of the `V2SessionConfig` literal | The two live **consumers** of `modalReg` (inbound resolve / deny-on-timeout, and reconnect replay via `modalReg.Snapshot`). Site 1 must describe producer→consumer truthfully. |
| `CODING-STYLE.md` | § "Comments — Citing Other Code" | The citation rule the rewritten comments are graded against. `make cite-guard` runs inside `make check` (see `Makefile`'s `check` target). |
| `docs/knowledge/features/relay-package.md` | § dispatch / reconnect-state accessors | Confirms `startRelayV2` is the sole v2 composition root. It names **neither** deleted starter and **neither** deleted variable — verified against a control, so no evergreen doc is owed an update. |

---

## Context

`startRelayV2` declares three cleanup funcs in one `var` block. Only `streamDrainCleanup` is ever assigned;
the PTY arm that assigned `streamCleanup` and `modalStreamCleanup` was deleted by #1348, and the comment
immediately below the surviving branch already records that ("The terminal-mode arm that stood here is gone
with #1348"). The two survivors are permanently nil, and their two nil-guarded calls in the returned drain
closure are permanently skipped. No runtime consequence; the cost is entirely readability, and it is
multiplied by four comment blocks in the same function that describe the deleted arm as though it still
exists.

**Every premise in the ticket body was re-verified at `feature/1547`'s base (`21bf534`), with a control on
each sweep.** Results:

- **Confirmed.** Neither `startInteractiveTurnStreamV2` nor `startInteractiveModalStreamV2` has a Go
  declaration anywhere in the repo. The only `func start…` declarations of those names live in
  `docs/specs/architecture/633-*.md` and `798-*.md`, which are historical build artifacts and correctly stay
  untouched. Control: `func startStreamTurnDrainV2` resolves in `cmd/pyry/stream_turn_drain.go`, so the
  declaration regex was working.
- **Confirmed.** `startRelayV2` has exactly one caller, `startRelay` in the same file, and **no test calls
  it.** `cmd/pyry/snapshot_usage_test.go`'s own comment says so in as many words. AC-level consequence: the
  guarantee this ticket ships is structural (the compiler proves the variables are gone) plus review of the
  four-call teardown order. Do **not** stand up a first-ever `startRelayV2` harness.
- **Confirmed.** The evergreen tier (`docs/knowledge/features/`, `architecture/`, `decisions/`) has zero hits
  for either variable name. Control: the same recursive sweep for `startRelayV2` hit seven files. No evergreen
  update is owed, and the documentation phase — not this ticket — owns that tier regardless.
- **Correction to the ticket body (does not change scope).** The body says "the same **two** dead starter
  names survive in four comments in three other files." Only `startInteractiveTurnStreamV2` survives outside
  `relay.go` — five mentions across `cmd/pyry/stream_turn_drain.go`, `cmd/pyry/interactive_turn_v2.go`, and
  `cmd/pyry/main.go`. `startInteractiveModalStreamV2` has **exactly two mentions repo-wide in Go code, both
  inside `startRelayV2`**, so this ticket removes that name from the Go tree entirely. Those five residue
  mentions stay out of scope, per the one-file discipline.
- **No blocking overlap.** `git fetch origin --prune` then a scan of all 68 `origin/feature/<N>` branches for
  ones touching `cmd/pyry/relay.go` returned nothing. In particular **`origin/feature/1544` does not exist** —
  #1544 is OPEN and `size:s` but has never been dispatched, so there is no branch to conflict with and no
  `blockedBy` edge to set. If #1544 dispatches later it rebases onto this, exactly as its body predicts.

**This work deserves no ADR.** It removes provably-dead wiring and corrects prose; there is no decision to
record.

### Two additional falsehoods inside the in-scope sites, not named in the ticket's table

The ticket's table lists three claims. Sweeping the four sites turned up two more, and both sit **inside**
site 3, so AC3's "each read as true of the code that exists" already covers them. Calling them out so they are
not left behind:

1. Site 3 justifies the gate with *"both dereference the typed-nil bootstrap `w.sup`"* and then says the
   branch *"does NOT gate on `w.bridge` / claudeSessionsDir"*. **`relayWiring` has neither a `sup` field nor a
   `bridge` field.** Both names are dead. (`claudeSessionsDir` is live and still used elsewhere in the
   function.)
2. Site 3 cites *"the PTY path (`interactive_turn_stream_v2.go`)"* and closes with *"The branches are mutually
   exclusive, so `SetReplaySource` runs at most once."* That file was deleted by #1348, and there is now only
   one branch — there is nothing for it to be mutually exclusive with.

Out-of-scope by the same one-file, four-site rule: `w.sup` also appears in **four other comments** in
`relay.go` outside the named sites. That is a distinct residue class (dead `relayWiring` field names in
prose), it is not what AC3's sweep targets, and widening to it would push this past XS. Left for a follow-up —
see § Open questions.

---

## Design

There is no new structure. Three mechanical deletions plus four prose rewrites, all inside `startRelayV2`.

### D1 — The `var` block collapses to one declaration

`startRelayV2`'s three-name cleanup `var` block becomes a single `var streamDrainCleanup func()`. Keep it a
plain `var` — the name is assigned inside the `if w.streamSink != nil` branch and read in the returned
closure, so it must outlive the branch, and `:=` inside the branch would shadow it into uselessness.

### D2 — The returned drain closure loses two guards, keeps one

Inside the closure `startRelayV2` returns, delete the `if streamCleanup != nil { … }` and
`if modalStreamCleanup != nil { … }` blocks. The remaining body runs, **in this exact order**:

1. `streamDrainCleanup()` — still wrapped in `if streamDrainCleanup != nil`, because `w.streamSink == nil`
   (foreground / v1) leaves it unassigned. This guard is load-bearing; deleting it is a nil-func panic on the
   foreground path.
2. `streamTransitionsCleanup()`
3. `streamQueueStateCleanup()`
4. `streamSessionErrCleanup()`
5. `<-mgrDone`

Producers stop before the manager is waited on, so no fan-out races a winding-down manager. That ordering is
the behavioural contract AC4 pins, and it is unchanged by this ticket — the only change is that two no-op
guards no longer sit in front of step 1.

### D3 — The four prose sites

Each site is identified by its enclosing symbol and an anchor description, never by line number. Rewrite each
so it describes the code that exists; none of them needs to grow, and three of them should shrink
substantially.

**Site 1 — the doc block above `modalReg := modalbridge.New()` in `startRelayV2`.**
Currently claims the interactive modal stream (`startInteractiveModalStreamV2`, "below") constructs the
surfacer over this instance. True replacement facts:

- `modalReg` is still the daemon-singleton outstanding-modal registry — that half of the sentence is fine.
- Its sole live producer is `streamApprovalBridge.Surface`, built by `newStreamApprovalBridge` further down
  this same function (#1080). The "below" direction survives; only the target changes.
- Its consumers are unchanged and already named correctly: the inbound resolver seam built by
  `newModalResolverV2` (including deny-on-timeout), and reconnect replay through the `OutstandingModals`
  field, which is `modalReg.Snapshot`.

**Site 2 — the wiring comment immediately above the cleanup `var` block.**
Currently describes two producers "inside one shared PTY gate", following the active conversation over
`resolveTarget` + `NewTargetSubscriber`. Every load-bearing claim is false: there is one producer, no PTY
gate, and **neither `resolveTarget` nor `NewTargetSubscriber` has a Go declaration anywhere in the repo** —
this comment is their last mention in any `.go` file. The replacement says what is actually wired: a single
turn-stream producer, `startStreamTurnDrainV2`, gated on `w.streamSink != nil`, whose cleanup blocks until its
goroutine exits.

> **Note for whoever sequences #1544.** Removing `resolveTarget` / `NewTargetSubscriber` here is not scope
> creep — it is forced by AC3's "reads as true" requirement, since both names are dead. A side effect is that
> #1544's `relay.go` row becomes a no-op. #1544's own sweep still passes; it just has nothing left to do in
> this file.

**Site 3 — the `STREAM MODE (#1081)` block at the top of the `if w.streamSink != nil` branch.**
The largest rewrite, and the one with the most falsehoods per line. Facts to keep, because they are still
true and still worth saying:

- The branch does not gate on a sessions dir or a PTY bridge — the drain consumes parsed `turnevent.Event`s
  from the sink, not an on-disk transcript, so it runs whenever stream mode is selected.
- The stream-mode analogue of the old PTY modal stream is the #1080 approval bridge, which is wired
  unconditionally earlier in this function via `modalResolver.streamApprovals`.
- The emitter is built by `newInteractiveTurnEmitterV2` and `SetReplaySource` is installed once here.

Facts to delete, because they are false: the "both PTY interactive streams are gated OFF" framing and the two
starter names; the typed-nil `w.sup` rationale (no such field); the `w.bridge` mention (no such field); the
`interactive_turn_stream_v2.go` reference (no such file); and "the branches are mutually exclusive" (there is
one branch). `SetReplaySource` still runs at most once — but because the branch runs at most once, not
because of an exclusivity that no longer exists.

**Site 4 — the teardown comment at the top of the returned closure.**
Currently enumerates "the structured turn stream, the modal stream, the session-transition producer, the
queue_state producer, and the session_error producer". Drop the modal stream from the list and name the turn
stream by the producer that actually feeds it. The two sentences after the list — each cleanup waits for its
goroutine on ctx-cancel, then the manager's `Run` is awaited on the closed `Frames` channel — are true and
stay.

### D4 — Contract sketch of the `var` block

```go
// before
var (
    streamCleanup      func()
    modalStreamCleanup func()
    streamDrainCleanup func()
)

// after
var streamDrainCleanup func()
```

That is the only code shape this spec prescribes. Everything else in the diff is a deletion of two `if`
blocks and four comment rewrites, whose wording is the developer's to choose within the constraints above.

---

## Concurrency model

**Unchanged, and the diff must not change it.** `startRelayV2` starts the manager on one goroutine
(`mgrDone` closes on its exit) and each producer owns a goroutine its cleanup joins. The returned drain runs
after the caller has cancelled `ctx` and `Close()`d `conn`: it stops every producer first, then blocks on
`<-mgrDone`.

The two deleted guards contribute nothing to this — both conditions are statically false, so removing them
cannot reorder or skip a join. The one guard that survives, `streamDrainCleanup != nil`, is genuinely dynamic
(foreground / v1 has no `streamSink`) and must survive verbatim.

## Error handling

No error path is touched. `startRelayV2`'s only error returns are the static-key load and the manager build,
both far upstream of the edited region; the drain closure returns nothing and cannot fail.

## Testing strategy

**No test is added and no test file is modified.** This is a deliberate decision, not an omission, and it is
what AC4's "no test file is added or modified" pins.

- The **compiler** is the proof for AC1 and AC2. Two unreferenced local variables cannot survive a build if
  their last references are deleted, and Go rejects an unused local outright, so a half-done deletion cannot
  compile.
- **Review** is the proof for AC4's ordering claim. Diff the returned closure and read the five steps.
- The **sweep** in AC3 is run by hand against the file and is already non-vacuous by construction: the control
  `startStreamTurnDrainV2` is a live symbol in `relay.go` (it is the sole assigner of `streamDrainCleanup`),
  so a sweep tool that returns nothing for it is broken rather than clean.

Run the sweep with a tool that actually honours word boundaries. `git grep -E '\bfoo\b'` matches **nothing**
— POSIX ERE has no `\b`, and the empty result reads exactly like clean absence. Use `git grep -P`, or a plain
fixed-string `git grep -F` (these names have no substring collisions), or the editor's own search. `git grep`
is also untracked-blind by construction, which is the clean way to satisfy the ticket's `.claude/worktrees/`
caution — no exclusion pathspec needed, because the leftover worktree copy is untracked.

**Gate: `make check` (AC5).** Two of its stages are the ones that can bite here:

- `cite-guard` — the rewritten comment lines are *added* lines, so the diff-scoped guard checks them. Do not
  write `file.go:NNN` in any of the four blocks, do not write a range, and never write a bare `:NNN`. Name
  symbols. `#1081`-style ticket references are not citations and are fine to keep.
- `staticcheck` — a leftover reference to a deleted variable, or a `var` block that keeps a name nothing
  reads, fails here even if `go vet` is happy.

`make check` compiles `cmd/pyry` with no build tag, which is the whole of what this ticket needs; the
live-claude suite cannot import `package main` and has nothing to say about it.

---

## Open questions

- **The `w.sup` / `w.bridge` residue outside the four sites.** Four comments in `relay.go` still name `w.sup`,
  a `relayWiring` field that does not exist. It is a different residue class from the two starter names, it is
  not in AC3's sweep, and #1544's sweep does not cover it either — so after both tickets land it is
  **unowned**. Recommend a separate XS ticket: sweep `cmd/pyry` comments for `relayWiring` field names that no
  longer resolve. Not this ticket.
- **The five `startInteractiveTurnStreamV2` residue mentions** in `stream_turn_drain.go`,
  `interactive_turn_v2.go`, and `main.go`. Explicitly out of scope here (one-file discipline). The ticket body
  recommends #1544 absorb them; that recommendation stands and is noted on #1544.
- **`interactive_turn_stream_v2.go` as a filename** also survives in comments in `cmd/pyry/snapshot_usage.go`,
  `internal/e2e/internal/fakeclaude/main.go` (twice), and `internal/sessions/reconcile.go`. Same class as the
  above, same recommendation, out of scope here.

---

## Size re-check (§ 4 gate, re-applied to this written spec)

| Boundary | Limit | This spec | |
|---|---|---|---|
| Production source files created or modified | ≤ 3 | 1 (`cmd/pyry/relay.go`) | ✅ |
| Total written work (production + tests + helpers + per-branch logs + spec-doc edits) | ≤ 400 | ≈ 60 (net −10 code, ~45 comment lines rewritten, 0 test, 0 helper, 0 log call) | ✅ |
| New exported types or interfaces | ≤ 5 | 0 | ✅ |
| Consumer call sites needing simultaneous update | ≤ 10 | 0 (nothing outside `startRelayV2` references either name) | ✅ |
| Acceptance criteria | ≤ 5 | 5 | ✅ |
| Distinct error/reject branches in a state machine | ≤ 10 | 0 | ✅ |

Ships as one XS ticket. PO's `size:xs` stands; no downward override available and none needed.
