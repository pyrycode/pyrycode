# `Pool.AdoptAnnouncedID` (#2135)

The third sibling of `onRotate` and `RotateForNewSession` ([`sessions-package-key-types-pool.md`](sessions-package-key-types-pool.md)): where `onRotate` *observes* a self-rotation the rotation watcher already confirmed on disk, and `RotateForNewSession` *drives* one for a daemon-initiated `new_session`, `AdoptAnnouncedID` *adopts* one claude announced on its own stdout — the id already has a fresh `<id>.jsonl` claude opened itself, before this method is ever called.

```go
func (p *Pool) AdoptAnnouncedID(oldID, newID SessionID) error
```

**Body shape is `RotateForNewSession`'s, not `onRotate`'s**, and that is a deliberate divergence: one `Pool.mu` hold covers every check *and* the mutation, so it cannot delegate to `RotateID` the way `onRotate` does. The reason is the collision check below — it must sit inside the same critical section as `rekeyLocked`, or checking then mutating is a TOCTOU.

Contract, in order:

- `oldID` absent → `ErrSessionNotFound`, no mutation, no transition. This is the **expected**, not exceptional, outcome whenever the coexisting rotation watcher (`internal/sessions/rotation`, see [rotation-watcher.md](rotation-watcher.md)) wins the race for the same `/clear`: it already re-keyed, so `oldID` is gone by the time this call arrives. Both producers rotate to the *same* announced id and converge on `RotateID`/`rekeyLocked` under `Pool.mu`, so the loser's `ErrSessionNotFound` is what keeps a client-visible AC ("exactly one `session_transition`") structural rather than merely likely — see [the reset follower](streamsup-package-announced-reset-follower.md).
- `oldID == newID` → nil, **no re-key and no transition**. Copied from the trap `onRotate` has always had: `RotateID` checks membership *first* and only returns nil for `oldID == newID` afterward, so `onRotate`'s "rotate then notify unconditionally" shape draws a spurious delimiter on a no-op rotation. `AdoptAnnouncedID` must not repeat it, because unlike the watcher's input (a real file the OS created) an announced id arrives on every line of claude's own stdout and an equal-id announcement is a normal, frequent shape, not an edge case.
- `newID` already names a **different live session** → `ErrSessionIDTaken`, no mutation, no transition. `rekeyLocked` moves a map entry without checking the destination — that is fine for `onRotate` and `RotateForNewSession`, whose inputs are trusted (a watcher-confirmed disk fact, a daemon-minted UUID), but an announced id is one stdout line away from a hostile or confused claude naming a session it doesn't own. Refusing here, inside the lock, is what stops that line from silently overwriting another session's registry entry. This is a distinct sentinel from `ErrSessionNotFound` on purpose — the caller logs the two at different severities (Debug for the watcher-race case, Warn for a genuine collision), and folding them into one sentinel would bury the collision under the race's normal noise.
- Otherwise `rekeyLocked` + `saveLocked`, then `notifyTransition(ReasonClear)` off-lock — the same best-effort-durability posture as its siblings.
- Does **not** call `registerAllocatedUUIDLocked` — diverging from `RotateForNewSession` and matching `onRotate`. claude minted `<newID>.jsonl` itself; leaving the id un-allocated is how the rotation watcher still recognizes a real self-rotation, which is what keeps the two producers converging rather than double-counting.
- Does **not** arm `Runner.BeginRotation` ([`streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md`](streamsup-package-per-conversation-turn-busy-track-rotation-delivery-gate.md)). That gate exists for a daemon-initiated restart where a child is about to be killed and every turn must be refused until a successor binds. Here no child is replaced and claude keeps running; arming it would refuse turns for no reason.

## The refusal is only half the boundary

`ErrSessionIDTaken` closes the **registry** half of a collision: `rekeyLocked` never runs, so no other session's entry is overwritten. It does **not** and structurally *cannot* close the other half, because the pool has no reach into it: the caller (`sessionResetFollower`, [`streamsup-package-announced-reset-follower.md`](streamsup-package-announced-reset-follower.md)) holds a live tag on the runner that stamps every later event with a session id, and that tag is not `Pool`-owned state.

The first security review of this ticket audited exactly the registry half and stopped there — the tag half was found only in a second pass, after the caller had shipped an *unconditional* tag rotation that ran the same regardless of what `AdoptAnnouncedID` answered. A caller that rotates its own state unconditionally and only conditionally rotates the pool's turns a clean pool-side refusal into a half-enforced one: the pool correctly refused to adopt a colliding id, but the caller's tag still pointed at it, so the runner's later events were stamped with — and, downstream, delivered into — a conversation this rotation was never allowed to touch. The lesson generalises past this ticket: **a refusal returned across a package boundary is not a control until the caller's own state is unwound to match it.** An audit that stops at the refusing side has audited half a boundary.

## Testing

`transition_test.go` additions: known-id re-key fires exactly one `ReasonClear` transition with the right `PreviousID`/`NewID`; equal ids re-key nothing and fire nothing (the regression `onRotate` would produce); unknown `oldID` returns `ErrSessionNotFound` with no mutation (the watcher-raced path); a `newID` already owned by a different live session is refused with both sessions left exactly as they were.

See the ticket's spec at [`docs/specs/architecture/2135-follow-announced-reset.md`](../../specs/architecture/2135-follow-announced-reset.md) for the full design and security review, including the mutation evidence for each branch above.
