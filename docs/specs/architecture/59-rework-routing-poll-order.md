---
ticket: 59
title: Rework routing fires before per-agent dispatch on every poll cycle
size: XS
stack: TypeScript (dispatcher pipeline) — not Go
---

# Context

Manual recoveries that flip `needs-rework:<agent>` labels while the dispatcher is stopped are misrouted on restart. The first poll after restart scans the agent columns top-down, dispatches the column's default agent before `runReworkRouting` runs, and burns developer turns on a stale spec.

`#45` is the trigger case: at midnight, an operator removed `error:developer`/`wip:developer`/`size:m` and added `needs-rework:po`/`size:s` while the dispatcher was down. On restart at 08:04 UTC, the In Development scan picked up `#45`, found no developer-gating label (the dev gate doesn't watch `needs-rework:po`), and dispatched developer against the M-shaped spec from the prior failure. ~$2–3 in tokens, ~30 min recovery.

The decision logic — `decideReworkRoutes` in `agents/dispatch/src/lib.ts` — is correct and tested. The defect is purely poll-loop ordering in `pollLoop` (`agents/dispatch/src/dispatch.ts`, ~line 872): rework routing currently runs *after* the per-agent dispatch loop, so on a cold start the first dispatch beats the first move.

This is the third instance of "labels-set-during-downtime misroutes on restart." Prior two were caught by the rework-count circuit breaker and the file-overlap check. Both of those are *defenses against the consequences*; this fix removes the *cause*.

# Design

Single change, single file. `pollLoop` already calls `runReworkRouting(client)` and `runClosedSweep(client)` later in the iteration (post-dispatch maintenance). We add an additional invocation of both at the **top** of each `while (true)` iteration, before the `for (const agent of pollOrder)` loop.

Sketch (TypeScript, illustrative — actual line layout matches the file's existing style):

```ts
while (true) {
  // Reconcile board state from labels BEFORE dispatching anything.
  // Handles: labels set while dispatcher was stopped, or set by external
  // automation between polls. Idempotent — no-ops when nothing to route.
  await runReworkRouting(client);
  await runClosedSweep(client);

  for (const agent of pollOrder) {
    // ... existing per-agent dispatch logic, unchanged
  }

  // Existing post-dispatch maintenance calls remain.
  await runReworkRouting(client);
  await runClosedSweep(client);

  await sleep(pollIntervalMs);
}
```

Both functions are already idempotent (the decision functions short-circuit on no-op routes / no-op sweeps), so the extra invocation per cycle costs one GraphQL roundtrip when the board is clean. The closed-sweep is hoisted alongside for consistency: a ticket closed during downtime should also be reconciled before dispatch decisions are made on it.

## What does NOT change

- `decideReworkRoutes` signature, behavior, or tests — explicitly out of scope per AC #5.
- `runReworkRouting` body — only its position in the call sequence.
- `shouldSkipDispatch` and per-agent gating logic — unchanged. The fix relies on the column move (Backlog) happening before the column scan, not on a new gating label.
- Existing post-dispatch and end-of-cycle calls — kept. Extra calls are cheap and idempotent.

## Why hoist, not gate

Alternative considered: extend `shouldSkipDispatch` (or each agent's gating) to skip when *any* `needs-rework:*` label is present, regardless of which agent it targets. Rejected:

1. Mirrors the same logic that already lives in `decideReworkRoutes` — duplication, two places to keep in sync.
2. The natural reconciliation point is the move, not a skip. A ticket sitting in the wrong column with the right label should *move*, not just be ignored.
3. Larger blast radius for an XS fix. Hoisting is one line moved (well, one block duplicated); gating changes touch every agent's skip path.

The ticket's technical notes call this out directly ("Do not extract a new `findReworkRoute` helper"). The fix is ordering, not new logic.

## Why also hoist `runClosedSweep`

Ticket says "likely … for consistency." Same failure shape: a ticket closed during downtime (e.g., manually closed as wontfix) should be cleaned up before the next dispatch cycle reads its column. Same idempotent property. Same one-line change. Including it now avoids a follow-up ticket for the symmetric bug.

# Concurrency model

Unchanged. `pollLoop` is a single async loop; `runReworkRouting` and `runClosedSweep` already await sequentially. No new goroutines / promises / shared state. The added calls execute serially before the dispatch loop, in the same async context.

The only concurrency-adjacent invariant: between the hoisted `runReworkRouting` and the per-agent dispatch loop, no other code mutates board state (no other writer in this process; external writers are out of scope and are precisely the cause we're handling). The existing post-dispatch call still catches anything that happens *during* the dispatch loop.

# Error handling

Unchanged. `runReworkRouting` and `runClosedSweep` already handle their own errors per the existing implementation. If the hoisted call throws, behavior is identical to the existing post-dispatch call throwing — propagates to `pollLoop`'s existing handler. We add no new try/catch.

If the hoisted reconciliation succeeds but the dispatch loop later errors, the post-dispatch call still runs in the existing finally/cleanup path (verify against current code while editing). No regression in failure-mode coverage.

# Testing strategy

Per AC, the existing pure-decision surface (`decideReworkRoutes` in `lib.test.ts`) is the right place — the change under test is *ordering of calls already tested in isolation*, and the integration is too thin to warrant a new harness.

1. **AC #2** — Add (if missing) a `decideReworkRoutes` case: `{ column: "In Development", labels: ["needs-rework:po"] }` → route to Backlog. Confirm before adding; existing tests may already cover this exact pairing.
2. **AC #3** — Confirm (or add) a case: `{ column: "Backlog", labels: ["needs-rework:po"] }` → no route. Idempotency / self-loop skip.
3. **AC #4** — Manual smoke, recorded in the PR description, not automated:
   - Stop dispatcher.
   - Pick a low-stakes ticket in "In Development".
   - Add `needs-rework:po` label via CLI.
   - Restart dispatcher.
   - Observe: first action on the ticket is a column move to Backlog and a PO dispatch — not a developer dispatch.
   - Capture relevant log lines in the PR description.

No new integration test for the poll-loop ordering itself. An integration test would need a fake GraphQL client and a fake clock to simulate "labels set during downtime"; the test infrastructure does not exist for this surface, and standing it up is far outside an XS budget. The manual smoke is the verification.

# Open questions

- **Does the existing `decideReworkRoutes` test suite already cover the `In Development → Backlog` case for `needs-rework:po`?** Read `lib.test.ts` first. If yes, AC #2 is "no change, confirmed in PR description." If no, add the row to the existing table-driven cases. Same for AC #3.
- **Exact line for the hoist.** Ticket says "around line 872" — confirm during implementation; the existing post-dispatch invocation is the anchor to mirror at the top.
- **Ordering between the two hoisted calls.** Existing post-dispatch order is `runReworkRouting` then `runClosedSweep`; mirror that at the top. No known dependency between them, but staying consistent costs nothing.

# Acceptance mapping

| AC | Where addressed |
|----|-----------------|
| 1. `runReworkRouting` runs BEFORE per-agent loop on every poll | Hoist block at top of `while (true)` |
| 2. Test: `In Development` + `needs-rework:po` → Backlog | Confirm/add in `lib.test.ts` `decideReworkRoutes` cases |
| 3. Test: `Backlog` + `needs-rework:po` → no route | Confirm/add in `lib.test.ts` |
| 4. Manual smoke recorded | PR description, not code |
| 5. No change to `decideReworkRoutes` | Explicit non-goal — verify in diff |
