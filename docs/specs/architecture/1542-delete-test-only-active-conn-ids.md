# #1542 — delete test-only `ActiveConnIDs`, retarget its coverage onto `ActiveConns`

**Ticket:** [#1542](https://github.com/pyrycode/pyrycode/issues/1542) · `size:s` · `security-sensitive`
**Split from:** #1521

---

## Files to read first

| Read | Symbol | What to extract |
|---|---|---|
| `internal/relay/v2session.go` | `ActiveConnIDs` | the deletion target: decl + doc comment. Note the nil-guard's stated reason. |
| `internal/relay/v2session.go` | `ActiveConns` | the survivor. Two `select`s: the **enqueue** one (`m.snapshot <- req`) and the **reply** one. AC4 mutates the enqueue arm only. |
| `internal/relay/v2session.go` | `handleActiveConns` | the `s.state == V2StateOpen` security gate, and the `make([]ActiveConn, 0, len(m.sessions))` that makes an empty snapshot **non-nil**. This is why the deleted nil-guard is redundant. |
| `internal/relay/v2session.go` | `snapshotReq` | doc comment names `ActiveConnIDs` — needs a clause dropped. |
| `internal/relay/v2session.go` | `V2SessionManager` | the `snapshot` field's doc comment names `ActiveConnIDs` — needs a parenthetical dropped. |
| `internal/relay/v2session.go` | `ActiveConn` | the struct the retargeted assertions read `ConnID` off. |
| `internal/relay/v2session_test.go` | `TestV2Session_ActiveConnIDs_OpenOnly`, `..._TornDownSessionAbsent`, `..._ConcurrentWithDispatch_RaceClean`, `..._EmptyManager`, `..._CtxCancelled_ReturnsNil` | the five tests to rename + retarget. Read each doc comment — the rationale is what must survive, not the call. |
| `internal/relay/v2session_test.go` | `TestV2Session_ActiveConns_MixedInteractive` | the flag assertion that stays **byte-identical**; the projection assertion below it that goes. |
| `internal/relay/v2session_test.go` | `TestV2Session_IdleChurn_ReturnsToBaseline` | two call sites to rewrite onto `ActiveConns`. |
| `internal/relay/v2session_test.go` | `TestV2Session_Push_ByteCeilingTearsDownSession` | **the fourth out-of-section site, new since the ticket was written.** One `for _, id := range` loop. |
| `internal/relay/v2session_test.go` | `buildMessageEnvelope` | doc comment promises a `#572` bridge that will never land. |
| `internal/relay/v2session_test.go` | `TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated` | the token-fail security proof. Already asserts through `ActiveConns` — **do not touch it**; read it to confirm this ticket cannot weaken it. |
| `cmd/pyry/interactive_turn_v2.go` | `interactiveBroadcaster` | doc comment names both `ActiveConnIDs` and the `v2Broadcaster` type #699 deleted. |
| `docs/knowledge/features/v2-session-manager.md` | § "Concurrency-safe open-session enumeration (#588)" | the funnel's design rationale — the `V2StateOpen` gate, single-writer, returns-no-error. Read for context; **do not edit** (see § Handoff). |

**Line numbers in the ticket body are stale.** It was written against `2e33ffe`; every site it cites has since moved by roughly two hundred lines. Resolve every site by symbol name with `codegraph_search` / `git grep -F`, never by the ticket's line numbers.

---

## Context

`(*V2SessionManager).ActiveConnIDs` is a thin `[]string` projection over `ActiveConns` that drops the interactive flag. Its doc comment says it is reachable only from `internal/relay` tests until #572 delivers a production wire-up.

**#572 is CLOSED as NOT_PLANNED — the wire-up will never land.** Re-verified at this branch point, not merely inherited from the ticket body:

- `git grep -F 'ActiveConnIDs' -- '*.go'` returns hits in exactly three files: the decl plus two comments in `v2session.go`, one comment in `cmd/pyry/interactive_turn_v2.go`, and 33 lines in `v2session_test.go`. **Zero production call sites.**
- Every production fan-out consumer (`interactive_turn_v2.go`, `modal_resolve_v2.go`, `queue_state_v2.go`, `session_error_v2.go`, `session_transition_v2.go`) already calls `ActiveConns`.

**The deletion is the easy half; the test section is the risk.** Five tests are *named* for the projection, but four of them pin properties of the **snapshot funnel** — which `ActiveConns` still owns and production still uses. Deleting the section wholesale would trade a maintenance cost for real coverage loss on a live security gate.

This is a deletion plus a mechanical retarget. It introduces no new exported surface and warrants no ADR.

### Two facts that changed since the ticket body was written

1. **There are four out-of-section `ActiveConnIDs` sites, not three.** The ticket lists three; `TestV2Session_Push_ByteCeilingTearsDownSession` (added by #1505, after `2e33ffe`) is a fourth. It is in scope and handled below.
2. **The frozen-build-record list is 18 files, not 17.** `docs/specs/architecture/1505-push-queue-byte-ceiling.md` joined the set. Treatment is unchanged: frozen, never rewritten.

### Overlap check (§ 1.5)

`origin/feature/449` touches both `internal/relay/v2session.go` and `internal/relay/v2session_test.go`. **Verified not in-flight:** issue #449 is CLOSED as `NOT_PLANNED` (2026-05-17), its branch was never merged into `main`, and its four commits are abandoned. No block set; a `blockedBy` on a three-month-dead ticket would be noise. This is the known false-positive shape of the branch-based check — it is a strict superset of the PR-based one and therefore also sees abandoned branches.

---

## Design

### Decision 1 — no test helper; retarget each site in place

The obvious move is a `func activeConnIDs(mgr, ctx) []string` test helper replicating the deleted method. **Reject it.** Walking the sites shows only **one** actually wants a sorted `[]string`:

| Site | What it needs from the snapshot |
|---|---|
| `_OpenOnly` | the id **set**, sorted, compared to a literal → wants ids |
| `_TornDownSessionAbsent` | "does it contain this conn-id" → `slices.ContainsFunc` on `[]ActiveConn` |
| `_ConcurrentWithDispatch_RaceClean` | iterate, compare each id → read `c.ConnID` in the loop |
| `_EmptyManager` | `len(...) == 0` → no projection at all |
| `_CtxCancelled_ReturnsNil` | `... != nil` → no projection at all |
| `IdleChurn` | `len == 1 && [0] == connID` → read `conns[0].ConnID` |
| `Push_ByteCeilingTearsDownSession` | iterate, compare → read `c.ConnID` in the loop |

A helper for one site is over-engineering, and it would relocate the public method rather than delete it. Retarget each site directly.

### Decision 2 — `_OpenOnly` does **not** fold into `MixedInteractive`

The ticket leaves this to the architect. Keep them separate.

`_OpenOnly` injects four sessions covering `V2StateOpen` ×2, `V2StateHandshakeComplete`, **and `V2StateAwaitingInit`**. `MixedInteractive` injects no `AwaitingInit` session. `_OpenOnly` is therefore the **only** test in the tree covering the `AwaitingInit` arm of the `V2StateOpen` gate, and that gate is this ticket's `security-sensitive` surface. Folding would either drop that arm or bury a security assertion inside a capability test. Rename and retarget it; leave its four-state injection map intact.

### Decision 3 — the nil-vs-empty contract needs no replacement code

The deleted method carried a nil-guard so that a cancelled snapshot stayed `nil`, distinct from a non-nil-empty snapshot. **`ActiveConns` already provides both halves natively:**

- cancelled → the `ctx.Done()` arm returns a literal `nil`
- empty started manager → `handleActiveConns` returns `make([]ActiveConn, 0, len(m.sessions))`, non-nil

So `_EmptyManager` and `_CtxCancelled_ReturnsNil` assert **directly** on `ActiveConns` with no projection and no guard. The deleted guard was redundant with the survivor, which is the cleanest possible justification for deleting it.

### Production changes

`internal/relay/v2session.go`:

1. Delete `ActiveConnIDs` — declaration and doc comment. Nothing else in the file references it after step 2–3.
2. `snapshotReq` doc comment — drop the trailing clause naming `ActiveConnIDs` as a projection over the same reply. The remaining sentence about the reply carrying the capability-aware enumeration stays.
3. The `snapshot` field doc comment on `V2SessionManager` — drop the `(and ActiveConnIDs, which projects over it)` parenthetical. The funnel rationale around it stays.

`cmd/pyry/interactive_turn_v2.go`:

4. `interactiveBroadcaster` doc comment — delete the sentence distinguishing it from `#589`'s `v2Broadcaster` "which uses the capability-agnostic `ActiveConnIDs`". That sentence names two things that do not exist: `v2Broadcaster` was deleted by #699 along with `cmd/pyry/assistant_turn_v2.go`, and `ActiveConnIDs` goes here. **Keep** the surrounding sentences — the capability-aware framing, `*relay.V2SessionManager satisfies it`, and the declared-at-the-consumer rationale are all still true and load-bearing.

### Test changes — `internal/relay/v2session_test.go`

Rename the five section tests `TestV2Session_ActiveConnIDs_*` → `TestV2Session_ActiveConns_*`. All five target names are free (verified: no existing declaration collides). Update the section header comment to name `ActiveConns`; keep the `(#588)` tag — the ticket that introduced the funnel is still the right provenance.

**Keep the section where it is.** Do not move it next to `MixedInteractive`; relocating ~250 lines inflates the diff for no benefit and drags unrelated citations through the diff-scoped `cite-guard`.

Per-site dispositions:

| Symbol | Disposition |
|---|---|
| `TestV2Session_ActiveConnIDs_OpenOnly` | rename; build the id slice from `ActiveConns`, sort, compare to the same `want` literal. Injection map unchanged — all four states. |
| `..._TornDownSessionAbsent` | rename; both `slices.Contains` checks become a contains-by-`ConnID` predicate over `[]ActiveConn`. **Leave the AEAD-failure teardown scaffolding untouched** (the ciphertext byte-flip, the two-envelope wait, the `StatusProtocolMismatch` assertion). |
| `..._ConcurrentWithDispatch_RaceClean` | rename; the hammer loop iterates `ActiveConns` and compares `c.ConnID`. **Leave the pre-seal loop, the `waitForEnvelopes` count, and the decrypt-in-capture-order check untouched.** |
| `..._EmptyManager` | rename; assert `len(mgr.ActiveConns(ctx)) == 0`. |
| `..._CtxCancelled_ReturnsNil` | rename; assert `mgr.ActiveConns(ctx) == nil`. Run is still deliberately not started — that is the whole point (see AC4). |
| `TestV2Session_ActiveConns_MixedInteractive` | delete the trailing projection assertion (the `ActiveConnIDs` call, its sort, and its `slices.Equal` check). The `ActiveConns` flag assertion above it stays **byte-identical**. Fix the doc comment: drop the "`ActiveConnIDs` is an unchanged projection" sentence, and repoint the "mirrors `TestV2Session_ActiveConnIDs_OpenOnly`" reference at the renamed test. |
| `TestV2Session_IdleChurn_ReturnsToBaseline` | both sites onto `ActiveConns`: exactly one open conn whose `ConnID` is `connID` after each handshake, zero open after each idle sweep. |
| `TestV2Session_Push_ByteCeilingTearsDownSession` | the loop iterates `ActiveConns` and compares `c.ConnID`. Same property: the torn-down conn is no longer enumerated. |
| `buildMessageEnvelope` | doc comment: the envelope shape is no longer "what the #572 bridge will hand to `Push`". Rephrase against a live consumer — `cmd/pyry`'s fan-out emitters call `ActiveConns` then `Push` today. No `#572`, no `ActiveConnIDs`. |

**Import note:** the `slices` import stays. `MixedInteractive` loses its last `slices` use, but `_OpenOnly` keeps `slices.Sort`/`slices.Equal` and `_TornDownSessionAbsent` keeps a `slices` predicate. Do not chase a phantom unused import.

### Failure-message discipline

Assertion formats now print `[]ActiveConn` rather than `[]string`. `%v` on `ActiveConn` yields `{c-int true}` — conn-id plus a routing bool, both non-secret, matching what `handleActiveConns` documents the snapshot as carrying. **Never format a `*V2Session`** in a failure message: it reaches CipherStates and key material. Print conn-ids, or the `[]ActiveConn` slice.

---

## Concurrency model

Unchanged by this ticket. `ActiveConns` funnels a `snapshotReq` onto `Run`'s dispatch goroutine over the unbuffered `m.snapshot` channel; `handleActiveConns` performs the only map read, serialised by `Run`'s `select` against every map write. The caller's ctx is the escape arm on both the enqueue and the reply `select`.

The deleted projection ran wholly on the caller's goroutine and added no synchronisation, so removing it changes no happens-before edge. `_ConcurrentWithDispatch_RaceClean` loses one no-op stack frame and retains an identical race surface.

**The enqueue `ctx.Done()` arm is the load-bearing one, and it has exactly one test.** Because `m.snapshot` is unbuffered, that arm is the only thing preventing `ActiveConns` from blocking forever when `Run` is not draining — the state a caller racing `Run`'s exit lands in. AC4 is the enforcement that the retarget keeps it covered rather than merely compiling against it.

The **reply** `select`'s `ctx.Done()` arm is already uncovered at this branch point — PO measured its mutant green against the unmodified suite. **Out of scope:** no observed failure, and covering it needs a ctx that fires between enqueue and reply. Do not read AC4 as covering both arms.

---

## Error handling

No error paths change. `ActiveConns` returns no error by design (documented on the method: a snapshot has no failure a caller can act on; `nil` and empty are interchangeable for the broadcast consumer). The deletion removes no failure mode and adds none.

---

## Testing strategy

`make check` is the gate. Beyond it, AC4 needs a mutation proof, run **without writing the mutant into the worktree** — use Go's overlay so the committed tree stays clean:

1. Copy `internal/relay/v2session.go` to the scratchpad and delete the `case <-ctx.Done(): return nil` arm from `ActiveConns`'s **enqueue** `select` (the one whose other case is the `m.snapshot` send). Leave the reply `select` alone.
2. Write an overlay JSON mapping the real path to the scratch copy, both absolute: `{"Replace":{"<abs>/internal/relay/v2session.go":"<abs>/scratch/v2session_mutant.go"}}`
3. Run both of these under the same flags, with a short timeout so the hang reports fast:
   - `go test -overlay=<abs overlay.json> -race -timeout 60s ./internal/relay/` → **must FAIL** (the mutant blocks forever on the unbuffered send).
   - the same command plus `-skip '^TestV2Session_ActiveConns_CtxCancelled_ReturnsNil$'` → **must pass `ok`**.

The second run is the discriminator and is not optional. Run 1 alone passes vacuously if some *other* test happens to hang under the mutant; run 2 proves the retargeted test is the **sole** killer, which is the property PO measured before the change. Record both outcomes in the PR body.

Everything else is covered by the existing suite: the five retargeted tests, `MixedInteractive`'s untouched flag assertion, and `TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated` — which already asserts through `ActiveConns` and is therefore structurally incapable of being weakened by this change.

---

## Acceptance criteria

1. `(*V2SessionManager).ActiveConnIDs` and its doc comment are deleted, and the identifier `ActiveConnIDs` appears in no `.go` file in the tree (`git grep -F 'ActiveConnIDs' -- '*.go'` is empty).
2. Each property below is still pinned, measured through `ActiveConns`:

   | Test (post-rename) | Property |
   |---|---|
   | `TestV2Session_ActiveConns_OpenOnly` | on an injected map, `V2StateOpen` sessions are reported while **both** a `V2StateHandshakeComplete` and a `V2StateAwaitingInit` session are excluded |
   | `..._TornDownSessionAbsent` | a session that reached `V2StateOpen` and was then torn down by an AEAD-failure 4421 close is absent from the next snapshot |
   | `..._ConcurrentWithDispatch_RaceClean` | snapshots taken in a tight loop while sealed inbound frames drive dispatch on the same open session never report a foreign conn id, and the solicited replies still decrypt in seal order — under `-race` |
   | `..._EmptyManager` | a started manager holding zero sessions returns a `len 0` result without blocking |
   | `..._CtxCancelled_ReturnsNil` | with `Run` never started and an already-cancelled ctx, the call returns `nil` without blocking |

3. The four out-of-section sites are handled per § Design's disposition table — `MixedInteractive`'s projection assertion deleted with its own flag assertion left byte-identical and green; `IdleChurn`'s two sites and `Push_ByteCeilingTearsDownSession`'s loop rewritten onto `ActiveConns` asserting the same properties.
4. Deleting the `ctx.Done()` arm from `ActiveConns`'s **enqueue** `select` reddens `internal/relay`, **and** the same mutant with `TestV2Session_ActiveConns_CtxCancelled_ReturnsNil` skipped passes `ok` — the retargeted test is the sole killer, not merely a call that compiles.
5. No `.go` comment points readers at `ActiveConnIDs` or at #572's wire-up. Sites: the `snapshotReq` and `snapshot`-field comments in `v2session.go`, `interactiveBroadcaster` in `cmd/pyry/interactive_turn_v2.go`, and `buildMessageEnvelope` plus the `#588` section header in `v2session_test.go`.

### Explicitly out of scope

- The **reply** `select`'s `ctx.Done()` arm — already uncovered, no observed failure (see § Concurrency model).
- The 18 frozen per-ticket build records under `docs/knowledge/codebase/` and `docs/specs/architecture/`. They record what was true when those tickets shipped. **Do not rewrite them.**
- Any file under `.claude/worktrees/` — a stale worktree copy. `git grep` is untracked-blind by construction, so prefer it over `grep -r`, which false-hits there.

---

## Handoff to the documentation phase — NOT developer deliverables

The ticket's AC5 also lists two **evergreen** docs. Those belong to the documentation phase, which owns `docs/knowledge/`; they are deliberately **not** acceptance criteria here and the developer must not edit them. Enumerated so that phase has the list without re-deriving it:

`docs/knowledge/features/v2-session-manager.md` — hits in six sections, all needing a retarget onto `ActiveConns` rather than deletion (the funnel, the `V2StateOpen` gate, and the returns-no-error decision all survive):

- the title/summary line at the top of the file
- § "Surface" — the API listing carries the now-deleted signature
- § "Concurrency-safe open-session enumeration (#588)" — **the heading itself names `ActiveConnIDs`**, plus several body references
- § "Capability negotiation on the handshake (#626)" — two references to the projection
- § "Concurrency"
- § "Same-package unit tests" — the test-name list and the "passes unchanged" claim, both now false
- § "Out of scope (deferred)"
- § "Related" — three entries describing what #588/#589/#626 delivered

`docs/knowledge/INDEX.md` — the `v2-session-manager.md` row under § "Features".

---

## Open questions

None blocking. One judgement call is delegated: the exact replacement wording for `buildMessageEnvelope`'s doc comment. The constraint is fixed (no `#572`, no `ActiveConnIDs`, point at a live `ActiveConns`-plus-`Push` consumer); the phrasing is the developer's.

---

## Split proposal

Not required. Re-counted against this written spec:

| Boundary | Limit | This spec |
|---|---|---|
| Production source files created/modified | ≤ 3 | **2** (`internal/relay/v2session.go`, `cmd/pyry/interactive_turn_v2.go`) |
| Total written work | ≤ 400 | **~150** (~24 deleted + ~6 comment lines production; ~110 test lines touched) |
| New exported types or interfaces | ≤ 5 | **0** — this is a deletion |
| Consumer call sites needing simultaneous update | ≤ 10 | **10** — all `ActiveConnIDs` invocations, all in one file |
| Acceptance criteria | ≤ 5 | **5** |
| Distinct error/reject branches | ≤ 10 | **0** |

The call-site count sits exactly on the inclusive boundary. It is genuinely inside rather than argued inside: all ten live in `v2session_test.go`, so they cost one file read and sequential edits with no cross-package cascade, no import flips, and no test-fixture fan-out. `codegraph_impact` on `ActiveConnIDs` confirms no production dependent.

---

## Security review

**Verdict:** PASS

**Findings:**

- **[Trust boundaries] MUST FIX — addressed in this spec (Decision 2).** The trust boundary is the `s.state == V2StateOpen` filter in `handleActiveConns`: the single point deciding whether a peer is enumerable by the fan-out that `Push` delivers to. This ticket does not touch that filter, but it rewrites the filter's tests. `TestV2Session_ActiveConnIDs_OpenOnly` is the **only** test in the tree exercising the `V2StateAwaitingInit` arm of the gate — `TestV2Session_ActiveConns_MixedInteractive` injects a `V2StateHandshakeComplete` session but no `AwaitingInit` one. Folding the two, which the ticket body floats as an option, would silently drop that arm and leave a mid-handshake peer's exclusion unpinned. Resolved by prescribing no fold and an unchanged four-state injection map.
- **[Concurrency] MUST FIX — addressed in this spec (AC4).** `m.snapshot` is unbuffered, so the enqueue `select`'s `ctx.Done()` arm is the only thing preventing an unbounded block when `Run` is not draining. A retarget that merely compiles against `ActiveConns` — for instance by giving the cancellation test a running `Run` — would leave that arm uncovered while every test stays green, and a later refactor could deadlock any caller racing `Run`'s exit. Resolved by requiring the mutant to redden **and** the sole-killer bisect, so vacuous coverage is detectable rather than assumed.
- **[Tokens, secrets, credentials] No findings.** The enumeration carries conn-id strings plus the negotiated interactive bool and never a `*V2Session`, key material, or plaintext — stated on `handleActiveConns` and unchanged here. Deleting a `[]string` projection cannot widen the payload: `ActiveConns` already returns a strict superset, and every production consumer already reads it.
- **[Error messages, logs, telemetry] SHOULD FIX — guidance in § Failure-message discipline.** Every retargeted assertion's format string changes from `[]string` to `[]ActiveConn`. That value is non-secret, but a developer reaching for richer output could format a `*V2Session` instead, which reaches CipherStates. The spec directs failure messages at conn-ids; code review should confirm no session struct is formatted.
- **[Cryptographic primitives] No findings, with a constraint.** Two retargeted tests carry crypto scaffolding whose semantics are the assertion: `_TornDownSessionAbsent`'s ciphertext byte-flip drives a real AEAD failure, and `_ConcurrentWithDispatch_RaceClean` pre-seals on a single goroutine so nonces advance in order and replies decrypt in seal order. The spec confines both edits to the enumeration call and marks the scaffolding untouched; tidying either would weaken a nonce-integrity proof without failing.
- **[Threat model alignment] No findings.** The relevant threat — an unauthenticated or token-failed peer receiving server-initiated pushes — is proved by `TestV2Session_CapabilitySpoof_TokenFail_NeverEnumerated`, which already asserts through `ActiveConns` and is not touched by this ticket. It is therefore structurally incapable of being weakened here; verified by its absence from the `ActiveConnIDs` call-site sweep.
- **[File operations] Not applicable.** No filesystem path is read, written, or constructed anywhere in this change.
- **[Subprocess / external command execution] Not applicable.** No `exec.Command` and no environment handling in scope.
- **[Network & I/O] Not applicable.** No socket read, size cap, header validation, or TLS configuration is in scope; the snapshot funnel is an in-process channel handoff.

**Reviewer:** architect (self-review per the security-review checklist)
**Date:** 2026-08-20
