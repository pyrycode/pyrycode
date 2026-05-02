# Dispatcher closed-sweep — moving closed tickets to Done and cleaning up orphaned branches

The dispatcher (`pyrycode/agents`, sibling repo, gitignored from this
worktree) runs a periodic *closed-sweep* over the GitHub project board.
Two responsibilities:

1. Move closed issues whose project status is not yet `Done` into `Done`.
2. Delete the corresponding `feature/<N>` branch on origin if one exists.

Lives in `agents/dispatch/src/dispatch.ts` (`runClosedSweep`); the pure
"is this branch in the list?" predicate lives in `lib.ts`
(`findFeatureBranch`) so it can be unit-tested without GraphQL fixtures.

## Why two classes of stale branches

| Class | Trigger | Handled by |
|---|---|---|
| **Merged** | PR for `#N` merges, `feature/<N>` lingers | GitHub repo setting `delete_branch_on_merge` |
| **Split-parent** | PO splits `#N` → A+B; spec already committed to `feature/<N>`; no PR ever opened; PO closes parent via split-summary comment | Closed-sweep (this feature) |

The `delete_branch_on_merge` setting closes the merged class with no
dispatcher work. The split-parent class is exactly what the
closed-sweep already inspects — it sees the closed parent ticket and
moves it to `Done`. Deleting the orphaned branch is a small extension
of the same loop.

Twelve such branches accumulated over two weeks before the manual
cleanup on 2026-05-02 (`feature/19, 21, 27, 28, 29, 34, 35, 36, 38, 39,
40, 41, 45`) — the recurrence rate that motivated this ticket.

## Behaviour

Per sweep pass:

```
runClosedSweep
  ├── client.getClosedItemsNotInDone()       → list of tickets to move
  ├── if list empty: early return            → no GraphQL spent
  ├── client.listFeatureBranches()           → one query per pass; warn-and-skip on error
  └── for each closed item:
        ├── client.updateItemStatus(id, "Done")
        │     on failure: log warn, continue (skip the branch delete for this item)
        └── if findFeatureBranch(item.issueNumber, branchNames) matches:
              └── client.deleteRef(refId)    → log "🌿 Deleted stale branch feature/<N>"
                  on failure: log warn, continue
```

One list query plus one mutation per orphaned branch. The closed-set
is typically 0–2 per pass, so cost is trivial.

### Match contract

`findFeatureBranch(n, branchList)` returns `feature/<n>` iff that exact
string is in the list, otherwise `null`. **Exact equality** — `feature/45`
does **not** match `feature/450` or `feature/45-foo`. The unit tests
(`lib.test.ts`) lock in the prefix-collision case as the load-bearing
guard against silently deleting the wrong branch.

### Heartbeat output

Successful delete: `   🌿 Deleted stale branch feature/<N>`. List or
delete failures surface as `⚠️` warn lines but never abort the sweep —
status moves take priority over branch hygiene.

## GraphQL surface

Two new methods on `GitHubProjectClient`:

- `listFeatureBranches()` — `repository.refs(refPrefix: "refs/heads/feature/", first: 100)`, returns `{ name, id }[]`. Capped at 100; pagination is the documented escape hatch.
- `deleteRef(refId)` — standard `deleteRef` mutation, equivalent to `git push origin --delete <branch>` but in-band with the rest of the client's GraphQL traffic.

Both go through `this.gql`, same as every other client method. Not
unit-tested — thin GraphQL wrappers, same convention as the rest of
`github.ts`. The manual integration test (file dummy ticket → close →
observe sweep delete) is the verification.

## Failure posture

Branch deletion is **best-effort**. The closed-sweep's primary job is
keeping project status accurate; branch cleanup is the bonus.

| Failure | Behaviour |
|---|---|
| `listFeatureBranches` errors | Logged warn; status moves still proceed; this pass deletes nothing |
| Branch not in the listed set | `findFeatureBranch` returns null; silently skip |
| `updateItemStatus` fails | Log warn, `continue` — do **not** attempt the branch delete (status drift is the bigger problem) |
| `deleteRef` fails | Log warn, continue with next ticket |
| Closed-set is empty | Early return — skip the list query entirely |

### Retry gap (accepted)

Once a ticket is in `Done`, `getClosedItemsNotInDone` no longer returns
it, so a failed `deleteRef` is **never retried**. A failed delete leaves
the branch present forever on origin until an operator audit notices.

Considered and rejected: a second pass that walks every `feature/*` ref,
looks up the issue state, and reconciles. Adds complexity for a
recurrence rate that is observably low (12 branches in two weeks pre-fix
was bad enough to motivate this ticket; post-fix the failure rate of the
delete itself is rarer still). If retry gaps become a real problem,
that's a follow-up ticket.

## Out of scope (by design)

- **PR-active branches.** No PR guard before `deleteRef`. A closed ticket with an open PR is a process inconsistency we want surfaced, not papered over. The `delete_branch_on_merge` setting handles the merged path; manual closes shouldn't leave open PRs in the first place.
- **Local branches.** Dispatcher only knows about origin.
- **Non-`feature/<N>` branches.** Out of the helper's match contract.
- **The agents repo's own branches.** This sweep targets the configured `client.config.owner`/`config.repo` (i.e. `pyrycode/pyrycode`), not the agents repo.

## Concurrency

`runClosedSweep` is called serially from the dispatcher's main loop,
strictly before `runReworkRouting` and `runAutoAdvance`. No new
concurrency. Per-item status moves and deletes are awaited sequentially —
parallelism would not change correctness but the closed-set is tiny, so
the simpler shape wins.

## Related

- Spec: [docs/specs/architecture/58-dispatcher-closed-sweep-branch-delete.md](../../specs/architecture/58-dispatcher-closed-sweep-branch-delete.md)
- Ticket: [#58](https://github.com/pyrycode/pyrycode/issues/58)
