---
ticket: 58
title: Dispatcher closed-sweep — delete feature branches for closed tickets
status: draft
size: S
---

# Closed-sweep feature-branch deletion

## Context

**Repo note.** This change lives in `pyrycode/agents` (TypeScript dispatcher),
not `pyrycode/pyrycode` (Go supervisor). The agents repo is gitignored from
this worktree as a sibling. The spec is committed here per pipeline
convention; the developer applies it in `agents/dispatch/src/`.

Two classes of stale `feature/<N>` branches accumulate today:

1. **Merged** — PR for `#N` merges, branch lingers. GitHub's
   `delete_branch_on_merge` repo setting (just enabled) handles this going
   forward. No dispatcher work needed.
2. **Split-parent** — PO splits `#N` into children A+B. The architect
   already committed a spec to `feature/<N>`. No PR is ever opened. PO
   closes the parent via a split-summary comment. `feature/<N>` is
   orphaned forever.

Twelve such branches accumulated over two weeks (`feature/19, 21, 27, 28, 29,
34, 35, 36, 38, 39, 40, 41, 45`) before being cleaned up manually on
2026-05-02. This ticket prevents recurrence by extending the dispatcher's
existing closed-sweep — which already moves closed tickets to Done — to
also delete the corresponding feature branch on origin.

## Design

Two changes, four files:

| File | Change |
|---|---|
| `lib.ts` | Add pure `findFeatureBranch(n, branchList)` helper |
| `lib.test.ts` | Three table-driven tests for the helper |
| `github.ts` | Add `listFeatureBranches()` and `deleteRef(refId)` methods on `GitHubProjectClient` |
| `dispatch.ts` | Wire branch fetch + delete into `runClosedSweep` |

### Pure helper — `lib.ts`

```ts
/**
 * Return the entry from `branchList` that matches `feature/<n>` exactly,
 * or null if no such entry exists. Pure: no I/O, side-effect free.
 *
 * Match is case-sensitive on the branch name and uses exact equality —
 * `feature/45` does not match `feature/450` or `feature/45-foo`.
 */
export function findFeatureBranch(n: number, branchList: string[]): string | null {
  const target = `feature/${n}`;
  return branchList.includes(target) ? target : null;
}
```

That's the entire helper. The function exists chiefly to make the
"is the branch present?" decision unit-testable without GraphQL fixtures —
the AC mandates it, and exact-match is the safe default for a numeric
ticket key that could otherwise prefix-match an unrelated ticket.

### GraphQL methods — `github.ts`

Two new methods on `GitHubProjectClient`. Both use the existing
`this.gql` instance for consistency with the rest of the client.

```ts
/**
 * Return the names + node IDs of every ref under refs/heads/feature/.
 * The node ID is needed for deleteRef. Capped at 100 refs per call;
 * the closed-sweep deletes the universe down so this should not grow
 * unbounded in practice.
 */
async listFeatureBranches(): Promise<Array<{ name: string; id: string }>> {
  const result: any = await this.gql(`
    query($owner: String!, $repo: String!) {
      repository(owner: $owner, name: $repo) {
        refs(refPrefix: "refs/heads/feature/", first: 100) {
          nodes { name id }
        }
      }
    }
  `, {
    owner: this.config.owner,
    repo: this.config.repo,
  });
  // The `name` field returns the unqualified branch name (e.g. "feature/45")
  // when refPrefix is set — that's what callers compare against.
  return result.repository.refs.nodes;
}

/**
 * Delete a ref by node ID. Used by the closed-sweep to remove orphaned
 * feature branches. Caller is responsible for confirming the ref should
 * be deleted (closed-sweep guards via findFeatureBranch).
 */
async deleteRef(refId: string): Promise<void> {
  await this.gql(`
    mutation($refId: ID!) {
      deleteRef(input: { refId: $refId }) { clientMutationId }
    }
  `, { refId });
}
```

`deleteRef` is the standard GraphQL mutation for ref removal —
equivalent to `git push origin --delete <branch>` but in-band with the
rest of the client's GraphQL traffic.

### Wiring — `dispatch.ts`

Inline the branch fetch + delete into `runClosedSweep`. One list query
per sweep pass, then per-ticket lookups against the cached list.

```ts
async function runClosedSweep(client: GitHubProjectClient): Promise<void> {
  try {
    const closed = await client.getClosedItemsNotInDone();
    if (closed.length === 0) return;

    // Fetch feature/* refs once per sweep pass. If this fails the sweep
    // still moves tickets to Done; branch deletion is best-effort.
    let branches: Array<{ name: string; id: string }> = [];
    try {
      branches = await client.listFeatureBranches();
    } catch (e) {
      console.warn(`   ⚠️  Could not list feature branches; skipping branch cleanup: ${e}`);
    }
    const branchIdByName = new Map(branches.map(b => [b.name, b.id]));
    const branchNames = [...branchIdByName.keys()];

    for (const item of closed) {
      try {
        await client.updateItemStatus(item.id, "Done");
        console.log(`   ✓ Closed-sweep: moved #${item.issueNumber} (${item.status} → Done)`);
      } catch (e) {
        console.warn(`   ⚠️  Failed to move closed #${item.issueNumber} to Done: ${e}`);
        continue; // Don't try to delete the branch if the status move failed.
      }

      const match = findFeatureBranch(item.issueNumber, branchNames);
      if (!match) continue;
      const refId = branchIdByName.get(match);
      if (!refId) continue;
      try {
        await client.deleteRef(refId);
        console.log(`   🌿 Deleted stale branch ${match}`);
      } catch (e) {
        console.warn(`   ⚠️  Failed to delete ${match}: ${e}`);
      }
    }
  } catch (error: any) {
    console.error(`Error running closed-sweep: ${error.message}`);
  }
}
```

Imports already include `GitHubProjectClient`; add `findFeatureBranch`
to the `lib.js` import.

## Data flow

```
runClosedSweep
  ├── client.getClosedItemsNotInDone()       (existing)
  ├── client.listFeatureBranches()           (new, once per pass)
  └── for each closed item:
        ├── client.updateItemStatus(id, "Done")   (existing)
        └── if findFeatureBranch matches:
              └── client.deleteRef(refId)         (new)
```

One list query plus one mutation per orphaned branch. In steady state
the closed-set is small (typically 0–2 per pass), so the cost is
trivial; the list query exists primarily to give `findFeatureBranch`
something to filter and to surface the node ID `deleteRef` requires.

## Concurrency model

`runClosedSweep` is called serially from the dispatcher's main loop
(see `dispatch.ts:961`). It already runs strictly before
`runReworkRouting` and `runAutoAdvance`. No new concurrency. Per-item
status moves and deletes are awaited sequentially — parallelism would
not change correctness but the closed-set is tiny, so the simpler
shape wins.

## Error handling

Failure modes and behaviour:

| Failure | Behaviour |
|---|---|
| `listFeatureBranches` GraphQL error | Logged at warn; status moves still proceed; this pass deletes nothing |
| Branch not in the listed set (already deleted, never existed) | `findFeatureBranch` returns null; silently skip |
| `updateItemStatus` fails | Log warn, `continue` — do **not** attempt branch delete for that ticket (status drift is the bigger problem) |
| `deleteRef` fails (permission, race with another deleter) | Log warn, continue with next ticket |
| Closed-set is empty | Early return — skip the list query entirely |

Branch deletion is best-effort. The next sweep pass retries because
the ticket is already in Done by then but the branch still appears in
`listFeatureBranches`. We need a way to retry deletion without depending
on the "closed but not in Done" guard.

**Retry semantics — open question.** As written, once a ticket is in
Done, `getClosedItemsNotInDone` no longer returns it, so a failed
`deleteRef` is never retried. Two options:

- **Accept the gap.** A failed delete leaves the branch present forever
  on origin. Operator notices it on the next manual audit (the same
  audit that motivated this ticket). Simple but defeats the purpose.
- **Iterate over `feature/*` refs without an open ticket.** After the
  closed-sweep loop, walk every `feature/<N>` ref; if `#N` is closed
  (cheap GraphQL `issue(number: N) { state }` lookup) and there's no
  open PR, delete it. Catches retries and orphans from before this
  ticket landed.

**Recommendation:** ship the simple version (option 1), then evaluate.
The operator already has 12 stale branches cleaned up manually; the
recurrence rate is low and the next failure is easy to spot. If retries
become a real problem, option 2 is a follow-up ticket.

## Out of scope (by design)

- **PR-active branches.** A closed ticket with an open PR is a process
  inconsistency. The dispatcher's `delete_branch_on_merge` setting
  handles the merged path; manual closes shouldn't leave open PRs in
  the first place. We do **not** add a PR guard before `deleteRef` —
  if the operator closed the ticket while a PR was open, that's the
  inconsistency to surface, not paper over. (Open question if the
  operator wants belt-and-suspenders here.)
- **Local branches.** Dispatcher only knows about origin.
- **Non-`feature/<N>` branches.** Out of the helper's match contract.
- **The agents repo's own branches.** This change does not touch
  `pyrycode/agents` branches, only `pyrycode/pyrycode` (the configured
  `client.config.owner`/`config.repo`).

## Testing strategy

### Unit tests — `lib.test.ts`

Three table-driven cases for `findFeatureBranch`:

```ts
test("findFeatureBranch", () => {
  const cases = [
    {
      name: "branch present",
      n: 45,
      branches: ["feature/41", "feature/45", "feature/72"],
      want: "feature/45",
    },
    {
      name: "branch absent",
      n: 99,
      branches: ["feature/41", "feature/45", "feature/72"],
      want: null,
    },
    {
      name: "list contains unrelated feature/<x> entries (no prefix match)",
      n: 4,
      branches: ["feature/41", "feature/45", "feature/450"],
      want: null,
    },
  ];
  for (const c of cases) {
    assert.strictEqual(findFeatureBranch(c.n, c.branches), c.want, c.name);
  }
});
```

The third case is the load-bearing one: it locks in exact-match against
the prefix-collision risk that would silently delete the wrong branch.

### Integration test — manual

Documented in the AC; run once to validate the wiring:

1. Open a dummy ticket. Add `ready:po` so the architect picks it up.
2. Architect commits a spec to `feature/<N>`, branch lands on origin.
3. Close the ticket manually (no PR opened).
4. Wait for the next dispatcher pass (or trigger one).
5. Verify `gh api repos/pyrycode/pyrycode/git/refs/heads/feature/<N>`
   returns 404, and the dispatcher log shows
   `🌿 Deleted stale branch feature/<N>`.

`github.ts` methods are not unit-tested — they're thin GraphQL
wrappers, same convention as the existing client methods. The manual
integration test is the verification.

## Open questions

1. **Retry semantics.** Accept the gap for failed `deleteRef`, or add a
   second pass that walks `feature/*` refs and reconciles against issue
   state? Recommendation: ship simple, revisit if recurrence is observed.
2. **PR-active guard.** Skip `deleteRef` when an open PR targets the
   branch? Recommendation: no — surface the inconsistency. The operator
   can override at any time by deleting branches manually.
3. **100-ref cap.** `listFeatureBranches` uses `first: 100`. If the
   project ever accumulates >100 `feature/*` refs simultaneously, older
   branches go invisible to the sweep. Same constraint as everywhere
   else in `github.ts`; pagination is the documented escape hatch. No
   action needed today.
